import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';
import { runBlock } from '../../src/cli/block.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { readRun, startRun, writeSigned } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { contentHash } from '../../src/worker/ticket.mjs';
import { resolveTicket } from '../../src/worker/queue.mjs';

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

/** @param {string} slug @param {string} [prefix] */
async function fixture(slug, prefix = '') {
  const repo = freshDir(slug);
  const workspace = path.join(repo, prefix);
  const ownedFile = prefix ? `${prefix}/src/a.mjs` : 'src/a.mjs';
  await git(['init', '-q', '-b', 'main'], repo);
  await git(['config', 'user.name', 'Fixture'], repo);
  await git(['config', 'user.email', 'fixture@example.invalid'], repo);
  const config = {
    version: 1, project: { slug }, ...cfgFor(),
    gates: { test: ['node', '--test'], lint: null, types: null, format: null },
    proof: { isolation: 'worktree', worktree: { dir: freshDir('trees') } },
  };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(workspace, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, ownedFile, '// base\n');
  writeFile(repo, 'docs.md', 'base docs\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const files = freshDir('inputs');
  writeFile(files, 'acceptance.json', JSON.stringify([{ clause: 'open rebase', tests: ['block-rebase-open'] }]));
  writeFile(files, 'coder.log', '$ node --test\n');
  const runId = `r-${slug}`;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  const sign = (/** @type {Record<string, any>} */ row) => writeSigned(runId, writeRow, { block: 'B1', ...row });
  await startRun({ workspace, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'src', '--acceptance', path.join(files, 'acceptance.json')]);
  assert.deepEqual([opened[0], opened[2]], [0, '']);
  const tree = (await readRun(runId)).blocks.B1.worktree;
  writeFile(tree.path, ownedFile, '// block\n');
  await git(['add', '-A'], tree.path);
  await git(['commit', '-q', '-m', 'block changes'], tree.path);
  await sign({ event: 'review.approved', file: ownedFile, content_hash: contentHash(tree.path, ownedFile), round: 1 });
  const ticket = resolveTicket({ repoRoot: repo, tree: tree.path, run: runId, block: 'B1', file: ownedFile }).ticket;
  return {
    repo, runId, base, tree, ownedFile, sign,
    closeOutput: `reviewed ${ownedFile} ticket ${ticket}\nblock B1 closed\n`,
    close: () => cli(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(files, 'coder.log')]),
    rebase: () => cli(['rebase', 'B1', '--run', runId]),
    rows: () => readAllRows(slug),
    entry: async () => (await readRun(runId)).blocks.B1,
    head: async () => (await git(['rev-parse', 'HEAD'], tree.path)).trim(),
    green: () => sign({ event: 'gate.done', all_ok: true, ts: new Date(Date.now() + 60000).toISOString() }),
    move: async (file = 'docs.md', text = 'upstream docs\n') => {
      writeFile(repo, file, text);
      await git(['add', '-A'], repo);
      await git(['commit', '-q', '-m', 'upstream'], repo);
      return (await git(['rev-parse', 'HEAD'], repo)).trim();
    },
  };
}

test('B159: open worktree rebase follows the recorded base branch, signs both bases and closes without upstream unowned changes', async () => {
  for (const prefix of ['', 'project']) {
    const f = await fixture(`b159-follow-${prefix || 'root'}`, prefix);
    const oldHead = await f.head();
    const newBase = await f.move();
    await git(['checkout', '-q', '-b', 'elsewhere', f.base], f.repo);
    assert.deepEqual(await f.rebase(), [0, `block B1 rebased ${f.base.slice(0, 12)} → ${newBase.slice(0, 12)}\n`, '']);
    assert.notEqual(await f.head(), oldHead, 'the block commit is replayed onto main');
    assert.equal((await git(['rev-parse', 'HEAD^'], f.tree.path)).trim(), newBase, 'main commit is the new parent');
    assert.equal(await git(['show', `HEAD:${f.ownedFile}`], f.tree.path), '// block\n', 'open owned changes survive');
    assert.equal((await git(['rev-parse', 'HEAD'], f.repo)).trim(), f.base, 'workspace checkout stays put');
    const entry = await f.entry();
    assert.deepEqual([entry.status, entry.base_sha, entry.worktree.base_sha, entry.base_branch], ['open', newBase, newBase, 'main']);
    const rows = (await f.rows()).filter((r) => r.event === 'block.rebase');
    assert.equal(rows.length, 1);
    assert.deepEqual([rows[0].block, rows[0].old_base, rows[0].new_base, rows[0].worktree, rows[0].closed], ['B1', f.base, newBase, true, undefined]);
    assert.deepEqual(verifyRow(rows[0], await loadKey(f.runId)), { ok: true });
    await f.green();
    assert.deepEqual(await f.close(), [0, f.closeOutput, ''], 'upstream docs produce zero unowned_change refusals');
    assert.equal((await f.entry()).status, 'closed');
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 1);
  }
});

