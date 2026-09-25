import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PROVIDER_DEFAULTS } from '../../src/config/defaults/index.mjs';
import { DOMAIN_RULE_IDS, validateConfig } from '../../src/config/validate.mjs';
import { SECRET_LEAK_CASES } from './secret-leak-cases.mjs';

/**
 * A fresh, minimal, fully-valid config — every one of the 14 tests below mutates a clone of this.
 * @returns {Record<string, any>} loosely typed so a test can freely bolt on any other §1.3 key.
 */
function validCfg() {
  return structuredClone({
    version: 1,
    provider: 'anthropic',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' },
      L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' },
      L3: { model: 'claude-fable-5-1' },
    },
  });
}

/**
 * @param {ReturnType<typeof validateConfig>} result
 * @param {string} ruleId
 * @returns {Array<{rule: string, severity: string, message: string, path?: string, keyword?: string}>} every issue (error OR
 *   warning) tagged with `ruleId` — never just "does one exist", so a caller can assert an exact
 *   count and read each message.
 */
function issuesOf(result, ruleId) {
  return [...result.errors, ...result.warnings].filter((issue) => issue.rule === ruleId);
}

// ── Control: the base fixture is valid with 0 errors and 0 warnings ────────

test('control: validCfg() is valid with 0 errors and 0 warnings', () => {
  const result = validateConfig(validCfg());
  assert.equal(result.valid, true);
  assert.deepEqual(result.errors, []);
  assert.deepEqual(result.warnings, []);
});

// ── Acceptance: three provider defaults validate (MAJOR fix: count, not a vacuous loop) ─

test('PROVIDER_DEFAULTS has EXACTLY the three providers, and each one validates with 0 errors', () => {
  assert.deepEqual(Object.keys(PROVIDER_DEFAULTS).sort(), ['anthropic', 'openai', 'xai']);
  for (const [provider, defaults] of Object.entries(PROVIDER_DEFAULTS)) {
    const cfg = { version: 1, provider, levels: defaults.levels };
    const result = validateConfig(cfg);
    assert.equal(result.errors.length, 0, `${provider} defaults produced errors: ${JSON.stringify(result.errors)}`);
    assert.equal(result.valid, true, `${provider} defaults failed: ${JSON.stringify(result.errors)}`);
  }
});

// ── 1. level-missing (ERROR) ─────────────────────────────────────────────────

test('rule 1 level-missing: deleting levels.L3 produces EXACTLY 1 level-missing error naming L3 (the schema layer ALSO reports its own "required property" error — both are expected, independently)', () => {
  const cfg = validCfg();
  delete cfg.levels.L3;
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  const hits = issuesOf(result, 'level-missing');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /\bL3\b/);
  assert.equal(issuesOf(result, 'schema').length, 1, 'the ajv layer independently flags the same missing required property');
  assert.equal(result.errors.length, 2, 'exactly these two, no other domain rule noise');
});

test('rule 1 level-missing: a level WITH a model but no resolvable provider gets the "has no provider" message, not "missing or has no model"', () => {
  const cfg = validCfg();
  delete cfg.provider;
  cfg.levels.L0.provider = 'anthropic';
  cfg.levels.L1.provider = 'anthropic';
  cfg.levels.L3.provider = 'anthropic';
  const result = validateConfig(cfg);
  const hits = issuesOf(result, 'level-missing');
  assert.deepEqual(
    hits.map((h) => h.message),
    ['levels.L2 has no provider (neither levels.L2.provider nor the top-level provider is set)'],
  );
});

test('rule 1 level-missing: deleting TWO levels produces EXACTLY 2 level-missing errors, one per level', () => {
  const cfg = validCfg();
  delete cfg.levels.L1;
  delete cfg.levels.L2;
  const result = validateConfig(cfg);
  const hits = issuesOf(result, 'level-missing');
  assert.equal(hits.length, 2);
  assert.deepEqual(
    hits.map((h) => (h.message.includes('L1') ? 'L1' : h.message.includes('L2') ? 'L2' : '?')).sort(),
    ['L1', 'L2'],
  );
});

// ── 2. unknown-model-id (WARN) ───────────────────────────────────────────────

