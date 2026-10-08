/**
 * The review engine (plan §4.2 adaptive, §4.4 consensus, §4.3 isolation; block B12a).
 *
 * `reviewFile(input, deps)` reviews ONE file at its current content:
 *  1. plans the sessions (`planSessions`):
 *       adaptive (default) — `risk < 1` ⇒ one L2 `quick`; `1 ≤ risk < 2` ⇒ one L2 `full`;
 *       `risk ≥ 2` ⇒ two blind L2 sessions (lens `A`, lens `B`) + one L3 `judge`;
 *       consensus (`review.multimodel: true`, risk above `review.single_reviewer_max_risk` (1),
 *       and not a docs/contract block unless `review.multimodel_for_docs`; B34) — one L2 `full` per EFFECTIVE provider
 *       (`resolve(L2)` and `review.second_levels.L2` / `review.second_provider`) + one L3 judge;
 *       the same effective provider twice is refused (`consensus-same-provider`);
 *  2. reads the diff once and assembles one packet per lens (`packet.mjs`);
 *  3. runs every session through `deps.spawn` — a fresh closed-book process in an empty cwd with
 *     the packet on stdin (`spawnSession`, B9a; `--safe-mode --tools ""` from the B4 builder);
 *  4. passes each answer through the stub guard (`validate-review.mjs`); an answer that fails it
 *     is `unavailable` with its reason and is NEVER approval; a failed lens means no judge;
 *  5. a valid answer with `needs_file` gets ONE more round with those files attached (git-tracked
 *     files only, never secret-like paths — `attachFiles`); a second `needs_file` is ignored; a
 *     request with a non-repo-relative path is refused (`needs_file-refused`, never approval);
 *  6. a session that ends in `timeout` is spawned ONCE more on the same packet and level
 *     (`session-retry.mjs`, B30); a second timeout is `unavailable: timeout`. The summary's
 *     `attempts` says how many ran;
 *  6b. a session whose answer fails the schema logs it (`review.schema_invalid`, the raw answer
 *     redacted); at the packet's second such miss it is tried ONCE on `review.second_levels.<level>`
 *     when the owner configured one (`schema-fallback.mjs`, B55; never in consensus mode).
 *     The summary then carries `schema_fallback: {level, provider, model}` (`level`:
 *     `review.second_levels.<level>`), the second level's own provider/model, and `tokens_in` /
 *     `tokens_out` summed over both sessions;
 *  7. a diff alone over the packet budget is `split_required` (with `tokens_in` and `budget`) —
 *     except a Markdown file (B54), which is reviewed SECTION BY SECTION (`sections.mjs`): every
 *     section packet runs the whole plan above (its own sessions, judge and stub guard, its own
 *     `review.plan` row with `section`), in file order, stopping at the first one that is not
 *     reviewed (the file is then `unavailable`). The file is approved only when EVERY section is;
 *     the findings of all sections are merged with ids `S<section>.<id>`, every session summary
 *     names its `section`, and `sections` lists them. A section that alone is still over the
 *     budget keeps the file `split_required` (plus `section`: its heading) — never truncated;
 *  8. code moved between this file and another changed file of the block (`input.peers`, both
 *     diffed against the block base) is named in a `## moved code` section of every packet —
 *     reviewers and judge; a Markdown section packet lists only the moves in that section
 *     (`moved.mjs`, B56). The peers' diffs are read once per review (`input.peerDiffs`, or a
 *     reader built from `input.peers`). It is extra text: every hunk still goes to the reviewer,
 *     and it counts toward the budget in the same measure.
 * Approval (until B12b's triage lands) = the final answer (the judge's in dual/consensus mode, else
 * the single reviewer's) passed the guard, says `passed: true`, and carries no `critical` or
 * `warning` finding.
 */

import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import path from 'node:path';
import { resolveLevel } from '../config/known-ids.mjs';
import { blockKind, isDocsKind } from '../decide/escalation.mjs';
import { fallbackRisk } from '../decide/fallback-rules.mjs';
import { tierFor } from '../proof/tiers.mjs';
import { assembleJudgePacket, assemblePacket, attachFiles, budgetFor, diffOnlyTokens, readFileDiff } from './packet.mjs';
import { movedText, movesFor, peerDiffReader } from './moved.mjs';
import { isMarkdownFile, packSections, sectionFindings, sectionGroups } from './sections.mjs';
import { FINDING_SCHEMA, minTokensOut, validateReview } from './validate-review.mjs';
import { spawnWithTimeoutRetry } from './session-retry.mjs';
import { afterSchemaMiss, logSecondLevelMiss } from './schema-fallback.mjs';
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
 * `review.single_reviewer_max_risk` (B34, default 1): at or below it ONE reviewer runs, no judge.
 * @param {Record<string, any> | undefined} cfg @returns {number}
 */
