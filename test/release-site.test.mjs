import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, test } from 'node:test';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const PARENT = mkdtempSync(path.join(os.tmpdir(), 'release-site-test-'));
const NPM = path.resolve(path.dirname(process.execPath), '../lib/node_modules/npm/bin/npm-cli.js');
writeFileSync(path.join(PARENT, 'package.json'), '{"type":"commonjs"}\n');
after(() => rmSync(PARENT, { recursive: true, force: true }));

/** @param {string} dir @param {string} file @param {string} text */
function put(dir, file, text) {
  mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
  writeFileSync(path.join(dir, file), text);
}

const FAKE_NPM = `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const a = process.argv.slice(2);
const log = (event) => fs.appendFileSync(process.env.EVENTS, JSON.stringify(event) + '\\n');
if (a[0] === 'view') {
  if (a.join(' ') !== 'view @example/pkg@0.21.0 version') process.exit(8);
  const count = fs.existsSync(process.env.POLLS) ? Number(fs.readFileSync(process.env.POLLS)) : 0;
  fs.writeFileSync(process.env.POLLS, String(count + 1));
  const served = !process.env.NEVER && count >= Number(process.env.DELAY || 0);
  log(served ? 'npm:served' : 'npm:missing');
  if (served) console.log('0.21.0');
  else { console.error('E404 not yet served'); process.exit(1); }
} else if (a.join(' ') === 'ci') {
  log('npm:ci:' + path.basename(process.cwd())); fs.mkdirSync('node_modules'); fs.writeFileSync('node_modules/dep.txt', 'installed');
} else if (a.join(' ') === 'run build:site') {
  log('build:site');
  // Like the real site: a nested package (docs/) builds with its own installed dependencies.
  if (fs.existsSync('docs/package.json') && !fs.existsSync('docs/node_modules/dep.txt')) {
    console.error('Cannot find package dep imported from docs'); process.exit(5);
  }
  for (const cache of ['.vite', '.astro', '.cache', '.vite-temp']) {
    fs.mkdirSync(path.join('node_modules', cache), {recursive: true});
    fs.writeFileSync(path.join('node_modules', cache, 'state'), 'new build cache');
  }
  fs.mkdirSync('dist', {recursive: true}); fs.writeFileSync('dist/index.html', 'new site');
  // A build changing unrelated tracked files must never put them in the release commit.
  fs.writeFileSync('README.md', 'build changed this file');
  if (process.env.FAIL_GATE === 'build:site') process.exit(4);
} else { console.error('unexpected npm ' + a.join(' ')); process.exit(9); }
`;

const FAKE_WRANGLER = `#!/usr/bin/env node
const fs = require('node:fs');
const cp = require('node:child_process');
const a = process.argv.slice(2);
if (a.join(' ') !== 'pages deploy dist --project-name code-forge-ai --branch main') process.exit(9);
const commit = cp.execFileSync('git', ['log', '-1', '--format=%s'], {encoding:'utf8'}).trim();
fs.appendFileSync(process.env.EVENTS, JSON.stringify('deploy:' + commit) + '\\n');
if (fs.readFileSync('dist/index.html', 'utf8') !== 'new site') process.exit(8);
console.log('Deployment complete: https://fixture.code-forge-ai.pages.dev');
`;

