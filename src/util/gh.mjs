/**
 * Shared `gh` helpers (moved out of `cli/logs.mjs`, B27/B28, for B47's tracked waiver issues):
 * the secret check a body must pass before it leaves the machine, a PATH lookup, and the `gh`
 * calls — argv only, never a shell, never gh's output in a message (only `exit n`).
 *
 *  - {@link findSecret}: the first secret-shaped text in a body (rule name and line only).
 *  - {@link pathLookup}: whether a command is an executable on PATH.
 *  - {@link ghRun}: one `gh` call that never throws.
 *  - {@link ghWithBodyFile}: a `gh` call whose body goes through a private temp file (removed after).
 *  - {@link findReported}: an earlier issue holding a marker in its body.
 */

import { accessSync, constants as fsConstants, statSync } from 'node:fs';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { SECRET_LOOKING_PATTERNS } from '../config/secret-patterns.mjs';
import { exec as realExec } from './exec.mjs';
import { currentRunRoot } from './tmp.mjs';

/** Every `gh` call gets at most this long. */
export const GH_TIMEOUT_MS = 60_000;

/**
 * Secret shapes the final report is checked for, after scrubbing. A hit stops the report.
 * @type {ReadonlyArray<{name: string, re: RegExp}>}
 */
export const REPORT_SECRET_RULES = Object.freeze([
  { name: 'API key (sk-)', re: /(?<![A-Za-z0-9_-])sk-[A-Za-z0-9_-]{10,}/ },
  { name: 'GitHub token', re: /(?<![A-Za-z0-9_])(?:gh[opsur]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})/ },
  { name: 'Slack token', re: /(?<![A-Za-z0-9_-])xox[abprs]-[A-Za-z0-9-]{10,}/ },
  { name: 'AWS access key', re: /(?<![A-Za-z0-9])AKIA[0-9A-Z]{16}(?![0-9A-Z])/ },
  { name: 'private key', re: /-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----/ },
  { name: 'JWT', re: /(?<![A-Za-z0-9_-])eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}/ },
  // copied without g/y: `.test()` must carry no `lastIndex` state between checks
  ...SECRET_LOOKING_PATTERNS.map((re) => ({ name: 'secret-shaped token', re: new RegExp(re.source, re.flags.replace(/[gy]/g, '')) })),
]);

/** @param {string} s @returns {number} Shannon entropy in bits per character */
export function entropy(s) {
  /** @type {Map<string, number>} */
  const freq = new Map();
  for (const ch of s) freq.set(ch, (freq.get(ch) ?? 0) + 1);
  let bits = 0;
  for (const n of freq.values()) {
    const p = n / s.length;
    bits -= p * Math.log2(p);
  }
  return bits;
}

/**
 * A 32+ character run of `[A-Za-z0-9+/_=-]` with at least two of upper/lower/digit and entropy of
 * at least 4 bits per character. A hex-only run (`0-9a-f`, either case) is never flagged here: a
 * hash using all 16 digits evenly reaches exactly 4 bits and would otherwise trip the rule; the
 * token-shape rules ({@link REPORT_SECRET_RULES}) still apply to it.
 * @param {string} line
 * @returns {boolean}
 */
function hasHighEntropyRun(line) {
  for (const m of line.matchAll(/[A-Za-z0-9+/_=-]{32,}/g)) {
    const run = m[0];
    if (/^[0-9a-f]+$/i.test(run)) continue; // a hash, not a token
    const classes = [/[A-Z]/, /[a-z]/, /[0-9]/].filter((re) => re.test(run)).length;
    if (classes >= 2 && entropy(run) >= 4) return true;
  }
  return false;
}

/**
 * The first secret-shaped text in `text` (rule name and 1-based line), or null. The matched text
 * itself is never returned.
 * @param {string} text
 * @returns {{rule: string, line: number}|null}
 */
export function findSecret(text) {
  const lines = text.split('\n');
  for (let i = 0; i < lines.length; i += 1) {
    for (const r of REPORT_SECRET_RULES) if (r.re.test(lines[i])) return { rule: r.name, line: i + 1 };
    if (hasHighEntropyRun(lines[i])) return { rule: 'high-entropy string', line: i + 1 };
  }
  return null;
}

/** @param {string} pathEnv @returns {(cmd: string) => boolean} */
export function pathLookup(pathEnv) {
  return (cmd) =>
    pathEnv.split(path.delimiter).some((dir) => {
      if (!dir) return false;
      try {
        const f = path.join(dir, cmd);
        accessSync(f, fsConstants.X_OK);
        return statSync(f).isFile();
      } catch {
        return false;
      }
    });
}

