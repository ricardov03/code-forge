import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { performance } from 'node:perf_hooks';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { startRun, readRun, saveRun } from '../../src/state/run.mjs';
import { loadKey, signRow } from '../../src/state/signer.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { announceWorker, retractWorker } from '../../src/worker/queue.mjs';
import { waitReady } from '../helpers/wait-ready.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const scratch = mkdtempSync('/tmp/cfs-');
const pkg = path.join(scratch, 'pkg');
const binary = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const entry = path.join(pkg, 'bin/code-forge');
const version = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8')).version;
before(() => {
  mkdirSync(path.dirname(binary), { recursive: true });
  mkdirSync(path.dirname(entry));
  // Build directly into this process's private package: install tests may move
  // the shared release binary at any time, including between build and copy.
  const result = spawnSync('go', ['build', '-trimpath', '-ldflags', `-s -w -X main.version=${version}`, '-o', binary, './cmd/code-forge-native'], {
    cwd: path.join(root, 'native'), encoding: 'utf8', timeout: 120000,
    env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' },
  });
  assert.equal(result.status, 0, result.stderr);
  copyFileSync(path.join(root, 'bin/code-forge'), entry);
  symlinkSync(path.join(root, 'bin/code-forge.mjs'), path.join(pkg, 'bin/code-forge.mjs'));
  symlinkSync(path.join(root, 'src'), path.join(pkg, 'src'));
});
after(() => rmSync(scratch, { recursive: true, force: true }));

function childrenOf(...parents) {
  const ps = spawnSync('/bin/ps', ['-axo', 'pid=,ppid='], { encoding: 'utf8' });
  assert.equal(ps.status, 0, ps.stderr);
  return ps.stdout.trim().split('\n').map((line) => line.trim().split(/\s+/).map(Number))
    .filter(([pid, ppid]) => pid !== ps.pid && parents.includes(ppid)).map(([pid]) => pid).sort((a, b) => a - b);
}

