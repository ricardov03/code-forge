import './support.mjs';
import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import test, { mock } from 'node:test';
import { captureStream, git, withTempDir } from './support.mjs';
import { configHash } from '../../src/state/config-snapshot.mjs';

// Keep run and ledger storage in memory. Gate execution and block close remain real;
// the unrelated review check is green so this test measures the gates close requirement.
const rows = [];
let record;
const key = Buffer.alloc(32, 7);
const signer = await import('../../src/state/signer.mjs');
mock.module('../../src/state/signer.mjs', { namedExports: { ...signer, loadKey: async () => key } });
mock.module('../../src/ledger/write.mjs', { namedExports: {
  ...await import('../../src/ledger/write.mjs'),
  appendRow: async (row) => { rows.push(row); }, readAllRows: async () => [...rows],
} });
mock.module('../../src/state/run.mjs', { namedExports: {
  ...await import('../../src/state/run.mjs'),
  readRun: async () => structuredClone(record), saveRun: async (value) => { record = structuredClone(value); },
  withRunLock: async (_run, fn) => fn(),
  writeSigned: async (run, writeRow, row) => writeRow(signer.signRow({ run, ...row }, key)),
} });
mock.module('../../src/review/gate-check.mjs', { namedExports: {
  ...await import('../../src/review/gate-check.mjs'), reviewGateCheck: () => async () => ({ ok: true }),
} });
const { runGatesVerb } = await import('../../src/cli/gates.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');

const command = [process.execPath, '-e', 'void 0'];
const gates = {
  test: command, lint: command, types: command, format: command,
  custom: [{ name: 'build', argv: [process.execPath, '-e', 'process.exit(1)'] }],
};

async function configure(cwd, kinds) {
  rows.length = 0;
  const config = { gates: { ...gates, kinds } };
  await writeFile(path.join(cwd, '.code-forge.yml'), JSON.stringify({ version: 1, ...config }));
  const hash = configHash(config);
  record = { run_id: 'r-kinds', status: 'active', project: 'kinds', workspace: cwd,
    config: { hash, snapshots: { [hash]: config } },
    worker: { pid: 4242, started_at: 'start' }, orphans: [],
    blocks: { DOC: { status: 'open', kind: 'docs', owned_files: ['design.md'], level: 'L2', base_sha: 'HEAD', human_checks: ['Inspect design'] } },
  };
  rows.push(signer.signRow({ run: 'r-kinds', block: 'DOC', event: 'dispatch' }, key));
}

async function run(cwd, block = 'DOC') {
  const stdout = captureStream(), stderr = captureStream();
  const code = await runGatesVerb(['run', '--cwd', cwd, ...(block ? ['--run', 'r-kinds', '--block', block] : [])], { stdout, stderr });
  assert.equal(stderr.text, '', 'gates run has no diagnostics');
  return { code, output: JSON.parse(stdout.text) };
}

test('kind sets select only named gates, report every exclusion, and leave missing entries, plain runs and light rules intact', async () => {
  await withTempDir(async (cwd) => {
    await configure(cwd, { docs: ['types'], contract: ['types'] });
    for (const block of [
      { kind: 'docs', owned_files: ['site.mjs'] },
      { owned_files: ['design.md'] },
      { owned_files: ['schema/api.json', 'design.md'] },
    ]) {
      record.blocks.DOC = { status: 'open', ...block };
      const kind = block.owned_files[0].startsWith('schema/') ? 'contract' : 'docs';
      const ran = await run(cwd);
      assert.deepEqual([ran.code, ran.output.allOk], [0, true], `${kind} only runs passing types`);
      assert.deepEqual(ran.output.results.map(({ gate, skipped, code, reason }) => [gate, skipped, code, reason]), [
        ['test', true, null, `not in gates.kinds.${kind}`],
        ['lint', true, null, `not in gates.kinds.${kind}`],
        ['types', false, 0, undefined],
        ['format', true, null, `not in gates.kinds.${kind}`],
        ['build', true, null, `not in gates.kinds.${kind}`],
      ], 'all five gates are reported and exactly one executes');
    }
    record.blocks.DOC = { status: 'open', kind: 'code', owned_files: ['design.md'] };
    for (const block of ['DOC', null]) {
      const ran = await run(cwd, block);
      assert.deepEqual([ran.code, ran.output.allOk], [1, false], 'missing kind entry or no block runs failing build');
      assert.deepEqual(ran.output.results.filter(({ skipped }) => !skipped).map(({ gate, ok }) => [gate, ok]),
        [['test', true], ['lint', true], ['types', true], ['format', true], ['build', false]]);
    }
    await configure(cwd, { docs: ['build'] });
    const custom = await run(cwd);
    assert.deepEqual(custom.output.results.filter(({ skipped }) => !skipped).map(({ gate, ok }) => [gate, ok]), [['build', false]]);
    assert.equal(custom.code, 1, 'a selected custom failure still fails');
    await writeFile(path.join(cwd, '.code-forge.yml'), JSON.stringify({ version: 1, gates: { ...gates, test: [...command, '{test_files}'], kinds: { code: [], docs: [] } } }));
    record.blocks.DOC = { status: 'open', kind: 'light', owned_files: ['design.md'], test_files: ['a.test.mjs'] };
    const light = await run(cwd);
    assert.deepEqual(light.output.results.filter(({ skipped }) => !skipped).map(({ gate }) => gate), ['test', 'lint', 'types', 'format', 'build'], 'light rules run configured gates regardless of kind sets');
    assert.equal(light.code, 1);
  });
});

test('an empty docs set writes one green done row and real block close reaches the human check; missing green refuses', async () => {
  await withTempDir(async (cwd) => {
    git(['init', '-q'], cwd);
    await writeFile(path.join(cwd, 'design.md'), '# Design\n');
    git(['add', 'design.md'], cwd);
    git(['commit', '-q', '-m', 'base'], cwd);
    await configure(cwd, { docs: [] });
    record.blocks.DOC.base_sha = git(['rev-parse', 'HEAD'], cwd);
    const close = async () => {
      const stdout = captureStream(), stderr = captureStream();
      const code = await runBlock(['close', 'DOC', '--run', 'r-kinds', '--worker-pid', '4242'], {
        stdout, stderr, probe: async () => 'start',
      });
      return { code, out: stdout.text, err: stderr.text };
    };
    const missing = await close();
    assert.equal(missing.code, 1);
    assert.match(missing.err, /no green gates run/);
    assert.equal(record.blocks.DOC.status, 'open');
    const ran = await run(cwd);
    assert.deepEqual([ran.code, ran.output.allOk], [0, true]);
    assert.equal(ran.output.message, '0 gates for kind docs');
    assert.equal(ran.output.results.length, 5);
    assert.deepEqual(ran.output.results.filter(({ skipped }) => !skipped), [], 'zero commands execute');
    assert.deepEqual(rows.filter(({ event }) => event === 'gate.done').map(({ block, all_ok }) => [block, all_ok]), [['DOC', true]], 'exactly one green done row');
    assert.equal(rows.filter(({ event }) => event === 'gate.red').length, 0);
    const closed = await close();
    assert.deepEqual([closed.code, closed.err], [0, '']);
    assert.equal(record.blocks.DOC.status, 'awaiting_human_check');
    assert.equal(rows.filter(({ event }) => event === 'block.close').length, 1);
  });
});
