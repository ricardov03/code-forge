import { cli, makeRepo, queued } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { announceWorker, enqueue, retractWorker, verifyResult, writeResult, reviewsDir } = await import('../../src/worker/queue.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { contentHash, normalizeRequestPath } = await import('../../src/worker/ticket.mjs');

describe('review-file enqueue (V4 paths, timing, worker_down)', () => {
  test('enqueue from the repo root returns {ticket, status: queued} in < 1 s', async () => {
    const { repo, runId } = await makeRepo();
    announceWorker(repo, { pid: process.pid, run: runId }); // this live process stands in for the worker
    try {
      const res = await cli(['review-file', 'src/a.mjs', '--block', 'B11'], repo);
      assert.equal(res.code, 0);
      assert.equal(res.json.status, 'queued');
      assert.match(res.json.ticket, /^[0-9a-f]{24}$/);
      assert.ok(res.ms < 1000, `enqueue took ${res.ms} ms`);
      assert.deepEqual(queued(repo, 'json'), [res.json.ticket]);
    } finally {
      retractWorker(repo, process.pid);
    }
  });

  test('review-file from a sub-directory writes file relative to the repo root', async () => {
    const { repo, runId } = await makeRepo();
    announceWorker(repo, { pid: process.pid, run: runId });
    try {
      const res = await cli(['review-file', 'a.mjs', '--block', 'B11'], path.join(repo, 'src'));
      assert.equal(res.code, 0);
      assert.equal(res.json.file, 'src/a.mjs');
      const ticket = JSON.parse(readFileSync(path.join(repo, '.code-forge', 'queue', `${res.json.ticket}.json`), 'utf8'));
      assert.equal(ticket.file, 'src/a.mjs');
      assert.equal(ticket.run, runId);
      assert.equal(ticket.block, 'B11');
    } finally {
      retractWorker(repo, process.pid);
    }
  });

  test('../x (an existing file outside the repo) and an absolute path are refused with bad-path; nothing is enqueued', async () => {
    const { repo, runId } = await makeRepo();
    writeFileSync(path.join(repo, '..', 'x'), 'outside\n');
    announceWorker(repo, { pid: process.pid, run: runId });
    try {
      const up = await cli(['review-file', '../x', '--block', 'B11'], repo);
      assert.equal(up.code, 1);
      assert.deepEqual([up.json.status, up.json.reason], ['refused', 'bad-path']);
      const abs = await cli(['review-file', path.join(repo, 'src', 'a.mjs'), '--block', 'B11'], repo);
      assert.equal(abs.code, 1);
      assert.equal(abs.json.reason, 'bad-path');
      // any `..` segment is refused, even one that resolves back inside the repo
      const back = await cli(['review-file', '../src/a.mjs', '--block', 'B11'], path.join(repo, 'src'));
      assert.deepEqual([back.code, back.json.reason], [1, 'bad-path']);
      assert.equal(queued(repo, 'json').length, 0);
    } finally {
      retractWorker(repo, process.pid);
    }
  });

  test('a symlinked file pointing outside the repo is refused with bad-path', async () => {
    const { repo, runId } = await makeRepo();
    const outside = path.join(repo, '..', 'outside-secret.txt');
    writeFileSync(outside, 'outside\n');
    symlinkSync(outside, path.join(repo, 'src', 'link.mjs'));
    announceWorker(repo, { pid: process.pid, run: runId });
    try {
      const res = await cli(['review-file', 'src/link.mjs', '--block', 'B11'], repo);
      assert.deepEqual([res.code, res.json.status, res.json.reason], [1, 'refused', 'bad-path']);
      assert.throws(() => contentHash(repo, 'src/link.mjs'), { code: 'bad-path' });
      assert.equal(queued(repo, 'json').length, 0);
    } finally {
      retractWorker(repo, process.pid);
    }
  });

  test('.GIT/config and .Code-Forge/x are refused with bad-path (case-insensitive)', async () => {
    const { repo } = await makeRepo();
    assert.throws(() => normalizeRequestPath('.GIT/config', repo, repo), { code: 'bad-path' });
    assert.throws(() => normalizeRequestPath('.Code-Forge/x', repo, repo), { code: 'bad-path' });
  });

  test('worker_down: no announced worker, or a dead one, is never approval and enqueues nothing', async () => {
    const { repo, runId } = await makeRepo();
    const none = await cli(['review-file', 'src/a.mjs', '--block', 'B11'], repo);
    assert.equal(none.code, 3);
    assert.equal(none.json.status, 'worker_down');
    announceWorker(repo, { pid: 2 ** 22 + 12345, run: runId }); // no such process
    const dead = await cli(['review-file', 'src/a.mjs', '--block', 'B11'], repo);
    assert.equal(dead.code, 3);
    assert.equal(dead.json.status, 'worker_down');
    assert.equal(queued(repo, 'json').length, 0);
  });

  test('tickets are idempotent by content hash: same content = same ticket, an edit = a new one', async () => {
    const { repo, runId } = await makeRepo();
    const first = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    const again = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    assert.equal(again.ticket, first.ticket);
    writeFileSync(path.join(repo, 'src', 'a.mjs'), 'export const a = 2;\n');
    const edited = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    assert.notEqual(edited.ticket, first.ticket);
    assert.equal(queued(repo, 'json').length, 2);
  });
});

describe('signed results (§8.6)', () => {
  test('a result signed with the run key verifies', async () => {
    const { repo, runId } = await makeRepo();
    const { ticket, content_hash } = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/b.mjs' });
    writeResult({ repoRoot: repo, runId, ticket, key: await loadKey(runId), result: { event: 'review.result', block: 'B11', file: 'src/b.mjs', content_hash, status: 'reviewed', approved: false } });
    const check = await verifyResult(repo, runId, ticket);
    assert.equal(check.ok, true);
    assert.equal(check.result.file, 'src/b.mjs');
  });

  test('a byte-flipped result is refused (mismatch)', async () => {
    const { repo, runId } = await makeRepo();
    const { ticket, content_hash } = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/b.mjs' });
    writeResult({ repoRoot: repo, runId, ticket, key: await loadKey(runId), result: { event: 'review.result', block: 'B11', file: 'src/b.mjs', content_hash, status: 'reviewed', approved: false } });
    const file = path.join(reviewsDir(repo, runId), `${ticket}.json`);
    const bytes = readFileSync(file);
    const at = bytes.indexOf(Buffer.from(content_hash)) + 3; // one hex char inside the hash
    bytes[at] = bytes[at] === 0x30 ? 0x31 : 0x30;
    writeFileSync(file, bytes);
    assert.deepEqual(await verifyResult(repo, runId, ticket), { ok: false, reason: 'mismatch' });
  });
});
