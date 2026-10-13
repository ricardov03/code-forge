/**
 * B86: the fail-closed JUnit report reader and the RED/GREEN classifier (red-green plan §1.1).
 * Fixtures under `test/fixtures/junit/`: B88 replaced the Swift ones (`xctest*.xml`, `swift-testing*.xml`) with
 * reports captured by `swift-live.test.mjs`; B89 replaced the PHP ones (`phpunit*.xml`, `pest*.xml`) with
 * reports captured by `laravel-live.test.mjs` on 2026-10-13 (PHP 8.4.25, Laravel Framework 13.35.0, PHPUnit
 * 12.5.39, Pest 4.7.8 on PHPUnit 12.5.33; `php artisan test <file> --filter <case> --log-junit <report>`
 * through `proof red-green`), byte-equal: `phpunit.xml` / `pest.xml` the RED step of the Clamp test's failing
 * case, `*-pass.xml` its GREEN step, `*-new-class.xml` the RED step of a test whose class the block added;
 * `phpunit-full.xml` a full-config run in the same check (`php artisan test --log-junit <report>`, no file, no
 * `--filter`): the root suite named after the `phpunit.xml` path, then `Unit` / `Feature`, then the classes.
 * `pytest.xml` is still hand-written to pytest's documented format (it says so).
 */
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { MAX_REPORT_BYTES, classifyGreen, classifyReport, loadReport, parseReport, readJUnit, readReportFd } from '../../src/proof/junit.mjs';

const FIXTURES = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'junit');
/** The reader's file URL, for a child process to import. */
const JUNIT_URL = new URL('../../src/proof/junit.mjs', import.meta.url).href;
/** The child's env: no GIT_* variables. */
const CHILD_ENV = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')));
const fixture = (/** @type {string} */ name) => readFileSync(path.join(FIXTURES, name), 'utf8');

const TMP = mkdtempSync(path.join(os.tmpdir(), 'junit-test-'));
after(() => rmSync(TMP, { recursive: true, force: true }));

const FAILED_RUN = Object.freeze({ code: 1, signal: null, timedOut: false, stdout: '', stderr: '' });
const PASSED_RUN = Object.freeze({ code: 0, signal: null, timedOut: false, stdout: '', stderr: '' });
/** The macOS console line XCTest printed for the failed case of `xctest.xml` (captured by the B88 live check; the path shortened). */
const XCTEST_MAC_LINE = '/Users/dev/Kit/Tests/KitTests/ClampXCTests.swift:6: error: -[KitTests.ClampXCTests testCapsHigh] : XCTAssertEqual failed: ("12") is not equal to ("10")\n';

/** @param {Array<{id: string, status: string, failure?: string}>} cases a minimal report */
function report(cases) {
  const body = cases
    .map(({ id, status, failure }) => {
      if (status === 'passed') return `<testcase name="${id}"/>`;
      if (status === 'skipped') return `<testcase name="${id}"><skipped/></testcase>`;
      if (status === 'error') return `<testcase name="${id}"><error type="RuntimeException" message="boom"/></testcase>`;
      return `<testcase name="${id}"><failure ${failure ?? 'type="AssertionError"'}/></testcase>`;
    })
    .join('');
  return `<testsuite name="s">${body}</testsuite>`;
}

/** @param {ReturnType<typeof classifyReport>} r */
const red = (r) => [r.verdict, r.red_kind, r.failed, r.failed_tests];

test('B86 (1): one assertion <failure> per runner fixture is RED assertion, failed 1, naming the test', () => {
  const expected = {
    'xctest.xml': 'KitTests.ClampXCTests.testCapsHigh',
    'swift-testing.xml': 'KitTests.ClampSwiftTests.capsHigh()',
    'phpunit.xml': 'Tests.Unit.ClampTest.test_caps_high',
    'pest.xml': 'Tests.Unit.ClampTest.caps high',
    'pytest.xml': 'tests.test_clamp.test_clamps_high',
  };
  // the PHP reports come from a `--filter` run of the one failing case: 1 test, none passed
  const counts = { 'phpunit.xml': [1, 0], 'pest.xml': [1, 0] };
  for (const [file, id] of Object.entries(expected)) {
    const parsed = readJUnit(fixture(file));
    const [total, passed] = counts[/** @type {'phpunit.xml'} */ (file)] ?? [2, 1];
    assert.equal(parsed.tests.length, total, file);
    assert.deepEqual(parsed.tests.filter((t) => t.status === 'passed').length, passed, file);
    const run = file === 'xctest.xml' ? { ...FAILED_RUN, stdout: XCTEST_MAC_LINE } : FAILED_RUN;
    const result = classifyReport({ run, reports: [parsed] });
    assert.deepEqual(red(result), ['RED', 'assertion', 1, [id]], file);
    assert.equal(result.framework, 'junit');
    assert.deepEqual(result.failed_ids, [id], file);
  }
});

