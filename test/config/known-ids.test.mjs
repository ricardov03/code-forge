import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  ALL_KNOWN_IDS,
  isKnownId,
  KNOWN_IDS,
  knownIdsForProvider,
  LINT_PATTERNS,
  resolveLevel,
} from '../../src/config/known-ids.mjs';

// ── Catalog shape ────────────────────────────────────────────────────────────

test('KNOWN_IDS holds EXACTLY the brief §3 ids (2026-09-24): 4 Anthropic, 3 OpenAI, 4 xAI — 11 total', () => {
  assert.deepEqual(KNOWN_IDS.anthropic, ['claude-haiku-4-5-20251001', 'claude-sonnet-5', 'claude-opus-5-5', 'claude-fable-5-1']);
  assert.deepEqual(KNOWN_IDS.openai, ['gpt-6-luna', 'gpt-6-sol', 'gpt-6-astra']);
  assert.deepEqual(KNOWN_IDS.xai, ['grok-4.7', 'grok-4.7-build-fast', 'grok-4.6', 'grok-4.5']);
  assert.deepEqual(Object.keys(KNOWN_IDS), ['anthropic', 'openai', 'xai']);
  assert.equal(ALL_KNOWN_IDS.length, 11);
});

test('index-alignment contract: LINT_PATTERNS[i] is the catalog pattern for ALL_KNOWN_IDS[i], for every i < 11', () => {
  for (const [i, id] of ALL_KNOWN_IDS.entries()) {
    assert.equal(LINT_PATTERNS[i].test(id), true, `LINT_PATTERNS[${i}] must match ALL_KNOWN_IDS[${i}] ("${id}") — the first 11 patterns are index-aligned with the catalog`);
  }
});

test('knownIdsForProvider/isKnownId agree with the catalog, including for an unknown provider', () => {
  assert.deepEqual(knownIdsForProvider('anthropic'), KNOWN_IDS.anthropic);
  assert.equal(isKnownId('anthropic', 'claude-sonnet-5'), true);
  assert.equal(isKnownId('anthropic', 'claude-nonexistent'), false);
  assert.deepEqual(knownIdsForProvider('made-up-provider'), []);
  assert.equal(isKnownId('made-up-provider', 'anything'), false);
});

// ── Object.hasOwn guard: a prototype-chain lookup must never leak a builtin (MINOR §33/34) ─

test('knownIdsForProvider/isKnownId never fall through to Object.prototype members for provider names like "constructor"/"toString"/"__proto__"', () => {
  for (const trap of ['constructor', 'toString', '__proto__', 'hasOwnProperty', 'valueOf']) {
    assert.deepEqual(knownIdsForProvider(trap), [], `knownIdsForProvider(${JSON.stringify(trap)}) leaked a prototype member`);
    assert.equal(isKnownId(trap, 'x'), false, `isKnownId(${JSON.stringify(trap)}, 'x') did not return false`);
  }
});

test('knownIdsForProvider("openai") is strictEqual to the real catalog array reference (proves the guard did not break the normal path)', () => {
  assert.equal(knownIdsForProvider('openai'), KNOWN_IDS.openai);
});

// ── LINT_PATTERNS (C1): counted, not just "non-empty" ───────────────────────

test('LINT_PATTERNS has exactly 11 catalog patterns + 4 vendor-prefix patterns + 1 alias pattern = 16, with NO g/y flags (which would carry lastIndex state across calls)', () => {
  assert.equal(LINT_PATTERNS.length, 16);
  for (const pattern of LINT_PATTERNS) {
    assert.equal(pattern.global, false, `pattern ${pattern} must not have the 'g' flag`);
    assert.equal(pattern.sticky, false, `pattern ${pattern} must not have the 'y' flag`);
  }
});

/**
 * @param {string} text
 * @returns {number[]} the INDICES into LINT_PATTERNS of every pattern that matches `text`.
 */
function matchingPatternIndices(text) {
  const indices = [];
  LINT_PATTERNS.forEach((pattern, i) => {
    if (pattern.test(text)) indices.push(i);
  });
  return indices;
}

// ── MAJOR fix: every one of the 11 catalog patterns is individually proven to match ITS OWN id ─

