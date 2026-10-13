/**
 * B72 (issues #12, #15): `block reopen <id> --file <path> --run <r> --reason <text>` — the human
 * restarts the review of a file the fix loop STOPPED (any reason: `review_cap`,
 * `l3_patch_exhausted`, `split_required`, …) or whose state is LOST (B68). Only a stopped file
 * needs content that differs from what its stop reviewed; a lost file restarts on its current
 * content.
 *
 * Human-only like `block waive`: the verb is on `CODER_ONLY_FORBIDDEN` (`src/util/forbidden.mjs`)
 * and its row is signed with the run key, `by: human` (`block waive` has no other in-verb guard,
 * so neither has this). The verb (`reopenFile`) refuses, writing no row, with a stable code:
 *  - `usage` (exit 2) — no `--file`, no `--reason`, or a path that is not a normalised
 *    repo-root-relative path;
 *  - `unknown_block` — the run does not know the block (the code `block waive` uses);
 *  - `reopen-block-not-open` — the block is not open (stopped or closed);
 *  - `reopen-not-owned` — the block does not own the file;
 *  - `reopen-tampered` — the file's state does not verify (an integrity failure stays a security
 *    event; the owner's exit is `block close --no-require-reviews`);
 *  - `reopen-not-stopped` — no state, or a state that is open or complete;
 *  - `reopen-same-content` — a stopped file whose current content is the content its stop
 *    reviewed (no free re-roll: the human waives the findings instead), or a stop whose content
 *    is unknown;
 *  - `reopen-already-recorded` — a verified human row already names the file's latest anchor seq
 *    for other content (run `review-file`, or the content changed since that reopen);
 *  - `reopen-write-failed` — the signed row was not written.
 * Accepted, it first drops the done marker of the current content's ticket (C11, `io.clearDone`),
 * then writes ONE signed `review.reopened {block, file, from: stopped|lost, stop_reason, at_seq,
 * from_hash, to_hash, reason, by: human}` row (`at_seq` = the file's latest anchor seq). There is
 * at most one verified human row per `at_seq`: a retry on the same content finds it and writes
 * none.
 *
 * The content a stop reviewed (`stopContentHash`) is the stopped state's `stop_content_hash`,
 * which the worker sets on every stop (`src/worker/engine.mjs`) and which is signed through the
 * anchor. A state from before B72 has none: then the latest verified `review.cap` row of THIS
 * stop counts — written after the file's latest verified `review.reopened` row, with the state's
 * round and stop reason — else the content is unknown and the reopen is refused.
 *
 * The worker (`fixLoopRound`) asks `reopenRowFor`: a verified human row of this run for the block
 * and file whose `at_seq` is the file's latest anchor seq starts a new state (round 1, a FULL
 * review, `reviewed_content` null, the level from the run record, `l3_rung_used` kept). The new
 * state saves at `at_seq + 1`, so a row works once. For `stopped` the content must still differ
 * from the stop's content; else the stop repeats.
 *
 * This file imports nothing from `src/worker/`: the fix-loop state is read through `io`.
 */
import path from 'node:path';
import { StateError } from '../state/paths.mjs';
import { verifyRow } from '../state/signer.mjs';

export const REOPENED_EVENT = 'review.reopened';

/**
 * Whether a row's MAC verifies with the run key (a throw is a no).
 * @param {Record<string, any>} row @param {Buffer} key
 */
function verified(row, key) {
  try {
    return verifyRow(row, key).ok === true;
  } catch {
    return false;
  }
}

/**
 * The content hash the stop of a stopped state reviewed, or null when it is unknown: the state's
 * `stop_content_hash`; for a state from before B72, the latest verified `review.cap` row of this
 * run for the block and file that belongs to this stop (after the file's latest verified
 * `review.reopened` row, with the state's round and stop reason).
 * @param {Record<string, any> | null} state
 * @param {ReadonlyArray<Record<string, any>>} rows
 * @param {{runId: string, block: string, file: string, key: Buffer}} at
 * @returns {string | null}
 */
export function stopContentHash(state, rows, { runId, block, file, key }) {
  if (state?.status !== 'stopped') return null;
  if (typeof state.stop_content_hash === 'string' && state.stop_content_hash.length > 0) return state.stop_content_hash;
  const mine = (/** @type {Record<string, any>} */ r) => r?.run === runId && r.block === block && r.file === file && verified(r, key);
  for (let i = rows.length - 1; i >= 0; i -= 1) {
    const r = rows[i];
    if (r?.event === REOPENED_EVENT && mine(r)) return null;
    if (r?.event !== 'review.cap' || !mine(r)) continue;
    if (typeof r.content_hash !== 'string' || r.round !== state.round || r.reason !== state.next?.reason) return null;
    return r.content_hash;
  }
  return null;
}

