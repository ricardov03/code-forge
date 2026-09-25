import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  bandFinding,
  bandResolved,
  bandsFor,
  computeConfidenceMargin,
  decide,
  DEFAULT_BAND,
  DEFAULT_FINDINGS,
  findingsThresholds,
} from '../../src/decide/thresholds.mjs';

// ── bandsFor: defaults, per-question override, thresholds.default override ─────────────────────

test('bandsFor falls back to the documented defaults when nothing is configured', () => {
  assert.deepEqual(bandsFor('lane', {}), { act: 0.9, check: 0.6, close_margin: 0.15 });
  assert.deepEqual(DEFAULT_BAND, { act: 0.9, check: 0.6, close_margin: 0.15 });
});

test('bandsFor: thresholds.default overrides the built-in default, per-question overrides thresholds.default', () => {
  const cfg = { thresholds: { default: { act: 0.8 }, risk: { check: 0.5 } } };
  assert.deepEqual(bandsFor('risk', cfg), { act: 0.8, check: 0.5, close_margin: 0.15 });
  assert.deepEqual(bandsFor('lane', cfg), { act: 0.8, check: 0.6, close_margin: 0.15 });
});

// ── computeConfidenceMargin: choice / score / noul ──────────────────────────────────────────────

test('computeConfidenceMargin: choice — top1/top2 from probabilities, value is the argmax key', () => {
  const r = computeConfidenceMargin({ type: 'choice', choice: 'L1', probabilities: { L0: 0.1, L1: 0.7, L2: 0.2 } });
  assert.equal(r.confidence, 0.7);
  assert.ok(Math.abs(r.margin - 0.5) < 1e-9);
  assert.equal(r.value, 'L1');
});

test('computeConfidenceMargin: choice with a single non-zero probability — margin equals confidence (top2 = 0)', () => {
  const r = computeConfidenceMargin({ type: 'choice', probabilities: { A: 1, B: 0, C: 0 } });
  assert.equal(r.confidence, 1);
  assert.equal(r.margin, 1);
});

test('computeConfidenceMargin: score — confidence/margin from probabilities, value is the raw score', () => {
  const r = computeConfidenceMargin({ type: 'score', score: 1.5, probabilities: { 0: 0.1, 1: 0.6, 2: 0.2, 3: 0.1 } });
  assert.equal(r.confidence, 0.6);
  assert.ok(Math.abs(r.margin - 0.4) < 1e-9);
  assert.equal(r.value, 1.5);
});

test('computeConfidenceMargin: noul — O8 formulas for p >= 0.5 and p < 0.5', () => {
  const high = computeConfidenceMargin({ type: 'noul', noul: 0.8 });
  assert.equal(high.confidence, 0.8);
  assert.ok(Math.abs(high.margin - 0.6) < 1e-9);
  assert.equal(high.value, true);

  const low = computeConfidenceMargin({ type: 'noul', noul: 0.2 });
  assert.ok(Math.abs(low.confidence - 0.8) < 1e-9);
  assert.ok(Math.abs(low.margin - 0.6) < 1e-9);
  assert.equal(low.value, false);

  const exact = computeConfidenceMargin({ type: 'noul', noul: 0.5 });
  assert.equal(exact.confidence, 0.5);
  assert.equal(exact.margin, 0);
  assert.equal(exact.value, true); // p >= 0.5 ⇒ true, boundary included
});

test('computeConfidenceMargin: rejects malformed answers instead of silently guessing', () => {
  assert.throws(() => computeConfidenceMargin(null), TypeError);
  assert.throws(() => computeConfidenceMargin({ type: 'noul', noul: 1.5 }), TypeError);
  assert.throws(() => computeConfidenceMargin({ type: 'noul', noul: 'high' }), TypeError);
  assert.throws(() => computeConfidenceMargin({ type: 'score', score: 'NaN', probabilities: { 0: 1 } }), TypeError);
  assert.throws(() => computeConfidenceMargin({ type: 'mystery', probabilities: { a: 1 } }), TypeError);
  assert.throws(() => computeConfidenceMargin({ type: 'choice', probabilities: {} }), TypeError);
});

// ── decide(): the three stages ──────────────────────────────────────────────────────────────────

test('decide: act when confidence >= act AND margin >= close_margin', () => {
  const r = decide('security_sensitive', { type: 'noul', noul: 0.96 }, {});
  assert.equal(r.decided, true);
  assert.equal(r.stage, 'act');
  assert.equal(r.value, true); // p >= 0.5 ⇒ true — fix round 1: no prior decide() test checked the VALUE at all
});

test('decide: check when confidence is in [check, act), regardless of margin', () => {
  const r = decide('security_sensitive', { type: 'noul', noul: 0.7 }, {});
  assert.equal(r.decided, false);
  assert.equal(r.stage, 'check');
});

// Fix round 1 finding: the plan §3.3 worked example quotes exactly this input ("finding:nit",
// noul 0.03) as its own acceptance clause ("noul 0.03 ⇒ decided=true, answer=false") — no
// `decide()` test exercised it directly (the fixture-replay test in probes.test.mjs does, but a
// bug that inverted the VALUE for p < 0.5 — the exact O8 bug this clause exists to catch — would
// not have been caught here, on decide() itself, in isolation).
test('decide: noul 0.03 ⇒ decided=true, stage=act, value=false (plan §3.3 worked example)', () => {
  const r = decide('security_sensitive', { type: 'noul', noul: 0.03 }, {});
  assert.equal(r.decided, true);
  assert.equal(r.stage, 'act');
  assert.equal(r.value, false);
});

