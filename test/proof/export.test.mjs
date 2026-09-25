import assert from 'node:assert/strict';
import { existsSync, lstatSync, readlinkSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { buildExport, incompleteMessage, runGatesInExport } from '../../src/proof/export.mjs';
import { CONTENT, OWNED_A, buildExportIgnoreRepo, fixtureText, readIn, tempDir } from './helpers.mjs';

const DEFAULT_OPTS = { linkDirs: ['vendor', 'node_modules'], copyUntracked: ['.env', '.env.testing'] };

test('export = base + owned at current content + linked dir + export-ignore docs/ + untracked .env, without the other block\'s dirty files', async () => {
  const ws = tempDir('full');
  const { baseSha } = buildExportIgnoreRepo(ws);
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, ...DEFAULT_OPTS });

  assert.equal(exp.dir, path.join(realpathSync(ws), '.code-forge', 'export', 'A'));
  // 5 path asserts
  assert.equal(readIn(exp.dir, 'src/base.mjs'), fixtureText('src/base.mjs'));
  assert.deepEqual([readIn(exp.dir, 'src/owned-a.mjs'), readIn(exp.dir, 'src/owned-b.mjs')], [CONTENT.ownedA, CONTENT.ownedB]);
  const lib = path.join(exp.dir, 'lib');
  assert.deepEqual([lstatSync(lib).isSymbolicLink(), readlinkSync(lib), readIn(exp.dir, 'lib/base.mjs')], [true, 'src', fixtureText('src/base.mjs')]);
  assert.equal(readIn(exp.dir, 'docs/guide.md'), fixtureText('docs/guide.md'));
  assert.equal(readIn(exp.dir, '.env'), CONTENT.env);
  // link_dirs: node_modules is a link to the main tree's (export resolves cwd; so does the expectation)
  const nodeModules = path.join(exp.dir, 'node_modules');
  assert.deepEqual([lstatSync(nodeModules).isSymbolicLink(), readlinkSync(nodeModules)], [true, path.join(realpathSync(ws), 'node_modules')]);
  // the other block's dirty files: its new file is absent, its modified file is at base
  assert.equal(existsSync(path.join(exp.dir, 'other/new.mjs')), false);
  assert.equal(readIn(exp.dir, 'other/shared.mjs'), fixtureText('other/shared.mjs'));

  assert.deepEqual(
    { restored: exp.restored, untracked: exp.untracked, owned: exp.owned, linked: exp.linked, removed: exp.removed },
    { restored: ['docs/guide.md'], untracked: ['.env', '.env.testing'], owned: ['src/owned-a.mjs', 'src/owned-b.mjs'], linked: ['node_modules'], removed: [] },
  );
});

test('without the export-ignore line the export has the same docs/ content (and nothing needed restoring)', async () => {
  const withAttr = tempDir('with-attr');
  const withoutAttr = tempDir('without-attr');
  const a = buildExportIgnoreRepo(withAttr);
  const b = buildExportIgnoreRepo(withoutAttr, { attributes: false });
  const expA = await buildExport({ cwd: withAttr, blockId: 'A', baseSha: a.baseSha, owned: OWNED_A, ...DEFAULT_OPTS });
  const expB = await buildExport({ cwd: withoutAttr, blockId: 'A', baseSha: b.baseSha, owned: OWNED_A, ...DEFAULT_OPTS });

  assert.equal(readIn(expB.dir, 'docs/guide.md'), readIn(expA.dir, 'docs/guide.md'));
  assert.deepEqual([expA.restored, expB.restored], [['docs/guide.md'], []]);
});

test('export.incomplete names .env.testing when the gate needs it and copy_untracked lacks it', async () => {
  const ws = tempDir('incomplete');
  const { baseSha } = buildExportIgnoreRepo(ws);
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, linkDirs: [], copyUntracked: ['.env'] });
  const run = await runGatesInExport({ gates: { test: [process.execPath, 'gate.mjs', '.env.testing'] }, exportDir: exp.dir, mainTree: ws });

  assert.deepEqual(run.incomplete, [{ gate: 'test', path: '.env.testing', message: incompleteMessage('.env.testing') }]);
  assert.equal(incompleteMessage('.env.testing'), 'export.incomplete: .env.testing — add it to proof.export.copy_untracked or set proof.isolation: lock');
  assert.deepEqual([run.results[0].result, run.allOk], ['export.incomplete', false]);
});

test('a file missing from the main tree too is a real red, not export.incomplete', async () => {
  const ws = tempDir('real-red');
  const { baseSha } = buildExportIgnoreRepo(ws);
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, linkDirs: [], copyUntracked: ['.env'] });
  const run = await runGatesInExport({ gates: { test: [process.execPath, 'gate.mjs', 'missing.txt'] }, exportDir: exp.dir, mainTree: ws });

  assert.deepEqual([run.results[0].result, run.incomplete.length, run.allOk], ['red', 0, false]);
});
