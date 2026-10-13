// Import first: the helper pins HOME under its per-file parent before src loads.
import { answerFor, cfgFor, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { freshVerdict, runReviewFile } = await import('../../src/cli/review-file.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { announceWorker, dropDoneIf, enqueue, pendingTickets, queueDir, readResult, retractWorker } = await import('../../src/worker/queue.mjs');
const { startRun, reloadRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

const FILE = 'src/sample.mjs';
const BLOCK = 'B1';
let sequence = 0;

/** @param {string} repo @param {string[]} [flags] @param {{dropDone?: typeof dropDoneIf}} [extra] */
async function request(repo, flags = [], extra = {}) {
  let out = '';
  let err = '';
  const code = await runReviewFile([FILE, '--block', BLOCK, ...flags], {
    cwd: repo,
    stdout: { write: (s) => { out += s; return true; } },
    stderr: { write: (s) => { err += s; return true; } },
    ...extra,
  });
  assert.equal(err, '');
  assert.equal(out.trim().split('\n').length, 1);
  return { code, json: JSON.parse(out) };
}

/** A risk-0 diff over 6000 tokens. Both budgets are small so B109's step-up cannot fit. */
function largeContent() {
  return Array.from({ length: 57 }, (_, i) => `export const value${i} = '${'x'.repeat(475)}';`).join('\n') + '\n';
}

/** @param {boolean} [small] @param {Array<Record<string, any>>} [findings] */
async function fixture(small = false, findings = []) {
  const repo = await makeRepo();
  writeFile(repo, FILE, small ? 'export const value = 1;\n' : largeContent());
  const config = cfgFor({ budgets: { quick_in: 6000, full_in: 6000 }, session_timeout_s: 2 });
  const runId = `r-fresh-split-${++sequence}`;
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const writeRow = async (/** @type {Record<string, any>} */ row) => void rows.push(row);
  await startRun({ workspace: repo, project: 'fresh-split-test', runId, config, writeRow });
  await openBlock({ runId, id: BLOCK, level: 'L2', owned: [FILE], acceptance: [{ clause: 'review the fixture', tests: ['fresh-split'] }], writeRow });
  /** @type {Array<Record<string, any>>} */
  const calls = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    calls.push(opts);
    const text = readFileSync(opts.promptPath, 'utf8');
    const hunks = text.split('\n').filter((line) => line.startsWith('- @@')).map((line) => line.slice(2));
    return { status: 'ok', exit_code: 0, answer: answerFor(hunks, { passed: findings.length === 0, findings }), usage: { tokens_in: opts.rowExtra.ctx_tokens_in, tokens_out: 300 } };
  };
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: config, runRootDir: freshDir('worker'), slug: 'fresh-split-test', key: await loadKey(runId), pollMs: 20 },
    { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, spawn, writeRow, readRows: async () => rows, jev: async () => ({ ok: false, reason: 'offline' }) },
  );
  announceWorker(repo, { pid: process.pid, run: runId });
  const first = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: FILE });
  return { repo, runId, config, rows, calls, worker, first, writeRow, marker: path.join(queueDir(repo), `${first.ticket}.done`), done: () => retractWorker(repo, process.pid) };
}