test('every one of the 11 catalog ids is matched by its OWN catalog pattern, and every catalog pattern is reachable by exactly one id', () => {
  // `grok-4.7-build-fast` legitimately ALSO matches the `grok-4.7` pattern — it is a real
  // superstring of that other real catalog id (the `\b...\b`-anchored "grok-4.7" pattern ends on
  // the word boundary right before the following "-build-fast", which is a genuine match, not a
  // regex defect). Every other id matches exactly one catalog pattern.
  const KNOWN_SUBSTRING_OVERLAPS = { 'grok-4.7-build-fast': 2 };
  /** @type {Set<number>} */
  const matchedCatalogPatternIndices = new Set();
  for (const [i, id] of ALL_KNOWN_IDS.entries()) {
    const hits = matchingPatternIndices(id).filter((h) => h < 11); // the first 11 patterns are the catalog-id patterns
    const expectedCount = KNOWN_SUBSTRING_OVERLAPS[id] ?? 1;
    assert.equal(hits.length, expectedCount, `expected ${expectedCount} catalog-pattern hit(s) for "${id}", got indices ${hits.join(',')}`);
    assert.ok(hits.includes(i), `"${id}"'s own pattern (index ${i}) must be among its hits, got ${hits.join(',')}`);
    for (const h of hits) matchedCatalogPatternIndices.add(h);
  }
  assert.equal(matchedCatalogPatternIndices.size, 11, 'every one of the 11 catalog patterns must be reachable by its own id — a duplicate or dead pattern would leave this < 11');
});

// ── MAJOR fix: each of the 4 vendor-prefix patterns is exercised on its own, not only incidentally ─

test('vendor-prefix mutation controls: claude-foo-9, gpt-9x, grok-9, gemini-9 each hit EXACTLY 1 vendor-prefix pattern (indices 11-14), and 0 hits without the digit', () => {
  const cases = [
    { text: 'Using claude-foo-9 as a stand-in id.', expectedIndex: 11 },
    { text: 'Using gpt-9x as a stand-in id.', expectedIndex: 12 },
    { text: 'Using grok-9 as a stand-in id.', expectedIndex: 13 },
    { text: 'Using gemini-9 as a stand-in id.', expectedIndex: 14 },
  ];
  for (const { text, expectedIndex } of cases) {
    const hits = matchingPatternIndices(text);
    assert.deepEqual(hits, [expectedIndex], `expected only vendor-prefix pattern ${expectedIndex} to fire for: ${text}`);
  }
  // The same four prefixes WITHOUT a digit: 0 hits each.
  for (const text of ['Using claude-foo as a stand-in.', 'Using gpt-x as a stand-in.', 'Using grok-pro as a stand-in.', 'Using gemini-pro as a stand-in.']) {
    assert.deepEqual(matchingPatternIndices(text), [], `expected 0 hits without a digit for: ${text}`);
  }
});

test('vendor-prefix patterns do NOT fire on ordinary hyphenated prose lacking a digit (Claude-compatible, GPT-style, grok-cli, claude-code) — 0 hits each', () => {
  for (const prose of ['Claude-compatible tooling.', 'A GPT-style response.', 'Run the grok-cli binary.', 'claude-code is the CLI.']) {
    assert.deepEqual(matchingPatternIndices(prose), [], `expected 0 hits for: ${prose}`);
  }
});

// ── Positive control: prose containing project vocabulary yields 0 hits ────

test('positive control: ordinary prose containing Solo, resolve, console, fable-forge, Jev, System 1 yields 0 hits', () => {
  const prose =
    'Solo spawns a worker; resolve(L2) reads the console; fable-forge is retired; Jev answers ' +
    'System 1 questions; escalation.mjs handles the rest.';
  assert.deepEqual(matchingPatternIndices(prose), [], `expected 0 hits in ordinary prose, got hits at: ${matchingPatternIndices(prose)}`);
});

// ── Mutation controls: exact hit count AND which pattern fired ──────────────

test('mutation control: injecting the exact catalog id claude-opus-5-5 fires EXACTLY its own catalog pattern AND the claude vendor-prefix pattern (index 11) — nothing else', () => {
  const clean = 'The reviewer runs at L2 and the judge runs at L3.';
  const mutated = 'The reviewer runs claude-opus-5-5 at L2 and the judge runs at L3.';
  assert.deepEqual(matchingPatternIndices(clean), []);
  assert.deepEqual(matchingPatternIndices(mutated), [ALL_KNOWN_IDS.indexOf('claude-opus-5-5'), 11]);
});