/** @param {{delay?: number, never?: boolean, fail?: string, modules?: boolean, nested?: boolean}} [opts] */
function fixture({ delay = 0, never = false, fail = '', modules = false, nested = false } = {}) {
  const root = mkdtempSync(path.join(PARENT, 'case-'));
  const work = path.join(root, 'package');
  const site = path.join(root, 'code-forge-site');
  const bin = path.join(root, 'bin');
  const env = { ...process.env, HOME: path.join(root, 'home'),
    PATH: `${bin}${path.delimiter}${process.env.PATH}`, EVENTS: path.join(root, 'events'),
    POLLS: path.join(root, 'polls'), DELAY: String(delay), NEVER: never ? '1' : '', FAIL_GATE: fail,
    GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull,
    GIT_AUTHOR_NAME: 'Test', GIT_AUTHOR_EMAIL: 'test@example.invalid',
    GIT_COMMITTER_NAME: 'Test', GIT_COMMITTER_EMAIL: 'test@example.invalid' };
  for (const key of Object.keys(env)) {
    if (key.startsWith('GIT_') && !key.startsWith('GIT_CONFIG_') && !key.startsWith('GIT_AUTHOR_') && !key.startsWith('GIT_COMMITTER_')) delete env[key];
  }
  /** @param {string} cwd @param {string[]} args */
  const git = (cwd, args) => {
    const r = spawnSync('git', args, { cwd, env, encoding: 'utf8' });
    assert.equal(r.status, 0, `fixture git ${args.join(' ')}: ${r.stderr}`);
    return r.stdout.trim();
  };
  for (const dir of [work, site, bin, env.HOME]) mkdirSync(dir, { recursive: true });
  const script = JSON.parse(readFileSync(path.join(REPO, 'package.json'), 'utf8')).scripts['release:site'];
  put(work, 'package.json', JSON.stringify({ name: '@example/pkg', version: '0.21.0', scripts: script ? { 'release:site': script } : {} }));
  // --if-present models the old release workflow: no site step, and no child crash.
  if (existsSync(path.join(REPO, 'scripts/release-site.mjs'))) {
    mkdirSync(path.join(work, 'scripts'), { recursive: true });
    symlinkSync(path.join(REPO, 'scripts/release-site.mjs'), path.join(work, 'scripts/release-site.mjs'));
  }
  put(work, 'scripts/gen-site-data.mjs', `
import fs from 'node:fs';
if (process.argv.slice(2).join(' ') !== '--tag v0.21.0') process.exit(9);
fs.appendFileSync(process.env.EVENTS, JSON.stringify('generate:v0.21.0') + '\\n');
console.log(JSON.stringify({version:'0.21.0', commands:[], changelog:[]}));
`);
  put(root, 'clock.mjs', `
if (process.argv[1]?.endsWith('/release-site.mjs')) {
  let now = Date.now(); Date.now = () => now;
  globalThis.setTimeout = (fn, ms) => {
    now += ms;
    return setImmediate(fn);
  };
}
`);
  env['NODE_OPTIONS'] = `--import=${path.join(root, 'clock.mjs')}`;
  for (const [name, content] of Object.entries({ npm: FAKE_NPM, wrangler: FAKE_WRANGLER,
    npx: '#!/bin/sh\n[ "$1" = wrangler ] || exit 9\nshift\nexec wrangler "$@"\n' })) {
    put(bin, name, content);
    chmodSync(path.join(bin, name), 0o755);
  }
  put(site, '.gitignore', 'node_modules/\ndist/\n.code-forge-tmp/\n');
  put(site, 'src/data/site-data.json', '{"version":"0.20.0"}\n');
  put(site, 'README.md', 'original site\n');
  put(site, 'package.json', '{"name":"fixture-site","version":"1.0.0"}\n');
  if (nested) put(site, 'docs/package.json', '{"name":"fixture-docs","version":"1.0.0"}\n');
  put(site, '.code-forge.yml', `gates:
  test: [node, gate.mjs, test]
  custom:
    - name: links
      argv: [node, gate.mjs, links]
      env: {GATE_MARKER: configured}
    - name: accessibility
      argv: [node, gate.mjs, accessibility]
`);
  put(site, 'gate.mjs', `
import fs from 'node:fs';
const name = process.argv[2];
if (name === 'links' && process.env.GATE_MARKER !== 'configured') process.exit(9);
if (JSON.parse(fs.readFileSync('src/data/site-data.json')).version !== '0.21.0') process.exit(8);
fs.appendFileSync(process.env.EVENTS, JSON.stringify('gate:' + name) + '\\n');
if (process.env.FAIL_GATE === name) process.exit(3);
`);
  for (const dir of [work, site]) {
    git(dir, ['init', '-q', '-b', 'main']);
    git(dir, ['add', '.']);
    git(dir, ['commit', '-qm', 'initial']);
  }
  git(work, ['tag', 'v0.21.0']);
  if (modules) {
    put(site, 'node_modules/local.txt', 'existing dependencies\n');
    put(site, 'node_modules/.vite/state', 'existing build cache\n');
    if (nested) put(site, 'docs/node_modules/dep.txt', 'existing docs dependencies\n');
  }
  const initialHead = git(site, ['rev-parse', 'HEAD']);
  /** @param {string[]} [args] */
  const run = (args = []) => spawnSync(process.execPath, [NPM, 'run', 'release:site', '--if-present', '--', ...args],
    { cwd: work, env, encoding: 'utf8', timeout: 20000 });
  const events = () => existsSync(env.EVENTS) ? readFileSync(env.EVENTS, 'utf8').trim().split('\n').map((s) => JSON.parse(s)) : [];
  return { root, work, site, run, events, git, initialHead };
}

