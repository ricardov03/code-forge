import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, stat, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { runParity } from '../helpers/parity.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const TMP = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-enqueue-flags-')));
const BIN = path.join(TMP, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const RUN = 'r-enqueue-flags', NO_NODE = '/nonexistent';
const ARGS = ['review-file', 'sample.mjs', '--block', 'B1', '--run', RUN];
after(() => rm(TMP, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BIN), { recursive: true });
  await mkdir(path.join(TMP, 'bin'));
  await symlink(CLI, path.join(TMP, 'bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const built = spawnSync('go', ['build', '-trimpath', '-ldflags', `-X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}`, '-o', BIN, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' },
  });
  assert.equal(built.status, 0, `native binary builds: ${built.stderr}`);
});
function git(repo, args) {
  const r = spawnSync('git', ['-C', repo, ...args], { encoding: 'utf8' });
  assert.equal(r.status, 0, `fixture git ${args[0]}: ${r.stderr}`);
  return r.stdout.trim();
}
async function put(root, name, value) {
  const file = path.join(root, '.code-forge', name);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, typeof value === 'string' ? value : JSON.stringify(value) + '\n');
}
async function fixture() {
  const base = await mkdtemp(path.join(TMP, 'f-'));
  const repo = path.join(base, 'repo'), home = path.join(base, 'home');
  await mkdir(repo); await mkdir(home);
  git(repo, ['init', '-q', '-b', 'main']);
  await writeFile(path.join(repo, 'sample.mjs'), 'export const sample = 1;\n');
  await writeFile(path.join(repo, 'answer.mjs'), 'export const answer = 2;\n');
  git(repo, ['add', '.']);
  git(repo, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'base']);
  const record = { run_id: RUN, status: 'active', workspace: repo, project: 'fixture', config: initialConfigState({}), blocks: { B1: { status: 'open', owned_files: ['sample.mjs', 'answer.mjs'] } } };
  const save = async () => { await put(home, `runs/${RUN}.json`, record); await put(repo, `runs/${RUN}.json`, record); };
  await save();
  await writeFile(path.join(home, '.code-forge/runs', `${RUN}.key`), Buffer.alloc(32, 7), { mode: 0o600 });
  const stamp = new Date().toISOString();
  await put(repo, `workers/${process.pid}-unknown.json`, { pid: process.pid, start_time: 'unknown', run: RUN, started_at: stamp, heartbeat_at: stamp });
  return { repo, home, record, save };
}
function execute(f, args = ARGS, node = false, extra = {}) {
  const r = spawnSync(node ? process.execPath : BIN, node ? [CLI, ...args] : args, { cwd: f.repo, encoding: 'utf8', env: {
    PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', CODE_FORGE_HANDOVER_LOG: '0', ...extra,
  } });
  return { code: r.status, stdout: r.stdout, stderr: r.stderr };
}
async function executeAsync(f, args) {
  const child = spawn(BIN, args, { cwd: f.repo, env: { PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_HANDOVER_LOG: '0' }, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', (b) => { stdout += b; }); child.stderr.on('data', (b) => { stderr += b; });
  const [code] = await once(child, 'close');
  return { code, stdout, stderr };
}
async function parity(f, args = ARGS, options = {}) {
  const r = await runParity(args, { fixture: f, goCommand: BIN, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE }, ...options });
  assert.deepEqual(r.diffs, [], `${args.join(' ')}: zero output, state and verified ledger differences`);
}
async function done(f, id, result, marker = {}) {
  await put(f.repo, 'reviews/result.json', result);
  await put(f.repo, `queue/${id}.done`, { ticket: id, status: result.status, result: '.code-forge/reviews/result.json', ...marker });
}
async function snapshot(f) {
  const files = [];
  async function walk(dir, prefix) {
    for (const entry of (await readdir(dir, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = path.join(dir, entry.name), name = `${prefix}/${entry.name}`;
      files.push([name, (await stat(file)).mode & 0o777, entry.isDirectory() ? null : await readFile(file)]);
      if (entry.isDirectory()) await walk(file, name);
    }
  }
  for (const area of ['repo', 'home']) await walk(path.join(f[area], '.code-forge'), area);
  return files;
}

test('unported help flag records exactly one hand-over with its B282 reason', async () => {
  const f = await fixture();
  const args = ['review-file', '--help'];
  const expected = execute(f, args, true);
  assert.deepEqual(execute(f, args, false, { CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: process.execPath, CODE_FORGE_HANDOVER_LOG: '1' }), expected, 'recording preserves Node output and exit');
  const rows = (await readFile(path.join(f.home, '.code-forge/native/handovers.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
  assert.equal(rows.length, 1, 'one hand-over writes exactly one JSONL row');
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  assert.deepEqual({ ...rows[0], at: '<time>' }, { at: '<time>', verb: 'review-file', sub: 'unknown', reason: 'enqueue-help-encoding', version }, 'help uses the existing reason and only safe telemetry fields');
  assert.equal(Number.isNaN(Date.parse(rows[0].at)), false, 'row has a valid timestamp');
});

test('enqueue flag modes and cache retries run in Go with zero Node parity differences', async () => {
  const f = await fixture();
  assert.equal(execute(f, [...ARGS, '--fresh']).code, 0, '--fresh without cache succeeds without Node');
  const id = JSON.parse(execute(f).stdout).ticket;
  await parity(f, [...ARGS, '--fresh']);
  for (const result of [{ status: 'stale' }, { status: 'unavailable' }, { status: 'split_required', round: 1 }, { status: 'refused', reason: 'other-run' }, { status: 'refused', reason: 'worktree-missing' }]) {
    await done(f, id, result);
    await parity(f, [...ARGS, '--fresh']);
    const r = execute(f, [...ARGS, '--fresh']);
    assert.equal(r.code, 0, `${result.status}: native fresh exits 0`);
    assert.equal(JSON.parse(r.stdout).fresh, true, `${result.status}: dropped cache reports fresh`);
  }
  for (const result of [{ status: 'unavailable' }, { status: 'split_required', round: 1 }]) {
    await done(f, id, result);
    await parity(f);
    assert.equal(JSON.parse(execute(f).stdout).retry, true, `${result.status}: automatic retry reports retry`);
  }
  await done(f, id, { status: 'reviewed', approved: true });
  await parity(f);
  assert.equal(JSON.parse(execute(f).stdout).status, 'done', 'counted cache is reused');
  const answerArgs = ['review-file', 'answer.mjs', '--block=B1', `--run=${RUN}`, '--answers=z:2,a.1', '--of=sample.mjs'];
  const ticket = JSON.parse(execute(f).stdout);
  const refusal = signRow({ event: 'review.result', run: RUN, block: 'B1', file: 'sample.mjs', content_hash: ticket.content_hash, status: 'reviewed', approved: false, open: [{ id: 'z:2', severity: 'warning' }, { id: 'a.1', severity: 'critical' }] }, Buffer.alloc(32, 7));
  await put(f.home, 'ledger/fixture.jsonl', JSON.stringify(refusal) + '\n');
  await parity(f, answerArgs);
  const answer = execute(f, answerArgs);
  assert.equal(answer.code, 0, 'answers enqueue succeeds without Node');
  assert.deepEqual(JSON.parse(answer.stdout).answers, ['a.1', 'z:2'], 'two finding ids sorted in identity and output');
  assert.equal(JSON.parse(answer.stdout).kind, 'answers', 'answers kind included');
  assert.equal((await stat(path.join(f.repo, '.code-forge/queue', `${JSON.parse(answer.stdout).ticket}.json`))).mode & 0o777, 0o644, 'answers ticket mode is exactly 0644');
  await parity(f, answerArgs);
  await parity(f, [...ARGS, '--cwd=.']);
  await done(f, JSON.parse(answer.stdout).ticket, { status: 'unavailable' });
  await parity(f, answerArgs);
  assert.equal(JSON.parse(execute(f, answerArgs).stdout).retry, true, 'answers unavailable cache retries');
  const rebaseArgs = ['review-file', '--rebase-check', '--block=B1', `--run=${RUN}`];
  await parity(f, rebaseArgs);
  assert.equal(execute(f, rebaseArgs).code, 2, 'non-worktree rebase check returns native usage');
  for (const args of [
    ['review-file', 'sample.mjs', '--block=B1', '--run=wrong-run', '--fresh'],
    ['review-file', 'sample.mjs', '--block=B1', '--run=wrong-run', '--answers=f1', '--of=answer.mjs'],
    ['review-file', '--rebase-check', '--block=B1', '--run=wrong-run'],
  ]) {
    await parity(f, args);
    assert.equal(execute(f, args).code, 3, 'requested run mismatch reports worker_down without Node');
  }
  await done(f, id, { status: 'unavailable' });
  const concurrent = await Promise.all([executeAsync(f, [...ARGS, '--fresh']), executeAsync(f, [...ARGS, '--fresh'])]);
  assert.deepEqual(concurrent.map((r) => r.code), [0, 0], 'two concurrent rename-claim writers both succeed');
  assert.equal((await readdir(path.join(f.repo, '.code-forge/queue'))).filter((n) => n.endsWith('.fresh')).length, 0, 'concurrent claim writers leave zero claims');
  const rebased = await fixture();
  const base = git(rebased.repo, ['rev-parse', 'HEAD']);
  const tree = path.join(rebased.repo, 'tree');
  git(rebased.repo, ['worktree', 'add', '-qb', 'block-tree', tree]);
  Object.assign(rebased.record.blocks.B1, { base_sha: base, base_branch: 'main', worktree: { path: tree, branch: 'block-tree', base_sha: base } });
  await rebased.save();
  await parity(rebased, rebaseArgs, { samePaths: true });
  const emptyCheck = execute(rebased, rebaseArgs);
  assert.equal(emptyCheck.code, 0, 'new open-worktree checkpoint succeeds without Node');
  assert.deepEqual(JSON.parse(emptyCheck.stdout).files, [], 'unchanged content queues exactly zero files');
  await writeFile(path.join(tree, 'sample.mjs'), 'export const sample = 2;\n');
  git(tree, ['add', '.']);
  git(tree, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'block change']);
  git(rebased.repo, ['merge', '--ff-only', 'block-tree']);
  await writeFile(path.join(rebased.repo, 'sample.mjs'), 'export const sample = 3;\n');
  git(rebased.repo, ['add', 'sample.mjs']);
  git(rebased.repo, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'upstream']);
  Object.assign(rebased.record.blocks.B1, { status: 'closed', base_sha: base, base_branch: 'main', worktree: { path: tree, branch: 'block-tree', base_sha: base } });
  const reviewedHead = git(tree, ['rev-parse', 'HEAD']);
  rebased.record.blocks.B1.rebase_check = signRow({ event: 'block.rebase_check', run: RUN, block: 'B1', old_base: base, new_base: git(rebased.repo, ['rev-parse', 'HEAD']), old_head: reviewedHead, head: reviewedHead, started_at: '2020-01-01T00:00:00.000Z', ts: '2020-01-01T00:00:00.000Z' }, Buffer.alloc(32, 7));
  await rebased.save();
  await parity(rebased, rebaseArgs, { samePaths: true });
  const checked = execute(rebased, rebaseArgs);
  assert.equal(checked.code, 0, `rebase check queues without Node: ${checked.stderr}`);
  assert.equal(JSON.parse(checked.stdout).files.length, 1, 'rebase check queues exactly one changed reviewed file');
  assert.equal(JSON.parse(checked.stdout).pending, true, 'rebase check retains its signed checkpoint');
  const saved = JSON.parse(await readFile(path.join(rebased.home, '.code-forge/runs', `${RUN}.json`), 'utf8')).blocks.B1;
  assert.equal(saved.base_sha, base, 'rebase check leaves the recorded base unchanged');
  assert.equal(verifyRow(saved.rebase_check, Buffer.alloc(32, 7)).ok, true, 'rebase checkpoint has one valid MAC');
  const joined = await Promise.all([executeAsync(rebased, rebaseArgs), executeAsync(rebased, rebaseArgs)]);
  assert.deepEqual(joined.map((r) => r.code), [0, 0], 'two concurrent rebase checks serialize under the run lock');
  assert.equal((await readdir(path.join(rebased.repo, '.code-forge/queue'))).filter((n) => n.endsWith('.json')).length, 1, 'concurrent checkpoint writers reuse exactly one ticket');
  assert.deepEqual(execute(rebased, rebaseArgs), execute(rebased, rebaseArgs, true), 'existing path-derived rebase tickets give byte-equal replies on their original tree');
});

test('enqueue flag refusals preserve cache bytes and unsupported cases hand over before writes', async () => {
  const f = await fixture();
  assert.equal(execute(f, [...ARGS, '--fresh', '--answers=f1', '--of=sample.mjs']).code, 2, 'fresh plus answers is native usage');
  const cases = [
    [...ARGS, '--fresh', '--of=sample.mjs'], [...ARGS, '--answers=f1'], [...ARGS, '--of=sample.mjs'],
    [...ARGS, '--answers=f1,f1', '--of=answer.mjs'], [...ARGS, '--answers=bad/file.json', '--of=answer.mjs'],
    [...ARGS, '--answers=' + Array.from({ length: 11 }, (_, i) => `f${i}`).join(','), '--of=answer.mjs'],
    [...ARGS, '--answers=f1', '--of=sample.mjs'], [...ARGS, '--answers=f1', '--of=unknown-ticket'],
    [...ARGS, '--answers=f1', '--of=answer.mjs'], [...ARGS, '--fresh', '--json'],
    ['review-file', '--rebase-check', '--block=B1', '--run=BAD'],
    ['review-file', '--rebase-check', '--block=B1'], ['review-file', '--rebase-check', '--block=bad/id', `--run=${RUN}`],
    ['review-file', '--rebase-check', '--block=B1', `--run=${RUN}`, '--max=1s'],
    [...ARGS, '--rebase-check'], [...ARGS, '--fresh', '--fresh'], [...ARGS, '--rebase-check=true'],
    [...ARGS, '--answers=f1', '--of=../outside'], [...ARGS, '--answers=f1', '--of=answer.mjs', '--json'],
  ];
  for (const args of cases) await parity(f, args);
  for (const args of [cases[0], [...ARGS, '--answers=f1', '--of=sample.mjs']]) {
    await parity(f, args, { env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '0' } });
  }
  const id = JSON.parse(execute(f).stdout).ticket;
  for (const result of [{ status: 'reviewed', approved: true }, { status: 'refused', reason: 'review-state-tampered' }, { status: 'refused', reason: 'review-state-lost' }]) {
    await done(f, id, result);
    const file = path.join(f.repo, '.code-forge/queue', `${id}.done`), bytes = await readFile(file);
    await parity(f, [...ARGS, '--fresh']);
    assert.equal(execute(f, [...ARGS, '--fresh']).code, 1, `${result.reason ?? result.status}: cached result is refused`);
    assert.deepEqual(await readFile(file), bytes, 'refusal changes zero cached marker bytes');
  }
  const before = await snapshot(f);
  assert.equal(execute(f, ['review-file', '--help']).code, 127, 'help still hands over with Node unavailable');
  assert.deepEqual(await snapshot(f), before, 'handover changes zero queue, ledger, run or log bytes and modes');
  await parity(f, ['review-file', '--help'], { env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: process.execPath } });
  await put(f.home, 'ledger/fixture.jsonl', JSON.stringify({ event: 'review.cap', run: RUN, block: 'B1', file: 'sample.mjs', open: ['f1'] }) + '\n');
  const capArgs = ['review-file', 'answer.mjs', '--block=B1', `--run=${RUN}`, '--answers=f1', '--of=sample.mjs'];
  const capBefore = await snapshot(f);
  assert.equal(execute(f, capArgs).code, 127, 'unsupported cap clearing rules hand over');
  assert.deepEqual(await snapshot(f), capBefore, 'unsupported answers case changes zero state bytes or modes');
  await parity(f, capArgs, { env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: process.execPath } });
});
