/**
 * The empty-cwd temp dir every closed-book session (reviewer, judge, S2, author, facts) runs in
 * (plan §4.3, §5.2). A fresh `os.tmpdir()`-rooted directory holding EXACTLY the packet/brief file
 * a caller writes into it — no project source, no `CLAUDE.md`/`AGENTS.md`, nothing a closed-book
 * session could read besides what it was explicitly handed. `doctor`'s isolation probe (B13, §2.3)
 * asks a closed-book session to echo the first line of the project's `CLAUDE.md` — this module is
 * why that should come back empty: the session's cwd never contained one.
 *
 * **Fix round 1 (isolated per-file review):**
 *  - `fileName` now also refuses `.` and `..` (previously only a `/`/`\` check — `path.join(dir,
 *    '..')` points OUTSIDE the fresh dir; the old code didn't escape, since `writeFile` on a
 *    directory path fails with `EISDIR`, but the caller got a vague filesystem error instead of
 *    the documented `TypeError`).
 *  - A `writeFile` failure (bad content type, disk full, …) after `mkdtemp` no longer leaks an
 *    empty `code-forge-isolated-*` directory: the dir is removed before rethrowing.
 *  - `cleanupIsolatedWorkspace` now also requires `dir`'s PARENT to be the temp root (or the
 *    `baseDir` the caller passes, matching `createIsolatedWorkspace`'s own override) — the old
 *    basename-prefix-only check would recursively delete ANY directory anywhere on disk that
 *    happened to be named `code-forge-isolated-*`, not only ones this module actually created.
 */

import { mkdtemp, readdir, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

/** Directory name prefix for every isolated workspace this module creates. */
const DIR_PREFIX = 'code-forge-isolated-';

/**
 * @param {string} fileName - a plain basename, e.g. `"packet.json"` — never a path (a caller
 *   trying to escape the fresh temp dir is a bug in the caller, not something to silently "fix").
 * @param {string} content
 * @param {{baseDir?: string}} [opts] - `baseDir` overrides `os.tmpdir()` (tests only).
 * @returns {Promise<{dir: string, filePath: string}>}
 * @throws {TypeError} if `fileName` contains a path separator, is empty, or is `.`/`..`.
 */
export async function createIsolatedWorkspace(fileName, content, opts = {}) {
  if (
    typeof fileName !== 'string' ||
    fileName.length === 0 ||
    fileName.includes('/') ||
    fileName.includes('\\') ||
    fileName === '.' ||
    fileName === '..'
  ) {
    throw new TypeError(`createIsolatedWorkspace: fileName must be a plain basename, got ${JSON.stringify(fileName)}`);
  }
  const dir = await mkdtemp(path.join(opts.baseDir ?? os.tmpdir(), DIR_PREFIX));
  const filePath = path.join(dir, fileName);
  try {
    await writeFile(filePath, content, 'utf8');
  } catch (err) {
    await rm(dir, { recursive: true, force: true });
    throw err;
  }
  return { dir, filePath };
}

/**
 * @param {string} dir
 * @returns {Promise<number>} how many entries `dir` currently holds (doctor / tests use this to
 *   prove isolation: a freshly created workspace must list exactly 1).
 */
export async function countWorkspaceEntries(dir) {
  const entries = await readdir(dir);
  return entries.length;
}

/**
 * @param {string} dir - MUST be one this module created: its basename must start with
 *   {@link DIR_PREFIX} AND its parent directory must be `baseDir` (default `os.tmpdir()`).
 * @param {{baseDir?: string}} [opts] - MUST match whatever `baseDir` `createIsolatedWorkspace` was
 *   called with, when it was overridden.
 * @returns {Promise<void>}
 * @throws {TypeError} if `dir` does not satisfy both checks above.
 */
export async function cleanupIsolatedWorkspace(dir, opts = {}) {
  const expectedParent = path.resolve(opts.baseDir ?? os.tmpdir());
  const actualParent = path.dirname(path.resolve(dir));
  if (!path.basename(dir).startsWith(DIR_PREFIX) || actualParent !== expectedParent) {
    throw new TypeError(`cleanupIsolatedWorkspace: refusing to remove a directory this module did not create: ${dir}`);
  }
  await rm(dir, { recursive: true, force: true });
}
