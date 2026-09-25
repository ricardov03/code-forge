import assert from 'node:assert/strict';
import { readlink, realpath, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { sink, tempDir, writeMarker } from './helpers.mjs';
import { DEFAULT_SKILL_SOURCE, parseUpgradeArgs, runUpgrade } from '../../src/cli/upgrade.mjs';
import { install, installsPath, readInstalls } from '../../src/install/link.mjs';

/** The package root, computed independently of `src/cli/upgrade.mjs`'s own `__dirname` math. */
const ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

// ── parseUpgradeArgs ────────────────────────────────────────────────────────

test('parseUpgradeArgs: no args', () => {
  assert.deepEqual(parseUpgradeArgs([]), {});
});

test('parseUpgradeArgs: --source <path>', () => {
  assert.deepEqual(parseUpgradeArgs(['--source', '/some/path']), { source: '/some/path' });
});

for (const bad of [['--source'], ['--bogus', '/x'], ['--source', '/x', 'extra']]) {
  test(`parseUpgradeArgs rejects ${JSON.stringify(bad)}`, () => {
    assert.equal(parseUpgradeArgs(bad), null);
  });
}

// ── DEFAULT_SKILL_SOURCE ──────────────────────────────────────────────────────

test('DEFAULT_SKILL_SOURCE resolves to <package root>/skill', () => {
  assert.equal(DEFAULT_SKILL_SOURCE, path.join(ROOT, 'skill'));
});

// ── runUpgrade wiring: proves the CLI verb, not just the library it calls ────

test('runUpgrade re-points ALL 3 recorded installs at the given --source, via the VERB', async () => {
  const home = await tempDir('cf-home-');
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');
  const installsFile = installsPath(home);

  const targets = await Promise.all([1, 2, 3].map(async (i) => path.join(await tempDir(`cf-tgt${i}-`), 'code-forge')));
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target: targets[0] });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source: sourceA, target: targets[1] });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'symlink', source: sourceA, target: targets[2] });

  const sourceB = await tempDir('cf-src-b-');
  await writeMarker(sourceB, 'B');

  const stdout = sink();
  const code = await runUpgrade(['--source', sourceB], { stdout, stderr: sink(), home });

  assert.equal(code, 0);
  for (const t of targets) {
    assert.equal(await readlink(t), sourceB);
  }
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 3);
  assert.ok(records.every((r) => r.source === sourceB));
  assert.equal(stdout.text.split('\n').filter((l) => l.startsWith('upgraded ')).length, 3);
});

test('runUpgrade resolves a RELATIVE --source against cwd — links AND the ledger store the ABSOLUTE path, not the typed relative one', async () => {
  const home = await tempDir('cf-home-');
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');
  const installsFile = installsPath(home);
  const targets = await Promise.all([1, 2, 3].map(async (i) => path.join(await tempDir(`cf-tgt${i}-`), 'code-forge')));
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target: targets[0] });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source: sourceA, target: targets[1] });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'symlink', source: sourceA, target: targets[2] });

  const projectDir = await tempDir('cf-project-');
  await writeMarker(path.join(projectDir, 'my-skill'), 'B');
  const absoluteSource = path.resolve(projectDir, 'my-skill');

  const code = await runUpgrade(['--source', './my-skill'], { stdout: sink(), stderr: sink(), home, cwd: projectDir });

  assert.equal(code, 0);
  for (const t of targets) {
    assert.equal(await readlink(t), absoluteSource, 'the recorded symlink target must be the ABSOLUTE source, not the typed relative one');
    // realpath must resolve without throwing — proves the link is genuinely reachable, not dangling.
    assert.equal(await realpath(t), await realpath(absoluteSource));
  }
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 3);
  for (const record of records) {
    assert.equal(record.source, absoluteSource);
    assert.equal(path.isAbsolute(record.source), true);
  }
});

test('runUpgrade with a MISSING --source exits 1, writes nothing, and leaves all 3 links resolving at their OLD source', async () => {
  const home = await tempDir('cf-home-');
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');
  const installsFile = installsPath(home);
  const targets = await Promise.all([1, 2, 3].map(async (i) => path.join(await tempDir(`cf-tgt${i}-`), 'code-forge')));
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target: targets[0] });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source: sourceA, target: targets[1] });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'symlink', source: sourceA, target: targets[2] });

  const stderr = sink();
  const code = await runUpgrade(['--source', '/definitely/does/not/exist/anywhere/code-forge'], { stdout: sink(), stderr, home });

  assert.equal(code, 1);
  assert.match(stderr.text, /does not exist/);
  for (const t of targets) {
    assert.equal(await readlink(t), sourceA, 'every link must still point at the OLD source');
  }
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 3);
  assert.ok(records.every((r) => r.source === sourceA));
});

test('runUpgrade with a --source that EXISTS but is a FILE (not a directory) exits 1 and touches nothing', async () => {
  const home = await tempDir('cf-home-');
  const filePath = path.join(await tempDir('cf-file-'), 'not-a-dir');
  await writeFile(filePath, 'x', 'utf8');
  const stderr = sink();
  const code = await runUpgrade(['--source', filePath], { stdout: sink(), stderr, home });
  assert.equal(code, 1);
  assert.match(stderr.text, /not a directory/);
});

test('runUpgrade with no --source falls back to deps.defaultSource', async () => {
  const home = await tempDir('cf-home-');
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');
  const installsFile = installsPath(home);
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target });

  const fallback = await tempDir('cf-src-fallback-');
  await writeMarker(fallback, 'fallback');

  const code = await runUpgrade([], { stdout: sink(), stderr: sink(), home, defaultSource: fallback });
  assert.equal(code, 0);
  assert.equal(await readlink(target), fallback);
});

test('runUpgrade on an empty installs.json prints "nothing to upgrade" and exits 0', async () => {
  const home = await tempDir('cf-home-');
  const stdout = sink();
  const code = await runUpgrade([], { stdout, stderr: sink(), home, defaultSource: await tempDir('cf-src-') });
  assert.equal(code, 0);
  assert.equal(stdout.text, 'nothing to upgrade\n');
});

test('runUpgrade with bad args exits 2 with the usage line on stderr', async () => {
  const home = await tempDir('cf-home-');
  const stderr = sink();
  const code = await runUpgrade(['--bogus'], { stdout: sink(), stderr, home });
  assert.equal(code, 2);
  assert.equal(stderr.text, 'usage: code-forge upgrade [--source <path>]\n');
});
