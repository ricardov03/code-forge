import assert from 'node:assert/strict';
import { test } from 'node:test';
// A namespace import and a `typeof` assertion first in every case: at the base version `ns.clamp`
// is undefined, so the red step fails on an assertion (RED), never on an import error or a
// TypeError (RED_INVALID).
import * as ns from '../src/math.mjs';

test('clamp returns the value inside the range', () => {
  assert.strictEqual(typeof ns.clamp, 'function');
  assert.strictEqual(ns.clamp(5, 0, 10), 5);
});

test('clamp returns min below the range', () => {
  assert.strictEqual(typeof ns.clamp, 'function');
  assert.strictEqual(ns.clamp(-3, 0, 10), 0);
});

test('clamp returns max above the range', () => {
  assert.strictEqual(typeof ns.clamp, 'function');
  assert.strictEqual(ns.clamp(42, 0, 10), 10);
});

test('clamp refuses min above max', () => {
  assert.strictEqual(typeof ns.clamp, 'function');
  assert.throws(() => ns.clamp(1, 10, 0), RangeError);
});