// Fix round 1 finding: no decide() test sat exactly ON a boundary (confidence == act,
// margin == close_margin, confidence == check) — a >/>= flip on either comparison would go
// undetected. `probabilities` need not sum to 1 here (computeConfidenceMargin never requires
// that): these are synthetic distributions built ONLY to land top1/top2 on an exact value.
//
// Fix round 2 MAJOR: the previous margin case used `top1: 0.9, top2: 0.75` expecting
// `margin === 0.15` — but `0.9 - 0.75` is `0.15000000000000002` in IEEE-754 double precision
// (verified: `0.9 - 0.75 === 0.15` is `false`), STRICTLY greater than close_margin, so both
// `margin >= close_margin` and a mutated `margin > close_margin` passed it — the boundary was
// never actually exercised, and the `Math.abs(r.margin - 0.15) < 1e-9` tolerance hid that. The
// values below are all binary fractions (0.75 = 3/4, 0.5 = 1/2, 0.375 = 3/8) that ARE exactly
// representable, and every assertion below is `assert.equal`, never a tolerance.
test('decide: margin EXACTLY at close_margin, on an exactly-representable boundary (act 0.75, close_margin 0.5, noul 0.75) ⇒ act', () => {
  const cfg = { thresholds: { default: { act: 0.75, close_margin: 0.5 } } };
  const r = decide('security_sensitive', { type: 'noul', noul: 0.75 }, cfg);
  assert.equal(r.confidence, 0.75); // exact: max(0.75, 1 - 0.75)
  assert.equal(r.margin, 0.5); // exact: |2 * 0.75 - 1|
  assert.equal(r.stage, 'act');
  assert.equal(r.decided, true);
});

test('decide: margin just BELOW close_margin (same act/close_margin, confidence still at act) ⇒ check, not act', () => {
  const cfg = { thresholds: { default: { act: 0.75, close_margin: 0.5 } } };
  // top1 = 0.75 keeps confidence at exactly `act`; top2 = 0.375 makes margin = 0.375 < 0.5.
  const r = decide('lane', { type: 'choice', probabilities: { A: 0.75, B: 0.375 } }, cfg);
  assert.equal(r.confidence, 0.75);
  assert.equal(r.margin, 0.375);
  assert.equal(r.stage, 'check');
  assert.equal(r.decided, false);
});

test('decide: confidence EXACTLY check (0.60) ⇒ check, not decide (the check band is inclusive on its low end)', () => {
  const r = decide('lane', { type: 'choice', probabilities: { A: 0.6, B: 0.4 } }, {}); // top1=0.6
  assert.equal(r.confidence, 0.6);
  assert.equal(r.stage, 'check');
  assert.equal(r.decided, false);
});

test('decide: check (not act) when confidence >= act but margin < close_margin — a real S1-hedged answer', () => {
  // A lowered `act`/`check` isolates the "confidence >= act, but margin < close_margin" branch
  // without needing a probability distribution with two near-tied top choices at 0.9+.
  const cfg = { thresholds: { default: { act: 0.4, check: 0.3, close_margin: 0.2 } } };
  const hedged = decide('lane', { type: 'choice', probabilities: { L0: 0.45, L1: 0.35, L2: 0.2 } }, cfg);
  assert.ok(hedged.confidence >= 0.4 && hedged.margin < 0.2);
  assert.equal(hedged.stage, 'check');
  assert.equal(hedged.decided, false);
});

test('decide: decide (S2 alone) when confidence < check', () => {
  const r = decide('risk', { type: 'score', score: 1.5, probabilities: { 0: 0.3, 1: 0.3, 2: 0.25, 3: 0.15 } }, {});
  assert.equal(r.stage, 'decide');
  assert.equal(r.decided, false);
});

// ── findings bands (separate from the general act/check/decide bands) ───────────────────────────

test('findingsThresholds defaults and override', () => {
  assert.deepEqual(findingsThresholds({}), DEFAULT_FINDINGS);
  assert.deepEqual(findingsThresholds({ thresholds: { findings: { fix: 0.95 } } }), { fix: 0.95, nit: 0.4, resolved: 0.9 });
});

test('bandFinding: fix_now >= 0.90, judge in [0.40, 0.90), nit < 0.40 — boundaries included on the low side', () => {
  assert.equal(bandFinding(0.9, {}), 'fix_now');
  assert.equal(bandFinding(0.95, {}), 'fix_now');
  assert.equal(bandFinding(0.89999, {}), 'judge');
  assert.equal(bandFinding(0.4, {}), 'judge');
  assert.equal(bandFinding(0.39999, {}), 'nit');
  assert.equal(bandFinding(0, {}), 'nit');
});

test('bandResolved: closed >= 0.90, else recheck', () => {
  assert.equal(bandResolved(0.9, {}), 'closed');
  assert.equal(bandResolved(0.89999, {}), 'recheck');
});

test('bandFinding/bandResolved reject an out-of-range probability', () => {
  assert.throws(() => bandFinding(1.5, {}), TypeError);
  assert.throws(() => bandFinding(-0.1, {}), TypeError);
  assert.throws(() => bandResolved(NaN, {}), TypeError);
});
