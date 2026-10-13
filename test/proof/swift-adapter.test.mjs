/**
 * B88: the `swift` runner adapter (red-green plan §1.3) — its default `{test_filter}`, the two
 * reports `swift test` writes (the XCTest report and its Swift Testing sibling), and the
 * compile-error rule. The runner here is a fake Node script (outside the repo) that writes the
 * captured reports where `swift test --xunit-output {report}` writes them, and prints the console
 * text a spec file gives it.
 *
 * Fixture provenance: the Swift fixtures in `test/fixtures/junit/` are byte-for-byte copies of the
 * reports the B88 live check (`swift-live.test.mjs`, run with `CODE_FORGE_SWIFT_CAPTURE=<dir>`)
 * captured on 2026-10-13 with `swift test --parallel --filter <filter> --xunit-output <report>`,
 * Apple Swift 6.3.3 (swiftlang-6.3.3.1.3), Xcode 26.6, macOS 26 arm64:
 *   - `xctest.xml`               report.xml of the XCTest RED step (testCapsHigh fails with the bare
 *                                `<failure message="failure">`, testKeepsInside passes);
 *   - `swift-testing-none.xml`   report-swift-testing.xml of that step (the filter matched XCTest
 *                                tests only: no test);
 *   - `xctest-pass.xml`          report.xml of the XCTest GREEN step (both pass);
 *   - `xctest-none.xml`          report.xml of the Swift Testing RED step (no test);
 *   - `swift-testing.xml`        report-swift-testing.xml of that step (capsHigh() fails);
 *   - `swift-testing-pass.xml`   report-swift-testing.xml of the Swift Testing GREEN step;
 *   - `swift-testing-crash.xml`  report-swift-testing.xml of a test that hit `fatalError` (signal 5):
 *                                SwiftPM leaves it cut off after `<testsuites>`.
 * They carry no added comment: they are what SwiftPM wrote.
 */

import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { classifyGreen, classifyReport, NO_REPORT, parseReport, readJUnit, swiftCompileError } from '../../src/proof/junit.mjs';
import { expandRunnerArgv, runnerHook, SWIFT_NO_MAIN_REPORT } from '../../src/proof/runners.mjs';

const FIXTURES = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'junit');
const fixture = (/** @type {string} */ name) => readFileSync(path.join(FIXTURES, name), 'utf8');
const NODE = process.execPath;

const TMP = mkdtempSync(path.join(os.tmpdir(), 'swift-adapter-test-'));
after(() => rmSync(TMP, { recursive: true, force: true }));
const FAKE = path.join(TMP, 'fake-swift.mjs');
const SPEC = path.join(TMP, 'spec.json');
const LOG = path.join(TMP, 'filters.log');
const WORK = path.join(TMP, 'work');
mkdirSync(WORK);

const XC_FILE = 'Tests/KitTests/ClampXCTests.swift';
const ST_FILE = 'Tests/KitTests/ClampSwiftTests.swift';
const XC_FAILED = 'KitTests.ClampXCTests.testCapsHigh';
const ST_FAILED = 'KitTests.ClampSwiftTests.capsHigh()';

/** The XCTest console line of the captured RED run (macOS; the path shortened). */
const XCTEST_LINE = '/Users/dev/Kit/Tests/KitTests/ClampXCTests.swift:6: error: -[KitTests.ClampXCTests testCapsHigh] : XCTAssertEqual failed: ("12") is not equal to ("10")\n';
/** The Swift Testing console line of the captured RED run: a column, but no `error:`. */
const ST_LINE = '\u{100884}  Test capsHigh() recorded an issue at ClampSwiftTests.swift:6:9: Expectation failed: (clamp(12, 0, 10) → 12) == 10\n';
/** The captured stderr of a test that calls a function the source does not have yet. */
const COMPILE_STDERR = [
  '/Users/dev/Kit/Tests/KitTests/WrapTests.swift:6:17: error: cannot find \'wrap\' in scope',
  '4 | @Suite struct WrapTests {',
  '5 |     @Test func wrapsAround() {',
  '6 |         #expect(wrap(12, 10) == 2)',
  '  |                 `- error: cannot find \'wrap\' in scope',
  '7 |     }',
  '8 | }',
  '',
  'error: fatalError',
  '',
].join('\n');
/** The captured console of a test whose code hit `fatalError` (the paths shortened). */
const CRASH_STDOUT = '\u{1007C8}  Suite HalfTests started.\n\u{1007C8}  Test halves() started.\nKit/Kit.swift:6: Fatal error: half is not written yet\n';
const CRASH_STDERR =
  "Building for debugging...\nBuild complete! (12.57s)\nerror: Process '/x/swiftpm-testing-helper --test-bundle-path /x/KitPackageTests --parallel --filter \\.HalfTests/ --xunit-output /x/report-swift-testing.xml /x/KitPackageTests --testing-library swift-testing' exited with unexpected signal code 5\n";

