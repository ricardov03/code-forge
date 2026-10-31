import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { readRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, enqueue, retractWorker, writeResult } = await import('../../src/worker/queue.mjs');

const probe = async (pid) => pid === process.pid ? 'merge-gate-worker' : null;

async function cli(args) {
  let stdout = '', stderr = '';
  const code = await runBlock(args, {
    stdout: { write: (s) => { stdout += s; } },
    stderr: { write: (s) => { stderr += s; } }, probe,
  });
  return { code, stdout, stderr };
}

async function fixture(name, { conflict = false, mergeGate = undefined } = {}) {
  const repo = freshDir(name);
  const inputs = freshDir('merge-inputs');
  const trees = freshDir('merge-trees');
  const runs = path.join(inputs, 'suite-runs.jsonl');
  const slug = `merge-${name}`;
  const runId = `r-${slug}`;
  const config = {
    version: 1, project: { slug }, ...cfgFor(),
    proof: { isolation: 'worktree', worktree: { dir: trees } },
    gates: { test: [process.execPath, 'suite.cjs', '{test_files}'], lint: null, types: null, format: null,
      ...(mergeGate === undefined ? {} : { merge_gate: mergeGate }) },
  };
  assert.deepEqual(validateConfig(config).errors, [], 'merge-gate config has zero validation errors');
  await git(['init', '-q', '-b', 'main'], repo);
  await git(['config', 'user.name', 'Fake Tester'], repo);
  await git(['config', 'user.email', 'fake@example.invalid'], repo);
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, 'value.txt', 'base\n');
  writeFile(repo, 'test/near.test.mjs', '// scoped test\n');
  writeFile(repo, 'suite.cjs', `const fs = require('node:fs');
const full = process.argv.length === 2;
fs.appendFileSync(${JSON.stringify(runs)}, JSON.stringify({cwd: process.cwd(), full}) + '\\n');
const red = full && fs.existsSync('main-only.txt') && fs.readFileSync('value.txt', 'utf8').trim() === 'block';
console.log('TAP version 13');
if (red) {
  for (let n = 1; n <= 12; n++) console.log('not ok ' + n + ' - far regression ' + n);
  process.exitCode = 1;
} else console.log('ok 1 - suite green');
`);
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'merge safely', tests: ['merge-gate'] }]));
  writeFile(inputs, 'report.txt', 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n');
  writeFile(inputs, 'transcript.log', '$ node --test\n');
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, probe, config, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'value.txt', '--tests', 'test/near.test.mjs', '--acceptance', path.join(inputs, 'acceptance.json')]);
  assert.deepEqual([opened.code, opened.stderr], [0, ''], 'fixture opens one worktree block');
  const { worktree } = (await readRun(runId)).blocks.B1;
  writeFile(worktree.path, 'value.txt', 'block\n');
  await git(['add', 'value.txt'], worktree.path);
  await git(['commit', '-q', '-m', 'block change'], worktree.path);
  const blockHead = (await git(['rev-parse', 'HEAD'], worktree.path)).trim();
  // Main advances independently: the block alone is green, the combined result can be red.
  writeFile(repo, conflict ? 'value.txt' : 'main-only.txt', conflict ? 'main\n' : 'main addition\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'main advances'], repo);
  const mainHead = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const ticket = enqueue({ repoRoot: repo, tree: worktree.path, run: runId, block: 'B1', file: 'value.txt' }).ticket;
  writeResult({ repoRoot: repo, runId, ticket, key: await loadKey(runId), result: { status: 'reviewed', approved: true, findings: [] } });
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'value.txt', content_hash: contentHash(worktree.path, 'value.txt'), round: 1 });
  announceWorker(repo, { pid: process.pid, run: runId });
  const registered = async () => (await git(['worktree', 'list', '--porcelain'], repo)).split('\n').filter((line) => line.startsWith('worktree '));
  const originalTrees = await registered();
  return {
    repo, inputs, trees, worktree, mainHead, blockHead,
    land: (args = []) => cli(['land', 'B1', '--run', runId, '--report', path.join(inputs, 'report.txt'), '--transcript', path.join(inputs, 'transcript.log'), '--merge', ...args]),
    suiteRuns: () => readFileSync(runs, 'utf8').trim().split('\n').map((line) => JSON.parse(line)),
    async unchangedAndClean() {
      assert.equal((await git(['rev-parse', 'HEAD'], repo)).trim(), mainHead, 'main HEAD stays exactly at its pre-land commit');
      assert.equal(await git(['status', '--porcelain', '--untracked-files=all'], repo), '', 'main has zero tracked, untracked or conflicted changes');
    },
    async noTemporaryTree() {
      assert.deepEqual(readdirSync(trees), ['B1'], 'worktree root contains only the original block tree');
      assert.deepEqual(await registered(), originalTrees, 'zero temporary worktree registrations remain');
    },
    cleanup: () => retractWorker(repo, process.pid),
  };
}

