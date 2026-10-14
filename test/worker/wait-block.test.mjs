import { BLOCK, makeRepo, startWorker, stopWorker, cli, queued, VALID_REVIEW, freshDir } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import path from 'node:path';

const { announceWorker, retractWorker, enqueue, queueDir, readResult, writeResult, enqueueAnswers } = await import('../../src/worker/queue.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { validateConfig, reviewFileLoop } = await import('../../src/config/validate.mjs');
const { loadConfigFile } = await import('../../src/config/load.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { readRun, saveRun } = await import('../../src/state/run.mjs');
const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');

/** @param {string} repo @param {string} runId @param {string[]} [extra] */
async function waitBlock(repo, runId, extra = []) {
  let out = '';
  let err = '';
  const code = await runReviewFile(['--wait-block', BLOCK, '--run', runId, ...extra], {
    cwd: repo, pollMs: 1, stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } },
  });
  assert.equal(err, '');
  assert.equal(out.trim().split('\n').length, 1);
  return { code, json: JSON.parse(out) };
}

test('(1) reproduction: three files enqueue without waiting, then one block wait joins all three', async () => {
  const { repo, runId } = await makeRepo();
  const worker = await startWorker(repo, runId, { FAKE_SLEEP_MS: '500', FAKE_ANSWER: VALID_REVIEW });
  try {
    const tickets = [];
    for (const file of ['src/a.mjs', 'src/b.mjs', 'src/c.mjs']) {
      const res = await cli(['review-file', file, '--block', BLOCK, '--run', runId], repo);
      assert.equal(res.code, 0);
      assert.equal(res.json.status, 'queued');
      tickets.push({ file, ticket: res.json.ticket, status: 'done' });
    }
    assert.equal(queued(repo, 'json').length, 3);
    const res = await cli(['review-file', '--wait-block', BLOCK, '--run', runId, '--max', '20s'], repo);
    assert.equal(res.code, 0, res.stderr);
    assert.equal(res.stdout.trim().split('\n').length, 1);
    assert.deepEqual(res.json, { block: BLOCK, files: tickets, done: true });
    assert.equal(queued(repo, 'done').length, 3);
  } finally {
    await stopWorker(worker, runId);
  }
});

test('(2) changed content is enqueued with the normal id; the worker finishes the old ticket stale', async () => {
  const { repo, runId } = await makeRepo();
  const old = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' });
  writeFileSync(path.join(repo, 'src/a.mjs'), 'export const a = 2;\n');
  for (const file of ['src/b.mjs', 'src/c.mjs']) enqueue({ repoRoot: repo, run: runId, block: BLOCK, file });
  // Stand in for a live worker until the wait has re-queued the new content. This makes the
  // ordering deterministic: no reviewer can start the old content before the edit.
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const pending = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.equal(pending.json.files.length, 3);
    assert.equal(pending.json.done, false);
    assert.notEqual(pending.json.files[0].ticket, old.ticket);
  } finally { retractWorker(repo, process.pid); }
  const worker = await startWorker(repo, runId, { FAKE_ANSWER: VALID_REVIEW });
  try {
    const res = await waitBlock(repo, runId, ['--max', '20s']);
    const normal = await cli(['review-file', 'src/a.mjs', '--block', BLOCK, '--run', runId], repo);
    assert.deepEqual([res.code, res.json.done, res.json.files.length], [0, true, 3]);
    assert.deepEqual(res.json.files[0], { file: 'src/a.mjs', ticket: normal.json.ticket, status: 'done' });
    assert.equal(readResult(repo, runId, old.ticket).status, 'stale');
    assert.deepEqual([queued(repo, 'json').length, queued(repo, 'done').length], [4, 4]);
  } finally { await stopWorker(worker, runId); }
});

test('(3) timeout lists exactly the pending files, preserving completed tickets', async () => {
  const { repo, runId } = await makeRepo();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const tickets = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => enqueue({ repoRoot: repo, run: runId, block: BLOCK, file }));
    writeResult({ repoRoot: repo, runId, ticket: tickets[0].ticket, key: await loadKey(runId), result: { status: 'reviewed', approved: false } });
    const res = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual(res, { code: 0, json: { block: BLOCK, files: tickets.map((t, i) => ({ file: t.file, ticket: t.ticket, status: i === 0 ? 'done' : 'pending' })), done: false, reason: 'wait_timeout' } });
    assert.deepEqual([queued(repo, 'json').length, queued(repo, 'done').length], [3, 1]);
  } finally { retractWorker(repo, process.pid); }
});

test('(4) missing own review is not_queued; other blocks, runs, trees and answers never count', async () => {
  const { repo, runId } = await makeRepo();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    enqueue({ repoRoot: repo, run: 'r-other', block: BLOCK, file: 'src/a.mjs' });
    enqueue({ repoRoot: repo, run: runId, block: 'Bother', file: 'src/a.mjs' });
    enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs', tree: repo });
    enqueueAnswers({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs', byFile: 'src/b.mjs', answers: ['F1'] });
    const res = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual(res, { code: 0, json: { block: BLOCK, files: ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => ({ file, ticket: null, status: 'not_queued' })), done: false, reason: 'not_queued' } });
    assert.equal(queued(repo, 'json').length, 4);
  } finally { retractWorker(repo, process.pid); }
});

