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

import { createHash, randomUUID } from 'node:crypto';
import { createReadStream, existsSync, lstatSync, readFileSync, readdirSync, readlinkSync, realpathSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { BLOCK_KINDS, blockKind, dispatchLevel } from '../decide/escalation.mjs';
import { exec } from '../util/exec.mjs';
import { DEFAULT_CONFIG_FILENAME } from '../config/load.mjs';
import { blockEndHook, configInForce, externalReviewer } from './config-snapshot.mjs';
import { ownedKinds, pathKind } from './owned-kinds.mjs';
import { isAncestorArgv, revParseArgv } from '../util/git.mjs';
import { StateError } from './paths.mjs';
import { assertOwned, blocksOverlap, isExactEntry, ownsFile, scopeGate } from './registry.mjs';
import { checkWorkerPin, readRun, saveRun, withRunLock, writeSigned } from './run.mjs';
import { canonicalJSON, verifyRow, loadKey } from './signer.mjs';
import * as reviewQueue from '../worker/queue.mjs';
import { repoRootOf } from '../worker/ticket.mjs';

/** @typedef {import('./run.mjs').WriteRow} WriteRow */

export const BRIEF_POINTER_MAX_BYTES = 200;

/** Ledger events the block gate requires to carry a valid MAC (§4.6 row 10). */
export const GATE_EVENTS = Object.freeze(['dispatch', 'block.attempt', 'block.rebase', 'block.claim', 'block.acceptance_amended', 'review.approved', 'proof']);

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
 * @returns {import('../gates/acceptance.mjs').AcceptanceClause[]}
 */
function assertAcceptance(clauses) {
  const ok =
    Array.isArray(clauses) &&
    clauses.length > 0 &&
    clauses.every(
      (c) => typeof c?.clause === 'string' && c.clause.length > 0 && (c.kind === 'human_check'
        ? !Object.hasOwn(c, 'tests')
        : Array.isArray(c.tests) && c.tests.length > 0 && c.tests.every((t) => typeof t === 'string' && t.length > 0)),
    );
  if (!ok) throw new StateError('bad-acceptance', 'acceptance: a non-empty list of {clause, tests: [test ids…]} or {clause, kind: human_check} without tests is required');
  return clauses;
}

/** Validate before the human question, and again under the run lock.
 * @param {Record<string, any>} record @param {string} id @param {unknown} clauses
 */
export function prepareAcceptanceAmendment(record, id, clauses) {
  const block = record.blocks?.[id];
  if (!block) throw new StateError('unknown_block', `block ${id} is not in run ${record.run_id}`);
  if (block.status !== 'open') throw new StateError('not-open', `not-open: block ${id} is not open`);
  const acceptance = assertAcceptance(clauses);
  const old_sha256 = sha256(canonicalJSON(block.acceptance));
  const new_sha256 = sha256(canonicalJSON(acceptance));
  if (old_sha256 === new_sha256) throw new StateError('acceptance-unchanged', 'acceptance-unchanged: the clause list is unchanged; nothing was recorded');
  return { acceptance, old_sha256, new_sha256, oldCount: block.acceptance.length };
}

/** @param {{runId: string, id: string, acceptance: unknown, reason: string, old_sha256: string, writeRow: WriteRow, before: (record: Record<string, any>) => void}} opts */
export async function amendAcceptanceBlock({ runId, id, acceptance, reason, old_sha256, writeRow, before }) {
  return mutateRun(runId, null, writeRow, (record) => {
    before(record);
    const prepared = prepareAcceptanceAmendment(record, id, acceptance);
    if (prepared.old_sha256 !== old_sha256) throw new StateError('acceptance-moved', `acceptance changed while confirming block ${id}; confirm again`);
    const block = record.blocks[id];
    block.acceptance = prepared.acceptance;
    const checks = prepared.acceptance.filter((c) => c.kind === 'human_check').map((c) => c.clause);
    if (checks.length) block.human_checks = checks;
    else delete block.human_checks;
    // Unique even when a later amendment restores an earlier list of clauses.
    block.acceptance_revision = randomUUID();
    const row = { event: 'block.acceptance_amended', block: id, old_sha256, new_sha256: prepared.new_sha256, clauses: prepared.acceptance.length, reason, by: 'human' };
    return { result: row, rows: [row] };
  });
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
 * B58: with `review.external.required` set in the run's snapshot, the project's `.code-forge.yml`
 * is the owner's switch past the local per-file review, so no block may own it. These are the
 * spellings an owned entry may match it by: relative to the workspace and, for a workspace in a
 * subdirectory, relative to the repository top level (the close gate matches both, `ownsAt`).
 * Empty when the key is not set. B64b: `hooks.on_block_end` in the snapshot guards it the same way
 * — the file names a command the orchestrator runs at every block end — and `why` names the key
 * that guards it (`review.external.required` first, so its message is unchanged). B87:
 * `proof.runners` in the snapshot guards it too — the file names the commands that prove a block;
 * any stored value guards it (fail closed: `validate` refuses an empty or malformed list anyway).
 * @param {Record<string, any>} record @returns {Promise<{paths: string[], why: string}>}
 * @throws {StateError} `config-snapshot`, `git-failed`
 */
async function guardedConfigPaths(record) {
  const runners = configInForce(record)?.proof?.runners;
  const why =
    externalReviewer(record) !== null
      ? 'review.external.required is true'
      : blockEndHook(record) !== null
        ? 'hooks.on_block_end is set'
        : runners !== undefined && runners !== null
          ? 'proof.runners is set'
          : null;
  if (why === null) return { paths: [], why: '' };
  const top = await repoTop(record.workspace);
  // the repo-top spelling `<prefix>/.code-forge.yml` may also match a different file of the same
  // name under the workspace (`<ws>/<prefix>/.code-forge.yml`): that over-match only refuses more,
  // never less, and is the one `ownsAt` gives the close gate
  const prefix = path.relative(top, realpathSync(record.workspace)).split(path.sep).join('/');
  return { paths: prefix === '' ? [DEFAULT_CONFIG_FILENAME] : [DEFAULT_CONFIG_FILENAME, `${prefix}/${DEFAULT_CONFIG_FILENAME}`], why };
}

/**
 * B58: refuse an owned entry that owns one of `guarded` (B57's rule, `ownsFile`: the exact path,
 * a directory above it, or a glob that matches it).
 * @param {ReadonlyArray<string>} owned @param {import('./registry.mjs').OwnedKinds} kinds @param {{paths: ReadonlyArray<string>, why: string}} guarded
 * @throws {StateError} `config-owned`
 */
function refuseConfigOwner(owned, kinds, guarded) {
  for (const entry of owned) {
    if (guarded.paths.some((file) => ownsFile([entry], file, kinds))) {
      throw new StateError('config-owned', `owned entry ${JSON.stringify(entry)} covers the project's ${DEFAULT_CONFIG_FILENAME}; no block may own it while ${guarded.why}`);
    }
  }
}

/**
 * @typedef {{path: string, branch: string, base_sha: string}} TreeRecord - a block entry's `worktree` (B62a).
 * @typedef {{worktree: TreeRecord, created: boolean, rollback: (() => Promise<string | null>) | null}} OpenTree - `rollback` resolves to a sentence naming leftovers, or null.
 */

/**
 * `block open` — refuses a second open of the same id and any owned set that overlaps another
 * ACTIVE block's; records base, owned files, level, attempt, acceptance, line forecast and the
 * block kind (B34: the declared `kind`, else `blockKind` of the owned files). A docs/contract
 * block asked for below `levels.coder_floor_docs` (default L1) is opened AT the floor: the entry
 * and the dispatch row carry the floor level, and the row adds `requested_level` and
 * `trigger: 'docs_floor'`. B52: the entry also records `tree_at_open` ({@link treeAtOpen}), the
 * tree's changes at open, which `block close` reads to tell the coder's changes from earlier ones.
 * B134: `base_branch` is the workspace's current branch, or null when detached.
 * B58: with `review.external.required` set, an owned entry that owns the project's config is
 * refused (`config-owned`, {@link refuseConfigOwner}).
 * B59: `decision` (the rules lane of a brief-mode block) is written as a signed `decision` row
 * naming the block, with `decisionId` (default a fresh UUID) as its `decision_id`, just before the
 * `dispatch` row — under the same lock, so a refused open writes neither. Its `answer` is the
 * requested lane and must equal `level` (else `bad-decision`, nothing written); the docs floor may
 * still raise the dispatched level, and the dispatch row then names the lane as `requested_level`.
 * B60: `tests` (`block open --tests <paths…>`, the same path rules as owned entries, else
 * `bad-tests`) is recorded as the entry's `test_files` and on the dispatch row; a gate argv item
 * `{test_files}` expands to them (`gates/expand.mjs`). Left out, neither carries the key.
 * B148: `after` lists dependencies in this run. Under the same run lock, every dependency must
 * be closed (`after-not-done`); self-dependency is `bad-after`. Unique ids are recorded on the
 * entry and dispatch only when supplied, and a later attempt checks only its own supplied list.
 * B153: `after_outcome` carries the specialist step ids and outcome byte hashes accepted by
 * the CLI against the run's project root; it is recorded on the entry and dispatch when supplied.
 * B62a: `worktree` (given by `block open` under `proof.isolation: worktree`, `proof/worktree.mjs`;
 * this module never imports it) plans the block's own worktree, or the reuse of the one an earlier
 * entry of the same id recorded (`previous`); its `make()` makes or reuses it. Both are called
 * under the lock before anything is recorded: the plan before the overlap check (which lets this
 * block share owned paths with another open block on a different tree, `blocksOverlap`), `make()`
 * after every other check. A refusal there writes nothing; if the rows or the save fail after
 * `make()` created a tree, its `rollback()` removes it again, and the original error is thrown
 * with a sentence added naming anything the rollback could not remove. The entry stores the tree's
 * `{path, branch, base_sha}` as `worktree`; a tree it made is a signed `worktree.created
 * {block, path, branch, base}` row just before `dispatch`. On a re-open the block's base is the
 * tree's `base_sha`, and a `base` that resolves to another commit is refused (`worktree-base`).
 * @param {{
 *   runId: string, id: string, level: string, owned: string[], acceptance: unknown,
 *   attempt?: number, base?: string, lines?: number, brief?: {path: string, content: Buffer | string},
 *   kind?: string, cfg?: Record<string, any>, writeRow: WriteRow, now?: Date, tests?: string[], after?: string[],
 *   after_outcome?: {step: string, sha256: string}[],
 *   decision?: {question: 'lane', answer: string, source: 'rules', facts: Record<string, unknown>}, decisionId?: string,
 *   worktree?: (ctx: {baseSha: string, previous: TreeRecord | null}) => Promise<{path: string, make: () => Promise<OpenTree>}>,
 * }} opts
 * @returns {Promise<{block: Record<string, any>, pointer: string | null, worktreeReused: boolean}>} `worktreeReused`:
 *   the block took an earlier attempt's worktree as it was.
 */
export async function openBlock(opts) {
  const { runId, id, level, attempt = 1, lines = null, writeRow, now = new Date() } = opts;
  if (typeof id !== 'string' || !BLOCK_ID.test(id)) throw new StateError('bad-block-id', `block id must match ${BLOCK_ID}`);
  if (typeof level !== 'string' || !LEVEL.test(level)) throw new StateError('bad-level', 'level must be L0..L3');
  if (!Number.isInteger(attempt) || attempt < 1) throw new StateError('bad-attempt', 'attempt must be an integer >= 1');
  if (lines !== null && !(Number.isInteger(lines) && lines > 0)) throw new StateError('bad-lines', 'lines forecast must be a positive integer');
  if (opts.kind !== undefined && opts.kind !== 'light' && !BLOCK_KINDS.includes(opts.kind)) throw new StateError('bad-kind', `kind must be one of ${[...BLOCK_KINDS, 'light'].join(', ')}`);
  if (opts.decision && opts.decision.answer !== level) throw new StateError('bad-decision', `the decision answer ${opts.decision.answer} is not the requested level ${level}`);
  const owned = assertOwned(opts.owned);
  const acceptance = assertAcceptance(opts.acceptance);
  const humanChecks = acceptance.filter((c) => c.kind === 'human_check').map((c) => c.clause);
  const tests = opts.tests === undefined ? null : [...assertOwned(opts.tests, { label: 'test files', code: 'bad-tests' })];
  const after = opts.after === undefined ? null : [...new Set(opts.after)];
  const kind = opts.kind === 'light' ? 'light' : blockKind({ owned, declared: opts.kind });
  const dispatched = dispatchLevel({ lane: level, kind, cfg: opts.cfg });
  const pointer = opts.brief ? briefPointer(opts.brief.path, opts.brief.content) : null;

  // B52: the tree is stamped BEFORE the run lock (hashing can be slow; other block commands must
  // not wait on it); under the lock the base is resolved again and must be the one stamped against
  const before = await readRun(runId);
  const { workspace } = before;
  // B62a: a re-open of a block with a worktree keeps the tree's base — the block's base is the
  // commit its tree started from, and a different --base is refused before anything is touched
  /** @param {Record<string, any>} record @returns {{path: string, branch: string, base_sha: string} | null} */
  const previousTree = (record) => (opts.worktree ? record.blocks[id]?.worktree ?? null : null);
  const previous = previousTree(before);
  const baseRef = previous ? previous.base_sha : opts.base ?? 'HEAD';
  const stampedBase = await resolveCommit(baseRef, workspace);
  if (previous && opts.base !== undefined && (await resolveCommit(opts.base, workspace)) !== previous.base_sha) {
    throw new StateError('worktree-base', `block ${id}: --base ${opts.base} is not ${previous.base_sha.slice(0, 12)}, the base of the block's worktree ${previous.path}; a re-open keeps the worktree and its base — drop --base; nothing was recorded`);
  }
  const treeOpen = await treeAtOpen(workspace, stampedBase);
  // B58: `review.external` cannot change mid-run, so the record read here decides
  const guarded = await guardedConfigPaths(before);

  /** @type {OpenTree | null} */
  let made = null;
  try {
    const { block, reused } = await mutateRun(runId, null, writeRow, async (record) => {
      if (record.blocks[id]?.status === 'open') throw new StateError('already-open', `block ${id} is already open`);
      if (record.blocks[id]?.status === 'awaiting_human_check') throw new StateError('already-open', `block ${id} is awaiting a human check`);
      if (after !== null) {
        if (after.includes(id)) throw new StateError('bad-after', `block ${id} cannot name itself in --after`);
        const blocking = after.filter((dependency) => record.blocks[dependency]?.status !== 'closed');
        if (blocking.length > 0) {
          const statuses = blocking.map((dependency) => `${dependency} (${record.blocks[dependency]?.status ?? 'not in run'})`);
          throw new StateError('after-not-done', `block ${id}: --after dependencies not closed: ${statuses.join(', ')}`);
        }
      }
      // B57: what each exact owned entry is on disk, under the lock (as `block claim`) — a regular
      // file owns only itself, here and in every later check
      const kinds = ownedKinds(record.workspace, owned);
      refuseConfigOwner(owned, kinds, guarded);
      const prior = previousTree(record);
      const baseSha = await resolveCommit(prior ? prior.base_sha : opts.base ?? 'HEAD', record.workspace);
      if (baseSha !== stampedBase || record.workspace !== workspace) throw new StateError('base-moved', `the base moved while block ${id} was being opened; run block open again`);
      const branch = await git(['git', 'symbolic-ref', '--quiet', '--short', 'HEAD'], record.workspace, [0, 1]);
      const baseBranch = branch.code === 0 ? branch.stdout.trim() : null;
      // B62a: the tree is planned (read-only checks) before the overlap check, which needs its path
      const planned = opts.worktree ? await opts.worktree({ baseSha, previous: prior }) : null;
      const self = { owned_files: owned, owned_kinds: kinds, worktree: planned ? { path: planned.path } : null };
      for (const [otherId, other] of Object.entries(activeBlocks(record))) {
        const pair = blocksOverlap(self, other);
        if (pair) throw new StateError('overlap', `block ${id} owns ${pair[0]}, which overlaps ${pair[1]} owned by open block ${otherId}`);
      }
      // B62a: the worktree is made (or an earlier attempt's reused) last, once every other check
      // passed; a tree made here is rolled back below if the rows or the save then fail
      made = planned ? await planned.make() : null;
      const tree = made;
      const outcomeDependencies = opts.after_outcome === undefined ? {} : { after_outcome: opts.after_outcome };
      const entry = { block: id, base_sha: baseSha, base_branch: baseBranch, owned_files: [...owned], owned_kinds: { ...kinds }, level: dispatched.level, kind, attempt, opened_at: now.toISOString(), acceptance, lines_forecast: lines, status: 'open', tree_at_open: treeOpen, ...(tests ? { test_files: tests } : {}), ...(after !== null ? { after } : {}), ...outcomeDependencies, ...(tree ? { worktree: tree.worktree } : {}) };
      if (humanChecks.length > 0) Object.assign(entry, { human_checks: humanChecks });
      record.blocks[id] = entry;
      const floor = dispatched.trigger === 'docs_floor' ? { requested_level: level, trigger: 'docs_floor' } : {};
      const created = tree?.created ? [{ event: 'worktree.created', block: id, path: tree.worktree.path, branch: tree.worktree.branch, base: tree.worktree.base_sha }] : [];
      return {
        result: { block: entry, reused: tree !== null && !tree.created },
        rows: [
          ...(opts.decision ? [{ ...opts.decision, event: 'decision', decision_id: opts.decisionId ?? randomUUID(), block: id }] : []),
          ...created,
          { event: 'dispatch', block: id, level: dispatched.level, kind, ...floor, attempt, base_sha: baseSha, base_branch: baseBranch, owned_files: entry.owned_files, lines_forecast: lines, ...(tests ? { test_files: tests } : {}), ...(after !== null ? { after } : {}), ...outcomeDependencies },
        ],
      };
    });
    return { block, pointer, worktreeReused: reused };
  } catch (err) {
    // B62a: nothing was recorded, so a tree this open made must not outlive it
    if (made?.created && made.rollback) {
      // the original error is thrown either way; what could not be removed is named in it (the
      // rollback checks each git result and resolves, it does not throw)
      const left = await made.rollback();
      if (left !== null) {
        if (!(err instanceof Error)) throw new Error(`${String(err).replace(/\.?$/, '.')} ${left}`, { cause: err });
        err.message = `${err.message.replace(/\.?$/, '.')} ${left}`;
      }
    }
    throw err;
  }
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
 * below it). B62a: refused for an open block with its own worktree (`worktree-rebase`).
 * B134: a closed worktree block instead rebases its branch onto its recorded base branch,
 * retaining every changed owned file's blob; a conflict or changed blob restores the old head.
 * @param {{runId: string, id: string, head?: string, writeRow: WriteRow}} opts
 * @returns {Promise<{old_base: string, new_base: string}>}
 */
export async function rebaseBlock({ runId, id, head = 'HEAD', writeRow }) {
  if (['closed', 'awaiting_human_check'].includes((await readRun(runId)).blocks[id]?.status)) return rebaseClosedBlock({ runId, id, writeRow });
  return mutateRun(runId, id, writeRow, async (record, block) => {
    // B62a: a worktree block's base is the commit its tree started from; moving it would leave the
    // tree on its old base, so the rebase is refused
    if (block.worktree) throw new StateError('worktree-rebase', `block ${id} has its own worktree ${block.worktree.path} on branch ${block.worktree.branch}; its base is the base of that tree, so block rebase does not apply — work on the branch in the tree instead`);
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
 * Hash committed paths, with null for a deletion. NUL-delimited tree entries preserve unusual
 * file names; filter exact names because git's path selection may also return descendants.
 * @param {string} cwd @param {string} ref @param {string[]} files
 * @returns {Promise<Map<string, string | null>>}
 */
async function committedBlobs(cwd, ref, files) {
  const hashes = new Map(files.map((file) => [file, /** @type {string | null} */ (null)]));
  if (files.length === 0) return hashes;
  const tree = await git(['git', 'ls-tree', '-r', '-z', '--full-tree', ref, '--', ...files], cwd);
  for (const entry of tree.stdout.split('\0').filter(Boolean)) {
    const tab = entry.indexOf('\t');
    const file = entry.slice(tab + 1);
    if (hashes.has(file)) hashes.set(file, entry.slice(0, tab).split(' ')[2]);
  }
  return hashes;
}

/**
 * Closed blocks have a separate mutation path: check their status again under the lock,
 * restore the branch on failure (including a failed signed write), and retain closed/awaiting status.
 * @param {{runId: string, id: string, writeRow: WriteRow}} opts
 * @returns {Promise<{old_base: string, new_base: string}>}
 */
async function rebaseClosedBlock({ runId, id, writeRow }) {
  return withRunLock(runId, async () => {
    const record = await readRun(runId);
    if (record.status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const block = record.blocks[id];
    if (!['closed', 'awaiting_human_check'].includes(block?.status) || !block.worktree) throw new StateError('no-block', `block ${id} is not open in run ${runId}`);
    if (!block.base_branch) throw new StateError('rebase-no-base', `block ${id} has no recorded base branch`);
    const tree = block.worktree.path;
    const branch = await git(['git', 'symbolic-ref', '--quiet', 'HEAD'], tree, [0, 1]);
    if (branch.stdout.trim() !== `refs/heads/${block.worktree.branch}`) throw new StateError('worktree-rebase', `block ${id}: its worktree is not on branch ${block.worktree.branch}`);
    // A reset must never discard edits made after close, or abort an earlier rebase.
    const status = await git(['git', 'status', '--porcelain'], tree);
    if (status.stdout.length > 0) throw new StateError('rebase-conflict', 'rebase-conflict');
    const oldHead = await resolveCommit('HEAD', tree);
    const newBase = await resolveCommit(`refs/heads/${block.base_branch}`, record.workspace);
    const top = await repoTop(record.workspace);
    const prefix = path.relative(top, realpathSync(record.workspace)).split(path.sep).join('/');
    const diff = await git(['git', 'diff', '--no-renames', '--name-only', '-z', block.base_sha, oldHead, '--'], tree);
    const files = diff.stdout.split('\0').filter((file) => file.length > 0 && (
      ownsFile(block.owned_files, file, block.owned_kinds) ||
      (prefix !== '' && file.startsWith(`${prefix}/`) && ownsFile(block.owned_files, file.slice(prefix.length + 1), block.owned_kinds))
    )).sort();
    const before = await committedBlobs(tree, oldHead, files);
    const moved = { old_base: block.base_sha, new_base: newBase };
    try {
      const rebased = await exec(['git', '-C', tree, 'rebase', newBase], { cwd: record.workspace, env: gitEnv(), timeoutMs: 30000 });
      if (rebased.result !== 'ok') throw new StateError('rebase-conflict', 'rebase-conflict');
      const after = await committedBlobs(tree, 'HEAD', files);
      const changed = files.filter((file) => before.get(file) !== after.get(file));
      if (changed.length > 0) throw new StateError('rebase-changed-reviewed', `rebase-changed-reviewed ${changed.join(', ')}`);
      block.base_sha = newBase;
      block.worktree.base_sha = newBase;
      await writeSigned(runId, writeRow, { event: 'block.rebase', block: id, ...moved, closed: true });
      await saveRun(record);
      return moved;
    } catch (err) {
      // --abort restores a conflicted rebase; after a completed rebase there is nothing to
      // abort, and reset restores the old branch head. The tree was clean before either.
      await exec(['git', '-C', tree, 'rebase', '--abort'], { cwd: record.workspace, env: gitEnv(), timeoutMs: 30000 });
      await git(['git', 'reset', '--hard', oldHead], tree);
      throw err;
    }
  });
}

/**
 * `block claim` — assign one EXACT path (an orphan, or any unowned path) to `id`; refused for a
 * glob, and when another open block owns it — or anything it would own: the claimed path becomes an
 * owned entry, so it is checked with `blocksOverlap` (B57: an entry owns the paths below it too,
 * unless it is a regular file now — its kind is recorded in `owned_kinds` as at `block open`).
 * B58: with `review.external.required` set, a path that owns the project's config is refused
 * (`config-owned`), as at `block open`. B62a: two blocks that each have their own worktree may
 * own the same path (`blocksOverlap`).
 * @param {{runId: string, id: string, file: string, writeRow: WriteRow}} opts
 */
export async function claimPath({ runId, id, file, writeRow }) {
  assertOwned([file]);
  if (!isExactEntry(file)) throw new StateError('bad-owned', `block claim takes one exact path, not a glob (${JSON.stringify(file)})`);
  const guarded = await guardedConfigPaths(await readRun(runId));
  await mutateRun(runId, id, writeRow, (record, block) => {
    const kinds = { [file]: pathKind(record.workspace, file) };
    refuseConfigOwner([file], kinds, guarded);
    for (const [otherId, other] of Object.entries(activeBlocks(record))) {
      const pair = otherId === id ? null : blocksOverlap({ owned_files: [file], owned_kinds: kinds, worktree: block.worktree ?? null }, other);
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
  await mutateRun(runId, null, writeRow, (record) => {
    const block = record.blocks[id];
    if (!['open', 'awaiting_human_check'].includes(block?.status)) throw new StateError('no-block', `block ${id} is not open in run ${runId}`);
    block.status = 'stopped';
    block.stop_reason = reason;
    return { result: undefined, rows: [{ event, block: id, reason }] };
  });
}

/**
 * Called only after the CLI's terminal confirmation; recheck under the lock before signing.
 * @param {{runId: string, id: string, note: string, proof?: string | null, writeRow: WriteRow, now?: Date}} opts
 */
export async function humanPassBlock({ runId, id, note, proof = null, writeRow, now = new Date() }) {
  return mutateRun(runId, null, writeRow, (record) => {
    const block = record.blocks[id];
    if (block?.status !== 'awaiting_human_check') throw new StateError('not-awaiting', `block ${id} is not awaiting a human check`);
    if (typeof note !== 'string' || note.trim().length === 0) throw new StateError('usage', 'block human-pass needs --note <text>');
    if (proof !== null && (typeof proof !== 'string' || !existsSync(proof))) throw new StateError('usage', '--proof must name an existing directory or file');
    const grant = record.autopilot;
    if (grant && !['stopped', 'expired'].includes(grant.status) && !(now.getTime() >= Date.parse(grant.until))) {
      throw new StateError('autopilot-active', `block human-pass is refused while an autopilot grant is active on run ${runId}`);
    }
    const row = { event: 'block.human_pass', block: id, note, proof, checks: [...block.human_checks], by: 'human' };
    block.status = 'closed';
    return { result: row, rows: [row] };
  });
}

/**
 * B8's rows of the block gate, in order: worker pin ⇒ `worker.replaced`/`worker.down` (block
 * stopped); the block's signed `dispatch` row must exist and verify, and every other
 * gate-relevant row of the block must verify ⇒ else `ledger.tamper` (block stopped); then
 * `extraChecks`; finally, UNDER the run lock, orphans in the registry ⇒ refused (block stays
 * open), else closed. B58: `closeRows` are signed and written under that same lock just before
 * `block.close`, and only when the block closes — a row that records why it may close is never
 * left behind by a refused close.
 * @param {{
 *   runId: string, id: string, rows: Record<string, any>[], writeRow: WriteRow,
 *   livePid?: number, probe?: import('./run.mjs').StartTimeProbe,
 *   extraChecks?: Array<() => Promise<{ok: boolean, reason?: string}>>,
 *   closeRows?: Record<string, any>[],
 *   dryRun?: boolean,
 * }} opts
 * @returns {Promise<{ok: boolean, status: 'closed' | 'awaiting_human_check' | 'stopped' | 'open', reason?: string, event?: string}>}
 */
export async function closeBlock({ runId, id, rows, writeRow, livePid, probe, extraChecks = [], closeRows = [], dryRun = false }) {
  const record = await readRun(runId);
  if (record.blocks[id]?.status !== 'open') throw new StateError('no-block', `block ${id} is not open in run ${runId}`);

  let pin = await checkWorkerPin(record, { livePid, probe });
  const pins = record.workers ?? (record.worker ? [record.worker] : []);
  const counted = rows.filter((row) => row.block === id && (row.run === undefined || row.run === runId) && row.event === 'review.result' && !['stale', 'unavailable', 'answers'].includes(row.status) && !(row.status === 'refused' && row.reason === 'other-run'));
  // Ledgers written before B107 omit worker identity and use the run's pin check above.
  if (counted.some((row) => row.worker !== undefined && !pins.some((p) => p.pid === row.worker?.pid && p.started_at === row.worker?.started_at))) {
    pin = { ok: false, event: 'worker.replaced', reason: 'a counted review result does not name a pinned worker' };
  }
  if (pin.ok) {
    const repoRoot = await repoRootOf(record.workspace);
    let names;
    try { names = readdirSync(reviewQueue.queueDir(repoRoot)); }
    catch (err) { if (err.code !== 'ENOENT') throw err; names = []; }
    for (const name of names.filter((n) => /^[0-9a-f]{24}\.json$/.test(n))) {
      const ticket = name.slice(0, 24);
      const queued = reviewQueue.readTicket(repoRoot, ticket);
      if (queued.run !== runId || queued.block !== id) continue;
      const claimKey = `ticket\0${runId}\0${ticket}`;
      const file = path.join(os.homedir(), '.code-forge', 'locks', `${createHash('sha256').update(claimKey).digest('hex')}.lock`);
      let claim;
      try { claim = JSON.parse(readFileSync(file, 'utf8')); }
      catch (err) { if (err.code === 'ENOENT') continue; throw err; }
      const owner = pins.find((p) => p.pid === claim.pid && p.started_at === claim.start);
      if (owner && !(await checkWorkerPin({ worker: owner }, { probe })).ok) {
        pin = { ok: false, event: 'worker.down', reason: `dead pinned worker pid ${owner.pid} holds ticket ${ticket}` };
        break;
      }
    }
  }
  if (!pin.ok) {
    if (!dryRun) await stopBlock({ runId, id, reason: pin.reason, writeRow, event: pin.event });
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
    if (!dryRun) await stopBlock({ runId, id, reason, writeRow, event: 'ledger.tamper' });
    return { ok: false, status: 'stopped', event: 'ledger.tamper', reason };
  }

  const failures = [];
  for (const check of extraChecks) {
    const result = await check();
    if (!result.ok) {
      if (!dryRun) return { ok: false, status: 'open', reason: result.reason ?? 'check failed' };
      failures.push(result.reason ?? 'check failed');
    }
  }
  if (dryRun) {
    if (record.orphans.length > 0) failures.push(`orphans: ${record.orphans.join(', ')}`);
    return { ok: failures.length === 0, status: 'open', ...(failures.length ? { reason: failures.join('; ') } : {}) };
  }

  /** @typedef {{ok: boolean, status: 'closed' | 'awaiting_human_check' | 'stopped' | 'open', reason?: string, event?: string}} CloseResult */
  return mutateRun(runId, id, writeRow, (current, block) => {
    if (current.orphans.length > 0) {
      /** @type {CloseResult} */
      const refused = { ok: false, status: 'open', reason: `orphans: ${current.orphans.join(', ')}` };
      return { result: refused, rows: [] };
    }
    const checks = block.human_checks ?? [];
    block.status = checks.length > 0 ? 'awaiting_human_check' : 'closed';
    /** @type {CloseResult} */
    const closed = { ok: true, status: block.status };
    return { result: closed, rows: [...closeRows, { event: 'block.close', block: id, status: 'complete' },
      ...(checks.length > 0 ? [{ event: 'block.awaiting_human_check', block: id, checks }] : [])] };
  });
}
