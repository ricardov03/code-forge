/**
 * `code-forge report` (plan §6.2, R4): builds the report sections from a project's ledger rows.
 * Pure data, no I/O — terminal rendering + `--export` live in `src/cli/report.mjs`.
 *
 * Shadow rows (`row.source === 'shadow'`) are excluded everywhere here (plan §6.3: shadow-sampled
 * answers are "never acted on" — must not move a single aggregate).
 *
 * Row conventions read here: `dispatch` {block,level,lane,lines}; `block.close`
 * {block,status,lines_actual} — a `block.close` row marks the block completed unless followed by
 * `block.awaiting_human_check`; `block.human_pass` completes the waiting block (a stop is
 * `block.stop`; B19); `review.plan` {block,tier,depth_unconstrained,depth_chosen,
 * degrade_step}; `session` {block,role,provider,level,tokens_in,tokens_out,usd,usd_unknown?} — one row per
 * coder/reviewer/judge/s2/author/facts spend (plus `ledger add coder` manual rows and
 * `session.background` rows, whose price is unknown); its dollars come from `./spend.mjs`
 * (`usd`, else `cost_usd`, else ESTIMATED from its tokens × `./prices.mjs`); `review.done` {block,file,context_mode,findings_by_severity,round}; `decision`
 * {block,question,source}; `run.start` {run}; `run.mode` {run,mode,plan} (B59, `buildRunModes`); `outcome` {block,missed_after_degrade}; `escalation` {block,trigger};
 * `review.unavailable` {block,reason}; `proof` {block,duration_ms}; `run.stop` {reason};
 * `outside_review` {run,pr,rounds,findings,link,blocks} (B66, `./outside-review.mjs`).
 *
 * Signatures are not verified here: like every other section, `outside_review` reads the rows as
 * the ledger holds them (an unsigned or tampered row is counted, not dropped).
 */

import { buildOutsideReview } from './outside-review.mjs';
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
 * beside the sections, never one of them (text only, not in `SECTION_NAMES`).
 * @param {Array<Record<string, any>>} rows
 * @returns {{runs: Array<{run: string | null, usd: number, unknown: number, sessions: number}>, total: {usd: number, unknown: number, sessions: number}}}
 */
export function buildSpend(rows) {
  const acted = rows.filter((r) => r.source !== 'shadow');
  return { runs: spendByRun(acted), total: sumSpend(acted) };
}

/**
 * B59: the mode of every run with a `run.start` row, in ledger order — `brief` when the run has a
 * `run.mode {mode: brief}` row, else `plan` (a run with no `run.mode` row is a plan run). Like the
 * spend, it is terminal text only (`src/cli/report.mjs`): it is not in `--json` or `--export`, so
 * the pinned `SECTION_NAMES` sections stay exactly as they are.
 * @param {Array<Record<string, any>>} rows
 * @returns {Array<{run: string, mode: 'brief' | 'plan'}>}
 */
export function buildRunModes(rows) {
  const brief = new Set(rows.filter((r) => r.event === 'run.mode' && r.mode === 'brief' && typeof r.run === 'string').map((r) => r.run));
  const runs = [...new Set(rows.filter((r) => r.event === 'run.start' && typeof r.run === 'string').map((r) => r.run))];
  return runs.map((run) => ({ run, mode: brief.has(run) ? 'brief' : 'plan' }));
}

/** A full ISO timestamp with a timezone; reject coerced values and impossible calendar dates.
 * @param {unknown} ts @returns {string | null} */
function timestamp(ts) {
  if (typeof ts !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(ts)
    || !Number.isFinite(Date.parse(ts))) return null;
  const day = ts.slice(0, 10);
  const date = Date.parse(`${day}T00:00:00.000Z`);
  return Number.isFinite(date) && new Date(date).toISOString().slice(0, 10) === day ? ts : null;
}

/** Missing or reversed endpoints cannot establish elapsed time.
 * @param {string | null} start @param {string | null} end @returns {number | null} */
