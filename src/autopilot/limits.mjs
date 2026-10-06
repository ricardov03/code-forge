/**
 * Autopilot limits (issue #5, plan autopilot §1, block B48): the budget stop per category and the
 * owner's expiring approvals.
 *
 * Budget per category. A grant's `--budget review=<usd>,coding=<usd>` caps and `--stop-at` ratio
 * are metered at the single budget gate of `spawnSession` (after the provider slot, under the
 * run's budget lock, like B41). A session's category comes from its role ({@link ROLE_CATEGORY}:
 * coder → coding, every other role → review). While a grant is active and caps the category:
 * spent in the category since the grant started + the running sessions' reservations in the
 * category ≥ stop_at × cap ⇒ the session is refused with {@link pausedMessage}, the grant is
 * paused for that category (`grant.paused[category]`) and ONE signed `autopilot.pause` row is
 * written per category per grant. `grantFor` then answers `paused` for the category's scopes
 * (`SCOPE_CATEGORY`). A paused category stays paused for the rest of the grant: the owner decides.
 * No grant (or no cap for the category) ⇒ nothing changes.
 *
 * Expiring approvals. `autopilot approve --key <dot.path> --value <json> --until <time>` (the owner,
 * at a terminal) writes the workspace `.code-forge.yml` and reloads the run through the same path
 * as `run reload` ({@link reloadWorkspace} → `reloadRun`), records the old value in the run record
 * and writes one signed `autopilot.approve` row (values redacted). {@link checkApprovals} is the
 * lazy restore, run by every `autopilot` command, every `block` command and every worker drain
 * ({@link expiryChecks}): an approval past its `until` gets its old value written back, a reload,
 * and ONE signed `autopilot.restore` row — unless the workspace value was changed by hand since
 * (it no longer equals the approved value): then nothing is overwritten and ONE signed
 * `autopilot.restore_skipped` row leaves it to the owner. Fixed keys (`IMMUTABLE_KEYS`) are refused.
 *
 * Limit keys. While a grant is active, a reload that changes a limit key (`isLimitKey`: a key in
 * `LIMIT_KEYS`, under one, or a parent of one) is refused — unless the change is exactly the one
 * an approval (or its restore) makes: {@link limitGuard} gets that change as an internal `allow`,
 * never from a user flag.
 */

