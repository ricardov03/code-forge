import { BLOCK, freshDir, makeRepo, waitFor } from './helpers.mjs';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import path from 'node:path';
import { test } from 'node:test';

const { createWorker } = await import('../../src/worker/loop.mjs');
const { announceWorker, enqueue, pendingTickets, retractWorker } = await import('../../src/worker/queue.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

/** Fake sessions retain their start/end times and stay live until the test opens the gate. */
async function fixture(providers, review = {}, providerHints = false) {
  const { repo, runId } = await makeRepo();
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const records = [];
  const tickets = { active: 0, max: 0, started: 0 };
  const files = providers.map((provider, i) => `src/${String.fromCharCode(97 + i)}.mjs`);
  for (const [i, file] of files.entries()) fs.writeFileSync(path.join(repo, file), `export const x = 1;\n${'// line\n'.repeat(i)}`);
  const cfg = providerHints ? { provider: providers[0], review: { ...review, routes: providers.map((provider, i) => ({ min_lines: i + 1, model: 'fake-reviewer', provider })) } } : { review };
  const worker = await createWorker({
    runId, repoRoot: repo, cfg, runRootDir: freshDir('runroot'),
    slug: 'worker-test', key: await loadKey(runId), pollMs: 10,
  }, {
    store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {},
    writeRow: async () => {}, readRows: async () => [],
    review: async (ticket, ctx) => {
      tickets.active += 1;
      tickets.started += 1;
      tickets.max = Math.max(tickets.max, tickets.active);
      try {
        const provider = providers[files.indexOf(ticket.file)];
        await ctx.spawn({ level: 'L2', role: 'reviewer', cfg: {
          provider, levels: { L2: { model: 'fake-reviewer' } }, review,
        } });
        return { status: 'reviewed', approved: true, engine: 'fake', sessions: [] };
      } finally { tickets.active -= 1; }
    },
    spawn: async (opts) => {
      const record = { provider: opts.cfg.provider, start: performance.now(), end: null };
      records.push(record);
      await gate;
      record.end = performance.now();
      return /** @type {any} */ ({ result: 'ok' });
    },
  });
  for (const [i, file] of files.entries()) enqueue({
    repoRoot: repo, run: runId, block: BLOCK, file, now: new Date(1_700_000_000_000 + i),
  });
  return { repo, runId, worker, records, tickets, release: () => release(), files };
}

function peak(records, provider) {
  const events = records.filter((r) => !provider || r.provider === provider)
    .flatMap((r) => [[r.start, 1], [r.end ?? Infinity, -1]])
    .sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  let active = 0;
  let max = 0;
  for (const [, delta] of events) max = Math.max(max, active += delta);
  return max;
}

test('default parallel_tickets caps seven queued reviews at six without binding provider caps', async () => {
  const p = await fixture(['anthropic', 'openai', 'anthropic', 'openai', 'anthropic', 'anthropic', 'openai'], {}, true);
  const drained = p.worker.drain();
  try {
    assert.equal(await waitFor(() => p.records.length >= 6, 5000), true, 'six reviewer sessions start');
    await sleep(100); // allow multiple drain ticks to try admitting the seventh ticket
    assert.equal(peak(p.records), 6, 'exactly six sessions overlap while the gate is held');
    assert.equal(p.tickets.max, 6, 'the default ticket pool admits at most six tickets');
  } finally { p.release(); await drained; }
  assert.equal(p.records.length, 7, 'all seven fake reviewer sessions finish');
  assert.ok(p.records.every((r) => r.end !== null), 'every reviewer session finishes after release');
  assert.equal(p.tickets.started, 7, 'all seven tickets run');
  assert.equal(peak(p.records), 6, 'six review intervals overlap with default configuration');
  assert.equal(pendingTickets(p.repo).length, 0, 'zero tickets remain pending');
});

test('provider_parallel 2 runs at most two of four same-provider sessions', async () => {
  const p = await fixture(Array(4).fill('anthropic'), { provider_parallel: 2 });
  const drained = p.worker.drain();
  try {
    await waitFor(() => p.records.length >= 2, 5000);
    await sleep(100);
  } finally { p.release(); await drained; }
  assert.equal(p.records.length, 4, 'all four reviews finish after waiting for provider slots');
  assert.equal(peak(p.records, 'anthropic'), 2, 'exactly two anthropic sessions overlap');
});

test('provider-waiting tickets count toward parallel_tickets 2 with four pending tickets', async () => {
  const p = await fixture(Array(4).fill('anthropic'), { parallel_tickets: 2, provider_parallel: 1 });
  const drained = p.worker.drain();
  try {
    assert.equal(await waitFor(() => p.tickets.started >= 2, 5000), true, 'two tickets enter the pool');
    await sleep(100); // multiple drain ticks with one active session and one provider waiter
    assert.equal(p.records.length, 1, 'the provider stays at its one-session cap');
    assert.equal(p.tickets.active, 2, 'the active session and provider waiter fill both ticket slots');
    assert.equal(p.tickets.started, 2, 'the other two tickets stay pending');
  } finally { p.release(); await drained; }
  assert.equal(p.tickets.max, 2, 'at most two tickets overlap throughout the drain');
  assert.equal(p.tickets.started, 4, 'all four tickets eventually run');
  assert.equal(pendingTickets(p.repo).length, 0, 'all four tickets complete');
});

test('default provider cap leaves other providers runnable behind saturated tickets', async () => {
  const p = await fixture([...Array(6).fill('anthropic'), 'openai'], {}, true);
  const drained = p.worker.drain();
  let otherStarted;
  try {
    otherStarted = await waitFor(() => p.records.some((r) => r.provider === 'openai'), 5000);
  } finally { p.release(); await drained; }
  assert.equal(otherStarted, true, 'openai starts while anthropic sessions are held');
  assert.equal(peak(p.records, 'anthropic'), 4, 'default provider cap permits exactly four anthropic sessions');
  assert.equal(p.records.length, 7, 'all seven sessions eventually finish');
  assert.ok(peak(p.records) <= 6, 'no more than six sessions overlap across providers');
  assert.ok(p.tickets.max <= 6, 'no more than six tickets overlap across providers');
});

async function join(p, deps = {}) {
  let stdout = '';
  let stderr = '';
  const code = await runReviewFile(['--wait-block', BLOCK, '--run', p.runId, '--max', '60s'], {
    cwd: p.repo, stdout: { write: (s) => { stdout += s; } },
    stderr: { write: (s) => { stderr += s; } }, ...deps,
  });
  assert.equal(code, 0, stderr);
  assert.equal(stderr, '', 'watching adds zero stderr bytes');
  return stdout;
}

test('wait-block wakes within one product second of the last completion at the five-second backoff', async (t) => {
  const p = await fixture(['anthropic', 'openai', 'anthropic']);
  announceWorker(p.repo, { pid: process.pid, run: p.runId });
  let notify;
  t.mock.method(fs, 'watch', (_dir, callback) => {
    notify = callback;
    return { on() { return this; }, close() {} };
  });
  syncBuiltinESMExports();
  t.after(() => { t.mock.restoreAll(); syncBuiltinESMExports(); });
  let time = 0;
  const sleeps = [];
  let cancelled = 0;
  let completedAt;
  let drained;
  try {
    const output = await join(p, {
      now: () => time,
      sleep: async (ms, signal) => {
        sleeps.push(ms);
        // Charge every later product wait to the wakeup limit, including another capped wait.
        if (completedAt !== undefined || ms < 5000) { time += ms; return; }
        const interrupted = new Promise((resolve) => {
          signal.addEventListener('abort', () => { cancelled += 1; resolve(undefined); }, { once: true });
        });
        p.release();
        drained = p.worker.drain();
        await drained;
        completedAt = time;
        notify('rename', 'last-ticket.done');
        await interrupted;
      },
    });
    await drained;
    assert.notEqual(completedAt, undefined, 'all reviews complete during the capped product wait');
    assert.ok(time - completedAt < 1000, `join waits ${time - completedAt} product ms after completion; limit is 1000 ms`);
    assert.deepEqual(sleeps, [200, 400, 800, 1600, 3200, 5000], 'six product backoff steps reach the cap; no extra poll follows completion');
    assert.equal(cancelled, 1, 'the completion notification cancels exactly one capped product wait');
    assert.deepEqual(JSON.parse(output).files.map((f) => f.status), ['done', 'done', 'done']);
    assert.equal(output, await join(p), 'completion output is byte-identical to an immediate join');
  } finally { p.release(); if (drained) await drained; retractWorker(p.repo, process.pid); }
});

test('wait-block falls back to polling when the done watch cannot start', async (t) => {
  const p = await fixture(['anthropic', 'openai', 'anthropic']);
  announceWorker(p.repo, { pid: process.pid, run: p.runId });
  let attempts = 0;
  t.mock.method(fs, 'watch', () => { attempts += 1; throw new Error('watch unavailable'); });
  syncBuiltinESMExports();
  t.after(() => { t.mock.restoreAll(); syncBuiltinESMExports(); });
  let polls = 0;
  let time = 0;
  try {
    const output = await join(p, {
      now: () => time,
      sleep: async (ms) => {
        time += ms;
        polls += 1;
        if (polls === 3) { p.release(); await p.worker.drain(); }
      },
    });
    assert.equal(attempts, 1, 'one failed watch setup falls back without retrying each poll');
    assert.equal(polls, 3, 'three fallback polls observe the completed reviews');
    assert.deepEqual(JSON.parse(output).files.map((f) => f.status), ['done', 'done', 'done']);
    assert.equal(JSON.parse(output).done, true, 'polling joins all three completed tickets');
  } finally { p.release(); retractWorker(p.repo, process.pid); }
});
