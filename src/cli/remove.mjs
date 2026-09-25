/**
 * `code-forge remove [<harness>] [--scope project|global]` — deletes ONLY paths recorded in
 * `~/.code-forge/installs.json` (plan §10.3 acceptance: "`remove` deletes only the 3 recorded
 * paths and leaves a 4th unrecorded file"). With no arguments, every recorded install is removed;
 * `<harness>` narrows to one harness id; `--scope` further narrows to `project` or `global`.
 */

import os from 'node:os';
import { writeSafe } from '../util/redact.mjs';
import { installsPath, removeRecords } from '../install/link.mjs';

const USAGE = 'usage: code-forge remove [<harness>] [--scope project|global]\n';

/**
 * Strict parse: `[<harness>] [--scope <project|global>]`, in that order. An unknown flag, a
 * missing/invalid `--scope` value, a harness name that looks like a flag, or an extra argument is
 * a usage error — never silently reinterpreted.
 * @param {string[]} args
 * @returns {{harness?: string, scope?: 'project'|'global'} | null}
 */
export function parseRemoveArgs(args) {
  if (args.length === 0) {
    return {};
  }
  let harness;
  let rest = args;
  if (!rest[0].startsWith('-')) {
    [harness, ...rest] = rest;
  }
  if (rest.length === 0) {
    return { harness };
  }
  const [flag, value, ...extra] = rest;
  if (flag !== '--scope' || (value !== 'project' && value !== 'global') || extra.length > 0) {
    return null;
  }
  // `harness` is only included when it was actually parsed — an object with an explicit
  // `harness: undefined` key is NOT deep-equal to one that omits the key altogether.
  return harness === undefined ? { scope: value } : { harness, scope: value };
}

/**
 * @param {string[]} args
 * @param {object} deps
 * @param {{write: (s: string) => unknown}} [deps.stdout]
 * @param {{write: (s: string) => unknown}} [deps.stderr]
 * @param {string} [deps.home]
 * @returns {Promise<number>}
 */
export async function runRemove(args, { stdout = process.stdout, stderr = process.stderr, home = os.homedir() } = {}) {
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);

  const parsed = parseRemoveArgs(args);
  if (!parsed) {
    err(USAGE);
    return 2;
  }

  const removed = await removeRecords({ installsFile: installsPath(home), harness: parsed.harness, scope: parsed.scope });
  for (const record of removed) {
    out(`removed ${record.harness} (${record.scope}) ${record.target}\n`);
  }
  if (removed.length === 0) {
    out('nothing to remove\n');
  }
  return 0;
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function remove(args) {
  return runRemove(args, {});
}