test('B110 (1)(3)(6): cached round-1 split reruns the same ticket after a budget reload, plain and fresh', { timeout: 20000 }, async () => {
  const observed = [];
  for (const flags of [[], ['--fresh']]) {
    const f = await fixture();
    try {
      assert.equal(await f.worker.drain(), 1);
      const before = readResult(f.repo, f.runId, f.first.ticket);
      assert.deepEqual([before?.status, before?.budget, before?.round, before?.sessions, f.calls.length], ['split_required', 6000, 0, [], 0]);
      assert.equal(f.rows.filter((r) => r.event === 'review.round').length, 0);
      const reload = await reloadRun({ runId: f.runId, config: cfgFor({ budgets: { quick_in: 9000, full_in: 9000 }, session_timeout_s: 2 }), readPending: () => pendingTickets(f.repo), writeRow: f.writeRow });
      assert.deepEqual([reload.changed, reload.pinned, reload.rowError], [['review.budgets.full_in', 'review.budgets.quick_in'], 0, null]);
      const retry = await request(f.repo, flags);
      // On HEAD plain returns done (budget 6000), while --fresh refuses fresh-needs-reopen.
      observed.push([retry.code, retry.json.status, retry.json.reason ?? null]);
      if (retry.json.status !== 'queued') continue;
      assert.deepEqual(retry.json, { ...f.first, ...(flags.length === 0 ? { retry: true } : { fresh: true }) });
      assert.equal(existsSync(f.marker), false);
      assert.deepEqual(pendingTickets(f.repo), [f.first.ticket]);
      assert.equal(await f.worker.drain(), 1);
      const result = readResult(f.repo, f.runId, f.first.ticket);
      assert.deepEqual([result?.ticket, result?.content_hash, result?.status, result?.approved, result?.round, f.calls.length, result?.sessions.length], [f.first.ticket, f.first.content_hash, 'reviewed', true, 1, 1, 1]);
      assert.deepEqual(f.calls.map((c) => [c.cfg.review.budgets.quick_in, c.cfg.review.budgets.full_in, c.rowExtra.lens, c.level]), [[9000, 9000, 'quick', 'L2']]);
      assert.equal(f.rows.filter((r) => r.event === 'review.round').length, 1);
      assert.equal(f.rows.filter((r) => r.event === 'review.approved').length, 1);
      assert.deepEqual(pendingTickets(f.repo), []);
    } finally {
      f.done();
    }
  }
  assert.deepEqual(observed, [[0, 'queued', null], [0, 'queued', null]]);
});

test('B110 (2): freshVerdict reruns split only with a matching marker status and keeps other verdicts', () => {
  const marker = (/** @type {string} */ status) => JSON.stringify({ status });
  assert.deepEqual([
    freshVerdict(marker('split_required'), { status: 'split_required' }),
    freshVerdict(marker('reviewed'), { status: 'split_required' }),
    freshVerdict(marker('unavailable'), { status: 'split_required' }),
    freshVerdict('{', { status: 'split_required' }),
    freshVerdict(marker('split_required'), null),
    freshVerdict(marker('stopped'), { status: 'stopped', stopped: 'split_required' }),
    freshVerdict(marker('unknown'), { status: 'unknown' }),
    freshVerdict(marker('refused'), { status: 'refused', reason: 'review-state-tampered' }),
    freshVerdict(marker('unavailable'), { status: 'unavailable' }),
  ], ['rerun', 'counted', 'counted', 'counted', 'counted', 'counted', 'counted', 'tampered', 'rerun']);
});

test('B110 (4): repeated retries with the small live budget still split and spawn zero sessions', { timeout: 20000 }, async () => {
  for (const flags of [[], ['--fresh']]) {
    const f = await fixture();
    try {
      assert.equal(await f.worker.drain(), 1);
      const initial = readResult(f.repo, f.runId, f.first.ticket);
      assert.deepEqual([initial?.status, initial?.budget, initial?.round], ['split_required', 6000, 0]);
      for (let attempt = 0; attempt < 2; attempt += 1) {
        const retry = await request(f.repo, flags);
        assert.deepEqual(retry, { code: 0, json: { ...f.first, ...(flags.length === 0 ? { retry: true } : { fresh: true }) } });
        assert.equal(existsSync(f.marker), false);
        assert.deepEqual(pendingTickets(f.repo), [f.first.ticket]);
        assert.equal(await f.worker.drain(), 1);
        const result = readResult(f.repo, f.runId, f.first.ticket);
        assert.deepEqual([result?.status, result?.budget, result?.tokens_in, result?.round, result?.sessions, f.calls.length], ['split_required', 6000, initial.tokens_in, 0, [], 0]);
        assert.deepEqual(pendingTickets(f.repo), []);
      }
      assert.deepEqual(f.rows.filter((r) => r.event === 'review.result').map((r) => [r.status, r.budget]), [['split_required', 6000], ['split_required', 6000], ['split_required', 6000]]);
      assert.deepEqual(f.rows.filter((r) => ['review.round', 'review.approved', 'review.cap'].includes(r.event)), []);
    } finally {
      f.done();
    }
  }
});

