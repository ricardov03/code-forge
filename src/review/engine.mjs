/**
 * The review engine (plan §4.2 adaptive, §4.4 consensus, §4.3 isolation; block B12a).
 *
 * `reviewFile(input, deps)` reviews ONE file at its current content:
 *  1. plans the sessions (`planSessions`):
 *       adaptive (default) — `risk < 1` ⇒ one L2 `quick`; `1 ≤ risk < 2` ⇒ one L2 `full`;
 *       `risk ≥ 2` ⇒ two blind L2 sessions (lens `A`, lens `B`) + one L3 `judge`;
 *       consensus (`review.multimodel: true`) — one L2 `full` per EFFECTIVE provider
 *       (`resolve(L2)` and `review.second_levels.L2` / `review.second_provider`) + one L3 judge;
 *       the same effective provider twice is refused (`consensus-same-provider`);
 *  2. reads the diff once and assembles one packet per lens (`packet.mjs`);
 *  3. runs every session through `deps.spawn` — a fresh closed-book process in an empty cwd with
 *     the packet on stdin (`spawnSession`, B9a; `--safe-mode --tools ""` from the B4 builder);
 *  4. passes each answer through the stub guard (`validate-review.mjs`); an answer that fails it
 *     is `unavailable` with its reason and is NEVER approval; a failed lens means no judge;
 *  5. a valid answer with `needs_file` gets ONE more round with those files attached (git-tracked
 *     files only, never secret-like paths — `attachFiles`); a second `needs_file` is ignored; a
 *     request with a non-repo-relative path is refused (`needs_file-refused`, never approval).
 * Approval (until B12b's triage lands) = the final answer (the judge's in dual/consensus mode, else
 * the single reviewer's) passed the guard, says `passed: true`, and carries no `critical` or
 * `warning` finding.
 */

import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import path from 'node:path';
import { resolveLevel } from '../config/known-ids.mjs';
import { fallbackRisk } from '../decide/fallback-rules.mjs';
import { tierFor } from '../proof/tiers.mjs';
import { assembleJudgePacket, assemblePacket, attachFiles, budgetFor, readFileDiff } from './packet.mjs';
import { FINDING_SCHEMA, minTokensOut, validateReview } from './validate-review.mjs';
import { assertRowPath } from '../worker/ticket.mjs';

/**
 * @typedef {{lens: 'quick' | 'full' | 'A' | 'B' | 'judge', role: 'reviewer' | 'judge', level: 'L2' | 'L3', slot: 'A' | 'B' | null, cfg?: Record<string, any>}} SessionSpec -
 *   `cfg`: a per-session config (the consensus second reviewer: `levels.L2` = its effective level).
 * @typedef {{mode: 'adaptive' | 'consensus', depth: 'quick' | 'full' | 'dual' | 'consensus' | null, sessions: SessionSpec[], refused?: string}} SessionPlan
 */

/**
 * The second reviewer's effective L2 level for consensus mode.
 * @param {Record<string, any>} cfg
 * @returns {{ok: boolean, level?: Record<string, any>, primary?: string, reason?: string}}
 */
export function secondLevel(cfg) {
  let primary;
  try {
    primary = resolveLevel(cfg, 'L2').provider;
  } catch {
    return { ok: false, reason: 'consensus-no-l2' };
  }
  const override = cfg?.review?.second_levels?.L2 ?? null;
  const provider = override?.provider ?? cfg?.review?.second_provider ?? null;
  if (typeof provider !== 'string' || provider.length === 0) return { ok: false, reason: 'consensus-no-second-provider' };
  if (provider === primary) return { ok: false, reason: 'consensus-same-provider' };
  if (typeof override?.model !== 'string' || override.model.length === 0) return { ok: false, reason: 'consensus-no-second-model' };
  // the second reviewer's fallback ladder never lands on the first reviewer's provider
  const fallback = (Array.isArray(override.fallback) ? override.fallback : []).filter((/** @type {any} */ f) => f?.provider !== primary);
  return { ok: true, level: { ...override, provider, fallback }, primary };
}

/**
 * @param {{risk: number, cfg: Record<string, any>}} opts
 * @returns {SessionPlan}
 */
