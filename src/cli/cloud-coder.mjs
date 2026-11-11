/**
 * `code-forge cloud-coder` — B371 Node shim for a verb the Go binary serves. `--help`/`-h` is
 * answered by the router from USAGE (same bytes the Go binary prints). Any other call hands the
 * arguments to the Go binary for this host, or prints one line naming that need and exits 1.
 */

import { spawnSync } from 'node:child_process';
import { nativePath } from '../util/native.mjs';

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function cloudCoder(args) {
  const binary = nativePath();
  if (binary === null) {
    process.stderr.write('code-forge cloud-coder: needs the Go binary native/bin/<platform>-<arch>/code-forge-native (missing for this host, or CODE_FORGE_NATIVE=0)\n');
    return 1;
  }
  // Go runs Node-only verbs (review-file, ledger) through CODE_FORGE_NODE, like a handover.
  const env = { ...process.env, CODE_FORGE_NODE: process.env.CODE_FORGE_NODE || process.execPath };
  const result = spawnSync(binary, ['cloud-coder', ...args], { stdio: 'inherit', env });
  return result.error || result.status === null ? 1 : result.status;
}

export const USAGE = "usage: code-forge cloud-coder --run <r> --block <id>\n";

export const SUMMARY = "Hand a block to the configured cloud coder and commit its returned patch in the block worktree (served by the Go binary).";
