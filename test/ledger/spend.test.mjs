/**
 * B33: session USD from tokens × the static price table, unknown prices never guessed, run/report
 * totals, and the 80 % / 100 % `budget.usd` check (`checkBudget`).
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildReport, buildSpend } from '../../src/ledger/report.mjs';
import { budgetUsdOf, checkBudget, priceSession, rowUsd, runSpend } from '../../src/ledger/spend.mjs';
import { spendLines } from '../../src/cli/report.mjs';

/** @returns {{write: (s: string) => boolean, text: string}} */
function sink() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

test('priceSession: exact usd for known prices (anthropic L2 1000+500 = 0.0225; openai L1 3000+1000 = 0.01; xai L3 250+250 = 0.01)', () => {
  assert.deepEqual(priceSession({ provider: 'anthropic', level: 'L2', tokens_in: 1000, tokens_out: 500 }), { usd: 0.0225, usd_unknown: false });
  assert.deepEqual(priceSession({ provider: 'openai', level: 'L1', tokens_in: 3000, tokens_out: 1000 }), { usd: 0.01, usd_unknown: false });
  assert.deepEqual(priceSession({ provider: 'xai', level: 'L3', tokens_in: 250, tokens_out: 250 }), { usd: 0.01, usd_unknown: false });
});

test('priceSession: unknown provider, unknown level, or no token count ⇒ usd null + usd_unknown, never a guess', () => {
  const unknown = { usd: null, usd_unknown: true };
  assert.deepEqual(priceSession({ provider: 'mistral', level: 'L2', tokens_in: 1000, tokens_out: 500 }), unknown);
  assert.deepEqual(priceSession({ provider: 'anthropic', level: 'L9', tokens_in: 1000, tokens_out: 500 }), unknown);
  assert.deepEqual(priceSession({ provider: 'anthropic', level: 'L2', tokens_in: null, tokens_out: null }), unknown);
  assert.equal(rowUsd({ event: 'session', usd: null, usd_unknown: true, cost_usd: 5 }), null);
  assert.equal(rowUsd({ event: 'session.background', provider: 'anthropic', level: 'L2' }), null);
});

test('budgetUsdOf: a number > 0 is the budget; absent, 0, negative or a string is no budget', () => {
  assert.deepEqual(
    [{ budget: { usd: 20 } }, {}, { budget: { usd: 0 } }, { budget: { usd: -1 } }, { budget: { usd: '20' } }].map(budgetUsdOf),
    [20, null, null, null, null],
  );
});

/** Two runs: r1 has 2 priced sessions, 1 unknown, 1 manual coder row; r2 one session; plus a shadow row. */
function twoRunLedger() {
  return [
    { event: 'run.start', run: 'r1' },
    { event: 'session', run: 'r1', block: 'B1', role: 'coder', provider: 'anthropic', level: 'L2', tokens_in: 1000, tokens_out: 500, usd: 0.0225 },
    { event: 'session', run: 'r1', block: 'B1', role: 'reviewer', provider: 'openai', level: 'L1', tokens_in: 3000, tokens_out: 1000, usd: 0.01 },
    { event: 'session', run: 'r1', block: 'B2', role: 'reviewer', provider: 'mistral', level: 'L1', tokens_in: 10, tokens_out: 10, usd: null, usd_unknown: true },
    { event: 'session', run: 'r1', block: 'B2', role: 'coder', manual: true, usd: 4.5, cost_source: 'manual' },
    { event: 'session', run: 'r2', block: 'B3', role: 'facts', provider: 'anthropic', level: 'L0', tokens_in: 2000, tokens_out: 0, usd: 0.002 },
    { event: 'session', run: 'r1', block: 'B1', role: 'coder', usd: 999, source: 'shadow' },
    { event: 'block.close', run: 'r1', block: 'B1' },
  ];
}

test('report totals are exact: per run (r1 4.5325 with 1 unknown, r2 0.002), overall 4.5345; open block B2 lists the manual row and the unknown session', () => {
  const rows = twoRunLedger();
  assert.deepEqual(runSpend(rows, 'r1'), { usd: 4.5325, unknown: 1, sessions: 4 });
  assert.deepEqual(buildSpend(rows), {
    runs: [
      { run: 'r1', usd: 4.5325, unknown: 1, sessions: 4 },
      { run: 'r2', usd: 0.002, unknown: 0, sessions: 1 },
    ],
    total: { usd: 4.5345, unknown: 1, sessions: 5 },
  });
  const blocks = buildReport(rows).sections.cost_per_block;
  assert.deepEqual(
    blocks.map((e) => [e.block, e.completed, e.coderUsd, e.reviewUsd, e.totalUsd, e.unknownUsdSessions]),
    [
      ['B1', true, 0.0225, 0.01, 0.0325, 0],
      ['B2', false, 4.5, 0, 4.5, 1],
      ['B3', false, 0, 0, 0.002, 0],
    ],
  );
  assert.equal(
    spendLines(buildSpend(rows)),
    [
      'spend per run (estimated USD, open blocks included):',
      '  run r1: $4.5325 · 4 session(s) · 1 with unknown price (not counted)',
      '  run r2: $0.0020 · 1 session(s)',
      '  total: $4.5345 · 5 session(s) · 1 with unknown price (not counted)',
      '',
    ].join('\n'),
  );
});