import { chmod, mkdir, readFile, rename, rmdir, stat, unlink, writeFile } from 'node:fs/promises';
import { readdirSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import path from 'node:path';
import { parseDocument } from 'yaml';
import { DEFAULT_CONFIG_FILENAME, loadProjectConfig, slugFor } from '../config/load.mjs';
import { loadSeenInCache } from '../config/refresh.mjs';
import { validateConfig } from '../config/validate.mjs';
import { hasCliOnPath } from '../cli/validate.mjs';
import { isSpendRow, reservedUsd, reserveBudget, roundUsd, rowUsd } from '../ledger/spend.mjs';
import { appendRow } from '../ledger/write.mjs';
import { canonicalJSON, changedKeyPaths, IMMUTABLE_KEYS } from '../state/config-snapshot.mjs';
import { assertRunId, runsDir, StateError } from '../state/paths.mjs';
import { readRun, reloadRun, saveRun, withRunLock, writeSigned } from '../state/run.mjs';
import { redact, writeSafe } from '../util/redact.mjs';
import { pendingTickets, queueDir } from '../worker/queue.mjs';
import { repoRootOf, WorkerError } from '../worker/ticket.mjs';
import { checkExpiry, grantState, parseUntil } from './grant.mjs';
import { BUDGET_CATEGORIES, isLimitKey } from './scopes.mjs';

/** @typedef {(row: Record<string, any>) => Promise<unknown>} WriteRow */
/** @typedef {import('./grant.mjs').Grant} Grant */

/* ------------------------------------------------------------------ budget per category -- */

/** The budget category of each session role (author and facts count as review). */
export const ROLE_CATEGORY = Object.freeze({ coder: 'coding', reviewer: 'review', judge: 'review', s2: 'review', delegate: 'review', author: 'review', facts: 'review' });

/** @param {unknown} role @returns {string | null} the role's category, null for a row without a known role. */
export const categoryOfRole = (role) => (typeof role === 'string' && Object.hasOwn(ROLE_CATEGORY, role) ? /** @type {Record<string, string>} */ (ROLE_CATEGORY)[role] : null);

/**
 * The refusal text at the stop ratio.
 * @param {string} category @param {number} spent - spent + reserved in the category.
 * @param {number} cap @param {number} ratio
 */
export const pausedMessage = (category, spent, cap, ratio) => `autopilot paused: ${category} spend ${spent.toFixed(2)} of ${cap.toFixed(2)} reached the stop at ${ratio}; waiting for the owner`;

/** The fail-closed refusal when a capped category's spend cannot be read. @param {string} category */
export const categoryUnreadableMessage = (category) => `autopilot: the ${category} spend cannot be read; not starting a session`;


/** The reservation key of a category (B41's reservation map, keyed apart from the run's own). @param {string} run @param {string} category */
export const categoryReservationKey = (run, category) => `autopilot:${category}:${run}`;

/**
 * USD spent by `runId` in `category` since `since` (rows with an unknown price count 0 and are
 * counted in `unknown`; a row without a readable `ts` counts — it cannot be dated, so it is not
 * left out). Spend rows only (`isSpendRow`); a row whose role has no category is not counted.
 * @param {Array<Record<string, any>>} rows @param {string} runId @param {string} category @param {string} since
 * @returns {{usd: number, unknown: number}}
 */
export function categorySpend(rows, runId, category, since) {
  const from = Date.parse(since);
  let usd = 0;
  let unknown = 0;
  for (const row of rows) {
    if (!isSpendRow(row) || row.run !== runId || categoryOfRole(row.role) !== category) continue;
    const t = typeof row.ts === 'string' ? Date.parse(row.ts) : Number.NaN;
    if (Number.isFinite(t) && Number.isFinite(from) && t < from) continue;
    const cost = rowUsd(row);
    if (cost === null) unknown += 1;
    else usd += cost;
  }
  return { usd: roundUsd(usd), unknown };
}

/**
 * Spend per budget category since the grant started (for `autopilot status`).
 * @param {Array<Record<string, any>>} rows @param {string} runId @param {Grant} grant
 * @returns {Record<string, number>}
 */
export function spendByCategory(rows, runId, grant) {
  /** @type {Record<string, number>} */
  const out = {};
  for (const category of BUDGET_CATEGORIES) out[category] = categorySpend(rows, runId, category, grant.started_at).usd;
  return out;
}

/**
 * @typedef {{category: string, cap: number, stopAt: number, grantId: string, startedAt: string, project: string, paused: boolean, rowPending: boolean}} CategoryPlan
 */

/** The refusal when the run record cannot be read: the autopilot budget is unknown. */
export const RECORD_UNKNOWN_MESSAGE = 'autopilot: the run record cannot be read, so the autopilot budget is unknown; not starting a session';

/**
 * A known failure to read a run record: a `StateError`, a JSON parse error, or a file-system
 * error (an `E…` code). Anything else (a TypeError, …) is a bug and propagates.
 * @param {unknown} thrown @returns {boolean}
 */
const isKnownReadFailure = (thrown) =>
  thrown instanceof StateError || thrown instanceof SyntaxError || (thrown instanceof Error && typeof (/** @type {any} */ (thrown).code) === 'string' && /^E[A-Z]+$/.test(/** @type {any} */ (thrown).code));

/**
 * What the spawn gate must meter for a session of `role` in `run` at `now`: null when the run has
 * no record, no grant, an inactive grant (or run), or no cap for the role's category.
 * `spawnSession` reads it once outside its budget lock (to decide whether to lock at all) and
 * AGAIN under the lock, so a session sees a pause the previous one wrote.
 * @param {string} run @param {string} role @param {Date} now
 * @returns {Promise<CategoryPlan | null>}
 * @throws {StateError} `record-unknown` when the record exists but cannot be read (a known read
 *   failure); any other error propagates. Accepted on purpose (fail closed): an unreadable record
 *   refuses the session EVEN for a run without a grant — the record is what says whether a grant
 *   exists, so without it "no grant" cannot be known.
 */
export async function categoryPlan(run, role, now) {
  /** @type {Record<string, any>} */
  let record;
  try {
    record = await readRun(run);
  } catch (thrown) {
    if (thrown instanceof StateError && (thrown.code === 'no-run' || thrown.code === 'bad-run-id')) return null;
    if (isKnownReadFailure(thrown)) throw new StateError('record-unknown', RECORD_UNKNOWN_MESSAGE);
    throw thrown;
  }
  const grant = record.autopilot;
  if (!grant || typeof grant !== 'object' || record.status !== 'active' || grantState(grant, now) !== 'active') return null;
  const category = categoryOfRole(role);
  const cap = category === null ? undefined : grant.caps?.[category];
  if (category === null || typeof cap !== 'number' || !(cap > 0)) return null;
  const stopAt = typeof grant.stop_at === 'number' && grant.stop_at > 0 && grant.stop_at <= 1 ? grant.stop_at : 1;
  const pause = grant.paused && Object.hasOwn(grant.paused, category) ? grant.paused[category] : null;
  return { category, cap, stopAt, grantId: grant.grant_id, startedAt: grant.started_at, project: record.project, paused: pause !== null, rowPending: pause?.row_pending === true };
}

/**
 * The category check proper, called by `spawnSession`'s budget gate UNDER the run's budget lock
 * (after the `budget.usd` check, with a plan read under that lock). Refuses at the stop ratio (and
 * records the pause), else reserves `estimate` in the category until the session row is written.
 * A paused category whose pause row is still pending gets the row written now.
 * @param {{
 *   plan: CategoryPlan, run: string, role: string, block?: string | null,
 *   readRows: (() => Promise<Array<Record<string, any>>>) | null, writeRow: WriteRow | null,
 *   stderr: {write: (s: string) => unknown}, now: Date, estimate: number,
 * }} opts
 * @returns {Promise<{refused: Record<string, any>, release?: undefined} | {refused: null, release: () => void}>}
 */
export async function categoryGate({ plan, run, role, block = null, readRows, writeRow, stderr, now, estimate }) {
  const { category, cap, stopAt } = plan;
  /** @param {string} message @param {Record<string, any>} extra */
  const refuse = (message, extra) => {
    writeSafe(stderr, `code-forge: ${message}\n`);
    return { refused: { status: 'unavailable', reason: 'autopilot-paused', message, answer: null, category, cap_usd: cap, ...extra } };
  };
  /** @type {Array<Record<string, any>> | null} */
  let rows = null;
  try {
    const got = readRows ? await readRows() : null;
    rows = Array.isArray(got) ? got : null;
  } catch {
    rows = null;
  }
  if (rows === null) return refuse(categoryUnreadableMessage(category), { spent_usd: null });
  const key = categoryReservationKey(run, category);
  const spent = categorySpend(rows, run, category, plan.startedAt).usd;
  const reserved = reservedUsd(key);
  const total = roundUsd(spent + reserved);
  if (plan.paused || total >= roundUsd(stopAt * cap)) {
    if (!plan.paused || plan.rowPending) await recordPause({ plan, run, role, block, spent, reserved, writeRow, now });
    return refuse(pausedMessage(category, total, cap, stopAt), { spent_usd: spent, reserved_usd: reserved });
  }
  return { refused: null, release: reserveBudget(key, estimate) };
}

/**
 * Pause the grant for the category and write its ONE signed `autopilot.pause` row, under the run
 * lock (re-checked there, so racing sessions write one row). The paused state is saved FIRST
 * (`row_pending: true`, with the row's fields), then the row is written and the pending mark
 * cleared. A row that cannot be written leaves the category paused with the row pending: the next
 * session's check writes it (one row in the end).
 * @param {{plan: CategoryPlan, run: string, role: string, block: string | null, spent: number, reserved: number, writeRow: WriteRow | null, now: Date}} o
 * @returns {Promise<boolean>} whether this call wrote the row.
 */
async function recordPause({ plan, run, role, block, spent, reserved, writeRow, now }) {
  const write = writeRow ?? ((/** @type {Record<string, any>} */ row) => appendRow(row, { slug: plan.project }));
  return withRunLock(run, async () => {
    const record = await readRun(run);
    const grant = record.autopilot;
    if (!grant || grant.grant_id !== plan.grantId) return false;
    let pause = grant.paused && Object.hasOwn(grant.paused, plan.category) ? grant.paused[plan.category] : null;
    if (pause !== null && pause.row_pending !== true) return false; // paused and recorded already
    if (pause === null) {
      pause = { at: now.toISOString(), spent_usd: spent, reserved_usd: reserved, cap_usd: plan.cap, stop_at: plan.stopAt, role, block, row_pending: true };
      grant.paused = { ...(grant.paused ?? {}), [plan.category]: pause };
      await saveRun(record); // the pause holds even if the row cannot be written
    }
    try {
      await writeSigned(run, write, {
        event: 'autopilot.pause',
        grant_id: plan.grantId,
        category: plan.category,
        spent_usd: pause.spent_usd,
        reserved_usd: pause.reserved_usd,
        cap_usd: pause.cap_usd,
        stop_at: pause.stop_at,
        role: pause.role,
        block: pause.block,
        ts: pause.at,
      });
    } catch {
      return false; // still pending: the next check writes it
    }
    pause.row_pending = false;
    await saveRun(record);
    return true;
  });
}

/* -------------------------------------------------------------------------- limit guard -- */

/** The refusal of a limit-key change while a grant is active. @param {string} key */
export const limitRefusal = (key) => `autopilot is active: ${key} is a limit; only the owner can change it with code-forge autopilot approve`;

/**
 * @typedef {{set: unknown} | {remove: true, prune: number}} KeyChange - `prune`: how many empty
 *   parent mappings (created by the approval) to remove after the key.
 * @typedef {{segs: string[], change: KeyChange}} AllowedChange
 */

/** @param {unknown} v @returns {v is Record<string, any>} */
const isMapping = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);

