/**
 * Per-run temp root (plan §9.6 rule 2, V12).
 *
 * Every temp path the package creates lives under ONE directory per run:
 * `<base>/<run-id>/`, where `<base>` is the `tmp.root` config value when the caller passes it and
 * `<os.tmpdir()>/code-forge` otherwise. The run root holds `owner.json` (`{pid, start_time,
 * created_at}` of the process that created it) and the pid registry `pids/` that `exec` fills.
 * A killed run therefore leaves one directory, not thousands, and the next `run start` (or the
 * test preload) calls `sweepRoots()`, which reaps the dead run's registered children
 * (`./reaper.mjs`) and removes the directory. A root whose owner is alive, or whose owner cannot
 * be read, is kept.
 *
 * Trust rule: the base directory and every run root must be a real directory (not a symlink)
 * owned by this user and not writable by group or others. On a shared `/tmp` another user could
 * otherwise plant a root with `pids/` entries and make the reaper signal our processes. `runRoot`
 * throws on an untrusted directory; `sweepRoots` reports it as `unknown` and never reaps it.
 * `owner.json` is written atomically (temp file in the root, then rename), so a parallel sweep
 * reads either the previous owner record or the new one, never a truncated one. An owner file
 * that exists but cannot be parsed, or names an invalid pid, is `unknown` and never reaped. Only a
 * root with NO owner file (ENOENT) uses the ownerless path: it is removed once nothing a live run
 * touches (the root, `pids/`, its entries) has changed for `ownerlessGraceMs` (a root being
 * created right now has no owner file for a moment).
 *
 * Claim rule: a sweep never reaps a root in place. It first renames the root to a tombstone
 * (`.reaping-<random>`, a name no run id can ever take: RUN_ID rejects a leading `.`), re-reads
 * the owner on the renamed path, and only then reaps and removes it. A run that re-adopted the
 * same run id in between (a resumed run) therefore either owns a fresh directory (its
 * `runRoot` retries after the rename) or is found alive on the tombstone, which is put back.
 *
 * A process that never picked a run root gets a lazy one (`proc-<pid>-<ms>`) the first time
 * `currentRunRoot()` is called, so `exec` always has somewhere to register its children. The lazy
 * root lives under the base named by `CODE_FORGE_TMP_ROOT` when that is an absolute path (the CLI
 * router sets it from the project's `tmp.root`, B19), else under `<os.tmpdir()>/code-forge`. On a
 * normal process exit the lazy root is removed when its `pids/` holds no live entry
 * (`releaseLazyRoot`); a root with a live child, or an entry it cannot read, is kept for the sweep.
 */

