/**
 * `code-forge review-file` (plan §4.1, §4.8, V4; block B11) — the coder's side of async review.
 *
 *   review-file <path> --block <id> [--run <id>]   enqueue; prints {ticket, status: queued|done, file}
 *   review-file --wait <ticket> [--max <90s>]      poll; prints {ticket, status: done, result} or
 *                                                  {ticket, status: pending, reason: wait_timeout}
 *                                                  when --max elapses (the review is still running)
 *   review-file --wait-block <id> --run <id>       join owned changed files' current tickets;
 *                                                  prints {block, files, done}; never reviews here
 *   review-file <answering file> --block <id> --answers <ids> --of <source> [--run <id>]
 *                                                  B71: enqueue an answers ticket; prints {ticket,
 *                                                  status, kind: answers, file: source, by_file, answers}
 *   review-file <path> --block <id> --fresh        B75: re-run a cached result that never counted
 *
 * The same content enqueued again after an `unavailable` or round-1 `split_required` result is a
 * fresh attempt (`retry: true`), not the old result.
 * A done result that is not approved and carries open findings adds `fix_list` (what the coder
 * fixes before re-running `review-file <path>`, which is then a round-2 recheck of the fix hunks,
 * §4.11); a result the loop stopped adds `stop` with the reason (`review_cap`, …) and (B73)
 * `exits: ["block reopen", "block waive", "review-file --answers", "re-decompose"]` — the coder
 * stops and reports; only the orchestrator and the human act on it. A `split_required` result
 * (the packet is over `review.budgets.full_in`) adds `stop: split_required` with `tokens_in` and
 * `budget` (always; null when unknown) and a Markdown `section` — never for another result (B54); a Markdown file gets there only when one of its sections alone is too large.
 *
 * Everything is a file read/write inside the workspace (a sandboxed coder has no network). The
 * path is given relative to the current directory and stored relative to the REPOSITORY ROOT
 * (V4); an absolute path or a `..` segment is refused with `bad-path`. With no live worker the
 * answer is `worker_down` — never approval — and nothing is enqueued; the coder must then print
 * `===BLOCK <id> FAILED: worker down===`. While waiting, `worker_down` needs the worker to look
 * down on two polls in a row, and a worker whose pid is alive with a fresh heartbeat is never down
 * (`liveWorker`, B30). Output is one JSON line on stdout.
 * B75: `--fresh` drops the cached done marker of the file's current content and queues the ticket
 * again ONLY when that result never counted as a review: `stale`, `unavailable`, round-1
 * `split_required`, or `refused` with `other-run`, `context-unreadable` or a `worktree-*` reason
 * (an allow-list, fail closed). Any
 * other cached result — a round ran (findings, approved, stopped), `review-state-lost`, an answers
 * result, an unknown status or reason, an unreadable marker — is refused `fresh-needs-reopen`
 * (exit 1, `exits: [block reopen, block waive]`, the marker kept byte-for-byte); a
 * `review-state-tampered` one is refused `fresh-tampered` (names only `block close
 * --no-require-reviews`). No cached result: a normal enqueue. A coder may run it; it never
 * re-rolls a counted round and writes no ledger row.
 * Exit codes: 0 queued/done/pending, 1 bad-path or another refusal, 2 usage, 3 worker_down.
 *
 * B62b: the queue and the worker are always the MAIN tree's (`mainRootOf`), also when the coder
 * runs `review-file` inside a block's worktree. For a block the run record gives a worktree
 * (`proof.isolation: worktree`), the content is hashed in that tree — never in the main tree — and
 * the ticket and the answer carry `tree: <tree top level>`; the worker reviews the same tree. The
 * path is still given relative to the current directory, inside the tree it is called from, and is
 * re-checked in the tree the content is read from. A block the record gives no worktree is hashed
 * in the main tree, wherever `review-file` is called. When the run record does not exist here or
 * cannot be opened (a sandboxed coder), the content is hashed in the tree `review-file` is called
 * from, which is the block's tree when the coder works there. A record naming a worktree that is
 * gone or malformed is refused (exit 1, `reason: worktree-missing|worktree-malformed|…`), and
 * nothing is queued.
 *
 * B71: `--answers <id,id,…> --of <source>` (1 to 10 finding ids, comma-separated) asks the worker
 * whether the change in the positional file (the answering file) resolves those open findings of
 * `<source>`. It is refused before anything is queued — exit 2 `usage` for a missing `--of`, no
 * ids, more than 10 ids or a malformed id; exit 1 `answers-same-file` when both name one file,
 * `answers-not-owned` when the run record says the block does not own both, `answers-not-open`
 * when an id is not open on the source at its current hash (not named by the latest refusal or
 * cap row there, or already waived or answered; these two pre-checks read what this process can read, unverified, and are skipped when the run
 * record cannot be opened here). The worker checks all of it again with the signed rows; that check
 * is the one that counts. Its done result has `status: answers` and the coder-facing `answers:
 * [{id, verdict: resolved|not_resolved, reason}]` instead of a fix list; a resolved id is a signed
 * `review.answered` row the close counts until either file changes. The answering file still
 * needs its own review.
 */

