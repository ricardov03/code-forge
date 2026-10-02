/**
 * The run record (plan §4.9, §4.8, C4): created by `run start`, pinned OUTSIDE the workspace at
 * `~/.code-forge/runs/<run>.json` (authoritative) and mirrored to `<workspace>/.code-forge/runs/`
 * without the `worker` pin (informational only — nothing reads the mirror back).
 *
 * The worker pin is `{pid, started_at}` where `started_at` is `ps -o lstart= -p <pid>` — the
 * process START time, so a recycled pid or a coder-started replacement worker does not match.
 * The worker itself is B11's: `startRun` takes the pid of a worker the orchestrator started (or
 * none), and `reattachWorker` re-pins after a restart.
 *
 * Ledger rows are written through the `writeRow` parameter (C17 seam: the B6 writer arrives as a
 * parameter; the CLI verb does the static import) and are SIGNED with the run key first.
 */

import { mkdir, readFile, rename, rmdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { exec } from '../util/exec.mjs';
import { StateError, assertRunId, assertWorkspaceOutsideRuns, mirrorPath, newRunId, runRecordPath, runsDir } from './paths.mjs';
import { changedKeyPaths, configHash, IMMUTABLE_KEYS, immutableChanges, initialConfigState } from './config-snapshot.mjs';
import { generateKey, loadKey, signRow } from './signer.mjs';

/** @typedef {(row: Record<string, any>) => Promise<unknown>} WriteRow */
/** @typedef {(pid: number) => Promise<string | null>} StartTimeProbe */

const LOCK_TIMEOUT_MS = 5000;
const LOCK_POLL_MS = 25;

/**
 * The process start time of `pid` (`ps -o lstart=`), or null when no such process exists.
 * @type {StartTimeProbe}
 */
export async function processStartTime(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return null;
  const res = await exec(['ps', '-o', 'lstart=', '-p', String(pid)], {
    env: { ...process.env, LC_ALL: 'C' },
    okExitCodes: [0, 1],
    timeoutMs: 10000,
  });
  const text = res.stdout.trim();
  return res.result === 'ok' && res.code === 0 && text.length > 0 ? text : null;
}

/** @param {unknown} value @returns {string} */
const serialize = (value) => `${JSON.stringify(value, null, 2)}\n`;

/**
 * @param {string} runId
 * @returns {Promise<Record<string, any>>}
 */
export async function readRun(runId) {
  let text;
  try {
    text = await readFile(runRecordPath(runId), 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') throw new StateError('no-run', `no run record for ${runId}`);
    throw err;
  }
  return JSON.parse(text);
}

/**
 * Atomically write the authoritative record (0600) and the workspace mirror (minus `worker`).
 * @param {Record<string, any>} record
 */
export async function saveRun(record) {
  await mkdir(runsDir(), { recursive: true, mode: 0o700 });
  const file = runRecordPath(record.run_id);
  const tmp = `${file}.${process.pid}.tmp`;
  await writeFile(tmp, serialize(record), { mode: 0o600 });
  await rename(tmp, file);
  const { worker, ...mirror } = record;
  const mirrorFile = mirrorPath(record.workspace, record.run_id);
  await mkdir(path.dirname(mirrorFile), { recursive: true });
  await writeFile(`${mirrorFile}.tmp`, serialize(mirror));
  await rename(`${mirrorFile}.tmp`, mirrorFile);
}

/**
 * Serialize read-modify-write of one run record across processes (a `mkdir` lock next to it).
 * @template T
 * @param {string} runId @param {() => Promise<T>} fn
 * @returns {Promise<T>}
 */
export async function withRunLock(runId, fn) {
  await mkdir(runsDir(), { recursive: true, mode: 0o700 });
  const lock = path.join(runsDir(), `${assertRunId(runId)}.lock`);
  const deadline = Date.now() + LOCK_TIMEOUT_MS;
  for (;;) {
    try {
      await mkdir(lock);
      break;
    } catch (err) {
      if (err.code !== 'EEXIST') throw err;
      if (Date.now() > deadline) throw new StateError('locked', `run ${runId} is locked (${lock}); remove it if no forge verb is running`);
      await new Promise((resolve) => setTimeout(resolve, LOCK_POLL_MS));
    }
  }
  try {
    return await fn();
  } finally {
    await rmdir(lock);
  }
}

/**
 * Sign `row` with the run key and hand it to the ledger writer.
 * @param {string} runId @param {WriteRow} writeRow @param {Record<string, any>} row
 */
export async function writeSigned(runId, writeRow, row) {
  const key = await loadKey(runId);
  return writeRow(signRow({ run: runId, ...row }, key));
}


/**
 * A pid this package may ever signal: an integer > 1. `kill(0)` signals the caller's whole
 * process group, `kill(-1)` every process the user owns, pid 1 is init/launchd.
 * @param {unknown} pid
 * @returns {pid is number}
 */
export const isSignalablePid = (pid) => Number.isInteger(pid) && /** @type {number} */ (pid) > 1;

/**
 * @param {number | undefined} workerPid @param {StartTimeProbe} probe
 * @returns {Promise<{pid: number, started_at: string} | null>}
 */
async function pinWorker(workerPid, probe) {
  if (workerPid === undefined) return null;
  if (!isSignalablePid(workerPid)) throw new StateError('bad-worker-pid', 'worker pid must be an integer > 1');
  const startedAt = await probe(workerPid);
  if (startedAt === null) throw new StateError('worker-not-running', `worker pid ${workerPid} is not running`);
  return { pid: workerPid, started_at: startedAt };
}

/**
 * `run start`: record + key + worker pin + ledger `run.start`, all under the run lock. An
 * existing run id is refused before anything is written (its key and record stay untouched).
 * @param {{
 *   workspace: string, project: string, config?: Record<string, any>, engine?: string,
 *   runId?: string, workerPid?: number, orchestratorPid?: number, soloPid?: string | null,
 *   writeRow: WriteRow, probe?: StartTimeProbe, now?: Date,
 * }} opts
 * @returns {Promise<Record<string, any>>} the record
 */
export async function startRun(opts) {
  const { project, config = {}, writeRow, probe = processStartTime, now = new Date() } = opts;
  const runId = assertRunId(opts.runId ?? newRunId(now));
  if (typeof project !== 'string' || !/^[a-z0-9][a-z0-9-]*$/.test(project)) {
    throw new StateError('bad-project', 'project slug must match /^[a-z0-9][a-z0-9-]*$/ (config key project.slug)');
  }
  const workspace = assertWorkspaceOutsideRuns(opts.workspace);
  const worker = await pinWorker(opts.workerPid, probe);
  const record = {
    version: 1,
    run_id: runId,
    project,
    workspace,
    engine: opts.engine ?? config.engine ?? 'auto',
    provider: config.provider ?? null,
    levels: config.levels ?? null,
    fallback_mode: config.system1?.fallback ?? 'rules',
    started_at: now.toISOString(),
    orchestrator_pid: opts.orchestratorPid ?? process.pid,
    solo_pid: opts.soloPid ?? null,
    worker,
    status: 'active',
    blocks: {},
    orphans: [],
    // B35: the config snapshot the worker reviews with (`run reload` replaces it). Only when the
    // caller passed the loaded config: a record without it keeps the worker on its boot config.
    ...(opts.config !== undefined ? { config: initialConfigState(config) } : {}),
  };
  return withRunLock(runId, async () => {
    const exists = await readRun(runId).then(
      () => true,
      (err) => (err.code === 'no-run' ? false : Promise.reject(err)),
    );
    if (exists) throw new StateError('run-exists', `run ${runId} already exists`);
    await generateKey(runId);
    await saveRun(record);
    await writeSigned(runId, writeRow, { event: 'run.start', engine: record.engine, worker_pinned: worker !== null });
    return record;
  });
}

/**
 * `run reload` (B35): replace the run's config snapshot with `config` (already loaded and
 * validated by the caller), entirely under the run lock. Open blocks, their attempts and the
 * worker pin are untouched.
 *
 * `readPending()` (the queue's tickets without a done marker) is called UNDER the lock, right
 * before the record is saved, so the window in which a ticket could be enqueued unpinned is as
 * short as it can be (enqueueing itself does not take the run lock). Each pending ticket keeps a
 * pin whose snapshot is still stored; any other pending ticket — new, or pinned to a snapshot that
 * has gone missing (never left dangling) — is pinned to the snapshot in force until now. Pins of
 * tickets no longer pending are dropped, and so are snapshots no pin or the current hash names.
 *
 * Fixed keys ({@link IMMUTABLE_KEYS}): every one that changed is refused at once
 * (`immutable-key`, nothing written). A record from before snapshots (no `config`) is checked
 * against what it does store: `engine` and the ledger slug (`effectiveSlug`, computed by the
 * caller the same way `run start` did); it then gets its first snapshot, `changed_keys: null`,
 * `old_hash: null`.
 *
 * No change (same hash) ⇒ nothing written, no row. Otherwise the record is saved FIRST (it is the
 * source of truth), then one signed `run.reload` row: the changed key PATHS (never values) and
 * the old/new snapshot hashes. A row that cannot be written leaves the reload in place and is
 * reported as `rowError`.
 * @param {{
 *   runId: string, config: Record<string, any>, readPending?: () => string[], effectiveSlug?: string,
 *   writeRow: WriteRow, now?: Date,
 * }} opts
 * @returns {Promise<{changed: string[] | null, oldHash: string | null, newHash: string, pinned: number, rowError: string | null}>}
 *   `changed` is `[]` when nothing changed.
 */
export async function reloadRun({ runId, config, readPending = () => [], effectiveSlug, writeRow, now = new Date() }) {
  return withRunLock(runId, async () => {
    const current = await readRun(runId);
    if (current.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const state = current.config && typeof current.config === 'object' && typeof current.config.hash === 'string' ? current.config : null;
    const stored = state && state.snapshots && typeof state.snapshots === 'object' ? state.snapshots : {};
    const newHash = configHash(config);
    const oldHash = state ? state.hash : null;
    if (oldHash === newHash) return { changed: [], oldHash, newHash, pinned: 0, rowError: null };
    const before = state && Object.hasOwn(stored, state.hash) ? stored[state.hash] : undefined;
    if (state && (before === null || typeof before !== 'object')) throw new StateError('config-snapshot', `run ${runId}: the current config snapshot is missing from the run record`);
    /** @type {string[]} */
    let frozen;
    if (state) {
      frozen = immutableChanges(before, config);
    } else {
      // no snapshot: compare what the record stores (`run start` wrote engine and the slug)
      frozen = [];
      if (effectiveSlug !== undefined && effectiveSlug !== current.project) frozen.push('project.slug');
      if (typeof current.engine === 'string' && (config.engine ?? 'auto') !== current.engine) frozen.push('engine');
    }
    if (frozen.length > 0) {
      const why = frozen.map((key) => `${key} cannot change mid-run (${IMMUTABLE_KEYS[/** @type {keyof typeof IMMUTABLE_KEYS} */ (key)]})`);
      throw new StateError('immutable-key', `${why.join('; ')} — put ${frozen.length === 1 ? 'it' : 'them'} back, or end the run and start a new one`);
    }
    const changed = state ? changedKeyPaths(before, config) : null;
    /** @type {Record<string, string>} */
    const pins = {};
    const oldPins = state?.pins && typeof state.pins === 'object' ? state.pins : {};
    for (const ticket of new Set(readPending())) {
      const hash = Object.hasOwn(oldPins, ticket) ? oldPins[ticket] : undefined;
      if (typeof hash === 'string' && Object.hasOwn(stored, hash)) pins[ticket] = hash;
      else if (oldHash !== null) pins[ticket] = oldHash;
    }
    /** @type {Record<string, any>} */
    const snapshots = { [newHash]: config };
    for (const hash of new Set(Object.values(pins))) snapshots[hash] = stored[hash];
    current.config = { hash: newHash, snapshots, pins, reloaded_at: now.toISOString() };
    current.provider = config.provider ?? null;
    current.levels = config.levels ?? null;
    current.fallback_mode = config.system1?.fallback ?? 'rules';
    await saveRun(current);
    const pinned = Object.keys(pins).length;
    let rowError = null;
    try {
      await writeSigned(runId, writeRow, { event: 'run.reload', changed_keys: changed, old_hash: oldHash, new_hash: newHash, pinned_tickets: pinned });
    } catch (err) {
      rowError = err?.code ?? err?.name ?? 'error';
    }
    return { changed, oldHash, newHash, pinned, rowError };
  });
}

/**
 * `run start --reattach`: re-pin a restarted worker (the orchestrator only — §4.8).
 * @param {{runId: string, workerPid: number, writeRow: WriteRow, probe?: StartTimeProbe}} opts
 */
export async function reattachWorker({ runId, workerPid, writeRow, probe = processStartTime }) {
  if (!isSignalablePid(workerPid)) throw new StateError('bad-worker-pid', 'worker pid must be an integer > 1');
  return withRunLock(runId, async () => {
    const current = await readRun(runId);
    if (current.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    current.worker = await pinWorker(workerPid, probe);
    await writeSigned(runId, writeRow, { event: 'worker.reattach', pid: workerPid });
    await saveRun(current);
    return current;
  });
}

/**
 * Does the live worker match the pin? `livePid` is the pid the caller sees serving the queue
 * (B11's worker pid file); the pinned pid's start time is re-read in every case.
 * @param {Record<string, any>} record
 * @param {{livePid?: number, probe?: StartTimeProbe}} [opts]
 * @returns {Promise<{ok: boolean, event?: 'worker.replaced' | 'worker.down', reason?: string}>}
 */
export async function checkWorkerPin(record, { livePid, probe = processStartTime } = {}) {
  const pin = record.worker;
  if (!pin) return { ok: false, event: 'worker.down', reason: 'no worker is pinned for this run' };
  if (!isSignalablePid(pin.pid)) return { ok: false, event: 'worker.replaced', reason: 'the pinned worker pid is not an integer > 1' };
  if (livePid !== undefined && livePid !== pin.pid) {
    return { ok: false, event: 'worker.replaced', reason: `live worker pid ${livePid} is not the pinned pid ${pin.pid}` };
  }
  const startedAt = await probe(pin.pid);
  if (startedAt === null) return { ok: false, event: 'worker.down', reason: `pinned worker pid ${pin.pid} is not running` };
  if (startedAt !== pin.started_at) {
    return { ok: false, event: 'worker.replaced', reason: `pid ${pin.pid} was restarted (start time differs from the pin)` };
  }
  return { ok: true };
}

/**
 * The default worker stopper: SIGTERM the pinned pid ONLY when it is an integer > 1 AND its start
 * time, re-read right before the signal, still equals the pin (a recycled pid is never signalled).
 * A worker that already exited (ESRCH) counts as not stopped, never as an error.
 * @param {{pid: number, started_at: string} | null} pin
 * @param {{probe?: StartTimeProbe, kill?: (pid: number, signal: NodeJS.Signals) => unknown}} [opts]
 * @returns {Promise<{stopped: boolean, reason?: string}>}
 */
export async function stopPinnedWorker(pin, { probe = processStartTime, kill = (pid, signal) => process.kill(pid, signal) } = {}) {
  if (!pin || !isSignalablePid(pin.pid)) return { stopped: false, reason: 'invalid pid' };
  const startedAt = await probe(pin.pid);
  if (startedAt === null) return { stopped: false, reason: 'not running' };
  if (startedAt !== pin.started_at) return { stopped: false, reason: 'pid reused' };
  try {
    kill(pin.pid, 'SIGTERM');
  } catch (err) {
    if (err.code === 'ESRCH') return { stopped: false, reason: 'already exited' };
    throw err;
  }
  return { stopped: true };
}

/**
 * `run end`, entirely under the run lock: refuse an ended run or open blocks; check the pin (a
 * failed check is written as its own signed `worker.replaced`/`worker.down` row); stop the
 * worker only when the pin verifies; mark ended and write `run.end`.
 * @param {{
 *   runId: string, writeRow: WriteRow, probe?: StartTimeProbe,
 *   stopWorker?: (pin: {pid: number, started_at: string}, opts: {probe?: StartTimeProbe}) => Promise<{stopped: boolean}>,
 * }} opts
 */
export async function endRun({ runId, writeRow, stopWorker = stopPinnedWorker, probe = processStartTime }) {
  return withRunLock(runId, async () => {
    const current = await readRun(runId);
    if (current.status !== 'active') throw new StateError('run-ended', `run ${runId} has already ended`);
    const open = Object.keys(current.blocks).filter((id) => current.blocks[id].status === 'open');
    if (open.length > 0) throw new StateError('open-blocks', `run ${runId} still has open blocks: ${open.join(', ')}`);
    const pin = await checkWorkerPin(current, { probe });
    if (!pin.ok) await writeSigned(runId, writeRow, { event: pin.event, reason: pin.reason });
    const stop = pin.ok ? await stopWorker(current.worker, { probe }) : { stopped: false };
    current.status = 'ended';
    current.ended_at = new Date().toISOString();
    await writeSigned(runId, writeRow, { event: 'run.end', worker_check: pin.ok ? 'ok' : pin.event, worker_stopped: stop.stopped });
    await saveRun(current);
    return current;
  });
}
