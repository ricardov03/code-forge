/**
 * `code-forge spawn --level L<n> --role <role> --brief <file> [--schema <file>] [--cwd <dir>]
 *   [--run <id>] [--block <id>] [--timeout <s>] [--background]` (plan §5.2; block B9a).
 *
 * Runs one isolated session through `src/session/spawn.mjs` and prints its result as one JSON line
 * on stdout; the `level=… provider=… model=… effort=… fallback_step=…` line goes to stderr.
 * Exit codes: 0 ok / started, 1 failed (timeout, bad output, refused), 2 usage, 3 unavailable
 * (every step of the ladder reported unavailability).
 *
 * B93: with `--role coder --run <id>` of a recorded run, `coder.sandbox` in the live config must
 * equal the run-start config snapshot's: a value that differs refuses the session
 * (`sandbox-changed`, exit 1) before anything is spawned. Other roles never read the key and are
 * not checked. A record from before snapshots, a run id with no record, or no `--run` uses the
 * live config. A record that cannot be read is `run-unreadable` (exit 2).
 */

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { loadProjectConfig, projectRootFor, slugFor } from '../config/load.mjs';
import { ROLES, runRootFor, SessionError, spawnSession } from '../session/spawn.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun } from '../state/run.mjs';
import { canonicalJSON, coderSandbox } from '../state/config-snapshot.mjs';
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

/** The SessionError codes that are a refusal (exit 1), not a usage error (exit 2). */
const REFUSED_CODES = new Set(['forbidden', 'sandbox-changed', 'sandbox-dir-refused']);

/**
 * B93: for `--role coder --run <id>`, the live config, after checking that its `coder.sandbox`
 * equals the run-start snapshot's (the key is fixed for the run). The value is the same, so the
 * live config is what the session builds from. A run id with no record, or a record from before
 * snapshots (no run-start value), uses the live config unchecked.
 * @param {Record<string, any>} live @param {string} runId
 * @returns {Promise<Record<string, any>>}
 * @throws {SessionError} `sandbox-changed` when the live value differs from the snapshot's;
 *   `run-unreadable` when the run record exists but cannot be read, parsed or understood.
 * @throws {StateError} `config-snapshot` when the snapshot in force does not match its hash.
 */
async function checkRunSandbox(live, runId) {
  let pinned;
  try {
    pinned = coderSandbox(await readRun(runId));
  } catch (err) {
    if (err instanceof StateError && err.code === 'no-run') return live; // no recorded run: the live config
    if (err instanceof StateError) throw err; // e.g. config-snapshot: named, exit 2
    throw new SessionError('run-unreadable', `run-unreadable: the record of run ${runId} cannot be read, so coder.sandbox cannot be checked against the run start`);
  }
  if (!pinned.snapshot) return live; // a run from before snapshots: no run-start value to hold to
  if (canonicalJSON(pinned.sandbox ?? null) !== canonicalJSON(live?.coder?.sandbox ?? null)) {
    throw new SessionError('sandbox-changed', `sandbox-changed: coder.sandbox in the live config differs from the run ${runId} snapshot — put it back, or end the run and start a new one`);
  }
  return live;
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
    const timeout = intFlag(flags.timeout, 'timeout');
    const schema = typeof flags.schema === 'string' ? await readJSONFlag(flags.schema, 'schema') : undefined;
    // the run id is checked (and its temp root made) before the run record is read
    const runRootDir = typeof flags.run === 'string' ? runRootFor(flags.run, loaded.config?.tmp?.root) : undefined;
    // B93: only a coder reads coder.sandbox, so only a coder session is held to the run-start value
    const cfg = typeof flags.run === 'string' && flags.role === 'coder' ? await checkRunSandbox(loaded.config, flags.run) : loaded.config;
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
        runRoot: runRootDir,
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
      return err instanceof SessionError && REFUSED_CODES.has(err.code) ? 1 : 2;
    }
    throw err;
  }
}

/** @param {string[]} args */
export default async function spawnVerb(args) {
  return runSpawn(args);
}