import { readFileSync, realpathSync, rmSync } from 'node:fs';
import path from 'node:path';
import { answersTicketId, assertAnswers, blockTickets, deliverBlockFindings, dropDoneIf, enqueue, enqueueAnswers, isDone, liveWorker, MAX_ANSWERS, queueDir, resolveTicket } from '../worker/queue.mjs';
import { assertBlockId, assertInsideRoot, assertTicketId, contentHash, mainRootOf, normalizeRequestPath, repoRootOf, WorkerError } from '../worker/ticket.mjs';
import { blockFileSet, namedOnHash } from '../review/gate-check.mjs';
import { STOP_EXITS } from '../review/stop-exits.mjs';
import { ownsFile } from '../state/registry.mjs';
import { readAllRows } from '../ledger/write.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { assertRunId } from '../state/paths.mjs';
import { readRun } from '../state/run.mjs';
import { blockTree } from '../proof/worktree.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE =
  'usage: code-forge review-file <path> --block <id> [--run <id>] [--fresh] | review-file --wait <ticket> [--max <seconds>s]\n' +
  '       code-forge review-file --wait-block <id> --run <id> [--max <seconds>s]\n' +
  '       code-forge review-file <answering file> --block <id> --answers <id,id,…> --of <source> [--run <id>]\n';

export const DEFAULT_WAIT_MS = 90_000;
const POLL_MS = 200;

/**
 * `90s`, `90`, `1500ms` → milliseconds.
 * @param {unknown} raw @returns {number}
 */
export function parseMax(raw) {
  const m = typeof raw === 'string' ? /^(\d{1,7})(ms|s)?$/.exec(raw) : null;
  if (!m) throw new WorkerError('usage', '--max must look like 90s or 1500ms');
  const n = Number(m[1]);
  return m[2] === 'ms' ? n : n * 1000;
}

/**
 * @param {string} repoRoot @param {string} ticket
 * @returns {Record<string, any> | null} the worker's result named by the done marker.
 */
function doneResult(repoRoot, ticket) {
  try {
    return resultOfMarker(repoRoot, readFileSync(path.join(queueDir(repoRoot), `${ticket}.done`), 'utf8'));
  } catch {
    return null;
  }
}

/**
 * @param {string} repoRoot @param {string} text - a done marker's text
 * @returns {Record<string, any> | null} the worker's result it names (unverified), or null.
 */
function resultOfMarker(repoRoot, text) {
  try {
    const marker = JSON.parse(text);
    const rel = typeof marker?.result === 'string' ? marker.result : '';
    if (!rel.startsWith(`.code-forge${path.sep}reviews${path.sep}`) || rel.split(path.sep).includes('..')) return null;
    const result = JSON.parse(readFileSync(path.join(repoRoot, rel), 'utf8'));
    return result !== null && typeof result === 'object' && !Array.isArray(result) ? result : null;
  } catch {
    return null;
  }
}

/** B75: the refused reasons that are not a verdict on the content (`--fresh` re-runs them). */
const FRESH_REFUSED = new Set(['other-run', 'context-unreadable']);

