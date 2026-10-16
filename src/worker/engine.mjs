/**
 * The review-engine hook (plan §4.2–§4.4, §4.11; B11 seam, body by B12a, fix loop wired by B12c).
 *
 * `reviewTicket(ticket, ctx)` is the ONE seam between the worker (queue, keys, signing, pid
 * hygiene — B11) and the review (`src/review/`). The worker signs whatever it returns. The hook
 * looks up the block's base SHA and level in the run record and gives the review a packet
 * directory under the run's temp root (removed afterwards). A run record that exists but cannot be
 * read is `unavailable`, never a review against a guessed base.
 *
 * With the run key in `ctx.key` (the worker always passes it), one ticket = ONE round of B12b's
 * fix loop (`fixloop.mjs`, §4.11) on the file's persisted state (`review-state.mjs`):
 *  - no state, or the last cycle `complete` (the file changed after its approval) ⇒ round 1: the
 *    B12a engine (`reviewFile`), then triage (`triage.mjs`);
 *  - open findings ⇒ the recheck round on the fix hunks only (the diff between the content the
 *    last round reviewed and the current content), with the open findings by id;
 *  - `next: patch` (the L3 rung at the cap) ⇒ the `patch_check` round; a `retry` re-runs its round;
 *  - `stopped` (`review_cap`, `l3_patch_exhausted`, `split_required`) ⇒ no session: the result
 *    repeats the stop — only the human moves on (§4.11 rule 5). A `split_required` result (round 1
 *    or a recheck stop) carries `tokens_in` and `budget` (B54).
 * A state that does not load is refused with no session: `review-state-tampered` (a wrong file or
 * anchor) or `review-state-lost` (the file is gone and its anchor has no `state_body`). A file
 * that is gone but whose anchor carries the signed body is `restored` (B68): saved as a new seq,
 * one signed `review.state_restored {block, file, seq, from_seq}` row, then the round runs as if
 * it had loaded. B69: the state file lives under the code-forge home (`runs/<run>/review-state/`),
 * not the temp root; a valid file still at the old temp path is moved there on its first load
 * (status `ok`, no restored row).
 * The round's outcome: `approved: true` ONLY when the loop is `complete` AND its signed
 * `review.approved` row (keyed `run, block, file, content_hash` = the ticket's hash) was written;
 * else `findings` = the open fix list, `next` = what the orchestrator does (`fix` at `level`,
 * `trigger: review_stall | review_rounds`, `patch`), `stopped` = the stop reason.
 * Without `ctx.key` (a direct call) the hook is the engine alone and writes no approval row.
 * Every packet (round 1, recheck, patch check) carries the block's acceptance clauses from the run
 * record in its facts slot, labelled `Acceptance clauses` (`acceptanceExcerpt`): redacted, capped
 * at an eighth of `review.budgets.full_in` with a truncation marker, and `(acceptance unavailable)`
 * when the record has none — a missing acceptance never fails the review.
 * The first keyed ticket of a block (no `review.budget` row for it in the run yet) records the
 * block's review budget (§4.10, B19): the forecast over the block's changed owned files (tier from
 * the path floors, before S1), one `review.budget` row per block; a failure there never fails the
 * review.
 * B55: every round gets the packet's signed schema-miss history (`ledgerSchemaHistory`: rows of
 * this run for this file and content hash whose MAC verifies with the run key), so a packet's
 * second `schema` miss — this round's first try after the earlier ticket's — is tried once on
 * `review.second_levels.<level>` when one is configured.
 * B56: every round gets ONE memoised reader of the block's OTHER changed files' diffs
 * (the ticket's `fileContext`: listed once, diffs read lazily at most once per ticket) so the review can name code moved
 * between them in a `## moved code` packet section (round 1, rechecks and patch check alike).
 * The review NEVER approves an empty or failed session: that is `unavailable` (§4.2 stub guard).
 * A throw inside the round is `unavailable` too, with the state saved as `next: retry` (see
 * `fixLoopRound`): the next ticket re-runs that round, never a fresh round 1.
 * B62b: for a block the run record gives a worktree (`proof.isolation: worktree`) every content
 * read — the file, its diff against the block base, the peers' diffs, the fix hunks and the
 * budget's file set — happens in that tree, against the tree's `base_sha` (`contentRootFor`; the
 * tree comes from the run record, never from the coder-writable ticket), and the packet's facts
 * slot names the tree and its branch. A file changed only in the main tree is never seen. The
 * base, the tree and the facts come from ONE read of the run record. A ticket is `unavailable`
 * with one of these reasons:
 *  - `worktree-missing`: the recorded tree is gone;
 *  - `worktree-not-dir`: its path is not a directory;
 *  - `worktree-malformed`: no absolute path, a branch that is empty or has control characters, or
 *    a base_sha that is not a full hex id;
 *  - `worktree-branch-mismatch`: the tree is not on its recorded branch;
 *  - `worktree-foreign`: the tree belongs to another repository;
 *  - `worktree-workspace-missing`: the workspace's subdirectory is not in the tree;
 *  - `worktree-mismatch`: the tree the worker hashed the ticket in (`ctx.contentRoot`, from the
 *    ticket's `tree` or its absence) is not the record's tree;
 *  - `git-failed`: git could not be run on the tree;
 *  - `run-record-unreadable`: the record cannot be parsed, or names no workspace.
 * A block without a worktree, or one the run record does not know, reads the main tree as before.
 * B63a: a ticket is also `unavailable` with `context-unreadable` when the config's `context` key
 * is bad or one of its rules or lessons files is missing, outside the repository or unreadable at
 * review time; no session runs and the packet dir is removed. The worker does not retry it (the
 * ticket is done); `review-file` on the same content is a fresh attempt every time (`retry: true`).
 * The tree is passed as `contentRoot`: `repoRoot` stays the main tree, and the fix-loop state
 * stays under the run root.
 * B72: a verified `review.reopened {by: human}` row of this run for the file whose `at_seq` is the
 * file's latest anchor seq (`reopenRowFor`, `review/reopen.mjs`) restarts a `stopped` file whose
 * content changed since the stop, or a `lost` one: a new state (round 1, a FULL review with no
 * `reviewed_content`, the level from the run record), saved at `at_seq + 1`, so the row works
 * once; no `review.state_restored` row then. The block's L3 rung stays spent (it is read from the
 * anchors). A row that does not verify, of another run, not `by: human`, at another seq, or a
 * stopped file on the stop's own content changes nothing: the stop (or the lost refusal) repeats.
 * Every stop records the ticket's content hash as the state's `stop_content_hash` (signed with
 * the state), the content `block reopen` and this check compare against.
 * B71: a ticket with `kind: 'answers'` (`review-file --answers`) runs no fix-loop round: after the
 * same run-record, tree and mismatch checks it goes to `reviewAnswers` (`review/answers.mjs`) —
 * one fresh L2 session that judges whether the answering file resolves the named open findings of
 * the source, with signed `review.answered` rows for the resolved ones; the source's fix-loop
 * state is neither read for a round nor saved.
 */