/**
 * One `gh` call; a throw from `exec` is a `failed` result with no output.
 * @param {typeof realExec} exec @param {string[]} argv @param {NodeJS.ProcessEnv} env
 * @param {{cwd?: string}} [opts]
 * @returns {Promise<{result: string, code: number|null, timedOut: boolean, stdout: string, stderr: string}>}
 */
export async function ghRun(exec, argv, env, opts = {}) {
  try {
    return await exec(argv, { env, timeoutMs: GH_TIMEOUT_MS, ...(opts.cwd ? { cwd: opts.cwd } : {}) });
  } catch {
    return { result: 'failed', code: null, timedOut: false, stdout: '', stderr: '' };
  }
}

/**
 * Write `text` to a private temp file and run the first argv of `argvsFor(file)`; when it fails
 * over a label, run the next one (once).
 * @param {typeof realExec} exec @param {NodeJS.ProcessEnv} env @param {string} text
 * @param {(file: string) => string[][]} argvsFor
 * @param {(stdout: string) => void} [onOk]
 * @returns {Promise<string|null>} null when gh succeeded; else why (`exit n`, never its output).
 */
export async function ghWithBodyFile(exec, env, text, argvsFor, onOk) {
  /** @type {string|null} */
  let dir = null;
  try {
    let res;
    try {
      const root = currentRunRoot();
      await mkdir(root, { recursive: true });
      dir = await mkdtemp(path.join(root, 'report-'));
      const bodyFile = path.join(dir, 'body.md');
      await writeFile(bodyFile, text, { encoding: 'utf8', mode: 0o600 });
      const [first, retry] = argvsFor(bodyFile);
      res = await ghRun(exec, first, env);
      // gh's stderr is matched to decide the retry, never printed
      if (res.result !== 'ok' && retry && /label/i.test(res.stderr)) res = await ghRun(exec, retry, env);
    } catch {
      return 'could not write the report file';
    }
    if (res.result !== 'ok') return res.code !== null ? `exit ${res.code}` : res.timedOut ? 'timed out' : 'did not finish';
    onOk?.(res.stdout);
    return null;
  } finally {
    if (dir !== null) await rm(dir, { recursive: true, force: true }).catch(() => {});
  }
}

/**
 * An earlier issue holding `needle` in its body (open or closed): `{hit}` (null when there is
 * none), or `{why}` when the search failed (`gh exit n`; never gh's output). With `opts.match`
 * the bodies are fetched too and a hit must contain that exact text (GitHub's search is fuzzy);
 * then a full page (`opts.limit` results, default 5) with no exact hit is `{why: 'too-many-results'}`
 * — the match may be on a page not read, so the caller must not assume there is none.
 * @param {typeof realExec} exec @param {NodeJS.ProcessEnv} env @param {string} repo @param {string} needle
 * @param {{match?: string, limit?: number}} [opts]
 * @returns {Promise<{hit: {number: number, url: string, state: string}|null} | {why: string}>}
 */
export async function findReported(exec, env, repo, needle, opts = {}) {
  const fields = opts.match === undefined ? 'number,url,state' : 'number,url,state,body';
  const limit = Number.isInteger(opts.limit) && /** @type {number} */ (opts.limit) > 0 ? /** @type {number} */ (opts.limit) : 5;
  const res = await ghRun(exec, ['gh', 'issue', 'list', '--repo', repo, '--state', 'all', '--search', `${needle} in:body`, '--json', fields, '--limit', String(limit)], env);
  if (res.result !== 'ok') return { why: res.code !== null ? `gh exit ${res.code}` : res.timedOut ? 'gh timed out' : 'gh did not finish' };
  let list;
  try {
    list = JSON.parse(res.stdout);
  } catch {
    return { why: 'gh gave no list' };
  }
  if (!Array.isArray(list)) return { why: 'gh gave no list' };
  const match = opts.match;
  const hit = list.find(
    (i) => Number.isSafeInteger(i?.number) && typeof i?.url === 'string' && /^https:\/\//.test(i.url) && (match === undefined || (typeof i.body === 'string' && i.body.includes(match))),
  );
  if (!hit && match !== undefined && list.length >= limit) return { why: 'too-many-results' };
  return { hit: hit ? { number: hit.number, url: hit.url, state: typeof hit.state === 'string' ? hit.state.toLowerCase() : 'unknown' } : null };
}
