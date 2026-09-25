/**
 * Harness detection (plan §2.1 "Detect" column): `<command>` on PATH OR `~/<homeMarker>/`
 * exists. Deliberately never shells out (`which`/`where`) — this module scans `PATH` directory
 * entries itself, which is both faster and, critically, testable under a FAKE `PATH`/`home`
 * without ever touching the real `$PATH` or the real `~/.claude`, `~/.codex`, `~/.grok`,
 * `~/.gemini`, `~/.agents` (coder-rules.md rule 10 — never inspect the real harness dirs in tests
 * or manual runs).
 */

import { access, constants, stat } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { HARNESSES } from './harnesses.mjs';

/** Windows executable suffixes probed when no extension is already present (PATHEXT convention). */
const DEFAULT_WIN32_EXTS = ['.EXE', '.CMD', '.BAT', '.COM'];

/**
 * @param {string} pathEnv - a `PATH`-shaped string (colon- or semicolon-separated).
 * @param {NodeJS.Platform} platform
 * @returns {string[]} non-empty directory entries, in order, duplicates kept (matches shell PATH
 *   resolution: the first match wins, so order must survive).
 */
function splitPathEnv(pathEnv, platform) {
  const sep = platform === 'win32' ? ';' : ':';
  return pathEnv.split(sep).filter((entry) => entry.length > 0);
}

/**
 * Whether `command` resolves to an executable file in `opts.pathEnv`. Never spawns a process.
 * @param {string} command
 * @param {object} [opts]
 * @param {string} [opts.pathEnv] - defaults to `process.env.PATH`; tests pass a fake one.
 * @param {NodeJS.Platform} [opts.platform] - defaults to `process.platform`.
 * @param {string} [opts.pathExt] - Windows only; defaults to `process.env.PATHEXT`.
 * @returns {Promise<boolean>}
 */
export async function commandOnPath(command, opts = {}) {
  if (typeof command !== 'string' || command.length === 0) {
    return false;
  }
  const platform = opts.platform ?? process.platform;
  const pathEnv = opts.pathEnv ?? process.env.PATH ?? '';
  const dirs = splitPathEnv(pathEnv, platform);
  const exts =
    platform === 'win32'
      ? (opts.pathExt ?? process.env.PATHEXT ?? DEFAULT_WIN32_EXTS.join(';')).split(';').filter((e) => e.length > 0)
      : [''];

  for (const dir of dirs) {
    for (const ext of exts) {
      const candidate = path.join(dir, `${command}${ext}`);
      try {
        // Sequential, not Promise.all: PATH entries are searched in order and the first match
        // must win, matching real shell PATH resolution.
        await access(candidate, constants.X_OK);
        return true;
      } catch {
        // not this directory — keep looking.
      }
    }
  }
  return false;
}

/**
 * Whether `<home>/<harness.homeMarker>` exists and is a directory.
 * @param {import('./harnesses.mjs').HarnessRow} harness
 * @param {object} [opts]
 * @param {string} [opts.home] - defaults to `os.homedir()`.
 * @returns {Promise<boolean>}
 */
export async function homeMarkerPresent(harness, opts = {}) {
  const home = opts.home ?? os.homedir();
  try {
    const info = await stat(path.join(home, harness.homeMarker));
    return info.isDirectory();
  } catch {
    return false;
  }
}

/**
 * @typedef {object} DetectionResult
 * @property {string} id
 * @property {boolean} detected - `onPath || homeDirPresent`.
 * @property {boolean} onPath
 * @property {boolean} homeDirPresent
 */

/**
 * @param {import('./harnesses.mjs').HarnessRow} harness
 * @param {object} [opts]
 * @param {string} [opts.home]
 * @param {string} [opts.pathEnv]
 * @param {NodeJS.Platform} [opts.platform]
 * @returns {Promise<DetectionResult>}
 */
export async function detectHarness(harness, opts = {}) {
  const [onPath, homeDirPresent] = await Promise.all([
    harness.command ? commandOnPath(harness.command, opts) : Promise.resolve(false),
    homeMarkerPresent(harness, opts),
  ]);
  return { id: harness.id, detected: onPath || homeDirPresent, onPath, homeDirPresent };
}

/**
 * Detects every row of {@link HARNESSES}, in table order.
 * @param {object} [opts]
 * @param {string} [opts.home]
 * @param {string} [opts.pathEnv]
 * @param {NodeJS.Platform} [opts.platform]
 * @returns {Promise<DetectionResult[]>}
 */
export async function detectAll(opts = {}) {
  const results = [];
  for (const harness of HARNESSES) {
    results.push(await detectHarness(harness, opts));
  }
  return results;
}
