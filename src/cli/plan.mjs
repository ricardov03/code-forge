/**
 * `code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]` (plan §3.8 rule 4, §3.2;
 * block B9b) — the deterministic harden exit. Prints every failing row on stderr, one per line, and
 * exits 1 when there is any; prints `plan check: ok (<n> blocks)` and exits 0 otherwise; 2 on usage.
 * WARN lines (a required section found by its position under a near-miss heading) go to stderr and
 * never fail the check. Either way stdout ends with the lane recorded per block (B36):
 * `lanes: B1 L1 (jev) · B2 none` — read from the project's ledger (`--slug`, else `project.slug`,
 * else the directory-name slug `run`, `author` and `jev ask` use — the project root's, B50).
 * Limits come from the project root's `.code-forge.yml` when there is one (`caps.coders`, `budget.block_cases`,
 * `budget.block_lines`), else the plan defaults.
 */

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { loadProjectConfig, projectRootFor, slugFor } from '../config/load.mjs';
import { ledgerPath } from '../ledger/paths.mjs';
import { readAllRows } from '../ledger/write.mjs';
import { FactsError, readProjectBins } from '../session/facts.mjs';
import { checkPlan } from '../session/plan-check.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE = 'usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]\n';

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}, cwd?: string}} [deps]
 * @returns {Promise<number>}
 */
export async function runPlanVerb(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const cwd = deps.cwd ?? process.cwd();
  try {
    const { flags, positionals } = parseFlags(args, { values: ['facts', 'slug'] });
    if (positionals.length !== 2 || positionals[0] !== 'check') {
      writeSafe(stderr, USAGE);
      return 2;
    }
    const planPath = path.resolve(cwd, positionals[1]);
    const factsPath = typeof flags.facts === 'string' ? path.resolve(cwd, flags.facts) : undefined;
    /** @param {string} file @param {string} what */
    const read = (file, what) => {
      try {
        return readFileSync(file, 'utf8');
      } catch {
        throw new StateError('usage', `${what} cannot be read`);
      }
    };
    const text = read(planPath, 'the plan file');
    const factsText = factsPath ? read(factsPath, '--facts') : undefined;
    // B50: the project's config and ledger, also from a subfolder of the project
    const root = projectRootFor(cwd);
    const loaded = await loadProjectConfig(root);
    if (!loaded.ok && loaded.error !== 'not-found') {
      writeSafe(stderr, `plan: ${loaded.message}\n`);
      return 2;
    }
    const cfg = loaded.ok ? loaded.config : undefined;
    // the same ledger as the rest of the tool (and `jev ask`): --slug, else project.slug, else the directory name
    const slug = typeof flags.slug === 'string' ? flags.slug : slugFor(cfg, root);
    try {
      ledgerPath(slug);
    } catch {
      throw new StateError('usage', '--slug must be lowercase letters, digits and hyphens');
    }
    // a missing ledger is no rows (every block `none`); any other read error propagates as itself
    const decisions = await readAllRows(slug);
    // package.json is read lazily: only a command claim that needs the bin prefix reads it, so a bad
    // package.json fails only the plans that depend on it
    let result;
    try {
      result = checkPlan(text, { planPath, factsPath, factsText, cfg, decisions, root, projectBins: () => readProjectBins(root) });
    } catch (err) {
      if (!(err instanceof FactsError)) throw err;
      writeSafe(stderr, `plan: ${err.message}\n`);
      return 2;
    }
    for (const line of result.warnings) writeSafe(stderr, `${line}\n`);
    for (const line of result.errors) writeSafe(stderr, `${line}\n`);
    if (result.ok) writeSafe(stdout, `plan check: ok (${result.blocks} blocks)\n`);
    if (result.lanes.length > 0) writeSafe(stdout, `lanes: ${result.lanes.map((l) => `${l.block} ${l.lane === null ? 'none' : `${l.lane} (${l.source})`}`).join(' · ')}\n`);
    return result.ok ? 0 : 1;
  } catch (err) {
    if (err instanceof StateError) {
      writeSafe(stderr, `plan: ${err.message}\n`);
      return 2;
    }
    throw err;
  }
}

/** @param {string[]} args */
export default async function planVerb(args) {
  return runPlanVerb(args);
}
