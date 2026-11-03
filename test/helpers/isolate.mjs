/**
 * Test preload (plan §9.6 rule 1, V8/V12): `node --import ./test/helpers/isolate.mjs --test …`.
 *
 * `node --test` runs this in every test WORKER process (the orchestrator does not evaluate
 * `--import`; the workers inherit it through `execArgv`, `[A28]`), before the test file is
 * evaluated. It:
 *  1. records the real `HOME` and `TMPDIR` once, in `CODE_FORGE_REAL_HOME` / `CODE_FORGE_REAL_TMPDIR`
 *     (a nested process keeps the first values);
 *  2. creates the private base `<real tmp>/code-forge-tests/` (mode 0700) and STOPS — throws,
 *     no sweep, no signal — unless it is a real directory (not a symlink) owned by this user
 *     with no group/other permission bit: on a shared `/tmp` another user could otherwise
 *     pre-create it or point a symlink at roots of their choosing;
 *  3. creates this process's root `<base>/code-forge-test-<pid>/` (a `src/util/tmp.mjs` run
 *     root, so it carries `owner.json` and the `pids/` registry `exec` writes into), points
 *     `HOME`, `TMPDIR` and the cwd at it, and exports it as `CODE_FORGE_TEST_ROOT`;
 *  4. then sweeps stale `code-forge-test-*` roots left by killed runs — their registered children
 *     are reaped (`src/util/reaper.mjs`, start-time checked) and the directories removed;
 *  5. on exit, reaps registered children, fails on new guarded entries except foreign or vanished locks and outside-temp runs with their project ledgers, and removes its root.
 *     (`leakedEntries`: new paths of a run already in the baseline are a live run, not a leak.)
 * `src/**` modules are imported dynamically, AFTER `HOME` is pinned, so no `src` module can ever
 * see the real `HOME` from this process.
 */

