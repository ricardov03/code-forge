/**
 * `xcresult` (red-green plan §1.6, B102): read the result bundle of an `xcodebuild test` run and
 * classify the RED and GREEN steps of the `xcodebuild` runner adapter from it.
 *
 * The bundle is read with `xcrun xcresulttool get test-results tests --path <bundle>` (argv only,
 * 60 s timeout, output capped at 8 MiB), and its JSON is mapped to the common `{tests}` shape of
 * `junit.mjs`. The node shape is pinned by real captured output (Xcode 26.6, `test/fixtures/xcresult/`):
 *
 *   {testNodes: [Test Plan → Unit test bundle | UI test bundle → Test Suite (→ Test Suite)…
 *                → Test Case → Failure Message]}
 *
 * A Test Case carries `nodeIdentifier` (`ClampXCTests/testCapsHigh()` for XCTest,
 * `ClampSwiftTests/capsHigh()` for Swift Testing) and `result` (`Passed`, `Failed`, `Skipped`). Its
 * id is `<bundle name>/<nodeIdentifier>`, e.g. `KitTests/ClampXCTests/testCapsHigh()`. A Test Plan
 * with no children (a filter that selected nothing) is zero tests.
 *
 * Fail closed: no bundle, a bundle that is not a real directory, a failed or timed-out
 * `xcresulttool`, output over 8 MiB, JSON that does not parse, two tests with the same id, and a
 * failed check of what this reader uses make the result unreadable. It checks every node's
 * `nodeType` (a known type) and its parent type (a known place), a Test Case's `result` (a known
 * result) and `nodeIdentifier` (a non-empty string), a bundle's `name` (non-empty, no `/`), a
 * Failure Message's `name` (a string, no children) and every `children` (a list). Any other key of a
 * node or of the document (`devices`, `testPlanConfigurations`, `duration`, …) is not read and is
 * ignored. An unreadable result never proves anything (RED is `RED_INVALID error`, GREEN is
 * `NOT_GREEN`). Today
 * that includes these xcresulttool node types: a parameterized Swift Testing test (`Arguments`),
 * repetitions (`Repetition`, `Test Case Run`), a multi-destination or multi-configuration run
 * (`Device`, `Test Plan Configuration`), `Runtime Warning`, `Source Code Reference`, `Attachment`,
 * `Expression`, `Test Value`, and the result `Expected Failure`.
 *
 * A Failure Message (`<file>.swift:<line>: <text>`, the prefix optional) is an assertion only for
 * these texts, as captured: `XCTAssert… failed` (but not `XCTAssert… failed: threw error`, a thrown
 * error inside the assertion), XCTFail's `failed` and `failed - <message>`, and Swift Testing's
 * `Expectation failed: …`. Every other text is an error: a thrown error (`failed: caught error: …`,
 * `Caught error: …`), `Issue recorded: …`, a crash. A failed test is an assertion only when every
 * one of its messages is one.
 */

import { lstat } from 'node:fs/promises';
import path from 'node:path';
import { exec } from '../util/exec.mjs';
import { MAX_REPORT_BYTES, NO_REPORT, classifyGreen, classifyReport, swiftCompileError } from './junit.mjs';

/** `xcresulttool` may run this long before it is stopped. */
export const XCRESULTTOOL_TIMEOUT_MS = 60_000;

/** Which parent node type each known node type may have (`null` = the `testNodes` list). */
const NODE_PARENTS = Object.freeze({
  'Test Plan': [null],
  'Unit test bundle': ['Test Plan'],
  'UI test bundle': ['Test Plan'],
  'Test Suite': ['Unit test bundle', 'UI test bundle', 'Test Suite'],
  'Test Case': ['Test Suite'],
  'Failure Message': ['Test Case'],
});

/** A Test Case `result` this reader knows, and its common status. */
const RESULT_STATUS = Object.freeze({ Passed: 'passed', Failed: 'failed', Skipped: 'skipped' });

