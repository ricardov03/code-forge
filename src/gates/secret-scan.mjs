/**
 * Secret scanning gate (plan §8, `gates.extra.secret_scan`). Reuses B1's shape-based token
 * detector (`config/secret-patterns.mjs`) rather than duplicating a pattern list — this module's
 * only job is turning "these files' text" into "hits", with the one FAKE-marker allowance the
 * package's own test fixtures rely on (coder rule: every fake key in this repo's tests contains
 * `FAKE`/`fake`, e.g. `sk-ant-FAKE0123456789`).
 *
 * Hits never carry the token text itself — only `{path, line}` — so a caller can log or print a
 * scan result without ever risking a real secret reaching output (plan §8.2).
 */

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { secretTokensIn } from '../config/secret-patterns.mjs';

/** A token containing this (case-insensitive) is a fixture/test key, never a real one. */
const FAKE_MARKER = /fake/i;

/**
 * @typedef {object} SecretHit
 * @property {string} path
 * @property {number} line - 1-based.
 * @property {number} count - secret-shaped tokens found on that line (fake-marked ones excluded).
 */

/**
 * @param {string} text
 * @param {string} [filePath] - included on each hit; defaults to `''`.
 * @returns {SecretHit[]}
 */
export function scanText(text, filePath = '') {
  /** @type {SecretHit[]} */
  const hits = [];
  const lines = String(text).split('\n');
  for (let i = 0; i < lines.length; i += 1) {
    const tokens = secretTokensIn(lines[i]).filter((token) => !FAKE_MARKER.test(token));
    if (tokens.length > 0) hits.push({ path: filePath, line: i + 1, count: tokens.length });
  }
  return hits;
}

/**
 * @param {ReadonlyArray<{path: string, content: string}>} files
 * @returns {SecretHit[]}
 */
export function scanFileContents(files) {
  if (!Array.isArray(files)) {
    throw new TypeError('scanFileContents: files must be an array of {path, content}');
  }
  return files.flatMap(({ path: filePath, content }) => scanText(content, filePath));
}

/**
 * Reads and scans each file under `cwd`. Only a MISSING file (deleted between the scope check and
 * the scan — `ENOENT`, or a dangling symlink target — `ENOTDIR` on a bogus intermediate segment)
 * is skipped rather than failing the whole scan, since it genuinely has no content to scan. Any
 * OTHER read error (`EACCES`, `EISDIR`, `EIO`, …) is a file this scan could NOT actually read —
 * silently treating that the same as "nothing to see" would report the gate green on a file it
 * never scanned, so it is re-thrown and turns the gate red via the caller's catch.
 * @param {string} cwd
 * @param {ReadonlyArray<string>} relPaths
 * @returns {Promise<SecretHit[]>}
 */
export async function scanFiles(cwd, relPaths) {
  /** @type {SecretHit[]} */
  const hits = [];
  for (const rel of relPaths) {
    let content;
    try {
      content = await readFile(path.join(cwd, rel), 'utf8');
    } catch (err) {
      const code = /** @type {NodeJS.ErrnoException} */ (err).code;
      if (code === 'ENOENT' || code === 'ENOTDIR') continue;
      throw err;
    }
    hits.push(...scanText(content, rel));
  }
  return hits;
}
