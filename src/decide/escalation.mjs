/**
 * Escalation precedence — deterministic (plan §3.6, O10, R1). "Evaluated in this order after
 * every attempt; the first rule that fires wins":
 *
 *   1. Attempt counter — attempts at the current level == `retries_per_level` (2) and not green
 *      ⇒ +1 level, whatever S1 said (`trigger: 'retries'`).
 *   2. Review rounds — `review_rounds_per_level` (2) exhausted with an open `fix_now` finding
 *      ⇒ +1 level (`trigger: 'review_rounds'`).
 *   3. Security floor (DISPATCH time only) — `security_sensitive == true` ⇒ first-attempt level
 *      = `min(max(lane, L1) + 1, L2)` (`trigger: 'security'`). This is the one rule that fires
 *      before the first attempt, not after one — `dispatchLevel()` below is its own entry point,
 *      never folded into `escalationAfterAttempt()`.
 *   4. S2 ruling — S1 `next = escalate` is never acted on directly; it goes to S2 as a check
 *      (`trigger: 's2_ruling'`, `action: 's2_check'`). S1 `next = stop` routes straight to the
 *      human (`action: 'stop'`).
 *   5. Never skip a level; `stop_at: L3` is fixed — when rule 1 or 2 fires AND the block is
 *      already at the ceiling (`currentLevel === stopAt`), there IS no "+1 level" to go to. R1's
 *      L3 rung takes over instead: `escalation.l3_mode: 'patch'` (default) routes to a fresh L3
 *      session that returns a PATCH, never a whole feature (`action: 'l3_rung', mode: 'patch'`).
 *      `l3_mode: 'code'` keeps D4 literal — the block keeps coding at L3 ONCE more
 *      (`action: 'escalate', level: 'L3'`), no patch machinery. Either way, a SECOND ceiling hit
 *      on the same block (`l3RungAlreadyUsed`) always `stop`s (fix round 1: `l3_mode: 'code'`
 *      used to have no end state at all — the caller resets its attempt counter, so rules 1/2
 *      would fire again every `retriesPerLevel` failures forever, never honouring `stop_at`).
 */

/** Level order, low to high. `escalation.stop_at` is schema-fixed to `'L3'` (B1's `checkStopAtNotL3`). */
export const LEVEL_ORDER = Object.freeze(['L0', 'L1', 'L2', 'L3']);

/**
 * @param {string} level
 * @returns {number}
 * @throws {TypeError} for anything not one of `LEVEL_ORDER`.
 */
export function levelIndex(level) {
  const i = LEVEL_ORDER.indexOf(level);
  if (i === -1) {
    throw new TypeError(`levelIndex: unknown level ${JSON.stringify(level)} (expected one of ${LEVEL_ORDER.join(', ')})`);
  }
  return i;
}

/**
 * @param {string} current
 * @param {string} [stopAt]
 * @returns {string} `current`'s successor, capped at `stopAt` — never skips a level, never
 *   exceeds the ceiling (rule 5's "never skip a level" half; the "at the ceiling" half lives in
 *   `escalationAfterAttempt`'s `finalizeEscalate`).
 */
export function nextLevelCapped(current, stopAt = 'L3') {
  const capIndex = levelIndex(stopAt);
  return LEVEL_ORDER[Math.min(levelIndex(current) + 1, capIndex)];
}

/**
 * Rule 3's arithmetic: `min(max(lane, L1) + 1, L2)`.
 * @param {string} lane - `'L0'|'L1'|'L2'` (§O10: "L3 is not a lane").
 * @returns {string}
 */
export function securityFloorLevel(lane) {
  const idx = Math.min(Math.max(levelIndex(lane), levelIndex('L1')) + 1, levelIndex('L2'));
  return LEVEL_ORDER[idx];
}

/**
 * Rule 3, at dispatch time only — call this once per block, before the first attempt.
 * @param {{lane: string, securitySensitive: boolean}} ctx
 * @returns {{level: string, trigger: 'security'|null}}
 */
export function dispatchLevel({ lane, securitySensitive }) {
  if (securitySensitive) {
    return { level: securityFloorLevel(lane), trigger: 'security' };
  }
  return { level: lane, trigger: null };
}

/**
 * Rule 4's floor: `next = 'escalate'` (and `'stop'`) must never be acted on directly by the
 * orchestrator — both always route through this module's rule 4, never straight to "continue as
 * S1 said" even when `thresholds.decide()` reported `decided: true` for the `next` question.
 * @param {string} next
 * @returns {boolean}
 */
export function mustRouteNextThroughEscalation(next) {
  return next === 'escalate' || next === 'stop';
}

