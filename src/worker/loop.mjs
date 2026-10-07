/**
 * The review worker (plan §4.8, §8.1, §8.2, §8.6; block B11).
 *
 * One long-lived process per run, started by the orchestrator (`forge run start`) OUTSIDE any
 * coder sandbox. At start-up it:
 *  - reads the run record (`~/.code-forge/runs/<run>.json`) and the project config;
 *  - creates the run's temp root `<tmp.root>/<run-id>/` with ITSELF as the owner and makes it the
 *    process's run root, so every session it spawns is registered in `<run-root>/pids/` and a
 *    killed worker leaves one root that the next `run start` sweep reaps (§9.6, V12);
 *  - loads the run's HMAC key and resolves the Jev key ONCE through B2's chain (§8.1) — both stay
 *    in this process: the sessions it spawns get an env with no `CODE_FORGE_*`/`JEV_*` variable and
 *    no variable whose value is one of those secrets (§8.2);
 *  - announces itself in `<repo>/.code-forge/queue/worker.json` (what `review-file` checks).
 * Then it polls the queue, hands each ticket to the engine hook (`./engine.mjs`, B12a), signs the
 * outcome with the run key and writes it with its done marker and a signed ledger row.
 *
 * Config (B35): the worker reviews each ticket with the run's config snapshot, read from the run
 * record per ticket — the ticket's pin when `run reload` pinned it (it was queued before the
 * reload), else the snapshot in force. So `run reload` reaches the worker without a restart, and a
 * restarted worker boots on the snapshot, not on whatever the file says now. A record from before
 * snapshots (or none) keeps the config the worker booted with.
 *
 * Ticket pool (B42): the worker runs up to `review.parallel_tickets` (default 3, 1–16) tickets at
 * once, oldest first; when one ends the next pending one starts. Two tickets for the same
 * (block, file) never overlap (a file lock; the later one stays pending without taking a slot),
 * and the engine takes the block's budget row and L3 rung under a block lock (`engine.mjs`). A
 * throw in one ticket is that ticket's `unavailable` result and never stops the others. `stop()`
 * starts no new ticket and waits for the running ones. With 1 the worker runs exactly as before.
 *
 * Restart: tickets live on disk and are idempotent by content hash, so a new worker simply re-reads
 * every ticket without a done marker. A worker that is stopping waits for its in-flight tickets: one
 * whose session the stop killed (an `unavailable` outcome) is abandoned (no done marker) instead of
 * recorded as a failure; one that reached a real outcome is recorded. The new worker's pid is
 * re-pinned in the run record by the orchestrator (`repinWorker`, = `run start --reattach`).
 */

