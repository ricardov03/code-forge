/**
 * The fix-loop state per `(run, block, file)` (plan §4.11; block B12c).
 *
 * B12b's `fixloop.mjs` is a state machine whose `FileState` (round, level, open findings,
 * `reviewed_content`, …) must survive between two `review-file` calls on the same file: round 1
 * is the first ticket, round 2 (the fix-hunk recheck) the next one, and so on. B69: the worker
 * keeps it in the run's own directory under the code-forge home, OUTSIDE the workspace and outside
 * the temp root (a temp sweep or a TMPDIR cleanup no longer loses it):
 *
 *   <code-forge home>/runs/<run>/review-state/<block>/<sha256(file)[0:24]>.json   ({@link statePath})
 *
 * The old place, `<run temp root>/review-state/<block>/<name>.json` ({@link legacyStatePath}), is
 * read once: a file missing at the new path but present at the old one that passes the same
 * checks is moved (new written atomically 0600, old removed); a wrong one there is `tampered` and
 * left where it is.
 *
 * Every state file is SIGNED with the run key (`{run, block, file, seq, state}` + `mac`) AND
 * anchored in the run's append-only ledger: each save first writes a signed row
 * `{event: 'review.state', block, file, seq, state_sha, l3_rung_used}`. A load matches the file
 * against the LATEST verified anchor (highest `seq`) for the file: an older signed file
 * (rollback), a different sha, or a file with no anchor is `tampered` — the hook refuses the
 * ticket, never a fresh round 1 that could clear a `review_cap` stop or an open finding. B68: the
 * anchor also carries `state_body`, the signed state with `reviewed_content` set to null and its
 * sha carried as `reviewed_content_sha` (or `state_omitted: "too_large"` over `STATE_BODY_CAP`), so a MISSING file is `restored` from it
 * (the engine saves it as a new seq and writes `review.state_restored`); an anchor with no body
 * makes a missing file `lost` (refused, never a round 1). A
 * `review.state` row of the run whose MAC does not verify is `tampered` for every block and file
 * of it (`blockAnchors`). Whether the block's one L3 rung was used comes from the anchors, never
 * from the state files.
 *
 * B78: the reviewed text itself is kept as a git blob in the content root's object store
 * (`git hash-object -w --no-filters --stdin`), its id in the signed body as `reviewed_content_blob`.
 * A restore reads it back (`git cat-file blob`) and uses it only when its sha256 equals
 * `reviewed_content_sha`; a missing, pruned or mismatching blob falls back to the B68 rule. A blob
 * write that fails saves `reviewed_content_blob: null` and never fails the round.
 */

import { spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { existsSync, linkSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { exec } from '../util/exec.mjs';
import { assertRunId, runsDir } from '../state/paths.mjs';
import { signRow, verifyRow } from '../state/signer.mjs';
// B78: every blob `git` call runs with `gitChildEnv()`: inherited GIT_* removed, no system config,
// global config = /dev/null
import { assertBlockId, assertRowPath, gitChildEnv } from './ticket.mjs';

/** @typedef {import('../review/fixloop.mjs').FileState} FileState */
/** @typedef {{runRootDir: string, runId: string, block: string, file: string, key: Buffer, rows: Array<Record<string, any>>}} Where */

export const STATE_EVENT = 'review.state';
/** B68: the row the engine writes after rebuilding a lost state file from its anchor. */
export const RESTORED_EVENT = 'review.state_restored';
/** B68: an anchor's `state_body` over this many bytes (as JSON) is `state_omitted: "too_large"`. */
export const STATE_BODY_CAP = 64 * 1024;

/**
 * B69: the block's state directory, `<code-forge home>/runs/<run>/review-state/<block>` (the run's
 * own directory, next to `runs/<run>/blocks/`).
 * @param {string} runId @param {string} block @returns {string}
 */
export const blockStateDir = (runId, block) => path.join(runsDir(), assertRunId(runId), 'review-state', assertBlockId(block));

/** @param {string} file @returns {string} the state file's name. */
const stateName = (file) => `${createHash('sha256').update(assertRowPath(file)).digest('hex').slice(0, 24)}.json`;

/**
 * The ONE path every reader and writer of a file's review state uses (B69).
 * @param {string} runId @param {string} block @param {string} file - repo-root-relative.
 * @returns {string}
 */
export function statePath(runId, block, file) {
  return path.join(blockStateDir(runId, block), stateName(file));
}

/**
 * B69: where the state file lived before (the run's temp root) — read once, to move it.
 * @param {string} runRootDir @param {string} block @param {string} file
 * @returns {string}
 */
export function legacyStatePath(runRootDir, block, file) {
  return path.join(runRootDir, 'review-state', assertBlockId(block), stateName(file));
}

/** @param {string} text @returns {string} */
const sha = (text) => createHash('sha256').update(text).digest('hex');

/**
 * B78: the env of a blob `git` call in `contentRoot`: `gitChildEnv()` plus a discovery ceiling at
 * the root's parent, so a content root that is not itself a repository never reaches (and writes
 * into) an enclosing one.
 * @param {string} contentRoot @returns {NodeJS.ProcessEnv}
 */
const blobEnv = (contentRoot) => ({ ...gitChildEnv(), GIT_CEILING_DIRECTORIES: path.dirname(path.resolve(contentRoot)) });

/** B78: a git object id (SHA-1 or SHA-256 repository), lowercase hex only. */
const OBJECT_ID = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;
/** B78: how long one blob write or read may take before it counts as failed. */
const BLOB_TIMEOUT_MS = 10_000;

/**
 * B78: write `text` (its UTF-8 bytes, no filters) as a blob into `contentRoot`'s object store.
 * @param {string} contentRoot @param {string} text
 * @returns {Promise<string | null>} the blob id, or null on any failure (never throws).
 */
async function writeBlob(contentRoot, text) {
  try {
    const res = await exec(['git', 'hash-object', '-w', '--no-filters', '--stdin'], {
      cwd: contentRoot,
      env: blobEnv(contentRoot),
      input: Buffer.from(text, 'utf8'),
      timeoutMs: BLOB_TIMEOUT_MS,
    });
    const id = res.stdout.trim();
    return res.result === 'ok' && OBJECT_ID.test(id) ? id : null;
  } catch {
    return null;
  }
}

/**
 * B78: the blob's text when it is in `contentRoot`'s object store and its sha256 is `wanted`.
 * Synchronous (the load is), so it spawns with `spawnSync`: argv only, no shell, a timeout.
 * @param {string} contentRoot @param {unknown} id @param {string} wanted
 * @returns {string | null} null when the id is not an object id, the blob is unreadable or gone,
 *   or its text does not match `wanted` (a mismatching blob is never used).
 */
function readBlob(contentRoot, id, wanted) {
  if (typeof id !== 'string' || !OBJECT_ID.test(id)) return null;
  try {
    const res = spawnSync('git', ['cat-file', 'blob', id], {
      cwd: contentRoot,
      env: blobEnv(contentRoot),
      shell: false,
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout: BLOB_TIMEOUT_MS,
      killSignal: 'SIGKILL',
      maxBuffer: 64 * 1024 * 1024,
    });
    if (res.error || res.status !== 0 || !Buffer.isBuffer(res.stdout)) return null;
    const text = res.stdout.toString('utf8');
    return sha(text) === wanted ? text : null;
  } catch {
    return null;
  }
}

/**
 * The block's verified `review.state` anchors, or null when any anchor of the RUN fails: EVERY
 * `review.state` row of the run is verified (MAC, integer `seq`, string `state_sha`) BEFORE the
 * block filter, so an anchor whose `block` was edited does not silently drop out and let the
 * previous seq's state file validate (a rollback) — it fails its MAC and the whole run is
 * `tampered`. The run selector stays first because the ledger is per project: other runs' anchors
 * are signed with their own keys and cannot verify with this one. (An anchor whose `run` was
 * edited is, for this run, a deleted line: out of the MAC's reach, like any dropped ledger row.)
 * @param {{runId: string, block: string, key: Buffer, rows: Array<Record<string, any>>}} opts
 * @returns {Array<Record<string, any>> | null}
 */
export function blockAnchors({ runId, block, key, rows }) {
  const ofRun = rows.filter((r) => r?.event === STATE_EVENT && r.run === runId);
  if (ofRun.some((r) => !verifyRow(r, key).ok || !Number.isInteger(r.seq) || typeof r.state_sha !== 'string')) return null;
  return ofRun.filter((r) => r.block === block);
}

/**
 * @param {Array<Record<string, any>>} anchors @param {string} file
 * @returns {Record<string, any> | null} the file's latest anchor.
 */
function latestAnchor(anchors, file) {
  let best = null;
  for (const a of anchors) if (a.file === file && (best === null || a.seq > best.seq)) best = a;
  return best;
}

/**
 * Whether a signed body (a state file's, or an anchor's `state_body`) is this file's state at the
 * anchor's seq: its MAC verifies with the run key, its run/block/file/seq equal the anchor's, and
 * `state.file === file`.
 * @param {any} body @param {{runId: string, block: string, file: string, key: Buffer, seq: number}} at
 * @returns {boolean}
 */
function bodyMatches(body, { runId, block, file, key, seq }) {
  return Boolean(
    body &&
      typeof body === 'object' &&
      verifyRow(body, key).ok &&
      body.run === runId &&
      body.block === block &&
      body.file === file &&
      body.seq === seq &&
      body.state &&
      typeof body.state === 'object' &&
      body.state.file === file,
  );
}

/**
 * B68: rebuild a lost state file from its latest anchor's signed `state_body`. `reviewed_content`
 * is (B78) the body's `reviewed_content_blob` read from the content root's object store when its
 * sha equals `reviewed_content_sha`; else the file's current working-tree content when its sha
 * equals it; else null (the next round then reviews the full file).
 * @param {Record<string, any>} anchor
 * @param {{runId: string, block: string, file: string, key: Buffer, contentRoot?: string}} at
 * @returns {{status: 'restored' | 'lost' | 'tampered', state: FileState | null, seq: number}}
 */
function restoreFromAnchor(anchor, { runId, block, file, key, contentRoot }) {
  if (!Object.hasOwn(anchor, 'state_body')) return { status: 'lost', state: null, seq: anchor.seq };
  const body = anchor.state_body;
  if (!bodyMatches(body, { runId, block, file, key, seq: anchor.seq })) return { status: 'tampered', state: null, seq: anchor.seq };
  const wanted = body.reviewed_content_sha;
  /** @type {string | null} */
  let reviewed = null;
  if (typeof wanted === 'string' && typeof contentRoot === 'string') reviewed = readBlob(contentRoot, body.reviewed_content_blob, wanted);
  if (reviewed === null && typeof wanted === 'string' && typeof contentRoot === 'string') {
    try {
      const current = readFileSync(path.join(contentRoot, file), 'utf8');
      if (sha(current) === wanted) reviewed = current;
    } catch {
      // unreadable now: reviewed stays null, the next round reviews the full file
    }
  }
  const state = /** @type {FileState} */ ({ ...body.state, reviewed_content: reviewed });
  return { status: 'restored', state, seq: anchor.seq };
}

/**
 * @param {string} file @returns {string | null} the file's text, null when it does not exist.
 * @throws any read error but ENOENT.
 */
function readIfThere(file) {
  try {
    return readFileSync(file, 'utf8');
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code === 'ENOENT') return null;
    throw err;
  }
}

/**
 * Replace `target` with `text` atomically (file mode 0600, a new dir 0700).
 * @param {string} target @param {string} text
 */
function writeAtomic(target, text) {
  mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
  const tmp = `${target}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
  try {
    writeFileSync(tmp, text, { mode: 0o600 });
    renameSync(tmp, target);
  } finally {
    try {
      rmSync(tmp, { force: true });
    } catch {
      // a stray temp file never hides the write's own error
    }
  }
}

/**
 * A state file's text checked against the file's latest anchor.
 * @param {string} text @param {Record<string, any>} anchor @param {{runId: string, block: string, file: string, key: Buffer}} at
 * @returns {{status: 'ok' | 'tampered', state: FileState | null, seq: number}}
 */
function checkText(text, anchor, { runId, block, file, key }) {
  if (sha(text) !== anchor.state_sha) return { status: 'tampered', state: null, seq: anchor.seq };
  let body = null;
  try {
    body = JSON.parse(text);
  } catch {
    body = null;
  }
  if (!bodyMatches(body, { runId, block, file, key, seq: anchor.seq })) return { status: 'tampered', state: null, seq: anchor.seq };
  return { status: 'ok', state: /** @type {FileState} */ (body.state), seq: anchor.seq };
}

/**
 * @param {Omit<Where, 'runRootDir'> & {runRootDir?: string, contentRoot?: string, migrate?: boolean, afterMiss?: () => void}} where -
 *   `contentRoot`: the tree the file's current content is read from when a lost state is restored
 *   (B68); without it the restored `reviewed_content` is null. `runRootDir`: the run's temp root,
 *   where an old state file may still be (B69). `migrate` (default true): move a valid old file to
 *   the new path; false only reads it (a sibling's check never moves another ticket's file).
 *   `afterMiss`: a test seam called when both paths were missing, before the new path is read
 *   once more.
 * @returns {{status: 'none' | 'ok' | 'restored' | 'lost' | 'tampered', state: FileState | null, seq: number}}
 */
export function loadState({ runRootDir, runId, block, file, key, rows, contentRoot, migrate = true, afterMiss }) {
  const anchors = blockAnchors({ runId, block, key, rows });
  if (anchors === null) return { status: 'tampered', state: null, seq: 0 };
  const anchor = latestAnchor(anchors, file);
  /** @type {string} */
  let target;
  let text = null;
  /** @type {string | null} */
  let legacy = null;
  try {
    target = statePath(runId, block, file);
    text = readIfThere(target);
    if (text === null && typeof runRootDir === 'string') {
      legacy = legacyStatePath(runRootDir, block, file);
      text = readIfThere(legacy);
      if (text === null) {
        legacy = null;
        // another ticket may have moved the old file to the new path between the two reads
        afterMiss?.();
        text = readIfThere(target);
      }
    }
  } catch {
    return { status: 'tampered', state: null, seq: 0 };
  }
  if (anchor === null) return text === null ? { status: 'none', state: null, seq: 0 } : { status: 'tampered', state: null, seq: 0 };
  if (text === null) return restoreFromAnchor(anchor, { runId, block, file, key, ...(contentRoot !== undefined ? { contentRoot } : {}) });
  const checked = checkText(text, anchor, { runId, block, file, key });
  if (checked.status !== 'ok' || legacy === null || !migrate) return checked;
  // B69: a valid file at the old temp path moves to the new one — never over a file already there
  // (a ticket that saved meanwhile wins and is loaded as usual) — then the old one is removed. A
  // move that fails leaves the old file where it is; the load is `ok` either way (no throw)
  const moved = moveIn(target, text);
  if (moved === 'exists') {
    const now = loadState({ runId, block, file, key, rows, ...(contentRoot !== undefined ? { contentRoot } : {}) });
    if (now.status !== 'ok') return now;
    // the new path holds a valid state (another ticket's move or save): the old copy is done
    try {
      rmSync(legacy, { force: true });
    } catch {
      // the old copy stays; the new path is read first from now on
    }
    return now;
  }
  if (moved === 'moved') {
    try {
      rmSync(legacy, { force: true });
    } catch {
      // the old copy stays; the new path is read first from now on
    }
  }
  return checked;
}

/**
 * B69: write `text` to the new state path without overwriting a file there: a hard link of a
 * 0600 temp file, or — where hard links fail (EPERM, ENOTSUP, …) — an exclusive create.
 * @param {string} target @param {string} text
 * @returns {'moved' | 'exists' | 'failed'}
 */
function moveIn(target, text) {
  let tmp = null;
  try {
    mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
    tmp = `${target}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
    writeFileSync(tmp, text, { mode: 0o600 });
    try {
      linkSync(tmp, target);
      return 'moved';
    } catch (err) {
      if (/** @type {NodeJS.ErrnoException} */ (err).code === 'EEXIST') return 'exists';
    }
    writeFileSync(target, text, { flag: 'wx', mode: 0o600 });
    return 'moved';
  } catch (err) {
    return /** @type {NodeJS.ErrnoException} */ (err)?.code === 'EEXIST' ? 'exists' : 'failed';
  } finally {
    if (tmp !== null) {
      try {
        rmSync(tmp, { force: true });
      } catch {
        // a stray temp file never fails the load
      }
    }
  }
}