/**
 * B75: may `--fresh` drop this cached result? An allow-list, fail closed: `rerun` only for a
 * result that never counted as a review — `stale`, `unavailable`, round-1 `split_required`, or
 * `refused` with a reason that is not a verdict (`other-run`, `context-unreadable`, `worktree-*`)
 * — whose marker names the same
 * status. `tampered` for `refused review-state-tampered`; `counted` for everything else (a round
 * ran, `review-state-lost`, an answers result, an unknown status or reason, an unreadable marker or
 * result).
 * @param {string} text - the done marker's text @param {Record<string, any> | null} result
 * @returns {'rerun' | 'tampered' | 'counted'}
 */
export function freshVerdict(text, result) {
  let marker;
  try {
    marker = JSON.parse(text);
  } catch {
    return 'counted';
  }
  if (result === null || marker === null || typeof marker !== 'object' || marker.status !== result.status) return 'counted';
  if (result.status === 'stale' || result.status === 'unavailable' || result.status === 'split_required') return 'rerun';
  if (result.status !== 'refused' || typeof result.reason !== 'string') return 'counted';
  if (result.reason === 'review-state-tampered') return 'tampered';
  return FRESH_REFUSED.has(result.reason) || result.reason.startsWith('worktree-') ? 'rerun' : 'counted';
}

/** B75: how many times --fresh classifies a marker that changed under its claim. */
const FRESH_ATTEMPTS = 3;

/** B75: the ways out a counted result names (human-only; a coder stops and reports). */
export const FRESH_EXITS = Object.freeze(['block reopen', 'block waive']);

/** The automatic retry allow-list excludes refused results, even those --fresh can rerun. */
const RETRY_STATUSES = new Set(['stale', 'unavailable', 'split_required']);

/**
 * Classify in place and use the same byte-checked claim for fresh and automatic retries.
 * @param {{repoRoot: string, ticket: string, file: string, dropDone?: typeof dropDoneIf, once?: boolean, statuses?: Set<string>}} opts
 * @returns {{verdict: 'rerun' | 'tampered' | 'counted', outcome: 'dropped' | 'gone' | 'kept'}}
 */
function dropFreshDone({ repoRoot, ticket, file, dropDone = dropDoneIf, once = false, statuses }) {
  const markerPath = path.join(queueDir(repoRoot), `${ticket}.done`);
  /** @type {'rerun' | 'tampered' | 'counted'} */
  let verdict = 'counted';
  // a marker that changed under the claim is classified again; bounded, then refused (fail closed)
  for (let attempt = 0; attempt < FRESH_ATTEMPTS; attempt += 1) {
    /** @type {Buffer | null} */
    let bytes;
    try {
      bytes = readFileSync(markerPath);
    } catch (err) {
      // no marker: a normal enqueue; a marker that exists but cannot be read is never dropped
      bytes = /** @type {{code?: unknown}} */ (err)?.code === 'ENOENT' ? null : Buffer.alloc(0);
    }
    if (bytes === null) return { verdict: 'counted', outcome: 'gone' };
    // classified IN PLACE: a refusal never moves, renames or touches the marker
    const text = bytes.toString('utf8');
    const result = resultOfMarker(repoRoot, text);
    verdict = freshVerdict(text, result);
    if (statuses && !statuses.has(result?.status)) verdict = 'counted';
    if (verdict !== 'rerun') break;
    /** @type {unknown} */
    let outcome;
    try {
      outcome = dropDone(repoRoot, ticket, bytes, once ? { once: true } : {});
    } catch (err) {
      // a named refusal passes through; any other throw is named here, never echoed
      if (err instanceof WorkerError) throw err;
      throw new WorkerError('fresh-claim-failed', `the claim of the cached result of ${file} failed; nothing was queued`);
    }
    if (outcome === 'changed') {
      verdict = 'counted';
      continue;
    }
    if (outcome === 'kept') break;
    // an allow-list of outcomes: anything else is refused and nothing is queued
    if (outcome !== 'dropped' && outcome !== 'gone') throw new WorkerError('fresh-claim-unknown', `the claim of the cached result of ${file} gave an unknown outcome; nothing was queued`);
    return { verdict, outcome };
  }
  return { verdict, outcome: 'kept' };
}

