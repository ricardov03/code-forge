/**
 * Pid registry + orphan reaper (plan §9.6 rule 3, V12, `[A30]`).
 *
 * `exec` writes one `<pids-dir>/<pid>.json` = `{pid, start_time, argv0}` per child it spawns and
 * removes it when the child is done. A run that is killed never removes its entries, so the next
 * `run start` / `run gc` / test preload calls `sweep(pidsDir)` on the dead run's directory.
 *
 * Safety rule — the reaper can only ever kill what this package started:
 *  - it only looks at pids listed in a registry directory it is handed;
 *  - it kills a pid only when the process's LIVE start time (`ps -o lstart= -p <pid>`) equals the
 *    start time recorded at spawn — a pid the OS has since reused for another program has a
 *    different start time and is left alone;
 *  - a start time it cannot read (recorded as `unknown`, or `ps` fails now) means NEVER kill
 *    (fail closed, plan §0.6 item 11);
 *  - a registered pid whose process is already gone is only un-registered; its process group is
 *    not signalled, because the group's identity can no longer be checked.
 * The kill goes to the process GROUP (every `exec` child is a group leader, `detached: true`):
 * SIGTERM first, then SIGKILL after a grace period, so a child that traps SIGTERM still dies.
 * Every signal is sent right after a synchronous start-time match, with no await in between; the
 * bare pid is signalled only when its group does not exist (ESRCH); EPERM means "not ours to
 * touch" (reported `unknown`, never retried). Once the leader is dead its pid is never signalled
 * again: the other group members captured before the SIGTERM are SIGKILLed one by one, each only
 * after its own start time still matches.
 *
 * `ps` is run with `execFileSync` and an argv array (never a shell) rather than through
 * `./exec.mjs`: `exec` registers every child here, so going through it would recurse.
 */

import { execFileSync } from 'node:child_process';
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

/** Value stored in `start_time` when `ps` could not read it at spawn time. */
export const UNKNOWN_START_TIME = 'unknown';

/** Default grace between SIGTERM and SIGKILL, in milliseconds (same as `exec`). */
const DEFAULT_GRACE_MS = 2000;

/** Poll interval while waiting for a signalled process to die. */
const POLL_MS = 25;

/**
 * @typedef {object} PidEntry
 * @property {number} pid
 * @property {string} start_time - `ps -o lstart=` text, or `UNKNOWN_START_TIME`.
 * @property {string} argv0
 */

/**
 * @typedef {object} SweepResult
 * @property {{pid: number, signal: 'SIGTERM'|'SIGKILL'}[]} killed - matched and killed; `signal`
 *   is the one that finished the process.
 * @property {number[]} mismatched - alive, but the live start time differs: not ours, untouched.
 * @property {number[]} unknown - alive, start time unreadable: never killed, entry kept.
 * @property {number[]} stale - already dead: entry removed, nothing signalled.
 */

/** @param {unknown} pid @returns {pid is number} */
function isValidPid(pid) {
  return Number.isInteger(pid) && /** @type {number} */ (pid) > 1;
}

/**
 * Whether a process with this pid exists. EPERM means it exists but belongs to someone else.
 * @param {number} pid
 * @returns {boolean}
 */
export function isAlive(pid) {
  if (!isValidPid(pid)) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return /** @type {NodeJS.ErrnoException} */ (err).code === 'EPERM';
  }
}

/**
 * The live start time of `pid` as `ps -o lstart=` prints it (C locale, trimmed), or `null` when
 * it cannot be read (no such process, no `ps`, empty output).
 * @param {number} pid
 * @returns {string | null}
 */
export function readStartTime(pid) {
  if (!isValidPid(pid)) return null;
  try {
    const out = execFileSync('ps', ['-o', 'lstart=', '-p', String(pid)], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
      env: { ...process.env, LC_ALL: 'C', LANG: 'C' },
      timeout: 5000,
    });
    const text = out.trim().replace(/\s+/g, ' ');
    return text.length > 0 ? text : null;
  } catch {
    return null;
  }
}

/**
 * Write `<dir>/<pid>.json` for a child just spawned. Returns the entry path.
 * @param {string} dir
 * @param {number} pid
 * @param {string} argv0
 * @returns {string}
 */
export function registerPid(dir, pid, argv0) {
  if (!isValidPid(pid)) throw new TypeError('reaper: pid must be an integer greater than 1');
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  /** @type {PidEntry} */
  const entry = { pid, start_time: readStartTime(pid) ?? UNKNOWN_START_TIME, argv0: String(argv0) };
  const file = path.join(dir, `${pid}.json`);
  writeFileSync(file, `${JSON.stringify(entry)}\n`, { mode: 0o600 });
  return file;
}

/**
 * Remove `<dir>/<pid>.json`; a missing entry is fine.
 * @param {string} dir
 * @param {number} pid
 */
export function unregisterPid(dir, pid) {
  rmSync(path.join(dir, `${pid}.json`), { force: true });
}

/**
 * Read every well-formed entry of a registry directory. Malformed files are skipped (never acted on).
 * @param {string} dir
 * @returns {PidEntry[]}
 */
