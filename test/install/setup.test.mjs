/**
 * B463: `code-forge setup` / `scripts/setup.mjs` checks the packaged Go binary for this host.
 * npm runs no install script (owner pick B): customers run `code-forge setup` themselves.
 * Every case runs the script inside a throwaway package directory with a fake binary; nothing
 * touches the real global install or `~/.code-forge`.
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const VERSION = '9.8.7';
const HOST = `${process.platform}-${process.arch}`;
const supported = ['darwin-arm64', 'darwin-x64', 'linux-x64', 'linux-arm64'].includes(HOST);

/** @type {string[]} */
const dirs = [];
after(() => { for (const dir of dirs) rmSync(dir, { recursive: true, force: true }); });

/**
 * @param {{binary?: string | null, mode?: number, sourceCheckout?: boolean}} opts
 * @returns {{pkg: string, binary: string}}
 */
function makePackage({ binary = null, mode = 0o755, sourceCheckout = false } = {}) {
  const pkg = mkdtempSync(path.join(os.tmpdir(), 'cf-setup-'));
  dirs.push(pkg);
  mkdirSync(path.join(pkg, 'scripts'));
  mkdirSync(path.join(pkg, 'bin'));
  copyFileSync(path.join(REPO, 'scripts', 'setup.mjs'), path.join(pkg, 'scripts', 'setup.mjs'));
  copyFileSync(path.join(REPO, 'bin', 'code-forge'), path.join(pkg, 'bin', 'code-forge'));
  chmodSync(path.join(pkg, 'bin', 'code-forge'), 0o755);
  writeFileSync(path.join(pkg, 'package.json'), JSON.stringify({ name: '@codedology/code-forge', version: VERSION }));
  if (sourceCheckout) {
    mkdirSync(path.join(pkg, 'native'), { recursive: true });
    writeFileSync(path.join(pkg, 'native', 'go.mod'), 'module x\n');
  }
  const dir = path.join(pkg, 'native', 'bin', HOST);
  const file = path.join(dir, 'code-forge-native');
  if (binary !== null) {
    mkdirSync(dir, { recursive: true });
    writeFileSync(file, binary);
    chmodSync(file, mode);
  }
  return { pkg, binary: file };
}

/** A fake Go binary: `internal version` prints JSON like the real one. */
const fake = (/** @type {string} */ body) => `#!/bin/sh\nif [ "$1 $2" = "internal version" ]; then\n${body}\nfi\nexit 3\n`;
const okBinary = fake(`echo '{"goVersion":"go1.25.0","version":"${VERSION}"}'; exit 0`);

/**
 * @param {string} pkg
 * @param {string[]} [args]
 */
function runSetup(pkg, args = []) {
  return spawnSync(process.execPath, [path.join(pkg, 'scripts', 'setup.mjs'), ...args], { encoding: 'utf8', cwd: pkg });
}

/** @param {ReturnType<typeof runSetup>} r */
function fixLine(r) {
  const lines = r.stderr.trim().split('\n');
  assert.equal(lines.filter((l) => l.startsWith('fix: ')).length, 1, r.stderr);
  return lines.find((l) => l.startsWith('fix: '));
}

test('ok binary: one OK line on stdout, exit 0', { skip: !supported }, () => {
  const { pkg } = makePackage({ binary: okBinary });
  const r = runSetup(pkg);
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stderr, '');
  assert.match(r.stdout, new RegExp(`^code-forge setup: OK Go binary ${HOST} v${VERSION.replaceAll('.', '\\.')} \\(.+code-forge-native\\)\\n$`));
});

test('missing binary: names the problem, one fix line (reinstall), exit 1', { skip: !supported }, () => {
  const { pkg } = makePackage();
  const r = runSetup(pkg);
  assert.equal(r.status, 1);
  assert.equal(r.stdout, '');
  assert.match(r.stderr, /Go binary is missing/);
  assert.equal(fixLine(r), `fix: npm install -g @codedology/code-forge@${VERSION}`);
});

test('missing binary in a source checkout: fix line builds it', { skip: !supported }, () => {
  const { pkg } = makePackage({ sourceCheckout: true });
  const r = runSetup(pkg);
  assert.equal(r.status, 1);
  assert.equal(fixLine(r), 'fix: node scripts/build-native.mjs --host');
});

