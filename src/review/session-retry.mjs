/**
 * One review session with ONE automatic retry on `timeout` (block B30, issue #2).
 *
 * Field data: a reviewer CLI can hang for the whole `review.session_timeout_s` with almost no
 * output, while the same packet reviews in seconds on a second try. So a session whose status is
 * `timeout` is spawned once more with the SAME options (same packet file, same level, same lens);
 * only when the second attempt times out too does the review report `unavailable: timeout`. Any
 * other failure (exit, schema, unavailable…) is never retried here.
 *
 * Both attempts are recorded: `spawnSession` writes one ledger `session` row per spawn as always,
 * and every timed-out attempt adds a `review.session_timeout` note `{lens, role, level, provider,
 * model, attempt, retried, duration_ms, stderr_tail, tail_note?, stdout_bytes, stdout_events,
 * packet_hash?, second_level?}`: the last 4 KB of the session's stderr, packet-free and redacted
 * (`stderrTail`; null with a `tail_note` when withheld), and stdout only as its byte count and last
 * event types (`stdoutEvents`) — never stdout text. A throwing spawn is returned as `res: null` and
 * a failed note is ignored; the helper throws only when `onBroken` throws (B91: rethrown, never
 * followed by the same-model retry — the caller turns it into a non-approval). `onAttempt(n)` is told each attempt as it
 * starts.
 *
 * B91: a timed-out attempt is a BROKEN result, like a schema miss. The caller passes the packet's
 * hash (`packetHash`, sha256 of the packet text): every note then carries `packet_hash`, so the
 * worker's signed history (`ledgerSchemaHistory`) counts it for that packet, level and role. In the
 * worker every note goes through its ledger writer, which signs it with the run key. A session of
 * the second-level try itself passes `secondLevel: true`: its notes say `second_level: true` and
 * never count. `onBroken(res)` (never used on a second-level try) is asked after each timed-out
 * attempt's note is written: null ⇒ the B30 flow goes on unchanged (retry once, then give up); a
 * result ⇒ the packet's second broken result started the B55 second-level try
 * (`schema-fallback.mjs`, `trigger: timeout`) and its outcome is the session's:
 * `{res, attempts: n + its attempts, to, broken}` (`broken`: the timed-out result it replaced, for
 * the token sum). The note's `retried` keeps its B30 meaning (the same-model retry was due); the
 * `review.schema_fallback` row written after it says the second level ran instead.
 */

import { logWarning } from '../util/error-log.mjs';

/** How many times a timed-out session is spawned again. */
export const TIMEOUT_RETRIES = 1;

/**
 * @typedef {{res: Record<string, any> | null, attempts: number, to: {level: string, provider: string, model: string}}} BrokenTry -
 *   the outcome of a second-level try started by a timed-out attempt (`afterSchemaMiss`).
 * @typedef {{packetHash?: string, secondLevel?: boolean, onBroken?: (res: Record<string, any>) => Promise<BrokenTry | null>}} BrokenOpts -
 *   B91: the packet hash every note carries, whether this is the second-level try, and the
 *   broken-result hook (see the module doc).
 */

/**
 * @param {(opts: Record<string, any>) => Promise<Record<string, any>>} spawn
 * @param {Record<string, any>} opts - the spawn options; passed unchanged to every attempt.
 * @param {(row: Record<string, any>) => Promise<unknown>} note - best-effort ledger note.
 * @param {(attempt: number) => void} [onAttempt] - told the attempt number as each one starts.
 * @param {BrokenOpts} [broken] - B91 (absent ⇒ the B30 behaviour, notes without `packet_hash`).
 * @returns {Promise<{res: Record<string, any> | null, attempts: number, to?: BrokenTry['to'], broken?: Record<string, any>}>}
 *   the last attempt's result (`null` when the spawn threw) and how many attempts ran; with `to`
 *   (and `broken`) when a timed-out attempt started the second-level try.
 */
export async function spawnWithTimeoutRetry(spawn, opts, note, onAttempt = () => {}, broken = {}) {
  let attempts = 0;
  for (;;) {
    attempts += 1;
    onAttempt(attempts);
    /** @type {Record<string, any> | null} */
    let res;
    try {
      res = await spawn(opts);
    } catch {
      return { res: null, attempts }; // a throw is an `exit` failure, never retried
    }
    if (res?.status !== 'timeout') return { res, attempts };
    const retried = attempts <= TIMEOUT_RETRIES;
    try {
      await note({
        event: 'review.session_timeout',
        lens: opts?.rowExtra?.lens ?? null,
        role: opts?.role ?? null,
        level: opts?.level ?? null,
        provider: res.provider ?? null,
        model: res.model ?? null,
        attempt: attempts,
        retried,
        duration_ms: typeof res.duration_ms === 'number' ? res.duration_ms : null,
        stderr_tail: typeof res.stderr_tail === 'string' ? res.stderr_tail : null,
        ...(typeof res.tail_note === 'string' ? { tail_note: res.tail_note } : {}),
        stdout_bytes: typeof res.stdout_bytes === 'number' ? res.stdout_bytes : null,
        stdout_events: Array.isArray(res.stdout_events) ? res.stdout_events : [],
        ...(typeof broken.packetHash === 'string' ? { packet_hash: broken.packetHash } : {}),
        ...(broken.secondLevel === true ? { second_level: true } : {}),
      });
    } catch {
      // a failed note never stops the retry or the result (the count then misses it: fewer tries)
    }
    if (broken.onBroken && broken.secondLevel !== true) {
      // Contract: `onBroken` is called AFTER this attempt's note was written (or its write failed),
      // so the hook's count already holds the attempt in hand and must not add it a second time.
      // A hook that throws is rethrown, never followed by the B30 same-model retry: a second-level
      // try may already have started, and the caller turns the throw into a non-approval.
      const taken = await broken.onBroken(res);
      if (taken) return { res: taken.res, attempts: attempts + taken.attempts, to: taken.to, broken: res };
    }
    if (!retried) return { res, attempts };
    // B37: the retry is a recoverable problem: one warning line in the local error log
    await logWarning({ warning: 'review_retry', message: 'a review session timed out; it was retried once' }).catch(() => null);
  }
}
