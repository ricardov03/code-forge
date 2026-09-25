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
 *  5. on exit, SIGKILLs any registered child still running (start time checked), fails the
 *     process when the real `~/.code-forge` or `<repo>/.code-forge` gained an
 *     entry during its lifetime, and removes its root.
 * `src/**` modules are imported dynamically, AFTER `HOME` is pinned, so no `src` module can ever
 * see the real `HOME` from this process.
 */

import { existsSync, mkdirSync, readdirSync, realpathSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

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

const baseline = snapshotGuarded();

// Pin before any src module is loaded. The roots live in a private base (0700, ours) rather than
// directly in the OS temp dir, which on Linux is world-writable and so never a trusted base.
mkdirSync(realTmp, { recursive: true });
/** The private base every test root (and the no-leak baseline file) lives in. */
export const TEST_BASE = path.join(realTmp, 'code-forge-tests');
const planned = path.join(TEST_BASE, `code-forge-test-${process.pid}`);
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
const root = realpathSync(tmp.runRoot(`code-forge-test-${process.pid}`, { root: TEST_BASE }));
tmp.setRunRoot(root);
await tmp.sweepRoots({ root: TEST_BASE, prefix: 'code-forge-test-' });

process.env.HOME = root;
process.env.TMPDIR = root;
process.env.CODE_FORGE_TEST_ROOT = root;
process.chdir(root);

process.on('exit', () => {
  const leaked = [...snapshotGuarded()].filter((p) => !baseline.has(p));
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
