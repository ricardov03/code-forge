/**
 * Test helpers for `test/worker/**`. Import FIRST: at import time `$HOME` points at a fresh
 * directory under ONE per-test-file temp parent, removed in `after()`. Workers run as real
 * subprocesses (`bin/code-forge.mjs worker`) against a fake `claude` on PATH; every worker and
 * every session it spawned is killed in `stopWorker` (SIGTERM, then SIGKILL of the group, then a
 * sweep of the run root's pid registry).
 */

import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after } from 'node:test';

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const REPO = path.resolve(HERE, '..', '..');
export const BIN = path.join(REPO, 'bin', 'code-forge.mjs');
const FAKE = path.join(HERE, 'fake-reviewer.mjs');

export const PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b11-')));
const ORIGINAL_HOME = process.env.HOME;
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME);
process.env.CODE_FORGE_KEY_BACKEND = 'file';

/** @type {Array<() => Promise<void>>} */
const cleanups = [];
after(async () => {
  for (const fn of cleanups.reverse()) await fn();
  process.env.HOME = ORIGINAL_HOME;
  rmSync(PARENT, { recursive: true, force: true });
});

/** Fake secrets (contain FAKE so scanners allow them). */
export const FAKE_JEV_KEY = 'FAKE-jev-key-b11-5e7a9c3d1f20';
export const FAKE_SNEAKY = 'FAKE-sneaky-token-b11-77aa31';

const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { liveWorker } = await import('../../src/worker/queue.mjs');
const { sweep } = await import('../../src/util/reaper.mjs');