test('B86 (1): the reader returns id, status and kind per testcase, through nested suites', () => {
  assert.deepEqual(readJUnit(fixture('phpunit.xml')), { tests: [{ id: 'Tests.Unit.ClampTest.test_caps_high', status: 'failed', kind: 'assertion' }] });
  // no classname: the id is the name; bytes are read as UTF-8; a BOM is skipped
  const bytes = Buffer.from('﻿<testsuites><testsuite name="a"><testcase name="só"/></testsuite><testsuite name="b"><testcase name="x"><skipped/></testcase></testsuite></testsuites>', 'utf8');
  assert.deepEqual(readJUnit(bytes), { tests: [{ id: 'só', status: 'passed' }, { id: 'x', status: 'skipped' }] });
});

test('B86 (2): an <error>, or a <failure> of type TypeError, is RED_INVALID error, also next to an assertion failure', () => {
  const alone = [
    report([{ id: 'a', status: 'error' }]),
    report([{ id: 'a', status: 'failed', failure: 'type="TypeError" message="clamp is not a function"' }]),
  ];
  for (const xml of alone) {
    assert.deepEqual(red(classifyReport({ run: FAILED_RUN, reports: [readJUnit(xml)] })), ['RED_INVALID', 'error', 1, ['a']]);
  }
  const mixed = [
    report([{ id: 'ok', status: 'failed' }, { id: 'bad', status: 'error' }]),
    report([{ id: 'ok', status: 'failed' }, { id: 'bad', status: 'failed', failure: 'type="TypeError"' }]),
  ];
  for (const xml of mixed) {
    assert.deepEqual(red(classifyReport({ run: FAILED_RUN, reports: [readJUnit(xml)] })), ['RED_INVALID', 'error', 2, ['ok', 'bad']]);
  }
  // a type that is not an assertion wins over a message that names one; an unknown failure is an error
  assert.deepEqual(readJUnit(report([{ id: 'a', status: 'failed', failure: 'type="TypeError" message="AssertionError"' }])).tests, [{ id: 'a', status: 'failed', kind: 'error' }]);
  assert.deepEqual(readJUnit(report([{ id: 'a', status: 'failed', failure: '' }])).tests, [{ id: 'a', status: 'failed', kind: 'error' }]);
  // a second report with an error spoils an assertion RED in the first
  const two = [readJUnit(fixture('xctest.xml')), readJUnit(report([{ id: 'b', status: 'error' }]))];
  assert.deepEqual(red(classifyReport({ run: FAILED_RUN, reports: two })), ['RED_INVALID', 'error', 2, ['KitTests.ClampXCTests.testCapsHigh', 'b']]);
});

