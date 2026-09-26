/**
 * `skill/scripts/forge` — the POSIX shim. Inside the package it runs `bin/code-forge.mjs` (the
 * symlinked-install path); copied out of the package it falls back to `code-forge` on PATH; with
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
  await rm(parent, { recursive: true, force: true });
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

test('inside the package the shim runs bin/code-forge.mjs (version prints the package version) with no code-forge on PATH', async () => {
  assert.equal(await resolvesTo('code-forge'), null, 'PATH is private: code-forge must not resolve');
  assert.equal(await resolvesTo('node'), path.join(fakeBin, 'node'));
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const { stdout } = await execFileP(SHIM, ['version'], { env });
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
