import { commitAll, makeRepo, writeFile, freshDir, cfgFor } from './helpers.mjs';
import { humanSaysYes } from '../helpers/human-confirm.mjs';
import assert from 'node:assert/strict';
import { readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { startRun, writeSigned, processStartTime } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { newFileState } = await import('../../src/review/fixloop.mjs');
const { saveState } = await import('../../src/worker/review-state.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, blockTickets, enqueue, pendingTickets, queueDir, readResult, writeResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

const FILE = 'src/a.mjs';
const OLD_TIME = new Date('2020-01-01T00:00:00.000Z');
let seq = 0;
const capture = () => ({ text: '', write(s) { this.text += s; } });

async function fixture() {
  const slug = `requeue-${++seq}`;
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 1;\n');
  await commitAll(repo);
  const cfg = cfgFor();
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, config: cfg, writeRow, probe: processStartTime });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'works', tests: ['t'] }], writeRow });
  const key = await loadKey(runId);
  const fromHash = contentHash(repo, FILE);
  const runRootDir = freshDir('runroot');
  const state = { ...newFileState({ file: FILE, level: 'L2' }), status: 'stopped', round: 2, stop_content_hash: fromHash, l3_rung_used: true, next: { action: 'stop', reason: 'l3_patch_exhausted' } };
  await saveState({ runId, block: 'B1', file: FILE, key, state: /** @type {any} */ (state), seq: 0, writeRow: (row) => writeSigned(runId, writeRow, row), runRootDir, rows: [] });
  writeFile(repo, FILE, 'export const a = 2;\n');
  const opts = { repoRoot: repo, run: runId, block: 'B1', file: FILE };
  const old = enqueue({ ...opts, now: OLD_TIME });
  writeResult({ repoRoot: repo, runId, ticket: old.ticket, key, result: { status: 'stopped', stopped: 'l3_patch_exhausted', approved: false, round: 2, findings: [{ id: 'C1', claim: 'old finding' }] } });
  const readRows = () => readAllRows(slug);
  const worker = await createWorker({ runId, repoRoot: repo, cfg, runRootDir, slug, key }, {
    store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow, readRows,
    spawn: async (opts) => {
      const packet = readFileSync(opts.promptPath, 'utf8');
      const hunks = [...packet.matchAll(/^- (@@.*)$/gm)].map((m) => m[1]);
      return { status: 'ok', exit_code: 0, answer: { passed: true, summary: 'clean', reviewed_hunks: hunks, findings: [], resolved: [], needs_file: [] }, usage: { tokens_in: 900, tokens_out: 300 } };
    },
  });
  announceWorker(repo, { pid: process.pid, run: runId });
  const reopen = async (extra = []) => {
    const stdout = capture();
    const stderr = capture();
    const code = await runBlock(['reopen', 'B1', '--run', runId, '--file', FILE, '--reason', 'owner fixed the file', ...extra], humanSaysYes({ stdout, stderr, probe: processStartTime }));
    assert.deepEqual([code, stderr.text], [0, '']);
    return stdout.text;
  };
  const legacyReopen = () => writeSigned(runId, writeRow, { event: 'review.reopened', block: 'B1', file: FILE, by: 'human', from: 'stopped', stop_reason: 'l3_patch_exhausted', at_seq: 1, from_hash: fromHash, to_hash: old.content_hash, reason: 'owner fixed the file' });
  const wait = async () => {
    const stdout = capture();
    const stderr = capture();
    const code = await runReviewFile(['--wait-block', 'B1', '--run', runId, '--max', '0ms'], { cwd: repo, stdout, stderr, pollMs: 1 });
    assert.deepEqual([code, stderr.text], [0, '']);
    return JSON.parse(stdout.text);
  };
  const tickets = () => blockTickets({ repoRoot: repo, run: runId, block: 'B1' });
  return { repo, runId, key, old, opts, worker, reopen, legacyReopen, wait, tickets, readRows };
}

