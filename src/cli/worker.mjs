/**
 * `code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]` (plan §4.8; block B11).
 *
 * Serves the run's review queue until SIGTERM/SIGINT (`--once`: drain the queue once and exit).
 * Started detached by the orchestrator (`run start`), never by a coder — `worker` is on the
 * forbidden list (§8.4 ★). Prints one status line on stderr; never a key.
 * Exit codes: 0 stopped cleanly, 1 refused (no run, run ended, another worker, no config), 2 usage.
 */

import { bootWorker } from '../worker/loop.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE = 'usage: code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]\n';

/**
 * @param {string[]} args
 * @param {import('../worker/loop.mjs').WorkerDeps & {stderr?: {write: (s: string) => unknown}}} [deps]
 * @returns {Promise<number>}
 */
export async function runWorker(args, deps = {}) {
  const stderr = deps.stderr ?? process.stderr;
  let parsed;
  try {
    parsed = parseFlags(args, { values: ['run', 'cwd', 'poll-ms'], booleans: ['once'] });
  } catch (err) {
    writeSafe(stderr, `worker: ${err.message}\n${USAGE}`);
    return 2;
  }
  const { flags, positionals } = parsed;
  if (positionals.length > 0 || typeof flags.run !== 'string') {
    writeSafe(stderr, USAGE);
    return 2;
  }
  let worker;
  try {
    const pollMs = intFlag(flags['poll-ms'], 'poll-ms');
    worker = await bootWorker({ runId: flags.run, cwd: typeof flags.cwd === 'string' ? flags.cwd : undefined, pollMs }, deps);
  } catch (err) {
    writeSafe(stderr, `worker: ${err?.message ?? String(err)}\n`);
    return err instanceof StateError && err.code === 'usage' ? 2 : 1;
  }
  // A failure inside the loop is reported as one redacted line (`writeSafe`) with exit 1, never as
  // an unhandled rejection whose raw stack could carry a key.
  if (flags.once) {
    try {
      const done = await worker.once(); // announces, drains, then always stops and releases
      writeSafe(stderr, `worker: drained ${done} ticket(s)\n`);
      return 0;
    } catch (err) {
      writeSafe(stderr, `worker: ${err?.message ?? String(err)}\n`);
      return 1;
    }
  }
  const stop = () => worker.stop();
  process.on('SIGTERM', stop);
  process.on('SIGINT', stop);
  writeSafe(stderr, `worker: pid ${process.pid} serving run ${flags.run} · jev key ${worker.jevKeyResolved ? 'resolved' : 'absent (fallback)'}\n`);
  try {
    await worker.run();
  } catch (err) {
    writeSafe(stderr, `worker: ${err?.message ?? String(err)}\n`);
    return 1;
  } finally {
    process.removeListener('SIGTERM', stop);
    process.removeListener('SIGINT', stop);
  }
  writeSafe(stderr, 'worker: stopped\n');
  return 0;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function run(args) {
  return runWorker(args);
}
