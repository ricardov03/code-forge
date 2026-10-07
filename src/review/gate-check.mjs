/**
 * The block gate's review rows (plan §4.6 rows 2, 9 and 11; §4.9 `block waive`; §4.11; block B12b).
 *
 * `checkBlockReviews` refuses the block when (the file set is the close's: the diff since base ∪
 * every owned file with review rows of the block, {@link reviewedFiles}, owned through
 * {@link ownsPath}):
 *  - `no_changes`: (B52) that set is empty — the block changed no owned file and has no reviews;
 *    a close that checked nothing is never a pass (`--no-require-reviews` is the owner's way out);
 *  - `unowned_change`: (B52) a changed or new file outside the block's `owned_files` that no other
 *    block of the run accounts for ({@link unownedChanges}: it is as it was when the block opened,
 *    another block of the run that is still OPEN owns it — the gate cannot tell which coder wrote
 *    it, so the close WARNs naming each such file — or a closed one owns it and its gate approved
 *    exactly the current content);
 *  - `unreviewed`: a changed or new file has NO `review.approved` row for its CURRENT content hash;
 *  - `unsigned`:   the file's approvals for that hash exist but none carries a valid MAC;
 *  - `not_approved`: (B52) the file's signed approval for its current hash is STALE — a later row of
 *    the block (ledger order) for the same file and hash refuses it ({@link denialOf}: a
 *    `review.result` with `approved: false` whatever its status, except the outcomes that never
 *    reviewed the content, {@link neverReviewed}; a `review.round` with `open_after` not 0; a
 *    `review.cap`; a refusal row with no `content_hash` counts for every hash of its file). One
 *    refusal per open finding the latest such row names, each cleared only by a valid waiver for
 *    the severity that row RECORDS (anything but warning/nit — a critical — only by a signed
 *    `by: human` waiver); a row that names no finding is one `not_approved <file>` that only a
 *    fresh approval clears. An old ledger's `review.result` row has no `approved` field and is
 *    trusted neither way: the ticket's signed result file decides ({@link resultFilesFor}); a
 *    file that is missing or fails its MAC refuses `not_approved <file>`. Refusal rows count
 *    whether or not they are signed (fail closed); approvals only when signed. The check is per
 *    content hash: an approval of the current bytes is not made stale by a review of other bytes
 *    (a file edited and then put back);
 *  - `rule_break`: a `rule_break` row names the block, or the coder transcript hits the forbidden
 *    list (`scanTranscript`; a read of `~/.code-forge/runs/` — the signer key — is the named case);
 *  - `late_unruled`: a `review.late_finding` has neither a signed `review.late_ruling` (the
 *    block-close sweep's L3 ruling) nor a signed `review.waived` row. B52: a ruling clears it only
 *    when it rules `nit` AND the late finding is recorded `warning` or `nit` — a `fix_now` ruling
 *    clears nothing, and a critical (or unrecorded severity) late finding needs a waiver;
 *  - `review_cap`: a file stopped at `review_cap` has an open finding with no signed
 *    `review.waived {by: human}` row — unless a signed approval of the file's current hash comes
 *    AFTER the cap row (ledger order) and no later refusal row made it stale (the human fixed it
 *    by hand and it was reviewed); a finding already refused as `not_approved` for that file is
 *    not repeated;
 *  - `unproven`:   a changed owned file of tier `high` (B20, Ricardo 2026-09-25) has no covering
 *    red→green proof (`checkBlockProof`, below). Light-tier files need no proof row.
 * Rulings and waivers only count when their MAC verifies AND name the finding's file exactly
 * (`file` + `finding`; a finding id is scoped to one file, so a waiver of `F1` in `a.mjs` never
 * clears `F1` in `b.mjs`); a late finding counts whether or not it is signed (fail closed).
 *
 * B47 (autopilot): a `review.waived {by: autopilot}` row (written by `autopilot waive`) clears a
 * finding ONLY when {@link autopilotWaiverProblem} finds nothing wrong with it: its MAC verifies;
 * the finding is not `proof`; the finding's severity recorded in the block's signed review rows
 * (`review.triage`, `review.late_finding`, B52: `review.result.open`; {@link recordedSeverity}) is `warning` or `nit` and
 * equals the waiver's `severity`; a signed `autopilot.grant` row of this run with the waiver's
 * `grant_id` exists, its window (`ts` ≤ waiver `ts` < `until`) holds the waiver, and its scopes
 * include `waive:<severity>`; no `autopilot.stop` / `autopilot.expire` row for that grant is
 * dated at or before the waiver (or undated); and its `decision_id` names a signed, acted
 * `autopilot.decision` of the same grant, scope, block, file and finding made at most 30 min
 * before it ({@link decisionProblem}). Otherwise the waiver is ignored and the finding is refused as
 * `autopilot_waiver_invalid` (the detail says why). `by: human` waivers are unchanged.
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
import { findOverlap } from '../state/registry.mjs';
import { sameStamp, treeStamp } from '../state/block.mjs';
import { verifyRow } from '../state/signer.mjs';
import { tierFor } from '../proof/tiers.mjs';
import { DELETED_HASH, contentHash, gitChildEnv } from '../worker/ticket.mjs';
import { verifyResult } from '../worker/queue.mjs';

/**
 * @typedef {{code: 'no_changes' | 'unowned_change' | 'not_approved' | 'unreviewed' | 'unsigned' | 'rule_break' | 'late_unruled' | 'review_cap' | 'unproven' | 'autopilot_waiver_invalid', file?: string, finding?: string, detail?: string}} Refusal
 */

