import { cfgWith, fakeDeps, freshDir, readRecords, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

/**
 * B33: the session row carries `usd` from its tokens, and `spawnSession` is the single budget choke
 * point — every role, every ladder step — with the fake CLIs only.
 */

const { spawnSession } = await import('../../src/session/spawn.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');
const { S2_SCHEMA } = await import('../../src/session/s2.mjs');
const { validateReview } = await import('../../src/review/validate-review.mjs');

const CLAUDE = { provider: 'anthropic', model: 'fake-opus', effort: 'high' };
const MESSAGE = 'budget.usd 20.00 reached (spent 20.13); raise budget.usd or end the run';

/**
 * A ledger double: reads return the seed rows PLUS every row written since (reads see writes, like
 * the real ledger); `written` is only what this test's sessions wrote.
 * @param {Array<Record<string, any>>} seed
 */
function ledgerDouble(seed) {
  /** @type {Array<Record<string, any>>} */
  const written = [];
  return { written, readRows: async () => [...seed, ...written], writeRow: async (/** @type {Record<string, any>} */ row) => void written.push(row) };
}

test('a session row carries usd from its reported tokens: 400-byte packet ⇒ 100 in + 42 out on anthropic L2 = 0.0021, cost_source estimated', async () => {
  const { deps } = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.md', 'x'.repeat(400));
  const result = await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, slug: 'b33-usd', run: 'r-usd', block: 'B1' }, deps);
  assert.equal(result.status, 'ok');
  const rows = await readAllRows('b33-usd');
  assert.equal(rows.length, 1);
  assert.deepEqual(
    [rows[0].event, rows[0].tokens_in, rows[0].tokens_out, rows[0].usd, rows[0].cost_source, Object.hasOwn(rows[0], 'usd_unknown')],
    ['session', 100, 42, 0.0021, 'estimated', false],
  );
});

for (const role of /** @type {const} */ (['coder', 'reviewer', 'judge', 's2', 'author', 'facts'])) {
  test(`budget reached: a ${role} session is refused with 0 spawns, the plain message and one budget.refused row`, async () => {
    const { deps, records, stderr } = fakeDeps();
    const ledger = ledgerDouble([
      { event: 'session', run: 'r-full', usd: 20 },
      { event: 'session', run: 'r-full', role: 'coder', manual: true, usd: 0.13 },
    ]);
    const prompt = writeIn(freshDir('pk'), 'prompt.md', 'do it');
    const cfg = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
    const result = await spawnSession(
      { cfg, level: 'L1', role, promptPath: prompt, run: 'r-full', block: 'B9', ...(role === 'coder' ? { cwd: freshDir('ws') } : {}) },
      { ...deps, readRows: ledger.readRows, writeRow: ledger.writeRow },
    );
    assert.deepEqual([result.status, result.reason, result.message], ['unavailable', 'budget', MESSAGE]);
    assert.equal(readRecords(records).length, 0, 'nothing was spawned');
    assert.deepEqual(ledger.written.map((r) => [r.event, r.role, r.spent_usd, r.budget_usd]), [['budget.refused', role, 20.13, 20]]);
    assert.equal(stderr.text(), `code-forge: ${MESSAGE}\n`);
    // the review's stub guard reports it as unavailable: budget
    assert.deepEqual(
      [validateReview(result, { hunkHeaders: [] }).ok, validateReview(result, { hunkHeaders: [] }).reason],
      [false, 'budget'],
    );
  });
}

test('at 80 %: two sessions both run, ONE budget.warning row and ONE warning line', async () => {
  const { deps, records, stderr } = fakeDeps();
  const ledger = ledgerDouble([{ event: 'session', run: 'r-80', usd: 16.5 }]);
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
  for (let i = 0; i < 2; i += 1) {
    const result = await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, run: 'r-80' }, { ...deps, readRows: ledger.readRows, writeRow: ledger.writeRow });
    assert.equal(result.status, 'ok');
  }
  assert.equal(readRecords(records).length, 2);
  assert.deepEqual(ledger.written.map((r) => r.event), ['budget.warning', 'session', 'session']);
  assert.equal(stderr.text().split('code-forge: budget.usd: spent 16.50 of 20.00').length - 1, 1);
});

test('no budget.usd ⇒ no check: the same spent ledger spawns and writes only the session row', async () => {
  const { deps, records } = fakeDeps();
  const ledger = ledgerDouble([{ event: 'session', run: 'r-free', usd: 500 }]);
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const result = await spawnSession({ cfg: cfgWith(CLAUDE), level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, run: 'r-free' }, { ...deps, readRows: ledger.readRows, writeRow: ledger.writeRow });
  assert.equal(result.status, 'ok');
  assert.equal(readRecords(records).length, 1);
  assert.deepEqual(ledger.written.map((r) => r.event), ['session']);
});

