/**
 * `code-forge docs` — B371 Node shim for a verb the Go binary serves. `--help`/`-h` is answered
 * by the router from USAGE (same text the Go binary prints). Any other call hands the arguments to
 * the Go binary for this host, or prints one line naming that need and exits 1.
 */

import { spawnSync } from 'node:child_process';
import { nativePath } from '../util/native.mjs';

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function docs(args) {
  const binary = nativePath();
  if (binary === null) {
    process.stderr.write('code-forge docs: needs the Go binary native/bin/<platform>-<arch>/code-forge-native (missing for this host, or CODE_FORGE_NATIVE=0)\n');
    return 1;
  }
  const env = { ...process.env, CODE_FORGE_NODE: process.env.CODE_FORGE_NODE || process.execPath };
  const result = spawnSync(binary, ['docs', ...args], { stdio: 'inherit', env });
  return result.error || result.status === null ? 1 : result.status;
}

export const USAGE = "usage: code-forge docs sync|path [--json]\n";

export const SUMMARY = "Fetch the docs for the installed version (sync) or print the best local copy (path), served by the Go binary.";
