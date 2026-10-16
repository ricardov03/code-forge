import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { readRun, saveRun } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { reviewTicket } = await import('../../src/worker/engine.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, enqueueHash, readResult, readTicket } = await import('../../src/worker/queue.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const FILES = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'];
const warning = (file) => ({ id: 'F1', file, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: 'wrong value', evidence: 'value', fix: 'fix value' });

async function fixture() {
  const { repo, runId } = await makeRepo();
  const record = await readRun(runId);
  record.blocks.B11.kind = 'light';
  await saveRun(record);
  const key = await loadKey(runId), rows = [], packets = [], lenses = [];
  const script = { findings: [], budget: 32000, min: 40, tokens: 300 };
  const store = await createKeyStore({ backends: [], dir: freshDir('light-store') });
  const boot = () => createWorker({ runId, repoRoot: repo, cfg: { review: { budgets: { full_in: script.budget }, min_tokens_out: script.min } }, runRootDir: freshDir('light-root'), slug: 'worker-test', key }, {
    store, env: {}, writeRow: async (row) => { rows.push(row); }, readRows: async () => rows,
    spawn: async (opts) => {
      const text = readFileSync(opts.promptPath, 'utf8');
      packets.push(text); lenses.push(opts.rowExtra.lens);
      return { status: 'ok', exit_code: 0, usage: { tokens_in: 900, tokens_out: script.tokens }, answer: {
        passed: script.findings.length === 0, summary: 'reviewed every file',
        reviewed_hunks: [...text.matchAll(/^- (@@.*)$/gm)].map((m) => m[1]), findings: script.findings, resolved: [], needs_file: [],
      } };
    }, jev: async () => ({ ok: false }),
  });
  let worker = await boot();
  const round = async (files = FILES) => {
    const tickets = files.map((file) => enqueue({ repoRoot: repo, run: runId, block: 'B11', file }));
    await worker.drain();
    return tickets.map((t) => readResult(repo, runId, t.ticket));
  };
  return { repo, runId, rows, key, packets, lenses, script, round, restart: async () => { worker = await boot(); } };
}

test('B139: one full-budget packet/session reviews all changed files and signs a round and approval for every content hash', async () => {
  const f = await fixture(), results = await f.round();
  assert.deepEqual(results.map((r) => [r.status, r.approved]), FILES.map(() => ['reviewed', true]));
  assert.equal(f.packets.length, 1, 'all three tickets must share one session');
  assert.deepEqual(f.lenses, ['full']);
  for (const file of FILES) {
    assert.ok(f.packets[0].includes(`### ${file}\n`), `missing diff heading for ${file}`);
    assert.ok(f.packets[0].includes(`+++ b/${file}`), `missing diff for ${file}`);
  }
  for (const event of ['review.round', 'review.approved']) {
    const rows = f.rows.filter((r) => r.event === event);
    assert.deepEqual(rows.map((r) => [r.file, r.content_hash]).sort(), FILES.map((file) => [file, contentHash(f.repo, file)]), `${event}: exactly one signed row per file, including files after the first`);
    assert.equal(rows.filter((r) => verifyRow(r, f.key).ok).length, 3);
  }
});

test('B139: warning ends the light round; next edits get one new batch, then the normal per-file loop', async () => {
  const f = await fixture();
  f.script.findings = [warning(FILES[0])];
  const first = await f.round();
  assert.deepEqual(first.map((r) => [r.approved, r.findings.length]), [[false, 1], [true, 0], [true, 0]]);
  assert.equal(first[0].findings[0].file, FILES[0]);
  assert.deepEqual(f.lenses, ['full'], 'no light recheck or triage sessions');
  appendFileSync(path.join(f.repo, FILES[0]), 'export const fixed = 1;\n');
  f.script.findings = [];
  await f.restart();
  const second = await f.round();
  assert.deepEqual(second.map((r) => r.approved), [true, true, true]);
  assert.deepEqual(f.lenses, ['full', 'full']);
  for (const file of FILES) appendFileSync(path.join(f.repo, file), 'export const later = 1;\n');
  const third = await f.round();
  assert.deepEqual(third.map((r) => r.approved), [true, true, true]);
  assert.equal(f.packets.length, 5, 'after two batches, three changed files get three separate sessions');
  assert.equal(f.rows.filter((r) => r.event === 'review.light_batch').length, 2);
});

test('B139: repeated same-content ticket after restart reuses the signed batch and consumes no extra round', async () => {
  const f = await fixture();
  await f.round([FILES[0]]);
  await f.restart();
  const results = await f.round([FILES[1], FILES[2]]);
  assert.deepEqual(results.map((r) => r.approved), [true, true]);
  assert.equal(f.packets.length, 1, 'a restarted worker must reuse the first batch for later file tickets');
  assert.equal(f.rows.filter((r) => r.event === 'review.round').length, 3);
});

test('B139: cached light results reject an older ticket hash and still serve the current hash without new rows or sessions', async () => {
  const f = await fixture(), file = FILES[0];
  const olderHash = contentHash(f.repo, file);
  appendFileSync(path.join(f.repo, file), 'export const updated = 1;\n');
  const [reviewed] = await f.round([file]);
  assert.equal(reviewed.status, 'reviewed');
  assert.equal(reviewed.approved, true);
  assert.equal(f.rows.filter((r) => r.event === 'review.light_result').length, 1);
  assert.equal(f.packets.length, 1);
  const rowsBefore = structuredClone(f.rows);
  const ctx = {
    repoRoot: f.repo, runId: f.runId, runRootDir: freshDir('light-cache-root'), cfg: {}, key: f.key,
    readRows: async () => f.rows,
    writeRow: async (row) => { f.rows.push(row); },
    writeBlockRow: async (row) => { f.rows.push(row); },
    spawn: async () => assert.fail('cached or stale tickets must not start a session'),
  };
  const older = enqueueHash({ repoRoot: f.repo, run: f.runId, block: 'B11', file, content_hash: olderHash });
  const stale = await reviewTicket(readTicket(f.repo, older.ticket), ctx);
  assert.equal(stale.status, 'stale');
  assert.equal(stale.approved, false);
  assert.deepEqual(stale.sessions, []);
  assert.deepEqual(f.rows, rowsBefore, 'a stale ticket must not write review rows');

  const current = enqueue({ repoRoot: f.repo, run: f.runId, block: 'B11', file });
  assert.notEqual(current.content_hash, older.content_hash);
  const cached = await reviewTicket(readTicket(f.repo, current.ticket), ctx);
  const batch = f.rows.find((r) => r.event === 'review.light_result');
  assert.deepEqual(cached, batch.outcomes[file], 'the current hash must reuse the cached outcome');
  assert.deepEqual(f.rows, rowsBefore, 'cache reuse must not write review rows');
  assert.equal(f.packets.length, 1);
});

test('B139: over-budget, short answers and findings naming another file never approve batch files', async () => {
  for (const mode of ['budget', 'short', 'foreign']) {
    const f = await fixture();
    if (mode === 'budget') f.script.budget = 1;
    if (mode === 'short') f.script.tokens = 1;
    if (mode === 'foreign') f.script.findings = [warning('foreign.mjs')];
    // The fixture worker uses the boot config when no config snapshot is present.
    await f.restart();
    const results = await f.round();
    assert.deepEqual(results.map((r) => r.approved), [false, false, false], mode);
    assert.equal(f.rows.filter((r) => r.event === 'review.approved').length, 0, mode);
    if (mode === 'budget') assert.equal(f.packets.length, 0, 'full_in overflow must not spawn');
  }
});