/** A ledger double: `rows` is what a read returns, and every write lands in it (reads see writes). */
function ledger(/** @type {Array<Record<string, any>>} */ seed) {
  const rows = [...seed];
  return { rows, writeRow: async (/** @type {Record<string, any>} */ row) => void rows.push(row) };
}

test('checkBudget: below 80 % nothing; exactly 80 % warns ONCE across 3 checks (the ledger row is the dedupe); a seeded warning row is never repeated', async () => {
  const stderr = sink();
  const low = ledger([{ event: 'session', run: 'w-low', usd: 15.99 }]);
  assert.deepEqual(await checkBudget({ budget: 20, run: 'w-low', rows: low.rows, writeRow: low.writeRow, stderr }), { refuse: false, spent: 15.99 });
  assert.equal(low.rows.length, 1);

  const at80 = ledger([{ event: 'session', run: 'w-80', usd: 16 }, { event: 'session', run: 'w-other', usd: 30 }]);
  for (let i = 0; i < 3; i += 1) {
    assert.deepEqual(await checkBudget({ budget: 20, run: 'w-80', rows: at80.rows, block: 'B1', role: 'reviewer', writeRow: at80.writeRow, stderr }), { refuse: false, spent: 16 });
  }
  assert.deepEqual(
    at80.rows.filter((r) => r.event === 'budget.warning'),
    [{ event: 'budget.warning', run: 'w-80', block: 'B1', role: 'reviewer', budget_usd: 20, spent_usd: 16, unknown_usd_sessions: 0 }],
  );
  assert.equal(stderr.text, 'code-forge: budget.usd: spent 16.00 of 20.00 (≥ 80%); new sessions stop at 100%\n');

  const seeded = ledger([{ event: 'session', run: 'w-row', usd: 17 }, { event: 'budget.warning', run: 'w-row' }]);
  await checkBudget({ budget: 20, run: 'w-row', rows: seeded.rows, writeRow: seeded.writeRow, stderr });
  assert.equal(seeded.rows.length, 2, 'a warning already in the ledger is not written again');
});

test('checkBudget boundaries at 4 decimals: 0.24 of 0.30 is exactly 80 % (warns); 0.2399 does not; spent === budget refuses', async () => {
  const at = ledger([{ event: 'session', run: 'b-at', usd: 0.24 }]);
  await checkBudget({ budget: 0.3, run: 'b-at', rows: at.rows, writeRow: at.writeRow, stderr: sink() });
  const below = ledger([{ event: 'session', run: 'b-below', usd: 0.2399 }]);
  await checkBudget({ budget: 0.3, run: 'b-below', rows: below.rows, writeRow: below.writeRow, stderr: sink() });
  assert.deepEqual([at.rows.length, below.rows.length], [2, 1]);
  const eq = ledger([{ event: 'session', run: 'b-eq', usd: 20 }]);
  const verdict = await checkBudget({ budget: 20, run: 'b-eq', rows: eq.rows, writeRow: eq.writeRow, stderr: sink() });
  assert.deepEqual([verdict.refuse, verdict.spent, eq.rows.map((r) => r.event)], [true, 20, ['session', 'budget.refused']]);
});

test('checkBudget without a writer: the in-process fallback warns once for the run', async () => {
  const stderr = sink();
  const rows = [{ event: 'session', run: 'w-nowriter', usd: 9 }];
  for (let i = 0; i < 2; i += 1) await checkBudget({ budget: 10, run: 'w-nowriter', rows, stderr });
  assert.equal(stderr.text.split('budget.usd: spent 9.00 of 10.00').length - 1, 1);
});

test('robust numbers: NaN / Infinity / negative / string amounts and null or string tokens are unknown, never 0, and never disable the budget', async () => {
  assert.equal(rowUsd({ event: 'session', usd: Number.NaN }), null);
  assert.equal(rowUsd({ event: 'session', usd: Number.POSITIVE_INFINITY }), null);
  assert.equal(rowUsd({ event: 'session', cost_usd: Number.NaN }), null);
  assert.equal(rowUsd({ event: 'session', cost_usd: -1 }), null);
  assert.equal(rowUsd({ event: 'session', cost_usd: '5' }), null);
  assert.deepEqual(priceSession({ provider: 'anthropic', level: 'L2', tokens_in: 1000, tokens_out: null }), { usd: null, usd_unknown: true });
  assert.deepEqual(priceSession({ provider: 'anthropic', level: 'L2', tokens_in: '1000', tokens_out: 0 }), { usd: null, usd_unknown: true });
  assert.deepEqual(priceSession({ provider: 'anthropic', level: 'L2', tokens_in: Number.NaN, tokens_out: 0 }), { usd: null, usd_unknown: true });
  const rows = [
    { event: 'session', run: 'nan', usd: Number.NaN },
    { event: 'session', run: 'nan', cost_usd: Number.POSITIVE_INFINITY },
    { event: 'session', run: 'nan', usd: 21 },
  ];
  assert.deepEqual(runSpend(rows, 'nan'), { usd: 21, unknown: 2, sessions: 3 });
  const verdict = await checkBudget({ budget: 20, run: 'nan', rows, stderr: sink() });
  assert.deepEqual([verdict.refuse, verdict.spent], [true, 21]);
});

