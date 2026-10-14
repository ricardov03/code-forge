/**
 * The review queue on disk (plan §1.2, §4.8; block B11). Everything is a file inside the
 * workspace, so a Codex `workspace-write` coder with no network can enqueue and poll:
 *
 *   <repo>/.code-forge/queue/<ticket>.json   the ticket (written by `review-file`, idempotent)
 *   <repo>/.code-forge/queue/<ticket>.done   the worker's done marker `{ticket, status, result}`
 *   <repo>/.code-forge/queue/worker.json     the worker's announcement `{pid, start_time, run, started_at, heartbeat_at}`
 *   <repo>/.code-forge/workers/<pid>-<start_time>.json   each worker's private registry entry
 *   <repo>/.code-forge/reviews/<run>/<ticket>.json   the SIGNED result (run key HMAC, §8.6)
 *
 * The forbidden list (§8.4 ★) denies a coder any write under `reviews/` and to `queue/*.done`;
 * the MAC makes an edit visible anyway (T1/T2). Writes are atomic (temp file + rename).
 *
 * B62b: `<repo>` is always the MAIN tree's top level, where the worker runs — a block with its own
 * worktree still queues there; only the reviewed content is read in the block's tree.
 *
 * B71: an ANSWERS ticket (`kind: 'answers'`, from `review-file <answering file> --answers <ids>
 * --of <source>`) asks the worker whether `by_file` resolves the open findings `answers` of
 * `file` (the source). Its id adds `answers`, `by_file`, `by_hash` and the sorted ids to the
 * normal ticket's parts, so it never collides with a review ticket of the same source content.
 *
 * B75: `review-file --fresh` drops a done marker it classified in place as re-runnable through
 * `dropDoneIf` (an atomic claim by rename, then a byte compare), so two concurrent callers never
 * both drop it. A counted marker the read saw is never moved; only a newer marker the worker wrote
 * between that read and the claim is moved for a moment, then linked straight back (`changed`).
 * B83: a claim that could not be linked back leaves a refused `fresh-restore-failed` marker in its
 * place (or, when that write fails too, the claim itself, which `pendingTickets` treats as done),
 * so a failed restore never hands the worker a ticket to run again; `clearDone` clears both.
 */

import { createHash, randomBytes } from 'node:crypto';
import { chmodSync, existsSync, linkSync, mkdirSync, readdirSync, readFileSync, realpathSync, renameSync, rmSync, writeFileSync } from 'node:fs';
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
/** @param {string} repoRoot */
const workersDir = (repoRoot) => path.join(repoRoot, '.code-forge', 'workers');
/** @param {string} repoRoot @param {{pid: number, start_time: string}} self */
const workerPath = (repoRoot, self) => path.join(workersDir(repoRoot), `${self.pid}-${self.start_time}.json`);
/** @param {string} repoRoot @param {string} runId */
export const reviewsDir = (repoRoot, runId) => path.join(repoRoot, '.code-forge', 'reviews', runId);

