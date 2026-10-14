// B63a: the coder context pack. `code-forge context pack --run <id> --block <id>` runs as a real
// CLI subprocess in temp repositories (a run started and a block opened in-process); a few cases
// call the verb in-process to make a write or the ledger fail. The review packet tests start a real
// run (its worker) with the scripted fake `claude` on PATH and read the packets the reviewer was
// sent. Helpers FIRST (temp HOME/TMPDIR).
import { cli, freshDir, PARENT, stopPid, stopPidAfter, useFake } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, existsSync, mkdirSync, readdirSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, test } from 'node:test';

const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const { liveWorker } = await import('../../src/worker/queue.mjs');
const { startRun, readRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { runRecordPath } = await import('../../src/state/paths.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { estimateTokens } = await import('../../src/review/packet.mjs');
const { argvText, reviewDigest } = await import('../../src/context/pack.mjs');
const { registerSecret } = await import('../../src/util/redact.mjs');
const { runContext } = await import('../../src/cli/context.mjs');

useFake(fileURLToPath(new URL('../worker/fake-scripted-reviewer.mjs', import.meta.url)));

const BASE_CONFIG = (/** @type {string} */ slug) => `version: 1
project:
  slug: ${slug}
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-1
review:
  session_timeout_s: 60
  min_tokens_out: 40
`;

let seq = 0;

/** @param {string} cwd @param {string[]} args */
async function git(cwd, args) {
  const res = await exec(['git', '-c', 'user.name=Fake Tester', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', ...args], { cwd, env: gitChildEnv(), timeoutMs: 20000 });
  assert.equal(res.result, 'ok', res.stderr);
}

/** @param {string} p @param {number} n @returns {string[]} `n` numbered lines. */
const numbered = (p, n) => Array.from({ length: n }, (_, i) => `${p} line ${i + 1}`);

/** @param {string} p @param {number} n @returns {string} the file text of `numbered(p, n)`. */
const fileOf = (p, n) => `${numbered(p, n).join('\n')}\n`;

/** @param {string} text @returns {string} the sha256 of the pack text (what the verb reports). */
const sha256Of = (text) => createHash('sha256').update(text).digest('hex');

/**
 * A repo with `src/a.mjs` committed, `.code-forge.yml` = the base config plus `context`, the
 * `files` written, a started run and block B1 open (level L2, owned src/a.mjs, `tests` when given).
 * `snapshot` is the config `run start` stores for the run (none by default).
 * @param {{context?: string, files?: Record<string, string>, tests?: string[], snapshot?: Record<string, any>}} opts
 */
async function packRepo({ context = '', files = {}, tests, snapshot } = {}) {
  const repo = freshDir('ctx-repo');
  await git(repo, ['init', '-q']);
  mkdirSync(path.join(repo, 'src'));
  writeFileSync(path.join(repo, 'src', 'a.mjs'), 'export const a = 1;\n');
  await git(repo, ['add', '-A']);
  await git(repo, ['commit', '-q', '-m', 'base']);
  seq += 1;
  const slug = `ctx-test-${seq}`;
  writeFileSync(path.join(repo, '.code-forge.yml'), BASE_CONFIG(slug) + context);
  for (const [rel, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(repo, rel)), { recursive: true });
    writeFileSync(path.join(repo, rel), text);
  }
  const runId = `r-ctx-${process.pid}-${seq}`;
  const writeRow = async () => {};
  await startRun({ workspace: repo, project: slug, runId, writeRow, ...(snapshot ? { config: snapshot } : {}) });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'the pack is built', tests: ['context'] }], ...(tests ? { tests } : {}), writeRow });
  const packFile = path.join(PARENT, 'code-forge', runId, 'context', 'B1.md');
  return { repo, runId, slug, packFile };
}

/** @param {string} slug @param {string} runId @returns {Promise<Array<Record<string, any>>>} the run's context.pack rows. */
async function packRows(slug, runId) {
  return (await readAllRows(slug)).filter((r) => r.event === 'context.pack' && r.run === runId);
}

/** @param {Record<string, any>} row */
const fieldsOf = (row) => ({ event: row.event, run: row.run, block: row.block, path: row.path, sha256: row.sha256, tokens: row.tokens, lines: row.lines, cut: row.cut });

/** @returns {{write: (s: string) => boolean, text: string}} */
function capture() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

/**
 * `runContext` in-process with TMPDIR = PARENT, as the CLI subprocesses have it (the run root).
 * @param {string[]} args @param {Record<string, any>} deps @returns {Promise<number>}
 */
async function packInProcess(args, deps) {
  const saved = process.env.TMPDIR;
  process.env.TMPDIR = PARENT;
  try {
    return await runContext(args, deps);
  } finally {
    if (saved === undefined) delete process.env.TMPDIR;
    else process.env.TMPDIR = saved;
  }
}

/** The header lines of a B1 pack with no test files, no worktree and no commands. @param {string} runId */
const PROOF_LINES = [
  "## proof rule (code-forge)",
  "- Red→green: each new or changed test must FAIL on the old code first (red), then PASS on the new code (green). A red caused by an import, compile or crash error proves nothing; the red must be an assertion failure. `code-forge proof red-green` does this with the block's base code.",
  "- No hand mutations. Do not break the code on purpose to test the tests. Red→green is the proof.",
  "- One exact test per acceptance clause, plus one negative or boundary test where the clause can fail. Assertions say which element and how many, never only \"it exists\".",
  "- Proof scales with risk: a light-tier file needs red→green per new test; a high-tier file must have it at the gate. Do not add tests, mutants, layers, options or abstractions that the acceptance does not ask for.",
  "- A weak assertion is fixed by a stronger assertion, not by more tests or mutants."
];

