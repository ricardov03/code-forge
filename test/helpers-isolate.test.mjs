import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdirSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { leakedEntries, snapshotGuarded } from './helpers/isolate.mjs';
import * as isolate from './helpers/isolate.mjs';

// B77: the exit guard of the preload ignores new paths of a run that was already in the baseline
// (the developer's live run), and still reports everything else. All dirs here are fake temp dirs.

let PARENT = '';
/** @type {string[]} */
let DIRS = [];
let HOME_CF = '';
let REPO_CF = '';

before(async () => {
  PARENT = await mkdtemp(path.join(os.tmpdir(), 'isolate-leak-'));
  HOME_CF = path.join(PARENT, 'home', '.code-forge');
  REPO_CF = path.join(PARENT, 'repo', '.code-forge');
  DIRS = [HOME_CF, REPO_CF];
});
after(async () => {
  if (PARENT) await rm(PARENT, { recursive: true, force: true });
});

/**
 * A fresh pair of fake guarded dirs, each with `runs/r-1.json` (a live run) in place.
 * @param {string} name
 * @returns {{ dirs: string[], home: string, repo: string }}
 */
function fakeDirs(name) {
  const home = path.join(PARENT, name, 'home', '.code-forge');
  const repo = path.join(PARENT, name, 'repo', '.code-forge');
  for (const dir of [home, repo]) {
    mkdirSync(path.join(dir, 'runs'), { recursive: true });
    writeFileSync(path.join(dir, 'runs', 'r-1.json'), '{}\n');
  }
  return { dirs: [home, repo], home, repo };
}

test('a live run in the baseline: its .lock, .key, .json.tmp, its dir and paths under it are 0 leaks, in both dirs', () => {
  const { dirs, home, repo } = fakeDirs('live');
  const baseline = snapshotGuarded(dirs);
  for (const dir of [home, repo]) {
    const runs = path.join(dir, 'runs');
    writeFileSync(path.join(runs, 'r-1.lock'), '');
    writeFileSync(path.join(runs, 'r-1.key'), '');
    writeFileSync(path.join(runs, 'r-1.json.tmp'), '');
    mkdirSync(path.join(runs, 'r-1', 'review-state'), { recursive: true });
    writeFileSync(path.join(runs, 'r-1', 'review-state', 'B1.json'), '{}\n');
  }
  const now = snapshotGuarded(dirs);
  assert.equal(now.size - baseline.size, 12);
  assert.deepEqual(leakedEntries(baseline, now, dirs), []);
});

test('a new run id (no .json in the baseline) is reported: its .json, .lock and directory, 4 paths', () => {
  const { dirs, home } = fakeDirs('new-run');
  const baseline = snapshotGuarded(dirs);
  const runs = path.join(home, 'runs');
  writeFileSync(path.join(runs, 'r-2.json'), '{}\n');
  writeFileSync(path.join(runs, 'r-2.lock'), '');
  mkdirSync(path.join(runs, 'r-2'));
  writeFileSync(path.join(runs, 'r-2', 'x'), '');
  assert.deepEqual(leakedEntries(baseline, snapshotGuarded(dirs), dirs), [
    path.join(runs, 'r-2'),
    path.join(runs, 'r-2.json'),
    path.join(runs, 'r-2.lock'),
    path.join(runs, 'r-2', 'x'),
  ]);
});

test('prefix-only id match is a leak: r-1 in the baseline does not cover r-10.lock, r-10/ or r-1x.lock', () => {
  const { dirs, home } = fakeDirs('prefix');
  const baseline = snapshotGuarded(dirs);
  const runs = path.join(home, 'runs');
  writeFileSync(path.join(runs, 'r-10.lock'), '');
  writeFileSync(path.join(runs, 'r-1x.lock'), '');
  mkdirSync(path.join(runs, 'r-10'));
  assert.deepEqual(leakedEntries(baseline, snapshotGuarded(dirs), dirs), [
    path.join(runs, 'r-10'),
    path.join(runs, 'r-10.lock'),
    path.join(runs, 'r-1x.lock'),
  ]);
});

test('a path outside runs/ and a new entry directly under a guarded dir are leaks, even next to a live run', () => {
  const { dirs, home, repo } = fakeDirs('outside');
  const baseline = snapshotGuarded(dirs);
  writeFileSync(path.join(home, 'r-1.lock'), '');
  mkdirSync(path.join(home, 'keys', 'r-1'), { recursive: true });
  writeFileSync(path.join(repo, 'config.yml'), '');
  assert.deepEqual(leakedEntries(baseline, snapshotGuarded(dirs), dirs), [
    path.join(home, 'keys'),
    path.join(home, 'keys', 'r-1'),
    path.join(home, 'r-1.lock'),
    path.join(repo, 'config.yml'),
  ].sort());
});

