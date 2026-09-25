/**
 * The stub guard (plan §4.2, O12; block B12a).
 *
 * A session's answer counts as a review only when ALL of these hold:
 *  1. the process exited 0 (not killed at the session timeout);
 *  2. the answer is a JSON object that validates against `finding.schema.json`;
 *  3. `reviewed_hunks` equals the packet's hunk headers EXACTLY and IN ORDER;
 *  4. `tokens_out ≥ review.min_tokens_out` (120).
 * Anything else is `unavailable` with one reason — `timeout`, `exit`, `schema`,
 * `hunks_mismatch`, `too_short` — and is never approval. There is no "approve on missing output":
 * no answer is a `schema` failure, no session is an `exit` failure.
 */

import { readFileSync } from 'node:fs';
import { Ajv2020 } from 'ajv/dist/2020.js';

export const UNAVAILABLE_REASONS = Object.freeze(['exit', 'schema', 'hunks_mismatch', 'too_short', 'timeout']);

export const DEFAULT_MIN_TOKENS_OUT = 120;

/** The source answer schema (compiled per provider by the spawner). */
export const FINDING_SCHEMA = Object.freeze(JSON.parse(readFileSync(new URL('./finding.schema.json', import.meta.url), 'utf8')));

const validateSchema = (() => {
  const body = structuredClone(FINDING_SCHEMA);
  delete body.$id;
  return new Ajv2020({ allErrors: true, strict: false }).compile(body);
})();

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
 * @property {'exit' | 'schema' | 'hunks_mismatch' | 'too_short' | 'timeout'} [reason]
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
  if (!(tokensOut >= min)) return { ok: false, reason: 'too_short', detail: `tokens_out ${tokensOut} < ${min}` };
  return { ok: true, review: session.answer, tokens_out: tokensOut };
}
