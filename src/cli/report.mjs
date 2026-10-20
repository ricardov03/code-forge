/**
 * `code-forge report [--slug <slug>] [--json] [--export <dir>]` (plan §6.2, R4). Terminal output
 * only, plus an optional `--export <dir>` that writes the same sections as JSON files. The
 * terminal output ends with the spend per run (B33) and the mode of each run (B59: brief or plan);
 * both are text only, never in `--json` or `--export`. The 14th section, `outside_review` (B66), is
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
    err('usage: code-forge report --slug <slug> [--json] [--export <dir>]\n');
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

    const exportDir = flagValue(args, '--export');
    if (exportDir) {
      const paths = await exportReportSections(exportDir, sections);
      out(`exported ${paths.length} section file(s) to ${exportDir}\n`);
      return 0;
    }

    if (args.includes('--json')) {
      out(`${JSON.stringify(sections, null, 2)}\n`);
      return 0;
    }

    for (const [name, data] of Object.entries(sections)) {
      let body;
      if (name === 'cost_per_block') body = blockTimeLines(data);
      else if (name === 'run_time') body = runTimeLines(data);
      else if (name === 'outside_review') body = outsideReviewLines(data);
      else body = `${JSON.stringify(data, null, 2)}\n`;
      out(`== ${name} ==\n${body}`);
    }
    out(spendLines(buildSpend(rows)));
    out(modeLines(buildRunModes(rows)));
    return 0;
  } catch (err_) {
    err(`report: ${err_?.message ?? String(err_)}\n`);
    return 1;
  }
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
    lines.push(`${b.run ?? '(none)'} | ${b.block} | $${b.totalUsd.toFixed(4)} | ${duration(b.wallMs)} | ${duration(b.humanWaitMs)} | ${b.completed ? 'completed' : b.stopped ? 'stopped' : 'open'}`);
    lines.push(`  level ${b.level ?? '-'} · lane ${b.lane ?? '-'} · coder $${b.coderUsd.toFixed(4)} · review $${b.reviewUsd.toFixed(4)} · S1+S2 $${b.s1s2Usd.toFixed(4)} · facts $${b.factsUsd.toFixed(4)} · other $${b.otherUsd.toFixed(4)} · proof ${duration(b.proofTimeMs)} · ${b.unknownUsdSessions} unknown`);
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
  for (const r of runs) lines.push(`  run ${r.run ?? '(none)'}: $${r.usd.toFixed(4)} · ${r.sessions} session(s)${unknown(r.unknown)}`);
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
