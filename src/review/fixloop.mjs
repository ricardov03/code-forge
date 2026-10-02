/**
 * The fix loop — the §4.11 convergence rule, per file, per block (plan §4.2 step 5, §4.11, §3.6
 * rules 2b/2/6, R1; block B12b).
 *
 *  1. Round 1 is the tiered review of §4.2 (`deps.review`, the B12a engine) triaged by
 *     `triage.mjs`; its `fix_now` set is the open set.
 *  2. Round n ≥ 2 reviews ONLY the fix hunks: the diff between the content reviewed in round n−1
 *     and the current content (`fixHunkDiff`), in the hunk form (`contextMode: 'recheck'`,
 *     ± `hunk_context_lines`), with the open findings listed by id and the `recheck` lens
 *     (`review.recheck_scope: file` re-sends the whole packet against the block base instead).
 *     Per open finding S1 `resolved` ≥ 0.90 closes it; when any stays open, ONE fresh L2 recheck
 *     session decides. A new finding inside a fix hunk is triaged and may join the open set; one
 *     OUTSIDE every fix hunk is a `late_finding` (ledger `review.late_finding`), never blocks the
 *     round (`review.late_findings: sweep`, default; `block` makes it an open finding instead).
 *  3. Strictly shrinking: open(n) ≥ open(n−1) ⇒ `trigger: review_stall` ⇒ +1 level now (rule 2b).
 *  4. Ladder: `escalation.review_rounds_per_level` (2) exhausted with an open finding ⇒ +1 level
 *     (rule 2); the next rounds are coded at that level by a fresh session (`deps.fix`).
 *  5. Cap: `review.max_rounds_per_file` (4) reached with an open finding ⇒ the L3 patch rung
 *     (rule 6; once per block — shared with rule 5's ceiling), whose `patch_check` round is
 *     OUTSIDE the cap. The rung is also rule 5: rule 2/2b at the running ceiling (L2 — L3 is never
 *     a coding level, R1) takes it, so for an L2 block the rung IS the +1 at round 2. After the
 *     patch the block continues at L2 (§3.6, Fable ruling A): `l3_rung_used = true`,
 *     `rounds_at_level = 0`, and the `patch_check` goes through `decideNext` like any recheck —
 *     clean ⇒ complete; open set not smaller than before the patch ⇒ `stopped: l3_patch_exhausted`
 *     (`trigger: review_stall`); round ≥ cap ⇒ `stopped: review_cap`; else `fix` at L2 with
 *     `l3_patch: true` in the brief. A later stall ⇒ `l3_patch_exhausted`; round 4 still open ⇒
 *     `review_cap` (the rung is spent). Either way the ledger `review.cap` row lists the open
 *     ids — the human decides (fix by hand, `block waive`, or re-decompose).
 *  6. Ledger per round: `review.round {file, round, level, kind, open_before, closed,
 *     new_in_hunks, late, open_after, tokens_in, tokens_out}`; a converged file gets
 *     `review.approved {file, content_hash, round}` (the worker signs every row it writes).
 *
 * Over budget (B12a follow-up, decided here): a recheck packet over `review.budgets.full_in`
 * falls to `contextMode: 'minimal'` (± `min_context_lines` around the SAME fix hunks) exactly as
 * `assemblePacket` does for any packet. The budget is a hard ceiling on what one session reads;
 * `minimal` is still fix hunks only, and the recheck's DEPTH (one session, the recheck lens) is
 * never degraded (§4.10). A packet that is still not `ok` (`split_required`: the fix hunks alone,
 * with no context at all, exceed the budget) is deterministic — re-running the round would build
 * the same packet — so it is TERMINAL, never a retry: `stopped`, `next: {action: 'stop', reason:
 * <packet.status>}`, and the gate-relevant `review.cap` row (the gate then refuses the file's open
 * findings until a human waives them or the file is reviewed again).
 *
 * A session that fails the stub guard is `unavailable`, and a failed write of a gate-relevant row
 * (`review.late_finding`, `review.round`, `review.cap`) is `ledger_write_failed`: either way the
 * round is not counted, nothing closes, and `next.action: 'retry'` with `pending_kind` set —
 * `converge()` re-runs that round on its next call.
 */

