import assert from 'node:assert/strict';
import { test, after } from 'node:test';
import { linkSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv, contentHash, hashBytes, ticketId, DELETED_HASH } = await import('../../src/worker/ticket.mjs');
const { enqueue, blockTickets, pendingTickets, queueDir, readResult, announceWorker, retractWorker, isDone, supersedeTicket } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { startRun, readRun, saveRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, signRow } = await import('../../src/state/signer.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { checkBlockReviews } = await import('../../src/review/gate-check.mjs');
const { validateConfig, reviewFileLoop } = await import('../../src/config/validate.mjs');
const { watchTick } = await import('../../src/worker/watch.mjs');
const parent = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'b104-')));
after(() => rmSync(parent, { recursive: true, force: true }));
let seq = 0;
const BLOCK = 'B104';
const WATCH = { version: 1, provider: 'anthropic', levels: {
  L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' },
  L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' },
}, review: { file_loop: 'watch' } };
const dir = () => { const d = path.join(parent, String(++seq)); mkdirSync(d); return d; };
async function git(repo, ...argv) {
  const res = await exec(['git', ...argv], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
  assert.equal(res.result, 'ok', res.stderr);
  return res.stdout.trim();
}
/** @param {{cfg?: Record<string, any>, tree?: boolean, review?: any, deps?: Record<string, any>}} [options] */
async function fixture({ cfg = WATCH, tree = false, review = undefined, deps = {} } = {}) {
  const repo = dir();
  await git(repo, 'init', '-q');
  await git(repo, '-c', 'user.name=Fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'base', '--allow-empty');
  const base = await git(repo, 'rev-parse', 'HEAD');
  const runId = `r-b104-${process.pid}-${++seq}`;
  const rows = [];
  const writeRow = async (row) => { rows.push(row); };
  await startRun({ workspace: repo, project: 'b104', runId, config: cfg, writeRow });
  await openBlock({ runId, id: BLOCK, level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'watch', tests: ['watch'] }], writeRow });
  let root = repo;
  if (tree) {
    root = path.join(parent, `tree-${++seq}`);
    await git(repo, 'worktree', 'add', '-qb', `b104-${seq}`, root, base);
    const record = await readRun(runId);
    record.blocks[BLOCK].worktree = { path: root, base_sha: base, branch: `b104-${seq}` };
    await saveRun(record);
  }
  mkdirSync(path.join(root, 'src'));
  const sessions = [];
  const worker = await createWorker({ runId, repoRoot: repo, cfg, runRootDir: dir(), slug: 'b104', key: await loadKey(runId), pollMs: 1 }, {
    store: await createKeyStore({ backends: [], dir: dir() }), env: {}, writeRow, expiryCheck: async () => {},
    review: async (ticket, ctx) => { sessions.push(ticket.ticket); return review ? review(ticket, ctx) : { status: 'reviewed', approved: true, sessions: [] }; },
    ...deps,
  });
  const opts = { repoRoot: repo, run: runId, block: BLOCK, ...(tree ? { tree: root } : {}) };
  const tickets = () => blockTickets(opts);
  const tick = async (extra = {}) => {
    assert.equal(typeof watchTick, 'function', 'watchTick must queue committed owned files');
    return watchTick({ repoRoot: repo, runId, cfg, writeRow, ...extra });
  };
  const commit = async (files) => {
    for (const [file, text] of Object.entries(files)) writeFileSync(path.join(root, file), text);
    await git(root, 'add', '--', ...Object.keys(files));
    await git(root, '-c', 'user.name=Fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'green');
    return git(root, 'rev-parse', 'HEAD');
  };
  return { repo, root, runId, base, rows, sessions, worker, opts, tickets, tick, commit };
}
const marker = (p, id) => JSON.parse(readFileSync(path.join(queueDir(p.repo), `${id}.done`), 'utf8'));
const skips = (p) => p.rows.filter((r) => r.event === 'watch.skipped').map(({ block, reason }) => ({ block, reason }));
const currentId = (p, file) => ticketId({ run: p.runId, block: BLOCK, file, content_hash: contentHash(p.root, file), tree: p.opts.tree ?? null });
async function cli(p, argv) {
  let out = ''; let err = '';
  announceWorker(p.repo, { pid: process.pid, run: p.runId });
  let code;
  try {
    code = await runReviewFile(argv, { cwd: p.root, pollMs: 1, stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } } });
  } finally { retractWorker(p.repo, process.pid); }
  assert.equal(err, '');
  assert.equal(out.trim().split('\n').length, 1);
  return { code, json: JSON.parse(out) };
}

