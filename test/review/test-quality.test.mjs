/**
 * B92: the `## test-quality` note. A test file that reads a source file and asserts on its text
 * gets a section naming the line numbers (never the file's text) in every packet the fake
 * reviewer receives; the lenses carry one rule about it; with no flag the packet is today's.
 */
import { answerFor, cfgFor, harness, makeRepo, readRecords, stdinOf, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

const { reviewFile } = await import('../../src/review/engine.mjs');
const { readFileDiff } = await import('../../src/review/packet.mjs');
const { buildRecheckPacket } = await import('../../src/review/fixloop.mjs');
const { isCheckedTestFile, renderTestQuality, testQualityLines } = await import('../../src/review/test-quality.mjs');

/** The one rule every reviewer lens carries, worded the same. */
const RULE =
  '- A `## test-quality` section names lines of a test that reads a source file and asserts on its text. Check those lines: such a test is a `warning` with the claim "test reads source text; prove behaviour instead", unless the read file is a fixture. The section is a hint, not a finding.';

const HINT = 'This test file reads a source file and asserts on its text (a hint from a text match, not a finding): check these lines and apply the lens rule on tests that read source text.';

/** The section heading as a packet line (the lens rule names it in backticks, mid-line). */
const HEADING = '\n## test-quality\n';

/** A text-assert line on the read's result (round 2 detector tests). */
const AL = "assert.ok(text.includes('x'));";

/** @param {string} text @param {string} part @returns {number} */
const occurrences = (text, part) => text.split(part).length - 1;

/** @param {string[]} ls @returns {string} the lines, each ending in a newline. */
const src = (ls) => `${ls.join('\n')}\n`;

/**
 * Review `file` (holding `content`) at `risk` on the fakes; every reviewer answers a valid review.
 * @param {{file: string, content: string, risk?: number, cfg?: Record<string, any>}} opts
 */
async function review({ file, content, risk = 0.5, cfg = cfgFor() }) {
  const repo = await makeRepo();
  writeFile(repo, file, content);
  const n = content.split('\n').length - 1;
  const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answerFor([`@@ -0,0 +1,${n} @@`])) } });
  const outcome = await reviewFile({ repoRoot: repo, file, base: null, cfg, risk, workDir: h.workDir }, { spawn: h.spawn, writeRow: h.writeRow });
  const recs = readRecords(h.records);
  return { repo, outcome, recs, packets: recs.map(stdinOf), rows: h.rows };
}

/** The section of `packet` from `## test-quality` up to (not including) the newline before `## diff`. */
function sectionOf(/** @type {string} */ packet) {
  const start = packet.indexOf('\n## test-quality\n');
  const end = packet.indexOf('\n## diff\n');
  return start < 0 || end < start ? null : packet.slice(start + 1, end);
}

const MARK = 'QX7-MARKER';
const A_TEST = src([
  "import assert from 'node:assert/strict';",
  "import { readFileSync } from 'node:fs';",
  "import { test } from 'node:test';",
  '',
  "test('a holds x', () => {",
  `  const text = readFileSync('src/a.mjs', 'utf8'); // ${MARK}`,
  `  assert.ok(text.includes('x')); // ${MARK}`,
  '});',
]);
/** The same file, same byte length, with an assertion that is not a contains/match call. */
const A_TEST_PLAIN = A_TEST.replace(".includes('x')", ".endsWith('x')");

const SWIFT_TEST = src([
  'import XCTest',
  '',
  'final class FooTests: XCTestCase {',
  '    func testFooIsPublic() throws {',
  '        let text = try String(contentsOfFile: "Sources/Foo.swift")',
  '        XCTAssertTrue(text.contains("public func foo"))',
  '    }',
  '}',
]);

