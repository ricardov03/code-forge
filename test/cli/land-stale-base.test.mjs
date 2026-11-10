// B363: `block land` merges onto the new tip and its merge gate tests the merged tree, so a block whose
// base branch moved past its recorded base still runs its gates, with the one --allow-stale-base WARN line.
import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

for (const key of ['CODE_FORGE_GATES_BASE_CHECKED']) delete process.env[key];
const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { readRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, enqueue, retractWorker, writeResult } = await import('../../src/worker/queue.mjs');

const probe = async (/** @type {number} */ pid) => (pid === process.pid ? 'land-stale-worker' : null);

/** @param {string[]} args */
async function cli(args) {
  let stdout = '', stderr = '';
  const code = await runBlock(args, {
    stdout: { write: (/** @type {string} */ s) => { stdout += s; return true; } },
    stderr: { write: (/** @type {string} */ s) => { stderr += s; return true; } }, probe,
  });
  return { code, stdout, stderr };
}

test('land of a stale-base block with the merge gate on still runs the gates and prints the WARN line once', async () => {
  const repo = freshDir('land-stale');
  const inputs = freshDir('land-stale-inputs');
  const runs = path.join(inputs, 'suite-runs.jsonl');
  const slug = 'land-stale';
  const runId = `r-${slug}`;
  const config = { version: 1, project: { slug }, ...cfgFor(),
    proof: { isolation: 'worktree', worktree: { dir: freshDir('land-stale-trees') } },
    gates: { test: [process.execPath, 'suite.cjs', '{test_files}'], lint: null, types: null, format: null, merge_gate: true } };
  await git(['init', '-q', '-b', 'main'], repo);
  await git(['config', 'user.name', 'Fake Tester'], repo);
  await git(['config', 'user.email', 'fake@example.invalid'], repo);
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, 'value.txt', 'base\n');
  writeFile(repo, 'test/near.test.mjs', '// scoped test\n');
  writeFile(repo, 'suite.cjs', `require('node:fs').appendFileSync(${JSON.stringify(runs)}, JSON.stringify({ full: process.argv.length === 2 }) + '\\n');
console.log('TAP version 13');
console.log('ok 1 - suite green');
`);
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'land a stale base', tests: ['land-stale-base'] }]));
  writeFile(inputs, 'report.txt', 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n');
  writeFile(inputs, 'transcript.log', '$ node --test\n');
  const writeRow = (/** @type {Record<string, unknown>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, probe, config, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'value.txt', '--tests', 'test/near.test.mjs', '--acceptance', path.join(inputs, 'acceptance.json')]);
  assert.deepEqual([opened.code, opened.stderr], [0, ''], 'the block opens in a worktree');
  const { worktree } = (await readRun(runId)).blocks.B1;
  writeFile(worktree.path, 'value.txt', 'block\n');
  await git(['add', 'value.txt'], worktree.path);
  await git(['commit', '-q', '-m', 'block change'], worktree.path);
  // main moves one commit past the block's recorded base
  writeFile(repo, 'main-only.txt', 'main addition\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'main advances'], repo);
  const tip = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const ticket = enqueue({ repoRoot: repo, tree: worktree.path, run: runId, block: 'B1', file: 'value.txt' }).ticket;
  writeResult({ repoRoot: repo, runId, ticket, key: await loadKey(runId), result: { status: 'reviewed', approved: true, findings: [] } });
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'value.txt', content_hash: contentHash(worktree.path, 'value.txt'), round: 1 });
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const result = await cli(['land', 'B1', '--run', runId, '--report', path.join(inputs, 'report.txt'), '--transcript', path.join(inputs, 'transcript.log'), '--merge']);
    assert.equal(result.code, 0, result.stderr);
    const warns = result.stderr.split('\n').filter((line) => line.includes('WARN') && line.includes('stale base'));
    assert.equal(warns.length, 1, `exactly one WARN line, got: ${result.stderr}`);
    assert.ok(warns[0].includes(`main moved ${worktree.base_sha.slice(0, 12)} → ${tip.slice(0, 12)}`), warns[0]);
    assert.ok(warns[0].includes(`fix: code-forge block rebase B1 --run ${runId} --accept-landed`), warns[0]);
    assert.ok(!result.stderr.includes('no gate was run'), `land never refuses at the stale check: ${result.stderr}`);
    const suite = readFileSync(runs, 'utf8').trim().split('\n').map((line) => JSON.parse(line).full);
    assert.deepEqual(suite, [false, true], 'the scoped gate ran once, then the merge gate ran the full suite once');
    assert.equal(result.stdout.trim().split('\n').at(-1), 'block B1 landed');
  } finally { retractWorker(repo, process.pid); }
});
