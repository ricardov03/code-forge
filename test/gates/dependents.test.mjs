import { chmod, mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { changedScope, changedTestFiles, expandArgv, fileBaseline, scopeFields } from '../../src/gates/expand.mjs';
import { runGates } from '../../src/gates/run.mjs';
import { git, withTempDir } from './support.mjs';

/** @param {string} cwd @param {Record<string, string>} files */
async function put(cwd, files) {
  for (const [file, text] of Object.entries(files)) {
    await mkdir(path.dirname(path.join(cwd, file)), { recursive: true });
    await writeFile(path.join(cwd, file), text);
  }
}

/** @param {string} cwd */
function baseCommit(cwd) {
  git(['init', '-q'], cwd);
  git(['add', '-A'], cwd);
  git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'base'], cwd);
  return git(['rev-parse', 'HEAD'], cwd);
}

test('changed scope adds exactly the direct, transitive, export-from and literal dynamic importers', async () => {
  await withTempDir(async (cwd) => {
    const ownedTest = 'test/owned.test.mjs';
    const added = ['test/cli/direct.test.mjs', 'test/dynamic.test.mjs', 'test/export.test.mjs', 'test/index.test.mjs', 'test/js.test.mjs', 'test/transitive.test.mjs'];
    const tests = [...added, ownedTest, 'test/unrelated.test.mjs'];
    const sources = ['src/b.mjs', 'src/plain.js', 'src/pkg/index.mjs'];
    const owned = [...sources, ownedTest];
    await put(cwd, {
      'src/b.mjs': "import './a.mjs'; export const b = 1;",
      'src/a.mjs': "import { b } from './b.mjs'; export { b };",
      'src/barrel.mjs': "export { b } from './b.mjs';",
      'src/plain.js': 'export const value = 1;',
      'src/pkg/index.mjs': 'export const value = 1;',
      [ownedTest]: "import { b } from '../src/b.mjs';",
      [added[0]]: "import {\n b\n} from '../../src/b.mjs';",
      [added[1]]: "const b = await import('../src/b.mjs');",
      [added[2]]: "import { b } from '../src/barrel.mjs';",
      [added[3]]: "import { value } from '../src/pkg';",
      [added[4]]: "import { value } from '../src/plain';",
      [added[5]]: "import { b } from '../src/a.mjs';",
      'test/unrelated.test.mjs': `import 'some-package'; import 'node:fs';
// import { b } from '../src/b.mjs';
const text = "import('../src/b.mjs')";
const name = '../src/b.mjs'; import(name);
`,
    });
    const base = baseCommit(cwd);
    const baseline = (await fileBaseline(cwd, owned)).files;
    await put(cwd, Object.fromEntries(sources.map((file) => [file, 'export const value = 2;'])));
    await put(cwd, { [ownedTest]: "import '../src/b.mjs'; // changed owned test" });
    const expected = [...added, ownedTest].sort();
    assert.deepEqual(await changedTestFiles({ cwd, base, owned }), expected, 'union contains all seven tests exactly once, including six outside ownership');
    const { dependentTests } = await import('../../src/gates/dependents.mjs');
    assert.deepEqual(await dependentTests({ cwd, changed: [...sources, ownedTest], testFiles: tests }), expected);
    const scope = changedScope({ baseline, current: await fileBaseline(cwd, owned), fallback: { scope: 'related', files: [ownedTest] }, owned });
    assert.deepEqual(scope.files, expected);
    const ran = await runGates({ test: [process.execPath, '-e', 'console.log(JSON.stringify(process.argv.slice(1)))', '{test_files}'] }, { cwd, scope });
    const result = JSON.parse(JSON.stringify(ran.results[0]));
    assert.equal(result.scope, 'changed');
    assert.deepEqual(JSON.parse(result.stdout), expected);
    assert.deepEqual(result.dependents, added, 'JSON explains only the six added tests');
  });
});