export function planSessions({ risk, cfg }) {
  if (typeof risk !== 'number' || !Number.isFinite(risk) || risk < 0 || risk > 3) throw new TypeError('planSessions: risk must be a number from 0 to 3');
  const judge = /** @type {SessionSpec} */ ({ lens: 'judge', role: 'judge', level: 'L3', slot: null });
  if (cfg?.review?.multimodel === true) {
    const second = secondLevel(cfg);
    if (!second.ok) return { mode: 'consensus', depth: null, sessions: [], refused: second.reason };
    return {
      mode: 'consensus',
      depth: 'consensus',
      sessions: [
        { lens: 'full', role: 'reviewer', level: 'L2', slot: 'A' },
        { lens: 'full', role: 'reviewer', level: 'L2', slot: 'B', cfg: { ...cfg, levels: { ...cfg.levels, L2: second.level } } },
        judge,
      ],
    };
  }
  if (risk < 1) return { mode: 'adaptive', depth: 'quick', sessions: [{ lens: 'quick', role: 'reviewer', level: 'L2', slot: null }] };
  if (risk < 2) return { mode: 'adaptive', depth: 'full', sessions: [{ lens: 'full', role: 'reviewer', level: 'L2', slot: null }] };
  return {
    mode: 'adaptive',
    depth: 'dual',
    sessions: [
      { lens: 'A', role: 'reviewer', level: 'L2', slot: 'A' },
      { lens: 'B', role: 'reviewer', level: 'L2', slot: 'B' },
      judge,
    ],
  };
}

/**
 * The rules risk (§3.5 fallback; S1 `risk` without Jev): a `proof.tiers.high.paths` match ⇒ 3,
 * a migration/policy/middleware path ⇒ 2, more than 200 added lines ⇒ 1, else 0.
 * @param {{file: string, plusCount: number, cfg: Record<string, any>}} opts
 * @returns {number}
 */
export function rulesRisk({ file, plusCount, cfg }) {
  const highPaths = cfg?.proof?.tiers?.high?.paths;
  const pathFloorHit = tierFor({ file, risk: 0, highPaths: Array.isArray(highPaths) ? highPaths : [] }).reason === 'path';
  return fallbackRisk({
    pathFloorHit,
    filesChanged: 1,
    linesAdded: plusCount,
    touchesMigration: /migration/i.test(file),
    touchesPolicyOrMiddleware: /polic(y|ies)|middleware/i.test(file),
  });
}

/**
 * @typedef {object} ReviewInput
 * @property {string} repoRoot - realpath'd repository root.
 * @property {string} file - repo-root-relative.
 * @property {string | null} [base] - the block's base SHA (null ⇒ HEAD).
 * @property {Record<string, any>} cfg
 * @property {number} [risk] - the S1 risk (0–3); absent ⇒ the rules risk.
 * @property {string} [rulesDigest] @property {string} [factsExcerpt]
 * @property {string} workDir - where packet files are written (under the run's temp root).
 */

/**
 * @typedef {object} ReviewDeps
 * @property {(opts: Record<string, any>) => Promise<Record<string, any>>} spawn - one isolated
 *   session (`spawnSession` opts minus the worker-pinned ones); a per-call `cfg` selects another
 *   provider's level for the same slot (consensus).
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeRow] - ledger rows
 *   (`review.plan`, `review.unavailable`); a failed write never fails the review.
 */

/** @param {ReviewDeps} deps @param {Record<string, any>} row */
async function note(deps, row) {
  try {
    await deps.writeRow?.(row);
  } catch {
    // the review result stands; the ledger row is best-effort
  }
}

/**
 * The `needs_file` request of a model answer, or null when any entry is not a plain
 * repo-relative path (absolute, `..`, `.git/`, `.code-forge/`, not a string).
 * @param {unknown} needs @returns {string[] | null}
 */
function safeNeeds(needs) {
  if (!Array.isArray(needs)) return null;
  try {
    return needs.map((p) => assertRowPath(p));
  } catch {
    return null;
  }
}

/**
 * @param {ReviewInput} input @param {ReviewDeps} deps
 * @returns {Promise<{status: string, approved: boolean, engine: string, sessions: Array<Record<string, any>>} & Record<string, any>>}
 */
