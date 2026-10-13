/**
 * B108: the release moves the README's "In the next release:" bullets under "In X.Y.Z:".
 * The rewrite cases pin exact README bytes before and after (the shape of commit 589096f). The
 * end-to-end cases run `scripts/release.mjs` as a child process on a temp git repository with a
 * bare `origin` beside it, never on this repository; `npm` and `claude` are fakes on PATH.
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

// ── fixtures (written out, never derived from the code under test) ─────────────────────────

/** The pre-589096f shape: heading 0.9, the anchor with two bullets (one continued), then In 0.9.0. */
const PRE_090 = `# pkg

Intro.

## What's new in 0.9

In the next release:

- **Alpha (B1).** One.
- **Beta (B2).** Two.
  Continued.

In 0.9.0:

- **Old (B0).** Zero.

## What's new in 0.8

In 0.8.0:

- Older.
`;

/** The 589096f shape after `release minor` from 0.9.0. */
const POST_0100 = `# pkg

Intro.

## What's new in 0.10

In the next release:

In 0.10.0:

- **Alpha (B1).** One.
- **Beta (B2).** Two.
  Continued.

## What's new in 0.9

In 0.9.0:

- **Old (B0).** Zero.

## What's new in 0.8

In 0.8.0:

- Older.
`;

/** Package 0.10.0, one bullet waiting. */
const PRE_0100 = `# pkg

## What's new in 0.10

In the next release:

- **New (B5).** Five.

In 0.10.0:

- **Alpha (B1).** One.

## What's new in 0.9

In 0.9.0:

- Old.
`;

/** `release patch` (or 0.10.3): the heading stays, the new list goes above In 0.10.0. */
const samePost = (/** @type {string} */ v) => `# pkg

## What's new in 0.10

In the next release:

In ${v}:

- **New (B5).** Five.

In 0.10.0:

- **Alpha (B1).** One.

## What's new in 0.9

In 0.9.0:

- Old.
`;

/** A new major.minor: a new heading on top, the 0.10 heading moves above its own In 0.10.0. */
const newPost = (/** @type {string} */ xy, /** @type {string} */ v) => `# pkg

## What's new in ${xy}

In the next release:

In ${v}:

- **New (B5).** Five.

## What's new in 0.10

In 0.10.0:

- **Alpha (B1).** One.

## What's new in 0.9

In 0.9.0:

- Old.
`;

/** The land step's README insertion (plans/tools/land-block.mjs step 4), copied for the round trip. */
function land(/** @type {string} */ text, /** @type {string} */ bullet) {
  const lines = text.split('\n');
  let i = lines.indexOf('In the next release:');
  if (i < 0) throw new Error('README anchor missing');
  i += 1; while (lines[i] === '') i += 1;
  if (!lines[i]?.startsWith('- ')) lines.splice(i, 0, bullet, '');
  else { while (lines[i]?.startsWith('- ') || lines[i]?.startsWith('  ')) i += 1; lines.splice(i, 0, bullet); }
  return lines.join('\n');
}

