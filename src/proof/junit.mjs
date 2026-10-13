/**
 * `junit` (issue #7, red-green plan §1.1, B86): read a JUnit XML test report and classify the RED
 * and GREEN steps of a red→green run from it, instead of from the console text.
 *
 * Fail closed everywhere: a report that is missing, empty, over 8 MiB, malformed, not UTF-8, has a
 * `<!DOCTYPE`/`<!ENTITY` (no entity expansion, no XXE) or an element this reader does not know is
 * unreadable, and an unreadable report never proves anything. Pure apart from `loadReport`, which
 * reads one file. No dependency.
 *
 * The common shape (`ReportResult`): `{tests: [{id, status, kind?, needs_console?}]}` or
 * `{unreadable: reason}`. `id` is `classname.name` (or `name` with no classname). A `<failure>` is
 * `assertion` only when its `type` names an assertion, or it has no `type` and its `message` names
 * one; any other `<failure>` and every `<error>` is `error`.
 *
 * SwiftPM's XCTest report says nothing about the cause: every failed test is
 * `<failure message="failure"></failure>` (SwiftPM 6.3 on macOS, captured by the B88 live check;
 * older and Linux builds write `message="failed"`), with no `type` and no body. Such a test is read
 * as `kind: error`
 * with `needs_console: 'xctest'`, and the RED step counts it as an assertion only when the console
 * text has an XCTest assertion line for that very test (`<file>:<line>: error: -[Module.Suite test]
 * : XCTAssert… failed` on macOS, `… error: Suite.test : XCTAssert… failed` on Linux, or XCTFail's
 * `: failed - <message>`) and no crash line (`Fatal error`, `Precondition failed`, `Assertion
 * failed`, `Unexpectedly found nil`, `signal code <n>`, `Illegal instruction`, `Segmentation
 * fault`). A thrown error (`: failed: caught error`) is not an assertion line. Else it stays `error`.
 *
 * `classifyReport` (RED step), first row that applies wins:
 *
 *   verdict      red_kind    when
 *   ───────────  ──────────  ──────────────────────────────────────────────────────────────────
 *   NOT_RED      none        exit 0, no signal
 *   RED_INVALID  fatal       a timeout or a signal
 *   RED_INVALID  import|compile|fatal   the console text hits a `parseRed` invalid family
 *   RED_INVALID  compile     no report was written (every report is `NO_REPORT`) and the
 *                            caller's compile check hits the console text (`swiftCompileError`
 *                            for the `swift` adapter, B88)
 *   RED_INVALID  error       no report, or one is unreadable
 *   RED_INVALID  error       the report has no test that ran (zero, or only skipped)
 *   RED_INVALID  error       a test errored, or failed without an assertion
 *   RED          assertion   at least one test failed on an assertion
 *   RED_INVALID  error       the run failed, but the report has no failure
 *
 * The result keeps names only: `failed_tests` holds at most 20 test ids of at most 200
 * characters, never a message or an excerpt. `failed_ids` is the full list, for the GREEN step
 * of the same call only; it never goes into a ledger row.
 */

import { closeSync, constants, fstatSync, openSync, readSync } from 'node:fs';
import { parseRed } from './red-parse.mjs';

/** @typedef {'passed' | 'failed' | 'error' | 'skipped'} TestStatus */
/** @typedef {{id: string, status: TestStatus, kind?: 'assertion' | 'error', needs_console?: 'xctest'}} ReportTest */
/** @typedef {{tests: ReportTest[]} | {unreadable: string}} ReportResult */
/** @typedef {{code: number | null, signal?: string | null, timedOut?: boolean, stdout?: string, stderr?: string}} RunResult */
/**
 * @typedef {{verdict: 'NOT_RED' | 'RED' | 'RED_INVALID', red_kind: 'none' | 'assertion' | 'import' | 'compile' | 'fatal' | 'error',
 *   framework: 'junit', failed: number | null, failed_tests: string[], failed_ids: string[], reason: string}} JUnitRedParse
 */
