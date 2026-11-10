/**
 * `code-forge report [--slug <slug>] [--json] [--export <dir>]` (plan §6.2, R4). Terminal output
 * only, plus an optional `--export <dir>` that writes the same sections as JSON files. The
 * terminal output includes spend per run (B33) and the mode of each run (B59: brief or plan)
 * before the final specialist_outcomes section. Spend and mode are text only, never in
 * `--json` or `--export`. The 14th section, `outside_review` (B66), is
 * printed as text lines under its header (`outside review: none`, or one line per PR).
 */

import { readdir, stat } from 'node:fs/promises';
import { writeSafe } from '../util/redact.mjs';
import { ledgerDir, ledgerPath } from '../ledger/paths.mjs';
import { buildReport, buildRunModes, buildSpend } from '../ledger/report.mjs';
import { exportReportSections } from '../ledger/export.mjs';
import { outsideReviewLines } from '../ledger/outside-review.mjs';
import { readAllRows } from '../ledger/write.mjs';

/**
 * @param {string[]} args @param {string} flag @returns {string|undefined}
 * `undefined` (not the next token) when the flag is absent, is the LAST token, or the "next
 * token" starts with `--` — otherwise `report --slug --json` would read `--json` as the slug, and
 * `report --slug x --export` (missing a directory) would silently print instead of exporting.
 */
function flagValue(args, flag) {
  const i = args.indexOf(flag);
  if (i < 0) return undefined;
  const next = args[i + 1];
  return next !== undefined && !next.startsWith('--') ? next : undefined;
}

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}}} [deps]
 * @returns {Promise<number>}
 */
export async function runReport(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr } = deps;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);

  const slug = flagValue(args, '--slug');
  if (!slug) {
    err(USAGE);
    return 2;
  }

  // `readAllRows`/`ledgerPath` (src/ledger/paths.mjs) already refuse a slug shaped like a path
  // (anything outside `/^[a-z0-9][a-z0-9-]*$/` — including `..` and `/`) with a TypeError; the
  // try/catch below turns that, and any other I/O failure, into a clean CLI message instead of a
  // raw stack trace that skips the `writeSafe` redaction path.
  try {
    try {
      await stat(ledgerPath(slug));
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
      const entries = await readdir(ledgerDir(), { withFileTypes: true }).catch((e) => {
        if (e.code === 'ENOENT') return [];
        throw e;
      });
      const slugs = [...new Set(entries.filter((e) => e.isFile() && /^[a-z0-9][a-z0-9-]*(?:\.\d+)?\.jsonl$/.test(e.name))
        .map((e) => e.name.replace(/(?:\.\d+)?\.jsonl$/, '')))].sort();
      throw new Error(`no ledger for slug "${slug}"; known slugs: ${slugs.join(', ') || 'none'}`);
    }
    const rows = await readAllRows(slug);
    const { sections } = buildReport(rows);
    // There is no native Go report path yet. Keep pace in this existing Node module:
    // its aggregation visits the ledger once, independently of the older sections.
    sections.pace = buildPace(rows);
    /** @type {Map<string | null, Set<string>>} */
    const recovered = new Map();
    for (const row of rows) {
      if (row.event !== 'gate.done' || !Array.isArray(row.flaky)) continue;
      const run = typeof row.run === 'string' ? row.run : null;
      const files = recovered.get(run) ?? new Set();
      for (const file of row.flaky) if (typeof file === 'string') files.add(file);
      if (files.size > 0) recovered.set(run, files);
    }
    const flaky = [...recovered].map(([run, files]) => ({ run, files: [...files].sort() }));
    const reported = flaky.length ? { ...sections, flaky } : sections;

    const exportDir = flagValue(args, '--export');
    if (exportDir) {
      const paths = await exportReportSections(exportDir, reported);
      out(`exported ${paths.length} section file(s) to ${exportDir}\n`);
      return 0;
    }

    if (args.includes('--json')) {
      out(`${JSON.stringify(reported, null, 2)}\n`);
      return 0;
    }

    for (const [name, data] of Object.entries(sections)) {
      if (name === 'specialist_outcomes') continue;
      let body;
      if (name === 'cost_per_block') body = blockTimeLines(data);
      else if (name === 'run_time') body = runTimeLines(data);
      else if (name === 'outside_review') body = outsideReviewLines(data);
      else if (name === 'pace') body = paceLines(data);
      else body = `${JSON.stringify(data, null, 2)}\n`;
      out(`== ${name} ==\n${body}`);
    }
    out(spendLines(buildSpend(rows)));
    out(modeLines(buildRunModes(rows)));
    for (const entry of flaky) out(`${entry.run === null ? 'outside runs' : `run ${entry.run}`} flaky: ${entry.files.join(', ')}\n`);
    out(`== specialist_outcomes ==\n${specialistOutcomeLines(sections.specialist_outcomes)}`);
    return 0;
  } catch (err_) {
    err(`report: ${err_?.message ?? String(err_)}\n`);
    return 1;
  }
}