/** @param {Record<string, any>} row @param {Buffer} key */
const verified = (row, key) => verifyRow(row, key).ok;

/**
 * @param {{
 *   block: string, runId: string, files: Array<{file: string, content_hash: string}>,
 *   rows: Array<Record<string, any>>, key: Buffer, transcript?: string | null,
 *   extraTokens?: string[], proofFiles: Array<{file: string, content_hash: string}>,
 *   highPaths: ReadonlyArray<string>, unowned?: ReadonlyArray<string>, resultFiles?: Record<string, ResultFile>, noChanges?: boolean,
 * }} opts `noChanges` (B52): the block changed no owned file and no owned file has review rows —
 *   refused `no_changes`. `resultFiles` (B52): the signed result files of old-ledger `review.result` rows
 *   ({@link resultFilesFor}; a row whose file is absent here refuses). `unowned` (B52): the
 *   changed files no block accounts for ({@link unownedChanges}),
 *   refused even when `files` is empty. `proofFiles` is the set the proof check reads — `--no-require-reviews` empties `files`
 *   but never the proof check; `highPaths` is `proof.tiers.high.paths`. Both are REQUIRED: a
 *   caller that leaves one out gets a TypeError, never a silent default that skips the proof.
 *   `runId` is required too: the proof check matches its rows on this run only (fix round 4).
 * @returns {{ok: boolean, refusals: Refusal[]}}
 * @throws {TypeError} `runId`, `proofFiles` or `highPaths` undefined
 */
