import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildReport, buildSpend } from '../../src/ledger/report.mjs';

const ts = (minutes) => new Date(Date.UTC(2026, 9, 20) + minutes * 60000).toISOString();
const row = (event, minutes, extra = {}) => ({ event, run: 'r1', ts: ts(minutes), ...extra });

test('B147 clause 1: block times span first dispatch to final completion, including human wait and attempts', () => {
  const rows = [
    row('dispatch', 0, { block: 'closed' }),
    row('block.close', 12.5, { block: 'closed' }),
    row('dispatch', 1, { block: 'human' }),
    row('block.close', 5, { block: 'human' }),
    row('block.awaiting_human_check', 6, { block: 'human' }),
    row('block.human_pass', 16, { block: 'human' }),
    row('dispatch', 2, { block: 'retry' }),
    row('block.close', 4, { block: 'retry' }),
    row('dispatch', 8, { block: 'retry' }),
    row('block.close', 20, { block: 'retry' }),
    row('dispatch', 0, { run: 'r2', block: 'closed' }),
    row('block.close', 1, { run: 'r2', block: 'closed' }),
  ];
  assert.deepEqual(buildReport(rows).sections.cost_per_block.map((b) =>
    [b.run, b.block, b.completed, b.wallMs, b.humanWaitMs]), [
    ['r1', 'closed', true, 750000, null],
    ['r1', 'human', true, 900000, 600000],
    ['r1', 'retry', true, 1080000, null],
    ['r2', 'closed', true, 60000, null],
  ]);
});

test('B147 clause 1 boundary: open, waiting, stopped and redispatched blocks have null times', () => {
  const rows = [
    row('dispatch', 0, { block: 'open' }),
    row('session', 1, { block: 'open', usd: 1 }),
    row('dispatch', 0, { block: 'waiting' }),
    row('block.close', 2, { block: 'waiting' }),
    row('block.awaiting_human_check', 3, { block: 'waiting' }),
    row('dispatch', 0, { block: 'retry' }),
    row('block.close', 2, { block: 'retry' }),
    row('dispatch', 4, { block: 'retry' }),
    row('dispatch', 0, { block: 'stopped' }),
    row('block.close', 2, { block: 'stopped' }),
    row('block.awaiting_human_check', 3, { block: 'stopped' }),
    row('block.stop', 5, { block: 'stopped' }),
  ];
  assert.deepEqual(buildReport(rows).sections.cost_per_block.map((b) =>
    [b.block, b.completed, b.wallMs, b.humanWaitMs]), [
    ['open', false, null, null], ['waiting', false, null, null],
    ['retry', false, null, null], ['stopped', false, null, null],
  ]);
});

test('B147 clause 2: run_time includes ended and open runs, existing spend totals, distinct block counts and ledger total', () => {
  const rows = [
    row('note', -1),
    row('run.start', 0),
    row('dispatch', 1, { block: 'b1' }),
    row('session', 2, { block: 'b1', usd: 1.25 }),
    row('session', 3, { cost_usd: 0.75 }),
    row('block.close', 4, { block: 'b1' }),
    row('dispatch', 5, { block: 'b1' }),
    row('block.close', 6, { block: 'b1' }),
    row('block.close', 7, { block: 'human' }),
    row('block.awaiting_human_check', 8, { block: 'human' }),
    row('block.stop', 9, { block: 'stopped' }),
    row('block.stop', 10, { block: 'stopped' }),
    row('run.end', 20),
    row('note', 25),
    // No run.start: use the first timestamp; a repeated block id belongs to its run.
    row('dispatch', 30, { run: 'r2', block: 'b1' }),
    row('session', 32, { run: 'r2', block: 'b1', usd: 0.5 }),
    row('session.background', 33, { run: 'r2', block: 'b1' }),
    row('block.close', 35, { run: 'r2', block: 'b1' }),
  ];
  const { run_time } = buildReport(rows).sections;
  assert.deepEqual(run_time, {
    runs: [
      { run: 'r1', started: ts(0), ended: ts(20), wallMs: 1200000, usd: 2, blocks_closed: 1, blocks_stopped: 1 },
      { run: 'r2', started: ts(30), ended: null, wallMs: 300000, usd: 0.5, blocks_closed: 1, blocks_stopped: 0, open: true },
    ],
    total: { runs: 2, wallMs: 1500000, usd: 2.5, blocks_closed: 2 },
  });
  const spend = buildSpend(rows);
  assert.deepEqual(run_time.runs.map(({ run, usd }) => ({ run, usd })), spend.runs.map(({ run, usd }) => ({ run, usd })));
  assert.equal(run_time.total.usd, spend.total.usd);
});