/** @param {string} dir @returns {Record<string, string>} */
function snapshot(dir) {
  const out = {};
  for (const entry of readdirSync(dir)) {
    if (entry === '.git' || entry === '.code-forge-tmp') continue;
    const file = path.join(dir, entry);
    if (lstatSync(file).isSymbolicLink()) out[entry] = `link:${readlinkSync(file)}`;
    else if (lstatSync(file).isDirectory()) {
      for (const [name, text] of Object.entries(snapshot(file))) out[`${entry}/${name}`] = text;
    } else out[entry] = readFileSync(file).toString('base64');
  }
  return out;
}

test('waits for the exact npm version before generating, building, committing or deploying', () => {
  const f = fixture({ delay: 1 });
  const r = f.run();
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.deepEqual(f.events().slice(0, 3), ['npm:missing', 'npm:served', 'generate:v0.21.0'], 'two polls precede the one data generation');
  assert.equal(f.events().filter((e) => e.startsWith('deploy:')).length, 1, 'exactly one deploy after npm serves');
});

test('wait limit exits 1 with a clear timeout and changes no site files or refs', () => {
  const f = fixture({ never: true });
  const before = snapshot(f.site);
  const r = f.run(['--wait-min', '0.5']);
  assert.equal(r.status, 1, 'timeout exit code is exactly 1');
  assert.match(r.stderr, /Timed out after 0\.5 min waiting for npm to serve @example\/pkg@0\.21\.0; nothing changed/);
  assert.deepEqual(f.events(), ['npm:missing'], 'one poll, zero generation, gates or deploys');
  assert.deepEqual(snapshot(f.site), before);
  assert.equal(f.git(f.site, ['show-ref', '--heads']), `${f.initialHead} refs/heads/main`, 'zero new branches or commits');
  assert.equal(existsSync(path.join(f.site, '.code-forge-tmp')), false, 'no temporary worktree on timeout');
});

test('green gates commit only the data file on the release branch and deploy dist once', (t) => {
  const f = fixture();
  const start = performance.now();
  const r = f.run(['--site', f.site]);
  t.diagnostic(`release:site fake-tool path: ${(performance.now() - start).toFixed(0)} ms`);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.deepEqual(f.events(), ['npm:served', 'generate:v0.21.0', 'npm:ci:release-site-0.21.0', 'build:site', 'gate:links', 'gate:accessibility', 'gate:test', 'deploy:Release site v0.21.0']);
  assert.equal(f.git(f.site, ['diff-tree', '--no-commit-id', '--name-only', '-r', 'release-site/v0.21.0']), 'src/data/site-data.json', 'exactly one committed path');
  assert.equal(f.git(f.site, ['rev-list', '--count', 'main..release-site/v0.21.0']), '1', 'exactly one new commit');
  assert.equal(JSON.parse(f.git(f.site, ['show', 'release-site/v0.21.0:src/data/site-data.json'])).version, '0.21.0');
  assert.match(r.stdout, /https:\/\/fixture\.code-forge-ai\.pages\.dev/);
  assert.match(r.stdout, /merge release-site\/v0\.21\.0/);
  assert.match(r.stdout, /push origin main/);
  assert.equal(f.git(f.site, ['worktree', 'list', '--porcelain']).split('worktree ').length - 1, 1, 'only main worktree remains');
});

