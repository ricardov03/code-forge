import { answerFor, cfgFor, freshDir, harness } from './helpers.mjs';
import assert from 'node:assert/strict';
import { writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { FINDING_SCHEMA, validateReview } = await import('../../src/review/validate-review.mjs');

const HUNKS = ['@@ -0,0 +1,3 @@'];

/**
 * One L2 reviewer session on the fake Claude, then the stub guard.
 * @param {{env?: Record<string, string>, min?: number, timeoutMs?: number}} opts
 */
async function guarded({ env = {}, min = 40, timeoutMs = 20000 }) {
  const dir = freshDir('vr');
  const { spawn } = harness({ repoRoot: dir, cfg: cfgFor(), env, timeoutMs });
  const promptPath = path.join(dir, 'packet.md');
  writeFileSync(promptPath, '# code-forge review packet\n');
  const session = await spawn({ level: 'L2', role: 'reviewer', promptPath, schema: FINDING_SCHEMA });
  return validateReview(session, { hunkHeaders: HUNKS, minTokensOut: min });
}

const valid = JSON.stringify(answerFor(HUNKS));

describe('stub guard (O12): one case per unavailable reason, and the passing case', () => {
  test('exit: the CLI exits non-zero after a well-formed answer', async () => {
    const v = await guarded({ env: { FAKE_ANSWER: valid, FAKE_EXIT: '3' } });
    assert.deepEqual([v.ok, v.reason], [false, 'exit']);
  });

  test('schema: an answer that is not the finding schema (the fake S2 answer)', async () => {
    const v = await guarded({});
    assert.deepEqual([v.ok, v.reason], [false, 'schema']);
  });

  test('hunks_mismatch: reviewed_hunks differs from the packet hunks', async () => {
    const v = await guarded({ env: { FAKE_ANSWER: JSON.stringify(answerFor(['@@ -1 +1 @@'])) } });
    assert.deepEqual([v.ok, v.reason], [false, 'hunks_mismatch']);
  });

  test('too_short: tokens_out 42 under review.min_tokens_out 120', async () => {
    // a blank summary is not a structured clean pass (B53), so the full floor applies
    const v = await guarded({ env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS, { summary: '' })) }, min: 120 });
    assert.deepEqual([v.ok, v.reason, v.detail], [false, 'too_short', 'tokens_out 42 < 120']);
  });

  test('timeout: the session is killed at the session timeout', async () => {
    const v = await guarded({ env: { FAKE_ANSWER: valid, FAKE_SLEEP_MS: '5000' }, timeoutMs: 400 });
    assert.deepEqual([v.ok, v.reason], [false, 'timeout']);
  });

  test('pass: exit 0, schema, hunks in order, tokens_out 42 ≥ 40', async () => {
    const v = await guarded({ env: { FAKE_ANSWER: valid } });
    assert.equal(v.ok, true);
    assert.deepEqual([v.tokens_out, v.review.reviewed_hunks, v.review.findings.length], [42, HUNKS, 0]);
  });

  test('missing output is never a review: no session ⇒ exit, an ok session with no answer ⇒ schema', () => {
    const none = validateReview(null, { hunkHeaders: HUNKS });
    const empty = validateReview({ status: 'ok', exit_code: 0, answer: null, usage: { tokens_out: 500 } }, { hunkHeaders: HUNKS });
    assert.deepEqual([none.ok, none.reason, empty.ok, empty.reason], [false, 'exit', false, 'schema']);
  });
});

describe('clean-pass floor (B53, issue #6: clean passes of 108-121 tokens on any diff size)', () => {
  // A finding that is valid against the finding schema for HUNKS (every required field, incl.
  // `file`), so the only thing that can reject it below is the token floor.
  const finding = { id: 'F1', file: 'src/placeholder.txt', severity: 'warning', line_start: 1, line_end: 1, category: 'correctness', claim: 'c', evidence: 'e', fix: 'f' };
  /** @param {number} out @param {Record<string, any>} [extra] @param {string[]} [hunks] */
  const session = (out, extra = {}, hunks = HUNKS) => ({ status: 'ok', exit_code: 0, answer: answerFor(hunks, extra), usage: { tokens_out: out } });

  test('a structured clean pass is accepted at 108 tokens — the whole verdict', () => {
    const v = validateReview(session(108), { hunkHeaders: HUNKS, minTokensOut: 120 });
    assert.deepEqual(v, { ok: true, review: answerFor(HUNKS), tokens_out: 108 });
  });
  test('an answer with a schema-valid finding keeps the full floor: too_short, tokens_out 108 < 120', () => {
    const answer = { passed: false, findings: [finding] };
    // The same finding at the full floor is accepted, so the rejection below is the floor alone.
    assert.equal(validateReview(session(120, answer), { hunkHeaders: HUNKS, minTokensOut: 120 }).ok, true);
    const v = validateReview(session(108, answer), { hunkHeaders: HUNKS, minTokensOut: 120 });
    assert.deepEqual(v, { ok: false, reason: 'too_short', detail: 'tokens_out 108 < 120' });
  });
  test('a clean pass with a blank summary keeps the full floor', () => {
    for (const summary of ['', '   ']) {
      const v = validateReview(session(108, { summary }), { hunkHeaders: HUNKS, minTokensOut: 120 });
      assert.deepEqual(v, { ok: false, reason: 'too_short', detail: 'tokens_out 108 < 120' });
    }
  });
  test('passed: false with no findings keeps the full floor', () => {
    const v = validateReview(session(108, { passed: false }), { hunkHeaders: HUNKS, minTokensOut: 120 });
    assert.deepEqual(v, { ok: false, reason: 'too_short', detail: 'tokens_out 108 < 120' });
  });
  test('the clean-pass floor is exactly 40: 39 is too_short, 40 is accepted', () => {
    const at39 = validateReview(session(39), { hunkHeaders: HUNKS, minTokensOut: 120 });
    assert.deepEqual(at39, { ok: false, reason: 'too_short', detail: 'tokens_out 39 < 40' });
    const at40 = validateReview(session(40), { hunkHeaders: HUNKS, minTokensOut: 120 });
    assert.deepEqual(at40, { ok: true, review: answerFor(HUNKS), tokens_out: 40 });
  });
  test('a configured floor below 40 stays the floor for a clean pass too', () => {
    const v = validateReview(session(30), { hunkHeaders: HUNKS, minTokensOut: 30 });
    assert.deepEqual(v, { ok: true, review: answerFor(HUNKS), tokens_out: 30 });
  });
  test('a clean pass with mismatched hunks is rejected as hunks_mismatch', () => {
    const v = validateReview(session(108, {}, ['@@ -1 +1 @@']), { hunkHeaders: HUNKS, minTokensOut: 120 });
    assert.deepEqual(v, { ok: false, reason: 'hunks_mismatch', detail: 'reviewed_hunks has 1 entries; the packet has 1 hunks' });
  });
});
