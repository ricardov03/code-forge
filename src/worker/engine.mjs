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
 *    repeats the stop — only the human moves on (§4.11 rule 5).
 * The round's outcome: `approved: true` ONLY when the loop is `complete` AND its signed
 * `review.approved` row (keyed `run, block, file, content_hash` = the ticket's hash) was written;
 * else `findings` = the open fix list, `next` = what the orchestrator does (`fix` at `level`,
 * `trigger: review_stall | review_rounds`, `patch`), `stopped` = the stop reason.
 * Without `ctx.key` (a direct call) the hook is the engine alone and writes no approval row.
 * The first keyed ticket of a block (no `review.budget` row for it in the run yet) records the
 * block's review budget (§4.10, B19): the forecast over the block's changed owned files (tier from
 * the path floors, before S1), one `review.budget` row per block; a failure there never fails the
 * review.
 * The review NEVER approves an empty or failed session: that is `unavailable` (§4.2 stub guard).
 * A throw inside the round is `unavailable` too, with the state saved as `next: retry` (see
 * `fixLoopRound`): the next ticket re-runs that round, never a fresh round 1.
 */

import { rmSync } from 'node:fs';
import path from 'node:path';
import { askJev } from '../decide/jev-client.mjs';
import { planAndRecord, tierOf } from '../review/budget.mjs';
import { reviewFile, rulesRisk } from '../review/engine.mjs';
import { computeFileSet, ownsFile } from '../gates/scope.mjs';
import { newFileState, runRound } from '../review/fixloop.mjs';
import { readRun } from '../state/run.mjs';
import { redact } from '../util/redact.mjs';
import { blockRungUsed, loadState, saveState } from './review-state.mjs';
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
 */

/**
 * The block's entry in the run record: `null` (⇒ HEAD, level L2) ONLY when no run record exists
 * (`no-run`). Once a record exists, a ticket naming a block it does not list throws
 * `block-unknown`; a record that cannot be read or parsed throws `run-record-unreadable`, and a
 * block entry without a base `block-base-missing` — a guessed base could let a change escape review.
 * @param {string} runId @param {string} block
 * @returns {Promise<{base: string | null, level: string, owned: string[]}>}
 */
export async function blockEntryFor(runId, block) {
  let record;
  try {
    record = await readRun(runId);
  } catch (err) {
    if (/** @type {any} */ (err)?.code === 'no-run') return { base: null, level: 'L2', owned: [] };
    throw new Error('run-record-unreadable');
  }
  if (!record || typeof record !== 'object' || !record.blocks || typeof record.blocks !== 'object') throw new Error('run-record-unreadable');
  if (typeof block !== 'string' || !Object.hasOwn(record.blocks, block)) throw new Error('block-unknown');
  const entry = record.blocks[block];
  const sha = entry?.base_sha;
  if (typeof sha !== 'string' || sha.length === 0) throw new Error('block-base-missing');
  const owned = Array.isArray(entry.owned_files) ? entry.owned_files.filter((f) => typeof f === 'string') : [];
  return { base: sha, level: typeof entry.level === 'string' && /^L[0-3]$/.test(entry.level) ? entry.level : 'L2', owned };
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
  try {
    if (!ctx.key) {
      return await reviewFile(
        { repoRoot: ctx.repoRoot, file: ticket.file, base: entry.base, cfg: ctx.cfg, workDir },
        { spawn: ctx.spawn, ...(ctx.writeRow ? { writeRow: ctx.writeRow } : {}) },
      );
    }
    return await fixLoopRound(ticket, ctx, { base: entry.base, level: entry.level, owned: entry.owned, key: ctx.key, workDir });
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

/**
 * One fix-loop round for the ticket (see the module doc).
 * @param {import('./queue.mjs').Ticket} ticket @param {ReviewContext} ctx
 * @param {{base: string | null, level: string, owned: string[], key: Buffer, workDir: string}} opts
 * @returns {Promise<ReviewOutcome>}
 */
async function fixLoopRound(ticket, ctx, { base, level, owned, key, workDir }) {
  if (!ctx.readRows || !ctx.writeRow) return { status: 'unavailable', reason: 'no-ledger', approved: false, engine: 'adaptive', sessions: [] };
  let rows;
  try {
    rows = (await ctx.readRows()).filter((r) => r?.run === ctx.runId);
  } catch (err) {
    return { status: 'unavailable', reason: failureReason(err), approved: false, engine: 'adaptive', sessions: [] };
  }
  // Check-then-write is safe here: a run has exactly ONE worker, and it processes its tickets one
  // at a time (`loop.mjs` awaits each `processTicket`), so no second ticket of this block can read
  // the rows between this check and the budget row's write.
  if (!rows.some((r) => r?.event === 'review.budget' && r.block === ticket.block)) {
    await recordBlockBudget(ticket, ctx, { base, owned });
  }
  const where = { runRootDir: ctx.runRootDir, runId: ctx.runId, block: ticket.block, file: ticket.file, key, rows };
  const loaded = loadState(where);
  const rung = blockRungUsed(where);
  if (loaded.status === 'tampered' || rung.status === 'tampered') return { status: 'refused', reason: 'review-state-tampered', approved: false, engine: 'adaptive', sessions: [] };
  const rungUsed = rung.used;
  let state = loaded.state;
  if (state === null || state.status === 'complete') state = newFileState({ file: ticket.file, level, l3RungUsed: rungUsed });
  state.l3_rung_used = state.l3_rung_used || rungUsed;

  if (state.status === 'stopped') {
    return { ...loopFields(state, null), status: 'stopped', stopped: state.next?.reason ?? 'stopped', approved: false, engine: 'adaptive', sessions: [], findings: state.open };
  }

  /** @type {'full' | 'recheck' | 'patch_check'} */
  let kind = state.round === 0 ? 'full' : 'recheck';
  if (state.next?.action === 'retry') kind = state.pending_kind ?? kind;
  else if (state.next?.action === 'patch') {
    kind = 'patch_check';
    state.l3_rung_used = true; // the orchestrator ran the block's one L3 patch before this ticket
  }

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
    await runRound(
      state,
      {
        repoRoot: ctx.repoRoot,
        cfg: ctx.cfg,
        workDir,
        base,
        writeRow,
        ...(jev ? { jev } : {}),
        review: async () => {
          engineOutcome = await reviewFile({ repoRoot: ctx.repoRoot, file: ticket.file, base, cfg: ctx.cfg, workDir }, { spawn: ctx.spawn, writeRow });
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
  await saveState({ ...where, state, seq: loaded.seq, writeRow: /** @type {(row: Record<string, any>) => Promise<unknown>} */ (ctx.writeRow) });

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
  if (status === 'stopped') return { ...head, status: 'stopped', stopped: next?.reason ?? 'stopped', approved: false, findings: state.open };
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
 * S1 through `askJev` with the worker's key bound (the key stays in this process).
 * @param {string} key @returns {import('../review/triage.mjs').JevAsk}
 */
function jevWith(key) {
  return (req) => askJev({ ...req, key });
}