import { ChildProcess, execFileSync } from 'node:child_process';
import { existsSync, lstatSync, mkdirSync, readdirSync, readFileSync, realpathSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

// ps start times have second precision. Owners from this same second remain unverified.
const suiteStart = Math.floor((Date.now() - process.uptime() * 1000) / 1000) * 1000;

// Retain ownership after exit/unregistration: ps can no longer prove a dead child's ancestry.
// Observe every async child (including exec's registered children) before its spawn listeners.
const suitePids = new Set([process.pid]);
const emitChildEvent = ChildProcess.prototype.emit;
ChildProcess.prototype.emit = function (event, ...args) {
  if (event === 'spawn' && this.pid !== undefined) suitePids.add(this.pid);
  return emitChildEvent.call(this, event, ...args);
};

// Node's execFileSync/execSync close over their internal spawnSync, so wrapping the public
// spawnSync export misses them. Observe their shared native boundary instead: it exposes the
// pid before exec helpers discard it, while preserving all native output/error semantics.
const syncBinding = /** @type {{ spawn: (options: unknown) => { pid: number } }} */ (process['binding']('spawn_sync'));
const spawnSyncProcess = syncBinding.spawn;
syncBinding.spawn = function (options) {
  const result = spawnSyncProcess.call(this, options);
  if (result.pid > 1) suitePids.add(result.pid);
  return result;
};

process.env.CODE_FORGE_REAL_HOME ??= os.homedir();
process.env.CODE_FORGE_REAL_TMPDIR ??= os.tmpdir();
const realTmp = process.env.CODE_FORGE_REAL_TMPDIR;

/** The real directories no test may ever write into. */
export const GUARDED_DIRS = Object.freeze([
  path.join(process.env.CODE_FORGE_REAL_HOME, '.code-forge'),
  path.join(REPO, '.code-forge'),
]);

/**
 * Every path under `dirs` (the directories themselves included when present).
 * @param {readonly string[]} [dirs] - defaults to the real guarded directories.
 * @returns {Set<string>}
 */
export function snapshotGuarded(dirs = GUARDED_DIRS) {
  const seen = new Set();
  for (const dir of dirs) {
    if (!existsSync(dir)) continue;
    seen.add(dir);
    for (const rel of readdirSync(dir, { recursive: true })) {
      seen.add(path.join(dir, String(rel)));
    }
  }
  return seen;
}

/**
 * Whether a path under `<dir>/runs/` belongs to a verified run: `<id>` (its directory),
 * `<id>.<anything>` or anything under `<id>/`.
 * The id is matched exactly, never as a prefix (`r-1` does not cover `r-10.lock`).
 * @param {string} dir - a guarded directory.
 * @param {string} p - a path under `dir`.
 * @param {(id: string) => boolean} hasRun
 * @returns {boolean}
 */
function belongsToRun(dir, p, hasRun) {
  const runs = path.join(dir, 'runs');
  if (!p.startsWith(runs + path.sep)) return false;
  const seg = p.slice(runs.length + 1).split(path.sep)[0];
  const covered = (/** @type {string} */ id) => id !== '' && hasRun(id);
  if (p !== path.join(runs, seg)) return covered(seg);
  if (covered(seg)) return true;
  for (let i = seg.indexOf('.'); i !== -1; i = seg.indexOf('.', i + 1)) {
    if (covered(seg.slice(0, i))) return true;
  }
  return false;
}

/** @typedef {(pid: number) => {start: string | null, ppid: number} | null | undefined} ProcessProbe */

/**
 * Synchronous because the guard runs during exit. null proves absence; undefined means
 * ps failed or returned an invalid record and leaves the owner unverified.
 * @type {ProcessProbe}
 */
function probeProcess(pid) {
  try {
    const text = execFileSync('/bin/ps', ['-o', 'ppid=', '-o', 'lstart=', '-p', String(pid)], {
      encoding: 'utf8',
      env: { ...process.env, LC_ALL: 'C' },
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout: 5000,
    }).trim();
    const match = /^(\d+)\s+(.+)$/.exec(text);
    return match ? { ppid: Number(match[1]), start: match[2] } : undefined;
  } catch (err) {
    const failure = /** @type {{status?: number, signal?: string | null, code?: string, stdout?: string | Buffer, stderr?: string | Buffer}} */ (err);
    return failure.status === 1 && failure.signal === null && failure.code === undefined
      && failure.stdout?.toString().trim() === '' && failure.stderr?.toString().trim() === ''
      ? null : undefined;
  }
}

/**
 * @param {string} p
 * @returns {string}
 */
function realpathOrRaw(p) {
  try {
    return realpathSync(p);
  } catch {
    return p;
  }
}

/**
 * Ignore vanished locks and exited outside owners whose starts predate this suite;
 * verify live owners' entire ancestry. Untracked dead descendants remain unverified.
 * Retained suite pids keep dead/reparented children from acquiring a foreign exemption.
 * @param {string} p
 * @param {ProcessProbe} probe
 * @returns {boolean}
 */
function isForeignLock(p, probe) {
  let text;
  try {
    text = readFileSync(p, 'utf8');
  } catch (err) {
    // Only absence proves there is no longer a lock. Unreadable existing files fail closed.
    return /** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT';
  }
  try {
    const lock = JSON.parse(text);
    if (lock === null || typeof lock !== 'object' || Array.isArray(lock)
      || typeof lock.key !== 'string' || !Number.isInteger(lock.pid) || lock.pid <= 1
      || typeof lock.start !== 'string' || lock.start.trim() === '' || lock.start === 'unknown') return false;
    if (suitePids.has(lock.pid)) return false;
    const owner = probe(lock.pid);
    if (owner === null) return Date.parse(lock.start) < suiteStart;
    if (owner === undefined) return false;
    if (owner.start !== lock.start) return false;
    const seen = new Set();
    let pid = lock.pid;
    let current = owner;
    while (pid !== 1) {
      if (suitePids.has(pid) || seen.has(pid) || current == null
        || !Number.isInteger(current.ppid) || current.ppid < 1) return false;
      seen.add(pid);
      pid = current.ppid;
      if (pid !== 1) current = probe(pid);
    }
    return true;
  } catch {
    return false;
  }
}

/**
 * Current run ids and project slugs whose absolute workspaces are outside all test roots.
 * Missing, unreadable or malformed records cannot establish an exemption.
 * @param {string} dir
 * @param {string} tempDir
 * @returns {Map<string, string | undefined>}
 */
function foreignRuns(dir, tempDir) {
  const runs = path.join(dir, 'runs');
  /** @type {Map<string, string | undefined>} */
  const foreign = new Map();
  let names;
  try {
    names = readdirSync(runs);
  } catch {
    return foreign;
  }
  const testBases = [tempDir, TEST_BASE].flatMap((base) => [base, realpathOrRaw(base)]);
  for (const name of names) {
    if (!name.endsWith('.json') || name.length === 5) continue;
    try {
      const record = JSON.parse(readFileSync(path.join(runs, name), 'utf8'));
      if (record === null || typeof record !== 'object' || Array.isArray(record)
        || typeof record.workspace !== 'string' || !path.isAbsolute(record.workspace)) continue;
      const workspaces = [record.workspace, realpathOrRaw(record.workspace)];
      const insideTests = workspaces.some((workspace) => testBases.some((base) => {
        const relative = path.relative(base, workspace);
        return relative === '' || (relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
      }));
      if (!insideTests) foreign.set(name.slice(0, -5), typeof record.project === 'string' ? record.project : undefined);
    } catch {
      // Unreadable or invalid run records remain unverified.
    }
  }
  return foreign;
}

/**
 * The new paths under the guarded dirs that count as leaks, sorted. A path not in `baseline` is a
 * leak unless it belongs to a run whose record was already in the baseline (a live run of the
 * developer that started before the tests): `runs/<id>`, `runs/<id>.<anything>`, `runs/<id>/…`.
 * Vanished `locks/<sha>.lock` entries and verified foreign owners are also exempt, as is a new
 * locks directory containing no leaks. Dead untracked owners need a start before the suite.
 * Existing malformed/unreadable locks, failed probes and unknown owner identity or ancestry
 * fail closed; retained suite pids always remain leaks.
 * New `runs/<id>`, `runs/<id>.<anything>` and nested paths, plus matching project ledgers, are also exempt when a current record names an absolute workspace outside the raw and resolved real temp dir and TEST_BASE.
 * @param {Iterable<string>} baseline - the snapshot taken at start.
 * @param {Iterable<string>} now - the snapshot taken at exit.
 * @param {readonly string[]} [dirs] - defaults to the real guarded directories.
 * @param {ProcessProbe} [probe] - defaults to synchronous /bin/ps.
 * @param {string} [tempDir] - defaults to CODE_FORGE_REAL_TMPDIR.
 * @returns {string[]}
 */
export function leakedEntries(baseline, now, dirs = GUARDED_DIRS, probe = probeProcess, tempDir = realTmp) {
  const before = new Set(baseline);
  const leaked = [];
  const newLockDirs = [];
  /** @type {Map<string, Map<string, string | undefined>>} */
  const foreignByDir = new Map();
  for (const p of new Set(now)) {
    if (before.has(p)) continue;
    const dir = dirs.find((d) => p.startsWith(d + path.sep));
    if (dir !== undefined && belongsToRun(dir, p, (id) => before.has(path.join(dir, 'runs', `${id}.json`)))) continue;
    if (dir !== undefined) {
      let foreign = foreignByDir.get(dir);
      if (foreign === undefined) {
        foreign = foreignRuns(dir, tempDir);
        foreignByDir.set(dir, foreign);
      }
      if (belongsToRun(dir, p, (id) => foreign.has(id))
        || [...foreign.values()].some((project) => project !== undefined
          && path.dirname(p) === path.join(dir, 'ledger')
          && path.basename(p) === `${project}.jsonl`)) continue;
      const locks = path.join(dir, 'locks');
      if (p === locks) {
        try {
          if (lstatSync(p).isDirectory()) {
            newLockDirs.push(p);
            continue;
          }
        } catch {
          // Unreadable entries remain leaks.
        }
      }
      if (path.dirname(p) === locks && /^[a-f\d]{64}\.lock$/i.test(path.basename(p))
        && isForeignLock(p, probe)) continue;
    }
    leaked.push(p);
  }
  for (const dir of newLockDirs) {
    if (leaked.some((p) => p.startsWith(dir + path.sep))) leaked.push(dir);
  }
  return leaked.sort();
}

/**
 * Snapshot the guarded dirs and apply the suite guard's leak filter synchronously.
 * @param {Iterable<string>} before - the snapshot taken at start.
 * @param {readonly string[]} [dirs] - defaults to the real guarded directories.
 * @param {ProcessProbe} [probe] - defaults to synchronous /bin/ps.
 * @returns {string[]}
 */
export function leakedSince(before, dirs = GUARDED_DIRS, probe = probeProcess) {
  return leakedEntries(before, snapshotGuarded(dirs), dirs, probe);
}

const baseline = snapshotGuarded();

// Pin before any src module is loaded. The roots live in a private base (0700, ours) rather than
// directly in the OS temp dir, which on Linux is world-writable and so never a trusted base.
mkdirSync(realTmp, { recursive: true });
/** The private base every test root (and the no-leak baseline file) lives in. */
export const TEST_BASE = path.join(realTmp, 'code-forge-tests');
export const TEST_HOME_PREFIX = 'code-forge-test-';
const planned = path.join(TEST_BASE, `${TEST_HOME_PREFIX}${process.pid}`);
process.env.HOME = planned;
process.env.TMPDIR = planned;

const tmp = await import('../../src/util/tmp.mjs');
const reaper = await import('../../src/util/reaper.mjs');

// Create the base ourselves, then verify it before anything is created, swept or signalled.
try {
  mkdirSync(TEST_BASE, { mode: 0o700 });
} catch {
  // it exists (fine, verified next) or cannot be created (the check below says why)
}
const baseReason = tmp.untrustedReason(TEST_BASE, 0o077);
if (baseReason !== null) {
  throw new Error(`isolate: refusing untrusted test base (${baseReason}): ${TEST_BASE} — no sweep, no signal`);
}

// Owner record first, sweep second: a parallel worker's sweep never sees this root ownerless.
const root = realpathSync(tmp.runRoot(`${TEST_HOME_PREFIX}${process.pid}`, { root: TEST_BASE }));
tmp.setRunRoot(root);
await tmp.sweepRoots({ root: TEST_BASE, prefix: TEST_HOME_PREFIX });

process.env.HOME = root;
process.env.TMPDIR = root;
process.env.CODE_FORGE_TEST_ROOT = root;
process.chdir(root);

process.on('exit', () => {
  const leaked = leakedEntries(baseline, snapshotGuarded());
  if (leaked.length > 0) {
    process.stderr.write(`isolate: ${leaked.length} new entries under the real code-forge dirs:\n${leaked.join('\n')}\n`);
    process.exitCode = 1;
  }
  // A registered child still running now would outlive this process: SIGKILL its group when
  // its start time still matches (same rule as reaper.sweep, synchronous because this is 'exit').
  for (const entry of reaper.listEntries(tmp.pidsDir(root))) {
    if (entry.start_time !== reaper.UNKNOWN_START_TIME && reaper.readStartTime(entry.pid) === entry.start_time) {
      try {
        process.kill(-entry.pid, 'SIGKILL');
      } catch {
        // already gone
      }
    }
  }
  try {
    process.chdir(realTmp);
  } catch {
    // the root is removed either way
  }
  rmSync(root, { recursive: true, force: true });
});