/**
 * B75: `review-file <path> --block <id> --fresh`. No done marker: a normal enqueue. The marker is
 * read and classified IN PLACE; only a cached result that never counted is then claimed by
 * `dropDoneIf` (rename, then a byte compare with what was classified) and the ticket queued again.
 * A marker that changed under the claim is restored (link only) and classified again, at most
 * {@link FRESH_ATTEMPTS} times. The queue has no lock: the rename claim is the exclusion, and a
 * crash between the drop and the enqueue leaves only a re-runnable pending ticket. Anything else is refused and the marker kept byte-for-byte:
 * `fresh-needs-reopen` (names `block reopen` and `block waive`) or `fresh-tampered` (names only
 * the owner's `block close --no-require-reviews`; reopen refuses a tampered state).
 * B110: a plain cached round-1 `split_required` uses the same claim, with `retry: true`.
 * @param {{repoRoot: string, run: string, block: string, file: string, where: {tree?: string}, say: (obj: Record<string, any>) => unknown, dropDone?: typeof dropDoneIf, retry?: boolean}} opts
 * @returns {number} the exit code.
 */
function freshTicket({ repoRoot, run, block, file, where, say, dropDone = dropDoneIf, retry = false }) {
  const { ticket } = resolveTicket({ repoRoot, run, block, file, tree: where.tree ?? null });
  const { verdict, outcome } = dropFreshDone({ repoRoot, ticket, file, dropDone });
  if (outcome === 'dropped' || outcome === 'gone') {
    const queued = enqueue({ repoRoot, run, block, file, ...where });
    say(outcome === 'dropped' ? { ...queued, ...(retry ? { retry: true } : { fresh: true }) } : queued);
    return 0;
  }
  if (verdict === 'tampered') {
    say({ status: 'refused', reason: 'fresh-tampered', ticket, file, exits: ['block close --no-require-reviews'], message: `fresh-tampered: the cached result of ${file} is review-state-tampered (a security event, not a stop); only the owner's block close --no-require-reviews gets past it; the cached result was kept` });
    return 1;
  }
  say({
    status: 'refused',
    reason: 'fresh-needs-reopen',
    ticket,
    file,
    exits: [...FRESH_EXITS],
    message: `fresh-needs-reopen: the cached result of ${file} counts as a review (or cannot be read, or kept changing under the claim); --fresh re-runs only stale, unavailable, round-1 split_required or refused other-run/worktree-*/context-unreadable results; the human's ways out are block reopen and block waive; the cached result was kept`,
  });
  return 1;
}

/**
 * B62b: where the block `block` of run `runId` keeps its content, from the run record (read from
 * the user's state dir, `~/.code-forge/runs`, whatever the current directory): `{top}` with the
 * top level of its worktree, realpath'd; `{top: null}` when the record gives the block no worktree
 * (then the main tree, never the tree `review-file` is called from); null when the record does
 * not exist here or cannot be opened (a sandboxed coder: no such file, or no permission) — only
 * then is the content hashed in the tree `review-file` is called from, and the worker refuses a
 * ticket hashed in the wrong tree. A record that names a worktree which cannot be used (gone,
 * moved, malformed, off its branch) is refused, and so is a record that cannot be parsed or is not
 * a record (`run-record-unreadable`): nothing is queued. Any other failure is rethrown.
 * @param {string} runId @param {string} block
 * @returns {Promise<{top: string | null} | null>}
 * @throws {WorkerError} a `worktree-*` code, or `run-record-unreadable`.
 */
async function blockTreeOf(runId, block) {
  let record;
  try {
    record = await readRun(runId);
  } catch (err) {
    const code = /** @type {{code?: unknown}} */ (err)?.code;
    if (code === 'no-run' || code === 'EACCES' || code === 'EPERM') return null;
    if (!(err instanceof SyntaxError)) throw err;
    record = null;
  }
  if (record === null || typeof record !== 'object') throw new WorkerError('run-record-unreadable', `the run record of ${runId} cannot be read (corrupt or not a record); nothing was queued`);
  try {
    return { top: (await blockTree(record.workspace, record.blocks?.[block]))?.top ?? null };
  } catch (err) {
    if (err instanceof StateError) throw new WorkerError(err.code, `block ${block}: ${err.message}; nothing was queued`);
    throw err;
  }
}

