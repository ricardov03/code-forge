/**
 * Test helpers for `test/gates/**`. Every `test/gates/*.test.mjs` file imports this module FIRST:
 * before any test — and before a mutated path function could run — `$HOME` points at a fresh temp
 * dir, so `gates.mjs`'s `--slug` ledger writes (`~/.code-forge/ledger/`, B6) never touch the real
 * `~/.code-forge` even from a stray hand-run.
 * Restored in `after()`.
 */

import { execFileSync } from 'node:child_process';
import { mkdtempSync, realpathSync, rmSync } from 'node:fs';
import { mkdtemp, realpath, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after } from 'node:test';

const GUARD_DIR = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b5-guard-')));
const ORIGINAL_HOME = process.env.HOME;
process.env.HOME = GUARD_DIR;
after(() => {
  if (ORIGINAL_HOME === undefined) delete process.env.HOME;
  else process.env.HOME = ORIGINAL_HOME;
  rmSync(GUARD_DIR, { recursive: true, force: true });
});

/**
 * @param {string[]} args @param {string} cwd @param {NodeJS.ProcessEnv} [extra]
 * @returns {string} trimmed stdout.
 */
export function git(args, cwd, extra = {}) {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  Object.assign(env, {
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: os.devNull,
    GIT_AUTHOR_NAME: 'B5 Fixture',
    GIT_AUTHOR_EMAIL: 'b5@example.test',
    GIT_COMMITTER_NAME: 'B5 Fixture',
    GIT_COMMITTER_EMAIL: 'b5@example.test',
    ...extra,
  });
  return execFileSync('git', args, { cwd, env, stdio: 'pipe', encoding: 'utf8' }).trimEnd();
}

/**
 * A throwaway git repo with one base commit holding `a.test.mjs` (2 `test(` declarations).
 * @param {string} dir - an existing empty directory
 * @returns {Promise<{baseSha: string}>}
 */
export async function buildTestRepo(dir) {
  git(['init', '-q'], dir);
  git(['symbolic-ref', 'HEAD', 'refs/heads/main'], dir);
  await writeFile(
    path.join(dir, 'a.test.mjs'),
    ["import { test } from 'node:test';", "test('one', () => {});", "test('two', () => {});", ''].join('\n'),
  );
  await writeFile(path.join(dir, 'b.test.mjs'), ["import { test } from 'node:test';", "test('three', () => {});", ''].join('\n'));
  git(['add', '-A'], dir);
  git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], dir);
  const baseSha = git(['rev-parse', 'HEAD'], dir);
  return { baseSha };
}

/** @returns {{write: (s: string) => boolean, text: string}} a stream double that records everything written to it. */
export function captureStream() {
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
 * Runs `fn` with a fresh temp workspace directory, removed afterward regardless of outcome.
 * @param {(dir: string) => Promise<void>} fn
 */
export async function withTempDir(fn) {
  const dir = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-b5-ws-')));
  try {
    await fn(dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}