import { realpathSync, rmSync } from 'node:fs';
import path from 'node:path';
import { blockKind } from '../decide/escalation.mjs';
import { askJev } from '../decide/jev-client.mjs';
import { planAndRecord, tierOf } from '../review/budget.mjs';
import { budgetFor } from '../review/packet.mjs';
import { ContextError, reviewDigest } from '../context/pack.mjs';
import { reviewFile, reviewLightBatch, rulesRisk } from '../review/engine.mjs';
import { reviewAnswers } from '../review/answers.mjs';
import { peerDiffReader } from '../review/moved.mjs';
import { computeFileSet, ownsFile } from '../gates/scope.mjs';
import { extraRoundsFor, newFileState, reopenForExtraRound, runRound } from '../review/fixloop.mjs';
import { reopenRowFor } from '../review/reopen.mjs';
import { processStartTime, readRun } from '../state/run.mjs';
import { StateError } from '../state/paths.mjs';
import { blockTree, worktreeRecord } from '../proof/worktree.mjs';
import { ledgerSchemaHistory } from '../review/schema-fallback.mjs';
import { verifyRow } from '../state/signer.mjs';
import { redact } from '../util/redact.mjs';
import { withLock } from '../util/file-locks.mjs';
import { blockAnchors, blockRungUsed, loadState, RESTORED_EVENT, saveState } from './review-state.mjs';
import { assertAnswers, enqueueHash, isDone, writeResult } from './queue.mjs';
import { assertTicketId, contentHash, readRegularFileNoFollow, WorkerError } from './ticket.mjs';

/**
 * @typedef {object} ReviewContext
 * @property {string} repoRoot - the main tree (the queue, the worker).
 * @property {string} [contentRoot] - B62b: the tree the ticket's content is read in (the block's
 *   worktree); `repoRoot` when absent.
 * @property {string} runId
 * @property {string} runRootDir - the run's temp root (packets and session dirs live under it; the
 *   review state no longer does — B69 — it is only read there once, to move an old file).
 * @property {Record<string, any>} cfg - the project config.
 * @property {string | null} jevKey - resolved once by the worker through B2's chain; never print.
 * @property {(opts: Record<string, any>) => Promise<Record<string, any>>} spawn - one isolated
 *   session (`spawnSession` opts minus `runRoot`, `run`, `block`, `timeoutMs`; `cfg` defaults to
 *   the worker's and a per-call `cfg` wins).
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeRow] - a signed ledger row
 *   (the worker adds `run`, `block`, `file`, `content_hash`).
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeBlockRow] - light fanout rows;
 *   the worker pins run and block, while the batch supplies each file and content hash.
 * @property {Buffer} [key] - the run's HMAC key: signs the fix-loop state. Absent ⇒ engine only.
 * @property {() => Promise<Array<Record<string, any>>>} [readRows] - the run's ledger rows (the
 *   `review.state` anchors the state is checked against); required with `key`.
 * @property {import('../review/triage.mjs').JevAsk} [jev] - S1 with the key bound; default: `askJev`
 *   with `jevKey` when one was resolved, else none (§3.5 fallback).
 */

/**
 * @typedef {object} ReviewOutcome
 * @property {string} status - `reviewed`, `unavailable`, `no_change`, `split_required`, `refused`.
 * @property {boolean} approved
 * @property {string} engine - `adaptive` or `consensus`.
 * @property {Array<Record<string, any>>} sessions - one summary per session run.
 * @property {string} [reason] - why it is not `reviewed` (stub-guard reason, `needs_file-refused`, …).
 * @property {string} [message] - a refusal's human-readable explanation (`review-state-lost`).
 * @property {Array<Record<string, any>>} [findings] - the open fix list (`reviewed` / `stopped`).
 * @property {number} [round] - the fix-loop round this ticket ran (patch_check rounds not counted).
 * @property {string} [kind] - `full`, `recheck` or `patch_check`.
 * @property {string} [level] - the level the file's fixes are coded at now.
 * @property {Record<string, any> | null} [next] - the loop's next step (`fix`, `patch`, `complete`, `stop`, `retry`).
 * @property {string | null} [trigger] - `review_stall`, `review_rounds`, `review_cap`, or null.
 * @property {string} [stopped] - the stop reason (`review_cap`, `l3_patch_exhausted`, …).
 * @property {number} [late] - late findings recorded so far for the file.
 * @property {number} [tokens_in] - `split_required` (or `stopped: split_required`): the estimated
 *   tokens of the diff-only packet that was over the budget (B54).
 * @property {number} [budget] - with `tokens_in`: the packet budget it exceeded (B54).
 * @property {string} [section] - a Markdown file's section that alone is over the budget (B54).
 */

/**
 * The block's entry in the run record: `null` (⇒ HEAD, level L2) ONLY when no run record exists
 * (`no-run`). Once a record exists, a ticket naming a block it does not list throws
 * `block-unknown`; a record that cannot be read or parsed throws `run-record-unreadable`, and a
 * block entry without a base `block-base-missing` — a guessed base could let a change escape review.
 * @param {string} runId @param {string} block
 * The block's kind (B34) is the recorded `kind` when `block open` stored one, else `blockKind`
 * of its owned files. `ownedKinds` is the recorded `owned_kinds` (B57: which exact entries were
 * regular files at `block open`/`claim`), `{}` when there is none (every exact entry directory-like).
 *   With no run record the kind is null: `reviewTicket` takes the ticket file's kind.
 * `tree` (B62b) is the recorded `worktree` `{path, branch, base_sha}`, null when the block has
 * none; `base` is then the tree's `base_sha`. A recorded worktree that `worktreeRecord` refuses
 * (path, branch, hex base_sha) throws `worktree-malformed`.
 * @returns {Promise<{base: string | null, level: string, owned: string[], ownedKinds: Record<string, string>, testFiles: string[], acceptance?: unknown, kind: string | null, tree: WorktreeEntry | null, workspace: string | null}>}
 */
