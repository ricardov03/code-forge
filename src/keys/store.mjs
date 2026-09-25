/**
 * The key store and the lookup chain (plan §8.1, D12).
 *
 * Chain, in this order, first hit wins:
 *   1. env var       — `env:NAME` from the reference, else `CODE_FORGE_KEY_<NAME>`
 *   2. OS keychain   — this store (`@napi-rs/keyring` → macOS `security` CLI → 0600 file)
 *   3. 1Password     — `op read <op://…>` (retry once on a first-call timeout); a hit is cached
 *                      into the store as `{"v", "exp"}` for `cacheHours` (default 8)
 *   4. ask           — hidden input, only when the caller passes `ask` (setup only)
 * An entry whose `exp` has passed is deleted on that first read and treated as absent, so the
 * chain moves on to 1Password and refreshes it. A backend that fails to read is skipped with a
 * non-secret reason (backend name + error code) in `errors`; the chain never aborts on it.
 *
 * `source` names where the value came from: `env`, the store backend that held it (`keychain`,
 * `security-cli`, `file`), `op`, or `ask`.
 *
 * Every value this module obtains is handed to `registerSecret` before it is returned, so every
 * redacted output path (`util/log`, `util/redact`) masks it from then on. Nothing here prints.
 *
 * Alongside the secrets the store keeps `index.json` (0600, no values): name → source, backend,
 * exp — what `keys list` shows.
 *
 * @typedef {object} Backend
 * @property {string} name
 * @property {boolean} writable
 * @property {() => Promise<boolean>} available
 * @property {(name: string) => Promise<string|null>} get
 * @property {(name: string, value: string) => Promise<void>} set
 * @property {(name: string) => Promise<boolean>} delete
 *
 * @typedef {{name: string, source: string, backend: string, exp: number|null}} IndexRow
 * @typedef {{value: string|null, source: string|null, exp: number|null, errors: string[]}} Resolution
 */

import { readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { registerSecret } from '../util/redact.mjs';
import { createFileBackend, writePrivateFile } from './backends/file.mjs';
import { createKeychainBackend } from './backends/keychain.mjs';
import { createSecurityCliBackend } from './backends/security-cli.mjs';
import { isOpRef, opRead as realOpRead } from './onepassword.mjs';

export const DEFAULT_CACHE_HOURS = 8;
const HOUR_MS = 3_600_000;
const NAME_PATTERN = /^[a-z][a-z0-9_-]{0,63}$/;

export const FILE_FALLBACK_WARNING =
  'keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store';

/**
 * @param {string} name
 * @returns {string}
 */
export function assertKeyName(name) {
  if (typeof name !== 'string' || !NAME_PATTERN.test(name)) {
    throw new TypeError('key name must match /^[a-z][a-z0-9_-]{0,63}$/');
  }
  return name;
}

/**
 * @param {string} name
 * @param {string} [ref] - `user` | `op://…` | `env:NAME` | `keychain:<name>`
 * @returns {{envName: string, storeName: string, opRef: string|null}}
 */
export function parseRef(name, ref) {
  assertKeyName(name);
  const envName = ref?.startsWith('env:') ? ref.slice(4) : `CODE_FORGE_KEY_${name.toUpperCase().replace(/-/g, '_')}`;
  const storeName = ref?.startsWith('keychain:') ? assertKeyName(ref.slice(9)) : name;
  if (ref?.startsWith('op://') && !isOpRef(ref)) {
    throw new TypeError(`key "${name}": malformed 1Password reference`);
  }
  return { envName, storeName, opRef: ref?.startsWith('op://') ? ref : null };
}

/**
 * @param {string} value
 * @param {number|null} exp
 * @returns {string}
 */
export function encodeEntry(value, exp) {
  return JSON.stringify({ v: value, exp });
}

/**
 * A stored string that is not our `{v, exp}` JSON (written by hand, or by another tool) is taken
 * as a raw value with no expiry.
 * @param {string} raw
 * @returns {{value: string, exp: number|null}}
 */
export function decodeEntry(raw) {
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed.v === 'string') {
      return { value: parsed.v, exp: Number.isFinite(parsed.exp) ? parsed.exp : null };
    }
  } catch {
    // not JSON — fall through to raw
  }
  return { value: raw, exp: null };
}