test('B86 (3): no report, an empty file, malformed XML, a DOCTYPE or ENTITY, a file over 8 MB are RED_INVALID error and never throw', () => {
  const missing = loadReport(path.join(TMP, 'none.xml'));
  assert.deepEqual(missing, { unreadable: 'no report was written' });
  // the reader allocates the fstat size + 1 and refuses a file that grew (or shrank) after fstat
  const grown = path.join(TMP, 'grown.xml');
  writeFileSync(grown, '<testsuite><testcase name="a"/></testsuite>');
  const fd = openSync(grown, 'r');
  try {
    assert.deepEqual(readReportFd(fd, 20), { unreadable: 'the report changed while it was read' });
    assert.deepEqual(readReportFd(fd, MAX_REPORT_BYTES + 1), { unreadable: 'the report is over 8 MiB' });
  } finally {
    closeSync(fd);
  }
  const fd2 = openSync(grown, 'r');
  try {
    assert.deepEqual(readReportFd(fd2, 100), { unreadable: 'the report changed while it was read' });
  } finally {
    closeSync(fd2);
  }
  const empty = path.join(TMP, 'empty.xml');
  writeFileSync(empty, '');
  assert.deepEqual(loadReport(empty), { unreadable: 'no root element' });
  const big = path.join(TMP, 'big.xml');
  writeFileSync(big, Buffer.concat([Buffer.from(fixture('xctest.xml')), Buffer.alloc(MAX_REPORT_BYTES, 0x20)]));
  assert.deepEqual(loadReport(big), { unreadable: 'the report is over 8 MiB' });
  const link = path.join(TMP, 'link.xml');
  symlinkSync(path.join(FIXTURES, 'xctest.xml'), link);
  assert.deepEqual(loadReport(link), { unreadable: 'the report could not be opened' });
  const good = path.join(TMP, 'good.xml');
  writeFileSync(good, fixture('pytest.xml'));
  assert.equal(/** @type {any} */ (loadReport(good)).tests.length, 2);

  const broken = {
    'unclosed': '<testsuite><testcase name="a">',
    'mismatched': '<testsuite><testcase name="a"></testsuite></testcase>',
    'doctype': '<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]><testsuite><testcase name="&e;"/></testsuite>',
    'entity in body': '<testsuite><!ENTITY e "x"><testcase name="a"/></testsuite>',
    'unknown entity': '<testsuite><testcase name="&e;"/></testsuite>',
    'unknown element': '<testsuite><testcase name="a"><flakyFailure/></testcase></testsuite>',
    'testcase at root': '<testcase name="a"/>',
    'two roots': '<testsuite/><testsuite/>',
    'no name': '<testsuite><testcase classname="c"/></testsuite>',
    'unquoted': '<testsuite><testcase name=a/></testsuite>',
    'text at root': 'oops<testsuite/>',
  };
  for (const [label, xml] of Object.entries(broken)) {
    assert.throws(() => readJUnit(xml), (/** @type {any} */ err) => err.code === 'report-unreadable' && !err.message.includes('passwd'), label);
    const parsed = parseReport(xml);
    assert.equal(typeof (/** @type {any} */ (parsed).unreadable), 'string', label);
    assert.deepEqual(red(classifyReport({ run: FAILED_RUN, reports: [parsed] })), ['RED_INVALID', 'error', null, []], label);
  }
  assert.throws(() => readJUnit(`<testsuite>${' '.repeat(MAX_REPORT_BYTES)}</testsuite>`), /over 8 MiB/);
  assert.deepEqual(parseReport(Buffer.from([0x3c, 0x61, 0xff, 0x3e])), { unreadable: 'the report is not UTF-8' });

  const none = classifyReport({ run: FAILED_RUN, reports: [] });
  assert.deepEqual([...red(none), none.reason], ['RED_INVALID', 'error', null, [], 'no readable test report']);
  for (const reports of [[missing], [readJUnit(fixture('xctest.xml')), missing], undefined, null, [null], [{ tests: [{ id: 1, status: 'failed' }] }]]) {
    assert.deepEqual(red(classifyReport({ run: FAILED_RUN, reports: /** @type {any} */ (reports) })), ['RED_INVALID', 'error', null, []]);
  }
  // even a missing input never throws
  assert.deepEqual(red(classifyReport(/** @type {any} */ (undefined))), ['RED_INVALID', 'error', null, []]);
  assert.equal(classifyGreen(/** @type {any} */ (undefined)).verdict, 'NOT_GREEN');
});

test('B86 (4): a report with zero tests, or only skipped ones, is RED_INVALID error', () => {
  for (const xml of ['<testsuites><testsuite name="s" tests="0"/></testsuites>', report([{ id: 'a', status: 'skipped' }, { id: 'b', status: 'skipped' }])]) {
    const r = classifyReport({ run: FAILED_RUN, reports: [readJUnit(xml)] });
    assert.deepEqual([...red(r), r.reason], ['RED_INVALID', 'error', 0, [], 'the filter ran no test']);
  }
  // a failed exit whose report has only passing tests proves nothing either
  const r = classifyReport({ run: FAILED_RUN, reports: [readJUnit(report([{ id: 'a', status: 'passed' }]))] });
  assert.deepEqual([...red(r), r.reason], ['RED_INVALID', 'error', 0, [], 'the run failed, but the report has no failure']);
});

