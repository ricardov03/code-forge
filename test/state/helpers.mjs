/**
 * Test helpers for `test/state/**`: every test runs under a temp `$HOME` (never the real
 * `~/.code-forge`) with the two-blocks fixture repo built fresh in a temp workspace.
 */

import { mkdtempSync, realpathSync, rmSync } from 'node:fs';
import { mkdtemp, realpath, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after } from 'node:test';
import { buildTwoBlocksRepo } from '../fixtures/repos/two-blocks/build.mjs';

/**
 * Import-time guard (every test file imports this module FIRST): before any test — and before a
 * mutated path function could run — `$HOME` AND the working directory point at a fresh temp dir,
 * so even code that escapes `withFixture` (a Stryker or hand mutant that resolves the runs dir
 * from `process.cwd()` or a stale `$HOME`) writes there, never into the real `~/.code-forge` or
 * the repository. Both are restored and the dir removed in `after()`.
 */
const GUARD_DIR = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b8-guard-')));
const ORIGINAL_HOME = process.env.HOME;
const ORIGINAL_CWD = process.cwd();
process.env.HOME = GUARD_DIR;
process.chdir(GUARD_DIR);
after(() => {
  process.chdir(ORIGINAL_CWD);
  if (ORIGINAL_HOME === undefined) delete process.env.HOME;
  else process.env.HOME = ORIGINAL_HOME;
  rmSync(GUARD_DIR, { recursive: true, force: true });
});

/**
 * @typedef {{home: string, ws: string, baseSha: string, headSha: string, sideSha: string}} Fixture
 * @param {(fx: Fixture) => Promise<void>} fn
 */
export async function withFixture(fn) {
  // Sequential only: HOME is process-global, so two overlapping fixtures would clobber each other
  // (and could leak into the real ~/.code-forge). Refuse instead of racing.
  if (active) throw new Error('withFixture is sequential-only: another fixture is still active');
  active = true;
  // Canonical paths (macOS /var → /private/var) so stored and expected paths compare equal.
  const home = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-b8-home-')));
  const ws = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-b8-ws-')));
  const original = process.env.HOME;
  process.env.HOME = home;
  try {
    const shas = await buildTwoBlocksRepo(ws);
    await fn({ home, ws, ...shas });
  } finally {
    if (original === undefined) delete process.env.HOME;
    else process.env.HOME = original;
    await rm(home, { recursive: true, force: true });
    await rm(ws, { recursive: true, force: true });
    active = false;
  }
}

let active = false;

/** A ledger writer double that keeps every row it is handed. */
export function rowSink() {
  /** @type {Record<string, any>[]} */
  const rows = [];
  return { rows, writeRow: async (/** @type {Record<string, any>} */ row) => void rows.push(row) };
}

/** A start-time probe double: `pid → start` from a map, null when absent. */
export function fakeProbe(/** @type {Record<number, string>} */ table) {
  return async (/** @type {number} */ pid) => table[pid] ?? null;
}

/** @returns {{write: (s: string) => boolean, text: string}} */
export function captureStream() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

/** @param {string} text @param {string} needle */
export const countOccurrences = (text, needle) => text.split(needle).length - 1;