let seq = 0;
/** @param {string} name @returns {string} */
export function freshDir(name) {
  seq += 1;
  const dir = path.join(PARENT, `${name}-${seq}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

const CONFIG = `version: 1
project:
  slug: worker-test
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-1
review:
  session_timeout_s: 60
  min_tokens_out: 40
`;

/**
 * A review answer that passes B12a's stub guard for the fixture files (each a new one-line file,
 * so its only hunk is `@@ -0,0 +1 @@`); the fake reports 42 output tokens (≥ min_tokens_out 40).
 */
export const VALID_REVIEW = JSON.stringify({ passed: true, summary: 'fake review', reviewed_hunks: ['@@ -0,0 +1 @@'], findings: [], resolved: [], needs_file: [] });

/** The block the fixture tickets name; the run record lists it (open, base = the empty commit). */
export const BLOCK = 'B11';

/**
 * A git repo with one empty base commit, `.code-forge.yml`, and the untracked new files
 * `src/a.mjs`, `src/b.mjs`, `src/c.mjs`; unless `start: false`, a started run with no worker
 * pinned and block `B11` open in it (the engine hook refuses a ticket for a block the run record
 * does not list).
 * @param {{start?: boolean}} [opts]
 * @returns {Promise<{repo: string, runId: string}>}
 */
export async function makeRepo({ start = true } = {}) {
  const repo = freshDir('repo');
  for (const args of [
    ['init', '-q'],
    ['-c', 'user.name=Fake Tester', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '--allow-empty', '-m', 'base'],
  ]) {
    const res = await exec(['git', ...args], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
    if (res.result !== 'ok') throw new Error(`git ${args.at(-1)} failed: ${res.stderr}`);
  }
  writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG);
  mkdirSync(path.join(repo, 'src'));
  for (const name of ['a', 'b', 'c']) writeFileSync(path.join(repo, 'src', `${name}.mjs`), `export const ${name} = 1;\n`);
  seq += 1;
  const runId = `r-b11-${process.pid}-${seq}`;
  if (start) {
    const writeRow = async () => {};
    await startRun({ workspace: repo, project: 'worker-test', runId, writeRow });
    await openBlock({ runId, id: BLOCK, level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'fixture files are reviewed', tests: ['worker'] }], writeRow });
  }
  return { repo, runId };
}

/** @returns {string} a PATH whose `claude` is the fake reviewer. */
function fakePath() {
  const bin = freshDir('bin');
  symlinkSync(FAKE, path.join(bin, 'claude'));
  return [bin, path.dirname(process.execPath), '/usr/bin', '/bin'].join(path.delimiter);
}

/**
 * The minimal env for a worker/CLI subprocess: PATH with the fake, the temp HOME/TMPDIR, the file
 * key backend; plus `extra`.
 * @param {Record<string, string>} [extra]
 */
export function childEnv(extra = {}) {
  return { PATH: fakePath(), HOME: /** @type {string} */ (process.env.HOME), TMPDIR: PARENT, CODE_FORGE_KEY_BACKEND: 'file', ...extra };
}

/** @param {string} runId @returns {string} the worker's run root (TMPDIR = PARENT). */
export const runRootOf = (runId) => path.join(PARENT, 'code-forge', runId);

/**
 * Start `code-forge worker --run <id>` detached in `repo` and wait until it announces itself.
 * @param {string} repo @param {string} runId @param {Record<string, string>} [extraEnv]
 */
export async function startWorker(repo, runId, extraEnv = {}) {
  const child = spawn(process.execPath, [BIN, 'worker', '--run', runId, '--poll-ms', '100'], {
    cwd: repo,
    env: childEnv(extraEnv),
    detached: true,
    stdio: ['ignore', 'ignore', 'pipe'],
    shell: false,
  });
  let stderr = '';
  child.stderr?.on('data', (d) => {
    stderr += String(d);
  });
  const exited = new Promise((resolve) => child.on('exit', resolve));
  const w = { child, pid: /** @type {number} */ (child.pid), exited, stderr: () => stderr };
  cleanups.push(() => stopWorker(w, runId));
  const ok = await waitFor(() => liveWorker(repo)?.pid === child.pid, 15000);
  if (!ok) throw new Error(`worker did not announce itself: ${stderr}`);
  return w;
}

/**
 * SIGTERM the worker (it forwards the signal to its live sessions), SIGKILL its group if it is
 * still alive after 5 s, then reap whatever its run root's registry still lists.
 * @param {{pid: number, exited: Promise<unknown>, child: import('node:child_process').ChildProcess}} w @param {string} runId
 */
export async function stopWorker(w, runId) {
  if (w.child.exitCode === null && w.child.signalCode === null) {
    try {
      process.kill(w.pid, 'SIGTERM');
    } catch {
      // gone
    }
    const done = await Promise.race([w.exited.then(() => true), new Promise((r) => setTimeout(() => r(false), 5000))]);
    if (!done) {
      try {
        process.kill(-w.pid, 'SIGKILL');
      } catch {
        // gone
      }
      await w.exited;
    }
  }
  await sweep(path.join(runRootOf(runId), 'pids'), { graceMs: 500 });
}

/**
 * Stop a worker known only by pid (one `run start` launched): SIGTERM, wait up to 5 s, SIGKILL its
 * group, then reap its run root's registry. Registered for `after()` too.
 * @param {number} pid @param {string} runId
 */
export async function stopPid(pid, runId) {
  try {
    process.kill(pid, 'SIGTERM');
  } catch {
    // gone
  }
  if (!(await waitFor(() => !alive(pid), 5000))) {
    try {
      process.kill(-pid, 'SIGKILL');
    } catch {
      // gone
    }
    await waitFor(() => !alive(pid), 2000);
  }
  await sweep(path.join(runRootOf(runId), 'pids'), { graceMs: 500 });
}

/** @param {number} pid @param {string} runId */
export function stopPidAfter(pid, runId) {
  cleanups.push(() => stopPid(pid, runId));
}

/**
 * Run the CLI as a subprocess (argv array, never a shell).
 * @param {string[]} args @param {string} cwd @param {Record<string, string>} [extraEnv]
 */
export async function cli(args, cwd, extraEnv = {}) {
  const started = Date.now();
  const res = await exec([process.execPath, BIN, ...args], { cwd, env: childEnv(extraEnv), timeoutMs: 60000 });
  return { code: res.code, stdout: res.stdout, stderr: res.stderr, ms: Date.now() - started, json: parseLast(res.stdout) };
}

/** @param {string} text @returns {any} the last JSON line, or null. */
function parseLast(text) {
  const line = text.trim().split('\n').at(-1) ?? '';
  try {
    return JSON.parse(line);
  } catch {
    return null;
  }
}

/** @param {string} repo @param {'json' | 'done'} ext @returns {string[]} ticket ids with that file. */
export function queued(repo, ext) {
  const dir = path.join(repo, '.code-forge', 'queue');
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter((n) => new RegExp(`^[0-9a-f]{24}\\.${ext}$`).test(n))
    .map((n) => n.slice(0, 24))
    .sort();
}

/** @param {string} dir @returns {Array<Record<string, any>>} the fake reviewer's records. */
export function records(dir) {
  return readdirSync(dir)
    .filter((n) => n.startsWith('reviewer-'))
    .map((n) => JSON.parse(readFileSync(path.join(dir, n), 'utf8')));
}

/** @param {number} pid */
export function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** @param {() => boolean} cond @param {number} [ms] */
export async function waitFor(cond, ms = 10000) {
  const until = Date.now() + ms;
  while (Date.now() < until) {
    if (cond()) return true;
    await new Promise((r) => setTimeout(r, 50));
  }
  return cond();
}
