import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, stat, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { runParity } from '../helpers/parity.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const pkg = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-enqueue-')));
const BINARY = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const RUN = 'r-enqueue-fixture', NO_NODE = '/nonexistent-code-forge-node';
const ARGS = ['review-file', 'sample.mjs', '--block', 'B1', '--run', RUN];
after(() => rm(pkg, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(pkg, 'bin'));
  await symlink(CLI, path.join(pkg, 'bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const home = process.env.CODE_FORGE_REAL_HOME ?? os.homedir();
  const build = spawnSync('go', ['build', '-trimpath', '-ldflags', `-s -w -X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}`, '-o', BINARY, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', timeout: 120_000,
    env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off', GOCACHE: path.join(home, process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build') },
  });
  assert.equal(build.status, 0, `private binary builds: ${build.stderr}`);
});

function git(repo, args) {
  const r = spawnSync('git', ['-C', repo, ...args], { encoding: 'utf8' });
  assert.equal(r.status, 0, `fixture git ${args[0]}: ${r.stderr}`);
  return r.stdout.trim();
}
async function put(root, name, value) {
  const file = path.join(root, '.code-forge', name);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, typeof value === 'string' ? value : `${JSON.stringify(value)}\n`);
}
async function fixture(config = {}) {
  const dir = await mkdtemp(path.join(pkg, 'f-'));
  const repo = path.join(dir, 'repo'), home = path.join(dir, 'home');
  await mkdir(repo); await mkdir(home);
  git(repo, ['init', '-q', '-b', 'main']);
  await writeFile(path.join(repo, 'sample.mjs'), 'export const value = "<&> café";\n');
  git(repo, ['add', 'sample.mjs']);
  git(repo, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'base']);
  const record = { run_id: RUN, workspace: repo, project: 'fixture', config: initialConfigState(config), blocks: { B1: { owned_files: ['sample.mjs'] } } };
  const save = async () => {
    await put(home, `runs/${RUN}.json`, record);
    await put(repo, `runs/${RUN}.json`, record);
  };
  await save();
  const now = new Date().toISOString();
  await put(repo, `workers/${process.pid}-unknown.json`, { pid: process.pid, start_time: 'unknown', run: RUN, started_at: now, heartbeat_at: now });
  return { repo, home, record, save };
}
async function execute(f, args = ARGS, node = false, extra = {}) {
  const child = spawn(node ? process.execPath : BINARY, node ? [CLI, ...args] : args, {
    cwd: f.repo, env: { PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '1', ...extra },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stdout = '', stderr = '';
  child.stdout.on('data', (b) => { stdout += b; }); child.stderr.on('data', (b) => { stderr += b; });
  const [code, signal] = await once(child, 'close');
  return { code, signal, stdout, stderr };
}
async function parity(f, args = ARGS, extra = {}) {
  const r = await runParity(args, { fixture: f, goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, ...extra } });
  assert.deepEqual(r.diffs, [], `${args.join(' ')}: zero process and state differences`);
  assert.equal(r.equal, true, 'harness reports equality');
}
async function done(f, id, result) {
  await put(f.repo, `queue/${id}.done`, { ticket: id, status: result.status, result: '.code-forge/reviews/result.json' });
  await put(f.repo, 'reviews/result.json', result);
}
async function tickets(f) { return (await readdir(path.join(f.repo, '.code-forge/queue'))).filter((n) => /^[a-f0-9]{24}\.json$/.test(n)); }
function clock(s) { return s.replace(/\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z/g, '<time>'); }

test('plain enqueue runs without Node and has byte parity across queue states, trees and fallbacks', async () => {
  const f = await fixture();
  // Speed proof is functional: a native success cannot have started Node.
  const first = await execute(f);
  assert.equal(first.code, 0, 'new enqueue succeeds with Node unavailable');
  assert.equal(first.stderr, '', 'new enqueue has zero stderr bytes');
  const queued = JSON.parse(first.stdout);
  assert.equal(queued.status, 'queued', 'new content is queued');
  assert.equal((await tickets(f)).length, 1, 'exactly one ticket is published');
  const target = path.join(f.repo, '.code-forge/queue', `${queued.ticket}.json`);
  const bytes = await readFile(target, 'utf8');
  assert.equal((await stat(target)).mode & 0o777, 0o644, 'published mode is 0644');
  await rm(target);
  await parity(f);
  const node = await execute(f, ARGS, true);
  assert.deepEqual(first, node, 'new enqueue emits exactly the Node answer');
  assert.equal(clock(await readFile(target, 'utf8')), clock(bytes), 'ticket bytes including key order match Node');
  await parity(f);
  assert.deepEqual(await execute(f), first, 'repeated content reuses exactly the same answer');
  const existing = await readFile(target);
  await Promise.all([execute(f), execute(f)]);
  assert.equal((await tickets(f)).length, 1, 'two concurrent callers leave exactly one ticket');
  assert.deepEqual(await readFile(target), existing, 'concurrent repeat preserves ticket bytes');
  await rm(target);
  const concurrent = await Promise.all([execute(f), execute(f)]);
  assert.deepEqual(concurrent.map((r) => r.code), [0, 0], 'both concurrent publishers succeed');
  assert.deepEqual(concurrent[0], concurrent[1], 'both publishers return the same bytes');
  assert.equal((await tickets(f)).length, 1, 'concurrent new publication has exactly one winner');
  assert.equal(clock(await readFile(target, 'utf8')), clock(bytes), 'concurrent new ticket retains Node bytes');
  await done(f, queued.ticket, { status: 'reviewed', approved: true });
  await parity(f);
  assert.equal(JSON.parse((await execute(f)).stdout).status, 'done', 'cached review returns done');
  for (const result of [{ status: 'unavailable' }, { status: 'split_required', round: 1 }, { status: 'stopped', stopped: 'split_required', lens: 'full', budget: 1 }]) {
    await done(f, queued.ticket, result);
    assert.equal((await execute(f)).code, 127, `cached ${result.status} hands over`);
    await parity(f, ARGS, { CODE_FORGE_NODE: process.execPath });
  }
  for (const args of [[...ARGS, '--fresh'], [...ARGS, '--answers', 'f1', '--of', 'source.mjs'], [...ARGS, '--rebase-check']]) {
    assert.equal((await execute(f, args)).code, 127, 'unported flags require Node');
    await parity(f, args, { CODE_FORGE_NODE: process.execPath });
  }
  await parity(f, [...ARGS.slice(0, -1), 'wrong-run']);
  await rm(path.join(f.repo, '.code-forge/workers'), { recursive: true });
  await parity(f);
  assert.equal((await execute(f)).code, 3, 'absent worker exits 3 without Node');
  const excluded = await fixture({ review: { exclude: ['**/*.{lock,json}', 'sample.mjs'] } });
  await parity(excluded);
  assert.equal((await execute(excluded)).stdout, 'not reviewed (review.exclude sample.mjs): sample.mjs\n', 'first matching exclusion prints its exact rule');
  const defaults = await fixture();
  await parity(defaults, ['review-file', 'nested/package-lock.json', '--block', 'B1']);
  // Different copy paths are part of worktree ticket hashes. Compare Node and Go
  // on the SAME disposable tree, and use the harness for main-tree queue cases.
  const tree = path.join(defaults.repo, 'tree');
  git(defaults.repo, ['worktree', 'add', '-qb', 'block-tree', tree]);
  defaults.record.blocks.B1.worktree = { path: tree, branch: 'block-tree', base_sha: git(defaults.repo, ['rev-parse', 'HEAD']) };
  await defaults.save();
  await writeFile(path.join(tree, 'sample.mjs'), 'changed in the block tree\n');
  const treeArgs = [...ARGS, '--cwd', tree];
  const treeNode = await execute(defaults, treeArgs, true);
  const treeTicket = JSON.parse(treeNode.stdout);
  assert.equal(treeTicket.tree, tree, 'answer names the block worktree');
  const treeTarget = path.join(defaults.repo, '.code-forge/queue', `${treeTicket.ticket}.json`);
  const treeBytes = await readFile(treeTarget, 'utf8');
  await rm(treeTarget);
  assert.deepEqual(await execute(defaults, treeArgs), treeNode, 'block-tree output exactly matches Node');
  assert.equal(clock(await readFile(treeTarget, 'utf8')), clock(treeBytes), 'block-tree ticket bytes match Node');
  assert.deepEqual(await execute(defaults), treeNode, 'calling from main still hashes the recorded block tree');
  await mkdir(path.join(defaults.repo, 'nested'));
  await symlink(pkg, path.join(tree, 'nested'));
  const outsideArgs = ['review-file', 'nested/outside.mjs', '--block', 'B1', '--run', RUN];
  assert.deepEqual(await execute(defaults, outsideArgs), await execute(defaults, outsideArgs, true), 'path is rechecked inside the recorded tree');
  assert.equal((await execute(defaults, outsideArgs)).code, 1, 'outside recorded-tree path is refused without Node');
  delete defaults.record.blocks.B1.worktree;
  await defaults.save();
  const mainNode = await execute(defaults, treeArgs, true);
  assert.deepEqual(await execute(defaults, treeArgs), mainNode, 'record without worktree hashes main from either tree');
  assert.equal(Object.hasOwn(JSON.parse(mainNode.stdout), 'tree'), false, 'main-tree identity omits tree');
  // Acceptance revisions alter the identity and are included after timing fields.
  f.record.blocks.B1.acceptance_revision = 'revision-2'; await f.save();
  await put(f.repo, 'queue/worker.json', { pid: process.pid, start_time: 'unknown', run: RUN });
  await parity(f);
  for (const logging of ['', '0']) await parity(f, ARGS, { CODE_FORGE_NO_ERROR_LOG: logging });
});

test('enqueue validates usage and outside-tree boundaries byte for byte without Node', async () => {
  const f = await fixture();
  const cases = [
    [[], 'give one path and --block'],
    [['sample.mjs'], 'give one path and --block'],
    [['a', 'b', '--block', 'B1'], 'give one path and --block'],
    [['sample.mjs', '--block', 'bad/id'], 'block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/'],
    [['--block'], '--block needs a value'],
    [['--block='], '--block needs a value'],
    [['--block=B1', '--block=B1'], '--block given more than once'],
    [['--json'], 'unknown flag "--json"'],
    [['--'], 'unknown flag "--"'],
    [['--fresh=yes'], '--fresh takes no value'],
    [['sample.mjs', '--block', 'B1', '--max=1s'], 'give one path and --block'],
    [['--cwd=missing-dir'], '--cwd must name an existing directory'],
    [['--cwd=sample.mjs'], '--cwd must name an existing directory'],
  ];
  for (const [tail, message] of cases) {
    const args = ['review-file', ...tail];
    const r = await execute(f, args);
    assert.equal(r.code, 2, `${tail}: usage stays native`);
    assert.equal(r.stdout, '', 'usage has zero stdout bytes');
    assert.ok(r.stderr.startsWith(`review-file: ${message}\nusage: code-forge review-file `), `exact error prefix: ${message}`);
    await parity(f, args);
    await parity(f, args, { CODE_FORGE_NO_ERROR_LOG: '0' });
  }
  await symlink(pkg, path.join(f.repo, 'outside'));
  await symlink('sample.mjs', path.join(f.repo, 'link.mjs'));
  for (const file of ['/absolute.mjs', '../sample.mjs', 'outside/no.mjs', 'link.mjs', '.git/config', '']) {
    const args = ['review-file', file, '--block', 'B1'];
    await parity(f, args);
    assert.equal((await execute(f, args)).code, 1, `${file}: refused without Node`);
  }
  assert.equal((await readdir(path.join(f.repo, '.code-forge'))).includes('queue'), false, 'invalid calls publish zero queue entries');
});
