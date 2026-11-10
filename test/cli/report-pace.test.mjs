import assert from 'node:assert/strict';
import * as fs from 'node:fs/promises';
import { performance } from 'node:perf_hooks';
import { mock, test } from 'node:test';

// The fixture is the ledger reader's output: no live ledger, worker, or model is used.
let ledger = [];
mock.module('node:fs/promises', { namedExports: { ...fs, stat: async () => ({}) } });
mock.module('../../src/ledger/write.mjs', { namedExports: { readAllRows: async () => ledger } });
const { runReport } = await import('../../src/cli/report.mjs');

const ts = (minutes) => new Date(Date.UTC(2026, 10, 1) + minutes * 60000).toISOString();
const row = (event, minutes, extra = {}) => ({ event, run: 'r1', block: 'A', ts: ts(minutes), ...extra });
const zero = {
  openToCloseMinutes: 0, coderMinutes: 0, reviewWaitMinutes: 0,
  reviewTimeouts: 0, reviewUnavailable: 0, gateMinutes: 0, gateRedCount: 0,
  landWaitMinutes: 0, claimsAfterOpen: 0, stopCount: 0,
  machineWaitMinutes: 0, deviceMinutes: 0, lockWaitMinutes: 0, lockWaitCount: 0,
};
const header = 'run | block | open->close min | coder min | review wait min | review timeouts | unavailable answers | gate min | gate reds | close->land min | claims after open | stops | machine.wait min | device min | lock wait min | lock waits';

async function report(rows, json = true) {
  ledger = rows;
  let stdout = '', stderr = '';
  const code = await runReport(['--slug', 'pace-fixture', ...(json ? ['--json'] : [])], {
    stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } },
  });
  assert.equal(code, 0, `report exit: ${stderr}`);
  assert.equal(stderr, '', 'report stderr');
  return json ? JSON.parse(stdout) : stdout;
}

test('report prints and JSON carries exact fixture pace per block and run, including absent kinds', async () => {
  const rows = [
    row('block.claim', -1, { path: 'before.mjs' }),
    row('dispatch', 0),
    row('session', 4, { role: 'coder', duration_ms: 240000 }),
    row('session', 5, { role: 'coder', duration_ms: 60000 }),
    row('session', 6, { role: 'reviewer', duration_ms: 600000 }),
    row('review.result', 8, { queued_at: ts(5), started_at: ts(7), duration_ms: 60000 }),
    row('review.result', 12, { queued_at: ts(8), started_at: ts(11), duration_ms: 60000 }),
    row('review.session_timeout', 9), row('review.session_timeout', 10),
    row('review.unavailable', 10),
    row('gate.red', 13, { duration_ms: 60000 }),
    row('gate.done', 14, { duration_ms: 120000, all_ok: false }),
    row('gate.done', 15, { duration_ms: 30000, all_ok: true }),
    row('block.claim', 16, { path: 'after.mjs' }),
    row('block.claim', 17, { path: 'another.mjs' }),
    row('machine.wait', 18, { duration_ms: 90000 }),
    row('device.boot', 18, { duration_ms: 120000 }),
    row('device.test', 19, { duration_ms: 30000 }),
    row('lock.wait', 19, { wait_ms: 15000 }),
    row('lock.wait', 19, { duration_ms: 45000 }),
    row('block.stop', 19), row('block.stop', 19.5),
    row('block.close', 20), row('land.timing', 25),
    row('dispatch', 30, { block: 'B' }), row('block.close', 32, { block: 'B' }),
    row('dispatch', 0, { run: 'r2' }), row('block.close', 3, { run: 'r2' }),
    row('gate.red', 33, { source: 'shadow' }),
    row('machine.wait', 33, { block: null, duration_ms: 60000 }),
  ];
  const a = { ...zero, openToCloseMinutes: 20, coderMinutes: 5, reviewWaitMinutes: 5,
    reviewTimeouts: 2, reviewUnavailable: 1, gateMinutes: 2.5, gateRedCount: 1,
    landWaitMinutes: 5, claimsAfterOpen: 2, stopCount: 2, machineWaitMinutes: 1.5,
    deviceMinutes: 2.5, lockWaitMinutes: 1, lockWaitCount: 2 };
  const expected = {
    blocks: [{ run: 'r1', block: 'A', ...a }, { run: 'r1', block: 'B', ...zero, openToCloseMinutes: 2 },
      { run: 'r2', block: 'A', ...zero, openToCloseMinutes: 3 }],
    runs: [{ run: 'r1', ...a, openToCloseMinutes: 22, machineWaitMinutes: 2.5 },
      { run: 'r2', ...zero, openToCloseMinutes: 3 }],
    total: { ...a, openToCloseMinutes: 25, machineWaitMinutes: 2.5 },
  };
  assert.deepEqual((await report(rows)).pace, expected, 'all 14 pace numbers for three block entries, two runs and total');
  const text = await report(rows, false);
  // B358: pace is a SECTION_NAMES section printed before run_time, so its table ends at that header
  assert.equal(text.split('== pace ==\n')[1]?.split('== run_time ==\n')[0], [header,
    'r1 | A | 20 | 5 | 5 | 2 | 1 | 2.5 | 1 | 5 | 2 | 2 | 1.5 | 2.5 | 1 | 2',
    'r1 | B | 2 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0',
    'r2 | A | 3 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0',
    'r1 | total | 22 | 5 | 5 | 2 | 1 | 2.5 | 1 | 5 | 2 | 2 | 2.5 | 2.5 | 1 | 2',
    'r2 | total | 3 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0',
    'all runs | total | 25 | 5 | 5 | 2 | 1 | 2.5 | 1 | 5 | 2 | 2 | 2.5 | 2.5 | 1 | 2', '',
  ].join('\n'), 'pace table has exactly three block rows, two run totals and one ledger total');
});

