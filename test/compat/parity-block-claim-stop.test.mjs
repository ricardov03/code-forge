import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { cp, lstat, mkdir, mkdtemp, readFile, readdir, readlink, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';
import { configHash } from '../../src/state/config-snapshot.mjs';
import { verifyRow } from '../../src/state/signer.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-claim-stop-'));
const pkg = path.join(scratch, 'package');
const BINARY = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const NO_NODE = '/nonexistent';
const RUN = 'r-claim-stop';
const KEY = Buffer.alloc(32, 7);
function childEnv(env = process.env) {
  const clean = { ...env };
  for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK']) delete clean[key];
  return clean;
}
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(pkg, 'bin'));
  await symlink(CLI, path.join(pkg, 'bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  linkGoBinary(BINARY, { trimpath: true, ldflags: `-s -w -X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}` });
});

/** @param {(record: any) => void} [change] */
async function fixture(change = () => {}) {
  const root = await mkdtemp(path.join(scratch, 'fixture-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const value = 1;\n');
  for (const args of [['init', '-q', '-b', 'main'], ['add', '.'], ['-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixture']]) {
    const git = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8', env: childEnv() });
    assert.equal(git.status, 0, `git fixture ${args[0]}: ${git.stderr}`);
  }
  const record = { run_id: RUN, project: 'claim-stop', workspace: f.repo, status: 'active', blocks: {
    B1: { status: 'open', owned_files: ['sample.mjs'], owned_kinds: { 'sample.mjs': 'file' } },
    B2: { status: 'open', owned_files: ['other'], owned_kinds: { other: 'absent' } },
  }, orphans: ['new/a.mjs', 'unclaimed.mjs'] };
  change(record);
  const dir = path.join(f.home, '.code-forge', 'runs');
  await mkdir(dir, { recursive: true });
  await writeFile(path.join(dir, `${RUN}.json`), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  await writeFile(path.join(dir, `${RUN}.key`), KEY, { mode: 0o600 });
  return f;
}
function snapshot(record, config) {
  const hash = configHash(config);
  record.config = { hash, snapshots: { [hash]: config }, pins: {} };
}
function launch(f, args, { node = false, env = {} } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(node ? process.execPath : BINARY, node ? [CLI, ...args] : args, { cwd: f.repo, env: childEnv({
      PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE,
      CODE_FORGE_KEY_BACKEND: 'file', CODE_FORGE_NO_ERROR_LOG: '1', ...env,
    }), stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', (b) => { stdout += b; }); child.stderr.on('data', (b) => { stderr += b; });
    child.on('error', reject); child.on('close', (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}
// Preserve absence, directory entries (including empty lock directories), symlink
// targets and raw file bytes. Both state trees include the ledger, queue and locks.
async function handoverState(file) {
  let stat;
  try { stat = await lstat(file); }
  catch (err) { if (err.code === 'ENOENT') return null; throw err; }
  if (stat.isSymbolicLink()) return { kind: 'symlink', target: await readlink(file) };
  if (stat.isDirectory()) {
    const entries = await readdir(file);
    return { kind: 'directory', entries: await Promise.all(entries.sort().map(async (name) =>
      [name, await handoverState(path.join(file, name))])) };
  }
  assert.ok(stat.isFile(), `unexpected state entry: ${file}`);
  return { kind: 'file', bytes: await readFile(file) };
}
async function assertHandoverWithoutMutation(f, args, message, env = {}) {
  const record = path.join(f.home, '.code-forge/runs', `${RUN}.json`);
  const trees = [f.home, f.repo].map((root) => path.join(root, '.code-forge'));
  const [recordBefore, ...stateBefore] = await Promise.all([
    readFile(record), ...trees.map(handoverState),
  ]);
  const result = await launch(f, args, { env });
  assert.equal(result.code, 127, message);
  assert.deepEqual(await readFile(record), recordBefore, `${message}: run record bytes unchanged`);
  await assert.rejects(readFile(path.join(f.home, '.code-forge/ledger/claim-stop.jsonl')),
    { code: 'ENOENT' }, `${message}: no ledger row before handover`);
  assert.deepEqual(await Promise.all(trees.map(handoverState)), stateBefore,
    `${message}: ledger, queue, lock and mirror state unchanged`);
}
const claim = (file = 'new') => ['block', 'claim', 'B1', file, '--run', RUN];
const stop = ['block', 'stop', 'B1', '--run', RUN, '--reason', 'finished <&> café\nnext'];
async function parity(f, args, env = {}) {
  const result = await runParity(args, { fixture: f, goCommand: BINARY, env: childEnv({ CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, ...env }) });
  assert.deepEqual(result.diffs, [], `${args.join(' ')}: zero stream, exit, signature or state diffs`);
  assert.equal(result.equal, true, 'parity reports equality');
}

const liveHook = { argv: [process.execPath, '-e', "require('node:fs').writeFileSync('.code-forge/live-hook-ran', 'ran')"] };
for (const { key, config } of [
  { key: 'hooks.on_block_end', config: { hooks: { on_block_end: liveHook } } },
  { key: 'review.external', config: { review: { external: { required: true, name: 'owner' } } } },
  { key: 'proof.runners', config: { proof: { runners: [{ match: ['test/**'], adapter: 'junit', argv: ['runner', '{test_file}', '{report}'] }] } } },
]) {
  test(`no-snapshot claim and stop ignore live ${key} with Node parity`, async () => {
    for (const hashless of [false, true]) {
      const make = async () => {
        const f = await fixture((r) => { if (hashless) r.config = { snapshots: {}, pins: {} }; });
        // JSON is valid YAML; the live settings deliberately have no run snapshot.
        await writeFile(path.join(f.repo, '.code-forge.yml'), JSON.stringify({ version: 1, ...config }) + '\n');
        await mkdir(path.join(f.repo, '.code-forge'));
        return f;
      };
      const f = await make();
      for (const args of [stop, claim('.code-forge.yml')]) {
        await parity(f, args);
        await parity(f, [...args, '--json']);
      }
      for (const node of [true, false]) {
        const direct = await make();
        const claimed = await launch(direct, [...claim('.code-forge.yml'), '--json'], { node });
        assert.deepEqual(claimed, { code: 0, signal: null, stdout: '{"block":"B1","event":"claimed","path":".code-forge.yml"}\n', stderr: '' }, 'live config does not guard claim; native needs no Node');
        const stopped = await launch(direct, [...stop, '--json'], { node });
        assert.deepEqual(stopped, { code: 0, signal: null, stdout: '{"block":"B1","event":"stopped"}\n', stderr: '' }, 'live config does not add a stop hook or done_file output');
        const rows = (await readFile(path.join(direct.home, '.code-forge/ledger/claim-stop.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
        assert.deepEqual(rows.map((r) => r.event), ['block.claim', 'block.stop'], 'no hook ledger rows');
        assert.deepEqual(rows.map((r) => verifyRow(r, KEY).ok), [true, true], 'both rows are signed');
        const done = JSON.parse(await readFile(path.join(direct.home, '.code-forge/runs', RUN, 'blocks/B1.done.json'), 'utf8'));
        assert.equal(done.status, 'stopped', 'stop still publishes the done file');
        await assert.rejects(readFile(path.join(direct.repo, '.code-forge/live-hook-ran')), { code: 'ENOENT' }, 'live hook is never executed');
      }
    }
  });
}

test('the live-hook test command executes when present in the run snapshot', async () => {
  const f = await fixture((r) => snapshot(r, { hooks: { on_block_end: liveHook } }));
  await mkdir(path.join(f.repo, '.code-forge'));
  const stopped = await launch(f, stop, { node: true });
  assert.equal(stopped.code, 0, stopped.stderr);
  assert.equal(await readFile(path.join(f.repo, '.code-forge/live-hook-ran'), 'utf8'), 'ran', 'positive control: the hook is executable');
});

test('claim and stop run natively with byte parity, signed concurrent writes and unchanged fallback', async () => {
  const speed = await launch(await fixture(), claim());
  assert.equal(speed.code, 0, 'claim succeeds with Node=/nonexistent');
  assert.deepEqual([speed.stdout, speed.stderr], ['block B1 claimed new\n', ''], 'claim emits exactly one success line and no expiry text');
  const f = await fixture();
  for (const args of [claim(), claim('sample.mjs'), [...claim(), '--json'], claim('other/a'), claim('*.mjs'), claim('[id]')]) await parity(f, args);
  await parity(f, claim(), { CODE_FORGE_NO_ERROR_LOG: '0' });
  await parity(f, stop, { CODE_FORGE_NO_ERROR_LOG: '0' });
  assert.equal((await launch(await fixture(), claim(), { env: { CODE_FORGE_NO_ERROR_LOG: '0' } })).code, 0, 'default breadcrumb logging also succeeds without Node');
  for (const [entry, file] of [['src/**', 'src/child'], ['src/*.mjs', 'src/child'], ['src/{a,b}', 'src/a/child'], ['src/{a,b}', 'src/a'], ['src/child/*.mjs', 'src']]) {
    await parity(await fixture((r) => { r.blocks.B2.owned_files = [entry]; r.blocks.B2.owned_kinds = {}; }), claim(file));
  }
  await parity(await fixture((r) => {
    r.blocks.B1.worktree = { path: '/first' }; r.blocks.B2.worktree = { path: '/second' };
  }), claim('other/a'));
  for (const status of ['open', 'awaiting_human_check']) {
    const end = await fixture((r) => { r.blocks.B1.status = status; });
    await parity(end, stop); await parity(end, [...stop, '--json']);
    const ended = await launch(end, stop);
    assert.deepEqual([ended.code, ended.stdout, ended.stderr], [0, 'block B1 stopped\n', ''], `${status}: native stop succeeds without expiry output`);
    const bytes = await readFile(path.join(end.home, '.code-forge/runs', RUN, 'blocks/B1.done.json'), 'utf8');
    const commit = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: end.repo, encoding: 'utf8', env: childEnv() }).stdout.trim();
    assert.equal(bytes.replace(/"at":"[^"]+"/, '"at":"<time>"'), JSON.stringify({ block: 'B1', run_id: RUN, status: 'stopped', event: 'block.stop', reason: stop.at(-1), commit, spent_usd: 0, unknown_usd_sessions: 0, findings: 0, at: '<time>' }) + '\n', 'done file has exact ordered bytes and trailing newline');
  }
  for (const cfg of [{ review: { external: { required: true, name: 'owner' } } }, { proof: { runners: [] } }]) {
    await parity(await fixture((r) => snapshot(r, cfg)), claim('.code-forge.yml'));
  }
  for (const obstruction of ['blocks', 'B1.done.json']) {
    const blocked = await fixture();
    const dir = path.join(blocked.home, '.code-forge/runs', RUN);
    await mkdir(dir);
    if (obstruction === 'blocks') await writeFile(path.join(dir, 'blocks'), 'obstruction');
    else await mkdir(path.join(dir, 'blocks', obstruction), { recursive: true });
    await parity(blocked, stop);
  }
  for (const mutate of [
    (r) => { r.autopilot = { status: 'active', until: '2099-01-01T00:00:00.000Z' }; },
    (r) => { r.autopilot_approvals = [{ status: 'active', until: '2000-01-01T00:00:00.000Z', approval_id: 'apr-test', key: 'budget.usd' }]; },
  ]) {
    const delegated = await fixture(mutate);
    for (const args of [claim(), stop]) {
      await assertHandoverWithoutMutation(delegated, args, 'grant or pending approval delegates without mutation');
      await parity(delegated, args, { CODE_FORGE_NODE: process.execPath });
    }
  }
  const hooked = await fixture((r) => snapshot(r, { hooks: { on_block_end: { argv: ['/usr/bin/true'] } } }));
  for (const args of [claim(), stop]) {
    await parity(hooked, args);
    const native = await launch(hooked, args);
    assert.equal(native.code, 0, `snapshot hook stays native: ${native.stderr}`);
  }
  // Use Go's observed serialization order as the Node oracle order. Both distinct
  // changes must survive, and the record and signed ledger must match those Node writes.
  const concurrent = await fixture();
  const copy = { repo: concurrent.repo, home: path.join(scratch, 'concurrent-home') };
  await cp(concurrent.home, copy.home, { recursive: true });
  const lock = path.join(copy.home, '.code-forge/runs', `${RUN}.lock`);
  await mkdir(lock);
  const writers = [launch(copy, claim('first.mjs')), launch(copy, claim('second.mjs'))];
  try {
    await delay(100);
    await assert.rejects(readFile(path.join(copy.home, '.code-forge/ledger/claim-stop.jsonl')), { code: 'ENOENT' }, 'neither writer signs while another process holds the run lock');
  } finally { await rm(lock, { recursive: true, force: true }); }
  const go = await Promise.all(writers);
  const goRows = (await readFile(path.join(copy.home, '.code-forge/ledger/claim-stop.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
  assert.equal(goRows.length, 2, 'two concurrent distinct claims each publish one row');
  const node = [];
  for (const row of goRows) node.push(await launch(concurrent, claim(row.path), { node: true }));
  assert.deepEqual(go, [...node].sort((a, b) => a.stdout.includes('first.mjs') ? -1 : b.stdout.includes('first.mjs') ? 1 : 0), 'both concurrent processes match their Node exit and stream bytes');
  const records = await Promise.all([concurrent, copy].map((c) => readFile(path.join(c.home, '.code-forge/runs', `${RUN}.json`), 'utf8')));
  assert.equal(records[1], records[0], 'concurrent record bytes match Node');
  assert.deepEqual(JSON.parse(records[1]).blocks.B1.owned_files.slice().sort(), ['first.mjs', 'sample.mjs', 'second.mjs'], 'both distinct claims survive the run lock');
  const rows = await Promise.all([concurrent, copy].map(async (c) => (await readFile(path.join(c.home, '.code-forge/ledger/claim-stop.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line))));
  for (const side of rows) {
    assert.equal(side.length, 2, 'two concurrent claims publish exactly two rows');
    assert.deepEqual(side.map((r) => verifyRow(r, KEY).ok), [true, true], 'both concurrent rows have valid signatures');
  }
  assert.deepEqual(rows[1].map(({ ts, mac, ...r }) => r), rows[0].map(({ ts, mac, ...r }) => r), 'all nonvolatile concurrent ledger fields match Node');
});

test('claim --auto and --auto-claim delegate without mutation and match Node', async () => {
  const f = await fixture();
  // Assert specific fallback flags; unrelated subcommands can become native.
  for (const flag of ['--auto', '--auto-claim']) {
    const args = [...claim(), flag];
    await assertHandoverWithoutMutation(f, args, `claim ${flag} delegates`);
    await parity(f, args, { CODE_FORGE_NODE: process.execPath });
  }
});

test('claim and stop refusals and usage boundaries match Node without starting Node', async () => {
  const first = await launch(await fixture(), ['block', 'claim']);
  assert.equal(first.code, 2, 'missing arguments are a native usage error');
  const f = await fixture();
  for (const args of [
    ['block', 'claim'], ['block', 'stop'], ['block', 'claim', 'B1', '--run', RUN], [...claim(), 'extra'],
    ['block', 'stop', 'B1', '--run', RUN], [...claim(), '--run', RUN], [...claim(), '--json=true'],
    [...claim(), '--run='], [...claim(), '--remove-worktree'], [...claim(), '--unknown'],
    [...claim(), '--'], [...claim(), '--json', '--json'], [...claim(), '--owned'], [...claim(), '--tests='],
    [...claim(), '--owned', 'one', '--owned=two', '--json'], [...claim(), '--reason', '--run'],
    [...claim(), '--run level', 'ignored'], ['block', 'stop', 'B1', 'ignored', '--run', RUN, '--reason', ''],
    ['block', 'claim', 'B1', 'x', '--run', 'BAD'], ['block', 'stop', 'B1', '--run', 'missing', '--reason', 'x'],
    ...['../x', '/absolute', 'a//b', 'a\\b', '*[x]', '{a,b}', '{broken'].map((p) => claim(p)),
  ]) await parity(f, args);
  for (const status of ['closed', 'stopped', 'awaiting_human_check', 'unknown']) {
    const bad = await fixture((r) => { if (status === 'unknown') delete r.blocks.B1; else r.blocks.B1.status = status; });
    await parity(bad, claim());
    if (status !== 'awaiting_human_check') await parity(bad, stop);
  }
  await parity(await fixture((r) => { r.status = 'ended'; }), claim());
  await parity(await fixture(), [...stop, '--reason=']);
  for (const sub of ['claim', 'stop']) for (const help of ['--help', '-h']) await parity(f, ['block', sub, '--invalid', help]);
  // Default error logging must preserve Node's scrubbed error records via fallback.
  await assertHandoverWithoutMutation(f, ['block', 'claim'], 'default error logging delegates without mutation', { CODE_FORGE_NO_ERROR_LOG: '0' });
  await parity(f, ['block', 'claim'], { CODE_FORGE_NODE: process.execPath, CODE_FORGE_NO_ERROR_LOG: '0' });
  const blocked = await fixture();
  await mkdir(path.join(blocked.repo, '.code-forge'));
  await writeFile(path.join(blocked.repo, '.code-forge/runs'), 'mirror obstruction');
  await assertHandoverWithoutMutation(blocked, claim(), 'an unsupported mirror layout delegates before signing');
  await parity(blocked, claim(), { CODE_FORGE_NODE: process.execPath });
});
