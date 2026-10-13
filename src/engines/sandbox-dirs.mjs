/**
 * The Codex coder sandbox profile (block B93): `coder.sandbox.{profile, add_dirs}`.
 *
 * Facts (Codex 0.155.1, `test/fixtures/help/codex-0.155.1.txt`): `--add-dir <DIR>` = "Additional
 * directories that should be writable alongside the primary workspace"; `-s` takes `read-only`,
 * `workspace-write` or `danger-full-access`.
 *
 * Profiles:
 *  - `workspace` (the default, and what no `coder` key means): today's argv, `-s workspace-write`
 *    with the project `-C <cwd>` as the only writable root.
 *  - `toolchain`: the same, plus one `--add-dir <abs>` per `add_dirs` entry (a build cache, a
 *    package store), right after `-s workspace-write`.
 *  - `full`: `-s danger-full-access`. Codex writes anywhere the user can; like the Claude coder
 *    (bypassPermissions), the forbidden list, the transcript grep and the gates are the limits.
 *
 * Every `add_dirs` entry is absolute or starts with `~/` (expanded against the process HOME). It is
 * resolved component by component: each symlink is replaced by its target (bounded; a loop is
 * refused), `..` steps up from the resolved dir, a missing tail is kept as written, and the
 * existing part is the native realpath (true case). So a symlink, even a dangling one, is judged
 * by its target. Matching folds case on macOS and Windows. An entry is REFUSED (`sandbox-dir-refused`) when it is `/`, the home dir itself, or is,
 * contains or sits inside: `~/.code-forge`, `~/.codex` (and `$CODEX_HOME`), `~/.ssh`, `~/.gnupg`,
 * `~/.aws`, `~/.config`, `~/Library/Keychains`, the run temp base / run temp root, or the session
 * CODEX_HOME. A refusal names the key path (`coder.sandbox.add_dirs[<i>]`), never the value.
 *
 * Pure apart from `lstat`/`readlink`/`realpath`; it never creates, lists or writes a directory.
 */

