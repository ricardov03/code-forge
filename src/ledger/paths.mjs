/**
 * Where the ledger lives on disk: `~/.code-forge/ledger/<slug>.jsonl` (plan §1.2, §6).
 * `os.homedir()` re-reads `process.env.HOME` on every call (POSIX), so a test that points `HOME`
 * at a temp dir before calling in gets a fully isolated ledger — nothing here ever touches a
 * real `~/.code-forge/`.
 */

import { homedir } from 'node:os';
import path from 'node:path';

/** A project slug: lowercase, digits, hyphens — same shape `project.slug` takes in the schema. */
const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]*$/;

/** @returns {string} */
export function ledgerDir() {
  return path.join(homedir(), '.code-forge', 'ledger');
}

/**
 * @param {string} slug
 * @returns {string}
 * @throws {TypeError} on an empty/invalid slug — a bad slug must never resolve to a path outside
 *   `ledgerDir()` (e.g. via `..` or `/`), so this rejects anything that isn't a plain slug token.
 */
export function ledgerPath(slug) {
  if (typeof slug !== 'string' || !SLUG_PATTERN.test(slug)) {
    throw new TypeError(`ledgerPath: slug must match ${SLUG_PATTERN} (got ${JSON.stringify(slug)})`);
  }
  return path.join(ledgerDir(), `${slug}.jsonl`);
}
