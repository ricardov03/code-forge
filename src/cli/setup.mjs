/**
 * `code-forge setup` — B463. The Go binary serves this verb. Node reaches it when the Go binary
 * hands it over (an older binary without the verb) or with CODE_FORGE_NATIVE=0; it runs the same
 * check as scripts/setup.mjs, which bin/code-forge also runs when the binary is missing, not
 * executable or crashes.
 */

import { runSetup } from '../../scripts/setup.mjs';

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function setup(args) {
  return runSetup(args);
}

export const USAGE = "usage: code-forge setup\n";

export const SUMMARY = "Check the Go binary for this system: print OK, or the problem and one fix command.";
