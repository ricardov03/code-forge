/**
 * Finding triage (plan §4.2 step 4, §3.3, O11; block B12b).
 *
 * `triageFindings` sorts the findings of ONE file's review into `fix_now` and `nit`:
 *  - findings that come FROM A JUDGE (dual adaptive or consensus) are FINAL: `critical` and
 *    `warning` are `fix_now`, `nit` is `nit`. S1 `defect` still runs on each of them (when Jev is
 *    reachable and the question is not disabled) but the answer is logged `source: shadow` and
 *    NEVER changes the verdict;
 *  - findings from a SINGLE L2 session go to S1 `defect`: the raw `noul` probability is banded by
 *    `bandFinding` (`fix_now` ≥ 0.90 · `judge` 0.40–0.90 · `nit` < 0.40). The mid band is batched
 *    to ONE L3 ruling per file (`deps.rule`). Jev unavailable or `defect` disabled ⇒ §3.5 fallback:
 *    `defect` has no rule (`alwaysResidue`), so every finding is residue for the L3 ruling;
 *  - fail closed: a mid-band finding with no ruling available (no `deps.rule`, or it failed or
 *    left the id out) is `fix_now` unless its severity is `nit`.
 * One `review.triage` row per finding: `{file, finding, severity, source, p, band, verdict}`.
 */

import { buildQuestionPayload, isQuestionDisabled } from '../decide/questions.mjs';
import { bandFinding, bandResolved } from '../decide/thresholds.mjs';
import { alwaysResidue } from '../decide/fallback-rules.mjs';
import { logWarning } from '../util/error-log.mjs';

/**
 * @typedef {{id: string, file?: string, line_start: number, line_end: number, severity: 'critical' | 'warning' | 'nit', category?: string, claim?: string, evidence?: string, fix?: string}} Finding
 * @typedef {(req: {state: Record<string, any>, questions: Record<string, any>}) => Promise<Record<string, any> | null>} JevAsk
 *   an `askJev`-shaped call with the key already bound (`{ok, answers}` or `{ok: false, kind}`).
 * @typedef {(req: {file: string, findings: Finding[]}) => Promise<Record<string, 'fix_now' | 'nit'> | null>} RuleFn
 *   one L3 ruling for a file's mid-band findings: id ⇒ verdict.
 * @typedef {(row: Record<string, any>) => Promise<unknown>} WriteRow
 */

/** @param {WriteRow | undefined} writeRow @param {Record<string, any>} row */
async function note(writeRow, row) {
  try {
    await writeRow?.(row);
  } catch {
    // triage stands; the ledger row is best-effort
  }
}

/**
 * The reasons a fallback warning may name: Jev client failure kinds (`jev-client.mjs`) plus
 * `error` (the call threw) and `invalid_answer` (ok, but no probability). Anything else is `other`.
 */
const FALLBACK_REASONS = new Set(['unauthorized', 'invalid_request', 'rate_limited', 'unavailable', 'network', 'timeout', 'unexpected_status', 'invalid_response', 'error', 'invalid_answer']);

/**
 * B37: Jev was asked and gave no usable answer, so §3.5's fallback decides: one warning line in the
 * local error log (once per reason per process). The reason is a word from a fixed list, never
 * Jev's text. Never throws: a logging failure cannot change the fallback.
 * @param {unknown} reason
 */
async function fellBack(reason) {
  const word = typeof reason === 'string' && FALLBACK_REASONS.has(reason) ? reason : 'other';
  try {
    await logWarning({ warning: 's1_fallback', message: `System 1 (Jev) gave no answer (${word}); fell back to the rules` }).catch(() => null);
  } catch {
    // the fallback stands without its warning line
  }
}

/** @param {Finding} f @returns {'fix_now' | 'nit'} the verdict a judge's severity means. */
export const severityVerdict = (f) => (f.severity === 'nit' ? 'nit' : 'fix_now');

/**
 * Ask S1 one `noul` question; null when Jev is unavailable, the question is disabled, or the
 * answer is not a probability.
 * @param {'defect' | 'resolved'} id @param {Record<string, any>} state
 * @param {{jev?: JevAsk, cfg?: Record<string, any>}} deps
 * @returns {Promise<number | null>}
 */
