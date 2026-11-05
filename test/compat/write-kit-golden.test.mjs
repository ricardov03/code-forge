import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, rmSync, symlinkSync } from 'node:fs';
import { chmod, mkdir, mkdtemp, readFile, rm, stat, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { appendRow } from '../../src/ledger/write.mjs';
import { repoTop, stopBlock, treeAtOpen, treeStamp } from '../../src/state/block.mjs';
import { ownedKinds } from '../../src/state/owned-kinds.mjs';
import { ownsFile } from '../../src/state/registry.mjs';
import { processStartTime, readRun, saveRun, withRunLock } from '../../src/state/run.mjs';
import { generateKey, signRow } from '../../src/state/signer.mjs';
import { changedFiles } from '../../src/review/gate-check.mjs';
import { listBadLocks, tryLock, withLock } from '../../src/util/file-locks.mjs';
import { contentHash, gitChildEnv } from '../../src/worker/ticket.mjs';
import { runParity } from '../helpers/parity.mjs';

const release = fileURLToPath(new URL(`../../native/bin/${process.platform}-${process.arch}/code-forge-native`, import.meta.url));
const pkg = mkdtempSync(path.join(os.tmpdir(), 'kit-bin-'));
const binary = path.join(pkg, 'native', 'bin', `${process.platform}-${process.arch}`, 'code-forge-native');
mkdirSync(path.dirname(binary), { recursive: true });
mkdirSync(path.join(pkg, 'bin'));
symlinkSync(fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url)), path.join(pkg, 'bin', 'code-forge.mjs'));
copyFileSync(release, binary);
chmodSync(binary, 0o755);
process.on('exit', () => rmSync(pkg, { recursive: true, force: true }));
const run = 'kit-fixture', slug = 'kit-golden', ts = '2026-10-31T01:02:03.456Z';

async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'write-kit-'));
  const home = path.join(root, 'home'), tree = path.join(root, 'tree');
  await mkdir(home); await mkdir(tree);
  const old = process.env.HOME;
  process.env.HOME = home;
  t.after(async () => { process.env.HOME = old; await rm(root, { recursive: true, force: true }); });
  return { root, home, tree };
}

function start(t, input, hold = false) {
  const child = spawn(binary, ['internal', 'kit-golden'], { env: { ...process.env, CODE_FORGE_NODE: '/nonexistent' }, stdio: ['pipe', 'pipe', 'pipe'] });
  t.after(() => { if (child.exitCode === null) child.kill(); });
  let stdout = '', stderr = '';
  child.stdout.on('data', (b) => { stdout += b; });
  child.stderr.on('data', (b) => { stderr += b; });
  child.stdin.on('error', () => {});
  const done = once(child, 'close').then(([code]) => ({ code, stdout, stderr }));
  child.stdin.write(`${JSON.stringify(input)}\n`);
  if (!hold) child.stdin.end();
  return { child, done };
}

async function go(t, input) {
  const result = await start(t, input).done;
  assert.equal(result.code, 0, `${input.op}: internal helper exits 0: ${result.stderr}`);
  assert.equal(result.stderr, '', `${input.op}: no stderr`);
  return JSON.parse(result.stdout);
}

function git(tree, ...args) {
  const r = spawnSync('git', args, { cwd: tree, env: gitChildEnv(), encoding: 'utf8' });
  assert.equal(r.status, 0, `fixture git ${args[0]}: ${r.stderr}`);
  return r.stdout;
}

const lockFile = (home, key) => path.join(home, '.code-forge', 'locks', `${createHash('sha256').update(key).digest('hex')}.lock`);
const caught = async (fn) => { try { await fn(); return null; } catch (e) { return { code: e.code, message: e.message }; } };

