import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { buildReport, SECTION_NAMES } from '../../src/ledger/report.mjs';

const BIN = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');
const TMP_PARENT = await mkdtemp(path.join(os.tmpdir(), 'cf-b66-report-'));
after(() => rm(TMP_PARENT, { recursive: true, force: true }));

test('B136: report forecast section shows repeat counts for closed and open gates, scoped to the run', async () => {
  const rows = [
    { event: 'dispatch', run: 'r1', block: 'B1', lines: 50 },
    { event: 'gate.done', run: 'r1', block: 'B1', repeats: { n: 2, files: ['test/a.mjs'], failed: false } },
    { event: 'block.close', run: 'r1', block: 'B1', lines_actual: 45 },
    { event: 'block.close', run: 'r2', block: 'B1', lines_actual: 30 },
    { event: 'gate.done', run: 'r1', block: 'B2', repeats: { n: 10, files: ['test/b.mjs'], failed: true } },
    { event: 'gate.done', run: 'r1', block: 'B3', repeats: { n: 3, files: ['test/c.mjs'], failed: false } },
    { event: 'gate.done', run: 'r1', block: 'B3', all_ok: true },
    { event: 'gate.done', run: 'r1', block: 'B4', source: 'shadow', repeats: { n: 4, files: [], failed: false } },
  ];
  const expected = [
    { block: 'B1', forecast: 50, actual: 45, repeats: 2 },
    { block: 'B1', forecast: 50, actual: 30 },
    { block: 'B2', forecast: null, actual: null, repeats: 10 },
  ];
  assert.deepEqual(buildReport(rows).sections.forecast_vs_actual_lines, expected);
  assert.deepEqual(buildReport(rows.filter((r) => r.event !== 'gate.done')).sections.forecast_vs_actual_lines,
    [{ block: 'B1', forecast: 50, actual: 45 }, { block: 'B1', forecast: 50, actual: 30 }]);
  const text = await reportCli('repeat-counts', rows, (slug) => ['--slug', slug]);
  assert.equal(text.code, 0, text.stderr);
  const section = text.stdout.split('== forecast_vs_actual_lines ==\n')[1].split('== ')[0];
  assert.deepEqual(JSON.parse(section), expected);
});

function threeBlockLedger() {
  const rows = [
    { event: 'dispatch', block: 'b1', level: 'L1', lane: 'L1', lines: 500 },
    { event: 'dispatch', block: 'b2', level: 'L2', lane: 'L1', lines: 500 },
    { event: 'dispatch', block: 'b3', level: 'L1', lane: 'L0', lines: 500 },
    // b1: coder $1.00 + reviewer $0.50, completed
    { event: 'session', block: 'b1', role: 'coder', cost_usd: 1.0 },
    { event: 'session', block: 'b1', role: 'reviewer', cost_usd: 0.5 },
    { event: 'block.close', block: 'b1', status: 'complete' },
    // b2: coder $2.00 + judge $0.75 + facts $0.10, completed
    { event: 'session', block: 'b2', role: 'coder', cost_usd: 2.0 },
    { event: 'session', block: 'b2', role: 'judge', cost_usd: 0.75 },
    { event: 'session', block: 'b2', role: 'facts', cost_usd: 0.1 },
    { event: 'block.close', block: 'b2', status: 'complete' },
    // b3: spend exists but the block was never closed — listed OPEN with its running total (B33)
    { event: 'session', block: 'b3', role: 'coder', cost_usd: 0.3 },
  ];
  return rows;
}

test('B33: cost per block matches the hand-computed total for each of 3 blocks — 2 completed, the open b3 with its running total', () => {
  const { sections } = buildReport(threeBlockLedger());
  assert.equal(sections.cost_per_block.length, 3);
  assert.deepEqual(
    sections.cost_per_block.map((e) => [e.block, e.completed, e.totalUsd, e.unknownUsdSessions]),
    [
      ['b1', true, 1.5, 0],
      ['b2', true, 2.85, 0],
      ['b3', false, 0.3, 0],
    ],
  );
});

