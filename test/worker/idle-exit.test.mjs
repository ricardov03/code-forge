import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { createWorker } from '../../src/worker/loop.mjs';
import { exec } from '../../src/util/exec.mjs';
import { enqueue, isDone, pendingTickets, readWorker, reviewsDir } from '../../src/worker/queue.mjs';
import { createKeyStore } from '../../src/keys/store.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';

const HOUR = 3600000;

/** @param {{hours?: number, ticketAt?: number, ledgerAt?: number, treeAt?: number, unwritable?: boolean, poisonTicket?: boolean, step?: number}} [opts] */
async function idleRun({ hours, ticketAt, ledgerAt, treeAt, unwritable = false, poisonTicket = false, step = HOUR } = {}) {
  const { repo, runId } = await makeRepo();
  mkdirSync(path.join(repo, '.code-forge', 'queue'), { recursive: true });
  const ticket = poisonTicket ? enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' }) : null;
  if (ticket) mkdirSync(path.join(reviewsDir(repo, runId), `${ticket.ticket}.json`, 'blocker'), { recursive: true });
  const start = Date.now();
  let now = start;
  let sleeps = 0;
  let forced = false;
  let reviews = 0;
  const rows = [];
  const history = [];
  const gitChecks = [];
  const key = await loadKey(runId);
  const worker = await createWorker({ runId, repoRoot: repo, runRootDir: freshDir('idle-root'), slug: 'worker-test', key,
    cfg: { native: { daemon: false }, ...(hours === undefined ? {} : { worker: { idle_exit_hours: hours } }) } }, {
    now: () => now,
    store: await createKeyStore({ backends: [], dir: freshDir('idle-store') }), env: {},
    readRun: async () => ({ run_id: runId, started_at: new Date(start).toISOString(), blocks: {
      open: { status: 'open', worktree: { path: repo } },
      removed: { status: 'open', worktree: { path: path.join(repo, 'removed') } },
    } }),
    readRows: async () => history,
    idleExec: async (argv, opts) => { gitChecks.push(now - start); return exec(argv, opts); },
    writeRow: async (row) => { if (unwritable) throw new Error('read only'); rows.push(row); },
    expiryCheck: async () => {},
    review: async () => { reviews++; return { status: 'reviewed', approved: true, engine: 'test', sessions: [] }; },
    sleep: async () => {
      now += step;
      sleeps++;
      if (sleeps === ticketAt) enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
      if (sleeps === ledgerAt) history.push({ run: runId, event: 'anything', ts: new Date(now).toISOString() });
      if (sleeps === treeAt) writeFileSync(path.join(repo, 'src/a.mjs'), 'export const a = 2;\n');
      if (sleeps === 60) { forced = true; worker.stop(); }
    },
  });
  await worker.run();
  assert.equal(readWorker(repo), null, 'the idle stop removes its heartbeat');
  return { rows: rows.filter((r) => r.event === 'worker.idle_exit'), sleeps, forced, key, runId, gitChecks, repo, ticket, reviews };
}

test('default idle timeout exits at 24 hours with exactly one signed row', async () => {
  const result = await idleRun();
  assert.equal(result.forced, false, 'worker exits without an external stop');
  assert.equal(result.sleeps, 24, 'default timeout is 24 hours');
  assert.equal(result.rows.length, 1, 'one idle exit row');
  assert.equal(result.rows[0].run, result.runId);
  assert.deepEqual(verifyRow(result.rows[0], result.key), { ok: true }, 'idle exit row verifies with the run key');
});

test('a queued ticket, any run ledger row or changed open worktree resets the idle clock', async () => {
  for (const activity of [{ ticketAt: 23 }, { ledgerAt: 23 }, { treeAt: 23 }]) {
    const result = await idleRun(activity);
    assert.equal(result.forced, false, 'activity postpones, but does not disable idle exit');
    assert.equal(result.sleeps, 47, '23 hours plus a fresh 24 hour idle window');
    assert.equal(result.rows.length, 1, 'one idle exit after new activity');
    if (activity.ticketAt) assert.equal(result.gitChecks.filter((at) => at === 23 * HOUR).length, 0, 'a queued ticket adds zero idle git calls');
  }
});

test('a poisoned pending ticket without a done marker does not block idle exit', async () => {
  const result = await idleRun({ hours: 1, poisonTicket: true });
  assert.ok(result.ticket);
  assert.equal(result.reviews, 1, 'the poisoned ticket is reviewed once and skipped thereafter');
  assert.deepEqual(pendingTickets(result.repo), [result.ticket.ticket], 'the failed result write leaves the ticket pending');
  assert.equal(isDone(result.repo, result.ticket.ticket), false, 'no done marker was published');
  assert.equal(result.forced, false, 'the poisoned ticket does not require an external stop');
  assert.equal(result.sleeps, 1, 'worker exits after the configured idle hour');
  assert.equal(result.rows.length, 1, 'one idle exit row');
});

test('idle activity scans run at most once a minute', async () => {
  const result = await idleRun({ hours: 1 / 60, step: 10000 });
  assert.equal(result.forced, false);
  assert.equal(result.sleeps, 6, 'one minute reaches the configured timeout');
  assert.deepEqual(result.gitChecks, [0, 0, 60000, 60000], 'only the initial and one-minute worktree scans run');
});

test('zero disables idle exit and an unwritable ledger still permits clean exit', async () => {
  const disabled = await idleRun({ hours: 0 });
  assert.equal(disabled.sleeps, 60);
  assert.equal(disabled.forced, true);
  assert.equal(disabled.rows.length, 0, 'disabled timeout writes no idle exit');
  const unwritable = await idleRun({ hours: 1, unwritable: true });
  assert.equal(unwritable.forced, false, 'ledger failure does not keep an idle worker alive');
  assert.equal(unwritable.sleeps, 1);
  assert.equal(unwritable.rows.length, 0);
});
