// B45 autopilot grants: input validation (scope vocabulary, fixed deny list, window), the grantFor
// matrix on a fake clock, lazy expiry (one autopilot.expire row), start/stop rows. One per-file
// temp parent, removed in `after()`; HOME points into it before any `src` module loads.
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-ap-grant-'));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { checkExpiry, grantFor, startGrant, stopGrant, validateGrantInput } = await import('../../src/autopilot/grant.mjs');
const { ALLOWABLE_SCOPES, FIXED_DENY, FIXED_DENY_SCOPES, isLimitKey } = await import('../../src/autopilot/scopes.mjs');
const { endRun } = await import('../../src/state/run.mjs');
const { readRun, startRun } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');

const T0 = new Date('2026-10-06T20:00:00.000Z');
/** @param {number} minutes */
const at = (minutes) => new Date(T0.getTime() + minutes * 60000);
const UNTIL = '2026-10-07T00:00:00+02:00'; // T0 + 2 h
const UNTIL_UTC = '2026-10-06T22:00:00.000Z';
const BASE = { until: UNTIL, delegate: 'L2', allow: 'waive:nit,waive:warning,round:extra' };

/** A run with its own ledger sink. @param {string} runId */
async function newRun(runId) {
  /** @type {Record<string, any>[]} */
  const rows = [];
  const writeRow = async (/** @type {Record<string, any>} */ row) => void rows.push(row);
  const workspace = path.join(PARENT, `ws-${runId}`);
  mkdirSync(workspace, { recursive: true });
  await startRun({ workspace, project: 'proj', runId, writeRow, now: T0 });
  rows.length = 0; // only the autopilot rows from here on
  return { rows, writeRow };
}

/** @param {() => unknown} fn @param {string} message */
const refuses = (fn, message) => assert.throws(fn, { name: 'StateError', code: 'usage', message });

test('the vocabulary: 4 allowable scopes, 9 fixed-deny scopes, no overlap', () => {
  assert.deepEqual([...ALLOWABLE_SCOPES], ['waive:warning', 'waive:nit', 'round:extra', 'model:choose']);
  assert.deepEqual([...FIXED_DENY_SCOPES], ['waive:critical', 'waive:proof', 'reviews:skip', 'limits:change', 'plan:approve', 'design:approve', 'pr:merge', 'destructive', 'budget:raise']);
  assert.equal(ALLOWABLE_SCOPES.filter((s) => FIXED_DENY_SCOPES.includes(s)).length, 0);
  // a limit key, a key under one, and a PARENT of one (replacing `review` replaces its limits) are all covered
  const keys = ['', 'review', 'budget', 'thresholds', 'review.max_rounds_per_file', 'budget.usd', 'review.allow_open_book_codex', 'thresholds.x', 'production', 'project.slug', 'review.session_timeout_s', 'budgetx', 'levels.L2.model', 'levels', 'autopilot.min_confidence', 'autopilotx'];
  assert.deepEqual(
    keys.filter((k) => isLimitKey(k)),
    ['', 'review', 'budget', 'thresholds', 'review.max_rounds_per_file', 'budget.usd', 'review.allow_open_book_codex', 'thresholds.x', 'production', 'autopilot.min_confidence'],
  );
  assert.equal(
    FIXED_DENY['limits:change'],
    'change a limit or rule key (budget.*, review.budgets, review.block_budget_tokens, review.block_budget_usd, review.max_rounds_per_file, review.single_reviewer_max_risk, review.multimodel, review.closed_book, review.allow_open_book_codex, thresholds.*, escalation.*, proof.*, system1.*, production.*, autopilot.*)',
  );
});

test('every fixed-deny scope in --allow is refused at start, naming the scope (9 of 9, literal texts)', () => {
  const what = {
    'waive:critical': 'waive a critical finding',
    'waive:proof': 'waive a proof finding',
    'reviews:skip': 'close a block without its reviews (--no-require-reviews)',
    'limits:change': 'change a limit or rule key (budget.*, review.budgets, review.block_budget_tokens, review.block_budget_usd, review.max_rounds_per_file, review.single_reviewer_max_risk, review.multimodel, review.closed_book, review.allow_open_book_codex, thresholds.*, escalation.*, proof.*, system1.*, production.*, autopilot.*)',
    'plan:approve': 'approve a plan',
    'design:approve': 'approve a design',
    'pr:merge': 'merge a pull request',
    destructive: 'run a destructive action',
    'budget:raise': 'raise a budget',
  };
  assert.deepEqual(Object.keys(what), [...FIXED_DENY_SCOPES]);
  let refused = 0;
  for (const [scope, text] of Object.entries(what)) {
    refuses(() => validateGrantInput({ ...BASE, allow: `waive:nit,${scope}` }, T0), `scope ${scope} is on the fixed deny list and can never be allowed (${text})`);
    refused += 1;
  }
  assert.equal(refused, 9);
});

