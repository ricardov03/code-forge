import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, stat, symlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const BINARY = path.join(ROOT, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const scratch = await realpath(await mkdtemp('/tmp/cf-end-hook-'));
const noNode = path.join(scratch, 'path');
const RUN = 'r-end-hook', SLUG = 'end-hook', KEY = Buffer.alloc(32, 28);
const inherited = ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK'];
function childEnv(env = process.env) {
  const clean = { ...env };
  for (const key of inherited) delete clean[key];
  return clean;
}
const env = { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_HANDOVER_LOG: '0', CODE_FORGE_KEY_BACKEND: 'file', B284_PARENT: 'parent value' };
before(async () => {
  await mkdir(noNode);
  const git = spawnSync('which', ['git'], { encoding: 'utf8', env: childEnv() });
  assert.equal(git.status, 0, git.stderr);
  await symlink(git.stdout.trim(), path.join(noNode, 'git'));
});
after(() => rm(scratch, { recursive: true, force: true }));
function git(repo, args) {
  const result = spawnSync('git', args, { cwd: repo, encoding: 'utf8', env: childEnv({ ...process.env, GIT_AUTHOR_DATE: '2020-01-01T00:00:00Z', GIT_COMMITTER_DATE: '2020-01-01T00:00:00Z' }) });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}
const recordFile = (f) => path.join(f.home, '.code-forge/runs', `${RUN}.json`);
const ledgerFile = (f) => path.join(f.home, '.code-forge/ledger', `${SLUG}.jsonl`);
const doneFile = (f) => path.join(f.home, '.code-forge/runs', RUN, 'blocks/B1.done.json');
async function rows(f) { return (await readFile(ledgerFile(f), 'utf8')).trim().split('\n').map((s) => JSON.parse(s)); }
async function fixture(mode = 'ok', external = false, reject = false) {
  const root = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const n = 1;\n');
  // Literal arguments must survive without shell expansion. The hook also proves
  // that the end and done file are published and the run lock is free on entry.
  await writeFile(path.join(f.repo, 'hook.cjs'), `
const fs = require('node:fs'), path = require('node:path');
const run = process.env.CODE_FORGE_RUN;
const dir = path.join(process.env.HOME, '.code-forge/runs');
const ledger = fs.readFileSync(path.join(process.env.HOME, '.code-forge/ledger/${SLUG}.jsonl'), 'utf8').trim().split('\\n').map(JSON.parse);
const lock = path.join(dir, run + '.lock');
fs.mkdirSync(lock); fs.rmdirSync(lock);
const done = process.env.CODE_FORGE_DONE_FILE;
const got = { argv: process.argv.slice(2), cwd: process.cwd(), stdin: fs.readFileSync(0, 'utf8'),
  env: Object.fromEntries(['CODE_FORGE_RUN','CODE_FORGE_BLOCK','CODE_FORGE_BLOCK_STATUS','CODE_FORGE_DONE_FILE','B284_PARENT', 'CODE_FORGE_RUN_ID','CODE_FORGE_OWNER_PID','CODE_FORGE_OWNER_START_TIME'].map(k => [k, process.env[k] ?? null])),
  events: ledger.map(r => r.event), done: done ? fs.readFileSync(done, 'utf8') : null };
fs.appendFileSync('.code-forge/hook-calls.jsonl', JSON.stringify(got) + '\\n');
console.log('discard stdout'); console.error('discard stderr');
if (process.argv[2] === 'fail') process.exit(7);
if (process.argv[2] === 'signal') process.kill(process.pid, 'SIGTERM');
if (process.argv[2] === 'timeout') {
  require('node:child_process').spawn(process.execPath, ['-e', "setTimeout(() => require('node:fs').writeFileSync('.code-forge/escaped-child', 'bad'), 1500)"], { stdio: 'ignore' });
  setTimeout(() => {}, 10000);
}
`);
  git(f.repo, ['init', '-qb', 'main']); git(f.repo, ['add', '.']);
  git(f.repo, ['-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'base']);
  const base = git(f.repo, ['rev-parse', 'HEAD']);
  const content = 'export const n = 2;\n';
  await writeFile(path.join(f.repo, 'sample.mjs'), content);
  if (reject) await writeFile(path.join(f.repo, 'outside.mjs'), 'unowned\n');
  const config = {
    ...(mode === 'none' ? {} : { hooks: { on_block_end: { argv: mode === 'missing' ? ['/nonexistent/hook'] : [process.execPath, 'hook.cjs', mode, 'literal $HOME; <&> café', '*.mjs'], timeout_s: 1 } } }),
    ...(external ? { review: { external: { required: true, name: '  outside owner  ' } } } : {}),
  };
  const record = { run_id: RUN, project: SLUG, workspace: f.repo, status: 'active', config: initialConfigState(config),
    worker: { pid: process.pid, started_at: await processStartTime(process.pid) },
    blocks: { B1: { status: 'open', owned_files: ['sample.mjs'], base_sha: base, tree_at_open: {}, kind: 'code', test_files: [] } }, orphans: [] };
  await mkdir(path.dirname(recordFile(f)), { recursive: true });
  await writeFile(recordFile(f), JSON.stringify(record, null, 2) + '\n');
  await writeFile(path.join(path.dirname(recordFile(f)), `${RUN}.key`), KEY, { mode: 0o600 });
  await mkdir(path.join(f.repo, '.code-forge/runs', RUN), { recursive: true });
  await writeFile(path.join(f.repo, '.code-forge/runs', RUN, 'B1.log'), 'Completed implementation.\n');
  await mkdir(path.dirname(ledgerFile(f)), { recursive: true });
  const initial = [{ event: 'dispatch', block: 'B1', level: 'L2' },
    { event: 'session', block: 'B1', usd: 1.2345 }, { event: 'session.background', block: 'B1', usd: null },
    ...(external ? [] : [{ event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: createHash('sha256').update(content).digest('hex') }])];
  await writeFile(ledgerFile(f), initial.map((r) => JSON.stringify(signRow({ run: RUN, ...r }, KEY))).join('\n') + '\n');
  return f;
}
function args(sub, json = true) { return ['block', sub, 'B1', ...(sub === 'claim' ? ['new.mjs'] : []), '--run', RUN, ...(sub === 'stop' ? ['--reason', 'finished <&> café\nnext'] : []), ...(json ? ['--json'] : [])]; }
function launch(f, argv, node = false) {
  return new Promise((resolve, reject) => {
    const child = spawn(node ? process.execPath : BINARY, node ? [CLI, ...argv] : argv, { cwd: f.repo,
      env: childEnv({ ...env, PATH: noNode, HOME: f.home }), stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', (s) => { stdout += s; }); child.stderr.on('data', (s) => { stderr += s; });
    child.on('error', reject); child.on('close', (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}
async function parity(f, argv) {
  const result = await runParity(argv, { fixture: f, goCommand: BINARY, env: childEnv({ ...env, PATH: noNode }),
    prepare: async (copy) => async () => {
      // Compare done-file bytes too: the harness normally decodes JSON state.
      try { await writeFile(path.join(copy.repo, '.code-forge/done-bytes.txt'), await readFile(doneFile(copy))); }
      catch (err) { if (err.code !== 'ENOENT') throw err; }
    } });
  assert.deepEqual(result.diffs, [], `${argv[1]}: exact Node process, state, hook, done bytes and signed row parity`);
}
async function assertEnd(f, sub, mode, expectedCode = 0) {
  const result = await launch(f, args(sub));
  assert.equal(result.code, expectedCode, `${sub}/${mode}: native exit with no Node on PATH: ${result.stderr}`);
  assert.equal(result.signal, null, 'command exits normally');
  const bytes = await readFile(doneFile(f), 'utf8');
  const done = JSON.parse(bytes);
  const status = sub === 'stop' ? 'stopped' : 'closed';
  assert.equal(bytes, JSON.stringify({ block: 'B1', run_id: RUN, status, event: `block.${sub}`, reason: sub === 'stop' ? 'finished <&> café\nnext' : null,
    commit: git(f.repo, ['rev-parse', 'HEAD']), spent_usd: 1.2345, unknown_usd_sessions: 1, findings: 0, at: done.at, ...(done.reviewed ? { reviewed: done.reviewed } : {}) }) + '\n', 'exact done-file order, values, UTF-8 and newline');
  assert.equal((await stat(doneFile(f))).mode & 0o777, 0o600, 'private atomic file');
  assert.deepEqual(await readdir(path.dirname(doneFile(f))), ['B1.done.json'], 'no temporary done file remains');
  assert.equal(JSON.parse(result.stdout).done_file, doneFile(f), 'JSON points to the one done file');
  const all = await rows(f), hooks = all.filter((r) => r.event === 'hook.ran');
  assert.equal(hooks.length, 1, 'one hook row per end');
  const hook = hooks[0];
  assert.deepEqual(Object.keys(hook), ['run', 'event', 'block', 'hook', 'argv0', 'exit', 'signal', 'timed_out', 'duration_ms', 'ts', 'mac', 'tokens_source', 'cost_source'], 'exact ordered signed hook row keys');
  assert.equal(verifyRow(hook, KEY).ok, true, 'hook row has a valid MAC');
  assert.deepEqual({ block: hook.block, hook: hook.hook, argv0: hook.argv0, exit: hook.exit, signal: hook.signal, timed_out: hook.timed_out },
    { block: 'B1', hook: 'on_block_end', argv0: mode === 'missing' ? 'hook' : path.basename(process.execPath), exit: mode === 'missing' || mode === 'timeout' || mode === 'signal' ? null : mode === 'fail' ? 7 : 0,
      signal: mode === 'timeout' ? 'SIGKILL' : mode === 'signal' ? 'SIGTERM' : null, timed_out: mode === 'timeout' }, 'exact hook outcome');
  assert.ok(Number.isInteger(hook.duration_ms) && hook.duration_ms >= (mode === 'timeout' ? 900 : 0), 'measured hook duration');
  assert.equal(all.at(-2).event, `block.${sub}`, 'end row precedes the hook row');
  const warning = mode === 'missing' ? 'not found' : mode === 'timeout' ? 'timed out' : mode === 'fail' ? 'exit 7' : mode === 'signal' ? 'signal SIGTERM' : null;
  assert.equal(result.stderr.split('\n').filter((s) => s.startsWith('warning:')).join('\n'), warning ? `warning: on_block_end hook ${warning}` : '', 'one exact hook warning only on stderr');
  assert.equal(result.stdout.includes('discard'), false, 'hook stdout discarded');
  assert.equal(result.stderr.includes('discard'), false, 'hook stderr discarded');
  if (mode !== 'missing') {
    const calls = (await readFile(path.join(f.repo, '.code-forge/hook-calls.jsonl'), 'utf8')).trim().split('\n').map((s) => JSON.parse(s));
    assert.equal(calls.length, 1, 'hook invoked exactly once');
    assert.deepEqual(calls[0], { argv: [mode, 'literal $HOME; <&> café', '*.mjs'], cwd: f.repo, stdin: '',
      env: { CODE_FORGE_RUN: RUN, CODE_FORGE_BLOCK: 'B1', CODE_FORGE_BLOCK_STATUS: status, CODE_FORGE_DONE_FILE: doneFile(f), B284_PARENT: 'parent value', CODE_FORGE_RUN_ID: null, CODE_FORGE_OWNER_PID: null, CODE_FORGE_OWNER_START_TIME: null },
      events: all.slice(0, -1).map((r) => r.event), done: bytes }, 'literal argv, inherited env, closed stdin, unlocked run and done bytes visible on hook entry');
  }
  if (mode === 'timeout') {
    await delay(700);
    await assert.rejects(readFile(path.join(f.repo, '.code-forge/escaped-child')), { code: 'ENOENT' }, 'timeout kills the descendant process group');
  }
}

test('claim-stop, stop and close hooks and external-review closes run in Go with Node byte parity', async (t) => {
  for (const sub of ['claim-stop', 'stop', 'close']) await t.test(sub, async () => {
    const f = await fixture();
    if (sub === 'claim-stop') {
      await parity(f, args('claim'));
      const claimed = await launch(f, args('claim'));
      assert.equal(claimed.code, 0, `claim with snapshot hook needs no Node: ${claimed.stderr}`);
      assert.equal((await rows(f)).filter((r) => r.event === 'hook.ran').length, 0, 'claim does not end a block');
      await assert.rejects(readFile(doneFile(f)), { code: 'ENOENT' }, 'claim writes no done file');
    }
    const end = sub === 'claim-stop' ? 'stop' : sub;
    for (const json of [false, true]) await parity(f, args(end, json));
    await assertEnd(f, end, 'ok');
  });
  for (const reject of [false, true]) await t.test(`external reviewer ${reject ? 'reject' : 'approve'}`, async () => {
    const f = await fixture('none', true, reject);
    for (const json of [false, true]) await parity(f, args('close', json));
    const result = await launch(f, args('close'));
    assert.equal(result.code, reject ? 1 : 0, `external-review close without Node: ${result.stderr}`);
    const all = await rows(f), waived = all.filter((r) => r.event === 'gate.reviews_waived');
    assert.equal(waived.length, reject ? 0 : 1, 'only approved close signs the external waiver');
    assert.equal(all.filter((r) => r.event === 'block.close').length, reject ? 0 : 1, 'exact close row count');
    assert.equal(all.filter((r) => !verifyRow(r, KEY).ok).length, 0, 'all external-review rows signed');
    if (!reject) assert.deepEqual(Object.fromEntries(Object.entries(waived[0]).filter(([k]) => !['ts', 'mac'].includes(k))), { run: RUN, event: 'gate.reviews_waived', block: 'B1', by: 'config', reviewer: 'outside owner', tokens_source: null, cost_source: null }, 'trimmed snapshot reviewer and exact waiver fields');
  });
});

test('hook failures and timeouts preserve end outputs and sign exactly one outcome', async (t) => {
  for (const sub of ['stop', 'close']) for (const mode of ['missing', 'fail', 'signal', 'timeout']) await t.test(`${sub}/${mode}`, async () => {
    const f = await fixture(mode);
    await parity(f, args(sub));
    await assertEnd(f, sub, mode);
  });
});
