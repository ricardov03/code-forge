import { readFileSync, readdirSync, statSync } from 'node:fs';
import path from 'node:path';
import { parseFlags } from '../state/cli-args.mjs';
import { loadProjectConfig } from '../config/load.mjs';
import { configInForce } from '../state/config-snapshot.mjs';
import { readRun } from '../state/run.mjs';
import { readAllRows } from '../ledger/write.mjs';
import { exec } from '../util/exec.mjs';
import { sweepProcesses } from '../util/reaper.mjs';
import { writeSafe } from '../util/redact.mjs';

/** @param {Record<string, any>} cfg */
export function idleExitHours(cfg) {
  const hours = cfg?.worker?.idle_exit_hours;
  return typeof hours === 'number' && Number.isFinite(hours) && hours >= 0 ? hours : 24;
}

/**
 * Latest run activity, including for legacy workers. Missing worktrees are normal after a
 * merge; other read failures propagate so callers keep the worker alive. Workers supply
 * their root so queued work needs no git read. Metadata detects edits to already dirty files.
 * @param {Record<string, any>} record
 * @param {{now?: number, repoRoot?: string, readRows?: () => Promise<Record<string, any>[]>,
 * execute?: typeof exec, trees?: Map<string, string>}} [deps]
 */
export async function runActivityAt(record, deps = {}) {
  const now = deps.now ?? Date.now();
  const execute = deps.execute ?? exec;
  const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('GIT_')));
  Object.assign(env, { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0' });
  let root = deps.repoRoot;
  if (!root) {
    const top = await execute(['git', 'rev-parse', '--show-toplevel'], { cwd: record.workspace, env, timeoutMs: 10000 });
    if (top.result !== 'ok' || !top.stdout.trim()) throw new Error('idle repository check unavailable');
    root = top.stdout.trim();
  }
  const queue = path.join(root, '.code-forge', 'queue');
  let names;
  try { names = new Set(readdirSync(queue)); } catch (err) {
    if (err.code !== 'ENOENT' && err.code !== 'ENOTDIR') throw err;
    names = new Set();
  }
  for (const name of names) {
    if (!/^[a-f0-9]{24}\.json$/.test(name) || names.has(name.slice(0, 24) + '.done')) continue;
    // Include fresh claims too: a held completion marker cannot prove inactivity.
    if (JSON.parse(readFileSync(path.join(queue, name), 'utf8')).run === record.run_id) return now;
  }
  let latest = Date.parse(record.started_at);
  if (!Number.isFinite(latest)) return now;
  for (const row of await (deps.readRows ?? (() => readAllRows(record.project)))()) {
    if (row.run !== record.run_id) continue;
    const at = Date.parse(row.ts);
    latest = Math.max(latest, Number.isFinite(at) ? at : now);
  }
  for (const entry of Object.values(record.blocks ?? {})) {
    if (entry.status !== 'open') continue;
    const tree = entry.worktree?.path ?? root;
    let info;
    try { info = statSync(tree); } catch (err) {
      if (entry.worktree && (err.code === 'ENOENT' || err.code === 'ENOTDIR')) continue;
      throw err;
    }
    latest = Math.max(latest, info.mtimeMs);
    const options = { cwd: tree, env, timeoutMs: 10000 };
    const listed = await execute(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z', '--', '.', ':!.code-forge'], options);
    const head = await execute(['git', 'log', '-1', '--format=%H:%ct'], options);
    if (listed.result !== 'ok' || head.result !== 'ok') throw new Error('idle worktree check unavailable');
    const fingerprint = [head.stdout];
    latest = Math.max(latest, Number(head.stdout.trim().split(':')[1]) * 1000 || 0);
    const dirs = new Set([tree]);
    for (const file of listed.stdout.split('\0').filter(Boolean)) {
      const full = path.join(tree, file);
      let parent = path.dirname(full);
      while (parent !== tree && parent.startsWith(tree + path.sep)) { dirs.add(parent); parent = path.dirname(parent); }
      try {
        const stat = statSync(full);
        latest = Math.max(latest, stat.mtimeMs, stat.ctimeMs);
        fingerprint.push(`${file}:${stat.mtimeMs}:${stat.ctimeMs}:${stat.size}`);
      } catch (err) {
        if (err.code !== 'ENOENT' && err.code !== 'ENOTDIR') throw err;
        fingerprint.push(`${file}:missing`);
      }
    }
    for (const dir of dirs) {
      try { latest = Math.max(latest, statSync(dir).mtimeMs); }
      catch (err) { if (err.code !== 'ENOENT' && err.code !== 'ENOTDIR') throw err; }
    }
    const stamp = fingerprint.join('\0');
    const previous = deps.trees?.get(tree);
    if (previous !== undefined && previous !== stamp) latest = now;
    deps.trees?.set(tree, stamp);
  }
  return latest;
}

/**
 * Keep the reaper's HOME, ownership and start-time checks for idle workers too. Only its
 * private classification sees an idle status; the authoritative run record stays active.
 * @param {Parameters<typeof sweepProcesses>[0]} [opts]
 * @param {typeof sweepProcesses} [sweep]
 */
export async function sweepIdleWorkers(opts = {}, sweep = sweepProcesses) {
  const loadRun = opts.readRun ?? readRun;
  return sweep({ ...opts, readRun: async (id) => {
    const record = await loadRun(id);
    if (!['open', 'running', 'active'].includes(record.status)) return record;
    try {
      let cfg = configInForce(record);
      if (!cfg) {
        const loaded = await loadProjectConfig(record.workspace);
        if (!loaded.ok) return record;
        cfg = loaded.config;
      }
      const hours = idleExitHours(cfg);
      if (hours > 0 && Date.now() - await runActivityAt(record) > hours * 3600000) return { ...record, status: 'idle' };
    } catch { /* unreadable activity cannot authorize termination */ }
    return record;
  } });
}

const HELP = 'usage: code-forge sweep [--apply] [--json]\nDry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.\n--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.\nZombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.\n';

/** @param {string[]} args @param {{sweep?:typeof sweepProcesses}} [deps] */
export default async function runSweep(args, deps = {}) {
  let flags;
  try {
    const parsed = parseFlags(args, { booleans: ['apply', 'json', 'help'] });
    if (parsed.positionals.length) throw new Error('unexpected positional argument');
    flags = parsed.flags;
  } catch {
    writeSafe(process.stderr, HELP);
    return 2;
  }
  if (flags.help) { writeSafe(process.stdout, HELP); return 0; }
  const report = await sweepIdleWorkers({ apply: flags.apply === true }, deps.sweep);
  if (flags.json) writeSafe(process.stdout, `${JSON.stringify(report)}\n`);
  else {
    for (const item of report.items) writeSafe(process.stdout,
      `${report.apply ? item.action ?? 'stopped' : 'would stop'} ${item.kind} pid ${item.pid} ${item.rssMb.toFixed(1)} MB ${item.command}\n`);
    for (const group of report.zombies) writeSafe(process.stdout, `${group.count} zombies (parent ${group.ppid} ${group.command}) — report only\n`);
    writeSafe(process.stdout, `${report.apply ? 'sweep' : 'dry run'}: ${report.items.length} processes, total ${report.totalRssMb.toFixed(1)} MB; ${report.registryRoots.length} dead-run roots\n`);
  }
  return 0;
}
