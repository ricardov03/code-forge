/**
 * B60 (issue #13 item 3): run only the block's related tests, and give a gate its own environment.
 * Scope and argv rules are pure; B99's fileBaseline reads bounded hashes from the workspace.
 * A gate argv item that is EXACTLY {@link TEST_FILES_TOKEN}
 * (one whole item, never a substring, never a shell string) stands for the block's test files:
 *
 *  - the block's `test_files` (`block open --tests`), else its owned entries that are concrete
 *    files (no glob, no trailing `/`) and look like tests ({@link isTestPath});
 *  - when the block owns more FILES than `gates.full_suite_threshold_files` (default 10; globs and
 *    directories count the tracked and new (not ignored) files they cover — the caller counts them, gates/owned-count.mjs),
 *    or no test file is known (also: a gate run with no block), the item is DROPPED and the gate
 *    runs the full suite.
 *
 * {@link testScope} decides which, once per gate run; {@link expandArgv} applies it to one argv.
 * A gate without the token is never touched.
 *
 * A gate given as `{argv, env}` runs with `env` added to the inherited environment. Gate env is
 * NOT a secret store: code-forge prints and records only the variable NAMES ({@link envKeys},
 * {@link printableGates}), never a value. The child's own output is masked only for values of
 * {@link MASK_MIN_LENGTH} or more characters ({@link maskableEnvValues}) — a short value such as
 * `CI=1` or `MODE=test` would otherwise mask unrelated text (`npm test`).
 */

import { createHash } from 'node:crypto';
import { access, lstat, readFile, realpath } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { isExactEntry, ownsFile } from '../state/registry.mjs';
import { pairsFor } from '../review/paired.mjs';
import { exec } from '../util/exec.mjs';
import { TEST_FILES_TOKEN, CHANGED_FILES_TOKEN } from '../util/gate-token.mjs';

export { TEST_FILES_TOKEN };

/** `gates.full_suite_threshold_files` when the config leaves it out (the schema default). */
export const DEFAULT_FULL_SUITE_THRESHOLD = 10;

/** The shortest env value masked in a gate's own output. */
export const MASK_MIN_LENGTH = 8;

/**
 * The threshold a configured value gives: the value itself when it is an integer >= 0, else the
 * default.
 * @param {unknown} value - `gates.full_suite_threshold_files` as the config holds it.
 * @returns {number}
 */
export function thresholdOf(value) {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : DEFAULT_FULL_SUITE_THRESHOLD;
}

/** Directory names that hold tests. */
const TEST_DIRS = new Set(['test', 'tests', '__tests__', 'spec']);

/**
 * The shared test-path predicate: a directory segment named `test`, `tests`, `__tests__` or
 * `spec`, or a file name ending `.test.<ext>` / `.spec.<ext>` (`a.test.mjs`, `a.spec.ts`),
 * `Test.php` (`UserTest.php`), `_test.go`, or a Python `test_*.py`.
 * @param {string} p - a repo-relative POSIX path (an owned entry; a glob is judged by its text).
 * @returns {boolean}
 */
export function isTestPath(p) {
  if (typeof p !== 'string' || p.length === 0) return false;
  const segments = p.split('/');
  const name = segments[segments.length - 1];
  if (segments.slice(0, -1).some((s) => TEST_DIRS.has(s))) return true;
  return /\.(test|spec)\.[^/]+$/.test(name) || name.endsWith('Test.php') || name.endsWith('_test.go') || /^test_.*\.py$/.test(name);
}

/**
 * An owned entry that names one file: no glob character (`*`, `?`, `{`) and no trailing `/`.
 * @param {string} p @returns {boolean}
 */
const isConcreteFile = (p) => typeof p === 'string' && p.length > 0 && isExactEntry(p) && !p.endsWith('/');

/**
 * @typedef {{scope: 'related', files: string[]} | {scope: 'full', reason: 'threshold' | 'no_tests'}} TestScope
 * @typedef {{scope: 'changed', files: string[], changedFiles: string[], reason?: 'no_baseline'|'unpaired'|'threshold'|'no_changes'}} ChangedScope
 */

