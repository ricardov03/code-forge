import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { isolatedEnv, runParity } from '../helpers/parity.mjs';
import { verifyRow } from '../../src/state/signer.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const scratch = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-custom-')));
const BINARY = path.join(scratch, 'pkg/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const ARGS = ['gates', 'run', '--run', 'fixture', '--block', 'B1'];
const NO_NODE = '/nonexistent';
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'pkg/bin'));
  await symlink(CLI, path.join(scratch, 'pkg/bin/code-forge.mjs'));
  linkGoBinary(BINARY, { ldflags: `-X main.version=0.19.0 -X main.reviewWaitNodeVersion=${process.version}` });
});
const command = (script, ...args) => [process.execPath, '-e', script, ...args];
function git(f, args) {
  const r = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8', env: isolatedEnv() });
  assert.equal(r.status, 0, r.stderr); return r.stdout.trim();
}
async function fixture(gates = {}, block = {}) {
  const root = await mkdtemp(path.join(scratch, 'case-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(path.join(f.home, '.code-forge/runs'), { recursive: true });
  git(f, ['init', '-q', '-b', 'main']);
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.test.mjs'), '// test fixture\n');
  await writeFile(path.join(f.repo, '.code-forge.yml'), JSON.stringify({ project: { slug: 'fixture' }, gates: { test: command('console.log("green")'), lint: null, types: null, format: null, rerun_failed: 0, ...gates } }));
  git(f, ['add', '.']); git(f, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture']);
  const base = git(f, ['rev-parse', 'HEAD']);
  await writeFile(path.join(f.home, '.code-forge/runs/fixture.json'), JSON.stringify({ run_id: 'fixture', project: 'fixture', workspace: f.repo, status: 'active', blocks: { B1: { status: 'open', kind: 'code', base_sha: base, owned_files: ['sample.test.mjs'], test_files: ['sample.test.mjs'], ...block } } }));
  await writeFile(path.join(f.home, '.code-forge/runs/fixture.key'), Buffer.alloc(32, 7), { mode: 0o600 });
  return f;
}
function launch(f, args = ARGS, env = {}, program = BINARY, prefix = []) {
  const child = spawn(program, [...prefix, ...args], { cwd: f.repo, env: isolatedEnv({ ...process.env, NODE_TEST_CONTEXT: undefined, HOME: f.home, CODE_FORGE_KEY_BACKEND: 'file', CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '1', ...env }), stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  child.stdout.on('data', (s) => { stdout += s; }); child.stderr.on('data', (s) => { stderr += s; });
  return { child, ended: once(child, 'close').then(([code, signal]) => ({ code, signal, stdout, stderr })) };
}
async function parity(f, args = ARGS, fallback = false, extra = {}) {
  const result = await runParity(args, { fixture: f, goCommand: BINARY, ...extra, env: isolatedEnv({ CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: fallback ? process.execPath : NO_NODE, ...extra.env }) });
  assert.deepEqual(result.diffs, [], `${args.join(' ')}: zero differences in exit, streams and every state file`);
}
async function rows(f) {
  return (await readFile(path.join(f.home, '.code-forge/ledger/fixture.jsonl'), 'utf8')).trim().split('\n').map((s) => JSON.parse(s));
}
const invocation = 'const fs=require("node:fs"); fs.appendFileSync("commands.jsonl", JSON.stringify({argv:process.argv.slice(1),cwd:process.cwd(),ci:process.env.CI,yes:process.env.npm_config_yes,mode:process.env.MODE,run:process.env.CODE_FORGE_RUN_ID})+"\\n");';

test('B289 gates run: native custom, repeats, light and flaky cases match Node including commands and signed rows', async () => {
  const custom = await fixture({ custom: [{ name: 'extra', argv: command(invocation + 'console.log(process.env.TOKEN); process.exit(3)', 'literal arg'), env: { MODE: 'test', TOKEN: 'secret-custom-value' } }] });
  const speed = await launch(custom).ended;
  assert.equal(speed.code, 1, 'custom gate failure runs natively with unavailable Node');
  assert.notEqual(speed.stdout, '', speed.stderr);
  assert.equal(JSON.parse(speed.stdout).results.length, 5, 'four fixed results and one custom result');
  assert.equal(JSON.parse(speed.stdout).results[4].stdout, '[REDACTED]\n');
  assert.deepEqual((await rows(custom)).map((r) => r.event), ['gate.red', 'gate.done']);
  await parity(custom); await parity(custom, [...ARGS, '--json']);
  const calls = JSON.parse((await readFile(path.join(custom.repo, 'commands.jsonl'), 'utf8')).trim());
  assert.deepEqual(calls, { argv: ['literal arg'], cwd: custom.repo, ci: '1', yes: 'false', mode: 'test', run: 'fixture' });

  const evidence = await fixture({ custom: [{ name: 'artifact', argv: command('require("node:fs").mkdirSync("out",{recursive:true});require("node:fs").writeFileSync("out/a.txt","proof")'), evidence: ['out/**/*.txt', 'absent'] }] });
  await mkdir(path.join(evidence.repo, 'out'));
  await writeFile(path.join(evidence.repo, 'out/café.txt'), '');
  await symlink('../sample.test.mjs', path.join(evidence.repo, 'out/link.txt'));
  await parity(evidence);
  const ev = JSON.parse((await launch(evidence).ended).stdout).results[4];
  assert.deepEqual(ev.evidence.map((e) => [e.path, e.bytes]), [['out/a.txt', 5], ['out/café.txt', 0]], 'exactly two regular evidence files, including Unicode and empty content; symlink omitted');
  assert.deepEqual(ev.missing, ['absent']);
  const excluded = await fixture({ custom: [{ name: 'extra', argv: command('process.exit(9)') }], kinds: { code: ['test'] } });
  await parity(excluded);
  assert.equal(JSON.parse((await launch(excluded).ended).stdout).results[4].reason, 'not in gates.kinds.code');

  for (const fail of [false, true]) {
    const repeat = await fixture({ test: command(invocation + `const n=Number(fs.existsSync("count")?fs.readFileSync("count","utf8"):0)+1;fs.writeFileSync("count",String(n));process.exit(${fail ? 'n===2?4:0' : '0'});`, '{test_files}'), repeat_new_tests: 2 }, { owned_files: ['new.test.mjs'], test_files: ['new.test.mjs'] });
    await writeFile(path.join(repeat.repo, 'new.test.mjs'), '// newly added test\n');
    await parity(repeat);
    await parity(repeat, [...ARGS, '--slug', 'alternate']);
    const result = await launch(repeat).ended;
    assert.equal(result.code, fail ? 1 : 0);
    const out = JSON.parse(result.stdout);
    assert.deepEqual(out.repeats, { n: 2, files: ['new.test.mjs'], failed: fail });
    assert.equal(await readFile(path.join(repeat.repo, 'count'), 'utf8'), '3', 'initial run and exactly two repeats');
    const recorded = await rows(repeat);
    assert.equal(recorded.length, fail ? 2 : 1);
    for (const row of recorded) assert.equal(verifyRow(row, Buffer.alloc(32, 7)).ok, true, 'repeat row has valid run MAC');
  }
  for (const gates of [{ repeat_new_tests: 2 }, { repeat_new_tests: 2, test: command('console.log("green")', '{test_files}'), full_suite_threshold_files: 0 }]) await parity(await fixture(gates));
  const light = await fixture({ test: command(invocation, '{test_files}'), full_suite_threshold_files: 0, kinds: { code: [] } }, { kind: 'light' });
  await parity(light, [...ARGS, '--changed']);
  const lightOut = JSON.parse((await launch(light).ended).stdout);
  assert.equal(lightOut.results[0].scope, 'related'); assert.equal(lightOut.results[0].test_count, 1);

  const flaky = await fixture({ rerun_failed: 1, test: command('const fs=require("node:fs");if(!fs.existsSync("retried")){fs.writeFileSync("retried","1");console.log("FAIL sample.test.mjs");process.exit(1)}', '{test_files}') });
  await parity(flaky); await parity(flaky, [...ARGS, '--json']);
  const recovered = await launch(flaky).ended;
  assert.equal(recovered.code, 0); assert.deepEqual(JSON.parse(recovered.stdout).flaky, ['sample.test.mjs']); assert.equal(recovered.stderr, 'flaky: sample.test.mjs\n');
  assert.deepEqual((await rows(flaky))[0].results[0].flaky, ['sample.test.mjs']);
  const realFlaky = await fixture({ rerun_failed: 1, test: [process.execPath, '--test', '--test-reporter', './reporter.mjs', '--test-concurrency', '2', '--test-shard=1/1', 'sample.test.mjs'] });
  await writeFile(path.join(realFlaky.repo, 'sample.test.mjs'), 'import test from "node:test";import assert from "node:assert/strict";import {existsSync,writeFileSync} from "node:fs";test("flaky",()=>{const seen=existsSync("retried");writeFileSync("retried","1");assert.equal(seen,true)});\n');
  await writeFile(path.join(realFlaky.repo, 'reporter.mjs'), 'export default async function* (events){for await(const event of events){if(event.type==="test:fail")yield "FAIL sample.test.mjs\\n"}}\n');
  await parity(realFlaky);
  const realRecovery = await launch(realFlaky).ended;
  assert.equal(realRecovery.code, 0, 'real failing Node test recovers with serial file rerun and preserved reporter operands');
  assert.deepEqual(JSON.parse(realRecovery.stdout).flaky, ['sample.test.mjs']);
  for (const script of ['console.log("FAIL sample.test.mjs");process.exit(2)', 'console.log("not ok 1 - unknown case");process.exit(2)']) await parity(await fixture({ rerun_failed: 1, test: command(script, '{test_files}') }));
  // Every enabled gate waits for cancellation: none must finish successfully
  // inside the 100 ms timeout just because the machine happens to be idle.
  const hanging = command('setInterval(()=>{},1000)');
  const timed = await fixture({ test: hanging, custom: [{ name: 'slow', argv: hanging }] });
  await parity(timed, [...ARGS, '--timeout-ms', '100'], false, {
    checkProcess(engine, result) {
      assert.equal(result.code, 1, `${engine}: timed gates fail`);
      assert.deepEqual(JSON.parse(String(result.stdout)).results.filter((r) => !r.skipped).map((r) => [r.gate, r.reason, r.timed_out]),
        [['test', 'timeout', true], ['slow', 'timeout', true]], `${engine}: exactly two enabled gates time out`);
    },
  });

  const cancelled = await fixture({ custom: [{ name: 'slow', argv: command('require("node:fs").writeFileSync("ready","1");setInterval(()=>{},1000)') }] });
  const nodeScript = `import {existsSync} from 'node:fs';import {runGatesVerb} from ${JSON.stringify(new URL('../../src/cli/gates.mjs', import.meta.url).href)};const c=new AbortController();const t=setInterval(()=>{if(existsSync('ready')){clearInterval(t);c.abort()}},5);try{process.exitCode=await runGatesVerb(process.argv.slice(1),{signal:c.signal})}finally{clearInterval(t)}`;
  const goScript = `import {spawn} from 'node:child_process';import {existsSync} from 'node:fs';const p=spawn(${JSON.stringify(BINARY)},process.argv.slice(1),{stdio:'inherit'});const t=setInterval(()=>{if(existsSync('ready')){clearInterval(t);p.kill('SIGTERM')}},5);p.on('close',code=>{clearInterval(t);process.exitCode=code})`;
  const reference = await launch(cancelled, ARGS.slice(1), {}, process.execPath, ['--input-type=module', '-e', nodeScript]).ended;
  const expectedRows = await readFile(path.join(cancelled.home, '.code-forge/ledger/fixture.jsonl'), 'utf8');
  await rm(path.join(cancelled.repo, 'ready'));
  await rm(path.join(cancelled.home, '.code-forge/ledger/fixture.jsonl'));
  const native = await launch(cancelled, ARGS, {}, process.execPath, ['--input-type=module', '-e', goScript]).ended;
  const normalize = (s) => s.replace(/\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z/g, '<time>').replace(/("(?:total_)?duration_ms":)\d+/g, '$1<duration>');
  assert.equal(native.code, 1); assert.equal(reference.code, 1);
  assert.equal(normalize(native.stdout), normalize(reference.stdout), 'SIGTERM custom cancellation stdout matches Node abort');
  assert.equal(native.stderr, reference.stderr);
  assert.equal(normalize(await readFile(path.join(cancelled.home, '.code-forge/ledger/fixture.jsonl'), 'utf8')), normalize(expectedRows), 'one byte-equal cancelled ledger row');
  assert.deepEqual((await rows(cancelled)).map((r) => r.event), ['gate.cancelled']);
});

test('gates run: proof.busy exits 1 with exact bytes and leaves the run file unchanged', async () => {
  const concurrent = await fixture({ custom: [{ name: 'extra', argv: command('') }] });
  const writer = spawn(BINARY, ['internal', 'state-golden', '--home', concurrent.home, '--run', 'fixture', '--op', 'lock'], { env: isolatedEnv({ ...process.env, HOME: concurrent.home }), stdio: ['pipe', 'pipe', 'pipe'] });
  await once(writer.stdout, 'data');
  const file = path.join(concurrent.home, '.code-forge/runs/fixture.json');
  const record = JSON.parse(await readFile(file, 'utf8')); record.proof_lock = { block: 'writer' };
  const expected = JSON.stringify(record);
  await writeFile(file, expected);
  // Native preflight reads before taking the run lock. Finish the fixture write
  // before launch so it cannot see the file between truncation and completion.
  const running = launch(concurrent);
  writer.stdin.end(); await once(writer, 'close');
  const busy = await running.ended;
  assert.equal(busy.code, 1, 'another proof holder refuses the gate natively');
  assert.equal(busy.stdout, '{"result":"proof.busy","holder":"writer"}\n', 'exact proof.busy stdout including its newline');
  assert.equal(busy.stderr, '', 'native proof.busy emits no stderr');
  assert.equal(await readFile(file, 'utf8'), expected, 'proof.busy preserves every run-file byte');
});

test('gates run boundary: the proof holder may run its own gates', async () => {
  const f = await fixture({ custom: [{ name: 'extra', argv: command('') }] });
  const file = path.join(f.home, '.code-forge/runs/fixture.json');
  const record = JSON.parse(await readFile(file, 'utf8')); record.proof_lock = { block: 'B1' };
  await writeFile(file, JSON.stringify(record));
  const result = await launch(f).ended;
  assert.equal(result.code, 0, `the proof holder is allowed to run natively: ${result.stderr}${result.stdout}`);
  assert.equal(JSON.parse(result.stdout).allOk, true, 'all gates succeed for the proof holder');
  assert.equal(JSON.parse(result.stdout).results.length, 5, 'four fixed gates and one custom gate');
  assert.deepEqual(JSON.parse(await readFile(file, 'utf8')).proof_lock, { block: 'B1' }, 'the holder retains its proof lock');
});

test('B289 telemetry: a still-unported gates subcommand records exactly one hand-over with its reason; B450 gates run --help runs in Go with no row', async () => {
  const f = await fixture();
  const runFile = path.join(f.home, '.code-forge/runs/fixture.json');
  const handovers = path.join(f.home, '.code-forge/native/handovers.jsonl');
  // B450: Node reads no --help on gates run (an ignored flag: the gates run);
  // Go does the same with Node unavailable and writes no hand-over row.
  await parity(f, [...ARGS, '--help']);
  const help = await launch(f, [...ARGS, '--help'], { CODE_FORGE_NATIVE: '1', CODE_FORGE_HANDOVER_LOG: '1' }).ended;
  assert.equal(help.code, 0, `gates run --help runs the gates in Go without Node: ${help.stderr}`);
  assert.equal(JSON.parse(help.stdout).allOk, true, 'the ignored flag runs the green gates');
  await assert.rejects(readFile(handovers), { code: 'ENOENT' });
  const before = await readFile(runFile, 'utf8');
  const ledgerBefore = await readFile(path.join(f.home, '.code-forge/ledger/fixture.jsonl'), 'utf8');
  const result = await launch(f, ['gates', 'detect'], { CODE_FORGE_NATIVE: '1', CODE_FORGE_HANDOVER_LOG: '1' }).ended;
  assert.equal(result.code, 127, 'gates detect still requires Node');
  const recorded = (await readFile(handovers, 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
  assert.equal(recorded.length, 1, 'one invocation writes exactly one hand-over row');
  assert.deepEqual(Object.keys(recorded[0]).sort(), ['at', 'reason', 'sub', 'verb', 'version']);
  assert.deepEqual({ ...recorded[0], at: '<time>' }, { at: '<time>', verb: 'gates', sub: 'detect', reason: 'gates-unported-subcommand', version: '0.19.0' });
  assert.equal(Number.isNaN(Date.parse(recorded[0].at)), false, 'row includes a valid timestamp');
  assert.equal(await readFile(runFile, 'utf8'), before, 'hand-over leaves run state unchanged');
  assert.equal(await readFile(path.join(f.home, '.code-forge/ledger/fixture.jsonl'), 'utf8'), ledgerBefore, 'hand-over adds no ledger row');
});

test('B289 boundary: light refusals and usage match; B450 forms run in Go with Node parity; unported subcommands hand over before writes', async () => {
  const light = await fixture({}, { kind: 'light' });
  const refused = await launch(light).ended;
  assert.equal(refused.code, 1, 'light block missing token is refused in Go');
  assert.equal(refused.stderr, 'gates run: light-tests: block tests and a gates.test {test_files} item are required\n');
  await parity(light);
  const f = await fixture({ custom: [{ name: 'extra', argv: command('') }] });
  for (const flags of [['--cwd'], ['--slug'], ['--timeout-ms'], ['--timeout-ms', '0']]) await parity(f, [...ARGS, ...flags]);
  const graph = await fixture({ repeat_new_tests: 2, test: command('', '{test_files}') }, { owned_files: ['source.mjs'] });
  await writeFile(path.join(graph.repo, 'source.mjs'), 'export const value = 1;\n');
  // B450: these forms handed over before; Go now runs them with Node unavailable.
  /** @type {[{repo: string, home: string}, string[]][]} */
  const ported = [
    [graph, ARGS],
    [await fixture({ custom: [{ name: 'extra', argv: ['/missing-gate'] }] }), ARGS],
    [await fixture({ rerun_failed: 2 }), ARGS],
    [await fixture({ custom: [{ name: 'extra', argv: command('', '{test_files}') }] }), ARGS],
    [f, ['gates', 'run']],
  ];
  for (const [sample, args] of ported) {
    assert.notEqual((await launch(sample, args).ended).code, 127, `${args.join(' ')} runs in Go without Node`);
    await parity(sample, args);
  }
  for (const args of [['gates', 'detect'], ['gates', 'scope']]) {
    const before = await readFile(path.join(f.home, '.code-forge/runs/fixture.json'), 'utf8');
    assert.equal((await launch(f, args).ended).code, 127, 'hand-over needs Node');
    assert.equal(await readFile(path.join(f.home, '.code-forge/runs/fixture.json'), 'utf8'), before, 'hand-over makes zero run writes');
    await parity(f, args, true);
  }
});
