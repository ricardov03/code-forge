/**
 * The review queue on disk (plan §1.2, §4.8; block B11). Everything is a file inside the
 * workspace, so a Codex `workspace-write` coder with no network can enqueue and poll:
 *
 *   <repo>/.code-forge/queue/<ticket>.json   the ticket (written by `review-file`, idempotent)
 *   <repo>/.code-forge/queue/<ticket>.done   the worker's done marker `{ticket, status, result}`
 *   <repo>/.code-forge/queue/worker.json     the worker's announcement `{pid, start_time, run, started_at}`
 *   <repo>/.code-forge/reviews/<run>/<ticket>.json   the SIGNED result (run key HMAC, §8.6)
 *
 * The forbidden list (§8.4 ★) denies a coder any write under `reviews/` and to `queue/*.done`;
 * the MAC makes an edit visible anyway (T1/T2). Writes are atomic (temp file + rename).
 */

import { randomBytes } from 'node:crypto';
import { existsSync, mkdirSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { loadKey, signRow, verifyRow } from '../state/signer.mjs';
import { isAlive, readStartTime, UNKNOWN_START_TIME } from '../util/reaper.mjs';
import { assertRowPath, assertTicketId, contentHash, ticketId, WorkerError } from './ticket.mjs';

export const WORKER_FILE = 'worker.json';

/** @param {string} repoRoot */
export const queueDir = (repoRoot) => path.join(repoRoot, '.code-forge', 'queue');
/** @param {string} repoRoot @param {string} runId */
export const reviewsDir = (repoRoot, runId) => path.join(repoRoot, '.code-forge', 'reviews', runId);

/** @param {string} file @param {string} text */
function writeAtomic(file, text) {
  mkdirSync(path.dirname(file), { recursive: true });
  const tmp = `${file}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
  try {
    writeFileSync(tmp, text, { mode: 0o644 });
    renameSync(tmp, file);
  } catch (err) {
    rmSync(tmp, { force: true });
    throw err;
  }
}

/** @param {string} file @returns {any} parsed JSON, or null when missing or unparseable. */
function readJSON(file) {
  try {
    return JSON.parse(readFileSync(file, 'utf8'));
  } catch {
    return null;
  }
}

/**
 * @typedef {{ticket: string, run: string, block: string, file: string, content_hash: string, enqueued_at: string}} Ticket
 */

/**
 * Enqueue `file` (already repo-root-relative) for review. Idempotent by content hash: the same
 * content returns the same ticket and never rewrites it.
 * @param {{repoRoot: string, run: string, block: string, file: string, now?: Date}} opts
 * @returns {{ticket: string, status: 'queued' | 'done', file: string, content_hash: string}}
 */
export function enqueue({ repoRoot, run, block, file, now = new Date() }) {
  const rel = assertRowPath(file);
  const content_hash = contentHash(repoRoot, rel);
  const ticket = ticketId({ run, block, file: rel, content_hash });
  const target = path.join(queueDir(repoRoot), `${ticket}.json`);
  if (!existsSync(target)) {
    /** @type {Ticket} */
    const body = { ticket, run, block, file: rel, content_hash, enqueued_at: now.toISOString() };
    writeAtomic(target, `${JSON.stringify(body)}\n`);
  }
  return { ticket, status: isDone(repoRoot, ticket) ? 'done' : 'queued', file: rel, content_hash };
}

/** @param {string} repoRoot @param {string} ticket */
export function isDone(repoRoot, ticket) {
  return existsSync(path.join(queueDir(repoRoot), `${assertTicketId(ticket)}.done`));
}

/**
 * Tickets with no done marker, oldest first (enqueue time, then id). Unparseable files are
 * skipped here and refused by the worker when it reads them.
 * @param {string} repoRoot
 * @returns {string[]} ticket ids
 */
export function pendingTickets(repoRoot) {
  let names;
  try {
    names = readdirSync(queueDir(repoRoot));
  } catch {
    return [];
  }
  const ids = names.filter((n) => /^[0-9a-f]{24}\.json$/.test(n)).map((n) => n.slice(0, 24));
  const pending = ids.filter((id) => !names.includes(`${id}.done`));
  const at = (/** @type {string} */ id) => {
    const t = readJSON(path.join(queueDir(repoRoot), `${id}.json`));
    return typeof t?.enqueued_at === 'string' ? t.enqueued_at : '';
  };
  return pending.map((id) => ({ id, at: at(id) })).sort((a, b) => (a.at === b.at ? (a.id < b.id ? -1 : 1) : a.at < b.at ? -1 : 1)).map((e) => e.id);
}

/**
 * Read and re-validate a ticket (coder-writable): its id must match its content.
 * @param {string} repoRoot @param {string} id
 * @returns {Ticket}
 * @throws {WorkerError} `bad-ticket` / `bad-path`
 */
export function readTicket(repoRoot, id) {
  const t = readJSON(path.join(queueDir(repoRoot), `${assertTicketId(id)}.json`));
  if (!t || typeof t !== 'object') throw new WorkerError('bad-ticket', `ticket ${id} is not readable JSON`);
  assertRowPath(t.file);
  if (typeof t.run !== 'string' || typeof t.block !== 'string' || typeof t.content_hash !== 'string' || ticketId(t) !== id) {
    throw new WorkerError('bad-ticket', `ticket ${id} does not match its id`);
  }
  return /** @type {Ticket} */ (t);
}

/**
 * Sign `result` with the run key and write it to `reviews/<run>/<ticket>.json`, then the done
 * marker. The result is written first, so a done marker always has its result.
 * @param {{repoRoot: string, runId: string, ticket: string, result: Record<string, any>, key: Buffer}} opts
 * @returns {Record<string, any>} the signed result
 */
export function writeResult({ repoRoot, runId, ticket, result, key }) {
  const signed = signRow({ ...result, run: runId, ticket: assertTicketId(ticket) }, key);
  const file = path.join(reviewsDir(repoRoot, runId), `${ticket}.json`);
  writeAtomic(file, `${JSON.stringify(signed)}\n`);
  writeAtomic(path.join(queueDir(repoRoot), `${ticket}.done`), `${JSON.stringify({ ticket, status: signed.status, result: path.relative(repoRoot, file) })}\n`);
  return signed;
}

/**
 * Read a result without verifying it (the coder's `--wait` cannot read the run key).
 * @param {string} repoRoot @param {string} runId @param {string} ticket
 * @returns {Record<string, any> | null}
 */
export function readResult(repoRoot, runId, ticket) {
  return readJSON(path.join(reviewsDir(repoRoot, runId), `${assertTicketId(ticket)}.json`));
}

/**
 * Verify a result's MAC with the run key (the orchestrator and the block gate do this; §8.6).
 * @param {string} repoRoot @param {string} runId @param {string} ticket
 * @returns {Promise<{ok: boolean, reason?: string, result?: Record<string, any>}>}
 */
export async function verifyResult(repoRoot, runId, ticket) {
  const file = path.join(reviewsDir(repoRoot, runId), `${assertTicketId(ticket)}.json`);
  if (!existsSync(file)) return { ok: false, reason: 'missing' };
  const result = readJSON(file);
  if (!result || typeof result !== 'object' || Array.isArray(result)) return { ok: false, reason: 'unparseable' };
  if (result.ticket !== ticket || result.run !== runId) return { ok: false, reason: 'mismatch' };
  const check = verifyRow(result, await loadKey(runId));
  return check.ok ? { ok: true, result } : { ok: false, reason: check.reason };
}

/**
 * Announce the serving worker in the queue directory (what `review-file` checks for liveness).
 * @param {string} repoRoot @param {{pid: number, run: string}} who
 */
export function announceWorker(repoRoot, { pid, run }) {
  const body = { pid, start_time: readStartTime(pid) ?? UNKNOWN_START_TIME, run, started_at: new Date().toISOString() };
  writeAtomic(path.join(queueDir(repoRoot), WORKER_FILE), `${JSON.stringify(body)}\n`);
  return body;
}

/** @param {string} repoRoot @returns {{pid: number, start_time: string, run: string} | null} */
export function readWorker(repoRoot) {
  const w = readJSON(path.join(queueDir(repoRoot), WORKER_FILE));
  return w && Number.isInteger(w.pid) && w.pid > 1 && typeof w.run === 'string' ? w : null;
}

/** Remove the announcement if it is still `pid`'s. @param {string} repoRoot @param {number} pid */
export function retractWorker(repoRoot, pid) {
  if (readWorker(repoRoot)?.pid === pid) rmSync(path.join(queueDir(repoRoot), WORKER_FILE), { force: true });
}

/**
 * The live worker serving this queue, or null (`worker_down`): the announced pid must be alive
 * AND still have the announced start time (a recycled pid is not our worker).
 * @param {string} repoRoot
 */
export function liveWorker(repoRoot) {
  const w = readWorker(repoRoot);
  if (!w || !isAlive(w.pid)) return null;
  if (w.start_time !== UNKNOWN_START_TIME && readStartTime(w.pid) !== w.start_time) return null;
  return w;
}