test('rule 2 unknown-model-id: an id outside the catalog produces EXACTLY 1 warning naming the PATH levels.L1.model — never the id itself — and 0 errors', () => {
  const cfg = validCfg();
  cfg.levels.L1.model = 'claude-totally-made-up';
  const result = validateConfig(cfg);
  assert.equal(result.valid, true, 'an unknown id alone must not fail validation');
  assert.equal(result.errors.length, 0);
  const hits = issuesOf(result, 'unknown-model-id');
  assert.deepEqual(
    hits.map((h) => h.message),
    ['levels.L1.model is not a known "anthropic" id, not in known_extra, and not seen-in-cache'],
  );
  assert.equal(hits[0].message.includes('claude-totally-made-up'), false, 'the value must never be echoed');
});

test('rule 2 is suppressed by known_extra — 0 unknown-model-id hits', () => {
  const cfg = validCfg();
  cfg.levels.L1.model = 'claude-totally-made-up';
  cfg.known_extra = { anthropic: ['claude-totally-made-up'] };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'unknown-model-id').length, 0);
});

test('rule 2 is suppressed by seenInCache (C15) — 0 unknown-model-id hits', () => {
  const cfg = validCfg();
  cfg.levels.L1.model = 'claude-totally-made-up';
  const result = validateConfig(cfg, { seenInCache: { anthropic: ['claude-totally-made-up'] } });
  assert.equal(issuesOf(result, 'unknown-model-id').length, 0);
});

test('rule 2 with a malformed known_extra[provider] (a number) does not crash, still reports EXACTLY 1 unknown-model-id hit, and the schema reports EXACTLY 1 error at /known_extra/anthropic', () => {
  const cfg = validCfg();
  cfg.levels.L1.model = 'claude-totally-made-up';
  cfg.known_extra = { anthropic: 42 };
  /** @type {ReturnType<typeof validateConfig> | undefined} */
  let result;
  assert.doesNotThrow(() => {
    result = validateConfig(cfg);
  });
  assert.ok(result);
  // Garbage can't suppress the warning, so the genuinely-unknown id still warns exactly once.
  assert.equal(issuesOf(result, 'unknown-model-id').length, 1);
  const schemaErrors = issuesOf(result, 'schema');
  assert.deepEqual(
    schemaErrors.map((e) => [e.path, e.keyword]),
    [['/known_extra/anthropic', 'type']],
  );
});

// ── 3. duplicate-level-tuple (ERROR) ─────────────────────────────────────────

test('rule 3 duplicate-level-tuple: L0 and L1 identical produces EXACTLY 1 error naming both L0 and L1', () => {
  const cfg = validCfg();
  cfg.levels.L1 = { ...cfg.levels.L0 };
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  const hits = issuesOf(result, 'duplicate-level-tuple');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /\bL0\b/);
  assert.match(hits[0].message, /\bL1\b/);
});

test('rule 3 duplicate-level-tuple: three-way duplicate (L0=L1=L2) produces EXACTLY 2 pairwise errors, not 3 or 1', () => {
  const cfg = validCfg();
  cfg.levels.L1 = { ...cfg.levels.L0 };
  cfg.levels.L2 = { ...cfg.levels.L0 };
  const result = validateConfig(cfg);
  // seenTuples records L0 first; L1 collides with L0 (1), L2 collides with L0 again (2) — L1/L2
  // are never compared to EACH OTHER once L0 already claimed the tuple.
  assert.deepEqual(
    issuesOf(result, 'duplicate-level-tuple').map((h) => h.message),
    [
      'levels.L0 and levels.L1 resolve to the same (provider, model, effort)',
      'levels.L0 and levels.L2 resolve to the same (provider, model, effort)',
    ],
  );
});

test('rule 3 does NOT fire when two levels share a model but differ by effort', () => {
  const cfg = validCfg();
  cfg.provider = 'xai';
  cfg.levels = {
    L0: { model: 'grok-4.7', effort: 'low' },
    L1: { model: 'grok-4.7', effort: 'medium' },
    L2: { model: 'grok-4.7', effort: 'high' },
    L3: { model: 'grok-4.7', effort: 'xhigh' },
  };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'duplicate-level-tuple').length, 0);
});

// ── 4. stop-at-not-l3 (ERROR) ────────────────────────────────────────────────

