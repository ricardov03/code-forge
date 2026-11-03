import './support.mjs';
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test, { after } from 'node:test';
import { runGates } from '../../src/gates/run.mjs';
import { runGatesVerb } from '../../src/cli/gates.mjs';
import { runLines, statusOf } from '../../src/cli/status.mjs';
import { runReport } from '../../src/cli/report.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { validateConfig } from '../../src/config/validate.mjs';
import { renderConfigDoc } from '../../scripts/gen-config-doc.mjs';
import { captureStream, withTempDir } from './support.mjs';

// Nested Node test runners must not inherit the outer worker's child context.
const nodeTestContext = process.env.NODE_TEST_CONTEXT;
delete process.env.NODE_TEST_CONTEXT;
after(() => { if (nodeTestContext !== undefined) process.env.NODE_TEST_CONTEXT = nodeTestContext; });

/** @param {Function} fn @param {string[]} args */
async function cli(fn, args) {
  const stdout = captureStream(), stderr = captureStream();
  const code = await fn(args, { stdout, stderr });
  return { code, out: stdout.text, err: stderr.text };
}

const FILES = ['a.test.mjs', 'b space.test.mjs'];
let sequence = 0;

/** Real Node tests log every invocation and fail deterministically before their first rerun.
 * @param {string} cwd @param {boolean} [always]
 */