test('B169 clause 1: reopening queues round 1 and prints its ticket, next command and JSON ticket', async () => {
  const f = await fixture();
  rmSync(path.join(queueDir(f.repo), `${f.old.ticket}.json`));
  const text = await f.reopen();
  const ticket = text.match(/^ticket ([0-9a-f]{24})$/m)?.[1];
  assert.ok(ticket);
  assert.notEqual(ticket, f.old.ticket, 'the reopened attempt has a new ticket id');
  assert.deepEqual(pendingTickets(f.repo), [ticket], 'exactly one fresh attempt is pending without a manual enqueue');
  assert.equal(f.tickets().length, 1, 'one ticket for the unchanged current content');
  assert.ok(f.tickets()[0].enqueued_at > OLD_TIME.toISOString(), 'the queued attempt is newer than the stopped result');
  assert.equal(text, `block B1 reopened ${FILE} (from stopped l3_patch_exhausted; by: human)\nticket ${ticket}\nnext: code-forge review-file --wait ${ticket}\n`);
  assert.equal(JSON.parse(await f.reopen(['--json'])).ticket, ticket);
  assert.equal(enqueue(f.opts).ticket, ticket, 'a manual enqueue reuses the reopened attempt');
  assert.equal(await f.worker.drain(), 1, 'the reopen itself queued exactly one review');
  const result = readResult(f.repo, f.runId, ticket);
  assert.deepEqual([result.status, result.round, result.kind, result.approved, result.findings], ['reviewed', 1, 'full', true, []]);
});

test('B169 clause 2: wait-block replaces a pre-reopen done result and waits for fresh round 1', async () => {
  const f = await fixture();
  rmSync(path.join(queueDir(f.repo), `${f.old.ticket}.json`));
  await f.legacyReopen();
  const waiting = await f.wait();
  const ticket = waiting.files[0].ticket;
  assert.notEqual(ticket, f.old.ticket);
  assert.deepEqual(waiting, { block: 'B1', files: [{ file: FILE, ticket, status: 'pending', outcome: null }], done: false, reason: 'wait_timeout' }, 'the old finding must not finish the join');
  assert.deepEqual(pendingTickets(f.repo), [ticket]);
  assert.ok(f.tickets()[0].enqueued_at > OLD_TIME.toISOString(), 'wait-block queues an attempt newer than the stopped result');
  assert.equal(await f.worker.drain(), 1);
  assert.deepEqual(await f.wait(), { block: 'B1', files: [{ file: FILE, ticket, status: 'done', outcome: 'approved' }], done: true });
  const result = readResult(f.repo, f.runId, ticket);
  assert.deepEqual([result.round, result.kind, result.findings], [1, 'full', []]);
});

test('B169 clause 2: wait-block leaves the old done ticket unchanged and joins a new pending ticket', async () => {
  const f = await fixture();
  const oldTicketPath = path.join(queueDir(f.repo), `${f.old.ticket}.json`);
  const oldDonePath = path.join(queueDir(f.repo), `${f.old.ticket}.done`);
  const oldTicket = readFileSync(oldTicketPath, 'utf8');
  const oldDone = readFileSync(oldDonePath, 'utf8');
  await f.legacyReopen();
  const waiting = await f.wait();
  const ticket = waiting.files[0].ticket;
  assert.notEqual(ticket, f.old.ticket, 'unchanged content must get a new attempt');
  assert.deepEqual(waiting, { block: 'B1', files: [{ file: FILE, ticket, status: 'pending', outcome: null }], done: false, reason: 'wait_timeout' }, 'the old finding must not finish the join');
  assert.deepEqual(pendingTickets(f.repo), [ticket]);
  assert.equal(readFileSync(oldTicketPath, 'utf8'), oldTicket, 'the old ticket is preserved byte for byte');
  assert.equal(readFileSync(oldDonePath, 'utf8'), oldDone, 'the old done marker is preserved byte for byte');
  assert.deepEqual(readResult(f.repo, f.runId, f.old.ticket).findings, [{ id: 'C1', claim: 'old finding' }]);
  assert.equal(await f.worker.drain(), 1);
  assert.deepEqual(await f.wait(), { block: 'B1', files: [{ file: FILE, ticket, status: 'done', outcome: 'approved' }], done: true });
  const result = readResult(f.repo, f.runId, ticket);
  assert.deepEqual([result.round, result.kind, result.findings], [1, 'full', []]);
});