test('B30 timeout retry: the budget is checked again before the automatic retry — attempt 2 is refused, 1 spawn total', async () => {
  const { spawnWithTimeoutRetry } = await import('../../src/review/session-retry.mjs');
  const { deps, records } = fakeDeps({ FAKE_SLEEP_MS: '3000' });
  let reads = 0;
  // first check: 0 spent; second check (before the retry): 25 spent, over the 20 budget
  const readRows = async () => (reads++ === 0 ? [] : [{ event: 'session', run: 'r-retry', usd: 25 }]);
  /** @type {Array<Record<string, any>>} */
  const written = [];
  const writeRow = async (/** @type {Record<string, any>} */ row) => void written.push(row);
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
  const opts = { cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, run: 'r-retry', timeoutMs: 500 };
  const { res, attempts } = await spawnWithTimeoutRetry((o) => spawnSession(/** @type {any} */ (o), { ...deps, readRows, writeRow }), opts, async () => {});
  assert.deepEqual([attempts, res?.status, res?.reason], [2, 'unavailable', 'budget']);
  assert.equal(readRecords(records).length, 1, 'only the first (timed-out) attempt spawned');
  assert.deepEqual(written.map((r) => [r.event, r.status ?? null]), [['session', 'timeout'], ['budget.refused', null]]);
});

const UNREADABLE = "budget.usd is set but the run's spend cannot be read; not starting a session";

test('fail closed: budget.usd + run but no readRows and no slug ⇒ refused, 0 spawns, the plain message', async () => {
  const { deps, records, stderr } = fakeDeps();
  /** @type {Array<Record<string, any>>} */
  const written = [];
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
  const result = await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, run: 'r-blind' }, { ...deps, writeRow: async (row) => void written.push(row) });
  assert.deepEqual([result.status, result.reason, result.message], ['unavailable', 'budget', UNREADABLE]);
  assert.equal(readRecords(records).length, 0);
  assert.equal(stderr.text(), `code-forge: ${UNREADABLE}\n`);
  assert.deepEqual(written.map((r) => [r.event, r.reason]), [['budget.refused', 'spend-unreadable']]);
});

test('fail closed: a ledger read that throws ⇒ refused, 0 spawns, no writer needed', async () => {
  const { deps, records, stderr } = fakeDeps();
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
  const readRows = async () => {
    throw new Error('EACCES ledger');
  };
  const result = await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, run: 'r-throw' }, { ...deps, readRows });
  assert.deepEqual([result.status, result.reason, result.message], ['unavailable', 'budget', UNREADABLE]);
  assert.equal(readRecords(records).length, 0);
  assert.equal(stderr.text(), `code-forge: ${UNREADABLE}\n`);
});

test('per run: a second run spending 500 does not touch this run; only the 5 of this run counts (no warning, the session runs)', async () => {
  const { deps, records } = fakeDeps();
  const ledger = ledgerDouble([
    { event: 'session', run: 'r-other', usd: 500 },
    { event: 'budget.refused', run: 'r-other' },
    { event: 'session', run: 'r-mine', usd: 5 },
  ]);
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
  const result = await spawnSession({ cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, run: 'r-mine' }, { ...deps, readRows: ledger.readRows, writeRow: ledger.writeRow });
  assert.equal(result.status, 'ok');
  assert.equal(readRecords(records).length, 1);
  assert.deepEqual(ledger.written.map((r) => r.event), ['session']);
});

test('rate-limit retry inside one ladder step backs off 2000 ms, then passes the budget gate again: attempt 2 is refused, 1 exec call', async () => {
  const { deps } = fakeDeps();
  let execCalls = 0;
  const rateLimited = async () => {
    execCalls += 1;
    return { result: 'failed', code: 1, signal: null, timedOut: false, stderr: '', stdout: `${JSON.stringify({ type: 'result', is_error: true, api_error_status: 429, result: 'API Error: 429' })}\n` };
  };
  /** @type {Array<Record<string, any>>} */
  const written = [];
  let reads = 0;
  // first gate: 0 spent; second gate (the same step's rate-limit retry): 25 spent
  const readRows = async () => (reads++ === 0 ? [] : [{ event: 'session', run: 'r-429', usd: 25 }]);
  const packet = writeIn(freshDir('pk'), 'packet.md', 'decide');
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
  /** @type {number[]} */
  const waits = [];
  const result = await spawnSession(
    { cfg, level: 'L2', role: 'reviewer', promptPath: packet, schema: S2_SCHEMA, run: 'r-429' },
    { ...deps, exec: /** @type {any} */ (rateLimited), readRows, writeRow: async (row) => void written.push(row), sleep: async (ms) => void waits.push(ms), random: () => 0.5 },
  );
  assert.deepEqual([result.status, result.reason, execCalls, reads, waits], ['unavailable', 'budget', 1, 2, [2000]]);
  assert.deepEqual(written.map((r) => [r.event, r.reason ?? null]), [['session', 'rate-limited'], ['budget.refused', null]]);
});