/** @typedef {{verdict: 'GREEN' | 'NOT_GREEN', failed: number | null, failed_tests: string[], reason: string}} JUnitGreenParse */

/** A report larger than this many bytes is refused. */
export const MAX_REPORT_BYTES = 8 * 1024 * 1024;
/**
 * What `loadReport` returns for a report file that does not exist: one shared frozen object, so
 * the compile rule can tell "no report was written" apart from every other unreadable report by
 * identity, never by its message.
 */
export const NO_REPORT = Object.freeze({ unreadable: 'no report was written' });
/** At most this many failed test names are kept. */
export const MAX_FAILED_TESTS = 20;
/** Each kept name is cut to this many characters. */
export const MAX_TEST_NAME = 200;

/** The `type` or `message` of an assertion failure, per runner. */
const ASSERTION_PATTERNS = Object.freeze([
  /\bAssertionError\b/, // node:assert and pytest. Not Jest: its report names no assertion type, so Jest RED is not supported yet
  /\bERR_ASSERTION\b/, // node:assert
  /\bExpectationFailedException\b/, // PHPUnit and Pest
  /\bAssertionFailedError\b/, // PHPUnit's assertion base class ($this->fail())
  /\bFailed asserting that\b/, // PHPUnit and Pest messages
  /\bXCTAssert\w*/, // XCTest
  /\bXCTFail\b/, // XCTest
  /\bExpectation failed\b/, // Swift Testing #expect
  /^\s*assert\s/, // pytest's rewritten assert message
]);

const ANSI = /\x1b\[[0-9;?]*[A-Za-z]/g;
/** An XCTest assertion line: `-[Module.Suite test] : …` (macOS) or `Suite.test : …` (Linux). */
const XCTEST_LINE = /:\d+: error: (?:-\[([^\s\]]+) ([^\s\]]+)\]|([^\s:]+)) : (?:XCTAssert\w* failed\b|failed - )/g;
/**
 * `swiftc` and SwiftPM build-failure lines: `<file>.swift:<line>:<col>: error:` (a compiler
 * diagnostic always has a column; XCTest's assertion line `<file>.swift:<line>: error: -[…]` has
 * none), and SwiftPM's own `error: emit-module command failed` and `error: compile command
 * failed`. SwiftPM 6.3's closing `error: fatalError` is left out: it is SwiftPM's generic exit
 * line, not a compiler diagnostic, and in the B88 live check it only follows the `swiftc` line.
 */
const SWIFT_COMPILE = [/\.swift:\d+:\d+: error: /, /^error: (?:emit-module command failed|compile command failed)\b/m];
/** A crash in the test process: a bare XCTest failure next to one of these is never an assertion. */
const SWIFT_CRASH = /\bFatal error\b|\bPrecondition failed\b|\bAssertion failed\b|\bUnexpectedly found nil\b|\bsignal code \d+|\bIllegal instruction\b|\bSegmentation fault\b/;

/** Which parents each known element may have (`null` = the document root). */
const PARENTS = Object.freeze({
  testsuites: [null],
  testsuite: [null, 'testsuites', 'testsuite'],
  testcase: ['testsuite'],
  failure: ['testcase'],
  error: ['testcase'],
  skipped: ['testcase'],
  'system-out': ['testsuites', 'testsuite', 'testcase'],
  'system-err': ['testsuites', 'testsuite', 'testcase'],
  properties: ['testsuites', 'testsuite', 'testcase'],
  property: ['properties'],
});

const NAME = /[A-Za-z_][A-Za-z0-9_.:-]*/y;
const SPACE = /[ \t\r\n]*/y;
const PREDEFINED = Object.freeze({ lt: '<', gt: '>', amp: '&', quot: '"', apos: "'" });

/** @param {string} reason @returns {never} */
function unreadable(reason) {
  const err = new Error(`report-unreadable: ${reason}`);
  /** @type {any} */ (err).code = 'report-unreadable';
  throw err;
}