const PACE_COLUMNS = Object.freeze({
  openToCloseMinutes: 'open->close min', coderMinutes: 'coder min',
  reviewWaitMinutes: 'review wait min', reviewTimeouts: 'review timeouts',
  reviewUnavailable: 'unavailable answers', gateMinutes: 'gate min', gateRedCount: 'gate reds',
  landWaitMinutes: 'close->land min', claimsAfterOpen: 'claims after open', stopCount: 'stops',
  machineWaitMinutes: 'machine.wait min', deviceMinutes: 'device min',
  lockWaitMinutes: 'lock wait min', lockWaitCount: 'lock waits',
});

/** @returns {Record<string, number>} */
function emptyPace() {
  return Object.fromEntries(Object.keys(PACE_COLUMNS).map((key) => [key, 0]));
}

/** @param {unknown} value @returns {number | null} */
function paceTimestamp(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)) return null;
  const ms = Date.parse(value), day = Date.parse(`${value.slice(0, 10)}T00:00:00Z`);
  return Number.isFinite(ms) && Number.isFinite(day)
    && new Date(day).toISOString().slice(0, 10) === value.slice(0, 10) ? ms : null;
}

/** Missing, invalid and reversed endpoints contribute zero. @param {number | null} start @param {number | null} end */
function paceElapsed(start, end) {
  return start !== null && end !== null && end >= start ? end - start : 0;
}

/**
 * One pass over raw ledger rows; only the smaller aggregate maps are visited afterwards.
 * Durations accumulate as milliseconds and convert once, avoiding fractional-minute drift.
 * Open means first dispatch (or block.open); close means latest close in that lifecycle;
 * land.timing is the existing landing row. Claims count in ledger order after open.
 * Review wait comes from each result's queued_at -> started_at; unavailable/timeout
 * counts come from their diagnostic row kinds, never session/result duplicates.
 * Runless rows stay outside runs; run-only waits contribute to run/ledger totals.
 * @param {Array<Record<string, any>>} rows
 */
