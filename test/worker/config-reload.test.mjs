// helpers FIRST: its import-time guard moves $HOME under a per-file temp parent.
import { BLOCK, freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { startRun, readRun, reloadRun, saveRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, pendingTickets, readResult } = await import('../../src/worker/queue.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { configHash, snapshotFor } = await import('../../src/state/config-snapshot.mjs');

const OLD = { version: 1, provider: 'anthropic', review: { session_timeout_s: 60 } };
const NEW = { version: 1, provider: 'anthropic', review: { session_timeout_s: 90 } };
const BOOT = { version: 1, provider: 'anthropic', review: { session_timeout_s: 30 } };

/**
 * A run started with config OLD, block B11 open, and one in-process worker (booted on BOOT, so a
 * ticket reviewed on the boot config is visible) whose engine hook records, per file, the cfg it
 * was handed and the session timeout its `ctx.spawn` reached the spawner with.
 */
async function setup() {
  const { repo, runId } = await makeRepo({ start: false });
  const writeRow = async () => {};
  await startRun({ workspace: repo, project: 'worker-test', config: OLD, runId, writeRow });
  await openBlock({ runId, id: BLOCK, level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  /** @type {Record<string, {cfg: Record<string, any>, timeoutMs: number}>} */
  const seen = {};
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: BOOT, runRootDir: freshDir('runroot'), slug: 'worker-test', key: await loadKey(runId) },
    {
      store: await createKeyStore({ backends: [], dir: freshDir('store') }),
      env: {},
      writeRow: async () => {},
      spawn: /** @type {any} */ (async (/** @type {Record<string, any>} */ opts) => ({ status: 'ok', timeoutMs: opts.timeoutMs })),
      review: async (ticket, ctx) => {
        const session = await ctx.spawn({});
        seen[ticket.file] = { cfg: ctx.cfg, timeoutMs: session.timeoutMs };
        return { status: 'reviewed', approved: false, engine: 'test', sessions: [] };
      },
    },
  );
  return { repo, runId, worker, seen, writeRow };
}

test('a reload reaches the running worker: a ticket queued before it keeps the old snapshot, one queued after uses the new one', async () => {
  const { repo, runId, worker, seen, writeRow } = await setup();
  const before = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' });
  await reloadRun({ runId, config: NEW, readPending: () => pendingTickets(repo), writeRow });
  const after = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/b.mjs' });
  const record = await readRun(runId);
  assert.equal(snapshotFor(record, before.ticket)?.hash, configHash(OLD));
  assert.equal(snapshotFor(record, after.ticket)?.hash, configHash(NEW)); // not pinned: the new snapshot
  assert.deepEqual(Object.keys(record.config.pins), [before.ticket]);

  assert.equal(await worker.drain(), 2);
  assert.deepEqual(Object.keys(seen).sort(), ['src/a.mjs', 'src/b.mjs']);
  assert.deepEqual(seen['src/a.mjs'], { cfg: OLD, timeoutMs: 60000 });
  assert.deepEqual(seen['src/b.mjs'], { cfg: NEW, timeoutMs: 90000 });
  assert.equal(readResult(repo, runId, before.ticket)?.status, 'reviewed');
  assert.equal(readResult(repo, runId, after.ticket)?.status, 'reviewed');
});

test('a snapshot altered in the run record is never used: the ticket is `unavailable`, reason config-snapshot', async () => {
  const { repo, runId, worker, seen } = await setup();
  const record = await readRun(runId);
  record.config.snapshots[record.config.hash].review.session_timeout_s = 1;
  await saveRun(record);
  const t = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/c.mjs' });

  assert.equal(await worker.drain(), 1);
  assert.deepEqual(seen, {});
  const result = readResult(repo, runId, t.ticket);
  assert.equal(result?.status, 'unavailable');
  assert.equal(result?.reason, 'config-snapshot');
});
