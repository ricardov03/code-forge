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
