import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { test } from 'node:test';

const { checkBlockReviews } = await import('../../src/review/gate-check.mjs');
const { signRow } = await import('../../src/state/signer.mjs');

const KEY = randomBytes(32);
const FILE = 'src/a.mjs';
const HASH = 'a'.repeat(64);
const OLD_HASH = 'b'.repeat(64);
const FILES = [{ file: FILE, content_hash: HASH }];
const signed = (/** @type {Record<string, any>} */ row) =>
  signRow({ run: 'r1', block: 'B1', file: FILE, content_hash: HASH, ...row }, KEY);
const cap = signed({ event: 'review.cap', content_hash: OLD_HASH, open: ['F1', 'F2', 'F3'] });
const reopen = signed({ event: 'review.reopened', by: 'human', from_hash: OLD_HASH, to_hash: HASH });
const round = signed({ event: 'review.round', round: 1, open_after: 1 });
const refusal = signed({ event: 'review.result', approved: false, status: 'reviewed', open: [{ id: 'F4', severity: 'warning' }] });
const waive = (/** @type {string} */ finding) => signed({ event: 'review.waived', finding, by: 'human' });
const check = (/** @type {Array<Record<string, any>>} */ rows, proofFiles = FILES) =>
  checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows, key: KEY, proofFiles, highPaths: [] });
const refusals = (/** @type {ReturnType<typeof check>} */ result) =>
  result.refusals.map((r) => [r.code, r.file, r.finding ?? null]);
const waived = (/** @type {string[]} */ findings) =>
  ({ file: FILE, content_hash: HASH, status: 'reviewed_by_waiver', findings, by: 'human' });
const unsigned = (/** @type {Record<string, any>} */ row) => {
  const { mac: _mac, ...rest } = row;
  return rest;
};

test('clause 1: an ordered verified human reopen and current signed round supersede cap ids in all three cap paths', () => {
  // Same-hash cap: its old unknown severity must not override the fresh review's warning
  // when F1 is reused. A reviewer answer can clear the fresh warning, never an unknown id.
  const sameHashCap = signed({ event: 'review.cap', open: ['F1', 'F2', 'F3'] });
  const oldRefusal = signed({ event: 'review.result', approved: false, status: 'stopped', open: [{ id: 'F1' }] });
  const freshRefusal = signed({ event: 'review.result', approved: false, status: 'reviewed', open: [{ id: 'F1', severity: 'warning' }] });
  const answer = signed({ event: 'review.answered', finding: 'F1', by_file: 'test/a.test.mjs', by_hash: HASH, ticket: 'answer' });
  const result = check([oldRefusal, sameHashCap, reopen, round, freshRefusal, answer], [...FILES, { file: 'test/a.test.mjs', content_hash: HASH }]);
  assert.deepEqual(result, {
    ok: true, refusals: [],
    waivedAll: [{ file: FILE, content_hash: HASH, status: 'reviewed_by_waiver', findings: [], by: null, answered: ['F1'] }],
  });
});

test('clause 2: after reopen the latest fresh refusal needs only F4 waived, with reviewed_by_waiver findings exactly [F4]', () => {
  const rows = [cap, reopen, round, refusal];
  const open = check(rows);
  assert.equal(open.ok, false);
  assert.deepEqual(refusals(open), [['unreviewed', FILE, null]]);
  assert.deepEqual(open.waivedAll, []);
  assert.deepEqual(check([...rows, waive('F4')]), { ok: true, refusals: [], waivedAll: [waived(['F4'])] });
  // A later refusal replaces F4 with F5; the F4 waiver cannot clear the fresh finding.
  const later = signed({ event: 'review.result', approved: false, status: 'reviewed', open: [{ id: 'F5', severity: 'warning' }] });
  assert.deepEqual(refusals(check([...rows, waive('F4'), later])), [['unreviewed', FILE, null]]);
  assert.deepEqual(check([...rows, waive('F4'), later, waive('F5')]), { ok: true, refusals: [], waivedAll: [waived(['F5'])] });
});

test('clause 3: a fresh review approving current bytes passes as approved without a review_cap refusal', () => {
  const approval = signed({ event: 'review.approved' });
  const cleanRound = signed({ event: 'review.round', round: 1, open_after: 0 });
  const cleanResult = signed({ event: 'review.result', status: 'reviewed', approved: true, open: [] });
  // An approval of these bytes may already exist before the cap of other bytes. A fresh
  // clean review after reopening clears the old cap even without another approval row.
  const rows = [approval, cap, reopen, cleanRound, cleanResult];
  assert.deepEqual(check(rows), { ok: true, refusals: [], waivedAll: [] });
  // The usual fresh approval after the reopened round also passes as approved, not waived.
  assert.deepEqual(check([...rows.slice(1), approval]), { ok: true, refusals: [], waivedAll: [] });
});

