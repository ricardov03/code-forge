import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { cpSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync, chmodSync } from 'node:fs';
import { cpus } from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { Worker } from 'node:worker_threads';

const root = fileURLToPath(new URL('../../', import.meta.url));
const pkg = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8'));
const host = `${process.platform}-${process.arch}`;
const scratch = realpathSync(mkdtempSync('/tmp/cfe-'));
const builtBinary = path.join(scratch, 'code-forge-native');
const env = { ...process.env, CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: process.execPath, CODE_FORGE_KEY_BACKEND: 'file' };
after(() => rmSync(scratch, { recursive: true, force: true }));
before(() => {
  const build = spawnSync('go', ['build', '-trimpath', '-ldflags', `-s -w -X main.version=${pkg.version}`, '-o', builtBinary, './cmd/code-forge-native'], {
    cwd: path.join(root, 'native'), encoding: 'utf8', timeout: 120_000,
    env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' },
  });
  assert.equal(build.status, 0, 'host binary builds before entry assertions: ' + build.stderr);
});

function fixture(t, probe = false) {
  const dir = mkdtempSync(path.join(scratch, 'pkg with spaces-'));
  const binary = path.join(dir, 'native/bin', host, 'code-forge-native');
  mkdirSync(path.dirname(binary), { recursive: true });
  mkdirSync(path.join(dir, 'bin'));
  mkdirSync(path.join(dir, 'temp bin'));
  copyFileSync(builtBinary, binary);
  copyFileSync(path.join(root, 'package.json'), path.join(dir, 'package.json'));
  const script = path.join(dir, 'bin/code-forge.mjs');
  if (probe) {
    writeFileSync(script, `#!/usr/bin/env node
import { readFileSync } from 'node:fs';
process.stdout.write(JSON.stringify({args: process.argv.slice(2), input: readFileSync(0, 'utf8'), cwd: process.cwd(), token: process.env.ENTRY_TOKEN, pid: process.pid}));
process.stderr.write('probe stderr\\n');
process.exitCode = Number(process.env.ENTRY_EXIT || 0);
`);
    chmodSync(script, 0o755);
  } else symlinkSync(path.join(root, 'bin/code-forge.mjs'), script);
  if (existsSync(path.join(root, 'bin/code-forge'))) copyFileSync(path.join(root, 'bin/code-forge'), path.join(dir, 'bin/code-forge'));
  // Two relative links match npm's bin link and exercise resolution at each hop.
  symlinkSync(path.join('..', pkg.bin['code-forge']), path.join(dir, 'temp bin/middle'));
  const launcher = path.join(dir, 'temp bin/code-forge');
  symlinkSync('middle', launcher);
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return { dir, binary, script, launcher };
}

function run(file, args, options = {}) {
  const result = spawnSync(file, args, { env, cwd: scratch, encoding: 'utf8', timeout: 15_000, ...options });
  assert.equal(result.error, undefined, 'entry must execute: ' + result.error?.message);
  assert.equal(result.signal, null, 'entry exits normally');
  return { stdout: result.stdout, stderr: result.stderr, code: result.status, pid: result.pid };
}

function output(result) {
  const { pid, ...rest } = result;
  return rest;
}

test('npm entry selects Go or Node and preserves all process inputs and outputs', (t) => {
  const f = fixture(t, true);
  const native = run(f.launcher, ['internal', 'version'], { env: { ...env, PATH: '/usr/bin:/bin', CODE_FORGE_NODE: 'entry-node-does-not-exist' } });
  assert.equal(native.code, 0, 'native entry version exits zero');
  assert.equal(JSON.parse(native.stdout).version, pkg.version, 'enabled npm entry starts Go, which emits its package version');
  assert.equal(native.stderr, '', 'Go version emits no stderr');
  const args = ['unported', 'two words', 'single\'quote', 'double"quote', '', '$HOME; *'];
  const input = 'stdin with spaces, quotes "\' and unicode é\n';
  for (const mode of ['native', 'disabled', 'missing']) {
    if (mode === 'missing') rmSync(f.binary);
    for (const code of [0, 1, 2, 3]) {
      const result = run(f.launcher, args, { cwd: f.dir, input, env: { ...env, ENTRY_TOKEN: 'kept "verbatim"', ENTRY_EXIT: String(code), CODE_FORGE_NATIVE: mode === 'disabled' ? '0' : '1' } });
      assert.equal(result.code, code, mode + ': exit ' + code + ' passes through');
      assert.equal(result.stderr, 'probe stderr\n', mode + ': stderr passes through once');
      assert.deepEqual(JSON.parse(result.stdout), { args, input, cwd: realpathSync(f.dir), token: 'kept "verbatim"', pid: result.pid }, mode + ': argv, stdin, cwd, env, stdout and exec PID pass through');
    }
  }
});

