/**
 * Every git call the package makes goes through here. The split is deliberate and load-bearing
 * for `test/util/git.test.mjs`: each helper has a pure `*Argv` builder (no process spawned) plus
 * a thin async wrapper that runs it through `./exec.mjs`. `commit` and `push` are the ONLY
 * writing verbs this module exposes (plan §10.2 acceptance 6) — everything else reads.
 *
 * `git rm` is treated as a write too (it deletes a tracked file, which includes deleting a test
 * file) even though the package never calls it: it is listed only so `./forbidden.mjs` and any
 * future helper here classify it the same way. Force-push, reset --hard, checkout --, restore,
 * clean, stash, and branch -D/--delete --force are simply not modeled as helpers here, because
 * the package has no legitimate reason to run them. This module does NOT run a forbidden-list
 * check before `exec`: what keeps `commit`/`push` inside the §8.4 rules is this module's own
 * argument validation — fixed argv shapes, a branch that must be a plain git branch name (no
 * leading `-` or `+`, no `:` or other refspec syntax — see `assertValidBranchName`), and typed
 * string checks on every value.
 *
 * `fetch` is classified READ, not WRITE, even though it touches the network and updates
 * remote-tracking refs and the object database: the write/read split here is specifically about
 * "can this alter the local branch history or working tree a coder's diff depends on", which is
 * what acceptance clause (6) exists to guard, and `fetch` cannot — it never moves a local branch
 * or touches the working tree. `test/util/git.test.mjs` asserts this classification explicitly
 * rather than leaving it implicit.
 *
 * Every positional ref/path argument that lands in argv WITHOUT a preceding `--` end-of-options
 * marker is validated to not start with `-` (`assertNoLeadingDash`) — a value the caller merely
 * expected to be a git ref (e.g. `base` in `diffNameOnlyArgv`) is exactly how `git diff
 * --upload-pack=evil` style option injection would otherwise reach git. `diffArgv`'s `path` is
 * the one exception: it already sits after a literal `--` in the built argv, so git treats it as
 * a pathspec no matter what it starts with.
 *
 * Every wrapper runs git with `process.env` minus the repository-context variables git itself
 * lists in `git rev-parse --local-env-vars` (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`,
 * `GIT_PREFIX`, `GIT_OBJECT_DIRECTORY`, …). Git hooks export them, so a code-forge call made from
 * inside a hook would otherwise act on the hook's repository instead of `cwd` (plan V8).
 * Everything else — `GIT_SSH_COMMAND`, `GIT_AUTHOR_*`, the user's global config — is kept.
 * `fetch`/`push` additionally set `GIT_TERMINAL_PROMPT=0`.
 */

import { exec } from './exec.mjs';

/** Git subcommands this module treats as read-only (see the `fetch` note above). */
export const READ_COMMANDS = Object.freeze([
  'status',
  'diff',
  'show',
  'rev-parse',
  'merge-base',
  'fetch',
  'archive',
  'log',
  'ls-files',
]);

/** Git subcommands this module treats as writes. `commit` and `push` are the only ones exposed as helpers. */
export const WRITE_COMMANDS = Object.freeze(['commit', 'push', 'rm']);

/** Network helpers (`fetch`, `push`) get a default timeout so a credential prompt can't hang forever. */
const DEFAULT_NETWORK_TIMEOUT_MS = 30000;

/** Output of `git rev-parse --local-env-vars` (git 2.5x): the variables that pick a repository. */
const LOCAL_REPO_ENV_VARS = Object.freeze([
  'GIT_ALTERNATE_OBJECT_DIRECTORIES',
  'GIT_CONFIG',
  'GIT_CONFIG_PARAMETERS',
  'GIT_CONFIG_COUNT',
  'GIT_OBJECT_DIRECTORY',
  'GIT_DIR',
  'GIT_WORK_TREE',
  'GIT_IMPLICIT_WORK_TREE',
  'GIT_GRAFT_FILE',
  'GIT_INDEX_FILE',
  'GIT_NO_REPLACE_OBJECTS',
  'GIT_REPLACE_REF_BASE',
  'GIT_PREFIX',
  'GIT_SHALLOW_FILE',
  'GIT_COMMON_DIR',
]);

