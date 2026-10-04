import { cfgWith, fakeDeps, freshDir, waitFor, writeIn } from '../session/helpers.mjs';
import assert from 'node:assert/strict';
import { beforeEach, test } from 'node:test';

/**
 * B41: the `budget.usd` check and the reservation of a session's estimated cost run under one
 * in-process lock per run, so sessions that start together cannot all pass a check that only sees
 * the ledger. Exec doubles only: a "sleeping" session waits on a gate the test opens, so every
 * count is deterministic.
 */

const { spawnSession } = await import('../../src/session/spawn.mjs');
const { spawnWithTimeoutRetry } = await import('../../src/review/session-retry.mjs');
const { estimateSessionUsd, reservedUsd, reserveBudget, budgetLockCalls, resetBudgetReservations, TOKENS_OUT_ESTIMATE } = await import('../../src/ledger/spend.mjs');

const CLAUDE = { provider: 'anthropic', model: 'fake-opus' };
const GROK = { provider: 'xai', model: 'fake-grok' };
/** 400-byte packet ⇒ 100 tokens in; reviewer L2 tokens_out 8000; anthropic L2 0.015 / 1K ⇒ 8100 × 0.015 / 1000 */
const ESTIMATE = 0.1215;
const OK = { result: 'ok', code: 0, signal: null, timedOut: false, stderr: '', stdout: `${JSON.stringify({ type: 'result', result: 'fine' })}\n` };

beforeEach(() => resetBudgetReservations());

/** A ledger double whose reads see every write. @param {Array<Record<string, any>>} seed */
function ledgerDouble(seed = []) {
  /** @type {Array<Record<string, any>>} */
  const written = [];
  return { written, readRows: async () => [...seed, ...written], writeRow: async (/** @type {Record<string, any>} */ row) => void written.push(row) };
}

/** A gate the sleeping exec doubles wait on. */
function gate() {
  /** @type {() => void} */
  let open = () => {};
  const opened = new Promise((resolve) => {
    open = () => resolve(undefined);
  });
  return { open, opened };
}

/** A reviewer session on a 400-byte packet. @param {Record<string, any>} cfg @param {string} run */
const reviewer = (cfg, run) => ({ cfg, level: /** @type {const} */ ('L2'), role: /** @type {const} */ ('reviewer'), promptPath: writeIn(freshDir('pk'), 'packet.md', 'x'.repeat(400)), run });

test('the estimate: 400-byte reviewer packet on anthropic L2 = 0.1215; an unpriced provider reserves 0; the table has 6 roles × 4 levels', () => {
  assert.equal(estimateSessionUsd({ provider: 'anthropic', level: 'L2', role: 'reviewer', tokensIn: 100 }), ESTIMATE);
  assert.equal(estimateSessionUsd({ provider: 'anthropic', level: 'L3', role: 'coder', tokensIn: 0 }), 1.92);
  assert.equal(estimateSessionUsd({ provider: 'mistral', level: 'L2', role: 'reviewer', tokensIn: 100 }), 0);
  assert.deepEqual(Object.keys(TOKENS_OUT_ESTIMATE), ['coder', 'author', 'reviewer', 'facts', 'judge', 's2']);
  assert.equal(Object.values(TOKENS_OUT_ESTIMATE).filter((row) => Object.keys(row).join() === 'L0,L1,L2,L3').length, 6);
});