test('empty public route table hands unknown verbs and aliases to Node through linked entries', (t) => {
  const f = fixture(t);
  const binaryLink = path.join(f.dir, 'temp bin/native-link');
  symlinkSync(path.relative(path.dirname(binaryLink), f.binary), binaryLink);
  for (const args of [['unknown-entry-verb', 'with spaces', 'a"b'], ['--help'], ['--version'], []]) {
    const expected = output(run(process.execPath, [path.join(root, 'bin/code-forge.mjs'), ...args]));
    assert.deepEqual(output(run(f.launcher, args)), expected, JSON.stringify(args) + ': npm symlink matches Node stdout, stderr and exit');
    assert.deepEqual(output(run(binaryLink, args)), expected, JSON.stringify(args) + ': executable symlink matches Node stdout, stderr and exit');
  }
});

test('internal namespace preserves native callers including doctor and daemon startup', async (t) => {
  const f = fixture(t);
  const version = run(f.binary, ['internal', 'version']);
  assert.equal(version.code, 0, 'internal version exits zero');
  assert.equal(version.stderr, '', 'internal version has no diagnostic');
  assert.deepEqual(Object.keys(JSON.parse(version.stdout)).sort(), ['goVersion', 'version'], 'internal version emits exactly the two JSON fields');
  assert.equal(JSON.parse(version.stdout).version, pkg.version, 'internal version reports package version');
  assert.match(JSON.parse(version.stdout).goVersion, /^go\d+\./, 'internal version reports Go runtime');
  assert.deepEqual(output(run(f.binary, ['--version'], { env: { ...env, CODE_FORGE_NODE: '' } })), output(run(process.execPath, [f.script, '--version'])), 'unset Node override resolves node from PATH');
  for (const verb of ['version', 'verify-ledger', 'serve', 'state-golden', 'block-rebase-landed']) {
    assert.deepEqual(output(run(f.binary, [verb])), output(run(process.execPath, [f.script, verb])), verb + ' without internal goes to Node');
    if (verb !== 'version') {
      const invalid = run(f.binary, ['internal', verb]);
      assert.equal(invalid.code, 2, verb + ': internal flags retain usage exit 2');
      assert.match(invalid.stderr, new RegExp(verb + ' requires --home'), verb + ': internal handler parses flags');
    }
  }
  // Copy the helper into a private package so nativePath cannot race installation tests.
  cpSync(path.join(root, 'src'), path.join(f.dir, 'src'), { recursive: true });
  symlinkSync(path.join(root, 'schema'), path.join(f.dir, 'schema'));
  const modules = path.dirname(path.dirname(fileURLToPath(import.meta.resolve('yaml/package.json'))));
  symlinkSync(modules, path.join(f.dir, 'node_modules'));
  const native = await import(pathToFileURL(path.join(f.dir, 'src/util/native.mjs')).href);
  const previous = { HOME: process.env.HOME, CODE_FORGE_NATIVE: process.env.CODE_FORGE_NATIVE };
  process.env.HOME = path.join(scratch, `h-${process.pid}`);
  process.env.CODE_FORGE_NATIVE = '1';
  mkdirSync(process.env.HOME, { recursive: true });
  t.after(() => {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
  });
  assert.equal(native.nativePath(), f.binary, 'helper finds exactly the private host binary');
  const viaHelper = await native.runNative(['version']);
  assert.equal(viaHelper.code, 0, 'Node helper reaches internal version');
  assert.equal(JSON.parse(viaHelper.stdout).version, pkg.version, 'Node helper retains native JSON output');
  const landed = await native.runNative(['block-rebase-landed']);
  assert.equal(landed.code, 2, 'block rebase helper retains validation exit');
  assert.match(landed.stderr, /block-rebase-landed requires --home/, 'block rebase helper reaches internal handler');
  const doctor = await import(pathToFileURL(path.join(f.dir, 'src/cli/doctor.mjs')).href);
  let doctorOutput = '';
  await doctor.runDoctorCli(['--json', '--cwd', f.dir], { stdout: { write: (s) => { doctorOutput += s; } }, stderr: { write: () => {} }, env });
  const rows = JSON.parse(doctorOutput).rows.filter((row) => row.id === 'native');
  assert.equal(rows.length, 1, 'doctor emits exactly one native row');
  assert.equal(rows[0].status, 'OK', 'doctor negotiates native version');
  assert.equal(rows[0].detail, f.binary + ', version ' + pkg.version, 'doctor identifies binary and version');
  const repo = path.join(scratch, `repo-${process.pid}`);
  mkdirSync(repo);
  assert.equal(run('git', ['init', '-q', repo]).code, 0, 'daemon fixture repository initializes');
  const { startRun } = await import(pathToFileURL(path.join(f.dir, 'src/state/run.mjs')).href);
  const runID = 'entry-daemon';
  await startRun({ workspace: repo, project: 'entry-fixture', runId: runID, config: {}, writeRow: async () => {} });
  const stop = await native.startNativeDaemon(runID, {}, { ...env, HOME: process.env.HOME });
  try {
    const ping = await native.daemonRequest(runID, { method: 'ping' });
    assert.equal(ping?.ok, true, 'daemon spawned by Node helper answers ping');
    assert.equal(ping?.run, runID, 'daemon answers for the requested run');
    assert.equal(ping?.version, pkg.version, 'daemon negotiates the package version');
  } finally { await stop(); }
});

