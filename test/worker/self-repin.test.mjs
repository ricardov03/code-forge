// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { cli, makeRepo, startWorker, stopWorker, waitFor } from './helpers.mjs';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { after, describe, test } from 'node:test';
import { loadedMs } from '../helpers/load.mjs';

const { checkWorkerPin, readRun, reattachWorker } = await import('../../src/state/run.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');

const SLUG = 'worker-test'; // the fixture project's ledger slug (helpers.mjs CONFIG)

/** @type {Array<import('node:child_process').ChildProcess>} */
const sleepers = [];
after(() => {
  for (const child of sleepers) if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
});

/** A plain sleeping process (not a worker): pinned to the run as if it were the old worker. */
async function sleeper() {
  const child = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 600000)'], { stdio: 'ignore', shell: false });
  sleepers.push(child);
  await new Promise((resolve, reject) => {
    child.once('spawn', resolve);
    child.once('error', reject);
  });
  return child;
}

/** @param {import('node:child_process').ChildProcess} child */
async function killAndReap(child) {
  const exited = new Promise((resolve) => child.once('exit', resolve));
  child.kill('SIGKILL');
  await exited; // reaped: `ps -p` no longer lists it (no zombie that still has a start time)
}

/** Pin `pid` as the run's only worker, through the same state call `run start --reattach` uses. */
async function pin(runId, pid) {
  await reattachWorker({ runId, workerPid: pid, writeRow: async () => {} });
}

/** @param {string} runId */
async function reattachRows(runId) {
  return (await readAllRows(SLUG)).filter((row) => row.event === 'worker.reattach' && row.run === runId);
}

describe('worker --run re-pins itself when the pinned worker is dead (B355)', () => {
  test('pinned pid is dead: the new worker pins itself, writes one worker.reattach row and one stderr line; the pin check accepts it', { timeout: loadedMs(90_000) }, async () => {
    const { repo, runId } = await makeRepo();
    const old = await sleeper();
    const oldPid = /** @type {number} */ (old.pid);
    await pin(runId, oldPid);
    await killAndReap(old);
    assert.equal((await checkWorkerPin(await readRun(runId))).ok, false); // the dead pin blocks a close today

    const w = await startWorker(repo, runId);
    try {
      const line = `worker: re-pinned run ${runId} from dead pid ${oldPid} to ${w.pid}\n`;
      await waitFor(() => w.stderr().includes(line), loadedMs(10_000));
      const record = await readRun(runId);
      assert.equal(record.worker?.pid, w.pid);
      assert.deepEqual(record.workers.map((/** @type {{pid: number}} */ p) => p.pid), [w.pid]);
      assert.deepEqual((record.worker_history ?? []).map((/** @type {{pid: number}} */ p) => p.pid), [oldPid]);
      const rows = await reattachRows(runId);
      assert.deepEqual(rows.map((row) => row.pid), [w.pid]);
      assert.equal(w.stderr().split(line).length - 1, 1);
      assert.equal(w.stderr().split('re-pinned').length - 1, 1);
      assert.deepEqual(await checkWorkerPin(record, { livePid: w.pid }), { ok: true });
    } finally {
      await stopWorker(w, runId);
    }
  });

  test('pinned pid is alive (not a worker of this queue): worker refuses with exit 1 and the pin is unchanged', { timeout: loadedMs(60_000) }, async () => {
    const { repo, runId } = await makeRepo();
    const live = await sleeper();
    const livePid = /** @type {number} */ (live.pid);
    await pin(runId, livePid);
    const before = (await readRun(runId)).worker;

    const res = await cli(['worker', '--run', runId, '--once'], repo);
    assert.equal(res.code, 1, res.stderr);
    assert.equal(res.stderr, `worker: another worker (pid ${livePid}) is pinned to run ${runId} and still alive\n`);
    const record = await readRun(runId);
    assert.deepEqual(record.worker, before);
    assert.deepEqual(record.workers.map((/** @type {{pid: number}} */ p) => p.pid), [livePid]);
    assert.equal((await reattachRows(runId)).length, 0);
    await killAndReap(live);
  });

  test('no pin: the worker boots as before and pins nothing', { timeout: loadedMs(60_000) }, async () => {
    const { repo, runId } = await makeRepo();
    const res = await cli(['worker', '--run', runId, '--once'], repo);
    assert.equal(res.code, 0, res.stderr);
    assert.equal(res.stderr, 'worker: drained 0 ticket(s)\n');
    const record = await readRun(runId);
    assert.equal(record.worker ?? null, null);
    assert.equal((record.workers ?? []).length, 0);
    assert.equal((await reattachRows(runId)).length, 0);
  });
});
