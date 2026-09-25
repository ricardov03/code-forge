import assert from 'node:assert/strict';
import { symlink } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { sink, tempDir, writeMarker } from './helpers.mjs';
import { runList } from '../../src/cli/list.mjs';
import { install, installsPath, writeInstallsAtomically } from '../../src/install/link.mjs';

/** @param {string} home @param {string[]} [args] */
function run(home, args = []) {
  const stdout = sink();
  const stderr = sink();
  return runList(args, { stdout, stderr, home, env: {} }).then((code) => ({ code, stdout, stderr }));
}

test('list with an unexpected argument is a usage error, exit 2', async () => {
  const home = await tempDir('cf-home-');
  const { code, stderr } = await run(home, ['whatever']);
  assert.equal(code, 2);
  assert.equal(stderr.text, 'usage: code-forge list\n');
});

const KNOWN_HARNESS_IDS = new Set(['claude', 'codex', 'grok', 'gemini', 'cursor', 'copilot']);

/** @param {string} stdout */
function harnessRowIds(stdout) {
  return stdout
    .split('\n')
    .map((line) => line.split('\t')[0])
    .filter((first) => KNOWN_HARNESS_IDS.has(first));
}

test('list on a fresh HOME with nothing installed prints "(none)" under INSTALLED and all 6 harness rows, no duplicates', async () => {
  const home = await tempDir('cf-home-');
  const { code, stdout } = await run(home);
  assert.equal(code, 0);
  assert.match(stdout.text, /^INSTALLED\n/);
  assert.match(stdout.text, /\(none\)\n/);
  const ids = harnessRowIds(stdout.text);
  assert.deepEqual(ids.sort(), ['claude', 'codex', 'copilot', 'cursor', 'gemini', 'grok']);
});

test('list prints an installed record\'s target and RESOLVES=yes, and exactly one row is UNVERIFIED (gemini)', async () => {
  const home = await tempDir('cf-home-');
  const source = await tempDir('cf-src-');
  await writeMarker(source);
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile: installsPath(home), harness: 'claude', scope: 'project', method: 'symlink', source, target });

  const { code, stdout } = await run(home);
  assert.equal(code, 0);
  assert.ok(stdout.text.includes(`claude\tproject\tsymlink\t${target}\tyes`), stdout.text);

  // Parse the VERIFIED column (index 3: ID, LABEL, DETECTED, VERIFIED, PROJECT PATH) explicitly,
  // rather than matching on a line-ending string that happens to include the path too — a row
  // wrongly marked UNVERIFIED with a DIFFERENT path must still be caught.
  const harnessRows = stdout.text.split('\n').filter((l) => KNOWN_HARNESS_IDS.has(l.split('\t')[0]));
  const unverifiedRows = harnessRows.filter((l) => l.split('\t')[3] === 'UNVERIFIED');
  assert.equal(unverifiedRows.length, 1);
  const [geminiId, , , geminiVerified, geminiPath] = unverifiedRows[0].split('\t');
  assert.equal(geminiId, 'gemini');
  assert.equal(geminiVerified, 'UNVERIFIED');
  assert.equal(geminiPath, '.agents/skills');
});

test('list reports RESOLVES=no for a broken (dangling) symlink install', async () => {
  const home = await tempDir('cf-home-');
  const parent = await tempDir('cf-tgt-parent-');
  const target = path.join(parent, 'code-forge');
  const missingSource = path.join(parent, 'gone');
  await symlink(missingSource, target, 'dir');
  // Write the install record directly (bypassing install(), which would try to relink a real source).
  await writeInstallsAtomically(installsPath(home), [
    { harness: 'codex', scope: 'project', method: 'symlink', source: missingSource, target, linked_at: 'x' },
  ]);

  const { stdout } = await run(home);
  assert.ok(stdout.text.includes(`codex\tproject\tsymlink\t${target}\tno`), stdout.text);
});