test('rule 4 stop-at-not-l3: escalation.stop_at "L2" produces EXACTLY 1 error with the exact path-only message', () => {
  const cfg = validCfg();
  cfg.escalation = { stop_at: 'L2' };
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  assert.deepEqual(
    issuesOf(result, 'stop-at-not-l3').map((h) => h.message),
    ['escalation.stop_at must be "L3"'],
  );
});

test('rule 4 does NOT fire for escalation.stop_at "L3" — 0 hits, config stays valid', () => {
  const cfg = validCfg();
  cfg.escalation = { stop_at: 'L3' };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'stop-at-not-l3').length, 0);
  assert.equal(result.valid, true);
});

// ── 5. multimodel-missing-second-provider (ERROR) ────────────────────────────

test('rule 5 multimodel-missing-second-provider: multimodel true with no second_provider produces EXACTLY 1 error', () => {
  const cfg = validCfg();
  // L3's provider is deliberately set apart from L2's so this fixture does not ALSO trip rule 7
  // (judge-family-collision fires whenever judge/L3 shares a provider with reviewer1/L2 in
  // consensus mode — orthogonal to this test, which is specifically about the empty second_provider).
  cfg.levels.L3.provider = 'xai';
  cfg.review = { multimodel: true };
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  assert.equal(issuesOf(result, 'multimodel-missing-second-provider').length, 1);
  assert.deepEqual(result.errors.map((e) => e.rule), ['multimodel-missing-second-provider']);
});

test('rule 5 does NOT fire when multimodel is true AND second_provider is set — 0 hits', () => {
  const cfg = validCfg();
  cfg.levels.L3.provider = 'xai';
  cfg.review = { multimodel: true, second_provider: 'openai' };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'multimodel-missing-second-provider').length, 0);
});

// ── 6. multimodel-effective-l2-collision (ERROR, "effective-L2 consensus rule") ─

test('rule 6 multimodel-effective-l2-collision: second_provider equal to the effective L2 provider produces EXACTLY 1 error naming that provider', () => {
  const cfg = validCfg(); // effective L2 provider is anthropic (top-level provider, no override)
  cfg.review = { multimodel: true, second_provider: 'anthropic' };
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  const hits = issuesOf(result, 'multimodel-effective-l2-collision');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /anthropic/);
});

test('rule 6 does not fire when second_provider genuinely differs from the effective L2 provider — 0 hits', () => {
  const cfg = validCfg();
  cfg.review = { multimodel: true, second_provider: 'openai' };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'multimodel-effective-l2-collision').length, 0);
});

test('rule 6 fires off second_levels.L2.provider specifically, even when second_provider differs — EXACTLY 1 error', () => {
  const cfg = validCfg();
  cfg.review = {
    multimodel: true,
    second_provider: 'openai', // differs from effective L2 (anthropic) — would NOT collide alone
    second_levels: { L2: { model: 'claude-opus-5-5', provider: 'anthropic' } }, // but this DOES
  };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'multimodel-effective-l2-collision').length, 1);
});

// ── 7. judge-family-collision (ERROR, consensus mode only) — fires INDEPENDENTLY of rule 6 ─

test('rule 7 judge-family-collision: judge (L3, anthropic) and reviewer1 (L2, anthropic) collide — EXACTLY 1 error, and rule 6 does NOT fire (second_provider is a genuinely different provider)', () => {
  const cfg = validCfg(); // L3 provider = anthropic (top-level), L2 provider = anthropic
  cfg.review = { multimodel: true, second_provider: 'openai' }; // differs from L2 -> rule 6 silent
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  assert.equal(issuesOf(result, 'judge-family-collision').length, 1, 'rule 7 must fire on its own');
  assert.equal(issuesOf(result, 'multimodel-effective-l2-collision').length, 0, 'rule 6 must NOT fire here — proves rule 7 is independent');
});

test('rule 7 judge-family-collision: judge collides with reviewer2 (second_levels.L2) instead of reviewer1 — still EXACTLY 1 error', () => {
  const cfg = validCfg(); // judge (L3) = anthropic
  cfg.levels.L2.provider = 'openai'; // reviewer1 now openai, differs from judge (anthropic)
  cfg.review = {
    multimodel: true,
    second_provider: 'openai',
    second_levels: { L2: { model: 'claude-opus-5-5', provider: 'anthropic' } }, // reviewer2 = anthropic = judge
  };
  const result = validateConfig(cfg);
  const hits = issuesOf(result, 'judge-family-collision');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /reviewer 2/);
  // reviewer1 (openai) and reviewer2 (anthropic) differ, so rule 6 is silent — rule 7 fired alone.
  assert.equal(issuesOf(result, 'multimodel-effective-l2-collision').length, 0);
});