/**
 * B71: the coder-side pre-checks of an answers ticket (fast refusal only): with a run record this
 * process can open, the block must own both files and every id must be open on the source's
 * current hash — named by the latest refusal or cap row there, not waived or answered yet — read by
 * the close's rules over unverified rows ({@link namedOnHash}). A record or ledger that
 * cannot be read here skips the check it needs: the worker re-checks everything with signed rows.
 * The rows of the ticket this request gets do not close an id (the retry of an attempt whose
 * ledger write failed part way). `treeName`: the ticket's tree (null in the main tree).
 * @param {{runId: string, block: string, file: string, byFile: string, tree: string, answers: string[], treeName: string | null}} opts
 * @throws {WorkerError} `answers-not-owned`, `answers-not-open`
 */
async function answersPrecheck({ runId, block, file, byFile, tree, answers, treeName }) {
  let record;
  try {
    record = await readRun(runId);
  } catch {
    return;
  }
  const entry = record?.blocks?.[block];
  if (!entry || typeof entry !== 'object') return;
  const owned = Array.isArray(entry.owned_files) ? entry.owned_files : [];
  if (!ownsFile(owned, file, entry.owned_kinds) || !ownsFile(owned, byFile, entry.owned_kinds)) {
    throw new WorkerError('answers-not-owned', `block ${block} does not own both ${byFile} and ${file}; nothing was queued`);
  }
  let rows;
  try {
    rows = await readAllRows(record.project);
  } catch {
    return;
  }
  const hash = contentHash(tree, file);
  const current = new Map([
    [file, hash],
    [byFile, contentHash(tree, byFile)],
  ]);
  const by_hash = /** @type {string} */ (current.get(byFile));
  const ticket = answersTicketId({ run: runId, block, file, content_hash: hash, by_file: byFile, by_hash, answers, tree: treeName });
  const named = namedOnHash({ block, runId, rows, file, hash, current, ticket });
  const missing = answers.filter((id) => !named.has(id));
  if (missing.length > 0) throw new WorkerError('answers-not-open', `${missing.join(', ')} is not an open finding of ${file} at its current content; nothing was queued`);
}

/**
 * B71: the `--answers` list: comma-separated finding ids, 1 to {@link MAX_ANSWERS}.
 * @param {unknown} raw @returns {string[]}
 * @throws {WorkerError} `usage`
 */
function answersOf(raw) {
  if (typeof raw !== 'string') throw new WorkerError('usage', '--answers needs finding ids');
  const ids = raw.split(',');
  if (ids.length > MAX_ANSWERS) throw new WorkerError('usage', `--answers takes at most ${MAX_ANSWERS} finding ids`);
  if (new Set(ids).size !== ids.length) throw new WorkerError('usage', '--answers names a finding id twice');
  return assertAnswers(ids);
}

/**
 * The coder-facing summary of a done result: the fix list and the stop reason, when any. B71: an
 * answers result gives `answers: [{id, verdict, reason}]` instead.
 * @param {Record<string, any> | null} result
 * @returns {Record<string, any>}
 */
export function fixList(result) {
  if (!result || result.approved === true) return {};
  /** @type {Record<string, any>} */
  const out = {};
  if (result.kind === 'answers' || result.status === 'answers') {
    if (Array.isArray(result.findings)) out.answers = result.findings.map((/** @type {Record<string, any>} */ f) => ({ id: f?.id ?? null, verdict: f?.verdict ?? 'not_resolved', reason: f?.reason ?? '' }));
    return out;
  }
  if (Array.isArray(result.findings) && result.findings.length > 0) {
    out.fix_list = result.findings.map((/** @type {Record<string, any>} */ f) => ({
      id: f?.id ?? null,
      severity: f?.severity ?? null,
      lines: `${f?.line_start ?? '?'}-${f?.line_end ?? '?'}`,
      claim: f?.claim ?? '',
      fix: f?.fix ?? '',
    }));
  }
  if (typeof result.stopped === 'string') {
    out.stop = result.stopped;
    // B73: a stopped file names its ways out (never a refused or tampered state: no `stopped`)
    out.exits = [...STOP_EXITS];
  } else if (result.status === 'split_required') out.stop = 'split_required';
  // B54: a split_required stop (only) says how large the packet was, the budget, and the section
  if (out.stop === 'split_required') {
    const fin = (/** @type {unknown} */ v) => (typeof v === 'number' && Number.isFinite(v) ? v : null);
    Object.assign(out, { tokens_in: fin(result.tokens_in), budget: fin(result.budget), ...(typeof result.section === 'string' ? { section: result.section } : {}) });
  }
  return out;
}

