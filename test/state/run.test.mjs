// helpers FIRST: its import-time guard moves $HOME and cwd to a temp dir before any src module loads.
import { captureStream, countOccurrences, fakeProbe, rowSink, withFixture } from './helpers.mjs';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, readdir, readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { runRun } from '../../src/cli/run.mjs';
import { readAllRows } from '../../src/ledger/write.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { checkWorkerPin, endRun, processStartTime, readRun, reattachWorker, saveRun, startRun, stopPinnedWorker } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';

const ACCEPTANCE = [{ clause: 'c1', tests: ['t1'] }];

test('`run start` writes the record and the 0600 key under the temp $HOME, and the workspace mirror is the record minus `worker`', async () => {
  await withFixture(async ({ home, ws }) => {
    const stdout = captureStream();
    const stderr = captureStream();
    const code = await runRun(['start', '--cwd', ws, '--run', 'r-cli', '--worker-pid', String(process.pid)], { stdout, stderr });
    assert.equal(code, 0, stderr.text);
    assert.equal(stdout.text, `run r-cli started · engine harness · worker pid ${process.pid}\n`);

    const runs = path.join(home, '.code-forge', 'runs');
    assert.deepEqual((await readdir(runs)).sort(), ['r-cli.json', 'r-cli.key']);
    assert.equal((await stat(path.join(runs, 'r-cli.key'))).mode & 0o777, 0o600);

    const authoritative = JSON.parse(await readFile(path.join(runs, 'r-cli.json'), 'utf8'));
    assert.equal(authoritative.worker.pid, process.pid);
    assert.equal(authoritative.project, 'two-blocks');
    const mirrorText = await readFile(path.join(ws, '.code-forge', 'runs', 'r-cli.json'), 'utf8');
    const { worker, ...minusWorker } = authoritative;
    assert.equal(mirrorText, `${JSON.stringify(minusWorker, null, 2)}\n`);
    assert.equal(Object.hasOwn(JSON.parse(mirrorText), 'worker'), false);

    const workspaceFiles = await readdir(path.join(ws, '.code-forge'), { recursive: true });
    assert.deepEqual(workspaceFiles.filter((f) => f.endsWith('.key')), []);
    const keyHex = (await loadKey('r-cli')).toString('hex');
    assert.equal(countOccurrences(stdout.text + stderr.text + mirrorText, keyHex), 0);

    const rows = await readAllRows('two-blocks');
    assert.deepEqual(rows.map((r) => r.event), ['run.start']);
    assert.deepEqual(verifyRow(rows[0], await loadKey('r-cli')), { ok: true });
  });
});

test('B50 `run start --cwd <subfolder>` records the project root as the workspace and the root config\'s slug', async () => {
  await withFixture(async ({ ws }) => {
    const sub = path.join(ws, 'notes', 'deep');
    await mkdir(sub, { recursive: true });
    const stderr = captureStream();
    assert.equal(await runRun(['start', '--cwd', sub, '--run', 'r-sub'], { stdout: captureStream(), stderr }), 0, stderr.text);
    const record = await readRun('r-sub');
    assert.deepEqual([record.workspace, record.project], [ws, 'two-blocks']);
    assert.deepEqual([(await readAllRows('two-blocks')).map((r) => r.event), (await readAllRows('deep')).length], [['run.start'], 0]);
  });
});

test('`run start` without a worker records none and says so; an unknown worker pid is refused', async () => {
  await withFixture(async ({ ws }) => {
    const stderr = captureStream();
    assert.equal(await runRun(['start', '--cwd', ws, '--run', 'r-none'], { stdout: captureStream(), stderr }), 0);
    assert.equal((await readRun('r-none')).worker, null);
    assert.match(stderr.text, /no worker pinned/);
    const refused = captureStream();
    const code = await runRun(['start', '--cwd', ws, '--run', 'r-dead', '--worker-pid', '7'], { stdout: captureStream(), stderr: refused, probe: fakeProbe({}) });
    assert.equal(code, 1);
    assert.equal(refused.text, 'run start: worker pid 7 is not running\n');
    const { writeRow } = rowSink();
    await assert.rejects(() => startRun({ workspace: ws, project: '../evil', runId: 'r-slug', writeRow }), { code: 'bad-project' });
    await assert.rejects(() => readRun('r-slug'), { code: 'no-run' });
  });
});

/** Spawn a sleeper child for `fn`, always SIGKILLed and reaped afterwards (even when fn throws). */
async function withSleeper(/** @type {(pid: number) => Promise<void>} */ fn) {
  const child = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 60000)'], { stdio: 'ignore' });
  try {
    await fn(/** @type {number} */ (child.pid));
  } finally {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGKILL');
      await once(child, 'exit');
    }
  }
}

