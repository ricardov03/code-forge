/**
 * Worktree isolation (B62a, issue #13 item 4; `proof.isolation: worktree`). Each block gets its own
 * git worktree on its own branch, made by `block open` before the block is recorded and removed,
 * on request, by `block close --remove-worktree` after a successful close. Nothing here pushes or
 * merges, and the only branch it deletes is one a failed open made itself.
 *
 * The settings come from the run's config snapshot (`configInForce`), never from the file on disk:
 * `proof.isolation` and `proof.worktree` are run-fixed (`IMMUTABLE_KEYS`), so `run reload` cannot
 * switch isolation while blocks are open.
 *
 *   proof.worktree.dir     where the trees go, resolved from the repository top level (not from the
 *                          workspace, even for a workspace in a subdirectory); default
 *                          `../<repo-name>-worktrees`, where `<repo-name>` is replaced with the
 *                          basename of the top level — beside the repository, so the main tree
 *                          never lists a tree as untracked. The block's tree is `<dir>/<block>`.
 *   proof.worktree.branch  a template; only `{run}` and `{block}` are substituted, and the result
 *                          must pass `git check-ref-format --branch`. Default `forge/{run}/{block}`.
 *   proof.worktree.base    the commit-ish the tree starts from; default the block's base (the
 *                          `--base` flag of `block open`, else HEAD).
 *
 * Every git call is an argv array (never a shell) with the inherited `GIT_*` environment stripped
 * (`gitEnv`), like the other git calls of the package. It never leaves half a tree behind: a
 * failed `git worktree add` deletes the branch it made, and a tree made for an open that then
 * fails to record is removed again (`rollback`).
 *
 * B62b: {@link blockTree} says where a block with a tree does its work — `gates run --run --block`
 * runs the gates there, `review-file` hashes the content there and the worker reviews it there,
 * and `block close` measures that tree against its base. The run's state (the record, the ledger,
 * the review queue and signed results under the main top level's `.code-forge/`) stays in the
 * main tree, and nothing checks out, pushes or merges: the work stays on the block's branch.
 */

