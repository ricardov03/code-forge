import { BLOCK, freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import path from 'node:path';
import { test } from 'node:test';

const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { announceWorker, retractWorker, enqueue, blockTickets, writeResult } = await import('../../src/worker/queue.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

/** @param {import('node:test').TestContext} t */
function counters(t) {
  const counts = { hashes: 0, git: 0 };
  const read = fs.readFileSync;
  t.mock.method(fs, 'readFileSync', (file, ...args) => {
    const bytes = read(file, ...args);
    if (typeof file === 'number' && bytes.toString().startsWith('export const')) counts.hashes += 1;
    return bytes;
  });
  for (const method of /** @type {const} */ (['spawn', 'execFile', 'spawnSync', 'execFileSync'])) {
    const original = childProcess[method];
    t.mock.method(childProcess, method, (command, ...args) => {
      if (command === 'git') counts.git += 1;
      return Reflect.apply(original, childProcess, [command, ...args]);
    });
  }
  syncBuiltinESMExports();
  t.after(() => { t.mock.restoreAll(); syncBuiltinESMExports(); });
  return counts;
}

/** @param {import('node:test').TestContext} t @param {(poll: number, repo: string, run: string) => Promise<void>} change */
async function join(t, change) {
  const { repo, runId } = await makeRepo();
  announceWorker(repo, { pid: process.pid, run: runId });
  const counts = counters(t);
  activeCounts = counts;
  // Legacy code has no injected clock: let its real-clock timeout finish immediately.
  let legacyTime = Date.now();
  t.mock.method(Date, 'now', () => (legacyTime += 60_000));
  let time = 0;
  const sleeps = [];
  let output = '';
  let errors = '';
  try {
    const code = await runReviewFile(['--wait-block', BLOCK, '--run', runId, '--max', '120s'], {
      cwd: repo, now: () => time,
      sleep: async (ms) => {
        sleeps.push(ms);
        assert.ok(sleeps.length <= 20, 'join terminates within 20 injected polls');
        await change(sleeps.length, repo, runId);
        time += ms;
      },
      stdout: { write: (s) => { output += s; } }, stderr: { write: (s) => { errors += s; } },
    });
    assert.equal(code, 0, errors);
    assert.equal(errors, '');
    return { sleeps, counts, result: JSON.parse(output) };
  } finally { retractWorker(repo, process.pid); }
}

test('idle block wait and worker grow sleeps to 5 s and reset on ticket completion or new work', { timeout: 30_000 }, async (t) => {
  const joined = await join(t, async (poll, repo, runId) => {
    const tickets = blockTickets({ repoRoot: repo, run: runId, block: BLOCK });
    if (poll === 8 || poll === 10) {
      const key = await loadKey(runId);
      for (const ticket of poll === 8 ? tickets.slice(0, 1) : tickets.slice(1)) {
        writeResult({ repoRoot: repo, runId, ticket: ticket.ticket, key, result: { status: 'reviewed', approved: true } });
      }
    }
  });
  const { repo, runId } = await makeRepo();
  const sleeps = [];
  const worker = await createWorker({ runId, repoRoot: repo, cfg: {}, key: await loadKey(runId), slug: 'worker-test', runRootDir: freshDir('idle-root') }, {
    env: {}, store: await createKeyStore({ backends: [], dir: freshDir('idle-store') }),
    writeRow: async () => {}, expiryCheck: async () => {},
    review: async () => ({ status: 'reviewed', approved: true, engine: 'test', sessions: [] }),
    sleep: async (ms) => {
      sleeps.push(ms);
      if (sleeps.length === 8) enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: 'src/a.mjs' });
      if (sleeps.length === 10) worker.stop();
    },
  });
  t.after(() => worker.stop());
  await worker.run();
  assert.deepEqual(sleeps, [250, 500, 1000, 2000, 4000, 5000, 5000, 5000, 250, 500], 'empty worker caps at 5000 ms and a new ticket resets to 250 ms');
  assert.deepEqual(joined.sleeps, [200, 400, 800, 1600, 3200, 5000, 5000, 5000, 200, 400], 'block wait caps at 5000 ms and completion resets to 200 ms');
  assert.equal(joined.result.done, true, 'all three completed tickets finish the join');
});

