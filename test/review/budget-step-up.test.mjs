// Import first: HOME is temporary before any src module loads.
import { answerFor, cfgFor, commitAll, freshDir, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const { reviewFile, rulesRisk } = await import('../../src/review/engine.mjs');
const { assemblePacket, readFileDiff } = await import('../../src/review/packet.mjs');
const FILE = 'src/sample.mjs';

/** @param {number} width */
async function reproduction(width = 52) {
  const repo = await makeRepo();
  const line = (/** @type {string} */ label, /** @type {number} */ i) => `${label}_${String(i).padStart(3, '0')}`.padEnd(width, 'x');
  const common = Array.from({ length: 116 }, (_, i) => line('keep', i));
  const before = [...common, ...Array.from({ length: 462 }, (_, i) => line('old', i))].join('\n') + '\n';
  const after = [...common, ...Array.from({ length: 57 }, (_, i) => line('new', i))].join('\n') + '\n';
  writeFile(repo, FILE, before);
  await commitAll(repo);
  writeFile(repo, FILE, after);
  const diff = await readFileDiff({ repoRoot: repo, file: FILE, base: null });
  assert.deepEqual([before.trimEnd().split('\n').length, after.trimEnd().split('\n').length, diff.plusCount, diff.minusCount], [578, 173, 57, 462]);
  assert.equal(rulesRisk({ file: FILE, plusCount: diff.plusCount, cfg: {} }), 0);
  return { repo, diff };
}

function fakeSession() {
  /** @type {Array<Record<string, any>>} */
  const calls = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    calls.push({ ...opts, text });
    const headers = text.split('\n').filter((line) => line.startsWith('- @@')).map((line) => line.slice(2));
    return { status: 'ok', exit_code: 0, answer: answerFor(headers), usage: { tokens_in: opts.rowExtra.ctx_tokens_in, tokens_out: 300 } };
  };
  return { calls, spawn };
}

/** @param {string} repo @param {Record<string, any>} [cfg] @param {Record<string, any>} [extra] */
async function engineRun(repo, cfg = cfgFor(), extra = {}) {
  const fake = fakeSession();
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const outcome = await reviewFile({ repoRoot: repo, file: FILE, base: null, cfg, workDir: freshDir('packets'), ...extra }, { spawn: fake.spawn, writeRow: async (row) => void rows.push(row) });
  return { ...fake, rows, outcome };
}

test('B109 (1)(2): deletion-heavy risk-0 file steps from quick to one full L2 session', async () => {
  const { repo, diff } = await reproduction();
  const quick = assemblePacket({ diff, lens: 'quick' });
  assert.equal(quick.status, 'split_required');
  assert.equal(quick.budget, 6000);
  assert.equal(quick.tokensIn > 6000 && quick.tokensIn < 32000, true);
  const { outcome, calls, rows } = await engineRun(repo);
  assert.equal(outcome.status, 'reviewed', JSON.stringify({ status: outcome.status, budget: outcome.budget, sessions: outcome.sessions.length, spawns: calls.length }));
  assert.deepEqual([outcome.approved, outcome.risk, outcome.depth, calls.length, outcome.sessions.length], [true, 0, 'full', 1, 1]);
  assert.deepEqual(outcome.sessions.map((s) => [s.lens, s.level, s.role, s.status]), [['full', 'L2', 'reviewer', 'ok']]);
  assert.deepEqual(calls.map((s) => [s.rowExtra.lens, s.level]), [['full', 'L2']]);
  const full = assemblePacket({ diff, lens: 'full' });
  assert.equal(full.status, 'ok');
  assert.equal(calls[0].text, 'text' in full ? full.text : '');
  assert.equal(rows.length, 1);
  assert.deepEqual(rows[0], { event: 'review.plan', depth_unconstrained: 'full', depth_chosen: 'full', degrade_step: 0, risk: 0, context_mode: 'whole', forecast_tokens: full.tokensIn, step_up: { from: 'quick', tokens_in: quick.tokensIn, budget: 6000 } });
});

test('B109 (3): over full_in stops at the full size and budget without a session or row', async () => {
  const { repo, diff } = await reproduction(260);
  const full = assemblePacket({ diff, lens: 'full' });
  assert.equal(full.status, 'split_required');
  assert.equal(full.tokensIn > 32000, true);
  const { outcome, calls, rows } = await engineRun(repo);
  assert.deepEqual([outcome.status, outcome.approved, outcome.depth, outcome.risk, outcome.tokens_in, outcome.budget, calls.length, outcome.sessions.length, rows.length], ['split_required', false, 'full', 0, full.tokensIn, 32000, 0, 0, 0]);
  const custom = await engineRun(repo, cfgFor({ budgets: { full_in: 31000 } }));
  assert.deepEqual([custom.outcome.tokens_in, custom.outcome.budget, custom.calls.length], [full.tokensIn, 31000, 0]);
});

test('B109 (4): a fitting quick file keeps its packet and exact plan row without step_up', async () => {
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const value = 1;\n');
  const diff = await readFileDiff({ repoRoot: repo, file: FILE, base: null });
  const packet = assemblePacket({ diff, lens: 'quick' });
  assert.equal(packet.status, 'ok');
  const { outcome, calls, rows } = await engineRun(repo);
  assert.deepEqual([outcome.status, outcome.depth, outcome.risk, calls.length, outcome.sessions.length], ['reviewed', 'quick', 0, 1, 1]);
  assert.equal(calls[0].text, 'text' in packet ? packet.text : '');
  assert.deepEqual(outcome.sessions.map((s) => [s.lens, s.level]), [['quick', 'L2']]);
  assert.deepEqual(rows, [{ event: 'review.plan', depth_unconstrained: 'quick', depth_chosen: 'quick', degrade_step: 0, risk: 0, context_mode: 'whole', forecast_tokens: packet.tokensIn }]);
});

