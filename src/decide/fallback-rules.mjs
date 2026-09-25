/**
 * Fallback when Jev is unavailable (plan §3.5, O9/O27) — `system1.fallback`, default `'rules'`.
 * Layer 1 of that fallback: deterministic rules for `lane`, `risk` and `security_sensitive` (ALL
 * THREE are always resolvable this way — "a rule that fires is final", so these three question
 * ids never sit in the residue), and for `next` only "when the evidence is unambiguous". The
 * plan's residue set — the questions rules cannot cover: `defect`, `resolved`, an AMBIGUOUS
 * `next`, and `scope` — goes to S2 instead, tagged `source: 's2-fallback'` so §3.4's
 * `> 6 non-fallback S2 calls per block ⇒ s2-heavy` counter never counts it.
 *
 * **Rule inputs are pre-computed facts, not raw diffs.** This module does no path-globbing, no
 * git diffing, no gate-output parsing — that belongs to the caller (B5's gate/scope machinery,
 * B9's evidence normalizer), which already has to do it anyway for the deterministic gate itself.
 * Keeping these functions pure over a small fact object keeps them exhaustively unit-testable and
 * decoupled from any one block's I/O.
 */

/** Keywords the plan names for the fallback rules (§3.5): "Money, Policy, Auth, Webhook, migration". */
export const FALLBACK_KEYWORDS = Object.freeze(['Money', 'Policy', 'Auth', 'Webhook', 'migration']);

/** Diff-size floor above which a change is at least L1 (§3.5). */
export const LANE_SIZE_FILES_FLOOR = 4;
export const LANE_SIZE_LINES_FLOOR = 200;

/** `> 6 non-fallback S2 calls per block ⇒ s2-heavy` (§3.4). */
export const S2_HEAVY_THRESHOLD = 6;

/**
 * @typedef {object} FallbackFacts
 * @property {boolean} [pathFloorHit] - a changed path matches `proof.tiers.high.paths` (§3.5:
 *   "Path floors ... ⇒ risk 3, security true").
 * @property {number} [filesChanged]
 * @property {number} [linesAdded]
 * @property {boolean} [touchesMigration]
 * @property {boolean} [touchesPolicyOrMiddleware]
 * @property {string[]} [keywordsFound] - any of `FALLBACK_KEYWORDS` found in the task text/diff/paths.
 */

/**
 * §3.5, verbatim: "diff size (> 200 added lines or > 4 files ⇒ **≥ L1**)". The "≥" only means
 * something if the baseline BELOW that floor is lower than L1 — so a change that trips neither
 * the migration/policy/path floor NOR the size floor is `'L0'` (trivial), and the size floor's
 * whole job is to bump an otherwise-untagged-but-sizeable diff up to `'L1'` (fix round 1: the old
 * version returned `'L1'` on BOTH the size branch and the fall-through default, making the size
 * floor dead code and `'L0'` unreachable despite the JSDoc return type listing it).
 * @param {FallbackFacts} facts
 * @returns {'L0'|'L1'|'L2'} never `null` — lane is always rule-covered in fallback mode.
 */
export function fallbackLane(facts = {}) {
  if (facts.touchesMigration || facts.touchesPolicyOrMiddleware || facts.pathFloorHit) {
    return 'L2';
  }
  if ((facts.filesChanged ?? 0) > LANE_SIZE_FILES_FLOOR || (facts.linesAdded ?? 0) > LANE_SIZE_LINES_FLOOR) {
    return 'L1';
  }
  return 'L0';
}

/**
 * @param {FallbackFacts} facts
 * @returns {0|1|2|3} never `null` — risk is always rule-covered in fallback mode.
 */
export function fallbackRisk(facts = {}) {
  if (facts.pathFloorHit) return 3;
  if (facts.touchesMigration || facts.touchesPolicyOrMiddleware) return 2;
  if ((facts.filesChanged ?? 0) > LANE_SIZE_FILES_FLOOR || (facts.linesAdded ?? 0) > LANE_SIZE_LINES_FLOOR) return 1;
  return 0;
}

/** Keywords whose presence, by themselves, mean the change touches a security-sensitive domain. */
const SECURITY_KEYWORDS = Object.freeze(['Auth', 'Webhook', 'Money']);

/**
 * @param {FallbackFacts} facts
 * @returns {boolean} never `null` — security_sensitive is always rule-covered in fallback mode.
 */
