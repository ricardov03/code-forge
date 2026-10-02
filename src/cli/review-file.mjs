/**
 * `code-forge review-file` (plan §4.1, §4.8, V4; block B11) — the coder's side of async review.
 *
 *   review-file <path> --block <id> [--run <id>]   enqueue; prints {ticket, status: queued|done, file}
 *   review-file --wait <ticket> [--max <90s>]      poll; prints {ticket, status: done, result} or
 *                                                  {ticket, status: pending, reason: wait_timeout}
 *                                                  when --max elapses (the review is still running)
 *
 * The same content enqueued again after an `unavailable` result is a fresh attempt (`retry: true`),
 * not the old result.
 * A done result that is not approved and carries open findings adds `fix_list` (what the coder
 * fixes before re-running `review-file <path>`, which is then a round-2 recheck of the fix hunks,
 * §4.11); a result the loop stopped adds `stop` with the reason (`review_cap`, …) — the coder
 * stops and reports; only the orchestrator and the human act on it.
 *
 * Everything is a file read/write inside the workspace (a sandboxed coder has no network). The
 * path is given relative to the current directory and stored relative to the REPOSITORY ROOT
 * (V4); an absolute path or a `..` segment is refused with `bad-path`. With no live worker the
 * answer is `worker_down` — never approval — and nothing is enqueued; the coder must then print
 * `===BLOCK <id> FAILED: worker down===`. While waiting, `worker_down` needs the worker to look
 * down on two polls in a row, and a worker whose pid is alive with a fresh heartbeat is never down
 * (`liveWorker`, B30). Output is one JSON line on stdout.
 * Exit codes: 0 queued/done/pending, 1 bad-path or another refusal, 2 usage, 3 worker_down.
 */

import { readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { enqueue, isDone, liveWorker, queueDir } from '../worker/queue.mjs';
import { assertBlockId, assertTicketId, normalizeRequestPath, repoRootOf, WorkerError } from '../worker/ticket.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE = 'usage: code-forge review-file <path> --block <id> [--run <id>] | review-file --wait <ticket> [--max <seconds>s]\n';

export const DEFAULT_WAIT_MS = 90_000;
const POLL_MS = 200;

/**
 * `90s`, `90`, `1500ms` → milliseconds.
 * @param {unknown} raw @returns {number}
 */
export function parseMax(raw) {
  const m = typeof raw === 'string' ? /^(\d{1,7})(ms|s)?$/.exec(raw) : null;
  if (!m) throw new WorkerError('usage', '--max must look like 90s or 1500ms');
  const n = Number(m[1]);
  return m[2] === 'ms' ? n : n * 1000;
}

/**
 * @param {string} repoRoot @param {string} ticket
 * @returns {Record<string, any> | null} the worker's result named by the done marker.
 */
function doneResult(repoRoot, ticket) {
  try {
    const marker = JSON.parse(readFileSync(path.join(queueDir(repoRoot), `${ticket}.done`), 'utf8'));
    const rel = typeof marker.result === 'string' ? marker.result : '';
    if (!rel.startsWith(`.code-forge${path.sep}reviews${path.sep}`) || rel.split(path.sep).includes('..')) return null;
    return JSON.parse(readFileSync(path.join(repoRoot, rel), 'utf8'));
  } catch {
    return null;
  }
}

/**
 * The coder-facing summary of a done result: the fix list and the stop reason, when any.
 * @param {Record<string, any> | null} result
 * @returns {Record<string, any>}
 */
export function fixList(result) {
  if (!result || result.approved === true) return {};
  /** @type {Record<string, any>} */
  const out = {};
  if (Array.isArray(result.findings) && result.findings.length > 0) {
    out.fix_list = result.findings.map((/** @type {Record<string, any>} */ f) => ({
      id: f?.id ?? null,
      severity: f?.severity ?? null,
      lines: `${f?.line_start ?? '?'}-${f?.line_end ?? '?'}`,
      claim: f?.claim ?? '',
      fix: f?.fix ?? '',
    }));
  }
  if (typeof result.stopped === 'string') out.stop = result.stopped;
  return out;
}

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}, cwd?: string, pollMs?: number}} [deps]
 * @returns {Promise<number>}
 */
export async function runReviewFile(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const say = (/** @type {Record<string, any>} */ obj) => writeSafe(stdout, `${JSON.stringify(obj)}\n`);
  const cwd = deps.cwd ?? process.cwd();
  try {
    const { flags, positionals } = parseFlags(args, { values: ['block', 'run', 'wait', 'max'] });
    const repoRoot = await repoRootOf(cwd);

    if (typeof flags.wait === 'string') {
      if (positionals.length > 0 || flags.block !== undefined) throw new WorkerError('usage', '--wait takes only a ticket and --max');
      const ticket = assertTicketId(flags.wait);
      const maxMs = flags.max === undefined ? DEFAULT_WAIT_MS : parseMax(flags.max);
      const deadline = Date.now() + maxMs;
      let downPolls = 0;
      for (;;) {
        if (isDone(repoRoot, ticket)) {
          const result = doneResult(repoRoot, ticket);
          say({ ticket, status: 'done', result, ...fixList(result) });
          return 0;
        }
        downPolls = liveWorker(repoRoot) ? 0 : downPolls + 1;
        if (downPolls >= 2 && !isDone(repoRoot, ticket)) {
          say({ ticket, status: 'worker_down' });
          return 3;
        }
        if (Date.now() >= deadline && downPolls === 0) {
          say({ ticket, status: 'pending', reason: 'wait_timeout' });
          return 0;
        }
        await new Promise((resolve) => setTimeout(resolve, deps.pollMs ?? POLL_MS));
      }
    }

    if (positionals.length !== 1 || typeof flags.block !== 'string' || flags.max !== undefined) throw new WorkerError('usage', 'give one path and --block');
    const block = assertBlockId(flags.block);
    const file = normalizeRequestPath(positionals[0], cwd, repoRoot);
    const worker = liveWorker(repoRoot);
    if (!worker || (typeof flags.run === 'string' && flags.run !== worker.run)) {
      say({ status: 'worker_down', file });
      return 3;
    }
    const queued = enqueue({ repoRoot, run: worker.run, block, file });
    if (queued.status === 'done' && doneResult(repoRoot, queued.ticket)?.status === 'unavailable') {
      // `unavailable` is never approval AND never final: drop the done marker so the worker runs
      // the ticket again (the fix loop re-runs its pending round); the signed result is replaced.
      rmSync(path.join(queueDir(repoRoot), `${queued.ticket}.done`), { force: true });
      say({ ...enqueue({ repoRoot, run: worker.run, block, file }), retry: true });
      return 0;
    }
    say(queued);
    return 0;
  } catch (err) {
    const code = err instanceof WorkerError || err?.name === 'StateError' ? err.code : 'error';
    if (code === 'usage') {
      writeSafe(stderr, `review-file: ${err.message}\n${USAGE}`);
      return 2;
    }
    say({ status: 'refused', reason: code, message: err?.message ?? String(err) });
    return 1;
  }
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function run(args) {
  return runReviewFile(args);
}
