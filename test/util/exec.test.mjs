import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { setTimeout } from 'node:timers/promises';
import { test } from 'node:test';
import { exec } from '../../src/util/exec.mjs';

const EXEC_URL = new URL('../../src/util/exec.mjs', import.meta.url).href;

/** @param {number} pid */
function isAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    if (err.code === 'ESRCH') return false;
    throw err;
  }
}

/**
 * Poll until `pid` is gone or `withinMs` elapses.
 * @param {number} pid @param {number} withinMs
 */
async function waitForDeath(pid, withinMs) {
  const deadline = Date.now() + withinMs;
  while (Date.now() < deadline) {
    if (!isAlive(pid)) return;
    await setTimeout(25);
  }
}

/**
 * Poll until `file` holds a positive integer pid.
 * @param {string} file @param {number} withinMs
 * @returns {Promise<number>}
 */
async function waitForPidFile(file, withinMs) {
  const deadline = Date.now() + withinMs;
  while (Date.now() < deadline) {
    const pid = Number(await readFile(file, 'utf8').catch(() => ''));
    if (Number.isInteger(pid) && pid > 0) return pid;
    await setTimeout(25);
  }
  throw new Error(`no pid written to ${file} within ${withinMs}ms`);
}

/** Best-effort cleanup so a failing test never leaves a stray process behind. */
function killQuietly(/** @type {number | undefined} */ pid) {
  if (!pid) return;
  try {
    process.kill(pid, 'SIGKILL');
  } catch {
    // already gone
  }
}

test('exec refuses a shell string in place of an argv array', () => {
  assert.throws(
    // @ts-expect-error - deliberately passing a shell string instead of an argv array
    () => exec('git status'),
    { name: 'TypeError', message: /argv must be a non-empty array of strings/ },
  );
});

test('exec refuses shell: true', () => {
  assert.throws(() => exec(['git', 'status'], { shell: true }), {
    name: 'TypeError',
    message: /"shell" option is forbidden/,
  });
});

test('exec refuses a non-positive or non-finite timeoutMs', () => {
  const refusal = { name: 'TypeError', message: /timeoutMs must be a finite number greater than 0/ };
  assert.throws(() => exec(['git', 'status'], { timeoutMs: 0 }), refusal);
  assert.throws(() => exec(['git', 'status'], { timeoutMs: -5 }), refusal);
  assert.throws(() => exec(['git', 'status'], { timeoutMs: Infinity }), refusal);
  assert.throws(
    // @ts-expect-error - deliberately passing a non-numeric timeoutMs
    () => exec(['git', 'status'], { timeoutMs: 'soon' }),
    refusal,
  );
});

test(
  'exec kills a child after timeout_ms with a real signal, and never resolves before the timeout',
  { timeout: 5000 },
  async () => {
    const startedAt = Date.now();
    const result = await exec([process.execPath, '-e', 'setInterval(() => {}, 1000)'], {
      timeoutMs: 200,
    });
    const elapsedMs = Date.now() - startedAt;

    assert.equal(result.result, 'failed');
    assert.equal(result.timedOut, true);
    assert.ok(
      result.signal === 'SIGTERM' || result.signal === 'SIGKILL',
      `expected a kill signal, got ${result.signal}`,
    );
    assert.ok(elapsedMs >= 200, `resolved too early: ${elapsedMs}ms`);
    assert.ok(elapsedMs < 4000, `resolved too late (kill likely didn't work): ${elapsedMs}ms`);
  },
);

/**
 * A direct child that spawns a grandchild (which writes its pid to `pidFile`) and then idles.
 * @param {string} pidFile
 * @param {boolean} grandchildTrapsSigterm
 */
function childWithGrandchild(pidFile, grandchildTrapsSigterm) {
  const grandchildCode = `${grandchildTrapsSigterm ? "process.on('SIGTERM', () => {});" : ''} setInterval(() => {}, 1000);`;
  return `
    const { spawn } = require('node:child_process');
    const fs = require('node:fs');
    const grandchild = spawn(process.execPath, ['-e', ${JSON.stringify(grandchildCode)}], { stdio: 'inherit' });
    fs.writeFileSync(${JSON.stringify(pidFile)}, String(grandchild.pid));
    setInterval(() => {}, 1000);
  `;
}

for (const trapsSigterm of [false, true]) {
  test(
    `exec kills the whole process group on timeout — a grandchild ${trapsSigterm ? 'that TRAPS SIGTERM ' : ''}is dead, not orphaned`,
    { timeout: 8000 },
    async () => {
      const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-exec-'));
      const pidFile = path.join(dir, 'grandchild.pid');
      let grandchildPid;
      try {
        // 1500 ms leaves the direct child ample time to start node and write the pid file even
        // on a loaded runner, so the test measures the group kill, not a startup race.
        const pending = exec([process.execPath, '-e', childWithGrandchild(pidFile, trapsSigterm)], { timeoutMs: 1500 });
        grandchildPid = await waitForPidFile(pidFile, 1400);
        const result = await pending;

        assert.equal(result.result, 'failed');
        assert.equal(result.timedOut, true);
        // Well inside the 2000 ms SIGTERM→SIGKILL grace: a trapped SIGTERM must not buy time.
        await waitForDeath(grandchildPid, 500);
        assert.equal(isAlive(grandchildPid), false, 'the grandchild process is still alive');
      } finally {
        killQuietly(grandchildPid);
        await rm(dir, { recursive: true, force: true });
      }
    },
  );
}

/**
 * Run a wrapper process that `exec`s a long-running child (pid → `pidFile`), then applies
 * `afterChildUp` inside the wrapper once the child is up. Returns the wrapper process plus
 * promises for its first stdout line (`up`) and its `exit` event.
 * @param {string} pidFile
 * @param {string} afterChildUp - wrapper-side code to run once the child pid file exists.
 */