test('an unrecognized session role still spends real money: it lands in otherUsd, and totalUsd is NOT under-reported', () => {
  const rows = [
    { event: 'dispatch', block: 'b1', level: 'L1', lane: 'L1' },
    { event: 'session', block: 'b1', role: 'mystery-role', cost_usd: 1.23 },
    { event: 'session', block: 'b1', role: 'coder', cost_usd: 0.5 },
    { event: 'block.close', block: 'b1', status: 'complete' },
  ];
  const { sections } = buildReport(rows);
  const b1 = sections.cost_per_block[0];
  assert.equal(b1.otherUsd, 1.23);
  assert.equal(b1.totalUsd, 1.73, 'totalUsd must include the unrecognized role, not silently drop it');
});

test('review budget: degradedCount is driven by depth_unconstrained !== depth_chosen, independent of degrade_step (which can disagree)', () => {
  const rows = [
    { event: 'review.plan', block: 'b1', file: 'a.mjs', depth_unconstrained: 'full', depth_chosen: 'full', degrade_step: 0 },
    { event: 'review.plan', block: 'b1', file: 'b.mjs', depth_unconstrained: 'full', depth_chosen: 'quick', degrade_step: 3 },
    { event: 'review.plan', block: 'b1', file: 'c.mjs', depth_unconstrained: 'a-b-judge', depth_chosen: 'full', degrade_step: 4 },
    // A row whose degrade_step says "degraded" but the depth fields DISAGREE (equal) — must NOT count.
    { event: 'review.plan', block: 'b1', file: 'd.mjs', depth_unconstrained: 'full', depth_chosen: 'full', degrade_step: 2 },
    { event: 'outcome', block: 'b1', file: 'b.mjs', missed_after_degrade: true },
    { event: 'outcome', block: 'b1', file: 'c.mjs', missed_after_degrade: false },
  ];
  const { sections } = buildReport(rows);
  assert.equal(sections.review_budget.filesPlanned, 4);
  assert.equal(sections.review_budget.degradedCount, 2, 'only b.mjs and c.mjs actually have differing depth fields — d.mjs must not count despite degrade_step: 2');
  assert.equal(sections.review_budget.missedAfterDegrade, 1);
});

test('review budget: a row missing one depth field is NOT counted as degraded (undefined !== value must not look like degradation)', () => {
  const rows = [{ event: 'review.plan', block: 'b1', file: 'a.mjs', depth_unconstrained: 'full' /* depth_chosen missing */ }];
  const { sections } = buildReport(rows);
  assert.equal(sections.review_budget.degradedCount, 0);
});

test('review budget: missed_after_degrade on a file that was NEVER degraded does not inflate the count', () => {
  const rows = [
    { event: 'review.plan', block: 'b1', file: 'a.mjs', depth_unconstrained: 'full', depth_chosen: 'full' }, // not degraded
    { event: 'review.plan', block: 'b1', file: 'b.mjs', depth_unconstrained: 'full', depth_chosen: 'quick' }, // degraded
    { event: 'outcome', block: 'b1', file: 'b.mjs', missed_after_degrade: true },
    { event: 'outcome', block: 'b1', file: 'a.mjs', missed_after_degrade: true }, // a.mjs was never degraded
  ];
  const { sections } = buildReport(rows);
  assert.equal(sections.review_budget.missedAfterDegrade, 1, 'the a.mjs outcome row must not count — a.mjs was never in the degraded set');
});

test('review budget: a duplicate outcome row for the SAME degraded (block, file) does not double-count', () => {
  const rows = [
    { event: 'review.plan', block: 'b1', file: 'b.mjs', depth_unconstrained: 'full', depth_chosen: 'quick' },
    { event: 'outcome', block: 'b1', file: 'b.mjs', missed_after_degrade: true },
    { event: 'outcome', block: 'b1', file: 'b.mjs', missed_after_degrade: true }, // e.g. a re-run --scan-git
  ];
  const { sections } = buildReport(rows);
  assert.equal(sections.review_budget.missedAfterDegrade, 1);
});