test('write kit matches Node locks, git, stamps, hashes, owned paths and locked signed block updates', async (t) => {
  const { home, tree } = await fixture(t);
  const entries = ['file', 'dir', 'missing', 'link', 'file/child', 'src/**/{a,b?.mjs}', '?.go', '??.go', 'pages/[id].vue', '__proto__'];
  await writeFile(path.join(tree, 'file'), 'héllo\0😀\n');
  await mkdir(path.join(tree, 'dir'));
  await symlink('file', path.join(tree, 'link'));
  const kinds = ownedKinds(tree, entries);
  const files = ['file', 'file/child', 'dir/a', 'missing/a', 'link/a', 'src/a', 'src/.dot/b1.mjs', '😀.go', 'pages/[id].vue/a', '__proto__/x', 'unowned/x'];
  assert.deepEqual(await go(t, { op: 'owned', root: tree, entries, files }), { kinds: { ...kinds }, matches: files.map((f) => ownsFile(entries, f, kinds)) }, 'all 11 owned matches and all exact kinds');
  for (const entry of entries) {
    const kinds = ownedKinds(tree, [entry]);
    assert.deepEqual(await go(t, { op: 'owned', root: tree, entries: [entry], files }), { kinds: { ...kinds }, matches: files.map((f) => ownsFile([entry], f, kinds)) }, `${entry}: all 11 positive and negative matches`);
  }

  git(tree, 'init', '-qb', 'main');
  git(tree, 'add', 'file', 'link');
  git(tree, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'base');
  const base = git(tree, 'rev-parse', 'HEAD').trim();
  const gitVars = ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_CONFIG_GLOBAL', 'GIT_TERMINAL_PROMPT'];
  const saved = gitVars.map((key) => process.env[key]);
  for (const key of gitVars) process.env[key] = '/nonexistent';
  t.after(() => gitVars.forEach((key, i) => { if (saved[i] === undefined) delete process.env[key]; else process.env[key] = saved[i]; }));
  await writeFile(path.join(tree, 'file'), 'changed\n'); await chmod(path.join(tree, 'file'), 0o755);
  await writeFile(path.join(tree, 'new\nfile'), 'untracked');
  await rm(path.join(tree, 'link'));
  const nested = path.join(tree, 'nested'); await mkdir(nested);
  git(nested, 'init', '-qb', 'main');
  await writeFile(path.join(nested, 'a'), 'nested'); git(nested, 'add', 'a');
  git(nested, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'nested');
  await writeFile(path.join(nested, 'a'), 'dirty'); await symlink('a', path.join(nested, 'untracked'));
  await mkdir(path.join(tree, 'module'));
  git(tree, 'update-index', '--add', '--cacheinfo', `160000,${base},module`);
  const paths = ['file', 'link', 'missing', 'dir', 'nested', 'module', 'file/child', 'new\nfile'];
  assert.deepEqual(await go(t, { op: 'git', root: tree, base, paths, args: ['rev-parse', 'HEAD'] }), {
    stdout: `${base}\n`, top: await repoTop(tree), changed: await changedFiles({ repoRoot: tree, base }),
    stamps: Object.fromEntries(await Promise.all(paths.map(async (p) => [p, await treeStamp(tree, p)]))), atOpen: await treeAtOpen(tree, base),
  }, 'git stdout, top, changed paths, all 8 stamps and tree-at-open match');
  for (const file of ['file', 'missing', 'new\nfile']) assert.deepEqual(await go(t, { op: 'hash', root: tree, file }), { hash: contentHash(tree, file) }, `${file}: content hash`);

  const key = '<lock>😀';
  const node = await tryLock(key); assert.equal(node.ok, true);
  try {
    assert.deepEqual(await go(t, { op: 'try-lock', key }), { ok: false, result: 'held' }, 'Go refuses a live Node owner');
    const waiter = start(t, { op: 'with-lock', key }); let finished = false; waiter.done.then(() => { finished = true; });
    await new Promise((r) => setTimeout(r, 100)); assert.equal(finished, false, 'Go waits while Node holds');
    node.release();
    assert.equal((await waiter.done).code, 0, 'Go proceeds after Node releases');
  } finally { node.release(); }
  const holder = start(t, { op: 'with-lock', key }, true);
  const ready = await Promise.race([once(holder.child.stdout, 'data').then(([b]) => b.toString()), holder.done.then((r) => r.stderr)]);
  assert.equal(ready, '{"ok":true}\n', 'Go announces acquisition');
  const file = lockFile(home, key), bytes = await readFile(file, 'utf8'), owner = JSON.parse(bytes);
  assert.equal(bytes, JSON.stringify({ key, pid: holder.child.pid, start: await processStartTime(holder.child.pid) }), 'owner bytes use Node property order and escaping');
  assert.equal(owner.key, key); assert.equal((await stat(file)).mode & 0o777, 0o600);
  assert.equal((await stat(path.dirname(file))).mode & 0o777, 0o700);
  assert.deepEqual(await tryLock(key), { ok: false, result: 'held' }, 'Node refuses a live Go owner');
  let entered = false; const waiting = withLock(key, () => { entered = true; });
  await new Promise((r) => setTimeout(r, 100)); assert.equal(entered, false, 'Node waits while Go holds');
  holder.child.stdin.end(); assert.equal((await holder.done).code, 0); await waiting;
  assert.equal(entered, true, 'Node proceeds after Go releases');

  const record = { run_id: run, project: slug, workspace: tree, status: 'active', worker: { pid: 123 }, blocks: { A: { status: 'open', owned_files: ['file'], extra: { z: 1, a: '😀' } } }, orphans: [] };
  await saveRun(record); const signer = await generateKey(run);
  const recordFile = path.join(home, '.code-forge', 'runs', `${run}.json`), mirrorFile = path.join(tree, '.code-forge', 'runs', `${run}.json`), ledgerFile = path.join(home, '.code-forge', 'ledger', `${slug}.jsonl`);
  const initial = await readFile(recordFile), mirror = await readFile(mirrorFile);
  await stopBlock({ runId: run, id: 'A', reason: 'done', writeRow: (row) => appendRow(signRow({ ...row, ts }, signer), { slug }) });
  const expected = await Promise.all([recordFile, mirrorFile, ledgerFile].map((f) => readFile(f)));
  await writeFile(recordFile, initial); await writeFile(mirrorFile, mirror); await rm(ledgerFile);
  let update;
  await withRunLock(run, async () => {
    update = start(t, { op: 'update', home, run, id: 'A', patch: { status: 'stopped', stop_reason: 'done' }, rows: [{ event: 'block.stop', block: 'A', reason: 'done', ts }] });
    let done = false; update.done.then(() => { done = true; });
    await new Promise((r) => setTimeout(r, 100)); assert.equal(done, false, 'update waits for Node run lock');
  });
  assert.equal((await update.done).code, 0, 'locked update succeeds');
  assert.deepEqual(await Promise.all([recordFile, mirrorFile, ledgerFile].map((f) => readFile(f))), expected, 'record, mirror and signed ledger row are byte equal');
  assert.equal((await stat(recordFile)).mode & 0o777, 0o600);
});