/**
 * `process.env` without the repository-context variables, plus `extra`.
 * @param {Record<string, string>} [extra]
 * @returns {NodeJS.ProcessEnv}
 */
function gitEnv(extra = {}) {
  /** @type {NodeJS.ProcessEnv} */
  const env = { ...process.env };
  for (const name of LOCAL_REPO_ENV_VARS) {
    delete env[name];
  }
  return { ...env, ...extra };
}

/**
 * @param {string} subcommand
 * @param {string[]} args
 * @returns {string[]}
 */
function gitArgv(subcommand, args) {
  return ['git', subcommand, ...args];
}

/**
 * Reject a positional argument that isn't sitting after a `--` marker and starts with `-` — git
 * would otherwise parse it as an option instead of the ref/path/branch it's meant to be.
 * @param {unknown} value
 * @param {string} paramName
 * @returns {string}
 */
function assertNoLeadingDash(value, paramName) {
  if (typeof value !== 'string' || value.length === 0) {
    throw new TypeError(`git.mjs: ${paramName} must be a non-empty string`);
  }
  if (value.startsWith('-')) {
    throw new TypeError(`git.mjs: ${paramName} must not start with "-" (got ${JSON.stringify(value)}) — option injection`);
  }
  return value;
}

/**
 * @param {unknown} value
 * @param {string} paramName
 * @returns {string}
 */
function assertNonEmptyString(value, paramName) {
  if (typeof value !== 'string' || value.length === 0) {
    throw new TypeError(`git.mjs: ${paramName} must be a non-empty string`);
  }
  return value;
}

/**
 * @param {string[]} args
 * @param {string} paramName
 * @returns {string[]}
 */
function assertNoLeadingDashes(args, paramName) {
  for (const arg of args) {
    assertNoLeadingDash(arg, `an element of ${paramName}`);
  }
  return args;
}

// ── Read-only argv builders ─────────────────────────────────────────────────

/** @returns {string[]} */
export function statusArgv() {
  return gitArgv('status', ['--porcelain', '--untracked-files=all']);
}

/** @param {string} base @returns {string[]} */
export function diffNameOnlyArgv(base) {
  return gitArgv('diff', ['--name-only', assertNoLeadingDash(base, 'base')]);
}

/**
 * `path` sits after a literal `--`, so it is always treated as a pathspec by git regardless of
 * its first character — only `base` needs the leading-dash guard here.
 * @param {string} base @param {string} path @returns {string[]}
 */
export function diffArgv(base, path) {
  return gitArgv('diff', [assertNoLeadingDash(base, 'base'), '--', assertNonEmptyString(path, 'path')]);
}

/**
 * `git diff --no-index /dev/null <path>` — every line of a new/untracked file as a `+` hunk.
 * Exits 1 when differences are found (the expected case), which is not a failure.
 * @param {string} path @returns {string[]}
 */
export function diffNoIndexArgv(path) {
  return gitArgv('diff', ['--no-index', '/dev/null', assertNoLeadingDash(path, 'path')]);
}

/** @param {string} ref @param {string} path @returns {string[]} */
export function showArgv(ref, path) {
  return gitArgv('show', [`${assertNoLeadingDash(ref, 'ref')}:${assertNonEmptyString(path, 'path')}`]);
}

/** @param {string} ref @returns {string[]} */
export function revParseArgv(ref) {
  return gitArgv('rev-parse', [assertNoLeadingDash(ref, 'ref')]);
}

/**
 * Exits 0 when `ancestor` is an ancestor of `descendant`, 1 when it is not — both normal answers.
 * @param {string} ancestor @param {string} descendant @returns {string[]}
 */
