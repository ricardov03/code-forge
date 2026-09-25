import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildCalibration, CALIBRATION_BUCKETS, sampleForShadow, shouldShadowSample } from '../../src/ledger/calibration.mjs';

const sumAllCells = (buckets) => Object.values(buckets).reduce((sum, cell) => sum + cell.correct + cell.wrong + cell.unknown, 0);

test('CALIBRATION_BUCKETS has exactly 8 buckets', () => {
  assert.equal(CALIBRATION_BUCKETS.length, 8);
});

test('buildCalibration renders an 8 x 3 grid (24 cells) whose total equals the answer count (3), not just one cell per answer', () => {
  const answers = [
    { decision_id: 'd1', question: 'lane', confidence: 0.55 },
    { decision_id: 'd2', question: 'lane', confidence: 0.92 },
    { decision_id: 'd3', question: 'risk', confidence: 1.0 },
  ];
  const outcomes = [
    { decision_id: 'd1', outcome: 'wrong' },
    { decision_id: 'd2', outcome: 'correct' },
  ];
  const { buckets, unbacked, unmapped } = buildCalibration(answers, outcomes);

  assert.equal(Object.keys(buckets).length, 8);
  for (const b of Object.values(buckets)) {
    assert.deepEqual(Object.keys(b).sort(), ['correct', 'unknown', 'wrong']);
  }
  assert.equal(buckets['0.50-0.60'].wrong, 1);
  assert.equal(buckets['0.90-0.95'].correct, 1);
  assert.equal(buckets['0.99-1.00'].unknown, 1); // d3 has no matching outcome row
  assert.equal(sumAllCells(buckets), 3, 'every answer must land in exactly one cell — no double counting, no dropped answer');
  assert.equal(unbacked, 0);
  assert.equal(unmapped, 0);
});

test('a confidence below 0.5 is unbacked: it lands in NO cell of ANY bucket, not silently mis-bucketed', () => {
  const { unbacked, buckets } = buildCalibration([{ decision_id: 'x', question: 'q', confidence: 0.3 }], []);
  assert.equal(unbacked, 1);
  assert.equal(sumAllCells(buckets), 0, 'the sub-0.5 answer must not have been counted anywhere');
});

test('bucket boundaries: 0.50, 0.60, 0.95 and 0.99 each land in the documented bucket', () => {
  const boundaryCases = [
    { confidence: 0.5, expected: '0.50-0.60' },
    { confidence: 0.6, expected: '0.60-0.70' }, // 0.50-0.60's max is EXCLUSIVE
    { confidence: 0.95, expected: '0.95-0.99' }, // 0.90-0.95's max is EXCLUSIVE
    { confidence: 0.99, expected: '0.99-1.00' }, // 0.95-0.99's max is EXCLUSIVE; 0.99-1.00 is CLOSED
    { confidence: 1.0, expected: '0.99-1.00' },
  ];
  for (const { confidence, expected } of boundaryCases) {
    const { buckets } = buildCalibration([{ decision_id: 'x', question: 'q', confidence }], []);
    assert.equal(buckets[expected].unknown, 1, `confidence ${confidence} should land in ${expected}`);
    assert.equal(sumAllCells(buckets), 1, `confidence ${confidence} should land in exactly one cell`);
  }
});

test('float noise just under a bucket edge (e.g. 0.85 arriving as 0.8499999999999996) still lands in the bucket the TRUE value 0.85 belongs to, not one bucket low', () => {
  const nearEdge = 0.85 - Number.EPSILON * 2; // simulates max(p, 1-p) float error just under 0.85
  assert.notEqual(nearEdge, 0.85, 'the fixture must actually exercise float noise, not accidentally equal 0.85');
  assert.ok(nearEdge < 0.85, 'the fixture must sit just BELOW 0.85, where an unrounded comparison would misbucket it');

  const { buckets } = buildCalibration([{ decision_id: 'x', question: 'q', confidence: nearEdge }], []);
  // 0.85 belongs to [0.85, 0.90) — an unrounded comparison would (wrongly) put it in [0.80, 0.85).
  assert.equal(buckets['0.85-0.90'].unknown, 1);
  assert.equal(buckets['0.80-0.85'].unknown, 0);
});

test('an outcome value outside the known vocabulary (correct/wrong/reverted/unknown) is tracked separately as unmapped, not folded into "unknown"', () => {
  const { buckets, unmapped } = buildCalibration(
    [{ decision_id: 'x', question: 'q', confidence: 0.7 }],
    [{ decision_id: 'x', outcome: 'some-future-value' }],
  );
  assert.equal(unmapped, 1);
  assert.equal(sumAllCells(buckets), 0, 'an unmapped outcome must not silently look like "unknown" (no verdict)');
});

test('a "reverted" outcome (from --scan-git) maps to "wrong" in the grid, not "unknown"', () => {
  const { buckets } = buildCalibration([{ decision_id: 'x', question: 'q', confidence: 0.7 }], [{ decision_id: 'x', outcome: 'reverted' }]);
  assert.equal(buckets['0.70-0.80'].wrong, 1);
  assert.equal(buckets['0.70-0.80'].unknown, 0);
});

test('shouldShadowSample never consults rng at shadow_rate 0 and always returns false', () => {
  let calls = 0;
  const rng = () => {
    calls += 1;
    return 0;
  };
  for (let i = 0; i < 50; i += 1) {
    assert.equal(shouldShadowSample(0, rng), false);
  }
  assert.equal(calls, 0);
});

test('shouldShadowSample rejects NaN and out-of-[0,0.5]-range rates instead of silently passing them through', () => {
  assert.throws(() => shouldShadowSample(NaN), RangeError);
  assert.throws(() => shouldShadowSample(-0.1), RangeError);
  assert.throws(() => shouldShadowSample(0.51), RangeError);
  assert.throws(() => shouldShadowSample(Infinity), RangeError);
});

test('sampleForShadow samples 0 of 50 eligible rows at shadow_rate: 0 (counted)', () => {
  const eligible = Array.from({ length: 50 }, (_, i) => ({ decision_id: `d${i}` }));
  assert.equal(sampleForShadow(eligible, 0).length, 0);
});

test('sampleForShadow picks EXACTLY the decision_ids an alternating rng says it should, in order', () => {
  const eligible = ['d0', 'd1', 'd2', 'd3'].map((decision_id) => ({ decision_id }));
  const sequence = [0.1, 0.9, 0.1, 0.9]; // rate 0.5: 0.1 < 0.5 (sampled), 0.9 < 0.5 (not)
  let i = 0;
  const rng = () => sequence[i++];
  const sampled = sampleForShadow(eligible, 0.5, rng);
  assert.deepEqual(sampled.map((r) => r.decision_id), ['d0', 'd2']);
});

test('sampleForShadow samples 0 rows when rng always returns a value above the rate', () => {
  const eligible = Array.from({ length: 10 }, (_, i) => ({ decision_id: `d${i}` }));
  assert.equal(sampleForShadow(eligible, 0.5, () => 0.99).length, 0);
});

test('sampleForShadow samples every row when rng always returns 0 and rate > 0', () => {
  const eligible = Array.from({ length: 10 }, (_, i) => ({ decision_id: `d${i}` }));
  assert.equal(sampleForShadow(eligible, 0.5, () => 0).length, 10);
});
