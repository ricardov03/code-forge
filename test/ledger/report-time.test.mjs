import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { buildReport, buildSpend } from '../../src/ledger/report.mjs';
import { runReport } from '../../src/cli/report.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { captureStream, withTempHome } from './helpers.mjs';

const ts = (minutes) => new Date(Date.UTC(2026, 9, 20) + minutes * 60000).toISOString();
const row = (event, minutes, extra = {}) => ({ event, run: 'r1', ts: ts(minutes), ...extra });

test('B150 clause 2: block text labels null runs outside runs while JSON and export keep run null', async () => {
  await withTempHome(async (home) => {
    const slug = 'report-outside-block';
    for (const r of [
      row('session', 0, { run: null, block: 'outside', usd: 0.25 }),
      row('session', 1, { block: 'inside', usd: 2 }),
    ]) await appendRow(r, { slug });
    const stdout = captureStream(), stderr = captureStream();
    assert.equal(await runReport(['--slug', slug], { stdout, stderr }), 0);
    assert.equal(stderr.text, '');
    const blocks = stdout.text.split('== cost_per_block ==\n')[1].split('== ')[0];
    assert.deepEqual(blocks.split('\n').filter((line) => line.includes(' | ')), [
      'run | block | USD | wall | human wait | status',
      'outside runs | outside | $0.2500 | - | - | open',
      'r1 | inside | $2.0000 | - | - | open',
    ]);
    assert.equal(blocks.split('(none)').length - 1, 0, 'no fake run column in cost_per_block');
    const json = captureStream();
    assert.equal(await runReport(['--slug', slug, '--json'], { stdout: json, stderr }), 0);
    const sections = JSON.parse(json.text);
    assert.deepEqual(sections.cost_per_block.map((b) => [b.run, b.block, b.totalUsd]), [
      [null, 'outside', 0.25], ['r1', 'inside', 2],
    ]);
    assert.deepEqual(sections.cost_per_block[0], {
      run: null, block: 'outside', level: null, lane: null, completed: false,
      reviewUsd: 0, coderUsd: 0, s1s2Usd: 0, factsUsd: 0, otherUsd: 0.25,
      proofTimeMs: 0, wallMs: null, humanWaitMs: null, totalUsd: 0.25, unknownUsdSessions: 0,
    });
    const exportDir = path.join(home, 'export');
    assert.equal(await runReport(['--slug', slug, '--export', exportDir], { stdout: captureStream(), stderr }), 0);
    assert.deepEqual(JSON.parse(await readFile(path.join(exportDir, 'cost_per_block.json'), 'utf8')), sections.cost_per_block);
    assert.equal(stderr.text, '');
  });
});

test('B150 clause 2 boundary: absent run on a reused block stays outside both real runs', async () => {
  await withTempHome(async () => {
    const slug = 'report-outside-reused';
    for (const r of [
      { event: 'session', block: 'shared', usd: 0 },
      row('session', 1, { block: 'shared', usd: 1 }),
      row('session', 2, { run: 'r2', block: 'shared', usd: 2 }),
    ]) await appendRow(r, { slug });
    const stdout = captureStream(), stderr = captureStream();
    assert.equal(await runReport(['--slug', slug], { stdout, stderr }), 0);
    assert.equal(stderr.text, '');
    const blocks = stdout.text.split('== cost_per_block ==\n')[1].split('== ')[0];
    assert.deepEqual(blocks.split('\n').filter((line) => line.includes(' | ')), [
      'run | block | USD | wall | human wait | status',
      'outside runs | shared | $0.0000 | - | - | open',
      'r1 | shared | $1.0000 | - | - | open',
      'r2 | shared | $2.0000 | - | - | open',
    ]);
    assert.equal(blocks.split('(none)').length - 1, 0);
    const json = captureStream();
    assert.equal(await runReport(['--slug', slug, '--json'], { stdout: json, stderr }), 0);
    assert.deepEqual(JSON.parse(json.text).cost_per_block.map((b) => [b.run, b.block, b.totalUsd]), [
      [null, 'shared', 0], ['r1', 'shared', 1], ['r2', 'shared', 2],
    ]);
    assert.equal(stderr.text, '');
  });
});

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

test('B149 clause 2: stopped blocks have wall time to the stop; open, waiting and redispatched blocks stay untimed', () => {
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
    row('dispatch', 1, { block: 'stop-only' }),
    row('block.stop', 7, { block: 'stop-only' }),
    row('dispatch', 0, { block: 'reopened', attempt: 1 }),
    row('block.stop', 2, { block: 'reopened' }),
    row('dispatch', 3, { block: 'reopened', attempt: 2 }),
    row('session', 4, { block: 'reopened', usd: 1 }),
    row('dispatch', 0, { block: 'completed', attempt: 1 }),
    row('block.stop', 2, { block: 'completed' }),
    row('dispatch', 3, { block: 'completed', attempt: 2 }),
    row('block.close', 8, { block: 'completed' }),
  ];
  assert.deepEqual(buildReport(rows).sections.cost_per_block.map((b) =>
    [b.block, b.completed, Object.hasOwn(b, 'stopped') ? b.stopped : 'omitted', b.wallMs, b.humanWaitMs]), [
    ['open', false, 'omitted', null, null], ['waiting', false, 'omitted', null, null],
    ['retry', false, 'omitted', null, null], ['stopped', false, true, 300000, null],
    ['stop-only', false, true, 360000, null], ['reopened', false, 'omitted', null, null],
    ['completed', true, 'omitted', 480000, null],
  ]);
});