export function checkBlockReviews({ block, runId, files, rows, key, transcript = null, extraTokens = [], proofFiles, highPaths, unowned = [], resultFiles = {}, noChanges = false }) {
  if (runId === undefined) throw new TypeError('checkBlockReviews: runId is required (the proof check matches rows on this run only)');
  if (proofFiles === undefined) throw new TypeError('checkBlockReviews: proofFiles is required (the changed owned files the proof check reads)');
  if (highPaths === undefined) throw new TypeError('checkBlockReviews: highPaths is required (proof.tiers.high.paths)');
  const mine = rows.filter((r) => r?.block === block && (runId === undefined || r.run === undefined || r.run === runId));
  /** @type {Refusal[]} */
  const refusals = [];
  const approvedFor = (/** @type {string} */ file, /** @type {string} */ hash) => mine.filter((r) => r.event === 'review.approved' && r.file === file && r.content_hash === hash);
  const hashOf = new Map(files.map((f) => [f.file, f.content_hash]));
  /**
   * B52: the index of the last signed approval and the latest refusal AFTER it for (file, hash);
   * `denial` is null when that approval stands (or there is none — `unreviewed` / `unsigned` say
   * so). A refusal row with no `content_hash` counts for every hash of its file (fail closed).
   * `latest` is the latest refusal for (file, hash) whether or not an approval exists.
   * @param {string} file @param {string} hash
   * @returns {{lastApproval: number, denial: Denial | null, latest: Denial | null}}
   */
  const latestOf = (file, hash) => {
    let lastApproval = -1;
    /** @type {Denial | null} */
    let denial = null;
    let lastDenial = -1;
    mine.forEach((r, i) => {
      if (r.file !== file) return;
      if (r.event === 'review.approved') {
        if (r.content_hash === hash && verified(r, key)) lastApproval = i;
        return;
      }
      if (r.content_hash !== hash && r.content_hash !== undefined) return;
      const d = denialOf(r, resultFiles);
      if (d !== null) {
        denial = d;
        lastDenial = i;
      }
    });
    return { lastApproval, denial: lastApproval >= 0 && lastDenial > lastApproval ? denial : null, latest: denial };
  };
  const staleBy = (/** @type {string} */ file, /** @type {string} */ hash) => latestOf(file, hash).denial;

  for (const file of unowned) refusals.push({ code: 'unowned_change', file, detail: `${file} changed but no block of the run owns it — block claim it, or put it back` });

  if (noChanges) refusals.push({ code: 'no_changes', detail: `block ${block} changed no owned file and has no reviews` });

  /** @type {Array<{file: string, denial: Denial}>} */
  const stale = [];
  for (const { file, content_hash } of files) {
    const approvals = approvedFor(file, content_hash);
    const signedApproval = approvals.some((r) => verified(r, key));
    if (approvals.length === 0) refusals.push({ code: 'unreviewed', file, detail: `no review.approved row for ${file} at its current content` });
    else if (!signedApproval) refusals.push({ code: 'unsigned', file, detail: `the review.approved row for ${file} has no valid MAC` });
    if (signedApproval) {
      const denial = staleBy(file, content_hash);
      if (denial !== null) stale.push({ file, denial });
    } else {
      // B52: never approved — its latest review's open findings are named too (the file stays
      // refused by `unreviewed` / `unsigned` whatever is waived: only an approval clears that)
      // (a file stopped at the cap is named by the `review_cap` check below instead)
      const latest = latestOf(file, content_hash).latest;
      const capped = mine.some((r) => r.event === 'review.cap' && r.file === file);
      if (latest !== null && latest.open.length > 0 && !capped) stale.push({ file, denial: latest });
    }
  }

  for (const r of mine.filter((row) => row.event === 'rule_break')) refusals.push({ code: 'rule_break', detail: `rule_break row: ${String(r.id ?? r.rule ?? 'unnamed')}` });
  if (typeof transcript === 'string' && transcript.length > 0) {
    for (const hit of scanTranscript(transcript, mergeForbidden(extraTokens))) refusals.push({ code: 'rule_break', detail: `transcript line ${hit.line}: ${hit.id}` });
  }

  const signedOf = (/** @type {string} */ event) => mine.filter((r) => r.event === event && verified(r, key));
  const waivers = signedOf('review.waived').filter((r) => r.by === 'human');
  const autopilotWaivers = mine.filter((r) => r.event === 'review.waived' && r.by === 'autopilot');
  const runRows = rows.filter((r) => r?.run === runId);
  const rulings = signedOf('review.late_ruling');
  const matches = (/** @type {Record<string, any>} */ r, /** @type {string} */ file, /** @type {string} */ finding) => r.finding === finding && r.file === file;
  /**
   * Whether the finding is waived: a signed human waiver, or an autopilot waiver its grant covered.
   * `invalid`: why the autopilot waivers naming it do not count (null when none names it).
   * @param {string} file @param {string} finding @returns {{waived: boolean, invalid: string | null}}
   */
  const waiverFor = (file, finding) => {
    if (waivers.some((r) => matches(r, file, finding))) return { waived: true, invalid: null };
    const named = autopilotWaivers.filter((r) => matches(r, file, finding));
    if (named.length === 0) return { waived: false, invalid: null };
    const problems = named.map((w) => autopilotWaiverProblem(w, { blockRows: mine, runRows, allRows: rows, key }));
    if (problems.some((p) => p === null)) return { waived: true, invalid: null };
    return { waived: false, invalid: /** @type {string} */ (problems[0]) };
  };
  /** @param {Refusal} refusal @param {string} finding @param {string | null} invalid @returns {Refusal} */
  const orInvalid = (refusal, finding, invalid) =>
    invalid === null ? refusal : { code: 'autopilot_waiver_invalid', file: refusal.file, finding, detail: `the autopilot waiver of ${finding} in ${refusal.file} does not count: ${invalid}` };

  /** B52: a ruling clears a late finding only when it rules `nit` on one recorded warning or nit. @param {Record<string, any>} late */
  const ruledNit = (late) => (late.severity === 'warning' || late.severity === 'nit') && rulings.some((r) => matches(r, late.file, late.finding) && r.ruling === 'nit');

  /** @type {Set<string>} `file\0finding` pairs already refused as not_approved */
  const notApproved = new Set();
  for (const { file, denial } of stale) {
    if (denial.open.length === 0) {
      refusals.push({ code: 'not_approved', file, detail: `a later review of ${file}'s current content did not approve it (${denial.event}) and names no finding; review it again` });
      continue;
    }
    for (const { id: finding, severity } of denial.open) {
      notApproved.add(`${file}\0${finding}`);
      const refusal = /** @type {Refusal} */ ({ code: 'not_approved', file, finding, detail: `the latest review of ${file}'s current content left ${finding} open and it is not waived` });
      // the severity the refusal row records wins: anything but warning/nit is the human's alone
      if (severity !== null && !DELEGABLE_SEVERITIES.includes(severity)) {
        if (waivers.some((r) => matches(r, file, finding))) continue;
        const delegated = autopilotWaivers.some((r) => matches(r, file, finding));
        refusals.push(delegated ? { code: 'autopilot_waiver_invalid', file, finding, detail: `the autopilot waiver of ${finding} in ${file} does not count: the latest review records it ${severity}; only the human waives it` } : refusal);
        continue;
      }
      const w = waiverFor(file, finding);
      if (!w.waived) refusals.push(orInvalid(refusal, finding, w.invalid));
    }
  }

  for (const late of mine.filter((r) => r.event === 'review.late_finding')) {
    if (ruledNit(late)) continue;
    const w = waiverFor(late.file, late.finding);
    if (w.waived) continue;
    refusals.push(orInvalid({ code: 'late_unruled', file: late.file, finding: late.finding, detail: `late finding ${late.finding} in ${late.file} has no ruling` }, late.finding, w.invalid));
  }

  for (const cap of mine.filter((r) => r.event === 'review.cap')) {
    const hash = hashOf.get(cap.file);
    // honoured unless a signed approval of the current content comes AFTER the cap and is not stale
    if (hash !== undefined) {
      const latest = latestOf(cap.file, hash);
      if (latest.lastApproval > mine.indexOf(cap) && latest.denial === null) continue;
    }
    for (const finding of Array.isArray(cap.open) ? cap.open : []) {
      if (notApproved.has(`${cap.file}\0${finding}`)) continue;
      const w = waiverFor(cap.file, finding);
      if (!w.waived) refusals.push(orInvalid({ code: 'review_cap', file: cap.file, finding, detail: `${cap.file} stopped at review_cap; finding ${finding} is open and not waived` }, finding, w.invalid));
    }
  }
  for (const r of checkBlockProof({ runId, files: proofFiles, rows: mine, key, highPaths })) {
    const w = waiverFor(/** @type {string} */ (r.file), PROOF_FINDING);
    if (!w.waived) refusals.push(orInvalid(r, PROOF_FINDING, w.invalid));
  }
  return { ok: refusals.length === 0, refusals };
}

