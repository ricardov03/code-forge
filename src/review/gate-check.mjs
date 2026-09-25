/**
 * The block gate's review rows (plan §4.6 rows 2, 9 and 11; §4.9 `block waive`; §4.11; block B12b).
 *
 * `checkBlockReviews` refuses the block when:
 *  - `unreviewed`: a changed or new file has NO `review.approved` row for its CURRENT content hash;
 *  - `unsigned`:   the file's approvals for that hash exist but none carries a valid MAC;
 *  - `rule_break`: a `rule_break` row names the block, or the coder transcript hits the forbidden
 *    list (`scanTranscript`; a read of `~/.code-forge/runs/` — the signer key — is the named case);
 *  - `late_unruled`: a `review.late_finding` has neither a signed `review.late_ruling` (the
 *    block-close sweep's L3 ruling) nor a signed `review.waived` row;
 *  - `review_cap`: a file stopped at `review_cap` has an open finding with no signed
 *    `review.waived {by: human}` row — unless the file has a signed approval at its current hash
 *    (the human fixed it by hand and it was reviewed).
 * Rulings and waivers only count when their MAC verifies AND name the finding's file exactly
 * (`file` + `finding`; a finding id is scoped to one file, so a waiver of `F1` in `a.mjs` never
 * clears `F1` in `b.mjs`); a late finding counts whether or not it is signed (fail closed).
 *
 * `block waive <id> <finding> --file <path> --reason <text>` (`waiveFinding`) writes a signed
 * `review.waived` row with `by: human`. The orchestrator runs it only after the human said so in chat; the brief
 * never mentions it; for a coder the argv is forbidden (`mergeForbidden()` always carries B12b's
 * coder-only entries, `CODER_ONLY_FORBIDDEN` in `util/forbidden.mjs`).
 */

import { lstatSync } from 'node:fs';
import path from 'node:path';
import { mergeForbidden, scanTranscript } from '../util/forbidden.mjs';
import { exec } from '../util/exec.mjs';
import { readRun, writeSigned } from '../state/run.mjs';
import { StateError } from '../state/paths.mjs';
import { verifyRow } from '../state/signer.mjs';
import { DELETED_HASH, contentHash, gitChildEnv } from '../worker/ticket.mjs';

/**
 * @typedef {{code: 'unreviewed' | 'unsigned' | 'rule_break' | 'late_unruled' | 'review_cap', file?: string, finding?: string, detail?: string}} Refusal
 */

/** @param {Record<string, any>} row @param {Buffer} key */
const verified = (row, key) => verifyRow(row, key).ok;

/**
 * @param {{
 *   block: string, runId?: string, files: Array<{file: string, content_hash: string}>,
 *   rows: Array<Record<string, any>>, key: Buffer, transcript?: string | null,
 *   extraTokens?: string[],
 * }} opts
 * @returns {{ok: boolean, refusals: Refusal[]}}
 */
export function checkBlockReviews({ block, runId, files, rows, key, transcript = null, extraTokens = [] }) {
  const mine = rows.filter((r) => r?.block === block && (runId === undefined || r.run === undefined || r.run === runId));
  /** @type {Refusal[]} */
  const refusals = [];
  const approvedFor = (/** @type {string} */ file, /** @type {string} */ hash) => mine.filter((r) => r.event === 'review.approved' && r.file === file && r.content_hash === hash);
  const hashOf = new Map(files.map((f) => [f.file, f.content_hash]));

  for (const { file, content_hash } of files) {
    const approvals = approvedFor(file, content_hash);
    if (approvals.length === 0) refusals.push({ code: 'unreviewed', file, detail: `no review.approved row for ${file} at its current content` });
    else if (!approvals.some((r) => verified(r, key))) refusals.push({ code: 'unsigned', file, detail: `the review.approved row for ${file} has no valid MAC` });
  }

  for (const r of mine.filter((row) => row.event === 'rule_break')) refusals.push({ code: 'rule_break', detail: `rule_break row: ${String(r.id ?? r.rule ?? 'unnamed')}` });
  if (typeof transcript === 'string' && transcript.length > 0) {
    for (const hit of scanTranscript(transcript, mergeForbidden(extraTokens))) refusals.push({ code: 'rule_break', detail: `transcript line ${hit.line}: ${hit.id}` });
  }

  const signedOf = (/** @type {string} */ event) => mine.filter((r) => r.event === event && verified(r, key));
  const waivers = signedOf('review.waived').filter((r) => r.by === 'human');
  const rulings = signedOf('review.late_ruling');
  const matches = (/** @type {Record<string, any>} */ r, /** @type {string} */ file, /** @type {string} */ finding) => r.finding === finding && r.file === file;
  const waived = (/** @type {string} */ file, /** @type {string} */ finding) => waivers.some((r) => matches(r, file, finding));

  for (const late of mine.filter((r) => r.event === 'review.late_finding')) {
    if (rulings.some((r) => matches(r, late.file, late.finding)) || waived(late.file, late.finding)) continue;
    refusals.push({ code: 'late_unruled', file: late.file, finding: late.finding, detail: `late finding ${late.finding} in ${late.file} has no ruling` });
  }

  for (const cap of mine.filter((r) => r.event === 'review.cap')) {
    const hash = hashOf.get(cap.file);
    if (hash !== undefined && approvedFor(cap.file, hash).some((r) => verified(r, key))) continue;
    for (const finding of Array.isArray(cap.open) ? cap.open : []) {
      if (!waived(cap.file, finding)) refusals.push({ code: 'review_cap', file: cap.file, finding, detail: `${cap.file} stopped at review_cap; finding ${finding} is open and not waived` });
    }
  }
  return { ok: refusals.length === 0, refusals };
}