/** A plain object (prototype `Object.prototype` or null): the only kind a key path descends into. @param {unknown} v @returns {v is Record<string, any>} */
const isPlainObject = (v) => isMapping(v) && (Object.getPrototypeOf(v) === Object.prototype || Object.getPrototypeOf(v) === null);

/** Key segments that could reach an object's prototype: refused in every key path. */
export const FORBIDDEN_SEGMENTS = Object.freeze(['__proto__', 'constructor', 'prototype']);

/** The refusal of a key path holding a forbidden segment. */
export const FORBIDDEN_SEGMENT_MESSAGE = '--key must not contain __proto__, constructor or prototype';

/**
 * Refuse a key path with a prototype segment (every caller, not only the flag parser).
 * @param {string[]} segs @returns {string[]}
 * @throws {StateError} `usage`
 */
export function assertSafeSegments(segs) {
  if (segs.some((seg) => FORBIDDEN_SEGMENTS.includes(seg))) throw new StateError('usage', FORBIDDEN_SEGMENT_MESSAGE);
  return segs;
}

/**
 * `obj` (a plain config) with `change` applied at `segs`, as a fresh copy.
 * @param {Record<string, any>} obj @param {string[]} segs @param {KeyChange} change
 */
export function withChange(obj, segs, change) {
  assertSafeSegments(segs);
  const copy = structuredClone(obj);
  /** @type {Record<string, any>[]} */
  const chain = [copy];
  let cur = copy;
  for (const seg of segs.slice(0, -1)) {
    // descend only into an OWN plain-object property; anything else is replaced by a fresh object
    let next = Object.hasOwn(cur, seg) ? cur[seg] : undefined;
    if (!isPlainObject(next)) {
      if ('remove' in change) return copy;
      next = {};
      Object.defineProperty(cur, seg, { value: next, enumerable: true, writable: true, configurable: true });
    }
    cur = next;
    chain.push(cur);
  }
  const last = segs[segs.length - 1];
  if ('set' in change) {
    Object.defineProperty(cur, last, { value: structuredClone(change.set), enumerable: true, writable: true, configurable: true });
    return copy;
  }
  if (Object.hasOwn(cur, last)) delete cur[last];
  for (let i = 0; i < change.prune; i += 1) {
    const depth = segs.length - 1 - i; // chain[depth] is the parent mapping of segs[depth]
    if (depth < 1 || Object.keys(chain[depth]).length > 0) break;
    delete chain[depth - 1][segs[depth - 1]];
  }
  return copy;
}

