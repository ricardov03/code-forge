/**
 * `code-forge facts --brief <file> [--sources <path>…] [--out <file>] [--run <id>] [--timeout <s>]`
 * (plan §3.8 step 1–3; block B9b). Builds the facts sheet: claim tokens, one L0 read-only delegate
 * session (run in a read-only snapshot of the project at HEAD plus the in-project `--sources`,
 * B9b.1), validation, render. Default `--out`: `<project.plans_dir | plans>/<brief name>.facts.md`.
 * Exit codes: 0 written, 1 the session failed, the answer was refused, a file vanished mid-run or
 * the run raised a `SessionError`, 2 usage (only pre-run argument errors: flags, config, an
 * unreadable `--brief` / `--sources`, a bad `--run` id), 3 L0 and every fallback unavailable
 * (`exitCodeFor`).
 */

import { accessSync, constants, statSync } from 'node:fs';
import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { buildFacts, FactsError } from '../session/facts.mjs';
import { runRootFor, SessionError } from '../session/spawn.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { writeSafe } from '../util/redact.mjs';
import { slugFor } from './run.mjs';
import { exitCodeFor } from './spawn.mjs';

/**
 * @param {string} file @param {{fileOnly?: boolean}} [opts] - `fileOnly`: a directory does not count.
 * @returns {boolean} a readable file (or directory, unless `fileOnly`).
 */
function readable(file, { fileOnly = false } = {}) {
  try {
    accessSync(file, constants.R_OK);
    const st = statSync(file);
    return fileOnly ? st.isFile() : st.isFile() || st.isDirectory();
  } catch {
    return false;
  }
}

const USAGE = 'usage: code-forge facts --brief <file> [--sources <path>...] [--out <file>] [--run <id>] [--timeout <seconds>]\n';

/**
 * @param {string[]} args
 * @param {import('../session/spawn.mjs').SessionDeps & {stdout?: {write: (s: string) => unknown}, cwd?: string}} [deps]
 * @returns {Promise<number>}
 */
export async function runFactsVerb(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const cwd = deps.cwd ?? process.cwd();
  try {
    const { flags, positionals } = parseFlags(args, { values: ['brief', 'out', 'run', 'timeout'], multi: ['sources'] });
    if (positionals.length > 0 || typeof flags.brief !== 'string') {
      writeSafe(stderr, USAGE);
      return 2;
    }
    const loaded = await loadProjectConfig(cwd);
    if (!loaded.ok) {
      writeSafe(stderr, `facts: ${loaded.message}\n`);
      return 2;
    }
    const cfg = /** @type {Record<string, any>} */ (loaded.config);
    const briefPath = path.resolve(cwd, flags.brief);
    const plansDir = path.resolve(cwd, cfg.project?.plans_dir ?? 'plans');
    const outPath = typeof flags.out === 'string' ? path.resolve(cwd, flags.out) : path.join(plansDir, `${path.basename(briefPath, path.extname(briefPath))}.facts.md`);
    const sources = Array.isArray(flags.sources) ? flags.sources.map((s) => path.resolve(cwd, s)) : [];
    // --brief must be a regular file; a --sources path may be a file or a directory
    if (!readable(briefPath, { fileOnly: true })) {
      writeSafe(stderr, 'facts: --brief cannot be read\n');
      return 2;
    }
    for (const source of sources) {
      if (!readable(source)) {
        writeSafe(stderr, 'facts: --sources path cannot be read\n');
        return 2;
      }
    }
    const timeout = intFlag(flags.timeout, 'timeout');
    const run = typeof flags.run === 'string' ? flags.run : undefined;
    // pre-run argument checks: a bad --run id or an unusable project state is usage (exit 2)
    const runRoot = run ? runRootFor(run, cfg?.tmp?.root) : undefined;
    const slug = slugFor(cfg, cwd);
    let result;
    try {
      result = await buildFacts(
        { cfg, briefPath, sources, outPath, run, runRoot, slug, projectDir: cwd, timeoutMs: timeout === undefined ? undefined : timeout * 1000 },
        { ...deps, stderr },
      );
    } catch (err) {
      if (err instanceof FactsError) {
        writeSafe(stderr, `facts: ${err.message}\n`);
        return 1;
      }
      if (err instanceof SessionError) {
        // raised by the run, not by the arguments: a session failure (3 only when it says unavailable)
        writeSafe(stderr, `facts: ${err.message}\n`);
        return exitCodeFor({ status: err.code === 'unavailable' ? 'unavailable' : 'failed' });
      }
      const e = /** @type {NodeJS.ErrnoException} */ (err);
      if (e.code === 'ENOENT' || e.code === 'EACCES' || e.code === 'EISDIR') {
        // the inputs were readable files a moment ago (checked above): this is a run failure, not usage
        const kind = e.path === briefPath ? 'the brief' : sources.includes(e.path ?? '') ? 'a source' : 'a working file';
        writeSafe(stderr, `facts: ${kind} could not be read while building the sheet (${e.code})\n`);
        return 1;
      }
      throw err;
    }
    if (result.status !== 'ok') {
      writeSafe(stderr, `facts: ${result.status}${result.reason ? ` (${result.reason})` : ''}\n`);
      return exitCodeFor(result);
    }
    const facts = result.facts ?? [];
    writeSafe(stdout, `${JSON.stringify({ out: outPath, claims: result.claims.length, verified: facts.filter((f) => f.tag === 'VERIFIED').length })}\n`);
    return 0;
  } catch (err) {
    if (err instanceof StateError || err instanceof SessionError) {
      writeSafe(stderr, `facts: ${/** @type {Error} */ (err).message}\n`);
      return 2;
    }
    throw err;
  }
}

/** @param {string[]} args */
export default async function factsVerb(args) {
  return runFactsVerb(args);
}