function elapsed(start, end) {
  if (start === null || end === null) return null;
  const ms = Date.parse(end) - Date.parse(start);
  return ms >= 0 ? ms : null;
}

/**
 * Cost per block — completed AND still open (B33: a running total while the block is open):
 * review $ / coder $ / S1+S2 $ / facts $ / proof ms, `completed` (code close with no pending human check) and
 * `unknownUsdSessions` (spend rows whose price is unknown, counted 0 in the totals). Grouped by
 * run + block, so a block id reused in a second run is its own entry; a row without a run joins
 * the entry of its block when that block id appears in exactly one run (else a `run: null` entry); shadow rows are dropped here too, exactly as `buildSpend` does. An entry is listed
 * once it has a spend row or a `block.close` row.
 */
function costPerBlock(rows) {
  /** @type {Map<string, any>} */
  const byBlock = new Map();
  /** @type {Map<string, {firstDispatch: string | null, completion: string | null, awaiting: string | null, humanWait: number | null}>} */
  const times = new Map();
  /** @type {Set<string>} run+block keys with at least one spend row */
  const spent = new Set();
  const closedKeys = new Set();
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
        wallMs: null,
        humanWaitMs: null,
        totalUsd: 0,
        unknownUsdSessions: 0,
      });
      times.set(key, { firstDispatch: null, completion: null, awaiting: null, humanWait: null });
    }
    const e = byBlock.get(key);
    if (row.event === 'dispatch') {
      e.level = row.level ?? e.level;
      e.lane = row.lane ?? e.lane;
      e.completed = false;
    }
    if (row.event === 'block.close') { e.completed = true; closedKeys.add(key); }
    if (row.event === 'block.awaiting_human_check') e.completed = false;
    if (row.event === 'block.human_pass') e.completed = true;
    if (row.event === 'block.stop') e.completed = false;
    // Invalid timestamps have no effect on time; lifecycle and spend still count normally.
    const ts = timestamp(row.ts);
    if (ts !== null) {
      const time = times.get(key);
      if (row.event === 'dispatch') {
        time.firstDispatch ??= ts;
        time.completion = null;
        time.awaiting = null;
        time.humanWait = null;
      }
      if (row.event === 'block.close') {
        time.completion = ts;
        time.humanWait = null;
      }
      if (row.event === 'block.awaiting_human_check') {
        time.awaiting = ts;
        time.completion = null;
        time.humanWait = null;
      }
      if (row.event === 'block.human_pass') {
        time.completion = ts;
        time.humanWait = elapsed(time.awaiting, ts);
      }
    }
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
  const out = [...byBlock.entries()].filter(([key, e]) => e.completed || spent.has(key) || closedKeys.has(key)).map(([key, e]) => {
    const time = times.get(key);
    if (e.completed) {
      e.wallMs = elapsed(time.firstDispatch, time.completion);
      e.humanWaitMs = time.humanWait;
    }
    return e;
  });
  for (const e of out) {
    for (const k of ['reviewUsd', 'coderUsd', 's1s2Usd', 'factsUsd', 'otherUsd', 'totalUsd']) e[k] = Math.round(e[k] * 10000) / 10000;
  }
  return out;
}

/** Per-run wall time, spend and distinct block counts, plus the ledger/site total.
 * Rows and costs have already had shadow rows removed. Untimed runs contribute 0 to total wallMs.
 * @param {Array<Record<string, any>>} rows @param {any[]} costs */
