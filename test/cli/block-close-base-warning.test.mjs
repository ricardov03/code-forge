import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { mock, test } from 'node:test';

const { exec: realExec } = await import('../../src/util/exec.mjs');
let failAncestry = false;
mock.module('../../src/util/exec.mjs', { namedExports: {
  exec: (argv, options) => failAncestry && argv[0] === 'git' && argv[1] === 'merge-base'
    ? Promise.resolve({ result: 'failed', code: 128, signal: null, stdout: '', stderr: 'git ancestry failed', timedOut: false })
    : realExec(argv, options),
} });

const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { readRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { resolveTicket } = await import('../../src/worker/queue.mjs');

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

/** @param {string} slug @param {boolean} worktree */
async function fixture(slug, worktree) {
  const repo = freshDir(slug);
  await git(['init', '-q', '-b', 'main'], repo);
  const config = {
    version: 1, project: { slug }, ...cfgFor(),
    ...(worktree ? { proof: { isolation: 'worktree', worktree: { dir: freshDir('trees') } } } : {}),
  };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('inputs');
  writeFile(files, 'acceptance.json', JSON.stringify([{ clause: 'base warning ancestry', tests: ['block-close-base-warning'] }]));
  writeFile(files, 'coder.log', '$ node --test\n');
  const runId = `r-${slug}`;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, 'acceptance.json')]);
  assert.deepEqual([opened[0], opened[2]], [0, '']);
  const block = (await readRun(runId)).blocks.B1;
  assert.equal(Boolean(block.worktree), worktree);
  const tree = block.worktree?.path ?? repo;
  writeFile(tree, 'a.mjs', 'export const a = 2;\n');
  await git(['add', 'a.mjs'], tree);
  await git(['commit', '-q', '-m', 'block changes'], tree);
  const blockHead = (await git(['rev-parse', 'HEAD'], tree)).trim();
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(tree, 'a.mjs'), round: 1 });
  const ticket = resolveTicket({ repoRoot: repo, ...(worktree ? { tree } : {}), run: runId, block: 'B1', file: 'a.mjs' }).ticket;
  return {
    repo, runId, blockHead,
    stdout: `reviewed a.mjs ticket ${ticket}\nblock B1 closed\n`,
    close: () => cli(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(files, 'coder.log')]),
  };
}

test('B134: commits on the base branch without a worktree close without a base warning', async () => {
  const f = await fixture('b134-base-own-commits', false);
  assert.deepEqual(await f.close(), [0, f.stdout, '']);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
});

test('B134: a worktree block warns when main gains a commit absent from the block', async () => {
  const f = await fixture('b134-base-diverged', true);
  writeFile(f.repo, 'docs.md', 'upstream documentation\n');
  await git(['add', 'docs.md'], f.repo);
  await git(['commit', '-q', '-m', 'upstream docs'], f.repo);
  assert.deepEqual(await f.close(), [0, f.stdout,
    `WARN block B1: base moved: main is 1 commit(s) ahead of the block base; run: code-forge block rebase B1 --run ${f.runId}\n`]);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
});

test('B134: main fast-forwarded to the worktree block commit closes without a base warning', async () => {
  const f = await fixture('b134-base-fast-forward', true);
  await git(['merge', '--ff-only', f.blockHead], f.repo);
  assert.deepEqual(await f.close(), [0, f.stdout, '']);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
});

test('B134: failed git ancestry checks suppress the advisory and still close', async () => {
  const f = await fixture('b134-base-ancestry-failure', true);
  await git(['commit', '-q', '--allow-empty', '-m', 'upstream commit'], f.repo);
  failAncestry = true;
  try {
    assert.deepEqual(await f.close(), [0, f.stdout, '']);
  } finally {
    failAncestry = false;
  }
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
});
