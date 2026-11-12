// B353: the anthropic default L3 model is claude-fable-5-2 in Node and Go, and a config that still
// names claude-fable-5-1 keeps validating as a known id.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cp, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { defaultsForProvider } from '../../src/config/defaults/index.mjs';
import { validateConfig } from '../../src/config/validate.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const goEnv = { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' };

// B541: copy every module package native/config imports (config/ordered.go imports ledger since
// B455), read from `go list -deps`, so a new import cannot break this fixture again.
function configPackages() {
  const listed = spawnSync('go', ['list', '-deps', '-f', '{{if not .Standard}}{{.Dir}}{{end}}', './config'], {
    cwd: path.join(root, 'native'), encoding: 'utf8', env: goEnv,
  });
  assert.equal(listed.status, 0, `go list -deps ./config: ${listed.stderr}`);
  const packages = listed.stdout.split('\n').filter(Boolean).map((dir) => path.relative(root, dir))
    .filter((relative) => relative.startsWith(`native${path.sep}`) && !relative.startsWith(`native${path.sep}vendor${path.sep}`));
  assert.ok(packages.includes(path.join('native', 'config')), 'go list names native/config itself');
  return packages;
}

/** @param {string} l3 @returns {Record<string, any>} a minimal anthropic config naming `l3` at L3. */
function cfgWithL3(l3) {
  return {
    version: 1,
    provider: 'anthropic',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' },
      L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' },
      L3: { model: l3 },
    },
  };
}

/** @param {ReturnType<typeof validateConfig>} result @returns {string[]} unknown-model-id messages. */
function unknownIdMessages(result) {
  return [...result.errors, ...result.warnings].filter((i) => i.rule === 'unknown-model-id').map((i) => i.message);
}

test('Node: the anthropic default matrix gives L3 model claude-fable-5-2 and leaves L0-L2 unchanged', () => {
  const defaults = defaultsForProvider('anthropic');
  assert.deepEqual(
    Object.entries(defaults.levels).map(([name, level]) => [name, level.model]),
    [['L0', 'claude-haiku-4-5-20251001'], ['L1', 'claude-sonnet-5'], ['L2', 'claude-opus-5-5'], ['L3', 'claude-fable-5-2']],
  );
});

test('Node: a config built from the anthropic defaults validates with 0 errors and 0 unknown-model-id warnings', () => {
  const result = validateConfig({ version: 1, ...structuredClone(defaultsForProvider('anthropic')) });
  assert.deepEqual(result.errors, []);
  assert.deepEqual(unknownIdMessages(result), []);
});

test('Node: claude-fable-5-2 and claude-fable-5-1 at L3 each validate with 0 unknown-model-id warnings', () => {
  for (const id of ['claude-fable-5-2', 'claude-fable-5-1']) {
    const result = validateConfig(cfgWithL3(id));
    assert.equal(result.valid, true, `${id}: config must validate`);
    assert.deepEqual(unknownIdMessages(result), [], `${id}: must be a known anthropic id`);
  }
});

test('Node boundary: claude-fable-5-3 at L3 gives exactly 1 unknown-model-id warning on levels.L3.model', () => {
  assert.deepEqual(unknownIdMessages(validateConfig(cfgWithL3('claude-fable-5-3'))),
    ['levels.L3.model is not a known "anthropic" id, not in known_extra, and not seen-in-cache']);
});

test('Go: DefaultsForProvider("anthropic") gives the same matrix with L3 model claude-fable-5-2', async (t) => {
  const repo = await mkdtemp(path.join(os.tmpdir(), 'b353-default-l3-'));
  t.after(() => rm(repo, { recursive: true, force: true }));
  for (const relative of ['package.json', ...configPackages(), 'native/vendor', 'native/go.mod', 'native/go.sum']) {
    const target = path.join(repo, relative);
    await mkdir(path.dirname(target), { recursive: true });
    await cp(path.join(root, relative), target, { recursive: true });
  }
  // One golden case, read by the Go config tests: the anthropic defaults operation and its exact
  // canonical bytes. The other three group files are empty so only this case exists.
  const golden = path.join(repo, 'test/compat/golden/config');
  await mkdir(golden, { recursive: true });
  const canonical = '{"caps":{"coders":2},"levels":{"L0":{"model":"claude-haiku-4-5-20251001"},'
    + '"L1":{"model":"claude-sonnet-5"},"L2":{"model":"claude-opus-5-5"},"L3":{"model":"claude-fable-5-2"}},'
    + '"provider":"anthropic"}';
  const entry = { name: 'provider-defaults/anthropic', group: 'valid', operation: 'defaults', provider: 'anthropic',
    expected: { ok: true, canonical } };
  await writeFile(path.join(golden, 'operations.json'), `${JSON.stringify([entry])}\n`);
  for (const name of ['errors.json', 'fixtures.json', 'roots.json']) await writeFile(path.join(golden, name), '[]\n');

  const result = spawnSync('go', ['test', '-count=1', '-json', './config', '-run', '^TestConfigValid$/^provider-defaults$/^anthropic$'], {
    cwd: path.join(repo, 'native'), encoding: 'utf8', timeout: 120_000, env: goEnv,
  });
  const outputs = (result.stdout ?? '').split('\n').filter(Boolean).map((line) => JSON.parse(line))
    .filter((e) => e.Action === 'output').map((e) => e.Output).join('');
  assert.equal(result.status, 0, `Go defaults differ from claude-fable-5-2:\n${outputs}\n${result.stderr ?? result.error}`);
  const passed = result.stdout.split('\n').filter(Boolean).map((line) => JSON.parse(line))
    .filter((e) => e.Action === 'pass' && e.Test === 'TestConfigValid/provider-defaults/anthropic');
  assert.equal(passed.length, 1, 'exactly the one anthropic defaults case must run and pass');
});