test('shadow rows (source: shadow) never move a single aggregate', () => {
  const base = threeBlockLedger();
  const withShadow = [
    ...base,
    { event: 'session', block: 'b1', role: 'coder', cost_usd: 999, source: 'shadow' },
    { event: 'decision', block: 'b1', question: 'lane', source: 'shadow', confidence: 0.99 },
  ];
  assert.deepEqual(buildReport(base).sections, buildReport(withShadow).sections);
});

test('buildReport renders EXACTLY the SECTION_NAMES set — no renamed, missing, or extra section', () => {
  assert.deepEqual(Object.keys(buildReport([]).sections), [...SECTION_NAMES]);
});

test('escalations section reports the exact trigger per row, in order', () => {
  const rows = [
    { event: 'escalation', block: 'b1', trigger: 'retries' },
    { event: 'escalation', block: 'b1', trigger: 's2_ruling' },
  ];
  const { sections } = buildReport(rows);
  assert.deepEqual(sections.escalations, [
    { block: 'b1', trigger: 'retries' },
    { block: 'b1', trigger: 's2_ruling' },
  ]);
});

test('review_unavailable tallies exact counts by reason', () => {
  const rows = [
    { event: 'review.unavailable', block: 'b1', reason: 'timeout' },
    { event: 'review.unavailable', block: 'b1', reason: 'timeout' },
    { event: 'review.unavailable', block: 'b2', reason: 'schema' },
  ];
  const { sections } = buildReport(rows);
  assert.deepEqual(sections.review_unavailable, { timeout: 2, schema: 1 });
});

test('fix_rounds keys by block::file — the same file reviewed in two different blocks is tracked separately, not collapsed to one max', () => {
  const rows = [
    { event: 'review.done', block: 'b1', file: 'shared.mjs', round: 3 },
    { event: 'review.done', block: 'b2', file: 'shared.mjs', round: 1 },
  ];
  const { sections } = buildReport(rows);
  assert.equal(sections.fix_rounds['b1::shared.mjs'], 3);
  assert.equal(sections.fix_rounds['b2::shared.mjs'], 1);
});

test('run_stop_counts counts DISTINCT blocks, not rows — the same logical stop stamped on two rows for one block counts once', () => {
  const rows = [
    { event: 'session', block: 'b1', role: 'coder', overspend: true },
    { event: 'run.stop', block: 'b1', overspend: true, reason: 'budget' },
    { event: 'run.stop', block: 'b2', overspend: true, reason: 'budget' },
  ];
  const { sections } = buildReport(rows);
  assert.equal(sections.run_stop_counts.overspend, 2, 'b1 (2 rows) + b2 (1 row) = 2 distinct blocks, not 3 rows');
});

test('B19: live-shaped rows — 2 token-only session rows for B1 and a status-less block.close give ONE cost_per_block entry with the exact estimated USD', () => {
  const rows = [
    { event: 'dispatch', block: 'B1', level: 'L2', lane: 'L2', lines: 100 },
    // anthropic L2: (1000 + 500) / 1000 × 0.015 = 0.0225
    { event: 'session', block: 'B1', role: 'coder', provider: 'anthropic', level: 'L2', tokens_in: 1000, tokens_out: 500, tokens_source: 'reported', cost_source: null },
    // openai L1: (3000 + 1000) / 1000 × 0.0025 = 0.01
    { event: 'session', block: 'B1', role: 'reviewer', provider: 'openai', level: 'L1', tokens_in: 3000, tokens_out: 1000, tokens_source: 'reported', cost_source: null },
    // no token count at all: no dollars, not a crash
    { event: 'session', block: 'B1', role: 'facts', provider: 'anthropic', level: 'L0', tokens_in: null, tokens_out: null },
    { event: 'block.close', block: 'B1' },
    // B2 spent tokens but never closed: listed open (B33) — anthropic L2: 1000 / 1000 × 0.015 = 0.015
    { event: 'session', block: 'B2', role: 'coder', provider: 'anthropic', level: 'L2', tokens_in: 1000, tokens_out: 0 },
  ];
  const { sections } = buildReport(rows);
  assert.equal(sections.cost_per_block.length, 2);
  const [b1, b2] = sections.cost_per_block;
  assert.deepEqual(
    [b1.block, b1.level, b1.completed, b1.coderUsd, b1.reviewUsd, b1.factsUsd, b1.totalUsd, b1.unknownUsdSessions],
    ['B1', 'L2', true, 0.0225, 0.01, 0, 0.0325, 1],
  );
  assert.deepEqual([b2.block, b2.completed, b2.totalUsd, b2.unknownUsdSessions], ['B2', false, 0.015, 0]);
});

