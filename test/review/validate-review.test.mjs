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
    const v = await guarded({ env: { FAKE_ANSWER: valid }, min: 120 });
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

describe('clean-pass floor on a small diff (a 3-line placeholder reviewed in 108 tokens)', () => {
  const finding = { id: 'F1', severity: 'warning', line_start: 1, line_end: 1, category: 'correctness', claim: 'c', evidence: 'e', fix: 'f' };
  /** @param {number} out @param {Record<string, any>} [extra] */
  const session = (out, extra = {}) => ({ status: 'ok', exit_code: 0, answer: answerFor(HUNKS, extra), usage: { tokens_out: out } });

  test('a clean pass on a small diff is accepted at 108 tokens', () => {
    const v = validateReview(session(108), { hunkHeaders: HUNKS, minTokensOut: 120, smallDiff: true });
    assert.equal(v.ok, true);
  });
  test('the same answer on a large diff keeps the full floor', () => {
    const v = validateReview(session(108), { hunkHeaders: HUNKS, minTokensOut: 120, smallDiff: false });
    assert.deepEqual([v.ok, v.reason, v.detail], [false, 'too_short', 'tokens_out 108 < 120']);
  });
  test('an answer with a finding keeps the full floor even on a small diff', () => {
    const v = validateReview(session(108, { passed: false, findings: [finding] }), { hunkHeaders: HUNKS, minTokensOut: 120, smallDiff: true });
    assert.equal(v.ok === true, false);
  });
  test('a clean pass still needs the clean-pass minimum', () => {
    const v = validateReview(session(30), { hunkHeaders: HUNKS, minTokensOut: 120, smallDiff: true });
    assert.deepEqual([v.ok, v.reason, v.detail], [false, 'too_short', 'tokens_out 30 < 40']);
  });
});