/**
 * The fake `swift test`: argv `<spec> <log> <filter> <report>`. It logs the filter, writes the
 * spec's `main` fixture at `<report>` and its `sibling` fixture at `<report stem>-swift-testing.xml`
 * (`null` writes nothing, `GARBAGE` writes a file that is not XML), prints `stdout` and `stderr`,
 * and exits with `code`.
 */
writeFileSync(
  FAKE,
  `
import { appendFileSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
const [specFile, logFile, filter, report] = process.argv.slice(2);
const spec = JSON.parse(readFileSync(specFile, 'utf8'));
appendFileSync(logFile, JSON.stringify(filter) + '\\n');
const put = (name, file) => {
  if (name === null) return;
  writeFileSync(file, name === 'GARBAGE' ? 'not a report' : readFileSync(path.join(${JSON.stringify(FIXTURES)}, name)));
};
put(spec.main, report);
put(spec.sibling, report.replace(/\\.xml$/, '-swift-testing.xml'));
process.stdout.write(spec.stdout ?? '');
process.stderr.write(spec.stderr ?? '');
process.exit(spec.code);
`,
);

/** @param {'junit' | 'swift'} adapter @returns {import('../../src/proof/runners.mjs').RunnerEntry} */
const entry = (adapter) => ({ match: ['Tests/**/*.swift'], adapter, argv: [NODE, FAKE, SPEC, LOG, '{test_filter}', '{report}'] });
/** @returns {string[]} the filters the fake was given so far */
const filters = () => readFileSync(LOG, 'utf8').trim().split('\n').map((l) => JSON.parse(l));

/** @typedef {{main: string | null, sibling: string | null, stdout?: string, stderr?: string, code: number}} FakeSpec */

/**
 * One RED step through the real hook.
 * @param {FakeSpec} spec
 * @param {{adapter?: 'junit' | 'swift', file?: string, testCase?: string}} [opts]
 */
async function redStep(spec, { adapter = 'swift', file = XC_FILE, testCase } = {}) {
  writeFileSync(SPEC, JSON.stringify(spec));
  const hook = runnerHook(entry(adapter), { file, ...(testCase === undefined ? {} : { testCase }) });
  return hook.red({ cwd: WORK, env: process.env, timeoutMs: 60_000 });
}

/** @param {Awaited<ReturnType<typeof redStep>>} r */
const red = (r) => [r.verdict, r.red_kind, r.failed, r.failed_tests];

