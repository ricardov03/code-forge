/**
 * Builds the shared-tree fixture for B8 (plan §4.7): a throwaway git repo, created fresh in a
 * temp dir by the test that calls this — never committed as a nested `.git`.
 *
 *   base  (a.txt b.txt c.txt .code-forge.yml .gitignore)
 *   head  = base + e.txt            (another block's commit landing on main)
 *   side  = base + b.txt changed, built with `commit-tree` (NOT an ancestor of head)
 *   working tree: a.txt and c.txt modified, d.txt untracked  ⇒ dirty set {a.txt, c.txt, d.txt}
 *
 * Every git call strips inherited `GIT_*` variables (a hook's GIT_DIR would redirect it to the
 * outer repo) and ignores system/global config.
 */

import { execFileSync } from 'node:child_process';
import { writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

/** @param {string[]} args @param {string} cwd @param {NodeJS.ProcessEnv} [extra] */
export function git(args, cwd, extra = {}) {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  Object.assign(env, {
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: os.devNull,
    GIT_AUTHOR_NAME: 'B8 Fixture',
    GIT_AUTHOR_EMAIL: 'b8@example.test',
    GIT_COMMITTER_NAME: 'B8 Fixture',
    GIT_COMMITTER_EMAIL: 'b8@example.test',
    ...extra,
  });
  return execFileSync('git', args, { cwd, env, stdio: 'pipe', encoding: 'utf8' }).trimEnd();
}

/** A minimal valid `.code-forge.yml` (provider + four levels), slug `two-blocks`. */
export const CONFIG_YAML = [
  'version: 1',
  'project:',
  '  slug: two-blocks',
  'provider: anthropic',
  'engine: harness',
  'levels:',
  '  L0: {model: claude-haiku-4-5-20251001}',
  '  L1: {model: claude-sonnet-5}',
  '  L2: {model: claude-opus-5-5}',
  '  L3: {model: claude-fable-5-1}',
  '',
].join('\n');

/**
 * @param {string} dir - an existing empty directory
 * @returns {Promise<{baseSha: string, headSha: string, sideSha: string}>}
 */
export async function buildTwoBlocksRepo(dir) {
  const write = (/** @type {string} */ name, /** @type {string} */ text) => writeFile(path.join(dir, name), text);
  const commit = (/** @type {string} */ msg) => git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', msg], dir);
  git(['init', '-q'], dir);
  git(['symbolic-ref', 'HEAD', 'refs/heads/main'], dir);
  await write('.gitignore', '.code-forge/\n');
  await write('.code-forge.yml', CONFIG_YAML);
  for (const name of ['a.txt', 'b.txt', 'c.txt']) await write(name, `${name} v1\n`);
  git(['add', '-A'], dir);
  commit('base');
  const baseSha = git(['rev-parse', 'HEAD'], dir);

  await write('e.txt', 'e v1\n');
  git(['add', 'e.txt'], dir);
  commit('another block lands e.txt');
  const headSha = git(['rev-parse', 'HEAD'], dir);

  await write('b.txt', 'b.txt side\n');
  git(['add', 'b.txt'], dir);
  const sideTree = git(['write-tree'], dir);
  const sideSha = git(['-c', 'commit.gpgsign=false', 'commit-tree', sideTree, '-p', baseSha, '-m', 'side'], dir);
  git(['reset', '-q', '--', 'b.txt'], dir);
  await write('b.txt', 'b.txt v1\n');

  await write('a.txt', 'a.txt dirty\n');
  await write('c.txt', 'c.txt dirty\n');
  await write('d.txt', 'd.txt untracked\n');
  return { baseSha, headSha, sideSha };
}

/**
 * The tree's changed + untracked files, parsed from real `git status --porcelain` output.
 * @param {string} dir
 * @returns {string[]}
 */
export function dirtyFiles(dir) {
  return git(['status', '--porcelain', '--untracked-files=all'], dir)
    .split('\n')
    .filter(Boolean)
    .map((line) => line.slice(3))
    .sort();
}
