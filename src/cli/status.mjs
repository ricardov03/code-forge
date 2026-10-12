/**
 * `code-forge status [--run <id>] [--json]` (B64a, issue #13 item 6): a short status of a run, so
 * the orchestrator checks progress with one command instead of reading logs or polling
 * transcripts.
 *
 * Read-only: it reads the run record (`~/.code-forge/runs/<id>.json`, as `run status` does — no
 * lock), the project's ledger rows and the workspace config (for `budget.usd`), and writes
 * nothing — no ledger row, no file, no lock. The data comes from `briefStatus`
 * (`src/state/brief-status.mjs`); this file finds the runs, counts open findings and renders.
 *
 * With `--run`, that run (ended or not). Without it, every run that is not ended whose workspace
 * is the current repository (`projectRootFor(cwd)`, compared on realpaths), newest first (by
 * `started_at`, then run id); with none, `no active run` (exit 0; `{"runs":[]}` with `--json`).
 * An unknown `--run` exits 1; a usage error (unknown flag, a positional) exits 2.
 *
 * Text: at most {@link STATUS_MAX_LINES} lines per run, runs separated by one blank line:
 *   run <id> · <status> · <plan|brief> · spent $<x>[ of $<budget>]
 *   <id> <state> <level> · <model or -> · gate <pass|fail|none>[ (<gate>)] · findings <n>
 *   … +<k> more blocks                  (only when the open blocks do not fit)
 *   closed <a> · stopped <b>            (only when any exist; printed last, but block lines are
 *                                        dropped first to keep it inside the cap)
 * The gate field is `gate pass`, `gate fail (<gate>)`, `gate fail` (no gate named) or `gate none`.
 * `<x>` is `run status`'s spent_usd with 2 decimals, followed by `+?` when some session has no
 * known price (`run status`'s unknown_usd_sessions > 0); the whole amount is `$?` when the ledger
 * cannot be read. A run record with no status shows `-`.
 *
 * `--json`: exactly one line, `{"runs":[{run_id, status, mode, spent_usd, unknown_usd_sessions,
 * budget_usd, blocks, more, closed, stopped}]}` — every block that is neither closed nor stopped
 * (no cap), `more` always 0, `spent_usd` and `unknown_usd_sessions` null when the ledger cannot be
 * read, `status` null when the record has none.
 *
 * The run lock (`<runs>/<id>.lock`, taken by the worker and the block verbs) is never taken nor
 * waited on: a held lock does not delay `status`.
 *
 * Findings: the number of distinct (file, finding) pairs the block gate would refuse today
 * (`checkBlockReviews`, `src/review/gate-check.mjs` — the same rules `block close` applies: the
 * latest review of each file left it open and no valid waiver, ruling or later approval cleared
 * it). The files are the ones the block's review rows name (`reviewedFiles`, the selection
 * `block close` uses for reviewed files), each at the content hash of its LAST review row (only
 * review rows — the events `reviewedFiles` counts — supply a hash; other rows never do). A file
 * whose review rows carry no hash is checked at the empty hash: the gate counts a refusal row
 * with no `content_hash` for every hash of its file (fail closed), so its open findings count.
 * The gate's other refusals (unreviewed, unsigned, proof, rule breaks) are not findings and are
 * not counted. No transcript, diff or review text is ever read or printed.
 *
 * The run key is only read (`loadKey`: a stat and a read, never a create or a write); with no
 * key, a zero key stands in, so no signed waiver or approval verifies (fail closed).
 */

import { readdir, realpath } from 'node:fs/promises';
import path from 'node:path';
import { loadProjectConfig, projectRootFor } from '../config/load.mjs';
import { budgetUsdOf } from '../ledger/spend.mjs';
import { readAllRows } from '../ledger/write.mjs';
import { findingsKey, openFindingsOf } from '../review/open-findings.mjs';
import { briefStatus } from '../state/brief-status.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { runsDir, StateError } from '../state/paths.mjs';
import { readRun } from '../state/run.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE = 'usage: code-forge status [--run <id>] [--json]\n';

/** The hard cap on text lines per run. */
const STATUS_MAX_LINES = 10;

/**
 * @typedef {import('../state/brief-status.mjs').RunStatus} RunStatus
 */

/** @param {string} p @returns {Promise<string>} */
async function realOr(p) {
  try {
    return await realpath(p);
  } catch {
    return path.resolve(p);
  }
}

/**
 * The status data of one run.
 * @param {Record<string, any>} record
 * @returns {Promise<RunStatus>}
 */
