/**
 * README releases update only the optional Latest line. End-to-end cases use disposable git
 * repositories with a bare origin and fake npm/claude; no network or models are used.
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { accessSync, chmodSync, constants, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, describe, test } from 'node:test';

import { releaseReadme } from '../scripts/release.mjs';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SCRIPT = path.join(REPO, 'scripts', 'release.mjs');
const PARENT = mkdtempSync(path.join(os.tmpdir(), 'release-readme-test-'));
const FAKE_BIN = path.join(PARENT, 'bin');
let counter = 0;

// A legacy list deliberately remains in the fixture: releasing must never move its bullets.
const LEGACY = "\n## What's new in 0.10\n\nIn the next release:\n\n- New.\n  Continued.\n\nIn 0.10.0:\n\n- Old.\n";
const INTRO = '# pkg\n\nA useful tool.\n\n';
const LATEST = 'Latest: 0.10.0 - see [CHANGELOG.md](CHANGELOG.md)\n';

test('README has zero version-history lists and one linked Latest line in its first ten lines', () => {
  const text = readFileSync(path.join(REPO, 'README.md'), 'utf8');
  assert.equal([...text.matchAll(/^#+ What's new in \d+\.\d+/gm)].length, 0, 'version-history headings');
  assert.equal([...text.matchAll(/^In (?:\d+\.\d+\.\d+|the next release):/gm)].length, 0, 'release lists');
  const lines = text.split('\n');
  const latest = lines.filter((line) => line.startsWith('Latest:'));
  const version = JSON.parse(readFileSync(path.join(REPO, 'package.json'), 'utf8')).version;
  assert.deepEqual(latest, [`Latest: ${version} - see [CHANGELOG.md](CHANGELOG.md)`]);
  assert.ok(lines.indexOf(latest[0]) < 10, 'Latest belongs in the first ten lines');
});

// ── end to end ─────────────────────────────────────────────────────────────────────────────

/**
 * The first executable file `bin` on PATH, found in node: no shell string and no child process.
 * @param {string} bin @returns {string}
 */
function which(bin) {
  for (const dir of (process.env.PATH ?? '').split(path.delimiter)) {
    if (!dir) continue;
    const candidate = path.join(dir, bin);
    try {
      accessSync(candidate, constants.X_OK);
      if (statSync(candidate).isFile()) return candidate;
    } catch { /* not here */ }
  }
  throw new Error(`test/release-readme: no ${bin} on PATH`);
}

/** Every inherited `GIT_*` stripped, no system/global config, a fixed identity. */
function baseEnv() {
  const env = { ...process.env };
  for (const k of Object.keys(env)) if (k.startsWith('GIT_')) delete env[k];
  return {
    ...env,
    PATH: [FAKE_BIN, path.dirname(which('git')), path.dirname(process.execPath)].join(path.delimiter),
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: os.devNull,
    GIT_AUTHOR_NAME: 'Test', GIT_AUTHOR_EMAIL: 'test@example.invalid',
    GIT_COMMITTER_NAME: 'Test', GIT_COMMITTER_EMAIL: 'test@example.invalid',
  };
}
const ENV = baseEnv();

/** @param {string} cwd @param {string[]} args */
function git(cwd, args) {
  const r = spawnSync('git', args, { cwd, env: ENV, encoding: 'utf8', timeout: 60000 });
  assert.equal(r.status, 0, `git ${args.join(' ')}: ${r.stderr}`);
  return r.stdout;
}

/** @param {string} cwd @param {string[]} args */
function release(cwd, args) {
  const r = spawnSync(process.execPath, [SCRIPT, ...args], { cwd, env: ENV, encoding: 'utf8', timeout: 120000 });
  return { code: r.status, stdout: r.stdout, stderr: r.stderr };
}

