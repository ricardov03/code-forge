import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { openBlock, rebaseBlock } from '../../src/state/block.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';
import { prepareWorktree, worktreeSettings } from '../../src/proof/worktree.mjs';

for (const localIdentity of [false, true]) test(`rebase keeps the original committer with useConfigOnly and ${localIdentity ? 'a different' : 'no'} local identity`, async () => {
  const repo = freshDir('rebase-identity');
  await git(['init', '-q', '-b', 'main'], repo);
  await git(['config', 'user.useConfigOnly', 'true'], repo);
  if (localIdentity) {
    await git(['config', 'user.name', 'Local User'], repo);
    await git(['config', 'user.email', 'local@example.invalid'], repo);
  }
  writeFile(repo, 'upstream.mjs', 'base\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-qm', 'base'], repo);
  const config = { version: 1, ...cfgFor(), proof: { isolation: 'worktree', worktree: { dir: freshDir('trees') } } };
  const runId = `r-rebase-identity-${localIdentity}`;
  const writeRow = async () => {};
  await startRun({ workspace: repo, project: 'rebase-identity', runId, config, writeRow });
  const settings = worktreeSettings(await readRun(runId));
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['owned.mjs'], acceptance: [{ clause: 'preserve committer', tests: ['rebase-identity'] }], writeRow,
    worktree: (ctx) => prepareWorktree({ cwd: repo, runId, id: 'B1', settings, ...ctx }),
  });
  const tree = (await readRun(runId)).blocks.B1.worktree.path;
  for (const value of ['first', 'second']) {
    writeFile(tree, 'owned.mjs', `${value}\n`);
    await git(['add', 'owned.mjs'], tree);
    await git(['-c', 'user.name=Original Committer', '-c', 'user.email=original@example.invalid', 'commit', '-qm', value, '--author=Original Author <author@example.invalid>'], tree);
  }
  const original = (await git(['log', '-2', '--format=%s%x00%cn%x00%ce'], tree)).trim().split('\n');
  writeFile(repo, 'upstream.mjs', 'upstream\n');
  await git(['add', 'upstream.mjs'], repo);
  await git(['commit', '-qm', 'upstream'], repo);
  const upstream = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const oldHead = (await git(['rev-parse', 'HEAD'], tree)).trim();
  const inherited = { GIT_COMMITTER_NAME: process.env.GIT_COMMITTER_NAME, GIT_COMMITTER_EMAIL: process.env.GIT_COMMITTER_EMAIL };
  process.env.GIT_COMMITTER_NAME = 'Inherited Hook';
  process.env.GIT_COMMITTER_EMAIL = 'hook@example.invalid';
  try {
    let failure;
    try { await rebaseBlock({ runId, id: 'B1', writeRow }); } catch (err) { failure = err; }
    assert.equal(failure, undefined, 'rebase succeeds without guessing a committer identity');
  } finally {
    for (const [key, value] of Object.entries(inherited)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
  }
  assert.notEqual((await git(['rev-parse', 'HEAD'], tree)).trim(), oldHead, 'the block commits were actually replayed');
  assert.equal((await git(['rev-parse', 'HEAD~2'], tree)).trim(), upstream, 'exactly two block commits follow upstream');
  assert.deepEqual((await git(['log', '-2', '--format=%s%x00%cn%x00%ce'], tree)).trim().split('\n'), original, 'both replayed commits keep their original committer, ignoring local and inherited identities');
  assert.deepEqual((await git(['log', '-2', '--format=%an <%ae>'], tree)).trim().split('\n'), Array(2).fill('Original Author <author@example.invalid>'));
});