test('B169 boundary: repeat reopen clears a cached stop before reusing the existing row and ticket', async () => {
  const f = await fixture();
  const first = JSON.parse(await f.reopen(['--json']));
  // A stale worker result arrives after the row is written, without advancing its anchor.
  writeResult({ repoRoot: f.repo, runId: f.runId, ticket: first.ticket, key: f.key, result: { status: 'stopped', stopped: 'l3_patch_exhausted', approved: false, round: 2, findings: [{ id: 'C1', claim: 'old finding' }] } });
  const repeated = JSON.parse(await f.reopen(['--json']));
  assert.equal(repeated.ticket_cleared, true, 'a repeat reopen must clear the cached stop');
  assert.equal(repeated.ticket, first.ticket, 'the live attempt keeps its identity');
  assert.equal((await f.readRows()).filter((row) => row.event === 'review.reopened').length, 1, 'the existing signed row is reused');
  assert.deepEqual(pendingTickets(f.repo), [first.ticket]);
  assert.deepEqual(await f.wait(), { block: 'B1', files: [{ file: FILE, ticket: first.ticket, status: 'pending', outcome: null }], done: false, reason: 'wait_timeout' }, 'the cached stop must not finish the join');
  assert.equal(await f.worker.drain(), 1);
  assert.deepEqual(await f.wait(), { block: 'B1', files: [{ file: FILE, ticket: first.ticket, status: 'done', outcome: 'approved' }], done: true });
});

test('B169 boundary: repeated joins reuse the live attempt and keep its post-reopen completion', async () => {
  const f = await fixture();
  rmSync(path.join(queueDir(f.repo), `${f.old.ticket}.json`));
  await f.legacyReopen();
  const first = await f.wait();
  assert.deepEqual([first.done, first.files[0].status], [false, 'pending']);
  assert.ok(f.tickets()[0].enqueued_at > OLD_TIME.toISOString(), 'wait-block queues an attempt newer than the stopped result');
  const ticketId = first.files[0].ticket;
  assert.notEqual(ticketId, f.old.ticket);
  const ticket = readFileSync(path.join(queueDir(f.repo), `${ticketId}.json`), 'utf8');
  assert.deepEqual(await f.wait(), first);
  assert.equal(f.tickets().length, 1, 'zero duplicate live tickets');
  assert.equal(readFileSync(path.join(queueDir(f.repo), `${ticketId}.json`), 'utf8'), ticket, 'the live ticket is not rewritten');
  assert.equal(await f.worker.drain(), 1);
  const done = await f.wait();
  assert.deepEqual([done.done, done.files[0].outcome], [true, 'approved']);
  assert.deepEqual(await f.wait(), done);
  assert.deepEqual(pendingTickets(f.repo), [], 'a completed post-reopen review is not queued again');
  assert.equal(await f.worker.drain(), 0);
});

test('B169 boundary: a manual review of new content after a reopen is reused by block waits', async () => {
  const f = await fixture();
  await f.legacyReopen();
  writeFile(f.repo, FILE, 'export const a = 3;\n');
  const reopened = (await f.readRows()).filter((row) => row.event === 'review.reopened').at(-1);
  const queued = enqueue({ ...f.opts, now: new Date(Date.parse(reopened.ts) + 1) });
  const first = await f.wait();
  assert.deepEqual(first.files, [{ file: FILE, ticket: queued.ticket, status: 'pending', outcome: null }]);
  assert.deepEqual(pendingTickets(f.repo), [queued.ticket], 'the join must not duplicate a post-reopen manual review');
  assert.equal(await f.worker.drain(), 1);
  const done = await f.wait();
  assert.deepEqual(done.files, [{ file: FILE, ticket: queued.ticket, status: 'done', outcome: 'approved' }]);
  assert.deepEqual(await f.wait(), done);
  assert.equal(await f.worker.drain(), 0);
});