const plainHeader = (runId) => ['# code-forge context pack', '## block', `run: ${runId}`, 'block: B1', 'level: L2', 'owned files:', '- src/a.mjs', '## commands (never run by code-forge)', '(none)', ...PROOF_LINES];

const FULL_CONTEXT = `context:
  rules: [RULES.md, docs/style.md]
  lessons: docs/lessons.md
  commands:
    build: [npm, run, build]
    test: [node, --test, "test dir/x.mjs"]
    lint: [sh, -c, "echo $HOME it's"]
    format: [prettier, --write, "src/*.mjs"]
  max_lines: 40
`;
const FULL_FILES = { 'RULES.md': 'Rule one.\nRule two.\n', 'docs/style.md': 'Style A.\n\n', 'docs/lessons.md': 'Lesson X.\n' };
// the commands as a POSIX shell takes them back: nothing in them expands when pasted
const FULL_COMMANDS = ['## commands (never run by code-forge)', 'build: npm run build', "test: node --test 'test dir/x.mjs'", "lint: sh -c 'echo $HOME it'\\''s'", "format: prettier --write 'src/*.mjs'"];

describe('code-forge context pack: the pack file, stdout and the ledger row', () => {
  test('the exact pack (header with test files and worktree, commands shell-quoted, rules then lessons), two stdout lines, one signed row', async () => {
    const { repo, runId, slug, packFile } = await packRepo({ context: FULL_CONTEXT, files: FULL_FILES, tests: ['test/a.test.mjs'] });
    // a worktree block (B62a): the header names its path and branch
    const recordFile = runRecordPath(runId);
    const record = JSON.parse(readFileSync(recordFile, 'utf8'));
    record.blocks.B1.worktree = { path: '/fake/trees/B1', branch: 'code-forge/B1', base_sha: record.blocks.B1.base_sha };
    writeFileSync(recordFile, JSON.stringify(record));

    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    assert.equal(res.code, 0, res.stderr);
    const expected = [
      '# code-forge context pack',
      '## block',
      `run: ${runId}`,
      'block: B1',
      'level: L2',
      'owned files:',
      '- src/a.mjs',
      'test files:',
      '- test/a.test.mjs',
      'worktree: /fake/trees/B1',
      'branch: code-forge/B1',
      ...FULL_COMMANDS,
      ...PROOF_LINES,
      '## rules: RULES.md',
      'Rule one.',
      'Rule two.',
      '## rules: docs/style.md',
      'Style A.',
      '## lessons: docs/lessons.md',
      'Lesson X.',
      '',
    ].join('\n');
    assert.equal(readFileSync(packFile, 'utf8'), expected);
    const tokens = estimateTokens(expected);
    assert.equal(res.stdout, `${packFile}\ntokens ~${tokens}\n`);
    const rows = await packRows(slug, runId);
    assert.deepEqual(rows.map(fieldsOf), [{ event: 'context.pack', run: runId, block: 'B1', path: packFile, sha256: sha256Of(expected), tokens, lines: 29, cut: 0 }]);
    assert.equal(verifyRow(rows[0], await loadKey(runId)).ok, true);
  });

  test('the same pack again writes no row and prints the same; --json prints one line; changed rules write a new row and the exact new pack', async () => {
    const { repo, runId, slug, packFile } = await packRepo({ context: FULL_CONTEXT, files: FULL_FILES });
    const first = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    const second = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    assert.deepEqual([first.code, second.code], [0, 0]);
    assert.equal(second.stdout, first.stdout);
    assert.equal((await packRows(slug, runId)).length, 1);

    const text = readFileSync(packFile, 'utf8');
    const json = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
    assert.equal(json.code, 0, json.stderr);
    assert.equal(json.stdout, `${JSON.stringify({ path: packFile, tokens: estimateTokens(text), lines: 25, cut: 0, sha256: sha256Of(text) })}\n`);
    assert.equal((await packRows(slug, runId)).length, 1);

    writeFileSync(path.join(repo, 'RULES.md'), 'Rule one, amended.\n');
    const changed = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
    assert.equal(changed.code, 0, changed.stderr);
    const newText = [...plainHeader(runId).slice(0, -8), ...FULL_COMMANDS, ...PROOF_LINES, '## rules: RULES.md', 'Rule one, amended.', '## rules: docs/style.md', 'Style A.', '## lessons: docs/lessons.md', 'Lesson X.', ''].join('\n');
    assert.equal(readFileSync(packFile, 'utf8'), newText);
    const rows = await packRows(slug, runId);
    assert.deepEqual(rows.map((r) => r.sha256), [sha256Of(text), sha256Of(newText)]);
    assert.deepEqual(changed.json, { path: packFile, tokens: estimateTokens(newText), lines: 24, cut: 0, sha256: sha256Of(newText) });
  });

  test('over max_lines the lessons are cut first, then the rules from the last file back, each with a [cut: N lines dropped] marker', async () => {
    const files = { 'r1.md': fileOf('r1', 5), 'r2.md': fileOf('r2', 5), 'l.md': fileOf('l', 10) };
    const ctx = (/** @type {number} */ max) => `context:\n  rules: [r1.md, r2.md]\n  lessons: l.md\n  max_lines: ${max}\n`;
    const { repo, runId, slug, packFile } = await packRepo({ context: ctx(36), files });
    const header = plainHeader(runId);
    const r1 = ['## rules: r1.md', ...numbered('r1', 5)];

    // 38 lines uncut, cap 36: three lessons lines go (two over, plus the marker's own line)
    const partial = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
    assert.equal(partial.code, 0, partial.stderr);
    const partialText = [...header, ...r1, '## rules: r2.md', ...numbered('r2', 5), '## lessons: l.md', ...numbered('l', 7), '[cut: 3 lines dropped]', ''].join('\n');
    assert.equal(readFileSync(packFile, 'utf8'), partialText);
    assert.deepEqual([partial.json.lines, partial.json.cut], [36, 3]);

    // cap 26: every lessons line goes, then four of r2's; header, commands and proof stay whole
    writeFileSync(path.join(repo, '.code-forge.yml'), BASE_CONFIG(slug) + ctx(26));
    const deep = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
    assert.equal(deep.code, 0, deep.stderr);
    const deepText = [...header, ...r1, '## rules: r2.md', 'r2 line 1', '[cut: 4 lines dropped]', '## lessons: l.md', '[cut: 10 lines dropped]', ''].join('\n');
    assert.equal(readFileSync(packFile, 'utf8'), deepText);
    assert.deepEqual([deep.json.lines, deep.json.cut], [26, 14]);
    assert.deepEqual((await packRows(slug, runId)).map((r) => [r.lines, r.cut]), [[36, 3], [26, 14]]);
  });

  test('a later rules file is cut whole before an earlier one is cut in part', async () => {
    // 15 protected lines + r1 (1 + 10) + r2 (1 + 5) = 32; cap 20: r2 loses 5, r1 nine
    const files = { 'r1.md': fileOf('r1', 10), 'r2.md': fileOf('r2', 5) };
    const { repo, runId, packFile } = await packRepo({ context: 'context:\n  rules: [r1.md, r2.md]\n  max_lines: 20\n', files });
    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
    assert.equal(res.code, 0, res.stderr);
    const expected = [...plainHeader(runId), '## rules: r1.md', ...numbered('r1', 1), '[cut: 9 lines dropped]', '## rules: r2.md', '[cut: 5 lines dropped]', ''].join('\n');
    assert.equal(readFileSync(packFile, 'utf8'), expected);
    assert.deepEqual([res.json.lines, res.json.cut], [20, 14]);
  });

  test('without max_lines the pack is cut at exactly 300 lines', async () => {
    const { repo, runId, packFile } = await packRepo({ context: 'context:\n  lessons: l.md\n', files: { 'l.md': fileOf('l', 400) } });
    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
    assert.equal(res.code, 0, res.stderr);
    // 15 protected lines + the lessons heading + 283 lessons lines + the marker = 300
    const expected = [...plainHeader(runId), '## lessons: l.md', ...numbered('l', 283), '[cut: 117 lines dropped]', ''].join('\n');
    assert.equal(readFileSync(packFile, 'utf8'), expected);
    assert.deepEqual([res.json.lines, res.json.cut], [300, 117]);
  });

  test('an empty or whitespace-only rules or lessons file gives its heading and no lines', async () => {
    const files = { 'empty.md': '', 'blank.md': '  \n\n\t\n', 'RULES.md': 'Rule one.\n', 'l.md': '\n' };
    const { repo, runId, packFile } = await packRepo({ context: 'context:\n  rules: [empty.md, blank.md, RULES.md]\n  lessons: l.md\n', files });
    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    assert.equal(res.code, 0, res.stderr);
    assert.equal(readFileSync(packFile, 'utf8'), [...plainHeader(runId), '## rules: empty.md', '## rules: blank.md', '## rules: RULES.md', 'Rule one.', '## lessons: l.md', ''].join('\n'));
    // the review digest has no blank line for them either
    assert.equal(reviewDigest({ repoRoot: repo, cfg: { context: { rules: ['empty.md', 'RULES.md', 'blank.md'], lessons: 'l.md' } } }), 'Rule one.\n### lessons');
  });

  test('a file whose name starts with ".." is inside the repository', async () => {
    const { repo, runId, packFile } = await packRepo({ context: 'context:\n  rules: [..notes.md]\n', files: { '..notes.md': 'Dotted rule.\n' } });
    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    assert.equal(res.code, 0, res.stderr);
    assert.equal(readFileSync(packFile, 'utf8'), [...plainHeader(runId), '## rules: ..notes.md', 'Dotted rule.', ''].join('\n'));
  });

  test('the pack goes under the run root of the run\'s own config snapshot, not the live tmp.root', async () => {
    const snapRoot = realpathSync(freshDir('ctx-snap-root'));
    chmodSync(snapRoot, 0o700);
    const files = { 'RULES.md': 'Rule one.\n' };
    const { repo, runId, slug, packFile } = await packRepo({ context: 'context:\n  rules: [RULES.md]\n', files, snapshot: { tmp: { root: snapRoot } } });
    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
    assert.equal(res.code, 0, res.stderr);
    const expectedFile = path.join(snapRoot, runId, 'context', 'B1.md');
    assert.equal(res.json.path, expectedFile);
    assert.equal(readFileSync(expectedFile, 'utf8'), [...plainHeader(runId), '## rules: RULES.md', 'Rule one.', ''].join('\n'));
    assert.equal(existsSync(packFile), false);
    assert.deepEqual((await packRows(slug, runId)).map((r) => r.path), [expectedFile]);
  });
});

