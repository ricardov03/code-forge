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
  // Default ticks advance one quiet period; worker drains use the later real clock.
  let time = Date.now() - 3600000;
  const tick = async (extra = {}) => {
    assert.equal(typeof watchTick, 'function', 'watchTick must queue committed owned files');
    time += 15000;
    return watchTick({ repoRoot: repo, runId, cfg, writeRow, now: () => time, ...extra });
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
const timingsOf = (p, id) => {
  const result = readResult(p.repo, p.runId, id);
  return Object.fromEntries(['queued_at', 'started_at', 'finished_at', 'duration_ms'].map((key) => [key, result?.[key]]));
};
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
test('a three-commit burst queues exactly one ticket per changed file for the last HEAD', async () => {
  const p = await fixture();
  const files = ['src/a.mjs', 'src/b.mjs'];
  for (const [index, text] of ['first\n', 'middle\n', 'latest\n'].entries()) {
    await p.commit(Object.fromEntries(files.map((file) => [file, text])));
    await p.tick({ now: () => index * 5000 });
    assert.deepEqual(p.tickets(), [], `commit ${index + 1} must queue zero tickets during the burst`);
  }
  await p.tick({ now: () => 24999 });
  assert.deepEqual(p.tickets(), [], 'zero tickets before the latest HEAD has been quiet for 15 seconds');
  await p.tick({ now: () => 25000 });
  assert.deepEqual(p.tickets().map(({ file, content_hash }) => ({ file, content_hash })).sort((a, b) => a.file.localeCompare(b.file)),
    files.map((file) => ({ file, content_hash: hashBytes(Buffer.from('latest\n')) })), 'exactly two tickets, both with the last committed content');
  assert.equal(await p.worker.drain(), 2);
  assert.deepEqual(p.sessions.slice().sort(), files.map((file) => currentId(p, file)).sort());
});
test('one commit queues only on a later tick at the 15-second quiet boundary', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'single\n' });
  await p.tick({ now: () => 0 });
  assert.deepEqual(p.tickets(), [], 'the first observation must queue zero tickets');
  await p.tick({ now: () => 14999 });
  assert.deepEqual(p.tickets(), [], '14,999 ms of quiet must queue zero tickets');
  await p.tick({ now: () => 15000 });
  assert.deepEqual(p.tickets().map(({ ticket, content_hash }) => ({ ticket, content_hash })),
    [{ ticket: currentId(p, 'src/a.mjs'), content_hash: hashBytes(Buffer.from('single\n')) }], 'exactly one ticket at 15,000 ms of quiet');
  await p.tick({ now: () => 30000 });
  assert.equal(await p.worker.drain(), 1);
  assert.deepEqual(p.sessions, [currentId(p, 'src/a.mjs')]);
});
test('commits every 10 seconds queue the current HEAD by 120 seconds and continue for three minutes', async () => {
  const p = await fixture();
  let first;
  for (let elapsed = 0; elapsed <= 180000; elapsed += 10000) {
    await p.commit({ 'src/a.mjs': `${elapsed}\n` });
    await p.tick({ now: () => elapsed });
    if (elapsed < 120000) assert.deepEqual(p.tickets(), [], `zero tickets before the burst limit at ${elapsed} ms`);
    else {
      first ??= currentId(p, 'src/a.mjs');
      assert.deepEqual(p.tickets().map(({ ticket, content_hash }) => ({ ticket, content_hash })),
        [{ ticket: first, content_hash: hashBytes(Buffer.from('120000\n')) }], `exactly one ticket from the HEAD at the 120-second limit at ${elapsed} ms`);
    }
  }
  await p.tick({ now: () => 195000 });
  assert.deepEqual(p.rows.filter((row) => row.event === 'watch.held'),
    [{ event: 'watch.held', block: BLOCK, file: 'src/a.mjs', content_hash: hashBytes(Buffer.from('180000\n')) }]);
  assert.deepEqual(marker(p, first), { ticket: first, status: 'refused', reason: 'superseded' });
});
test('a rewound HEAD during pending writes one rewound skip and queues nothing', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'pending\n' });
  await p.tick({ now: () => 0 });
  await git(p.root, 'reset', '--hard', p.base);
  await p.tick({ now: () => 5000 });
  await p.tick({ now: () => 20000 });
  assert.deepEqual(skips(p), [{ block: BLOCK, reason: 'rewound' }]);
  assert.deepEqual(p.tickets(), [], 'the abandoned pending HEAD must create zero tickets');
  mkdirSync(path.join(p.root, 'src'), { recursive: true });
  await p.commit({ 'src/a.mjs': 'replacement\n' });
  await p.tick({ now: () => 120000 });
  assert.deepEqual(p.tickets(), [], 'a fresh burst after rewind must start its own quiet wait');
  await p.tick({ now: () => 135000 });
  assert.deepEqual(p.tickets().map(({ ticket, content_hash }) => ({ ticket, content_hash })),
    [{ ticket: currentId(p, 'src/a.mjs'), content_hash: hashBytes(Buffer.from('replacement\n')) }]);
});
for (const tree of [false, true]) test(`(1) two committed files have review-file ids; drain watches ${tree ? 'worktree' : 'repo'} HEAD`, async () => {
  const p = await fixture({ tree });
  await p.commit({ 'src/a.mjs': 'export const a = 1;\n', 'src/b.mjs': 'export const b = 1;\n' });
  await p.tick();
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
  await p.commit(Object.fromEntries(files.map((file) => [file, 'old\n']))); await p.tick(); await p.tick();
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
  await p.commit({ 'src/a.mjs': 'initial\n' }); await p.tick(); await p.tick();
  const first = currentId(p, 'src/a.mjs');
  assert.equal(await p.worker.drain(), 1);
  for (const text of ['fix one\n', 'fix two\n', 'fix three\n']) {
    await p.commit({ 'src/a.mjs': text }); await p.tick(); await p.tick();
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
  assert.deepEqual(await cli(p, ['--wait-block', BLOCK, '--run', p.runId]), { code: 0, json: { block: BLOCK, files: [{ file: 'src/a.mjs', ticket: latest, status: 'done', outcome: 'approved', ...timingsOf(p, latest) }], done: true } });
  assert.equal(await p.worker.drain(), 0);
  assert.deepEqual(p.sessions, [first, latest]);
});
test('(3) commit after head read queues that head, then supersedes it on the next tick; old approval cannot pass', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'first\n' });
  const firstHash = contentHash(p.root, 'src/a.mjs');
  await p.tick();
  let calls = 0;
  await p.tick({ hooks: { afterHead: async () => { calls++; await p.commit({ 'src/a.mjs': 'second\n' }); } } });
  assert.equal(calls, 1);
  const old = p.tickets()[0]; assert.equal(old.content_hash, firstHash);
  await p.tick(); await p.tick();
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
  await p.tick();
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
    await p.tick(); await p.tick();
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
  await p.tick();
  assert.equal(await p.worker.drain(), 1);
  const state = path.join(queueDir(p.repo), 'watch', `${BLOCK}.json`);
  assert.equal(JSON.parse(readFileSync(state, 'utf8')).sha, head);
  rmSync(state); await p.tick(); assert.equal(await p.worker.drain(), 0);
  writeFileSync(state, '{bad'); await p.tick(); assert.equal(await p.worker.drain(), 0);
  assert.deepEqual([p.tickets().length, p.sessions.length], [1, 1]);
});
test('(7) --fresh refuses superseded bytes exactly and preserves their marker', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'old\n' }); await p.tick(); await p.tick();
  const old = p.tickets()[0].ticket;
  await p.commit({ 'src/a.mjs': 'new\n' }); await p.tick(); await p.tick();
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
test('worker and manual watch ticks share the quiet deadline before superseding held content', async () => {
  let now = 0;
  const p = await fixture({ deps: { watchNow: () => now } });
  const tick = () => p.tick({ now: () => now });
  await p.commit({ 'src/a.mjs': 'first\n' });
  await tick(); now = 15000; await tick();
  const first = p.tickets()[0].ticket;
  await p.commit({ 'src/a.mjs': 'second\n' });
  // The scheduler observes this HEAD first. Manual ticks must use the same deadline.
  now = 30000; await p.worker.watchTick();
  now = 44999; await tick();
  assert.deepEqual(pendingTickets(p.repo), [first], 'the older ticket remains pending at 14,999 ms');
  now = 45000; await tick();
  assert.deepEqual(pendingTickets(p.repo), [], 'the older pending ticket is superseded at exactly 15,000 ms');
  assert.deepEqual(marker(p, first), { ticket: first, status: 'refused', reason: 'superseded' });
  assert.deepEqual(p.tickets().map(({ ticket }) => ticket), [first], 'held content queues zero additional tickets');
  assert.deepEqual(p.rows.filter(({ event }) => event === 'watch.held').map(({ file, content_hash }) => ({ file, content_hash })),
    [{ file: 'src/a.mjs', content_hash: hashBytes(Buffer.from('second\n')) }], 'exactly one held observation at the shared quiet deadline');
});
test('overlapping watch ticks preserve the same state and queue as two serial ticks', async () => {
  const exercise = async (overlap) => {
    const p = await fixture();
    const head = await p.commit({ 'src/a.mjs': 'first\n' });
    let calls = 0; let reads = 0;
    // Resolve the initial reads immediately so the unprotected second tick deterministically
    // saves its pending state while the first is paused. Object reads still use real git.
    const execute = async (argv, options) => {
      calls++;
      if (argv[1] === 'rev-parse') return { result: 'ok', stdout: `${head}\n`, stderr: '', code: 0 };
      if (argv[1] === 'merge-base') return { result: 'ok', stdout: '', stderr: '', code: 0 };
      return exec(argv, options);
    };
    let started;
    const secondStarted = new Promise((resolve) => { started = resolve; });
    const loadRun = async (id) => {
      const record = await readRun(id);
      if (++reads === 2) started();
      return record;
    };
    const tick = (time, hooks = {}) => p.tick({ now: () => time, exec: execute, readRun: loadRun, hooks });
    if (overlap) {
      let entered; let release;
      const firstEntered = new Promise((resolve) => { entered = resolve; });
      const held = new Promise((resolve) => { release = resolve; });
      const first = tick(0, { afterHead: async () => { entered(); await held; } });
      await firstEntered;
      const second = tick(15000);
      try {
        await secondStarted;
        await new Promise((resolve) => setImmediate(resolve));
      } finally { release(); }
      await Promise.all([first, second]);
    } else {
      await tick(0);
      await tick(15000);
    }
    const state = JSON.parse(readFileSync(path.join(queueDir(p.repo), 'watch', `${BLOCK}.json`), 'utf8'));
    return { p, head, state, calls, reads };
  };
  const serial = await exercise(false);
  const overlap = await exercise(true);
  assert.deepEqual(overlap.state, { ...serial.state, run: overlap.p.runId, base: overlap.p.base, sha: overlap.head });
  const queue = (p) => p.tickets().map(({ file, content_hash, ticket }) => ({ file, content_hash, pending: pendingTickets(p.repo).includes(ticket) }));
  assert.equal(queue(serial.p).length, 1);
  assert.deepEqual(queue(overlap.p), queue(serial.p));
  assert.deepEqual([overlap.calls, overlap.reads], [serial.calls, 2], 'serialization adds no git calls or run-record reads');
});
test('held commits preserve running and done tickets while superseding a later pending ticket', async () => {
  let p; let completed;
  let now = 0;
  const tick = async (extra = {}) => { now += 15000; await p.tick({ ...extra, now: () => now }); };
  let reviewFailure;
  p = await fixture({ deps: { watchNow: () => now }, review: async (ticket) => {
    try {
      if (ticket.file === 'src/a.mjs') {
        await p.commit({ 'src/a.mjs': 'second\n', 'src/b.mjs': 'second\n' });
        await tick({ running: new Set([ticket.ticket]) });
        await tick({ running: new Set([ticket.ticket]) });
        assert.deepEqual(pendingTickets(p.repo), [ticket.ticket], 'held content must not queue alongside the running review');
        const second = enqueue({ ...p.opts, file: 'src/a.mjs' }).ticket;
        await p.commit({ 'src/a.mjs': 'third\n' }); await tick({ running: new Set([ticket.ticket]) });
        await tick({ running: new Set([ticket.ticket]) });
        assert.deepEqual(pendingTickets(p.repo), [ticket.ticket]);
        assert.deepEqual(marker(p, second), { ticket: second, status: 'refused', reason: 'superseded' });
        assert.equal(isDone(p.repo, ticket.ticket), false);
        assert.equal(readResult(p.repo, p.runId, ticket.ticket), null);
        assert.deepEqual(marker(p, completed.ticket), completed.marker, 'completed ticket must keep its real result');
      }
      return { status: 'reviewed', approved: true, sessions: [] };
    } catch (err) { reviewFailure = err; throw err; }
  } });
  await p.commit({ 'src/a.mjs': 'first\n', 'src/b.mjs': 'first\n' }); await tick(); await tick();
  const first = currentId(p, 'src/a.mjs');
  const done = currentId(p, 'src/b.mjs');
  assert.equal((await p.worker.processTicket(done)).status, 'reviewed');
  completed = { ticket: done, marker: marker(p, done) };
  const drained = await p.worker.drain();
  assert.equal(reviewFailure, undefined, 'review callback assertions must reach the test runner');
  assert.equal(drained, 1);
  assert.deepEqual(p.sessions, [done, first]);
  assert.equal(readResult(p.repo, p.runId, first).approved, true);
});
test('watch hashes exact committed binary bytes and deletions, ignores dirty content', async () => {
  const p = await fixture();
  const bytes = Buffer.from([0, 255, 128, 10]);
  await p.commit({ 'src/a.mjs': bytes });
  writeFileSync(path.join(p.root, 'src/a.mjs'), 'dirty');
  await p.tick(); await p.tick();
  const binary = ticketId({ run: p.runId, block: BLOCK, file: 'src/a.mjs', content_hash: hashBytes(bytes) });
  assert.deepEqual(p.tickets().map(({ ticket, content_hash }) => ({ ticket, content_hash })), [{ ticket: binary, content_hash: hashBytes(bytes) }]);
  await git(p.root, 'rm', '-f', 'src/a.mjs');
  await git(p.root, '-c', 'user.name=Fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'delete');
  await p.tick(); await p.tick();
  assert.deepEqual(p.tickets().map(({ ticket, content_hash }) => ({ ticket, content_hash })), [{ ticket: binary, content_hash: hashBytes(bytes) }]);
  assert.deepEqual(p.rows.filter((row) => row.event === 'watch.held'), [{ event: 'watch.held', block: BLOCK, file: 'src/a.mjs', content_hash: DELETED_HASH }]);
  assert.deepEqual(marker(p, binary), { ticket: binary, status: 'refused', reason: 'superseded' });
  assert.deepEqual(pendingTickets(p.repo), []);
  assert.equal(await p.worker.drain(), 0);
  assert.deepEqual(p.sessions, []);
});

test('returning A after held B keeps it superseded until manual enqueue reviews A once', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'A\n' }); await p.tick(); await p.tick();
  const a = currentId(p, 'src/a.mjs');
  await p.commit({ 'src/a.mjs': 'B\n' }); await p.tick(); await p.tick();
  assert.deepEqual(p.tickets().map((t) => t.ticket), [a]);
  assert.deepEqual(marker(p, a), { ticket: a, status: 'refused', reason: 'superseded' });
  await p.commit({ 'src/a.mjs': 'A\n' }); await p.tick(); await p.tick();
  assert.deepEqual(p.tickets().map((t) => t.ticket), [a]);
  assert.deepEqual([isDone(p.repo, a), pendingTickets(p.repo)], [true, []]);
  assert.equal(enqueue({ ...p.opts, file: 'src/a.mjs' }).status, 'queued');
  assert.equal(await p.worker.drain(), 1);
  assert.deepEqual(p.sessions, [a]);
  assert.equal(readResult(p.repo, p.runId, a).approved, true);
  const done = marker(p, a);
  // A restart at base and a manual enqueue both preserve a real result.
  rmSync(path.join(queueDir(p.repo), 'watch', `${BLOCK}.json`));
  await p.tick();
  assert.equal(await p.worker.drain(), 0);
  assert.equal(enqueue({ ...p.opts, file: 'src/a.mjs' }).status, 'done');
  assert.deepEqual(marker(p, a), done);
  assert.deepEqual(p.sessions, [a]);
  assert.deepEqual(await cli(p, ['--wait-block', BLOCK, '--run', p.runId]), { code: 0, json: { block: BLOCK, files: [{ file: 'src/a.mjs', ticket: a, status: 'done', outcome: 'approved', ...timingsOf(p, a) }], done: true } });
});

test('manual enqueue revives superseded current content and keeps real results', async () => {
  const p = await fixture();
  await p.commit({ 'src/a.mjs': 'A\n' }); await p.tick(); await p.tick();
  const a = currentId(p, 'src/a.mjs');
  await p.commit({ 'src/a.mjs': 'B\n' }); await p.tick(); await p.tick();
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
  await p.tick();
  assert.equal(await p.worker.drain(), 1);
  await git(p.root, 'reset', '--hard', p.base);
  await p.tick();
  assert.deepEqual(p.tickets().map((t) => t.ticket), [a]);
  assert.deepEqual(pendingTickets(p.repo), []);
  assert.deepEqual(skips(p), [{ block: BLOCK, reason: 'rewound' }]);
  mkdirSync(path.join(p.root, 'src'), { recursive: true });
  await p.commit({ 'src/a.mjs': 'first\n', 'src/b.mjs': 'new branch\n' });
  const b = currentId(p, 'src/b.mjs');
  await p.tick(); await p.tick();
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