export function buildPace(rows) {
  /** @type {Map<string | null, {run: string | null, metrics: Record<string, number>}>} */
  const runs = new Map();
  /** @type {Map<string, {run: string | null, block: string, metrics: Record<string, number>, opened: boolean, start: number | null, close: number | null, land: number | null}>} */
  const blocks = new Map();
  /** @param {unknown} ms */
  const measured = (ms) => typeof ms === 'number' && Number.isFinite(ms) && ms >= 0 ? ms : 0;
  for (const row of rows) {
    if (row.source === 'shadow') continue;
    const run = typeof row.run === 'string' ? row.run : null;
    if (!runs.has(run)) runs.set(run, { run, metrics: emptyPace() });
    let metrics = runs.get(run).metrics;
    const event = typeof row.event === 'string' ? row.event : '';
    if (typeof row.block === 'string' && row.block.length > 0) {
      const key = JSON.stringify([run, row.block]);
      if (!blocks.has(key)) blocks.set(key, { run, block: row.block, metrics: emptyPace(), opened: false, start: null, close: null, land: null });
      const block = blocks.get(key);
      metrics = block.metrics;
      if (event === 'dispatch' || event === 'block.open') {
        block.opened = true;
        block.start ??= paceTimestamp(row.ts);
        block.close = null;
        block.land = null;
      }
      if (event === 'block.close') block.close = paceTimestamp(row.ts) ?? block.close;
      if (event === 'land.timing') block.land = paceTimestamp(row.ts) ?? block.land;
      if (event === 'block.claim' && block.opened) metrics.claimsAfterOpen++;
    }
    if (event === 'session' && row.role === 'coder') metrics.coderMinutes += measured(row.duration_ms);
    if (event === 'review.result') metrics.reviewWaitMinutes += paceElapsed(paceTimestamp(row.queued_at), paceTimestamp(row.started_at));
    if (event === 'review.session_timeout') metrics.reviewTimeouts++;
    if (event === 'review.unavailable') metrics.reviewUnavailable++;
    if (event === 'gate.done') metrics.gateMinutes += measured(row.duration_ms);
    if (event === 'gate.red') metrics.gateRedCount++;
    if (event === 'block.stop') metrics.stopCount++;
    if (event === 'machine.wait') metrics.machineWaitMinutes += measured(row.duration_ms ?? row.wait_ms);
    if (event.startsWith('device.')) metrics.deviceMinutes += measured(row.duration_ms);
    if (event === 'lock.wait') {
      metrics.lockWaitCount++;
      metrics.lockWaitMinutes += measured(row.wait_ms ?? row.duration_ms);
    }
  }
  /** @param {Record<string, number>} target @param {Record<string, number>} source */
  const add = (target, source) => {
    for (const key of Object.keys(PACE_COLUMNS)) target[key] += source[key];
  };
  /** @param {Record<string, number>} metrics */
  const minutes = (metrics) => Object.fromEntries(Object.entries(metrics)
    .map(([key, value]) => [key, key.endsWith('Minutes') ? value / 60000 : value]));
  const perBlock = [];
  for (const block of blocks.values()) {
    const { run, metrics } = block;
    metrics.openToCloseMinutes = paceElapsed(block.start, block.close);
    metrics.landWaitMinutes = paceElapsed(block.close, block.land);
    add(runs.get(run).metrics, metrics);
    perBlock.push({ run, block: block.block, ...minutes(metrics) });
  }
  const total = emptyPace();
  const perRun = [];
  for (const { run, metrics } of runs.values()) {
    add(total, metrics);
    perRun.push({ run, ...minutes(metrics) });
  }
  return { blocks: perBlock, runs: perRun, total: minutes(total) };
}

/** @param {ReturnType<typeof buildPace>} pace */
function paceLines(pace) {
  const keys = Object.keys(PACE_COLUMNS);
  /** @param {string | null} run @param {string} block @param {Record<string, any>} metrics */
  const line = (run, block, metrics) => [run ?? 'outside runs', block, ...keys.map((key) => metrics[key])].join(' | ');
  return `${[
    ['run', 'block', ...Object.values(PACE_COLUMNS)].join(' | '),
    ...pace.blocks.map((block) => line(block.run, block.block, block)),
    ...pace.runs.map((run) => line(run.run, 'total', run)),
    line('all runs', 'total', pace.total),
  ].join('\n')}\n`;
}

/** @param {{outcomes: any[], total: {count: number, cost_usd: number, minutes: number}}} data */
function specialistOutcomeLines({ outcomes, total }) {
  const cost = (/** @type {number | null} */ usd) => usd === null ? '-' : `$${usd.toFixed(4)}`;
  const lines = ['skill | step | status | cost_usd | minutes'];
  for (const r of outcomes) lines.push(`${r.skill} | ${r.step ?? '-'} | ${r.status} | ${cost(r.cost_usd)} | ${r.minutes ?? '-'}`);
  lines.push(`total (${total.count} outcomes): ${cost(total.cost_usd)} | ${total.minutes} minutes`);
  return `${lines.join('\n')}\n`;
}

