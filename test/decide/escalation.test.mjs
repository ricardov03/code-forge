/**
 * Escalation precedence (plan §3.6, O10, R1; v1.3 adds rule 2b `review_stall` and rule 6
 * `review_cap`, §4.11) — "6 rules × 2 cases = 12 tests" (fires / falls through, for each of the
 * six *triggered* rules: retries, review_stall, review_rounds, security, s2_ruling, review_cap —
 * rule 5, the ceiling guard, carries no trigger of its own and is exercised separately), plus the
 * `l3_mode: patch` round trip (O10/R1) and the `review_cap` round trip (v1.3), each tested
 * separately from the 12.
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  dispatchLevel,
  escalationAfterAttempt,
  LEVEL_ORDER,
  levelIndex,
  mustRouteNextThroughEscalation,
  MAX_RUNNING_LEVEL,
  nextLevelCapped,
  reviewRoundStalled,
  runningCeiling,
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

// ── Rule 2b: review stall (v1.3, §4.11) — checked BEFORE rule 2's own counter ───────────────────

test('reviewRoundStalled: 3 → 3 stalls, 3 → 2 does not (§4.11 shrink test)', () => {
  assert.equal(reviewRoundStalled(3, 3), true);
  assert.equal(reviewRoundStalled(3, 2), false);
});

test('rule 2b FIRES: an open fix_now set that did not shrink escalates immediately, PRE-EMPTING rule 2 — reviewRoundsAtLevel is still below its own limit here', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 0,
    reviewRoundsAtLevel: 1,
    reviewRoundsPerLevel: 2, // rule 2's own counter is NOT exhausted (1 < 2) — proves precedence
    reviewStall: true,
    openFixNowFinding: true,
    currentLevel: 'L1',
  });
  assert.equal(r.trigger, 'review_stall');
  assert.equal(r.action, 'escalate');
  assert.equal(r.level, 'L2');
});

test('rule 2b PRE-EMPTS rule 2 even when BOTH triggers hold: reviewRoundsAtLevel is also exhausted here, and review_stall still wins by precedence', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 0,
    reviewRoundsAtLevel: 2,
    reviewRoundsPerLevel: 2, // exhausted — rule 2 alone would also fire here
    reviewStall: true,
    openFixNowFinding: true,
    currentLevel: 'L1',
  });
  assert.equal(r.trigger, 'review_stall'); // not 'review_rounds' — this is what actually proves order
  assert.equal(r.action, 'escalate');
  assert.equal(r.level, 'L2');
});

test('rule 2b does NOT fire: the open fix_now set strictly shrank this round', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 0,
    reviewRoundsAtLevel: 1,
    reviewRoundsPerLevel: 2,
    reviewStall: reviewRoundStalled(3, 2), // false — shrank 3 → 2
    openFixNowFinding: true,
    currentLevel: 'L1',
  });
  assert.notEqual(r.trigger, 'review_stall');
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

// ── Rule 5: never skip a level; L3 is never a RUNNING level (R1, B3.2) ───────────────────────────
//
// The running ceiling is L2 (or stop_at when lower). Rules 1/2b/2 at that ceiling take the block's
// one L3 rung — a patch (default) or one L3 coding turn (`l3_mode: code`) — never "+1 to L3".

test('runningCeiling: L2 by default, lowered to stop_at when stop_at is lower', () => {
  assert.equal(MAX_RUNNING_LEVEL, 'L2');
  assert.equal(runningCeiling('L3'), 'L2');
  assert.equal(runningCeiling(), 'L2');
  assert.equal(runningCeiling('L1'), 'L1');
});

test('rule 5 FIRES: L2 + rule-2 trigger ⇒ the L3 patch rung, NOT escalate to L3', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 0,
    reviewRoundsAtLevel: 2,
    reviewRoundsPerLevel: 2,
    openFixNowFinding: true,
    currentLevel: 'L2',
  });
  assert.deepEqual(r, { trigger: 'review_rounds', action: 'l3_rung', mode: 'patch' });
});

test('rule 5 FIRES for rules 1 and 2b too: L2 + retries / review_stall ⇒ the L3 patch rung', () => {
  const retries = escalationAfterAttempt({ attemptsAtLevel: 2, retriesPerLevel: 2, gateGreen: false, currentLevel: 'L2' });
  assert.deepEqual(retries, { trigger: 'retries', action: 'l3_rung', mode: 'patch' });
  const stall = escalationAfterAttempt({ reviewStall: true, openFixNowFinding: true, currentLevel: 'L2' });
  assert.deepEqual(stall, { trigger: 'review_stall', action: 'l3_rung', mode: 'patch' });
});

test('rule 5 does NOT fire: L1 + rule-2 trigger ⇒ escalate to L2 (below the running ceiling)', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 0,
    reviewRoundsAtLevel: 2,
    reviewRoundsPerLevel: 2,
    openFixNowFinding: true,
    currentLevel: 'L1',
  });
  assert.deepEqual(r, { trigger: 'review_rounds', action: 'escalate', level: 'L2' });
});

test('stop_at: L1 ⇒ the ceiling is L1 and there is no rung: L0 escalates to L1, L1 stops', () => {
  const below = escalationAfterAttempt({ attemptsAtLevel: 2, retriesPerLevel: 2, gateGreen: false, currentLevel: 'L0', stopAt: 'L1' });
  assert.deepEqual(below, { trigger: 'retries', action: 'escalate', level: 'L1' });
  const at = escalationAfterAttempt({ attemptsAtLevel: 2, retriesPerLevel: 2, gateGreen: false, currentLevel: 'L1', stopAt: 'L1' });
  assert.deepEqual(at, { trigger: 'retries', action: 'stop', reason: 'stop_at' });
});

test('nextLevelCapped never skips a level and never exceeds an explicit ceiling', () => {
  assert.equal(nextLevelCapped('L0', 'L3'), 'L1'); // no skip: L0 → L1, never straight to L2/L3
  assert.equal(nextLevelCapped('L1', 'L1'), 'L1'); // already at the ceiling: stays put
});

// ── l3_mode: patch round trip (O10/R1) — tested separately from the rule cases above ───────────

test('l3_mode: patch round trip at L2 — the first hit is the rung, a second hit after the rung stops', () => {
  const firstRung = escalationAfterAttempt({
    attemptsAtLevel: 2,
    retriesPerLevel: 2,
    gateGreen: false,
    currentLevel: 'L2',
    l3Mode: 'patch',
    l3RungAlreadyUsed: false,
  });
  assert.deepEqual(firstRung, { trigger: 'retries', action: 'l3_rung', mode: 'patch' });

  const secondRung = escalationAfterAttempt({
    attemptsAtLevel: 2,
    retriesPerLevel: 2,
    gateGreen: false,
    currentLevel: 'L2', // the block continued at L2 after the patch (§3.6)
    l3Mode: 'patch',
    l3RungAlreadyUsed: true, // the patch from firstRung was applied and the block failed again
  });
  assert.deepEqual(secondRung, { trigger: 'retries', action: 'stop', reason: 'l3_patch_exhausted' });
});

// Fix round 1 finding: 'code' mode used to have no bound. It gets the SAME one-rung-then-stop shape
// as patch mode: ONE L3 coding turn from the L2 ceiling, then a second hit stops.
test('l3_mode: code — L2 + trigger ⇒ ONE L3 coding turn; a second hit after it stops', () => {
  // `l3_mode: code` is the plan's explicit opt-in (§3.6 "keeps D4 literal"): the rung is ONE L3 turn, not a +1 running level.
  const firstHit = escalationAfterAttempt({ attemptsAtLevel: 2, retriesPerLevel: 2, gateGreen: false, currentLevel: 'L2', l3Mode: 'code' });
  assert.deepEqual(firstHit, { trigger: 'retries', action: 'escalate', level: 'L3' });

  const secondHit = escalationAfterAttempt({
    attemptsAtLevel: 2,
    retriesPerLevel: 2,
    gateGreen: false,
    currentLevel: 'L3',
    l3Mode: 'code',
    l3RungAlreadyUsed: true, // the block failed again during the one L3 turn firstHit granted
  });
  assert.deepEqual(secondHit, { trigger: 'retries', action: 'stop', reason: 'l3_code_exhausted' });
});

// ── Rule 6: review cap (v1.3, §4.11) — the file's absolute round count, checked last ────────────

test('rule 6 FIRES: review.max_rounds_per_file default (4) reached on a file with an open fix_now ⇒ the L3 patch rung', () => {
  const r = escalationAfterAttempt({
    attemptsAtLevel: 0,
    currentLevel: 'L1', // nowhere near the L3 ceiling — proves this is a separate trigger from rule 5
    roundsAtFile: 4,
    openFixNowFinding: true,
  });
  assert.equal(r.trigger, 'review_cap');
  assert.equal(r.action, 'l3_rung');
  assert.equal(r.mode, 'patch');
});

test('rule 6 does NOT fire: rounds below the cap, or the open fix_now set is already resolved', () => {
  const belowCap = escalationAfterAttempt({ attemptsAtLevel: 0, currentLevel: 'L1', roundsAtFile: 3, openFixNowFinding: true });
  assert.notEqual(belowCap.trigger, 'review_cap');
  assert.equal(belowCap.action, 'none');

  const resolved = escalationAfterAttempt({ attemptsAtLevel: 0, currentLevel: 'L1', roundsAtFile: 4, openFixNowFinding: false });
  assert.notEqual(resolved.trigger, 'review_cap');
  assert.equal(resolved.action, 'none');
});

test('rule 6 reads review.max_rounds_per_file rather than a hardcoded 4', () => {
  const belowConfiguredCap = escalationAfterAttempt({
    attemptsAtLevel: 0,
    currentLevel: 'L1',
    roundsAtFile: 4,
    maxRoundsPerFile: 6, // 4 < 6 — a hardcoded-4 implementation would wrongly fire here
    openFixNowFinding: true,
  });
  assert.equal(belowConfiguredCap.action, 'none');

  const atConfiguredCap = escalationAfterAttempt({
    attemptsAtLevel: 0,
    currentLevel: 'L1',
    roundsAtFile: 6,
    maxRoundsPerFile: 6,
    openFixNowFinding: true,
  });
  assert.equal(atConfiguredCap.trigger, 'review_cap');
  assert.equal(atConfiguredCap.action, 'l3_rung');
});

test('review_cap round trip: first breach patches once, a second breach on the same block stops (l3RungAlreadyUsed is shared with rule 5)', () => {
  const firstBreach = escalationAfterAttempt({ attemptsAtLevel: 0, currentLevel: 'L1', roundsAtFile: 4, openFixNowFinding: true, l3RungAlreadyUsed: false });
  assert.equal(firstBreach.action, 'l3_rung');
  assert.equal(firstBreach.mode, 'patch');

  const secondBreach = escalationAfterAttempt({
    attemptsAtLevel: 0,
    currentLevel: 'L1',
    roundsAtFile: 4,
    openFixNowFinding: true,
    l3RungAlreadyUsed: true, // the patch from firstBreach was applied and the file is still open
  });
  assert.equal(secondBreach.trigger, 'review_cap');
  assert.equal(secondBreach.action, 'stop');
  assert.equal(secondBreach.reason, 'review_cap');
});

test('escalationAfterAttempt requires currentLevel', () => {
  assert.throws(() => escalationAfterAttempt({}), TypeError);
});
