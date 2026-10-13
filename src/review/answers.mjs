/**
 * B71 (issue #9 b): an answers ticket — `review-file <answering file> --block <id> --answers <ids>
 * --of <source>` — asks ONE fresh L2 reviewer session whether the answering file (`by_file`)
 * resolves the open findings `<ids>` of the source file. The worker runs it through the engine
 * seam (`reviewTicket` dispatches here on `kind: 'answers'`); `loop.mjs` has already checked the
 * ticket's run, its tree and that the source still has the ticket's hash.
 *
 * Checks here (the ones that count; `review-file` only pre-checks the same rules, unverified):
 *  - both files are owned by the block (`answers-not-owned`) and they are two files
 *    (`answers-same-file`);
 *  - each file is read ONCE: the answering file's bytes must hash to the ticket's `by_hash` (else
 *    `stale`, `answering-file-changed`; absent or unreadable: `answers-file-missing`) and the
 *    source's to its `content_hash` (else `stale`, `source-changed`); the packet is built from
 *    those same bytes, so the row's `by_hash` is the hash of what the reviewer saw;
 *  - every id is open on the source's CURRENT hash by the signed rows and not waived or answered
 *    yet — `answerableFindings`, the close's own rules (`answers-not-open`).
 * A refusal runs no session. The packet holds each finding (file, lines, severity, claim, evidence,
 * fix — from the source's signed fix-loop state; only id and severity when the state has none),
 * the source lines around it, and the answering file's diff against the block base (its full
 * content when it is new). The reviewer answers per id `resolved` or `not_resolved` with a reason;
 * a missing, repeated or broken verdict is `not_resolved`, and a session that fails (no result,
 * not `ok`, a non-zero exit, a thrown spawn error: `session-error`) makes the ticket `unavailable`
 * with no row. A packet that cannot be built is `packet-unreadable`, one that cannot be written to
 * the work dir `packet-write-failed` (both `unavailable`). For each resolved id the
 * worker writes one signed `review.answered {block, file: source, content_hash: source hash,
 * finding, by_file, by_hash, ticket}` row (`ctx.writeRow` stamps the block, the source and its
 * hash). The ticket's result is `status: 'answers'`, never an approval: the gate never reads it
 * as a verdict on the source (`neverReviewed`), the answering file still needs its own approval,
 * and the source's fix-loop state is not touched.
 *
 * The worker's file and state readers come in through `io` (the worker passes its own
 * `readRegularFileNoFollow`, `loadState` and `assertAnswers`), so this review-side module imports
 * nothing from `src/worker/`.
 */

import { createHash, randomBytes } from 'node:crypto';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { ownsFile } from '../state/registry.mjs';
import { exec } from '../util/exec.mjs';
import { redact } from '../util/redact.mjs';
import { ANSWERED_EVENT, ANSWERS_STATUS, answerableFindings, ticketAnswered } from './gate-check.mjs';
import { fixHunkDiff } from './fixloop.mjs';
import { assertPacketPath, budgetFor, isSecretLike, plainGitEnv, reviewGitEnv } from './packet.mjs';
import { spawnWithTimeoutRetry } from './session-retry.mjs';

/** The answer the reviewer gives: one verdict per finding id. */
export const ANSWERS_SCHEMA = Object.freeze({
  type: 'object',
  properties: {
    verdicts: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          id: { type: 'string' },
          verdict: { type: 'string', enum: ['resolved', 'not_resolved'] },
          reason: { type: 'string' },
        },
        required: ['id', 'verdict', 'reason'],
        additionalProperties: false,
      },
    },
  },
  required: ['verdicts'],
  additionalProperties: false,
});

/** The system prompt of the answers session. */
export const ANSWERS_LENS =
  'You are a code reviewer for code-forge, judging whether a change in one file resolves findings a review raised about another file. ' +
  'You have no tools and no files; everything you may use is in the message. Answer once, with a JSON object that matches the schema.';

