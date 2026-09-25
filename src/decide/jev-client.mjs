/**
 * The Jev (System 1) HTTP transport (plan §3.1, §3.2, O22). This module owns exactly one job:
 * turn `{state, questions}` into one `POST` to Jev and a typed result — nothing here decides
 * WHETHER to call Jev, picks a fallback, or writes a ledger row (that is `fallback-rules.mjs`,
 * the CLI verb, and later blocks).
 *
 * **Verified request/response shape (research/jev/jev_test.py + jev_results.json, this Mac,
 * 2026-09-24; plan header `[A10]`):** `POST https://api.typesafe.ai/v1/systemone`, header
 * `Authorization: Bearer <key>`, body `{state, model, questions}` where each `questions.<id>` is
 * `{type: "noul"|"choice"|"score", instructions, criteria}`. A successful response body is
 * `{answers: {<id>: <typed answer>}, usage?: {input_tokens, output_tokens}}` — `choice` answers
 * come back `{type, choice, probabilities, confidence}`, `score` `{type, score, legend,
 * probabilities, confidence}`, `noul` `{type, noul}` (a PROBABILITY that the statement is true,
 * NOT a confidence — `thresholds.mjs` computes confidence/margin itself, per O8; this module never
 * reads those API-supplied `confidence` fields).
 *
 * **Security (rule 6, O22):** the key is read from `opts.key` (a plaintext the caller already
 * resolved via B2's chain) and used ONLY in the `Authorization` header — never in the request
 * body, never interpolated into an error message or a log line. This module never calls
 * `registerSecret` itself (the resolver already did, per `src/keys/store.mjs`); it just never
 * prints the value. Every error path here carries only a `kind`/`status`/`requestId` — HTTP
 * status codes and header names, not response bodies that might echo request content back.
 *
 * **Retries (§3.5):** 429 (rate limited), 529 (overloaded), a network failure (fetch rejects) and
 * a per-attempt TIMEOUT (fix round 1 — see below) back off `0.5 s → 2 s → 8 s` between attempts
 * (3 retries after the first try = 4 attempts total), then give up and report the failure — the
 * CALLER decides what "give up" means (fallback rules, one banner). 401 (bad/expired key) and 422
 * (malformed request) never retry: a retry cannot fix either.
 *
 * **Per-attempt timeout (fix round 1, review finding "no per-attempt timeout"):** every attempt
 * carries `signal: AbortSignal.timeout(timeoutMs)` (default `DEFAULT_JEV_TIMEOUT_MS`, 30 s — a
 * generous headroom over Jev's documented ~0.5 s response time, not a measured server-side bound;
 * `[A10]` names no server timeout to match). Without this, the only limit was undici's own
 * default `headersTimeout`/`bodyTimeout` (~300 s), so 4 attempts could block for ~20 minutes
 * before §3.5's fallback ever got control — far too long for an interactive `jev ask`. An abort
 * is reported as `kind: 'timeout'` and retries on the SAME backoff as a network failure.
 */

/** The one Jev endpoint (verified 2026-09-24). */
export const JEV_URL = 'https://api.typesafe.ai/v1/systemone';

/** The model id Jev's own API expects — not one of `levels.Lx`'s model ids (verified). */
export const JEV_MODEL = 'jev-latest';

/** Backoff delays between retryable attempts, in milliseconds (§3.5: "0.5 s → 2 s → 8 s"). */
export const RETRY_DELAYS_MS = Object.freeze([500, 2000, 8000]);

/** Default per-attempt timeout (fix round 1) — see the module header for why 30 s. */
export const DEFAULT_JEV_TIMEOUT_MS = 30000;

/** HTTP statuses that never retry — a wait cannot fix a bad key or a malformed request. */
const TERMINAL_STATUS_KINDS = Object.freeze({ 401: 'unauthorized', 422: 'invalid_request' });

/** HTTP statuses that DO retry, per the backoff schedule above. */
const RETRYABLE_STATUS_KINDS = Object.freeze({ 429: 'rate_limited', 529: 'unavailable' });

/**
 * @param {number} ms
 * @returns {Promise<void>}
 */
function defaultSleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * @param {Response} response
 * @returns {Promise<string|null>} a request id from the JSON body's `request_id`/`requestId`, else
 *   from an `x-request-id` header, else `null` — never anything else from the body (rule: no
 *   parser excerpts in a message).
 */
async function requestIdOf(response) {
  const header = response.headers?.get?.('x-request-id');
  if (typeof header === 'string' && header.length > 0) {
    return header;
  }
  try {
    const body = await response.clone().json();
    const id = body?.request_id ?? body?.requestId;
    return typeof id === 'string' && id.length > 0 ? id : null;
  } catch {
    return null;
  }
}

