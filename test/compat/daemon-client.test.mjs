import assert from 'node:assert/strict';
import childProcess, { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import fs, { copyFileSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { after, mock, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const binary = path.join(root, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const daemonBinary = `${binary}-client-test-${process.pid}`;
let daemonSpawns = 0;
const daemonChildren = [];
mock.module('node:child_process', { defaultExport: childProcess, namedExports: { ...childProcess, spawn: (file, args, options) => {
  if (args?.[0] === 'serve') daemonSpawns++;
  const child = spawn(file === binary ? daemonBinary : file, args, options);
  if (args?.[0] === 'serve') daemonChildren.push(child);
  return child;
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
const { default: status, statusOf } = await import('../../src/cli/status.mjs');
const { loadProjectConfig } = await import('../../src/config/load.mjs');
const native = await import('../../src/util/native.mjs');
const { startRun, readRun } = await import('../../src/state/run.mjs');
const { runRecordPath } = await import('../../src/state/paths.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const queue = await import('../../src/worker/queue.mjs');
const { announceWorker, blockTickets, queueDir, retractWorker, writeResult } = queue;
let announceFailure = null;
mock.module('../../src/worker/queue.mjs', { namedExports: { ...queue, announceWorker: (repo, who) => {
  if (announceFailure) {
    assert.equal(existsSync(announceFailure.socket), true, 'daemon socket exists before announcement fails');
    assert.equal(daemonChildren.at(-1).exitCode, null, 'daemon is running before announcement fails');
    throw announceFailure.error;
  }
  return queue.announceWorker(repo, who);
} } });
const { bootWorker, createWorker } = await import('../../src/worker/loop.mjs');
mock.module('../../src/doctor/index.mjs', { namedExports: { runDoctor: async () => [] } });
const { runDoctorCli } = await import('../../src/cli/doctor.mjs');

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
  const firstDaemon = daemonChildren.length;
  t.after(async () => {
    announceFailure = null;
    retractWorker(repo, process.pid);
    for (const child of [...children, ...daemonChildren.slice(firstDaemon)]) {
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
  async function doctor() {
    let text = '';
    await runDoctorCli(['--json', '--cwd', repo], {
      stdout: { write: (s) => { text += s; } }, stderr: { write: () => {} },
    });
    const rows = JSON.parse(text).rows.filter((row) => row.id === 'native_daemon');
    assert.equal(rows.length, 1, 'doctor reports exactly one daemon diagnostic');
    assert.equal(rows[0].label, 'native daemon');
    return rows[0].detail;
  }
  return { home, run, repo, start, wait, show, doctor, config, key: await loadKey(run) };
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

test('status text with a live daemon equals CODE_FORGE_NATIVE=0 for the same run', async (t) => {
  const f = await fixture(t);
  await f.start();
  ledgerReads = 0;
  const live = await f.show(false);
  assert.equal(ledgerReads, 0, 'live status uses the daemon');
  assert.equal(await f.doctor(), 'live');
  process.env.CODE_FORGE_NATIVE = '0';
  ledgerReads = 0;
  const node = await f.show(false);
  assert.ok(ledgerReads > 0, 'disabled daemon uses the Node status path');
  assert.equal(live, node, 'the complete stdout text is byte-identical');
  assert.doesNotMatch(node, /native daemon:/);
  assert.equal(await f.doctor(), 'off');
});

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
  ledgerReads = 0;
  const json = await f.show();
  assert.ok(ledgerReads > 0, 'Node-only JSON status exercises the client ledger reader');
  assert.equal(json, `${JSON.stringify({ runs: [await statusOf(await readRun(f.run))] })}\n`, 'JSON contains exactly the Node run summary, without daemon metadata');
  ledgerReads = 0;
  const text = await f.show(false);
  assert.ok(ledgerReads > 0, 'Node-only human status exercises the client ledger reader');
  assert.doesNotMatch(text, /native daemon:/);
  assert.equal(await f.doctor(), 'off');
  delete process.env.CODE_FORGE_NATIVE;
  ledgerReads = 0;
  assert.equal(await f.wait(), done, 'done wait bytes preserve file and timing key order');
  assert.equal(await f.show(false), text, 'full human status bytes equal Node with CODE_FORGE_NATIVE=0');
  assert.equal(await f.show(), json, 'full JSON status bytes equal Node without removing any key');
  assert.equal(ledgerReads, 0, 'live wait and both status renders call the client ledger reader zero times');
  assert.equal(await f.doctor(), 'live');
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
  const fallbackText = await f.show(false);
  assert.doesNotMatch(fallbackText, /native daemon:/);
  assert.equal(await f.doctor(), 'fallback');
  process.env.CODE_FORGE_NATIVE = '0';
  assert.equal(actual, await f.wait(), 'mid-wait fallback finishes with identical bytes');
  assert.equal(fallbackStatus, await f.show(), 'full fallback JSON status bytes equal Node without removing any key');
  assert.equal(fallbackText, await f.show(false), 'full fallback human status bytes equal Node');
  delete process.env.CODE_FORGE_NATIVE;
  const socketPath = path.join(f.home, '.code-forge', 'runs', f.run, 'native.sock');
  assert.equal(existsSync(socketPath), true, 'SIGKILL leaves a stale socket file');
  assert.equal(await f.wait(), actual, 'stale socket uses the identical Node wait answer');
  assert.equal(await f.show(), fallbackStatus, 'stale socket uses the identical Node JSON status answer');
  assert.equal(await f.show(false), fallbackText, 'stale socket uses the identical Node human status answer');

  rmSync(socketPath);
  assert.equal(existsSync(socketPath), false, 'missing-socket checks run without a socket file');
  ledgerReads = 0;
  assert.equal(await f.wait(), actual, 'missing socket uses the identical Node wait answer');
  assert.ok(ledgerReads > 0, 'missing socket makes wait read through Node');
  ledgerReads = 0;
  assert.equal(await f.show(), fallbackStatus, 'missing socket uses the identical Node JSON status answer');
  assert.ok(ledgerReads > 0, 'missing socket makes JSON status read through Node');
  ledgerReads = 0;
  assert.equal(await f.show(false), fallbackText, 'missing socket uses the identical Node human status answer');
  assert.ok(ledgerReads > 0, 'missing socket makes human status read through Node');

  assert.equal(typeof native.startNativeDaemon, 'function', 'worker lifecycle helper is available');
  let connections = 0;
  const server = net.createServer((socket) => { connections++; socket.destroy(); });
  // Replace the removed stale socket with a connection counter.
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

test('broken project config without a snapshot still returns the Node status answer', async (t) => {
  const f = await fixture(t);
  const record = await readRun(f.run);
  delete record.config; // A run created before config snapshots must load the project file.
  writeFileSync(runRecordPath(f.run), JSON.stringify(record));
  writeFileSync(path.join(f.repo, '.code-forge.yml'), 'version: 1\nbudget:\n  usd: 5\n');
  process.env.CODE_FORGE_NATIVE = '0';
  assert.equal(JSON.parse(await f.show()).runs[0].budget_usd, 5, 'valid project config contributes the status budget');
  writeFileSync(path.join(f.repo, '.code-forge.yml'), 'version: [broken\n');
  const loaded = await loadProjectConfig(f.repo);
  assert.equal(loaded.ok, false, 'broken config returns a failed load rather than throwing');
  assert.equal(loaded.config, undefined, 'failed load has no config for the daemon client');
  const expected = `${JSON.stringify({ runs: [await statusOf(record)] })}\n`;
  assert.equal(await f.show(), expected, 'disabled daemon returns the complete Node answer despite broken config');
  assert.equal(JSON.parse(expected).runs[0].budget_usd, null, 'Node still renders status when the budget config cannot be loaded');
  await f.start();
  delete process.env.CODE_FORGE_NATIVE;
  ledgerReads = 0;
  assert.equal(await f.show(), expected, 'invalid config uses the same complete Node status JSON');
  assert.ok(ledgerReads > 0, 'invalid config disables the live daemon and reads through Node');
  assert.doesNotMatch(await f.show(false), /native daemon:/);
});

for (const mode of ['run', 'once', 'boot']) {
  test(`${mode}: an announcement failure reaps the daemon process and removes its socket before rejecting`, async (t) => {
    const f = await fixture(t);
    retractWorker(f.repo, process.pid);
    const failure = new Error('announcement failed');
    const beforeSpawns = daemonSpawns;
    announceFailure = { error: failure, socket: path.join(f.home, '.code-forge', 'runs', f.run, 'native.sock') };
    if (mode !== 'boot') {
      const worker = await createWorker({ runId: f.run, repoRoot: f.repo, cfg: f.config,
        runRootDir: path.join(f.home, 'worker-tmp'), slug: 'daemon-client-test', key: f.key,
      }, { writeRow: async () => {} });
      await assert.rejects(worker[mode](), (error) => error === failure);
    } else {
      await assert.rejects(bootWorker({ runId: f.run, cwd: f.repo }, { writeRow: async () => {} }), (error) => error === failure);
    }
    announceFailure = null;
    assert.equal(daemonSpawns - beforeSpawns, 1, 'failure occurs after starting a real daemon');
    const child = daemonChildren.at(-1);
    assert.ok(child.exitCode !== null || child.signalCode !== null, 'owned daemon child has exited before rejection');
    assert.throws(() => process.kill(child.pid, 0), { code: 'ESRCH' }, 'daemon pid no longer exists');
    assert.equal(await ping(f.home, f.run), false, 'no daemon answers after rejection');
    assert.equal(existsSync(path.join(f.home, '.code-forge', 'runs', f.run, 'native.sock')), false, 'daemon socket is removed');
    assert.equal(queue.liveWorker(f.repo), null, 'no worker announcement is left');
  });
}

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