test('pinned worker: a different LIVE pid or a changed start time ⇒ worker.replaced; a dead pid ⇒ worker.down', async () => {
  await withFixture(async ({ ws }) => {
    const { writeRow } = rowSink();
    const record = await startRun({ workspace: ws, project: 'two-blocks', runId: 'r-pin', workerPid: process.pid, writeRow });
    assert.equal(record.worker.started_at, await processStartTime(process.pid));
    assert.deepEqual(await checkWorkerPin(record), { ok: true });
    await withSleeper(async (otherPid) => {
      assert.notEqual(await processStartTime(otherPid), null); // the other pid is really alive
      assert.equal((await checkWorkerPin(record, { livePid: otherPid })).event, 'worker.replaced');
    });
    const restarted = fakeProbe({ [process.pid]: 'Thu Jan  1 00:00:00 2026' });
    assert.equal((await checkWorkerPin(record, { probe: restarted })).event, 'worker.replaced');
    assert.equal((await checkWorkerPin({ worker: { pid: 0, started_at: 'x' } }, { probe: fakeProbe({ 0: 'x' }) })).event, 'worker.replaced');

    await withSleeper(async (childPid) => {
      const pinned = await reattachWorker({ runId: 'r-pin', workerPid: childPid, writeRow });
      assert.equal((await readRun('r-pin')).worker.pid, childPid);
      process.kill(childPid, 'SIGKILL');
      for (let i = 0; i < 100 && (await processStartTime(childPid)) !== null; i += 1) await new Promise((r) => setTimeout(r, 20));
      assert.equal((await checkWorkerPin(pinned)).event, 'worker.down');
    });
    await assert.rejects(() => reattachWorker({ runId: 'r-pin', workerPid: undefined, writeRow }), { code: 'bad-worker-pid' });
    await assert.rejects(() => reattachWorker({ runId: 'r-pin', workerPid: 1, writeRow }), { code: 'bad-worker-pid' });
    assert.notEqual((await readRun('r-pin')).worker, null);
  });
});

test('`run start` refuses an existing run id and leaves its key and record byte-identical', async () => {
  await withFixture(async ({ home, ws }) => {
    const { writeRow } = rowSink();
    await startRun({ workspace: ws, project: 'two-blocks', runId: 'r-dup', writeRow });
    const runs = path.join(home, '.code-forge', 'runs');
    const keyBefore = await readFile(path.join(runs, 'r-dup.key'));
    const recordBefore = await readFile(path.join(runs, 'r-dup.json'));
    await assert.rejects(() => startRun({ workspace: ws, project: 'two-blocks', runId: 'r-dup', writeRow }), { code: 'run-exists' });
    assert.deepEqual(await readFile(path.join(runs, 'r-dup.key')), keyBefore);
    assert.deepEqual(await readFile(path.join(runs, 'r-dup.json')), recordBefore);
  });
});

test('a workspace that contains ~/.code-forge/runs ($HOME itself) is refused before anything is written', async () => {
  await withFixture(async ({ home }) => {
    const { writeRow } = rowSink();
    await assert.rejects(() => startRun({ workspace: home, project: 'two-blocks', runId: 'r-home', writeRow }), { code: 'workspace-contains-runs-dir' });
    await assert.rejects(() => readRun('r-home'), { code: 'no-run' });
  });
});

test('CLI `run`: an unknown subcommand prints usage and exits 2; a traversal run id and pid 0 are refused', async () => {
  await withFixture(async ({ ws }) => {
    const stderr = captureStream();
    assert.equal(await runRun(['stat', '--run', 'r1'], { stdout: captureStream(), stderr }), 2);
    assert.equal(countOccurrences(stderr.text, 'usage: code-forge run'), 1);
    const traversal = captureStream();
    assert.equal(await runRun(['status', '--run', '../../x'], { stdout: captureStream(), stderr: traversal }), 1);
    assert.match(traversal.text, /^run status: run id must match/);
    const zero = captureStream();
    assert.equal(await runRun(['start', '--cwd', ws, '--run', 'r-zero', '--worker-pid', '0'], { stdout: captureStream(), stderr: zero }), 2);
    assert.equal(zero.text, 'run start: --worker-pid must be a positive integer\n');
  });
});

