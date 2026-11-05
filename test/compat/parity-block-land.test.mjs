import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { chmod, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { serveDaemon } from '../helpers/daemon-fixture.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { announceWorker, writeResult } from '../../src/worker/queue.mjs';
import { contentHash } from '../../src/worker/ticket.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-land-'));
const BINARY = path.join(scratch, 'package/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const RUN = 'r-land-fixture';
const args = ['block', 'land', 'B1', '--run', RUN, '--report', 'report.txt', '--transcript', 'transcript.txt'];
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'package/bin'));
  await symlink(CLI, path.join(scratch, 'package/bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const build = spawnSync('go', ['build', '-ldflags', `-X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}`, '-o', BINARY, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, GOPROXY: 'off', GOTOOLCHAIN: 'local' },
  });
  assert.equal(build.status, 0, build.stderr);
});

async function fixture(status = 'closed', config = {}) {
  const root = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  const runs = path.join(f.home, '.code-forge/runs');
  await mkdir(runs, { recursive: true });
  const record = { run_id: RUN, project: 'land-fixture', workspace: f.repo, status: 'active',
    config: initialConfigState(config), blocks: status === 'unknown' ? {} : { B1: { status, owned_files: ['sample.mjs'] } }, orphans: [] };
  await writeFile(path.join(runs, `${RUN}.json`), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  await writeFile(path.join(runs, `${RUN}.key`), Buffer.alloc(32, 27), { mode: 0o600 });
  return f;
}
function launch(f, argv, env = {}) {
  return spawnSync(BINARY, argv, { cwd: f.repo, encoding: 'utf8', env: {
    PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent',
    CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', ...env,
  } });
}

test('early land refusals run in Go and preserve Node streams, exit codes and signed timing rows', async () => {
  for (const status of ['closed', 'unknown']) {
    for (const json of [false, true]) {
      const f = await fixture(status), argv = [...args, ...(json ? ['--json'] : [])];
      const result = launch(f, argv);
      assert.equal(result.status, 1, `${status}: native land refuses with Node unavailable`);
      assert.match(result.stderr, /^block B1 land stopped at join: /);
      const parity = await runParity(argv, { fixture: await fixture(status), goCommand: BINARY,
        env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
      assert.deepEqual(parity.diffs, [], `${status}, json=${json}: zero process and state differences`);
      const rows = (await readFile(path.join(f.home, '.code-forge/ledger/land-fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
      assert.deepEqual(rows.map((r) => r.event), ['land.timing'], 'one timing row and no mutation rows');
    }
  }
});

test('land usage and bad max boundaries match Node without starting Node', async () => {
  for (const argv of [
    ['block', 'land'], ['block', 'land', 'B1'], args.slice(0, -2), [...args, 'extra'],
    [...args, '--max', 'wrong'], [...args, '--max', '9999999m'], [...args, '--json=true'],
    [...args, '--run', RUN], [...args, '--unknown'],
    ['block', 'land', 'B1', '--run', RUN, '--report', '--merge', 'report.txt', '--transcript', 'transcript.txt'],
    [...args, '--merge', '--merge'], [...args, '--no-merge-gate=false'],
  ]) {
    const f = await fixture(), direct = launch(f, argv);
    assert.equal(direct.status, 2, `${argv}: native usage exits 2`);
    const parity = await runParity(argv, { fixture: f, goCommand: BINARY,
      env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(parity.diffs, [], `${argv}: zero usage differences`);
  }
  const f = await fixture('open', { hooks: { on_block_end: { argv: ['/usr/bin/true'] } } });
  const runs = path.join(f.home, '.code-forge/runs');
  const before = await Promise.all((await readdir(runs)).sort().map(async (name) => [name, await readFile(path.join(runs, name))]));
  const unavailable = launch(f, [...args, '--json']);
  assert.equal(unavailable.status, 127, 'unsupported close hook hands over the entire land before Go writes');
  const probe = path.join(scratch, 'argv-node');
  await writeFile(probe, `#!${process.execPath}\nprocess.stdout.write(JSON.stringify(process.argv.slice(3)) + '\\n');\n`);
  await chmod(probe, 0o755);
  const forwarded = launch(f, [...args, '--json'], { CODE_FORGE_NODE: probe });
  assert.deepEqual([forwarded.status, forwarded.stdout, forwarded.stderr], [0, JSON.stringify([...args, '--json']) + '\n', ''], 'whole-land handover forwards every original argument exactly once');
  assert.deepEqual(await Promise.all((await readdir(runs)).sort().map(async (name) => [name, await readFile(path.join(runs, name))])), before, 'handover preserves all run bytes');
  assert.deepEqual((await readdir(path.join(f.home, '.code-forge'))).sort(), ['runs'], 'zero Go ledger rows or other state directories on handover');
});

const KEY = Buffer.alloc(32, 27);
function git(repo, argv) {
  const result = spawnSync('git', argv, { cwd: repo, encoding: 'utf8', env: { ...process.env, GIT_AUTHOR_DATE: '2020-01-01T00:00:00Z', GIT_COMMITTER_DATE: '2020-01-01T00:00:00Z' } });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}
function native(f, argv = args, env = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(BINARY, argv, { cwd: f.repo, env: { PATH: process.env.PATH, HOME: f.home,
      CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', ...env } });
    let stdout = '', stderr = '';
    child.stdout.on('data', (b) => { stdout += b; }); child.stderr.on('data', (b) => { stderr += b; });
    child.on('error', reject); child.on('close', (code) => resolve({ code, stdout, stderr }));
  });
}
async function openFixture(kind = 'clean', config = {}) {
  const f = await fixture('open');
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const value = 1;\n');
  const gate = ['/bin/sh', '-c', kind === 'red-slow' ? 'sleep 1.1; echo failed; exit 1' : kind === 'red' ? 'echo failed; exit 1' : 'true'];
  const live = { version: 1, project: { slug: 'land-fixture' }, provider: 'anthropic', engine: 'harness',
    levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } },
    gates: { test: null, lint: gate, types: null, format: null }, ...config };
  await writeFile(path.join(f.repo, '.code-forge.yml'), JSON.stringify(live) + '\n');
  git(f.repo, ['init', '-qb', 'main']);
  git(f.repo, ['config', 'user.name', 'Fixture']); git(f.repo, ['config', 'user.email', 'f@example.invalid']);
  git(f.repo, ['config', 'commit.gpgsign', 'false']);
  git(f.repo, ['add', '.']); git(f.repo, ['commit', '-qm', 'base']);
  const base = git(f.repo, ['rev-parse', 'HEAD']);
  const tree = path.join(f.repo, '.code-forge/worktrees/B1');
  git(f.repo, ['worktree', 'add', '-qb', 'block-B1', tree, base]);
  await writeFile(path.join(tree, 'sample.mjs'), 'export const value = 2;\n');
  if (kind === 'auto') await writeFile(path.join(tree, 'outsider.mjs'), 'export const claimed = true;\n');
  git(tree, ['add', '.']); git(tree, ['commit', '-qm', 'block changes']);
  const entry = { status: 'open', owned_files: ['sample.mjs'], base_sha: base, base_branch: 'main',
    tree_at_open: {}, kind: 'code', test_files: [], worktree: { path: tree, branch: 'block-B1', base_sha: base } };
  const record = { run_id: RUN, project: 'land-fixture', workspace: f.repo, status: 'active', worker: { pid: process.pid, started_at: await processStartTime(process.pid) },
    config: initialConfigState(live), blocks: { B1: entry }, orphans: [] };
  for (const root of [f.home, f.repo]) {
    const dir = path.join(root, '.code-forge/runs'); await mkdir(dir, { recursive: true });
    await writeFile(path.join(dir, RUN + '.json'), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  }
  await writeFile(path.join(f.repo, 'report.txt'), 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n');
  await writeFile(path.join(f.repo, 'transcript.txt'), 'Completed implementation.\n');
  // Land inputs are outside the block tree and ignored in the main checkout.
  await writeFile(path.join(f.repo, '.git/info/exclude'), 'report.txt\ntranscript.txt\n');
  const saved = process.env.HOME;
  try {
    process.env.HOME = f.home;
    await mkdir(path.join(f.repo, '.code-forge/queue'), { recursive: true });
    const ticket = 'a'.repeat(24);
    const findings = kind === 'findings' ? [{ id: 'F1', severity: 'warning', file: 'sample.mjs', line_start: 1, line_end: 1, claim: 'fix this', fix: 'change value' }] : [];
    writeResult({ repoRoot: f.repo, runId: RUN, ticket, key: KEY, result: { status: 'reviewed', approved: !findings.length, findings } });
    await appendRow(signRow({ run: RUN, event: 'dispatch', block: 'B1', level: 'L2' }, KEY), { slug: 'land-fixture' });
    await appendRow(signRow({ run: RUN, event: findings.length ? 'review.result' : 'review.approved', block: 'B1', file: 'sample.mjs',
      content_hash: contentHash(tree, 'sample.mjs'), ...(findings.length ? { ticket, approved: false, status: 'reviewed', open: findings } : {}) }, KEY), { slug: 'land-fixture' });
    if (kind === 'auto') await appendRow(signRow({ run: RUN, event: 'review.approved', block: 'B1', file: 'outsider.mjs', content_hash: contentHash(tree, 'outsider.mjs') }, KEY), { slug: 'land-fixture' });
    announceWorker(f.repo, { pid: process.pid, run: RUN });
    return { ...f, tree, ticket, kind };
  } finally { process.env.HOME = saved; }
}
async function prepareLand(copy, kind = 'clean') {
  const record = JSON.parse(await readFile(path.join(copy.home, '.code-forge/runs', RUN + '.json'), 'utf8'));
  const tree = record.blocks.B1.worktree.path;
  let joins = 0;
  const daemon = await serveDaemon(copy, RUN, async (req) => {
    if (req.method === 'rows') return { rows: (await readFile(path.join(copy.home, '.code-forge/ledger/land-fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line)) };
    assert.equal(req.method, 'wait-block');
    joins++;
    if (kind === 'moved' && joins === 1) git(tree, ['commit', '-q', '--allow-empty', '-m', 'during join']);
    if (kind === 'orphan') {
      // Wait until the parallel gate has published, then simulate a concurrent
      // writer updating the registry before close takes its fresh run lock.
      const deadline = Date.now() + 5000;
      while (!(await readFile(path.join(copy.home, '.code-forge/ledger/land-fixture.jsonl'), 'utf8')).includes('"event":"gate.done"')) {
        assert.ok(Date.now() < deadline, 'gates must run during join');
        await new Promise((resolve) => setTimeout(resolve, 10));
      }
      const file = path.join(copy.home, '.code-forge/runs', RUN + '.json');
      const fresh = JSON.parse(await readFile(file, 'utf8'));
      fresh.orphans = ['late.mjs'];
      await writeFile(file, JSON.stringify(fresh, null, 2) + '\n', { mode: 0o600 });
    }
    // Give the gate time to finish in findings cases to avoid a cancellation race.
    await new Promise((resolve) => setTimeout(resolve, kind === 'timeout' ? req.max_ms : 200));
    if (kind === 'timeout') return { block: 'B1', files: [], done: false, reason: 'wait_timeout' };
    const queue = path.join(copy.repo, '.code-forge/queue');
    const marker = (await readdir(queue)).find((name) => name.endsWith('.done'));
    return { block: 'B1', files: [{ file: 'sample.mjs', ticket: marker.slice(0, -5), status: kind === 'findings' ? 'findings' : 'reviewed', outcome: kind === 'findings' ? 'findings' : 'approved' }, ...(kind === 'auto' ? [{ file: 'outsider.mjs', status: 'reviewed', outcome: 'approved' }] : [])], done: true };
  });
  return async () => {
    await daemon.cleanup();
    const rows = (await readFile(path.join(copy.home, '.code-forge/ledger/land-fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(rows.filter((r) => r.event === 'land.timing').length, 1, 'exactly one signed timing row');
    assert.equal(rows.filter((r) => r.event === 'gate.done').length, kind === 'moved' ? 2 : 1, 'tree moves trigger exactly one gate rerun');
    assert.equal(rows.filter((r) => r.event === 'block.close').length, ['clean', 'moved', 'auto'].includes(kind) ? 1 : 0);
    assert.equal(rows.filter((r) => r.event === 'block.claim').length, kind === 'auto' ? 1 : 0);
    if (kind === 'findings') assert.equal(joins, 3, 'findings join retries are capped at three');
  };
}
for (const kind of ['clean', 'findings', 'red', 'moved', 'timeout', 'auto', 'orphan']) {
  test(`full native land: ${kind} has exact Node parity`, async () => {
    for (const json of [false, true]) {
      const f = await openFixture(kind), argv = [...args, '--max', kind === 'timeout' ? '2s' : '10s', ...(json ? ['--json'] : [])];
      const result = await runParity(argv, { fixture: f, goCommand: BINARY, tempRoot: '/tmp', samePaths: true,
        env: { CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent' }, prepare: (copy) => prepareLand(copy, kind) });
      assert.deepEqual(result.diffs, [], `${kind} json=${json}: exact process and state parity`);
    }
  });
}
test('ported land and optional merge succeed with Node unavailable', async () => {
  for (const extra of [[], ['--json'], ['--merge', '--no-merge-gate'], ['--merge', '--no-merge-gate', '--json']]) {
    const f = await openFixture();
    // HOME must be short enough for a Unix socket on macOS.
    const short = await mkdtemp('/tmp/cf-ls-');
    const { cp } = await import('node:fs/promises');
    await cp(f.home, short, { recursive: true });
    const direct = { ...f, home: short };
    const cleanup = await prepareLand(direct);
    try {
      const result = await native(direct, [...args, ...extra]);
      assert.equal(result.code, 0, result.stderr);
      assert.match(result.stdout, /closed|landed/);
      const record = JSON.parse(await readFile(path.join(short, '.code-forge/runs', RUN + '.json'), 'utf8'));
      assert.equal(record.blocks.B1.status, 'closed');
      const rows = (await readFile(path.join(short, '.code-forge/ledger/land-fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
      assert.equal(rows.filter((r) => r.event === 'block.close').length, 1);
      assert.equal(rows.filter((r) => r.event === 'land.timing').length, 1);
      assert.equal(rows.every((r) => !r.mac || verifyRow(r, KEY).ok), true);
    } finally { await cleanup(); await rm(short, { recursive: true, force: true }); }
    const result = await runParity([...args, ...extra], { fixture: await openFixture(), goCommand: BINARY, tempRoot: '/tmp', samePaths: true,
      env: { CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent' }, prepare: (copy) => prepareLand(copy) });
    assert.deepEqual(result.diffs, [], `merge flow ${extra}: exact Node parity`);
  }
});
test('no daemon, close hook, and unscoped merge gate hand over before any Go writes', async () => {
  const cases = /** @type {Array<[Record<string, any>, string[]]>} */ ([[{}, []], [{ hooks: { on_block_end: { argv: ['/usr/bin/true'] } } }, []], [{}, ['--merge']]]);
  for (const [config, extra] of cases) {
    const f = await openFixture('clean', config);
    const home = await mkdtemp('/tmp/cf-lh-');
    const { cp } = await import('node:fs/promises');
    await cp(f.home, home, { recursive: true });
    const direct = { ...f, home };
    const daemon = (extra.length || Object.keys(config).length) ? await serveDaemon(direct, RUN, async () => { throw new Error('no step may start'); }) : null;
    try {
      const before = await snapshot(direct);
      const result = await native(direct, [...args, ...extra]);
      assert.equal(result.code, 127, result.stderr);
      assert.deepEqual(await snapshot(direct), before, 'every state byte and directory unchanged');
    } finally { await daemon?.cleanup(); await rm(home, { recursive: true, force: true }); }
  }
});
async function snapshot(f) {
  const out = [];
  async function walk(dir) {
    for (const e of (await readdir(dir, { withFileTypes: true })).sort((a,b) => a.name.localeCompare(b.name))) {
      const file = path.join(dir, e.name);
      out.push([file, e.isDirectory() ? 'directory' : e.isSocket() ? 'socket' : await readFile(file)]);
      if (e.isDirectory()) await walk(file);
    }
  }
  for (const root of [f.home, path.join(f.repo, '.code-forge')]) await walk(root);
  return out;
}

test('composed land logs only the outer block invocation and matches Node failure logs', async () => {
  for (const kind of ['clean', 'findings', 'red', 'red-slow', 'orphan']) {
    const result = await runParity([...args, '--max', '10s', '--json'], { fixture: await openFixture(kind), goCommand: BINARY, tempRoot: '/tmp', samePaths: true,
      env: { CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '0' }, prepare: (copy) => prepareLand(copy, kind) });
    assert.deepEqual(result.diffs, [], `${kind}: exact outer breadcrumb and error log, no child CLI logs`);
  }
});

test('native land usage keeps normal router logging with Node unavailable', async () => {
  for (const argv of [['block', 'land'], [...args, '--max', 'bad']]) {
    const f = await fixture();
    const direct = launch(f, argv, { CODE_FORGE_NO_ERROR_LOG: '0' });
    assert.equal(direct.status, 2, direct.stderr);
    const result = await runParity(argv, { fixture: await fixture(), goCommand: BINARY,
      env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '0' } });
    assert.deepEqual(result.diffs, [], 'usage logs match Node with no Node executable');
  }
});

test('early land refusals also preserve normal router logs without Node', async () => {
  for (const status of ['closed', 'unknown']) {
    const result = await runParity([...args, '--json'], { fixture: await fixture(status), goCommand: BINARY,
      env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '0' } });
    assert.deepEqual(result.diffs, [], `${status}: refusal, timing and router logs`);
  }
});
