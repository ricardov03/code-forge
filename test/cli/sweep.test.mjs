import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { after, test } from 'node:test';
import { run } from '../../bin/code-forge.mjs';
import * as reaper from '../../src/util/reaper.mjs';
import { runRecordPath } from '../../src/state/paths.mjs';

const root = path.join(os.tmpdir(), 'sweep-fixtures');
mkdirSync(root, { recursive: true });
const script = path.join(root, 'code-forge.mjs');
writeFileSync(script, "console.log('ready'); setInterval(()=>{},1000);");
const children = new Set();
const orphanPids = new Set();
after(async () => {
  for (const child of children) child.kill('SIGKILL');
  for (const pid of orphanPids) { try { process.kill(pid, 'SIGKILL'); } catch { /* gone */ } }
  await sleep(50);
  rmSync(root, { recursive: true, force: true });
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
    const cli = await import('../../src/cli/sweep.mjs').catch(() => null);
    const ids = new Set([process.pid, ...[...children].map((c) => c.pid), ...orphanPids]);
    // Only our fixtures enter the scan: an apply test must never sweep the developer's workers.
    const psText = execFileSync('/bin/ps', ['-A', '-o', 'uid=,pid=,ppid=,stat=,rss=,lstart=,command='], { encoding: 'utf8' })
      .split('\n').filter((line) => ids.has(Number(line.trim().split(/\s+/)[1]))).join('\n');
    code = cli ? await cli.default(args, { sweep: (opts) => reaper.sweepProcesses({ ...opts, psText }) }) : await run(['sweep', ...args]);
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
  const lines = `501 810 1 S 2048 Wed Oct 21 10:00:00 2026 node parent.mjs
501 811 810 Z 0 Wed Oct 21 10:00:01 2026 <defunct>
501 812 810 Z+ 0 Wed Oct 21 10:00:02 2026 <defunct>
502 813 810 Z 0 Wed Oct 21 10:00:03 2026 <defunct>`;
  const report = await reaper.sweepProcesses?.({ psText: lines, uid: 501, apply: true, readEnv: () => null, readRun: async () => ({ status: 'active' }) });
  assert.deepEqual(report?.zombies ?? [], [{ ppid: 810, command: 'node parent.mjs', count: 2, pids: [811, 812] }]);
  assert.deepEqual(report.items, [], 'zombies and their parent produce zero signal candidates');
});

test('unreadable environment and live/reused owners fail closed; own run limits orphan cleanup', async () => {
  const started = reaper.readStartTime(process.pid);
  const lines = `501 820 1 S 1024 Wed Oct 21 10:00:00 2026 node helper
501 ${process.pid} 1 S 1024 ${started} node owner`;
  for (const env of [null, {}, { CODE_FORGE_OWNER_PID: String(process.pid), CODE_FORGE_OWNER_START_TIME: started }, { CODE_FORGE_OWNER_PID: '99999999', CODE_FORGE_RUN_ID: 'r-other' }]) {
    const report = await reaper.sweepProcesses?.({ psText: lines, uid: 501, orphansOnly: true, runId: 'r-own', readEnv: () => env });
    assert.deepEqual(report?.items ?? null, [], 'zero candidates without readable proof and matching run');
  }
});
