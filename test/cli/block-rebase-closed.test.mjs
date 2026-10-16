import { cfgFor, freshDir, git, lines, writeFile } from '../review/helpers.mjs';
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
const CHANGED = ['a.mjs', 'b.mjs', 'gone.mjs', 'new.mjs'];
const source = (first = 'base', last = 'base') => lines(30, (i) => `// line ${i}: ${i === 1 ? first : i === 30 ? last : 'base'}`);

/** @param {string[]} args @returns {Promise<[number, string, string]>} */
async function cli(args) {
  let stdout = '';
  let stderr = '';
  const code = await runBlock(args, {
    stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } }, probe: PROBE,
  });
  return [code, stdout, stderr];
}

/** @param {string} slug @param {boolean} [detached] */
async function fixture(slug, detached = false) {
  const repo = freshDir(slug);
  await git(['init', '-q', '-b', 'main'], repo);
  await git(['config', 'user.name', 'Fixture'], repo);
  await git(['config', 'user.email', 'fixture@example.invalid'], repo);
  const config = { version: 1, project: { slug }, ...cfgFor(), proof: { isolation: 'worktree', worktree: { dir: freshDir('trees') } } };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  for (const file of ['a.mjs', 'b.mjs', 'gone.mjs', 'untouched.mjs']) writeFile(repo, file, source());
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  if (detached) await git(['checkout', '-q', '--detach'], repo);
  const files = freshDir('inputs');
  writeFile(files, 'acceptance.json', JSON.stringify([{ clause: 'closed rebase', tests: ['block-rebase-closed'] }]));
  writeFile(files, 'coder.log', '$ node --test\n');
  const runId = `r-${slug}`;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', '*.mjs', '--acceptance', path.join(files, 'acceptance.json')]);
  assert.deepEqual([opened[0], opened[2]], [0, '']);
  const tree = (await readRun(runId)).blocks.B1.worktree;
  for (const file of ['a.mjs', 'b.mjs']) writeFile(tree.path, file, source('block'));
  writeFile(tree.path, 'new.mjs', '// added by block\n');
  await git(['rm', '-q', 'gone.mjs'], tree.path);
  await git(['add', '-A'], tree.path);
  await git(['commit', '-q', '-m', 'reviewed block'], tree.path);
  const tickets = [];
  for (const file of CHANGED) {
    await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file, content_hash: contentHash(tree.path, file), round: 1 });
    tickets.push(`reviewed ${file} ticket ${resolveTicket({ repoRoot: repo, tree: tree.path, run: runId, block: 'B1', file }).ticket}\n`);
  }
  return {
    repo, runId, base, tree,
    closeOutput: `${tickets.join('')}block B1 closed\n`,
    close: () => cli(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(files, 'coder.log')]),
    rebase: () => cli(['rebase', 'B1', '--run', runId]),
    rows: () => readAllRows(slug),
    entry: async () => (await readRun(runId)).blocks.B1,
    move: async (kind = 'docs', count = 1) => {
      await git(['checkout', '-q', 'main'], repo);
      for (let i = 0; i < count; i++) {
        if (kind === 'changed') {
          for (const file of ['a.mjs', 'b.mjs']) writeFile(repo, file, source('base', 'upstream'));
        } else if (kind === 'conflict') writeFile(repo, 'a.mjs', source('upstream'));
        else writeFile(repo, 'docs.md', `upstream docs ${i}\n`);
        // Owned upstream files outside the block's diff must not enter the reviewed set.
        writeFile(repo, 'untouched.mjs', source('upstream'));
        await git(['add', '-A'], repo);
        await git(['commit', '-q', '-m', `upstream ${i}`], repo);
      }
      return (await git(['rev-parse', 'HEAD'], repo)).trim();
    },
  };
}

/** @param {string} tree @param {string} ref */
async function blobs(tree, ref) {
  const found = (await git(['ls-tree', '-r', '-z', ref, '--', ...CHANGED], tree)).split('\0').filter(Boolean);
  return Object.fromEntries(CHANGED.map((file) => [file, found.find((entry) => entry.endsWith(`\t${file}`))?.split(' ')[2].split('\t')[0] ?? null]));
}

