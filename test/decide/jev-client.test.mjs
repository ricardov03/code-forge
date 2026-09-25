/**
 * Jev transport tests (plan §3.2, §3.5) — "401/422/429/529 paths (4 tests)", plus success,
 * backoff timing, network failures and malformed responses. Every `fetch` here is a fake:
 * `askJev` is never allowed to touch the real network in a test.
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { askJev, JEV_MODEL, JEV_URL, RETRY_DELAYS_MS } from '../../src/decide/jev-client.mjs';

// The documented schedule (§3.5: "0.5 s → 2 s → 8 s"), as a LITERAL — every backoff assertion
// below compares against this, never against the module's own `RETRY_DELAYS_MS` export (fix
// round 1 finding: comparing an export to itself is circular — a constant changed to the wrong
// values would still pass). This one assertion is what actually pins the export to the spec.
const DOCUMENTED_BACKOFF_MS = [500, 2000, 8000];
test('RETRY_DELAYS_MS is exactly the documented 0.5s/2s/8s schedule', () => {
  assert.deepEqual([...RETRY_DELAYS_MS], DOCUMENTED_BACKOFF_MS);
});

const FAKE_KEY = 'FAKE-cf-jev-key-3f9a1c7e5b2d4086';
const STATE = { block: { task: 'x' } };
/** @type {Record<string, import('../../src/decide/jev-client.mjs').JevQuestionSpec>} */
const QUESTIONS = { lane: { type: 'choice', instructions: 'i', criteria: { L0: 'trivial' } } };

/**
 * A minimal fetch `Response` double — only the members `askJev` actually reads (`ok`, `status`,
 * `headers.get`, `json`, `clone`). Typed as `any` at the call site (`fetch: fetchImpl`'s real type
 * is the DOM `fetch`); a test double is deliberately not a full `Response`.
 * @param {number} status @param {any} [body] @param {Headers} [headers]
 * @returns {any}
 */
function jsonResponse(status, body, headers = new Headers()) {
  return { ok: status >= 200 && status < 300, status, headers, json: async () => body, clone() { return this; } };
}

/** @param {(number)[]} statuses - one per call, cycling on the last entry if more calls happen */
function sequencedFetch(statuses, { calls = [] } = {}) {
  let i = 0;
  return {
    calls,
    fetch: async (url, init) => {
      calls.push({ url, init });
      const status = statuses[Math.min(i, statuses.length - 1)];
      i += 1;
      if (status === 200) {
        return jsonResponse(200, { answers: { lane: { type: 'choice', choice: 'L0', probabilities: { L0: 1 } } }, usage: { input_tokens: 10, output_tokens: 5 } });
      }
      return jsonResponse(status, {});
    },
  };
}

/** A no-wait sleep that records every delay it was asked for. */
function recordingSleep() {
  const delays = [];
  return { delays, sleep: async (ms) => { delays.push(ms); } };
}

// ── success ──────────────────────────────────────────────────────────────────────────────────────

test('askJev: success — POSTs the documented shape and returns the parsed answers + usage', async () => {
  const { calls, fetch } = sequencedFetch([200]);
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch });
  assert.equal(result.ok, true);
  assert.equal(result.attempts, 1);
  const lane = /** @type {import('../../src/decide/jev-client.mjs').ChoiceAnswer} */ (result.answers.lane);
  assert.deepEqual(lane.probabilities, { L0: 1 });
  assert.equal(result.usage.input_tokens, 10);

  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, JEV_URL);
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.headers.Authorization, `Bearer ${FAKE_KEY}`);
  const sentBody = JSON.parse(calls[0].init.body);
  assert.deepEqual(sentBody.state, STATE);
  assert.equal(sentBody.model, JEV_MODEL);
  assert.deepEqual(sentBody.questions, QUESTIONS);
});

test('askJev: never puts the key anywhere but the Authorization header', async () => {
  const { calls, fetch } = sequencedFetch([200]);
  await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch });
  assert.equal(calls[0].init.body.includes(FAKE_KEY), false);
  assert.equal(calls[0].init.headers.Authorization, `Bearer ${FAKE_KEY}`);
});

// ── the 4 documented error paths (401/422/429/529) ──────────────────────────────────────────────

test('askJev: 401 stops immediately — no retry, kind: unauthorized', async () => {
  const { calls, fetch } = sequencedFetch([401]);
  const { sleep } = recordingSleep();
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, sleep });
  assert.equal(result.ok, false);
  assert.equal(result.kind, 'unauthorized');
  assert.equal(result.status, 401);
  assert.equal(result.attempts, 1);
  assert.equal(calls.length, 1); // no retry
});

test('askJev: 422 stops immediately with a request id — no retry, kind: invalid_request', async () => {
  const headers = new Headers({ 'x-request-id': 'req-abc123' });
  const fetch = async () => jsonResponse(422, { message: 'bad shape' }, headers);
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch });
  assert.equal(result.ok, false);
  assert.equal(result.kind, 'invalid_request');
  assert.equal(result.status, 422);
  assert.equal(result.requestId, 'req-abc123');
  assert.equal(result.attempts, 1);
});

test('askJev: 429 retries on the documented 0.5s/2s/8s backoff, then gives up as rate_limited', async () => {
  const { calls, fetch } = sequencedFetch([429, 429, 429, 429]);
  const { delays, sleep } = recordingSleep();
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, sleep });
  assert.equal(result.ok, false);
  assert.equal(result.kind, 'rate_limited');
  assert.equal(result.status, 429);
  assert.equal(calls.length, 4); // 1 initial + 3 retries
  assert.deepEqual(delays, DOCUMENTED_BACKOFF_MS);
});

