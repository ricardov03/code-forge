import assert from 'node:assert/strict';
import { test } from 'node:test';
import { clamp } from '../src/math.mjs';

test('clamp caps at hi', () => {
  assert.equal(clamp(5, 0, 3), 3);
});
