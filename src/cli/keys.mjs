/**
 * `code-forge keys set|list|test|remove` (plan §8.1). No subcommand ever prints a key value:
 * `list` shows name, source, backend and expiry from the store's index; `test` reports where a
 * key resolved from. Every write goes through `writeSafe`, so even a value that slipped into a
 * message would be masked once the chain registered it.
 *
 *   keys list
 *   keys set <name> [--op <ref> [--cache-hours <n>]]
 *                                        hidden prompt, or read once from 1Password and cache it
 *   keys test <name> [--ref <ref>]       resolve through the chain (no prompt)
 *   keys remove <name>
 *
 * Cache time (B84): `--cache-hours` (only with `--op`; a typed key never expires), else
 * `keys.cache_hours` from the `.code-forge.yml` of the project root of the current directory, else
 * 8 h — whole hours from 1 to 24. `keys set --op` and `keys test` read that config; a missing file
 * is the default, a file that cannot be loaded is the default with one WARN line. An invalid
 * `--cache-hours`, or an invalid `keys.cache_hours` for `set --op`, exits 2 before any 1Password
 * call; `keys test` warns once and uses 8 h for an invalid `keys.cache_hours` (as `jev ask` does).
 * A cached entry is never
 * trusted for longer than those hours (`store.read`).
 *
 * An `--op`/`--ref` value may be a 1Password item ID or item link instead of `op://vault/item/field`:
 * it is resolved to `op://<vaultId>/<itemId>/<fieldId>` first (`op item get`, B25).
 *
 * Every name is validated before any store call, on every subcommand.
 */

import path from 'node:path';
import { writeSafe } from '../util/redact.mjs';
import { DEFAULT_CONFIG_FILENAME, loadConfigFile, projectRootFor } from '../config/load.mjs';
import { describeOpItem, isOpRef, OP_APPROVE_NOTE, OP_MESSAGES, opItemIdFromInput, toOpRef } from '../keys/onepassword.mjs';
import { assertKeyName, CACHE_HOURS_RULE, cacheHoursFrom, createDefaultKeyStore, DEFAULT_CACHE_HOURS, isCacheHours, parseRef, readOpAndCache, resolveKey } from '../keys/store.mjs';

const USAGE = 'usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>\n';

/** The flags each subcommand accepts (list and remove take none); each at most once, any order. */
const FLAGS = /** @type {Record<string, string[]>} */ ({ list: [], set: ['--op', '--cache-hours'], test: ['--ref'], remove: [] });

/**
 * @param {string} text
 * @returns {number | null} the whole number of hours `text` spells (digits only) within the
 *   bounds, else null.
 */
export function parseCacheHours(text) {
  if (!/^[0-9]{1,3}$/.test(text)) {
    return null;
  }
  const hours = Number(text);
  return isCacheHours(hours) ? hours : null;
}

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
 * Strict parse: `<sub> [<name>] [<flag> <value>]...` — the name first, then the subcommand's flags,
 * each at most once, in any order. A flag with no value, a value that looks like a flag, a name
 * that looks like a flag, an unknown or repeated flag, an extra argument, or `--cache-hours`
 * without `--op` (a typed key never expires) is a usage error — never silently reinterpreted.
 * @param {string[]} args
 * @returns {{sub: string, name?: string, flagArg?: string, cacheHoursArg?: string} | null}
 */
export function parseKeysArgs(args) {
  const [sub, ...rest] = args;
  if (typeof sub !== 'string' || !Object.hasOwn(FLAGS, sub)) {
    return null;
  }
  if (sub === 'list') {
    return rest.length === 0 ? { sub } : null;
  }
  const [name, ...flags] = rest;
  if (typeof name !== 'string' || name.startsWith('-') || flags.length % 2 !== 0) {
    return null;
  }
  /** @type {Record<string, string>} */
  const values = {};
  for (let i = 0; i < flags.length; i += 2) {
    const flagName = flags[i];
    const value = flags[i + 1];
    if (!FLAGS[sub].includes(flagName) || Object.hasOwn(values, flagName) || value.length === 0 || value.startsWith('-')) {
      return null;
    }
    values[flagName] = value;
  }
  if (values['--cache-hours'] !== undefined && values['--op'] === undefined) {
    return null;
  }
  const flagArg = sub === 'set' ? values['--op'] : values['--ref'];
  return {
    sub,
    name,
    ...(flagArg !== undefined ? { flagArg } : {}),
    ...(values['--cache-hours'] !== undefined ? { cacheHoursArg: values['--cache-hours'] } : {}),
  };
}

