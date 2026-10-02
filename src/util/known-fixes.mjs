/**
 * Known fixes (B38): a small table shipped in the package, `src/util/known-fixes.json`, that maps
 * an error fingerprint (`util/error-log.mjs` `fingerprint`) to the version that fixed it.
 *
 *   [{ "fp": "<12 hex>", "fixed_in": "x.y.z", "summary": "<one plain line>", "issue": <number|null> }]
 *
 * `code-forge logs` marks a known-fixed line `[fixed in x.y.z]`; `logs report` does not file an
 * error that is fixed in a newer version than the one running (unless `--force`) and notes a
 * possible regression when the running version already has the fix. The maintainer adds entries
 * with `npm run known-fix -- add …` (`scripts/known-fix.mjs`, which uses {@link validateKnownFixes});
 * `.github/workflows/error-triage.yml` reads the same file to comment on new error reports.
 *
 * Reading never throws: a missing or unreadable table is empty, and an entry that does not
 * validate is left out (a broken table must never hide a report).
 */

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/** The shipped table. */
export const KNOWN_FIXES_FILE = path.join(path.dirname(fileURLToPath(import.meta.url)), 'known-fixes.json');

/** A summary longer than this is refused (it is one plain line). */
export const SUMMARY_MAX_CHARS = 200;

const FP_RE = /^[0-9a-f]{12}$/;
const VERSION_RE = /^(0|[1-9]\d{0,5})\.(0|[1-9]\d{0,5})\.(0|[1-9]\d{0,5})$/;
const KEYS = Object.freeze(['fp', 'fixed_in', 'summary', 'issue']);

/** @typedef {{fp: string, fixed_in: string, summary: string, issue: number|null}} KnownFix */

/**
 * Compare two semver versions: `x.y.z`, then a pre-release is older than its release
 * (`1.2.0-beta.1` < `1.2.0`) and pre-releases compare part by part (numbers as numbers, numbers
 * before words). Build metadata (`+…`) is ignored.
 * @param {string} a @param {string} b
 * @returns {number|null} >0 when a is newer, <0 when older, 0 when equal; null when unreadable.
 */
export function compareVersions(a, b) {
  const re = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/;
  const pa = typeof a === 'string' ? re.exec(a.trim()) : null;
  const pb = typeof b === 'string' ? re.exec(b.trim()) : null;
  if (!pa || !pb) return null;
  for (let i = 1; i <= 3; i += 1) {
    const d = Number(pa[i]) - Number(pb[i]);
    if (d !== 0) return d;
  }
  if (pa[4] === undefined || pb[4] === undefined) return pa[4] === pb[4] ? 0 : pa[4] === undefined ? 1 : -1;
  const xa = pa[4].split('.');
  const xb = pb[4].split('.');
  for (let i = 0; i < Math.max(xa.length, xb.length); i += 1) {
    if (xa[i] === undefined) return -1;
    if (xb[i] === undefined) return 1;
    const na = /^\d+$/.test(xa[i]);
    const nb = /^\d+$/.test(xb[i]);
    if (na && nb) {
      const d = Number(xa[i]) - Number(xb[i]);
      if (d !== 0) return d;
    } else if (na !== nb) {
      return na ? -1 : 1;
    } else if (xa[i] !== xb[i]) {
      return xa[i] < xb[i] ? -1 : 1;
    }
  }
  return 0;
}

/**
 * The problems of one entry, each naming the key only (never its value).
 * @param {unknown} e
 * @returns {string[]} empty when the entry is valid.
 */
export function entryProblems(e) {
  if (e === null || typeof e !== 'object' || Array.isArray(e)) return ['not an object'];
  const o = /** @type {Record<string, unknown>} */ (e);
  const problems = [];
  for (const k of Object.keys(o)) if (!KEYS.includes(k)) problems.push(`unknown key ${JSON.stringify(k)}`);
  if (typeof o.fp !== 'string' || !FP_RE.test(o.fp)) problems.push('fp must be 12 lowercase hex characters');
  if (typeof o.fixed_in !== 'string' || !VERSION_RE.test(o.fixed_in)) problems.push('fixed_in must be a version x.y.z');
  if (typeof o.summary !== 'string' || o.summary.trim().length === 0) problems.push('summary must be a non-empty string');
  else if (o.summary !== o.summary.trim() || /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/.test(o.summary)) problems.push('summary must be one plain line (no line breaks, tabs, control or text-direction characters, or leading/trailing spaces)');
  else if ([...o.summary].length > SUMMARY_MAX_CHARS) problems.push(`summary must be at most ${SUMMARY_MAX_CHARS} characters`);
  if (!Object.hasOwn(o, 'issue') || !(o.issue === null || (Number.isSafeInteger(o.issue) && /** @type {number} */ (o.issue) > 0))) {
    problems.push('issue must be a positive issue number or null');
  }
  return problems;
}

/**
 * Validate a whole table: an array of valid entries, each fingerprint once.
 * @param {unknown} data
 * @returns {string[]} the problems (`entry 2: fp must be …`); empty when the table is valid.
 */
export function validateKnownFixes(data) {
  if (!Array.isArray(data)) return ['the table must be a JSON array'];
  const problems = [];
  /** @type {Map<string, number>} */
  const seen = new Map();
  data.forEach((e, i) => {
    for (const p of entryProblems(e)) problems.push(`entry ${i + 1}: ${p}`);
    const fp = /** @type {any} */ (e)?.fp;
    if (typeof fp === 'string' && FP_RE.test(fp)) {
      if (seen.has(fp)) problems.push(`entry ${i + 1}: fp is already listed in entry ${seen.get(fp)}`);
      else seen.set(fp, i + 1);
    }
  });
  return problems;
}

/**
 * The valid entries of the table at `file`, by fingerprint (first one wins). Never throws.
 * @param {string} [file]
 * @returns {Map<string, KnownFix>}
 */
export function loadKnownFixes(file = KNOWN_FIXES_FILE) {
  let data;
  try {
    data = JSON.parse(readFileSync(file, 'utf8'));
  } catch {
    return new Map();
  }
  return knownFixMap(data);
}

/**
 * The valid entries of `data`, by fingerprint (first one wins); anything else is left out.
 * @param {unknown} data
 * @returns {Map<string, KnownFix>}
 */
export function knownFixMap(data) {
  /** @type {Map<string, KnownFix>} */
  const out = new Map();
  if (!Array.isArray(data)) return out;
  for (const e of data) {
    if (entryProblems(e).length > 0 || out.has(e.fp)) continue;
    out.set(e.fp, { fp: e.fp, fixed_in: e.fixed_in, summary: e.summary, issue: e.issue });
  }
  return out;
}

/**
 * How a known fix relates to the running version: `older` (the fix is in a newer version than the
 * one running), `has-fix` (the running version is the fixed one or newer: a possible regression),
 * or `unknown` (the running version cannot be read).
 * @param {KnownFix} fix @param {string} running
 * @returns {'older'|'has-fix'|'unknown'}
 */
export function fixStatus(fix, running) {
  const c = compareVersions(running, fix.fixed_in);
  if (c === null) return 'unknown';
  return c < 0 ? 'older' : 'has-fix';
}
