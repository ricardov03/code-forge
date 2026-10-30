import assert from 'node:assert/strict';
import childProcess, { execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { syncBuiltinESMExports } from 'node:module';
import { createInterface } from 'node:readline';
import { after, mock, test } from 'node:test';
import { exec } from '../../src/util/exec.mjs';
import { readStartTime } from '../../src/util/reaper.mjs';

const children = new Set();
after(() => {
  for (const pid of children) {
    try { process.kill(pid, 'SIGKILL'); } catch { /* gone */ }
  }
});

function running(pid) {
  try { return !execFileSync('/bin/ps', ['-o', 'stat=', '-p', String(pid)], { encoding: 'utf8' }).trim().startsWith('Z'); }
  catch (err) {
    if (err.status === 1) return false; // ps found no process with this pid.
    throw err;
  }
}

for (const mode of ['success', 'failure', 'timeout']) {
  test(`exec stops remaining group members after a ${mode} leader`, { timeout: 20000 }, async (t) => {
    let ready;
    let exited;
    const spawn = childProcess.spawn;
    const spy = mock.method(childProcess, 'spawn', (command, args, options) => {
      const child = spawn(command, args, options);
      ready = once(createInterface({ input: child.stdout }), 'line');
      exited = once(child, 'exit');
      return child;
    });
    syncBuiltinESMExports();
    const code = `const {spawn}=require('node:child_process');
      const {writeSync}=require('node:fs');
      const c=spawn(process.execPath,['-e',"process.on('SIGTERM',()=>{}); process.on('SIGHUP',()=>{}); console.log('ready'); setInterval(()=>{},1000)"],{stdio:['ignore','pipe','ignore']});
      c.stdout.once('data',()=>{writeSync(1,String(c.pid)+'\\n'); ${mode === 'timeout' ? 'setInterval(()=>{},1000)' : `process.exit(${mode === 'failure' ? 7 : 0})`}});`;
    try {
      // Startup time under load must not decide whether the grandchild is even created.
      // Exercise exec's timeout callback only once the child's real readiness event arrives.
      if (mode === 'timeout') t.mock.timers.enable({ apis: ['setTimeout'] });
      const pending = exec([process.execPath, '-e', code], mode === 'timeout' ? { timeoutMs: 1500 } : {});
      const [pidText] = await ready;
      const pid = Number(pidText.trim());
      if (pid > 1) children.add(pid);
      assert.ok(pid > 1, `${mode}: exactly one grandchild pid reported`);
      if (mode === 'timeout') {
        t.mock.timers.tick(1500);
        t.mock.timers.reset();
      }
      const [exitCode, signal] = await exited;
      const result = await pending;
      assert.equal(result.stdout.trim(), String(pid));
      assert.equal(exitCode, mode === 'timeout' ? null : mode === 'failure' ? 7 : 0);
      assert.equal(signal, mode === 'timeout' ? 'SIGTERM' : null);
      assert.equal(result.timedOut, mode === 'timeout');
      assert.equal(running(pid), false, `${mode}: grandchild must be stopped before exec resolves`);
      assert.equal(result.result, mode === 'success' ? 'ok' : 'failed');
    } finally {
      if (mode === 'timeout') t.mock.timers.reset();
      spy.mock.restore();
      syncBuiltinESMExports();
    }
  });
}

test('exec marks children even with a replaced environment, preserving known run identity', async () => {
  const result = await exec([process.execPath, '-e', 'console.log(JSON.stringify(process.env))'], {
    env: { CODE_FORGE_RUN_ID: 'r-fixture', CODE_FORGE_OWNER_PID: '1' },
  });
  const env = JSON.parse(result.stdout);
  assert.equal(env.CODE_FORGE_OWNER_PID, String(process.pid));
  assert.equal(env.CODE_FORGE_OWNER_START_TIME, readStartTime(process.pid));
  assert.equal(env.CODE_FORGE_RUN_ID, 'r-fixture');
});

test('exec drains the full stdout and stderr tail when the leader exits before its grandchild', { timeout: 20000 }, async () => {
  const bytes = 32 * 1024;
  const spawn = childProcess.spawn;
  const spy = mock.method(childProcess, 'spawn', (command, args, options) => {
    const child = spawn(command, args, options);
    // Hold the real pipes until exit to deterministically exercise pending output at settlement.
    child.once('spawn', () => {
      child.stdout?.pause();
      child.stderr?.pause();
      child.once('exit', () => setImmediate(() => {
        child.stdout?.resume();
        child.stderr?.resume();
      }));
    });
    return child;
  });
  syncBuiltinESMExports();
  const code = `const {spawn}=require('node:child_process');
    const {writeSync}=require('node:fs');
    const c=spawn(process.execPath,['-e',"process.on('SIGTERM',()=>{}); console.log('ready'); setInterval(()=>{},1000)"],{stdio:['ignore','pipe','ignore']});
    c.stdout.once('data',()=>{
      writeSync(1, String(c.pid)+'\\n');
      for (let i=0;i<${bytes / 4096};i++) {
        writeSync(1, Buffer.alloc(4096, 'o'));
        writeSync(2, Buffer.alloc(4096, 'e'));
      }
      process.exit(0);
    });`;
  let result;
  try {
    result = await exec([process.execPath, '-e', code], { timeoutMs: 10000 });
  } finally {
    spy.mock.restore();
    syncBuiltinESMExports();
  }
  const [pidText] = result.stdout.split('\n');
  const tail = result.stdout.slice(result.stdout.indexOf('\n') + 1);
  const pid = Number(pidText);
  if (pid > 1) children.add(pid);
  assert.equal(result.result, 'ok');
  assert.equal(result.code, 0);
  assert.equal(tail.length, bytes, 'all stdout bytes arrive');
  assert.equal(result.stderr.length, bytes, 'all stderr bytes arrive');
  assert.ok(pid > 1);
  assert.equal(tail, 'o'.repeat(bytes));
  assert.equal(result.stderr, 'e'.repeat(bytes));
  assert.equal(running(pid), false, 'grandchild is stopped before exec resolves');
});
