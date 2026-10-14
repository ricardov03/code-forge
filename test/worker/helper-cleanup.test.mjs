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
let sweepError = null;
mock.module(new URL('../../src/state/run.mjs', import.meta.url).href, {
  namedExports: { ...run, readRun: async (id) => runs.get(id) },
});
mock.module(new URL('../../src/worker/queue.mjs', import.meta.url).href, {
  namedExports: { ...queue, liveWorker: (repo) => { lookups.push(repo); return workers.get(repo) ?? null; } },
});
mock.module(new URL('../../src/util/reaper.mjs', import.meta.url).href, {
  namedExports: { ...reaper, sweep: async (dir) => { sweeps.push(dir); if (sweepError) throw sweepError; } },
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

test('B115: stopWorker preserves the exact stop error and attaches the failed sweep as cause', async () => {
  const signals = [];
  const kill = mock.method(process, 'kill', (pid, signal) => { signals.push([pid, signal]); return true; });
  let now = 0;
  const clock = mock.method(Date, 'now', () => { now += 10000; return now; });
  sweeps.length = 0;
  sweepError = new Error('fixture sweep failed');
  try {
    await assert.rejects(stopWorker({ pid: 31010, child: /** @type {any} */ ({ exitCode: null, signalCode: null }), exited: Promise.resolve() }, 'double-failure'), (err) => {
      assert.ok(err instanceof Error);
      assert.equal(err.message, 'worker pid 31010 did not stop');
      assert.equal(err.cause, sweepError);
      return true;
    });
    assert.deepEqual(signals, [[31010, 'SIGTERM'], [-31010, 'SIGKILL']]);
    assert.deepEqual(sweeps, [path.join(runRootOf('double-failure'), 'pids')]);
    const stopped = { pid: 31011, child: /** @type {any} */ ({ exitCode: 0, signalCode: null }), exited: Promise.resolve() };
    await assert.rejects(stopWorker(stopped, 'sweep-only'), (err) => err === sweepError);
    assert.deepEqual(sweeps, ['double-failure', 'sweep-only'].map((id) => path.join(runRootOf(id), 'pids')));
  } finally { sweepError = null; clock.mock.restore(); kill.mock.restore(); }
});

/** Inject a stop failure before signalling, and verify the exact error survives a failed sweep.
 * @param {unknown[]} errors
 */
async function checkUnattachableStopErrors(errors) {
  sweeps.length = 0;
  sweepError = new Error('fixture sweep failed');
  const logs = [];
  const logger = mock.method(console, 'error', (...args) => { logs.push(args); });
  try {
    for (const [index, stopError] of errors.entries()) {
      const child = { get exitCode() { throw stopError; }, signalCode: null };
      let rejected = false;
      try {
        await stopWorker({ pid: 31012, child: /** @type {any} */ (child), exited: Promise.resolve() }, `unattachable-${index}`);
      } catch (err) {
        rejected = true;
        assert.equal(err, stopError);
      }
      assert.equal(rejected, true);
    }
    assert.deepEqual(sweeps, errors.map((_, index) => path.join(runRootOf(`unattachable-${index}`), 'pids')));
    assert.deepEqual(logs, errors.map(() => ['worker pid sweep failed after stop failure:', sweepError]));
  } finally { sweepError = null; logger.mock.restore(); }
}

test('B115: primitive stop errors survive sweep failure unchanged', async () => {
  await checkUnattachableStopErrors(['fixture primitive', null, undefined, 0, false, Symbol('stop')]);
});

test('B115: frozen and accessor-backed stop errors survive sweep failure unchanged', async () => {
  const cause = new Error('original cause');
  const frozen = Object.freeze(new Error('frozen stop', { cause }));
  const accessor = Object.defineProperty(new Error('accessor stop'), 'cause', { get() { throw new Error('cause getter failed'); } });
  await checkUnattachableStopErrors([frozen, accessor]);
  assert.equal(frozen.cause, cause);
});

test('B115: stopWorker keeps an existing cause together with the sweep error', async () => {
  const cause = new Error('original stop cause');
  const stopError = new Error('fixture stop failed', { cause });
  const child = { get exitCode() { throw stopError; }, signalCode: null };
  sweeps.length = 0;
  sweepError = new Error('fixture sweep failed');
  try {
    await assert.rejects(stopWorker({ pid: 31013, child: /** @type {any} */ (child), exited: Promise.resolve() }, 'existing-cause'), (err) => {
      assert.equal(err, stopError);
      assert.ok(err.cause instanceof AggregateError);
      assert.deepEqual(err.cause.errors, [cause, sweepError]);
      return true;
    });
    assert.deepEqual(sweeps, [path.join(runRootOf('existing-cause'), 'pids')]);
  } finally { sweepError = null; }
});

test('B115: a logging failure cannot replace an unattachable stop error', async () => {
  const stopError = Object.freeze(new Error('frozen stop'));
  const child = { get exitCode() { throw stopError; }, signalCode: null };
  const logger = mock.method(console, 'error', () => { throw new Error('fixture logging failed'); });
  sweeps.length = 0;
  sweepError = new Error('fixture sweep failed');
  try {
    await assert.rejects(stopWorker({ pid: 31014, child: /** @type {any} */ (child), exited: Promise.resolve() }, 'logging-failure'), (err) => err === stopError);
    assert.deepEqual(sweeps, [path.join(runRootOf('logging-failure'), 'pids')]);
    assert.equal(logger.mock.calls.length, 1);
  } finally { sweepError = null; logger.mock.restore(); }
});
