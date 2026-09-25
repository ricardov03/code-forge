/**
 * Escalation precedence (plan §3.6, O10, R1) — "5 rules × 2 cases = 10 tests" (fires / falls
 * through, for each of the 5 documented rules), plus the `l3_mode: patch` round trip (O10/R1)
 * tested separately, as the plan's `decide/` test list itemizes it apart from the 10.
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  dispatchLevel,
  escalationAfterAttempt,
  LEVEL_ORDER,
  levelIndex,
  mustRouteNextThroughEscalation,
  nextLevelCapped,
  securityFloorLevel,
} from '../../src/decide/escalation.mjs';

// ── Level arithmetic (used by rules 1, 2, 3, 5) ─────────────────────────────────────────────────

test('LEVEL_ORDER / levelIndex / nextLevelCapped', () => {
  assert.deepEqual(LEVEL_ORDER, ['L0', 'L1', 'L2', 'L3']);
  assert.equal(levelIndex('L2'), 2);
  assert.throws(() => levelIndex('L4'), TypeError);
  assert.equal(nextLevelCapped('L0'), 'L1');
  assert.equal(nextLevelCapped('L2'), 'L3');
  assert.equal(nextLevelCapped('L3'), 'L3'); // capped at stopAt — never skips past it
});

test('securityFloorLevel: min(max(lane, L1) + 1, L2) — always L2 for any real lane (L0/L1/L2)', () => {
  assert.equal(securityFloorLevel('L0'), 'L2');
  assert.equal(securityFloorLevel('L1'), 'L2');
  assert.equal(securityFloorLevel('L2'), 'L2');
});

// ── Rule 1: attempt counter ──────────────────────────────────────────────────────────────────────

test('rule 1 FIRES: attempts at the current level == retries_per_level and not green ⇒ +1 level', () => {
  const r = escalationAfterAttempt({ attemptsAtLevel: 2, retriesPerLevel: 2, gateGreen: false, currentLevel: 'L1' });
  assert.equal(r.trigger, 'retries');
  assert.equal(r.action, 'escalate');
  assert.equal(r.level, 'L2');
});

test('rule 1 does NOT fire: attempts below the limit', () => {
  const r = escalationAfterAttempt({ attemptsAtLevel: 1, retriesPerLevel: 2, gateGreen: false, currentLevel: 'L1' });
  assert.notEqual(r.trigger, 'retries');
  assert.equal(r.action, 'none');
});

// ── Rule 2: review rounds ───────────────────────────────────────────────────────────────────────

test('rule 2 FIRES: review_rounds_per_level exhausted with an open fix_now finding ⇒ +1 level', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 0,
    reviewRoundsAtLevel: 2,
    reviewRoundsPerLevel: 2,
    openFixNowFinding: true,
    currentLevel: 'L1',
  });
  assert.equal(r.trigger, 'review_rounds');
  assert.equal(r.action, 'escalate');
  assert.equal(r.level, 'L2');
});

test('rule 2 does NOT fire: rounds exhausted but no open fix_now finding', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 0,
    reviewRoundsAtLevel: 2,
    reviewRoundsPerLevel: 2,
    openFixNowFinding: false,
    currentLevel: 'L1',
  });
  assert.notEqual(r.trigger, 'review_rounds');
  assert.equal(r.action, 'none');
});

// ── Rule 3: security floor (dispatch time only) ─────────────────────────────────────────────────

test('rule 3 FIRES: security_sensitive true forces the security floor at dispatch', () => {
  const r = dispatchLevel({ lane: 'L0', securitySensitive: true });
  assert.equal(r.trigger, 'security');
  assert.equal(r.level, 'L2');
});

test('rule 3 does NOT fire: security_sensitive false dispatches straight at the lane', () => {
  const r = dispatchLevel({ lane: 'L1', securitySensitive: false });
  assert.equal(r.trigger, null);
  assert.equal(r.level, 'L1');
});

// ── Rule 4: S2 ruling ────────────────────────────────────────────────────────────────────────────

test('rule 4 FIRES: S1 next=escalate is never acted on directly — routed to S2 as a check', () => {
  const r = escalationAfterAttempt({ attemptsAtLevel: 0, currentLevel: 'L1', s1Next: 'escalate' });
  assert.equal(r.trigger, 's2_ruling');
  assert.equal(r.action, 's2_check');
});

test('rule 4 does NOT fire: S1 next=complete/retry never routes to S2', () => {
  const complete = escalationAfterAttempt({ attemptsAtLevel: 0, currentLevel: 'L1', s1Next: 'complete' });
  assert.equal(complete.action, 'none');
  const retry = escalationAfterAttempt({ attemptsAtLevel: 0, currentLevel: 'L1', s1Next: 'retry' });
  assert.equal(retry.action, 'none');
});

test('mustRouteNextThroughEscalation matches rule 4 exactly (escalate and stop, nothing else)', () => {
  assert.equal(mustRouteNextThroughEscalation('escalate'), true);
  assert.equal(mustRouteNextThroughEscalation('stop'), true);
  assert.equal(mustRouteNextThroughEscalation('complete'), false);
  assert.equal(mustRouteNextThroughEscalation('retry'), false);
});

test('S1 next=stop routes straight to the human (action: stop), not through S2', () => {
  const r = escalationAfterAttempt({ attemptsAtLevel: 0, currentLevel: 'L1', s1Next: 'stop' });
  assert.equal(r.trigger, 's2_ruling');
  assert.equal(r.action, 'stop');
});

// ── Rule 5: never skip a level; stop_at is fixed (the ceiling guard on rules 1/2) ──────────────────
//
// Fix round 1 finding: the original pair both hardcoded stopAt to its 'L3' default, so the "the
// ceiling is stopAt, not always L3" claim was never actually exercised — a mutant that ignored
// stopAt entirely (always capping at 'L3') would have survived. These now pass an EXPLICIT stopAt
// below L3 so "L2 is the real ceiling here, not L3" is the thing under test.

test('rule 5 FIRES: escalating past an explicit ceiling BELOW L3 (stopAt: L2) reaches the L3 rung, not level L3', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 2,
    retriesPerLevel: 2,
    gateGreen: false,
    currentLevel: 'L2',
    stopAt: 'L2',
    l3Mode: 'patch',
  });
  assert.equal(r.trigger, 'retries');
  assert.equal(r.action, 'l3_rung'); // 'L2' IS the ceiling here — 'L3' is never reached
  assert.equal(r.mode, 'patch');
});

test('rule 5 does NOT fire: an ordinary escalate below an explicit ceiling (stopAt: L2, currentLevel: L1) lands on L2, never L3', () => {
  const r = escalationAfterAttempt({ attemptsAtLevel: 2, retriesPerLevel: 2, gateGreen: false, currentLevel: 'L1', stopAt: 'L2' });
  assert.equal(r.action, 'escalate');
  assert.equal(r.level, 'L2'); // proves stopAt is honoured, not a hardcoded 'L3'
  assert.notEqual(r.level, 'L3');
  assert.notEqual(r.action, 'l3_rung');
});

test('nextLevelCapped never skips a level and never exceeds an explicit ceiling', () => {
  assert.equal(nextLevelCapped('L0', 'L3'), 'L1'); // no skip: L0 → L1, never straight to L2/L3
  assert.equal(nextLevelCapped('L1', 'L1'), 'L1'); // already at the ceiling: stays put
});

// ── l3_mode: patch round trip (O10/R1) — tested separately from the 10 rule cases above ────────

test('l3_mode: patch round trip — first rung patches, a second rung on the same block stops', () => {
  const firstRung = escalationAfterAttempt({
    attemptsAtLevel: 2,
    retriesPerLevel: 2,
    gateGreen: false,
    currentLevel: 'L3',
    l3Mode: 'patch',
    l3RungAlreadyUsed: false,
  });
  assert.equal(firstRung.action, 'l3_rung');
  assert.equal(firstRung.mode, 'patch');

  const secondRung = escalationAfterAttempt({
    attemptsAtLevel: 2,
    retriesPerLevel: 2,
    gateGreen: false,
    currentLevel: 'L3',
    l3Mode: 'patch',
    l3RungAlreadyUsed: true, // the patch from firstRung was applied and the block failed again
  });
  assert.equal(secondRung.action, 'stop');
  assert.equal(secondRung.reason, 'l3_patch_exhausted');
});

// Fix round 1 finding: 'code' mode used to return {action:'escalate', level:'L3'} with NO bound —
// the caller resets its attempt counter, so rules 1/2 would fire again every retriesPerLevel
// failures forever, and 'stop_at: L3' would never actually stop anything. It now gets the SAME
// one-more-try-then-stop shape as patch mode, just without the patch machinery.
test('l3_mode: code keeps D4 literal for ONE more ceiling hit, then stops (fix round 1 — no more unbounded retries)', () => {
  const firstHit = escalationAfterAttempt({ attemptsAtLevel: 2, retriesPerLevel: 2, gateGreen: false, currentLevel: 'L3', l3Mode: 'code' });
  assert.equal(firstHit.action, 'escalate');
  assert.equal(firstHit.level, 'L3');
  assert.equal(firstHit.mode, undefined);

  const secondHit = escalationAfterAttempt({
    attemptsAtLevel: 2,
    retriesPerLevel: 2,
    gateGreen: false,
    currentLevel: 'L3',
    l3Mode: 'code',
    l3RungAlreadyUsed: true, // the block failed again after the one extra try firstHit granted
  });
  assert.equal(secondHit.action, 'stop');
  assert.equal(secondHit.reason, 'l3_code_exhausted');
});

test('escalationAfterAttempt requires currentLevel', () => {
  assert.throws(() => escalationAfterAttempt({}), TypeError);
});