test('askJev: 529 retries the same way, then gives up as unavailable', async () => {
  const { calls, fetch } = sequencedFetch([529, 529, 529, 529]);
  const { delays, sleep } = recordingSleep();
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, sleep });
  assert.equal(result.ok, false);
  assert.equal(result.kind, 'unavailable');
  assert.equal(calls.length, 4);
  assert.deepEqual(delays, DOCUMENTED_BACKOFF_MS);
});

test('askJev: a 429 that recovers on retry 2 succeeds without exhausting all retries', async () => {
  const { calls, fetch } = sequencedFetch([429, 200]);
  const { sleep } = recordingSleep();
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, sleep });
  assert.equal(result.ok, true);
  assert.equal(result.attempts, 2);
  assert.equal(calls.length, 2);
});

// ── network failures retry the same way ─────────────────────────────────────────────────────────

test('askJev: a rejecting fetch (network failure) retries on the same backoff, then gives up as network', async () => {
  let n = 0;
  const fetch = async () => {
    n += 1;
    throw new Error('ECONNREFUSED');
  };
  const { delays, sleep } = recordingSleep();
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, sleep });
  assert.equal(result.ok, false);
  assert.equal(result.kind, 'network');
  assert.equal(n, 4);
  assert.deepEqual(delays, DOCUMENTED_BACKOFF_MS);
});

// ── per-attempt timeout (fix round 1: "no per-attempt timeout") ────────────────────────────────

/**
 * A fetch double that NEVER resolves on its own — it only settles when the caller's own
 * `AbortSignal.timeout(timeoutMs)` fires, exactly like real `fetch` does. Proves `askJev` passes
 * a REAL, working `signal` (not just a decorative option) — a `timeoutMs` of a few ms keeps the
 * real wall-clock cost of this test tiny while still exercising the genuine timer.
 *
 * `AbortSignal.timeout()`'s own internal timer is UNREF'D (verified empirically on this Mac,
 * Node 22.23.2 — it is designed to piggyback on a real in-flight socket that already keeps the
 * event loop alive; nothing else here does that). A bare `new Promise` executor with only an
 * `addEventListener('abort', …)` never gets a chance to fire and hangs forever, so this mock adds
 * its own ordinary (REF'D) `setTimeout` purely as a keep-alive, cleared the instant the real abort
 * fires.
 * @param {{url: string, init: object}[]} calls
 */
function neverRespondingFetch(calls) {
  return (url, init) =>
    new Promise((_resolve, reject) => {
      calls.push({ url, init });
      const keepAlive = setTimeout(() => {}, 60_000);
      init.signal?.addEventListener('abort', () => {
        clearTimeout(keepAlive);
        reject(init.signal.reason);
      });
    });
}

test('askJev: a per-attempt timeout aborts and retries on the same backoff, then gives up as "timeout"', async () => {
  const calls = [];
  const fetch = neverRespondingFetch(calls);
  const { delays, sleep } = recordingSleep();
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, sleep, timeoutMs: 5 });
  assert.equal(result.ok, false);
  assert.equal(result.kind, 'timeout');
  assert.equal(result.attempts, 4);
  assert.equal(calls.length, 4);
  assert.deepEqual(delays, DOCUMENTED_BACKOFF_MS);
  // Every attempt got its OWN real AbortSignal — not four references to the same one.
  const signals = new Set(calls.map((c) => c.init.signal));
  assert.equal(signals.size, 4);
});

test('askJev: rejects a non-finite/non-positive timeoutMs before ever calling fetch', async () => {
  let called = false;
  const fetch = async () => { called = true; return jsonResponse(200, { answers: {} }); };
  await assert.rejects(() => askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, timeoutMs: 0 }), TypeError);
  await assert.rejects(() => askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, timeoutMs: -5 }), TypeError);
  await assert.rejects(() => askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch, timeoutMs: NaN }), TypeError);
  assert.equal(called, false);
});

// ── malformed / unexpected responses ────────────────────────────────────────────────────────────

test('askJev: a 200 with no "answers" key is invalid_response, not a silent success', async () => {
  const fetch = async () => jsonResponse(200, { nope: true });
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch });
  assert.equal(result.ok, false);
  assert.equal(result.kind, 'invalid_response');
});

test('askJev: an undocumented status fails fast with no retry', async () => {
  const { calls, fetch } = sequencedFetch([500]);
  const result = await askJev({ state: STATE, questions: QUESTIONS, key: FAKE_KEY, fetch });
  assert.equal(result.ok, false);
  assert.equal(result.kind, 'unexpected_status');
  assert.equal(result.status, 500);
  assert.equal(calls.length, 1);
});

// ── argument validation ─────────────────────────────────────────────────────────────────────────

test('askJev: rejects a missing/empty key, state or questions before ever calling fetch', async () => {
  let called = false;
  const fetch = async () => {
    called = true;
    return jsonResponse(200, { answers: {} });
  };
  await assert.rejects(() => askJev({ state: STATE, questions: QUESTIONS, key: '', fetch }), TypeError);
  await assert.rejects(() => askJev({ state: null, questions: QUESTIONS, key: FAKE_KEY, fetch }), TypeError);
  await assert.rejects(() => askJev({ state: STATE, questions: {}, key: FAKE_KEY, fetch }), TypeError);
  assert.equal(called, false);
});
