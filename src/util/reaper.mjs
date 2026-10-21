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
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
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
    const out = execFileSync(existsSync('/bin/ps') ? '/bin/ps' : 'ps', ['-o', 'lstart=', '-p', String(pid)], {
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
    out = execFileSync(existsSync('/bin/ps') ? '/bin/ps' : 'ps', ['-A', '-o', 'pid=,pgid=,lstart='], {
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

/** @typedef {{uid:number,pid:number,ppid:number,state:string,rssMb:number,start_time:string,command:string}} ProcessInfo */

/** Parse C-locale ps output; malformed rows supply no authority to signal.
 * @param {string} text @returns {ProcessInfo[]}
 */
export function parseProcesses(text) {
  return text.split('\n').flatMap((line) => {
    const m = /^\s*(\d+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(\d+)\s+(\S+\s+\S+\s+\d+\s+\d+:\d+:\d+\s+\d{4})\s+(.+?)\s*$/.exec(line);
    return m ? [{ uid: +m[1], pid: +m[2], ppid: +m[3], state: m[4], rssMb: +m[5] / 1024,
      start_time: m[6].replace(/\s+/g, ' '), command: m[7] }] : [];
  });
}

/** @returns {ProcessInfo[]} */
export function readProcesses() {
  try {
    return parseProcesses(execFileSync(existsSync('/bin/ps') ? '/bin/ps' : 'ps',
      ['-A', '-o', 'uid=,pid=,ppid=,stat=,rss=,lstart=,command='], {
        encoding: 'utf8', env: { ...process.env, LC_ALL: 'C', LANG: 'C' },
        timeout: 5000, maxBuffer: 16 * 1024 * 1024, stdio: ['ignore', 'pipe', 'ignore'],
      }));
  } catch { return []; }
}

/** Current process, ancestors, zombies and their parents are never signal targets.
 * @param {ProcessInfo[]} rows @returns {Set<number>}
 */
function protectedPids(rows) {
  const blocked = new Set([1, process.pid, process.ppid]);
  const byPid = new Map(rows.map((p) => [p.pid, p]));
  let pid = process.ppid;
  const visited = new Set();
  while (pid > 1 && !visited.has(pid)) {
    visited.add(pid);
    blocked.add(pid);
    pid = byPid.get(pid)?.ppid ?? 1;
  }
  for (const p of rows) if (p.state.startsWith('Z')) { blocked.add(p.pid); blocked.add(p.ppid); }
  return blocked;
}

/** One synchronous identity/uid/state check immediately followed by a bare-pid signal.
 * @param {number} pid @param {string} started @param {NodeJS.Signals} signal
 * @returns {boolean}
 */
function signalMember(pid, started, signal) {
  if (!started || started === UNKNOWN_START_TIME || !process.getuid) return false;
  const rows = readProcesses();
  const p = rows.find((r) => r.pid === pid);
  if (!p || p.uid !== process.getuid() || protectedPids(rows).has(pid) || p.start_time !== started) return false;
  try { process.kill(pid, signal); return true; } catch { return false; }
}

/** Stop a captured set, checking every member separately at both signals.
 * @param {Map<number,string>} members @param {number} [graceMs]
 * @returns {Promise<Map<number, 'SIGTERM'|'SIGKILL'>>}
 */
export async function stopMembers(members, graceMs = DEFAULT_GRACE_MS) {
  const sent = new Map();
  for (const [pid, started] of members) if (signalMember(pid, started, 'SIGTERM')) sent.set(pid, 'SIGTERM');
  const deadline = Date.now() + graceMs;
  while (sent.size && Date.now() < deadline) {
    const live = readProcesses();
    if (![...sent.keys()].some((pid) => live.some((p) => p.pid === pid && !p.state.startsWith('Z') && p.start_time === members.get(pid)))) break;
    await sleep(POLL_MS);
  }
  for (const pid of sent.keys()) if (signalMember(pid, members.get(pid), 'SIGKILL')) sent.set(pid, 'SIGKILL');
  // Give the OS and Node's child watcher a chance to observe the final signal before resolving.
  if (sent.size) await sleep(POLL_MS);
  return sent;
}

/** Read only ownership markers, never retain/print the rest of a child's environment.
 * @param {ProcessInfo} p @returns {Record<string,string>|null}
 */
export function readProcessEnv(p) {
  try {
    let text;
    if (process.platform === 'linux') text = readFileSync(`/proc/${p.pid}/environ`, 'utf8');
    else if (process.platform === 'darwin') {
      const out = execFileSync('/bin/ps', ['-E', '-o', 'pid=,ppid=,lstart=,command=', '-p', String(p.pid)], {
        encoding: 'utf8', timeout: 5000, maxBuffer: 8 * 1024 * 1024,
        env: { ...process.env, LC_ALL: 'C', LANG: 'C' }, stdio: ['ignore', 'pipe', 'ignore'],
      });
      const m = /^\s*(\d+)\s+(\d+)\s+(\S+\s+\S+\s+\d+\s+\d+:\d+:\d+\s+\d{4})\s+/.exec(out);
      if (!m || +m[1] !== p.pid || +m[2] !== p.ppid || m[3].replace(/\s+/g, ' ') !== p.start_time) return null;
      text = out.slice(m[0].length);
    } else return null;
    /** @type {Record<string,string>} */
    const markers = {};
    for (const name of ['CODE_FORGE_OWNER_PID', 'CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_START_TIME']) {
      const match = process.platform === 'linux'
        ? text.split('\0').find((entry) => entry.startsWith(`${name}=`))?.slice(name.length + 1)
        : new RegExp(`(?:^|\\s)${name}=(.*?)(?=\\s+[A-Za-z_][A-Za-z0-9_]*=|$)`).exec(text)?.[1];
      if (match !== undefined) markers[name] = match;
    }
    return markers;
  } catch { return null; }
}

/**
 * Inspect and optionally stop proven code-forge processes. Defaults are read-only.
 * `psText`/readers are test seams; production always probes the OS and existing record reader.
 * @param {{apply?:boolean, root?:string, runId?:string, orphansOnly?:boolean, zombies?:boolean,
 * graceMs?:number, psText?:string, uid?:number,
 * readEnv?:(p:ProcessInfo)=>Record<string,string>|null,
 * readRun?:(id:string)=>Promise<Record<string,any>>}} [opts]
 */
export async function sweepProcesses(opts = {}) {
  const rows = opts.psText === undefined ? readProcesses() : parseProcesses(opts.psText);
  const uid = opts.uid ?? process.getuid?.();
  const own = rows.filter((p) => p.uid === uid);
  const blocked = protectedPids(rows);
  const envFor = opts.readEnv ?? readProcessEnv;
  const loadRun = opts.readRun ?? (await import('../state/run.mjs')).readRun;
  const tmp = await import('./tmp.mjs');
  /** @type {(ProcessInfo & {kind:'worker'|'orphan'|'registry',runId?:string,action?:string})[]} */
  const items = [];
  /** @type {{ppid:number,command:string,count:number,pids:number[]}[]} */
  const zombies = [];
  if (opts.zombies !== false && !opts.orphansOnly) {
    for (const p of own.filter((p) => p.state.startsWith('Z'))) {
      let group = zombies.find((g) => g.ppid === p.ppid);
      if (!group) { group = { ppid: p.ppid, command: rows.find((r) => r.pid === p.ppid)?.command ?? '(unreadable)', count: 0, pids: [] }; zombies.push(group); }
      group.count++; group.pids.push(p.pid);
    }
  }
  for (const p of own) {
    if (p.pid <= 1 || blocked.has(p.pid)) continue;
    // Exactly node + a code-forge entry point + worker, never an argv substring in a shell/-e.
    const worker = /^(?:\S*\/)?node(?:js)?\s+(?:\S*\/)?code-forge\.mjs\s+worker\s+--run(?:=|\s+)([a-z0-9][a-z0-9-]{0,63})(?:\s|$)/.exec(p.command);
    if (worker && !opts.orphansOnly) {
      try {
        const record = await loadRun(worker[1]);
        if (typeof record.status === 'string' && !['open', 'running', 'active'].includes(record.status)) items.push({ ...p, kind: 'worker', runId: worker[1] });
      } catch (err) {
        if (err?.code === 'no-run') items.push({ ...p, kind: 'worker', runId: worker[1] });
      }
    }
    if (p.ppid !== 1 || items.some((item) => item.pid === p.pid)) continue;
    const env = envFor(p);
    const owner = env?.CODE_FORGE_OWNER_PID;
    if (!owner || !/^[1-9]\d*$/.test(owner) || +owner <= 1 || (opts.runId && env.CODE_FORGE_RUN_ID !== opts.runId)) continue;
    const liveOwner = rows.find((r) => r.pid === +owner);
    const recorded = env.CODE_FORGE_OWNER_START_TIME;
    // A missing row alone is not proof of death when ps was incomplete.
    const dead = !liveOwner ? !isAlive(+owner) : !!recorded && recorded !== UNKNOWN_START_TIME && liveOwner.start_time !== recorded;
    if (dead) items.push({ ...p, kind: 'orphan', ...(env.CODE_FORGE_RUN_ID ? { runId: env.CODE_FORGE_RUN_ID } : {}) });
  }
  const root = tmp.tmpBase(opts.root);
  const registryRoots = [];
  if (!opts.orphansOnly && tmp.untrustedReason(root) === null) {
    for (const dir of readdirSync(root, { withFileTypes: true })) {
      const full = path.join(root, dir.name);
      if (!dir.isDirectory() || tmp.untrustedReason(full) !== null || tmp.ownerState(full) !== 'dead') continue;
      registryRoots.push(full);
      for (const entry of listEntries(path.join(full, 'pids'))) {
        const p = own.find((p) => p.pid === entry.pid && p.start_time === entry.start_time);
        if (!p || blocked.has(p.pid) || items.some((i) => i.pid === p.pid)) continue;
        items.push({ ...p, kind: 'registry' });
      }
    }
  }
  // Memory across all code-forge trees, deduplicated. Unmarked descendants are children too.
  const forge = new Set(own.filter((p) => /^(?:\S*\/)?node(?:js)?\s+(?:\S*\/)?code-forge\.mjs(?:\s|$)/.test(p.command) || p.pid === process.pid).map((p) => p.pid));
  const tree = new Set(forge);
  let size;
  do { size = tree.size; for (const p of own) if (tree.has(p.ppid)) tree.add(p.pid); } while (tree.size !== size);
  const sum = (/** @type {ProcessInfo[]} */ ps) => ps.reduce((n, p) => n + p.rssMb, 0);
  const memory = { codeForgeMb: sum(own.filter((p) => forge.has(p.pid))), childrenMb: sum(own.filter((p) => tree.has(p.pid) && !forge.has(p.pid))), orphansMb: sum(items.filter((p) => p.kind === 'orphan')) };
  if (opts.apply) {
    for (const item of items.filter((p) => p.kind !== 'registry')) {
      const stopped = await stopMembers(new Map([[item.pid, item.start_time]]), opts.graceMs);
      item.action = stopped.has(item.pid) ? 'stopped' : 'skipped';
    }
    if (!opts.orphansOnly) await tmp.sweepRoots({ root, graceMs: opts.graceMs });
    for (const item of items.filter((p) => p.kind === 'registry')) item.action = readStartTime(item.pid) === item.start_time ? 'skipped' : 'stopped';
  }
  return { apply: opts.apply === true, items, registryRoots, zombies, totalRssMb: sum(items), memory };
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
