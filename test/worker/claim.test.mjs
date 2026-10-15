// Fixture helper pins HOME before any source import.
import { BLOCK, freshDir, makeRepo, REPO } from './helpers.mjs';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFileSync, existsSync, readFileSync, readdirSync, watch, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { pathToFileURL } from 'node:url';

assert.equal(os.homedir(), process.env.HOME, 'children and parent share the isolated home');
const { enqueue, pendingTickets, readResult } = await import('../../src/worker/queue.mjs');
const moduleUrl = (file) => JSON.stringify(pathToFileURL(path.join(REPO, file)).href);
const lockPath = (key) => path.join(os.homedir(), '.code-forge', 'locks', `${createHash('sha256').update(key).digest('hex')}.lock`);

// All ordering uses file barriers. Timers only bound a failed barrier, never order workers.
async function reached(dir, names, diagnostic) {
  const found = () => names.find((name) => existsSync(path.join(dir, name)));
  if (found()) return found();
  return new Promise((resolve, reject) => {
    const watcher = watch(dir, () => { const name = found(); if (name) { clearTimeout(timer); watcher.close(); resolve(name); } });
    const timer = setTimeout(() => { watcher.close(); reject(new assert.AssertionError({ message: diagnostic(`no barrier reached: ${names.join(', ')}`), actual: [], expected: names })); }, 10000);
    const name = found();
    if (name) { clearTimeout(timer); watcher.close(); resolve(name); }
  });
}

async function fixture() {
  const { repo, runId } = await makeRepo();
  const dir = freshDir('claim');
  const script = path.join(dir, 'child.mjs');
  writeFileSync(path.join(dir, 'rows'), '');
  writeFileSync(path.join(dir, 'sessions'), '');
  writeFileSync(script, `
import { appendFileSync, existsSync, mkdirSync, readFileSync, renameSync, watch, writeFileSync } from 'node:fs';
import path from 'node:path';
import { mock } from 'node:test';
const [dir, repo, runId, name, mode] = process.argv.slice(2);
// Existence is the barrier: publish only after the complete payload has been written.
// Linux inotify can report creation before writeFileSync writes the count (Number('') is 0).
const signal = (file, value = '') => {
  const target = path.join(dir, file);
  const temp = target + '.' + process.pid + '.tmp';
  writeFileSync(temp, String(value));
  renameSync(temp, target);
};
const wait = async (file) => {
  if (existsSync(path.join(dir, file))) return;
  await new Promise((resolve) => {
    const watcher = watch(dir, () => {
      if (existsSync(path.join(dir, file))) { watcher.close(); resolve(); }
    });
    if (existsSync(path.join(dir, file))) { watcher.close(); resolve(); }
  });
};
// Observe actual held locks through the public afterRead hook; keep the real implementation.
const locks = await import(${moduleUrl('src/util/file-locks.mjs')});
mock.module(${moduleUrl('src/util/file-locks.mjs')}, { namedExports: {
  ...locks,
  withLock: (key, fn, opts = {}) => locks.withLock(key, fn, { ...opts, afterRead: async () => {
    signal('held-' + name + '-' + (key.startsWith('block:') ? 'block' : key.startsWith('budget:') ? 'budget' : 'file'));
  } }),
} });
const { createWorker } = await import(${moduleUrl('src/worker/loop.mjs')});
const { loadKey } = await import(${moduleUrl('src/state/signer.mjs')});
const { createKeyStore } = await import(${moduleUrl('src/keys/store.mjs')});
const rowsPath = path.join(dir, 'rows');
const readRows = async () => {
  const rows = readFileSync(rowsPath, 'utf8').split('\\n').filter(Boolean).map(JSON.parse);
  if (mode === 'budget' && name === 'a' && !existsSync(path.join(dir, 'budget-read-a'))) {
    signal('budget-read-a');
    await wait('finish-a');
  }
  return rows;
};
const writeRow = async (row) => {
  appendFileSync(rowsPath, JSON.stringify(row) + '\\n');
  if (row.event === 'review.budget') signal('budget-written-' + name);
};
const session = (event, file) => appendFileSync(path.join(dir, 'sessions'), JSON.stringify({event, name, file}) + '\\n');
const review = async (ticket) => {
  if (mode === 'crash') { signal('crash-ready-a'); await wait('never'); }
  session('start', ticket.file);
  signal('session-' + name);
  if (name === 'a') await wait('finish-a');
  session('end', ticket.file);
  return { status: 'reviewed', approved: true, engine: 'test', sessions: [] };
};
const spawnSession = async (opts) => {
  session('start', opts.block);
  signal('session-' + name);
  const text = readFileSync(opts.promptPath, 'utf8');
  const hunks = text.slice(text.indexOf('\\nhunks:\\n') + 8).split('\\n');
  session('end', opts.block);
  return {status: 'ok', exit_code: 0, answer: { passed: true, summary: 's', reviewed_hunks: hunks.slice(0, hunks.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2)), findings: [], resolved: [], needs_file: [] }, usage: {tokens_in: 900, tokens_out: 300}};
};
const runRootDir = path.join(dir, 'root-' + name);
mkdirSync(runRootDir);
const worker = await createWorker({runId, repoRoot: repo, cfg: {review: {parallel_tickets: 1}}, runRootDir, slug: 'worker-test', key: await loadKey(runId), pollMs: 10}, {
  store: await createKeyStore({backends: [], dir: path.join(dir, 'store-' + name)}), env: {}, expiryCheck: async () => {}, readRows, writeRow,
  ...(mode === 'budget' ? {spawn: spawnSession, jev: async () => ({ok: false})} : {review}),
});
signal('reached-' + name);
await wait('go');
await wait('dispatch-' + name);
signal('done-' + name, await worker.drain());
await wait('again-' + name);
signal('again-done-' + name, await worker.drain());
worker.release();
`);
  const children = [];
  const start = (name, mode = 'fake') => {
    const child = spawn(process.execPath, ['--experimental-test-module-mocks', script, dir, repo, runId, name, mode], { env: { ...process.env, HOME: os.homedir() }, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    child.stdout.on('data', (bytes) => { output += bytes; });
    child.stderr.on('data', (bytes) => { output += bytes; });
    const exited = new Promise((resolve) => {
      child.once('error', (err) => resolve({ code: -1, signal: null, output: String(err) }));
      child.once('exit', (code, signal) => resolve({ code, signal, output }));
    });
    const handle = { child, exited, output: () => output };
    children.push(handle);
    return handle;
  };
  const signal = (name) => writeFileSync(path.join(dir, name), '');
  const wait = (...names) => reached(dir, names, diagnostic);
  const observedCounts = {};
  const count = (name) => {
    const bytes = readFileSync(path.join(dir, name), 'utf8');
    observedCounts[name] = bytes;
    assert.match(bytes, /^(0|[1-9][0-9]*)$/, diagnostic(`${name} publishes one complete nonnegative integer count`));
    return Number(bytes);
  };
  const sessions = () => readFileSync(path.join(dir, 'sessions'), 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line));
  const rows = () => readFileSync(path.join(dir, 'rows'), 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line));
  const tickets = [];
  const add = (file = 'src/a.mjs') => {
    const id = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file }).ticket;
    tickets.push(id);
    return id;
  };
  const snapshot = (file) => {
    try { return readFileSync(file, 'utf8'); }
    catch (err) { return { error: err.code }; }
  };
  const diagnostic = (message) => `${message}; fixture=${JSON.stringify({
    sessions: snapshot(path.join(dir, 'sessions')), rows: snapshot(path.join(dir, 'rows')), observedCounts,
    barriers: Object.fromEntries(readdirSync(dir).filter((name) => name.startsWith('done-') || name.startsWith('again-done-')).map((name) => [name, snapshot(path.join(dir, name))])),
    claims: Object.fromEntries(tickets.map((id) => [id, snapshot(lockPath(`ticket\0${runId}\0${id}`))])),
    children: children.map(({ child, output }) => ({ pid: child.pid, exitCode: child.exitCode, signalCode: child.signalCode, output: output() })),
  })}`;
  const stop = async (handle) => {
    if (handle.child.exitCode !== null || handle.child.signalCode !== null) return;
    // macOS answers EPERM, not ESRCH, when every process in the group has exited but is not reaped yet.
    try { process.kill(-handle.child.pid, 'SIGKILL'); } catch (err) { if (err.code !== 'ESRCH' && err.code !== 'EPERM') throw err; }
    await handle.exited;
  };
  const close = async () => { for (const handle of children) await stop(handle); };
  return { repo, runId, dir, start, signal, wait, count, sessions, rows, add, stop, close, diagnostic };
}

