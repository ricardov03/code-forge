#!/usr/bin/env node
// Maintainer benchmark: time the actual npm entry, using only private fixture state.
import { spawnSync } from 'node:child_process';
import { accessSync, constants, cpSync, mkdirSync, mkdtempSync, readdirSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../', import.meta.url));
const ENTRY = path.join(ROOT, 'bin/code-forge');
const USAGE = 'usage: node scripts/bench-verbs.mjs [--runs n] [--case name]... [--json] [--out file]\n';
const CASES = [
  { name: 'logs path', args: ['logs', 'path'] },
  { name: 'logs --last 5 --json', args: ['logs', '--last', '5', '--json'] },
  { name: 'logs summary --json', args: ['logs', 'summary', '--json'] },
  { name: 'ledger tail', args: ['ledger', 'tail', '--slug', 'bench-verbs', '--n', '5'] },
  { name: '--version', args: ['--version'] },
  { name: 'status --json', args: ['status', '--json', '--run', 'r-bench-verbs'] },
];

function parseArgs(argv) {
  const params = { runs: 15, warmups: 2, cases: [] };
  let json = false, out;
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (flag === '--json') json = true;
    else if (flag === '--runs') {
      const value = argv[++i];
      if (!/^\d+$/.test(value ?? '') || !Number.isSafeInteger(Number(value)) || Number(value) < 1) throw new Error('--runs needs a whole number >= 1');
      params.runs = Number(value);
    } else if (flag === '--case') {
      const value = argv[++i];
      if (!CASES.some((entry) => entry.name === value)) throw new Error('--case needs one of: ' + CASES.map((entry) => entry.name).join(', '));
      params.cases.push(value);
    } else if (flag === '--out') {
      out = argv[++i];
      if (!out || out.startsWith('--')) throw new Error('--out needs a file');
    } else throw new Error(`unknown argument ${flag}`);
  }
  return { params, json, out };
}

/** Check before fixture removal, so cleanup cannot hide a socket or an orphan.
 * ps environment inspection also finds native/Node workers reparented to init.
 * Only process identity and argv are reported; environment values stay private.
 */
export async function inspectLeftovers(home) {
  // Synchronous timing spawns can leave exit callbacks queued until the event loop runs.
  await delay(50);
  const snapshot = spawnSync('ps', ['-A', '-o', 'pid=,ppid=,args='], { encoding: 'utf8', timeout: 5000 });
  if (snapshot.error || snapshot.status !== 0) throw new Error(`process probe failed: ${snapshot.error?.message ?? snapshot.stderr}`);
  const rows = snapshot.stdout.trim().split('\n').flatMap((line) => {
    const match = /^\s*(\d+)\s+(\d+)\s+(.+)$/.exec(line);
    return match ? [{ pid: Number(match[1]), ppid: Number(match[2]), command: match[3] }] : [];
  });
  const tree = new Set([process.pid]);
  let changed = true;
  while (changed) {
    changed = false;
    for (const row of rows) if (tree.has(row.ppid) && !tree.has(row.pid)) { tree.add(row.pid); changed = true; }
  }
  const fixture = path.dirname(home);
  const list = rows.filter((row) => {
    if (row.pid === process.pid || row.pid === snapshot.pid) return false;
    if (tree.has(row.pid)) return true;
    if (!/(?:^|[\s/])code-forge(?:-native|\.mjs)?(?:\s|$)/.test(row.command)) return false;
    if (row.command.includes(fixture)) return true;
    const env = spawnSync('ps', ['eww', '-p', String(row.pid), '-o', 'command='], { encoding: 'utf8', timeout: 5000 });
    if (env.error || env.status !== 0) {
      // A process that has exited is harmless; an unavailable probe is not proof.
      try { process.kill(row.pid, 0); } catch (err) { if (err.code === 'ESRCH') return false; }
      throw new Error(`process environment probe failed for pid ${row.pid}`);
    }
    return env.stdout.includes(`TMPDIR=${fixture}`) || env.stdout.includes(`HOME=${home}`)
      || env.stdout.includes(`CODE_FORGE_TMP_ROOT=${fixture}${path.sep}`);
  }).sort((a, b) => a.pid - b.pid);
  // ps can catch a child in the final instant of exec/exit (macOS renders it as
  // `(node)`). Confirm candidates after the probe itself has exited; never count
  // a historical snapshot entry as a live leftover.
  await delay(50);
  const live = list.filter((row) => {
    try { process.kill(row.pid, 0); return true; }
    catch (err) { if (err.code === 'ESRCH') return false; throw err; }
  });
  return { leftover_processes: { count: live.length, list: live }, native_sockets: nativeSockets(home) };
}

function nativeSockets(home) {
  const sockets = [];
  function walk(dir) {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      if (entry.name === 'native.sock') sockets.push(full);
      else if (entry.isDirectory()) walk(full);
    }
  }
  walk(home);
  return sockets.sort();
}

