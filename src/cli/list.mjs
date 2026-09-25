/**
 * `code-forge list` (plan §2.1: "`code-forge list` prints what is linked where and whether the
 * target resolves"). Two sections:
 *  - INSTALLED — every recorded row of `~/.code-forge/installs.json`, plus a live `resolves`
 *    check (a broken symlink, or a target someone deleted by hand, reports `no`).
 *  - HARNESSES — the full path table (plan §2.1), with live PATH/home-dir detection and each
 *    row's `verified` flag from `src/install/harnesses.mjs` (the Gemini row is `UNVERIFIED`).
 *
 * Takes no arguments; `code-forge list <anything>` is a usage error.
 */

import os from 'node:os';
import { writeSafe } from '../util/redact.mjs';
import { detectAll } from '../install/detect.mjs';
import { HARNESSES } from '../install/harnesses.mjs';
import { installsPath, readInstalls, targetResolves } from '../install/link.mjs';

const USAGE = 'usage: code-forge list\n';

/**
 * @param {string[]} args
 * @param {object} deps
 * @param {{write: (s: string) => unknown}} [deps.stdout]
 * @param {{write: (s: string) => unknown}} [deps.stderr]
 * @param {string} [deps.home]
 * @param {NodeJS.ProcessEnv} [deps.env]
 * @returns {Promise<number>}
 */
export async function runList(args, { stdout = process.stdout, stderr = process.stderr, home = os.homedir(), env = process.env } = {}) {
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);

  if (args.length > 0) {
    err(USAGE);
    return 2;
  }

  const records = await readInstalls(installsPath(home));
  out('INSTALLED\n');
  out('HARNESS\tSCOPE\tMETHOD\tTARGET\tRESOLVES\n');
  for (const record of records) {
    const resolves = await targetResolves(record.target);
    out(`${record.harness}\t${record.scope}\t${record.method}\t${record.target}\t${resolves ? 'yes' : 'no'}\n`);
  }
  if (records.length === 0) {
    out('(none)\n');
  }

  const detections = await detectAll({ home, pathEnv: env.PATH });
  out('\nHARNESSES\n');
  out('ID\tLABEL\tDETECTED\tVERIFIED\tPROJECT PATH\n');
  for (const harness of HARNESSES) {
    const detection = detections.find((d) => d.id === harness.id);
    out(
      `${harness.id}\t${harness.label}\t${detection?.detected ? 'yes' : 'no'}\t${harness.verified ? 'yes' : 'UNVERIFIED'}\t${harness.projectSkillsDir}\n`,
    );
  }

  return 0;
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function list(args) {
  return runList(args, {});
}
