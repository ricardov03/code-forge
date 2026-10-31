import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, blockTickets, retractWorker, writeResult } = await import('../../src/worker/queue.mjs');
const probe = async () => 'test-worker';

async function fixture(slug, isolated = false) {
  const repo = freshDir(slug), inputs = freshDir('inputs'), runId = `r-${slug}`;
  await git(['init', '-q', '-b', 'main'], repo);
  const config = { version: 1, project: { slug }, ...cfgFor(), gates: { test: [process.execPath, '-e', 'process.exit(0)'], lint: null, types: null, format: null },
    ...(isolated ? { proof: { isolation: 'worktree', worktree: { dir: freshDir('trees') } } } : {}) };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  for (const name of ['a.mjs', 'extra.mjs']) writeFile(repo, name, 'export const value = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'auto claim', tests: ['auto-claim'] }]));
  writeFile(inputs, 'coder.log', 'safe\n');
  writeFile(inputs, 'report.txt', 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n');
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, probe, config, writeRow });
  const cli = async (args) => {
    let stdout = '', stderr = '';
    const code = await runBlock([...args, '--run', runId], { probe, stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } } });
    return { code, stdout, stderr };
  };
  const open = (id, owned) => cli(['open', id, '--level', 'L1', '--owned', owned, '--acceptance', path.join(inputs, 'acceptance.json')]);
  assert.equal((await open('B1', 'a.mjs')).code, 0);
  const tree = (await readRun(runId)).blocks.B1.worktree?.path ?? repo;
  await writeSigned(runId, writeRow, { event: 'gate.done', block: 'B1', all_ok: true });
  announceWorker(repo, { pid: process.pid, run: runId });
  return { repo, tree, runId, cli, open, writeRow, rows: () => readAllRows(slug),
    close: (args = []) => cli(['close', 'B1', '--transcript', path.join(inputs, 'coder.log'), ...args]),
    land: () => cli(['land', 'B1', '--report', path.join(inputs, 'report.txt'), '--transcript', path.join(inputs, 'coder.log')]),
    cleanup: () => retractWorker(repo, process.pid) };
}

test('auto-claim queues and joins the extra file review before close or land; findings refuse close', async () => {
  for (const mode of ['close', 'land', 'finding']) {
    const f = await fixture(`auto-${mode}`, mode === 'land');
    const key = await loadKey(f.runId), reviewed = [];
    let busy = false, failure;
    const worker = setInterval(async () => {
      if (busy) return;
      busy = true;
      try {
        for (const ticket of blockTickets({ repoRoot: f.repo, run: f.runId, block: 'B1', ...(f.tree === f.repo ? {} : { tree: f.tree }) })) {
          if (reviewed.includes(ticket.ticket)) continue;
          reviewed.push(ticket.ticket);
          assert.equal(ticket.file, 'extra.mjs', 'exactly the extra file is queued');
          assert.equal((await readRun(f.runId)).blocks.B1.owned_files.filter((p) => p === 'extra.mjs').length, 1, 'claim precedes review');
          const findings = mode === 'finding' ? [{ id: 'F1', severity: 'warning', file: 'extra.mjs', claim: 'extra finding', line_start: 1, line_end: 1, fix: 'fix it' }] : [];
          await writeSigned(f.runId, f.writeRow, { event: findings.length ? 'review.result' : 'review.approved', block: 'B1', file: ticket.file, ticket: ticket.ticket, content_hash: contentHash(f.tree, ticket.file), approved: !findings.length, status: 'reviewed', open: findings });
          writeResult({ repoRoot: f.repo, runId: f.runId, ticket: ticket.ticket, key, result: { status: 'reviewed', approved: !findings.length, findings } });
        }
      } catch (err) { failure = err; }
      finally { busy = false; }
    }, 20);
    try {
      writeFile(f.tree, 'extra.mjs', 'export const value = 2;\n');
      const result = mode === 'land' ? await f.land() : await f.close(['--auto-claim']);
      assert.equal(result.code, mode === 'finding' ? 1 : 0, result.stderr);
      if (failure) throw failure;
      const rows = await f.rows(), claims = rows.filter((r) => r.event === 'block.claim');
      assert.equal(claims.length, 1, 'exactly one signed auto claim');
      assert.deepEqual([claims[0].path, claims[0].auto, claims[0].reason], ['extra.mjs', true, 'auto-claim: changed outside owned files']);
      assert.deepEqual(verifyRow(claims[0], key), { ok: true });
      assert.equal(reviewed.length, 1, 'exactly one review completes');
      assert.equal(result.stdout.split('\n').filter((s) => s.includes('auto-claimed extra.mjs')).length, 1, 'one auto-claim line');
      assert.equal(rows.filter((r) => r.event === 'block.close').length, mode === 'finding' ? 0 : 1, 'only approved review permits one close');
      assert.equal((await readRun(f.runId)).blocks.B1.status, mode === 'finding' ? 'open' : 'closed');
      if (mode === 'finding') assert.match(result.stderr, /F1/);
    } finally { clearInterval(worker); f.cleanup(); }
  }
});

test('auto-claim refuses another open owner, including its claims and separate worktree; plain close still refuses unowned files', async () => {
  for (const mode of ['owner', 'claim', 'worktree', 'plain']) {
    const f = await fixture(`auto-boundary-${mode}`, mode === 'worktree');
    try {
      if (mode !== 'plain') {
        assert.equal((await f.open('B2', mode === 'claim' ? 'other.mjs' : 'extra.mjs')).code, 0);
        if (mode === 'claim') assert.equal((await f.cli(['claim', 'B2', 'extra.mjs'])).code, 0);
      }
      writeFile(f.tree, 'extra.mjs', 'export const value = 3;\n');
      const before = (await f.rows()).filter((r) => r.event === 'block.claim').length;
      const result = await f.close(mode === 'plain' ? [] : ['--auto-claim']);
      assert.equal(result.code, 1, result.stderr);
      assert.match(result.stderr, /unowned_change.*extra\.mjs/s);
      if (mode !== 'plain') assert.match(result.stderr, /B2/);
      assert.equal((await f.rows()).filter((r) => r.event === 'block.claim').length, before, 'zero new claims after refusal');
      assert.equal(blockTickets({ repoRoot: f.repo, run: f.runId, block: 'B1' }).length, 0, 'zero reviews queued for refused path');
      assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
    } finally { f.cleanup(); }
  }
});