async function fixture(t) {
  const dir = mkdtempSync(path.join(scratch, 'f-'));
  const home = path.join(dir, 'h');
  const repo = path.join(dir, 'repo');
  const previous = process.env.HOME;
  const previousBackend = process.env.CODE_FORGE_KEY_BACKEND;
  process.env.HOME = home;
  process.env.CODE_FORGE_KEY_BACKEND = 'file';
  mkdirSync(repo);
  assert.equal(spawnSync('git', ['init', '-q'], { cwd: repo }).status, 0);
  writeFileSync(path.join(repo, '.code-forge.yml'), 'version: 1\nproject:\n  slug: status-test\nbudget:\n  usd: 8\n');
  const run = 'r-status-test';
  const config = { native: { daemon: true } };
  await startRun({ workspace: repo, project: 'status-test', runId: run, config, writeRow: async () => {} });
  const worker = announceWorker(repo, { pid: process.pid, run });
  let record = await readRun(run);
  record.worker = { pid: worker.pid, started_at: worker.start_time };
  record.workers = [record.worker];
  record.blocks = {
    B1: { status: 'open', level: 'L2' },
    B2: { status: 'awaiting_human_check', level: 'L1' },
    B3: { status: 'closed' }, B4: { status: 'stopped' },
  };
  await saveRun(record);
  await appendRow(signRow({ event: 'session', run, block: 'B1', role: 'coder', provider: 'fixture', model: '<model>&é', usd: 0.0000001 }, await loadKey(run)), { slug: 'status-test' });
  await appendRow({ event: 'gate.red', run, block: 'B1', reason: 'timeout', gate: 'check' }, { slug: 'status-test' });
  await appendRow({ event: 'gate.done', run, block: 'B1', all_ok: false, flaky: ['a.mjs'] }, { slug: 'status-test' });
  const socket = path.join(home, '.code-forge/runs', run, 'native.sock');
  const children = [];
  const helperPids = new Set();
  async function stop() {
    for (const child of children) {
      if (child.exitCode === null && child.signalCode === null) {
        for (const pid of childrenOf(child.pid)) helperPids.add(pid);
        const exited = once(child, 'exit');
        child.kill('SIGTERM');
        assert.deepEqual(await exited, [0, null], 'daemon exits zero on SIGTERM');
      }
    }
    assert.equal(existsSync(socket), false, 'zero native.sock files remain after daemon shutdown');
    assert.deepEqual(childrenOf(process.pid, ...children.map((c) => c.pid)), [], 'zero test or daemon children remain');
    for (const pid of helperPids) assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' }, `helper ${pid} was reaped`);
  }
  t.after(async () => {
    try { await stop(); } finally {
      retractWorker(repo, process.pid);
      if (previous === undefined) delete process.env.HOME; else process.env.HOME = previous;
      if (previousBackend === undefined) delete process.env.CODE_FORGE_KEY_BACKEND; else process.env.CODE_FORGE_KEY_BACKEND = previousBackend;
      rmSync(dir, { recursive: true, force: true });
    }
  });
  const env = { ...process.env, HOME: home, CODE_FORGE_NODE: process.execPath, CODE_FORGE_NATIVE: '1', CODE_FORGE_KEY_BACKEND: 'file' };
  async function command(args, extra = {}, directNode = false) {
    const child = spawn(directNode ? process.execPath : entry, [...(directNode ? [path.join(root, 'bin/code-forge.mjs')] : []), 'status', ...args], { cwd: repo, env: { ...env, ...extra }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    const [code, signal] = await once(child, 'close');
    assert.equal(signal, null, 'status exits normally');
    return { stdout, stderr, code };
  }
  async function start() {
    const child = spawn(binary, ['internal', 'serve', '--home', home, '--run', run], { cwd: repo, env, stdio: ['ignore', 'ignore', 'pipe'] });
    children.push(child);
    let stderr = '';
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    await waitReady(async () => {
      const answer = await ping(socket);
      return answer?.ok === true;
    }, { stop: () => child.exitCode !== null });
    assert.equal(child.exitCode, null, stderr);
    return child;
  }
  async function change(update) {
    record = await readRun(run);
    update(record);
    await saveRun(record);
  }
  return { run, socket, command, start, stop, change, config, repo };
}

function ping(socket) {
  return new Promise((resolve) => {
    const client = net.createConnection(socket);
    client.setTimeout(200, () => client.destroy());
    client.on('connect', () => client.write('{"method":"ping"}\n'));
    client.on('error', () => resolve(null));
    client.on('close', () => resolve(null));
    client.on('data', (chunk) => {
      try { resolve(JSON.parse(chunk.toString())); } catch { resolve(null); }
      client.destroy();
    });
  });
}

test('direct Go status preserves Node bytes and exit codes across forms and daemon configurations', async (t) => {
  const f = await fixture(t);
  const args = ['--json', '--run', f.run];
  const daemon = await f.start();
  const expected = await f.command(args, { CODE_FORGE_NATIVE: '0' });
  assert.equal(expected.code, 0);
  assert.equal(expected.stderr, '');
  const summary = JSON.parse(expected.stdout).runs[0];
  assert.equal(summary.blocks.length, 2, 'fixture has two unfinished blocks');
  assert.deepEqual(summary.awaiting_human_check, ['B2']);
  assert.deepEqual(summary.flaky, ['a.mjs']);
  assert.equal(summary.blocks[0].gate.result, 'timeout');
  for (const flags of [args, ['--run', f.run, '--json'], [`--run=${f.run}`, '--json']]) {
    assert.deepEqual(await f.command(flags), expected, 'Go stdout, stderr and code equal Node byte for byte');
    assert.deepEqual(await f.command(flags, { CODE_FORGE_NODE: '/nonexistent' }), expected, 'Go serves status when Node cannot start');
  }
  const warmChildren = childrenOf(process.pid, daemon.pid);
  const times = { go: [], node: [] };
  for (let i = 0; i < 15; i++) {
    for (const mode of ['go', 'node']) {
      const started = performance.now();
      assert.deepEqual(await f.command(args, { CODE_FORGE_NATIVE: mode === 'go' ? '1' : '0' }), expected);
      times[mode].push(performance.now() - started);
    }
  }
  t.diagnostic(`15-run median ms: Go ${times.go.sort((a, b) => a - b)[7].toFixed(2)}, Node ${times.node.sort((a, b) => a - b)[7].toFixed(2)}`);
  assert.deepEqual(childrenOf(process.pid, daemon.pid), warmChildren, 'status leaves exactly the existing daemon and companion');
  for (const flags of [[], ['--json'], ['--run', f.run], ['--json', '--run', 'r-missing'], ['--json', '--run'], ['--json=true'], ['--json', '--json'], ['--run', '', '--run', f.run, '--json'], ['--run', f.run, '--run', f.run, '--json'], ['--wat'], ['unexpected'], ['--run', '../escape', '--json']]) {
    const node = await f.command(flags, { CODE_FORGE_NATIVE: '0' });
    assert.deepEqual(await f.command(flags), node, `direct Go preserves ${JSON.stringify(flags)}`);
    assert.deepEqual(await f.command(flags, { CODE_FORGE_NODE: '/nonexistent' }), node, `${JSON.stringify(flags)} is served without Node`);
  }
  // Direct status reads disk independently of the daemon's config and snapshots.
  writeFileSync(path.join(f.repo, '.code-forge.yml'), 'version: 1\nproject:\n  slug: status-test\nnative:\n  daemon: false\nbudget:\n  usd: 8\n');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), expected, 'enabled snapshot wins over disabled workspace config');
  await f.change((record) => { delete record.config; });
  assert.deepEqual(await f.command(args), expected, 'legacy record with disabled daemon preserves Node bytes');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), expected);
  writeFileSync(path.join(f.repo, '.code-forge.yml'), 'version: 1\nproject:\n  slug: status-test\nbudget:\n  usd: 8\n');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), expected, 'legacy record uses enabled workspace config');
  await f.change((record) => {
    record.config = initialConfigState(f.config);
    record.config.snapshots[record.config.hash].native.daemon = false;
  });
  assert.deepEqual(await f.command(args), expected, 'corrupt daemon snapshot does not affect direct status');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), expected);
  await f.change((record) => {
    const disabled = initialConfigState({ native: { daemon: false } });
    record.config = initialConfigState(f.config);
    Object.assign(record.config.snapshots, disabled.snapshots);
    record.config.pins[''] = disabled.hash;
  });
  assert.deepEqual(await f.command(args), expected, 'empty-ticket config pin disables the daemon');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), expected);
  await f.change((record) => { record.config = initialConfigState({ native: { daemon: false } }); });
  const disabled = await f.command(args, { CODE_FORGE_NATIVE: '0' });
  assert.deepEqual(await f.command(args), disabled, 'disabled snapshot preserves Node bytes');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), disabled, 'disabled daemon still serves direct status');
  await f.change((record) => { record.config = initialConfigState(f.config); delete record.worker; record.workers = []; });
  const warning = await f.command(args, { CODE_FORGE_NATIVE: '0' });
  assert.match(warning.stderr, /warning: live worker .* is not pinned/);
  assert.deepEqual(await f.command(args), warning, 'direct status preserves the unpinned worker warning');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), warning);
  await f.stop();
  // Change disk state so shutdown checks cannot reuse the live-daemon expectation.
  await f.change((record) => { record.blocks.B1.status = 'closed'; });
  const down = await f.command(args, { CODE_FORGE_NATIVE: '0' });
  assert.deepEqual(await f.command(args), down, 'down daemon preserves Node bytes');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), down, 'down daemon still serves direct status');
  await appendRow({ event: 'review.answered', run: f.run, block: 'B1' }, { slug: 'status-test' });
  const unsupported = await f.command(args, { CODE_FORGE_NATIVE: '0' });
  assert.deepEqual(await f.command(args), unsupported, 'unsupported review state still hands over with exact Node bytes');
  const handed = await f.command(args, { CODE_FORGE_NODE: '/nonexistent' });
  assert.equal(handed.code, 127, 'unsupported review state must still start Node');
  assert.equal(handed.stdout, '', 'handover emits no partial summary');
});

