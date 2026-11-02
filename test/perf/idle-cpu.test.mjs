import { BIN, BLOCK, childEnv, makeRepo, startWorker, stopWorker } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { setTimeout as sleep } from 'node:timers/promises';
import { test } from 'node:test';
import { announceWorker, retractWorker } from '../../src/worker/queue.mjs';

function cpuSeconds(pid) {
  const value = execFileSync('/bin/ps', ['-p', String(pid), '-o', 'time='], { encoding: 'utf8' }).trim();
  const parts = value.split(':').map(Number);
  assert.ok(parts.length >= 2 && parts.every(Number.isFinite), 'valid CPU time for pid ' + pid + ': ' + value);
  return parts.reduce((sum, part) => sum * 60 + part, 0);
}

// Included by the full suite's test/**/*.test.mjs glob; no opt-in performance flag.
test('real idle worker and real wait-block waiter each stay below 3% of one core', { timeout: 60000 }, async (t) => {
  const idle = await makeRepo();
  const waiting = await makeRepo();
  const worker = await startWorker(idle.repo, idle.runId, { CODE_FORGE_NATIVE: '0' });
  // The waiter has pending files in a separate queue served by a live stand-in. This keeps
  // it waiting without sending model work to the idle worker under measurement.
  announceWorker(waiting.repo, { pid: process.pid, run: waiting.runId });
  const waiter = spawn(process.execPath, [BIN, 'review-file', '--wait-block', BLOCK, '--run', waiting.runId, '--max', '50s'], {
    cwd: waiting.repo, env: childEnv({ CODE_FORGE_NATIVE: '0' }), stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  waiter.stdout.on('data', (chunk) => { output += chunk; });
  waiter.stderr.on('data', (chunk) => { output += chunk; });
  const exited = once(waiter, 'exit');
  try {
    await sleep(3000); // startup, initial git scans and scheduler backoff are outside the idle window
    assert.equal(waiter.exitCode, null, output);
    assert.equal(worker.child.exitCode, null, worker.stderr());
    const before = [cpuSeconds(worker.pid), cpuSeconds(waiter.pid)];
    const start = performance.now();
    await sleep(30000);
    const seconds = (performance.now() - start) / 1000;
    assert.equal(waiter.exitCode, null, 'waiter remains pending throughout the sample');
    assert.equal(worker.child.exitCode, null, 'worker stays alive throughout the sample');
    for (const [i, [label, pid]] of [['worker', worker.pid], ['waiter', waiter.pid]].entries()) {
      const cpu = cpuSeconds(pid) - before[i];
      const percent = cpu / seconds * 100;
      t.diagnostic(`${label}: ${cpu.toFixed(3)} CPU seconds / ${seconds.toFixed(3)} wall seconds = ${percent.toFixed(2)}%`);
      assert.ok(percent < 3, label + ' uses ' + percent.toFixed(2) + '%, must be below 3% of one core');
    }
  } finally {
    waiter.kill('SIGTERM');
    await exited;
    retractWorker(waiting.repo, process.pid);
    await stopWorker(worker, idle.runId);
  }
});
