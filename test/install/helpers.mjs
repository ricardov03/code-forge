/**
 * Test doubles and a fake `$HOME` for `src/install/**` tests. Nothing here touches the real
 * `~/.claude`, `~/.codex`, `~/.grok`, `~/.gemini`, `~/.cursor`, `~/.copilot`, or `~/.code-forge`.
 *
 * Importing this module points `$HOME` at a fresh temp directory for the whole test process
 * BEFORE any `src/install/**` module loads (coder-rules.md rule 10) — even a mutated default
 * parameter (`home = os.homedir()`) in `link.mjs`/`detect.mjs` can then only ever reach a temp
 * directory, never a real one. Every temp directory created through this module is removed after
 * the file's tests.
 */

import { mkdtempSync } from 'node:fs';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after } from 'node:test';

export const REAL_HOME = os.homedir();

process.env.HOME = mkdtempSync(path.join(os.tmpdir(), 'cf-install-home-'));

/** @type {string[]} */
const created = [process.env.HOME];

after(async () => {
  await Promise.all(created.map((dir) => rm(dir, { recursive: true, force: true })));
});

/**
 * @param {string} [prefix]
 * @returns {Promise<string>} a fresh temp directory, removed after the file's tests.
 */
export async function tempDir(prefix = 'cf-install-') {
  const dir = await mkdtemp(path.join(os.tmpdir(), prefix));
  created.push(dir);
  return dir;
}

/**
 * Creates `dir` and writes a `MARKER` file inside it with `content` — a cheap, readable stand-in
 * for "a real skill directory's contents", used to prove a symlink vs. a copy behave differently
 * (a copy's marker never changes after the source's does; a symlink's always does).
 * @param {string} dir
 * @param {string} [content]
 * @returns {Promise<string>} the marker file's path.
 */
export async function writeMarker(dir, content = 'marker') {
  await mkdir(dir, { recursive: true });
  const markerPath = path.join(dir, 'MARKER');
  await writeFile(markerPath, content, 'utf8');
  return markerPath;
}

/** Collects everything written to it, and counts calls — a minimal `{write}` stream double. */
export function sink() {
  return {
    text: '',
    calls: 0,
    /** @param {string} chunk */
    write(chunk) {
      this.text += chunk;
      this.calls += 1;
      return true;
    },
  };
}