test('(1) config accepts watch and preserves wait by default', () => {
  assert.deepEqual([validateConfig(WATCH).errors, reviewFileLoop(WATCH), reviewFileLoop({})], [[], 'watch', 'wait']);
});
for (const tree of [false, true]) test(`(1) two committed files have review-file ids; drain watches ${tree ? 'worktree' : 'repo'} HEAD`, async () => {
  const p = await fixture({ tree });
  await p.commit({ 'src/a.mjs': 'export const a = 1;\n', 'src/b.mjs': 'export const b = 1;\n' });
  assert.equal(await p.worker.drain(), 2);
  assert.equal(p.tickets().length, 2);
  for (const file of ['src/a.mjs', 'src/b.mjs']) {
    const res = await cli(p, [file, '--block', BLOCK, '--run', p.runId]);
    assert.equal(res.code, 0);
    assert.equal(res.json.ticket, p.tickets().find((t) => t.file === file).ticket);
  }
  assert.deepEqual([p.sessions.length, p.tickets().length, pendingTickets(p.repo)], [2, 2, []]);
});
test('(2) later commits are held once per file per head and supersede older pending tickets', async () => {
  const p = await fixture();
  const files = ['src/a.mjs', 'src/b.mjs'];
  await p.commit(Object.fromEntries(files.map((file) => [file, 'old\n']))); await p.tick();
  const old = p.tickets();
  const expected = [];
  for (const text of ['middle\n', 'latest\n']) {
    await p.commit(Object.fromEntries(files.map((file) => [file, text])));
    await p.tick(); await p.tick();
    expected.push(...files.map((file) => ({ event: 'watch.held', block: BLOCK, file, content_hash: hashBytes(Buffer.from(text)) })));
    assert.deepEqual(p.tickets(), old, 'held content must create zero new tickets for either file');
    assert.deepEqual(pendingTickets(p.repo), [], 'both older pending tickets must be superseded');
    assert.deepEqual(p.rows.filter((row) => row.event === 'watch.held'), expected, 'exactly one held row per file per new head');
  }
  for (const { ticket } of old) assert.deepEqual(marker(p, ticket), { ticket, status: 'refused', reason: 'superseded' });
  assert.equal(await p.worker.drain(), 0);
  assert.deepEqual(p.sessions, []);
});
test('three held fix commits join the latest content once through --wait-block', async () => {
  const reviews = [];
  const p = await fixture({ review: async (ticket) => {
    reviews.push({ file: ticket.file, content_hash: ticket.content_hash });
    return { status: 'reviewed', approved: true, sessions: [] };
  } });
  await p.commit({ 'src/a.mjs': 'initial\n' }); await p.tick();
  const first = currentId(p, 'src/a.mjs');
  assert.equal(await p.worker.drain(), 1);
  for (const text of ['fix one\n', 'fix two\n', 'fix three\n']) {
    await p.commit({ 'src/a.mjs': text }); await p.tick();
    assert.equal(await p.worker.drain(), 0, 'no fix review may start before the join');
    assert.deepEqual(p.tickets().map((ticket) => ticket.ticket), [first]);
  }
  const latest = currentId(p, 'src/a.mjs');
  const joined = await cli(p, ['--wait-block', BLOCK, '--run', p.runId, '--max', '0ms']);
  assert.deepEqual(joined, { code: 0, json: { block: BLOCK, files: [{ file: 'src/a.mjs', ticket: latest, status: 'pending', outcome: null }], done: false, reason: 'wait_timeout' } });
  assert.deepEqual(p.tickets().map((ticket) => ticket.ticket).sort(), [first, latest].sort());
  assert.deepEqual(pendingTickets(p.repo), [latest]);
  assert.equal(await p.worker.drain(), 1);
  assert.deepEqual(reviews, [
    { file: 'src/a.mjs', content_hash: hashBytes(Buffer.from('initial\n')) },
    { file: 'src/a.mjs', content_hash: hashBytes(Buffer.from('fix three\n')) },
  ], 'one initial session and exactly one fix session for this file, none for middle commits');
  assert.deepEqual(await cli(p, ['--wait-block', BLOCK, '--run', p.runId]), { code: 0, json: { block: BLOCK, files: [{ file: 'src/a.mjs', ticket: latest, status: 'done', outcome: 'approved' }], done: true } });
  assert.equal(await p.worker.drain(), 0);
  assert.deepEqual(p.sessions, [first, latest]);
});
test('(3) commit after head read queues that head, then supersedes it on the next tick; old approval cannot pass', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'first\n' });
  const firstHash = contentHash(p.root, 'src/a.mjs');
  let calls = 0;
  await p.tick({ hooks: { afterHead: async () => { calls++; await p.commit({ 'src/a.mjs': 'second\n' }); } } });
  assert.equal(calls, 1);
  const old = p.tickets()[0]; assert.equal(old.content_hash, firstHash);
  await p.tick();
  const newest = currentId(p, 'src/a.mjs');
  assert.deepEqual(pendingTickets(p.repo), []);
  assert.deepEqual(p.tickets().map((ticket) => ticket.ticket), [old.ticket]);
  assert.deepEqual(marker(p, old.ticket), { ticket: old.ticket, status: 'refused', reason: 'superseded' });
  const key = await loadKey(p.runId);
  const gate = checkBlockReviews({ block: BLOCK, runId: p.runId, files: [{ file: old.file, content_hash: contentHash(p.root, old.file) }], rows: [signRow({ run: p.runId, event: 'review.approved', block: BLOCK, file: old.file, content_hash: old.content_hash }, key)], key, proofFiles: [], highPaths: [] });
  assert.deepEqual(gate.refusals.map(({ code, file }) => ({ code, file })), [{ code: 'unreviewed', file: 'src/a.mjs' }]);
  assert.equal(gate.ok, false);
  await cli(p, ['--wait-block', BLOCK, '--run', p.runId, '--max', '0ms']);
  assert.equal(await p.worker.drain(), 1);
  assert.deepEqual(p.sessions, [newest]);
});
test('(4) coder enqueue during watch queues one ticket and runs one session', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'same\n' });
  let id; let calls = 0;
  await p.tick({ hooks: { beforeQueue: async () => { calls++; id = (await cli(p, ['src/a.mjs', '--block', BLOCK, '--run', p.runId])).json.ticket; } } });
  assert.equal(calls, 1);
  assert.deepEqual(p.tickets().map((t) => t.ticket), [id]);
  assert.equal(await p.worker.drain(), 1);
  assert.deepEqual(p.sessions, [id]);
});
test('(5) unowned change is ignored and logged once; hand enqueue still works', async () => {
  const p = await fixture();
  await p.commit({ 'outside.mjs': 'other\n' });
  await p.tick(); await p.tick();
  assert.deepEqual([p.tickets(), skips(p)], [[], [{ block: BLOCK, reason: 'unowned_path' }]]);
  mkdirSync(path.join(p.root, 'src'), { recursive: true });
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'manual\n');
  assert.equal((await cli(p, ['src/a.mjs', '--block', BLOCK, '--run', p.runId])).code, 0);
  assert.equal(await p.worker.drain(), 1);
});
for (const reason of ['not_descendant', 'rewound', 'unreadable_head']) test(`(5) ${reason} fails closed once and manual review works`, async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'first\n' });
  if (reason === 'not_descendant') {
    await git(p.root, 'checkout', '--orphan', 'foreign');
    await git(p.root, '-c', 'user.name=Fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'foreign');
  } else if (reason === 'rewound') {
    await p.tick();
    await git(p.root, 'reset', '--hard', p.base);
  }
  const before = p.tickets().length;
  const extra = reason === 'unreadable_head' ? { exec: async () => ({ result: 'failed', stdout: '', stderr: '', code: 128 }) } : {};
  await p.tick(extra); await p.tick(extra);
  assert.equal(p.tickets().length, before);
  assert.deepEqual(skips(p), [{ block: BLOCK, reason }]);
  mkdirSync(path.join(p.root, 'src'), { recursive: true });
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'manual\n');
  const res = await cli(p, ['src/a.mjs', '--block', BLOCK, '--run', p.runId]);
  assert.equal(res.code, 0);
  const record = await readRun(p.runId); assert.equal(record.blocks[BLOCK].status, 'open');
  // The normal worker processing path remains available independently of the watch skip.
  assert.equal((await p.worker.processTicket(res.json.ticket)).status, 'reviewed');
  assert.equal(readResult(p.repo, p.runId, res.json.ticket).approved, true);
});
test('(6) missing or unreadable watch state restarts at base without duplicate sessions', async () => {
  const p = await fixture();
  const head = await p.commit({ 'src/a.mjs': 'once\n' });
  assert.equal(await p.worker.drain(), 1);
  const state = path.join(queueDir(p.repo), 'watch', `${BLOCK}.json`);
  assert.equal(JSON.parse(readFileSync(state, 'utf8')).sha, head);
  rmSync(state); assert.equal(await p.worker.drain(), 0);
  writeFileSync(state, '{bad'); assert.equal(await p.worker.drain(), 0);
  assert.deepEqual([p.tickets().length, p.sessions.length], [1, 1]);
});
test('(7) --fresh refuses superseded bytes exactly and preserves their marker', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'old\n' }); await p.tick();
  const old = p.tickets()[0].ticket;
  await p.commit({ 'src/a.mjs': 'new\n' }); await p.tick();
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'old\n');
  const res = await cli(p, ['src/a.mjs', '--block', BLOCK, '--run', p.runId, '--fresh']);
  assert.deepEqual(res, { code: 1, json: { status: 'refused', reason: 'fresh-needs-reopen', ticket: old, file: 'src/a.mjs', exits: ['block reopen', 'block waive'], message: "fresh-needs-reopen: the cached result of src/a.mjs counts as a review (or cannot be read, or kept changing under the claim); --fresh re-runs only stale, unavailable, round-1 split_required or refused other-run/worktree-*/context-unreadable results; the human's ways out are block reopen and block waive; the cached result was kept" } });
  assert.deepEqual(marker(p, old), { ticket: old, status: 'refused', reason: 'superseded' });
});
test('(8) watch setting reaches exec spy; without setting idle drains do no watch reads or git work', async () => {
  let calls = 0; let reads = 0;
  const spy = async (argv, options) => { calls++; return exec(argv, options); };
  const readSpy = async (id) => { reads++; return readRun(id); };
  const setup = (cfg) => fixture({ cfg, deps: { watchExec: spy, readRun: readSpy } });
  const positive = await setup(WATCH);
  for (let i = 0; i < 3; i++) assert.equal(await positive.worker.drain(), 0);
  assert.ok(calls > 0, 'the same worker setup with watch must reach the exec seam');
  assert.deepEqual([calls, reads], [6, 3]);
  const p = await setup({ version: 1, provider: 'anthropic' });
  calls = 0; reads = 0;
  await p.tick({ exec: spy, readRun: readSpy });
  for (let i = 0; i < 3; i++) assert.equal(await p.worker.drain(), 0);
  assert.deepEqual([calls, reads, readdirSync(p.repo).includes('.code-forge')], [0, 0, true]);
});
test('held commits preserve running and done tickets while superseding a later pending ticket', async () => {
  let p; let completed;
  p = await fixture({ review: async (ticket) => {
    if (ticket.file === 'src/a.mjs') {
      await p.commit({ 'src/a.mjs': 'second\n', 'src/b.mjs': 'second\n' });
      await p.tick({ running: new Set([ticket.ticket]) });
      assert.deepEqual(pendingTickets(p.repo), [ticket.ticket], 'held content must not queue alongside the running review');
      const second = enqueue({ ...p.opts, file: 'src/a.mjs' }).ticket;
      await p.commit({ 'src/a.mjs': 'third\n' }); await p.tick({ running: new Set([ticket.ticket]) });
      assert.deepEqual(pendingTickets(p.repo), [ticket.ticket]);
      assert.deepEqual(marker(p, second), { ticket: second, status: 'refused', reason: 'superseded' });
      assert.equal(isDone(p.repo, ticket.ticket), false);
      assert.equal(readResult(p.repo, p.runId, ticket.ticket), null);
      assert.deepEqual(marker(p, completed.ticket), completed.marker, 'completed ticket must keep its real result');
    }
    return { status: 'reviewed', approved: true, sessions: [] };
  } });
  await p.commit({ 'src/a.mjs': 'first\n', 'src/b.mjs': 'first\n' }); await p.tick();
  const first = currentId(p, 'src/a.mjs');
  const done = currentId(p, 'src/b.mjs');
  assert.equal((await p.worker.processTicket(done)).status, 'reviewed');
  completed = { ticket: done, marker: marker(p, done) };
  assert.equal(await p.worker.drain(), 1);
  assert.deepEqual(p.sessions, [done, first]);
  assert.equal(readResult(p.repo, p.runId, first).approved, true);
});
test('watch hashes exact committed binary bytes and deletions, ignores dirty content', async () => {
  const p = await fixture();
  const bytes = Buffer.from([0, 255, 128, 10]);
  await p.commit({ 'src/a.mjs': bytes });
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'dirty');
  await p.tick();
  const binary = ticketId({ run: p.runId, block: BLOCK, file: 'src/a.mjs', content_hash: hashBytes(bytes) });
  assert.deepEqual(p.tickets().map(({ ticket, content_hash }) => ({ ticket, content_hash })), [{ ticket: binary, content_hash: hashBytes(bytes) }]);
  await git(p.root, 'rm', '-f', 'src/a.mjs');
  await git(p.root, '-c', 'user.name=Fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'delete');
  await p.tick();
  assert.deepEqual(p.tickets().map(({ ticket, content_hash }) => ({ ticket, content_hash })), [{ ticket: binary, content_hash: hashBytes(bytes) }]);
  assert.deepEqual(p.rows.filter((row) => row.event === 'watch.held'), [{ event: 'watch.held', block: BLOCK, file: 'src/a.mjs', content_hash: DELETED_HASH }]);
  assert.deepEqual(marker(p, binary), { ticket: binary, status: 'refused', reason: 'superseded' });
  assert.deepEqual(pendingTickets(p.repo), []);
  assert.equal(await p.worker.drain(), 0);
  assert.deepEqual(p.sessions, []);
});

