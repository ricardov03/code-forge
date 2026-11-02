import { commitAll, freshDir, git, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { runBlock } from '../../src/cli/block.mjs';
import { loadProjectConfig } from '../../src/config/load.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';

const PROBE = async (/** @type {number} */ pid) => pid === 4242 ? 'start-A' : null;
const WORKTREE_SUFFIX = process.platform === 'darwin' ? '.noindex' : '';

/** @param {Function} fn @param {string[]} args */
async function cli(fn, args) {
  let output = '';
  const stream = { write: (/** @type {string} */ text) => { output += text; return true; } };
  const code = await fn(args, { stdout: stream, stderr: stream, probe: PROBE, isTTY: false });
  return { code, output };
}

/** Two commits, with HEAD and origin/main newer than the requested base. @param {string} name */
async function fixture(name) {
  const repo = await makeRepo();
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  writeFile(repo, '.code-forge.yml', [
    'version: 1', `project: {slug: ${name}}`, 'provider: anthropic', 'engine: harness',
    'levels:', '  L0: {model: claude-haiku-4-5-20251001}', '  L1: {model: claude-sonnet-5}',
    '  L2: {model: claude-opus-5-5}', '  L3: {model: claude-fable-5-1}',
    'proof:', '  isolation: worktree', '  worktree: {base: origin/main}',
    'review:', '  external: {required: true, name: fixture}', '',
  ].join('\n'));
  writeFile(repo, 'coder.log', '$ node --test\n');
  writeFile(repo, 'upstream.txt', 'newer upstream\n');
  await commitAll(repo);
  const head = (await git(['rev-parse', 'HEAD'], repo)).trim();
  await git(['update-ref', 'refs/remotes/origin/main', head], repo);
  const acc = path.join(freshDir('acceptance'), 'acceptance.json');
  writeFile(path.dirname(acc), path.basename(acc), JSON.stringify([{ clause: 'works', tests: ['fixture'] }]));
  const runId = `r-${name}`;
  // This fixture needs a signed run with its config snapshot, not CLI startup's
  // machine-wide process sweep. Repeating that sweep made unrelated host load part of each test.
  const loaded = await loadProjectConfig(repo);
  assert.equal(loaded.ok, true, loaded.message);
  await startRun({ workspace: repo, project: name, config: loaded.config, runId, workerPid: 4242,
    probe: PROBE, writeRow: (row) => appendRow(row, { slug: name }) });
  const rows = () => readAllRows(name);
  const tree = (/** @type {string} */ id) => `${repo}-worktrees${WORKTREE_SUFFIX}/${id}`;
  const branch = (/** @type {string} */ id) => `forge/${runId}/${id}`;
  const open = (/** @type {string} */ id, /** @type {string} */ sha, /** @type {string} */ owned = 'child.txt') =>
    cli(runBlock, ['open', id, '--run', runId, '--level', 'L1', '--owned', owned, '--acceptance', acc, '--base', sha]);
  const close = (/** @type {string} */ id) => cli(runBlock, ['close', id, '--run', runId, '--worker-pid', '4242', '--transcript', path.join(repo, 'coder.log')]);
  return { repo, runId, base, head, rows, tree, branch, open, close };
}

test('explicit older base controls worktree HEAD, recorded bases and the single dispatch despite origin/main configuration', { timeout: 60_000 }, async () => {
  const fx = await fixture('open-older-base');
  const opened = await fx.open('Child', fx.base);
  assert.equal(opened.code, 0, opened.output);
  assert.equal((await git(['rev-parse', 'HEAD'], fx.tree('Child'))).trim(), fx.base, 'worktree HEAD must be the requested older commit');
  const block = (await readRun(fx.runId)).blocks.Child;
  assert.deepEqual([block.base_sha, block.worktree.base_sha], [fx.base, fx.base]);
  const dispatch = (await fx.rows()).filter(r => r.event === 'dispatch');
  assert.deepEqual(dispatch.map(r => [r.block, r.base_sha]), [['Child', fx.base]]);
  assert.deepEqual((await fx.rows()).filter(r => r.event === 'worktree.created').map(r => r.base), [fx.base]);
  assert.equal((await git(['rev-parse', 'HEAD'], fx.repo)).trim(), fx.head, 'main checkout stays at its newer commit');
});

test('stacked block closes only its own diff against the parent branch tip', { timeout: 60_000 }, async () => {
  const fx = await fixture('open-stacked-base');
  const parent = await fx.open('Parent', fx.head, 'parent.txt');
  assert.equal(parent.code, 0, parent.output);
  writeFile(fx.tree('Parent'), 'parent.txt', 'parent work\n');
  await commitAll(fx.tree('Parent'));
  const parentTip = (await git(['rev-parse', fx.branch('Parent')], fx.repo)).trim();
  const child = await fx.open('Child', parentTip);
  assert.equal(child.code, 0, child.output);
  writeFile(fx.tree('Child'), 'child.txt', 'child work\n');
  await commitAll(fx.tree('Child'));
  assert.deepEqual((await git(['diff', '--name-only', parentTip], fx.tree('Child'))).trim().split('\n'), ['child.txt'], 'the child diff must contain exactly its own file');
  const closed = await fx.close('Child');
  assert.equal(closed.code, 0, closed.output);
  assert.equal((await readRun(fx.runId)).blocks.Child.status, 'closed');
  assert.deepEqual((await fx.rows()).filter(r => r.event === 'block.close').map(r => [r.block, r.status]), [['Child', 'complete']]);
});

test('existing unrecorded block branches reuse both an equal base and a descendant tip without resetting work', { timeout: 60_000 }, async () => {
  const fx = await fixture('open-existing-base');
  for (const [id, tip] of [['Equal', fx.base], ['Descendant', fx.head]]) {
    await git(['branch', fx.branch(id), tip], fx.repo);
    const opened = await fx.open(id, fx.base);
    assert.equal(opened.code, 0, opened.output);
    assert.equal((await git(['rev-parse', 'HEAD'], fx.tree(id))).trim(), tip, id + ': preserve existing branch tip');
    const block = (await readRun(fx.runId)).blocks[id];
    assert.deepEqual([block.base_sha, block.worktree.base_sha], [fx.base, fx.base]);
  }
  assert.deepEqual((await fx.rows()).filter(r => r.event === 'dispatch').map(r => [r.block, r.base_sha]), [['Equal', fx.base], ['Descendant', fx.base]]);
});

test('existing branch outside the base ancestry refuses with branch and recovery instructions and records nothing', { timeout: 60_000 }, async () => {
  const fx = await fixture('open-divergent-base');
  await git(['branch', fx.branch('Child'), fx.base], fx.repo);
  const before = await readRun(fx.runId);
  const rowsBefore = await fx.rows();
  const opened = await fx.open('Child', fx.head);
  assert.equal(opened.code, 1, opened.output);
  const refusal = `block open: block Child: branch ${fx.branch('Child')} does not descend from base ${fx.head} — delete the branch or pick another block id; nothing was recorded\n`;
  assert.equal(opened.output, refusal);
  assert.deepEqual(await readRun(fx.runId), before, 'refusal must leave the run unchanged');
  assert.deepEqual(await fx.rows(), rowsBefore, 'refusal must append zero rows');
  assert.equal(existsSync(fx.tree('Child')), false, 'refusal must create no worktree');
  assert.equal((await git(['rev-parse', fx.branch('Child')], fx.repo)).trim(), fx.base, 'refusal must preserve the existing branch');
});
