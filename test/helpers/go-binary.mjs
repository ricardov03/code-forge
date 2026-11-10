/**
 * One shared, cached build of the native Go binary for the whole test suite (B354).
 *
 * `goBinary({ ldflags, overlay, trimpath })` returns the path of a
 * `code-forge-native` built from the current `native/` source with exactly those
 * build args. Builds are cached under
 *   <real tmp>/code-forge-test-bin/<sha256 of source tree + go version + args>/code-forge-native
 * (`CODE_FORGE_GO_BIN_CACHE` overrides the cache root, for tests of this helper).
 * The real tmp is `CODE_FORGE_REAL_TMPDIR` when the suite preload pinned one, so
 * every test process shares the cache instead of its private TMPDIR.
 *
 * Concurrent callers in other processes coordinate through `<key>.lock`
 * (created with O_EXCL and holding the builder's pid): one builds, the rest wait
 * and reuse its result. A lock whose pid is dead, or that is older than the
 * longest build, is stale and taken over. Builds run with GOMAXPROCS=2 and
 * `go build -p 2` so a suite never fans out into many all-core Go builds.
 * Cache entries unused for a day are removed opportunistically.
 *
 * `linkGoBinary(dest, options)` places that binary at `dest` (a hard link, so the
 * same inode is reused and macOS does not rescan a fresh copy; falls back to copy).
 */
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  closeSync, copyFileSync, existsSync, linkSync, mkdirSync, openSync, readdirSync, readFileSync,
  renameSync, rmSync, statSync, unlinkSync, utimesSync, writeSync,
} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const NATIVE = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'native');
const DAY_MS = 24 * 60 * 60 * 1000;
const BUILD_TIMEOUT_MS = 10 * 60 * 1000;
const POLL_MS = 100;

/** @param {number} ms */
function sleep(ms) { Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms); }

function cacheRoot() {
  if (process.env.CODE_FORGE_GO_BIN_CACHE) return process.env.CODE_FORGE_GO_BIN_CACHE;
  return path.join(process.env.CODE_FORGE_REAL_TMPDIR ?? os.tmpdir(), 'code-forge-test-bin');
}

/** The build cache Go would use for the real user (test HOMEs are private and empty). */
function goCache() {
  if (process.env.GOCACHE) return process.env.GOCACHE;
  const home = process.env.CODE_FORGE_REAL_HOME ?? os.homedir();
  return path.join(home, process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build');
}

/** @param {import('node:crypto').Hash} hash @param {string} dir @param {string} rel */
function hashTree(hash, dir, rel = '') {
  for (const entry of readdirSync(dir, { withFileTypes: true }).sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))) {
    const relPath = rel ? `${rel}/${entry.name}` : entry.name;
    if (relPath === 'bin') continue; // build outputs, never sources
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) hashTree(hash, full, relPath);
    else if (entry.isFile()) hash.update(`f\0${relPath}\0`).update(readFileSync(full)).update('\0');
  }
}

let goVersion = '';
function readGoVersion() {
  if (goVersion) return goVersion;
  const r = spawnSync('go', ['env', 'GOVERSION'], { cwd: NATIVE, encoding: 'utf8', env: { ...process.env, GOTOOLCHAIN: 'local' } });
  if (r.status !== 0) throw new Error(`go env GOVERSION failed: ${r.error?.message ?? r.stderr}`);
  goVersion = r.stdout.trim();
  return goVersion;
}

/** @param {{ldflags?: string, overlay?: string, trimpath?: boolean}} options */
function cacheKey({ ldflags = '', overlay, trimpath = false }) {
  const hash = createHash('sha256');
  hashTree(hash, NATIVE);
  hash.update(`go\0${readGoVersion()}\0ldflags\0${ldflags}\0trimpath\0${trimpath}\0`);
  if (overlay) {
    // The overlay file lives in each caller's private scratch: key on what it replaces, not where it is.
    const replace = JSON.parse(readFileSync(overlay, 'utf8')).Replace ?? {};
    for (const target of Object.keys(replace).sort()) {
      hash.update(`overlay\0${target}\0`).update(replace[target] ? readFileSync(replace[target]) : '\0deleted').update('\0');
    }
  }
  return hash.digest('hex');
}

/** @param {number} pid */
function alive(pid) {
  try { process.kill(pid, 0); return true; } catch (err) { return /** @type {NodeJS.ErrnoException} */ (err).code === 'EPERM'; }
}