test('returning A after held B keeps it superseded until manual enqueue reviews A once', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'A\n' }); await p.tick();
  const a = currentId(p, 'src/a.mjs');
  await p.commit({ 'src/a.mjs': 'B\n' }); await p.tick();
  assert.deepEqual(p.tickets().map((t) => t.ticket), [a]);
  assert.deepEqual(marker(p, a), { ticket: a, status: 'refused', reason: 'superseded' });
  await p.commit({ 'src/a.mjs': 'A\n' }); await p.tick();
  assert.deepEqual(p.tickets().map((t) => t.ticket), [a]);
  assert.deepEqual([isDone(p.repo, a), pendingTickets(p.repo)], [true, []]);
  assert.equal(enqueue({ ...p.opts, file: 'src/a.mjs' }).status, 'queued');
  assert.equal(await p.worker.drain(), 1);
  assert.deepEqual(p.sessions, [a]);
  assert.equal(readResult(p.repo, p.runId, a).approved, true);
  const done = marker(p, a);
  // A restart at base and a manual enqueue both preserve a real result.
  rmSync(path.join(queueDir(p.repo), 'watch', `${BLOCK}.json`));
  assert.equal(await p.worker.drain(), 0);
  assert.equal(enqueue({ ...p.opts, file: 'src/a.mjs' }).status, 'done');
  assert.deepEqual(marker(p, a), done);
  assert.deepEqual(p.sessions, [a]);
  assert.deepEqual(await cli(p, ['--wait-block', BLOCK, '--run', p.runId]), { code: 0, json: { block: BLOCK, files: [{ file: 'src/a.mjs', ticket: a, status: 'done', outcome: 'approved' }], done: true } });
});

