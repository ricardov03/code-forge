import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, beforeEach, mock, test } from 'node:test';

const home = mkdtempSync(path.join(os.tmpdir(), 'file-locks-'));
const originalHome = process.env.HOME;
process.env.HOME = home;
assert.equal(os.homedir(), home, 'os.homedir must use the isolated HOME before imports');
const { tryLock, withLock, listBadLocks } = await import('../../src/util/file-locks.mjs');
const { checkLocks } = await import('../../src/doctor/local.mjs');
const { processStartTime } = await import('../../src/state/run.mjs');
const dir = path.join(home, '.code-forge', 'locks');
const lockPath = (key) => path.join(dir, `${createHash('sha256').update(key).digest('hex')}.lock`);
const put = (key, owner) => {
  const bytes = JSON.stringify(owner);
  writeFileSync(lockPath(key), bytes);
  return bytes;
};
beforeEach(() => { rmSync(dir, { recursive: true, force: true }); mkdirSync(dir, { recursive: true }); });
after(() => { process.env.HOME = originalHome; rmSync(home, { recursive: true, force: true }); });

async function exitedPid() {
  const child = spawn(process.execPath, ['-e', ''], { stdio: 'ignore' });
  try {
    await new Promise((resolve, reject) => { child.once('error', reject); child.once('exit', resolve); });
    return child.pid;
  } finally { if (child.exitCode === null) child.kill('SIGKILL'); }
}

test('free key: one live owner, held contender, release then acquisition', async () => {
  const key = 'free\0key';
  const first = await tryLock(key);
  assert.equal(first.ok, true, 'first owner acquires the free key');
  if (!first.ok) return;
  try {
    assert.deepEqual(JSON.parse(readFileSync(lockPath(key), 'utf8')), { key, pid: process.pid, start: await processStartTime(process.pid) });
    assert.deepEqual(await tryLock(key), { ok: false, result: 'held' }, 'second owner is held while first is live');
  } finally { first.release(); }
  assert.equal(existsSync(lockPath(key)), false, 'release removes exactly the owner file');
  const second = await tryLock(key);
  assert.equal(second.ok, true, 'second owner acquires after release');
  if (second.ok) { second.release(); second.release(); }
});

test('dead-owner break race: two afterRead arrivals yield exactly one holder', async () => {
  const key = 'break-race';
  put(key, { key, pid: await exitedPid(), start: 'exited child' });
  let arrivals = 0;
  let open;
  const gate = new Promise((resolve) => { open = resolve; });
  const afterRead = async () => { if (++arrivals === 2) open(); await gate; };
  const results = await Promise.all([tryLock(key, { afterRead }), tryLock(key, { afterRead })]);
  try {
    assert.equal(arrivals, 2, 'both breakers read the dead owner before either proceeds');
    assert.equal(results.filter((r) => r.ok).length, 1, 'exactly one breaker holds the lock');
    assert.deepEqual(results.filter((r) => !r.ok), [{ ok: false, result: 'held' }]);
    assert.equal(JSON.parse(readFileSync(lockPath(key), 'utf8')).pid, process.pid, 'winner replaces dead pid');
    assert.equal(readdirSync(dir).length, 1, 'only the winner lock remains');
  } finally { results.forEach((r) => { if (r.ok) r.release(); }); }
});

test('stale second breaker preserves the first owner against a third process', async () => {
  const key = 'three-process-race';
  put(key, { key, pid: await exitedPid(), start: 'dead' });
  let read;
  let resume;
  const readGate = new Promise((resolve) => { read = resolve; });
  const resumeGate = new Promise((resolve) => { resume = resolve; });
  const stale = tryLock(key, { afterRead: async () => { read(); await resumeGate; } });
  await readGate;
  const first = await tryLock(key);
  assert.equal(first.ok, true, 'first breaker owns the key before stale breaker resumes');
  const firstBytes = readFileSync(lockPath(key));
  const moduleUrl = new URL('../../src/util/file-locks.mjs', import.meta.url).href;
  const contender = () => {
    const child = spawnSync(process.execPath, ['--input-type=module', '-e', `
      const { tryLock } = await import(${JSON.stringify(moduleUrl)});
      console.log(JSON.stringify(await tryLock(${JSON.stringify(key)})));
    `], { env: { ...process.env, HOME: home }, encoding: 'utf8' });
    assert.equal(child.status, 0, child.stderr);
    return JSON.parse(child.stdout);
  };
  let third;
  const rename = fs.renameSync;
  const hook = mock.method(fs, 'renameSync', (from, to) => {
    rename(from, to);
    if (from === lockPath(key)) third = contender();
  });
  syncBuiltinESMExports();
  let second;
  try {
    resume();
    second = await stale;
    third ??= contender();
    assert.equal([first, second, third].filter((r) => r.ok).length, 1, 'exactly one of the three processes acquires the key');
    assert.deepEqual(readFileSync(lockPath(key)), firstBytes, 'stale breaker leaves the first owner file unchanged');
  } finally {
    hook.mock.restore();
    syncBuiltinESMExports();
    if (first.ok) first.release();
    if (second?.ok) second.release();
  }
});

