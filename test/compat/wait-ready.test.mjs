import assert from 'node:assert/strict';
import { setTimeout as delay } from 'node:timers/promises';
import { test } from 'node:test';
import { waitReady } from '../helpers/wait-ready.mjs';

test('waitReady returns the third probe answer without delaying the fast path', async () => {
  const answer = { pong: true };
  const started = Date.now();
  let calls = 0;
  const waiting = waitReady(() => {
    calls++;
    return [false, undefined, answer][calls - 1];
  });
  assert.equal(calls, 1, 'the first probe runs immediately without a polling delay');
  const actual = await waiting;
  assert.equal(actual, answer, 'the third probe answer is returned unchanged');
  assert.equal(calls, 3, 'false and undefined each trigger exactly one retry');
  assert.ok(Date.now() - started < 200, 'three fast probes finish within 200 ms');
});

test('waitReady retries two thrown probes and returns the next answer', async () => {
  let calls = 0;
  const actual = await waitReady(() => {
    calls++;
    if (calls < 3) throw new Error('socket not bound');
    return 0;
  });
  assert.equal(actual, 0, 'a successful falsy answer is returned unchanged');
  assert.equal(calls, 3, 'two thrown probes cause exactly two retries');
});

test('waitReady times out at the wall-clock deadline when no probe answers', async () => {
  const started = Date.now();
  const error = await waitReady(() => false, { timeoutMs: 100 }).catch((error) => error);
  const elapsed = Date.now() - started;
  assert.ok(error instanceof Error, 'an unanswered probe rejects with an Error');
  assert.match(error.message, /^waitReady timeout after \d+ ms$/, 'the timeout reports its reason and waited milliseconds');
  assert.ok(elapsed >= 100, 'the 100 ms deadline is respected');
  assert.ok(elapsed < 1000, 'an unanswered probe stops within one second');
});

test('waitReady counts slow probes toward the deadline rather than counting tries', async () => {
  const started = Date.now();
  let calls = 0;
  const error = await waitReady(async () => {
    calls++;
    await delay(60);
    return false;
  }, { timeoutMs: 150 }).catch((error) => error);
  const elapsed = Date.now() - started;
  assert.ok(error instanceof Error, 'slow unanswered probes reject with an Error');
  assert.match(error.message, /^waitReady timeout after \d+ ms$/, 'slow probes report the timeout and waited milliseconds');
  assert.ok(calls >= 1 && calls <= 3, 'the 150 ms deadline permits at most three 60 ms probes');
  assert.ok(elapsed >= 150 && elapsed < 1000, 'slow probes stop by elapsed time');
});

test('waitReady stops immediately after the second probe when the child exits', async () => {
  const started = Date.now();
  let calls = 0;
  const error = await waitReady(() => {
    calls++;
    return false;
  }, { timeoutMs: 15000, intervalMs: 200, stop: () => calls === 2 }).catch((error) => error);
  assert.ok(error instanceof Error, 'stopping rejects with an Error');
  assert.match(error.message, /^waitReady stopped after \d+ ms$/, 'the stop reports its reason and waited milliseconds');
  assert.equal(calls, 2, 'the stop condition prevents a third probe');
  assert.ok(Date.now() - started < 350, 'stopping skips the next 200 ms polling interval');
});
