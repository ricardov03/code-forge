import { commitAll, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runGatesVerb } = await import('../../src/cli/gates.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');
const { runGates } = await import('../../src/gates/run.mjs');
const { changedScope, expandArgv, fileBaseline, hasScopeItem, scopeFields } = await import('../../src/gates/expand.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');

/** @param {Function} fn @param {string[]} args */
async function cli(fn, args) {
  let out = '', err = '';
  const code = await fn(args, { stdout: { write: (/** @type {string} */ s) => { out += s; } }, stderr: { write: (/** @type {string} */ s) => { err += s; } }, probe: async () => 'fake-start' });
  return { code, out, err, json: out.startsWith('{') ? JSON.parse(out) : null };
}

let seq = 0;
/** @param {number} [threshold] @param {boolean} [unpaired] @param {string[]} [ownedEntries] */
async function fixture(threshold = 10, unpaired = false, ownedEntries) {
  const repo = await makeRepo();
  const slug = `fake-b99-${++seq}`, run = `r-${slug}`;
  const probe = path.join(repo, 'probe.mjs');
  writeFile(repo, 'probe.mjs', 'console.log(JSON.stringify(process.argv.slice(2)));\n');
  const command = (/** @type {string} */ token) => JSON.stringify([process.execPath, probe, token]);
  writeFile(repo, '.code-forge.yml', [
    'version: 1', `project: {slug: ${slug}}`, 'provider: anthropic', 'engine: harness',
    'levels:', '  L0: {model: claude-haiku-4-5-20251001}', '  L1: {model: claude-sonnet-5}', '  L2: {model: claude-opus-5-5}', '  L3: {model: claude-fable-5-1}',
    'gates:', `  test: ${command('{test_files}')}`, `  lint: ${command('{changed_files}')}`, `  types: ${command('all')}`, '  format: null', `  full_suite_threshold_files: ${threshold}`, '',
  ].join('\n'));
  const files = [unpaired ? 'src/c.mjs' : 'src/a.mjs', 'src/b.mjs', 'test/a.test.mjs', 'test/b.test.mjs'];
  const owned = ownedEntries ?? files;
  for (const file of files) writeFile(repo, file, 'export {};\n');
  const acc = path.join(freshDir('acceptance'), 'acc.json');
  writeFile(path.dirname(acc), 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
  const started = await cli(runRun, ['start', '--cwd', repo, '--run', run, '--worker-pid', '4242']);
  assert.equal(started.code, 0, started.err);
  const opened = await cli(runBlock, ['open', 'B1', '--run', run, '--level', 'L2', '--acceptance', acc, '--owned', ...owned]);
  assert.equal(opened.code, 0, opened.err);
  const gate = (/** @type {boolean} */ changed = false, /** @type {boolean} */ omitSlug = false) => cli(runGatesVerb, ['run', '--cwd', repo, ...(omitSlug ? [] : ['--slug', slug]), '--run', run, '--block', 'B1', '--timeout-ms', '5000', ...(changed ? ['--changed'] : [])]);
  const rows = async () => (await readAllRows(slug)).filter((r) => r.event === 'gate.done');
  return { repo, owned, gate, rows };
}

test('B99 reproduction: green baseline records four hashes; a fix runs one paired test and two lint files', async () => {
  const fx = await fixture();
  const first = await fx.gate(false, true);
  assert.equal(first.code, 0, first.err);
  assert.deepEqual(first.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [['test/a.test.mjs', 'test/b.test.mjs'], [], ['all'], null]);
  const hash = createHash('sha256').update('export {};\n').digest('hex');
  assert.deepEqual((await fx.rows()).map((r) => r.files), [{ 'src/a.mjs': hash, 'src/b.mjs': hash, 'test/a.test.mjs': hash, 'test/b.test.mjs': hash }]);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(fx.repo, 'test/a.test.mjs', 'export const t = 2;\n');
  const fixed = await fx.gate(true);
  assert.equal(fixed.code, 0, fixed.err);
  assert.deepEqual(fixed.json.results.map((/** @type {any} */ r) => [r.gate, r.skipped ? null : JSON.parse(r.stdout)]), [
    ['test', ['test/a.test.mjs']], ['lint', ['src/a.mjs', 'test/a.test.mjs']], ['types', ['all']], ['format', null],
  ]);
  const rows = await fx.rows();
  assert.equal(rows.length, 2);
  assert.deepEqual([rows[1].scope, rows[1].test_count, rows[1].reason], ['changed', 1, undefined]);
  assert.equal(readFileSync(path.join(fx.repo, 'src/b.mjs'), 'utf8'), 'export {};\n');
  writeFile(fx.repo, 'src/b.mjs', 'export const b = 3;\n');
  const sourceOnly = await fx.gate(true);
  assert.equal(sourceOnly.code, 0, sourceOnly.err);
  assert.deepEqual(sourceOnly.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [['test/b.test.mjs'], ['src/b.mjs'], ['all'], null]);
  assert.equal((await fx.rows()).length, 3);
});

test('B99 no green baseline: existing two-test scope and no_baseline on the row', async () => {
  const fx = await fixture();
  const ran = await fx.gate(true);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(ran.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [['test/a.test.mjs', 'test/b.test.mjs'], [], ['all'], null]);
  assert.deepEqual((await fx.rows()).map((r) => [r.scope, r.test_count, r.reason]), [['changed', 2, 'no_baseline']]);
});

test('B99 unpaired source: existing two-test scope and unpaired on the row', async () => {
  const fx = await fixture(10, true);
  assert.equal((await fx.gate()).code, 0);
  writeFile(fx.repo, 'src/c.mjs', 'export const c = 2;\n');
  const ran = await fx.gate(true);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(JSON.parse(ran.json.results[0].stdout), ['test/a.test.mjs', 'test/b.test.mjs']);
  assert.deepEqual(JSON.parse(ran.json.results[1].stdout), ['src/c.mjs']);
  assert.deepEqual((await fx.rows()).map((r) => [r.scope, r.reason]), [['related', undefined], ['changed', 'unpaired']]);
});

test('B99 deleted source still pairs; deleted tests and sources never reach either token', () => {
  // Mutation deleted-paths: use all changes for changedFiles, including deleted paths.
  const hash = createHash('sha256').update('old').digest('hex');
  const baseline = { 'src/a.mjs': hash, 'test/b.test.mjs': hash, 'test/a.test.mjs': hash };
  const current = { files: { 'test/a.test.mjs': hash }, candidates: ['test/a.test.mjs'] };
  const scope = changedScope({ baseline, current, fallback: { scope: 'related', files: ['test/a.test.mjs', 'test/b.test.mjs'] } });
  assert.deepEqual(scope, { scope: 'changed', files: ['test/a.test.mjs'], changedFiles: [] });
  assert.deepEqual(expandArgv(['test', '{test_files}'], scope).argv, ['test', 'test/a.test.mjs']);
  assert.deepEqual(expandArgv(['lint', '{changed_files}'], scope).argv, ['lint']);
  assert.deepEqual(changedScope({ baseline, current, fallback: { scope: 'full', reason: 'threshold' }, threshold: 1 }), {
    scope: 'changed', files: [], changedFiles: [], reason: 'threshold',
  });
});

test('B99 deleted test alone falls back to existing tests with no_changes', () => {
  // Mutation removed-test-baseline: classify a removed test as no_baseline.
  const hash = createHash('sha256').update('old').digest('hex');
  const scope = changedScope({
    baseline: { 'test/a.test.mjs': hash, 'test/b.test.mjs': hash },
    current: { files: { 'test/b.test.mjs': hash }, candidates: ['test/b.test.mjs'] },
    fallback: { scope: 'related', files: ['test/a.test.mjs', 'test/b.test.mjs'] },
  });
  assert.deepEqual(scope, { scope: 'changed', files: ['test/b.test.mjs'], changedFiles: [], reason: 'no_changes' });
  assert.deepEqual(expandArgv(['test', '{test_files}'], scope).argv, ['test', 'test/b.test.mjs']);
  assert.deepEqual(scopeFields(scope), { scope: 'changed', test_count: 1, reason: 'no_changes' });
});

test('B99 tracked source deleted without git rm runs its existing paired test through gates run', async () => {
  // Mutation tracked-source-enoent: treat lstat ENOENT as a baseline failure.
  // Mutation source-scoped-results: omit scope from the test or lint result.
  const fx = await fixture();
  await commitAll(fx.repo);
  assert.equal((await fx.gate()).code, 0);
  rmSync(path.join(fx.repo, 'src/a.mjs'));
  const ran = await fx.gate(true);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(ran.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [['test/a.test.mjs'], [], ['all'], null]);
  const scopedResults = ran.json.results.filter((/** @type {any} */ r) => r.scope);
  assert.equal(scopedResults.length, 2);
  assert.deepEqual(scopedResults.map((/** @type {any} */ r) => r.gate), ['test', 'lint']);
  for (const result of scopedResults) {
    assert.deepEqual([result.scope, result.test_count, result.reason], ['changed', 1, undefined]);
  }
  const rows = await fx.rows();
  assert.deepEqual([rows[1].scope, rows[1].test_count, rows[1].reason], ['changed', 1, undefined]);
  assert.equal(Object.hasOwn(rows[1].files, 'src/a.mjs'), false);
  assert.equal(Object.keys(rows[1].files).length, 3);
});

test('B99 tracked test deleted without git rm records no_changes through gates run', async () => {
  // Mutation tracked-test-candidate: retain a missing listed file in candidates.
  // Mutation deleted-test-scoped-results: omit scope from the test or lint result.
  const fx = await fixture();
  await commitAll(fx.repo);
  assert.equal((await fx.gate()).code, 0);
  rmSync(path.join(fx.repo, 'test/a.test.mjs'));
  const ran = await fx.gate(true);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(ran.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [['test/b.test.mjs'], [], ['all'], null]);
  const scopedResults = ran.json.results.filter((/** @type {any} */ r) => r.scope);
  assert.equal(scopedResults.length, 2);
  assert.deepEqual(scopedResults.map((/** @type {any} */ r) => r.gate), ['test', 'lint']);
  for (const result of scopedResults) {
    assert.deepEqual([result.scope, result.test_count, result.reason], ['changed', 1, 'no_changes']);
  }
  const rows = await fx.rows();
  assert.deepEqual([rows[1].scope, rows[1].test_count, rows[1].reason], ['changed', 1, 'no_changes']);
  assert.equal(Object.hasOwn(rows[1].files, 'test/a.test.mjs'), false);
  assert.equal(Object.keys(rows[1].files).length, 3);
});

test('B99 tracked paths below a replaced directory are missing for owned hashes and non-owned candidates', async () => {
  // Mutation owned-enotdir: treat an owned lstat ENOTDIR as a baseline failure.
  // Mutation candidate-enotdir: retain or reject a non-owned candidate whose parent is a file.
  const repo = await makeRepo();
  writeFile(repo, 'src/a.mjs', 'export {};\n');
  writeFile(repo, 'test/a.test.mjs', 'export {};\n');
  await commitAll(repo);
  rmSync(path.join(repo, 'src'), { recursive: true });
  writeFile(repo, 'src', 'replacement\n');
  const hash = createHash('sha256').update('export {};\n').digest('hex');
  const expected = { files: { 'test/a.test.mjs': hash }, candidates: ['src', 'test/a.test.mjs'] };
  for (const owned of [['src/a.mjs', 'test/a.test.mjs'], ['test/a.test.mjs']]) {
    const baseline = await fileBaseline(repo, owned);
    assert.ok(baseline);
    assert.deepEqual({ ...baseline, files: { ...baseline.files } }, expected);
  }
});

test('B99 not-yet-created owned entries keep the green baseline through gates run', async () => {
  // Mutation unmatched-owned-entry: refuse a baseline if any owned entry has no matching file.
  const fx = await fixture(10, false, ['src/a.mjs', 'test/a.test.mjs', 'src/future.mjs', 'future', 'pending/*.mjs']);
  const green = await fx.gate();
  assert.equal(green.code, 0, green.err);
  const hash = createHash('sha256').update('export {};\n').digest('hex');
  assert.deepEqual((await fx.rows())[0].files, { 'src/a.mjs': hash, 'test/a.test.mjs': hash });
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  const ran = await fx.gate(true);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(ran.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [['test/a.test.mjs'], ['src/a.mjs'], ['all'], null]);
  const rows = await fx.rows();
  assert.deepEqual([rows[1].scope, rows[1].test_count, rows[1].reason], ['changed', 1, undefined]);
});

test('B99 unchanged green baseline runs today\'s tests and records no_changes', async () => {
  // Mutation no-changes-fallback: remove the empty-test fallback in changedScope.
  const fx = await fixture();
  assert.equal((await fx.gate()).code, 0);
  const ran = await fx.gate(true);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(ran.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [['test/a.test.mjs', 'test/b.test.mjs'], [], ['all'], null]);
  assert.deepEqual((await fx.rows()).map((r) => [r.scope, r.test_count, r.reason]), [['related', 2, undefined], ['changed', 2, 'no_changes']]);
});

test('B99 missing gates have no scope item', () => {
  // Mutation missing-scope-guard: remove the nullish guard from hasScopeItem.
  assert.equal(hasScopeItem(undefined), false);
  assert.equal(hasScopeItem(null), false);
  assert.equal(hasScopeItem({ lint: ['lint', '{changed_files}'] }), true);
});

test('B99 empty argv error names both dropped scope tokens before running gates', async () => {
  // Mutation dropped-token-message: name only {test_files} in the empty-argv error.
  const repo = await makeRepo();
  const marker = path.join(repo, 'should-not-run');
  await assert.rejects(runGates({
    test: [process.execPath, '-e', `require('node:fs').writeFileSync(${JSON.stringify(marker)}, 'ran')`],
    lint: ['{test_files}', '{changed_files}'],
  }, { cwd: repo }), {
    name: 'TypeError',
    message: "gates.lint: the argv is empty once '{test_files}' and '{changed_files}' is dropped for the full suite — put the test command before it",
  });
  assert.equal(existsSync(marker), false);
});

test('B99 over 200 owned files leaves no green files record; next changed run has no_baseline', async () => {
  // Mutation baseline-cap: raise fileBaseline's cap from 200 to 201.
  const fx = await fixture(1000, false, ['src', 'test']);
  for (let i = 0; i < 197; i++) writeFile(fx.repo, `src/extra${i}.mjs`, 'export {};\n');
  const green = await fx.gate(false, true);
  assert.equal(green.code, 0, green.err);
  const first = (await fx.rows())[0];
  assert.equal(first.all_ok, true);
  assert.equal(Object.hasOwn(first, 'files'), false);
  const changed = await fx.gate(true, true);
  assert.equal(changed.code, 0, changed.err);
  assert.deepEqual((await fx.rows()).map((r) => [r.all_ok, Object.hasOwn(r, 'files'), r.scope, r.reason]), [
    [true, false, 'full', 'no_tests'], [true, false, 'changed', 'no_baseline'],
  ]);
});

test('B99 threshold: two changes above one run full test and lint scope', async () => {
  const fx = await fixture(1);
  assert.equal((await fx.gate()).code, 0);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(fx.repo, 'test/a.test.mjs', 'export const t = 2;\n');
  const ran = await fx.gate(true);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(ran.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [[], [], ['all'], null]);
  assert.deepEqual((await fx.rows()).map((r) => [r.scope, r.test_count, r.reason]), [['full', undefined, 'threshold'], ['changed', 0, 'threshold']]);
});

test('B99 changes equal to threshold stay changed through gates run', async () => {
  // Mutation threshold-equality: fall back when changes.length >= threshold.
  // Mutation threshold-scoped-results: omit scope from the test or lint result.
  const fx = await fixture(2);
  assert.equal((await fx.gate()).code, 0);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(fx.repo, 'test/a.test.mjs', 'export const t = 2;\n');
  const ran = await fx.gate(true);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(ran.json.results.map((/** @type {any} */ r) => r.skipped ? null : JSON.parse(r.stdout)), [['test/a.test.mjs'], ['src/a.mjs', 'test/a.test.mjs'], ['all'], null]);
  const scopedResults = ran.json.results.filter((/** @type {any} */ r) => r.scope);
  assert.equal(scopedResults.length, 2);
  assert.deepEqual(scopedResults.map((/** @type {any} */ r) => r.gate), ['test', 'lint']);
  for (const result of scopedResults) {
    assert.deepEqual([result.scope, result.test_count, result.reason], ['changed', 1, undefined]);
  }
  const rows = await fx.rows();
  assert.deepEqual([rows[1].scope, rows[1].test_count, rows[1].reason], ['changed', 1, undefined]);
});

test('B99 gates without tokens keep identical results; --changed requires a block', async () => {
  const fx = await fixture();
  const probe = path.join(fx.repo, 'probe.mjs');
  const plain = { test: [process.execPath, probe, 'all'] };
  const full = await runGates(plain, { cwd: fx.repo, timeoutMs: 5000 });
  const narrowed = await runGates(plain, { cwd: fx.repo, timeoutMs: 5000, scope: { scope: 'changed', files: ['test/a.test.mjs'], changedFiles: ['src/a.mjs'] } });
  assert.deepEqual(narrowed, full);
  assert.equal(full.results.length, 4);
  const refused = await cli(runGatesVerb, ['run', '--cwd', fx.repo, '--changed']);
  assert.deepEqual(refused, { code: 2, out: '', err: 'gates run: --changed needs --run <id> and --block <id>\n', json: null });
  assert.equal((await fx.rows()).length, 0);
});

test('B99 changed token cannot be the only item or command; later bad gate runs nothing', async () => {
  const fx = await fixture();
  const { parse } = await import('yaml');
  const config = parse(readFileSync(path.join(fx.repo, '.code-forge.yml'), 'utf8'));
  const marker = path.join(fx.repo, 'should-not-run');
  for (const argv of [['{changed_files}'], ['{changed_files}', 'x']]) {
    config.gates.lint = { argv };
    const result = validateConfig(config);
    assert.equal(result.valid, false);
    assert.deepEqual(result.errors.map((e) => [e.rule, e.message]), [['gates-not-argv-array', argv.length === 1
      ? "gates.lint: '{changed_files}' cannot be the only item (the full suite would run an empty command)"
      : "gates.lint: '{changed_files}' cannot be the command (argv[0])"]]);
    await assert.rejects(runGates({ test: [process.execPath, '-e', `require('node:fs').writeFileSync(${JSON.stringify(marker)}, 'ran')`], lint: argv }, { cwd: fx.repo, timeoutMs: 5000 }), { name: 'TypeError', message: "gates.lint: '{changed_files}' cannot be the command (argv[0])" });
    assert.equal(existsSync(marker), false);
  }
});

test('B99 baselines cap at 200 files, strip inherited git env, and refuse symlinks', async () => {
  const repo = await makeRepo();
  for (let i = 0; i < 200; i++) writeFile(repo, `src/f${i}.mjs`, 'export {};\n');
  const inherited = process.env.GIT_DIR;
  process.env.GIT_DIR = path.join(repo, 'missing-git');
  try {
    const baseline = await fileBaseline(repo, ['src']);
    assert.equal(Object.keys(baseline?.files ?? {}).length, 200);
    writeFile(repo, 'src/extra.mjs', 'export {};\n');
    assert.equal(await fileBaseline(repo, ['src']), null);
    rmSync(path.join(repo, 'src/extra.mjs'));
    rmSync(path.join(repo, 'src/f0.mjs'));
    symlinkSync(path.join(repo, 'src/f1.mjs'), path.join(repo, 'src/f0.mjs'));
    assert.equal(await fileBaseline(repo, ['src']), null);
  } finally {
    if (inherited === undefined) delete process.env.GIT_DIR;
    else process.env.GIT_DIR = inherited;
  }
});
