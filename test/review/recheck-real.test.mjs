// Import first: the helper pins HOME before loading any src module.
import { freshDir, makeRepo } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { converge, fixHunkDiff, newFileState, recheckTokens, runRound } = await import('../../src/review/fixloop.mjs');
const { sectionGroups } = await import('../../src/review/sections.mjs');
const { enqueue, queueDir, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const FILE = 'src/a.mjs';
const PRIMARY = 'claude-opus-5-5';
const SECOND = 'grok-4.7';
/** @returns {Record<string, any>} */
const cfgFor = (/** @type {Record<string, any>} */ review = {}) => ({
  provider: 'anthropic', levels: { L2: { model: PRIMARY } },
  review: { min_tokens_out: 40, ...review }, escalation: { after_rounds_with_warnings: 0 },
});
/** @returns {import('../../src/review/triage.mjs').Finding} */
const finding = (id = 'F1', line = 1, file = FILE) => ({ id, file, line_start: line, line_end: line, severity: 'warning', category: 'correctness', claim: 'fake claim', evidence: 'fake evidence', fix: 'fake fix' });

/** @param {string} text */
function hunksOf(text) {
  const lines = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return lines.slice(0, lines.findIndex((line) => !line.startsWith('- @@'))).map((line) => line.slice(2));
}
/** @param {string} text @param {Record<string, any>} [extra] */
const answerFor = (text, extra = {}) => ({ passed: true, summary: 'fake review', reviewed_hunks: hunksOf(text), findings: [], resolved: [], needs_file: [], ...extra });
const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
  const [id] = Object.keys(req.questions);
  return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
};

/** @param {{file?: string, content?: string, findings?: Record<string, any>[], review?: Record<string, any>, reply?: (text: string, second: boolean) => Record<string, any>}} [opts] */
async function fixture(opts = {}) {
  const file = opts.file ?? FILE;
  const repoRoot = freshDir('recheck');
  mkdirSync(path.dirname(path.join(repoRoot, file)), { recursive: true });
  writeFileSync(path.join(repoRoot, file), opts.content ?? 'export const a = 1;\n');
  const rows = /** @type {Record<string, any>[]} */ ([]);
  const packets = /** @type {string[]} */ ([]);
  const models = /** @type {string[]} */ ([]);
  const deps = {
    repoRoot, workDir: freshDir('packet'), cfg: cfgFor(opts.review), jev,
    writeRow: async (/** @type {Record<string, any>} */ row) => void rows.push(row),
    review: async () => ({ status: 'reviewed', findings: opts.findings ?? [finding()] }),
    schemaHistory: async () => ({ failures: rows.filter((r) => r.event === 'review.schema_invalid' && !r.second_level).length, fellBack: rows.filter((r) => r.event === 'review.schema_fallback').length > 0 }),
    spawn: async (/** @type {Record<string, any>} */ request) => {
      const text = readFileSync(request.promptPath, 'utf8');
      packets.push(text);
      models.push(request.cfg?.levels?.L2?.model ?? PRIMARY);
      return { status: 'ok', exit_code: 0, provider: request.cfg?.levels?.L2?.provider ?? 'anthropic', model: models.at(-1), usage: { tokens_in: 900, tokens_out: 300 }, answer: opts.reply?.(text, !!request.cfg) ?? answerFor(text) };
    },
  };
  const state = await runRound(newFileState({ file, level: 'L2' }), deps);
  const edit = (content = 'export const a = 2;\n') => writeFileSync(path.join(repoRoot, file), content);
  const rounds = () => rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind, r.closed, r.open_after]);
  return { state, deps, rows, packets, models, edit, rounds };
}

test('P2 reproduction: a valid answer skipping F1 is schema, never a counted stall; no second level', async () => {
  const f = await fixture();
  f.edit();
  await runRound(f.state, f.deps);
  assert.deepEqual(f.state.next, { action: 'retry', reason: 'schema' });
  assert.deepEqual([f.state.round, f.state.rounds_at_level, f.state.open.map((r) => r.id)], [1, 1, ['F1']]);
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 1]]);
  assert.deepEqual(f.rows.filter((r) => r.event === 'review.unavailable').map((r) => r.reason), ['schema']);
  assert.deepEqual(f.models, [PRIMARY]);
  assert.equal(f.rows.filter((r) => r.event === 'review.schema_fallback').length, 0);
});

