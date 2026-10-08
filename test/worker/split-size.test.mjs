// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { BLOCK, freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

const { createWorker, packetSize } = await import('../../src/worker/loop.mjs');
const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { classify, renderText } = await import('../../src/cli/review.mjs');

/**
 * An in-process worker on a started run whose engine hook answers `outcome` for every ticket.
 * @param {Record<string, any>} outcome
 */
async function workerWith(outcome) {
  const { repo, runId } = await makeRepo();
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: {}, runRootDir: freshDir('runroot'), slug: 'split-size-test', key: await loadKey(runId), pollMs: 20 },
    { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow: async (row) => void rows.push(row), readRows: async () => rows, review: async () => /** @type {any} */ (outcome) },
  );
  const ticket = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' }).ticket;
  assert.equal(await worker.drain(), 1);
  return { result: readResult(repo, runId, ticket), row: rows.find((r) => r.event === 'review.result') };
}

describe('split_required carries its packet size (B54)', () => {
  test('packetSize: only for a split_required outcome, always tokens_in and budget (null unless finite); a named section rides along', () => {
    const split = { status: 'split_required' };
    assert.deepEqual(
      [
        packetSize({ ...split, tokens_in: 41000, budget: 32000 }),
        packetSize({ ...split, tokens_in: 41000, budget: 32000, section: '## Plan' }),
        packetSize({ status: 'stopped', stopped: 'split_required', tokens_in: 41000, budget: 32000 }),
        packetSize({ ...split, tokens_in: 41000 }),
        packetSize({ ...split, tokens_in: Number.NaN, budget: Number.POSITIVE_INFINITY }),
        packetSize({ status: 'stopped', stopped: 'review_cap', tokens_in: 41000, budget: 32000 }),
        packetSize({ status: 'reviewed', tokens_in: 41000, budget: 32000 }),
      ],
      [{ tokens_in: 41000, budget: 32000 }, { tokens_in: 41000, budget: 32000, section: '## Plan' }, { tokens_in: 41000, budget: 32000 }, { tokens_in: 41000, budget: null }, { tokens_in: null, budget: null }, {}, {}],
    );
  });

  test('round 1 split_required: the signed result and the review.result ledger row carry tokens_in and budget', async () => {
    const { result, row } = await workerWith({ status: 'split_required', approved: false, engine: 'adaptive', tokens_in: 41000, budget: 32000, section: '## Plan', sessions: [] });
    assert.deepEqual([result?.status, result?.approved, result?.tokens_in, result?.budget, result?.section], ['split_required', false, 41000, 32000, '## Plan']);
    assert.deepEqual([row?.status, row?.tokens_in, row?.budget, row?.section], ['split_required', 41000, 32000, '## Plan']);
  });

  test('a recheck stopped at split_required: the result and the ledger row carry them too', async () => {
    const { result, row } = await workerWith({ status: 'stopped', stopped: 'split_required', approved: false, engine: 'adaptive', tokens_in: 40001, budget: 32000, sessions: [], findings: [] });
    assert.deepEqual([result?.status, result?.stopped, result?.tokens_in, result?.budget], ['stopped', 'split_required', 40001, 32000]);
    assert.deepEqual([row?.stopped, row?.tokens_in, row?.budget], ['split_required', 40001, 32000]);
  });

  test('any other result carries none of them, even when the engine outcome names numbers', async () => {
    const { result, row } = await workerWith({ status: 'stopped', stopped: 'review_cap', approved: false, engine: 'adaptive', tokens_in: 9, budget: 8, section: '## X', sessions: [], findings: [] });
    const keys = ['tokens_in', 'budget', 'section'];
    assert.deepEqual([keys.filter((k) => Object.hasOwn(result ?? {}, k)), keys.filter((k) => Object.hasOwn(row ?? {}, k))], [[], []]);
  });

  test('`code-forge review`: a split_required file is `stopped: split_required` with its size in the text and the JSON', () => {
    const split = classify('docs/brief.md', { status: 'done', result: { status: 'split_required', approved: false, tokens_in: 41000, budget: 32000, section: '## Plan' } });
    const stopped = classify('src/big.mjs', { status: 'done', result: { status: 'stopped', stopped: 'split_required', approved: false, tokens_in: 40001, budget: 32000 } });
    assert.deepEqual(split, { file: 'docs/brief.md', result: 'stopped', reason: 'split_required', findings: [], tokens_in: 41000, budget: 32000, section: '## Plan' });
    assert.deepEqual(stopped, { file: 'src/big.mjs', result: 'stopped', reason: 'split_required', findings: [], tokens_in: 40001, budget: 32000 });
    assert.equal(
      renderText([split, stopped]),
      'docs/brief.md  stopped: split_required (~41000 tokens, budget 32000; section "## Plan")\nsrc/big.mjs    stopped: split_required (~40001 tokens, budget 32000)\n\ntotals: 2 files · 0 approved · 0 with findings (0 findings) · 2 stopped · 0 unavailable\n',
    );
  });

  test('a split_required result with no size still names tokens_in: null and budget: null (result file and ledger row)', async () => {
    const { result, row } = await workerWith({ status: 'split_required', approved: false, engine: 'adaptive', sessions: [] });
    assert.deepEqual([result?.tokens_in, result?.budget, row?.tokens_in, row?.budget], [null, null, null, null]);
  });

  test('`code-forge review`: null or non-finite sizes keep the old label (never NaN); another stop never shows a size', () => {
    const inf = classify('docs/b.md', { status: 'done', result: { status: 'split_required', approved: false, tokens_in: Number.POSITIVE_INFINITY, budget: 32000 } });
    assert.deepEqual(inf, { file: 'docs/b.md', result: 'stopped', reason: 'split_required', findings: [], tokens_in: null, budget: 32000 });
    assert.equal(renderText([inf]), 'docs/b.md  stopped: split_required\n\ntotals: 1 file · 0 approved · 0 with findings (0 findings) · 1 stopped · 0 unavailable\n');
  });

  test('`code-forge review`: null sizes keep the old label; another stop never shows a size', () => {
    const nulls = classify('docs/a.md', { status: 'done', result: { status: 'split_required', approved: false, tokens_in: null, budget: null } });
    const cap = classify('src/c.mjs', { status: 'done', result: { status: 'stopped', stopped: 'review_cap', approved: false, tokens_in: 9, budget: 8 } });
    assert.deepEqual([nulls, cap], [
      { file: 'docs/a.md', result: 'stopped', reason: 'split_required', findings: [], tokens_in: null, budget: null },
      { file: 'src/c.mjs', result: 'stopped', reason: 'review_cap', findings: [] },
    ]);
    assert.equal(renderText([nulls, cap]), 'docs/a.md  stopped: split_required\nsrc/c.mjs  stopped: review_cap\n\ntotals: 2 files · 0 approved · 0 with findings (0 findings) · 2 stopped · 0 unavailable\n');
  });
});
