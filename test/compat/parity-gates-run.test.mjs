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
import { linkGoBinary } from '../helpers/go-binary.mjs';
import { changedScope, isChangedTest } from '../../src/gates/expand.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-gates-'));
const BINARY = path.join(scratch, 'pkg/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const NO_NODE = '/nonexistent-code-forge-node';
const ARGS = ['gates', 'run', '--run', 'fixture', '--block', 'B1'];
// Fixture children must not inherit the enclosing gate's run or ownership.
function isolatedEnv(env) {
  const clean = { ...env };
  for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK']) delete clean[key];
  return clean;
}
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'pkg/bin'));
  await symlink(path.join(ROOT, 'bin/code-forge.mjs'), path.join(scratch, 'pkg/bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  linkGoBinary(BINARY, { ldflags: `-X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}` });
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
  const result = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8', env: isolatedEnv(process.env) });
  assert.equal(result.status, 0, `fixture git ${args[0]}: ${result.stderr}`);
  return result.stdout.trim();
}
async function record(f, value) {
  const dir = path.join(f.home, '.code-forge/runs');
  await mkdir(dir, { recursive: true });
  await writeFile(path.join(dir, 'fixture.json'), JSON.stringify(value));
}
function launch(f, args = ARGS, extra = {}, program = BINARY, prefix = []) {
  const child = spawn(program, [...prefix, ...args], { cwd: f.repo, env: isolatedEnv({ ...process.env, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '1', ...extra }), stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', (s) => { stdout += s; }); child.stderr.on('data', (s) => { stderr += s; });
  return { child, ended: once(child, 'close').then(([code, signal]) => ({ code, signal, stdout, stderr })) };
}
// B393: Go records on gate.done the clean Git tree a worktree gate ran on;
// Node never writes it. The compare drops it from Go's rows only; the
// worktree case below checks the Go value.
const GO_ONLY = { 'gate.done': ['tree'], 'gate.cancelled': ['tree'] };
async function parity(f, args = ARGS, fallback = false, env = {}) {
  const result = await runParity(args, { fixture: f, goCommand: BINARY, goOnlyFields: GO_ONLY, env: isolatedEnv({ CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: fallback ? process.execPath : NO_NODE, ...env }) });
  assert.deepEqual(result.diffs, [], `${args.join(' ')}: zero stream, exit and state differences`);
}
const normalizeRows = (bytes) => bytes.replace(/\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z/g, '<time>').replace(/("(?:total_)?duration_ms":)\d+/g, '$1<duration>');
async function rowBytes(f, args) {
  const file = path.join(f.home, '.code-forge/ledger/fixture.jsonl');
  const node = await launch(f, args, {}, process.execPath, [path.join(ROOT, 'bin/code-forge.mjs')]).ended;
  const expected = await readFile(file, 'utf8'); await rm(file);
  const native = await launch(f, args).ended;
  assert.equal(native.code, node.code, 'raw-row comparison preserves the gate exit');
  assert.equal(normalizeRows(await readFile(file, 'utf8')), normalizeRows(expected), 'all ledger row bytes match after only timestamp and duration normalization');
}

test('review cwd-equals-ignored: Node ignores joined cwd outside a worktree', async () => {
  const f = await fixture();
  const elsewhere = path.join(f.repo, 'elsewhere');
  await mkdir(elsewhere);
  await writeFile(path.join(elsewhere, '.code-forge.yml'), JSON.stringify({ gates: { test: command('process.exit(42)'), rerun_failed: 0 } }));
  for (const flags of [[`--cwd=${elsewhere}`], ['--cwd', f.repo, `--cwd=${elsewhere}`]]) {
    await parity(f, [...ARGS, ...flags]);
    assert.equal((await launch(f, [...ARGS, ...flags]).ended).code, 0, 'joined cwd must not select the failing gate');
  }
});

test('review F1 signals: native termination diagnostics match Node signal names', async () => {
  for (const signal of ['SIGHUP', 'SIGSEGV', 'SIGUSR1', 'SIGUSR2', 'SIGPIPE', 'SIGALRM']) {
    const f = await fixture({ test: ['/bin/sh', '-c', `ulimit -c 0; kill -${signal.slice(3)} $$`] });
    await parity(f);
    const result = JSON.parse((await launch(f).ended).stdout).results[0];
    assert.equal(result.reason, signal);
    assert.equal(result.stderr, `gate terminated: ${signal}\n`);
  }
});

test('review spawn/PATH: unresolvable child commands hand over to Node before running', async () => {
  const missing = await fixture({ test: { argv: ['sh', '-c', 'echo wrong-parent-path'], env: { PATH: '/nonexistent-child-path' } } });
  const denied = await fixture({ test: ['./no-execute'] });
  await writeFile(path.join(denied.repo, 'no-execute'), '#!/bin/sh\nexit 0\n', { mode: 0o600 });
  const shell = await fixture({ test: ['./plain-script'] });
  await writeFile(path.join(shell.repo, 'plain-script'), 'printf shell-fallback\n', { mode: 0o755 });
  for (const f of [missing, denied, shell]) {
    assert.equal((await launch(f).ended).code, 127, 'preflight delegates unsupported spawns to Node');
    await parity(f, ARGS, true);
  }
});

test('review F1 scope: changed Swift pairs, Python suffix follows Node filtering', async () => {
  /** @type {[string, string, boolean][]} */
  const cases = [
    ['Foo.swift', 'FooTests.swift', true],
    ['foo.py', 'foo_test.py', false],
    ['foo.py', 'test_foo.py', true],
    ['foo.py', 'tests/foo_test.py', true],
  ];
  for (const [source, candidate, expected] of cases) {
    const f = await fixture({ test: ['/bin/echo', '{test_files}'] }, { owned_files: [source], test_files: [] });
    await writeFile(path.join(f.repo, source), '// before\n');
    await mkdir(path.dirname(path.join(f.repo, candidate)), { recursive: true });
    await writeFile(path.join(f.repo, candidate), '// empty test\n');
    assert.equal(isChangedTest(candidate), expected, 'Node changed-test predicate defines candidate eligibility');
    const nodeScope = changedScope({ baseline: { [source]: 'a'.repeat(64) }, current: { files: { [source]: 'b'.repeat(64) }, candidates: [source, candidate], dependents: {} }, fallback: { scope: 'full', reason: 'no_tests' } });
    assert.deepEqual(nodeScope.files, expected ? [candidate] : []);
    assert.equal(nodeScope.reason, expected ? undefined : 'unpaired');
    assert.equal((await launch(f).ended).code, 0, 'save native baseline');
    await writeFile(path.join(f.repo, source), '// after\n');
    // B404: Go selects --changed tests itself (no Node parity): the same
    // stem pair, and nothing (a skipped gate, never the full suite) without one.
    const result = JSON.parse((await launch(f, [...ARGS, '--changed']).ended).stdout).results[0];
    assert.deepEqual([result.test_count, result.skipped, result.scope, result.reason], [expected ? 1 : 0, !expected, 'related', 'import_graph']);
    assert.equal(result.stdout, expected ? `${candidate}\n` : '');
  }
});

test('review F1 rerun_failed: Node and Go recover a failing gate with zero parity differences', async () => {
  const f = await fixture({ rerun_failed: 1, test: command('const fs=require("node:fs");if(!fs.existsSync("retried")){fs.writeFileSync("retried","1");console.log("FAIL sample.test.mjs");process.exit(1)}', '{test_files}') });
  const result = await runParity(ARGS, {
    fixture: f, goCommand: BINARY, env: isolatedEnv({ CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE }),
    prepare: async (copy) => async () => {
      assert.equal(await readFile(path.join(copy.repo, 'retried'), 'utf8'), '1', 'the initial gate failed and created its marker');
      const rows = (await readFile(path.join(copy.home, '.code-forge/ledger/fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
      assert.equal(rows.length, 1);
      assert.equal(rows[0].event, 'gate.done');
      assert.equal(rows[0].all_ok, true, 'the failed gate recovered');
      assert.deepEqual(rows[0].flaky, ['sample.test.mjs'], 'each implementation ran flaky detection');
      assert.deepEqual(rows[0].results[0].flaky, ['sample.test.mjs'], 'the test gate reran successfully');
    },
  });
  assert.deepEqual(result.diffs, [], 'rerun_failed: zero stream, exit and state differences');
});

test('review F1 repeat_new_tests: Node and Go repeat a changed test with zero parity differences', async () => {
  const f = await fixture({ repeat_new_tests: 2, test: command('const fs=require("node:fs");const n=Number(fs.existsSync("repeat-count")?fs.readFileSync("repeat-count","utf8"):0)+1;fs.writeFileSync("repeat-count",String(n))', '{test_files}') }, { owned_files: ['sample.test.mjs'] });
  const run = JSON.parse(await readFile(path.join(f.home, '.code-forge/runs/fixture.json'), 'utf8'));
  run.blocks.B1.base_sha = git(f, ['rev-parse', 'HEAD']);
  await record(f, run);
  await writeFile(path.join(f.home, '.code-forge/runs/fixture.key'), Buffer.alloc(32, 7), { mode: 0o600 });
  await writeFile(path.join(f.repo, 'sample.test.mjs'), '// changed test\n');
  const result = await runParity(ARGS, {
    fixture: f, goCommand: BINARY, env: isolatedEnv({ CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE }),
    prepare: async (copy) => async () => {
      assert.equal(await readFile(path.join(copy.repo, 'repeat-count'), 'utf8'), '3', 'each implementation ran the initial gate and exactly two repeats');
      const rows = (await readFile(path.join(copy.home, '.code-forge/ledger/fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
      assert.equal(rows.length, 1);
      assert.equal(rows[0].event, 'gate.done');
      assert.equal(rows[0].all_ok, true);
      assert.deepEqual(rows[0].repeats, { n: 2, files: ['sample.test.mjs'], failed: false }, 'each implementation recorded both repeats');
    },
  });
  assert.deepEqual(result.diffs, [], 'repeat_new_tests: zero stream, exit and state differences');
});

test('gates run: native speed and byte parity for gates, scope, lock, worktrees, usage and fallback', async () => {
  const green = await fixture();
  const speed = await launch(green).ended;
  assert.equal(speed.code, 0, `green gate runs with unavailable Node handover executable: ${speed.stderr}`);
  assert.equal(JSON.parse(speed.stdout).results.length, 4, 'exactly four built-in gate results');
  await parity(green);
  const logged = await launch(green, ARGS, { CODE_FORGE_NO_ERROR_LOG: '0' }).ended;
  assert.equal(logged.code, 0, 'default logging also runs without Node');
  await parity(green, ARGS, false, { CODE_FORGE_NO_ERROR_LOG: '0' });
  for (const { gates, timeoutMs = '10000' } of [
    { gates: { test: command('process.exit(3)'), lint: command('console.log("after red")') } },
    { gates: { test: command('setTimeout(()=>{}, 10000)') }, timeoutMs: '100' },
    { gates: { test: { argv: command('console.log(process.env.TOKEN, process.env.MODE, process.env.CI, process.env.npm_config_yes)'), env: { TOKEN: 'secret-value-123', MODE: 'test' } } } },
    { gates: { test: { argv: command('console.log(process.env.TOKEN)'), env: { TOKEN: 'gate.done' } } } },
    { gates: { test: command('console.log(process.argv.slice(1).join(","))', '{test_files}') } },
    { gates: { test: command('console.log(process.argv.slice(1).join(","))', '{test_files}'), full_suite_threshold_files: 0 } },
    { gates: { kinds: { code: [] } } },
  ]) {
    const f = await fixture(gates);
    const args = [...ARGS, '--json', '--timeout-ms', timeoutMs];
    await parity(f, args);
    await rowBytes(f, args);
    if (!Array.isArray(gates.test) && gates.test?.env?.TOKEN === 'secret-value-123') {
      const output = JSON.parse((await launch(f).ended).stdout);
      assert.equal(output.results[0].stdout, '[REDACTED] test 1 false\n', 'long env value masked, short value and child defaults retained');
      assert.deepEqual(output.results[0].env_keys, ['MODE', 'TOKEN'], 'exactly two sorted env names');
    }
  }
  const scoped = { test: command('console.log(process.argv.slice(1).join(","))', '{test_files}') };
  const noTests = await fixture(scoped, { test_files: [] }); await parity(noTests);
  // B404: --changed is selected in Go (no Node parity): the block's test,
  // scope related, reason import_graph, before and after a change.
  const changed = await fixture(scoped);
  const goChanged = async () => JSON.parse((await launch(changed, [...ARGS, '--changed']).ended).stdout).results[0];
  assert.deepEqual(await goChanged().then((r) => [r.stdout, r.scope, r.reason]), ['sample.test.mjs\n', 'related', 'import_graph']);
  assert.equal((await launch(changed).ended).code, 0, 'baseline succeeds');
  await writeFile(path.join(changed.repo, 'sample.mjs'), 'export const n = 2;\n');
  assert.deepEqual(await goChanged().then((r) => [r.stdout, r.scope, r.reason]), ['sample.test.mjs\n', 'related', 'import_graph']);
  const busy = await fixture({}, {}, { proof_lock: { block: 'B2', acquired_at: '2026-01-01T00:00:00.000Z' } });
  await parity(busy);
  const b = await launch(busy).ended; assert.equal(b.code, 1); assert.equal(b.stdout, '{"result":"proof.busy","holder":"B2"}\n');
  await parity(busy, ARGS, false, { CODE_FORGE_NO_ERROR_LOG: '0' });
  const tree = await fixture(scoped);
  const base = git(tree, ['rev-parse', 'HEAD']);
  const treePath = path.join(tree.repo, 'linked');
  git(tree, ['worktree', 'add', '-q', '-b', 'linked', treePath]);
  await record(tree, { run_id: 'fixture', project: 'fixture', workspace: tree.repo, status: 'active', blocks: { B1: { status: 'open', kind: 'code', owned_files: ['sample.mjs'], test_files: ['sample.test.mjs'], worktree: { path: treePath, branch: 'linked', base_sha: base } } } });
  for (const flags of [[], ['--cwd', treePath], ['--cwd', tree.repo], [`--cwd=${tree.repo}`]]) await parity(tree, [...ARGS, ...flags]);
  assert.equal((await launch(tree, [...ARGS, '--cwd', treePath]).ended).code, 0, 'worktree gate runs green in Go');
  const treeRows = (await readFile(path.join(tree.home, '.code-forge/ledger/fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
  assert.deepEqual(treeRows.filter((row) => row.event === 'gate.done').map((row) => row.tree),
    [git(tree, ['rev-parse', 'linked^{tree}'])], 'Go gate.done in a clean worktree records the commit tree it ran on (B393)');
  for (const flags of [['--timeout-ms'], ['--timeout-ms', 'bad'], ['--timeout-ms', '0'], ['--slug'], ['--cwd'], ['--run'], ['--block']]) {
    await parity(green, [...ARGS, ...flags]);
    await parity(green, [...ARGS, ...flags], false, { CODE_FORGE_NO_ERROR_LOG: '0' });
  }
  for (const args of [['gates', 'run', '--run', 'fixture'], ['gates', 'run', '--block', 'B1']]) await parity(green, args);
  /** @type {[Record<string, any>, Record<string, any>, number][]} */
  const ported = [
    [{ custom: [{ name: 'extra', argv: command('') }] }, {}, 0],
    [{}, { kind: 'light' }, 1],
  ];
  for (const [gates, block, code] of ported) {
    const f = await fixture(gates, block);
    assert.equal((await launch(f).ended).code, code, 'ported case runs in Go with unavailable Node');
    await parity(f);
  }
  /** @type {[Record<string, any>, Record<string, any>, string[]][]} */
  const handovers = [
    [{ test: ['/nonexistent-gate-tool'] }, {}, ARGS], [{}, {}, ['gates', 'detect']], [{}, {}, ['gates', 'run']],
  ];
  for (const [gates, block, args] of handovers) {
    const f = await fixture(gates, block);
    assert.equal((await launch(f, args).ended).code, 127, 'unported case requires Node');
    await parity(f, args, true);
  }
});

test('gates run waits for a concurrent run writer and preserves its new proof lock', async () => {
  const f = await fixture();
  const writer = spawn(BINARY, ['internal', 'state-golden', '--home', f.home, '--run', 'fixture', '--op', 'lock'], { cwd: f.repo, env: isolatedEnv({ ...process.env, HOME: f.home }), stdio: ['pipe', 'pipe', 'pipe'] });
  await once(writer.stdout, 'data');
  const run = launch(f);
  await delay(100);
  const data = JSON.parse(await readFile(path.join(f.home, '.code-forge/runs/fixture.json'), 'utf8'));
  data.proof_lock = { block: 'writer', acquired_at: '2026-01-01T00:00:00.000Z' };
  await record(f, data);
  writer.stdin.end();
  const [code] = await once(writer, 'close'); assert.equal(code, 0, 'writer releases the same run lock');
  const result = await run.ended;
  assert.equal(result.code, 1); assert.equal(result.stdout, '{"result":"proof.busy","holder":"writer"}\n');
  const saved = JSON.parse(await readFile(path.join(f.home, '.code-forge/runs/fixture.json'), 'utf8'));
  assert.deepEqual(saved, data, 'waiting gate preserves every writer field and starts no gate');
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
    running.child.kill(/** @type {NodeJS.Signals} */ (signal));
    const result = await running.ended;
    assert.equal(result.code, 1, `${signal}: cancelled exit`);
    assert.notEqual(result.stdout, '', `cancelled output: ${result.stderr}`);
    const out = JSON.parse(result.stdout);
    assert.equal(out.cancelled, true); assert.equal(out.allOk, false); assert.equal(out.results.length, 1);
    assert.throws(() => process.kill(pid, 0), /ESRCH/, `${signal}: gate child reaped`);
    const rows = (await readFile(path.join(f.home, '.code-forge/ledger/fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(rows.length, 1); assert.equal(rows[0].event, 'gate.cancelled'); assert.equal(rows[0].cancelled, true);
    const ledgerFile = path.join(f.home, '.code-forge/ledger/fixture.jsonl');
    const nativeRows = await readFile(ledgerFile, 'utf8');
    await rm(ledgerFile); await rm(path.join(f.repo, 'child.pid'));
    // Compare a real process signal to Node's AbortSignal path. Trigger the
    // latter through a fixture marker so exec's signal-forwarding listener
    // cannot race the abort and change which signal the child observes.
    const script = `import { existsSync } from 'node:fs'; import { runGatesVerb } from ${JSON.stringify(new URL('../../src/cli/gates.mjs', import.meta.url).href)}; const c = new AbortController(); const timer = setInterval(() => { if (existsSync('abort.marker')) { clearInterval(timer); c.abort(); } }, 5); try { process.exitCode = await runGatesVerb(${JSON.stringify(ARGS.slice(1))}, { signal: c.signal }); } finally { clearInterval(timer); }`;
    const node = launch(f, [], {}, process.execPath, ['--input-type=module', '-e', script]);
    let nodePid;
    for (let i = 0; i < 200; i++) {
      try { nodePid = Number(await readFile(path.join(f.repo, 'child.pid'), 'utf8')); break; } catch { await delay(10); }
    }
    assert.ok(nodePid > 1, 'Node reference gate starts before abort');
    await writeFile(path.join(f.repo, 'abort.marker'), 'abort');
    const reference = await node.ended;
    assert.equal(reference.code, result.code, `${signal}: same exit as Node abort`);
    assert.equal(normalizeRows(result.stdout), normalizeRows(reference.stdout), `${signal}: stdout bytes match Node abort`);
    assert.equal(normalizeRows(nativeRows), normalizeRows(await readFile(ledgerFile, 'utf8')), `${signal}: cancelled row bytes match Node abort`);
  }
});
