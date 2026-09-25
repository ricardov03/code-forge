/**
 * The fix-loop state per `(run, block, file)` (plan §4.11; block B12c).
 *
 * B12b's `fixloop.mjs` is a state machine whose `FileState` (round, level, open findings,
 * `reviewed_content`, …) must survive between two `review-file` calls on the same file: round 1
 * is the first ticket, round 2 (the fix-hunk recheck) the next one, and so on. The worker keeps it
 * in the run's temp root, OUTSIDE the workspace:
 *
 *   <run-root>/review-state/<block>/<sha256(file)[0:24]>.json
 *
 * Every state file is SIGNED with the run key (`{run, block, file, seq, state}` + `mac`) AND
 * anchored in the run's append-only ledger: each save first writes a signed row
 * `{event: 'review.state', block, file, seq, state_sha, l3_rung_used}`. A load matches the file
 * against the LATEST verified anchor (highest `seq`) for the file: a missing file, an older signed
 * file (rollback), a different sha, or a file with no anchor is `tampered` — the hook refuses the
 * ticket, never a fresh round 1 that could clear a `review_cap` stop or an open finding. A
 * `review.state` row of the run whose MAC does not verify is `tampered` for every block and file
 * of it (`blockAnchors`). Whether the block's one L3 rung was used comes from the anchors, never
 * from the state files.
 */

import { createHash, randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { signRow, verifyRow } from '../state/signer.mjs';
import { assertBlockId, assertRowPath } from './ticket.mjs';

/** @typedef {import('../review/fixloop.mjs').FileState} FileState */
/** @typedef {{runRootDir: string, runId: string, block: string, file: string, key: Buffer, rows: Array<Record<string, any>>}} Where */

export const STATE_EVENT = 'review.state';

/** @param {string} runRootDir @param {string} block @returns {string} */
export const blockStateDir = (runRootDir, block) => path.join(runRootDir, 'review-state', assertBlockId(block));

/**
 * @param {string} runRootDir @param {string} block @param {string} file - repo-root-relative.
 * @returns {string}
 */
export function statePath(runRootDir, block, file) {
  const name = createHash('sha256').update(assertRowPath(file)).digest('hex').slice(0, 24);
  return path.join(blockStateDir(runRootDir, block), `${name}.json`);
}

/** @param {string} text @returns {string} */
const sha = (text) => createHash('sha256').update(text).digest('hex');

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
 * @param {Where} where
 * @returns {{status: 'none' | 'ok' | 'tampered', state: FileState | null, seq: number}}
 */
export function loadState({ runRootDir, runId, block, file, key, rows }) {
  const anchors = blockAnchors({ runId, block, key, rows });
  if (anchors === null) return { status: 'tampered', state: null, seq: 0 };
  const anchor = latestAnchor(anchors, file);
  let text = null;
  try {
    text = readFileSync(statePath(runRootDir, block, file), 'utf8');
  } catch (err) {
    if (/** @type {NodeJS.ErrnoException} */ (err).code !== 'ENOENT') return { status: 'tampered', state: null, seq: 0 };
  }
  if (anchor === null) return text === null ? { status: 'none', state: null, seq: 0 } : { status: 'tampered', state: null, seq: 0 };
  if (text === null || sha(text) !== anchor.state_sha) return { status: 'tampered', state: null, seq: anchor.seq };
  let body = null;
  try {
    body = JSON.parse(text);
  } catch {
    body = null;
  }
  const ok =
    body &&
    typeof body === 'object' &&
    verifyRow(body, key).ok &&
    body.run === runId &&
    body.block === block &&
    body.file === file &&
    body.seq === anchor.seq &&
    body.state &&
    typeof body.state === 'object' &&
    body.state.file === file;
  if (!ok) return { status: 'tampered', state: null, seq: anchor.seq };
  return { status: 'ok', state: /** @type {FileState} */ (body.state), seq: anchor.seq };
}

/**
 * Anchor, then write: the signed `review.state` row goes to the ledger FIRST (a failed ledger write
 * changes nothing and throws), then the state file is replaced atomically (mode 0600).
 * @param {Where & {state: FileState, seq: number, writeRow: (row: Record<string, any>) => Promise<unknown>}} opts
 *   `seq` - the loaded state's seq (0 for a new file); this save is `seq + 1`.
 */
export async function saveState({ runRootDir, runId, block, file, key, state, seq, writeRow }) {
  const target = statePath(runRootDir, block, file);
  const next = seq + 1;
  const text = `${JSON.stringify(signRow({ run: runId, block, file, seq: next, state }, key))}\n`;
  await writeRow({ event: STATE_EVENT, block, file, seq: next, state_sha: sha(text), l3_rung_used: state.l3_rung_used === true });
  mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
  const tmp = `${target}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
  try {
    writeFileSync(tmp, text, { mode: 0o600 });
    renameSync(tmp, target);
  } catch (err) {
    rmSync(tmp, { force: true });
    throw err;
  }
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
