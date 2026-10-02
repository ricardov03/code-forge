/**
 * The review queue on disk (plan §1.2, §4.8; block B11). Everything is a file inside the
 * workspace, so a Codex `workspace-write` coder with no network can enqueue and poll:
 *
 *   <repo>/.code-forge/queue/<ticket>.json   the ticket (written by `review-file`, idempotent)
 *   <repo>/.code-forge/queue/<ticket>.done   the worker's done marker `{ticket, status, result}`
 *   <repo>/.code-forge/queue/worker.json     the worker's announcement `{pid, start_time, run, started_at, heartbeat_at}`
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

/** How often a serving worker refreshes `heartbeat_at` (B30). */
export const HEARTBEAT_MS = 5000;

/** A heartbeat older than this is not fresh (twelve missed beats). */
export const HEARTBEAT_STALE_MS = 60_000;

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
  const now = new Date().toISOString();
  const body = { pid, start_time: readStartTime(pid) ?? UNKNOWN_START_TIME, run, started_at: now, heartbeat_at: now };
  writeAtomic(path.join(queueDir(repoRoot), WORKER_FILE), `${JSON.stringify(body)}\n`);
  return body;
}

/**
 * Refresh the announcement's `heartbeat_at` (B30) while it is still THIS worker's: the file is
 * re-read right before the write and must name the same `pid` AND `start_time` the worker
 * announced. Another worker's announcement is never overwritten (the caller stops beating).
 * The beat is written to a temp file, the announcement re-read and compared once more, and only
 * then renamed over it. The worker calls this from a timer of its own, so it keeps beating while
 * a long review session is awaited.
 * @param {string} repoRoot @param {{pid: number, start_time: string}} self - what `announceWorker` returned.
 * @param {Date} [now]
 * @returns {boolean} whether a beat was written.
 */
export function beatWorker(repoRoot, self, now = new Date()) {
  const mine = (/** @type {Record<string, any> | null} */ w) => w?.pid === self.pid && w.start_time === self.start_time;
  const w = readWorker(repoRoot);
  if (!mine(w)) return false;
  const file = path.join(queueDir(repoRoot), WORKER_FILE);
  const tmp = `${file}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
  try {
    writeFileSync(tmp, `${JSON.stringify({ ...w, heartbeat_at: now.toISOString() })}\n`, { mode: 0o644 });
    // re-read right before the rename: an announcement that changed meanwhile is never replaced
    if (!mine(readWorker(repoRoot))) {
      rmSync(tmp, { force: true });
      return false;
    }
    renameSync(tmp, file);
    return true;
  } catch (err) {
    rmSync(tmp, { force: true });
    throw err;
  }
}

/** A heartbeat further than this in the future is not fresh (clock skew tolerance). */
export const HEARTBEAT_FUTURE_MS = 5000;

/**
 * Whether the announcement's heartbeat is fresh: at most `HEARTBEAT_STALE_MS` old and at most
 * `HEARTBEAT_FUTURE_MS` ahead of our clock (a heartbeat far in the future is forged or skewed).
 * @param {{heartbeat_at?: unknown}} w @param {number} [nowMs]
 */
export function heartbeatFresh(w, nowMs = Date.now()) {
  const at = typeof w?.heartbeat_at === 'string' ? Date.parse(w.heartbeat_at) : NaN;
  if (!Number.isFinite(at)) return false;
  const age = nowMs - at;
  return age <= HEARTBEAT_STALE_MS && age >= -HEARTBEAT_FUTURE_MS;
}

/** @param {string} repoRoot @returns {{pid: number, start_time: string, run: string, heartbeat_at?: string} | null} */
export function readWorker(repoRoot) {
  const w = readJSON(path.join(queueDir(repoRoot), WORKER_FILE));
  return w && Number.isInteger(w.pid) && w.pid > 1 && typeof w.run === 'string' ? w : null;
}

/** Remove the announcement if it is still `pid`'s. @param {string} repoRoot @param {number} pid */
export function retractWorker(repoRoot, pid) {
  if (readWorker(repoRoot)?.pid === pid) rmSync(path.join(queueDir(repoRoot), WORKER_FILE), { force: true });
}

/**
 * The live worker serving this queue, or null (`worker_down`): the announced pid must be alive,
 * AND either its heartbeat is fresh (B30: the worker itself wrote it within `HEARTBEAT_STALE_MS`)
 * or it still has the announced start time (a recycled pid is not our worker). A fresh heartbeat
 * wins over the start-time check: `ps` can fail or print another time zone's `lstart` inside a
 * coder's sandbox, which made a live worker look down mid-review (issue #2).
 * Accepted trade-off: a worker that is SIGKILLed (no retraction) leaves a heartbeat that stays
 * fresh for up to `HEARTBEAT_STALE_MS` (60 s); if the OS reuses its pid inside that window, the
 * queue reads as served until the heartbeat goes stale. A clean stop retracts the file at once.
 * @param {string} repoRoot @param {{nowMs?: number}} [opts]
 */
export function liveWorker(repoRoot, opts = {}) {
  const w = readWorker(repoRoot);
  if (!w || !isAlive(w.pid)) return null;
  if (heartbeatFresh(w, opts.nowMs)) return w;
  if (w.start_time !== UNKNOWN_START_TIME && readStartTime(w.pid) !== w.start_time) return null;
  return w;
}