/** A gate run with no block: no test file is known. */
export const NO_BLOCK_SCOPE = Object.freeze(/** @type {TestScope} */ ({ scope: 'full', reason: 'no_tests' }));

/**
 * Which tests a `{test_files}` gate runs for one block.
 * @param {{testFiles?: ReadonlyArray<string> | null, owned: ReadonlyArray<string>, ownedFiles?: number, threshold?: number}} opts
 *   `testFiles` - the block's `test_files` (`block open --tests`), or none; `owned` - its owned
 *   entries; `ownedFiles` - how many files those entries cover (`ownedFileCount`,
 *   gates/owned-count.mjs; when not given, one per entry); `threshold` -
 *   `gates.full_suite_threshold_files`, compared with `ownedFiles`.
 * @returns {TestScope}
 */
export function testScope({ testFiles, owned, ownedFiles = owned.length, threshold = DEFAULT_FULL_SUITE_THRESHOLD }) {
  if (ownedFiles > threshold) return { scope: 'full', reason: 'threshold' };
  const files = testFiles && testFiles.length > 0 ? [...testFiles] : owned.filter((p) => isConcreteFile(p) && isTestPath(p));
  if (files.length === 0) return { scope: 'full', reason: 'no_tests' };
  return { scope: 'related', files };
}

/**
 * The argv of one gate command (an argv array or `{argv, ...}`), or null.
 * @param {unknown} command @returns {ReadonlyArray<unknown> | null}
 */
function argvOf(command) {
  if (Array.isArray(command)) return command;
  if (command && typeof command === 'object' && Array.isArray(/** @type {{argv?: unknown}} */ (command).argv)) return /** @type {{argv: unknown[]}} */ (command).argv;
  return null;
}

/**
 * Does any gate in `gates` have an argv item that is exactly {@link TEST_FILES_TOKEN} (a whole
 * item — the same test {@link expandArgv} applies)? Only then is a block's test scope worked out;
 * a run of plain gates never reads it.
 * @param {Record<string, unknown> | null | undefined} gates @returns {boolean}
 */
export function hasTestFilesItem(gates) {
  return Object.values(gates ?? {}).some((command) => argvOf(command)?.includes(TEST_FILES_TOKEN) === true);
}

/** @param {Record<string, unknown> | null | undefined} gates @returns {boolean} */
export function hasScopeItem(gates) {
  return hasTestFilesItem(gates) || Object.values(gates ?? {}).some((command) => argvOf(command)?.includes(CHANGED_FILES_TOKEN));
}

/** A safe repo-relative path that cannot turn into an argv option. @param {string} file */
function safeFile(file) {
  return file.length > 0 && !file.startsWith('-') && !path.isAbsolute(file) && !/[\\\x00-\x1f]/.test(file) && !file.split('/').some((s) => s === '' || s === '.' || s === '..');
}

/**
 * Bounded content baseline. Missing listed files are omitted from hashes and candidates; an
 * owned entry with no current file has no hash. More than 200 owned files, a symlink, a
 * non-regular owned file, a path outside the root or another read error means no record;
 * callers retain the old scope. Git sees only argv and an isolated git environment.
 * @param {string} cwd @param {string[]} owned @param {Record<string, string>} [kinds]
 * @returns {Promise<{files: Record<string, string>, candidates: string[]} | null>}
 */
