import { BLOCK, freshDir, makeRepo } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { runReport } = await import('../../src/cli/report.mjs');
const { buildReport } = await import('../../src/ledger/report.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { announceWorker, retractWorker, enqueue, enqueueAnswers, readTicket, writeResult, queueDir, blockTickets } = await import('../../src/worker/queue.mjs');
const { readRun, saveRun } = await import('../../src/state/run.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const { captureStream } = await import('../ledger/helpers.mjs');

async function request(cwd, args) {
  const stdout = captureStream(), stderr = captureStream();
  const code = await runReviewFile(args, { cwd, stdout, stderr, pollMs: 1 });
  return { code, json: stdout.text ? JSON.parse(stdout.text) : null, stderr: stderr.text };
}

const timing = (value) => Object.fromEntries(['queued_at', 'started_at', 'finished_at', 'duration_ms'].map((key) => [key, value[key]]));

async function workerFor(repo, runId, review, rows) {
  return createWorker({ runId, repoRoot: repo, cfg: {}, runRootDir: freshDir('worker'), slug: 'worker-test', key: await loadKey(runId) }, {
    env: {}, store: await createKeyStore({ backends: [], dir: freshDir('store') }), review,
    writeRow: async (row) => void rows.push(row), readRows: async () => rows, expiryCheck: async () => {},
  });
}

test('clause 1: --cwd queues and both waits match execution inside a block worktree subdirectory', async () => {
  const { repo, runId } = await makeRepo();
  const tree = freshDir('tree');
  assert.equal((await exec(['git', 'worktree', 'add', '-b', 'fixture-block', tree], { cwd: repo, env: gitChildEnv() })).code, 0);
  mkdirSync(path.join(tree, 'src'));
  for (const name of ['a', 'b', 'c']) writeFileSync(path.join(tree, 'src', `${name}.mjs`), `export const ${name} = 2;\n`);
  const record = await readRun(runId);
  record.blocks[BLOCK].worktree = { path: tree, branch: 'fixture-block', base_sha: record.blocks[BLOCK].base_sha };
  await saveRun(record);
  const cwd = path.join(tree, 'src');
  const outside = freshDir('outside');
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const args = ['a.mjs', '--block', BLOCK, '--run', runId];
    const inside = await request(cwd, args);
    const redirected = await request(outside, [...args, '--cwd', path.relative(outside, cwd)]);
    assert.deepEqual([redirected.code, redirected.stderr, redirected.json], [0, '', inside.json], 'one identical ticket from a relative --cwd outside git');
    assert.deepEqual([inside.json.file, inside.json.tree, blockTickets({ repoRoot: repo, run: runId, block: BLOCK, tree }).length], ['src/a.mjs', tree, 1]);
    const key = await loadKey(runId);
    for (const file of ['src/a.mjs', 'src/b.mjs', 'src/c.mjs']) {
      const queued = enqueue({ repoRoot: repo, run: runId, block: BLOCK, tree, file });
      writeResult({ repoRoot: repo, runId, ticket: queued.ticket, key, result: { status: 'reviewed', approved: true } });
    }
    for (const wait of [['--wait', inside.json.ticket], ['--wait-block', BLOCK, '--run', runId]]) {
      const direct = await request(cwd, [...wait, '--max', '0ms']);
      const viaFlag = await request(outside, [...wait, '--max', '0ms', '--cwd', cwd]);
      assert.deepEqual(viaFlag, direct, `${wait[0]} returns the same JSON from the main queue`);
      assert.equal(viaFlag.code, 0);
      if (wait[0] === '--wait-block') assert.deepEqual([viaFlag.json.done, viaFlag.json.files.length], [true, 3]);
      else assert.equal(viaFlag.json.result.approved, true);
    }
  } finally { retractWorker(repo, process.pid); }
});

test('clause 1 boundary: nonexistent directories, file paths and missing --cwd values exit 2', async () => {
  const { repo } = await makeRepo();
  const results = [];
  for (const tail of [['--cwd', path.join(repo, 'absent')], ['--cwd', path.join(repo, 'src/a.mjs')], ['--cwd']]) {
    for (const args of [['src/a.mjs', '--block', BLOCK], ['--wait', 'a'.repeat(24)], ['--wait-block', BLOCK, '--run', 'r-fixture']]) {
      results.push(await request(repo, [...args, ...tail]));
    }
  }
  assert.deepEqual(results.map((r) => [r.code, r.json]), Array.from({ length: 9 }, () => [2, null]), 'all nine invalid directory requests are usage errors with no JSON');
  assert.equal(results.filter((r) => /--cwd/.test(r.stderr)).length, 9, 'all nine usage messages identify --cwd');
  assert.equal(results.slice(0, 6).filter((r) => /--cwd.*directory/.test(r.stderr)).length, 6, 'six invalid paths identify an unusable directory');
  assert.equal(results.slice(6).filter((r) => /--cwd needs a value/.test(r.stderr)).length, 3, 'three missing values are diagnosed before repository lookup');
});

test('clause 2: worker records all ticket timings, waits expose them and report totals review time beside gate time', async (t) => {
  const { repo, runId } = await makeRepo();
  const rows = [];
  const queuedAt = new Date('2026-10-20T10:00:00.000Z');
  const tickets = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => enqueue({ repoRoot: repo, run: runId, block: BLOCK, file, now: queuedAt }));
  const worker = await workerFor(repo, runId, async (ticket) => {
    const active = readTicket(repo, ticket.ticket);
    assert.deepEqual([active.queued_at, active.finished_at, active.duration_ms], [queuedAt.toISOString(), null, null]);
    assert.ok(Date.parse(active.started_at) >= queuedAt.getTime() + 10000 && Date.parse(active.started_at) <= Date.now(), 'active ticket records a start before the fake review');
    t.mock.timers.tick(1250);
    return { status: 'reviewed', approved: true, sessions: [] };
  }, rows);
  t.mock.timers.enable({ apis: ['Date'], now: queuedAt.getTime() + 10000 });
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    assert.equal(readTicket(repo, tickets[0].ticket).queued_at, queuedAt.toISOString(), 'queue timestamp is persisted before processing');
    assert.equal(await worker.drain(), 3);
    const joined = await request(repo, ['--wait-block', BLOCK, '--run', runId, '--max', '0ms']);
    assert.deepEqual([joined.code, joined.json.done, joined.json.files.length], [0, true, 3]);
    for (const ticket of tickets) {
      const saved = readTicket(repo, ticket.ticket);
      const expected = timing(saved);
      assert.equal(expected.queued_at, queuedAt.toISOString());
      assert.equal(expected.duration_ms, Date.parse(expected.finished_at) - Date.parse(expected.started_at));
      assert.ok(expected.duration_ms >= 1250, 'duration includes the fake review, excludes ten seconds of queue delay');
      assert.ok(Date.parse(expected.started_at) >= queuedAt.getTime() + 10000);
      const waited = await request(repo, ['--wait', ticket.ticket, '--max', '0ms']);
      assert.deepEqual([waited.code, waited.json.status, timing(waited.json), timing(waited.json.result)], [0, 'done', expected, expected]);
      assert.deepEqual(timing(joined.json.files.find((f) => f.ticket === ticket.ticket)), expected);
    }
    const completed = rows.filter((row) => row.event === 'review.result');
    assert.equal(completed.length, 3, 'one timing row for each completed ticket');
    const total = completed.reduce((sum, row) => sum + row.duration_ms, 0);
    assert.ok(Number.isFinite(total) && total >= 3750);
    const reportRows = [...completed, { event: 'gate.done', run: runId, block: BLOCK, duration_ms: 2000 }];
    assert.deepEqual(buildReport(reportRows).sections.cost_per_block.map((b) => [b.block, b.reviewTimeMs, b.gateTimeMs]), [[BLOCK, total, 2000]]);
    for (const row of reportRows) await appendRow(row, { slug: 'review-timing' });
    const stdout = captureStream(), stderr = captureStream();
    assert.equal(await runReport(['--slug', 'review-timing'], { stdout, stderr }), 0);
    assert.match(stdout.text.split('== cost_per_block ==\n')[1].split('== ')[0], /gates 2s · review time \d+s · proof 0s/);
    assert.equal(stderr.text, '');
  } finally { retractWorker(repo, process.pid); t.mock.timers.reset(); }
});

