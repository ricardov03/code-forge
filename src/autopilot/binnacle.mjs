/**
 * The autopilot binnacle and full log (issue #5, plan autopilot §2 row B49a): what the owner reads
 * on return, shaped like their "overnight run" doc.
 *
 *   - {@link buildBinnacle}: the 8-part summary (title + byline, Status at a glance, Decisions,
 *     Blocks, Open questions, Actions only you can take, Incidents, Timeline newest first) from the
 *     run's ledger rows and run record. Pure: no I/O, no clock besides the `now` it is given.
 *   - {@link buildFullLog}: one entry per autopilot-related ledger row, newest first, each with a
 *     short fixed-format `detail` (never raw packet text).
 *   - {@link renderBinnacleMarkdown} / {@link renderLogMarkdown}: the Markdown of each.
 *   - {@link writeAutopilotFiles} / {@link refreshAutopilotFiles}: `autopilot-binnacle.md` and
 *     `autopilot-log.md` in the run dir (`~/.code-forge/runs/<run>/`, 0600), rewritten after every
 *     delegate decision (best effort, `delegate.mjs`).
 *   - {@link storeLink}: `autopilot binnacle --link <url>` — the https link of the live doc, kept in
 *     the active grant under a signed `autopilot.link` row.
 *
 * Every text leaves through the same scrub: registered secrets and secret-shaped tokens masked, the
 * home dir as `~`, the working dir as `<project>`, `op://` references as `op://<ref>`.
 */

import { randomBytes } from 'node:crypto';
import { mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { maskSecretTokens } from '../config/secret-patterns.mjs';
import { buildSpend } from '../ledger/report.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { StateError, assertRunId, runsDir } from '../state/paths.mjs';
import { readRun, saveRun, withRunLock, writeSigned } from '../state/run.mjs';
import { createScrubber } from '../util/error-log.mjs';
import { exec } from '../util/exec.mjs';
import { redact } from '../util/redact.mjs';
import { checkExpiry, grantState } from './grant.mjs';
import { spendByCategory } from './limits.mjs';
import { BUDGET_CATEGORIES } from './scopes.mjs';

export const BINNACLE_FILE = 'autopilot-binnacle.md';
export const LOG_FILE = 'autopilot-log.md';
export const NONE_YET = 'none yet';

/** The ledger events (besides every `autopilot.*`) that belong to the log and the timeline. */
const EXTRA_EVENTS = Object.freeze(['block.close', 'review.session_timeout', 'worker.down', 'worker.replaced', 'ledger.tamper', 'budget.refused', 'gate.red', 'gate.done']);

/** @typedef {Record<string, any>} Row */
/** @typedef {{home?: string | null, cwd?: string | null}} ScrubCtx */

/**
 * The scrub every output text goes through.
 * @param {ScrubCtx} [ctx] @returns {(text: string) => string}
 */
export function makeScrub(ctx = {}) {
  const scrubber = createScrubber({ home: ctx.home === undefined ? os.homedir() : ctx.home, cwd: ctx.cwd === undefined ? process.cwd() : ctx.cwd });
  return (text) => scrubber.scrub(maskSecretTokens(redact(String(text))));
}

/** @param {unknown} value @param {(t: string) => string} scrub @returns {any} `value` with every string scrubbed. */
function deepScrub(value, scrub) {
  if (typeof value === 'string') return scrub(value);
  if (Array.isArray(value)) return value.map((v) => deepScrub(v, scrub));
  if (value !== null && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, deepScrub(v, scrub)]));
  return value;
}

/** @param {unknown} v @returns {v is string} a non-empty string. */
const str = (v) => typeof v === 'string' && v.length > 0;
/** @param {unknown} v @returns {string | null} */
const strOr = (v) => (str(v) ? v : null);
/** @param {unknown} x @returns {string} a list as `a, b`; anything but an array is empty. */
const list = (x) => (Array.isArray(x) ? x.join(', ') : '');
/** @param {...unknown} xs @returns {string | null} the first non-empty string. */
const first = (...xs) => xs.find(str) ?? null;
/** @param {Row | null | undefined} record @returns {ScrubCtx} the scrub context of a run: its workspace is the project path. */
export const scrubCtxOf = (record) => ({ cwd: str(record?.workspace) ? record?.workspace : null });
/** @param {unknown} x @param {string} [fallback] @returns {string} `x` as text, or `fallback`: no text ever says undefined or null. */
const u = (x, fallback = 'unknown') => (x === undefined || x === null || x === '' ? fallback : String(x));
/** @param {string | null | undefined} sha */
const short = (sha) => (str(sha) ? sha.slice(0, 7) : null);
/** @param {number} n */
const usd = (n) => n.toFixed(2);
/** @param {string} text @param {number} max */
const cut = (text, max) => (text.length > max ? `${text.slice(0, max - 1)}…` : text);

