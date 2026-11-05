import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-gates-'));
const BINARY = path.join(scratch, 'pkg/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const NO_NODE = '/nonexistent-code-forge-node';
const ARGS = ['gates', 'run', '--run', 'fixture', '--block', 'B1'];
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'pkg/bin'));
  await symlink(path.join(ROOT, 'bin/code-forge.mjs'), path.join(scratch, 'pkg/bin/code-forge.mjs'));
  const home = process.env.CODE_FORGE_REAL_HOME ?? os.homedir();
  const build = spawnSync('go', ['build', '-o', BINARY, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off', GOCACHE: path.join(home, process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build') },
  });
  assert.equal(build.status, 0, `private native binary builds: ${build.stderr}`);
});

const command = (script, ...args) => [process.execPath, '-e', script, ...args];
async function fixture(gates = {}, block = {}, extra = {}) {
  const root = await mkdtemp(path.join(scratch, 'case-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  git(f, ['init', '-q', '-b', 'main']);
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const n = 1;\n');
  await writeFile(path.join(f.repo, 'sample.test.mjs'), '// fixture test\n');
  await writeFile(path.join(f.repo, '.code-forge.yml'), JSON.stringify({ project: { slug: 'fixture' }, gates: { test: command('console.log("green")'), lint: null, types: null, format: null, rerun_failed: 0, ...gates } }));
  git(f, ['add', '.']);
  git(f, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture']);
  await record(f, { run_id: 'fixture', project: 'fixture', workspace: f.repo, status: 'active', blocks: { B1: { status: 'open', kind: 'code', owned_files: ['sample.mjs'], test_files: ['sample.test.mjs'], ...block } }, ...extra });
  return f;
}
function git(f, args) {
  const result = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8' });
  assert.equal(result.status, 0, `fixture git ${args[0]}: ${result.stderr}`);
  return result.stdout.trim();
}
async function record(f, value) {
  const dir = path.join(f.home, '.code-forge/runs');
  await mkdir(dir, { recursive: true });
  await writeFile(path.join(dir, 'fixture.json'), JSON.stringify(value));
}
function launch(f, args = ARGS, extra = {}) {
  const child = spawn(BINARY, args, { cwd: f.repo, env: { ...process.env, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '1', ...extra }, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', (s) => { stdout += s; }); child.stderr.on('data', (s) => { stderr += s; });
  return { child, ended: once(child, 'close').then(([code, signal]) => ({ code, signal, stdout, stderr })) };
}
async function parity(f, args = ARGS, fallback = false) {
  const result = await runParity(args, { fixture: f, goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: fallback ? process.execPath : NO_NODE } });
  assert.deepEqual(result.diffs, [], `${args.join(' ')}: zero stream, exit and state differences`);
}

test('gates run: native speed and byte parity for gates, scope, lock, worktrees, usage and fallback', async () => {
  const green = await fixture();
  const speed = await launch(green).ended;
  assert.equal(speed.code, 0, `green gate runs with unavailable Node handover executable: ${speed.stderr}`);
  assert.equal(JSON.parse(speed.stdout).results.length, 4, 'exactly four built-in gate results');
  await parity(green);
  for (const gates of [
    { test: command('process.exit(3)'), lint: command('console.log("after red")') },
    { test: command('setTimeout(()=>{}, 10000)') },
    { test: { argv: command('console.log(process.env.TOKEN, process.env.MODE, process.env.CI, process.env.npm_config_yes)'), env: { TOKEN: 'secret-value-123', MODE: 'test' } } },
    { test: command('console.log(process.argv.slice(1).join(","))', '{test_files}') },
    { test: command('console.log(process.argv.slice(1).join(","))', '{test_files}'), full_suite_threshold_files: 0 },
    { kinds: { code: [] } },
  ]) {
    const f = await fixture(gates);
    await parity(f, [...ARGS, '--json', '--timeout-ms', '100']);
  }
  const scoped = { test: command('console.log(process.argv.slice(1).join(","))', '{test_files}') };
  const noTests = await fixture(scoped, { test_files: [] }); await parity(noTests);
  const changed = await fixture(scoped); await parity(changed, [...ARGS, '--changed']);
  assert.equal((await launch(changed).ended).code, 0, 'baseline succeeds');
  await writeFile(path.join(changed.repo, 'sample.mjs'), 'export const n = 2;\n');
  await parity(changed, [...ARGS, '--changed']);
  const busy = await fixture({}, {}, { proof_lock: { block: 'B2', acquired_at: '2026-01-01T00:00:00.000Z' } });
  await parity(busy);
  const b = await launch(busy).ended; assert.equal(b.code, 1); assert.equal(b.stdout, '{"result":"proof.busy","holder":"B2"}\n');
  const tree = await fixture(scoped);
  const base = git(tree, ['rev-parse', 'HEAD']);
  const treePath = path.join(tree.repo, 'linked');
  git(tree, ['worktree', 'add', '-q', '-b', 'linked', treePath]);
  await record(tree, { run_id: 'fixture', project: 'fixture', workspace: tree.repo, status: 'active', blocks: { B1: { status: 'open', kind: 'code', owned_files: ['sample.mjs'], test_files: ['sample.test.mjs'], worktree: { path: treePath, branch: 'linked', base_sha: base } } } });
  for (const flags of [[], ['--cwd', treePath], ['--cwd', tree.repo], [`--cwd=${tree.repo}`]]) await parity(tree, [...ARGS, ...flags]);
  for (const flags of [['--timeout-ms'], ['--timeout-ms', 'bad'], ['--timeout-ms', '0'], ['--slug'], ['--cwd'], ['--run'], ['--block']]) await parity(green, [...ARGS, ...flags]);
  for (const args of [['gates', 'run', '--run', 'fixture'], ['gates', 'run', '--block', 'B1']]) await parity(green, args);
  for (const [gates, block, args] of [
    [{ repeat_new_tests: 2 }, {}, ARGS], [{ custom: [{ name: 'extra', argv: command('') }] }, {}, ARGS],
    [{}, { kind: 'light' }, ARGS], [{ rerun_failed: 1 }, {}, ARGS], [{}, {}, ['gates', 'detect']], [{}, {}, ['gates', 'run']],
  ]) {
    const f = await fixture(gates, block);
    assert.equal((await launch(f, args).ended).code, 127, 'unported case requires Node');
    await parity(f, args, true);
  }
});

test('gates run cancellation stops the gate and records exactly one cancelled row', { timeout: 15_000 }, async () => {
  for (const signal of ['SIGINT', 'SIGTERM']) {
    const f = await fixture({ test: command('require("node:fs").writeFileSync("child.pid", String(process.pid)); setTimeout(()=>{}, 30000)') });
    const running = launch(f);
    let pid;
    for (let i = 0; i < 200; i++) {
      try { pid = Number(await readFile(path.join(f.repo, 'child.pid'), 'utf8')); break; } catch { await delay(10); }
    }
    assert.ok(pid > 1, `${signal}: gate starts`);
    running.child.kill(signal);
    const result = await running.ended;
    assert.equal(result.code, 1, `${signal}: cancelled exit`);
    assert.notEqual(result.stdout, '', `cancelled output: ${result.stderr}`);
    const out = JSON.parse(result.stdout);
    assert.equal(out.cancelled, true); assert.equal(out.allOk, false); assert.equal(out.results.length, 1);
    assert.throws(() => process.kill(pid, 0), /ESRCH/, `${signal}: gate child reaped`);
    const rows = (await readFile(path.join(f.home, '.code-forge/ledger/fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(rows.length, 1); assert.equal(rows[0].event, 'gate.cancelled'); assert.equal(rows[0].cancelled, true);
  }
});
