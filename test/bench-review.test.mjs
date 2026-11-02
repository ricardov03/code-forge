// B44: `scripts/bench-review.mjs` runs as a child process (own temp HOME, never ours) with 300 ms
// fake reviews, so CI stays fast: 6 tickets at parallel_tickets 3 must finish at least 1.5x faster
// than at 1 (ideal 3x; 1.5x leaves room for a loaded machine while excluding serial work).
import { BLOCK, freshDir, makeRepo } from './worker/helpers.mjs';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, test } from 'node:test';
import { exec } from '../src/util/exec.mjs';
import { parseArgs } from '../scripts/bench-review.mjs';

const { createWorker } = await import('../src/worker/loop.mjs');
const { enqueue } = await import('../src/worker/queue.mjs');
const { loadKey } = await import('../src/state/signer.mjs');
const { createKeyStore } = await import('../src/keys/store.mjs');

const SCRIPT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'scripts', 'bench-review.mjs');

/** @param {string[]} args */
const bench = (args) => exec([process.execPath, SCRIPT, ...args], { env: { PATH: [path.dirname(process.execPath), '/usr/bin', '/bin'].join(path.delimiter), HOME: String(process.env.HOME), TMPDIR: String(process.env.TMPDIR ?? '/tmp') }, timeoutMs: 120000 });

describe('scripts/bench-review.mjs (B44)', () => {
  test('300 ms fake reviews over 6 tickets: parallel_tickets 3 is at least 1.5x faster than 1, and the text output names both times', async (t) => {
    const json = await bench(['--review-ms', '300', '--json']);
    assert.equal(json.code, 0);
    const res = JSON.parse(json.stdout.trim());
    assert.deepEqual([res.reviewMs, res.files, res.runs.map((r) => r.tickets)], [300, 6, [1, 3]]);
    const [one, three] = res.runs;
    t.diagnostic(`N=1 ${one.ms} ms, N=3 ${three.ms} ms, speedup ${res.speedup}x`);
    assert.equal(one.ms >= 6 * 300, true); // serial: at least 6 reviews back to back
    const timing = `N=1 ${one.ms} ms, N=3 ${three.ms} ms`;
    assert.equal(one.ms / three.ms >= 1.5, true, timing);
    assert.equal(res.speedup, Math.round((one.ms / three.ms) * 100) / 100);

    const text = await bench(['--review-ms', '50', '--files', '2', '--tickets', '1,2']);
    assert.equal(text.code, 0);
    const lines = text.stdout.trim().split('\n');
    assert.equal(lines.length, 4);
    assert.equal(lines[0], '2 tickets, fake review 50 ms each');
    assert.match(lines[1] ?? '', /^parallel_tickets 1: \d+ ms$/);
    assert.match(lines[2] ?? '', /^parallel_tickets 2: \d+ ms$/);
    assert.match(lines[3] ?? '', /^speed-up: [\d.]+x \(parallel_tickets 2 vs 1\)$/);
  });

  test('bad arguments exit 2 with the usage line; parseArgs defaults are 2000 ms, 6 files, 1 and 3', async () => {
    assert.deepEqual(parseArgs([]), { reviewMs: 2000, files: 6, tickets: [1, 3], json: false });
    const bad = await bench(['--tickets', '3']);
    assert.equal(bad.code, 2);
    assert.match(bad.stderr, /^bench-review: --tickets needs two or more whole numbers from 1 to 16, like 1,3\nusage: /);
  });
});

test('a ticket arriving during capped idle back-off wakes the worker within the fast interval', { timeout: 30_000 }, async (t) => {
  const { repo, runId } = await makeRepo();
  let idle;
  let resume;
  let reviewed;
  const capped = new Promise((resolve) => { idle = resolve; });
  const sleeping = new Promise((resolve) => { resume = resolve; });
  const started = new Promise((resolve) => { reviewed = resolve; });
  const sleeps = [];
  let elapsed = 0;
  let notify;
  const watcher = Object.assign(new EventEmitter(), { close() {} });
  t.mock.method(fs, 'watch', (_dir, listener) => { notify = listener; return watcher; });
  syncBuiltinESMExports();
  t.after(() => { t.mock.restoreAll(); syncBuiltinESMExports(); });
  const worker = await createWorker({ runId, repoRoot: repo, cfg: {}, key: await loadKey(runId), slug: 'worker-test', runRootDir: freshDir('wake-root') }, {
    env: {}, store: await createKeyStore({ backends: [], dir: freshDir('wake-store') }),
    expiryCheck: async () => {}, writeRow: async () => {}, readRows: async () => [],
    sleep: async (ms) => {
      sleeps.push(ms);
      if (ms === 5000) { idle(); await sleeping; }
      elapsed += ms;
    },
    review: async (ticket) => {
      reviewed(ticket.file);
      worker.stop();
      return { status: 'reviewed', approved: true, engine: 'test', sessions: [] };
    },
  });
  const running = worker.run();
  t.after(async () => { worker.stop(); resume(); await running; });
  try {
    await capped;
    const { ticket } = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' });
    const arrived = elapsed;
    // Deliver the rename explicitly. The capped sleep stays pending throughout dispatch:
    // a wake must start this ticket without consuming even the 250 ms fast interval.
    notify('rename', `${ticket}.json`);
    assert.equal(await started, 'src/a.mjs', 'the notification dispatches exactly the waiting file');
    assert.equal(elapsed - arrived, 0, 'the ticket starts before any of the 250 ms fast interval elapses');
    assert.deepEqual(sleeps, [250, 500, 1000, 2000, 4000, 5000], 'the empty queue still backs off to 5 s');
  } finally {
    worker.stop();
    resume();
    await running;
  }
});
