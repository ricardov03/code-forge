import assert from 'node:assert/strict';
import { test } from 'node:test';
import { chunk, unique } from '../src/list.mjs';

test('chunk splits into slices of at most size items', () => {
  assert.deepEqual(chunk([1, 2, 3, 4, 5], 2), [[1, 2], [3, 4], [5]]);
  assert.throws(() => chunk([1], 0), RangeError);
});

test('unique keeps the first occurrence, in order', () => {
  assert.deepEqual(unique([3, 1, 3, 2, 1]), [3, 1, 2]);
});