test('B86 (5): exit 0 is NOT_RED; a timeout or signal is RED_INVALID fatal; an import or compile line beats a report assertion', () => {
  const reports = [readJUnit(fixture('phpunit.xml'))];
  assert.deepEqual(red(classifyReport({ run: PASSED_RUN, reports })).slice(0, 2), ['NOT_RED', 'none']);
  assert.deepEqual(red(classifyReport({ run: { code: null, signal: 'SIGTERM', timedOut: true }, reports })).slice(0, 2), ['RED_INVALID', 'fatal']);
  assert.deepEqual(red(classifyReport({ run: { code: null, signal: 'SIGKILL' }, reports })).slice(0, 2), ['RED_INVALID', 'fatal']);
  const imp = classifyReport({ run: { code: 1, stderr: "Error [ERR_MODULE_NOT_FOUND]: Cannot find module '/x/src/clamp.mjs'" }, reports });
  assert.deepEqual([imp.verdict, imp.red_kind], ['RED_INVALID', 'import']);
  const php = classifyReport({ run: { code: 2, stdout: 'Class "App\\Support\\Clamp" not found' }, reports });
  assert.deepEqual([php.verdict, php.red_kind], ['RED_INVALID', 'import']);
  const comp = classifyReport({ run: { code: 1, stdout: 'PHP Parse error: syntax error, unexpected token' }, reports });
  assert.deepEqual([comp.verdict, comp.red_kind], ['RED_INVALID', 'compile']);
  const fatal = classifyReport({ run: { code: 255, stdout: 'PHP Fatal error: Allowed memory size of 128 bytes exhausted' }, reports });
  assert.deepEqual([fatal.verdict, fatal.red_kind], ['RED_INVALID', 'fatal']);
});

test('B86 (6): GREEN only on exit 0 with every RED-failed id passed; a missing id, a new failure or zero tests is NOT_GREEN', () => {
  const redIds = classifyReport({ run: FAILED_RUN, reports: [readJUnit(fixture('pest.xml'))] }).failed_ids;
  assert.deepEqual(redIds, ['Tests.Unit.ClampTest.caps high']);
  const allPass = readJUnit(fixture('pest-pass.xml'));
  assert.deepEqual(allPass, { tests: [{ id: 'Tests.Unit.ClampTest.caps high', status: 'passed' }] });
  const green = classifyGreen({ run: PASSED_RUN, reports: [allPass], redFailed: redIds });
  assert.deepEqual([green.verdict, green.failed, green.failed_tests], ['GREEN', 0, []]);

  const cases = [
    ['a missing id', { run: PASSED_RUN, reports: [readJUnit(report([{ id: 'other', status: 'passed' }]))], redFailed: redIds }, 'a test that failed at RED did not pass'],
    ['the id skipped', { run: PASSED_RUN, reports: [readJUnit('<testsuite><testcase classname="Tests.Unit.ClampTest" name="caps high"><skipped/></testcase><testcase name="z"/></testsuite>')], redFailed: redIds }, 'a test that failed at RED did not pass'],
    ['a new failure', { run: PASSED_RUN, reports: [allPass, readJUnit(report([{ id: 'new', status: 'failed' }]))], redFailed: redIds }, 'a test failed'],
    ['zero tests', { run: PASSED_RUN, reports: [readJUnit('<testsuites/>')], redFailed: redIds }, 'the filter ran no test'],
    ['a failed exit', { run: FAILED_RUN, reports: [allPass], redFailed: redIds }, 'the test run did not exit 0'],
    ['a timeout', { run: { code: 0, timedOut: true, signal: 'SIGTERM' }, reports: [allPass], redFailed: redIds }, 'the test run did not exit 0'],
    ['no report', { run: PASSED_RUN, reports: [{ unreadable: 'no report was written' }], redFailed: redIds }, 'no readable test report'],
    ['no RED ids', { run: PASSED_RUN, reports: [allPass], redFailed: [] }, 'no test failed at RED'],
  ];
  for (const [label, input, reason] of cases) {
    const r = classifyGreen(/** @type {any} */ (input));
    assert.deepEqual([r.verdict, r.reason], ['NOT_GREEN', reason], /** @type {string} */ (label));
  }
});

