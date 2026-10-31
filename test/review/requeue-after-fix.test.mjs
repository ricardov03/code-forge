import { answerFor, commitAll, makeRepo, writeFile, freshDir, cfgFor, git } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { startRun, readRun, saveRun, writeSigned, processStartTime } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { newFileState } = await import('../../src/review/fixloop.mjs');
const { saveState } = await import('../../src/worker/review-state.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, blockTickets, enqueue, pendingTickets, readResult, retractWorker, writeResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

const FILE = 'src/a.mjs';
const REASON = 'auto-requeue: content changed after stopped review';
let seq = 0;
const capture = () => ({ text: '', write(s) { this.text += s; } });

async function fixture({ changed = true, waived = false, otherOwner = false, worktree = false } = {}) {
  const slug = `after-fix-${++seq}`;
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, FILE, 'export const a = 0;\n');
  await commitAll(repo);
  const cfg = cfgFor();
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, config: cfg, writeRow, probe: processStartTime });
  await openBlock({ runId, id: 'B1', level: 'L1', owned: [FILE], acceptance: [{ clause: 'works', tests: ['t'] }], writeRow });
  const tree = worktree ? freshDir('tree') : repo;
  if (worktree) {
    const branch = `fix-${seq}`;
    await git(['worktree', 'add', '-b', branch, tree], repo);
    const record = await readRun(runId);
    record.blocks.B1.worktree = { path: tree, branch, base_sha: record.blocks.B1.base_sha };
    await saveRun(record);
  }
  const where = worktree ? { tree } : {};
  const previousHash = contentHash(tree, FILE);
  writeFile(tree, FILE, 'export const a = 1;\n');
  const key = await loadKey(runId);
  const hash = contentHash(tree, FILE);
  const finding = { id: 'F1', file: FILE, severity: 'warning', line_start: 1, line_end: 1, claim: 'incorrect value', fix: 'use 2' };
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', file: FILE, ...row });
  if (waived) {
    await sign({ event: 'review.cap', round: 1, content_hash: previousHash, reason: 'review_cap', open: ['F0'] });
    await sign({ event: 'review.reopened', from: 'stopped', from_hash: previousHash, to_hash: hash, at_seq: 0, by: 'auto', reason: REASON });
  }
  await sign({ event: 'review.round', round: 2, content_hash: hash, open_after: 1, open: [finding] });
  await sign({ event: 'review.cap', round: 2, content_hash: hash, reason: 'review_cap', open: ['F1'] });
  const state = { ...newFileState({ file: FILE, level: 'L1' }), status: 'stopped', round: 2, open: [finding], stop_content_hash: hash, next: { action: 'stop', reason: 'review_cap' } };
  const runRootDir = freshDir('runroot');
  await saveState({ runId, block: 'B1', file: FILE, key, state: /** @type {any} */ (state), seq: 0, writeRow: (row) => writeSigned(runId, writeRow, row), runRootDir, rows: await readAllRows(slug) });
  const old = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE, ...where });
  writeResult({ repoRoot: repo, runId, ticket: old.ticket, key, result: { status: 'stopped', stopped: 'review_cap', approved: false, round: 2, findings: [finding] } });
  if (waived) await sign({ event: 'review.waived', finding: 'F1', by: 'human', reason: 'accepted warning' });
  if (changed) writeFile(tree, FILE, 'export const a = 2;\n');
  if (otherOwner) {
    const record = await readRun(runId);
    record.blocks.B2 = { ...record.blocks.B1, owned_files: [FILE], status: 'open' };
    await saveRun(record);
  }
  let sessions = 0;
  const worker = await createWorker({ runId, repoRoot: repo, cfg, runRootDir, slug, key }, {
    store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow, readRows: () => readAllRows(slug),
    spawn: async (opts) => {
      sessions++;
      const hunks = [...readFileSync(opts.promptPath, 'utf8').matchAll(/^- (@@.*)$/gm)].map((m) => m[1]);
      return { status: 'ok', exit_code: 0, answer: answerFor(hunks), usage: { tokens_in: 900, tokens_out: 300 } };
    },
  });
  announceWorker(repo, { pid: process.pid, run: runId });
  const join = async (wait = false) => {
    const stdout = capture(), stderr = capture();
    const code = await runReviewFile(['--wait-block', 'B1', '--run', runId, '--max', wait ? '5s' : '0ms'], {
      cwd: tree, stdout, stderr, pollMs: 1, sleep: async () => { await worker.drain(); },
    });
    return { code, result: JSON.parse(stdout.text), lines: stderr.text.trim().split('\n').filter(Boolean) };
  };
  const transcript = path.join(freshDir('inputs'), 'coder.log');
  writeFile(path.dirname(transcript), 'coder.log', '$ node --test\n');
  const close = async () => {
    const stdout = capture(), stderr = capture();
    const code = await runBlock(['close', 'B1', '--run', runId, '--transcript', transcript], { stdout, stderr, probe: processStartTime });
    return { code, stdout: stdout.text, stderr: stderr.text };
  };
  return { repo, tree, runId, key, old, worker, join, close, sessions: () => sessions,
    rows: () => readAllRows(slug), tickets: () => blockTickets({ repoRoot: repo, run: runId, block: 'B1', ...where }),
    cleanup: () => retractWorker(repo, process.pid) };
}

