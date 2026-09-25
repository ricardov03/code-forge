import { alive, cli, FAKE_JEV_KEY, FAKE_SNEAKY, freshDir, makeRepo, queued, records, runRootOf, startWorker, stopPid, stopPidAfter, stopWorker, waitFor } from './helpers.mjs';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { existsSync, readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { announceWorker, enqueue, liveWorker, readResult, verifyResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { readRun, stopPinnedWorker } = await import('../../src/state/run.mjs');
const { runRun } = await import('../../src/cli/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

describe('a long review session (the fake sleeps 3 s; the coder waits 1 s at a time)', () => {
  test('the coder-side wait ends first; the ticket still ends done; 0 tickets lost', async () => {
    // The plan's 150 s is scaled to 3 s through the fake's FAKE_SLEEP_MS knob; what is shown is the
    // same: the session outlives the coder's `--wait --max`, and only the worker's own session
    // timeout (review.session_timeout_s = 60 in the fixture) could end it.
    const { repo, runId } = await makeRepo();
    const recordDir = freshDir('records');
    const w = await startWorker(repo, runId, { FAKE_RECORD: recordDir, FAKE_SLEEP_MS: '3000' });
    try {
      const a = await cli(['review-file', 'src/a.mjs', '--block', 'B11'], repo);
      const b = await cli(['review-file', 'src/b.mjs', '--block', 'B11'], repo);
      assert.deepEqual([a.json.status, b.json.status], ['queued', 'queued']);
      const short = await cli(['review-file', '--wait', a.json.ticket, '--max', '1s'], repo);
      assert.deepEqual([short.code, short.json.status], [0, 'pending']);
      const doneA = await cli(['review-file', '--wait', a.json.ticket, '--max', '30s'], repo);
      const doneB = await cli(['review-file', '--wait', b.json.ticket, '--max', '30s'], repo);
      assert.deepEqual([doneA.json.status, doneA.json.result.status, doneA.json.result.file], ['done', 'reviewed', 'src/a.mjs']);
      assert.deepEqual([doneB.json.status, doneB.json.result.status], ['done', 'reviewed']);
      assert.deepEqual(queued(repo, 'done'), queued(repo, 'json'));
      assert.equal(queued(repo, 'json').length, 2);
    } finally {
      await stopWorker(w, runId);
    }
  });
});

describe('the sessions the worker spawns', () => {
  test('the env passed to the fake CLI has 0 key variables', async () => {
    const { repo, runId } = await makeRepo();
    const recordDir = freshDir('records');
    const w = await startWorker(repo, runId, { FAKE_RECORD: recordDir, CODE_FORGE_KEY_JEV: FAKE_JEV_KEY, JEV_API_KEY: FAKE_JEV_KEY, SNEAKY_TOKEN: FAKE_SNEAKY });
    try {
      const a = await cli(['review-file', 'src/a.mjs', '--block', 'B11'], repo);
      assert.equal((await cli(['review-file', '--wait', a.json.ticket, '--max', '30s'], repo)).json.status, 'done');
      const recs = records(recordDir);
      assert.equal(recs.length, 1);
      const names = recs[0].env_keys;
      const keyVars = names.filter((/** @type {string} */ n) => /^(CODE_FORGE_|JEV_)/.test(n) || /(_TOKEN|_KEY|_SECRET)$/.test(n));
      assert.deepEqual(keyVars, []);
      assert.equal(names.includes('SNEAKY_TOKEN'), false);
      assert.deepEqual(recs[0].fake_valued_keys, []); // no variable, whatever its name, carries a fake secret
      assert.equal(names.includes('FAKE_RECORD'), true); // the env did reach the fake
    } finally {
      await stopWorker(w, runId);
    }
  });

  test('every session has a pid-registry entry while alive, and none after', async () => {
    const { repo, runId } = await makeRepo();
    const recordDir = freshDir('records');
    const w = await startWorker(repo, runId, { FAKE_RECORD: recordDir, FAKE_SLEEP_MS: '2000' });
    try {
      const a = await cli(['review-file', 'src/a.mjs', '--block', 'B11'], repo);
      assert.ok(await waitFor(() => records(recordDir).length === 1));
      const { pid } = records(recordDir)[0];
      const entry = path.join(runRootOf(runId), 'pids', `${pid}.json`);
      assert.equal(alive(pid), true);
      assert.equal(JSON.parse(readFileSync(entry, 'utf8')).pid, pid);
      assert.equal((await cli(['review-file', '--wait', a.json.ticket, '--max', '30s'], repo)).json.status, 'done');
      assert.ok(await waitFor(() => !existsSync(entry), 3000));
    } finally {
      await stopWorker(w, runId);
    }
  });
});

describe('worker restart', () => {
  test('a new worker re-reads 3 queued tickets and completes them; run start --reattach re-pins its pid', async () => {
    const { repo, runId } = await makeRepo();
    const recordDir = freshDir('records');
    const first = await startWorker(repo, runId, { FAKE_RECORD: recordDir, FAKE_SLEEP_MS: '5000' });
    assert.equal((await cli(['run', 'start', '--reattach', '--run', runId], repo)).code, 0);
    assert.equal((await readRun(runId)).worker.pid, first.pid);
    const tickets = [];
    for (const f of ['a', 'b', 'c']) tickets.push((await cli(['review-file', `src/${f}.mjs`, '--block', 'B11'], repo)).json.ticket);
    assert.ok(await waitFor(() => records(recordDir).length === 1)); // ticket 1 is in flight
    const inFlight = records(recordDir)[0].pid;
    await stopWorker(first, runId);
    assert.equal(alive(inFlight), false);
    assert.deepEqual(queued(repo, 'done'), []); // the killed session was abandoned, not recorded

    const second = await startWorker(repo, runId, { FAKE_RECORD: recordDir });
    try {
      assert.ok(await waitFor(() => queued(repo, 'done').length === 3, 20000));
      assert.deepEqual(queued(repo, 'done'), [...tickets].sort());
      for (const t of tickets) assert.equal((await verifyResult(repo, runId, t)).ok, true);

      assert.equal((await readRun(runId)).worker.pid, first.pid); // restarting alone never re-pins
      const reattach = await cli(['run', 'start', '--reattach', '--run', runId], repo);
      assert.equal(reattach.code, 0, reattach.stderr);
      assert.equal(reattach.stdout, `run ${runId}: worker re-pinned to pid ${second.pid}\n`);
      assert.equal((await readRun(runId)).worker.pid, second.pid);
    } finally {
      await stopWorker(second, runId);
    }
  });
});

describe('keys inside the worker (§8.1)', () => {
  test('the Jev key is resolved through B2 chain once per worker: the mock backend is called exactly once', async () => {
    const { repo, runId } = await makeRepo();
    /** @type {string[]} */
    const calls = [];
    const mem = {
      name: 'mock',
      writable: true,
      available: async () => true,
      get: async (/** @type {string} */ name) => {
        calls.push(`get:${name}`);
        return name === 'jev' ? FAKE_JEV_KEY : null;
      },
      set: async () => {},
      delete: async () => false,
    };
    const store = await createKeyStore({ backends: [mem], dir: freshDir('store') });
    /** @type {Array<string | null>} */
    const seen = [];
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    const key = await loadKey(runId);
    const worker = await createWorker(
      { runId, repoRoot: repo, cfg: {}, runRootDir: freshDir('runroot'), slug: 'worker-test', key },
      {
        store,
        env: { PATH: process.env.PATH ?? '' },
        writeRow: async (row) => rows.push(row),
        review: async (_ticket, ctx) => {
          seen.push(ctx.jevKey);
          return { status: 'reviewed', approved: false, engine: 'test', sessions: [] };
        },
      },
    );
    enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/b.mjs' });
    assert.equal(await worker.drain(), 2);
    assert.deepEqual(calls, ['get:jev']);
    assert.deepEqual(seen, [FAKE_JEV_KEY, FAKE_JEV_KEY]);
    assert.equal(rows.length, 2);
    assert.equal(rows.every((r) => verifyRow(r, key).ok), true);
    assert.equal(JSON.stringify(rows).includes(FAKE_JEV_KEY), false);
  });
});

describe('worker lifecycle', () => {
  test('worker --once releases the queue: a second --once on the same run succeeds', async () => {
    const { repo, runId } = await makeRepo();
    const a = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    const first = await cli(['worker', '--run', runId, '--once'], repo);
    assert.equal(first.code, 0, first.stderr);
    assert.equal(liveWorker(repo), null);
    const b = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/b.mjs' });
    const second = await cli(['worker', '--run', runId, '--once'], repo);
    assert.equal(second.code, 0, second.stderr);
    assert.match(second.stderr, /drained 1 ticket/);
    assert.deepEqual(queued(repo, 'done'), [a.ticket, b.ticket].sort());
  });

  test('a file deleted after enqueue gets a signed stale/missing-file result; the next ticket is still served', async () => {
    const { repo, runId } = await makeRepo();
    const key = await loadKey(runId);
    const worker = await createWorker(
      { runId, repoRoot: repo, cfg: {}, runRootDir: freshDir('runroot'), slug: 'worker-test', key },
      { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow: async () => {}, review: async () => ({ status: 'reviewed', approved: false, engine: 'test', sessions: [] }) },
    );
    const a = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    rmSync(path.join(repo, 'src', 'a.mjs'));
    const b = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/b.mjs' });
    assert.equal(await worker.drain(), 2);
    const ra = readResult(repo, runId, a.ticket);
    assert.deepEqual([ra.status, ra.reason], ['stale', 'missing-file']);
    assert.equal((await verifyResult(repo, runId, a.ticket)).ok, true);
    assert.equal(readResult(repo, runId, b.ticket).status, 'reviewed');
  });

  test('run start (CLI) leaves a live worker whose pid is pinned in the run record', async () => {
    const { repo, runId } = await makeRepo({ start: false });
    const res = await cli(['run', 'start', '--run', runId], repo);
    const live = liveWorker(repo);
    if (live) stopPidAfter(live.pid, runId);
    assert.equal(res.code, 0, res.stderr);
    assert.notEqual(live, null);
    assert.equal(alive(live.pid), true);
    assert.equal((await readRun(runId)).worker.pid, live.pid);
    assert.equal(res.stdout, `run ${runId} started · engine auto · worker pid ${live.pid}\n`);
    await stopPid(live.pid, runId);
    assert.equal(alive(live.pid), false);
  });

  /**
   * A live child that never writes `worker.json` (stands in for a launched worker that never
   * announces itself). SIGKILLed by the returned `kill` in the caller's `finally`.
   */
  function sleepingChild() {
    const child = spawn('sleep', ['60'], { stdio: 'ignore', shell: false });
    const pid = /** @type {number} */ (child.pid);
    const exited = new Promise((resolve) => child.on('exit', () => resolve(true)));
    const kill = () => {
      try {
        process.kill(pid, 'SIGKILL');
      } catch {
        // already gone
      }
    };
    return { pid, exited, kill };
  }

  /**
   * `run start` in-process with `startWorker` returning `pid` and a `stopWorker` spy around the
   * real stopper. Returns the exit code, the pids the stop path saw and the captured streams.
   * @param {string} repo @param {string} runId @param {number} pid
   */
  async function runStartLaunching(repo, runId, pid) {
    /** @type {number[]} */
    const stops = [];
    /** @type {import('../../src/cli/run.mjs').StopWorker} */
    const stopWorker = (pin, opts) => {
      stops.push(pin.pid);
      return stopPinnedWorker(pin, opts);
    };
    const stdout = { text: '', write: (/** @type {string} */ s) => void (stdout.text += s) };
    const stderr = { text: '', write: (/** @type {string} */ s) => void (stderr.text += s) };
    const code = await runRun(['start', '--cwd', repo, '--run', runId], { stdout, stderr, startWorker: async () => ({ pid, repoRoot: repo }), stopWorker });
    return { code, stops, stdout: stdout.text, stderr: stderr.text };
  }

  /** The launched child must be gone within 3 s of the stop, or the stop did not work. */
  async function assertStoppedQuickly(/** @type {{pid: number, exited: Promise<unknown>}} */ c) {
    const exited = await Promise.race([c.exited, new Promise((r) => setTimeout(() => r(false), 3000))]);
    assert.equal(exited, true, `pid ${c.pid} still running 3 s after the stop`);
    assert.equal(await waitFor(() => !alive(c.pid), 3000), true);
  }

  test('run start whose launched worker never announces itself stops that pid exactly once (no orphan) and fails', async () => {
    const { repo, runId } = await makeRepo({ start: false });
    const launched = sleepingChild(); // `repinWorker` throws `worker_down`
    try {
      const res = await runStartLaunching(repo, runId, launched.pid);
      assert.equal(res.code, 1);
      assert.deepEqual(res.stops, [launched.pid]);
      await assertStoppedQuickly(launched);
      assert.equal(res.stderr, `run start: launched worker pid ${launched.pid} was not pinned; stopped it\nrun start: no live worker serves this queue\n`);
      assert.equal(res.stdout, '');
      assert.equal((await readRun(runId)).worker, null);
    } finally {
      launched.kill();
    }
  });

  test('run start with an older live worker announced for the run: the launched pid is never pinned over, so it is stopped, and the older one is left alone', async () => {
    const { repo, runId } = await makeRepo({ start: false });
    const older = sleepingChild(); // stands in for a worker started by hand and still announced
    const launched = sleepingChild();
    try {
      announceWorker(repo, { pid: older.pid, run: runId });
      const res = await runStartLaunching(repo, runId, launched.pid);
      assert.equal(res.code, 1);
      assert.deepEqual(res.stops, [launched.pid]); // the new pid: stopped once; the older pid: never signalled
      await assertStoppedQuickly(launched);
      assert.equal(alive(older.pid), true);
      assert.equal(liveWorker(repo)?.pid, older.pid);
      assert.equal(
        res.stderr,
        `run start: launched worker pid ${launched.pid} was not pinned; stopped it\nrun start: the queue announces worker pid ${older.pid}, not the launched pid ${launched.pid}\n`,
      );
      assert.equal((await readRun(runId)).worker, null); // neither pid was pinned
    } finally {
      launched.kill();
      older.kill();
    }
  });
});