/** @param {number} cp @returns {boolean} an XML 1.0 `Char` */
const isXmlChar = (cp) => cp === 0x9 || cp === 0xa || cp === 0xd || (cp >= 0x20 && cp <= 0xd7ff) || (cp >= 0xe000 && cp <= 0xfffd) || (cp >= 0x10000 && cp <= 0x10ffff);
/** Any raw code point (or lone surrogate) that `isXmlChar` refuses: NUL, C0 controls but tab/LF/CR, U+FFFE, U+FFFF. */
const NOT_XML_CHAR = /[^\t\n\r\x20-\uD7FF\uE000-\uFFFD\u{10000}-\u{10FFFF}]/u;

/**
 * Decode the five predefined entities and character references; any other `&` refuses.
 * @param {string} raw @returns {string}
 */
function decode(raw) {
  if (!raw.includes('&')) return raw;
  let out = '';
  let i = 0;
  for (;;) {
    const amp = raw.indexOf('&', i);
    if (amp === -1) return out + raw.slice(i);
    out += raw.slice(i, amp);
    const semi = raw.indexOf(';', amp);
    if (semi === -1 || semi - amp > 12) unreadable('a bare & or an unknown entity');
    const ref = raw.slice(amp + 1, semi);
    if (Object.hasOwn(PREDEFINED, ref)) out += PREDEFINED[ref];
    else {
      const m = /^#(?:([0-9]{1,7})|x([0-9A-Fa-f]{1,6}))$/.exec(ref);
      if (!m) unreadable('an unknown entity');
      const cp = m[1] !== undefined ? Number(m[1]) : Number.parseInt(m[2], 16);
      if (!isXmlChar(cp)) unreadable('a character reference that is not an XML character');
      out += String.fromCodePoint(cp);
    }
    i = semi + 1;
  }
}

/**
 * Parse a start tag's attributes from `pos` (just after the element name).
 * @param {string} text @param {number} pos
 * @returns {{attrs: Map<string, string>, end: number, selfClosing: boolean}}
 */
function readAttributes(text, pos) {
  const attrs = new Map();
  let i = pos;
  for (;;) {
    SPACE.lastIndex = i;
    SPACE.exec(text);
    const hadSpace = SPACE.lastIndex > i;
    i = SPACE.lastIndex;
    if (text.startsWith('/>', i)) return { attrs, end: i + 2, selfClosing: true };
    if (text[i] === '>') return { attrs, end: i + 1, selfClosing: false };
    if (!hadSpace) unreadable('a malformed start tag');
    NAME.lastIndex = i;
    const name = NAME.exec(text)?.[0];
    if (!name) unreadable('a malformed attribute');
    i = NAME.lastIndex;
    SPACE.lastIndex = i;
    SPACE.exec(text);
    i = SPACE.lastIndex;
    if (text[i] !== '=') unreadable('an attribute without a value');
    SPACE.lastIndex = i + 1;
    SPACE.exec(text);
    i = SPACE.lastIndex;
    const quote = text[i];
    if (quote !== '"' && quote !== "'") unreadable('an unquoted attribute value');
    const close = text.indexOf(quote, i + 1);
    if (close === -1) unreadable('an unterminated attribute value');
    const raw = text.slice(i + 1, close);
    if (raw.includes('<')) unreadable('a < inside an attribute value');
    if (attrs.has(name)) unreadable('a duplicate attribute');
    attrs.set(name, decode(raw));
    i = close + 1;
  }
}

/**
 * @param {Map<string, string>} attrs
 * @returns {'assertion' | 'error' | 'bare'} `bare` = SwiftPM's XCTest shape, `message="failure"` (or
 *   `"failed"`) and no `type` (it also needs an empty body, checked at the end tag).
 */
function failureKind(attrs) {
  const type = (attrs.get('type') ?? '').trim();
  if (type !== '') return ASSERTION_PATTERNS.some((p) => p.test(type)) ? 'assertion' : 'error';
  const message = attrs.get('message') ?? '';
  if (message.trim() === 'failed' || message.trim() === 'failure') return 'bare';
  return ASSERTION_PATTERNS.some((p) => p.test(message)) ? 'assertion' : 'error';
}