/**
 * The block checks every reopen runs first, before any config, key or ledger read: the run must
 * know the block (`unknown_block`, the code `block waive` uses) and the block must be open.
 * @param {Record<string, any>} record @param {string} runId @param {string} id
 * @returns {Record<string, any>} the block's entry.
 * @throws {StateError} `unknown_block` or `reopen-block-not-open`.
 */
export function openBlockEntry(record, runId, id) {
  const entry = record.blocks?.[id];
  if (!entry) throw new StateError('unknown_block', `block ${id} is not in run ${runId}`);
  if (entry.status !== 'open') throw new StateError('reopen-block-not-open', `reopen-block-not-open: block ${id} is not open in run ${runId}; nothing was recorded`);
  return entry;
}

/**
 * The latest verified human `review.reopened` row of this run for the block and file whose
 * `at_seq` is `seq`, or null.
 * @param {ReadonlyArray<Record<string, any>>} rows
 * @param {{runId: string, block: string, file: string, key: Buffer, seq: number}} at
 * @returns {Record<string, any> | null}
 */
function latestRowAt(rows, { runId, block, file, key, seq }) {
  for (let i = rows.length - 1; i >= 0; i -= 1) {
    const r = rows[i];
    if (r?.event !== REOPENED_EVENT || r.run !== runId || r.block !== block || r.file !== file) continue;
    if (r.by !== 'human' || r.at_seq !== seq || !verified(r, key)) continue;
    return r;
  }
  return null;
}

/**
 * Worker side: the `review.reopened` row that restarts this file now, or null. Only a row whose MAC
 * verifies with the run key, of this run, block and file, `by: human`, and whose `at_seq` is the
 * file's latest anchor seq counts (the latest such row). A `lost` state needs `from: lost`; a
 * stopped state needs `from: stopped` AND current content that is neither the row's `from_hash`
 * nor the content the stop reviewed (`stopContentHash`; unknown ⇒ no restart).
 * @param {ReadonlyArray<Record<string, any>>} rows
 * @param {{runId: string, block: string, file: string, key: Buffer, status: string, state: Record<string, any> | null, seq: number, contentHash: string}} at
 * @returns {Record<string, any> | null}
 */
export function reopenRowFor(rows, { runId, block, file, key, status, state, seq, contentHash }) {
  const row = latestRowAt(rows, { runId, block, file, key, seq });
  if (row === null) return null;
  if (status === 'lost') return row.from === 'lost' ? row : null;
  if ((status === 'ok' || status === 'restored') && state?.status === 'stopped' && row.from === 'stopped') {
    const stopHash = stopContentHash(state, rows, { runId, block, file, key });
    if (stopHash === null || contentHash === stopHash || contentHash === row.from_hash) return null;
    return row;
  }
  return null;
}

/**
 * The verb (`block reopen`): check, drop the done marker (`io.clearDone`), then write the signed
 * row — or return the verified human row an earlier try wrote for the same `at_seq` and `to_hash`
 * (at most one row per `at_seq`: one for other content is refused `reopen-already-recorded`).
 * @param {{
 *   runId: string, id: string, file: unknown, reason: unknown,
 *   record: Record<string, any>, rows: ReadonlyArray<Record<string, any>>, key: Buffer,
 *   writeRow: (row: Record<string, any>) => Promise<unknown>,
 *   writeSigned: (runId: string, writeRow: (row: Record<string, any>) => Promise<unknown>, row: Record<string, any>) => Promise<unknown>,
 *   io: {
 *     owns: (owned: ReadonlyArray<string>, file: string, kinds?: Readonly<Record<string, string>>) => boolean,
 *     loadState: (where: Record<string, any>) => {status: string, state: Record<string, any> | null, seq: number},
 *     contentHash: (file: string) => string,
 *     clearDone: (file: string) => boolean,
 *   },
 * }} opts
 * @returns {Promise<{row: Record<string, any>, cleared: boolean, existing: boolean}>}
 * @throws {StateError} `usage` or a refusal code (see the module doc).
 */
