/**
 * `code-forge run start|status|end` (plan §4.9). The orchestrator runs these; a coder never does
 * (`run start` is on the forbidden list, ★ `code-forge-worker-from-coder`).
 *
 *   run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>]
 *   run start --reattach --run <id> [--worker-pid <pid>]
 *   run status --run <id>     (B33: includes spent_usd / budget_usd / unknown_usd_sessions)
 *   run reload --run <id>
 *   run end --run <id>
 *
 * B11: from the CLI (the default export), `run start` without `--worker-pid` sweeps dead run
 * roots, launches `code-forge worker --run <id>` detached, waits until it announces itself in the
 * queue and pins it; `run start --reattach` without `--worker-pid` re-pins the live worker the
 * queue announces (`repinWorker`). Called as `runRun(args, deps)` without `deps.startWorker` (the
 * state tests), no worker is launched and none is pinned unless `--worker-pid` is given.
 *
 * B35 `run reload`: re-read and validate the workspace's `.code-forge.yml` and replace the run's
 * config snapshot (`reloadRun`) — no `block stop` / `run end` / `run start` cycle. An invalid
 * config or a change to a key that is fixed for the run is refused with nothing changed. It prints
 * the changed key PATHS (never values); queued review tickets keep the config they were enqueued
 * with, new tickets use the new one, and the worker picks it up on its next ticket (it reads the
 * run record per ticket — no restart).
 */

import { readdirSync } from 'node:fs';
import path from 'node:path';
import { DEFAULT_CONFIG_FILENAME, loadProjectConfig } from '../config/load.mjs';
import { loadSeenInCache } from '../config/refresh.mjs';
import { validateConfig } from '../config/validate.mjs';
import { budgetUsdOf, runSpend } from '../ledger/spend.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { endRun, processStartTime, readRun, reattachWorker, reloadRun, startRun, stopPinnedWorker } from '../state/run.mjs';
import { writeSafe } from '../util/redact.mjs';
import { sweepRoots, tmpBase } from '../util/tmp.mjs';
import { launchWorker, repinWorker } from '../worker/loop.mjs';
import { pendingTickets, queueDir } from '../worker/queue.mjs';
import { repoRootOf, WorkerError } from '../worker/ticket.mjs';
import { hasCliOnPath } from './validate.mjs';

/**
 * `project.slug` from config, else the workspace directory name as a ledger slug. The configured
 * value is NOT trusted here: `startRun` refuses any slug outside `/^[a-z0-9][a-z0-9-]*$/`
 * (`bad-project`) before anything is written, so `../x` never reaches the ledger path. Run ids
 * are checked the same way by the state layer (`assertRunId`, `bad-run-id`).
 * @param {Record<string, any>} cfg @param {string} workspace
 * @returns {string}
 */
export function slugFor(cfg, workspace) {
  const configured = cfg?.project?.slug;
  if (typeof configured === 'string' && configured.length > 0) return configured;
  const derived = path.basename(workspace).toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '');
  return derived || 'project';
}

/**
 * B33: the run's spend so far (estimated USD from its ledger rows, open blocks included) and its
 * `budget.usd` (read from the workspace config now — the value the next session is checked
 * against; null when unset or the config does not load).
 * @param {Record<string, any>} record
 * @returns {Promise<{spent_usd: number, budget_usd: number | null, unknown_usd_sessions: number}>}
 */
async function spendOf(record) {
  const { usd, unknown } = runSpend(await readAllRows(record.project), record.run_id);
  let budget = null;
  try {
    const loaded = await loadProjectConfig(record.workspace);
    budget = loaded.ok ? budgetUsdOf(loaded.config) : null;
  } catch {
    budget = null;
  }
  return { spent_usd: usd, budget_usd: budget, unknown_usd_sessions: unknown };
}

/** @param {string} slug */
const ledgerWriter = (slug) => (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });

/** @typedef {(pin: {pid: number, started_at: string}, opts: {probe?: import('../state/run.mjs').StartTimeProbe}) => Promise<{stopped: boolean, reason?: string}>} StopWorker */

/**
 * A worker that `run start` launched but could not pin must not outlive the command as an orphan:
 * stop it through the pinned-worker stopper (pid > 1, start time re-checked right before the
 * signal, so a recycled pid is never signalled) and name the pid on stderr. `startedAt` is the
 * start time read right after the launch; null means the process was already gone.
 * @param {{pid: number, startedAt: string | null}} launched
 * @param {{stopWorker: StopWorker, probe?: import('../state/run.mjs').StartTimeProbe, err: (s: string) => void}} deps
 */
