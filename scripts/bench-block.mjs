#!/usr/bin/env node
// Maintainer benchmark: real CLI children and an in-process fake reviewer, no model or network.
import { execFileSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { inspectLeftovers } from './bench-verbs.mjs';

const CLI = fileURLToPath(new URL('../bin/code-forge.mjs', import.meta.url));
const ENTRY = fileURLToPath(new URL('../bin/code-forge', import.meta.url));
const SCRIPT = fileURLToPath(import.meta.url);
const USAGE = 'usage: node scripts/bench-block.mjs [--files n] [--review-ms n] [--ledger-rows n] [--out file] [--json]\n';
const CONFIG = 'version: 1\nproject:\n  slug: bench-block\nprovider: anthropic\nlevels:\n  L2:\n    model: claude-opus-5-5\n';

function parseArgs(argv) {
  const params = { files: 6, review_ms: 50, ledger_rows: 2000 };
  let json = false;
  let out;
  let mode;
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (flag === '--json') json = true;
    else if (flag === '--mode') {
      mode = argv[++i];
      if (!['node', 'go', 'entry'].includes(mode)) throw new Error('--mode needs node, go or entry');
    } else if (flag === '--out') {
      out = argv[++i];
      if (!out || out.startsWith('--')) throw new Error('--out needs a file');
    } else if (['--files', '--review-ms', '--ledger-rows'].includes(flag)) {
      const n = Number(argv[++i]);
      const min = flag === '--files' ? 1 : 0;
      if (!Number.isSafeInteger(n) || n < min) throw new Error(`${flag} needs a whole number >= ${min}`);
      params[flag.slice(2).replaceAll('-', '_')] = n;
    } else throw new Error(`unknown argument ${flag}`);
  }
  return { params, json, out, mode };
}

// RSS includes the measured child, which hosts the fake worker, but excludes this observer;
// processes counts only sampled descendants. Short-lived children between ticks can be missed.
function sampler(rootPid) {
  const seen = new Set();
  let peak = 0;
  let failure;
  let inFlight;
  async function sample() {
    const snapshot = child('ps', ['-A', '-o', 'pid=,ppid=,rss='], process.cwd());
    const timeout = setTimeout(() => snapshot.proc.kill('SIGKILL'), 5000);
    let result;
    try { result = await snapshot.done; } finally { clearTimeout(timeout); }
    if (result.code !== 0) throw new Error(`ps failed: ${result.stderr || result.signal}`);
    const rows = result.stdout.trim().split('\n').map((line) => {
      const [pid, ppid, rss] = line.trim().split(/\s+/).map(Number);
      return { pid, ppid, rss };
    });
    const tree = new Set([rootPid]);
    let changed = true;
    while (changed) {
      changed = false;
      for (const row of rows) {
        if (tree.has(row.ppid) && !tree.has(row.pid)) {
          tree.add(row.pid);
          seen.add(row.pid);
          changed = true;
        }
      }
    }
    peak = Math.max(peak, rows.filter((r) => tree.has(r.pid)).reduce((sum, r) => sum + r.rss, 0));
  }
  const tick = () => {
    if (inFlight) return;
    inFlight = sample().catch((err) => {
      failure = err;
      clearInterval(timer);
    }).finally(() => { inFlight = undefined; });
  };
  const timer = setInterval(tick, 10);
  tick();
  return async () => {
    clearInterval(timer);
    await inFlight;
    if (failure) throw failure;
    return { processes: seen.size, peak_rss_mb: Math.round(peak / 1024 * 100) / 100 };
  };
}

