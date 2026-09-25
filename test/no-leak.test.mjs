import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { GUARDED_DIRS, snapshotGuarded, TEST_BASE } from './helpers/isolate.mjs';
import { runsDir } from '../src/state/paths.mjs';

// Plan §9.6 rule 4 / V12: nothing a test does reaches the real `~/.code-forge` or the repo's
// `.code-forge/`. Two guards:
//  - per worker: the preload (test/helpers/isolate.mjs) snapshots both real dirs when a worker
//    starts and fails it at exit on any new entry, so every test file fails on its own leak;
//  - per `npm test` run (clause 7, the counted assertion): the `test` script runs this file
//    FIRST with CODE_FORGE_NO_LEAK=snapshot, which writes the pre-suite snapshot of the real dirs
//    to <TEST_BASE>/no-leak-baseline.json, then the whole suite, then this file again LAST with
//    CODE_FORGE_NO_LEAK=final, which compares the real dirs against that snapshot and asserts
//    exactly []. In the suite run (no mode) both mode tests are skipped.
// The controls never write into the real dirs: the positive control runs the snapshotter over
// temp dirs; the negative control writes under the temp HOME and watches the real dirs.

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const MODE = process.env.CODE_FORGE_NO_LEAK ?? 'suite';
const BASELINE_FILE = path.join(TEST_BASE, 'no-leak-baseline.json');
const SKIP_SNAPSHOT = MODE !== 'snapshot' && 'runs only in the snapshot step of the test script';
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
  return [...snapshotGuarded(dirs)].filter((p) => !baseline.has(p));
}

test('the snapshot watches the REAL dirs: <real HOME>/.code-forge and <repo>/.code-forge, exactly 2', () => {
  assert.deepEqual(GUARDED_DIRS, [
    path.join(/** @type {string} */ (process.env.CODE_FORGE_REAL_HOME), '.code-forge'),
    path.join(REPO, '.code-forge'),
  ]);
  assert.notEqual(GUARDED_DIRS[0], path.join(os.homedir(), '.code-forge'));
});

test('control: a product write under the temp HOME is 1 file there and 0 new entries in the real dirs', () => {
  const baseline = snapshotGuarded();
  const dir = runsDir();
  assert.equal(dir, path.join(/** @type {string} */ (process.env.CODE_FORGE_TEST_ROOT), '.code-forge', 'runs'));
  mkdirSync(dir, { recursive: true });
  const probe = path.join(dir, 'leak-probe.json');
  writeFileSync(probe, '{}\n');

  assert.equal(existsSync(probe), true);
  assert.deepEqual(newEntries(GUARDED_DIRS, baseline), []);
});

test('positive control over TEMP dirs: a planned entry is exactly the one new path, and [] after cleanup', () => {
  const dirs = [path.join(PARENT, 'home-code-forge'), path.join(PARENT, 'repo-code-forge')];
  for (const dir of dirs) mkdirSync(dir);
  const baseline = snapshotGuarded(dirs);
  assert.deepEqual([...baseline], dirs);

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
  writeFileSync(BASELINE_FILE, `${JSON.stringify([...snapshotGuarded()])}\n`, { mode: 0o600 });
  assert.equal(existsSync(BASELINE_FILE), true);
});

test('final step (CODE_FORGE_NO_LEAK=final): 0 new entries under the real dirs since the pre-suite snapshot', { skip: SKIP_FINAL }, () => {
  assert.equal(existsSync(BASELINE_FILE), true, `no pre-suite snapshot at ${BASELINE_FILE}: run the snapshot step first`);
  /** @type {unknown} */
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(BASELINE_FILE, 'utf8'));
  } finally {
    rmSync(BASELINE_FILE, { force: true });
  }
  assert.ok(Array.isArray(parsed) && parsed.every((p) => typeof p === 'string'), 'the snapshot is a JSON array of paths');
  assert.deepEqual(newEntries(GUARDED_DIRS, new Set(parsed)), []);
});
