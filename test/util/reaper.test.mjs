import assert from 'node:assert/strict';
import childProcess, { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdirSync, readdirSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import { syncBuiltinESMExports } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { after, before, mock, test } from 'node:test';
import {
  isAlive,
  groupMembers,
  listEntries,
  readStartTime,
  registerPid,
  sweep,
  sweepProcesses,
  UNKNOWN_START_TIME,
} from '../../src/util/reaper.mjs';

let PARENT = '';
/** Children this file spawned that have not exited yet; each removes itself on 'exit'. */
const live = new Set();

test('sweep lists orphans of zombie owners even when the recorded start time matches', async () => {
  const started = 'Wed Oct 21 10:00:00 2026';
  for (const state of ['S', 'Z', 'Z+']) {
    const report = await sweepProcesses({
      psText: `501 810 1 ${state} 0 ${started} node owner
501 811 1 S 1024 Wed Oct 21 10:00:01 2026 node helper`,
      uid: 501,
      orphansOnly: true,
      readEnv: () => ({ CODE_FORGE_OWNER_PID: '810', CODE_FORGE_OWNER_START_TIME: started }),
    });
    assert.deepEqual(report.items.map((p) => [p.kind, p.pid]), state === 'S' ? [] : [['orphan', 811]], state);
  }
});

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

const STARTED = 'Wed Oct 21 10:00:00 2026';

/**
 * Model the OS boundaries, recording signals without sending any to real processes.
 * @param {{pid:number,ppid:number,pgid:number,state:string,command:string}[]} rows
 * @param {(os: {psText:string, alive:Set<number>, starts:Map<number,string|null>, calls:[number,string|number][]}) => Promise<void>} body
 */
async function withProcessTable(rows, body) {
  const alive = new Set(rows.map((p) => p.pid));
  const starts = new Map(rows.map((p) => [p.pid, /** @type {string|null} */ (STARTED)]));
  /** @type {[number,string|number][]} */
  const calls = [];
  const psText = rows.map((p) => `${process.getuid()} ${p.pid} ${p.ppid} ${p.state} 1024 ${STARTED} ${p.command}`).join('\n');
  const ps = mock.method(childProcess, 'execFileSync', /** @type {typeof childProcess.execFileSync} */ ((file, args) => {
    assert.ok(String(file).endsWith('ps'), 'only ps is invoked');
    if (args.includes('lstart=')) return starts.get(Number(args.at(-1))) ?? '';
    const current = rows.filter((p) => alive.has(p.pid));
    if (args.includes('pid=,pgid=,lstart=')) return current.map((p) => `${p.pid} ${p.pgid} ${STARTED}`).join('\n');
    assert.ok(args.includes('uid=,pid=,ppid=,stat=,rss=,lstart=,command='));
    return psText.split('\n').filter((line) => alive.has(Number(line.split(' ')[1]))).join('\n');
  }));
  const kill = mock.method(process, 'kill', (/** @type {number} */ pid, /** @type {string|number} */ signal) => {
    if (signal === 0) {
      if (alive.has(pid)) return true;
      throw Object.assign(new Error('no such process'), { code: 'ESRCH' });
    }
    calls.push([pid, signal]);
    for (const p of rows) {
      if ((p.pid === pid || p.pgid === -pid) && !p.state.startsWith('Z')) alive.delete(p.pid);
    }
    return true;
  });
  syncBuiltinESMExports();
  try { await body({ psText, alive, starts, calls }); }
  finally { ps.mock.restore(); kill.mock.restore(); syncBuiltinESMExports(); }
}

test('zombie parents remain eligible workers, orphans and registry leaders; protected pids never receive signals', async () => {
  const root = await registryDir('zombie-parents');
  const run = path.join(root, 'dead-run');
  const pids = path.join(run, 'pids');
  mkdirSync(pids, { recursive: true, mode: 0o700 });
  writeFileSync(path.join(run, 'owner.json'), JSON.stringify({ pid: 90000010, start_time: STARTED }));
  const worker = 'node /fixture/code-forge.mjs worker --run r-ended';
  const rows = [
    { pid: 90000020, ppid: 1, pgid: 90000020, state: 'S', command: worker },
    { pid: 90000030, ppid: 1, pgid: 90000030, state: 'S', command: 'node helper' },
    { pid: 90000040, ppid: 1, pgid: 90000040, state: 'S', command: 'node server' },
    ...[90000020, 90000030, 90000040].map((pid) => ({ pid: pid + 1, ppid: pid, pgid: pid, state: 'Z+', command: worker })),
    { pid: 1, ppid: 0, pgid: 1, state: 'S', command: worker },
    { pid: process.pid, ppid: process.ppid, pgid: process.pid, state: 'S', command: worker },
    { pid: process.ppid, ppid: 90000001, pgid: process.ppid, state: 'S', command: worker },
    { pid: 90000001, ppid: 1, pgid: 90000001, state: 'S', command: worker },
  ];
  for (const p of rows.filter((p) => p.pid > 1)) {
    writeFileSync(path.join(pids, `${p.pid}.json`), JSON.stringify({ pid: p.pid, start_time: STARTED, argv0: 'node' }));
  }
  await withProcessTable(rows, async ({ psText, alive, calls }) => {
    const opts = { root, psText, graceMs: 0, readRun: async () => ({ status: 'ended' }),
      readEnv: (p) => p.pid === 90000030 ? { CODE_FORGE_OWNER_PID: '90000010' } : null };
    const expected = [['worker', 90000020], ['orphan', 90000030], ['registry', 90000040]];
    const dry = await sweepProcesses(opts);
    assert.deepEqual(dry.items.map((p) => [p.kind, p.pid]), expected);
    assert.equal(dry.zombies.length, 3);
    assert.deepEqual(calls, [], 'dry run sends no signals');
    const applied = await sweepProcesses({ ...opts, apply: true });
    assert.deepEqual(applied.items.map((p) => [p.kind, p.pid, p.action]), expected.map((p) => [...p, 'stopped']));
    assert.deepEqual(calls, [[90000020, 'SIGTERM'], [90000030, 'SIGTERM'], [90000040, 'SIGTERM']], 'only verified parents receive bare-pid signals');
    assert.deepEqual([90000020, 90000030, 90000040].map((pid) => alive.has(pid)), [false, false, false]);
    assert.ok(rows.filter((p) => ![90000020, 90000030, 90000040].includes(p.pid)).every((p) => alive.has(p.pid)), 'zombies, pid 1, self and ancestors survive');
  });
});

test('registry apply skips a live pid with an unreadable start time and reports stopped only on death or known reuse', async () => {
  for (const state of ['unreadable', 'matching', 'reused', 'dead']) {
    const root = await registryDir(`registry-${state}`);
    const run = path.join(root, 'dead-run');
    const pids = path.join(run, 'pids');
    mkdirSync(pids, { recursive: true, mode: 0o700 });
    const pid = 90000040;
    writeFileSync(path.join(run, 'owner.json'), JSON.stringify({ pid: 90000010, start_time: STARTED }));
    writeFileSync(path.join(pids, `${pid}.json`), JSON.stringify({ pid, start_time: STARTED, argv0: 'node' }));
    await withProcessTable([{ pid, ppid: 1, pgid: pid, state: 'S', command: 'node server' }], async ({ psText, alive, starts, calls }) => {
      starts.set(pid, state === 'unreadable' || state === 'dead' ? null : state === 'reused' ? 'Wed Oct 21 10:00:01 2026' : STARTED);
      if (state === 'dead') alive.delete(pid);
      // Deny delivery to the matching live process; successful cleanup is tested above.
      const denied = mock.method(process, 'kill', (target, signal) => {
        if (signal === 0) {
          if (alive.has(target)) return true;
          throw Object.assign(new Error('no such process'), { code: 'ESRCH' });
        }
        calls.push([target, signal]);
        throw Object.assign(new Error('permission denied'), { code: 'EPERM' });
      });
      try {
        const report = await sweepProcesses({ root, psText, apply: true, graceMs: 0, readEnv: () => null });
        assert.deepEqual(report.items.map((p) => [p.kind, p.pid, p.action]), [['registry', pid, ['dead', 'reused'].includes(state) ? 'stopped' : 'skipped']], state);
        assert.equal(alive.has(pid), state !== 'dead', `${state}: live process is preserved`);
        if (state !== 'matching') assert.deepEqual(calls, [], `${state}: no signals sent`);
      } finally { denied.mock.restore(); }
    });
  }
});

test('B135: readStartTime reads this process without ps on PATH and rejects absent pids', () => {
  const oldPath = process.env.PATH;
  process.env.PATH = '/nonexistent-b135-bin';
  try {
    const own = readStartTime(process.pid);
    assert.equal(typeof own, 'string');
    assert.match(/** @type {string} */ (own), /\d{4}$/);
    assert.equal(readStartTime(0), null);
    assert.equal(readStartTime(2 ** 22 + 12345), null);
  } finally { process.env.PATH = oldPath; }
});

test('registerPid writes {pid, start_time, argv0} with the live start time', { timeout: 30000 }, async () => {
  const dir = await registryDir('reg');
  const pid = await spawnIdle(false);
  const file = registerPid(dir, pid, process.execPath);

  assert.equal(file, path.join(dir, `${pid}.json`));
  assert.deepEqual(listEntries(dir), [{ pid, start_time: readStartTime(pid), argv0: process.execPath }]);
  process.kill(-pid, 'SIGKILL');
});

test('B135 boundary: group members are found without ps on PATH', { timeout: 30000 }, async () => {
  const child = track(spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' }));
  const { execFileSync } = await import('node:child_process');
  const pgid = Number(execFileSync('/bin/ps', ['-o', 'pgid=', '-p', String(child.pid)], { encoding: 'utf8' }).trim());
  const oldPath = process.env.PATH;
  process.env.PATH = '/nonexistent-b135-bin';
  try {
    const found = groupMembers(pgid);
    assert.equal(found.has(child.pid), true, 'the child is one member of its actual process group');
    assert.equal(found.get(child.pid), readStartTime(child.pid));
    assert.equal(groupMembers(2 ** 22 + 12345).size, 0, 'nonexistent group has zero members');
  } finally { process.env.PATH = oldPath; child.kill('SIGKILL'); }
});

test('sweep kills a registered child that TRAPS SIGTERM (SIGKILL after the grace) and clears its entry', { timeout: 30000 }, async () => {
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

test('sweep leaves alone a registered pid whose live start time differs from the recorded one', { timeout: 30000 }, async () => {
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

test('sweep never kills a registered pid whose start time is unknown, and keeps its entry', { timeout: 30000 }, async () => {
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

test('sweep clears the entry of an already-dead pid without signalling, and skips malformed files', { timeout: 30000 }, async () => {
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

test('a leader that dies of SIGTERM is never signalled again: exactly one signal, to its group', { timeout: 30000 }, async () => {
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

test('EPERM on the group signal: the pid is reported unknown and never retried', { timeout: 30000 }, async () => {
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

test('registry sweep never signals the current process or its ancestors', async () => {
  const dir = await registryDir('protected');
  registerPid(dir, process.pid, 'node');
  registerPid(dir, process.ppid, 'parent');
  await withKillSpy(() => {}, async (calls) => {
    const result = await sweep(dir, { graceMs: 0 });
    assert.deepEqual(calls, [], 'zero signals to this process or ancestors');
    assert.deepEqual(result.killed, [], 'zero protected processes killed');
  });
});