test('schema changes add config and doctor tests; their union above the threshold runs the full suite', async () => {
  await withTempDir(async (cwd) => {
    const schema = 'schema/code-forge.schema.json';
    const ownedTest = 'test/owned.test.mjs';
    const added = ['test/config/schema.test.mjs', 'test/doctor/report.test.mjs'];
    const owned = [schema, ownedTest];
    await put(cwd, { [schema]: '{}', [ownedTest]: 'export {};', [added[0]]: 'export {};', [added[1]]: 'export {};', 'test/unrelated.test.mjs': 'export {};' });
    const base = baseCommit(cwd);
    const baseline = (await fileBaseline(cwd, owned)).files;
    await put(cwd, { [schema]: '{"changed": true}', [ownedTest]: 'export const changed = true;' });
    const expected = [...added, ownedTest];
    assert.deepEqual(await changedTestFiles({ cwd, base, owned }), expected, 'schema union has exactly the config, doctor and owned tests');
    const current = await fileBaseline(cwd, owned);
    const fallback = { scope: /** @type {const} */ ('related'), files: [ownedTest] };
    const atLimit = changedScope({ baseline, current, fallback, owned, threshold: 3 });
    assert.deepEqual(atLimit.files, expected, 'equal to threshold stays scoped');
    const atLimitFields = scopeFields(atLimit);
    assert.ok(atLimitFields.scope === 'changed');
    assert.deepEqual(atLimitFields.dependents, added);
    const overLimit = changedScope({ baseline, current, fallback, owned, threshold: 2 });
    assert.deepEqual([overLimit.files, overLimit.reason], [[], 'threshold']);
    const overLimitFields = scopeFields(overLimit);
    assert.ok(overLimitFields.scope === 'changed');
    assert.deepEqual(overLimitFields.dependents, added, 'full-suite result retains its explanation');
    assert.deepEqual(expandArgv(['runner', '{test_files}'], overLimit).argv, ['runner']);
    const { dependentTests } = await import('../../src/gates/dependents.mjs');
    assert.deepEqual(await dependentTests({ cwd, changed: [schema], testFiles: [...added, ownedTest, 'test/unrelated.test.mjs'] }), added);
  });
});

test('full fallback keeps the full suite when another changed source has an importing test', async (t) => {
  await withTempDir(async (cwd) => {
    const owned = ['src/unpaired.mjs', 'src/value.mjs'];
    const importer = 'test/consumer.test.mjs';
    await put(cwd, {
      [owned[0]]: 'export const unpaired = 1;',
      [owned[1]]: 'export const value = 1;',
      [importer]: "import { value } from '../src/value.mjs';",
      'test/unrelated.test.mjs': 'export {};',
    });
    baseCommit(cwd);
    const baseline = (await fileBaseline(cwd, owned)).files;
    await put(cwd, {
      [owned[0]]: 'export const unpaired = 2;',
      [owned[1]]: 'export const value = 2;',
    });
    const current = await fileBaseline(cwd, owned);
    for (const [reason, previous] of Object.entries({ unpaired: baseline, no_baseline: null })) {
      await t.test(reason, () => {
        const scope = changedScope({ baseline: previous, current, fallback: { scope: 'full', reason: 'no_tests' }, owned });
        assert.equal(scope.reason, reason);
        assert.deepEqual(scope.dependents, [importer], 'the importing test remains in the explanation');
        assert.deepEqual(scope.files, [], 'the full fallback must cover the source with no callers');
        assert.deepEqual(expandArgv(['runner', '{test_files}'], scope).argv, ['runner']);
        const related = changedScope({ baseline: previous, current, fallback: { scope: 'related', files: ['test/unrelated.test.mjs'] }, owned });
        assert.deepEqual(related.files, [importer, 'test/unrelated.test.mjs'], 'related fallback still merges dependents');
      });
    }
  });
});

test('unreadable and malformed files are skipped, and a changed test does not pull in its importers', async () => {
  await withTempDir(async (cwd) => {
    const good = 'test/good.test.mjs';
    const unreadable = 'test/unreadable.test.mjs';
    const broken = 'test/broken.test.mjs';
    const source = 'src/value.mjs';
    const owned = [source, 'test/helper.test.mjs'];
    await put(cwd, {
      [source]: 'export const value = 1;',
      [good]: "import { value } from '../src/value.mjs';",
      [unreadable]: "import '../src/value.mjs';",
      [broken]: "import { value } from '../src/value.mjs'; /* unclosed",
      'test/helper.test.mjs': 'export {};',
      'test/consumer.test.mjs': "import './helper.test.mjs';",
    });
    const base = baseCommit(cwd);
    await chmod(path.join(cwd, unreadable), 0);
    try {
      await put(cwd, { [source]: 'export const value = 2;', 'test/helper.test.mjs': 'export const value = 2;' });
      assert.deepEqual(await changedTestFiles({ cwd, base, owned }), [good, 'test/helper.test.mjs'], 'only one readable importer and the owned changed test run');
      const { dependentTests } = await import('../../src/gates/dependents.mjs');
      const testFiles = [good, unreadable, broken, 'test/missing.test.mjs', 'test/consumer.test.mjs'];
      assert.deepEqual(await dependentTests({ cwd, changed: [source, 'test/helper.test.mjs'], testFiles }), [good]);
      await rm(path.join(cwd, source));
      assert.deepEqual(await dependentTests({ cwd, changed: [source], testFiles }), [good], 'deleted source still has an importer');
    } finally {
      await chmod(path.join(cwd, unreadable), 0o600);
    }
  });
});
