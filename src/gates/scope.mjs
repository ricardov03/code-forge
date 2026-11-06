/**
 * The block gate's file set (plan §4.1, O1) and the shared-tree scope check (§4.7, O2).
 *
 * File set = `git diff --name-only -z <base>` (tracked changes) ∪ untracked files from
 * `git status --porcelain -z --untracked-files=all`. **`-z` is load-bearing, not cosmetic:**
 * without it, git wraps any path containing whitespace, quotes, control characters or (under the
 * default `core.quotePath`) non-ASCII bytes in C-style quotes (`"a b.txt"`, `"caf\303\251.md"`),
 * so a newline-split reader gets the quoted literal instead of the real path and every later
 * `ownsFile`/`scopeGate` lookup silently fails to match it — a real file becomes a phantom orphan
 * (or a wrongly-missed out-of-scope file). `-z` disables quoting entirely and NUL-terminates each
 * entry instead, so paths are read as their exact raw bytes; `splitNul` below never `.trim()`s an
 * entry, because a real leading/trailing space in a filename is data, not padding.
 *
 * The scope MATHS itself (which block owns which file, which files are orphans) already lives in
 * B8's `state/registry.mjs` — this module only supplies the file set that maths runs on (the
 * registry's own doc: "the caller supplies the file set … B5 computes it"), and re-exports the
 * registry functions so a consumer needs one import.
 *
 * Every `git` call here strips inherited `GIT_*` env vars (a hook can export `GIT_DIR`/
 * `GIT_WORK_TREE`, silently redirecting the read to an unrelated repo) and disables system/global
 * git config, the same guard every other git-calling module in this package uses. `base` is
 * rejected outright when it starts with `-` (`assertRefNoLeadingDash`) — the SAME argv-injection
 * guard `util/git.mjs`'s builders apply to their own ref arguments (`--output=/some/path` would
 * otherwise be read by git as an OPTION, not a revision) — reimplemented locally here because the
 * `-z` forms below are not among `util/git.mjs`'s existing read-only builders.
 */

import os from 'node:os';
import { createHash } from 'node:crypto';
import { lstat, readFile } from 'node:fs/promises';
import path from 'node:path';
import { exec } from '../util/exec.mjs';
import { diffNoIndexArgv } from '../util/git.mjs';