describe('code-forge context pack: refusals write nothing', () => {
  /**
   * @param {{context?: string, files?: Record<string, string>, setup?: (repo: string) => void}} opts
   * @param {string} message - the exact stderr.
   */
  async function refused({ context, files, setup }, message) {
    const { repo, runId, slug, packFile } = await packRepo({ context, files });
    setup?.(repo);
    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    assert.deepEqual([res.code, res.stdout, res.stderr], [1, '', message]);
    assert.equal(existsSync(packFile), false);
    assert.deepEqual(await packRows(slug, runId), []);
  }

  test('no context key: exit 1 naming the key', async () => {
    await refused({}, 'context: the config has no context key: add context (rules, lessons, commands, max_lines) to .code-forge.yml; nothing was written\n');
  });

  test('a bad context value is refused naming the key: max_lines 19, a command that is not an argv list of strings, a rules file named twice', async () => {
    await refused({ context: 'context:\n  max_lines: 19\n' }, 'context: context.max_lines must be an integer >= 20; nothing was written\n');
    await refused({ context: 'context:\n  commands:\n    test: "npm test"\n' }, 'context: context.commands.test must be an argv list of non-empty strings; nothing was written\n');
    await refused({ context: 'context:\n  commands:\n    build: [npm, 7]\n' }, 'context: context.commands.build must be an argv list of non-empty strings; nothing was written\n');
    await refused({ context: 'context:\n  commands:\n    lint: [npm, ""]\n' }, 'context: context.commands.lint must be an argv list of non-empty strings; nothing was written\n');
    await refused({ context: 'context:\n  rules: [RULES.md, RULES.md]\n', files: { 'RULES.md': 'x\n' } }, 'context: context.rules names a file twice; nothing was written\n');
  });

  test('a missing rules file and a missing lessons file are refused naming the key and the path', async () => {
    await refused({ context: 'context:\n  rules: [RULES.md, nope.md]\n', files: { 'RULES.md': 'x\n' } }, 'context: context.rules: "nope.md" does not exist; nothing was written\n');
    await refused({ context: 'context:\n  lessons: docs/gone.md\n' }, 'context: context.lessons: "docs/gone.md" does not exist; nothing was written\n');
  });

  test('a path outside the repository (.., absolute, a symlink leading out) is refused', async () => {
    await refused({ context: 'context:\n  rules: [../outside.md]\n' }, 'context: context.rules: "../outside.md" is outside the repository (give a path relative to the repository root); nothing was written\n');
    const outside = path.join(freshDir('ctx-outside'), 'secret.md');
    writeFileSync(outside, 'not for the pack\n');
    await refused({ context: `context:\n  lessons: ${outside}\n` }, `context: context.lessons: ${JSON.stringify(outside)} is outside the repository (give a path relative to the repository root); nothing was written\n`);
    await refused(
      { context: 'context:\n  rules: [link.md]\n', setup: (repo) => symlinkSync(outside, path.join(repo, 'link.md')) },
      'context: context.rules: "link.md" leads outside the repository; nothing was written\n',
    );
    await refused(
      { context: 'context:\n  rules: [out/secret.md]\n', setup: (repo) => symlinkSync(path.dirname(outside), path.join(repo, 'out')) },
      'context: context.rules: "out/secret.md" leads outside the repository; nothing was written\n',
    );
  });

  test('a broken symlink is refused: "leads outside" when its target is outside, "does not exist" when it is inside', async () => {
    const gone = path.join(freshDir('ctx-gone'), 'never-written.md');
    await refused(
      { context: 'context:\n  rules: [dangling.md]\n', setup: (repo) => symlinkSync(gone, path.join(repo, 'dangling.md')) },
      'context: context.rules: "dangling.md" leads outside the repository; nothing was written\n',
    );
    await refused(
      { context: 'context:\n  lessons: dangling.md\n', setup: (repo) => symlinkSync(path.join(repo, 'docs', 'not-there.md'), path.join(repo, 'dangling.md')) },
      'context: context.lessons: "dangling.md" does not exist; nothing was written\n',
    );
    // a broken directory link on the way is followed too
    await refused(
      { context: 'context:\n  rules: [away/x.md]\n', setup: (repo) => symlinkSync(path.dirname(gone) + '-missing', path.join(repo, 'away')) },
      'context: context.rules: "away/x.md" leads outside the repository; nothing was written\n',
    );
  });

  test('a symlink that stays inside the repository is followed', async () => {
    const { repo, runId, packFile } = await packRepo({ context: 'context:\n  rules: [alias.md]\n', files: { 'docs/real.md': 'Real rule.\n' } });
    symlinkSync(path.join(repo, 'docs', 'real.md'), path.join(repo, 'alias.md'));
    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    assert.equal(res.code, 0, res.stderr);
    assert.equal(readFileSync(packFile, 'utf8'), [...plainHeader(runId), '## rules: alias.md', 'Real rule.', ''].join('\n'));
  });

  test('usage errors exit 2 with the exact usage; an unknown block exits 1 and writes no file and no row', async () => {
    const { repo, runId, slug, packFile } = await packRepo({ context: FULL_CONTEXT, files: FULL_FILES });
    const USAGE = 'usage: code-forge context pack --run <id> --block <id> [--json]\n';
    const NEEDS = `context: context pack needs --run and --block\n${USAGE}`;
    /** @type {Array<[string[], string]>} */
    const cases = [
      [['context'], USAGE],
      [['context', 'unpack'], USAGE],
      [['context', 'pack', '--run', runId], NEEDS],
      [['context', 'pack', '--block', 'B1', '--run', runId, 'extra'], NEEDS],
      [['context', 'pack', '--run', runId, '--block', 'b 1'], `context: --block: "b 1" is not a block id\n${USAGE}`],
    ];
    for (const [args, stderr] of cases) {
      const res = await cli(args, repo);
      assert.deepEqual([res.code, res.stdout, res.stderr], [2, '', stderr], args.join(' '));
    }
    const unknown = await cli(['context', 'pack', '--run', runId, '--block', 'B9'], repo);
    assert.deepEqual([unknown.code, unknown.stdout, unknown.stderr], [1, '', `context: run ${runId} has no block B9; nothing was written\n`]);
    assert.equal(existsSync(path.dirname(packFile)), false);
    assert.deepEqual(await packRows(slug, runId), []);
  });

  test('a control character in a command token, a rules or lessons path, or a header value is refused with its own message', async () => {
    await refused({ context: 'context:\n  commands:\n    lint: [sh, "-c\\nrm -rf x"]\n' }, 'context: context.commands.lint contains a control character; nothing was written\n');
    await refused({ context: 'context:\n  commands:\n    test: ["npm\\ttest"]\n' }, 'context: context.commands.test contains a control character; nothing was written\n');
    await refused({ context: 'context:\n  rules: ["a\\nb.md"]\n' }, 'context: context.rules: "a\\nb.md" contains a control character; nothing was written\n');
    await refused({ context: 'context:\n  lessons: "l\\u001b.md"\n' }, 'context: context.lessons: "l\\u001b.md" contains a control character; nothing was written\n');
    // an owned or test file name in the run record reaches the header: one with a newline is refused
    for (const [field, label] of [['owned_files', 'owned file'], ['test_files', 'test file']]) {
      const { repo, runId, slug, packFile } = await packRepo({ context: 'context:\n  rules: [RULES.md]\n', files: { 'RULES.md': 'x\n' } });
      const recordFile = runRecordPath(runId);
      const record = JSON.parse(readFileSync(recordFile, 'utf8'));
      record.blocks.B1[field] = ['src/a.mjs\n## rules: forged.md'];
      writeFileSync(recordFile, JSON.stringify(record));
      const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
      assert.deepEqual([res.code, res.stdout, res.stderr], [1, '', `context: block B1: ${label} "src/a.mjs\\n## rules: forged.md" contains a control character; nothing was written\n`]);
      assert.equal(existsSync(packFile), false);
      assert.deepEqual(await packRows(slug, runId), []);
    }
  });

  test('the same rules file named twice after normalizing (RULES.md and ./RULES.md) is refused', async () => {
    await refused({ context: 'context:\n  rules: [RULES.md, ./RULES.md]\n', files: { 'RULES.md': 'x\n' } }, 'context: context.rules names a file twice; nothing was written\n');
  });

  test('an absolute path is refused even when it points inside the repository', async () => {
    const { repo, runId, slug, packFile } = await packRepo({ files: { 'RULES.md': 'x\n' } });
    const abs = path.join(repo, 'RULES.md');
    writeFileSync(path.join(repo, '.code-forge.yml'), `${BASE_CONFIG(slug)}context:\n  rules: [${abs}]\n`);
    const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    assert.deepEqual([res.code, res.stdout, res.stderr], [1, '', `context: context.rules: ${JSON.stringify(abs)} is outside the repository (give a path relative to the repository root); nothing was written\n`]);
    assert.equal(existsSync(packFile), false);
    assert.deepEqual(await packRows(slug, runId), []);
  });

  test('a file whose lstat fails with anything but ENOENT is "cannot be read (<code>)"', async () => {
    const { repo, runId, slug, packFile } = await packRepo({ context: 'context:\n  rules: [locked/x.md]\n', files: { 'locked/x.md': 'x\n' } });
    chmodSync(path.join(repo, 'locked'), 0o000);
    try {
      const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
      assert.deepEqual([res.code, res.stdout, res.stderr], [1, '', 'context: context.rules: "locked/x.md" cannot be read (EACCES); nothing was written\n']);
      assert.equal(existsSync(packFile), false);
      assert.deepEqual(await packRows(slug, runId), []);
    } finally {
      chmodSync(path.join(repo, 'locked'), 0o700);
    }
  });

  test('a temp file that cannot be written is exit 1 with the reason: no file, no row, nothing was written', async () => {
    const { runId, slug, packFile } = await packRepo({ context: 'context:\n  rules: [RULES.md]\n', files: { 'RULES.md': 'Rule one.\n' } });
    const dir = path.dirname(packFile);
    mkdirSync(dir, { recursive: true });
    chmodSync(dir, 0o500);
    try {
      const stdout = capture();
      const stderr = capture();
      const code = await packInProcess(['pack', '--run', runId, '--block', 'B1'], { stdout, stderr });
      assert.deepEqual([code, stdout.text, stderr.text], [1, '', `context: cannot write ${packFile} (EACCES); nothing was written\n`]);
      assert.deepEqual(readdirSync(dir), []);
      assert.deepEqual(await packRows(slug, runId), []);
    } finally {
      chmodSync(dir, 0o700);
    }
  });

  test('a rename that fails after the row was appended says the row was written; the temp file is removed', async () => {
    const { runId, slug, packFile } = await packRepo({ context: 'context:\n  rules: [RULES.md]\n', files: { 'RULES.md': 'Rule one.\n' } });
    // a directory where the pack goes: the rename fails
    mkdirSync(path.join(packFile, 'occupied'), { recursive: true });
    const stdout = capture();
    const stderr = capture();
    const code = await packInProcess(['pack', '--run', runId, '--block', 'B1'], { stdout, stderr });
    assert.equal(code, 1);
    assert.equal(stdout.text, '');
    assert.match(stderr.text, new RegExp(`^context: cannot write ${packFile.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} \\([A-Z]+\\); its context.pack ledger row was already written\\n$`));
    assert.deepEqual(readdirSync(path.dirname(packFile)), ['B1.md']);
    assert.deepEqual(readdirSync(packFile), ['occupied']);
    assert.equal((await packRows(slug, runId)).length, 1);
  });

  test('a run root that cannot be used (an untrusted tmp.root) is exit 1 with the reason, nothing written', async () => {
    const snapRoot = realpathSync(freshDir('ctx-open-root'));
    chmodSync(snapRoot, 0o777);
    try {
      const { repo, runId, slug } = await packRepo({ context: 'context:\n  rules: [RULES.md]\n', files: { 'RULES.md': 'Rule one.\n' }, snapshot: { tmp: { root: snapRoot } } });
      const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
      assert.equal(res.code, 1, res.stderr);
      assert.equal(res.stdout, '');
      assert.match(res.stderr, /^context: the run root cannot be used \(.+\); nothing was written\n$/);
      assert.equal(existsSync(path.join(snapRoot, runId, 'context')), false);
      assert.deepEqual(await packRows(slug, runId), []);
    } finally {
      chmodSync(snapRoot, 0o700);
    }
  });

  test('a ledger row that cannot be appended leaves the earlier pack exactly as it was: exit 1, no new row, no temp file', async () => {
    const { repo, runId, slug, packFile } = await packRepo({ context: 'context:\n  rules: [RULES.md]\n', files: { 'RULES.md': 'Rule one.\n' } });
    const first = await cli(['context', 'pack', '--run', runId, '--block', 'B1'], repo);
    assert.equal(first.code, 0, first.stderr);
    const earlier = readFileSync(packFile, 'utf8');
    writeFileSync(path.join(repo, 'RULES.md'), 'Rule one, changed.\n');
    const stdout = capture();
    const stderr = capture();
    const appendRow = async () => {
      throw new Error('disk full');
    };
    const code = await packInProcess(['pack', '--run', runId, '--block', 'B1'], { stdout, stderr, appendRow });
    assert.deepEqual([code, stdout.text, stderr.text], [1, '', 'context: the context.pack ledger row could not be written (disk full); nothing was written\n']);
    assert.equal(readFileSync(packFile, 'utf8'), earlier);
    assert.deepEqual(readdirSync(path.dirname(packFile)), ['B1.md']);
    assert.deepEqual((await packRows(slug, runId)).map((r) => r.sha256), [sha256Of(earlier)]);
  });

  test('a command line naming a registered secret is redacted in the pack, as rules and lessons are', async () => {
    const secret = 'fake-token-ZQ81x-not-real';
    registerSecret(secret);
    const { runId, packFile } = await packRepo({ context: `context:\n  commands:\n    test: [curl, -H, "Authorization: Bearer ${secret}"]\n`, files: {} });
    const stdout = capture();
    const stderr = capture();
    const code = await packInProcess(['pack', '--run', runId, '--block', 'B1'], { stdout, stderr });
    assert.equal(code, 0, stderr.text);
    const text = readFileSync(packFile, 'utf8');
    assert.equal(text, [...plainHeader(runId).slice(0, -7), "test: curl -H 'Authorization: Bearer [REDACTED]'", ...PROOF_LINES, ''].join('\n'));
    assert.equal(text.includes(secret), false);
  });

  test('an empty argv token is shown as \'\' (the config refuses one; the renderer still quotes it)', () => {
    assert.equal(argvText(['printf', '%s', '']), "printf %s ''");
  });
});

