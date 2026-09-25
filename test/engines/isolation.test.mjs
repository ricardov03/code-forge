import assert from 'node:assert/strict';
import { readFile, readdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { cleanupIsolatedWorkspace, countWorkspaceEntries, createIsolatedWorkspace } from '../../src/engines/isolation.mjs';

/** @type {string} */
let baseDir;

before(async () => {
  baseDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-isolation-test-'));
});

after(async () => {
  await rm(baseDir, { recursive: true, force: true });
});

test('createIsolatedWorkspace makes a fresh dir holding EXACTLY 1 file — the packet', async () => {
  const { dir, filePath } = await createIsolatedWorkspace('packet.json', '{"ok":true}', { baseDir });
  const entries = await readdir(dir);
  assert.equal(entries.length, 1, `expected exactly 1 file, got ${entries.length}: ${entries.join(', ')}`);
  assert.equal(entries[0], 'packet.json');
  assert.equal(await countWorkspaceEntries(dir), 1);
  assert.equal(await readFile(filePath, 'utf8'), '{"ok":true}');
  // the dir is created DIRECTLY under baseDir — not silently written somewhere else (os.tmpdir()
  // itself, or a nested subfolder) (fix round 1, MINOR).
  assert.equal(path.dirname(dir), path.resolve(baseDir));
  await cleanupIsolatedWorkspace(dir, { baseDir });
});

// Fix round 1 (MINOR): a broken countWorkspaceEntries that always returned 1 would pass the test
// above too — this proves the function can actually report a DIFFERENT count.
test('countWorkspaceEntries reports 2 when a workspace really holds 2 files, and 0 for an empty dir', async () => {
  const { dir } = await createIsolatedWorkspace('packet.json', 'x', { baseDir });
  await writeFile(path.join(dir, 'second.json'), 'y', 'utf8');
  assert.equal(await countWorkspaceEntries(dir), 2);
  await cleanupIsolatedWorkspace(dir, { baseDir });

  const emptyDir = await mkdtemp(path.join(baseDir, 'code-forge-isolated-'));
  assert.equal(await countWorkspaceEntries(emptyDir), 0);
  await cleanupIsolatedWorkspace(emptyDir, { baseDir });
});

test('two isolated workspaces never collide (distinct dirs each holding their own 1 file)', async () => {
  const a = await createIsolatedWorkspace('packet.json', 'A', { baseDir });
  const b = await createIsolatedWorkspace('packet.json', 'B', { baseDir });
  assert.notEqual(a.dir, b.dir);
  assert.equal(await readFile(a.filePath, 'utf8'), 'A');
  assert.equal(await readFile(b.filePath, 'utf8'), 'B');
  assert.equal(await countWorkspaceEntries(a.dir), 1);
  assert.equal(await countWorkspaceEntries(b.dir), 1);
  await cleanupIsolatedWorkspace(a.dir, { baseDir });
  await cleanupIsolatedWorkspace(b.dir, { baseDir });
});

test('createIsolatedWorkspace refuses a fileName that is a path, not a basename, or "." / ".."', async () => {
  const before_ = await readdir(baseDir);
  await assert.rejects(() => createIsolatedWorkspace('../escape.json', 'x', { baseDir }), TypeError);
  await assert.rejects(() => createIsolatedWorkspace('sub/dir.json', 'x', { baseDir }), TypeError);
  await assert.rejects(() => createIsolatedWorkspace('', 'x', { baseDir }), TypeError);
  await assert.rejects(() => createIsolatedWorkspace('.', 'x', { baseDir }), TypeError);
  await assert.rejects(() => createIsolatedWorkspace('..', 'x', { baseDir }), TypeError);
  // fix round 1, MINOR: nothing was created or left behind by any of the refused calls — no new
  // entry in baseDir, and no stray file escaped to baseDir's own parent.
  assert.deepEqual(await readdir(baseDir), before_);
  assert.equal(
    await readFile(path.join(baseDir, '..', 'escape.json'), 'utf8').then(
      () => true,
      () => false,
    ),
    false,
  );
});

// Fix round 1 (MINOR): a writeFile failure after mkdtemp used to leak an empty
// code-forge-isolated-* directory. Simulated with a content value writeFile cannot accept (an
// object is not a valid fs.writeFile data argument and throws synchronously before any bytes move).
test('a writeFile failure after mkdtemp does not leak the empty temp directory', async () => {
  const before_ = await readdir(baseDir);
  await assert.rejects(() => createIsolatedWorkspace('packet.json', /** @type {any} */ ({ not: 'a string or buffer' }), { baseDir }));
  const after_ = await readdir(baseDir);
  assert.deepEqual(after_, before_, `a code-forge-isolated-* dir was leaked: before=${before_.join(',')} after=${after_.join(',')}`);
});

test('cleanupIsolatedWorkspace refuses a directory it did not create, and LEAVES IT UNTOUCHED', async () => {
  const foreign = await mkdtemp(path.join(baseDir, 'not-code-forge-isolated-'));
  await writeFile(path.join(foreign, 'marker.txt'), 'still here', 'utf8');
  await assert.rejects(() => cleanupIsolatedWorkspace(foreign, { baseDir }), TypeError);
  // fix round 1, MINOR: the old test only checked the throw — this proves nothing was deleted.
  assert.deepEqual(await readdir(foreign), ['marker.txt']);
  await rm(foreign, { recursive: true, force: true });
});

// Fix round 1 (MINOR): the old basename-prefix-only check would delete ANY directory anywhere on
// disk named `code-forge-isolated-*`, not only ones actually created under the expected baseDir.
test('cleanupIsolatedWorkspace refuses a correctly-named dir sitting under the WRONG parent', async () => {
  const wrongParent = await mkdtemp(path.join(os.tmpdir(), 'code-forge-isolation-OTHER-'));
  const lookalike = await mkdtemp(path.join(wrongParent, 'code-forge-isolated-'));
  await assert.rejects(() => cleanupIsolatedWorkspace(lookalike, { baseDir }), TypeError);
  assert.deepEqual(await readdir(lookalike), []); // untouched
  await rm(wrongParent, { recursive: true, force: true });
});

test('cleanupIsolatedWorkspace actually removes the directory it created (matching baseDir)', async () => {
  const { dir } = await createIsolatedWorkspace('packet.json', 'x', { baseDir });
  await cleanupIsolatedWorkspace(dir, { baseDir });
  await assert.rejects(() => readdir(dir), { code: 'ENOENT' });
});

test('cleanupIsolatedWorkspace with the DEFAULT baseDir (os.tmpdir()) works for a workspace also created with the default baseDir', async () => {
  const { dir } = await createIsolatedWorkspace('packet.json', 'x'); // no baseDir override: real os.tmpdir()
  assert.equal(path.dirname(dir), path.resolve(os.tmpdir()));
  await cleanupIsolatedWorkspace(dir); // no baseDir override either
  await assert.rejects(() => readdir(dir), { code: 'ENOENT' });
});
