/**
 * Autopilot grants (issue #5, plan autopilot §1, block B45): the owner's time-boxed permission for
 * a delegate to answer some owner-level questions while the owner is away.
 *
 * A grant lives in the run record under `autopilot` (written under the run lock) and is signed in
 * the ledger:
 *   - `autopilot.grant`  — at `start`: grant_id, scopes, deny, delegate, until, caps, stop_at;
 *   - `autopilot.stop`   — at `stop`;
 *   - `autopilot.expire` — ONCE, the first time any check finds the window over.
 *
 * Expiry is lazy (no daemon): {@link checkExpiry} is called by every `autopilot` command, by
 * {@link grantFor} and — from later blocks — by `block` commands and every worker drain. The first
 * caller that sees `now >= until` on a grant still marked active takes the run lock, re-reads the
 * record, and only then writes the row and marks the grant expired, so two callers racing write
 * one row.
 *
 * Every delegated action asks {@link grantFor}`(runId, scope, now)`; a refusal sends the question
 * back to the owner. A run with no grant behaves exactly as before (`no-grant`).
 */

import { randomBytes } from 'node:crypto';
import { appendRow } from '../ledger/write.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun, saveRun, withRunLock, writeSigned } from '../state/run.mjs';
import { ALL_SCOPES, ALLOWABLE_SCOPES, BUDGET_CATEGORIES, DELEGATE_LEVELS, FIXED_DENY, isAllowable, isFixedDeny, SCOPE_CATEGORY } from './scopes.mjs';

/** @typedef {(row: Record<string, any>) => Promise<unknown>} WriteRow */
/**
 * @typedef {{
 *   grant_id: string, status: 'active' | 'stopped' | 'expired', scopes: string[], deny: string[],
 *   delegate: string, until: string, caps: Record<string, number>, stop_at: number,
 *   started_at: string, stopped_at: string | null, expired_at: string | null, link: string | null,
 *   paused?: Record<string, {at: string, spent_usd: number, reserved_usd: number, cap_usd: number}>,
 * }} Grant - `paused` (B48): the budget categories that reached `stop_at` of their cap.
 */
/** @typedef {'no-grant' | 'run-ended' | 'expired' | 'stopped' | 'denied' | 'not-allowed' | 'paused'} RefusalReason */
/**
 * A further check run on an active grant after the scope checks; it returns a refusal reason
 * string, or null to let the action through. B48 adds the per-category budget / stop-at check here.
 * @typedef {(grant: Grant, scope: string, now: Date) => Promise<string | null> | string | null} GrantCheck
 */
/** @typedef {{writeRow?: WriteRow, checks?: GrantCheck[]}} GrantDeps */

/** The longest window a grant may cover. */
export const MAX_WINDOW_MS = 24 * 60 * 60 * 1000;
/** The default `--stop-at` (plan autopilot §5 Q2). */
export const DEFAULT_STOP_AT = 0.9;

const UNTIL_PATTERN = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?(Z|([+-])(\d{2}):(\d{2}))$/;
const UNTIL_EXAMPLE = '2026-10-07T07:00:00+02:00';
const MONEY_PATTERN = /^(?:\d+(?:\.\d+)?|\.\d+)$/;

/** @param {string} message @returns {StateError} */
const usage = (message) => new StateError('usage', message);

/**
 * Epoch ms of an `--until` text, or NaN. Calendar-checked by round trip: the fields are composed
 * as UTC and must read back unchanged (`2026-02-30`, month 13, hour 24 and minute 60 all fail),
 * and the offset is at most ±23:59; only then is the offset applied.
 * @param {string} text @returns {number}
 */
function untilMs(text) {
  const m = UNTIL_PATTERN.exec(text);
  if (m === null) return Number.NaN;
  const [y, mo, d, h, mi, sec] = [1, 2, 3, 4, 5, 6].map((i) => Number(m[i] ?? 0));
  const frac = m[7] === undefined ? 0 : Number(m[7].padEnd(3, '0'));
  const local = new Date(Date.UTC(y, mo - 1, d, h, mi, sec, frac));
  local.setUTCFullYear(y); // Date.UTC maps years 0–99 to 1900–1999
  const same = local.getUTCFullYear() === y && local.getUTCMonth() === mo - 1 && local.getUTCDate() === d && local.getUTCHours() === h && local.getUTCMinutes() === mi && local.getUTCSeconds() === sec;
  if (!same) return Number.NaN;
  if (m[8] === 'Z') return local.getTime();
  const [oh, om] = [Number(m[10]), Number(m[11])];
  if (oh > 23 || om > 59) return Number.NaN;
  return local.getTime() - (m[9] === '+' ? 1 : -1) * (oh * 60 + om) * 60000;
}

