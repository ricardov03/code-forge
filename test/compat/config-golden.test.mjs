import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));
const committed = path.join(root, 'test/compat/golden/config');
const goEnv = { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' };

async function names(dir) {
  return (await readdir(dir).catch((err) => {
    if (err.code === 'ENOENT') return [];
    throw err;
  })).sort();
}

async function kit() {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'config-golden-'));
  try {
    const generated = spawnSync(process.execPath, [path.join(root, 'scripts/compat-golden.mjs'), dir, '--config'], {
      cwd: dir, encoding: 'utf8', env: process.env,
    });
    assert.equal(generated.status, 0, `Node config oracle failed: ${generated.stderr}`);
    const files = await names(path.join(dir, 'config'));
    assert.ok(files.length >= 4, `expected at least four config golden groups, generated ${files.length}`);
    assert.deepEqual(await names(committed), files, 'committed config groups must equal regenerated groups');
    const result = {};
    for (const name of files) {
      const expected = await readFile(path.join(dir, 'config', name), 'utf8');
      assert.equal(await readFile(path.join(committed, name), 'utf8'), expected, `${name}: committed config golden bytes drifted`);
      result[name] = JSON.parse(expected);
    }
    return result;
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

function goTest(pattern, expectedCount) {
  const result = spawnSync('go', ['test', '-count=1', '-json', './config', '-run', pattern], {
    cwd: path.join(root, 'native'), encoding: 'utf8', timeout: 120_000, env: goEnv,
  });
  assert.equal(result.status, 0, `Go config assertions failed:\n${result.stdout}\n${result.stderr ?? result.error}`);
  const events = result.stdout.trim().split('\n').map((line) => JSON.parse(line));
  assert.equal(events.filter((e) => e.Action === 'pass' && e.Test?.startsWith(`${pattern}/`)).length,
    expectedCount, `${pattern}: every one of ${expectedCount} golden cases must execute`);
}

test('config goldens are current and Go resolved canonical bytes equal Node for every valid case', async () => {
  const data = await kit();
  const cases = Object.values(data).flat().filter((c) => c.group === 'valid');
  assert.ok(cases.length >= 20, `expected at least 20 valid config cases, got ${cases.length}`);
  goTest('TestConfigValid', cases.length);
});

test('Go refuses every invalid config with the Node error kind and exact message', async () => {
  const data = await kit();
  const cases = Object.values(data).flat().filter((c) => c.group === 'error');
  assert.deepEqual([...new Set(cases.map((c) => c.expected.error))].sort(),
    ['migrate-error', 'not-found', 'parse-error', 'read-error'], 'all four Node loader error kinds must be covered');
  goTest('TestConfigErrors', cases.length);
});

test('pinned vendored YAML resolves offline and all four native release targets build', () => {
  const dependency = spawnSync('go', ['list', '-mod=vendor', '-json', 'gopkg.in/yaml.v3'], {
    cwd: path.join(root, 'native'), encoding: 'utf8', env: goEnv,
  });
  assert.equal(dependency.status, 0, `vendored YAML must resolve with GOPROXY=off: ${dependency.stderr}`);
  const module = JSON.parse(dependency.stdout).Module;
  assert.equal(module.Version, 'v3.0.1', 'YAML dependency must be pinned to v3.0.1');
  const built = spawnSync(process.execPath, [path.join(root, 'scripts/build-native.mjs')], {
    cwd: root, encoding: 'utf8', timeout: 240_000, env: goEnv,
  });
  assert.equal(built.status, 0, `offline release build failed: ${built.stderr}`);
  assert.deepEqual(built.stdout.split('\n').filter((line) => line.startsWith('native ')).map((line) => line.split(':')[0]).sort(),
    ['native darwin-arm64', 'native darwin-x64', 'native linux-arm64', 'native linux-x64'], 'all four targets must build offline');
});

test('project root boundaries and caller symlink spelling match Node in every golden', async () => {
  const data = await kit();
  const cases = Object.values(data).flat().filter((c) => c.group === 'root');
  assert.ok(cases.length >= 10, `expected at least 10 project-root boundary cases, got ${cases.length}`);
  goTest('TestConfigRoots', cases.length);
});
