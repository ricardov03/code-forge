import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { beatWorker, enqueue, heartbeatFresh, liveWorker, queueDir, readWorker, retractWorker, HEARTBEAT_STALE_MS, WORKER_FILE } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

/** @param {number} ms */
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/**
 * Write the queue's worker announcement by hand.
 * @param {string} repo @param {Record<string, any>} body
 */
function announce(repo, body) {
  mkdirSync(queueDir(repo), { recursive: true });
  writeFileSync(path.join(queueDir(repo), WORKER_FILE), `${JSON.stringify(body)}\n`);
}

/**
 * `review-file --wait <ticket>` in-process; returns the exit code and the JSON answer.
 * @param {string} repo @param {string} ticket @param {string} max
 */
async function wait(repo, ticket, max) {
  let out = '';
  const code = await runReviewFile(['--wait', ticket, '--max', max], { cwd: repo, pollMs: 20, stdout: { write: (/** @type {string} */ s) => (out += s) }, stderr: { write: () => true } });
  return { code, json: JSON.parse(out.trim().split('\n').at(-1) ?? 'null') };
}

describe('the worker heartbeat (B30)', () => {
  test('heartbeat_at advances while a long session is awaited, and the worker stays live throughout', async () => {
    const { repo, runId } = await makeRepo();
    /** @type {string[]} */
    const beats = [];
    /** @type {boolean[]} */
    const live = [];
    const worker = await createWorker(
      { runId, repoRoot: repo, cfg: {}, runRootDir: freshDir('runroot'), slug: 'worker-test', key: await loadKey(runId), heartbeatMs: 100 },
      {
        store: await createKeyStore({ backends: [], dir: freshDir('store') }),
        env: {},
        writeRow: async () => {},
        // the long session: ~900 ms in which the event loop is free, as in a real `exec` await
        spawn: /** @type {any} */ (async () => {
          for (let i = 0; i < 6; i += 1) {
            await sleep(150);
            beats.push(String(readWorker(repo)?.heartbeat_at));
            live.push(liveWorker(repo)?.pid === process.pid);
          }
          return { status: 'ok' };
        }),
        review: async (_ticket, ctx) => {
          await ctx.spawn({ role: 'reviewer' });
          return { status: 'reviewed', approved: true, engine: 'test', sessions: [] };
        },
      },
    );
    enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    assert.equal(await worker.once(), 1);
    assert.equal(beats.length, 6);
    assert.deepEqual(live, [true, true, true, true, true, true]);
    const times = beats.map((b) => Date.parse(b));
    assert.equal(times.every((t) => Number.isFinite(t)), true);
    for (let i = 1; i < times.length; i += 1) assert.equal(times[i] >= times[i - 1], true, `beat ${i} went backwards: ${beats.join(', ')}`);
    assert.equal(new Set(beats).size >= 3, true, `fewer than 3 distinct beats: ${beats.join(', ')}`);
    assert.equal(readWorker(repo), null); // once() released the announcement
    await sleep(350); // three more beat intervals: the stopped timer writes nothing
    assert.equal(readWorker(repo), null);
  });
});

describe('review-file --wait and worker liveness (B30)', () => {
  test('a live pid with a fresh heartbeat is never worker_down, even when its start time reads differently', async () => {
    const { repo, runId } = await makeRepo();
    const { ticket } = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    // a sandboxed `ps` (another time zone, or no `ps` at all) reads a start time that is not the announced one
    announce(repo, { pid: process.pid, start_time: 'Thu Jan  1 00:00:00 1970', run: runId, started_at: new Date().toISOString(), heartbeat_at: new Date().toISOString() });
    try {
      assert.equal(liveWorker(repo)?.pid, process.pid);
      const res = await wait(repo, ticket, '300ms');
      assert.deepEqual([res.code, res.json.status, res.json.reason, res.json.ticket], [0, 'pending', 'wait_timeout', ticket]);
    } finally {
      retractWorker(repo, process.pid);
    }
  });

  test('the same start-time mismatch with a stale heartbeat is worker_down (exit 3)', async () => {
    const { repo, runId } = await makeRepo();
    const { ticket } = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    const stale = new Date(Date.now() - HEARTBEAT_STALE_MS - 5000).toISOString();
    announce(repo, { pid: process.pid, start_time: 'Thu Jan  1 00:00:00 1970', run: runId, started_at: stale, heartbeat_at: stale });
    try {
      assert.equal(liveWorker(repo), null);
      const res = await wait(repo, ticket, '5s');
      assert.deepEqual([res.code, res.json.status], [3, 'worker_down']);
    } finally {
      retractWorker(repo, process.pid);
    }
  });

  test('a dead pid is worker_down even with a fresh heartbeat', async () => {
    const { repo, runId } = await makeRepo();
    const { ticket } = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    announce(repo, { pid: 2 ** 22 + 12345, start_time: 'unknown', run: runId, started_at: new Date().toISOString(), heartbeat_at: new Date().toISOString() });
    assert.equal(liveWorker(repo), null);
    const res = await wait(repo, ticket, '5s');
    assert.deepEqual([res.code, res.json.status], [3, 'worker_down']);
  });

  test('a heartbeat more than 5 s in the future is not fresh (the start-time check decides)', async () => {
    const { repo, runId } = await makeRepo();
    const ahead = new Date(Date.now() + 60_000).toISOString();
    announce(repo, { pid: process.pid, start_time: 'Thu Jan  1 00:00:00 1970', run: runId, started_at: ahead, heartbeat_at: ahead });
    try {
      assert.equal(heartbeatFresh({ heartbeat_at: ahead }), false);
      assert.equal(heartbeatFresh({ heartbeat_at: new Date(Date.now() + 2000).toISOString() }), true);
      assert.equal(liveWorker(repo), null);
    } finally {
      retractWorker(repo, process.pid);
    }
  });

  test('beatWorker never writes over the announcement of another worker (pid or start time differ)', async () => {
    const { repo, runId } = await makeRepo();
    const other = { pid: process.pid, start_time: 'Thu Jan  1 00:00:00 1970', run: runId, started_at: 'x', heartbeat_at: '2020-01-01T00:00:00.000Z' };
    announce(repo, other);
    try {
      assert.equal(beatWorker(repo, { pid: process.pid, start_time: 'Fri Jan  2 00:00:00 1970' }), false);
      assert.equal(beatWorker(repo, { pid: process.pid + 1, start_time: other.start_time }), false);
      assert.equal(readWorker(repo)?.heartbeat_at, '2020-01-01T00:00:00.000Z');
      assert.equal(beatWorker(repo, { pid: process.pid, start_time: other.start_time }), true);
      assert.notEqual(readWorker(repo)?.heartbeat_at, '2020-01-01T00:00:00.000Z');
    } finally {
      retractWorker(repo, process.pid);
    }
  });
});