describe('B92 (1): a test that reads src/a.mjs and asserts includes(): the packet names the lines, never the text', () => {
  test('the quick packet the fake reviewer receives has exactly the section, before the diff, with lines 6 and 7', async () => {
    const { packets, outcome, rows } = await review({ file: 'test/a.test.mjs', content: A_TEST });
    assert.equal(packets.length, 1);
    const [packet] = packets;
    assert.equal(sectionOf(packet), ['## test-quality', 'file: test/a.test.mjs', 'lines: 6, 7', HINT].join('\n'));
    assert.equal(occurrences(packet, HEADING), 1);
    // no file text: the marker on both flagged lines is in the context and the diff only (2 + 2)
    assert.equal(occurrences(packet, MARK), 4);
    assert.equal(occurrences(/** @type {string} */ (sectionOf(packet)), MARK), 0);
    assert.equal(occurrences(packet, "readFileSync('src/a.mjs'"), 2);
    // a hint, never a finding or a row: the review stands on the reviewer's answer
    assert.deepEqual([outcome.status, outcome.approved], ['reviewed', true]);
    assert.deepEqual(rows.map((r) => r.event), ['review.plan']);
  });

  test('every packet of the file carries it: lens A, lens B and the judge (risk 2.5)', async () => {
    const { packets } = await review({ file: 'test/a.test.mjs', content: A_TEST, risk: 2.5 });
    assert.equal(packets.length, 3);
    const rows = packets.map((p) => [p.split('\n')[2], occurrences(p, HEADING), sectionOf(p)?.split('\n')[2] ?? null]);
    // lens A and lens B run in parallel, so their arrival order varies; the judge comes after both
    assert.deepEqual(
      [...rows.slice(0, 2).sort((x, y) => String(x[0]).localeCompare(String(y[0]))), rows[2]],
      [
        ['# Lens: A (correctness and edge cases)', 1, 'lines: 6, 7'],
        ['# Lens: B (contracts, project rules, security)', 1, 'lines: 6, 7'],
        ['# Lens: judge', 1, 'lines: 6, 7'],
      ],
    );
  });
});

describe('B92 (2): Swift', () => {
  test('Tests/FooTests.swift reading Sources/Foo.swift with contains( is flagged at lines 5 and 6', async () => {
    const { packets } = await review({ file: 'Tests/FooTests.swift', content: SWIFT_TEST });
    assert.equal(packets.length, 1);
    assert.equal(sectionOf(packets[0]), ['## test-quality', 'file: Tests/FooTests.swift', 'lines: 5, 6', HINT].join('\n'));
  });
});

describe('B92 (3): no section', () => {
  /** @type {Array<[string, string, string]>} name, file, content */
  const cases = [
    [
      'a test that reads a .md file',
      'test/doc.test.mjs',
      src(["import { readFileSync } from 'node:fs';", "const text = readFileSync('docs/guide.md', 'utf8');", "if (!text.includes('forge review')) throw new Error('missing');"]),
    ],
    [
      'a test that reads a .txt file',
      'test/notes.test.mjs',
      src(["import { readFileSync } from 'node:fs';", "const text = readFileSync('docs/notes.txt', 'utf8');", "if (!text.includes('forge')) throw new Error('missing');"]),
    ],
    [
      'a test that reads a source file but never asserts on its text',
      'test/size.test.mjs',
      src(["import assert from 'node:assert/strict';", "import { readFileSync } from 'node:fs';", "const text = readFileSync('src/a.mjs', 'utf8');", 'assert.equal(text.length > 0, true);']),
    ],
    [
      'a source file (not a test) that reads and matches',
      'src/build.mjs',
      src(["import { readFileSync } from 'node:fs';", "const text = readFileSync('src/a.mjs', 'utf8');", "export const hasX = text.includes('x');"]),
    ],
    [
      'a test that reads a fixture (a path under a fixtures dir)',
      'test/b.test.mjs',
      src(["import { readFileSync } from 'node:fs';", "const text = readFileSync('test/fixtures/sample.mjs', 'utf8');", "if (!text.includes('x')) throw new Error('missing');"]),
    ],
  ];
  for (const [name, file, content] of cases) {
    test(name, async () => {
      const { packets } = await review({ file, content });
      assert.equal(packets.length, 1);
      assert.equal(occurrences(packets[0], HEADING), 0);
      assert.equal(sectionOf(packets[0]), null);
    });
  }
});

