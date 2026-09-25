/**
 * `code-forge doctor [--quick] [--json] [--cwd <dir>]` (plan §2.3; block B13b).
 *
 * Prints one row per check (`<STATUS> <label>: <detail>`) on stdout, or with `--json` ONE JSON
 * document `{ok, counts, rows}`; a one-line summary goes to stderr. Exit 0 when no row FAILs,
 * 1 when one does, 2 on a usage error. The full run adopts a private temp root for its probes
 * (every child it spawns is registered there) and removes it before exiting.
 */

import { randomBytes } from 'node:crypto';
import { rmSync } from 'node:fs';
import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { runDoctor } from '../doctor/index.mjs';
import { counts, exitCode, renderJSON, renderText } from '../doctor/rows.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { writeSafe } from '../util/redact.mjs';
import { runRoot, setRunRoot } from '../util/tmp.mjs';

const USAGE = 'usage: code-forge doctor [--quick] [--json] [--cwd <dir>]\n';

/**
 * @param {string[]} args
 * @param {import('../doctor/index.mjs').DoctorDeps & {stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}}} [deps]
 * @param {boolean} [adopt] - the real verb: adopt one temp root so exec registers every probe
 *   child there (in-process callers keep their own run root).
 * @returns {Promise<number>}
 */
export async function runDoctorCli(args, deps = {}, adopt = false) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  let parsed;
  try {
    parsed = parseFlags(args, { values: ['cwd'], booleans: ['quick', 'json'] });
  } catch (err) {
    writeSafe(stderr, `doctor: ${err.message}\n${USAGE}`);
    return 2;
  }
  if (parsed.positionals.length > 0) {
    writeSafe(stderr, USAGE);
    return 2;
  }
  const { flags } = parsed;
  const cwd = path.resolve(typeof flags.cwd === 'string' ? flags.cwd : process.cwd());
  /** @type {string | undefined} */
  let root;
  if (adopt && !flags.quick) {
    const loaded = await loadProjectConfig(cwd);
    const tmpRoot = loaded.ok && typeof loaded.config?.tmp?.root === 'string' ? loaded.config.tmp.root : undefined;
    try {
      root = runRoot(`doctor-${process.pid}-${randomBytes(3).toString('hex')}`, { root: tmpRoot });
      setRunRoot(root);
    } catch {
      root = undefined; // runDoctor reports the missing temp root as a FAIL row
    }
  }
  try {
    const rows = await runDoctor({ cwd, quick: flags.quick === true, ...(root ? { runRootDir: root } : {}) }, deps);
    writeSafe(stdout, flags.json ? renderJSON(rows) : renderText(rows));
    const c = counts(rows);
    writeSafe(stderr, `doctor: ${c.OK} ok, ${c.WARN} warn, ${c.FAIL} fail\n`);
    return exitCode(rows);
  } finally {
    if (root) {
      setRunRoot(null);
      rmSync(root, { recursive: true, force: true });
    }
  }
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function doctor(args) {
  return runDoctorCli(args, {}, true);
}