import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import path from 'node:path';
import { escalationAfterAttempt, reviewRoundStalled } from '../decide/escalation.mjs';
import { exec } from '../util/exec.mjs';
import { contentHash, gitChildEnv } from '../worker/ticket.mjs';
import { parseDiff } from './context.mjs';
import { assemblePacket, assertPacketPath, readFileDiff } from './packet.mjs';
import { cameFromJudge, resolvedBand, triageFindings } from './triage.mjs';
import { FINDING_SCHEMA, minTokensOut, validateReview } from './validate-review.mjs';
import { spawnWithTimeoutRetry } from './session-retry.mjs';

/** @typedef {import('./triage.mjs').Finding} Finding */
/** @typedef {import('./context.mjs').Hunk} Hunk */

/**
 * @typedef {object} NextStep
 * @property {'fix' | 'patch' | 'complete' | 'stop' | 'retry'} action
 * @property {string} [level] - the level the next fix is coded at.
 * @property {string | null} [trigger] - the escalation trigger that moved the level, if any.
 * @property {string} [reason] - `review_cap`, `l3_patch_exhausted`, a non-ok packet status (`split_required`), … for `stop`; the stub-guard reason for `retry`.
 */

/**
 * @typedef {object} FileState - JSON-serialisable; the caller keeps it between `review-file` calls.
 * @property {string} file
 * @property {string} level - the level the file's fixes are coded at now.
 * @property {number} round - rounds counted so far (patch_check rounds excluded).
 * @property {number} rounds_at_level
 * @property {Finding[]} open - the open `fix_now` set.
 * @property {Finding[]} late
 * @property {string | null} reviewed_content - the content the last counted round reviewed.
 * @property {boolean} l3_rung_used - the block's L3 rung (rule 5 or 6) was used — once per block.
 * @property {'open' | 'complete' | 'stopped'} status
 * @property {NextStep | null} next
 * @property {{context_mode: string, context_lines: number} | null} [last_packet] - the last recheck packet's context.
 * @property {'full' | 'recheck' | 'patch_check' | null} [pending_kind] - the round to re-run after a `retry`.
 */

/**
 * @typedef {object} LoopDeps
 * @property {string} repoRoot - realpath'd.
 * @property {Record<string, any>} [cfg]
 * @property {string} workDir - packet and diff scratch (under the run's temp root).
 * @property {() => Promise<Record<string, any>>} review - round 1: the B12a engine (`reviewFile`).
 * @property {(opts: Record<string, any>) => Promise<Record<string, any>>} [spawn] - one isolated session.
 * @property {import('./triage.mjs').JevAsk} [jev]
 * @property {import('./triage.mjs').RuleFn} [rule]
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeRow]
 * @property {(req: {file: string, round: number, level: string, open: Finding[], l3_patch: boolean}) => Promise<unknown>} [fix] - a fresh coder session at `level`; `l3_patch` = the block's L3 patch is in the tree (§3.6).
 * @property {(req: {file: string, level: 'L3', open: Finding[]}) => Promise<unknown>} [patch] - the L3 patch rung (≤ 80 lines, owned files only).
 * @property {string | null} [base] - the block base (only for `recheck_scope: file`).
 * @property {string} [rulesDigest] @property {string} [factsExcerpt]
 */

/** @param {LoopDeps} deps @param {Record<string, any>} row */
async function note(deps, row) {
  try {
    await deps.writeRow?.(row);
  } catch {
    // the loop's verdict stands; the ledger row is best-effort
  }
}

/** @param {Record<string, any> | undefined} cfg */
function settings(cfg) {
  const r = cfg?.review ?? {};
  const e = cfg?.escalation ?? {};
  const int = (/** @type {unknown} */ v, /** @type {number} */ d) => (Number.isInteger(v) && /** @type {number} */ (v) >= 0 ? /** @type {number} */ (v) : d);
  return {
    maxRounds: int(r.max_rounds_per_file, 4),
    scope: r.recheck_scope === 'file' ? 'file' : 'fix_hunks',
    late: r.late_findings === 'block' ? 'block' : 'sweep',
    perLevel: int(e.review_rounds_per_level, 2),
    stopAt: typeof e.stop_at === 'string' ? e.stop_at : 'L3',
    l3Mode: e.l3_mode === 'code' ? 'code' : 'patch',
  };
}

/**
 * @param {{file: string, level: string, l3RungUsed?: boolean}} opts
 * @returns {FileState}
 */
