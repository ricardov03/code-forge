// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { BLOCK, freshDir, makeRepo, waitFor } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { createWorker, parallelTickets } = await import('../../src/worker/loop.mjs');
const { enqueue, isDone, liveWorker, pendingTickets, readResult, readTicket, readWorker, reviewsDir } = await import('../../src/worker/queue.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { startRun, readRun, reloadRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { snapshotFor, configHash } = await import('../../src/state/config-snapshot.mjs');

/** @param {number} ms */
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const T0 = Date.parse('2026-10-03T10:00:00.000Z');
/** @param {number} i @returns {Date} enqueue time i seconds after T0 (oldest first = lowest i) */
const at = (i) => new Date(T0 + i * 1000);

/**
 * An instrumented fake engine hook: it counts how many reviews run at once, overall and per
 * (block, file), and logs the order reviews start and end in. `hold(ticket)` decides how long one
 * review lasts (a gate, a delay); `onStart(ticket)` runs as it starts.
 * @param {{hold?: (t: Record<string, any>) => Promise<unknown>, onStart?: (t: Record<string, any>) => void, fail?: (t: Record<string, any>) => boolean}} [opts]
 */
function instrumented({ hold = async () => {}, onStart = () => {}, fail = () => false } = {}) {
  const log = { active: 0, max: 0, overlap: 0, /** @type {string[]} */ starts: [], /** @type {string[]} */ ends: [], /** @type {Map<string, number>} */ perFile: new Map() };
  /** @type {any} */
  const review = async (/** @type {Record<string, any>} */ ticket) => {
    const k = `${ticket.block}\0${ticket.file}`;
    log.active += 1;
    log.max = Math.max(log.max, log.active);
    log.perFile.set(k, (log.perFile.get(k) ?? 0) + 1);
    if (/** @type {number} */ (log.perFile.get(k)) > 1) log.overlap += 1;
    log.starts.push(`${ticket.block}:${ticket.file}`);
    try {
      onStart(ticket);
      await hold(ticket);
      if (fail(ticket)) throw new Error('fake engine crash');
    } finally {
      log.perFile.set(k, /** @type {number} */ (log.perFile.get(k)) - 1);
      log.active -= 1;
      log.ends.push(`${ticket.block}:${ticket.file}`);
    }
    return { status: 'reviewed', approved: true, engine: 'test', sessions: [] };
  };
  return { log, review };
}

/** Gates keyed by file: a review held on `wait(file)` ends when `open(file)` is called. */
function gates() {
  /** @type {Map<string, () => void>} */
  const openers = new Map();
  /** @type {Map<string, Promise<void>>} */
  const waits = new Map();
  /** @param {string} file */
  const ensure = (file) => {
    if (!waits.has(file)) waits.set(file, new Promise((resolve) => openers.set(file, () => resolve(undefined))));
    return /** @type {Promise<void>} */ (waits.get(file));
  };
  return {
    /** @param {string} file */
    wait: (file) => ensure(file),
    /** @param {string} file */
    open: (file) => {
      ensure(file);
      openers.get(file)?.();
    },
    /** @param {string[]} files */
    openAll: (files) => files.forEach((f) => {
      ensure(f);
      openers.get(f)?.();
    }),
  };
}

/**
 * A repo with a started run (block B11 open, no config snapshot ⇒ the worker's boot `cfg` is the
 * ticket config) and an in-process worker with `parallel_tickets: n` on `review`.
 * @param {{n?: number, review: any, files?: string[], heartbeatMs?: number, deps?: Record<string, any>}} opts
 */
async function pool({ n, review, files = [], heartbeatMs, deps = {} }) {
  const { repo, runId } = await makeRepo();
  for (const f of files) writeFileSync(path.join(repo, f), `export const x = '${f}';\n`);
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const cfg = n === undefined ? {} : { review: { parallel_tickets: n } };
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg, runRootDir: freshDir('runroot'), slug: 'worker-test', key: await loadKey(runId), pollMs: 20, ...(heartbeatMs ? { heartbeatMs } : {}) },
    { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow: async (row) => void rows.push(row), readRows: async () => rows, review, ...deps },
  );
  /** @param {string} file @param {number} i @param {string} [block] */
  const add = (file, i, block = BLOCK) => enqueue({ repoRoot: repo, run: runId, block, file, now: at(i) }).ticket;
  return { repo, runId, worker, rows, add };
}

