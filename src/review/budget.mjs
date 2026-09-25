/**
 * Review budget per block (plan §4.10, R4, O19; block B12b) — the middle point.
 *
 * Budget = `review.block_budget_tokens` (150 000) + whatever the orchestrator raised for the run
 * (`raised`). The forecast is the 0.1 static table (all `estimated`, [A31] for the recheck):
 *   quick 7k · full 13k · A+B+judge 34k (26k for the two lenses + 8k judge) · consensus 30k ·
 *   recheck 5k.
 * Unconstrained depth per file: tier `light` ⇒ quick, `standard` ⇒ full, `high` ⇒ A+B+judge
 * (`consensus` for every file under `review.multimodel: true`).
 *
 * Degrade order when the forecast exceeds the budget — one step at a time, cheapest first,
 * re-forecasting after each, stopping at the first step that fits:
 *   1. batch small files of the same tier into one packet (≤ `full_in`; one row per file;
 *      `review.batch_small_files: false` skips it) — a file is small when its estimated packet
 *      is ≤ `full_in / 3`;
 *   2. high tier: the judge runs once per BLOCK instead of per file (lens A + B still per file);
 *   3. standard band: `full` ⇒ `quick`;
 *   4. high tier: `A + B + judge` ⇒ one `full` L2 + judge — the FLOOR for the high tier
 *      (consensus floor = the full consensus: steps 2–4 never touch it);
 *   (5. light: `quick` stays `quick` — a no-op, never counted.)
 * Still over after step 4 ⇒ `over_budget`: the orchestrator raises the budget once per run or
 * splits the block. Never silently, never below the floors. RECHECKS ARE NEVER DEGRADED: a
 * round ≥ 2 packet is already the minimum (§4.11); they are never batched either.
 *
 * Rows (R4 dataset): per file `review.plan {file, tier, depth_unconstrained, depth_chosen,
 * degrade_step, forecast_tokens, context_mode}` and per block `review.budget {budget,
 * forecast_unconstrained, forecast_chosen, actual, raised, over_budget}`.
 */

import { budgetFor } from './packet.mjs';

export const SESSION_COST = Object.freeze({ quick: 7000, full: 13000, dual: 34000, consensus: 30000, recheck: 5000 });

/** The judge's share of `dual` (A + B = 2 × 13k; the judge reads two reports). */
export const JUDGE_COST = SESSION_COST.dual - 2 * SESSION_COST.full;

export const DEFAULT_BLOCK_BUDGET = 150000;

/** Per-file cost of each depth when the judge is billed separately (per block). */
const PER_FILE = Object.freeze({
  quick: SESSION_COST.quick,
  full: SESSION_COST.full,
  dual: SESSION_COST.dual,
  'dual-block-judge': 2 * SESSION_COST.full,
  'full+judge': SESSION_COST.full + JUDGE_COST,
  'full+block-judge': SESSION_COST.full,
  consensus: SESSION_COST.consensus,
  recheck: SESSION_COST.recheck,
});

/**
 * Tier per file (§7.1): `high` when risk ≥ 2, a high path, or security-sensitive; `standard` for
 * 1 ≤ risk < 2; else `light`.
 * @param {{risk: number, highPath?: boolean, securitySensitive?: boolean}} opts
 * @returns {'light' | 'standard' | 'high'}
 */
export function tierOf({ risk, highPath = false, securitySensitive = false }) {
  if (highPath || securitySensitive || risk >= 2) return 'high';
  return risk >= 1 ? 'standard' : 'light';
}

/**
 * @typedef {object} BudgetFile
 * @property {string} file
 * @property {'light' | 'standard' | 'high'} tier
 * @property {number} [tokens] - the estimated packet size (for batching); absent ⇒ not small.
 * @property {boolean} [recheck] - a round ≥ 2 packet: forecast at `recheck`, never degraded.
 */

/**
 * @typedef {object} PlanEntry
 * @property {string} file @property {string} tier
 * @property {string} depth_unconstrained @property {string} depth_chosen
 * @property {number} degrade_step @property {number} forecast_tokens
 * @property {string | null} context_mode @property {number | null} batch
 */

/**
 * @typedef {object} BudgetPlan
 * @property {number} budget @property {number} raised
 * @property {number} forecast_unconstrained @property {number} forecast_chosen
 * @property {number} degrade_step - the last step applied (0 = none).
 * @property {boolean} over_budget
 * @property {boolean} judge_per_block
 * @property {PlanEntry[]} plan
 */

/** @param {'light' | 'standard' | 'high'} tier @param {boolean} consensus */
const unconstrainedDepth = (tier, consensus) => (consensus ? 'consensus' : tier === 'high' ? 'dual' : tier === 'standard' ? 'full' : 'quick');

/**
 * Forecast the plan: per-file costs, batches billed once per batch, the block judge once.
 * @param {PlanEntry[]} entries @param {boolean} judgePerBlock @returns {number}
 */
function forecast(entries, judgePerBlock) {
  let total = 0;
  /** @type {Map<number, number>} */
  const batches = new Map();
  for (const e of entries) {
    const cost = PER_FILE[/** @type {keyof typeof PER_FILE} */ (e.depth_chosen)];
    if (e.batch === null) {
      e.forecast_tokens = cost;
      total += cost;
    } else {
      batches.set(e.batch, Math.max(batches.get(e.batch) ?? 0, cost));
    }
  }
  for (const [id, cost] of batches) {
    const members = entries.filter((e) => e.batch === id);
    for (const e of members) e.forecast_tokens = Math.ceil(cost / members.length);
    total += cost;
  }
  const blockJudge = judgePerBlock && entries.some((e) => e.depth_chosen === 'dual-block-judge' || e.depth_chosen === 'full+block-judge');
  return total + (blockJudge ? JUDGE_COST : 0);
}

