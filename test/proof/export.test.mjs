import assert from 'node:assert/strict';
import { existsSync, lstatSync, mkdirSync, readlinkSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { buildExport, incompleteMessage, runGatesInExport } from '../../src/proof/export.mjs';
import { CONTENT, OWNED_A, buildExportIgnoreRepo, fixtureText, readIn, tempDir } from './helpers.mjs';

/** B57: `ownedKinds` is passed explicitly at every call site, as `cli/proof.mjs` passes the block's `owned_kinds`. */
const DEFAULT_OPTS = { linkDirs: ['vendor', 'node_modules'], copyUntracked: ['.env', '.env.testing'], ownedKinds: {} };

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

test('B57: an exact owned directory is skipped itself and exports the files inside it at current content (never EISDIR); `ownedKinds` reaching it as `file` owns only the path itself', async () => {
  const ws = tempDir('dir-entry');
  const { baseSha } = buildExportIgnoreRepo(ws);
  const exp = await buildExport({ ...DEFAULT_OPTS, cwd: ws, blockId: 'B', baseSha, owned: ['other', 'docs'] });
  assert.deepEqual([exp.owned, exp.removed, exp.restored], [['docs/guide.md', 'other/new.mjs', 'other/shared.mjs'], [], ['docs/guide.md']]);
  assert.deepEqual([readIn(exp.dir, 'other/shared.mjs'), readIn(exp.dir, 'other/new.mjs'), readIn(exp.dir, 'docs/guide.md')], [CONTENT.sharedDirty, CONTENT.otherNew, fixtureText('docs/guide.md')]);
  // the same entry recorded as a regular file at `block open`: it owns only `other` itself, a directory, so nothing is copied
  const asFile = await buildExport({ ...DEFAULT_OPTS, cwd: ws, blockId: 'B2', baseSha, owned: ['other'], ownedKinds: { other: 'file' } });
  assert.deepEqual([asFile.owned, asFile.removed], [[], []]);
  assert.equal(readIn(asFile.dir, 'other/shared.mjs'), fixtureText('other/shared.mjs'));
  assert.equal(existsSync(path.join(asFile.dir, 'other/new.mjs')), false);
});

test('B57: an owned directory missing from the tree loses its owned base blobs one by one (listed), never the export directory whole; a `file` kind claims no base blob below it', async () => {
  const ws = tempDir('dir-missing');
  const { baseSha } = buildExportIgnoreRepo(ws);
  rmSync(path.join(ws, 'other'), { recursive: true, force: true });
  const exp = await buildExport({ ...DEFAULT_OPTS, cwd: ws, blockId: 'B', baseSha, owned: ['other'] });
  assert.deepEqual([exp.owned, exp.removed], [[], ['other/shared.mjs']]);
  assert.deepEqual([lstatSync(path.join(exp.dir, 'other')).isDirectory(), existsSync(path.join(exp.dir, 'other/shared.mjs'))], [true, false]);
  const asFile = await buildExport({ ...DEFAULT_OPTS, cwd: ws, blockId: 'B2', baseSha, owned: ['other'], ownedKinds: { other: 'file' } });
  assert.deepEqual([asFile.owned, asFile.removed], [[], []]);
  assert.equal(readIn(asFile.dir, 'other/shared.mjs'), fixtureText('other/shared.mjs'));
});

test('B57: a base FILE the tree turned into a directory leaves the export (listed) before the files below it are copied — owned itself or not', async () => {
  for (const owned of [['src/base.mjs'], ['src/base.mjs/inner.mjs']]) {
    const ws = tempDir(`file-to-dir-${owned.length}-${owned[0].length}`);
    const { baseSha } = buildExportIgnoreRepo(ws);
    rmSync(path.join(ws, 'src/base.mjs'));
    mkdirSync(path.join(ws, 'src/base.mjs'));
    writeFileSync(path.join(ws, 'src/base.mjs/inner.mjs'), 'export const inner = 1;\n');
    const exp = await buildExport({ ...DEFAULT_OPTS, cwd: ws, blockId: 'B', baseSha, owned });
    assert.deepEqual([exp.owned, exp.removed], [['src/base.mjs/inner.mjs'], ['src/base.mjs']], owned.join());
    assert.equal(readIn(exp.dir, 'src/base.mjs/inner.mjs'), 'export const inner = 1;\n');
  }
});

test('B57: a base DIRECTORY the tree turned into a file leaves the export with every base blob in it (listed, owned or not), then the file is copied', async () => {
  for (const ownedKinds of [{}, { other: 'file' }]) {
    const ws = tempDir(`dir-to-file-${Object.keys(ownedKinds).length}`);
    const { baseSha } = buildExportIgnoreRepo(ws);
    rmSync(path.join(ws, 'other'), { recursive: true, force: true });
    writeFileSync(path.join(ws, 'other'), 'now a file\n');
    const exp = await buildExport({ ...DEFAULT_OPTS, cwd: ws, blockId: 'B', baseSha, owned: ['other'], ownedKinds });
    assert.deepEqual([exp.owned, exp.removed], [['other'], ['other/shared.mjs']], JSON.stringify(ownedKinds));
    assert.equal(readIn(exp.dir, 'other'), 'now a file\n');
  }
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
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, ownedKinds: {}, linkDirs: [], copyUntracked: ['.env'] });
  const run = await runGatesInExport({ gates: { test: [process.execPath, 'gate.mjs', '.env.testing'] }, exportDir: exp.dir, mainTree: ws });

  assert.deepEqual(run.incomplete, [{ gate: 'test', path: '.env.testing', message: incompleteMessage('.env.testing') }]);
  assert.equal(incompleteMessage('.env.testing'), 'export.incomplete: .env.testing — add it to proof.export.copy_untracked or set proof.isolation: lock');
  assert.deepEqual([run.results[0].result, run.allOk], ['export.incomplete', false]);
});