/** True when the lock's owner is gone (dead pid, never wrote its pid, or overran any build). */
function stale(lock) {
  let text, mtime;
  try { text = readFileSync(lock, 'utf8'); mtime = statSync(lock).mtimeMs; } catch { return null; }
  const pid = Number(text);
  const age = Date.now() - mtime;
  if (age > BUILD_TIMEOUT_MS) return text;
  if (!Number.isInteger(pid) || pid <= 0) return age > 10_000 ? text : null;
  return alive(pid) ? null : text;
}

/** @param {string} lock @returns {boolean} true when this process now holds the lock */
function tryLock(lock) {
  let fd;
  try { fd = openSync(lock, 'wx'); } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code !== 'EEXIST') throw err;
    const seen = stale(lock);
    if (seen !== null) {
      // Remove only the stale owner's lock we inspected, never a fresh one created since.
      try { if (readFileSync(lock, 'utf8') === seen) unlinkSync(lock); } catch {}
    }
    return false;
  }
  try { writeSync(fd, String(process.pid)); } finally { closeSync(fd); }
  return true;
}

let swept = false;
/** Remove cache entries (and leftover locks) not used for a day. Best effort. */
function sweep(root, keep) {
  if (swept) return;
  swept = true;
  try {
    for (const name of readdirSync(root)) {
      if (name === keep || name === `${keep}.lock`) continue;
      const full = path.join(root, name);
      try { if (Date.now() - statSync(full).mtimeMs > DAY_MS) rmSync(full, { recursive: true, force: true }); } catch {}
    }
  } catch {}
}

/** Marks a cache entry as used (the sweep keys on mtime). */
function touch(dir) { try { const now = new Date(); utimesSync(dir, now, now); } catch {} }

/**
 * @param {{ldflags?: string, overlay?: string, trimpath?: boolean}} [options]
 * @returns {string} absolute path of the cached binary
 */
export function goBinary(options = {}) {
  const { ldflags = '', overlay, trimpath = false } = options;
  const root = cacheRoot();
  const key = cacheKey({ ldflags, overlay, trimpath });
  const dir = path.join(root, key);
  const binary = path.join(dir, 'code-forge-native');
  const lock = `${dir}.lock`;
  mkdirSync(root, { recursive: true });
  const deadline = Date.now() + BUILD_TIMEOUT_MS;
  while (!existsSync(binary)) {
    if (tryLock(lock)) {
      try {
        if (!existsSync(binary)) build(dir, binary, { ldflags, overlay, trimpath });
      } finally {
        try { if (readFileSync(lock, 'utf8') === String(process.pid)) unlinkSync(lock); } catch {}
      }
      break;
    }
    if (Date.now() > deadline) throw new Error(`goBinary: timed out waiting for ${lock}`);
    sleep(POLL_MS);
  }
  touch(dir);
  sweep(root, key);
  return binary;
}

/**
 * @param {string} dir @param {string} binary
 * @param {{ldflags: string, overlay?: string, trimpath: boolean}} options
 */
function build(dir, binary, { ldflags, overlay, trimpath }) {
  mkdirSync(dir, { recursive: true });
  const tmp = `${binary}.${process.pid}.tmp`;
  const args = ['build', '-p', '2', ...(trimpath ? ['-trimpath'] : []), ...(overlay ? ['-overlay', overlay] : []),
    '-ldflags', ldflags, '-o', tmp, './cmd/code-forge-native'];
  const r = spawnSync('go', args, {
    cwd: NATIVE, encoding: 'utf8', timeout: BUILD_TIMEOUT_MS,
    env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off', GOMAXPROCS: '2', GOCACHE: goCache() },
  });
  if (r.status !== 0) {
    rmSync(tmp, { force: true });
    throw new Error(`goBinary: go ${args.join(' ')} failed: ${r.error?.message ?? r.stderr}`);
  }
  renameSync(tmp, binary); // atomic: readers see no binary or a whole one
}

/**
 * Builds (or reuses) the binary and places it at `dest`, whose directory must exist.
 * @param {string} dest
 * @param {{ldflags?: string, overlay?: string, trimpath?: boolean}} [options]
 * @returns {string} dest
 */
export function linkGoBinary(dest, options = {}) {
  const binary = goBinary(options);
  rmSync(dest, { force: true });
  try { linkSync(binary, dest); } catch { copyFileSync(binary, dest); }
  return dest;
}
