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
 * B371: every public Go route has a thin src/cli shim (cloud-coder.mjs, docs.mjs), so the readdir
 * above is the one source of the verb list. No verb is Go-only any more; this stays exported, and
 * empty, for test/router.test.mjs ("help lists no name beyond the src/cli files"). A Go route added
 * without a shim fails TestHelpListsEveryGoRoute.
 */
export const GO_ONLY_VERBS = Object.freeze([]);

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
  process.stdout.write('\nBlock usage: code-forge block <verb> --help (or -h).\n');
  process.stdout.write('  block open --acceptance <file>: JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.\n');
  process.stdout.write('  block open --brief <file>: a file inside the repository, not inline text.\n');
  process.stdout.write('  block land <id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--push] [--watch-ci] [--copy-links] [--json]: join reviews, run changed gates, close, then with --merge run the merge gate and merge with --no-ff.\n');
  process.stdout.write('  block land <id> <id>... --run <r> [--max <dur>] [--push] [--watch-ci] [--copy-links] [--json]: land closed blocks in order with one merge gate.\n');
  process.stdout.write('  Configuration fallback entries must be {provider, model} objects, not model-name strings (optional effort).\n');
  process.stdout.write('\nTest queue: gates wait in one machine-wide FIFO queue (code-forge queue shows it); wrap other test commands in `code-forge queue run -- <cmd>`.\n');
  return 0;
}

export const USAGE = "usage: code-forge help\n";

export const SUMMARY = "List the available CLI verbs and shared command guidance.";