export async function reopenFile({ runId, id, file, reason, record, rows, key, writeRow, writeSigned, io }) {
  if (typeof file !== 'string' || file.length === 0) throw new StateError('usage', 'block reopen needs --file <path>');
  if (typeof reason !== 'string' || reason.trim().length === 0) throw new StateError('usage', 'block reopen needs --reason');
  if (path.isAbsolute(file) || file.split('/').includes('..') || file.startsWith('./') || path.posix.normalize(file) !== file) {
    throw new StateError('usage', '--file must be repo-root-relative');
  }
  const entry = openBlockEntry(record, runId, id);
  const owned = Array.isArray(entry.owned_files) ? entry.owned_files : [];
  if (!io.owns(owned, file, entry.owned_kinds)) throw new StateError('reopen-not-owned', `reopen-not-owned: block ${id} does not own ${file}; nothing was recorded`);

  const runRows = rows.filter((r) => r?.run === runId);
  const loaded = io.loadState({ runId, block: id, file, key, rows: runRows });
  if (loaded.status === 'tampered') {
    throw new StateError('reopen-tampered', `reopen-tampered: the review state of ${file} does not verify (a security event, not a stop); the owner's way out is block close --no-require-reviews; nothing was recorded`);
  }
  const current = io.contentHash(file);
  /** @type {'stopped' | 'lost'} */
  let from;
  /** @type {string | null} */
  let stopReason = null;
  /** @type {string | null} */
  let fromHash = null;
  if (loaded.status === 'lost') {
    from = 'lost';
  } else if ((loaded.status === 'ok' || loaded.status === 'restored') && loaded.state?.status === 'stopped') {
    from = 'stopped';
    stopReason = typeof loaded.state.next?.reason === 'string' ? loaded.state.next.reason : 'stopped';
    fromHash = stopContentHash(loaded.state, runRows, { runId, block: id, file, key });
    if (fromHash === null) {
      throw new StateError('reopen-same-content', `reopen-same-content: the content the stop of ${file} reviewed is unknown, so a change cannot be shown; waive its findings instead (block waive); nothing was recorded`);
    }
    if (fromHash === current) {
      throw new StateError('reopen-same-content', `reopen-same-content: ${file} still has the content its stop (${stopReason}) reviewed; change the file first, or waive its findings (block waive); nothing was recorded`);
    }
  } else {
    const what = loaded.status === 'none' || loaded.state === null ? 'has no review state' : `is ${loaded.state.status}`;
    throw new StateError('reopen-not-stopped', `reopen-not-stopped: ${file} ${what}; only a stopped or lost file is reopened; nothing was recorded`);
  }
  if (!Number.isInteger(loaded.seq) || loaded.seq < 0) {
    throw new StateError('reopen-tampered', `reopen-tampered: the review state of ${file} has no valid anchor seq; nothing was recorded`);
  }
  // one verified human row per at_seq: the same content reuses it, other content is refused
  const earlier = latestRowAt(runRows, { runId, block: id, file, key, seq: loaded.seq });
  if (earlier !== null && earlier.to_hash !== current) {
    throw new StateError('reopen-already-recorded', `reopen-already-recorded: ${file} was already reopened at state seq ${loaded.seq} for other content; run review-file, or the content changed since the reopen; nothing was recorded`);
  }
  // C11: the cached result of the current content's ticket would repeat the stop — dropped FIRST,
  // so a failed row write leaves at worst a re-run that returns the same stop, never a row with a
  // stale marker
  const cleared = io.clearDone(file);
  if (earlier !== null) return { row: earlier, cleared, existing: true };
  const row = { event: REOPENED_EVENT, block: id, file, from, stop_reason: stopReason, at_seq: loaded.seq, from_hash: fromHash, to_hash: current, reason: reason.trim(), by: 'human' };
  /** @type {Record<string, any> | null} */
  let written = null;
  let signedOut = null;
  try {
    signedOut = await writeSigned(runId, async (signed) => {
      const result = await writeRow(signed);
      // the ledger's appendRow resolves with the row it wrote; nothing (or false) is no write
      if (result === null || result === undefined || result === false) return result;
      written = signed;
      return result;
    }, row);
  } catch {
    written = null;
  }
  if (written === null || signedOut === null || signedOut === undefined || signedOut === false) throw new StateError('reopen-write-failed', `reopen-write-failed: the signed review.reopened row for ${file} was not written; run block reopen again`);
  return { row: written, cleared, existing: false };
}
