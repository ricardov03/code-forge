/**
 * `code-forge decisions` — B390 Node shim (B371 pattern) for a verb the Go binary serves. `--help`/`-h` is answered
 * by the router from USAGE (same text the Go binary prints). Any other call hands the arguments to
 * the Go binary for this host, or prints one line naming that need and exits 1.
 */

import { spawnSync } from 'node:child_process';
import { nativePath } from '../util/native.mjs';

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function decisions(args) {
  const binary = nativePath();
  if (binary === null) {
    process.stderr.write('code-forge decisions: needs the Go binary native/bin/<platform>-<arch>/code-forge-native (missing for this host, or CODE_FORGE_NATIVE=0)\n');
    return 1;
  }
  const env = { ...process.env, CODE_FORGE_NODE: process.env.CODE_FORGE_NODE || process.execPath };
  const result = spawnSync(binary, ['decisions', ...args], { stdio: 'inherit', env });
  return result.error || result.status === null ? 1 : result.status;
}

export const USAGE = "usage: code-forge decisions list [--run <r>] [--json]\n";

export const SUMMARY = "List the block-close decisions the autopilot signed for the owner (rule_break hits, warning and nit waivers), served by the Go binary.";