/**
 * The rows of one run, oldest first, each with a time: its own `ts`, else the time of the row
 * before it (`approx`), else null. Shadow rows are dropped.
 * @param {Row[]} rows @param {string} runId
 * @returns {Array<{row: Row, time: string | null, approx: boolean, index: number}>}
 */
function ordered(rows, runId) {
  /** @type {Array<{row: Row, time: string | null, approx: boolean, index: number}>} */
  const out = [];
  let last = null;
  rows.forEach((row, index) => {
    if (row === null || typeof row !== 'object' || row.run !== runId || row.source === 'shadow') return;
    const good = str(row.ts) && Number.isFinite(Date.parse(row.ts));
    if (good) last = row.ts;
    out.push({ row, time: good ? row.ts : last, approx: !good, index });
  });
  return out;
}

/** @param {Row} row */
const isDelegateSession = (row) => row.event === 'session' && row.role === 'delegate';
/** @param {Row} row */
const inLog = (row) => (typeof row.event === 'string' && (row.event.startsWith('autopilot.') || EXTRA_EVENTS.includes(row.event))) || (row.event === 'review.waived' && row.by === 'autopilot') || isDelegateSession(row);

/**
 * Event label and fixed-format detail of one row (never the packet, question or answer text).
 * @param {Row} row @returns {{event: string, detail: string, link: string | null}}
 */
function describe(row) {
  const scope = strOr(row.scope) ?? 'unknown scope';
  switch (row.event) {
    case 'autopilot.grant':
      return { event: 'Grant started', detail: `grant ${u(row.grant_id)} · delegate ${u(row.delegate)} · allow ${list(row.scopes)} · until ${u(row.until)}`, link: null };
    case 'autopilot.stop':
      return { event: 'Grant stopped', detail: `grant ${u(row.grant_id)}`, link: null };
    case 'autopilot.expire':
      return { event: 'Grant expired', detail: `grant ${u(row.grant_id)} · window ended ${u(row.until)}`, link: null };
    case 'autopilot.link':
      return { event: 'Log link stored', detail: `grant ${u(row.grant_id)}`, link: strOr(row.link) };
    case 'autopilot.decision': {
      const conf = typeof row.confidence === 'number' ? ` · confidence ${u(row.confidence)}` : '';
      if (row.acted === true) return { event: 'Delegate decided', detail: `${scope} · acted · decision ${u(row.decision, 'no decision')}${conf} · ${u(row.decision_id)}`, link: null };
      return { event: 'Question to the owner', detail: `${scope} · to owner (${row.owner_reason ?? 'unknown'})${conf} · ${u(row.decision_id)}`, link: null };
    }
    case 'autopilot.pause':
      return { event: 'Budget category paused', detail: `${u(row.category)} · spent ${usd(Number(row.spent_usd ?? 0))} of ${usd(Number(row.cap_usd ?? 0))} USD`, link: null };
    case 'autopilot.approve':
      return { event: 'Owner approval', detail: `${u(row.key)} · until ${u(row.until)} · ${u(row.approval_id)}`, link: null };
    case 'autopilot.restore':
      return { event: 'Approval restored', detail: `${row.key ?? row.approval_id ?? 'approval'}`, link: null };
    case 'autopilot.restore_skipped':
      return { event: 'Restore skipped', detail: `${row.key ?? row.approval_id ?? 'approval'} · ${row.reason === 'changed-by-hand' ? 'changed by hand' : (row.reason ?? 'unknown')}`, link: null };
    case 'autopilot.extra_round':
      return { event: 'Extra fix round', detail: `grant ${u(row.grant_id)} · decision ${u(row.decision_id)}`, link: null };
    case 'autopilot.level':
      return { event: 'Coder level chosen', detail: `${u(row.level)} · lane ${row.lane ?? 'none'} · plan ${row.plan_level ?? 'none'} · decision ${u(row.decision_id)}`, link: null };
    case 'autopilot.issue_failed':
      return { event: 'Waiver issue failed', detail: `finding ${u(row.finding, 'unknown finding')} · no issue opened`, link: null };
    case 'review.waived':
      return { event: 'Waiver by autopilot', detail: `finding ${u(row.finding, 'unknown finding')} · ${row.severity ?? 'unknown severity'} · issue ${row.issue ?? 'none'} · decision ${u(row.decision_id)}`, link: strOr(row.issue) };
    case 'session':
      return { event: 'Delegate session', detail: `${row.level ?? 'level unknown'} · ${row.provider ?? 'provider unknown'} · status ${row.status ?? 'unknown'}`, link: null };
    case 'block.close':
      return { event: 'Block closed', detail: `status ${row.status ?? 'unknown'}${short(first(row.commit, row.sha, row.commit_sha)) ? ` · commit ${short(first(row.commit, row.sha, row.commit_sha))}` : ''}`, link: first(row.pr_url, row.pr) };
    case 'review.session_timeout':
      return { event: 'Review session timed out', detail: `${row.lens ?? row.role ?? 'session'} · attempt ${u(row.attempt)} · ${row.retried === true ? 'retried' : 'gave up'}`, link: null };
    case 'worker.down':
      return { event: 'Worker down', detail: 'the pinned worker is not running', link: null };
    case 'worker.replaced':
      return { event: 'Worker replaced', detail: 'the pinned worker was replaced by another process', link: null };
    case 'ledger.tamper':
      return { event: 'Ledger check failed', detail: `block ${row.block ?? 'unknown'} stopped`, link: null };
    case 'budget.refused':
      return { event: 'Budget refused a session', detail: `${row.role ?? 'session'} · spent ${typeof row.spent_usd === 'number' ? usd(row.spent_usd) : 'unknown'} of ${typeof row.budget_usd === 'number' ? usd(row.budget_usd) : 'unknown'} USD`, link: null };
    case 'gate.red':
      return { event: 'Gate red', detail: `${row.gate ?? 'gate'} · exit ${row.code ?? 'unknown'}`, link: null };
    case 'gate.done':
      return { event: 'Gates finished', detail: `${row.stack ?? 'stack'} · ${row.all_ok === true ? 'all green' : 'not all green'}`, link: null };
    default:
      return { event: String(row.event), detail: '', link: null };
  }
}