export async function blockEntryFor(runId, block) {
  let record;
  try {
    record = await readRun(runId);
  } catch (err) {
    if (/** @type {any} */ (err)?.code === 'no-run') return { base: null, level: 'L2', owned: [], ownedKinds: {}, testFiles: [], acceptance: undefined, kind: null, tree: null, workspace: null };
    throw new Error('run-record-unreadable');
  }
  if (!record || typeof record !== 'object' || !record.blocks || typeof record.blocks !== 'object') throw new Error('run-record-unreadable');
  if (typeof block !== 'string' || !Object.hasOwn(record.blocks, block)) throw new Error('block-unknown');
  const entry = record.blocks[block];
  const sha = entry?.base_sha;
  if (typeof sha !== 'string' || sha.length === 0) throw new Error('block-base-missing');
  const owned = Array.isArray(entry.owned_files) ? entry.owned_files.filter((f) => typeof f === 'string') : [];
  const kinds = entry.owned_kinds;
  const ownedKinds = kinds && typeof kinds === 'object' && !Array.isArray(kinds) ? kinds : {};
  const testFiles = Array.isArray(entry.test_files) ? entry.test_files.filter((f) => typeof f === 'string' && !/[*?\[\]{}]/.test(f) && ownsFile(owned, f, ownedKinds)) : [];
  /** @type {WorktreeEntry | null} */
  let tree = null;
  if (entry.worktree !== undefined && entry.worktree !== null) {
    try {
      tree = worktreeRecord(entry.worktree);
    } catch {
      throw new Error('worktree-malformed');
    }
  }
  return { base: tree ? tree.base_sha : sha, level: typeof entry.level === 'string' && /^L[0-3]$/.test(entry.level) ? entry.level : 'L2', owned, ownedKinds, testFiles, acceptance: entry.acceptance, kind: entry.kind === 'light' ? 'light' : blockKind({ owned, declared: entry.kind }), tree, workspace: typeof record.workspace === 'string' ? record.workspace : null };
}

/** @typedef {{path: string, branch: string, base_sha: string}} WorktreeEntry */
/** @typedef {{peers: string[], changedFiles: string[], ownedFiles: string[], testFiles: string[]}} FileContext */

/** The refusals of {@link blockTree} a ticket answers with, as its `reason`. */
const TREE_CODES = new Set(['worktree-malformed', 'worktree-missing', 'worktree-not-dir', 'worktree-branch-mismatch', 'worktree-workspace-missing', 'worktree-foreign', 'git-failed']);

/**
 * B62b: where the content of a ticket of `block` is read — the top of the block's worktree from
 * the run record (`blockTree`: present, a directory, on its recorded branch, of this repository),
 * else `repoRoot`. `repoRoot` exactly as before B62b when there is no run record, the block is not
 * in it, or it has no `worktree`: only a block WITH a worktree record can be refused here.
 * @param {string} runId @param {string} block @param {string} repoRoot
 * @returns {Promise<string>}
 * @throws {WorkerError} a code of {@link TREE_CODES}, or `run-record-unreadable` for a record that
 *   cannot be parsed or is not a record; any other failure (an I/O error) is rethrown.
 */
export async function contentRootFor(runId, block, repoRoot) {
  let record;
  try {
    record = await readRun(runId);
  } catch (err) {
    if (/** @type {any} */ (err)?.code === 'no-run') return repoRoot;
    if (err instanceof SyntaxError) throw new WorkerError('run-record-unreadable', `block ${block}: the run record cannot be read`);
    throw err;
  }
  if (record === null || typeof record !== 'object') throw new WorkerError('run-record-unreadable', `block ${block}: the run record cannot be read`);
  const blocks = record.blocks;
  const entry = blocks && typeof blocks === 'object' && typeof block === 'string' && Object.hasOwn(blocks, block) ? blocks[block] : null;
  if (entry?.worktree === undefined || entry.worktree === null) return repoRoot;
  return treeTopOf(record.workspace, block, entry.worktree);
}

/**
 * The realpath'd top of the recorded worktree `worktree` of `block` (`blockTree`).
 * @param {string} workspace @param {string} block @param {unknown} worktree
 * @returns {Promise<string>}
 * @throws {WorkerError} a code of {@link TREE_CODES}; any other failure is rethrown.
 */
async function treeTopOf(workspace, block, worktree) {
  if (typeof workspace !== 'string' || !path.isAbsolute(workspace)) throw new WorkerError('run-record-unreadable', `block ${block}: the run record names no workspace`);
  try {
    return /** @type {import('../proof/worktree.mjs').BlockTree} */ (await blockTree(workspace, { worktree })).top;
  } catch (err) {
    const code = /** @type {any} */ (err)?.code;
    if (err instanceof StateError && TREE_CODES.has(code)) throw new WorkerError(code, `block ${block}: ${err.message}`);
    throw err;
  }
}

/** @param {string} p @returns {string} `p` realpath'd, or `p` itself when it cannot be resolved. */
export function canonicalPath(p) {
  try {
    return realpathSync(p);
  } catch {
    return p;
  }
}

/**
 * The packet's facts slot: the acceptance clauses, and for a block with a worktree (B62b) a line
 * naming the tree and its branch the file is reviewed in.
 * @param {{acceptance?: unknown, tree: WorktreeEntry | null}} entry @param {Record<string, any> | undefined} cfg
 * @returns {string}
 */
function factsFor(entry, cfg) {
  const excerpt = acceptanceExcerpt(entry.acceptance, cfg);
  if (!entry.tree) return excerpt;
  return `${excerpt}\nWorktree: the file is reviewed as it is in the block's worktree ${redact(entry.tree.path)} (branch ${redact(entry.tree.branch)}), against the block base.`;
}

/** The acceptance line written when the block's clauses cannot be read. */
export const ACCEPTANCE_UNAVAILABLE = '(acceptance unavailable)';
const ACCEPTANCE_HEAD = 'Acceptance clauses (what the change must do):';

/**
 * The block's acceptance clauses as the packet's facts-slot text: one `- <clause>` line each
 * (whitespace folded, so a clause can never open a packet section), redacted, cut at an eighth of
 * the `full_in` budget (bytes = tokens × 4) with a marker naming how many clauses were left out.
 * Not a list of `{clause}` objects with text ⇒ `(acceptance unavailable)`.
 * @param {unknown} acceptance - the run record's `blocks.<id>.acceptance`
 * @param {Record<string, any> | undefined} cfg
 * @returns {string}
 */
