/**
 * The block gate's transcript grep (plan §8.6/C4, row 9 of §4.6: "no `rule_break` row —
 * including the transcript grep"). The actual scanner (`scanTranscript`) already lives in B0's
 * `util/forbidden.mjs` (extended by B8 in Wave 1 with the signer-protection ★ entries) — this
 * module is the gate-facing wrapper: it decides pass/fail and shapes a `rule_break`-ready reason,
 * it does not re-implement the scan.
 */

import { FORBIDDEN, mergeForbidden, scanTranscript } from '../util/forbidden.mjs';

/**
 * @typedef {{id: string, line: number}} TranscriptHit
 * @typedef {{ok: boolean, hits: TranscriptHit[]}} TranscriptCheck
 */

/**
 * @param {string} text - captured coder output (subprocess stdout, Solo `search_output`, a
 *   harness log).
 * @param {{extraTokens?: string[]}} [opts] - `extraTokens` merges in `production.markers`/
 *   `production.names` (B1 config), the same way `mergeForbidden` does everywhere else.
 * @returns {TranscriptCheck}
 */
export function checkTranscript(text, opts = {}) {
  const list = opts.extraTokens && opts.extraTokens.length > 0 ? mergeForbidden(opts.extraTokens) : FORBIDDEN;
  const hits = scanTranscript(text, list);
  return { ok: hits.length === 0, hits };
}