export function newFileState({ file, level, l3RungUsed = false }) {
  return { file, level, round: 0, rounds_at_level: 0, open: [], late: [], reviewed_content: null, l3_rung_used: l3RungUsed, status: 'open', next: null, last_packet: null, pending_kind: null };
}

/** @param {string} repoRoot @param {string} file @returns {{rel: string, content: string, hash: string}} */
function readCurrent(repoRoot, file) {
  const rel = assertPacketPath(repoRoot, file);
  const hash = contentHash(repoRoot, rel);
  let content = '';
  try {
    content = readFileSync(path.join(repoRoot, rel), 'utf8');
  } catch {
    content = ''; // deleted: the fix diff is all `-` lines
  }
  return { rel, content, hash };
}

/**
 * The fix hunks: `git diff --no-index` between the content reviewed last round and the current
 * content, rewritten to name `file` on both sides. Unchanged content ⇒ no hunks.
 * @param {{file: string, previous: string, current: string, workDir: string}} opts
 * @returns {Promise<import('./packet.mjs').FileDiff>}
 */
export async function fixHunkDiff({ file, previous, current, workDir }) {
  mkdirSync(workDir, { recursive: true, mode: 0o700 });
  const tag = randomBytes(6).toString('hex');
  const before = path.join(workDir, `prev-${tag}`);
  const after = path.join(workDir, `cur-${tag}`);
  writeFileSync(before, previous, { mode: 0o600 });
  writeFileSync(after, current, { mode: 0o600 });
  try {
    const res = await exec(['git', 'diff', '--no-index', '--no-color', '--no-ext-diff', '--no-textconv', '-U3', '--', before, after], {
      cwd: workDir,
      env: gitChildEnv(),
      timeoutMs: 30000,
      okExitCodes: [0, 1],
    });
    if (res.result !== 'ok') throw new Error(`git diff --no-index failed (exit ${res.code})`);
    const at = res.stdout.indexOf('\n@@');
    const body = res.stdout.startsWith('@@') ? res.stdout : at >= 0 ? res.stdout.slice(at + 1) : '';
    const diffText = body.length > 0 ? `--- a/${file}\n+++ b/${file}\n${body}` : '';
    return { file, kind: 'tracked', diffText, content: current, ...parseDiff(diffText) };
  } finally {
    rmSync(before, { force: true });
    rmSync(after, { force: true });
  }
}

/**
 * The recheck packet: the `recheck` lens on the fix hunks (hunk form, never the whole file), then
 * the open findings by id. Over budget ⇒ `minimal` (see the module doc).
 * @param {{diff: import('./packet.mjs').FileDiff, open: Finding[], cfg?: Record<string, any>, contextMode?: 'auto' | 'recheck', rulesDigest?: string, factsExcerpt?: string}} opts
 */
export function buildRecheckPacket({ diff, open, cfg, contextMode = 'recheck', rulesDigest, factsExcerpt }) {
  const packet = assemblePacket({ diff, lens: 'recheck', cfg, contextMode, rulesDigest, factsExcerpt });
  if (packet.status !== 'ok') return packet;
  const listed = open.map((f) => `- ${f.id} (${f.severity}, lines ${f.line_start}-${f.line_end}): ${f.claim ?? ''}`);
  const text = `${packet.text}## open findings\n${listed.length > 0 ? listed.join('\n') : '(none)'}\n`;
  return { ...packet, text, tokensIn: packet.tokensIn + Math.ceil(Buffer.byteLength(text.slice(packet.text.length)) / 4) };
}

/** @param {Finding} f @param {ReadonlyArray<Hunk>} hunks @returns {boolean} */
export function insideHunks(f, hunks) {
  return hunks.some((h) => {
    const last = h.newStart + Math.max(h.newLines, 1) - 1;
    return f.line_start >= h.newStart && f.line_end <= last && f.line_start <= f.line_end;
  });
}

/**
 * One recheck session through the stub guard.
 * @param {{text: string, hunkHeaders: string[], tokensIn: number, contextMode: string}} packet
 * @param {LoopDeps} deps
 */