describe('review.parallel_tickets (B42)', () => {
  test('parallelTickets: an integer 1–16 is used, anything else is the default 3', () => {
    assert.deepEqual(
      [parallelTickets({ review: { parallel_tickets: 1 } }), parallelTickets({ review: { parallel_tickets: 16 } }), parallelTickets({}), parallelTickets({ review: { parallel_tickets: 0 } }), parallelTickets({ review: { parallel_tickets: 17 } }), parallelTickets({ review: { parallel_tickets: 2.5 } }), parallelTickets(null)],
      [1, 16, 3, 3, 3, 3, 3],
    );
  });

  test('N = 3: at most 3 tickets run at once, oldest first; when one ends the next pending one starts while the others still run', async () => {
    const g = gates();
    const { log, review } = instrumented({ hold: (t) => g.wait(t.file) });
    const files = ['src/d.mjs', 'src/e.mjs', 'src/f.mjs'];
    const p = await pool({ n: 3, review, files });
    // enqueued newest first on disk; the enqueue times make a, b, c, d, e, f the oldest-first order
    const order = ['src/f.mjs', 'src/e.mjs', 'src/d.mjs', 'src/c.mjs', 'src/b.mjs', 'src/a.mjs'];
    order.forEach((f, k) => p.add(f, 5 - k));
    const drained = p.worker.drain();
    assert.equal(await waitFor(() => log.active === 3), true);
    await sleep(80); // several poll ticks: still exactly 3
    // the 3 oldest were taken (they start together, so their review-start order is a race)
    assert.deepEqual([log.active, [...log.starts].sort()], [3, ['B11:src/a.mjs', 'B11:src/b.mjs', 'B11:src/c.mjs']]);
    g.open('src/b.mjs');
    assert.equal(await waitFor(() => log.starts.length === 4), true);
    // no batch barrier: d started while a and c are still held
    assert.deepEqual([log.active, log.starts[3], log.ends], [3, 'B11:src/d.mjs', ['B11:src/b.mjs']]);
    g.openAll(['src/a.mjs', 'src/c.mjs', 'src/d.mjs', 'src/e.mjs', 'src/f.mjs']);
    assert.equal(await drained, 6);
    assert.deepEqual([log.max, log.starts.length, pendingTickets(p.repo).length], [3, 6, 0]);
    assert.deepEqual([[...log.starts.slice(0, 3)].sort(), log.starts[3], [...log.starts.slice(4)].sort()], [['B11:src/a.mjs', 'B11:src/b.mjs', 'B11:src/c.mjs'], 'B11:src/d.mjs', ['B11:src/e.mjs', 'B11:src/f.mjs']]);
  });

  test('N = 1: one ticket at a time, oldest first, results and ledger rows in that order (the serial worker)', async () => {
    const { log, review } = instrumented({ hold: () => sleep(5) });
    const p = await pool({ n: 1, review });
    const tc = p.add('src/c.mjs', 2);
    const ta = p.add('src/a.mjs', 0);
    const tb = p.add('src/b.mjs', 1);
    assert.equal(await p.worker.drain(), 3);
    assert.equal(log.max, 1);
    assert.deepEqual(log.starts, ['B11:src/a.mjs', 'B11:src/b.mjs', 'B11:src/c.mjs']);
    assert.deepEqual(log.ends, log.starts);
    assert.deepEqual(p.rows.map((r) => [r.event, r.ticket, r.status]), [['review.result', ta, 'reviewed'], ['review.result', tb, 'reviewed'], ['review.result', tc, 'reviewed']]);
  });

  test('no config: the pool runs the default 3 at once', async () => {
    const g = gates();
    const { log, review } = instrumented({ hold: (t) => g.wait(t.file) });
    const p = await pool({ review, files: ['src/d.mjs'] });
    ['src/a.mjs', 'src/b.mjs', 'src/c.mjs', 'src/d.mjs'].forEach((f, i) => p.add(f, i));
    const drained = p.worker.drain();
    assert.equal(await waitFor(() => log.active === 3), true);
    await sleep(60);
    assert.equal(log.starts.length, 3);
    g.openAll(['src/a.mjs', 'src/b.mjs', 'src/c.mjs', 'src/d.mjs']);
    assert.equal(await drained, 4);
    assert.equal(log.max, 3);
  });

  test('two tickets for the same (block, file) never overlap: the later stays pending without a slot, a ticket for another file takes that slot', async () => {
    const g = gates();
    /** @type {Record<string, string>} */
    const ids = {};
    let first = true;
    /** @type {any} */
    let p;
    const { log, review } = instrumented({
      hold: (t) => (t.file === 'src/a.mjs' && t.ticket === ids.a1 ? g.wait('a1') : Promise.resolve()),
      onStart: (t) => {
        if (t.file !== 'src/a.mjs' || !first) return;
        first = false;
        // the coder edits a.mjs while its review runs, and asks for a review of the new bytes
        appendFileSync(path.join(p.repo, 'src/a.mjs'), 'export const fixed = 1;\n');
        ids.a2 = p.add('src/a.mjs', 1);
        ids.c = p.add('src/c.mjs', 2);
      },
    });
    p = await pool({ n: 2, review });
    ids.a1 = p.add('src/a.mjs', 0);
    const drained = p.worker.drain();
    // c (newer than a2) ran and finished while a1 is held: a2 did not take the free slot
    assert.equal(await waitFor(() => log.ends.includes('B11:src/c.mjs')), true);
    assert.deepEqual([log.active, log.starts], [1, ['B11:src/a.mjs', 'B11:src/c.mjs']]);
    g.open('a1');
    assert.equal(await drained, 3);
    assert.equal(log.overlap, 0);
    assert.deepEqual(log.starts, ['B11:src/a.mjs', 'B11:src/c.mjs', 'B11:src/a.mjs']);
    assert.deepEqual([readResult(p.repo, p.runId, ids.a1)?.status, readResult(p.repo, p.runId, ids.a2)?.status, readResult(p.repo, p.runId, ids.c)?.status], ['reviewed', 'reviewed', 'reviewed']);
  });

  test('a ticket whose engine throws is `unavailable` (engine-error); the others finish and its slot is freed', async () => {
    const g = gates();
    const { log, review } = instrumented({ hold: (t) => (t.file === 'src/b.mjs' ? Promise.resolve() : g.wait(t.file)), fail: (t) => t.file === 'src/b.mjs' });
    const files = ['src/d.mjs', 'src/e.mjs'];
    const p = await pool({ n: 2, review, files });
    const ids = ['src/b.mjs', 'src/a.mjs', 'src/c.mjs', 'src/d.mjs', 'src/e.mjs'].map((f, i) => p.add(f, i));
    const drained = p.worker.drain();
    // b crashed at once; both slots are taken again by a and c (no slot leaked)
    assert.equal(await waitFor(() => log.active === 2), true);
    assert.deepEqual([[...log.starts.slice(0, 2)].sort(), log.starts[2]], [['B11:src/a.mjs', 'B11:src/b.mjs'], 'B11:src/c.mjs']);
    g.openAll(['src/a.mjs', 'src/c.mjs', 'src/d.mjs', 'src/e.mjs']);
    assert.equal(await drained, 5);
    const results = ids.map((id) => readResult(p.repo, p.runId, id));
    assert.deepEqual(results.map((r) => [r?.status, r?.reason ?? null]), [['unavailable', 'engine-error'], ['reviewed', null], ['reviewed', null], ['reviewed', null], ['reviewed', null]]);
    assert.deepEqual([log.max, log.active, pendingTickets(p.repo).length], [2, 0, 0]);
  });

  test('a ticket whose failure cannot be written is poisoned: the others complete, and the next drain skips it', async () => {
    const { log, review } = instrumented({ hold: () => sleep(5) });
    const p = await pool({ n: 2, review });
    const ta = p.add('src/a.mjs', 0);
    p.add('src/b.mjs', 1);
    p.add('src/c.mjs', 2);
    // a's result path is a non-empty directory: its result and its worker-error both fail to write
    mkdirSync(path.join(reviewsDir(p.repo, p.runId), `${ta}.json`, 'blocker'), { recursive: true });
    assert.equal(await p.worker.drain(), 2);
    assert.deepEqual(pendingTickets(p.repo), [ta]);
    assert.equal(await p.worker.drain(), 0);
    // a and b start together (their start order is a race); a was reviewed once, never again
    assert.deepEqual([...log.starts].sort(), ['B11:src/a.mjs', 'B11:src/b.mjs', 'B11:src/c.mjs']);
  });

  test('the heartbeat keeps beating while 3 tickets run, and once() waits for all of them before it releases', async () => {
    const g = gates();
    const { log, review } = instrumented({ hold: (t) => g.wait(t.file) });
    const p = await pool({ n: 3, review, heartbeatMs: 100 });
    ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].forEach((f, i) => p.add(f, i));
    let finished = false;
    const once = p.worker.once().then((n) => {
      finished = true;
      return n;
    });
    assert.equal(await waitFor(() => log.active === 3), true);
    const beat = String(readWorker(p.repo)?.heartbeat_at);
    assert.equal(await waitFor(() => String(readWorker(p.repo)?.heartbeat_at) !== beat, 2000), true);
    assert.equal(liveWorker(p.repo)?.pid, process.pid);
    g.openAll(['src/a.mjs', 'src/b.mjs']);
    await sleep(60);
    assert.deepEqual([finished, log.active], [false, 1]); // c still runs: once() has not returned
    g.open('src/c.mjs');
    assert.equal(await once, 3);
    assert.equal(liveWorker(p.repo), null);
  });

  test('stop(): no new ticket starts; the running ones are awaited and recorded (result + done marker); the unstarted one has neither', async () => {
    const g = gates();
    const { log, review } = instrumented({ hold: (t) => g.wait(t.file) });
    const p = await pool({ n: 2, review });
    const ids = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs'].map((f, i) => p.add(f, i));
    let ended = false;
    const running = p.worker.run().then(() => {
      ended = true;
    });
    assert.equal(await waitFor(() => log.active === 2), true);
    p.worker.stop();
    await sleep(60);
    assert.equal(ended, false); // still waiting for a and b
    g.openAll(['src/a.mjs', 'src/b.mjs', 'src/c.mjs']);
    await running;
    assert.deepEqual([...log.starts].sort(), ['B11:src/a.mjs', 'B11:src/b.mjs']);
    assert.deepEqual(ids.map((id) => isDone(p.repo, id)), [true, true, false]);
    assert.deepEqual(ids.map((id) => readResult(p.repo, p.runId, id)?.status ?? null), ['reviewed', 'reviewed', null]);
    assert.deepEqual(pendingTickets(p.repo), [ids[2]]);
    assert.equal(liveWorker(p.repo), null);
  });

  test('an idle drain reads no run record: 5 drains of an empty queue ⇒ 0 readRun calls; one pending ticket ⇒ the record is read', async () => {
    const { review } = instrumented();
    let reads = 0;
    const p = await pool({ n: 2, review, deps: { readRun: async (/** @type {string} */ id) => ((reads += 1), readRun(id)) } });
    for (let i = 0; i < 5; i += 1) assert.equal(await p.worker.drain(), 0);
    assert.equal(reads, 0);
    p.add('src/a.mjs', 0);
    assert.equal(await p.worker.drain(), 1);
    assert.equal(reads, 2); // the pool size once, the ticket's config once
  });

  test('a ticket waiting on a busy file is read once for scheduling across many wake-ups (+ once when it is processed)', async () => {
    const g = gates();
    /** @type {Record<string, number>} */
    const reads = {};
    /** @type {Record<string, string>} */
    const ids = {};
    /** @type {any} */
    let p;
    const { log, review } = instrumented({
      hold: (t) => (t.ticket === ids.a1 ? g.wait('a1') : Promise.resolve()),
      onStart: (t) => {
        if (t.ticket !== ids.a1) return;
        appendFileSync(path.join(p.repo, 'src/a.mjs'), 'export const fixed = 1;\n');
        ids.a2 = p.add('src/a.mjs', 1);
      },
    });
    const counting = (/** @type {string} */ repoRoot, /** @type {string} */ id) => {
      reads[id] = (reads[id] ?? 0) + 1;
      return readTicket(repoRoot, id);
    };
    p = await pool({ n: 2, review, deps: { readTicket: counting } });
    ids.a1 = p.add('src/a.mjs', 0);
    const drained = p.worker.drain();
    assert.equal(await waitFor(() => (reads[ids.a2] ?? 0) >= 1), true);
    await sleep(200); // ~10 poll wake-ups (pollMs 20) while a2 waits on a1's file
    assert.deepEqual([reads[ids.a2], log.starts.length], [1, 1]);
    g.open('a1');
    assert.equal(await drained, 2);
    assert.deepEqual([reads[ids.a1], reads[ids.a2], log.overlap], [2, 2, 0]);
  });

  test('the file-lock backstop: a ticket unreadable when scheduled (no key) but readable when processed still never overlaps its file', async () => {
    const g = gates();
    /** @type {Record<string, number>} */
    const reads = {};
    /** @type {Record<string, string>} */
    const ids = {};
    /** @type {any} */
    let p;
    const { log, review } = instrumented({
      hold: (t) => (t.ticket === ids.a1 ? g.wait('a1') : Promise.resolve()),
      onStart: (t) => {
        if (t.ticket !== ids.a1) return;
        appendFileSync(path.join(p.repo, 'src/a.mjs'), 'export const fixed = 1;\n');
        ids.a2 = p.add('src/a.mjs', 1);
      },
    });
    const flaky = (/** @type {string} */ repoRoot, /** @type {string} */ id) => {
      reads[id] = (reads[id] ?? 0) + 1;
      // a2's first read (the scheduler's) fails, as a ticket caught mid-write would
      if (id === ids.a2 && reads[id] === 1) throw new Error('fake unreadable');
      return readTicket(repoRoot, id);
    };
    p = await pool({ n: 2, review, deps: { readTicket: flaky } });
    ids.a1 = p.add('src/a.mjs', 0);
    const drained = p.worker.drain();
    // a2 took the free slot (no key to check) and was read by processTicket: it waits on the lock
    assert.equal(await waitFor(() => (reads[ids.a2] ?? 0) >= 2), true);
    await sleep(60);
    assert.deepEqual([log.starts.length, log.active], [1, 1]);
    g.open('a1');
    assert.equal(await drained, 2);
    assert.equal(log.overlap, 0);
    assert.deepEqual(log.starts, ['B11:src/a.mjs', 'B11:src/a.mjs']);
    assert.deepEqual([readResult(p.repo, p.runId, ids.a1)?.status, readResult(p.repo, p.runId, ids.a2)?.status], ['reviewed', 'reviewed']);
  });

  test('stress: 10 tickets over 3 blocks (two files re-queued mid-review), random delays (seeded), N = 3: all done, never more than 3 at once, no (block, file) overlap', async () => {
    let seed = 42;
    const rand = () => {
      // mulberry32
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
    const files = Array.from({ length: 8 }, (_, i) => `src/s${i}.mjs`);
    const blocks = ['B11', 'B12', 'B13'];
    /** @type {any} */
    let p;
    const requeued = new Set();
    let next = 100;
    const { log, review } = instrumented({
      hold: () => sleep(Math.floor(rand() * 30)),
      onStart: (t) => {
        if (!['src/s0.mjs', 'src/s1.mjs'].includes(t.file) || requeued.has(t.file)) return;
        requeued.add(t.file);
        appendFileSync(path.join(p.repo, t.file), 'export const again = 1;\n');
        p.add(t.file, (next += 1), t.block);
      },
    });
    p = await pool({ n: 3, review, files });
    const ids = files.map((f, i) => p.add(f, i, blocks[i % 3]));
    assert.equal(await p.worker.drain(), 10);
    assert.deepEqual([log.overlap, log.active, log.starts.length, pendingTickets(p.repo).length], [0, 0, 10, 0]);
    assert.equal(log.max <= 3, true);
    assert.equal(log.max >= 2, true); // they did run together
    assert.deepEqual(ids.map((id) => readResult(p.repo, p.runId, id)?.status), Array(8).fill('reviewed'));
    assert.equal(p.rows.filter((r) => r.event === 'review.result' && r.status === 'reviewed').length, 10);
  });

  test('run reload between two tickets: the pinned ticket keeps the old config, the new one gets the new config, and the pool takes its size from the snapshot in force', async () => {
    const OLD = { version: 1, provider: 'anthropic', review: { session_timeout_s: 60, parallel_tickets: 2 } };
    const NEW = { version: 1, provider: 'anthropic', review: { session_timeout_s: 90, parallel_tickets: 1 } };
    const { repo, runId } = await makeRepo({ start: false });
    const writeRow = async () => {};
    await startRun({ workspace: repo, project: 'worker-test', config: OLD, runId, writeRow });
    await openBlock({ runId, id: BLOCK, level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
    writeFileSync(path.join(repo, 'src/d.mjs'), 'export const d = 1;\n');
    writeFileSync(path.join(repo, 'src/e.mjs'), 'export const e = 1;\n');
    /** @type {Record<string, Record<string, any>>} */
    const seen = {};
    const { log, review } = instrumented({ hold: () => sleep(40) });
    const worker = await createWorker(
      { runId, repoRoot: repo, cfg: { review: { parallel_tickets: 3 } }, runRootDir: freshDir('runroot'), slug: 'worker-test', key: await loadKey(runId), pollMs: 20 },
      {
        store: await createKeyStore({ backends: [], dir: freshDir('store') }),
        env: {},
        writeRow: async () => {},
        review: async (/** @type {any} */ ticket, /** @type {any} */ ctx) => {
          seen[ticket.file] = ctx.cfg;
          return review(ticket);
        },
      },
    );
    enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/c.mjs', now: at(0) });
    enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/d.mjs', now: at(1) });
    enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/e.mjs', now: at(1.5) });
    assert.equal(await worker.drain(), 3);
    assert.equal(log.max, 2); // 3 tickets, 2 at once: OLD's parallel_tickets, not the boot config's 3

    const before = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs', now: at(2) });
    await reloadRun({ runId, config: NEW, readPending: () => pendingTickets(repo), writeRow });
    const after = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/b.mjs', now: at(3) });
    const record = await readRun(runId);
    assert.equal(snapshotFor(record, before.ticket)?.hash, configHash(OLD));
    log.max = 0;
    assert.equal(await worker.drain(), 2);
    assert.equal(log.max, 1); // NEW's parallel_tickets
    assert.deepEqual([seen['src/a.mjs'], seen['src/b.mjs']], [OLD, NEW]);
    assert.deepEqual([readResult(repo, runId, before.ticket)?.status, readResult(repo, runId, after.ticket)?.status], ['reviewed', 'reviewed']);
  });
});

// ---------------------------------------------------------------------------------------------
// The real engine hook (fix loop) under the pool: the block lock.

/** @param {string} file @param {string} id */
const finding = (file, id) => ({ id, file, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
}

describe('the block lock (B42): budget row and L3 rung with two files of one block in parallel', () => {
  test('round 1 of a.mjs and b.mjs runs together; the budget row is written once; at round 2 only ONE file takes the L3 rung, the other stops (l3_patch_exhausted)', async () => {
    const { repo, runId } = await makeRepo();
    const key = await loadKey(runId);
    const FILES = ['src/a.mjs', 'src/b.mjs'];
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    const sessions = { active: 0, max: 0, round: 1, arrived: 0 };
    const spawn = async (/** @type {Record<string, any>} */ opts) => {
      sessions.active += 1;
      sessions.arrived += 1;
      sessions.max = Math.max(sessions.max, sessions.active);
      try {
        // round 1: wait (bounded) for the other file's session, so running together is observable
        if (sessions.round === 1) await waitFor(() => sessions.arrived >= 2, 3000);
        else await sleep(20);
        const text = readFileSync(opts.promptPath, 'utf8');
        const lens = opts.rowExtra?.lens;
        const file = FILES.find((f) => text.includes(f)) ?? FILES[0];
        const at0 = text.indexOf('\n## open findings\n');
        const open = at0 >= 0 ? [...text.slice(at0).matchAll(/^- (\S+) \(/gm)].map((m) => m[1]) : [];
        const resolved = lens === 'recheck' && open.length > 0 ? [{ id: open[0], resolved: true, why: 'fixed' }] : [];
        const tag = file === 'src/a.mjs' ? 'A' : 'B';
        const findings = lens === 'recheck' ? [] : [1, 2, 3, 4].map((n) => finding(file, `${tag}${n}`));
        const answer = { passed: false, summary: 's', reviewed_hunks: hunksOf(text), findings, resolved, needs_file: [] };
        return { status: 'ok', exit_code: 0, answer, usage: { tokens_in: 900, tokens_out: 300 } };
      } finally {
        sessions.active -= 1;
      }
    };
    const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
      const [id] = Object.keys(req.questions);
      return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
    };
    const worker = await createWorker(
      { runId, repoRoot: repo, cfg: { review: { parallel_tickets: 2 } }, runRootDir: freshDir('runroot'), slug: 'worker-test', key, pollMs: 20 },
      { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow: async (row) => void rows.push(row), readRows: async () => rows, spawn: /** @type {any} */ (spawn), jev },
    );
    let n = 0;
    const round = async () => {
      n += 1;
      const ids = FILES.map((f, i) => {
        appendFileSync(path.join(repo, f), `export const fix${n} = ${n};\n`);
        return enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: f, now: at(n * 10 + i) }).ticket;
      });
      assert.equal(await worker.drain(), 2);
      return ids.map((id) => /** @type {Record<string, any>} */ (readResult(repo, runId, id)));
    };

    const r1 = await round();
    assert.deepEqual(r1.map((r) => [r.file, r.status, r.round, r.kind, r.next?.action, r.findings.length]), [
      ['src/a.mjs', 'reviewed', 1, 'full', 'fix', 4],
      ['src/b.mjs', 'reviewed', 1, 'full', 'fix', 4],
    ]);
    assert.equal(sessions.max, 2); // a first full round cannot take the rung: it ran outside the block lock
    assert.equal(rows.filter((r) => r.event === 'review.budget' && r.block === BLOCK).length, 1);

    sessions.round = 2;
    sessions.max = 0;
    const r2 = await round();
    assert.equal(sessions.max, 1); // the rechecks could take the rung: one at a time under the block lock
    const outcomes = r2.map((r) => [r.next?.action ?? null, r.stopped ?? null]).sort();
    assert.deepEqual(outcomes, [['patch', null], ['stop', 'l3_patch_exhausted']]);
    const caps = rows.filter((r) => r.event === 'review.cap');
    assert.equal(caps.length, 1);
    assert.equal(caps[0].reason, 'l3_patch_exhausted');
    assert.equal(caps[0].file, r2.find((r) => r.stopped)?.file);
    assert.equal(rows.filter((r) => r.event === 'review.budget' && r.block === BLOCK).length, 1);
  });
});
