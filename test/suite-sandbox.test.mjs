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
// Bound the OLD wrapper's unconditional glob to this fixture during red runs.
const result = cp.spawnSync(${JSON.stringify(NODE)}, args.filter(a => a !== 'test/**/*.test.mjs'), {stdio: 'inherit'});
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
    assert.deepEqual(calls[1].args.slice(calls[1].args.indexOf('--test') + 1), [f.sample],
      `${name}: exactly the supplied file runs, with no full-suite glob`);
  }
});

test('forced snapshot still fails on exactly one escaped file written by the suite', t => {
  const f = fixture(t);
  const leak = path.join(f.guarded[0], 'escaped-write');
  writeFileSync(f.sample, `import {writeFileSync} from 'node:fs';
writeFileSync(${JSON.stringify(leak)}, 'escaped');\n`);
  const result = spawnSync('/bin/sh', [WRAPPER, f.sample], { cwd: REPO,
    env: { ...f.env, CODE_FORGE_LEAK_GUARD: 'snapshot' }, encoding: 'utf8', timeout: 30000 });
  assert.equal(result.status, 1, result.stdout + result.stderr);
  assert.match(result.stderr, /leak guard: snapshot \(forced\)/);
  assert.ok((result.stdout + result.stderr).includes(`isolate: 1 new entries under the real code-forge dirs:\n${leak}\n`),
    'the unchanged preload reports exactly the escaped file');
  assert.equal(readFileSync(leak, 'utf8'), 'escaped', 'snapshot detects the write after it succeeds');
});
