/**
 * The test-quality note (block B92, issue #10 item 3): a test file that reads a source file and
 * asserts on its text proves text, not behaviour. Its review packets get a `## test-quality`
 * section before the diff that names the lines (never their text), so the reviewer looks at them.
 * The lenses tell the reviewer to raise such a test as a `warning` unless the read file is a
 * fixture. The note is a hint, never a tool-made finding: no row, no gate change.
 *
 * The detector is pure (path and content in, line numbers out; no I/O):
 *  - it runs only on a test file: `isTestPath` (`test/ tests/ __tests__/ spec/` dirs, `*.test.*`,
 *    `*.spec.*`, `*Test.php`, `*_test.go`, `test_*.py`) plus a `Tests/` dir and a name ending in
 *    `Tests.<ext>` or `Test.<ext>` (`FooTests.swift`, `FooTest.kt`). A docs file (`.md`, `.txt`)
 *    and a file under a fixtures dir are never checked;
 *  - a READ line holds a read call ({@link READ_CALLS}) and, in the call's span, either a quoted
 *    path literal that ends in a source extension ({@link SOURCE_EXTENSIONS}), or a name bound on an
 *    earlier line to such a literal (`const SRC = 'src/a.mjs'` … `readFileSync(SRC)`). A name is
 *    bound only when its value IS the literal: the whole right-hand side, or the literal passed
 *    straight to `join`/`path.join`/`resolve`/`path.resolve`; the binding may carry modifiers and a
 *    type (`private val`, `const val`, `private static let`, `private static final String`,
 *    `private const string`, `export const`). The span is the call's line, plus up to
 *    {@link SPAN_LINES} following lines while the read call's own `(` is still open (a formatter
 *    split); the call's line is the one reported. A span with any string literal that has a
 *    fixtures dir segment (`fixtures`, `fixture`, `__fixtures__`) — `'test/fixtures/a.mjs'` or
 *    `join(dir, 'fixtures', 'a.mjs')` — or that names a name bound to such a literal
 *    (`const FIXTURES = join(dir, 'fixtures')`) is a fixture read, never a source read;
 *  - a TEXT-ASSERT line holds a contains or match call ({@link ASSERT_CALLS});
 *  - the file is flagged only when it has at least one of each. The flagged lines are every read
 *    line and every text-assert line, 1-based, sorted; the section lists at most
 *    {@link MAX_LINES} of them and counts the rest.
 */

import { isTestPath } from '../gates/expand.mjs';

/** The section names at most this many line numbers. */
export const MAX_LINES = 10;

/** The file extensions of source code a test should not read for its text. */
export const SOURCE_EXTENSIONS = Object.freeze(['swift', 'ts', 'tsx', 'js', 'mjs', 'cjs', 'py', 'go', 'rs', 'kt', 'java', 'rb', 'php', 'cs']);

/** Common read calls, matched as plain text. */
export const READ_CALLS = Object.freeze(['readFileSync', 'readFile', 'open(', 'read_text', 'String(contentsOf', 'contentsOfFile', 'File.read', 'os.ReadFile', 'fs::read_to_string', 'readText(', 'readString(', 'ReadAllText(']);

/** A read call's path may sit on at most this many lines after the call (a formatter split). */
export const SPAN_LINES = 3;

/** Contains or match calls on text, matched as plain text (`XCTAssertTrue(x.contains(…))` holds `contains(`). */
export const ASSERT_CALLS = Object.freeze(['includes(', 'contains(', 'toContain(', 'toMatch(', 'assert.match(', 'assertIn(', 'strings.Contains(']);

