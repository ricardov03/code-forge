import { BLOCK, makeRepo, freshDir, cli } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtempSync, rmSync } from 'node:fs';

const { announceWorker, retractWorker, enqueue, writeResult, readResult } = await import('../../src/worker/queue.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { loadConfigFile } = await import('../../src/config/load.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { daemonRequest, startNativeDaemon } = await import('../../src/util/native.mjs');

const FILE = 'src/a.mjs';

async function fixture(t) {
  const { repo, runId } = await makeRepo();
  const key = await loadKey(runId);
  const cfg = (await loadConfigFile(`${repo}/.code-forge.yml`)).config;
  const tickets = [FILE, 'src/b.mjs', 'src/c.mjs'].map((file) => enqueue({ repoRoot: repo, run: runId, block: BLOCK, file }));
  const cache = (ticket, result) => writeResult({ repoRoot: repo, runId, ticket: ticket.ticket, key, result });
  for (const ticket of tickets.slice(1)) cache(ticket, { status: 'reviewed', approved: true, findings: [] });
  const sessions = [];
  const worker = await createWorker({ repoRoot: repo, runId, cfg, runRootDir: freshDir('unavailable-worker'), slug: 'worker-test', key }, {
    store: await createKeyStore({ backends: [], dir: freshDir('unavailable-store') }), env: {},
    writeRow: async () => {}, readRows: async () => [], expiryCheck: async () => {},
    review: async (ticket) => {
      sessions.push(ticket.file);
      return { status: 'unavailable', reason: 'engine-error', approved: false, engine: 'test', sessions: [] };
    },
  });
  announceWorker(repo, { pid: process.pid, run: runId });
  t.after(() => retractWorker(repo, process.pid));
  const join = () => cli(['review-file', '--wait-block', BLOCK, '--run', runId, '--max', '0ms'], repo, { CODE_FORGE_NATIVE: '0' });
  const expected = () => ({
    block: BLOCK,
    files: tickets.map((ticket, i) => {
      const result = readResult(repo, runId, ticket.ticket);
      const timings = Object.fromEntries(['queued_at', 'started_at', 'finished_at', 'duration_ms'].filter((key) => result?.[key] !== undefined).map((key) => [key, result[key]]));
      return { file: ticket.file, ticket: ticket.ticket, status: 'done', outcome: i === 0 ? 'unavailable' : 'approved', ...timings };
    }),
    done: false, reason: 'review_unavailable',
    next: `review-file ${FILE} --fresh re-runs it; if --fresh refuses, block reopen ${BLOCK} --run ${runId} --file ${FILE} --reason <text>`,
  });
  return { repo, runId, cfg, tickets, cache, worker, sessions, join, expected };
}

test('B217: the second unavailable result is reported after exactly one retry; approval completes the same block', async (t) => {
  const f = await fixture(t);
  assert.equal(await f.worker.drain(), 1, 'one initial review returns unavailable');
  const pending = await f.join();
  assert.deepEqual([pending.code, pending.json.done, pending.json.reason, pending.json.files.map((file) => file.status)], [0, false, 'wait_timeout', ['pending', 'done', 'done']]);
  assert.deepEqual([await f.worker.drain(), await f.worker.drain()], [1, 0], 'exactly one automatic retry finishes');
  const report = await f.join();
  assert.deepEqual([report.code, report.json], [0, f.expected()]);
  assert.equal(await f.worker.drain(), 0, 'report queues zero third reviews');
  assert.deepEqual(f.sessions, [FILE, FILE], 'exactly two reviews of the unavailable file');
  f.cache(f.tickets[0], { status: 'reviewed', approved: true, findings: [] });
  const approved = await f.join();
  assert.deepEqual([approved.code, approved.json.done, approved.json.files.map((file) => file.outcome)], [0, true, ['approved', 'approved', 'approved']]);
});

test('B217: a refused lost review is reported without retry; findings still complete the block', async (t) => {
  const f = await fixture(t);
  f.cache(f.tickets[0], { status: 'refused', reason: 'review-state-lost' });
  const report = await f.join();
  assert.deepEqual([report.code, report.json], [0, f.expected()]);
  assert.deepEqual([await f.worker.drain(), f.sessions], [0, []], 'refusal starts zero reviews');
  f.cache(f.tickets[0], { status: 'reviewed', approved: false, findings: [{ id: 'F1' }] });
  const findings = await f.join();
  assert.deepEqual([findings.code, findings.json.done, findings.json.files.map((file) => file.outcome)], [0, true, ['findings', 'approved', 'approved']]);
});

test('B217: the live daemon and CODE_FORGE_NATIVE=0 report the same repeated unavailability', async (t) => {
  // Darwin Unix socket paths must fit in 104 bytes, including the run name.
  const savedHome = process.env.HOME;
  const home = mkdtempSync('/tmp/cf217-');
  process.env.HOME = home;
  let stop = async () => {};
  try {
    const f = await fixture(t);
    assert.equal(await f.worker.drain(), 1);
    assert.equal((await f.join()).json.files[0].status, 'pending');
    assert.equal(await f.worker.drain(), 1);
    stop = await startNativeDaemon(f.runId, f.cfg);
    assert.equal((await daemonRequest(f.runId, { method: 'ping' }))?.ok, true, 'a live daemon must answer; fallback does not prove parity');
    const native = await daemonRequest(f.runId, { method: 'wait-block', block: BLOCK, max_ms: 0 });
    const node = await f.join();
    assert.deepEqual([node.code, native, node.json], [0, f.expected(), f.expected()]);
    assert.equal(await f.worker.drain(), 0, 'neither path queues a third review');
  } finally {
    await stop();
    process.env.HOME = savedHome;
    rmSync(home, { recursive: true, force: true });
  }
});