test('rule 7 does not fire outside consensus mode (multimodel off) — 0 hits', () => {
  const cfg = validCfg();
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'judge-family-collision').length, 0);
});

test('rule 7 does not fire in consensus mode when the judge (L3) has a genuinely different provider than both reviewers', () => {
  const cfg = validCfg();
  cfg.levels.L3.provider = 'xai'; // judge now xai; L2 stays anthropic
  cfg.review = { multimodel: true, second_provider: 'openai' };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'judge-family-collision').length, 0);
});

// ── 8. orchestrator-l3-multimodel-off (WARN) ─────────────────────────────────

test('rule 8 orchestrator-l3-multimodel-off: orchestrator L3 with multimodel off produces EXACTLY 1 warning, 0 errors', () => {
  const cfg = validCfg();
  cfg.orchestrator = 'L3';
  const result = validateConfig(cfg);
  assert.equal(result.valid, true);
  assert.equal(result.errors.length, 0);
  assert.equal(issuesOf(result, 'orchestrator-l3-multimodel-off').length, 1);
});

test('rule 8 does not fire when orchestrator is L3 AND multimodel is on', () => {
  const cfg = validCfg();
  cfg.orchestrator = 'L3';
  cfg.review = { multimodel: true, second_provider: 'openai' };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'orchestrator-l3-multimodel-off').length, 0);
});

// ── 9. gates-not-argv-array (ERROR) ──────────────────────────────────────────

test('rule 9 gates-not-argv-array: gates.test as a string produces EXACTLY 1 error naming "gates.test"', () => {
  const cfg = validCfg();
  cfg.gates = { test: 'node --test' }; // a string, not an argv array
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  const hits = issuesOf(result, 'gates-not-argv-array');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /gates\.test\b/);
});

test('rule 9 gates-not-argv-array: TWO bad gate keys produce EXACTLY 2 errors, each naming its own key', () => {
  const cfg = validCfg();
  cfg.gates = { test: 'x', lint: [] }; // string AND an empty array — both now invalid
  const result = validateConfig(cfg);
  const hits = issuesOf(result, 'gates-not-argv-array');
  assert.equal(hits.length, 2);
  assert.deepEqual(hits.map((h) => (h.message.includes('gates.test') ? 'test' : h.message.includes('gates.lint') ? 'lint' : '?')).sort(), ['lint', 'test']);
});

test('rule 9 allows gates.test: null (the documented "argv array or null") — 0 hits', () => {
  const cfg = validCfg();
  cfg.gates = { test: null };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'gates-not-argv-array').length, 0);
});

test('rule 9 refuses an EMPTY argv array ([]) — a gate that would run nothing is not valid, distinct from null', () => {
  const cfg = validCfg();
  cfg.gates = { lint: [] };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'gates-not-argv-array').length, 1);
});

test('schema: gates.test: [] is refused by the schema too ($defs.argvOrNull minItems), at exactly /gates/test', () => {
  const cfg = validCfg();
  cfg.gates = { test: [] };
  const schemaErrors = issuesOf(validateConfig(cfg), 'schema');
  assert.deepEqual(
    schemaErrors.map((e) => [e.path, e.keyword]),
    [
      ['/gates/test', 'minItems'],
      ['/gates/test', 'type'],
      ['/gates/test', 'anyOf'],
    ],
  );
});

test('schema: gates.test: [""] (an empty command) is refused by the schema at /gates/test/0 (minLength)', () => {
  const cfg = validCfg();
  cfg.gates = { test: [''] };
  const schemaErrors = issuesOf(validateConfig(cfg), 'schema');
  assert.deepEqual(
    schemaErrors.filter((e) => e.keyword === 'minLength').map((e) => e.path),
    ['/gates/test/0'],
  );
});

// ── 10. secret-looking-value (ERROR, names the key) ──────────────────────────