test('B86 (7): failed_tests keeps at most 20 names of at most 200 characters and no message text', () => {
  const MESSAGE = 'FAKE-message-sk-ant-FAKE0123456789';
  const cases = Array.from({ length: 25 }, (_, n) => ({ id: `${String(n).padStart(2, '0')}${'n'.repeat(298)}`, status: 'failed', failure: `type="AssertionError" message="${MESSAGE}"` }));
  const r = classifyReport({ run: { code: 1, stdout: MESSAGE }, reports: [readJUnit(report(cases))] });
  assert.deepEqual([r.verdict, r.red_kind, r.failed], ['RED', 'assertion', 25]);
  assert.equal(r.failed_tests.length, 20);
  assert.deepEqual(r.failed_tests.map((n) => n.length), Array(20).fill(200));
  assert.deepEqual(r.failed_tests.map((n) => n.slice(0, 2)), Array.from({ length: 20 }, (_, n) => String(n).padStart(2, '0')));
  assert.equal(r.failed_ids.length, 25);
  assert.equal(JSON.stringify(r).split(MESSAGE).length - 1, 0);
  const g = classifyGreen({ run: PASSED_RUN, reports: [readJUnit(report(cases))], redFailed: [] });
  assert.equal(g.failed_tests.length, 20);
  assert.equal(JSON.stringify(g).split(MESSAGE).length - 1, 0);
  // C0 and C1 control characters become spaces; 200 characters, not 200 UTF-16 units
  const odd = classifyReport({ run: FAILED_RUN, reports: [readJUnit(report([{ id: `a&#10;b\u009bc\u0080d\u009fe${'😀'.repeat(250)}`, status: 'failed' }]))] });
  assert.equal(odd.failed_tests[0].slice(0, 9), 'a b c d e');
  assert.equal(odd.failed_ids[0].slice(0, 9), 'a\nb\u009bc\u0080d\u009fe');
  assert.equal(Array.from(odd.failed_tests[0]).length, 200);
});

test('B86 (3): a FIFO report path is unreadable at once and never hangs', (t) => {
  const fifo = path.join(TMP, 'report.fifo');
  try {
    execFileSync('mkfifo', [fifo], { stdio: 'ignore' });
  } catch (err) {
    if (/** @type {any} */ (err)?.code === 'ENOENT') return t.skip('mkfifo is not installed');
    throw err;
  }
  // a child process, so a blocking open fails the test on the timeout instead of hanging the suite
  const script = 'const { loadReport } = await import(process.argv[1]); process.stdout.write(JSON.stringify(loadReport(process.argv[2])));';
  const out = execFileSync(process.execPath, ['--input-type=module', '-e', script, JUNIT_URL, fifo], { timeout: 5000, encoding: 'utf8', env: CHILD_ENV });
  assert.deepEqual(JSON.parse(out), { unreadable: 'the report is not a regular file' });
});