test('an agreed verified agent reopen and fresh clean review supersede the cap; other agent rows still need waivers', () => {
  const approval = signed({ event: 'review.approved' });
  const cleanRound = signed({ event: 'review.round', round: 1, open_after: 0 });
  const cleanResult = signed({ event: 'review.result', status: 'reviewed', approved: true, open: [] });
  const agent = { ...unsigned(reopen), by: 'agent', verdict: { verdict: 'agree', reason: 'The cited fix addresses the stop.' } };
  assert.deepEqual(check([approval, cap, signed(agent), cleanRound, cleanResult]), { ok: true, refusals: [], waivedAll: [] });
  const capRefusals = ['F1', 'F2', 'F3'].map((finding) => ['review_cap', FILE, finding]);
  /** @type {Array<[string, Record<string, any>, boolean]>} */
  const cases = [
    ['disagree', signed({ ...agent, verdict: { verdict: 'disagree', reason: 'Insufficient evidence.' } }), false],
    ['missing verdict', signed({ ...agent, verdict: undefined }), false],
    ['unsigned', agent, true],
    ['bad MAC', { ...signed(agent), mac: '0'.repeat(64) }, true],
  ];
  for (const [name, row, unproven] of cases) {
    const rows = [approval, cap, row, cleanRound, cleanResult];
    const result = check(rows);
    assert.equal(result.ok, false, name);
    assert.deepEqual(refusals(result), [...capRefusals, ...(unproven ? [['unproven', FILE, null]] : [])], name);
    assert.deepEqual(result.waivedAll, [], name);
    if (!unproven) assert.deepEqual(check([...rows, ...['F1', 'F2', 'F3'].map(waive)]), { ok: true, refusals: [], waivedAll: [] }, name);
  }
  assert.deepEqual(refusals(check([approval, cap, signed(agent)])), capRefusals, 'a reopen alone does not supersede the cap');
});

test('clause 4: missing, invalid, mismatched or out-of-order reopen/round rows keep all three old cap ids', () => {
  // Positive control distinguishes a real supersede from merely retaining every cap.
  assert.deepEqual(check([cap, reopen, round, refusal, waive('F4')]), { ok: true, refusals: [], waivedAll: [waived(['F4'])] });
  const badReopen = { ...reopen, mac: '0'.repeat(64) };
  const badRound = { ...round, mac: '0'.repeat(64) };
  /** @type {Array<[string, Array<Record<string, any>>, boolean]>} */
  const cases = [
    ['missing reopen', [cap, round], false],
    ['unsigned reopen', [cap, unsigned(reopen), round], true],
    ['bad MAC reopen', [cap, badReopen, round], true],
    ['non-human reopen', [cap, signed({ ...unsigned(reopen), by: 'autopilot' }), round], false],
    ['other block reopen', [cap, signed({ ...unsigned(reopen), block: 'B2' }), round], false],
    ['other file reopen', [cap, signed({ ...unsigned(reopen), file: 'src/b.mjs' }), round], false],
    ['other run reopen', [cap, signed({ ...unsigned(reopen), run: 'r2' }), round], false],
    ['missing run reopen', [cap, signed({ ...unsigned(reopen), run: undefined }), round], false],
    ['reopen before cap', [reopen, cap, round], false],
    ['missing round', [cap, reopen], false],
    ['unsigned round', [cap, reopen, unsigned(round)], true],
    ['bad MAC round', [cap, reopen, badRound], true],
    ['old hash round', [cap, reopen, signed({ ...unsigned(round), content_hash: OLD_HASH })], false],
    ['round before reopen', [cap, round, reopen], false],
    ['other block round', [cap, reopen, signed({ ...unsigned(round), block: 'B2' })], false],
    ['other file round', [cap, reopen, signed({ ...unsigned(round), file: 'src/b.mjs' })], false],
    ['other run round', [cap, reopen, signed({ ...unsigned(round), run: 'r2' })], false],
    ['missing run round', [cap, reopen, signed({ ...unsigned(round), run: undefined })], false],
  ];
  for (const [name, rows, unproven] of cases) {
    const result = check([...rows, refusal, waive('F4')]);
    assert.equal(result.ok, false, name);
    assert.deepEqual(refusals(result), [
      ['unreviewed', FILE, null],
      ['review_cap', FILE, 'F1'], ['review_cap', FILE, 'F2'], ['review_cap', FILE, 'F3'],
      ...(unproven ? [['unproven', FILE, null]] : []),
    ], name);
    assert.deepEqual(result.waivedAll, [], name);
  }
});