test('write kit fails closed for malformed owners, stale races, unsafe hashes and refused updates', async (t) => {
  const { home, tree } = await fixture(t);
  assert.deepEqual(await go(t, { op: 'bad-locks' }), { paths: [] }, 'absent lock directory has exactly zero bad locks');
  const key = 'boundary', file = lockFile(home, key);
  await mkdir(path.dirname(file), { recursive: true });
  for (const owner of ['{', '{}', JSON.stringify({ key: 'wrong', pid: 99999999, start: 'old' }), JSON.stringify({ key, pid: 1, start: 'old' }), JSON.stringify({ key, pid: 1e100, start: 'unknown' }), JSON.stringify({ key, pid: process.pid, start: null })]) {
    await writeFile(file, owner);
    const node = await tryLock(key);
    assert.deepEqual(await go(t, { op: 'try-lock', key }), node, `owner ${owner}: same refusal`);
    assert.equal(await readFile(file, 'utf8'), owner, 'refusal preserves all owner bytes');
    assert.deepEqual(await go(t, { op: 'bad-locks' }), { paths: listBadLocks() }, 'bad-lock listing is exact');
  }
  for (const owner of [{ key, pid: 99999999, start: 'old' }, { key, pid: process.pid, start: 'reused-pid' }]) {
    await writeFile(file, JSON.stringify(owner)); const node = await tryLock(key); assert.equal(node.ok, true); node.release();
    await writeFile(file, JSON.stringify(owner)); assert.deepEqual(await go(t, { op: 'try-lock', key }), { ok: true }, 'Go reclaims the same dead/reused owner');
  }
  const dead = JSON.stringify({ key, pid: 99999999, start: 'old' });
  await writeFile(file, dead); await writeFile(`${file}.break`, dead);
  assert.deepEqual(await go(t, { op: 'try-lock', key }), await tryLock(key), 'existing break mutex refuses both contenders');
  assert.deepEqual(await go(t, { op: 'bad-locks' }), { paths: listBadLocks() }, 'dead break mutex is bad, valid dead lock is not');
  await rm(`${file}.break`);
  const replacement = JSON.stringify({ key, pid: process.pid, start: await processStartTime(process.pid) });
  await writeFile(file, dead);
  const node = await tryLock(key, { afterRead: async () => writeFile(file, replacement) });
  await writeFile(file, dead);
  assert.deepEqual(await go(t, { op: 'try-lock', key, replaceAfterRead: replacement }), node, 'changed owner is never removed by stale breaker');
  assert.equal(await readFile(file, 'utf8'), replacement);
  await rm(file);

  await symlink('missing', path.join(tree, 'link')); await mkdir(path.join(tree, 'dir')); await writeFile(path.join(tree, 'file'), 'regular');
  for (const file of ['link', 'dir', 'file/child', '../escape', '.git/config', 'a\\b', '']) {
    assert.deepEqual(await go(t, { op: 'hash', root: tree, file }), { error: await caught(() => contentHash(tree, file)) }, `${file}: exact hash refusal`);
  }
  for (const base of ['HEAD', '-x', 'abcdef0']) {
    assert.deepEqual(await go(t, { op: 'changed', root: tree, base }), { error: await caught(() => changedFiles({ repoRoot: tree, base })) }, `${base}: exact changed-files refusal`);
  }
  for (const status of ['ended', 'active']) {
    await saveRun({ run_id: run, project: slug, workspace: tree, status, blocks: { A: { status: 'closed' } } });
    const before = await readRun(run);
    const error = await caught(() => stopBlock({ runId: run, id: 'A', reason: 'x', writeRow: async () => {} }));
    assert.deepEqual(await go(t, { op: 'update', home, run, id: 'A', patch: { status: 'stopped' } }), { error }, `${status}: update refusal matches Node`);
    assert.deepEqual(await readRun(run), before, 'refusal preserves every record field');
  }
  await saveRun({ run_id: run, project: slug, workspace: tree, status: 'active', blocks: { A: { status: 'open' } } });
  const before = await readRun(run);
  const error = await caught(() => stopBlock({ runId: run, id: 'A', reason: 'x', writeRow: async () => {} }));
  assert.deepEqual(await go(t, { op: 'update', home, run, id: 'A', patch: { status: 'stopped' }, rows: [{ event: 'block.stop', block: 'A', reason: 'x', ts }] }), { error }, 'missing signer key refuses publication');
  assert.deepEqual(await readRun(run), before, 'failed signed row leaves record unchanged');
  const locks = path.dirname(lockFile(home, key));
  await rm(locks, { recursive: true }); await writeFile(locks, 'obstruction');
  assert.deepEqual(await go(t, { op: 'try-lock', key }), { error: await caught(() => tryLock(key)) }, 'obstructed locks directory has the exact Node error code and message');
  assert.deepEqual(await go(t, { op: 'bad-locks' }), { error: await caught(() => listBadLocks()) }, 'obstructed lock listing has the exact Node error code and message');
  await rm(locks);
  const handed = spawnSync(binary, ['kit-unported'], { env: { ...process.env, CODE_FORGE_NODE: '/nonexistent' }, encoding: 'utf8' });
  assert.equal(handed.status, 127, 'unported public command still hands over to Node');
  assert.deepEqual(await runParity(['kit-unported'], { fixture: { repo: tree, home }, goCommand: binary,
    env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: process.execPath } }), { equal: true, diffs: [] }, 'handover has zero process or state differences under the parity harness');
});