async function fullCpuLoad(t) {
  const workers = [];
  t.after(async () => { await Promise.all(workers.map((worker) => worker.terminate())); });
  const ready = [];
  for (let i = 0; i < cpus().length; i++) {
    const worker = new Worker(`
      const { parentPort } = require('node:worker_threads');
      parentPort.postMessage('ready');
      for (;;) {}
    `, { eval: true, execArgv: [] });
    workers.push(worker);
    ready.push(once(worker, 'message'));
  }
  assert.deepEqual(await Promise.all(ready), workers.map(() => ['ready']), workers.length + ' CPU load workers are running before timing');
  t.diagnostic(`CPU load: ${workers.length} busy workers for ${cpus().length} logical CPUs`);
}

function handoverOverhead(t, f, slow = false) {
  const npmLink = path.join(f.dir, 'temp bin/npm-code-forge');
  symlinkSync(path.join('..', pkg.bin['code-forge']), npmLink);
  const delayed = path.join(f.dir, 'slow-handover');
  writeFileSync(delayed, '#!/bin/sh\nsleep 0.04\nexec "$@"\n', { mode: 0o755 });
  const entry = slow ? delayed : npmLink;
  const entryArgs = slow ? [npmLink, '--version'] : ['--version'];
  // Both modes use the same npm link, shell resolution and Node executable.
  // Comparing against bare Node charges the Go budget for shell subprocesses
  // waiting to be scheduled, which grows with machine load.
  const timingEnv = { ...env, PATH: `${path.dirname(process.execPath)}${path.delimiter}${process.env.PATH || ''}` };
  const measure = (file, args, native = '1') => {
    const start = performance.now();
    const result = run(file, args, { env: { ...timingEnv, CODE_FORGE_NATIVE: native } });
    const elapsed = performance.now() - start;
    assert.equal(result.code, 0, 'version timing command succeeds: ' + result.stderr);
    assert.equal(result.stdout, pkg.name + ' ' + pkg.version + '\n', 'timed command prints exactly the package name and version');
    return elapsed;
  };
  // Assert Go actually hands over before measuring the public linked entry.
  assert.deepEqual(output(run(f.binary, ['--version'])), output(run(process.execPath, [f.script, '--version'])), 'timing measures a working Go handover');
  measure(npmLink, ['--version'], '0');
  measure(entry, entryArgs);
  const median = (values) => { values.sort((a, b) => a - b); return (values[4] + values[5]) / 2; };
  const attempts = [];
  for (let attempt = 1; attempt <= 3; attempt++) {
    const direct = [], entries = [], differences = [];
    // Pair samples under the same load, alternating which mode runs first.
    for (let i = 0; i < 10; i++) {
      const control = () => direct.push(measure(npmLink, ['--version'], '0'));
      const handover = () => entries.push(measure(entry, entryArgs));
      if (i % 2 === 0) { control(); handover(); } else { handover(); control(); }
      differences.push(entries[i] - direct[i]);
    }
    const nodeMedian = median(direct), entryMedian = median(entries);
    const overhead = median(differences);
    const summary = `attempt ${attempt}, 10 pairs: Node control median ${nodeMedian.toFixed(2)} ms; Go entry median ${entryMedian.toFixed(2)} ms; paired overhead ${overhead.toFixed(2)} ms`;
    attempts.push(overhead);
    t.diagnostic(summary);
  }
  return Math.min(...attempts);
}

