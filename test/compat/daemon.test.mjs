import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { appendFileSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runReviewFile } from '../../src/cli/review-file.mjs';
import { ledgerPath } from '../../src/ledger/paths.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { startRun } from '../../src/state/run.mjs';
import { loadKey, signRow } from '../../src/state/signer.mjs';
import { readStartTime } from '../../src/util/reaper.mjs';
import { announceWorker, blockTickets, enqueue, enqueueAnswers, liveWorker, queueDir, readTicket, retractWorker, writeResult, writeTicketTimings } from '../../src/worker/queue.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const binary = path.join(root, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
// Installation tests temporarily rename/chmod the release binary in parallel.
const daemonBinary = `${binary}-daemon-test-${process.pid}`;
after(() => rmSync(daemonBinary, { force: true }));
let built = false;
function build(t) {
  if (spawnSync('go', ['version']).error?.['code'] === 'ENOENT') {
    t.skip('Go daemon compatibility skipped: go is not on PATH');
    return false;
  }
  if (!built) {
    const result = spawnSync(process.execPath, ['scripts/build-native.mjs', '--host'], { cwd: root, encoding: 'utf8', timeout: 120_000 });
    assert.equal(result.status, 0, result.stderr);
    copyFileSync(binary, daemonBinary);
    built = true;
  }
  return true;
}

async function fixture(t) {
  const dir = mkdtempSync('/tmp/cfd-');
  const oldHome = process.env.HOME;
  const oldBackend = process.env.CODE_FORGE_KEY_BACKEND;
  process.env.HOME = path.join(dir, 'h');
  process.env.CODE_FORGE_KEY_BACKEND = 'file';
  const home = process.env.HOME;
  const repo = path.join(dir, 'repo');
  mkdirSync(repo);
  for (const args of [['init', '-q'], ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '--allow-empty', '-m', 'base']]) {
    const result = spawnSync('git', args, { cwd: repo, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
  }
  writeFileSync(path.join(repo, '.code-forge.yml'), 'version: 1\nproject:\n  slug: daemon-test\nprovider: anthropic\nlevels:\n  L2:\n    model: claude-opus-5-5\nreview:\n  exclude: ["**/*.md"]\n');
  mkdirSync(path.join(repo, 'src'));
  for (const name of ['a.mjs', 'b.mjs', 'skip.md']) writeFileSync(path.join(repo, 'src', name), `fixture ${name}\n`);
  const run = 'r-daemon-test';
  await startRun({ workspace: repo, project: 'daemon-test', runId: run, config: { review: { exclude: ['**/*.md'] } }, writeRow: async () => {} });
  await openBlock({ runId: run, id: 'B1', level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'fixture', tests: ['fixture'] }], writeRow: async () => {} });
  const key = await loadKey(run);
  await appendRow(signRow({ event: 'block.open', run, block: 'B1' }, key), { slug: 'daemon-test' });
  const socket = path.join(home, '.code-forge', 'runs', run, 'native.sock');
  const children = [];
  t.after(async () => {
    retractWorker(repo, process.pid);
    for (const child of children) {
      if (child.exitCode === null && child.signalCode === null) {
        const exited = once(child, 'exit');
        child.kill('SIGTERM');
        await exited;
      }
    }
    if (oldHome === undefined) delete process.env.HOME; else process.env.HOME = oldHome;
    if (oldBackend === undefined) delete process.env.CODE_FORGE_KEY_BACKEND; else process.env.CODE_FORGE_KEY_BACKEND = oldBackend;
    rmSync(dir, { recursive: true, force: true });
  });
  function start(idle = '30m') {
    const child = spawn(daemonBinary, ['serve', '--home', home, '--run', run, '--idle', idle], { stdio: ['ignore', 'ignore', 'pipe'] });
    children.push(child);
    return child;
  }
  async function ready(child) {
    let stderr = '';
    child.stderr.on('data', (s) => { stderr += s; });
    for (let i = 0; i < 100 && child.exitCode === null; i++) {
      try { return await request(socket, { method: 'ping' }); } catch { await new Promise((resolve) => setTimeout(resolve, 20)); }
    }
    assert.fail(`serve must answer ping on the run socket; exit=${child.exitCode}: ${stderr}`);
  }
  const reference = async (expectedCode = 0) => {
    let text = '';
    let error = '';
    const code = await runReviewFile(['--wait-block', 'B1', '--run', run, '--max', '0ms'], {
      cwd: repo, pollMs: 1, stdout: { write: (s) => { text += s; } }, stderr: { write: (s) => { error += s; } },
    });
    assert.equal(code, expectedCode, error);
    return JSON.parse(text);
  };
  const referenceStatus = () => {
    const result = spawnSync(process.execPath, [path.join(root, 'bin/code-forge.mjs'), 'status', '--run', run, '--json'], {
      cwd: repo, env: process.env, encoding: 'utf8', timeout: 10000,
    });
    assert.equal(result.status, 0, result.stderr);
    return JSON.parse(result.stdout);
  };
  return { home, repo, run, key, socket, start, ready, reference, referenceStatus };
}