/**
 * Plan the block's review depth under its budget.
 * @param {{files: BudgetFile[], cfg?: Record<string, any>, raised?: number}} opts
 * @returns {BudgetPlan}
 */
export function planBudget({ files, cfg = {}, raised = 0 }) {
  if (!Array.isArray(files)) throw new TypeError('planBudget: files must be an array');
  const base = Number.isInteger(cfg?.review?.block_budget_tokens) && cfg.review.block_budget_tokens > 0 ? cfg.review.block_budget_tokens : DEFAULT_BLOCK_BUDGET;
  if (!Number.isInteger(raised) || raised < 0) throw new TypeError('planBudget: raised must be an integer >= 0');
  const budget = base + raised;
  const consensus = cfg?.review?.multimodel === true;
  const fullIn = budgetFor(cfg, 'full_in');

  /** @type {PlanEntry[]} */
  const plan = files.map((f) => {
    const depth = f.recheck === true ? 'recheck' : unconstrainedDepth(f.tier, consensus);
    return { file: f.file, tier: f.tier, depth_unconstrained: depth, depth_chosen: depth, degrade_step: 0, forecast_tokens: 0, context_mode: f.recheck === true ? 'recheck' : null, batch: null };
  });
  let judgePerBlock = false;
  const unconstrained = forecast(plan, false);
  let chosen = unconstrained;
  let step = 0;

  /** @type {Array<() => void>} */
  const steps = [
    // 1. batch small files of the same tier
    () => {
      if (cfg?.review?.batch_small_files === false) return;
      let next = 0;
      for (const tier of ['light', 'standard', 'high']) {
        let load = 0;
        let current = -1;
        files.forEach((f, i) => {
          const e = plan[i];
          if (f.recheck === true || f.tier !== tier || !(typeof f.tokens === 'number' && f.tokens <= fullIn / 3)) return;
          if (current < 0 || load + f.tokens > fullIn) {
            current = next++;
            load = 0;
          }
          load += f.tokens;
          e.batch = current;
          e.degrade_step = 1;
        });
      }
      // a "batch" of one file is no batch
      for (let id = 0; id < next; id += 1) {
        const members = plan.filter((e) => e.batch === id);
        if (members.length === 1) Object.assign(members[0], { batch: null, degrade_step: 0 });
      }
    },
    // 2. judge per block for high-tier files
    () => {
      judgePerBlock = true;
      for (const e of plan) if (e.depth_chosen === 'dual') Object.assign(e, { depth_chosen: 'dual-block-judge', degrade_step: 2 });
    },
    // 3. standard: full ⇒ quick
    () => {
      for (const e of plan) if (e.depth_chosen === 'full' && e.tier === 'standard') Object.assign(e, { depth_chosen: 'quick', degrade_step: 3 });
    },
    // 4. high: A + B (+ judge) ⇒ one full + judge — the floor
    () => {
      for (const e of plan) {
        if (e.depth_chosen === 'dual-block-judge') Object.assign(e, { depth_chosen: 'full+block-judge', degrade_step: 4 });
        else if (e.depth_chosen === 'dual') Object.assign(e, { depth_chosen: 'full+judge', degrade_step: 4 });
      }
    },
  ];
  while (chosen > budget && step < steps.length) {
    steps[step]();
    step += 1;
    chosen = forecast(plan, judgePerBlock);
  }
  return { budget, raised, forecast_unconstrained: unconstrained, forecast_chosen: chosen, degrade_step: step, over_budget: chosen > budget, judge_per_block: judgePerBlock, plan };
}

/**
 * The ledger rows of a plan: one `review.plan` per file, then one `review.budget` for the block.
 * @param {BudgetPlan} result @param {{block: string, actual?: number | null}} opts
 * @returns {Array<Record<string, any>>}
 */
export function budgetRows(result, { block, actual = null }) {
  /** @type {Array<Record<string, any>>} */
  const rows = result.plan.map((e) => ({
    event: 'review.plan',
    block,
    file: e.file,
    tier: e.tier,
    depth_unconstrained: e.depth_unconstrained,
    depth_chosen: e.depth_chosen,
    degrade_step: e.degrade_step,
    forecast_tokens: e.forecast_tokens,
    context_mode: e.context_mode,
    tokens_source: 'estimated',
  }));
  rows.push({
    event: 'review.budget',
    block,
    budget: result.budget,
    forecast_unconstrained: result.forecast_unconstrained,
    forecast_chosen: result.forecast_chosen,
    actual,
    raised: result.raised,
    over_budget: result.over_budget,
    degrade_step: result.degrade_step,
  });
  if (result.over_budget) rows.push({ event: 'review.over_budget', block, budget: result.budget, forecast_chosen: result.forecast_chosen });
  return rows;
}

/**
 * Plan and write the rows (the ledger writer signs/stamps them).
 * @param {{files: BudgetFile[], cfg?: Record<string, any>, raised?: number, block: string, writeRow: (row: Record<string, any>) => Promise<unknown>}} opts
 * @returns {Promise<BudgetPlan>}
 */
export async function planAndRecord({ files, cfg, raised, block, writeRow }) {
  const result = planBudget({ files, cfg, raised });
  for (const row of budgetRows(result, { block })) await writeRow(row);
  return result;
}
