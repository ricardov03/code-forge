/**
 * `code-forge report [--slug <slug>] [--json] [--export <dir>]` (plan §6.2, R4). Terminal output
 * only, plus an optional `--export <dir>` that writes the same 13 sections as JSON files.
 */

import { writeSafe } from '../util/redact.mjs';
import { buildReport } from '../ledger/report.mjs';
import { exportReportSections } from '../ledger/export.mjs';
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
      out(`== ${name} ==\n${JSON.stringify(data, null, 2)}\n`);
    }
    return 0;
  } catch (err_) {
    err(`report: ${err_?.message ?? String(err_)}\n`);
    return 1;
  }
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function report(args) {
  return runReport(args);
}