test('B86 (1): a bare SwiftPM XCTest failure is RED only with its own XCTest assertion line and no crash', () => {
  const reports = [readJUnit(fixture('xctest.xml'))];
  assert.deepEqual(reports[0].tests, [
    { id: 'KitTests.ClampXCTests.testCapsHigh', status: 'failed', kind: 'error', needs_console: 'xctest' },
    { id: 'KitTests.ClampXCTests.testKeepsInside', status: 'passed' },
  ]);
  const ID = 'KitTests.ClampXCTests.testCapsHigh';
  const LINUX_LINE = '/src/Tests/KitTests/ClampXCTests.swift:6: error: ClampXCTests.testCapsHigh : XCTAssertEqual failed: ("12") is not equal to ("10") - \n';
  const XCTFAIL_LINE = '/src/Tests/KitTests/ClampXCTests.swift:6: error: -[KitTests.ClampXCTests testCapsHigh] : failed - not clamped\n';
  const asRed = [
    ['macOS line on stdout', { stdout: XCTEST_MAC_LINE }],
    ['macOS line on stderr, coloured', { stderr: `\x1b[31m${XCTEST_MAC_LINE}\x1b[0m` }],
    ['Linux line', { stdout: LINUX_LINE }],
    ['XCTFail line', { stdout: XCTFAIL_LINE }],
  ];
  for (const [label, text] of asRed) {
    const r = classifyReport({ run: { code: 1, ...(/** @type {object} */ (text)) }, reports });
    assert.deepEqual(red(r), ['RED', 'assertion', 1, [ID]], /** @type {string} */ (label));
  }
  const asError = [
    ['no console line', ''],
    ['a line for another test', XCTEST_MAC_LINE.replace('testCapsHigh', 'testKeepsInside')],
    ['a thrown error', '/src/ClampXCTests.swift:6: error: -[KitTests.ClampXCTests testCapsHigh] : failed: caught error: "boom"\n'],
    ['a crash next to the line', `${XCTEST_MAC_LINE}Kit/Clamp.swift:3: Fatal error: Unexpectedly found nil\n`],
    ['a signal exit next to the line', `${XCTEST_MAC_LINE}error: Exited with unexpected signal code 4\n`],
  ];
  for (const [label, stdout] of asError) {
    const r = classifyReport({ run: { code: 1, stdout }, reports });
    assert.deepEqual([...red(r), r.reason], ['RED_INVALID', 'error', 1, [ID], 'a test errored or failed without an assertion'], label);
  }
  // only the exact bare shape: a body, or a type, makes it a plain error
  for (const xml of ['<testsuite><testcase classname="K.S" name="t"><failure message="failed">crash</failure></testcase></testsuite>', '<testsuite><testcase classname="K.S" name="t"><failure message="failed"><![CDATA[x]]></failure></testcase></testsuite>', '<testsuite><testcase classname="K.S" name="t"><failure type="Error" message="failed"/></testcase></testsuite>']) {
    assert.deepEqual(readJUnit(xml).tests, [{ id: 'K.S.t', status: 'failed', kind: 'error' }], xml);
    assert.equal(classifyReport({ run: { code: 1, stdout: '/x.swift:1: error: -[K.S t] : XCTAssertTrue failed\n' }, reports: [readJUnit(xml)] }).verdict, 'RED_INVALID', xml);
  }
  // a build failure with no report still never counts
  assert.deepEqual(red(classifyReport({ run: { code: 1, stdout: XCTEST_MAC_LINE }, reports: [{ unreadable: 'no report was written' }] })), ['RED_INVALID', 'error', null, []]);
  // GREEN: a bare failure is a failure
  assert.equal(classifyGreen({ run: PASSED_RUN, reports, redFailed: [ID] }).verdict, 'NOT_GREEN');
});

test('B86 (3): a raw NUL or C0 control in text or an attribute is unreadable; a whitespace CDATA keeps the bare XCTest shape', () => {
  for (const xml of [
    '<testsuite><testcase name="a"><system-out>x\u0000y</system-out></testcase></testsuite>',
    '<testsuite><testcase name="a\u0001b"/></testsuite>',
    '<testsuite><testcase name="a"><![CDATA[\u001f]]></testcase></testsuite>',
    // B87 (B86 leftover): the two noncharacters U+FFFE and U+FFFF, raw in text and in an attribute
    '<testsuite><testcase name="a"><system-out>x\uFFFEy</system-out></testcase></testsuite>',
    '<testsuite><testcase name="a\uFFFFb"/></testsuite>',
  ]) {
    assert.throws(() => readJUnit(xml), { message: 'report-unreadable: a character that is not an XML character' });
    assert.deepEqual(parseReport(Buffer.from(xml)), { unreadable: 'a character that is not an XML character' });
  }
  // tab, LF and CR stay allowed
  assert.deepEqual(readJUnit('<testsuite>\t\r\n<testcase name="a"/></testsuite>').tests, [{ id: 'a', status: 'passed' }]);
  const ws = readJUnit('<testsuite><testcase classname="K.S" name="t"><failure message="failed"><![CDATA[ \n ]]></failure></testcase></testsuite>');
  assert.deepEqual(ws.tests, [{ id: 'K.S.t', status: 'failed', kind: 'error', needs_console: 'xctest' }]);
  const r = classifyReport({ run: { code: 1, stdout: '/x.swift:1: error: -[K.S t] : XCTAssertTrue failed\n' }, reports: [ws] });
  assert.deepEqual(red(r), ['RED', 'assertion', 1, ['K.S.t']]);
});

