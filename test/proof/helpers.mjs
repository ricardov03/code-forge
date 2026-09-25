/**
 * Test helpers for `test/proof/**`. The preload (`test/helpers/isolate.mjs`) already pins HOME,
 * TMPDIR and the cwd to this process's temp root; every repo a test builds lives under ONE
 * `mkdtemp` parent per test file, removed in `after()`.
 *
 * The `export-ignore` fixture is built fresh from the plain files under
 * `test/fixtures/repos/export-ignore/` (never a committed nested `.git`):
 *
 *   base commit: tree/** + `.gitignore` + `.code-forge.yml` (a complete, valid config) + the tracked
 *                symlinked dir `lib -> src` (relative target, made here with `symlinkSync`; the
 *                committed fixture holds no symlink) (+ `.gitattributes` = `docs/ export-ignore`
 *                unless `attributes: false`)
 *   working tree after base (block A owns src/owned-{a,b}.mjs, block B owns other/**):
 *     src/owned-a.mjs modified, src/owned-b.mjs new          — block A
 *     other/shared.mjs modified, other/new.mjs new           — block B (the "other block's dirty files")
 *     node_modules/dep/index.js, .env, .env.testing          — untracked (git-ignored)
 */

import { execFileSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after } from 'node:test';

export const FIXTURE = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'repos', 'export-ignore');

export const SLUG = 'export-ignore';
export const OWNED_A = Object.freeze(['src/owned-a.mjs', 'src/owned-b.mjs']);
export const OWNED_B = Object.freeze(['other/**']);

export const CONTENT = Object.freeze({
  ownedA: "export const ownedA = 'owned-a CURRENT';\n",
  ownedB: "export const ownedB = 'owned-b CURRENT (new)';\n",
  sharedDirty: "export const shared = 'shared DIRTY by block B';\n",
  otherNew: "export const otherNew = 'new file of block B';\n",
  env: 'APP_KEY=FAKE-not-a-secret\n',
  envTesting: 'DB_PASSWORD=FAKE-testing\n',
});

export const CONFIG_YAML = [
  'version: 1',
  'project:',
  `  slug: ${SLUG}`,
  'provider: anthropic',
  'engine: harness',
  'levels:',
  '  L0: {model: claude-haiku-4-5-20251001}',
  '  L1: {model: claude-sonnet-5}',
  '  L2: {model: claude-opus-5-5}',
  '  L3: {model: claude-fable-5-1}',
  'proof:',
  '  export:',
  '    copy_untracked: [".env"]',
  '',
].join('\n');

/** The per-file temp parent (created at import, removed after the file's tests). */
export const TEMP_PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b10a-')));
after(() => rmSync(TEMP_PARENT, { recursive: true, force: true }));

/** @param {string} name @returns {string} a fresh directory under the temp parent */
export function tempDir(name) {
  const dir = path.join(TEMP_PARENT, name);
  mkdirSync(dir, { recursive: false });
  return dir;
}

/** git with every inherited GIT_* stripped, no system/global config, a fixed identity. */
export function git(/** @type {string[]} */ args, /** @type {string} */ cwd) {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  Object.assign(env, {
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: os.devNull,
    GIT_AUTHOR_NAME: 'B10a Fixture',
    GIT_AUTHOR_EMAIL: 'b10a@example.test',
    GIT_COMMITTER_NAME: 'B10a Fixture',
    GIT_COMMITTER_EMAIL: 'b10a@example.test',
  });
  return execFileSync('git', args, { cwd, env, stdio: 'pipe', encoding: 'utf8' }).trimEnd();
}

/**
 * @param {string} dir - an existing empty directory
 * @param {{attributes?: boolean}} [opts]
 * @returns {{baseSha: string}}
 */
export function buildExportIgnoreRepo(dir, { attributes = true } = {}) {
  const write = (/** @type {string} */ rel, /** @type {string} */ text) => {
    mkdirSync(path.dirname(path.join(dir, rel)), { recursive: true });
    writeFileSync(path.join(dir, rel), text);
  };
  cpSync(path.join(FIXTURE, 'tree'), dir, { recursive: true });
  write('.gitignore', '.code-forge/\nnode_modules/\n.env\n.env.testing\n');
  write('.code-forge.yml', CONFIG_YAML);
  symlinkSync('src', path.join(dir, 'lib'), 'dir');
  if (attributes) write('.gitattributes', readFileSync(path.join(FIXTURE, 'gitattributes.txt'), 'utf8'));
  git(['init', '-q'], dir);
  git(['symbolic-ref', 'HEAD', 'refs/heads/main'], dir);
  git(['add', '-A'], dir);
  git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], dir);
  const baseSha = git(['rev-parse', 'HEAD'], dir);

  write('src/owned-a.mjs', CONTENT.ownedA);
  write('src/owned-b.mjs', CONTENT.ownedB);
  write('other/shared.mjs', CONTENT.sharedDirty);
  write('other/new.mjs', CONTENT.otherNew);
  write('node_modules/dep/index.js', 'module.exports = 1;\n');
  write('.env', CONTENT.env);
  write('.env.testing', CONTENT.envTesting);
  return { baseSha };
}

/** @param {string} dir @param {string} rel @returns {string} */
export const readIn = (dir, rel) => readFileSync(path.join(dir, rel), 'utf8');

/** @param {string} rel @returns {string} a fixture file's committed content */
export const fixtureText = (rel) => readFileSync(path.join(FIXTURE, 'tree', rel), 'utf8');

/** @returns {{write: (s: string) => boolean, text: string}} */
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