export function isAncestorArgv(ancestor, descendant) {
  return gitArgv('merge-base', [
    '--is-ancestor',
    assertNoLeadingDash(ancestor, 'ancestor'),
    assertNoLeadingDash(descendant, 'descendant'),
  ]);
}

/** @returns {string[]} */
export function fetchArgv() {
  return gitArgv('fetch', []);
}

/** @param {string} ref @returns {string[]} */
export function archiveArgv(ref) {
  return gitArgv('archive', [assertNoLeadingDash(ref, 'ref')]);
}

/**
 * `args` is a raw passthrough today (no block/block owns a caller yet); every element is
 * guarded against a leading `-` for the same option-injection reason as the single-ref builders
 * above, which also closes the `--output=<file>`/`-o`/`--exec=<cmd>` "read helper writes a file
 * or runs a command" hole. A future caller that needs a real git-log flag extends this guard
 * (or moves to typed options) deliberately, rather than this staying an open passthrough.
 * @param {string[]} [args] @returns {string[]}
 */
export function logArgv(args = []) {
  return gitArgv('log', assertNoLeadingDashes(args, 'args'));
}

/** @param {string[]} [args] @returns {string[]} */
export function lsFilesArgv(args = []) {
  return gitArgv('ls-files', assertNoLeadingDashes(args, 'args'));
}

// ── The two allowed writes ──────────────────────────────────────────────────

/** @param {string} message @returns {string[]} */
export function commitArgv(message) {
  return gitArgv('commit', ['-m', assertNonEmptyString(message, 'message')]);
}

/**
 * Why a branch value is not a plain git branch name, or null when it is. Mirrors
 * `git check-ref-format --branch` (git-check-ref-format(1)), plus two refspec-specific refusals:
 * `git push origin <branch>` reads the value as a REFSPEC, so a leading `+` is a force-push and a
 * `:` deletes (`:main`) or pushes a different ref (`src:dst`).
 * @param {string} branch
 * @returns {string | null}
 */