test('5 sessions start together with room for exactly 2: 2 spawn, 3 refused with the would-be-exceeded message and 3 budget.refused rows; reserved 0 after', async () => {
  const { deps, stderr } = fakeDeps();
  const ledger = ledgerDouble();
  const g = gate();
  let execCalls = 0;
  const exec = async () => {
    execCalls += 1;
    await g.opened;
    return OK;
  };
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 0.3 } };
  let settled = 0;
  const runs = Array.from({ length: 5 }, () =>
    spawnSession(reviewer(cfg, 'r-five'), { ...deps, exec: /** @type {any} */ (exec), readRows: ledger.readRows, writeRow: ledger.writeRow }).finally(() => {
      settled += 1;
    }),
  );
  const ready = await waitFor(() => settled === 3 && execCalls === 2);
  const heldWhileRunning = reservedUsd('r-five');
  g.open();
  assert.equal(ready, true);
  // the 2 running sessions hold their estimates: 0.243 committed, under the 0.30 budget (no overshoot)
  assert.equal(heldWhileRunning, 0.243);
  const results = await Promise.all(runs);
  const message = 'budget.usd 0.30 would be exceeded: spent 0.00, reserved by running sessions 0.24, this session about 0.12; it starts when running sessions finish or when budget.usd is raised';
  assert.deepEqual(results.map((r) => r.status).sort(), ['ok', 'ok', 'unavailable', 'unavailable', 'unavailable']);
  assert.deepEqual(results.filter((r) => r.status === 'unavailable').map((r) => [r.reason, r.message]), [['budget', message], ['budget', message], ['budget', message]]);
  assert.equal(execCalls, 2);
  assert.deepEqual(
    ledger.written.map((r) => [r.event, r.reserved_usd ?? null, r.estimate_usd ?? null]),
    [
      ['budget.refused', 0.243, ESTIMATE],
      ['budget.refused', 0.243, ESTIMATE],
      ['budget.refused', 0.243, ESTIMATE],
      ['session', null, null],
      ['session', null, null],
    ],
  );
  assert.equal(stderr.text().split(`code-forge: ${message}\n`).length - 1, 3);
  assert.equal(reservedUsd('r-five'), 0);
});

test('the 80 % warning under concurrency: 5 sessions at 85 % of the budget ⇒ exactly 1 budget.warning row and 1 warning line, 5 spawn', async () => {
  const { deps, stderr } = fakeDeps();
  const ledger = ledgerDouble([{ event: 'session', run: 'r-warn', usd: 8.5 }]);
  const g = gate();
  let execCalls = 0;
  const exec = async () => {
    execCalls += 1;
    await g.opened;
    return OK;
  };
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 10 } };
  const runs = Array.from({ length: 5 }, () => spawnSession(reviewer(cfg, 'r-warn'), { ...deps, exec: /** @type {any} */ (exec), readRows: ledger.readRows, writeRow: ledger.writeRow }));
  // the 5 gates have all run (each checked under the lock); 4 sessions hold the 4 anthropic slots (B40)
  const started = await waitFor(() => execCalls === 4);
  g.open();
  assert.equal(started, true);
  const results = await Promise.all(runs);
  assert.deepEqual(results.map((r) => r.status), ['ok', 'ok', 'ok', 'ok', 'ok']);
  assert.equal(ledger.written.filter((r) => r.event === 'budget.warning').length, 1);
  assert.equal(ledger.written.filter((r) => r.event === 'session').length, 5);
  assert.equal(stderr.text().split('code-forge: budget.usd: spent 8.50 of 10.00').length - 1, 1);
  assert.equal(reservedUsd('r-warn'), 0);
});

/**
 * Deps for one session (budget 20 in `cfg`) with an exec double; `held` records the reservation
 * seen while the child "runs". @param {() => any} execImpl @param {string} run
 */
function onePath(execImpl, run) {
  const { deps } = fakeDeps();
  const ledger = ledgerDouble();
  /** @type {number[]} */
  const held = [];
  const exec = async () => {
    held.push(reservedUsd(run));
    return execImpl();
  };
  const cfg = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
  return { held, ledger, deps: { ...deps, exec: /** @type {any} */ (exec), readRows: ledger.readRows, writeRow: ledger.writeRow }, cfg };
}

test('release on exit path ok: held 0.1215 while running, 0 after', async () => {
  const p = onePath(() => OK, 'r-ok');
  const res = await spawnSession(reviewer(p.cfg, 'r-ok'), p.deps);
  assert.deepEqual([res.status, p.held, reservedUsd('r-ok')], ['ok', [ESTIMATE], 0]);
});

test('release on exit path unavailable (cli-missing): held 0.1215 while running, 0 after', async () => {
  const p = onePath(() => ({ result: 'failed', code: null, signal: null, timedOut: false, error: 'spawn fake ENOENT', stderr: '', stdout: '' }), 'r-gone');
  const res = await spawnSession(reviewer(p.cfg, 'r-gone'), p.deps);
  assert.deepEqual([res.status, res.reason, p.held, reservedUsd('r-gone')], ['unavailable', 'cli-missing', [ESTIMATE], 0]);
});

