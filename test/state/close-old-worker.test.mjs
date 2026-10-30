// helpers FIRST: isolate HOME and cwd before loading state modules.
import { fakeProbe, rowSink, withFixture } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { closeBlock, openBlock } from '../../src/state/block.mjs';
import { checkWorkerPin, readRun, reattachWorker, saveRun, startRun, writeSigned } from '../../src/state/run.mjs';

const ACCEPTANCE = [{ clause: 'review author was pinned to this run', tests: ['test/state/close-old-worker.test.mjs'] }];
const A = { pid: 4242, started_at: 'start-A' };
const B = { pid: 4243, started_at: 'start-B' };
const C = { pid: 4244, started_at: 'start-C' };
const D = { pid: 4245, started_at: 'start-D' };
const PROBE = fakeProbe({ [A.pid]: A.started_at, [B.pid]: B.started_at, [C.pid]: C.started_at, [D.pid]: D.started_at });

test('after reattach, close accepts reviews from every earlier pin of the same run', async () => {
  await withFixture(async ({ ws }) => {
    const { rows, writeRow } = rowSink();
    const runId = 'r-old-pins';
    await startRun({ workspace: ws, project: 'two-blocks', runId, workerPid: A.pid, probe: PROBE, writeRow });
    await openBlock({ runId, id: 'B1', level: 'L1', owned: ['a.txt'], acceptance: ACCEPTANCE, writeRow });
    await writeSigned(runId, writeRow, { event: 'review.result', block: 'B1', status: 'reviewed', worker: A });
    await reattachWorker({ runId, workerPid: B.pid, add: true, probe: PROBE, writeRow });
    await writeSigned(runId, writeRow, { event: 'review.result', block: 'B1', status: 'reviewed', worker: B });
    await reattachWorker({ runId, workerPid: C.pid, probe: PROBE, writeRow });
    await writeSigned(runId, writeRow, { event: 'review.result', block: 'B1', status: 'reviewed', worker: C });
    await reattachWorker({ runId, workerPid: D.pid, probe: PROBE, writeRow });
    const probe = fakeProbe({ [D.pid]: D.started_at });

    assert.deepEqual(await closeBlock({ runId, id: 'B1', rows, writeRow, livePid: D.pid, probe }), { ok: true, status: 'closed' });
    const record = await readRun(runId);
    assert.equal(record.blocks.B1.status, 'closed');
    assert.deepEqual(record.worker_history, [A, B, C], 'all three retired pins survive successive replacements');
    assert.deepEqual(record.workers, [D], 'only the new pin remains active');
    assert.deepEqual(rows.filter((r) => r.event === 'block.close').map((r) => r.block), ['B1']);
    assert.deepEqual(rows.filter((r) => r.event === 'worker.replaced'), []);
  });
});

test('a review naming a worker never pinned to this run still stops close with worker.replaced', async () => {
  await withFixture(async ({ ws }) => {
    const { rows, writeRow } = rowSink();
    const runId = 'r-author-check';
    await startRun({ workspace: ws, project: 'two-blocks', runId, workerPid: A.pid, probe: PROBE, writeRow });
    // C is pinned elsewhere, which gives it no authority in this run.
    await startRun({ workspace: ws, project: 'two-blocks', runId: 'r-other', workerPid: C.pid, probe: PROBE, writeRow });
    await reattachWorker({ runId, workerPid: B.pid, probe: PROBE, writeRow });
    const authors = [A, C, { ...A, started_at: 'recycled-A' }];
    const results = [];
    for (const [index, worker] of authors.entries()) {
      const id = `B${index + 1}`;
      await openBlock({ runId, id, level: 'L1', owned: [`${id}.txt`], acceptance: ACCEPTANCE, writeRow });
      await writeSigned(runId, writeRow, { event: 'review.result', block: id, status: 'reviewed', worker });
      results.push(await closeBlock({ runId, id, rows, writeRow, livePid: B.pid, probe: PROBE }));
    }
    const rejected = { ok: false, status: 'stopped', event: 'worker.replaced', reason: 'a counted review result does not name a pinned worker' };
    assert.deepEqual(results, [{ ok: true, status: 'closed' }, rejected, rejected], 'only the exact earlier pin may authorize a review');
    const record = await readRun(runId);
    assert.deepEqual(['B1', 'B2', 'B3'].map((id) => record.blocks[id].status), ['closed', 'stopped', 'stopped']);
    assert.deepEqual(rows.filter((r) => r.event === 'worker.replaced').map((r) => r.block), ['B2', 'B3']);
    assert.deepEqual(rows.filter((r) => r.event === 'block.close').map((r) => r.block), ['B1']);
  });
});

test('legacy single-pin records retain their review author on reattach and pre-B107 rows still close', async () => {
  await withFixture(async ({ ws }) => {
    const { rows, writeRow } = rowSink();
    const runId = 'r-legacy-pin';
    const legacy = await startRun({ workspace: ws, project: 'two-blocks', runId, workerPid: A.pid, probe: PROBE, writeRow });
    delete legacy.workers;
    await saveRun(legacy);
    await reattachWorker({ runId, workerPid: B.pid, probe: PROBE, writeRow });
    const results = [];
    for (const { id, worker } of [{ id: 'B1', worker: A }, { id: 'B2', worker: undefined }]) {
      await openBlock({ runId, id, level: 'L1', owned: [`${id}.txt`], acceptance: ACCEPTANCE, writeRow });
      await writeSigned(runId, writeRow, { event: 'review.result', block: id, status: 'reviewed', ...(worker ? { worker } : {}) });
      results.push(await closeBlock({ runId, id, rows, writeRow, livePid: B.pid, probe: PROBE }));
    }
    assert.deepEqual(results, [{ ok: true, status: 'closed' }, { ok: true, status: 'closed' }]);
    const record = await readRun(runId);
    assert.deepEqual(record.worker_history, [A]);
    assert.deepEqual(rows.filter((r) => r.event === 'block.close').map((r) => r.block), ['B1', 'B2']);
    assert.deepEqual(rows.filter((r) => r.event === 'worker.replaced'), []);
    assert.deepEqual(await checkWorkerPin(record, { probe: fakeProbe({ [A.pid]: A.started_at }) }), {
      ok: false, event: 'worker.down', reason: `pinned worker pid ${B.pid} is not running`,
    }, 'an earlier pin cannot stand in for a dead active worker');
  });
});
