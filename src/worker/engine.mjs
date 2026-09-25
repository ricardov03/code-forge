/**
 * The review-engine hook (plan §4.2–§4.4; block B11 stub, replaced by B12a).
 *
 * `reviewTicket(ticket, ctx)` is the ONE seam between the worker (queue, keys, signing, pid
 * hygiene — B11) and the review engine (packet, context, lenses, S1 risk, triage — B12a). B12a
 * replaces this module's body and keeps the signature; the worker signs whatever it returns.
 *
 * The stub builds a minimal packet (path + current content), runs ONE closed-book L2 `reviewer`
 * session through `ctx.spawn` (the worker's `spawnSession` with the run root, timeout and the
 * key-free env already applied), and returns the session outcome. It NEVER approves: approval is
 * the engine's decision (`review.approved`, B12a), and a stub that approved would let a block
 * close unreviewed.
 */

import { readFileSync, rmSync, writeFileSync, mkdirSync } from 'node:fs';
import path from 'node:path';

/**
 * @typedef {object} ReviewContext
 * @property {string} repoRoot
 * @property {string} runId
 * @property {string} runRootDir - the run's temp root (packets and session dirs live under it).
 * @property {Record<string, any>} cfg - the project config.
 * @property {string | null} jevKey - resolved once by the worker through B2's chain; never print.
 * @property {(opts: Record<string, any>) => Promise<Record<string, any>>} spawn - one isolated
 *   session (`spawnSession` opts minus `cfg`, `runRoot`, `run`, `block`, `timeoutMs`).
 */

/**
 * @typedef {object} ReviewOutcome
 * @property {'reviewed' | 'unavailable'} status
 * @property {boolean} approved - always false from the stub.
 * @property {string} engine
 * @property {Array<Record<string, any>>} sessions - one summary per session run.
 */

/**
 * @param {import('./queue.mjs').Ticket} ticket
 * @param {ReviewContext} ctx
 * @returns {Promise<ReviewOutcome>}
 */
export async function reviewTicket(ticket, ctx) {
  const dir = path.join(ctx.runRootDir, 'packets');
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const packet = path.join(dir, `${ticket.ticket}.md`);
  let content = '';
  try {
    content = readFileSync(path.join(ctx.repoRoot, ticket.file), 'utf8');
  } catch {
    content = '(deleted)';
  }
  writeFileSync(packet, `# review packet (stub)\nfile: ${ticket.file}\ncontent_hash: ${ticket.content_hash}\n\n${content}\n`, { mode: 0o600 });
  try {
    const res = await ctx.spawn({ level: 'L2', role: 'reviewer', promptPath: packet });
    const session = { status: res.status, reason: res.reason ?? null, provider: res.provider ?? null, model: res.model ?? null, fallback_step: res.fallback_step ?? 0 };
    return { status: res.status === 'ok' ? 'reviewed' : 'unavailable', approved: false, engine: 'stub', sessions: [session] };
  } finally {
    rmSync(packet, { force: true });
  }
}
