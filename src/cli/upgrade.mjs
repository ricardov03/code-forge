/**
 * `code-forge upgrade [--source <path>]` — re-points every recorded install at a skill source
 * (plan §10.3 acceptance: "`upgrade` re-points all 3 recorded links"; research/installer-patterns.md
 * point 8, "a self `upgrade` command"). A symlink record is re-created to point at the new
 * source; a copy record is re-copied from it. With no `--source`, defaults to this running
 * package's own `skill/` directory (the normal case: `npm update` moved the package, `upgrade`
 * re-syncs every harness to it) — `--source` exists for pointing at an explicit path instead
 * (tests, or a power user working from a git checkout of the skill).
 */

import { stat } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { writeSafe } from '../util/redact.mjs';
import { installsPath, upgradeAll } from '../install/link.mjs';

const USAGE = 'usage: code-forge upgrade [--source <path>]\n';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
/** This package's own skill directory: `<package root>/skill`, i.e. two levels above `src/cli/`. */
export const DEFAULT_SKILL_SOURCE = path.join(__dirname, '..', '..', 'skill');

/**
 * Strict parse: `[--source <path>]` only.
 * @param {string[]} args
 * @returns {{source?: string} | null}
 */
export function parseUpgradeArgs(args) {
  if (args.length === 0) {
    return {};
  }
  const [flag, value, ...extra] = args;
  if (flag !== '--source' || typeof value !== 'string' || value.length === 0 || extra.length > 0) {
    return null;
  }
  return { source: value };
}

/**
 * @param {string[]} args
 * @param {object} deps
 * @param {{write: (s: string) => unknown}} [deps.stdout]
 * @param {{write: (s: string) => unknown}} [deps.stderr]
 * @param {string} [deps.home]
 * @param {string} [deps.defaultSource]
 * @param {string} [deps.cwd] - a relative `--source` is resolved against this, never against the
 *   link's own directory (which `symlink()` would otherwise silently use).
 * @returns {Promise<number>}
 */
export async function runUpgrade(
  args,
  {
    stdout = process.stdout,
    stderr = process.stderr,
    home = os.homedir(),
    defaultSource = DEFAULT_SKILL_SOURCE,
    cwd = process.cwd(),
  } = {},
) {
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);

  const parsed = parseUpgradeArgs(args);
  if (!parsed) {
    err(USAGE);
    return 2;
  }

  // Resolved BEFORE use: a relative --source (e.g. "./skill") must mean "relative to where the
  // user ran this command", never get stored/symlinked as a relative string (which `readlink`
  // would then resolve against the LINK's own directory, not the user's cwd).
  const resolvedSource = path.resolve(cwd, parsed.source ?? defaultSource);

  let sourceStat;
  try {
    sourceStat = await stat(resolvedSource);
  } catch {
    err(`upgrade: source "${resolvedSource}" does not exist\n`);
    return 1;
  }
  if (!sourceStat.isDirectory()) {
    err(`upgrade: source "${resolvedSource}" is not a directory\n`);
    return 1;
  }

  const updated = await upgradeAll({ installsFile: installsPath(home), source: resolvedSource });
  for (const record of updated) {
    out(`upgraded ${record.harness} (${record.scope}) -> ${record.source}\n`);
  }
  if (updated.length === 0) {
    out('nothing to upgrade\n');
  }
  return 0;
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function upgrade(args) {
  return runUpgrade(args, {});
}