async function recheckSession(packet, deps) {
  mkdirSync(deps.workDir, { recursive: true, mode: 0o700 });
  const promptPath = path.join(deps.workDir, `recheck-${randomBytes(6).toString('hex')}.md`);
  writeFileSync(promptPath, packet.text, { mode: 0o600 });
  /** @type {Record<string, any> | null} */
  let res = null;
  try {
    // a `timeout` is spawned once more on the same packet and level (B30)
    res = deps.spawn
      ? (
          await spawnWithTimeoutRetry(
            deps.spawn,
            { level: 'L2', role: 'reviewer', promptPath, schema: FINDING_SCHEMA, rowExtra: { lens: 'recheck', context_mode: packet.contextMode, ctx_tokens_in: packet.tokensIn } },
            (row) => note(deps, row),
          )
        ).res
      : null;
  } catch {
    res = null; // the helper never throws; if it ever did, it is still an `exit` failure
  } finally {
    rmSync(promptPath, { force: true });
  }
  const verdict = validateReview(res, { hunkHeaders: packet.hunkHeaders, minTokensOut: minTokensOut(deps.cfg) });
  return { verdict, tokens_in: res?.usage?.tokens_in ?? null, tokens_out: res?.usage?.tokens_out ?? null };
}

/**
 * After a counted round with open findings: cap first (rule 6), else stall / ladder (2b, 2).
 * @param {FileState} state @param {number} openBefore @param {ReturnType<typeof settings>} s
 * @param {'full' | 'recheck' | 'patch_check'} kind - a `patch_check` compares against the open
 *   set before the patch (the rung's own shrink test), whatever the round number.
 * @returns {NextStep}
 */
