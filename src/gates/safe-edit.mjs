/**
 * The safe-edit gate (plan `gates.extra.safe_edit`, default true): abort a block whose diff
 * deletes a test file, or whose test-declaration count shrinks by more than 20 % against the
 * block's base — the two ways a coder can make a red suite look green by removing the evidence
 * instead of fixing the code, per the project's own "verification remains mandatory" rule.
 *
 * The decision (`safeEdit`) is a PURE function over two `{file: count}` maps so it is trivial to
 * test and cannot itself run a process; `checkSafeEdit` is the thin git-backed orchestration that
 * builds those maps for a real base/working-tree pair. Every git call strips inherited `GIT_*` env
 * (a hook's `GIT_DIR` would silently redirect the read) and disables system/global config.
 */

import os from 'node:os';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { exec } from '../util/exec.mjs';
import { showArgv } from '../util/git.mjs';

/** Fraction of the base test count below which the shrink aborts the gate (a >20% shrink). */
export const SHRINK_THRESHOLD = 0.8;

/**
 * File-name → stack, by basename pattern. A LIST (not a map keyed by stack) checked in order,
 * because file-classification and declaration-counting must agree on exactly one stack per file —
 * summing every stack's declaration pattern over one file double-counts wherever two stacks'
 * patterns overlap (PHP's `\btest\(`/`\bit\(` also match plain JS `test('x', ...)`/`it('x', ...)`
 * calls character-for-character), which is exactly the bug this per-file classification avoids.
 * @type {ReadonlyArray<{stack: string, pattern: RegExp}>}
 */
const TEST_FILE_KINDS = Object.freeze([
  { stack: 'node', pattern: /\.test\.[cm]?[jt]sx?$/ }, // foo.test.mjs, foo.test.ts
  { stack: 'node', pattern: /\.spec\.[cm]?[jt]sx?$/ },
  { stack: 'php', pattern: /Test\.php$/ }, // PHPUnit/Pest class-style
  { stack: 'python', pattern: /^test_.*\.py$/ }, // pytest
  { stack: 'go', pattern: /_test\.go$/ },
  // Rust has no test-FILE naming convention (a #[test] can live in any .rs file) — safe-edit's
  // file-level "deleted test file" check does not apply to it; `countTestDeclarations(x, 'rust')`
  // is still directly usable by a caller that already knows a given file is a rust test module.
]);

