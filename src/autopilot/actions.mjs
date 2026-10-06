/**
 * Delegated actions (issue #5, plan autopilot §1, block B47): what autopilot may do for the owner
 * while a grant covers it. Each action asks `grantFor(run, scope, now)` (B45) first; a refusal
 * sends the question back to the owner and writes nothing.
 *
 *  - {@link waiveForOwner} (`autopilot waive`, scope `waive:<severity>`): a signed
 *    `review.waived` row with `by: 'autopilot'`, `grant_id`, `severity` and `issue`. Only for a
 *    finding the block's SIGNED review rows record as `warning` or `nit` (`recordedSeverity`, the
 *    same function the gate uses — the `--severity` flag is never trusted alone): a finding
 *    recorded as critical/blocker, with no recorded severity, or with an unverifiable row is
 *    refused ("critical findings are never delegated"); `proof` is never delegable.
 *    Each waiver opens ONE tracked issue in the PROJECT's GitHub repo (`gh repo view` in the
 *    run's workspace — never this package's repo), deduped by a body marker
 *    {@link waiverMarker}: an issue that already holds it gets a comment instead. The body is
 *    scrubbed; a secret-shaped text left in it blocks the issue (not the waiver). No `gh`, not
 *    signed in, no repo, a failed call → the waiver still stands with `issue: null`, plus one
 *    signed `autopilot.issue_failed` row naming why (a code, never gh's output). Never asks.
 *  - {@link grantExtraRound} (`autopilot round`, scope `round:extra`): one signed
 *    `autopilot.extra_round` row — ONE fix round past the file's cap per grant (the fix loop and
 *    the worker read it through `extraRoundsFor`); a second request for the file under the same
 *    grant is refused.
 *  - {@link chooseCoderLevel} (`autopilot level`, scope `model:choose`): the block's coder level
 *    = its recorded Jev lane (`recordedLanes`, the rows `jev ask lane --block` writes), capped at
 *    the plan's level + 1 and never above L2 (L3 is never a coding level); one signed
 *    `autopilot.level` row. `block open` without `--level` takes the latest such row.
 *
 * The delegate decides (issue #5): every action needs `--decision <id>`, a signed
 * `autopilot.decision` row (B46, `autopilot ask`) with `acted: true`, the same grant and scope,
 * a `subject` naming the same block (+ file + finding for waive, + file for round), made at most
 * 30 min before, whose `decision` is the fixed option word of the action — `waive` (waive),
 * `allow` (round), the level being set `L0`/`L1`/`L2` (level) — so the orchestrator asks with
 * `--options waive,fix`, `--options allow,deny` or `--options L1,L2` (`decisionProblem` in
 * `review/gate-check.mjs`, the check the gate and the fix loop use too). The action row records
 * the `decision_id`.
 *
 * Stop race: every action checks the grant (`grantFor`) and the decision first, then — for the
 * write — takes the run lock, reads the run record again with a FRESH clock and re-checks the
 * grant ({@link liveGrantRefusal}: same grant, run active, not stopped or expired, scope allowed)
 * and the decision, and writes its row inside that lock dated with that fresh time. A grant
 * stopped or expired in between (e.g. while gh ran) means no row; a waiver issue already created
 * then gets one best-effort comment "waiver cancelled: autopilot grant ended".
 *
 * The per-finding lock is in-process only (`keyedLock`); two separate processes waiving the same
 * finding at once rely on the marker search alone (accepted, B47 fix round 1).
 *
 * Tracked issues: the dedupe searches the exact quoted marker with up to 100 results and confirms
 * it in the bodies; a full page without it is `search-failed` (no new issue). Two waives of one
 * finding in this process run one at a time (`keyedLock`), so they open one issue.
 */

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { DELEGABLE_SEVERITIES, decisionProblem, PROOF_FINDING, recordedSeverity, repoRelativePath } from '../review/gate-check.mjs';
import { parsePlan, recordedLanes } from '../session/plan-check.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun, withRunLock, writeSigned } from '../state/run.mjs';
import { loadKey, verifyRow } from '../state/signer.mjs';
import { exec as realExec } from '../util/exec.mjs';
import { findReported, findSecret, ghRun, ghWithBodyFile, pathLookup } from '../util/gh.mjs';
import { keyedLock } from '../util/locks.mjs';
import { scrub } from './delegate.mjs';
import { grantFor, grantState } from './grant.mjs';
import { isFixedDeny } from './scopes.mjs';

