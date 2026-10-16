import './support.mjs';
import assert from 'node:assert/strict';
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { runGates } from '../../src/gates/run.mjs';
import { runGatesVerb } from '../../src/cli/gates.mjs';
import { runRun } from '../../src/cli/run.mjs';
import { runBlock } from '../../src/cli/block.mjs';
import { readAllRows } from '../../src/ledger/write.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { captureStream, git, withTempDir } from './support.mjs';

let repeatSeq = 0;
/** @param {Function} fn @param {string[]} args */
async function repeatCli(fn, args) {
  const stdout = captureStream(), stderr = captureStream();
  const code = await fn(args, { stdout, stderr, probe: async () => 'fake-repeat-start' });
  return { code, out: stdout.text, err: stderr.text };
}

/**
 * A deterministic fake runner: its second invocation can fail, then later invocations pass.
 * @param {string} dir
 * @param {{n?: number, token?: boolean, mutate?: boolean, failAt?: number}} [opts]
 */
async function repeatFixture(dir, { n = 2, token = true, mutate = true, failAt = 0 } = {}) {
  const slug = `fake-repeat-${++repeatSeq}`, run = `r-${slug}`;
  git(['init', '-q'], dir);
  await mkdir(path.join(dir, 'test'));
  await mkdir(path.join(dir, 'src'));
  for (const file of ['a.test.mjs', 'b.test.mjs', 'deleted.test.mjs']) await writeFile(path.join(dir, 'test', file), 'export {};\n');
  await writeFile(path.join(dir, 'src', 'b.mjs'), 'export {};\n');
  git(['add', '-A'], dir);
  git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'base'], dir);
  await writeFile(path.join(dir, 'fake.mjs'), `
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
const log = existsSync('calls.json') ? JSON.parse(readFileSync('calls.json', 'utf8')) : [];
log.push(process.argv.slice(2));
writeFileSync('calls.json', JSON.stringify(log));
if (log.length === ${failAt}) {
  console.log('failure stdout tail');
  console.error('failure stderr tail');
  process.exit(1);
}
console.log('passed ' + log.length);
`);
  const config = (/** @type {number | undefined} */ count) => [
    'version: 1', `project: {slug: ${slug}}`, 'provider: anthropic', 'engine: harness',
    'levels:', '  L0: {model: claude-haiku-4-5-20251001}', '  L1: {model: claude-sonnet-5}', '  L2: {model: claude-opus-5-5}', '  L3: {model: claude-fable-5-1}',
    'gates:', `  test: ${JSON.stringify([process.execPath, path.join(dir, 'fake.mjs'), ...(token ? ['{test_files}'] : [])])}`,
    '  lint: null', '  types: null', '  format: null', '  full_suite_threshold_files: 1',
    ...(count === undefined ? [] : [`  repeat_new_tests: ${count}`]), '',
  ].join('\n');
  const configure = (/** @type {number | undefined} */ count) => writeFile(path.join(dir, '.code-forge.yml'), config(count));
  await configure(n);
  await writeFile(path.join(dir, 'acceptance.json'), JSON.stringify([{ clause: 'fake', tests: ['fake test'] }]));
  const started = await repeatCli(runRun, ['start', '--cwd', dir, '--run', run, '--worker-pid', '4242']);
  assert.equal(started.code, 0, started.err);
  const opened = await repeatCli(runBlock, ['open', 'B1', '--run', run, '--level', 'L2', '--acceptance', path.join(dir, 'acceptance.json'), '--owned', 'test', 'src']);
  assert.equal(opened.code, 0, opened.err);
  if (mutate) {
    await writeFile(path.join(dir, 'test', 'a.test.mjs'), 'export const changed = true;\n');
    await writeFile(path.join(dir, 'test', 'new.test.mjs'), 'export {};\n');
    await rm(path.join(dir, 'test', 'deleted.test.mjs'));
    await writeFile(path.join(dir, 'src', 'b.mjs'), 'export const sourceChanged = true;\n');
    await writeFile(path.join(dir, 'foreign.test.mjs'), 'export {};\n');
    // Cover committed changes as well as untracked additions; the base remains the open commit.
    git(['add', 'test/a.test.mjs'], dir);
    git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'changed test'], dir);
  }
  return {
    run, configure,
    gate: () => repeatCli(runGatesVerb, ['run', '--cwd', dir, '--run', run, '--block', 'B1']),
    calls: async () => JSON.parse(await readFile(path.join(dir, 'calls.json'), 'utf8')),
    rows: async () => (await readAllRows(slug)).filter((r) => r.event.startsWith('gate.')),
  };
}

