import { BLOCK, makeRepo, startWorker, stopWorker, cli, queued, VALID_REVIEW, freshDir } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdirSync, readFileSync, writeFileSync, rmSync, readdirSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const { announceWorker, retractWorker, enqueue, queueDir, readResult, writeResult, enqueueAnswers, blockTickets, resolveTicket, dropDoneIf } = await import('../../src/worker/queue.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { validateConfig, reviewFileLoop } = await import('../../src/config/validate.mjs');
const { loadConfigFile } = await import('../../src/config/load.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { readRun, saveRun, startRun } = await import('../../src/state/run.mjs');
const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');

/** @param {string} repo @param {string} runId @param {string[]} [extra] @param {{dropDone?: typeof dropDoneIf}} [deps] */
async function waitBlock(repo, runId, extra = [], deps = {}) {
  let out = '';
  let err = '';
  const code = await runReviewFile(['--wait-block', BLOCK, '--run', runId, ...extra], {
    ...deps, cwd: repo, pollMs: 1, stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } },
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
      tickets.push({ file, ticket: res.json.ticket, status: 'done', outcome: 'approved' });
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
    assert.deepEqual(res.json.files[0], { file: 'src/a.mjs', ticket: normal.json.ticket, status: 'done', outcome: 'approved' });
    assert.equal(readResult(repo, runId, old.ticket).status, 'stale');
    assert.deepEqual([queued(repo, 'json').length, queued(repo, 'done').length], [4, 4]);
  } finally { await stopWorker(worker, runId); }
});

test('(3) timeout lists exactly the pending files, preserving completed tickets', async () => {
  const { repo, runId } = await makeRepo();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const tickets = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => enqueue({ repoRoot: repo, run: runId, block: BLOCK, file }));
    writeResult({ repoRoot: repo, runId, ticket: tickets[0].ticket, key: await loadKey(runId), result: { status: 'reviewed', approved: false, findings } });
    const res = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual(res, { code: 0, json: { block: BLOCK, files: tickets.map((t, i) => ({ file: t.file, ticket: t.ticket, status: i === 0 ? 'done' : 'pending', outcome: i === 0 ? 'findings' : null })), done: false, reason: 'wait_timeout' } });
    assert.deepEqual([queued(repo, 'json').length, queued(repo, 'done').length], [3, 1]);
  } finally { retractWorker(repo, process.pid); }
});

test('B131: live join queues missing own reviews; other blocks, runs, trees and answers never count', async () => {
  const { repo, runId } = await makeRepo();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    enqueue({ repoRoot: repo, run: 'r-other', block: BLOCK, file: 'src/a.mjs' });
    enqueue({ repoRoot: repo, run: runId, block: 'Bother', file: 'src/a.mjs' });
    enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs', tree: repo });
    enqueueAnswers({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs', byFile: 'src/b.mjs', answers: ['F1'] });
    const res = await waitBlock(repo, runId, ['--max', '0ms']);
    const files = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => ({ file, ticket: resolveTicket({ repoRoot: repo, run: runId, block: BLOCK, file }).ticket, status: 'pending', outcome: null }));
    assert.deepEqual(res, { code: 0, json: { block: BLOCK, files, done: false, reason: 'wait_timeout' } });
    assert.equal(queued(repo, 'json').length, 7, 'exactly three owned current-content tickets added');
    assert.deepEqual(await waitBlock(repo, runId, ['--max', '0ms']), res, 'rejoining preserves the three pending ticket IDs');
    assert.equal(queued(repo, 'json').length, 7, 'zero duplicate tickets on the second join');
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
    const expected = { code: 3, json: { block: BLOCK, files: tickets.map((t) => ({ file: t.file, ticket: t.ticket, status: 'done', outcome: 'approved' })), done: false, status: 'worker_down' } };
    assert.deepEqual([absent, wrong], [expected, expected]);
    assert.equal(queued(repo, 'json').length, 3);
  } finally { retractWorker(repo, process.pid); }
});

