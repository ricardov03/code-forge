/**
 * OS keychain backend over `@napi-rs/keyring` (an optionalDependency, plan §1.1 / §8.1).
 *
 * The module is loaded dynamically, and `available()` proves the keychain is USABLE, not only
 * importable: it reads a probe entry that never exists. A binding that loads on a machine with
 * no usable store (headless Linux without Secret Service, a denied keychain, a CI container)
 * throws there, `available()` resolves `false`, and the store falls back to the macOS `security`
 * CLI, then the 0600 file (see `../store.mjs`).
 *
 * Every entry lives under service `code-forge`, account = key name — the same pair the
 * `security` CLI backend reads.
 *
 * @typedef {import('../store.mjs').Backend} Backend
 */

import { SERVICE } from './constants.mjs';

export { SERVICE };

/** An account name no key can have (key names start with a lowercase letter). */
export const PROBE_ACCOUNT = '__code-forge-probe__';

/** @returns {Promise<any>} the `@napi-rs/keyring` module namespace. */
async function importKeyring() {
  return import('@napi-rs/keyring');
}

/**
 * @param {object} [opts]
 * @param {() => Promise<any>} [opts.loadModule] - injected in tests; never the real keyring there.
 * @param {string} [opts.service]
 * @returns {Backend}
 */
export function createKeychainBackend({ loadModule = importKeyring, service = SERVICE } = {}) {
  /** @type {Promise<any> | undefined} */
  let loading;
  const load = () => {
    loading ??= loadModule().then(
      (mod) => {
        const Entry = mod?.Entry ?? mod?.default?.Entry ?? null;
        if (!Entry) {
          return null;
        }
        try {
          new Entry(service, PROBE_ACCOUNT).getPassword();
          return Entry;
        } catch {
          return null;
        }
      },
      () => null,
    );
    return loading;
  };
  /** @param {string} name */
  const entry = async (name) => {
    const Entry = await load();
    if (!Entry) {
      throw new Error('keychain: @napi-rs/keyring is not available');
    }
    return new Entry(service, name);
  };

  return {
    name: 'keychain',
    writable: true,
    async available() {
      return (await load()) !== null;
    },
    async get(name) {
      const value = (await entry(name)).getPassword();
      return typeof value === 'string' && value.length > 0 ? value : null;
    },
    async set(name, value) {
      (await entry(name)).setPassword(value);
    },
    async delete(name) {
      return Boolean((await entry(name)).deletePassword());
    },
  };
}
