/**
 * `code-forge ledger calibration|outcome|tail` (plan §6.3, C12). Every subcommand takes
 * `--slug <slug>` explicitly: this block (B6) depends only on B0, so it has no config loader
 * (B1) to read `project.slug` from — a later block's verb passes `--slug` through from config.
 *
 *   ledger calibration --slug <slug> [--question <id>]
 *   ledger outcome --scan-git --slug <slug> [--cwd <dir>] [--days <n>]
 *   ledger outcome --pr <n> --ci red|green|reverted --slug <slug>
 *   ledger tail --slug <slug> [--n <count> | -n <count>]
 *   ledger add coder --run <r> --block <b> --usd <n> [--note "..."]
 *
 * B33: `ledger add coder` records manual/cloud coder spend (a session code-forge never saw) as a
 * signed `session` row (`role: coder, manual: true, usd, cost_source: manual`) in the run's
 * ledger, so `report` and the `budget.usd` check count it. The slug is the run record's project.
 */

import { writeSafe } from '../util/redact.mjs';
import { buildCalibration } from '../ledger/calibration.mjs';
import { recordCiOutcome, reviewedEntriesFromRows, scanGitAndRecord } from '../ledger/outcome.mjs';
import { roundUsd, runSpend } from '../ledger/spend.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { readRun, writeSigned } from '../state/run.mjs';

/**
 * @param {string[]} args @param {string} flag
 * @returns {{ok: true, value: string|undefined} | {ok: false}} `ok: false` means the flag WAS
 *   given but with no usable value (missing, or the "next token" is itself another flag) — the
 *   caller must treat this as a usage error, not silently fall back to a default. Without this
 *   distinction, `ledger outcome --scan-git --slug s --cwd --days 3` would read `--cwd`'s value as
 *   absent and quietly scan `process.cwd()` instead — the wrong repo, exiting 0.
 */
function readOptionalFlag(args, flag) {
  const i = args.indexOf(flag);
  if (i < 0) return { ok: true, value: undefined };
  const next = args[i + 1];
  if (next === undefined || next.startsWith('--')) return { ok: false };
  return { ok: true, value: next };
}

/**
 * @param {string[]} args @param {string} flag @returns {string|undefined}
 * For REQUIRED flags only (`--slug`, `--pr`, `--ci`): a missing value is indistinguishable from a
 * missing flag, and both already fall through to the same "required flag absent" usage error at
 * the call site — unlike the optional flags above, there is no silent-default to protect against.
 */
function flagValue(args, flag) {
  const result = readOptionalFlag(args, flag);
  return result.ok ? result.value : undefined;
}

/** @param {string} raw @returns {number|null} a non-negative integer, or null if `raw` isn't one. */
function parseNonNegativeInt(raw) {
  if (!/^\d+$/.test(raw)) return null;
  return Number.parseInt(raw, 10);
}

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}}} [deps]
 * @returns {Promise<number>}
 */