const DOCS = /\.(md|txt)$/i;
const FIXTURE_DIRS = new Set(['fixtures', 'fixture', '__fixtures__']);
/** A quoted string literal (single, double or back quote) with no quote or newline inside. */
const LITERAL = /(['"`])([^'"`\n]*)\1/g;
const SOURCE_END = new RegExp(`\\.(${SOURCE_EXTENSIONS.join('|')})$`);
/**
 * A binding at the line start: modifiers (`export`, `private`, `static`, `final`, `const`, …), then
 * `const|let|var|val X` (JS, Swift, Kotlin) or an optional type and `X` (Java `String X`, Python
 * `X`, Go `X :=`), an optional `: Type`, and `=` (not `==`). Group 1 or 2 is the name.
 */
const BINDING = /^\s*(?:(?:export|public|private|protected|internal|fileprivate|open|static|final|readonly|lazy|const)\s+)*(?:(?:const|let|var|val)\s+([A-Za-z_$][\w$]*)|(?:[A-Za-z_][\w.<>[\],]*\s+)?([A-Za-z_$][\w$]*))\s*(?::[^=]*)?=(?!=)/;

/** @param {string} p @returns {string[]} the `/`-separated segments of a path. */
const segments = (p) => p.split('/');

/** @param {string} p @returns {boolean} whether `p` has a fixtures dir segment. */
const underFixtures = (p) => segments(p).some((s) => FIXTURE_DIRS.has(s));

/**
 * Whether the detector checks `file`: a test file that is not a docs file and not a fixture.
 * @param {unknown} file - a repo-relative POSIX path.
 * @returns {boolean}
 */
export function isCheckedTestFile(file) {
  if (typeof file !== 'string' || file.length === 0) return false;
  if (DOCS.test(file) || underFixtures(file)) return false;
  if (isTestPath(file)) return true;
  const parts = segments(file);
  const name = parts[parts.length - 1];
  return parts.slice(0, -1).includes('Tests') || /(Tests|Test)\.[^./]+$/.test(name);
}

/** @param {string} line @returns {boolean} whether the line holds a quoted source-path literal (not a fixture). */
function hasSourceLiteral(line) {
  for (const m of line.matchAll(LITERAL)) {
    const value = m[2];
    if (SOURCE_END.test(value) && !underFixtures(value)) return true;
  }
  return false;
}

/** @param {string} line @returns {boolean} whether any string literal on the line has a fixtures dir segment. */
const hasFixtureLiteral = (line) => [...line.matchAll(LITERAL)].some((m) => underFixtures(m[2]));

/** @param {string} line @returns {number} `(` minus `)` outside string literals. */
const parenDelta = (line) => {
  const bare = line.replace(LITERAL, '');
  return (bare.match(/\(/g) ?? []).length - (bare.match(/\)/g) ?? []).length;
};

/**
 * The lines of the read call on `lines[i]`: that line, plus up to {@link SPAN_LINES} more while
 * the read call's own `(` stays open (the depth counts from that `(`, not the whole line).
 * @param {string[]} lines @param {number} i @returns {string[]}
 */
function callSpan(lines, i) {
  const line = lines[i];
  const span = [line];
  const at = Math.min(...READ_CALLS.map((c) => line.indexOf(c)).filter((n) => n >= 0));
  const open = line.indexOf('(', at);
  let depth = open === -1 ? 0 : parenDelta(line.slice(open));
  for (let j = i + 1; depth > 0 && j < lines.length && j <= i + SPAN_LINES; j += 1) {
    span.push(lines[j]);
    depth += parenDelta(lines[j]);
  }
  return span;
}

/** A call that passes a path straight on: `join(`, `path.join(`, `resolve(`, `path.resolve(`. */
const PATH_CALL = /^(?:path\.)?(?:join|resolve)\((.*)\)$/;

/**
 * Whether a binding's right-hand side IS a source-path literal: the literal alone, or the literal
 * passed straight to a path call. `spawnSync('node', ['bin/cli.mjs'])` is not.
 * @param {string} rhs @returns {boolean}
 */
function valueIsSourceLiteral(rhs) {
  const value = rhs.trim().replace(/;$/, '').trim();
  const whole = /^(['"`])([^'"`\n]*)\1$/.exec(value);
  if (whole) return hasSourceLiteral(value);
  const call = PATH_CALL.exec(value);
  return call !== null && hasSourceLiteral(call[1]);
}

/** @param {string} line @param {ReadonlyArray<string>} calls @returns {boolean} */
const holdsAny = (line, calls) => calls.some((c) => line.includes(c));

/**
 * The flagged line numbers of a test file that reads a source file and asserts on its text.
 * @param {unknown} file - repo-relative path. @param {unknown} content - the file's current text.
 * @returns {number[]} 1-based, sorted, no duplicates; [] when the file is not flagged.
 */
export function testQualityLines(file, content) {
  if (!isCheckedTestFile(file) || typeof content !== 'string') return [];
  const lines = content.split(/\r?\n/);
  /** @type {Set<string>} names bound to a source-path literal so far */
  const bound = new Set();
  /** @type {Set<string>} names bound to a literal with a fixtures segment so far */
  const fixtureBound = new Set();
  /** @type {number[]} */
  const reads = [];
  /** @type {number[]} */
  const asserts = [];
  /** @param {Set<string>} names @returns {(line: string) => boolean} whether a line names one of `names` */
  const names = (names) => (line) => [...names].some((name) => new RegExp(`(^|[^\\w$])${name.replace(/\$/g, '\\$')}([^\\w$]|$)`).test(line));
  const namesBound = names(bound);
  const namesFixture = names(fixtureBound);
  for (const [i, line] of lines.entries()) {
    if (holdsAny(line, READ_CALLS)) {
      const span = callSpan(lines, i);
      const fixture = span.some((l) => hasFixtureLiteral(l) || namesFixture(l));
      if (!fixture && span.some((l) => hasSourceLiteral(l) || namesBound(l))) reads.push(i + 1);
    }
    if (holdsAny(line, ASSERT_CALLS)) asserts.push(i + 1);
    const m = BINDING.exec(line);
    const name = m?.[1] ?? m?.[2];
    if (name) {
      if (hasFixtureLiteral(line)) fixtureBound.add(name);
      else if (valueIsSourceLiteral(line.slice(m[0].length))) bound.add(name);
    }
  }
  if (reads.length === 0 || asserts.length === 0) return [];
  return [...new Set([...reads, ...asserts])].sort((a, b) => a - b);
}

/**
 * The `## test-quality` section for `file`, or '' when no line is flagged. It names line numbers
 * only — never the file's text.
 * @param {string} file @param {ReadonlyArray<number>} lines - from {@link testQualityLines}.
 * @returns {string}
 */
export function renderTestQuality(file, lines) {
  if (lines.length === 0) return '';
  const shown = lines.slice(0, MAX_LINES);
  const more = lines.length - shown.length;
  return [
    '## test-quality',
    `file: ${file}`,
    `lines: ${shown.join(', ')}${more > 0 ? ` (${more} more not listed)` : ''}`,
    'This test file reads a source file and asserts on its text (a hint from a text match, not a finding): check these lines and apply the lens rule on tests that read source text.',
  ].join('\n');
}

/**
 * The section for one file at its current content ('' for none, a deleted file, or any error).
 * @param {string} file @param {string | null} content @returns {string}
 */
export function testQualityNote(file, content) {
  try {
    return content === null ? '' : renderTestQuality(file, testQualityLines(file, content));
  } catch {
    return ''; // a hint only: no note, the review goes on
  }
}
