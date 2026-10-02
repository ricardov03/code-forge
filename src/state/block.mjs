/**
 * Block state verbs (plan §4.9): `open`, `attempt`, `rebase`, `claim`, `scope`, `close`, `stop`,
 * and the brief pointer. Every mutation runs under the run lock and writes its SIGNED ledger row
 * through the `writeRow` parameter BEFORE saving the registry in the authoritative run record
 * (a failed ledger write leaves the registry unchanged). `closeBlock` reads the pin and the rows
 * outside the lock and re-checks status and orphans inside it before closing.
 *
 * `closeBlock` is B8's share of the block gate (§4.6): the worker pin (row 9/§4.8), the MAC of
 * every gate-relevant row (row 10), and the orphan rule (row 1). The other rows (reviews, gates,
 * acceptance, proof, forecast) arrive as `extraChecks` from the blocks that own them (B5, B10,
 * B12) — this module never imports them (C17).
 */

import { createHash } from 'node:crypto';
import os from 'node:os';
import { BLOCK_KINDS, blockKind, dispatchLevel } from '../decide/escalation.mjs';
import { exec } from '../util/exec.mjs';
import { isAncestorArgv, revParseArgv } from '../util/git.mjs';
import { StateError } from './paths.mjs';
import { assertOwned, findOverlap, scopeGate } from './registry.mjs';
import { checkWorkerPin, readRun, saveRun, withRunLock, writeSigned } from './run.mjs';
import { verifyRow, loadKey } from './signer.mjs';

/** @typedef {import('./run.mjs').WriteRow} WriteRow */

export const BRIEF_POINTER_MAX_BYTES = 200;

/** Ledger events the block gate requires to carry a valid MAC (§4.6 row 10). */
export const GATE_EVENTS = Object.freeze(['dispatch', 'block.attempt', 'block.rebase', 'block.claim', 'review.approved', 'proof']);

const BLOCK_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/;
const LEVEL = /^L[0-3]$/;

/** `process.env` minus every inherited `GIT_*` (hooks export GIT_DIR…), with no system/global config. */
function gitEnv() {
  /** @type {NodeJS.ProcessEnv} */
  const env = { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull };
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  return env;
}

/** @param {string[]} argv @param {string} cwd @param {number[]} [okExitCodes] */
async function git(argv, cwd, okExitCodes) {
  const res = await exec(argv, { cwd, env: gitEnv(), okExitCodes, timeoutMs: 30000 });
  if (res.result !== 'ok') throw new StateError('git-failed', `${argv.slice(0, 2).join(' ')} failed (exit ${res.code})`);
  return res;
}

/** @param {string} ref @param {string} cwd @returns {Promise<string>} the full commit sha */
async function resolveCommit(ref, cwd) {
  if (typeof ref !== 'string' || ref.length === 0 || ref.startsWith('-')) {
    throw new StateError('bad-ref', `a git ref must be non-empty and not start with "-" (got ${JSON.stringify(ref)})`);
  }
  const res = await git(revParseArgv(`${ref}^{commit}`), cwd);
  return res.stdout.trim();
}

/**
 * `BRIEF <path> lines=<n> sha=<sha8> <<<EOM>>>` — `sha8` is the first 8 hex of the brief's
 * SHA-256, `lines` counts `\n` (plus a final unterminated line). The coder answers
 * `ACK <sha8> lines=<n>` (§5.3, parsed by B4).
 * @param {string} relPath @param {Buffer | string} content
 * @returns {string}
 * @throws {StateError} `pointer-too-long` over 200 bytes, `bad-brief-path` on whitespace.
 */
export function briefPointer(relPath, content) {
  if (typeof relPath !== 'string' || relPath.length === 0 || /\s/.test(relPath)) {
    throw new StateError('bad-brief-path', 'brief path must be non-empty and contain no whitespace');
  }
  const bytes = Buffer.isBuffer(content) ? content : Buffer.from(content, 'utf8');
  const sha8 = createHash('sha256').update(bytes).digest('hex').slice(0, 8);
  let lines = 0;
  for (const byte of bytes) if (byte === 0x0a) lines += 1;
  if (bytes.length > 0 && bytes[bytes.length - 1] !== 0x0a) lines += 1;
  const pointer = `BRIEF ${relPath} lines=${lines} sha=${sha8} <<<EOM>>>`;
  const size = Buffer.byteLength(pointer, 'utf8');
  if (size > BRIEF_POINTER_MAX_BYTES) {
    throw new StateError('pointer-too-long', `brief pointer is ${size} bytes (max ${BRIEF_POINTER_MAX_BYTES}); use a shorter brief path`);
  }
  return pointer;
}