function request(socket, body) {
  return new Promise((resolve, reject) => {
    const client = net.createConnection(socket);
    client.setTimeout(5000, () => client.destroy(new Error('daemon request timed out')));
    let text = '';
    client.on('error', reject);
    client.on('connect', () => client.write(`${JSON.stringify(body)}\n`));
    client.on('data', (chunk) => {
      text += chunk;
      if (text.includes('\n')) {
        client.destroy();
        try { resolve(JSON.parse(text.split('\n')[0])); } catch (err) { reject(err); }
      }
    });
    client.on('end', () => { if (!text.includes('\n')) reject(new Error('daemon closed without a response')); });
  });
}

const withoutStats = ({ stats, ...result }) => result;

test('daemon hot reads match Node, incrementally tail and index, long-poll, and prune only signed closed blocks', async (t) => {
  if (!build(t)) return;
  const f = await fixture(t);
  const { repo, run, key, socket } = f;
  announceWorker(repo, { pid: process.pid, run });
  const tickets = ['a', 'b'].map((name) => enqueue({ repoRoot: repo, run, block: 'B1', file: `src/${name}.mjs` }));
  enqueue({ repoRoot: repo, run, block: 'Other', file: 'src/a.mjs' });
  enqueueAnswers({ repoRoot: repo, run, block: 'B1', file: 'src/a.mjs', byFile: 'src/b.mjs', answers: ['F1'] });
  const child = f.start();
  const ping = await f.ready(child);
  assert.deepEqual([ping.ok, ping.run, ping.pid], [true, run, child.pid]);
  assert.equal(typeof ping.version, 'string');
  assert.equal(statSync(socket).mode & 0o777, 0o600, 'socket must be private');
  const listed = await request(socket, { method: 'block-tickets', block: 'B1' });
  assert.deepEqual(withoutStats(listed), { block: 'B1', tickets: blockTickets({ repoRoot: repo, run, block: 'B1' }) });
  assert.equal(listed.stats.tickets_parsed, 4, 'parse each of four ticket files exactly once');
  const initial = await request(socket, { method: 'wait-block', block: 'B1', max_ms: 0 });
  assert.deepEqual(withoutStats(initial), await f.reference());
  assert.deepEqual(withoutStats(await request(socket, { method: 'status' })), f.referenceStatus());
  assert.equal(initial.stats.tickets_parsed, 4, 'unchanged tickets are not reparsed by another request');
  const bytes = Array.from({ length: 7 }, (_, n) => JSON.stringify(signRow({ event: 'session', run, n }, key)) + '\n').join('');
  appendFileSync(ledgerPath('daemon-test'), bytes);
  const after = await request(socket, { method: 'ping' });
  assert.equal(after.stats.bytes_read - initial.stats.bytes_read, Buffer.byteLength(bytes), 'read exactly seven appended rows, zero old bytes');
  writeTicketTimings(repo, blockTickets({ repoRoot: repo, run, block: 'B1' })[0], { duration_ms: 17 });
  const changed = await request(socket, { method: 'block-tickets', block: 'B1' });
  assert.equal(changed.stats.tickets_parsed - after.stats.tickets_parsed, 1, 'only one modified ticket is reparsed');
  assert.deepEqual(changed.tickets, blockTickets({ repoRoot: repo, run, block: 'B1' }));
  const waiting = request(socket, { method: 'wait-block', block: 'B1', max_ms: 3000 });
  await new Promise((resolve) => setTimeout(resolve, 250));
  assert.equal((await request(socket, { method: 'ping' })).pid, child.pid, 'long-poll does not block other clients or spawn a replacement daemon');
  for (const [i, ticket] of tickets.entries()) writeResult({ repoRoot: repo, runId: run, ticket: ticket.ticket, key, result: { status: 'reviewed', approved: i === 0, findings: i === 0 ? [] : [{ id: 'F1' }], duration_ms: 31 } });
  const completed = await waiting;
  assert.deepEqual(withoutStats(completed), await f.reference());
  assert.deepEqual(withoutStats(await request(socket, { method: 'status' })), f.referenceStatus());
  assert.equal(completed.done, true, 'both current tickets finish and excluded Markdown does not block');
  writeFileSync(path.join(repo, 'src/b.mjs'), 'fixture blind edit\n');
  const handoff = await f.reference();
  rmSync(path.join(queueDir(repo), `${tickets[1].ticket}.delivered`));
  assert.deepEqual(withoutStats(await request(socket, { method: 'wait-block', block: 'B1', max_ms: 0 })), handoff, 'deliver earlier findings before queuing a blind edit');
  const requeued = await f.reference();
  assert.deepEqual(withoutStats(await request(socket, { method: 'wait-block', block: 'B1', max_ms: 0 })), requeued, 'the next join reviews the edited content');
  tickets[1] = enqueue({ repoRoot: repo, run, block: 'B1', file: 'src/b.mjs' });
  writeResult({ repoRoot: repo, runId: run, ticket: tickets[1].ticket, key, result: { status: 'reviewed', approved: true } });
  writeResult({ repoRoot: repo, runId: run, ticket: tickets[0].ticket, key, result: { status: 'unavailable' } });
  const retry = await request(socket, { method: 'wait-block', block: 'B1', max_ms: 0 });
  assert.deepEqual(withoutStats(retry), await f.reference(), 'the first unavailable result spends the same single automatic retry as Node');
  assert.equal(retry.files[0].status, 'pending', 'only the unavailable current ticket is retried');
  writeResult({ repoRoot: repo, runId: run, ticket: tickets[0].ticket, key, result: { status: 'unavailable' } });
  assert.deepEqual(withoutStats(await request(socket, { method: 'wait-block', block: 'B1', max_ms: 0 })), await f.reference(), 'a repeated unavailable result stays finished after the single retry');
  appendFileSync(path.join(repo, '.git/info/exclude'), '\nsrc/a.mjs\n');
  assert.deepEqual(withoutStats(await request(socket, { method: 'wait-block', block: 'B1', max_ms: 0 })), await f.reference(), 'Git metadata changes invalidate the stat-based changed-file cache');
  writeFileSync(path.join(repo, '.git/info/exclude'), '');
  for (const args of [['add', 'src/a.mjs'], ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'track reviewed file']]) {
    assert.equal(spawnSync('git', args, { cwd: repo }).status, 0);
  }
  // Move the block base without touching source-file stamps, as a scope refresh does.
  const { readRun, saveRun } = await import('../../src/state/run.mjs');
  const record = await readRun(run);
  record.blocks.B1.base_sha = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).stdout.trim();
  await saveRun(record);
  assert.deepEqual(withoutStats(await request(socket, { method: 'wait-block', block: 'B1', max_ms: 0 })), await f.reference(), 'a new scope base refreshes changed files without content edits');
  assert.deepEqual(withoutStats(await request(socket, { method: 'prune', block: 'B1' })), { ok: false, error: 'block-open' });
  await appendRow({ event: 'block.close', run, block: 'B1', status: 'complete' }, { slug: 'daemon-test' });
  assert.equal((await request(socket, { method: 'prune', block: 'B1' })).error, 'block-open', 'unsigned close cannot authorize deletion');
  await appendRow(signRow({ event: 'block.close', run, block: 'B1', status: 'complete' }, key), { slug: 'daemon-test' });
  const pruned = await request(socket, { method: 'prune', block: 'B1' });
  assert.deepEqual(withoutStats(pruned), { ok: true, block: 'B1', tickets: 4, markers: 5 });
  assert.deepEqual((await request(socket, { method: 'block-tickets', block: 'B1' })).tickets, []);
  assert.equal(blockTickets({ repoRoot: repo, run, block: 'Other' }).length, 1, 'other block ticket survives pruning');
});