function runWrapper(pidFile, afterChildUp) {
  const childCode = `require('node:fs').writeFileSync(${JSON.stringify(pidFile)}, String(process.pid)); setInterval(() => {}, 1000);`;
  const wrapper = `
    import { exec } from ${JSON.stringify(EXEC_URL)};
    import { readFileSync } from 'node:fs';
    exec([process.execPath, '-e', ${JSON.stringify(childCode)}]);
    const poll = setInterval(() => {
      let text = '';
      try { text = readFileSync(${JSON.stringify(pidFile)}, 'utf8'); } catch {}
      if (text) { clearInterval(poll); process.stdout.write('up\\n'); ${afterChildUp} }
    }, 20);
  `;
  const proc = spawn(process.execPath, ['--input-type=module', '-e', wrapper], { stdio: ['ignore', 'pipe', 'pipe'] });
  let stderr = '';
  proc.stderr.on('data', (d) => {
    stderr += d;
  });
  // Subscribed at spawn time: a wrapper that exits quickly must not emit 'exit' before anyone listens.
  const exited = once(proc, 'exit');
  const up = once(proc.stdout, 'data');
  return { proc, exited, up, stderrText: () => stderr };
}

for (const signal of /** @type {const} */ (['SIGTERM', 'SIGINT', 'SIGHUP'])) {
  test(`a ${signal} to the parent reaches the detached child group, and the parent still dies of ${signal}`, { timeout: 8000 }, async () => {
    const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-exec-sig-'));
    const pidFile = path.join(dir, 'child.pid');
    let childPid;
    try {
      const { proc, exited, up, stderrText } = runWrapper(pidFile, '');
      childPid = await waitForPidFile(pidFile, 4000);
      // Wait for the wrapper to observe the child too, so exec's handlers are certainly installed.
      await up;
      proc.kill(signal);
      const [code, exitSignal] = await exited;

      assert.equal(exitSignal, signal, `wrapper exited with code ${code}, signal ${exitSignal}; stderr: ${stderrText()}`);
      await waitForDeath(childPid, 1500);
      assert.equal(isAlive(childPid), false, `the child survived the parent's ${signal} as an orphan`);
    } finally {
      killQuietly(childPid);
      await rm(dir, { recursive: true, force: true });
    }
  });
}

test('a parent that exits while a child runs takes the child group down with it', { timeout: 8000 }, async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-exec-exit-'));
  const pidFile = path.join(dir, 'child.pid');
  let childPid;
  try {
    const { exited, stderrText } = runWrapper(pidFile, 'process.exit(7);');
    childPid = await waitForPidFile(pidFile, 4000);
    const [code] = await exited;

    assert.equal(code, 7, stderrText());
    await waitForDeath(childPid, 1500);
    assert.equal(isAlive(childPid), false, 'the child survived the parent exiting');
  } finally {
    killQuietly(childPid);
    await rm(dir, { recursive: true, force: true });
  }
});

test('exec installs its signal/exit handlers only while a child is live', { timeout: 3000 }, async () => {
  const before = ['SIGINT', 'SIGTERM', 'SIGHUP', 'exit'].map((e) => process.listenerCount(e));
  const pending = exec([process.execPath, '-e', 'setTimeout(() => {}, 200)'], { timeoutMs: 2000 });
  const during = ['SIGINT', 'SIGTERM', 'SIGHUP', 'exit'].map((e) => process.listenerCount(e));
  await pending;
  const after = ['SIGINT', 'SIGTERM', 'SIGHUP', 'exit'].map((e) => process.listenerCount(e));

  assert.deepEqual(during, before.map((n) => n + 1));
  assert.deepEqual(after, before);
});

test('exec ignores stdin by default so a child reading stdin sees an immediate EOF, not a hang', { timeout: 3000 }, async () => {
  const script = `
    process.stdin.resume();
    process.stdin.on('end', () => { process.stdout.write('eof'); process.exit(0); });
  `;
  const result = await exec([process.execPath, '-e', script], { timeoutMs: 2000 });

  assert.equal(result.result, 'ok');
  assert.equal(result.stdout, 'eof');
  assert.equal(result.timedOut, false);
});

test('exec collects multi-byte UTF-8 output correctly even when split across chunks', { timeout: 3000 }, async () => {
  // Write 'café ☃' (é = 2 bytes, ☃ = 3 bytes) one byte at a time so characters straddle
  // separate 'data' events.
  const script = `
    const bytes = Buffer.from('café ☃', 'utf8');
    let i = 0;
    const timer = setInterval(() => {
      if (i >= bytes.length) { clearInterval(timer); process.exit(0); return; }
      process.stdout.write(bytes.subarray(i, i + 1));
      i += 1;
    }, 1);
  `;
  const result = await exec([process.execPath, '-e', script], { timeoutMs: 2000 });

  assert.equal(result.result, 'ok');
  assert.equal(result.stdout, 'café ☃');
});

test('exec kills a child that writes past maxBufferBytes, keeps exactly the cap, and reports it as failed', { timeout: 5000 }, async () => {
  const script = `
    const chunk = Buffer.alloc(1024, 'x');
    setInterval(() => { process.stdout.write(chunk); }, 1);
  `;
  const result = await exec([process.execPath, '-e', script], {
    timeoutMs: 4000,
    maxBufferBytes: 4096,
  });

  assert.equal(result.result, 'failed');
  // Exact: pipe reads can merge several 1 KB writes into one event, and exec cuts at the cap.
  assert.equal(result.stdout.length, 4096);
  assert.equal(result.timedOut, false);
  assert.equal(result.error, 'exec: maxBufferBytes exceeded');
});
