/**
 * `code-forge heal` — B429 Node shim (the B371 pattern) for a verb the Go binary serves. `--help`/`-h` is answered
 * by the router from USAGE (same text the Go binary prints). Any other call hands the arguments to
 * the Go binary for this host, or prints one line naming that need and exits 1.
 */

import { spawnSync } from 'node:child_process';
import { nativePath } from '../util/native.mjs';

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function heal(args) {
  const binary = nativePath();
  if (binary === null) {
    process.stderr.write('code-forge heal: needs the Go binary native/bin/<platform>-<arch>/code-forge-native (missing for this host, or CODE_FORGE_NATIVE=0)\n');
    return 1;
  }
  const env = { ...process.env, CODE_FORGE_NODE: process.env.CODE_FORGE_NODE || process.execPath };
  const result = spawnSync(binary, ['heal', ...args], { stdio: 'inherit', env });
  return result.error || result.status === null ? 1 : result.status;
}

export const USAGE = "usage: code-forge heal list [--run R] [--json] | show <id> [--run R] [--json] | run --run R [--once|--watch] [--max-tries N] [--json]\n";

export const SUMMARY = "List, show and retry the heal queue: failures that pass by themselves (a held lock, a transient git error), served by the Go binary.";