export async function runLedger(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr } = deps;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const usage = () => err('usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."]\n');

  const [sub] = args;
  if (sub === 'add') return addSpend(args.slice(1), { out, err });
  const slug = flagValue(args, '--slug');
  if (!slug) {
    usage();
    return 2;
  }

  try {
    if (sub === 'calibration') {
      const question = readOptionalFlag(args, '--question');
      if (!question.ok) {
        err('ledger calibration: --question requires a value\n');
        return 2;
      }
      const rows = await readAllRows(slug);
      const answers = rows.filter((r) => r.event === 'decision' && typeof r.confidence === 'number');
      const outcomes = rows.filter((r) => r.event === 'outcome' && r.decision_id);
      const { buckets, unbacked, unmapped } = buildCalibration(answers, outcomes, { questionId: question.value });
      out(`${JSON.stringify({ buckets, unbacked, unmapped }, null, 2)}\n`);
      return 0;
    }

    if (sub === 'outcome' && args.includes('--scan-git')) {
      const cwdFlag = readOptionalFlag(args, '--cwd');
      if (!cwdFlag.ok) {
        err('ledger outcome --scan-git: --cwd requires a value\n');
        return 2;
      }
      const cwd = cwdFlag.value ?? process.cwd();

      const daysFlag = readOptionalFlag(args, '--days');
      if (!daysFlag.ok) {
        err('ledger outcome --scan-git: --days requires a value\n');
        return 2;
      }
      let days;
      if (daysFlag.value !== undefined) {
        days = Number(daysFlag.value);
        if (!Number.isFinite(days) || days <= 0) {
          err(`ledger outcome --scan-git: --days must be a finite number > 0, got "${daysFlag.value}"\n`);
          return 2;
        }
      }
      const rows = await readAllRows(slug);
      const reviewed = reviewedEntriesFromRows(rows);
      const written = await scanGitAndRecord({ cwd, reviewed, days, slug });
      out(`scanned git: ${written.length} row(s) marked reverted\n`);
      return 0;
    }

    if (sub === 'outcome' && flagValue(args, '--pr') && flagValue(args, '--ci')) {
      const prArg = flagValue(args, '--pr');
      const pr = parseNonNegativeInt(prArg);
      if (pr === null || pr === 0) {
        err(`ledger outcome: --pr must be a positive integer, got "${prArg}"\n`);
        return 2;
      }
      const ci = flagValue(args, '--ci');
      if (!['red', 'green', 'reverted'].includes(ci)) {
        err(`ledger outcome: --ci must be red|green|reverted, got "${ci}"\n`);
        return 2;
      }
      await recordCiOutcome({ pr, ci: /** @type {'red'|'green'|'reverted'} */ (ci), slug });
      out(`recorded outcome for PR #${pr}: ${ci}\n`);
      return 0;
    }

    if (sub === 'tail') {
      // `-n` is the spelling everyone types from `tail(1)`; before B31 it was not recognised, so
      // `ledger tail -n 3` silently printed the default 20 rows (issue #2, field note 8).
      const longN = readOptionalFlag(args, '--n');
      const shortN = readOptionalFlag(args, '-n');
      if (!longN.ok || !shortN.ok) {
        err(`ledger tail: ${longN.ok ? '-n' : '--n'} requires a value\n`);
        return 2;
      }
      if (longN.value !== undefined && shortN.value !== undefined) {
        err('ledger tail: give the count once, as --n <count> or -n <count>\n');
        return 2;
      }
      const nArg = longN.value ?? shortN.value ?? '20';
      const n = parseNonNegativeInt(nArg);
      if (n === null) {
        err(`ledger tail: --n/-n must be a non-negative integer, got "${nArg}"\n`);
        return 2;
      }
      const rows = await readAllRows(slug);
      for (const row of n === 0 ? [] : rows.slice(-n)) {
        out(`${JSON.stringify(row)}\n`);
      }
      return 0;
    }
  } catch (thrown) {
    // Same problem `report.mjs` fixed in round 1: a corrupt ledger, an unwritable folder, or
    // `--scan-git` outside a git repo must not leak a raw stack trace past the redaction path.
    err(`ledger ${sub}: ${thrown?.message ?? String(thrown)}\n`);
    return 1;
  }

  usage();
  return 2;
}

const ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const MAX_NOTE_CHARS = 500;

/**
 * `ledger add coder --run <r> --block <b> --usd <n> [--note "..."]` (B33).
 * @param {string[]} args - after `add`.
 * @param {{out: (s: string) => void, err: (s: string) => void}} io
 * @returns {Promise<number>}
 */
async function addSpend(args, { out, err }) {
  const usage = () => err('usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note "..."]\n');
  const [role] = args;
  const run = flagValue(args, '--run');
  const block = flagValue(args, '--block');
  const usdArg = flagValue(args, '--usd');
  if (role !== 'coder' || !run || !block || usdArg === undefined) {
    usage();
    return 2;
  }
  const note = readOptionalFlag(args, '--note');
  if (!note.ok) {
    err('ledger add: --note requires a value\n');
    return 2;
  }
  if (!ID.test(run)) {
    err('ledger add: --run must be a run id (letters, digits, ".", "_", "-")\n');
    return 2;
  }
  if (!ID.test(block)) {
    err('ledger add: --block must be a block id (letters, digits, ".", "_", "-")\n');
    return 2;
  }
  // rounded to the ledger's 4 decimals FIRST, then checked: 0.00001 rounds to 0 and is refused
  const usd = /^\d+(\.\d+)?$/.test(usdArg) ? roundUsd(Number(usdArg)) : Number.NaN;
  if (!Number.isFinite(usd) || usd <= 0) {
    err(`ledger add: --usd must be a number > 0 (at 4 decimals), got "${usdArg}"\n`);
    return 2;
  }
  if (note.value !== undefined && note.value.length > MAX_NOTE_CHARS) {
    err(`ledger add: --note is longer than ${MAX_NOTE_CHARS} characters\n`);
    return 2;
  }
  try {
    const { project } = await readRun(run);
    const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: project });
    const row = {
      event: 'session',
      role: 'coder',
      manual: true,
      block,
      provider: null,
      level: null,
      tokens_in: null,
      tokens_out: null,
      usd,
      cost_source: 'manual',
      ...(note.value !== undefined ? { note: note.value } : {}),
    };
    await writeSigned(run, writeRow, row);
    // the row is written: from here on nothing may throw or change the exit code
    /** @type {number | null} */
    let total = null;
    try {
      const spent = runSpend(await readAllRows(project), run).usd;
      total = Number.isFinite(spent) ? spent : null;
    } catch {
      total = null;
    }
    out(`recorded coder spend $${usd.toFixed(4)} for run ${run} block ${block} · run total ${total === null ? 'unknown' : `$${total.toFixed(4)}`}\n`);
    return 0;
  } catch (thrown) {
    err(`ledger add: ${thrown?.message ?? String(thrown)}\n`);
    return 1;
  }
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function ledger(args) {
  return runLedger(args);
}