/** The optional `<file>.swift:<line>: ` prefix of a Failure Message. */
const MESSAGE_PREFIX = /^[^\n:]+\.swift:\d+: /;
/** The assertion texts, after the prefix (see the header). */
const ASSERTION_MESSAGES = Object.freeze([/^XCTAssert\w* failed(?!: threw error)/, /^failed - /, /^failed$/, /^Expectation failed: /]);

// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;?]*[A-Za-z]/g;
/** `xcodebuild`'s own build-failure lines (`** BUILD FAILED **`, `** TEST BUILD FAILED **`). */
const BUILD_FAILED_LINE = /^\*\* (?:TEST )?BUILD FAILED \*\*[ \t\r]*$/m;
/** `xcodebuild test` (Xcode 26.6) on a test that does not compile prints this line, tab-indented, then `** TEST FAILED **`. */
const BUILD_CANCELLED_LINE = /^[ \t]*Testing cancelled because the build failed\.[ \t\r]*$/m;

/** @param {string} reason @returns {never} */
function unreadable(reason) {
  const err = new Error(`report-unreadable: ${reason}`);
  /** @type {any} */ (err).code = 'report-unreadable';
  throw err;
}

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);

/**
 * Is a Failure Message text an assertion failure?
 * @param {string} text @returns {'assertion' | 'error'}
 */
export function failureMessageKind(text) {
  const rest = String(text).replace(MESSAGE_PREFIX, '');
  return ASSERTION_MESSAGES.some((p) => p.test(rest)) ? 'assertion' : 'error';
}

/**
 * Read `xcresulttool get test-results tests` JSON.
 * @param {string} text
 * @returns {{tests: import('./junit.mjs').ReportTest[]}}
 * @throws {Error} `code: 'report-unreadable'`; the message names the reason only, never content.
 */
export function readXcresult(text) {
  if (typeof text !== 'string') unreadable('the xcresulttool output is not text');
  if (Buffer.byteLength(text, 'utf8') > MAX_REPORT_BYTES) unreadable('the xcresulttool output is over 8 MiB');
  let doc;
  try {
    doc = JSON.parse(text);
  } catch {
    unreadable('the xcresulttool output is not JSON');
  }
  if (!isObject(doc) || !Array.isArray(doc.testNodes)) unreadable('the xcresulttool output has no testNodes list');

  /** @type {import('./junit.mjs').ReportTest[]} */
  const tests = [];
  const seen = new Set();
  /** @param {unknown} node @param {string | null} parent @param {string | null} bundle */
  const walk = (node, parent, bundle) => {
    if (!isObject(node)) unreadable('a test node that is not an object');
    const type = node.nodeType;
    if (typeof type !== 'string' || !Object.hasOwn(NODE_PARENTS, type)) unreadable('a node type this reader does not know');
    if (!NODE_PARENTS[/** @type {keyof typeof NODE_PARENTS} */ (type)].includes(/** @type {any} */ (parent))) unreadable('a test node in the wrong place');
    const children = node.children ?? [];
    if (!Array.isArray(children)) unreadable('test node children that are not a list');
    if (type === 'Failure Message') {
      if (typeof node.name !== 'string') unreadable('a failure message without a text');
      if (children.length > 0) unreadable('a failure message with children');
      return;
    }
    let inBundle = bundle;
    if (type === 'Unit test bundle' || type === 'UI test bundle') {
      if (typeof node.name !== 'string' || node.name === '' || node.name.includes('/')) unreadable('a test bundle without a name');
      inBundle = node.name;
    }
    if (type === 'Test Case') {
      const ident = node.nodeIdentifier;
      if (typeof ident !== 'string' || ident === '') unreadable('a test case without a nodeIdentifier');
      const result = node.result;
      if (typeof result !== 'string' || !Object.hasOwn(RESULT_STATUS, result)) unreadable('a test result this reader does not know');
      const status = RESULT_STATUS[/** @type {keyof typeof RESULT_STATUS} */ (result)];
      const kinds = children.map((child) => {
        walk(child, 'Test Case', inBundle);
        return failureMessageKind(/** @type {any} */ (child).name);
      });
      const id = `${inBundle}/${ident}`;
      // one id twice (two bundles of one name, a repeated node) would let one result hide the other
      if (seen.has(id)) unreadable('two test cases with the same id');
      seen.add(id);
      if (status === 'failed') tests.push({ id, status, kind: kinds.length > 0 && kinds.every((k) => k === 'assertion') ? 'assertion' : 'error' });
      else if (kinds.length > 0) unreadable('a failure message on a test that did not fail');
      else tests.push({ id, status });
      return;
    }
    for (const child of children) walk(child, type, inBundle);
  };
  for (const node of doc.testNodes) walk(node, null, null);
  return { tests };
}

