import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildClaudeArgv } from '../../src/engines/builders/claude.mjs';
import { buildCodexArgv, VALID_EFFORTS as CODEX_VALID_EFFORTS } from '../../src/engines/builders/codex.mjs';
import { buildGrokArgv } from '../../src/engines/builders/grok.mjs';
import { assertEffortForProvider, checkEffort, displayEffort, knownEffortWords, PROVIDER_EFFORTS } from '../../src/engines/efforts.mjs';

// B29 (issue #2): one per-provider effort table, read by the builders AND by validateConfig.

const base = { promptPath: '/p', cwd: '/c' };
const FAKE_SECRET = 'sk-ant-FAKE0123456789abcdefghijklmnop';
const NONE = [];

test('PROVIDER_EFFORTS: exact lists per provider, xai free-form (null), the table and every list frozen', () => {
  assert.deepEqual(Object.keys(PROVIDER_EFFORTS).sort(), ['anthropic', 'openai', 'xai']);
  assert.deepEqual([...(PROVIDER_EFFORTS.anthropic ?? [])], ['low', 'medium', 'high', 'xhigh', 'max']);
  assert.deepEqual([...(PROVIDER_EFFORTS.openai ?? [])], ['minimal', 'low', 'medium', 'high']);
  assert.equal(PROVIDER_EFFORTS.xai, null);
  assert.equal(Object.isFrozen(PROVIDER_EFFORTS), true);
  assert.equal(Object.isFrozen(PROVIDER_EFFORTS.anthropic), true);
  assert.equal(Object.isFrozen(PROVIDER_EFFORTS.openai), true);
});

test('the codex builder re-exports the SAME list object (not a copy)', () => {
  assert.equal(CODEX_VALID_EFFORTS, PROVIDER_EFFORTS.openai);
});

test('knownEffortWords: the 6 distinct words of every closed list; displayEffort echoes only those', () => {
  assert.deepEqual([...knownEffortWords()].sort(), ['high', 'low', 'max', 'medium', 'minimal', 'xhigh']);
  assert.equal(displayEffort('xhigh'), '"xhigh"');
  assert.equal(displayEffort(FAKE_SECRET), '(unrecognised value)');
  assert.equal(displayEffort(''), '(unrecognised value)');
});

test('checkEffort: the whole result for every kind', () => {
  assert.deepEqual(checkEffort('openai', undefined), { kind: 'ok', allowed: NONE });
  assert.deepEqual(checkEffort('openai', 'high'), { kind: 'ok', allowed: PROVIDER_EFFORTS.openai });
  assert.deepEqual(checkEffort('openai', 'xhigh'), { kind: 'invalid', allowed: PROVIDER_EFFORTS.openai });
  assert.deepEqual(checkEffort('openai', ''), { kind: 'invalid', allowed: PROVIDER_EFFORTS.openai });
  assert.deepEqual(checkEffort('anthropic', ''), { kind: 'invalid', allowed: PROVIDER_EFFORTS.anthropic });
  assert.deepEqual(checkEffort('xai', 'anything'), { kind: 'ok', allowed: NONE });
  assert.deepEqual(checkEffort('xai', ''), { kind: 'empty', allowed: NONE });
  assert.deepEqual(checkEffort('not-a-provider', 'high'), { kind: 'unknown-provider', allowed: NONE });
  assert.deepEqual(checkEffort('constructor', 'high'), { kind: 'unknown-provider', allowed: NONE });
  // The no-effort form, through an injected table (no current provider uses it).
  assert.deepEqual(checkEffort('none', 'high', { none: [] }), { kind: 'unsupported', allowed: NONE });
  assert.deepEqual(checkEffort('none', undefined, { none: [] }), { kind: 'ok', allowed: NONE });
});

test('assertEffortForProvider: a no-effort provider throws (injected table)', () => {
  assert.throws(() => assertEffortForProvider('buildX', /** @type {any} */ ('none'), 'high', { none: [] }), {
    name: 'TypeError',
    message: 'buildX: provider none takes no effort, got "high"',
  });
});

test('codex builder: "xhigh" throws the same message as before B29; every listed effort builds', () => {
  assert.throws(() => buildCodexArgv({ ...base, role: 'coder', model: 'gpt-6-sol', effort: 'xhigh' }), {
    name: 'TypeError',
    message: 'buildCodexArgv: effort must be one of minimal, low, medium, high when given, got "xhigh"',
  });
  let built = 0;
  for (const effort of PROVIDER_EFFORTS.openai ?? []) {
    // B32: Codex refuses role judge (no no-tools mode); facts takes the same closed-book branch
    assert.doesNotThrow(() => buildCodexArgv({ ...base, role: 'facts', model: 'gpt-6-sol', effort }));
    built += 1;
  }
  assert.equal(built, 4);
});

test('codex builder: a fake secret as effort throws with 0 occurrences of it in the message', () => {
  assert.throws(() => buildCodexArgv({ ...base, role: 'facts', model: 'gpt-6-sol', effort: FAKE_SECRET }), (err) => {
    assert.equal(/** @type {Error} */ (err).message, 'buildCodexArgv: effort must be one of minimal, low, medium, high when given, got (unrecognised value)');
    assert.equal(/** @type {Error} */ (err).message.split(FAKE_SECRET).length - 1, 0);
    return true;
  });
});

test('claude builder uses the shared list: all 5 anthropic efforts land after --effort; "minimal" throws', () => {
  let built = 0;
  for (const effort of PROVIDER_EFFORTS.anthropic ?? []) {
    const b = buildClaudeArgv({ ...base, role: 'reviewer', model: 'claude-opus-5-5', effort });
    assert.equal(b.argv[b.argv.indexOf('--effort') + 1], effort);
    built += 1;
  }
  assert.equal(built, 5);
  assert.throws(() => buildClaudeArgv({ ...base, role: 'reviewer', model: 'claude-opus-5-5', effort: 'minimal' }), {
    name: 'TypeError',
    message: 'buildClaudeArgv: effort must be one of low, medium, high, xhigh, max when given, got "minimal"',
  });
});

test('grok builder: "xhigh" builds; an empty effort throws without echoing it', () => {
  const built = buildGrokArgv({ ...base, role: 'reviewer', model: 'grok-4.7', effort: 'xhigh' });
  assert.equal(built.argv[built.argv.indexOf('--reasoning-effort') + 1], 'xhigh');
  assert.throws(() => buildGrokArgv({ ...base, role: 'reviewer', model: 'grok-4.7', effort: '' }), {
    name: 'TypeError',
    message: 'buildGrokArgv: effort must be a non-empty string when given, got (unrecognised value)',
  });
});