test('B134: close warns exactly once with the base commit count and rebase command, only when its recorded branch moved', async () => {
  for (const { count, detached } of [{ count: 1, detached: false }, { count: 2, detached: false }, { count: 0, detached: false }, { count: 1, detached: true }]) {
    const f = await fixture(`b134-warning-${count}-${detached}`, detached);
    if (count) {
      await f.move('docs', count);
      // The workspace's current HEAD is not the base branch HEAD being checked.
      await git(['checkout', '-q', '-b', 'elsewhere', f.base], f.repo);
    }
    const warning = count && !detached
      ? `WARN block B1: base moved: main is ${count} commit(s) ahead of the block base; run: code-forge block rebase B1 --run ${f.runId}\n` : '';
    assert.deepEqual(await f.close(), [0, f.closeOutput, warning], `count=${count}, detached=${detached}`);
    assert.equal((await f.entry()).status, 'closed');
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 1);
  }
});

test('B134: closed worktree rebase preserves every changed blob, updates both bases and writes one signed closed row', async () => {
  for (const moved of [true, false]) {
    const f = await fixture(`b134-rebase-${moved}`);
    assert.deepEqual(await f.close(), [0, f.closeOutput, '']);
    const oldHead = (await git(['rev-parse', 'HEAD'], f.tree.path)).trim();
    const before = await blobs(f.tree.path, oldHead);
    assert.deepEqual(Object.keys(before), CHANGED);
    assert.equal(before['gone.mjs'], null);
    const newBase = moved ? await f.move() : f.base;
    // Resolve the stored main branch, even from a workspace checked out elsewhere.
    await git(['checkout', '-q', '-b', 'elsewhere', f.base], f.repo);
    assert.deepEqual(await f.rebase(), [0, `block B1 rebased ${f.base.slice(0, 12)} → ${newBase.slice(0, 12)}\n`, '']);
    assert.deepEqual(await blobs(f.tree.path, 'HEAD'), before, 'all four changed paths retain their exact blob or deletion');
    assert.equal((await git(['rev-parse', 'HEAD^'], f.tree.path)).trim(), newBase);
    assert.equal((await git(['rev-parse', `refs/heads/${f.tree.branch}`], f.repo)).trim(), (await git(['rev-parse', 'HEAD'], f.tree.path)).trim());
    assert.equal((await git(['rev-parse', 'HEAD'], f.repo)).trim(), f.base, 'workspace stays on its own HEAD');
    const entry = await f.entry();
    assert.deepEqual([entry.status, entry.base_sha, entry.base_branch, entry.worktree.base_sha], ['closed', newBase, 'main', newBase]);
    const rows = (await f.rows()).filter((r) => r.event === 'block.rebase');
    assert.equal(rows.length, 1);
    assert.deepEqual([rows[0].block, rows[0].old_base, rows[0].new_base, rows[0].closed], ['B1', f.base, newBase, true]);
    assert.deepEqual(verifyRow(rows[0], await loadKey(f.runId)), { ok: true });
    if (moved) assert.notEqual((await git(['rev-parse', 'HEAD'], f.tree.path)).trim(), oldHead);
  }
});

for (const kind of ['changed', 'conflict']) {
  test(`B134: closed worktree rebase refuses ${kind} and restores the old branch, tree and record`, async () => {
    const f = await fixture(`b134-refuse-${kind}`);
    assert.deepEqual(await f.close(), [0, f.closeOutput, '']);
    const oldHead = (await git(['rev-parse', 'HEAD'], f.tree.path)).trim();
    const before = await blobs(f.tree.path, oldHead);
    const entry = await f.entry();
    const rows = await f.rows();
    const newBase = await f.move(kind);
    const reason = kind === 'changed' ? 'rebase-changed-reviewed a.mjs, b.mjs' : 'rebase-conflict';
    assert.deepEqual(await f.rebase(), [1, '', `block rebase: ${reason}\n`]);
    assert.equal((await git(['rev-parse', 'HEAD'], f.tree.path)).trim(), oldHead);
    assert.equal((await git(['rev-parse', `refs/heads/${f.tree.branch}`], f.repo)).trim(), oldHead);
    assert.equal(await git(['status', '--porcelain'], f.tree.path), '', 'rollback leaves no conflict or staged change');
    assert.deepEqual(await blobs(f.tree.path, 'HEAD'), before);
    assert.deepEqual(await f.entry(), entry);
    assert.deepEqual(await f.rows(), rows, 'refusal writes zero rows');
    assert.equal((await git(['rev-parse', 'HEAD'], f.repo)).trim(), newBase);
  });
}
