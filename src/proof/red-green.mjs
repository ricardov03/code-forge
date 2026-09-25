/**
 * The journaled red→green runner (plan §7.2, O13, O35). A product mechanism run by the CLI on a
 * consumer's tree — in the block's measurement export (`isolation: export`, B10a) or, under the
 * proof lock, in the shared tree (`isolation: lock`). Never used on code-forge's own build (R14).
 *
 * One call proves one test (`file`, optionally `::case` for the label; the caller's `argv` is the
 * test command already filtered to it):
 *
 *   1. refuse when a journal exists in the work dir (an earlier run was interrupted: `proof restore`);
 *   2. snapshot the files the red step will change — bytes (base64), sha256, mode, or "absent" —
 *      into `<workDir>/.code-forge/proof-journal.json`, written atomically BEFORE any change;
 *   3. write the red state:
 *        mechanism `revert` — every non-test source file at its base version (read from the repo
 *        with a temporary index + `checkout-index`, never `git stash`/`checkout --`; binary-safe);
 *        a file absent at base is removed;
 *        mechanism `assertion-deletion` — for a `characterization` test (behaviour that already
 *        exists at base) or a test with no source change: every single-line assertion statement
 *        of the test file is deleted and replaced by a forced failure of the same assertion
 *        library (`assert.fail(…)` / `$this->fail(…)`). RED then proves the test's assertions are
 *        reached by the filtered run and decide its outcome; a test whose assertions never run
 *        stays green and proves nothing;
 *   4. run `argv` and classify the output with `red-parse` (RED only on an assertion failure);
 *   5. restore every journaled file, verify each sha256, delete the journal — in a `finally`, so an
 *      ordinary error of the red step (a write error, a command that does not start) restores too;
 *   6. when the red step was `RED`, run `argv` again: exit 0 ⇒ `GREEN`.
 *
 * Signals: while the test child runs, `exec` forwards SIGINT/SIGTERM/SIGHUP to its process group
 * and the runner dies of the signal. The journal is left in place on purpose: restoring from a
 * signal handler could itself be interrupted, so the one recovery path is `proof restore`, which
 * verifies every hash. A SIGKILL leaves the same journal.
 *
 * Prints one line per step (`RED <label>` · `GREEN <label>` · `RED_INVALID <label> (<kind>)` ·
 * `NOT_RED <label>` · `NOT_GREEN <label>`) and, when `record` is given, writes a signed `proof`
 * row through B10a/B6 with `mechanism` and `red_kind`.
 */

import { createHash } from 'node:crypto';
import { chmod, lstat, mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { StateError } from '../state/paths.mjs';
import { exec } from '../util/exec.mjs';
import { revParse } from '../util/git.mjs';
import { currentRunRoot } from '../util/tmp.mjs';
import { ISOLATIONS, recordProof } from './export.mjs';
import { parseRed } from './red-parse.mjs';

/** Where the journal lives, relative to the work dir. */
export const JOURNAL_REL = path.join('.code-forge', 'proof-journal.json');
export const MECHANISMS = Object.freeze(/** @type {const} */ (['revert', 'assertion-deletion']));

const GIT_TIMEOUT_MS = 120000;
const DEFAULT_TEST_TIMEOUT_MS = 600000;
const JOURNAL_VERSION = 1;
const DELETED_MARKER = 'code-forge: assertion deleted';

/**
 * @typedef {{path: string, existed: false} | {path: string, existed: true, sha256: string, mode: number, content: string}} JournalEntry
 * @typedef {{version: number, pid: number, created_at: string, mechanism: string, block?: string, test: string, files: JournalEntry[]}} Journal
 */

/** @param {Buffer} buf @returns {string} */
export const sha256 = (buf) => createHash('sha256').update(buf).digest('hex');

/** @param {string} workDir @returns {string} */
export const journalPath = (workDir) => path.join(workDir, JOURNAL_REL);

/** `process.env` minus every `GIT_*` (a hook's `GIT_DIR` must not redirect us), plus `extra`. */
function gitEnv(/** @type {Record<string, string>} */ extra = {}) {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) if (!key.startsWith('GIT_')) env[key] = value;
  return { ...env, ...extra };
}