/**
 * @typedef {{type: 'choice', choice: string, probabilities: Record<string, number>}} ChoiceAnswer
 * @typedef {{type: 'score', score: number, legend: Record<string, string>, probabilities: Record<string, number>}} ScoreAnswer
 * @typedef {{type: 'noul', noul: number}} NoulAnswer
 * @typedef {ChoiceAnswer|ScoreAnswer|NoulAnswer} JevAnswer
 *
 * @typedef {object} JevQuestionSpec
 * @property {'choice'|'score'|'noul'} type
 * @property {string} instructions
 * @property {Record<string, string>} criteria
 *
 * @typedef {object} JevSuccess
 * @property {true} ok
 * @property {Record<string, JevAnswer>} answers
 * @property {{input_tokens?: number, output_tokens?: number}|undefined} usage
 * @property {number} attempts
 * @property {number} ms
 *
 * @typedef {object} JevFailure
 * @property {false} ok
 * @property {'unauthorized'|'invalid_request'|'rate_limited'|'unavailable'|'network'|'timeout'|'unexpected_status'|'invalid_response'} kind
 * @property {number|null} status
 * @property {string|null} requestId
 * @property {number} attempts
 */

/**
 * One typed call to Jev, with the documented retry/backoff for 429/529/network. Never throws for
 * a normal failure (bad key, rate limit, …) — always resolves to `{ok, ...}`; only a caller
 * programming error (bad `argv` shape) throws synchronously, before any network call.
 *
 * @param {object} opts
 * @param {Record<string, any>} opts.state
 * @param {Record<string, JevQuestionSpec>} opts.questions
 * @param {string} opts.key - a plaintext already resolved by the caller (B2's chain). Used ONLY
 *   in the `Authorization` header.
 * @param {typeof fetch} [opts.fetch] - defaults to Node's global `fetch`; tests always inject one.
 * @param {string} [opts.url]
 * @param {string} [opts.model]
 * @param {(ms: number) => Promise<void>} [opts.sleep] - injectable so a test never really waits.
 * @param {() => number} [opts.now]
 * @param {number} [opts.timeoutMs] - per-attempt timeout; default {@link DEFAULT_JEV_TIMEOUT_MS}.
 * @returns {Promise<JevSuccess|JevFailure>}
 */
export async function askJev({
  state,
  questions,
  key,
  fetch: fetchImpl = globalThis.fetch,
  url = JEV_URL,
  model = JEV_MODEL,
  sleep = defaultSleep,
  now = Date.now,
  timeoutMs = DEFAULT_JEV_TIMEOUT_MS,
}) {
  if (state === null || typeof state !== 'object' || Array.isArray(state)) {
    throw new TypeError('askJev: state must be a plain object');
  }
  if (questions === null || typeof questions !== 'object' || Object.keys(questions).length === 0) {
    throw new TypeError('askJev: questions must be a non-empty object');
  }
  if (typeof key !== 'string' || key.length === 0) {
    throw new TypeError('askJev: key must be a non-empty string');
  }
  if (typeof fetchImpl !== 'function') {
    throw new TypeError('askJev: no fetch implementation available (Node < 18, or none injected)');
  }
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    throw new TypeError('askJev: timeoutMs must be a finite number > 0');
  }

  const body = JSON.stringify({ state, model, questions });
  const started = now();
  const maxAttempts = RETRY_DELAYS_MS.length + 1;

  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    /** @type {Response} */
    let response;
    try {
      response = await fetchImpl(url, {
        method: 'POST',
        headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
        body,
        signal: AbortSignal.timeout(timeoutMs),
      });
    } catch (err) {
      // Network failure (DNS, refused connection, …) or OUR OWN per-attempt timeout firing — the
      // exact error text may carry ambient details we never want to log; only a `kind` survives
      // past this catch. `AbortSignal.timeout()` aborts with a `TimeoutError` DOMException; any
      // other rejection (connection refused, DNS failure, …) is the generic "network" kind.
      const kind = err?.name === 'TimeoutError' ? 'timeout' : 'network';
      if (attempt < maxAttempts) {
        await sleep(RETRY_DELAYS_MS[attempt - 1]);
        continue;
      }
      return { ok: false, kind, status: null, requestId: null, attempts: attempt };
    }

    if (response.ok) {
      let parsed;
      try {
        parsed = await response.json();
      } catch {
        return { ok: false, kind: 'invalid_response', status: response.status, requestId: null, attempts: attempt };
      }
      if (parsed === null || typeof parsed !== 'object' || parsed.answers === null || typeof parsed.answers !== 'object') {
        return { ok: false, kind: 'invalid_response', status: response.status, requestId: null, attempts: attempt };
      }
      return { ok: true, answers: parsed.answers, usage: parsed.usage, attempts: attempt, ms: now() - started };
    }

    const status = response.status;
    if (Object.hasOwn(TERMINAL_STATUS_KINDS, status)) {
      const requestId = await requestIdOf(response);
      return { ok: false, kind: TERMINAL_STATUS_KINDS[status], status, requestId, attempts: attempt };
    }
    if (Object.hasOwn(RETRYABLE_STATUS_KINDS, status)) {
      if (attempt < maxAttempts) {
        await sleep(RETRY_DELAYS_MS[attempt - 1]);
        continue;
      }
      return { ok: false, kind: RETRYABLE_STATUS_KINDS[status], status, requestId: null, attempts: attempt };
    }
    // An undocumented status: fail fast rather than guess at retry semantics for it.
    return { ok: false, kind: 'unexpected_status', status, requestId: null, attempts: attempt };
  }

  // Unreachable: the loop always returns by `maxAttempts`. Kept only so a mutant that breaks the
  // loop bound cannot silently fall through to `undefined`.
  /* c8 ignore next */
  return { ok: false, kind: 'network', status: null, requestId: null, attempts: maxAttempts };
}
