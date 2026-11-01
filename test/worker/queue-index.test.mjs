import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { appendFileSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import fs from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { after, mock, test } from 'node:test';
import { fileURLToPath } from 'node:url';

// Count actual ledger bytes at the filesystem boundary, including the old full reader.
const reads = new Map();
function count(file, bytes, full = false) {
  const name = String(file);
  const value = reads.get(name) ?? { bytes: 0, full: 0 };
  value.bytes += bytes;
  value.full += Number(full);
  reads.set(name, value);
}
mock.module('node:fs/promises', { namedExports: { ...fs,
  readFile: async (file, ...args) => {
    const data = await fs.readFile(file, ...args);
    count(file, Buffer.byteLength(data), true);
    return data;
  },
  open: async (file, flags, mode) => {
    const handle = await fs.open(file, flags, mode);
    const read = handle.read.bind(handle);
    handle.read = async (...params) => {
      const result = await read(...params);
      count(file, result.bytesRead, params[3] === 0);
      return result;
    };
    return handle;
  },
} });

const { ledgerPath } = await import('../../src/ledger/paths.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
// On the base code exercise the existing reader, so red is a byte/row assertion.
const { createTailReader } = await import('../../src/ledger/tail.mjs').catch((err) => {
  if (err.code !== 'ERR_MODULE_NOT_FOUND') throw err;
  return { createTailReader: (slug) => ({ read: () => readAllRows(slug) }) };
});
const { openBlock } = await import('../../src/state/block.mjs');
const { startRun, readRun } = await import('../../src/state/run.mjs');
const { loadKey, signRow } = await import('../../src/state/signer.mjs');
const { writeDoneFile } = await import('../../src/state/block-end.mjs');
const { daemonRequest } = await import('../../src/util/native.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { announceWorker, enqueue, pendingTickets, queueDir, retractWorker, writeResult } = await import('../../src/worker/queue.mjs');
const { waitReady } = await import('../helpers/wait-ready.mjs');
const root = fileURLToPath(new URL('../../', import.meta.url));
const scratch = '/tmp';
const binary = path.join(scratch, `queue-index-native-${process.pid}`);
after(() => rmSync(binary, { force: true }));
let built = false;
function build(t) {
  if (spawnSync('go', ['version']).error?.['code'] === 'ENOENT') {
    t.skip('Go is missing');
    return false;
  }
  if (!built) {
    const result = spawnSync(process.execPath, ['scripts/build-native.mjs', '--host'], { cwd: root, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    copyFileSync(path.join(root, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native'), binary);
    built = true;
  }
  return true;
}
async function fixture(t) {
  const dir = mkdtempSync(path.join(scratch, 'queue-index-'));
  const saved = { HOME: process.env.HOME, CODE_FORGE_KEY_BACKEND: process.env.CODE_FORGE_KEY_BACKEND, CODE_FORGE_NATIVE: process.env.CODE_FORGE_NATIVE };
  process.env.HOME = path.join(dir, 'h');
  process.env.CODE_FORGE_KEY_BACKEND = 'file';
  process.env.CODE_FORGE_NATIVE = '1';
  const home = process.env.HOME;
  const repo = path.join(dir, 'repo');
  mkdirSync(repo);
  for (const args of [['init', '-q'], ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '--allow-empty', '-m', 'base']]) {
    const result = spawnSync('git', args, { cwd: repo, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
  }
  writeFileSync(path.join(repo, '.code-forge.yml'), 'version: 1\nproject:\n  slug: queue-index\nprovider: anthropic\nlevels:\n  L2:\n    model: claude-opus-5-5\n');
  const run = 'r-queue-index';
  await startRun({ workspace: repo, project: 'queue-index', runId: run, writeRow: async () => {} });
  for (const [block, file] of [['B1', 'a.mjs'], ['B2', 'b.mjs']]) {
    writeFileSync(path.join(repo, file), 'export const x = 1;\n');
    await openBlock({ runId: run, id: block, level: 'L2', owned: [file], acceptance: [{ clause: 'fixture', tests: ['fixture'] }], writeRow: async () => {} });
  }
  const key = await loadKey(run);
  const children = [];
  const socket = path.join(home, '.code-forge', 'runs', run, 'native.sock');
  t.after(async () => {
    retractWorker(repo, process.pid);
    for (const child of children) if (child.exitCode === null && child.signalCode === null) {
      const exited = once(child, 'exit');
      child.kill('SIGTERM');
      await exited;
    }
    for (const [k, v] of Object.entries(saved)) if (v === undefined) delete process.env[k]; else process.env[k] = v;
    rmSync(dir, { recursive: true, force: true });
  });
  async function start(grace) {
    const env = { ...process.env };
    delete env.CODE_FORGE_PRUNE_GRACE;
    if (grace !== undefined) env.CODE_FORGE_PRUNE_GRACE = grace;
    const child = spawn(binary, ['serve', '--home', home, '--run', run], { env, stdio: 'ignore' });
    children.push(child);
    return waitReady(() => request(socket, { method: 'ping' }), { stop: () => child.exitCode !== null });
  }
  async function wait(native) {
    process.env.CODE_FORGE_NATIVE = native ? '1' : '0';
    let output = '';
    let error = '';
    const code = await runReviewFile(['--wait-block', 'B2', '--run', run, '--max', '0ms'], {
      cwd: repo, stdout: { write: (s) => { output += s; } }, stderr: { write: (s) => { error += s; } },
    });
    assert.equal(code, 0, error);
    return JSON.parse(output);
  }
  return { home, repo, run, key, socket, start, wait };
}
function request(socket, body) {
  return new Promise((resolve, reject) => {
    const client = net.createConnection(socket);
    let output = '';
    client.setTimeout(5000, () => client.destroy(new Error('request timed out')));
    client.on('error', reject);
    client.on('connect', () => client.write(`${JSON.stringify(body)}\n`));
    client.on('data', (chunk) => {
      output += chunk;
      if (output.includes('\n')) {
        client.destroy();
        try { resolve(JSON.parse(output.split('\n')[0])); } catch (err) { reject(err); }
      }
    });
  });
}
function queuePair(f) {
  announceWorker(f.repo, { pid: process.pid, run: f.run });
  return ['a', 'b'].map((name, i) => {
    const ticket = enqueue({ repoRoot: f.repo, run: f.run, block: `B${i + 1}`, file: `${name}.mjs` }).ticket;
    writeResult({ repoRoot: f.repo, runId: f.run, ticket, key: f.key, result: { status: 'reviewed', approved: true } });
    for (const suffix of ['delivered', 'retried']) writeFileSync(path.join(queueDir(f.repo), `${ticket}.${suffix}`), '{}');
    return ticket;
  });
}

test('Done tickets are pruned and wait-block output stays the same', async (t) => {
  if (!build(t)) return;
  const f = await fixture(t);
  const tickets = queuePair(f);
  const before = await f.wait(false);
  assert.equal(before.files.length, 1);
  assert.equal(before.files[0].status, 'done');
  await appendRow(signRow({ event: 'block.close', run: f.run, block: 'B1', status: 'complete' }, f.key), { slug: 'queue-index' });
  await f.start('0s');
  for (const [i, ticket] of tickets.entries()) for (const suffix of ['json', 'done', 'delivered', 'retried']) {
    assert.equal(existsSync(path.join(queueDir(f.repo), `${ticket}.${suffix}`)), i === 1, `B${i + 1} ${suffix} survival`);
  }
  assert.deepEqual(await f.wait(true), before);
  assert.deepEqual(await f.wait(false), before);
});

test('fresh close keeps tickets under default grace; ended runs require their own valid key', async (t) => {
  if (!build(t)) return;
  const f = await fixture(t);
  const tickets = queuePair(f);
  await appendRow(signRow({ event: 'block.close', run: f.run, block: 'B1', status: 'complete' }, f.key), { slug: 'queue-index' });
  const others = [];
  for (const kind of ['signed', 'unsigned', 'forged', 'missing']) {
    const run = `r-${kind}`;
    await startRun({ workspace: f.repo, project: 'queue-index', runId: run, writeRow: async () => {} });
    const key = await loadKey(run);
    const ticket = enqueue({ repoRoot: f.repo, run, block: 'B9', file: 'a.mjs' }).ticket;
    writeResult({ repoRoot: f.repo, runId: run, ticket, key, result: { status: 'reviewed', approved: true } });
    for (const suffix of ['delivered', 'retried']) writeFileSync(path.join(queueDir(f.repo), `${ticket}.${suffix}`), '{}');
    const row = { event: 'run.end', run, ts: '2020-01-01T00:00:00.000Z' };
    await appendRow(kind === 'unsigned' ? row : signRow(row, kind === 'forged' ? f.key : key), { slug: 'queue-index' });
    if (kind === 'missing') rmSync(path.join(f.home, '.code-forge', 'runs', `${run}.key`));
    others.push({ kind, ticket });
  }
  await f.start();
  assert.equal(existsSync(path.join(queueDir(f.repo), `${tickets[0]}.json`)), true, 'fresh block close gets ten minutes');
  for (const { kind, ticket } of others) for (const suffix of ['json', 'done', 'delivered', 'retried']) {
    assert.equal(existsSync(path.join(queueDir(f.repo), `${ticket}.${suffix}`)), kind !== 'signed', `${kind} run ${suffix}`);
  }
});

test('A wait or close reads only new ledger rows, not the full ledger', async (t) => {
  if (!build(t)) return;
  const f = await fixture(t);
  queuePair(f);
  const padding = Array.from({ length: 5000 }, (_, n) => JSON.stringify({ event: 'session', run: f.run, block: 'B1', n, cost_usd: 0.001 }) + '\n').join('');
  const file = ledgerPath('queue-index');
  mkdirSync(path.dirname(file), { recursive: true });
  appendFileSync(file, padding);
  const initial = await f.start();
  const extra = [1, 2, 3].map((n) => JSON.stringify({ event: 'session', run: f.run, block: 'B2', n, cost_usd: 0.2 }) + '\n').join('');
  appendFileSync(file, extra);
  await f.wait(true);
  const reply = await request(f.socket, { method: 'rows', block: 'B2' });
  assert.deepEqual(reply.rows, extra.trim().split('\n').map((line) => JSON.parse(line)), 'exactly three B2 rows in ledger order');
  assert.equal(reply.stats.bytes_read - initial.stats.bytes_read, Buffer.byteLength(extra));
  const all = await request(f.socket, { method: 'rows' });
  assert.equal(all.rows.length, 5003, 'unfiltered rows include both blocks');
  /** @type {Parameters<typeof writeDoneFile>[0]} */
  const options = { record: await readRun(f.run), id: 'B2', status: 'closed', event: 'block.close', now: new Date('2026-01-01'), openFindings: async (rows) => rows.filter((r) => r.block === 'B2').length, err: assert.fail };
  process.env.CODE_FORGE_NATIVE = '0';
  const done = await writeDoneFile(options);
  const expected = readFileSync(done, 'utf8');
  reads.delete(file);
  process.env.CODE_FORGE_NATIVE = '1';
  await writeDoneFile(options);
  assert.equal(readFileSync(done, 'utf8'), expected, 'done file is byte-identical');
  assert.equal(reads.get(file)?.bytes ?? 0, 0, 'daemon close reads zero ledger bytes in Node');
});

test('tail reads only appended bytes, preserves cross-run rows, and leaves pending queue order unchanged', async (t) => {
  const f = await fixture(t);
  const file = ledgerPath('queue-index');
  await appendRow({ event: 'session', run: 'r-older', n: 0 }, { slug: 'queue-index' });
  const reader = createTailReader('queue-index');
  const first = await reader.read();
  assert.equal(first.length, 1);
  first.push({ event: 'caller-mutation' });
  reads.delete(file);
  const extra = '{"event":"session","run":"r-current","text":"á😀"}\n';
  appendFileSync(file, extra);
  const actual = await reader.read();
  assert.equal(reads.get(file)?.bytes, Buffer.byteLength(extra), 'second read consumes only appended bytes');
  assert.deepEqual(actual, await readAllRows('queue-index'));
  const tickets = [3, 1, 2, 0].map((n) => {
    writeFileSync(path.join(f.repo, `q${n}.mjs`), 'fixture');
    return enqueue({ repoRoot: f.repo, run: f.run, block: 'B2', file: `q${n}.mjs`, now: new Date(1700000000000 + n * 1000) }).ticket;
  });
  writeFileSync(path.join(queueDir(f.repo), `${tickets[0]}.done`), '{}');
  writeFileSync(path.join(queueDir(f.repo), `${tickets[2]}.delivered`), '{}');
  writeFileSync(path.join(queueDir(f.repo), `${tickets[3]}.done.123.abcdef01.fresh`), '{}');
  assert.deepEqual(pendingTickets(f.repo), [tickets[1], tickets[2]]);
});

test('tail holds a complete JSON value without newline; rotation, replacement and truncation reload history', async (t) => {
  await fixture(t);
  const file = ledgerPath('queue-index');
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, '{"event":"one"}\n{"event":"two"}');
  const reader = createTailReader('queue-index');
  assert.deepEqual(await reader.read(), [{ event: 'one' }], 'unterminated JSON waits for newline');
  appendFileSync(file, '\n');
  assert.deepEqual(await reader.read(), await readAllRows('queue-index'));
  renameSync(file, file.replace('.jsonl', '.1.jsonl'));
  writeFileSync(file, '{"event":"three"}\n');
  reads.delete(file);
  assert.deepEqual(await reader.read(), await readAllRows('queue-index'));
  assert.ok(reads.get(file.replace('.jsonl', '.1.jsonl'))?.bytes > 0, 'rotation rereads history');
  writeFileSync(file, '{}\n');
  assert.deepEqual(await reader.read(), await readAllRows('queue-index'));
  renameSync(file, `${file}.old`);
  writeFileSync(file, '{"event":"replacement"}\n');
  assert.deepEqual(await reader.read(), await readAllRows('queue-index'));
  appendFileSync(file, '{"event":');
  assert.deepEqual(await reader.read(), await readAllRows('queue-index'));
  appendFileSync(file, '"finished"}\n');
  assert.deepEqual(await reader.read(), await readAllRows('queue-index'));
  appendFileSync(file, 'malformed\n{"event":"after"}\n');
  await assert.rejects(() => reader.read(), SyntaxError);
});

test('worker default reader processes three tickets with one full ledger read', async (t) => {
  const f = await fixture(t);
  await appendRow({ event: 'session', run: 'r-older', n: 7 }, { slug: 'queue-index' });
  const file = ledgerPath('queue-index');
  reads.delete(file);
  let reviewed = 0;
  const worker = await createWorker({ runId: f.run, repoRoot: f.repo, cfg: { review: { parallel_tickets: 1 } }, runRootDir: path.join(f.repo, 'tmp'), slug: 'queue-index', key: f.key }, {
    store: await createKeyStore({ backends: [], dir: path.join(f.repo, 'store') }), env: {}, expiryCheck: async () => {}, writeRow: async () => {},
    review: async (_ticket, ctx) => {
      assert.equal((await ctx.readRows()).filter((r) => r.run === 'r-older').length, 1);
      reviewed++;
      return { status: 'reviewed', approved: true, engine: 'test', sessions: [] };
    },
  });
  t.after(() => worker.stop());
  for (const name of ['a.mjs', 'b.mjs', 'c.mjs']) {
    writeFileSync(path.join(f.repo, name), 'export const x = 1;\n');
    enqueue({ repoRoot: f.repo, run: f.run, block: 'B1', file: name });
  }
  assert.equal(await worker.drain(), 3, 'all three queued tickets processed');
  assert.equal(reviewed, 3, 'all three reached the engine');
  assert.equal(reads.get(file)?.full, 1, 'one initial ledger read across three tickets');
});

test('daemonRequest rejects malformed rows replies', async (t) => {
  const f = await fixture(t);
  const pkg = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8'));
  /** @type {any} */
  let rows = [null];
  mkdirSync(path.dirname(f.socket), { recursive: true });
  const server = net.createServer((socket) => {
    let buffer = '';
    socket.on('data', (chunk) => {
      buffer += chunk;
      while (buffer.includes('\n')) {
        const end = buffer.indexOf('\n');
        const req = JSON.parse(buffer.slice(0, end));
        buffer = buffer.slice(end + 1);
        socket.write(JSON.stringify(req.method === 'ping' ? { ok: true, version: pkg.version, run: f.run } : { rows }) + '\n');
      }
    });
  });
  server.listen(f.socket);
  await once(server, 'listening');
  t.after(() => new Promise((resolve) => server.close(resolve)));
  for (const invalid of [[null], [[]], [1], {}, 'rows']) {
    rows = invalid;
    assert.equal(await daemonRequest(f.run, { method: 'rows' }), null, `reject ${JSON.stringify(invalid)}`);
  }
  rows = [{ event: 'session' }];
  assert.deepEqual(await daemonRequest(f.run, { method: 'rows' }), { rows });
});
