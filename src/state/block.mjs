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
import { createReadStream, existsSync, lstatSync, readlinkSync, realpathSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { BLOCK_KINDS, blockKind, dispatchLevel } from '../decide/escalation.mjs';
import { exec } from '../util/exec.mjs';
import { ownedKinds, pathKind } from './owned-kinds.mjs';
import { isAncestorArgv, revParseArgv } from '../util/git.mjs';
import { StateError } from './paths.mjs';
import { assertOwned, findOverlap, isExactEntry, ownsFile, scopeGate } from './registry.mjs';
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

/** B52: the stamp of a path whose state could not be read; never equal to anything (see {@link sameStamp}). */
export const UNREADABLE = 'unreadable';

/**
 * B52: a stamp of one path's current state, never following a symlink and never throwing:
 *  - `deleted` — nothing at the path;
 *  - `file:<x|->:<sha256 of the bytes>` — a regular file, with its executable bit (hash streamed);
 *  - `link:<sha256 of the target text>` — a symlink, by where it points;
 *  - `repo:<HEAD commit>:<sha256 of its changes>` — a directory holding a git repository (a
 *    checked-out submodule, a nested repo): its checked-out commit, and the status, the binary
 *    diff against HEAD and every untracked file's content (`repoStamp`) — so a repo already dirty
 *    at open and edited again later stamps differently;
 *  - `gitlink:<commit>` — a directory with no `.git` that the index records as a gitlink (an
 *    uninitialised submodule);
 *  - {@link UNREADABLE} — anything else (a plain directory, a FIFO, a read or git failure).
 * Two stamps are the same only when the path did not change between them, and an `unreadable`
 * stamp never matches — a read failure must never make a file look unchanged.
 * @param {string} top - the repository top level (`repoTop`). @param {string} file - top-relative.
 * @returns {Promise<string>}
 */
export async function treeStamp(top, file) {
  const full = path.join(top, file);
  let stat;
  try {
    stat = lstatSync(full);
  } catch (err) {
    return /** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT' ? 'deleted' : UNREADABLE;
  }
  try {
    if (stat.isSymbolicLink()) return `link:${sha256(readlinkSync(full))}`;
    if (stat.isFile()) return `file:${(stat.mode & 0o111) !== 0 ? 'x' : '-'}:${await fileSha256(full)}`;
    if (stat.isDirectory() && existsSync(path.join(full, '.git'))) return await repoStamp(full);
    if (stat.isDirectory()) return await gitlinkStamp(top, file);
  } catch {
    return UNREADABLE;
  }
  return UNREADABLE;
}

/** @param {Buffer | string} data @returns {string} */
const sha256 = (data) => createHash('sha256').update(data).digest('hex');

/**
 * B52: a file's sha256, streamed (a large file is never read whole into memory).
 * @param {string} full @returns {Promise<string>}
 */
async function fileSha256(full) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(full)) hash.update(/** @type {Buffer} */ (chunk));
  return hash.digest('hex');
}

/**
 * B52: a nested repository's stamp — its HEAD, and a sha256 over its own changes: `git status
 * --porcelain=v1 -z`, `git diff HEAD --binary` (the content of every tracked change) and each
 * untracked file's path and sha256. Any git failure is {@link UNREADABLE}.
 * @param {string} dir @returns {Promise<string>}
 */
async function repoStamp(dir) {
  const run = (/** @type {string[]} */ argv) => exec(argv, { cwd: dir, env: gitEnv(), timeoutMs: 30000 });
  const head = await run(['git', 'rev-parse', '--verify', 'HEAD']);
  const status = await run(['git', 'status', '--porcelain=v1', '-z', '--untracked-files=all']);
  const diff = await run(['git', 'diff', 'HEAD', '--binary']);
  const untracked = await run(['git', 'ls-files', '-z', '--others', '--exclude-standard']);
  if (head.result !== 'ok' || status.result !== 'ok' || diff.result !== 'ok' || untracked.result !== 'ok') return UNREADABLE;
  const hash = createHash('sha256').update(status.stdout).update('\0diff\0').update(diff.stdout);
  for (const rel of untracked.stdout.split('\0').filter((f) => f.length > 0).sort()) {
    const inner = path.join(dir, rel);
    const st = lstatSync(inner);
    hash.update(`\0${rel}\0`).update(st.isFile() ? await fileSha256(inner) : st.isSymbolicLink() ? `link:${sha256(readlinkSync(inner))}` : 'other');
  }
  return `repo:${head.stdout.trim()}:${hash.digest('hex')}`;
}

/**
 * B52: a directory with no `.git` of its own that the index records as a gitlink (an uninitialised
 * submodule): `gitlink:<commit>` from `git ls-files -s`; anything else is {@link UNREADABLE}.
 * @param {string} top @param {string} file @returns {Promise<string>}
 */
async function gitlinkStamp(top, file) {
  const res = await exec(['git', 'ls-files', '-s', '-z', '--', file], { cwd: top, env: gitEnv(), timeoutMs: 30000 });
  if (res.result !== 'ok') return UNREADABLE;
  const entry = res.stdout.split('\0').find((e) => e.endsWith(`\t${file}`));
  const m = entry ? /^160000 ([0-9a-f]{40,64}) \d+\t/.exec(entry) : null;
  return m ? `gitlink:${m[1]}` : UNREADABLE;
}

/**
 * B52: whether two stamps say the path did not change: equal, and neither {@link UNREADABLE}.
 * @param {unknown} a @param {unknown} b @returns {boolean}
 */
export const sameStamp = (a, b) => typeof a === 'string' && a === b && a !== UNREADABLE;

/**
 * B52: the repository's top level (`git rev-parse --show-toplevel`, realpath'd) — the root both
 * git listings run from and every stamp is taken against, so a workspace in a subdirectory of
 * the repository lines up with the repo-root-relative paths git prints.
 * @param {string} cwd @returns {Promise<string>}
 * @throws {StateError} `git-failed`
 */
export async function repoTop(cwd) {
  const res = await git(['git', 'rev-parse', '--show-toplevel'], cwd);
  return realpathSync(res.stdout.trim());
}

/**
 * B52: the tree's changes when a block opens — every file changed since `base` (deletions
 * included) and every untracked, not ignored file, listed from the repository top level, each
 * with its {@link treeStamp}. Recorded in the block's entry (`tree_at_open`, top-relative paths):
 * at `block close`, a file outside the block's `owned_files` whose stamp is still the same
 * ({@link sameStamp}) is not the coder's change (plan files, `init`'s config, a skill link…); one
 * that changed since, or could not be read either time, is `unowned_change` (`review/gate-check.mjs`).
 * @param {string} cwd @param {string} base - a full commit sha.
 * @returns {Promise<Record<string, string>>}
 * @throws {StateError} `git-failed`
 */
export async function treeAtOpen(cwd, base) {
  const top = await repoTop(cwd);
  const changed = await git(['git', 'diff', '--name-only', '-z', '--no-renames', base, '--'], top);
  const untracked = await git(['git', 'ls-files', '-z', '--others', '--exclude-standard'], top);
  const files = [...new Set([...changed.stdout.split('\0'), ...untracked.stdout.split('\0')].filter((f) => f.length > 0))].sort();
  /** @type {Record<string, string>} */
  const out = {};
  for (const file of files) out[file] = await treeStamp(top, file);
  return out;
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
 * `trigger: 'docs_floor'`. B52: the entry also records `tree_at_open` ({@link treeAtOpen}), the
 * tree's changes at open, which `block close` reads to tell the coder's changes from earlier ones.
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

  // B52: the tree is stamped BEFORE the run lock (hashing can be slow; other block commands must
  // not wait on it); under the lock the base is resolved again and must be the one stamped against
  const { workspace } = await readRun(runId);
  const stampedBase = await resolveCommit(opts.base ?? 'HEAD', workspace);
  const treeOpen = await treeAtOpen(workspace, stampedBase);

  const block = await mutateRun(runId, null, writeRow, async (record) => {
    if (record.blocks[id]?.status === 'open') throw new StateError('already-open', `block ${id} is already open`);
    // B57: what each exact owned entry is on disk, under the lock (as `block claim`) — a regular
    // file owns only itself, here and in every later check
    const kinds = ownedKinds(record.workspace, owned);
    for (const [otherId, other] of Object.entries(activeBlocks(record))) {
      const pair = findOverlap(owned, other.owned_files, { kindsA: kinds, kindsB: other.owned_kinds });
      if (pair) throw new StateError('overlap', `block ${id} owns ${pair[0]}, which overlaps ${pair[1]} owned by open block ${otherId}`);
    }
    const baseSha = await resolveCommit(opts.base ?? 'HEAD', record.workspace);
    if (baseSha !== stampedBase || record.workspace !== workspace) throw new StateError('base-moved', `the base moved while block ${id} was being opened; run block open again`);
    const entry = { block: id, base_sha: baseSha, owned_files: [...owned], owned_kinds: { ...kinds }, level: dispatched.level, kind, attempt, opened_at: now.toISOString(), acceptance, lines_forecast: lines, status: 'open', tree_at_open: treeOpen };
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
 * is a conflict too. Never silent. Owned means `ownsFile` (B57: a directory entry owns the files
 * below it).
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
    const conflicts = diff.stdout.split('\n').filter((file) => file.length > 0 && ownsFile(block.owned_files, file, block.owned_kinds));
    if (conflicts.length > 0) throw new StateError('owned-conflict', `block ${id}: commits since its base changed owned files: ${conflicts.join(', ')}`);
    const moved = { old_base: block.base_sha, new_base: newBase };
    block.base_sha = newBase;
    return { result: moved, rows: [{ event: 'block.rebase', block: id, ...moved }] };
  });
}

/**
 * `block claim` — assign one EXACT path (an orphan, or any unowned path) to `id`; refused for a
 * glob, and when another open block owns it — or anything it would own: the claimed path becomes an
 * owned entry, so it is checked with `findOverlap` (B57: an entry owns the paths below it too,
 * unless it is a regular file now — its kind is recorded in `owned_kinds` as at `block open`).
 * @param {{runId: string, id: string, file: string, writeRow: WriteRow}} opts
 */
export async function claimPath({ runId, id, file, writeRow }) {
  assertOwned([file]);
  if (!isExactEntry(file)) throw new StateError('bad-owned', `block claim takes one exact path, not a glob (${JSON.stringify(file)})`);
  await mutateRun(runId, id, writeRow, (record, block) => {
    const kinds = { [file]: pathKind(record.workspace, file) };
    for (const [otherId, other] of Object.entries(activeBlocks(record))) {
      const pair = otherId === id ? null : findOverlap([file], other.owned_files, { kindsA: kinds, kindsB: other.owned_kinds });
      if (pair) throw new StateError('overlap', `${file} overlaps ${pair[1]} owned by open block ${otherId}`);
    }
    if (!block.owned_files.includes(file)) {
      block.owned_files.push(file);
      block.owned_kinds = { ...block.owned_kinds, ...kinds };
    }
    record.orphans = record.orphans.filter((/** @type {string} */ o) => !ownsFile([file], o, kinds));
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