/** @param {string[]} args @param {string} cwd @param {{input?: string, env?: Record<string, string>}} [opts] */
async function git(args, cwd, opts = {}) {
  const res = await exec(['git', ...args], { cwd, env: gitEnv(opts.env), input: opts.input, timeoutMs: GIT_TIMEOUT_MS });
  if (res.result !== 'ok') throw new StateError('git-failed', `git ${args[0]} failed (exit ${res.code})`);
  return res.stdout;
}

/**
 * A repo-relative POSIX path with no `.`/`..`/empty segment, never under `.code-forge/`.
 * @param {unknown} p @param {string} key @returns {string}
 */
function assertRelPath(p, key) {
  if (typeof p !== 'string' || p.length === 0 || path.isAbsolute(p) || p.includes('\\') || p.includes('\0')) {
    throw new StateError('bad-path', `${key}: every entry must be a repo-relative POSIX path`);
  }
  const segments = p.split('/');
  if (segments.some((s) => s === '' || s === '.' || s === '..')) throw new StateError('bad-path', `${key}: no ".", ".." or empty segments`);
  if (segments[0] === '.code-forge') throw new StateError('bad-path', `${key}: nothing under .code-forge/`);
  return p;
}

/** @param {string} p @returns {Promise<import('node:fs').Stats | null>} */
async function lstatOrNull(p) {
  try {
    return await lstat(p);
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT') return null;
    throw err;
  }
}

/** @param {string} workDir @returns {Promise<Journal | null>} */
export async function readJournal(workDir) {
  let text;
  try {
    text = await readFile(journalPath(workDir), 'utf8');
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT') return null;
    throw err;
  }
  const journal = JSON.parse(text);
  if (journal?.version !== JOURNAL_VERSION || !Array.isArray(journal.files)) throw new StateError('bad-journal', `${JOURNAL_REL} is not a version ${JOURNAL_VERSION} journal`);
  for (const entry of journal.files) assertRelPath(entry?.path, 'journal');
  return journal;
}

/**
 * Snapshot `files` (bytes + hash + mode, or absent) and write the journal atomically.
 * @param {string} workDir @param {string[]} files @param {Omit<Journal, 'version' | 'pid' | 'created_at' | 'files'>} meta
 * @returns {Promise<Journal>}
 */
async function writeJournal(workDir, files, meta) {
  /** @type {JournalEntry[]} */
  const entries = [];
  for (const rel of files) {
    const abs = path.join(workDir, rel);
    const st = await lstatOrNull(abs);
    if (st === null) {
      entries.push({ path: rel, existed: false });
      continue;
    }
    if (!st.isFile()) throw new StateError('bad-path', `red-green: ${rel} is not a regular file`);
    const bytes = await readFile(abs);
    entries.push({ path: rel, existed: true, sha256: sha256(bytes), mode: st.mode & 0o777, content: bytes.toString('base64') });
  }
  /** @type {Journal} */
  const journal = { version: JOURNAL_VERSION, pid: process.pid, created_at: new Date().toISOString(), ...meta, files: entries };
  const file = journalPath(workDir);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(`${file}.tmp`, `${JSON.stringify(journal)}\n`, { mode: 0o600 });
  await rename(`${file}.tmp`, file);
  return journal;
}

/**
 * `proof restore`: put every journaled file back byte for byte, verify every sha256, delete the
 * journal. A mismatch keeps the journal and throws.
 * @param {{workDir: string}} opts
 * @returns {Promise<{restored: string[]}>} repo-relative paths, journal order
 * @throws {StateError} `no-journal`, `bad-journal`, `hash-mismatch`
 */