/**
 * One entry per autopilot-related ledger row of the run, newest first (ties: the later row first).
 * @param {{runId: string, rows: Row[], scrubCtx?: ScrubCtx}} opts
 * @returns {Array<{time: string | null, event: string, block: string | null, file: string | null, detail: string, link: string | null}>}
 */
export function buildFullLog({ runId, rows, scrubCtx }) {
  const scrub = makeScrub(scrubCtx);
  const mine = ordered(rows, runId).filter((x) => inLog(x.row));
  mine.sort((a, b) => (Date.parse(b.time ?? '') || 0) - (Date.parse(a.time ?? '') || 0) || b.index - a.index);
  return mine.map(({ row, time }) => {
    const d = describe(row);
    return deepScrub({ time, event: d.event, block: strOr(row.block), file: strOr(row.file), detail: d.detail, link: d.link }, scrub);
  });
}

/** @param {Row} row @returns {string} the subject line of an event, e.g. ` · b1 · src/a.mjs`. */
const where = (row) => `${str(row.block) ? ` · ${u(row.block)}` : ''}${str(row.file) ? ` · ${u(row.file)}` : ''}`;

/**
 * @param {Row[]} blockRows rows of one block @param {Row | undefined} entry its run-record entry
 * @param {string} id
 */
function blockLine(blockRows, entry, id) {
  const last = (/** @type {(r: Row) => boolean} */ f) => [...blockRows].reverse().find(f);
  const pick = (/** @type {string[]} */ keys) => {
    for (const r of [...blockRows].reverse()) for (const k of keys) if (str(r[k])) return /** @type {string} */ (r[k]);
    return null;
  };
  const branch = pick(['branch']);
  const pr = pick(['pr_url', 'pr']);
  const sha = short(pick(['commit', 'commit_sha', 'sha']));
  const files = (/** @type {string} */ ev) => new Set(blockRows.filter((r) => r.event === ev && str(r.file)).map((r) => r.file)).size;
  const results = blockRows.filter((r) => r.event === 'review.result').length;
  const review = results + files('review.approved') + files('review.waived') === 0 ? 'none yet' : `${results} result${results === 1 ? '' : 's'} · ${files('review.approved')} approved · ${files('review.waived')} waived`;
  const proofs = blockRows.filter((r) => r.event === 'proof');
  const tests = proofs.length === 0 ? 'not recorded' : `${proofs.length} proof run${proofs.length === 1 ? '' : 's'}${str(proofs[proofs.length - 1].isolation) ? `, last ${proofs[proofs.length - 1].isolation}` : ''}`;
  const gateRow = last((r) => r.event === 'gate.done');
  const closed = blockRows.some((r) => r.event === 'block.close');
  const gates = gateRow ? (gateRow.all_ok === true ? 'green' : 'red') : closed ? 'passed at close' : 'none yet';
  const status = entry?.status ?? (closed ? 'closed' : 'unknown');
  const state = status === 'closed' ? 'Closed, ready for you to mark ready and merge' : status === 'open' ? 'Open' : status === 'stopped' ? 'Stopped' : String(status);
  return { block: id, branch: [branch, sha ? `commit ${sha}` : null, pr].filter((x) => x !== null).join(' · ') || 'not recorded', review, tests, gates, state };
}