test('direct status ignores missing socket, bad JSON, timeout, oversized and odd daemon answers, leaving no processes or socket', async (t) => {
  const f = await fixture(t);
  const args = ['--json', '--run', f.run];
  const expected = await f.command(args, { CODE_FORGE_NATIVE: '0' });
  // A live daemon and a missing socket must produce the same direct answer.
  await f.start();
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), expected, 'healthy Go client returns exactly one summary without Node');
  await f.stop();
  assert.deepEqual(await f.command(args), expected, 'no socket preserves Node bytes');
  assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), expected, 'no socket: served directly without Node');
  for (const fault of ['bad JSON', 'timeout', 'oversized', 'odd status', 'odd fields', 'wrong version', 'wrong run']) {
    const sockets = new Set();
    let connections = 0;
    const server = net.createServer((socket) => {
      connections++;
      sockets.add(socket);
      socket.on('error', () => {});
      socket.on('close', () => sockets.delete(socket));
      let buffer = '';
      socket.on('data', (chunk) => {
        buffer += chunk;
        while (buffer.includes('\n')) {
          const end = buffer.indexOf('\n');
          const req = JSON.parse(buffer.slice(0, end));
          buffer = buffer.slice(end + 1);
          if (req.method === 'ping') {
            socket.write(`${JSON.stringify({ ok: true, version: fault === 'wrong version' ? 'incompatible' : version, run: fault === 'wrong run' ? 'r-other' : f.run })}\n`);
          } else if (fault === 'bad JSON') socket.end('{broken}\n');
          else if (fault === 'oversized') socket.end(`${JSON.stringify({ padding: 'x'.repeat(8 * 1024 * 1024) })}\n`);
          else if (fault === 'odd status') socket.end('{"runs":[{"run_id":"r-other","blocks":[]}]}\n');
          else if (fault === 'odd fields') {
            const answer = JSON.parse(expected.stdout);
            answer.runs[0].blocks[0].model = { z: 1, a: 2 };
            socket.end(`${JSON.stringify(answer)}\n`);
          }
          // timeout deliberately leaves the method unanswered.
        }
      });
    });
    await new Promise((resolve) => server.listen(f.socket, () => resolve(undefined)));
    try {
      assert.deepEqual(await f.command(args), expected, `${fault}: exact Node output and exit`);
      assert.deepEqual(await f.command(args, { CODE_FORGE_NODE: '/nonexistent' }), expected, `${fault}: served directly without Node`);
      assert.equal(connections, 0, `${fault}: direct status makes zero daemon connections`);
    } finally {
      for (const socket of sockets) socket.destroy();
      await new Promise((resolve) => server.close(resolve));
    }
    assert.equal(existsSync(f.socket), false, `${fault}: zero sockets remain`);
    assert.deepEqual(childrenOf(process.pid), [], `${fault}: zero children remain`);
  }
});
