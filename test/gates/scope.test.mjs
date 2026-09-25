import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { computeFileSet, diffNoIndexWithCount, ownsFile, scopeGate, trackedChangedFiles, untrackedFiles } from '../../src/gates/scope.mjs';
import { buildTestRepo, git, withTempDir } from './support.mjs';

test('file set includes 1 untracked file, and its --no-index diff has exactly 40 + lines', async () => {
  await withTempDir(async (dir) => {
    const { baseSha } = await buildTestRepo(dir);
    const lines = Array.from({ length: 40 }, (_, i) => `line ${i}`);
    await writeFile(path.join(dir, 'new-file.mjs'), `${lines.join('\n')}\n`);

    const fileSet = await computeFileSet({ cwd: dir, base: baseSha });
    assert.deepEqual(fileSet.untracked, ['new-file.mjs']);
    assert.deepEqual(fileSet.tracked, []);
    assert.deepEqual(fileSet.all, ['new-file.mjs']);

    const { plusCount } = await diffNoIndexWithCount(dir, 'new-file.mjs');
    assert.equal(plusCount, 40);
  });
});

test('trackedChangedFiles sees a modified tracked file, untrackedFiles does not', async () => {
  await withTempDir(async (dir) => {
    const { baseSha } = await buildTestRepo(dir);
    await writeFile(path.join(dir, 'a.test.mjs'), "import { test } from 'node:test';\ntest('one', () => {});\ntest('changed', () => {});\n");
    const tracked = await trackedChangedFiles(baseSha, dir);
    const untracked = await untrackedFiles(dir);
    assert.deepEqual(tracked, ['a.test.mjs']);
    assert.deepEqual(untracked, []);
  });
});

test('an inherited GIT_DIR (as a git hook would export) does not redirect the read', async () => {
  await withTempDir(async (dir) => {
    await withTempDir(async (otherRepoDir) => {
      git(['init', '-q'], otherRepoDir);
      await buildTestRepo(dir);
      await writeFile(path.join(dir, 'untracked.txt'), 'x\n');
      const original = process.env.GIT_DIR;
      process.env.GIT_DIR = path.join(otherRepoDir, '.git');
      try {
        // If GIT_* were inherited (not stripped), this would read otherRepoDir's (empty, no
        // commits) git dir against dir's worktree — a mismatch git either errors on or reports
        // wrong. Stripped, it correctly reports dir's own untracked file.
        const untracked = await untrackedFiles(dir);
        assert.deepEqual(untracked, ['untracked.txt']);
      } finally {
        if (original === undefined) delete process.env.GIT_DIR;
        else process.env.GIT_DIR = original;
      }
    });
  });
});

test('re-exports B8 registry.mjs scope maths (ownsFile, scopeGate) — one import point for O1+O2', () => {
  assert.equal(ownsFile(['src/gates/**'], 'src/gates/scope.mjs'), true);
  assert.equal(ownsFile(['src/gates/**'], 'src/other/x.mjs'), false);
  const gate = scopeGate({ B5: { owned_files: ['src/gates/**'] } }, 'B5', ['src/gates/scope.mjs', 'src/unowned.mjs']);
  assert.equal(gate.ok, false);
  assert.deepEqual(gate.orphans, ['src/unowned.mjs']);
});

test('trackedChangedFiles refuses an empty base', async () => {
  await assert.rejects(() => trackedChangedFiles('', '/tmp'), TypeError);
});

test('trackedChangedFiles refuses a base starting with "-" (argv option injection, e.g. --output=/some/path)', async () => {
  await assert.rejects(() => trackedChangedFiles('--output=/tmp/evil', '/tmp'), TypeError);
  await assert.rejects(() => trackedChangedFiles('--ext-diff', '/tmp'), TypeError);
});

test('untrackedFiles reports the EXACT path of a file whose name contains a space, not a C-quoted literal (git wraps such names in quotes without -z)', async () => {
  await withTempDir(async (dir) => {
    await buildTestRepo(dir);
    await writeFile(path.join(dir, 'a b.txt'), 'x\n');
    const untracked = await untrackedFiles(dir);
    assert.deepEqual(untracked, ['a b.txt']);
  });
});

test('diffNoIndexWithCount counts by POSITION (after the @@ hunk marker), not by a "+++"-prefix match: a content line that itself starts with "++" must still count as one "+" line, not be mistaken for a second header', async () => {
  await withTempDir(async (dir) => {
    await buildTestRepo(dir);
    const lines = [...Array.from({ length: 39 }, (_, i) => `line ${i}`), '++i; // a real content line starting with ++'];
    await writeFile(path.join(dir, 'plusplus.mjs'), `${lines.join('\n')}\n`);
    const { plusCount } = await diffNoIndexWithCount(dir, 'plusplus.mjs');
    assert.equal(plusCount, 40); // all 40 content lines, including the "++i;" one
  });
});