/**
 * B100: read block scope from the workspace mirror, never the private run record. This is a
 * progress summary, not an approval: block close still verifies the authoritative signed rows.
 * @param {string} repoRoot @param {string} cwd @param {string} run @param {string} block
 * @returns {Promise<{entry: Record<string, any>, tree: string}>}
 */
async function waitBlockScope(repoRoot, cwd, run, block) {
  const top = await repoRootOf(cwd);
  const candidates = new Set([repoRoot]);
  for (let dir = realpathSync(cwd); ; dir = path.dirname(dir)) {
    candidates.add(dir);
    if (dir === top || dir === path.dirname(dir)) break;
  }
  for (const dir of candidates) {
    let record;
    try {
      record = JSON.parse(readFileSync(path.join(dir, '.code-forge', 'runs', `${run}.json`), 'utf8'));
    } catch (err) {
      if (err.code === 'ENOENT') continue;
      throw new WorkerError('block-scope-unreadable', 'the block scope cannot be read');
    }
    const entry = record?.blocks?.[block];
    if (record?.run_id !== run || !entry || !Array.isArray(entry.owned_files) || entry.owned_files.length === 0) {
      throw new WorkerError('block-scope-unreadable', 'the block scope cannot be read');
    }
    const tree = (await blockTree(record.workspace, entry))?.top ?? repoRoot;
    return { entry, tree };
  }
  throw new WorkerError('block-scope-unreadable', 'the block scope cannot be read');
}

/** Read a completion marker without the signing key.
 * @param {string} repoRoot @param {string} ticket @returns {Record<string, any> | null}
 */
function blockTicketMarker(repoRoot, ticket) {
  try {
    const marker = JSON.parse(readFileSync(path.join(queueDir(repoRoot), `${ticket}.done`), 'utf8'));
    if (marker?.ticket === ticket && typeof marker.status === 'string' && marker.status.length > 0) return marker;
  } catch (err) {
    if (err.code === 'ENOENT') return null;
  }
  throw new WorkerError('result-unreadable', 'a ticket completion marker cannot be read');
}

/**
 * A block wait uses unsigned results only to classify retries; it never reads the signing key.
 * Re-scan current content on every poll. Deliver unseen findings of the newest earlier ticket
 * before rechecking a blind edit; the next join queues the fix. Stopped files stay finished.
 * @param {{repoRoot: string, cwd: string, block: string, run: string, maxMs: number, pollMs: number, say: (obj: Record<string, any>) => unknown, dropDone?: typeof dropDoneIf}} opts
 */
