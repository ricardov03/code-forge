/**
 * Test helpers for `test/review/**`. Import FIRST: at import time `$HOME` points at a directory
 * under ONE per-test-file temp parent, removed in `after()`; `src` modules load after that.
 * Sessions run against the shared fake CLIs in `test/fixtures/bin/` only (never a real CLI, never
 * a key), through the same `ctx.spawn` shape the B11 worker builds (config pinned by the worker).
 */

import { mkdtempSync, mkdirSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after } from 'node:test';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const BIN_DIR = path.resolve(HERE, '..', 'fixtures', 'bin');

/** The shared fake CLIs, keyed by the CLI name the builders put in argv[0]. */
export const BINS = Object.freeze({
  claude: path.join(BIN_DIR, 'fake-claude'),
  codex: path.join(BIN_DIR, 'fake-codex'),
  grok: path.join(BIN_DIR, 'fake-grok'),
});

export const PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b12a-')));
const ORIGINAL_HOME = process.env.HOME;
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME);
after(() => {
  process.env.HOME = ORIGINAL_HOME;
  rmSync(PARENT, { recursive: true, force: true });
});

const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const { spawnSession } = await import('../../src/session/spawn.mjs');

let seq = 0;
/** @param {string} name @returns {string} a fresh directory under the per-file parent. */
export function freshDir(name) {
  seq += 1;
  const dir = path.join(PARENT, `${name}-${seq}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

/** @param {string[]} args @param {string} cwd */
export async function git(args, cwd) {
  const res = await exec(['git', '-c', 'user.name=Fake Tester', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', ...args], { cwd, env: gitChildEnv(), timeoutMs: 20000 });
  if (res.result !== 'ok') throw new Error(`git ${args[0]} failed: ${res.stderr}`);
  return res.stdout;
}

/** @returns {Promise<string>} a fresh git repo with one empty commit. */
export async function makeRepo() {
  const repo = freshDir('repo');
  await git(['init', '-q'], repo);
  await git(['commit', '-q', '--allow-empty', '-m', 'base'], repo);
  return repo;
}

/** @param {string} repo */
export async function commitAll(repo) {
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'fixture'], repo);
}

/**
 * @param {number} n @param {(i: number) => string} [line] - 1-based line number ⇒ text.
 * @returns {string} n lines, each ending in a newline.
 */
export function lines(n, line = (i) => `export const v${i} = ${i};`) {
  let out = '';
  for (let i = 1; i <= n; i += 1) out += `${line(i)}\n`;
  return out;
}

/** @param {string} repo @param {string} rel @param {string} content */
export function writeFile(repo, rel, content) {
  mkdirSync(path.dirname(path.join(repo, rel)), { recursive: true });
  writeFileSync(path.join(repo, rel), content);
}

/**
 * A config on the fakes: every level on Anthropic (L3 = the judge model), `min_tokens_out` low
 * enough for the fakes' reported usage unless `review` overrides it.
 * @param {Record<string, any>} [review]
 */
export function cfgFor(review = {}) {
  return {
    provider: 'anthropic',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' },
      L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' },
      L3: { model: 'claude-fable-5-1' },
    },
    review: { min_tokens_out: 40, ...review },
  };
}

/**
 * A valid review answer for `hunks`.
 * @param {string[]} hunks @param {Record<string, any>} [extra]
 */
export function answerFor(hunks, extra = {}) {
  return { passed: true, summary: 'fake review: no defect found', reviewed_hunks: hunks, findings: [], resolved: [], needs_file: [], ...extra };
}

/**
 * The worker's `ctx` with `spawn` and `writeRow` shaped exactly as B11's loop builds them (the
 * worker's `cfg` unless a call passes its own; ledger rows collected in `rows`), the fakes
 * as binaries, and a minimal env (PATH for `#!/usr/bin/env node`, HOME, TMPDIR, FAKE_* knobs).
 * @param {{repoRoot: string, cfg: Record<string, any>, env?: Record<string, string>, timeoutMs?: number, runId?: string}} opts
 */
export function harness({ repoRoot, cfg, env = {}, timeoutMs = 20000, runId = 'r-b12a-test' }) {
  const records = freshDir('records');
  const runRootDir = freshDir('runroot');
  const deps = {
    bins: BINS,
    stderr: { write: () => true },
    env: { PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', TMPDIR: process.env.TMPDIR ?? '', FAKE_RECORD: records, ...env },
  };
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const ctx = {
    repoRoot,
    runId,
    runRootDir,
    cfg,
    jevKey: null,
    spawn: (/** @type {Record<string, any>} */ o) => spawnSession(/** @type {any} */ ({ cfg, ...o, runRoot: runRootDir, timeoutMs }), deps),
    writeRow: async (/** @type {Record<string, any>} */ row) => {
      rows.push(row);
    },
  };
  return { ctx, records, rows, runRootDir, spawn: ctx.spawn, writeRow: ctx.writeRow, workDir: path.join(runRootDir, 'packets', 't') };
}

/** @param {string} dir @returns {Array<Record<string, any>>} the fakes' records, oldest first. */
export function readRecords(dir) {
  return readdirSync(dir)
    .filter((n) => n.endsWith('.json'))
    .map((n) => JSON.parse(readFileSync(path.join(dir, n), 'utf8')))
    .sort((a, b) => (BigInt(a.seq) < BigInt(b.seq) ? -1 : BigInt(a.seq) > BigInt(b.seq) ? 1 : 0));
}

/** @param {Record<string, any>} rec @returns {string} the stdin the fake received. */
export const stdinOf = (rec) => Buffer.from(rec.stdin_b64, 'base64').toString('utf8');

/** @param {string[]} argv @param {string} flag */
export function flag(argv, flag) {
  const i = argv.indexOf(flag);
  return i >= 0 ? argv[i + 1] : undefined;
}
