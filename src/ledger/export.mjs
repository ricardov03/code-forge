/**
 * `report --export <dir>` — writes one JSON file per report section (plan §6.2: "13 sections").
 */

import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

/** A section key becomes `<dir>/<key>.json` verbatim — must be a plain, path-safe token. Case is
 * allowed here (not restricted to lowercase) so the SEPARATE case-insensitive collision check
 * below is reachable and meaningful, rather than dead code a lowercase-only pattern would make it. */
const SAFE_KEY = /^[A-Za-z0-9_-]+$/;

/**
 * @param {string} dir
 * @param {Record<string, unknown>} sections
 * @returns {Promise<string[]>} the file paths written, one per section key, in `Object.keys` order.
 * @throws {Error} if a key is unsafe (path traversal / separators), collides with another key
 *   case-insensitively (would silently overwrite on a case-insensitive filesystem), or a section's
 *   data is `undefined` (`JSON.stringify(undefined)` returns `undefined`, which would otherwise
 *   write the literal text `"undefined\n"` — not valid JSON — with no error).
 */
export async function exportReportSections(dir, sections) {
  const keys = Object.keys(sections);

  // Validate EVERY key/value up front, before touching the filesystem at all — an export that
  // fails must fail with nothing written, not with some `.json` files present and others missing
  // (fix round 2: the `undefined`-data check used to run inline in the write loop below, so
  // `{a: [1], b: undefined}` wrote `a.json` before throwing on `b`).
  const seenLower = new Set();
  for (const key of keys) {
    if (!SAFE_KEY.test(key)) {
      throw new Error(`exportReportSections: unsafe section key ${JSON.stringify(key)} (must match ${SAFE_KEY})`);
    }
    const lower = key.toLowerCase();
    if (seenLower.has(lower)) {
      throw new Error(`exportReportSections: section key ${JSON.stringify(key)} collides case-insensitively with another key`);
    }
    seenLower.add(lower);
    if (sections[key] === undefined) {
      throw new Error(`exportReportSections: section ${JSON.stringify(key)} has undefined data`);
    }
  }

  await mkdir(dir, { recursive: true });
  const paths = [];
  for (const key of keys) {
    const file = path.join(dir, `${key}.json`);
    await writeFile(file, `${JSON.stringify(sections[key], null, 2)}\n`, 'utf8');
    paths.push(file);
  }
  return paths;
}