test('B149 clause 2 boundary: stopped blocks ignore invalid timestamps and shadow lifecycle rows', () => {
  const rows = [
    row('dispatch', 0, { block: 'valid' }),
    row('block.close', 1, { block: 'valid' }),
    row('block.awaiting_human_check', 2, { block: 'valid' }),
    row('block.human_pass', 3, { block: 'valid' }),
    row('block.stop', 4, { block: 'valid' }),
    ...[undefined, null, 123, {}, 'bad-ts', '2026-02-30T00:00:00.000Z'].map((ts) =>
      ({ event: 'block.stop', run: 'r1', block: 'valid', ts })),
    row('dispatch', 5, { block: 'valid', source: 'shadow' }),
    row('block.close', 6, { block: 'valid', source: 'shadow' }),
    row('dispatch', 0, { block: 'untimed' }),
    row('block.stop', 4, { block: 'untimed', ts: 'bad-ts' }),
    row('block.stop', 5, { block: 'no-dispatch' }),
    row('dispatch', 6, { block: 'reversed' }),
    row('block.stop', 5, { block: 'reversed' }),
  ];
  assert.deepEqual(buildReport(rows).sections.cost_per_block.map((b) =>
    [b.block, b.completed, b.stopped, b.wallMs, b.humanWaitMs]), [
    ['valid', false, true, 240000, null], ['untimed', false, true, null, null],
    ['no-dispatch', false, true, null, null], ['reversed', false, true, null, null],
  ]);
});

test('B149 clause 1: rows without string run ids keep spend outside runs without adding runs or wall time', () => {
  const rows = [
    row('note', -10000, { run: null }),
    row('run.start', 0),
    row('session', 1, { usd: 2 }),
    row('run.end', 10),
    row('session', 10000, { run: null, usd: 1.25 }),
    row('session', 10001, { run: undefined, cost_usd: 0.75 }),
    row('session', 10002, { run: 42, provider: 'anthropic', level: 'L1', tokens_in: 1000000, tokens_out: 0 }),
    row('session.background', 10003, { run: null }),
    row('session', 20000, { run: null, usd: 999, source: 'shadow' }),
  ];
  const spend = buildSpend(rows);
  assert.deepEqual(buildReport(rows).sections.run_time, {
    runs: [{ run: 'r1', started: ts(0), ended: ts(10), wallMs: 600000, usd: 2, blocks_closed: 0, blocks_stopped: 0 }],
    outside_runs: { usd: 5, rows: 5 },
    total: { runs: 1, wallMs: 600000, usd: 7, blocks_closed: 0 },
  });
  assert.equal(spend.total.usd, 7, 'full ledger spend includes outside-run explicit, legacy and token-estimated costs');
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

test('B149 clause 1 boundary: empty and all-run ledgers omit outside_runs; outside-only rows have zero run totals', () => {
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
      { run: 'old', started: null, ended: null, wallMs: null, usd: 0, blocks_closed: 0, blocks_stopped: 0, open: true },
    ],
    outside_runs: { usd: 0.25, rows: 3 },
    total: { runs: 1, wallMs: 0, usd: 0.25, blocks_closed: 0 },
  });
  assert.equal(Object.hasOwn(buildReport(rows.slice(3)).sections.run_time, 'outside_runs'), false);
  assert.deepEqual(buildReport(rows.slice(0, 3)).sections.run_time, {
    runs: [], outside_runs: { usd: 0.25, rows: 3 },
    total: { runs: 0, wallMs: 0, usd: 0.25, blocks_closed: 0 },
  });
  assert.deepEqual(buildReport([row('note', 0, { run: null })]).sections.run_time, {
    runs: [], outside_runs: { usd: 0, rows: 1 },
    total: { runs: 0, wallMs: 0, usd: 0, blocks_closed: 0 },
  });
  assert.deepEqual(buildReport(rows).sections.cost_per_block.map((b) => [b.wallMs, b.humanWaitMs]), [[null, null]]);
});

test('B149 clause 3: CLI text shows completed, stopped and open blocks and outside-run spend before the run total', async () => {
  await withTempHome(async () => {
    const slug = 'report-lifecycle';
    for (const r of [
      row('note', -10000, { run: null }),
      row('run.start', 0),
      row('dispatch', 0, { block: 'stopped' }),
      row('block.stop', 5, { block: 'stopped' }),
      row('dispatch', 0, { block: 'completed' }),
      row('block.close', 10, { block: 'completed' }),
      row('dispatch', 0, { block: 'open' }),
      row('session', 1, { block: 'open', usd: 2 }),
      row('run.end', 15),
      row('session', 10000, { run: null, usd: 0.25 }),
    ]) await appendRow(r, { slug });
    const stdout = captureStream(), stderr = captureStream();
    assert.equal(await runReport(['--slug', slug], { stdout, stderr }), 0);
    assert.equal(stderr.text, '');
    const blocks = stdout.text.split('== cost_per_block ==\n')[1].split('== ')[0];
    assert.deepEqual(blocks.split('\n').filter((line) => line.startsWith('r1 | ')), [
      'r1 | stopped | $0.0000 | 5m 0s | - | stopped',
      'r1 | completed | $0.0000 | 10m 0s | - | completed',
      'r1 | open | $2.0000 | - | - | open',
    ]);
    const runs = stdout.text.split('== run_time ==\n')[1].split('spend per run')[0];
    assert.equal(runs,
      'run | started | ended | wall | USD | closed | stopped\n' +
      `r1 | ${ts(0)} | ${ts(15)} | 15m 0s | $2.0000 | 1 | 1\n` +
      'outside runs: $0.2500 (2 rows, no wall time)\n' +
      'total (1 run): 15m 0s | $2.2500 | 1 closed\n');
    assert.equal(runs.includes('(none)'), false);
  });
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