test('scope refusals: unknown in --allow or --deny, a scope in both, an empty --allow', () => {
  const allowable = 'allowable scopes: waive:warning, waive:nit, round:extra, model:choose';
  refuses(() => validateGrantInput({ ...BASE, allow: 'waive:nit,waive:all' }, T0), `unknown scope "waive:all" in --allow; ${allowable}`);
  refuses(() => validateGrantInput({ ...BASE, deny: 'nope' }, T0), `unknown scope "nope" in --deny; ${allowable}`);
  refuses(() => validateGrantInput({ ...BASE, deny: 'waive:nit' }, T0), 'scope waive:nit is in both --allow and --deny; name it in one of them');
  refuses(() => validateGrantInput({ ...BASE, allow: ' , ' }, T0), `--allow is required: one or more of waive:warning, waive:nit, round:extra, model:choose`);
  refuses(() => validateGrantInput({ ...BASE, allow: undefined }, T0), `--allow is required: one or more of waive:warning, waive:nit, round:extra, model:choose`);
  // a fixed-deny scope in --deny is accepted and changes nothing (it is always denied)
  assert.deepEqual(validateGrantInput({ ...BASE, allow: 'waive:nit', deny: 'model:choose,pr:merge' }, T0).deny, ['model:choose']);
});

test('--until: offset required, after now, at most 24 h ahead (24 h exactly is accepted)', () => {
  const example = 'e.g. 2026-10-07T07:00:00+02:00';
  refuses(() => validateGrantInput({ ...BASE, until: undefined }, T0), `--until is required: an ISO-8601 time with an offset, ${example}`);
  refuses(() => validateGrantInput({ ...BASE, until: '2026-10-06T23:00:00' }, T0), `--until must be an ISO-8601 time with an offset (Z or ±hh:mm), ${example}`);
  refuses(() => validateGrantInput({ ...BASE, until: 'tomorrow' }, T0), `--until must be an ISO-8601 time with an offset (Z or ±hh:mm), ${example}`);
  refuses(() => validateGrantInput({ ...BASE, until: '2026-10-06T20:00:00Z' }, T0), '--until is in the past; give a time after now, at most 24 h ahead');
  refuses(() => validateGrantInput({ ...BASE, until: '2026-10-07T20:00:01Z' }, T0), '--until is more than 24 h ahead; a grant lasts at most 24 h');
  refuses(() => validateGrantInput({ ...BASE, until: '2026-10-07T20:01:00Z' }, T0), '--until is more than 24 h ahead; a grant lasts at most 24 h'); // 24 h + 1 min
  assert.equal(validateGrantInput({ ...BASE, until: '2026-10-07T20:00:00Z' }, T0).until, '2026-10-07T20:00:00.000Z'); // 24 h exactly
  assert.equal(validateGrantInput({ ...BASE, until: '2026-10-07T22:00:00+02:00' }, T0).until, '2026-10-07T20:00:00.000Z');
  assert.equal(validateGrantInput(BASE, T0).until, UNTIL_UTC);
  // calendar-invalid fields are refused (round trip), not rolled over into another day
  const calendar = ['2026-02-30T07:00:00Z', '2026-13-01T07:00:00Z', '2026-10-32T07:00:00Z', '2026-10-06T24:00:00Z', '2026-10-06T21:60:00Z', '2026-10-06T21:00:61Z', '2026-10-06T21:00:00+24:00', '2026-10-06T21:00:00+02:60'];
  for (const until of calendar) refuses(() => validateGrantInput({ ...BASE, until }, T0), `--until must be an ISO-8601 time with an offset (Z or ±hh:mm), ${example}`);
  assert.deepEqual(
    ['2026-10-06T23:30:00.5+01:00', '2026-10-06T15:00-05:30', '2026-10-07T09:00:00+12:00'].map((until) => validateGrantInput({ ...BASE, until }, T0).until),
    ['2026-10-06T22:30:00.500Z', '2026-10-06T20:30:00.000Z', '2026-10-06T21:00:00.000Z'],
  );
});