describe('B92 (4): the budget and the no-flag packet', () => {
  test('the section counts in the packet budget: at a budget the plain file fits, the flagged one is split_required, no session', async () => {
    const at = (/** @type {number} */ quick_in) => cfgFor({ budgets: { quick_in } });
    const flagged1 = await review({ file: 'test/a.test.mjs', content: A_TEST, cfg: at(1) });
    const plain1 = await review({ file: 'test/a.test.mjs', content: A_TEST_PLAIN, cfg: at(1) });
    assert.deepEqual([flagged1.outcome.status, plain1.outcome.status, flagged1.recs.length, plain1.recs.length], ['split_required', 'split_required', 0, 0]);
    const tFlag = flagged1.outcome.tokens_in;
    const tPlain = plain1.outcome.tokens_in;
    // the two files have the same byte length: the gap is the section plus its joining newline,
    // in tokens (bytes / 4, rounded up on each side), so it is floor or ceil of that size / 4
    assert.equal(A_TEST.length, A_TEST_PLAIN.length);
    const sectionBytes = Buffer.byteLength(['## test-quality', 'file: test/a.test.mjs', 'lines: 6, 7', HINT].join('\n')) + 1;
    assert.equal(sectionBytes, 226);
    assert.equal([56, 57].includes(tFlag - tPlain), true, `gap ${tFlag - tPlain}`);
    const plain = await review({ file: 'test/a.test.mjs', content: A_TEST_PLAIN, cfg: at(tPlain) });
    assert.deepEqual([plain.outcome.status, plain.recs.length], ['reviewed', 1]);
    assert.equal(occurrences(plain.packets[0], HEADING), 0);
    // the same path any extra takes today: the diff-only size (lens + extras + diff) is over ⇒ split_required, never a cut
    const flagged = await review({ file: 'test/a.test.mjs', content: A_TEST, cfg: at(tPlain) });
    assert.deepEqual(
      { status: flagged.outcome.status, tokens_in: flagged.outcome.tokens_in, budget: flagged.outcome.budget, sessions: flagged.outcome.sessions, spawned: flagged.recs.length },
      { status: 'split_required', tokens_in: tFlag, budget: tPlain, sessions: [], spawned: 0 },
    );
  });

  test('with no flag the packet is byte-equal to the base (d60059f) packet, plus only the lens rule line', async () => {
    const { packets } = await review({
      file: 'test/doc.test.mjs',
      content: src([
        "import assert from 'node:assert/strict';",
        "import { readFileSync } from 'node:fs';",
        "import { test } from 'node:test';",
        '',
        "test('docs name the verb', () => {",
        "  const text = readFileSync('docs/guide.md', 'utf8');",
        "  assert.ok(text.includes('forge review'));",
        '});',
      ]),
    });
    assert.equal(packets.length, 1);
    // TODAY: the quick packet captured from the base code (d60059f) for this file, byte for byte
    const TODAY = [
      '# code-forge review packet',
      '## lens',
      '# Lens: quick',
      '',
      'You are a code reviewer doing a QUICK pass on one small, low-risk change.',
      '',
      '## What to look for',
      '',
      '- Obvious bugs in the changed lines: wrong condition, wrong variable, off-by-one, a missing `await`, an unhandled error path.',
      '- A change that plainly does not do what its own code and names say.',
      '- Anything that leaks a secret or writes outside the paths the change is about.',
      '',
      'Skip style, naming and structure unless it hides a bug. Keep `findings` short.',
      '',
      '## Rules for every answer',
      '',
      '- Judge only what is in the packet; if you need a file you cannot see, return it in `needs_file` instead of guessing.',
      '- You have no tools and no project files. The packet is everything you get.',
      '- `reviewed_hunks`: copy every `@@ … @@` hunk header listed under "hunks" in the packet, exactly and in the same order. A review whose `reviewed_hunks` does not match is discarded.',
      '- Answer with ONE JSON object that matches the answer schema: `passed`, `summary` (≤ 300 chars), `reviewed_hunks`, `findings[]`, and `needs_file[]` (empty when you need nothing).',
      '- Every finding names `file`, `line_start`, `line_end` (line numbers of the CURRENT file, as numbered in the context), `severity` (`critical` = wrong behaviour, data loss, security; `warning` = a real defect that is not critical; `nit` = style or preference), a short `category`, the `claim`, the `evidence` you read in the packet, and the `fix`. Each of `claim`, `evidence`, `fix` ≤ 300 chars.',
      '- `passed` is true only when you found no `critical` and no `warning`.',
      '- Do not invent a problem to look thorough. An empty `findings` list is a valid answer.',
      '## project rules',
      '(none)',
      '## facts',
      '(none)',
      '## context',
      '### test/doc.test.mjs (whole file, 8 lines)',
      "1| import assert from 'node:assert/strict';",
      "2| import { readFileSync } from 'node:fs';",
      "3| import { test } from 'node:test';",
      '4| ',
      "5| test('docs name the verb', () => {",
      "6|   const text = readFileSync('docs/guide.md', 'utf8');",
      "7|   assert.ok(text.includes('forge review'));",
      '8| });',
      '## diff',
      'file: test/doc.test.mjs',
      'hunks:',
      '- @@ -0,0 +1,8 @@',
      '',
      'diff --git a/test/doc.test.mjs b/test/doc.test.mjs',
      'new file mode 100644',
      'index 0000000..b39a77f',
      '--- /dev/null',
      '+++ b/test/doc.test.mjs',
      '@@ -0,0 +1,8 @@',
      "+import assert from 'node:assert/strict';",
      "+import { readFileSync } from 'node:fs';",
      "+import { test } from 'node:test';",
      '+',
      "+test('docs name the verb', () => {",
      "+  const text = readFileSync('docs/guide.md', 'utf8');",
      "+  assert.ok(text.includes('forge review'));",
      '+});',
      '',
    ];
    // B92 adds one line to every reviewer lens (the RULE, last of "Rules for every answer"); nothing else
    const lastRule = TODAY.indexOf('- Do not invent a problem to look thorough. An empty `findings` list is a valid answer.');
    assert.equal(lastRule, 22);
    const expected = [...TODAY.slice(0, lastRule + 1), RULE, ...TODAY.slice(lastRule + 1)].join('\n');
    assert.equal(packets[0], expected);
  });
});