test('B131: outcomes come from completion markers while existing done and pending statuses stay unchanged', async () => {
  const { repo, runId } = await makeRepo();
  const tickets = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => enqueue({ repoRoot: repo, run: runId, block: BLOCK, file }));
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    /** @type {Array<[Record<string, any>, string]>} */
    const cases = [
      [{ status: 'reviewed', approved: true }, 'approved'],
      [{ status: 'reviewed', open_findings: true }, 'findings'],
      [{ status: 'reviewed', approved: false }, 'findings'],
      [{ status: 'reviewed' }, 'approved'], // older markers encode approval by absence of open_findings
      [{ status: 'stopped' }, 'stopped'],
      [{ status: 'split_required' }, 'split_required'],
      [{ status: 'unavailable' }, 'unavailable'],
      [{ status: 'stale' }, 'unavailable'],
      [{ status: 'refused' }, 'unavailable'],
      [{ status: 'unknown' }, 'unavailable'],
    ];
    for (const [marker, outcome] of cases) {
      // Deliberately no result file: classification uses the marker alone, without a key.
      writeFileSync(path.join(queueDir(repo), `${tickets[0].ticket}.done`), JSON.stringify({ ticket: tickets[0].ticket, ...marker }));
      const res = await waitBlock(repo, runId, ['--max', '0ms']);
      assert.deepEqual([res.code, res.json.files], [0, tickets.map((t, i) => ({ file: t.file, ticket: t.ticket, status: i === 0 ? 'done' : 'pending', outcome: i === 0 ? outcome : null }))], `${JSON.stringify(marker)} classifies exactly one done file and two pending files`);
    }
  } finally { retractWorker(repo, process.pid); }
});

test('B131: a repeated split_required makes the join incomplete and names the owner budget/reload/fresh steps', async () => {
  const { repo, runId } = await makeRepo();
  const tickets = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => enqueue({ repoRoot: repo, run: runId, block: BLOCK, file }));
  const key = await loadKey(runId);
  const split = tickets[1];
  const finishSplit = () => writeResult({ repoRoot: repo, runId, ticket: split.ticket, key, result: { status: 'split_required', sessions: [] } });
  writeResult({ repoRoot: repo, runId, ticket: tickets[0].ticket, key, result: { status: 'reviewed', approved: true } });
  // c stays pending: split_required must take precedence over the join timeout.
  finishSplit();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const retry = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual([retry.json.done, retry.json.reason, retry.json.files[1].status], [false, 'wait_timeout', 'pending'], 'the existing one-time retry remains pending');
    finishSplit();
    for (let join = 0; join < 2; join += 1) {
      if (join === 1) writeResult({ repoRoot: repo, runId, ticket: tickets[2].ticket, key, result: { status: 'reviewed', approved: true } });
      const res = await waitBlock(repo, runId, ['--max', '0ms']);
      assert.deepEqual([res.code, res.json.done, res.json.reason, res.json.files.map((f) => [f.status, f.outcome])], [0, false, 'split_required', [['done', 'approved'], ['done', 'split_required'], join === 0 ? ['pending', null] : ['done', 'approved']]]);
      assert.equal(res.json.next, `The owner raises review.budgets for ${split.file}, runs run reload, then review-file ${split.file} --fresh.`);
      assert.deepEqual([queued(repo, 'json').length, queued(repo, 'done').length], [3, join === 0 ? 2 : 3], 'later joins keep the split marker and add zero tickets');
    }
  } finally { retractWorker(repo, process.pid); }
});

test('B131 boundary: missing tickets remain not_queued with null outcomes when no worker is live', async () => {
  const { repo, runId } = await makeRepo();
  const res = await waitBlock(repo, runId, ['--max', '0ms']);
  assert.deepEqual(res, { code: 3, json: { block: BLOCK, files: ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((file) => ({ file, ticket: null, status: 'not_queued', outcome: null })), done: false, status: 'worker_down' } });
  assert.equal(queued(repo, 'json').length, 0, 'zero tickets queued without a live worker');
});