test('the run record covers only its own guarded dir: home r-1.json does not cover <repo>/.code-forge/runs/r-1.lock', () => {
  mkdirSync(path.join(HOME_CF, 'runs'), { recursive: true });
  writeFileSync(path.join(HOME_CF, 'runs', 'r-1.json'), '{}\n');
  const baseline = snapshotGuarded(DIRS);
  assert.deepEqual([...baseline], [HOME_CF, path.join(HOME_CF, 'runs'), path.join(HOME_CF, 'runs', 'r-1.json')]);
  mkdirSync(path.join(REPO_CF, 'runs'), { recursive: true });
  writeFileSync(path.join(REPO_CF, 'runs', 'r-1.lock'), '');
  writeFileSync(path.join(HOME_CF, 'runs', 'r-1.lock'), '');
  assert.deepEqual(leakedEntries(baseline, snapshotGuarded(DIRS), DIRS), [
    REPO_CF,
    path.join(REPO_CF, 'runs'),
    path.join(REPO_CF, 'runs', 'r-1.lock'),
  ]);
});

test('a guarded dir that appears is a leak, and an unchanged snapshot is 0 leaks', () => {
  const dirs = [path.join(PARENT, 'appear', 'home', '.code-forge')];
  mkdirSync(path.dirname(dirs[0]), { recursive: true });
  const baseline = snapshotGuarded(dirs);
  assert.equal(baseline.size, 0);
  assert.deepEqual(leakedEntries(baseline, baseline, dirs), []);
  mkdirSync(dirs[0]);
  assert.deepEqual(leakedEntries(baseline, snapshotGuarded(dirs), dirs), [dirs[0]]);
});

const FOREIGN_PID = 2_000_000_001;
const START = 'Mon Oct 19 08:21:32 2026';
/** @param {number} n */
const lockName = (n) => `${n.toString(16).padStart(64, '0')}.lock`;
/** @param {number} pid */
const foreignProbe = (pid) => pid === FOREIGN_PID ? { start: START, ppid: 1 } : null;

// Optional calls keep a missing export on the base an assertion failure, not an import error.
test('leakedSince ignores a new file under a run whose record was in the baseline: 0 leaks', () => {
  const { dirs, home } = fakeDirs('since-baseline-run');
  const before = snapshotGuarded(dirs);
  const state = path.join(home, 'runs', 'r-1', 'review-state');
  mkdirSync(state, { recursive: true });
  writeFileSync(path.join(state, 'ticket.json'), '{}\n');
  assert.deepEqual(isolate.leakedSince?.(before, dirs, foreignProbe), []);
});

test('leakedSince reports a new run without a baseline record: exactly its directory and file', () => {
  const { dirs, home } = fakeDirs('since-new-run');
  const before = snapshotGuarded(dirs);
  const run = path.join(home, 'runs', 'r-2');
  mkdirSync(run);
  const file = path.join(run, 'state.json');
  writeFileSync(file, '{}\n');
  assert.deepEqual(isolate.leakedSince?.(before, dirs, foreignProbe), [run, file]);
});

test('leakedSince ignores a verified live foreign lock: 0 leaks', () => {
  const { dirs, home } = fakeDirs('since-foreign-lock');
  const before = snapshotGuarded(dirs);
  const locks = path.join(home, 'locks');
  mkdirSync(locks);
  writeFileSync(path.join(locks, lockName(1)), JSON.stringify({ key: 'foreign', pid: FOREIGN_PID, start: START }));
  assert.deepEqual(isolate.leakedSince?.(before, dirs, foreignProbe), []);
});

test('leakedSince reports a new stray file: exactly that path', () => {
  const { dirs, home } = fakeDirs('since-stray');
  const before = snapshotGuarded(dirs);
  const stray = path.join(home, 'stray.txt');
  writeFileSync(stray, '');
  assert.deepEqual(isolate.leakedSince?.(before, dirs, foreignProbe), [stray]);
});

test('live foreign locks with matching start times are 0 leaks, including a new empty locks directory', () => {
  const { dirs, home, repo } = fakeDirs('foreign-locks');
  mkdirSync(path.join(repo, 'locks'));
  const baseline = snapshotGuarded(dirs);
  mkdirSync(path.join(home, 'locks'));
  assert.deepEqual(leakedEntries(baseline, snapshotGuarded(dirs), dirs, foreignProbe), [], 'new empty locks directory: 0 leaks');
  for (const dir of dirs) {
    writeFileSync(path.join(dir, 'locks', lockName(1)), JSON.stringify({ key: 'foreign', pid: FOREIGN_PID, start: START }));
  }
  assert.deepEqual(leakedEntries(baseline, snapshotGuarded(dirs), dirs, foreignProbe), [], 'new and existing locks directories with foreign owners: 0 leaks');
});

