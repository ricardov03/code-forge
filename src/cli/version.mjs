/** `code-forge version` (also reached via `--version`/`-v`, aliased by `bin/code-forge.mjs`). */

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PACKAGE_JSON_PATH = path.join(__dirname, '..', '..', 'package.json');

/**
 * @param {string[]} _args
 * @returns {Promise<number>}
 */
export default async function version(_args) {
  const pkg = JSON.parse(await readFile(PACKAGE_JSON_PATH, 'utf8'));
  process.stdout.write(`${pkg.name} ${pkg.version}\n`);
  return 0;
}
