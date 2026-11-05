import { makeRepo, writeFile } from './review/helpers.mjs';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { stringify } from 'yaml';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const NODE = process.execPath;
const CLI = path.join(REPO, 'bin/code-forge.mjs');
const { runRun } = await import('../src/cli/run.mjs');
const { runBlock } = await import('../src/cli/block.mjs');
const { readAllRows } = await import('../src/ledger/write.mjs');

/** @param {Function} fn @param {string[]} args */
async function setup(fn, args) {
  let out = '', err = '';
  const code = await fn(args, {
    stdout: { write: s => { out += s; } }, stderr: { write: s => { err += s; } },
    probe: async () => 'fixture-start',
  });
  assert.equal(code, 0, out + err);
}

let sequence = 0;
/** A real Node runner in a tiny repo; only the leak-check fixtures are stand-ins.
 * @param {import('node:test').TestContext} t
 */
async function fixture(t) {
  const repo = await makeRepo();
  const slug = `suite-scope-${++sequence}`;
  const stages = path.join(repo, 'stages.jsonl');
  writeFile(repo, 'test/helpers/isolate.mjs', '');
  copyFileSync(path.join(REPO, 'test/helpers/run-suite.sh'), path.join(repo, 'test/helpers/run-suite.sh'));
  copyFileSync(path.join(REPO, 'test/helpers/suite-sandbox.sb'), path.join(repo, 'test/helpers/suite-sandbox.sb'));
  writeFile(repo, 'test/no-leak.test.mjs', `
import { appendFileSync } from 'node:fs';
import { test } from 'node:test';
test('fixture leak check', () => {
  appendFileSync(process.env.SUITE_STAGES, JSON.stringify({
    mode: process.env.CODE_FORGE_NO_LEAK || 'suite', guard: process.env.CODE_FORGE_LEAK_GUARD,
  }) + '\\n');
});
`);
  writeFile(repo, 'test/a.test.mjs', "import { test } from 'node:test'; test('paired a', () => {});\n");
  writeFile(repo, 'test/changed.test.mjs', "import { test } from 'node:test'; test('changed test', () => {});\n");
  writeFile(repo, 'src/shared.mjs', 'export const value = 1;\n');
  writeFile(repo, 'src/bridge.mjs', "export { value } from './shared.mjs';\n");
  writeFile(repo, 'test/dependent.test.mjs', "import { test } from 'node:test'; import '../src/bridge.mjs'; test('dependent test', () => {});\n");
  writeFile(repo, 'test/unrelated.test.mjs', "import { test } from 'node:test'; test('unrelated test', async () => { await new Promise(resolve => setTimeout(resolve, 300)); });\n");
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'src/unrelated.mjs', 'export const unrelated = 1;\n');
  const config = {
    version: 1, provider: 'anthropic', levels: {
      L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' },
    },
  };
  config.project = { slug };
  config.gates = { test: ['sh', 'test/helpers/run-suite.sh', '{test_files}'], lint: null, types: null, format: null };
  writeFile(repo, '.code-forge.yml', stringify(config));
  writeFile(repo, 'package.json', JSON.stringify({ type: 'module', scripts: { test: 'sh test/helpers/run-suite.sh' } }));
  /** @type {NodeJS.ProcessEnv} */
  const env = { ...process.env, CODE_FORGE_LEAK_GUARD: 'snapshot', SUITE_STAGES: stages,
    NODE_OPTIONS: `${process.env.NODE_OPTIONS ?? ''} --test-reporter=tap`,
    PATH: `${path.dirname(NODE)}:${process.env.PATH}` };
  delete env.NODE_TEST_CONTEXT;
  t.after(() => rmSync(repo, { recursive: true, force: true }));
  const calls = () => existsSync(stages) ? readFileSync(stages, 'utf8').trim().split('\n').map(line => JSON.parse(line)) : [];
  const clear = () => rmSync(stages, { force: true });
  const suite = (/** @type {string[]} */ args) => spawnSync('/bin/sh', ['test/helpers/run-suite.sh', ...args], {
    cwd: repo, env, encoding: 'utf8', timeout: 20000,
  });
  return { repo, slug, env, calls, clear, suite };
}