export function acceptanceExcerpt(acceptance, cfg) {
  const clauses = Array.isArray(acceptance)
    ? acceptance.map((c) => (typeof c?.clause === 'string' ? /** @type {string} */ (redact(c.clause.replace(/\s+/g, ' ').trim())) : '')).filter((c) => c.length > 0)
    : [];
  if (clauses.length === 0) return `${ACCEPTANCE_HEAD}\n${ACCEPTANCE_UNAVAILABLE}`;
  const maxBytes = Math.max(256, Math.floor((budgetFor(cfg, 'full_in') * 4) / 8));
  const lines = [ACCEPTANCE_HEAD];
  let used = Buffer.byteLength(ACCEPTANCE_HEAD) + 1;
  for (let i = 0; i < clauses.length; i += 1) {
    const line = `- ${clauses[i]}`;
    const size = Buffer.byteLength(line) + 1;
    if (used + size > maxBytes) {
      if (lines.length === 1) lines.push(`${Buffer.from(line).subarray(0, Math.max(0, maxBytes - used - 4)).toString('utf8')} …`);
      const left = clauses.length - (lines.length - 1);
      if (left > 0) lines.push(`(acceptance truncated: ${left} more clause(s) over the packet budget)`);
      break;
    }
    lines.push(line);
    used += size;
  }
  return lines.join('\n');
}

/**
 * The block's base SHA (`blockEntryFor(...).base`).
 * @param {string} runId @param {string} block
 * @returns {Promise<string | null>}
 */
export async function baseFor(runId, block) {
  return (await blockEntryFor(runId, block)).base;
}

/**
 * The packet directory for a ticket, or null when the id cannot name one. The directory is
 * removed with `rmSync({recursive: true})` afterwards, and tickets are coder-writable, so the id
 * must be the 24-hex form B11 generates AND the resolved path must sit directly under
 * `<runRootDir>/packets` — never the run root itself (`''`), a parent (`../..`) or a nested path.
 * @param {string} runRootDir @param {unknown} id
 * @returns {string | null}
 */
export function packetDirFor(runRootDir, id) {
  const packets = path.resolve(runRootDir, 'packets');
  let workDir;
  try {
    workDir = path.resolve(packets, assertTicketId(id));
  } catch {
    return null;
  }
  const rel = path.relative(packets, workDir);
  if (rel.length === 0 || rel.includes(path.sep) || rel === '..' || path.isAbsolute(rel) || path.dirname(workDir) !== packets) return null;
  return workDir;
}


/**
 * @param {import('./queue.mjs').Ticket} ticket
 * @param {ReviewContext} ctx
 * @returns {Promise<ReviewOutcome>}
 */