export function singleReviewerMaxRisk(cfg) {
  const v = cfg?.review?.single_reviewer_max_risk;
  // `validate` rejects anything outside 0–3 (schema); this guard only covers an unvalidated config
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 && v <= 3 ? v : 1;
}

/**
 * The review topology — the ONE place it is decided (B34):
 *  - consensus runs only when `review.multimodel` is true, the block is not a docs/contract block
 *    (unless `review.multimodel_for_docs` is true), and `risk > review.single_reviewer_max_risk`;
 *  - otherwise adaptive: `risk ≤ single_reviewer_max_risk` or `risk < 2` ⇒ one L2 session
 *    (`quick` below risk 1, else `full`); else two blind lenses + an L3 judge.
 * @param {{risk: number, cfg: Record<string, any>, kind?: unknown}} opts - `kind`: the block kind
 *   (`blockKind`), default `code`.
 * @returns {SessionPlan}
 */
export function planSessions({ risk, cfg, kind = 'code' }) {
  if (typeof risk !== 'number' || !Number.isFinite(risk) || risk < 0 || risk > 3) throw new TypeError('planSessions: risk must be a number from 0 to 3');
  const judge = /** @type {SessionSpec} */ ({ lens: 'judge', role: 'judge', level: 'L3', slot: null });
  const single = risk <= singleReviewerMaxRisk(cfg);
  const docsOff = isDocsKind(kind) && cfg?.review?.multimodel_for_docs !== true;
  if (cfg?.review?.multimodel === true && !single && !docsOff) {
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
  if (risk < 2 || single) return { mode: 'adaptive', depth: 'full', sessions: [{ lens: 'full', role: 'reviewer', level: 'L2', slot: null }] };
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
 * @property {string} [kind] - the block kind (`code`, `docs`, `contract`; B34); absent ⇒ `blockKind` of the reviewed file.
 * @property {string} [rulesDigest] @property {string} [factsExcerpt]
 * @property {string} workDir - where packet files are written (under the run's temp root).
 * @property {ReadonlyArray<string>} [peers] - B56: the block's OTHER changed files (repo-relative);
 *   code moved between this file and one of them is named in every packet (`## moved code`).
 * @property {() => Promise<import('./moved.mjs').PeerDiff[]>} [peerDiffs] - B56: a memoised reader
 *   of those files' diffs (`peerDiffReader`), shared by the caller across the round; wins over `peers`.
 */

/**
 * @typedef {object} ReviewDeps
 * @property {(opts: Record<string, any>) => Promise<Record<string, any>>} spawn - one isolated
 *   session (`spawnSession` opts minus the worker-pinned ones); a per-call `cfg` selects another
 *   provider's level for the same slot (consensus).
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeRow] - ledger rows
 *   (`review.plan`, `review.unavailable`, `review.schema_invalid`, `review.schema_fallback`); a
 *   failed write never fails the review (a failed `review.schema_fallback` write means no try).
 * @property {import('./schema-fallback.mjs').SchemaHistory} [schemaHistory] - B55: the packet's
 *   signed schema-miss history (the worker's); absent ⇒ no second-level try.
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
  // no recorded kind ⇒ the reviewed file's own kind (a lone `.md` is a docs review), never a blind `code`
  const kind = typeof input.kind === 'string' ? input.kind : blockKind({ owned: [diff.file] });
  const plan = planSessions({ risk, cfg, kind });
  const head = { engine: plan.mode, depth: plan.depth, risk, file: diff.file };
  if (plan.refused) return { status: 'refused', reason: plan.refused, approved: false, ...head, sessions: [] };
  if (diff.diffText.trim().length === 0) return { status: 'no_change', approved: false, ...head, sessions: [] };
  mkdirSync(workDir, { recursive: true, mode: 0o700 });
  // B56: the file's moves, read once; every packet and every measure of a part uses `movedOf`
  /** @type {import('./moved.mjs').Moves | null} */
  let moves = null;
  try {
    const peerDiffs = input.peerDiffs ?? peerDiffReader({ repoRoot, base: input.base, file: diff.file, peers: input.peers });
    moves = await movesFor({ diff, peerDiffs });
  } catch {
    moves = null; // no hint; the review goes on
  }
  /** @param {ReadonlyArray<number> | null} keys - a Markdown section's group keys; null ⇒ the whole file. */
  const movedOf = (keys) => movedText(moves, diff.content, keys);
  const min = minTokensOut(cfg);
  // B55: never in consensus mode — its providers (two reviewers, the judge) are the check itself
  const schemaFallback = plan.mode !== 'consensus';

  /** @param {SessionSpec} spec @param {{text: string, hunkHeaders: string[], tokensIn: number, contextMode: string | null}} packet */
  const run = async (spec, packet) => {
    /** @type {Record<string, any>} */
    let summary = await runOne(spec, packet.text, packet, { deps, workDir, min, cfg, schemaFallback });
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
      summary = { ...(await runOne(spec, `${packet.text}${extra}`, packet, { deps, workDir, min, cfg, schemaFallback })), needs_file_round: true };
    }
    return summary;
  };

  const reviewers = plan.sessions.filter((s) => s.role === 'reviewer');
  const judgeSpec = plan.sessions.find((s) => s.role === 'judge');
  /** @param {import('./packet.mjs').FileDiff} part @param {ReadonlyArray<number> | null} [keys] */
  const packetsFor = (part, keys = null) => reviewers.map((spec) => assemblePacket({ diff: part, lens: spec.lens, rulesDigest: input.rulesDigest, factsExcerpt: input.factsExcerpt, cfg, moved: movedOf(keys) }));

  /**
   * The plan on one packet set (the whole file, or one Markdown section): reviewers, then the judge.
   * @param {import('./packet.mjs').FileDiff} part @param {Array<Record<string, any>>} packets
   * @param {Record<string, any>} [rowExtra] - added to the `review.plan` row (`section`).
   * @param {ReadonlyArray<number> | null} [keys] - the section's group keys (its moved-code hint).
   * @returns {Promise<{status: 'reviewed' | 'unavailable', reason?: string, approved: boolean, summary?: string, findings?: Array<Record<string, any>>, sessions: Array<Record<string, any>>}>}
   */
  const reviewPart = async (part, packets, rowExtra = {}, keys = null) => {
    await note(deps, { event: 'review.plan', depth_unconstrained: plan.depth, depth_chosen: plan.depth, degrade_step: 0, risk, context_mode: packets[0]?.contextMode ?? null, forecast_tokens: packets.reduce((n, p) => n + p.tokensIn, 0), ...rowExtra });
    const done = await Promise.all(reviewers.map((spec, i) => run(spec, /** @type {any} */ (packets[i]))));
    const sessions = done.map(publicSummary);
    const failed = done.find((s) => s.status !== 'ok');
    if (failed) {
      await note(deps, { event: 'review.unavailable', reason: failed.reason, lens: failed.lens, ...rowExtra });
      return { status: 'unavailable', reason: failed.reason, approved: false, sessions };
    }
    let final = done[0];
    if (judgeSpec) {
      const bySlot = Object.fromEntries(done.map((s) => [s.slot, s.review]));
      const packet = assembleJudgePacket({ diff: part, reports: { A: bySlot.A, B: bySlot.B }, cfg, moved: movedOf(keys) });
      final = await runOne(judgeSpec, packet.text, packet, { deps, workDir, min, cfg, schemaFallback });
      sessions.push(publicSummary(final));
      if (final.status !== 'ok') {
        await note(deps, { event: 'review.unavailable', reason: final.reason, lens: 'judge', ...rowExtra });
        return { status: 'unavailable', reason: final.reason, approved: false, sessions };
      }
    }
    const findings = final.review.findings;
    const approved = final.review.passed === true && findings.every((/** @type {any} */ f) => f.severity === 'nit');
    return { status: 'reviewed', approved, summary: final.review.summary, findings, sessions };
  };

  const packets = packetsFor(diff);
  const split = /** @type {{status: 'split_required', tokensIn: number, budget: number} | undefined} */ (packets.find((p) => p.status === 'split_required'));
  if (!split) {
    const { status, reason, approved, summary, findings, sessions } = await reviewPart(diff, packets);
    if (status !== 'reviewed') return { status, reason, approved: false, ...head, sessions };
    return { status, approved, ...head, summary, findings, sessions };
  }
  if (!isMarkdownFile(diff.file)) return { status: 'split_required', approved: false, ...head, tokens_in: split.tokensIn, budget: split.budget, sessions: [] };

  // B54: a Markdown file over the budget is reviewed section by section
  /**
   * @param {import('./packet.mjs').FileDiff} part @param {ReadonlyArray<{key: number}> | null} [groups] - null ⇒ the whole file.
   * @returns {number} the diff-only packet tokens, largest over the lenses.
   */
  const measure = (part, groups = null) => {
    const moved = movedOf(groups === null ? null : groups.map((g) => g.key));
    return Math.max(...reviewers.map((spec) => diffOnlyTokens({ diff: part, lens: spec.lens, moved })));
  };
  const packed = packSections({ diff, budget: split.budget, measure });
  /**
   * A `split_required` outcome, its size always numeric (measured here when the packet lacks it).
   * @param {import('./packet.mjs').FileDiff} part @param {{tokensIn?: unknown, budget?: unknown}} over @param {string | null} section
   * @param {ReadonlyArray<{key: number}> | null} [groups] - the part's heading groups (its moved-code hint); null ⇒ the whole file.
   */
  const splitOutcome = (part, over, section, groups = null) => ({
    status: 'split_required',
    approved: false,
    ...head,
    tokens_in: Number.isFinite(over.tokensIn) ? over.tokensIn : measure(part, groups),
    budget: Number.isFinite(over.budget) ? over.budget : split.budget,
    ...(section ? { section } : {}),
    sessions: [],
  });
  if (packed.status !== 'ok') {
    // a named section is measured on its own diff, never the whole file's
    const group = packed.section ? sectionGroups(diff)?.find((g) => g.heading === packed.section) : undefined;
    return splitOutcome(group ? group.diff : diff, packed, packed.section, group ? [group] : null);
  }
  // never approve with no section: no packet ⇒ no session ⇒ split_required at the whole size
  if (packed.sections.length === 0) return splitOutcome(diff, split, null);
  // every section packet is assembled BEFORE any session runs. Packing and `assemblePacket` share
  // one measure, so with the real packer none is ever not `ok`: this is a DEFENSIVE guard (a test
  // reaches it through a mocked packer) — one that is not `ok` stops the file as split_required,
  // sized on that section's own diff
  const sectionPackets = packed.sections.map((section) => packetsFor(section.diff, section.keys));
  for (const [i, set] of sectionPackets.entries()) {
    const over = /** @type {{tokensIn?: unknown, budget?: unknown} | undefined} */ (set.find((p) => p.status !== 'ok'));
    if (over) return splitOutcome(packed.sections[i].diff, over, packed.sections[i].headings[0] ?? '(before first heading)', packed.sections[i].keys.map((key) => ({ key })));
  }
  const sections = packed.sections.map((s) => ({ index: s.index, headings: s.headings, hunks: s.diff.hunks.length }));
  /** @type {Array<Record<string, any>>} */
  const sessions = [];
  /** @type {Array<Record<string, any>>} */
  const findings = [];
  const summaries = [];
  let approved = true;
  for (const [i, section] of packed.sections.entries()) {
    const part = await reviewPart(section.diff, sectionPackets[i], { section: section.index, sections: packed.sections.length }, section.keys);
    sessions.push(...part.sessions.map((s) => ({ ...s, section: section.index })));
    if (part.status !== 'reviewed') return { status: part.status, reason: part.reason, approved: false, ...head, sections, sessions };
    approved = approved && part.approved;
    findings.push(...sectionFindings(/** @type {Array<{id: string}>} */ (part.findings), section.index));
    summaries.push(`S${section.index}: ${part.summary}`);
  }
  return { status: 'reviewed', approved, ...head, summary: summaries.join('\n'), findings, sessions, sections };
}

/**
 * One session: packet file (0600, removed after), spawn, stub guard — and, on a `schema` miss, the
 * B55 second-level try (`schema-fallback.mjs`) on the same packet file before it is removed.
 * @param {SessionSpec} spec @param {string} text
 * @param {{hunkHeaders: string[], tokensIn: number, contextMode: string | null}} packet
 * @param {{deps: ReviewDeps, workDir: string, min: number, cfg: Record<string, any>, schemaFallback?: boolean}} env -
 *   `schemaFallback: false` (consensus mode: its providers ARE the check) never tries.
 */
async function runOne(spec, text, packet, { deps, workDir, min, cfg, schemaFallback = true }) {
  const promptPath = path.join(workDir, `${spec.lens}-${randomBytes(6).toString('hex')}.md`);
  const base = { lens: spec.lens, role: spec.role, level: spec.level, slot: spec.slot, context_mode: packet.contextMode, ctx_tokens_in: packet.tokensIn };
  writeFileSync(promptPath, text, { mode: 0o600 });
  /** @type {Record<string, any> | null} */
  let res = null;
  let attempts = 0;
  /** @type {import('./validate-review.mjs').Verdict} */
  let verdict = { ok: false, reason: 'exit', detail: 'no session result' };
  /** @type {{level: string, provider: string, model: string} | null} */
  let fellBackTo = null;
  /** @param {Record<string, any> | undefined} sessionCfg @param {number} done - attempts before this call. */
  const attempt = (sessionCfg, done) =>
    // a `timeout` is spawned once more on the same packet and level (B30); a spawn that throws
    // (bad config, refused argv) is an `exit` failure, never approval
    spawnWithTimeoutRetry(
      deps.spawn,
      {
        level: spec.level,
        role: spec.role,
        promptPath,
        schema: FINDING_SCHEMA,
        rowExtra: { lens: spec.lens, context_mode: packet.contextMode, ctx_tokens_in: packet.tokensIn },
        ...(sessionCfg ? { cfg: sessionCfg } : {}),
      },
      (row) => note(deps, row),
      (n) => {
        attempts = done + n;
      },
    );
  /** @type {number | null} the session's tokens: the first spawn's, plus a second-level try's */
  let tokensIn = null;
  /** @type {number | null} */
  let tokensOut = null;
  // the packet file is removed on every path, and only after the second-level try (which reads it)
  try {
    try {
      ({ res, attempts } = await attempt(spec.cfg, 0));
    } catch {
      // defensive only: the helper never throws. If it ever did, it is still an `exit` failure,
      // never approval, with the attempts it had reported so far (at least 1).
      res = null;
      attempts = Math.max(attempts, 1);
    }
    tokensIn = res?.usage?.tokens_in ?? null;
    tokensOut = res?.usage?.tokens_out ?? null;
    verdict = validateReview(res, { hunkHeaders: packet.hunkHeaders, minTokensOut: min });
    if (!verdict.ok && verdict.reason === 'schema') {
      try {
        const session = { lens: spec.lens, role: spec.role, level: spec.level };
        const done = attempts;
        // no ledger writer ⇒ nothing logged, nothing tried; every row goes through the same writer
        // (each helper handles its own failed write)
        const write = deps.writeRow ? (/** @type {Record<string, any>} */ row) => /** @type {NonNullable<ReviewDeps['writeRow']>} */ (deps.writeRow)(row) : undefined;
        const second = await afterSchemaMiss({
          res,
          session,
          packetText: text,
          cfg: spec.cfg ?? cfg,
          allowed: schemaFallback && !spec.cfg,
          history: deps.schemaHistory,
          writeRow: write,
          spawnAt: (c) => attempt(c, done),
        });
        if (second) {
          const secondVerdict = validateReview(second.res, { hunkHeaders: packet.hunkHeaders, minTokensOut: min });
          res = second.res;
          attempts = done + second.attempts;
          fellBackTo = second.to;
          verdict = secondVerdict;
          tokensIn = addTokens(tokensIn, second.res?.usage?.tokens_in);
          tokensOut = addTokens(tokensOut, second.res?.usage?.tokens_out);
          if (!verdict.ok && verdict.reason === 'schema') await logSecondLevelMiss({ res, session, packetText: text, writeRow: write });
        }
      } catch {
        // defensive only: the helpers never throw. If they ever did, the first session's result
        // and its `schema` verdict stand (never turned into `exit`, never approval)
      }
    }
  } finally {
    rmSync(promptPath, { force: true });
  }
  const meta = {
    ...base,
    provider: res?.provider ?? null,
    model: res?.model ?? null,
    fallback_step: res?.fallback_step ?? 0,
    attempts,
    ...(fellBackTo ? { schema_fallback: fellBackTo } : {}),
    tokens_in: tokensIn,
    tokens_out: tokensOut,
  };
  if (!verdict.ok) return { ...meta, status: 'unavailable', reason: verdict.reason, review: null };
  return { ...meta, status: 'ok', reason: null, review: verdict.review };
}

/**
 * Two token counts added; a missing one (not a number) adds nothing, both missing ⇒ null.
 * @param {unknown} a @param {unknown} b @returns {number | null}
 */
function addTokens(a, b) {
  if (typeof a !== 'number') return typeof b === 'number' ? b : null;
  return typeof b === 'number' ? a + b : a;
}

/** The per-session summary the worker signs: the answer's verdict and findings, never the packet. */
function publicSummary(/** @type {Record<string, any>} */ s) {
  const { review, ...rest } = s;
  return review ? { ...rest, passed: review.passed === true, findings: review.findings } : rest;
}
