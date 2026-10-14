import assert from 'node:assert/strict';
import * as run from '../../src/state/run.mjs';
import * as queue from '../../src/worker/queue.mjs';
import * as reaper from '../../src/util/reaper.mjs';
import { after, mock, test } from 'node:test';
import path from 'node:path';

const runs = new Map();
const workers = new Map();
const lookups = [];
const sweeps = [];
mock.module(new URL('../../src/state/run.mjs', import.meta.url).href, {
  namedExports: { ...run, readRun: async (id) => runs.get(id) },
});
mock.module(new URL('../../src/worker/queue.mjs', import.meta.url).href, {
  namedExports: { ...queue, liveWorker: (repo) => { lookups.push(repo); return workers.get(repo) ?? null; } },
});
mock.module(new URL('../../src/util/reaper.mjs', import.meta.url).href, {
  namedExports: { ...reaper, sweep: async (dir) => { sweeps.push(dir); } },
});
const { cleanupWorkers, runRootOf, stopPidAfter, stopRunAfter, stopWorker } = await import('./helpers.mjs');
after(() => mock.reset());

test('B113: stopWorker sweeps the pid registry even when SIGKILL cannot stop the worker', async () => {
  const signals = [];
  const kill = mock.method(process, 'kill', (pid, signal) => { signals.push([pid, signal]); return true; });
  let now = 0;
  const clock = mock.method(Date, 'now', () => { now += 10000; return now; });
  const pid = 31001;
  const runId = 'unstoppable-fixture';
  try {
    await assert.rejects(stopWorker({ pid, child: /** @type {any} */ ({ exitCode: null, signalCode: null }), exited: Promise.resolve() }, runId), /worker pid 31001 did not stop/);
    assert.deepEqual(signals, [[pid, 'SIGTERM'], [-pid, 'SIGKILL']]);
    assert.deepEqual(sweeps, [path.join(runRootOf(runId), 'pids')]);
  } finally { clock.mock.restore(); kill.mock.restore(); }
});

test('B113: stopRunAfter skips unsafe or stale pids and shares pid/run deduplication with stopPidAfter', async () => {
  const signals = [];
  const kill = mock.method(process, 'kill', (pid, signal) => {
    if (signal === 0) throw Object.assign(new Error('fixture exited'), { code: 'ESRCH' });
    signals.push([pid, signal]);
    return true;
  });
  const add = (id, pid, live = { pid, run: id }) => {
    const workspace = `fixture-${id}`;
    runs.set(id, { workspace, worker: { pid } });
    workers.set(workspace, live);
    stopRunAfter(id);
  };
  sweeps.length = 0;
  for (const [i, pid] of [undefined, 0, 1, -1, 1.5, '31002', process.pid].entries()) add(`unsafe-${i}`, pid);
  add('gone', 31003, null);
  add('replaced', 31004, { pid: 31005, run: 'replaced' });
  add('other-run', 31006, { pid: 31006, run: 'different' });
  add('live', 31007);
  stopRunAfter('live');
  add('pid-first', 31008);
  stopPidAfter(31008, 'pid-first');
  stopPidAfter(31009, 'run-first');
  add('run-first', 31009);
  try {
    await cleanupWorkers();
    assert.deepEqual(signals.sort((a, b) => a[0] - b[0]), [[31007, 'SIGTERM'], [31008, 'SIGTERM'], [31009, 'SIGTERM']]);
    assert.deepEqual(sweeps.sort(), ['live', 'pid-first', 'run-first'].map((id) => path.join(runRootOf(id), 'pids')).sort());
    assert.equal(lookups.includes('fixture-unsafe-0'), false);
    assert.ok(lookups.includes('fixture-live'));
  } finally { kill.mock.restore(); }
});
