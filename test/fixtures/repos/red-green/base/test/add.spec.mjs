import assert from 'node:assert/strict';
import { test } from 'node:test';
import { add } from '../src/math.mjs';

test('add pins existing behaviour', () => {
  assert.equal(add(2, 3), 5);
  assert.equal(add(-1, 1), 0);
});