/**
 * The binnacle data. See the module doc.
 * @param {{runId: string, rows: Row[], record: Row, now: Date, scrubCtx?: ScrubCtx, owner?: string | null}} opts - `owner`: the byline's owner (see {@link gitOwner}), null when unknown.
 */
export function buildBinnacle({ runId, rows, record, now, scrubCtx = scrubCtxOf(record), owner = null }) {
  const scrub = makeScrub(scrubCtx);
  const mine = ordered(rows, runId);
  const all = mine.map((x) => x.row);
  const grant = record?.autopilot && typeof record.autopilot === 'object' ? record.autopilot : null;
  const grantRow = [...all].reverse().find((r) => r.event === 'autopilot.grant');
  const linkRow = [...all].reverse().find((r) => r.event === 'autopilot.link');
  const link = strOr(grant?.link) ?? strOr(linkRow?.link);

  // ---- blocks
  const blocksRec = /** @type {Record<string, Row>} */ (record?.blocks && typeof record.blocks === 'object' ? record.blocks : {});
  const ids = [...new Set([...Object.keys(blocksRec), ...all.filter((r) => r.event === 'block.close' && str(r.block)).map((r) => r.block)])].sort((a, b) => String(blocksRec[a]?.opened_at ?? '').localeCompare(String(blocksRec[b]?.opened_at ?? '')) || a.localeCompare(b));
  const blocks = ids.map((id) =>
    blockLine(
      all.filter((r) => r.block === id),
      blocksRec[id],
      id,
    ),
  );
  const closedIds = ids.filter((id) => (blocksRec[id]?.status ?? (all.some((r) => r.event === 'block.close' && r.block === id) ? 'closed' : '')) === 'closed');

  // ---- decisions
  /** @type {Array<{time: string | null, decision: string, why: string, reverse: string}>} */
  const decisions = [];
  const stopCmd = `code-forge autopilot stop --run ${runId}`;
  for (const { row, time } of mine) {
    const r = row;
    if (r.event === 'autopilot.decision') {
      if (r.acted === true) decisions.push({ time, decision: `${u(r.scope, 'unknown scope')}: the delegate chose "${u(r.decision, 'no decision')}"${where({ block: r.subject?.block, file: r.subject?.file })}`, why: `${r.reason ?? 'no reason recorded'} (${typeof r.confidence === 'number' ? `confidence ${r.confidence}` : 'no confidence'})`, reverse: `Overrule it: undo the action it allowed (see its own entry) and, to stop further ones, run ${stopCmd}` });
      else decisions.push({ time, decision: `Asked the owner (${u(r.scope, 'unknown scope')}): ${r.question ?? 'question not recorded'}`, why: `Not acted on: ${r.failure ?? r.owner_reason ?? 'unknown'}`, reverse: 'Nothing was done; answer it yourself (see Open questions).' });
    } else if (r.event === 'review.waived' && r.by === 'autopilot') {
      decisions.push({ time, decision: `Waived ${u(r.finding, 'unknown finding')} (${r.severity ?? 'unknown severity'}) in ${u(r.file, 'unknown file')}, block ${u(r.block, 'unknown block')}`, why: `${r.reason ?? 'no reason recorded'} (decision ${u(r.decision_id)})`, reverse: `Reopen the finding: fix it in ${u(r.file, 'unknown file')}, then delete the waiver issue (${r.issue ?? 'none was opened'})` });
    } else if (r.event === 'autopilot.extra_round') {
      decisions.push({ time, decision: `One extra fix round for ${u(r.file, 'unknown file')}, block ${u(r.block, 'unknown block')}`, why: `The delegate allowed it (decision ${u(r.decision_id)})`, reverse: 'The round is spent; refuse further ones by stopping the grant or denying round:extra.' });
    } else if (r.event === 'autopilot.level') {
      decisions.push({ time, decision: `Block ${u(r.block, 'unknown block')} codes at ${u(r.level)}`, why: `Lane ${r.lane ?? 'none'}, plan level ${r.plan_level ?? 'none'} (decision ${u(r.decision_id)})`, reverse: 'Open the block again with code-forge block open --level <other level>.' });
    } else if (r.event === 'autopilot.approve') {
      decisions.push({ time, decision: `You approved ${u(r.key)} until ${u(r.until)}`, why: `Owner approval ${u(r.approval_id)}`, reverse: `It restores itself at ${u(r.until)}; or set the old value back in .code-forge.yml and run code-forge run reload.` });
    }
  }

  // ---- open questions and owner actions
  const toOwner = mine.filter((x) => x.row.event === 'autopilot.decision' && x.row.acted !== true).map((x) => x.row);
  const questions = toOwner.map((r) => `${r.question ?? 'question not recorded'} (${u(r.scope, 'unknown scope')}, ${r.owner_reason ?? 'unknown'})`);
  /** @type {Array<{text: string, done: boolean}>} */
  const ownerActions = toOwner.map((r) => ({ text: `Answer: ${r.question ?? 'question not recorded'} (${u(r.scope, 'unknown scope')})`, done: false }));
  // criticals are never delegated: per (block, file) the LATEST review.done decides; a critical there
  // stays an owner action until a later approval of that file or a human waiver clears it
  /** @type {Map<string, {row: Row, index: number}>} */
  const latestDone = new Map();
  for (const { row, index } of mine) if (row.event === 'review.done') latestDone.set(`${row.block}\0${row.file}`, { row, index });
  for (const { row, index } of latestDone.values()) {
    if (!(Number(row.findings_by_severity?.critical) > 0)) continue;
    const cleared = mine.some((y) => y.index > index && y.row.block === row.block && y.row.file === row.file && (y.row.event === 'review.approved' || (y.row.event === 'review.waived' && y.row.by === 'human')));
    if (!cleared) ownerActions.push({ text: `Decide the critical finding in ${u(row.file, 'unknown file')} (block ${u(row.block, 'unknown block')}): critical findings are never delegated`, done: false });
  }
  const paused = grant ? Object.keys(grant.paused && typeof grant.paused === 'object' ? grant.paused : {}).sort() : [...new Set(all.filter((r) => r.event === 'autopilot.pause').map((r) => r.category))];
  for (const c of paused) {
    ownerActions.push({ text: `Budget ${u(c, 'unknown category')} is paused: raise the cap or leave it (only you can raise a budget)`, done: false });
    const p = grant ? grant.paused?.[c] : all.find((r) => r.event === 'autopilot.pause' && r.category === c);
    if (p) questions.push(`Budget ${u(c, 'unknown category')} stopped at ${usd(Number(p.spent_usd ?? 0))} of ${usd(Number(p.cap_usd ?? 0))} USD: raise the cap or leave it?`);
  }
  for (const r of all.filter((x) => x.event === 'autopilot.restore_skipped')) ownerActions.push({ text: `Check ${r.key ?? r.approval_id ?? 'the approval'}: it was changed by hand, so it was not restored`, done: false });
  for (const id of closedIds) {
    const pr = first(...[...all].reverse().filter((r) => r.block === id).map((r) => first(r.pr_url, r.pr)));
    ownerActions.push({ text: `Mark block ${id} ready and merge it${pr ? ` (${pr})` : ''}`, done: false });
  }

  // ---- incidents
  /** @type {Array<{time: string | null, what: string, effect: string, fix: string}>} */
  const incidents = [];
  for (const { row, time } of mine) {
    const r = row;
    if (r.event === 'review.session_timeout') incidents.push({ time, what: `A ${r.lens ?? r.role ?? 'review'} session timed out (attempt ${u(r.attempt)})`, effect: r.retried === true ? 'It was retried.' : 'No retry left; the review was marked unavailable.', fix: r.retried === true ? 'None needed unless it repeats.' : 'Run the review again.' });
    else if (r.event === 'worker.down' || r.event === 'worker.replaced') incidents.push({ time, what: r.event === 'worker.down' ? 'The pinned worker was down' : 'The pinned worker was replaced', effect: 'Reviews wait until the worker is back.', fix: 'Restart the worker for this run.' });
    else if (r.event === 'ledger.tamper') incidents.push({ time, what: `A ledger check failed for block ${r.block ?? 'unknown'}`, effect: 'The block was stopped.', fix: 'Look at the block before reopening it.' });
    else if (r.event === 'autopilot.issue_failed') incidents.push({ time, what: `The waiver issue for ${u(r.finding, 'unknown finding')} in ${u(r.file, 'unknown file')} could not be opened`, effect: 'The waiver stands without an issue.', fix: 'Open the tracking issue by hand.' });
    else if (r.event === 'budget.refused') incidents.push({ time, what: `A ${r.role ?? 'session'} session was refused: budget ${typeof r.spent_usd === 'number' ? usd(r.spent_usd) : 'unknown'} of ${typeof r.budget_usd === 'number' ? usd(r.budget_usd) : 'unknown'} USD`, effect: 'The session did not run.', fix: 'Raise the budget (only you can) or finish by hand.' });
    else if (r.event === 'autopilot.pause') incidents.push({ time, what: `Budget category ${u(r.category)} reached its stop at ${usd(Number(r.spent_usd ?? 0))} of ${usd(Number(r.cap_usd ?? 0))} USD`, effect: `Delegated actions in ${u(r.category)} stopped.`, fix: 'Raise the cap or leave it paused.' });
  }

  // ---- timeline (newest first)
  const timeline = buildFullLog({ runId, rows, scrubCtx }).map((e) => ({ time: e.time, event: `${e.event}${e.block ? ` · ${e.block}` : ''}${e.file ? ` · ${e.file}` : ''}${e.detail ? ` — ${e.detail}` : ''}` }));

  // ---- status
  const state = grant ? (checkedState(grant, record, now)) : 'no grant';
  const waived = all.filter((r) => r.event === 'review.waived' && r.by === 'autopilot').length;
  const approved = new Set(all.filter((r) => r.event === 'review.approved').map((r) => `${u(r.block, 'unknown block')}\0${u(r.file, 'unknown file')}`)).size;
  const results = all.filter((r) => r.event === 'review.result').length;
  const prs = [...new Set(all.map((r) => first(r.pr_url, r.pr)).filter(str))];
  const fullSuite = [...all].reverse().find((r) => r.event === 'gate.done');
  const readFiles = [...new Set([...all.filter((r) => (r.event === 'review.waived' && r.by === 'autopilot') || r.event === 'autopilot.extra_round').map((r) => r.file), ...toOwner.map((r) => r.subject?.file)].filter(str))];
  const baseSha = ids.map((id) => blocksRec[id]?.base_sha).find(str);
  const approx = mine.filter((x) => x.approx).length;
  const caps = grant?.caps ?? grantRow?.caps ?? {};
  const spendBy = grant ? spendByCategory(all, runId, grant) : null;
  const total = buildSpend(all).total;
  const spendText = spendBy
    ? BUDGET_CATEGORIES.map((c) => (typeof caps[c] === 'number' ? `${c} ${usd(spendBy[c] ?? 0)} of ${usd(caps[c])} USD` : `${c} ${usd(spendBy[c] ?? 0)} USD (no cap)`)).join(', ')
    : `${usd(total.usd)} USD in total`;
  const status = [
    { item: 'Result', value: ids.length === 0 ? 'no blocks yet' : `${closedIds.length} of ${ids.length} block${ids.length === 1 ? '' : 's'} closed` },
    { item: 'Stack/PRs', value: prs.length === 0 ? 'none recorded' : prs.join(', ') },
    { item: 'Full suite', value: fullSuite ? (fullSuite.all_ok === true ? 'green' : 'red') : 'not recorded' },
    { item: 'Reviews', value: `${results} result${results === 1 ? '' : 's'} · ${approved} file${approved === 1 ? '' : 's'} approved · ${waived} waived by autopilot` },
    { item: 'Files to read', value: readFiles.length === 0 ? 'none' : `${readFiles.slice(0, 5).join(', ')}${readFiles.length > 5 ? ` and ${readFiles.length - 5} more` : ''}` },
    { item: 'Base', value: short(baseSha) ?? 'unknown' },
    { item: 'Spend', value: spendText },
    { item: 'Autopilot window', value: grant ? `${state} · grant ${u(grant.grant_id)} · ${u(grant.started_at)} to ${u(grant.until)} · delegate ${u(grant.delegate)} · allow ${list(grant.scopes)}` : grantRow ? `grant ${u(grantRow.grant_id)} · until ${u(grantRow.until)}` : 'no grant' },
    { item: 'Log link', value: link ?? 'none' },
    ...(approx > 0 ? [{ item: 'Clock note', value: approx === 1 ? '1 row carries no valid timestamp and takes the time of the row before it' : `${approx} rows carry no valid timestamp and take the time of the rows before them` }] : []),
  ];

  const out = {
    title: `Autopilot run ${runId}`,
    byline: { date: now.toISOString().slice(0, 10), owner: str(owner) ? owner : null },
    status,
    decisions,
    blocks,
    open_questions: questions,
    owner_actions: ownerActions,
    incidents,
    timeline,
  };
  return deepScrub(out, scrub);
}