describe('B92: the lens rule in the built prompt', () => {
  /** @param {string} packet @returns {string} the packet's `## lens` section. */
  const lensOf = (packet) => {
    const start = packet.indexOf('## lens\n');
    const end = packet.indexOf('\n## project rules\n');
    assert.notEqual(start, -1, 'the packet has a ## lens heading');
    assert.notEqual(end, -1, 'the packet has a ## project rules heading');
    return packet.slice(start, end);
  };

  test('quick, full, A and B reviewer packets carry the rule once; the judge packet does not', async () => {
    const quick = await review({ file: 'src/a.mjs', content: src(['export const a = 1;', 'export const b = 2;']), risk: 0.5 });
    const full = await review({ file: 'src/a.mjs', content: src(['export const a = 1;', 'export const b = 2;']), risk: 1.5 });
    const dual = await review({ file: 'src/a.mjs', content: src(['export const a = 1;', 'export const b = 2;']), risk: 2.5 });
    // lens A and lens B run in parallel and arrive in either order: sort them by their lens line
    const ab = dual.packets.slice(0, 2).sort((x, y) => x.split('\n')[2].localeCompare(y.split('\n')[2]));
    const all = [...quick.packets, ...full.packets, ...ab];
    assert.deepEqual(
      all.map((p) => [p.split('\n')[2], occurrences(lensOf(p), RULE)]),
      [
        ['# Lens: quick', 1],
        ['# Lens: full', 1],
        ['# Lens: A (correctness and edge cases)', 1],
        ['# Lens: B (contracts, project rules, security)', 1],
      ],
    );
    assert.equal(dual.packets[2].split('\n')[2], '# Lens: judge');
    assert.equal(occurrences(dual.packets[2], RULE), 0);
  });

  test('the recheck packet of a FLAGGED test file has no section, and its lens carries the rule once', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'test/a.test.mjs', A_TEST);
    assert.deepEqual(testQualityLines('test/a.test.mjs', A_TEST), [6, 7]); // the file is flagged
    const diff = await readFileDiff({ repoRoot: repo, file: 'test/a.test.mjs', base: null });
    const packet = /** @type {{status: string, text: string}} */ (buildRecheckPacket({ diff, open: [], cfg: cfgFor() }));
    assert.equal(packet.status, 'ok');
    assert.deepEqual([packet.text.split('\n')[2], occurrences(packet.text, HEADING), occurrences(lensOf(packet.text), RULE)], ['# Lens: recheck', 0, 1]);
  });
});