/** @param {string} file @param {string} text @param {number} [mode] */
function writeAtomic(file, text, mode = 0o644) {
  mkdirSync(path.dirname(file), { recursive: true });
  const tmp = `${file}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
  try {
    writeFileSync(tmp, text, { mode });
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
 * @typedef {{ticket: string, run: string, block: string, file: string, content_hash: string, enqueued_at: string, tree?: string, kind?: 'answers', by_file?: string, by_hash?: string, answers?: string[]}} Ticket
 *   `tree` (B62b): the block's worktree the content was hashed in. It is part of the ticket id,
 *   and the worker only checks it against the tree it takes from the run record (a ticket whose
 *   tree is not that one is `unavailable`, `worktree-mismatch`); it never reads content there.
 */

/**
 * The review ticket for `file`'s CURRENT content: the one rule `enqueue` and `clearDone` (B72)
 * share — the repo-relative path, the realpath'd tree (or null), the content hash taken under the
 * tree when given (else under `repoRoot`) and the ticket id over all of them.
 * @param {{repoRoot: string, run: string, block: string, file: string, tree?: string | null}} opts
 * @returns {{rel: string, root: string | null, content_hash: string, ticket: string}}
 * @throws {WorkerError} `bad-path` for a bad file path or a tree that cannot be resolved.
 */
export function resolveTicket({ repoRoot, run, block, file, tree = null }) {
  const rel = assertRowPath(file);
  const root = tree === null ? null : realTree(tree);
  const content_hash = contentHash(root ?? repoRoot, rel);
  return { rel, root, content_hash, ticket: ticketId({ run, block, file: rel, content_hash, tree: root }) };
}

/**
 * Enqueue `file` (already repo-root-relative) for review. Idempotent by content hash: the same
 * content returns the same ticket and never rewrites it. The queue is under `repoRoot`; the
 * content is hashed under `tree` when given (B62b: the block's worktree, realpath'd here), else
 * under `repoRoot`. The tree is part of the ticket id, so a main-tree ticket and a tree ticket for
 * the same bytes differ, and the answer names the tree given here, never a stored value.
 * @param {{repoRoot: string, run: string, block: string, file: string, tree?: string | null, now?: Date}} opts
 * @returns {{ticket: string, status: 'queued' | 'done', file: string, content_hash: string, tree?: string}}
 * @throws {WorkerError} `bad-path` for a relative `tree` or one that cannot be resolved.
 */
export function enqueue({ repoRoot, run, block, file, tree = null, now = new Date() }) {
  const { content_hash } = resolveTicket({ repoRoot, run, block, file, tree });
  return enqueueHash({ repoRoot, run, block, file, tree, content_hash, now });
}

/** Queue proven git-object content through the same identity as a manual review.
 * @param {{repoRoot: string, run: string, block: string, file: string, content_hash: string, tree?: string | null, now?: Date}} opts
 * @returns {{ticket: string, status: 'queued' | 'done', file: string, content_hash: string, tree?: string}}
 */
export function enqueueHash({ repoRoot, run, block, file, content_hash, tree = null, now = new Date() }) {
  const rel = assertRowPath(file);
  const root = tree === null ? null : realTree(tree);
  const ticket = ticketId({ run, block, file: rel, content_hash, tree: root });
  const target = path.join(queueDir(repoRoot), `${ticket}.json`);
  const named = root === null ? {} : { tree: root };
  if (!existsSync(target)) {
    /** @type {Ticket} */
    const body = { ticket, run, block, file: rel, content_hash, enqueued_at: now.toISOString(), ...named };
    // Publish a complete immutable ticket without replacing a concurrent coder's ticket.
    mkdirSync(path.dirname(target), { recursive: true });
    const tmp = `${target}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
    try {
      writeFileSync(tmp, `${JSON.stringify(body)}\n`, { mode: 0o644 });
      try { linkSync(tmp, target); } catch (err) { if (err.code !== 'EEXIST') throw err; }
    } finally { rmSync(tmp, { force: true }); }
  }
  // Returning content is current again. Claim only a superseded marker's exact bytes;
  // a real result that replaced it wins the comparison and is restored unchanged.
  let seen;
  let done;
  try {
    seen = readFileSync(path.join(queueDir(repoRoot), `${ticket}.done`));
    done = JSON.parse(seen.toString('utf8'));
  } catch { /* missing or unreadable markers remain untouched */ }
  if (seen && done?.status === 'refused' && done.reason === 'superseded' && !done.result) dropDoneIf(repoRoot, ticket, seen);
  return { ticket, status: isDone(repoRoot, ticket) ? 'done' : 'queued', file: rel, content_hash, ...named };
}

/** Finish only an unstarted, pending review. A superseded marker has no review result.
 * @param {string} repoRoot @param {string} ticket
 * @param {{write?: (file: string, text: string) => void, link?: (from: string, to: string) => void}} [io] - publication seams for tests.
 */