function runTime(rows, costs) {
  /** @type {Map<string | null, Array<Record<string, any>>>} */
  const groups = new Map();
  for (const row of rows) {
    const run = typeof row.run === 'string' ? row.run : null;
    if (!groups.has(run)) groups.set(run, []);
    groups.get(run).push(row);
  }
  const spend = new Map(spendByRun(rows).map((r) => [r.run, r.usd]));
  const runs = [...groups].map(([run, list]) => {
    const timed = list.filter((r) => timestamp(r.ts) !== null);
    const started = timed.find((r) => r.event === 'run.start')?.ts ?? timed[0]?.ts ?? null;
    const ended = timed.filter((r) => r.event === 'run.end').at(-1)?.ts ?? null;
    const open = ended === null;
    return {
      run, started, ended,
      wallMs: elapsed(started, open ? timed.at(-1)?.ts ?? null : ended),
      usd: spend.get(run) ?? 0,
      blocks_closed: costs.filter((b) => b.run === run && b.completed).length,
      blocks_stopped: new Set(list.filter((r) => r.event === 'block.stop' && r.block).map((r) => r.block)).size,
      ...(open ? { open: true } : {}),
    };
  });
  return {
    runs,
    total: {
      runs: runs.length,
      wallMs: runs.reduce((sum, r) => sum + (r.wallMs ?? 0), 0),
      usd: sumSpend(rows).usd,
      blocks_closed: runs.reduce((sum, r) => sum + r.blocks_closed, 0),
    },
  };
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
  const key = (r) => JSON.stringify([r.run ?? null, r.block]);
  const gates = new Map(rows.filter((r) => r.event === 'gate.done').map((r) => [key(r), r]));
  const closed = rows.filter((r) => r.event === 'block.close' && typeof r.lines_actual === 'number');
  const closedKeys = new Set(closed.map(key));
  const repeatCount = (r) => gates.get(key(r))?.repeats?.n;
  return [
    ...closed.map((r) => ({ block: r.block, forecast: forecasts.get(r.block) ?? null, actual: r.lines_actual,
      ...(repeatCount(r) !== undefined ? { repeats: repeatCount(r) } : {}) })),
    ...[...gates.values()].filter((r) => r.repeats && !closedKeys.has(key(r)))
      .map((r) => ({ block: r.block, forecast: forecasts.get(r.block) ?? null, actual: null, repeats: r.repeats.n })),
  ];
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

/** The exact, ordered set of section keys `buildReport` produces (plan §6.2; B66 appended
 * `outside_review`, B124b `agent_waivers`, B147 `run_time` — append new sections, never reorder) —
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
  'outside_review',
  'agent_waivers',
  'run_time',
]);

/**
 * @param {import('./write.mjs').LedgerRow[]} rows - raw ledger rows (`./write.mjs`'s `readAllRows`).
 * @returns {{sections: Record<string, any>}} exactly the `SECTION_NAMES` sections (plan §6.2).
 */
export function buildReport(rows) {
  const acted = rows.filter((r) => r.source !== 'shadow');
  // computed once: `cost_per_block` and `outside_review` share the same entries
  const costs = costPerBlock(acted);
  const builders = {
    cost_per_block: () => costs,
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
    // B66: per PR, the outside review rounds next to the cost of its blocks — the SAME
    // `costPerBlock` entries the `cost_per_block` section shows
    outside_review: (/** @type {any[]} */ r) => buildOutsideReview(r, costs),
    agent_waivers: (/** @type {any[]} */ r) => r.flatMap((row, i) => row.event === 'review.waived' && row.by === 'agent'
      ? [{ block: row.block, file: row.file, finding: row.finding, severity: row.severity, reason: row.reason, verdict: row.verdict,
        unwaived: r.slice(i + 1).some((undo) => undo.event === 'review.unwaived' && undo.by === 'human'
          && undo.run === row.run && undo.block === row.block && undo.file === row.file && undo.finding === row.finding) }]
      : []),
    run_time: (/** @type {any[]} */ r) => runTime(r, costs),
  };
  /** @type {Record<string, any>} */
  const sections = {};
  for (const name of SECTION_NAMES) sections[name] = builders[name](acted);
  // Preserve the existing report shape for projects that have no human-check blocks.
  if (acted.some((r) => r.event === 'block.awaiting_human_check')) {
    const waiting = new Map();
    for (const row of acted) {
      const key = `${row.run ?? ''}\u0000${row.block}`;
      if (row.event === 'block.awaiting_human_check') waiting.set(key, row.block);
      if (['block.human_pass', 'block.stop', 'dispatch'].includes(row.event)) waiting.delete(key);
    }
    sections.awaiting_human_check = [...new Set(waiting.values())].sort();
  }
  return { sections };
}