function decideNext(state, openBefore, s, kind) {
  if (state.open.length === 0) return { action: 'complete' };
  if (kind === 'patch_check') {
    // the block's one rung is spent: a patch that did not shrink the set stops FIRST — even when
    // the rung was taken at the cap — then the cap; else the block continues at L2 below
    if (reviewRoundStalled(openBefore, state.open.length)) return { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' };
    if (state.round >= s.maxRounds) return { action: 'stop', reason: 'review_cap', trigger: 'review_cap' };
  }
  if (state.round >= s.maxRounds) {
    const esc = escalationAfterAttempt({ currentLevel: state.level, roundsAtFile: state.round, maxRoundsPerFile: s.maxRounds, openFixNowFinding: true, l3RungAlreadyUsed: state.l3_rung_used });
    return esc.action === 'l3_rung' ? { action: 'patch', level: 'L3', trigger: 'review_cap' } : { action: 'stop', reason: 'review_cap', trigger: 'review_cap' };
  }
  const esc = escalationAfterAttempt({
    currentLevel: state.level,
    reviewStall: (kind === 'patch_check' || state.round >= 2) && reviewRoundStalled(openBefore, state.open.length),
    openFixNowFinding: true,
    reviewRoundsAtLevel: state.rounds_at_level,
    reviewRoundsPerLevel: s.perLevel,
    stopAt: s.stopAt,
    l3Mode: /** @type {'patch' | 'code'} */ (s.l3Mode),
    l3RungAlreadyUsed: state.l3_rung_used,
  });
  if (esc.action === 'escalate' && esc.level) {
    if (esc.level !== state.level) state.rounds_at_level = 0;
    state.level = esc.level;
    return { action: 'fix', level: state.level, trigger: esc.trigger };
  }
  if (esc.action === 'l3_rung') return { action: 'patch', level: 'L3', trigger: esc.trigger };
  if (esc.action === 'stop') return { action: 'stop', reason: esc.reason ?? 'stop', trigger: esc.trigger };
  return { action: 'fix', level: state.level, trigger: null };
}

/**
 * Write a row the gate or the convergence rule depends on (`review.late_finding`, `review.round`,
 * `review.cap`). No writer ⇒ nothing to write; a writer that throws ⇒ false.
 * @param {LoopDeps} deps @param {Record<string, any>} row @returns {Promise<boolean>}
 */
async function mustNote(deps, row) {
  if (!deps.writeRow) return true;
  try {
    await deps.writeRow(row);
    return true;
  } catch {
    return false;
  }
}

/**
 * @param {FileState} state @param {'full' | 'recheck' | 'patch_check'} kind @param {string} reason
 * @returns {FileState}
 */
function retry(state, kind, reason) {
  state.next = { action: 'retry', reason };
  state.pending_kind = kind;
  return state;
}

/**
 * A terminal stop that is NOT a counted round (a non-ok recheck packet): the round's rows are
 * not written, the open set stays as it was, and the gate-relevant `review.cap` row goes through
 * the must-write path — a failed write is a `retry`, never a silent stop.
 * @param {FileState} state @param {LoopDeps} deps
 * @param {'full' | 'recheck' | 'patch_check'} kind @param {string} file @param {string} reason
 * @returns {Promise<FileState>}
 */
async function stopBeforeRound(state, deps, kind, file, reason) {
  const row = { event: 'review.cap', file, round: state.round, reason, open: state.open.map((f) => f.id) };
  if (!(await mustNote(deps, row))) return retry(state, kind, 'ledger_write_failed');
  state.status = 'stopped';
  state.next = { action: 'stop', reason };
  state.pending_kind = null;
  return state;
}

/**
 * Run the file's next round (round 1, a recheck, or the `patch_check` after the L3 patch).
 * The outcome is staged on a copy of `state` and committed only after its gate-relevant rows
 * (`review.late_finding`, `review.round`, `review.cap`) are written; a failed write leaves the
 * state as it was with `next: {action: 'retry', reason: 'ledger_write_failed'}` (the round is
 * re-run, never closed as clean). `state.next` says what the caller does now.
 * @param {FileState} state @param {LoopDeps} deps
 * @param {{kind?: 'full' | 'recheck' | 'patch_check'}} [opts]
 * @returns {Promise<FileState>}
 */
export async function runRound(state, deps, opts = {}) {
  const s = settings(deps.cfg);
  const kind = opts.kind ?? (state.round === 0 ? 'full' : 'recheck');
  const current = readCurrent(deps.repoRoot, state.file);
  const openBefore = state.open.length;
  /** @type {FileState} */
  const draft = structuredClone({ ...state, next: null, pending_kind: null });
  let closed = 0;
  let newInHunks = 0;
  /** @type {Array<Record<string, any>>} */
  const lateRows = [];
  /** @type {number | null} */
  let tokensIn = null;
  /** @type {number | null} */
  let tokensOut = null;
  /** @type {{context_mode: string, context_lines: number} | null} */
  let packetInfo = null;

  if (kind === 'full') {
    const result = await deps.review();
    if (result?.status !== 'reviewed') return retry(state, kind, result?.reason ?? result?.status ?? 'unavailable');
    const triaged = await triageFindings({ file: current.rel, findings: result.findings ?? [], fromJudge: cameFromJudge(result), cfg: deps.cfg, jev: deps.jev, rule: deps.rule, writeRow: deps.writeRow });
    draft.open = triaged.fix_now;
    newInHunks = triaged.fix_now.length;
  } else {
    const diff =
      s.scope === 'file'
        ? await readFileDiff({ repoRoot: deps.repoRoot, file: current.rel, base: deps.base ?? null })
        : await fixHunkDiff({ file: current.rel, previous: state.reviewed_content ?? '', current: current.content, workDir: deps.workDir });
    if (diff.hunks.length > 0) {
      /** @type {Finding[]} */
      const pending = [];
      for (const finding of state.open) {
        const { band } = await resolvedBand({ finding, fixHunk: diff.diffText, jev: deps.jev, cfg: deps.cfg });
        if (band === 'closed') closed += 1;
        else pending.push(finding);
      }
      draft.open = [];
      if (pending.length > 0) {
        const packet = buildRecheckPacket({ diff, open: pending, cfg: deps.cfg, contextMode: s.scope === 'file' ? 'auto' : 'recheck', rulesDigest: deps.rulesDigest, factsExcerpt: deps.factsExcerpt });
        // deterministic (the same hunks build the same packet): terminal, never a retry
        if (packet.status !== 'ok') return stopBeforeRound(state, deps, kind, current.rel, packet.status);
        packetInfo = { context_mode: packet.contextMode, context_lines: packet.contextLines };
        const session = await recheckSession(packet, deps);
        tokensIn = session.tokens_in;
        tokensOut = session.tokens_out;
        if (!session.verdict.ok) {
          await note(deps, { event: 'review.unavailable', file: current.rel, lens: 'recheck', reason: session.verdict.reason });
          return retry(state, kind, session.verdict.reason ?? 'unavailable');
        }
        const review = /** @type {Record<string, any>} */ (session.verdict.review);
        const resolvedIds = new Set((review.resolved ?? []).filter((/** @type {any} */ r) => r?.resolved === true).map((/** @type {any} */ r) => r.id));
        const stillOpen = pending.filter((f) => !resolvedIds.has(f.id));
        closed += pending.length - stillOpen.length;
        /** @type {Finding[]} */
        const inside = [];
        /** @type {Finding[]} */
        const blocking = [];
        for (const f of /** @type {Finding[]} */ (review.findings ?? [])) {
          if (insideHunks(f, diff.hunks)) {
            inside.push(f);
            continue;
          }
          draft.late.push(f);
          lateRows.push({ event: 'review.late_finding', file: current.rel, round: state.round + 1, finding: f.id, severity: f.severity, line_start: f.line_start, line_end: f.line_end, mode: s.late });
          if (s.late === 'block' && f.severity !== 'nit') blocking.push(f);
        }
        const triaged = await triageFindings({ file: current.rel, findings: inside, fromJudge: false, diffText: diff.diffText, cfg: deps.cfg, jev: deps.jev, rule: deps.rule, writeRow: deps.writeRow });
        newInHunks = triaged.fix_now.length;
        draft.open = [...stillOpen, ...triaged.fix_now, ...blocking];
      }
    }
    // no fix hunks (the content did not change): nothing closes — the round stalls
  }

  if (kind !== 'patch_check') {
    draft.round += 1;
    draft.rounds_at_level += 1;
  } else {
    // the block continues at L2 after the rung with a fresh round counter (§3.6) — also here, not
    // only in `converge`, because the worker runs the patch_check from its own ticket
    draft.rounds_at_level = 0;
  }
  draft.reviewed_content = current.content;
  draft.last_packet = packetInfo;
  const roundRow = {
    event: 'review.round',
    file: current.rel,
    round: draft.round,
    level: draft.level,
    kind,
    open_before: openBefore,
    closed,
    new_in_hunks: newInHunks,
    late: lateRows.length,
    open_after: draft.open.length,
    tokens_in: tokensIn,
    tokens_out: tokensOut,
    ...(packetInfo ? { context_mode: packetInfo.context_mode } : {}),
  };

  if (draft.open.length === 0) {
    draft.status = 'complete';
    draft.next = { action: 'complete' };
  } else {
    // after a patch_check the block continues at L2 (§3.6): the rung is spent, so a stall now
    // stops (`l3_patch_exhausted`) and the cap stops (`review_cap`); a shrunk set is fixed at L2
    draft.next = decideNext(draft, openBefore, s, kind);
    if (draft.next.action === 'stop') draft.status = 'stopped';
  }
  const rows = [...lateRows, roundRow];
  if (draft.status === 'stopped') rows.push({ event: 'review.cap', file: current.rel, round: draft.round, reason: draft.next.reason, open: draft.open.map((f) => f.id) });
  for (const row of rows) {
    if (!(await mustNote(deps, row))) return retry(state, kind, 'ledger_write_failed');
  }
  Object.assign(state, draft);
  // best-effort: a missing approval fails closed at the block gate (`unreviewed`)
  if (state.status === 'complete') await note(deps, { event: 'review.approved', file: current.rel, content_hash: current.hash, round: state.round });
  return state;
}

/**
 * Drive one file to `complete` or `stopped` as `state.next` says: round 1, then fix ⇒ recheck
 * rounds, the L3 patch ⇒ `patch_check` once. A `retry` left by an earlier call (an unavailable
 * session, a failed ledger write) re-runs the pending round first — the fix is not re-coded; a
 * new `retry` in this call returns to the caller.
 * @param {FileState} state @param {LoopDeps} deps
 * @returns {Promise<FileState>}
 */
export async function converge(state, deps) {
  if (state.next?.action === 'retry') {
    const kind = state.pending_kind ?? (state.round === 0 ? 'full' : 'recheck');
    state.next = null;
    state.pending_kind = null;
    await runRound(state, deps, { kind });
  } else if (state.round === 0 && state.next === null) {
    await runRound(state, deps);
  }
  for (let guard = 0; guard < 32; guard += 1) {
    const next = state.next;
    if (!next || next.action === 'complete' || next.action === 'stop' || next.action === 'retry') return state;
    if (next.action === 'patch') {
      state.l3_rung_used = true;
      state.rounds_at_level = 0;
      await deps.patch?.({ file: state.file, level: 'L3', open: [...state.open] });
      await runRound(state, deps, { kind: 'patch_check' });
      continue;
    }
    await deps.fix?.({ file: state.file, round: state.round + 1, level: state.level, open: [...state.open], l3_patch: state.l3_rung_used });
    await runRound(state, deps);
  }
  return state;
}