test('daemon and Node accept null-tree tickets and agree on worker liveness', async (t) => {
  if (!build(t)) return;
  const f = await fixture(t);
  const { repo, run, socket } = f;
  const ticket = enqueue({ repoRoot: repo, run, block: 'B1', file: 'src/a.mjs' });
  const file = path.join(queueDir(repo), `${ticket.ticket}.json`);
  writeFileSync(file, JSON.stringify({ ...JSON.parse(readFileSync(file, 'utf8')), tree: null }));
  assert.equal(readTicket(repo, ticket.ticket).tree, null);
  // Use the legacy announcement alone so the same identity is checked on both sides.
  const stale = new Date(Date.now() - 120000).toISOString();
  await f.ready(f.start());
  assert.deepEqual(withoutStats(await request(socket, { method: 'block-tickets', block: 'B1' })), {
    block: 'B1', tickets: blockTickets({ repoRoot: repo, run, block: 'B1' }),
  });
  const start = readStartTime(process.pid);
  assert.equal(typeof start, 'string', 'current process start time must be readable');
  writeFileSync(path.join(queueDir(repo), 'worker.json'), JSON.stringify({
    pid: process.pid, run, start_time: start, heartbeat_at: new Date().toISOString(),
  }));
  for (let i = 0; i < 3; i++) {
    for (const method of ['wait-block', 'status']) {
      const response = await request(socket, { method, block: 'B1', max_ms: 0 });
      assert.equal(response.stats.ps_processes, 0, `${method} with a fresh heartbeat must not spawn ps`);
      assert.deepEqual(withoutStats(response), method === 'status' ? f.referenceStatus() : await f.reference());
    }
  }
  assert.equal((await request(socket, { method: 'ping' })).stats.ps_processes, 0);
  const cases = [
    { name: 'unknown stale', start_time: 'unknown', heartbeat_at: stale, live: true },
    { name: 'mismatch stale', start_time: 'wrong start', heartbeat_at: stale, live: false },
    { name: 'match stale', start_time: start, heartbeat_at: stale, live: true },
    { name: 'mismatch fresh', start_time: 'wrong start', heartbeat_at: new Date().toISOString(), live: true },
    { name: 'mismatch far future', start_time: 'wrong start', heartbeat_at: new Date(Date.now() + 60000).toISOString(), live: false },
    { name: 'dead fresh', pid: 2 ** 30, start_time: 'unknown', heartbeat_at: new Date().toISOString(), live: false },
  ];
  async function check(c) {
    writeFileSync(path.join(queueDir(repo), 'worker.json'), JSON.stringify({ pid: process.pid, run, started_at: stale, ...c }));
    assert.equal(liveWorker(repo) !== null, c.live, 'Node liveness');
    const before = (await request(socket, { method: 'ping' })).stats.ps_processes;
    const response = await request(socket, { method: 'wait-block', block: 'B1', max_ms: 0 });
    if (c.name === 'unknown stale') assert.equal(response.stats.ps_processes, before, 'unknown identity does not need ps');
    if (c.name === 'match stale') assert.ok(response.stats.ps_processes > before, 'stale heartbeat with a known identity counts spawned ps');
    const result = withoutStats(response);
    assert.equal(result.status === 'worker_down', !c.live, 'daemon liveness');
    assert.deepEqual(result, await f.reference(c.live ? 0 : 3), 'wait-block must match Node');
  }
  for (const c of cases) await t.test(c.name, () => check(c));
  await t.test('EPERM fresh', async (t) => {
    const processes = spawnSync('/bin/ps', ['-axo', 'pid='], { encoding: 'utf8' });
    assert.equal(processes.status, 0, processes.stderr);
    const pid = processes.stdout.trim().split(/\s+/).map(Number).find((pid) => {
      if (pid <= 1) return false;
      try { process.kill(pid, 0); } catch (err) { return err.code === 'EPERM'; }
      return false;
    });
    if (pid === undefined) return t.skip('no live pid greater than 1 returns EPERM on this host');
    await check({ name: 'system process', pid, start_time: 'wrong start', heartbeat_at: new Date().toISOString(), live: true });
  });
  assert.deepEqual(withoutStats(await request(socket, { method: 'status' })), f.referenceStatus());
});