test('(6) file_loop accepts wait, overlap and watch, defaults to wait, and rejects every other type/value', async () => {
  const { repo } = await makeRepo();
  const loaded = await loadConfigFile(path.join(repo, '.code-forge.yml'));
  const cfg = loaded.config;
  assert.deepEqual([validateConfig(cfg).errors, reviewFileLoop(cfg)], [[], 'wait']);
  const accepted = [];
  for (const mode of ['wait', 'overlap', 'watch']) {
    cfg.review.file_loop = mode;
    accepted.push([validateConfig(cfg).errors, reviewFileLoop(cfg)]);
  }
  assert.deepEqual(accepted, [[[], 'wait'], [[], 'overlap'], [[], 'watch']]);
  const rejected = [];
  for (const mode of ['other', '', null, 1, true, [], {}]) {
    cfg.review.file_loop = mode;
    const result = validateConfig(cfg);
    rejected.push([result.valid, result.errors.map((e) => [e.rule, e.path])]);
  }
  assert.deepEqual(rejected, [1, 1, 2, 2, 2, 2, 2].map((count) => [false, Array.from({ length: count }, () => ['schema', '/review/file_loop'])]));
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
    assert.deepEqual(res, { code: 0, json: { block: BLOCK, files: [{ file: 'src/a.mjs', ticket: normal.json.ticket, status: 'done', outcome: 'approved' }], done: true } });
    assert.deepEqual([queued(repo, 'json').length, queued(repo, 'done').length], [2, 1]);
  } finally { retractWorker(repo, process.pid); }
});