/**
 * `--until`: an ISO-8601 time WITH an offset (`Z` or `±hh:mm`), after `now`, at most 24 h ahead.
 * @param {unknown} text @param {Date} now
 * @returns {string} the time as a UTC ISO string
 * @throws {StateError} `usage`
 */
export function parseUntil(text, now) {
  if (typeof text !== 'string' || text.length === 0) throw usage(`--until is required: an ISO-8601 time with an offset, e.g. ${UNTIL_EXAMPLE}`);
  const ms = untilMs(text);
  if (!Number.isFinite(ms)) throw usage(`--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. ${UNTIL_EXAMPLE}`);
  if (ms <= now.getTime()) throw usage('--until is in the past; give a time after now, at most 24 h ahead');
  if (ms - now.getTime() > MAX_WINDOW_MS) throw usage('--until is more than 24 h ahead; a grant lasts at most 24 h');
  return new Date(ms).toISOString();
}

/**
 * Split a comma list, trimming items and dropping empty ones and repeats (order kept).
 * @param {unknown} text @returns {string[]}
 */
function commaList(text) {
  if (typeof text !== 'string') return [];
  return [...new Set(text.split(',').map((s) => s.trim()).filter((s) => s.length > 0))];
}

/**
 * `--allow`: one or more allowable scopes; a fixed-deny scope is refused by name.
 * @param {unknown} text @returns {string[]}
 * @throws {StateError} `usage`
 */
export function parseAllow(text) {
  const scopes = commaList(text);
  if (scopes.length === 0) throw usage(`--allow is required: one or more of ${ALLOWABLE_SCOPES.join(', ')}`);
  for (const scope of scopes) {
    if (isFixedDeny(scope)) throw usage(`scope ${scope} is on the fixed deny list and can never be allowed (${FIXED_DENY[/** @type {keyof typeof FIXED_DENY} */ (scope)]})`);
    if (!isAllowable(scope)) throw usage(`unknown scope ${JSON.stringify(scope)} in --allow; allowable scopes: ${ALLOWABLE_SCOPES.join(', ')}`);
  }
  return scopes;
}

/**
 * `--deny`: more scopes to deny, from the allowable list (a fixed-deny scope is accepted and
 * changes nothing: it is always denied).
 * @param {unknown} text @returns {string[]} only the allowable scopes named
 * @throws {StateError} `usage`
 */
export function parseDeny(text) {
  if (text === undefined) return [];
  const scopes = commaList(text);
  if (scopes.length === 0) throw usage('--deny needs one or more scopes');
  for (const scope of scopes) {
    if (!ALL_SCOPES.includes(scope)) throw usage(`unknown scope ${JSON.stringify(scope)} in --deny; allowable scopes: ${ALLOWABLE_SCOPES.join(', ')}`);
  }
  return scopes.filter(isAllowable);
}

/**
 * `--budget <category>=<usd>,…`: a cap in USD per category.
 * @param {unknown} text @returns {Record<string, number>}
 * @throws {StateError} `usage`
 */
export function parseBudget(text) {
  /** @type {Record<string, number>} */
  const caps = {};
  if (text === undefined) return caps;
  const items = typeof text === 'string' ? text.split(',').map((s) => s.trim()).filter((s) => s.length > 0) : [];
  if (items.length === 0) throw usage('--budget needs <category>=<usd>, e.g. review=5,coding=20');
  for (const item of items) {
    const eq = item.indexOf('=');
    const category = eq < 0 ? item : item.slice(0, eq);
    if (!BUDGET_CATEGORIES.includes(category)) throw usage(`unknown budget category ${JSON.stringify(category)}; categories: ${BUDGET_CATEGORIES.join(', ')}`);
    if (Object.hasOwn(caps, category)) throw usage(`budget category ${category} given more than once`);
    const raw = eq < 0 ? '' : item.slice(eq + 1);
    const usd = MONEY_PATTERN.test(raw) ? Number(raw) : Number.NaN;
    if (!(Number.isFinite(usd) && usd > 0)) throw usage(`budget for ${category} must be a number of USD above 0, e.g. ${category}=5`);
    caps[category] = usd;
  }
  return caps;
}

/**
 * `--stop-at`: the share of a category's cap at which new sessions stop (B48); default 0.9.
 * @param {unknown} text @returns {number}
 * @throws {StateError} `usage`
 */
export function parseStopAt(text) {
  if (text === undefined) return DEFAULT_STOP_AT;
  const value = typeof text === 'string' && MONEY_PATTERN.test(text) ? Number(text) : Number.NaN;
  if (!(Number.isFinite(value) && value > 0 && value <= 1)) throw usage('--stop-at must be a number above 0 and at most 1, e.g. 0.9');
  return value;
}

/**
 * `--delegate`: the level the delegate runs at.
 * @param {unknown} text @returns {string}
 * @throws {StateError} `usage`
 */