/**
 * @param {unknown} clauses
 * @returns {{clause: string, tests: string[]}[]}
 */
function assertAcceptance(clauses) {
  const ok =
    Array.isArray(clauses) &&
    clauses.length > 0 &&
    clauses.every(
      (c) => typeof c?.clause === 'string' && c.clause.length > 0 && Array.isArray(c.tests) && c.tests.length > 0 && c.tests.every((t) => typeof t === 'string' && t.length > 0),
    );
  if (!ok) throw new StateError('bad-acceptance', 'acceptance: a non-empty list of {clause, tests: [test ids…]} is required');
  return clauses;
}

/**
 * Under the run lock: load the run, check it is active and (when `id` is given) that the block
 * is open, run `mutate`, write its signed `rows` through the ledger writer, THEN save the record —
 * a ledger write that fails leaves the registry unchanged.
 * @template T
 * @param {string} runId @param {string | null} id @param {WriteRow} writeRow
 * @param {(record: Record<string, any>, block: Record<string, any> | undefined) => Promise<{result: T, rows?: Record<string, any>[]}> | {result: T, rows?: Record<string, any>[]}} mutate
 * @returns {Promise<T>}
 */
async function mutateRun(runId, id, writeRow, mutate) {
  return withRunLock(runId, async () => {
    const record = await readRun(runId);
    if (record.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const block = id === null ? undefined : record.blocks[id];
    if (id !== null && block?.status !== 'open') throw new StateError('no-block', `block ${id} is not open in run ${runId}`);
    const { result, rows = [] } = await mutate(record, block);
    for (const row of rows) await writeSigned(runId, writeRow, row);
    await saveRun(record);
    return result;
  });
}

/** @param {Record<string, any>} record @returns {Record<string, any>} the open blocks only */
function activeBlocks(record) {
  return Object.fromEntries(Object.entries(record.blocks).filter(([, b]) => b.status === 'open'));
}

/**
 * `block open` — refuses a second open of the same id and any owned set that overlaps another
 * ACTIVE block's; records base, owned files, level, attempt, acceptance, line forecast and the
 * block kind (B34: the declared `kind`, else `blockKind` of the owned files). A docs/contract
 * block asked for below `levels.coder_floor_docs` (default L1) is opened AT the floor: the entry
 * and the dispatch row carry the floor level, and the row adds `requested_level` and
 * `trigger: 'docs_floor'`.
 * @param {{
 *   runId: string, id: string, level: string, owned: string[], acceptance: unknown,
 *   attempt?: number, base?: string, lines?: number, brief?: {path: string, content: Buffer | string},
 *   kind?: string, cfg?: Record<string, any>, writeRow: WriteRow, now?: Date,
 * }} opts
 * @returns {Promise<{block: Record<string, any>, pointer: string | null}>}
 */
export async function openBlock(opts) {
  const { runId, id, level, attempt = 1, lines = null, writeRow, now = new Date() } = opts;
  if (typeof id !== 'string' || !BLOCK_ID.test(id)) throw new StateError('bad-block-id', `block id must match ${BLOCK_ID}`);
  if (typeof level !== 'string' || !LEVEL.test(level)) throw new StateError('bad-level', 'level must be L0..L3');
  if (!Number.isInteger(attempt) || attempt < 1) throw new StateError('bad-attempt', 'attempt must be an integer >= 1');
  if (lines !== null && !(Number.isInteger(lines) && lines > 0)) throw new StateError('bad-lines', 'lines forecast must be a positive integer');
  if (opts.kind !== undefined && !BLOCK_KINDS.includes(opts.kind)) throw new StateError('bad-kind', `kind must be one of ${BLOCK_KINDS.join(', ')}`);
  const owned = assertOwned(opts.owned);
  const acceptance = assertAcceptance(opts.acceptance);
  const kind = blockKind({ owned, declared: opts.kind });
  const dispatched = dispatchLevel({ lane: level, kind, cfg: opts.cfg });
  const pointer = opts.brief ? briefPointer(opts.brief.path, opts.brief.content) : null;

  const block = await mutateRun(runId, null, writeRow, async (record) => {
    if (record.blocks[id]?.status === 'open') throw new StateError('already-open', `block ${id} is already open`);
    for (const [otherId, other] of Object.entries(activeBlocks(record))) {
      const pair = findOverlap(owned, other.owned_files);
      if (pair) throw new StateError('overlap', `block ${id} owns ${pair[0]}, which overlaps ${pair[1]} owned by open block ${otherId}`);
    }
    const baseSha = await resolveCommit(opts.base ?? 'HEAD', record.workspace);
    const entry = { block: id, base_sha: baseSha, owned_files: [...owned], level: dispatched.level, kind, attempt, opened_at: now.toISOString(), acceptance, lines_forecast: lines, status: 'open' };
    record.blocks[id] = entry;
    const floor = dispatched.trigger === 'docs_floor' ? { requested_level: level, trigger: 'docs_floor' } : {};
    return {
      result: entry,
      rows: [{ event: 'dispatch', block: id, level: dispatched.level, kind, ...floor, attempt, base_sha: baseSha, owned_files: entry.owned_files, lines_forecast: lines }],
    };
  });
  return { block, pointer };
}

/** @param {{runId: string, id: string, writeRow: WriteRow}} opts */
export async function attemptBlock({ runId, id, writeRow }) {
  return mutateRun(runId, id, writeRow, (_record, block) => {
    block.attempt += 1;
    return { result: block.attempt, rows: [{ event: 'block.attempt', block: id, attempt: block.attempt }] };
  });
}

/**
 * `block rebase` — move the base to `head` only when the old base is its ancestor and no commit
 * in between touched a file this block owns (that is a conflict ⇒ stop). Rename detection is OFF
 * (`--no-renames`): a rename lists both its old and its new path, so moving an owned file away
 * is a conflict too. Never silent.
 * @param {{runId: string, id: string, head?: string, writeRow: WriteRow}} opts
 * @returns {Promise<{old_base: string, new_base: string}>}
 */
export async function rebaseBlock({ runId, id, head = 'HEAD', writeRow }) {
  return mutateRun(runId, id, writeRow, async (record, block) => {
    const cwd = record.workspace;
    const newBase = await resolveCommit(head, cwd);
    const ancestor = await git(isAncestorArgv(block.base_sha, newBase), cwd, [0, 1]);
    if (ancestor.code !== 0) throw new StateError('base-not-ancestor', `block ${id}: base ${block.base_sha.slice(0, 12)} is not an ancestor of ${head}`);
    const diff = await git(['git', 'diff', '--no-renames', '--name-only', block.base_sha, newBase], cwd);
    const conflicts = diff.stdout.split('\n').filter((file) => file.length > 0 && findOverlap(block.owned_files, [file]) !== null);
    if (conflicts.length > 0) throw new StateError('owned-conflict', `block ${id}: commits since its base changed owned files: ${conflicts.join(', ')}`);
    const moved = { old_base: block.base_sha, new_base: newBase };
    block.base_sha = newBase;
    return { result: moved, rows: [{ event: 'block.rebase', block: id, ...moved }] };
  });
}

/**
 * `block claim` — assign one EXACT path (an orphan, or any unowned path) to `id`; refused for a
 * glob, and when another open block owns it.
 * @param {{runId: string, id: string, file: string, writeRow: WriteRow}} opts
 */
export async function claimPath({ runId, id, file, writeRow }) {
  assertOwned([file]);
  if (/[*?{]/.test(file)) throw new StateError('bad-owned', `block claim takes one exact path, not a glob (${JSON.stringify(file)})`);
  await mutateRun(runId, id, writeRow, (record, block) => {
    for (const [otherId, other] of Object.entries(activeBlocks(record))) {
      if (otherId !== id && findOverlap([file], other.owned_files)) throw new StateError('overlap', `${file} is owned by open block ${otherId}`);
    }
    if (!block.owned_files.includes(file)) block.owned_files.push(file);
    record.orphans = record.orphans.filter((/** @type {string} */ o) => o !== file);
    return { result: undefined, rows: [{ event: 'block.claim', block: id, path: file }] };
  });
}

/**
 * The scope row for `id` against the tree's file set; NEW orphans are written to the registry
 * and the ledger (`block.orphan`), so every active gate sees them.
 * @param {{runId: string, id: string, files: string[], writeRow: WriteRow}} opts
 * @returns {Promise<{ok: boolean, scope: string[], orphans: string[]}>}
 */
export async function checkScope({ runId, id, files, writeRow }) {
  return mutateRun(runId, id, writeRow, (record) => {
    const gate = scopeGate(activeBlocks(record), id, files);
    const fresh = gate.orphans.filter((o) => !record.orphans.includes(o));
    record.orphans = [...record.orphans, ...fresh];
    return { result: gate, rows: fresh.map((orphan) => ({ event: 'block.orphan', path: orphan })) };
  });
}

/** @param {{runId: string, id: string, reason: string, writeRow: WriteRow, event?: string}} opts */
export async function stopBlock({ runId, id, reason, writeRow, event = 'block.stop' }) {
  await mutateRun(runId, id, writeRow, (_record, block) => {
    block.status = 'stopped';
    block.stop_reason = reason;
    return { result: undefined, rows: [{ event, block: id, reason }] };
  });
}

/**
 * B8's rows of the block gate, in order: worker pin ⇒ `worker.replaced`/`worker.down` (block
 * stopped); the block's signed `dispatch` row must exist and verify, and every other
 * gate-relevant row of the block must verify ⇒ else `ledger.tamper` (block stopped); then
 * `extraChecks`; finally, UNDER the run lock, orphans in the registry ⇒ refused (block stays
 * open), else closed.
 * @param {{
 *   runId: string, id: string, rows: Record<string, any>[], writeRow: WriteRow,
 *   livePid?: number, probe?: import('./run.mjs').StartTimeProbe,
 *   extraChecks?: Array<() => Promise<{ok: boolean, reason?: string}>>,
 * }} opts
 * @returns {Promise<{ok: boolean, status: 'closed' | 'stopped' | 'open', reason?: string, event?: string}>}
 */
export async function closeBlock({ runId, id, rows, writeRow, livePid, probe, extraChecks = [] }) {
  const record = await readRun(runId);
  if (record.blocks[id]?.status !== 'open') throw new StateError('no-block', `block ${id} is not open in run ${runId}`);

  const pin = await checkWorkerPin(record, { livePid, probe });
  if (!pin.ok) {
    await stopBlock({ runId, id, reason: pin.reason, writeRow, event: pin.event });
    return { ok: false, status: 'stopped', event: pin.event, reason: pin.reason };
  }

  const key = await loadKey(runId);
  const relevant = rows.filter((row) => row.block === id && GATE_EVENTS.includes(row.event) && (row.run === undefined || row.run === runId));
  const checked = relevant.map((row) => ({ row, result: verifyRow(row, key) }));
  const bad = checked.filter((x) => !x.result.ok);
  const dispatchOk = checked.some((x) => x.row.event === 'dispatch' && x.result.ok);
  if (bad.length > 0 || !dispatchOk) {
    const reasons = bad.map((x) => `${x.row.event}: ${x.result.reason}`);
    if (!dispatchOk) reasons.push('no verified dispatch row');
    const reason = reasons.join('; ');
    await stopBlock({ runId, id, reason, writeRow, event: 'ledger.tamper' });
    return { ok: false, status: 'stopped', event: 'ledger.tamper', reason };
  }

  for (const check of extraChecks) {
    const result = await check();
    if (!result.ok) return { ok: false, status: 'open', reason: result.reason ?? 'check failed' };
  }

  /** @typedef {{ok: boolean, status: 'closed' | 'stopped' | 'open', reason?: string, event?: string}} CloseResult */
  return mutateRun(runId, id, writeRow, (current, block) => {
    if (current.orphans.length > 0) {
      /** @type {CloseResult} */
      const refused = { ok: false, status: 'open', reason: `orphans: ${current.orphans.join(', ')}` };
      return { result: refused, rows: [] };
    }
    block.status = 'closed';
    /** @type {CloseResult} */
    const closed = { ok: true, status: 'closed' };
    return { result: closed, rows: [{ event: 'block.close', block: id, status: 'complete' }] };
  });
}