const FILE = 'src/a.mjs';
const deliveredPath = (repo, ticket) => path.join(queueDir(repo), `${ticket}.delivered`);
const findings = ['F1', 'F2', 'F3'].map((id) => ({ id, file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' }));

/** Marker-only join fixture; b and c already finished. */
async function blindChange(status = 'reviewed', approved = false) {
  const p = await makeRepo();
  const key = await loadKey(p.runId);
  const first = enqueue({ repoRoot: p.repo, run: p.runId, block: BLOCK, file: FILE });
  writeResult({ repoRoot: p.repo, runId: p.runId, ticket: first.ticket, key, result: { status, approved, findings: approved ? [] : findings } });
  for (const file of ['src/b.mjs', 'src/c.mjs']) {
    const t = enqueue({ repoRoot: p.repo, run: p.runId, block: BLOCK, file });
    writeResult({ repoRoot: p.repo, runId: p.runId, ticket: t.ticket, key, result: { status: 'reviewed', approved: true } });
  }
  writeFileSync(path.join(p.repo, FILE), 'export const a = 2;\n');
  announceWorker(p.repo, { pid: process.pid, run: p.runId });
  return { ...p, first };
}

test('B122 clause 1: blind changed content delivers findings once without a new ticket', async () => {
  const p = await blindChange();
  try {
    const older = p.first.ticket;
    p.first = enqueue({ repoRoot: p.repo, run: p.runId, block: BLOCK, file: FILE });
    writeResult({ repoRoot: p.repo, runId: p.runId, ticket: p.first.ticket, key: await loadKey(p.runId), result: { status: 'reviewed', approved: false, findings } });
    writeFileSync(path.join(p.repo, FILE), 'export const a = 3;\n');
    assert.notEqual(p.first.ticket, older);
    const res = await waitBlock(p.repo, p.runId, ['--max', '0ms']);
    assert.deepEqual(res.json.files[0], { file: FILE, ticket: p.first.ticket, status: 'findings', outcome: 'findings' });
    assert.deepEqual([res.code, res.json.done, queued(p.repo, 'json').length], [0, true, 4]);
    const names = readdirSync(queueDir(p.repo)).filter((n) => n.endsWith('.delivered'));
    assert.deepEqual(names, [`${p.first.ticket}.delivered`]);
    assert.deepEqual(JSON.parse(readFileSync(deliveredPath(p.repo, p.first.ticket), 'utf8')), { block: BLOCK, file: FILE, ticket: p.first.ticket });
  } finally { retractWorker(p.repo, process.pid); }
});

/** Real watch and worker with scripted reviewer subprocesses, counting sessions by content. */
async function scriptedWatch() {
  const { repo, runId } = await makeRepo({ start: false });
  rmSync(path.join(repo, 'src/b.mjs')); rmSync(path.join(repo, 'src/c.mjs'));
  const cfg = (await loadConfigFile(path.join(repo, '.code-forge.yml'))).config;
  cfg.review.file_loop = 'watch';
  const rows = [];
  const writeRow = async (row) => { rows.push(row); };
  await startRun({ workspace: repo, project: 'worker-test', runId, config: cfg, writeRow });
  const { openBlock } = await import('../../src/state/block.mjs');
  await openBlock({ runId, id: BLOCK, level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'blind commit replay', tests: ['wait-block'] }], writeRow });
  const { watchTick } = await import('../../src/worker/watch.mjs');
  const { createWorker } = await import('../../src/worker/loop.mjs');
  const { createKeyStore } = await import('../../src/keys/store.mjs');
  const { spawnSession } = await import('../../src/session/spawn.mjs');
  const script = path.join(freshDir('blind-script'), 'script.json');
  writeFileSync(script, JSON.stringify({ full: { findings }, recheck: { resolve: true } }));
  const sessions = [];
  const worker = await createWorker({ runId, repoRoot: repo, cfg, runRootDir: freshDir('blind-worker'), slug: 'worker-test', key: await loadKey(runId), pollMs: 1 }, {
    store: await createKeyStore({ backends: [], dir: freshDir('blind-store') }),
    env: { PATH: process.env.PATH, HOME: process.env.HOME, TMPDIR: process.env.TMPDIR, FAKE_SCRIPT: script },
    bins: { claude: fileURLToPath(new URL('./fake-scripted-reviewer.mjs', import.meta.url)) }, writeRow, readRows: async () => rows, expiryCheck: async () => {},
    spawn: async (opts, deps) => { sessions.push(resolveTicket({ repoRoot: repo, run: runId, block: BLOCK, file: FILE }).content_hash); return spawnSession(opts, deps); },
  });
  const commit = async (text) => {
    writeFileSync(path.join(repo, FILE), text);
    for (const args of [['add', '--', FILE], ['-c', 'user.name=Fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'content']]) {
      assert.equal((await exec(['git', ...args], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 })).code, 0);
    }
    await watchTick({ repoRoot: repo, runId, cfg, writeRow });
    return resolveTicket({ repoRoot: repo, run: runId, block: BLOCK, file: FILE });
  };
  return { repo, runId, rows, sessions, worker, commit };
}

test('B122 clause 2: the next join queues one latest-content ticket and runs one recheck', async () => {
  const { repo, runId, sessions, worker, commit } = await scriptedWatch();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const first = await commit('export const a = 1;\n');
    assert.equal(await worker.drain(), 1);
    await commit('export const a = 2;\n');
    assert.equal((await waitBlock(repo, runId, ['--max', '0ms'])).json.files[0].status, 'findings');
    const marker = statSync(deliveredPath(repo, first.ticket));
    const latest = await commit('export const a = 3;\n');
    const joined = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual(joined.json.files[0], { file: FILE, ticket: latest.ticket, status: 'pending', outcome: null });
    assert.deepEqual((await waitBlock(repo, runId, ['--max', '0ms'])).json, joined.json);
    assert.equal(queued(repo, 'json').length, 2);
    assert.deepEqual([await worker.drain(), await worker.drain()], [1, 0]);
    assert.deepEqual(sessions, [first.content_hash, latest.content_hash], 'one initial session and exactly one recheck session for latest content');
    assert.deepEqual([readResult(repo, runId, latest.ticket).kind, readResult(repo, runId, latest.ticket).approved], ['recheck', true]);
    assert.deepEqual([statSync(deliveredPath(repo, first.ticket)).ino, statSync(deliveredPath(repo, first.ticket)).mtimeMs], [marker.ino, marker.mtimeMs]);
    assert.deepEqual((await waitBlock(repo, runId)).json.files[0], { file: FILE, ticket: latest.ticket, status: 'done', outcome: 'approved' });
  } finally { retractWorker(repo, process.pid); }
});

test('B122 clause 3: watch, blind commit, findings join, fix and one approved recheck never stall', async () => {
  const { repo, runId, rows, sessions, worker, commit } = await scriptedWatch();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const first = await commit('export const a = 1;\n');
    assert.equal(await worker.drain(), 1);
    const result = readResult(repo, runId, first.ticket);
    assert.deepEqual([result.status, result.approved, result.findings.map((f) => f.id)], ['reviewed', false, ['F1', 'F2', 'F3']]);
    const blind = await commit('export const a = 2;\n');
    assert.equal(await worker.drain(), 0);
    const joined = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual(joined, { code: 0, json: { block: BLOCK, files: [{ file: FILE, ticket: first.ticket, status: 'findings', outcome: 'findings' }], done: true } });
    assert.equal(await worker.drain(), 0);
    const read = await cli(['review-file', '--wait', first.ticket], repo);
    assert.deepEqual(read.json.fix_list.map((f) => f.id), ['F1', 'F2', 'F3']);
    const fixed = await commit('export const a = 3;\n');
    assert.equal(await worker.drain(), 0);
    assert.deepEqual((await waitBlock(repo, runId, ['--max', '0ms'])).json.files, [{ file: FILE, ticket: fixed.ticket, status: 'pending', outcome: null }]);
    assert.deepEqual([await worker.drain(), await worker.drain()], [1, 0]);
    const approved = readResult(repo, runId, fixed.ticket);
    assert.deepEqual([approved.status, approved.approved, approved.findings, approved.stopped ?? null, approved.trigger ?? null], ['reviewed', true, [], null, null]);
    assert.deepEqual(sessions, [first.content_hash, fixed.content_hash], 'exactly one session per initial/fixed content and zero for the blind commit');
    assert.equal(sessions.filter((hash) => hash === blind.content_hash).length, 0);
    assert.deepEqual(blockTickets({ repoRoot: repo, run: runId, block: BLOCK }).map((t) => t.ticket).sort(), [first.ticket, fixed.ticket].sort());
    assert.deepEqual(rows.filter((r) => r.event === 'review.cap' || r.trigger === 'review_stall' || r.reason === 'l3_patch_exhausted'), []);
    assert.equal((await waitBlock(repo, runId)).json.done, true);
  } finally { retractWorker(repo, process.pid); }
});

