import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { runReport } from '../../src/cli/report.mjs';
import { buildReport, SECTION_NAMES } from '../../src/ledger/report.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { captureStream, withTempHome } from './helpers.mjs';

async function report(args) {
  const stdout = captureStream(), stderr = captureStream();
  assert.equal(await runReport(['--slug', 'specialists', ...args], { stdout, stderr }), 0, stderr.text);
  assert.equal(stderr.text, '');
  return stdout.text;
}

test('B154 clause 2: specialist_outcomes is last, with one line per outcome and totals in text, JSON and export', async () => {
  await withTempHome(async (home) => {
    const outcomes = [
      { skill: 'logo-designer', step: 'design', status: 'approved', cost_usd: 1.25, minutes: 12 },
      { skill: 'copywriter', step: null, status: 'approved', cost_usd: 0.75, minutes: 8 },
      { skill: 'editor', step: 'edit', status: 'approved', cost_usd: null, minutes: null },
    ];
    /** @type {Record<string, any>[]} */
    const rows = outcomes.map((r) => ({ event: 'outcome.recorded', ...r }));
    rows.push({ event: 'outcome', skill: 'calibration', cost_usd: 99, minutes: 99 });
    for (const row of rows) await appendRow(row, { slug: 'specialists' });
    const expected = { outcomes, total: { count: 3, cost_usd: 2, minutes: 20 } };
    assert.equal(SECTION_NAMES.length, 17);
    assert.deepEqual(SECTION_NAMES.slice(-2), ['run_time', 'specialist_outcomes']);
    assert.deepEqual(buildReport(rows).sections.specialist_outcomes, expected);
    assert.deepEqual(JSON.parse(await report(['--json'])).specialist_outcomes, expected);
    const text = await report([]);
    assert.equal(text.match(/^== /gm).length, 17);
    assert.equal(text.slice(text.indexOf('== specialist_outcomes ==')), '== specialist_outcomes ==\n' +
      'skill | step | status | cost_usd | minutes\n' +
      'logo-designer | design | approved | $1.2500 | 12\n' +
      'copywriter | - | approved | $0.7500 | 8\n' +
      'editor | edit | approved | - | -\n' +
      'total (3 outcomes): $2.0000 | 20 minutes\n');
    const dir = path.join(home, 'export');
    assert.equal(await report(['--export', dir]), `exported 17 section file(s) to ${dir}\n`);
    assert.deepEqual(JSON.parse(await readFile(path.join(dir, 'specialist_outcomes.json'), 'utf8')), expected);
  });
});

test('B154 clause 2 boundary: no recorded outcomes yields an empty section and zero totals', async () => {
  await withTempHome(async () => {
    const row = { event: 'outcome', skill: 'calibration', cost_usd: 99, minutes: 99 };
    await appendRow(row, { slug: 'specialists' });
    const expected = { outcomes: [], total: { count: 0, cost_usd: 0, minutes: 0 } };
    assert.deepEqual(buildReport([]).sections.specialist_outcomes, expected);
    assert.deepEqual(JSON.parse(await report(['--json'])).specialist_outcomes, expected);
    const text = await report([]);
    assert.equal(text.slice(text.indexOf('== specialist_outcomes ==')), '== specialist_outcomes ==\n' +
      'skill | step | status | cost_usd | minutes\ntotal (0 outcomes): $0.0000 | 0 minutes\n');
  });
});
