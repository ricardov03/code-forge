/**
 * One review session: advance a configured fallback ladder immediately on timeout or capacity;
 * without a ladder, keep ONE automatic retry on `timeout` (block B30, issue #2).
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
import { resolveLevel } from '../config/known-ids.mjs';
import { closedBookRefused, openBookCodexAllowed } from '../config/closed-book.mjs';

/** How many times a timed-out session is spawned again. */
export const TIMEOUT_RETRIES = 1;

/**
 * A broken answer gets one retry on its model, then the next distinct, runnable model of the
 * level's ladder. Pin each retry to one model so provider-error fallback cannot consume the
 * ladder before the same-model retry. A valid negative answer stops just like a valid positive.
 * The packet and the original config are unchanged throughout.
 * @param {(opts: Record<string, any>) => Promise<{res: Record<string, any> | null, attempts: number}>} spawn
 * @param {Record<string, any>} opts
 * @param {(res: Record<string, any> | null) => boolean} isBroken
 * @param {(res: Record<string, any> | null, cfg: Record<string, any>) => Promise<unknown>} [onBroken]
 * @param {{res: Record<string, any> | null, attempts: number}} [initial] - reuse a completed
 *   attempt, including any provider fallback it already took, rather than spawning it again.
 * @returns {Promise<{res: Record<string, any> | null, attempts: number, to?: BrokenTry['to'], usage: {tokens_in: number | null, tokens_out: number | null}}>}
 */
export async function spawnWithBrokenFallback(spawn, opts, isBroken, onBroken = async () => {}, initial) {
  const resolved = resolveLevel(opts.cfg, opts.level);
  const models = [resolved, ...resolved.fallback];
  const startAt = initial ? Math.max(0, models.findIndex((entry) => entry.model === initial.res?.model
    && (entry.provider ?? resolved.provider) === initial.res?.provider)) : 0;
  let pending = initial;
  const seen = new Set();
  let attempts = 0;
  /** @type {Record<string, any> | null} */
  let res = null;
  /** @type {BrokenTry['to'] | undefined} */
  let to;
  /** @type {{tokens_in: number | null, tokens_out: number | null}} */
  const usage = { tokens_in: null, tokens_out: null };
  for (const [index, entry] of models.entries()) {
    if (index < startAt) continue;
    const provider = entry.provider ?? resolved.provider;
    const key = `${provider}\0${entry.model}`;
    if (seen.has(key) || closedBookRefused(provider, opts.role, openBookCodexAllowed(opts.cfg))) continue;
    seen.add(key);
    const cfg = { ...opts.cfg, levels: { ...opts.cfg.levels, [opts.level]: {
      provider, model: entry.model, ...(entry.effort !== undefined ? { effort: entry.effort } : {}), fallback: [],
    } } };
    if (index > 0) to = { level: `levels.${opts.level}.fallback`, provider, model: entry.model };
    for (let n = 0; n < 2; n++) {
      try {
        const out = pending ?? await spawn({ ...opts, cfg });
        pending = undefined;
        res = out.res;
        attempts += out.attempts;
      } catch {
        res = null;
        attempts++;
      }
      for (const field of /** @type {const} */ (['tokens_in', 'tokens_out'])) {
        if (typeof res?.usage?.[field] === 'number') usage[field] = (usage[field] ?? 0) + res.usage[field];
      }
      if (!isBroken(res)) return { res, attempts, ...(to ? { to } : {}), usage };
      await onBroken(res, cfg);
    }
  }
  return { res, attempts, ...(to ? { to } : {}), usage };
}

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
  const hasLadder = opts.cfg?.levels?.[opts.level]?.fallback?.length > 0;
  let currentOpts = opts;
  let attempts = 0;
  for (;;) {
    attempts += 1;
    onAttempt(attempts);
    /** @type {Record<string, any> | null} */
    let res;
    try {
      res = await spawn(currentOpts);
    } catch {
      return { res: null, attempts }; // a throw is an `exit` failure, never retried
    }
    const capacity = res?.status === 'unavailable' && res.reason === 'at-capacity';
    if (res?.status !== 'timeout' && !capacity) return { res, attempts };
    // A route's ladder takes precedence over retrying a stalled or overloaded model. The
    // spawner may already have consumed provider-error steps: resume AFTER its actual model.
    let nextOpts;
    if (hasLadder) {
      const level = resolveLevel(currentOpts.cfg, currentOpts.level);
      const models = [level, ...level.fallback];
      const at = models.findIndex((m) => m.model === res.model && (m.provider ?? level.provider) === res.provider);
      const remaining = models.slice(Math.max(0, at) + 1)
        .filter((m) => !closedBookRefused(m.provider ?? level.provider, opts.role, openBookCodexAllowed(opts.cfg)))
        .filter((m) => m.model !== res.model || (m.provider ?? level.provider) !== res.provider)
        .map((m) => ({ ...m, provider: m.provider ?? level.provider }));
      const next = remaining[0];
      if (next) nextOpts = { ...opts, cfg: { ...opts.cfg, levels: { ...opts.cfg.levels, [opts.level]: {
        provider: next.provider ?? level.provider, model: next.model,
        ...(next.effort !== undefined ? { effort: next.effort } : {}), fallback: remaining.slice(1),
      } } } };
    }
    if (capacity) {
      if (!nextOpts) return { res, attempts };
      currentOpts = nextOpts;
      continue;
    }
    const retried = !hasLadder && attempts <= TIMEOUT_RETRIES;
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
    if (nextOpts) {
      currentOpts = nextOpts;
      continue;
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