export async function reviewFile(input, deps) {
  const { repoRoot, cfg, workDir } = input;
  const diff = await readFileDiff({ repoRoot, file: input.file, base: input.base ?? null });
  const risk = input.risk ?? rulesRisk({ file: diff.file, plusCount: diff.plusCount, cfg });
  const plan = planSessions({ risk, cfg });
  const head = { engine: plan.mode, depth: plan.depth, risk, file: diff.file };
  if (plan.refused) return { status: 'refused', reason: plan.refused, approved: false, ...head, sessions: [] };
  if (diff.diffText.trim().length === 0) return { status: 'no_change', approved: false, ...head, sessions: [] };
  mkdirSync(workDir, { recursive: true, mode: 0o700 });
  const min = minTokensOut(cfg);

  /** @param {SessionSpec} spec @param {{text: string, hunkHeaders: string[], tokensIn: number, contextMode: string | null}} packet */
  const run = async (spec, packet) => {
    /** @type {Record<string, any>} */
    let summary = await runOne(spec, packet.text, packet, { deps, workDir, min, cfg });
    const needs = summary.review?.needs_file;
    if (spec.role === 'reviewer' && summary.status === 'ok' && Array.isArray(needs) && needs.length > 0) {
      const paths = safeNeeds(needs);
      let extra = null;
      try {
        extra = paths ? await attachFiles({ repoRoot, paths, budgetTokens: budgetFor(cfg, 'full_in') }) : null;
      } catch {
        extra = null;
      }
      if (extra === null) return { ...summary, status: 'unavailable', reason: 'needs_file-refused', review: null };
      summary = { ...(await runOne(spec, `${packet.text}${extra}`, packet, { deps, workDir, min, cfg })), needs_file_round: true };
    }
    return summary;
  };

  const reviewers = plan.sessions.filter((s) => s.role === 'reviewer');
  const packets = reviewers.map((spec) => assemblePacket({ diff, lens: spec.lens, rulesDigest: input.rulesDigest, factsExcerpt: input.factsExcerpt, cfg }));
  const split = packets.find((p) => p.status === 'split_required');
  if (split) return { status: 'split_required', approved: false, ...head, tokens_in: split.tokensIn, budget: split.budget, sessions: [] };
  const firstPacket = /** @type {any} */ (packets[0]);
  await note(deps, { event: 'review.plan', depth_unconstrained: plan.depth, depth_chosen: plan.depth, degrade_step: 0, risk, context_mode: firstPacket?.contextMode ?? null, forecast_tokens: packets.reduce((n, p) => n + p.tokensIn, 0) });
  const done = await Promise.all(reviewers.map((spec, i) => run(spec, /** @type {any} */ (packets[i]))));
  const sessions = done.map(publicSummary);
  const failed = done.find((s) => s.status !== 'ok');
  if (failed) {
    await note(deps, { event: 'review.unavailable', reason: failed.reason, lens: failed.lens });
    return { status: 'unavailable', reason: failed.reason, approved: false, ...head, sessions };
  }

  let final = done[0];
  const judgeSpec = plan.sessions.find((s) => s.role === 'judge');
  if (judgeSpec) {
    const bySlot = Object.fromEntries(done.map((s) => [s.slot, s.review]));
    const packet = assembleJudgePacket({ diff, reports: { A: bySlot.A, B: bySlot.B }, cfg });
    final = await runOne(judgeSpec, packet.text, packet, { deps, workDir, min, cfg });
    sessions.push(publicSummary(final));
    if (final.status !== 'ok') {
      await note(deps, { event: 'review.unavailable', reason: final.reason, lens: 'judge' });
      return { status: 'unavailable', reason: final.reason, approved: false, ...head, sessions };
    }
  }
  const findings = final.review.findings;
  const approved = final.review.passed === true && findings.every((/** @type {any} */ f) => f.severity === 'nit');
  return { status: 'reviewed', approved, ...head, summary: final.review.summary, findings, sessions };
}

/**
 * One session: packet file (0600, removed after), spawn, stub guard.
 * @param {SessionSpec} spec @param {string} text
 * @param {{hunkHeaders: string[], tokensIn: number, contextMode: string | null}} packet
 * @param {{deps: ReviewDeps, workDir: string, min: number, cfg: Record<string, any>}} env
 */
async function runOne(spec, text, packet, { deps, workDir, min }) {
  const promptPath = path.join(workDir, `${spec.lens}-${randomBytes(6).toString('hex')}.md`);
  const base = { lens: spec.lens, role: spec.role, level: spec.level, slot: spec.slot, context_mode: packet.contextMode, ctx_tokens_in: packet.tokensIn };
  writeFileSync(promptPath, text, { mode: 0o600 });
  /** @type {Record<string, any> | null} */
  let res = null;
  try {
    res = await deps.spawn({
      level: spec.level,
      role: spec.role,
      promptPath,
      schema: FINDING_SCHEMA,
      rowExtra: { lens: spec.lens, context_mode: packet.contextMode, ctx_tokens_in: packet.tokensIn },
      ...(spec.cfg ? { cfg: spec.cfg } : {}),
    });
  } catch {
    res = null; // a spawn that throws (bad config, refused argv) is an `exit` failure, never approval
  } finally {
    rmSync(promptPath, { force: true });
  }
  const verdict = validateReview(res, { hunkHeaders: packet.hunkHeaders, minTokensOut: min });
  const meta = {
    ...base,
    provider: res?.provider ?? null,
    model: res?.model ?? null,
    fallback_step: res?.fallback_step ?? 0,
    tokens_in: res?.usage?.tokens_in ?? null,
    tokens_out: res?.usage?.tokens_out ?? null,
  };
  if (!verdict.ok) return { ...meta, status: 'unavailable', reason: verdict.reason, review: null };
  return { ...meta, status: 'ok', reason: null, review: verdict.review };
}

/** The per-session summary the worker signs: the answer's verdict and findings, never the packet. */
function publicSummary(/** @type {Record<string, any>} */ s) {
  const { review, ...rest } = s;
  return review ? { ...rest, passed: review.passed === true, findings: review.findings } : rest;
}