/** Per-stack test-declaration regexes (global, so `matchAll` can count every occurrence). */
const TEST_DECL_PATTERNS = Object.freeze({
  node: /\b(?:test|it)\s*\(/g,
  php: /(?:public\s+function\s+test[A-Z_]\w*\s*\(|\bit\s*\(|\btest\s*\()/g,
  python: /^\s*def test_\w+/gm,
  rust: /#\[test\]/g,
  go: /^func Test[A-Z]\w*/gm,
});

/**
 * @param {string} filePath
 * @returns {string | null} the stack `filePath`'s basename matches, by naming convention, or
 *   `null` when none does — the file is not treated as a test file at all.
 */
function classifyTestFile(filePath) {
  const base = path.basename(filePath);
  const kind = TEST_FILE_KINDS.find(({ pattern }) => pattern.test(base));
  return kind ? kind.stack : null;
}

/**
 * @param {string} filePath
 * @returns {boolean} whether `filePath`'s basename matches a known test-file naming convention.
 */
export function isTestFile(filePath) {
  return classifyTestFile(filePath) !== null;
}

/**
 * Count test declarations in `content`, using ONLY the given stack's pattern — never every
 * pattern summed, which would double-count where two stacks' patterns overlap (see
 * `TEST_FILE_KINDS`'s doc). A caller that does not already know the file's stack should classify
 * it first (`isTestFile` alone only says yes/no; this module classifies internally via
 * `classifyTestFile` for its own git-backed orchestration below).
 * @param {string} content @param {string} stack - one of `TEST_DECL_PATTERNS`'s keys.
 * @returns {number}
 * @throws {TypeError} on an unknown stack.
 */
export function countTestDeclarations(content, stack) {
  const pattern = /** @type {Record<string, RegExp>} */ (TEST_DECL_PATTERNS)[stack];
  if (!pattern) {
    throw new TypeError(`countTestDeclarations: unknown stack ${JSON.stringify(stack)} (known: ${Object.keys(TEST_DECL_PATTERNS).join(', ')})`);
  }
  // The pattern object carries the `g` flag's own `lastIndex`; matchAll on a fresh copy avoids
  // any stateful reuse across calls.
  return [...content.matchAll(new RegExp(pattern.source, pattern.flags))].length;
}

/**
 * @typedef {Record<string, number>} CountsByFile - test-declaration count per test-file path.
 * @typedef {{ok: true} | {ok: false, reason: string}} SafeEditResult
 */

/**
 * The pure decision (plan acceptance: "aborts on a >20% test-count shrink and on a deleted test
 * file"). Deleted-file check runs first: a deleted file makes the shrink check redundant but a
 * caller should see the more specific reason.
 * @param {{base: CountsByFile, current: CountsByFile}} counts
 * @returns {SafeEditResult}
 */
export function safeEdit({ base, current }) {
  const deleted = Object.keys(base).filter((file) => !(file in current));
  if (deleted.length > 0) {
    return { ok: false, reason: `deleted test file(s): ${deleted.sort().join(', ')}` };
  }
  const totalBase = Object.values(base).reduce((a, b) => a + b, 0);
  const totalCurrent = Object.values(current).reduce((a, b) => a + b, 0);
  if (totalBase > 0 && totalCurrent < totalBase * SHRINK_THRESHOLD) {
    return { ok: false, reason: `test count shrank from ${totalBase} to ${totalCurrent} (more than 20%)` };
  }
  return { ok: true };
}

/** `process.env` minus every inherited `GIT_*`, with no system/global config. */
function gitEnv() {
  /** @type {NodeJS.ProcessEnv} */
  const env = { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull };
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  return env;
}

const GIT_TIMEOUT_MS = 30000;

/**
 * `git ls-tree -r -z --name-only <base>` — NUL-terminated, unquoted paths, the same `-z`
 * convention `gates/scope.mjs` uses and this file's own `currentTestFiles` already uses for the
 * CURRENT side. Without `-z`, git C-quotes a path containing non-ASCII bytes, `"`, `\` or control
 * characters (e.g. `café.test.mjs` → `"caf\303\251.test.mjs"`); a newline-split reader then gets
 * that quoted literal, whose basename ends in `.mjs"` — `classifyTestFile` no longer recognizes
 * it as a test file at all, so a deleted quoted-name test file is silently dropped from the base
 * count instead of tripping the deleted-file check. Not one of `util/git.mjs`'s read-only
 * builders (its `lsFilesArgv` deliberately refuses any `-`-leading element, so this couldn't be
 * built through it either way); `base` is guarded the same way those builders guard their own ref
 * arguments.
 * @param {string} base @returns {string[]}
 */
function lsTreeArgv(base) {
  if (typeof base !== 'string' || base.length === 0 || base.startsWith('-')) {
    throw new TypeError(`safe-edit: base must be a non-empty string not starting with "-" (got ${JSON.stringify(base)})`);
  }
  return ['git', 'ls-tree', '-r', '-z', '--name-only', base];
}

/**
 * @param {string} base @param {string} cwd
 * @returns {Promise<{file: string, stack: string}[]>} every test file tracked at `base`, with the
 *   stack its name classified to.
 */
async function testFilesAtBase(base, cwd) {
  const res = await exec(lsTreeArgv(base), { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  if (res.result !== 'ok') {
    throw new Error(`safe-edit: git ls-tree -r -z --name-only ${base} failed (exit ${res.code}): ${res.stderr}`);
  }
  /** @type {{file: string, stack: string}[]} */
  const testFiles = [];
  for (const file of splitNul(res.stdout)) {
    const stack = classifyTestFile(file);
    if (stack !== null) testFiles.push({ file, stack });
  }
  return testFiles;
}

/**
 * @param {string} base @param {string} filePath @param {string} stack @param {string} cwd
 * @returns {Promise<number>} test-declaration count of `filePath` AT `base`.
 */
async function countAtBase(base, filePath, stack, cwd) {
  const res = await exec(showArgv(base, filePath), { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  if (res.result !== 'ok') {
    throw new Error(`safe-edit: git show ${base}:${filePath} failed (exit ${res.code}): ${res.stderr}`);
  }
  return countTestDeclarations(res.stdout, stack);
}

/**
 * `git ls-files -z` (tracked, incl. staged) — NUL-terminated so a path with whitespace/quotes is
 * read exactly, matching `gates/scope.mjs`'s `-z` convention.
 * @returns {string[]}
 */
function lsFilesZArgv() {
  return ['git', 'ls-files', '-z'];
}

/** `git ls-files -z --others --exclude-standard` — untracked, not gitignored. @returns {string[]} */
function lsFilesOthersZArgv() {
  return ['git', 'ls-files', '-z', '--others', '--exclude-standard'];
}

/** @param {string} text @returns {string[]} NUL-split entries, empty ones dropped, never trimmed. */
function splitNul(text) {
  return text.split('\0').filter((entry) => entry.length > 0);
}

/**
 * Every CURRENT test file in the working tree — tracked union untracked-but-not-ignored — each
 * classified to its stack. This is deliberately NOT limited to base's file list: a test moved
 * from one file to a NEW one (a coder splitting `a.test.mjs`'s 10 tests into `a.test.mjs` (2) +
 * `b.test.mjs` (8), still 10 total) must still be counted, or the shrink check would misread a
 * pure reorganization as a real loss of coverage.
 * @param {string} cwd
 * @returns {Promise<{file: string, stack: string}[]>}
 */
async function currentTestFiles(cwd) {
  const [tracked, untracked] = await Promise.all([
    exec(lsFilesZArgv(), { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS }),
    exec(lsFilesOthersZArgv(), { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS }),
  ]);
  if (tracked.result !== 'ok') {
    throw new Error(`safe-edit: git ls-files -z failed (exit ${tracked.code}): ${tracked.stderr}`);
  }
  if (untracked.result !== 'ok') {
    throw new Error(`safe-edit: git ls-files -z --others --exclude-standard failed (exit ${untracked.code}): ${untracked.stderr}`);
  }
  const files = new Set([...splitNul(tracked.stdout), ...splitNul(untracked.stdout)]);
  /** @type {{file: string, stack: string}[]} */
  const testFiles = [];
  for (const file of files) {
    const stack = classifyTestFile(file);
    if (stack !== null) testFiles.push({ file, stack });
  }
  return testFiles;
}

/**
 * @param {string} filePath @param {string} stack @param {string} cwd
 * @returns {Promise<number | null>} test-declaration count of `filePath` on DISK, or `null` when
 *   it no longer exists there (a race between listing it and reading it — not the normal "was it
 *   deleted" question, which `safeEdit` answers from the base/current file-name sets themselves).
 */
async function countOnDisk(filePath, stack, cwd) {
  try {
    const content = await readFile(path.join(cwd, filePath), 'utf8');
    return countTestDeclarations(content, stack);
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT') return null;
    throw err;
  }
}

/**
 * The git-backed orchestration: lists test files tracked at `base`, and SEPARATELY every test file
 * present in the CURRENT working tree (tracked + untracked, not just base's own file list — see
 * `currentTestFiles`'s doc), counts each with its own stack's pattern, then applies `safeEdit`.
 * @param {{cwd: string, base: string}} opts
 * @returns {Promise<SafeEditResult>}
 */
export async function checkSafeEdit({ cwd, base }) {
  const [baseFiles, currentFiles] = await Promise.all([testFilesAtBase(base, cwd), currentTestFiles(cwd)]);

  /** @type {CountsByFile} */
  const baseCounts = {};
  for (const { file, stack } of baseFiles) {
    baseCounts[file] = await countAtBase(base, file, stack, cwd);
  }

  /** @type {CountsByFile} */
  const currentCounts = {};
  for (const { file, stack } of currentFiles) {
    const count = await countOnDisk(file, stack, cwd);
    if (count !== null) currentCounts[file] = count;
  }

  return safeEdit({ base: baseCounts, current: currentCounts });
}