export function listEntries(dir) {
  /** @type {string[]} */
  let names;
  try {
    names = readdirSync(dir);
  } catch {
    return [];
  }
  /** @type {PidEntry[]} */
  const entries = [];
  for (const name of names.sort()) {
    const match = /^(\d+)\.json$/.exec(name);
    if (!match) continue;
    try {
      const parsed = JSON.parse(readFileSync(path.join(dir, name), 'utf8'));
      if (parsed && isValidPid(parsed.pid) && parsed.pid === Number(match[1]) && typeof parsed.start_time === 'string') {
        entries.push({ pid: parsed.pid, start_time: parsed.start_time, argv0: String(parsed.argv0 ?? '') });
      }
    } catch {
      // unreadable or not JSON: skipped, never killed
    }
  }
  return entries;
}

/**
 * Every OTHER live member of process group `pgid` with its start time, read in one `ps` call:
 * `Map<pid, lstart>`. Empty when `ps` fails.
 * @param {number} pgid
 * @returns {Map<number, string>}
 */
export function groupMembers(pgid) {
  /** @type {Map<number, string>} */
  const members = new Map();
  let out = '';
  try {
    out = execFileSync('ps', ['-A', '-o', 'pid=,pgid=,lstart='], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
      env: { ...process.env, LC_ALL: 'C', LANG: 'C' },
      timeout: 5000,
      maxBuffer: 16 * 1024 * 1024,
    });
  } catch {
    return members;
  }
  for (const line of out.split('\n')) {
    const match = /^\s*(\d+)\s+(\d+)\s+(.+?)\s*$/.exec(line);
    if (!match) continue;
    const pid = Number(match[1]);
    if (Number(match[2]) === pgid && pid !== pgid && pid !== process.pid) {
      members.set(pid, match[3].replace(/\s+/g, ' '));
    }
  }
  return members;
}

/**
 * Send `signal` to `pid`'s group — only after a SYNCHRONOUS start-time match, with no await
 * between the check and the kill. Falls back to the bare pid only when the group does not exist
 * (ESRCH), still inside the same synchronous step.
 * @param {number} pid
 * @param {string} recorded - the start time recorded at spawn.
 * @param {NodeJS.Signals} signal
 * @returns {'sent'|'mismatch'|'eperm'|'gone'}
 */
function signalIfOurs(pid, recorded, signal) {
  if (readStartTime(pid) !== recorded) return 'mismatch';
  try {
    process.kill(-pid, signal);
    return 'sent';
  } catch (err) {
    const code = /** @type {NodeJS.ErrnoException} */ (err).code;
    if (code === 'EPERM') return 'eperm';
    if (code !== 'ESRCH') return 'gone';
  }
  try {
    process.kill(pid, signal);
    return 'sent';
  } catch (err) {
    return /** @type {NodeJS.ErrnoException} */ (err).code === 'EPERM' ? 'eperm' : 'gone';
  }
}

/** @param {number} pid @param {number} withinMs @returns {Promise<boolean>} true once dead */
async function waitForDeath(pid, withinMs) {
  const deadline = Date.now() + withinMs;
  while (Date.now() < deadline) {
    if (!isAlive(pid)) return true;
    await sleep(POLL_MS);
  }
  return !isAlive(pid);
}

/**
 * Reap one registry directory: kill registered children whose live start time still matches,
 * leave everything else alone. See the module comment for the rules.
 * @param {string} dir
 * @param {{graceMs?: number}} [opts]
 * @returns {Promise<SweepResult>}
 */
export async function sweep(dir, opts = {}) {
  const { graceMs = DEFAULT_GRACE_MS } = opts;
  /** @type {SweepResult} */
  const result = { killed: [], mismatched: [], unknown: [], stale: [] };

  for (const entry of listEntries(dir)) {
    const { pid } = entry;
    if (pid === process.pid) continue;

    if (!isAlive(pid)) {
      unregisterPid(dir, pid);
      result.stale.push(pid);
      continue;
    }
    if (entry.start_time === UNKNOWN_START_TIME || readStartTime(pid) === null) {
      result.unknown.push(pid);
      continue;
    }

    // Other members of the leader's group, captured while the leader is alive and verified ours:
    // after the leader dies they are only killed one by one, each after its own start-time match.
    const members = groupMembers(pid);
    const term = signalIfOurs(pid, entry.start_time, 'SIGTERM');
    if (term === 'mismatch') {
      // The pid now names a different process: not ours. Drop the stale entry, touch nothing.
      unregisterPid(dir, pid);
      result.mismatched.push(pid);
      continue;
    }
    if (term === 'eperm') {
      result.unknown.push(pid);
      continue;
    }

    if (await waitForDeath(pid, graceMs)) {
      // The leader died of SIGTERM. Its pid is never signalled again (it may be reused); a
      // member that trapped SIGTERM gets SIGKILL only if its start time still matches.
      for (const [member, started] of members) {
        signalMemberIfOurs(member, started);
      }
      result.killed.push({ pid, signal: 'SIGTERM' });
    } else {
      const kill = signalIfOurs(pid, entry.start_time, 'SIGKILL');
      if (kill !== 'sent') {
        result.unknown.push(pid);
        continue;
      }
      await waitForDeath(pid, graceMs);
      result.killed.push({ pid, signal: 'SIGKILL' });
    }
    unregisterPid(dir, pid);
  }
  return result;
}

/**
 * SIGKILL one former group member, only when its live start time equals the one captured before
 * the leader was signalled (synchronous check, then the kill).
 * @param {number} pid @param {string} started
 */
function signalMemberIfOurs(pid, started) {
  if (readStartTime(pid) !== started) return;
  try {
    process.kill(pid, 'SIGKILL');
  } catch {
    // gone between the check and the kill
  }
}