test('B88 (1) swift: no ::case ⇒ {test_filter} is the file base name, escaped and anchored as \\.<name>/; a case is escaped and anchored as (?:^|[./])<case>(?![^/]); junit keeps the plain escaped case', async () => {
  const report = path.join(TMP, 'r.xml');
  const argv = (/** @type {string} */ file, /** @type {string | undefined} */ testCase) =>
    expandRunnerArgv(entry('swift'), { file, ...(testCase === undefined ? {} : { testCase }), report }).slice(4);
  assert.deepEqual(argv(XC_FILE, undefined), ['\\.ClampXCTests/', report]);
  assert.deepEqual(argv('Tests/KitTests/Clamp+Edge.Tests.swift', undefined), ['\\.Clamp\\+Edge\\.Tests/', report]);
  assert.deepEqual(argv(ST_FILE, 'ClampSwiftTests/capsHigh()'), ['(?:^|[./])ClampSwiftTests/capsHigh\\(\\)(?![^/])', report]);
  assert.deepEqual(argv(XC_FILE, 'FooTests/bar()'), ['(?:^|[./])FooTests/bar\\(\\)(?![^/])', report]);
  assert.deepEqual(argv(XC_FILE, 'testFoo'), ['(?:^|[./])testFoo(?![^/])', report]);
  // the junit adapter keeps the plain escaped case
  assert.deepEqual(expandRunnerArgv(entry('junit'), { file: XC_FILE, testCase: 'FooTests/bar()', report }).slice(4), ['FooTests/bar\\(\\)', report]);
  // a base name that starts with "-" is no option once anchored
  assert.deepEqual(argv('Tests/-Dash.swift', undefined), ['\\.-Dash/', report]);
  assert.throws(() => expandRunnerArgv(entry('swift'), { file: 'Tests/A.swift', testCase: '-x', report }), { code: 'usage', message: 'the test case starts with "-" (it would be read as an option)' });
  assert.throws(() => expandRunnerArgv(entry('junit'), { file: 'Tests/A.swift', report }), {
    code: 'usage',
    message: 'the junit runner for Tests/A.swift filters by test case — give --test <file>::<case>',
  });
  // through the hook: the runner child gets exactly these filters
  writeFileSync(LOG, '');
  await redStep({ main: 'xctest.xml', sibling: 'swift-testing-none.xml', stdout: XCTEST_LINE, code: 1 });
  await redStep({ main: 'xctest-none.xml', sibling: 'swift-testing.xml', code: 1 }, { file: ST_FILE, testCase: 'ClampSwiftTests/capsHigh()' });
  assert.deepEqual(filters(), ['\\.ClampXCTests/', '(?:^|[./])ClampSwiftTests/capsHigh\\(\\)(?![^/])']);
});

test('B88 (1) swift: the anchored no-case filter selects that suite only, never a near-name suite', () => {
  const report = path.join(TMP, 'r.xml');
  // `swift test --filter` searches the regex in `Module.Suite/test` without anchors (live check)
  const selects = (/** @type {string} */ file) => {
    const re = new RegExp(expandRunnerArgv(entry('swift'), { file, report })[4]);
    return (/** @type {string} */ id) => re.test(id);
  };
  /** @type {[string, string, boolean][]} */
  const cases = [
    [XC_FILE, 'KitTests.ClampXCTests/testCapsHigh', true],
    [XC_FILE, 'KitTests.ClampXCTests/testKeepsInside', true],
    [XC_FILE, 'KitTests.Outer.ClampXCTests/testCapsHigh', true],
    [XC_FILE, 'KitTests.BarClampXCTests/testCapsHigh', false],
    [XC_FILE, 'KitTests.ClampXCTestsHelpers/testCapsHigh', false],
    [XC_FILE, 'KitTests.ClampXCTestsExtra/testAlwaysFails', false],
    [XC_FILE, 'KitTests.ClampXCTests', false],
    [ST_FILE, 'KitTests.ClampSwiftTests/capsHigh()', true],
    [ST_FILE, 'KitTests.MoreClampSwiftTests/alwaysFails()', false],
    ['Tests/KitTests/Clamp+Edge.Tests.swift', 'KitTests.Clamp+Edge.Tests/t()', true],
    ['Tests/KitTests/Clamp+Edge.Tests.swift', 'KitTests.ClampEdgeXTests/t()', false],
  ];
  for (const [file, id, expected] of cases) assert.deepEqual([file, id, selects(file)(id)], [file, id, expected]);
});

test('B88 (1) swift: the anchored case filter selects that test only, never a near-name suite or a longer test name', () => {
  const report = path.join(TMP, 'r.xml');
  // the same search as `swift test --filter`; SwiftPM 6.3.3 matches a Swift Testing test against an
  // id with more after the test name (`$` selects none, checked live), modelled here as `/<location>`
  const selects = (/** @type {string} */ testCase, /** @type {string} */ id) =>
    new RegExp(expandRunnerArgv(entry('swift'), { file: XC_FILE, testCase, report })[4]).test(id);
  /** @type {[string, string, boolean][]} */
  const cases = [
    ['ClampSwiftTests/capsHigh()', 'KitTests.ClampSwiftTests/capsHigh()', true],
    ['ClampSwiftTests/capsHigh()', 'KitTests.ClampSwiftTests/capsHigh()/ClampSwiftTests.swift:5:6', true],
    ['ClampSwiftTests/capsHigh()', 'KitTests.MoreClampSwiftTests/capsHigh()', false],
    ['ClampSwiftTests/capsHigh()', 'KitTests.MoreClampSwiftTests/capsHigh()/ClampSwiftTests.swift:9:6', false],
    ['ClampSwiftTests/capsHigh()', 'KitTests.ClampSwiftTests/capsHighToo()', false],
    ['ClampXCTests/testFoo', 'KitTests.ClampXCTests/testFoo', true],
    ['ClampXCTests/testFoo', 'KitTests.ClampXCTests/testFooBar', false],
    ['ClampXCTests/testFoo', 'KitTests.MoreClampXCTests/testFoo', false],
    ['testFoo', 'KitTests.ClampXCTests/testFoo', true],
    ['testFoo', 'KitTests.ClampXCTests/testFooBar', false],
    ['ClampXCTests', 'KitTests.ClampXCTests/testFoo', true],
    ['ClampXCTests', 'KitTests.ClampXCTestsExtra/testAlwaysFails', false],
    ['KitTests.ClampXCTests/testFoo', 'KitTests.ClampXCTests/testFoo', true],
  ];
  for (const [testCase, id, expected] of cases) assert.deepEqual([testCase, id, selects(testCase, id)], [testCase, id, expected]);
});

