/** Shared worktree path selection and read-only repository discovery. */
import { lstatSync, realpathSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { StateError } from '../state/paths.mjs';
import { exec } from './exec.mjs';

/** The default branch template (`proof.worktree.branch`). */
export const DEFAULT_BRANCH_TEMPLATE = 'forge/{run}/{block}';

/** Substitute once, preserving template-like text within run and block values. */
/** @param {string} template @param {{run: string, block: string}} values */
export function branchName(template, { run, block }) {
  return template.replace(/\{(run|block)\}/g, (_whole, key) => (key === 'run' ? run : block));
}

/** @param {string} top @param {NodeJS.Platform} [platform] */
export function defaultWorktreeDirectory(top, platform = process.platform) {
  return path.resolve(top, '..', `${path.basename(top)}-worktrees${platform === 'darwin' ? '.noindex' : ''}`);
}

/** @param {string} dir */
export function isNoindexPath(dir) {
  return dir.split(path.sep).some((part) => part.endsWith('.noindex'));
}

/** Resolve existing ancestors so planned and recorded paths have the same spelling.
 * @param {string} p
 * @returns {string}
 */
export function realAsFarAsExists(p) {
  const rest = [];
  let head = p;
  while (path.dirname(head) !== head) {
    try { lstatSync(head); break; } catch { /* try the parent */ }
    rest.unshift(path.basename(head));
    head = path.dirname(head);
  }
  try { head = realpathSync(head); } catch { /* keep unresolved spelling */ }
  return path.join(head, ...rest);
}

/** Read git without inherited repository context, global configuration or prompts.
 * @param {string[]} argv @param {string} cwd
 */
async function git(argv, cwd) {
  const env = { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull, GIT_TERMINAL_PROMPT: '0' };
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  return exec(argv, { cwd, env, timeoutMs: 30000 });
}

/** @param {{stderr: string, code: number | null}} res */
function gitReason(res) {
  const line = res.stderr.split('\n').find((l) => /^(fatal|error): /.test(l));
  return line ? line.replace(/^(fatal|error): /, '').trim() : `git exited ${res.code}`;
}

/** Resolve the directory without moving live trees; an existing macOS run keeps its old default.
 * @param {{top: string, settings: {dir: string | null, branch: string}, runId?: string, platform?: NodeJS.Platform}} opts
 * @returns {Promise<{dir: string, mark: boolean}>}
 */
export async function worktreeDirectory({ top, settings, runId, platform = process.platform }) {
  const old = realAsFarAsExists(defaultWorktreeDirectory(top, 'linux'));
  if (settings.dir !== null) return { dir: realAsFarAsExists(path.resolve(top, settings.dir)), mark: false };
  if (platform !== 'darwin') return { dir: old, mark: false };
  if (runId) {
    const res = await git(['git', 'worktree', 'list', '--porcelain', '-z'], top);
    if (res.result !== 'ok') throw new StateError('git-failed', `git worktree list failed (${gitReason(res)})`);
    let where = '';
    for (const line of res.stdout.split('\0')) {
      if (line.startsWith('worktree ')) where = line.slice('worktree '.length);
      else if (line.startsWith('branch refs/heads/') && where
        && realAsFarAsExists(path.dirname(where)) === old
        && line.slice('branch refs/heads/'.length) === branchName(settings.branch, { run: runId, block: path.basename(where) })) return { dir: old, mark: false };
    }
  }
  return { dir: realAsFarAsExists(defaultWorktreeDirectory(top, platform)), mark: true };
}

/** @param {string} cwd @returns {Promise<string>} the repository top level, realpath'd. */
export async function repoTopOf(cwd) {
  const res = await git(['git', 'rev-parse', '--show-toplevel'], cwd);
  if (res.result !== 'ok') throw new StateError('git-failed', `git rev-parse --show-toplevel failed (${gitReason(res)})`);
  return realpathSync(res.stdout.trim());
}
