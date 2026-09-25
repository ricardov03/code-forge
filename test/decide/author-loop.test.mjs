import assert from 'node:assert/strict';
import { test } from 'node:test';
import { blockingQuestions, isAuthorLoopDone, remainingBlockingQuestions } from '../../src/decide/author-loop.mjs';

const BLOCKING = { id: 'q1', question: 'Which provider?', blocking: true };
const NON_BLOCKING = { id: 'q2', question: 'A nice-to-have?', blocking: false };

test('isAuthorLoopDone: empty questions[] ⇒ done', () => {
  assert.equal(isAuthorLoopDone([]), true);
});

test('isAuthorLoopDone: a non-blocking item the human explicitly skipped ⇒ done', () => {
  assert.equal(isAuthorLoopDone([NON_BLOCKING], ['q2']), true);
});

test('isAuthorLoopDone: a non-blocking item present but NOT in the skip set ⇒ NOT done (fix round 1 — an item nobody looked at is not "skipped")', () => {
  assert.equal(isAuthorLoopDone([NON_BLOCKING]), false);
  assert.equal(isAuthorLoopDone([NON_BLOCKING], []), false);
});

test('isAuthorLoopDone: any blocking item in the latest round ⇒ NOT done, full stop', () => {
  assert.equal(isAuthorLoopDone([BLOCKING]), false);
  assert.equal(isAuthorLoopDone([BLOCKING, NON_BLOCKING], ['q2']), false);
});

// Fix round 1 finding "spec deviation": §3.7 gives blocking questions no skip path at all — a
// blocking id sitting in skippedIds must NOT clear it (the old behaviour let it, which stopped
// the loop too early on a re-asked blocking question).
test('fix round 1: a blocking question id in skippedIds does NOT stop the loop', () => {
  assert.equal(isAuthorLoopDone([BLOCKING], ['q1']), false);
  assert.equal(isAuthorLoopDone([BLOCKING], new Set(['q1'])), false);
});

test('fix round 1: all non-blocking AND all skipped ⇒ stops (true); mixed in a bigger set', () => {
  const q3 = { id: 'q3', question: 'Another nice-to-have', blocking: false };
  assert.equal(isAuthorLoopDone([NON_BLOCKING, q3], ['q2', 'q3']), true);
  assert.equal(isAuthorLoopDone([NON_BLOCKING, q3], ['q2']), false); // q3 not skipped yet
});

test('blockingQuestions filters out non-blocking items', () => {
  assert.deepEqual(blockingQuestions([BLOCKING, NON_BLOCKING]), [BLOCKING]);
  assert.deepEqual(blockingQuestions([NON_BLOCKING]), []);
});

test('remainingBlockingQuestions: every blocking question in the round, regardless of any skip set (blocking is never resolved by skipping)', () => {
  const q3 = { id: 'q3', question: 'Second blocker', blocking: true };
  assert.deepEqual(remainingBlockingQuestions([BLOCKING, q3, NON_BLOCKING]), [BLOCKING, q3]);
});

// ── fail-closed shape validation (fix round 1 finding "fails open") ─────────────────────────────

test('fails closed: a missing/malformed "blocking" field throws instead of silently becoming non-blocking', () => {
  assert.throws(() => isAuthorLoopDone([{ id: 'q1', question: 'x' }]), TypeError); // no blocking field
  assert.throws(() => isAuthorLoopDone([{ id: 'q1', question: 'x', blocking: 'true' }]), TypeError); // string, not boolean
  assert.throws(() => blockingQuestions([{ id: 'q1', question: 'x' }]), TypeError);
});

test('fails closed: a missing/empty "id" field throws', () => {
  assert.throws(() => isAuthorLoopDone([{ question: 'x', blocking: true }]), TypeError);
  assert.throws(() => isAuthorLoopDone([{ id: '', question: 'x', blocking: true }]), TypeError);
});

test('rejects a non-array questions argument', () => {
  assert.throws(() => isAuthorLoopDone(null), TypeError);
  assert.throws(() => blockingQuestions('nope'), TypeError);
});