test('--delegate, --budget and --stop-at: accepted values and exact refusals', () => {
  refuses(() => validateGrantInput({ ...BASE, delegate: 'L1' }, T0), '--delegate must be L2 or L3');
  refuses(() => validateGrantInput({ ...BASE, budget: 'travel=5' }, T0), 'unknown budget category "travel"; categories: coding, review');
  refuses(() => validateGrantInput({ ...BASE, budget: 'review=0' }, T0), 'budget for review must be a number of USD above 0, e.g. review=5');
  refuses(() => validateGrantInput({ ...BASE, budget: 'review=5,review=6' }, T0), 'budget category review given more than once');
  refuses(() => validateGrantInput({ ...BASE, stopAt: '0' }, T0), '--stop-at must be a number above 0 and at most 1, e.g. 0.9');
  refuses(() => validateGrantInput({ ...BASE, stopAt: '1.5' }, T0), '--stop-at must be a number above 0 and at most 1, e.g. 0.9');
  assert.deepEqual(validateGrantInput({ ...BASE, delegate: 'L3', budget: 'review=5,coding=20.5', stopAt: '1' }, T0), {
    until: UNTIL_UTC,
    delegate: 'L3',
    scopes: ['waive:nit', 'waive:warning', 'round:extra'],
    deny: [],
    caps: { review: 5, coding: 20.5 },
    stop_at: 1,
  });
  assert.equal(validateGrantInput(BASE, T0).stop_at, 0.9);
});

test('startGrant stores the grant and writes one signed autopilot.grant row; a second active grant is refused', async () => {
  const { rows, writeRow } = await newRun('g-start');
  const grant = await startGrant({ runId: 'g-start', input: { ...BASE, allow: 'waive:nit', deny: 'round:extra', budget: 'review=5', stopAt: '0.8' }, now: T0, writeRow, grantId: 'ap-0001' });
  const expected = {
    grant_id: 'ap-0001',
    status: 'active',
    until: UNTIL_UTC,
    delegate: 'L2',
    scopes: ['waive:nit'],
    deny: ['round:extra'],
    caps: { review: 5 },
    stop_at: 0.8,
    started_at: T0.toISOString(),
    stopped_at: null,
    expired_at: null,
    link: null,
  };
  assert.deepEqual(grant, expected);
  assert.deepEqual((await readRun('g-start')).autopilot, expected);
  assert.equal(rows.length, 1);
  const { mac, ...fields } = rows[0];
  assert.deepEqual(fields, { run: 'g-start', event: 'autopilot.grant', grant_id: 'ap-0001', scopes: ['waive:nit'], deny: ['round:extra'], delegate: 'L2', until: UNTIL_UTC, caps: { review: 5 }, stop_at: 0.8, ts: T0.toISOString() });
  assert.deepEqual(verifyRow(rows[0], await loadKey('g-start')), { ok: true });

  await assert.rejects(startGrant({ runId: 'g-start', input: BASE, now: at(30), writeRow }), {
    code: 'grant-active',
    message: `run g-start already has an active autopilot grant until ${UNTIL_UTC}; stop it first with code-forge autopilot stop --run g-start`,
  });
  assert.equal(rows.length, 1);
});

test('grantFor matrix on a fake clock: no-grant, ok, not-allowed, denied (--deny and all 9 fixed), expired, stopped', async () => {
  const none = await newRun('g-none');
  assert.deepEqual(await grantFor('g-none', 'waive:nit', T0, { writeRow: none.writeRow }), { ok: false, reason: 'no-grant', grant: null });
  await assert.rejects(grantFor('g-none', 'waive:everything', T0), { code: 'unknown-scope' });

  const { rows, writeRow } = await newRun('g-matrix');
  await startGrant({ runId: 'g-matrix', input: { ...BASE, allow: 'waive:nit,waive:warning', deny: 'round:extra' }, now: T0, writeRow });
  const reason = async (/** @type {string} */ scope, /** @type {Date} */ now) => {
    const r = await grantFor('g-matrix', scope, now, { writeRow });
    return r.ok ? 'ok' : r.reason;
  };
  assert.equal(await reason('waive:nit', at(1)), 'ok');
  assert.equal(await reason('waive:warning', at(119)), 'ok');
  assert.equal(await reason('model:choose', at(1)), 'not-allowed');
  assert.equal(await reason('round:extra', at(1)), 'denied');
  const fixed = [];
  for (const scope of FIXED_DENY_SCOPES) fixed.push(await reason(scope, at(1)));
  assert.deepEqual(fixed, Array(9).fill('denied'));
  assert.equal(rows.length, 1); // grant row only: no check wrote anything
  assert.equal(await reason('waive:nit', at(120)), 'expired'); // until itself is outside the window

  const s = await newRun('g-stopped');
  await startGrant({ runId: 'g-stopped', input: BASE, now: T0, writeRow: s.writeRow });
  await stopGrant({ runId: 'g-stopped', now: at(5), writeRow: s.writeRow });
  assert.deepEqual(
    await Promise.all(['waive:nit', 'waive:critical'].map(async (scope) => (await grantFor('g-stopped', scope, at(6), { writeRow: s.writeRow })).reason)),
    ['stopped', 'stopped'],
  );
});