test('two processes see one pending ticket: one session, contender skips and done prevents rerun', async () => {
  const f = await fixture();
  try {
    const id = f.add();
    f.start('a'); f.start('b');
    await Promise.all([f.wait('reached-a'), f.wait('reached-b')]);
    f.signal('go'); f.signal('dispatch-a');
    await f.wait('session-a');
    f.signal('dispatch-b');
    await f.wait('done-b');
    assert.equal(f.count('done-b'), 0, f.diagnostic('live ticket claim skips the contender without a slot'));
    assert.deepEqual(f.sessions().filter((s) => s.event === 'start').map((s) => s.name), ['a'], f.diagnostic('exactly one review session'));
    f.signal('finish-a'); await f.wait('done-a');
    assert.equal(f.count('done-a'), 1, f.diagnostic('claim owner completes once'));
    f.signal('again-b'); await f.wait('again-done-b');
    assert.equal(f.count('again-done-b'), 0, f.diagnostic('done marker stops a later drain'));
    assert.equal(existsSync(lockPath(`ticket\0${f.runId}\0${id}`)), false, f.diagnostic('completed ticket releases its claim'));
    assert.deepEqual(pendingTickets(f.repo), [], f.diagnostic('zero tickets remain pending after completion'));
  } finally { await f.close(); }
});

