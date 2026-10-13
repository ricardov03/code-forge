/**
 * B23: `scripts/release.mjs`, run end to end as a child process. Every test works on a temp git
 * repository with a bare `origin` beside it, never on this repository; `npm`, `gh` and `claude`
 * are fakes on PATH (the fake `npm` never publishes and answers `view` with a fixed version).
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { accessSync, chmodSync, constants, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, describe, test } from 'node:test';
import { parse } from 'yaml';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SCRIPT = path.join(REPO, 'scripts', 'release.mjs');
const PARENT = mkdtempSync(path.join(os.tmpdir(), 'release-test-'));
const FAKE_BIN = path.join(PARENT, 'bin');
let counter = 0;

/**
 * The first executable file `bin` on PATH, found in node: no shell and no child process.
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
  throw new Error(`test/release: no ${bin} on PATH`);
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

/** @param {string} cwd @param {string[]} args @param {Record<string, string>} [extraEnv] */
function release(cwd, args, extraEnv = {}) {
  const r = spawnSync(process.execPath, [SCRIPT, ...args], { cwd, env: { ...ENV, ...extraEnv }, encoding: 'utf8', timeout: 120000 });
  return { code: r.status, stdout: r.stdout, stderr: r.stderr };
}

const FAKE_NPM = `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const a = process.argv.slice(2);
// FAKE_NPM_SEEN=<dir>: copy the release files as this check sees them into <dir>/<check>/
const seen = (check) => {
  const dir = process.env.FAKE_NPM_SEEN;
  if (!dir) return;
  fs.mkdirSync(path.join(dir, check), { recursive: true });
  for (const f of ['README.md', 'CHANGELOG.md', 'package.json', 'package-lock.json']) {
    if (fs.existsSync(f)) fs.copyFileSync(f, path.join(dir, check, f));
  }
};
if (a[0] === 'view') { process.stdout.write((process.env.FAKE_NPM_VIEW || '0.1.0') + '\\n'); process.exit(0); }
if (a[0] === 'test') { seen('test'); process.stdout.write('fake npm test\\n'); process.exit(Number(process.env.FAKE_NPM_TEST_EXIT || 0)); }
if (a[0] === 'run' && a[1] === 'typecheck') { seen('typecheck'); process.exit(Number(process.env.FAKE_NPM_TYPECHECK_EXIT || 0)); }
if (a[0] === 'pack') {
  const p = JSON.parse(fs.readFileSync('package.json', 'utf8'));
  process.stdout.write(JSON.stringify([{ name: p.name, version: p.version, entryCount: 3, files: [] }]));
  process.exit(0);
}
if (a[0] === 'ls') { process.stdout.write('{"name":"x","dependencies":{}}'); process.exit(0); }
process.stderr.write('fake npm: unexpected ' + a.join(' ') + '\\n'); process.exit(9);
`;
const FAKE_GH = `#!/bin/sh
echo "fake gh: unexpected $*" >&2; exit 9
`;
const FAKE_CLAUDE = `#!/bin/sh
if [ "$1" = plugin ] && [ "$2" = validate ]; then echo "Validation passed"; exit 0; fi
exit 9
`;

const CHANGELOG = `# Changelog

## [Unreleased]

### Added

- A new verb.

## [0.1.0] — first release

- First.
`;
const PKG = `${JSON.stringify({ name: '@example/pkg', version: '0.1.0', type: 'module',
  repository: { type: 'git', url: 'git+https://github.com/example/pkg.git' } }, null, 2)}\n`;
const SHRINKWRAP = `${JSON.stringify({ name: '@example/pkg', version: '0.1.0', lockfileVersion: 3, requires: true,
  packages: { '': { name: '@example/pkg', version: '0.1.0' } } }, null, 2)}\n`;