/** Lines of the source shown before and after each finding. */
const AROUND = 5;

/** A reason longer than this is cut (the reviewer's free text). */
const REASON_MAX = 500;

/**
 * @typedef {object} AnswersOutcome
 * @property {string} status - `answers`, `stale` or `unavailable`.
 * @property {boolean} approved - always false.
 * @property {string} engine
 * @property {string} [reason]
 * @property {string} kind - `answers`.
 * @property {Array<{id: string, severity: unknown, verdict: 'resolved' | 'not_resolved', reason: string}>} [findings]
 * @property {Array<Record<string, any>>} sessions
 */

/**
 * The ticket fields an answers ticket carries (`worker/queue.mjs` `enqueueAnswers`).
 * @typedef {object} AnswersTicket
 * @property {string} ticket
 * @property {string} block
 * @property {string} file - the source.
 * @property {string} content_hash - the source's hash.
 * @property {string} [by_file]
 * @property {string} [by_hash]
 * @property {string[]} [answers]
 */

/**
 * What the worker's review context gives an answers ticket.
 * @typedef {object} AnswersContext
 * @property {string} repoRoot
 * @property {string | null} [contentRoot] - the block's tree, when it has one.
 * @property {string} runId
 * @property {string} [runRootDir]
 * @property {Buffer | null} [key]
 * @property {Parameters<typeof budgetFor>[0]} cfg
 * @property {Parameters<typeof spawnWithTimeoutRetry>[0]} spawn
 * @property {() => Promise<Array<Record<string, any>>>} [readRows]
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeRow]
 */

/**
 * The worker's readers (injected, see the module doc).
 * @typedef {object} AnswersIo
 * @property {(root: string, file: string) => Buffer | null} readFile - no symlinks; null when absent.
 * @property {(opts: Record<string, any>) => {status: string, state?: Record<string, any> | null}} loadState - the signed fix-loop state, read only.
 * @property {(answers: unknown) => string[]} assertAnswers - the queue's id-list rule (sorted, unique); throws when the list is bad.
 */

/**
 * @param {string} status @param {string} reason @param {Array<Record<string, any>>} [sessions]
 * @returns {AnswersOutcome}
 */
const outcome = (status, reason, sessions = []) => ({ status, reason, approved: false, engine: 'adaptive', kind: 'answers', sessions });

/**
 * Run one answers ticket.
 * @param {AnswersTicket} ticket - `kind: 'answers'`.
 * @param {AnswersContext} ctx - `contentRoot` is the block's tree.
 * @param {{entry: {base: string | null, owned: string[], ownedKinds?: Record<string, string>}, workDir: string, io: AnswersIo}} opts
 * @returns {Promise<AnswersOutcome>}
 */