/**
 * B52: the `review.result` outcomes that never reviewed the content, and so are no verdict on it,
 * whatever they carry: `stale` (the file moved before the review), `unavailable` (no session
 * answered, or the worker failed) and `refused` with reason `other-run` (the ticket was another
 * run's). Every other status — `reviewed`, `stopped`, `refused` for any other reason,
 * `no_change`, `split_required`, … — is a verdict, and anything but `approved: true` refuses.
 * @param {Record<string, any>} r @returns {boolean}
 */
export const neverReviewed = (r) => r.status === 'stale' || r.status === 'unavailable' || (r.status === 'refused' && r.reason === 'other-run');

/**
 * @typedef {{event: string, open: Array<{id: string, severity: string | null}>}} Denial
 *   a refusal row: the findings it leaves open (empty = it names none: only a fresh approval
 *   clears it) with the severity it records (null = it records none; the triage rows decide).
 * @typedef {{status: unknown, approved: unknown, findings: unknown} | null} ResultFile
 *   a ticket's signed result file as the gate read it — null when it is missing or its MAC fails.
 */

/**
 * B52: what a row says about a review of its (file, content_hash), or null when it is no refusal:
 *  - `review.result` (the worker's): no verdict when {@link neverReviewed} — those outcomes never
 *    reviewed the content, so they neither approve nor refuse it. `approved: true` with no
 *    critical in its `open` is no refusal (the approval itself is the `review.approved` row);
 *    `approved: true` whose `open` names a critical, and `approved: false`, refuse with every
 *    finding of `open`. A row WITHOUT `approved` (an old ledger) cannot be trusted either way: the
 *    ticket's signed result file (`resultFiles[ticket]`, read and MAC-checked by
 *    {@link resultFilesFor}) decides by the same rule. In particular a file that says `approved:
 *    true` and lists no critical is no refusal: that is the fix loop's normal approval — the
 *    remaining warnings and nits were accepted by the reviewer and triage (a critical never is,
 *    `triage.mjs`). A file that is missing, fails its MAC or names another status refuses with no
 *    finding (fail closed);
 *  - `review.round` (the fix loop's): `open_after` not 0, with no finding named;
 *  - `review.cap`: the file stopped; its `open` ids (severity from the triage rows).
 * @param {Record<string, any>} r @param {Record<string, ResultFile>} [resultFiles]
 * @returns {Denial | null}
 */
export function denialOf(r, resultFiles = {}) {
  if (r?.event === 'review.result') {
    if (neverReviewed(r)) return null;
    /** @param {unknown} approved @param {Array<{id: string, severity: string}>} open @returns {Denial | null} */
    const judge = (approved, open) => (approved === true && !open.some((f) => f.severity === 'critical') ? null : { event: r.event, open });
    if (r.approved === true || r.approved === false) return judge(r.approved, normalizeOpen(r.open));
    const file = typeof r.ticket === 'string' && Object.hasOwn(resultFiles, r.ticket) ? resultFiles[r.ticket] : null;
    if (!file || file.status !== r.status) return { event: r.event, open: [] };
    return judge(file.approved, openFindings(file.findings));
  }
  if (r?.event === 'review.round') return r.open_after === 0 ? null : { event: r.event, open: [] };
  if (r?.event === 'review.cap') {
    const ids = Array.isArray(r.open) ? r.open.filter((/** @type {unknown} */ id) => typeof id === 'string' && id.length > 0) : [];
    return { event: r.event, open: [...new Set(/** @type {string[]} */ (ids))].map((id) => ({ id, severity: null })) };
  }
  return null;
}

/**
 * B52: a ledger row's `open` list as `{id, severity}` — an entry without a string id is kept
 * under a synthetic id by position (`X<n>`) and an unknown severity as `unknown`, so nothing
 * vanishes; repeated ids keep the worst severity.
 * @param {unknown} open @returns {Array<{id: string, severity: string}>}
 */
function normalizeOpen(open) {
  return Array.isArray(open) ? openFindings(open) : [];
}

/** The finding id form `block waive` accepts. */
const FINDING_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;
/** B52: severity order for {@link openFindings}; an unknown severity ranks above critical (fail closed). */
const OPEN_RANK = Object.freeze({ nit: 0, warning: 1, critical: 2, unknown: 3 });