// ── B89: the captured PHP reports ───────────────────────────────────────────

/**
 * The console of the captured new-class RED steps (B89 live check; paths shortened, colour codes kept). Under an
 * AI agent (`CLAUDECODE` and the like set) Laravel 13 prints one JSON summary line, where the class name is
 * JSON-escaped (`Class \"App\\Support\\Greeting\" not found`); in a plain terminal it prints Collision's text,
 * the class name as is.
 */
const PHP_NEW_CLASS_CONSOLE = {
  json: {
    phpunit:
      '{"tool":"phpunit","result":"failed","tests":1,"passed":0,"assertions":0,"duration_ms":3,"errors":1,"error_details":[{"test":"Tests\\\\Unit\\\\GreetingTest::test_greeting_comes_from_the_owned_class","file":"/x/tests/Unit/GreetingTest.php","line":12,"message":"Class \\"App\\\\Support\\\\Greeting\\" not found"}]}\n',
    pest: '{"tool":"pest","result":"failed","tests":1,"passed":0,"assertions":0,"duration_ms":10,"errors":1,"error_details":[{"test":"P\\\\Tests\\\\Unit\\\\GreetingTest::__pest_evaluable_greeting_comes_from_the_owned_class","file":"/x/tests/Unit/GreetingTest.php","line":6,"message":"Class \\"App\\\\Support\\\\Greeting\\" not found"}]}\n',
  },
  text: '  \u001b[41;1m FAILED \u001b[49;22m \u001b[1mTests\\Unit\\GreetingTest\u001b[22m \u001b[90m>\u001b[39m greeting comes from the owned cl… \u001b[41;1m Error \u001b[49;22m  \n\u001b[39;1m  Class "App\\Support\\Greeting" not found\u001b[39;22m\n',
};

test('B89 (3): the captured PHPUnit and Pest reports: RED assertion, GREEN on the pass report, and a new class is RED_INVALID import from both console forms (JSON agent line and plain text); without a console line it is error', () => {
  const ids = {
    phpunit: { clamp: 'Tests.Unit.ClampTest.test_caps_high', greeting: 'Tests.Unit.GreetingTest.test_greeting_comes_from_the_owned_class' },
    pest: { clamp: 'Tests.Unit.ClampTest.caps high', greeting: 'Tests.Unit.GreetingTest.greeting comes from the owned class' },
  };
  for (const runner of /** @type {const} */ (['phpunit', 'pest'])) {
    const { clamp, greeting } = ids[runner];
    const redRun = classifyReport({ run: FAILED_RUN, reports: [readJUnit(fixture(`${runner}.xml`))] });
    assert.deepEqual(red(redRun), ['RED', 'assertion', 1, [clamp]], runner);
    const pass = readJUnit(fixture(`${runner}-pass.xml`));
    assert.deepEqual(pass, { tests: [{ id: clamp, status: 'passed' }] }, runner);
    const green = classifyGreen({ run: PASSED_RUN, reports: [pass], redFailed: redRun.failed_ids });
    assert.deepEqual([green.verdict, green.failed, green.failed_tests], ['GREEN', 0, []], runner);
    // the pass report is not RED; the RED report is not GREEN
    assert.equal(classifyReport({ run: PASSED_RUN, reports: [pass] }).verdict, 'NOT_RED', runner);
    assert.equal(classifyGreen({ run: FAILED_RUN, reports: [readJUnit(fixture(`${runner}.xml`))], redFailed: redRun.failed_ids }).verdict, 'NOT_GREEN', runner);

    const newClass = readJUnit(fixture(`${runner}-new-class.xml`));
    assert.deepEqual(newClass, { tests: [{ id: greeting, status: 'error', kind: 'error' }] }, runner);
    const fromJson = classifyReport({ run: { ...FAILED_RUN, code: 2, stdout: PHP_NEW_CLASS_CONSOLE.json[runner] }, reports: [newClass] });
    assert.deepEqual([fromJson.verdict, fromJson.red_kind, fromJson.failed_tests], ['RED_INVALID', 'import', [greeting]], runner);
    // the report alone (no console line) still never proves: its <error> is RED_INVALID error
    const reportOnly = classifyReport({ run: { ...FAILED_RUN, code: 2 }, reports: [newClass] });
    assert.deepEqual([reportOnly.verdict, reportOnly.red_kind, reportOnly.failed_tests], ['RED_INVALID', 'error', [greeting]], runner);
    const fromText = classifyReport({ run: { ...FAILED_RUN, code: 2, stdout: PHP_NEW_CLASS_CONSOLE.text }, reports: [newClass] });
    assert.deepEqual([fromText.verdict, fromText.red_kind, fromText.failed_tests], ['RED_INVALID', 'import', [greeting]], runner);
  }
});

