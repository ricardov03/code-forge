import assert from 'node:assert/strict';
import { test } from 'node:test';
import { money } from '../src/format.mjs';

test('money formats cents', () => {
  assert.equal(money(1050), '$10.50');
});
