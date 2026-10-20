import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { Ajv2020 } from 'ajv/dist/2020.js';

// A missing feature produces assertion failures during the initial red run.
let schema = false;
try {
  schema = JSON.parse(readFileSync(new URL('../../schema/outcome.schema.json', import.meta.url), 'utf8'));
} catch (err) {
  if (err.code !== 'ENOENT') throw err;
}
const validate = new Ajv2020({ allErrors: true }).compile(schema);
/** @type {any} */
const outcome = await import('../../src/outcome/outcome.mjs').catch((err) => {
  if (err.code !== 'ERR_MODULE_NOT_FOUND') throw err;
  return {};
});

const logo = {
  skill: 'logo-designer', version: '0.1.0', request: 'brand/logo-brief.md',
  status: 'approved', approved_by: 'owner', artifact: 'https://claude.ai/artifact/abc',
  files: [{ path: 'brand/logo/logo.svg', sha256: '8774...' }],
  decisions: ['Round 1: owner picked option A'],
  checks: [{ name: 'logo-check-final', result: 'pass', detail: ['mark 16px spread 0.261'] }],
  cost_usd: null, minutes: null,
};

test('outcome schema accepts the specialist contract and optional fields', () => {
  assert.equal(validate(logo), true, 'the complete logo outcome is valid');
  assert.deepEqual(outcome.validateOutcome?.(logo), { ok: true, problems: [] });
  for (const status of ['draft', 'approved', 'blocked', 'failed']) {
    assert.equal(validate({ skill: 'logo-designer', status, files: [] }), true, `${status} is valid`);
  }
  const nullable = { ...logo, approved_by: null, artifact: null, cost_usd: 0.12, minutes: 5,
    checks: [{ name: 'review', result: 'warn', evidence: 'review.md' }, { name: 'lint', result: 'fail' }] };
  assert.equal(validate(nullable), true, 'nullable metadata and all check results are valid');
});

test('outcome schema refuses missing required fields, invalid enums and unknown keys', () => {
  assert.equal(validate(logo), true, 'the valid control outcome must pass');
  const invalid = [
    ...['skill', 'status', 'files'].map((key) => {
      const value = { ...logo };
      delete value[key];
      return [key, value];
    }),
    ['status', { ...logo, status: 'ready' }],
    ['check result', { ...logo, checks: [{ name: 'review', result: 'skip' }] }],
    ['top-level key', { ...logo, surprise: true }],
    ['file key', { ...logo, files: [{ path: 'logo.svg', sha256: '', surprise: true }] }],
    ['check key', { ...logo, checks: [{ name: 'review', result: 'pass', surprise: true }] }],
    ['file hash', { ...logo, files: [{ path: 'logo.svg' }] }],
    ['detail type', { ...logo, checks: [{ name: 'review', result: 'pass', detail: 'text' }] }],
  ];
  for (const [label, value] of invalid) {
    assert.equal(validate(value), false, `${label} must be refused by the schema`);
    const result = outcome.validateOutcome(value);
    assert.equal(result.ok, false, `${label} must be refused by the shared validator`);
    assert.ok(result.problems.length >= 1, `${label} must identify a problem`);
    assert.ok(result.problems.every((problem) => typeof problem === 'string' && !problem.includes('\n')));
  }
});