/** @typedef {(row: Record<string, any>) => Promise<unknown>} WriteRow */
/**
 * @typedef {object} ActionDeps
 * @property {() => Date} [now] - the clock.
 * @property {WriteRow} [writeRow] - the ledger writer (default: the run's project ledger).
 * @property {() => Promise<Array<Record<string, any>>>} [readRows] - the project's ledger rows (default: `readAllRows`).
 * @property {typeof realExec} [exec] - runs `gh` (tests: a fake).
 * @property {NodeJS.ProcessEnv} [env]
 * @property {(cmd: string) => boolean} [onPath] - whether `cmd` is on PATH.
 */
/** @typedef {{ok: true, row: Record<string, any>} | {ok: false, reason: string, message: string}} ActionResult */

/** The label a waiver issue asks for (retried without it when the repo has no such label). */
export const WAIVER_LABEL = 'autopilot-waiver';

/** A finding id, as `block waive` accepts it. */
const FINDING_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;

/** The refusal text for anything above warning. */
export const NEVER_CRITICAL = 'critical findings are never delegated';

/**
 * The body marker an issue for one waived finding carries; the dedupe searches for it.
 * @param {string} runId @param {string} block @param {string} file @param {string} finding
 * @returns {string}
 */
export const waiverMarker = (runId, block, file, finding) => `<!-- code-forge-waiver: ${runId}/${block}/${file}/${finding} -->`;

/** @param {Record<string, any>} record @param {ActionDeps} deps @returns {WriteRow} */
const writerFor = (record, deps) => deps.writeRow ?? ((row) => appendRow(row, { slug: record.project }));

/**
 * The run's rows (other runs and unparsable rows dropped).
 * @param {Record<string, any>} record @param {ActionDeps} deps @returns {Promise<Array<Record<string, any>>>}
 */
async function runRowsOf(record, deps) {
  const all = deps.readRows ? await deps.readRows() : await readAllRows(record.project);
  return all.filter((r) => r?.run === record.run_id);
}

/**
 * Sign and write `row`; the signed row is returned.
 * @param {string} runId @param {WriteRow} write @param {Record<string, any>} row
 * @returns {Promise<Record<string, any>>}
 */
async function signed(runId, write, row) {
  /** @type {Record<string, any> | null} */
  let out = null;
  await writeSigned(runId, async (r) => {
    out = r;
    return write(r);
  }, row);
  return /** @type {Record<string, any>} */ (out);
}

/** @param {Record<string, any>} record @param {string} block */
function assertBlock(record, block) {
  if (typeof block !== 'string' || !record.blocks?.[block]) throw new StateError('unknown_block', `block ${block} is not in run ${record.run_id}`);
}

/** @param {string} reason @returns {string} */
const grantRefusal = (reason) => `the grant refuses (${reason}); the question goes to the owner`;

/**
 * The waiver issue text (already scrubbed). `marker` ends the body.
 * @param {{runId: string, block: string, file: string, finding: string, severity: string, reason: string, grantId: string, ts: string}} w
 * @returns {{title: string, body: string, comment: string, marker: string}}
 */