test('release on exit path throw: the exec double throws, spawnSession rejects, reserved 0 after', async () => {
  const p = onePath(() => {
    throw new Error('exec blew up');
  }, 'r-throw');
  await assert.rejects(spawnSession(reviewer(p.cfg, 'r-throw'), p.deps), /exec blew up/);
  assert.deepEqual([p.held, reservedUsd('r-throw')], [[ESTIMATE], 0]);
});

test('release on exit path budget refusal: nothing is reserved, 0 exec calls, reserved 0', async () => {
  const p = onePath(() => OK, 'r-full');
  const res = await spawnSession(reviewer(p.cfg, 'r-full'), { ...p.deps, readRows: async () => [{ event: 'session', run: 'r-full', usd: 19.95 }] });
  assert.deepEqual([res.status, res.reason, p.held.length, reservedUsd('r-full')], ['unavailable', 'budget', 0, 0]);
  assert.equal(res.message, 'budget.usd 20.00 would be exceeded: spent 19.95, this session about 0.12; raise budget.usd or end the run');
  assert.deepEqual(p.ledger.written.map((r) => r.event), ['budget.refused']);
});

test('release on exit path timeout retry (B30): 2 attempts, each held 0.1215 alone, 0 after, 2 session rows', async () => {
  const p = onePath(() => ({ result: 'failed', code: null, signal: 'SIGKILL', timedOut: true, stderr: '', stdout: '' }), 'r-slow');
  const { res, attempts } = await spawnWithTimeoutRetry((o) => spawnSession(/** @type {any} */ (o), p.deps), { ...reviewer(p.cfg, 'r-slow'), timeoutMs: 500 }, async () => {});
  assert.deepEqual([attempts, res?.status, p.held, reservedUsd('r-slow')], [2, 'timeout', [ESTIMATE, ESTIMATE], 0]);
  assert.deepEqual(p.ledger.written.map((r) => r.event), ['session', 'session']);
});

test('no budget.usd ⇒ the budget lock is never entered and nothing is reserved (with a budget: 1 entry)', async () => {
  const p = onePath(() => OK, 'r-free');
  const res = await spawnSession(reviewer(cfgWith(CLAUDE), 'r-free'), p.deps);
  assert.deepEqual([res.status, budgetLockCalls(), p.held, reservedUsd('r-free')], ['ok', 0, [0], 0]);
  const budgeted = await spawnSession(reviewer(p.cfg, 'r-free'), p.deps);
  assert.deepEqual([budgeted.status, budgetLockCalls()], ['ok', 1]);
});

/**
 * One coder L3 session on an EMPTY brief (estimate exactly 64000 out × 0.03 / 1K = 1.92) against
 * `seed` spend and budget `usd`; returns the result, the rows written and the stderr text.
 * @param {string} run @param {number} usd @param {Array<Record<string, any>>} seed
 */
async function coderL3(run, usd, seed) {
  const { deps, stderr } = fakeDeps();
  const ledger = ledgerDouble(seed);
  let execCalls = 0;
  const exec = async () => {
    execCalls += 1;
    return OK;
  };
  const cfg = { ...cfgWith(CLAUDE), budget: { usd } };
  const result = await spawnSession(
    { cfg, level: 'L3', role: 'coder', promptPath: writeIn(freshDir('pk'), 'brief.md', ''), cwd: freshDir('ws'), run },
    { ...deps, exec: /** @type {any} */ (exec), readRows: ledger.readRows, writeRow: ledger.writeRow },
  );
  return { result, written: ledger.written, stderr: stderr.text(), execCalls };
}

test('message, spent >= budget: the exact B33 text, 0 exec calls, exactly 1 budget.refused row', async () => {
  const r = await coderL3('r-m-b33', 20, [{ event: 'session', run: 'r-m-b33', usd: 20.13 }]);
  const message = 'budget.usd 20.00 reached (spent 20.13); raise budget.usd or end the run';
  assert.deepEqual([r.result.status, r.result.reason, r.result.message, r.execCalls], ['unavailable', 'budget', message, 0]);
  assert.deepEqual(r.written.map((row) => [row.event, row.spent_usd, row.reserved_usd, row.estimate_usd]), [['budget.refused', 20.13, 0, 1.92]]);
  assert.equal(r.stderr, `code-forge: ${message}\n`);
});