test('B88 (2) swift: both reports are read and a failure in either counts', async () => {
  // XCTest fails (bare failure + its console line), the sibling has no test
  assert.deepEqual(red(await redStep({ main: 'xctest.xml', sibling: 'swift-testing-none.xml', stdout: XCTEST_LINE, code: 1 })), ['RED', 'assertion', 1, [XC_FAILED]]);
  // Swift Testing fails, the XCTest report has no test
  assert.deepEqual(red(await redStep({ main: 'xctest-none.xml', sibling: 'swift-testing.xml', stdout: ST_LINE, code: 1 })), ['RED', 'assertion', 1, [ST_FAILED]]);
  // both fail
  assert.deepEqual(red(await redStep({ main: 'xctest.xml', sibling: 'swift-testing.xml', stdout: XCTEST_LINE, code: 1 })), ['RED', 'assertion', 2, [ST_FAILED, XC_FAILED]]);
  // the bare XCTest failure without its console line is no assertion
  assert.deepEqual(red(await redStep({ main: 'xctest.xml', sibling: 'swift-testing-none.xml', code: 1 })), ['RED_INVALID', 'error', 1, [XC_FAILED]]);
  // a failing main report and no sibling (RED reads what was written): the main report decides; GREEN then needs the same report files
  assert.deepEqual(red(await redStep({ main: 'xctest.xml', sibling: null, stdout: XCTEST_LINE, code: 1 })), ['RED', 'assertion', 1, [XC_FAILED]]);
  // the junit adapter reads only {report}: the failing sibling is not seen
  assert.deepEqual(red(await redStep({ main: 'xctest-none.xml', sibling: 'swift-testing.xml', code: 1 }, { adapter: 'junit', testCase: 'ClampXCTests' })), ['RED_INVALID', 'error', 0, []]);
});

test('B88 (2) swift: fail closed — an unreadable report, or a sibling with no main report, proves nothing', async () => {
  // an unreadable sibling next to a failing main report
  assert.deepEqual(red(await redStep({ main: 'xctest.xml', sibling: 'GARBAGE', stdout: XCTEST_LINE, code: 1 })), ['RED_INVALID', 'error', null, []]);
  // an unreadable main report next to a failing sibling
  assert.deepEqual(red(await redStep({ main: 'GARBAGE', sibling: 'swift-testing.xml', stdout: ST_LINE, code: 1 })), ['RED_INVALID', 'error', null, []]);
  // a failing sibling but no main report: --parallel is missing from the argv
  const noMain = await redStep({ main: null, sibling: 'swift-testing.xml', stdout: ST_LINE, code: 1 });
  assert.deepEqual([...red(noMain), noMain.failed_ids, noMain.reason], ['RED_INVALID', 'error', null, [], [], SWIFT_NO_MAIN_REPORT]);
  assert.equal(
    SWIFT_NO_MAIN_REPORT,
    'swift test wrote a Swift Testing report but no XCTest report at {report}: keep --parallel in the runner argv (SwiftPM 6.3 writes the XCTest report only with it)',
  );
  // the same with a compile line in the console: a report was written, so it is no compile error
  const noMainCompile = await redStep({ main: null, sibling: 'swift-testing.xml', stderr: COMPILE_STDERR, code: 1 });
  assert.deepEqual([...red(noMainCompile), noMainCompile.failed_ids, noMainCompile.reason], ['RED_INVALID', 'error', null, [], [], SWIFT_NO_MAIN_REPORT]);
  // the cut-off sibling of a crashed run
  assert.deepEqual(red(await redStep({ main: 'xctest-none.xml', sibling: 'swift-testing-crash.xml', stdout: CRASH_STDOUT, stderr: CRASH_STDERR, code: 1 }, { file: 'Tests/KitTests/HalfTests.swift' })), [
    'RED_INVALID',
    'error',
    null,
    [],
  ]);
});