export function waiverIssueText(w) {
  const marker = waiverMarker(w.runId, w.block, w.file, w.finding);
  const reason = scrub(w.reason).replace(/\r?\n/g, ' ');
  const title = scrub(`code-forge autopilot waived ${w.severity} ${w.finding} in ${w.file} (block ${w.block})`);
  const body = [
    `code-forge autopilot waived a ${w.severity} review finding while the owner was away. It still needs a follow-up.`,
    '',
    '| field | value |',
    '|---|---|',
    `| run | ${w.runId} |`,
    `| block | ${w.block} |`,
    `| file | \`${w.file}\` |`,
    `| finding | ${w.finding} |`,
    `| severity | ${w.severity} |`,
    `| grant | ${w.grantId} |`,
    `| waived at | ${w.ts} |`,
    '',
    `Reason: ${reason}`,
    '',
    marker,
    '',
  ].join('\n');
  const comment = `Waived again by code-forge autopilot at ${w.ts} (grant ${w.grantId}). Reason: ${reason}\n\n${marker}\n`;
  return { title, body: scrub(body), comment: scrub(comment), marker };
}

/** The only `autopilot.issue_failed` reasons; anything else is written as `gh-failed`. */
export const ISSUE_FAILURES = Object.freeze(['gh-missing', 'gh-not-logged-in', 'no-project-repo', 'secret-in-body', 'search-failed', 'comment-failed', 'create-failed', 'gh-failed']);

/** At most this many search results are read for the dedupe (a full page with no exact match is `search-failed`). */
export const DEDUPE_LIMIT = 100;

/**
 * Open (or comment on) the tracked issue for a waiver. Never throws; never asks. The failure is a
 * fixed code from {@link ISSUE_FAILURES} — never gh's output.
 * @param {{workspace: string, text: ReturnType<typeof waiverIssueText>}} req
 * @param {ActionDeps} deps
 * @returns {Promise<{url: string, action: 'created' | 'commented'} | {url: null, failure: string}>}
 */
export async function trackWaiverIssue({ workspace, text }, deps) {
  /** @param {string} code @returns {{url: null, failure: string}} */
  const failed = (code) => ({ url: null, failure: ISSUE_FAILURES.includes(code) ? code : 'gh-failed' });
  try {
    const env = deps.env ?? process.env;
    const exec = deps.exec ?? realExec;
    const onPath = deps.onPath ?? pathLookup(env.PATH ?? '');
    if (!onPath('gh')) return failed('gh-missing');
    if ((await ghRun(exec, ['gh', 'auth', 'status'], env)).result !== 'ok') return failed('gh-not-logged-in');
    const view = await ghRun(exec, ['gh', 'repo', 'view', '--json', 'nameWithOwner'], env, { cwd: workspace });
    if (view.result !== 'ok') return failed('no-project-repo');
    /** @type {string | null} */
    let repo = null;
    try {
      const name = JSON.parse(view.stdout)?.nameWithOwner;
      repo = typeof name === 'string' && /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(name) ? name : null;
    } catch {
      repo = null;
    }
    if (repo === null) return failed('no-project-repo');
    if (findSecret(`${text.title}\n${text.body}\n${text.comment}`) !== null) return failed('secret-in-body');
    // the exact marker, quoted, and up to 100 results; the bodies are checked for the exact marker
    const search = await findReported(exec, env, repo, `"${text.marker}"`, { match: text.marker, limit: DEDUPE_LIMIT });
    if ('why' in search) return failed('search-failed');
    const target = repo;
    if (search.hit !== null) {
      const dup = search.hit;
      const why = await ghWithBodyFile(exec, env, text.comment, (file) => [['gh', 'issue', 'comment', String(dup.number), '--repo', target, '--body-file', file]]);
      return why === null ? { url: dup.url, action: 'commented' } : failed('comment-failed');
    }
    /** @type {string | null} */
    let url = null;
    const why = await ghWithBodyFile(
      exec,
      env,
      text.body,
      (file) => {
        const base = ['gh', 'issue', 'create', '--repo', target, '--title', text.title, '--body-file', file];
        return [[...base, '--label', WAIVER_LABEL], base];
      },
      (stdout) => {
        url = stdout.split('\n').map((l) => l.trim()).filter((l) => /^https:\/\/\S+$/.test(l)).pop() ?? null;
      },
    );
    if (why !== null || url === null) return failed('create-failed');
    return { url, action: 'created' };
  } catch {
    return failed('gh-failed');
  }
}