test('changed stopped content: join queues one signed restart, waits for approval, then closes', async () => {
  for (const worktree of [false, true]) {
    const f = await fixture({ worktree });
    try {
      const joined = await f.join(true);
      assert.deepEqual([joined.code, joined.result.done, joined.result.files.map((file) => [file.file, file.status, file.outcome])],
        [0, true, [[FILE, 'done', 'approved']]], 'one changed stopped file must finish with a fresh approval');
      const ticket = joined.result.files[0].ticket;
      assert.notEqual(ticket, f.old.ticket, 'the old findings ticket must be replaced');
      assert.equal(joined.lines.length, 1, 'exactly one auto-requeue line per changed stopped file');
      assert.match(joined.lines[0], /auto-requeue.*src\/a\.mjs/);
      const rows = (await f.rows()).filter((row) => row.event === 'review.reopened');
      assert.deepEqual(rows.map((row) => [row.reason, row.from_hash, row.to_hash, verifyRow(row, f.key).ok]),
        [[REASON, f.old.content_hash, contentHash(f.tree, FILE), true]], 'one signed restart records the old and current content');
      assert.deepEqual([f.tickets().length, f.sessions(), readResult(f.repo, f.runId, ticket).round], [2, 1, 1], 'one fresh ticket starts a full review at round 1');
      assert.equal((await f.join()).lines.length, 0, 'a repeated join prints no duplicate restart');
      const closed = await f.close();
      assert.deepEqual([closed.code, closed.stderr, (await readRun(f.runId)).blocks.B1.status], [0, '', 'closed']);
      if (worktree) assert.equal(readFileSync(path.join(f.repo, FILE), 'utf8'), 'export const a = 0;\n', 'the main tree stays unchanged while the block tree is reviewed');
    } finally { f.cleanup(); }
  }
});

test('unchanged stopped content retains its findings and queues zero reviews', async () => {
  const f = await fixture({ changed: false });
  try {
    const joined = await f.join();
    assert.deepEqual([joined.code, joined.result.files.map((file) => [file.ticket, file.outcome]), joined.lines], [0, [[f.old.ticket, 'stopped']], []]);
    assert.deepEqual([f.tickets().length, pendingTickets(f.repo).length, await f.worker.drain(), f.sessions()], [1, 0, 0, 0]);
    assert.equal(readResult(f.repo, f.runId, f.old.ticket).findings.length, 1, 'the original finding remains visible');
    assert.equal((await f.close()).code, 1, 'unchanged findings still refuse close');
    assert.equal((await f.rows()).filter((row) => row.event === 'review.reopened').length, 0);
  } finally { f.cleanup(); }
});

test('direct close requeues changed stopped content and waits for the fake reviewer', async () => {
  const f = await fixture();
  const serving = f.worker.run();
  try {
    const closed = await f.close();
    assert.deepEqual([closed.code, (await readRun(f.runId)).blocks.B1.status, f.sessions()], [0, 'closed', 1],
      'direct close must wait for exactly one fresh approval instead of refusing unreviewed content');
    assert.equal(closed.stderr.trim().split('\n').length, 1, 'direct close prints one requeue line');
    assert.match(closed.stderr, /auto-requeue.*src\/a\.mjs/);
    assert.deepEqual([f.tickets().length, (await f.rows()).filter((row) => row.event === 'review.reopened').length], [2, 1]);
  } finally { f.worker.stop(); await serving; f.cleanup(); }
});

test('waived stopped findings on changed content queue zero reviews', async () => {
  const f = await fixture({ waived: true });
  try {
    const joined = await f.join();
    assert.deepEqual([joined.code, joined.result.files.map((file) => file.ticket), joined.lines], [0, [f.old.ticket], []]);
    assert.deepEqual([f.tickets().length, pendingTickets(f.repo).length, await f.worker.drain(), f.sessions()], [1, 0, 0, 0]);
    assert.equal((await f.rows()).filter((row) => row.event === 'review.reopened').length, 1, 'a covering waiver prevents another restart after the earlier automatic review');
  } finally { f.cleanup(); }
});

test('another open block owning changed stopped content prevents auto-requeue', async () => {
  const f = await fixture({ otherOwner: true });
  try {
    const joined = await f.join();
    assert.deepEqual([joined.code, joined.result.files.map((file) => file.ticket), joined.lines], [0, [f.old.ticket], []]);
    assert.deepEqual([f.tickets().length, pendingTickets(f.repo).length, await f.worker.drain(), f.sessions()], [1, 0, 0, 0]);
    assert.equal((await f.rows()).filter((row) => row.event === 'review.reopened').length, 0, 'foreign ownership prevents a signed restart');
  } finally { f.cleanup(); }
});