function child(command, args, cwd, ipc = false) {
  const proc = spawn(command, args, { cwd, env: process.env, stdio: ipc ? ['ignore', 'pipe', 'pipe', 'ipc'] : ['ignore', 'pipe', 'pipe'] });
  let stdout = '';
  let stderr = '';
  proc.stdout.on('data', (chunk) => { stdout += chunk; });
  proc.stderr.on('data', (chunk) => { stderr += chunk; });
  const done = new Promise((resolve, reject) => {
    proc.once('error', reject);
    proc.once('close', (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
  // A daemon can fail before its readiness check; keep that rejection handled until cleanup.
  done.catch(() => {});
  return { proc, done };
}

async function stopChild(running) {
  if (!running) return;
  if (running.proc.exitCode === null && running.proc.signalCode === null) running.proc.kill('SIGTERM');
  const timer = setTimeout(() => running.proc.kill('SIGKILL'), 2000);
  try { await running.done; } finally { clearTimeout(timer); }
}

async function mode(name, dir, params) {
  const home = path.join(dir, 'h');
  const repo = path.join(dir, 'repo');
  mkdirSync(home, { recursive: true });
  mkdirSync(path.join(repo, 'src'), { recursive: true });
  // Keep the Unix socket address short even with macOS's long OS temp path. All
  // processes use this repo as cwd, so ../h still names the fixture's private HOME.
  process.chdir(repo);
  process.env.HOME = '../h';
  process.env.TMPDIR = dir;
  process.env.CODE_FORGE_TMP_ROOT = path.join(dir, 'tmp');
  process.env.CODE_FORGE_KEY_BACKEND = 'file';
  process.env.CODE_FORGE_NODE = process.execPath;
  process.env.CODE_FORGE_NO_ERROR_LOG = '1';
  process.env.PATH = `${path.dirname(process.execPath)}${path.delimiter}${process.env.PATH ?? ''}`;
  const run = `r-bench-block-${name}-${process.pid}`;
  const slug = `bench-block-${name}-${process.pid}`;
  const identity = { run_id: run, slug, home };
  if (name === 'node') process.env.CODE_FORGE_NATIVE = '0';
  else delete process.env.CODE_FORGE_NATIVE;
  // Pin HOME before importing any code that can resolve state or credentials.
  const { nativePath, daemonRequest } = await import('../src/util/native.mjs');
  const binary = name === 'node' ? null : nativePath();
  if (name !== 'node' && !binary) return { ...identity, skipped: 'no native binary', ...await inspectLeftovers(home) };
  const { startRun } = await import('../src/state/run.mjs');
  const { openBlock } = await import('../src/state/block.mjs');
  const { loadKey, signRow } = await import('../src/state/signer.mjs');
  const { appendRow } = await import('../src/ledger/write.mjs');
  const { announceWorker, beatWorker, retractWorker, pendingTickets, writeResult } = await import('../src/worker/queue.mjs');
  const { gitChildEnv } = await import('../src/worker/ticket.mjs');
  for (const args of [
    ['init', '-q'],
    ['-c', 'user.name=Bench', '-c', 'user.email=bench@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '--allow-empty', '-m', 'base'],
  ]) execFileSync('git', args, { cwd: repo, env: gitChildEnv(), stdio: 'pipe' });
  writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG.replace('bench-block', slug));
  const block = 'B1';
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId: run, writeRow });
  await openBlock({ runId: run, id: block, level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'bench', tests: ['bench'] }], writeRow });
  const key = await loadKey(run);
  for (let i = 0; i < params.ledger_rows; i++) {
    await writeRow(signRow({ event: 'bench.padding', run, index: i }, key));
  }
  for (let i = 0; i < params.files; i++) writeFileSync(path.join(repo, 'src', `f${i}.mjs`), `export const f${i} = ${i};\n`);

  const self = announceWorker(repo, { pid: process.pid, run });
  const heartbeat = setInterval(() => beatWorker(repo, self), 5000);
  const pending = new Map();
  let workerError;
  const worker = setInterval(() => {
    try {
      for (const ticket of pendingTickets(repo)) {
        if (!pending.has(ticket)) pending.set(ticket, performance.now() + params.review_ms);
        if (performance.now() < pending.get(ticket)) continue;
        writeResult({ repoRoot: repo, runId: run, ticket, key, result: { status: 'reviewed', approved: true, engine: 'bench', sessions: [] } });
        pending.delete(ticket);
      }
    } catch (err) { workerError = err; clearInterval(worker); }
  }, 5);
  let daemon;
  let active;
  let cliRuns = 0;
  let report;
  const cli = async (args) => {
    if (workerError) throw workerError;
    active = name === 'entry' ? child(ENTRY, [...args, '--run', run], repo) : child(process.execPath, [CLI, ...args, '--run', run], repo);
    cliRuns++;
    const timer = setTimeout(() => active.proc.kill('SIGKILL'), 60_000);
    let result;
    try { result = await active.done; } finally { clearTimeout(timer); }
    if (result.code !== 0) throw new Error(`${name} ${args[0]} failed: ${result.stderr || result.stdout || result.signal}`);
    if (workerError) throw workerError;
    return JSON.parse(result.stdout);
  };
  try {
    // Wait for the observer before starting the clock; fixture setup and teardown are excluded.
    if (process.send) {
      await new Promise((resolve) => {
        process.once('message', resolve);
        process.send('bench:start');
      });
    }
    const started = performance.now();
    if (binary) {
      daemon = child(binary, ['internal', 'serve', '--home', process.env.HOME, '--run', run], repo);
      let ready = false;
      for (let i = 0; i < 100 && daemon.proc.exitCode === null; i++) {
        if (await daemonRequest(run, { method: 'ping' }, { timeoutMs: 100 })) { ready = true; break; }
        await delay(20);
      }
      if (!ready) {
        await stopChild(daemon);
        throw new Error(`native daemon did not answer ping: ${(await daemon.done).stderr}`);
      }
    }
    for (let i = 0; i < params.files; i++) await cli(['review-file', `src/f${i}.mjs`, '--block', block]);
    const joined = await cli(['review-file', '--wait-block', block, '--max', '30s']);
    if (!joined.done || joined.files.length !== params.files || joined.files.some((f) => f.status !== 'done' || f.outcome !== 'approved')) {
      throw new Error(`${name} did not join ${params.files} approved reviews`);
    }
    await cli(['status', '--json']);
    const wall_ms = Math.round((performance.now() - started) * 100) / 100;
    report = { ...identity, cli_runs: cliRuns, wall_ms };
    return report;
  } finally {
    process.send?.('bench:stop');
    clearInterval(worker);
    clearInterval(heartbeat);
    await Promise.all([stopChild(active), stopChild(daemon)]);
    retractWorker(repo, process.pid, self.start_time);
    const leftovers = await inspectLeftovers(home);
    if (report) Object.assign(report, leftovers);
  }
}