describe('B92: the detector (pure)', () => {
  test('which paths are checked test files', () => {
    /** @type {Array<[string, boolean]>} */
    const cases = [
      ['test/a.test.mjs', true],
      ['src/a.test.ts', true],
      ['tests/helpers.py', true],
      ['spec/a_spec.rb', true],
      ['pkg/a_test.go', true],
      ['test_a.py', true],
      ['app/UserTest.php', true],
      ['Tests/Helpers.swift', true],
      ['App/FooTests.swift', true],
      ['app/src/FooTest.kt', true],
      ['src/a.mjs', false],
      ['Sources/Foo.swift', false],
      ['src/Testing.swift', false],
      ['test/README.md', false],
      ['tests/notes.txt', false],
      ['test/fixtures/sample.test.mjs', false],
      ['Tests/__fixtures__/FooTests.swift', false],
    ];
    assert.deepEqual(
      cases.map(([p]) => [p, isCheckedTestFile(p)]),
      cases,
    );
  });

  test('each read call, with a source-path literal and an includes( assert, flags lines [2, 3]', () => {
    /** @type {Array<[string, string]>} call, read line */
    const reads = [
      ['readFileSync', "const t = readFileSync('src/a.mjs', 'utf8');"],
      ['readFile', "const t = await readFile('src/a.ts', 'utf8');"],
      ['open(', "t = open('pkg/a.py').read()"],
      ['read_text', "t = Path('pkg/a.py').read_text()"],
      ['String(contentsOf', 'let t = try String(contentsOf: URL(fileURLWithPath: "Sources/Foo.swift"))'],
      ['contentsOfFile', 'let t = loader.contentsOfFile("Sources/Foo.swift")'],
      ['File.read', "t = File.read('lib/a.rb')"],
      ['os.ReadFile', 't, _ := os.ReadFile("cmd/a.go")'],
      ['fs::read_to_string', 'let t = fs::read_to_string("src/lib.rs").unwrap();'],
      ['readText(', 'val t = File("src/main/kotlin/Foo.kt").readText()'],
      ['readString(', 'String t = Files.readString(Path.of("src/main/java/Foo.java"));'],
      ['ReadAllText(', 'var t = File.ReadAllText("src/Foo.cs");'],
    ];
    const got = reads.map(([call, line]) => [call, testQualityLines('test/a.test.mjs', src(['// header', line, "check(t.includes('x'));"]))]);
    assert.deepEqual(got, reads.map(([call]) => [call, [2, 3]]));
  });

  test('each text-assert call, after a readFileSync of a source file, flags lines [1, 2]', () => {
    /** @type {Array<[string, string]>} call, assert line */
    const asserts = [
      ['includes(', "assert.ok(t.includes('x'));"],
      ['contains(', 'XCTAssert(t.contains("x"))'],
      ['toContain(', "expect(t).toContain('x');"],
      ['toMatch(', 'expect(t).toMatch(/x/);'],
      ['assert.match(', 'assert.match(t, /x/);'],
      ['assertIn(', "self.assertIn('x', t)"],
      ['XCTAssertTrue(…contains', 'XCTAssertTrue(t.contains("x"))'],
      ['strings.Contains(', 'if !strings.Contains(t, "x") { panic("x") }'],
    ];
    const got = asserts.map(([call, line]) => [call, testQualityLines('test/a.test.mjs', src(["const t = readFileSync('src/a.mjs', 'utf8');", line]))]);
    assert.deepEqual(got, asserts.map(([call]) => [call, [1, 2]]));
  });

  test('every source extension is a source read; .md, .txt, .json and a fixture path are not', () => {
    const exts = ['swift', 'ts', 'tsx', 'js', 'mjs', 'cjs', 'py', 'go', 'rs', 'kt', 'java', 'rb', 'php', 'cs', 'md', 'txt', 'json'];
    const got = exts.map((ext) => [ext, testQualityLines('test/a.test.mjs', src(['// header', `const t = readFileSync('src/a.${ext}', 'utf8');`, "assert.ok(t.includes('x'));"]))]);
    const source = new Set(['swift', 'ts', 'tsx', 'js', 'mjs', 'cjs', 'py', 'go', 'rs', 'kt', 'java', 'rb', 'php', 'cs']);
    assert.deepEqual(got, exts.map((ext) => [ext, source.has(ext) ? [2, 3] : []]));
    const dirs = ['fixtures', 'fixture', '__fixtures__'];
    assert.deepEqual(
      dirs.map((dir) => [dir, testQualityLines('test/a.test.mjs', src([`const t = readFileSync('test/${dir}/a.mjs', 'utf8');`, "assert.ok(t.includes('x'));"]))]),
      dirs.map((dir) => [dir, []]),
    );
  });

  test('a name bound to a source path on an earlier line is followed into the read call; one bound to a fixture is not', () => {
    const body = (/** @type {string} */ target) =>
      src([`const SRC = path.join(ROOT, '${target}');`, '', 'test(() => {', "  const text = readFileSync(SRC, 'utf8');", "  assert.ok(text.includes('x'));", '});']);
    assert.deepEqual(testQualityLines('test/a.test.mjs', body('src/a.mjs')), [4, 5]);
    assert.deepEqual(testQualityLines('test/a.test.mjs', body('test/fixtures/a.mjs')), []);
    // a read of another name (not bound to a source path) is no source read
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(["const SRC = 'src/a.mjs';", "const text = readFileSync(SRCX, 'utf8');", "assert.ok(text.includes('x'));"])), []);
  });

  test('round 1 (3): a read whose call holds a fixtures segment in any string literal is a fixture read', () => {
    const assertLine = "assert.ok(text.includes('x'));";
    /** @type {Array<[string, string[]]>} name, file lines */
    const cases = [
      ['join with a fixtures segment', ["const text = readFileSync(join(__dirname, 'fixtures', 'a.mjs'), 'utf8');", assertLine]],
      ['path.join with fixture', ["const text = readFileSync(path.join(ROOT, 'test', 'fixture', 'b.ts'), 'utf8');", assertLine]],
      ['split call, fixtures on a later line', ['const text = readFileSync(', "  join(__dirname, '__fixtures__', 'a.mjs'),", "  'utf8',", ');', assertLine]],
      ['a name bound with a fixtures segment', ["const P = join(__dirname, 'fixtures', 'a.mjs');", "const text = readFileSync(P, 'utf8');", assertLine]],
    ];
    assert.deepEqual(cases.map(([name, lines]) => [name, testQualityLines('test/a.test.mjs', src(lines))]), cases.map(([name]) => [name, []]));
    // the same join with no fixtures segment is a source read: the call line and the assert
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(["const text = readFileSync(join(__dirname, 'src', 'a.mjs'), 'utf8');", assertLine])), [1, 2]);
  });

  test('round 1 (4): a name bound with modifiers and a type is followed (Kotlin, Swift, Java, TS export)', () => {
    /** @type {Array<[string, string, string[]]>} name, file, lines — the read is on line 4, the assert on line 5 */
    const cases = [
      ['Kotlin private val', 'src/test/kotlin/FooTest.kt', ['class FooTest {', '  private val SRC = "src/main/kotlin/Foo.kt"', '  @Test fun names() {', '    val text = File(SRC).readText()', '    assertTrue(text.contains("x"))', '  }', '}']],
      ['Kotlin private val with a type', 'src/test/kotlin/FooTest.kt', ['class FooTest {', '  private val SRC: String = "src/main/kotlin/Foo.kt"', '  @Test fun names() {', '    val text = File(SRC).readText()', '    assertTrue(text.contains("x"))', '  }', '}']],
      ['Swift private static let', 'Tests/FooTests.swift', ['final class FooTests: XCTestCase {', '  private static let SRC = "Sources/Foo.swift"', '  func testNames() throws {', '    let text = try String(contentsOfFile: Self.SRC)', '    XCTAssertTrue(text.contains("x"))', '  }', '}']],
      ['Java private static final String', 'src/test/java/FooTest.java', ['class FooTest {', '  private static final String SRC = "src/main/java/Foo.java";', '  @Test void names() throws Exception {', '    String text = Files.readString(Path.of(SRC));', '    assertTrue(text.contains("x"));', '  }', '}']],
      ['TS export const', 'test/a.test.ts', ['// header', "export const SRC = 'src/a.ts';", '', "const text = readFileSync(SRC, 'utf8');", "assert.ok(text.includes('x'));"]],
      ['TS export let', 'test/a.test.ts', ['// header', "export let SRC = 'src/a.ts';", '', "const text = readFileSync(SRC, 'utf8');", "assert.ok(text.includes('x'));"]],
      ['Kotlin const val', 'src/test/kotlin/FooTest.kt', ['// header', 'const val SRC = "src/main/kotlin/Foo.kt"', 'class FooTest {', '  @Test fun names() { val text = File(SRC).readText()', '    assertTrue(text.contains("x")) }', '}']],
      ['Kotlin private const val', 'src/test/kotlin/FooTest.kt', ['// header', 'private const val SRC = "src/main/kotlin/Foo.kt"', 'class FooTest {', '  @Test fun names() { val text = File(SRC).readText()', '    assertTrue(text.contains("x")) }', '}']],
      ['C# private const string', 'Tests/FooTests.cs', ['public class FooTests {', '  private const string SRC = "src/Foo.cs";', '  [Fact] public void Names() {', '    var text = File.ReadAllText(SRC);', '    Assert.True(text.contains("x"));', '  }', '}']],
      ['C# const string', 'Tests/FooTests.cs', ['public class FooTests {', '  const string SRC = "src/Foo.cs";', '  [Fact] public void Names() {', '    var text = File.ReadAllText(SRC);', '    Assert.True(text.contains("x"));', '  }', '}']],
    ];
    assert.deepEqual(cases.map(([name, file, lines]) => [name, testQualityLines(file, src(lines))]), cases.map(([name]) => [name, [4, 5]]));
  });

  test('round 1 (5): a read call whose path literal is on the next 1-3 lines is detected; the call line is the one reported', () => {
    const assertLine = "assert.ok(text.includes('x'));";
    // call on line 1, the literal on line 2, the assert on line 5 ⇒ [1, 5] (line 2 is not listed)
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(['const text = readFileSync(', "  'src/a.mjs',", "  'utf8',", ');', assertLine])), [1, 5]);
    // the literal 3 lines after the call (line 4) ⇒ [1, 7]
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(['const text = readFileSync(', '  join(', '    ROOT,', "    'src/a.mjs',", '  ),', ');', assertLine])), [1, 7]);
    // a bound name 2 lines after the call ⇒ [2, 6]
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(["const SRC = 'src/a.mjs';", 'const text = readFileSync(', '  SRC,', "  'utf8',", ');', assertLine])), [2, 6]);
    // the literal 4 lines after the call is outside the span ⇒ no section
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(['const text = readFileSync(', '  join(', '    ROOT,', '    DIR,', "    'src/a.mjs',", '  ),', ');', assertLine])), []);
    // a call closed on its own line takes no later line: a source literal on the next line is no read
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(["const text = readFileSync(other, 'utf8');", "const name = 'src/a.mjs';", assertLine])), []);
  });

  test("round 2 (1): the span counts depth from the read call's own `(`, not the whole line", () => {
    // `it(` is still open at the line end, but the read call closes on its line ⇒ line 2 is not in its span
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(["it('x', async () => { const text = await readFile(p);", "  const name = 'src/a.mjs';", `  ${AL}`, '});'])), []);
    // the same opening with the read call left open ⇒ its span reaches line 2 ⇒ [1, 3]
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(["it('x', async () => { const text = await readFile(", "  'src/a.mjs');", `  ${AL}`, '});'])), [1, 3]);
  });

  test('round 2 (2): a name is bound only when its value IS the source literal (whole value, or passed straight to join/resolve)', () => {
    /** @type {Array<[string, string, number[]]>} name, binding line on line 1, lines (read on line 2, assert on line 3) */
    const cases = [
      ['spawnSync args bind nothing', "const SRC = spawnSync('node', ['bin/cli.mjs']);", []],
      ['a literal inside another call binds nothing', "const SRC = wrap('src/a.mjs');", []],
      ['a literal and more binds nothing', "const SRC = 'src/a.mjs' + suffix;", []],
      ['the literal alone binds', "const SRC = 'src/a.mjs';", [2, 3]],
      ['the literal passed to join binds', "const SRC = join(ROOT, 'src/a.mjs');", [2, 3]],
      ['the literal passed to path.resolve binds', "const SRC = path.resolve(ROOT, 'src', 'a.mjs');", [2, 3]],
    ];
    const got = cases.map(([name, binding]) => [name, testQualityLines('test/a.test.mjs', src([binding, "const text = readFileSync(SRC, 'utf8');", AL]))]);
    assert.deepEqual(got, cases.map(([name, , lines]) => [name, lines]));
  });

  test('round 2 (4): a name bound to a literal with a fixtures segment makes the reads that use it fixture reads', () => {
    /** @type {Array<[string, string[], number[]]>} name, file lines, flagged lines */
    const cases = [
      ['join(FIXTURES, a source name)', ["const FIXTURES = join(__dirname, 'fixtures');", "const text = readFileSync(join(FIXTURES, 'a.mjs'), 'utf8');", AL], []],
      ['a fixture dir literal', ["const DIR = 'test/__fixtures__';", "const text = readFileSync(join(DIR, 'b.ts'));", AL], []],
      ['split call naming FIXTURES on a later line', ["const FIXTURES = path.join(ROOT, 'test', 'fixture');", 'const text = readFileSync(', "  join(FIXTURES, 'a.mjs'),", ');', AL], []],
      ['a read not naming it is still a source read', ["const FIXTURES = join(__dirname, 'fixtures');", "const text = readFileSync(join(ROOT, 'a.mjs'), 'utf8');", AL], [2, 3]],
    ];
    assert.deepEqual(cases.map(([name, lines]) => [name, testQualityLines('test/a.test.mjs', src(lines))]), cases.map(([name, , want]) => [name, want]));
  });

  test('a line that both reads and asserts is listed once; lines are sorted', () => {
    assert.deepEqual(testQualityLines('test/a.test.mjs', src(["assert.ok(t.includes('y'));", "assert.ok(readFileSync('src/a.mjs', 'utf8').includes('x'));"])), [1, 2]);
  });

  test('the section lists at most 10 line numbers and counts the rest; no line ⇒ no section', () => {
    const many = src(["const t = readFileSync('src/a.mjs', 'utf8');", ...Array.from({ length: 12 }, (_, i) => `assert.ok(t.includes('x${i}'));`)]);
    const lines = testQualityLines('test/a.test.mjs', many);
    assert.deepEqual(lines, [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13]);
    assert.equal(renderTestQuality('test/a.test.mjs', lines), ['## test-quality', 'file: test/a.test.mjs', 'lines: 1, 2, 3, 4, 5, 6, 7, 8, 9, 10 (3 more not listed)', HINT].join('\n'));
    assert.equal(renderTestQuality('test/a.test.mjs', []), '');
  });
});
