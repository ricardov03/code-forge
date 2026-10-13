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
 *   - Fail closed: an entry the validator refuses is never run (`runner-invalid`); a `junit`
 *     `{test_filter}` with no `::case`, or a test path or case that starts with `-`, is a usage error.
 *   - The env is the test env without any inherited `GIT_*` variable, plus the entry's variables. Values of 8 or more characters are
 *     masked in every printed line; only the names are shown (`env_keys`).
 *
 * The `swift` adapter (plan §1.3, B88) differs from `junit` in three ways:
 *   - `{test_filter}` with no `::case` is the file's base name without `.swift` (an XCTest class
 *     or Swift Testing suite named after its file), regex-escaped and anchored as `\.<name>/`:
 *     `swift test --filter` searches `Module.Suite/test` without anchors, so the bare name would
 *     also select `BarFooTests` and `FooTestsHelpers`; with a case it is the case, regex-escaped,
 *     with `(?:^|[./])` before it and `(?![^/])` after it (`swiftCaseFilter`);
 *   - every `*.xml` in the step's report dir is read: `swift test --xunit-output <dir>/report.xml`
 *     writes the XCTest report there (SwiftPM 6.3 writes it only with `--parallel`) and the Swift
 *     Testing report next to it as `report-swift-testing.xml`; a failure in either counts; a
 *     sibling without the main report is `RED_INVALID error` (`--parallel` missing) and carries
 *     no failed data; GREEN needs every report RED had;
 *   - a failed run that wrote no report and printed a `swiftc` error is `RED_INVALID compile`
 *     (`swiftCompileError`): a test of an API that does not exist yet needs the owner's waiver.
 */

import { mkdtemp, readdir, rm } from 'node:fs/promises';
import path from 'node:path';
import { RUNNER_ADAPTER_TOKENS, runnerMessages } from '../config/validate.mjs';
import { MASK_MIN_LENGTH } from '../gates/expand.mjs';
import { StateError } from '../state/paths.mjs';
import { globMatch } from '../state/registry.mjs';
import { exec } from '../util/exec.mjs';
import { registerSecret } from '../util/redact.mjs';
import { currentRunRoot } from '../util/tmp.mjs';
import { classifyGreen, classifyReport, loadReport, swiftCompileError } from './junit.mjs';

/** `proof.runners[].timeout_s` when an entry leaves it out (the schema default). */
export const DEFAULT_RUNNER_TIMEOUT_S = 600;
/** The report file name inside a step's dir. */
const REPORT_NAME = 'report.xml';

/**
 * B88: the report files a swift RED step wrote, keyed by its result. GREEN needs each of them
 * again: a report that RED had and GREEN lacks is NOT_GREEN, never skipped.
 * @type {WeakMap<object, string[]>}
 */
const SWIFT_RED_REPORTS = new WeakMap();
/** B88: the swift step wrote the Swift Testing report but not the XCTest one at `{report}`. */
export const SWIFT_NO_MAIN_REPORT =
  'swift test wrote a Swift Testing report but no XCTest report at {report}: keep --parallel in the runner argv (SwiftPM 6.3 writes the XCTest report only with it)';

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
 * B88: the `swift` adapter's `{test_filter}` when no `::case` is given: the file's base name without
 * `.swift` (an XCTest class or a Swift Testing suite named after its file), regex-escaped, with a
 * `.` before it and a `/` after it, so it matches the `Module.Suite/test` id of that suite only.
 * @param {string} file @returns {string}
 */
const swiftDefaultFilter = (file) => `\\.${regexEscape(path.posix.basename(file, '.swift'))}/`;

/**
 * B88: the `swift` adapter's `{test_filter}` for a `::case`: the case, regex-escaped, starting the
 * id or after a `.` or `/`, and ending it or followed by a `/`. The end is not `$`: SwiftPM 6.3.3
 * matches a Swift Testing test against an id with more after the test name, so `$` selects no
 * Swift Testing test (checked live; `\.Suite/test$` still selects an XCTest test). `Suite/capsHigh()` then never selects
 * `MoreSuite/capsHigh()`, and `testFoo` never selects `testFooBar`.
 * @param {string} testCase @returns {string}
 */
const swiftCaseFilter = (testCase) => `(?:^|[./])${regexEscape(testCase)}(?![^/])`;

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
    if (testCase === undefined && entry.adapter !== 'swift') throw new StateError('usage', `the ${entry.adapter} runner for ${file} filters by test case — give --test <file>::<case>`);
    if (testCase !== undefined && testCase.startsWith('-')) throw new StateError('usage', 'the test case starts with "-" (it would be read as an option)');
  }
  /** @type {Record<string, string>} */
  const values = {
    '{test_file}': file,
    '{test_id}': testCase === undefined ? file : `${file}::${testCase}`,
    '{test_filter}': testCase === undefined ? swiftDefaultFilter(file) : entry.adapter === 'swift' ? swiftCaseFilter(testCase) : regexEscape(testCase),
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
      if (entry.adapter !== 'swift') return { run, reports: [loadReport(report)], noMain: false, names: [] };
      const names = (await readdir(dir)).filter((name) => name.endsWith('.xml')).sort();
      // none written ⇒ the missing main report, read as NO_REPORT
      if (names.length === 0) return { run, reports: [loadReport(report)], noMain: false, names };
      // a sibling without the main report: unreadable, so it never proves anything
      const noMain = !names.includes(REPORT_NAME);
      return { run, reports: [...(noMain ? [{ unreadable: SWIFT_NO_MAIN_REPORT }] : []), ...names.map((name) => loadReport(path.join(dir, name)))], noMain, names };
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  };
  return {
    adapter: entry.adapter,
    env_keys: runnerEnvKeys(entry),
    red: async (step) => {
      const { run, reports, noMain, names } = await runStep(step);
      const parsed = classifyReport({ run, reports, ...(entry.adapter === 'swift' ? { compileError: swiftCompileError } : {}) });
      // an invalid RED carries no failed data: the sibling's failures never reach the row
      /** @type {import('./junit.mjs').JUnitRedParse} */
      const result = noMain ? { ...parsed, verdict: 'RED_INVALID', red_kind: 'error', failed: null, failed_tests: [], failed_ids: [], reason: SWIFT_NO_MAIN_REPORT } : parsed;
      if (entry.adapter === 'swift') SWIFT_RED_REPORTS.set(result, names);
      return result;
    },
    green: async ({ red, ...step }) => {
      const { run, reports, names } = await runStep(step);
      if (entry.adapter === 'swift') {
        // fail closed: a RED no swift hook classified, or a report RED had and GREEN lacks
        const had = SWIFT_RED_REPORTS.get(red);
        if (!had || had.some((name) => !names.includes(name))) return 'NOT_GREEN';
      }
      return classifyGreen({ run, reports, redFailed: red.failed_ids }).verdict;
    },
  };
}