/**
 * @param {unknown} err
 * @returns {string} a reason that can never carry a value: the error's code or class name only.
 */
function reasonOf(err) {
  const e = /** @type {{code?: unknown, name?: unknown}} */ (err ?? {});
  return typeof e.code === 'string' ? e.code : typeof e.name === 'string' ? e.name : 'Error';
}

/**
 * @param {NodeJS.ProcessEnv} [env]
 * @returns {string}
 */
export function codeForgeHome(env = process.env) {
  return path.join(env.HOME || os.homedir(), '.code-forge');
}

/**
 * @param {object} opts
 * @param {Backend[]} opts.backends - candidates, most preferred first; unavailable ones are dropped.
 * @param {string} opts.dir - where `index.json` lives.
 */
export async function createKeyStore({ backends, dir }) {
  /** @type {Backend[]} */
  const live = [];
  for (const backend of backends) {
    if (await backend.available().catch(() => false)) {
      live.push(backend);
    }
  }
  const writer = live.find((b) => b.writable) ?? null;
  // The writer is read first: a fresh value cached there must win over a stale copy that a
  // read-only backend still holds.
  const readOrder = writer ? [writer, ...live.filter((b) => b !== writer)] : live;
  const indexFile = path.join(dir, 'index.json');

  /** @returns {Promise<Record<string, Omit<IndexRow, 'name'>>>} a missing or corrupt index reads as empty */
  const readIndex = async () => {
    try {
      const parsed = JSON.parse(await readFile(indexFile, 'utf8'));
      return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
    } catch (err) {
      if (err?.code === 'ENOENT' || err instanceof SyntaxError) {
        return {};
      }
      throw err;
    }
  };
  /** @param {(index: Record<string, Omit<IndexRow, 'name'>>) => void} change */
  const updateIndex = async (change) => {
    const index = await readIndex();
    change(index);
    await writePrivateFile(indexFile, JSON.stringify(index, null, 2));
  };

  return {
    backends: live.map((b) => b.name),
    writer: writer?.name ?? null,
    warning: writer?.name === 'file' ? FILE_FALLBACK_WARNING : null,

    /**
     * @param {string} name
     * @param {number} now
     * @param {string[]} [errors] - receives one non-secret reason per backend that failed.
     * @returns {Promise<{value: string, exp: number|null, backend: string}|null>}
     */
    async read(name, now, errors = []) {
      assertKeyName(name);
      for (const backend of readOrder) {
        let raw;
        try {
          raw = await backend.get(name);
        } catch (err) {
          errors.push(`${backend.name}: read failed (${reasonOf(err)})`);
          continue;
        }
        if (raw === null) {
          continue;
        }
        const entry = decodeEntry(raw);
        registerSecret(entry.value);
        if (entry.exp !== null && entry.exp <= now) {
          await backend.delete(name).catch((err) => {
            errors.push(`${backend.name}: delete of expired entry failed (${reasonOf(err)})`);
          });
          await updateIndex((index) => {
            delete index[name];
          });
          return null;
        }
        return { ...entry, backend: backend.name };
      }
      return null;
    },

    /**
     * @param {string} name
     * @param {string} value
     * @param {{source: string, exp: number|null}} meta
     */
    async put(name, value, { source, exp }) {
      assertKeyName(name);
      if (!writer) {
        throw new Error('keys: no writable backend');
      }
      registerSecret(value);
      await writer.set(name, encodeEntry(value, exp));
      await updateIndex((index) => {
        index[name] = { source, backend: writer.name, exp };
      });
    },

    /** @param {string} name @returns {Promise<number>} how many backends held it */
    async remove(name) {
      assertKeyName(name);
      let removed = 0;
      for (const backend of live) {
        if (await backend.delete(name).catch(() => false)) {
          removed += 1;
        }
      }
      await updateIndex((index) => {
        delete index[name];
      });
      return removed;
    },

    /** @returns {Promise<IndexRow[]>} metadata only — never a value */
    async list() {
      const index = await readIndex();
      return Object.keys(index)
        .sort()
        .map((name) => ({
          name,
          source: String(index[name].source),
          backend: String(index[name].backend),
          exp: index[name].exp ?? null,
        }));
    },
  };
}

