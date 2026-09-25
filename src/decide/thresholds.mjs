/**
 * The S1 → S2 hand-off maths (plan §3.3, O8) — the ONE place that turns a raw Jev answer into
 * `{confidence, margin}` and decides whether S1 acts, S2 checks, or S2 decides alone.
 *
 * **O8, verbatim:** `choice`/`score` — `confidence = p(top1)`, `margin = p(top1) − p(top2)`
 * (`p(top2) = 0` when there is only one non-zero probability). `noul` — `answer = p ≥ 0.5`,
 * `confidence = max(p, 1 − p)`, `margin = |2p − 1|`. This module NEVER reads an API-supplied
 * `confidence`/`score` field for this purpose — it recomputes from `probabilities`/`noul` every
 * time, so a provider that ever changes how it rounds its own `confidence` field cannot silently
 * drift our thresholds. (A mutant that reads `p` as the confidence for a `noul` answer must turn
 * `decide()` red on the documented `finding:nit` fixture — see `test/decide/probes.test.mjs`.)
 *
 * **Bands (§3.3):** `confidence ≥ act (0.90)` AND `margin ≥ close_margin (0.15)` ⇒ `'act'` (S1
 * decides) · `check (0.60) ≤ confidence < act`, OR `confidence ≥ act` but `margin < close_margin`
 * ⇒ `'check'` (S2 checks S1's answer) · `confidence < check` ⇒ `'decide'` (S2 decides from the
 * state alone). Overridable per question via `thresholds.<id>`, falling back to
 * `thresholds.default`, falling back to the defaults below — `validate.mjs`/the schema never
 * apply these as JSON-Schema `default`s (no `ajv useDefaults` anywhere in this package), so this
 * module is the one place they are real.
 *
 * **Findings keep their own bands** (`thresholds.findings`, §3.3 last sentence): a `defect`
 * answer's raw `noul` PROBABILITY (not confidence) is banded `fix_now` (≥ 0.90) / `judge`
 * (0.40–0.90) / `nit` (< 0.40); a `resolved` answer is `closed` (≥ 0.90) or `recheck` (else).
 * These are a SEPARATE decision from `decide()`'s general act/check/decide bands — a review
 * engine (B12) uses `bandFinding`/`bandResolved` to triage a finding's severity, not to decide
 * whether S1 or S2 owns the call.
 */

/** §3.3's documented defaults — used whenever `thresholds.<id>`/`thresholds.default` omit a field. */
export const DEFAULT_BAND = Object.freeze({ act: 0.9, check: 0.6, close_margin: 0.15 });

/** §1.3's documented `thresholds.findings` defaults. */
export const DEFAULT_FINDINGS = Object.freeze({ fix: 0.9, nit: 0.4, resolved: 0.9 });

/**
 * @typedef {import('./jev-client.mjs').JevAnswer} JevAnswer
 * @typedef {{act: number, check: number, close_margin: number}} ThresholdBand
 */

/**
 * @param {string} questionId
 * @param {Record<string, any>} [cfg]
 * @returns {ThresholdBand}
 */
export function bandsFor(questionId, cfg = {}) {
  const perQuestion = cfg?.thresholds?.[questionId] ?? {};
  const dflt = cfg?.thresholds?.default ?? {};
  return {
    act: perQuestion.act ?? dflt.act ?? DEFAULT_BAND.act,
    check: perQuestion.check ?? dflt.check ?? DEFAULT_BAND.check,
    close_margin: perQuestion.close_margin ?? dflt.close_margin ?? DEFAULT_BAND.close_margin,
  };
}

/**
 * @param {Record<string, any>} [cfg]
 * @returns {{fix: number, nit: number, resolved: number}}
 */
export function findingsThresholds(cfg = {}) {
  const raw = cfg?.thresholds?.findings ?? {};
  return {
    fix: raw.fix ?? DEFAULT_FINDINGS.fix,
    nit: raw.nit ?? DEFAULT_FINDINGS.nit,
    resolved: raw.resolved ?? DEFAULT_FINDINGS.resolved,
  };
}

/**
 * Sort a `probabilities` map's values descending. Tolerant of a single-key map (`p(top2) = 0`,
 * O8's own worked example — a lane answer with only `L0: 1.0` set).
 * @param {Record<string, number>} probabilities
 * @returns {number[]}
 */
function sortedProbabilities(probabilities) {
  if (probabilities === null || typeof probabilities !== 'object') {
    throw new TypeError('sortedProbabilities: probabilities must be an object');
  }
  const values = Object.values(probabilities);
  if (values.length === 0 || values.some((v) => typeof v !== 'number' || !Number.isFinite(v))) {
    throw new TypeError('sortedProbabilities: probabilities must hold only finite numbers');
  }
  return [...values].sort((a, b) => b - a);
}

/**
 * @param {Record<string, number>} probabilities
 * @returns {string} the key whose value is the largest (first one, on a tie).
 */