/**
 * Read a JUnit XML report.
 * @param {string | Uint8Array} input - the report text, or its bytes (UTF-8).
 * @returns {{tests: ReportTest[]}}
 * @throws {Error} `code: 'report-unreadable'` for anything this reader does not accept; the
 *   message names the reason only, never report content.
 */
export function readJUnit(input) {
  let text;
  if (typeof input === 'string') {
    if (Buffer.byteLength(input, 'utf8') > MAX_REPORT_BYTES) unreadable('the report is over 8 MiB');
    text = input;
  } else if (input instanceof Uint8Array) {
    if (input.byteLength > MAX_REPORT_BYTES) unreadable('the report is over 8 MiB');
    try {
      text = new TextDecoder('utf-8', { fatal: true }).decode(input);
    } catch {
      unreadable('the report is not UTF-8');
    }
  } else unreadable('the report is not text');
  if (text.charCodeAt(0) === 0xfeff) text = text.slice(1);
  // raw text, attribute values, CDATA and comments must hold XML characters only, as character references must
  if (NOT_XML_CHAR.test(text)) unreadable('a character that is not an XML character');

  /** @type {ReportTest[]} */
  const tests = [];
  /** @typedef {{id: string, failures: Array<'assertion' | 'error' | 'bare'>, errors: number, skipped: number}} OpenTest */
  /** @type {{name: string, test?: OpenTest, failure?: number, body?: boolean}[]} */
  const stack = [];
  let rootSeen = false;
  let i = 0;

  /** @param {string} name @param {Map<string, string>} attrs */
  const open = (name, attrs) => {
    if (!Object.hasOwn(PARENTS, name)) unreadable('an element this reader does not know');
    const parent = stack.length ? stack[stack.length - 1].name : null;
    if (parent === null && rootSeen) unreadable('more than one root element');
    if (!PARENTS[name].includes(parent)) unreadable('an element in the wrong place');
    if (parent === null) rootSeen = true;
    /** @type {{name: string, test?: OpenTest, failure?: number, body?: boolean}} */
    const frame = { name };
    if (name === 'testcase') {
      const caseName = attrs.get('name');
      if (!caseName) unreadable('a testcase without a name');
      const classname = attrs.get('classname');
      frame.test = { id: classname ? `${classname}.${caseName}` : caseName, failures: [], errors: 0, skipped: 0 };
    } else if (name === 'failure' || name === 'error' || name === 'skipped') {
      const test = stack[stack.length - 1].test;
      if (name === 'failure') frame.failure = test.failures.push(failureKind(attrs)) - 1;
      else if (name === 'error') test.errors += 1;
      else test.skipped += 1;
    }
    stack.push(frame);
  };

  /** @param {string} name */
  const close = (name) => {
    const frame = stack.pop();
    if (!frame || frame.name !== name) unreadable('a mismatched end tag');
    if (frame.failure !== undefined && frame.body) {
      const owner = stack[stack.length - 1].test;
      if (owner.failures[frame.failure] === 'bare') owner.failures[frame.failure] = 'error';
    }
    const t = frame.test;
    if (!t) return;
    if (t.errors > 0) tests.push({ id: t.id, status: 'error', kind: 'error' });
    else if (t.failures.length > 0 && t.failures.every((k) => k === 'assertion')) tests.push({ id: t.id, status: 'failed', kind: 'assertion' });
    else if (t.failures.length > 0 && t.failures.every((k) => k !== 'error')) tests.push({ id: t.id, status: 'failed', kind: 'error', needs_console: 'xctest' });
    else if (t.failures.length > 0) tests.push({ id: t.id, status: 'failed', kind: 'error' });
    else if (t.skipped > 0) tests.push({ id: t.id, status: 'skipped' });
    else tests.push({ id: t.id, status: 'passed' });
  };

  while (i < text.length) {
    const lt = text.indexOf('<', i);
    const chunk = text.slice(i, lt === -1 ? text.length : lt);
    if (stack.length === 0) {
      if (!/^[ \t\r\n]*$/.test(chunk)) unreadable('text outside the root element');
    } else {
      decode(chunk);
      if (/[^ \t\r\n]/.test(chunk)) stack[stack.length - 1].body = true;
    }
    if (lt === -1) break;
    i = lt;
    if (text.startsWith('<!--', i)) {
      const end = text.indexOf('-->', i + 4);
      if (end === -1) unreadable('an unterminated comment');
      i = end + 3;
    } else if (text.startsWith('<![CDATA[', i)) {
      if (stack.length === 0) unreadable('CDATA outside the root element');
      const end = text.indexOf(']]>', i + 9);
      if (end === -1) unreadable('an unterminated CDATA section');
      if (/[^ \t\r\n]/.test(text.slice(i + 9, end))) stack[stack.length - 1].body = true;
      i = end + 3;
    } else if (text.startsWith('<!', i)) {
      unreadable('a DOCTYPE or entity declaration');
    } else if (text.startsWith('<?', i)) {
      const end = text.indexOf('?>', i + 2);
      if (end === -1) unreadable('an unterminated processing instruction');
      i = end + 2;
    } else if (text.startsWith('</', i)) {
      NAME.lastIndex = i + 2;
      const name = NAME.exec(text)?.[0];
      if (!name) unreadable('a malformed end tag');
      SPACE.lastIndex = NAME.lastIndex;
      SPACE.exec(text);
      if (text[SPACE.lastIndex] !== '>') unreadable('a malformed end tag');
      close(name);
      i = SPACE.lastIndex + 1;
    } else {
      NAME.lastIndex = i + 1;
      const name = NAME.exec(text)?.[0];
      if (!name) unreadable('a malformed start tag');
      const { attrs, end, selfClosing } = readAttributes(text, NAME.lastIndex);
      open(name, attrs);
      if (selfClosing) close(name);
      i = end;
    }
  }
  if (stack.length > 0) unreadable('an unclosed element');
  if (!rootSeen) unreadable('no root element');
  return { tests };
}