test('B109 (5): Markdown still splits into exactly three quick section sessions without step_up', async () => {
  const repo = await makeRepo();
  const file = 'docs/sample.md';
  const content = ['One', 'Two', 'Three'].map((heading) => `## ${heading}\n` + Array.from({ length: 220 }, (_, i) => `${heading} ${i} `.padEnd(52, 'x')).join('\n') + '\n').join('');
  writeFile(repo, file, content);
  const diff = await readFileDiff({ repoRoot: repo, file, base: null });
  assert.equal(assemblePacket({ diff, lens: 'quick' }).status, 'split_required');
  const { outcome, calls, rows } = await engineRun(repo, cfgFor(), { file, risk: 0 });
  assert.deepEqual([outcome.status, outcome.approved, outcome.depth, calls.length, rows.length], ['reviewed', true, 'quick', 3, 3]);
  assert.deepEqual(outcome.sections.map((s) => s.headings), [['## One'], ['## Two'], ['## Three']]);
  assert.deepEqual(outcome.sessions.map((s) => [s.lens, s.level, s.section]), [['quick', 'L2', 1], ['quick', 'L2', 2], ['quick', 'L2', 3]]);
  assert.deepEqual(rows.map((r) => [r.depth_chosen, r.section, r.sections, Object.hasOwn(r, 'step_up')]), [['quick', 1, 3, false], ['quick', 2, 3, false], ['quick', 3, 3, false]]);
  const packetDiffs = calls.map((c) => c.text.slice(c.text.indexOf('\n## diff\n')));
  assert.deepEqual(packetDiffs.map((text) => text.split('\n').filter((line) => line.startsWith('+') && !line.startsWith('+++')).length), [221, 221, 221]);
});

test('B109 (6): risk 1 keeps one full L2 session and the same row with no step_up', async () => {
  const { repo, diff } = await reproduction();
  const packet = assemblePacket({ diff, lens: 'full' });
  const { outcome, calls, rows } = await engineRun(repo, cfgFor(), { risk: 1 });
  assert.deepEqual([outcome.status, outcome.approved, outcome.depth, outcome.risk, calls.length, outcome.sessions.length], ['reviewed', true, 'full', 1, 1, 1]);
  assert.equal(calls[0].text, 'text' in packet ? packet.text : '');
  assert.deepEqual(outcome.sessions.map((s) => [s.lens, s.level]), [['full', 'L2']]);
  assert.deepEqual(rows, [{ event: 'review.plan', depth_unconstrained: 'full', depth_chosen: 'full', degrade_step: 0, risk: 1, context_mode: 'whole', forecast_tokens: packet.tokensIn }]);
});

test('B109: a non-full second plan fails closed without spawning consensus', async () => {
  const { repo, diff } = await reproduction();
  const quick = assemblePacket({ diff, lens: 'quick' });
  const { outcome, calls, rows } = await engineRun(repo, cfgFor({ multimodel: true, single_reviewer_max_risk: 0.5 }));
  assert.deepEqual([outcome.status, outcome.approved, outcome.depth, outcome.tokens_in, outcome.budget, calls.length, rows.length], ['split_required', false, 'quick', quick.tokensIn, 6000, 0, 0]);
});

test('B109 (7): the worker signs a reviewed ticket for the reproduction file', async () => {
  const { startRun } = await import('../../src/state/run.mjs');
  const { openBlock } = await import('../../src/state/block.mjs');
  const { loadKey } = await import('../../src/state/signer.mjs');
  const { createWorker } = await import('../../src/worker/loop.mjs');
  const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
  const { createKeyStore } = await import('../../src/keys/store.mjs');
  const { repo } = await reproduction();
  const runId = 'r-budget-step-up';
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const writeRow = async (/** @type {Record<string, any>} */ row) => void rows.push(row);
  await startRun({ workspace: repo, project: 'budget-step-up-test', runId, writeRow });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'review changed file', tests: ['budget-step-up'] }], writeRow });
  const fake = fakeSession();
  const worker = await createWorker({ runId, repoRoot: repo, cfg: cfgFor(), runRootDir: freshDir('worker'), slug: 'budget-step-up-test', key: await loadKey(runId), pollMs: 20 }, { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, spawn: fake.spawn, writeRow, readRows: async () => rows, jev: async () => ({ ok: false, reason: 'offline' }) });
  const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE }).ticket;
  assert.equal(await worker.drain(), 1);
  const result = readResult(repo, runId, ticket);
  assert.deepEqual([result?.status, result?.approved, fake.calls.length], ['reviewed', true, 1]);
  assert.deepEqual(result?.sessions.map((s) => [s.lens, s.level, s.status]), [['full', 'L2', 'ok']]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.result').map((r) => [r.status, r.approved]), [['reviewed', true]]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.plan').map((r) => [r.depth_chosen, r.step_up?.from, r.step_up?.budget]), [['full', 'quick', 6000]]);
  assert.equal(rows.filter((r) => r.event === 'review.approved').length, 1);
});