test('daemon status refreshes cached ledger, run state, config, and signed finding decisions', async (t) => {
  if (!build(t)) return;
  const f = await fixture(t);
  const { repo, run, key, socket } = f;
  const child = f.start();
  await f.ready(child);
  const check = async () => {
    const result = withoutStats(await request(socket, { method: 'status' }));
    assert.deepEqual(result, f.referenceStatus());
    return result.runs[0];
  };
  assert.equal((await check()).blocks[0].findings, 0);
  for (const row of [
    { event: 'session', run, block: 'B1', role: 'coder', provider: 'anthropic', model: 'fixture-model', usd: 1.25 },
    { event: 'session.background', run, block: 'B1' },
    { event: 'gate.red', run, block: 'B1', gate: 'fixture-gate' },
    { event: 'review.late_finding', run, block: 'B1', file: 'src/a.mjs', finding: 'F1', severity: 'critical' },
  ]) await appendRow(signRow(row, key), { slug: 'daemon-test' });
  const pending = await check();
  assert.equal(pending.spent_usd, 1.25);
  assert.equal(pending.unknown_usd_sessions, 1);
  assert.deepEqual(pending.blocks[0].gate, { result: 'fail', name: 'fixture-gate' });
  assert.equal(pending.blocks[0].model, 'anthropic/fixture-model');
  assert.equal(pending.blocks[0].findings, 1);
  await appendRow(signRow({ event: 'review.waived', run, block: 'B1', file: 'src/a.mjs', finding: 'F1', by: 'human' }, key), { slug: 'daemon-test' });
  await appendRow({ event: 'gate.done', run, block: 'B1', all_ok: true }, { slug: 'daemon-test' });
  const { readRun, saveRun } = await import('../../src/state/run.mjs');
  const record = await readRun(run);
  record.blocks.B1.status = 'awaiting_human_check';
  await saveRun(record);
  appendFileSync(path.join(repo, '.code-forge.yml'), 'budget:\n  usd: 5\n');
  const cleared = await check();
  assert.equal(cleared.blocks[0].findings, 0);
  assert.deepEqual(cleared.blocks[0].gate, { result: 'pass', name: null });
  assert.deepEqual(cleared.awaiting_human_check, ['B1']);
  assert.equal(cleared.budget_usd, 5);
});