test('cost_per_block groups by run + block: B1 in r1 and B1 in r2 are two entries with their own totals; shadow rows count nowhere', () => {
  const rows = [
    { event: 'session', run: 'r1', block: 'B1', role: 'coder', usd: 1.25 },
    { event: 'session', run: 'r1', block: 'B1', role: 'reviewer', usd: 0.5 },
    { event: 'block.close', run: 'r1', block: 'B1' },
    { event: 'session', run: 'r2', block: 'B1', role: 'coder', usd: 2 },
    { event: 'session', run: 'r2', block: 'B1', role: 'coder', usd: 100, source: 'shadow' },
  ];
  assert.deepEqual(
    buildReport(rows).sections.cost_per_block.map((e) => [e.run, e.block, e.completed, e.coderUsd, e.reviewUsd, e.totalUsd]),
    [
      ['r1', 'B1', true, 1.25, 0.5, 1.75],
      ['r2', 'B1', false, 2, 0, 2],
    ],
  );
  assert.deepEqual(buildSpend(rows).runs, [
    { run: 'r1', usd: 1.75, unknown: 0, sessions: 2 },
    { run: 'r2', usd: 2, unknown: 0, sessions: 1 },
  ]);
});

test('checkBudget: at 100 % refuses with the plain message and one budget.refused row', async () => {
  const stderr = sink();
  /** @type {Array<Record<string, any>>} */
  const written = [];
  const rows = [
    { event: 'session', run: 'f-100', usd: 20 },
    { event: 'session', run: 'f-100', usd: 0.13 },
    { event: 'session', run: 'other', usd: 50 },
  ];
  const verdict = await checkBudget({ budget: 20, run: 'f-100', rows, block: 'B2', role: 'coder', writeRow: async (row) => void written.push(row), stderr });
  assert.deepEqual(verdict, { refuse: true, spent: 20.13, message: 'budget.usd 20.00 reached (spent 20.13); raise budget.usd or end the run' });
  assert.equal(stderr.text, 'code-forge: budget.usd 20.00 reached (spent 20.13); raise budget.usd or end the run\n');
  assert.deepEqual(written, [{ event: 'budget.refused', run: 'f-100', block: 'B2', role: 'coder', budget_usd: 20, spent_usd: 20.13, unknown_usd_sessions: 0 }]);
});

test('checkBudget: a writer that throws on the budget.refused row still refuses (no throw out)', async () => {
  const stderr = sink();
  const rows = [{ event: 'session', run: 'f-throw', usd: 25 }];
  const writeRow = async () => {
    throw new Error('disk full');
  };
  const verdict = await checkBudget({ budget: 20, run: 'f-throw', rows, writeRow, stderr });
  assert.deepEqual(verdict, { refuse: true, spent: 25, message: 'budget.usd 20.00 reached (spent 25.00); raise budget.usd or end the run' });
  assert.equal(stderr.text, 'code-forge: budget.usd 20.00 reached (spent 25.00); raise budget.usd or end the run\n');
});

test('cost_per_block never splits a block: a run-less block.close joins B1 of r1 (ONE entry, completed, dollars included); in a block seen in two runs a run-less row stays apart', () => {
  const one = [
    { event: 'dispatch', block: 'B1', level: 'L2', lane: 'L1' },
    { event: 'session', run: 'r1', block: 'B1', role: 'coder', usd: 1.25 },
    { event: 'session', run: 'r1', block: 'B1', role: 'reviewer', usd: 0.5 },
    { event: 'block.close', block: 'B1' },
  ];
  assert.deepEqual(
    buildReport(one).sections.cost_per_block.map((e) => [e.run, e.block, e.level, e.lane, e.completed, e.coderUsd, e.reviewUsd, e.totalUsd]),
    [['r1', 'B1', 'L2', 'L1', true, 1.25, 0.5, 1.75]],
  );
  const two = [
    { event: 'session', run: 'r1', block: 'B1', role: 'coder', usd: 1 },
    { event: 'session', run: 'r2', block: 'B1', role: 'coder', usd: 2 },
    { event: 'block.close', block: 'B1' },
  ];
  assert.deepEqual(
    buildReport(two).sections.cost_per_block.map((e) => [e.run, e.block, e.completed, e.totalUsd]),
    [
      ['r1', 'B1', false, 1],
      ['r2', 'B1', false, 2],
      [null, 'B1', true, 0],
    ],
  );
});