test('manual enqueue revives superseded current content and keeps real results', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'A\n' }); await p.tick();
  const a = currentId(p, 'src/a.mjs');
  await p.commit({ 'src/a.mjs': 'B\n' }); await p.tick();
  assert.deepEqual(p.tickets().map((t) => t.ticket), [a]);
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'A\n');
  assert.deepEqual(enqueue({ ...p.opts, file: 'src/a.mjs' }), { ticket: a, status: 'queued', file: 'src/a.mjs', content_hash: hashBytes(Buffer.from('A\n')) });
  assert.deepEqual(pendingTickets(p.repo), [a]);
  assert.equal((await p.worker.processTicket(a)).status, 'reviewed');
  const done = marker(p, a);
  assert.equal(enqueue({ ...p.opts, file: 'src/a.mjs' }).status, 'done');
  assert.deepEqual(marker(p, a), done);
  assert.deepEqual(p.sessions, [a]);
});

test('rewound tick queues nothing, then new commit restarts at base without repeat reviews', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'first\n' });
  const a = currentId(p, 'src/a.mjs');
  assert.equal(await p.worker.drain(), 1);
  await git(p.root, 'reset', '--hard', p.base);
  await p.tick();
  assert.deepEqual(p.tickets().map((t) => t.ticket), [a]);
  assert.deepEqual(pendingTickets(p.repo), []);
  assert.deepEqual(skips(p), [{ block: BLOCK, reason: 'rewound' }]);
  mkdirSync(path.join(p.root, 'src'), { recursive: true });
  await p.commit({ 'src/a.mjs': 'first\n', 'src/b.mjs': 'new branch\n' });
  const b = currentId(p, 'src/b.mjs');
  await p.tick();
  assert.deepEqual(p.tickets().map((t) => t.ticket).sort(), [a, b].sort());
  assert.deepEqual(pendingTickets(p.repo), [b]);
  assert.equal(await p.worker.drain(), 1);
  assert.equal(await p.worker.drain(), 0);
  assert.deepEqual(p.sessions, [a, b]);
  assert.deepEqual(skips(p), [{ block: BLOCK, reason: 'rewound' }]);
});

