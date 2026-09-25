/**
 * The review-engine hook (plan §4.2–§4.4; B11 seam, body by B12a).
 *
 * `reviewTicket(ticket, ctx)` is the ONE seam between the worker (queue, keys, signing, pid
 * hygiene — B11) and the review engine (`src/review/engine.mjs`, B12a). The worker signs whatever
 * it returns. The hook looks up the block's base SHA in the run record and gives the engine a packet
 * directory under the run's temp root (removed afterwards). A run record that exists but cannot be
 * read is `unavailable`, never a review against a guessed base.
 * The engine NEVER approves an empty or failed review: that is `unavailable` (§4.2 stub guard).
 */

import { rmSync } from 'node:fs';
import path from 'node:path';
import { reviewFile } from '../review/engine.mjs';
import { readRun } from '../state/run.mjs';
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
 */

/**
 * @typedef {object} ReviewOutcome
 * @property {string} status - `reviewed`, `unavailable`, `no_change`, `split_required`, `refused`.
 * @property {boolean} approved
 * @property {string} engine - `adaptive` or `consensus`.
 * @property {Array<Record<string, any>>} sessions - one summary per session run.
 * @property {string} [reason] - why it is not `reviewed` (stub-guard reason, `needs_file-refused`, …).
 * @property {Array<Record<string, any>>} [findings] - the final answer's findings (`reviewed` only).
 */

/**
 * The block's base SHA from the run record. `null` (⇒ HEAD) ONLY when no run record exists
 * (`no-run`). Once a record exists, a ticket naming a block it does not list throws
 * `block-unknown`; a record that cannot be read or parsed throws `run-record-unreadable`, and a
 * block entry without a base `block-base-missing` — a guessed base could let a change escape review.
 * @param {string} runId @param {string} block
 * @returns {Promise<string | null>}
 */
export async function baseFor(runId, block) {
  let record;
  try {
    record = await readRun(runId);
  } catch (err) {
    if (/** @type {any} */ (err)?.code === 'no-run') return null;
    throw new Error('run-record-unreadable');
  }
  if (!record || typeof record !== 'object' || !record.blocks || typeof record.blocks !== 'object') throw new Error('run-record-unreadable');
  if (typeof block !== 'string' || !Object.hasOwn(record.blocks, block)) throw new Error('block-unknown');
  const entry = record.blocks[block];
  const sha = entry?.base_sha;
  if (typeof sha !== 'string' || sha.length === 0) throw new Error('block-base-missing');
  return sha;
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
  let base;
  try {
    base = await baseFor(ctx.runId, ticket.block);
  } catch (err) {
    return { status: 'unavailable', reason: err instanceof Error ? err.message : 'run-record-unreadable', approved: false, engine: 'adaptive', sessions: [] };
  }
  try {
    return await reviewFile(
      { repoRoot: ctx.repoRoot, file: ticket.file, base, cfg: ctx.cfg, workDir },
      { spawn: ctx.spawn, ...(ctx.writeRow ? { writeRow: ctx.writeRow } : {}) },
    );
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}
