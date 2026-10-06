// B48 autopilot limits: the budget stop per category (exact message, one signed pause row per
// category, grantFor 'paused', reservations count, no grant ⇒ nothing changes), the owner's
// expiring approvals (applied through the run reload path + one signed row; no terminal / --yes /
// fixed key refused; lazy restore at expiry from the autopilot and block commands; changed by hand
// ⇒ restore_skipped) and `run reload` refusing a limit key while a grant is active. Sessions run
// against the fake CLIs; the clock is fake except where `run reload` itself reads it.
// `../session/helpers.mjs` is imported FIRST: it points HOME and the cwd at one per-file temp
// parent, removed in `after()`.
import { cfgWith, fakeDeps, freshDir, readRecords, writeIn } from '../session/helpers.mjs';
import assert from 'node:assert/strict';
import { chmodSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { parse as parseYAML } from 'yaml';

const { grantFor, startGrant } = await import('../../src/autopilot/grant.mjs');
const { categoryReservationKey, checkApprovals, limitGuard, FORBIDDEN_SEGMENT_MESSAGE, pausedMessage, RECORD_UNKNOWN_MESSAGE, reloadFailureText, reloadWorkspace, withChange } = await import('../../src/autopilot/limits.mjs');
const { APPROVE_NO_TTY, APPROVE_NO_YES, runAutopilot } = await import('../../src/cli/autopilot.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runRun } = await import('../../src/cli/run.mjs');
const { budgetLockCalls, estimateSessionUsd, reservedUsd, reserveBudget, resetBudgetReservations } = await import('../../src/ledger/spend.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { estimateTokens, spawnSession } = await import('../../src/session/spawn.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { runRecordPath } = await import('../../src/state/paths.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { createWorker, errorKind, EXPIRY_CHECK_FAILED } = await import('../../src/worker/loop.mjs');
const { StateError } = await import('../../src/state/paths.mjs');
const { enqueue } = await import('../../src/worker/queue.mjs');
const { configHash } = await import('../../src/state/config-snapshot.mjs');
const { readRun, saveRun, startRun } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { buildTwoBlocksRepo, CONFIG_YAML } = await import('../fixtures/repos/two-blocks/build.mjs');

const T0 = new Date('2026-10-06T20:00:00.000Z');
/** @param {number} minutes */
const at = (minutes) => new Date(T0.getTime() + minutes * 60000);
const UNTIL = '2026-10-06T22:00:00Z'; // T0 + 2 h
const CLAUDE = { provider: 'anthropic', model: 'fake-opus', effort: 'high' };
const SCHEMA = { type: 'object', properties: { ok: { type: 'boolean' } } };

/** Signed-row check. @param {Record<string, any>} row @param {string} runId */
const signed = async (row, runId) => assert.deepEqual(verifyRow(row, await loadKey(runId)), { ok: true });

/** @param {string} slug @param {string} event */
const rowsOf = async (slug, event) => (await readAllRows(slug)).filter((r) => r.event === event);

/**
 * A run (ledger slug = its id) with a grant started at T0 capping review at 1 USD and coding at 5,
 * stopping at 0.8, plus seed spend rows in the real ledger.
 * @param {string} runId @param {Array<Record<string, any>>} seed
 */
async function cappedRun(runId, seed) {
  const workspace = freshDir(`ws-${runId}`);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: runId });
  await startRun({ workspace, project: runId, runId, writeRow, now: T0 });
  await startGrant({ runId, input: { until: UNTIL, delegate: 'L2', allow: 'waive:nit,round:extra', budget: 'review=1,coding=5', stopAt: '0.8' }, now: T0, writeRow, grantId: `ap-${runId}` });
  for (const row of seed) await appendRow({ run: runId, ...row }, { slug: runId });
}

/**
 * One session of `role` through the real spawner (fake CLIs), clock at T0 + 10 min.
 * @param {string} runId @param {string} role @param {ReturnType<typeof fakeDeps>} fake
 */
function session(runId, role, fake) {
  const prompt = writeIn(freshDir('pk'), 'prompt.md', 'review this');
  return spawnSession(
    { cfg: cfgWith(CLAUDE), level: 'L1', role: /** @type {any} */ (role), promptPath: prompt, run: runId, slug: runId, block: 'B1', ...(role === 'coder' ? { cwd: freshDir('cwd') } : { schema: SCHEMA }) },
    { ...fake.deps, now: () => at(10) },
  );
}

test('category stop: review spend 0.80 of 1.00 at stop 0.8 ⇒ reviewer and s2 refused with the exact message, ONE signed pause row, grantFor paused for review scopes only; coding still runs until its own stop', async () => {
  resetBudgetReservations();
  await cappedRun('e-cap', [
    { event: 'session', role: 'reviewer', usd: 0.5, ts: at(1).toISOString() },
    { event: 'session', role: 'judge', usd: 0.3, ts: at(2).toISOString() },
    { event: 'session', role: 'coder', usd: 3, ts: at(3).toISOString() },
    { event: 'session', role: 'reviewer', usd: 10, ts: at(-60).toISOString() }, // before the grant: not counted
  ]);
  // another run, inside the window: only the run filter leaves it out
  await appendRow({ event: 'session', run: 'other-run', role: 'reviewer', usd: 10, ts: at(1).toISOString() }, { slug: 'e-cap' });
  const message = 'autopilot paused: review spend 0.80 of 1.00 reached the stop at 0.8; waiting for the owner';
  assert.equal(pausedMessage('review', 0.8, 1, 0.8), message);

  const fake = fakeDeps();
  const first = await session('e-cap', 'reviewer', fake);
  assert.deepEqual([first.status, first.reason, first.message], ['unavailable', 'autopilot-paused', message]);
  const second = await session('e-cap', 's2', fake);
  assert.deepEqual([second.status, second.reason, second.message], ['unavailable', 'autopilot-paused', message]);
  assert.equal(readRecords(fake.records).length, 0, 'nothing was spawned');
  assert.equal(fake.stderr.text(), `code-forge: ${message}\ncode-forge: ${message}\n`);

  const pauses = await rowsOf('e-cap', 'autopilot.pause');
  assert.equal(pauses.length, 1);
  const { mac, tokens_source, cost_source, ...fields } = pauses[0];
  assert.deepEqual(fields, { run: 'e-cap', event: 'autopilot.pause', grant_id: 'ap-e-cap', category: 'review', spent_usd: 0.8, reserved_usd: 0, cap_usd: 1, stop_at: 0.8, role: 'reviewer', block: 'B1', ts: at(10).toISOString() });
  await signed(pauses[0], 'e-cap');
  assert.deepEqual(Object.keys((await readRun('e-cap')).autopilot.paused), ['review']);

  assert.deepEqual([(await grantFor('e-cap', 'waive:nit', at(11))).reason, (await grantFor('e-cap', 'round:extra', at(11))).ok], ['paused', true]);

  // coding: 3 of 5 (stop at 4) runs; after one more 1 USD coder row it is refused too
  const coder = await session('e-cap', 'coder', fake);
  assert.notEqual(coder.reason, 'autopilot-paused');
  assert.equal(readRecords(fake.records).length, 1);
  await appendRow({ event: 'session', run: 'e-cap', role: 'coder', usd: 1, ts: at(9).toISOString() }, { slug: 'e-cap' });
  const coderSpent = (await readAllRows('e-cap')).filter((r) => r.event === 'session' && r.run === 'e-cap' && r.role === 'coder' && r.ts >= T0.toISOString()).reduce((s, r) => s + (r.usd ?? 0), 0);
  const refused = await session('e-cap', 'coder', fake);
  const coderUsd = Math.round(coderSpent * 10000) / 10000;
  assert.equal(refused.message, pausedMessage('coding', coderUsd, 5, 0.8));
  assert.ok(coderSpent >= 4);
  const both = await rowsOf('e-cap', 'autopilot.pause');
  assert.deepEqual(both.map((r) => r.category), ['review', 'coding']);
  const { mac: _m2, tokens_source: _t2, cost_source: _c2, ...coding } = both[1];
  assert.deepEqual(coding, { run: 'e-cap', event: 'autopilot.pause', grant_id: 'ap-e-cap', category: 'coding', spent_usd: coderUsd, reserved_usd: 0, cap_usd: 5, stop_at: 0.8, role: 'coder', block: 'B1', ts: at(10).toISOString() });
  await signed(both[1], 'e-cap');
  assert.equal((await grantFor('e-cap', 'round:extra', at(12))).reason, 'paused');

  // status: spend per category against its cap, and the paused categories
  const out = { text: '', write(/** @type {string} */ s) { this.text += s; return true; } };
  assert.equal(await runAutopilot(['status', '--run', 'e-cap'], { stdout: out, stderr: out, now: () => at(13) }), 0);
  assert.match(out.text, /\n {2}spend {5}coding \d+\.\d{2} of 5\.00 USD, review 0\.80 of 1\.00 USD · paused coding, review\n/);
});

test('reservations count: spent 0.50 + 0.30 reserved by a running review session reaches 0.8 × 1.00; the pause row carries reserved_usd 0.3', async () => {
  resetBudgetReservations();
  await cappedRun('e-res', [{ event: 'session', role: 'reviewer', usd: 0.5, ts: at(1).toISOString() }]);
  const release = reserveBudget(categoryReservationKey('e-res', 'review'), 0.3);
  const fake = fakeDeps();
  try {
    const res = await session('e-res', 'reviewer', fake);
    assert.equal(res.message, 'autopilot paused: review spend 0.80 of 1.00 reached the stop at 0.8; waiting for the owner');
  } finally {
    release();
  }
  assert.equal(readRecords(fake.records).length, 0);
  const pauses = await rowsOf('e-res', 'autopilot.pause');
  assert.equal(pauses.length, 1);
  const { mac, tokens_source, cost_source, ...fields } = pauses[0];
  assert.deepEqual(fields, { run: 'e-res', event: 'autopilot.pause', grant_id: 'ap-e-res', category: 'review', spent_usd: 0.5, reserved_usd: 0.3, cap_usd: 1, stop_at: 0.8, role: 'reviewer', block: 'B1', ts: at(10).toISOString() });
  await signed(pauses[0], 'e-res');
});

test('two sessions crossing the stop at once: the first holds its reservation, the second (plan re-read under the lock) is paused — ONE pause row; a third sees the pause', async () => {
  resetBudgetReservations();
  const estimate = estimateSessionUsd({ provider: 'anthropic', level: 'L1', role: 'reviewer', tokensIn: estimateTokens(Buffer.byteLength('review this')) });
  assert.ok(estimate > 0);
  // spent sits just under the stop: the first passes; its reservation carries the second over
  const spent = Math.round((0.8 - estimate / 2) * 10000) / 10000;
  await cappedRun('e-race', [{ event: 'session', role: 'reviewer', usd: spent, ts: at(1).toISOString() }]);
  const fake = fakeDeps({ FAKE_SLEEP_MS: '300' });
  // the same role twice: whichever goes first reserves exactly `estimate`
  const [a, b] = await Promise.all([session('e-race', 'reviewer', fake), session('e-race', 'reviewer', fake)]);
  const reasons = [a.reason ?? null, b.reason ?? null];
  assert.deepEqual(reasons.filter((r) => r === 'autopilot-paused').length, 1);
  const third = await session('e-race', 'reviewer', fake);
  assert.equal(third.reason, 'autopilot-paused');
  const pauses = await rowsOf('e-race', 'autopilot.pause');
  assert.equal(pauses.length, 1);
  assert.equal(pauses[0].reserved_usd, estimate);
  await signed(pauses[0], 'e-race');
});

test('a pause row that cannot be written: the category is paused FIRST (row pending); the next check writes the ONE row', async () => {
  resetBudgetReservations();
  await cappedRun('e-pend', [{ event: 'session', role: 'reviewer', usd: 0.9, ts: at(1).toISOString() }]);
  const fake = fakeDeps();
  const prompt = writeIn(freshDir('pk'), 'prompt.md', 'review this');
  const opts = { cfg: cfgWith(CLAUDE), level: /** @type {const} */ ('L1'), role: /** @type {const} */ ('reviewer'), promptPath: prompt, run: 'e-pend', slug: 'e-pend', block: 'B1', schema: SCHEMA };
  const failing = async () => {
    throw new Error('ledger down');
  };
  const first = await spawnSession(opts, { ...fake.deps, now: () => at(10), writeRow: failing });
  assert.equal(first.reason, 'autopilot-paused');
  assert.equal((await rowsOf('e-pend', 'autopilot.pause')).length, 0);
  assert.equal((await readRun('e-pend')).autopilot.paused.review.row_pending, true);
  assert.equal((await grantFor('e-pend', 'waive:nit', at(10))).reason, 'paused');
  const second = await session('e-pend', 's2', fake);
  assert.equal(second.reason, 'autopilot-paused');
  await session('e-pend', 'reviewer', fake);
  const pauses = await rowsOf('e-pend', 'autopilot.pause');
  assert.equal(pauses.length, 1);
  assert.deepEqual([pauses[0].role, pauses[0].spent_usd, pauses[0].ts], ['reviewer', 0.9, at(10).toISOString()]);
  await signed(pauses[0], 'e-pend');
  assert.equal((await readRun('e-pend')).autopilot.paused.review.row_pending, false);
  assert.equal(readRecords(fake.records).length, 0);
});

test('a run record that cannot be read refuses with autopilot-unknown (not autopilot-paused) and its own message', async () => {
  resetBudgetReservations();
  await cappedRun('e-bad', []);
  writeFileSync(runRecordPath('e-bad'), '{ not json');
  const fake = fakeDeps();
  const res = await session('e-bad', 'reviewer', fake);
  assert.deepEqual([res.status, res.reason, res.message], ['unavailable', 'autopilot-unknown', RECORD_UNKNOWN_MESSAGE]);
  assert.equal(RECORD_UNKNOWN_MESSAGE, 'autopilot: the run record cannot be read, so the autopilot budget is unknown; not starting a session');
  assert.equal(readRecords(fake.records).length, 0);
});

test('no grant: the same spend changes nothing — the session runs, no pause row, no budget lock taken', async () => {
  resetBudgetReservations();
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'e-none' });
  await startRun({ workspace: freshDir('ws-none'), project: 'e-none', runId: 'e-none', writeRow, now: T0 });
  await appendRow({ event: 'session', run: 'e-none', role: 'reviewer', usd: 50, ts: at(1).toISOString() }, { slug: 'e-none' });
  const fake = fakeDeps();
  const res = await session('e-none', 'reviewer', fake);
  assert.notEqual(res.reason, 'autopilot-paused');
  assert.equal(readRecords(fake.records).length, 1);
  assert.equal((await rowsOf('e-none', 'autopilot.pause')).length, 0);
  assert.equal(budgetLockCalls(), 0);
});

