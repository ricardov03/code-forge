/**
 * B23: `scripts/changelog.mjs`, run as a child process on a temp CHANGELOG.md (never this
 * repository's): add an entry, create a subsection or the `[Unreleased]` section, refuse bad
 * input, keep every other byte, and `list`.
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, describe, test } from 'node:test';

const SCRIPT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'scripts', 'changelog.mjs');
const PARENT = mkdtempSync(path.join(os.tmpdir(), 'changelog-test-'));
let counter = 0;
after(() => rmSync(PARENT, { recursive: true, force: true }));

const HEAD = `# Changelog

Intro line.

`;
const RELEASED = `## [0.1.0] — first release

### Added

- First.
`;
const BASE = `${HEAD}## [Unreleased]

### Added

- **One.** An entry that wraps onto a second line because it is long enough to pass the column
  the file uses.

### Fixed

- A fix.

${RELEASED}`;

/** @param {string} text */
function copy(text) {
  counter += 1;
  const dir = path.join(PARENT, `case-${counter}`);
  mkdirSync(dir);
  writeFileSync(path.join(dir, 'CHANGELOG.md'), text);
  return dir;
}
/** @param {string} dir @param {string[]} args */
function changelog(dir, args) {
  const r = spawnSync(process.execPath, [SCRIPT, ...args], { cwd: dir, encoding: 'utf8' });
  return { code: r.status, stdout: r.stdout, stderr: r.stderr, text: readFileSync(path.join(dir, 'CHANGELOG.md'), 'utf8') };
}

describe('scripts/changelog.mjs (B23)', () => {
  test('adds a bullet at the end of an existing subsection; every other byte is unchanged', () => {
    const dir = copy(BASE);
    const r = changelog(dir, ['added', 'Two.']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(r.stdout, 'added to [Unreleased] → Added\n');
    assert.equal(r.text, BASE.replace('  the file uses.\n', '  the file uses.\n- Two.\n'));
  });

  test('a new subsection goes in Keep-a-Changelog order (Changed between Added and Fixed; Security last)', () => {
    const dir = copy(BASE);
    assert.equal(changelog(dir, ['changed', 'Changed one.']).code, 0);
    let text = readFileSync(path.join(dir, 'CHANGELOG.md'), 'utf8');
    assert.equal(text, BASE.replace('### Fixed\n', '### Changed\n\n- Changed one.\n\n### Fixed\n'));
    const r = changelog(dir, ['SECURITY', 'Secured.']);
    assert.equal(r.code, 0, r.stderr);
    text = r.text;
    assert.equal(text, BASE.replace('### Fixed\n', '### Changed\n\n- Changed one.\n\n### Fixed\n')
      .replace('- A fix.\n', '- A fix.\n\n### Security\n\n- Secured.\n'));
  });

  test('a missing [Unreleased] section is created above the first release', () => {
    const dir = copy(`${HEAD}${RELEASED}`);
    const r = changelog(dir, ['removed', 'Gone.']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(r.text, `${HEAD}## [Unreleased]\n\n### Removed\n\n- Gone.\n\n${RELEASED}`);
  });

  test('long text wraps at the column the file already uses (its widest bullet line, 94 here), continuation indented two spaces', () => {
    const dir = copy(BASE);
    const text = `**Bold lead.** ${'word '.repeat(30).trim()}`;
    const r = changelog(dir, ['fixed', text]);
    assert.equal(r.code, 0, r.stderr);
    const added = r.text.split('\n').slice(r.text.split('\n').indexOf('- A fix.') + 1, r.text.split('\n').indexOf('- A fix.') + 3);
    assert.deepEqual(added, [
      `- **Bold lead.** ${'word '.repeat(15).trim()}`,
      `  ${'word '.repeat(15).trim()}`,
    ]);
    assert.equal(added[0].length, 91);
  });

  test('refuses a duplicate (exit 1), empty text and an unknown type (exit 2); the file is unchanged', () => {
    const dir = copy(BASE);
    const dup = changelog(dir, ['fixed', 'A fix.']);
    assert.equal(dup.code, 1);
    assert.equal(dup.stderr, 'REFUSED: "A fix." is already under Fixed in [Unreleased]\n');
    const wrapped = changelog(dir, ['added', '**One.** An entry that wraps onto a second line because it is long enough to pass the column the file uses.']);
    assert.equal(wrapped.code, 1);
    const empty = changelog(dir, ['added', '   ']);
    assert.equal(empty.code, 2);
    assert.match(empty.stderr, /^REFUSED: the entry text is empty\n/);
    const unknown = changelog(dir, ['improved', 'x']);
    assert.equal(unknown.code, 2);
    assert.match(unknown.stderr, /^REFUSED: unknown type "improved": use one of added, changed, deprecated, removed, fixed, security\n/);
    assert.equal(unknown.text, BASE);
  });

  test('list prints each [Unreleased] subsection with its count and one line per entry', () => {
    const dir = copy(BASE);
    const r = changelog(dir, ['list']);
    assert.equal(r.code, 0);
    assert.equal(
      r.stdout,
      'Added (1)\n  - **One.** An entry that wraps onto a second line because it is long enough to pass the column the file uses.\n' +
        'Fixed (1)\n  - A fix.\n',
    );
    assert.equal(changelog(copy(`${HEAD}${RELEASED}`), ['list']).stdout, '[Unreleased] has no entries\n');
  });
});