export async function restoreJournal({ workDir }) {
  if (typeof workDir !== 'string' || !path.isAbsolute(workDir)) throw new StateError('bad-path', 'restore: the work dir must be an absolute path');
  const journal = await readJournal(workDir);
  if (journal === null) throw new StateError('no-journal', `no red→green journal in ${workDir} — nothing to restore`);
  for (const entry of journal.files) {
    const abs = path.join(workDir, entry.path);
    if (entry.existed) {
      await mkdir(path.dirname(abs), { recursive: true });
      await rm(abs, { force: true }); // a symlink left in its place is replaced, never followed
      await writeFile(abs, Buffer.from(entry.content, 'base64'), { mode: entry.mode });
      await chmod(abs, entry.mode);
    } else {
      await rm(abs, { force: true });
    }
  }
  const bad = [];
  for (const entry of journal.files) {
    const abs = path.join(workDir, entry.path);
    const st = await lstatOrNull(abs);
    if (entry.existed ? !st?.isFile() || sha256(await readFile(abs)) !== entry.sha256 : st !== null) bad.push(entry.path);
  }
  if (bad.length > 0) throw new StateError('hash-mismatch', `restore: ${bad.length} file(s) do not match the journal (${bad.join(', ')}); the journal is kept`);
  await rm(journalPath(workDir), { force: true });
  return { restored: journal.files.map((e) => e.path) };
}

/**
 * The base version of each path, as bytes, or null when the path is absent at base. Read through
 * a temporary index and `checkout-index` into a scratch dir (the real index is never touched).
 * @param {string} repoDir @param {string} base @param {string[]} files
 * @returns {Promise<Map<string, Buffer | null>>}
 */
async function readBaseFiles(repoDir, base, files) {
  const resolved = await revParse(`${base}^{commit}`, repoDir);
  if (resolved.result !== 'ok') throw new StateError('bad-ref', 'red-green: the base is not a commit in this repository');
  const sha = resolved.stdout.trim();
  const listed = new Set((await git(['ls-tree', '-r', '-z', '--full-tree', '--name-only', sha], repoDir)).split('\0').filter(Boolean));
  const present = files.filter((f) => listed.has(f));
  /** @type {Map<string, Buffer | null>} */
  const out = new Map(files.map((f) => [f, null]));
  if (present.length === 0) return out;
  const scratch = await mkdtemp(path.join(currentRunRoot(), 'red-green-'));
  try {
    const env = { GIT_INDEX_FILE: path.join(scratch, 'index') };
    const into = path.join(scratch, 'tree');
    await git(['read-tree', sha], repoDir, { env });
    await git(['checkout-index', '-f', '-z', '--stdin', `--prefix=${into}${path.sep}`], repoDir, { env, input: `${present.join('\0')}\0` });
    for (const rel of present) out.set(rel, await readFile(path.join(into, rel)));
    return out;
  } finally {
    await rm(scratch, { recursive: true, force: true });
  }
}

/**
 * Assertion deletion rules — a FAIL-CLOSED, line-based heuristic (no parser dependency). A line is
 * rewritten only when it is provably one whole assertion statement: it starts and ends in plain
 * code (not inside a block comment, template literal, multi-line string or heredoc), touches no
 * block comment, template literal or heredoc, holds exactly one statement (a call chain with
 * balanced parentheses, one terminating `;` — or, in JS, a closing `)` that the next line cannot
 * continue), and may end with a `//` (or PHP `#`) line comment. It must also sit at STATEMENT
 * POSITION in the enclosing context, tracked across lines by the lexer's bracket stack:
 *   (a) the innermost open bracket at the line's start is a `{` block (or none), and the code
 *       token before the line is `;`, `{`, `}` or the start of the file — never an arrow body
 *       (`() =>` + newline), an array element or a call argument;
 *   (b) the statement closes on that line, at the same bracket depth it started;
 *   (c) the next code token starts a new statement (no leading `)`, `]`, `,`, `.`, `?`, operator).
 * Otherwise the forced failure would land in an expression (`assert.fail(…);` followed by `)`),
 * a syntax error that yields RED_INVALID instead of RED. Any other line that mentions an
 * assertion is refused, so the tree is never touched on a guess. Per language:
 *   `statement` — the whole-statement prefix, matched on the masked line (strings/comments blanked);
 *   `token`     — an assertion mention anywhere in masked code; a token outside an accepted
 *                 statement (and outside an import/`use`/`require` line) refuses the file;
 *   `replace`   — the forced failure written in place of the statement, same library.
 */