const PLUGIN = `${JSON.stringify({ name: 'pkg', version: '0.1.0' }, null, 2)}\n`;
const SHIM = '#!/bin/sh\n# shim: PINNED_VERSION="0.1.0" is set below\nPINNED_VERSION="0.1.0"\nset -eu\necho "npx pkg@$PINNED_VERSION"\n';
const NOTES_020 = '### Added\n\n- A new verb.\n\nnpm: https://www.npmjs.com/package/@example/pkg/v/0.2.0\n' +
  'Full changelog: https://github.com/example/pkg/blob/v0.2.0/CHANGELOG.md';
const FILES = ['package.json', 'npm-shrinkwrap.json', '.claude-plugin/plugin.json', 'skill/scripts/forge', 'CHANGELOG.md', 'README.md'];
const README = "# pkg\n\n## What's new in 0.1\n\nIn the next release:\n\n- A new verb.\n\nIn 0.1.0:\n\n- First.\n";
/** The README after a 0.1.0 → 0.2.0 release (written out, not derived). */
const README_020 = "# pkg\n\n## What's new in 0.2\n\nIn the next release:\n\nIn 0.2.0:\n\n- A new verb.\n\n" +
  "## What's new in 0.1\n\nIn 0.1.0:\n\n- First.\n";
/** The README after a 0.1.0 → 0.1.1 patch (written out, not derived): the heading stays. */
const README_011 = "# pkg\n\n## What's new in 0.1\n\nIn the next release:\n\nIn 0.1.1:\n\n- A new verb.\n\nIn 0.1.0:\n\n- First.\n";
const LOCK = JSON.stringify({ name: '@example/pkg', version: '0.1.0', lockfileVersion: 3, requires: true,
  packages: { '': { name: '@example/pkg', version: '0.1.0' } } }, null, 2) + '\n';
/** `## [Unreleased]` with an empty `### Added`. */
const NO_ENTRIES = '# Changelog\n\n## [Unreleased]\n\n### Added\n\n## [0.1.0] — first release\n\n- First.\n';
/** Every file a snapshot holds: the release files and a `package-lock.json` (null when absent). */
const SNAP_FILES = [...FILES, 'package-lock.json'];
/** A fake config-doc generator: exit 0 only for `--check` (exit 3 otherwise, so a missing flag fails). */
const GEN_DOC_OK = "process.exit(process.argv.slice(2).join(' ') === '--check' ? 0 : 3);\n";
const GEN_DOC_STALE = "process.stderr.write('gen-config-doc --check: docs/reference/config.md is stale (fake)\\n'); process.exit(1);\n";

/**
 * A fresh work repo with one pushed commit on main and a bare origin.
 * @param {{ changelog?: string, genDoc?: string | null, lock?: boolean }} [o] - `genDoc: null` writes no
 *   scripts/gen-config-doc.mjs; `lock: true` also commits a package-lock.json
 */
function makeRepo({ changelog = CHANGELOG, genDoc = GEN_DOC_OK, lock = false } = {}) {
  counter += 1;
  const dir = path.join(PARENT, `case-${counter}`);
  const origin = path.join(dir, 'origin.git');
  const work = path.join(dir, 'work');
  mkdirSync(work, { recursive: true });
  git(dir, ['init', '-q', '--bare', '-b', 'main', origin]);
  git(work, ['init', '-q', '-b', 'main']);
  mkdirSync(path.join(work, '.claude-plugin'));
  mkdirSync(path.join(work, 'skill', 'scripts'), { recursive: true });
  writeFileSync(path.join(work, 'package.json'), PKG);
  writeFileSync(path.join(work, 'npm-shrinkwrap.json'), SHRINKWRAP);
  writeFileSync(path.join(work, '.claude-plugin', 'plugin.json'), PLUGIN);
  writeFileSync(path.join(work, 'skill', 'scripts', 'forge'), SHIM);
  writeFileSync(path.join(work, 'CHANGELOG.md'), changelog);
  writeFileSync(path.join(work, 'README.md'), README);
  if (lock) writeFileSync(path.join(work, 'package-lock.json'), LOCK);
  if (genDoc !== null) {
    mkdirSync(path.join(work, 'scripts'));
    writeFileSync(path.join(work, 'scripts', 'gen-config-doc.mjs'), genDoc);
  }
  git(work, ['add', '-A']);
  git(work, ['commit', '-q', '-m', 'base']);
  git(work, ['remote', 'add', 'origin', origin]);
  git(work, ['push', '-q', 'origin', 'main']);
  return { dir, origin, work };
}