test('B122 clause 4: newer content after approval queues; stopped content never requeues', async () => {
  for (const status of ['reviewed', 'stopped']) {
    const p = await blindChange(status);
    try {
      if (status === 'reviewed') {
        p.first = enqueue({ repoRoot: p.repo, run: p.runId, block: BLOCK, file: FILE });
        writeResult({ repoRoot: p.repo, runId: p.runId, ticket: p.first.ticket, key: await loadKey(p.runId), result: { status, approved: true, findings: [] } });
        writeFileSync(path.join(p.repo, FILE), 'export const a = 3;\n');
      }
      const res = await waitBlock(p.repo, p.runId, ['--max', '0ms']);
      const latest = resolveTicket({ repoRoot: p.repo, run: p.runId, block: BLOCK, file: FILE }).ticket;
      assert.deepEqual(res.json.files[0], { file: FILE, ticket: status === 'stopped' ? p.first.ticket : latest, status: status === 'stopped' ? 'done' : 'pending', outcome: status === 'stopped' ? 'stopped' : null });
      assert.equal(queued(p.repo, 'json').length, status === 'stopped' ? 3 : 5);
      assert.deepEqual(readdirSync(queueDir(p.repo)).filter((n) => n.endsWith('.delivered')), []);
    } finally { retractWorker(p.repo, process.pid); }
  }
});

test('B122 boundary: an unreadable delivered marker refuses instead of queueing', async () => {
  const p = await blindChange();
  try {
    mkdirSync(deliveredPath(p.repo, p.first.ticket));
    const res = await waitBlock(p.repo, p.runId, ['--max', '0ms']);
    assert.deepEqual([res.code, res.json.status, res.json.reason, queued(p.repo, 'json').length], [1, 'refused', 'result-unreadable', 3]);
  } finally { retractWorker(p.repo, process.pid); }
});