test('dead pid, start mismatch, unknown start, malformed, own pid and descendant locks remain exact leaks', () => {
  const descendant = FOREIGN_PID + 1;
  const intermediate = FOREIGN_PID + 2;
  /** @type {Array<{name: string, record?: unknown, raw?: string, filename?: string, directory?: boolean, probe?: (pid: number) => {start: string | null, ppid: number} | null}>} */
  const cases = [
    { name: 'dead pid', record: { key: 'dead', pid: FOREIGN_PID + 3, start: START } },
    { name: 'start mismatch', record: { key: 'reused', pid: FOREIGN_PID, start: 'Sun Oct 18 08:21:32 2026' } },
    { name: 'unknown live start', probe: (pid) => pid === descendant ? { start: null, ppid: 1 } : foreignProbe(pid) },
    { name: 'unknown recorded start', record: { key: 'unknown', pid: FOREIGN_PID, start: 'unknown' } },
    { name: 'unknown matching start', record: { key: 'unknown', pid: descendant, start: 'unknown' }, probe: (pid) => pid === descendant ? { start: 'unknown', ppid: 1 } : foreignProbe(pid) },
    { name: 'malformed JSON', raw: '{' },
    { name: 'malformed null', raw: 'null' },
    { name: 'missing key', record: { pid: FOREIGN_PID, start: START } },
    { name: 'invalid pid', record: { key: 'bad-pid', pid: 1, start: START } },
    { name: 'empty start', record: { key: 'empty', pid: FOREIGN_PID, start: '' } },
    { name: 'wrong name', filename: 'foreign.lock', record: { key: 'name', pid: FOREIGN_PID, start: START } },
    { name: 'unreadable lock', directory: true },
    { name: 'own pid', record: { key: 'self', pid: process.pid, start: START }, probe: (pid) => pid === process.pid ? { start: START, ppid: 1 } : foreignProbe(pid) },
    { name: 'descendant pid', probe: (pid) => pid === descendant ? { start: START, ppid: intermediate } : pid === intermediate ? { start: START, ppid: process.pid } : foreignProbe(pid) },
    { name: 'unknown ancestry', probe: (pid) => pid === descendant ? { start: START, ppid: intermediate } : foreignProbe(pid) },
    { name: 'cyclic ancestry', probe: (pid) => pid === descendant ? { start: START, ppid: descendant } : foreignProbe(pid) },
    { name: 'probe failure', probe: (pid) => { if (pid === descendant) throw new Error('ps unavailable'); return foreignProbe(pid); } },
  ];
  for (const fixture of cases) {
    const { dirs, home } = fakeDirs(`invalid-lock-${fixture.name}`);
    const baseline = snapshotGuarded(dirs);
    const locks = path.join(home, 'locks');
    mkdirSync(locks);
    writeFileSync(path.join(locks, lockName(1)), JSON.stringify({ key: 'allowed neighbor', pid: FOREIGN_PID, start: START }));
    const bad = path.join(locks, fixture.filename ?? lockName(2));
    if (fixture.directory) mkdirSync(bad);
    else writeFileSync(bad, fixture.raw ?? JSON.stringify(fixture.record ?? { key: 'bad', pid: descendant, start: START }));
    assert.deepEqual(leakedEntries(baseline, snapshotGuarded(dirs), dirs, fixture.probe ?? foreignProbe), [locks, bad], `${fixture.name}: exactly the new locks directory and bad lock leak`);
  }
});

test('real ps allows a live foreign parent lock and reports exactly the live child lock and its new directory', async () => {
  const { dirs, home } = fakeDirs('real-locks');
  const baseline = snapshotGuarded(dirs);
  const locks = path.join(home, 'locks');
  mkdirSync(locks);
  const child = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { stdio: 'ignore' });
  try {
    await once(child, 'spawn');
    for (const [index, pid] of [process.ppid, child.pid].entries()) {
      const start = execFileSync('/bin/ps', ['-o', 'lstart=', '-p', String(pid)], { encoding: 'utf8', env: { ...process.env, LC_ALL: 'C' } }).trim();
      assert.notEqual(start, '', `owner ${pid} has a known start time`);
      writeFileSync(path.join(locks, lockName(index + 1)), JSON.stringify({ key: `real-${pid}`, pid, start }));
    }
    assert.deepEqual(leakedEntries(baseline, snapshotGuarded(dirs), dirs), [locks, path.join(locks, lockName(2))], 'exactly 2 leaks: new directory and child lock; foreign parent lock is allowed');
  } finally {
    const exited = once(child, 'exit');
    child.kill('SIGKILL');
    await exited;
  }
});
