import './support.mjs';
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { runGatesVerb } from '../../src/cli/gates.mjs';
import { buildTestRepo, captureStream, git, withTempDir } from './support.mjs';

/** @param {string} cwd @param {number} [gateCode] */
async function configure(cwd, gateCode = 0) {
  await writeFile(path.join(cwd, '.code-forge.yml'), `version: 1\ngates:\n  test: ${JSON.stringify([process.execPath, '-e', `process.exit(${gateCode})`])}\n  lint: null\n  types: null\n  format: null\n`);
}

/** @param {string} cwd */
async function run(cwd) {
  const stdout = captureStream(), stderr = captureStream();
  const code = await runGatesVerb(['run', '--cwd', cwd], { stdout, stderr });
  return { code, result: JSON.parse(stdout.text), lines: stderr.text.trim().split('\n').filter(Boolean) };
}

/** @param {string} dir */
const warning = (dir) => `warn: ${dir} has package.json but no node_modules; run npm ci there first`;

test('gates run warns exactly once per missing cwd, worktree root and main root, with unchanged gate exit codes', async () => {
  await withTempDir(async (parent) => {
    const main = path.join(parent, 'main'), tree = path.join(parent, 'tree');
    await mkdir(main);
    await buildTestRepo(main);
    await writeFile(path.join(main, 'package.json'), JSON.stringify({ dependencies: { example: '1' } }));
    git(['add', 'package.json'], main);
    git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'manifest'], main);
    git(['worktree', 'add', '--detach', tree, 'HEAD'], main);
    await configure(tree);
    const passed = await run(tree);
    assert.deepEqual(passed.lines, [warning(tree), warning(main)]);
    assert.equal(passed.code, 0);
    assert.equal(passed.result.allOk, true);
    await configure(tree, 3);
    const failed = await run(tree);
    assert.deepEqual(failed.lines, [warning(tree), warning(main)]);
    assert.equal(failed.code, 1);
    assert.equal(failed.result.results[0].code, 3);
    const nested = path.join(tree, 'nested');
    await mkdir(nested);
    await writeFile(path.join(nested, 'package.json'), JSON.stringify({ devDependencies: { example: '1' } }));
    const fromNested = await run(nested);
    assert.deepEqual(fromNested.lines, [warning(nested), warning(tree), warning(main)]);
    assert.equal(fromNested.code, 1);
  });
});

test('standalone devDependencies warn; installed deps, empty deps and no deps produce zero warnings', async () => {
  await withTempDir(async (cwd) => {
    await configure(cwd);
    await writeFile(path.join(cwd, 'package.json'), JSON.stringify({ devDependencies: { example: '1' } }));
    const missing = await run(cwd);
    assert.deepEqual(missing.lines, [warning(cwd)]);
    assert.equal(missing.code, 0);
    await mkdir(path.join(cwd, 'node_modules'));
    const installed = await run(cwd);
    assert.deepEqual(installed.lines, []);
    assert.equal(installed.code, 0);
  });
  await withTempDir(async (cwd) => {
    await configure(cwd);
    for (const manifest of [{ name: 'no-deps' }, { dependencies: {}, devDependencies: {} }]) {
      await writeFile(path.join(cwd, 'package.json'), JSON.stringify(manifest));
      const ran = await run(cwd);
      assert.deepEqual(ran.lines, []);
      assert.equal(ran.code, 0);
    }
  });
});