test('P2: second miss tries the second level once; skipping F1 there is also schema', async () => {
  const f = await fixture({ review: { second_levels: { L2: { provider: 'xai', model: SECOND } } } });
  f.edit();
  for (let i = 0; i < 3; i += 1) {
    await runRound(f.state, f.deps);
    assert.deepEqual([f.state.next, f.state.round, f.state.rounds_at_level, f.state.open.map((r) => r.id)], [{ action: 'retry', reason: 'schema' }, 1, 1, ['F1']]);
  }
  assert.deepEqual(f.models, [PRIMARY, PRIMARY, SECOND, PRIMARY]);
  assert.equal(new Set(f.packets).size, 1);
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 1]]);
  assert.deepEqual(f.rows.filter((r) => r.event === 'review.schema_invalid').map((r) => r.second_level), [false, false, true, false]);
  assert.deepEqual(f.rows.filter((r) => r.event === 'review.schema_fallback').map((r) => [r.attempts, r.trigger, r.to_model]), [[2, 'schema', SECOND]]);
  assert.equal(f.rows.filter((r) => r.event === 'review.unavailable').length, 3);
});

test('P2: resolving F1 while skipping F2 rejects the whole packet without closing either finding', async () => {
  for (const second of [false, true]) {
    const f = await fixture({
      findings: [finding('F1'), finding('F2')],
      review: second ? { second_levels: { L2: { provider: 'xai', model: SECOND } } } : {},
      reply: (text) => answerFor(text, { resolved: [{ id: 'F1', resolved: true, why: 'fixed' }] }),
    });
    f.edit();
    for (let i = 0; i < 3; i += 1) {
      await runRound(f.state, f.deps);
      assert.deepEqual([f.state.next, f.state.round, f.state.rounds_at_level, f.state.open.map((r) => r.id)], [{ action: 'retry', reason: 'schema' }, 1, 1, ['F1', 'F2']]);
    }
    assert.deepEqual(f.rounds(), [[1, 'full', 0, 2]]);
    assert.deepEqual(f.models, second ? [PRIMARY, PRIMARY, SECOND, PRIMARY] : [PRIMARY, PRIMARY, PRIMARY]);
    assert.deepEqual(f.rows.filter((r) => r.event === 'review.schema_invalid').map((r) => r.second_level), second ? [false, false, true, false] : [false, false, false]);
    assert.deepEqual(f.rows.filter((r) => r.event === 'review.schema_fallback').map((r) => [r.attempts, r.trigger, r.to_model]), second ? [[2, 'schema', SECOND]] : []);
    assert.deepEqual(f.rows.filter((r) => r.event === 'review.unavailable').map((r) => r.reason), ['schema', 'schema', 'schema']);
    assert.equal(f.rows.filter((r) => r.event === 'review.cap').length, 0);
    assert.deepEqual(f.packets.map((text) => [...text.slice(text.indexOf('## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => m[1])), second ? [['F1', 'F2'], ['F1', 'F2'], ['F1', 'F2'], ['F1', 'F2']] : [['F1', 'F2'], ['F1', 'F2'], ['F1', 'F2']]);
  }
});

test('P1: unchanged recheck keeps counts and findings, writes no round and runs no session', async () => {
  const f = await fixture();
  const before = structuredClone(f.state);
  await runRound(f.state, f.deps);
  assert.deepEqual([f.state.round, f.state.rounds_at_level, f.state.open, f.state.reviewed_content], [before.round, before.rounds_at_level, before.open, before.reviewed_content]);
  assert.deepEqual(f.state.next, { action: 'fix', level: 'L2', trigger: null, reason: 'no-fix-hunks' });
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 1]]);
  assert.equal(f.packets.length, 0);
  assert.equal(f.rows.filter((r) => r.event === 'review.cap').length, 0);
  // A driver whose fix makes no change returns to the caller without spinning.
  let fixes = 0;
  await converge(f.state, { ...f.deps, fix: async () => { fixes += 1; } });
  assert.equal(fixes, 1);
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 1]]);
});

test('empty session with zero output is unavailable without a counted round or stall', async () => {
  const f = await fixture();
  f.edit();
  await runRound(f.state, { ...f.deps, spawn: async () => ({ status: 'ok', exit_code: 0, answer: null, usage: { tokens_in: 900, tokens_out: 0 } }) });
  assert.deepEqual([f.state.next, f.state.round, f.state.rounds_at_level, f.state.open.map((r) => r.id)], [{ action: 'retry', reason: 'schema' }, 1, 1, ['F1']]);
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 1]]);
  assert.deepEqual(f.rows.filter((r) => r.event === 'review.unavailable').map((r) => r.reason), ['schema']);
});

test('real resolved false counts and still triggers review_stall', async () => {
  const f = await fixture({ reply: (text) => answerFor(text, { resolved: [{ id: 'F1', resolved: false, why: 'still open' }] }) });
  f.edit();
  await runRound(f.state, f.deps);
  assert.deepEqual([f.state.round, f.state.rounds_at_level, f.state.open.map((r) => r.id)], [2, 2, ['F1']]);
  assert.deepEqual(f.state.next, { action: 'patch', level: 'L3', trigger: 'review_stall' });
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 1], [2, 'recheck', 0, 1]]);
  assert.equal(f.packets.length, 1);
});

