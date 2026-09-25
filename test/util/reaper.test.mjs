import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, readdirSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { after, before, mock, test } from 'node:test';
import {
  isAlive,
  listEntries,
  readStartTime,
  registerPid,
  sweep,
  UNKNOWN_START_TIME,
} from '../../src/util/reaper.mjs';

let PARENT = '';
/** Children this file spawned that have not exited yet; each removes itself on 'exit'. */
const live = new Set();

/** @param {import('node:child_process').ChildProcess} child */
function track(child) {
  live.add(child);
  child.once('exit', () => live.delete(child));
  return child;
}

before(async () => {
  PARENT = await mkdtemp(path.join(os.tmpdir(), 'code-forge-reaper-'));
});

after(async () => {
  // Only children that are still running (Node has not reaped them), so no pid here can have
  // been reused by another program. Each is a detached group leader: kill its group.
  for (const child of live) {
    if (child.exitCode === null && child.signalCode === null && typeof child.pid === 'number') {
      try {
        process.kill(-child.pid, 'SIGKILL');
      } catch {
        child.kill('SIGKILL');
      }
    }
  }
  if (PARENT) await rm(PARENT, { recursive: true, force: true });
});

/**
 * A detached (group-leader) idle child; with `trapSigterm` it ignores SIGTERM. Resolves once the
 * child has installed its handler and said so.
 * @param {boolean} trapSigterm
 * @returns {Promise<number>}
 */
async function spawnIdle(trapSigterm) {
  const code = `${trapSigterm ? "process.on('SIGTERM', () => {});" : ''} process.stdout.write('ready'); setInterval(() => {}, 1000);`;
  const child = track(spawn(process.execPath, ['-e', code], { detached: true, stdio: ['ignore', 'pipe', 'ignore'] }));
  assert.equal(typeof child.pid, 'number');
  await once(/** @type {import('node:stream').Readable} */ (child.stdout), 'data');
  return /** @type {number} */ (child.pid);
}

/** @param {string} name */
async function registryDir(name) {
  return mkdtemp(path.join(PARENT, `${name}-`));
}

test('readStartTime reads this process and returns null for a pid that does not exist', () => {
  const own = readStartTime(process.pid);
  assert.equal(typeof own, 'string');
  assert.match(/** @type {string} */ (own), /\d{4}$/);
  assert.equal(readStartTime(0), null);
  assert.equal(readStartTime(2 ** 22 + 12345), null);
});

test('registerPid writes {pid, start_time, argv0} with the live start time', async () => {
  const dir = await registryDir('reg');
  const pid = await spawnIdle(false);
  const file = registerPid(dir, pid, process.execPath);

  assert.equal(file, path.join(dir, `${pid}.json`));
  assert.deepEqual(listEntries(dir), [{ pid, start_time: readStartTime(pid), argv0: process.execPath }]);
  process.kill(-pid, 'SIGKILL');
});

test('sweep kills a registered child that TRAPS SIGTERM (SIGKILL after the grace) and clears its entry', { timeout: 8000 }, async () => {
  const dir = await registryDir('trap');
  const pid = await spawnIdle(true);
  registerPid(dir, pid, process.execPath);

  const result = await sweep(dir, { graceMs: 300 });

  assert.deepEqual(result.killed, [{ pid, signal: 'SIGKILL' }]);
  assert.deepEqual([result.mismatched, result.unknown, result.stale], [[], [], []]);
  await sleep(50);
  assert.equal(isAlive(pid), false);
  assert.deepEqual(readdirSync(dir), []);
});

test('sweep leaves alone a registered pid whose live start time differs from the recorded one', { timeout: 8000 }, async () => {
  const dir = await registryDir('mismatch');
  const pid = await spawnIdle(false);
  writeFileSync(
    path.join(dir, `${pid}.json`),
    JSON.stringify({ pid, start_time: 'Mon Jan 1 00:00:00 2001', argv0: 'node' }),
  );

  const result = await sweep(dir, { graceMs: 300 });
  await sleep(350);

  assert.deepEqual(result.mismatched, [pid]);
  assert.deepEqual(result.killed, []);
  assert.equal(isAlive(pid), true);
  // The stale entry is dropped; the process is not ours to touch.
  assert.equal(existsSync(path.join(dir, `${pid}.json`)), false);
  process.kill(-pid, 'SIGKILL');
});