test('B33: a block with session rows but NO block.close row is listed open with its running total', () => {
  const rows = [{ event: 'session', block: 'B1', role: 'coder', provider: 'anthropic', level: 'L2', tokens_in: 1000, tokens_out: 500 }];
  assert.deepEqual(
    buildReport(rows).sections.cost_per_block.map((e) => [e.block, e.completed, e.coderUsd, e.totalUsd]),
    [['B1', false, 0.0225, 0.0225]],
  );
});

/**
 * B66: two PRs over shared (B2) and distinct (B1, B3) blocks of run r1, with priced and
 * unknown-price sessions; the same block id B1 in another run r2 must not count; PR 12 is recorded
 * twice (the later row wins); a shadow outside_review row and a shadow session are excluded.
 */
function outsideReviewLedger() {
  return [
    { event: 'run.start', run: 'r1' },
    { event: 'session', run: 'r1', block: 'B1', role: 'coder', usd: 1.5 },
    { event: 'session', run: 'r1', block: 'B1', role: 'reviewer', usd: 0.25 },
    { event: 'block.close', run: 'r1', block: 'B1' },
    { event: 'session', run: 'r1', block: 'B2', role: 'coder', usd: 2 },
    { event: 'session', run: 'r1', block: 'B2', role: 'reviewer', usd: null, usd_unknown: true },
    { event: 'session', run: 'r1', block: 'B3', role: 'reviewer', usd: 0.1 },
    { event: 'session.background', run: 'r1', block: 'B3', role: 'coder' },
    { event: 'session', run: 'r2', block: 'B1', role: 'coder', usd: 9 },
    { event: 'outside_review', run: 'r1', pr: 12, rounds: 1, findings: 1, link: null, blocks: ['B1'] },
    { event: 'outside_review', run: 'r1', pr: 7, rounds: 1, findings: 0, link: null, blocks: ['B2', 'B3'] },
    { event: 'outside_review', run: 'r1', pr: 12, rounds: 3, findings: 7, link: 'https://github.com/o/r/pull/12', blocks: ['B1', 'B2'] },
    { event: 'outside_review', run: 'r1', pr: 99, rounds: 5, findings: 5, link: null, blocks: ['B1'], source: 'shadow' },
    { event: 'session', run: 'r1', block: 'B1', role: 'coder', usd: 100, source: 'shadow' },
  ];
}

const EXPECTED_OUTSIDE = [
  { pr: 7, rounds: 1, findings: 0, link: null, blocks: ['B2', 'B3'], code_forge_usd: 2.1, unknown_usd_sessions: 2 },
  { pr: 12, rounds: 3, findings: 7, link: 'https://github.com/o/r/pull/12', blocks: ['B1', 'B2'], code_forge_usd: 3.75, unknown_usd_sessions: 1 },
];