export function parseDelegate(text) {
  if (typeof text !== 'string' || !DELEGATE_LEVELS.includes(text)) throw usage(`--delegate must be ${DELEGATE_LEVELS.join(' or ')}`);
  return text;
}

/**
 * Validate every `start` input; nothing is read or written.
 * @param {{until?: unknown, delegate?: unknown, allow?: unknown, deny?: unknown, budget?: unknown, stopAt?: unknown}} input
 * @param {Date} now
 * @returns {{until: string, delegate: string, scopes: string[], deny: string[], caps: Record<string, number>, stop_at: number}}
 * @throws {StateError} `usage`, naming the flag (and the scope, for a scope refusal)
 */
export function validateGrantInput(input, now) {
  const until = parseUntil(input.until, now);
  const delegate = parseDelegate(input.delegate);
  const scopes = parseAllow(input.allow);
  const deny = parseDeny(input.deny);
  const both = scopes.filter((s) => deny.includes(s));
  if (both.length > 0) throw usage(`scope ${both[0]} is in both --allow and --deny; name it in one of them`);
  return { until, delegate, scopes, deny, caps: parseBudget(input.budget), stop_at: parseStopAt(input.stopAt) };
}

/**
 * The grant's state at `now` (pure): a stopped grant stays stopped; an active one whose window
 * is over is expired even before its expire row is written.
 * @param {Grant} grant @param {Date} now
 * @returns {'active' | 'stopped' | 'expired'}
 */
export function grantState(grant, now) {
  if (grant.status === 'stopped') return 'stopped';
  if (grant.status === 'expired') return 'expired';
  return now.getTime() >= Date.parse(grant.until) ? 'expired' : 'active';
}

/**
 * @param {Record<string, any>} record @returns {Grant | null}
 */
const grantOf = (record) => (record.autopilot && typeof record.autopilot === 'object' ? /** @type {Grant} */ (record.autopilot) : null);

/** @param {Record<string, any>} record @param {GrantDeps} deps @returns {WriteRow} */
const writerFor = (record, deps) => deps.writeRow ?? ((row) => appendRow(row, { slug: record.project }));

/**
 * The refusal for a second grant while one is active.
 * @param {string} runId @param {string} until @returns {string}
 */
export const activeGrantMessage = (runId, until) => `run ${runId} already has an active autopilot grant until ${until}; stop it first with code-forge autopilot stop --run ${runId}`;

/**
 * The refusal for stopping a grant whose window is already over.
 * @param {string} runId @param {string} until @returns {string}
 */
export const expiredMessage = (runId, until) => `run ${runId}'s autopilot grant already expired at ${until}`;

/**
 * Lazy expiry: when the run's grant is past `until` but still marked active, mark it expired and
 * write ONE signed `autopilot.expire` row (under the run lock, re-checked after the lock is held).
 * The row's `ts` is the time of the check that found the expiry (not `until`, which the row also
 * carries). A run that has ended (`status` not active) answers `run-ended` and writes nothing:
 * its grant can no longer act, whatever its window says.
 * @param {string} runId @param {Date} [now] @param {GrantDeps} [deps]
 * @returns {Promise<{state: 'none' | 'active' | 'stopped' | 'expired' | 'run-ended', grant: Grant | null, expiredNow: boolean}>}
 */
export async function checkExpiry(runId, now = new Date(), deps = {}) {
  const record = await readRun(runId);
  const grant = grantOf(record);
  if (grant === null) return { state: 'none', grant: null, expiredNow: false };
  if (record.status !== 'active') return { state: 'run-ended', grant, expiredNow: false };
  const state = grantState(grant, now);
  if (!(state === 'expired' && grant.status === 'active')) return { state, grant, expiredNow: false };
  return withRunLock(runId, async () => {
    const fresh = await readRun(runId);
    const current = grantOf(fresh);
    if (current === null) return { state: 'none', grant: null, expiredNow: false };
    if (fresh.status !== 'active') return { state: 'run-ended', grant: current, expiredNow: false };
    if (!(grantState(current, now) === 'expired' && current.status === 'active')) return { state: grantState(current, now), grant: current, expiredNow: false };
    // the row first: a record saved without its row would never get one
    await writeSigned(runId, writerFor(fresh, deps), { event: 'autopilot.expire', grant_id: current.grant_id, until: current.until, ts: now.toISOString() });
    current.status = 'expired';
    current.expired_at = now.toISOString();
    await saveRun(fresh);
    return { state: 'expired', grant: current, expiredNow: true };
  });
}

