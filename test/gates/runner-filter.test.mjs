import './support.mjs';
import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import * as expand from '../../src/gates/expand.mjs';
import { runGates } from '../../src/gates/run.mjs';
import { git, withTempDir } from './support.mjs';

const MIXED = ['native/a/a_test.go', 'test/x.test.mjs'];

test('testsForRunner selects each runner family by executable basename and preserves unknown runners', () => {
  assert.deepEqual(expand.expandArgv(['node', '--test', '{test_files}'], { scope: 'related', files: MIXED }).argv,
    ['node', '--test', MIXED[1]], 'the node runner must receive only its own test files');
  assert.equal(typeof expand.testsForRunner, 'function', 'testsForRunner must be exported');
  const js = ['js', 'mjs', 'cjs', 'jsx', 'ts', 'mts', 'cts', 'tsx'].map((ext) => `test/x.test.${ext}`);
  const others = ['test/XTest.php', 'test/test_x.py', 'test/x.rs', 'test/XTests.swift'];
  const files = Object.freeze([...MIXED, ...js.filter((file) => file !== MIXED[1]), ...others, 'test/README.md']);
  for (const runner of ['node', 'bun', 'deno', 'npm', 'npx', 'pnpm', 'yarn', 'bunx', 'vitest', 'jest', 'mocha']) {
    assert.deepEqual(expand.testsForRunner([`/usr/local/bin/${runner}`, '--test', '{test_files}'], files),
      [MIXED[1], ...js.filter((file) => file !== MIXED[1])], runner);
  }
  for (const [runners, expected] of [
    [['go'], [MIXED[0]]],
    [['php', 'pest', 'phpunit', 'vendor/bin/pest', 'vendor/bin/phpunit'], [others[0]]],
    [['pytest', 'python', 'python3'], [others[1]]],
    [['cargo'], [others[2]]],
    [['swift', 'xcodebuild'], [others[3]]],
  ]) {
    for (const runner of runners) assert.deepEqual(expand.testsForRunner([runner, 'test', '{test_files}'], files), expected, runner);
  }
  for (const runner of ['sh', 'make']) assert.deepEqual(expand.testsForRunner([runner, 'run.sh'], files), files, runner);
  assert.deepEqual(expand.testsForRunner(['node', '--test'], []), []);
});

test('expandArgv filters test_files while changed_files retains every changed path', () => {
  const changedFiles = [...MIXED, 'src/x.mjs'];
  const scope = { scope: /** @type {const} */ ('changed'), files: MIXED, changedFiles };
  assert.deepEqual(expand.expandArgv(['node', '--test', '{test_files}'], scope),
    { argv: ['node', '--test', MIXED[1]], scoped: true });
  assert.deepEqual(expand.expandArgv(['node', '{test_files}', '--changed', '{changed_files}'], scope),
    { argv: ['node', MIXED[1], '--changed', ...changedFiles], scoped: true });
  assert.deepEqual(expand.expandArgv(['node', '{changed_files}'], scope),
    { argv: ['node', ...changedFiles], scoped: true });
});

test('expandArgv drops test_files for a full run when only foreign tests remain', () => {
  for (const scope of [
    { scope: /** @type {const} */ ('changed'), files: [MIXED[0]], changedFiles: [MIXED[0]] },
    { scope: /** @type {const} */ ('related'), files: [MIXED[0]] },
  ]) assert.deepEqual(expand.expandArgv(['node', '--test', '{test_files}'], scope),
    // scoped records placeholder handling; full identifies the suite fallback.
    { argv: ['node', '--test'], scoped: true, full: true, reason: 'runner-filter' });
});

async function recorder(dir) {
  await writeFile(path.join(dir, 'record.mjs'), [
    "import { appendFileSync } from 'node:fs';",
    "appendFileSync('calls.jsonl', JSON.stringify(process.argv.slice(2)) + '\\n');",
  ].join('\n'));
  return {
    argv: [process.execPath, 'record.mjs', '{test_files}'],
    calls: async () => (await readFile(path.join(dir, 'calls.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line)),
  };
}

test('runGates passes only runner tests and reports the number actually passed, including a full fallback', async () => {
  await withTempDir(async (dir) => {
    git(['init', '-q'], dir);
    const fake = await recorder(dir);
    for (const scope of ['related', 'changed']) for (const files of [MIXED, [MIXED[0]]]) {
      const ran = await runGates({ test: fake.argv }, { cwd: dir, scope: scope === 'changed'
        ? { scope, files, changedFiles: files } : { scope: 'related', files } });
      assert.equal(ran.allOk, true);
      const result = ran.results[0];
      assert.equal(result.skipped, false);
      assert.deepEqual([result.scope, result.reason, result.test_count],
        files.length === 2 ? [scope, undefined, 1] : ['full', 'runner-filter', undefined]);
      if (files.length === 1) assert.equal(Object.hasOwn(result, 'test_count'), false);
    }
    assert.deepEqual(await fake.calls(), [[MIXED[1]], [], [MIXED[1]], []]);
  });
});

test('a runner-filter full fallback skips repeats even when a compatible changed test exists', async () => {
  await withTempDir(async (dir) => {
    git(['init', '-q'], dir);
    const fake = await recorder(dir);
    await mkdir(path.join(dir, 'test'));
    await writeFile(path.join(dir, MIXED[1]), 'export {};\n');
    git(['add', '-A'], dir);
    git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'base'], dir);
    const base = git(['rev-parse', 'HEAD'], dir);
    await writeFile(path.join(dir, MIXED[1]), 'export const changed = true;\n');
    const skipped = [];
    const ran = await runGates({ test: fake.argv,
      repeatNewTests: { n: 2, base, owned: [MIXED[1]], onSkip: (line) => skipped.push(line) },
    }, { cwd: dir, scope: { scope: 'related', files: [MIXED[0]] } });
    assert.equal(ran.allOk, true);
    assert.deepEqual([ran.results[0].scope, ran.results[0].reason], ['full', 'runner-filter']);
    assert.deepEqual(await fake.calls(), [[]]);
    assert.deepEqual(skipped, ['repeat_new_tests: skipped (no changed tests for this runner)']);
    assert.equal(Object.hasOwn(ran, 'repeats'), false);
    assert.equal(Object.hasOwn(ran.results[0], 'repeats'), false);
  });
});

test('repeat_new_tests skips foreign changed tests and calls onSkip without repeating the full suite', async () => {
  await withTempDir(async (dir) => {
    git(['init', '-q'], dir);
    const fake = await recorder(dir);
    await mkdir(path.join(dir, 'native/a'), { recursive: true });
    await writeFile(path.join(dir, MIXED[0]), 'package a\n');
    git(['add', '-A'], dir);
    git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'base'], dir);
    const base = git(['rev-parse', 'HEAD'], dir);
    await writeFile(path.join(dir, MIXED[0]), 'package a\n// changed\n');
    const skipped = [];
    const ran = await runGates({ test: fake.argv, repeatNewTests: { n: 2, base, owned: [MIXED[0]], onSkip: (line) => skipped.push(line) } }, { cwd: dir });
    assert.equal(ran.allOk, true);
    assert.deepEqual(await fake.calls(), [[]], 'the initial full run must be the only invocation');
    assert.deepEqual(skipped, ['repeat_new_tests: skipped (no changed tests for this runner)']);
    assert.equal(Object.hasOwn(ran, 'repeats'), false);
    assert.equal(Object.hasOwn(ran.results[0], 'repeats'), false);
  });
});