async function nodeFixture(cwd, always = false) {
  for (const file of [...FILES, 'passing.test.mjs']) {
    await writeFile(path.join(cwd, file), `
import assert from 'node:assert/strict';
import { appendFileSync, existsSync, writeFileSync } from 'node:fs';
import { test } from 'node:test';
const file = ${JSON.stringify(file)};
const marker = file + '.seen';
const seen = existsSync(marker);
appendFileSync('calls.jsonl', JSON.stringify({ file, argv: process.execArgv }) + '\\n');
writeFileSync(marker, 'seen');
test('suite', async (t) => {
  await t.test('same assertion name', () => {
    assert.ok(file === 'passing.test.mjs' || (seen && ${!always}), 'deliberate fixture assertion');
  });
});
`);
  }
  return async () => (await readFile(path.join(cwd, 'calls.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
}

test('failed test gates rerun only failed files serially once and publish flaky files in results and ledger', async () => {
  for (const reporter of ['tap', 'spec']) await withTempDir(async (cwd) => {
    const calls = await nodeFixture(cwd);
    const slug = `rerun-node-${++sequence}`;
    const argv = [process.execPath, '--test', '--test-reporter', reporter, '--test-concurrency', '3', '*.test.mjs', ...(reporter === 'spec' ? ['{test_files}'] : [])];
    await writeFile(path.join(cwd, '.code-forge.yml'), JSON.stringify({ version: 1, gates: { test: argv } }));
    const ran = await cli(runGatesVerb, ['run', '--cwd', cwd, '--slug', slug, '--json']);
    assert.equal(ran.code, 0, `recovered ${reporter} test gate must exit 0: ${ran.err}\n${ran.out}`);
    const json = JSON.parse(ran.out), result = json.results[0];
    assert.deepEqual([json.allOk, result.ok, result.code], [true, true, 0]);
    assert.deepEqual(result.flaky, FILES, 'exactly two failed files, sorted and deduplicated');
    assert.deepEqual(json.flaky, FILES);
    const invocations = await calls();
    assert.deepEqual(invocations.slice(0, 3).map((call) => call.file).sort(), [...FILES, 'passing.test.mjs'].sort());
    assert.deepEqual(invocations.slice(3).map((call) => call.file), FILES, 'passing file must not rerun; each failed file runs once');
    for (const call of invocations.slice(3)) assert.deepEqual(call.argv.filter((arg) => arg.startsWith('--test-concurrency')), ['--test-concurrency=1']);
    const rows = await readAllRows(slug);
    assert.deepEqual(rows.map((row) => row.event), ['gate.done'], 'recovered gate must never emit gate.red');
    assert.deepEqual(rows[0].flaky, FILES);
    assert.deepEqual(rows[0].results[0].flaky, FILES);
    assert.equal(rows[0].all_ok, true);
    assert.equal(result.duration_ms, rows[0].duration_ms);
  });

  // An arbitrary runner can narrow only through its original whole-item token.
  await withTempDir(async (cwd) => {
    for (const file of FILES) await writeFile(path.join(cwd, file), 'export {};\n');
    await writeFile(path.join(cwd, 'runner.mjs'), `
import { appendFileSync } from 'node:fs';
appendFileSync('argv.jsonl', JSON.stringify(process.argv.slice(2)) + '\\n');
if (process.argv.length === 2) {
  console.log('FAILED a.test.mjs::first - AssertionError');
  console.error('FAILED b space.test.mjs::second - AssertionError');
  process.exitCode = 1;
}
`);
    const ran = await runGates({ test: { argv: [process.execPath, 'runner.mjs', '{test_files}'], env: { RERUN_MODE: 'fixture' } } }, { cwd });
    assert.equal(ran.allOk, true, 'token runner must recover');
    assert.deepEqual(ran.results[0].flaky, FILES);
    assert.deepEqual((await readFile(path.join(cwd, 'argv.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line)), [[], [FILES[0]], [FILES[1]]]);
    assert.deepEqual(ran.results[0].env_keys, ['RERUN_MODE']);
    await writeFile(path.join(cwd, '.code-forge.yml'), JSON.stringify({ version: 1, gates: { test: [process.execPath, 'runner.mjs', '{test_files}'] } }));
    const text = await cli(runGatesVerb, ['run', '--cwd', cwd]);
    assert.equal(text.code, 0, text.err);
    assert.equal(text.err, `flaky: ${FILES.join(', ')}\n`, 'text output has exactly one flaky line');
  });
});

test('second failure stays red; disabled, unparseable and unnarrowable gates execute only once', async () => {
  for (const disabled of [false, true]) await withTempDir(async (cwd) => {
    const calls = await nodeFixture(cwd, true);
    await writeFile(path.join(cwd, '.code-forge.yml'), JSON.stringify({ version: 1, gates: {
      test: [process.execPath, '--test', '--test-reporter=tap', ...FILES], ...(disabled ? { rerun_failed: 0 } : {}),
    } }));
    const ran = await cli(runGatesVerb, ['run', '--cwd', cwd]);
    const result = JSON.parse(ran.out);
    assert.deepEqual([ran.code, result.allOk, result.results[0].ok], [1, false, false]);
    const counts = (await calls()).reduce((all, call) => ({ ...all, [call.file]: (all[call.file] ?? 0) + 1 }), {});
    assert.deepEqual(counts, Object.fromEntries(FILES.map((file) => [file, disabled ? 1 : 2])), 'one rerun per failed file unless disabled');
    assert.equal(Object.hasOwn(result.results[0], 'flaky'), false, 'persistent failures must not be labelled recovered');
  });
  for (const { token, output, gate } of [
    { token: false, output: 'FAILED a.test.mjs::case - AssertionError', gate: 'test' },
    { token: true, output: 'unknown failure without a file', gate: 'test' },
    { token: true, output: "not ok 1 - known\n  ---\n  location: 'a.test.mjs:1:1'\n  ...\nnot ok 2 - unmapped assertion\n  ---\n  error: 'AssertionError'\n  ...", gate: 'test' },
    { token: true, output: 'FAILED a.test.mjs::case - AssertionError', gate: 'lint' },
  ]) await withTempDir(async (cwd) => {
    await writeFile(path.join(cwd, 'a.test.mjs'), 'export {};\n');
    await writeFile(path.join(cwd, 'runner.mjs'), `
import { appendFileSync } from 'node:fs';
appendFileSync('calls', 'called\\n');
console.error(${JSON.stringify(output)});
process.exitCode = 1;
`);
    const ran = await runGates({ [gate]: [process.execPath, 'runner.mjs', ...(token ? ['{test_files}'] : [])] }, { cwd });
    assert.equal(ran.allOk, false);
    assert.equal(await readFile(path.join(cwd, 'calls'), 'utf8'), 'called\n', `${gate}, token=${token}: exactly one invocation`);
    assert.equal(Object.hasOwn(ran.results.find((result) => result.gate === gate), 'flaky'), false);
  });
});

test('status and report retain a run’s flaky files once, including after a later clean gate', async () => {
  await withTempDir(async (workspace) => {
    const slug = `rerun-report-${++sequence}`, run = 'r-flaky-fixture';
    const rows = [
      { event: 'gate.done', run, block: 'B1', all_ok: true, flaky: FILES },
      { event: 'gate.done', run, block: 'B1', all_ok: true, flaky: [FILES[0]] },
      { event: 'gate.done', run, block: 'B1', all_ok: true },
      { event: 'gate.done', run: 'r-other-fixture', block: 'B1', all_ok: true },
    ];
    const summary = await statusOf({ run_id: run, workspace, blocks: { B1: { status: 'open', level: 'L2' } } }, rows);
    assert.deepEqual(summary.flaky, FILES, 'run status has exactly its two unique recovered files');
    assert.deepEqual(runLines(summary).filter((line) => line.startsWith('flaky:')), [`flaky: ${FILES.join(', ')}`]);
    const clean = await statusOf({ run_id: 'r-other-fixture', workspace, blocks: {} }, rows);
    assert.equal(Object.hasOwn(clean, 'flaky'), false, 'other runs must not inherit flaky files');
    assert.deepEqual(runLines(clean).filter((line) => line.startsWith('flaky:')), []);
    for (const row of rows) await appendRow(row, { slug });
    const report = await cli(runReport, ['--slug', slug]);
    assert.equal(report.code, 0, report.err);
    assert.deepEqual(report.out.split('\n').filter((line) => line.includes('flaky:')), [`run ${run} flaky: ${FILES.join(', ')}`]);
    const json = await cli(runReport, ['--slug', slug, '--json']);
    assert.equal(json.code, 0, json.err);
    assert.deepEqual(JSON.parse(json.out).flaky, [{ run, files: FILES }]);
  });
});

test('rerun_failed schema accepts only 0 or 1, defaults to 1 and appears once in the generated config reference', async () => {
  const base = { version: 1, provider: 'anthropic', levels: Object.fromEntries(['L0', 'L1', 'L2', 'L3'].map((level) => [level, { model: `fixture-model-${level}` }])) };
  for (const rerun_failed of [0, 1]) {
    const validated = validateConfig({ ...base, gates: { rerun_failed } });
    assert.deepEqual(validated.errors, [], `rerun_failed=${rerun_failed} must be accepted`);
  }
  for (const rerun_failed of [-1, 2, 0.5, '1', null]) {
    const validated = validateConfig({ ...base, gates: { rerun_failed } });
    assert.equal(validated.valid, false, `rerun_failed=${rerun_failed} must be refused`);
    assert.equal(validated.errors.filter((error) => error.rule === 'schema' && error.path === '/gates/rerun_failed').length, 1);
  }
  const doc = renderConfigDoc();
  const rows = doc.split('\n').filter((line) => line.startsWith('| `gates.rerun_failed` |'));
  assert.equal(rows.length, 1, 'exactly one documented config key');
  assert.match(rows[0], /\| integer \|\s*\| `1` \| min 0; max 1;/);
  assert.equal(await readFile(new URL('../../docs/reference/config.md', import.meta.url), 'utf8'), doc);
});