test('rule 10 secret-looking-value: a value shaped like a real secret produces EXACTLY 1 error naming the exact key path, and the value appears nowhere in the result', () => {
  const secret = 'sk-fake-1234567890abcdef1234567890abcdef';
  const cfg = validCfg();
  cfg.project = { name: secret };
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  const hits = issuesOf(result, 'secret-looking-value');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /^project\.name\b/);
  assert.equal(hits[0].message.includes(secret), false);
  assert.equal(JSON.stringify(result).includes(secret), false);
});

test('rule 10 finds a secret nested TWO levels deep, still EXACTLY 1 hit naming the full path, value never echoed', () => {
  const secret = 'xoxb-fake1234567890-abcdefghijklmnop';
  const cfg = validCfg();
  cfg.system1 = { key: secret };
  const result = validateConfig(cfg);
  const hits = issuesOf(result, 'secret-looking-value');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /^system1\.key\b/);
  assert.equal(hits[0].message.includes(secret), false);
  assert.equal(JSON.stringify(result).includes(secret), false);
});

test('rule 10: a secret pasted into levels.L0.model is refused EXACTLY once naming levels.L0.model, and NO message (error or warning) contains it', () => {
  const secret = `sk-${'A'.repeat(40)}`;
  const cfg = validCfg();
  cfg.levels.L0.model = secret;
  const result = validateConfig(cfg);
  assert.deepEqual(
    issuesOf(result, 'secret-looking-value').map((h) => h.message),
    ['levels.L0.model looks like a secret value — config must hold references, never plaintext secrets'],
  );
  // rule 2 fires on the same value (unknown id) — it must name the path, not echo the value.
  assert.equal(issuesOf(result, 'unknown-model-id').length, 1);
  const leaking = [...result.errors, ...result.warnings].filter((i) => i.message.includes(secret));
  assert.deepEqual(leaking, []);
});

test('rule 10 catches an xAI key (xai- prefix): EXACTLY 1 hit naming system1.key', () => {
  const cfg = validCfg();
  cfg.system1 = { key: `xai-${'Z'.repeat(40)}` };
  const hits = issuesOf(validateConfig(cfg), 'secret-looking-value');
  assert.deepEqual(hits.map((h) => h.message.split(' ')[0]), ['system1.key']);
});

test('rule 10 catches a token EMBEDDED in a longer string (a bearer header inside gates.test): EXACTLY 1 hit naming gates.test[2]', () => {
  const cfg = validCfg();
  cfg.gates = { test: ['curl', '-H', `Authorization: Bearer sk-${'Y'.repeat(40)}`] };
  const result = validateConfig(cfg);
  assert.deepEqual(issuesOf(result, 'secret-looking-value').map((h) => h.message.split(' ')[0]), ['gates.test[2]']);
  assert.equal(issuesOf(result, 'gates-not-argv-array').length, 0, 'the argv shape itself is fine');
});

test('rule 10 does not flag prefix lookalikes glued to a preceding word (task-…, disk-…) — 0 hits', () => {
  const cfg = validCfg();
  cfg.project = { name: 'task-abcdefghijklmnop', slug: 'disk-abcdefghijklmnop' };
  assert.equal(issuesOf(validateConfig(cfg), 'secret-looking-value').length, 0);
});

// ── Secret safety across EVERY rule: one planted secret per rule, 0 occurrences anywhere ─

for (const leakCase of SECRET_LEAK_CASES) {
  test(`no-leak [${leakCase.name}]: EXACTLY ${leakCase.count} "${leakCase.rule}" issue(s), ${leakCase.secretHits} secret-looking-value, and the planted secret appears 0 times in any message or in the result`, () => {
    const result = validateConfig(leakCase.build(leakCase.secret), { hasCliOnPath: () => false });
    assert.equal(issuesOf(result, leakCase.rule).length, leakCase.count);
    assert.equal(issuesOf(result, 'secret-looking-value').length, leakCase.secretHits);
    const all = [...result.errors, ...result.warnings];
    assert.deepEqual(all.filter((i) => i.message.includes(leakCase.secret)).map((i) => i.rule), []);
    assert.equal(JSON.stringify(result).split(leakCase.secret).length - 1, 0);
  });
}