/**
 * B69 (doctor): the files of the run whose latest verified `review.state` anchor has a state file
 * neither at the new path nor (when `runRootDir` is given) at the old one, each with whether that
 * anchor carries a `state_body` (the ledger can rebuild it) or not (lost). The anchor is picked
 * exactly as `loadState` picks it (`blockAnchors` per block, then `latestAnchor`).
 * @param {{runId: string, key: Buffer, rows: Array<Record<string, any>>, runRootDir?: string}} opts
 * @returns {Array<{block: string, file: string, rebuildable: boolean}> | null} null when an anchor
 *   of the run does not verify (every ticket of the run is then refused `review-state-tampered`).
 */
export function missingStateFiles({ runId, key, rows, runRootDir }) {
  const blocks = [...new Set(rows.filter((r) => r?.event === STATE_EVENT && r.run === runId).map((r) => r.block))];
  const out = [];
  for (const block of blocks) {
    const anchors = blockAnchors({ runId, block, key, rows });
    if (anchors === null) return null;
    for (const file of new Set(anchors.map((a) => a.file))) {
      const anchor = latestAnchor(anchors, file);
      if (anchor === null) continue;
      let paths;
      try {
        paths = [statePath(runId, block, file), ...(typeof runRootDir === 'string' ? [legacyStatePath(runRootDir, block, file)] : [])];
      } catch {
        continue; // a block id or a path no state file can have
      }
      if (paths.some((p) => existsSync(p))) continue;
      out.push({ block, file, rebuildable: Object.hasOwn(anchor, 'state_body') });
    }
  }
  return out;
}

