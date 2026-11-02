import { makeRepo, cli } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { test } from 'node:test';
import { readRun, saveRun } from '../../src/state/run.mjs';

test('status hints exactly once only while every open branch is merged into its base', async () => {
  const { repo, runId } = await makeRepo();
  const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trim();
  const base = git('branch', '--show-current');
  git('branch', 'merged-one');
  git('branch', 'merged-two');
  const record = await readRun(runId);
  record.blocks = Object.fromEntries(['one', 'two'].map((id) => [id, { status: 'open', base_branch: base,
    worktree: { path: `${repo}/removed-${id}`, branch: `merged-${id}` } }]));
  await saveRun(record);
  const hint = `all open blocks are merged: end the run with: code-forge run end --run ${runId}`;
  const all = await cli(['status', '--run', runId], repo, { CODE_FORGE_NATIVE: '0' });
  assert.equal(all.code, 0, all.stderr);
  assert.equal(all.stdout.split('\n').filter((line) => line === hint).length, 1, 'one hint for two merged blocks, even with removed worktrees');
  const original = record.blocks;
  record.blocks = Object.fromEntries(Array.from({ length: 12 }, (_, i) => ['block-' + i, original.one]));
  await saveRun(record);
  const capped = await cli(['status', '--run', runId], repo, { CODE_FORGE_NATIVE: '0' });
  assert.equal(capped.code, 0, capped.stderr);
  assert.equal(capped.stdout.trim().split('\n').length, 10, 'hint fits the ten-line status cap');
  assert.equal(capped.stdout.split('\n').filter((line) => line === '… +5 more blocks').length, 1, 'omitted count includes the block displaced by the hint');
  record.blocks = original;
  git('checkout', '-q', 'merged-two');
  git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '--allow-empty', '-m', 'unmerged');
  git('checkout', '-q', base);
  for (const state of ['unmerged', 'ended', 'empty', 'missing']) {
    if (state === 'ended') record.status = 'ended';
    if (state === 'empty') { record.status = 'active'; record.blocks = {}; }
    if (state === 'missing') record.blocks = { one: { status: 'open', base_branch: base, worktree: { branch: 'missing' } } };
    await saveRun(record);
    const result = await cli(['status', '--run', runId], repo, { CODE_FORGE_NATIVE: '0' });
    assert.equal(result.code, 0, result.stderr);
    assert.equal(result.stdout.includes(hint), false, `no hint for ${state}`);
  }
});