test('real resolved true closes F1 and approves exactly once', async () => {
  const f = await fixture({ reply: (text) => answerFor(text, { resolved: [{ id: 'F1', resolved: true, why: 'fixed' }] }) });
  f.edit();
  await runRound(f.state, f.deps);
  assert.deepEqual([f.state.round, f.state.open, f.state.status, f.state.next], [2, [], 'complete', { action: 'complete' }]);
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 1], [2, 'recheck', 1, 0]]);
  assert.equal(f.packets.length, 1);
  assert.equal(f.rows.filter((r) => r.event === 'review.approved').length, 1);
});

test('Markdown section packets require only their own open finding, and reject a local omission', async () => {
  const file = 'docs/sample.md';
  const content = (tag = '') => ['# Sample', ...Array.from({ length: 40 }, (_, i) => `alpha ${i} ${'a'.repeat(80)}${tag}`), '## Detail', ...Array.from({ length: 40 }, (_, i) => `beta ${i} ${'b'.repeat(80)}${tag}`)].join('\n') + '\n';
  const findings = [finding('F1', 10, file), finding('F2', 60, file)];
  const f = await fixture({ file, content: content(), findings, reply: (text) => answerFor(text, { resolved: [...text.slice(text.indexOf('## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => ({ id: m[1], resolved: true, why: 'fixed' })) }) });
  const diff = await fixHunkDiff({ file, previous: content(), current: content(' fixed'), workDir: f.deps.workDir });
  const groups = sectionGroups(diff);
  assert.equal(groups.length, 2);
  const budget = Math.max(...groups.map((g, i) => recheckTokens(g.diff, [findings[i]])));
  assert.ok(recheckTokens(diff, findings) > budget);
  f.deps.cfg.review.budgets = { full_in: budget };
  f.edit(content(' fixed'));
  const before = structuredClone(f.state);
  await runRound(f.state, f.deps);
  assert.equal(f.packets.length, 2);
  assert.deepEqual(f.packets.map((text) => [...text.slice(text.indexOf('## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => m[1])), [['F1'], ['F2']]);
  assert.deepEqual([f.state.status, f.state.open], ['complete', []]);
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 2], [2, 'recheck', 2, 0]]);
  const negativePackets = /** @type {string[]} */ ([]);
  await runRound(before, { ...f.deps, spawn: async (opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    negativePackets.push(text);
    return { status: 'ok', exit_code: 0, answer: answerFor(text, { resolved: text.includes('- F1 (') ? [{ id: 'F1', resolved: true, why: 'fixed' }] : [] }), usage: { tokens_in: 900, tokens_out: 300 } };
  } });
  assert.deepEqual(negativePackets.map((text) => [...text.slice(text.indexOf('## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => m[1])), [['F1'], ['F2']]);
  assert.deepEqual([before.next, before.round, before.rounds_at_level, before.open.map((r) => r.id)], [{ action: 'retry', reason: 'schema' }, 1, 1, ['F1', 'F2']]);
  assert.deepEqual(f.rows.filter((r) => r.event === 'review.unavailable').map((r) => [r.reason, r.section]), [['schema', 2]]);
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 2], [2, 'recheck', 2, 0]]);
});

test('patch_check with unchanged content still stops l3_patch_exhausted under B73', async () => {
  const f = await fixture();
  f.state.l3_rung_used = true;
  await runRound(f.state, f.deps, { kind: 'patch_check' });
  assert.deepEqual([f.state.status, f.state.next, f.state.round, f.state.rounds_at_level, f.state.open.map((r) => r.id)], ['stopped', { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' }, 1, 0, ['F1']]);
  assert.deepEqual(f.rounds(), [[1, 'full', 0, 1], [1, 'patch_check', 0, 1]]);
  assert.equal(f.packets.length, 0);
  assert.deepEqual(f.rows.filter((r) => r.event === 'review.cap').map((r) => [r.reason, r.open]), [['l3_patch_exhausted', ['F1']]]);
});

/** A real worker and signed state, with fake sessions only. */
async function wired() {
  const { repo, runId } = await makeRepo();
  const key = await loadKey(runId);
  const rows = /** @type {Record<string, any>[]} */ ([]);
  let mode = 'ok';
  const worker = await createWorker({ runId, repoRoot: repo, cfg: cfgFor(), runRootDir: freshDir('runroot'), slug: 'worker-test', key }, {
    store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, jev,
    writeRow: async (row) => void rows.push(row), readRows: async () => rows,
    spawn: /** @type {any} */ (async (/** @type {Record<string, any>} */ opts) => {
      const text = readFileSync(opts.promptPath, 'utf8');
      const head = { provider: 'anthropic', model: PRIMARY, usage: { tokens_in: 901, tokens_out: mode === 'fail' ? 0 : 301 } };
      if (mode === 'fail') return { ...head, status: 'invalid-output', reason: 'fake schema miss', exit_code: 0, answer: null };
      if (mode === 'missing') return { ...head, exit_code: 0, answer: null };
      return { ...head, status: 'ok', exit_code: 0, answer: answerFor(text, opts.rowExtra?.lens === 'recheck' ? { resolved: [{ id: 'F1', resolved: mode !== 'open', why: mode === 'open' ? 'still open' : 'fixed' }] } : { passed: false, findings: [finding()] }) };
    }),
  });
  const round = async () => {
    let ticket = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: FILE });
    if (ticket.status === 'done') {
      rmSync(path.join(queueDir(repo), `${ticket.ticket}.done`));
      ticket = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: FILE });
    }
    assert.equal(await worker.drain(), 1);
    return /** @type {Record<string, any>} */ (readResult(repo, runId, ticket.ticket));
  };
  return { repo, rows, key, round, fail: () => { mode = 'fail'; }, missingStatus: () => { mode = 'missing'; }, leaveOpen: () => { mode = 'open'; }, recover: () => { mode = 'ok'; } };
}

test('worker unchanged recheck returns reviewed no-fix-hunks with F1, zero sessions or new rounds', async () => {
  const w = await wired();
  await w.round();
  const result = await w.round();
  assert.deepEqual([result.status, result.approved, result.reason, result.round, result.findings.map((/** @type {any} */ r) => r.id), result.sessions], ['reviewed', false, 'no-fix-hunks', 1, ['F1'], []]);
  assert.deepEqual(w.rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind]), [[1, 'full']]);
  assert.equal(w.rows.filter((r) => r.event === 'review.cap').length, 0);
});

test('worker recheck summary includes status, exact token counts and reason only for a failed string status', async () => {
  const w = await wired();
  await w.round();
  writeFileSync(path.join(w.repo, FILE), 'export const a = 2;\n');
  w.fail();
  const failed = await w.round();
  assert.deepEqual([failed.status, failed.reason, failed.round, failed.approved], ['unavailable', 'schema', 1, false]);
  assert.deepEqual(failed.sessions, [{ lens: 'recheck', role: 'reviewer', level: 'L2', provider: 'anthropic', model: PRIMARY, status: 'invalid-output', tokens_in: 901, tokens_out: 0, reason: 'fake schema miss' }]);
  w.missingStatus();
  const missing = await w.round();
  assert.deepEqual([missing.status, missing.reason, missing.round, missing.approved], ['unavailable', 'schema', 1, false]);
  assert.deepEqual(missing.sessions, [{ lens: 'recheck', role: 'reviewer', level: 'L2', provider: 'anthropic', model: PRIMARY, status: null, tokens_in: 901, tokens_out: 301 }]);
  w.recover();
  const fixed = await w.round();
  assert.deepEqual([fixed.status, fixed.approved, fixed.round], ['reviewed', true, 2]);
  assert.deepEqual(fixed.sessions, [{ lens: 'recheck', role: 'reviewer', level: 'L2', provider: 'anthropic', model: PRIMARY, status: 'ok', tokens_in: 901, tokens_out: 301 }]);
  assert.deepEqual(w.rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind]), [[1, 'full'], [2, 'recheck']]);
  assert.equal(w.rows.filter((r) => r.event === 'review.approved').length, 1);
  assert.equal(w.rows.filter((r) => r.event === 'review.round').map((r) => verifyRow(r, w.key).ok).filter(Boolean).length, 2);
});

test('worker counted recheck leaving F1 open keeps findings-open as the outcome reason', async () => {
  const w = await wired();
  await w.round();
  writeFileSync(path.join(w.repo, FILE), 'export const a = 2;\n');
  w.leaveOpen();
  const result = await w.round();
  assert.deepEqual([result.status, result.approved, result.reason, result.round, result.findings.map((/** @type {any} */ r) => r.id)], ['reviewed', false, 'findings-open', 2, ['F1']]);
  assert.deepEqual([result.next, result.trigger], [{ action: 'patch', level: 'L3', trigger: 'review_stall' }, 'review_stall']);
  assert.deepEqual(result.sessions, [{ lens: 'recheck', role: 'reviewer', level: 'L2', provider: 'anthropic', model: PRIMARY, status: 'ok', tokens_in: 901, tokens_out: 301 }]);
  assert.deepEqual(w.rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind, r.closed, r.open_after]), [[1, 'full', 0, 1], [2, 'recheck', 0, 1]]);
});