/** @param {Row} grant @param {Row} record @param {Date} now @returns {string} */
function checkedState(grant, record, now) {
  if (record?.status && record.status !== 'active') return 'run ended';
  return grantState(/** @type {import("./grant.mjs").Grant} */ (grant), now);
}

/** @param {unknown} text @returns {string} one table cell: no pipe, no line break. */
const cell = (text) => String(text ?? '').replace(/\r?\n/g, ' ').replace(/\|/g, '\\|');

/**
 * @param {string[]} head @param {string[][]} body @returns {string[]} the lines of a pipe table, or `none yet`.
 */
function table(head, body) {
  if (body.length === 0) return [NONE_YET, ''];
  return [`| ${head.join(' | ')} |`, `| ${head.map(() => '---').join(' | ')} |`, ...body.map((r) => `| ${r.map(cell).join(' | ')} |`), ''];
}

/**
 * The binnacle as Markdown: title, byline, then the 7 sections in the doc's order.
 * @param {ReturnType<typeof buildBinnacle>} b @param {ScrubCtx} [scrubCtx] @returns {string}
 */
export function renderBinnacleMarkdown(b, scrubCtx) {
  const scrub = makeScrub(scrubCtx);
  const lines = [`# ${b.title}`, '', `${b.byline.date} · ${b.byline.owner ?? 'owner not set'}`, ''];
  lines.push('## Status at a glance', '', ...table(['Item', 'Value'], b.status.map((s) => [s.item, s.value])));
  lines.push('## Decisions', '', ...table(['Time', 'Decision', 'Why', 'What would reverse it'], b.decisions.map((d) => [d.time ?? '', d.decision, d.why, d.reverse])));
  lines.push('## Blocks', '', ...table(['Block', 'Branch / PR', 'Review', 'Tests', 'Gates', 'State'], b.blocks.map((x) => [x.block, x.branch, x.review, x.tests, x.gates, x.state])));
  lines.push('## Open questions', '', ...(b.open_questions.length === 0 ? [NONE_YET] : b.open_questions.map((q, i) => `${i + 1}. ${cell(q)}`)), '');
  lines.push('## Actions only you can take', '', ...(b.owner_actions.length === 0 ? [NONE_YET] : b.owner_actions.map((a) => `- [${a.done ? 'x' : ' '}] ${cell(a.text)}`)), '');
  lines.push('## Incidents', '', ...table(['Time', 'What', 'Effect', 'Fix'], b.incidents.map((i) => [i.time ?? '', i.what, i.effect, i.fix])));
  lines.push('## Timeline', '', ...(b.timeline.length === 0 ? [NONE_YET] : b.timeline.map((t) => `- ${t.time ?? 'unknown time'} — ${cell(t.event)}`)), '');
  return scrub(`${lines.join('\n').replace(/\n+$/, '')}\n`);
}