import { spawn as nodeSpawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { expiryChecks } from '../autopilot/limits.mjs';
import { loadProjectConfig } from '../config/load.mjs';
import { createDefaultKeyStore, resolveKey } from '../keys/store.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { spawnSession } from '../session/spawn.mjs';
import { snapshotFor } from '../state/config-snapshot.mjs';
import { readRun, reattachWorker } from '../state/run.mjs';
import { loadKey, signRow } from '../state/signer.mjs';
import { logWarning } from '../util/error-log.mjs';
import { keyedLock } from '../util/locks.mjs';
import { writeSafe } from '../util/redact.mjs';
import { runRoot, setRunRoot } from '../util/tmp.mjs';
import { openFindings } from '../review/gate-check.mjs';
import { reviewTicket } from './engine.mjs';
import { announceWorker, beatWorker, HEARTBEAT_MS, liveWorker, pendingTickets, readTicket, retractWorker, writeResult } from './queue.mjs';
import { contentHash, DELETED_HASH, repoRootOf, WorkerError } from './ticket.mjs';

/** Name of the key the worker resolves for System 1 (`keys.jev` / `system1.key` reference). */
export const JEV_KEY_NAME = 'jev';

export const DEFAULT_POLL_MS = 250;
export const DEFAULT_SESSION_TIMEOUT_S = 300;

/** B42: `review.parallel_tickets` when the config does not set a valid one. */
export const DEFAULT_PARALLEL_TICKETS = 3;

/** B48: the fixed text of a failed autopilot expiry check in a drain (stderr and the warning log). */
export const EXPIRY_CHECK_FAILED = 'the autopilot expiry check failed in a worker drain; it runs again at the next drain';

/**
 * B48: what kind of error an expiry check threw — its code or class only, NEVER its message (a
 * message can carry a path or a value): `EACCES`, `StateError:record-unknown`, `TypeError`.
 * Anything that does not look like a plain identifier is `Error`.
 * @param {unknown} thrown @returns {string}
 */
export function errorKind(thrown) {
  if (!(thrown instanceof Error)) return 'Error';
  const err = /** @type {any} */ (thrown);
  const name = typeof err.name === 'string' ? err.name : 'Error';
  const code = typeof err?.code === 'string' ? err.code : null;
  const kind = code === null ? name : /^E[A-Z]+$/.test(code) ? code : `${name}:${code}`;
  return /^[A-Za-z0-9_:.-]{1,64}$/.test(kind) ? kind : 'Error';
}
export const MAX_PARALLEL_TICKETS = 16;

/**
 * How many tickets the worker runs at once: `review.parallel_tickets` when it is an integer
 * 1–16, else `DEFAULT_PARALLEL_TICKETS`.
 * @param {Record<string, any> | null | undefined} cfg @returns {number}
 */
export function parallelTickets(cfg) {
  const n = cfg?.review?.parallel_tickets;
  return Number.isInteger(n) && n >= 1 && n <= MAX_PARALLEL_TICKETS ? n : DEFAULT_PARALLEL_TICKETS;
}

/**
 * The key of the file lock a ticket takes (B42): one ticket per (block, file) at a time.
 * @param {string} block @param {string} file @returns {string}
 */
export const fileLockKey = (block, file) => `${block}\0${file}`;

/** B35: how long `configFor` waits before its one retry of a failed run-record read. */
export const CONFIG_RETRY_MS = 200;

/** The shortest heartbeat interval a worker accepts; anything else falls back to `HEARTBEAT_MS`. */
export const MIN_HEARTBEAT_MS = 100;

/**
 * Provider-CLI auth variables a reviewer CLI may legitimately need (the reviewer runs on the
 * provider's own account; §8.2 bars the Jev and signer keys, not the provider login).
 */
export const PROVIDER_AUTH_VARS = Object.freeze(['ANTHROPIC_API_KEY', 'OPENAI_API_KEY', 'CODEX_API_KEY', 'XAI_API_KEY', 'GROK_API_KEY']);

/** Names that look like a secret: `*_TOKEN`, `*_KEY`, `*_SECRET`, `*_PASSWORD` (any case). */
export const SECRET_NAME = /(_TOKEN|_KEY|_SECRET|_PASSWORD)$/i;

/**
 * The env handed to every session the worker spawns: the worker's env minus code-forge's own and
 * Jev variables, minus every secret-shaped name except the provider-CLI auth variables, and minus
 * any variable whose value contains one of `secrets` (§8.2).
 * @param {NodeJS.ProcessEnv} env @param {ReadonlyArray<string | null | undefined>} secrets
 * @returns {NodeJS.ProcessEnv}
 */
export function sessionEnv(env, secrets) {
  const live = secrets.filter((s) => typeof s === 'string' && s.length >= 8);
  /** @type {NodeJS.ProcessEnv} */
  const out = {};
  for (const [name, value] of Object.entries(env)) {
    if (/^(CODE_FORGE_|JEV_)/i.test(name)) continue;
    if (SECRET_NAME.test(name) && !PROVIDER_AUTH_VARS.includes(name)) continue;
    if (typeof value === 'string' && live.some((s) => value.includes(/** @type {string} */ (s)))) continue;
    out[name] = value;
  }
  return out;
}

/**
 * Resolve the Jev key through B2's chain (env → store → 1Password). A missing key is not an error:
 * System 1 then falls back (`system1.fallback`, B3). The value is registered for redaction by B2.
 * @param {Record<string, any>} cfg
 * @param {{store: import('../keys/store.mjs').KeyStore, env: NodeJS.ProcessEnv, opRead?: any}} deps
 * @returns {Promise<string | null>}
 */
export async function resolveJevKey(cfg, { store, env, opRead }) {
  const ref = cfg?.system1?.key ?? cfg?.keys?.[JEV_KEY_NAME];
  const res = await resolveKey(JEV_KEY_NAME, { store, env, ref: typeof ref === 'string' ? ref : undefined, ...(opRead ? { opRead } : {}) });
  return res.value;
}

/** The fix-loop fields of an engine outcome the result carries (B12c, §4.11). */
const LOOP_FIELDS = Object.freeze(['round', 'kind', 'level', 'next', 'trigger', 'stopped', 'late']);

/**
 * @param {Record<string, any>} outcome @returns {Record<string, any>} the fix-loop fields present.
 */
export function loopResult(outcome) {
  /** @type {Record<string, any>} */
  const out = {};
  for (const name of LOOP_FIELDS) if (outcome[name] !== undefined) out[name] = outcome[name];
  return out;
}

/**
 * B52: the verdict a result is recorded with (its signed file and its ledger row), always with
 * `approved` and `open`: `approved: true, open: []` only when the outcome said so AND it lists no
 * critical finding. Otherwise `approved: false` and `open` = ALL its findings (`openFindings`:
 * criticals, warnings and nits; a missing or malformed id as `X<n>` by position; the worst
 * severity per id; `[]` when it names none, which the gate refuses until a fresh approval) — so
 * an "approved" result that names a critical is downgraded with every one of its findings open.
 * @param {Record<string, any>} result @returns {Record<string, any>}
 */
export function recordedVerdict(result) {
  const listed = openFindings(result.findings);
  if (result.approved === true && !listed.some((f) => f.severity === 'critical')) return { ...result, approved: true, open: [] };
  return { ...result, approved: false, open: listed };
}

/**
 * @typedef {object} WorkerOpts
 * @property {string} runId
 * @property {string} repoRoot - realpath'd repository root.
 * @property {Record<string, any>} cfg
 * @property {string} runRootDir
 * @property {string} slug - ledger project slug (the run record's `project`).
 * @property {Buffer} key - the run's HMAC key.
 * @property {number} [pollMs]
 * @property {number} [heartbeatMs] - how often `heartbeat_at` is refreshed: an integer ≥ `MIN_HEARTBEAT_MS`,
 *   else `HEARTBEAT_MS`.
 */

/**
 * @typedef {object} WorkerDeps
 * @property {typeof reviewTicket} [review] - the engine hook (default `./engine.mjs`).
 * @property {typeof spawnSession} [spawn]
 * @property {typeof readRun} [readRun] - the run-record read behind the per-ticket config (a test seam).
 * @property {typeof readTicket} [readTicket] - the ticket read (a test seam).
 * @property {import('../keys/store.mjs').KeyStore} [store] - default: B2's production store.
 * @property {any} [opRead]
 * @property {NodeJS.ProcessEnv} [env]
 * @property {Partial<Record<'claude'|'codex'|'grok', string>>} [bins]
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeRow] - default: B6 `appendRow`.
 * @property {() => Promise<Array<Record<string, any>>>} [readRows] - the ledger rows the fix-loop
 *   state is anchored in; default: B6 `readAllRows(slug)`.
 * @property {{write: (s: string) => unknown}} [stderr]
 * @property {import('../review/triage.mjs').JevAsk} [jev] - S1 for triage and the recheck (tests
 *   inject a mock); default: `askJev` with the resolved Jev key, none without one.
 * @property {() => Promise<unknown>} [expiryCheck] - B48: the lazy autopilot checks, run once at the
 *   start of every drain, before any ticket; default: `expiryChecks(opts.runId, <now>, {writeRow})`
 *   (grant expiry, expired approvals). A failure is never silent: one stderr line and one
 *   `autopilot_expiry_check_failed` warning per streak of failures; the next drain tries again.
 */

/**
 * @param {WorkerOpts} opts @param {WorkerDeps} [deps]
 */
export async function createWorker(opts, deps = {}) {
  const { runId, repoRoot, cfg, runRootDir, slug, key } = opts;
  const env = deps.env ?? process.env;
  const review = deps.review ?? reviewTicket;
  const spawn = deps.spawn ?? spawnSession;
  const loadRun = deps.readRun ?? readRun;
  const loadTicket = deps.readTicket ?? readTicket;
  const writeRow = deps.writeRow ?? ((/** @type {Record<string, any>} */ row) => appendRow(row, { slug }));
  const readRows = deps.readRows ?? (() => readAllRows(slug));
  // B48: the run's own writer (unsigned base; the checks sign their rows with the run key)
  const expiryCheck = deps.expiryCheck ?? (() => expiryChecks(runId, new Date(), { writeRow }));
  const warnStream = deps.stderr ?? process.stderr;
  let expiryFailing = false; // inside a streak of failed checks: warn once per streak
  const store = deps.store ?? (await createDefaultKeyStore(env));
  const jevKey = await resolveJevKey(cfg, { store, env, opRead: deps.opRead });
  const childEnv = sessionEnv(env, [jevKey, key.toString('hex')]);
  const pollMs = opts.pollMs ?? DEFAULT_POLL_MS;
  const heartbeatMs = Number.isInteger(opts.heartbeatMs) && /** @type {number} */ (opts.heartbeatMs) >= MIN_HEARTBEAT_MS ? /** @type {number} */ (opts.heartbeatMs) : HEARTBEAT_MS;
  let stopping = false;
  /** @type {ReturnType<typeof setInterval> | null} */
  let heartbeat = null;
  /** @type {(() => void) | null} */
  let wake = null;
  /** @type {(() => void) | null} wakes the pool's wait in `drain` (stop) */
  let poolWake = null;

  /** @param {Record<string, any>} row */
  const ledger = (row) => writeRow(signRow({ run: runId, ...row }, key));

  /**
   * The config ticket `id` is reviewed with (B35): its snapshot from the run record, re-read per
   * ticket; the boot `cfg` when the run has no record or the record has no snapshot.
   * @param {string} id
   * @returns {Promise<Record<string, any>>}
   * @throws {Error} `config-snapshot` — a record that cannot be read, or a missing/altered snapshot.
   */
  async function configFor(id) {
    // The Jev key and the run root were settled at boot from `cfg`; a reload can never change
    // them, because `keys`, `system1.key` and `tmp.root` are fixed keys `run reload` refuses.
    let record;
    for (let attempt = 1; ; attempt += 1) {
      try {
        record = await loadRun(runId);
        break;
      } catch (err) {
        if (/** @type {any} */ (err)?.code === 'no-run') return cfg;
        // a transient read error (e.g. the record caught mid-replace) gets ONE retry
        if (attempt >= 2) throw new Error('config-snapshot');
        await new Promise((resolve) => setTimeout(resolve, CONFIG_RETRY_MS));
      }
    }
    return snapshotFor(record, id)?.config ?? cfg;
  }

  /**
   * One ticket through the engine hook with `tcfg` (the ticket's config snapshot): the unsigned result.
   * @param {import('./queue.mjs').Ticket} ticket @param {Record<string, any>} base
   * @param {Record<string, any>} tcfg
   * @returns {Promise<Record<string, any>>}
   */
  async function reviewWith(ticket, base, tcfg) {
    const timeoutS = Number.isInteger(tcfg?.review?.session_timeout_s) && tcfg.review.session_timeout_s > 0 ? tcfg.review.session_timeout_s : DEFAULT_SESSION_TIMEOUT_S;
    /** @type {import('./engine.mjs').ReviewContext} */
    const ctx = {
      repoRoot,
      runId,
      runRootDir,
      cfg: tcfg,
      jevKey,
      key, // signs the fix-loop state (B12c); the sessions never see it (`childEnv`)
      readRows,
      ...(deps.jev ? { jev: deps.jev } : {}),
      // `sessionOpts.cfg` (a per-call config, e.g. the consensus second reviewer's level) wins
      // over the ticket's; everything else the worker pins.
      spawn: (sessionOpts) =>
        spawn(
          /** @type {any} */ ({ cfg: tcfg, ...sessionOpts, runRoot: runRootDir, run: runId, block: ticket.block, slug, timeoutMs: timeoutS * 1000 }),
          { env: childEnv, ...(deps.bins ? { bins: deps.bins } : {}), ...(deps.stderr ? { stderr: deps.stderr } : {}) },
        ),
      writeRow: (row) => ledger({ ...row, block: ticket.block, file: ticket.file, content_hash: ticket.content_hash }),
    };
    try {
      const outcome = await review(ticket, ctx);
      return {
        ...base,
        status: outcome.status,
        approved: outcome.approved === true,
        engine: outcome.engine,
        ...(typeof outcome.reason === 'string' ? { reason: outcome.reason } : {}),
        ...(Array.isArray(outcome.findings) ? { findings: outcome.findings } : {}),
        ...loopResult(outcome),
        sessions: outcome.sessions,
      };
    } catch {
      return { ...base, status: 'unavailable', reason: 'engine-error', approved: false };
    }
  }

  /**
   * @param {string} id
   * @returns {Promise<Record<string, any> | null>} the signed result, or null when abandoned.
   */
  async function processTicket(id) {
    /** @type {Record<string, any>} */
    let result;
    let ticket = null;
    try {
      ticket = loadTicket(repoRoot, id);
    } catch (err) {
      result = { event: 'review.result', status: 'refused', reason: err instanceof WorkerError ? err.code : 'bad-ticket', approved: false };
    }
    if (ticket) {
      // B42: never two tickets for the same (block, file) at once — its fix-loop state (`seq`) is
      // read, advanced and saved by one ticket at a time; the later one waits here. The key
      // carries the run so two workers in one process (tests) never share a lock.
      const t = ticket;
      result = await keyedLock(`${runId}\0${fileLockKey(t.block, t.file)}`, () => reviewOne(id, t));
    }
    // Stopping: a ticket whose session our own stop killed ends `unavailable` — leave it queued
    // (no done marker) for the next worker instead of recording the kill as a failure. A ticket
    // that reached a real outcome while the pool drained is recorded as usual.
    if (stopping && result.status === 'unavailable') return null;
    return complete(id, result);
  }

  /**
   * The unsigned result for a ticket that was read: refused / stale checks, then the review.
   * @param {string} id @param {import('./queue.mjs').Ticket} ticket
   * @returns {Promise<Record<string, any>>}
   */
  async function reviewOne(id, ticket) {
    const base = { event: 'review.result', block: ticket.block, file: ticket.file, content_hash: ticket.content_hash };
    /** @type {string | null} */
    let current;
    try {
      current = ticket.run === runId ? contentHash(repoRoot, ticket.file) : null;
    } catch (err) {
      // the path no longer passes the on-disk check, or the file cannot be read
      return { ...base, status: 'refused', reason: err instanceof WorkerError ? err.code : 'unreadable', approved: false };
    }
    if (ticket.run !== runId) return { ...base, status: 'refused', reason: 'other-run', approved: false };
    if (current === DELETED_HASH && ticket.content_hash !== DELETED_HASH) return { ...base, status: 'stale', reason: 'missing-file', approved: false };
    if (current !== ticket.content_hash) return { ...base, status: 'stale', approved: false };
    let ticketCfg;
    try {
      ticketCfg = await configFor(id);
    } catch {
      return { ...base, status: 'unavailable', reason: 'config-snapshot', approved: false };
    }
    return reviewWith(ticket, base, ticketCfg);
  }

  /**
   * Sign and write the result + done marker, then the signed ledger row. A ledger failure does not
   * undo the result: the done marker is what the queue reads.
   * @param {string} id @param {Record<string, any>} outcome - recorded through {@link recordedVerdict}.
   */
  async function complete(id, outcome) {
    const result = recordedVerdict(outcome);
    const signed = writeResult({ repoRoot, runId, ticket: id, result, key });
    try {
      await ledger({
        event: 'review.result',
        ticket: id,
        block: result.block ?? null,
        file: result.file ?? null,
        content_hash: result.content_hash ?? null,
        status: result.status,
        // B52: the gate reads the verdict from the ledger, never from the result file
        approved: result.approved,
        open: result.open,
        // the gate tells `refused: other-run` (never reviewed) from other refusals by it
        ...(typeof result.reason === 'string' ? { reason: result.reason } : {}),
        ...(typeof result.trigger === 'string' ? { trigger: result.trigger } : {}),
        ...(typeof result.stopped === 'string' ? { stopped: result.stopped } : {}),
      });
    } catch {
      // the signed result file stands; the gate reads it (§4.6)
    }
    return signed;
  }

  /** Tickets this worker could not even record a failure for: skipped until a restart. */
  const poisoned = new Set();

  /**
   * One ticket in a pool slot: never rejects. An unexpected throw becomes a signed `unavailable`
   * result (reason `worker-error`) when that can still be written; a ticket whose failure cannot
   * be written either is poisoned (skipped for this worker's life).
   * @param {string} id @returns {Promise<number>} 1 when completed, else 0.
   */
  async function runOne(id) {
    try {
      return (await processTicket(id)) !== null ? 1 : 0;
    } catch {
      try {
        await complete(id, { event: 'review.result', status: 'unavailable', reason: 'worker-error', approved: false });
        return 1;
      } catch {
        poisoned.add(id);
        return 0;
      }
    }
  }

  /**
   * B42: how many tickets run at once — `review.parallel_tickets` of the snapshot in force (so a
   * `run reload` resizes the pool at the next drain), else of the boot config; anything but an
   * integer 1–16 is the default.
   * @returns {Promise<number>}
   */
  async function poolSize() {
    let current = cfg;
    try {
      current = await configFor('');
    } catch {
      // an unreadable record: the boot config's size (each ticket still fails closed on its own)
    }
    return parallelTickets(current);
  }

  /**
   * The (block, file) key of each ticket read so far, by id: a written ticket never changes (its
   * id is its content's hash), so it is read once for scheduling, however many wake-ups it waits.
   * Dropped once the ticket is done.
   * @type {Map<string, string>}
   */
  const fileKeys = new Map();

  /**
   * The (block, file) a pending ticket names, or null when it cannot be read (it is started
   * anyway and refused by `processTicket`; a null is not cached). Only a scheduling hint:
   * `processTicket` takes the file lock on the ticket it actually read.
   * @param {string} id @returns {string | null}
   */
  function fileKeyOf(id) {
    const known = fileKeys.get(id);
    if (known !== undefined) return known;
    try {
      const t = loadTicket(repoRoot, id);
      const key = fileLockKey(t.block, t.file);
      fileKeys.set(id, key);
      return key;
    } catch {
      return null;
    }
  }

  /**
   * Serve every pending ticket (B42): up to `review.parallel_tickets` at once, oldest first; when
   * one finishes, the queue is re-read and the next pending ticket starts (no batch barrier; the
   * queue is also re-read every `pollMs` while tickets run, so a new ticket fills a free slot).
   * A ticket whose (block, file) is already running is left pending (it does not take a slot)
   * and starts once that one ends. Each ticket starts at most once per drain. On `stop()` no new
   * ticket starts; the running ones are awaited before `drain` returns. The pool size (a run-record
   * read) is read only when there is a pending ticket: an idle drain reads nothing but the queue.
   * @returns {Promise<number>} how many were completed.
   */
  async function drain() {
    // B48: the lazy autopilot checks (grant expiry, expired approvals) before any ticket starts;
    // a failed check never stops the drain (the next drain tries again) and is never silent
    try {
      await expiryCheck();
      expiryFailing = false;
    } catch (thrown) {
      if (!expiryFailing) {
        expiryFailing = true;
        const text = `${EXPIRY_CHECK_FAILED} (${errorKind(thrown)})`;
        writeSafe(warnStream, `worker: WARN ${text}\n`);
        await logWarning({ warning: 'autopilot_expiry_check_failed', message: text }).catch(() => null);
      }
    }
    /** @type {number | null} */
    let limit = null;
    let done = 0;
    /** @type {Map<string, Promise<void>>} */
    const running = new Map();
    /** @type {Set<string>} the (block, file) keys of the running tickets */
    const busy = new Set();
    /** @type {Set<string>} */
    const started = new Set();
    for (;;) {
      if (!stopping) {
        for (const id of pendingTickets(repoRoot)) {
          if (poisoned.has(id) || started.has(id)) continue;
          if (limit === null) limit = await poolSize();
          if (running.size >= limit || stopping) break;
          const fileKey = fileKeyOf(id);
          if (fileKey !== null && busy.has(fileKey)) continue; // waits without a slot
          started.add(id);
          if (fileKey !== null) busy.add(fileKey);
          const slot = runOne(id)
            .then((n) => {
              done += n;
            })
            .finally(() => {
              // the slot is freed on every exit path (`runOne` never rejects)
              running.delete(id);
              fileKeys.delete(id);
              if (fileKey !== null) busy.delete(fileKey);
            });
          running.set(id, slot);
        }
      }
      if (running.size === 0) break;
      await nextEvent([...running.values()]);
    }
    return done;
  }

  /**
   * Resolve when one of `slots` settles, `pollMs` passes, or `stop()` is called.
   * @param {Array<Promise<void>>} slots
   */
  async function nextEvent(slots) {
    /** @type {ReturnType<typeof setTimeout> | undefined} */
    let timer;
    const tick = new Promise((resolve) => {
      timer = setTimeout(resolve, pollMs);
      poolWake = () => resolve(undefined);
    });
    try {
      await Promise.race([...slots, tick]);
    } finally {
      clearTimeout(timer);
      poolWake = null;
    }
  }

  /**
   * Announce this worker and keep its heartbeat beating on a timer of its own (B30), independent
   * of the ticket being processed: a review session can run for minutes, and `review-file --wait`
   * must still see the worker as live the whole time.
   */
  function announce() {
    const self = announceWorker(repoRoot, { pid: process.pid, run: runId });
    stopBeating();
    const timer = setInterval(() => {
      try {
        // another worker announced itself over us: stop beating, never overwrite its file
        if (!beatWorker(repoRoot, self)) stopBeating();
      } catch {
        // a missed beat (an fs error) is retried on the next tick; the poll loop goes on
      }
    }, heartbeatMs);
    timer.unref();
    heartbeat = timer;
  }

  function stopBeating() {
    if (heartbeat !== null) clearInterval(heartbeat);
    heartbeat = null;
  }

  /** Serve the queue until `stop()`. */
  async function run() {
    announce();
    try {
      while (!stopping) {
        try {
          await drain();
        } catch {
          // the queue directory itself failed to read: try again on the next poll
        }
        if (stopping) break;
        await new Promise((resolve) => {
          const timer = setTimeout(resolve, pollMs);
          wake = () => {
            clearTimeout(timer);
            resolve(undefined);
          };
        });
        wake = null;
      }
    } finally {
      release();
    }
  }

  /** Drain the queue once while announced, then release (`worker --once`). */
  async function once() {
    announce();
    try {
      return await drain();
    } finally {
      stop();
      release();
    }
  }

  /** Stop taking tickets; the running ones are awaited (a killed one is abandoned, see `processTicket`). */
  function stop() {
    stopping = true;
    wake?.();
    poolWake?.();
  }

  /** Withdraw this process's announcement (`worker.json`) if it is still ours. */
  function release() {
    stopBeating();
    retractWorker(repoRoot, process.pid);
  }

  return { processTicket, drain, run, once, stop, release, jevKeyResolved: jevKey !== null, sessionEnv: childEnv };
}

/**
 * Start-up for the `worker` verb: run record, repo root, config, run temp root (owned by this
 * process), run key; refuses when another live worker already serves this queue.
 * @param {{runId: string, cwd?: string, pollMs?: number}} opts @param {WorkerDeps} [deps]
 */
export async function bootWorker(opts, deps = {}) {
  const record = await readRun(opts.runId);
  if (record.status !== 'active') throw new WorkerError('run-ended', `run ${opts.runId} has ended`);
  const repoRoot = await repoRootOf(path.resolve(opts.cwd ?? record.workspace));
  // B35: a run with a config snapshot boots on it (the snapshot in force, not on whatever the file
  // says now); a run from before snapshots boots on the file, as before.
  let cfg;
  try {
    cfg = snapshotFor(record, '')?.config;
  } catch {
    throw new WorkerError('config', `run ${opts.runId}: the config snapshot in the run record is missing or altered`);
  }
  if (!cfg) {
    const loaded = await loadProjectConfig(repoRoot);
    if (!loaded.ok) throw new WorkerError('config', loaded.message ?? 'the project config cannot be loaded');
    cfg = loaded.config;
  }
  const other = liveWorker(repoRoot);
  if (other && other.pid !== process.pid) throw new WorkerError('worker-running', `worker pid ${other.pid} already serves this queue`);
  const tmpRoot = typeof cfg?.tmp?.root === 'string' ? cfg.tmp.root : undefined;
  const runRootDir = runRoot(opts.runId, { root: tmpRoot });
  setRunRoot(runRootDir);
  const key = await loadKey(opts.runId);
  const worker = await createWorker({ runId: opts.runId, repoRoot, cfg, runRootDir, slug: record.project, key, pollMs: opts.pollMs }, deps);
  return { ...worker, repoRoot, runRootDir };
}

/**
 * `run start --reattach` for a restarted worker: re-pin the run record to the pid the queue
 * announces, when that worker is alive and serves this run (the orchestrator runs this, never a
 * coder — the record lives outside the workspace).
 * `expectPid` (a `run start` that just launched a worker) pins ONLY that pid: an older or stale
 * announcement naming another live pid is refused (`worker_mismatch`) instead of being pinned
 * over the new worker.
 * @param {{runId: string, repoRoot: string, writeRow?: (row: Record<string, any>) => Promise<unknown>, probe?: import('../state/run.mjs').StartTimeProbe, expectPid?: number}} opts
 */
export async function repinWorker({ runId, repoRoot, writeRow, probe, expectPid }) {
  const live = liveWorker(repoRoot);
  if (!live) throw new WorkerError('worker_down', 'no live worker serves this queue');
  if (live.run !== runId) throw new WorkerError('other-run', `the live worker serves run ${live.run}`);
  if (expectPid !== undefined && live.pid !== expectPid) {
    throw new WorkerError('worker_mismatch', `the queue announces worker pid ${live.pid}, not the launched pid ${expectPid}`);
  }
  const { project } = await readRun(runId);
  const write = writeRow ?? ((/** @type {Record<string, any>} */ row) => appendRow(row, { slug: project }));
  return reattachWorker({ runId, workerPid: live.pid, writeRow: write, ...(probe ? { probe } : {}) });
}

/** The CLI entry the worker is launched from. */
const BIN = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));

