/**
 * `code-forge ledger calibration|outcome|tail` (plan §6.3, C12). Every subcommand takes
 * `--slug <slug>` explicitly: this block (B6) depends only on B0, so it has no config loader
 * (B1) to read `project.slug` from — a later block's verb passes `--slug` through from config.
 *
 *   ledger calibration --slug <slug> [--question <id>]
 *   ledger outcome --scan-git --slug <slug> [--cwd <dir>] [--days <n>]
 *   ledger outcome --pr <n> --ci red|green|reverted --slug <slug>
 *   ledger tail --slug <slug> [--n <count>]
 */

import { writeSafe } from '../util/redact.mjs';
import { buildCalibration } from '../ledger/calibration.mjs';
import { recordCiOutcome, reviewedEntriesFromRows, scanGitAndRecord } from '../ledger/outcome.mjs';
import { readAllRows } from '../ledger/write.mjs';

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
  const usage = () => err('usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]\n');

  const [sub] = args;
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
      const nFlag = readOptionalFlag(args, '--n');
      if (!nFlag.ok) {
        err('ledger tail: --n requires a value\n');
        return 2;
      }
      const nArg = nFlag.value ?? '20';
      const n = parseNonNegativeInt(nArg);
      if (n === null) {
        err(`ledger tail: --n must be a non-negative integer, got "${nArg}"\n`);
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

/** @param {string[]} args @returns {Promise<number>} */
export default async function ledger(args) {
  return runLedger(args);
}