const FAKE_NPM = `#!/usr/bin/env node
const fs = require('node:fs');
const a = process.argv.slice(2);
if (a[0] === 'view') { process.stdout.write('0.1.0\\n'); process.exit(0); }
if (a[0] === 'test') process.exit(0);
if (a[0] === 'run' && a[1] === 'typecheck') process.exit(0);
if (a[0] === 'pack') {
  const p = JSON.parse(fs.readFileSync('package.json', 'utf8'));
  process.stdout.write(JSON.stringify([{ name: p.name, version: p.version, entryCount: 3, files: [] }]));
  process.exit(0);
}
if (a[0] === 'ls') { process.stdout.write('{"name":"x","dependencies":{}}'); process.exit(0); }
process.stderr.write('fake npm: unexpected ' + a.join(' ') + '\\n'); process.exit(9);
`;
const FAKE_CLAUDE = '#!/bin/sh\nif [ "$1" = plugin ] && [ "$2" = validate ]; then exit 0; fi\nexit 9\n';

const CHANGELOG = '# Changelog\n\n## [Unreleased]\n\n### Added\n\n- A new verb.\n\n## [0.1.0] — first release\n\n- First.\n';
const json = (/** @type {unknown} */ o) => `${JSON.stringify(o, null, 2)}\n`;
const VERSION_FILES = ['package.json', 'npm-shrinkwrap.json', '.claude-plugin/plugin.json', 'skill/scripts/forge', 'CHANGELOG.md'];
const FILES = [...VERSION_FILES, 'README.md'];

/**
 * A fresh work repo at `version` with one pushed commit on main and a bare origin.
 * @param {{ version: string, readme: string | null, changelog?: string }} o - `readme: null` writes no README
 */
function makeRepo({ version, readme, changelog = CHANGELOG }) {
  counter += 1;
  const dir = path.join(PARENT, `case-${counter}`);
  const origin = path.join(dir, 'origin.git');
  const work = path.join(dir, 'work');
  mkdirSync(path.join(work, '.claude-plugin'), { recursive: true });
  mkdirSync(path.join(work, 'skill', 'scripts'), { recursive: true });
  mkdirSync(path.join(work, 'scripts'));
  git(dir, ['init', '-q', '--bare', '-b', 'main', origin]);
  git(work, ['init', '-q', '-b', 'main']);
  writeFileSync(path.join(work, 'package.json'), json({ name: '@example/pkg', version, type: 'module' }));
  writeFileSync(path.join(work, 'npm-shrinkwrap.json'), json({ name: '@example/pkg', version, lockfileVersion: 3,
    requires: true, packages: { '': { name: '@example/pkg', version } } }));
  writeFileSync(path.join(work, '.claude-plugin', 'plugin.json'), json({ name: 'pkg', version }));
  writeFileSync(path.join(work, 'skill', 'scripts', 'forge'), `#!/bin/sh\nPINNED_VERSION="${version}"\n`);
  writeFileSync(path.join(work, 'CHANGELOG.md'), changelog);
  writeFileSync(path.join(work, 'scripts', 'gen-config-doc.mjs'), "process.exit(process.argv.slice(2).join(' ') === '--check' ? 0 : 3);\n");
  if (readme !== null) writeFileSync(path.join(work, 'README.md'), readme);
  git(work, ['add', '-A']);
  git(work, ['commit', '-q', '-m', 'base']);
  git(work, ['remote', 'add', 'origin', origin]);
  git(work, ['push', '-q', 'origin', 'main']);
  return { origin, work };
}

/** Each file's bytes, or null when it does not exist. @param {string} work */
const snapshot = (work) => FILES.map((f) => (existsSync(path.join(work, f)) ? readFileSync(path.join(work, f)) : null));
/** @param {string} work */
const state = (work) => ({
  head: git(work, ['rev-parse', 'HEAD']).trim(),
  commits: Number(git(work, ['rev-list', '--count', 'HEAD']).trim()),
  tags: git(work, ['tag', '--list']).trim(),
  status: git(work, ['status', '--porcelain']),
});

before(() => {
  mkdirSync(FAKE_BIN);
  for (const [name, body] of [['npm', FAKE_NPM], ['claude', FAKE_CLAUDE]]) {
    writeFileSync(path.join(FAKE_BIN, name), body);
    chmodSync(path.join(FAKE_BIN, name), 0o755);
  }
});
after(() => rmSync(PARENT, { recursive: true, force: true }));