test('daemon refuses a live duplicate, replaces a stale socket, handles rotations and torn tails, and exits idle or on SIGTERM', async (t) => {
  if (!build(t)) return;
  const f = await fixture(t);
  const child = f.start();
  await f.ready(child);
  const duplicate = f.start();
  const [code] = await once(duplicate, 'exit');
  assert.equal(code, 1, 'second daemon refuses the live listener');
  assert.equal((await request(f.socket, { method: 'ping' })).pid, child.pid, 'duplicate cannot unlink the original socket');
  const file = ledgerPath('daemon-test');
  const before = await request(f.socket, { method: 'ping' });
  const partial = '{"event":"session"';
  appendFileSync(file, partial);
  const torn = await request(f.socket, { method: 'ping' });
  assert.equal(torn.stats.bytes_read - before.stats.bytes_read, Buffer.byteLength(partial));
  const suffix = ',"n":1}\n';
  appendFileSync(file, suffix);
  const fixed = await request(f.socket, { method: 'ping' });
  assert.equal(fixed.stats.bytes_read - torn.stats.bytes_read, Buffer.byteLength(suffix), 'torn prefix is cached while reading only new bytes');
  assert.equal(fixed.stats.rows - before.stats.rows, 1, 'completed tail appears exactly once');
  renameSync(file, file.replace('.jsonl', '.1.jsonl'));
  const next = '{"event":"session","n":2}\n';
  writeFileSync(file, next);
  const rotated = await request(f.socket, { method: 'ping' });
  assert.equal(rotated.stats.bytes_read - fixed.stats.bytes_read, Buffer.byteLength(next), 'renamed inode is reused without rereading rotated bytes');
  assert.equal(rotated.stats.rows - fixed.stats.rows, 1, 'rotation retains old rows without duplicates');
  appendFileSync(file, '{bad}\n{"valid":true}\n');
  assert.equal((await request(f.socket, { method: 'ping' })).ok, false, 'malformed interior row is refused');
  const exited = once(child, 'exit');
  child.kill('SIGTERM');
  assert.deepEqual(await exited, [0, null]);
  // A leftover socket inode, as after a crash, must be recoverable.
  const stale = net.createServer();
  await new Promise((resolve) => stale.listen(f.socket, () => resolve(undefined)));
  const leftover = `${f.socket}.leftover`;
  renameSync(f.socket, leftover);
  await new Promise((resolve) => stale.close(() => resolve(undefined)));
  renameSync(leftover, f.socket);
  // Restore a clean ledger for the new daemon.
  writeFileSync(file, next);
  const idle = f.start('400ms');
  const idleExit = once(idle, 'exit');
  await f.ready(idle);
  assert.deepEqual(await idleExit, [0, null], 'short idle timeout terminates cleanly');
  assert.throws(() => statSync(f.socket), { code: 'ENOENT' }, 'idle exit removes the socket');
});