/**
 * `readXcresult` that never throws.
 * @param {string} text @returns {import('./junit.mjs').ReportResult}
 */
export function parseXcresult(text) {
  try {
    return readXcresult(text);
  } catch (err) {
    const message = err instanceof Error ? err.message : '';
    return { unreadable: message.startsWith('report-unreadable: ') ? message.slice('report-unreadable: '.length) : 'the result bundle could not be read' };
  }
}

/**
 * Read a result bundle with `xcresulttool`. Never throws. No bundle at the path is `NO_REPORT`; a
 * symlink or any other non-directory is unreadable. A relative `bundle` is resolved against `cwd`
 * once, and that one absolute path is both checked and read.
 * @param {string} bundle @param {{cwd: string, env: NodeJS.ProcessEnv, timeoutMs?: number}} opts
 * @returns {Promise<import('./junit.mjs').ReportResult>}
 */
export async function loadXcresult(bundle, { cwd, env, timeoutMs = XCRESULTTOOL_TIMEOUT_MS }) {
  const target = path.resolve(cwd, bundle);
  let st;
  try {
    st = await lstat(target);
  } catch (err) {
    return /** @type {any} */ (err)?.code === 'ENOENT' ? NO_REPORT : { unreadable: 'the result bundle could not be opened' };
  }
  if (!st.isDirectory()) return { unreadable: 'the result bundle is not a directory' };
  try {
    const res = await exec(['xcrun', 'xcresulttool', 'get', 'test-results', 'tests', '--path', target], { cwd, env, timeoutMs, maxBufferBytes: MAX_REPORT_BYTES + 1 });
    if (res.result !== 'ok' || res.code !== 0 || res.signal || res.timedOut) return { unreadable: 'xcresulttool failed' };
    return parseXcresult(res.stdout);
  } catch {
    return { unreadable: 'xcresulttool failed' };
  }
}

/**
 * Does the console text show that the test build failed? Colours are stripped first.
 * @param {string} text @returns {boolean}
 */
export function xcodeBuildFailed(text) {
  const plain = String(text).replace(ANSI, '');
  return BUILD_FAILED_LINE.test(plain) || BUILD_CANCELLED_LINE.test(plain);
}

/** @param {import('./junit.mjs').RunResult} run @returns {boolean} */
const exitedClean = (run) => run.code === 0 && !run.signal && !run.timedOut;

/**
 * A scope is `<test_target>/<Suite>/`: two non-empty parts and the closing `/`. Without the `/`,
 * `KitTests/Clamp` would also admit `KitTests/ClampMore/…`, so any other scope throws (the callers
 * then classify fail closed).
 * @param {unknown} scope @returns {asserts scope is string}
 */
function checkScope(scope) {
  if (typeof scope !== 'string' || !/^[^/]+\/[^/]+\/$/.test(scope)) throw new Error('an xcodebuild scope is <test_target>/<Suite>/');
}

/**
 * Is any test of a readable result outside `scope` (`<test_target>/<Suite>/`)?
 * @param {import('./junit.mjs').ReportResult} report @param {string} scope @returns {boolean}
 * @throws {Error} a scope that is not `<test_target>/<Suite>/`
 */
const ranOtherTests = (report, scope) => {
  checkScope(scope);
  return 'tests' in report && report.tests.some((t) => !t.id.startsWith(scope));
};