test('a file missing from the main tree too is a real red, not export.incomplete', async () => {
  const ws = tempDir('real-red');
  const { baseSha } = buildExportIgnoreRepo(ws);
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, ownedKinds: {}, linkDirs: [], copyUntracked: ['.env'] });
  const run = await runGatesInExport({ gates: { test: [process.execPath, 'gate.mjs', 'missing.txt'] }, exportDir: exp.dir, mainTree: ws });

  assert.deepEqual([run.results[0].result, run.incomplete.length, run.allOk], ['red', 0, false]);
});

// ── B61: a Composer vendor dir is copied, not linked ───────────────────────────

/**
 * A Composer-style `vendor/` in the main tree: the generated autoloader, one package file and a
 * relative `vendor/bin` symlink to it.
 * @param {string} ws
 */
function writeComposerVendor(ws) {
  mkdirSync(path.join(ws, 'vendor/composer'), { recursive: true });
  mkdirSync(path.join(ws, 'vendor/acme/tool'), { recursive: true });
  mkdirSync(path.join(ws, 'vendor/bin'), { recursive: true });
  writeFileSync(path.join(ws, 'vendor/composer/autoload_real.php'), '<?php // fixture autoloader\n');
  writeFileSync(path.join(ws, 'vendor/autoload.php'), '<?php // fixture autoload\n');
  writeFileSync(path.join(ws, 'vendor/acme/tool/run.php'), '<?php // tool v1\n');
  symlinkSync('../acme/tool/run.php', path.join(ws, 'vendor/bin/tool'));
}

test('B61: a link_dirs entry holding composer/autoload_real.php is copied into the export (a real directory, relative vendor/bin links kept); node_modules stays linked', async () => {
  const ws = tempDir('composer-vendor');
  const { baseSha } = buildExportIgnoreRepo(ws);
  writeComposerVendor(ws);
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, ...DEFAULT_OPTS });

  assert.deepEqual([exp.copied, exp.linked], [['vendor'], ['node_modules']]);
  const vendor = path.join(exp.dir, 'vendor');
  assert.deepEqual([lstatSync(vendor).isSymbolicLink(), lstatSync(vendor).isDirectory()], [false, true]);
  assert.deepEqual([readlinkSync(path.join(vendor, 'bin/tool')), readIn(exp.dir, 'vendor/bin/tool')], ['../acme/tool/run.php', '<?php // tool v1\n']);
  // what an autoloader computing its directory from its own real path sees: the export, not the main tree
  const where = await runGatesInExport({
    gates: { test: [process.execPath, '-e', "process.stdout.write(require('node:fs').realpathSync('vendor/composer/autoload_real.php'))"] },
    exportDir: exp.dir,
    mainTree: ws,
  });
  assert.deepEqual([where.allOk, where.results[0].stdout], [true, path.join(realpathSync(exp.dir), 'vendor/composer/autoload_real.php')]);
  // a later change to the main tree's vendor does not reach the export
  writeFileSync(path.join(ws, 'vendor/acme/tool/run.php'), '<?php // tool v2\n');
  assert.equal(readIn(exp.dir, 'vendor/acme/tool/run.php'), '<?php // tool v1\n');
});

