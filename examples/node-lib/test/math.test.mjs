import assert from 'node:assert/strict';
import { test } from 'node:test';
import { add, mean, sub } from '../src/math.mjs';

test('add and sub', () => {
  assert.equal(add(2, 3), 5);
  assert.equal(sub(2, 3), -1);
});

test('mean of three values, and of none', () => {
  assert.equal(mean([1, 2, 6]), 3);
  assert.throws(() => mean([]), RangeError);
});
