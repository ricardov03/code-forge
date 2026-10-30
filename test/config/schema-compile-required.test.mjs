import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Ajv2020 } from 'ajv/dist/2020.js';
import { compileSchema, openAIStrictViolations } from '../../src/config/schema-compile.mjs';
import { FACTS_SESSION_SCHEMA } from '../../src/session/facts.mjs';
import { FINDING_SCHEMA } from '../../src/review/validate-review.mjs';
import { AUTHOR_SCHEMA } from '../../src/session/author.mjs';

test('compiled facts keep six required strings non-nullable, why nullable, and both objects closed and fully required', () => {
  const compiled = compileSchema(FACTS_SESSION_SCHEMA, 'openai', { strict: true });
  const validate = new Ajv2020({ strict: false }).compile(compiled);
  const row = { fact_id: 'F1', claim: './a', kind: 'path', command: 'ls ./a', output_excerpt: './a', tag: 'VERIFIED', why: null };
  assert.equal(validate({ facts: [row] }), true);
  const fields = ['fact_id', 'claim', 'kind', 'command', 'output_excerpt', 'tag'];
  assert.equal(fields.length, 6);
  for (const field of fields) {
    assert.equal(validate({ facts: [{ ...row, [field]: null }] }), false, `${field} rejects null`);
  }
  assert.deepEqual(compiled.properties.facts.items.properties.why, { type: ['string', 'null'] });
  for (const [node, count] of [[compiled, 1], [compiled.properties.facts.items, 7]]) {
    assert.equal(Object.keys(node.properties).length, count);
    assert.deepEqual(node.required, Object.keys(node.properties));
    assert.equal(node.additionalProperties, false);
  }
});

test('only the source-optional property becomes nullable and both properties become required', () => {
  const source = { type: 'object', properties: { required: { type: 'string' }, optional: { type: 'integer' } }, required: ['required'] };
  const before = structuredClone(source);
  const compiled = compileSchema(source, 'openai', { strict: true });
  const validate = new Ajv2020({ strict: false }).compile(compiled);
  assert.equal(validate({ required: null, optional: 1 }), false, 'source-required string rejects null');
  assert.equal(validate({ required: 'yes', optional: null }), true);
  assert.equal(validate({ required: 'yes' }), false, 'source-optional field must still be present');
  assert.equal(validate({ required: 'yes', optional: 'wrong' }), false);
  assert.deepEqual(compiled.required, ['required', 'optional']);
  assert.deepEqual(compiled.properties.optional, { anyOf: [{ type: 'integer' }, { type: 'null' }] });
  assert.deepEqual(source, before);
});

test('facts, review and author compile without strict violations while preserving required field types', () => {
  const schemas = [[FACTS_SESSION_SCHEMA, 'facts'], [FINDING_SCHEMA, 'passed'], [AUTHOR_SCHEMA, 'questions']];
  assert.equal(schemas.length, 3);
  for (const [source, field] of schemas) {
    const compiled = compileSchema(source, 'openai', { strict: true });
    assert.deepEqual(openAIStrictViolations(compiled), [], `${field}: no strict violations`);
    const validate = new Ajv2020({ strict: false }).compile(compiled.properties[field]);
    assert.equal(validate(null), false, `${field}: source-required field rejects null`);
  }
});
