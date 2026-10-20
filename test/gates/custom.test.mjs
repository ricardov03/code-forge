import './support.mjs';
import assert from 'node:assert/strict';
import test, { mock } from 'node:test';
import { createHash } from 'node:crypto';
import { mkdir, readFile, symlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { validateConfig } from '../../src/config/validate.mjs';
import { runGates } from '../../src/gates/run.mjs';
import { redactJSON } from '../../src/util/redact.mjs';
import { captureStream, withTempDir } from './support.mjs';

const command = [process.execPath, '-e', 'void 0'];
const entry = { name: 'build', argv: command };
const config = (custom) => ({ version: 1, project: { slug: 'custom-test' }, provider: 'anthropic', levels: Object.fromEntries(['L0', 'L1', 'L2', 'L3'].map((level) => [level, { model: 'claude-sonnet-4-5' }])), gates: { custom } });
const gateErrors = (custom) => validateConfig(config(custom)).errors.filter((error) => error.rule === 'gates-not-argv-array');

test('custom config refuses invalid entries with an entry-specific error', () => {
  const cases = [
    [null, 'gates.custom'],
    [{}, 'gates.custom'],
    [[null], 'custom[0]'],
    [[{ ...entry, name: 'Build' }], 'Build'],
    [[{ ...entry, name: 'a'.repeat(33) }], 'a'.repeat(33)],
    ...['', '1build', 'build.dot', 'test', 'lint', 'types', 'format'].map((name) => [[{ ...entry, name }], 'custom[0]']),
    [[entry, entry], 'custom[1]'],
    [[{ ...entry, argv: [] }], 'build'],
    [[{ ...entry, argv: ['', 'arg'] }], 'build'],
    [[{ ...entry, argv: ['node', 3] }], 'build'],
    [[{ ...entry, argv: 'node' }], 'build'],
    ...[null, [], { '1BAD': 'x' }, { GOOD: 1 }].map((env) => [[{ ...entry, env }], 'build']),
    ...['{test_files}', '{changed_files}'].flatMap((token) => [token, `prefix=${token}`].map((arg) => [[{ ...entry, argv: ['node', arg] }], token])),
    ...['', '/outside', '../outside', 'reports/../outside', 'C:\\outside', '\\outside'].map((pattern) => [[{ ...entry, evidence: [pattern] }], 'build']),
    ...['report.json', [3]].map((evidence) => [[{ ...entry, evidence }], 'build']),
    [Array.from({ length: 21 }, (_, i) => ({ ...entry, name: `gate${i}` })), 'gates.custom'],
  ];
  for (const [custom, label] of cases) {
    const errors = gateErrors(custom);
    assert.equal(errors.length, 1, `refuse ${JSON.stringify(custom)} with exactly one gates-not-argv-array error`);
    assert.ok(errors.some((error) => error.message.includes(String(label))), `error must name ${label}: ${JSON.stringify(errors)}`);
  }
});

test('custom config accepts twenty named object gates and safe evidence globs', () => {
  const custom = Array.from({ length: 20 }, (_, i) => ({
    name: i === 0 ? `a${'b'.repeat(31)}` : `gate_${i}-web`, argv: command,
    env: { MODE: 'web', _COUNT2: '' }, evidence: ['report.json', 'reports/*.json', 'shots/**/*.png'],
  }));
  assert.deepEqual(gateErrors(custom), [], 'all twenty valid entries pass the domain rule');
  const schemaErrors = validateConfig(config(custom)).errors.filter((error) => error.rule === 'schema');
  assert.deepEqual(schemaErrors, [], 'custom gates are accepted by the schema');
  assert.deepEqual(gateErrors([]), [], 'an empty custom list is valid');
});

test('custom gates run after all fixed gates in list order and independently include failures in allOk', async () => {
  await withTempDir(async (cwd) => {
    const makeCommand = (name, code = 0) => [process.execPath, '-e', `require('node:fs').appendFileSync('order', '${name}\\n'); console.log(process.env.MODE || '${name}'); console.error('${name} stderr'); process.exit(${code})`];
    const gates = {
      test: makeCommand('test'), lint: makeCommand('lint', 1), types: makeCommand('types'), format: makeCommand('format'),
      custom: [{ name: 'build', argv: makeCommand('build', 2), env: { MODE: 'web' } }, { name: 'links', argv: makeCommand('links') }],
    };
    const ran = await runGates(gates, { cwd });
    assert.deepEqual(ran.results.map((result) => [result.gate, result.ok]), [['test', true], ['lint', false], ['types', true], ['format', true], ['build', false], ['links', true]]);
    assert.equal(await readFile(path.join(cwd, 'order'), 'utf8'), 'test\nlint\ntypes\nformat\nbuild\nlinks\n');
    assert.equal(ran.allOk, false);
    assert.deepEqual(ran.results.slice(4).map((result) => ({ custom: result.custom, code: result.code, stdout: result.stdout, stderr: result.stderr })), [
      { custom: true, code: 2, stdout: 'web\n', stderr: 'build stderr\n' },
      { custom: true, code: 0, stdout: 'links\n', stderr: 'links stderr\n' },
    ]);
    assert.deepEqual(ran.results[4].env_keys, ['MODE']);
    const onlyRedCustom = await runGates({ custom: [{ name: 'build', argv: makeCommand('build', 1) }] }, { cwd });
    assert.equal(onlyRedCustom.allOk, false, 'a custom failure alone makes allOk false');
    const green = await runGates({ custom: [{ name: 'links', argv: command }] }, { cwd });
    assert.equal(green.allOk, true, 'all custom gates green makes allOk true');
    assert.equal(green.results.length, 5, 'four skipped fixed gates and one green custom gate');
  });
});

test('custom gates keep timeout, env masking and execution under changed and light scopes', async () => {
  await withTempDir(async (cwd) => {
    const gates = { custom: [
      { name: 'slow', argv: [process.execPath, '-e', 'setTimeout(() => {}, 30000)'] },
      { name: 'links', argv: [process.execPath, '-e', 'console.log(process.env.WEB_VALUE)'], env: { WEB_VALUE: 'private-custom-value' } },
    ] };
    for (const light of [false, true]) {
      const ran = await runGates({ ...gates, ...(light ? { lightTestFiles: ['a.test.mjs'] } : {}) }, { cwd, timeoutMs: 300, scope: { scope: 'changed', files: [], changedFiles: [] } });
      assert.deepEqual(ran.results.slice(4).map((result) => [result.gate, result.custom, result.ok, result.scope]), [['slow', true, false, undefined], ['links', true, true, undefined]]);
      assert.equal(redactJSON(ran).includes('private-custom-value'), false, 'custom env value is masked in printed child output');
      assert.deepEqual(ran.results[5].env_keys, ['WEB_VALUE']);
      assert.equal(ran.allOk, false);
    }
  });
});

test('custom evidence hashes sorted regular files after pass or fail, deduplicates patterns and lists missing without failing', async () => {
  await withTempDir(async (cwd) => {
    await mkdir(path.join(cwd, 'reports/nested'), { recursive: true });
    await writeFile(path.join(cwd, 'reports/z.json'), 'last');
    await writeFile(path.join(cwd, 'reports/nested/a.json'), 'nested');
    const argv = [process.execPath, '-e', "require('node:fs').writeFileSync('reports/a.json', 'é'); process.exit(1)"];
    const patterns = ['reports/**/*.json', 'reports/*.json', 'missing/*.png'];
    const ran = await runGates({ custom: [{ name: 'lighthouse', argv, evidence: patterns }, { name: 'links', argv: command, evidence: ['reports/*.json', 'absent.json'] }] }, { cwd });
    assert.equal(ran.results.length, 6, 'four fixed gates followed by two custom results');
    const expected = [['reports/a.json', 'é'], ['reports/nested/a.json', 'nested'], ['reports/z.json', 'last']].map(([file, contents]) => ({ path: file, bytes: Buffer.byteLength(contents), sha256: createHash('sha256').update(contents).digest('hex') }));
    assert.deepEqual(ran.results[4].evidence, expected, 'red gate hashes three unique files including output created by the command');
    assert.deepEqual(ran.results[4].missing, ['missing/*.png']);
    assert.equal(ran.results[4].ok, false);
    assert.deepEqual(ran.results[5].evidence, [expected[0], expected[2]], '* matches only files within one path segment');
    assert.deepEqual(ran.results[5].missing, ['absent.json']);
    assert.equal(ran.results[5].ok, true, 'missing evidence does not fail a passing command');
  });
});

test('custom evidence refuses outside paths before running, skips symlinks and caps sorted files at 200', async () => {
  await withTempDir(async (outside) => {
    await withTempDir(async (cwd) => {
      await writeFile(path.join(outside, 'secret.json'), 'outside contents');
      await symlink(outside, path.join(cwd, 'escaped'));
      await symlink(path.join(outside, 'secret.json'), path.join(cwd, 'linked.json'));
      await mkdir(path.join(cwd, 'reports'));
      await Promise.all(Array.from({ length: 201 }, (_, i) => writeFile(path.join(cwd, 'reports', `${String(i).padStart(3, '0')}.json`), String(i))));
      const ran = await runGates({ custom: [{ name: 'shots', argv: command, evidence: ['**/*.json', 'linked.json', 'escaped/*.json', 'reports/200.json'] }] }, { cwd });
      assert.equal(ran.results.length, 5);
      assert.equal(ran.results[4].evidence?.length, 200);
      assert.deepEqual(ran.results[4].evidence?.map((item) => item.path), Array.from({ length: 200 }, (_, i) => `reports/${String(i).padStart(3, '0')}.json`));
      assert.deepEqual(ran.results[4].missing, ['linked.json', 'escaped/*.json'], 'a matching file beyond the cap is not missing; symlinks are missing');
      for (const pattern of ['../secret.json', path.join(outside, 'secret.json')]) {
        await assert.rejects(runGates({ test: [process.execPath, '-e', "throw new Error('must not run')"], custom: [{ name: 'shots', argv: command, evidence: [pattern] }] }, { cwd }), /gates\.custom\[0\].*shots.*relative/);
      }
    });
  });
});

// Capture row arguments rather than reading state or ledger files. The guarded runner double
// retains the actual gate execution and supplies only the unrelated proof-lock context.
const rows = [];
let record;
mock.module('../../src/ledger/write.mjs', { namedExports: { ...await import('../../src/ledger/write.mjs'), appendRow: async (row) => { rows.push(row); } } });
mock.module('../../src/state/run.mjs', { namedExports: { ...await import('../../src/state/run.mjs'), readRun: async () => record } });
mock.module('../../src/proof/lock.mjs', { namedExports: { ...await import('../../src/proof/lock.mjs'), runGatesGuarded: async ({ gates, cwd, timeoutMs }) => ({ result: 'ok', value: await runGates(gates, { cwd, timeoutMs }) }) } });
const { runGatesVerb } = await import('../../src/cli/gates.mjs');

async function cli(args) {
  const stdout = captureStream(), stderr = captureStream();
  const code = await runGatesVerb(args, { stdout, stderr });
  assert.equal(stderr.text, '', 'CLI completes without diagnostics');
  return { code, output: JSON.parse(stdout.text), text: stdout.text };
}

async function configure(cwd, gates) {
  await writeFile(path.join(cwd, '.code-forge.yml'), JSON.stringify({ ...config(undefined), gates }));
}

test('custom CLI detect and run list named gates; red and done rows include summaries and block evidence', async () => {
  await withTempDir(async (cwd) => {
    const custom = [
      { name: 'build', argv: [process.execPath, '-e', "require('node:fs').writeFileSync('proof.txt', 'web'); process.exit(2)"], evidence: ['proof.txt', 'absent.png'], env: { WEB_TOKEN: 'custom-private-env' } },
      { name: 'links', argv: command },
    ];
    await configure(cwd, { custom });
    const detected = await cli(['detect', '--cwd', cwd]);
    assert.equal(detected.code, 0);
    assert.deepEqual(detected.output.custom, [{ name: 'build', argv: custom[0].argv, evidence: custom[0].evidence, env_keys: ['WEB_TOKEN'] }, custom[1]], 'detect lists both commands and only env names');
    assert.equal(detected.text.includes('custom-private-env'), false);
    const evidence = [{ path: 'proof.txt', bytes: 3, sha256: createHash('sha256').update('web').digest('hex') }];
    for (const block of [false, true]) {
      rows.length = 0;
      record = { workspace: cwd, blocks: { WEB: { owned_files: [] } } };
      const ran = await cli(['run', '--cwd', cwd, '--slug', 'fake-custom-cli', ...(block ? ['--run', 'fake-run', '--block', 'WEB', '--changed'] : [])]);
      assert.equal(ran.code, 1);
      assert.equal(ran.output.allOk, false);
      assert.deepEqual(ran.output.results.slice(4).map((result) => [result.gate, result.custom, result.ok]), [['build', true, false], ['links', true, true]]);
      assert.deepEqual(ran.output.results[4].evidence, evidence);
      const owner = block ? { run: 'fake-run', block: 'WEB' } : {};
      assert.deepEqual(rows, [
        { ...owner, event: 'gate.red', gate: 'build', custom: true, code: 2 },
        { ...owner, event: 'gate.done', stack: 'unknown', all_ok: false,
          custom: [{ name: 'build', ok: false, evidence_count: 1, missing: ['absent.png'] }, { name: 'links', ok: true, evidence_count: 0 }],
          ...(block ? { evidence: { build: evidence, links: [] } } : {}),
        },
      ], 'exactly one red row and one done row; evidence map only with run/block');
    }
  });
});

test('custom CLI boundary keeps legacy row shapes and still runs custom gates in a light block', async () => {
  await withTempDir(async (cwd) => {
    rows.length = 0;
    record = { workspace: cwd, blocks: { WEB: { kind: 'light', test_files: ['a.test.mjs'], owned_files: [] } } };
    await configure(cwd, { test: [...command, '{test_files}'], custom: [{ name: 'links', argv: command, evidence: ['absent.json'] }] });
    const light = await cli(['run', '--cwd', cwd, '--run', 'fake-run', '--block', 'WEB', '--changed']);
    assert.equal(light.output.results.length, 5, 'a light block still runs its custom gate');
    assert.deepEqual(light.output.results[4].missing, ['absent.json']);
    assert.equal(light.output.allOk, true);
    for (const custom of [undefined, []]) {
      rows.length = 0;
      await configure(cwd, { test: [process.execPath, '-e', 'process.exit(1)'], ...(custom ? { custom } : {}) });
      const legacy = await cli(['run', '--cwd', cwd, '--slug', 'fake-legacy-cli']);
      assert.equal(legacy.code, 1);
      assert.equal(legacy.output.results.length, 4);
      assert.equal(Object.hasOwn(legacy.output, 'custom'), false);
      assert.deepEqual(rows, [{ event: 'gate.red', gate: 'test', code: 1 }, { event: 'gate.done', stack: 'unknown', all_ok: false }], 'no new row keys without custom gates');
    }
  });
});
