/**
 * `code-forge report` (plan §6.2, R4): builds the 13 report sections from a run's ledger rows.
 * Pure data, no I/O — terminal rendering + `--export` live in `src/cli/report.mjs`.
 *
 * Shadow rows (`row.source === 'shadow'`) are excluded everywhere here (plan §6.3: shadow-sampled
 * answers are "never acted on" — must not move a single aggregate).
 *
 * Row conventions read here: `dispatch` {block,level,lane,lines}; `block.close`
 * {block,status,lines_actual} — ANY `block.close` row marks the block completed (a stop is
 * `block.stop`; B19); `review.plan` {block,tier,depth_unconstrained,depth_chosen,
 * degrade_step}; `session` {block,role,provider,level,tokens_in,tokens_out,usd,usd_unknown?} — one row per
 * coder/reviewer/judge/s2/author/facts spend (plus `ledger add coder` manual rows and
 * `session.background` rows, whose price is unknown); its dollars come from `./spend.mjs`
 * (`usd`, else `cost_usd`, else ESTIMATED from its tokens × `./prices.mjs`); `review.done` {block,file,context_mode,findings_by_severity,round}; `decision`
 * {block,question,source}; `outcome` {block,missed_after_degrade}; `escalation` {block,trigger};
 * `review.unavailable` {block,reason}; `proof` {block,duration_ms}; `run.stop` {reason}.
 */

import { isSpendRow, rowUsd, spendByRun, sumSpend } from './spend.mjs';

const ROLE_BUCKET = Object.freeze({
  coder: 'coderUsd',
  author: 'coderUsd',
  reviewer: 'reviewUsd',
  judge: 'reviewUsd',
  s2: 's1s2Usd',
  // B46: the autopilot delegate answers one owner-level question per session, like S2
  delegate: 's1s2Usd',
  facts: 'factsUsd',
});

/** @param {any[]} rows @param {(row: any) => string|undefined} keyOf */
function countBy(rows, keyOf) {
  /** @type {Record<string, number>} */
  const out = {};
  for (const row of rows) {
    const key = keyOf(row);
    if (key !== undefined) out[key] = (out[key] ?? 0) + 1;
  }
  return out;
}

/**
 * A session row's dollars (B33: its `usd`; null when `usd_unknown`; older rows: `cost_usd`, else
 * tokens × the static price table); null when the price is unknown.
 * @param {Record<string, any>} row @returns {number | null}
 */
export function sessionCostUsd(row) {
  return rowUsd(row);
}

/**
 * B33: spend per run (running totals, open blocks included) and over the whole ledger — kept
 * beside the 13 sections, not as a 14th (`SECTION_NAMES` stays pinned at 13).
 * @param {Array<Record<string, any>>} rows
 * @returns {{runs: Array<{run: string | null, usd: number, unknown: number, sessions: number}>, total: {usd: number, unknown: number, sessions: number}}}
 */
export function buildSpend(rows) {
  const acted = rows.filter((r) => r.source !== 'shadow');
  return { runs: spendByRun(acted), total: sumSpend(acted) };
}

/**
 * Cost per block — completed AND still open (B33: a running total while the block is open):
 * review $ / coder $ / S1+S2 $ / facts $ / proof ms, `completed` (any `block.close` row) and
 * `unknownUsdSessions` (spend rows whose price is unknown, counted 0 in the totals). Grouped by
 * run + block, so a block id reused in a second run is its own entry; a row without a run joins
 * the entry of its block when that block id appears in exactly one run (else a `run: null` entry); shadow rows are dropped here too, exactly as `buildSpend` does. An entry is listed
 * once it has a spend row or a `block.close` row.
 */
