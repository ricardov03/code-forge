/**
 * `code-forge plan check <plan-file> [--facts <sheet>]` (plan §3.8 rule 4, §3.2; block B9b) —
 * the deterministic harden exit. Prints every failing row on stderr, one per line, and exits 1
 * when there is any; prints `plan check: ok (<n> blocks)` and exits 0 otherwise; 2 on usage.
 * Limits come from `.code-forge.yml` when there is one (`caps.coders`, `budget.block_cases`,
 * `budget.block_lines`), else the plan defaults.
 */

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { checkPlan } from '../session/plan-check.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE = 'usage: code-forge plan check <plan-file> [--facts <sheet>]\n';

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
    const { flags, positionals } = parseFlags(args, { values: ['facts'] });
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
    const loaded = await loadProjectConfig(cwd);
    if (!loaded.ok && loaded.error !== 'not-found') {
      writeSafe(stderr, `plan: ${loaded.message}\n`);
      return 2;
    }
    const result = checkPlan(text, { planPath, factsPath, factsText, cfg: loaded.ok ? loaded.config : undefined });
    if (!result.ok) {
      for (const line of result.errors) writeSafe(stderr, `${line}\n`);
      return 1;
    }
    writeSafe(stdout, `plan check: ok (${result.blocks} blocks)\n`);
    return 0;
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