/* --------------------------------------------------------------- approvals and reload -- */

const BASE_YAML = `${CONFIG_YAML}review:\n  max_rounds_per_file: 4\n`;

/**
 * A real workspace (git repo + `.code-forge.yml`) and a run started by `run start`; optionally a
 * grant started at `grantAt` for 2 h.
 * @param {string} runId @param {{yaml?: string, grantAt?: Date | null}} [o]
 */
async function workspaceRun(runId, { yaml = BASE_YAML, grantAt = T0 } = {}) {
  const ws = freshDir(`repo-${runId}`);
  await buildTwoBlocksRepo(ws);
  writeFileSync(path.join(ws, '.code-forge.yml'), yaml);
  const io = { write: () => true };
  assert.equal(await runRun(['start', '--cwd', ws, '--run', runId], { stdout: io, stderr: io }), 0);
  if (grantAt !== null) {
    const until = new Date(grantAt.getTime() + 2 * 3600e3).toISOString();
    await startGrant({ runId, input: { until, delegate: 'L2', allow: 'waive:nit' }, now: grantAt, writeRow: (row) => appendRow(row, { slug: 'two-blocks' }), grantId: `ap-${runId}` });
  }
  return { ws, file: path.join(ws, '.code-forge.yml') };
}

/** @param {string[]} args @param {{now?: Date, isTTY?: boolean, answer?: boolean}} [o] */
async function ap(args, { now = at(5), isTTY = true, answer = true } = {}) {
  let out = '';
  let err = '';
  /** @type {string[]} */
  const asked = [];
  const ui = { confirm: async (/** @type {{message: string}} */ q) => (asked.push(q.message), answer), isCancel: () => false };
  const code = await runAutopilot(args, { stdout: { write: (s) => ((out += s), true) }, stderr: { write: (s) => ((err += s), true) }, now: () => now, isTTY, ui });
  return { code, out, err, asked };
}