async function stopLaunched({ pid, startedAt }, { stopWorker, probe, err }) {
  /** @type {{stopped: boolean, reason?: string}} */
  let outcome;
  try {
    outcome = startedAt === null ? { stopped: false, reason: 'not running' } : await stopWorker({ pid, started_at: startedAt }, { ...(probe ? { probe } : {}) });
  } catch (thrown) {
    outcome = { stopped: false, reason: thrown?.message ?? String(thrown) };
  }
  err(`run start: launched worker pid ${pid} was not pinned; ${outcome.stopped ? 'stopped it' : `not stopped (${outcome.reason ?? 'unknown'})`}\n`);
}

/**
 * @param {string[]} args
 * @param {{
 *   stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown},
 *   probe?: import('../state/run.mjs').StartTimeProbe,
 *   stopWorker?: StopWorker,
 *   startWorker?: (opts: {runId: string, workspace: string}) => Promise<{pid: number, repoRoot: string}>,
 * }} [deps] - `startWorker` (the CLI passes B11's `launchWorker`) launches the worker on a plain
 *   `run start`; `stopWorker` defaults to the state layer's `stopPinnedWorker` (pid > 1 and a
 *   re-verified start time before any signal) — `run end` uses it, and so does a `run start` whose
 *   launched worker could not be pinned.
 * @returns {Promise<number>}
 */
export async function runRun(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr, probe, stopWorker, startWorker } = deps;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const [sub, ...rest] = args;
  if (!['start', 'status', 'reload', 'end'].includes(sub)) {
    err(USAGE);
    return 2;
  }
  try {
    const { flags } = parseFlags(rest, { values: ['run', 'cwd', 'engine', 'worker-pid'], booleans: ['reattach'] });
    const runId = typeof flags.run === 'string' ? flags.run : undefined;
    const workerPid = intFlag(flags['worker-pid'], 'worker-pid');

    if (sub === 'start' && flags.reattach) {
      if (!runId) throw new StateError('usage', 'run start --reattach needs --run');
      const { project, workspace } = await readRun(runId);
      if (workerPid !== undefined) {
        await reattachWorker({ runId, workerPid, writeRow: ledgerWriter(project), probe });
        out(`run ${runId}: worker re-pinned to pid ${workerPid}\n`);
        return 0;
      }
      const repinned = await repinWorker({ runId, repoRoot: await repoRootOf(workspace), writeRow: ledgerWriter(project), ...(probe ? { probe } : {}) });
      out(`run ${runId}: worker re-pinned to pid ${repinned.worker.pid}\n`);
      return 0;
    }
    if (sub === 'start') {
      const workspace = path.resolve(typeof flags.cwd === 'string' ? flags.cwd : process.cwd());
      const loaded = await loadProjectConfig(workspace);
      if (!loaded.ok) {
        err(`run start: ${loaded.message}\n`);
        return 2;
      }
      const project = slugFor(loaded.config, workspace);
      const engine = typeof flags.engine === 'string' ? flags.engine : undefined;
      const tmpRoot = typeof loaded.config?.tmp?.root === 'string' ? loaded.config.tmp.root : undefined;
      await sweepRoots({ root: tmpBase(tmpRoot) }); // dead runs' temp roots and orphans (§9.6, V12)
      let record = await startRun({ workspace, project, config: loaded.config, engine, runId, workerPid, writeRow: ledgerWriter(project), probe });
      if (!record.worker && startWorker) {
        const launched = await startWorker({ runId: record.run_id, workspace });
        const startedAt = await (probe ?? processStartTime)(launched.pid); // what the stop below re-checks against
        try {
          // pin ONLY the pid we launched: an older announced worker must not be pinned over it
          record = await repinWorker({ runId: record.run_id, repoRoot: launched.repoRoot, writeRow: ledgerWriter(project), expectPid: launched.pid, ...(probe ? { probe } : {}) });
        } catch (thrown) {
          await stopLaunched({ pid: launched.pid, startedAt }, { stopWorker: stopWorker ?? stopPinnedWorker, probe, err });
          throw thrown;
        }
      }
      out(`run ${record.run_id} started · engine ${record.engine} · worker ${record.worker ? `pid ${record.worker.pid}` : 'not pinned'}\n`);
      if (!record.worker) err('run start: no worker pinned — start the worker, then `run start --reattach --run <id> --worker-pid <pid>`\n');
      return 0;
    }
    if (sub === 'status' && runId) {
      const record = await readRun(runId);
      const { run_id, status, engine, blocks, orphans } = record;
      const spend = await spendOf(record);
      out(`${JSON.stringify({ run_id, status, engine, worker_pinned: Boolean(record.worker), blocks, orphans, ...spend }, null, 2)}\n`);
      // on stderr, so stdout stays one JSON document
      const n = spend.unknown_usd_sessions;
      if (n > 0) err(`${n} session${n === 1 ? ' has' : 's have'} no known price; the budget does not count ${n === 1 ? 'it' : 'them'}\n`);
      return 0;
    }
    if (sub === 'reload' && runId) return await reloadVerb(runId, { out, err });
    if (sub === 'end' && runId) {
      const { project } = await readRun(runId);
      const record = await endRun({ runId, writeRow: ledgerWriter(project), stopWorker, probe });
      out(`run ${record.run_id} ended\n`);
      return 0;
    }
  } catch (thrown) {
    err(`run ${sub ?? ''}: ${thrown?.message ?? String(thrown)}\n`);
    return thrown instanceof StateError && thrown.code === 'usage' ? 2 : 1;
  }
  err(USAGE);
  return 2;
}

