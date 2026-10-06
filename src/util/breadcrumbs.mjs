/**
 * Breadcrumbs (B37): the names of the last commands run, for the error log's `before` field.
 *
 * `bin/code-forge.mjs` calls {@link noteCommand} once per known verb, before it runs. It keeps the
 * last {@link BREADCRUMB_MAX} commands in `~/.code-forge/logs/breadcrumbs.json` (a JSON array of
 * strings, oldest first; folder 0700, file 0600, written to a temp file and renamed). A command is
 * the verb name plus its subcommand word only when that word is one of the verb's own words
 * (`keys test`, `init`): never a flag, a flag value or any other positional — the same rule as the
 * error log's `sub`. The list as it was BEFORE this command is kept in memory
 * ({@link previousCommands}), so an error or warning logged by this process names the commands
 * that came before it.
 *
 * Best effort: never throws, never changes the verb's outcome. `CODE_FORGE_NO_ERROR_LOG=1` turns it
 * off (checked here too: nothing is read or written); no HOME means no file. A file that does not
 * parse, or entries that are not command names, are dropped. An existing logs folder is set to
 * 0700 (best effort).
 */

import { chmod, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';

/**
 * The subcommand words each verb knows (the error log's `sub` uses the same table); a word is
 * recorded only when it is one of these (anything else could be a user value: a path, an id, a name).
 * @type {Readonly<Record<string, readonly string[]>>}
 */
export const SUBCOMMANDS = Object.freeze({
  autopilot: ['start', 'status', 'stop'],
  block: ['open', 'attempt', 'rebase', 'claim', 'close', 'stop', 'waive'],
  gates: ['detect', 'run', 'secret-scan', 'safe-edit', 'scope', 'acceptance', 'transcript-grep'],
  jev: ['ask'],
  keys: ['list', 'set', 'test', 'remove'],
  ledger: ['calibration', 'outcome', 'tail'],
  logs: ['summary', 'clear', 'path', 'report'],
  plan: ['check'],
  proof: ['tier', 'export', 'lock', 'unlock', 'restore', 'red-green'],
  run: ['start', 'status', 'end'],
  tools: ['install'],
});

/** The opt-out variable (the same one the error log reads). */
const NO_LOG_ENV = 'CODE_FORGE_NO_ERROR_LOG';

/** @param {NodeJS.ProcessEnv} env @returns {boolean} */
function off(env) {
  const flag = env[NO_LOG_ENV];
  return typeof flag === 'string' && flag !== '' && flag !== '0';
}

/** How many commands the file keeps. */
export const BREADCRUMB_MAX = 5;

/** A command name: a verb, optionally one subcommand word (`keys test`). */
export const CRUMB = /^[a-z][a-z0-9-]{0,39}(?: [a-z][a-z0-9-]{0,39})?$/;

/** @type {{verb: string, sub: string|null, before: string[]} | null} */
let current = null;

/**
 * @param {string|null|undefined} home
 * @returns {string|null} the breadcrumb file, or null without a home.
 */
export function breadcrumbPath(home) {
  return typeof home === 'string' && home.length > 0 ? path.join(home, '.code-forge', 'logs', 'breadcrumbs.json') : null;
}

/**
 * The commands in the file, oldest first: only command-shaped strings, at most the last
 * {@link BREADCRUMB_MAX}; [] when there is no file or it does not parse.
 * @param {string|null} file
 * @returns {Promise<string[]>}
 */
export async function readBreadcrumbs(file) {
  if (file === null) return [];
  try {
    const parsed = JSON.parse(await readFile(file, 'utf8'));
    return cleanList(parsed);
  } catch {
    return [];
  }
}

/**
 * @param {unknown} list
 * @returns {string[]} the command-shaped strings of `list`, the last {@link BREADCRUMB_MAX}.
 */
export function cleanList(list) {
  if (!Array.isArray(list)) return [];
  return list.filter((c) => typeof c === 'string' && CRUMB.test(c)).slice(-BREADCRUMB_MAX);
}

/**
 * Record one command (called by the router before the verb runs). Never throws.
 * @param {object} o
 * @param {string} o.verb - a known verb name.
 * @param {string|null} o.sub - its subcommand word; dropped unless it is one of the verb's own
 *   words in {@link SUBCOMMANDS}.
 * @param {NodeJS.ProcessEnv} [o.env]
 * @returns {Promise<boolean>} whether the file was written.
 */
export async function noteCommand({ verb, sub, env = process.env }) {
  try {
    if (typeof verb !== 'string' || !CRUMB.test(verb)) {
      current = null;
      return false;
    }
    const known = Object.hasOwn(SUBCOMMANDS, verb) ? SUBCOMMANDS[verb] : [];
    const word = typeof sub === 'string' && known.includes(sub) ? sub : null;
    const crumb = word ? `${verb} ${word}` : verb;
    current = { verb, sub: word, before: [] };
    if (off(env)) return false;
    const file = breadcrumbPath(env.HOME);
    if (file === null) return false;
    const before = await readBreadcrumbs(file);
    current.before = before;
    const next = [...before, crumb].slice(-BREADCRUMB_MAX);
    const tmp = `${file}.tmp-${process.pid}`;
    try {
      await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
      await chmod(path.dirname(file), 0o700).catch(() => {}); // an older folder may be 0755
      await writeFile(tmp, `${JSON.stringify(next)}\n`, { encoding: 'utf8', mode: 0o600 });
      await rename(tmp, file);
      return true;
    } catch {
      await rm(tmp, { force: true }).catch(() => {});
      return false;
    }
  } catch {
    return false;
  }
}

/** @returns {string[]} the commands run before this one (oldest first); [] when none was noted. */
export function previousCommands() {
  return current ? [...current.before] : [];
}

/** @returns {{verb: string, sub: string|null} | null} the command this process runs, when noted. */
export function currentCommand() {
  return current ? { verb: current.verb, sub: current.sub } : null;
}

/** Forget the noted command (tests). */
export function resetBreadcrumbs() {
  current = null;
}
