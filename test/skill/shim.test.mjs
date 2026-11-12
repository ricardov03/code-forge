/**
 * `skill/scripts/forge` — the POSIX shim. Inside the package it runs the `bin/code-forge` sh
 * launcher (Go first, Node router with CODE_FORGE_NATIVE=0 or no Go binary; an old package with
 * only `bin/code-forge.mjs` runs that with node); copied out of the package it falls back to
 * `code-forge` on PATH; with
 * neither it falls back to `npx @codedology/code-forge@<pinned>`; with nothing at all it fails with
 * an install hint. PATH is fully private in every case (`<fakeBin>:/usr/bin:/bin`, `node`
 * symlinked into fakeBin), so a real `code-forge` or `npx` on this machine can never be what
 * answers. All temp state lives under ONE `mkdtemp` parent removed in `after()` (coder rules §8).
 */
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { chmod, cp, mkdir, mkdtemp, readFile, rm, stat, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { promisify } from 'node:util';
import { ROOT, SKILL_DIR } from './helpers.mjs';

const execFileP = promisify(execFile);
const SHIM = path.join(SKILL_DIR, 'scripts', 'forge');
/** Arguments with a space and a glob character: each must arrive as one intact argument. */
const ARGS = ['doctor', 'a b', '*'];

/** @type {string} */
let parent;
/** @type {string} */
let fakeBin;
/** @type {string} */
let copiedShim;
/** @type {NodeJS.ProcessEnv} */
let env;

before(async () => {
  parent = await mkdtemp(path.join(os.tmpdir(), 'code-forge-skill-shim-'));
  fakeBin = path.join(parent, 'bin');
  await mkdir(fakeBin);
  await symlink(process.execPath, path.join(fakeBin, 'node'));
  env = { ...process.env, PATH: `${fakeBin}:/usr/bin:/bin` };
  delete env.CODE_FORGE_BIN;
  // A shim copied out of the package: no `../../bin/code-forge.mjs` above it.
  const copiedDir = path.join(parent, 'installed-skill', 'scripts');
  await mkdir(copiedDir, { recursive: true });
  copiedShim = path.join(copiedDir, 'forge');
  await cp(SHIM, copiedShim);
  await chmod(copiedShim, 0o755);
});

after(async () => {
  if (parent) await rm(parent, { recursive: true, force: true });
});

/**
 * @param {string} name
 * @param {string} prefix - printed before the bracketed arguments.
 */
async function fakeExecutable(name, prefix) {
  const p = path.join(fakeBin, name);
  await writeFile(p, `#!/bin/sh\nprintf '%s' '${prefix}'\nfor a in "$@"; do printf '[%s]' "$a"; done\nprintf '\\n'\n`);
  await chmod(p, 0o755);
}

/**
 * What `command -v <name>` resolves to under the test PATH, or null.
 * @param {string} name @returns {Promise<string | null>}
 */
async function resolvesTo(name) {
  try {
    const { stdout } = await execFileP('/bin/sh', ['-c', 'command -v -- "$1"', 'sh', name], { env });
    return stdout.trim();
  } catch {
    return null;
  }
}

test('the shim is an executable POSIX sh script whose npx pin equals package.json\'s version', async () => {
  const mode = (await stat(SHIM)).mode & 0o111;
  assert.equal(mode, 0o111, 'user, group and other execute bits');
  const text = await readFile(SHIM, 'utf8');
  assert.match(text, /^#!\/bin\/sh\n/);
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const pin = /^PINNED_VERSION="([^"]+)"$/m.exec(text);
  assert.equal(pin?.[1], pkg.version);
  assert.equal(/npx[^\n]*--yes[^\n]*@codedology\/code-forge"?\s/.test(text), false, 'no unpinned npx spec');
});

test('inside the package the shim runs the package CLI (version prints the package version) with no code-forge on PATH', async () => {
  assert.equal(await resolvesTo('code-forge'), null, 'PATH is private: code-forge must not resolve');
  assert.equal(await resolvesTo('node'), path.join(fakeBin, 'node'));
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const { stdout } = await execFileP(SHIM, ['version'], { env: { ...env, CODE_FORGE_NATIVE: '0' } });
  assert.equal(stdout.trim(), `${pkg.name} ${pkg.version}`);
});

test('copied out of the package, the shim falls back to the `code-forge` on PATH with every argument intact', async () => {
  await fakeExecutable('code-forge', 'fake-code-forge');
  assert.equal(await resolvesTo('code-forge'), path.join(fakeBin, 'code-forge'));
  const { stdout } = await execFileP(copiedShim, ARGS, { env });
  assert.equal(stdout, 'fake-code-forge[doctor][a b][*]\n');
});

test('with neither the package nor `code-forge` on PATH, the shim falls back to npx pinned to the package version', async () => {
  await rm(path.join(fakeBin, 'code-forge'), { force: true });
  assert.equal(await resolvesTo('code-forge'), null, 'code-forge must not resolve any more');
  await fakeExecutable('npx', 'fake-npx');
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const { stdout } = await execFileP(copiedShim, ARGS, { env });
  assert.equal(stdout, `fake-npx[--yes][@codedology/code-forge@${pkg.version}][doctor][a b][*]\n`);
});

test('with nothing available the shim exits 127 with an install hint and runs nothing', async () => {
  await rm(path.join(fakeBin, 'npx'), { force: true });
  assert.equal(await resolvesTo('npx'), null, 'npx must not resolve');
  await assert.rejects(execFileP(copiedShim, ARGS, { env }), (/** @type {any} */ err) => {
    assert.equal(err.code, 127);
    assert.equal(err.stdout, '');
    assert.match(err.stderr, /^forge: no code-forge found\. Install it: npm install -g @codedology\/code-forge@/);
    return true;
  });
});

/** The launcher's names for this host (bin/code-forge maps `uname -sm` the same way). */
const HOST_DIR = `${process.platform}-${process.arch}`;

/**
 * A temp package: the shim at skill/scripts/forge, and under bin/ whatever `files` lists:
 * `launcher` (the real bin/code-forge), `router` (a fake bin/code-forge.mjs) and `native` (a fake
 * Go binary at native/bin/<platform>-<arch>/code-forge-native). Each fake prints a marker and its
 * bracketed arguments.
 * @param {string} name @param {{launcher?: boolean, router?: boolean, native?: boolean}} files
 */
async function fakePackage(name, files) {
  const pkg = path.join(parent, name);
  await mkdir(path.join(pkg, 'skill', 'scripts'), { recursive: true });
  await mkdir(path.join(pkg, 'bin'), { recursive: true });
  const shim = path.join(pkg, 'skill', 'scripts', 'forge');
  await cp(SHIM, shim);
  await chmod(shim, 0o755);
  if (files.launcher) {
    await cp(path.join(ROOT, 'bin', 'code-forge'), path.join(pkg, 'bin', 'code-forge'));
    await chmod(path.join(pkg, 'bin', 'code-forge'), 0o755);
  }
  if (files.router) {
    await writeFile(
      path.join(pkg, 'bin', 'code-forge.mjs'),
      "process.stdout.write('fake-node-router' + process.argv.slice(2).map((a) => `[${a}]`).join('') + '\\n');\n",
    );
  }
  if (files.native) {
    const dir = path.join(pkg, 'native', 'bin', HOST_DIR);
    await mkdir(dir, { recursive: true });
    const bin = path.join(dir, 'code-forge-native');
    await writeFile(bin, `#!/bin/sh\nprintf 'fake-native'\nfor a in "$@"; do printf '[%s]' "$a"; done\nprintf '\\n'\n`);
    await chmod(bin, 0o755);
  }
  return { pkg, shim };
}

test('inside the package `forge foo bar` reaches the Go binary through the bin/code-forge sh launcher', async () => {
  const { shim } = await fakePackage('pkg-go', { launcher: true, router: true, native: true });
  const { stdout } = await execFileP(shim, ['foo', 'bar', 'a b', '*'], { env });
  assert.equal(stdout, 'fake-native[foo][bar][a b][*]\n');
});

test('inside the package with CODE_FORGE_NATIVE=0 the launcher runs the Node router', async () => {
  const { shim } = await fakePackage('pkg-node', { launcher: true, router: true, native: true });
  const { stdout } = await execFileP(shim, ['foo', 'bar'], { env: { ...env, CODE_FORGE_NATIVE: '0' } });
  assert.equal(stdout, 'fake-node-router[foo][bar]\n');
});

test('an old install with only bin/code-forge.mjs next to the skill still runs it with node', async () => {
  const { shim } = await fakePackage('pkg-old', { router: true, native: true });
  const { stdout } = await execFileP(shim, ['foo', 'bar'], { env });
  assert.equal(stdout, 'fake-node-router[foo][bar]\n');
});

test('CODE_FORGE_BIN naming bin/code-forge.mjs with a sibling launcher runs the launcher (Go first)', async () => {
  const { pkg } = await fakePackage('pkg-bin-mjs', { launcher: true, router: true, native: true });
  const binEnv = { ...env, CODE_FORGE_BIN: path.join(pkg, 'bin', 'code-forge.mjs') };
  const go = await execFileP(copiedShim, ['foo', 'bar'], { env: binEnv });
  assert.equal(go.stdout, 'fake-native[foo][bar]\n');
  const node = await execFileP(copiedShim, ['foo', 'bar'], { env: { ...binEnv, CODE_FORGE_NATIVE: '0' } });
  assert.equal(node.stdout, 'fake-node-router[foo][bar]\n');
});

test('CODE_FORGE_BIN naming the sh launcher runs it, also as a bare name from its own folder', async () => {
  const { pkg } = await fakePackage('pkg-bin-sh', { launcher: true, router: true, native: true });
  const full = await execFileP(copiedShim, ['foo'], { env: { ...env, CODE_FORGE_BIN: path.join(pkg, 'bin', 'code-forge') } });
  assert.equal(full.stdout, 'fake-native[foo]\n');
  const bare = await execFileP(copiedShim, ['foo'], { env: { ...env, CODE_FORGE_BIN: 'code-forge' }, cwd: path.join(pkg, 'bin') });
  assert.equal(bare.stdout, 'fake-native[foo]\n');
});

test('CODE_FORGE_BIN naming a .mjs with no sibling launcher keeps running it with node (old setups)', async () => {
  const { pkg } = await fakePackage('pkg-bin-old', { router: true, native: true });
  const { stdout } = await execFileP(copiedShim, ['foo', 'bar'], { env: { ...env, CODE_FORGE_BIN: path.join(pkg, 'bin', 'code-forge.mjs') } });
  assert.equal(stdout, 'fake-node-router[foo][bar]\n');
  const other = path.join(parent, 'other-cli.mjs');
  await writeFile(other, "process.stdout.write('fake-other' + process.argv.slice(2).map((a) => `[${a}]`).join('') + '\\n');\n");
  const res = await execFileP(copiedShim, ['foo'], { env: { ...env, CODE_FORGE_BIN: other } });
  assert.equal(res.stdout, 'fake-other[foo]\n');
});
