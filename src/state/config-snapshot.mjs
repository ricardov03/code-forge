/**
 * The run's config snapshot (issue #2, block B35): `run start` stores the loaded
 * `.code-forge.yml` in the run record, `run reload` replaces it mid-run, and the worker reviews
 * every ticket with the snapshot it was enqueued under.
 *
 * Shape, in the authoritative run record (`~/.code-forge/runs/<run>.json`):
 *
 *   config: {
 *     hash:      sha256 of the canonical JSON of the snapshot in force,
 *     snapshots: {<hash>: <config>}   every snapshot a queued ticket may still need,
 *     pins:      {<ticket id>: <hash>} tickets that were pending at a reload keep the old snapshot,
 *     reloaded_at?: ISO time of the last reload,
 *   }
 *
 * A ticket is pinned by `run reload` itself (the orchestrator, under the run lock), never by the
 * coder-writable ticket file, so a coder cannot pick an older (weaker) config for a new ticket.
 * Messages here name key PATHS only, never values.
 */

import { createHash } from 'node:crypto';
import { StateError } from './paths.mjs';

/**
 * Keys that cannot change mid-run, each with why. A change to one is refused by `run reload`
 * (end the run and start a new one).
 */
export const IMMUTABLE_KEYS = Object.freeze({
  version: 'the config format is fixed for the run',
  'project.slug': 'the ledger file of the run is chosen at run start',
  engine: 'the run engine is fixed at run start',
  'tmp.root': "the worker's run temp root is created at run start",
  keys: 'the worker resolves keys once, at start',
  'system1.key': 'the worker resolves the Jev key once, at start',
  // B58: the owner's switch past the local per-file review is set before the run starts or not at
  // all, so nobody turns it on (or off) mid-run
  'review.external': 'the outside reviewer is fixed by the owner before the run starts',
  'review.agent_waivers.require_ack': 'human acknowledgement of agent waivers is fixed by the owner before the run starts',
  // B62a: every block of the run is isolated the same way — only two blocks that each have their
  // own worktree may share owned paths — so neither key changes mid-run
  'proof.isolation': 'every block of the run is isolated the same way',
  'proof.worktree': 'the worktrees and branches of the open blocks were made with it',
  // B63b: it tightens `block close` (the coder's green test run), so it is authority like
  // `review.external`; the rest of `context` stays live
  'context.require_coder_tests': 'whether close needs the coder\'s green test run is fixed by the owner before the run starts',
  // B64b: the block-end hook runs a command on the owner's machine at every block end, so only
  // the owner sets it, before the run starts
  'hooks.on_block_end': 'it runs a command on the owner\'s machine at every block end; fixed by the owner before the run starts',
  // B87: the runner argv proves the coder's own work, so only the owner sets it, before the run starts
  'proof.runners': 'the commands that prove a block are fixed by the owner before the run starts',
  // B93: the coder sandbox profile widens what the coder may write, so only the owner sets it,
  // before the run starts (a coder can never own `.code-forge.yml`)
  'coder.sandbox': 'the coder sandbox widens what the coder may write; fixed by the owner before the run starts',
});

/** @param {unknown} v @returns {boolean} what `JSON.stringify` drops from an object. */
const dropped = (v) => v === undefined || typeof v === 'function' || typeof v === 'symbol';

/**
 * JSON with object keys sorted at every level (arrays keep their order), so the hash does not
 * depend on key order in the YAML. JSON semantics otherwise: a value with `toJSON` (a `Date` from
 * a YAML timestamp ⇒ its ISO string) is converted first, and what JSON drops is dropped (`null` in
 * a list) — so the hash of a config equals the hash of the same config after the run record's
 * JSON save/read round trip.
 * @param {unknown} value
 * @returns {string}
 */
export function canonicalJSON(value) {
  const v = value !== null && typeof value === 'object' && typeof (/** @type {any} */ (value).toJSON) === 'function' ? /** @type {any} */ (value).toJSON() : value;
  if (Array.isArray(v)) return `[${v.map((item) => canonicalJSON(dropped(item) ? null : item)).join(',')}]`;
  if (v !== null && typeof v === 'object') {
    const obj = /** @type {Record<string, unknown>} */ (v);
    const keys = Object.keys(obj)
      .filter((k) => !dropped(obj[k]))
      .sort();
    return `{${keys.map((k) => `${JSON.stringify(k)}:${canonicalJSON(obj[k])}`).join(',')}}`;
  }
  return dropped(v) ? 'null' : JSON.stringify(v);
}