export { computeScopes, ownsFile, scopeGate } from '../state/registry.mjs';

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
 * Reject a git ref argument that starts with `-` before it ever reaches argv — the same guard
 * `util/git.mjs`'s `assertNoLeadingDash` applies, reimplemented here because the `-z` builders
 * below are local (not one of `util/git.mjs`'s existing read-only builders).
 * @param {unknown} value @param {string} name @returns {string}
 * @throws {TypeError}
 */
function assertRefNoLeadingDash(value, name) {
  if (typeof value !== 'string' || value.length === 0 || value.startsWith('-')) {
    throw new TypeError(`${name} must be a non-empty string not starting with "-" (got ${JSON.stringify(value)}) — option injection`);
  }
  return value;
}

/**
 * `git diff --name-only -z <base>` — NUL-terminated, unquoted paths (see the module doc).
 * @param {string} base @returns {string[]}
 */
function diffNameOnlyZArgv(base) {
  return ['git', 'diff', '--name-only', '-z', assertRefNoLeadingDash(base, 'base')];
}

/** `git status --porcelain -z --untracked-files=all` — NUL-terminated, unquoted entries. */
function statusZArgv() {
  return ['git', 'status', '--porcelain', '-z', '--untracked-files=all'];
}

/**
 * Split a NUL-terminated `-z` git output into its entries, dropping the trailing empty entry the
 * final terminator produces. Never trims — a real leading/trailing space in a path is data.
 * @param {string} text @returns {string[]}
 */
function splitNul(text) {
  return text.split('\0').filter((entry) => entry.length > 0);
}

/**
 * @typedef {object} FileSet
 * @property {string[]} tracked - changed tracked files (`git diff --name-only -z <base>`).
 * @property {string[]} untracked - untracked files (`git status -z`, `??` entries).
 * @property {string[]} all - the sorted, de-duplicated union — the file set of §4.1.
 */

/**
 * @param {string} base @param {string} cwd
 * @returns {Promise<string[]>}
 * @throws {TypeError} when `base` starts with `-` (argv-injection guard) or isn't a non-empty string.
 */
export async function trackedChangedFiles(base, cwd) {
  const res = await exec(diffNameOnlyZArgv(base), { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  if (res.result !== 'ok') {
    throw new Error(`trackedChangedFiles: git diff --name-only -z ${base} failed (exit ${res.code}): ${res.stderr}`);
  }
  return splitNul(res.stdout);
}

/**
 * @param {string} cwd
 * @returns {Promise<string[]>} untracked paths only (git status `??` entries, exact bytes) —
 *   renames, modifications and deletions of tracked files are NOT included; those come from
 *   `trackedChangedFiles`.
 */
export async function untrackedFiles(cwd) {
  const res = await exec(statusZArgv(), { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  if (res.result !== 'ok') {
    throw new Error(`untrackedFiles: git status failed (exit ${res.code}): ${res.stderr}`);
  }
  // A rename/copy entry's `-z` record is TWO NUL-terminated fields (new path, then the ORIGINAL
  // path with no status prefix at all) — splitNul yields that orig-path as its own bare entry,
  // which the `?? ` prefix filter below naturally excludes since it never starts with `?? `.
  return splitNul(res.stdout)
    .filter((entry) => entry.startsWith('?? '))
    .map((entry) => entry.slice(3));
}

/**
 * The complete, UNFILTERED file set of §4.1: tracked changes since `base` union untracked files.
 * A caller that needs it filtered to one block's `owned_files` does so with `ownsFile` (re-exported
 * above) — the scope check specifically needs the unfiltered set (§4.1: "unfiltered for the scope
 * check").
 * @param {{cwd: string, base: string}} opts
 * @returns {Promise<FileSet>}
 */
export async function computeFileSet({ cwd, base }) {
  const [tracked, untracked] = await Promise.all([trackedChangedFiles(base, cwd), untrackedFiles(cwd)]);
  return { tracked, untracked, all: [...new Set([...tracked, ...untracked])].sort() };
}

/** Resolve the block against the current base branch, never the pre-rebase fork point.
 * A missing/unresolvable base is deliberately not evidence for reuse.
 * @param {string} cwd @param {{base_branch?: string, base_sha?: string, worktree?: {base_sha?: string}}} block
 * @returns {Promise<string | null>}
 */
export async function blockMergeBase(cwd, block) {
  // In a shared checkout the current branch is also base_branch; it is not a separate
  // upstream and cannot distinguish another block's commits. Retain the hash baseline there.
  if (!block.base_branch) return null;
  const branch = await exec(['git', 'symbolic-ref', '--quiet', '--short', 'HEAD'], { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  if (branch.result === 'ok' && branch.stdout.trim() === block.base_branch) return null;
  const ref = `refs/heads/${block.base_branch}`;
  if (!ref || ref.startsWith('-')) return null;
  const result = await exec(['git', 'merge-base', 'HEAD', ref], { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
  return result.result === 'ok' && /^[a-f0-9]{40,64}$/.test(result.stdout.trim()) ? result.stdout.trim() : null;
}

/** Git bytes at the effective base, including deleted block paths.
 * @param {string} cwd @param {string} base @param {string[]} files
 * @returns {Promise<Record<string, string> | null>}
 */
export async function baseFileHashes(cwd, base, files) {
  const hashes = {};
  for (const file of files) {
    const result = await exec(['git', 'show', `${base}:${file}`], { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
    if (result.result === 'ok') hashes[file] = createHash('sha256').update(result.stdout).digest('hex');
    else {
      const exists = await exec(['git', 'ls-tree', '-z', base, '--', file], { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
      if (exists.result !== 'ok' || exists.stdout) return null;
    }
  }
  return hashes;
}

/** Hash the complete test input tree, not just owned files. Only conventional documentation
 * outside known graph edges and release version fields are ignored. Unknown files, modes,
 * config, lock dependencies and untracked inputs all invalidate reuse. Symlinks fail closed.
 * @param {string} cwd @param {Record<string, string[]>} [dependents]
 * @returns {Promise<string | null>}
 */
export async function testTreeStamp(cwd, dependents = {}) {
  try {
    const listed = await exec(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], { cwd, env: gitEnv(), timeoutMs: GIT_TIMEOUT_MS });
    if (listed.result !== 'ok') return null;
    const hash = createHash('sha256');
    for (const file of [...new Set(splitNul(listed.stdout))].sort()) {
      if (!dependents[file]?.length && (/^(?:docs\/|CHANGELOG(?:\.|$))/.test(file) || /^(?:README|LICENSE)(?:\.|$)/.test(file))) continue;
      const absolute = path.resolve(cwd, file);
      const stat = await lstat(absolute).catch((error) => {
        if (error.code === 'ENOENT' || error.code === 'ENOTDIR') return null;
        throw error;
      });
      if (!stat) { hash.update(JSON.stringify([file, 'deleted'])); continue; }
      if (!stat.isFile()) return null;
      let bytes = await readFile(absolute);
      if (['package.json', 'npm-shrinkwrap.json', 'package-lock.json', '.claude-plugin/plugin.json'].includes(file)) {
        const value = JSON.parse(bytes.toString('utf8'));
        delete value.version;
        if (value.packages?.['']) delete value.packages[''].version;
        bytes = Buffer.from(JSON.stringify(value));
      }
      hash.update(JSON.stringify([file, stat.mode & 0o111, bytes.length]));
      hash.update(bytes);
    }
    return hash.digest('hex');
  } catch { return null; }
}

/**
 * `git diff --no-index /dev/null <path>` for one NEW/untracked file, plus its `+` line count.
 * The header (`--- a/…`, `+++ b/<path>`) is skipped by POSITION — everything up to and including
 * the first `@@ …@@` hunk marker — not by matching a `+++` PREFIX: a real content line that itself
 * starts with `++` (`++i;`, a markdown/diff fixture) renders as `+++i;` in the diff body and would
 * otherwise be misread as a second header line and dropped, undercounting. Exit code 1
 * (differences found) is the expected, non-error outcome.
 * @param {string} cwd @param {string} relPath
 * @returns {Promise<{diff: string, plusCount: number}>}
 */
export async function diffNoIndexWithCount(cwd, relPath) {
  const res = await exec(diffNoIndexArgv(relPath), { cwd, env: gitEnv(), okExitCodes: [0, 1], timeoutMs: GIT_TIMEOUT_MS });
  if (res.result !== 'ok') {
    throw new Error(`diffNoIndexWithCount: git diff --no-index failed for ${relPath} (exit ${res.code}): ${res.stderr}`);
  }
  const lines = res.stdout.split('\n');
  const hunkStart = lines.findIndex((line) => line.startsWith('@@'));
  const body = hunkStart < 0 ? [] : lines.slice(hunkStart + 1);
  const plusCount = body.filter((line) => line.startsWith('+')).length;
  return { diff: res.stdout, plusCount };
}