/** Rows of one run in the shared fixture ledger. @param {string} runId @param {string} event */
const runRows = async (runId, event) => (await readAllRows('two-blocks')).filter((r) => r.run === runId && r.event === event);

const APPROVE = (/** @type {string} */ runId, /** @type {string} */ until = '2026-10-06T21:00:00Z') => ['approve', '--run', runId, '--key', 'review.max_rounds_per_file', '--value', '6', '--until', until];

test('approve (owner, terminal): writes the workspace config, reloads through run reload while the grant is active, one signed autopilot.approve row; expiry restores the old value with ONE signed restore row and a reload', async () => {
  const { file } = await workspaceRun('e-apr');
  const res = await ap(APPROVE('e-apr'));
  assert.equal(res.err, '');
  assert.equal(res.code, 0);
  assert.deepEqual(res.asked, ['Approve review.max_rounds_per_file = 6 for run e-apr until 2026-10-06T21:00:00.000Z? The old value comes back then.']);
  const approval = (await readRun('e-apr')).autopilot_approvals[0];
  assert.equal(res.out, `autopilot approve run e-apr: review.max_rounds_per_file changed until 2026-10-06T21:00:00.000Z (approval ${approval.approval_id}); the old value comes back then\n`);
  assert.equal(parseYAML(readFileSync(file, 'utf8')).review.max_rounds_per_file, 6);
  const approvedHash = configHash(parseYAML(readFileSync(file, 'utf8')));
  assert.equal((await readRun('e-apr')).config.hash, approvedHash);
  assert.deepEqual(
    { key: approval.key, new: approval.new, old: approval.old, old_absent: approval.old_absent, status: approval.status },
    { key: 'review.max_rounds_per_file', new: 6, old: 4, old_absent: false, status: 'active' },
  );
  const reloads = await runRows('e-apr', 'run.reload');
  assert.deepEqual(reloads.map((r) => r.changed_keys), [['review.max_rounds_per_file']]);
  const rows = await runRows('e-apr', 'autopilot.approve');
  assert.equal(rows.length, 1);
  const { mac, tokens_source, cost_source, ...fields } = rows[0];
  assert.deepEqual(fields, { run: 'e-apr', event: 'autopilot.approve', approval_id: approval.approval_id, key: 'review.max_rounds_per_file', old: 4, old_absent: false, new: 6, until: '2026-10-06T21:00:00.000Z', new_hash: approvedHash, ts: at(5).toISOString() });
  await signed(rows[0], 'e-apr');

  // before until: nothing restored
  assert.deepEqual(await checkApprovals('e-apr', at(59)), { restored: [], skipped: [], failed: [] });
  // at until, from an autopilot command: the old value is back, one restore row, the snapshot reloaded
  const status = await ap(['status', '--run', 'e-apr'], { now: at(60) });
  assert.equal(status.code, 0);
  assert.equal(status.err, `autopilot: approval ${approval.approval_id} expired; the old value is back\n`);
  assert.deepEqual(parseYAML(readFileSync(file, 'utf8')), parseYAML(BASE_YAML));
  assert.equal((await readRun('e-apr')).config.hash, configHash(parseYAML(BASE_YAML)));
  assert.equal((await ap(['status', '--run', 'e-apr'], { now: at(61) })).err, '');
  const restores = await runRows('e-apr', 'autopilot.restore');
  assert.equal(restores.length, 1);
  assert.deepEqual([restores[0].approval_id, restores[0].key, restores[0].restored, restores[0].reloaded, restores[0].ts], [approval.approval_id, 'review.max_rounds_per_file', 4, true, at(60).toISOString()]);
  await signed(restores[0], 'e-apr');
  assert.equal((await readRun('e-apr')).autopilot_approvals[0].status, 'restored');
  assert.deepEqual((await runRows('e-apr', 'run.reload')).map((r) => r.changed_keys), [['review.max_rounds_per_file'], ['review.max_rounds_per_file']]);
});

