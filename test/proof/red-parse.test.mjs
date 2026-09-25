import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { parseRed } from '../../src/proof/red-parse.mjs';

// Output shapes captured once from real runs (node:test 24 spec reporter; Pest collision printer),
// paths anonymised. No PHP is run here.
const CAPTURED = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'repos', 'red-green', 'captured');
const captured = (/** @type {string} */ name) => readFileSync(path.join(CAPTURED, name), 'utf8');

/** @param {ReturnType<typeof parseRed>} r */
const shape = (r) => [r.verdict, r.red_kind, r.framework, r.failed];

test('red-parse: a node:test assertion failure is RED (assertion)', () => {
  assert.deepEqual(shape(parseRed({ code: 1, stdout: captured('node-assertion.txt') })), ['RED', 'assertion', 'node:test', 1]);
});

test('red-parse: a Pest assertion failure is RED (assertion)', () => {
  // Pest prints on stdout; the collision printer colours are stripped first.
  const colored = `\x1b[31m${captured('pest-assertion.txt')}\x1b[39m`;
  assert.deepEqual(shape(parseRed({ code: 1, stdout: colored })), ['RED', 'assertion', 'pest', 1]);
});

test('red-parse: a node:test import error is RED_INVALID (import), although the runner prints "test failed"', () => {
  assert.deepEqual(shape(parseRed({ code: 1, stdout: captured('node-import-error.txt') })), ['RED_INVALID', 'import', 'node:test', 1]);
});

test('red-parse boundaries: Pest class-not-found, a pass, a timeout and a failure with no assertion line', () => {
  assert.deepEqual(shape(parseRed({ code: 1, stdout: captured('pest-class-not-found.txt') })), ['RED_INVALID', 'import', 'pest', 1]);
  assert.deepEqual(shape(parseRed({ code: 0, stdout: 'ℹ tests 1\nℹ pass 1\nℹ fail 0\n' })), ['NOT_RED', 'none', 'node:test', 0]);
  // a timed-out run is never RED, even with an assertion line in its output
  assert.deepEqual(shape(parseRed({ code: null, signal: 'SIGTERM', timedOut: true, stdout: 'AssertionError [ERR_ASSERTION]' })), ['RED_INVALID', 'fatal', 'unknown', null]);
  assert.deepEqual(shape(parseRed({ code: 1, stderr: 'TypeError: clamp is not a function\n' })), ['RED_INVALID', 'error', 'unknown', null]);
});