/**
 * The full log as Markdown: one table, newest first.
 * @param {{runId: string, entries: ReturnType<typeof buildFullLog>}} opts @param {ScrubCtx} [scrubCtx]
 * @returns {string}
 */
export function renderLogMarkdown({ runId, entries }, scrubCtx) {
  const scrub = makeScrub(scrubCtx);
  const lines = [`# Autopilot full log ${runId}`, '', 'Newest first.', '', ...table(['Time', 'Event', 'Block', 'File', 'Detail', 'Link'], entries.map((e) => [e.time ?? '', e.event, e.block ?? '', e.file ?? '', e.detail, e.link ?? '']))];
  return scrub(`${lines.join('\n').replace(/\n+$/, '')}\n`);
}

/**
 * The byline's owner: the workspace's `git config user.name` (the owner's own local setting), best
 * effort: any failure, a missing name or a non-repo gives null. `GIT_*` variables of a calling hook
 * are dropped so the answer is the workspace's.
 * @param {unknown} workspace @returns {Promise<string | null>}
 */
export async function gitOwner(workspace) {
  if (!str(workspace)) return null;
  try {
    const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')));
    const res = await exec(['git', '-C', workspace, 'config', 'user.name'], { env, timeoutMs: 10000, okExitCodes: [0, 1] });
    const name = res.result === 'ok' && res.code === 0 ? res.stdout.trim() : '';
    return name.length > 0 && name.length <= 200 ? name : null;
  } catch {
    return null;
  }
}