const REPEATED_FILES = ['test/a.test.mjs', 'test/new.test.mjs'];

test('B136: repeat N additional times only for added or changed block tests, even above the scope threshold', async () => {
  await withTempDir(async (dir) => {
    const fx = await repeatFixture(dir, { n: 2 });
    const ran = await fx.gate();
    assert.equal(ran.code, 0, ran.err);
    assert.deepEqual(await fx.calls(), [[], REPEATED_FILES, REPEATED_FILES], 'unchanged, deleted, paired-only and unowned tests must not repeat');
    assert.deepEqual(JSON.parse(ran.out).repeats, { n: 2, files: REPEATED_FILES, failed: false });
    assert.equal(ran.err, '');
  });
});

test('B136: a failed repeat stays flaky-test red with files and failing output even after a passing repeat', async () => {
  await withTempDir(async (dir) => {
    const fx = await repeatFixture(dir, { failAt: 2 });
    const ran = await fx.gate();
    const json = JSON.parse(ran.out), result = json.results[0];
    assert.equal(ran.code, 1, 'failed repeat must set the CLI exit code');
    assert.equal(json.allOk, false, 'named mutation: ignoring a failed repeat must fail here');
    assert.deepEqual([result.ok, result.reason, result.code], [false, 'flaky-test', 1]);
    assert.deepEqual(result.repeats, { n: 2, files: REPEATED_FILES, failed: true });
    assert.equal(result.stdout, 'failure stdout tail\n');
    assert.equal(result.stderr, 'failure stderr tail\n');
    assert.deepEqual(await fx.calls(), [[], REPEATED_FILES, REPEATED_FILES]);
    assert.deepEqual(json.results.slice(1).map((r) => [r.gate, r.skipped, r.ok]), [['lint', true, true], ['types', true, true], ['format', true, true]]);
  });
});

test('B136: output and signed gate rows carry exact repeats metadata on success and failure', async () => {
  for (const failAt of [0, 2]) await withTempDir(async (dir) => {
    const fx = await repeatFixture(dir, { failAt });
    const ran = await fx.gate(), json = JSON.parse(ran.out);
    const repeats = { n: 2, files: REPEATED_FILES, failed: failAt === 2 };
    assert.deepEqual(json.repeats, repeats);
    assert.deepEqual(json.results[0].repeats, repeats);
    const rows = await fx.rows(), key = await loadKey(fx.run);
    assert.deepEqual(rows.map((r) => r.event), failAt ? ['gate.red', 'gate.done'] : ['gate.done']);
    for (const row of rows) {
      assert.deepEqual(row.repeats, repeats);
      assert.deepEqual(verifyRow(row, key), { ok: true });
      assert.equal(verifyRow({ ...row, repeats: { ...repeats, n: 9 } }, key).ok, false, 'repeats must be covered by the signature');
    }
    if (failAt) assert.equal(rows[0].reason, 'flaky-test');
    assert.equal(rows.at(-1).all_ok, failAt === 0);
  });
});

test('B136: missing test_files prints one skip line; unchanged tests and initially red tests do not repeat', async () => {
  for (const opts of [{ token: false }, { mutate: false }, { failAt: 1 }]) await withTempDir(async (dir) => {
    const fx = await repeatFixture(dir, opts), ran = await fx.gate(), json = JSON.parse(ran.out);
    assert.equal(ran.err, opts.token === false ? 'repeat_new_tests: skipped (gates.test has no {test_files})\n' : '');
    assert.deepEqual(await fx.calls(), [[]]);
    assert.equal(Object.hasOwn(json, 'repeats'), false);
    assert.equal(Object.hasOwn(json.results[0], 'repeats'), false);
    assert.equal(ran.code, opts.failAt ? 1 : 0);
    if (opts.failAt) assert.equal(json.results[0].reason, 'threshold', 'initial failure keeps its original reason');
    for (const row of await fx.rows()) assert.equal(Object.hasOwn(row, 'repeats'), false);
  });
});

