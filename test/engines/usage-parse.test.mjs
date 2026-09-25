import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parseUsage } from '../../src/engines/usage-parse.mjs';

test('parseUsage("claude", ...) reports tokens when usage.input_tokens/output_tokens are present (no cache fields)', () => {
  const result = parseUsage('claude', { usage: { input_tokens: 100, output_tokens: 50 } });
  assert.deepEqual(result, { tokensIn: 100, tokensOut: 50, tokensSource: 'reported' });
});

// Fix round 1 (MAJOR): cache-creation/cache-read tokens are SEPARATE fields in the Anthropic
// Messages API shape Claude Code's JSON result reuses — folding only input_tokens undercounts a
// cached run. tokensIn must be the SUM, and the split is kept on the record.
test('parseUsage("claude", ...) folds cache_creation_input_tokens + cache_read_input_tokens into tokensIn, and keeps the split', () => {
  const result = parseUsage('claude', { usage: { input_tokens: 5, output_tokens: 50, cache_creation_input_tokens: 200, cache_read_input_tokens: 1000 } });
  assert.deepEqual(result, {
    tokensIn: 5 + 200 + 1000,
    tokensOut: 50,
    tokensSource: 'reported',
    cacheCreationTokens: 200,
    cacheReadTokens: 1000,
  });
});

test('parseUsage("claude", ...) treats a MISSING cache field as 0 and does not add a split key for it', () => {
  const result = parseUsage('claude', { usage: { input_tokens: 5, output_tokens: 50, cache_read_input_tokens: 1000 } });
  assert.deepEqual(result, { tokensIn: 5 + 1000, tokensOut: 50, tokensSource: 'reported', cacheReadTokens: 1000 });
  assert.equal('cacheCreationTokens' in result, false);
});

test('parseUsage("claude", ...) falls back to estimated when a PRESENT cache field is malformed (never silently drops it to 0)', () => {
  assert.deepEqual(parseUsage('claude', { usage: { input_tokens: 5, output_tokens: 50, cache_creation_input_tokens: -1 } }), {
    tokensIn: null,
    tokensOut: null,
    tokensSource: 'estimated',
  });
});

test('parseUsage("claude", ...) falls back to estimated when usage is absent', () => {
  assert.deepEqual(parseUsage('claude', { result: 'no usage field here' }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
});

test('parseUsage("claude", ...) falls back to estimated on malformed usage (non-integer, negative) — BOTH input and output independently', () => {
  assert.deepEqual(parseUsage('claude', { usage: { input_tokens: 1.5, output_tokens: 50 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
  assert.deepEqual(parseUsage('claude', { usage: { input_tokens: -1, output_tokens: 50 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
  // A VALID input paired with an INVALID output must also degrade — not just the reverse.
  assert.deepEqual(parseUsage('claude', { usage: { input_tokens: 50, output_tokens: -1 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
  assert.deepEqual(parseUsage('claude', { usage: { input_tokens: 50, output_tokens: 2.5 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
});

test('parseUsage: the zero boundary is "reported", not "estimated" (0 is a legitimate token count)', () => {
  assert.deepEqual(parseUsage('claude', { usage: { input_tokens: 0, output_tokens: 0 } }), { tokensIn: 0, tokensOut: 0, tokensSource: 'reported' });
});

test('parseUsage: a PARTIAL usage object (only one of the two required fields) is estimated, never half-reported', () => {
  assert.deepEqual(parseUsage('claude', { usage: { input_tokens: 100 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
  assert.deepEqual(parseUsage('claude', { usage: { output_tokens: 100 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
});

test('parseUsage: a numeric-STRING token count is estimated, never coerced', () => {
  assert.deepEqual(parseUsage('claude', { usage: { input_tokens: '100', output_tokens: 50 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
});

// Fix round 1 (MINOR): Codex/Grok must pick ONE naming scheme per payload — never mix
// input_tokens from one vendor's convention with completion_tokens from another's.
test('parseUsage("codex", ...) accepts input_tokens/output_tokens when BOTH present', () => {
  assert.deepEqual(parseUsage('codex', { usage: { input_tokens: 10, output_tokens: 5 } }), { tokensIn: 10, tokensOut: 5, tokensSource: 'reported' });
});

test('parseUsage("codex", ...) accepts prompt_tokens/completion_tokens when BOTH present', () => {
  assert.deepEqual(parseUsage('codex', { usage: { prompt_tokens: 20, completion_tokens: 8 } }), { tokensIn: 20, tokensOut: 8, tokensSource: 'reported' });
});

test('parseUsage("codex", ...) NEVER mixes schemes — input_tokens present with completion_tokens (not output_tokens) is estimated, not a mixed reading', () => {
  assert.deepEqual(parseUsage('codex', { usage: { input_tokens: 10, completion_tokens: 5 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
  assert.deepEqual(parseUsage('codex', { usage: { prompt_tokens: 10, output_tokens: 5 } }), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
});

test('parseUsage("grok", ...) accepts BOTH naming schemes, same as codex (never mixed)', () => {
  assert.deepEqual(parseUsage('grok', { usage: { input_tokens: 7, output_tokens: 2 } }), { tokensIn: 7, tokensOut: 2, tokensSource: 'reported' });
  assert.deepEqual(parseUsage('grok', { usage: { prompt_tokens: 3, completion_tokens: 1 } }), { tokensIn: 3, tokensOut: 1, tokensSource: 'reported' });
  assert.deepEqual(parseUsage('grok', {}), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
});

test('parseUsage falls back to estimated for non-object / array / null parsed input, never throws', () => {
  assert.deepEqual(parseUsage('claude', null), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
  assert.deepEqual(parseUsage('claude', []), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
  assert.deepEqual(parseUsage('claude', 'not even an object'), { tokensIn: null, tokensOut: null, tokensSource: 'estimated' });
});

test('parseUsage throws for an ordinary unknown cli', () => {
  assert.throws(() => parseUsage(/** @type {any} */ ('unknown-cli'), {}), /unknown cli/);
});

// Fix round 1 (MINOR): PARSERS[cli] also resolved prototype-chain members.
test('parseUsage throws the documented "unknown cli" error for a prototype-chain name, never an unrelated crash or wrong result', () => {
  for (const trap of ['constructor', 'toString', '__proto__', 'hasOwnProperty', 'valueOf']) {
    assert.throws(() => parseUsage(/** @type {any} */ (trap), {}), /unknown cli/, `cli ${trap} should be refused`);
  }
});

// Fix round 3 (MINOR): the Messages API types both cache fields as `integer | null`; a null one is
// "no caching", not malformed — the result stays `reported` with an exact tokensIn.
test('parseUsage("claude", ...) treats a JSON-null cache field as 0 (reported, no split key), not as malformed', () => {
  const result = parseUsage('claude', { usage: { input_tokens: 7, output_tokens: 9, cache_creation_input_tokens: 40, cache_read_input_tokens: null } });
  assert.deepEqual(result, { tokensIn: 47, tokensOut: 9, tokensSource: 'reported', cacheCreationTokens: 40 });
  const bothNull = parseUsage('claude', { usage: { input_tokens: 7, output_tokens: 9, cache_creation_input_tokens: null, cache_read_input_tokens: null } });
  assert.deepEqual(bothNull, { tokensIn: 7, tokensOut: 9, tokensSource: 'reported' });
});