test('B89 (3): the reader does not depend on the root suite name (PHPUnit and Pest name it after the test class)', () => {
  for (const file of ['phpunit.xml', 'pest.xml', 'phpunit-new-class.xml', 'pest-new-class.xml']) {
    const original = fixture(file);
    const renamed = original.replace(/<testsuite name="[^"]*"/, '<testsuite name="Other Suite"');
    assert.notEqual(renamed, original, file);
    assert.deepEqual(readJUnit(renamed), readJUnit(original), file);
    assert.deepEqual(readJUnit(original.replace(/<testsuite name="[^"]*"/, '<testsuite')), readJUnit(original), file);
  }
});

test('B89 (2): a captured full-config PHPUnit run (no --filter): nested suites, exact ids and counts, whatever the root suite is named', () => {
  const original = fixture('phpunit-full.xml');
  // the captured shape: <testsuites> > root suite (the config path) > "Unit" / "Feature" > class suites
  const names = [...original.matchAll(/<testsuite name="([^"]*)"/g)].map((m) => m[1]);
  assert.deepEqual(names.slice(1), ['Unit', 'Tests\\Unit\\ClampTest', 'Tests\\Unit\\ExampleTest', 'Tests\\Unit\\GreetingTest', 'Feature', 'Tests\\Feature\\ExampleTest', 'Tests\\Feature\\UserCountTest']);
  assert.equal(path.basename(names[0]), 'phpunit.xml');
  const expected = {
    tests: [
      { id: 'Tests.Unit.ClampTest.test_caps_high', status: 'failed', kind: 'assertion' },
      { id: 'Tests.Unit.ClampTest.test_keeps_inside', status: 'passed' },
      { id: 'Tests.Unit.ExampleTest.test_that_true_is_true', status: 'passed' },
      { id: 'Tests.Unit.GreetingTest.test_greeting_comes_from_the_owned_class', status: 'passed' },
      { id: 'Tests.Feature.ExampleTest.test_the_application_returns_a_successful_response', status: 'passed' },
      { id: 'Tests.Feature.UserCountTest.test_a_user_is_written_to_the_in_memory_database', status: 'passed' },
    ],
  };
  assert.deepEqual(readJUnit(original), expected);
  const redRun = classifyReport({ run: FAILED_RUN, reports: [readJUnit(original)] });
  assert.deepEqual(red(redRun), ['RED', 'assertion', 1, ['Tests.Unit.ClampTest.test_caps_high']]);
  // the root suite's name (an absolute config path) never matters: renamed, emptied or dropped, the same result
  const root = `<testsuite name="${names[0]}"`;
  assert.equal(original.split(root).length, 2);
  for (const variant of [original.replace(root, '<testsuite name="Other Suite"'), original.replace(root, '<testsuite name=""'), original.replace(root, '<testsuite')]) {
    assert.notEqual(variant, original);
    assert.deepEqual(readJUnit(variant), expected);
    assert.deepEqual(red(classifyReport({ run: FAILED_RUN, reports: [readJUnit(variant)] })), red(redRun));
  }
});
