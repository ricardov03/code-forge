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
 * model, attempt, retried, duration_ms, stderr_tail, tail_note?, stdout_bytes, stdout_events}`:
 * the last 4 KB of the session's stderr, packet-free and redacted (`stderrTail`; null with a
 * `tail_note` when withheld), and stdout only as its byte count and last event types
 * (`stdoutEvents`) — never stdout text. The helper never throws: a throwing spawn is returned as
 * `res: null` and a failed note is ignored. `onAttempt(n)` is told each attempt as it starts.
 */

import { logWarning } from '../util/error-log.mjs';

/** How many times a timed-out session is spawned again. */
export const TIMEOUT_RETRIES = 1;

/**
 * @param {(opts: Record<string, any>) => Promise<Record<string, any>>} spawn
 * @param {Record<string, any>} opts - the spawn options; passed unchanged to every attempt.
 * @param {(row: Record<string, any>) => Promise<unknown>} note - best-effort ledger note.
 * @param {(attempt: number) => void} [onAttempt] - told the attempt number as each one starts.
 * @returns {Promise<{res: Record<string, any> | null, attempts: number}>} the last attempt's result
 *   (`null` when the spawn threw) and how many attempts ran.
 */
export async function spawnWithTimeoutRetry(spawn, opts, note, onAttempt = () => {}) {
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
      });
    } catch {
      // a failed note never stops the retry or the result
    }
    if (!retried) return { res, attempts };
    // B37: the retry is a recoverable problem: one warning line in the local error log
    await logWarning({ warning: 'review_retry', message: 'a review session timed out; it was retried once' }).catch(() => null);
  }
}