/**
 * `run reload --run <id>`: load + validate the workspace config, pin the queue's pending tickets
 * to the old snapshot and store the new one (`reloadRun`). Exit 0 on a reload or no change, 1 on a
 * config that does not load or validate, or on a refused key.
 * @param {string} runId
 * @param {{out: (s: string) => void, err: (s: string) => void}} io
 * @returns {Promise<number>}
 */
async function reloadVerb(runId, { out, err }) {
  const { project, workspace, status } = await readRun(runId);
  if (status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
  const loaded = await loadProjectConfig(workspace);
  if (!loaded.ok || !loaded.config) {
    // never the parser's message: it can quote a line of the file
    if (loaded.error === 'not-found') err(`run reload: no ${DEFAULT_CONFIG_FILENAME} in the workspace — nothing changed\n`);
    else {
      const line = loaded.error === 'parse-error' ? /\(line (\d+), column \d+\)$/.exec(loaded.message ?? '')?.[1] : undefined;
      err(`run reload: the config could not be parsed (${DEFAULT_CONFIG_FILENAME}${line ? `:${line}` : ''}) — nothing changed\n`);
    }
    return 1;
  }
  const result = validateConfig(loaded.config, { seenInCache: await loadSeenInCache(), hasCliOnPath });
  if (!result.valid) {
    // rule ids and key paths only: a validator message may quote a value
    const lines = result.errors.map((e) => `  [${e.rule}]${typeof e.path === 'string' && e.path.length > 0 ? ` ${e.path}` : ''}\n`);
    err(`run reload: ${DEFAULT_CONFIG_FILENAME} is invalid (${result.errors.length} error${result.errors.length === 1 ? '' : 's'}) — nothing changed; run code-forge validate\n${lines.join('')}`);
    return 1;
  }
  /** @type {string | null} */
  let repoRoot = null;
  try {
    repoRoot = await repoRootOf(workspace);
  } catch (thrown) {
    if (!(thrown instanceof WorkerError && thrown.code === 'no-repo')) throw thrown;
    // no repository: there is no review queue to pin
  }
  const queueRoot = repoRoot;
  const readPending = () => {
    if (queueRoot === null) return [];
    try {
      readdirSync(queueDir(queueRoot));
    } catch (thrown) {
      if (thrown?.code === 'ENOENT') return [];
      throw new StateError('queue-unreadable', 'cannot read the review queue; nothing changed');
    }
    return pendingTickets(queueRoot);
  };
  const reloaded = await reloadRun({ runId, config: loaded.config, readPending, effectiveSlug: slugFor(loaded.config, workspace), writeRow: ledgerWriter(project) });
  if (reloaded.changed !== null && reloaded.changed.length === 0) {
    out(`run ${runId}: no config change\n`);
    return 0;
  }
  if (reloaded.changed === null) {
    out(`run ${runId}: config snapshot recorded (the run predates run reload; no earlier snapshot to compare)\n`);
  } else {
    out(`run ${runId}: config reloaded · ${reloaded.changed.length} key${reloaded.changed.length === 1 ? '' : 's'} changed\n`);
    for (const key of reloaded.changed) out(`  ${key}\n`);
  }
  out(`queued review tickets (${reloaded.pinned}) keep the config they were enqueued with; new tickets use the new config\n`);
  if (reloaded.rowError !== null) err(`run reload: WARN the run.reload ledger row could not be written (${reloaded.rowError}); the run record holds the new config\n`);
  return 0;
}

const USAGE =
  'usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id>\n';

/** @param {string[]} args @returns {Promise<number>} */
export default async function run(args) {
  return runRun(args, { startWorker: launchWorker });
}
