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
 *    (the human fixed it by hand and it was reviewed);
 *  - `unproven`:   a changed owned file of tier `high` (B20, Ricardo 2026-09-25) has no covering
 *    red→green proof (`checkBlockProof`, below). Light-tier files need no proof row.
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
import { tierFor } from '../proof/tiers.mjs';
import { DELETED_HASH, contentHash, gitChildEnv } from '../worker/ticket.mjs';

/**
 * @typedef {{code: 'unreviewed' | 'unsigned' | 'rule_break' | 'late_unruled' | 'review_cap' | 'unproven', file?: string, finding?: string, detail?: string}} Refusal
 */

/** @param {Record<string, any>} row @param {Buffer} key */
const verified = (row, key) => verifyRow(row, key).ok;

/**
 * @param {{
 *   block: string, runId: string, files: Array<{file: string, content_hash: string}>,
 *   rows: Array<Record<string, any>>, key: Buffer, transcript?: string | null,
 *   extraTokens?: string[], proofFiles: Array<{file: string, content_hash: string}>,
 *   highPaths: ReadonlyArray<string>,
 * }} opts `proofFiles` is the set the proof check reads — `--no-require-reviews` empties `files`
 *   but never the proof check; `highPaths` is `proof.tiers.high.paths`. Both are REQUIRED: a
 *   caller that leaves one out gets a TypeError, never a silent default that skips the proof.
 *   `runId` is required too: the proof check matches its rows on this run only (fix round 4).
 * @returns {{ok: boolean, refusals: Refusal[]}}
 * @throws {TypeError} `runId`, `proofFiles` or `highPaths` undefined
 */
export function checkBlockReviews({ block, runId, files, rows, key, transcript = null, extraTokens = [], proofFiles, highPaths }) {
  if (runId === undefined) throw new TypeError('checkBlockReviews: runId is required (the proof check matches rows on this run only)');
  if (proofFiles === undefined) throw new TypeError('checkBlockReviews: proofFiles is required (the changed owned files the proof check reads)');
  if (highPaths === undefined) throw new TypeError('checkBlockReviews: highPaths is required (proof.tiers.high.paths)');
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
  for (const r of checkBlockProof({ runId, files: proofFiles, rows: mine, key, highPaths })) {
    if (!waived(/** @type {string} */ (r.file), PROOF_FINDING)) refusals.push(r);
  }
  return { ok: refusals.length === 0, refusals };
}

/** The finding id a human waives to clear an `unproven` file: `block waive <id> proof --file <path>`. */
export const PROOF_FINDING = 'proof';

/**
 * The file's proof tier at close, from the same inputs `proof tier` uses (§7.1): the config's
 * `proof.tiers.high.paths`, the highest `risk` any row of the block recorded for the file (the
 * worker's `review.plan` rows), and `security_sensitive: true` on any such row. The MAC is checked
 * FIRST and fails closed: when ANY row naming the file does not verify — with or without a `risk`
 * field — the file counts as risk 3.
 * @param {{file: string, rows: Array<Record<string, any>>, key: Buffer, highPaths: ReadonlyArray<string>}} opts
 * @returns {import('../proof/tiers.mjs').Tier}
 */
export function closeTier({ file, rows, key, highPaths }) {
  let risk = 0;
  let securitySensitive = false;
  for (const r of rows) {
    if (r?.file !== file) continue;
    if (!verified(r, key)) {
      risk = 3;
      continue;
    }
    if (typeof r.risk === 'number' && Number.isFinite(r.risk)) risk = Math.max(risk, Math.min(3, Math.max(0, r.risk)));
    if (r.security_sensitive === true) securitySensitive = true;
  }
  return tierFor({ file, risk, securitySensitive, highPaths: [...highPaths] }).tier;
}