test('B66 (2): outside_review — per PR sorted ascending, the latest row per PR, the cost of its blocks in its run (unknown-price sessions count 0 and are counted), shadow rows excluded', () => {
  const { sections } = buildReport(outsideReviewLedger());
  assert.deepEqual(sections.outside_review, EXPECTED_OUTSIDE);
  // the PR dollars are the cost_per_block totals of the same blocks
  const total = (/** @type {string} */ block) => sections.cost_per_block.find((e) => e.run === 'r1' && e.block === block).totalUsd;
  assert.deepEqual([total('B1'), total('B2'), total('B3')], [1.75, 2, 0.1]);
  // and the PR unknown counts are the cost_per_block unknown-session counts of the same blocks
  const unknown = (/** @type {string} */ block) => sections.cost_per_block.find((e) => e.run === 'r1' && e.block === block).unknownUsdSessions;
  assert.deepEqual([unknown('B1'), unknown('B2'), unknown('B3')], [0, 1, 1]);
});

test('B66 (2): with no outside_review rows the section is an empty array; a block-less PR costs 0', () => {
  assert.deepEqual(buildReport([]).sections.outside_review, []);
  assert.deepEqual(buildReport(outsideReviewLedger().filter((r) => r.event !== 'outside_review')).sections.outside_review, []);
  const rows = [{ event: 'session', run: 'r1', block: 'B1', role: 'coder', usd: 1, usd_unknown: false }, { event: 'outside_review', run: 'r1', pr: 3, rounds: 2, findings: 1, link: null, blocks: [] }];
  assert.deepEqual(buildReport(rows).sections.outside_review, [{ pr: 3, rounds: 2, findings: 1, link: null, blocks: [], code_forge_usd: 0, unknown_usd_sessions: 0 }]);
});

test('B66 (2): only shadow outside_review rows give an empty section', () => {
  const rows = [{ event: 'outside_review', run: 'r1', pr: 1, rounds: 1, findings: 1, link: null, blocks: [], source: 'shadow' }];
  assert.deepEqual(buildReport(rows).sections.outside_review, []);
});

test('B66 (2): an outside_review row whose rounds or findings are not 0-or-more safe integers is skipped', () => {
  const bad = (/** @type {any} */ rounds, /** @type {any} */ findings) => ({ event: 'outside_review', run: 'r1', pr: 3, rounds, findings, link: null, blocks: [] });
  const rows = [bad(-1, 0), bad(0, -1), bad(1.5, 0), bad(0, '2'), bad(null, 0), bad(0, 2 ** 53), bad(2, 4)];
  assert.deepEqual(buildReport(rows).sections.outside_review, [
    { pr: 3, rounds: 2, findings: 4, link: null, blocks: [], code_forge_usd: 0, unknown_usd_sessions: 0 },
  ]);
  // a bad row after a good one does not replace it
  assert.deepEqual(buildReport([bad(2, 4), bad(-1, 0)]).sections.outside_review.map((/** @type {any} */ e) => e.rounds), [2]);
  // a row with no string run never matches the null-run cost entries: 0 dollars, 0 unknown
  const runless = [
    { event: 'session', block: 'B1', role: 'coder', usd: 5 },
    { event: 'session', block: 'B1', role: 'reviewer', usd: null, usd_unknown: true },
    { event: 'outside_review', pr: 4, rounds: 1, findings: 1, link: null, blocks: ['B1'] },
  ];
  const { sections } = buildReport(runless);
  assert.deepEqual(
    sections.cost_per_block.map((/** @type {any} */ e) => [e.run, e.block, e.totalUsd, e.unknownUsdSessions]),
    [[null, 'B1', 5, 1]],
  );
  assert.deepEqual(sections.outside_review, [{ pr: 4, rounds: 1, findings: 1, link: null, blocks: ['B1'], code_forge_usd: 0, unknown_usd_sessions: 0 }]);
});

test('B124b clause 3: SECTION_NAMES has 15 sections, preserving the original 14 in order', () => {
  assert.deepEqual(
    [...SECTION_NAMES],
    [
      'cost_per_block',
      'lane_distribution',
      'escalations',
      's1_calls_per_block',
      's2_rate',
      'review_budget',
      'review_depth_and_findings',
      'fix_rounds',
      'review_unavailable',
      'proof_time',
      'forecast_vs_actual_lines',
      'blocks_stopped_at_l3',
      'run_stop_counts',
      'outside_review',
      'agent_waivers',
    ],
  );
});