test('lazy expiry: the first check past until writes ONE signed autopilot.expire row; later checks write none', async () => {
  const { rows, writeRow } = await newRun('g-exp');
  await startGrant({ runId: 'g-exp', input: BASE, now: T0, writeRow, grantId: 'ap-exp' });
  assert.deepEqual((await checkExpiry('g-exp', at(119), { writeRow })).state, 'active');
  assert.equal(rows.length, 1);

  const first = await checkExpiry('g-exp', at(121), { writeRow });
  assert.equal(first.state, 'expired');
  assert.equal(first.expiredNow, true);
  await grantFor('g-exp', 'waive:nit', at(122), { writeRow });
  await grantFor('g-exp', 'waive:nit', at(200), { writeRow });
  const again = await checkExpiry('g-exp', at(300), { writeRow });
  assert.equal(again.expiredNow, false);

  const expires = rows.filter((r) => r.event === 'autopilot.expire');
  assert.equal(expires.length, 1);
  // ts is the time of the check that found the expiry (at(121)), not `until` (carried as its own field)
  const { mac, ...fields } = expires[0];
  assert.deepEqual(fields, { run: 'g-exp', event: 'autopilot.expire', grant_id: 'ap-exp', until: UNTIL_UTC, ts: at(121).toISOString() });
  assert.deepEqual(verifyRow(expires[0], await loadKey('g-exp')), { ok: true });
  const stored = (await readRun('g-exp')).autopilot;
  assert.equal(stored.status, 'expired');
  assert.equal(stored.expired_at, at(121).toISOString());

  // two checks racing past until still write one row (the second re-reads under the lock)
  const race = await newRun('g-race');
  await startGrant({ runId: 'g-race', input: BASE, now: T0, writeRow: race.writeRow });
  await Promise.all([checkExpiry('g-race', at(130), { writeRow: race.writeRow }), checkExpiry('g-race', at(130), { writeRow: race.writeRow })]);
  assert.equal(race.rows.filter((r) => r.event === 'autopilot.expire').length, 1);

  // after expiry a new grant may start (no second expire row)
  const next = await startGrant({ runId: 'g-exp', input: { ...BASE, until: '2026-10-07T03:00:00Z' }, now: at(301), writeRow });
  assert.equal(next.status, 'active');
  assert.deepEqual(rows.map((r) => r.event), ['autopilot.grant', 'autopilot.expire', 'autopilot.grant']);
});

test('stopGrant: one signed autopilot.stop row; stopping twice or after expiry is refused', async () => {
  const { rows, writeRow } = await newRun('g-stop');
  await startGrant({ runId: 'g-stop', input: BASE, now: T0, writeRow, grantId: 'ap-stop' });
  const stopped = await stopGrant({ runId: 'g-stop', now: at(10), writeRow });
  assert.equal(stopped.status, 'stopped');
  assert.equal(stopped.stopped_at, at(10).toISOString());
  const { mac, ...fields } = rows[1];
  assert.deepEqual(fields, { run: 'g-stop', event: 'autopilot.stop', grant_id: 'ap-stop', ts: at(10).toISOString() });
  assert.deepEqual(verifyRow(rows[1], await loadKey('g-stop')), { ok: true });
  await assert.rejects(stopGrant({ runId: 'g-stop', now: at(11), writeRow }), { code: 'grant-stopped', message: `run g-stop's autopilot grant was already stopped at ${at(10).toISOString()}` });
  // a stopped grant never expires (no expire row) and a new one may start
  await checkExpiry('g-stop', at(500), { writeRow });
  assert.deepEqual(rows.map((r) => r.event), ['autopilot.grant', 'autopilot.stop']);

  const e = await newRun('g-stop-exp');
  await startGrant({ runId: 'g-stop-exp', input: BASE, now: T0, writeRow: e.writeRow });
  await assert.rejects(stopGrant({ runId: 'g-stop-exp', now: at(121), writeRow: e.writeRow }), { code: 'grant-expired', message: `run g-stop-exp's autopilot grant already expired at ${UNTIL_UTC}` });
  assert.deepEqual(e.rows.map((r) => r.event), ['autopilot.grant', 'autopilot.expire']);
  const n = await newRun('g-stop-none');
  await assert.rejects(stopGrant({ runId: 'g-stop-none', now: T0, writeRow: n.writeRow }), { code: 'no-grant', message: 'run g-stop-none has no autopilot grant' });
});