test('B88 (2) swift GREEN: every test that failed at RED must pass in its own report, with exit 0', async () => {
  const hook = () => runnerHook(entry('swift'), { file: ST_FILE });
  // RED and GREEN go through the same file; RED fails in both reports
  writeFileSync(SPEC, JSON.stringify({ main: 'xctest.xml', sibling: 'swift-testing.xml', stdout: XCTEST_LINE, code: 1 }));
  const redResult = await hook().red({ cwd: WORK, env: process.env, timeoutMs: 60_000 });
  assert.deepEqual([...red(redResult), redResult.failed_ids], ['RED', 'assertion', 2, [ST_FAILED, XC_FAILED], [ST_FAILED, XC_FAILED]]);
  /** @type {[string, FakeSpec, string][]} */
  const cases = [
    ['both pass', { main: 'xctest-pass.xml', sibling: 'swift-testing-pass.xml', code: 0 }, 'GREEN'],
    ['the sibling still fails', { main: 'xctest-pass.xml', sibling: 'swift-testing.xml', code: 0 }, 'NOT_GREEN'],
    ['the XCTest test missing', { main: 'xctest-none.xml', sibling: 'swift-testing-pass.xml', code: 0 }, 'NOT_GREEN'],
    ['exit 1', { main: 'xctest-pass.xml', sibling: 'swift-testing-pass.xml', code: 1 }, 'NOT_GREEN'],
    ['no report at all', { main: null, sibling: null, code: 0 }, 'NOT_GREEN'],
  ];
  for (const [name, spec, expected] of cases) {
    writeFileSync(SPEC, JSON.stringify(spec));
    assert.deepEqual([name, await hook().green({ cwd: WORK, env: process.env, timeoutMs: 60_000, red: redResult })], [name, expected]);
  }
});

test('B88 (2) swift GREEN fail closed: a missing or unreadable report is NOT_GREEN even when every test that failed at RED passed in the report that stays', async () => {
  const hook = () => runnerHook(entry('swift'), { file: ST_FILE });
  /** @param {FakeSpec} spec */
  const redOf = async (spec) => {
    writeFileSync(SPEC, JSON.stringify(spec));
    return hook().red({ cwd: WORK, env: process.env, timeoutMs: 60_000 });
  };
  /** @param {FakeSpec} spec @param {Awaited<ReturnType<typeof redOf>>} redResult */
  const greenOf = async (spec, redResult) => {
    writeFileSync(SPEC, JSON.stringify(spec));
    return hook().green({ cwd: WORK, env: process.env, timeoutMs: 60_000, red: redResult });
  };
  // only XCTest fails at RED: the main report holds every failed id, the sibling none
  const xcRed = await redOf({ main: 'xctest.xml', sibling: 'swift-testing-none.xml', stdout: XCTEST_LINE, code: 1 });
  assert.deepEqual([xcRed.verdict, xcRed.failed_ids], ['RED', [XC_FAILED]]);
  // only Swift Testing fails at RED: the sibling holds every failed id, the main report none
  const stRed = await redOf({ main: 'xctest-none.xml', sibling: 'swift-testing.xml', stdout: ST_LINE, code: 1 });
  assert.deepEqual([stRed.verdict, stRed.failed_ids], ['RED', [ST_FAILED]]);
  /** @type {[string, FakeSpec, typeof xcRed, string][]} */
  const cases = [
    // the controls: the report that stays alone proves it
    ['XCTest RED, both reports', { main: 'xctest-pass.xml', sibling: 'swift-testing-none.xml', code: 0 }, xcRed, 'GREEN'],
    ['Swift Testing RED, both reports', { main: 'xctest-none.xml', sibling: 'swift-testing-pass.xml', code: 0 }, stRed, 'GREEN'],
    // the same with one report missing or not XML
    ['XCTest RED, no sibling', { main: 'xctest-pass.xml', sibling: null, code: 0 }, xcRed, 'NOT_GREEN'],
    ['XCTest RED, an unreadable sibling', { main: 'xctest-pass.xml', sibling: 'GARBAGE', code: 0 }, xcRed, 'NOT_GREEN'],
    ['Swift Testing RED, no main report', { main: null, sibling: 'swift-testing-pass.xml', code: 0 }, stRed, 'NOT_GREEN'],
    ['Swift Testing RED, an unreadable main report', { main: 'GARBAGE', sibling: 'swift-testing-pass.xml', code: 0 }, stRed, 'NOT_GREEN'],
  ];
  for (const [name, spec, redResult, expected] of cases) assert.deepEqual([name, await greenOf(spec, redResult)], [name, expected]);
  // a RED result no swift hook classified proves nothing, even with both reports passing
  assert.equal(await greenOf({ main: 'xctest-pass.xml', sibling: 'swift-testing-none.xml', code: 0 }, { ...xcRed }), 'NOT_GREEN');
});

