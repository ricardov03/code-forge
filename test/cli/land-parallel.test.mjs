import { cfgFor, freshDir, git, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { syncBuiltinESMExports } from 'node:module';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { test } from 'node:test';

const { landBlock } = await import('../../src/cli/land.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runGatesVerb } = await import('../../src/cli/gates.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { startRun, readRun } = await import('../../src/state/run.mjs');
const { signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const { parseTapPassed } = await import('../../src/gates/acceptance.mjs');
const { exec } = await import('../../src/util/exec.mjs');

let serial = 0;

for (const queuedTimeout of [false, true]) {
  test(`exec sends no SIGKILL after exit when aborted before its timeout elapses (queued timeout: ${queuedTimeout})`, { timeout: 10000 }, async (t) => {
    const controller = new AbortController();
    const spawn = childProcess.spawn;
    let child, ready, exited;
    let hasExited = false;
    const signals = [];
    const spawnSpy = t.mock.method(childProcess, 'spawn', (command, args, options) => {
      child = spawn(command, args, options);
      ready = once(child.stdout, 'data');
      exited = once(child, 'exit');
      child.once('exit', () => { hasExited = true; });
      return child;
    });
    syncBuiltinESMExports();
    const kill = process.kill;
    const killSpy = t.mock.method(process, 'kill', (pid, signal) => {
      if (pid === -child?.pid) {
        // Hold the real child alive across both cancellation and the timeout deadline.
        signals.push({ signal, afterExit: hasExited });
        return true;
      }
      return kill.call(process, pid, signal);
    });
    t.mock.timers.enable({ apis: ['setTimeout'] });
    const setTimeout = globalThis.setTimeout;
    let timeoutCallback;
    const timerSpy = t.mock.method(globalThis, 'setTimeout', (callback, delay, ...args) => {
      if (delay === 100) timeoutCallback = callback;
      return setTimeout(callback, delay, ...args);
    });
    try {
      const pending = exec([process.execPath, '-e', "console.log('ready'); setInterval(() => {}, 1000)"], {
        signal: controller.signal, timeoutMs: 100,
      });
      await ready;
      controller.abort();
      t.mock.timers.tick(100);
      // A timer callback already queued for delivery must also respect cancellation.
      if (queuedTimeout) timeoutCallback();
      assert.equal(hasExited, false, 'the timeout deadline passes before the child exits');
      child.kill('SIGTERM');
      await exited;
      const result = await pending;
      assert.equal(hasExited, true, 'the child exits before either escalation deadline');
      // Check the abort's original deadline separately from the timeout's later deadline:
      // overwriting killTimer used to leave the first handle alive after exit.
      t.mock.timers.tick(1899);
      assert.deepEqual(signals.filter(({ signal }) => signal === 'SIGKILL'), [], 'no escalation before the grace period');
      t.mock.timers.tick(1);
      assert.deepEqual(signals.filter(({ signal, afterExit }) => signal === 'SIGKILL' && afterExit), [],
        'the abort escalation timer must not signal the exited process group');
      t.mock.timers.tick(100);
      assert.deepEqual(signals, [{ signal: 'SIGTERM', afterExit: false }], 'abort sends exactly one SIGTERM');
      assert.equal(result.cancelled, true);
      assert.equal(result.timedOut, false);
      assert.equal(result.result, 'failed');
    } finally {
      timerSpy.mock.restore();
      t.mock.timers.reset();
      if (child && !hasExited) {
        child.kill('SIGKILL');
        await exited;
      }
      killSpy.mock.restore();
      spawnSpy.mock.restore();
      syncBuiltinESMExports();
    }
  });
}

/** @param {{finding?: boolean, red?: boolean, joinDelay?: number, gateDelay?: number, waitForGates?: boolean, change?: (repo: string) => Promise<void>, throwJoin?: boolean, throwGate?: boolean}} [settings] */
async function fixture(settings = {}) {
  const repo = await makeRepo();
  const slug = `land-parallel-${++serial}`;
  const runId = `r-${slug}`;
  const key = randomBytes(32);
  const record = { workspace: repo, project: slug, blocks: { B1: { status: 'open', base_branch: 'main' } } };
  let closes = 0, gates = 0, joins = 0, stdout = '', stderr = '';
  let started;
  const gatesStarted = new Promise((resolve) => { started = resolve; });
  /** @type {import('../../src/cli/land.mjs').LandDeps} */
  const deps = {
    stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } },
    autoClaimChanges: async () => {}, blockTree: async () => ({ top: repo, path: repo, workspace: repo, branch: 'block' }),
    repoTop: async () => repo, configInForce: () => ({}), gitChildEnv, parseTapPassed,
    appendRow, signRow, loadKey: async () => key,
    runBlock: async (_args, streams) => { closes++; streams.stdout.write(JSON.stringify({ status: 'closed' })); return 0; },
    runReviewFile: async (args, streams) => {
      if (args[0] === '--wait') { streams.stdout.write('F1: review finding'); return 0; }
      joins++;
      if (settings.waitForGates) {
        let timer;
        try {
          await Promise.race([gatesStarted, new Promise((_, reject) => {
            timer = setTimeout(() => reject(new Error('gates did not start during join within 2 s')), 2000);
          })]);
        } finally { clearTimeout(timer); }
      }
      await sleep(settings.joinDelay ?? 0);
      if (settings.change) await settings.change(repo);
      if (settings.throwJoin) throw new Error('join crashed');
      streams.stdout.write(JSON.stringify({ done: true, files: [{ file: 'a.mjs', status: settings.finding ? 'findings' : 'reviewed', outcome: settings.finding ? 'findings' : 'approved', ticket: 'ticket' }] }));
      return 0;
    },
    runGatesVerb: async (_args, streams) => {
      gates++;
      started();
      await sleep(settings.gateDelay ?? 0);
      if (settings.throwGate) throw new Error('gates crashed');
      streams.stdout.write(JSON.stringify({ allOk: !settings.red }));
      if (settings.red) streams.stderr.write('gate failure');
      return settings.red ? 1 : 0;
    },
  };
  return {
    repo, slug, runId, key, deps,
    land: () => landBlock({ id: 'B1', runId, record, report: 'report', transcript: 'transcript', maxMs: 10_000, merge: false, noMergeGate: false, json: false }, deps),
    result: () => ({ closes, gates, joins, stdout, stderr }),
  };
}

test('land starts gates during the join and closes exactly once only after both pass', async () => {
  const f = await fixture({ waitForGates: true });
  assert.equal(await f.land(), 0, 'parallel join completes within its 2 s gate-start deadline');
  assert.deepEqual(f.result(), { closes: 1, gates: 1, joins: 1, stderr: '', stdout: `hint: the merge gate runs only with --merge\nblock B1 closed; merge: git -C ${f.repo} merge --no-ff --no-edit -- block\n` }, 'clean land preserves the exact output and calls each stage once');
});

test('findings and gate failures both prevent close and are reported together', async () => {
  for (const [finding, red] of [[true, false], [false, true], [true, true]]) {
    const f = await fixture({ finding, red });
    assert.equal(await f.land(), 1, 'either failed prerequisite stops land');
    assert.equal(f.result().closes, 0, 'zero close calls when either prerequisite fails');
    assert.equal(f.result().gates, 1, 'exactly one gate run even when join has findings');
    assert.equal(f.result().joins, finding ? 3 : 1, 'findings retain the existing three-attempt join cap');
    if (finding) assert.match(f.result().stderr, /a\.mjs: findings.*F1: review finding/s, 'stop includes the review finding and file');
    if (red) assert.match(f.result().stderr, /gate failure/, 'same stop includes the gate failure');
    assert.equal(f.result().stderr.split('\n').filter((line) => line.startsWith('next: ')).length, 1, 'exactly one stop with one next command');
  }
});

test('HEAD or porcelain changes during join invalidate the first gate run', async () => {
  for (const committed of [false, true]) {
    const f = await fixture({ change: async (repo) => {
      if (committed) await git(['commit', '-q', '--allow-empty', '-m', 'join advances HEAD'], repo);
      else writeFile(repo, 'during-join.txt', 'changed\n');
    } });
    assert.equal(await f.land(), 0, 'fresh passing gates permit close after a tree change');
    assert.equal(f.result().gates, 2, 'exactly two gate runs when HEAD or porcelain changes');
    assert.equal(f.result().closes, 1, 'exactly one close after fresh gates pass');
  }
});

test('rejected join and gate promises settle together and never close', async () => {
  const f = await fixture({ throwJoin: true, throwGate: true, gateDelay: 60 });
  assert.equal(await f.land(), 1, 'rejected prerequisites stop land');
  assert.deepEqual([f.result().closes, f.result().gates], [0, 1], 'zero closes and exactly one gate run despite a rejected join');
  assert.match(f.result().stderr, /join crashed.*gates crashed/s, 'one stop reports both rejected prerequisites');
});

test('each land writes one signed timing row and real scoped gates retain duration evidence', async () => {
  for (const red of [false, true]) {
    const f = await fixture({ joinDelay: 140, gateDelay: 90, red });
    assert.equal(await f.land(), red ? 1 : 0, 'both success and stop finish');
    const rows = (await readAllRows(f.slug)).filter((row) => row.event === 'land.timing');
    assert.equal(rows.length, 1, 'exactly one land.timing row after each pass or stop');
    const [row] = rows;
    assert.deepEqual([row.run, row.block, row.parallel, row.merge_gate_ms], [f.runId, 'B1', true, null], 'timing identifies this run/block and no merge gate');
    assert.ok(Math.abs(row.join_ms - 140) <= 50, `join_ms ${row.join_ms} measures the 140 ms fake join within 50 ms`);
    assert.ok(Math.abs(row.gates_ms - 90) <= 50, `gates_ms ${row.gates_ms} measures the 90 ms fake gate within 50 ms`);
    assert.deepEqual(verifyRow(row, f.key), { ok: true }, 'stored land timing signature verifies');
  }

  const repo = await makeRepo();
  const slug = 'land-real-duration';
  const runId = `r-${slug}`;
  const config = { version: 1, project: { slug }, ...cfgFor(), gates: { test: [process.execPath, '-e', 'setTimeout(() => {}, 80)'], lint: null, types: null, format: null } };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  await git(['add', '.code-forge.yml'], repo);
  await git(['commit', '-q', '-m', 'config'], repo);
  const inputs = freshDir('duration-inputs');
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'duration', tests: ['duration'] }]));
  const probe = async (pid) => pid === process.pid ? 'test-worker' : null;
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, probe, config, writeRow: (row) => appendRow(row, { slug }) });
  const streams = { stdout: { write: (_s) => {} }, stderr: { write: (_s) => {} } };
  assert.equal(await runBlock(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', '.code-forge.yml', '--acceptance', path.join(inputs, 'acceptance.json')], { ...streams, probe }), 0, 'opens one real run block');
  assert.equal((await readRun(runId)).blocks.B1.status, 'open');
  assert.equal(await runGatesVerb(['run', '--run', runId, '--block', 'B1', '--cwd', repo], streams), 0, 'real scoped gate passes');
  const done = (await readAllRows(slug)).filter((row) => row.event === 'gate.done');
  assert.equal(done.length, 1, 'exactly one gate.done row for one scoped gate run');
  assert.deepEqual([done[0].run, done[0].block, done[0].all_ok], [runId, 'B1', true]);
  assert.ok(done[0].duration_ms >= 80, 'gate.done records at least the 80 ms gate delay');
  assert.equal(done[0].results.filter((result) => result.gate === 'test' && result.duration_ms >= 80).length, 1, 'one test gate retains its individual duration');
});