export async function reviewTicket(ticket, ctx) {
  const workDir = packetDirFor(ctx.runRootDir, ticket.ticket);
  if (workDir === null) return { status: 'unavailable', reason: 'ticket-id-invalid', approved: false, engine: 'adaptive', sessions: [] };
  let entry;
  try {
    entry = await blockEntryFor(ctx.runId, ticket.block);
  } catch (err) {
    return { status: 'unavailable', reason: err instanceof Error ? err.message : 'run-record-unreadable', approved: false, engine: 'adaptive', sessions: [] };
  }
  if (entry.kind === null) entry = { ...entry, kind: blockKind({ owned: [ticket.file] }) };
  // B62b: every content read of this ticket happens in the block's tree (the main tree without
  // one), taken from the SAME entry as the base and the facts. The worker passes the tree it
  // hashed the ticket in as `ctx.contentRoot`; a record that changed since names another tree.
  let contentRoot;
  try {
    contentRoot = entry.tree ? await treeTopOf(entry.workspace, ticket.block, entry.tree) : ctx.repoRoot;
  } catch (err) {
    return { status: 'unavailable', reason: err instanceof WorkerError ? err.code : 'run-record-unreadable', approved: false, engine: 'adaptive', sessions: [] };
  }
  if (ctx.contentRoot !== undefined && canonicalPath(ctx.contentRoot) !== canonicalPath(contentRoot)) {
    return { status: 'unavailable', reason: 'worktree-mismatch', approved: false, engine: 'adaptive', sessions: [] };
  }
  /** @type {ReviewContext} */
  const tctx = { ...ctx, contentRoot };
  if (ticket.kind === 'answers') {
    // B71: an answers ticket is no review round: one answers session, the fix-loop state untouched
    try {
      return await reviewAnswers(ticket, tctx, { entry, workDir, io: { readFile: readRegularFileNoFollow, loadState, assertAnswers } });
    } finally {
      rmSync(workDir, { recursive: true, force: true });
    }
  }
  const factsExcerpt = factsFor(entry, ctx.cfg);
  // Resolve concrete paths in this block's tree; neither globs nor another block's tests
  // authorize an untracked attachment. Read lazily so a stopped ticket needs no file listing.
  /** @type {Promise<FileContext> | undefined} */
  let contextOnce;
  /** @type {() => Promise<import('../review/moved.mjs').PeerDiff[]>} */
  let readDiffs = () => Promise.resolve([]);
  const fileContext = () => contextOnce ??= (async () => {
    try {
      // Read the listing directly: blockPeers' [] fallback would hide a git failure and
      // still authorize explicit tests. Context and peer diffs must fail soft together.
      const peers = entry.base === null || entry.owned.length === 0 ? [] : (await computeFileSet({ cwd: contentRoot, base: entry.base })).all.filter((f) => f !== ticket.file && ownsFile(entry.owned, f, entry.ownedKinds));
      readDiffs = peerDiffReader({ repoRoot: contentRoot, base: entry.base, file: ticket.file, peers });
      const changedFiles = [...peers, ticket.file].filter((f) => ownsFile(entry.owned, f, entry.ownedKinds));
      return { peers, changedFiles, ownedFiles: [...new Set([...changedFiles, ...entry.testFiles])], testFiles: entry.testFiles };
    } catch {
      return { peers: [], changedFiles: [], ownedFiles: [], testFiles: [] };
    }
  })();
  const peerDiffs = async () => {
    await fileContext();
    return readDiffs();
  };
  try {
    // B63a: the `context` rules and lessons, read from the MAIN tree (`ctx.repoRoot`, never the
    // block's worktree) with the config in force for this ticket; '' without a `context` key, so
    // the packet stays as it was. A bad `context` value or a rules/lessons file that is missing,
    // outside the repository or unreadable makes the ticket `unavailable` (`context-unreadable`,
    // re-queued by `review-file` on the same content, as often as asked): no review runs without the rules it was promised.
    let rulesDigest;
    try {
      rulesDigest = reviewDigest({ repoRoot: ctx.repoRoot, cfg: ctx.cfg });
    } catch (err) {
      if (!(err instanceof ContextError)) throw err;
      return { status: 'unavailable', reason: 'context-unreadable', approved: false, engine: 'adaptive', sessions: [] };
    }
    const digest = rulesDigest === '' ? {} : { rulesDigest };
    if (entry.kind === 'light' && ctx.key) {
      const batch = await lightRound(ticket, tctx, { entry, workDir, factsExcerpt, rulesDigest });
      if (batch) return batch;
    }
    if (!ctx.key) {
      return await reviewFile(
        { repoRoot: contentRoot, file: ticket.file, base: entry.base, cfg: ctx.cfg, kind: entry.kind, workDir, factsExcerpt, ...digest, peerDiffs, ...(await fileContext()) },
        { spawn: ctx.spawn, ...(ctx.writeRow ? { writeRow: ctx.writeRow } : {}) },
      );
    }
    return await fixLoopRound(ticket, tctx, { base: entry.base, level: entry.level, owned: entry.owned, ownedKinds: entry.ownedKinds, blockKindOf: entry.kind, key: ctx.key, workDir, factsExcerpt, rulesDigest, peerDiffs, fileContext });
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

/** Serialize a light block across tickets/workers; durable signed rows enforce the two-batch cap.
 * A batch completes every file's queue ticket, including files queued after this ticket started.
 * @param {import('./queue.mjs').Ticket} ticket @param {ReviewContext} ctx
 * @param {{entry: Awaited<ReturnType<typeof blockEntryFor>>, workDir: string, factsExcerpt: string, rulesDigest: string}} input
 * @returns {Promise<ReviewOutcome | null>} null enters the ordinary per-file loop.
 */
async function lightRound(ticket, ctx, { entry, workDir, factsExcerpt, rulesDigest }) {
  if (!ctx.key || !ctx.readRows || !ctx.writeBlockRow) return { status: 'unavailable', reason: 'no-ledger', approved: false, engine: 'adaptive', sessions: [] };
  const key = ctx.key, readRows = ctx.readRows, writeRow = ctx.writeBlockRow;
  const worker = { pid: process.pid, started_at: await processStartTime(process.pid) };
  return withLock(`light:${ctx.runId}\0${ticket.block}`, async () => {
    const root = ctx.contentRoot ?? ctx.repoRoot;
    const files = (await computeFileSet({ cwd: root, base: entry.base ?? 'HEAD' })).all.filter((file) => ownsFile(entry.owned, file, entry.ownedKinds)).sort();
    const hashes = files.map((file) => ({ file, content_hash: contentHash(root, file) }));
    if (!hashes.some((f) => f.file === ticket.file && f.content_hash === ticket.content_hash)) return { status: 'stale', approved: false, engine: 'adaptive', sessions: [] };
    const signature = JSON.stringify([entry.base, hashes]);
    const rows = (await readRows()).filter((r) => r.run === ctx.runId && r.block === ticket.block && verifyRow(r, key).ok);
    const cached = rows.filter((r) => r.event === 'review.light_result' && r.signature === signature).at(-1)?.outcomes?.[ticket.file];
    if (cached && (cached.status === 'reviewed' || isDone(ctx.repoRoot, ticket.ticket))) return cached;
    const count = rows.filter((r) => r.event === 'review.light_batch').length;
    if (count >= 2) return null;
    const round = count + 1;
    // Reserve before the session: a crash cannot reset the cap on a worker restart.
    await writeRow({ event: 'review.light_batch', round, signature });
    const batch = await reviewLightBatch({ repoRoot: root, files, base: entry.base, cfg: ctx.cfg, workDir, factsExcerpt, rulesDigest }, { spawn: ctx.spawn, writeRow });
    const moved = hashes.some((f) => contentHash(root, f.file) !== f.content_hash);
    const findings = batch.findings ?? [];
    const blocking = findings.some((f) => f.severity === 'critical' || f.severity === 'warning');
    /** @type {Record<string, ReviewOutcome>} */
    const outcomes = {};
    for (const { file, content_hash } of hashes) {
      const own = findings.filter((f) => f.file === file);
      const approved = !moved && batch.status === 'reviewed' && (batch.approved || blocking) && own.every((f) => f.severity === 'nit');
      const outcome = { ...batch, ...(moved ? { status: 'stale' } : {}), approved, findings: own, round, kind: 'full', level: entry.level,
        next: { action: approved ? 'complete' : 'fix', level: entry.level } };
      outcomes[file] = outcome;
      if (batch.status === 'reviewed' && !moved) {
        await writeRow({ event: 'review.round', file, content_hash, round, kind: 'full', level: entry.level, open_after: own.filter((f) => f.severity !== 'nit').length });
        if (approved) await writeRow({ event: 'review.approved', file, content_hash });
      }
      const queued = enqueueHash({ repoRoot: ctx.repoRoot, run: ctx.runId, block: ticket.block, file, content_hash, ...(ticket.tree ? { tree: ticket.tree } : {}) });
      const open = approved ? [] : own;
      await writeRow({ event: 'review.result', ticket: queued.ticket, file, content_hash, status: outcome.status, approved, open, worker });
      writeResult({ repoRoot: ctx.repoRoot, runId: ctx.runId, ticket: queued.ticket, result: { ...outcome, block: ticket.block, file, content_hash, open, worker }, key });
    }
    await writeRow({ event: 'review.light_result', signature, outcomes });
    return outcomes[ticket.file];
  });
}

/**
 * B56: the block's OTHER changed files — the §4.1 file set since the block base (tracked changes ∪
 * untracked), filtered to the block's owned files (`ownsFile`, as the budget row does), minus the
 * ticket's file. The review names code moved between them (`## moved code`). No base, no owned
 * list, or a git failure ⇒ [] (no hint; the review itself goes on unchanged).
 * @param {string} repoRoot @param {{base: string | null, owned: string[], ownedKinds?: Record<string, string>, file: string}} opts
 * @returns {Promise<string[]>}
 */
export async function blockPeers(repoRoot, { base, owned, ownedKinds, file }) {
  if (base === null || !Array.isArray(owned) || owned.length === 0) return [];
  try {
    return (await computeFileSet({ cwd: repoRoot, base })).all.filter((f) => f !== file && ownsFile(owned, f, ownedKinds));
  } catch {
    return [];
  }
}

/**
 * B56: ONE memoised reader per ticket of the block's other changed files' diffs: the file set is
 * listed and the diffs read (`peerDiffReader`) at most once, however many packets ask — lazily, so
 * a round with no packet (a stopped file) runs no git call. Any throw or rejection on the way ⇒
 * no peer diffs (no hint), memoised like a success.
 * @param {string} repoRoot @param {{base: string | null, owned: string[], ownedKinds?: Record<string, string>, file: string}} opts
 * @returns {() => Promise<import('../review/moved.mjs').PeerDiff[]>}
 */
export function blockPeerDiffs(repoRoot, { base, owned, ownedKinds, file }) {
  /** @type {Promise<import('../review/moved.mjs').PeerDiff[]> | null} */
  let once = null;
  return () => {
    once ??= (async () => {
      try {
        return await peerDiffReader({ repoRoot, base, file, peers: await blockPeers(repoRoot, { base, owned, ownedKinds, file }) })();
      } catch {
        return [];
      }
    })();
    return once;
  };
}

/**
 * The key of a block's lock (B42): the block's budget row and its L3 rung are taken under it.
 * It carries the run, so two runs in one process never share a lock.
 * @param {string} runId @param {string} block @returns {string}
 */
export const blockLockKey = (runId, block) => `block:${runId}\0${block}`;

/** B68: the `review-state-lost` refusal's message (the file is gone and the ledger has no body). */
export const LOST_MESSAGE = 'the review state file is gone and the ledger cannot rebuild it (its anchor carries no state_body); the file is not reviewed again from round 1 — the owner can run `code-forge block reopen`';

/**
 * Whether the block's one L3 rung is taken: a verified anchor says so (`blockRungUsed`), OR another
 * file of the block has a saved state whose round decided `next: patch` — that round took the rung,
 * but its anchor records `l3_rung_used` only once the patch_check ticket saves. A sibling state that
 * does not load is counted as taken (fail closed; its own ticket refuses it).
 * @param {import('./review-state.mjs').Where} where
 * @returns {{status: 'ok' | 'tampered', used: boolean}}
 */
export function rungTaken(where) {
  const rung = blockRungUsed(where);
  if (rung.status === 'tampered' || rung.used) return rung;
  const anchors = blockAnchors(where) ?? [];
  const siblings = new Set(anchors.map((a) => a.file).filter((f) => typeof f === 'string' && f !== where.file));
  for (const file of siblings) {
    let other;
    try {
      other = loadState({ ...where, file, migrate: false }); // B69: a sibling's old file is read, never moved here
    } catch {
      return { status: 'ok', used: true };
    }
    // a sibling that would be restored (B68) counts by its rebuilt state; lost or tampered fails closed
    if ((other.status !== 'ok' && other.status !== 'restored') || other.state?.next?.action === 'patch') return { status: 'ok', used: true };
  }
  return rung;
}

/**
 * Whether this round could take the block's L3 rung (so it must run under the block lock): never
 * when the rung is taken or the round is the patch_check (the rung is already this file's); never
 * for a file's first full round while the cap and the per-level ladder are both above 1 (round 1
 * cannot stall, reach the cap or exhaust the ladder); else conservatively yes.
 * @param {import('../review/fixloop.mjs').FileState} state @param {'full' | 'recheck' | 'patch_check'} kind
 * @param {Record<string, any>} cfg @param {boolean} rungUsed
 * @returns {boolean}
 */
export function mayTakeRung(state, kind, cfg, rungUsed) {
  if (rungUsed || kind === 'patch_check') return false;
  const int = (/** @type {unknown} */ v, /** @type {number} */ d) => (Number.isInteger(v) && /** @type {number} */ (v) >= 0 ? /** @type {number} */ (v) : d);
  const maxRounds = int(cfg?.review?.max_rounds_per_file, 4);
  const perLevel = int(cfg?.escalation?.review_rounds_per_level, 2);
  if (kind === 'full' && state.round === 0 && state.rounds_at_level === 0 && maxRounds > 1 && perLevel > 1) return false;
  return true;
}

/**
 * One fix-loop round for the ticket (see the module doc).
 *
 * Concurrency (B42): the worker runs several tickets at once but never two for the same
 * (block, file) (its file lock, `loop.mjs`), so this file's state `seq` is read and saved by one
 * ticket at a time. What the files of a BLOCK share is taken under the block lock
 * (`blockLockKey`): the budget row's check-then-write (written once per block) and the L3 rung —
 * the rung read and, for a round that could take the rung (`mayTakeRung`), the whole round and its
 * state save, so two files of one block never both take it. A round that cannot take the rung (a
 * first full round, or any round once the rung is gone) runs outside the block lock, in parallel
 * with the block's other files.
 * @param {import('./queue.mjs').Ticket} ticket @param {ReviewContext} ctx
 * @param {{base: string | null, level: string, owned: string[], ownedKinds?: Record<string, string>, blockKindOf: string, key: Buffer, workDir: string, factsExcerpt: string, rulesDigest?: string, peerDiffs?: () => Promise<import('../review/moved.mjs').PeerDiff[]>, fileContext: () => Promise<FileContext>}} opts
 * @returns {Promise<ReviewOutcome>}
 */
async function fixLoopRound(ticket, ctx, { base, level, owned, ownedKinds, blockKindOf, key, workDir, factsExcerpt, rulesDigest = '', peerDiffs, fileContext }) {
  if (!ctx.readRows || !ctx.writeRow) return { status: 'unavailable', reason: 'no-ledger', approved: false, engine: 'adaptive', sessions: [] };
  const readRows = ctx.readRows;
  /** @type {{outcome: ReviewOutcome} | {round: () => Promise<ReviewOutcome>}} */
  const prepared = await withLock(blockLockKey(ctx.runId, ticket.block), async () => {
    let rows;
    try {
      rows = (await readRows()).filter((r) => r?.run === ctx.runId);
    } catch (err) {
      return { outcome: /** @type {ReviewOutcome} */ ({ status: 'unavailable', reason: failureReason(err), approved: false, engine: 'adaptive', sessions: [] }) };
    }
    // Check-then-write under the block lock: no other ticket of this block reads the rows between
    // this check and the budget row's write, so the row is written once per block.
    if (!rows.some((r) => r?.event === 'review.budget' && r.block === ticket.block)) {
      await recordBlockBudget(ticket, ctx, { base, owned, ownedKinds });
    }
    const where = { runRootDir: ctx.runRootDir, runId: ctx.runId, block: ticket.block, file: ticket.file, key, rows };
    const loaded = loadState({ ...where, contentRoot: ctx.contentRoot ?? ctx.repoRoot });
    const rung = rungTaken(where);
    if (loaded.status === 'tampered' || rung.status === 'tampered') return { outcome: /** @type {ReviewOutcome} */ ({ status: 'refused', reason: 'review-state-tampered', approved: false, engine: 'adaptive', sessions: [] }) };
    // B72: the human's `block reopen` row for this file's latest anchor seq restarts a stopped
    // (on changed content) or lost file: a new state, saved at that seq + 1 — the row works once
    // (`rows`: this run's rows read above under the block lock; `key`: the run key from opts)
    const reopenAt = {
      runId: ctx.runId,
      block: ticket.block,
      file: ticket.file,
      key,
      status: loaded.status,
      state: loaded.state,
      seq: loaded.seq,
      contentHash: ticket.content_hash,
    };
    const reopened = reopenRowFor(rows, reopenAt) !== null;
    if (loaded.status === 'lost' && !reopened) return { outcome: /** @type {ReviewOutcome} */ ({ status: 'refused', reason: 'review-state-lost', message: LOST_MESSAGE, approved: false, engine: 'adaptive', sessions: [] }) };
    let seq = loaded.seq;
    if (loaded.status === 'restored' && loaded.state !== null && !reopened) {
      // B68: the lost file is rebuilt from the anchor's signed body; saved as a new seq, then recorded
      const writeRow = /** @type {(row: Record<string, any>) => Promise<unknown>} */ (ctx.writeRow);
      await saveState({ ...where, state: loaded.state, seq, writeRow, contentRoot: ctx.contentRoot ?? ctx.repoRoot });
      seq += 1;
      await writeRow({ event: RESTORED_EVENT, block: ticket.block, file: ticket.file, seq, from_seq: loaded.seq });
    }
    const rungUsed = rung.used;
    let state = reopened ? null : loaded.state;
    if (state === null || state.status === 'complete') state = newFileState({ file: ticket.file, level, l3RungUsed: rungUsed });
    state.l3_rung_used = state.l3_rung_used || rungUsed;
    // B47: an autopilot extra round raises the file's cap; a file stopped at the old cap reopens
    const extraRounds = extraRoundsFor(rows, { runId: ctx.runId, block: ticket.block, file: ticket.file, key });
    reopenForExtraRound(state, extraRounds, ctx.cfg);

    if (state.status === 'stopped') {
      return { outcome: /** @type {ReviewOutcome} */ ({ ...loopFields(state, null), status: 'stopped', stopped: state.next?.reason ?? 'stopped', ...stopSize(state), approved: false, engine: 'adaptive', sessions: [], findings: state.open }) };
    }

    /** @type {'full' | 'recheck' | 'patch_check'} */
    let kind = state.round === 0 ? 'full' : 'recheck';
    if (state.next?.action === 'retry') kind = state.pending_kind ?? kind;
    else if (state.next?.action === 'patch') {
      kind = 'patch_check';
      state.l3_rung_used = true; // the orchestrator ran the block's one L3 patch before this ticket
    }
    const fixed = state;
    const round = () => playRound(ticket, ctx, { state: fixed, kind, where, seq, base, blockKindOf, workDir, factsExcerpt, rulesDigest, extraRounds, peerDiffs, fileContext });
    // a round that could take the rung keeps the block lock until its state is saved
    if (mayTakeRung(fixed, kind, ctx.cfg, rungUsed)) return { outcome: await round() };
    return { round };
  });
  return 'outcome' in prepared ? prepared.outcome : prepared.round();
}

/**
 * Run the prepared round and save the file's state: the ticket's outcome.
 * @param {import('./queue.mjs').Ticket} ticket @param {ReviewContext} ctx
 * @param {{state: import('../review/fixloop.mjs').FileState, kind: 'full' | 'recheck' | 'patch_check', where: import('./review-state.mjs').Where, seq: number, base: string | null, blockKindOf: string, workDir: string, factsExcerpt: string, rulesDigest?: string, extraRounds?: number, peerDiffs?: () => Promise<import('../review/moved.mjs').PeerDiff[]>, fileContext: () => Promise<FileContext>}} opts
 * @returns {Promise<ReviewOutcome>}
 */
async function playRound(ticket, ctx, { state, kind, where, seq, base, blockKindOf, workDir, factsExcerpt, rulesDigest = '', extraRounds = 0, peerDiffs, fileContext }) {
  /** @type {Record<string, any> | null} */
  let engineOutcome = null;
  let approvalWritten = false;
  /** @type {Array<Record<string, any>>} */
  const rechecks = [];
  /** @param {Record<string, any>} row */
  const writeRow = async (row) => {
    if (!ctx.writeRow) throw new Error('no-ledger');
    if (row.event === 'review.approved') {
      // the approval names the reviewed bytes: a file that moved since the ticket is not approved
      if (row.content_hash !== ticket.content_hash) throw new Error('hash-moved');
      await ctx.writeRow(row);
      approvalWritten = true;
      return undefined;
    }
    return ctx.writeRow(row);
  };
  const jev = ctx.jev ?? (typeof ctx.jevKey === 'string' && ctx.jevKey.length > 0 ? jevWith(ctx.jevKey) : undefined);
  const before = JSON.stringify(state);
  /** @type {string | null} */
  let crashed = null;
  try {
    // B55: a packet's schema misses are counted from the run's signed rows for this run, file,
    // content hash AND packet hash (`ledgerSchemaHistory` is asked per packet). Built inside the
    // guarded block: a throw here is this round's `unavailable` with `next: retry`, like any other;
    // a ledger read that throws later only disables the second-level try (with one warning line).
    // Once per packet holds because this ticket holds the (block, file) file lock (`loop.mjs`) for
    // the whole round — round 1, its retry, a recheck and the patch_check alike — and
    // `schema-fallback.mjs` adds a per-packet lock around read-history → write-mark.
    // The rows are matched on the values the worker's writeRow stamps on them (`loop.mjs`:
    // `{...row, block, file, content_hash: ticket.content_hash}`): `ticket.file` and
    // `ticket.content_hash` here are the same ticket's, for round 1 and rechecks alike (a recheck
    // ticket's hash is the fixed content's). `where.key` is the run key: `loop.mjs` puts the run's
    // `key` in `ctx.key` (`key, // signs the fix-loop state`), `reviewTicket` passes `key: ctx.key`
    // to `fixLoopRound`, which builds `where = {…, key, rows}`. A row whose check throws is not
    // counted (`ledgerSchemaHistory` catches per row).
    const schemaHistory = ctx.readRows
      ? ledgerSchemaHistory({ readRows: ctx.readRows, verify: (row) => verifyRow(row, where.key).ok, runId: ctx.runId, file: ticket.file, contentHash: ticket.content_hash })
      : undefined;
    const files = await fileContext();
    await runRound(
      state,
      {
        repoRoot: ctx.contentRoot ?? ctx.repoRoot,
        cfg: ctx.cfg,
        workDir,
        base,
        writeRow,
        factsExcerpt,
        ...(rulesDigest !== '' ? { rulesDigest } : {}),
        extraRounds,
        ...files,
        ...(peerDiffs ? { peerDiffs } : {}),
        ...(jev ? { jev } : {}),
        ...(schemaHistory ? { schemaHistory } : {}),
        review: async () => {
          engineOutcome = await reviewFile({ repoRoot: ctx.contentRoot ?? ctx.repoRoot, file: ticket.file, base, cfg: ctx.cfg, kind: blockKindOf, workDir, factsExcerpt, ...files, ...(rulesDigest !== '' ? { rulesDigest } : {}), ...(peerDiffs ? { peerDiffs } : {}) }, { spawn: ctx.spawn, writeRow, ...(schemaHistory ? { schemaHistory } : {}) });
          return engineOutcome;
        },
        spawn: async (opts) => {
          const res = await ctx.spawn(opts);
          rechecks.push({ lens: opts?.rowExtra?.lens ?? null, role: opts?.role ?? null, level: opts?.level ?? null, provider: res?.provider ?? null, model: res?.model ?? null,
            status: res?.status ?? null, tokens_in: res?.usage?.tokens_in ?? res?.row?.tokens_in ?? null, tokens_out: res?.usage?.tokens_out ?? res?.row?.tokens_out ?? null,
            ...(typeof res?.status === 'string' && res.status !== 'ok' ? { reason: res.reason ?? res.status } : {}),
          });
          return res;
        },
      },
      { kind },
    );
  } catch (err) {
    // A throw mid-round (the engine, a session, S1, `git diff`) is `unavailable`, never an
    // exception the worker turns into `engine-error` with the state UNSAVED. `runRound` stages
    // the round on a draft and commits it only after its gate rows (`review.round`, `review.cap`)
    // are written, so a throw leaves `state` as it was and no `review.round` row exists for the
    // round that threw: the state is saved as `next: retry` with the pending kind — exactly what
    // `fixloop` records for an unavailable session — so its anchor stays the latest and the next
    // ticket re-runs THIS round. (An unsaved state would re-run the earlier rounds' rows too; a
    // state that was already committed is kept as committed, its rows are in the ledger.)
    crashed = failureReason(err);
    if (JSON.stringify(state) === before) {
      state.next = { action: 'retry', reason: crashed };
      state.pending_kind = kind;
    }
  }
  // B72: a stop (any reason) records the content it reviewed — the ticket's — in the signed state,
  // so `block reopen` and the worker know which content this stop saw (`stopContentHash`)
  const stamped = /** @type {Record<string, any>} */ (state);
  if (state.status === 'stopped') stamped.stop_content_hash = ticket.content_hash;
  else delete stamped.stop_content_hash;
  await saveState({ ...where, state, seq, writeRow: /** @type {(row: Record<string, any>) => Promise<unknown>} */ (ctx.writeRow), contentRoot: ctx.contentRoot ?? ctx.repoRoot });

  const eng = /** @type {Record<string, any> | null} */ (engineOutcome);
  const head = { ...loopFields(state, kind), engine: eng?.engine ?? 'adaptive', sessions: eng?.sessions ?? rechecks };
  if (crashed !== null) return { ...head, status: 'unavailable', reason: crashed, approved: false };
  const next = state.next;
  // `runRound` moved the state on: re-read its status (not the narrowed pre-round value)
  const status = /** @type {'open' | 'complete' | 'stopped'} */ (state.status);
  if (next?.action === 'retry') {
    if (eng && eng.status !== 'reviewed') return /** @type {ReviewOutcome} */ ({ ...eng, ...loopFields(state, kind), approved: false });
    return { ...head, status: 'unavailable', reason: next.reason ?? 'unavailable', approved: false };
  }
  if (status === 'complete') {
    if (!approvalWritten) return { ...head, status: 'unavailable', reason: 'approval-not-written', approved: false };
    return { ...head, status: 'reviewed', approved: true, findings: [] };
  }
  if (status === 'stopped') return { ...head, status: 'stopped', stopped: next?.reason ?? 'stopped', ...stopSize(state), approved: false, findings: state.open };
  return { ...head, status: 'reviewed', approved: false, reason: next?.reason === 'no-fix-hunks' ? 'no-fix-hunks' : 'findings-open', findings: state.open };
}

/**
 * The block's review budget (§4.10, B19): the forecast over the block's changed owned files (the
 * ticket's file always included), each tiered by its path floor (`rulesRisk` with no diff — S1 has
 * not run yet), written as ONE `review.budget` row (+ `review.over_budget` when over). Best effort:
 * a git or ledger failure is swallowed, the review goes on.
 * @param {import('./queue.mjs').Ticket} ticket @param {ReviewContext} ctx
 * @param {{base: string | null, owned: string[], ownedKinds?: Record<string, string>}} opts
 */
async function recordBlockBudget(ticket, ctx, { base, owned, ownedKinds }) {
  try {
    /** @type {string[]} */
    let changed = [];
    if (base !== null && owned.length > 0) {
      try {
        changed = (await computeFileSet({ cwd: ctx.contentRoot ?? ctx.repoRoot, base })).all.filter((f) => ownsFile(owned, f, ownedKinds));
      } catch {
        // no row on a git failure: a ticket-only forecast would be permanent (the row is written
        // once per block); the block's next keyed ticket retries
        return;
      }
    }
    const files = [...new Set([...changed, ticket.file])].sort().map((file) => ({ file, tier: tierOf({ risk: rulesRisk({ file, plusCount: 0, cfg: ctx.cfg }) }) }));
    await planAndRecord({ files, cfg: ctx.cfg, block: ticket.block, blockOnly: true, writeRow: /** @type {(row: Record<string, any>) => Promise<unknown>} */ (ctx.writeRow) });
  } catch {
    // the budget row is a report dataset, never a reason to fail the review
  }
}

/**
 * The `unavailable` reason for a throw: the error's code when it has one, else its message
 * through B0 `redact` (never a secret; a message can quote a path, never config values).
 * @param {unknown} err @returns {string}
 */
function failureReason(err) {
  const code = /** @type {any} */ (err)?.code;
  if (typeof code === 'string' && code.length > 0) return code;
  const text = err instanceof Error ? err.message : String(err);
  const safe = /** @type {string} */ (redact(text));
  return safe.length > 0 ? safe : 'round-failed';
}

/**
 * @param {import('../review/fixloop.mjs').FileState} state @param {string | null} kind
 * @returns {Record<string, any>}
 */
function loopFields(state, kind) {
  return { round: state.round, ...(kind ? { kind } : {}), level: state.level, next: state.next, trigger: state.next?.trigger ?? null, late: state.late.length };
}

/**
 * B54: a `split_required` stop's packet size (`tokens_in`, `budget` — null when unknown — and a
 * Markdown `section`), from the state's `next`; nothing for any other stop.
 * @param {import('../review/fixloop.mjs').FileState} state @returns {{tokens_in?: number | null, budget?: number | null, section?: string}}
 */
function stopSize(state) {
  const next = state.next;
  if (next?.reason !== 'split_required') return {};
  const fin = (/** @type {unknown} */ v) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
  return { tokens_in: fin(next.tokens_in), budget: fin(next.budget), ...(typeof next.section === 'string' ? { section: next.section } : {}) };
}

/**
 * S1 through `askJev` with the worker's key bound (the key stays in this process).
 * @param {string} key @returns {import('../review/triage.mjs').JevAsk}
 */
function jevWith(key) {
  return (req) => askJev({ ...req, key });
}
