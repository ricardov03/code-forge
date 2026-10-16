import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import { mock, test } from 'node:test';

const { exec: realExec } = await import('../../src/util/exec.mjs');
let failGitLog = false;
mock.module('../../src/util/exec.mjs', { namedExports: {
  exec: (argv, options) => failGitLog && argv[0] === 'git' && argv[1] === 'log'
    ? Promise.resolve({ result: 'failed', code: 128, signal: null, stdout: '', stderr: 'git log failed', timedOut: false })
    : realExec(argv, options),
} });

const { runBlock } = await import('../../src/cli/block.mjs');
const { runGatesVerb } = await import('../../src/cli/gates.mjs');
const { startRun, readRun, writeSigned } = await import('../../src/state/run.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { enqueue } = await import('../../src/worker/queue.mjs');

const PROBE = async (/** @type {number} */ pid) => pid === 4242 ? 'start-A' : null;

/** @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string, string]>} */
async function cli(args, deps = {}) {
  let stdout = '';
  let stderr = '';
  const code = await runBlock(args, {
    stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } }, probe: PROBE, ...deps,
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
    refusal: `block B1 open: no green gates run; run: code-forge gates run --run ${runId} --block B1\n`,
    sign: (/** @type {Record<string, any>} */ row) => writeSigned(runId, writeRow, { block: 'B1', ...row }),
    close: (extra = [], deps = {}) => cli(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(files, 'coder.log'), ...extra], deps),
    gates: () => gatesCli(['run', '--cwd', repo, '--run', runId, '--block', 'B1']),
    rows: () => readAllRows(slug),
    gateRows: async () => (await readAllRows(slug)).filter((r) => r.event === 'gate.done' || r.event === 'gate.red')
      .map(({ ts: _ts, tokens_source: _tokens, cost_source: _cost, ...row }) => row),
  };
}

test('B135: configured test gate refuses missing or failed gates without closing or writing rows', async () => {
  const cases = [[], [{ event: 'gate.done', all_ok: false }], [{ event: 'gate.red', gate: 'test' }]];
  for (const [i, rows] of cases.entries()) {
    const f = await fixture(`b118-close-${i}`, ['node', '--test']);
    for (const row of rows) await f.sign(row);
    // A green gate for another block or run cannot satisfy this block's requirement.
    await f.sign({ event: 'gate.done', block: 'B2', all_ok: true });
    await f.sign({ event: 'gate.done', run: 'r-other', all_ok: true });
    const before = (await f.rows()).length;
    assert.deepEqual(await f.close(), [1, '', f.refusal]);
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
    assert.equal((await f.rows()).length, before, 'refused close writes zero rows');
  }
});

test('B135 boundary: any green row suffices without a later commit; null test gates need none', async () => {
  const cases = [
    { testGate: ['node', '--test'], rows: [{ event: 'gate.red', gate: 'test' }, { event: 'gate.done', all_ok: true }] },
    { testGate: null, rows: [] },
    { testGate: null, rows: [{ event: 'gate.done', all_ok: false }] },
    { testGate: ['node', '--test'], rows: [{ event: 'gate.done', all_ok: true }, { event: 'gate.red', gate: 'test' }] },
    { testGate: ['node', '--test'], rows: [{ event: 'gate.done', all_ok: true }, { event: 'gate.done', all_ok: false }] },
  ];
  for (const [i, c] of cases.entries()) {
    const f = await fixture(`b118-close-boundary-${i}`, c.testGate);
    for (const row of c.rows) await f.sign(row);
    assert.deepEqual(await f.close(), [0, f.stdout, '']);
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
  }
});

test('B135: waived reviews still require fresh green gates and keep the human confirmation', async () => {
  for (const evidence of ['missing', 'failed', 'stale', 'unconfigured']) {
    const f = await fixture(`b135-waived-gate-${evidence}`, evidence === 'unconfigured' ? null : ['node', '--test']);
    if (evidence === 'failed') await f.sign({ event: 'gate.done', all_ok: false });
    if (evidence === 'stale') {
      await git(['add', 'a.mjs'], f.repo);
      await git(['commit', '-q', '-m', 'block changes'], f.repo);
      const committed = Number((await git(['log', '-1', '--format=%ct'], f.repo)).trim()) * 1000;
      await f.sign({ event: 'gate.done', all_ok: true, ts: new Date(committed - 1000).toISOString() });
    }
    let confirms = 0;
    const close = () => f.close(['--no-require-reviews'], { isTTY: true, ui: {
      confirm: async () => { confirms++; return true; }, isCancel: () => false,
    } });
    if (evidence !== 'unconfigured') {
      assert.deepEqual(await close(), [1, '', f.refusal], evidence);
      assert.equal(confirms, 1, 'one confirmation per close attempt');
      assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
      assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 0);
      await f.sign({ event: 'gate.done', all_ok: true, ts: new Date(Date.now() + 1000).toISOString() });
    }
    assert.deepEqual(await close(), [0, 'block B1 closed\n', '']);
    assert.equal(confirms, evidence === 'unconfigured' ? 1 : 2);
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 1);
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

test('B135: close succeeds after real green gates and refuses after real red gates', async () => {
  for (const exit of [0, 1]) {
    const f = await fixture(`b120-close-${exit}`, ['node', '-e', `process.exit(${exit})`]);
    const [code, stdout, stderr] = await f.gates();
    assert.deepEqual([code, stderr, JSON.parse(stdout).allOk], [exit, '', exit === 0]);
    assert.deepEqual(await f.close(), exit === 0 ? [0, f.stdout, ''] : [1, '', f.refusal]);
    assert.equal((await readRun(f.runId)).blocks.B1.status, exit === 0 ? 'closed' : 'open');
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close' && r.block === 'B1').length, exit === 0 ? 1 : 0);
  }
});

test('B135 boundary: a real red plain run after green retains both runs and the green evidence', async () => {
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
  assert.deepEqual(await f.close(), [0, f.stdout, '']);
});

test('B135: green evidence older than the last block commit refuses; a newer green row closes', async () => {
  const f = await fixture('b135-stale-gates', ['node', '--test']);
  await git(['add', 'a.mjs'], f.repo);
  await git(['commit', '-q', '-m', 'block changes'], f.repo);
  const committed = Number((await git(['log', '-1', '--format=%ct'], f.repo)).trim()) * 1000;
  await f.sign({ event: 'gate.done', all_ok: true, ts: new Date(committed - 1000).toISOString() });
  assert.deepEqual(await f.close(), [1, '', f.refusal]);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
  assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 0);
  await f.sign({ event: 'gate.done', all_ok: true, ts: new Date(committed + 1000).toISOString() });
  assert.deepEqual(await f.close(), [0, f.stdout, '']);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
  assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 1);
});

test('B135: failed git log refuses green evidence; successful empty log permits closing', async () => {
  const f = await fixture('b135-gate-log-failure', ['node', '--test']);
  await f.sign({ event: 'gate.done', all_ok: true });
  const before = await f.rows();
  failGitLog = true;
  try {
    assert.deepEqual(await f.close(), [1, '', f.refusal]);
  } finally {
    failGitLog = false;
  }
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
  assert.deepEqual(await f.rows(), before, 'a log failure writes zero rows');
  const base = (await readRun(f.runId)).blocks.B1.base_sha;
  assert.equal(await git(['log', '-1', '--format=%ct', `${base}..HEAD`, '--', 'a.mjs'], f.repo), '');
  assert.deepEqual(await f.close(), [0, f.stdout, '']);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
  assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 1);
});