test('sweep never kills a registered pid whose start time is unknown, and keeps its entry', { timeout: 8000 }, async () => {
  const dir = await registryDir('unknown');
  const pid = await spawnIdle(false);
  writeFileSync(path.join(dir, `${pid}.json`), JSON.stringify({ pid, start_time: UNKNOWN_START_TIME, argv0: 'node' }));

  const result = await sweep(dir, { graceMs: 300 });
  await sleep(350);

  assert.deepEqual(result.unknown, [pid]);
  assert.deepEqual(result.killed, []);
  assert.equal(isAlive(pid), true);
  assert.equal(existsSync(path.join(dir, `${pid}.json`)), true);
  process.kill(-pid, 'SIGKILL');
});

test('sweep clears the entry of an already-dead pid without signalling, and skips malformed files', { timeout: 8000 }, async () => {
  const dir = await registryDir('stale');
  const child = spawn(process.execPath, ['-e', ''], { stdio: 'ignore' });
  const deadPid = /** @type {number} */ (child.pid);
  await once(child, 'exit');
  await sleep(50);
  writeFileSync(path.join(dir, `${deadPid}.json`), JSON.stringify({ pid: deadPid, start_time: 'x', argv0: 'node' }));
  writeFileSync(path.join(dir, '123.json'), 'not json');
  writeFileSync(path.join(dir, `${process.pid}.txt`), '{}');

  const result = await sweep(dir, { graceMs: 300 });

  assert.deepEqual(result, { killed: [], mismatched: [], unknown: [], stale: [deadPid] });
  assert.deepEqual(readdirSync(dir).sort(), ['123.json', `${process.pid}.txt`].sort());
});

/**
 * Run `body` with process.kill wrapped: signal-0 probes pass through untouched and are not
 * recorded; every real signal is recorded and then handled by `onSignal`.
 * @param {(target: number, signal: string | number) => void} onSignal
 * @param {(calls: [number, string | number][]) => Promise<void>} body
 */
async function withKillSpy(onSignal, body) {
  const original = process.kill.bind(process);
  /** @type {[number, string | number][]} */
  const calls = [];
  const spy = mock.method(process, 'kill', (/** @type {number} */ target, /** @type {any} */ signal) => {
    if (signal === 0) return original(target, 0);
    calls.push([target, signal]);
    onSignal(target, signal);
    return true;
  });
  try {
    await body(calls);
  } finally {
    spy.mock.restore();
  }
}

test('a leader that dies of SIGTERM is never signalled again: exactly one signal, to its group', { timeout: 8000 }, async () => {
  const dir = await registryDir('term-dies');
  const pid = await spawnIdle(false);
  registerPid(dir, pid, process.execPath);
  const original = process.kill.bind(process);

  await withKillSpy((target, signal) => original(target, /** @type {any} */ (signal)), async (calls) => {
    const result = await sweep(dir, { graceMs: 2000 });
    assert.deepEqual(result.killed, [{ pid, signal: 'SIGTERM' }]);
    assert.deepEqual(calls, [[-pid, 'SIGTERM']]);
  });
  assert.equal(isAlive(pid), false);
});

test('EPERM on the group signal: the pid is reported unknown and never retried', { timeout: 8000 }, async () => {
  const dir = await registryDir('eperm');
  const pid = await spawnIdle(false);
  registerPid(dir, pid, process.execPath);

  await withKillSpy(
    () => {
      throw Object.assign(new Error('EPERM'), { code: 'EPERM' });
    },
    async (calls) => {
      const result = await sweep(dir, { graceMs: 200 });
      assert.deepEqual(result, { killed: [], mismatched: [], unknown: [pid], stale: [] });
      assert.deepEqual(calls, [[-pid, 'SIGTERM']]);
    },
  );
  assert.equal(isAlive(pid), true);
  assert.equal(existsSync(path.join(dir, `${pid}.json`)), true);
  process.kill(-pid, 'SIGKILL');
});
