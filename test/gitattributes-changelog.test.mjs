/**
 * B396: the tracked `.gitattributes` makes CHANGELOG.md merge with git's union driver, so two
 * blocks that each add a line at the top of `## [Unreleased]` land without a rebase conflict.
 * Each case builds a throwaway git repo under os.tmpdir (the isolate preload points it at the
 * test root), copies this repository's `.gitattributes` in (or not), and merges two branches.
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, describe, test } from 'node:test';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const PARENT = mkdtempSync(path.join(os.tmpdir(), 'gitattributes-test-'));
after(() => rmSync(PARENT, { recursive: true, force: true }));

const BASE = `# Changelog

## [Unreleased]

### Changed

- Old entry (B1)
`;
const LINE_A = '- Entry from block A (B900)';
const LINE_B = '- Entry from block B (B901)';

function git(cwd, ...args) {
  const r = spawnSync(
    'git',
    ['-c', 'user.name=t', '-c', 'user.email=t@t', '-c', 'commit.gpgsign=false', '-c', 'init.defaultBranch=main', ...args],
    { cwd, encoding: 'utf8', env: { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull } },
  );
  return { status: r.status, out: `${r.stdout}${r.stderr}` };
}

function ok(cwd, ...args) {
  const r = git(cwd, ...args);
  assert.equal(r.status, 0, `git ${args.join(' ')} failed:\n${r.out}`);
  return r.out;
}

function addTop(dir, line) {
  const file = path.join(dir, 'CHANGELOG.md');
  writeFileSync(file, readFileSync(file, 'utf8').replace('### Changed\n\n', `### Changed\n\n${line}\n`));
}

/** Two branches each add a different top line; returns the merge result and the merged file. */
function mergeTwoBranches(name, withAttributes) {
  const dir = path.join(PARENT, name);
  ok(PARENT, 'init', '-q', dir);
  writeFileSync(path.join(dir, 'CHANGELOG.md'), BASE);
  // A missing repo file is caught by the first test; here it shows as a conflicting merge.
  if (withAttributes && existsSync(path.join(REPO, '.gitattributes'))) copyFileSync(path.join(REPO, '.gitattributes'), path.join(dir, '.gitattributes'));
  ok(dir, 'add', '-A');
  ok(dir, 'commit', '-q', '-m', 'base');
  ok(dir, 'switch', '-q', '-c', 'a');
  addTop(dir, LINE_A);
  ok(dir, 'commit', '-q', '-am', 'a');
  ok(dir, 'switch', '-q', 'main');
  ok(dir, 'switch', '-q', '-c', 'b');
  addTop(dir, LINE_B);
  ok(dir, 'commit', '-q', '-am', 'b');
  const merged = git(dir, 'merge', '--no-edit', 'a');
  return { merged, text: readFileSync(path.join(dir, 'CHANGELOG.md'), 'utf8') };
}

const count = (text, line) => text.split('\n').filter((l) => l === line).length;

describe('B396: CHANGELOG.md merge=union', () => {
  test('the repo .gitattributes has the line CHANGELOG.md merge=union', () => {
    const file = path.join(REPO, '.gitattributes');
    assert.ok(existsSync(file), '.gitattributes is missing at the repo root');
    const lines = readFileSync(file, 'utf8').split('\n').map((l) => l.trim());
    assert.equal(lines.filter((l) => l === 'CHANGELOG.md merge=union').length, 1);
  });

  test('with the repo .gitattributes, two top-line additions merge clean and keep both lines once', () => {
    const { merged, text } = mergeTwoBranches('with', true);
    assert.equal(merged.status, 0, `merge failed:\n${merged.out}`);
    assert.doesNotMatch(text, /^(<<<<<<<|=======|>>>>>>>)/m, 'conflict markers left in CHANGELOG.md');
    assert.equal(count(text, LINE_A), 1);
    assert.equal(count(text, LINE_B), 1);
    assert.equal(count(text, '- Old entry (B1)'), 1);
    assert.equal(count(text, '## [Unreleased]'), 1);
  });

  test('without .gitattributes the same merge conflicts (baseline)', () => {
    const { merged, text } = mergeTwoBranches('without', false);
    assert.notEqual(merged.status, 0, 'merge unexpectedly clean without .gitattributes');
    assert.match(merged.out, /CONFLICT \(content\): Merge conflict in CHANGELOG\.md/);
    assert.match(text, /^<<<<<<< /m);
  });

  test('releasing docs explain the union merge and the npm package does not pack .gitattributes', () => {
    const doc = readFileSync(path.join(REPO, 'docs', 'releasing.md'), 'utf8');
    assert.match(doc, /`CHANGELOG\.md merge=union`/);
    const files = JSON.parse(readFileSync(path.join(REPO, 'package.json'), 'utf8')).files;
    assert.equal(files.filter((f) => f.replace(/^!/, '').includes('.gitattributes')).length, 0);
  });
});
