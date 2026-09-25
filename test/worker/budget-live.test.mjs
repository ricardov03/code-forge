// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { openBlock } = await import('../../src/state/block.mjs');

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
}

/** An in-process worker on a stub spawner whose every session is a clean review. */
async function cleanWorker() {
  const { repo, runId } = await makeRepo();
  rmSync(path.join(repo, 'src', 'c.mjs')); // the block (owned `src/**`) changes exactly 2 files
  const key = await loadKey(runId);
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    const answer = { passed: true, summary: 's', reviewed_hunks: hunksOf(text), findings: [], resolved: [], needs_file: [] };
    return { status: 'ok', exit_code: 0, answer, usage: { tokens_in: 900, tokens_out: 300 } };
  };
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: {}, runRootDir: freshDir('runroot'), slug: 'worker-test', key },
    {
      store: await createKeyStore({ backends: [], dir: freshDir('store') }),
      env: {},
      writeRow: async (row) => void rows.push(row),
      readRows: async () => rows,
      spawn: /** @type {any} */ (spawn),
      jev: async () => ({ ok: false }),
    },
  );
  return { repo, runId, key, rows, worker };
}

test('B19: through the worker path, one block with 2 files gives exactly 1 signed review.budget row (and the engine keeps its 2 review.plan rows)', async () => {
  const w = await cleanWorker();
  for (const file of ['src/a.mjs', 'src/b.mjs']) {
    const t = enqueue({ repoRoot: w.repo, run: w.runId, block: 'B11', file });
    assert.equal(await w.worker.drain(), 1);
    assert.equal(/** @type {any} */ (readResult(w.repo, w.runId, t.ticket)).approved, true);
  }
  const budgets = w.rows.filter((r) => r.event === 'review.budget');
  assert.equal(budgets.length, 1);
  const [b] = budgets;
  // 2 light-tier files (no path floor) × quick 7 000 = 14 000, under the 150 000 default budget
  assert.deepEqual(
    [b.block, b.budget, b.forecast_unconstrained, b.forecast_chosen, b.over_budget, b.degrade_step, b.raised, b.actual],
    ['B11', 150000, 14000, 14000, false, 0, 0, null],
  );
  assert.equal(verifyRow(b, w.key).ok, true);
  assert.deepEqual(w.rows.filter((r) => r.event === 'review.plan').map((r) => r.file), ['src/a.mjs', 'src/b.mjs']);
  assert.equal(w.rows.filter((r) => r.event === 'review.over_budget').length, 0);
});

test('B19: a block whose review.budget row already exists in the run gets no second one', async () => {
  const w = await cleanWorker();
  w.rows.push({ event: 'review.budget', run: w.runId, block: 'B11', budget: 150000 });
  const t = enqueue({ repoRoot: w.repo, run: w.runId, block: 'B11', file: 'src/a.mjs' });
  assert.equal(await w.worker.drain(), 1);
  assert.equal(/** @type {any} */ (readResult(w.repo, w.runId, t.ticket)).approved, true);
  assert.equal(w.rows.filter((r) => r.event === 'review.budget').length, 1);
});

test('B19 R1: a seeded review.budget for B11 does not stop a second block (B12) from getting its own: exactly 1 new row, block B12, 2 in total', async () => {
  const w = await cleanWorker();
  mkdirSync(path.join(w.repo, 'lib'));
  writeFileSync(path.join(w.repo, 'lib', 'x.mjs'), 'export const x = 1;\n');
  await openBlock({ runId: w.runId, id: 'B12', level: 'L2', owned: ['lib/**'], acceptance: [{ clause: 'lib is reviewed', tests: ['worker'] }], writeRow: async () => {} });
  w.rows.push({ event: 'review.budget', run: w.runId, block: 'B11', budget: 150000 });
  const t = enqueue({ repoRoot: w.repo, run: w.runId, block: 'B12', file: 'lib/x.mjs' });
  assert.equal(await w.worker.drain(), 1);
  assert.equal(/** @type {any} */ (readResult(w.repo, w.runId, t.ticket)).approved, true);
  const budgets = w.rows.filter((r) => r.event === 'review.budget');
  assert.equal(budgets.length, 2);
  assert.deepEqual(budgets.slice(1).map((r) => [r.block, r.forecast_unconstrained]), [['B12', 7000]]);
});
