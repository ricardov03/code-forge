/**
 * The proof lock (plan §4.7 `proof.isolation: lock`, §7.1 `proof.busy`). For stacks whose tests
 * cannot run from an export, one block at a time measures in the SHARED tree: `proof lock <id>`
 * takes a run-wide lock that pauses every other block's gate run, `proof unlock <id>` releases it.
 *
 * The lock lives in the authoritative run record (`record.proof_lock = {block, acquired_at}`),
 * read and written only under B8's run lock (`withRunLock`), so taking it and starting a gate are
 * atomic with respect to each other. While a gate runs, its block carries `gate_running: true`
 * (+ `gate_pid`) in the registry (§4.7's registry shape):
 *
 *   - a gate of block B while A holds the lock ⇒ `{ok: false, result: 'proof.busy'}` (not run);
 *   - `proof lock` for A while another block's gate is running (its pid alive) ⇒ `proof.busy`.
 *
 * Solo's `lock_acquire` is an MCP tool the orchestrator may use on top of this; the CLI cannot
 * call it, so the run record IS the lock file here. Every lock/unlock writes a signed `proof` row
 * with `isolation: lock` through the B6 writer.
 */

import { runGates } from '../gates/run.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun, saveRun, withRunLock } from '../state/run.mjs';
import { isAlive } from '../util/reaper.mjs';
import { recordProof } from './export.mjs';

/** @typedef {import('../state/run.mjs').WriteRow} WriteRow */

/**
 * @param {Record<string, any>} record @param {string} blockId
 * @returns {Record<string, any>} the open block
 */
function openBlockOf(record, blockId) {
  if (record.status !== 'active') throw new StateError('run-ended', `run ${record.run_id} has ended`);
  const block = record.blocks?.[blockId];
  if (block?.status !== 'open') throw new StateError('no-block', `block ${blockId} is not open in run ${record.run_id}`);
  return block;
}

/** @param {Record<string, any>} block @returns {boolean} a gate is running for it right now */
function gateLive(block) {
  if (block.gate_running !== true) return false;
  return !Number.isInteger(block.gate_pid) || isAlive(block.gate_pid);
}

/**
 * The block holding the lock, or null.
 * @param {string} runId
 * @returns {Promise<string | null>}
 */
export async function lockHolder(runId) {
  const record = await readRun(runId);
  return record.proof_lock?.block ?? null;
}

/**
 * `proof lock <id>`. Idempotent for the holder.
 * @param {{runId: string, blockId: string, writeRow: WriteRow, now?: Date}} opts
 * @returns {Promise<{block: string, acquired_at: string}>}
 * @throws {StateError} `proof.busy` — another block holds the lock or has a gate running.
 */
export async function acquireProofLock({ runId, blockId, writeRow, now = new Date() }) {
  return withRunLock(runId, async () => {
    const record = await readRun(runId);
    openBlockOf(record, blockId);
    const held = record.proof_lock;
    if (held && held.block === blockId) return held;
    if (held) throw new StateError('proof.busy', `proof.busy: block ${held.block} holds the proof lock`);
    const running = Object.entries(record.blocks)
      .filter(([id, b]) => id !== blockId && b.status === 'open' && gateLive(b))
      .map(([id]) => id);
    if (running.length > 0) throw new StateError('proof.busy', `proof.busy: gate running for block ${running.join(', ')}`);
    const lock = { block: blockId, acquired_at: now.toISOString() };
    await recordProof({ runId, writeRow, row: { block: blockId, isolation: 'lock', step: 'lock' } });
    record.proof_lock = lock;
    await saveRun(record);
    return lock;
  });
}

/**
 * `proof unlock <id>` — only the holder releases.
 * @param {{runId: string, blockId: string, writeRow: WriteRow}} opts
 * @throws {StateError} `not-locked`, `not-holder`
 */
export async function releaseProofLock({ runId, blockId, writeRow }) {
  return withRunLock(runId, async () => {
    const record = await readRun(runId);
    const held = record.proof_lock;
    if (!held) throw new StateError('not-locked', `run ${runId} has no proof lock`);
    if (held.block !== blockId) throw new StateError('not-holder', `block ${blockId} does not hold the proof lock (${held.block} does)`);
    await recordProof({ runId, writeRow, row: { block: blockId, isolation: 'lock', step: 'unlock' } });
    delete record.proof_lock;
    await saveRun(record);
  });
}

/**
 * Run `fn` as block `blockId`'s gate: refused with `proof.busy` while ANOTHER block holds the
 * proof lock; otherwise `gate_running` is set for the duration and cleared afterwards (also on a
 * throw).
 * @template T
 * @param {{runId: string, blockId: string, fn: () => Promise<T>, pid?: number}} opts
 * @returns {Promise<{ok: true, result: 'ran', value: T} | {ok: false, result: 'proof.busy', holder: string}>}
 */
export async function withGateSlot({ runId, blockId, fn, pid = process.pid }) {
  const holder = await withRunLock(runId, async () => {
    const record = await readRun(runId);
    const block = openBlockOf(record, blockId);
    const held = record.proof_lock?.block;
    if (held && held !== blockId) return held;
    block.gate_running = true;
    block.gate_pid = pid;
    await saveRun(record);
    return null;
  });
  if (holder !== null) return { ok: false, result: 'proof.busy', holder };
  try {
    return { ok: true, result: 'ran', value: await fn() };
  } finally {
    await withRunLock(runId, async () => {
      const record = await readRun(runId);
      const block = record.blocks?.[blockId];
      if (block) {
        delete block.gate_running;
        delete block.gate_pid;
        await saveRun(record);
      }
    });
  }
}

/**
 * B5's gate runner behind the lock — the entry point a gate caller (`gates run --run --block`,
 * the B12 block gate) uses.
 * @param {{runId: string, blockId: string, gates: Parameters<typeof runGates>[0], cwd: string, timeoutMs?: number}} opts
 */
export async function runGatesGuarded({ runId, blockId, gates, cwd, timeoutMs }) {
  return withGateSlot({ runId, blockId, fn: () => runGates(gates, { cwd, ...(timeoutMs ? { timeoutMs } : {}) }) });
}