export async function fileBaseline(cwd, owned, kinds) {
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('GIT_')));
  env.GIT_CONFIG_NOSYSTEM = '1';
  env.GIT_CONFIG_GLOBAL = os.devNull;
  const listed = await exec(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], { cwd, env, timeoutMs: 30000 });
  if (listed.result !== 'ok') return null;
  const listedFiles = [...new Set(listed.stdout.split('\0').filter(Boolean))].sort();
  const candidates = [];
  const files = /** @type {Record<string, string>} */ (Object.create(null));
  try {
    const root = await realpath(cwd);
    for (const file of listedFiles) {
      const isOwned = ownsFile(owned, file, kinds);
      if (isOwned && !safeFile(file)) return null;
      const absolute = path.join(root, file);
      if (!isOwned) {
        const exists = await access(absolute).then(() => true).catch((error) => {
          if (error.code === 'ENOENT' || error.code === 'ENOTDIR') return false;
          throw error;
        });
        if (exists) candidates.push(file);
        continue;
      }
      const stat = await lstat(absolute).catch((error) => {
        if (error.code === 'ENOENT' || error.code === 'ENOTDIR') return null;
        throw error;
      });
      if (stat === null) continue;
      candidates.push(file);
      if (Object.keys(files).length >= 200 || !stat.isFile() || !(await realpath(absolute)).startsWith(root + path.sep)) return null;
      files[file] = createHash('sha256').update(await readFile(absolute)).digest('hex');
    }
  } catch { return null; }
  return { files, candidates };
}

/**
 * Compare the last green record. A source without a stem pair cannot narrow the test gate.
 * @param {{baseline: unknown, current: Awaited<ReturnType<typeof fileBaseline>>, fallback: TestScope, threshold?: number, owned?: string[], kinds?: Record<string, string>}} opts
 * @returns {ChangedScope}
 */
export function changedScope({ baseline, current, fallback, threshold = DEFAULT_FULL_SUITE_THRESHOLD, owned, kinds }) {
  const old = /** @type {Record<string, string> | null} */ (baseline);
  const candidates = new Set(current?.candidates);
  const fallbackFiles = fallback.scope === 'related' ? fallback.files.filter((file) => !current || candidates.has(file)) : [];
  if (!current || !old || typeof old !== 'object' || Array.isArray(old) || Object.keys(old).length > 200 ||
      Object.entries(old).some(([file, hash]) => !safeFile(file) || (owned && !ownsFile(owned, file, kinds)) || typeof hash !== 'string' || !/^[a-f0-9]{64}$/.test(hash))) {
    return { scope: 'changed', files: fallbackFiles, changedFiles: [], reason: 'no_baseline' };
  }
  const changes = [...new Set([...Object.keys(old), ...Object.keys(current.files)])].filter((file) => old[file] !== current.files[file]).sort();
  if (changes.length > threshold) return { scope: 'changed', files: [], changedFiles: [], reason: 'threshold' };
  const changedFiles = changes.filter((file) => candidates.has(file));
  // B97 also pairs Swift Tests names outside lowercase test directories.
  const isChangedTest = (/** @type {string} */ file) => isTestPath(file) || file.endsWith('Tests.swift');
  const tests = new Set(changedFiles.filter(isChangedTest));
  for (const file of changes.filter((f) => !isChangedTest(f))) {
    const pairs = pairsFor(file, current.candidates).filter(isChangedTest);
    if (pairs.length === 0) return { scope: 'changed', files: fallbackFiles, changedFiles, reason: 'unpaired' };
    for (const paired of pairs) tests.add(paired);
  }
  if (tests.size === 0) return { scope: 'changed', files: fallbackFiles, changedFiles, reason: 'no_changes' };
  return { scope: 'changed', files: [...tests].sort(), changedFiles };
}

/**
 * One gate argv with every item that is exactly {@link TEST_FILES_TOKEN} replaced by the related
 * test files, or dropped for a full run. Always a new array; `scoped` is false (and the items
 * unchanged) when the gate has no such item.
 * @param {ReadonlyArray<string>} argv @param {TestScope | ChangedScope} scope
 * @returns {{argv: string[], scoped: boolean}}
 * @throws {TypeError} a test path that starts with `-` (it would be read as an option; `block
 *   open --tests` refuses one too).
 */