/** @typedef {Awaited<ReturnType<typeof createKeyStore>>} KeyStore */

/**
 * The production store: keyring → `security` CLI → 0600 file. `CODE_FORGE_KEY_BACKEND=file`
 * pins the file backend (CI, containers, tests). The keychain factories are injectable so a
 * test never constructs a real keychain backend.
 *
 * @param {NodeJS.ProcessEnv} [env]
 * @param {object} [factories]
 * @param {() => Backend} [factories.keychain]
 * @param {() => Backend} [factories.securityCli]
 * @returns {Promise<KeyStore>}
 */
export function createDefaultKeyStore(
  env = process.env,
  { keychain = () => createKeychainBackend(), securityCli = () => createSecurityCliBackend() } = {},
) {
  const dir = path.join(codeForgeHome(env), 'store');
  const file = createFileBackend({ dir });
  const backends = env.CODE_FORGE_KEY_BACKEND === 'file' ? [file] : [keychain(), securityCli(), file];
  return createKeyStore({ backends, dir });
}

/**
 * Read a key from 1Password and cache it into the store as `{v, exp}` for `cacheHours`. A cache
 * write failure is reported in `errors` but does not lose the value just read.
 *
 * @param {string} storeName
 * @param {string} opRef
 * @param {object} deps
 * @param {KeyStore} deps.store
 * @param {typeof realOpRead} [deps.opRead]
 * @param {number} [deps.now]
 * @param {number} [deps.cacheHours]
 * @returns {Promise<Resolution>}
 */
export async function readOpAndCache(storeName, opRef, { store, opRead = realOpRead, now = Date.now(), cacheHours = DEFAULT_CACHE_HOURS }) {
  const res = await opRead(opRef);
  if (res.value === null) {
    return { value: null, source: null, exp: null, errors: [res.error ?? 'op read failed'] };
  }
  registerSecret(res.value);
  const exp = now + cacheHours * HOUR_MS;
  /** @type {string[]} */
  const errors = [];
  await store.put(storeName, res.value, { source: 'op', exp }).catch((err) => {
    errors.push(`cache write failed (${reasonOf(err)})`);
  });
  return { value: res.value, source: 'op', exp, errors };
}

/**
 * Resolve one key through the chain. Returns the value with its source; never prints it.
 *
 * @param {string} name
 * @param {object} deps
 * @param {KeyStore} deps.store
 * @param {string} [deps.ref]
 * @param {NodeJS.ProcessEnv} [deps.env]
 * @param {typeof realOpRead} [deps.opRead]
 * @param {(name: string) => Promise<string|null>} [deps.ask] - setup only; omitted everywhere else.
 * @param {number} [deps.now]
 * @param {number} [deps.cacheHours]
 * @returns {Promise<Resolution>}
 */
export async function resolveKey(name, { store, ref, env = process.env, opRead = realOpRead, ask, now = Date.now(), cacheHours = DEFAULT_CACHE_HOURS }) {
  const { envName, storeName, opRef } = parseRef(name, ref);
  /** @type {string[]} */
  const errors = [];

  const fromEnv = env[envName];
  if (typeof fromEnv === 'string' && fromEnv.length > 0) {
    registerSecret(fromEnv);
    return { value: fromEnv, source: 'env', exp: null, errors };
  }

  const cached = await store.read(storeName, now, errors);
  if (cached) {
    return { value: cached.value, source: cached.backend, exp: cached.exp, errors };
  }

  if (opRef) {
    const fromOp = await readOpAndCache(storeName, opRef, { store, opRead, now, cacheHours });
    errors.push(...fromOp.errors);
    if (fromOp.value !== null) {
      return { ...fromOp, errors };
    }
  }

  if (ask) {
    const typed = await ask(name);
    if (typeof typed === 'string' && typed.length > 0) {
      registerSecret(typed);
      await store.put(storeName, typed, { source: 'user', exp: null }).catch((err) => {
        errors.push(`cache write failed (${reasonOf(err)})`);
      });
      return { value: typed, source: 'ask', exp: null, errors };
    }
  }

  return { value: null, source: null, exp: null, errors };
}