import { lstatSync, realpathSync, statSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { configInForce } from '../state/config-snapshot.mjs';
import { StateError } from '../state/paths.mjs';
import { exec } from '../util/exec.mjs';

/** The default branch template (`proof.worktree.branch`). */
export const DEFAULT_BRANCH_TEMPLATE = 'forge/{run}/{block}';

/** `git worktree add` checks out a whole tree: allow it longer than a read. */
const ADD_TIMEOUT_MS = 300000;
const GIT_TIMEOUT_MS = 30000;

/**
 * @typedef {{dir: string | null, branch: string, base: string | null}} WorktreeSettings
 * @typedef {{path: string, branch: string, base_sha: string}} WorktreeRecord - what the block entry stores.
 * @typedef {{worktree: WorktreeRecord, created: boolean, rollback: (() => Promise<string | null>) | null}} MadeWorktree
 *   `rollback` (only when `created`) removes the tree this call made, and its branch if this call made that too;
 *   it resolves to null when both are gone, else a sentence naming each leftover with git's reason.
 * @typedef {{path: string, make: () => Promise<MadeWorktree>}} PlannedWorktree
 */

/**
 * The worktree settings of the run, from its config snapshot in force, or null when the run is
 * not isolated by worktree (`proof.isolation` export or lock, absent, or a record from before
 * snapshots).
 * @param {Record<string, any>} record
 * @returns {WorktreeSettings | null}
 * @throws {StateError} `config-snapshot` when the snapshot in force does not match its hash.
 */
export function worktreeSettings(record) {
  const proof = configInForce(record)?.proof;
  if (proof?.isolation !== 'worktree') return null;
  const wt = proof.worktree ?? {};
  return {
    // the schema's default text `../<repo-name>-worktrees` is documentation only, never used as is:
    // an unset dir is null here, and `prepareWorktree` puts the top level's basename in its place
    dir: typeof wt.dir === 'string' ? wt.dir : null,
    branch: typeof wt.branch === 'string' ? wt.branch : DEFAULT_BRANCH_TEMPLATE,
    base: typeof wt.base === 'string' ? wt.base : null,
  };
}

/**
 * The branch name for a block: `{run}` and `{block}` substituted in one pass (a value that itself
 * contains `{block}` is never substituted again); any other text is kept as written.
 * @param {string} template @param {{run: string, block: string}} values
 * @returns {string}
 */
export function branchName(template, { run, block }) {
  return template.replace(/\{(run|block)\}/g, (_whole, key) => (key === 'run' ? run : block));
}

/** `process.env` minus every inherited `GIT_*` (hooks export GIT_DIR…), with no system/global config and no prompt. */
function gitEnv() {
  /** @type {NodeJS.ProcessEnv} */
  const env = { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull, GIT_TERMINAL_PROMPT: '0' };
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  return env;
}

/** @param {string[]} argv @param {string} cwd @param {number} [timeoutMs] */
function git(argv, cwd, timeoutMs = GIT_TIMEOUT_MS) {
  return exec(argv, { cwd, env: gitEnv(), timeoutMs });
}

/**
 * Git's own reason for a failed call: its first `fatal:` or `error:` line without the prefix, else
 * the exit code.
 * @param {{stderr: string, code: number | null}} res @returns {string}
 */
function gitReason(res) {
  const line = res.stderr.split('\n').find((l) => /^(fatal|error): /.test(l));
  return line ? line.replace(/^(fatal|error): /, '').trim() : `git exited ${res.code}`;
}

/**
 * @param {string} name @param {string} top @param {string} id
 * @throws {StateError} `worktree-failed` when git does not take `name` as a branch name.
 */
async function assertBranchName(name, top, id) {
  const refuse = () => {
    throw new StateError('worktree-failed', `block ${id}: proof.worktree.branch gives ${JSON.stringify(name)}, which is not a valid git branch name; nothing was recorded`);
  };
  if (name.startsWith('-')) refuse();
  const res = await git(['git', 'check-ref-format', '--branch', name], top);
  // `--branch` also expands `@{-n}` to a previous branch: the name must come back unchanged
  if (res.result !== 'ok' || res.stdout.trim() !== name) refuse();
}

/** @param {string} branch @param {string} top @returns {Promise<boolean>} */
async function branchExists(branch, top) {
  const res = await git(['git', 'show-ref', '--verify', '--quiet', `refs/heads/${branch}`], top);
  return res.result === 'ok';
}

/** @param {string} p @returns {boolean} whether anything (a dangling symlink included) is at `p`. */
function occupied(p) {
  try {
    lstatSync(p);
    return true;
  } catch {
    return false;
  }
}

/** @param {string} p @returns {string} the realpath, or `p` itself when it cannot be resolved. */
function real(p) {
  try {
    return realpathSync(p);
  } catch {
    return p;
  }
}

/**
 * The worktree git has registered at `where`: its branch (`refs/heads/<name>` ⇒ `<name>`; null for
 * a detached HEAD) and whether git marks it `prunable` (its directory is gone). Null when no
 * worktree of the repository is registered there.
 * @param {string} where @param {string} top
 * @returns {Promise<{branch: string | null, prunable: boolean} | null>}
 * @throws {StateError} `git-failed` when the list cannot be read.
 */
async function registeredAt(where, top) {
  const res = await git(['git', 'worktree', 'list', '--porcelain', '-z'], top);
  if (res.result !== 'ok') throw new StateError('git-failed', `git worktree list failed (${gitReason(res)})`);
  const target = real(where);
  // `-z`: one NUL-terminated line per attribute, an empty line between worktrees
  /** @type {{branch: string | null, prunable: boolean} | null} */
  let current = null;
  /** @type {{branch: string | null, prunable: boolean} | null} */
  let found = null;
  for (const line of res.stdout.split('\0')) {
    if (line.startsWith('worktree ')) {
      current = { branch: null, prunable: false };
      if (real(line.slice('worktree '.length)) === target) found = current;
    } else if (current !== null && line.startsWith('branch refs/heads/')) current.branch = line.slice('branch refs/heads/'.length);
    else if (current !== null && (line === 'prunable' || line.startsWith('prunable '))) current.prunable = true;
  }
  return found;
}

/**
 * @param {string} id
 * @throws {StateError} `worktree-failed` unless `id` is one plain path segment, so `<dir>/<id>` stays inside `dir`.
 */
function assertSegment(id) {
  if (id === '' || id === '.' || id === '..' || id.includes('/') || id.includes('\\') || id.includes('\0')) {
    throw new StateError('worktree-failed', `block ${JSON.stringify(id)}: a block id must be one plain path segment (no "/", "\\" or "..") to name its worktree; nothing was recorded`);
  }
}

/**
 * Remove what a failed open made: the tree at `treePath` (`git worktree remove --force`), then the
 * branch (`git branch -D`); either may be null (nothing to remove). Each result is checked.
 * @param {string} top @param {string | null} treePath @param {string | null} branch
 * @returns {Promise<string[]>} one entry per leftover, with git's reason; empty when all is gone.
 */
async function discard(top, treePath, branch) {
  /** @type {string[]} */
  const left = [];
  if (treePath !== null) {
    const res = await git(['git', 'worktree', 'remove', '--force', treePath], top);
    if (res.result !== 'ok') left.push(`the worktree ${treePath} (${gitReason(res)})`);
  }
  if (branch !== null) {
    const res = await git(['git', 'branch', '-D', branch], top);
    if (res.result !== 'ok') left.push(`the branch ${branch} (${gitReason(res)})`);
  }
  return left;
}

/** @param {string[]} left - from {@link discard}, not empty. @returns {string} */
function leftBehind(left) {
  return `left behind: ${left.join('; ')} — remove ${left.length === 1 ? 'it' : 'them'} by hand`;
}

/**
 * `git worktree add <treePath> …`. When it fails, whatever it made is removed again so a failed
 * open leaves nothing behind: a tree git registered anyway (after a timeout, say) is removed
 * (`--force`; a registration whose directory is gone is pruned), then a branch it made
 * (`newBranch`) is deleted. Anything that cannot be removed is named in the refusal.
 * @param {string[]} argv @param {string} treePath @param {string} top @param {string} id @param {string | null} newBranch
 * @throws {StateError} `worktree-failed` with git's reason.
 */
async function addTree(argv, treePath, top, id, newBranch) {
  const res = await git(argv, top, ADD_TIMEOUT_MS);
  if (res.result === 'ok') return;
  const entry = await registeredAt(treePath, top);
  if (entry?.prunable) await git(['git', 'worktree', 'prune'], top);
  const tree = entry !== null && !entry.prunable ? treePath : null;
  const branch = newBranch !== null && (await branchExists(newBranch, top)) ? newBranch : null;
  const left = await discard(top, tree, branch);
  const outcome = left.length === 0 ? 'nothing was recorded' : `nothing was recorded, but ${leftBehind(left)}`;
  throw new StateError('worktree-failed', `block ${id}: git worktree add failed (${gitReason(res)}); ${outcome}`);
}

/**
 * `p` with its nearest existing ancestor realpath'd (the rest joined on as written), so a path
 * planned before it exists is spelled as its realpath will be once it does.
 * @param {string} p - absolute. @returns {string}
 */
function realAsFarAsExists(p) {
  /** @type {string[]} */
  const rest = [];
  let head = p;
  while (!occupied(head) && path.dirname(head) !== head) {
    rest.unshift(path.basename(head));
    head = path.dirname(head);
  }
  return path.join(real(head), ...rest);
}

/**
 * @param {string} ref @param {string} top @param {string} id @returns {Promise<string>} the full commit sha
 * @throws {StateError} `worktree-failed`
 */
async function commitOf(ref, top, id) {
  const res = ref.startsWith('-') ? null : await git(['git', 'rev-parse', '--verify', '--quiet', `${ref}^{commit}`], top);
  if (res === null || res.result !== 'ok') throw new StateError('worktree-failed', `block ${id}: proof.worktree.base ${JSON.stringify(ref)} is not a commit of the repository; nothing was recorded`);
  return res.stdout.trim();
}

/**
 * Plan the block's worktree, then make it (or reuse the one an earlier attempt of the same block
 * made) with `make()`. Both run in `block open` under the run lock BEFORE anything is recorded: the
 * plan first (read-only checks; its `path` feeds the overlap rule), `make()` after the open's
 * other checks. A refusal in either leaves no block, no row, no dispatch, no tree and no branch.
 *
 * Fresh (no `previous`): the branch is `proof.worktree.branch` for this run and block; the tree is
 * `<dir>/<block>` (`dir` resolved from the repository top level); the base is
 * `proof.worktree.base`, else `baseSha`. A block id that is not one path segment, a branch name git
 * refuses, a branch that already exists, something already at the path, a base that is not a
 * commit, or a failing `git worktree add <path> -b <branch> <base>` refuses the open
 * (`worktree-failed`); a failed add deletes the branch it made.
 *
 * Re-open (`previous`, the worktree an earlier entry of the same block recorded): the same path,
 * branch and base. A worktree still registered at that path, on that branch, not `prunable`, with
 * its directory present, is reused as it is (`created` false); one on another branch or a detached
 * HEAD refuses the open. Otherwise a stale registration (the
 * directory deleted by hand, or removed by `block close --remove-worktree`) is pruned
 * (`git worktree prune`) and the tree is made again on the same branch
 * (`git worktree add <path> <branch>`, or `-b <branch> <its base>` when the branch was deleted
 * too); anything else at the path refuses the open.
 *
 * When `created`, `rollback()` undoes `make()` for an open that fails to record afterwards:
 * `git worktree remove --force <path>`, and `git branch -D <branch>` only when this call made the
 * branch (a branch kept from an earlier attempt is kept). Both results are checked: it resolves to
 * a sentence naming what is left behind, or null.
 * @param {{cwd: string, settings: WorktreeSettings, runId: string, id: string, baseSha: string, previous: WorktreeRecord | null}} opts
 * @returns {Promise<PlannedWorktree>} the tree's `path` is absolute (a realpath once made).
 * @throws {StateError} `worktree-failed`, `git-failed`
 */
export async function prepareWorktree({ cwd, settings, runId, id, baseSha, previous }) {
  assertSegment(id);
  const top = await repoTopOf(cwd);
  const refuseOccupied = (/** @type {string} */ p) => {
    if (occupied(p)) throw new StateError('worktree-failed', `block ${id}: ${p} already exists and is not this block's worktree — move it away or change proof.worktree.dir; nothing was recorded`);
  };
  /** @param {WorktreeRecord} want @param {string | null} newBranch @returns {MadeWorktree} */
  const made = (want, newBranch) => {
    const worktree = { ...want, path: realpathSync(want.path) };
    const rollback = async () => {
      const left = await discard(top, worktree.path, newBranch);
      return left.length === 0 ? null : `Removing the worktree of the failed open ${leftBehind(left)}.`;
    };
    return { worktree, created: true, rollback };
  };

  if (previous) {
    const want = { ...previous };
    const make = async () => {
      const entry = await registeredAt(want.path, top);
      if (entry !== null && !entry.prunable && occupied(want.path)) {
        if (entry.branch === want.branch) return { worktree: want, created: false, rollback: null };
        throw new StateError('worktree-failed', `block ${id}: worktree ${want.path} is on ${entry.branch ?? 'a detached HEAD'}, not ${want.branch} — check out ${want.branch} in it; nothing was recorded`);
      }
      if (entry !== null) await git(['git', 'worktree', 'prune'], top);
      refuseOccupied(want.path);
      const attach = await branchExists(want.branch, top);
      const newBranch = attach ? null : want.branch;
      await addTree(attach ? ['git', 'worktree', 'add', want.path, want.branch] : ['git', 'worktree', 'add', want.path, '-b', want.branch, want.base_sha], want.path, top, id, newBranch);
      return made(want, newBranch);
    };
    return { path: want.path, make };
  }

  const branch = branchName(settings.branch, { run: runId, block: id });
  await assertBranchName(branch, top, id);
  const refuseBranch = async () => {
    if (await branchExists(branch, top)) throw new StateError('worktree-failed', `block ${id}: branch ${branch} already exists — delete it or change proof.worktree.branch; nothing was recorded`);
  };
  await refuseBranch();
  // spelled as the realpath it will have, so the planned path (the overlap rule) equals the recorded one
  const dir = realAsFarAsExists(path.resolve(top, settings.dir ?? path.join('..', `${path.basename(top)}-worktrees`)));
  const want = { path: path.join(dir, id), branch, base_sha: settings.base === null ? baseSha : await commitOf(settings.base, top, id) };
  refuseOccupied(want.path);
  const make = async () => {
    // checked again: a branch made between the plan and now is someone else's, so the add must not
    // run (its failure would delete that branch)
    await refuseBranch();
    await addTree(['git', 'worktree', 'add', want.path, '-b', want.branch, want.base_sha], want.path, top, id, want.branch);
    return made(want, want.branch);
  };
  return { path: want.path, make };
}

/**
 * `git worktree remove <path>` — never `--force`, so a tree with modified or untracked files is
 * kept. The branch is always kept. A tree whose directory is already gone (git marks it
 * `prunable`) is pruned (`git worktree prune`) and counts as removed.
 * @param {{cwd: string, worktree: WorktreeRecord}} opts
 * @returns {Promise<string | null>} null when the tree was removed, else git's reason.
 */
export async function removeWorktree({ cwd, worktree }) {
  const res = await git(['git', 'worktree', 'remove', worktree.path], cwd);
  if (res.result === 'ok') return null;
  /** @type {{branch: string | null, prunable: boolean} | null} */
  let entry;
  try {
    entry = await registeredAt(worktree.path, cwd);
  } catch {
    return gitReason(res);
  }
  if (!entry?.prunable) return gitReason(res);
  const pruned = await git(['git', 'worktree', 'prune'], cwd);
  return pruned.result === 'ok' ? null : gitReason(pruned);
}

/**
 * @param {string} cwd @returns {Promise<string>} the repository top level, realpath'd.
 * @throws {StateError} `git-failed`
 */
async function repoTopOf(cwd) {
  const res = await git(['git', 'rev-parse', '--show-toplevel'], cwd);
  if (res.result !== 'ok') throw new StateError('git-failed', `git rev-parse --show-toplevel failed (${gitReason(res)})`);
  return realpathSync(res.stdout.trim());
}

/**
 * @typedef {{top: string, workspace: string, path: string, branch: string, base_sha: string}} BlockTree
 *   `top`: the tree's top level, realpath'd; `workspace`: the run's workspace at its place in the
 *   tree (the tree itself, or the same subdirectory of it when the workspace is a subdirectory of
 *   the main tree); `path`, `branch`, `base_sha`: as the block entry records them.
 */

/**
 * B62b: where the block `entry` of the run whose workspace is `workspace` does its work — null for
 * a block without a `worktree` record (export and lock runs, and blocks opened before B62a).
 * @param {string} workspace - the run record's `workspace` (in the main tree).
 * @param {Record<string, any> | undefined} entry - the run record's block entry.
 * The messages name the tree but not the block or what was skipped: each caller adds those.
 * @returns {Promise<BlockTree | null>} null exactly when the entry has no `worktree` record.
 * @throws {StateError} `worktree-malformed` ({@link worktreeRecord}); `worktree-missing` when the
 *   recorded tree is gone; `worktree-not-dir` when its path is not a directory;
 *   `worktree-branch-mismatch` when the tree is not on its recorded branch (another branch, a
 *   detached HEAD, or a plain directory that is no git worktree); `worktree-workspace-missing`
 *   when the workspace's subdirectory is not in the tree; `worktree-foreign` when the tree belongs
 *   to another repository (its git common dir is not the workspace's); `git-failed` when git
 *   cannot run (a timeout, a spawn error) or the main top level cannot be read.
 */
export async function blockTree(workspace, entry) {
  if (entry?.worktree === undefined || entry.worktree === null) return null;
  const tree = worktreeRecord(entry.worktree);
  let top;
  try {
    top = realpathSync(tree.path);
  } catch {
    throw new StateError('worktree-missing', `the worktree ${tree.path} is gone (removed or moved)`);
  }
  if (!statSync(top).isDirectory()) throw new StateError('worktree-not-dir', `the worktree ${tree.path} is not a directory`);
  await assertOnBranch(top, tree);
  if ((await commonDirOf(top)) !== (await commonDirOf(workspace))) throw new StateError('worktree-foreign', `the worktree ${tree.path} belongs to another repository`);
  const rel = path.relative(await repoTopOf(workspace), real(workspace));
  if (rel.startsWith('..') || path.isAbsolute(rel)) throw new StateError('worktree-workspace-missing', `the run's workspace ${workspace} is not inside its repository, so it has no place in the worktree ${tree.path}`);
  const inTree = rel === '' ? top : path.join(top, rel);
  if (rel !== '' && !isDirectory(inTree)) throw new StateError('worktree-workspace-missing', `the worktree ${tree.path} has no ${rel} directory (the run's workspace)`);
  return { top, workspace: inTree, path: tree.path, branch: tree.branch, base_sha: tree.base_sha };
}

/** A full hex object id: SHA-1 (40 hex digits) or SHA-256 (64). */
const OBJECT_ID = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;
/** A control character (newlines included). */
const CONTROL = /[\u0000-\u001f\u007f]/;

/**
 * B62b: a block's recorded `worktree`, checked before any of it reaches a path or git: an absolute
 * `path` without control characters, a non-empty `branch` without control characters, and a `base_sha` that is a full hex
 * object id (so it can never be read as a git option).
 * @param {unknown} recorded
 * @returns {{path: string, branch: string, base_sha: string}}
 * @throws {StateError} `worktree-malformed`
 */
export function worktreeRecord(recorded) {
  const t = /** @type {Record<string, unknown>} */ (recorded);
  const ok =
    t !== null &&
    typeof t === 'object' &&
    typeof t.path === 'string' &&
    path.isAbsolute(t.path) &&
    !CONTROL.test(t.path) &&
    typeof t.branch === 'string' &&
    t.branch.length > 0 &&
    !CONTROL.test(t.branch) &&
    typeof t.base_sha === 'string' &&
    OBJECT_ID.test(t.base_sha);
  if (!ok) throw new StateError('worktree-malformed', 'malformed worktree record (it needs an absolute path, a branch and a base_sha)');
  return { path: /** @type {string} */ (t.path), branch: /** @type {string} */ (t.branch), base_sha: /** @type {string} */ (t.base_sha) };
}

/**
 * The tree at `top` must still be on its recorded branch: a read-only `git symbolic-ref -q HEAD`
 * that does not look above `top`, so a plain directory is never taken for a tree.
 * @param {string} top - realpath'd @param {{path: string, branch: string}} tree
 * @throws {StateError} `worktree-branch-mismatch`; `git-failed` when git cannot run (a timeout, a
 *   spawn error, a signal) — that is never read as a branch mismatch.
 */
async function assertOnBranch(top, tree) {
  const res = await exec(['git', '-C', top, 'symbolic-ref', '-q', 'HEAD'], { cwd: top, env: { ...gitEnv(), GIT_CEILING_DIRECTORIES: path.dirname(top) }, timeoutMs: GIT_TIMEOUT_MS });
  if (res.timedOut || res.error !== undefined || res.signal !== null) {
    throw new StateError('git-failed', `git symbolic-ref HEAD could not run in the worktree ${tree.path} (${res.timedOut ? 'timed out' : (res.error ?? `killed by ${res.signal}`)})`);
  }
  const head = res.result === 'ok' ? res.stdout.trim() : null;
  if (head === `refs/heads/${tree.branch}`) return;
  const now = head === null ? 'its HEAD is detached, or it is not a git worktree' : `it is on ${head.startsWith('refs/heads/') ? head.slice('refs/heads/'.length) : head}`;
  throw new StateError('worktree-branch-mismatch', `the worktree ${tree.path} is not on its branch ${tree.branch} (${now})`);
}

/**
 * @param {string} cwd @returns {Promise<string>} the realpath of the git common dir of the
 *   repository at `cwd` (the main `.git`, which all of its worktrees share).
 * @throws {StateError} `git-failed`
 */
async function commonDirOf(cwd) {
  const res = await git(['git', 'rev-parse', '--path-format=absolute', '--git-common-dir'], cwd);
  if (res.result !== 'ok') throw new StateError('git-failed', `git rev-parse --git-common-dir failed in ${cwd} (${gitReason(res)})`);
  return real(res.stdout.trim());
}

/** @param {string} p @returns {boolean} whether `p` is a directory (following symlinks). */
function isDirectory(p) {
  try {
    return statSync(p).isDirectory();
  } catch {
    return false;
  }
}
