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
 * (`blockPeerDiffs`: listed and read at most once per ticket) so the review can name code moved
 * between them in a `## moved code` packet section (round 1, rechecks and patch check alike).
 * The review NEVER approves an empty or failed session: that is `unavailable` (§4.2 stub guard).
 * A throw inside the round is `unavailable` too, with the state saved as `next: retry` (see
 * `fixLoopRound`): the next ticket re-runs that round, never a fresh round 1.
 */

import { rmSync } from 'node:fs';
import path from 'node:path';
import { blockKind } from '../decide/escalation.mjs';
import { askJev } from '../decide/jev-client.mjs';
import { planAndRecord, tierOf } from '../review/budget.mjs';
import { budgetFor } from '../review/packet.mjs';
import { reviewFile, rulesRisk } from '../review/engine.mjs';
import { peerDiffReader } from '../review/moved.mjs';
import { computeFileSet, ownsFile } from '../gates/scope.mjs';
import { extraRoundsFor, newFileState, reopenForExtraRound, runRound } from '../review/fixloop.mjs';
import { readRun } from '../state/run.mjs';
import { ledgerSchemaHistory } from '../review/schema-fallback.mjs';
import { verifyRow } from '../state/signer.mjs';
import { redact } from '../util/redact.mjs';
import { keyedLock } from '../util/locks.mjs';
import { blockAnchors, blockRungUsed, loadState, saveState } from './review-state.mjs';
import { assertTicketId } from './ticket.mjs';

/**
 * @typedef {object} ReviewContext
 * @property {string} repoRoot
 * @property {string} runId
 * @property {string} runRootDir - the run's temp root (packets and session dirs live under it).
 * @property {Record<string, any>} cfg - the project config.
 * @property {string | null} jevKey - resolved once by the worker through B2's chain; never print.
 * @property {(opts: Record<string, any>) => Promise<Record<string, any>>} spawn - one isolated
 *   session (`spawnSession` opts minus `runRoot`, `run`, `block`, `timeoutMs`; `cfg` defaults to
 *   the worker's and a per-call `cfg` wins).
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeRow] - a signed ledger row
 *   (the worker adds `run`, `block`, `file`, `content_hash`).
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
 * of its owned files.
 *   With no run record the kind is null: `reviewTicket` takes the ticket file's kind.
 * @returns {Promise<{base: string | null, level: string, owned: string[], acceptance?: unknown, kind: string | null}>}
 */