export async function reviewAnswers(ticket, ctx, { entry, workDir, io }) {
  const root = ctx.contentRoot ?? ctx.repoRoot;
  const source = ticket.file;
  const byFile = /** @type {string} */ (ticket.by_file);
  // the id list, checked here too by the queue's own rule: 1 to 10 well-formed ids, none twice
  /** @type {string[]} */
  let ids;
  try {
    ids = io.assertAnswers(ticket.answers);
  } catch {
    return outcome(ANSWERS_STATUS, 'usage');
  }
  if (!Array.isArray(ticket.answers) || ids.length !== ticket.answers.length) return outcome(ANSWERS_STATUS, 'usage');
  if (!ctx.key || !ctx.readRows || !ctx.writeRow) return outcome('unavailable', 'no-ledger');
  if (typeof byFile !== 'string' || path.posix.normalize(byFile) === path.posix.normalize(source)) return outcome(ANSWERS_STATUS, 'answers-same-file');
  if (!ownsFile(entry.owned, source, entry.ownedKinds) || !ownsFile(entry.owned, byFile, entry.ownedKinds)) return outcome(ANSWERS_STATUS, 'answers-not-owned');
  // each file is read ONCE; the hashes and the packet come from these bytes
  /** @type {Buffer | null} */
  let byBytes;
  /** @type {Buffer | null} */
  let srcBytes;
  try {
    byBytes = io.readFile(root, byFile);
  } catch {
    byBytes = null;
  }
  if (byBytes === null) return outcome(ANSWERS_STATUS, 'answers-file-missing');
  const byHash = hashOf(byBytes);
  if (byHash !== ticket.by_hash) return outcome('stale', 'answering-file-changed');
  try {
    srcBytes = io.readFile(root, source);
  } catch {
    srcBytes = null;
  }
  if (srcBytes === null || hashOf(srcBytes) !== ticket.content_hash) return outcome('stale', 'source-changed');

  let rows;
  try {
    rows = (await ctx.readRows()).filter((r) => r?.run === ctx.runId);
  } catch {
    return outcome('unavailable', 'ledger-unreadable');
  }
  const current = new Map([
    [source, ticket.content_hash],
    [byFile, byHash],
  ]);
  // this ticket's own rows do not close an id: a retry after a partial write finishes the rest
  const open = answerableFindings({ block: ticket.block, runId: ctx.runId, rows, key: ctx.key, file: source, hash: ticket.content_hash, current, ticket: ticket.ticket });
  if (!ids.every((id) => open.has(id))) return outcome(ANSWERS_STATUS, 'answers-not-open');

  let text;
  try {
    text = await packetText({ ticket, ctx, rows, root, entry, open, io, ids, srcBytes, byBytes, workDir });
  } catch {
    return outcome('unavailable', 'packet-unreadable');
  }
  const promptPath = path.join(workDir, `answers-${randomBytes(6).toString('hex')}.md`);
  try {
    mkdirSync(workDir, { recursive: true, mode: 0o700 });
    writeFileSync(promptPath, text, { mode: 0o600 });
  } catch {
    return outcome('unavailable', 'packet-write-failed');
  }

  const note = async (/** @type {Record<string, any>} */ row) => {
    try {
      await ctx.writeRow?.(row);
    } catch {
      // a note is best-effort
    }
  };
  let res;
  try {
    ({ res } = await spawnWithTimeoutRetry(ctx.spawn, { level: 'L2', role: 'reviewer', promptPath, schema: ANSWERS_SCHEMA, systemPromptText: ANSWERS_LENS, rowExtra: { lens: 'answers' } }, note));
  } catch {
    // a spawn or adapter error is no verdict: retry the ticket
    return outcome('unavailable', 'session-error');
  } finally {
    rmSync(promptPath, { force: true });
  }
  const sessions = [{ lens: 'answers', role: 'reviewer', level: 'L2', provider: res?.provider ?? null, model: res?.model ?? null, tokens_in: res?.usage?.tokens_in ?? null, tokens_out: res?.usage?.tokens_out ?? null }];
  // a spawn that threw (`spawnWithTimeoutRetry` gives null) is `session-error`, like a throw above
  if (!res) return outcome('unavailable', 'session-error', sessions);
  if (res.status !== 'ok' || (res.exit_code !== undefined && res.exit_code !== 0)) return outcome('unavailable', typeof res.status === 'string' && res.status !== 'ok' ? res.status : 'exit', sessions);

  const verdicts = verdictsOf(res.answer, ids);
  /** @type {NonNullable<AnswersOutcome['findings']>} */
  const findings = ids.map((id) => {
    const v = verdicts.get(id) ?? { verdict: /** @type {const} */ ('not_resolved'), reason: 'no valid verdict' };
    return { id, severity: open.get(id) ?? null, verdict: v.verdict, reason: v.reason };
  });
  // every verdict is built before any row is written; an id this ticket already answered (an
  // earlier attempt whose later write failed) gets no second row, so a retry ends with one per id
  const done = ticketAnswered({ block: ticket.block, runId: ctx.runId, rows, key: ctx.key, file: source, current, ticket: ticket.ticket });
  for (const f of findings) {
    if (f.verdict !== 'resolved' || done.has(f.id)) continue;
    try {
      await ctx.writeRow({ event: ANSWERED_EVENT, finding: f.id, by_file: byFile, by_hash: byHash, ticket: ticket.ticket });
    } catch {
      return outcome('unavailable', 'ledger-write-failed', sessions);
    }
  }
  return { status: ANSWERS_STATUS, approved: false, engine: 'adaptive', kind: 'answers', findings, sessions };
}