/**
 * The delegate-decision check for an action (`decisionProblem` over the run's rows), as a
 * refusal, or null when the decision allows it.
 * @param {Array<Record<string, any>>} rows @param {Parameters<typeof decisionProblem>[1]} q
 * @returns {{ok: false, reason: string, message: string} | null}
 */
function decisionRefusal(rows, q) {
  const problem = decisionProblem(rows, q);
  return problem === null ? null : { ok: false, reason: 'no-decision', message: `${problem}; the question goes to the owner (autopilot ask, then --decision <id>)` };
}

/**
 * The grant re-check made INSIDE the run lock (where `grantFor` cannot run: its lazy expiry takes
 * the same lock). Reads the run record as it is now: the run must be active and its grant the same
 * one, still active at `now` (not stopped, not past `until`), allowing `scope`. An expired grant's
 * one expire row is left to the next `grantFor` check.
 * @param {string} runId @param {{grantId: string, scope: string, now: Date}} q
 * @returns {Promise<{ok: false, reason: string, message: string} | null>}
 */
export async function liveGrantRefusal(runId, { grantId, scope, now }) {
  const record = await readRun(runId);
  const grant = record.autopilot && typeof record.autopilot === 'object' ? record.autopilot : null;
  /** @type {string | null} */
  let reason = null;
  if (grant === null || grant.grant_id !== grantId) reason = 'no-grant';
  else if (record.status !== 'active') reason = 'run-ended';
  else {
    const state = grantState(grant, now);
    if (state !== 'active') reason = state;
    else if (isFixedDeny(scope) || grant.deny.includes(scope)) reason = 'denied';
    else if (!grant.scopes.includes(scope)) reason = 'not-allowed';
  }
  return reason === null ? null : { ok: false, reason, message: grantRefusal(reason) };
}

/** The comment a created waiver issue gets when its waiver was not written. */
export const CANCELLED_COMMENT = 'waiver cancelled: autopilot grant ended';

/**
 * Best effort: one comment on the waiver issue saying the waiver was cancelled. Never throws.
 * @param {string} url @param {string} marker @param {ActionDeps} deps
 */