function costPerBlock(rows) {
  /** @type {Map<string, any>} */
  const byBlock = new Map();
  /** @type {Set<string>} run+block keys with at least one spend row */
  const spent = new Set();
  // never split one block: a row without a run joins its block's entry when that block id appears
  // in exactly one run; only a block seen in several runs leaves such a row in a `run: null` entry
  /** @type {Map<string, Set<string>>} */
  const runsOfBlock = new Map();
  for (const row of rows) {
    if (!row.block || row.source === 'shadow' || typeof row.run !== 'string') continue;
    if (!runsOfBlock.has(row.block)) runsOfBlock.set(row.block, new Set());
    /** @type {Set<string>} */ (runsOfBlock.get(row.block)).add(row.run);
  }
  /** @param {Record<string, any>} row @returns {string | null} */
  const runOf = (row) => {
    if (typeof row.run === 'string') return row.run;
    const runs = runsOfBlock.get(row.block);
    return runs && runs.size === 1 ? [...runs][0] : null;
  };
  for (const row of rows) {
    if (!row.block || row.source === 'shadow') continue;
    const run = runOf(row);
    const key = `${run ?? ''}\u0000${row.block}`;
    if (!byBlock.has(key)) {
      byBlock.set(key, {
        run,
        block: row.block,
        level: null,
        lane: null,
        completed: false,
        reviewUsd: 0,
        coderUsd: 0,
        s1s2Usd: 0,
        factsUsd: 0,
        otherUsd: 0,
        proofTimeMs: 0,
        totalUsd: 0,
        unknownUsdSessions: 0,
      });
    }
    const e = byBlock.get(key);
    if (row.event === 'dispatch') {
      e.level = row.level ?? e.level;
      e.lane = row.lane ?? e.lane;
    }
    if (row.event === 'block.close') e.completed = true;
    if (isSpendRow(row)) {
      spent.add(key);
      const cost = sessionCostUsd(row);
      if (cost === null) {
        e.unknownUsdSessions += 1;
      } else {
        // An unrecognized role still spent real money — bucket it as `otherUsd` instead of
        // silently dropping it from every total (fix round 1: an unknown role must not make
        // totalUsd under-report actual spend).
        const bucket = ROLE_BUCKET[row.role] ?? 'otherUsd';
        e[bucket] = (e[bucket] ?? 0) + cost;
        e.totalUsd += cost;
      }
    }
    if (row.event === 'proof' && typeof row.duration_ms === 'number') e.proofTimeMs += row.duration_ms;
  }
  const out = [...byBlock.entries()].filter(([key, e]) => e.completed || spent.has(key)).map(([, e]) => e);
  for (const e of out) {
    for (const k of ['reviewUsd', 'coderUsd', 's1s2Usd', 'factsUsd', 'otherUsd', 'totalUsd']) e[k] = Math.round(e[k] * 10000) / 10000;
  }
  return out;
}

const laneDistribution = (rows) =>
  rows.filter((r) => r.event === 'dispatch').map((r) => ({ block: r.block, lanePicked: r.lane ?? null, levelOpened: r.level ?? null }));

const escalationsPerBlock = (rows) => rows.filter((r) => r.event === 'escalation').map((r) => ({ block: r.block, trigger: r.trigger }));

const s1CallsPerBlock = (rows) =>
  countBy(
    rows.filter((r) => r.event === 'decision' && (r.source === 'jev' || r.source === 'rules')),
    (r) => r.block,
  );

function s2RateAndOverrule(rows) {
  const s2Rows = rows.filter((r) => r.event === 'decision' && (r.source === 's2' || r.source === 's2-fallback'));
  return {
    calls: countBy(s2Rows, (r) => r.question),
    overrules: countBy(
      s2Rows.filter((r) => r.overrule === true),
      (r) => r.question,
    ),
  };
}

/**
 * Review budget table (§4.10, R4 dataset): degrade count, `missed_after_degrade`, per-step plan
 * counts. A file only counts as "degraded" when BOTH depth fields are present and differ — a row
 * missing one of them is incomplete data, not evidence of degradation (`undefined !== 'full'` is
 * true, which would otherwise silently count it). `missedAfterDegrade` counts DISTINCT
 * `block::file` pairs that are BOTH flagged `missed_after_degrade: true` AND actually in the
 * degraded set — this keeps a duplicate outcome row (a re-run `--scan-git`) or a stray flag on a
 * file that was never degraded from inflating the count.
 */
function reviewBudgetTable(rows) {
  const planRows = rows.filter((r) => r.event === 'review.plan');
  const degradedRows = planRows.filter(
    (r) => r.depth_unconstrained !== undefined && r.depth_chosen !== undefined && r.depth_unconstrained !== r.depth_chosen,
  );
  const degradedKeys = new Set(degradedRows.map((r) => `${r.block}::${r.file}`));
  const missedKeys = new Set(
    rows
      .filter((r) => r.event === 'outcome' && r.missed_after_degrade === true && degradedKeys.has(`${r.block}::${r.file}`))
      .map((r) => `${r.block}::${r.file}`),
  );
  return {
    filesPlanned: planRows.length,
    degradedCount: degradedRows.length,
    missedAfterDegrade: missedKeys.size,
    byDegradeStep: countBy(planRows, (r) => String(r.degrade_step ?? 0)),
  };
}