/** Each file's bytes, or null when it does not exist. @param {string} work */
const snapshot = (work) => SNAP_FILES.map((f) => (existsSync(path.join(work, f)) ? readFileSync(path.join(work, f)) : null));
/** @param {string} work */
const state = (work) => ({
  head: git(work, ['rev-parse', 'HEAD']).trim(),
  commits: Number(git(work, ['rev-list', '--count', 'HEAD']).trim()),
  tags: git(work, ['tag', '--list']).trim(),
  status: git(work, ['status', '--porcelain']),
});
/** @param {string} origin */
const originRefs = (origin) => git(origin, ['for-each-ref', '--format=%(refname) %(objectname)']);

before(() => {
  mkdirSync(FAKE_BIN);
  for (const [name, body] of [['npm', FAKE_NPM], ['gh', FAKE_GH], ['claude', FAKE_CLAUDE]]) {
    writeFileSync(path.join(FAKE_BIN, name), body);
    chmodSync(path.join(FAKE_BIN, name), 0o755);
  }
});
after(() => rmSync(PARENT, { recursive: true, force: true }));

describe('scripts/release.mjs (B23)', () => {
  test('--dry-run shows 0.1.0 → 0.2.0 and the README diff, and changes 0 files, 0 commits, 0 tags; the same release then passes', () => {
    const { work, origin } = makeRepo();
    const files = snapshot(work);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor', '--dry-run', '--date', '2026-09-26']);
    assert.equal(r.code, 0, r.stderr);
    assert.match(r.stdout, /0\.1\.0 → 0\.2\.0/);
    assert.match(r.stdout, /\+ ## \[0\.2\.0\] — 2026-09-26/);
    assert.match(r.stdout, /\+ PINNED_VERSION="0\.2\.0"/);
    // B108: the README entry and its whole diff, line for line, are the last plan entry
    assert.equal(r.stdout.split('\nREADME.md: ').length - 1, 1, r.stdout);
    assert.equal(r.stdout.slice(r.stdout.indexOf('\nREADME.md: ') + 1, r.stdout.indexOf('\n== plan (dry run) ==\n')), [
      'README.md: "In the next release:" → "In 0.2.0:"',
      '      # pkg', '      ',
      "    - ## What's new in 0.1", '    - ', '    - In the next release:', '    - ', '    - - A new verb.',
      "    + ## What's new in 0.2", '    + ', '    + In the next release:', '    + ', '    + In 0.2.0:', '    + ',
      '    + - A new verb.', '    + ', "    + ## What's new in 0.1",
      '      ', '      In 0.1.0:', '      ', '',
    ].join('\n'));
    // the key lines, each exactly once in the whole output
    for (const key of ["    + ## What's new in 0.2", '    + In 0.2.0:', "    + ## What's new in 0.1", '    + - A new verb.']) {
      assert.equal(r.stdout.split('\n').filter((l) => l === key).length, 1, key);
    }
    assert.match(r.stdout, /^would run the checks: gen-config-doc --check, npm test, npm run typecheck, npm pack --dry-run, npm ls --json, claude plugin validate \.$/m);
    assert.match(r.stdout, /^would commit "Release v0\.2\.0" \(6 files\)/m);
    // nothing written: every file byte-equal, no commit, no tag, origin untouched
    assert.deepEqual(snapshot(work), files);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README);
    assert.deepEqual(state(work), st);
    assert.equal(st.tags, '');
    assert.equal(originRefs(origin), refs);
    // a release that passes: the same command without --dry-run succeeds and writes the README the diff showed
    const real = release(work, ['minor', '--date', '2026-09-26']);
    assert.equal(real.code, 0, `${real.stdout}\n${real.stderr}`);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README_020);
  });

  test('minor: 0.2.0 in all 4 locations, CHANGELOG and README moved, 1 commit of 6 files, 1 annotated tag, nothing pushed', () => {
    const { work, origin } = makeRepo();
    const before = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor', '--date', '2026-09-26']);
    assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);

    assert.equal(readFileSync(path.join(work, 'package.json'), 'utf8'), PKG.replace('"0.1.0"', '"0.2.0"'));
    assert.equal(readFileSync(path.join(work, 'npm-shrinkwrap.json'), 'utf8'), SHRINKWRAP.replaceAll('"0.1.0"', '"0.2.0"'));
    assert.equal(readFileSync(path.join(work, '.claude-plugin', 'plugin.json'), 'utf8'), PLUGIN.replace('"0.1.0"', '"0.2.0"'));
    assert.equal(readFileSync(path.join(work, 'skill', 'scripts', 'forge'), 'utf8'), SHIM.replace('\nPINNED_VERSION="0.1.0"\n', '\nPINNED_VERSION="0.2.0"\n'));
    assert.equal(
      readFileSync(path.join(work, 'CHANGELOG.md'), 'utf8'),
      CHANGELOG.replace('## [Unreleased]\n', '## [Unreleased]\n\n## [0.2.0] — 2026-09-26\n'),
    );
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README_020);
    assert.match(r.stdout, /^ok {4}gen-config-doc --check$/m);

    const after = state(work);
    assert.equal(after.commits, before.commits + 1);
    assert.equal(git(work, ['log', '-1', '--format=%s']).trim(), 'Release v0.2.0');
    assert.equal(git(work, ['show', '--name-only', '--format=', 'HEAD']).trim().split('\n').sort().join(','), [...FILES].sort().join(','));
    assert.equal(after.status, '');
    assert.equal(after.tags, 'v0.2.0');
    assert.equal(git(work, ['cat-file', '-t', 'v0.2.0']).trim(), 'tag');
    assert.equal(git(work, ['rev-parse', 'v0.2.0^{commit}']).trim(), after.head);
    const tagObject = git(work, ['cat-file', 'tag', 'v0.2.0']);
    assert.equal(tagObject.slice(tagObject.indexOf('\n\n') + 2), 'Release v0.2.0\n\n### Added\n\n- A new verb.\n');

    // one path: publish by hand, then push (CI never publishes; the tag push only makes the GitHub release)
    assert.match(r.stdout, /^ {2}publish to npm first, then push \(the tag push only creates the GitHub release; CI never publishes\):\n {6}npm publish\n {6}git push origin main --follow-tags$/m);
    assert.equal(r.stdout.split('git push origin main --follow-tags').length - 1, 1);
    assert.equal(r.stdout.includes('NPM_TOKEN'), false);
    const cmd = /^ {6}(gh release create .*)$/m.exec(r.stdout)?.[1];
    const notes = /--notes-file (\S+)/.exec(cmd)?.[1];
    assert.equal(cmd, `gh release create v0.2.0 --title v0.2.0 --notes-file ${notes} --verify-tag`);
    assert.equal(readFileSync(notes, 'utf8'), `${NOTES_020}\n`);
    assert.equal(originRefs(origin), refs);
    assert.equal(git(origin, ['tag', '--list']).trim(), '');
  });

  test('an explicit version not greater than the current one is refused (equal and lower)', () => {
    const { work } = makeRepo();
    for (const v of ['0.1.0', '0.0.9']) {
      const r = release(work, [v]);
      assert.equal(r.code, 1);
      assert.match(r.stderr, new RegExp(`REFUSED: the new version ${v.replaceAll('.', '\\.')} is not greater than the current 0\\.1\\.0`));
    }
    const r = release(work, ['0.1.5'], { FAKE_NPM_VIEW: '0.3.0' });
    assert.equal(r.code, 1);
    assert.match(r.stderr, /not greater than the latest on npm \(0\.3\.0\)/);
    assert.equal(state(work).commits, 1);
  });

  test('a dirty tree is refused', () => {
    const { work } = makeRepo();
    writeFileSync(path.join(work, 'stray.txt'), 'x');
    const r = release(work, ['patch']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /REFUSED: the working tree is not clean:\n\?\? stray\.txt/);
    assert.equal(readFileSync(path.join(work, 'package.json'), 'utf8'), PKG);
  });

  test('behind origin is refused', () => {
    const { dir, work, origin } = makeRepo();
    const other = path.join(dir, 'other');
    git(dir, ['clone', '-q', origin, other]);
    writeFileSync(path.join(other, 'new.txt'), 'x');
    git(other, ['add', 'new.txt']);
    git(other, ['commit', '-q', '-m', 'upstream']);
    git(other, ['push', '-q', 'origin', 'main']);
    const r = release(work, ['patch']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /REFUSED: main is behind origin\/main/);
  });

  test('an existing tag is refused, local or only on origin', () => {
    const local = makeRepo();
    git(local.work, ['tag', 'v0.1.1']);
    let r = release(local.work, ['patch']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /REFUSED: the tag v0\.1\.1 already exists locally/);

    const remote = makeRepo();
    const other = path.join(remote.dir, 'other');
    git(remote.dir, ['clone', '-q', remote.origin, other]);
    git(other, ['tag', 'v0.1.1']);
    git(other, ['push', '-q', 'origin', 'v0.1.1']);
    r = release(remote.work, ['patch']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /REFUSED: the tag v0\.1\.1 already exists on origin/);
  });

  test('an empty [Unreleased] is refused in preflight: README, CHANGELOG and the package files byte-unchanged; HEAD and tags unchanged', () => {
    const { work, origin } = makeRepo({ changelog: NO_ENTRIES, lock: true });
    const files = snapshot(work);
    assert.equal(files.filter((b) => b === null).length, 0);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['patch']);
    assert.equal(r.code, 1);
    // B108: the README still has its bullet, so the refusal names it too
    assert.equal(r.stderr, '\nREFUSED: CHANGELOG.md: "## [Unreleased]" has no entries: add one with npm run changelog -- added "…"; ' +
      'README.md has 1 bullet(s) under "In the next release:" with no CHANGELOG entry\n');
    // all 7 files byte-for-byte as before, and each of the B108 ones against its written-out text
    assert.deepEqual(snapshot(work), files);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README);
    assert.equal(readFileSync(path.join(work, 'CHANGELOG.md'), 'utf8'), NO_ENTRIES);
    assert.equal(readFileSync(path.join(work, 'package.json'), 'utf8'), PKG);
    assert.equal(readFileSync(path.join(work, 'npm-shrinkwrap.json'), 'utf8'), SHRINKWRAP);
    assert.equal(readFileSync(path.join(work, 'package-lock.json'), 'utf8'), LOCK);
    // the same HEAD, still 1 commit, no tag, a clean tree; origin untouched
    assert.deepEqual(state(work), st);
    assert.equal(st.commits, 1);
    assert.equal(st.tags, '');
    assert.equal(st.status, '');
    assert.equal(originRefs(origin), refs);
  });

  test('a failing check (npm test exits 1) rolls back all 6 files byte-for-byte: no commit, no tag', () => {
    const { work, origin } = makeRepo();
    const files = snapshot(work);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor'], { FAKE_NPM_TEST_EXIT: '1' });
    assert.equal(r.code, 1);
    assert.match(r.stderr, /FAILED: npm test failed \(exit 1\)/);
    assert.match(r.stderr, /rolled back 6 files; nothing was committed or tagged/);
    assert.deepEqual(snapshot(work), files);
    assert.deepEqual(state(work), st);
    assert.equal(originRefs(origin), refs);
  });

  test('B108: a check that fails after README.md and the other files were rewritten (npm test or npm run typecheck exits 1) restores README, CHANGELOG, package.json and package-lock.json byte-for-byte: 7 files, no commit, no tag', () => {
    const cases = [
      ['test', 'FAKE_NPM_TEST_EXIT', 'npm test failed (exit 1):\nfake npm test'],
      ['typecheck', 'FAKE_NPM_TYPECHECK_EXIT', 'npm run typecheck failed (exit 1)'],
    ];
    for (const [check, exitVar, why] of cases) {
      const { dir, work, origin } = makeRepo({ lock: true });
      const files = snapshot(work);
      assert.equal(files.filter((b) => b === null).length, 0);
      const st = state(work);
      const refs = originRefs(origin);
      const seen = path.join(dir, 'seen');
      const r = release(work, ['minor', '--date', '2026-09-26'], { [exitVar]: '1', FAKE_NPM_SEEN: seen });
      assert.equal(r.code, 1, `${check}: ${r.stdout}\n${r.stderr}`);
      assert.equal(r.stderr, `\nFAILED: ${why}\nrolled back 7 files; nothing was committed or tagged\n`);
      // the failing check ran on the rewritten files (the fake npm copied them as it saw them)
      const saw = (/** @type {string} */ f) => readFileSync(path.join(seen, check, f), 'utf8');
      assert.equal(saw('README.md'), README_020, check);
      assert.equal(saw('CHANGELOG.md'), CHANGELOG.replace('## [Unreleased]\n', '## [Unreleased]\n\n## [0.2.0] — 2026-09-26\n'), check);
      assert.equal(saw('package.json'), PKG.replace('"0.1.0"', '"0.2.0"'), check);
      assert.equal(saw('package-lock.json'), LOCK.replaceAll('"0.1.0"', '"0.2.0"'), check);
      // after the failure: all 7 files byte-for-byte as before, the 4 named ones against their written-out text
      assert.deepEqual(snapshot(work), files, check);
      assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README, check);
      assert.equal(readFileSync(path.join(work, 'CHANGELOG.md'), 'utf8'), CHANGELOG, check);
      assert.equal(readFileSync(path.join(work, 'package.json'), 'utf8'), PKG, check);
      assert.equal(readFileSync(path.join(work, 'package-lock.json'), 'utf8'), LOCK, check);
      // no commit, no tag, a clean tree, origin untouched
      assert.deepEqual(state(work), st, check);
      assert.equal(st.tags, '');
      assert.equal(originRefs(origin), refs, check);
    }
  });

  test('B108: a stale config doc (gen-config-doc --check exits 1) or no script fails the checks: 6 files rolled back, README byte-identical, no commit, no tag', () => {
    const cases = /** @type {Array<[string | null, string]>} */ ([
      [GEN_DOC_STALE, 'gen-config-doc --check failed (exit 1):\ngen-config-doc --check: docs/reference/config.md is stale (fake)'],
      [null, 'gen-config-doc --check failed: scripts/gen-config-doc.mjs not found'],
    ]);
    for (const [genDoc, why] of cases) {
      const { work, origin } = makeRepo({ genDoc });
      const files = snapshot(work);
      const st = state(work);
      const refs = originRefs(origin);
      const r = release(work, ['minor']);
      assert.equal(r.code, 1);
      assert.equal(r.stderr, `\nFAILED: ${why}\nrolled back 6 files; nothing was committed or tagged\n`);
      assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README);
      assert.deepEqual(snapshot(work), files);
      assert.deepEqual(state(work), st);
      assert.equal(st.tags, '');
      assert.equal(originRefs(origin), refs);
    }
  });

  test('notes <vX.Y.Z> prints the section plus the npm and changelog links; --out writes it; a missing version exits 1', () => {
    const { work } = makeRepo();
    let r = release(work, ['notes', 'v0.1.0']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(r.stdout, '- First.\n\nnpm: https://www.npmjs.com/package/@example/pkg/v/0.1.0\n' +
      'Full changelog: https://github.com/example/pkg/blob/v0.1.0/CHANGELOG.md\n');
    r = release(work, ['notes', 'v0.1.0', '--out', 'notes.md']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(readFileSync(path.join(work, 'notes.md'), 'utf8'), '- First.\n\nnpm: https://www.npmjs.com/package/@example/pkg/v/0.1.0\n' +
      'Full changelog: https://github.com/example/pkg/blob/v0.1.0/CHANGELOG.md\n');
    r = release(work, ['notes', 'v0.9.0']);
    assert.equal(r.code, 1);
    assert.equal(r.stderr, 'REFUSED: CHANGELOG.md has no "## [0.9.0]" section\n');
  });

  test('release.yml: one github-release job with exactly {contents: write}; it never publishes to npm; every run block parses', () => {
    const raw = readFileSync(path.join(REPO, '.github', 'workflows', 'release.yml'), 'utf8');
    assert.equal(existsSync(path.join(REPO, '.github', 'workflows', 'publish.yml')), false);
    const wf = parse(raw);
    assert.deepEqual(wf.on, { push: { tags: ['v*'] } });
    assert.deepEqual(Object.keys(wf.jobs), ['github-release']);
    const job = wf.jobs['github-release'];
    assert.deepEqual(job.permissions, { contents: 'write' });
    for (const word of ['npm publish', 'NPM_TOKEN', 'id-token', 'NODE_AUTH_TOKEN']) assert.equal(raw.includes(word), false, word);
    const runs = job.steps.filter((st) => st.run).map((st) => st.run);
    assert.equal(runs.length, 3);
    assert.match(runs[0], /if \[ "v\$\{version\}" != "\$\{GITHUB_REF_NAME\}" \]; then/);
    assert.equal(runs[1], 'node scripts/release.mjs notes "$GITHUB_REF_NAME" --out notes.md');
    assert.match(runs[2], /gh release create "\$GITHUB_REF_NAME" --title "\$GITHUB_REF_NAME" --notes-file notes\.md --verify-tag/);
    assert.match(runs[2], /gh release edit "\$GITHUB_REF_NAME"/);
    assert.equal(job.steps.find((st) => st.run === runs[2]).env.GH_TOKEN, '${{ github.token }}');
  });

  test('the shim bump is a line edit: every other byte is the same and exactly 1 PINNED_VERSION line remains', () => {
    const { work } = makeRepo();
    const r = release(work, ['patch', '--no-checks']);
    assert.equal(r.code, 0, r.stderr);
    const shim = readFileSync(path.join(work, 'skill', 'scripts', 'forge'), 'utf8');
    assert.equal(shim, '#!/bin/sh\n# shim: PINNED_VERSION="0.1.0" is set below\nPINNED_VERSION="0.1.1"\nset -eu\necho "npx pkg@$PINNED_VERSION"\n');
    assert.equal(shim.split('\n').filter((l) => l.startsWith('PINNED_VERSION=')).length, 1);
    assert.match(r.stdout, /--no-checks: gen-config-doc --check, npm test, typecheck, npm pack, npm ls and plugin validate were NOT run/);
  });

  test('B108: patch 0.1.0 → 0.1.1 keeps "## What\'s new in 0.1"; "In 0.1.1:" goes under the kept, empty anchor, above "In 0.1.0:" (exact bytes); 1 commit of 6 files, 1 tag', () => {
    const { work, origin } = makeRepo();
    const before = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['patch', '--date', '2026-09-26']);
    assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README_011);
    assert.equal(
      readFileSync(path.join(work, 'CHANGELOG.md'), 'utf8'),
      CHANGELOG.replace('## [Unreleased]\n', '## [Unreleased]\n\n## [0.1.1] — 2026-09-26\n'),
    );
    const after = state(work);
    assert.equal(after.commits, before.commits + 1);
    assert.equal(git(work, ['log', '-1', '--format=%s']).trim(), 'Release v0.1.1');
    assert.equal(git(work, ['show', '--name-only', '--format=', 'HEAD']).trim().split('\n').sort().join(','), [...FILES].sort().join(','));
    assert.equal(git(work, ['show', 'HEAD:README.md']), README_011);
    assert.equal(after.status, '');
    assert.equal(after.tags, 'v0.1.1');
    assert.equal(originRefs(origin), refs);
  });

  test('a second PINNED_VERSION line is refused and nothing changes', () => {
    const { work } = makeRepo();
    writeFileSync(path.join(work, 'skill', 'scripts', 'forge'), `${SHIM}PINNED_VERSION="0.1.0"\n`);
    git(work, ['commit', '-q', '-am', 'two pins']);
    const st = state(work);
    const r = release(work, ['patch']);
    assert.equal(r.code, 1);
    assert.equal(r.stderr, '\nREFUSED: skill/scripts/forge: expected exactly 1 PINNED_VERSION="…" line, found 2\n');
    assert.deepEqual(state(work), st);
  });

  test('on a feature branch the release is refused; nothing changes', () => {
    const { work, origin } = makeRepo();
    git(work, ['checkout', '-q', '-b', 'feat']);
    const files = snapshot(work);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor']);
    assert.equal(r.code, 1);
    assert.equal(r.stderr, '\nREFUSED: on branch "feat", expected "main" (use --allow-branch feat to release from it)\n');
    assert.deepEqual(snapshot(work), files);
    assert.deepEqual(state(work), st);
    assert.equal(originRefs(origin), refs);
  });

  test('--allow-branch feat gets past the branch check (dry run)', () => {
    const { work } = makeRepo();
    git(work, ['checkout', '-q', '-b', 'feat']);
    git(work, ['push', '-q', 'origin', 'feat']);
    const st = state(work);
    const r = release(work, ['minor', '--dry-run', '--allow-branch', 'feat']);
    assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);
    assert.match(r.stdout, /^ok {4}on branch feat$/m);
    assert.match(r.stdout, /^ok {4}up to date with origin\/feat$/m);
    assert.match(r.stdout, /^would print: git push origin feat --follow-tags$/m);
    assert.deepEqual(state(work), st);
  });

  test('npm pack --dry-run ships neither release tool but still ships scripts/first-use.mjs', () => {
    const res = spawnSync('npm', ['pack', '--dry-run', '--json'], { cwd: REPO, encoding: 'utf8', timeout: 120000 });
    assert.equal(res.status, 0, res.stderr);
    const paths = JSON.parse(res.stdout)[0].files.map((/** @type {{path: string}} */ f) => f.path);
    assert.deepEqual(paths.filter((p) => p.startsWith('scripts/')).sort(), ['scripts/first-use.mjs', 'scripts/gen-config-doc.mjs']);
  });
});

test('setJsonFields refuses a non-canonical JSON layout instead of reformatting unrelated lines', async () => {
  const { setJsonFields } = await import('../scripts/release.mjs');
  const inline = '{\n  "version": "0.1.0",\n  "files": ["bin", "src"]\n}\n';
  assert.throws(() => setJsonFields(inline, [{ keys: ['version'], value: '0.2.0' }]), /not in canonical JSON\.stringify layout/);
  const canonical = '{\n  "version": "0.1.0",\n  "files": [\n    "bin",\n    "src"\n  ]\n}\n';
  assert.equal(setJsonFields(canonical, [{ keys: ['version'], value: '0.2.0' }]), canonical.replace('"0.1.0"', '"0.2.0"'));
});