function hasNative() {
  try {
    const binary = path.join(ROOT, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
    accessSync(binary, constants.X_OK);
    return statSync(binary).isFile();
  }
  catch { return false; }
}

function fixture(dir) {
  const home = path.join(dir, 'home'), repo = path.join(dir, 'repo'), seed = path.join(dir, 'seed');
  mkdirSync(repo); mkdirSync(home); mkdirSync(path.join(dir, 'tmp'));
  // Do not inherit credentials, git routing, Node preloads or caller state overrides.
  const env = { PATH: `${path.dirname(process.execPath)}${path.delimiter}${process.env.PATH ?? ''}`,
    HOME: home, TMPDIR: dir, CODE_FORGE_TMP_ROOT: path.join(dir, 'tmp'), CODE_FORGE_KEY_BACKEND: 'file',
    CODE_FORGE_NODE: process.execPath, CODE_FORGE_NO_ERROR_LOG: '1', LC_ALL: 'C' };
  const git = spawnSync('git', ['init', '-q'], { cwd: repo, env, encoding: 'utf8' });
  if (git.error || git.status !== 0) throw new Error(`fixture git init: ${git.error?.message ?? git.stderr}`);
  writeFileSync(path.join(repo, '.code-forge.yml'), 'version: 1\nproject:\n  slug: bench-verbs\nprovider: anthropic\nlevels:\n  L2:\n    model: claude-opus-5-5\nnative:\n  daemon: false\n');
  const state = path.join(home, '.code-forge');
  for (const sub of ['runs', 'logs', 'ledger']) mkdirSync(path.join(state, sub), { recursive: true });
  writeFileSync(path.join(state, 'runs/r-bench-verbs.json'), JSON.stringify({ run_id: 'r-bench-verbs', project: 'bench-verbs', workspace: repo, status: 'active', mode: 'plan', started_at: '2099-01-02T03:04:05.000Z', blocks: {} }));
  writeFileSync(path.join(state, 'ledger/bench-verbs.jsonl'), Array.from({ length: 25 }, (_, index) => JSON.stringify({ event: 'bench.padding', run: 'r-bench-verbs', index })).join('\n') + '\n');
  writeFileSync(path.join(state, 'logs/errors.jsonl'), Array.from({ length: 12 }, (_, index) => JSON.stringify({ ts: '2099-01-02T03:04:05.000Z', verb: 'run', sub: null, kind: 'exit', exit: 2, message: `fixture error ${index}`, fp: String(index % 4).padStart(12, '0') })).join('\n') + '\n');
  cpSync(home, seed, { recursive: true });
  const sockets = new Set();
  const reset = () => {
    // Restoring a snapshot must not erase evidence left by an earlier invocation.
    for (const socket of nativeSockets(home)) sockets.add(socket);
    rmSync(home, { recursive: true, force: true }); cpSync(seed, home, { recursive: true });
  };
  return { home, repo, env, reset, sockets };
}

function rss(stderr) {
  const match = process.platform === 'darwin' ? /(\d+)\s+maximum resident set size/.exec(stderr)
    : /Maximum resident set size \(kbytes\):\s*(\d+)/.exec(stderr);
  return match ? Number(match[1]) / (process.platform === 'darwin' ? 1024 * 1024 : 1024) : null;
}

// Mirror the parity harness's text normalization, preserving JSON ordering and whitespace.
function normalize(text, f) {
  text = text.replaceAll(f.repo, '<repo>').replaceAll(f.home, '<home>');
  text = text.replace(/(<home>\/tmp\/[^/\s"'\\]*?)[A-Za-z0-9]{6}(?=[/\s"'\\]|$)/g, '$1<temp>');
  text = text.replaceAll(path.dirname(f.repo) + path.sep, '<copy>/');
  text = text.replace(/\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\b/g, (stamp) => Number.isFinite(Date.parse(stamp)) ? '<timestamp>' : stamp);
  text = text.replace(/\br-\d{8}-\d{6}-[a-f0-9]{6}\b/g, '<clock-run>');
  const pids = new Set(['pid', 'pids', 'worker_pid']);
  const durations = new Set(['duration_ms', 'elapsed_ms', 'wall_ms', 'durationMs', 'elapsedMs', 'wallMs', 'total_duration_ms', 'join_ms', 'gates_ms', 'merge_gate_ms', 'proofTimeMs', 'gateTimeMs', 'reviewTimeMs', 'humanWaitMs']);
  text = text.replace(/("([^"\n]+)"\s*:\s*)(\d+(?:\.\d+)?)(?=\s*[,}\]])/g, (all, prefix, field) => pids.has(field) ? `${prefix}<pid>` : durations.has(field) ? `${prefix}<duration>` : all);
  return text.replace(/\bpid(\s+|\s*[=:]\s*)\d+\b/gi, 'pid$1<pid>');
}

function summarize(samples) {
  const sorted = samples.map((sample) => sample.wall_ms).sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  const peaks = samples.map((sample) => sample.peak_rss_mb).filter((value) => value !== null);
  return { median_ms: sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2,
    p90_ms: sorted[Math.ceil(sorted.length * 0.9) - 1], min_ms: sorted[0], peak_rss_mb: peaks.length ? Math.max(...peaks) : null, samples };
}

async function main() {
  let opts;
  try { opts = parseArgs(process.argv.slice(2)); }
  catch (err) { process.stderr.write(`bench-verbs: ${err.message}\n${USAGE}`); return 2; }
  const dir = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-bv-')));
  try {
    const f = fixture(dir), native = hasNative();
    const flag = process.platform === 'darwin' ? '-l' : '-v';
    const probe = ['darwin', 'linux'].includes(process.platform)
      ? spawnSync('/usr/bin/time', [flag, process.execPath, '-e', ''], { cwd: f.repo, env: f.env, encoding: 'utf8', timeout: 5000 }) : null;
    const time = probe?.status === 0 && rss(probe.stderr) !== null;
    function invoke(args, lane, node = process.execPath) {
      f.reset(); // Restore the same snapshot outside the measured interval, even for probes/warmups.
      const env = { ...f.env, CODE_FORGE_NATIVE: lane === 'node' ? '0' : '1', CODE_FORGE_NODE: node };
      const started = performance.now();
      const result = spawnSync(time ? '/usr/bin/time' : ENTRY, time ? [flag, ENTRY, ...args] : args,
        { cwd: f.repo, env, encoding: 'utf8', timeout: 30_000, maxBuffer: 8 * 1024 * 1024 });
      const wall_ms = performance.now() - started;
      if (result.error || result.signal || result.status === null) throw new Error(`${lane} ${args.join(' ')} did not exit normally: ${result.error?.message ?? result.signal}`);
      return { code: result.status, stdout: normalize(result.stdout, f), sample: { wall_ms, peak_rss_mb: time ? rss(result.stderr) : null } };
    }
    const cases = [];
    for (const entry of CASES.filter((entry) => !opts.params.cases.length || opts.params.cases.includes(entry.name))) {
      const samples = { node: [], go: [] };
      let parity = native ? true : null, engine = null;
      if (native) engine = invoke(entry.args, 'go', '/nonexistent').code === 0 ? 'go' : 'node-handover';
      for (let i = 0; i < opts.params.warmups + opts.params.runs; i++) {
        const lanes = native ? (i % 2 ? ['go', 'node'] : ['node', 'go']) : ['node'];
        const pair = {};
        for (const lane of lanes) {
          pair[lane] = invoke(entry.args, lane);
          if (i >= opts.params.warmups) samples[lane].push(pair[lane].sample);
        }
        if (native && (pair.node.code !== pair.go.code || pair.node.stdout !== pair.go.stdout)) parity = false;
      }
      const node = summarize(samples.node), go = native ? summarize(samples.go) : { skipped: 'no native binary' };
      cases.push({ case: entry.name, engine, node, go, speedup: native ? node.median_ms / go.median_ms : null, parity });
    }
    const report = { cases, ...await inspectLeftovers(f.home), params: opts.params,
      host: { platform: process.platform, arch: process.arch, node: process.version, cpus: os.cpus().length } };
    report.native_sockets = [...new Set([...f.sockets, ...report.native_sockets])].sort();
    const json = `${JSON.stringify(report)}\n`;
    if (opts.out) writeFileSync(opts.out, json);
    if (opts.json) process.stdout.write(json);
    else {
      process.stdout.write('case | engine | lane | median_ms | p90_ms | min_ms | peak_rss_mb | speedup | parity\n');
      for (const entry of cases) for (const lane of ['node', 'go']) {
        const metrics = entry[lane];
        process.stdout.write(`${entry.case} | ${entry.engine ?? 'unmeasured'} | ${lane} | ${'skipped' in metrics ? `skipped: ${metrics.skipped}` : `${metrics.median_ms.toFixed(2)} | ${metrics.p90_ms.toFixed(2)} | ${metrics.min_ms.toFixed(2)} | ${metrics.peak_rss_mb?.toFixed(2) ?? 'null'}`} | ${entry.speedup?.toFixed(2) ?? 'null'} | ${entry.parity}\n`);
      }
      process.stdout.write(`leftover_processes: ${JSON.stringify(report.leftover_processes)}; native_sockets: ${JSON.stringify(report.native_sockets)}\n`);
    }
    return cases.some((entry) => entry.parity === false) || report.leftover_processes.count || report.native_sockets.length ? 1 : 0;
  } finally { rmSync(dir, { recursive: true, force: true }); }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = await main(); }
  catch (err) { process.stderr.write(`bench-verbs: ${err.message}\n`); process.exitCode = 1; }
}