async function observeMode(name, params) {
  const args = [SCRIPT, '--mode', name, '--json'];
  for (const [key, value] of Object.entries(params)) args.push(`--${key.replaceAll('_', '-')}`, String(value));
  const running = child(process.execPath, args, process.cwd(), true);
  let finishSampling;
  let measurements;
  running.proc.on('message', (message) => {
    if (message === 'bench:start') {
      finishSampling = sampler(running.proc.pid);
      running.proc.send('bench:ready');
    } else if (message === 'bench:stop' && finishSampling) {
      measurements = finishSampling();
      measurements.catch(() => {});
    }
  });
  try {
    const result = await running.done;
    if (result.code !== 0 && !result.stdout.trim()) throw new Error(`${name} pass failed: ${result.stderr || result.signal}`);
    const report = JSON.parse(result.stdout.trim());
    if (result.code !== 0 && (result.code !== 1 || !(report.leftover_processes.count || report.native_sockets.length))) {
      throw new Error(`${name} pass failed: ${result.stderr || result.signal || result.code}`);
    }
    if ('skipped' in report) return report;
    if (!measurements) throw new Error(`${name} pass did not complete observation`);
    return { ...report, ...await measurements };
  } finally {
    try { if (finishSampling) await (measurements ?? finishSampling()); }
    finally { await stopChild(running); }
  }
}

async function main() {
  let opts;
  try { opts = parseArgs(process.argv.slice(2)); }
  catch (err) { process.stderr.write(`bench-block: ${err.message}\n${USAGE}`); return 2; }
  if (opts.mode) {
    const cwd = process.cwd();
    const dir = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-bb-')));
    let report;
    try { report = await mode(opts.mode, dir, opts.params); process.stdout.write(`${JSON.stringify(report)}\n`); }
    finally { process.chdir(cwd); rmSync(dir, { recursive: true, force: true }); }
    return report.leftover_processes.count || report.native_sockets.length ? 1 : 0;
  }
  const node = await observeMode('node', opts.params);
  const go = await observeMode('go', opts.params);
  const entry = await observeMode('entry', opts.params);
  const report = { node, go, entry, params: opts.params };
  const json = `${JSON.stringify(report)}\n`;
  if (opts.out) writeFileSync(opts.out, json);
  if (opts.json) process.stdout.write(json);
  else {
    process.stdout.write('mode   processes  cli_runs  peak_rss_mb  wall_ms  leftover_processes\n');
    for (const [name, metrics] of Object.entries({ node, go, entry })) {
      process.stdout.write('skipped' in metrics ? `${name}    skipped: ${metrics.skipped}; leftover_processes: ${JSON.stringify(metrics.leftover_processes)}\n` : `${name.padEnd(7)}${String(metrics.processes).padEnd(11)}${String(metrics.cli_runs).padEnd(10)}${String(metrics.peak_rss_mb).padEnd(13)}${metrics.wall_ms}  ${JSON.stringify(metrics.leftover_processes)}\n`);
      if (metrics.native_sockets.length) process.stdout.write(`native_sockets: ${JSON.stringify(metrics.native_sockets)}\n`);
    }
  }
  return [node, go, entry].some((metrics) => metrics.leftover_processes.count || metrics.native_sockets.length) ? 1 : 0;
}

try { process.exitCode = await main(); }
catch (err) { process.stderr.write(`bench-block: ${err.message}\n`); process.exitCode = 1; }
finally { if (process.connected) process.disconnect(); }
