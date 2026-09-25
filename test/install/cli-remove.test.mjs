import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';
import { sink, tempDir, writeMarker } from './helpers.mjs';
import { parseRemoveArgs, runRemove } from '../../src/cli/remove.mjs';
import { install, installsPath, readInstalls, targetResolves } from '../../src/install/link.mjs';

// ── parseRemoveArgs ────────────────────────────────────────────────────────────

test('parseRemoveArgs: no args removes everything', () => {
  assert.deepEqual(parseRemoveArgs([]), {});
});

test('parseRemoveArgs: a bare harness name', () => {
  assert.deepEqual(parseRemoveArgs(['claude']), { harness: 'claude' });
});

test('parseRemoveArgs: harness + --scope', () => {
  assert.deepEqual(parseRemoveArgs(['claude', '--scope', 'global']), { harness: 'claude', scope: 'global' });
});

test('parseRemoveArgs: --scope alone (no harness)', () => {
  assert.deepEqual(parseRemoveArgs(['--scope', 'project']), { scope: 'project' });
});

for (const bad of [['claude', '--scope'], ['claude', '--scope', 'nowhere'], ['claude', '--bogus', 'x'], ['claude', '--scope', 'global', 'extra']]) {
  test(`parseRemoveArgs rejects ${JSON.stringify(bad)}`, () => {
    assert.equal(parseRemoveArgs(bad), null);
  });
}

// ── runRemove wiring: proves the CLI verb, not just the library it calls ─────

test('runRemove deletes only the 3 recorded targets and leaves a 4th unrecorded path — via the VERB, not the library directly', async () => {
  const home = await tempDir('cf-home-');
  const source = await tempDir('cf-src-');
  await writeMarker(source);
  const installsFile = installsPath(home);

  const targets = await Promise.all([1, 2, 3].map(async (i) => path.join(await tempDir(`cf-tgt${i}-`), 'code-forge')));
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: targets[0] });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source, target: targets[1] });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'copy', source, target: targets[2] });
  const unrecorded = path.join(await tempDir('cf-tgt4-'), 'code-forge');
  await writeMarker(unrecorded);

  const stdout = sink();
  const code = await runRemove([], { stdout, stderr: sink(), home });

  assert.equal(code, 0);
  for (const t of targets) {
    assert.equal(await targetResolves(t), false);
  }
  assert.equal(await targetResolves(unrecorded), true);
  // Two of the three removed targets are symlinks to `source` — proves remove deleted the LINKS,
  // not what they point to.
  assert.equal(await targetResolves(source), true, 'the shared source directory must survive');
  assert.deepEqual(await readInstalls(installsFile), []);
  assert.equal(stdout.text.split('\n').filter((l) => l.startsWith('removed ')).length, 3);
});

test('runRemove with a harness name removes only that harness', async () => {
  const home = await tempDir('cf-home-');
  const source = await tempDir('cf-src-');
  await writeMarker(source);
  const installsFile = installsPath(home);
  const targetA = path.join(await tempDir('cf-tgt-a-'), 'code-forge');
  const targetB = path.join(await tempDir('cf-tgt-b-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: targetA });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source, target: targetB });

  const code = await runRemove(['claude'], { stdout: sink(), stderr: sink(), home });

  assert.equal(code, 0);
  assert.equal(await targetResolves(targetA), false);
  assert.equal(await targetResolves(targetB), true);
  assert.equal(await targetResolves(source), true, 'the shared source directory must survive a harness-filtered remove too');
});

test('runRemove on nothing recorded prints "nothing to remove" and exits 0', async () => {
  const home = await tempDir('cf-home-');
  const stdout = sink();
  const code = await runRemove([], { stdout, stderr: sink(), home });
  assert.equal(code, 0);
  assert.equal(stdout.text, 'nothing to remove\n');
});

test('runRemove with bad args exits 2 with the usage line on stderr', async () => {
  const home = await tempDir('cf-home-');
  const stderr = sink();
  const code = await runRemove(['claude', '--scope', 'nowhere'], { stdout: sink(), stderr, home });
  assert.equal(code, 2);
  assert.equal(stderr.text, 'usage: code-forge remove [<harness>] [--scope project|global]\n');
});