test('stopGrant writes the stop row FIRST: a failing writer leaves the grant active, and a retry stops it', async () => {
  const { rows, writeRow } = await newRun('g-stop-fail');
  await startGrant({ runId: 'g-stop-fail', input: BASE, now: T0, writeRow, grantId: 'ap-fail' });
  const failing = async () => {
    throw new Error('ledger disk full');
  };
  await assert.rejects(stopGrant({ runId: 'g-stop-fail', now: at(5), writeRow: failing }), { message: 'ledger disk full' });
  const still = (await readRun('g-stop-fail')).autopilot;
  assert.deepEqual([still.status, still.stopped_at], ['active', null]);
  assert.equal((await grantFor('g-stop-fail', 'waive:nit', at(6), { writeRow })).ok, true);
  const stopped = await stopGrant({ runId: 'g-stop-fail', now: at(7), writeRow });
  assert.deepEqual([stopped.status, stopped.stopped_at], ['stopped', at(7).toISOString()]);
  assert.deepEqual(rows.map((r) => [r.event, r.ts]), [['autopilot.grant', T0.toISOString()], ['autopilot.stop', at(7).toISOString()]]);
});

test('a grant on an ended run: grantFor answers run-ended, checkExpiry writes no expire row, stop is refused', async () => {
  const { rows, writeRow } = await newRun('g-ended');
  await startGrant({ runId: 'g-ended', input: BASE, now: T0, writeRow });
  await endRun({ runId: 'g-ended', writeRow, probe: async () => null });
  rows.length = 0;
  assert.deepEqual(
    await Promise.all(['waive:nit', 'model:choose', 'pr:merge'].map(async (scope) => (await grantFor('g-ended', scope, at(1), { writeRow })).reason)),
    ['run-ended', 'run-ended', 'run-ended'],
  );
  assert.equal((await checkExpiry('g-ended', at(200), { writeRow })).state, 'run-ended');
  await assert.rejects(stopGrant({ runId: 'g-ended', now: at(2), writeRow }), { code: 'run-ended', message: 'run g-ended has ended' });
  assert.deepEqual(rows, []);
  assert.equal((await readRun('g-ended')).autopilot.status, 'active');
});

test('two concurrent starts (both past their prompts): the lock re-check lets exactly one grant through', async () => {
  const { rows, writeRow } = await newRun('g-race-start');
  const results = await Promise.allSettled([
    startGrant({ runId: 'g-race-start', input: BASE, now: T0, writeRow, grantId: 'ap-a' }),
    startGrant({ runId: 'g-race-start', input: { ...BASE, delegate: 'L3' }, now: T0, writeRow, grantId: 'ap-b' }),
  ]);
  assert.deepEqual(results.map((r) => r.status).sort(), ['fulfilled', 'rejected']);
  const won = /** @type {PromiseFulfilledResult<any>} */ (results.find((r) => r.status === 'fulfilled')).value;
  const lost = /** @type {PromiseRejectedResult} */ (results.find((r) => r.status === 'rejected')).reason;
  assert.equal(lost.code, 'grant-active');
  assert.equal(rows.filter((r) => r.event === 'autopilot.grant').length, 1);
  assert.equal(rows[0].grant_id, won.grant_id);
  assert.equal((await readRun('g-race-start')).autopilot.grant_id, won.grant_id);
});

test('the checks hook (B48 budget stop) runs only on an allowed scope and its reason is passed through', async () => {
  const { writeRow } = await newRun('g-hook');
  await startGrant({ runId: 'g-hook', input: BASE, now: T0, writeRow });
  /** @type {string[]} */
  const seen = [];
  const checks = [(/** @type {any} */ grant, /** @type {string} */ scope) => (seen.push(scope), scope === 'round:extra' ? 'budget-stop' : null)];
  assert.deepEqual(
    await Promise.all(['waive:nit', 'round:extra', 'model:choose', 'pr:merge'].map(async (scope) => {
      const r = await grantFor('g-hook', scope, at(1), { writeRow, checks });
      return r.ok ? 'ok' : r.reason;
    })),
    ['ok', 'budget-stop', 'not-allowed', 'denied'],
  );
  assert.deepEqual(seen.sort(), ['round:extra', 'waive:nit']);
});