test('two processes serialize one (block, file) and write one block budget row', async () => {
  const f = await fixture();
  const budget = await fixture();
  try {
    const first = f.add();
    f.start('a'); f.start('b');
    await Promise.all([f.wait('reached-a'), f.wait('reached-b')]);
    f.signal('go'); f.signal('dispatch-a'); await f.wait('session-a');
    appendFileSync(path.join(f.repo, 'src/a.mjs'), 'export const changed = 2;\n');
    const second = f.add();
    assert.notEqual(second, first, 'changed content produces a distinct ticket');
    f.signal('dispatch-b');
    await f.wait('held-b-file', 'session-b');
    const startsWhileHeld = f.sessions().filter((s) => s.event === 'start').length;
    f.signal('finish-a');
    await Promise.all([f.wait('done-a'), f.wait('done-b')]);
    await f.close(); // only two worker processes live at any point

    // Hold A's empty row snapshot; B must wait for its block lock before reading those rows.
    budget.add('src/a.mjs'); budget.add('src/b.mjs');
    budget.start('a', 'budget'); budget.start('b', 'budget');
    await Promise.all([budget.wait('reached-a'), budget.wait('reached-b')]);
    budget.signal('go'); budget.signal('dispatch-a'); await budget.wait('budget-read-a');
    budget.signal('dispatch-b');
    await budget.wait('held-b-block', 'budget-written-b');
    budget.signal('finish-a');
    await Promise.all([budget.wait('done-a'), budget.wait('done-b')]);
    const budgets = budget.rows().filter((r) => r.event === 'review.budget' && r.block === BLOCK);
    assert.deepEqual([startsWhileHeld, budgets.length], [1, 1], 'one same-file session while held; exactly one budget row across workers');
    assert.deepEqual(f.sessions().map((s) => `${s.event}-${s.name}`), ['start-a', 'end-a', 'start-b', 'end-b'], 'same-file reviews never overlap');
    assert.equal(f.sessions().filter((s) => s.event === 'start').length, 2, 'both distinct tickets review exactly once');
    assert.equal(readResult(f.repo, f.runId, second)?.approved, true);
    assert.equal(budget.sessions().filter((s) => s.event === 'start').length, 2, 'both files receive a real engine session');
    assert.deepEqual(pendingTickets(budget.repo), []);
  } finally { await f.close(); await budget.close(); }
});

test('killed worker leaves a dead ticket claim: next drain breaks it and reviews once', async () => {
  const f = await fixture();
  try {
    const id = f.add();
    const owner = f.start('a', 'crash');
    const next = f.start('b');
    await Promise.all([f.wait('reached-a'), f.wait('reached-b')]);
    f.signal('go'); f.signal('dispatch-a'); await f.wait('crash-ready-a');
    const claim = lockPath(`ticket\0${f.runId}\0${id}`);
    assert.equal(existsSync(claim), true, f.diagnostic('worker holds its ticket claim before being killed'));
    assert.equal(JSON.parse(readFileSync(claim, 'utf8')).pid, owner.child.pid, f.diagnostic('claim belongs to the real worker pid'));
    await f.stop(owner);
    assert.equal(existsSync(claim), true, f.diagnostic('SIGKILL leaves the claim for dead-owner recovery'));
    f.signal('dispatch-b'); await f.wait('done-b');
    assert.equal(f.count('done-b'), 1, f.diagnostic('next worker recovers and completes the pending ticket'));
    assert.deepEqual(f.sessions().filter((s) => s.event === 'start').map((s) => s.name), ['b'], f.diagnostic('exactly one session after recovery'));
    assert.equal(existsSync(claim), false, f.diagnostic('recovered ticket releases its claim'));
    f.signal('again-b'); await f.wait('again-done-b');
    assert.equal(f.count('again-done-b'), 0, f.diagnostic('recovered done ticket never reruns'));
    assert.equal((await next.exited).code, 0, f.diagnostic('recovery worker exits cleanly'));
  } finally { await f.close(); }
});

test('boundary: a held claim takes no slot and another file runs in a one-slot worker', async () => {
  const f = await fixture();
  try {
    const held = f.add();
    f.start('a'); f.start('b');
    await Promise.all([f.wait('reached-a'), f.wait('reached-b')]);
    f.signal('go'); f.signal('dispatch-a'); await f.wait('session-a');
    const other = f.add('src/b.mjs');
    f.signal('dispatch-b'); await f.wait('done-b');
    assert.equal(f.count('done-b'), 1, f.diagnostic('one-slot contender runs only the unclaimed other file'));
    assert.deepEqual(f.sessions().filter((s) => s.event === 'start').map((s) => [s.name, s.file]), [['a', 'src/a.mjs'], ['b', 'src/b.mjs']], f.diagnostic('exactly one session per file, held owner then contender'));
    assert.deepEqual(pendingTickets(f.repo), [held], f.diagnostic('live owner ticket remains pending while other file is done'));
    assert.equal(readResult(f.repo, f.runId, other)?.approved, true, f.diagnostic('the other file has an approved result'));
    f.signal('finish-a'); await f.wait('done-a');
    assert.deepEqual(pendingTickets(f.repo), [], f.diagnostic('zero tickets remain pending after the held owner finishes'));
  } finally { await f.close(); }
});