/** @param {string} runId @returns {string} the run dir the Markdown files live in. */
export const runFilesDir = (runId) => path.join(runsDir(), assertRunId(runId));

/**
 * Write `text` to `<run dir>/<name>` atomically (0600).
 * @param {string} runId @param {string} name @param {string} text @returns {string} the path
 */
function writeFileIn(runId, name, text) {
  const dir = runFilesDir(runId);
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const file = path.join(dir, name);
  const tmp = `${file}.${process.pid}.${randomBytes(3).toString('hex')}.tmp`;
  try {
    writeFileSync(tmp, text, { mode: 0o600 });
    renameSync(tmp, file);
  } catch (err) {
    rmSync(tmp, { force: true }); // never leave the temp file behind
    throw err;
  }
  return file;
}

/**
 * Write both Markdown files for the run from `rows` and `record`.
 * @param {{runId: string, rows: Row[], record: Row, now: Date, scrubCtx?: ScrubCtx, owner?: string | null}} opts
 * @returns {{binnacle: string, log: string}} the two paths
 */
export function writeAutopilotFiles({ runId, rows, record, now, scrubCtx = scrubCtxOf(record), owner = null }) {
  const binnacle = writeFileIn(runId, BINNACLE_FILE, renderBinnacleMarkdown(buildBinnacle({ runId, rows, record, now, scrubCtx, owner }), scrubCtx));
  const log = writeFileIn(runId, LOG_FILE, renderLogMarkdown({ runId, entries: buildFullLog({ runId, rows, scrubCtx }) }, scrubCtx));
  return { binnacle, log };
}