/**
 * `readJUnit` that never throws.
 * @param {string | Uint8Array} input @returns {ReportResult}
 */
export function parseReport(input) {
  try {
    return readJUnit(input);
  } catch (err) {
    const message = err instanceof Error ? err.message : '';
    return { unreadable: message.startsWith('report-unreadable: ') ? message.slice('report-unreadable: '.length) : 'the report could not be read' };
  }
}

/**
 * Read and parse one report file. Never throws. A symlink, a non-regular file (a FIFO is opened
 * non-blocking, so it never hangs), a missing file or a file over `MAX_REPORT_BYTES` is
 * unreadable.
 * @param {string} file @returns {ReportResult}
 */
export function loadReport(file) {
  let fd;
  try {
    fd = openSync(file, constants.O_RDONLY | constants.O_NONBLOCK | constants.O_NOFOLLOW);
  } catch (err) {
    return /** @type {any} */ (err)?.code === 'ENOENT' ? NO_REPORT : { unreadable: 'the report could not be opened' };
  }
  try {
    const st = fstatSync(fd);
    if (!st.isFile()) return { unreadable: 'the report is not a regular file' };
    return readReportFd(fd, st.size);
  } catch {
    return { unreadable: 'the report could not be read' };
  } finally {
    closeSync(fd);
  }
}

/**
 * Read an open report whose `fstat` size was `size`, into a buffer of `min(size, cap) + 1` bytes.
 * A file over the cap, or one that grew or shrank after `fstat`, is unreadable. Never throws.
 * @param {number} fd @param {number} size @returns {ReportResult}
 */
export function readReportFd(fd, size) {
  try {
    if (size > MAX_REPORT_BYTES) return { unreadable: 'the report is over 8 MiB' };
    const buf = Buffer.alloc(size + 1);
    let n = 0;
    for (;;) {
      const got = readSync(fd, buf, n, buf.length - n, null);
      if (got === 0 || (n += got) === buf.length) break;
    }
    if (n !== size) return { unreadable: 'the report changed while it was read' };
    return parseReport(buf.subarray(0, n));
  } catch {
    return { unreadable: 'the report could not be read' };
  }
}

