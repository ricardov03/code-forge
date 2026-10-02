/**
 * Escalation precedence — deterministic (plan §3.6, O10, R1; v1.3 adds rule 2b `review_stall`
 * and rule 6 `review_cap`, §4.11). "Evaluated in this order after every attempt; the first rule
 * that fires wins":
 *
 *   1. Attempt counter — attempts at the current level == `retries_per_level` (2) and not green
 *      ⇒ +1 level, whatever S1 said (`trigger: 'retries'`).
 *   2b. Review stall (v1.3) — a re-check round whose open `fix_now` set did NOT strictly shrink
 *      (`reviewRoundStalled` below) ⇒ +1 level IMMEDIATELY (`trigger: 'review_stall'`), checked
 *      BEFORE rule 2's own round-exhaustion counter — a block need not exhaust
 *      `review_rounds_per_level` to escalate on a stalled round.
 *   2c. Heavy rounds (B34, issue #2) — `escalation.after_rounds_with_warnings` (1) rounds at the
 *      current level whose open `fix_now` set held ≥ `escalation.warning_threshold` (2) warnings
 *      or any `critical` ⇒ +1 level (`trigger: 'review_warnings'`), checked after 2b and before 2.
 *      Below the running ceiling only: at L2 it does not fire (the L3 rung keeps its own
 *      triggers), so it never spends the rung on round 1.
 *   2. Review rounds — `review_rounds_per_level` (2) exhausted with an open `fix_now` finding
 *      ⇒ +1 level (`trigger: 'review_rounds'`).
 *   3. Security floor (DISPATCH time only) — `security_sensitive == true` ⇒ first-attempt level
 *      = `min(max(lane, L1) + 1, L2)` (`trigger: 'security'`). This is the one rule that fires
 *      before the first attempt, not after one — `dispatchLevel()` below is its own entry point,
 *      never folded into `escalationAfterAttempt()`.
 *   4. S2 ruling — S1 `next = escalate` is never acted on directly; it goes to S2 as a check
 *      (`trigger: 's2_ruling'`, `action: 's2_check'`). S1 `next = stop` routes straight to the
 *      human (`action: 'stop'`).
 *   5. Never skip a level; `stop_at: L3` is fixed. L3 is never a RUNNING level (R1: "the L3 rung
 *      is a patch, not bulk coding"; the block continues at L2): the running ceiling is
 *      `MAX_RUNNING_LEVEL` (L2), or `stop_at` when lower. When rule 1, 2b or 2 fires AND the block
 *      is already at the running ceiling, there IS no "+1 level" to go to. R1's L3 rung takes
 *      over instead: `escalation.l3_mode: 'patch'` (default) routes to a fresh L3
 *      session that returns a PATCH, never a whole feature (`action: 'l3_rung', mode: 'patch'`).
 *      `l3_mode: 'code'` keeps D4 literal — the block keeps coding at L3 ONCE more
 *      (`action: 'escalate', level: 'L3'`), no patch machinery. Either way, a SECOND ceiling hit
 *      on the same block (`l3RungAlreadyUsed`) always `stop`s (fix round 1: `l3_mode: 'code'`
 *      used to have no end state at all — the caller resets its attempt counter, so rules 1/2
 *      would fire again every `retriesPerLevel` failures forever, never honouring `stop_at`).
 *      A `stop_at` below L3 (schema-rejected; defensive) has no rung: at its ceiling ⇒ `stop`
 *      (`reason: 'stop_at'`).
 *   6. Review cap (v1.3) — `review.max_rounds_per_file` (default 4) reached on a file with an
 *      open `fix_now` finding ⇒ the L3 patch rung for THAT FILE, once (`trigger: 'review_cap'`,
 *      `action: 'l3_rung', mode: 'patch'` — always `'patch'`, regardless of `l3Mode`: a round-cap
 *      breach is a fix-scale problem, never a whole re-code). It shares the SAME
 *      `l3RungAlreadyUsed` flag as rule 5 — the plan's L3 rung is one per block, not one per
 *      trigger — so a second breach from EITHER rule 5 or rule 6 always `stop`s
 *      (`reason: 'review_cap'`). Checked last: it only matters when rules 1/2b/2/4 did not
 *      already return an action this attempt.
 *
 * Rules 1, 2b, 2, 4 and 6 are the six *triggered* rules of the precedence table (each carries its
 * own `trigger` name); rule 5 is the ceiling guard folded into `finalizeEscalate` below, not a
 * seventh trigger.
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
 * Block kinds (B34, issue #2). A `docs` or `contract` block is coded at `levels.coder_floor_docs`
 * (default L1) or above — never by L0 — and its review never runs the multimodel consensus unless
 * `review.multimodel_for_docs` is true.
 */
