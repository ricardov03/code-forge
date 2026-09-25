import { cfgWith, fakeDeps, freshDir, readRecords, sink, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { copyFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { S2_SCHEMA, S2_MAX_PROMPT_TOKENS, buildS2Prompt, isS2Heavy, runS2 } = await import('../../src/session/s2.mjs');
const { estimateTokens, SessionError } = await import('../../src/session/spawn.mjs');
const { runS2Verb } = await import('../../src/cli/s2.mjs');
const { compileSchema } = await import('../../src/config/schema-compile.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');
const { DEFAULT_ANSWER } = await import('../fixtures/bin/fake-common.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

const SOURCE_SNAPSHOT = {
  type: 'object',
  properties: {
    decision: { type: 'string' },
    confidence: { type: 'number', minimum: 0, maximum: 1 },
    reason: { type: 'string' },
    overrule: { type: 'boolean' },
    ask_human: { type: 'boolean' },
    human_question: { type: ['string', 'null'] },
  },
  required: ['decision', 'confidence', 'reason', 'overrule', 'ask_human', 'human_question'],
  additionalProperties: false,
};

const OPENAI_SNAPSHOT = {
  type: 'object',
  properties: {
    decision: { anyOf: [{ type: 'string' }, { type: 'null' }] },
    confidence: { anyOf: [{ type: 'number', minimum: 0, maximum: 1 }, { type: 'null' }] },
    reason: { anyOf: [{ type: 'string' }, { type: 'null' }] },
    overrule: { anyOf: [{ type: 'boolean' }, { type: 'null' }] },
    ask_human: { anyOf: [{ type: 'boolean' }, { type: 'null' }] },
    human_question: { type: ['string', 'null'] },
  },
  required: ['decision', 'confidence', 'reason', 'overrule', 'ask_human', 'human_question'],
  additionalProperties: false,
};

test('compiled S2 schema per provider (3 snapshots)', () => {
  assert.deepEqual(compileSchema(S2_SCHEMA, 'anthropic'), SOURCE_SNAPSHOT);
  assert.deepEqual(compileSchema(S2_SCHEMA, 'xai'), SOURCE_SNAPSHOT);
  assert.deepEqual(compileSchema(S2_SCHEMA, 'openai', { strict: true }), OPENAI_SNAPSHOT);
});

test('the S2 prompt stays at or under 4k tokens when the context is 100 KB', () => {
  const prompt = buildS2Prompt({ question: 'Split B9 into two blocks?', context: 'é'.repeat(50_000), options: ['yes', 'no'] });
  const tokens = estimateTokens(Buffer.byteLength(prompt));
  assert.equal(S2_MAX_PROMPT_TOKENS, 4000);
  assert.deepEqual([tokens <= 4000, tokens >= 3990, prompt.includes('�'), prompt.endsWith('[context cut: 100000 bytes given]\n')], [true, true, false, true]);
});

test('a question that alone is over budget is refused', () => {
  assert.throws(
    () => buildS2Prompt({ question: 'q'.repeat(16_001) }),
    (err) => err instanceof SessionError && err.code === 's2-too-large',
  );
});

test('the prompt never exceeds the budget when the fixed part leaves no room for context', () => {
  const maxTokens = 200;
  const probe = buildS2Prompt({ question: 'x' }, { maxTokens });
  const question = 'x'.repeat(maxTokens * 4 - (Buffer.byteLength(probe) - 1) - 30); // fixed part = limit - 30 bytes
  const prompt = buildS2Prompt({ question, context: 'c'.repeat(10_000) }, { maxTokens });
  assert.equal(Buffer.byteLength(prompt) <= maxTokens * 4, true);
  assert.equal(maxTokens * 4 - Buffer.byteLength(prompt), 30);
});

test('runS2: L3 answer on the first step is source s2; after a 402 the fallback row is s2-fallback + l3_fallback', async () => {
  const cfg = cfgWith({ provider: 'anthropic', model: 'fake-fable', fallback: [{ provider: 'xai', model: 'fake-grok' }] });
  const first = fakeDeps();
  const ok = await runS2({ cfg, packet: { question: 'go?' }, slug: 'b9a-s2', block: 'B2' }, first.deps);
  assert.deepEqual([ok.status, ok.answer], ['ok', DEFAULT_ANSWER]);
  const down = fakeDeps({ FAKE_402_MODELS: 'fake-fable' });
  const fell = await runS2({ cfg, packet: { question: 'go?' }, slug: 'b9a-s2', block: 'B2' }, down.deps);
  const rows = await readAllRows('b9a-s2');
  assert.deepEqual(
    rows.map((r) => [r.role, r.provider, r.model, r.source, r.status, r.l3_fallback ?? false]),
    [
      ['s2', 'anthropic', 'fake-fable', 's2', 'ok', false],
      ['s2', 'anthropic', 'fake-fable', 's2', 'unavailable', false],
      ['s2', 'xai', 'fake-grok', 's2-fallback', 'ok', true],
    ],
  );
  const spawned = readRecords(down.records).map((r) => [r.name, r.argv[r.argv.findIndex((a) => a === '--model' || a === '-m') + 1]]);
  assert.deepEqual(spawned, [
    ['claude', 'fake-fable'],
    ['grok', 'fake-grok'],
  ]);
  assert.deepEqual([fell.status, fell.fallback_step], ['ok', 1]);
});

test('runS2 refuses an answer whose reason is over 280 characters', async () => {
  const { deps } = fakeDeps({ FAKE_ANSWER: JSON.stringify({ ...DEFAULT_ANSWER, reason: 'r'.repeat(281) }) });
  const result = await runS2({ cfg: cfgWith({ provider: 'anthropic', model: 'fake-fable' }), packet: { question: 'go?' } }, deps);
  assert.deepEqual([result.status, result.answer], ['invalid-output', null]);
});

test('s2-heavy: 7 non-fallback S2 calls in a block flag it, 6 plus fallbacks do not', () => {
  const row = (source) => ({ event: 'session', role: 's2', block: 'B3', source });
  assert.equal(isS2Heavy(Array(7).fill(row('s2')), 'B3'), true);
  assert.equal(isS2Heavy([...Array(6).fill(row('s2')), row('s2-fallback'), row('s2-fallback')], 'B3'), false);
});

test('s2 verb prints the answer and exits 0; exits 3 when L3 has no available step', async () => {
  const ws = freshDir('verb');
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
  const packet = writeIn(ws, 'packet.json', JSON.stringify({ question: 'go?' }));
  const before = process.cwd();
  process.chdir(ws);
  try {
    const { deps } = fakeDeps();
    const stdout = sink();
    assert.equal(await runS2Verb(['--packet', packet], { ...deps, stdout }), 0);
    assert.deepEqual(JSON.parse(stdout.text()), DEFAULT_ANSWER);
    const down = fakeDeps({ FAKE_402_MODELS: 'claude-fable-5-1' });
    assert.equal(await runS2Verb(['--packet', packet], { ...down.deps, stdout: sink() }), 3);
    const bad = fakeDeps();
    assert.equal(await runS2Verb(['--packet', path.join(ws, 'missing.json')], { ...bad.deps, stdout: sink() }), 2);
    assert.deepEqual(bad.stderr.text().split('\n').filter(Boolean), ['s2: --packet cannot be read']);
  } finally {
    process.chdir(before);
  }
});