async function statusOf(record) {
  /** @type {Array<Record<string, any>> | null} null: the ledger cannot be read */
  let rows = null;
  try {
    rows = await readAllRows(record.project);
  } catch {
    rows = null;
  }
  let budget = null;
  try {
    const loaded = await loadProjectConfig(record.workspace);
    budget = loaded.ok ? budgetUsdOf(loaded.config) : null;
  } catch {
    budget = null;
  }
  const key = await findingsKey(record);
  const runId = String(record.run_id);
  const all = rows ?? [];
  /** @type {Map<string, Array<Record<string, any>>>} the run's rows, grouped by block once */
  const byBlock = new Map();
  for (const r of all) {
    if (r?.run !== runId || typeof r.block !== 'string') continue;
    const list = byBlock.get(r.block);
    if (list) list.push(r);
    else byBlock.set(r.block, [r]);
  }
  return briefStatus({ record, rows, budget, findingsOf: (block) => openFindingsOf({ block, runId, mine: byBlock.get(block) ?? [], rows: all, key }) });
}

/**
 * Every run that is not ended whose workspace is `root`, newest first.
 * @param {string} root
 * @returns {Promise<Array<Record<string, any>>>}
 */
async function activeRunsFor(root) {
  const want = await realOr(root);
  let names;
  try {
    names = await readdir(runsDir());
  } catch {
    return [];
  }
  const records = [];
  for (const name of names) {
    if (!name.endsWith('.json')) continue;
    let record;
    try {
      record = await readRun(name.slice(0, -'.json'.length));
    } catch {
      continue; // not a run record (a bad id, unparsable): not a run to show
    }
    if (!record || typeof record !== 'object' || record.status === 'ended' || typeof record.workspace !== 'string') continue;
    if ((await realOr(record.workspace)) !== want) continue;
    records.push(record);
  }
  const at = (/** @type {Record<string, any>} */ r) => (typeof r.started_at === 'string' ? r.started_at : '');
  return records.sort((a, b) => at(b).localeCompare(at(a)) || String(b.run_id).localeCompare(String(a.run_id)));
}

/** @param {number} n @returns {string} */
const usd = (n) => `$${n.toFixed(2)}`;

/**
 * The text lines of one run, at most {@link STATUS_MAX_LINES}.
 * @param {RunStatus} s @returns {string[]}
 */
export function runLines(s) {
  const spent = s.spent_usd === null ? '$?' : `${usd(s.spent_usd)}${(s.unknown_usd_sessions ?? 0) > 0 ? '+?' : ''}`;
  const lines = [`run ${s.run_id} · ${s.status ?? '-'} · ${s.mode} · spent ${spent}${s.budget_usd !== null ? ` of ${usd(s.budget_usd)}` : ''}`];
  const tally = s.closed + s.stopped > 0 ? `closed ${s.closed} · stopped ${s.stopped}` : null;
  const room = STATUS_MAX_LINES - 1 - (tally === null ? 0 : 1);
  const shown = s.blocks.length <= room ? s.blocks : s.blocks.slice(0, room - 1);
  for (const b of shown) {
    lines.push(`${b.id} ${b.state} ${b.level ?? '-'} · ${b.model ?? '-'} · gate ${b.gate.result}${b.gate.name ? ` (${b.gate.name})` : ''} · findings ${b.findings}`);
  }
  if (shown.length < s.blocks.length) lines.push(`… +${s.blocks.length - shown.length} more blocks`);
  if (tally !== null) lines.push(tally);
  return lines;
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function status(args) {
  const out = (/** @type {string} */ s) => writeSafe(process.stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(process.stderr, s);
  let flags;
  try {
    const parsed = parseFlags(args, { values: ['run'], booleans: ['json'] });
    if (parsed.positionals.length > 0) throw new StateError('usage', `unexpected argument ${JSON.stringify(parsed.positionals[0])}`);
    flags = parsed.flags;
  } catch (thrown) {
    err(`status: ${thrown?.message ?? String(thrown)}\n${USAGE}`);
    return 2;
  }
  const json = flags.json === true;
  try {
    /** @type {Array<Record<string, any>>} */
    let records;
    if (typeof flags.run === 'string') {
      const id = flags.run;
      const record = await readRun(id).catch((thrown) => (thrown?.code === 'no-run' ? null : Promise.reject(thrown)));
      if (!record || typeof record !== 'object') {
        err(`status: no run record for ${id}\n`); // the wording every verb uses for an unknown run
        return 1;
      }
      records = [record];
    } else {
      records = await activeRunsFor(projectRootFor(process.cwd()));
    }
    const shown = [];
    for (const record of records) shown.push(await statusOf(record));
    if (json) {
      out(`${JSON.stringify({ runs: shown })}\n`); // briefStatus builds exactly the clause-3 run object
      return 0;
    }
    if (shown.length === 0) {
      out('no active run\n');
      return 0;
    }
    out(`${shown.map((s) => runLines(s).join('\n')).join('\n\n')}\n`);
    return 0;
  } catch (thrown) {
    err(`status: ${thrown?.message ?? String(thrown)}\n`);
    return 1;
  }
}