test('B136: absent repeat_new_tests keeps the previous output, unsigned rows and exit codes', async () => {
  await withTempDir(async (dir) => {
    const fx = await repeatFixture(dir);
    assert.deepEqual(JSON.parse((await fx.gate()).out).repeats, { n: 2, files: REPEATED_FILES, failed: false });
    await fx.configure(undefined);
    await rm(path.join(dir, 'calls.json'));
    const ran = await fx.gate(), json = JSON.parse(ran.out);
    assert.deepEqual([ran.code, ran.err, json.allOk], [0, '', true]);
    assert.deepEqual(Object.keys(json), ['stack', 'results', 'allOk']);
    assert.deepEqual(json.results[0], {
      gate: 'test', argv: [process.execPath, path.join(dir, 'fake.mjs')], skipped: false, ok: true,
      code: 0, stdout: 'passed 1\n', stderr: '', scope: 'full', reason: 'threshold',
    });
    assert.deepEqual(await fx.calls(), [[]]);
    const row = (await fx.rows()).at(-1);
    assert.equal(Object.hasOwn(row, 'repeats'), false);
    assert.equal(Object.hasOwn(row, 'mac'), false);
    assert.equal(row.all_ok, true);
  });
});

test('a red gate in the middle of 3 is reported red — each exit status read independently, never &&-chained', async () => {
  await withTempDir(async (dir) => {
    const gates = {
      test: ['node', '-e', 'process.exit(0)'],
      lint: ['node', '-e', 'process.exit(1)'],
      types: ['node', '-e', 'process.exit(0)'],
      format: null,
    };
    const { results, allOk } = await runGates(gates, { cwd: dir });
    assert.equal(allOk, false);
    const byGate = Object.fromEntries(results.map((r) => [r.gate, r]));
    // All three configured gates actually RAN (their own exit status read individually) — proves
    // the middle failure did not short-circuit the run.
    assert.equal(byGate.test.ok, true);
    assert.equal(byGate.test.skipped, false);
    assert.equal(byGate.lint.ok, false);
    assert.equal(byGate.lint.code, 1);
    assert.equal(byGate.types.ok, true);
    assert.equal(byGate.types.skipped, false);
    assert.equal(byGate.format.skipped, true);
    assert.equal(byGate.format.ok, true);
  });
});

test('all green ⇒ allOk true', async () => {
  await withTempDir(async (dir) => {
    const gates = { test: ['node', '-e', 'process.exit(0)'], lint: null, types: null, format: null };
    const { allOk, results } = await runGates(gates, { cwd: dir });
    assert.equal(allOk, true);
    assert.equal(results.length, 4);
  });
});

test('runGates refuses a missing cwd or a non-object gates argument', async () => {
  await assert.rejects(() => runGates(null, { cwd: '/tmp' }), TypeError);
  await assert.rejects(() => runGates({}, /** @type {any} */ ({})), TypeError);
});

// ── failOnStdout descriptor (e.g. detect.mjs's Go `gofmt -l` gate, which always exits 0) ──────

test('a failOnStdout gate that exits 0 but prints something to stdout is reported red', async () => {
  await withTempDir(async (dir) => {
    /** @type {{test: null, lint: null, types: null, format: {argv: string[], failOnStdout: true}}} */
    const gates = {
      test: null,
      lint: null,
      types: null,
      // Simulates `gofmt -l` finding an unformatted file: exits 0, but prints its name.
      format: { argv: ['node', '-e', 'process.stdout.write("unformatted.go\\n"); process.exit(0);'], failOnStdout: true },
    };
    const { results, allOk } = await runGates(gates, { cwd: dir });
    const format = results.find((r) => r.gate === 'format');
    assert.equal(allOk, false);
    assert.equal(format.ok, false);
    assert.equal(format.code, 0); // the process itself still exited 0 — ok is false anyway
    assert.match(format.stdout, /unformatted\.go/);
  });
});

test('a failOnStdout gate that exits 0 with EMPTY stdout is reported green (the boundary the flag exists for)', async () => {
  await withTempDir(async (dir) => {
    /** @type {{test: null, lint: null, types: null, format: {argv: string[], failOnStdout: true}}} */
    const gates = { test: null, lint: null, types: null, format: { argv: ['node', '-e', 'process.exit(0);'], failOnStdout: true } };
    const { results, allOk } = await runGates(gates, { cwd: dir });
    const format = results.find((r) => r.gate === 'format');
    assert.equal(allOk, true);
    assert.equal(format.ok, true);
    assert.equal(format.stdout, '');
  });
});

test('a PLAIN argv gate (no failOnStdout) with non-empty stdout still passes on exit 0 — the flag only applies when set', async () => {
  await withTempDir(async (dir) => {
    const gates = { test: ['node', '-e', 'console.log("noisy but fine"); process.exit(0);'], lint: null, types: null, format: null };
    const { results, allOk } = await runGates(gates, { cwd: dir });
    assert.equal(allOk, true);
    assert.equal(results[0].ok, true);
  });
});