/**
 * The block's file set (§4.1): changed tracked files since `base` (deletions included, hashed
 * `DELETED_HASH`) ∪ untracked files, filtered to `owned` (exact paths; a glob owned entry matches through `matchOwned`), each with its content hash.
 * @param {{repoRoot: string, base: string, owned: string[], matchOwned?: (file: string) => boolean}} opts
 * @returns {Promise<Array<{file: string, content_hash: string}>>}
 */
export async function blockFileSet({ repoRoot, base, owned, matchOwned }) {
  if (typeof base !== 'string' || !/^[0-9a-f]{7,64}$/.test(base)) throw new StateError('bad-ref', 'the block base must be a commit id');
  const env = gitChildEnv();
  const changed = await exec(['git', 'diff', '--name-only', '-z', '--no-renames', base, '--'], { cwd: repoRoot, env, timeoutMs: 30000 });
  const untracked = await exec(['git', 'ls-files', '-z', '--others', '--exclude-standard'], { cwd: repoRoot, env, timeoutMs: 30000 });
  if (changed.result !== 'ok' || untracked.result !== 'ok') throw new StateError('git-failed', 'git could not list the block file set');
  const all = new Set([...changed.stdout.split('\0'), ...untracked.stdout.split('\0')].filter((f) => f.length > 0));
  const isOwned = matchOwned ?? ((/** @type {string} */ f) => owned.includes(f));
  return [...all]
    .filter((f) => isOwned(f))
    .sort()
    .map((file) => ({ file, content_hash: isOnDisk(repoRoot, file) ? contentHash(repoRoot, file) : DELETED_HASH }));
}

/**
 * A deleted owned file (its directory may be gone too) is a change that needs review; its hash is
 * `DELETED_HASH`, never a read of a path that is not there.
 * @param {string} repoRoot @param {string} file @returns {boolean}
 */
function isOnDisk(repoRoot, file) {
  try {
    lstatSync(path.join(repoRoot, file));
    return true;
  } catch {
    return false;
  }
}

/**
 * A `closeBlock` extra check (B8's `extraChecks` seam): refusals joined into one reason.
 * @param {() => Promise<Parameters<typeof checkBlockReviews>[0]>} load
 * @returns {() => Promise<{ok: boolean, reason?: string}>}
 */
export function reviewGateCheck(load) {
  return async () => {
    const result = checkBlockReviews(await load());
    if (result.ok) return { ok: true };
    return { ok: false, reason: result.refusals.map((r) => `${r.code}${r.file ? ` ${r.file}` : ''}${r.finding ? ` ${r.finding}` : ''}`).join('; ') };
  };
}

/**
 * `block waive` — a signed `review.waived` row, `by: human`, scoped to ONE file (`file` is
 * required: the gate matches a waiver by `file` + `finding`). The block must exist in the run.
 * @param {{runId: string, id: string, finding: string, reason: string, file: string, writeRow: (row: Record<string, any>) => Promise<unknown>}} opts
 * @returns {Promise<Record<string, any>>} the signed row.
 */
export async function waiveFinding({ runId, id, finding, reason, file, writeRow }) {
  if (typeof finding !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/.test(finding)) throw new StateError('usage', 'a finding id is required (letters, digits, . _ : -)');
  if (typeof reason !== 'string' || reason.trim().length === 0) throw new StateError('usage', 'block waive needs --reason');
  if (typeof file !== 'string' || file.length === 0) throw new StateError('usage', 'block waive needs --file <path>');
  if (path.isAbsolute(file) || file.split('/').includes('..') || file.startsWith('./')) throw new StateError('usage', '--file must be repo-root-relative');
  const record = await readRun(runId);
  if (!record.blocks?.[id]) throw new StateError('unknown_block', `block ${id} is not in run ${runId}`);
  const row = { event: 'review.waived', block: id, file, finding, reason: reason.trim(), by: 'human' };
  let written = null;
  await writeSigned(runId, async (signed) => {
    written = signed;
    return writeRow(signed);
  }, row);
  return /** @type {Record<string, any>} */ (written);
}