test('a block command restores an expired approval: exit 0, ONE signed restore row, the snapshot reloaded', async () => {
  const { file } = await workspaceRun('e-blk');
  await openBlock({ runId: 'e-blk', id: 'B1', level: 'L1', owned: ['a.txt'], acceptance: [{ clause: 'c1', tests: ['t1'] }], writeRow: async () => {} });
  assert.equal((await ap(APPROVE('e-blk', '2026-10-06T20:30:00Z'))).code, 0);
  const id = (await readRun('e-blk')).autopilot_approvals[0].approval_id;
  let out = '';
  let err = '';
  const stdout = { write: (/** @type {string} */ s) => ((out += s), true) };
  const stderr = { write: (/** @type {string} */ s) => ((err += s), true) };
  assert.equal(await runBlock(['stop', 'B1', '--run', 'e-blk', '--reason', 'none'], { stdout, stderr, now: () => at(31) }), 0);
  assert.equal(err, `autopilot: approval ${id} expired; the old value is back\n`);
  assert.equal(out, 'block B1 stopped\n');
  assert.equal(parseYAML(readFileSync(file, 'utf8')).review.max_rounds_per_file, 4);
  assert.equal((await readRun('e-blk')).config.hash, configHash(parseYAML(BASE_YAML)));
  const restores = await runRows('e-blk', 'autopilot.restore');
  assert.deepEqual(restores.map((r) => [r.approval_id, r.restored, r.reloaded, r.new_hash]), [[id, 4, true, configHash(parseYAML(BASE_YAML))]]);
  await signed(restores[0], 'e-blk');
});