export function expandArgv(argv, scope) {
  if (!argv.includes(TEST_FILES_TOKEN) && !argv.includes(CHANGED_FILES_TOKEN)) return { argv: [...argv], scoped: false };
  const files = scope.scope === 'full' ? [] : scope.files;
  const changed = scope.scope === 'changed' ? scope.changedFiles : [];
  if (changed.some((file) => !safeFile(file))) throw new TypeError('expandArgv: unsafe changed path');
  const dashed = files.find((f) => f.startsWith('-'));
  if (dashed !== undefined) throw new TypeError(`expandArgv: test path ${JSON.stringify(dashed)} starts with "-"`);
  return { argv: argv.flatMap((item) => item === TEST_FILES_TOKEN ? files : item === CHANGED_FILES_TOKEN ? changed : [item]), scoped: true };
}

/**
 * The fields a scoped gate's result and ledger row carry: `scope`, plus `test_count` when related
 * or `reason` when full.
 * @param {TestScope | ChangedScope} scope
 * @returns {{scope: 'changed', test_count: number, reason?: 'no_baseline'|'unpaired'|'threshold'|'no_changes'} | {scope: 'related', test_count: number} | {scope: 'full', reason: 'threshold'|'no_tests'}}
 */
export function scopeFields(scope) {
  if (scope.scope === 'changed') return { scope: 'changed', test_count: scope.files.length, ...(scope.reason ? { reason: scope.reason } : {}) };
  return scope.scope === 'related' ? { scope: 'related', test_count: scope.files.length } : { scope: 'full', reason: scope.reason };
}

/**
 * A gate command's env as a plain `NAME: value` map, or null when it has none: an argv array, an
 * object without `env`, or an empty `env: {}` (treated as no env at all).
 * @param {unknown} command
 * @returns {Record<string, string> | null}
 */
export function envOf(command) {
  if (!command || typeof command !== 'object' || Array.isArray(command)) return null;
  const env = /** @type {{env?: unknown}} */ (command).env;
  if (!env || typeof env !== 'object' || Array.isArray(env) || Object.keys(env).length === 0) return null;
  return /** @type {Record<string, string>} */ (env);
}

/**
 * The sorted variable NAMES of a gate command's env, or null when it has none.
 * @param {unknown} command @returns {string[] | null}
 */
export function envKeys(command) {
  const env = envOf(command);
  return env ? Object.keys(env).sort() : null;
}

/**
 * Every non-empty string value of every gate's `env`, across every entry of `gates`.
 * @param {Record<string, unknown> | null | undefined} gates
 * @returns {string[]}
 */
export function envValues(gates) {
  /** @type {string[]} */
  const values = [];
  for (const command of Object.values(gates ?? {})) {
    for (const value of Object.values(envOf(command) ?? {})) if (typeof value === 'string' && value.length > 0) values.push(value);
  }
  return values;
}

/**
 * The env values a gate's own output is masked for: those of {@link MASK_MIN_LENGTH} or more
 * characters. Shorter ones are not masked (they would hide unrelated text).
 * @param {Record<string, unknown> | null | undefined} gates @returns {string[]}
 */
export function maskableEnvValues(gates) {
  return envValues(gates).filter((value) => value.length >= MASK_MIN_LENGTH);
}

/**
 * A copy of `gates` safe to print: each command given with `env` keeps its other fields and shows
 * `env_keys` (the names) in place of `env`; an empty env is left out. Nothing else changes.
 * @template {Record<string, any>} T
 * @param {T} gates @returns {T}
 */
export function printableGates(gates) {
  /** @type {Record<string, unknown>} */
  const out = {};
  for (const [key, command] of Object.entries(gates ?? {})) {
    if (!command || typeof command !== 'object' || Array.isArray(command) || !Object.hasOwn(command, 'env')) {
      out[key] = command;
      continue;
    }
    const { env: _env, ...rest } = /** @type {Record<string, unknown>} */ (command);
    const keys = envKeys(command);
    out[key] = keys ? { ...rest, env_keys: keys } : rest;
  }
  return /** @type {T} */ (out);
}