/**
 * B52: the open findings of a review as `{id, severity}`, for the ledger's `review.result` row
 * (the worker) and the gate (`not_approved <file> <id>`). Nothing vanishes: a finding with a
 * missing or malformed id (not the form `block waive` takes) is kept as `X<n>`, n its 1-based
 * position in the list; a severity outside critical/warning/nit is `unknown` (only the human
 * waives it). An id given more than once keeps its WORST severity (unknown > critical > warning >
 * nit), at its first position.
 * @param {unknown} findings @returns {Array<{id: string, severity: string}>}
 */
export function openFindings(findings) {
  if (!Array.isArray(findings)) return [];
  /** @type {Map<string, string>} */
  const out = new Map();
  /** @type {Record<string, number>} */
  const rank = OPEN_RANK;
  findings.forEach((f, i) => {
    const id = typeof f?.id === 'string' && FINDING_ID.test(f.id) ? f.id : `X${i + 1}`;
    const severity = typeof f?.severity === 'string' && Object.hasOwn(rank, f.severity) && f.severity !== 'unknown' ? f.severity : 'unknown';
    const had = out.get(id);
    if (had === undefined || rank[severity] > rank[had]) out.set(id, severity);
  });
  return [...out].map(([id, severity]) => ({ id, severity }));
}

/**
 * B52: the signed result files {@link denialOf} needs — one per `review.result` row of the block
 * that has no `approved` field (an old ledger) and is a verdict — keyed by ticket. A file that is
 * missing, unparsable, names another run or ticket, or fails its MAC is null.
 * @param {{repoRoot: string, runId: string, block: string, rows: Array<Record<string, any>>}} opts
 *   `repoRoot`: the repository root the worker writes `.code-forge/reviews/` under.
 * @returns {Promise<Record<string, ResultFile>>}
 */
export async function resultFilesFor({ repoRoot, runId, block, rows }) {
  /** @type {Record<string, ResultFile>} */
  const out = {};
  for (const r of rows) {
    if (r?.event !== 'review.result' || r.block !== block || Object.hasOwn(r, 'approved') || neverReviewed(r) || typeof r.ticket !== 'string' || Object.hasOwn(out, r.ticket)) continue;
    try {
      const checked = await verifyResult(repoRoot, runId, r.ticket);
      out[r.ticket] = checked.ok && checked.result ? { status: checked.result.status, approved: checked.result.approved, findings: checked.result.findings } : null;
    } catch {
      out[r.ticket] = null; // a malformed ticket id, an unreadable file: fail closed
    }
  }
  return out;
}

/** The finding severities autopilot may waive (B47); anything else is never delegated. */
export const DELEGABLE_SEVERITIES = Object.freeze(['warning', 'nit']);

/** Severity order for {@link recordedSeverity}: the worst recorded one wins. */
const SEVERITY_RANK = Object.freeze({ nit: 0, warning: 1, critical: 2, blocker: 3 });

/**
 * The severity the block's signed review rows record for one finding of one file (B47): every
 * `review.triage` and `review.late_finding` row with that `block`, `file` and `finding`, and (B52)
 * every `review.result` row whose `open` list names it (with the severity recorded there); the worst
 * one wins. Fails closed: no row ⇒ `not-recorded`; any such row whose MAC does not verify ⇒
 * `unverified`; a severity outside critical/blocker/warning/nit ⇒ `unknown` — each with
 * `severity: null`.
 * @param {Array<Record<string, any>>} rows - rows of ONE run (other runs filtered out by the caller).
 * @param {{block: string, file: string, finding: string, key: Buffer}} where
 * @returns {{severity: string, why: null} | {severity: null, why: 'not-recorded' | 'unverified' | 'unknown'}}
 */
export function recordedSeverity(rows, { block, file, finding, key }) {
  /** @type {Array<{row: Record<string, any>, severity: unknown}>} */
  const named = [];
  for (const r of rows) {
    if (r?.block !== block || r.file !== file) continue;
    if ((r.event === 'review.triage' || r.event === 'review.late_finding') && r.finding === finding) named.push({ row: r, severity: r.severity });
    // B52: the worker's review.result rows record the open findings' severities too
    else if (r.event === 'review.result' && Array.isArray(r.open)) {
      for (const o of r.open) if (o?.id === finding) named.push({ row: r, severity: o.severity });
    }
  }
  if (named.length === 0) return { severity: null, why: 'not-recorded' };
  if (named.some((n) => !verified(n.row, key))) return { severity: null, why: 'unverified' };
  /** @type {Record<string, number>} */
  const rank = SEVERITY_RANK;
  let worst = '';
  for (const { severity } of named) {
    if (typeof severity !== 'string' || !Object.hasOwn(rank, severity)) return { severity: null, why: 'unknown' };
    if (worst === '' || rank[severity] > rank[worst]) worst = severity;
  }
  return { severity: worst, why: null };
}