test('mutation control: injecting "--model opus" fires EXACTLY the alias pattern (index 15), nothing else', () => {
  const clean = 'Pass the model as a role, never a flag value.';
  const mutated = 'Run it with --model opus for the reviewer role.';
  assert.deepEqual(matchingPatternIndices(clean), []);
  assert.deepEqual(matchingPatternIndices(mutated), [15]);
});

test('mutation control: "model: opus" (the YAML-key value-position form) fires EXACTLY the alias pattern (index 15)', () => {
  assert.deepEqual(matchingPatternIndices('model: opus'), [15]);
  assert.deepEqual(matchingPatternIndices('the model of good behavior'), [], 'a bare "model" word with no colon/flag must not fire');
});

test('mutation control: "--model=sonnet" (= separator, no space) fires EXACTLY the alias pattern — the old \\s* form silently let this through unmatched', () => {
  assert.deepEqual(matchingPatternIndices('--model=sonnet'), [15]);
});

test('the alias pattern does NOT fire on the bare alias word alone (only in --model/model: value position)', () => {
  // "opus" appearing as ordinary prose (not after --model / model:) must not be flagged — this is
  // exactly the C1 fix: the OLD bare-token list matched "fable-forge" etc. for the same reason.
  const prose = 'The opus of this project is the code-forge CLI itself.';
  assert.deepEqual(matchingPatternIndices(prose), []);
});

// ── resolveLevel: pure function ──────────────────────────────────────────────

const BASE_CFG = Object.freeze({
  provider: 'anthropic',
  levels: Object.freeze({
    L0: Object.freeze({ model: 'claude-haiku-4-5-20251001' }),
    L1: Object.freeze({ model: 'claude-sonnet-5' }),
    L2: Object.freeze({ model: 'claude-opus-5-5', provider: 'openai', effort: 'high' }),
    L3: Object.freeze({
      model: 'claude-fable-5-1',
      fallback: Object.freeze([Object.freeze({ provider: 'openai', model: 'gpt-6-astra', effort: 'high' })]),
    }),
  }),
});

test('resolveLevel uses the top-level provider when the level has none', () => {
  const resolved = resolveLevel(BASE_CFG, 'L0');
  assert.equal(resolved.provider, 'anthropic');
  assert.equal(resolved.model, 'claude-haiku-4-5-20251001');
  assert.equal(resolved.effort, undefined);
  assert.deepEqual(resolved.fallback, []);
});

test('resolveLevel uses the PER-LEVEL provider override when set (R5), not the top-level one', () => {
  const resolved = resolveLevel(BASE_CFG, 'L2');
  assert.equal(resolved.provider, 'openai');
  assert.equal(resolved.model, 'claude-opus-5-5');
  assert.equal(resolved.effort, 'high');
});

test('resolveLevel carries the fallback list through unchanged', () => {
  const resolved = resolveLevel(BASE_CFG, 'L3');
  assert.equal(resolved.fallback.length, 1);
  assert.equal(resolved.fallback[0].provider, 'openai');
  assert.equal(resolved.fallback[0].model, 'gpt-6-astra');
});

test('resolveLevel throws for a level with no model, naming the level', () => {
  assert.throws(() => resolveLevel({ provider: 'anthropic', levels: {} }, 'L1'), /levels\.L1/);
});

test('resolveLevel throws when NEITHER the level nor the top level has a provider (MINOR §39/40 fix)', () => {
  const cfg = { levels: { L1: { model: 'claude-sonnet-5' } } }; // no cfg.provider, no level.provider
  assert.throws(() => resolveLevel(cfg, 'L1'), /levels\.L1 has no provider/);
});

test('resolveLevel is pure: calling it twice on the same input returns equal (not identical) results and never mutates the input', () => {
  const before = JSON.stringify(BASE_CFG);
  const first = resolveLevel(BASE_CFG, 'L0');
  const second = resolveLevel(BASE_CFG, 'L0');
  assert.deepEqual(first, second);
  assert.notEqual(first, second);
  assert.equal(JSON.stringify(BASE_CFG), before, 'resolveLevel must not mutate its input');
});
