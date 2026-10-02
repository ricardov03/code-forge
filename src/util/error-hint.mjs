/**
 * The one-line hint after a logged failure (B28). `bin/code-forge.mjs` calls {@link maybeHint}
 * after the error log took a line; it prints {@link HINT_LINE} on stderr only when:
 *  - stderr is a terminal and no agent harness runs code-forge (agent mode: one of
 *    {@link AGENT_ENV_VARS}, the same names `src/install/agent-env.mjs` checks — copied here
 *    because the router may only load `src/util`; a test keeps the two lists equal);
 *  - the entry has a well-formed fingerprint (12 hex characters);
 *  - the exit is not 2 (a usage error says what to fix already) and the entry is no warning (B37);
 *  - the same fingerprint got no hint in the last 24 hours. The time of each hint is kept in
 *    `~/.code-forge/logs/hints.json` (`{"<fp>": "<ISO time>"}`, 0600, older ones dropped).
 * Never throws; a broken state file only means the hint may show again.
 */

import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';

export const HINT_LINE = 'code-forge: this error was saved to the local log. To send it to us: code-forge logs report\n';

/** A fingerprint: 12 lowercase hex characters. */
const FP = /^[0-9a-f]{12}$/;

/** At most one hint per fingerprint in this window. */
export const HINT_WINDOW_MS = 24 * 60 * 60 * 1000;

/** The agent-harness variables (same list and order as `src/install/agent-env.mjs`). */
export const AGENT_ENV_VARS = Object.freeze(['CLAUDECODE', 'CLAUDE_CODE', 'CURSOR_AGENT', 'AI_AGENT']);

/**
 * @param {string|null|undefined} home
 * @returns {string|null} the hint state file next to the error log, or null without a home.
 */
export function hintStatePath(home) {
  return typeof home === 'string' && home.length > 0 ? path.join(home, '.code-forge', 'logs', 'hints.json') : null;
}

/** @param {NodeJS.ProcessEnv} env */
function agentMode(env) {
  return AGENT_ENV_VARS.some((name) => typeof env[name] === 'string' && /** @type {string} */ (env[name]).length > 0);
}

/**
 * @param {object} o
 * @param {{fp?: string, kind?: string}|null} o.entry - the entry the log just took (null: nothing was logged).
 * @param {number} o.exit
 * @param {boolean} o.isTTY - whether stderr is a terminal.
 * @param {NodeJS.ProcessEnv} [o.env]
 * @param {Date} [o.now]
 * @param {{write: (s: string) => unknown}} [o.stderr]
 * @returns {Promise<boolean>} whether the hint was printed.
 */
export async function maybeHint(o) {
  try {
    const env = o.env ?? process.env;
    const fp = o.entry?.fp;
    if (o.entry?.kind === 'warning') return false;
    if (typeof fp !== 'string' || !FP.test(fp) || o.exit === 2 || o.isTTY !== true || agentMode(env)) return false;
    const file = hintStatePath(env.HOME);
    if (file === null) return false;
    const now = (o.now ?? new Date()).getTime();
    /** @type {Record<string, unknown>} */
    let state = {};
    try {
      const parsed = JSON.parse(await readFile(file, 'utf8'));
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) state = parsed;
    } catch {
      // no state yet, or a broken file: start over
    }
    const seen = state[fp];
    const last = typeof seen === 'string' ? Date.parse(seen) : Number.NaN;
    if (Number.isFinite(last) && now - last < HINT_WINDOW_MS && now >= last) return false;
    /** @type {Record<string, string>} */
    const kept = {};
    for (const [key, ts] of Object.entries(state)) {
      if (typeof ts !== 'string' || !FP.test(key)) continue;
      const t = Date.parse(ts);
      if (Number.isFinite(t) && now - t < HINT_WINDOW_MS && now >= t) kept[key] = ts;
    }
    kept[fp] = new Date(now).toISOString();
    (o.stderr ?? process.stderr).write(HINT_LINE);
    const tmp = `${file}.tmp-${process.pid}`;
    try {
      await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
      await writeFile(tmp, `${JSON.stringify(kept)}\n`, { encoding: 'utf8', mode: 0o600 });
      await rename(tmp, file);
    } catch {
      // the hint was shown; only the 24-hour memory is lost (and no temp file is left)
      await rm(tmp, { force: true }).catch(() => {});
    }
    return true;
  } catch {
    return false;
  }
}