export function fallbackSecuritySensitive(facts = {}) {
  if (facts.pathFloorHit) return true;
  return (facts.keywordsFound ?? []).some((k) => SECURITY_KEYWORDS.includes(k));
}

/**
 * Resolve the three ALWAYS-covered dispatch questions by rules alone — every entry is
 * `source: 'rules'`, so a caller counting S2 calls over the result gets exactly 0.
 * @param {FallbackFacts} facts
 * @returns {{lane: {value: string, source: 'rules'}, risk: {value: number, source: 'rules'}, security_sensitive: {value: boolean, source: 'rules'}}}
 */
export function resolveDispatchQuestionsByRules(facts = {}) {
  return {
    lane: { value: fallbackLane(facts), source: 'rules' },
    risk: { value: fallbackRisk(facts), source: 'rules' },
    security_sensitive: { value: fallbackSecuritySensitive(facts), source: 'rules' },
  };
}

/**
 * @typedef {object} NextEvidence
 * @property {boolean} allGreen - every gate (tests/lint/types/format) passed.
 * @property {number} attempt
 * @property {boolean} [spreading] - failures grew across attempts, or span more than one file/area.
 * @property {boolean} [newTestsAdded]
 * @property {string} [noNewTestsReason]
 */

/**
 * `next`, "when the evidence is unambiguous" (§3.5) — the one rule-covered question that CAN be
 * residue: an ambiguous case (spreading failures, or a later attempt with no clear single cause)
 * returns `null`, and the caller must route it to S2 instead (`source: 's2-fallback'`).
 * @param {NextEvidence} evidence
 * @returns {'complete'|'retry'|null}
 */
export function fallbackNext(evidence) {
  if (evidence.allGreen && (evidence.newTestsAdded || Boolean(evidence.noNewTestsReason))) {
    return 'complete';
  }
  if (!evidence.allGreen && !evidence.spreading && (evidence.attempt ?? 1) <= 1) {
    return 'retry';
  }
  return null;
}

/**
 * @param {NextEvidence} evidence
 * @returns {{value: 'complete'|'retry', source: 'rules'} | {value: null, source: 's2-fallback'}}
 */
export function resolveNextByRules(evidence) {
  const value = fallbackNext(evidence);
  return value === null ? { value: null, source: 's2-fallback' } : { value, source: 'rules' };
}

/**
 * `defect`, `resolved` and `scope` have no rule at all — they are ALWAYS residue in fallback mode.
 * @param {string} _id - one of `'defect'|'resolved'|'scope'`; accepted as a plain `string` (not
 *   narrowed further) for call-site clarity/documentation only — this function ignores it.
 * @returns {{value: null, source: 's2-fallback'}}
 */
export function alwaysResidue(_id) {
  return { value: null, source: 's2-fallback' };
}

/**
 * How many of `entries` actually require an S2 call — anything whose `source` isn't `'rules'`
 * (a floor/keyword/size rule fired and answered outright) needs S2, whether that S2 call is a
 * fallback residue (`'s2-fallback'`) or, later, an ordinary band check (`'s2'`). This is the
 * literal "S2 call count" the plan's acceptance clause names: a batch resolved entirely by rules
 * (the floor path) counts 0; a batch with one residue entry (e.g. an ambiguous `next`) counts 1.
 * @param {{source: string}[]} entries
 * @returns {number}
 */
export function countS2Calls(entries) {
  return entries.filter((entry) => entry.source !== 'rules').length;
}

/**
 * The `s2-heavy` counter (§3.4) counts only NON-fallback S2 calls — a `source: 's2-fallback'`
 * call is exempt (§3.5): Jev being down for the whole run must not itself trip a "this block is
 * asking S2 too much" flag.
 * @param {string} source
 * @returns {boolean}
 */
export function countsTowardS2Heavy(source) {
  return source === 's2' || source === 's2-check' || source === 's2-decide';
}

/**
 * @param {{source: string}[]} s2Calls - every S2 invocation recorded for one block, in order.
 * @param {number} [threshold] - default `S2_HEAVY_THRESHOLD` (6).
 * @returns {{count: number, heavy: boolean}} `count` is the NON-fallback tally only.
 */
export function evaluateS2Heavy(s2Calls, threshold = S2_HEAVY_THRESHOLD) {
  const count = s2Calls.filter((call) => countsTowardS2Heavy(call.source)).length;
  return { count, heavy: count > threshold };
}