export const BLOCK_KINDS = Object.freeze(['code', 'docs', 'contract']);

/** Owned paths with one of these endings are docs (`.txt` is not: `requirements.txt`, `CMakeLists.txt`, `robots.txt` are code or config). */
export const DOCS_EXTENSIONS = Object.freeze(['.md', '.mdx', '.markdown', '.rst', '.adoc']);

/** Owned paths with one of these endings are contracts (schemas, IDLs, API descriptions). */
export const CONTRACT_SUFFIXES = Object.freeze(['.schema.json', '.schema.yaml', '.schema.yml', '.proto', '.graphql', '.gql', '.avsc', '.openapi.json', '.openapi.yaml', '.openapi.yml']);

/** A `.json`/`.yaml`/`.yml` path with one of these directory names is a contract too. */
const CONTRACT_DIRS = Object.freeze(['schema', 'schemas', 'contract', 'contracts']);

/**
 * The kind of ONE owned path (a glob is judged by its literal ending: `docs/**` is `code`, a
 * block that owns it declares its kind).
 * @param {string} file
 * @returns {'code'|'docs'|'contract'}
 */
export function fileKind(file) {
  const lower = String(file).toLowerCase();
  if (CONTRACT_SUFFIXES.some((s) => lower.endsWith(s))) return 'contract';
  if (/\.(json|ya?ml)$/.test(lower) && lower.split('/').slice(0, -1).some((d) => CONTRACT_DIRS.includes(d))) return 'contract';
  if (DOCS_EXTENSIONS.some((e) => lower.endsWith(e))) return 'docs';
  return 'code';
}

/**
 * A block's kind: the declared `kind` when it is one of `BLOCK_KINDS` (a declaration wins, so a
 * block can also declare `code`); else `docs` when every owned path is docs, `contract` when every
 * owned path is docs or contract and at least one is a contract, else `code` (no owned path ⇒ `code`).
 * @param {{owned?: ReadonlyArray<string>, declared?: unknown}} opts
 * @returns {'code'|'docs'|'contract'}
 */
export function blockKind({ owned = [], declared } = {}) {
  if (typeof declared === 'string' && BLOCK_KINDS.includes(declared)) return /** @type {'code'|'docs'|'contract'} */ (declared);
  if (owned.length === 0) return 'code';
  const kinds = owned.map(fileKind);
  if (kinds.every((k) => k === 'docs')) return 'docs';
  if (kinds.every((k) => k !== 'code')) return 'contract';
  return 'code';
}

/** @param {unknown} kind @returns {boolean} true for a `docs` or `contract` block (both get the coder floor and no multimodel review by default). */
export function isDocsKind(kind) {
  return kind === 'docs' || kind === 'contract';
}

/**
 * The lowest level a block of `kind` may be coded at: `levels.coder_floor_docs` (default L1) for
 * a docs/contract block, else L0.
 * @param {unknown} kind @param {Record<string, any> | undefined} [cfg]
 * @returns {string}
 */
export function coderFloor(kind, cfg) {
  if (!isDocsKind(kind)) return 'L0';
  const floor = cfg?.levels?.coder_floor_docs;
  return typeof floor === 'string' && ['L0', 'L1', 'L2'].includes(floor) ? floor : 'L1';
}

/**
 * Rule 3 (and the B34 docs floor), at dispatch time only — call this once per block, before the
 * first attempt. Both floors apply: the level is `max(security floor, docs floor)` (the lane when
 * neither raises it). The trigger names the floor that set the level — `security` when the
 * security floor reaches it, else `docs_floor`.
 * @param {{lane: string, securitySensitive?: boolean, kind?: unknown, cfg?: Record<string, any>}} ctx
 * @returns {{level: string, trigger: 'security'|'docs_floor'|null}}
 */
export function dispatchLevel({ lane, securitySensitive = false, kind = 'code', cfg }) {
  const security = securitySensitive ? securityFloorLevel(lane) : lane;
  const docs = coderFloor(kind, cfg);
  if (levelIndex(docs) > levelIndex(security)) return { level: docs, trigger: 'docs_floor' };
  return { level: security, trigger: securitySensitive ? 'security' : null };
}