test('(5) absent or wrong-run worker returns worker_down, even with all markers finished', async () => {
  const { repo, runId } = await makeRepo();
  const tickets = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => enqueue({ repoRoot: repo, run: runId, block: BLOCK, file }));
  const key = await loadKey(runId);
  for (const t of tickets) writeResult({ repoRoot: repo, runId, ticket: t.ticket, key, result: { status: 'reviewed', approved: true } });
  const absent = await waitBlock(repo, runId, ['--max', '0ms']);
  announceWorker(repo, { pid: process.pid, run: 'r-other' });
  try {
    const wrong = await waitBlock(repo, runId, ['--max', '0ms']);
    const expected = { code: 3, json: { block: BLOCK, files: tickets.map((t) => ({ file: t.file, ticket: t.ticket, status: 'done' })), done: false, status: 'worker_down' } };
    assert.deepEqual([absent, wrong], [expected, expected]);
    assert.equal(queued(repo, 'json').length, 3);
  } finally { retractWorker(repo, process.pid); }
});

test('(6) file_loop accepts wait and overlap, defaults to wait, and rejects every other type/value', async () => {
  const { repo } = await makeRepo();
  const loaded = await loadConfigFile(path.join(repo, '.code-forge.yml'));
  const cfg = loaded.config;
  assert.deepEqual([validateConfig(cfg).errors, reviewFileLoop(cfg)], [[], 'wait']);
  const accepted = [];
  for (const mode of ['wait', 'overlap']) {
    cfg.review.file_loop = mode;
    accepted.push([validateConfig(cfg).errors, reviewFileLoop(cfg)]);
  }
  assert.deepEqual(accepted, [[[], 'wait'], [[], 'overlap']]);
  const rejected = [];
  for (const mode of ['watch', 'other', '', null, 1, true, [], {}]) {
    cfg.review.file_loop = mode;
    const result = validateConfig(cfg);
    rejected.push([result.valid, result.errors.map((e) => [e.rule, e.path])]);
  }
  assert.deepEqual(rejected, [1, 1, 1, 2, 2, 2, 2, 2].map((count) => [false, Array.from({ length: count }, () => ['schema', '/review/file_loop'])]));
});

test('unreadable scope or corrupt tickets fail closed; no files are silently skipped', async () => {
  const { repo, runId } = await makeRepo();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const mirror = path.join(repo, '.code-forge', 'runs', `${runId}.json`);
    const original = readFileSync(mirror);
    rmSync(mirror);
    const missing = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual([missing.code, missing.json.status, missing.json.reason, queued(repo, 'json').length], [1, 'refused', 'block-scope-unreadable', 0]);
    writeFileSync(mirror, original);
    writeFileSync(path.join(queueDir(repo), '0123456789abcdef01234567.json'), '{}');
    const corrupt = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual([corrupt.code, corrupt.json.status, corrupt.json.reason], [1, 'refused', 'bad-path']);
    assert.equal(queued(repo, 'json').length, 1);
    rmSync(path.join(queueDir(repo), '0123456789abcdef01234567.json'));
    const t = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' });
    writeFileSync(path.join(queueDir(repo), `${t.ticket}.done`), '{}');
    const marker = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual([marker.code, marker.json.status, marker.json.reason], [1, 'refused', 'result-unreadable']);
  } finally { retractWorker(repo, process.pid); }
});

test('worktree scope and ticket IDs use only that tree, sharing the main queue', async () => {
  const { repo, runId } = await makeRepo();
  const tree = path.join(freshDir('trees'), 'block');
  const added = await exec(['git', 'worktree', 'add', '-b', 'fake-block', tree], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
  assert.equal(added.code, 0, added.stderr);
  mkdirSync(path.join(tree, 'src'));
  writeFileSync(path.join(tree, 'src/a.mjs'), 'export const a = 9;\n');
  const record = await readRun(runId);
  record.blocks[BLOCK].worktree = { path: tree, branch: 'fake-block', base_sha: record.blocks[BLOCK].base_sha };
  await saveRun(record);
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const main = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' });
    const normal = await cli(['review-file', 'src/a.mjs', '--block', BLOCK, '--run', runId], tree);
    assert.deepEqual([normal.code, normal.json.tree, queued(repo, 'json').length], [0, tree, 2]);
    assert.notEqual(normal.json.ticket, main.ticket);
    writeResult({ repoRoot: repo, runId, ticket: normal.json.ticket, key: await loadKey(runId), result: { status: 'reviewed', approved: true } });
    const res = await waitBlock(tree, runId, ['--max', '0ms']);
    assert.deepEqual(res, { code: 0, json: { block: BLOCK, files: [{ file: 'src/a.mjs', ticket: normal.json.ticket, status: 'done' }], done: true } });
    assert.deepEqual([queued(repo, 'json').length, queued(repo, 'done').length], [2, 1]);
  } finally { retractWorker(repo, process.pid); }
});