function argmax(probabilities) {
  let bestKey = null;
  let bestValue = -Infinity;
  for (const [key, value] of Object.entries(probabilities)) {
    if (value > bestValue) {
      bestValue = value;
      bestKey = key;
    }
  }
  return bestKey;
}

/**
 * O8's confidence/margin maths, plus the decided VALUE (independent of the raw `choice`/`score`
 * field the API also returned, for `choice` — the argmax of `probabilities` is authoritative here
 * so confidence/margin and the reported value can never disagree with each other).
 *
 * `answer` is typed `any`, not the stricter {@link JevAnswer} union, deliberately: this is a
 * boundary function for an external API's parsed JSON — TypeScript cannot verify a network
 * response's shape at compile time regardless of the JSDoc typedef, and this function's entire
 * job IS the runtime validation `JevAnswer` only documents. Every branch below still checks its
 * own shape and throws a `TypeError` on anything that doesn't match.
 * @param {any} answer
 * @returns {{confidence: number, margin: number, value: string|number|boolean}}
 */
export function computeConfidenceMargin(answer) {
  if (answer === null || typeof answer !== 'object') {
    throw new TypeError('computeConfidenceMargin: answer must be an object');
  }
  if (answer.type === 'choice') {
    const [top1, top2 = 0] = sortedProbabilities(answer.probabilities);
    return { confidence: top1, margin: top1 - top2, value: argmax(answer.probabilities) };
  }
  if (answer.type === 'score') {
    const [top1, top2 = 0] = sortedProbabilities(answer.probabilities);
    if (typeof answer.score !== 'number' || !Number.isFinite(answer.score)) {
      throw new TypeError('computeConfidenceMargin: a "score" answer must carry a finite score');
    }
    return { confidence: top1, margin: top1 - top2, value: answer.score };
  }
  if (answer.type === 'noul') {
    if (typeof answer.noul !== 'number' || !Number.isFinite(answer.noul) || answer.noul < 0 || answer.noul > 1) {
      throw new TypeError('computeConfidenceMargin: a "noul" answer must carry noul in [0, 1]');
    }
    const p = answer.noul;
    return { confidence: Math.max(p, 1 - p), margin: Math.abs(2 * p - 1), value: p >= 0.5 };
  }
  throw new TypeError(`computeConfidenceMargin: unknown answer.type ${JSON.stringify(answer?.type)}`);
}

/**
 * @typedef {object} Decision
 * @property {boolean} decided - true only for the `'act'` stage.
 * @property {'act'|'check'|'decide'} stage
 * @property {string|number|boolean} value - the S1-computed answer, independent of `stage`.
 * @property {number} confidence
 * @property {number} margin
 */

/**
 * The general act/check/decide band (§3.3) for one question's answer. Does NOT apply to
 * `defect`/`resolved` FINDING triage — use `bandFinding`/`bandResolved` for those.
 * @param {string} questionId
 * @param {any} answer - see {@link computeConfidenceMargin} for why this isn't `JevAnswer`.
 * @param {Record<string, any>} [cfg]
 * @returns {Decision}
 */
export function decide(questionId, answer, cfg = {}) {
  const { confidence, margin, value } = computeConfidenceMargin(answer);
  const band = bandsFor(questionId, cfg);
  /** @type {'act'|'check'|'decide'} */
  let stage;
  if (confidence >= band.act && margin >= band.close_margin) {
    stage = 'act';
  } else if (confidence >= band.check) {
    stage = 'check';
  } else {
    stage = 'decide';
  }
  return { decided: stage === 'act', stage, value, confidence, margin };
}

/**
 * Finding triage band for a `defect` answer's raw `noul` PROBABILITY (§3.3, §4.2 step 4).
 * @param {number} p - `answer.noul`, the probability the finding is real (NOT a confidence).
 * @param {Record<string, any>} [cfg]
 * @returns {'fix_now'|'judge'|'nit'}
 */
export function bandFinding(p, cfg = {}) {
  if (typeof p !== 'number' || !Number.isFinite(p) || p < 0 || p > 1) {
    throw new TypeError('bandFinding: p must be a finite number in [0, 1]');
  }
  const t = findingsThresholds(cfg);
  if (p >= t.fix) return 'fix_now';
  if (p >= t.nit) return 'judge';
  return 'nit';
}

/**
 * Finding triage band for a `resolved` answer's raw `noul` probability (§3.3).
 * @param {number} p
 * @param {Record<string, any>} [cfg]
 * @returns {'closed'|'recheck'}
 */
export function bandResolved(p, cfg = {}) {
  if (typeof p !== 'number' || !Number.isFinite(p) || p < 0 || p > 1) {
    throw new TypeError('bandResolved: p must be a finite number in [0, 1]');
  }
  const t = findingsThresholds(cfg);
  return p >= t.resolved ? 'closed' : 'recheck';
}