function reviewDepthAndFindings(rows) {
  const doneRows = rows.filter((r) => r.event === 'review.done');
  /** @type {Record<string, number>} */
  const findingsBySeverity = {};
  for (const r of doneRows) {
    for (const [sev, n] of Object.entries(r.findings_by_severity ?? {})) findingsBySeverity[sev] = (findingsBySeverity[sev] ?? 0) + n;
  }
  return { contextModeDistribution: countBy(doneRows, (r) => r.context_mode), findingsBySeverity };
}

/** Keyed by `<block>::<file>`, not `file` alone — the same path reviewed in two different blocks
 * is two independent fix-round histories, not one collapsed max. */
function fixRoundsPerFile(rows) {
  /** @type {Record<string, number>} */
  const maxRound = {};
  for (const r of rows.filter((r) => r.event === 'review.done' && r.file)) {
    const key = `${r.block}::${r.file}`;
    maxRound[key] = Math.max(maxRound[key] ?? 0, r.round ?? 0);
  }
  return maxRound;
}

const reviewUnavailableByReason = (rows) =>
  countBy(
    rows.filter((r) => r.event === 'review.unavailable'),
    (r) => r.reason,
  );

function proofTimePerBlock(rows) {
  /** @type {Record<string, number>} */
  const out = {};
  for (const r of rows.filter((r) => r.event === 'proof')) out[r.block] = (out[r.block] ?? 0) + (r.duration_ms ?? 0);
  return out;
}

function forecastVsActualLines(rows) {
  const forecasts = new Map(rows.filter((r) => r.event === 'dispatch').map((r) => [r.block, r.lines]));
  return rows
    .filter((r) => r.event === 'block.close' && typeof r.lines_actual === 'number')
    .map((r) => ({ block: r.block, forecast: forecasts.get(r.block) ?? null, actual: r.lines_actual }));
}

const blocksStoppedAtL3 = (rows) => rows.filter((r) => r.event === 'block.stop' && r.level === 'L3').map((r) => r.block);

/** Distinct `block` (falling back to `run`, then a shared bucket) values among matching rows —
 * `overspend`/`l3_fallback` can legitimately be stamped on more than one row for the SAME logical
 * event (e.g. a session row and its `run.stop`/`block.stop`), so counting rows would double-count. */
const distinctScopeCount = (rows) => new Set(rows.map((r) => r.block ?? r.run ?? 'unscoped')).size;

const runStopCounts = (rows) => ({
  overspend: distinctScopeCount(rows.filter((r) => r.overspend === true)),
  l3Fallback: distinctScopeCount(rows.filter((r) => r.l3_fallback === true)),
  noEngineStops: rows.filter((r) => r.event === 'run.stop' && r.reason === 'no_engine').length,
});

/** The exact, ordered set of section keys `buildReport` produces (plan §6.2: "13 sections") —
 * exported so tests (and `--export`) can assert against ONE source of truth instead of a copy. */
export const SECTION_NAMES = Object.freeze([
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
]);

/**
 * @param {import('./write.mjs').LedgerRow[]} rows - raw ledger rows (`./write.mjs`'s `readAllRows`).
 * @returns {{sections: Record<string, any>}} exactly the `SECTION_NAMES` sections (plan §6.2).
 */
export function buildReport(rows) {
  const acted = rows.filter((r) => r.source !== 'shadow');
  const builders = {
    cost_per_block: costPerBlock,
    lane_distribution: laneDistribution,
    escalations: escalationsPerBlock,
    s1_calls_per_block: s1CallsPerBlock,
    s2_rate: s2RateAndOverrule,
    review_budget: reviewBudgetTable,
    review_depth_and_findings: reviewDepthAndFindings,
    fix_rounds: fixRoundsPerFile,
    review_unavailable: reviewUnavailableByReason,
    proof_time: proofTimePerBlock,
    forecast_vs_actual_lines: forecastVsActualLines,
    blocks_stopped_at_l3: blocksStoppedAtL3,
    run_stop_counts: runStopCounts,
  };
  /** @type {Record<string, any>} */
  const sections = {};
  for (const name of SECTION_NAMES) sections[name] = builders[name](acted);
  return { sections };
}
