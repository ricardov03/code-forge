import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const REPO = fileURLToPath(new URL('../../', import.meta.url));
const TMP = mkdtempSync(path.join(os.tmpdir(), 'help-go-verbs-'));
mkdirSync(path.join(TMP, 'home'));
mkdirSync(path.join(TMP, 'tmp'));
after(() => rmSync(TMP, { recursive: true, force: true }));
const env = {
  PATH: process.env.PATH, HOME: path.join(TMP, 'home'), TMPDIR: path.join(TMP, 'tmp'),
  CODE_FORGE_NATIVE: '0', CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_HANDOVER_LOG: '0',
  NO_COLOR: '1', LANG: 'C', LC_ALL: 'C', TZ: 'UTC',
};

/** Every `  <verb>` line under `Verbs:` up to the first blank line. */
function helpVerbs() {
  const result = spawnSync(process.execPath, [path.join(REPO, 'bin/code-forge.mjs'), 'help'], {
    cwd: TMP, env, encoding: 'utf8', timeout: 15000,
  });
  assert.equal(result.status, 0, result.stderr);
  const section = result.stdout.split('Verbs:\n')[1].split('\n\n')[0];
  return section.split('\n').map((line) => {
    assert.match(line, /^ {2}[a-z][a-z0-9-]*$/, `verb line format: ${JSON.stringify(line)}`);
    return line.slice(2);
  });
}

const nodeVerbs = readdirSync(path.join(REPO, 'src/cli'))
  .filter((name) => /^[a-z][a-z0-9-]*\.mjs$/.test(name)).map((name) => name.slice(0, -4)).sort();

test('help lists cloud-coder exactly once and every Node verb exactly once, sorted', () => {
  const verbs = helpVerbs();
  assert.equal(verbs.filter((v) => v === 'cloud-coder').length, 1, 'cloud-coder count');
  for (const verb of nodeVerbs) assert.equal(verbs.filter((v) => v === verb).length, 1, `${verb} count`);
  assert.deepEqual(verbs, [...verbs].sort(), 'verbs stay sorted');
  assert.equal(verbs.filter((v) => v === 'zz-not-a-verb').length, 0, 'a name in neither Node nor Go is absent');
});

/** Build the native binary for this host into the test's temp dir; null when Go is absent. */
function buildNative() {
  const realHome = process.env.CODE_FORGE_REAL_HOME ?? os.homedir();
  const cache = path.join(realHome, process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build');
  const binary = path.join(TMP, 'code-forge-native');
  const built = spawnSync('go', ['build', '-o', binary, './cmd/code-forge-native'], {
    cwd: path.join(REPO, 'native'), encoding: 'utf8', timeout: 300_000,
    env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off', GOCACHE: cache },
  });
  if (built.error?.['code'] === 'ENOENT') return null;
  assert.equal(built.status, 0, `go build failed: ${built.stderr}`);
  return binary;
}

/** True when the Go binary serves `verb` itself (prints its usage) instead of handing over. */
function goServes(binary, verb) {
  const result = spawnSync(binary, [verb, '--help'], {
    cwd: TMP, env: { ...env, CODE_FORGE_NATIVE: '', CODE_FORGE_NODE: process.execPath }, encoding: 'utf8', timeout: 15000,
  });
  return `${result.stdout}${result.stderr}`.includes(`usage: code-forge ${verb}`);
}

// B371: cloud-coder and docs are Go routes with thin Node shims (src/cli/<verb>.mjs).
const GO_ROUTED = ['cloud-coder', 'docs', 'ci'];

/** Run the shim through a Node entry with CODE_FORGE_NATIVE unset; returns the spawn result. */
function viaNode(script, args, extraEnv = {}) {
  return spawnSync(process.execPath, [script, ...args], {
    cwd: TMP, env: { ...env, ...extraEnv }, encoding: 'utf8', timeout: 15000,
  });
}

test('without the Go binary each Go-routed shim prints one line naming it, exits 1 and writes nothing', () => {
  for (const verb of GO_ROUTED) {
    const result = viaNode(path.join(REPO, 'bin/code-forge.mjs'), [verb, '--run', 'r-x', '--block', 'B1']);
    assert.equal(result.status, 1, `${verb} exit`);
    assert.equal(result.stdout, '', `${verb} stdout`);
    const lines = result.stderr.split('\n').filter(Boolean);
    assert.equal(lines.length, 1, `${verb} stderr lines: ${result.stderr}`);
    assert.match(lines[0], new RegExp(`^code-forge ${verb}: needs the Go binary native/bin/<platform>-<arch>/code-forge-native `));
    assert.deepEqual(readdirSync(path.join(TMP, 'home'), { recursive: true }), [], `${verb} HOME stays empty`);
  }
});

test('the Go binary serves cloud-coder and docs, and each Node shim hands its call to it; a made-up name is not served', (t) => {
  const binary = buildNative();
  if (binary === null) return t.skip('go is not on PATH');
  for (const verb of GO_ROUTED) assert.equal(goServes(binary, verb), true, `${verb} must be a native route`);
  assert.equal(goServes(binary, 'zz-not-a-verb'), false, 'a name with no Go route is handed over, not served');
  // A package copy with the binary where nativePath() looks for it.
  const pkg = path.join(TMP, 'pkg');
  for (const part of ['bin', 'src', 'package.json']) cpSync(path.join(REPO, part), path.join(pkg, part), { recursive: true });
  const nativeDir = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`);
  mkdirSync(nativeDir, { recursive: true });
  cpSync(binary, path.join(nativeDir, 'code-forge-native'));
  const script = path.join(pkg, 'bin/code-forge.mjs');
  // Only Go validates these arguments, so its own usage error proves the hand-off.
  const cloud = viaNode(script, ['cloud-coder', '--run', 'BAD!', '--block', 'B1'], { CODE_FORGE_NATIVE: '' });
  assert.deepEqual({ status: cloud.status, stdout: cloud.stdout, stderr: cloud.stderr }, {
    status: 2, stdout: '',
    stderr: 'cloud-coder: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\nusage: code-forge cloud-coder --run <r> --block <id>\n',
  });
  const docs = viaNode(script, ['docs', 'nope'], { CODE_FORGE_NATIVE: '' });
  assert.deepEqual({ status: docs.status, stdout: docs.stdout, stderr: docs.stderr },
    { status: 2, stdout: '', stderr: 'usage: code-forge docs sync|path [--json]\n' });
  assert.deepEqual(readdirSync(path.join(TMP, 'home'), { recursive: true }), [], 'HOME stays empty');
});