describe('review packets carry the context rules and lessons (B63a)', () => {
  const RULES = 'Rule one.\nRule two.\n';
  const STYLE = 'Style A.\n';
  const LESSONS = 'Lesson X.\nLesson Y.\n';
  const CONTEXT = 'context:\n  rules: [RULES.md, docs/style.md]\n  lessons: docs/lessons.md\n';
  const WORKTREE = 'proof:\n  isolation: worktree\n';
  const DIGEST = 'Rule one.\nRule two.\nStyle A.\n### lessons\nLesson X.\nLesson Y.';
  const FILE = 'src/a.mjs';
  /** @param {string} id @param {number} line */
  const finding = (id, line) => ({ id, file: FILE, line_start: line, line_end: line, severity: 'warning', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

  /** @param {string} packet @returns {string} the `## project rules` slot's text. */
  const slotOf = (packet) => {
    const from = packet.indexOf('\n## project rules\n');
    const to = packet.indexOf('\n## facts\n');
    assert.ok(from >= 0 && to > from, packet.slice(0, 2000));
    return packet.slice(from + '\n## project rules\n'.length, to);
  };

  /**
   * A committed repo (`src/a.mjs` of 20 lines, the rules and lessons files), a run started with
   * `startContext` (+ `extra` config lines) whose worker `run start` launched, and block B1 open on
   * src/a.mjs at L2. `stop()` stops the worker.
   * @param {{startContext: string, extra?: string, files?: Record<string, string>}} opts
   */
  async function reviewFixture({ startContext, extra = '', files = { 'RULES.md': RULES, 'docs/style.md': STYLE, 'docs/lessons.md': LESSONS } }) {
    const repo = realpathSync(freshDir('ctx-review'));
    await git(repo, ['init', '-q', '-b', 'main']);
    seq += 1;
    const slug = `ctx-review-${seq}`;
    writeFileSync(path.join(repo, '.code-forge.yml'), BASE_CONFIG(slug) + extra + startContext);
    mkdirSync(path.join(repo, 'src'));
    writeFileSync(path.join(repo, FILE), Array.from({ length: 20 }, (_, k) => `export const v${k + 1} = ${k + 1};\n`).join(''));
    for (const [rel, text] of Object.entries(files)) {
      mkdirSync(path.dirname(path.join(repo, rel)), { recursive: true });
      writeFileSync(path.join(repo, rel), text);
    }
    await git(repo, ['add', '-A']);
    await git(repo, ['commit', '-q', '-m', 'base']);
    const origin = realpathSync(freshDir('ctx-origin'));
    await git(origin, ['init', '-q', '--bare']);
    await git(repo, ['remote', 'add', 'origin', origin]);
    const runId = `r-ctx-rev-${process.pid}-${seq}`;
    const records = freshDir('ctx-records');
    const scriptFile = path.join(freshDir('ctx-script'), 'script.json');
    writeFileSync(scriptFile, '{}');
    const env = { FAKE_RECORD: records, FAKE_SCRIPT: scriptFile };
    const started = await cli(['run', 'start', '--run', runId], repo, env);
    assert.equal(started.code, 0, started.stderr);
    const live = liveWorker(repo);
    if (live) stopPidAfter(live.pid, runId);
    const stop = () => (live ? stopPid(live.pid, runId) : Promise.resolve());
    try {
      const acceptance = path.join(freshDir('ctx-acc'), 'acceptance.yml');
      writeFileSync(acceptance, '- clause: the rules reach the reviewer\n  tests: [context]\n');
      const opened = await cli(['block', 'open', 'B1', '--run', runId, '--level', 'L2', '--owned', FILE, '--acceptance', acceptance], repo, env);
      assert.equal(opened.code, 0, opened.stderr);
    } catch (err) {
      await stop();
      throw err;
    }
    return {
      repo,
      runId,
      slug,
      env,
      stop,
      /** @param {Record<string, any>} s */
      script: (s) => writeFileSync(scriptFile, JSON.stringify(s)),
      /** Rewrite `.code-forge.yml` with `context` and `run reload`. @param {string} context */
      reload: async (context) => {
        writeFileSync(path.join(repo, '.code-forge.yml'), BASE_CONFIG(slug) + extra + context);
        const reloaded = await cli(['run', 'reload', '--run', runId], repo, env);
        assert.equal(reloaded.code, 0, reloaded.stderr);
      },
      /** Change line `line` of src/a.mjs in `root` (default: the main tree). @param {number} line @param {string} tag @param {string} [root] */
      edit: (line, tag, root = repo) => {
        const full = path.join(root, FILE);
        const all = readFileSync(full, 'utf8').split('\n');
        all[line - 1] = `export const v${line} = ${JSON.stringify(tag)};`;
        writeFileSync(full, all.join('\n'));
      },
      /** `review-file` then `--wait`, from `cwd` (default: the main tree). @param {string} [cwd] */
      review: async (cwd = repo) => {
        const queued = await cli(['review-file', FILE, '--block', 'B1'], cwd, env);
        assert.equal(queued.json?.status, 'queued', queued.stdout + queued.stderr);
        const waited = await cli(['review-file', '--wait', queued.json.ticket, '--max', '30s'], cwd, env);
        assert.equal(waited.json?.status, 'done', waited.stdout);
        return { ...waited.json, ticket: queued.json.ticket };
      },
      /** @returns {string[]} the packets the fake reviewer was sent, oldest first. */
      packets: () =>
        readdirSync(records)
          .filter((n) => n.startsWith('packet-'))
          .sort((a, b) => (BigInt(a.slice(7, -3)) < BigInt(b.slice(7, -3)) ? -1 : 1))
          .map((n) => readFileSync(path.join(records, n), 'utf8')),
    };
  }

  test('with context set at run start, the worker packet holds the rules then "### lessons" in the project-rules slot', async () => {
    const f = await reviewFixture({ startContext: CONTEXT });
    try {
      f.edit(10, 'ten');
      await f.review();
      const packets = f.packets();
      assert.equal(packets.length, 1);
      assert.equal(slotOf(packets[0]), DIGEST);
    } finally {
      await f.stop();
    }
  });

  test('without context the slot is "(none)"; a context added by run reload reaches the next review', async () => {
    const f = await reviewFixture({ startContext: '' });
    try {
      f.edit(10, 'ten');
      await f.review();
      await f.reload(CONTEXT);
      f.edit(12, 'twelve');
      await f.review();
      const packets = f.packets();
      assert.equal(packets.length, 2);
      assert.equal(slotOf(packets[0]), '(none)');
      assert.equal(slotOf(packets[1]), DIGEST);
    } finally {
      await f.stop();
    }
  });

  test('rules and lessons over 60 lines share the one cap: the slot keeps the first 60 digest lines', async () => {
    const files = { 'RULES.md': fileOf('rule', 50), 'docs/lessons.md': fileOf('lesson', 20) };
    const f = await reviewFixture({ startContext: 'context:\n  rules: [RULES.md]\n  lessons: docs/lessons.md\n', files });
    try {
      f.edit(10, 'ten');
      await f.review();
      const packets = f.packets();
      assert.equal(packets.length, 1);
      assert.equal(slotOf(packets[0]), [...numbered('rule', 50), '### lessons', ...numbered('lesson', 9)].join('\n'));
    } finally {
      await f.stop();
    }
  });

  test('a worktree block\'s packet carries the MAIN tree\'s rules and lessons, never the worktree\'s', async () => {
    const f = await reviewFixture({ startContext: CONTEXT, extra: WORKTREE });
    try {
      const tree = (await readRun(f.runId)).blocks.B1.worktree.path;
      assert.equal(typeof tree, 'string');
      writeFileSync(path.join(tree, 'RULES.md'), 'Tree rule, written by the coder.\n');
      writeFileSync(path.join(tree, 'docs', 'lessons.md'), 'Tree lesson.\n');
      f.edit(10, 'ten', tree);
      const out = await f.review(tree);
      assert.equal(out.result.status, 'reviewed');
      const packets = f.packets();
      assert.equal(packets.length, 1);
      assert.equal(slotOf(packets[0]), DIGEST);
      assert.equal(packets[0].includes('Tree rule'), false);
      assert.equal(packets[0].includes('Tree lesson'), false);
    } finally {
      await f.stop();
    }
  });

  test('a recheck packet carries the digest too', async () => {
    const f = await reviewFixture({ startContext: CONTEXT });
    try {
      f.script({ full: { findings: [finding('F1', 10)] }, recheck: { resolve: true, findings: [] } });
      f.edit(10, 'change');
      const first = await f.review();
      assert.deepEqual([first.result.status, first.result.approved, first.result.findings.length], ['reviewed', false, 1]);
      f.edit(10, 'fixed');
      const second = await f.review();
      assert.deepEqual([second.result.status, second.result.approved, second.result.kind], ['reviewed', true, 'recheck']);
      const packets = f.packets();
      assert.equal(packets.length, 2);
      assert.equal(packets[0].includes('\n## open findings\n'), false);
      assert.equal(packets[1].includes('\n## open findings\n- F1 (warning, lines 10-10)'), true);
      assert.deepEqual(packets.map(slotOf), [DIGEST, DIGEST]);
    } finally {
      await f.stop();
    }
  });

  test('a rules or lessons file missing at review time: the ticket is unavailable (context-unreadable), no session runs, the packet dir is removed; review-file on the same content retries, as often as asked; back in place, it is reviewed', async () => {
    const f = await reviewFixture({ startContext: CONTEXT });
    try {
      rmSync(path.join(f.repo, 'docs', 'lessons.md'));
      f.edit(10, 'ten');
      const out = await f.review();
      assert.deepEqual([out.result.status, out.result.reason, out.result.approved], ['unavailable', 'context-unreadable', false]);
      assert.deepEqual(f.packets(), []);
      assert.equal(existsSync(path.join(PARENT, 'code-forge', f.runId, 'packets', out.ticket)), false);

      // final state: the ticket is done (its done marker written); the worker never retries it
      assert.equal(existsSync(path.join(f.repo, '.code-forge', 'queue', `${out.ticket}.done`)), true);

      // `review-file` on the SAME content is a fresh attempt (`retry: true`), as often as asked:
      // still missing, it is unavailable again; back in place, it is reviewed
      /** @returns {Promise<Record<string, any>>} */
      const retry = async () => {
        const queued = await cli(['review-file', FILE, '--block', 'B1'], f.repo, f.env);
        assert.deepEqual([queued.json?.ticket, queued.json?.status, queued.json?.retry], [out.ticket, 'queued', true], queued.stdout);
        const waited = await cli(['review-file', '--wait', out.ticket, '--max', '30s'], f.repo, f.env);
        assert.equal(waited.json?.status, 'done', waited.stdout);
        return waited.json.result;
      };
      const still = await retry();
      assert.deepEqual([still.status, still.reason], ['unavailable', 'context-unreadable']);
      assert.deepEqual(f.packets(), []);
      writeFileSync(path.join(f.repo, 'docs', 'lessons.md'), LESSONS);
      const again = await retry();
      assert.deepEqual([again.status, again.approved], ['reviewed', true]);
      assert.deepEqual(f.packets().map(slotOf), [DIGEST]);
    } finally {
      await f.stop();
    }
  });
});


test('B117 (1): empty context still carries exactly one complete proof section after commands', async () => {
  const { repo, runId, packFile } = await packRepo({ context: 'context: {}\n' });
  const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
  assert.equal(res.code, 0, res.stderr);
  assert.equal(readFileSync(packFile, 'utf8'), [...plainHeader(runId), ''].join('\n'));
  assert.deepEqual([res.json.lines, res.json.cut], [15, 0]);
});

test('B117 (1) boundary: protected proof exceeds max_lines intact after all rules and lessons are cut', async () => {
  const { repo, runId, packFile } = await packRepo({
    context: 'context:\n  rules: [r.md]\n  lessons: l.md\n  max_lines: 20\n',
    files: { 'r.md': fileOf('r', 5), 'l.md': fileOf('l', 5) },
    tests: Array.from({ length: 8 }, (_, i) => `test/${i}.test.mjs`),
  });
  const res = await cli(['context', 'pack', '--run', runId, '--block', 'B1', '--json'], repo);
  assert.equal(res.code, 0, res.stderr);
  const header = plainHeader(runId);
  header.splice(7, 0, 'test files:', ...Array.from({ length: 8 }, (_, i) => `- test/${i}.test.mjs`));
  assert.equal(readFileSync(packFile, 'utf8'), [...header, '## rules: r.md', '[cut: 5 lines dropped]', '## lessons: l.md', '[cut: 5 lines dropped]', ''].join('\n'));
  assert.deepEqual([res.json.lines, res.json.cut], [28, 10]);
});
