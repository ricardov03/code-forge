import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const WRAPPER = path.join(REPO, 'test/helpers/run-suite.sh');
const NODE = process.execPath;

/** @param {import('node:test').TestContext} t */
function fixture(t) {
  const root = mkdtempSync(path.join(os.tmpdir(), 'suite-sandbox-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const home = path.join(root, 'home');
  const tmp = path.join(root, 'tmp');
  const bin = path.join(root, 'bin');
  const guarded = [path.join(home, '.code-forge'), path.join(tmp, 'code-forge')];
  for (const dir of [home, tmp, bin, guarded[0]]) mkdirSync(dir, { recursive: true });
  // Exercise realpath through an alias, including a guarded directory not yet created.
  const alias = path.join(root, 'alias');
  symlinkSync(root, alias);
  const stages = path.join(root, 'stages.jsonl');
  const sample = path.join(root, 'sample.test.mjs');
  const shim = path.join(bin, 'node');
  symlinkSync('/bin/ps', path.join(bin, 'ps'));
  writeFileSync(shim, `#!${NODE}
const fs = require('node:fs');
const cp = require('node:child_process');
const args = process.argv.slice(2);
if (args.includes('--test')) {
  fs.appendFileSync(process.env.SUITE_STAGES, JSON.stringify({
    mode: process.env.CODE_FORGE_NO_LEAK || 'suite',
    guard: process.env.CODE_FORGE_LEAK_GUARD, args,
  }) + '\\n');
}
// Log the complete argv contract, then bound both glob arguments to this fixture.
const result = cp.spawnSync(${JSON.stringify(NODE)}, args.filter(a => a !== 'test/**/*.test.mjs' && a !== '*'), {stdio: 'inherit'});
process.exit(result.status ?? 1);
`, { mode: 0o700 });
  /** @type {NodeJS.ProcessEnv} */
  const env = { ...process.env, HOME: home, TMPDIR: tmp,
    CODE_FORGE_REAL_HOME: home, CODE_FORGE_REAL_TMPDIR: tmp,
    CODE_FORGE_TEST_SUITE: `sandbox-fixture-${path.basename(root)}`,
    CODE_FORGE_LEAK_GUARD: '',
    CODE_FORGE_LEAK_GUARD_DIRS: JSON.stringify(guarded.map(dir => path.join(alias, path.relative(root, dir)))),
    PATH: `${bin}:/usr/bin:/bin`, SUITE_STAGES: stages,
    GUARDED_SAMPLE: JSON.stringify(guarded), SAMPLE_ROOT: root,
  };
  delete env.CODEX_SANDBOX;
  delete env.NODE_TEST_CONTEXT;
  return { root, home, tmp, bin, guarded, sample, stages, env };
}

/** @param {ReturnType<typeof fixture>} f */
function stages(f) {
  return readFileSync(f.stages, 'utf8').trim().split('\n').map(line => JSON.parse(line));
}

test('inherited sandbox mode restores snapshot diagnostics for a new fixture suite or guarded directory', t => {
  const f = fixture(t);
  // Emulate only the inherited mode here, over disposable directories. The
  // kernel-enforcement cases below separately prove that Seatbelt is active.
  /** @type {NodeJS.ProcessEnv} */
  const env = { ...f.env, CODE_FORGE_LEAK_GUARD: 'sandbox' };
  delete env.CODE_FORGE_LEAK_GUARD_SCOPE;
  const script = `
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {mkdirSync} from 'node:fs';
import path from 'node:path';
assert.equal(process.env.CODE_FORGE_LEAK_GUARD, 'sandbox');
for (const change of ['suite', 'directory']) {
  const env = {...process.env};
  if (change === 'suite') env.CODE_FORGE_TEST_SUITE += '-nested';
  else env.CODE_FORGE_REAL_HOME = path.join(process.env.SAMPLE_ROOT, 'nested-home');
  const dir = path.join(env.CODE_FORGE_REAL_HOME, '.code-forge');
  mkdirSync(dir, {recursive: true});
  const leak = path.join(dir, change + '-leak');
  const child = spawnSync(process.execPath, ['--import', './test/helpers/isolate.mjs', '--input-type=module', '-e',
    "import assert from 'node:assert/strict'; import {writeFileSync} from 'node:fs';"
    + "assert.equal(process.env.CODE_FORGE_LEAK_GUARD, 'snapshot');"
    + 'writeFileSync(' + JSON.stringify(leak) + ", 'fixture');"],
    {cwd: ${JSON.stringify(REPO)}, env, encoding: 'utf8', timeout: 20000});
  assert.equal(child.status, 1, child.stdout + child.stderr);
  assert.ok(child.stderr.includes('isolate: 1 new entries under the real code-forge dirs:\\n' + leak + '\\n'),
    child.stdout + child.stderr);
}
`;
  const result = spawnSync(NODE, ['--import', './test/helpers/isolate.mjs', '--input-type=module', '-e', script], {
    cwd: REPO, env, encoding: 'utf8', timeout: 30000,
  });
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test('sandbox wrapper preserves main argv and nested fixture snapshot/final reports', {
  skip: process.platform !== 'darwin' && 'requires macOS sandbox-exec (Seatbelt)',
}, t => {
  const probe = spawnSync('/usr/bin/sandbox-exec', ['-p', '(version 1) (allow default)', '/usr/bin/true']);
  if (probe.error || probe.status !== 0) {
    t.skip(`sandbox-exec unavailable: ${probe.error?.message ?? probe.stderr?.toString().trim()}`);
    return;
  }
  const f = fixture(t);
  writeFileSync(f.sample, `
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {mkdirSync, writeFileSync} from 'node:fs';
import path from 'node:path';
assert.equal(process.env.CODE_FORGE_LEAK_GUARD, 'sandbox');
const env = {...process.env};
delete env.NODE_TEST_CONTEXT;
const run = (file, env, extra = []) => spawnSync(process.execPath,
  ['--import', './test/helpers/isolate.mjs', '--experimental-test-module-mocks', '--test', '--test-reporter=tap', ...extra, file],
  {cwd: ${JSON.stringify(REPO)}, env, encoding: 'utf8', timeout: 30000});
// The unchanged B115 fixture replaces its suite marker and checks directory,
// worker, baseline and marker reports, including invalid/missing baselines.
const reports = run('test/no-leak-final.test.mjs', env);
assert.equal(reports.status, 0, reports.stdout + reports.stderr);
assert.match(reports.stdout, /# pass 1/);
// Changing only fixture directories must also restore snapshots, even while
// retaining the sandboxed suite's marker and inherited guard environment.
env.CODE_FORGE_REAL_HOME = path.join(process.env.SAMPLE_ROOT, 'nested-home');
env.CODE_FORGE_REAL_TMPDIR = path.join(process.env.SAMPLE_ROOT, 'nested-tmp');
const guarded = path.join(env.CODE_FORGE_REAL_HOME, '.code-forge');
mkdirSync(guarded, {recursive: true});
const extra = ['--test-name-pattern=^(snapshot|final) step'];
const snapshot = run('test/no-leak.test.mjs', {...env, CODE_FORGE_NO_LEAK: 'snapshot'}, extra);
assert.equal(snapshot.status, 0, snapshot.stdout + snapshot.stderr);
assert.match(snapshot.stdout, /# pass 1/);
const leak = path.join(guarded, 'nested-leak');
writeFileSync(leak, 'fixture');
const final = run('test/no-leak.test.mjs', {...env, CODE_FORGE_NO_LEAK: 'final'}, extra);
assert.equal(final.status, 1, final.stdout + final.stderr);
assert.ok(final.stdout.includes('suite leaks: directories ' + JSON.stringify([leak])
  + '; leaked pid/run list []; baseline: valid; suite marker: valid; worker report: valid'),
  final.stdout + final.stderr);
process.stdout.write('nested leak reports verified\\n');
`);
  const extra = ['--test-name-pattern=a b', '--test-name-pattern=x*', '*', '--test-reporter=spec'];
  const result = spawnSync('/bin/sh', [WRAPPER, ...extra, f.sample], {
    cwd: REPO, env: f.env, encoding: 'utf8', timeout: 60000,
  });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /nested leak reports verified/);
  assert.deepEqual(result.stderr.match(/^leak guard: .+$/gm), ['leak guard: sandbox']);
  const calls = stages(f);
  assert.deepEqual(calls.map(s => [s.mode, s.guard]),
    [['snapshot', 'sandbox'], ['suite', 'sandbox'], ['final', 'sandbox']]);
  assert.deepEqual(calls.map(s => s.args), [
    ['--import', './test/helpers/isolate.mjs', '--test', 'test/no-leak.test.mjs'],
    ['--import', './test/helpers/isolate.mjs', '--experimental-test-module-mocks', '--test',
      ...extra, f.sample, 'test/**/*.test.mjs'],
    ['--import', './test/helpers/isolate.mjs', '--test', 'test/no-leak.test.mjs'],
  ], 'only main receives extra arguments, followed by the default glob');
});

test('sandbox rejects suite writes with EPERM, permits other writes, and ignores an unrelated writer', {
  skip: process.platform !== 'darwin' && 'requires macOS sandbox-exec (Seatbelt)',
}, async t => {
  const probe = spawnSync('/usr/bin/sandbox-exec', ['-p', '(version 1) (allow default)', '/usr/bin/true']);
  if (probe.error || probe.status !== 0) {
    t.skip(`sandbox-exec unavailable: ${probe.error?.message ?? probe.stderr?.toString().trim()}`);
    return;
  }
  const f = fixture(t);
  writeFileSync(f.sample, `
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {existsSync, mkdirSync, readdirSync, writeFileSync} from 'node:fs';
import path from 'node:path';
assert.equal(process.env.CODE_FORGE_LEAK_GUARD, 'sandbox', 'worker uses kernel guard');
const dirs = JSON.parse(process.env.GUARDED_SAMPLE);
const result = spawnSync(process.execPath, ['-e', \`
  const assert = require('node:assert/strict');
  const fs = require('node:fs');
  const path = require('node:path');
  const dirs = JSON.parse(process.env.GUARDED_SAMPLE);
  assert.throws(() => fs.writeFileSync(path.join(dirs[0], 'suite-write'), ''), {code: 'EPERM'});
  assert.throws(() => fs.mkdirSync(dirs[1]), {code: 'EPERM'});
  fs.writeFileSync(dirs[0] + '-neighbor', 'allowed');
  fs.writeFileSync(path.join(process.env.SAMPLE_ROOT, 'allowed'), 'allowed');
\`], {encoding: 'utf8'});
assert.equal(result.status, 0, result.stderr);
process.stdout.write('sandbox-suite-ready\\n');
const deadline = Date.now() + 10000;
while (!existsSync(path.join(process.env.SAMPLE_ROOT, 'release')) && Date.now() < deadline)
  await new Promise(resolve => setTimeout(resolve, 20));
assert.equal(existsSync(path.join(dirs[0], 'unrelated')), true, 'exactly one outside write arrived');
assert.equal(readdirSync(${JSON.stringify(path.join(f.tmp, 'code-forge-tests'))}).filter(n => n.startsWith('no-leak-')).length,
  0, 'sandbox snapshot step creates zero baseline files');
`);
  const child = spawn('/bin/sh', [WRAPPER, f.sample], { cwd: REPO, env: f.env });
  t.after(() => { if (child.exitCode === null) child.kill('SIGKILL'); });
  let stdout = '';
  let stderr = '';
  let writes = 0;
  child.stderr.on('data', data => { stderr += data; });
  child.stdout.on('data', data => {
    stdout += data;
    if (writes === 0 && stdout.includes('sandbox-suite-ready')) {
      // This parent is outside the sandbox and outside the wrapped suite's process tree.
      writeFileSync(path.join(f.guarded[0], 'unrelated'), 'outside');
      writeFileSync(path.join(f.root, 'release'), '');
      writes++;
    }
  });
  const [code] = await once(child, 'exit');
  assert.equal(code, 0, stdout + stderr);
  assert.equal(writes, 1, 'exactly one unrelated write occurred during the suite');
  assert.equal(readFileSync(path.join(f.root, 'allowed'), 'utf8'), 'allowed');
  assert.equal(readFileSync(`${f.guarded[0]}-neighbor`, 'utf8'), 'allowed');
  assert.equal(existsSync(path.join(f.guarded[0], 'suite-write')), false);
  assert.equal(existsSync(f.guarded[1]), false, 'kernel blocks creating the absent guarded directory');
  assert.deepEqual(stderr.match(/^leak guard: .+$/gm), ['leak guard: sandbox']);
  assert.deepEqual(stages(f).map(s => [s.mode, s.guard]),
    [['snapshot', 'sandbox'], ['suite', 'sandbox'], ['final', 'sandbox']], 'all three stages are sandboxed');
});

test('wrapper selects and reports snapshot for every fallback, preserving explicit test file arguments', t => {
  const cases = [
    ['forced', 'Darwin', 'snapshot', '', 'ok', 'forced'],
    ['non-darwin', 'Linux', '', '', 'ok', 'not macOS'],
    ['codex', 'Darwin', '', 'seatbelt', 'ok', 'CODEX_SANDBOX'],
    ['missing', 'Darwin', '', '', 'missing', 'sandbox-exec missing'],
    ['probe', 'Darwin', '', '', 'fail', 'sandbox-exec probe failed'],
  ];
  for (const [name, platform, guard, codex, sandbox, reason] of cases) {
    const f = fixture(t);
    writeFileSync(path.join(f.bin, 'uname'), `#!/bin/sh\nprintf '%s\\n' '${platform}'\n`, { mode: 0o700 });
    if (sandbox !== 'missing') writeFileSync(path.join(f.bin, 'sandbox-exec'),
      `#!/bin/sh\nexit ${sandbox === 'fail' ? 71 : 99}\n`, { mode: 0o700 });
    writeFileSync(f.sample, `import assert from 'node:assert/strict';
assert.equal(process.env.CODE_FORGE_LEAK_GUARD, 'snapshot');\n`);
    /** @type {NodeJS.ProcessEnv} */
    const env = { ...f.env, PATH: f.bin, CODE_FORGE_LEAK_GUARD: guard };
    if (codex) env.CODEX_SANDBOX = codex;
    const result = spawnSync('/bin/sh', [WRAPPER, f.sample], { cwd: REPO,
      env,
      encoding: 'utf8', timeout: 30000 });
    assert.equal(result.status, 0, `${name}: ${result.stdout}${result.stderr}`);
    assert.deepEqual(result.stderr.match(/^leak guard: .+$/gm), [`leak guard: snapshot (${reason})`],
      `${name}: exactly one diagnostic identifies the fallback reason`);
    const calls = stages(f);
    assert.deepEqual(calls.map(s => [s.mode, s.guard]),
      [['snapshot', 'snapshot'], ['suite', 'snapshot'], ['final', 'snapshot']], `${name}: all three steps retain snapshot checks`);
    assert.deepEqual(calls[1].args.slice(calls[1].args.indexOf('--test') + 1), [f.sample, 'test/**/*.test.mjs'],
      `${name}: the supplied file precedes the default glob`);
  }
});

test('forced snapshot still fails on exactly one escaped file written by the suite', t => {
  for (const escaped of [false, true]) {
    const f = fixture(t);
    const leak = path.join(f.guarded[0], 'escaped-write');
    writeFileSync(f.sample, escaped ? `import {writeFileSync} from 'node:fs';
writeFileSync(${JSON.stringify(leak)}, 'escaped');\n` : '');
    const result = spawnSync('/bin/sh', [WRAPPER, f.sample], { cwd: REPO,
      env: { ...f.env, CODE_FORGE_LEAK_GUARD: 'snapshot' }, encoding: 'utf8', timeout: 30000 });
    const output = result.stdout + result.stderr;
    assert.equal(result.status, escaped ? 1 : 0, output);
    assert.match(result.stderr, /leak guard: snapshot \(forced\)/);
    // Node 22 can omit the preload's exit-time stderr; the final step still
    // reports the leaked directories. Check every report, whichever emits it.
    const reports = [...output.matchAll(/isolate: (\d+) new entries under the real code-forge dirs:\n/g)]
      .map(match => output.slice(match.index + match[0].length).split('\n')
        .slice(0, Number(match[1])).map(line => line.replace(/^# /, '')));
    for (const match of output.matchAll(/suite leaks: directories (\[[^\n]*?\]); leaked pid\/run list/g))
      reports.push(JSON.parse(match[1]));
    if (escaped) assert.ok(reports.length > 0, `the escaped write must produce a leak report: ${output}`);
    for (const reported of reports)
      assert.deepEqual(reported, escaped ? [leak] : [], 'each leak report names exactly the expected paths');
    if (escaped)
      assert.equal(readFileSync(leak, 'utf8'), 'escaped', 'snapshot detects the write after it succeeds');
    else assert.equal(existsSync(leak), false, 'the clean run creates no escaped file');
  }
});