test('B124b report: every agent waiver is audited in ledger order with scoped cancellation and later waivers retained', () => {
  const waiver = { event: 'review.waived', run: 'r1', block: 'B1', file: 'a.mjs', finding: 'W1', severity: 'warning', reason: 'bounded', verdict: 'agree', by: 'agent' };
  const undo = { event: 'review.unwaived', run: 'r1', block: 'B1', file: 'a.mjs', finding: 'W1', by: 'human' };
  const rows = [waiver, { ...waiver }, { ...waiver, by: 'human' }, { ...waiver, run: 'r2' }, { ...waiver, block: 'B2' }, { ...waiver, file: 'b.mjs' }, { ...waiver, finding: 'W2' }, undo, { ...waiver }, { ...waiver, source: 'shadow' }];
  const entry = (/** @type {Record<string, any>} */ { block, file, finding, severity, reason, verdict }, unwaived) => ({ block, file, finding, severity, reason, verdict, unwaived });
  assert.deepEqual(buildReport(rows).sections.agent_waivers, [entry(waiver, true), entry(waiver, true), ...rows.slice(3, 7).map((r) => entry(r, false)), entry(waiver, false)]);
  assert.deepEqual(buildReport([]).sections.agent_waivers, []);
  const { agent_waivers, ...before } = buildReport([]).sections;
  const { agent_waivers: audited, ...after } = buildReport(rows).sections;
  assert.deepEqual(after, before, 'all original sections are unchanged by waiver audit rows');
});

/**
 * The real CLI under a temp HOME whose ledger for `slug` holds `rows`.
 * @param {string} name @param {Array<Record<string, any>>} rows @param {(slug: string, dir: string) => string[]} argsFor
 */
async function reportCli(name, rows, argsFor) {
  const dir = path.join(TMP_PARENT, name);
  const home = path.join(dir, 'home');
  await mkdir(path.join(home, '.code-forge', 'ledger'), { recursive: true });
  const slug = `b66-${name}`;
  await writeFile(path.join(home, '.code-forge', 'ledger', `${slug}.jsonl`), rows.map((r) => `${JSON.stringify(r)}\n`).join(''));
  return new Promise((resolve) => {
    execFile(process.execPath, [BIN, 'report', ...argsFor(slug, dir)], { cwd: dir, env: { ...process.env, HOME: home, CODE_FORGE_NO_ERROR_LOG: '1' } }, (error, stdout, stderr) =>
      resolve({ code: error ? (typeof error.code === 'number' ? error.code : -1) : 0, stdout, stderr, dir }),
    );
  });
}

test('B66 (3): report --json carries outside_review; --export writes outside_review.json with the same data; both carry [] with no rows', async () => {
  const json = await reportCli('json', outsideReviewLedger(), (slug) => ['--slug', slug, '--json']);
  assert.equal(json.code, 0, json.stderr);
  const parsed = JSON.parse(json.stdout);
  assert.deepEqual(Object.keys(parsed), [...SECTION_NAMES]);
  assert.deepEqual(parsed.outside_review, EXPECTED_OUTSIDE);

  const exported = await reportCli('export', outsideReviewLedger(), (slug, dir) => ['--slug', slug, '--export', path.join(dir, 'out')]);
  assert.deepEqual([exported.code, exported.stdout], [0, `exported 15 section file(s) to ${path.join(exported.dir, 'out')}\n`], exported.stderr);
  assert.equal((await readdir(path.join(exported.dir, 'out'))).length, 15);
  assert.deepEqual(JSON.parse(await readFile(path.join(exported.dir, 'out', 'outside_review.json'), 'utf8')), EXPECTED_OUTSIDE);

  const none = [{ event: 'run.start', run: 'r1' }];
  const emptyJson = await reportCli('empty-json', none, (slug) => ['--slug', slug, '--json']);
  assert.equal(emptyJson.code, 0, emptyJson.stderr);
  assert.deepEqual(JSON.parse(emptyJson.stdout).outside_review, []);
  const emptyExport = await reportCli('empty-export', none, (slug, dir) => ['--slug', slug, '--export', path.join(dir, 'out')]);
  assert.equal(emptyExport.code, 0, emptyExport.stderr);
  assert.equal(await readFile(path.join(emptyExport.dir, 'out', 'outside_review.json'), 'utf8'), '[]\n');
});

