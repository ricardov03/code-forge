import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { validateConfig } from '../../src/config/validate.mjs';

const command = [process.execPath, '-e', 'void 0'];
const entry = { name: 'build', argv: command };
const config = (custom) => ({ version: 1, project: { slug: 'custom-test' }, provider: 'anthropic', levels: Object.fromEntries(['L0', 'L1', 'L2', 'L3'].map((level) => [level, { model: 'claude-sonnet-4-5' }])), gates: { custom } });
const gateErrors = (custom) => validateConfig(config(custom)).errors.filter((error) => error.rule === 'gates-not-argv-array');

test('custom config refuses invalid entries with an entry-specific error', () => {
  const cases = [
    [null, 'gates.custom'],
    [{}, 'gates.custom'],
    [[null], 'custom[0]'],
    [[{ ...entry, name: 'Build' }], 'Build'],
    [[{ ...entry, name: 'a'.repeat(33) }], 'a'.repeat(33)],
    ...['', '1build', 'build.dot', 'test', 'lint', 'types', 'format'].map((name) => [[{ ...entry, name }], 'custom[0]']),
    [[entry, entry], 'custom[1]'],
    [[{ ...entry, argv: [] }], 'build'],
    [[{ ...entry, argv: ['', 'arg'] }], 'build'],
    [[{ ...entry, argv: ['node', 3] }], 'build'],
    [[{ ...entry, argv: 'node' }], 'build'],
    ...[null, [], { '1BAD': 'x' }, { GOOD: 1 }].map((env) => [[{ ...entry, env }], 'build']),
    ...['{test_files}', '{changed_files}'].flatMap((token) => [token, `prefix=${token}`].map((arg) => [[{ ...entry, argv: ['node', arg] }], token])),
    ...['', '/outside', '../outside', 'reports/../outside', 'C:\\outside', '\\outside'].map((pattern) => [[{ ...entry, evidence: [pattern] }], 'build']),
    ...['report.json', [3]].map((evidence) => [[{ ...entry, evidence }], 'build']),
    [Array.from({ length: 21 }, (_, i) => ({ ...entry, name: `gate${i}` })), 'gates.custom'],
  ];
  for (const [custom, label] of cases) {
    const errors = gateErrors(custom);
    assert.ok(errors.length > 0, `refuse ${JSON.stringify(custom)} with a gates-not-argv-array error`);
    assert.ok(errors.some((error) => error.message.includes(String(label))), `error must name ${label}: ${JSON.stringify(errors)}`);
  }
});

test('custom config accepts twenty named object gates and safe evidence globs', () => {
  const custom = Array.from({ length: 20 }, (_, i) => ({
    name: i === 0 ? `a${'b'.repeat(31)}` : `gate_${i}-web`, argv: command,
    env: { MODE: 'web', _COUNT2: '' }, evidence: ['report.json', 'reports/*.json', 'shots/**/*.png'],
  }));
  assert.deepEqual(gateErrors(custom), [], 'all twenty valid entries pass the domain rule');
  const schemaErrors = validateConfig(config(custom)).errors.filter((error) => error.rule === 'schema');
  assert.deepEqual(schemaErrors, [], 'custom gates are accepted by the schema');
  assert.deepEqual(gateErrors([]), [], 'an empty custom list is valid');
});