export function supersedeTicket(repoRoot, ticket, io = {}) {
  const marker = path.join(queueDir(repoRoot), `${assertTicketId(ticket)}.done`);
  const tmp = `${marker}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
  try {
    (io.write ?? ((file, text) => writeFileSync(file, text, { mode: 0o644 })))(tmp, `${JSON.stringify({ ticket, status: 'refused', reason: 'superseded' })}\n`);
    try { (io.link ?? linkSync)(tmp, marker); } catch (err) { if (err.code !== 'EEXIST') throw err; }
  } finally { rmSync(tmp, { force: true }); }
}

/** B71: the most finding ids one answers ticket may name. */
export const MAX_ANSWERS = 10;

/** B71: the finding-id shape (the one `block waive` and the review results use). */
const ANSWER_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;

/**
 * B71: the id of an answers ticket — the normal ticket's parts (run, block, source, source hash,
 * tree) plus `answers`, the answering file, its hash and the sorted ids, so it differs from the
 * normal ticket of the same source content and from an answer of other ids or another file.
 * @param {{run: string, block: string, file: string, content_hash: string, by_file: string, by_hash: string, answers: string[], tree?: string | null}} t
 * @returns {string}
 */
export function answersTicketId({ run, block, file, content_hash, by_file, by_hash, answers, tree = null }) {
  const parts = ['answers', run, block, file, content_hash, by_file, by_hash, [...answers].sort().join(','), ...(tree === null ? [] : [tree])];
  return createHash('sha256').update(parts.join('\0')).digest('hex').slice(0, 24);
}

/**
 * B71: refuse an id list that is not 1..MAX_ANSWERS finding ids; returns them sorted and unique.
 * @param {unknown} answers @returns {string[]}
 * @throws {WorkerError} `usage`
 */
export function assertAnswers(answers) {
  if (!Array.isArray(answers) || answers.length === 0) throw new WorkerError('usage', '--answers needs at least one finding id');
  if (answers.some((a) => typeof a !== 'string' || !ANSWER_ID.test(a))) throw new WorkerError('usage', 'a finding id is letters, digits, . _ : - (at most 64)');
  const ids = [...new Set(/** @type {string[]} */ (answers))].sort();
  if (ids.length > MAX_ANSWERS) throw new WorkerError('usage', `--answers takes at most ${MAX_ANSWERS} finding ids`);
  return ids;
}

/**
 * @typedef {Ticket & {kind: 'answers', by_file: string, by_hash: string, answers: string[]}} AnswersTicket
 */

/**
 * B71: enqueue an answers ticket: `byFile` answers the findings `answers` of `file` (both
 * repo-root-relative). Both files are hashed now (under `tree` when given); idempotent like
 * {@link enqueue}. The caller has already refused the same file and the id list.
 * @param {{repoRoot: string, run: string, block: string, file: string, byFile: string, answers: string[], tree?: string | null, now?: Date}} opts
 * @returns {{ticket: string, status: 'queued' | 'done', file: string, content_hash: string, by_file: string, by_hash: string, answers: string[], tree?: string}}
 * @throws {WorkerError} `bad-path`, `usage`
 */
export function enqueueAnswers({ repoRoot, run, block, file, byFile, answers, tree = null, now = new Date() }) {
  const rel = assertRowPath(file);
  const by_file = assertRowPath(byFile);
  const ids = assertAnswers(answers);
  const root = tree === null ? null : realTree(tree);
  const content_hash = contentHash(root ?? repoRoot, rel);
  const by_hash = contentHash(root ?? repoRoot, by_file);
  const ticket = answersTicketId({ run, block, file: rel, content_hash, by_file, by_hash, answers: ids, tree: root });
  const target = path.join(queueDir(repoRoot), `${ticket}.json`);
  const named = root === null ? {} : { tree: root };
  if (!existsSync(target)) {
    /** @type {AnswersTicket} */
    const body = { ticket, kind: 'answers', run, block, file: rel, content_hash, by_file, by_hash, answers: ids, enqueued_at: now.toISOString(), ...named };
    writeAtomic(target, `${JSON.stringify(body)}\n`);
  }
  return { ticket, status: isDone(repoRoot, ticket) ? 'done' : 'queued', file: rel, content_hash, by_file, by_hash, answers: ids, ...named };
}

/**
 * @param {string} tree @returns {string} `tree` realpath'd.
 * @throws {WorkerError} `bad-path` when it is relative or cannot be resolved.
 */
function realTree(tree) {
  if (!path.isAbsolute(tree)) throw new WorkerError('bad-path', `the content tree ${tree} is not an absolute path`);
  try {
    return realpathSync(tree);
  } catch {
    throw new WorkerError('bad-path', `the content tree ${tree} cannot be resolved`);
  }
}

/** @param {string} repoRoot @param {string} ticket */
export function isDone(repoRoot, ticket) {
  return existsSync(path.join(queueDir(repoRoot), `${assertTicketId(ticket)}.done`));
}

/** B83: a `dropDoneIf` claim of a done marker (`<ticket>.done.<pid>.<hex>.fresh`). */
const CLAIM = /^([0-9a-f]{24})\.done\.\d+\.[0-9a-f]{8}\.fresh$/;

/**
 * B83: an fs error's code when it is a plain code word, else `unknown` (a message names the code
 * only, never a path). @param {unknown} err @returns {string}
 */
function codeOf(err) {
  const code = /** @type {{code?: unknown}} */ (err)?.code;
  return typeof code === 'string' && /^[A-Za-z][A-Za-z0-9_-]{0,31}$/.test(code) ? code : 'unknown';
}

/**
 * B72 (`block reopen`, C11): drop the done marker of the review ticket for the file's CURRENT
 * content, so the next `review-file` on it runs instead of returning the cached result. The ticket
 * id comes from `resolveTicket`, the rule `enqueue` uses; no ticket is written. Synchronous.
 * B83: it also removes the ticket's leftover `dropDoneIf` claims (`<ticket>.done.*.fresh`), which
 * would otherwise keep the ticket off the worker for good; `removed` still names the marker only.
 * @param {{repoRoot: string, run: string, block: string, file: string, tree?: string | null}} opts
 * @returns {{ticket: string, content_hash: string, removed: boolean}}
 * @throws {WorkerError} `bad-path` for a bad file path or a tree that cannot be resolved.
 */
export function clearDone({ repoRoot, run, block, file, tree = null }) {
  const { content_hash, ticket } = resolveTicket({ repoRoot, run, block, file, tree });
  const marker = path.join(queueDir(repoRoot), `${ticket}.done`);
  const removed = existsSync(marker);
  rmSync(marker, { force: true });
  let names;
  try {
    names = readdirSync(queueDir(repoRoot));
  } catch {
    names = [];
  }
  // accepted race: a claim a concurrent dropDoneIf still uses may go here, which can leave a refused
  // fresh-restore-failed marker (rare, fail closed; a second `block reopen` clears it)
  for (const name of names) if (CLAIM.exec(name)?.[1] === ticket) rmSync(path.join(queueDir(repoRoot), name), { force: true });
  return { ticket, content_hash, removed };
}

/**
 * B75 (`review-file --fresh`): drop the done marker of `ticket` only while it still holds `seen`,
 * the bytes the caller already read IN PLACE and classified as re-runnable (a marker classified
 * as counted, tampered or unknown is never passed here, so it is never moved). The queue has no
 * lock file; the order is the guarantee:
 *   1. the caller reads the marker in place and classifies it (a refusal touches nothing);
 *   2. only a re-runnable marker is claimed by an atomic rename to a name of this process — of
 *      two concurrent callers only one takes it, the other gets `gone`;
 *   3. the claim's bytes are compared with `seen`: equal, the claim is removed (`dropped`) and the
 *      caller enqueues; different (a newer result arrived), the marker is restored with
 *      `linkSync` only — on EEXIST a newer marker is there and wins, the claim is removed
 *      (`changed`); on any other link error `fresh-restore-failed` is thrown (below).
 * B83, fail closed — no failure leaves a ticket the worker runs again by itself:
 *   - a link-back that fails with other than EEXIST writes a refused marker in place,
 *     `{ticket, status: 'refused', reason: 'fresh-restore-failed'}` (no-overwrite open `wx`, mode
 *     0o600; it names no result, so `--fresh` counts it and refuses, and `block reopen` clears it).
 *     EEXIST on that write: a newer marker is in place. Any other write error keeps the claim; a
 *     claim (`<ticket>.done.<pid>.<hex>.fresh`) counts as done for `pendingTickets`, and
 *     `clearDone` (`block reopen`) removes it;
 *   - a claim that cannot be removed after a `dropped` compare is `fresh-claim-failed` (it would
 *     hold the re-queued ticket off the worker; `block reopen` clears it); after a good link-back
 *     the marker is back, so a claim that cannot be removed is left for `clearDone`.
 * A crash between the drop and the caller's enqueue leaves a re-runnable ticket with no marker:
 * harmless — its ticket file is pending, and the next `review-file` queues it anyway. Synchronous.
 * @param {string} repoRoot @param {string} ticket @param {Buffer} seen
 * @param {{link?: (from: string, to: string) => void, read?: (file: string) => Buffer, rename?: (from: string, to: string) => void, remove?: (file: string) => void, write?: (file: string, text: string) => void}} [io] - for
 *   tests (default `linkSync`, `readFileSync`, `renameSync`, `rmSync` with force, `writeFileSync`
 *   with flag `wx` and mode 0o600). A claim that cannot be read is restored the same way and
 *   refused `fresh-restore-failed`.
 * @returns {'dropped' | 'gone' | 'changed'}
 * @throws {WorkerError} `fresh-claim-failed` (the claim could not be taken, or not removed after
 *   the drop; nothing was queued) or `fresh-restore-failed` (the message says which state was left:
 *   put back, a newer marker, a refused marker, the claim kept, or nothing). A message names an
 *   error code, never a path.
 */
export function dropDoneIf(repoRoot, ticket, seen, io = {}) {
  const marker = path.join(queueDir(repoRoot), `${assertTicketId(ticket)}.done`);
  const claim = `${marker}.${process.pid}.${randomBytes(4).toString('hex')}.fresh`;
  const remove = io.remove ?? ((/** @type {string} */ file) => rmSync(file, { force: true }));
  try {
    (io.rename ?? renameSync)(marker, claim);
  } catch (err) {
    if (/** @type {{code?: unknown}} */ (err)?.code === 'ENOENT') return 'gone';
    throw new WorkerError('fresh-claim-failed', `the done marker of ticket ${ticket} could not be claimed (${codeOf(err)}); nothing was queued`);
  }
  /** @type {string | null} */
  let readFailed = null;
  /** @type {boolean} */
  let same = false;
  try {
    same = (io.read ?? readFileSync)(claim).equals(seen);
  } catch (err) {
    readFailed = codeOf(err);
  }
  if (same) {
    try {
      remove(claim);
    } catch (err) {
      throw new WorkerError('fresh-claim-failed', `the done marker of ticket ${ticket} was dropped but its claim could not be removed (${codeOf(err)}); block reopen clears it; nothing was queued`);
    }
    return 'dropped';
  }
  /** @type {string | null} */
  let linkFailed = null;
  try {
    (io.link ?? linkSync)(claim, marker);
  } catch (err) {
    linkFailed = codeOf(err);
  }
  const unread = `the claimed done marker of ticket ${ticket} could not be read (${readFailed})`;
  if (linkFailed !== null && linkFailed !== 'EEXIST') {
    const cause = readFailed !== null ? `${unread} and could not be restored (${linkFailed})` : `the done marker of ticket ${ticket} changed and could not be restored (${linkFailed})`;
    throw new WorkerError('fresh-restore-failed', `${cause}; ${refuseInPlace(marker, claim, ticket, io, remove)}; nothing was queued`);
  }
  try {
    remove(claim);
  } catch {
    // the marker is back (or a newer one won), so the ticket is done; `clearDone` removes the claim
  }
  if (readFailed !== null) throw new WorkerError('fresh-restore-failed', `${unread}; ${linkFailed === 'EEXIST' ? 'a newer marker is in place' : 'it was put back'}; nothing was queued`);
  return 'changed';
}

/**
 * B83: after a failed link-back, put a refused `fresh-restore-failed` marker where the done marker
 * was (never over one), so the worker does not run the ticket; the claim is then removed when it
 * can be (`clearDone` removes a leftover one). When the write fails the claim is kept instead.
 * @param {string} marker @param {string} claim @param {string} ticket
 * @param {{write?: (file: string, text: string) => void}} io @param {(file: string) => void} remove
 * @returns {string} which state was left, for the message (codes only, no path).
 */
function refuseInPlace(marker, claim, ticket, io, remove) {
  const text = `${JSON.stringify({ ticket, status: 'refused', reason: 'fresh-restore-failed' })}\n`;
  /** @type {string | null} */
  let writeFailed = null;
  try {
    (io.write ?? ((/** @type {string} */ file, /** @type {string} */ body) => writeFileSync(file, body, { flag: 'wx', mode: 0o600 })))(marker, text);
  } catch (err) {
    writeFailed = codeOf(err);
  }
  if (writeFailed === null || writeFailed === 'EEXIST') {
    try {
      remove(claim);
    } catch {
      // the marker holds the ticket off the worker; `clearDone` removes the leftover claim
    }
    return writeFailed === null ? 'a refused fresh-restore-failed marker was written in its place, so the worker does not run the ticket' : 'a newer marker is in place';
  }
  // the claim holds the ticket off the worker (`pendingTickets`); a claim that is gone cannot
  return existsSync(claim)
    ? `the refused marker could not be written (${writeFailed}); its claim was kept, so the worker does not run the ticket; block reopen clears it`
    : `the refused marker could not be written (${writeFailed}) and no claim exists`;
}

/**
 * Tickets with no done marker, oldest first (enqueue time, then id). Unparseable files are
 * skipped here and refused by the worker when it reads them. B83: a ticket whose marker is held
 * by a `dropDoneIf` claim is not pending either (a claim kept after a failed restore fails closed).
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
  const claimed = new Set(names.map((n) => CLAIM.exec(n)?.[1]));
  const ids = names.filter((n) => /^[0-9a-f]{24}\.json$/.test(n)).map((n) => n.slice(0, 24));
  const pending = ids.filter((id) => !names.includes(`${id}.done`) && !claimed.has(id));
  const at = (/** @type {string} */ id) => {
    const t = readJSON(path.join(queueDir(repoRoot), `${id}.json`));
    return typeof t?.enqueued_at === 'string' ? t.enqueued_at : '';
  };
  return pending.map((id) => ({ id, at: at(id) })).sort((a, b) => (a.at === b.at ? (a.id < b.id ? -1 : 1) : a.at < b.at ? -1 : 1)).map((e) => e.id);
}

/**
 * Valid review tickets for one block and content tree, oldest first. Answers tickets cannot
 * stand in for a file's own review. Unreadable tickets are refused, never silently omitted.
 * @param {{repoRoot: string, run: string, block: string, tree?: string | null}} opts
 * @returns {Ticket[]}
 */
export function blockTickets({ repoRoot, run, block, tree = null }) {
  let names;
  try {
    names = readdirSync(queueDir(repoRoot));
  } catch (err) {
    if (err.code === 'ENOENT') return [];
    throw new WorkerError('queue-unreadable', 'the review queue cannot be read');
  }
  const tickets = names.filter((n) => /^[0-9a-f]{24}\.json$/.test(n)).map((n) => readTicket(repoRoot, n.slice(0, 24)));
  return tickets.filter((t) => !t.kind && t.run === run && t.block === block && (t.tree ?? null) === tree)
    .sort((a, b) => a.enqueued_at.localeCompare(b.enqueued_at) || a.ticket.localeCompare(b.ticket));
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
  if (t.tree !== undefined && (typeof t.tree !== 'string' || !path.isAbsolute(t.tree))) throw new WorkerError('bad-ticket', `ticket ${id} names a tree that is not an absolute path`);
  if (typeof t.run !== 'string' || typeof t.block !== 'string' || typeof t.content_hash !== 'string') {
    throw new WorkerError('bad-ticket', `ticket ${id} does not match its id`);
  }
  if (t.kind !== undefined) {
    // B71: the only other kind is an answers ticket; its id covers the answering file and the ids
    if (t.kind !== 'answers') throw new WorkerError('bad-ticket', `ticket ${id} has an unknown kind`);
    assertRowPath(t.by_file);
    let ids;
    try {
      ids = assertAnswers(t.answers);
    } catch {
      throw new WorkerError('bad-ticket', `ticket ${id} names no valid finding ids`);
    }
    if (typeof t.by_hash !== 'string' || ids.join(',') !== t.answers.join(',') || answersTicketId(t) !== id) throw new WorkerError('bad-ticket', `ticket ${id} does not match its id`);
    return /** @type {Ticket} */ (t);
  }
  if (ticketId(t) !== id) throw new WorkerError('bad-ticket', `ticket ${id} does not match its id`);
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
 * Announce the serving worker in the private registry and legacy queue file.
 * @param {string} repoRoot @param {{pid: number, run: string}} who
 */
export function announceWorker(repoRoot, { pid, run }) {
  const now = new Date().toISOString();
  const body = { pid, start_time: readStartTime(pid) ?? UNKNOWN_START_TIME, run, started_at: now, heartbeat_at: now };
  mkdirSync(workersDir(repoRoot), { recursive: true, mode: 0o700 });
  chmodSync(workersDir(repoRoot), 0o700);
  writeAtomic(workerPath(repoRoot, body), `${JSON.stringify(body)}\n`, 0o600);
  const legacy = readWorker(repoRoot);
  if (!legacy || !workerLive(legacy)) writeAtomic(path.join(queueDir(repoRoot), WORKER_FILE), `${JSON.stringify(body)}\n`);
  return body;
}

/**
 * Refresh both announcements' `heartbeat_at` (B30) while each is still THIS worker's: each file is
 * re-read right before the write and must name the same `pid` AND `start_time` the worker
 * announced. Another worker's announcement is never overwritten. A worker keeps beating its
 * registry entry even when another worker owns worker.json.
 * The beat is written to a temp file, the announcement re-read and compared once more, and only
 * then renamed over it. The worker calls this from a timer of its own, so it keeps beating while
 * a long review session is awaited.
 * @param {string} repoRoot @param {{pid: number, start_time: string}} self - what `announceWorker` returned.
 * @param {Date} [now]
 * @returns {boolean} whether a beat was written.
 */
export function beatWorker(repoRoot, self, now = new Date()) {
  const mine = (/** @type {Record<string, any> | null} */ w) => w?.pid === self.pid && w.start_time === self.start_time;
  let written = false;
  const registryFile = workerPath(repoRoot, self);
  for (const file of [registryFile, path.join(queueDir(repoRoot), WORKER_FILE)]) {
    const w = readJSON(file);
    if (!mine(w)) continue;
    const tmp = `${file}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
    try {
      writeFileSync(tmp, `${JSON.stringify({ ...w, heartbeat_at: now.toISOString() })}\n`, { mode: file === registryFile ? 0o600 : 0o644 });
      // Re-read before rename; another owner's announcement is never replaced.
      if (!mine(readJSON(file))) continue;
      renameSync(tmp, file);
      written = true;
    } finally {
      rmSync(tmp, { force: true });
    }
  }
  return written;
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