test('rule 10 does not flag a keys.<name> reference value (env:/op:// syntax) — 0 hits', () => {
  const cfg = validCfg();
  cfg.keys = { jev: 'op://vault/item/field', github: 'env:GITHUB_TOKEN' };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'secret-looking-value').length, 0);
});

test('rule 10 does not flag an ordinary model id as a secret (false-positive guard) — 0 hits across the whole valid config', () => {
  const result = validateConfig(validCfg());
  assert.equal(issuesOf(result, 'secret-looking-value').length, 0);
});

// ── 11. fallback-unknown-or-cli-absent (WARN, "fallback validation") ────────

test('rule 11: an unknown fallback model id (CLI present) produces EXACTLY 1 warning naming "unknown model id", not "CLI"', () => {
  const cfg = validCfg();
  cfg.levels.L3.fallback = [{ provider: 'anthropic', model: 'claude-does-not-exist' }];
  const result = validateConfig(cfg, { hasCliOnPath: () => true }); // deterministic: CLI branch off
  assert.equal(result.valid, true);
  const hits = issuesOf(result, 'fallback-unknown-or-cli-absent');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /unknown model id/);
  assert.doesNotMatch(hits[0].message, /no CLI on PATH/);
});

test('rule 11: a KNOWN fallback id with its CLI absent produces EXACTLY 1 warning naming "no CLI on PATH", not "unknown"', () => {
  const cfg = validCfg();
  cfg.levels.L3.fallback = [{ provider: 'openai', model: 'gpt-6-astra' }]; // a KNOWN id
  const result = validateConfig(cfg, { hasCliOnPath: () => false });
  const hits = issuesOf(result, 'fallback-unknown-or-cli-absent');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /no CLI on PATH/);
  assert.doesNotMatch(hits[0].message, /unknown model id/);
});

test('rule 11 does not fire for a known fallback id with its CLI present — 0 hits', () => {
  const cfg = validCfg();
  cfg.levels.L3.fallback = [{ provider: 'openai', model: 'gpt-6-astra' }];
  const result = validateConfig(cfg, { hasCliOnPath: () => true });
  assert.equal(issuesOf(result, 'fallback-unknown-or-cli-absent').length, 0);
});

test('rule 11 with TWO fallback entries, only the unknown one warns — EXACTLY 1 warning, indexed fallback[1]', () => {
  const cfg = validCfg();
  cfg.levels.L3.fallback = [
    { provider: 'openai', model: 'gpt-6-astra' }, // known, CLI present -> clean
    { provider: 'openai', model: 'gpt-9-nonexistent' }, // unknown -> warns
  ];
  const result = validateConfig(cfg, { hasCliOnPath: () => true });
  const hits = issuesOf(result, 'fallback-unknown-or-cli-absent');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /fallback\[1\]/);
});

test('rule 11 does NOT crash on a malformed fallback shape, produces 0 rule-11 warnings, and the SCHEMA reports the exact errors under /levels/L3/fallback', () => {
  /** @type {Array<[unknown, Array<[string, string]>]>} */
  const cases = [
    ['not-an-array', [['/levels/L3/fallback', 'type']]],
    [{}, [['/levels/L3/fallback', 'type']]],
    [[null], [['/levels/L3/fallback/0', 'type']]],
    [
      [{ provider: 42, model: null }],
      [
        ['/levels/L3/fallback/0/provider', 'type'],
        ['/levels/L3/fallback/0/provider', 'enum'],
        ['/levels/L3/fallback/0/model', 'type'],
      ],
    ],
  ];
  for (const [malformed, expectedSchemaErrors] of cases) {
    const cfg = validCfg();
    cfg.levels.L3.fallback = malformed;
    /** @type {ReturnType<typeof validateConfig> | undefined} */
    let result;
    assert.doesNotThrow(() => {
      result = validateConfig(cfg, { hasCliOnPath: () => true });
    }, `threw on fallback = ${JSON.stringify(malformed)}`);
    assert.ok(result);
    assert.equal(issuesOf(result, 'fallback-unknown-or-cli-absent').length, 0, `unexpected warning for ${JSON.stringify(malformed)}`);
    assert.equal(result.valid, false);
    assert.deepEqual(
      issuesOf(result, 'schema').map((e) => [e.path, e.keyword]),
      expectedSchemaErrors,
      `schema errors for ${JSON.stringify(malformed)}`,
    );
  }
});

