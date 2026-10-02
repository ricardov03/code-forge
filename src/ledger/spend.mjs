/**
 * Run spend in USD (B33, plan rule R6): the one place that turns ledger rows into dollars for
 * `report`, `run status`, the `budget.usd` stop in `spawnSession`, and `ledger add coder`.
 *
 * Dollars are estimates from the static price table (`./prices.mjs`, provider × level, blended
 * in+out per 1K tokens). A row whose price is unknown carries `usd: null, usd_unknown: true` —
 * never a guess — and counts as 0 in totals while being counted in `unknown`.
 *
 * Spend rows: `event: 'session'` (one per foreground attempt, plus the manual/cloud coder rows
 * `ledger add coder` writes with `manual: true`) and `event: 'session.background'` (a detached
 * coder: its tokens are never seen, so its price is unknown). Shadow rows never count.
 */

import { logWarning } from '../util/error-log.mjs';
import { writeSafe } from '../util/redact.mjs';
import { estimateCostUsd } from './prices.mjs';

/** The ledger events that carry a session's spend. */
export const SPEND_EVENTS = Object.freeze(['session', 'session.background']);

/** Round to 4 decimals (the price table's precision). @param {number} n */
export const roundUsd = (n) => Math.round(n * 10000) / 10000;

/** A usable amount: a finite number ≥ 0 (tokens or dollars). @param {unknown} v @returns {v is number} */
export const isAmount = (v) => typeof v === 'number' && Number.isFinite(v) && v >= 0;

/**
 * Price one session from its token counts. Both counts must be finite numbers ≥ 0 — a null,
 * missing, NaN, negative or string count is unknown (never read as 0) — and the provider/level
 * must be in the price table; anything else ⇒ `{usd: null, usd_unknown: true}`.
 * @param {{provider?: unknown, level?: unknown, tokens_in?: unknown, tokens_out?: unknown}} row
 * @returns {{usd: number, usd_unknown: false} | {usd: null, usd_unknown: true}}
 */
export function priceSession({ provider, level, tokens_in, tokens_out }) {
  if (!isAmount(tokens_in) || !isAmount(tokens_out)) return { usd: null, usd_unknown: true };
  try {
    const usd = estimateCostUsd({ provider: /** @type {string} */ (provider), level: /** @type {string} */ (level), tokensIn: tokens_in, tokensOut: tokens_out });
    return isAmount(usd) ? { usd, usd_unknown: false } : { usd: null, usd_unknown: true };
  } catch {
    return { usd: null, usd_unknown: true };
  }
}

/** @param {Record<string, any>} row @returns {boolean} */
export const isSpendRow = (row) => SPEND_EVENTS.includes(row?.event) && row.source !== 'shadow';

/**
 * A spend row's dollars, or null when unknown. `usd` when the row carries the key (a non-amount —
 * null, NaN, Infinity, negative, a string — is unknown); `usd_unknown: true` is unknown; older rows
 * (before B33): `cost_usd` when present (a non-amount is unknown), else tokens × the price table.
 * @param {Record<string, any>} row @returns {number | null}
 */
export function rowUsd(row) {
  if (row.usd !== undefined) return isAmount(row.usd) ? row.usd : null;
  if (row.usd_unknown === true) return null;
  if (row.cost_usd !== undefined && row.cost_usd !== null) return isAmount(row.cost_usd) ? row.cost_usd : null;
  if (row.event === 'session.background') return null;
  return priceSession(row).usd;
}

/**
 * Total spend of the rows: `usd` (unknown rows count 0), `unknown` (rows whose price is unknown),
 * `sessions` (spend rows counted).
 * @param {Array<Record<string, any>>} rows
 * @returns {{usd: number, unknown: number, sessions: number}}
 */
export function sumSpend(rows) {
  let usd = 0;
  let unknown = 0;
  let sessions = 0;
  for (const row of rows) {
    if (!isSpendRow(row)) continue;
    sessions += 1;
    const cost = rowUsd(row);
    if (cost === null) unknown += 1;
    else usd += cost;
  }
  return { usd: roundUsd(usd), unknown, sessions };
}

/**
 * Spend of one run (rows whose `run` is `runId`).
 * @param {Array<Record<string, any>>} rows @param {string} runId
 */
export const runSpend = (rows, runId) => sumSpend(rows.filter((r) => r.run === runId));