const DELETION_RULES = Object.freeze([
  {
    lang: /** @type {const} */ ('js'),
    files: /\.(?:mjs|cjs|js|mts|cts|ts|jsx|tsx)$/,
    statement: /^(\s*)(?:(\w+)\.)?(assert|expect)\b(?:\.\w+)?\s*\(/,
    token: /\bassert\b|\bexpect\s*\(/,
    exempt: /^\s*(?:import\b|export\b.*\bfrom\b)|\brequire\s*\(/,
    /** @param {RegExpExecArray} m */
    replace: (m) => (m[3] === 'expect' ? (m[2] ? null : `${m[1]}expect('${DELETED_MARKER}').toBe('');`) : `${m[1]}${m[2] ? `${m[2]}.` : ''}assert.fail('${DELETED_MARKER}');`),
  },
  {
    lang: /** @type {const} */ ('php'),
    files: /\.php$/,
    statement: /^(\s*)(?:(\$this)->assert\w*|(expect))\s*\(/,
    token: /\$this->assert|\bexpect\s*\(|\b(?:self|static|Assert)::assert/,
    exempt: /^\s*(?:use|namespace)\b/,
    /** @param {RegExpExecArray} m */
    replace: (m) => `${m[1]}$this->fail('${DELETED_MARKER}');`,
  },
]);

/** The code token that may precede a statement: `;`, `{`, `}` or the start of the file. */
const STATEMENT_BEFORE = new Set(['', ';', '{', '}']);
/** A next code token that continues the expression instead of starting a new statement. */
const CONTINUES_EXPRESSION = /^(?:[()[\]`.,?:+\-*/%<>=&|^~!]|(?:in|instanceof)\b)/;
/** @type {Record<string, string>} closing bracket → its opener */
const CLOSER_OF = { ')': '(', ']': '[', '}': '{' };

/**
 * @typedef {{masked: string, start: string, end: string, complex: boolean, openAtStart: string, openAtEnd: string, prev: string}} LexedLine
 *   `masked`: the line with string contents blanked and comments removed (same length or shorter);
 *   `start`/`end`: the lexer state at the line's start/end (`code` = plain code);
 *   `complex`: the line touched a block comment, a template literal or a heredoc;
 *   `openAtStart`/`openAtEnd`: the stack of open brackets (`(`, `[`, `{`, innermost last) at the
 *   line's start/end — tracked across lines, so a line knows its enclosing context;
 *   `prev`: the last code character before this line (`''` at the start of the file).
 */

/**
 * A small lexer: per line, the code with string literal contents blanked and comments dropped,
 * plus the bracket context each line starts and ends in.
 * @param {string[]} lines @param {'js' | 'php'} lang
 * @returns {{lexed: LexedLine[], end: string, open: string, mismatch: number | null}}
 *   `open`: brackets still open at the end of the file; `mismatch`: the first line (0-based) that
 *   closed a bracket of the wrong kind or closed one that was never opened.
 */
function lex(lines, lang) {
  /** @type {string} */
  let state = 'code'; // code | block | template | ' | " | heredoc
  let heredocId = '';
  /** @type {string[]} */
  const open = [];
  /** @type {number | null} */
  let mismatch = null;
  let prev = '';
  /** @type {LexedLine[]} */
  const lexed = [];
  for (const [index, line] of lines.entries()) {
    const start = state;
    const openAtStart = open.join('');
    let masked = '';
    let complex = state === 'block' || state === 'template' || state === 'heredoc';
    if (state === 'heredoc') {
      const closes = new RegExp(`^\\s*${heredocId}\\b`).test(line);
      if (closes) {
        state = 'code';
        masked = line.replace(new RegExp(`^\\s*${heredocId}`), '');
      }
      lexed.push({ masked: closes ? masked : '', start, end: state, complex: true, openAtStart, openAtEnd: openAtStart, prev });
      continue;
    }
    for (let i = 0; i < line.length; i += 1) {
      const ch = line[i];
      const next = line[i + 1];
      if (state === 'block') {
        if (ch === '*' && next === '/') {
          state = 'code';
          i += 1;
        }
        continue;
      }
      if (state === 'template' || state === "'" || state === '"') {
        const close = state === 'template' ? '`' : state;
        if (ch === '\\') i += 1;
        else if (ch === close) {
          masked += ch;
          state = 'code';
        } else masked += ' ';
        continue;
      }
      // plain code
      if (ch === '/' && next === '*') {
        state = 'block';
        complex = true;
        i += 1;
      } else if ((ch === '/' && next === '/') || (lang === 'php' && ch === '#' && next !== '[')) {
        break; // a line comment: the rest of the line is dropped
      } else if (lang === 'js' && ch === '`') {
        state = 'template';
        complex = true;
        masked += ch;
      } else if (ch === "'" || ch === '"') {
        state = ch;
        masked += ch;
      } else if (lang === 'php' && ch === '<' && line.startsWith('<<<', i)) {
        const m = /^<<<\s*(['"]?)([A-Za-z_]\w*)\1/.exec(line.slice(i));
        if (m) {
          state = 'heredoc';
          heredocId = m[2];
          complex = true;
          break;
        }
        masked += ch;
      } else {
        if (ch === '(' || ch === '[' || ch === '{') open.push(ch);
        else if (ch in CLOSER_OF && open.pop() !== CLOSER_OF[ch] && mismatch === null) mismatch = index;
        masked += ch;
      }
    }
    lexed.push({ masked, start, end: state, complex, openAtStart, openAtEnd: open.join(''), prev });
    const code = masked.trimEnd();
    if (code.length > 0) prev = code[code.length - 1];
  }
  return { lexed, end: state, open: open.join(''), mismatch };
}

/**
 * Is the masked code (comment already dropped) exactly one call-chain statement starting at the
 * first `(`? Parentheses balance, depth-0 text is only a member chain (`.x(`, `->x(`), and the
 * only `;` is the last character.
 * @param {string} code @param {'js' | 'php'} lang
 * @returns {'semicolon' | 'bare' | null} how it ends, or null (not one statement)
 */
function oneStatement(code, lang) {
  const text = code.trimEnd();
  let depth = 0;
  for (let i = text.indexOf('('); i < text.length; i += 1) {
    const ch = text[i];
    if (ch === '(') depth += 1;
    else if (ch === ')') {
      depth -= 1;
      if (depth < 0) return null;
    } else if (depth === 0) {
      if (ch === ';') return i === text.length - 1 ? 'semicolon' : null;
      if (!/[\w\s.$>-]/.test(ch)) return null;
      if (ch === '-' && text[i + 1] !== '>') return null;
      if (ch === '>' && text[i - 1] !== '-') return null;
    }
  }
  if (depth !== 0) return null;
  return lang === 'js' && text.endsWith(')') ? 'bare' : null;
}

/**
 * The test file with every assertion statement deleted and replaced by a forced failure. Fail
 * closed: any assertion mention that is not provably one whole single-line statement refuses the
 * file before the tree is touched.
 * @param {string} file - repo-relative, picks the language rule
 * @param {Buffer} bytes
 * @returns {{bytes: Buffer, deleted: number}}
 * @throws {StateError} `unsupported-test`, `unparseable-test`, `unclassified-assertion`, `no-assertions`
 */
export function deleteAssertions(file, bytes) {
  const rule = DELETION_RULES.find((r) => r.files.test(file));
  if (!rule) throw new StateError('unsupported-test', `assertion deletion: no rule for ${path.posix.extname(file) || 'this file type'}`);
  const lines = bytes.toString('utf8').split('\n');
  const { lexed, end, open, mismatch } = lex(lines, rule.lang);
  if (end !== 'code' || open.length > 0) throw new StateError('unparseable-test', `assertion deletion: ${file} ends inside a comment, string, heredoc or an open bracket`);
  if (mismatch !== null) throw new StateError('unparseable-test', `assertion deletion: ${file} closes a bracket it did not open at line ${mismatch + 1}`);
  const refuse = (/** @type {number} */ i) => {
    throw new StateError('unclassified-assertion', `assertion deletion: the assertion at line ${i + 1} is not one whole single-line statement at statement position`);
  };
  let deleted = 0;
  const out = lines.map((line, i) => {
    const { masked, start, end: lineEnd, complex, openAtStart, openAtEnd, prev } = lexed[i];
    if (start !== 'code') return line; // inside a comment/string/heredoc: not code
    const m = rule.statement.exec(masked);
    if (!m) {
      if (rule.token.test(masked) && !rule.exempt.test(masked)) refuse(i);
      return line;
    }
    if (complex || lineEnd !== 'code') refuse(i);
    // (a) statement position: innermost open bracket is a `{` block (or none), after `;` `{` `}` or the file start
    if ((openAtStart.length > 0 && !openAtStart.endsWith('{')) || !STATEMENT_BEFORE.has(prev)) refuse(i);
    // (b) exactly one statement, closed on this line at the depth it started
    if (oneStatement(masked, rule.lang) === null || openAtEnd !== openAtStart) refuse(i);
    // (c) the next code token starts a new statement (a bare `)` ending is an ASI hazard otherwise)
    const next = lexed.slice(i + 1).find((l) => l.masked.trim().length > 0);
    if (next && CONTINUES_EXPRESSION.test(next.masked.trim())) refuse(i);
    const replacement = rule.replace(m);
    if (replacement === null) refuse(i);
    deleted += 1;
    return replacement;
  });
  if (deleted === 0) throw new StateError('no-assertions', `assertion deletion: ${file} has no single-line assertion statement`);
  return { bytes: Buffer.from(out.join('\n'), 'utf8'), deleted };
}

/** @returns {NodeJS.ProcessEnv} the test child's env: a node:test parent's worker marker never leaks into it */
function testEnv() {
  const { NODE_TEST_CONTEXT, ...env } = process.env;
  return env;
}

/**
 * @typedef {object} RedGreenOptions
 * @property {string} workDir - absolute: the export dir, or the workspace under the proof lock
 * @property {string} [repoDir] - the git repository the base is read from (default `workDir`)
 * @property {string} base - the block's base sha
 * @property {string[]} argv - the test command, already filtered to this test
 * @property {{file: string, case?: string, characterization?: boolean}} test
 * @property {string[]} [sources] - the block's non-test changed files (repo-relative)
 * @property {'export' | 'lock'} isolation
 * @property {number} [timeoutMs]
 * @property {(line: string) => void} [print]
 * @property {{runId: string, blockId: string, writeRow: import('../state/run.mjs').WriteRow}} [record]
 */

/**
 * Run one red→green proof. See the module comment for the steps.
 * @param {RedGreenOptions} opts
 * @returns {Promise<{label: string, mechanism: 'revert' | 'assertion-deletion', red: import('./red-parse.mjs').RedParse, green: 'GREEN' | 'NOT_GREEN' | null, proven: boolean}>}
 * @throws {StateError} `journal-present`, `bad-path`, `bad-isolation`, `bad-ref`, and the assertion-deletion refusals
 */
export async function runRedGreen(opts) {
  const { workDir, base, argv, test, sources = [], isolation, timeoutMs = DEFAULT_TEST_TIMEOUT_MS, print = () => {}, record } = opts;
  if (typeof workDir !== 'string' || !path.isAbsolute(workDir)) throw new StateError('bad-path', 'red-green: workDir must be an absolute path');
  const repoDir = opts.repoDir ?? workDir;
  if (!path.isAbsolute(repoDir)) throw new StateError('bad-path', 'red-green: repoDir must be an absolute path');
  if (!ISOLATIONS.includes(/** @type {any} */ (isolation))) throw new StateError('bad-isolation', `red-green: isolation must be one of ${ISOLATIONS.join(', ')}`);
  if (!Array.isArray(argv) || argv.length === 0 || !argv.every((a) => typeof a === 'string')) throw new StateError('usage', 'red-green: argv must be a non-empty array of strings');
  assertRelPath(test?.file, 'test.file');
  for (const s of sources) assertRelPath(s, 'sources');
  if (sources.includes(test.file)) throw new StateError('bad-path', 'red-green: the test file cannot also be a source file');
  if (typeof base !== 'string' || base.length === 0 || base.startsWith('-')) throw new StateError('bad-ref', 'red-green: base must be a commit');

  const label = test.case ? `${test.file}::${test.case}` : test.file;
  const mechanism = test.characterization === true || sources.length === 0 ? 'assertion-deletion' : 'revert';
  if ((await readJournal(workDir)) !== null) {
    throw new StateError('journal-present', `a red→green journal exists in ${workDir} — an earlier run was interrupted; run code-forge proof restore first`);
  }

  // Compute the red state BEFORE touching the tree, so a refusal changes nothing.
  /** @type {Map<string, Buffer | null>} */
  let redState;
  if (mechanism === 'revert') {
    redState = await readBaseFiles(repoDir, base, [...new Set(sources)].sort());
  } else {
    const abs = path.join(workDir, test.file);
    if (!(await lstatOrNull(abs))?.isFile()) throw new StateError('bad-path', `red-green: ${test.file} is not a regular file`);
    redState = new Map([[test.file, deleteAssertions(test.file, await readFile(abs)).bytes]]);
  }

  await writeJournal(workDir, [...redState.keys()], { mechanism, test: label, ...(record ? { block: record.blockId } : {}) });
  /** @type {import('./red-parse.mjs').RedParse} */
  let red;
  try {
    for (const [rel, bytes] of redState) {
      const abs = path.join(workDir, rel);
      await rm(abs, { force: true });
      if (bytes !== null) {
        await mkdir(path.dirname(abs), { recursive: true });
        await writeFile(abs, bytes);
      }
    }
    red = parseRed(await exec(argv, { cwd: workDir, env: testEnv(), timeoutMs }));
  } finally {
    // Any ordinary error in the red step still restores (signals keep the journal: module comment).
    await restoreJournal({ workDir });
  }
  print(red.verdict === 'RED_INVALID' ? `RED_INVALID ${label} (${red.red_kind})` : `${red.verdict} ${label}`);

  /** @type {'GREEN' | 'NOT_GREEN' | null} */
  let green = null;
  if (red.verdict === 'RED') {
    const greenRun = await exec(argv, { cwd: workDir, env: testEnv(), timeoutMs });
    green = greenRun.result === 'ok' ? 'GREEN' : 'NOT_GREEN';
    print(`${green} ${label}`);
  }
  const proven = red.verdict === 'RED' && green === 'GREEN';
  if (record) {
    await recordProof({
      runId: record.runId,
      writeRow: record.writeRow,
      row: { block: record.blockId, isolation, step: 'red-green', test: label, mechanism, red_kind: red.red_kind, red: red.verdict, green, proven, failed: red.failed },
    });
  }
  return { label, mechanism, red, green, proven };
}
