import { answerFor, freshDir, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { parse } from 'yaml';

const { reviewFile } = await import('../../src/review/engine.mjs');
const { schemaErrorsOf } = await import('../../src/review/validate-review.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, readResult, isDone } = await import('../../src/worker/queue.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const commonDir = execFileSync('git', ['rev-parse', '--path-format=absolute', '--git-common-dir'],
  { cwd: new URL('../../', import.meta.url), encoding: 'utf8', env: gitChildEnv() }).trim();
const FIXTURES = `${path.resolve(commonDir, '..', 'plans/tools/forge/b234')}/`;
const sample = readFileSync(`${FIXTURES}sample.jsonl`, 'utf8').trim().split('\n').map((s) => JSON.parse(s));
const miss = sample.find((r) => r.event === 'review.schema_invalid');
const route = sample.find((r) => r.event === 'review.route');
const FILE = 'src/example.mjs';
let sequence = 0;

/** @param {(model: string, hunks: string[]) => Record<string, any>} answer */
async function review(answer) {
  const repo = await makeRepo();
  writeFile(repo, FILE, lines(route.lines));
  const cfg = parse(readFileSync(`${FIXTURES}sample-run-config.yml`, 'utf8'));
  const runId = `r-route-schema-${process.pid}-${++sequence}`;
  await startRun({ workspace: repo, project: 'route-schema-test', runId, config: cfg, writeRow: async () => {} });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'review', tests: ['route-schema'] }], writeRow: async () => {} });
  const key = await loadKey(runId);
  const calls = [];
  const packets = [];
  const rows = [];
  const bytes = [];
  const worker = await createWorker({ runId, repoRoot: repo, cfg, runRootDir: freshDir('worker'), slug: 'route-schema-test', key }, {
    store: await createKeyStore({ backends: [], dir: freshDir('keys') }), env: {},
    readRows: async () => rows,
    review: async (_ticket, ctx) => reviewFile({ repoRoot: repo, file: FILE, cfg: ctx.cfg, risk: 1, workDir: freshDir('packets') }, ctx),
    spawn: async (opts) => {
      const entry = (opts.cfg ?? cfg).levels.L2;
      calls.push(entry.model);
      const packet = readFileSync(opts.promptPath, 'utf8');
      packets.push(packet);
      const hunks = packet.split('\n').filter((s) => s.startsWith('- @@')).map((s) => s.slice(2));
      const value = answer(entry.model, hunks);
      // Match the production spawner: rejected JSON remains in text, with no parsed answer.
      const invalid = schemaErrorsOf(value).length > 0;
      const text = JSON.stringify(value).padEnd(miss.answer_bytes, ' ');
      bytes.push(Buffer.byteLength(text));
      return { status: invalid ? 'invalid-output' : 'ok', exit_code: 0,
        provider: entry.provider ?? cfg.provider, model: entry.model,
        answer: invalid ? null : value, text, usage: { tokens_in: 100, tokens_out: 400 } };
    },
    writeRow: async (row) => { rows.push(row); },
  });
  const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE });
  assert.equal(await worker.drain(), 1);
  assert.equal(isDone(repo, ticket.ticket), true, 'the same ticket reaches done');
  const outcome = readResult(repo, runId, ticket.ticket);
  assert.equal(verifyRow(outcome, key).ok, true);
  return { outcome, calls, packets, rows, cfg, key, bytes };
}

/** @param {string[]} hunks @param {Record<string, any>} [fields] */
function findingAnswer(hunks, fields = {}) {
  return answerFor(hunks, { findings: [{ id: 'F1', file: FILE, line_start: 1, line_end: 1,
    severity: 'nit', category: 'style', claim: 'claim', evidence: 'e'.repeat(400), fix: 'fix', ...fields }] });
}

