import { freshDir, makeRepo } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { appendRow } from '../../src/ledger/write.mjs';
import { processStartTime, startRun } from '../../src/state/run.mjs';
import { announceWorker } from '../../src/worker/queue.mjs';

const execFileAsync = promisify(execFile);
const BIN = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));
const DEAD_PID = 2147483646;
let sequence = 0;

async function fixture({ livePin = false } = {}) {
  const repo = await makeRepo();
  const run = `r-unpinned-${++sequence}`;
  const project = `unpinned-${sequence}`;
  const record = await startRun({
    workspace: repo, project, runId: run,
    config: { native: { daemon: false } },
    workerPid: livePin ? process.pid : DEAD_PID,
    probe: livePin ? processStartTime : async () => 'Sun Nov 1 00:00:00 2026',
    writeRow: (row) => appendRow(row, { slug: project }),
  });
  return { repo, run, record };
}

async function status(fx, json = false) {
  const { stdout, stderr } = await execFileAsync(process.execPath,
    [BIN, 'status', '--run', fx.run, ...(json ? ['--json'] : [])],
    { cwd: fx.repo, env: { ...process.env, CODE_FORGE_NO_ERROR_LOG: '1' } });
  return { stdout, stderr }; // execFile rejects nonzero exits and signal kills
}

const hint = (run, pid = process.pid) => `warning: live worker ${pid} is not pinned: run start --reattach --run ${run}`;
const deadWarning = (run) => `${hint(run)} (pinned pid ${DEAD_PID} is down)`;

test('a lone unpinned live worker warns with its run and dead or replaced pin; ended and unreadable runs stay quiet', async () => {
  const fx = await fixture();
  announceWorker(fx.repo, { pid: process.pid, run: fx.run });
  // This public behavior assertion also runs at base, before the new helper exists.
  assert.equal((await status(fx)).stderr, `${deadWarning(fx.run)}\n`, 'exactly one warning names the live worker, run and dead pin');
  const { unpinnedWorkerWarnings } = await import('../../src/state/worker-warning.mjs');
  assert.deepEqual(await unpinnedWorkerWarnings(fx.record), [deadWarning(fx.run)], 'default repo discovery finds the live registry worker');
  const replaced = { ...fx.record, workers: undefined, worker: { pid: process.pid, started_at: 'old identity' } };
  assert.deepEqual(await unpinnedWorkerWarnings(replaced), [
    `${hint(fx.run)} (pinned pid ${process.pid} is replaced)`,
  ], 'the same pid with another start time is unpinned and replaced');
  assert.deepEqual(await unpinnedWorkerWarnings({ ...fx.record, workers: [], worker: null }), [hint(fx.run)], 'no pin still warns without inventing a pinned pid');
  assert.deepEqual(await unpinnedWorkerWarnings({ ...fx.record, status: 'ended' }), [], 'ended runs emit zero warnings');
  assert.deepEqual(await unpinnedWorkerWarnings({ ...fx.record, workspace: freshDir('not-a-repo') }), [], 'a workspace outside git emits zero warnings');
  assert.deepEqual(await unpinnedWorkerWarnings(fx.record, {
    repoRoot: fx.repo, registry: () => { throw new Error('unreadable registry'); },
  }), [], 'an unreadable registry emits zero warnings');
  assert.deepEqual(await unpinnedWorkerWarnings(fx.record, {
    repoRoot: fx.repo, registry: () => [{ file: 'stale', live: false, worker: { pid: process.pid, run: fx.run, start_time: 'old' } }],
  }), [], 'a stale registry worker emits zero warnings despite the dead pin');
});

test('two unpinned workers each get a pid hint; matching single and pool pins and another run emit zero warnings', async (t) => {
  const fx = await fixture();
  const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' });
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) {
      const exited = once(child, 'exit');
      child.kill('SIGKILL');
      await exited;
    }
  });
  await once(child, 'spawn');
  for (const pid of [process.pid, child.pid]) announceWorker(fx.repo, { pid, run: fx.run });
  const expected = [process.pid, child.pid].map((pid) => `${hint(fx.run, pid)} --worker-pid ${pid}`).sort();
  assert.deepEqual((await status(fx)).stderr.trim().split('\n').sort(), expected, 'exactly two lines each select their own live pid');
  const { unpinnedWorkerWarnings } = await import('../../src/state/worker-warning.mjs');
  assert.deepEqual((await unpinnedWorkerWarnings(fx.record)).sort(), expected, 'unit result contains exactly the same two pid hints');

  const pinned = await fixture({ livePin: true });
  announceWorker(pinned.repo, { pid: process.pid, run: pinned.run });
  assert.deepEqual(await unpinnedWorkerWarnings(pinned.record), [], 'a live worker matching the single pin emits zero warnings');
  const padded = { ...pinned.record.worker, started_at: `  ${pinned.record.worker.started_at.replaceAll(' ', '  ')}\n` };
  assert.deepEqual(await unpinnedWorkerWarnings({ ...pinned.record, worker: fx.record.worker, workers: [fx.record.worker, padded] }), [], 'a normalized identity matching any pool pin emits zero warnings');
  assert.equal((await status(pinned)).stderr, '', 'a pinned worker leaves CLI stderr empty');

  const other = await fixture();
  announceWorker(other.repo, { pid: process.pid, run: 'r-another-run' });
  assert.deepEqual(await unpinnedWorkerWarnings(other.record), [], 'a live worker of another run emits zero warnings');
  assert.equal((await status(other)).stderr, '', 'another run leaves CLI stderr empty');
});

test('status warns on stderr in text and JSON modes while stdout keeps the same bytes and JSON keys', async () => {
  const fx = await fixture();
  const beforeText = await status(fx);
  const beforeJson = await status(fx, true);
  announceWorker(fx.repo, { pid: process.pid, run: fx.run });
  const text = await status(fx);
  const json = await status(fx, true);
  assert.deepEqual([text.stderr, json.stderr], [`${deadWarning(fx.run)}\n`, `${deadWarning(fx.run)}\n`], 'both successful CLI modes emit exactly one stderr warning');
  assert.deepEqual([beforeText.stderr, beforeJson.stderr], ['', ''], 'a dead pin alone emits zero warnings');
  assert.equal(text.stdout, beforeText.stdout, 'text stdout is byte-identical with the warning');
  assert.equal(text.stdout, `run ${fx.run} · active · plan · spent $0.00\n`, 'the text summary retains its original shape');
  assert.equal(json.stdout, beforeJson.stdout, 'JSON stdout is byte-identical with the warning');
  assert.deepEqual(JSON.parse(json.stdout), { runs: [{
    run_id: fx.run, status: 'active', mode: 'plan', spent_usd: 0, unknown_usd_sessions: 0,
    budget_usd: null, blocks: [], more: 0, closed: 0, stopped: 0,
  }] }, 'the one run has exactly the established JSON keys and values');
  assert.equal(json.stdout.split('\n').length, 2, 'stdout is exactly one JSON line and its newline');
});