test('B125 clause 1: current content returning to a stale ticket stays pending until one real review', async () => {
  const { repo, runId, sessions, worker, commit } = await scriptedWatch();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const first = await commit('export const a = 1;\n');
    writeFileSync(path.join(repo, FILE), 'export const a = 2;\n');
    assert.equal(await worker.drain(), 1, 'one old-content ticket finishes stale');
    assert.equal(readResult(repo, runId, first.ticket).status, 'stale');
    writeFileSync(path.join(repo, FILE), 'export const a = 1;\n');
    assert.deepEqual(sessions, [], 'zero reviewer sessions for content that moved before review');
    const pending = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual(pending, { code: 0, json: { block: BLOCK, files: [{ file: FILE, ticket: first.ticket, status: 'pending', outcome: null }], done: false, reason: 'wait_timeout' } });
    assert.deepEqual(queued(repo, 'done'), [], 'the stale current-content marker is dropped');
    assert.deepEqual([await worker.drain(), await worker.drain()], [1, 0]);
    assert.deepEqual(sessions, [first.content_hash], 'exactly one session reviews the returned content');
    assert.equal(readResult(repo, runId, first.ticket).status, 'reviewed');
    assert.deepEqual((await waitBlock(repo, runId)).json, { block: BLOCK, files: [{ file: FILE, ticket: first.ticket, status: 'done', outcome: 'findings' }], done: true });
  } finally { retractWorker(repo, process.pid); }
});

test('B125 clause 2: unavailable and round-1 split retry once; counted and unreadable results stay cached', async () => {
  const cases = [
    { result: { status: 'unavailable', reason: 'engine-error' }, retry: true },
    { result: { status: 'split_required', round: 0, sessions: [] }, retry: true },
    { result: { status: 'reviewed', approved: false, findings }, retry: false },
    { result: { status: 'reviewed', approved: true, findings: [] }, retry: false },
    { result: { status: 'stopped', stopped: 'split_required' }, retry: false },
    { result: { status: 'refused', reason: 'review-state-lost' }, retry: false },
    { result: { status: 'refused', reason: 'other-run' }, retry: false },
    { result: { status: 'unknown' }, retry: false },
    { result: { status: 'reviewed', approved: true }, corrupt: 'mismatch', retry: false },
    { result: { status: 'stale' }, corrupt: 'result', retry: false },
    { result: { status: 'stale' }, corrupt: 'marker', retry: false },
  ];
  for (const { result, retry, corrupt } of cases) {
    const { repo, runId, sessions, worker, commit } = await scriptedWatch();
    announceWorker(repo, { pid: process.pid, run: runId });
    try {
      const first = await commit('export const a = 1;\n');
      writeResult({ repoRoot: repo, runId, ticket: first.ticket, key: await loadKey(runId), result });
      const marker = path.join(queueDir(repo), `${first.ticket}.done`);
      if (corrupt === 'result') {
        const rel = JSON.parse(readFileSync(marker, 'utf8')).result;
        writeFileSync(path.join(repo, rel), '{');
      }
      if (corrupt === 'mismatch') {
        const parsed = JSON.parse(readFileSync(marker, 'utf8'));
        writeFileSync(marker, JSON.stringify({ ...parsed, status: 'stale' }));
      }
      if (corrupt === 'marker') writeFileSync(marker, '{');
      const before = readFileSync(marker);
      const inode = statSync(marker).ino;
      const joined = await waitBlock(repo, runId, ['--max', '0ms']);
      if (corrupt === 'marker') {
        assert.deepEqual([joined.code, joined.json.reason], [1, 'result-unreadable']);
      } else {
        const outcome = retry ? null : corrupt || result.status === 'refused' || result.status === 'unknown' ? 'unavailable' : result.status === 'stopped' ? 'stopped' : result.approved ? 'approved' : 'findings';
        assert.deepEqual([joined.code, joined.json.done, joined.json.files], [0, !retry, [{ file: FILE, ticket: first.ticket, status: retry ? 'pending' : 'done', outcome }]], `${result.status}/${corrupt ?? 'valid'} join`);
      }
      if (!retry) {
        assert.deepEqual([readFileSync(marker), statSync(marker).ino], [before, inode], `${result.status}/${corrupt ?? 'valid'} marker kept without a rename`);
      }
      assert.deepEqual([await worker.drain(), await worker.drain()], [retry ? 1 : 0, 0]);
      assert.deepEqual(sessions, retry ? [first.content_hash] : [], `${result.status}/${corrupt ?? 'valid'} exact sessions for current content`);
      if (retry) assert.equal((await waitBlock(repo, runId)).json.done, true);
    } finally { retractWorker(repo, process.pid); }
  }
});