/**
 * @param {'retries'|'review_rounds'} trigger
 * @param {string} currentLevel
 * @param {string} stopAt
 * @param {'patch'|'code'} l3Mode
 * @param {boolean} l3RungAlreadyUsed - the ceiling was already hit once before on THIS block,
 *   in EITHER mode. A second hit always stops (fix round 1: `l3_mode: 'code'` used to have no
 *   end state — `stop_at: L3` must mean something regardless of mode).
 * @returns {EscalationResult}
 */
function finalizeEscalate(trigger, currentLevel, stopAt, l3Mode, l3RungAlreadyUsed) {
  if (currentLevel !== stopAt) {
    return { trigger, action: 'escalate', level: nextLevelCapped(currentLevel, stopAt) };
  }
  // At the ceiling: rule 5 — there is no level above `stopAt` to escalate to.
  if (l3RungAlreadyUsed) {
    return { trigger, action: 'stop', reason: l3Mode === 'code' ? 'l3_code_exhausted' : 'l3_patch_exhausted' };
  }
  if (l3Mode === 'code') {
    // D4 literal: the block keeps coding at L3 ONCE more (the caller resets its attempt counter)
    // — a second ceiling hit falls into the `l3RungAlreadyUsed` branch above and stops.
    return { trigger, action: 'escalate', level: stopAt };
  }
  return { trigger, action: 'l3_rung', mode: 'patch' };
}

/**
 * @typedef {object} EscalationCtx
 * @property {number} [attemptsAtLevel]
 * @property {number} [retriesPerLevel] - `escalation.retries_per_level`, default 2.
 * @property {boolean} [gateGreen] - whether the block's gate is green on this attempt.
 * @property {number} [reviewRoundsAtLevel]
 * @property {number} [reviewRoundsPerLevel] - `escalation.review_rounds_per_level`, default 2.
 * @property {boolean} [openFixNowFinding] - an unresolved `fix_now` finding remains open.
 * @property {'complete'|'retry'|'escalate'|'stop'|undefined} [s1Next] - S1's `next` answer.
 * @property {string} [currentLevel] - REQUIRED at runtime (checked below, throws when absent);
 *   optional in this typedef only so a deliberately-invalid `{}` type-checks in a test that
 *   proves the runtime check fires.
 * @property {string} [stopAt] - `escalation.stop_at`, default `'L3'`.
 * @property {'patch'|'code'} [l3Mode] - `escalation.l3_mode`, default `'patch'` (R1).
 * @property {boolean} [l3RungAlreadyUsed] - the ceiling (`currentLevel === stopAt`) was already
 *   hit once before on THIS block, in either `l3_mode`. A second hit always `stop`s.
 *
 * @typedef {object} EscalationResult
 * @property {'retries'|'review_rounds'|'s2_ruling'|null} trigger
 * @property {'escalate'|'s2_check'|'stop'|'l3_rung'|'none'} action
 * @property {string} [level]
 * @property {string} [mode]
 * @property {string} [reason]
 */

/**
 * Rules 1, 2 and 4 (rule 3 is `dispatchLevel`; rule 5 is the ceiling guard inside
 * `finalizeEscalate`), evaluated in the documented order — the first rule that fires wins.
 * @param {EscalationCtx} ctx
 * @returns {EscalationResult}
 */
export function escalationAfterAttempt(ctx) {
  const {
    attemptsAtLevel = 0,
    retriesPerLevel = 2,
    gateGreen = false,
    reviewRoundsAtLevel = 0,
    reviewRoundsPerLevel = 2,
    openFixNowFinding = false,
    s1Next,
    currentLevel,
    stopAt = 'L3',
    l3Mode = 'patch',
    l3RungAlreadyUsed = false,
  } = ctx;

  if (typeof currentLevel !== 'string') {
    throw new TypeError('escalationAfterAttempt: ctx.currentLevel is required');
  }

  // Rule 1: attempt counter.
  if (attemptsAtLevel >= retriesPerLevel && !gateGreen) {
    return finalizeEscalate('retries', currentLevel, stopAt, l3Mode, l3RungAlreadyUsed);
  }
  // Rule 2: review rounds.
  if (reviewRoundsAtLevel >= reviewRoundsPerLevel && openFixNowFinding) {
    return finalizeEscalate('review_rounds', currentLevel, stopAt, l3Mode, l3RungAlreadyUsed);
  }
  // Rule 4: S2 ruling.
  if (s1Next === 'escalate') {
    return { trigger: 's2_ruling', action: 's2_check' };
  }
  if (s1Next === 'stop') {
    return { trigger: 's2_ruling', action: 'stop' };
  }
  // No rule fired this attempt.
  return { trigger: null, action: 'none' };
}