test('a restore whose signed row cannot be written reports restore-incomplete and stays active; the next check writes the ONE row (no restore_skipped)', async () => {
  const { file } = await workspaceRun('e-inc');
  assert.equal((await ap(APPROVE('e-inc', '2026-10-06T20:30:00Z'))).code, 0);
  const id = (await readRun('e-inc')).autopilot_approvals[0].approval_id;
  const failing = async () => {
    throw new Error('ledger down');
  };
  // the reload's own run.reload row fails too: reloadRun keeps the reload (rowError)
  assert.deepEqual(await checkApprovals('e-inc', at(31), { writeRow: failing }), { restored: [], skipped: [], failed: [{ approval_id: id, reason: 'restore-incomplete' }] });
  assert.equal((await readRun('e-inc')).autopilot_approvals[0].status, 'active');
  assert.equal(parseYAML(readFileSync(file, 'utf8')).review.max_rounds_per_file, 4);
  assert.deepEqual(await checkApprovals('e-inc', at(32)), { restored: [id], skipped: [], failed: [] });
  assert.equal((await runRows('e-inc', 'autopilot.restore')).length, 1);
  assert.equal((await runRows('e-inc', 'autopilot.restore_skipped')).length, 0);
  assert.equal((await readRun('e-inc')).autopilot_approvals[0].status, 'restored');
});

test('changed by hand since the approval: not overwritten — ONE signed autopilot.restore_skipped row, no reload', async () => {
  const { file } = await workspaceRun('e-hand');
  assert.equal((await ap(APPROVE('e-hand', '2026-10-06T20:30:00Z'))).code, 0);
  writeFileSync(file, readFileSync(file, 'utf8').replace('max_rounds_per_file: 6', 'max_rounds_per_file: 5'));
  const hashBefore = (await readRun('e-hand')).config.hash;
  const first = await checkApprovals('e-hand', at(31));
  const id = (await readRun('e-hand')).autopilot_approvals[0].approval_id;
  assert.deepEqual(first, { restored: [], skipped: [id], failed: [] });
  assert.deepEqual(await checkApprovals('e-hand', at(32)), { restored: [], skipped: [], failed: [] });
  assert.equal(parseYAML(readFileSync(file, 'utf8')).review.max_rounds_per_file, 5);
  assert.equal((await readRun('e-hand')).config.hash, hashBefore);
  const skipped = await runRows('e-hand', 'autopilot.restore_skipped');
  assert.deepEqual(skipped.map((r) => [r.approval_id, r.key, r.reason]), [[id, 'review.max_rounds_per_file', 'changed-by-hand']]);
  await signed(skipped[0], 'e-hand');
  assert.equal((await runRows('e-hand', 'autopilot.restore')).length, 0);
  assert.equal((await readRun('e-hand')).autopilot_approvals[0].status, 'restore_skipped');
});

