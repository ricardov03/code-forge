/**
 * Fallback rules (plan §3.5, O9/O27) — "rules-final: floor path ⇒ source: rules, S2 call count 0;
 * residue ⇒ S2 call count 1; s2-heavy ignores s2-fallback".
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  alwaysResidue,
  countS2Calls,
  countsTowardS2Heavy,
  evaluateS2Heavy,
  fallbackLane,
  fallbackNext,
  fallbackRisk,
  fallbackSecuritySensitive,
  LANE_SIZE_FILES_FLOOR,
  LANE_SIZE_LINES_FLOOR,
  resolveDispatchQuestionsByRules,
  resolveNextByRules,
  S2_HEAVY_THRESHOLD,
} from '../../src/decide/fallback-rules.mjs';

// ── Path floors ⇒ risk 3, security true (and lane escalates too) ───────────────────────────────

test('a path-floor hit is final for lane, risk AND security_sensitive — no ambiguity', () => {
  const facts = { pathFloorHit: true };
  assert.equal(fallbackLane(facts), 'L2');
  assert.equal(fallbackRisk(facts), 3);
  assert.equal(fallbackSecuritySensitive(facts), true);
});

// ── diff size ⇒ >= L1; migrations/policies/middleware ⇒ L2 ─────────────────────────────────────
//
// Fix round 1 finding: the size branch used to return 'L1' on the SAME value as the fall-through
// default, making it dead code (unreachable — no test could ever distinguish "the size floor
// fired" from "nothing fired") and 'L0' unreachable despite the JSDoc listing it. The baseline
// with no signal at all is now 'L0'; the size floor is what bumps it to 'L1'. These are exact
// boundary tests (4 vs 5 files, 200 vs 201 lines) so a >/>= flip in either comparison goes red.

test('fallbackLane: no signal at all (no floor, no migration, small diff) is L0, the true baseline', () => {
  assert.equal(fallbackLane({}), 'L0');
  assert.equal(fallbackLane({ filesChanged: 1, linesAdded: 10 }), 'L0');
});

test('fallbackLane: the file-count floor is EXCLUSIVE — 4 files stays L0, 5 files reaches L1', () => {
  assert.equal(fallbackLane({ filesChanged: LANE_SIZE_FILES_FLOOR }), 'L0'); // 4: at, not over, the floor
  assert.equal(fallbackLane({ filesChanged: LANE_SIZE_FILES_FLOOR + 1 }), 'L1'); // 5: over it
});

test('fallbackLane: the added-lines floor is EXCLUSIVE — 200 lines stays L0, 201 lines reaches L1', () => {
  assert.equal(fallbackLane({ linesAdded: LANE_SIZE_LINES_FLOOR }), 'L0'); // 200: at, not over, the floor
  assert.equal(fallbackLane({ linesAdded: LANE_SIZE_LINES_FLOOR + 1 }), 'L1'); // 201: over it
});

test('fallbackLane: migration/policy/middleware forces L2 even with a small diff', () => {
  assert.equal(fallbackLane({ touchesMigration: true, filesChanged: 1 }), 'L2');
  assert.equal(fallbackLane({ touchesPolicyOrMiddleware: true }), 'L2');
});

test('fallbackRisk: escalates 0 → 1 → 2 → 3 as signals stack, path floor always wins', () => {
  assert.equal(fallbackRisk({}), 0);
  assert.equal(fallbackRisk({ filesChanged: 10 }), 1);
  assert.equal(fallbackRisk({ touchesMigration: true }), 2);
  assert.equal(fallbackRisk({ touchesMigration: true, pathFloorHit: true }), 3);
});

test('fallbackSecuritySensitive: keywords Auth/Webhook/Money trigger it even without a path floor', () => {
  assert.equal(fallbackSecuritySensitive({ keywordsFound: ['Auth'] }), true);
  assert.equal(fallbackSecuritySensitive({ keywordsFound: ['Webhook'] }), true);
  assert.equal(fallbackSecuritySensitive({ keywordsFound: ['Money'] }), true);
  assert.equal(fallbackSecuritySensitive({ keywordsFound: ['Policy'] }), false); // not a security keyword
  assert.equal(fallbackSecuritySensitive({}), false);
});

// ── "floor path ⇒ source: rules, S2 call count 0" ───────────────────────────────────────────────
//
// Fix round 1 finding: `countS2Calls` counts the `source` LABEL the resolver itself attaches —
// this module never calls S2 (there is no S2-calling code anywhere in `src/decide/**`; that is
// B9's `src/session/s2.mjs`, a later block), so there is no real spy to inject here. What these
// tests can and do prove instead: the source label tracks something OTHER than "value happens to
// be non-null" — it is exercised across every input shape lane/risk/security_sensitive/next can
// take (floor, no-floor, unambiguous, ambiguous), not just the one case each acceptance clause
// names, so a bug that hardcodes `source` independently of which branch actually fired would show
// up as a wrong count on at least one of these, not just the headline case.

test('resolveDispatchQuestionsByRules on the floor path: all three answers source: rules, S2 call count 0', () => {
  const resolved = resolveDispatchQuestionsByRules({ pathFloorHit: true });
  assert.equal(resolved.lane.source, 'rules');
  assert.equal(resolved.risk.source, 'rules');
  assert.equal(resolved.security_sensitive.source, 'rules');
  assert.equal(resolved.lane.value, 'L2');
  assert.equal(resolved.risk.value, 3);
  assert.equal(resolved.security_sensitive.value, true);
  assert.equal(countS2Calls(Object.values(resolved)), 0);
});

test('resolveDispatchQuestionsByRules with NO signal at all: still all source: rules, S2 call count 0', () => {
  const resolved = resolveDispatchQuestionsByRules({});
  assert.equal(resolved.lane.value, 'L0');
  assert.equal(resolved.risk.value, 0);
  assert.equal(resolved.security_sensitive.value, false);
  assert.equal(resolved.lane.source, 'rules');
  assert.equal(resolved.risk.source, 'rules');
  assert.equal(resolved.security_sensitive.source, 'rules');
  assert.equal(countS2Calls(Object.values(resolved)), 0);
});

// ── "residue ⇒ S2 call count 1" ──────────────────────────────────────────────────────────────────

test('fallbackNext: unambiguous evidence resolves by rule; spreading/later-attempt failures are residue (null)', () => {
  assert.equal(fallbackNext({ allGreen: true, newTestsAdded: true, attempt: 1 }), 'complete');
  assert.equal(fallbackNext({ allGreen: true, noNewTestsReason: 'pure refactor', attempt: 1 }), 'complete');
  assert.equal(fallbackNext({ allGreen: false, spreading: false, attempt: 1 }), 'retry');
  assert.equal(fallbackNext({ allGreen: false, spreading: true, attempt: 3 }), null); // ambiguous
  assert.equal(fallbackNext({ allGreen: false, spreading: false, attempt: 2 }), null); // not the FIRST attempt
});

test('resolveNextByRules: an UNAMBIGUOUS evidence shape ⇒ source: rules, S2 call count 0', () => {
  const resolved = resolveNextByRules({ allGreen: true, newTestsAdded: true, attempt: 1 });
  assert.equal(resolved.value, 'complete');
  assert.equal(resolved.source, 'rules');
  assert.equal(countS2Calls([resolved]), 0);
});

test('resolveNextByRules: residue ⇒ S2 call count 1', () => {
  const resolved = resolveNextByRules({ allGreen: false, spreading: true, attempt: 3 });
  assert.equal(resolved.value, null);
  assert.equal(resolved.source, 's2-fallback');
  assert.equal(countS2Calls([resolved]), 1);
});

test('alwaysResidue: defect/resolved/scope have no rule at all — always residue', () => {
  for (const id of ['defect', 'resolved', 'scope']) {
    const r = alwaysResidue(id);
    assert.equal(r.value, null);
    assert.equal(r.source, 's2-fallback');
  }
});

// ── "s2-heavy ignores s2-fallback" ──────────────────────────────────────────────────────────────

test('countsTowardS2Heavy: fallback-sourced calls never count', () => {
  assert.equal(countsTowardS2Heavy('s2-fallback'), false);
  assert.equal(countsTowardS2Heavy('s2'), true);
});

test('s2-heavy ignores s2-fallback: 7 fallback calls stay 0/not-heavy; 7 non-fallback calls DO trip it', () => {
  const allFallback = Array.from({ length: 7 }, () => ({ source: 's2-fallback' }));
  assert.deepEqual(evaluateS2Heavy(allFallback), { count: 0, heavy: false });

  const sevenReal = Array.from({ length: 7 }, () => ({ source: 's2' }));
  assert.deepEqual(evaluateS2Heavy(sevenReal), { count: 7, heavy: true });
  assert.ok(7 > S2_HEAVY_THRESHOLD);
});

test('s2-heavy: a mix counts only the non-fallback calls (6 real + 20 fallback ⇒ not heavy at exactly 6)', () => {
  const mixed = [
    ...Array.from({ length: 6 }, () => ({ source: 's2' })),
    ...Array.from({ length: 20 }, () => ({ source: 's2-fallback' })),
  ];
  assert.deepEqual(evaluateS2Heavy(mixed), { count: 6, heavy: false }); // > 6 is heavy, 6 itself is not
});