test('B61: in a copied Composer vendor an absolute link into the main tree (a path repository) points into the export; a link outside the main tree is kept', async () => {
  const ws = tempDir('composer-path-repo');
  const { baseSha } = buildExportIgnoreRepo(ws);
  writeComposerVendor(ws);
  mkdirSync(path.join(ws, 'packages/pkg'), { recursive: true });
  const outside = tempDir('composer-outside');
  symlinkSync(path.join(ws, 'packages/pkg'), path.join(ws, 'vendor/acme/pkg'));
  symlinkSync(path.join(realpathSync(ws), 'packages/pkg'), path.join(ws, 'vendor/acme/pkg-real'));
  symlinkSync(outside, path.join(ws, 'vendor/acme/shared'));
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, ...DEFAULT_OPTS });

  assert.deepEqual(exp.copied, ['vendor']);
  assert.deepEqual(
    ['vendor/acme/pkg', 'vendor/acme/pkg-real', 'vendor/acme/shared', 'vendor/bin/tool'].map((rel) => readlinkSync(path.join(exp.dir, rel))),
    [path.join(exp.dir, 'packages/pkg'), path.join(exp.dir, 'packages/pkg'), outside, '../acme/tool/run.php'],
  );
  // the main tree's links are untouched
  assert.equal(readlinkSync(path.join(ws, 'vendor/acme/pkg')), path.join(ws, 'packages/pkg'));
});

test('B61: in a copied Composer vendor a link to the main-tree root points at the export root; a link into the main tree\'s .code-forge/ is kept', async () => {
  const ws = tempDir('composer-root-link');
  const { baseSha } = buildExportIgnoreRepo(ws);
  writeComposerVendor(ws);
  symlinkSync(ws, path.join(ws, 'vendor/acme/root'));
  symlinkSync(path.join(ws, '.code-forge/cache'), path.join(ws, 'vendor/acme/state'));
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, ...DEFAULT_OPTS });

  assert.deepEqual(
    ['vendor/acme/root', 'vendor/acme/state'].map((rel) => readlinkSync(path.join(exp.dir, rel))),
    [exp.dir, path.join(ws, '.code-forge/cache')],
  );
});

test('B61: a main-tree vendor that is a symlink to a Composer vendor elsewhere is copied as a real directory, and the real vendor\'s links (and those behind a linked dir) stay byte-for-byte', async () => {
  const ws = tempDir('composer-linked-vendor');
  const { baseSha } = buildExportIgnoreRepo(ws);
  const shared = tempDir('composer-shared');
  writeComposerVendor(shared);
  const behind = tempDir('composer-behind-link');
  symlinkSync(path.join(ws, 'packages/inner'), path.join(behind, 'inner'));
  symlinkSync(path.join(ws, 'packages/pkg'), path.join(shared, 'vendor/acme/pkg'));
  symlinkSync(behind, path.join(shared, 'vendor/acme/linked-dir'));
  symlinkSync(path.join(shared, 'vendor'), path.join(ws, 'vendor'));
  const links = [
    path.join(shared, 'vendor/bin/tool'),
    path.join(shared, 'vendor/acme/pkg'),
    path.join(shared, 'vendor/acme/linked-dir'),
    path.join(behind, 'inner'),
    path.join(ws, 'vendor'),
  ];
  const before = links.map((p) => readlinkSync(p, { encoding: 'buffer' }));
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, ...DEFAULT_OPTS });

  assert.deepEqual([exp.copied, exp.linked], [['vendor'], ['node_modules']]);
  const vendor = path.join(exp.dir, 'vendor');
  assert.deepEqual([lstatSync(vendor).isSymbolicLink(), lstatSync(vendor).isDirectory()], [false, true]);
  assert.deepEqual(
    ['vendor/acme/pkg', 'vendor/acme/linked-dir', 'vendor/bin/tool'].map((rel) => readlinkSync(path.join(exp.dir, rel))),
    [path.join(exp.dir, 'packages/pkg'), behind, '../acme/tool/run.php'],
  );
  assert.deepEqual(links.map((p) => readlinkSync(p, { encoding: 'buffer' })), before);
});

test('B61: a vendor dir without composer/autoload_real.php (a Go vendor) is still linked, and nothing is copied', async () => {
  const ws = tempDir('go-vendor');
  const { baseSha } = buildExportIgnoreRepo(ws);
  mkdirSync(path.join(ws, 'vendor/example.com/mod'), { recursive: true });
  writeFileSync(path.join(ws, 'vendor/modules.txt'), '# example.com/mod v1.0.0\n');
  const exp = await buildExport({ cwd: ws, blockId: 'A', baseSha, owned: OWNED_A, ...DEFAULT_OPTS });

  assert.deepEqual([exp.copied, exp.linked], [[], ['node_modules', 'vendor']]);
  const vendor = path.join(exp.dir, 'vendor');
  assert.deepEqual([lstatSync(vendor).isSymbolicLink(), readlinkSync(vendor)], [true, path.join(realpathSync(ws), 'vendor')]);
});
