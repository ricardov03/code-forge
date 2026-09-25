// helpers FIRST: its import-time guard moves $HOME and cwd to a temp dir before any src module loads.
import './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { intFlag, parseFlags } from '../../src/state/cli-args.mjs';

const SPEC = { values: ['run', 'level'], booleans: ['reattach'], multi: ['owned'] };

test('a multi-value flag takes an inline value plus every following token, and may repeat', () => {
  assert.deepEqual(parseFlags(['--owned=a', 'b', '--run', 'r1', 'X'], SPEC), { flags: { owned: ['a', 'b'], run: 'r1' }, positionals: ['X'] });
  assert.deepEqual(parseFlags(['--owned=a'], SPEC).flags.owned, ['a']);
  assert.deepEqual(parseFlags(['--owned', 'a', '--owned', 'b'], SPEC).flags.owned, ['a', 'b']);
  assert.throws(() => parseFlags(['--owned', '--run', 'r'], SPEC), { code: 'usage', message: '--owned needs at least one value' });
});

test('usage errors: unknown flag, bare --, repeated single-value flag, boolean with a value, value flag without one', () => {
  for (const [args, message] of [
    [['--ownd', 'a'], 'unknown flag "--ownd"'],
    [['--'], 'unknown flag "--"'],
    [['--run', 'a', '--run', 'b'], '--run given more than once'],
    [['--reattach=false'], '--reattach takes no value'],
    [['--run'], '--run needs a value'],
    [['--run='], '--run needs a value'],
    [['--run', '--level', 'L1'], '--run needs a value'],
  ]) {
    assert.throws(() => parseFlags(/** @type {string[]} */ (args), SPEC), { code: 'usage', message }, JSON.stringify(args));
  }
});

test('intFlag accepts only positive safe integers without sign or leading zeros', () => {
  assert.equal(intFlag('4242', 'n'), 4242);
  assert.equal(intFlag(undefined, 'n'), undefined);
  for (const raw of ['0', '007', '-1', '1.5', '1e3', '', '99999999999999999999']) {
    assert.throws(() => intFlag(raw, 'n'), { code: 'usage', message: '--n must be a positive integer' }, raw);
  }
});
