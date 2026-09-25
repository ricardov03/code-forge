import assert from 'node:assert/strict';
import { test } from 'node:test';
import openai from '../../src/config/defaults/openai.mjs';
import { defaultsForProvider, PROVIDER_DEFAULTS } from '../../src/config/defaults/index.mjs';

test('PROVIDER_DEFAULTS has exactly the 3 providers', () => {
  assert.deepEqual(Object.keys(PROVIDER_DEFAULTS).sort(), ['anthropic', 'openai', 'xai']);
});

test('defaultsForProvider("openai") is strictEqual to the imported openai module object', () => {
  assert.equal(defaultsForProvider('openai'), openai);
});

// ── Object.hasOwn guard: a prototype-chain lookup must never leak a builtin (MINOR §31/32) ─

test('defaultsForProvider never falls through to Object.prototype members for provider names like "constructor"/"__proto__"', () => {
  for (const trap of ['constructor', 'toString', '__proto__', 'hasOwnProperty', 'valueOf']) {
    assert.strictEqual(defaultsForProvider(trap), undefined, `defaultsForProvider(${JSON.stringify(trap)}) leaked a prototype member`);
  }
});

test('defaultsForProvider returns undefined for a genuinely unknown provider string', () => {
  assert.strictEqual(defaultsForProvider('made-up-provider'), undefined);
});

test('defaultsForProvider is defensive against a non-string argument (no throw, returns undefined)', () => {
  for (const bad of [42, null, undefined, {}, []]) {
    assert.doesNotThrow(() => defaultsForProvider(/** @type {any} */ (bad)));
    assert.strictEqual(defaultsForProvider(/** @type {any} */ (bad)), undefined);
  }
});

// ── v1.3 (B1.1, R10): caps.coders defaults to 2 on every provider default ──

test('caps.coders is exactly 2 on all three provider default matrices (R10: at most 2 coders at a time)', () => {
  assert.deepEqual(Object.keys(PROVIDER_DEFAULTS).sort(), ['anthropic', 'openai', 'xai']);
  for (const [provider, defaults] of Object.entries(PROVIDER_DEFAULTS)) {
    assert.equal(defaults.caps?.coders, 2, `${provider} defaults.caps.coders must be 2, got ${JSON.stringify(defaults.caps)}`);
  }
});