/** Remove only this owner's announcements. @param {string} repoRoot @param {number} pid @param {string} [startTime] */
export function retractWorker(repoRoot, pid, startTime) {
  const mine = (w) => w?.pid === pid && (startTime === undefined || w.start_time === startTime);
  for (const entry of workerRegistry(repoRoot)) {
    if (mine(entry.worker)) rmSync(entry.file, { force: true });
  }
  if (mine(readWorker(repoRoot))) rmSync(path.join(queueDir(repoRoot), WORKER_FILE), { force: true });
}

/** @typedef {{pid: number, start_time: string, run: string, started_at: string, heartbeat_at: string}} WorkerAnnouncement */

/** Bad entries remain occupied until a human fixes them. @param {any} w @returns {w is WorkerAnnouncement} */
function workerShape(w) {
  return w && Number.isInteger(w.pid) && w.pid > 1 && typeof w.start_time === 'string' && w.start_time.length > 0
    && !/[\\/]/.test(w.start_time) && typeof w.run === 'string' && w.run.length > 0
    && typeof w.started_at === 'string' && Number.isFinite(Date.parse(w.started_at))
    && typeof w.heartbeat_at === 'string' && Number.isFinite(Date.parse(w.heartbeat_at));
}

/** @param {{pid: number, start_time: string, heartbeat_at?: string}} w @param {number} [nowMs] */
function workerLive(w, nowMs) {
  if (!isAlive(w.pid)) return false;
  if (heartbeatFresh(w, nowMs)) return true;
  return w.start_time === UNKNOWN_START_TIME || readStartTime(w.pid) === w.start_time;
}