/**
 * `run start`: launch `code-forge worker --run <id>` DETACHED in the workspace (the orchestrator's
 * context, outside any coder sandbox; argv only, no shell) and wait until it announces itself in
 * the queue. A worker that does not announce within `timeoutMs` is killed and the start refused.
 * @param {{runId: string, workspace: string, env?: NodeJS.ProcessEnv, timeoutMs?: number}} opts
 * @returns {Promise<{pid: number, repoRoot: string}>}
 */
export async function launchWorker({ runId, workspace, env = process.env, timeoutMs = 15000 }) {
  const repoRoot = await repoRootOf(workspace);
  const child = nodeSpawn(process.execPath, [BIN, 'worker', '--run', runId], { cwd: repoRoot, env, detached: true, stdio: 'ignore', shell: false });
  child.on('error', () => {});
  const pid = child.pid;
  child.unref();
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (typeof pid === 'number' && liveWorker(repoRoot)?.pid === pid) return { pid, repoRoot };
    if (child.exitCode !== null || child.signalCode !== null) break;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (typeof pid === 'number') {
    try {
      process.kill(-pid, 'SIGKILL');
    } catch {
      // already gone
    }
  }
  throw new WorkerError('worker_down', 'the worker did not start (run `code-forge worker --run <id>` by hand to see why)');
}