test('B135: report refuses an unknown slug and still renders an existing empty ledger', async () => {
  const absent = await reportCli('missing', [], () => ['--slug', 'absent']);
  assert.deepEqual([absent.code, absent.stdout, absent.stderr], [1, '', 'report: no ledger for slug "absent"; known slugs: b66-missing\n']);
  const empty = await reportCli('empty-ledger', [], (slug) => ['--slug', slug, '--json']);
  assert.deepEqual([empty.code, empty.stderr], [0, '']);
  assert.deepEqual(JSON.parse(empty.stdout), buildReport([]).sections);
});

test('B66 (2): the text report prints outside_review after run_stop_counts — one line per PR, or "outside review: none"', async () => {
  const res = await reportCli('text', outsideReviewLedger(), (slug) => ['--slug', slug]);
  assert.equal(res.code, 0, res.stderr);
  const afterStops = res.stdout.split('== run_stop_counts ==\n')[1];
  const section = afterStops.slice(afterStops.indexOf('== outside_review ==\n'));
  // the header and its lines exactly: everything from the header up to the spend lines
  assert.equal(
    section.slice(0, section.indexOf('== agent_waivers ==')),
    '== outside_review ==\n' +
      'PR #7 · 1 outside round · 0 findings · code-forge $2.10 (+2 unknown) · B2, B3\n' +
      'PR #12 · 3 outside rounds · 7 findings · code-forge $3.75 (+1 unknown) · B1, B2\n',
  );
  assert.equal(res.stdout.match(/^== /gm)?.length, 15);

  const none = await reportCli('text-none', [{ event: 'run.start', run: 'r1' }], (slug) => ['--slug', slug]);
  assert.equal(none.code, 0, none.stderr);
  assert.ok(none.stdout.includes('== run_stop_counts ==\n{\n  "overspend": 0,\n  "l3Fallback": 0,\n  "noEngineStops": 0\n}\n== outside_review ==\noutside review: none\n== agent_waivers ==\n[]\nspend per run'), none.stdout);
  const noneSection = none.stdout.slice(none.stdout.indexOf('== outside_review ==\n'));
  assert.equal(noneSection.slice(0, noneSection.indexOf('== agent_waivers ==')), '== outside_review ==\noutside review: none\n');

  // a fully priced PR prints no "(+N unknown)"; a PR with no blocks prints $0.00 and "-"
  const pricedRows = [
    ...outsideReviewLedger().filter((r) => r.event !== 'outside_review'),
    { event: 'outside_review', run: 'r1', pr: 20, rounds: 2, findings: 1, link: null, blocks: ['B1'] },
    { event: 'outside_review', run: 'r1', pr: 21, rounds: 0, findings: 0, link: null, blocks: [] },
  ];
  const priced = await reportCli('text-priced', pricedRows, (slug) => ['--slug', slug]);
  assert.equal(priced.code, 0, priced.stderr);
  const pricedSection = priced.stdout.slice(priced.stdout.indexOf('== outside_review ==\n'));
  assert.equal(
    pricedSection.slice(0, pricedSection.indexOf('== agent_waivers ==')),
    '== outside_review ==\n' +
      'PR #20 · 2 outside rounds · 1 finding · code-forge $1.75 · B1\n' +
      'PR #21 · 0 outside rounds · 0 findings · code-forge $0.00 · -\n',
  );
});