test('`run end` refuses open blocks, records a failed pin as its own signed row, stops only a verified worker, and refuses a second end', async () => {
  await withFixture(async ({ ws }) => {
    const { rows, writeRow } = rowSink();
    const probe = fakeProbe({ 4242: 'start-A' });
    await startRun({ workspace: ws, project: 'two-blocks', runId: 'r-end', workerPid: 4242, writeRow, probe });
    await openBlock({ runId: 'r-end', id: 'B1', level: 'L1', owned: ['a.txt'], acceptance: ACCEPTANCE, writeRow });
    const stopped = [];
    const stopWorker = async (/** @type {{pid: number}} */ pin) => {
      stopped.push(pin.pid);
      return { stopped: true };
    };
    await assert.rejects(() => endRun({ runId: 'r-end', writeRow, stopWorker, probe }), { code: 'open-blocks' });

    const record = await readRun('r-end');
    record.blocks.B1.status = 'closed';
    await saveRun(record);
    await endRun({ runId: 'r-end', writeRow, stopWorker, probe: fakeProbe({ 4242: 'start-B' }) });
    assert.deepEqual(stopped, []);
    assert.equal((await readRun('r-end')).status, 'ended');
    assert.deepEqual(rows.map((r) => [r.event, r.worker_check, r.worker_stopped]), [
      ['run.start', undefined, undefined],
      ['dispatch', undefined, undefined],
      ['worker.replaced', undefined, undefined],
      ['run.end', 'worker.replaced', false],
    ]);
    const key = await loadKey('r-end');
    assert.deepEqual(rows.map((r) => verifyRow(r, key).ok), [true, true, true, true]);
    await assert.rejects(() => endRun({ runId: 'r-end', writeRow, stopWorker, probe }), { code: 'run-ended' });

    await startRun({ workspace: ws, project: 'two-blocks', runId: 'r-end2', workerPid: 4242, writeRow, probe });
    await endRun({ runId: 'r-end2', writeRow, stopWorker, probe });
    assert.deepEqual(stopped, [4242]);
    assert.equal((await readRun('r-end2')).status, 'ended');
    assert.deepEqual([rows.at(-1).event, rows.at(-1).worker_check, rows.at(-1).worker_stopped], ['run.end', 'ok', true]);
  });
});

test('stopPinnedWorker never signals pid 0, -1, 1 or a recycled pid, and treats ESRCH as already stopped', async () => {
  /** @type {number[]} */
  const killed = [];
  const kill = (/** @type {number} */ pid) => void killed.push(pid);
  for (const pid of [0, -1, 1, 2.5]) {
    assert.deepEqual(await stopPinnedWorker({ pid, started_at: 'S' }, { probe: fakeProbe({ [pid]: 'S' }), kill }), { stopped: false, reason: 'invalid pid' });
  }
  assert.deepEqual(await stopPinnedWorker({ pid: 4242, started_at: 'S' }, { probe: fakeProbe({ 4242: 'T' }), kill }), { stopped: false, reason: 'pid reused' });
  assert.deepEqual(await stopPinnedWorker({ pid: 4242, started_at: 'S' }, { probe: fakeProbe({}), kill }), { stopped: false, reason: 'not running' });
  assert.deepEqual(killed, []);
  const esrch = () => {
    throw Object.assign(new Error('gone'), { code: 'ESRCH' });
  };
  assert.deepEqual(await stopPinnedWorker({ pid: 4242, started_at: 'S' }, { probe: fakeProbe({ 4242: 'S' }), kill: esrch }), { stopped: false, reason: 'already exited' });
  assert.deepEqual(await stopPinnedWorker({ pid: 4242, started_at: 'S' }, { probe: fakeProbe({ 4242: 'S' }), kill }), { stopped: true });
  assert.deepEqual(killed, [4242]);
});

test('`run end` with a pin of 0 in the record (hand-edited) never reaches a kill', async () => {
  await withFixture(async ({ ws }) => {
    const { rows, writeRow } = rowSink();
    await startRun({ workspace: ws, project: 'two-blocks', runId: 'r-zero', writeRow });
    const record = await readRun('r-zero');
    record.worker = { pid: 0, started_at: 'S' };
    await saveRun(record);
    /** @type {number[]} */
    const killed = [];
    const stopWorker = (/** @type {any} */ pin, /** @type {any} */ opts) => stopPinnedWorker(pin, { ...opts, kill: (pid) => void killed.push(pid) });
    await endRun({ runId: 'r-zero', writeRow, stopWorker, probe: fakeProbe({ 0: 'S' }) });
    assert.deepEqual(killed, []);
    assert.equal(rows.at(-1).worker_check, 'worker.replaced');
  });
});
