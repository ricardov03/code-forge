import assert from 'node:assert/strict';
import { test } from 'node:test';
import schema from '../../schema/code-forge.schema.json' with { type: 'json' };
import { deref, resolveSchemaPath } from '../../src/config/schema-paths.mjs';

// ── Direct unit coverage for the navigator itself (MINOR §71/72) ────────────
// schema-coverage.test.mjs and schema.test.mjs both depend on this module without exercising it
// directly — if it resolved a path wrongly, both of those could stay green for the wrong reason.

test('resolveSchemaPath("") — the empty path — returns the schema root itself', () => {
  assert.equal(resolveSchemaPath(schema, ''), schema);
});

test('resolveSchemaPath returns undefined for a top-level key that does not exist', () => {
  assert.equal(resolveSchemaPath(schema, 'this_key_does_not_exist'), undefined);
});

test('resolveSchemaPath("levels.L0.model") goes through a $ref ($defs/level) to reach model', () => {
  const node = resolveSchemaPath(schema, 'levels.L0.model');
  assert.deepEqual(node, { type: 'string', minLength: 1 });
});

test('resolveSchemaPath("review.second_levels.L2.provider") goes through anyOf, THEN a typed additionalProperties map, THEN a $ref', () => {
  const node = resolveSchemaPath(schema, 'review.second_levels.L2.provider');
  assert.deepEqual(node, schema.$defs.provider);
});

test('resolveSchemaPath("thresholds.some_made_up_question.act") resolves through the OPEN-ENDED map for any key name', () => {
  const node = resolveSchemaPath(schema, 'thresholds.some_made_up_question.act');
  assert.deepEqual(node, { type: 'number', minimum: 0, maximum: 1 });
});

test('resolveSchemaPath stops (returns undefined) one segment past where a fixed-shape object runs out of properties', () => {
  assert.equal(resolveSchemaPath(schema, 'caps.coders.nonexistent'), undefined);
});

test('resolveSchemaPath on a completely empty root schema ({}) returns undefined for any non-empty path', () => {
  assert.equal(resolveSchemaPath({}, 'anything'), undefined);
});

// ── deref() directly ──────────────────────────────────────────────────────

test('deref() returns a plain node (no $ref) unchanged', () => {
  const node = { type: 'string' };
  assert.equal(deref(node, schema), node);
});

test('deref() follows a $ref to its $defs target', () => {
  const node = { $ref: '#/$defs/provider' };
  assert.equal(deref(node, schema), schema.$defs.provider);
});

test('deref() on a $ref pointing nowhere real returns undefined (walks off the end of the root object)', () => {
  const node = { $ref: '#/$defs/does_not_exist' };
  assert.equal(deref(node, schema), undefined);
});