/**
 * The reviewer's verdict per requested id: only `{id, verdict: 'resolved' | 'not_resolved',
 * reason: string}` entries count; an id given twice, an unknown id or a broken entry gives no
 * verdict for that id (the caller reads it as `not_resolved`).
 * @param {unknown} answer @param {string[]} ids
 * @returns {Map<string, {verdict: 'resolved' | 'not_resolved', reason: string}>}
 */
export function verdictsOf(answer, ids) {
  /** @type {Map<string, {verdict: 'resolved' | 'not_resolved', reason: string}>} */
  const out = new Map();
  let value = answer;
  if (typeof value === 'string') {
    try {
      value = JSON.parse(value);
    } catch {
      return out;
    }
  }
  const list = value && typeof value === 'object' && Array.isArray(/** @type {any} */ (value).verdicts) ? /** @type {unknown[]} */ (/** @type {any} */ (value).verdicts) : [];
  /** @type {Set<string>} */
  const seen = new Set();
  /** @type {Set<string>} */
  const repeated = new Set();
  for (const v of list) {
    const e = /** @type {Record<string, unknown>} */ (v);
    if (!e || typeof e !== 'object' || typeof e.id !== 'string' || !ids.includes(e.id)) continue;
    if (seen.has(e.id)) repeated.add(e.id);
    seen.add(e.id);
    if ((e.verdict === 'resolved' || e.verdict === 'not_resolved') && typeof e.reason === 'string') out.set(e.id, { verdict: e.verdict, reason: e.reason.slice(0, REASON_MAX) });
  }
  for (const id of repeated) out.delete(id);
  return out;
}

/** The worker's content hash of bytes it read (`worker/ticket.mjs` `contentHash`: sha256, hex). */
const hashOf = (/** @type {Buffer} */ bytes) => createHash('sha256').update(bytes).digest('hex');

/**
 * The answering file's change, built from the bytes that were hashed (never a second read): its
 * diff against its text at the block base (`entry.base`, else HEAD), or its full content when the
 * base has no such file. A secret-like path or a git failure throws (`packet-unreadable`).
 * @param {{root: string, file: string, base: string | null, bytes: Buffer, workDir: string}} opts
 * @returns {Promise<{kind: 'new' | 'tracked', text: string}>}
 */
async function answeringChange({ root, file, base, bytes, workDir }) {
  const rel = assertPacketPath(root, file);
  if (isSecretLike(rel)) throw new Error('secret-like path');
  const ref = base ?? 'HEAD';
  if (typeof ref !== 'string' || ref.length === 0 || ref.startsWith('-')) throw new Error('the base must be a commit id');
  const git = { cwd: root, timeoutMs: 30000 };
  const listed = await exec(['git', 'ls-tree', '-z', ref, '--', rel], { ...git, env: reviewGitEnv() });
  if (listed.result !== 'ok') throw new Error(`git ls-tree failed (exit ${listed.code})`);
  const current = bytes.toString('utf8');
  if (listed.stdout.length === 0) return { kind: 'new', text: current };
  const shown = await exec(['git', 'show', `${ref}:${rel}`], { ...git, env: plainGitEnv() });
  if (shown.result !== 'ok') throw new Error(`git show failed (exit ${shown.code})`);
  const diff = await fixHunkDiff({ file: rel, previous: shown.stdout, current, workDir });
  return { kind: 'tracked', text: diff.diffText };
}

