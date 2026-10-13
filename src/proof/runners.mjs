/**
 * `proof.runners` (issue #7 part 2, red-green plan §1.2, B87): how `proof red-green` runs a test
 * file that is not a node:test file, and reads its result from a JUnit XML report instead of the
 * console text.
 *
 * The list comes from the run snapshot only (`proofRunners`, never the live `.code-forge.yml`):
 * `run start` freezes it, `run reload` refuses to change it, and with it set no block may own
 * `.code-forge.yml`. A coder cannot change the command that proves its own work.
 *
 *   - Selection: the first entry whose `match` covers the `--test` file, in list order. No entry ⇒
 *     the caller uses the built-in node:test command, or refuses (`unsupported-test`).
 *   - Tokens: an argv item that is exactly `{test_file}`, `{test_id}`, `{test_filter}` or
 *     `{report}` is replaced; nothing else is (no shell, no substring expansion).
 *   - Each step (RED, GREEN) gets a fresh report dir under the run temp root, outside the work dir;
 *     the dir is removed once the report is read.
 *   - Fail closed: an entry the validator refuses is never run (`runner-invalid`); a `{test_filter}`
 *     with no `::case`, or a test path or case that starts with `-`, is a usage error.
 *   - The env is the test env without any inherited `GIT_*` variable, plus the entry's variables. Values of 8 or more characters are
 *     masked in every printed line; only the names are shown (`env_keys`).
 *
 * The `swift` adapter runs as `junit` in this block (B88 adds its default filter, the Swift
 * Testing report and compile-error detection).
 */

import { mkdtemp, rm } from 'node:fs/promises';
import path from 'node:path';
import { RUNNER_ADAPTER_TOKENS, runnerMessages } from '../config/validate.mjs';
import { MASK_MIN_LENGTH } from '../gates/expand.mjs';
import { StateError } from '../state/paths.mjs';
import { globMatch } from '../state/registry.mjs';
import { exec } from '../util/exec.mjs';
import { registerSecret } from '../util/redact.mjs';
import { currentRunRoot } from '../util/tmp.mjs';
import { classifyGreen, classifyReport, loadReport } from './junit.mjs';

/** `proof.runners[].timeout_s` when an entry leaves it out (the schema default). */
export const DEFAULT_RUNNER_TIMEOUT_S = 600;
/** The report file name inside a step's dir. */
const REPORT_NAME = 'report.xml';

/**
 * @typedef {object} RunnerEntry
 * @property {string[]} match
 * @property {'junit' | 'swift'} adapter
 * @property {string[]} argv
 * @property {Record<string, string>} [env]
 * @property {number} [timeout_s]
 */

/**
 * @typedef {object} RunnerHook - what `runRedGreen` calls in place of its node:test steps.
 * @property {string} adapter
 * @property {string[]} env_keys - the entry's env variable names, sorted (never values)
 * @property {(step: {cwd: string, env: NodeJS.ProcessEnv, timeoutMs: number}) => Promise<import('./junit.mjs').JUnitRedParse>} red
 * @property {(step: {cwd: string, env: NodeJS.ProcessEnv, timeoutMs: number, red: import('./junit.mjs').JUnitRedParse}) => Promise<'GREEN' | 'NOT_GREEN'>} green
 */

/**
 * Check the snapshot list and every entry again (fail closed: the snapshot was checked at run
 * start, but a record is a file on disk). A value that is not a list, or an empty list, refuses
 * the proof too: never a fallback. The message names key paths only.
 * @param {unknown} runners
 * @returns {RunnerEntry[]}
 * @throws {StateError} `runner-invalid`
 */
function checkedRunners(runners) {
  if (!Array.isArray(runners)) throw new StateError('runner-invalid', "the run snapshot's proof.runners is not a list; no proof was run");
  if (runners.length === 0) throw new StateError('runner-invalid', "the run snapshot's proof.runners is an empty list; no proof was run");
  const problems = runners.flatMap((entry, i) => runnerMessages(entry, i));
  if (problems.length > 0) throw new StateError('runner-invalid', `the run snapshot's proof.runners is invalid (${problems.join('; ')}); no proof was run`);
  return /** @type {RunnerEntry[]} */ (runners);
}

/**
 * The first runner whose `match` covers `file`, or null.
 * @param {unknown} runners - the snapshot's `proof.runners`, or null without one
 * @param {string} file - repo-relative POSIX path
 * @returns {RunnerEntry | null}
 * @throws {StateError} `runner-invalid`
 */
export function selectRunner(runners, file) {
  if (runners === null) return null;
  return checkedRunners(runners).find((entry) => entry.match.some((glob) => globMatch(file, glob))) ?? null;
}

/**
 * Is `file` a test file of any configured runner? Such a file is never reverted as a source.
 * @param {unknown} runners @param {string} file
 * @returns {boolean}
 * @throws {StateError} `runner-invalid`
 */