test('a red build, custom gate or test gate prevents every commit and deploy and removes the worktree', () => {
  for (const fail of ['build:site', 'links', 'test']) {
    const f = fixture({ fail });
    const r = f.run();
    assert.equal(r.status, 1, `${fail}: ${r.stdout}${r.stderr}`);
    assert.match(r.stderr, new RegExp(`Gate failed: ${fail}`));
    assert.equal(f.events().filter((e) => e.startsWith('deploy:')).length, 0, `${fail}: zero deploys`);
    assert.equal(f.git(f.site, ['show-ref', '--heads']), `${f.initialHead} refs/heads/main`, `${fail}: zero new commits or branches`);
    assert.equal(f.git(f.site, ['worktree', 'list', '--porcelain']).split('worktree ').length - 1, 1, `${fail}: zero temporary worktrees`);
  }
});

test('dirty main checkout, staged work, untracked bytes and existing node_modules stay identical', () => {
  const f = fixture({ modules: true });
  put(f.site, 'README.md', 'staged open work\n');
  f.git(f.site, ['add', 'README.md']);
  put(f.site, 'README.md', 'unstaged open work\n');
  put(f.site, 'src/data/site-data.json', 'unfinished data\n');
  put(f.site, 'open.bin', '\u0000untracked\u00ff\n');
  const bytes = snapshot(f.site);
  const index = readFileSync(path.join(f.site, '.git/index'));
  const status = f.git(f.site, ['status', '--porcelain']);
  const r = f.run();
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(f.events().filter((e) => e.startsWith('npm:ci')).length, 0, 'zero dependency installs when main node_modules exists');
  assert.equal(f.events().filter((e) => e.startsWith('deploy:')).length, 1, 'one deploy despite dirty main');
  assert.deepEqual(snapshot(f.site), bytes, 'every main checkout file stays byte-identical');
  assert.deepEqual(readFileSync(path.join(f.site, '.git/index')), index, 'staging index stays byte-identical');
  assert.equal(f.git(f.site, ['status', '--porcelain']), status);
  assert.equal(f.git(f.site, ['rev-parse', 'HEAD']), f.initialHead);
});

test('dry run executes generation and every gate but makes zero commits or deploys', () => {
  const f = fixture();
  const r = f.run(['--dry-run']);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.deepEqual(f.events(), ['npm:served', 'generate:v0.21.0', 'npm:ci:release-site-0.21.0', 'build:site', 'gate:links', 'gate:accessibility', 'gate:test']);
  assert.match(r.stdout, /Would commit src\/data\/site-data.json.*release-site\/v0.21.0/);
  assert.match(r.stdout, /npx wrangler pages deploy dist --project-name code-forge-ai --branch main/);
  assert.equal(f.git(f.site, ['show-ref', '--heads']), `${f.initialHead} refs/heads/main`);
  assert.equal(f.git(f.site, ['worktree', 'list', '--porcelain']).split('worktree ').length - 1, 1);
});

test('a nested site package builds with the main checkout dependencies linked, or installs its own when none exist', () => {
  const linked = fixture({ modules: true, nested: true });
  const bytes = snapshot(linked.site);
  const r = linked.run(['--dry-run']);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.deepEqual(linked.events(), ['npm:served', 'generate:v0.21.0', 'build:site', 'gate:links', 'gate:accessibility', 'gate:test'], 'zero installs, build green');
  assert.deepEqual(snapshot(linked.site), bytes, 'main checkout stays byte-identical');
  const fresh = fixture({ nested: true });
  const r2 = fresh.run(['--dry-run']);
  assert.equal(r2.status, 0, r2.stdout + r2.stderr);
  assert.deepEqual(fresh.events().filter((e) => e.startsWith('npm:ci')).sort(), ['npm:ci:docs', 'npm:ci:release-site-0.21.0'], 'one install per package');
  assert.equal(fresh.git(fresh.site, ['worktree', 'list', '--porcelain']).split('worktree ').length - 1, 1);
});
