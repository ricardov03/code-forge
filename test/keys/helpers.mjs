/**
 * Test doubles for the key store. Nothing here touches a real keychain or runs `op`.
 *
 * Importing this module points `$HOME` at a fresh temp directory and pins the file backend for
 * the whole test process, so even a mutated `codeForgeHome` / backend pin (Stryker) can only
 * ever reach a temp directory — never the real `~/.code-forge` or the login keychain. Every temp
 * directory is removed after the file's tests.
 */

import { mkdtempSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after } from 'node:test';

/** @type {string[]} */
const created = [];

export const REAL_HOME = os.homedir();
process.env.HOME = mkdtempSync(path.join(os.tmpdir(), 'cf-keys-home-'));
process.env.CODE_FORGE_KEY_BACKEND = 'file';
created.push(process.env.HOME);

after(async () => {
  await Promise.all(created.map((dir) => rm(dir, { recursive: true, force: true })));
});

/** Fake secrets — obviously fake, long enough to be unambiguous when counted in output. */
export const FAKE_KEY = 'FAKE-cf-jev-key-3f9a1c7e5b2d4086';
export const FAKE_OP_KEY = 'FAKE-cf-op-key-8e2d6b0a9c4f1735';
export const FAKE_ENV_KEY = 'FAKE-cf-env-key-1a2b3c4d5e6f7081';
export const FAKE_ASK_KEY = 'FAKE-cf-ask-key-90817263544536aa';

/**
 * @param {string} text
 * @param {string} needle
 * @returns {number}
 */
export function countOccurrences(text, needle) {
  if (typeof needle !== 'string' || needle.length === 0) {
    throw new TypeError('countOccurrences: needle must be a non-empty string');
  }
  return text.split(needle).length - 1;
}

/** @returns {Promise<string>} a fresh temp directory, removed after the file's tests. */
export async function tempHome() {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'cf-keys-'));
  created.push(dir);
  return dir;
}

/**
 * In-memory backend that records every call as `<backend>.<op>:<name>`. A read-only double
 * (`writable: false`) logs and then THROWS on `set`, so a misrouted write fails loudly.
 * @param {string} name
 * @param {object} [opts]
 * @param {Record<string, string>} [opts.data]
 * @param {boolean} [opts.writable]
 * @param {boolean} [opts.available]
 * @param {string[]} [opts.calls] - shared call log
 */
export function memoryBackend(name, { data = {}, writable = true, available = true, calls = [] } = {}) {
  const store = new Map(Object.entries(data));
  return {
    name,
    writable,
    calls,
    store,
    async available() {
      return available;
    },
    async get(/** @type {string} */ key) {
      calls.push(`${name}.get:${key}`);
      return store.has(key) ? store.get(key) : null;
    },
    async set(/** @type {string} */ key, /** @type {string} */ value) {
      calls.push(`${name}.set:${key}`);
      if (!writable) {
        throw new Error(`${name} is read-only`);
      }
      store.set(key, value);
    },
    async delete(/** @type {string} */ key) {
      calls.push(`${name}.delete:${key}`);
      return store.delete(key);
    },
  };
}

/** Collects everything written to it. */
export function sink() {
  return {
    text: '',
    write(/** @type {string} */ chunk) {
      this.text += chunk;
      return true;
    },
  };
}