test('live pid, other start time is broken; same start time is held', async () => {
  const key = 'recycled-pid';
  const bytes = put(key, { key, pid: process.pid, start: 'old start' });
  assert.deepEqual(await tryLock(key, { alive: () => true, probe: async () => 'old start' }), { ok: false, result: 'held' });
  assert.equal(readFileSync(lockPath(key), 'utf8'), bytes, 'same start time is never broken');
  const acquired = await tryLock(key, { alive: () => true, probe: async () => 'other start' });
  assert.equal(acquired.ok, true, 'a live recycled pid with another start time must be broken');
  if (acquired.ok) acquired.release();
});

test('break mutex records its live owner while the dead lock is replaced', async () => {
  const key = 'mutex-owner';
  put(key, { key, pid: await exitedPid(), start: 'dead' });
  let observed;
  const open = fs.openSync;
  const hook = mock.method(fs, 'openSync', (file, flags, mode) => {
    if (file === lockPath(key) && existsSync(`${file}.break`)) {
      observed = JSON.parse(readFileSync(`${file}.break`, 'utf8'));
    }
    return open(file, flags, mode);
  });
  syncBuiltinESMExports();
  let lock;
  try {
    lock = await tryLock(key);
    assert.equal(lock.ok, true);
    assert.deepEqual(observed, { key, pid: process.pid, start: await processStartTime(process.pid) });
    assert.equal(existsSync(`${lockPath(key)}.break`), false, 'successful breaker removes its mutex');
  } finally {
    hook.mock.restore();
    syncBuiltinESMExports();
    if (lock?.ok) lock.release();
  }
});

test('doctor names dead, malformed and unreadable break mutexes but leaves live ones alone', async () => {
  const deadPid = await exitedPid();
  const deadKey = 'dead-mutex';
  put(deadKey, { key: deadKey, pid: deadPid, start: 'dead' });
  const dead = `${lockPath(deadKey)}.break`;
  const deadBytes = JSON.stringify({ key: deadKey, pid: deadPid, start: 'dead' });
  writeFileSync(dead, deadBytes);
  const live = `${lockPath('live-mutex')}.break`;
  writeFileSync(live, JSON.stringify({ key: 'live-mutex', pid: process.pid, start: await processStartTime(process.pid) }));
  const malformed = `${lockPath('malformed-mutex')}.break`;
  writeFileSync(malformed, JSON.stringify({ key: 'malformed-mutex', pid: 2.5, start: 's' }));
  const unreadable = `${lockPath('unreadable-mutex')}.break`;
  mkdirSync(unreadable);
  const paths = [dead, malformed, unreadable].sort();
  assert.deepEqual(listBadLocks(), paths, 'all unusable break mutexes are reported; live mutex is omitted');
  assert.deepEqual(await checkLocks(), paths.map((p) => ({ id: 'locks', status: 'WARN', label: 'locks', detail: p })));
  assert.deepEqual(await tryLock(deadKey), { ok: false, result: 'held' }, 'a crashed mutex is never auto-broken');
  assert.equal(readFileSync(dead, 'utf8'), deadBytes, 'crashed mutex stays for human removal');
  assert.equal(JSON.parse(readFileSync(lockPath(deadKey), 'utf8')).pid, deadPid, 'blocked owner lock stays untouched');
});