test('an owned path that is never readable (a symlink) writes ONE unreadable_head row over many ticks', async () => {
  const p = await fixture();
  symlinkSync('elsewhere', path.join(p.root, 'src/link.mjs'));
  await p.commit({ 'src/a.mjs': 'export const a = 1;\n' });
  await git(p.root, 'add', '--', 'src/link.mjs');
  await git(p.root, '-c', 'user.name=Fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'link');
  for (let i = 0; i < 4; i += 1) await p.tick();
  assert.deepEqual(skips(p), [{ block: BLOCK, reason: 'unreadable_head' }]);
  assert.deepEqual(p.tickets(), []);
});

test('healthy unchanged head clears skip reasons so the next failure logs again', async () => {
  const p = await fixture();
  await p.tick();
  const failure = { exec: async () => ({ result: 'failed', stdout: '', stderr: '', code: 128 }) };
  await p.tick(failure); await p.tick(failure);
  assert.deepEqual(skips(p), [{ block: BLOCK, reason: 'unreadable_head' }]);
  await p.tick();
  await p.tick(failure); await p.tick(failure);
  assert.deepEqual(skips(p), [{ block: BLOCK, reason: 'unreadable_head' }, { block: BLOCK, reason: 'unreadable_head' }]);
  assert.deepEqual([p.tickets(), p.sessions], [[], []]);
});

test('rejected watch row writer warns once and dispatches the pending ticket', async () => {
  let attempts = 0; let warnings = '';
  const p = await fixture({ deps: {
    watchExec: async () => ({ result: 'failed', stdout: '', stderr: '', code: 128 }),
    writeRow: async (row) => { if (row.event === 'watch.skipped' && ++attempts <= 2) throw new Error('row writer unavailable'); },
    stderr: { write: (text) => { warnings += text; } },
  } });
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'manual\n');
  const id = enqueue({ ...p.opts, file: 'src/a.mjs' }).ticket;
  assert.equal(await p.worker.drain(), 1);
  assert.deepEqual(p.sessions, [id]);
  assert.equal(readResult(p.repo, p.runId, id).approved, true);
  assert.equal(attempts, 3);
  assert.equal(warnings, 'worker: WARN watch tick failed (Error)\n');
});

