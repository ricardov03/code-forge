/**
 * Helpers for `test/init/**` (block B13a). At import time — before any `src` module loads — one
 * per-test-file parent is made with `mkdtemp` and `HOME` points into it; `after()` removes it.
 * Every wizard run gets its own temp HOME and a temp copy of a fixture project, an env built from
 * scratch (never the caller's: no inherited `CLAUDECODE`, no real PATH, no real key) and the 0600
 * file key backend, so the real keychain, `~` and `~/.claude` are never reached.
 */

import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after } from 'node:test';

export const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-init-'));
process.env.HOME = path.join(PARENT, 'home-default');
process.env.CODE_FORGE_KEY_BACKEND = 'file';
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

/** A fake Jev key (contains FAKE so secret scanners allow it). */
export const FAKE_JEV_KEY = 'sk-FAKE-jev-b13a-0123456789abcdef';

let counter = 0;
/** @param {string} label @returns {string} a new empty directory under PARENT */
export function freshDir(label) {
  counter += 1;
  const dir = path.join(PARENT, `${label}-${counter}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

/** The skill directory the wizard links (a stand-in for the package's `skill/`). */
export const SKILL_SOURCE = freshDir('skill');
writeFileSync(path.join(SKILL_SOURCE, 'SKILL.md'), '---\nname: code-forge\n---\n');

/**
 * @param {(dir: string) => Promise<void>} build - a fixture's `build`
 * @param {{git?: boolean}} [opts] - `git`: add a `.git` dir so the default scope is `project`
 * @returns {Promise<string>}
 */
export async function makeProject(build, { git = true } = {}) {
  const dir = freshDir('project');
  await build(dir);
  if (git) mkdirSync(path.join(dir, '.git'));
  return dir;
}

/** @param {string} home @param {Record<string, string>} [extra] @returns {NodeJS.ProcessEnv} */
export function baseEnv(home, extra = {}) {
  return { HOME: home, PATH: freshDir('emptybin'), CODE_FORGE_KEY_BACKEND: 'file', ...extra };
}

/** @returns {{write: (s: string) => boolean, text: () => string}} */
export function sink() {
  const chunks = [];
  return { write: (s) => (chunks.push(String(s)), true), text: () => chunks.join('') };
}

/** A doctor stand-in: one OK row. */
export const okDoctor = async () => [{ status: 'OK', label: 'config', detail: 'valid' }];

/**
 * @param {string[]} args
 * @param {object} opts
 * @param {string} opts.cwd
 * @param {string} opts.home
 * @param {NodeJS.ProcessEnv} opts.env
 * @param {boolean} [opts.isTTY]
 * @param {any} [opts.ui]
 * @param {any} [opts.doctor]
 * @returns {Promise<{code: number, stdout: string, stderr: string}>}
 */
export async function runWizard(args, { cwd, home, env, isTTY = false, ui, doctor = okDoctor }) {
  const { runInit } = await import('../../src/install/wizard/run.mjs');
  const stdout = sink();
  const stderr = sink();
  const code = await runInit(args, { cwd, home, env, isTTY, ui, doctor, stdout, stderr, skillSource: SKILL_SOURCE });
  return { code, stdout: stdout.text(), stderr: stderr.text() };
}

/**
 * A scripted `@clack/prompts` stand-in: every question takes its default (Enter) unless
 * `replies[message-prefix]` says otherwise. Every call is recorded.
 * @param {Record<string, unknown>} [replies]
 */
export function scriptedUi(replies = {}) {
  /** @type {Array<{kind: string, message: string, options?: any[]}>} */
  const calls = [];
  /** @param {string} message */
  const reply = (message) => Object.entries(replies).find(([prefix]) => message.startsWith(prefix));
  /** @param {string} kind @param {(o: any) => unknown} fallback */
  const q = (kind, fallback) => async (/** @type {any} */ o) => {
    calls.push({ kind, message: o.message, options: o.options });
    const hit = reply(o.message);
    return hit ? hit[1] : fallback(o);
  };
  return {
    calls,
    select: q('select', (o) => o.initialValue),
    multiselect: q('multiselect', (o) => o.initialValues ?? []),
    text: q('text', (o) => o.initialValue ?? ''),
    confirm: q('confirm', (o) => o.initialValue ?? true),
    password: q('password', () => ''),
    isCancel: () => false,
  };
}