test('unreadable and malformed locks stay held; doctor names one path per warning', async () => {
  mkdirSync(lockPath('unreadable'));
  const bad = [
    null,
    { key: 1, pid: 2, start: 's' },
    { key: 'bad-2', pid: 1, start: 's' },
    { key: 'bad-3', pid: 2.5, start: 's' },
    { key: 'bad-4', pid: 2 },
    { key: 'bad-5', pid: 2, start: 1 },
  ];
  bad.forEach((owner, i) => {
    const key = `bad-${i}`;
    if (owner && typeof owner.key === 'string') {
      assert.equal(owner.key, key, `${key}: fixture isolates its pid or start shape rule`);
    }
    put(key, owner);
  });
  writeFileSync(lockPath('json'), '{');
  const keys = ['unreadable', 'json', ...bad.map((_, i) => `bad-${i}`)];
  for (const key of keys) {
    let probed = 0;
    assert.deepEqual(await tryLock(key, { alive: () => { probed++; return false; } }), { ok: false, result: 'held' });
    assert.equal(probed, 0, `${key}: malformed owner is never probed or broken`);
    assert.equal(existsSync(lockPath(key)), true, `${key}: bad lock remains`);
  }
  const paths = keys.map(lockPath).sort();
  assert.deepEqual(await listBadLocks(), paths, 'every unreadable/bad lock is named exactly once');
  assert.deepEqual(await checkLocks(), paths.map((p) => ({ id: 'locks', status: 'WARN', label: 'locks', detail: p })));
  keys.forEach((k) => rmSync(lockPath(k), { recursive: true }));
  put('good', { key: 'good', pid: process.pid, start: 's' });
  assert.deepEqual(await listBadLocks(), [], 'well formed locks produce no bad paths');
  assert.deepEqual(await checkLocks(), [{ id: 'locks', status: 'OK', label: 'locks', detail: 'no unreadable or malformed locks' }]);
});

test('withLock owns during fn and releases after return and throw', async () => {
  const key = 'exit-paths';
  assert.equal(await withLock(key, () => {
    assert.equal(existsSync(lockPath(key)), true, 'returning fn runs with its lock file present');
    return 42;
  }), 42);
  assert.equal(existsSync(lockPath(key)), false, 'return removes the file');
  const failure = new Error('fn failed');
  await assert.rejects(withLock(key, () => {
    assert.equal(existsSync(lockPath(key)), true, 'throwing fn runs with its lock file present');
    throw failure;
  }), (err) => err === failure, 'the exact fn error is passed on');
  assert.equal(existsSync(lockPath(key)), false, 'throw removes the file');
});

test('boundary: changed bytes are untouched and an old release preserves a replacement', async () => {
  const key = 'changed';
  put(key, { key, pid: await exitedPid(), start: 'dead' });
  const replacement = JSON.stringify({ key, pid: process.pid, start: 'replacement' });
  const hook = mock.method(fs, 'renameSync');
  syncBuiltinESMExports();
  const result = await tryLock(key, { afterRead: async () => writeFileSync(lockPath(key), replacement) });
  try {
    assert.deepEqual(result, { ok: false, result: 'held' }, 'changed owner cannot be broken');
    assert.equal(hook.mock.callCount(), 0, 'changed owner is never renamed');
    assert.equal(readFileSync(lockPath(key), 'utf8'), replacement, 'changed bytes stay on their original path');
    assert.deepEqual(readdirSync(dir), [path.basename(lockPath(key))], 'only the unchanged lock file remains');
  } finally {
    hook.mock.restore();
    syncBuiltinESMExports();
    if (result.ok) result.release();
  }
  rmSync(lockPath(key));
  const owner = await tryLock(key);
  assert.equal(owner.ok, true);
  if (owner.ok) {
    writeFileSync(lockPath(key), replacement);
    owner.release();
    assert.equal(readFileSync(lockPath(key), 'utf8'), replacement, 'old release never removes replacement bytes');
  }
});

test('boundary: unknown start time stays held and withLock retries in FIFO order', async () => {
  const key = 'unknown';
  const bytes = put(key, { key, pid: process.pid, start: 's' });
  assert.deepEqual(await tryLock(key, { alive: () => true, probe: async () => null }), { ok: false, result: 'held' });
  assert.equal(readFileSync(lockPath(key), 'utf8'), bytes, 'unknown live start does not prove death');
  const order = [];
  const expectedStart = await processStartTime(process.pid);
  let read;
  let resume;
  const readGate = new Promise((resolve) => { read = resolve; });
  const resumeGate = new Promise((resolve) => { resume = resolve; });
  const opts = { pollMs: 1, alive: () => true, probe: async () => 's', afterRead: async () => { read(); await resumeGate; } };
  const first = withLock(key, () => {
    assert.equal(JSON.parse(readFileSync(lockPath(key), 'utf8')).start, expectedStart, 'FIFO fn owns its file, replacing the previous owner');
    order.push(1);
  }, opts);
  const second = withLock(key, () => { order.push(2); }, opts);
  // Racing first also makes an assertion failure in a missing-lock stub propagate promptly.
  await Promise.race([readGate, first]);
  assert.deepEqual(order, [], 'held key does not enter either fn');
  rmSync(lockPath(key));
  resume();
  await Promise.all([first, second]);
  assert.deepEqual(order, [1, 2], 'same-process waiters run in call order after held key is freed');
  assert.equal(existsSync(lockPath(key)), false, 'last FIFO holder releases');
});