/** @param {{pid: number, start_time: string, heartbeat_at?: string}} w @param {number} [nowMs] */
function workerLiveAtBoot(w, nowMs) {
  if (!isAlive(w.pid)) return false;
  const startTime = readStartTime(w.pid);
  // Boot trusts orchestrator ps; sandbox readers let fresh heartbeats mask unreliable ps.
  if (startTime !== null && w.start_time !== UNKNOWN_START_TIME) return startTime === w.start_time;
  return heartbeatFresh(w, nowMs) || w.start_time === UNKNOWN_START_TIME;
}

/** Read all entries, keeping unreadable/bad entries occupied. Only proven stale owners may be pruned.
 * @param {string} repoRoot @param {{prune?: boolean, nowMs?: number}} [opts]
 * @returns {Array<{file: string, worker: WorkerAnnouncement | null, live: boolean}>}
 */
export function workerRegistry(repoRoot, opts = {}) {
  let names;
  try { names = readdirSync(workersDir(repoRoot)); }
  catch (err) { if (err.code === 'ENOENT') return []; throw err; }
  const entries = [];
  for (const name of names.sort().filter((n) => n.endsWith('.json'))) {
    const file = path.join(workersDir(repoRoot), name);
    const w = readJSON(file);
    const worker = workerShape(w) && name === `${w.pid}-${w.start_time}.json` ? w : null;
    const live = worker === null || (opts.prune ? workerLiveAtBoot(worker, opts.nowMs) : workerLive(worker, opts.nowMs));
    if (!live && opts.prune) rmSync(file, { force: true });
    else entries.push({ file, worker, live });
  }
  return entries;
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
 * Registry entries take precedence; worker.json supports workers from older versions.
 * @param {string} repoRoot @param {{nowMs?: number, pid?: number, registeredOnly?: boolean}} [opts]
 */
export function liveWorker(repoRoot, opts = {}) {
  const entry = workerRegistry(repoRoot, opts).find((e) => e.live && e.worker && (opts.pid === undefined || e.worker.pid === opts.pid));
  if (entry) return entry.worker;
  if (opts.registeredOnly) return null;
  const w = readWorker(repoRoot);
  return w && (opts.pid === undefined || w.pid === opts.pid) && workerLive(w, opts.nowMs) ? w : null;
}

