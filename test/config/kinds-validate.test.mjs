import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { PROVIDER_DEFAULTS } from '../../src/config/defaults/index.mjs';
import { validateConfig } from '../../src/config/validate.mjs';

const command = [process.execPath, '-e', 'void 0'];
const config = (gates) => ({ version: 1, provider: 'anthropic', levels: PROVIDER_DEFAULTS.anthropic.levels, gates });

test('kind gate validation refuses unknown names and null fixed gates with the kind and name', () => {
  /** @type {Array<[string, string, Record<string, any>]>} */
  const cases = [
    ['docs', 'missing', {}],
    ['code', 'types', { types: null }],
    ['contract', 'build', { custom: [{ name: 'links', argv: command }] }],
  ];
  for (const [kind, name, gates] of cases) {
    const result = validateConfig(config({ ...gates, kinds: { [kind]: [name] } }));
    assert.equal(result.valid, false, `${kind}.${name} must be refused`);
    const errors = result.errors.filter(({ rule }) => rule === 'gates-not-argv-array');
    assert.equal(errors.length, 1, `${kind}.${name} must produce exactly one domain error`);
    assert.match(errors[0].message, new RegExp(`gates\\.kinds\\.${kind}.*${name}`));
  }
});

test('kind gate validation accepts fixed, custom, omitted and empty sets and rejects invalid shapes', () => {
  const gates = { types: command, custom: [{ name: 'build', argv: command }] };
  for (const kinds of [undefined, {}, { docs: [] }, { code: ['test', 'lint', 'format'], docs: ['types'], contract: ['build'] }]) {
    const result = validateConfig(config({ ...gates, ...(kinds === undefined ? {} : { kinds }) }));
    assert.deepEqual(result.errors, [], `valid kind sets ${JSON.stringify(kinds)}`);
    assert.equal(result.valid, true);
  }
  for (const kinds of [null, [], { light: [] }, { docs: 'types' }, { docs: [3] }]) {
    const result = validateConfig(config({ ...gates, kinds }));
    assert.equal(result.valid, false, `invalid kind sets ${JSON.stringify(kinds)}`);
    assert.equal(result.errors.filter(({ rule }) => rule === 'schema').length, 1);
  }
});

test('unknown config keys preserve the old message and explain upgrading with the running version', () => {
  const version = JSON.parse(readFileSync(new URL('../../package.json', import.meta.url), 'utf8')).version;
  const result = validateConfig({ ...config({}), future_option: true });
  assert.equal(result.valid, false);
  assert.equal(result.errors.length, 1, 'one unknown key produces exactly one error');
  assert.equal(result.errors[0].message,
    `(root) must NOT have additional property "future_option" (unknown key; if it comes from a newer code-forge, upgrade: this is code-forge ${version})`);
});