/**
 * The one gate every delegated action asks. Order: no grant → run ended → stopped → expired (writes the
 * expire row the first time) → denied (fixed deny list or the grant's `--deny`) → not allowed →
 * paused (B48: the scope's budget category reached `stop_at` of its cap; `SCOPE_CATEGORY`) →
 * each `deps.checks` hook (its reason is passed through).
 * @param {string} runId @param {string} scope @param {Date} [now] @param {GrantDeps} [deps]
 * @returns {Promise<{ok: true, grant: Grant, reason?: undefined} | {ok: false, reason: RefusalReason | string, grant: Grant | null}>}
 * @throws {StateError} `unknown-scope` for a scope outside the vocabulary (a caller bug, not a refusal)
 */
export async function grantFor(runId, scope, now = new Date(), deps = {}) {
  if (!ALL_SCOPES.includes(scope)) throw new StateError('unknown-scope', `unknown autopilot scope ${JSON.stringify(scope)}`);
  const { state, grant } = await checkExpiry(runId, now, deps);
  if (grant === null) return { ok: false, reason: 'no-grant', grant: null };
  if (state === 'run-ended') return { ok: false, reason: 'run-ended', grant };
  if (state === 'stopped') return { ok: false, reason: 'stopped', grant };
  if (state === 'expired') return { ok: false, reason: 'expired', grant };
  if (isFixedDeny(scope) || grant.deny.includes(scope)) return { ok: false, reason: 'denied', grant };
  if (!grant.scopes.includes(scope)) return { ok: false, reason: 'not-allowed', grant };
  const category = /** @type {Record<string, string>} */ (SCOPE_CATEGORY)[scope];
  if (category !== undefined && grant.paused && Object.hasOwn(grant.paused, category)) return { ok: false, reason: 'paused', grant };
  for (const check of deps.checks ?? []) {
    const reason = await check(grant, scope, now);
    if (reason !== null) return { ok: false, reason, grant };
  }
  return { ok: true, grant };
}

/**
 * `autopilot start`: validate, then — under the run lock — refuse an ended run or a second active
 * grant, write the signed `autopilot.grant` row and store the grant. An earlier grant whose window
 * is over is expired first (its row written once), then replaced.
 * @param {{runId: string, input: Parameters<typeof validateGrantInput>[0], now?: Date, writeRow?: WriteRow, grantId?: string}} opts
 * @returns {Promise<Grant>}
 */
export async function startGrant({ runId, input, now = new Date(), writeRow, grantId }) {
  const valid = validateGrantInput(input, now);
  await checkExpiry(runId, now, { writeRow });
  return withRunLock(runId, async () => {
    const record = await readRun(runId);
    if (record.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const previous = grantOf(record);
    if (previous !== null && grantState(previous, now) === 'active') {
      throw new StateError('grant-active', activeGrantMessage(runId, previous.until));
    }
    /** @type {Grant} */
    const grant = {
      grant_id: grantId ?? `ap-${randomBytes(4).toString('hex')}`,
      status: 'active',
      ...valid,
      started_at: now.toISOString(),
      stopped_at: null,
      expired_at: null,
      link: null,
    };
    // the row first: a grant stored without its signed row would act unrecorded (fail closed)
    await writeSigned(runId, writerFor(record, { writeRow }), {
      event: 'autopilot.grant',
      grant_id: grant.grant_id,
      scopes: grant.scopes,
      deny: grant.deny,
      delegate: grant.delegate,
      until: grant.until,
      caps: grant.caps,
      stop_at: grant.stop_at,
      ts: now.toISOString(),
    });
    record.autopilot = grant;
    await saveRun(record);
    return grant;
  });
}

/**
 * `autopilot stop`: end the active grant now — the signed `autopilot.stop` row first, then the
 * grant is saved as stopped. A row that cannot be written leaves the grant active and unchanged,
 * so the stop can simply be retried (and the ledger never misses a stop that took effect).
 * An expired grant is refused (`grant-expired`; its expire row comes from the check, once).
 * @param {{runId: string, now?: Date, writeRow?: WriteRow}} opts
 * @returns {Promise<Grant>}
 */
export async function stopGrant({ runId, now = new Date(), writeRow }) {
  await checkExpiry(runId, now, { writeRow });
  return withRunLock(runId, async () => {
    const record = await readRun(runId);
    const grant = grantOf(record);
    if (grant === null) throw new StateError('no-grant', `run ${runId} has no autopilot grant`);
    if (record.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const state = grantState(grant, now);
    if (state === 'stopped') throw new StateError('grant-stopped', `run ${runId}'s autopilot grant was already stopped at ${grant.stopped_at}`);
    if (state === 'expired') throw new StateError('grant-expired', expiredMessage(runId, grant.until));
    await writeSigned(runId, writerFor(record, { writeRow }), { event: 'autopilot.stop', grant_id: grant.grant_id, ts: now.toISOString() });
    grant.status = 'stopped';
    grant.stopped_at = now.toISOString();
    await saveRun(record);
    return grant;
  });
}