export async function askNoul(id, state, { jev, cfg = {} }) {
  if (typeof jev !== 'function' || isQuestionDisabled(id, cfg)) return null;
  let res;
  try {
    res = await jev({ state, questions: buildQuestionPayload([id], cfg) });
  } catch {
    await fellBack('error');
    return null;
  }
  const p = res?.ok === true ? res.answers?.[id]?.noul : undefined;
  if (typeof p === 'number' && Number.isFinite(p) && p >= 0 && p <= 1) return p;
  await fellBack(res?.ok === true ? 'invalid_answer' : res?.kind);
  return null;
}

/**
 * S1 `resolved` for one open finding against the fix hunk: `closed` at ≥ `thresholds.findings.resolved`
 * (0.90), else `recheck`; null when S1 gave no answer (the recheck session decides).
 * @param {{finding: Finding, fixHunk: string, jev?: JevAsk, cfg?: Record<string, any>}} opts
 * @returns {Promise<{p: number | null, band: 'closed' | 'recheck'}>}
 */
export async function resolvedBand({ finding, fixHunk, jev, cfg = {} }) {
  const p = await askNoul('resolved', { finding, fix_hunk: fixHunk }, { jev, cfg });
  return { p, band: p === null ? 'recheck' : bandResolved(p, cfg) };
}

/**
 * @param {{
 *   file: string, findings: Finding[], fromJudge: boolean, diffText?: string,
 *   cfg?: Record<string, any>, jev?: JevAsk, rule?: RuleFn, writeRow?: WriteRow,
 * }} opts
 * @returns {Promise<{fix_now: Finding[], nit: Finding[], rulings: number}>}
 */
export async function triageFindings({ file, findings, fromJudge, diffText = '', cfg = {}, jev, rule, writeRow }) {
  /** @type {Finding[]} */
  const fixNow = [];
  /** @type {Finding[]} */
  const nits = [];
  /** @type {Array<{finding: Finding, p: number | null}>} */
  const mid = [];
  const put = (/** @type {Finding} */ f, /** @type {'fix_now' | 'nit'} */ verdict) => (verdict === 'fix_now' ? fixNow : nits).push(f);

  for (const finding of findings) {
    const p = await askNoul('defect', { file_diff_hunk: diffText, finding }, { jev, cfg });
    if (fromJudge) {
      const verdict = severityVerdict(finding);
      put(finding, verdict);
      await note(writeRow, { event: 'review.triage', file, finding: finding.id, severity: finding.severity, source: 'shadow', p, band: p === null ? null : bandFinding(p, cfg), verdict });
      continue;
    }
    if (p === null) {
      // §3.5: `defect` has no rule in fallback mode — always residue for the L3 ruling
      mid.push({ finding, p: alwaysResidue('defect').value });
      continue;
    }
    const band = bandFinding(p, cfg);
    if (band === 'judge') {
      mid.push({ finding, p });
      continue;
    }
    put(finding, band);
    await note(writeRow, { event: 'review.triage', file, finding: finding.id, severity: finding.severity, source: 'jev', p, band, verdict: band });
  }

  let rulings = 0;
  if (mid.length > 0) {
    /** @type {Record<string, 'fix_now' | 'nit'> | null} */
    let ruled = null;
    if (typeof rule === 'function') {
      rulings = 1;
      try {
        ruled = await rule({ file, findings: mid.map((m) => m.finding) });
      } catch {
        ruled = null;
      }
    }
    for (const { finding, p } of mid) {
      const given = ruled?.[finding.id];
      const verdict = given === 'fix_now' || given === 'nit' ? given : severityVerdict(finding);
      put(finding, verdict);
      await note(writeRow, {
        event: 'review.triage',
        file,
        finding: finding.id,
        severity: finding.severity,
        source: p === null ? 's2-fallback' : 's2',
        p,
        band: p === null ? null : 'judge',
        verdict,
      });
    }
  }
  return { fix_now: fixNow, nit: nits, rulings };
}

/**
 * Whether an engine result's final findings came from a judge: dual adaptive (`A`+`B`+`judge`)
 * and consensus both end in a judge session.
 * @param {{sessions?: Array<Record<string, any>>}} result @returns {boolean}
 */
export function cameFromJudge(result) {
  return Array.isArray(result?.sessions) && result.sessions.some((s) => s?.role === 'judge');
}