import { lstatSync, readlinkSync, realpathSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

/** The profiles, in the order the docs list them. */
export const SANDBOX_PROFILES = Object.freeze(['workspace', 'toolchain', 'full']);

/** The default profile: today's argv. */
export const DEFAULT_SANDBOX_PROFILE = 'workspace';

/** The error code a refused `add_dirs` entry carries (validation rule and builder). */
export const SANDBOX_DIR_REFUSED = 'sandbox-dir-refused';

/** Home-relative directories no coder sandbox may open (nor any dir that holds or sits in one). */
export const PROTECTED_HOME_DIRS = Object.freeze(['.code-forge', '.codex', '.ssh', '.gnupg', '.aws', '.config', path.join('Library', 'Keychains')]);

/** An `Error` with a `code` of {@link SANDBOX_DIR_REFUSED}. */
export class SandboxDirError extends Error {
  /** @param {string} message */
  constructor(message) {
    super(message);
    this.name = 'SandboxDirError';
    this.code = SANDBOX_DIR_REFUSED;
  }
}

/** The most symlinks one entry may pass through before it is refused (as the kernel's ELOOP). */
const MAX_LINK_HOPS = 40;

/** A path whose symlinks cannot be followed (a loop, too many hops, an unreadable link). */
class Unresolvable extends Error {}

/** @param {string} p @returns {string} the native realpath (true case), or `p` when it fails. */
function native(p) {
  try {
    return realpathSync.native(p);
  } catch {
    return p;
  }
}

/**
 * Resolve `p` the way the kernel would: walk it one component at a time with `lstat`; a symlink is
 * replaced by its target (read with `readlink`, followed again, bounded); `..` steps up from the
 * resolved dir, never lexically. A missing component ends the walk: the existing part is the
 * native realpath, the rest is kept as written. So a dangling symlink is judged by its target.
 * @param {string} p - absolute.
 * @param {{n: number}} [hops]
 * @returns {string}
 * @throws {Unresolvable} on a loop, too many hops, or a component that cannot be read.
 */
function resolvePath(p, hops = { n: 0 }) {
  const { root } = path.parse(p);
  const parts = p.slice(root.length).split(path.sep).filter((c) => c.length > 0 && c !== '.');
  let cur = root;
  for (let i = 0; i < parts.length; i += 1) {
    if (parts[i] === '..') {
      cur = path.dirname(cur);
      continue;
    }
    const next = path.join(cur, parts[i]);
    let st;
    try {
      st = lstatSync(next);
    } catch (err) {
      const code = /** @type {NodeJS.ErrnoException} */ (err).code;
      if (code === 'ENOENT' || code === 'ENOTDIR') return path.join(native(cur), ...parts.slice(i));
      throw new Unresolvable();
    }
    if (!st.isSymbolicLink()) {
      cur = next;
      continue;
    }
    hops.n += 1;
    if (hops.n > MAX_LINK_HOPS) throw new Unresolvable();
    let target;
    try {
      target = readlinkSync(next);
    } catch {
      throw new Unresolvable();
    }
    cur = resolvePath(path.isAbsolute(target) ? target : `${cur}${path.sep}${target}`, hops);
  }
  return native(cur);
}

/** @param {string} p @returns {string} a refused tree, resolved; a tree that cannot be is kept as written. */
function resolveTree(p) {
  try {
    return resolvePath(path.resolve(p));
  } catch {
    return path.resolve(p);
  }
}

/** Case-insensitive by default on macOS and Windows: compare folded there, so a missing tree is matched too. */
const FOLD_CASE = process.platform === 'darwin' || process.platform === 'win32';

/** @param {string} p @returns {string} */
function fold(p) {
  return FOLD_CASE ? p.toLowerCase() : p;
}

/** @param {string} a @param {string} b @returns {boolean} `a` is `b` (both resolved; case folded where the FS is). */
function same(a, b) {
  return fold(a) === fold(b);
}

/** @param {string} a @param {string} b @returns {boolean} `a` is `b` or inside it (both resolved). */
function within(a, b) {
  const rel = path.relative(fold(b), fold(a));
  if (rel === '') return true;
  return !(rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel));
}

/** @param {NodeJS.ProcessEnv} env @returns {string} */
function homeOf(env) {
  return typeof env.HOME === 'string' && env.HOME.length > 0 ? env.HOME : os.homedir();
}

/**
 * Expand one entry: absolute, or `~/…` against `home`. Anything else is `null`. `.` and `..` are
 * kept as written: the resolver steps through them after following symlinks.
 * @param {unknown} entry @param {string} home @returns {string | null}
 */
export function expandSandboxDir(entry, home) {
  if (typeof entry !== 'string' || entry.length === 0 || entry.includes('\0')) return null;
  if (entry.startsWith('~/')) return `${path.resolve(home)}${path.sep}${entry.slice(2)}`;
  if (path.isAbsolute(entry)) return entry;
  return null;
}

/**
 * @typedef {object} SandboxGuards
 * @property {NodeJS.ProcessEnv} [env] - the env whose HOME / CODEX_HOME count (default `process.env`).
 * @property {ReadonlyArray<string | null | undefined>} [extra] - more refused trees: the run temp
 *   base or root, the session CODEX_HOME (absolute paths; `null`/`undefined` are skipped).
 */

/**
 * The refused trees for `guards`, each resolved once.
 * @param {SandboxGuards} guards
 * @returns {{home: string, realHome: string, protectedTrees: string[], runTrees: string[]}}
 */
function refusedTrees(guards) {
  const env = guards.env ?? process.env;
  const home = homeOf(env);
  const protectedTrees = PROTECTED_HOME_DIRS.map((d) => resolveTree(path.join(home, d)));
  if (typeof env.CODEX_HOME === 'string' && path.isAbsolute(env.CODEX_HOME)) protectedTrees.push(resolveTree(env.CODEX_HOME));
  /** @type {string[]} */
  const runTrees = [];
  for (const p of guards.extra ?? []) {
    if (typeof p === 'string' && path.isAbsolute(p)) runTrees.push(resolveTree(p));
  }
  return { home, realHome: resolveTree(home), protectedTrees, runTrees };
}

