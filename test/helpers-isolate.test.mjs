import assert from 'node:assert/strict';
import { mkdirSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { leakedEntries, snapshotGuarded } from './helpers/isolate.mjs';

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