/**
 * Why a `review.waived {by: autopilot}` row does NOT clear its finding, or null when it does (the
 * rules are in the module doc). `blockRows`: the block's rows of this run (the review rows the
 * severity is read from); `runRows`: every row of this run (grant, stop and expire rows carry no
 * block); `allRows`: every row given (stop and expire rows are matched by grant id across all of
 * them, whatever their `run`). A stop or expire row counts whether or not it is signed (fail closed).
 * @param {Record<string, any>} waiver
 * @param {{blockRows: Array<Record<string, any>>, runRows: Array<Record<string, any>>, allRows?: Array<Record<string, any>>, key: Buffer}} ctx
 * @returns {string | null}
 */
export function autopilotWaiverProblem(waiver, { blockRows, runRows, allRows = runRows, key }) {
  if (!verified(waiver, key)) return 'the waiver has no valid MAC';
  if (waiver.finding === PROOF_FINDING) return 'proof is never delegated';
  const recorded = recordedSeverity(blockRows, { block: waiver.block, file: waiver.file, finding: waiver.finding, key });
  if (recorded.severity === null) return `the finding's severity is ${recorded.why}; critical findings are never delegated`;
  if (!DELEGABLE_SEVERITIES.includes(recorded.severity)) return `the finding is ${recorded.severity}; critical findings are never delegated`;
  if (waiver.severity !== recorded.severity) return `the waiver says ${String(waiver.severity)} but the finding is recorded as ${recorded.severity}`;
  const at = typeof waiver.ts === 'string' ? Date.parse(waiver.ts) : Number.NaN;
  if (!Number.isFinite(at)) return 'the waiver has no time';
  const scope = `waive:${recorded.severity}`;
  return (
    grantCoverProblem(runRows, { grantId: waiver.grant_id, scope, at, key, what: 'the waiver', endRows: allRows }) ??
    decisionProblem(runRows, { decisionId: waiver.decision_id, grantId: waiver.grant_id, scope, subject: { block: waiver.block, file: waiver.file, finding: waiver.finding }, decision: 'waive', at, key })
  );
}

/** A delegate decision allows an action for at most this long after it was made (B47). */
export const DECISION_MAX_AGE_MS = 30 * 60 * 1000;

/**
 * Why grant `grantId` did NOT cover `scope` at time `at` (epoch ms), or null when it did (B47;
 * shared by the waiver check and the fix loop's extra rounds): a signed `autopilot.grant` row of
 * the run with that id, whose window (`ts` ≤ at < `until`) holds `at`, whose scopes include the
 * scope and whose deny list does not, and no `autopilot.stop` / `autopilot.expire` row for it at
 * or before `at`. A stop or expire row counts whether or not it is signed, and one with a missing
 * or unparsable `ts` counts as having stopped the grant (fail closed). Stop and expire rows are
 * looked for in `endRows` (default `runRows`) by grant id alone, whatever their `run`.
 * @param {Array<Record<string, any>>} runRows - every row of the run.
 * @param {{grantId: unknown, scope: string, at: number, key: Buffer, what?: string, endRows?: Array<Record<string, any>>}} q - `what` names the action in the message.
 * @returns {string | null}
 */
export function grantCoverProblem(runRows, { grantId, scope, at, key, what = 'the action', endRows = runRows }) {
  const id = grantId;
  const grant = runRows.find((r) => r?.event === 'autopilot.grant' && typeof id === 'string' && r.grant_id === id && verified(r, key));
  if (!grant) return `no signed autopilot.grant row for grant ${String(id)} in this run`;
  const from = Date.parse(grant.ts);
  const until = Date.parse(grant.until);
  if (!(Number.isFinite(from) && Number.isFinite(until) && at >= from && at < until)) return `${what} is outside grant ${id}'s window`;
  if (!Array.isArray(grant.scopes) || !grant.scopes.includes(scope) || (Array.isArray(grant.deny) && grant.deny.includes(scope))) return `grant ${id} does not allow ${scope}`;
  const ended = endRows.find((r) => {
    if ((r?.event !== 'autopilot.stop' && r?.event !== 'autopilot.expire') || r.grant_id !== id) return false;
    const ts = typeof r.ts === 'string' ? Date.parse(r.ts) : Number.NaN;
    return !Number.isFinite(ts) || ts <= at;
  });
  if (ended) return `grant ${id} was ${ended.event === 'autopilot.stop' ? 'stopped' : 'expired'} before ${what}`;
  return null;
}

/**
 * Why decision `decisionId` does NOT allow the action, or null when it does (B47): a signed
 * `autopilot.decision` row of the run with that `decision_id`, `acted: true`, the same grant and
 * scope, the fixed option word the action needs as its `decision` (`waive` for a waiver, `allow`
 * for an extra round, the level itself — `L0`/`L1`/`L2` — for a coder level; so an acted "fix" or
 * "deny" never authorises anything), a `subject` naming the same block (and file / finding when
 * the action has them), made at or before `at` and at most {@link DECISION_MAX_AGE_MS} before it.
 * @param {Array<Record<string, any>>} runRows
 * @param {{decisionId: unknown, grantId: unknown, scope: string, subject: {block: string, file?: string, finding?: string}, decision: string, at: number, key: Buffer}} q
 * @returns {string | null}
 */
