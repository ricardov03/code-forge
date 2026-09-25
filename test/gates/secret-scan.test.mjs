import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { chmod, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { scanFileContents, scanFiles, scanText } from '../../src/gates/secret-scan.mjs';
import { withTempDir } from './support.mjs';

/**
 * Every REAL-shaped secret token in this file is assembled at runtime from pieces, never written
 * as a literal — so no line of source here is itself a secret-shaped literal a scanner (this
 * package's own `gates` run, or GitHub push protection) could flag. The only secret-shaped
 * LITERALS in this file carry the FAKE/fake marker, per the suite-wide convention.
 */
const anthropicToken = ['sk-ant-', 'api03-', 'realLookingSecretValue123456'].join('');
const anthropicToken2 = ['sk-ant-', 'firstRealLookingToken1234'].join('');
const xaiToken = ['xai-', 'secondRealLookingToken123456789'].join('');
const awsToken = ['AKIA', '1234567890REALKEY'].join('');

test('secret-scan finds the planted real-shaped token (1 of 1) among FAKE-marked keys, which are all allowed', async () => {
  await withTempDir(async (dir) => {
    await writeFile(path.join(dir, 'fixture-keys.txt'), ['const a = "sk-ant-FAKE0123456789";', 'const b = "gho_FAKEtoken1234567890123";', ''].join('\n'));
    await writeFile(path.join(dir, 'leak.txt'), `Authorization: Bearer ${anthropicToken}\n`);
    const hits = await scanFiles(dir, ['fixture-keys.txt', 'leak.txt']);
    assert.equal(hits.length, 1);
    assert.equal(hits[0].path, 'leak.txt');
    assert.equal(hits[0].line, 1);
  });
});

test('scanText: 0 hits when every secret-shaped token on the line carries the FAKE marker — WITH a positive control proving the filter actually does something', () => {
  // Positive control first (finding: a deepEqual([]) assertion alone also passes if the pattern
  // simply stopped matching at all, which would prove nothing about the FAKE filter itself): the
  // SAME token shape, unmarked, must find exactly 1 hit with count 1.
  const unmarked = scanText(`key = ${anthropicToken}`);
  assert.equal(unmarked.length, 1);
  assert.equal(unmarked[0].count, 1);

  const upperMarked = scanText('key = sk-ant-FAKEabcdefghijklmnop');
  assert.deepEqual(upperMarked, []);
});

test('scanText: case-insensitive FAKE marker allowance ("fake" lowercase also exempts) — WITH the same positive control', () => {
  const unmarked = scanText(`key = ${anthropicToken}`);
  assert.equal(unmarked.length, 1);
  assert.equal(unmarked[0].count, 1);

  const lowerMarked = scanText('key = sk-ant-fake0123456789abcdef');
  assert.deepEqual(lowerMarked, []);
});

test('scanText counts multiple real tokens on one line', () => {
  const hits = scanText(`${anthropicToken2} and ${xaiToken}`);
  assert.equal(hits.length, 1);
  assert.equal(hits[0].count, 2);
});

test('scanFileContents is a pure in-memory variant of scanFiles', () => {
  const hits = scanFileContents([
    { path: 'a.txt', content: 'nothing here' },
    { path: 'b.txt', content: awsToken },
  ]);
  assert.deepEqual(hits, [{ path: 'b.txt', line: 1, count: 1 }]);
});

test('scanFiles skips a file that no longer exists rather than failing the whole scan (ENOENT only)', async () => {
  await withTempDir(async (dir) => {
    const hits = await scanFiles(dir, ['does-not-exist.txt']);
    assert.deepEqual(hits, []);
  });
});

test('scanFiles does NOT silently skip a real read error (a directory given as a file path) — the gate must turn red, not report a false green', async () => {
  await withTempDir(async (dir) => {
    await mkdir(path.join(dir, 'a-directory'));
    await assert.rejects(() => scanFiles(dir, ['a-directory']));
  });
});

test('scanFiles does NOT silently skip an unreadable file (chmod 000) — re-thrown, not treated as "nothing to scan"', async (t) => {
  if (process.getuid && process.getuid() === 0) {
    t.skip('running as root: chmod 000 does not block reads');
    return;
  }
  await withTempDir(async (dir) => {
    const filePath = path.join(dir, 'locked.txt');
    await writeFile(filePath, 'irrelevant');
    await chmod(filePath, 0o000);
    try {
      await assert.rejects(() => scanFiles(dir, ['locked.txt']));
    } finally {
      await chmod(filePath, 0o644); // restore so withTempDir's rm() can clean it up
    }
  });
});
