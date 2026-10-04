import assert from 'node:assert/strict';
import { test } from 'node:test';
import { keyedLock, semaphore } from '../../src/util/locks.mjs';

/** B40: in-process keyed locks and the counting semaphore. */

/** A promise and its resolver, so a test decides when a holder finishes. */
function deferred() {
  /** @type {(v?: unknown) => void} */
  let resolve = () => {};
  const promise = new Promise((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

/** Let every queued microtask (and the next macrotask turn) run. */
const settle = () => new Promise((r) => setImmediate(r));

test('keyedLock: 3 calls on one key run one at a time in call order (FIFO); max active 1', async () => {
  const log = /** @type {string[]} */ ([]);
  let active = 0;
  let maxActive = 0;
  const gates = [deferred(), deferred(), deferred()];
  const runs = gates.map((g, n) =>
    keyedLock('k-fifo', async () => {
      active += 1;
      maxActive = Math.max(maxActive, active);
      log.push(`start${n}`);
      await g.promise;
      log.push(`end${n}`);
      active -= 1;
      return n;
    }),
  );
  await settle();
  assert.deepEqual(log, ['start0']);
  gates[2].resolve(); // finishing out of order changes nothing: call 2 has not started
  gates[1].resolve();
  await settle();
  assert.deepEqual(log, ['start0']);
  gates[0].resolve();
  assert.deepEqual(await Promise.all(runs), [0, 1, 2]);
  assert.deepEqual(log, ['start0', 'end0', 'start1', 'end1', 'start2', 'end2']);
  assert.equal(maxActive, 1);
});

test('keyedLock: 2 different keys run together (both started before either ends)', async () => {
  const log = /** @type {string[]} */ ([]);
  const a = deferred();
  const b = deferred();
  const ra = keyedLock('k-a', async () => {
    log.push('startA');
    await a.promise;
  });
  const rb = keyedLock('k-b', async () => {
    log.push('startB');
    await b.promise;
  });
  await settle();
  assert.deepEqual(log, ['startA', 'startB']);
  a.resolve();
  b.resolve();
  await Promise.all([ra, rb]);
});

test('keyedLock: a throw (sync and async) rejects that call and releases the key — the next call runs', async () => {
  const first = keyedLock('k-throw', () => {
    throw new Error('boom-sync');
  });
  const second = keyedLock('k-throw', async () => {
    throw new Error('boom-async');
  });
  const third = keyedLock('k-throw', async () => 'ran');
  await assert.rejects(first, { message: 'boom-sync' });
  await assert.rejects(second, { message: 'boom-async' });
  assert.equal(await third, 'ran');
});

test('semaphore(2): 5 run() calls never have more than 2 active; all 5 finish in start order', async () => {
  const sem = semaphore(2);
  let active = 0;
  let maxActive = 0;
  const started = /** @type {number[]} */ ([]);
  const gates = Array.from({ length: 5 }, deferred);
  const runs = gates.map((g, n) =>
    sem.run(async () => {
      active += 1;
      maxActive = Math.max(maxActive, active);
      started.push(n);
      await g.promise;
      active -= 1;
      return n;
    }),
  );
  await settle();
  assert.deepEqual([started, sem.active, sem.pending], [[0, 1], 2, 3]);
  gates[1].resolve();
  await settle();
  assert.deepEqual([started, sem.active, sem.pending], [[0, 1, 2], 2, 2]);
  for (const g of gates) g.resolve();
  assert.deepEqual(await Promise.all(runs), [0, 1, 2, 3, 4]);
  assert.deepEqual([started, maxActive, sem.active, sem.pending], [[0, 1, 2, 3, 4], 2, 0, 0]);
});

test('semaphore(1): a throw inside run() releases the slot — the waiter runs, active ends at 0', async () => {
  const sem = semaphore(1);
  const failing = sem.run(async () => {
    throw new Error('slot-throw');
  });
  const next = sem.run(async () => sem.active);
  await assert.rejects(failing, { message: 'slot-throw' });
  assert.equal(await next, 1);
  assert.deepEqual([sem.active, sem.pending], [0, 0]);
});

test('semaphore(1): release is idempotent — calling it twice frees exactly one slot', async () => {
  const sem = semaphore(1);
  const release = await sem.acquire();
  let secondGot = false;
  const second = sem.acquire().then((r) => {
    secondGot = true;
    return r;
  });
  const third = sem.acquire();
  release();
  release(); // a no-op: it must not also let `third` in
  const releaseSecond = await second;
  await settle();
  assert.deepEqual([secondGot, sem.active, sem.pending], [true, 1, 1]);
  releaseSecond();
  const releaseThird = await third;
  assert.deepEqual([sem.active, sem.pending], [1, 0]);
  releaseThird();
  releaseThird();
  assert.deepEqual([sem.active, sem.pending], [0, 0]);
});

test('semaphore: n must be an integer >= 1 (0, 1.5 and "2" throw TypeError)', () => {
  for (const bad of [0, 1.5, '2']) {
    assert.throws(() => semaphore(/** @type {any} */ (bad)), { name: 'TypeError', message: 'semaphore: n must be an integer >= 1' });
  }
});