export async function blockEntryFor(runId, block) {
  let record;
  try {
    record = await readRun(runId);
  } catch (err) {
    if (/** @type {any} */ (err)?.code === 'no-run') return { base: null, level: 'L2', owned: [], acceptance: undefined, kind: null };
    throw new Error('run-record-unreadable');
  }
  if (!record || typeof record !== 'object' || !record.blocks || typeof record.blocks !== 'object') throw new Error('run-record-unreadable');
  if (typeof block !== 'string' || !Object.hasOwn(record.blocks, block)) throw new Error('block-unknown');
  const entry = record.blocks[block];
  const sha = entry?.base_sha;
  if (typeof sha !== 'string' || sha.length === 0) throw new Error('block-base-missing');
  const owned = Array.isArray(entry.owned_files) ? entry.owned_files.filter((f) => typeof f === 'string') : [];
  return { base: sha, level: typeof entry.level === 'string' && /^L[0-3]$/.test(entry.level) ? entry.level : 'L2', owned, acceptance: entry.acceptance, kind: blockKind({ owned, declared: entry.kind }) };
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
  const peerDiffs = blockPeerDiffs(ctx.repoRoot, { base: entry.base, owned: entry.owned, file: ticket.file });
  try {
    if (!ctx.key) {
      return await reviewFile(
        { repoRoot: ctx.repoRoot, file: ticket.file, base: entry.base, cfg: ctx.cfg, kind: entry.kind, workDir, factsExcerpt: acceptanceExcerpt(entry.acceptance, ctx.cfg), peerDiffs },
        { spawn: ctx.spawn, ...(ctx.writeRow ? { writeRow: ctx.writeRow } : {}) },
      );
    }
    return await fixLoopRound(ticket, ctx, { base: entry.base, level: entry.level, owned: entry.owned, blockKindOf: entry.kind, key: ctx.key, workDir, factsExcerpt: acceptanceExcerpt(entry.acceptance, ctx.cfg), peerDiffs });
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

/**
 * B56: the block's OTHER changed files — the §4.1 file set since the block base (tracked changes ∪
 * untracked), filtered to the block's owned files (`ownsFile`, as the budget row does), minus the
 * ticket's file. The review names code moved between them (`## moved code`). No base, no owned
 * list, or a git failure ⇒ [] (no hint; the review itself goes on unchanged).
 * @param {string} repoRoot @param {{base: string | null, owned: string[], file: string}} opts
 * @returns {Promise<string[]>}
 */
export async function blockPeers(repoRoot, { base, owned, file }) {
  if (base === null || !Array.isArray(owned) || owned.length === 0) return [];
  try {
    return (await computeFileSet({ cwd: repoRoot, base })).all.filter((f) => f !== file && ownsFile(owned, f));
  } catch {
    return [];
  }
}

/**
 * B56: ONE memoised reader per ticket of the block's other changed files' diffs: the file set is
 * listed and the diffs read (`peerDiffReader`) at most once, however many packets ask — lazily, so
 * a round with no packet (a stopped file) runs no git call. Any throw or rejection on the way ⇒
 * no peer diffs (no hint), memoised like a success.
 * @param {string} repoRoot @param {{base: string | null, owned: string[], file: string}} opts
 * @returns {() => Promise<import('../review/moved.mjs').PeerDiff[]>}
 */
export function blockPeerDiffs(repoRoot, { base, owned, file }) {
  /** @type {Promise<import('../review/moved.mjs').PeerDiff[]> | null} */
  let once = null;
  return () => {
    once ??= (async () => {
      try {
        return await peerDiffReader({ repoRoot, base, file, peers: await blockPeers(repoRoot, { base, owned, file }) })();
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
      other = loadState({ ...where, file });
    } catch {
      return { status: 'ok', used: true };
    }
    if (other.status !== 'ok' || other.state?.next?.action === 'patch') return { status: 'ok', used: true };
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
 * @param {{base: string | null, level: string, owned: string[], blockKindOf: string, key: Buffer, workDir: string, factsExcerpt: string, peerDiffs?: () => Promise<import('../review/moved.mjs').PeerDiff[]>}} opts
 * @returns {Promise<ReviewOutcome>}
 */
async function fixLoopRound(ticket, ctx, { base, level, owned, blockKindOf, key, workDir, factsExcerpt, peerDiffs }) {
  if (!ctx.readRows || !ctx.writeRow) return { status: 'unavailable', reason: 'no-ledger', approved: false, engine: 'adaptive', sessions: [] };
  const readRows = ctx.readRows;
  /** @type {{outcome: ReviewOutcome} | {round: () => Promise<ReviewOutcome>}} */
  const prepared = await keyedLock(blockLockKey(ctx.runId, ticket.block), async () => {
    let rows;
    try {
      rows = (await readRows()).filter((r) => r?.run === ctx.runId);
    } catch (err) {
      return { outcome: /** @type {ReviewOutcome} */ ({ status: 'unavailable', reason: failureReason(err), approved: false, engine: 'adaptive', sessions: [] }) };
    }
    // Check-then-write under the block lock: no other ticket of this block reads the rows between
    // this check and the budget row's write, so the row is written once per block.
    if (!rows.some((r) => r?.event === 'review.budget' && r.block === ticket.block)) {
      await recordBlockBudget(ticket, ctx, { base, owned });
    }
    const where = { runRootDir: ctx.runRootDir, runId: ctx.runId, block: ticket.block, file: ticket.file, key, rows };
    const loaded = loadState(where);
    const rung = rungTaken(where);
    if (loaded.status === 'tampered' || rung.status === 'tampered') return { outcome: /** @type {ReviewOutcome} */ ({ status: 'refused', reason: 'review-state-tampered', approved: false, engine: 'adaptive', sessions: [] }) };
    const rungUsed = rung.used;
    let state = loaded.state;
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
    const round = () => playRound(ticket, ctx, { state: fixed, kind, where, seq: loaded.seq, base, blockKindOf, workDir, factsExcerpt, extraRounds, peerDiffs });
    // a round that could take the rung keeps the block lock until its state is saved
    if (mayTakeRung(fixed, kind, ctx.cfg, rungUsed)) return { outcome: await round() };
    return { round };
  });
  return 'outcome' in prepared ? prepared.outcome : prepared.round();
}

/**
 * Run the prepared round and save the file's state: the ticket's outcome.
 * @param {import('./queue.mjs').Ticket} ticket @param {ReviewContext} ctx
 * @param {{state: import('../review/fixloop.mjs').FileState, kind: 'full' | 'recheck' | 'patch_check', where: import('./review-state.mjs').Where, seq: number, base: string | null, blockKindOf: string, workDir: string, factsExcerpt: string, extraRounds?: number, peerDiffs?: () => Promise<import('../review/moved.mjs').PeerDiff[]>}} opts
 * @returns {Promise<ReviewOutcome>}
 */
async function playRound(ticket, ctx, { state, kind, where, seq, base, blockKindOf, workDir, factsExcerpt, extraRounds = 0, peerDiffs }) {
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
    await runRound(
      state,
      {
        repoRoot: ctx.repoRoot,
        cfg: ctx.cfg,
        workDir,
        base,
        writeRow,
        factsExcerpt,
        extraRounds,
        ...(peerDiffs ? { peerDiffs } : {}),
        ...(jev ? { jev } : {}),
        ...(schemaHistory ? { schemaHistory } : {}),
        review: async () => {
          engineOutcome = await reviewFile({ repoRoot: ctx.repoRoot, file: ticket.file, base, cfg: ctx.cfg, kind: blockKindOf, workDir, factsExcerpt, ...(peerDiffs ? { peerDiffs } : {}) }, { spawn: ctx.spawn, writeRow, ...(schemaHistory ? { schemaHistory } : {}) });
          return engineOutcome;
        },
        spawn: async (opts) => {
          const res = await ctx.spawn(opts);
          rechecks.push({ lens: opts?.rowExtra?.lens ?? null, role: opts?.role ?? null, level: opts?.level ?? null, provider: res?.provider ?? null, model: res?.model ?? null });
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
  await saveState({ ...where, state, seq, writeRow: /** @type {(row: Record<string, any>) => Promise<unknown>} */ (ctx.writeRow) });

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
  return { ...head, status: 'reviewed', approved: false, reason: 'findings-open', findings: state.open };
}

/**
 * The block's review budget (§4.10, B19): the forecast over the block's changed owned files (the
 * ticket's file always included), each tiered by its path floor (`rulesRisk` with no diff — S1 has
 * not run yet), written as ONE `review.budget` row (+ `review.over_budget` when over). Best effort:
 * a git or ledger failure is swallowed, the review goes on.
 * @param {import('./queue.mjs').Ticket} ticket @param {ReviewContext} ctx
 * @param {{base: string | null, owned: string[]}} opts
 */
async function recordBlockBudget(ticket, ctx, { base, owned }) {
  try {
    /** @type {string[]} */
    let changed = [];
    if (base !== null && owned.length > 0) {
      try {
        changed = (await computeFileSet({ cwd: ctx.repoRoot, base })).all.filter((f) => ownsFile(owned, f));
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
