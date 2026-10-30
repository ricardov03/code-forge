import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import runSweep from '../../src/cli/sweep.mjs';
import * as reaper from '../../src/util/reaper.mjs';
import { runRecordPath } from '../../src/state/paths.mjs';
import { checkReviewState } from '../../src/doctor/local.mjs';

const root = path.join(os.tmpdir(), 'sweep-fixtures');
const previousHome = process.env.HOME;
process.env.HOME = path.join(root, 'home');
mkdirSync(root, { recursive: true });
const script = path.join(root, 'code-forge.mjs');
writeFileSync(script, "console.log('ready'); setInterval(()=>{},1000);");
const children = new Set();
const orphanPids = new Set();

test('sweep fixture run records stay inside the fixture home', () => {
  for (const id of ['r-closed', 'r-open', 'r-running', 'r-active', 'r-corrupt']) {
    assert.equal(path.dirname(runRecordPath(id)), path.join(root, 'home', '.code-forge', 'runs'));
  }
});

test('a failed sweep import aborts the fixture suite without invoking an unfiltered CLI', () => {
  // Replace the top-level CLI with a harmless sentinel: the old fallback must never reach
  // real processes, even while this regression test is red.
  const sweepURL = new URL('../../src/cli/sweep.mjs', import.meta.url).href;
  const binURL = new URL('../../bin/code-forge.mjs', import.meta.url).href;
  const sentinel = path.join(root, 'unfiltered-cli-called');
  const stub = `import {writeFileSync} from 'node:fs'; export async function run() { writeFileSync(${JSON.stringify(sentinel)}, 'called'); return 0; }`;
  const loader = `export async function load(url, context, nextLoad) {
    if (url === ${JSON.stringify(sweepURL)}) throw new Error('forced-sweep-import-failure');
    if (url === ${JSON.stringify(binURL)}) return {format:'module',shortCircuit:true,
      source:${JSON.stringify(stub)}};
    return nextLoad(url, context);
  }`;
  const register = `import {register} from 'node:module'; register(${JSON.stringify(`data:text/javascript,${encodeURIComponent(loader)}`)}, import.meta.url);`;
  const result = spawnSync(process.execPath, [
    '--import', new URL('../helpers/isolate.mjs', import.meta.url).href,
    '--import', `data:text/javascript,${encodeURIComponent(register)}`,
    '--test', '--test-name-pattern=^sweep dry run', fileURLToPath(import.meta.url),
  ], { encoding: 'utf8', timeout: 15000, env: { ...process.env, NODE_TEST_CONTEXT: undefined } });
  assert.ifError(result.error);
  const output = result.stdout + result.stderr;
  assert.equal(existsSync(sentinel), false, 'no unfiltered fallback is invoked');
  assert.match(output, /forced-sweep-import-failure/);
  assert.equal(result.status, 1, 'missing sweep module fails the suite');
});
after(async () => {
  for (const child of children) child.kill('SIGKILL');
  for (const pid of orphanPids) { try { process.kill(pid, 'SIGKILL'); } catch { /* gone */ } }
  await sleep(50);
  rmSync(root, { recursive: true, force: true });
  if (previousHome === undefined) delete process.env.HOME;
  else process.env.HOME = previousHome;
});

async function worker(id) {
  const child = spawn(process.execPath, [script, 'worker', '--run', id], { stdio: ['ignore', 'pipe', 'ignore'] });
  children.add(child);
  child.once('exit', () => children.delete(child));
  await once(child.stdout, 'data');
  return child;
}

async function command(args) {
  let out = '';
  const write = process.stdout.write;
  process.stdout.write = (chunk) => { out += chunk; return true; };
  let code;
  try {
    const ids = new Set([process.pid, ...[...children].map((c) => c.pid), ...orphanPids]);
    // Only our fixtures enter the scan: an apply test must never sweep the developer's workers.
    const psText = execFileSync('/bin/ps', ['-A', '-o', 'uid=,pid=,ppid=,stat=,rss=,lstart=,command='], { encoding: 'utf8' })
      .split('\n').filter((line) => ids.has(Number(line.trim().split(/\s+/)[1]))).join('\n');
    code = await runSweep(args, { sweep: (opts) => reaper.sweepProcesses({ ...opts, root, psText }) });
  } finally { process.stdout.write = write; }
  return { code, out, json: out.startsWith('{') ? JSON.parse(out) : null };
}

test('sweep dry run lists missing and closed workers with RSS; apply stops exactly those workers', async () => {
  const missing = await worker('r-missing');
  const closed = await worker('r-closed');
  mkdirSync(path.dirname(runRecordPath('r-closed')), { recursive: true });
  writeFileSync(runRecordPath('r-closed'), JSON.stringify({ status: 'ended' }));
  const dry = await command(['--json']);
  assert.equal(dry.code, 0, 'sweep dry run succeeds');
  assert.deepEqual(dry.json.items.filter((p) => p.kind === 'worker').map((p) => p.pid).sort(), [missing.pid, closed.pid].sort());
  assert.ok(dry.json.items.every((p) => typeof p.rssMb === 'number'));
  assert.ok(dry.json.totalRssMb > 0);
  assert.equal(reaper.isAlive(missing.pid), true, 'dry run preserves missing worker');
  assert.equal(reaper.isAlive(closed.pid), true, 'dry run preserves closed worker');
  const applied = await command(['--apply', '--json']);
  assert.equal(applied.code, 0);
  await sleep(50);
  assert.equal(reaper.isAlive(missing.pid), false, 'apply stops missing worker');
  assert.equal(reaper.isAlive(closed.pid), false, 'apply stops closed worker');
});

