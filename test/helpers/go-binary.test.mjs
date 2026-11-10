import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, describe, test } from 'node:test';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const HELPER = path.join(REPO, 'test/helpers/go-binary.mjs');
const scratch = mkdtempSync(path.join(os.tmpdir(), 'go-binary-test-'));
after(() => rmSync(scratch, { recursive: true, force: true }));

const realGo = spawnSync('/bin/sh', ['-c', 'command -v go'], { encoding: 'utf8' }).stdout.trim();

/** A `go` wrapper that logs each `go build` and runs the real toolchain. */
function goShim(dir, log) {
  mkdirSync(dir, { recursive: true });
  const shim = path.join(dir, 'go');
  writeFileSync(shim, `#!/bin/sh\n[ "$1" = build ] && printf 'build\\n' >> '${log}'\nexec '${realGo}' "$@"\n`);
  chmodSync(shim, 0o755);
}

/** Runs goBinary(options) in a separate Node process; resolves with the returned path. */
function askInChild(env, options) {
  return new Promise((resolve, reject) => {
    const script = `import(${JSON.stringify(HELPER)}).then((m) => process.stdout.write(m.goBinary(${JSON.stringify(options)})))`;
    const child = spawn(process.execPath, ['-e', script], { env, stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '', err = '';
    child.stdout.on('data', (d) => { out += d; });
    child.stderr.on('data', (d) => { err += d; });
    child.on('error', reject);
    child.on('close', (code) => code === 0 ? resolve(out) : reject(new Error(`goBinary child exit ${code}: ${err}`)));
  });
}

function versionOf(binary) {
  const r = spawnSync(binary, ['internal', 'version'], { encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
  return JSON.parse(r.stdout).version;
}

describe('goBinary shared cache', { skip: realGo ? false : 'needs go' }, () => {
  const cache = path.join(scratch, 'cache');
  const log = path.join(scratch, 'builds.log');
  goShim(path.join(scratch, 'shim'), log);
  const env = { ...process.env, CODE_FORGE_GO_BIN_CACHE: cache, PATH: `${path.join(scratch, 'shim')}${path.delimiter}${process.env.PATH}` };
  const builds = () => existsSync(log) ? readFileSync(log, 'utf8').split('\n').filter(Boolean).length : 0;
  const options = { ldflags: '-X main.version=354.0.1', trimpath: true };

  test('two concurrent processes share one build of the same source and args', async () => {
    const [a, b] = await Promise.all([askInChild(env, options), askInChild(env, options)]);
    assert.equal(builds(), 1, 'exactly one go build for two concurrent callers');
    assert.equal(a, b, 'both callers get the same cached path');
    assert.ok(a.startsWith(cache + path.sep), `cached under the cache root: ${a}`);
    assert.equal(versionOf(a), '354.0.1');
    assert.deepEqual(readFileSync(a), readFileSync(b));
  });

  test('changed args give a different cached path', async () => {
    const before = builds();
    const first = await askInChild(env, options);
    const changed = await askInChild(env, { ...options, ldflags: '-X main.version=354.0.2' });
    assert.notEqual(changed, first);
    assert.equal(versionOf(changed), '354.0.2');
    assert.equal(versionOf(first), '354.0.1');
    assert.equal(builds() - before, 1, 'only the changed args build again');
  });

  test('a stale lock left by a dead pid does not block the build', async () => {
    const stale = { ldflags: '-X main.version=354.0.3' };
    const dead = spawnSync(process.execPath, ['-e', '0']).pid;
    // Learn this key's location once, then drop the binary and leave a dead owner's lock.
    const binary = await askInChild(env, stale);
    rmSync(path.dirname(binary), { recursive: true, force: true });
    writeFileSync(path.dirname(binary) + '.lock', String(dead));
    const before = builds();
    const started = Date.now();
    const again = await askInChild(env, stale);
    assert.equal(again, binary);
    assert.equal(versionOf(again), '354.0.3');
    assert.equal(builds() - before, 1, 'the stale lock is taken over and the binary rebuilt once');
    assert.equal(existsSync(path.dirname(binary) + '.lock'), false, 'the lock is released after the build');
    assert.ok(Date.now() - started < 120_000, 'no wait for a dead lock owner');
  });
});

describe('run-suite test concurrency', () => {
  const cores = os.availableParallelism();
  const expected = Math.max(1, Math.floor(cores / 4));

  /** A tiny fixture suite; a `node` wrapper records the runner's argv. */
  function suite(t) {
    const repo = mkdtempSync(path.join(scratch, 'suite-'));
    t.after(() => rmSync(repo, { recursive: true, force: true }));
    mkdirSync(path.join(repo, 'test/helpers'), { recursive: true });
    mkdirSync(path.join(repo, 'bin'));
    writeFileSync(path.join(repo, 'test/helpers/isolate.mjs'), '');
    copyFileSync(path.join(REPO, 'test/helpers/run-suite.sh'), path.join(repo, 'test/helpers/run-suite.sh'));
    copyFileSync(path.join(REPO, 'test/helpers/suite-sandbox.sb'), path.join(repo, 'test/helpers/suite-sandbox.sb'));
    writeFileSync(path.join(repo, 'test/no-leak.test.mjs'), "import { test } from 'node:test'; test('leak stand-in', () => {});\n");
    writeFileSync(path.join(repo, 'test/a.test.mjs'), "import { test } from 'node:test'; test('a', () => {});\n");
    writeFileSync(path.join(repo, 'package.json'), JSON.stringify({ type: 'module' }));
    const argvLog = path.join(repo, 'argv.log');
    writeFileSync(path.join(repo, 'bin/node'), `#!/bin/sh\nfor a; do printf '%s\\n' "$a"; done >> '${argvLog}'\nprintf '%s\\n' '--END--' >> '${argvLog}'\nexec '${process.execPath}' "$@"\n`);
    chmodSync(path.join(repo, 'bin/node'), 0o755);
    const run = (/** @type {string[]} */ args, /** @type {Record<string, string>} */ extra = {}) => {
      /** @type {Record<string, string | undefined>} */
      const env = { ...process.env, CODE_FORGE_LEAK_GUARD: 'snapshot', PATH: `${path.join(repo, 'bin')}${path.delimiter}${process.env.PATH}`, ...extra };
      delete env.NODE_TEST_CONTEXT;
      if (!('CODE_FORGE_TEST_CONCURRENCY' in extra)) delete env.CODE_FORGE_TEST_CONCURRENCY;
      const r = spawnSync('/bin/sh', ['test/helpers/run-suite.sh', ...args], { cwd: repo, env, encoding: 'utf8', timeout: 60_000 });
      assert.equal(r.status, 0, r.stdout + r.stderr);
      // The test runner is the one Node call that loads test-module mocks.
      const calls = readFileSync(argvLog, 'utf8').split('--END--\n').map((c) => c.split('\n').filter(Boolean));
      const runners = calls.filter((c) => c.includes('--experimental-test-module-mocks'));
      assert.equal(runners.length, 1, 'one test runner call');
      const values = [];
      runners[0].forEach((arg, i) => {
        if (arg === '--test-concurrency') values.push(runners[0][i + 1]);
        else if (arg.startsWith('--test-concurrency=')) values.push(arg.slice('--test-concurrency='.length));
      });
      return values;
    };
    return run;
  }

  test(`no flag: the runner gets max(1, cores/4) = ${expected}`, (t) => {
    assert.deepEqual(suite(t)(['test/a.test.mjs']), [String(expected)]);
  });

  test('an explicit flag is kept and no default is added', (t) => {
    assert.deepEqual(suite(t)(['--test-concurrency', '3', 'test/a.test.mjs']), ['3']);
    assert.deepEqual(suite(t)(['--test-concurrency=4', 'test/a.test.mjs']), ['4']);
  });

  test('CODE_FORGE_TEST_CONCURRENCY overrides the default', (t) => {
    assert.deepEqual(suite(t)(['test/a.test.mjs'], { CODE_FORGE_TEST_CONCURRENCY: '5' }), ['5']);
  });
});
