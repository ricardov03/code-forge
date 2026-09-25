import { cfgFor } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { planAndRecord, planBudget } = await import('../../src/review/budget.mjs');

/** @param {number} n @param {'light' | 'standard' | 'high'} tier @param {Record<string, any>} [extra] */
const files = (n, tier, extra = {}) => Array.from({ length: n }, (_, i) => ({ file: `src/${tier}-${i + 1}.mjs`, tier, tokens: 9000, ...extra }));

test('6 high-tier files over the 150k budget degrade exactly to step 4 (full + judge), with 6 review.plan rows', async () => {
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const result = await planAndRecord({ files: files(6, 'high'), cfg: cfgFor(), block: 'B1', writeRow: async (r) => void rows.push(r) });
  assert.deepEqual([result.forecast_unconstrained, result.forecast_chosen, result.degrade_step, result.over_budget], [204000, 86000, 4, false]);
  const plans = rows.filter((r) => r.event === 'review.plan');
  assert.equal(plans.length, 6);
  assert.deepEqual(new Set(plans.map((r) => `${r.depth_unconstrained}>${r.depth_chosen}@${r.degrade_step}`)), new Set(['dual>full+block-judge@4']));
  assert.deepEqual(rows.filter((r) => r.event === 'review.budget').map((r) => [r.budget, r.forecast_chosen, r.over_budget]), [[150000, 86000, false]]);
});

test('20 high-tier files are still over after step 4 ⇒ over_budget (never below the floor)', async () => {
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const result = await planAndRecord({ files: files(20, 'high'), cfg: cfgFor(), block: 'B1', writeRow: async (r) => void rows.push(r) });
  assert.deepEqual([result.forecast_chosen, result.degrade_step, result.over_budget], [268000, 4, true]);
  assert.equal(result.plan.filter((e) => e.depth_chosen === 'full+block-judge').length, 20);
  assert.deepEqual(rows.filter((r) => r.event === 'review.over_budget').map((r) => r.block), ['B1']);
});

test('rechecks are never degraded or batched, even when the block degrades to step 4', () => {
  const small = { tokens: 1000 };
  const result = planBudget({ files: [...files(6, 'high'), ...files(3, 'high', { ...small, recheck: true })], cfg: cfgFor() });
  assert.equal(result.degrade_step, 4);
  const rechecks = result.plan.filter((e) => e.depth_unconstrained === 'recheck');
  assert.deepEqual(rechecks.map((e) => [e.depth_chosen, e.degrade_step, e.forecast_tokens, e.batch, e.context_mode]), [
    ['recheck', 0, 5000, null, 'recheck'],
    ['recheck', 0, 5000, null, 'recheck'],
    ['recheck', 0, 5000, null, 'recheck'],
  ]);
});

test('within budget ⇒ no step; small light files batch at step 1 before anything else degrades', () => {
  const fits = planBudget({ files: files(2, 'high'), cfg: cfgFor() });
  assert.deepEqual([fits.degrade_step, fits.forecast_chosen, fits.plan.map((e) => e.depth_chosen)], [0, 68000, ['dual', 'dual']]);
  const batched = planBudget({ files: files(30, 'light', { tokens: 2000 }), cfg: cfgFor() });
  assert.deepEqual([batched.degrade_step, batched.over_budget, batched.forecast_unconstrained, batched.forecast_chosen], [1, false, 210000, 35000]);
});
