/**
 * Signed ledger rows (plan §4.8, §8.6, C4) — `mac = HMAC-SHA256(run key, canonical row)`.
 *
 * What this proves, stated as the plan states it: TAMPER EVIDENCE within a coder's ordinary tool
 * use (T1 a row the worker never wrote, T2 a result edited after signing, T3 — with the row keyed
 * by content hash upstream — a stale approval). It is NOT a same-user boundary: the key is a 0600
 * file the same OS user can read, so a coder that deliberately `cat`s it can mint a valid MAC. The
 * forbidden list (`code-forge-runs-access`) and the transcript grep make that a named rule-break;
 * the real boundary (a second OS user or a container) is the consumer's choice.
 *
 * Canonical form: the row minus `UNSIGNED_FIELDS`, keys sorted recursively, `JSON.stringify`
 * value encoding. `tokens_source`/`cost_source` are excluded because the ledger writer (B6
 * `appendRow`) fills them in AFTER signing — they are labels, never gate inputs. `ts` IS signed,
 * so `signRow` stamps it first (the writer keeps an existing `ts`).
 */

import { createHmac, randomBytes, timingSafeEqual } from 'node:crypto';
import { chmod, mkdir, readFile, stat, writeFile } from 'node:fs/promises';
import { registerSecret } from '../util/redact.mjs';
import { StateError, runKeyPath, runsDir } from './paths.mjs';

export const KEY_BYTES = 32;

/** Fields the MAC does not cover (see the module doc). */
export const UNSIGNED_FIELDS = Object.freeze(['mac', 'tokens_source', 'cost_source']);

/**
 * Create the run's key: 32 random bytes, mode 0600, in `~/.code-forge/runs/` (0700). Refuses to
 * overwrite an existing key — key rotation is a new run.
 * @param {string} runId
 * @returns {Promise<Buffer>}
 */
export async function generateKey(runId) {
  await mkdir(runsDir(), { recursive: true, mode: 0o700 });
  await chmod(runsDir(), 0o700); // `mode` only applies to a directory mkdir creates
  const key = randomBytes(KEY_BYTES);
  const file = runKeyPath(runId);
  try {
    await writeFile(file, key, { mode: 0o600, flag: 'wx' });
  } catch (err) {
    if (err.code === 'EEXIST') throw new StateError('key-exists', `run ${runId} already has a key`);
    throw err;
  }
  await chmod(file, 0o600);
  registerSecret(key.toString('hex'));
  return key;
}

/**
 * @param {string} runId
 * @returns {Promise<Buffer>}
 * @throws {StateError} `no-key` / `bad-key` / `bad-key-mode` (group- or world-accessible)
 */
export async function loadKey(runId) {
  let key;
  try {
    const { mode } = await stat(runKeyPath(runId));
    if ((mode & 0o077) !== 0) throw new StateError('bad-key-mode', `run ${runId} signer key is accessible to other users (mode ${(mode & 0o777).toString(8)}); expected 600`);
    key = await readFile(runKeyPath(runId));
  } catch (err) {
    if (err.code === 'ENOENT') throw new StateError('no-key', `run ${runId} has no signer key`);
    throw err;
  }
  if (key.length !== KEY_BYTES) throw new StateError('bad-key', `run ${runId} signer key is not ${KEY_BYTES} bytes`);
  registerSecret(key.toString('hex'));
  return key;
}

/**
 * @param {unknown} value
 * @returns {string} JSON with object keys sorted at every depth.
 */
export function canonicalJSON(value) {
  if (Array.isArray(value)) return `[${value.map((v) => (v === undefined ? 'null' : canonicalJSON(v))).join(',')}]`;
  if (value !== null && typeof value === 'object') {
    const entries = Object.keys(value)
      .sort()
      .filter((k) => value[k] !== undefined)
      .map((k) => `${JSON.stringify(k)}:${canonicalJSON(value[k])}`);
    return `{${entries.join(',')}}`;
  }
  return JSON.stringify(value);
}

/**
 * @param {Record<string, any>} row @param {Buffer} key
 * @returns {string} hex MAC
 */
export function macFor(row, key) {
  // The JSON round trip is what the ledger stores: Date → ISO string, functions/undefined dropped;
  // a BigInt throws (a row that cannot be written must not be signed).
  const covered = JSON.parse(JSON.stringify(row));
  for (const field of UNSIGNED_FIELDS) delete covered[field];
  return createHmac('sha256', key).update(canonicalJSON(covered)).digest('hex');
}

/**
 * @param {Record<string, any>} row @param {Buffer} key
 * @returns {Record<string, any>} the row with `ts` stamped (when absent) and `mac` set.
 */
export function signRow(row, key) {
  /** @type {Record<string, any>} */
  const stamped = { ...row, ts: row.ts ?? new Date().toISOString() };
  delete stamped.mac;
  return { ...stamped, mac: macFor(stamped, key) };
}

/**
 * @param {Record<string, any>} row @param {Buffer} key
 * @returns {{ok: boolean, reason?: 'unsigned' | 'mismatch'}}
 */
export function verifyRow(row, key) {
  if (typeof row?.mac !== 'string' || row.mac.length === 0) return { ok: false, reason: 'unsigned' };
  if (!/^[0-9a-f]{64}$/.test(row.mac)) return { ok: false, reason: 'mismatch' };
  const expected = Buffer.from(macFor(row, key), 'hex');
  const given = Buffer.from(row.mac, 'hex');
  if (given.length !== expected.length || !timingSafeEqual(given, expected)) return { ok: false, reason: 'mismatch' };
  return { ok: true };
}

/**
 * @param {Record<string, any>[]} rows @param {Buffer} key
 * @returns {{ok: boolean, failures: {index: number, event: string, reason: string}[]}}
 */
export function verifyRows(rows, key) {
  const failures = [];
  rows.forEach((row, index) => {
    const result = verifyRow(row, key);
    if (!result.ok) failures.push({ index, event: String(row?.event), reason: result.reason });
  });
  return { ok: failures.length === 0, failures };
}
