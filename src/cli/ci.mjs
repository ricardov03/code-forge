/**
 * `code-forge ci` — B430 Node shim for a verb the Go binary serves (same pattern as docs.mjs).
 * `--help`/`-h` is answered by the router from USAGE (same text the Go binary prints). Any other
 * call hands the arguments to the Go binary for this host, or prints one line naming that need and
 * exits 1.
 */

import { spawnSync } from 'node:child_process';
import { nativePath } from '../util/native.mjs';

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function ci(args) {
  const binary = nativePath();
  if (binary === null) {
    process.stderr.write('code-forge ci: needs the Go binary native/bin/<platform>-<arch>/code-forge-native (missing for this host, or CODE_FORGE_NATIVE=0)\n');
    return 1;
  }
  const env = { ...process.env, CODE_FORGE_NODE: process.env.CODE_FORGE_NODE || process.execPath };
  const result = spawnSync(binary, ['ci', ...args], { stdio: 'inherit', env });
  return result.error || result.status === null ? 1 : result.status;
}

export const USAGE = "usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]\n";

export const SUMMARY = "Watch the GitHub Actions runs of one commit; a red run names the failing jobs and tests and queues a heal item (served by the Go binary).";