export function runnerCovers(runners, file) {
  if (runners === null) return false;
  return checkedRunners(runners).some((entry) => entry.match.some((glob) => globMatch(file, glob)));
}

/** @param {string} s @returns {string} `s` with every regex metacharacter escaped */
const regexEscape = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/**
 * The entry's argv with its tokens replaced, for a report at `report`.
 * @param {RunnerEntry} entry @param {{file: string, testCase?: string, report: string}} values
 * @returns {string[]}
 * @throws {StateError} `usage`
 */
export function expandRunnerArgv(entry, { file, testCase, report }) {
  if (file.startsWith('-')) throw new StateError('usage', `the test path ${JSON.stringify(file)} starts with "-" (it would be read as an option)`);
  // one source of truth: the adapter's own token list (the validator refuses any other token)
  const known = RUNNER_ADAPTER_TOKENS[entry.adapter];
  const uses = (/** @type {string} */ token) => known.includes(token) && entry.argv.slice(1).includes(token);
  if (testCase === '') throw new StateError('usage', '--test <file>::<case> needs a case name after ::');
  if (uses('{test_filter}')) {
    if (testCase === undefined) throw new StateError('usage', `the ${entry.adapter} runner for ${file} filters by test case — give --test <file>::<case>`);
    if (testCase.startsWith('-')) throw new StateError('usage', 'the test case starts with "-" (it would be read as an option)');
  }
  /** @type {Record<string, string>} */
  const values = {
    '{test_file}': file,
    '{test_id}': testCase === undefined ? file : `${file}::${testCase}`,
    '{test_filter}': testCase === undefined ? '' : regexEscape(testCase),
    '{report}': report,
  };
  return entry.argv.map((item, i) => (i > 0 && known.includes(item) ? values[item] : item));
}

/** @param {RunnerEntry} entry @returns {string[]} the sorted env variable names (never values) */
export function runnerEnvKeys(entry) {
  return entry.env ? Object.keys(entry.env).sort() : [];
}

/**
 * The runner child's env: the test env with every inherited `GIT_*` variable removed (an inherited
 * `GIT_DIR` or `GIT_WORK_TREE` would point a runner's git at another repository than the work
 * dir), then the entry's own variables, which the owner set on purpose.
 * @param {NodeJS.ProcessEnv} env @param {RunnerEntry} entry @returns {NodeJS.ProcessEnv}
 */
export function runnerEnv(env, entry) {
  /** @type {NodeJS.ProcessEnv} */
  const out = {};
  for (const [key, value] of Object.entries(env)) if (!key.startsWith('GIT_')) out[key] = value;
  return { ...out, ...(entry.env ?? {}) };
}

/** @param {RunnerEntry} entry @returns {number} the step timeout in ms */
export function runnerTimeoutMs(entry) {
  return (entry.timeout_s ?? DEFAULT_RUNNER_TIMEOUT_S) * 1000;
}

/**
 * The adapter's precheck, run before the export is built and before any file is reverted. The
 * `junit` and `swift` adapters have none (B102 adds one for `xcodebuild`).
 * @param {RunnerEntry} _entry
 * @returns {Promise<void>}
 */
export async function adapterPrecheck(_entry) {}

/**
 * Validate the expansion up front (so a usage error changes nothing) and build the hook
 * `runRedGreen` calls for the RED and GREEN steps.
 * @param {RunnerEntry} entry @param {{file: string, testCase?: string}} test
 * @returns {RunnerHook}
 * @throws {StateError} `usage`
 */
export function runnerHook(entry, { file, testCase }) {
  expandRunnerArgv(entry, { file, testCase, report: path.join(currentRunRoot(), REPORT_NAME) });
  // gate env is not a secret store, and neither is this; long values are still masked in output
  for (const value of Object.values(entry.env ?? {})) if (value.length >= MASK_MIN_LENGTH) registerSecret(value);

  /** @param {{cwd: string, env: NodeJS.ProcessEnv, timeoutMs: number}} step */
  const runStep = async ({ cwd, env, timeoutMs }) => {
    const dir = await mkdtemp(path.join(currentRunRoot(), 'proof-report-'));
    try {
      const report = path.join(dir, REPORT_NAME);
      const argv = expandRunnerArgv(entry, { file, testCase, report });
      const run = await exec(argv, { cwd, env: runnerEnv(env, entry), timeoutMs });
      return { run, reports: [loadReport(report)] };
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  };
  return {
    adapter: entry.adapter,
    env_keys: runnerEnvKeys(entry),
    red: async (step) => classifyReport(await runStep(step)),
    green: async ({ red, ...step }) => classifyGreen({ ...(await runStep(step)), redFailed: red.failed_ids }).verdict,
  };
}
