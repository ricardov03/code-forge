import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, test } from 'node:test';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SCRIPT = path.join(REPO, 'scripts/docs-block.mjs');
const PARENT = mkdtempSync(path.join(os.tmpdir(), 'docs-block-test-'));
/** @type {NodeJS.ProcessEnv} */
const ENV = { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull,
  GIT_AUTHOR_NAME: 'Test', GIT_AUTHOR_EMAIL: 'test@example.invalid',
  GIT_COMMITTER_NAME: 'Test', GIT_COMMITTER_EMAIL: 'test@example.invalid' };
for (const key of Object.keys(ENV)) if (key.startsWith('GIT_') && key !== 'GIT_CONFIG_NOSYSTEM' &&
  key !== 'GIT_CONFIG_GLOBAL' && !key.startsWith('GIT_AUTHOR_') && !key.startsWith('GIT_COMMITTER_')) delete ENV[key];
let counter = 0;

/** @param {string} cwd @param {string[]} args */
function git(cwd, args) {
  const r = spawnSync('git', args, { cwd, env: ENV, encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
  return r.stdout.trim();
}

/** @param {string} cwd @param {string[]} args @param {NodeJS.ProcessEnv} [extra] */
function docsBlock(cwd, args, extra = {}) {
  assert.equal(existsSync(SCRIPT), true, 'the docs-block maintainer command must be present');
  return spawnSync(process.execPath, [SCRIPT, ...args], {
    cwd, env: { ...ENV, CODE_FORGE_SITE_DIR: path.join(PARENT, 'absent'), ...extra }, encoding: 'utf8',
  });
}

/** @param {string} work @param {string} file @param {string} text */
function put(work, file, text) {
  mkdirSync(path.dirname(path.join(work, file)), { recursive: true });
  writeFileSync(path.join(work, file), text);
}

function makeRepo() {
  const dir = path.join(PARENT, `case-${++counter}`);
  const work = path.join(dir, 'work');
  mkdirSync(work, { recursive: true });
  git(work, ['init', '-q', '-b', 'main']);
  put(work, 'README.md', '# Example\n');
  put(work, 'CHANGELOG.md', '# Changes\n\n## [Unreleased]\n\n### Added\n\n- New feature.\n  Continued entry.\n\n## [0.1.0]\n\n- Old entry.\n');
  put(work, 'docs/guide.md', '# Guide\n');
  put(work, 'docs/reference/nested.md', '# Nested\n');
  put(work, 'docs/ignore.txt', 'not markdown\n');
  put(work, 'package.json', JSON.stringify({ name: '@example/pkg', version: '0.1.0' }, null, 2) + '\n');
  put(work, 'scripts/gen-config-doc.mjs', 'process.exit(0);\n');
  git(work, ['add', '-A']);
  git(work, ['commit', '-qm', 'old release']);
  git(work, ['tag', '-a', 'v0.1.0', '-m', 'release']);
  put(work, 'feature.mjs', 'export const feature = true;\n');
  git(work, ['add', '-A']);
  git(work, ['commit', '-qm', 'add feature']);
  const feature = git(work, ['rev-parse', '--short', 'HEAD']);
  put(work, 'docs/guide.md', '# Updated guide\n');
  git(work, ['commit', '-qam', 'document feature']);
  return { dir, work, feature, docs: git(work, ['rev-parse', '--short', 'HEAD']) };
}

/** Extract just the list under one heading. @param {string} output @param {string} heading */
const section = (output, heading) => output.split(`${heading}\n`)[1]?.split('\n\n')[0].split('\n');

after(() => rmSync(PARENT, { recursive: true, force: true }));

test('brief lists exactly the release delta, Unreleased, docs, checks and every site page', () => {
  const { dir, work, feature, docs } = makeRepo();
  const site = path.join(dir, 'custom site');
  for (const page of ['index.astro', 'guide.astro', 'pricing.astro', 'ignore.md', 'docs/x.astro', 'docs/ignore.md']) {
    put(site, `src/pages/${page}`, 'page\n');
  }
  const r = docsBlock(work, ['brief'], { CODE_FORGE_SITE_DIR: site });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stderr, '');
  assert.match(r.stdout, /^Latest release tag: v0\.1\.0$/m);
  assert.deepEqual(section(r.stdout, 'Commits since v0.1.0:'), [`- ${feature} add feature`, `- ${docs} document feature`]);
  assert.deepEqual(section(r.stdout, 'Files changed since v0.1.0:'), ['- docs/guide.md', '- feature.mjs']);
  assert.deepEqual(section(r.stdout, 'CHANGELOG [Unreleased]:'), ['### Added']);
  assert.equal(r.stdout.split('- New feature.\n  Continued entry.').length - 1, 1, 'one complete Unreleased entry');
  assert.equal(r.stdout.includes('Old entry.'), false, 'zero released entries');
  assert.deepEqual(section(r.stdout, 'Docs:'), ['- README.md', '- docs/guide.md', '- docs/reference/nested.md']);
  assert.deepEqual(section(r.stdout, 'Generated-doc checks:'), ['- node scripts/gen-config-doc.mjs --check', '- node scripts/gen-commands-doc.mjs --check']);
  assert.deepEqual(section(r.stdout, 'Site pages a person must check:'), ['- src/pages/docs/x.astro', '- src/pages/guide.astro', '- src/pages/index.astro', '- src/pages/pricing.astro']);
  assert.equal(git(work, ['status', '--porcelain']), '', 'brief writes zero files');
});

test('brief boundary: sibling site fallback, absent site line and no earlier release tag', () => {
  const { dir, work } = makeRepo();
  const site = path.join(dir, 'code-forge-site');
  put(site, 'src/pages/index.astro', 'page\n');
  const fallback = docsBlock(work, ['brief'], { CODE_FORGE_SITE_DIR: '' });
  assert.equal(fallback.status, 0, fallback.stderr);
  assert.deepEqual(section(fallback.stdout, 'Site pages a person must check:'), ['- src/pages/index.astro']);
  git(work, ['tag', '-d', 'v0.1.0']);
  const missing = docsBlock(work, ['brief']);
  assert.equal(missing.status, 0, missing.stderr);
  assert.match(missing.stdout, /^Latest release tag: \(none\)$/m);
  assert.equal(section(missing.stdout, 'Commits since repository start:').length, 3, 'all three commits without a release tag');
  assert.deepEqual(section(missing.stdout, 'Site pages a person must check:'), [`Site folder absent: ${path.join(PARENT, 'absent')}`]);
  assert.equal(missing.stderr, '');
});

test('stamp records exactly HEAD and latest tag; committing it permits the next release', () => {
  const { dir, work } = makeRepo();
  const head = git(work, ['rev-parse', 'HEAD']);
  const r = docsBlock(work, ['stamp']);
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout, 'docs/release-docs.json\n');
  assert.equal(r.stderr, '');
  assert.deepEqual(JSON.parse(readFileSync(path.join(work, 'docs/release-docs.json'), 'utf8')), { through: head, tag: 'v0.1.0' });
  assert.equal(git(work, ['rev-parse', 'HEAD']), head, 'stamp makes zero commits');
  git(work, ['add', 'docs/release-docs.json']);
  git(work, ['commit', '-qm', 'docs stamp']);
  const origin = path.join(dir, 'origin.git');
  git(work, ['init', '-q', '--bare', origin]);
  git(work, ['remote', 'add', 'origin', origin]);
  git(work, ['push', '-q', 'origin', 'main']);
  // Fake npm handles every release child locally, with no registry or model access.
  const bin = path.join(dir, 'bin');
  put(bin, 'npm', '#!/usr/bin/env node\nconst a = process.argv[2];\nif (a === "view") console.log("0.1.0");\nif (a === "pack") console.log(JSON.stringify([{name:"@example/pkg",version:"0.1.1",entryCount:1}]));\nif (a === "ls") console.log("{}");\n');
  put(bin, 'claude', '#!/usr/bin/env node\nprocess.exit(0);\n');
  chmodSync(path.join(bin, 'npm'), 0o755);
  chmodSync(path.join(bin, 'claude'), 0o755);
  const release = spawnSync(process.execPath, [path.join(REPO, 'scripts/release.mjs'), 'patch'], {
    cwd: work, env: { ...ENV, PATH: `${bin}${path.delimiter}${ENV.PATH}` }, encoding: 'utf8',
  });
  assert.equal(release.status, 0, release.stdout + release.stderr);
  assert.equal(git(work, ['log', '-1', '--format=%s']), 'Release v0.1.1');
  assert.equal(git(work, ['tag', '--list', 'v0.1.1']), 'v0.1.1');
});

test('stamp boundary: refuses tracked, staged and untracked dirt without writing a stamp', () => {
  for (const dirty of ['tracked', 'staged', 'untracked']) {
    const { work } = makeRepo();
    const head = git(work, ['rev-parse', 'HEAD']);
    put(work, dirty === 'untracked' ? 'stray.txt' : 'README.md', 'dirty\n');
    if (dirty === 'staged') git(work, ['add', 'README.md']);
    const before = git(work, ['status', '--porcelain']);
    const r = docsBlock(work, ['stamp']);
    assert.equal(r.status, 1, dirty);
    assert.match(r.stderr, /working tree is not clean/);
    assert.equal(r.stdout, '', 'zero success output on refusal');
    assert.equal(existsSync(path.join(work, 'docs/release-docs.json')), false, 'zero stamp files on refusal');
    assert.equal(git(work, ['rev-parse', 'HEAD']), head);
    assert.equal(git(work, ['status', '--porcelain']), before, 'dirty bytes and index preserved');
  }
});