test('approve refusals: no terminal (exit 2), --yes (exit 2), a fixed key or its parent (exit 1) — nothing written', async () => {
  const { file } = await workspaceRun('e-ref');
  const before = readFileSync(file, 'utf8');
  assert.deepEqual(await ap(APPROVE('e-ref'), { isTTY: false }), { code: 2, out: '', err: `${APPROVE_NO_TTY}\n`, asked: [] });
  assert.deepEqual(await ap([...APPROVE('e-ref'), '--yes']), { code: 2, out: '', err: `${APPROVE_NO_YES}\n`, asked: [] });
  assert.deepEqual(await ap(['approve', '--run', 'e-ref', '--key', 'engine', '--value', '"cli"', '--until', '2026-10-06T21:00:00Z']), {
    code: 1,
    out: '',
    err: 'autopilot approve: engine cannot be approved: engine cannot change mid-run (the run engine is fixed at run start)\n',
    asked: [],
  });
  assert.deepEqual(await ap(['approve', '--run', 'e-ref', '--key', 'system1', '--value', '{}', '--until', '2026-10-06T21:00:00Z']), {
    code: 1,
    out: '',
    err: 'autopilot approve: system1 cannot be approved: system1.key cannot change mid-run (the worker resolves the Jev key once, at start)\n',
    asked: [],
  });
  // prototype segments: refused before anything is read or asked; Object.prototype untouched
  for (const key of ['__proto__.polluted', 'review.constructor', 'levels.prototype.x']) {
    assert.deepEqual(await ap(['approve', '--run', 'e-ref', '--key', key, '--value', 'true', '--until', '2026-10-06T21:00:00Z']), { code: 2, out: '', err: `autopilot approve: ${FORBIDDEN_SEGMENT_MESSAGE}\n`, asked: [] }, key);
  }
  assert.equal(/** @type {any} */ ({}).polluted, undefined);
  assert.equal(Object.hasOwn(Object.prototype, 'polluted'), false);
  assert.throws(() => withChange({}, ['__proto__', 'polluted'], { set: true }), { code: 'usage', message: FORBIDDEN_SEGMENT_MESSAGE });
  assert.throws(() => withChange({ a: {} }, ['a', 'constructor', 'x'], { set: true }), { code: 'usage' });
  assert.equal(/** @type {any} */ ({}).polluted, undefined);
  // withChange descends only into OWN plain-object properties: an inherited one is replaced
  const base = Object.create({ inherited: { x: 1 } });
  assert.deepEqual(withChange(base, ['inherited', 'y'], { set: 2 }), { inherited: { y: 2 } });
  assert.equal(readFileSync(file, 'utf8'), before);
  assert.equal((await readRun('e-ref')).autopilot_approvals, undefined);
  assert.equal((await runRows('e-ref', 'autopilot.approve')).length, 0);
  assert.equal((await runRows('e-ref', 'run.reload')).length, 0);
});

/** @param {string} runId */
async function reload(runId) {
  let out = '';
  let err = '';
  const code = await runRun(['reload', '--run', runId], { stdout: { write: (s) => ((out += s), true) }, stderr: { write: (s) => ((err += s), true) } });
  return { code, out, err };
}

test('reloadWorkspace: a config that cannot be read is read-error with the errno code only (no OS text, no path)', async () => {
  const { file } = await workspaceRun('e-rd', { grantAt: null });
  rmSync(file);
  mkdirSync(file);
  const res = await reloadWorkspace({ runId: 'e-rd' });
  assert.deepEqual(res, { ok: false, error: 'read-error', code: 'EISDIR' });
  assert.equal(reloadFailureText(res), '.code-forge.yml could not be read (EISDIR)');
  const verb = await reload('e-rd');
  assert.deepEqual(verb, { code: 1, out: '', err: 'run reload: .code-forge.yml could not be read (EISDIR) — nothing changed\n' });
});