test('B147 clause 2 boundary: empty and untimed legacy runs retain spend and null times', () => {
  assert.deepEqual(buildReport([]).sections.run_time, {
    runs: [], total: { runs: 0, wallMs: 0, usd: 0, blocks_closed: 0 },
  });
  const rows = [
    { event: 'dispatch', block: 'b1' },
    { event: 'session', block: 'b1', usd: 0.25 },
    { event: 'block.close', block: 'b1' },
    { event: 'run.start', run: 'old' },
    { event: 'run.end', run: 'old' },
  ];
  assert.deepEqual(buildReport(rows).sections.run_time, {
    runs: [
      { run: null, started: null, ended: null, wallMs: null, usd: 0.25, blocks_closed: 1, blocks_stopped: 0, open: true },
      { run: 'old', started: null, ended: null, wallMs: null, usd: 0, blocks_closed: 0, blocks_stopped: 0, open: true },
    ],
    total: { runs: 2, wallMs: 0, usd: 0.25, blocks_closed: 1 },
  });
  assert.deepEqual(buildReport(rows).sections.cost_per_block.map((b) => [b.wallMs, b.humanWaitMs]), [[null, null]]);
});

test('B147 clause 3: shadow rows and missing or invalid ISO timestamps do not change valid times', () => {
  const rows = [
    row('run.start', 0),
    row('dispatch', 1, { block: 'b1' }),
    row('block.close', 4, { block: 'b1' }),
    row('block.awaiting_human_check', 5, { block: 'b1' }),
    row('block.human_pass', 10, { block: 'b1' }),
    row('run.end', 15),
  ];
  const noise = [
    row('run.start', -20, { source: 'shadow' }),
    row('dispatch', -10, { block: 'b1', source: 'shadow' }),
    ...[undefined, null, 123, {}, 'bad-ts', '2026/10/20', '2026-02-30T00:00:00.000Z'].map((ts) =>
      ({ event: 'dispatch', run: 'r1', block: 'b1', ts })),
    ...rows,
    { event: 'block.human_pass', run: 'r1', block: 'b1', ts: 'bad-ts' },
    { event: 'run.end', run: 'r1', ts: 'bad-ts' },
    row('session', 99, { block: 'b1', source: 'shadow', usd: 999 }),
    row('block.human_pass', 100, { block: 'b1', source: 'shadow' }),
    row('run.end', 200, { source: 'shadow' }),
    row('run.start', 300, { run: 'shadow-only', source: 'shadow' }),
  ];
  const times = (input) => {
    const { cost_per_block, run_time } = buildReport(input).sections;
    return { blocks: cost_per_block.map((b) => [b.block, b.wallMs, b.humanWaitMs]), run_time };
  };
  assert.deepEqual(times(noise), {
    blocks: [['b1', 540000, 300000]],
    run_time: {
      runs: [{ run: 'r1', started: ts(0), ended: ts(15), wallMs: 900000, usd: 0, blocks_closed: 1, blocks_stopped: 0 }],
      total: { runs: 1, wallMs: 900000, usd: 0, blocks_closed: 1 },
    },
  });
  assert.deepEqual(times(noise), times(rows));
});

test('B147 clause 3 boundary: invalid endpoints stay null; open runs stop at the last valid timestamp', () => {
  const rows = [
    { event: 'run.start', run: 'r1', ts: 'bad-ts' },
    row('dispatch', 1, { block: 'b1' }),
    row('block.close', 4, { block: 'b1', ts: 'bad-ts' }),
    row('note', 6),
    { event: 'note', run: 'r1' },
    { event: 'run.end', run: 'r1', ts: 'bad-ts' },
  ];
  const { cost_per_block, run_time } = buildReport(rows).sections;
  assert.deepEqual(cost_per_block.map((b) => [b.block, b.wallMs, b.humanWaitMs]), [['b1', null, null]]);
  assert.deepEqual(run_time, {
    runs: [{ run: 'r1', started: ts(1), ended: null, wallMs: 300000, usd: 0, blocks_closed: 1, blocks_stopped: 0, open: true }],
    total: { runs: 1, wallMs: 300000, usd: 0, blocks_closed: 1 },
  });
});
