/**
 * Review tickets (plan §4.8, V4; block B11): repo-root-relative paths, content hashes and ticket
 * ids. Pure functions plus the one `git rev-parse --show-toplevel` call.
 *
 * Path rule (V4): a ticket's `file` — and every `review.*` row the worker writes — is relative to
 * the REPOSITORY ROOT (realpath'd `git rev-parse --show-toplevel`), whatever directory the coder
 * called `review-file` from. A raw argument that is absolute or has a `..` segment is refused with
 * `bad-path` before anything is written; so is a result outside the root or under `.git/` or
 * `.code-forge/`. A stored value is re-checked by `assertRowPath` (tickets are coder-writable).
 *
 * Ticket id = the first 24 hex chars of `sha256(run \0 block \0 file \0 content_hash)`: the same
 * content enqueued twice is the same ticket (idempotent), an edited file is a new one.
 */

import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { exec } from '../util/exec.mjs';

/** A refusal with a stable machine-readable `code` (`bad-path`, `usage`, `no-repo`). */
export class WorkerError extends Error {
  /** @param {string} code @param {string} message */
  constructor(code, message) {
    super(message);
    this.name = 'WorkerError';
    this.code = code;
  }
}

/** The content hash recorded for a file that no longer exists (a deletion is reviewable). */
export const DELETED_HASH = 'deleted';

const BLOCK_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const TICKET_ID = /^[0-9a-f]{24}$/;

/** @param {unknown} id @returns {string} */
export function assertBlockId(id) {
  if (typeof id !== 'string' || !BLOCK_ID.test(id)) throw new WorkerError('usage', `block id must match ${BLOCK_ID}`);
  return id;
}

/** @param {unknown} id @returns {string} */
export function assertTicketId(id) {
  if (typeof id !== 'string' || !TICKET_ID.test(id)) throw new WorkerError('usage', 'a ticket id is 24 lowercase hex characters');
  return id;
}

/**
 * The env for a child `git`: no inherited repository-selecting `GIT_*` variable (a git hook
 * exports `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, …), no system or global config.
 * @param {NodeJS.ProcessEnv} [env]
 * @returns {NodeJS.ProcessEnv}
 */
export function gitChildEnv(env = process.env) {
  /** @type {NodeJS.ProcessEnv} */
  const out = {};
  for (const [name, value] of Object.entries(env)) {
    if (!name.startsWith('GIT_')) out[name] = value;
  }
  return { ...out, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0' };
}

/**
 * The repository root containing `cwd`, realpath'd.
 * @param {string} cwd
 * @returns {Promise<string>}
 * @throws {WorkerError} `no-repo`
 */
export async function repoRootOf(cwd) {
  const res = await exec(['git', 'rev-parse', '--show-toplevel'], { cwd, env: gitChildEnv(), timeoutMs: 10000 });
  const top = res.stdout.trim();
  if (res.result !== 'ok' || top.length === 0) throw new WorkerError('no-repo', 'not inside a git repository');
  return realpathSync(top);
}

/**
 * Refuse a stored row path that is not a plain repo-root-relative POSIX path (V4).
 * @param {unknown} value
 * @returns {string}
 * @throws {WorkerError} `bad-path`
 */
export function assertRowPath(value) {
  if (typeof value !== 'string' || value.length === 0 || value.length > 4096) throw new WorkerError('bad-path', 'file must be a non-empty relative path');
  if (value.includes('\0') || value.includes('\\')) throw new WorkerError('bad-path', 'file must be a POSIX path');
  if (path.posix.isAbsolute(value)) throw new WorkerError('bad-path', 'file must be relative to the repository root, not absolute');
  const segments = value.split('/');
  if (segments.some((s) => s === '' || s === '.' || s === '..')) throw new WorkerError('bad-path', 'file must not contain "", "." or ".." segments');
  const top = segments[0].toLowerCase(); // case-insensitive file systems (macOS, Windows) alias `.GIT`
  if (top === '.git' || top === '.code-forge') throw new WorkerError('bad-path', `file may not be under ${segments[0]}/`);
  return value;
}

/**
 * Turn a `review-file` argument, given relative to `cwd`, into the repo-root-relative path (V4).
 * The file's directory is realpath'd when it exists, so a symlinked directory cannot point the
 * result outside the root.
 * @param {string} raw @param {string} cwd @param {string} repoRoot - realpath'd.
 * @returns {string}
 * @throws {WorkerError} `bad-path`
 */
export function normalizeRequestPath(raw, cwd, repoRoot) {
  if (typeof raw !== 'string' || raw.length === 0) throw new WorkerError('bad-path', 'a path is required');
  if (path.isAbsolute(raw)) throw new WorkerError('bad-path', 'give the path relative to the current directory, not absolute');
  if (raw.split(/[/\\]/).includes('..')) throw new WorkerError('bad-path', 'the path may not contain a ".." segment');
  const joined = path.resolve(realOrSelf(cwd), raw);
  const real = path.join(realOrSelf(path.dirname(joined)), path.basename(joined));
  const rel = path.relative(repoRoot, real);
  if (!isInside(rel)) throw new WorkerError('bad-path', 'the path is outside the repository');
  return assertInsideRoot(repoRoot, rel.split(path.sep).join('/'));
}

/** @param {string} rel @returns {boolean} whether a `path.relative` result stays inside. */
function isInside(rel) {
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${path.sep}`) && !path.isAbsolute(rel);
}

/**
 * Re-check a repo-root-relative path on disk: the file itself may not be a symlink (it could
 * point anywhere), and its directory, realpath'd, must still be inside the root. A missing file
 * passes (a deletion is reviewable); its existing parent is still checked.
 * @param {string} repoRoot - realpath'd. @param {string} file
 * @returns {string} `file`
 * @throws {WorkerError} `bad-path`
 */
export function assertInsideRoot(repoRoot, file) {
  const full = path.join(repoRoot, assertRowPath(file));
  try {
    if (lstatSync(full).isSymbolicLink()) throw new WorkerError('bad-path', 'the path is a symlink');
  } catch (err) {
    if (err instanceof WorkerError) throw err;
  }
  const rel = path.relative(realOrSelf(repoRoot), path.join(realOrSelf(path.dirname(full)), path.basename(full)));
  if (!isInside(rel)) throw new WorkerError('bad-path', 'the path is outside the repository');
  return file;
}

/** @param {string} p @returns {string} */
function realOrSelf(p) {
  try {
    return realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

/**
 * sha256 of the file's bytes, or `DELETED_HASH` when it does not exist. Re-checks the path on
 * disk first (`assertInsideRoot`: no symlinked file, no directory outside the root).
 * @param {string} repoRoot @param {string} file - repo-root-relative.
 * @returns {string}
 */
export function contentHash(repoRoot, file) {
  let bytes;
  assertInsideRoot(repoRoot, file);
  try {
    bytes = readFileSync(path.join(repoRoot, file));
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT') return DELETED_HASH;
    throw err;
  }
  return createHash('sha256').update(bytes).digest('hex');
}

/**
 * @param {{run: string, block: string, file: string, content_hash: string}} t
 * @returns {string} the ticket id.
 */
export function ticketId({ run, block, file, content_hash }) {
  return createHash('sha256').update([run, block, file, content_hash].join('\0')).digest('hex').slice(0, 24);
}