/** @param {unknown} cfg @returns {string} sha256 hex of the canonical JSON. */
export function configHash(cfg) {
  return createHash('sha256').update(canonicalJSON(cfg)).digest('hex');
}

/** @param {unknown} v @returns {v is Record<string, unknown>} */
const isMapping = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);

/**
 * The dotted key paths whose value differs between `before` and `after`, sorted. Mappings are
 * walked key by key; a key present on one side only, a list, or a scalar is one path. A path
 * segment that is not a plain identifier is written in brackets (`keys["a.b"]`).
 * @param {unknown} before @param {unknown} after
 * @returns {string[]}
 */
export function changedKeyPaths(before, after) {
  /** @type {string[]} */
  const out = [];
  /** @param {unknown} a @param {unknown} b @param {string} at */
  const walk = (a, b, at) => {
    if (isMapping(a) && isMapping(b)) {
      const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
      for (const k of keys) walk(own(a, k), own(b, k), join(at, k));
      return;
    }
    if (canonicalJSON(a ?? null) !== canonicalJSON(b ?? null) || (a === undefined) !== (b === undefined)) out.push(at);
  };
  walk(before, after, '');
  return out.sort();
}

/** @param {Record<string, unknown>} obj @param {string} key @returns {unknown} an OWN property only. */
const own = (obj, key) => (Object.hasOwn(obj, key) ? obj[key] : undefined);

/** @param {string} at @param {string} key */
function join(at, key) {
  const seg = /^[A-Za-z_][A-Za-z0-9_-]*$/.test(key) ? key : `[${JSON.stringify(key)}]`;
  if (at === '') return seg;
  return seg.startsWith('[') ? `${at}${seg}` : `${at}.${seg}`;
}

/**
 * @param {unknown} cfg @param {string} dotted
 * @returns {unknown} the value at `dotted`, or undefined.
 */
function valueAt(cfg, dotted) {
  /** @type {unknown} */
  let cur = cfg;
  for (const seg of dotted.split('.')) {
    if (!isMapping(cur)) return undefined;
    cur = own(cur, seg);
  }
  return cur;
}

/**
 * The immutable keys whose value differs between the two configs, in {@link IMMUTABLE_KEYS} order.
 * @param {unknown} before @param {unknown} after
 * @returns {string[]}
 */
export function immutableChanges(before, after) {
  return Object.keys(IMMUTABLE_KEYS).filter((k) => canonicalJSON(valueAt(before, k) ?? null) !== canonicalJSON(valueAt(after, k) ?? null));
}

/**
 * The `config` block for a new run record.
 * @param {Record<string, any>} cfg
 */
export function initialConfigState(cfg) {
  const hash = configHash(cfg);
  return { hash, snapshots: { [hash]: cfg }, pins: {} };
}

/**
 * The snapshot a ticket is reviewed with: its pin when `run reload` pinned it, else the current
 * one. Null when the record predates snapshots (the caller keeps the config it booted with).
 * A snapshot that is missing or does not match its hash throws `config-snapshot`.
 * @param {Record<string, any>} record @param {string} ticket
 * @returns {{hash: string, config: Record<string, any>} | null}
 */
export function snapshotFor(record, ticket) {
  const state = record?.config;
  if (!isMapping(state) || typeof state.hash !== 'string') return null;
  const pins = isMapping(state.pins) ? state.pins : {};
  const pin = own(pins, ticket);
  const hash = typeof pin === 'string' ? pin : state.hash;
  const snaps = isMapping(state.snapshots) ? state.snapshots : {};
  const config = own(snaps, hash);
  if (!isMapping(config) || configHash(config) !== hash) throw new Error('config-snapshot');
  return { hash, config };
}

/**
 * The config snapshot in force for the run (no ticket pin), or null for a record from before
 * snapshots.
 * @param {Record<string, any>} record
 * @returns {Record<string, any> | null}
 * @throws {StateError} `config-snapshot` when the snapshot in force does not match its hash.
 */
export function configInForce(record) {
  try {
    return snapshotFor(record, '')?.config ?? null;
  } catch (thrown) {
    if (!(thrown instanceof Error) || thrown.message !== 'config-snapshot') throw thrown;
    throw new StateError('config-snapshot', `run ${record.run_id}: the config snapshot in force does not match its hash`);
  }
}