test('B88 (3) swift: a failed run with no report and a swiftc error line is RED_INVALID compile; an XCTest line or a crash alone is not', async () => {
  assert.deepEqual(red(await redStep({ main: null, sibling: null, stderr: COMPILE_STDERR, code: 1 })), ['RED_INVALID', 'compile', null, []]);
  assert.deepEqual(red(await redStep({ main: null, sibling: null, stderr: `\x1b[1m${COMPILE_STDERR}\x1b[0m`, code: 1 })), ['RED_INVALID', 'compile', null, []]);
  assert.deepEqual(red(await redStep({ main: null, sibling: null, stdout: XCTEST_LINE, code: 1 })), ['RED_INVALID', 'error', null, []]);
  assert.deepEqual(red(await redStep({ main: null, sibling: null, stdout: ST_LINE, code: 1 })), ['RED_INVALID', 'error', null, []]);
  // a crashed test that wrote no report: an error, never a compile error
  assert.deepEqual(red(await redStep({ main: null, sibling: null, stdout: CRASH_STDOUT, stderr: CRASH_STDERR, code: 1 }, { file: 'Tests/KitTests/HalfTests.swift' })), ['RED_INVALID', 'error', null, []]);
  // SwiftPM's closing line alone is no compile error
  assert.deepEqual(red(await redStep({ main: null, sibling: null, stdout: CRASH_STDOUT, stderr: 'error: fatalError\n', code: 1 })), ['RED_INVALID', 'error', null, []]);
  // a report was written: the report decides, not the console
  assert.deepEqual(red(await redStep({ main: 'xctest-none.xml', sibling: 'swift-testing.xml', stderr: COMPILE_STDERR, code: 1 })), ['RED', 'assertion', 1, [ST_FAILED]]);
  // the junit adapter has no compile rule
  assert.deepEqual(red(await redStep({ main: null, sibling: null, stderr: COMPILE_STDERR, code: 1 }, { adapter: 'junit', testCase: 'ClampXCTests' })), ['RED_INVALID', 'error', null, []]);
  // exit 0 is NOT_RED whatever the console says
  assert.deepEqual(red(await redStep({ main: null, sibling: null, stderr: COMPILE_STDERR, code: 0 })), ['NOT_RED', 'none', null, []]);
});

test('B88 (3) swiftCompileError: compiler and SwiftPM build lines only', () => {
  /** @type {[string, boolean][]} */
  const cases = [
    ["/x/WrapTests.swift:6:17: error: cannot find 'wrap' in scope\n", true],
    ["  /x/WrapTests.swift:6:17: error: cannot find 'wrap' in scope\n", true],
    [COMPILE_STDERR, true],
    ['error: emit-module command failed with exit code 1 (use -v to see invocation)\n', true],
    ['error: compile command failed due to signal 6 (use -v to see invocation)\n', true],
    ["\x1b[1m/x/A.swift:3:5: \x1b[31merror: \x1b[0mcannot convert value\n", true],
    ['error: fatalError\n', false],
    [`${CRASH_STDOUT}${CRASH_STDERR}`, false],
    [XCTEST_LINE, false],
    [ST_LINE, false],
    ['/x/A.swift:3:5: warning: variable was never mutated\n', false],
    ['error: Exited with unexpected signal code 4\n', false],
    ['', false],
  ];
  for (const [text, expected] of cases) assert.deepEqual([text, swiftCompileError(text)], [text, expected]);
});