/**
 * `cfg` without the key at `segs` and without the mappings on its path that are left empty.
 * @param {Record<string, any>} cfg @param {string[]} segs
 */
const withoutKey = (cfg, segs) => withChange(cfg, segs, { remove: true, prune: segs.length - 1 });

/**
 * The `reloadRun` guard: while the record's grant is active, any changed limit key is refused —
 * except at the key `allow` names (an approval or its restore; internal, never a flag).
 *
 * Which view is compared: BOTH sides are the migrated config — the run's snapshot (`before`) and
 * the freshly loaded file (`after`) — with the allowed key (and the parents left empty by removing
 * it) taken out of both. So the guard never compares the key's VALUE: whatever migration or a
 * default fills there in the snapshot cannot make an approval's restore look like a second change,
 * while ANY other difference (a hand edit made meanwhile) is still caught. The approval's old
 * value / absence / prune count come from the raw file on purpose: the raw file is what the
 * restore writes back. A key that migration MOVES elsewhere would show up as another change and
 * be refused (fail closed).
 * @param {AllowedChange | null} allow @param {Date} now
 */
export function limitGuard(allow, now) {
  /** @type {(record: Record<string, any>, before: Record<string, any> | undefined, after: Record<string, any>, changed: string[] | null) => void} */
  return (record, before, after, changed) => {
    const grant = record.autopilot;
    if (!grant || typeof grant !== 'object' || grantState(grant, now) !== 'active') return;
    let paths = changed;
    if (allow !== null && before !== undefined) paths = changedKeyPaths(withoutKey(before, allow.segs), withoutKey(after, allow.segs));
    const hit = paths === null ? 'the whole config' : paths.find((p) => isLimitKey(p));
    if (hit !== undefined) throw new StateError('autopilot-limit', limitRefusal(hit));
  };
}

/* -------------------------------------------------------------------- workspace reload -- */

/**
 * @typedef {{
 *   ok: boolean, error?: 'not-found' | 'read-error' | 'parse-error' | 'migrate-error' | 'invalid', line?: string, code?: string, errors?: Array<{rule: string, path?: string}>,
 *   changed?: string[] | null, oldHash?: string | null, newHash?: string, pinned?: number, rowError?: string | null,
 * }} ReloadOutcome - `ok: true` carries the `reloadRun` result; `ok: false` the `error` (with `line`
 *   for a parse error, `errors` for an invalid config).
 */

/**
 * The `run reload` path (B35), shared by the verb, `autopilot approve` and the lazy restore: load
 * and validate the workspace config, pin the queue's pending tickets, `reloadRun` with the
 * autopilot {@link limitGuard} — ALWAYS: every caller (the `run reload` verb included) goes through
 * the guard. `allow` is internal: only `approve` and its restore pass it; the verb never does.
 * @param {{runId: string, allow?: AllowedChange | null, writeRow?: WriteRow, now?: Date}} opts
 * @returns {Promise<ReloadOutcome>}
 * @throws {StateError} `run-ended`, `immutable-key`, `autopilot-limit`, `queue-unreadable`, …
 */