test('run reload while a grant is active: a limit key is refused with the exact message (nothing changed); a non-limit key reloads; with no grant the limit key reloads', async () => {
  // `run reload` reads the real clock: the grant starts now
  const { file } = await workspaceRun('e-rl', { grantAt: new Date() });
  const hash = (await readRun('e-rl')).config.hash;
  writeFileSync(file, BASE_YAML.replace('max_rounds_per_file: 4', 'max_rounds_per_file: 6'));
  assert.deepEqual(await reload('e-rl'), { code: 1, out: '', err: 'run reload: autopilot is active: review.max_rounds_per_file is a limit; only the owner can change it with code-forge autopilot approve\n' });
  assert.equal((await readRun('e-rl')).config.hash, hash);
  assert.equal((await runRows('e-rl', 'run.reload')).length, 0);

  writeFileSync(file, BASE_YAML.replace('L2: {model: claude-opus-5-5}', 'L2: {model: claude-fable-5-1}').replace('L3: {model: claude-fable-5-1}', 'L3: {model: claude-opus-5-5}'));
  const allowed = await reload('e-rl');
  assert.equal(allowed.code, 0, allowed.err);
  assert.match(allowed.out, /^run e-rl: config reloaded · 2 keys changed\n {2}levels\.L2\.model\n {2}levels\.L3\.model\n/);

  const free = await workspaceRun('e-rl-free', { grantAt: null });
  writeFileSync(free.file, BASE_YAML.replace('max_rounds_per_file: 4', 'max_rounds_per_file: 6'));
  const res = await reload('e-rl-free');
  assert.equal(res.code, 0, res.err);
  assert.match(res.out, /^run e-rl-free: config reloaded · 1 key changed\n {2}review\.max_rounds_per_file\n/);
});

/* ------------------------------------------------------------------------ worker drain -- */

/** An in-process worker on a real run; the engine hook records the order. @param {string} runId @param {Record<string, any>} deps */
async function drainWorker(runId, deps) {
  const { ws } = await workspaceRun(runId, { grantAt: null });
  /** @type {string[]} */
  const order = [];
  /** @type {any} */
  const review = async () => {
    order.push('review');
    return { status: 'reviewed', approved: true, engine: 'test', sessions: [] };
  };
  const worker = await createWorker(
    { runId, repoRoot: ws, cfg: {}, runRootDir: freshDir('runroot'), slug: 'two-blocks', key: await loadKey(runId), pollMs: 20 },
    { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow: async () => {}, readRows: async () => [], review, ...deps },
  );
  return { ws, order, worker };
}

test('worker drain: the expiry check runs exactly once, before the first ticket', async () => {
  /** @type {string[]} */
  let order = [];
  const w = await drainWorker('e-drain', {
    expiryCheck: async () => {
      order.push('check');
    },
  });
  order = w.order;
  enqueue({ repoRoot: w.ws, run: 'e-drain', block: 'B1', file: 'a.txt' });
  assert.equal(await w.worker.drain(), 1);
  assert.deepEqual(order, ['check', 'review']);
});

test('worker drain: a throwing check is logged once per failure streak (stderr + autopilot_expiry_check_failed warning) and never stops the drain', async () => {
  let calls = 0;
  const stderr = { text: '', write(/** @type {string} */ s) { this.text += s; return true; } };
  const w = await drainWorker('e-dfail', {
    stderr,
    expiryCheck: async () => {
      calls += 1;
      throw new TypeError('boom');
    },
  });
  enqueue({ repoRoot: w.ws, run: 'e-dfail', block: 'B1', file: 'a.txt' });
  assert.equal(await w.worker.drain(), 1);
  assert.equal(await w.worker.drain(), 0);
  assert.equal(calls, 2);
  assert.deepEqual(w.order, ['review']);
  assert.equal(stderr.text, `worker: WARN ${EXPIRY_CHECK_FAILED} (TypeError)\n`);
  assert.equal(stderr.text.includes('boom'), false); // the kind only, never the message
  const log = path.join(process.env.HOME ?? '', '.code-forge', 'logs', 'errors.jsonl');
  const warnings = readFileSync(log, 'utf8').split('\n').filter((l) => l.length > 0).map((l) => JSON.parse(l)).filter((e) => e.warning === 'autopilot_expiry_check_failed');
  assert.equal(warnings.length, 1);
  assert.equal(warnings[0].message, `${EXPIRY_CHECK_FAILED} (TypeError)`);
  const eacces = Object.assign(new Error('EACCES: permission denied, open /secret/path'), { code: 'EACCES' });
  assert.deepEqual([errorKind(eacces), errorKind(new StateError('record-unknown', 'x')), errorKind(new TypeError('x')), errorKind('a string')], ['EACCES', 'StateError:record-unknown', 'TypeError', 'Error']);
});

/* ------------------------------------------------------------------- fix round 2 -- */

test('budget gate, no budget.usd + an active grant: the lock is taken, the category is reserved while the session runs, and the release frees it (the run reservation stays 0)', async () => {
  resetBudgetReservations();
  await cappedRun('e-gate', []);
  const fake = fakeDeps({ FAKE_SLEEP_MS: '400' });
  const key = categoryReservationKey('e-gate', 'review');
  /** @type {number[]} */
  const seen = [];
  const running = session('e-gate', 'reviewer', fake);
  const timer = setInterval(() => seen.push(reservedUsd(key)), 20);
  const res = await running;
  clearInterval(timer);
  assert.equal(res.status, 'ok');
  assert.equal(budgetLockCalls(), 1);
  const estimate = estimateSessionUsd({ provider: 'anthropic', level: 'L1', role: 'reviewer', tokensIn: estimateTokens(Buffer.byteLength('review this')) });
  assert.ok(seen.includes(estimate), `the category held ${estimate} while running (saw ${[...new Set(seen)].join(', ')})`);
  assert.deepEqual([reservedUsd(key), reservedUsd('e-gate')], [0, 0]);
});

