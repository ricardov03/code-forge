/**
 * `code-forge spawn --level L<n> --role <role> --brief <file> [--schema <file>] [--cwd <dir>]
 *   [--run <id>] [--block <id>] [--timeout <s>] [--background]` (plan §5.2; block B9a).
 *
 * Runs one isolated session through `src/session/spawn.mjs` and prints its result as one JSON line
 * on stdout; the `level=… provider=… model=… effort=… fallback_step=…` line goes to stderr.
 * Exit codes: 0 ok / started, 1 failed (timeout, bad output, refused), 2 usage, 3 unavailable
 * (every step of the ladder reported unavailability).
 */

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { loadProjectConfig, projectRootFor, slugFor } from '../config/load.mjs';
import { ROLES, runRootFor, SessionError, spawnSession } from '../session/spawn.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { writeSafe } from '../util/redact.mjs';

/**
 * @param {string} file @param {string} flag
 * @returns {Promise<any>}
 * @throws {SessionError} `usage` naming the flag only — never the file's content.
 */
async function readJSONFlag(file, flag) {
  let text;
  try {
    text = await readFile(path.resolve(file), 'utf8');
  } catch {
    throw new SessionError('usage', `--${flag} cannot be read`);
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new SessionError('usage', `--${flag} is not a valid JSON file`);
  }
}

const USAGE = `usage: code-forge spawn --level L0|L1|L2|L3 --role ${ROLES.join('|')} --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <seconds>] [--background]\n`;

/**
 * @param {{status: string}} result
 * @returns {number}
 */
export function exitCodeFor(result) {
  if (result.status === 'ok' || result.status === 'started') return 0;
  return result.status === 'unavailable' ? 3 : 1;
}

/**
 * @param {string[]} args
 * @param {import('../session/spawn.mjs').SessionDeps & {stdout?: {write: (s: string) => unknown}}} [deps]
 * @returns {Promise<number>}
 */
export async function runSpawn(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  try {
    const { flags, positionals } = parseFlags(args, { values: ['level', 'role', 'brief', 'schema', 'cwd', 'run', 'block', 'timeout'], booleans: ['background'] });
    if (positionals.length > 0 || typeof flags.level !== 'string' || typeof flags.role !== 'string' || typeof flags.brief !== 'string') {
      writeSafe(stderr, USAGE);
      return 2;
    }
    // B50: the project root's config and ledger slug, also from a subfolder
    const root = projectRootFor(process.cwd());
    const loaded = await loadProjectConfig(root);
    if (!loaded.ok) {
      writeSafe(stderr, `spawn: ${loaded.message}\n`);
      return 2;
    }
    const cfg = loaded.config;
    const timeout = intFlag(flags.timeout, 'timeout');
    const schema = typeof flags.schema === 'string' ? await readJSONFlag(flags.schema, 'schema') : undefined;
    const result = await spawnSession(
      {
        cfg,
        level: /** @type {any} */ (flags.level),
        role: /** @type {any} */ (flags.role),
        promptPath: path.resolve(flags.brief),
        cwd: typeof flags.cwd === 'string' ? path.resolve(flags.cwd) : undefined,
        schema,
        timeoutMs: timeout === undefined ? undefined : timeout * 1000,
        background: flags.background === true,
        runRoot: typeof flags.run === 'string' ? runRootFor(flags.run, cfg?.tmp?.root) : undefined,
        run: typeof flags.run === 'string' ? flags.run : undefined,
        block: typeof flags.block === 'string' ? flags.block : undefined,
        slug: slugFor(cfg, root),
      },
      { ...deps, stderr },
    );
    const { text: _text, ...shown } = /** @type {Record<string, any>} */ (result);
    writeSafe(stdout, `${JSON.stringify(shown)}\n`);
    return exitCodeFor(result);
  } catch (err) {
    if (err instanceof StateError || err instanceof SessionError) {
      writeSafe(stderr, `spawn: ${/** @type {Error} */ (err).message}\n`);
      return err instanceof SessionError && err.code === 'forbidden' ? 1 : 2;
    }
    throw err;
  }
}

/** @param {string[]} args */
export default async function spawnVerb(args) {
  return runSpawn(args);
}