async function waitBlock({ repoRoot, cwd, block, run, maxMs, pollMs, say, dropDone = dropDoneIf }) {
  const { entry, tree } = await waitBlockScope(repoRoot, cwd, run, block);
  const where = realpathSync(tree) === realpathSync(repoRoot) ? {} : { tree: realpathSync(tree) };
  const deadline = Date.now() + maxMs;
  let downPolls = 0;
  const deliveredHere = new Set();
  for (;;) {
    const worker = liveWorker(repoRoot);
    const live = worker?.run === run;
    downPolls = live ? 0 : downPolls + 1;
    const tickets = blockTickets({ repoRoot, run, block, ...where });
    const current = await blockFileSet({ repoRoot: tree, base: entry.base_sha, owned: entry.owned_files, ownedKinds: entry.owned_kinds });
    const files = current.map(({ file }) => {
      const opts = { repoRoot, run, block, file, ...where };
      const { ticket } = resolveTicket(opts);
      const previous = tickets.filter((t) => t.file === file);
      let present = previous.find((t) => t.ticket === ticket);
      if (!present && previous.length > 0) {
        const newest = previous[previous.length - 1];
        const marker = blockTicketMarker(repoRoot, newest.ticket);
        if (marker?.status === 'stopped') return { file, ticket: newest.ticket, status: 'done' };
        if (marker?.status === 'reviewed' && marker.open_findings === true && live) {
          if (deliveredHere.has(newest.ticket) || deliverBlockFindings({ repoRoot, block, file, ticket: newest.ticket })) {
            deliveredHere.add(newest.ticket);
            return { file, ticket: newest.ticket, status: 'findings' };
          }
        }
      }
      if (!present && previous.length > 0 && live) {
        enqueue(opts);
        present = { ...previous[previous.length - 1], ticket };
      }
      let marker = present ? blockTicketMarker(repoRoot, ticket) : null;
      if (live && marker && RETRY_STATUSES.has(marker.status)) {
        dropFreshDone({ repoRoot, ticket, file, dropDone, once: true, statuses: RETRY_STATUSES });
        marker = blockTicketMarker(repoRoot, ticket);
      }
      return { file, ticket: present ? ticket : null, status: !present ? 'not_queued' : marker ? 'done' : 'pending' };
    });
    if (downPolls >= 2) {
      say({ block, files, done: false, status: 'worker_down' });
      return 3;
    }
    if (live && files.every((f) => f.status === 'done' || f.status === 'findings')) {
      say({ block, files, done: true });
      return 0;
    }
    if (live && (files.some((f) => f.status === 'not_queued') || Date.now() >= deadline)) {
      say({ block, files, done: false, reason: files.some((f) => f.status === 'not_queued') ? 'not_queued' : 'wait_timeout' });
      return 0;
    }
    await new Promise((resolve) => setTimeout(resolve, pollMs));
  }
}

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}, cwd?: string, pollMs?: number, dropDone?: typeof dropDoneIf}} [deps] - `dropDone`: B75's claim, for tests.
 * @returns {Promise<number>}
 */
export async function runReviewFile(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const say = (/** @type {Record<string, any>} */ obj) => writeSafe(stdout, `${JSON.stringify(obj)}\n`);
  const cwd = deps.cwd ?? process.cwd();
  try {
    const { flags, positionals } = parseFlags(args, { values: ['block', 'run', 'wait', 'wait-block', 'max', 'answers', 'of'], booleans: ['fresh'] });
    // the queue and the worker: the main tree, also from inside a block's worktree
    const repoRoot = await mainRootOf(cwd);

    if (flags['wait-block'] !== undefined) {
      if (positionals.length > 0 || flags.block !== undefined || flags.wait !== undefined || flags.answers !== undefined || flags.of !== undefined || flags.fresh === true || typeof flags.run !== 'string') {
        throw new WorkerError('usage', '--wait-block takes a block, --run and --max only');
      }
      return await waitBlock({ repoRoot, cwd, block: assertBlockId(flags['wait-block']), run: assertRunId(flags.run), maxMs: flags.max === undefined ? DEFAULT_WAIT_MS : parseMax(flags.max), pollMs: deps.pollMs ?? POLL_MS, say, dropDone: deps.dropDone });
    }

    if (typeof flags.wait === 'string') {
      if (positionals.length > 0 || flags.block !== undefined || flags.answers !== undefined || flags.of !== undefined || flags.fresh === true) throw new WorkerError('usage', '--wait takes only a ticket and --max');
      const ticket = assertTicketId(flags.wait);
      const maxMs = flags.max === undefined ? DEFAULT_WAIT_MS : parseMax(flags.max);
      const deadline = Date.now() + maxMs;
      let downPolls = 0;
      for (;;) {
        if (isDone(repoRoot, ticket)) {
          const result = doneResult(repoRoot, ticket);
          say({ ticket, status: 'done', result, ...fixList(result) });
          return 0;
        }
        downPolls = liveWorker(repoRoot) ? 0 : downPolls + 1;
        if (downPolls >= 2 && !isDone(repoRoot, ticket)) {
          say({ ticket, status: 'worker_down' });
          return 3;
        }
        if (Date.now() >= deadline && downPolls === 0) {
          say({ ticket, status: 'pending', reason: 'wait_timeout' });
          return 0;
        }
        await new Promise((resolve) => setTimeout(resolve, deps.pollMs ?? POLL_MS));
      }
    }

    if (positionals.length !== 1 || typeof flags.block !== 'string' || flags.max !== undefined) throw new WorkerError('usage', 'give one path and --block');
    const block = assertBlockId(flags.block);
    if (flags.fresh === true && (flags.answers !== undefined || flags.of !== undefined)) throw new WorkerError('usage', '--fresh takes no --answers or --of');
    if (flags.answers !== undefined || flags.of !== undefined) return await enqueueAnswersTicket({ flags, answering: positionals[0], block, cwd, repoRoot, say });
    const file = normalizeRequestPath(positionals[0], cwd, await repoRootOf(cwd));
    const worker = liveWorker(repoRoot);
    if (!worker || (typeof flags.run === 'string' && flags.run !== worker.run)) {
      say({ status: 'worker_down', file });
      return 3;
    }
    // the content: the block's worktree when the record gives one, the main tree when it gives
    // none; only a record that cannot be opened here falls back to the tree review-file is called from
    const recorded = await blockTreeOf(worker.run, block);
    const tree = recorded === null ? await repoRootOf(cwd) : (recorded.top ?? repoRoot);
    assertInsideRoot(tree, file); // the path, re-checked in the tree its content is read from
    const where = realpathSync(tree) === realpathSync(repoRoot) ? {} : { tree };
    if (flags.fresh === true) return freshTicket({ repoRoot, run: worker.run, block, file, where, say, ...(deps.dropDone ? { dropDone: deps.dropDone } : {}) });
    const queued = enqueue({ repoRoot, run: worker.run, block, file, ...where });
    if (queued.status === 'done' && ['unavailable', 'split_required'].includes(doneResult(repoRoot, queued.ticket)?.status)) {
      return freshTicket({ repoRoot, run: worker.run, block, file, where, say, retry: true, ...(deps.dropDone ? { dropDone: deps.dropDone } : {}) });
    }
    say(queued);
    return 0;
  } catch (err) {
    const code = err instanceof WorkerError || err?.name === 'StateError' ? err.code : 'error';
    if (code === 'usage') {
      writeSafe(stderr, `review-file: ${err.message}\n${USAGE}`);
      return 2;
    }
    say({ status: 'refused', reason: code, message: err?.message ?? String(err) });
    return 1;
  }
}

