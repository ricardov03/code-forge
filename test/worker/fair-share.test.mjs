// Helpers first: pin HOME before importing source modules.
import { BLOCK, freshDir, makeRepo, waitFor } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, pendingTickets, readTicket } = await import('../../src/worker/queue.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { startRun, reloadRun } = await import('../../src/state/run.mjs');
const { validateConfig, configSchema } = await import('../../src/config/validate.mjs');
const { PROVIDER_DEFAULTS } = await import('../../src/config/defaults/index.mjs');

const T0 = Date.parse('2026-10-03T10:00:00.000Z');
const FILES = ['src/a.mjs', 'src/b.mjs', 'src/c.mjs', 'src/d.mjs', 'other/y.mjs'];
const Y = 'B12';

/** @param {Record<string, any>} review */
function validConfig(review) {
  return { version: 1, provider: 'anthropic', levels: PROVIDER_DEFAULTS.anthropic.levels, review };
}

/** @param {boolean} fairShare */
function accepts(fairShare) {
  const result = validateConfig(validConfig({ parallel_tickets: 2, fair_share: fairShare }));
  assert.deepEqual([result.valid, result.errors], [true, []], `fair_share: ${fairShare} must validate`);
}

/** An instrumented review hook with a gate for each ticket. */
function instrumented() {
  const log = { active: 0, overlap: 0, /** @type {string[]} */ starts: [], /** @type {Map<string, number>} */ perFile: new Map() };
  /** @type {Map<string, () => void>} */
  const gates = new Map();
  /** @type {any} */
  const review = async (/** @type {Record<string, any>} */ ticket) => {
    const key = `${ticket.block}:${ticket.file}`;
    log.active += 1;
    log.perFile.set(key, (log.perFile.get(key) ?? 0) + 1);
    if (log.perFile.get(key) > 1) log.overlap += 1;
    log.starts.push(key);
    try {
      await new Promise((resolve) => gates.set(ticket.ticket, () => resolve(undefined)));
    } finally {
      log.active -= 1;
      log.perFile.set(key, log.perFile.get(key) - 1);
    }
    return { status: 'reviewed', approved: true, engine: 'test', sessions: [] };
  };
  return { log, review, release: (/** @type {string} */ id) => gates.get(id)?.(), releaseAll: () => gates.forEach((open) => open()) };
}

/** @param {{fairShare?: boolean, snapshot?: Record<string, any>}} [opts] */
async function pool({ fairShare, snapshot } = {}) {
  const { repo, runId } = await makeRepo({ start: snapshot === undefined });
  const writeRow = async () => {};
  if (snapshot !== undefined) {
    await startRun({ workspace: repo, project: 'worker-test', config: snapshot, runId, writeRow });
    await openBlock({ runId, id: BLOCK, level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'fixture', tests: ['worker'] }], writeRow });
  }
  await openBlock({ runId, id: Y, level: 'L2', owned: ['other/**'], acceptance: [{ clause: 'fixture', tests: ['worker'] }], writeRow });
  mkdirSync(path.join(repo, 'other'));
  for (const file of FILES) writeFileSync(path.join(repo, file), `export const x = '${file}';\n`);
  const hook = instrumented();
  /** @type {Map<string, number>} */
  const reads = new Map();
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: { review: { parallel_tickets: 2, ...(fairShare === undefined ? {} : { fair_share: fairShare }) } }, runRootDir: freshDir('runroot'), slug: 'worker-test', key: await loadKey(runId), pollMs: 20 },
    { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow, readRows: async () => [], review: hook.review,
      readTicket: (root, id) => {
        reads.set(id, (reads.get(id) ?? 0) + 1);
        return readTicket(root, id);
      } },
  );
  /** @param {string} file @param {number} i @param {string} [block] */
  const add = (file, i, block = BLOCK) => enqueue({ repoRoot: repo, run: runId, block, file, now: new Date(T0 + i * 1000) }).ticket;
  return { repo, runId, worker, add, reads, ...hook };
}

/** @param {Awaited<ReturnType<typeof pool>>} p @param {Promise<number>} drained */
async function stop(p, drained) {
  p.worker.stop();
  p.releaseAll();
  return drained;
}

test('fair share: four older X tickets and one newer Y ticket start exactly X oldest and Y', async () => {
  const p = await pool({ fairShare: true });
  const ids = FILES.map((file, i) => p.add(file, i, i === 4 ? Y : BLOCK));
  const drained = p.worker.drain();
  try {
    assert.equal(await waitFor(() => p.log.active === 2), true);
    assert.deepEqual([p.log.active, [...p.log.starts].sort()], [2, [`${BLOCK}:src/a.mjs`, `${Y}:other/y.mjs`]]);
    assert.deepEqual(ids.map((id) => p.reads.get(id)), [2, 1, 1, 1, 2], 'one scheduling read per ticket, plus one processing read per start');
  } finally {
    assert.equal(await stop(p, drained), 2);
  }
});