test('customer sample: a 2150-byte JSON answer keeps its finding and records evidence cut to 300', async () => {
  const w = await review((_model, hunks) => findingAnswer(hunks));
  assert.deepEqual([w.outcome.status, w.outcome.approved], ['reviewed', true]);
  assert.equal(w.outcome.findings.length, 1);
  assert.equal(w.outcome.findings[0].evidence, `${'e'.repeat(299)}…`);
  assert.deepEqual(w.outcome.sessions[0].truncated_fields, [{ path: '/findings/0/evidence', original_length: 400, max_length: 300 }]);
  assert.deepEqual(w.bytes, [2150], 'legal JSON whitespace reproduces the sample answer size');
  assert.equal(w.rows.filter((r) => r.event === 'review.schema_invalid').length, 0);
  assert.deepEqual(w.calls, [route.model]);
});

test('other schema errors advance the route ladder immediately in the same review, with one signed answering-model row', async () => {
  const w = await review((model, hunks) => answerFor(hunks, { passed: model === route.model ? null : true }));
  assert.deepEqual([w.outcome.status, w.outcome.approved], ['reviewed', true]);
  assert.deepEqual(w.calls, [route.model, 'claude-opus-5-5']);
  assert.equal(new Set(w.packets).size, 1);
  assert.equal(w.outcome.sessions[0].attempts, 2);
  const rows = w.rows.filter((r) => r.event === 'review.schema_fallback');
  assert.equal(rows.length, 1);
  assert.deepEqual([rows[0].from_model, rows[0].to_provider, rows[0].to_model], [route.model, 'anthropic', 'claude-opus-5-5']);
  assert.equal(verifyRow(rows[0], w.key).ok, true);
});

test('boundary: length repair keeps Unicode intact and never repairs another schema error', async () => {
  const lengths = { id: 64, category: 64, claim: 300, evidence: 300, fix: 300 };
  const long = await review((_model, hunks) => findingAnswer(hunks, Object.fromEntries(
    Object.entries(lengths).map(([field, limit]) => [field, '😀'.repeat(limit + 1)]))));
  assert.deepEqual([long.outcome.status, long.outcome.approved], ['reviewed', true]);
  assert.deepEqual(long.outcome.sessions[0].truncated_fields.map((f) => [f.path, f.original_length, f.max_length]),
    Object.entries(lengths).map(([field, limit]) => [`/findings/0/${field}`, limit + 1, limit]));
  for (const [field, limit] of Object.entries(lengths)) {
    assert.equal(long.outcome.findings[0][field], `${'😀'.repeat(limit - 1)}…`, field);
  }
  assert.deepEqual(long.calls, [route.model]);
  const w = await review((model, hunks) => model === route.model
    ? findingAnswer(hunks, { evidence: '😀'.repeat(400), severity: 'unknown' })
    : answerFor(hunks, { passed: null }));
  assert.deepEqual([w.outcome.status, w.outcome.approved, w.outcome.reason], ['unavailable', false, 'schema']);
  assert.deepEqual(w.calls, [route.model, 'claude-opus-5-5']);
  const errors = w.rows.filter((r) => r.event === 'review.schema_invalid');
  assert.equal(errors.length, 2);
  assert.deepEqual(errors[0].schema_errors.map((e) => e.keyword), ['enum']);
});

test('speed and length boundary: a valid first answer at its limits starts exactly one session and records zero cuts', async () => {
  const w = await review((_model, hunks) => findingAnswer(hunks, { evidence: '😀'.repeat(300), category: 'c'.repeat(64) }));
  assert.deepEqual([w.outcome.status, w.outcome.approved], ['reviewed', true]);
  assert.deepEqual(w.calls, [route.model]);
  assert.equal(w.outcome.sessions[0].attempts, 1);
  assert.deepEqual(w.outcome.sessions[0].truncated_fields ?? [], []);
  assert.equal(w.outcome.findings[0].evidence, '😀'.repeat(300));
  assert.equal(w.rows.filter((r) => r.event === 'review.schema_fallback').length, 0);
  const cut = await review((_model, hunks) => findingAnswer(hunks));
  assert.deepEqual([cut.outcome.status, cut.outcome.approved], ['reviewed', true]);
  assert.deepEqual(cut.calls, [route.model], 'a length-only repair starts no extra session either');
  assert.equal(cut.outcome.sessions[0].attempts, 1);
});
