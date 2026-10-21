import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { after, test } from 'node:test';
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
  catch { return false; }
}

test('exec stops remaining group members after successful, failed and timed-out leaders', { timeout: 20000 }, async () => {
  for (const mode of ['success', 'failure', 'timeout']) {
    const code = `const {spawn}=require('node:child_process');
      const c=spawn(process.execPath,['-e',"process.on('SIGTERM',()=>{}); process.on('SIGHUP',()=>{}); console.log('ready'); setInterval(()=>{},1000)"],{stdio:['ignore','pipe','ignore']});
      c.stdout.once('data',()=>{console.log(c.pid); ${mode === 'timeout' ? 'setInterval(()=>{},1000)' : `process.exit(${mode === 'failure' ? 7 : 0})`}});`;
    const result = await exec([process.execPath, '-e', code], { timeoutMs: 1500 });
    const pid = Number(result.stdout.trim());
    children.add(pid);
    assert.ok(pid > 1, `${mode}: exactly one grandchild pid reported`);
    assert.equal(running(pid), false, `${mode}: grandchild must be stopped before exec resolves`);
    assert.equal(result.result, mode === 'success' ? 'ok' : 'failed');
  }
});

test('exec marks children even with a replaced environment, preserving known run identity', async () => {
  const result = await exec([process.execPath, '-e', 'console.log(JSON.stringify(process.env))'], {
    env: { CODE_FORGE_RUN_ID: 'r-fixture', CODE_FORGE_OWNER_PID: '1' },
  });
  const env = JSON.parse(result.stdout);
  assert.equal(env.CODE_FORGE_OWNER_PID, String(process.pid));
  assert.equal(env.CODE_FORGE_OWNER_START_TIME, readStartTime(process.pid));
  assert.equal(env.CODE_FORGE_RUN_ID, 'r-fixture');
});
