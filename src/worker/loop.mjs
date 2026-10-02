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
 * Restart: tickets live on disk and are idempotent by content hash, so a new worker simply re-reads
 * every ticket without a done marker. A worker that is stopping abandons its in-flight ticket (no
 * done marker) instead of recording the killed session as a failure. The new worker's pid is
 * re-pinned in the run record by the orchestrator (`repinWorker`, = `run start --reattach`).
 */

import { spawn as nodeSpawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadProjectConfig } from '../config/load.mjs';
import { createDefaultKeyStore, resolveKey } from '../keys/store.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { spawnSession } from '../session/spawn.mjs';
import { snapshotFor } from '../state/config-snapshot.mjs';
import { readRun, reattachWorker } from '../state/run.mjs';
import { loadKey, signRow } from '../state/signer.mjs';
import { runRoot, setRunRoot } from '../util/tmp.mjs';
import { reviewTicket } from './engine.mjs';
import { announceWorker, beatWorker, HEARTBEAT_MS, liveWorker, pendingTickets, readTicket, retractWorker, writeResult } from './queue.mjs';
import { contentHash, DELETED_HASH, repoRootOf, WorkerError } from './ticket.mjs';

/** Name of the key the worker resolves for System 1 (`keys.jev` / `system1.key` reference). */
export const JEV_KEY_NAME = 'jev';

export const DEFAULT_POLL_MS = 250;
export const DEFAULT_SESSION_TIMEOUT_S = 300;

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
 */

/**
 * @param {WorkerOpts} opts @param {WorkerDeps} [deps]
 */
export async function createWorker(opts, deps = {}) {
  const { runId, repoRoot, cfg, runRootDir, slug, key } = opts;
  const env = deps.env ?? process.env;
  const review = deps.review ?? reviewTicket;
  const spawn = deps.spawn ?? spawnSession;
  const writeRow = deps.writeRow ?? ((/** @type {Record<string, any>} */ row) => appendRow(row, { slug }));
  const readRows = deps.readRows ?? (() => readAllRows(slug));
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
        record = await readRun(runId);
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
      ticket = readTicket(repoRoot, id);
    } catch (err) {
      result = { event: 'review.result', status: 'refused', reason: err instanceof WorkerError ? err.code : 'bad-ticket', approved: false };
    }
    if (ticket) {
      const base = { event: 'review.result', block: ticket.block, file: ticket.file, content_hash: ticket.content_hash };
      /** @type {string | null} */
      let current = null;
      try {
        current = ticket.run === runId ? contentHash(repoRoot, ticket.file) : null;
      } catch (err) {
        result = { ...base, status: 'refused', reason: err instanceof WorkerError ? err.code : 'unreadable', approved: false };
      }
      if (result) {
        // refused above: the path no longer passes the on-disk check, or the file cannot be read
      } else if (ticket.run !== runId) {
        result = { ...base, status: 'refused', reason: 'other-run', approved: false };
      } else if (current === DELETED_HASH && ticket.content_hash !== DELETED_HASH) {
        result = { ...base, status: 'stale', reason: 'missing-file', approved: false };
      } else if (current !== ticket.content_hash) {
        result = { ...base, status: 'stale', approved: false };
      } else {
        /** @type {Record<string, any> | null} */
        let ticketCfg = null;
        try {
          ticketCfg = await configFor(id);
        } catch {
          result = { ...base, status: 'unavailable', reason: 'config-snapshot', approved: false };
        }
        if (ticketCfg) result = await reviewWith(ticket, base, ticketCfg);
      }
    }
    if (stopping) return null; // the session was killed by our own stop: leave the ticket queued
    return complete(id, result);
  }

  /**
   * Sign and write the result + done marker, then the signed ledger row. A ledger failure does not
   * undo the result: the done marker is what the queue reads.
   * @param {string} id @param {Record<string, any>} result
   */
  async function complete(id, result) {
    const signed = writeResult({ repoRoot, runId, ticket: id, result, key });
    try {
      await ledger({
        event: 'review.result',
        ticket: id,
        block: result.block ?? null,
        file: result.file ?? null,
        content_hash: result.content_hash ?? null,
        status: result.status,
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
   * Process every pending ticket once. An unexpected throw on one ticket becomes a signed
   * `unavailable` result (reason `worker-error`) when that can still be written, and never stops
   * the loop; a ticket whose failure cannot be written either is skipped for this worker's life.
   * @returns {Promise<number>} how many were completed.
   */
  async function drain() {
    let done = 0;
    for (const id of pendingTickets(repoRoot)) {
      if (stopping) break;
      if (poisoned.has(id)) continue;
      try {
        if ((await processTicket(id)) !== null) done += 1;
      } catch {
        try {
          await complete(id, { event: 'review.result', status: 'unavailable', reason: 'worker-error', approved: false });
          done += 1;
        } catch {
          poisoned.add(id);
        }
      }
    }
    return done;
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

  function stop() {
    stopping = true;
    wake?.();
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
