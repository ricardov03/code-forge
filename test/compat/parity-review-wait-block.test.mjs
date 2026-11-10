import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { chmod, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const pkg = await mkdtemp(path.join(os.tmpdir(), 'cf-wait-block-'));
const BINARY = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const RUN = 'r-wait-block', BLOCK = 'B1';
const ARGS = ['review-file', '--wait-block', BLOCK, '--run', RUN, '--max=0ms'];
const NO_NODE = '/nonexistent-code-forge-node';
const pids = [];
after(async () => {
  assert.deepEqual(pids.filter(alive), [], 'zero CLI processes survive the suite');
  await rm(pkg, { recursive: true, force: true });
});
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(pkg, 'bin'));
  await symlink(CLI, path.join(pkg, 'bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  linkGoBinary(BINARY, { trimpath: true, ldflags: `-s -w -X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}` });
});

function alive(pid) { try { process.kill(pid, 0); return true; } catch { return false; } }
async function fixture(t, config = {}) {
  const dir = await mkdtemp(path.join(pkg, 'f-'));
  // Unix socket paths have a small platform limit; keep HOME outside the long package path.
  const home = await mkdtemp(path.join('/tmp', 'cf-wb-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const repo = path.join(dir, 'repo');
  await mkdir(repo);
  for (const args of [['init', '-q', repo], ['-C', repo, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'base', '--allow-empty']]) {
    const git = spawnSync('git', args, { encoding: 'utf8' });
    assert.equal(git.status, 0, `fixture git: ${git.stderr}`);
  }
  const runs = path.join(repo, '.code-forge/runs');
  await mkdir(runs, { recursive: true });
  const record = { run_id: RUN, workspace: repo, project: 'fixture', config: initialConfigState(config), blocks: { [BLOCK]: { owned_files: ['sample.mjs'], base_sha: 'HEAD' } } };
  await writeFile(path.join(runs, `${RUN}.json`), JSON.stringify(record));
  const tools = path.join(dir, 'tools'), pidFile = path.join(dir, 'git-pids');
  await mkdir(tools);
  await writeFile(path.join(tools, 'git'), `#!/bin/sh\necho $$ >> '${pidFile}'\nexec /usr/bin/git "$@"\n`);
  await chmod(path.join(tools, 'git'), 0o755);
  const argvNode = path.join(tools, 'node-argv');
  await writeFile(argvNode, `#!${process.execPath}\nprocess.stdout.write(JSON.stringify(process.argv.slice(3)) + '\\n');\n`);
  await chmod(argvNode, 0o755);
  t.after(async () => {
    const observed = (await readFile(pidFile, 'utf8')).trim().split('\n').map(Number);
    assert.ok(observed.length >= 2, 'at least two git discovery children were observed');
    assert.deepEqual(observed.filter(alive), [], 'zero observed git children survive the CLI invocations');
  });
  return { repo, home, record, runs, tools, argvNode };
}

async function daemon(t, f, answers) {
  const socketPath = path.join(f.home, '.code-forge/runs', RUN, 'native.sock');
  await mkdir(path.dirname(socketPath), { recursive: true });
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const sockets = new Set(), requests = [];
  let index = 0;
  const server = net.createServer((socket) => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
    socket.on('error', () => {});
    let buffer = '';
    socket.on('data', (chunk) => {
      buffer += chunk;
      while (buffer.includes('\n')) {
        const at = buffer.indexOf('\n'), request = JSON.parse(buffer.slice(0, at));
        buffer = buffer.slice(at + 1);
        if (request.method === 'ping') socket.write(`${JSON.stringify({ ok: true, version, run: RUN })}\n`);
        else {
          requests.push(request);
          const answer = answers[Math.min(index++, answers.length - 1)];
          if (answer === 'disconnect') socket.destroy();
          else socket.write(`${typeof answer === 'string' ? answer : JSON.stringify(answer)}\n`);
        }
      }
    });
  });
  await new Promise((resolve) => server.listen(socketPath, () => resolve(undefined)));
  t.after(async () => {
    for (const socket of sockets) socket.destroy();
    await new Promise((resolve) => server.close(resolve));
    assert.equal(sockets.size, 0, 'zero daemon connections survive close');
  });
  return { requests, reset: () => { index = 0; requests.length = 0; } };
}

async function execute(f, args, { node = false, client = false, env = {} } = {}) {
  // Disable native CLI routing for the Node oracle. Only re-enable its daemon
  // client when comparing the existing daemon-answer rendering branch.
  const source = `import { mock } from 'node:test';
    const url = ${JSON.stringify(new URL('../../src/util/native.mjs', import.meta.url).href)};
    const actual = await import(url);
    mock.module(url, { namedExports: { ...actual, daemonRequest: async (...args) => {
      process.env.CODE_FORGE_NATIVE = '1';
      try { return await actual.daemonRequest(...args); }
      finally { process.env.CODE_FORGE_NATIVE = '0'; }
    } } });
    process.argv = [process.execPath, ${JSON.stringify(CLI)}, ...process.argv.slice(1)];
    await import(${JSON.stringify(CLI)});`;
  const command = node ? process.execPath : BINARY;
  const argv = node ? (client ? ['--experimental-test-module-mocks', '--input-type=module', '-e', source, ...args] : [CLI, ...args]) : args;
  const child = spawn(command, argv, { cwd: f.repo, env: {
    PATH: `${f.tools}:${process.env.PATH}`, HOME: f.home, CODE_FORGE_NATIVE: node ? '0' : '1',
    CODE_FORGE_NODE: process.execPath, CODE_FORGE_NO_ERROR_LOG: '1', ...env,
    NODE_NO_WARNINGS: '1',
  }, stdio: ['ignore', 'pipe', 'pipe'] });
  pids.push(child.pid);
  const stdout = [], stderr = [];
  child.stdout.on('data', (s) => stdout.push(s)); child.stderr.on('data', (s) => stderr.push(s));
  const [code, signal] = await once(child, 'close');
  assert.equal(alive(child.pid), false, 'CLI process is reaped after close');
  return { code, signal, stdout: Buffer.concat(stdout).toString(), stderr: Buffer.concat(stderr).toString() };
}

const file = { file: 'café<&>.mjs', ticket: 'abcdef0123456789abcdef01', status: 'done', outcome: 'approved' };
const approved = { block: BLOCK, files: [file], done: true };
async function nativeRoute(f) {
  const result = await execute(f, ARGS, { env: { CODE_FORGE_NODE: NO_NODE } });
  assert.equal(result.code, 0, 'live wait-block runs in Go with Node unavailable');
  assert.equal(result.stdout, `${JSON.stringify(approved)}\n`, 'native route emits exactly one ordered approval');
  assert.equal(result.stderr, '', 'native route emits zero stderr bytes');
}

test('live daemon wait-block has byte and exit parity for every answer and timeout retry', async (t) => {
  const responses = /** @type {any[]} */ ([approved]);
  const f = await fixture(t), d = await daemon(t, f, responses);
  await nativeRoute(f);
  d.reset();
  const defaultMax = await execute(f, ARGS.slice(0, -1), { env: { CODE_FORGE_NODE: NO_NODE } });
  assert.equal(defaultMax.stdout, `${JSON.stringify(approved)}\n`, 'omitted max returns the exact native answer');
  assert.equal(defaultMax.code, 0, 'omitted max stays native');
  assert.equal(d.requests.length, 1, 'default max sends exactly one request');
  assert.ok(d.requests[0].max_ms > 89000 && d.requests[0].max_ms <= 90000, 'default deadline is 90 seconds');
  const cases = [
    approved,
    { block: BLOCK, files: [{ ...file, status: 'pending', outcome: null }], done: false, reason: 'wait_timeout' },
    { block: BLOCK, files: [{ ...file, status: 'findings', outcome: 'findings', queued_at: 'q', started_at: null, finished_at: 'f', duration_ms: 1.5 }], done: true },
    { block: BLOCK, files: [{ file: 'skip.md', ticket: null, status: 'excluded', outcome: null, rule: '**/*.md' }], done: true },
    { block: BLOCK, files: [], done: false, status: 'worker_down' },
    { block: BLOCK, files: [file], done: false, reason: 'split_required', next: '<owner> raises budget' },
    { block: BLOCK, files: [{ file: 'missing-ticket', status: 'pending' }], done: false, status: null, next: null },
  ];
  for (const answer of cases) {
    responses.splice(0, responses.length, answer);
    d.reset();
    const node = await execute(f, ARGS, { node: true, client: true });
    d.reset();
    const go = await execute(f, ARGS, { env: { CODE_FORGE_NODE: NO_NODE } });
    assert.deepEqual(go, node, `all stdout/stderr bytes and exit match for ${JSON.stringify(answer)}`);
    assert.equal(go.stdout, `${JSON.stringify(answer)}\n`, 'one exact ordered answer line');
    assert.equal(go.code, answer.status === 'worker_down' ? 3 : 0, 'worker_down alone exits 3');
    assert.deepEqual(d.requests, [{ method: 'wait-block', block: BLOCK, max_ms: 0 }], 'exactly one scoped request at the zero deadline');
  }
  responses.splice(0, responses.length, { ...approved, done: false, reason: 'wait_timeout' }, approved);
  const args = ['review-file', '--cwd', f.repo, `--wait-block=${BLOCK}`, `--run=${RUN}`, '--max', '2s'];
  d.reset();
  const node = await execute(f, args, { node: true, client: true });
  d.reset();
  const go = await execute(f, args, { env: { CODE_FORGE_NODE: NO_NODE } });
  assert.deepEqual(go, node, 'early wait_timeout retries to the identical final answer');
  assert.equal(go.stdout, `${JSON.stringify(approved)}\n`, 'early timeout prints no intermediate line');
  assert.equal(d.requests.length, 2, 'exactly two requests before the deadline');
  assert.ok(d.requests.every((r) => r.max_ms > 0 && r.max_ms <= 2000), 'remaining milliseconds stay inside the requested max');

  for (const answer of [approved, { ...approved, done: false, status: 'worker_down' }]) {
    responses.splice(0, responses.length, answer);
    const logs = path.join(f.home, '.code-forge/logs');
    const results = [];
    for (const nodeMode of [true, false]) {
      await rm(logs, { recursive: true, force: true });
      await mkdir(logs, { recursive: true });
      await writeFile(path.join(logs, 'breadcrumbs.json'), '["init","run start"]');
      const result = await execute(f, ARGS, { node: nodeMode, client: nodeMode, env: { CODE_FORGE_NO_ERROR_LOG: '0', ...(nodeMode ? {} : { CODE_FORGE_NODE: NO_NODE }) } });
      const crumbs = JSON.parse(await readFile(path.join(logs, 'breadcrumbs.json'), 'utf8'));
      let errors = [];
      try { errors = (await readFile(path.join(logs, 'errors.jsonl'), 'utf8')).trim().split('\n').map((s) => { const e = JSON.parse(s); delete e.ts; return e; }); }
      catch (err) { if (err.code !== 'ENOENT') throw err; }
      results.push({ result, crumbs, errors });
    }
    assert.deepEqual(results[1], results[0], 'logging fields, previous verbs, and breadcrumb count equal Node');
    assert.equal(results[1].errors.length, answer.status === 'worker_down' ? 1 : 0, 'exactly one error entry for worker_down');
    assert.deepEqual(results[1].crumbs, ['init', 'run start', 'review-file'], 'one breadcrumb per invocation');
  }
});

test('disabled, unreachable, invalid and stopped daemon answers hand over unchanged to Node', async (t) => {
  const f = await fixture(t), responses = /** @type {any[]} */ ([approved]), d = await daemon(t, f, responses);
  await nativeRoute(f);
  const cases = [
    { answer: approved, env: { CODE_FORGE_NATIVE: '0' } },
    { answer: approved, config: { native: { daemon: false } } },
    { answer: 'disconnect' }, { answer: 'bad JSON' },
    { answer: { ...approved, block: 'other' } },
    { answer: { ...approved, files: [{}] } },
    { answer: { ...approved, done: 'true' } },
    { answer: { ...approved, files: [{ ...file, outcome: 'stopped' }] } },
    { answer: approved, args: [...ARGS, '--unknown=value'] },
    { answer: approved, args: [...ARGS, '--max=1ms'] },
    { answer: approved, args: [...ARGS, '--cwd', 'missing'] },
    { answer: approved, args: [...ARGS.slice(0, -1), '--max=-1'] },
  ];
  for (const c of cases) {
    f.record.config = initialConfigState(c.config ?? {});
    await writeFile(path.join(f.runs, `${RUN}.json`), JSON.stringify(f.record));
    responses.splice(0, responses.length, c.answer);
    d.reset();
    const args = c.args ?? ARGS;
    const go = await execute(f, args, { env: c.env });
    const node = await execute(f, args, { node: true });
    assert.deepEqual(go, node, `fallback bytes and exit match: ${JSON.stringify(c)}`);
    const unavailable = await execute(f, args, { env: { ...c.env, CODE_FORGE_NODE: NO_NODE } });
    assert.equal(unavailable.code, 127, 'each fallback requires Node');
    const forwarded = await execute(f, args, { env: { ...c.env, CODE_FORGE_NODE: f.argvNode } });
    assert.equal(forwarded.code, 0, 'handover executes the selected Node executable');
    assert.equal(forwarded.stdout, `${JSON.stringify(args)}\n`, 'every original argv element is forwarded in order without extra output');
    if (c.config || c.env) assert.equal(d.requests.length, 0, 'disabled daemon receives zero requests');
  }
  // No listener is reachable at this different run socket.
  const other = 'r-no-daemon';
  await writeFile(path.join(f.runs, `${other}.json`), JSON.stringify({ ...f.record, run_id: other }));
  const args = ['review-file', '--wait-block', BLOCK, '--run', other, '--max=0ms'];
  assert.deepEqual(await execute(f, args), await execute(f, args, { node: true }), 'unreachable daemon has exact Node parity');
  assert.equal((await execute(f, args, { env: { CODE_FORGE_NODE: NO_NODE } })).code, 127, 'unreachable daemon requires Node');
  assert.equal((await execute(f, args, { env: { CODE_FORGE_NODE: f.argvNode } })).stdout, `${JSON.stringify(args)}\n`, 'unreachable daemon forwards the complete original argv');
});