test('binary without exec bits: setup restores 0755 and passes', { skip: !supported }, () => {
  const { pkg, binary } = makePackage({ binary: okBinary, mode: 0o644 });
  const r = runSetup(pkg);
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /OK Go binary/);
  assert.equal(statSync(binary).mode & 0o777, 0o755);
});

test('wrong version: names both versions, fix line reinstalls, exit 1', { skip: !supported }, () => {
  const { pkg } = makePackage({ binary: fake(`echo '{"goVersion":"go1.25.0","version":"0.0.1"}'; exit 0`) });
  const r = runSetup(pkg);
  assert.equal(r.status, 1);
  assert.match(r.stderr, /reports v0\.0\.1, package is v9\.8\.7/);
  assert.equal(fixLine(r), `fix: npm install -g @codedology/code-forge@${VERSION}`);
});

test('crashing binary: reports the signal, fix line reinstalls, exit 1', { skip: !supported }, () => {
  const { pkg } = makePackage({ binary: fake('kill -SEGV $$') });
  const r = runSetup(pkg);
  assert.equal(r.status, 1);
  assert.match(r.stderr, /Go binary failed to run \(signal SIGSEGV\)/);
  assert.equal(fixLine(r), `fix: npm install -g @codedology/code-forge@${VERSION}`);
});

test('binary exits non-zero with garbage: reports the exit code, exit 1', { skip: !supported }, () => {
  const { pkg } = makePackage({ binary: fake('echo boom >&2; exit 7') });
  const r = runSetup(pkg);
  assert.equal(r.status, 1);
  assert.match(r.stderr, /Go binary failed to run \(exit 7\): boom/);
  fixLine(r);
});

test('unknown argument: usage on stderr, exit 2', () => {
  const { pkg } = makePackage();
  const r = runSetup(pkg, ['--nope']);
  assert.equal(r.status, 2);
  assert.equal(r.stderr, 'usage: code-forge setup\n');
  const old = runSetup(pkg, ['--postinstall']);
  assert.equal(old.status, 2, 'the removed --postinstall form is a usage error');
  const help = runSetup(pkg, ['--help']);
  assert.equal(help.status, 0);
  assert.equal(help.stdout, 'usage: code-forge setup\n');
});

test('launcher: `code-forge setup` with a broken or missing Go binary names the install fix, no Node (B569)', { skip: !supported }, () => {
  for (const opts of [{ binary: fake('kill -SEGV $$') }, { binary: okBinary }, {}]) {
    const { pkg, binary } = makePackage(opts);
    const real = realpathSync(pkg);
    const r = spawnSync(path.join(pkg, 'bin', 'code-forge'), ['setup'], { encoding: 'utf8', cwd: pkg });
    const state = opts.binary === undefined ? 'is missing' : 'failed to run setup (exit 3)';
    assert.equal(r.status, 69, r.stderr);
    assert.equal(r.stdout, '');
    assert.equal(r.stderr, `code-forge: the Go engine for ${HOST} ${state}\n  expected: ${path.join(real, path.relative(pkg, binary))}\n  fix: sh '${real}/scripts/install.sh'\n  if that fails, reinstall: npm i -g @codedology/code-forge\n`);
  }
});

test('launcher: a Go binary that serves setup keeps its own result (exit 1 here), Node does not re-run', { skip: !supported }, () => {
  const served = `#!/bin/sh\nif [ "$1" = setup ]; then echo 'code-forge setup: served by Go' >&2; echo 'fix: x' >&2; exit 1; fi\nexit 3\n`;
  const { pkg } = makePackage({ binary: served });
  const r = spawnSync(path.join(pkg, 'bin', 'code-forge'), ['setup'], { encoding: 'utf8', cwd: pkg });
  assert.equal(r.status, 1);
  assert.equal(r.stderr, 'code-forge setup: served by Go\nfix: x\n');
});

test('package manifest runs nothing at install time (no preinstall, install or postinstall script)', () => {
  const manifest = JSON.parse(readFileSync(path.join(REPO, 'package.json'), 'utf8'));
  const scripts = manifest.scripts ?? {};
  for (const hook of ['preinstall', 'install', 'postinstall']) {
    assert.equal(Object.hasOwn(scripts, hook), false, `package.json must not declare a "${hook}" script`);
  }
  assert.equal(scripts.setup, 'node scripts/setup.mjs', 'the manual check stays as npm run setup');
});