/** @param {string} output */
const counts = output => [...output.matchAll(/^# tests (\d+)$/gm)].map(match => Number(match[1]));

test('suite file arguments select only those tests with both leak steps; no files and flags retain full scope', async t => {
  const f = await fixture(t);
  for (const file of ['test/a.test.mjs', path.join(f.repo, 'test/a.test.mjs')]) {
    f.clear();
    const selected = f.suite(['--test-timeout=10000', '--test-concurrency', '1',
      '--test-name-pattern', 'paired a|../not-a-path', file]);
    assert.equal(selected.status, 0, selected.stdout + selected.stderr);
    assert.deepEqual(counts(selected.stdout), [1, 1, 1], 'snapshot, exactly one selected test, final');
    assert.doesNotMatch(selected.stdout, /unrelated test|changed test|dependent test/);
    assert.deepEqual(f.calls(), [{ mode: 'snapshot', guard: 'snapshot' }, { mode: 'final', guard: 'snapshot' }]);
  }
  for (const args of [[], ['--test-timeout=10000', '--test-concurrency=1'],
    ['--test-name-pattern', '.*|../not-a-path'],
    ['--test-skip-pattern', '../not-a-test', '--test-timeout', '10000']]) {
    f.clear();
    const full = f.suite(args);
    assert.equal(full.status, 0, full.stdout + full.stderr);
    assert.deepEqual(counts(full.stdout), [1, 5, 1], 'the full glob includes four tests and the no-leak file');
    assert.deepEqual(f.calls(), [
      { mode: 'snapshot', guard: 'snapshot' }, { mode: 'suite', guard: 'snapshot' }, { mode: 'final', guard: 'snapshot' },
    ]);
  }
});

test('suite rejects traversal and outside paths before any leak or test step', async t => {
  const f = await fixture(t);
  symlinkSync(path.dirname(f.repo), path.join(f.repo, 'escape'));
  symlinkSync(path.dirname(f.repo), path.join(f.repo, '-escape'));
  const paths = ['../a.test.mjs', 'test/../test/a.test.mjs', `${f.repo}-neighbor/a.test.mjs`, 'escape/a.test.mjs',
    '../evil.mjs', '..', `${f.repo}-neighbor/x.js`, path.dirname(f.repo), 'escape'];
  const inputs = paths.map(file => [file]);
  inputs.push(['--test-name-pattern', '../not-a-path', 'test/a.test.mjs', '../evil.mjs'],
    ['--test-name-pattern=paired a', '../evil.mjs'], ['--test-only', 'escape'], ['--', '-escape']);
  for (const args of inputs) {
    f.clear();
    const rejected = f.suite(args);
    assert.equal(rejected.status, 2, `${JSON.stringify(args)}: ${rejected.stdout}${rejected.stderr}`);
    assert.equal(rejected.stdout, '', 'unsafe input launches no Node test process');
    assert.equal(rejected.stderr.trim().split('\n').length, 1, 'one refusal line');
    assert.match(rejected.stderr, /path.*inside.*repo|repo.*path/i);
    assert.deepEqual(f.calls(), [], 'no leak step runs for unsafe input');
  }
});

test('block changed gates run changed, paired and dependent tests, record counts, and preserve full fallbacks', async t => {
  const f = await fixture(t);
  const run = `r-${f.slug}`;
  const acceptance = path.join(f.repo, 'acceptance.json');
  writeFile(f.repo, 'acceptance.json', JSON.stringify([{ clause: 'fixture', tests: ['test/a.test.mjs'] }]));
  await setup(runRun, ['start', '--cwd', f.repo, '--run', run, '--worker-pid', '4242']);
  await setup(runBlock, ['open', 'B1', '--run', run, '--level', 'L1', '--acceptance', acceptance,
    '--owned', 'src/a.mjs', 'src/shared.mjs', 'test/changed.test.mjs',
    '--tests', 'test/a.test.mjs', 'test/changed.test.mjs', 'test/dependent.test.mjs', 'test/unrelated.test.mjs']);
  const gate = (/** @type {string[]} */ args) => {
    f.clear();
    const started = performance.now();
    const result = spawnSync(NODE, [CLI, 'gates', 'run', '--cwd', f.repo, ...args], {
      cwd: f.repo, env: f.env, encoding: 'utf8', timeout: 20000,
    });
    const elapsed = performance.now() - started;
    assert.equal(result.status, 0, result.stdout + result.stderr);
    const json = JSON.parse(result.stdout);
    assert.equal(json.allOk, true);
    return { result: json.results.find(r => r.gate === 'test'), elapsed };
  };
  gate(['--run', run, '--block', 'B1']);
  writeFile(f.repo, 'src/a.mjs', 'export const a = 2;\n');
  const one = gate(['--run', run, '--block', 'B1', '--changed']);
  t.diagnostic(`one-file changed gate wall time: ${one.elapsed.toFixed(1)} ms; TAP counts: ${counts(one.result.stdout)}`);
  assert.deepEqual(counts(one.result.stdout), [1, 1, 1], 'one source change runs exactly its paired test');
  assert.deepEqual([one.result.scope, one.result.test_count], ['changed', 1]);

  writeFile(f.repo, 'src/a.mjs', 'export const a = 3;\n');
  writeFile(f.repo, 'src/shared.mjs', 'export const value = 2;\n');
  writeFile(f.repo, 'test/changed.test.mjs', "import { test } from 'node:test'; test('changed test v2', () => {});\n");
  const impacted = gate(['--run', run, '--block', 'B1', '--changed']).result;
  assert.deepEqual(impacted.argv, ['sh', 'test/helpers/run-suite.sh', 'test/a.test.mjs', 'test/changed.test.mjs', 'test/dependent.test.mjs']);
  assert.deepEqual(counts(impacted.stdout), [1, 3, 1]);
  assert.doesNotMatch(impacted.stdout, /unrelated test/);
  assert.deepEqual([impacted.scope, impacted.test_count, impacted.dependents], ['changed', 3, ['test/dependent.test.mjs']]);
  const rows = (await readAllRows(f.slug)).filter(row => row.event === 'gate.done');
  assert.deepEqual(rows.map(row => [row.scope, row.test_count]), [['related', 4], ['changed', 1], ['changed', 3]]);

  await setup(runBlock, ['open', 'B2', '--run', run, '--level', 'L1', '--acceptance', acceptance, '--owned', 'src/unrelated.mjs']);
  const fallback = gate(['--run', run, '--block', 'B2', '--changed']).result;
  assert.deepEqual([fallback.scope, fallback.test_count, fallback.reason], ['changed', 0, 'no_baseline']);
  assert.deepEqual(fallback.argv, ['sh', 'test/helpers/run-suite.sh']);
  assert.deepEqual(counts(fallback.stdout), [1, 5, 1]);
  const plain = gate([]).result;
  assert.deepEqual([plain.scope, plain.reason, plain.argv], ['full', 'no_tests', ['sh', 'test/helpers/run-suite.sh']]);
  assert.deepEqual(counts(plain.stdout), [1, 5, 1]);
});
