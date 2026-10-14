import { freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
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

/** @param {string} slug @param {string[] | null} testGate */
async function fixture(slug, testGate) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('close-files');
  writeFile(files, 'acc.json', JSON.stringify([{ clause: 'gate warning', tests: ['b118'] }]));
  writeFile(files, 'coder.log', '$ node --test\n');
  const runId = `r-${slug}`;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config: { gates: { test: testGate } }, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, 'acc.json')]);
  assert.deepEqual([opened[0], opened[2]], [0, '']);
  writeFile(repo, 'a.mjs', 'export const a = 2;\n');
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(repo, 'a.mjs'), round: 1 });
  const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: 'a.mjs' }).ticket;
  return {
    runId,
    stdout: `reviewed a.mjs ticket ${ticket}\nblock B1 closed\n`,
    warning: `WARN block B1: no green gates run for this block; run gates run --run ${runId} --block B1 before close\n`,
    sign: (/** @type {Record<string, any>} */ row) => writeSigned(runId, writeRow, { block: 'B1', ...row }),
    close: () => cli(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(files, 'coder.log')]),
    rows: () => readAllRows(slug),
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
