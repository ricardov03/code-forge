/**
 * Calibration (`code-forge ledger calibration`, plan §6.3): per-question reliability buckets ×
 * outcome, plus the shadow-sampling gate that feeds one of the four ground-truth callers.
 *
 * Bucket edges (8): a `noul` confidence is `max(p, 1-p)`, never below 0.5, so the table spans
 * [0.5, 1.0]. The last bucket is CLOSED at 1.0 so Jev's `1.00` lane answers land inside it (plan
 * §3.3: "Jev's 1.00 lane answers are tracked in the [0.99, 1.0] bucket"). Confidence is rounded to
 * 6 decimal places before bucketing (fix round 1) — a value meant to sit exactly on an edge (e.g.
 * `max(p, 1-p)` computing 0.85) can arrive as `0.8499999999999999` from float arithmetic and would
 * otherwise land one bucket low.
 */

export const CALIBRATION_BUCKETS = Object.freeze([
  { id: '0.50-0.60', min: 0.5, max: 0.6 },
  { id: '0.60-0.70', min: 0.6, max: 0.7 },
  { id: '0.70-0.80', min: 0.7, max: 0.8 },
  { id: '0.80-0.85', min: 0.8, max: 0.85 },
  { id: '0.85-0.90', min: 0.85, max: 0.9 },
  { id: '0.90-0.95', min: 0.9, max: 0.95 },
  { id: '0.95-0.99', min: 0.95, max: 0.99 },
  { id: '0.99-1.00', min: 0.99, max: 1.0, closed: true },
]);

export const OUTCOMES = Object.freeze(['correct', 'wrong', 'unknown']);

/** Ledger vocabularies map onto the 3-outcome grid explicitly — a `reverted` mark (from
 * `--scan-git`) is ground truth that the original call was WRONG, not an unlabelled unknown. */
const OUTCOME_MAP = Object.freeze({ correct: 'correct', wrong: 'wrong', reverted: 'wrong', unknown: 'unknown' });

const ROUND_TO = 1e6;
const roundConfidence = (c) => Math.round(c * ROUND_TO) / ROUND_TO;

/** @param {number} confidence @returns {{id: string}|null} */
function bucketFor(confidence) {
  const c = roundConfidence(confidence);
  for (const b of CALIBRATION_BUCKETS) {
    const inRange = b.closed ? c >= b.min && c <= b.max : c >= b.min && c < b.max;
    if (inRange) return b;
  }
  return null;
}

/**
 * @param {any[]} answerRows - rows shaped `{decision_id, question, confidence}`.
 * @param {any[]} outcomeRows - rows shaped `{decision_id, outcome}`. When a `decision_id` has more
 *   than one outcome row (e.g. a later `--scan-git` run touching the same decision), the LAST one
 *   in array order wins — outcome rows are append-only, so the last is the most recent verdict.
 * @param {{questionId?: string}} [opts] - restrict to one question id; omitted = every question.
 * @returns {{buckets: Record<string, Record<string, number>>, unbacked: number, unmapped: number}}
 *   `buckets` always has exactly the 8 `CALIBRATION_BUCKETS` ids, each mapping to exactly the 3
 *   `OUTCOMES` — the grid is fully "rendered" even where a cell's count is 0. `unbacked` counts
 *   answers whose confidence didn't fall in any bucket (< 0.5); `unmapped` counts answers whose
 *   outcome value isn't in `OUTCOME_MAP` — these are NOT folded into the `unknown` cell, so a
 *   vocabulary drift is visible instead of silently looking like "no verdict yet".
 */
export function buildCalibration(answerRows, outcomeRows, opts = {}) {
  const outcomeById = new Map(outcomeRows.map((o) => [o.decision_id, o.outcome]));
  /** @type {Record<string, Record<string, number>>} */
  const buckets = {};
  for (const b of CALIBRATION_BUCKETS) buckets[b.id] = { correct: 0, wrong: 0, unknown: 0 };

  let unbacked = 0;
  let unmapped = 0;
  for (const row of answerRows) {
    if (opts.questionId && row.question !== opts.questionId) continue;
    const bucket = bucketFor(row.confidence);
    if (!bucket) {
      unbacked += 1;
      continue;
    }
    const rawOutcome = outcomeById.get(row.decision_id);
    if (rawOutcome !== undefined && !Object.hasOwn(OUTCOME_MAP, rawOutcome)) {
      unmapped += 1;
      continue;
    }
    const key = rawOutcome === undefined ? 'unknown' : OUTCOME_MAP[rawOutcome];
    buckets[bucket.id][key] += 1;
  }
  return { buckets, unbacked, unmapped };
}

/**
 * Decide whether ONE S1-decided answer gets shadow-sampled to S2 (plan §6.3 caller 1). Pure and
 * deterministic given `rng`, so "0 of 50 at shadow_rate: 0" is provable, not just observed.
 * @param {number} shadowRate - `calibration.shadow_rate`, schema-validated in [0, 0.5] by B1 — also
 *   re-validated here (NaN passes `typeof x === 'number'` and `NaN < 0` is false, so a bare
 *   `< 0` guard alone lets a NaN rate silently disable sampling with no error).
 * @param {() => number} [rng] - returns a value in [0, 1); defaults to `Math.random`.
 * @returns {boolean}
 */
export function shouldShadowSample(shadowRate, rng = Math.random) {
  if (!Number.isFinite(shadowRate) || shadowRate < 0 || shadowRate > 0.5) {
    throw new RangeError(`shouldShadowSample: shadowRate must be a finite number in [0, 0.5], got ${shadowRate}`);
  }
  // Short-circuit at rate 0 without even consulting rng — makes "0 sampled" true by construction,
  // not by a lucky draw, and lets a test prove rng was never called.
  if (shadowRate === 0) return false;
  return rng() < shadowRate;
}

/**
 * @param {any[]} eligibleRows
 * @param {number} shadowRate
 * @param {() => number} [rng]
 * @returns {any[]} the subset sampled.
 */
export function sampleForShadow(eligibleRows, shadowRate, rng = Math.random) {
  return eligibleRows.filter(() => shouldShadowSample(shadowRate, rng));
}