export async function reloadWorkspace({ runId, allow = null, writeRow, now = new Date() }) {
  const { project, workspace, status } = await readRun(runId);
  if (status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
  const loaded = await loadProjectConfig(workspace);
  if (!loaded.ok || !loaded.config) {
    if (loaded.error === 'not-found') return { ok: false, error: 'not-found' };
    if (loaded.error === 'parse-error') {
      // never the parser's message: it can quote a line of the file
      const line = /\(line (\d+), column \d+\)$/.exec(loaded.message ?? '')?.[1];
      return { ok: false, error: 'parse-error', ...(line ? { line } : {}) };
    }
    if (loaded.error === 'migrate-error') return { ok: false, error: 'migrate-error' };
    // a read error: the errno code only, never the OS text (it carries the full path)
    const code = /could not read .*: (E[A-Z]+)$/.exec(loaded.message ?? '')?.[1];
    return { ok: false, error: 'read-error', ...(code ? { code } : {}) };
  }
  const result = validateConfig(loaded.config, { seenInCache: await loadSeenInCache(), hasCliOnPath });
  if (!result.valid) return { ok: false, error: 'invalid', errors: result.errors.map((e) => ({ rule: e.rule, ...(typeof e.path === 'string' && e.path.length > 0 ? { path: e.path } : {}) })) };
  /** @type {string | null} */
  let repoRoot = null;
  try {
    repoRoot = await repoRootOf(workspace);
  } catch (thrown) {
    if (!(thrown instanceof WorkerError && thrown.code === 'no-repo')) throw thrown;
    // no repository: there is no review queue to pin
  }
  const readPending = () => {
    if (repoRoot === null) return [];
    try {
      readdirSync(queueDir(repoRoot));
    } catch (thrown) {
      if (thrown?.code === 'ENOENT') return [];
      throw new StateError('queue-unreadable', 'cannot read the review queue; nothing changed');
    }
    return pendingTickets(repoRoot);
  };
  const write = writeRow ?? ((/** @type {Record<string, any>} */ row) => appendRow(row, { slug: project }));
  const reloaded = await reloadRun({ runId, config: loaded.config, readPending, effectiveSlug: slugFor(loaded.config, workspace), writeRow: write, now, guard: limitGuard(allow, now) });
  return { ok: true, ...reloaded };
}

/** A failed reload as one plain line (rule ids and key paths only). @param {ReloadOutcome} res */
export function reloadFailureText(res) {
  if (res.ok) return '';
  if (res.error === 'not-found') return `no ${DEFAULT_CONFIG_FILENAME} in the workspace`;
  if (res.error === 'read-error') return `${DEFAULT_CONFIG_FILENAME} could not be read${res.code ? ` (${res.code})` : ''}`;
  if (res.error === 'parse-error') return `the config could not be parsed (${DEFAULT_CONFIG_FILENAME}${res.line ? `:${res.line}` : ''})`;
  if (res.error === 'migrate-error') return `${DEFAULT_CONFIG_FILENAME} could not be migrated to the current format`;
  return `${DEFAULT_CONFIG_FILENAME} would be invalid (${(res.errors ?? []).map((e) => `[${e.rule}]${e.path ? ` ${e.path}` : ''}`).join(', ')})`;
}

/* ------------------------------------------------------------------ expiring approvals -- */

/**
 * @typedef {{
 *   approval_id: string, key: string, new: unknown, old: unknown, old_absent: boolean, prune: number,
 *   until: string, approved_at: string, status: 'active' | 'restored' | 'restore_skipped', ended_at: string | null,
 *   finish_pending?: {status: 'restored' | 'restore_skipped', row: Record<string, any>},
 *   restoring?: boolean,
 * }} Approval - stored in the run record (outside the workspace, 0600) under `autopilot_approvals`.
 */

const KEY_SEGMENT = /^[A-Za-z0-9_-]+$/;
const LOCK_TIMEOUT_MS = 10000;
const LOCK_POLL_MS = 25;

/** @param {string} message */
const usage = (message) => new StateError('usage', message);

/**
 * `--key`: a dotted path of plain segments (letters, digits, `_`, `-`).
 * @param {unknown} key @returns {string[]}
 */
export function parseKeyPath(key) {
  if (typeof key !== 'string' || key.length === 0) throw usage('--key is required: a dotted config key path, e.g. review.max_rounds_per_file');
  const segs = key.split('.');
  if (!segs.every((s) => KEY_SEGMENT.test(s))) throw usage('--key must be a dotted path of plain segments (letters, digits, _ and -), e.g. review.max_rounds_per_file');
  return assertSafeSegments(segs);
}

/**
 * The fixed key `key` would touch (equal, under, or a parent of one), or null.
 * @param {string} key @returns {string | null}
 */
export function immutableHit(key) {
  return Object.keys(IMMUTABLE_KEYS).find((k) => key === k || key.startsWith(`${k}.`) || k.startsWith(`${key}.`)) ?? null;
}

/** @param {string} a @param {string} b @returns {boolean} the two key paths overlap (equal, or one under the other). */
const overlaps = (a, b) => a === b || a.startsWith(`${b}.`) || b.startsWith(`${a}.`);

/** @param {Record<string, any>} record @returns {Approval[]} */
const approvalsOf = (record) => (Array.isArray(record.autopilot_approvals) ? record.autopilot_approvals : []);

/** @param {Record<string, any>} record @param {{writeRow?: WriteRow}} deps @returns {WriteRow} */
const writerFor = (record, deps) => deps.writeRow ?? ((row) => appendRow(row, { slug: record.project }));

/**
 * Validate an `approve` before anyone is asked: key, fixed keys, the JSON value, `--until`, an
 * active run and no active approval on an overlapping key. Nothing is written.
 * @param {{runId: string, key: unknown, value: unknown, until: unknown, now: Date}} input
 * @returns {Promise<{key: string, segs: string[], value: unknown, until: string}>}
 * @throws {StateError} `usage`, `immutable-key`, `run-ended`, `approval-active`
 */
export async function validateApproval({ runId, key, value, until, now }) {
  const segs = parseKeyPath(key);
  const keyPath = segs.join('.');
  const fixed = immutableHit(keyPath);
  if (fixed !== null) throw new StateError('immutable-key', `${keyPath} cannot be approved: ${fixed} cannot change mid-run (${IMMUTABLE_KEYS[/** @type {keyof typeof IMMUTABLE_KEYS} */ (fixed)]})`);
  if (typeof value !== 'string') throw usage('--value is required: a JSON value, e.g. 4, true, "text" or {"a": 1}');
  /** @type {unknown} */
  let parsed;
  try {
    parsed = JSON.parse(value);
  } catch {
    throw usage('--value must be JSON, e.g. 4, true, "text" or {"a": 1}'); // never the value itself
  }
  const untilIso = parseUntil(until, now);
  const record = await readRun(runId);
  if (record.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
  const clash = approvalsOf(record).find((a) => a.status === 'active' && overlaps(a.key, keyPath));
  if (clash) throw new StateError('approval-active', `an approval for ${clash.key} is active until ${clash.until}; it must end first`);
  return { key: keyPath, segs, value: parsed, until: untilIso };
}

/**
 * Serialize approvals and restores of one run across processes (a `mkdir` lock next to the run
 * record). It wraps `reloadRun`, which takes the run lock itself, so it is a lock of its own.
 * @template T @param {string} runId @param {() => Promise<T>} fn @returns {Promise<T>}
 */
async function withApprovalsLock(runId, fn) {
  await mkdir(runsDir(), { recursive: true, mode: 0o700 });
  const lock = path.join(runsDir(), `${assertRunId(runId)}.approvals.lock`);
  const deadline = Date.now() + LOCK_TIMEOUT_MS;
  for (;;) {
    try {
      await mkdir(lock);
      break;
    } catch (err) {
      if (err.code !== 'EEXIST') throw err;
      if (Date.now() > deadline) throw new StateError('locked', `run ${runId}'s approvals are locked (${lock}); remove it if no forge verb is running`);
      await new Promise((resolve) => setTimeout(resolve, LOCK_POLL_MS));
    }
  }
  let failed = false;
  try {
    return await fn();
  } catch (thrown) {
    failed = true;
    throw thrown;
  } finally {
    try {
      await rmdir(lock);
    } catch (rmError) {
      // never mask fn's own error; a lock left behind after a success is reported
      if (!failed) throw rmError; // eslint-disable-line no-unsafe-finally
    }
  }
}

/**
 * The workspace config as an editable YAML document.
 * @param {string} workspace
 * @returns {Promise<{file: string, text: string, doc: import('yaml').Document, js: Record<string, any>}>}
 */
async function readWorkspace(workspace) {
  const file = path.join(workspace, DEFAULT_CONFIG_FILENAME);
  let text;
  try {
    text = await readFile(file, 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') throw new StateError('no-config', `no ${DEFAULT_CONFIG_FILENAME} in the workspace`);
    throw new StateError('config-read', `${DEFAULT_CONFIG_FILENAME} could not be read${typeof err.code === 'string' ? ` (${err.code})` : ''}`);
  }
  const doc = parseDocument(text, { prettyErrors: false });
  const js = doc.errors.length === 0 ? doc.toJS() : null;
  if (!isMapping(js)) throw new StateError('config-parse', `${DEFAULT_CONFIG_FILENAME} could not be parsed`);
  return { file, text, doc, js };
}

/**
 * Atomic text write that keeps the file's mode (a new file gets the default 0644); the temp file is
 * removed when the write or the rename fails.
 * @param {string} file @param {string} text
 */
async function writeText(file, text) {
  let mode = 0o644;
  try {
    mode = (await stat(file)).mode & 0o7777;
  } catch {
    // no file yet: the default mode
  }
  const tmp = `${file}.${process.pid}.${randomBytes(3).toString('hex')}.tmp`;
  try {
    await writeFile(tmp, text, { mode });
    await chmod(tmp, mode); // the umask may have narrowed it
    await rename(tmp, file);
  } catch (thrown) {
    await unlink(tmp).catch(() => null);
    throw thrown;
  }
}

/**
 * @param {Record<string, any>} js @param {string[]} segs
 * @returns {{present: boolean, value: unknown, missing: number}} `missing`: how many trailing parent
 *   mappings do not exist yet (they are created by a set).
 */
function valueAt(js, segs) {
  assertSafeSegments(segs);
  /** @type {unknown} */
  let cur = js;
  for (let i = 0; i < segs.length; i += 1) {
    if (!isPlainObject(cur) || !Object.hasOwn(cur, segs[i])) return { present: false, value: undefined, missing: isPlainObject(cur) ? segs.length - 1 - i : 0 };
    cur = cur[segs[i]];
  }
  return { present: true, value: cur, missing: 0 };
}

/**
 * Apply `change` to the YAML document (comments and layout elsewhere kept).
 * @param {import('yaml').Document} doc @param {string[]} segs @param {KeyChange} change
 */
function editDoc(doc, segs, change) {
  assertSafeSegments(segs);
  try {
    if ('set' in change) {
      doc.setIn(segs, doc.createNode(change.set));
      return;
    }
    doc.deleteIn(segs);
    for (let i = 0; i < change.prune; i += 1) {
      const parent = segs.slice(0, segs.length - 1 - i);
      if (parent.length === 0) break;
      const node = /** @type {any} */ (doc.getIn(parent, true));
      if (!node || !Array.isArray(node.items) || node.items.length > 0) break;
      doc.deleteIn(parent);
    }
  } catch {
    throw usage(`${segs.join('.')} cannot be set: a parent of it is not a mapping in ${DEFAULT_CONFIG_FILENAME}`);
  }
}

/** The change that puts an approval's old value back. @param {Approval} a @returns {KeyChange} */
const revertChange = (a) => (a.old_absent ? { remove: true, prune: a.prune } : { set: a.old });

/**
 * `autopilot approve` after the owner confirmed: write the workspace config, reload (the approved
 * change is the one limit change the guard lets through), then — under the run lock — write the
 * signed `autopilot.approve` row and store the approval. Any failure puts the file back (and,
 * after a reload, reloads the old value) and throws: nothing is half-approved.
 * @param {{runId: string, key: string, segs: string[], value: unknown, until: string, now?: Date, writeRow?: WriteRow, approvalId?: string}} opts
 * @returns {Promise<Approval>}
 */
export async function approveChange({ runId, key, segs, value, until, now = new Date(), writeRow, approvalId }) {
  return withApprovalsLock(runId, async () => {
    const record = await readRun(runId);
    if (record.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const clash = approvalsOf(record).find((a) => a.status === 'active' && overlaps(a.key, key));
    if (clash) throw new StateError('approval-active', `an approval for ${clash.key} is active until ${clash.until}; it must end first`);
    const ws = await readWorkspace(record.workspace);
    const before = valueAt(ws.js, segs);
    if (before.present && canonicalJSON(before.value) === canonicalJSON(value)) throw new StateError('no-change', `${key} already has that value in ${DEFAULT_CONFIG_FILENAME}; nothing to approve`);
    /** @type {Approval} */
    const approval = {
      approval_id: approvalId ?? `apr-${randomBytes(4).toString('hex')}`,
      key,
      new: value,
      old: before.present ? before.value : null,
      old_absent: !before.present,
      prune: before.present ? 0 : before.missing,
      until,
      approved_at: now.toISOString(),
      status: 'active',
      ended_at: null,
    };
    editDoc(ws.doc, segs, { set: value });
    await writeText(ws.file, String(ws.doc));
    /** @type {ReloadOutcome} */
    let res;
    try {
      res = await reloadWorkspace({ runId, allow: { segs, change: { set: value } }, writeRow, now });
    } catch (thrown) {
      await writeText(ws.file, ws.text);
      throw thrown;
    }
    if (!res.ok) {
      await writeText(ws.file, ws.text);
      throw new StateError('config-invalid', `${reloadFailureText(res)} — nothing changed`);
    }
    try {
      await withRunLock(runId, async () => {
        const fresh = await readRun(runId);
        await writeSigned(runId, writerFor(fresh, { writeRow }), {
          event: 'autopilot.approve',
          approval_id: approval.approval_id,
          key,
          old: approval.old_absent ? null : redact(approval.old),
          old_absent: approval.old_absent,
          new: redact(value),
          until,
          new_hash: res.ok ? res.newHash : null,
          ts: now.toISOString(),
        });
        fresh.autopilot_approvals = [...approvalsOf(fresh), approval];
        await saveRun(fresh);
      });
    } catch (thrown) {
      // the row or the record failed: undo the change so nothing stands unrecorded
      await writeText(ws.file, ws.text);
      await reloadWorkspace({ runId, allow: { segs, change: revertChange(approval) }, writeRow, now }).catch(() => null);
      throw thrown;
    }
    return approval;
  });
}

/**
 * @typedef {{restored: string[], skipped: string[], failed: Array<{approval_id: string, reason: string}>}} ApprovalCheck
 */

/**
 * The lazy restore: every active approval of the run whose `until` is at or before `now` is
 * restored (old value written back, reload, ONE signed `autopilot.restore` row) — or, when the
 * workspace value no longer equals the approved value (changed by hand), left alone with ONE
 * signed `autopilot.restore_skipped` row. A restore that cannot load or reload the config puts the
 * file back and stays active (reported in `failed`; the next check tries again). For a run that
 * has ended the old value is written back without a reload. Crash safety: `restoring: true` is
 * saved on the approval BEFORE the file is touched; a later check that finds it finishes the
 * restore (no rewrite when the file already holds the old value) and never reads it as a change
 * by hand.
 * @param {string} runId
 * @param {Date | (() => Date)} [clock] - a clock is read only when the run has an active approval
 *   (a caller's fake clock is not advanced by a run without any).
 * @param {{writeRow?: WriteRow}} [deps]
 * @returns {Promise<ApprovalCheck>}
 */
export async function checkApprovals(runId, clock = new Date(), deps = {}) {
  /** @type {ApprovalCheck} */
  const out = { restored: [], skipped: [], failed: [] };
  /** @type {Record<string, any>} */
  let record;
  try {
    record = await readRun(runId);
  } catch (thrown) {
    if (thrown instanceof StateError && (thrown.code === 'no-run' || thrown.code === 'bad-run-id')) return out;
    throw thrown;
  }
  if (!approvalsOf(record).some((a) => a.status === 'active')) return out;
  const now = typeof clock === 'function' ? clock() : clock;
  /** @param {Approval} a */
  const due = (a) => a.status === 'active' && now.getTime() >= Date.parse(a.until);
  if (!approvalsOf(record).some(due)) return out;
  return withApprovalsLock(runId, async () => {
    const fresh = await readRun(runId);
    for (const approval of approvalsOf(fresh).filter(due)) {
      /**
       * Write the signed row, then end the approval. When the row cannot be written the approval
       * stays active with the finished step recorded (`finish_pending`): the next check writes the
       * row without touching the file again.
       * @param {'restored' | 'restore_skipped'} status @param {Record<string, any>} row
       * @returns {Promise<boolean>} whether the row was written now
       */
      const finish = (status, row) =>
        withRunLock(runId, async () => {
          const latest = await readRun(runId);
          const stored = approvalsOf(latest).find((a) => a.approval_id === approval.approval_id);
          if (!stored || stored.status !== 'active') return false;
          const pending = stored.finish_pending ?? { status, row: { approval_id: approval.approval_id, key: approval.key, until: approval.until, ...row, ts: now.toISOString() } };
          try {
            await writeSigned(runId, writerFor(latest, deps), pending.row);
          } catch {
            stored.finish_pending = pending;
            await saveRun(latest);
            return false;
          }
          delete stored.finish_pending;
          stored.status = pending.status;
          stored.ended_at = pending.row.ts;
          await saveRun(latest);
          return true;
        });
      /** @param {'restored' | 'restore_skipped'} status @param {Record<string, any>} row */
      const report = async (status, row) => {
        if (await finish(status, row)) (status === 'restored' ? out.restored : out.skipped).push(approval.approval_id);
        else out.failed.push({ approval_id: approval.approval_id, reason: 'restore-incomplete' });
      };
      if (approval.finish_pending) {
        // the file step is done; only the signed row is missing
        await report(approval.finish_pending.status, {});
        continue;
      }
      /** @type {string[]} */
      let segs;
      try {
        segs = assertSafeSegments(approval.key.split('.'));
      } catch {
        out.failed.push({ approval_id: approval.approval_id, reason: 'unsafe key' });
        continue;
      }
      /** @type {Awaited<ReturnType<typeof readWorkspace>>} */
      let ws;
      try {
        ws = await readWorkspace(fresh.workspace);
      } catch (thrown) {
        out.failed.push({ approval_id: approval.approval_id, reason: thrown?.message ?? String(thrown) });
        continue;
      }
      const current = valueAt(ws.js, segs);
      const holdsOld = approval.old_absent ? !current.present : current.present && canonicalJSON(current.value) === canonicalJSON(approval.old);
      if (approval.restoring !== true) {
        if (!current.present || canonicalJSON(current.value) !== canonicalJSON(approval.new)) {
          await report('restore_skipped', { event: 'autopilot.restore_skipped', reason: 'changed-by-hand' });
          continue;
        }
        // crash safety: the restore is recorded as started BEFORE the file is touched, so a check
        // after a crash finishes it instead of reading the old value as a change by hand
        await withRunLock(runId, async () => {
          const latest = await readRun(runId);
          const stored = approvalsOf(latest).find((a) => a.approval_id === approval.approval_id);
          if (stored) stored.restoring = true;
          await saveRun(latest);
        });
      }
      const change = revertChange(approval);
      if (!holdsOld) {
        try {
          editDoc(ws.doc, segs, change);
          await writeText(ws.file, String(ws.doc));
        } catch (thrown) {
          out.failed.push({ approval_id: approval.approval_id, reason: thrown?.message ?? String(thrown) });
          continue;
        }
      }
      /** @type {string | null} */
      let newHash = null;
      if (fresh.status === 'active') {
        /** @type {string | null} */
        let failure = null;
        try {
          const res = await reloadWorkspace({ runId, allow: { segs, change }, writeRow: deps.writeRow, now });
          if (res.ok) newHash = res.newHash;
          else failure = reloadFailureText(res);
        } catch (thrown) {
          failure = thrown?.message ?? String(thrown);
        }
        if (failure !== null) {
          await writeText(ws.file, ws.text);
          out.failed.push({ approval_id: approval.approval_id, reason: failure });
          continue;
        }
      }
      await report('restored', { event: 'autopilot.restore', restored: approval.old_absent ? null : redact(approval.old), old_absent: approval.old_absent, reloaded: fresh.status === 'active', new_hash: newHash });
    }
    return out;
  });
}

/**
 * The lazy checks every `block` command and every worker drain run (and every `autopilot`
 * command, which runs the grant expiry itself): the grant's expiry, then the approvals' restore.
 * @param {string} runId @param {Date} [now] @param {{writeRow?: WriteRow}} [deps]
 * @returns {Promise<ApprovalCheck>}
 */
export async function expiryChecks(runId, now = new Date(), deps = {}) {
  try {
    await checkExpiry(runId, now, deps);
  } catch (thrown) {
    if (!(thrown instanceof StateError && (thrown.code === 'no-run' || thrown.code === 'bad-run-id'))) throw thrown;
  }
  return checkApprovals(runId, now, deps);
}

/**
 * `ApprovalCheck` as stderr lines (ids and key paths only), for the CLI callers.
 * @param {ApprovalCheck} res @returns {string}
 */
export function approvalCheckText(res) {
  return [
    ...res.restored.map((id) => `autopilot: approval ${id} expired; the old value is back\n`),
    ...res.skipped.map((id) => `autopilot: approval ${id} expired but its key was changed by hand; left for the owner\n`),
    ...res.failed.map((f) =>
      f.reason === 'restore-incomplete'
        ? `autopilot: WARN approval ${f.approval_id}: its signed row could not be written yet (restore-incomplete); the next check writes it\n`
        : `autopilot: WARN approval ${f.approval_id} could not be restored yet (${f.reason})\n`,
    ),
  ].join('');
}
