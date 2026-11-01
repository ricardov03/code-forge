#!/usr/bin/env node
// Maintainer benchmark: real CLI children and an in-process fake reviewer, no model or network.
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';

const CLI = fileURLToPath(new URL('../bin/code-forge.mjs', import.meta.url));
const USAGE = 'usage: node scripts/bench-block.mjs [--files n] [--review-ms n] [--ledger-rows n] [--out file] [--json]\n';
const CONFIG = 'version: 1\nproject:\n  slug: bench-block\nprovider: anthropic\nlevels:\n  L2:\n    model: claude-opus-5-5\n';

function parseArgs(argv) {
  const params = { files: 6, review_ms: 50, ledger_rows: 2000 };
  let json = false;
  let out;
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (flag === '--json') json = true;
    else if (flag === '--out') {
      out = argv[++i];
      if (!out || out.startsWith('--')) throw new Error('--out needs a file');
    } else if (['--files', '--review-ms', '--ledger-rows'].includes(flag)) {
      const n = Number(argv[++i]);
      const min = flag === '--files' ? 1 : 0;
      if (!Number.isSafeInteger(n) || n < min) throw new Error(`${flag} needs a whole number >= ${min}`);
      params[flag.slice(2).replaceAll('-', '_')] = n;
    } else throw new Error(`unknown argument ${flag}`);
  }
  return { params, json, out };
}

// The observer itself is excluded. RSS includes this process, which hosts the fake worker;
// processes counts only sampled descendants. Short-lived children between ticks can be missed.
function sampler() {
  const seen = new Set();
  let peak = 0;
  let failure;
  function sample() {
    const snapshot = spawnSync('ps', ['-A', '-o', 'pid=,ppid=,rss='], { encoding: 'utf8', timeout: 5000 });
    if (snapshot.error || snapshot.status !== 0) throw snapshot.error ?? new Error(`ps failed: ${snapshot.stderr}`);
    const rows = snapshot.stdout.trim().split('\n').map((line) => {
      const [pid, ppid, rss] = line.trim().split(/\s+/).map(Number);
      return { pid, ppid, rss };
    });
    const observer = snapshot.pid;
    const tree = new Set([process.pid]);
    let changed = true;
    while (changed) {
      changed = false;
      for (const row of rows) {
        if (row.pid !== observer && tree.has(row.ppid) && !tree.has(row.pid)) {
          tree.add(row.pid);
          seen.add(row.pid);
          changed = true;
        }
      }
    }
    peak = Math.max(peak, rows.filter((r) => tree.has(r.pid)).reduce((sum, r) => sum + r.rss, 0));
  }
  sample();
  const timer = setInterval(() => {
    try { sample(); } catch (err) { failure = err; clearInterval(timer); }
  }, 10);
  return () => {
    clearInterval(timer);
    if (failure) throw failure;
    return { processes: seen.size, peak_rss_mb: Math.round(peak / 1024 * 100) / 100 };
  };
}

function child(command, args, cwd) {
  const proc = spawn(command, args, { cwd, env: process.env, stdio: ['ignore', 'pipe', 'pipe'] });
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
  if (name === 'node') process.env.CODE_FORGE_NATIVE = '0';
  else delete process.env.CODE_FORGE_NATIVE;
  // Pin HOME before importing any code that can resolve state or credentials.
  const { nativePath, daemonRequest } = await import('../src/util/native.mjs');
  const binary = name === 'go' ? nativePath() : null;
  if (name === 'go' && !binary) return { skipped: 'no native binary' };
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
  writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG);
  const run = 'r-bench-block';
  const block = 'B1';
  const writeRow = (row) => appendRow(row, { slug: 'bench-block' });
  await startRun({ workspace: repo, project: 'bench-block', runId: run, writeRow });
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
  let finishSampling;
  let cliRuns = 0;
  const cli = async (args) => {
    if (workerError) throw workerError;
    active = child(process.execPath, [CLI, ...args, '--run', run], repo);
    cliRuns++;
    const timer = setTimeout(() => active.proc.kill('SIGKILL'), 60_000);
    let result;
    try { result = await active.done; } finally { clearTimeout(timer); }
    if (result.code !== 0) throw new Error(`${name} ${args[0]} failed: ${result.stderr || result.stdout || result.signal}`);
    if (workerError) throw workerError;
    return JSON.parse(result.stdout);
  };
  try {
    finishSampling = sampler();
    const started = performance.now();
    if (binary) {
      daemon = child(binary, ['serve', '--home', process.env.HOME, '--run', run], repo);
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
    return { ...finishSampling(), cli_runs: cliRuns, wall_ms };
  } finally {
    clearInterval(worker);
    clearInterval(heartbeat);
    try { finishSampling?.(); }
    finally {
      await Promise.all([stopChild(active), stopChild(daemon)]);
      retractWorker(repo, process.pid, self.start_time);
    }
  }
}

async function main() {
  let opts;
  try { opts = parseArgs(process.argv.slice(2)); }
  catch (err) { process.stderr.write(`bench-block: ${err.message}\n${USAGE}`); return 2; }
  if (opts.out) opts.out = path.resolve(opts.out);
  const cwd = process.cwd();
  const parent = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-bb-')));
  try {
    const node = await mode('node', path.join(parent, 'n'), opts.params);
    const go = await mode('go', path.join(parent, 'g'), opts.params);
    const report = { node, go, params: opts.params };
    const json = `${JSON.stringify(report)}\n`;
    if (opts.out) writeFileSync(opts.out, json);
    if (opts.json) process.stdout.write(json);
    else {
      process.stdout.write('mode  processes  cli_runs  peak_rss_mb  wall_ms\n');
      for (const [name, metrics] of Object.entries({ node, go })) {
        process.stdout.write('skipped' in metrics ? `${name}    skipped: ${metrics.skipped}\n` : `${name.padEnd(6)}${String(metrics.processes).padEnd(11)}${String(metrics.cli_runs).padEnd(10)}${String(metrics.peak_rss_mb).padEnd(13)}${metrics.wall_ms}\n`);
      }
    }
    return 0;
  } finally { process.chdir(cwd); rmSync(parent, { recursive: true, force: true }); }
}

try { process.exitCode = await main(); }
catch (err) { process.stderr.write(`bench-block: ${err.message}\n`); process.exitCode = 1; }