// ── 12. engine-subprocess-no-cli (ERROR) ─────────────────────────────────────

test('rule 12 engine-subprocess-no-cli: no CLI at all produces EXACTLY 1 error naming the single effective provider', () => {
  const cfg = validCfg();
  cfg.engine = 'subprocess';
  const result = validateConfig(cfg, { hasCliOnPath: () => false });
  assert.equal(result.valid, false);
  const hits = issuesOf(result, 'engine-subprocess-no-cli');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /anthropic/);
});

test('rule 12 does not fire when the CLI is present — 0 hits', () => {
  const cfg = validCfg();
  cfg.engine = 'subprocess';
  const result = validateConfig(cfg, { hasCliOnPath: () => true });
  assert.equal(issuesOf(result, 'engine-subprocess-no-cli').length, 0);
});

test('rule 12 does not fire for engine !== "subprocess" even with no CLI detector wired up — 0 hits', () => {
  const cfg = validCfg();
  cfg.engine = 'auto';
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'engine-subprocess-no-cli').length, 0);
});

test('rule 12 checks EVERY distinct effective level provider, not just the top-level one — an L2 override with no CLI is caught', () => {
  const cfg = validCfg();
  cfg.engine = 'subprocess';
  cfg.levels.L2.provider = 'openai'; // per-level override; top-level provider stays anthropic
  const result = validateConfig(cfg, { hasCliOnPath: (p) => p === 'anthropic' }); // only anthropic has a CLI
  const hits = issuesOf(result, 'engine-subprocess-no-cli');
  assert.equal(hits.length, 1);
  assert.match(hits[0].message, /openai/);
  assert.doesNotMatch(hits[0].message, /anthropic/, 'anthropic HAS a CLI — must not be named as missing');
});

test('rule 12 names BOTH missing providers when two distinct levels lack a CLI', () => {
  const cfg = validCfg();
  cfg.engine = 'subprocess';
  cfg.levels.L2.provider = 'openai';
  cfg.levels.L3.provider = 'xai';
  const result = validateConfig(cfg, { hasCliOnPath: () => false });
  const hits = issuesOf(result, 'engine-subprocess-no-cli');
  assert.equal(hits.length, 1, 'one issue naming every missing provider, not one issue per provider');
  assert.match(hits[0].message, /anthropic/);
  assert.match(hits[0].message, /openai/);
  assert.match(hits[0].message, /xai/);
});

// ── 13. proof-tool-absent-for-high-tier (WARN) ───────────────────────────────

test('rule 13 proof-tool-absent-for-high-tier: high.paths set with no tool produces EXACTLY 1 warning', () => {
  const cfg = validCfg();
  cfg.proof = { tiers: { high: { paths: ['src/money/**'] } } };
  const result = validateConfig(cfg);
  assert.equal(result.valid, true);
  assert.equal(issuesOf(result, 'proof-tool-absent-for-high-tier').length, 1);
});

test('rule 13 does not fire once tool is set — 0 hits', () => {
  const cfg = validCfg();
  cfg.proof = { tiers: { high: { paths: ['src/money/**'], tool: 'stryker' } } };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'proof-tool-absent-for-high-tier').length, 0);
});

test('rule 13 does not fire when high.paths is present but EMPTY (nothing configured as high-tier yet)', () => {
  const cfg = validCfg();
  cfg.proof = { tiers: { high: { paths: [] } } };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'proof-tool-absent-for-high-tier').length, 0);
});

// ── 14. shadow-rate-range (ERROR, "shadow_rate range") ───────────────────────

test('rule 14 shadow-rate-range: shadow_rate above 0.5 produces EXACTLY 1 error with the exact path-only message', () => {
  const cfg = validCfg();
  cfg.calibration = { shadow_rate: 0.6 };
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  assert.deepEqual(
    issuesOf(result, 'shadow-rate-range').map((h) => h.message),
    ['calibration.shadow_rate must be a number within [0, 0.5]'],
  );
});

test('rule 14 shadow-rate-range: a negative shadow_rate also produces EXACTLY 1 error', () => {
  const cfg = validCfg();
  cfg.calibration = { shadow_rate: -0.1 };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'shadow-rate-range').length, 1);
});

