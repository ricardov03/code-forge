import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import fs, { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import { syncBuiltinESMExports } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { GUARDED_DIRS, leakedEntries, snapshotGuarded, TEST_BASE } from './helpers/isolate.mjs';
import { runsDir } from '../src/state/paths.mjs';
import { requireSuiteMarker, suiteWorkers } from './helpers/worker-processes.mjs';

// Plan §9.6 rule 4 / V12: nothing a test does reaches the real `~/.code-forge` or the repo's
// `.code-forge/`. Two guards:
//  - per worker: the preload (test/helpers/isolate.mjs) snapshots both real dirs when a worker
//    starts and fails it at exit on any new entry, so every test file fails on its own leak;
//  - per `npm test` run (clause 7, the counted assertion): the `test` script runs this file
//    FIRST with CODE_FORGE_NO_LEAK=snapshot, which writes the pre-suite snapshot of the real dirs
//    to <TEST_BASE>/no-leak-<suite hash>.json, then the whole suite, then this file again LAST with
//    CODE_FORGE_NO_LEAK=final, which compares the real dirs against that snapshot and asserts
//    exactly [] and, after 3 seconds, exactly 0 workers with this suite's marker and temp HOME.
//    In the suite run (no mode) both mode tests are skipped.
// The controls never write into the real dirs: the positive control runs the snapshotter over
// temp dirs; the negative control writes under the temp HOME and watches the real dirs.

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const MODE = process.env.CODE_FORGE_NO_LEAK ?? 'suite';
const SANDBOX = process.env.CODE_FORGE_LEAK_GUARD === 'sandbox';
const KERNEL_GUARD = SANDBOX && 'kernel sandbox enforces guarded writes; no snapshot comparison';
const SUITE = process.env.CODE_FORGE_TEST_SUITE ?? '';
const BASELINE_FILE = path.join(TEST_BASE, `no-leak-${createHash('sha256').update(SUITE).digest('hex')}.json`);
const SKIP_SNAPSHOT = KERNEL_GUARD || (MODE !== 'snapshot' && 'runs only in the snapshot step of the test script');
const SKIP_FINAL = MODE !== 'final' && 'runs only in the final step of the test script';
/** @returns {number | null} */
const baselineMtime = () => (existsSync(BASELINE_FILE) ? statSync(BASELINE_FILE).mtimeMs : null);
const baselineMtimeAtLoad = baselineMtime();

let PARENT = '';
before(async () => {
  PARENT = await mkdtemp(path.join(os.tmpdir(), 'no-leak-'));
});
after(async () => {
  if (PARENT) await rm(PARENT, { recursive: true, force: true });
});

/**
 * @param {readonly string[]} dirs
 * @param {Set<string>} baseline
 * @returns {string[]}
 */
function newEntries(dirs, baseline) {
  return leakedEntries(baseline, snapshotGuarded(dirs), dirs);
}

// A grandchild has a real pid that the caller's retained direct-child set does not know.
// Only the injected ancestry makes it foreign; the real process stays in our tree for cleanup.
/** @param {import('node:test').TestContext} t */
async function lockOwner(t) {
  const child = spawn(process.execPath, ['-e', `
    const { spawn } = require('node:child_process');
    const owner = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' });
    owner.once('spawn', () => process.send(owner.pid));
    process.once('message', () => owner.kill());
    process.once('disconnect', () => owner.kill());
    owner.once('exit', () => process.exit(0));
  `], { stdio: ['ignore', 'ignore', 'inherit', 'ipc'] });
  const exited = once(child, 'exit');
  let stopped = false;
  const stop = async () => {
    if (!stopped) {
      stopped = true;
      child.send('stop');
    }
    await exited;
  };
  t.after(stop);
  const [pid] = await once(child, 'message');
  assert.equal(Number.isInteger(pid) && pid > 1, true, 'one real grandchild owner was spawned');
  return { pid: /** @type {number} */ (pid), parent: child.pid, stop };
}

test('lock guard exempts live foreign sample locks, recovered probes and locks gone at re-check: exactly 0 leaks', async (t) => {
  const owner = await lockOwner(t);
  const dir = path.join(PARENT, 'foreign-lock-guard');
  mkdirSync(dir);
  const before = snapshotGuarded([dir]);
  const locks = path.join(dir, 'locks');
  mkdirSync(locks);
  const keys = ['r-foreign\u0000A0.2\u0000fixtures/input.json', 'ticket\u0000r-foreign\u0000ticket-id'];
  const files = keys.map((key) => path.join(locks, `${createHash('sha256').update(key).digest('hex')}.lock`));
  const start = 'Tue Nov 3 12:29:44 2026';
  const rawStart = 'Tue Nov  3 12:29:44 2026';
  const ancestor = 2_000_000_001;
  /** @param {number} pid */
  const probe = (pid) => pid === owner.pid ? { start: rawStart, ppid: ancestor }
    : pid === ancestor ? { start: rawStart, ppid: 1 } : null;
  for (const [index, file] of files.entries()) {
    writeFileSync(file, JSON.stringify({ key: keys[index], pid: owner.pid, start }));
  }
  const live = leakedEntries(before, snapshotGuarded([dir]), [dir], probe);
  rmSync(files[1]);
  const now = snapshotGuarded([dir]);
  assert.equal(now.size - before.size, 2, 'exactly the new locks directory and remaining lock');

  let ownerAttempts = 0;
  const recoveredOwner = leakedEntries(before, now, [dir], (pid) => {
    if (pid === owner.pid && ++ownerAttempts <= 2) return undefined;
    return probe(pid);
  });
  let ancestorAttempts = 0;
  const recoveredAncestry = leakedEntries(before, now, [dir], (pid) => {
    if (pid === ancestor && ++ancestorAttempts <= 2) throw new Error('ps timed out');
    return probe(pid);
  });
  const vanishedDuringProbe = leakedEntries(before, now, [dir], () => {
    rmSync(files[0], { force: true });
    return undefined;
  });
  const vanishedBeforeRead = leakedEntries(before, now, [dir], probe);
  writeFileSync(files[0], JSON.stringify({ key: keys[0], pid: owner.pid, start }));
  const originalRead = fs.readFileSync;
  const reader = t.mock.method(fs, 'readFileSync', (file, options) => {
    if (file !== files[0]) return originalRead(file, options);
    rmSync(files[0]);
    throw Object.assign(new Error('writer replaced the lock during read'), { code: 'EACCES' });
  });
  syncBuiltinESMExports();
  let vanishedDuringRead;
  try {
    vanishedDuringRead = leakedEntries(before, now, [dir], probe);
  } finally {
    reader.mock.restore();
    syncBuiltinESMExports();
  }
  assert.deepEqual({ live, recoveredOwner, ownerAttempts, recoveredAncestry, ancestorAttempts, vanishedDuringProbe, vanishedBeforeRead, vanishedDuringRead }, {
    live: [], recoveredOwner: [], ownerAttempts: 3, recoveredAncestry: [], ancestorAttempts: 3,
    vanishedDuringProbe: [], vanishedBeforeRead: [], vanishedDuringRead: [],
  }, 'both sample bodies, three-attempt owner/ancestor recovery and all three vanished-lock races yield exactly 0 leaks');
});

test('lock guard keeps suite owners and dead owners started during the suite as exact leaks beside a live foreign lock', async (t) => {
  const owner = await lockOwner(t);
  const dir = path.join(PARENT, 'strict-lock-guard');
  const locks = path.join(dir, 'locks');
  mkdirSync(locks, { recursive: true });
  const before = snapshotGuarded([dir]);
  const start = new Date().toString();
  const self = path.join(locks, `${'1'.repeat(64)}.lock`);
  const child = path.join(locks, `${'2'.repeat(64)}.lock`);
  const descendant = path.join(locks, `${'3'.repeat(64)}.lock`);
  const foreign = path.join(locks, `${'4'.repeat(64)}.lock`);
  const dead = path.join(locks, `${'5'.repeat(64)}.lock`);
  for (const [index, pid] of [process.pid, owner.parent, owner.pid].entries()) {
    writeFileSync([self, child, descendant][index], JSON.stringify({ key: 'ticket\u0000r-suite\u0000ticket-id', pid, start }));
  }
  writeFileSync(foreign, JSON.stringify({ key: 'r-foreign\u0000A0.2\u0000fixtures/input.json', pid: process.ppid, start: 'Tue Nov 3 12:29:44 2026' }));
  /** @param {number} pid */
  const probe = (pid) => pid === owner.pid ? { start, ppid: /** @type {number} */ (owner.parent) }
    : pid === process.ppid ? { start: 'Tue Nov  3 12:29:44 2026', ppid: 1 }
      : { start, ppid: 1 };
  const liveTree = leakedEntries(before, snapshotGuarded([dir]), [dir], probe);
  await owner.stop();
  rmSync(descendant);
  writeFileSync(dead, JSON.stringify({ key: 'ticket\u0000r-suite\u0000dead-id', pid: owner.pid, start }));
  const deadTree = leakedEntries(before, snapshotGuarded([dir]), [dir], (pid) => pid === owner.pid ? null : probe(pid));
  assert.deepEqual({ liveTree, deadTree }, {
    liveTree: [self, child, descendant], deadTree: [self, child, dead],
  }, 'exactly three suite locks leak in each check; the matching live foreign neighbor never leaks');
});

test('the snapshot watches the REAL dirs: <real HOME>/.code-forge and <repo>/.code-forge, exactly 2', () => {
  assert.deepEqual(GUARDED_DIRS, [
    path.join(/** @type {string} */ (process.env.CODE_FORGE_REAL_HOME), '.code-forge'),
    path.join(REPO, '.code-forge'),
  ]);
  assert.notEqual(GUARDED_DIRS[0], path.join(os.homedir(), '.code-forge'));
});

test('control: a product write under the temp HOME is 1 file there and 0 new entries in the real dirs', { skip: KERNEL_GUARD }, () => {
  const baseline = snapshotGuarded();
  const dir = runsDir();
  assert.equal(dir, path.join(/** @type {string} */ (process.env.CODE_FORGE_TEST_ROOT), '.code-forge', 'runs'));
  mkdirSync(dir, { recursive: true });
  const probe = path.join(dir, 'leak-probe.json');
  writeFileSync(probe, '{}\n');

  assert.equal(existsSync(probe), true);
  assert.deepEqual(newEntries(GUARDED_DIRS, baseline), []);
});

test('control over TEMP dirs: outside writes into a pre-existing run are exempt; an escaped product write is exactly 1 leak', () => {
  const dirs = [path.join(PARENT, 'home-code-forge'), path.join(PARENT, 'repo-code-forge')];
  for (const dir of dirs) mkdirSync(dir);
  const runs = path.join(dirs[0], 'runs');
  mkdirSync(runs);
  const record = path.join(runs, 'existing.json');
  writeFileSync(record, '{}\n');
  const baseline = snapshotGuarded(dirs);
  assert.deepEqual([...baseline].sort(), [...dirs, runs, record].sort());

  const state = path.join(runs, 'existing', 'review-state', 'other-block');
  mkdirSync(state, { recursive: true });
  writeFileSync(path.join(state, 'outside.json'), '{}\n');
  assert.equal(snapshotGuarded(dirs).size - baseline.size, 4, 'the outside worker added exactly 4 paths to its existing run');
  assert.deepEqual(newEntries(dirs, baseline), [], 'outside writes into the existing run are 0 leaks');

  const probe = path.join(dirs[1], 'probe.json');
  writeFileSync(probe, '');
  assert.deepEqual(newEntries(dirs, baseline), [probe]);

  rmSync(probe);
  assert.deepEqual(newEntries(dirs, baseline), []);
});

test('suite run (CODE_FORGE_NO_LEAK unset): the snapshot and final steps are skipped and this file writes no baseline', {
  skip: MODE !== 'suite' && 'runs only in the suite run',
}, () => {
  assert.deepEqual([typeof SKIP_SNAPSHOT, typeof SKIP_FINAL], ['string', 'string']);
  assert.equal(baselineMtime(), baselineMtimeAtLoad);
});

test('snapshot step (CODE_FORGE_NO_LEAK=snapshot): the pre-suite snapshot of the real dirs is written', { skip: SKIP_SNAPSHOT }, () => {
  requireSuiteMarker(SUITE);
  writeFileSync(BASELINE_FILE, `${JSON.stringify([...snapshotGuarded()])}\n`, { mode: 0o600 });
  assert.equal(existsSync(BASELINE_FILE), true);
});

test('final step (CODE_FORGE_NO_LEAK=final): 0 new entries under the real dirs and 0 live suite workers', { skip: SKIP_FINAL }, async () => {
  let baselineError = null;
  let baseline = [];
  if (!SANDBOX) try {
    if (!existsSync(BASELINE_FILE)) baselineError = `no pre-suite snapshot at ${BASELINE_FILE}: run the snapshot step first`;
    else {
      const parsed = JSON.parse(readFileSync(BASELINE_FILE, 'utf8'));
      if (Array.isArray(parsed) && parsed.every((p) => typeof p === 'string')) baseline = parsed;
      else baselineError = 'the snapshot is a JSON array of paths';
    }
  } catch (err) {
    baselineError = err.message;
  } finally {
    rmSync(BASELINE_FILE, { force: true });
  }
  const dirs = SANDBOX ? [] : leakedEntries(baseline, snapshotGuarded());
  let markerError = null;
  try { requireSuiteMarker(SUITE); } catch (err) { markerError = err.message; }
  // Allow cleanup and the orphan guard several seconds after the last test process exits.
  if (markerError === null) await new Promise((resolve) => setTimeout(resolve, 3000));
  let workers = [];
  let workerError = null;
  try { workers = suiteWorkers(SUITE, TEST_BASE); } catch (err) { workerError = err.message; }
  if (workers.length > 0) process.stderr.write(`leaked workers: ${JSON.stringify(workers)}\n`);
  assert.deepEqual({ dirs, workers, baselineError, markerError, workerError }, { dirs: [], workers: [], baselineError: null, markerError: null, workerError: null }, `suite leaks: directories ${JSON.stringify(dirs)}; leaked pid/run list ${JSON.stringify(workers)}; baseline: ${baselineError ?? 'valid'}; suite marker: ${markerError ?? 'valid'}; worker report: ${workerError ?? 'valid'}`);
});