test('clause 2 boundary: legacy answers tickets and engine failures retain timings; report ignores unmeasured and shadow rows', async (t) => {
  const { repo, runId } = await makeRepo();
  const queuedAt = new Date('2026-10-20T10:00:00.000Z');
  const queued = enqueueAnswers({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs', byFile: 'src/b.mjs', answers: ['F1'], now: queuedAt });
  const legacy = readTicket(repo, queued.ticket);
  delete legacy.queued_at;
  writeFileSync(path.join(queueDir(repo), `${queued.ticket}.json`), JSON.stringify(legacy));
  const rows = [];
  const worker = await workerFor(repo, runId, async () => { t.mock.timers.tick(250); throw new Error('fake engine failure'); }, rows);
  t.mock.timers.enable({ apis: ['Date'], now: queuedAt.getTime() + 5000 });
  try {
    assert.equal(await worker.drain(), 1);
    const waited = await request(repo, ['--wait', queued.ticket]);
    const expected = { queued_at: queuedAt.toISOString(), started_at: new Date(queuedAt.getTime() + 5000).toISOString(), finished_at: new Date(queuedAt.getTime() + 5250).toISOString(), duration_ms: 250 };
    assert.deepEqual([waited.json.result.status, waited.json.result.reason, timing(waited.json), timing(readTicket(repo, queued.ticket))], ['unavailable', 'engine-error', expected, expected]);
    const base = { run: runId, block: BLOCK };
    const noise = [undefined, null, -1, NaN, Infinity, '100'].map((duration_ms) => ({ ...base, event: 'review.result', duration_ms }));
    const report = buildReport([...rows, ...noise,
      { ...base, event: 'review.done', duration_ms: 9999 },
      { ...base, event: 'review.result', source: 'shadow', duration_ms: 9999 },
      { ...base, event: 'review.result', run: 'r-other', duration_ms: 0 },
      { ...base, event: 'block.close', run: 'r-legacy' },
    ]).sections.cost_per_block;
    assert.deepEqual(report.map((b) => [b.run, b.reviewTimeMs ?? null]), [[runId, 250], ['r-other', 0], ['r-legacy', null]], 'count only measured completion rows in their own run; preserve unknown legacy timing');
  } finally { t.mock.timers.reset(); }
});
