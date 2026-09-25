import assert from 'node:assert/strict';
import { test } from 'node:test';
import { CURRENT_VERSION, migrateConfig } from '../../src/config/migrate.mjs';

test('CURRENT_VERSION is 1 (the only version ever shipped)', () => {
  assert.equal(CURRENT_VERSION, 1);
});

test('a config missing "version" entirely is filled in as version 1 — EXACT result, no stray keys', () => {
  const result = migrateConfig({ provider: 'anthropic' });
  assert.deepEqual(result, { version: 1, provider: 'anthropic' });
});

test('a config with version: 1 passes through unchanged (other keys untouched)', () => {
  const input = { version: 1, provider: 'openai', levels: { L0: { model: 'gpt-6-luna' } } };
  const result = migrateConfig(input);
  assert.deepEqual(result, input);
});

test('migrateConfig does not mutate its input', () => {
  const input = { provider: 'anthropic' };
  const before = JSON.stringify(input);
  migrateConfig(input);
  assert.equal(JSON.stringify(input), before);
});

// ── Version refusal — exact message, word-bounded (MINOR §123/124) ─────────

test('a version newer than CURRENT_VERSION throws naming the GIVEN version (99) and CURRENT_VERSION (1), word-bounded so it cannot match by coincidence (e.g. inside "L1")', () => {
  assert.throws(() => migrateConfig({ version: 99 }), (/** @type {any} */ err) => {
    assert.match(err.message, /\b99\b/, `message must name the given version 99: ${err.message}`);
    assert.match(err.message, /\b1\b/, `message must name CURRENT_VERSION 1: ${err.message}`);
    assert.match(err.message, /newer/);
    return true;
  });
});

test('a version newer than CURRENT_VERSION throws Error (not TypeError — that is reserved for "not a mapping")', () => {
  assert.throws(() => migrateConfig({ version: 2 }), (/** @type {any} */ err) => {
    assert.equal(err.name, 'Error');
    return true;
  });
});

// ── Invalid version values (MINOR §125/126, §127/128) ───────────────────────

// NOTE: `version: null` is deliberately NOT in this list — `mapping.version ?? 1` treats an
// explicit `null` exactly like a MISSING key (both are "nullish"), so it resolves to version 1
// rather than throwing; that is intentional pass-through behavior, not an invalid-version case.
test('migrateConfig({version: null}) is treated the same as a MISSING version — resolves to 1, does not throw', () => {
  assert.deepEqual(migrateConfig({ version: null, provider: 'anthropic' }), { version: 1, provider: 'anthropic' });
});

// migrateConfig throws a plain Error (not TypeError — that's reserved for "not a mapping" at all,
// checked separately below) for an out-of-range/non-integer version.
for (const [label, badVersion] of [
  ['0', 0],
  ['-1', -1],
  ['1.5', 1.5],
  ['"not-a-number"', 'not-a-number'],
  ['NaN', NaN],
]) {
  test(`migrateConfig({version: ${label}}) throws "positive integer", plain Error`, () => {
    assert.throws(() => migrateConfig({ version: badVersion }), (/** @type {any} */ err) => {
      assert.equal(err.name, 'Error');
      assert.match(err.message, /positive integer/);
      return true;
    });
  });
}

// ── Not-a-mapping guard, with a descriptive "kind" per bad input (MINOR §45/46) ─

test('migrateConfig(null) throws TypeError naming "null" as the kind (not "object", which typeof null returns)', () => {
  assert.throws(() => migrateConfig(null), (/** @type {any} */ err) => {
    assert.equal(err.name, 'TypeError');
    assert.match(err.message, /got null\b/, `expected "got null", not the misleading "got object": ${err.message}`);
    return true;
  });
});

test('migrateConfig([]) throws TypeError naming "array" as the kind (not "object", which typeof [] returns)', () => {
  assert.throws(() => migrateConfig([]), (/** @type {any} */ err) => {
    assert.equal(err.name, 'TypeError');
    assert.match(err.message, /got array\b/, `expected "got array", not the misleading "got object": ${err.message}`);
    return true;
  });
});

for (const bad of [undefined, 'a string', 42]) {
  test(`migrateConfig(${JSON.stringify(bad)}) throws TypeError naming "${typeof bad}" as the kind`, () => {
    assert.throws(() => migrateConfig(bad), (/** @type {any} */ err) => {
      assert.equal(err.name, 'TypeError');
      assert.match(err.message, new RegExp(`got ${typeof bad}\\b`));
      return true;
    });
  });
}
