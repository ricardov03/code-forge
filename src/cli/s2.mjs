/**
 * `code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <s>]` (plan §3.4;
 * block B9a). The packet file is `{question, context?, options?, s1?}`; the verb prints the S2
 * answer `{decision, confidence, reason, overrule, ask_human, human_question}` as one JSON line.
 * Exit codes: 0 answered, 1 failed or invalid answer, 2 usage, 3 L3 and every fallback
 * unavailable (S2 ⇒ stop and ask the human, §5.6).
 */

import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { runS2 } from '../session/s2.mjs';
import { runRootFor, SessionError } from '../session/spawn.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { writeSafe } from '../util/redact.mjs';
import { slugFor } from './run.mjs';
import { exitCodeFor } from './spawn.mjs';

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

const USAGE = 'usage: code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]\n';

/**
 * @param {string[]} args
 * @param {import('../session/spawn.mjs').SessionDeps & {stdout?: {write: (s: string) => unknown}}} [deps]
 * @returns {Promise<number>}
 */
export async function runS2Verb(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  try {
    const { flags, positionals } = parseFlags(args, { values: ['packet', 'run', 'block', 'timeout'] });
    if (positionals.length > 0 || typeof flags.packet !== 'string') {
      writeSafe(stderr, USAGE);
      return 2;
    }
    const loaded = await loadProjectConfig(process.cwd());
    if (!loaded.ok) {
      writeSafe(stderr, `s2: ${loaded.message}\n`);
      return 2;
    }
    const cfg = loaded.config;
    const timeout = intFlag(flags.timeout, 'timeout');
    const packet = await readJSONFlag(flags.packet, 'packet');
    const result = await runS2(
      {
        cfg,
        packet,
        run: typeof flags.run === 'string' ? flags.run : undefined,
        block: typeof flags.block === 'string' ? flags.block : undefined,
        runRoot: typeof flags.run === 'string' ? runRootFor(flags.run, cfg?.tmp?.root) : undefined,
        slug: slugFor(cfg, process.cwd()),
        timeoutMs: timeout === undefined ? undefined : timeout * 1000,
      },
      { ...deps, stderr },
    );
    if (result.status === 'ok') {
      writeSafe(stdout, `${JSON.stringify(result.answer)}\n`);
    } else {
      writeSafe(stderr, `s2: ${result.status}${result.reason ? ` (${result.reason})` : ''}\n`);
    }
    return exitCodeFor(result);
  } catch (err) {
    if (err instanceof StateError || err instanceof SessionError) {
      writeSafe(stderr, `s2: ${/** @type {Error} */ (err).message}\n`);
      return 2;
    }
    throw err;
  }
}

/** @param {string[]} args */
export default async function s2Verb(args) {
  return runS2Verb(args);
}
