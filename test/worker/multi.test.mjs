import { BLOCK, cli, freshDir, makeRepo, stopPidAfter, waitFor } from './helpers.mjs';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { runRun } from '../../src/cli/run.mjs';
import { createKeyStore } from '../../src/keys/store.mjs';
import { closeBlock } from '../../src/state/block.mjs';
import { endRun, processStartTime, readRun, saveRun, stopPinnedWorker, writeSigned } from '../../src/state/run.mjs';
import { loadKey, signRow, verifyRow } from '../../src/state/signer.mjs';
import { createWorker } from '../../src/worker/loop.mjs';
import { enqueue, readResult, workerRegistry } from '../../src/worker/queue.mjs';
import { readStartTime } from '../../src/util/reaper.mjs';

function gate() {
  let open;
  const promise = new Promise((resolve) => { open = resolve; });
  return { promise, open: () => open() };
}

test('B107: add-worker stops its launched child when the initial probe throws and adds no pin', async () => {
  const { repo, runId } = await makeRepo();
  const config = path.join(repo, '.code-forge.yml');
  writeFileSync(config, readFileSync(config, 'utf8').replace('review:', 'review:\n  workers: 2'));
  const original = await readRun(runId);
  original.worker = { pid: process.pid, started_at: await processStartTime(process.pid) };
  original.workers = [original.worker];
  await saveRun(original);
  const child = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 60000)'], { stdio: 'ignore' });
  const exited = once(child, 'exit');
  const pid = /** @type {number} */ (child.pid);
  const stops = [];
  let launched = false, stderr = '', stdout = '';
  try {
    const code = await runRun(['start', '--add-worker', '--run', runId], {
      stdout: { write: (s) => { stdout += s; } },
      stderr: { write: (s) => { stderr += s; } },
      startWorker: async () => { launched = true; return { pid, repoRoot: repo }; },
      probe: async (probedPid) => {
        assert.equal(launched, true, 'probe runs after launch');
        assert.equal(probedPid, pid);
        throw new Error('initial worker probe failed');
      },
      stopWorker: async (pin, opts) => { stops.push(pin.pid); return stopPinnedWorker(pin, opts); },
    });
    assert.equal(code, 1);
    assert.match(stderr, /initial worker probe failed/);
    assert.equal(stdout, '');
    const record = await readRun(runId);
    assert.deepEqual(record.workers, original.workers, 'no new pin is added');
    assert.deepEqual(record.worker, original.worker, 'the existing pin is preserved');
    assert.deepEqual(stops, [pid], 'cleanup stops exactly the launched pid');
    assert.equal(await waitFor(() => child.exitCode !== null || child.signalCode !== null, 3000), true, 'launched child actually exits');
    assert.match(stderr, /not pinned; stopped it/);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    await exited;
  }
});

test('B107: add-worker pins another active worker, preserves first pin, and names capacity', async () => {
  const { repo, runId } = await makeRepo({ start: false });
  const config = path.join(repo, '.code-forge.yml');
  writeFileSync(config, readFileSync(config, 'utf8').replace('review:', 'review:\n  workers: 2'));
  const first = await cli(['run', 'start', '--run', runId], repo);
  assert.equal(first.code, 0, first.stderr);
  const original = await readRun(runId);
  assert.deepEqual(original.workers, [original.worker], 'start records exactly its first pin');
  const added = await cli(['run', 'start', '--add-worker', '--run', runId], repo);
  for (const entry of workerRegistry(repo)) if (entry.worker) stopPidAfter(entry.worker.pid, runId);
  assert.equal(added.code, 0, added.stderr);
  const record = await readRun(runId);
  assert.equal(record.workers.length, 2, 'two pins stored');
  assert.deepEqual(record.worker, original.worker, 'legacy pin stays first');
  assert.deepEqual(record.workers[0], original.worker, 'workers first pin stays first');
  assert.notEqual(record.workers[1].pid, original.worker.pid, 'second worker is a distinct process');
  const full = await cli(['run', 'start', '--add-worker', '--run', runId], repo);
  assert.equal(full.code, 1, 'capacity refuses');
  assert.match(full.stderr, /review.workers.*2/, 'capacity names configured limit');
  assert.deepEqual((await readRun(runId)).workers, record.workers, 'refusal changes no pins');
  const missing = await cli(['run', 'start', '--add-worker'], repo);
  assert.equal(missing.code, 2, 'add-worker needs a run id');
  const stopped = [];
  await endRun({ runId, writeRow: async () => {}, stopWorker: async (pin) => { stopped.push(pin); return { stopped: true }; } });
  assert.deepEqual(stopped, record.workers, 'run end stops each pinned worker');
  const ended = await cli(['run', 'start', '--add-worker', '--run', runId], repo);
  assert.equal(ended.code, 1, 'cannot add to ended run');
  assert.match(ended.stderr, /has ended/);
});

