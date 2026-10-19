import { BIN, childEnv, cli, freshDir, makeRepo, pollMs, startWorker, stopPid, stopWorker, VALID_REVIEW, queued, waitFor } from './helpers.mjs';
import { snapshotGuarded } from '../helpers/isolate.mjs';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runRecordPath, runsDir } = await import('../../src/state/paths.mjs');
const { enqueue, liveWorker, readResult, readWorker } = await import('../../src/worker/queue.mjs');
const ISOLATE = path.resolve(path.dirname(BIN), '../test/helpers/isolate.mjs');
const NO_LEAK = path.resolve(path.dirname(BIN), '../test/no-leak.test.mjs');

test('B113: removing the run record, runs directory, or HOME exits cleanly within 20 poll ticks, with 0 new tickets taken', async () => {
  for (const removed of ['record', 'runs', 'home']) {
    const { repo, runId } = await makeRepo();
    const w = await startWorker(repo, runId);
    try {
      rmSync(removed === 'record' ? runRecordPath(runId) : removed === 'runs' ? runsDir() : process.env.HOME, { recursive: true, force: true });
      const { ticket } = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
      assert.equal(await waitFor(() => w.child.exitCode !== null || w.child.signalCode !== null, Math.max(pollMs * 20, 20000)), true, removed);
      assert.deepEqual([w.child.exitCode, w.child.signalCode, readWorker(repo), readResult(repo, runId, ticket)], [0, null, null, null]);
      assert.equal(existsSync(path.join(repo, '.code-forge/queue', `${ticket}.done`)), false);
      if (removed === 'home') assert.equal(existsSync(/** @type {string} */ (process.env.HOME)), false, 'the stopping worker must not create the removed HOME again');
    } finally {
      try {
        await stopWorker(w, runId);
      } finally {
        if (removed === 'home') mkdirSync(process.env.HOME, { recursive: true });
      }
    }
  }
});

test('B113: an open run stays alive for 10 poll ticks and then completes exactly 1 queued ticket', async () => {
  const { repo, runId } = await makeRepo();
  const w = await startWorker(repo, runId, { FAKE_ANSWER: VALID_REVIEW });
  try {
    await new Promise((resolve) => setTimeout(resolve, 10 * pollMs));
    assert.deepEqual([w.child.exitCode, w.child.signalCode, readWorker(repo)?.pid], [null, null, w.pid]);
    const { ticket } = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: 'src/a.mjs' });
    assert.equal(await waitFor(() => readResult(repo, runId, ticket) !== null), true);
    assert.equal(readResult(repo, runId, ticket)?.approved, true);
    assert.deepEqual(queued(repo, 'done'), [ticket]);
    assert.deepEqual([w.child.exitCode, w.child.signalCode], [null, null]);
  } finally {
    await stopWorker(w, runId);
  }
});

/** @param {'snapshot'|'final'} mode @param {string} suite @param {Record<string, string>} [extra] */
async function noLeak(mode, suite, extra = {}) {
  const child = spawn(process.execPath, ['--import', ISOLATE, '--test', NO_LEAK], {
    cwd: path.dirname(BIN), env: childEnv({ CODE_FORGE_NO_LEAK: mode, CODE_FORGE_TEST_SUITE: suite, CODE_FORGE_REAL_HOME: process.env.CODE_FORGE_REAL_HOME, CODE_FORGE_REAL_TMPDIR: process.env.CODE_FORGE_REAL_TMPDIR, ...extra }), stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  child.stdout.on('data', (data) => { output += data; });
  child.stderr.on('data', (data) => { output += data; });
  const code = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error(`no-leak ${mode} timed out after 60 s:\n${output}`));
    }, 60000);
    child.on('error', (err) => { clearTimeout(timer); reject(err); });
    child.on('close', (code) => { clearTimeout(timer); resolve(code); });
  });
  return { code, output };
}