test('superseded marker is invisible until complete atomic publication', async () => {
  const p = await fixture();
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'A\n');
  const id = enqueue({ ...p.opts, file: 'src/a.mjs' }).ticket;
  const expected = { ticket: id, status: 'refused', reason: 'superseded' };
  let writes = 0; let links = 0;
  supersedeTicket(p.repo, id, {
    write: (file, text) => {
      writes++; writeFileSync(file, '');
      assert.deepEqual([isDone(p.repo, id), pendingTickets(p.repo)], [false, [id]]);
      writeFileSync(file, text);
      assert.equal(isDone(p.repo, id), false);
    },
    link: (from, to) => {
      links++;
      assert.deepEqual(JSON.parse(readFileSync(from, 'utf8')), expected);
      assert.equal(isDone(p.repo, id), false);
      linkSync(from, to);
      assert.deepEqual(marker(p, id), expected);
    },
  });
  assert.deepEqual([writes, links, marker(p, id), pendingTickets(p.repo)], [1, 1, expected, []]);
  assert.deepEqual(readdirSync(queueDir(p.repo)).sort(), [`${id}.done`, `${id}.json`]);
});

test('supersede publication preserves a concurrently published real marker on EEXIST', async () => {
  const p = await fixture();
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'A\n');
  const id = enqueue({ ...p.opts, file: 'src/a.mjs' }).ticket;
  const expected = { ticket: id, status: 'reviewed', result: `reviews/${p.runId}/${id}.json` };
  let links = 0;
  supersedeTicket(p.repo, id, { link: (from, to) => {
    links++; writeFileSync(to, `${JSON.stringify(expected)}\n`);
    linkSync(from, to);
  } });
  assert.deepEqual([links, marker(p, id), pendingTickets(p.repo)], [1, expected, []]);
  supersedeTicket(p.repo, id);
  assert.deepEqual(marker(p, id), expected);
  assert.deepEqual(readdirSync(queueDir(p.repo)).sort(), [`${id}.done`, `${id}.json`]);
});
