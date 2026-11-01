import './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import * as run from '../../src/state/run.mjs';
import { readStartTime } from '../../src/util/reaper.mjs';

const PADDED = 'Sun Nov  1 00:06:44 2026';
const NORMALIZED = 'Sun Nov 1 00:06:44 2026';

test('normalizeStartTime collapses whitespace and returns null for empty input', () => {
  assert.equal(typeof run.normalizeStartTime, 'function', 'normalizeStartTime is exported');
  assert.equal(run.normalizeStartTime(PADDED), NORMALIZED, 'one-digit day has one separating space');
  assert.equal(run.normalizeStartTime(` \tSun\nNov\t 1 00:06:44 2026\r\n`), NORMALIZED, 'every whitespace run is collapsed');
  assert.equal(run.normalizeStartTime('  \n'), null, 'whitespace-only input has no start time');
  assert.equal(run.normalizeStartTime(''), null, 'empty input has no start time');
});

test('processStartTime matches readStartTime for the live test process', async () => {
  const expected = readStartTime(process.pid);
  assert.equal(typeof expected, 'string', 'the live test process has a readable start time');
  assert.equal(await run.processStartTime(process.pid), expected, 'both probes return the same normalized identity');
  assert.doesNotMatch(await run.processStartTime(process.pid), /\s{2,}/, 'the live probe has no padded whitespace');
});

test('processStartTime normalizes raw padded ps stdout regardless of the current date', async (t) => {
  t.mock.module('../../src/util/exec.mjs', { namedExports: {
    exec: async () => ({ result: 'ok', code: 0, stdout: 'Sun Nov  1 00:06:44 2026\n' }),
  } });
  // Re-import so the probe binds to the mocked exec while the live test keeps its real exec.
  const probe = await import(new URL('../../src/state/run.mjs?padded-ps', import.meta.url).href);
  assert.equal(await probe.processStartTime(process.pid), NORMALIZED, 'the probe collapses padding in raw ps output');
});

test('legacy padded worker pins match live workers while different identities remain refused', async () => {
  const pin = { pid: 4242, started_at: PADDED };
  const probe = async (pid) => pid === pin.pid ? NORMALIZED : null;
  const record = { worker: pin };
  const pool = { worker: pin, workers: [pin, { pid: 4243, started_at: NORMALIZED }] };
  assert.deepEqual(await run.checkWorkerPin(record, { livePid: pin.pid, probe }), { ok: true }, 'legacy single-worker pin matches');
  assert.deepEqual(await run.checkWorkerPin(pool, { livePid: pin.pid, probe }), { ok: true }, 'selected legacy pool pin matches');
  assert.deepEqual(await run.checkWorkerPin(pool, { probe }), { ok: true }, 'one live legacy pool pin is sufficient');

  const signals = [];
  const kill = (pid, signal) => void signals.push([pid, signal]);
  assert.deepEqual(await run.stopPinnedWorker(pin, { probe, kill }), { stopped: true }, 'legacy pin authorizes stopping the same worker');
  assert.deepEqual(signals, [[pin.pid, 'SIGTERM']], 'exactly the pinned worker receives one SIGTERM');

  const restarted = async () => 'Sun Nov 1 00:06:45 2026';
  assert.deepEqual(await run.checkWorkerPin(record, { livePid: pin.pid, probe: restarted }), {
    ok: false, event: 'worker.replaced', reason: `pid ${pin.pid} was restarted (start time differs from the pin)`,
  }, 'a different start time is still rejected');
  assert.deepEqual(await run.checkWorkerPin(pool, { probe: restarted }), {
    ok: false, event: 'worker.down', reason: 'no pinned worker is running',
  }, 'a pool with no matching start times is rejected');
  assert.deepEqual(await run.stopPinnedWorker(pin, { probe: restarted, kill }), { stopped: false, reason: 'pid reused' });
  assert.deepEqual(signals, [[pin.pid, 'SIGTERM']], 'a reused pid receives no additional signal');
});