/**
 * The cache time for `keys set --op` / `keys test`: `--cache-hours`, else `keys.cache_hours` from
 * the project config, else 8. An invalid flag fails closed; an invalid `keys.cache_hours` fails
 * closed when `strict` (`set --op`) and otherwise (`test`) gives the default with one warning. A
 * config that cannot be loaded gives the default with one warning (`keys` must keep working beside
 * an unrelated config error).
 * @param {string|undefined} cacheHoursArg
 * @param {string} cwd
 * @param {typeof loadConfigFile} loadConfig
 * @param {boolean} strict - true for `set --op`: an invalid `keys.cache_hours` refuses.
 * @returns {Promise<{ok: true, hours: number, from: string|null, warning: string|null} | {ok: false, message: string}>}
 *   `from` names where hours other than the default came from.
 */
async function cacheHoursFor(cacheHoursArg, cwd, loadConfig, strict) {
  let fromFlag = null;
  if (cacheHoursArg !== undefined) {
    fromFlag = parseCacheHours(cacheHoursArg);
    if (fromFlag === null) {
      return { ok: false, message: `keys: --cache-hours must be ${CACHE_HOURS_RULE}\n` };
    }
  }
  const loaded = await loadConfig(path.join(projectRootFor(cwd), DEFAULT_CONFIG_FILENAME));
  /** @type {string|null} */
  let warning = null;
  /** @type {Record<string, any>} */
  let cfg = {};
  if (loaded.ok) {
    cfg = loaded.config ?? {};
  } else if (loaded.error !== 'not-found') {
    // the error class only, never the loader's message; the WARN names the hours really used
    const used = fromFlag !== null ? `${fromFlag} h from --cache-hours` : `the default ${DEFAULT_CACHE_HOURS} h`;
    warning = `WARN keys: ${DEFAULT_CONFIG_FILENAME} cannot be loaded (${loaded.error}); caching for ${used}\n`;
  }
  const fromConfig = cacheHoursFrom(cfg);
  if (fromConfig.problem !== null) {
    if (strict) {
      return { ok: false, message: `keys: ${fromConfig.problem} (${DEFAULT_CONFIG_FILENAME})\n` };
    }
    // fromConfig.hours is already the default here
    warning = `WARN keys: ${fromConfig.problem} (${DEFAULT_CONFIG_FILENAME}); caching for the default ${fromConfig.hours} h\n`;
  }
  if (fromFlag !== null) {
    return { ok: true, hours: fromFlag, from: '--cache-hours', warning };
  }
  return { ok: true, hours: fromConfig.hours, from: fromConfig.source === 'config' ? 'keys.cache_hours' : null, warning };
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
 * @param {string} [deps.cwd] - the project config is read at the project root of this directory.
 * @param {typeof loadConfigFile} [deps.loadConfig]
 * @returns {Promise<number>}
 */
export async function runKeys(args, { store, stdout = process.stdout, stderr = process.stderr, ask = askHidden, opRead, opExec, env = process.env, now = Date.now(), cwd = process.cwd(), loadConfig = loadConfigFile }) {
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);

  const parsed = parseKeysArgs(args);
  if (!parsed) {
    err(USAGE);
    return 2;
  }
  const { sub, name, cacheHoursArg } = parsed;
  let { flagArg } = parsed;
  try {
    if (name !== undefined) {
      assertKeyName(name);
    }
  } catch (e) {
    err(`keys: ${e.message}\n`);
    return 2;
  }
  // B84: the cache time is settled before any 1Password call (`set --op` and `test` may cache);
  // only `set --op` refuses an invalid keys.cache_hours, `test` warns once and uses 8 h
  /** @type {{hours: number, from: string|null}} */
  let cache = { hours: DEFAULT_CACHE_HOURS, from: null };
  if (sub === 'test' || (sub === 'set' && flagArg !== undefined)) {
    const found = await cacheHoursFor(cacheHoursArg, cwd, loadConfig, sub === 'set');
    if ('message' in found) {
      err(found.message);
      return 2;
    }
    if (found.warning !== null) {
      err(found.warning);
    }
    cache = found;
  }
  const fullRef = flagArg !== undefined && isOpRef(flagArg);
  const itemId = flagArg !== undefined && !fullRef ? opItemIdFromInput(flagArg) : null;
  if (sub === 'set' && flagArg !== undefined && !fullRef && itemId === null) {
    err(`${name}: --op must be a 1Password item ID, item link or op://vault/item/field reference\n`);
    return 2;
  }
  if (flagArg !== undefined && (itemId !== null || fullRef)) err(`${OP_APPROVE_NOTE}\n`);
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
    const res = await resolveKey(name, { store, ref: flagArg, env, opRead, now, cacheHours: cache.hours });
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
    const res = await readOpAndCache(name, flagArg, { store, opRead, now, cacheHours: cache.hours });
    for (const e of res.errors) {
      err(`${name}: ${e}\n`);
    }
    // A null value always carries at least one reason (`readOpAndCache` guarantees it).
    if (res.value === null) {
      return 1;
    }
    const from = cache.from === null ? '' : ` (cache ${cache.hours} h from ${cache.from})`;
    out(`stored ${name} in ${store.writer} (source=op, expires=${formatExpiry(res.exp, now)})${from}\n`);
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