/**
 * The packet: the findings, the source lines around each, and the answering file's change — all
 * from the bytes `reviewAnswers` read and hashed.
 * @param {{ticket: AnswersTicket, ctx: AnswersContext, rows: Array<Record<string, any>>, root: string, entry: {base: string | null}, open: Map<string, unknown>, io: AnswersIo, ids: string[], srcBytes: Buffer, byBytes: Buffer, workDir: string}} opts
 * @returns {Promise<string>}
 */
async function packetText({ ticket, ctx, rows, root, entry, open, io, ids, srcBytes, byBytes, workDir }) {
  const source = ticket.file;
  const byFile = /** @type {string} */ (ticket.by_file);
  const details = findingDetails({ ticket, ctx, rows, root, io });
  const lines = srcBytes.toString('utf8').split('\n');
  const diff = await answeringChange({ root, file: byFile, base: entry.base, bytes: byBytes, workDir });
  const limit = budgetFor(ctx.cfg, 'full_in') * 4;
  let change = diff.text;
  if (change.length > limit) change = `${change.slice(0, limit)}\n[... truncated at the packet budget]`;
  const out = [
    '# Answered findings',
    '',
    `A review of \`${source}\` left the findings below open. The coder says the change in \`${byFile}\` resolves them.`,
    'For EACH finding id, decide whether that change resolves it: `resolved` only when the change clearly removes the problem the finding names; otherwise `not_resolved`. Give a short reason for each.',
    'Reply with ONE JSON object: {"verdicts": [{"id": string, "verdict": "resolved" | "not_resolved", "reason": string}]}, one entry per id below.',
    '',
  ];
  for (const id of ids) {
    const f = details.get(id) ?? null;
    const start = Number.isInteger(f?.line_start) ? /** @type {number} */ (f?.line_start) : null;
    const end = Number.isInteger(f?.line_end) ? /** @type {number} */ (f?.line_end) : start;
    out.push(`## Finding ${id}`, '', `- file: ${source}`, `- lines: ${start === null ? 'unknown' : `${start}-${end}`}`, `- severity: ${String(f?.severity ?? open.get(id) ?? 'unknown')}`);
    for (const k of /** @type {const} */ (['claim', 'evidence', 'fix'])) out.push(`- ${k}: ${typeof f?.[k] === 'string' ? f[k] : '(not recorded)'}`);
    out.push('');
    if (start !== null && end !== null && lines.length > 0) {
      const from = Math.max(1, start - AROUND);
      const to = Math.min(lines.length, end + AROUND);
      out.push(`Source lines ${from}-${to}:`, '```', ...lines.slice(from - 1, to).map((l, i) => `${from + i}: ${l}`), '```', '');
    }
  }
  out.push(`## The change in ${byFile}`, '', diff.kind === 'new' ? '(a new file: its full content)' : `(its diff against the block base)`, '```', change, '```', '');
  return redact(out.join('\n'));
}

/**
 * The open findings' details from the source's signed fix-loop state (read only, never moved or
 * restored here). A state that does not load gives none: the packet then names id and severity.
 * @param {{ticket: AnswersTicket, ctx: AnswersContext, rows: Array<Record<string, any>>, root: string, io: AnswersIo}} opts
 * @returns {Map<string, Record<string, any>>}
 */
function findingDetails({ ticket, ctx, rows, root, io }) {
  /** @type {Map<string, Record<string, any>>} */
  const out = new Map();
  try {
    const loaded = io.loadState({ runRootDir: ctx.runRootDir, runId: ctx.runId, block: ticket.block, file: ticket.file, key: /** @type {Buffer} */ (ctx.key), rows, contentRoot: root, migrate: false });
    if (loaded.status !== 'ok' && loaded.status !== 'restored') return out;
    for (const f of [...(loaded.state?.open ?? []), ...(loaded.state?.late ?? [])]) if (typeof f?.id === 'string' && !out.has(f.id)) out.set(f.id, f);
  } catch {
    // no details: id and severity only
  }
  return out;
}