/** @param {number | null} ms @returns {string} */
function duration(ms) {
  if (ms === null) return '-';
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m ${seconds % 60}s`;
}

/** @param {any[]} blocks @returns {string} */
function blockTimeLines(blocks) {
  const lines = ['run | block | USD | wall | human wait | status'];
  for (const b of blocks) {
    lines.push(`${b.run ?? 'outside runs'} | ${b.block} | $${b.totalUsd.toFixed(4)} | ${duration(b.wallMs)} | ${duration(b.humanWaitMs)} | ${b.completed ? 'completed' : b.stopped ? 'stopped' : 'open'}`);
    lines.push(`  level ${b.level ?? '-'} · lane ${b.lane ?? '-'} · coder $${b.coderUsd.toFixed(4)} · review $${b.reviewUsd.toFixed(4)} · S1+S2 $${b.s1s2Usd.toFixed(4)} · facts $${b.factsUsd.toFixed(4)} · other $${b.otherUsd.toFixed(4)} · gates ${duration(b.gateTimeMs ?? null)} · review time ${duration(b.reviewTimeMs ?? null)} · proof ${duration(b.proofTimeMs)} · ${b.unknownUsdSessions} unknown`);
  }
  return `${lines.join('\n')}\n`;
}

/** @param {{runs: any[], outside_runs?: {usd: number, rows: number}, total: {runs: number, wallMs: number, usd: number, blocks_closed: number}}} data @returns {string} */
function runTimeLines({ runs, outside_runs, total }) {
  const lines = ['run | started | ended | wall | USD | closed | stopped'];
  for (const r of runs) lines.push(`${r.run} | ${r.started ?? '-'} | ${r.open ? 'open' : r.ended ?? '-'} | ${duration(r.wallMs)} | $${r.usd.toFixed(4)} | ${r.blocks_closed} | ${r.blocks_stopped}`);
  if (outside_runs) lines.push(`outside runs: $${outside_runs.usd.toFixed(4)} (${outside_runs.rows} rows, no wall time)`);
  lines.push(`total (${total.runs} run${total.runs === 1 ? '' : 's'}): ${duration(total.wallMs)} | $${total.usd.toFixed(4)} | ${total.blocks_closed} closed`);
  return `${lines.join('\n')}\n`;
}

/**
 * B33: the running spend per run (open blocks included), after the sections. Unknown-price
 * sessions are named, never priced.
 * @param {ReturnType<typeof buildSpend>} spend @returns {string}
 */
export function spendLines({ runs, total }) {
  const unknown = (/** @type {number} */ n) => (n > 0 ? ` · ${n} with unknown price (not counted)` : '');
  const lines = ['spend per run (estimated USD, open blocks included):'];
  for (const r of runs.filter((r) => r.run !== null)) lines.push(`  run ${r.run}: $${r.usd.toFixed(4)} · ${r.sessions} session(s)${unknown(r.unknown)}`);
  const outside = runs.find((r) => r.run === null);
  if (outside) lines.push(`  outside runs: $${outside.usd.toFixed(4)} · ${outside.sessions} session(s)${unknown(outside.unknown)}`);
  lines.push(`  total: $${total.usd.toFixed(4)} · ${total.sessions} session(s)${unknown(total.unknown)}`);
  return `${lines.join('\n')}\n`;
}

/**
 * B59: the mode of each run (`brief` or `plan`), after the spend — text only, never in `--json`
 * or `--export`. Empty (no header) when the ledger has no run.
 * @param {ReturnType<typeof buildRunModes>} modes
 * @returns {string}
 */
export function modeLines(modes) {
  if (modes.length === 0) return '';
  return `${['run mode:', ...modes.map((m) => `  run ${m.run}: ${m.mode}`)].join('\n')}\n`;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function report(args) {
  return runReport(args);
}

export const USAGE = 'usage: code-forge report --slug <slug> [--json] [--export <dir>]\n';

export const SUMMARY = "Display or export project ledger reports, including per-block pace and run totals. Pace durations are minutes; absent row kinds contribute 0. JSON includes the same pace numbers. Report currently runs in Node; no Go report exists yet.";
