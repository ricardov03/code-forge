import assert from 'node:assert/strict';
import childProcess, { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { runRun } from '../../src/cli/run.mjs';
import { checkReviewState } from '../../src/doctor/local.mjs';
import { isAlive, parseProcesses, readProcesses, sweepProcesses } from '../../src/util/reaper.mjs';

const STARTED = 'Fri Oct 30 10:00:00 2026';
const workerRow = (pid, id) => `${process.getuid()} ${pid} 1 S 1024 ${STARTED} node /fixture/code-forge.mjs worker --run ${id}`;

// Synthetic workers never reach the OS: model both the process table and signal delivery.
function processTable(t, psText) {
  const alive = new Set(parseProcesses(psText).map((p) => p.pid));
  const signals = [];
  const ps = t.mock.method(childProcess, 'execFileSync', (file, args) => {
    assert.ok(String(file).endsWith('ps'));
    assert.ok(args.includes('uid=,pid=,ppid=,stat=,rss=,lstart=,command='));
    return psText.split('\n').filter((line) => alive.has(Number(line.split(' ')[1]))).join('\n');
  });
  const kill = t.mock.method(process, 'kill', (pid, signal) => {
    signals.push([pid, signal]);
    alive.delete(pid);
    return true;
  });
  syncBuiltinESMExports();
  t.after(() => { ps.mock.restore(); kill.mock.restore(); syncBuiltinESMExports(); });
  return { alive, signals };
}

function fixtureRoot(t) {
  const root = mkdtempSync(path.join(os.tmpdir(), 'sweep-home-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}

test('sweep excludes foreign HOME, unreadable env and missing HOME before consulting run records', async (t) => {
  const root = fixtureRoot(t);
  const psText = [90000110, 90000111, 90000112].map((pid) => workerRow(pid, 'r-missing')).join('\n');
  const { alive, signals } = processTable(t, psText);
  const envs = new Map([[90000110, { HOME: `${os.homedir()}-other` }], [90000111, null], [90000112, {}]]);
  const reads = [];
  for (const apply of [false, true]) {
    const report = await sweepProcesses({ root, psText, apply, graceMs: 0,
      readEnv: (p) => envs.get(p.pid),
      readRun: async (id) => { reads.push(id); throw Object.assign(new Error('missing'), { code: 'no-run' }); },
    });
    assert.deepEqual(report.items, [], `${apply ? 'apply' : 'dry run'} lists zero workers outside a proven home`);
    assert.equal(report.totalRssMb, 0, 'excluded workers contribute no stale RSS');
  }
  assert.deepEqual(reads, [], 'no run records are consulted for workers outside this home');
  assert.deepEqual(signals, [], 'no signals are sent to excluded workers');
  assert.deepEqual([...alive], [90000110, 90000111, 90000112], 'all three excluded workers survive');
});

test('same-home closed and missing workers stop; open, running, active and unreadable runs survive', async (t) => {
  const root = fixtureRoot(t);
  const statuses = ['closed', 'missing', 'open', 'running', 'active', 'unreadable', 'foreign-missing'];
  const psText = statuses.map((status, i) => workerRow(90000120 + i, `r-${status}`)).join('\n');
  const { alive, signals } = processTable(t, psText);
  const opts = { root, psText, graceMs: 0,
    readEnv: (p) => ({ HOME: p.pid === 90000126 ? `${os.homedir()}-other` : os.homedir() }),
    readRun: async (id) => {
      if (id.endsWith('missing')) throw Object.assign(new Error('missing'), { code: 'no-run' });
      if (id === 'r-unreadable') throw new Error('unreadable run');
      return { status: id.slice(2) };
    },
  };
  const expected = [['worker', 90000120, 'r-closed'], ['worker', 90000121, 'r-missing']];
  const dry = await sweepProcesses(opts);
  assert.deepEqual(dry.items.map((p) => [p.kind, p.pid, p.runId]), expected, 'exactly the two same-home stale workers are listed');
  assert.deepEqual(signals, [], 'dry run sends zero signals');
  const applied = await sweepProcesses({ ...opts, apply: true });
  assert.deepEqual(applied.items.map((p) => [p.kind, p.pid, p.runId, p.action]), expected.map((p) => [...p, 'stopped']));
  assert.deepEqual(signals, [[90000120, 'SIGTERM'], [90000121, 'SIGTERM']], 'exactly the two stale workers receive SIGTERM');
  assert.deepEqual([...alive], [90000122, 90000123, 90000124, 90000125, 90000126], 'all five other workers survive');
});

test('run start in an isolated HOME preserves a live foreign-home worker and doctor excludes it', { timeout: 15000 }, async (t) => {
  const root = fixtureRoot(t);
  const foreignHome = path.join(root, 'other home');
  const tmpRoot = path.join(root, 'tmp');
  mkdirSync(foreignHome);
  mkdirSync(tmpRoot, { mode: 0o700 });
  const script = path.join(root, 'code-forge.mjs');
  writeFileSync(script, "console.log('ready'); setInterval(() => {}, 1000);\n");
  writeFileSync(path.join(root, '.code-forge.yml'), `version: 1\nengine: harness\nproject:\n  slug: sweep-home\ntmp:\n  root: ${tmpRoot}\n`);
  const child = spawn(process.execPath, [script, 'worker', '--run', 'r-foreign-home'], {
    env: { ...process.env, HOME: foreignHome }, stdio: ['ignore', 'pipe', 'ignore'],
  });
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) {
      const exited = once(child, 'exit');
      child.kill('SIGKILL');
      await exited;
    }
  });
  await once(child.stdout, 'data');
  assert.notEqual(foreignHome, os.homedir(), 'the worker has a different HOME from the isolated sweeper');
  // Even the red run can inspect/signal only the dummy this test started.
  const execFileSync = childProcess.execFileSync;
  const ps = t.mock.method(childProcess, 'execFileSync', (file, args, options) => {
    const out = execFileSync(file, args, options);
    if (args?.includes('uid=,pid=,ppid=,stat=,rss=,lstart=,command=')) {
      return String(out).split('\n').filter((line) => Number(line.trim().split(/\s+/)[1]) === child.pid).join('\n');
    }
    return out;
  });
  syncBuiltinESMExports();
  t.after(() => { ps.mock.restore(); syncBuiltinESMExports(); });
  const visible = readProcesses();
  assert.deepEqual(visible.map((p) => p.pid), [child.pid], 'the OS scan sees exactly our live dummy');
  assert.match(visible[0].command, /\/code-forge\.mjs worker --run r-foreign-home$/);
  const doctor = await checkReviewState({ tmp: { root: tmpRoot } });
  let stderr = '';
  const code = await runRun(['start', '--cwd', root, '--run', 'r-isolated-home'], {
    stdout: { write() {} }, stderr: { write(s) { stderr += s; } },
  });
  assert.equal(code, 0, stderr);
  assert.equal(isAlive(child.pid), true, 'run start leaves the live worker in the other HOME running');
  assert.equal(child.signalCode, null, 'run start delivered no terminating signal');
  const sweepRows = doctor.filter((row) => row.id === 'sweep');
  assert.equal(sweepRows.length, 1, 'doctor reports exactly one sweep row');
  assert.match(sweepRows[0].detail, /^0 stale workers, 0 orphans, 0 zombies/, 'doctor excludes the foreign worker');
});