test('rule 14 refuses NaN (e.g. YAML .nan) — the old rate<0||rate>0.5 form let it through', () => {
  const cfg = validCfg();
  cfg.calibration = { shadow_rate: NaN };
  const result = validateConfig(cfg);
  assert.equal(issuesOf(result, 'shadow-rate-range').length, 1);
});

test('rule 14 allows the boundary values 0 and 0.5 — 0 hits at either edge', () => {
  for (const rate of [0, 0.5]) {
    const cfg = validCfg();
    cfg.calibration = { shadow_rate: rate };
    const result = validateConfig(cfg);
    assert.equal(issuesOf(result, 'shadow-rate-range').length, 0, `rate ${rate} must be within range`);
  }
});

// ── All 14 rule ids, cross-checked against the REAL exported set (MAJOR fix) ─

test('DOMAIN_RULE_IDS (imported from validate.mjs, not re-typed here) has exactly 14 distinct ids', () => {
  assert.equal(DOMAIN_RULE_IDS.length, 14);
  assert.equal(new Set(DOMAIN_RULE_IDS).size, 14, 'no duplicate rule ids');
});

test('every rule id exercised by THIS test file is a member of the real DOMAIN_RULE_IDS set (catches a renamed/dropped rule)', () => {
  const exercisedInThisFile = [
    'level-missing',
    'unknown-model-id',
    'duplicate-level-tuple',
    'stop-at-not-l3',
    'multimodel-missing-second-provider',
    'multimodel-effective-l2-collision',
    'judge-family-collision',
    'orchestrator-l3-multimodel-off',
    'gates-not-argv-array',
    'secret-looking-value',
    'fallback-unknown-or-cli-absent',
    'engine-subprocess-no-cli',
    'proof-tool-absent-for-high-tier',
    'shadow-rate-range',
  ];
  assert.deepEqual([...exercisedInThisFile].sort(), [...DOMAIN_RULE_IDS].sort());
});

// ── Schema-layer errors also surface, and don't crash the domain layer ──────

test('an unknown top-level key is refused by the schema layer, and ONLY the schema layer (0 domain-rule noise)', () => {
  const cfg = validCfg();
  cfg.totally_unknown_key = true;
  const result = validateConfig(cfg);
  assert.equal(result.valid, false);
  assert.deepEqual(result.errors.map((e) => e.rule), ['schema']);
});

for (const badKey of ['l2', 'L4']) {
  test(`review.second_levels with the key "${badKey}" is refused by EXACTLY 1 propertyNames error at /review/second_levels naming "${badKey}", and no domain rule fires`, () => {
    const cfg = validCfg();
    // L3 on its own provider so no consensus-mode domain rule (6/7) can make the config invalid —
    // the ONLY reason it may fail is the schema refusing the key.
    cfg.levels.L3 = { model: 'grok-4.7', provider: 'xai' };
    cfg.review = { multimodel: true, second_provider: 'openai', second_levels: { [badKey]: { model: 'x' } } };
    const result = validateConfig(cfg);
    assert.equal(result.valid, false);
    assert.deepEqual([...new Set(result.errors.map((e) => e.rule))], ['schema'], 'only the schema layer may refuse this config');
    const propertyNameErrors = result.errors.filter((e) => e.keyword === 'propertyNames');
    assert.equal(propertyNameErrors.length, 1);
    assert.equal(propertyNameErrors[0].path, '/review/second_levels');
    assert.equal(
      propertyNameErrors[0].message,
      `/review/second_levels property name "${badKey}" is not allowed (propertyNames)`,
    );
    // The anyOf over [null, map] also reports its own two branch failures — and nothing else.
    assert.deepEqual(result.errors.map((e) => e.keyword), ['type', 'propertyNames', 'anyOf']);
  });
}

test('control: review.second_levels with a VALID key ("L2") passes the schema — proves the propertyNames refusal above is about the key', () => {
  const cfg = validCfg();
  cfg.levels.L3 = { model: 'grok-4.7', provider: 'xai' };
  cfg.review = { multimodel: true, second_provider: 'openai', second_levels: { L2: { model: 'gpt-6-astra', provider: 'openai' } } };
  const result = validateConfig(cfg);
  assert.deepEqual(issuesOf(result, 'schema'), []);
});