test('B125 clause 3: a rerun that goes stale again is done across joins with no second drop', async () => {
  const { repo, runId, sessions, worker, commit } = await scriptedWatch();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const first = await commit('export const a = 1;\n');
    const move = () => writeFileSync(path.join(repo, FILE), 'export const a = 2;\n');
    const restore = () => writeFileSync(path.join(repo, FILE), 'export const a = 1;\n');
    move();
    assert.equal(await worker.drain(), 1);
    restore();
    const pending = await waitBlock(repo, runId, ['--max', '0ms']);
    assert.deepEqual([pending.json.done, pending.json.files], [false, [{ file: FILE, ticket: first.ticket, status: 'pending', outcome: null }]]);
    move();
    assert.equal(await worker.drain(), 1, 'the one rerun finishes stale again');
    restore();
    const marker = path.join(queueDir(repo), `${first.ticket}.done`);
    const before = readFileSync(marker);
    for (let join = 0; join < 2; join += 1) {
      const res = await cli(['review-file', '--wait-block', BLOCK, '--run', runId, '--max', '0ms'], repo);
      assert.deepEqual([res.code, res.json], [0, { block: BLOCK, files: [{ file: FILE, ticket: first.ticket, status: 'done', outcome: 'unavailable' }], done: true }]);
      assert.equal(await worker.drain(), 0, `join ${join + 2} schedules zero additional reviews`);
      assert.deepEqual(readFileSync(marker), before, 'second stale result stays byte-for-byte');
    }
    assert.equal(readResult(repo, runId, first.ticket).status, 'stale');
    assert.deepEqual(sessions, [], 'zero sessions for either stale attempt and zero for later joins');
    assert.deepEqual(queued(repo, 'json'), [first.ticket]);
  } finally { retractWorker(repo, process.pid); }
});

test('B125 boundary: a counted result arriving under the claim is restored; concurrent joins rerun only once', async () => {
  const { repo, runId, sessions, worker, commit } = await scriptedWatch();
  announceWorker(repo, { pid: process.pid, run: runId });
  try {
    const first = await commit('export const a = 1;\n');
    const cache = async (result) => writeResult({ repoRoot: repo, runId, ticket: first.ticket, key: await loadKey(runId), result });
    await cache({ status: 'stale' });
    let claims = 0;
    const key = await loadKey(runId);
    const joined = await waitBlock(repo, runId, ['--max', '0ms'], { dropDone: (root, ticket, seen, io) => {
      claims += 1;
      writeResult({ repoRoot: repo, runId, ticket, key, result: { status: 'reviewed', approved: true, findings: [] } });
      return dropDoneIf(root, ticket, seen, io);
    } });
    assert.deepEqual([claims, joined.json.done, await worker.drain(), sessions], [1, true, 0, []], 'one changed claim restores approval and starts zero sessions');
    assert.equal(readResult(repo, runId, first.ticket).approved, true);
    // A failed byte comparison must not spend the one successful drop.
    await cache({ status: 'stale' });
    const joins = await Promise.all([waitBlock(repo, runId, ['--max', '0ms']), waitBlock(repo, runId, ['--max', '0ms'])]);
    assert.deepEqual(joins.map((r) => [r.json.done, r.json.files[0].status]), [[false, 'pending'], [false, 'pending']]);
    assert.deepEqual([await worker.drain(), await worker.drain(), sessions], [1, 0, [first.content_hash]], 'two joins produce exactly one reviewer session for current content');
    assert.equal((await waitBlock(repo, runId)).json.done, true);
  } finally { retractWorker(repo, process.pid); }
});