/**
 * B71: `review-file <answering file> --block <id> --answers <ids> --of <source>`.
 * @param {{flags: Record<string, any>, answering: string, block: string, cwd: string, repoRoot: string, say: (obj: Record<string, any>) => unknown}} opts
 * @returns {Promise<number>}
 * @throws {WorkerError}
 */
async function enqueueAnswersTicket({ flags, answering, block, cwd, repoRoot, say }) {
  if (typeof flags.of !== 'string' || flags.of.length === 0) throw new WorkerError('usage', '--answers needs --of <source>');
  const answers = answersOf(flags.answers);
  const top = await repoRootOf(cwd);
  const byFile = normalizeRequestPath(answering, cwd, top);
  const file = normalizeRequestPath(flags.of, cwd, top);
  if (byFile === file) throw new WorkerError('answers-same-file', `${file} cannot answer its own findings: give the file that resolves them`);
  const worker = liveWorker(repoRoot);
  if (!worker || (typeof flags.run === 'string' && flags.run !== worker.run)) {
    say({ status: 'worker_down', file });
    return 3;
  }
  const recorded = await blockTreeOf(worker.run, block);
  const tree = recorded === null ? top : (recorded.top ?? repoRoot);
  assertInsideRoot(tree, file);
  assertInsideRoot(tree, byFile);
  const where = realpathSync(tree) === realpathSync(repoRoot) ? {} : { tree };
  await answersPrecheck({ runId: worker.run, block, file, byFile, tree, answers, treeName: where.tree === undefined ? null : realpathSync(tree) });
  const opts = { repoRoot, run: worker.run, block, file, byFile, answers, ...where };
  const queued = enqueueAnswers(opts);
  if (queued.status === 'done' && doneResult(repoRoot, queued.ticket)?.status === 'unavailable') {
    rmSync(path.join(queueDir(repoRoot), `${queued.ticket}.done`), { force: true });
    say({ ...enqueueAnswers(opts), kind: 'answers', retry: true });
    return 0;
  }
  say({ ...queued, kind: 'answers' });
  return 0;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function run(args) {
  return runReviewFile(args);
}