test('idle block polls add zero hashes or Git calls; mtime and size independently invalidate only one hash', async (t) => {
  let baseline;
  const snapshots = [];
  const joined = await join(t, async (poll, repo, runId) => {
    // Count through the public builtin seams, including hashes hidden in ticket resolution.
    snapshots.push({ ...activeCounts });
    if (poll === 1) baseline = { ...activeCounts };
    if (poll === 8) {
      const file = path.join(repo, 'src/a.mjs');
      const stat = fs.statSync(file, { bigint: true });
      fs.writeFileSync(file, 'export const a = 2;\n');
      // Whole seconds remain exactly representable when restored on the next poll.
      fs.utimesSync(file, stat.atime, Number(stat.mtimeNs / 1_000_000_000n) + 2);
      const edited = fs.statSync(file, { bigint: true });
      assert.equal(edited.size, stat.size, 'mtime-only edit preserves file size');
      assert.notEqual(edited.mtimeNs, stat.mtimeNs, 'mtime-only edit changes mtime');
    }
    if (poll === 9) {
      const file = path.join(repo, 'src/a.mjs');
      const stat = fs.statSync(file, { bigint: true });
      fs.appendFileSync(file, '// larger\n');
      fs.utimesSync(file, stat.atime, Number(stat.mtimeNs) / 1_000_000_000);
      const edited = fs.statSync(file, { bigint: true });
      assert.equal(edited.mtimeNs, stat.mtimeNs, 'size-only edit preserves nanosecond mtime');
      assert.ok(edited.size > stat.size, 'size-only edit increases file size');
    }
    if (poll === 10) {
      const key = await loadKey(runId);
      for (const ticket of blockTickets({ repoRoot: repo, run: runId, block: BLOCK })) {
        writeResult({ repoRoot: repo, runId, ticket: ticket.ticket, key, result: { status: 'reviewed', approved: true } });
      }
    }
  });
  assert.equal(snapshots.length, 10, 'ten polls were observed');
  assert.equal(baseline.hashes, 3, 'initial join hashes each of three files exactly once');
  assert.ok(baseline.git > 0, 'initial join records Git calls through the live counter');
  assert.deepEqual(snapshots.slice(0, 8), Array(8).fill(baseline), 'eight idle polls add zero hashes and zero Git calls');
  assert.deepEqual(snapshots.slice(8).map((s) => s.hashes), [4, 5], 'mtime-only and size-only edits each add exactly one hash');
  assert.deepEqual(joined.sleeps.slice(8), [200, 200], 'each metadata change resets the wait');
});

let activeCounts;

test('new and deleted owned paths invalidate discovery and reset a capped block wait', async (t) => {
  const joined = await join(t, async (poll, repo, runId) => {
    if (poll === 8) fs.writeFileSync(path.join(repo, 'src/d.mjs'), 'export const d = 1;\n');
    if (poll === 9) fs.rmSync(path.join(repo, 'src/b.mjs'));
    if (poll === 10) {
      const key = await loadKey(runId);
      for (const ticket of blockTickets({ repoRoot: repo, run: runId, block: BLOCK })) {
        writeResult({ repoRoot: repo, runId, ticket: ticket.ticket, key, result: { status: 'reviewed', approved: true } });
      }
    }
  });
  assert.deepEqual(joined.result.files.map((f) => [f.file, f.status]), [['src/a.mjs', 'done'], ['src/c.mjs', 'done'], ['src/d.mjs', 'done']], 'new untracked file joins and deleted untracked file leaves the set');
  assert.deepEqual(joined.sleeps.slice(8), [200, 200], 'addition and deletion each reset a capped wait');
});
