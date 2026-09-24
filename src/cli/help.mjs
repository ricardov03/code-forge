/**
 * `code-forge help` (also reached via `--help`/`-h`/no verb, aliased by `bin/code-forge.mjs`).
 * Lists every discovered verb, one per line, each indented by exactly two spaces so a test (or a
 * script) can count them reliably: `/^ {2}\S+$/`.
 */

import { readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

/**
 * A verb file's basename must match this. Duplicated (not imported) from `bin/code-forge.mjs` —
 * both B0-owned files must stay in sync since there is no shared-util file in B0's scope to hold
 * this once.
 */
const VERB_FILE_PATTERN = /^[a-z][a-z0-9-]*\.mjs$/;

/**
 * Fallback discovery used only when this verb is called without the router's `context.verbs`
 * (i.e. not through `bin/code-forge.mjs` — covered by the "help() with no context" test in
 * `test/router.test.mjs`, so this path is exercised, not dead code).
 * @returns {Promise<string[]>}
 */
async function discoverVerbs() {
  const entries = await readdir(__dirname, { withFileTypes: true });
  return entries
    .filter((entry) => entry.isFile() && VERB_FILE_PATTERN.test(entry.name))
    .map((entry) => entry.name.slice(0, -'.mjs'.length))
    .sort();
}

/**
 * @param {string[]} _args
 * @param {{verbs?: string[]}} [context] - the router already knows the verb list; reuse it when
 *   present instead of reading the directory a second time.
 * @returns {Promise<number>}
 */
export default async function help(_args, context = {}) {
  const verbs = context.verbs ?? (await discoverVerbs());
  process.stdout.write('Usage: code-forge <verb> [args]\n\n');
  process.stdout.write('Verbs:\n');
  for (const verb of verbs) {
    process.stdout.write(`  ${verb}\n`);
  }
  return 0;
}