test('budget gate: a budget.usd refusal (checkGate) leaves no category reservation and writes no pause row', async () => {
  resetBudgetReservations();
  await cappedRun('e-b33', [{ event: 'session', role: 'coder', usd: 30, ts: at(-90).toISOString() }]); // before the grant: budget only
  const fake = fakeDeps();
  const prompt = writeIn(freshDir('pk'), 'prompt.md', 'review this');
  const res = await spawnSession(
    { cfg: { ...cfgWith(CLAUDE), budget: { usd: 20 } }, level: 'L1', role: 'reviewer', promptPath: prompt, run: 'e-b33', slug: 'e-b33', block: 'B1', schema: SCHEMA },
    { ...fake.deps, now: () => at(10) },
  );
  assert.equal(res.reason, 'budget');
  assert.equal(reservedUsd(categoryReservationKey('e-b33', 'review')), 0);
  assert.equal(reservedUsd('e-b33'), 0);
  assert.equal((await rowsOf('e-b33', 'autopilot.pause')).length, 0);
  assert.equal((await readRun('e-b33')).autopilot.paused, undefined);
});

test('crash after the restore reload and before its row: the next check finishes it — ONE restore row, no restore_skipped, no rewrite', async () => {
  const { file } = await workspaceRun('e-crash');
  assert.equal((await ap(APPROVE('e-crash', '2026-10-06T20:30:00Z'))).code, 0);
  const record = await readRun('e-crash');
  const id = record.autopilot_approvals[0].approval_id;
  // the crashed check: restoring saved, old value written, reload done — then the process died
  record.autopilot_approvals[0].restoring = true;
  await saveRun(record);
  writeFileSync(file, BASE_YAML);
  const reloaded = await reloadWorkspace({ runId: 'e-crash', allow: { segs: ['review', 'max_rounds_per_file'], change: { set: 4 } }, now: at(31) });
  assert.equal(reloaded.ok, true);
  const mtime = statSync(file).mtimeMs;
  assert.deepEqual(await checkApprovals('e-crash', at(32)), { restored: [id], skipped: [], failed: [] });
  assert.equal(statSync(file).mtimeMs, mtime, 'the file already held the old value: not rewritten');
  assert.equal((await runRows('e-crash', 'autopilot.restore')).length, 1);
  assert.equal((await runRows('e-crash', 'autopilot.restore_skipped')).length, 0);
  assert.equal((await readRun('e-crash')).autopilot_approvals[0].status, 'restored');
});

test('the guard compares the migrated view on both sides without the approved key: a value the snapshot fills by migration does not block the restore; another limit change still does', async () => {
  // no `version:` line: migration fills version 1 in the snapshot, the raw file never has it
  const noVersion = CONFIG_YAML.replace('version: 1\n', '');
  const { file } = await workspaceRun('e-mig', { yaml: noVersion });
  assert.equal((await readRun('e-mig')).config.snapshots[(await readRun('e-mig')).config.hash].version, 1);
  // the key is absent in the raw file: approve adds it, the restore removes it (and its new parent)
  assert.equal((await ap(APPROVE('e-mig', '2026-10-06T20:30:00Z'))).code, 0);
  const id = (await readRun('e-mig')).autopilot_approvals[0].approval_id;
  assert.equal((await readRun('e-mig')).autopilot_approvals[0].old_absent, true);
  assert.deepEqual(await checkApprovals('e-mig', at(31)), { restored: [id], skipped: [], failed: [] });
  assert.deepEqual(parseYAML(readFileSync(file, 'utf8')), parseYAML(noVersion));
  assert.equal((await readRun('e-mig')).config.hash, configHash({ ...parseYAML(noVersion), version: 1 }));

  // unit: the snapshot holds a filled value at the key; the file view lacks it ⇒ allowed
  const record = { autopilot: { status: 'active', until: at(120).toISOString() } };
  const allow = { segs: ['review', 'max_rounds_per_file'], change: /** @type {any} */ ({ remove: true, prune: 1 }) };
  const guard = limitGuard(allow, at(10));
  assert.doesNotThrow(() => guard(record, { budget: { usd: 5 }, review: { max_rounds_per_file: 4 } }, { budget: { usd: 5 } }, ['review']));
  assert.throws(() => guard(record, { budget: { usd: 5 }, review: { max_rounds_per_file: 4 } }, { budget: { usd: 9 } }, ['budget.usd', 'review']), {
    message: 'autopilot is active: budget.usd is a limit; only the owner can change it with code-forge autopilot approve',
  });
});

test('writeText keeps the config file mode and leaves no temp file', async () => {
  const { ws, file } = await workspaceRun('e-mode');
  chmodSync(file, 0o600);
  assert.equal((await ap(APPROVE('e-mode'))).code, 0);
  assert.equal(statSync(file).mode & 0o777, 0o600);
  assert.deepEqual(readdirSync(ws).filter((n) => n.endsWith('.tmp')), []);
});
