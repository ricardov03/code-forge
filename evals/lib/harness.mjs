/**
 * A minimal, standalone (no `node:test`) harness for the `evals/scenarios/*.mjs` that need a real
 * `code-forge` subprocess plus the live worker it launches (cases 18 and 19; plan §4.11). It is the
 * same shape as `test/worker/helpers.mjs` / `test/integration/review-flow.test.mjs`'s `flow()`
 * helper (worker subprocess, fake `claude` on PATH, argv-array `exec`), reduced to what `evals/`
 * needs and kept free of `node:test` so `evals/run.mjs` also works outside a test run (on tag, or
 * `node evals/run.mjs`). Never a real model call: the only executable ever put on `PATH` as
 * `claude` is one of the fakes under `test/worker/` or `test/fixtures/bin/`.
 */

import { spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, symlinkSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const REPO = path.resolve(HERE, '..', '..');
export const BIN = path.join(REPO, 'bin', 'code-forge.mjs');

const { exec } = await import(path.join(REPO, 'src', 'util', 'exec.mjs'));
const { gitChildEnv } = await import(path.join(REPO, 'src', 'worker', 'ticket.mjs'));
const { liveWorker } = await import(path.join(REPO, 'src', 'worker', 'queue.mjs'));
const { sweep } = await import(path.join(REPO, 'src', 'util', 'reaper.mjs'));

/** One temp parent per harness instance; removed by `cleanup()`. */
export function makeHarness(label) {
  const parent = mkdtempSync(path.join(os.tmpdir(), `cf-eval-${label}-`));
  const home = path.join(parent, 'home');
  mkdirSync(home, { recursive: true });
  let seq = 0;

  /** @param {string} name @returns {string} */
  function freshDir(name) {
    seq += 1;
    const dir = path.join(parent, `${name}-${seq}`);
    mkdirSync(dir, { recursive: true });
    return dir;
  }

  /** @param {string} fakeClaudePath @returns {string} a PATH whose `claude` is the given fake. */
  function fakePath(fakeClaudePath) {
    const bin = freshDir('bin');
    symlinkSync(fakeClaudePath, path.join(bin, 'claude'));
    return [bin, path.dirname(process.execPath), '/usr/bin', '/bin'].join(path.delimiter);
  }

  /**
   * @param {string} fakeClaudePath @param {Record<string, string>} [extra]
   * @returns {NodeJS.ProcessEnv}
   */
  function childEnv(fakeClaudePath, extra = {}) {
    return { PATH: fakePath(fakeClaudePath), HOME: home, TMPDIR: parent, CODE_FORGE_KEY_BACKEND: 'file', ...extra };
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

  /**
   * Run `bin/code-forge.mjs <args>` as a real subprocess (argv array, never a shell).
   * @param {string[]} args @param {string} cwd @param {NodeJS.ProcessEnv} env
   */
  async function cli(args, cwd, env) {
    const res = await exec([process.execPath, BIN, ...args], { cwd, env, timeoutMs: 60000 });
    return { code: res.code, stdout: res.stdout, stderr: res.stderr, json: parseLast(res.stdout) };
  }

  /** @param {string[]} args @param {string} cwd */
  async function git(args, cwd) {
    const res = await exec(['git', '-c', 'user.name=Eval Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', ...args], {
      cwd,
      env: gitChildEnv(),
      timeoutMs: 20000,
    });
    if (res.result !== 'ok') throw new Error(`git ${args.join(' ')} failed: ${res.stderr}`);
    return res;
  }

  /** @param {number} pid @returns {boolean} */
  function alive(pid) {
    try {
      process.kill(pid, 0);
      return true;
    } catch {
      return false;
    }
  }

  /** @param {() => boolean} cond @param {number} ms */
  async function waitFor(cond, ms) {
    const until = Date.now() + ms;
    while (Date.now() < until) {
      if (cond()) return true;
      await new Promise((r) => setTimeout(r, 50));
    }
    return cond();
  }

  /** SIGTERM, wait up to 5s, SIGKILL the group, then reap the run root's pid registry. */
  async function stopPid(pid, runId) {
    try {
      process.kill(pid, 'SIGTERM');
    } catch {
      // already gone
    }
    if (!(await waitFor(() => !alive(pid), 5000))) {
      try {
        process.kill(-pid, 'SIGKILL');
      } catch {
        // already gone
      }
      await waitFor(() => !alive(pid), 2000);
    }
    await sweep(path.join(parent, 'code-forge', runId, 'pids'), { graceMs: 500 });
  }

  /** @param {string} repo @returns {number | null} the pid of the worker `run start` launched, if live. */
  function workerPid(repo) {
    const w = liveWorker(repo);
    return w ? w.pid : null;
  }

  function cleanup() {
    rmSync(parent, { recursive: true, force: true });
  }

  return { parent, home, freshDir, childEnv, cli, git, stopPid, workerPid, waitFor, alive, spawnDetached: spawn, cleanup };
}
