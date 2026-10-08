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
 *     (rule 2); the next rounds are coded at that level by a fresh session (`deps.fix`). Below L2,
 *     `escalation.after_rounds_with_warnings` (1) heavy rounds at the level — an open set with
 *     ≥ `escalation.warning_threshold` (2) warnings or any `critical` — climb at once (rule 2c,
 *     B34: `trigger: review_warnings`); `warn_rounds_at_level` counts them and resets on a climb.
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
 * Autopilot extra round (B47): `deps.extraRounds` (from {@link extraRoundsFor}: one per signed
 * `autopilot.extra_round` row for the file, one per grant) raises the file's cap by that many
 * rounds — the cap checks here and rule 6 in `escalationAfterAttempt` both use the raised cap. A
 * file already stopped at `review_cap` is reopened for its extra round by
 * {@link reopenForExtraRound} (the worker calls it before the round).
 *
 * Over budget (B12a follow-up, decided here): a recheck packet over `review.budgets.full_in`
 * falls to `contextMode: 'minimal'` (± `min_context_lines` around the SAME fix hunks) exactly as
 * `assemblePacket` does for any packet. The budget is a hard ceiling on what one session reads;
 * `minimal` is still fix hunks only, and the recheck's DEPTH (one session, the recheck lens) is
 * never degraded (§4.10). A packet that is still not `ok` (`split_required`: the fix hunks alone,
 * with no context at all, exceed the budget) is deterministic — re-running the round would build
 * the same packet — so it is TERMINAL, never a retry: `stopped`, `next: {action: 'stop', reason:
 * <packet.status>}`, and the gate-relevant `review.cap` row (the gate then refuses the file's open
 * findings until a human waives them or the file is reviewed again). The stop carries the packet's
 * `tokens_in` and `budget` (and a Markdown section's heading as `section`) in `next` and in the
 * `review.cap` row (B54).
 *
 * Markdown sections (B54): a Markdown file whose recheck packet is over the budget is rechecked
 * SECTION BY SECTION (`sections.mjs`, `packRecheck`: packets sized with their open-findings list)
 * instead: one recheck session per section packet, each listing only the open findings that fall
 * in it (`sectionOf`: the spanning or nearest section), each through the stub guard (its
 * own `reviewed_hunks`). A finding closes only when ITS section's session resolves it; new
 * findings get ids `S<section>.<id>`; any section session unavailable ⇒ `retry` of the whole
 * round. A section still over the budget alone ⇒ the terminal stop above (`split_required`).
 *
 * Moved code (B56): when `deps.peerDiffs` (or `deps.peers`) names the block's other changed files,
 * every recheck and patch_check packet carries the file's `## moved code` section — computed from
 * the file's diff against the block base (`deps.base`), never from the fix hunks, so a stale
 * "feature deleted" finding on code that moved to another file meets the same hint as round 1. A
 * Markdown section packet lists only the moves in its section. Sizing (`recheckTokens`,
 * `packRecheck`) uses the same section text. The peers are read once per deps object (one ticket):
 * the caller's `deps.peerDiffs` reader, else one memoised reader per deps (`recheckPeerDiffs`). Any
 * failure there means no hint, never a failed recheck.
 *
 * Schema misses (B55): a recheck (or patch_check) session whose answer fails the schema at its
 * packet's second miss is tried once on `review.second_levels.L2` when configured
 * (`schema-fallback.mjs`); only when that fails too is the round `unavailable` as below.
 *
 * A session that fails the stub guard is `unavailable`, and a failed write of a gate-relevant row
 * (`review.late_finding`, `review.round`, `review.cap`) is `ledger_write_failed`: either way the
 * round is not counted, nothing closes, and `next.action: 'retry'` with `pending_kind` set —
 * `converge()` re-runs that round on its next call.
 */

import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import path from 'node:path';
import { escalationAfterAttempt, heavyRound, reviewRoundStalled } from '../decide/escalation.mjs';
import { exec } from '../util/exec.mjs';
import { verifyRow } from '../state/signer.mjs';
import { decisionProblem, grantCoverProblem } from './gate-check.mjs';
import { contentHash, gitChildEnv } from '../worker/ticket.mjs';
import { parseDiff } from './context.mjs';
import { assemblePacket, assertPacketPath, diffOnlyTokens, readFileDiff } from './packet.mjs';
import { movedText, movesFor, peerDiffReader } from './moved.mjs';
import { isMarkdownFile, packSections, sectionFindings, sectionGroups, sectionOf } from './sections.mjs';
import { cameFromJudge, resolvedBand, triageFindings } from './triage.mjs';
import { FINDING_SCHEMA, minTokensOut, validateReview } from './validate-review.mjs';
import { spawnWithTimeoutRetry } from './session-retry.mjs';
import { afterSchemaMiss, logSecondLevelMiss } from './schema-fallback.mjs';

/** @typedef {import('./triage.mjs').Finding} Finding */
/** @typedef {import('./context.mjs').Hunk} Hunk */

/**
 * @typedef {object} NextStep
 * @property {'fix' | 'patch' | 'complete' | 'stop' | 'retry'} action
 * @property {string} [level] - the level the next fix is coded at.
 * @property {string | null} [trigger] - the escalation trigger that moved the level, if any.
 * @property {string} [reason] - `review_cap`, `l3_patch_exhausted`, a non-ok packet status (`split_required`), … for `stop`; the stub-guard reason for `retry`.
 * @property {number} [tokens_in] - a `split_required` stop: the estimated tokens of the diff-only packet (B54).
 * @property {number} [budget] - a `split_required` stop: the packet budget it exceeded (B54).
 * @property {string} [section] - a `split_required` stop of a Markdown recheck: the heading of the section that alone is over (B54).
 */

/**
 * @typedef {object} FileState - JSON-serialisable; the caller keeps it between `review-file` calls.
 * @property {string} file
 * @property {string} level - the level the file's fixes are coded at now.
 * @property {number} round - rounds counted so far (patch_check rounds excluded).
 * @property {number} rounds_at_level
 * @property {number} [warn_rounds_at_level] - heavy rounds at the current level (rule 2c, B34); absent in a state saved before B34 ⇒ 0.
 * @property {Finding[]} open - the open `fix_now` set.
 * @property {Finding[]} late
 * @property {string | null} reviewed_content - the content the last counted round reviewed.
 * @property {boolean} l3_rung_used - the block's L3 rung (rule 5 or 6) was used — once per block.
 * @property {'open' | 'complete' | 'stopped'} status
 * @property {NextStep | null} next
 * @property {{context_mode: string, context_lines: number} | null} [last_packet] - the last recheck round's context: one packet's, or over a Markdown recheck's section packets the summed lines and the one mode they share, else `mixed` (B54).
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
 * @property {string | null} [base] - the block base (`recheck_scope: file`, and the B56 moved-code diff).
 * @property {ReadonlyArray<string>} [peers] - B56: the block's other changed files (moved-code hints).
 * @property {() => Promise<import('./moved.mjs').PeerDiff[]>} [peerDiffs] - B56: a memoised reader of their diffs (`peerDiffReader`); wins over `peers`.
 * @property {string} [rulesDigest] @property {string} [factsExcerpt]
 * @property {number} [extraRounds] - B47: rounds past `review.max_rounds_per_file` granted by autopilot (`extraRoundsFor`).
 * @property {import('./schema-fallback.mjs').SchemaHistory} [schemaHistory] - B55: the packet's signed schema-miss history; absent ⇒ no second-level try.
 */

/** @param {LoopDeps} deps @param {Record<string, any>} row */
async function note(deps, row) {
  try {
    await deps.writeRow?.(row);
  } catch {
    // the loop's verdict stands; the ledger row is best-effort
  }
}

/**
 * Token counts over a round's sessions: a missing count (null/undefined) adds nothing, and no
 * count at all stays null (one session without usage ⇒ null, as before B54).
 * @param {number | null | undefined} a @param {number | null | undefined} b @returns {number | null}
 */
export const sum = (a, b) => (typeof a !== 'number' ? (typeof b === 'number' ? b : null) : typeof b === 'number' ? a + b : a);

/**
 * The round's `last_packet` over its recheck packets (B54): the context lines add up; the context
 * mode is the packets' one mode when they all agree, else `mixed`.
 * @param {{context_mode: string, context_lines: number} | null} info
 * @param {{contextMode: string, contextLines: number}} packet
 * @returns {{context_mode: string, context_lines: number}}
 */
export function mergePacketInfo(info, packet) {
  if (info === null) return { context_mode: packet.contextMode, context_lines: packet.contextLines };
  return { context_mode: info.context_mode === packet.contextMode ? info.context_mode : 'mixed', context_lines: info.context_lines + packet.contextLines };
}

/**
 * Pack a Markdown recheck into section packets (B54), each sized with {@link recheckTokens} over
 * its own diff and its own open findings. Each open finding belongs to ONE heading group — the one
 * `sectionOf` picks over the groups (spanning, else nearest) — and is sized and listed in the
 * packet that holds that group, so sizing and listing never disagree. No group at all (no hunk,
 * or hunks that do not parse one to one) ⇒ `split_required` at the whole size, never sectioned;
 * a finding no packet holds is listed in the LAST packet — every open finding is listed once.
 * @param {import('./packet.mjs').FileDiff} diff @param {Finding[]} open @param {number} budget
 * @param {import('./moved.mjs').Moves | null} [moves] - the file's moves (B56): each candidate is sized
 *   with the `## moved code` text of ITS sections (`movedText`), the whole size with all of them.
 * @returns {{status: 'ok', sections: Array<import('./sections.mjs').Section & {open: Finding[]}>} | {status: 'split_required', tokensIn: number, budget: number, section: string | null}}
 */
export function packRecheck(diff, open, budget, moves = null) {
  const moved = movedText(moves, diff.content, null);
  const groups = sectionGroups(diff);
  if (groups === null || groups.length === 0) return { status: 'split_required', tokensIn: recheckTokens(diff, open, moved), budget, section: null };
  /** @type {Map<Finding, number>} */
  const keyOf = new Map();
  for (const f of open) {
    const at = sectionOf(groups, f);
    if (at >= 0) keyOf.set(f, groups[at].key);
  }
  /** @param {ReadonlyArray<number>} keys @returns {Finding[]} the open findings of those groups, in order. */
  const openIn = (keys) => open.filter((f) => keyOf.has(f) && keys.includes(/** @type {number} */ (keyOf.get(f))));
  const packed = packSections({ diff, budget, measure: (part, set) => (set === null ? recheckTokens(part, open, moved) : recheckTokens(part, openIn(set.map((g) => g.key)), movedText(moves, diff.content, set.map((g) => g.key)))) });
  if (packed.status !== 'ok') return packed;
  const sections = packed.sections.map((sec) => ({ ...sec, open: openIn(sec.keys) }));
  if (sections.length === 0) return { status: 'split_required', tokensIn: recheckTokens(diff, open, moved), budget, section: null };
  // defensive: a finding no packet holds goes to the last packet (it is sized there by the check
  // every packet passes before any session) — never dropped, never closed unseen
  const listed = new Set(sections.flatMap((sec) => sec.open));
  const last = /** @type {(typeof sections)[number]} */ (sections.at(-1));
  for (const f of open) if (!listed.has(f)) last.open.push(f);
  return { status: 'ok', sections };
}

/** @param {unknown} v @param {number} d @returns {number} `v` when a non-negative integer, else `d`. */
const int = (v, d) => (Number.isInteger(v) && /** @type {number} */ (v) >= 0 ? /** @type {number} */ (v) : d);

/**
 * The file's round cap: `review.max_rounds_per_file` (default 4) plus the autopilot extra rounds.
 * @param {Record<string, any> | undefined} cfg @param {number} [extraRounds]
 * @returns {number}
 */
export function roundCap(cfg, extraRounds = 0) {
  return int(cfg?.review?.max_rounds_per_file, 4) + int(extraRounds, 0);
}

/**
 * B47: the extra rounds autopilot granted the file — the number of distinct grants with an
 * `autopilot.extra_round` row of this run for this block and file that holds up as the gate's
 * waiver check does: its MAC verifies, its grant covered `round:extra` at its `ts`
 * (`grantCoverProblem`) and its `decision_id` names the delegate's acted decision for that block
 * and file (`decisionProblem`). A row that fails any check, or whose check throws, counts for nothing.
 * @param {Array<Record<string, any>>} rows @param {{runId: string, block: string, file: string, key: Buffer}} where
 * @returns {number}
 */
export function extraRoundsFor(rows, { runId, block, file, key }) {
  const runRows = rows.filter((r) => r?.run === runId);
  const grants = new Set();
  for (const r of runRows) {
    if (r?.event !== 'autopilot.extra_round' || r.block !== block || r.file !== file || typeof r.grant_id !== 'string') continue;
    try {
      if (!verifyRow(r, key).ok) continue;
      const at = typeof r.ts === 'string' ? Date.parse(r.ts) : Number.NaN;
      if (!Number.isFinite(at)) continue;
      if (grantCoverProblem(runRows, { grantId: r.grant_id, scope: 'round:extra', at, key, endRows: rows }) !== null) continue;
      if (decisionProblem(runRows, { decisionId: r.decision_id, grantId: r.grant_id, scope: 'round:extra', subject: { block, file }, decision: 'allow', at, key }) !== null) continue;
      grants.add(r.grant_id);
    } catch {
      // a row the checks cannot read gives no round (fail closed)
    }
  }
  return grants.size;
}

/**
 * B47: reopen a file stopped at `review_cap` when its raised cap leaves a round: the state goes
 * back to `open` with `next: fix` at its level (`trigger: autopilot_extra_round`); the next round
 * is a recheck. Any other state is left as it is.
 * @param {FileState} state @param {number} extraRounds @param {Record<string, any> | undefined} cfg
 * @returns {boolean} whether the state was reopened.
 */
export function reopenForExtraRound(state, extraRounds, cfg) {
  if (state.status !== 'stopped' || state.next?.reason !== 'review_cap') return false;
  if (state.round >= roundCap(cfg, extraRounds)) return false;
  state.status = 'open';
  state.next = { action: 'fix', level: state.level, trigger: 'autopilot_extra_round' };
  state.pending_kind = null;
  return true;
}

/** @param {Record<string, any> | undefined} cfg @param {number} [extraRounds] */
function settings(cfg, extraRounds = 0) {
  const r = cfg?.review ?? {};
  const e = cfg?.escalation ?? {};
  return {
    maxRounds: roundCap(cfg, extraRounds),
    scope: r.recheck_scope === 'file' ? 'file' : 'fix_hunks',
    late: r.late_findings === 'block' ? 'block' : 'sweep',
    perLevel: int(e.review_rounds_per_level, 2),
    warnAfter: int(e.after_rounds_with_warnings, 1),
    warnThreshold: e.warning_threshold, // heavyRound falls back to 2 for anything but an integer ≥ 1
    stopAt: typeof e.stop_at === 'string' ? e.stop_at : 'L3',
    l3Mode: e.l3_mode === 'code' ? 'code' : 'patch',
  };
}

/**
 * @param {{file: string, level: string, l3RungUsed?: boolean}} opts
 * @returns {FileState}
 */
export function newFileState({ file, level, l3RungUsed = false }) {
  return { file, level, round: 0, rounds_at_level: 0, warn_rounds_at_level: 0, open: [], late: [], reviewed_content: null, l3_rung_used: l3RungUsed, status: 'open', next: null, last_packet: null, pending_kind: null };
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

/** The heading of the open-findings list a recheck packet ends with. */
const OPEN_FINDINGS_HEAD = '## open findings\n';

/** @param {Finding} f @returns {string} the finding's line in a recheck packet's open-findings list. */
export const openFindingLine = (f) => `- ${f.id} (${f.severity}, lines ${f.line_start}-${f.line_end}): ${f.claim ?? ''}`;

/** @param {ReadonlyArray<Finding>} open @returns {string} the open-findings list a recheck packet ends with. */
const openFindingsText = (open) => `${OPEN_FINDINGS_HEAD}${open.length > 0 ? open.map(openFindingLine).join('\n') : '(none)'}\n`;

/**
 * The size a recheck packet is held to (B54): its diff-only `recheck` packet (the measure behind
 * `split_required`) plus the open-findings list `buildRecheckPacket` appends. Section packing and
 * the check before any session use this ONE function.
 * @param {import('./packet.mjs').FileDiff} diff @param {ReadonlyArray<Finding>} open
 * @param {string} [moved] - the `## moved code` section the packet carries (B56).
 * @returns {number}
 */
export function recheckTokens(diff, open, moved = '') {
  return diffOnlyTokens({ diff, lens: 'recheck', moved }) + Math.ceil(Buffer.byteLength(openFindingsText(open)) / 4);
}

/**
 * The recheck packet: the `recheck` lens on the fix hunks (hunk form, never the whole file), then
 * the open findings by id. Over budget ⇒ `minimal` (see the module doc).
 * @param {{diff: import('./packet.mjs').FileDiff, open: Finding[], cfg?: Record<string, any>, contextMode?: 'auto' | 'recheck', rulesDigest?: string, factsExcerpt?: string, moved?: string}} opts -
 *   `moved`: the file's `## moved code` section (B56), '' for none.
 */
export function buildRecheckPacket({ diff, open, cfg, contextMode = 'recheck', rulesDigest, factsExcerpt, moved = '' }) {
  const packet = assemblePacket({ diff, lens: 'recheck', cfg, contextMode, rulesDigest, factsExcerpt, moved });
  if (packet.status !== 'ok') return packet;
  const text = `${packet.text}${openFindingsText(open)}`;
  return { ...packet, text, tokensIn: packet.tokensIn + Math.ceil(Buffer.byteLength(text.slice(packet.text.length)) / 4) };
}

/**
 * B56: the peer readers built from `deps.peers` (no `deps.peerDiffs`): per deps object, one reader
 * per (repoRoot, base, file, peers) key, so A → B → A on one deps reads A's peers once.
 * @type {WeakMap<object, Map<string, () => Promise<import('./moved.mjs').PeerDiff[]>>>}
 */
const peerReaders = new WeakMap();

/**
 * The peers' diffs reader of a recheck: `deps.peerDiffs` when the caller gave one, else ONE
 * memoised `peerDiffReader` per deps object and (repoRoot, base, file, peers) key, so the peers are
 * read once however many rechecks run with the same deps (one ticket), whatever the order.
 * @param {LoopDeps} deps @param {string} base @param {string} rel
 * @returns {() => Promise<import('./moved.mjs').PeerDiff[]>}
 */
function recheckPeerDiffs(deps, base, rel) {
  if (deps.peerDiffs) return deps.peerDiffs;
  const key = JSON.stringify([deps.repoRoot, base, rel, deps.peers ?? null]);
  let readers = peerReaders.get(deps);
  if (!readers) {
    readers = new Map();
    peerReaders.set(deps, readers);
  }
  const known = readers.get(key);
  if (known) return known;
  const reader = peerDiffReader({ repoRoot: deps.repoRoot, base, file: rel, peers: deps.peers });
  readers.set(key, reader);
  return reader;
}

/**
 * The recheck's moves (B56): from the file's diff against the block base (the one already read
 * for `recheck_scope: file`, else read here), never from the fix hunks; the peers' diffs from the
 * memoised reader (`recheckPeerDiffs`). No base, no peer, or ANY failure (building the reader
 * included) ⇒ null: no hint, the recheck goes on.
 * @param {LoopDeps} deps @param {string} rel @param {import('./packet.mjs').FileDiff | null} baseDiff
 * @returns {Promise<import('./moved.mjs').Moves | null>}
 */
async function recheckMoves(deps, rel, baseDiff) {
  try {
    if (typeof deps.base !== 'string') return null;
    const peerDiffs = recheckPeerDiffs(deps, deps.base, rel);
    // nothing to read when there is no peer: the base diff is read only for a real peer set
    if ((await peerDiffs()).length === 0) return null;
    const diff = baseDiff ?? (await readFileDiff({ repoRoot: deps.repoRoot, file: rel, base: deps.base }));
    return await movesFor({ diff, peerDiffs });
  } catch {
    return null;
  }
}

/** @param {Finding} f @param {ReadonlyArray<Hunk>} hunks @returns {boolean} */
export function insideHunks(f, hunks) {
  return hunks.some((h) => {
    const last = h.newStart + Math.max(h.newLines, 1) - 1;
    return f.line_start >= h.newStart && f.line_end <= last && f.line_start <= f.line_end;
  });
}

/**
 * One recheck session through the stub guard — and, on a `schema` miss, the B55 second-level try
 * on the same packet (`schema-fallback.mjs`; rechecks and the patch_check alike).
 * @param {{text: string, hunkHeaders: string[], tokensIn: number, contextMode: string}} packet
 * @param {LoopDeps} deps
 */
async function recheckSession(packet, deps) {
  mkdirSync(deps.workDir, { recursive: true, mode: 0o700 });
  const promptPath = path.join(deps.workDir, `recheck-${randomBytes(6).toString('hex')}.md`);
  writeFileSync(promptPath, packet.text, { mode: 0o600 });
  const min = minTokensOut(deps.cfg);
  const session = { lens: 'recheck', role: 'reviewer', level: 'L2' };
  /** @param {Record<string, any> | undefined} cfg */
  const attempt = async (cfg) =>
    deps.spawn
      ? // a `timeout` is spawned once more on the same packet and level (B30)
        spawnWithTimeoutRetry(
          deps.spawn,
          { level: 'L2', role: 'reviewer', promptPath, schema: FINDING_SCHEMA, rowExtra: { lens: 'recheck', context_mode: packet.contextMode, ctx_tokens_in: packet.tokensIn }, ...(cfg ? { cfg } : {}) },
          (row) => note(deps, row),
        )
      : { res: null, attempts: 0 };
  /** @type {Record<string, any> | null} */
  let res = null;
  /** @type {import('./validate-review.mjs').Verdict} */
  let verdict = { ok: false, reason: 'exit', detail: 'no session result' };
  /** @type {number | null} the round's tokens: the first session's, plus a second-level try's */
  let tokensIn = null;
  /** @type {number | null} */
  let tokensOut = null;
  // the packet file is removed on every path, and only after the second-level try (which reads it)
  try {
    try {
      res = (await attempt(undefined)).res;
    } catch {
      res = null; // the helper never throws; if it ever did, it is still an `exit` failure
    }
    tokensIn = res?.usage?.tokens_in ?? null;
    tokensOut = res?.usage?.tokens_out ?? null;
    verdict = validateReview(res, { hunkHeaders: packet.hunkHeaders, minTokensOut: min });
    if (!verdict.ok && verdict.reason === 'schema') {
      try {
        // no ledger writer ⇒ nothing logged, nothing tried; every row goes through the same writer
        // (each helper handles its own failed write)
        const write = deps.writeRow ? (/** @type {Record<string, any>} */ row) => /** @type {NonNullable<LoopDeps['writeRow']>} */ (deps.writeRow)(row) : undefined;
        const second = await afterSchemaMiss({ res, session, packetText: packet.text, cfg: deps.cfg ?? {}, allowed: true /* rechecks are never consensus; the budget gate is inside the spawn */, history: deps.schemaHistory, writeRow: write, spawnAt: attempt });
        if (second) {
          // compute everything first, then assign together, so a throw never leaves a mixed state
          const secondVerdict = validateReview(second.res, { hunkHeaders: packet.hunkHeaders, minTokensOut: min });
          const sumIn = sum(tokensIn, second.res?.usage?.tokens_in);
          const sumOut = sum(tokensOut, second.res?.usage?.tokens_out);
          res = second.res;
          verdict = secondVerdict;
          tokensIn = sumIn;
          tokensOut = sumOut;
          if (!verdict.ok && verdict.reason === 'schema') await logSecondLevelMiss({ res, session, packetText: packet.text, writeRow: write });
        }
      } catch {
        // defensive only: the helpers never throw. If they ever did, the first session's result
        // and its `schema` verdict stand (never turned into `exit`, never approval)
      }
    }
  } finally {
    rmSync(promptPath, { force: true });
  }
  return { verdict, tokens_in: tokensIn, tokens_out: tokensOut };
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
    warningRoundsAtLevel: state.warn_rounds_at_level ?? 0,
    afterRoundsWithWarnings: s.warnAfter,
    stopAt: s.stopAt,
    l3Mode: /** @type {'patch' | 'code'} */ (s.l3Mode),
    l3RungAlreadyUsed: state.l3_rung_used,
  });
  if (esc.action === 'escalate' && esc.level) {
    if (esc.level !== state.level) {
      state.rounds_at_level = 0;
      state.warn_rounds_at_level = 0;
    }
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
 * @param {'full' | 'recheck' | 'patch_check'} kind @param {string} file
 * @param {{status: string, tokensIn?: number, budget?: number, section?: string | null}} packet - the non-ok packet (a Markdown section names its heading).
 * @returns {Promise<FileState>}
 */
async function stopBeforeRound(state, deps, kind, file, packet) {
  const reason = packet.status;
  const size =
    typeof packet.tokensIn === 'number' && typeof packet.budget === 'number'
      ? { tokens_in: packet.tokensIn, budget: packet.budget, ...(typeof packet.section === 'string' ? { section: packet.section } : {}) }
      : {};
  const row = { event: 'review.cap', file, round: state.round, reason, open: state.open.map((f) => f.id), ...size };
  if (!(await mustNote(deps, row))) return retry(state, kind, 'ledger_write_failed');
  state.status = 'stopped';
  state.next = { action: 'stop', reason, ...size };
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
  const s = settings(deps.cfg, deps.extraRounds);
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
        const contextMode = s.scope === 'file' ? 'auto' : 'recheck';
        const moves = await recheckMoves(deps, current.rel, s.scope === 'file' ? diff : null);
        const whole = buildRecheckPacket({ diff, open: pending, cfg: deps.cfg, contextMode, rulesDigest: deps.rulesDigest, factsExcerpt: deps.factsExcerpt, moved: movedText(moves, diff.content, null) });
        /** @type {Array<{index: number | null, heading: string | null, diff: import('./packet.mjs').FileDiff, open: Finding[], keys: number[] | null}>} */
        let parts = [{ index: null, heading: null, diff, open: pending, keys: null }];
        if (whole.status !== 'ok') {
          // deterministic (the same hunks build the same packet): terminal, never a retry — except
          // a Markdown file whose packet is `split_required`, rechecked section by section (B54);
          // any other status stops as it always did
          const packed = whole.status === 'split_required' && isMarkdownFile(current.rel) ? packRecheck(diff, pending, whole.budget, moves) : null;
          if (!packed || packed.status !== 'ok') return stopBeforeRound(state, deps, kind, current.rel, packed ?? whole);
          parts = packed.sections.map((sec) => ({ index: sec.index, heading: sec.headings[0], diff: sec.diff, open: sec.open, keys: sec.keys }));
        }
        // EVERY packet is built and held to the budget BEFORE any session runs: a section over it
        // stops the round as split_required (with its heading) before anything is spent
        /** @type {Array<Record<string, any>>} */
        const built = [];
        for (const part of parts) {
          if (part.index === null) {
            built.push(whole);
            continue;
          }
          const moved = movedText(moves, diff.content, part.keys);
          const packet = buildRecheckPacket({ diff: part.diff, open: part.open, cfg: deps.cfg, contextMode, rulesDigest: deps.rulesDigest, factsExcerpt: deps.factsExcerpt, moved });
          // a packet that fails to build stops with ITS status (and size, when it has one)
          if (packet.status !== 'ok') return stopBeforeRound(state, deps, kind, current.rel, { ...packet, section: part.heading });
          const size = recheckTokens(part.diff, part.open, moved);
          if (size > whole.budget) return stopBeforeRound(state, deps, kind, current.rel, { status: 'split_required', tokensIn: size, budget: whole.budget, section: part.heading });
          built.push(packet);
        }
        /** @type {Set<Finding>} the pending findings a session that LISTED them resolved */
        const resolved = new Set();
        /** @type {Finding[]} */
        const inside = [];
        /** @type {Finding[]} */
        const blocking = [];
        for (const [i, part] of parts.entries()) {
          const packet = /** @type {any} */ (built[i]);
          packetInfo = mergePacketInfo(packetInfo, packet);
          const session = await recheckSession(packet, deps);
          tokensIn = sum(tokensIn, session.tokens_in);
          tokensOut = sum(tokensOut, session.tokens_out);
          if (!session.verdict.ok) {
            await note(deps, { event: 'review.unavailable', file: current.rel, lens: 'recheck', reason: session.verdict.reason, ...(part.index === null ? {} : { section: part.index }) });
            return retry(state, kind, session.verdict.reason ?? 'unavailable');
          }
          const review = /** @type {Record<string, any>} */ (session.verdict.review);
          const resolvedIds = new Set((review.resolved ?? []).filter((/** @type {any} */ r) => r?.resolved === true).map((/** @type {any} */ r) => r.id));
          // only the findings listed in THIS packet can be resolved by its session
          for (const f of part.open) if (resolvedIds.has(f.id)) resolved.add(f);
          const found = /** @type {Finding[]} */ (review.findings ?? []);
          for (const f of part.index === null ? found : sectionFindings(found, part.index)) {
            if (insideHunks(f, diff.hunks)) {
              inside.push(f);
              continue;
            }
            draft.late.push(f);
            lateRows.push({ event: 'review.late_finding', file: current.rel, round: state.round + 1, finding: f.id, severity: f.severity, line_start: f.line_start, line_end: f.line_end, mode: s.late });
            if (s.late === 'block' && f.severity !== 'nit') blocking.push(f);
          }
        }
        // every pending finding stays open unless the session that listed it resolved it
        const stillOpen = pending.filter((f) => !resolved.has(f));
        closed += pending.length - stillOpen.length;
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
    // THIS round's open set, after triage: only the warnings still open count
    if (heavyRound(draft.open, s.warnThreshold)) draft.warn_rounds_at_level = (draft.warn_rounds_at_level ?? 0) + 1;
  } else {
    // the block continues at L2 after the rung with a fresh round counter (§3.6) — also here, not
    // only in `converge`, because the worker runs the patch_check from its own ticket
    draft.rounds_at_level = 0;
    draft.warn_rounds_at_level = 0;
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
      state.warn_rounds_at_level = 0;
      await deps.patch?.({ file: state.file, level: 'L3', open: [...state.open] });
      await runRound(state, deps, { kind: 'patch_check' });
      continue;
    }
    await deps.fix?.({ file: state.file, round: state.round + 1, level: state.level, open: [...state.open], l3_patch: state.l3_rung_used });
    await runRound(state, deps);
  }
  return state;
}