test('B113: the final no-leak check reports directory leaks and exactly 1 planted worker by pid and run, excluding another suite', async () => {
  const suite = `b113-planted-${process.pid}`;
  const realHome = freshDir('guarded-home');
  const extra = { CODE_FORGE_REAL_HOME: realHome };
  assert.equal((await noLeak('snapshot', suite, extra)).code, 0);
  const a = await makeRepo({ start: false });
  const b = await makeRepo({ start: false });
  try {
    const startedA = await cli(['run', 'start', '--run', a.runId], a.repo, { CODE_FORGE_TEST_SUITE: suite });
    assert.equal(startedA.code, 0, startedA.stdout + startedA.stderr);
    const startedB = await cli(['run', 'start', '--run', b.runId], b.repo, { CODE_FORGE_TEST_SUITE: `${suite}-other` });
    assert.equal(startedB.code, 0, startedB.stdout + startedB.stderr);
    const planted = liveWorker(a.repo);
    const other = liveWorker(b.repo);
    assert.ok(planted);
    assert.ok(other);
    const workerOnly = await noLeak('final', suite, extra);
    assert.equal(workerOnly.code, 1, workerOnly.output);
    assert.ok(workerOnly.output.includes(`leaked pid/run list ${JSON.stringify([{ pid: planted.pid, run: a.runId }])}`), workerOnly.output);
    assert.equal((await noLeak('snapshot', suite, extra)).code, 0);
    const leakedDir = path.join(realHome, '.code-forge');
    mkdirSync(leakedDir);
    writeFileSync(path.join(leakedDir, 'planted.json'), '{}');
    const final = await noLeak('final', suite, extra);
    assert.equal(final.code, 1, final.output);
    const diagnostics = final.output.split('\n').filter((line) => line.includes('leaked workers: '));
    assert.equal(diagnostics.length, 1, final.output);
    assert.equal(diagnostics[0].endsWith(JSON.stringify([{ pid: planted.pid, run: a.runId }])), true, final.output);
    assert.ok(final.output.includes('suite leaks: directories'), final.output);
    assert.ok(final.output.includes(path.join(leakedDir, 'planted.json')), final.output);
    assert.ok(final.output.includes(`leaked pid/run list ${JSON.stringify([{ pid: planted.pid, run: a.runId }])}`), final.output);
  } finally {
    const errors = [];
    for (const { repo, runId } of [a, b]) {
      try {
        const worker = liveWorker(repo);
        if (worker) await stopPid(worker.pid, runId);
      } catch (err) { errors.push(err); }
    }
    if (errors.length > 0) throw new AggregateError(errors, 'planted worker cleanup failed');
  }
});

test('B113: the snapshot refuses an empty suite marker with a clear diagnostic', async () => {
  const guarded = freshDir('empty-suite');
  const snapshot = await noLeak('snapshot', '', { CODE_FORGE_REAL_HOME: guarded, CODE_FORGE_REAL_TMPDIR: guarded });
  assert.equal(snapshot.code, 1, snapshot.output);
  assert.ok(snapshot.output.includes('CODE_FORGE_TEST_SUITE must be non-empty'), snapshot.output);
});

test('B113: the final check reports directory leaks together with an empty suite marker', async () => {
  const guarded = freshDir('final-empty-suite');
  const extra = { CODE_FORGE_REAL_HOME: guarded, CODE_FORGE_REAL_TMPDIR: guarded };
  const { createHash } = await import('node:crypto');
  const base = path.join(guarded, 'code-forge-tests');
  mkdirSync(base, { recursive: true, mode: 0o700 });
  const baseline = path.join(base, `no-leak-${createHash('sha256').update('').digest('hex')}.json`);
  writeFileSync(baseline, JSON.stringify([...snapshotGuarded([path.join(path.resolve(path.dirname(BIN), '..'), '.code-forge')])]));
  const leakedDir = path.join(guarded, '.code-forge');
  mkdirSync(leakedDir);
  const leakedFile = path.join(leakedDir, 'planted.json');
  writeFileSync(leakedFile, '{}');
  const final = await noLeak('final', '', extra);
  assert.equal(final.code, 1, final.output);
  assert.ok(final.output.includes('CODE_FORGE_TEST_SUITE must be non-empty'), final.output);
  assert.ok(final.output.includes(`suite leaks: directories ${JSON.stringify([leakedDir, leakedFile])}`), final.output);
});