/**
 * Spend per run, in first-seen order; rows without a run are grouped under `null`.
 * @param {Array<Record<string, any>>} rows
 * @returns {Array<{run: string | null, usd: number, unknown: number, sessions: number}>}
 */
export function spendByRun(rows) {
  /** @type {Map<string | null, Array<Record<string, any>>>} */
  const groups = new Map();
  for (const row of rows) {
    if (!isSpendRow(row)) continue;
    const key = typeof row.run === 'string' ? row.run : null;
    if (!groups.has(key)) groups.set(key, []);
    /** @type {Array<Record<string, any>>} */ (groups.get(key)).push(row);
  }
  return [...groups.entries()].map(([run, list]) => ({ run, ...sumSpend(list) }));
}

/**
 * `budget.usd` from a loaded config: a finite number > 0, else null (no budget).
 * @param {Record<string, any> | null | undefined} cfg @returns {number | null}
 */
export function budgetUsdOf(cfg) {
  const v = cfg?.budget?.usd;
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : null;
}

/** The plain refusal text (R6). @param {number} budget @param {number} spent */
export const budgetReachedMessage = (budget, spent) => `budget.usd ${budget.toFixed(2)} reached (spent ${spent.toFixed(2)}); raise budget.usd or end the run`;

/** The one-time warning text at 80 %. @param {number} budget @param {number} spent */
export const budgetWarningMessage = (budget, spent) => `budget.usd: spent ${spent.toFixed(2)} of ${budget.toFixed(2)} (≥ 80%); new sessions stop at 100%`;

/**
 * Runs this process warned about — the FALLBACK dedupe, used only when the warning row could not
 * be written (no writer, or the write failed). The primary record is the `budget.warning` row.
 */
const warnedRuns = new Set();

/**
 * The budget check run before every session spawn. Reads the run's spend from `rows` (only rows
 * whose `run` is `run` count): spent ≥ budget ⇒ `{refuse: true}` and a `budget.refused` row;
 * spent ≥ 80 % of budget ⇒ one `budget.warning` row and stderr line per run — skipped when the
 * ledger already holds a `budget.warning` row for the run.
 * @param {{budget: number, run: string, rows: Array<Record<string, any>>, block?: string | null, role?: string,
 *   writeRow?: ((row: Record<string, any>) => Promise<unknown>) | null, stderr: {write: (s: string) => unknown}}} opts
 * @returns {Promise<{refuse: boolean, spent: number, message?: string}>}
 */
export async function checkBudget({ budget, run, rows, block = null, role, writeRow = null, stderr }) {
  const { usd: spent, unknown } = runSpend(rows, run);
  const base = { run, block, ...(role ? { role } : {}), budget_usd: budget, spent_usd: spent, unknown_usd_sessions: unknown };
  if (spent >= budget) {
    const message = budgetReachedMessage(budget, spent);
    writeSafe(stderr, `code-forge: ${message}\n`);
    if (writeRow) {
      try {
        await writeRow({ event: 'budget.refused', ...base });
      } catch {
        // the refusal stands without its row
      }
    }
    return { refuse: true, spent, message };
  }
  // compared at the ledger's 4-decimal precision, so 0.24 of 0.30 is exactly 80 %
  if (spent >= roundUsd(0.8 * budget) && !rows.some((r) => r.event === 'budget.warning' && r.run === run)) {
    let recorded = false;
    if (writeRow) {
      try {
        await writeRow({ event: 'budget.warning', ...base });
        recorded = true;
      } catch {
        recorded = false; // fall back to the in-process dedupe below
      }
    }
    if (recorded || !warnedRuns.has(run)) {
      writeSafe(stderr, `code-forge: ${budgetWarningMessage(budget, spent)}\n`);
      warnedRuns.add(run); // after the write attempt, never before
      // B37: a warning line in the local error log — never the amounts (config values)
      await logWarning({ warning: 'budget_warning', message: 'budget.usd: 80% of the run budget is spent; new sessions stop at 100%' }).catch(() => null);
    }
  }
  return { refuse: false, spent };
}

/** The fail-closed refusal text: a budget is set but the run's spend cannot be read. */
export const SPEND_UNREADABLE_MESSAGE = "budget.usd is set but the run's spend cannot be read; not starting a session";
