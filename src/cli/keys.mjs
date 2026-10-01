/**
 * `code-forge keys set|list|test|remove` (plan §8.1). No subcommand ever prints a key value:
 * `list` shows name, source, backend and expiry from the store's index; `test` reports where a
 * key resolved from. Every write goes through `writeSafe`, so even a value that slipped into a
 * message would be masked once the chain registered it.
 *
 *   keys list
 *   keys set <name> [--op <ref>]         hidden prompt, or read once from 1Password and cache 8 h
 *   keys test <name> [--ref <ref>]       resolve through the chain (no prompt)
 *   keys remove <name>
 *
 * An `--op`/`--ref` value may be a 1Password item ID or item link instead of `op://vault/item/field`:
 * it is resolved to `op://<vaultId>/<itemId>/<fieldId>` first (`op item get`, B25).
 *
 * Every name is validated before any store call, on every subcommand.
 */

import { writeSafe } from '../util/redact.mjs';
import { describeOpItem, isOpRef, OP_MESSAGES, opItemIdFromInput, toOpRef } from '../keys/onepassword.mjs';
import { assertKeyName, createDefaultKeyStore, parseRef, readOpAndCache, resolveKey } from '../keys/store.mjs';

const USAGE = 'usage: code-forge keys list | set <name> [--op <item-id|link|op://ref>] | test <name> [--ref <ref>] | remove <name>\n';

/** The one flag each subcommand accepts (list and remove take none). */
const FLAGS = /** @type {Record<string, string|null>} */ ({ list: null, set: '--op', test: '--ref', remove: null });

/**
 * @param {number|null} exp
 * @param {number} now
 * @returns {string}
 */
export function formatExpiry(exp, now) {
  if (exp === null) {
    return 'never';
  }
  return exp <= now ? 'expired' : new Date(exp).toISOString();
}

/**
 * Strict parse: `<sub> [<name>] [<flag> <value>]`, in that order. A flag with no value, a value
 * that looks like a flag, a name that looks like a flag, an unknown flag or an extra argument is
 * a usage error — never silently reinterpreted.
 * @param {string[]} args
 * @returns {{sub: string, name?: string, flagArg?: string} | null}
 */
export function parseKeysArgs(args) {
  const [sub, ...rest] = args;
  if (typeof sub !== 'string' || !Object.hasOwn(FLAGS, sub)) {
    return null;
  }
  if (sub === 'list') {
    return rest.length === 0 ? { sub } : null;
  }
  const [name, flagName, flagArg, ...extra] = rest;
  if (typeof name !== 'string' || name.startsWith('-') || extra.length > 0) {
    return null;
  }
  if (flagName === undefined) {
    return { sub, name };
  }
  if (flagName !== FLAGS[sub] || typeof flagArg !== 'string' || flagArg.length === 0 || flagArg.startsWith('-')) {
    return null;
  }
  return { sub, name, flagArg };
}

/** @param {string} name */
async function askHidden(name) {
  const { password, isCancel } = await import('@clack/prompts');
  const typed = await password({ message: `Value for key "${name}" (input hidden)` });
  return isCancel(typed) ? null : String(typed);
}

/**
 * @param {string[]} args
 * @param {object} deps
 * @param {import('../keys/store.mjs').KeyStore} deps.store
 * @param {{write: (s: string) => unknown}} [deps.stdout]
 * @param {{write: (s: string) => unknown}} [deps.stderr]
 * @param {(name: string) => Promise<string|null>} [deps.ask]
 * @param {typeof import('../keys/onepassword.mjs').opRead} [deps.opRead]
 * @param {typeof import('../util/exec.mjs').exec} [deps.opExec] - runs `op item get` for an item ID or link.
 * @param {NodeJS.ProcessEnv} [deps.env]
 * @param {number} [deps.now]
 * @returns {Promise<number>}
 */
export async function runKeys(args, { store, stdout = process.stdout, stderr = process.stderr, ask = askHidden, opRead, opExec, env = process.env, now = Date.now() }) {
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);

  const parsed = parseKeysArgs(args);
  if (!parsed) {
    err(USAGE);
    return 2;
  }
  const { sub, name } = parsed;
  let { flagArg } = parsed;
  try {
    if (name !== undefined) {
      assertKeyName(name);
    }
  } catch (e) {
    err(`keys: ${e.message}\n`);
    return 2;
  }
  const fullRef = flagArg !== undefined && isOpRef(flagArg);
  const itemId = flagArg !== undefined && !fullRef ? opItemIdFromInput(flagArg) : null;
  if (sub === 'set' && flagArg !== undefined && !fullRef && itemId === null) {
    err(`${name}: --op must be a 1Password item ID, item link or op://vault/item/field reference\n`);
    return 2;
  }
  // A 1Password item ID or link becomes a full op:// reference before any store call (B25).
  if (flagArg !== undefined && itemId !== null) {
    /** @type {Awaited<ReturnType<typeof toOpRef>>} */
    let res;
    try {
      res = await toOpRef(flagArg, opExec ? { exec: opExec } : {});
    } catch {
      // never the thrown message: it could carry op output
      res = { ref: null, kind: 'op_failed', error: `${OP_MESSAGES.op_failed} (unexpected error); run \`op item get ${itemId}\` yourself to see why` };
    }
    if (res.ref === null) {
      err(`${name}: ${res.error ?? OP_MESSAGES.op_failed}\n`);
      return 1;
    }
    const found = describeOpItem(res);
    out(`${name}: ${found ? `${found} -> ` : ''}${res.ref}\n`);
    flagArg = res.ref;
  }
  try {
    if (sub === 'test' && flagArg !== undefined) {
      parseRef(name, flagArg);
    }
  } catch (e) {
    err(`keys: ${e.message}\n`);
    return 2;
  }

  if (store.warning) {
    err(`WARN ${store.warning}\n`);
  }

  if (sub === 'list') {
    const rows = await store.list();
    out('NAME\tSOURCE\tBACKEND\tEXPIRES\n');
    for (const row of rows) {
      out(`${row.name}\t${row.source}\t${row.backend}\t${formatExpiry(row.exp, now)}\n`);
    }
    return 0;
  }

  if (sub === 'remove') {
    const removed = await store.remove(name);
    out(`removed ${name} (${removed} backend${removed === 1 ? '' : 's'})\n`);
    return 0;
  }

  if (sub === 'test') {
    const res = await resolveKey(name, { store, ref: flagArg, env, opRead, now });
    for (const e of res.errors) {
      err(`${name}: ${e}\n`);
    }
    if (res.value === null) {
      err(`${name}: not found\n`);
      return 1;
    }
    out(`${name}: ok source=${res.source} expires=${formatExpiry(res.exp, now)}\n`);
    return 0;
  }

  if (flagArg !== undefined) {
    const res = await readOpAndCache(name, flagArg, { store, opRead, now });
    for (const e of res.errors) {
      err(`${name}: ${e}\n`);
    }
    // A null value always carries at least one reason (`readOpAndCache` guarantees it).
    if (res.value === null) {
      return 1;
    }
    out(`stored ${name} in ${store.writer} (source=op, expires=${formatExpiry(res.exp, now)})\n`);
    return 0;
  }

  const typed = await ask(name);
  if (typeof typed !== 'string' || typed.length === 0) {
    err(`${name}: nothing stored\n`);
    return 1;
  }
  await store.put(name, typed, { source: 'user', exp: null });
  out(`stored ${name} in ${store.writer} (source=user, expires=never)\n`);
  return 0;
}

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function keys(args) {
  return runKeys(args, { store: await createDefaultKeyStore() });
}
