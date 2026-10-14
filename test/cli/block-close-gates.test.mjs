import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { runGatesVerb } = await import('../../src/cli/gates.mjs');
const { startRun, readRun, writeSigned } = await import('../../src/state/run.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { enqueue } = await import('../../src/worker/queue.mjs');

const PROBE = async (/** @type {number} */ pid) => pid === 4242 ? 'start-A' : null;

/** @param {string[]} args @returns {Promise<[number, string, string]>} */
async function cli(args) {
  let stdout = '';
  let stderr = '';
  const code = await runBlock(args, {
    stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } }, probe: PROBE,
  });
  return [code, stdout, stderr];
}

/** @param {string[]} args @returns {Promise<[number, string, string]>} */
async function gatesCli(args) {
  let stdout = '';
  let stderr = '';
  const code = await runGatesVerb(args, {
    stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } },
  });
  return [code, stdout, stderr];
}

/** @param {string} slug @param {string[] | null} testGate */
async function fixture(slug, testGate) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  const config = { version: 1, project: { slug }, ...cfgFor(), gates: { test: testGate, lint: null, types: null, format: null } };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('close-files');
  writeFile(files, 'acc.json', JSON.stringify([{ clause: 'gate warning', tests: ['b118'] }]));
  writeFile(files, 'coder.log', '$ node --test\n');
  const runId = `r-${slug}`;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, 'acc.json')]);
  assert.deepEqual([opened[0], opened[2]], [0, '']);
  writeFile(repo, 'a.mjs', 'export const a = 2;\n');
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(repo, 'a.mjs'), round: 1 });
  const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: 'a.mjs' }).ticket;
  return {
    config,
    repo,
    runId,
    stdout: `reviewed a.mjs ticket ${ticket}\nblock B1 closed\n`,
    warning: `WARN block B1: no green gates run for this block; run gates run --run ${runId} --block B1 before close\n`,
    sign: (/** @type {Record<string, any>} */ row) => writeSigned(runId, writeRow, { block: 'B1', ...row }),
    close: () => cli(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(files, 'coder.log')]),
    gates: () => gatesCli(['run', '--cwd', repo, '--run', runId, '--block', 'B1']),
    rows: () => readAllRows(slug),
    gateRows: async () => (await readAllRows(slug)).filter((r) => r.event === 'gate.done' || r.event === 'gate.red')
      .map(({ ts: _ts, tokens_source: _tokens, cost_source: _cost, ...row }) => row),
  };
}

test('B118: configured test gate warns exactly once and closes for missing or failed last gates', async () => {
  const cases = [[], [{ event: 'gate.done', all_ok: false }], [{ event: 'gate.red', gate: 'test' }]];
  for (const [i, rows] of cases.entries()) {
    const f = await fixture(`b118-close-${i}`, ['node', '--test']);
    for (const row of rows) await f.sign(row);
    // A green gate for another block or run cannot suppress this block's warning.
    await f.sign({ event: 'gate.done', block: 'B2', all_ok: true });
    await f.sign({ event: 'gate.done', run: 'r-other', all_ok: true });
    assert.deepEqual(await f.close(), [0, f.stdout, f.warning]);
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close' && r.block === 'B1').length, 1);
  }
});

test('B118 boundary: green gates and null test gates stay silent; a later red or failed done warns', async () => {
  const cases = [
    { testGate: ['node', '--test'], rows: [{ event: 'gate.red', gate: 'test' }, { event: 'gate.done', all_ok: true }], warn: false },
    { testGate: null, rows: [], warn: false },
    { testGate: null, rows: [{ event: 'gate.done', all_ok: false }], warn: false },
    { testGate: ['node', '--test'], rows: [{ event: 'gate.done', all_ok: true }, { event: 'gate.red', gate: 'test' }], warn: true },
    { testGate: ['node', '--test'], rows: [{ event: 'gate.done', all_ok: true }, { event: 'gate.done', all_ok: false }], warn: true },
  ];
  for (const [i, c] of cases.entries()) {
    const f = await fixture(`b118-close-boundary-${i}`, c.testGate);
    for (const row of c.rows) await f.sign(row);
    assert.deepEqual(await f.close(), [0, f.stdout, c.warn ? f.warning : '']);
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
  }
});

test('B120: real plain gates write exactly one done and one red per failure to the run project ledger', async () => {
  for (const exit of [0, 1]) {
    const argv = ['node', '-e', `process.exit(${exit})`];
    const f = await fixture(`b120-ledger-${exit}`, argv);
    const [code, stdout, stderr] = await f.gates();
    assert.deepEqual([code, stderr], [exit, '']);
    const result = JSON.parse(stdout);
    assert.equal(result.allOk, exit === 0);
    assert.deepEqual(result.results.filter((r) => !r.skipped).map((r) => [r.gate, r.argv, r.code, r.ok]),
      [['test', argv, exit, exit === 0]]);
    assert.deepEqual(await f.gateRows(), [
      ...(exit === 1 ? [{ event: 'gate.red', run: f.runId, block: 'B1', gate: 'test', code: 1 }] : []),
      { event: 'gate.done', run: f.runId, block: 'B1', stack: 'unknown', all_ok: exit === 0 },
    ], `plain exit ${exit}: exact gate rows in the run's project ledger`);
  }
});

test('B120: close is silent after real green plain gates and warns exactly once after real red gates', async () => {
  for (const exit of [0, 1]) {
    const f = await fixture(`b120-close-${exit}`, ['node', '-e', `process.exit(${exit})`]);
    const [code, stdout, stderr] = await f.gates();
    assert.deepEqual([code, stderr, JSON.parse(stdout).allOk], [exit, '', exit === 0]);
    assert.deepEqual(await f.close(), [0, f.stdout, exit === 0 ? '' : f.warning],
      `plain exit ${exit}: close must print exactly ${exit} gate warnings`);
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close' && r.block === 'B1').length, 1);
  }
});

test('B120 boundary: a real red plain run after green records both runs and restores one close warning', async () => {
  const f = await fixture('b120-later-red', ['node', '-e', 'process.exit(0)']);
  const green = await f.gates();
  assert.deepEqual([green[0], green[2], JSON.parse(green[1]).allOk], [0, '', true]);
  writeFile(f.repo, '.code-forge.yml', JSON.stringify({ ...f.config, gates: { ...f.config.gates, test: ['node', '-e', 'process.exit(1)'] } }));
  const red = await f.gates();
  assert.deepEqual([red[0], red[2], JSON.parse(red[1]).allOk], [1, '', false]);
  writeFile(f.repo, '.code-forge.yml', JSON.stringify(f.config));
  assert.deepEqual(await f.gateRows(), [
    { event: 'gate.done', run: f.runId, block: 'B1', stack: 'unknown', all_ok: true },
    { event: 'gate.red', run: f.runId, block: 'B1', gate: 'test', code: 1 },
    { event: 'gate.done', run: f.runId, block: 'B1', stack: 'unknown', all_ok: false },
  ], 'the later failure must append one red and one failed done after the green done');
  assert.deepEqual(await f.close(), [0, f.stdout, f.warning]);
});