async function cancelOnIssue(url, marker, deps) {
  try {
    const m = /^https:\/\/github\.com\/([A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+)\/issues\/(\d+)$/.exec(url);
    if (!m) return;
    await ghWithBodyFile(deps.exec ?? realExec, deps.env ?? process.env, `${CANCELLED_COMMENT}\n\n${marker}\n`, (file) => [['gh', 'issue', 'comment', m[2], '--repo', m[1], '--body-file', file]]);
  } catch {
    // best effort: the missing waiver row is what the gate reads
  }
}

/** @param {unknown} id @param {string} sub */
function needDecision(id, sub) {
  if (typeof id !== 'string' || !/^[A-Za-z0-9._:-]{1,64}$/.test(id)) throw new StateError('usage', `autopilot ${sub} needs --decision <id> (the delegate's acted decision from autopilot ask)`);
}

/**
 * `autopilot waive`: see the module doc.
 * @param {{runId: string, block: string, file: string, finding: string, severity: string, reason: string, decisionId: string}} req
 * @param {ActionDeps} [deps]
 * @returns {Promise<{ok: true, row: Record<string, any>, issue: string | null, issueAction: string | null, issueFailure: string | null, issueRowWritten: boolean} | {ok: false, reason: string, message: string}>}
 * @throws {StateError} `usage` for a malformed request; `unknown_block`, `no-run`.
 */
export async function waiveForOwner(req, deps = {}) {
  const clock = deps.now ?? (() => new Date());
  if (!DELEGABLE_SEVERITIES.includes(req.severity)) throw new StateError('usage', `--severity must be warning or nit (${NEVER_CRITICAL})`);
  if (typeof req.finding !== 'string' || !FINDING_PATTERN.test(req.finding)) throw new StateError('usage', 'a finding id is required (letters, digits, . _ : -)');
  if (typeof req.reason !== 'string' || req.reason.trim().length === 0) throw new StateError('usage', 'autopilot waive needs --reason <text>');
  // the reason goes into a public issue: a secret-shaped text left after the scrub refuses the waive
  const reason = scrub(req.reason.trim());
  if (findSecret(reason) !== null) throw new StateError('usage', 'the reason looks like it holds a secret; reword it');
  if (req.finding === PROOF_FINDING) return { ok: false, reason: 'never-delegated', message: 'proof is never delegated; only the owner can waive it (block waive)' };
  needDecision(req.decisionId, 'waive');
  let file;
  try {
    file = repoRelativePath(req.file);
  } catch {
    throw new StateError('usage', '--file must be a repo-relative path');
  }
  const record = await readRun(req.runId);
  assertBlock(record, req.block);
  const key = await loadKey(req.runId);
  const recorded = recordedSeverity(await runRowsOf(record, deps), { block: req.block, file, finding: req.finding, key });
  if (recorded.severity === null) {
    return { ok: false, reason: 'severity-unknown', message: `finding ${req.finding} in ${file} has no verifiable recorded severity (${recorded.why}); ${NEVER_CRITICAL}` };
  }
  if (!DELEGABLE_SEVERITIES.includes(recorded.severity)) {
    return { ok: false, reason: 'critical', message: `finding ${req.finding} in ${file} is recorded as ${recorded.severity}; ${NEVER_CRITICAL}` };
  }
  if (recorded.severity !== req.severity) {
    return { ok: false, reason: 'severity-mismatch', message: `finding ${req.finding} in ${file} is recorded as ${recorded.severity}, not ${req.severity}` };
  }
  const scope = `waive:${recorded.severity}`;
  const now = clock();
  const write = writerFor(record, deps);
  const gate = await grantFor(req.runId, scope, now, { writeRow: deps.writeRow });
  if (!gate.ok) return { ok: false, reason: gate.reason, message: grantRefusal(gate.reason) };
  const grantId = gate.grant.grant_id;
  const subject = { block: req.block, file, finding: req.finding };
  const refused = decisionRefusal(await runRowsOf(record, deps), { decisionId: req.decisionId, grantId, scope, subject, decision: 'waive', at: now.getTime(), key });
  if (refused) return refused;
  const ts = now.toISOString();
  const text = waiverIssueText({ runId: req.runId, block: req.block, file, finding: req.finding, severity: recorded.severity, reason, grantId, ts });

  // one finding at a time in this process: the dedupe search, the create and the waiver row of
  // two concurrent waives never interleave, so they open one issue
  return keyedLock(`autopilot-waive\0${req.runId}\0${req.block}\0${file}\0${req.finding}`, async () => {
    const issue = await trackWaiverIssue({ workspace: record.workspace, text }, deps);
    // the gh work may have outlived the grant: re-check grant and decision under the run lock with
    // a fresh clock, and write the waiver there, dated then
    /** @type {{ok: true, row: Record<string, any>} | {ok: false, reason: string, message: string}} */
    const done = await withRunLock(req.runId, async () => {
      const fresh = clock();
      const ended = await liveGrantRefusal(req.runId, { grantId, scope, now: fresh });
      if (ended) return ended;
      const stale = decisionRefusal(await runRowsOf(record, deps), { decisionId: req.decisionId, grantId, scope, subject, decision: 'waive', at: fresh.getTime(), key });
      if (stale) return stale;
      const row = await signed(req.runId, write, {
        event: 'review.waived',
        block: req.block,
        file,
        finding: req.finding,
        reason,
        by: 'autopilot',
        grant_id: grantId,
        decision_id: req.decisionId,
        severity: recorded.severity,
        issue: issue.url,
        ts: fresh.toISOString(),
      });
      return { ok: /** @type {const} */ (true), row };
    });
    if (done.ok === false) {
      if (issue.url !== null) await cancelOnIssue(issue.url, text.marker, deps);
      return done;
    }
    const row = /** @type {{row: Record<string, any>}} */ (done).row;
    /** @type {string | null} */
    let failure = null;
    let issueRowWritten = true;
    if (issue.url === null) {
      failure = /** @type {{failure: string}} */ (issue).failure;
      try {
        await signed(req.runId, write, { event: 'autopilot.issue_failed', block: req.block, file, finding: req.finding, grant_id: grantId, reason: failure, ts: row.ts });
      } catch {
        issueRowWritten = false; // the waiver stands; the result says the issue row is missing
      }
    }
    return { ok: /** @type {const} */ (true), row, issue: issue.url, issueAction: issue.url === null ? null : /** @type {{action: string}} */ (issue).action, issueFailure: failure, issueRowWritten };
  });
}

/**
 * `autopilot round`: one extra fix round past the file's cap, once per file per grant, on the
 * delegate's acted decision for that block and file.
 * @param {{runId: string, block: string, file: string, decisionId: string}} req @param {ActionDeps} [deps]
 * @returns {Promise<ActionResult>}
 * @throws {StateError} `usage`, `unknown_block`, `no-run`.
 */
export async function grantExtraRound(req, deps = {}) {
  const clock = deps.now ?? (() => new Date());
  needDecision(req.decisionId, 'round');
  let file;
  try {
    file = repoRelativePath(req.file);
  } catch {
    throw new StateError('usage', '--file must be a repo-relative path');
  }
  const record = await readRun(req.runId);
  assertBlock(record, req.block);
  const now = clock();
  const gate = await grantFor(req.runId, 'round:extra', now, { writeRow: deps.writeRow });
  if (!gate.ok) return { ok: false, reason: gate.reason, message: grantRefusal(gate.reason) };
  const grantId = gate.grant.grant_id;
  const key = await loadKey(req.runId);
  const write = writerFor(record, deps);
  // check-then-write under the run lock: two requests for one file never both get a round
  return withRunLock(req.runId, async () => {
    const fresh = clock();
    const ended = await liveGrantRefusal(req.runId, { grantId, scope: 'round:extra', now: fresh });
    if (ended) return ended;
    const rows = await runRowsOf(record, deps);
    const refused = decisionRefusal(rows, { decisionId: req.decisionId, grantId, scope: 'round:extra', subject: { block: req.block, file }, decision: 'allow', at: fresh.getTime(), key });
    if (refused) return refused;
    const used = rows.some((r) => r.event === 'autopilot.extra_round' && r.block === req.block && r.file === file && r.grant_id === grantId && verifyRow(r, key).ok);
    if (used) return { ok: false, reason: 'already-used', message: `${file} already had its extra round under grant ${grantId}` };
    const row = await signed(req.runId, write, { event: 'autopilot.extra_round', block: req.block, file, grant_id: grantId, decision_id: req.decisionId, ts: fresh.toISOString() });
    return { ok: true, row };
  });
}

/** @param {string} level @returns {number} */
const levelIndex = (level) => Number(level.slice(1));

/** The highest level a coder ever runs at (L3 is never a coding level, R1). */
export const CODER_MAX_LEVEL = 'L2';

/**
 * The coder level for a block: the recorded lane, at most the plan's level + 1, at most L2.
 * @param {{lane: string, planLevel: string}} input - both `L0`–`L3`.
 * @returns {string}
 */
export function chooseLevel({ lane, planLevel }) {
  const level = Math.min(levelIndex(lane), levelIndex(planLevel) + 1, levelIndex(CODER_MAX_LEVEL));
  return `L${level}`;
}

/**
 * `autopilot level`: see the module doc. The plan's level comes from the plan file's block table
 * (`parsePlan`); the lane from the project ledger's `decision` rows for `lane` with this block
 * (rows naming another plan file are skipped, as `plan check` does).
 * @param {{runId: string, block: string, plan: string, decisionId: string}} req - `plan`: the plan file (absolute or relative to the process cwd).
 * @param {ActionDeps} [deps]
 * @returns {Promise<ActionResult>}
 * @throws {StateError} `usage` (no plan file, the block not in it, no level for it, no --decision), `no-run`.
 */
export async function chooseCoderLevel(req, deps = {}) {
  const clock = deps.now ?? (() => new Date());
  if (typeof req.block !== 'string' || !/^[A-Za-z0-9._-]{1,32}$/.test(req.block)) throw new StateError('usage', 'autopilot level needs --block <id>');
  if (typeof req.plan !== 'string' || req.plan.length === 0) throw new StateError('usage', 'autopilot level needs --plan <file> (the plan whose block table gives the level)');
  needDecision(req.decisionId, 'level');
  let text;
  try {
    text = await readFile(req.plan, 'utf8');
  } catch {
    throw new StateError('usage', `--plan ${req.plan} cannot be read`);
  }
  const entry = parsePlan(text).blocks.find((b) => b.id === req.block);
  if (!entry) throw new StateError('usage', `block ${req.block} is not in the plan's block table`);
  if (entry.level === null) throw new StateError('usage', `block ${req.block} has no level (L0–L3) in the plan`);
  // the block need not be open yet: the level is chosen before `block open` (which then uses it)
  const record = await readRun(req.runId);
  const now = clock();
  const gate = await grantFor(req.runId, 'model:choose', now, { writeRow: deps.writeRow });
  if (!gate.ok) return { ok: false, reason: gate.reason, message: grantRefusal(gate.reason) };
  const all = deps.readRows ? await deps.readRows() : await readAllRows(record.project);
  const key = await loadKey(req.runId);
  const runRows = all.filter((r) => r?.run === req.runId);
  const recorded = recordedLanes(all, path.basename(req.plan)).get(req.block);
  if (!recorded) return { ok: false, reason: 'no-lane', message: `block ${req.block} has no recorded lane — run forge jev ask lane --block ${req.block}; the level goes to the owner` };
  if (!/^L[0-3]$/.test(recorded.lane)) return { ok: false, reason: 'no-lane', message: `block ${req.block}'s recorded lane is ${JSON.stringify(recorded.lane)}, not a level; the level goes to the owner` };
  const level = chooseLevel({ lane: recorded.lane, planLevel: entry.level });
  // the delegate must have chosen exactly this level (its options were the levels, e.g. L1,L2)
  const refused = decisionRefusal(runRows, { decisionId: req.decisionId, grantId: gate.grant.grant_id, scope: 'model:choose', subject: { block: req.block }, decision: level, at: now.getTime(), key });
  if (refused) return refused;
  const grantId = gate.grant.grant_id;
  return withRunLock(req.runId, async () => {
    const fresh = clock();
    const ended = await liveGrantRefusal(req.runId, { grantId, scope: 'model:choose', now: fresh });
    if (ended) return ended;
    const stale = decisionRefusal(await runRowsOf(record, deps), { decisionId: req.decisionId, grantId, scope: 'model:choose', subject: { block: req.block }, decision: level, at: fresh.getTime(), key });
    if (stale) return stale;
    const row = await signed(req.runId, writerFor(record, deps), {
      event: 'autopilot.level',
      block: req.block,
      level,
      lane: recorded.lane,
      lane_source: recorded.source,
      lane_decision_id: recorded.decision_id,
      plan_level: entry.level,
      grant_id: grantId,
      decision_id: req.decisionId,
      ts: fresh.toISOString(),
    });
    return { ok: /** @type {const} */ (true), row };
  });
}