/**
 * Anchor, then write: the signed `review.state` row goes to the ledger FIRST (a failed ledger write
 * changes nothing and throws), then the state file is replaced atomically (mode 0600).
 * @param {Where & {state: FileState, seq: number, writeRow: (row: Record<string, any>) => Promise<unknown>, contentRoot?: string}} opts
 *   `seq` - the loaded state's seq (0 for a new file); this save is `seq + 1`. `contentRoot` (B78):
 *   the tree the round read the file in; the reviewed text goes into its object store as a blob.
 */
export async function saveState({ runId, block, file, key, state, seq, writeRow, contentRoot }) {
  const target = statePath(runId, block, file);
  const next = seq + 1;
  const text = `${JSON.stringify(signRow({ run: runId, block, file, seq: next, state }, key))}\n`;
  // B68: the slim signed state rides on the anchor (its own MAC covers it), so a lost file can be
  // rebuilt; the full reviewed text stays out of the ledger, only its sha (and, B78, the id of its
  // git blob in the content root's object store) goes in
  const reviewed = typeof state.reviewed_content === 'string' ? state.reviewed_content : null;
  const blob = reviewed !== null && typeof contentRoot === 'string' ? await writeBlob(contentRoot, reviewed) : null;
  const stateBody = signRow(
    { run: runId, block, file, seq: next, state: { ...state, reviewed_content: null }, reviewed_content_sha: reviewed === null ? null : sha(reviewed), reviewed_content_blob: blob },
    key,
  );
  const slim = Buffer.byteLength(JSON.stringify(stateBody), 'utf8') > STATE_BODY_CAP ? { state_omitted: 'too_large' } : { state_body: stateBody };
  await writeRow({ event: STATE_EVENT, block, file, seq: next, state_sha: sha(text), l3_rung_used: state.l3_rung_used === true, ...slim });
  writeAtomic(target, text);
}

/**
 * Whether the block's one L3 rung (§3.6 rules 5/6) was already used on ANY of its files — the
 * rung is once per block. Derived from the ledger anchors only; fails closed: a block anchor that
 * does not verify is `tampered`.
 * @param {{runId: string, block: string, key: Buffer, rows: Array<Record<string, any>>}} opts
 * @returns {{status: 'ok' | 'tampered', used: boolean}}
 */
export function blockRungUsed(opts) {
  const anchors = blockAnchors(opts);
  if (anchors === null) return { status: 'tampered', used: true };
  return { status: 'ok', used: anchors.some((a) => a.l3_rung_used === true) };
}