/** @param {string} id @returns {string} the id as a row may keep it: no C0 or C1 control characters, at most 200 characters */
function boundedName(id) {
  // eslint-disable-next-line no-control-regex
  return Array.from(id.replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ')).slice(0, MAX_TEST_NAME).join('');
}

/**
 * Merge the reports of one step. Fail closed: no report or any unreadable one ⇒ `null`.
 * @param {unknown} reports @returns {ReportTest[] | null}
 */
function mergeReports(reports) {
  if (!Array.isArray(reports) || reports.length === 0) return null;
  /** @type {ReportTest[]} */
  const all = [];
  for (const r of reports) {
    if (!r || typeof r !== 'object' || !Array.isArray(/** @type {any} */ (r).tests)) return null;
    for (const t of /** @type {any} */ (r).tests) {
      if (!t || typeof t.id !== 'string' || !['passed', 'failed', 'error', 'skipped'].includes(t.status)) return null;
      if (t.needs_console !== undefined && t.needs_console !== 'xctest') return null;
      all.push(t);
    }
  }
  return all;
}

/** @param {ReportTest[] | null} tests */
function failures(tests) {
  const failedIds = tests ? tests.filter((t) => t.status === 'failed' || t.status === 'error').map((t) => t.id) : [];
  return {
    failed: tests ? failedIds.length : null,
    failed_tests: failedIds.slice(0, MAX_FAILED_TESTS).map(boundedName),
    failed_ids: failedIds,
  };
}

/**
 * The ids the console text shows an XCTest assertion line for, or none when it shows a crash.
 * @param {string} text - the run's stdout and stderr, colours stripped.
 * @returns {{ids: Set<string>, tails: Set<string>}} `-[Module.Suite test]` ids, and `Suite.test` tails
 */
function xctestAssertions(text) {
  const ids = new Set();
  const tails = new Set();
  if (SWIFT_CRASH.test(text)) return { ids, tails };
  for (const m of text.matchAll(XCTEST_LINE)) {
    if (m[1] !== undefined) ids.add(`${m[1]}.${m[2]}`);
    else tails.add(m[3]);
  }
  return { ids, tails };
}

/**
 * A bare XCTest failure counts as an assertion only with its own assertion line in the console.
 * @param {ReportTest[]} tests @param {RunResult} run @returns {ReportTest[]}
 */
function confirmXCTest(tests, run) {
  if (!tests.some((t) => t.needs_console === 'xctest')) return tests;
  const { ids, tails } = xctestAssertions(`${run.stdout ?? ''}\n${run.stderr ?? ''}`.replace(ANSI, ''));
  const seen = (/** @type {string} */ id) => ids.has(id) || [...tails].some((tail) => id === tail || id.endsWith(`.${tail}`));
  return tests.map((t) => (t.needs_console === 'xctest' && seen(t.id) ? { id: t.id, status: t.status, kind: 'assertion' } : t));
}

/**
 * Does the console text show a Swift compile or build failure (§1.3 item 3)? Colours are stripped
 * first. An XCTest assertion line is not one. Used by the `swift` adapter only (B102 adds the
 * `xcodebuild` one).
 * @param {string} text @returns {boolean}
 */
export function swiftCompileError(text) {
  const plain = String(text).replace(ANSI, '');
  return SWIFT_COMPILE.some((p) => p.test(plain));
}

/** @param {RunResult} run @returns {boolean} */
const exitedClean = (run) => run.code === 0 && !run.signal && !run.timedOut;

/**
 * Classify the RED step from the run and its parsed reports. Never throws.
 * @param {{run: RunResult, reports: ReportResult[], compileError?: (text: string) => boolean}} input -
 *   `compileError`: the adapter's compile check, asked only when no report was written at all
 *   (a non-empty `reports` that holds only `NO_REPORT`)
 * @returns {JUnitRedParse}
 */
export function classifyReport(input) {
  try {
    const { run, reports, compileError } = input;
    const tests = mergeReports(reports);
    const base = { framework: /** @type {const} */ ('junit'), ...failures(tests) };
    /** @param {JUnitRedParse['verdict']} verdict @param {JUnitRedParse['red_kind']} red_kind @param {string} reason @returns {JUnitRedParse} */
    const out = (verdict, red_kind, reason) => ({ verdict, red_kind, ...base, reason });
    if (exitedClean(run)) return out('NOT_RED', 'none', 'the test passed without the change');
    if (run.timedOut || run.signal) return out('RED_INVALID', 'fatal', run.timedOut ? 'the test run timed out' : 'the test run was killed by a signal');
    const text = parseRed(run);
    if (text.verdict === 'RED_INVALID' && (text.red_kind === 'import' || text.red_kind === 'compile' || text.red_kind === 'fatal')) {
      return out('RED_INVALID', text.red_kind, text.reason);
    }
    const noneWritten = Array.isArray(reports) && reports.length > 0 && reports.every((r) => r === NO_REPORT);
    if (!tests && noneWritten && typeof compileError === 'function' && compileError(`${run.stdout ?? ''}\n${run.stderr ?? ''}`)) {
      return out('RED_INVALID', 'compile', 'the test did not compile');
    }
    if (!tests) return out('RED_INVALID', 'error', 'no readable test report');
    if (!tests.some((t) => t.status !== 'skipped')) return out('RED_INVALID', 'error', 'the filter ran no test');
    const confirmed = confirmXCTest(tests, run);
    if (confirmed.some((t) => t.kind === 'error' || t.status === 'error')) return out('RED_INVALID', 'error', 'a test errored or failed without an assertion');
    if (confirmed.some((t) => t.status === 'failed' && t.kind === 'assertion')) return out('RED', 'assertion', 'an assertion failed');
    return out('RED_INVALID', 'error', 'the run failed, but the report has no failure');
  } catch {
    return { verdict: 'RED_INVALID', red_kind: 'error', framework: 'junit', failed: null, failed_tests: [], failed_ids: [], reason: 'no readable test report' };
  }
}

/**
 * Classify the GREEN step: `GREEN` only when the run exited 0, every report is readable, at least
 * one test ran, none failed or errored, and every id that failed at RED is in the report and
 * passed. Never throws.
 * @param {{run: RunResult, reports: ReportResult[], redFailed: string[]}} input
 * @returns {JUnitGreenParse}
 */
export function classifyGreen(input) {
  try {
    const { run, reports, redFailed } = input;
    const tests = mergeReports(reports);
    const { failed, failed_tests } = failures(tests);
    const base = { failed, failed_tests };
    /** @param {JUnitGreenParse['verdict']} verdict @param {string} reason @returns {JUnitGreenParse} */
    const out = (verdict, reason) => ({ verdict, ...base, reason });
    if (!exitedClean(run)) return out('NOT_GREEN', 'the test run did not exit 0');
    if (!tests) return out('NOT_GREEN', 'no readable test report');
    if (!tests.some((t) => t.status !== 'skipped')) return out('NOT_GREEN', 'the filter ran no test');
    if (tests.some((t) => t.status === 'failed' || t.status === 'error')) return out('NOT_GREEN', 'a test failed');
    if (!Array.isArray(redFailed) || redFailed.length === 0) return out('NOT_GREEN', 'no test failed at RED');
    for (const id of redFailed) {
      const runs = tests.filter((t) => t.id === id);
      if (runs.length === 0 || runs.some((t) => t.status !== 'passed')) return out('NOT_GREEN', 'a test that failed at RED did not pass');
    }
    return out('GREEN', 'every test that failed at RED passed');
  } catch {
    return { verdict: 'NOT_GREEN', failed: null, failed_tests: [], reason: 'no readable test report' };
  }
}