/**
 * B34: a review round is "heavy" when its open `fix_now` set holds at least `threshold` warnings
 * or any `critical` finding. A threshold that is not an integer ≥ 1 is the default 2; findings
 * that are not an array are not heavy.
 * @param {unknown} findings @param {unknown} [threshold] - `escalation.warning_threshold`, default 2.
 * @returns {boolean}
 */
export function heavyRound(findings, threshold = 2) {
  if (!Array.isArray(findings)) return false;
  const min = Number.isInteger(threshold) && /** @type {number} */ (threshold) >= 1 ? /** @type {number} */ (threshold) : 2;
  if (findings.some((f) => f?.severity === 'critical')) return true;
  return findings.filter((f) => f?.severity === 'warning').length >= min;
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
 * Rule 2b's shrink test (v1.3, §4.11): a re-check round's open `fix_now` count must be STRICTLY
 * less than the previous round's. Equal or larger ⇒ the round stalled.
 * @param {number} openBefore - open `fix_now` count entering this round.
 * @param {number} openAfter - open `fix_now` count after this round's fixes + recheck.
 * @returns {boolean} true when the round did NOT strictly shrink (a stall).
 */
export function reviewRoundStalled(openBefore, openAfter) {
  return openAfter >= openBefore;
}

/**
 * The highest level a block CODES at (R1, D4, §0.7: "the L3 rung is a patch, not bulk coding";
 * §3.6: after the patch "the block continues at L2"). L3 is never a running level — it is only
 * the one-shot rung that `finalizeEscalate` returns once the block is at this ceiling.
 */
export const MAX_RUNNING_LEVEL = 'L2';

/**
 * @param {string} stopAt - `escalation.stop_at`.
 * @returns {string} the running ceiling: `MAX_RUNNING_LEVEL`, lowered to `stopAt` when that is
 *   lower (defensive — B1's schema fixes `stop_at` to `'L3'`).
 */
export function runningCeiling(stopAt = 'L3') {
  return LEVEL_ORDER[Math.min(levelIndex(MAX_RUNNING_LEVEL), levelIndex(stopAt))];
}

/**
 * Rules 1, 2b and 2 share this: below the running ceiling ⇒ +1 level (never to L3); at the
 * running ceiling ⇒ rule 5, the block's one L3 rung (R1), then `stop`.
 * @param {'retries'|'review_rounds'|'review_stall'|'review_warnings'} trigger
 * @param {string} currentLevel
 * @param {string} stopAt
 * @param {'patch'|'code'} l3Mode
 * @param {boolean} l3RungAlreadyUsed - the L3 rung was already used once on THIS block, in
 *   EITHER mode and from either rule 5 or rule 6 (one rung per block, not per trigger). A second
 *   hit always stops.
 * @returns {EscalationResult}
 */
function finalizeEscalate(trigger, currentLevel, stopAt, l3Mode, l3RungAlreadyUsed) {
  const ceiling = runningCeiling(stopAt);
  if (levelIndex(currentLevel) < levelIndex(ceiling)) {
    return { trigger, action: 'escalate', level: nextLevelCapped(currentLevel, ceiling) };
  }
  // At (or, after an `l3_mode: code` turn, above) the running ceiling: rule 5.
  if (stopAt !== 'L3') {
    // `stop_at` below L3 forbids any L3 session, so there is no rung to take (§3.6 fixes
    // `stop_at: L3`; this only guards a hand-built ctx).
    return { trigger, action: 'stop', reason: 'stop_at' };
  }
  if (l3RungAlreadyUsed) {
    return { trigger, action: 'stop', reason: l3Mode === 'code' ? 'l3_code_exhausted' : 'l3_patch_exhausted' };
  }
  if (l3Mode === 'code') {
    // D4 literal: ONE coding turn at L3 (the caller resets its attempt counter); the next
    // ceiling hit falls into the `l3RungAlreadyUsed` branch above and stops.
    return { trigger, action: 'escalate', level: 'L3' };
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
 * @property {boolean} [reviewStall] - rule 2b (v1.3, §4.11): this round's open `fix_now` set did
 *   NOT strictly shrink (see `reviewRoundStalled`). Checked, and fires, before rule 2's own
 *   `reviewRoundsAtLevel` counter is exhausted.
 * @property {'complete'|'retry'|'escalate'|'stop'|undefined} [s1Next] - S1's `next` answer.
 * @property {string} [currentLevel] - REQUIRED at runtime (checked below, throws when absent);
 *   optional in this typedef only so a deliberately-invalid `{}` type-checks in a test that
 *   proves the runtime check fires.
 * @property {string} [stopAt] - `escalation.stop_at`, default `'L3'`.
 * @property {'patch'|'code'} [l3Mode] - `escalation.l3_mode`, default `'patch'` (R1).
 * @property {boolean} [l3RungAlreadyUsed] - the L3 rung (ceiling, rule 5, OR the file's review
 *   cap, rule 6) was already used once before on THIS block, from either source. A second hit
 *   always `stop`s — the plan's L3 rung is one per block, not one per trigger.
 * @property {number} [roundsAtFile] - rule 6 (v1.3, §4.11): total review rounds run on this file
 *   so far, counted across every level — unlike `reviewRoundsAtLevel`, this is never reset by a
 *   level escalation.
 * @property {number} [maxRoundsPerFile] - `review.max_rounds_per_file`, default 4 (schema
 *   minimum 2, maximum 6; B1.1).
 * @property {number} [warningRoundsAtLevel] - rule 2c (B34): heavy rounds (`heavyRound`) at the
 *   current level, this one included.
 * @property {number} [afterRoundsWithWarnings] - `escalation.after_rounds_with_warnings`, default
 *   1; 0 turns rule 2c off.
 *
 * @typedef {object} EscalationResult
 * @property {'retries'|'review_stall'|'review_warnings'|'review_rounds'|'s2_ruling'|'review_cap'|null} trigger
 * @property {'escalate'|'s2_check'|'stop'|'l3_rung'|'none'} action
 * @property {string} [level]
 * @property {string} [mode]
 * @property {string} [reason]
 */

/**
 * Rules 1, 2b, 2c, 2, 4 and 6 (rule 3 is `dispatchLevel`; rule 5 is the ceiling guard inside
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
    reviewStall = false,
    s1Next,
    currentLevel,
    stopAt = 'L3',
    l3Mode = 'patch',
    l3RungAlreadyUsed = false,
    roundsAtFile = 0,
    maxRoundsPerFile = 4,
    warningRoundsAtLevel = 0,
    afterRoundsWithWarnings = 1,
  } = ctx;

  if (typeof currentLevel !== 'string') {
    throw new TypeError('escalationAfterAttempt: ctx.currentLevel is required');
  }

  // Rule 1: attempt counter.
  if (attemptsAtLevel >= retriesPerLevel && !gateGreen) {
    return finalizeEscalate('retries', currentLevel, stopAt, l3Mode, l3RungAlreadyUsed);
  }
  // Rule 2b: review stall — fires immediately, ahead of rule 2's own round-exhaustion counter.
  if (reviewStall && openFixNowFinding) {
    return finalizeEscalate('review_stall', currentLevel, stopAt, l3Mode, l3RungAlreadyUsed);
  }
  // Rule 2c (B34): heavy review rounds — below the running ceiling only (a cheap coder climbs at
  // once; at L2 the L3 rung keeps its own triggers, rules 2b/2/6).
  if (
    afterRoundsWithWarnings > 0 &&
    warningRoundsAtLevel >= afterRoundsWithWarnings &&
    openFixNowFinding &&
    levelIndex(currentLevel) < levelIndex(runningCeiling(stopAt))
  ) {
    return finalizeEscalate('review_warnings', currentLevel, stopAt, l3Mode, l3RungAlreadyUsed);
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
  // Rule 6: review cap — the file's absolute round count, independent of the escalation level.
  // Always `patch` (never `l3Mode: 'code'`): a round-cap breach is a fix-scale problem. Shares
  // `l3RungAlreadyUsed` with rule 5's ceiling guard (one L3 rung per block, not per trigger).
  if (roundsAtFile >= maxRoundsPerFile && openFixNowFinding) {
    if (l3RungAlreadyUsed) {
      return { trigger: 'review_cap', action: 'stop', reason: 'review_cap' };
    }
    return { trigger: 'review_cap', action: 'l3_rung', mode: 'patch' };
  }
  // No rule fired this attempt.
  return { trigger: null, action: 'none' };
}