/**
 * Classify the RED step of an `xcodebuild` run. Never throws. A `scope` that is not
 * `<test_target>/<Suite>/` is `RED_INVALID error`. Then the first row that applies wins:
 *   - exit 0 and no readable result (no bundle, or an unreadable one) ⇒ `RED_INVALID error`: a clean
 *     exit with nothing to read says nothing about the test;
 *   - exit 0 and a readable result with a test outside `scope` (or only such tests) ⇒
 *     `RED_INVALID error` (the filter ran other tests): the test under proof did not run cleanly
 *     in scope, so a passing run is not `NOT_RED`;
 *   - exit 0 and a readable result with no test that ran ⇒ `RED_INVALID error` (the filter ran no
 *     test). This differs from `junit` and `swift`, where `classifyReport` reads any exit 0 as
 *     `NOT_RED`: `xcodebuild test` exits 0 when `-only-testing` selects nothing (a Swift Testing id
 *     without its `()`), so an empty run says nothing about the test and must not read as "it passed";
 *   - exit 0 otherwise ⇒ `NOT_RED`; a timeout or a signal ⇒ `RED_INVALID fatal`;
 *   - a failed run whose console shows a build failure, or that has no tests and a `swiftc` error
 *     ⇒ `RED_INVALID compile`;
 *   - a test outside `scope` ⇒ `RED_INVALID error` (the filter ran other tests);
 *   - else `classifyReport` (§1.1).
 * Every invalid row this adds carries no failed data.
 * @param {{run: import('./junit.mjs').RunResult, report: import('./junit.mjs').ReportResult, scope: string}} input
 * @returns {import('./junit.mjs').JUnitRedParse}
 */
export function classifyXcodeRed({ run, report, scope }) {
  try {
    checkScope(scope);
    const base = classifyReport({ run, reports: [report], compileError: swiftCompileError });
    /** @param {'compile' | 'error'} red_kind @param {string} reason @returns {import('./junit.mjs').JUnitRedParse} */
    const invalid = (red_kind, reason) => ({ ...base, verdict: 'RED_INVALID', red_kind, failed: null, failed_tests: [], failed_ids: [], reason });
    const readable = isObject(report) && Array.isArray(/** @type {any} */ (report).tests);
    if (exitedClean(run)) {
      if (!readable) return invalid('error', 'no readable test result');
      if (readable && ranOtherTests(report, scope)) return invalid('error', 'the filter ran other tests');
      if (readable && !(/** @type {any} */ (report).tests.some((/** @type {any} */ t) => t.status !== 'skipped'))) return invalid('error', 'the filter ran no test');
      return base;
    }
    if (run.timedOut || run.signal) return base;
    const output = `${run.stdout ?? ''}\n${run.stderr ?? ''}`;
    if (xcodeBuildFailed(output)) return invalid('compile', 'the test did not build');
    const noTests = report === NO_REPORT || (readable && /** @type {any} */ (report).tests.length === 0);
    if (noTests && swiftCompileError(output)) return invalid('compile', 'the test did not compile');
    if (readable && ranOtherTests(report, scope)) return invalid('error', 'the filter ran other tests');
    return base;
  } catch {
    return { verdict: 'RED_INVALID', red_kind: 'error', framework: 'junit', failed: null, failed_tests: [], failed_ids: [], reason: 'no readable test result' };
  }
}

/**
 * Classify the GREEN step of an `xcodebuild` run: `classifyGreen`, and `NOT_GREEN` when the result
 * has a test outside `scope`. Never throws.
 * @param {{run: import('./junit.mjs').RunResult, report: import('./junit.mjs').ReportResult, redFailed: string[], scope: string}} input
 * @returns {import('./junit.mjs').JUnitGreenParse}
 */
export function classifyXcodeGreen({ run, report, redFailed, scope }) {
  try {
    checkScope(scope);
    const base = classifyGreen({ run, reports: [report], redFailed });
    if (base.verdict === 'GREEN' && ranOtherTests(report, scope)) return { ...base, verdict: 'NOT_GREEN', reason: 'the filter ran other tests' };
    return base;
  } catch {
    return { verdict: 'NOT_GREEN', failed: null, failed_tests: [], reason: 'no readable test result' };
  }
}
