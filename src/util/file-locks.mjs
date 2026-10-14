/** Cross-process locks, with FIFO serialization inside this process. */
import { createHash } from 'node:crypto';
import { closeSync, mkdirSync, openSync, readFileSync, readdirSync, unlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { processStartTime } from '../state/run.mjs';
import { keyedLock } from './locks.mjs';

/** @typedef {{probe?: typeof processStartTime, alive?: (pid: number) => boolean, afterRead?: () => Promise<unknown>, pollMs?: number}} LockOpts */
/** @typedef {{ok: true, release: () => void} | {ok: false, result: 'held'}} LockResult */
/** @type {{ok: false, result: 'held'}} */
const HELD = { ok: false, result: 'held' };
const locksDir = () => path.join(os.homedir(), '.code-forge', 'locks');
/** @param {string} key */
const lockPath = (key) => path.join(locksDir(), `${createHash('sha256').update(key).digest('hex')}.lock`);
/** @type {Promise<string | null> | undefined} */
let selfStart;

/** @param {number} pid */
function alive(pid) {
  try { process.kill(pid, 0); return true; }
  catch (err) { return err.code !== 'ESRCH'; }
}

/** @param {Buffer} bytes @returns {{key: string, pid: number, start: string} | null} */
function ownerOf(bytes) {
  try {
    const owner = JSON.parse(bytes.toString('utf8'));
    return owner && typeof owner.key === 'string' && Number.isInteger(owner.pid) && owner.pid > 1 && typeof owner.start === 'string' ? owner : null;
  } catch { return null; }
}

/** Exclusive creation; release synchronously removes only this owner's bytes.
 * @param {string} file @param {Buffer} bytes @returns {LockResult}
 */
function create(file, bytes) {
  let fd;
  try { fd = openSync(file, 'wx', 0o600); }
  catch (err) { if (err.code === 'EEXIST') return HELD; throw err; }
  try { writeFileSync(fd, bytes); }
  finally { closeSync(fd); }
  let released = false;
  return { ok: true, release() {
    if (released) return;
    released = true;
    try { if (readFileSync(file).equals(bytes)) unlinkSync(file); }
    catch { /* missing, unreadable or replaced: leave it alone */ }
  } };
}

/** Take a key or fail closed. A proven dead owner permits one guarded break attempt.
 * @param {string} key @param {LockOpts} [opts] @returns {Promise<LockResult>}
 */
export async function tryLock(key, opts = {}) {
  const probe = opts.probe ?? processStartTime;
  const start = await (selfStart ??= processStartTime(process.pid));
  if (typeof start !== 'string') throw new Error('cannot determine lock owner process start time');
  const file = lockPath(key);
  const bytes = Buffer.from(JSON.stringify({ key, pid: process.pid, start }));
  mkdirSync(locksDir(), { recursive: true, mode: 0o700 });
  const initial = create(file, bytes);
  if (initial.ok) return initial;
  let seen;
  try { seen = readFileSync(file); }
  catch { return HELD; }
  await opts.afterRead?.();
  const owner = ownerOf(seen);
  if (owner === null || owner.key !== key) return HELD;
  let dead = !(opts.alive ?? alive)(owner.pid);
  if (!dead) {
    const actual = await probe(owner.pid);
    dead = actual !== null && actual !== owner.start;
  }
  if (!dead) return HELD;

  // Only unchanged dead-owner bytes may be removed under the per-key break mutex.
  const mutex = `${file}.break`;
  let guard;
  try { guard = openSync(mutex, 'wx', 0o600); }
  catch { return HELD; }
  try {
    try { if (!readFileSync(file).equals(seen)) return HELD; }
    catch { return HELD; }
    unlinkSync(file);
    return create(file, bytes);
  } finally {
    closeSync(guard);
    unlinkSync(mutex);
  }
}

/** @template T @param {string} key @param {() => T | Promise<T>} fn @param {LockOpts} [opts] @returns {Promise<T>} */
export function withLock(key, fn, opts = {}) {
  return keyedLock(key, async () => {
    let lock = await tryLock(key, opts);
    while (!lock.ok) {
      await new Promise((resolve) => setTimeout(resolve, opts.pollMs ?? 25));
      lock = await tryLock(key, opts);
    }
    try { return await fn(); }
    finally { lock.release(); }
  });
}

/** @returns {string[]} paths of unreadable or malformed lock files. */
export function listBadLocks() {
  let names;
  try { names = readdirSync(locksDir()); }
  catch (err) { if (err.code === 'ENOENT') return []; throw err; }
  return names.filter((name) => name.endsWith('.lock')).sort().map((name) => path.join(locksDir(), name)).filter((file) => {
    try { return ownerOf(readFileSync(file)) === null; }
    catch { return true; }
  });
}