import { randomBytes } from 'node:crypto';
import { lstatSync, mkdirSync, readdirSync, readFileSync, realpathSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { isAlive, listEntries, readStartTime, sweep, UNKNOWN_START_TIME } from './reaper.mjs';

/** Name of the directory under `os.tmpdir()` used when no `tmp.root` is configured. */
export const DEFAULT_BASE_NAME = 'code-forge';

/** File inside a run root naming the process that owns it. */
export const OWNER_FILE = 'owner.json';

/** A run id is one plain path segment: no separators, no `.`/`..`. */
const RUN_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

/** An ownerless root younger than this is being created, not abandoned. */
const DEFAULT_OWNERLESS_GRACE_MS = 10 * 60 * 1000;

/** Prefix of a root claimed by a sweep; starts with `.` so `RUN_ID` can never produce it. */
const TOMBSTONE_PREFIX = '.reaping-';

/** Environment variable naming the base directory when no `root` is passed (the CLI's `tmp.root`). */
export const TMP_ROOT_ENV = 'CODE_FORGE_TMP_ROOT';

/** @type {string | null} */
let current = null;

/** The lazy root this process created (`proc-<pid>-<ms>`), or null. */
/** @type {string | null} */
let lazy = null;
let exitHooked = false;

/**
 * Why `dir` is not a trusted directory, or null when it is: must exist, not be a symlink, be
 * owned by this user and have none of the `mask` permission bits set (default: no group/other
 * write bit; pass `0o077` for a directory that must be private).
 * @param {string} dir
 * @param {number} [mask]
 * @returns {string | null}
 */
export function untrustedReason(dir, mask = 0o022) {
  let st;
  try {
    st = lstatSync(dir);
  } catch {
    return 'missing';
  }
  if (st.isSymbolicLink()) return 'symlink';
  if (!st.isDirectory()) return 'not a directory';
  if (typeof process.getuid === 'function' && st.uid !== process.getuid()) return 'foreign owner';
  if ((st.mode & mask) !== 0) return mask === 0o022 ? 'group/other writable' : 'group/other accessible';
  return null;
}

/**
 * Write `<dir>/owner.json` atomically: a fresh temp file in the same directory, then rename over
 * the final name. On any failure the temp file is removed, so no partial file is ever left.
 * @param {string} dir
 * @param {{pid: number, start_time: string, created_at: string}} owner
 */
function writeOwnerAtomic(dir, owner) {
  const temp = path.join(dir, `.${OWNER_FILE}.${process.pid}.${randomBytes(6).toString('hex')}.tmp`);
  try {
    writeFileSync(temp, `${JSON.stringify(owner)}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temp, path.join(dir, OWNER_FILE));
  } catch (err) {
    rmSync(temp, { force: true });
    throw err;
  }
}

/** @param {string} dir @param {string} what */
function assertTrusted(dir, what) {
  const reason = untrustedReason(dir);
  if (reason !== null) throw new Error(`tmp: refusing untrusted ${what} (${reason}): ${dir}`);
}

/**
 * The directory run roots live in.
 * @param {string} [root] - the `tmp.root` config value; must be absolute when given.
 * @returns {string}
 */
export function tmpBase(root) {
  if (root === undefined || root === null) {
    const fromEnv = process.env[TMP_ROOT_ENV];
    if (typeof fromEnv === 'string' && path.isAbsolute(fromEnv)) return fromEnv;
    return path.join(os.tmpdir(), DEFAULT_BASE_NAME);
  }
  if (typeof root !== 'string' || !path.isAbsolute(root)) {
    throw new TypeError('tmp: tmp.root must be an absolute path');
  }
  return root;
}

/**
 * Create (if needed) the run root `<base>/<runId>/`, record this process as its owner, and
 * return its real absolute path.
 * @param {string} runId
 * @param {{root?: string}} [opts]
 * @returns {string}
 */
export function runRoot(runId, opts = {}) {
  if (typeof runId !== 'string' || !RUN_ID.test(runId)) {
    throw new TypeError('tmp: runId must be one path segment of letters, digits, ".", "_" or "-"');
  }
  const base = tmpBase(opts.root);
  const dir = path.join(base, runId);
  // A parallel sweep that claimed a dead root of this name renames it away (see the claim rule)
  // between our mkdir and our write: the directory is then "missing" or the write fails with
  // ENOENT, and the retry creates a fresh directory in its place. A tombstone itself can never be
  // adopted here: its name starts with `.`, which RUN_ID rejects above.
  for (let attempt = 0; ; attempt++) {
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    assertTrusted(base, 'tmp base');
    const reason = untrustedReason(dir);
    if (reason === 'missing' && attempt < 2) continue;
    if (reason !== null) throw new Error(`tmp: refusing untrusted run root (${reason}): ${dir}`);
    try {
      writeOwnerAtomic(dir, {
        pid: process.pid,
        start_time: readStartTime(process.pid) ?? UNKNOWN_START_TIME,
        created_at: new Date().toISOString(),
      });
      return realpathSync(dir);
    } catch (err) {
      if (/** @type {NodeJS.ErrnoException} */ (err).code !== 'ENOENT' || attempt >= 2) throw err;
    }
  }
}

/**
 * Make `dir` this process's run root (`null` forgets it; the next `currentRunRoot()` makes a lazy one).
 * @param {string | null} dir
 */
export function setRunRoot(dir) {
  if (dir !== null && (typeof dir !== 'string' || !path.isAbsolute(dir))) {
    throw new TypeError('tmp: a run root must be an absolute path');
  }
  current = dir;
}

/** @returns {string} this process's run root, created lazily as `proc-<pid>-<ms>`. */
export function currentRunRoot() {
  if (current === null) {
    current = runRoot(`proc-${process.pid}-${Date.now()}`);
    lazy = current;
    if (!exitHooked) {
      exitHooked = true;
      process.once('exit', () => {
        releaseLazyRoot();
      });
    }
  }
  return current;
}

/**
 * Remove this process's lazy root when its pid registry holds no live entry: every file in
 * `pids/` must be a well-formed entry whose pid is dead. Anything else (a live child, an entry
 * that cannot be read) keeps the root for the next sweep. Called from the `exit` hook, so it NEVER
 * throws: any error keeps the root and returns false.
 * @param {{rmSync?: typeof rmSync, listEntries?: typeof listEntries}} [deps] - test seams.
 * @returns {boolean} true when the lazy root was removed.
 */
export function releaseLazyRoot(deps = {}) {
  try {
    if (lazy === null) return false;
    const dir = lazy;
    const pids = path.join(dir, 'pids');
    const names = safeReaddir(pids);
    const entries = (deps.listEntries ?? listEntries)(pids);
    if (entries.length !== names.length || entries.some((e) => isAlive(e.pid))) return false;
    (deps.rmSync ?? rmSync)(dir, { recursive: true, force: true });
    if (current === dir) current = null;
    lazy = null;
    return true;
  } catch {
    return false;
  }
}

/**
 * The pid-registry directory of a run root.
 * @param {string} [root] - defaults to `currentRunRoot()`.
 * @returns {string}
 */
export function pidsDir(root) {
  return path.join(root ?? currentRunRoot(), 'pids');
}

/**
 * The state of a run root's recorded owner:
 *  - `alive`: the recorded pid is a live process with the recorded start time;
 *  - `dead`: gone, or its pid now names another process;
 *  - `missing`: there is no owner file (ENOENT) — the root is being created, or was abandoned;
 *  - `invalid`: an owner file exists but is unreadable, not JSON, or names an invalid pid.
 * @param {string} dir
 * @returns {'alive' | 'dead' | 'missing' | 'invalid'}
 */
export function ownerState(dir) {
  let text;
  try {
    text = readFileSync(path.join(dir, OWNER_FILE), 'utf8');
  } catch (err) {
    return /** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT' ? 'missing' : 'invalid';
  }
  let owner;
  try {
    owner = JSON.parse(text);
  } catch {
    return 'invalid';
  }
  if (!owner || !Number.isInteger(owner.pid) || owner.pid <= 1) return 'invalid';
  if (!isAlive(owner.pid)) return 'dead';
  if (typeof owner.start_time === 'string' && owner.start_time !== UNKNOWN_START_TIME) {
    const live = readStartTime(owner.pid);
    if (live !== null && live !== owner.start_time) return 'dead';
  }
  return 'alive';
}

/**
 * Whether the recorded owner of a run root is still the same live process.
 * `true` = alive, `false` = dead, `null` = unknown (owner file missing or invalid).
 * @param {string} dir
 * @returns {boolean | null}
 */
export function ownerAlive(dir) {
  const state = ownerState(dir);
  return state === 'alive' ? true : state === 'dead' ? false : null;
}

/**
 * Remove sibling run roots whose owner is dead, after reaping their registered children.
 * This process's own root is never touched; untrusted roots (and every root of an untrusted
 * base) and roots with an invalid owner file are `unknown` and never reaped; a root with no
 * owner file is removed only once its last activity is older than `ownerlessGraceMs`. Every
 * reap goes through the claim rule (rename to a tombstone, re-check, then reap); tombstones
 * left by a killed sweep are visited whatever the `prefix`.
 * @param {{root?: string, prefix?: string, graceMs?: number, ownerlessGraceMs?: number}} [opts] -
 *   `root` is the base directory (default `tmpBase()`); `prefix` limits the sweep to entries
 *   starting with it.
 * @returns {Promise<{removed: string[], kept: string[], unknown: string[]}>} absolute paths.
 */
export async function sweepRoots(opts = {}) {
  const base = opts.root ?? tmpBase();
  const prefix = opts.prefix ?? '';
  const ownerlessGraceMs = opts.ownerlessGraceMs ?? DEFAULT_OWNERLESS_GRACE_MS;
  /** @type {{removed: string[], kept: string[], unknown: string[]}} */
  const out = { removed: [], kept: [], unknown: [] };
  /** @type {import('node:fs').Dirent[]} */
  let entries;
  try {
    entries = readdirSync(base, { withFileTypes: true }).sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  } catch {
    return out;
  }
  const own = current === null ? null : path.resolve(current);
  const baseTrusted = untrustedReason(base) === null;
  for (const entry of entries) {
    if (!(entry.name.startsWith(prefix) || entry.name.startsWith(TOMBSTONE_PREFIX))) continue;
    if (!(entry.isDirectory() || entry.isSymbolicLink())) continue;
    const dir = path.join(base, entry.name);
    if (own !== null && (dir === own || safeRealpath(dir) === own)) continue;
    if (!baseTrusted || untrustedReason(dir) !== null) {
      out.unknown.push(dir);
      continue;
    }
    const state = ownerState(dir);
    if (state === 'alive') {
      out.kept.push(dir);
      continue;
    }
    if (state !== 'dead' && !(state === 'missing' && Date.now() - lastActivityMs(dir) > ownerlessGraceMs)) {
      out.unknown.push(dir);
      continue;
    }
    // Claim, then re-check on the claimed path: a run that re-adopted this run id between the
    // check above and the rename is alive on the tombstone and gets its root back, untouched.
    const tomb = path.join(base, `${TOMBSTONE_PREFIX}${randomBytes(6).toString('hex')}`);
    try {
      renameSync(dir, tomb);
    } catch {
      continue; // already gone (removed by another sweep)
    }
    if (ownerState(tomb) === 'alive') {
      unclaim(tomb, dir);
      out.kept.push(dir);
      continue;
    }
    await sweep(path.join(tomb, 'pids'), { graceMs: opts.graceMs });
    rmSync(tomb, { recursive: true, force: true });
    out.removed.push(dir);
  }
  return out;
}

/**
 * Undo a claim: put the tombstone back at `dir`. When the re-adopting run already re-created
 * `dir` (its first spawn makes `pids/` again), the rename fails; its owner record and pid
 * entries are then moved into the re-created directory and the tombstone is removed, so the
 * live run's root is never left ownerless.
 * @param {string} tomb
 * @param {string} dir
 */
function unclaim(tomb, dir) {
  try {
    renameSync(tomb, dir);
    return;
  } catch {
    // `dir` exists again and is not empty: merge below
  }
  const pids = path.join(dir, 'pids');
  mkdirSync(pids, { recursive: true, mode: 0o700 });
  for (const name of safeReaddir(path.join(tomb, 'pids'))) {
    try {
      renameSync(path.join(tomb, 'pids', name), path.join(pids, name));
    } catch {
      // unregistered meanwhile
    }
  }
  try {
    renameSync(path.join(tomb, OWNER_FILE), path.join(dir, OWNER_FILE));
  } catch {
    // no owner record on the tombstone
  }
  rmSync(tomb, { recursive: true, force: true });
}

/** @param {string} p @returns {string[]} entry names, or [] when unreadable. */
function safeReaddir(p) {
  try {
    return readdirSync(p);
  } catch {
    return [];
  }
}

/** @param {string} p @returns {string | null} */
function safeRealpath(p) {
  try {
    return realpathSync(p);
  } catch {
    return null;
  }
}

/** @param {string} p @returns {number | null} modification time, or null when unreadable. */
function mtimeMs(p) {
  try {
    return lstatSync(p).mtimeMs;
  } catch {
    return null;
  }
}

/**
 * When a run last touched this root: the newest mtime of the root itself, its `pids/` directory
 * and every entry in it (each spawn writes one). An unreadable root counts as touched now.
 * @param {string} dir
 * @returns {number}
 */
function lastActivityMs(dir) {
  let newest = mtimeMs(dir);
  if (newest === null) return Date.now();
  const pids = path.join(dir, 'pids');
  for (const p of [pids, ...safeReaddir(pids).map((name) => path.join(pids, name))]) {
    const m = mtimeMs(p);
    if (m !== null && m > newest) newest = m;
  }
  return newest;
}
