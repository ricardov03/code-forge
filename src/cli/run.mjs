/**
 * `code-forge run start|status|end` (plan §4.9). The orchestrator runs these; a coder never does
 * (`run start` is on the forbidden list, ★ `code-forge-worker-from-coder`).
 *
 *   run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>]
 *   run start --reattach --run <id> --worker-pid <pid>
 *   run status --run <id>
 *   run end --run <id>
 *
 * The worker verb ships with B11; until it is wired in, `run start` pins the worker the
 * orchestrator started (`--worker-pid`) or records none and says so.
 */

import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { appendRow } from '../ledger/write.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { endRun, readRun, reattachWorker, startRun } from '../state/run.mjs';
import { writeSafe } from '../util/redact.mjs';

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

/** @param {string} slug */
const ledgerWriter = (slug) => (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });

/**
 * @param {string[]} args
 * @param {{
 *   stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown},
 *   probe?: import('../state/run.mjs').StartTimeProbe,
 *   stopWorker?: (pin: {pid: number, started_at: string}, opts: {probe?: import('../state/run.mjs').StartTimeProbe}) => Promise<{stopped: boolean}>,
 * }} [deps] - `stopWorker` defaults to the state layer's `stopPinnedWorker` (pid > 1 and a
 *   re-verified start time before any signal).
 * @returns {Promise<number>}
 */
export async function runRun(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr, probe, stopWorker } = deps;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const [sub, ...rest] = args;
  if (!['start', 'status', 'end'].includes(sub)) {
    err(USAGE);
    return 2;
  }
  try {
    const { flags } = parseFlags(rest, { values: ['run', 'cwd', 'engine', 'worker-pid'], booleans: ['reattach'] });
    const runId = typeof flags.run === 'string' ? flags.run : undefined;
    const workerPid = intFlag(flags['worker-pid'], 'worker-pid');

    if (sub === 'start' && flags.reattach) {
      if (!runId || workerPid === undefined) throw new StateError('usage', 'run start --reattach needs --run and --worker-pid');
      const { project } = await readRun(runId);
      await reattachWorker({ runId, workerPid, writeRow: ledgerWriter(project), probe });
      out(`run ${runId}: worker re-pinned to pid ${workerPid}\n`);
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
      const record = await startRun({ workspace, project, config: loaded.config, engine, runId, workerPid, writeRow: ledgerWriter(project), probe });
      out(`run ${record.run_id} started · engine ${record.engine} · worker ${record.worker ? `pid ${record.worker.pid}` : 'not pinned'}\n`);
      if (!record.worker) err('run start: no worker pinned — start the worker, then `run start --reattach --run <id> --worker-pid <pid>`\n');
      return 0;
    }
    if (sub === 'status' && runId) {
      const record = await readRun(runId);
      const { run_id, status, engine, blocks, orphans } = record;
      out(`${JSON.stringify({ run_id, status, engine, worker_pinned: Boolean(record.worker), blocks, orphans }, null, 2)}\n`);
      return 0;
    }
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

const USAGE =
  'usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] | start --reattach --run <id> --worker-pid <pid> | status --run <id> | end --run <id>\n';

/** @param {string[]} args @returns {Promise<number>} */
export default async function run(args) {
  return runRun(args);
}
