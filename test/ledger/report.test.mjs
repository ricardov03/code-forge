import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildReport, SECTION_NAMES } from '../../src/ledger/report.mjs';

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
    // b3: spend exists but the block was never closed — must be excluded entirely
    { event: 'session', block: 'b3', role: 'coder', cost_usd: 0.3 },
  ];
  return rows;
}

test('cost per completed block matches the hand-computed total for each of 3 blocks, and only 2 blocks are reported (3 asserts + a length check)', () => {
  const { sections } = buildReport(threeBlockLedger());
  assert.equal(sections.cost_per_block.length, 2, 'b3 was never closed — it must not appear at all, not even as a zero entry');
  const byBlock = Object.fromEntries(sections.cost_per_block.map((e) => [e.block, e]));
  assert.equal(byBlock.b1.totalUsd, 1.5);
  assert.equal(byBlock.b2.totalUsd, 2.85);
  assert.equal(byBlock.b3, undefined);
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
    // B2 spent tokens but never closed: not listed
    { event: 'session', block: 'B2', role: 'coder', provider: 'anthropic', level: 'L2', tokens_in: 1000, tokens_out: 0 },
  ];
  const { sections } = buildReport(rows);
  assert.equal(sections.cost_per_block.length, 1);
  const [b1] = sections.cost_per_block;
  assert.deepEqual(
    [b1.block, b1.level, b1.completed, b1.coderUsd, b1.reviewUsd, b1.factsUsd, b1.totalUsd],
    ['B1', 'L2', true, 0.0225, 0.01, 0, 0.0325],
  );
});

test('B19: a block with session rows but NO block.close row gives an empty cost_per_block', () => {
  const rows = [{ event: 'session', block: 'B1', role: 'coder', provider: 'anthropic', level: 'L2', tokens_in: 1000, tokens_out: 500 }];
  assert.deepEqual(buildReport(rows).sections.cost_per_block, []);
});
