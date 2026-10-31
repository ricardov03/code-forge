import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const probe = async () => 'test-worker';

async function fixture(slug) {
  const repo = freshDir(slug), inputs = freshDir('inputs'), runId = `r-${slug}`;
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, '.code-forge.yml', JSON.stringify({ version: 1, ...cfgFor() }));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, 'a.mjs', 'old\n');
  writeFile(repo, 'extra.mjs', 'old\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'keep claims', tests: ['claims-reopen'] }]));
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, probe, config: cfgFor(), writeRow });
  const cli = async (args) => {
    let stdout = '', stderr = '';
    const code = await runBlock([...args, '--run', runId], { probe, stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } } });
    return { code, stdout, stderr };
  };
  const open = (id, owned, attempt = '1') => cli(['open', id, '--level', 'L1', '--owned', owned, '--acceptance', path.join(inputs, 'acceptance.json'), '--attempt', attempt]);
  assert.equal((await open('B1', 'a.mjs')).code, 0);
  assert.equal((await cli(['claim', 'B1', 'extra.mjs'])).code, 0);
  return { repo, runId, cli, open, writeRow, rows: () => readAllRows(slug) };
}

test('reopening attempt 2 and advancing an attempt retain one signed claim and close without claiming again', async () => {
  for (const mode of ['open', 'attempt']) {
    const f = await fixture(`claims-${mode}`);
    let result;
    if (mode === 'open') {
      assert.equal((await f.cli(['stop', 'B1', '--reason', 'worker down'])).code, 0);
      result = await f.open('B1', 'a.mjs', '2');
    } else result = await f.cli(['attempt', 'B1']);
    assert.equal(result.code, 0, result.stderr);
    assert.equal(result.stdout.split('\n').filter((s) => s === 'kept claims: 1').length, 1, 'one kept-claims count');
    assert.deepEqual((await readRun(f.runId)).blocks.B1.owned_files, ['a.mjs', 'extra.mjs']);
    writeFile(f.repo, 'extra.mjs', 'changed after retry\n');
    await writeSigned(f.runId, f.writeRow, { event: 'review.approved', block: 'B1', file: 'extra.mjs', content_hash: contentHash(f.repo, 'extra.mjs') });
    const closed = await f.cli(['close', 'B1']);
    assert.equal(closed.code, 0, closed.stderr);
    assert.equal((await f.rows()).filter((r) => r.event === 'block.claim').length, 1, 'no replacement claim is written');
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
  }
});

test('a retained claim conflicting with a new open owner refuses retry without overwriting the stopped block', async () => {
  const f = await fixture('claims-overlap');
  assert.equal((await f.cli(['stop', 'B1', '--reason', 'retry'])).code, 0);
  assert.equal((await f.open('B2', 'extra.mjs')).code, 0);
  const result = await f.open('B1', 'a.mjs', '2');
  assert.equal(result.code, 1, result.stderr);
  assert.match(result.stderr, /extra\.mjs.*B2/);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'stopped');
  assert.equal((await f.rows()).filter((r) => r.event === 'dispatch' && r.block === 'B1').length, 1, 'no new dispatch after conflict');
});