test('B110 (5): a real recheck split stop keeps its cap and cached bytes; fresh needs reopen', { timeout: 20000 }, async () => {
  const finding = { id: 'W1', file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: 'fake defect', evidence: 'fake evidence', fix: 'fix the fixture' };
  const f = await fixture(true, [finding]);
  try {
    assert.equal(await f.worker.drain(), 1);
    const first = readResult(f.repo, f.runId, f.first.ticket);
    assert.deepEqual([first?.status, first?.approved, first?.round, f.calls.length], ['reviewed', false, 1, 1]);
    writeFile(f.repo, FILE, largeContent());
    const queued = await request(f.repo);
    assert.deepEqual([queued.code, queued.json.status], [0, 'queued']);
    assert.equal(await f.worker.drain(), 1);
    const ticket = queued.json.ticket;
    const stopped = readResult(f.repo, f.runId, ticket);
    assert.deepEqual([stopped?.status, stopped?.stopped, stopped?.round, stopped?.sessions, f.calls.length], ['stopped', 'split_required', 1, [], 1]);
    assert.deepEqual(f.rows.filter((r) => r.event === 'review.cap').map((r) => [r.reason, r.round, r.open, r.budget]), [['split_required', 1, ['W1'], 6000]]);
    assert.equal(f.rows.filter((r) => r.event === 'review.round').length, 1);
    const marker = path.join(queueDir(f.repo), `${ticket}.done`);
    const bytes = readFileSync(marker);
    const plain = await request(f.repo);
    assert.deepEqual(plain, { code: 0, json: { ...queued.json, status: 'done' } });
    const fresh = await request(f.repo, ['--fresh']);
    assert.deepEqual([fresh.code, fresh.json.status, fresh.json.reason, fresh.json.ticket, fresh.json.exits], [1, 'refused', 'fresh-needs-reopen', ticket, ['block reopen', 'block waive']]);
    assert.deepEqual(readFileSync(marker), bytes);
    assert.deepEqual(readResult(f.repo, f.runId, ticket), stopped);
    assert.deepEqual(pendingTickets(f.repo), []);
    assert.equal(await f.worker.drain(), 0);
    assert.equal(f.calls.length, 1);
  } finally {
    f.done();
  }
});

test('B110 fail closed: a split claim whose restore fails queues nothing, plain and fresh', { timeout: 20000 }, async () => {
  for (const flags of [[], ['--fresh']]) {
    const f = await fixture();
    try {
      assert.equal(await f.worker.drain(), 1);
      let claims = 0;
      const dropDone = (/** @type {string} */ repo, /** @type {string} */ ticket, /** @type {Buffer} */ seen) => {
        claims += 1;
        return dropDoneIf(repo, ticket, seen, {
          read: () => Buffer.from('a changed marker'),
          link: () => { throw Object.assign(new Error('fake denied'), { code: 'EACCES' }); },
        });
      };
      const refused = await request(f.repo, flags, { dropDone });
      assert.deepEqual([refused.code, refused.json.status, refused.json.reason, claims], [1, 'refused', 'fresh-restore-failed', 1]);
      assert.deepEqual(JSON.parse(readFileSync(f.marker, 'utf8')), { ticket: f.first.ticket, status: 'refused', reason: 'fresh-restore-failed' });
      assert.deepEqual(pendingTickets(f.repo), []);
      assert.equal(await f.worker.drain(), 0);
      assert.equal(f.calls.length, 0);
      assert.equal(f.rows.filter((r) => r.event === 'review.result').length, 1);
    } finally {
      f.done();
    }
  }
});
