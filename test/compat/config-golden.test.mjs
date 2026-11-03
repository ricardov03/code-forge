import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cp, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));
const goEnv = { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' };

test('Go embedded config schema equals the source schema byte for byte', async () => {
  const [source, embedded] = await Promise.all([
    readFile(path.join(root, 'schema/code-forge.schema.json')),
    readFile(path.join(root, 'native/config/schema.json')),
  ]);
  assert.deepEqual(embedded, source, 'embedded config schema drifted; run node scripts/compat-golden.mjs');
});

async function names(dir) {
  return (await readdir(dir).catch((err) => {
    if (err.code === 'ENOENT') return [];
    throw err;
  })).sort();
}

async function kit(repo = root) {
  const committed = path.join(repo, 'test/compat/golden/config');
  const { version } = JSON.parse(await readFile(path.join(repo, 'package.json'), 'utf8'));
  const dir = await mkdtemp(path.join(os.tmpdir(), 'config-golden-'));
  try {
    const generated = spawnSync(process.execPath, [path.join(repo, 'scripts/compat-golden.mjs'), dir, '--config'], {
      cwd: dir, encoding: 'utf8', env: process.env,
    });
    assert.equal(generated.status, 0, `Node config oracle failed: ${generated.stderr}`);
    const files = await names(path.join(dir, 'config'));
    assert.ok(files.length >= 4, `expected at least four config golden groups, generated ${files.length}`);
    assert.deepEqual(await names(committed), files, 'committed config groups must equal regenerated groups');
    const result = {};
    for (const name of files) {
      const expected = await readFile(path.join(dir, 'config', name), 'utf8');
      const saved = await readFile(path.join(committed, name), 'utf8');
      assert.equal(saved.replaceAll('{{version}}', version), expected.replaceAll('{{version}}', version),
        `${name}: committed config golden bytes drifted`);
      result[name] = JSON.parse(expected);
    }
    return result;
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

function goTest(pattern, expectedCount, repo = root) {
  const result = spawnSync('go', ['test', '-count=1', '-json', './config', '-run', pattern], {
    cwd: path.join(repo, 'native'), encoding: 'utf8', timeout: 120_000, env: goEnv,
  });
  assert.equal(result.status, 0, `Go config assertions failed:\n${result.stdout}\n${result.stderr ?? result.error}`);
  const events = result.stdout.trim().split('\n').map((line) => JSON.parse(line));
  assert.equal(events.filter((e) => e.Action === 'pass' && e.Test?.startsWith(`${pattern}/`)).length,
    expectedCount, `${pattern}: every one of ${expectedCount} golden cases must execute`);
}

async function versionFixture(t) {
  const repo = await mkdtemp(path.join(os.tmpdir(), 'config-version-'));
  t.after(() => rm(repo, { recursive: true, force: true }));
  // Copy only the oracle's tracked inputs and Go config dependencies, never local state or binaries.
  for (const relative of ['package.json', 'scripts/compat-golden.mjs', 'src', 'schema', 'examples',
    'test/fixtures', 'test/compat/golden/config', 'native/config', 'native/canon', 'native/vendor', 'native/go.mod', 'native/go.sum']) {
    const target = path.join(repo, relative);
    await mkdir(path.dirname(target), { recursive: true });
    await cp(path.join(root, relative), target, { recursive: true });
  }
  const dependencies = path.dirname(path.dirname(fileURLToPath(import.meta.resolve('ajv/package.json'))));
  await symlink(dependencies, path.join(repo, 'node_modules'), 'dir');
  const pkgPath = path.join(repo, 'package.json');
  const pkg = JSON.parse(await readFile(pkgPath, 'utf8'));
  pkg.version = '9.8.7';
  await writeFile(pkgPath, `${JSON.stringify(pkg, null, 2)}\n`);
  return repo;
}

test('config goldens survive a package version bump in Node and Go and live messages name that version', async (t) => {
  const repo = await versionFixture(t);
  const data = await kit(repo);
  const versionCases = data['operations.json'].filter((c) => c.expected.message?.includes('{{version}}'));
  assert.deepEqual(versionCases.map((c) => c.name),
    ['validate/unknown-key', 'validate/nested-unknown', 'validate/pattern-properties'],
    'exactly the three unknown-property messages must hold the stable version placeholder');
  const live = spawnSync(process.execPath, ['--input-type=module', '-e', `
    import { validateConfig } from './src/config/validate.mjs';
    const cases = JSON.parse(process.argv[1]);
    console.log(JSON.stringify(cases.map((c) => validateConfig(JSON.parse(c.files['project/.code-forge.yml']),
      { hasCliOnPath: () => true }).errors.filter((issue) => issue.rule === 'schema').map((issue) => issue.message).join('\\n'))));
  `, JSON.stringify(versionCases)], { cwd: repo, encoding: 'utf8', env: process.env });
  assert.equal(live.status, 0, `live Node validation failed: ${live.stderr}`);
  const messages = JSON.parse(live.stdout);
  assert.deepEqual(messages, versionCases.map((c) => c.expected.message.replaceAll('{{version}}', '9.8.7')),
    'all three live diagnostics must match the expanded golden messages exactly');
  assert.equal(messages.filter((message) => message.endsWith('this is code-forge 9.8.7)')).length, 3,
    'all three live diagnostics must actually name the bumped package version');
  goTest('TestConfigErrors', Object.values(data).flat().filter((c) => c.group === 'error').length, repo);
});

test('a changed golden message word still fails the exact Node and Go checks after a version bump', async (t) => {
  const repo = await versionFixture(t);
  const file = path.join(repo, 'test/compat/golden/config/operations.json');
  const data = JSON.parse(await readFile(file, 'utf8'));
  const changed = data.filter((c) => c.name === 'validate/unknown-key');
  assert.equal(changed.length, 1, 'change exactly the root unknown-key golden case');
  changed[0].expected.message = changed[0].expected.message.replace('additional', 'unexpected');
  await writeFile(file, `${JSON.stringify(data, null, 2)}\n`);
  await assert.rejects(kit(repo), (err) => {
    assert.ok(err instanceof assert.AssertionError);
    assert.equal(err.code, 'ERR_ASSERTION');
    assert.equal(err.message.split('\n')[0], 'operations.json: committed config golden bytes drifted');
    return true;
  });
  const result = spawnSync('go', ['test', '-count=1', '-json', './config', '-run', 'TestConfigErrors'], {
    cwd: path.join(repo, 'native'), encoding: 'utf8', timeout: 120_000, env: goEnv,
  });
  assert.equal(result.status, 1, `Go must reject the changed message: ${result.stderr ?? result.error}`);
  const events = result.stdout.trim().split('\n').map((line) => JSON.parse(line));
  assert.deepEqual(events.filter((e) => e.Action === 'fail' && e.Test?.startsWith('TestConfigErrors/')).map((e) => e.Test),
    ['TestConfigErrors/validate/unknown-key'], 'exactly the case with a changed word must fail; version expansion must preserve every other message');
});

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
    ['migrate-error', 'not-found', 'parse-error', 'read-error', 'schema'], 'all four Node loader error kinds and schema refusals must be covered');
  goTest('TestConfigErrors', cases.length);
});

test('Go schema patterns match Ajv or fail loudly for unsupported rules', async () => {
  const data = await kit();
  const cases = Object.values(data).flat().filter((c) => c.group === 'pattern');
  assert.equal(cases.length, 6);
  goTest('TestConfigPatterns', cases.length);
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
