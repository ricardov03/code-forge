import './support.mjs';
import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { captureStream, git, withTempDir } from './support.mjs';
import { PROVIDER_DEFAULTS } from '../../src/config/defaults/index.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { readRun, saveRun, startRun, writeSigned } from '../../src/state/run.mjs';
import { contentHash } from '../../src/worker/ticket.mjs';
import { enqueue } from '../../src/worker/queue.mjs';
const { runGatesVerb } = await import('../../src/cli/gates.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');

const command = [process.execPath, '-e', 'void 0'];
const gates = {
  test: command, lint: command, types: command, format: command,
  custom: [{ name: 'build', argv: [process.execPath, '-e', 'process.exit(1)'] }],
};

let sequence = 0;
const probe = async () => 'start';

async function configure(cwd, kinds) {
  const slug = `gate-kinds-${++sequence}`, runId = `r-${slug}`;
  const config = { version: 1, project: { slug }, provider: 'anthropic', levels: PROVIDER_DEFAULTS.anthropic.levels,
    gates: { ...gates, kinds },
  };
  git(['init', '-q', '-b', 'main'], cwd);
  await writeFile(path.join(cwd, 'design.md'), '# Design\n');
  await writeFile(path.join(cwd, 'coder.log'), '$ node --test\n');
  await writeFile(path.join(cwd, '.code-forge.yml'), JSON.stringify(config));
  git(['add', '-A'], cwd);
  git(['commit', '-q', '-m', 'base'], cwd);
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: cwd, project: slug, runId, workerPid: 4242, probe, config, writeRow });
  await openBlock({ runId, id: 'DOC', level: 'L2', owned: ['design.md'],
    acceptance: [{ clause: 'Inspect design', kind: 'human_check' }], writeRow,
  });
  return { runId, writeRow, rows: () => readAllRows(slug),
    update: async (block) => {
      const record = await readRun(runId);
      record.blocks.DOC = { ...record.blocks.DOC, ...block };
      await saveRun(record);
    },
  };
}

async function run(cwd, runId, block = 'DOC') {
  const stdout = captureStream(), stderr = captureStream();
  const code = await runGatesVerb(['run', '--cwd', cwd, ...(block ? ['--run', runId, '--block', block] : [])], { stdout, stderr });
  assert.equal(stderr.text, '', 'gates run has no diagnostics');
  return { code, output: JSON.parse(stdout.text) };
}

test('kind sets select only named gates, report every exclusion, and leave missing entries, plain runs and light rules intact', async () => {
  await withTempDir(async (cwd) => {
    let fixture = await configure(cwd, { docs: ['types'], contract: ['types'] });
    for (const block of [
      { kind: 'docs', owned_files: ['site.mjs'] },
      { owned_files: ['design.md'] },
      { owned_files: ['schema/api.json', 'design.md'] },
    ]) {
      await fixture.update({ kind: undefined, ...block });
      const kind = block.owned_files[0].startsWith('schema/') ? 'contract' : 'docs';
      const ran = await run(cwd, fixture.runId);
      assert.deepEqual([ran.code, ran.output.allOk], [0, true], `${kind} only runs passing types`);
      assert.deepEqual(ran.output.results.map(({ gate, skipped, code, reason }) => [gate, skipped, code, reason]), [
        ['test', true, null, `not in gates.kinds.${kind}`],
        ['lint', true, null, `not in gates.kinds.${kind}`],
        ['types', false, 0, undefined],
        ['format', true, null, `not in gates.kinds.${kind}`],
        ['build', true, null, `not in gates.kinds.${kind}`],
      ], 'all five gates are reported and exactly one executes');
    }
    await fixture.update({ kind: 'code', owned_files: ['design.md'] });
    for (const block of ['DOC', null]) {
      const ran = await run(cwd, fixture.runId, block);
      assert.deepEqual([ran.code, ran.output.allOk], [1, false], 'missing kind entry or no block runs failing build');
      assert.deepEqual(ran.output.results.filter(({ skipped }) => !skipped).map(({ gate, ok }) => [gate, ok]),
        [['test', true], ['lint', true], ['types', true], ['format', true], ['build', false]]);
    }
    fixture = await configure(cwd, { docs: ['build'] });
    const custom = await run(cwd, fixture.runId);
    assert.deepEqual(custom.output.results.filter(({ skipped }) => !skipped).map(({ gate, ok }) => [gate, ok]), [['build', false]]);
    assert.equal(custom.code, 1, 'a selected custom failure still fails');
    await writeFile(path.join(cwd, '.code-forge.yml'), JSON.stringify({ version: 1, gates: { ...gates, test: [...command, '{test_files}'], kinds: { code: [], docs: [] } } }));
    await fixture.update({ kind: 'light', owned_files: ['design.md'], test_files: ['a.test.mjs'] });
    const light = await run(cwd, fixture.runId);
    assert.deepEqual(light.output.results.filter(({ skipped }) => !skipped).map(({ gate }) => gate), ['test', 'lint', 'types', 'format', 'build'], 'light rules run configured gates regardless of kind sets');
    assert.equal(light.code, 1);
  });
});

test('an empty docs set writes one green done row and real block close reaches the human check; missing green refuses', async () => {
  await withTempDir(async (cwd) => {
    const fixture = await configure(cwd, { docs: [] });
    await writeFile(path.join(cwd, 'design.md'), '# Updated design\n');
    await writeSigned(fixture.runId, fixture.writeRow, { event: 'review.approved', block: 'DOC', file: 'design.md', content_hash: contentHash(cwd, 'design.md') });
    enqueue({ repoRoot: cwd, run: fixture.runId, block: 'DOC', file: 'design.md' });
    const close = async () => {
      const stdout = captureStream(), stderr = captureStream();
      const code = await runBlock(['close', 'DOC', '--run', fixture.runId, '--worker-pid', '4242', '--transcript', path.join(cwd, 'coder.log')], {
        stdout, stderr, probe,
      });
      return { code, out: stdout.text, err: stderr.text };
    };
    const missing = await close();
    assert.equal(missing.code, 1);
    assert.match(missing.err, /no green gates run/);
    assert.equal((await readRun(fixture.runId)).blocks.DOC.status, 'open');
    const ran = await run(cwd, fixture.runId);
    assert.deepEqual([ran.code, ran.output.allOk], [0, true]);
    assert.equal(ran.output.message, '0 gates for kind docs');
    assert.equal(ran.output.results.length, 5);
    assert.deepEqual(ran.output.results.filter(({ skipped }) => !skipped), [], 'zero commands execute');
    assert.deepEqual((await fixture.rows()).filter(({ event }) => event === 'gate.done').map(({ block, all_ok }) => [block, all_ok]), [['DOC', true]], 'exactly one green done row');
    assert.equal((await fixture.rows()).filter(({ event }) => event === 'gate.red').length, 0);
    const closed = await close();
    assert.deepEqual([closed.code, closed.err], [0, '']);
    assert.equal((await readRun(fixture.runId)).blocks.DOC.status, 'awaiting_human_check');
    assert.equal((await fixture.rows()).filter(({ event }) => event === 'block.close').length, 1);
  });
});