function assertHandoverBudget(overhead) {
  assert.ok(overhead <= 25, 'Go handover overhead ' + overhead.toFixed(2) + ' ms must be <=25 ms under full CPU load');
}

test('Go handover stays within 25 ms under full CPU load', async (t) => {
  const f = fixture(t);
  await fullCpuLoad(t);
  const overhead = handoverOverhead(t, f);
  assertHandoverBudget(overhead);
});

test('Go handover budget rejects a 40 ms delay under full CPU load', async (t) => {
  const f = fixture(t);
  await fullCpuLoad(t);
  const overhead = handoverOverhead(t, f, true);
  assert.throws(() => assertHandoverBudget(overhead), { code: 'ERR_ASSERTION' }, '40 ms delayed handover overhead ' + overhead.toFixed(2) + ' ms must fail the same 25 ms budget check');
});

test('entry boundaries handle nonexecutable native files and missing Node with one error', (t) => {
  const f = fixture(t, true);
  for (const node of ['', 'entry-node-does-not-exist']) {
    const missing = run(f.binary, ['unported'], { env: { ...env, CODE_FORGE_NODE: node, PATH: '' } });
    assert.equal(missing.code, 127, 'missing Node exits exactly 127');
    assert.equal(missing.stdout, '', 'missing Node emits zero stdout bytes');
    assert.equal(missing.stderr.trim().split('\n').length, 1, 'missing Node emits exactly one diagnostic');
    assert.match(missing.stderr, /node.*not found/i, 'missing Node diagnostic explains the missing executable');
  }
  const unknown = run(f.binary, ['internal', 'unknown']);
  assert.equal(unknown.code, 2, 'unknown internal verb exits 2');
  assert.equal(unknown.stdout, '', 'unknown internal verb never reaches Node probe');
  assert.match(unknown.stderr, /usage: code-forge-native internal/, 'internal usage names the reserved namespace');
  chmodSync(f.binary, 0o644);
  const fallback = run(f.launcher, ['nonexecutable fallback'], { input: 'boundary stdin' });
  assert.equal(fallback.code, 0, 'nonexecutable binary uses Node successfully');
  assert.equal(fallback.stderr, 'probe stderr\n', 'Node fallback emits its exact stderr');
  assert.deepEqual(JSON.parse(fallback.stdout).args, ['nonexecutable fallback'], 'Node fallback preserves the one argument');
  assert.equal(JSON.parse(fallback.stdout).input, 'boundary stdin', 'Node fallback receives stdin');
});