test('absent or false fair share starts exactly the two oldest X tickets', async () => {
  for (const fairShare of [undefined, false]) {
    const p = await pool({ fairShare });
    FILES.map((file, i) => p.add(file, i, i === 4 ? Y : BLOCK));
    const drained = p.worker.drain();
    try {
      assert.equal(await waitFor(() => p.log.active === 2), true);
      assert.deepEqual([p.log.active, [...p.log.starts].sort()], [2, [`${BLOCK}:src/a.mjs`, `${BLOCK}:src/b.mjs`]], `fair_share: ${fairShare}`);
    } finally {
      assert.equal(await stop(p, drained), 2);
    }
  }
  accepts(false);
});

test('one block keeps oldest-first start order with and without fair share', async () => {
  for (const fairShare of [undefined, false, true]) {
    const p = await pool({ fairShare });
    // Reverse enqueue order; fixed timestamps define the start order.
    const ids = [p.add('src/c.mjs', 2), p.add('src/b.mjs', 1), p.add('src/a.mjs', 0)];
    const drained = p.worker.drain();
    try {
      assert.equal(await waitFor(() => p.log.active === 2), true);
      assert.deepEqual([...p.log.starts].sort(), [`${BLOCK}:src/a.mjs`, `${BLOCK}:src/b.mjs`]);
      p.release(ids[2]);
      assert.equal(await waitFor(() => p.log.starts.length === 3), true);
      assert.deepEqual([p.log.active, p.log.starts[2]], [2, `${BLOCK}:src/c.mjs`]);
    } finally {
      assert.equal(await stop(p, drained), 3);
    }
  }
  accepts(true);
});

test('fair share never overlaps two tickets for the same block and file', async () => {
  const p = await pool({ fairShare: true });
  const first = p.add('src/a.mjs', 0);
  const drained = p.worker.drain();
  let expectedDone = 1;
  try {
    assert.equal(await waitFor(() => p.log.active === 1), true);
    appendFileSync(path.join(p.repo, 'src/a.mjs'), 'export const fixed = 1;\n');
    const second = p.add('src/a.mjs', 1);
    p.add('src/b.mjs', 2);
    assert.equal(await waitFor(() => p.log.active === 2), true);
    expectedDone = 2;
    assert.deepEqual([p.log.starts, p.reads.get(second), p.log.overlap], [[`${BLOCK}:src/a.mjs`, `${BLOCK}:src/b.mjs`], 1, 0]);
    p.release(first);
    assert.equal(await waitFor(() => p.log.starts.length === 3), true);
    expectedDone = 3;
    assert.deepEqual([p.log.active, p.log.starts, p.log.overlap, p.reads.get(second)], [2, [`${BLOCK}:src/a.mjs`, `${BLOCK}:src/b.mjs`, `${BLOCK}:src/a.mjs`], 0, 2]);
  } finally {
    assert.equal(await stop(p, drained), expectedDone);
  }
  accepts(true);
});

test('fair share accepts only booleans and its schema default is false', () => {
  for (const value of [true, false, undefined]) {
    const result = validateConfig(validConfig(value === undefined ? {} : { fair_share: value }));
    assert.deepEqual([result.valid, result.errors], [true, []], `fair_share: ${value}`);
  }
  assert.equal(configSchema.properties.review.properties.fair_share.default, false);
  for (const value of ['true', 'false', 0, 1, null, [], {}]) {
    const result = validateConfig(validConfig({ fair_share: value }));
    assert.deepEqual([result.valid, result.errors.map((e) => [e.rule, e.keyword, e.path])], [false, [['schema', 'type', '/review/fair_share']]], `fair_share: ${JSON.stringify(value)}`);
  }
});

test('reload applies fair share at the next drain from the current snapshot, not boot or ticket pins', async () => {
  const old = validConfig({ parallel_tickets: 2, fair_share: false });
  const p = await pool({ fairShare: true, snapshot: old });
  p.add('src/a.mjs', 0);
  p.add('src/b.mjs', 1);
  p.add('src/c.mjs', 2);
  p.add('src/d.mjs', 3);
  p.add('other/y.mjs', 4, Y);
  const first = p.worker.drain();
  try {
    assert.equal(await waitFor(() => p.log.active === 2), true);
    assert.deepEqual([...p.log.starts].sort(), [`${BLOCK}:src/a.mjs`, `${BLOCK}:src/b.mjs`]);
  } finally {
    p.releaseAll();
  }
  // Let all tickets finish before reusing the worker and reloading the run.
  for (let count = 3; count <= 5; count += 1) {
    assert.equal(await waitFor(() => p.log.starts.length >= count), true);
    p.releaseAll();
  }
  assert.equal(await first, 5);
  for (const [i, file] of FILES.entries()) {
    appendFileSync(path.join(p.repo, file), 'export const next = 1;\n');
    p.add(file, 10 + i, i === 4 ? Y : BLOCK);
  }
  await reloadRun({ runId: p.runId, config: validConfig({ parallel_tickets: 2, fair_share: true }), readPending: () => pendingTickets(p.repo), writeRow: async () => {} });
  const second = p.worker.drain();
  try {
    assert.equal(await waitFor(() => p.log.active === 2), true);
    assert.deepEqual([p.log.active, [...p.log.starts.slice(5)].sort()], [2, [`${BLOCK}:src/a.mjs`, `${Y}:other/y.mjs`]]);
  } finally {
    assert.equal(await stop(p, second), 2);
  }
});