/**
 * B20 (Ricardo 2026-09-25): every changed owned file of tier `high` needs a covering red→green
 * proof, checked PER FILE. A file is covered by a `proof` row of this run and block (`rows` must
 * already be the block's rows) with `step: 'red-green'`, `proven: true`, `red_kind: 'assertion'`,
 * whose MAC verifies and whose `covers` list (written by `proof red-green`: the sources the
 * `revert` mechanism put back to base, or the test file itself for `assertion-deletion`) names
 * that file. A deleted file has nothing a test can exercise and is skipped (its review row is
 * still required). Light-tier files need nothing.
 * @param {{runId: string, files: Array<{file: string, content_hash: string}>, rows: Array<Record<string, any>>, key: Buffer, highPaths: ReadonlyArray<string>}} opts
 *   `runId` and `highPaths` are required: a proof row counts only when `r.run === runId` (a row
 *   of another run, or with no `run`, never covers anything), and there is no "no high path" default.
 * @returns {Refusal[]} one `unproven` refusal per uncovered high-tier file.
 * @throws {TypeError} `runId` or `highPaths` undefined
 */
export function checkBlockProof({ runId, files, rows, key, highPaths }) {
  if (runId === undefined) throw new TypeError('checkBlockProof: runId is required (a proof row counts only for its own run)');
  if (highPaths === undefined) throw new TypeError('checkBlockProof: highPaths is required (proof.tiers.high.paths)');
  const high = files.filter((f) => f.content_hash !== DELETED_HASH && closeTier({ file: f.file, rows, key, highPaths }) === 'high');
  if (high.length === 0) return [];
  const proofs = rows.filter(
    (r) =>
      r?.event === 'proof' &&
      r.step === 'red-green' &&
      r.proven === true &&
      r.red_kind === 'assertion' &&
      r.run === runId &&
      Array.isArray(r.covers) &&
      verified(r, key),
  );
  return high
    .filter(({ file }) => !proofs.some((r) => r.covers.includes(file)))
    .map(({ file }) => ({ code: /** @type {const} */ ('unproven'), file, detail: `${file} is high tier and has no proven red→green row covering it` }));
}

/**
 * A file as the gate names it: a repo-relative POSIX path with `./` segments dropped. This is the
 * ONE normalization of the block file set and of a proof row's `covers` (B20), so a path written
 * by `proof red-green` compares equal to the gate's changed-file path. An absolute path, a `..`
 * escape, a backslash or a NUL is refused.
 * @param {string} raw @returns {string}
 * @throws {StateError} `bad-path`
 */
export function repoRelativePath(raw) {
  if (typeof raw !== 'string' || raw.length === 0 || path.posix.isAbsolute(raw) || raw.includes('\\') || raw.includes('\0')) {
    throw new StateError('bad-path', 'a block file must be a repo-relative POSIX path');
  }
  const normalized = path.posix.normalize(raw);
  if (normalized === '.' || normalized === '..' || normalized.startsWith('../')) throw new StateError('bad-path', 'a block file must stay inside the repository');
  return normalized;
}

/**
 * The block's file set (§4.1): changed tracked files since `base` (deletions included, hashed
 * `DELETED_HASH`) ∪ untracked files, filtered to `owned` (exact paths; a glob owned entry matches through `matchOwned`), each with its content hash.
 * Every path goes through `repoRelativePath`.
 * @param {{repoRoot: string, base: string, owned: string[], matchOwned?: (file: string) => boolean}} opts
 * @returns {Promise<Array<{file: string, content_hash: string}>>}
 */
export async function blockFileSet({ repoRoot, base, owned, matchOwned }) {
  if (typeof base !== 'string' || !/^[0-9a-f]{7,64}$/.test(base)) throw new StateError('bad-ref', 'the block base must be a commit id');
  const env = gitChildEnv();
  const changed = await exec(['git', 'diff', '--name-only', '-z', '--no-renames', base, '--'], { cwd: repoRoot, env, timeoutMs: 30000 });
  const untracked = await exec(['git', 'ls-files', '-z', '--others', '--exclude-standard'], { cwd: repoRoot, env, timeoutMs: 30000 });
  if (changed.result !== 'ok' || untracked.result !== 'ok') throw new StateError('git-failed', 'git could not list the block file set');
  const all = new Set(
    [...changed.stdout.split('\0'), ...untracked.stdout.split('\0')].filter((f) => f.length > 0).map((f) => repoRelativePath(f)),
  );
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