function branchNameProblem(branch) {
  if (branch.startsWith('-')) {
    return 'must not start with "-" — option injection (--force/--mirror/--delete)';
  }
  if (branch.startsWith('+')) {
    return 'must not start with "+" — a +refspec is a force-push';
  }
  if (branch.includes(':')) {
    return 'must not contain ":" — refspec syntax (":main" deletes, "a:b" pushes another ref)';
  }
  // Space, DEL and ASCII control characters, plus git's forbidden ref characters.
  if (/[\u0000- \u007f~^?*[\\]/.test(branch)) {
    return 'must not contain whitespace, control characters or any of ~ ^ ? * [ \\';
  }
  if (branch.includes('..') || branch.includes('@{') || branch === '@') {
    return 'must not contain "..", "@{" or be "@"';
  }
  if (branch.endsWith('/') || branch.endsWith('.') || branch.includes('//')) {
    return 'must not end with "/" or ".", or contain "//"';
  }
  if (branch.split('/').some((part) => part.startsWith('.') || part.endsWith('.lock'))) {
    return 'no path component may start with "." or end with ".lock"';
  }
  return null;
}

/**
 * @param {unknown} branch
 * @returns {string}
 */
function assertValidBranchName(branch) {
  if (typeof branch !== 'string' || branch.trim().length === 0) {
    throw new TypeError('git.mjs: pushArgv branch must be a non-empty string');
  }
  const problem = branchNameProblem(branch);
  if (problem) {
    throw new TypeError(`git.mjs: pushArgv branch ${JSON.stringify(branch)} ${problem}`);
  }
  return branch;
}

/**
 * @param {{branch?: string, setUpstream?: boolean}} [opts]
 * @returns {string[]}
 * @throws {TypeError} if `branch` is not a plain git branch name (option injection or refspec
 *   syntax — see `branchNameProblem`), or if `setUpstream` is true without a `branch`.
 */
export function pushArgv(opts = {}) {
  const { branch, setUpstream = false } = opts;
  if (setUpstream && !branch) {
    throw new TypeError('git.mjs: pushArgv({ setUpstream: true }) requires a branch');
  }
  if (branch === undefined) {
    return gitArgv('push', []);
  }
  assertValidBranchName(branch);
  return setUpstream ? gitArgv('push', ['-u', 'origin', branch]) : gitArgv('push', ['origin', branch]);
}

// ── Async wrappers ──────────────────────────────────────────────────────────

/** @param {string} cwd */
export function status(cwd) {
  return exec(statusArgv(), { cwd, env: gitEnv() });
}

/** @param {string} base @param {string} cwd */
export function diffNameOnly(base, cwd) {
  return exec(diffNameOnlyArgv(base), { cwd, env: gitEnv() });
}

/** @param {string} base @param {string} path @param {string} cwd */
export function diff(base, path, cwd) {
  return exec(diffArgv(base, path), { cwd, env: gitEnv() });
}

/** @param {string} path @param {string} cwd */
export function diffNoIndex(path, cwd) {
  return exec(diffNoIndexArgv(path), { cwd, env: gitEnv(), okExitCodes: [0, 1] });
}

/** @param {string} ref @param {string} path @param {string} cwd */
export function show(ref, path, cwd) {
  return exec(showArgv(ref, path), { cwd, env: gitEnv() });
}

/** @param {string} ref @param {string} cwd */
export function revParse(ref, cwd) {
  return exec(revParseArgv(ref), { cwd, env: gitEnv() });
}

/**
 * @param {string} ancestor @param {string} descendant @param {string} cwd
 * @returns {Promise<boolean>}
 * @throws {Error} if the underlying `git merge-base --is-ancestor` call errors for a reason
 *   other than "not an ancestor" (exit 1) — e.g. exit 128 on an unknown ref — so that case is
 *   never silently reported as `false`.
 */
export async function isAncestor(ancestor, descendant, cwd) {
  const res = await exec(isAncestorArgv(ancestor, descendant), { cwd, env: gitEnv(), okExitCodes: [0, 1] });
  if (res.result !== 'ok') {
    throw new Error(
      `git.mjs: isAncestor(${ancestor}, ${descendant}) failed (code ${res.code}): ${res.stderr || res.error || 'unknown error'}`,
    );
  }
  return res.code === 0;
}

/**
 * @param {string} cwd
 * @param {{timeoutMs?: number}} [opts]
 */
export function fetch(cwd, opts = {}) {
  const { timeoutMs = DEFAULT_NETWORK_TIMEOUT_MS } = opts;
  return exec(fetchArgv(), { cwd, timeoutMs, env: gitEnv({ GIT_TERMINAL_PROMPT: '0' }) });
}

/** @param {string} ref @param {string} cwd */
export function archive(ref, cwd) {
  return exec(archiveArgv(ref), { cwd, env: gitEnv() });
}

/** @param {string} cwd @param {string[]} [args] */
export function log(cwd, args) {
  return exec(logArgv(args), { cwd, env: gitEnv() });
}

/** @param {string} cwd @param {string[]} [args] */
export function lsFiles(cwd, args) {
  return exec(lsFilesArgv(args), { cwd, env: gitEnv() });
}

/** @param {string} message @param {string} cwd */
export function commit(message, cwd) {
  return exec(commitArgv(message), { cwd, env: gitEnv() });
}

/**
 * @param {string} cwd
 * @param {{branch?: string, setUpstream?: boolean, timeoutMs?: number}} [opts]
 */
export function push(cwd, opts = {}) {
  const { timeoutMs = DEFAULT_NETWORK_TIMEOUT_MS, ...pushOpts } = opts;
  return exec(pushArgv(pushOpts), { cwd, timeoutMs, env: gitEnv({ GIT_TERMINAL_PROMPT: '0' }) });
}
