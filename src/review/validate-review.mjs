/**
 * The stub guard (plan §4.2, O12; block B12a).
 *
 * A session's answer counts as a review only when ALL of these hold:
 *  1. the process exited 0 (not killed at the session timeout);
 *  2. the answer is a JSON object that validates against `finding.schema.json`;
 *  3. `reviewed_hunks` equals the packet's hunk headers EXACTLY and IN ORDER;
 *  4. `tokens_out ≥ review.min_tokens_out` (120), or ≥ 40 for a structured clean pass (B53).
 * Anything else is `unavailable` with one reason — `timeout`, `exit`, `schema`,
 * `hunks_mismatch`, `too_short`, or `budget` (B33: the spawner refused at `budget.usd`) — and is
 * never approval. There is no "approve on missing output":
 * no answer is a `schema` failure, no session is an `exit` failure.
 */

import { readFileSync } from 'node:fs';
import { Ajv2020 } from 'ajv/dist/2020.js';

export const UNAVAILABLE_REASONS = Object.freeze(['exit', 'schema', 'hunks_mismatch', 'too_short', 'timeout', 'budget']);

export const DEFAULT_MIN_TOKENS_OUT = 120;

/**
 * A structured clean pass legitimately needs few words, whatever the diff size: GPT-6 Astra clean
 * passes measured 108-121 output tokens while reviews with findings measured 300-1,400, so the full
 * floor refused 11 of 19 clean files on one block (issue #6). A clean pass is `passed: true`, no
 * findings, every hunk acknowledged in order (checked above) and a non-blank `summary`; for it the
 * floor drops to this value. Any finding, or a blank summary, keeps the full floor (B53).
 */
export const CLEAN_PASS_MIN_TOKENS_OUT = 40;

/** The source answer schema (compiled per provider by the spawner). */
export const FINDING_SCHEMA = Object.freeze(JSON.parse(readFileSync(new URL('./finding.schema.json', import.meta.url), 'utf8')));

const validateSchema = (() => {
  const body = structuredClone(FINDING_SCHEMA);
  delete body.$id;
  return new Ajv2020({ allErrors: true, strict: false }).compile(body);
})();

/**
 * The finding-schema errors of `value` from the SAME validator the stub guard uses (B55): Ajv's
 * raw error objects, every one (`allErrors`). Callers must strip anything that can carry the
 * answer's content (`review/schema-fallback.mjs`); this returns them as Ajv gives them.
 * @param {unknown} value @returns {Array<import('ajv').ErrorObject>} empty when it validates.
 */
export function schemaErrorsOf(value) {
  if (validateSchema(value) === true) return [];
  return [...(validateSchema.errors ?? [])];
}

/** @param {Record<string, any> | undefined} cfg @returns {number} */
export function minTokensOut(cfg) {
  const v = cfg?.review?.min_tokens_out;
  return Number.isInteger(v) && v > 0 ? v : DEFAULT_MIN_TOKENS_OUT;
}

/**
 * @typedef {object} Verdict - `ok: true` carries `review` and `tokens_out`; `ok: false` carries
 *   `reason` (one of `UNAVAILABLE_REASONS`) and `detail`.
 * @property {boolean} ok
 * @property {Record<string, any>} [review]
 * @property {number} [tokens_out]
 * @property {'exit' | 'schema' | 'hunks_mismatch' | 'too_short' | 'timeout' | 'budget'} [reason]
 * @property {string} [detail]
 */

/**
 * @param {Record<string, any> | null | undefined} session - a `spawnSession` result.
 * @param {{hunkHeaders: ReadonlyArray<string>, minTokensOut?: number}} expect
 * @returns {Verdict}
 */
export function validateReview(session, { hunkHeaders, minTokensOut: min = DEFAULT_MIN_TOKENS_OUT }) {
  if (!session || typeof session !== 'object') return { ok: false, reason: 'exit', detail: 'no session result' };
  if (session.status === 'timeout') return { ok: false, reason: 'timeout', detail: 'killed at the session timeout' };
  // B33: the spawner refused the session because the run reached `budget.usd` — nothing ran
  if (session.status === 'unavailable' && session.reason === 'budget') return { ok: false, reason: 'budget', detail: String(session.message ?? 'budget.usd reached') };
  const exitCode = session.exit_code;
  if (session.status === 'failed' || session.status === 'unavailable' || (exitCode !== undefined && exitCode !== 0)) {
    return { ok: false, reason: 'exit', detail: `session ${session.status}${exitCode === undefined ? '' : ` (exit ${exitCode})`}` };
  }
  /** @type {any} */
  const answer = session.answer;
  if (session.status !== 'ok' || answer === null || typeof answer !== 'object' || Array.isArray(answer) || validateSchema(answer) !== true) {
    return { ok: false, reason: 'schema', detail: 'the answer does not validate against the finding schema' };
  }
  const got = /** @type {string[]} */ (session.answer.reviewed_hunks);
  if (got.length !== hunkHeaders.length || got.some((h, i) => h !== hunkHeaders[i])) {
    return { ok: false, reason: 'hunks_mismatch', detail: `reviewed_hunks has ${got.length} entries; the packet has ${hunkHeaders.length} hunks` };
  }
  const tokensOut = Number(session.usage?.tokens_out ?? session.row?.tokens_out ?? 0);
  const review = /** @type {{passed: boolean, summary: string, findings: unknown[]}} */ (session.answer);
  const cleanPass = review.passed === true && Array.isArray(review.findings) && review.findings.length === 0 && review.summary.trim().length > 0;
  const floor = cleanPass ? Math.min(min, CLEAN_PASS_MIN_TOKENS_OUT) : min;
  if (!(tokensOut >= floor)) return { ok: false, reason: 'too_short', detail: `tokens_out ${tokensOut} < ${floor}` };
  return { ok: true, review: session.answer, tokens_out: tokensOut };
}