test('B159: owned upstream commits, dirty trees and real unowned git conflicts refuse without moving the branch, bases or rows', async () => {
  for (const kind of ['owned', 'owned-reverted', 'dirty', 'conflict']) {
    const f = await fixture(`b159-refuse-${kind}`, kind.startsWith('owned') ? 'project' : '');
    if (kind === 'conflict') {
      // Committed unowned edits can conflict in git even though the owned-file precheck passes.
      writeFile(f.tree.path, 'docs.md', 'block docs\n');
      await git(['add', 'docs.md'], f.tree.path);
      await git(['commit', '-q', '-m', 'conflicting unowned edit'], f.tree.path);
    }
    const oldHead = await f.head();
    const entry = await f.entry();
    const rows = await f.rows();
    await f.move(kind.startsWith('owned') ? f.ownedFile : 'docs.md', kind.startsWith('owned') ? '// upstream\n' : 'upstream docs\n');
    if (kind === 'owned-reverted') await f.move(f.ownedFile, '// base\n');
    if (kind === 'dirty') writeFile(f.tree.path, 'untracked.txt', 'keep me\n');
    const status = await git(['status', '--porcelain'], f.tree.path);
    const result = await f.rebase();
    assert.deepEqual(result.slice(0, 2), [1, ''], kind);
    if (kind.startsWith('owned')) assert.equal(result[2], `block rebase: block B1: commits since its base changed owned files: ${f.ownedFile}\n`);
    else if (kind === 'dirty') assert.match(result[2], /rebase-dirty.*commit or clean first/);
    else assert.equal(result[2], 'block rebase: rebase-conflict\n');
    assert.equal(await f.head(), oldHead, `${kind}: old head restored`);
    assert.equal((await git(['rev-parse', `refs/heads/${f.tree.branch}`], f.repo)).trim(), oldHead);
    assert.equal(await git(['status', '--porcelain'], f.tree.path), status, `${kind}: no residual conflict or lost edits`);
    assert.deepEqual(await f.entry(), entry, `${kind}: both bases and status unchanged`);
    assert.deepEqual(await f.rows(), rows, `${kind}: zero rows written`);
  }
});

test('B159: a green gates row before rebase cannot close the changed tree; fresh green gates can', async () => {
  const f = await fixture('b159-stale-gates');
  // A future timestamp isolates row ordering from git commit timestamps and clock resolution.
  await f.green();
  const oldHead = await f.head();
  await f.move();
  assert.equal((await f.rebase())[0], 0, 'open tree rebases successfully');
  assert.notEqual(await f.head(), oldHead);
  const rows = await f.rows();
  assert.deepEqual(await f.close(), [1, '', `block B1 open: no green gates run; run: code-forge gates run --run ${f.runId} --block B1\n`]);
  assert.equal((await f.entry()).status, 'open');
  assert.deepEqual(await f.rows(), rows, 'stale green refusal writes zero rows');
  await f.green();
  assert.deepEqual(await f.close(), [0, f.closeOutput, '']);
  assert.equal((await f.rows()).filter((r) => r.event === 'gate.done').length, 2);
  assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 1);
});