test('a block whose merged full suite fails is not landed and leaves main clean and unchanged', async () => {
  const f = await fixture('red');
  try {
    const result = await f.land(['--json']);
    assert.equal(result.code, 1, 'land refuses the failing merged full suite');
    assert.equal(JSON.parse(result.stdout).step, 'merge-gate', 'refusal identifies the merge-gate step');
    assert.deepEqual([...result.stderr.matchAll(/far regression \d+/g)].map((m) => m[0]), Array.from({ length: 10 }, (_, i) => `far regression ${i + 1}`), 'diagnostic lists exactly the first ten failing tests');
    assert.equal(result.stderr.split('\n').filter((line) => line.startsWith('next: ')).length, 1, 'refusal gives exactly one next action');
    await f.unchangedAndClean();
    await f.noTemporaryTree();
    const runs = f.suiteRuns();
    assert.deepEqual(runs.map((r) => r.full), [false, true], 'scoped gates pass before exactly one full suite');
    assert.equal(path.dirname(runs[1].cwd), f.trees, 'merged suite runs under the run worktree root');
    assert.equal(existsSync(runs[1].cwd), false, 'merged suite worktree is removed after failure');
  } finally { f.cleanup(); }
});

test('a green merged full suite permits a no-ff merge and reports its duration', async () => {
  const f = await fixture('green');
  try {
    // Keep the main-only addition, but make the full suite green on the merged result.
    await git(['mv', 'main-only.txt', 'green-main.txt'], f.repo);
    await git(['commit', '-q', '-m', 'green main'], f.repo);
    const mainHead = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
    const result = await f.land();
    assert.equal(result.code, 0, result.stderr);
    assert.match(result.stdout, /merge gate: full suite passed \(\d+(?:\.\d+)?s\)/);
    assert.equal(result.stdout.trim().split('\n').at(-1), 'block B1 landed');
    assert.deepEqual((await git(['rev-list', '--parents', '-n', '1', 'HEAD'], f.repo)).trim().split(' ').slice(1), [mainHead, f.blockHead], 'real merge has the tested main and block parents');
    assert.deepEqual(f.suiteRuns().map((r) => r.full), [false, true], 'exactly one full suite runs after scoped gates');
    assert.equal(await git(['status', '--porcelain'], f.repo), '', 'successful merge leaves main clean');
    await f.noTemporaryTree();
  } finally { f.cleanup(); }
});

test('a merge conflict refuses at merge-gate without changing main or leaking a worktree', async () => {
  const f = await fixture('conflict', { conflict: true });
  try {
    const result = await f.land(['--json']);
    assert.equal(result.code, 1, 'conflicting merge is refused');
    assert.equal(JSON.parse(result.stdout).step, 'merge-gate');
    assert.match(result.stderr, /conflict.*value\.txt/s, 'conflict diagnostic names the conflicted file');
    assert.equal(result.stderr.split('\n').filter((line) => line.startsWith('next: ')).length, 1);
    await f.unchangedAndClean();
    await f.noTemporaryTree();
    assert.deepEqual(f.suiteRuns().map((r) => r.full), [false], 'conflict runs zero full suites');
  } finally { f.cleanup(); }
});

test('--no-merge-gate or gates.merge_gate=false warns and merges without running the failing full suite', async () => {
  for (const configured of [false, true]) {
    const f = await fixture(`skip-${configured}`, configured ? { mergeGate: false } : {});
    try {
      const result = await f.land(configured ? [] : ['--no-merge-gate']);
      assert.equal(result.code, 0, result.stderr);
      assert.match(result.stdout + result.stderr, /warning.*merge gate.*skip/i);
      assert.deepEqual((await git(['rev-list', '--parents', '-n', '1', 'HEAD'], f.repo)).trim().split(' ').slice(1), [f.mainHead, f.blockHead]);
      assert.deepEqual(f.suiteRuns().map((r) => r.full), [false], 'skip runs zero full suites');
      await f.noTemporaryTree();
    } finally { f.cleanup(); }
  }
});
