import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { chmod, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { runInvariants } from '../../src/gates/invariants.mjs';
import { withTempDir } from './support.mjs';

test('no configured invariants path ⇒ skipped and ok (a project with no invariants script has nothing extra to check)', async () => {
  const result = await runInvariants({ cwd: '/tmp' });
  assert.deepEqual(result, { skipped: true, ok: true, code: null, stdout: '', stderr: '' });
});

test('a configured invariants script that exits 0 ⇒ ok', async () => {
  await withTempDir(async (dir) => {
    const scriptPath = path.join(dir, 'invariants.sh');
    await writeFile(scriptPath, '#!/bin/sh\nexit 0\n');
    await chmod(scriptPath, 0o755);
    const result = await runInvariants({ cwd: dir, invariantsPath: scriptPath });
    assert.equal(result.skipped, false);
    assert.equal(result.ok, true);
    assert.equal(result.code, 0);
  });
});

test('a configured invariants script that exits 1 ⇒ not ok', async () => {
  await withTempDir(async (dir) => {
    const scriptPath = path.join(dir, 'invariants.sh');
    await writeFile(scriptPath, '#!/bin/sh\nexit 1\n');
    await chmod(scriptPath, 0o755);
    const result = await runInvariants({ cwd: dir, invariantsPath: scriptPath });
    assert.equal(result.ok, false);
    assert.equal(result.code, 1);
  });
});

test('runInvariants refuses a missing cwd', async () => {
  await assert.rejects(() => runInvariants(/** @type {any} */ ({})), TypeError);
});

test('a BARE-filename invariantsPath (no slash) resolves against the project root, not PATH', async () => {
  await withTempDir(async (dir) => {
    const scriptPath = path.join(dir, 'invariants.sh');
    await writeFile(scriptPath, '#!/bin/sh\nexit 0\n');
    await chmod(scriptPath, 0o755);
    // "invariants.sh" with no slash: the OS would otherwise look this up on PATH (ENOENT, since
    // it isn't installed anywhere), not find it in `cwd`.
    const result = await runInvariants({ cwd: dir, invariantsPath: 'invariants.sh' });
    assert.equal(result.skipped, false);
    assert.equal(result.ok, true);
    assert.equal(result.code, 0);
  });
});

test('a bare-filename invariantsPath that does NOT exist in the project root fails (proves it is not silently found elsewhere on PATH)', async () => {
  await withTempDir(async (dir) => {
    const result = await runInvariants({ cwd: dir, invariantsPath: 'does-not-exist.sh' });
    assert.equal(result.ok, false);
  });
});
