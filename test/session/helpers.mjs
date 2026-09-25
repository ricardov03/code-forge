/**
 * Test helpers for `test/session/**`. Import FIRST: at import time `$HOME` and the cwd point at one
 * per-test-file temp parent (under the preload's root), removed in `after()`. Sessions run against
 * the fake CLIs in `test/fixtures/bin/` only — never a real `claude`/`codex`/`grok`, never a key.
 */

import { mkdtempSync, mkdirSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after } from 'node:test';

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const BIN_DIR = path.resolve(HERE, '..', 'fixtures', 'bin');

/** The fake CLIs, keyed by the CLI name the builders put in argv[0]. */
export const BINS = Object.freeze({
  claude: path.join(BIN_DIR, 'fake-claude'),
  codex: path.join(BIN_DIR, 'fake-codex'),
  grok: path.join(BIN_DIR, 'fake-grok'),
});

export const PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b9a-')));
const ORIGINAL_HOME = process.env.HOME;
const ORIGINAL_CWD = process.cwd();
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME);
process.chdir(PARENT);
after(() => {
  process.chdir(ORIGINAL_CWD);
  process.env.HOME = ORIGINAL_HOME;
  rmSync(PARENT, { recursive: true, force: true });
});

let seq = 0;
/** @param {string} name @returns {string} a fresh directory under the per-file parent. */
export function freshDir(name) {
  seq += 1;
  const dir = path.join(PARENT, `${name}-${seq}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

/** @param {string} dir @param {string} name @param {string | Buffer} content @returns {string} */
export function writeIn(dir, name, content) {
  const file = path.join(dir, name);
  writeFileSync(file, content);
  return file;
}

/**
 * A config with every level on `level` (other levels copy it), shaped as `resolveLevel` reads it.
 * @param {{provider: string, model: string, effort?: string, fallback?: Array<{provider: string, model: string, effort?: string}>}} level
 */
export function cfgWith(level) {
  const entry = { ...level };
  return { provider: level.provider, levels: { L0: entry, L1: entry, L2: entry, L3: entry } };
}

/** @param {string} dir @returns {Array<Record<string, any>>} every record the fakes wrote, oldest first (by the fake's start stamp `seq`). */
export function readRecords(dir) {
  return readdirSync(dir)
    .filter((n) => n.endsWith('.json'))
    .map((n) => JSON.parse(readFileSync(path.join(dir, n), 'utf8')))
    .sort((a, b) => (BigInt(a.seq) < BigInt(b.seq) ? -1 : BigInt(a.seq) > BigInt(b.seq) ? 1 : 0));
}

/** A stream stand-in that keeps what was written. */
export function sink() {
  const chunks = /** @type {string[]} */ ([]);
  return { write: (/** @type {string} */ s) => chunks.push(String(s)), text: () => chunks.join('') };
}

/**
 * The deps every session test passes: the fakes as binaries, a stderr sink, and a MINIMAL env —
 * PATH (the fakes' `#!/usr/bin/env node`), HOME, TMPDIR and the FAKE_* knobs; never the caller's
 * full environment, which could hold a real `*_API_KEY`.
 * @param {Record<string, string>} [env] - extra FAKE_* knobs.
 */
export function fakeDeps(env = {}) {
  const records = freshDir('records');
  const stderr = sink();
  /** @type {Record<string, string>} */
  const minimal = { PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', TMPDIR: process.env.TMPDIR ?? '', FAKE_RECORD: records, ...env };
  return { records, stderr, deps: { bins: BINS, stderr, env: minimal } };
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
export async function waitFor(cond, ms = 5000) {
  const until = Date.now() + ms;
  while (Date.now() < until) {
    if (cond()) return true;
    await new Promise((r) => setTimeout(r, 25));
  }
  return cond();
}
