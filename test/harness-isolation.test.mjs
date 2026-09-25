import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdirSync, readdirSync, realpathSync, symlinkSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { isAlive, registerPid } from '../src/util/reaper.mjs';
import { currentRunRoot, pidsDir } from '../src/util/tmp.mjs';

// Plan §9.6 rule 1 / §0.6 item 10 [A28]: the `--import ./test/helpers/isolate.mjs` preload reaches
// this `node --test` worker process and pinned HOME, TMPDIR and the cwd before this file loaded.

const ROOT = process.env.CODE_FORGE_TEST_ROOT;
const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const ISOLATE = path.join(REPO, 'test', 'helpers', 'isolate.mjs');

let PARENT = '';
/** Idle children this file spawned (detached group leaders); killed in after() if still alive. */
const live = new Set();

before(async () => {
  PARENT = realpathSync(await mkdtemp(path.join(os.tmpdir(), 'isolate-')));
});

after(async () => {
  for (const child of live) {
    if (child.exitCode === null && child.signalCode === null && typeof child.pid === 'number') {
      try {
        process.kill(-child.pid, 'SIGKILL');
      } catch {
        child.kill('SIGKILL');
      }
    }
  }
  if (PARENT) await rm(PARENT, { recursive: true, force: true });
});

test('the preload ran in this worker: CODE_FORGE_TEST_ROOT is <real tmp>/code-forge-tests/code-forge-test-<pid>', () => {
  assert.equal(typeof ROOT, 'string', 'CODE_FORGE_TEST_ROOT is unset — run with --import ./test/helpers/isolate.mjs');
  const realTmp = realpathSync(/** @type {string} */ (process.env.CODE_FORGE_REAL_TMPDIR));
  assert.equal(ROOT, path.join(realTmp, 'code-forge-tests', `code-forge-test-${process.pid}`));
});

test('process.env.HOME and os.homedir() are the temp root, not the real home', () => {
  assert.equal(process.env.HOME, ROOT);
  assert.equal(os.homedir(), ROOT);
  assert.notEqual(process.env.HOME, process.env.CODE_FORGE_REAL_HOME);
});

test('process.cwd() and os.tmpdir() are the temp root', () => {
  assert.equal(realpathSync(process.cwd()), ROOT);
  assert.equal(os.tmpdir(), ROOT);
});

test('exec registers its children under the same root (the preload set it as the run root)', () => {
  assert.equal(currentRunRoot(), ROOT);
  assert.equal(pidsDir(), path.join(/** @type {string} */ (ROOT), 'pids'));
});

/** A pid that existed and is now dead. */
async function deadPid() {
  const child = spawn(process.execPath, ['--version'], { stdio: 'ignore' });
  await once(child, 'exit');
  return /** @type {number} */ (child.pid);
}

/**
 * A stale-looking test root under `base`: dead owner, one registered LIVE idle child (its real
 * start time). A sweep of `base` would kill that child and remove the root.
 * @param {string} base
 * @returns {Promise<{root: string, pid: number}>}
 */
async function plantStaleRoot(base) {
  const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], {
    detached: true,
    stdio: 'ignore',
  });
  live.add(child);
  child.once('exit', () => live.delete(child));
  const pid = /** @type {number} */ (child.pid);
  const root = path.join(base, 'code-forge-test-1');
  mkdirSync(root, { recursive: true, mode: 0o700 });
  writeFileSync(path.join(root, 'owner.json'), `${JSON.stringify({ pid: await deadPid(), start_time: 'unknown' })}\n`);
  registerPid(path.join(root, 'pids'), pid, 'node');
  return { root, pid };
}

/**
 * Run the preload in a child node whose "real" temp dir is `fakeTmp` (HOME and cwd are temp too).
 * @param {string} fakeTmp
 * @returns {Promise<{code: number | null, stderr: string}>}
 */
async function runPreload(fakeTmp) {
  const noop = path.join(fakeTmp, 'noop.mjs');
  writeFileSync(noop, '');
  const child = spawn(process.execPath, ['--import', ISOLATE, noop], {
    cwd: fakeTmp,
    env: { ...process.env, CODE_FORGE_REAL_TMPDIR: fakeTmp, HOME: fakeTmp },
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  let stderr = '';
  child.stderr?.setEncoding('utf8').on('data', (chunk) => {
    stderr += chunk;
  });
  const [code] = await once(child, 'exit');
  return { code, stderr };
}

test('a trusted base: the preload sweeps a stale root (dir removed, its registered child dead) and exits 0', { timeout: 15000 }, async () => {
  const fakeTmp = path.join(PARENT, 'trusted');
  const base = path.join(fakeTmp, 'code-forge-tests');
  mkdirSync(base, { recursive: true, mode: 0o700 });
  const { root, pid } = await plantStaleRoot(base);

  const { code, stderr } = await runPreload(fakeTmp);

  assert.equal(code, 0, stderr);
  assert.equal(existsSync(root), false);
  assert.equal(isAlive(pid), false);
  assert.deepEqual(readdirSync(base), [], 'the child removed its own root at exit');
});

test('an untrusted base (a symlink): the preload stops — no sweep, no signal, no root created, exit 1', { timeout: 15000 }, async () => {
  const fakeTmp = path.join(PARENT, 'symlinked');
  const target = path.join(PARENT, 'elsewhere');
  mkdirSync(fakeTmp, { mode: 0o700 });
  mkdirSync(target, { mode: 0o700 });
  symlinkSync(target, path.join(fakeTmp, 'code-forge-tests'));
  const { root, pid } = await plantStaleRoot(target);
  try {
    const { code, stderr } = await runPreload(fakeTmp);

    assert.equal(code, 1);
    assert.match(stderr, /isolate: refusing untrusted test base \(symlink\)/);
    assert.equal(isAlive(pid), true, 'the registered child was not signalled');
    assert.equal(existsSync(path.join(root, 'pids', `${pid}.json`)), true);
    assert.deepEqual(readdirSync(target), ['code-forge-test-1'], 'nothing removed, no root created');
  } finally {
    process.kill(-pid, 'SIGKILL');
  }
});
