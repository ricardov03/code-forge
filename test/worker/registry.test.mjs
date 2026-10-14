import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { mock, test } from 'node:test';

const { announceWorker, beatWorker, liveWorker, queueDir, readWorker, retractWorker } = await import('../../src/worker/queue.mjs');
const { readStartTime, UNKNOWN_START_TIME } = await import('../../src/util/reaper.mjs');
const { bootWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { checkLocks } = await import('../../src/doctor/local.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');

const dir = (repo) => path.join(repo, '.code-forge', 'workers');
const entries = (repo) => existsSync(dir(repo)) ? readdirSync(dir(repo)).sort() : [];
const fileOf = (repo, w) => path.join(dir(repo), `${w.pid}-${w.start_time}.json`);
const body = (run, extra = {}) => ({ pid: process.pid, start_time: 'old start', run, started_at: new Date().toISOString(), heartbeat_at: new Date().toISOString(), ...extra });
function put(repo, name, value) {
  mkdirSync(dir(repo), { recursive: true });
  const file = path.join(dir(repo), name);
  writeFileSync(file, JSON.stringify(value));
  return file;
}
async function deps(hooks = {}) {
  return { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow: async () => {}, expiryCheck: async () => {}, hooks };
}
function gate() {
  let open;
  const promise = new Promise((resolve) => { open = () => resolve(undefined); });
  return { promise, open: () => open() };
}

test('clause 1: boot announces one registry entry and legacy file, beats both on its timer, and clean stop removes both', async () => {
  const { repo, runId } = await makeRepo();
  const injected = await deps();
  mkdirSync(dir(repo), { recursive: true, mode: 0o755 });
  mock.timers.enable({ apis: ['Date', 'setInterval'], now: Date.now() });
  let worker;
  try {
    worker = await bootWorker({ runId, cwd: repo }, injected);
    const self = readWorker(repo);
    assert.equal(self?.pid, process.pid, 'boot publishes its pid before run/once');
    assert.deepEqual(entries(repo), [`${self.pid}-${self.start_time}.json`], 'boot writes exactly one identity entry');
    assert.equal(statSync(dir(repo)).mode & 0o777, 0o700, 'registry directory is private');
    assert.equal(statSync(fileOf(repo, self)).mode & 0o777, 0o600, 'registry entry is private');
    assert.deepEqual(readdirSync(queueDir(repo)), ['worker.json'], 'registry lives outside the queue');
    assert.deepEqual(JSON.parse(readFileSync(fileOf(repo, self), 'utf8')), self, 'registry and legacy bodies agree');
    mock.timers.tick(5000);
    const beaten = readWorker(repo);
    assert.equal(Date.parse(beaten.heartbeat_at) - Date.parse(self.heartbeat_at), 5000, 'timer advances legacy heartbeat');
    assert.deepEqual(JSON.parse(readFileSync(fileOf(repo, self), 'utf8')), beaten, 'same timer beats registry');
    assert.equal(statSync(fileOf(repo, self)).mode & 0o777, 0o600, 'heartbeat keeps the registry entry private');
    assert.equal(await worker.once(), 0, 'own re-announcement drains without refusing itself');
    assert.deepEqual(entries(repo), [], 'clean stop removes its one registry entry');
    assert.equal(readWorker(repo), null, 'clean stop removes its legacy file');
    mock.timers.tick(5000);
    assert.deepEqual(entries(repo), [], 'stopped heartbeat creates no entries');
  } finally { worker?.release(); mock.timers.reset(); }
});

test('clause 2: a second boot is worker-running at limit one and writes no second entry', async () => {
  const { repo, runId } = await makeRepo();
  const injected = await deps();
  const first = await bootWorker({ runId, cwd: repo }, injected);
  try {
    const before = entries(repo);
    await assert.rejects(bootWorker({ runId, cwd: repo }, injected), { code: 'worker-running' }, 'even another boot in this pid must be refused');
    assert.equal(before.length, 1, 'first boot owns exactly one entry');
    assert.deepEqual(entries(repo), before, 'refusal leaves exactly the original entry');
  } finally { first.release(); }
});

test('clause 3: boot removes dead/recycled owners with fresh heartbeats; unreadable and bad shapes block unchanged and doctor names each path', async () => {
  const { repo, runId } = await makeRepo();
  const child = spawn(process.execPath, ['-e', ''], { stdio: 'ignore' });
  await new Promise((resolve, reject) => { child.once('exit', resolve); child.once('error', reject); });
  const dead = put(repo, `${child.pid}-dead.json`, body(runId, { pid: child.pid, start_time: 'dead' }));
  const recycled = put(repo, `${process.pid}-old start.json`, body(runId));
  let worker;
  await assert.doesNotReject(async () => { worker = await bootWorker({ runId, cwd: repo }, await deps()); }, 'fresh heartbeat cannot rescue a recycled pid at boot');
  try {
    assert.equal(existsSync(dead), false, 'dead owner is removed even with fresh heartbeat');
    assert.equal(existsSync(recycled), false, 'recycled pid is removed even with fresh heartbeat');
    assert.equal(entries(repo).length, 1, 'only the booted worker remains');
  } finally { worker.release(); }
  const bad = [null, body(runId, { pid: 1 }), body(runId, { pid: 2.5 }), body(runId, { start_time: undefined }), body(runId, { start_time: 'bad-run', run: 1 }), body(runId, { start_time: 'bad-started', started_at: 1 }), body(runId, { start_time: 'bad-beat', heartbeat_at: 'invalid' })];
  const paths = bad.map((value, i) => put(repo, value ? `${value.pid}-${value.start_time}.json` : `bad-${i}.json`, value));
  const unreadable = path.join(dir(repo), 'unreadable.json');
  mkdirSync(unreadable);
  const malformed = path.join(dir(repo), 'malformed.json');
  writeFileSync(malformed, '{');
  paths.push(unreadable, malformed);
  for (const file of paths) {
    // Test each bad entry alone, so no other entry can hide an unsafe shape rule.
    const hidden = paths.filter((p) => p !== file);
    hidden.forEach((p) => renameSync(p, `${p}.held`));
    try {
      const before = statSync(file).isDirectory() ? null : readFileSync(file);
      await assert.rejects(bootWorker({ runId, cwd: repo }, await deps()), { code: 'worker-running' }, `${file}: blocks boot`);
      assert.equal(existsSync(file), true, `${file}: never removed`);
      if (before) assert.deepEqual(readFileSync(file), before, `${file}: bytes unchanged`);
      assert.deepEqual((await checkLocks(repo)).filter((r) => r.id === 'worker-registry'), [{ id: 'worker-registry', status: 'WARN', label: 'worker registry', detail: file }], 'doctor names exactly the bad path');
      assert.equal(readWorker(repo), null, 'refused boot publishes no legacy announcement');
    } finally { hidden.forEach((p) => renameSync(`${p}.held`, p)); }
  }
});

test('clause 4: live registry serves review-file wait without worker.json; only no live registered worker is worker_down', async () => {
  const { repo, runId } = await makeRepo();
  const startTime = readStartTime(process.pid);
  assert.notEqual(startTime, null, 'current process start time is readable');
  const fresh = body(runId, { start_time: startTime });
  const file = put(repo, `${fresh.pid}-${fresh.start_time}.json`, fresh);
  assert.deepEqual(liveWorker(repo), fresh, 'registry preserves full liveWorker return shape');
  const ticket = 'a'.repeat(24);
  const wait = async () => {
    let out = '';
    const code = await runReviewFile(['--wait', ticket, '--max', '0ms'], { cwd: repo, pollMs: 1, stdout: { write: (s) => { out += s; } } });
    return { code, json: JSON.parse(out) };
  };
  assert.deepEqual(await wait(), { code: 0, json: { ticket, status: 'pending', reason: 'wait_timeout' } }, 'live registry avoids worker_down without legacy file');
  rmSync(file);
  assert.equal(liveWorker(repo), null, 'empty registry has no live worker');
  assert.deepEqual(await wait(), { code: 3, json: { ticket, status: 'worker_down' } }, 'no live entry returns worker_down');
});

test('clause 5: forced count/announce race admits exactly one boot and refuses the other worker-running', async () => {
  const { repo, runId } = await makeRepo();
  const counted = gate();
  const resume = gate();
  const attempted = gate();
  const counts = [];
  let attempts = 0;
  const injected = await deps({ beforeLock: () => { if (++attempts === 2) attempted.open(); }, afterCount: async (count) => { counts.push(count); counted.open(); await resume.promise; } });
  const results = [];
  const boot = () => bootWorker({ runId, cwd: repo }, injected).then((worker) => { results.push({ worker }); }, (error) => { results.push({ error }); });
  const first = boot();
  await Promise.race([counted.promise, first]);
  assert.deepEqual(counts, [0], 'first boot reaches count before any announcement');
  const second = boot();
  try {
    await Promise.race([attempted.promise, second]);
    // The contender signals immediately before entering withLock. Its continuation has queued
    // before this observer runs; a missing lock instead enters afterCount with count zero.
    assert.deepEqual(counts, [0], 'lock keeps the second count out while the first announcement is held');
    resume.open();
    await Promise.all([first, second]);
    assert.deepEqual(counts, [0, 1], 'contender counts the first announcement after the lock releases');
    assert.deepEqual(entries(repo), [`${process.pid}-${readWorker(repo).start_time}.json`], 'exactly the admitted worker owns one entry');
  } finally { resume.open(); await Promise.all([first, second]); results.forEach((r) => r.worker?.release()); }
  assert.equal(results.filter((r) => r.worker).length, 1, 'exactly one boot announces');
  assert.deepEqual(results.filter((r) => r.error).map((r) => r.error.code), ['worker-running'], 'exactly one boot refuses worker-running');
});

test('boundary: boot prunes a fresh recycled pid while liveWorker still reports it', async () => {
  const { repo, runId } = await makeRepo();
  const registered = body(runId);
  const file = put(repo, `${registered.pid}-${registered.start_time}.json`, registered);
  // liveWorker keeps the B30 fresh-heartbeat rule on purpose (issue #2: a sandboxed ps reads another time zone; pinned by test/worker/heartbeat.test.mjs:81); only the boot prune is strict.
  assert.deepEqual(liveWorker(repo), registered, 'sandbox reader lets a fresh heartbeat win over a start-time mismatch');
  let worker;
  await assert.doesNotReject(async () => { worker = await bootWorker({ runId, cwd: repo }, await deps()); }, 'known start-time mismatch cannot block boot with a fresh heartbeat');
  try {
    assert.equal(existsSync(file), false, 'fresh mismatched owner is removed');
    assert.equal(entries(repo).length, 1, 'only the booted worker remains');
  } finally { worker.release(); }
});

test('boundary: an unknown start time with a fresh heartbeat is kept and blocks boot', async () => {
  const { repo, runId } = await makeRepo();
  const registered = body(runId, { start_time: UNKNOWN_START_TIME });
  const file = put(repo, `${registered.pid}-${registered.start_time}.json`, registered);
  await assert.rejects(bootWorker({ runId, cwd: repo }, await deps()), { code: 'worker-running' }, 'fresh unknown owner blocks boot');
  assert.deepEqual(JSON.parse(readFileSync(file, 'utf8')), registered, 'fresh unknown owner is kept unchanged');
  assert.equal(readWorker(repo), null, 'refused boot publishes no legacy announcement');
});

test('boundary: legacy first owner is preserved and another owner heartbeat/retraction cannot alter it', async () => {
  const { repo, runId } = await makeRepo();
  const first = announceWorker(repo, { pid: process.pid, run: runId });
  const second = announceWorker(repo, { pid: process.pid + 1, run: runId });
  try {
    assert.equal(entries(repo).length, 2, 'each announcement owns a separate entry');
    assert.deepEqual(readWorker(repo), first, 'first worker keeps legacy announcement');
    assert.equal(beatWorker(repo, second, new Date(Date.now() + 1000)), true, 'second worker beats its registry entry');
    assert.deepEqual(readWorker(repo), first, 'second heartbeat does not overwrite first legacy owner');
    retractWorker(repo, second.pid);
    assert.deepEqual(entries(repo), [path.basename(fileOf(repo, first))], 'second retraction removes only second entry');
    assert.deepEqual(readWorker(repo), first, 'second retraction preserves first legacy owner');
  } finally { retractWorker(repo, first.pid); retractWorker(repo, second.pid); }
});