describe('releaseReadme (B108)', () => {
  test('minor 0.9.0 → 0.10.0 writes the 589096f shape byte-for-byte', () => {
    assert.equal(releaseReadme(PRE_090, '0.9.0', '0.10.0'), POST_0100);
  });

  test('patch 0.10.0 → 0.10.1 keeps the heading and the anchor; In 0.10.1 goes above an unchanged In 0.10.0', () => {
    assert.equal(releaseReadme(PRE_0100, '0.10.0', '0.10.1'), samePost('0.10.1'));
  });

  test('major 0.10.0 → 1.0.0 adds "## What\'s new in 1.0"', () => {
    assert.equal(releaseReadme(PRE_0100, '0.10.0', '1.0.0'), newPost('1.0', '1.0.0'));
  });

  test('explicit x.y.z: 0.10.3 follows the patch rule; 0.11.2 adds "## What\'s new in 0.11" with In 0.11.2', () => {
    assert.equal(releaseReadme(PRE_0100, '0.10.0', '0.10.3'), samePost('0.10.3'));
    assert.equal(releaseReadme(PRE_0100, '0.10.0', '0.11.2'), newPost('0.11', '0.11.2'));
  });

  test('a new major.minor over a section with no "In x.y.z:" lists leaves no empty "## What\'s new" heading (list ends at "## " or at the end)', () => {
    const atHeading = "# pkg\n\n## What's new in 0.1\n\nIn the next release:\n\n- **First (B1).** One.\n\n## Install\n\nRun it.\n";
    assert.equal(releaseReadme(atHeading, '0.1.0', '0.2.0'),
      "# pkg\n\n## What's new in 0.2\n\nIn the next release:\n\nIn 0.2.0:\n\n- **First (B1).** One.\n\n## Install\n\nRun it.\n");
    const atEnd = "# pkg\n\n## What's new in 0.1\n\nIn the next release:\n\n- **First (B1).** One.\n";
    assert.equal(releaseReadme(atEnd, '0.1.0', '0.2.0'),
      "# pkg\n\n## What's new in 0.2\n\nIn the next release:\n\nIn 0.2.0:\n\n- **First (B1).** One.\n");
  });

  test('after every case: exactly 1 anchor, then a blank line and the new In X.Y.Z line; a landed bullet goes above it and moves at the next release', () => {
    const cases = /** @type {Array<[string, string, string]>} */ ([
      [PRE_090, '0.9.0', '0.10.0'], [PRE_0100, '0.10.0', '0.10.1'], [PRE_0100, '0.10.0', '1.0.0'],
      [PRE_0100, '0.10.0', '0.10.3'], [PRE_0100, '0.10.0', '0.11.2'],
    ]);
    for (const [text, current, next] of cases) {
      const out = releaseReadme(text, current, next);
      const lines = out.split('\n');
      const at = lines.flatMap((l, i) => (l === 'In the next release:' ? [i] : []));
      assert.equal(at.length, 1, next);
      assert.deepEqual(lines.slice(at[0], at[0] + 3), ['In the next release:', '', `In ${next}:`], next);

      const landed = land(out, '- **Later (B9).** Nine.');
      const ll = landed.split('\n');
      assert.deepEqual(ll.slice(at[0], at[0] + 5), ['In the next release:', '', '- **Later (B9).** Nine.', '', `In ${next}:`], next);
      const [M, m, p] = next.split('.').map(Number);
      const again = releaseReadme(landed, next, `${M}.${m}.${p + 1}`);
      const al = again.split('\n');
      const ai = al.indexOf('In the next release:');
      assert.deepEqual(al.slice(ai, ai + 7),
        ['In the next release:', '', `In ${M}.${m}.${p + 1}:`, '', '- **Later (B9).** Nine.', '', `In ${next}:`], next);
    }
  });
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

describe('scripts/release.mjs README step (B108)', () => {
  test('release minor from 0.9.0 commits the 589096f README shape with the 5 other files', () => {
    const { work } = makeRepo({ version: '0.9.0', readme: PRE_090 });
    const r = release(work, ['minor', '--date', '2026-10-13']);
    assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), POST_0100);
    assert.deepEqual(git(work, ['show', '--name-only', '--format=', 'HEAD']).trim().split('\n').sort(), [...FILES].sort());
    assert.equal(git(work, ['tag', '--list']).trim(), 'v0.10.0');
    assert.equal(state(work).status, '');
  });

  // [name, README (null = none), CHANGELOG, the exact refusal]
  const NO_ENTRIES = CHANGELOG.replace('### Added\n\n- A new verb.\n\n', '### Added\n\n');
  const refusals = /** @type {Array<[string, string | null, string, string]>} */ ([
    ['README missing', null, CHANGELOG,
      'README.md is missing or unreadable: the release moves its "In the next release:" bullets'],
    ['no anchor', PRE_0100.replace('In the next release:\n\n', ''), CHANGELOG,
      'README.md has no "In the next release:" line (the land step adds bullets after it): put it under "## What\'s new in X.Y"'],
    ['two anchors', PRE_0100.replace('In 0.10.0:', 'In the next release:'), CHANGELOG,
      'README.md has 2 "In the next release:" lines, expected exactly 1'],
    ['no What\'s new heading above the anchor', PRE_0100.replace("## What's new in 0.10", '## Usage'), CHANGELOG,
      'README.md: the nearest "## " heading above "In the next release:" (line 5) is not "## What\'s new in X.Y"'],
    ['heading 0.9 while the package is 0.10.0', PRE_0100.replace("## What's new in 0.10", "## What's new in 0.9"), CHANGELOG,
      'README.md: "In the next release:" sits under "## What\'s new in 0.9" but package.json is 0.10.0: expected "## What\'s new in 0.10"'],
    ['a non-bullet line in the list', PRE_0100.replace('- **New (B5).** Five.\n', '- **New (B5).** Five.\nSome prose.\n'), CHANGELOG,
      'README.md line 8: the "In the next release:" list may hold only "- " bullets, their "  " continuation lines and blank lines'],
    ['an empty list while [Unreleased] has entries', PRE_0100.replace('- **New (B5).** Five.\n\n', ''), CHANGELOG,
      'README.md: the "In the next release:" list is empty while CHANGELOG.md "## [Unreleased]" has entries: add a README bullet for each landed change'],
    ['an empty [Unreleased] while the list has bullets', PRE_0100, NO_ENTRIES,
      'CHANGELOG.md: "## [Unreleased]" has no entries: add one with npm run changelog -- added "…"; README.md has 1 bullet(s) under "In the next release:" with no CHANGELOG entry'],
  ]);

  for (const [name, readme, changelog, reason] of refusals) {
    test(`refused in preflight, exit 1, all 6 files byte-identical, no commit, no tag; --dry-run lists it too: ${name}`, () => {
      const { work, origin } = makeRepo({ version: '0.10.0', readme, changelog });
      const files = snapshot(work);
      assert.equal(files.filter((f) => f === null).length, readme === null ? 1 : 0);
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
    });
  }
});