test('pace boundary: empty ledger and malformed or incomplete durations produce zero numbers', async () => {
  assert.deepEqual((await report([])).pace, { blocks: [], runs: [], total: zero }, 'empty pace totals');
  const rows = [
    row('dispatch', 10), row('block.close', 9), row('land.timing', 8),
    row('session', 11, { role: 'coder', duration_ms: -1 }),
    row('gate.done', 11, { duration_ms: '60000' }),
    row('machine.wait', 11, { duration_ms: Infinity }),
    row('device.boot', 11),
    row('review.result', 11, { queued_at: ts(10), started_at: 'invalid' }),
    row('block.close', 12, { ts: null }),
    row('dispatch', 12, { block: 'open' }),
    row('block.close', 15, { source: 'shadow' }),
  ];
  assert.deepEqual((await report(rows)).pace, {
    blocks: [{ run: 'r1', block: 'A', ...zero }, { run: 'r1', block: 'open', ...zero }],
    runs: [{ run: 'r1', ...zero }], total: zero,
  }, 'reversed endpoints, absent kinds, invalid values, open blocks and shadow rows add zero');
});

test('10,000 ledger rows report exact pace in under 1 s', async (t) => {
  const rows = Array.from({ length: 10000 }, (_, i) => row('machine.wait', i, {
    block: `B${i % 100}`, duration_ms: 600,
  }));
  const start = performance.now();
  const pace = (await report(rows)).pace;
  const elapsed = performance.now() - start;
  assert.deepEqual(pace, {
    blocks: Array.from({ length: 100 }, (_, i) => ({ run: 'r1', block: `B${i}`, ...zero, machineWaitMinutes: 1 })),
    runs: [{ run: 'r1', ...zero, machineWaitMinutes: 100 }], total: { ...zero, machineWaitMinutes: 100 },
  }, '10,000 rows contribute exactly 100 block minutes and 100 run/total minutes');
  assert.ok(elapsed < 1000, `10,000-row report took ${elapsed.toFixed(3)} ms (limit 1000 ms)`);
  t.diagnostic(`10,000-row report: ${elapsed.toFixed(3)} ms`);
});
