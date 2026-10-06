/**
 * `code-forge author --job plan|harden --brief <file> [--facts <file>] [--draft <file>]
 *   [--answers <file>] [--out <file>] [--run <id>] [--timeout <s>]` (plan §3.7, §3.8 rule 2;
 * block B9b). One author round: writes the draft to `--out` (default
 * `<project.plans_dir | plans>/<brief name>.<job>.md`) and prints `{draft, questions, cost}` as one
 * JSON line (`draft` is the path). The orchestrator shows the questions to the human and calls the
 * verb again with `--draft` and `--answers`.
 * Exit codes: 0 answered, 1 failed or invalid answer, 2 usage / no facts sheet / stale sheet,
 * 3 L3 and every fallback unavailable.
 */

import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { loadProjectConfig, projectRootFor, slugFor } from '../config/load.mjs';
import { AuthorError, runAuthor } from '../session/author.mjs';
import { runRootFor, SessionError } from '../session/spawn.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { writeSafe } from '../util/redact.mjs';
import { exitCodeFor } from './spawn.mjs';

const USAGE = 'usage: code-forge author --job plan|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]\n';

/**
 * @param {string[]} args
 * @param {import('../session/spawn.mjs').SessionDeps & {stdout?: {write: (s: string) => unknown}, cwd?: string}} [deps]
 * @returns {Promise<number>}
 */
export async function runAuthorVerb(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const cwd = deps.cwd ?? process.cwd();
  try {
    const { flags, positionals } = parseFlags(args, { values: ['job', 'brief', 'facts', 'draft', 'answers', 'out', 'run', 'timeout'] });
    if (positionals.length > 0 || typeof flags.job !== 'string' || typeof flags.brief !== 'string') {
      writeSafe(stderr, USAGE);
      return 2;
    }
    /** @param {string | string[] | true | undefined} v */
    const abs = (v) => (typeof v === 'string' ? path.resolve(cwd, v) : undefined);
    const briefPath = /** @type {string} */ (abs(flags.brief));
    // B50: the project root's config and ledger slug, also from a subfolder; flag paths stay
    // cwd-relative, and the default output goes to <root>/<plans_dir>
    const root = projectRootFor(cwd);
    const loaded = await loadProjectConfig(root);
    if (!loaded.ok) {
      writeSafe(stderr, `author: ${loaded.message}\n`);
      return 2;
    }
    const cfg = /** @type {Record<string, any>} */ (loaded.config);
    const timeout = intFlag(flags.timeout, 'timeout');
    const run = typeof flags.run === 'string' ? flags.run : undefined;
    const result = await runAuthor(
      {
        cfg,
        job: flags.job,
        briefPath,
        factsPath: abs(flags.facts),
        draftPath: abs(flags.draft),
        answersPath: abs(flags.answers),
        run,
        runRoot: run ? runRootFor(run, cfg?.tmp?.root) : undefined,
        slug: slugFor(cfg, root),
        timeoutMs: timeout === undefined ? undefined : timeout * 1000,
      },
      { ...deps, stderr },
    );
    if (result.status !== 'ok') {
      writeSafe(stderr, `author: ${result.status}${result.reason ? ` (${result.reason})` : ''}\n`);
      return exitCodeFor(result);
    }
    const out = abs(flags.out) ?? path.join(path.resolve(root, cfg.project?.plans_dir ?? 'plans'), `${path.basename(briefPath, path.extname(briefPath))}.${flags.job}.md`);
    mkdirSync(path.dirname(out), { recursive: true });
    writeFileSync(out, /** @type {string} */ (result.draft));
    writeSafe(stdout, `${JSON.stringify({ draft: out, questions: result.questions, cost: result.cost })}\n`);
    return 0;
  } catch (err) {
    if (err instanceof AuthorError || err instanceof StateError || err instanceof SessionError) {
      writeSafe(stderr, `author: ${/** @type {Error} */ (err).message}\n`);
      return 2;
    }
    throw err;
  }
}

/** @param {string[]} args */
export default async function authorVerb(args) {
  return runAuthorVerb(args);
}
