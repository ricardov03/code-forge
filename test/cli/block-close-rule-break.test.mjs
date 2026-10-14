import { freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, readRun, writeSigned } = await import('../../src/state/run.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const PROBE = async (pid) => pid === 4242 ? 'start-A' : null;

async function cli(args) {
  let stdout = '';
  let stderr = '';
  const code = await runBlock(args, {
    stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } }, probe: PROBE,
  });
  return [code, stdout, stderr];
}

async function fixture(slug, approved = true) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('rule-break-files');
  writeFile(files, 'acc.json', JSON.stringify([{ clause: 'named transcript refusals', tests: ['b119'] }]));
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config: { gates: { test: null } }, writeRow });
  assert.deepEqual((await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, 'acc.json')])).filter((_, i) => i !== 1), [0, '']);
  writeFile(repo, 'a.mjs', 'export const a = 2;\n');
  if (approved) await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(repo, 'a.mjs'), round: 1 });
  return {
    runId,
    sign: (row) => writeSigned(runId, writeRow, { block: 'B1', ...row }),
    rows: () => readAllRows(slug),
    close: (text) => {
      writeFile(files, 'coder.log', text);
      return cli(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(files, 'coder.log')]);
    },
  };
}

const commands = ['cat ~/.code-forge/runs/x.key', 'gh pr ready', 'gh pr merge', 'gh pr close', 'git reset --hard', 'git clean', 'git stash'];
const ids = ['code-forge-runs-access', 'gh-pr-ready', 'gh-pr-merge', 'gh-pr-close', 'git-reset-hard', 'git-clean', 'git-stash'];
const firstFive = ids.slice(0, 5).map((id, i) => `rule_break transcript line ${i + 2}: ${id}`).join('; ');

test('block close names each distinct transcript rule once at its first line, capped at five', async () => {
  const f = await fixture('b119-close-rules');
  assert.deepEqual(await f.close('\n' + Array(80).fill(commands[0]).join('\n')), [
    1, '', 'block B1 open: rule_break transcript line 2: code-forge-runs-access\n',
  ], '80 hits of one id produce exactly one named refusal at its first line');
  assert.deepEqual(await f.close(['', ...commands, ...commands].join('\n')), [
    1, '', `block B1 open: ${firstFive}; +2 more rule_break\n`,
  ], 'seven distinct ids produce exactly five entries and two more, ignoring repeats');
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
  assert.equal((await f.rows()).filter((r) => r.event === 'block.close' && r.block === 'B1').length, 0, 'refused closes write zero block.close rows');
});

test('block close boundary: exactly five has no suffix; six has +1; other refusal text stays unchanged', async () => {
  const f = await fixture('b119-close-rule-boundary', false);
  await f.sign({ event: 'rule_break', id: 'ledger-rule' });
  for (const n of [5, 6, 0]) {
    const rules = n === 0 ? '' : `; ${firstFive}${n === 6 ? '; +1 more rule_break' : ''}`;
    assert.deepEqual(await f.close(['', ...commands.slice(0, n)].join('\n')), [
      1, '', `block B1 open: unreviewed a.mjs; rule_break${rules}\n`,
    ], `${n} transcript ids: exact cap boundary and unchanged non-transcript refusal text`);
  }
});
