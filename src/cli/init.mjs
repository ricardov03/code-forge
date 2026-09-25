/**
 * `code-forge init` — the setup wizard (plan §2; block B13a). The wizard lives in
 * `src/install/wizard/`; this verb only hands it the process's argv and streams.
 */

import { runInit } from '../install/wizard/run.mjs';

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function init(args) {
  return runInit(args);
}