/**
 * Check one `add_dirs` entry against resolved trees.
 * @param {unknown} entry @param {number} index @param {ReturnType<typeof refusedTrees>} trees
 * @returns {{ok: true, dir: string} | {ok: false, keyPath: string, why: string}}
 */
function checkOne(entry, index, trees) {
  const keyPath = `coder.sandbox.add_dirs[${index}]`;
  const expanded = expandSandboxDir(entry, trees.home);
  if (expanded === null) return { ok: false, keyPath, why: 'it must be an absolute path or start with ~/' };
  let real;
  try {
    real = resolvePath(expanded);
  } catch {
    return { ok: false, keyPath, why: 'its symlinks cannot be resolved' };
  }
  if (real === path.parse(real).root) return { ok: false, keyPath, why: 'it is the filesystem root' };
  if (same(real, trees.realHome)) return { ok: false, keyPath, why: 'it is the home directory' };
  if (within(trees.realHome, real)) return { ok: false, keyPath, why: 'it holds the home directory' };
  if (trees.protectedTrees.some((t) => within(real, t) || within(t, real))) {
    return { ok: false, keyPath, why: 'it is, holds or sits inside a protected directory (~/.code-forge, ~/.codex, ~/.ssh, ~/.gnupg, ~/.aws, ~/.config, ~/Library/Keychains)' };
  }
  if (trees.runTrees.some((t) => within(real, t) || within(t, real))) {
    return { ok: false, keyPath, why: 'it is, holds or sits inside the run temp root or the session CODEX_HOME' };
  }
  return { ok: true, dir: real };
}

/**
 * Every refused `add_dirs` entry (validation reports each one).
 * @param {unknown} dirs @param {SandboxGuards} [guards]
 * @returns {Array<{keyPath: string, why: string}>}
 */
export function sandboxDirRefusals(dirs, guards = {}) {
  if (dirs === undefined) return [];
  if (!Array.isArray(dirs)) return [{ keyPath: 'coder.sandbox.add_dirs', why: 'it must be a list of directories' }];
  const trees = refusedTrees(guards);
  /** @type {Array<{keyPath: string, why: string}>} */
  const out = [];
  dirs.forEach((entry, i) => {
    const one = checkOne(entry, i, trees);
    if ('keyPath' in one) out.push({ keyPath: one.keyPath, why: one.why });
  });
  return out;
}

/**
 * Check `add_dirs` and resolve each entry; the first refusal wins (the builder fails closed).
 * @param {unknown} dirs - the `coder.sandbox.add_dirs` value.
 * @param {SandboxGuards} [guards]
 * @returns {{ok: true, dirs: string[]} | {ok: false, keyPath: string, why: string}} `why` names a
 *   category, never the value.
 */
export function checkSandboxDirs(dirs, guards = {}) {
  if (dirs === undefined) return { ok: true, dirs: [] };
  if (!Array.isArray(dirs)) return { ok: false, keyPath: 'coder.sandbox.add_dirs', why: 'it must be a list of directories' };
  const trees = refusedTrees(guards);
  /** @type {string[]} */
  const out = [];
  for (let i = 0; i < dirs.length; i += 1) {
    const one = checkOne(dirs[i], i, trees);
    if ('keyPath' in one) return { ok: false, keyPath: one.keyPath, why: one.why };
    out.push(one.dir);
  }
  return { ok: true, dirs: out };
}

/**
 * The message of a refusal: the key path and the category, never the value.
 * @param {{keyPath: string, why: string}} refusal @returns {string}
 */
export function sandboxRefusalMessage(refusal) {
  return `${SANDBOX_DIR_REFUSED}: ${refusal.keyPath} is refused — ${refusal.why}`;
}