/**
 * Regenerate both files from the run's ledger. Best effort for callers that must never fail
 * (the delegate): this one throws, they catch.
 * @param {string} runId @param {{now?: Date, readRows?: (slug: string) => Promise<Row[]>}} [deps]
 */
export async function refreshAutopilotFiles(runId, deps = {}) {
  const record = await readRun(runId);
  const rows = await (deps.readRows ?? readAllRows)(record.project);
  return writeAutopilotFiles({ runId, rows, record, now: deps.now ?? new Date(), owner: await gitOwner(record.workspace) });
}

const LINK_MAX = 2048;

/**
 * `--link`: the `https` URL of the live doc. Plain URLs only: no credentials, no secret-shaped
 * text, at most {@link LINK_MAX} characters.
 * @param {unknown} text @returns {string}
 * @throws {StateError} `usage`
 */
export function parseLink(text) {
  if (typeof text !== 'string' || text.length === 0 || text.length > LINK_MAX) throw new StateError('usage', `--link must be an https URL of at most ${LINK_MAX} characters`);
  /** @type {URL} */
  let url;
  try {
    url = new URL(text);
  } catch {
    throw new StateError('usage', '--link must be an https URL, e.g. https://claude.ai/…');
  }
  if (url.protocol !== 'https:' || url.hostname === '') throw new StateError('usage', '--link must be an https URL; http and other schemes are refused');
  if (url.username !== '' || url.password !== '') throw new StateError('usage', '--link must not carry credentials');
  const link = url.href;
  if (maskSecretTokens(redact(link)) !== link) throw new StateError('usage', '--link looks like it carries a secret; give a plain URL');
  return link;
}

/**
 * Store the doc link in the ACTIVE grant: the signed `autopilot.link` row first, then the grant.
 * @param {{runId: string, link: unknown, now?: Date, writeRow?: (row: Row) => Promise<unknown>}} opts
 * @returns {Promise<string>} the stored link
 * @throws {StateError} `usage` for a bad link; `no-grant`, `grant-stopped`, `grant-expired`, `run-ended`
 */
export async function storeLink({ runId, link, now = new Date(), writeRow }) {
  const url = parseLink(link);
  await checkExpiry(runId, now, { writeRow });
  return withRunLock(runId, async () => {
    const record = await readRun(runId);
    const grant = record.autopilot;
    if (!grant || typeof grant !== 'object') throw new StateError('no-grant', `run ${runId} has no autopilot grant`);
    if (record.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const state = grantState(grant, now);
    if (state !== 'active') throw new StateError(`grant-${state}`, `run ${runId}'s autopilot grant is ${state}; the link goes into an active grant`);
    const write = writeRow ?? ((/** @type {Row} */ row) => appendRow(row, { slug: record.project }));
    await writeSigned(runId, write, { event: 'autopilot.link', grant_id: grant.grant_id, link: url, ts: now.toISOString() });
    grant.link = url;
    await saveRun(record);
    return url;
  });
}
