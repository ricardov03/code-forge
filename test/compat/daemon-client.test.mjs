import assert from 'node:assert/strict';
import childProcess, { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import fs, { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { after, mock, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const binary = path.join(root, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const daemonBinary = `${binary}-client-test-${process.pid}`;
let daemonSpawns = 0;
mock.module('node:child_process', { defaultExport: childProcess, namedExports: { ...childProcess, spawn: (file, args, options) => {
  if (args?.[0] === 'serve') daemonSpawns++;
  return spawn(file === binary ? daemonBinary : file, args, options);
} } });
// Installation tests rename/chmod the release binary in parallel. The lifecycle
// still uses the real client and worker, resolving only that binary to our copy.
mock.module('node:fs', { defaultExport: { ...fs }, namedExports: { ...fs,
  statSync: (file, options) => fs.statSync(file === binary ? daemonBinary : file, options),
  accessSync: (file, mode) => fs.accessSync(file === binary ? daemonBinary : file, mode),
} });
const ledger = await import('../../src/ledger/write.mjs');
let ledgerReads = 0;
mock.module('../../src/ledger/write.mjs', { namedExports: { ...ledger, readAllRows: async (slug) => {
  ledgerReads++;
  return ledger.readAllRows(slug);
} } });
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { default: status } = await import('../../src/cli/status.mjs');
const native = await import('../../src/util/native.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { announceWorker, blockTickets, queueDir, retractWorker, writeResult } = await import('../../src/worker/queue.mjs');
const { bootWorker } = await import('../../src/worker/loop.mjs');

after(() => rmSync(daemonBinary, { force: true }));
let built = false;
function build() {
  if (built) return;
  const result = spawnSync(process.execPath, ['scripts/build-native.mjs', '--host'], { cwd: root, encoding: 'utf8', timeout: 120000 });
  assert.equal(result.status, 0, result.stderr);
  copyFileSync(binary, daemonBinary);
  built = true;
}
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function fixture(t, config = { review: { exclude: ['**/*.md'] } }) {
  build();
  const dir = mkdtempSync('/tmp/cfc-');
  const saved = { HOME: process.env.HOME, CODE_FORGE_NATIVE: process.env.CODE_FORGE_NATIVE, CODE_FORGE_KEY_BACKEND: process.env.CODE_FORGE_KEY_BACKEND };
  const home = path.join(dir, 'home');
  process.env.HOME = home;
  process.env.CODE_FORGE_KEY_BACKEND = 'file';
  delete process.env.CODE_FORGE_NATIVE;
  const repo = path.join(dir, 'repo');
  mkdirSync(repo);
  for (const args of [['init', '-q'], ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '--allow-empty', '-m', 'base']]) {
    const result = spawnSync('git', args, { cwd: repo, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
  }
  writeFileSync(path.join(repo, '.code-forge.yml'), 'version: 1\nprovider: anthropic\nlevels:\n  L2:\n    model: claude-opus-5-5\n');
  mkdirSync(path.join(repo, 'src'));
  writeFileSync(path.join(repo, 'src/a.mjs'), 'fixture\n');
  writeFileSync(path.join(repo, 'src/skip.md'), 'excluded fixture\n');
  const run = 'r-client-test';
  await startRun({ workspace: repo, project: 'daemon-client-test', runId: run, config, writeRow: async () => {} });
  await openBlock({ runId: run, id: 'B1', level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'fixture', tests: ['fixture'] }], writeRow: async () => {} });
  await ledger.appendRow({ event: 'block.open', run, block: 'B1' }, { slug: 'daemon-client-test' });
  announceWorker(repo, { pid: process.pid, run });
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
    for (const [key, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
    rmSync(dir, { recursive: true, force: true });
  });
  async function start() {
    const child = spawn(daemonBinary, ['serve', '--home', home, '--run', run], { stdio: ['ignore', 'ignore', 'pipe'] });
    let error = '';
    child.stderr.on('data', (s) => { error += s; });
    children.push(child);
    for (let i = 0; i < 100; i++) {
      if (await ping(home, run)) return child;
      await delay(20);
    }
    assert.fail(`daemon must answer ping within 2 seconds: ${error}`);
  }
  async function wait(max = '0ms') {
    let stdout = '';
    let stderr = '';
    const code = await runReviewFile(['--wait-block', 'B1', '--run', run, '--max', max], {
      cwd: repo, pollMs: 5, stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } },
    });
    assert.equal(code, 0, stderr);
    return stdout;
  }
  async function show(json = true) {
    let text = '';
    const oldWrite = process.stdout.write;
    process.stdout.write = (s) => { text += s; return true; };
    try { assert.equal(await status(['--run', run, ...(json ? ['--json'] : [])]), 0); }
    finally { process.stdout.write = oldWrite; }
    return text;
  }
  return { home, run, repo, start, wait, show, config, key: await loadKey(run) };
}

function ping(home, run) {
  return new Promise((resolve) => {
    const socket = net.createConnection(path.join(home, '.code-forge', 'runs', run, 'native.sock'));
    socket.setTimeout(200, () => socket.destroy());
    socket.on('error', () => resolve(false));
    socket.on('close', () => resolve(false));
    socket.on('connect', () => socket.write('{"method":"ping"}\n'));
    socket.on('data', () => { socket.destroy(); resolve(true); });
  });
}

test('live daemon gives byte-identical wait and status payloads without client ledger reads', async (t) => {
  const f = await fixture(t);
  process.env.CODE_FORGE_NATIVE = '0';
  const pending = await f.wait();
  const tickets = blockTickets({ repoRoot: f.repo, run: f.run, block: 'B1' });
  assert.equal(tickets.length, 1, 'one changed file queues one ticket');
  delete process.env.CODE_FORGE_NATIVE;
  await f.start();
  ledgerReads = 0;
  assert.equal(await f.wait(), pending, 'pending wait bytes and excluded file equal Node');
  assert.equal(ledgerReads, 0, 'live pending poll calls the client ledger reader zero times');
  process.env.CODE_FORGE_NATIVE = '0';
  writeResult({ repoRoot: f.repo, runId: f.run, key: f.key, ticket: tickets[0].ticket, result: {
    status: 'reviewed', approved: true, queued_at: '2026-01-01T00:00:00Z',
    started_at: '2026-01-01T00:00:01Z', finished_at: '2026-01-01T00:00:02Z', duration_ms: 1000,
  } });
  const done = await f.wait();
  const json = await f.show();
  const text = await f.show(false);
  assert.equal(JSON.parse(json).runs[0].native_daemon, 'off', 'Node-only status reports off');
  delete process.env.CODE_FORGE_NATIVE;
  ledgerReads = 0;
  assert.equal(await f.wait(), done, 'done wait bytes preserve file and timing key order');
  assert.equal(await f.show(false), text, 'text status bytes equal Node');
  assert.equal(await f.show(), json.replace('"native_daemon":"off"', '"native_daemon":"live"'), 'JSON differs only in daemon diagnostic');
  assert.equal(ledgerReads, 0, 'live wait and both status renders call the client ledger reader zero times');
  assert.equal(JSON.parse(pending).reason, 'wait_timeout', 'pending fixture exercises timeout');

  // Findings delivered while another file is pending must survive the whole join.
  process.env.CODE_FORGE_NATIVE = '0';
  writeFileSync(path.join(f.repo, 'src/b.mjs'), 'second fixture\n');
  await f.wait();
  const second = blockTickets({ repoRoot: f.repo, run: f.run, block: 'B1' }).find((ticket) => ticket.file === 'src/b.mjs');
  writeResult({ repoRoot: f.repo, runId: f.run, key: f.key, ticket: tickets[0].ticket,
    result: { status: 'reviewed', approved: false, findings: [{ id: 'F1' }] } });
  writeFileSync(path.join(f.repo, 'src/a.mjs'), 'edited before receiving findings\n');
  const finishSecond = () => writeResult({ repoRoot: f.repo, runId: f.run, key: f.key,
    ticket: second.ticket, result: { status: 'reviewed', approved: true } });
  const [nodeJoin] = await Promise.all([f.wait('5s'), delay(1400).then(finishSecond)]);
  assert.equal(JSON.parse(nodeJoin).files[0].status, 'findings', 'Node retains the first file findings until the second file finishes');
  rmSync(path.join(queueDir(f.repo), `${second.ticket}.done`));
  rmSync(path.join(queueDir(f.repo), `${tickets[0].ticket}.delivered`));
  delete process.env.CODE_FORGE_NATIVE;
  ledgerReads = 0;
  const [daemonJoin] = await Promise.all([f.wait('5s'), delay(1400).then(finishSecond)]);
  assert.equal(daemonJoin, nodeJoin, 'daemon retains identical findings across a wait longer than one second');
  assert.equal(ledgerReads, 0, 'long daemon join reads the client ledger zero times');
});

test('dead or missing daemon falls back mid-wait; kill switches never touch a socket or start a daemon', async (t) => {
  const f = await fixture(t);
  process.env.CODE_FORGE_NATIVE = '0';
  await f.wait();
  const [ticket] = blockTickets({ repoRoot: f.repo, run: f.run, block: 'B1' });
  delete process.env.CODE_FORGE_NATIVE;
  const child = await f.start();
  ledgerReads = 0;
  const waiting = f.wait('2s');
  await delay(100);
  const exited = once(child, 'exit');
  child.kill('SIGKILL');
  await exited;
  await delay(50);
  writeResult({ repoRoot: f.repo, runId: f.run, key: f.key, ticket: ticket.ticket, result: { status: 'reviewed', approved: true } });
  const actual = await waiting;
  assert.ok(ledgerReads > 0, 'killed daemon causes the pending wait to read through Node');
  const fallbackStatus = await f.show();
  assert.equal(JSON.parse(fallbackStatus).runs[0].native_daemon, 'fallback', 'dead daemon reports fallback');
  process.env.CODE_FORGE_NATIVE = '0';
  assert.equal(actual, await f.wait(), 'mid-wait fallback finishes with identical bytes');
  assert.equal(fallbackStatus, (await f.show()).replace('"native_daemon":"off"', '"native_daemon":"fallback"'), 'fallback status preserves all Node payload bytes');
  delete process.env.CODE_FORGE_NATIVE;
  assert.equal(await f.wait(), actual, 'missing socket uses the identical Node answer');

  assert.equal(typeof native.startNativeDaemon, 'function', 'worker lifecycle helper is available');
  let connections = 0;
  const server = net.createServer((socket) => { connections++; socket.destroy(); });
  // SIGKILL leaves the old socket behind; replace it with a connection counter.
  const socketPath = path.join(f.home, '.code-forge', 'runs', f.run, 'native.sock');
  rmSync(socketPath, { force: true });
  await new Promise((resolve) => server.listen(socketPath, () => resolve(undefined)));
  t.after(() => server.close());
  const beforeSpawns = daemonSpawns;
  for (const config of [{}, { native: { daemon: false } }]) {
    if (!config.native) process.env.CODE_FORGE_NATIVE = '0'; else delete process.env.CODE_FORGE_NATIVE;
    const stop = await native.startNativeDaemon(f.run, config);
    assert.equal(await native.daemonRequest(f.run, { method: 'status' }, { config }), null, 'disabled client returns null');
    await stop();
  }
  assert.equal(connections, 0, 'both kill switches make zero socket connections');
  assert.equal(daemonSpawns - beforeSpawns, 0, 'both kill switches spawn zero daemons');
});

test('worker owns daemon lifetime and protocol errors return null within a bounded timeout', async (t) => {
  const f = await fixture(t);
  retractWorker(f.repo, process.pid);
  const beforeSpawns = daemonSpawns;
  let serving = 0;
  const worker = await bootWorker({ runId: f.run, cwd: f.repo }, { writeRow: async () => {}, expiryCheck: async () => {
    assert.equal(await ping(f.home, f.run), true, 'worker starts daemon before draining');
    serving++;
    const stop = await native.startNativeDaemon(f.run, {});
    await stop();
    assert.equal(await ping(f.home, f.run), true, 'a second worker does not stop the existing daemon');
  } });
  await worker.once();
  assert.equal(serving, 1, 'one drain runs with a live daemon');
  assert.equal(daemonSpawns - beforeSpawns, 1, 'worker starts exactly one daemon and the duplicate start reuses it');
  assert.equal(await ping(f.home, f.run), false, 'worker completion reaps its daemon');
  const socketPath = path.join(f.home, '.code-forge', 'runs', f.run, 'native.sock');
  for (const response of ['bad json\n', '{"ok":true,"version":"wrong","run":"r-client-test"}\n', '']) {
    const server = net.createServer((socket) => {
      socket.on('error', () => {});
      socket.on('data', () => { if (response) socket.end(response); });
    });
    await new Promise((resolve) => server.listen(socketPath, () => resolve(undefined)));
    try {
      assert.equal(await native.daemonRequest(f.run, { method: 'status' }, { timeoutMs: 30 }), null, 'malformed, mismatched or silent server falls back');
    } finally { await new Promise((resolve) => server.close(resolve)); }
  }
});