describe('scripts/release.mjs README step', () => {
  test('release changes only Latest for patch, minor, major and explicit versions; legacy bullets stay byte-identical', () => {
    for (const [bump, next] of [['patch', '0.10.1'], ['minor', '0.11.0'], ['major', '1.0.0'], ['0.10.3', '0.10.3'], ['0.11.2', '0.11.2']]) {
      const readme = INTRO + LATEST + LEGACY;
      const expected = INTRO + `Latest: ${next} - see [CHANGELOG.md](CHANGELOG.md)\n` + LEGACY;
      assert.equal(releaseReadme(readme, '0.10.0', next), expected, bump);
      const { work, origin } = makeRepo({ version: '0.10.0', readme });
      const files = snapshot(work);
      const st = state(work);
      const refs = git(origin, ['for-each-ref', '--format=%(refname) %(objectname)']);
      const dry = release(work, [bump, '--dry-run', '--date', '2026-10-15']);
      assert.equal(dry.code, 0, dry.stderr);
      assert.equal(dry.stdout.split('\n').filter((line) => line === `    + Latest: ${next} - see [CHANGELOG.md](CHANGELOG.md)`).length, 1, 'one Latest diff line');
      assert.deepEqual(snapshot(work), files, 'dry run changes zero bytes');
      assert.deepEqual(state(work), st, 'dry run changes zero refs');
      const r = release(work, [bump, '--date', '2026-10-15']);
      assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);
      assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), expected, bump);
      assert.deepEqual(git(work, ['show', '--name-only', '--format=', 'HEAD']).trim().split('\n').sort(), [...FILES].sort());
      assert.equal(state(work).commits, 2, 'exactly one release commit');
      assert.equal(state(work).tags, `v${next}`);
      assert.equal(git(work, ['cat-file', '-t', `v${next}`]).trim(), 'tag', 'annotated tag');
      assert.equal(state(work).status, '');
      assert.equal(git(origin, ['for-each-ref', '--format=%(refname) %(objectname)']), refs, 'no push');
    }
  });

  test('without Latest, anchor-free and legacy READMEs are unchanged and excluded from the release commit', () => {
    const variants = [
      INTRO,
      INTRO + LEGACY,
      INTRO + LEGACY.replace('In the next release:', 'In the next release:\nIn the next release:'),
      INTRO + LEGACY.replace("## What's new in 0.10", '## Usage'),
      INTRO + LEGACY.replace("## What's new in 0.10", "## What's new in 0.9"),
      INTRO + LEGACY.replace('- New.', 'Some prose.'),
      INTRO + LEGACY.replace('- New.\n  Continued.\n', ''),
    ];
    for (const readme of variants) {
      const { work } = makeRepo({ version: '0.10.0', readme });
      const r = release(work, ['patch']);
      assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);
      assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), readme, 'all README bytes preserved');
      assert.deepEqual(git(work, ['show', '--name-only', '--format=', 'HEAD']).trim().split('\n').sort(), [...VERSION_FILES].sort());
      assert.equal(state(work).commits, 2);
      assert.equal(state(work).tags, 'v0.10.1');
      assert.equal(state(work).status, '');
    }
  });

  test('missing README and empty Unreleased still refuse before any writes, including dry runs', () => {
    const empty = CHANGELOG.replace('### Added\n\n- A new verb.\n\n', '### Added\n\n');
    for (const [readme, changelog, reason] of [
      [null, CHANGELOG, 'README.md is missing or unreadable'],
      [INTRO, empty, 'CHANGELOG.md: "## [Unreleased]" has no entries: add one with npm run changelog -- added "…"'],
      [INTRO + LEGACY, empty, 'CHANGELOG.md: "## [Unreleased]" has no entries: add one with npm run changelog -- added "…"'],
    ]) {
      const { work, origin } = makeRepo({ version: '0.10.0', readme, changelog });
      const files = snapshot(work);
      const st = state(work);
      const refs = git(origin, ['for-each-ref', '--format=%(refname) %(objectname)']);
      for (const args of [['patch'], ['patch', '--dry-run']]) {
        const r = release(work, args);
        assert.equal(r.code, 1, args.join(' '));
        assert.equal(r.stderr, `\nREFUSED: ${reason}\n`, args.join(' '));
        assert.deepEqual(snapshot(work), files);
        assert.deepEqual(state(work), st);
        assert.equal(st.tags, '');
        assert.equal(git(origin, ['for-each-ref', '--format=%(refname) %(objectname)']), refs);
      }
    }
  });
});