/**
 * B58: the outside reviewer named by the snapshot in force when `review.external.required` is
 * true; null otherwise (key absent, `required` false, or a record from before snapshots). The key
 * is immutable ({@link IMMUTABLE_KEYS}), so this is the value `run start` stored.
 * @param {Record<string, any>} record
 * @returns {string | null}
 * @throws {StateError} `config-snapshot` when the snapshot in force does not match its hash.
 */
export function externalReviewer(record) {
  const external = configInForce(record)?.review?.external;
  if (!isMapping(external) || external.required !== true) return null;
  // required without a usable name is no switch at all (the schema refuses it; this is the floor)
  const name = typeof external.name === 'string' ? external.name.trim() : '';
  return name === '' ? null : name; // trimmed: no row ever carries padding
}

/**
 * B63b: whether `block close` needs the coder's `TESTS <command> exit=0` line — true only when the
 * snapshot in force sets `context.require_coder_tests: true` (false for the key absent or false,
 * or a record from before snapshots). The key is immutable ({@link IMMUTABLE_KEYS}), so this is
 * the value `run start` stored; the live config is never read.
 * @param {Record<string, any>} record
 * @returns {boolean}
 * @throws {StateError} `config-snapshot` when the snapshot in force does not match its hash.
 */
export function requireCoderTests(record) {
  const context = configInForce(record)?.context;
  return isMapping(context) && context.require_coder_tests === true;
}

/** The default `hooks.on_block_end.timeout_s`. */
export const HOOK_TIMEOUT_DEFAULT_S = 60;
/** The schema's bounds of `hooks.on_block_end.timeout_s`. */
export const HOOK_TIMEOUT_MIN_S = 1;
export const HOOK_TIMEOUT_MAX_S = 600;

/**
 * `hooks.on_block_end` of the run-start config snapshot (never the live config), or null when it
 * is absent or malformed (the schema refuses a malformed one at `run start`).
 * @param {Record<string, any>} record
 * @returns {{argv: string[], timeoutS: number} | null}
 */
export function blockEndHook(record) {
  let hook;
  try {
    hook = configInForce(record)?.hooks?.on_block_end;
  } catch {
    return null;
  }
  if (hook === null || typeof hook !== 'object') return null;
  const { argv, timeout_s: timeout } = hook;
  if (!Array.isArray(argv) || argv.length === 0 || !argv.every((a) => typeof a === 'string' && a.length > 0)) return null;
  const timeoutS = timeout === undefined ? HOOK_TIMEOUT_DEFAULT_S : timeout;
  if (!Number.isInteger(timeoutS) || timeoutS < HOOK_TIMEOUT_MIN_S || timeoutS > HOOK_TIMEOUT_MAX_S) return null;
  return { argv: [...argv], timeoutS };
}

/**
 * B87: `proof.runners` of the run-start config snapshot (never the live config), as a deep copy
 * (entries and argv arrays), or null when the key is absent or the record is from before
 * snapshots. `proof/runners.mjs` checks each entry again before it runs one (fail closed).
 * @param {Record<string, any>} record
 * @returns {unknown[] | null}
 * @throws {StateError} `config-snapshot` when the snapshot in force does not match its hash;
 *   `runner-invalid` when the stored value is not a list, or is an empty one.
 */
export function proofRunners(record) {
  const runners = configInForce(record)?.proof?.runners;
  if (runners === undefined || runners === null) return null;
  if (!Array.isArray(runners)) throw new StateError('runner-invalid', `run ${record.run_id}: proof.runners in the run snapshot is not a list`);
  // `validate` refuses an empty list (minItems 1); a record that holds one anyway is refused too
  if (runners.length === 0) throw new StateError('runner-invalid', `run ${record.run_id}: proof.runners in the run snapshot is an empty list`);
  // a deep copy: a caller that changes an entry or an argv array never changes the snapshot
  return structuredClone(runners);
}

/**
 * B93: `coder.sandbox` of the run-start config snapshot (never the live config). A record from
 * before snapshots has no run-start value: `{snapshot: false}`. Otherwise `{snapshot: true,
 * sandbox}`, where `sandbox` is a deep copy of the value, or null when the snapshot has no key.
 * @param {Record<string, any>} record
 * @returns {{snapshot: false} | {snapshot: true, sandbox: Record<string, any> | null}}
 * @throws {StateError} `config-snapshot` when the snapshot in force does not match its hash.
 */
export function coderSandbox(record) {
  const config = configInForce(record);
  if (config === null) return { snapshot: false };
  const sandbox = config.coder?.sandbox;
  return { snapshot: true, sandbox: sandbox === undefined || sandbox === null ? null : structuredClone(sandbox) };
}