test('B88 (3) classifyReport asks the compile check only when every report is NO_REPORT (a non-empty list)', () => {
  const run = { code: 1, signal: null, timedOut: false, stdout: '', stderr: COMPILE_STDERR };
  assert.deepEqual(red(classifyReport({ run, reports: [NO_REPORT], compileError: swiftCompileError })), ['RED_INVALID', 'compile', null, []]);
  assert.deepEqual(red(classifyReport({ run, reports: [NO_REPORT, NO_REPORT], compileError: swiftCompileError })), ['RED_INVALID', 'compile', null, []]);
  // an empty list is not "no report was written"
  assert.deepEqual(red(classifyReport({ run, reports: [], compileError: swiftCompileError })), ['RED_INVALID', 'error', null, []]);
  // the same message on another object is not NO_REPORT: identity, never the message text
  assert.equal(NO_REPORT.unreadable, 'no report was written');
  assert.deepEqual(red(classifyReport({ run, reports: [{ unreadable: 'no report was written' }], compileError: swiftCompileError })), ['RED_INVALID', 'error', null, []]);
  assert.deepEqual(red(classifyReport({ run, reports: [NO_REPORT, { unreadable: 'a bare & or an unknown entity' }], compileError: swiftCompileError })), ['RED_INVALID', 'error', null, []]);
  assert.deepEqual(red(classifyReport({ run, reports: [{ unreadable: 'a bare & or an unknown entity' }], compileError: swiftCompileError })), ['RED_INVALID', 'error', null, []]);
  // no compile check given
  assert.deepEqual(red(classifyReport({ run, reports: [NO_REPORT] })), ['RED_INVALID', 'error', null, []]);
  assert.equal(Object.isFrozen(NO_REPORT), true);
});

test('B88 (6) the captured Swift reports: exact tests, the real bare XCTest failure shape, the Swift Testing assertion, the cut-off crash report', () => {
  /** @type {[string, unknown[]][]} */
  const expected = [
    ['xctest.xml', [
      { id: XC_FAILED, status: 'failed', kind: 'error', needs_console: 'xctest' },
      { id: 'KitTests.ClampXCTests.testKeepsInside', status: 'passed' },
    ]],
    ['xctest-pass.xml', [
      { id: 'KitTests.ClampXCTests.testKeepsInside', status: 'passed' },
      { id: 'KitTests.ClampXCTests.testCapsHigh', status: 'passed' },
    ]],
    ['xctest-none.xml', []],
    ['swift-testing.xml', [
      { id: ST_FAILED, status: 'failed', kind: 'assertion' },
      { id: 'KitTests.ClampSwiftTests.keepsInside()', status: 'passed' },
    ]],
    ['swift-testing-pass.xml', [
      { id: 'KitTests.ClampSwiftTests.keepsInside()', status: 'passed' },
      { id: 'KitTests.ClampSwiftTests.capsHigh()', status: 'passed' },
    ]],
    ['swift-testing-none.xml', []],
  ];
  for (const [name, tests] of expected) assert.deepEqual([name, readJUnit(fixture(name)).tests], [name, tests]);
  assert.deepEqual(parseReport(fixture('swift-testing-crash.xml')), { unreadable: 'an unclosed element' });
  // the older bare shape (message="failed") is read the same way as the captured one (message="failure")
  for (const message of ['failure', 'failed']) {
    const parsed = readJUnit(`<testsuites><testsuite name="TestResults"><testcase classname="K.S" name="t"><failure message="${message}"></failure></testcase></testsuite></testsuites>`);
    assert.deepEqual([message, parsed.tests], [message, [{ id: 'K.S.t', status: 'failed', kind: 'error', needs_console: 'xctest' }]]);
  }
  // a GREEN on the captured pass reports
  const green = classifyGreen({ run: { code: 0, signal: null, timedOut: false }, reports: [readJUnit(fixture('xctest-pass.xml')), readJUnit(fixture('swift-testing-pass.xml'))], redFailed: [XC_FAILED, ST_FAILED] });
  assert.deepEqual([green.verdict, green.failed], ['GREEN', 0]);
});
