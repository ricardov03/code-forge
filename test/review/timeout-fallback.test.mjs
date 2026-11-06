import { answerFor, freshDir, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

const FILE = 'src/small.mjs';
const PRIMARY = 'claude-opus-5-5';
const SLOW = 'claude-fable-5-1';
const FAST = 'claude-sonnet-5';

// Every git/provider child inherits a clean environment, including when run outside the suite.
for (const name of Object.keys(process.env)) if (name.startsWith('CODE_FORGE_')) delete process.env[name];

/** @param {string} slug @param {boolean} alternatives @param {boolean} [fallbackTimeout] */
async function review(slug, alternatives, fallbackTimeout = false) {
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const small = 1;\n');
  const runId = `r-${slug}`;
  const cfg = {
    provider: 'anthropic', project: { slug },
    levels: { L2: { model: PRIMARY }, ...(alternatives ? { L3: { model: SLOW, fallback: [{ model: FAST }] } } : {}) },
    review: { min_tokens_out: 40, adaptive: { explore: 0 } },
  };
  await startRun({ workspace: repo, project: slug, runId, config: cfg, writeRow: async () => {} });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'review', tests: ['fake'] }], writeRow: async () => {} });
  const key = await loadKey(runId);
  const writeRow = (row) => appendRow(row, { slug });
  if (alternatives) {
    for (const [model, duration_ms] of [[SLOW, 100000], [FAST, 1000]]) {
      for (let i = 0; i < 10; i++) await writeRow(signRow({ event: 'session', run: runId, role: 'reviewer', model, status: 'ok', duration_ms, ts: new Date().toISOString() }, key));
    }
  }
  const calls = [];
  const packets = [];
  const worker = await createWorker({ runId, repoRoot: repo, cfg, runRootDir: freshDir('worker'), slug, key }, {
    env: {}, store: await createKeyStore({ backends: [], dir: freshDir('keys') }), expiryCheck: async () => {},
    writeRow,
    spawn: async (opts) => {
      const { model, provider = cfg.provider } = opts.cfg.levels[opts.level];
      calls.push(model);
      const packet = readFileSync(opts.promptPath, 'utf8');
      packets.push(packet);
      if (model === PRIMARY || fallbackTimeout) return { status: 'timeout', model, provider, duration_ms: 60000, usage: { tokens_out: 0 } };
      const hunks = packet.split('\n').filter((line) => line.startsWith('- @@')).map((line) => line.slice(2));
      return { status: 'ok', model, provider, exit_code: 0, answer: answerFor(hunks), usage: { tokens_in: 100, tokens_out: 100 } };
    },
  });
  const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE });
  assert.equal(await worker.drain(), 1, 'exactly one file ticket processed');
  return { result: readResult(repo, runId, ticket.ticket), calls, packets, rows: await readAllRows(slug), key };
}

test('second timeout selects one different configured reviewer by measured performance and signs its fallback row', async () => {
  for (const fallbackTimeout of [false, true]) {
    const { result, calls, packets, rows, key } = await review(`timeout-alternative-${Number(fallbackTimeout)}`, true, fallbackTimeout);
    assert.deepEqual(calls, [PRIMARY, PRIMARY, FAST], 'two primary timeouts then exactly one measured fallback attempt');
    assert.equal(new Set(packets).size, 1, 'all three sessions review the same file packet');
    assert.equal(result.sessions[0].attempts, 3);
    assert.deepEqual([result.status, result.approved], fallbackTimeout ? ['unavailable', false] : ['reviewed', true]);
    const fallback = rows.filter((row) => row.event === 'review.schema_fallback' && row.trigger === 'timeout');
    assert.equal(fallback.length, 1, 'exactly one signed timeout fallback row');
    assert.deepEqual([fallback[0].file, fallback[0].level, fallback[0].from_model, fallback[0].to_model, fallback[0].reason, fallback[0].routing_reason],
      [FILE, 'L2', PRIMARY, FAST, 'repeated-timeout', 'performance']);
    assert.deepEqual(verifyRow(fallback[0], key), { ok: true });
  }
});

test('without another configured reviewer two timeouts remain unavailable with retry', async () => {
  const { result, calls, rows } = await review('timeout-no-alternative', false);
  assert.deepEqual(calls, [PRIMARY, PRIMARY], 'exactly two original-model attempts');
  assert.deepEqual([result.status, result.reason, result.approved, result.next.action], ['unavailable', 'timeout', false, 'retry']);
  assert.equal(rows.filter((row) => row.event === 'review.schema_fallback').length, 0, 'no fallback row without another model');
});