test('B107: two worker loops claim each ticket once, both take work, and signed results name their worker', async () => {
  const { repo, runId } = await makeRepo();
  const key = await loadKey(runId);
  const rows = [], calls = [];
  const entered = gate(), finish = gate();
  const workers = [];
  const make = async (name) => createWorker({ runId, repoRoot: repo, cfg: { review: { parallel_tickets: 1 } }, runRootDir: freshDir(name), slug: 'worker-test', key }, {
    store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, expiryCheck: async () => {},
    writeRow: async (row) => { rows.push(row); },
    review: async (ticket) => {
      calls.push([name, ticket.file]);
      if (name === 'a') { entered.open(); await finish.promise; }
      return { status: 'reviewed', approved: true, engine: 'test', sessions: [] };
    },
  });
  const a = await make('a'), b = await make('b'); workers.push(a, b);
  const first = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' }).ticket;
  const draining = a.drain();
  try {
    await entered.promise;
    const second = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/b.mjs' }).ticket;
    assert.equal(await b.drain(), 1, 'contender skips held ticket and completes the other');
    finish.open();
    assert.equal(await draining, 1, 'owner completes only its ticket');
    assert.deepEqual(calls, [['a', 'src/a.mjs'], ['b', 'src/b.mjs']], 'one review per ticket, one per loop');
    assert.equal(await b.drain(), 0, 'completed tickets never rerun');
    const expected = { pid: process.pid, started_at: readStartTime(process.pid) };
    for (const ticket of [first, second]) {
      const result = readResult(repo, runId, ticket);
      assert.deepEqual(result.worker, expected, `${ticket} result names actual process identity`);
      assert.equal(verifyRow(result, key).ok, true, 'result identity is signed');
    }
    const results = rows.filter((row) => row.event === 'review.result');
    assert.equal(results.length, 2, 'exactly two ledger results');
    for (const row of results) { assert.deepEqual(row.worker, expected); assert.equal(verifyRow(row, key).ok, true); }
  } finally { finish.open(); await draining; workers.forEach((w) => w.release()); }
});

test('B107: block pin check requires pinned result identities, a live pin and no dead pinned ticket claim', async () => {
  const { repo, runId } = await makeRepo();
  const live = { pid: 4242, started_at: 'live' }, dead = { pid: 4243, started_at: 'dead' };
  const record = await readRun(runId);
  record.worker = live; record.workers = [live, dead]; await saveRun(record);
  const rows = [];
  await writeSigned(runId, async (row) => { rows.push(row); }, { event: 'dispatch', block: BLOCK });
  const key = await loadKey(runId);
  const review = (worker) => signRow({ run: runId, block: BLOCK, event: 'review.result', status: 'reviewed', approved: true, worker }, key);
  const close = (resultRows, probe = async (pid) => pid === live.pid ? live.started_at : null) => closeBlock({ runId, id: BLOCK, rows: [...rows, ...resultRows], writeRow: async () => {}, probe, dryRun: true });
  assert.equal((await close([review(dead)])).ok, true, 'dead pinned result accepted while another pin is live');
  for (const worker of [null, { pid: 9999, started_at: 'foreign' }, { ...live, started_at: 'recycled' }]) {
    assert.equal((await close([review(worker)])).event, 'worker.replaced', `unmatched result ${JSON.stringify(worker)} refuses`);
  }
  assert.equal((await close([review(live)], async () => null)).event, 'worker.down', 'all pins dead refuses');
  const ticket = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' }).ticket;
  const claimKey = `ticket\0${runId}\0${ticket}`;
  const locks = path.join(os.homedir(), '.code-forge', 'locks'); mkdirSync(locks, { recursive: true });
  const claim = path.join(locks, `${createHash('sha256').update(claimKey).digest('hex')}.lock`);
  try {
    writeFileSync(claim, JSON.stringify({ key: claimKey, pid: dead.pid, start: dead.started_at }));
    assert.equal((await close([review(live)])).event, 'worker.down', 'dead pin holding this block ticket refuses despite live peer');
    writeFileSync(path.join(repo, '.code-forge', 'queue', `${ticket}.done`), '{}');
    assert.equal((await close([review(live)])).event, 'worker.down', 'a published result does not hide a dead pinned claim');
    writeFileSync(claim, JSON.stringify({ key: claimKey, pid: live.pid, start: live.started_at }));
    assert.equal((await close([review(live)])).ok, true, 'live pin claim passes');
  } finally { rmSync(claim, { force: true }); }
  record.workers = [live]; await saveRun(record);
  assert.equal((await close([review(live)])).ok, true, 'one worker accepts its result');
  assert.equal((await close([review(null)])).event, 'worker.replaced', 'one worker also rejects an invalid result identity');
});

test('B107: a legacy result without a worker field closes with one live pinned worker', async () => {
  const { runId } = await makeRepo();
  const worker = { pid: 4242, started_at: 'live' };
  const record = await readRun(runId);
  record.worker = worker; record.workers = [worker]; await saveRun(record);
  const rows = [];
  const writeRow = async (row) => { rows.push(row); };
  await writeSigned(runId, writeRow, { event: 'dispatch', block: BLOCK });
  await writeSigned(runId, writeRow, { event: 'review.result', block: BLOCK, status: 'reviewed', approved: true });
  assert.equal(Object.hasOwn(rows.at(-1), 'worker'), false, 'legacy result omits worker');
  const close = (opts = {}) => closeBlock({ runId, id: BLOCK, rows, writeRow, probe: async (pid) => pid === worker.pid ? worker.started_at : null, ...opts });
  assert.equal((await close({ livePid: 9999, dryRun: true })).event, 'worker.replaced', 'legacy live-pid mismatch still refuses');
  assert.equal((await close({ probe: async () => null, dryRun: true })).event, 'worker.down', 'legacy dead pin still refuses');
  assert.deepEqual(await close(), { ok: true, status: 'closed' });
  assert.equal((await readRun(runId)).blocks[BLOCK].status, 'closed');
  assert.equal(rows.at(-1).event, 'block.close');
});