test('message, running sessions + this one would pass the budget: spent 3.10, reserved 15.80, about 1.92 — exact text, exactly 1 budget.refused row', async () => {
  const release = reserveBudget('r-m-held', 15.8);
  try {
    const r = await coderL3('r-m-held', 20, [{ event: 'session', run: 'r-m-held', usd: 3.1 }]);
    const message = 'budget.usd 20.00 would be exceeded: spent 3.10, reserved by running sessions 15.80, this session about 1.92; it starts when running sessions finish or when budget.usd is raised';
    assert.deepEqual([r.result.status, r.result.reason, r.result.message, r.execCalls], ['unavailable', 'budget', message, 0]);
    assert.deepEqual(r.written.map((row) => [row.event, row.spent_usd, row.reserved_usd, row.estimate_usd]), [['budget.refused', 3.1, 15.8, 1.92]]);
    assert.equal(r.stderr, `code-forge: ${message}\n`);
  } finally {
    release();
  }
  assert.equal(reservedUsd('r-m-held'), 0);
});

test('message, this session alone is above the budget: about 1.92 vs 1.00 — exact text, exactly 1 budget.refused row', async () => {
  const r = await coderL3('r-m-big', 1, []);
  const message = "this session's estimated cost (about 1.92) is above budget.usd 1.00; raise budget.usd";
  assert.deepEqual([r.result.status, r.result.reason, r.result.message, r.execCalls], ['unavailable', 'budget', message, 0]);
  assert.deepEqual(r.written.map((row) => [row.event, row.spent_usd, row.reserved_usd, row.estimate_usd]), [['budget.refused', 0, 0, 1.92]]);
  assert.equal(r.stderr, `code-forge: ${message}\n`);
});

test('slot first, then reservation: 4 anthropic sessions hold all 4 slots, a 5th waits holding no reservation, an xai session with room starts', async () => {
  const { deps } = fakeDeps();
  const ledger = ledgerDouble();
  const g = gate();
  /** @type {Record<string, number>} */
  const calls = { claude: 0, grok: 0 };
  const exec = async (/** @type {string[]} */ argv) => {
    const cli = argv[0].endsWith('fake-grok') ? 'grok' : 'claude';
    calls[cli] += 1;
    await g.opened;
    return cli === 'grok' ? { ...OK, stdout: `${JSON.stringify({ text: 'fine' })}\n` } : OK;
  };
  const d = { ...deps, exec: /** @type {any} */ (exec), readRows: ledger.readRows, writeRow: ledger.writeRow };
  const anthropic = { ...cfgWith(CLAUDE), budget: { usd: 20 } };
  const runs = Array.from({ length: 5 }, () => spawnSession(reviewer(anthropic, 'r-slots'), d));
  const fourRunning = await waitFor(() => calls.claude === 4);
  const heldByFour = reservedUsd('r-slots');
  const locksByFour = budgetLockCalls();
  // xai L2: 100 in + 8000 out at 0.01 / 1K = 0.081
  const xai = spawnSession(reviewer({ ...cfgWith(GROK), budget: { usd: 20 } }, 'r-slots'), d);
  const xaiRunning = await waitFor(() => calls.grok === 1);
  const heldWithXai = reservedUsd('r-slots');
  g.open();
  const results = await Promise.all([...runs, xai]);
  assert.deepEqual([fourRunning, xaiRunning], [true, true]);
  // the 5th anthropic session never entered the budget lock while it waited for a slot
  assert.deepEqual([heldByFour, locksByFour, heldWithXai], [0.486, 4, 0.567]);
  assert.deepEqual(results.map((r) => r.status), ['ok', 'ok', 'ok', 'ok', 'ok', 'ok']);
  assert.deepEqual([calls.claude, calls.grok, budgetLockCalls(), reservedUsd('r-slots')], [5, 1, 6, 0]);
});

test('nothing reserved: the would-exceed text does not promise that running sessions will finish', async () => {
  const { budgetWouldExceedMessage } = await import('../../src/ledger/spend.mjs');
  assert.equal(budgetWouldExceedMessage(20, 19.95, 0, 0.12), 'budget.usd 20.00 would be exceeded: spent 19.95, this session about 0.12; raise budget.usd or end the run');
  assert.equal(budgetWouldExceedMessage(20, 3.1, 15.8, 1.92), 'budget.usd 20.00 would be exceeded: spent 3.10, reserved by running sessions 15.80, this session about 1.92; it starts when running sessions finish or when budget.usd is raised');
});