export function decisionProblem(runRows, { decisionId, grantId, scope, subject, decision, at, key }) {
  if (typeof decisionId !== 'string' || decisionId.length === 0) return 'no delegate decision named';
  const row = runRows.find((r) => r?.event === 'autopilot.decision' && r.decision_id === decisionId);
  if (!row) return `no autopilot.decision row ${decisionId} in this run`;
  if (!verified(row, key)) return `decision ${decisionId} has no valid MAC`;
  if (row.acted !== true) return `decision ${decisionId} did not act (it went to the owner)`;
  if (row.grant_id !== grantId) return `decision ${decisionId} is under another grant`;
  if (row.scope !== scope) return `decision ${decisionId} is for ${String(row.scope)}, not ${scope}`;
  if (row.decision !== decision) return `decision ${decisionId} decided ${JSON.stringify(row.decision)}, not ${JSON.stringify(decision)}`;
  const named = row.subject && typeof row.subject === 'object' ? row.subject : {};
  for (const k of /** @type {Array<'block' | 'file' | 'finding'>} */ (['block', 'file', 'finding'])) {
    if (subject[k] !== undefined && named[k] !== subject[k]) return `decision ${decisionId} is about another ${k}`;
  }
  const ts = typeof row.ts === 'string' ? Date.parse(row.ts) : Number.NaN;
  if (!Number.isFinite(ts) || ts > at || at - ts > DECISION_MAX_AGE_MS) return `decision ${decisionId} is older than 30 min (or not yet made)`;
  return null;
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
 * @param {{repoRoot: string, base: string, owned: string[], matchOwned?: (file: string) => boolean, changed?: string[]}} opts
 *   `changed`: the tree's changed files when the caller already listed them ({@link changedFiles}).
 * @returns {Promise<Array<{file: string, content_hash: string}>>}
 */
export async function blockFileSet({ repoRoot, base, owned, matchOwned, changed }) {
  const all = changed ?? (await changedFiles({ repoRoot, base }));
  const isOwned = matchOwned ?? ((/** @type {string} */ f) => owned.includes(f));
  return all.filter((f) => isOwned(f)).map((file) => ({ file, content_hash: currentHash(repoRoot, file) }));
}

/**
 * Every changed tracked file since `base` (deletions included) ∪ every untracked, not ignored
 * file, through `repoRelativePath`, sorted — the set {@link blockFileSet} filters to the block.
 * @param {{repoRoot: string, base: string}} opts
 * @returns {Promise<string[]>}
 * @throws {StateError} `bad-ref`, `git-failed`
 */
export async function changedFiles({ repoRoot, base }) {
  if (typeof base !== 'string' || !/^[0-9a-f]{7,64}$/.test(base)) throw new StateError('bad-ref', 'the block base must be a commit id');
  const env = gitChildEnv();
  const changed = await exec(['git', 'diff', '--name-only', '-z', '--no-renames', base, '--'], { cwd: repoRoot, env, timeoutMs: 30000 });
  const untracked = await exec(['git', 'ls-files', '-z', '--others', '--exclude-standard'], { cwd: repoRoot, env, timeoutMs: 30000 });
  if (changed.result !== 'ok' || untracked.result !== 'ok') throw new StateError('git-failed', 'git could not list the block file set');
  const all = new Set(
    [...changed.stdout.split('\0'), ...untracked.stdout.split('\0')].filter((f) => f.length > 0).map((f) => repoRelativePath(f)),
  );
  return [...all].sort();
}

/**
 * B52: whether `owned` (a block's `owned_files`) covers `file` at the gate: the `findOverlap`
 * match `block open` uses (exact paths and globs), and — what the field run needed — an entry with
 * no glob character also covers every path below it as a directory (`src/feature` owns
 * `src/feature/Fan.swift`). A wider match only ever puts more files under the gate's checks.
 * @param {ReadonlyArray<string>} owned @param {string} file @returns {boolean}
 */
export function ownsPath(owned, file) {
  if (findOverlap(owned, [file]) !== null) return true;
  return owned.some((o) => typeof o === 'string' && o.length > 0 && !/[*?[\]{}]/.test(o) && file.startsWith(`${o.replace(/\/+$/, '')}/`));
}

/**
 * B52: {@link ownsPath} for the close's one path base, the repository top level: `file` is
 * repo-root-relative (as git lists it and the worker signs it); an owned entry matches it as
 * given, or — for a workspace in a subdirectory (`wsPrefix`, the workspace relative to the top;
 * `''` when they are the same) — relative to the workspace. A file outside the workspace is
 * matched as given only.
 * @param {string} wsPrefix @returns {(owned: ReadonlyArray<string>, file: string) => boolean}
 */
export function ownsAt(wsPrefix) {
  const prefix = wsPrefix.replace(/\/+$/, '');
  return (owned, file) => ownsPath(owned, file) || (prefix !== '' && prefix !== '.' && file.startsWith(`${prefix}/`) && ownsPath(owned, file.slice(prefix.length + 1)));
}

/** B52: the review rows that name a file the block's worker reviewed. */
const REVIEW_EVENTS = new Set(['review.result', 'review.round', 'review.approved', 'review.triage', 'review.cap', 'review.late_finding']);

/**
 * B52: every file with ANY review row (`review.result`, `review.round`, `review.approved`,
 * `review.triage`, `review.cap`, `review.late_finding`) for `block` among the run's `rows`,
 * through `repoRelativePath` (a path it
 * refuses is left out), sorted. The close adds them to the diff's file set, so work the diff
 * cannot see — committed before `block open`, done in another worktree — is still checked.
 * @param {Array<Record<string, any>>} rows @param {string} block @returns {string[]}
 */
export function reviewedFiles(rows, block) {
  /** @type {Set<string>} */
  const out = new Set();
  for (const r of rows) {
    if (r?.block !== block || !REVIEW_EVENTS.has(r.event) || typeof r.file !== 'string') continue;
    try {
      out.add(repoRelativePath(r.file));
    } catch {
      // not a repo path: nothing on disk to check it against
    }
  }
  return [...out].sort();
}

/** @param {string} repoRoot @param {string} file @returns {string} the content hash, `DELETED_HASH` when gone. */
const currentHash = (repoRoot, file) => (isOnDisk(repoRoot, file) ? contentHash(repoRoot, file) : DELETED_HASH);

/**
 * B52: the changed files of the tree (`changed`, from {@link changedFiles} run at the repository
 * top level `top`, so top-relative) that NO block of the run accounts for — each one is an
 * `unowned_change` refusal at `block close`. A file is accounted for when:
 *  - this block owns it (`isMine`, the close's own `owned_files` match);
 *  - it is exactly as it was when this block opened: its `tree_at_open` stamp (`treeStamp`) is
 *    the same now (`sameStamp`; an `unreadable` stamp at open or now never is) — a change made
 *    before the coder started (plan files, `init`'s config, a skill link) is not the coder's; any
 *    later edit to it is;
 *  - another block of the run that is still OPEN owns it (parallel coders share the tree and its
 *    own gate reviews it). The gate cannot tell which coder wrote it, so such files are returned
 *    in `siblings` and the close WARNs naming each one;
 *  - a CLOSED block of the run owns it AND a signed `review.approved` row of that block names the
 *    file at its current content hash (that block's work, exactly as its gate approved it — a
 *    later edit makes it unowned again);
 *  - it is code-forge's own state: under `.code-forge/` at the top level or under `stateDir` (the
 *    workspace's `.code-forge/`, top-relative). Nothing else is exempt — not the close's own
 *    `--transcript` or `--report` when they sit in the source tree.
 * A stopped block accounts for nothing. Paths match through {@link ownsPath} (globs as at `block
 * open`). A stamp that throws counts as not accounted (refused). A block opened before B52 has no
 * `tree_at_open`: null — nothing can tell the coder's changes from earlier ones, so the caller
 * warns that the check did not run (never a silent pass).
 * @param {{
 *   top: string, changed: ReadonlyArray<string>, id: string, blocks: Record<string, any>,
 *   rows: Array<Record<string, any>>, key: Buffer, isMine: (file: string) => boolean, owns?: (owned: ReadonlyArray<string>, file: string) => boolean, stateDir?: string,
 * }} opts `blocks`: the run record's `blocks`; `rows`: the run's ledger rows; `owns` (default {@link ownsPath}): how another block's `owned_files` match a path.
 * @returns {Promise<{unowned: string[], siblings: Array<{file: string, block: string}>} | null>}
 */
export async function unownedChanges({ top, changed, id, blocks, rows, key, isMine, owns = ownsPath, stateDir = '.code-forge' }) {
  const atOpen = blocks?.[id]?.tree_at_open;
  if (!atOpen || typeof atOpen !== 'object' || Array.isArray(atOpen)) return null;
  const others = Object.entries(blocks ?? {}).filter(([other, b]) => other !== id && b && Array.isArray(b.owned_files));
  /** @param {string} file @returns {boolean} */
  const approvedClosed = (file) => {
    let hash;
    try {
      hash = currentHash(top, file);
    } catch {
      return false; // a symlink or an unreadable path was never approved
    }
    return others.some(
      ([other, b]) =>
        b.status === 'closed' &&
        owns(b.owned_files, file) &&
        rows.some((r) => r?.event === 'review.approved' && r.block === other && r.file === file && r.content_hash === hash && verified(r, key)),
    );
  };
  /** @param {string} file @returns {Promise<boolean>} */
  const unchangedSinceOpen = async (file) => {
    if (!Object.hasOwn(atOpen, file)) return false;
    try {
      return sameStamp(atOpen[file], await treeStamp(top, file));
    } catch {
      return false;
    }
  };
  const state = (/** @type {string} */ f) => [...new Set(['.code-forge', stateDir])].some((dir) => f === dir || f.startsWith(`${dir}/`));
  /** @type {string[]} */
  const unowned = [];
  /** @type {Array<{file: string, block: string}>} */
  const siblings = [];
  for (const file of changed) {
    if (state(file) || isMine(file) || (await unchangedSinceOpen(file))) continue;
    const sibling = others.find(([, b]) => b.status === 'open' && owns(b.owned_files, file));
    if (sibling) siblings.push({ file, block: sibling[0] });
    else if (!approvedClosed(file)) unowned.push(file);
  }
  return { unowned, siblings };
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