test('sweep preserves open, running and active workers and fails closed on unreadable records', async () => {
  const live = [];
  for (const status of ['open', 'running', 'active', 'corrupt']) {
    const id = `r-${status}`;
    mkdirSync(path.dirname(runRecordPath(id)), { recursive: true });
    writeFileSync(runRecordPath(id), status === 'corrupt' ? '{' : JSON.stringify({ status }));
    live.push(await worker(id));
  }
  const report = await command(['--apply', '--json']);
  assert.equal(report.code, 0);
  assert.deepEqual(report.json.items.filter((p) => live.some((c) => c.pid === p.pid)), [], 'zero live/unreadable workers listed');
  assert.deepEqual(live.map((c) => reaper.isAlive(c.pid)), [true, true, true, true]);
  for (const c of live) c.kill('SIGKILL');
  await sleep(50);
});

async function orphan(marked) {
  const owner = spawn(process.execPath, ['-e', `const {spawn}=require('node:child_process');
    const c=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{detached:true,stdio:'ignore',env:${marked ? '{...process.env,CODE_FORGE_OWNER_PID:String(process.pid),CODE_FORGE_RUN_ID:"r-orphan"}' : '{}'}});
    console.log(c.pid); c.unref();`], { stdio: ['ignore', 'pipe', 'ignore'] });
  children.add(owner);
  owner.once('exit', () => children.delete(owner));
  const exited = once(owner, 'exit');
  const [data] = await once(owner.stdout, 'data');
  const pid = Number(String(data).trim());
  orphanPids.add(pid);
  await exited;
  return pid;
}

test('sweep lists and stops marked orphans of dead owners, preserving unmarked orphans', async () => {
  const marked = await orphan(true);
  const unmarked = await orphan(false);
  const dry = await command(['--json']);
  assert.equal(dry.code, 0);
  assert.deepEqual(dry.json.items.filter((p) => [marked, unmarked].includes(p.pid)).map((p) => [p.kind, p.pid]), [['orphan', marked]]);
  assert.equal(reaper.isAlive(marked), true);
  assert.equal((await command(['--apply', '--json'])).code, 0);
  await sleep(100);
  assert.equal(reaper.readStartTime(marked), null, 'marked orphan stopped');
  assert.equal(reaper.isAlive(unmarked), true, 'unmarked orphan preserved');
});

test('zombie ps lines are grouped by parent command and never eligible for a signal', async () => {
  const parentCommand = `node ${script} worker --run r-zombie-parent`;
  const lines = `501 810 1 S 2048 Wed Oct 21 10:00:00 2026 ${parentCommand}
501 811 810 Z 0 Wed Oct 21 10:00:01 2026 node ${script} worker --run r-zombie
501 812 810 Z+ 0 Wed Oct 21 10:00:02 2026 <defunct>
502 813 810 Z 0 Wed Oct 21 10:00:03 2026 <defunct>
502 814 1 S 2048 Wed Oct 21 10:00:00 2026 node ${script} worker --run r-foreign`;
  const report = await reaper.sweepProcesses?.({ root, psText: lines, uid: 501, apply: true, readEnv: () => null, readRun: async () => ({ status: 'ended' }) });
  assert.deepEqual(report?.zombies ?? [], [{ ppid: 810, command: parentCommand, count: 2, pids: [811, 812] }]);
  assert.deepEqual(report.items.map((p) => [p.kind, p.pid]), [['worker', 810]], 'only the stale parent is a candidate; zombies and other users are excluded');
});

test('unreadable environment and live/reused owners fail closed; own run limits orphan cleanup', async () => {
  const started = reaper.readStartTime(process.pid);
  const lines = `501 820 1 S 1024 Wed Oct 21 10:00:00 2026 node helper
501 ${process.pid} 1 S 1024 ${started} node owner`;
  for (const env of [null, {}, { CODE_FORGE_OWNER_PID: String(process.pid), CODE_FORGE_OWNER_START_TIME: started, CODE_FORGE_RUN_ID: 'r-own' }, { CODE_FORGE_OWNER_PID: '99999999', CODE_FORGE_RUN_ID: 'r-other' }]) {
    const report = await reaper.sweepProcesses?.({ psText: lines, uid: 501, orphansOnly: true, runId: 'r-own', readEnv: () => env });
    assert.deepEqual(report?.items ?? null, [], 'zero candidates without readable proof and matching run');
  }
  const reused = await reaper.sweepProcesses?.({ root, psText: lines, uid: 501, orphansOnly: true, runId: 'r-own',
    readEnv: () => ({ CODE_FORGE_OWNER_PID: String(process.pid), CODE_FORGE_OWNER_START_TIME: 'Wed Jan 1 00:00:00 2001', CODE_FORGE_RUN_ID: 'r-own' }) });
  assert.deepEqual(reused.items.map((p) => [p.kind, p.pid]), [['orphan', 820]], 'one orphan whose recorded owner pid was reused');
});

test('doctor reports sweep and memory rows without stopping a stale worker', async () => {
  const child = await worker('r-doctor-missing');
  const rows = await checkReviewState({ tmp: { root } });
  assert.equal(rows.filter((r) => r.id === 'sweep').length, 1, 'exactly one sweep row');
  assert.match(rows.find((r) => r.id === 'sweep').detail, /\d+ stale workers, \d+ orphans, \d+ zombies/);
  assert.equal(rows.filter((r) => r.id === 'memory').length, 1, 'exactly one memory row');
  assert.match(rows.find((r) => r.id === 'memory').detail, /code-forge [\d.]+ MB, children [\d.]+ MB, orphans [\d.]+ MB/);
  assert.equal(reaper.isAlive(child.pid), true, 'doctor never stops stale worker');
  child.kill('SIGKILL');
});
