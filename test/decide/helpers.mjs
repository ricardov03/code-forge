/**
 * Test doubles for `test/decide/jev-cli.test.mjs`. Importing this module points `$HOME` at a
 * fresh temp directory BEFORE any `src` module loads (rule: a mutated home/path function — or a
 * Stryker mutant of one — can then only ever reach a temp directory, never the real
 * `~/.code-forge`). Every temp directory this file hands out is removed after the test run.
 */

import { mkdtempSync } from 'node:fs';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after } from 'node:test';

export { path };

/** @type {string[]} */
const created = [];

/**
 * Fix round 2 finding: redirecting `HOME` alone is not enough. `os.homedir()` reads
 * `USERPROFILE`, not `HOME`, on Windows — so a HOME-only redirect gives NO isolation there at
 * all — and a future or third-party path resolver in this codebase could follow the XDG base-dir
 * vars instead of `HOME` directly. This set is deliberately wider than "what today's B2/B6 code
 * happens to read" (`os.homedir()` only, on POSIX) — that gap is exactly what fix round 2 found.
 * @param {string} root
 * @returns {Record<string, string>}
 */
function isolationEnv(root) {
  return {
    HOME: root,
    USERPROFILE: root,
    XDG_CONFIG_HOME: path.join(root, '.config'),
    XDG_DATA_HOME: path.join(root, '.local', 'share'),
  };
}

const importTimeHome = mkdtempSync(path.join(os.tmpdir(), 'cf-decide-home-'));
for (const [key, value] of Object.entries(isolationEnv(importTimeHome))) {
  process.env[key] = value;
}
process.env.CODE_FORGE_KEY_BACKEND = 'file';
created.push(importTimeHome);

after(async () => {
  await Promise.all(created.map((dir) => rm(dir, { recursive: true, force: true })));
});

/** @returns {Promise<string>} a fresh temp directory, removed after the test run. */
export async function tempDir() {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'cf-decide-'));
  created.push(dir);
  return dir;
}

/**
 * Point EVERY home/config-dir env var (see {@link isolationEnv}) at a FRESH, dedicated temp dir
 * for the duration of `fn`, then restore each one exactly (`delete`, not `= undefined`, when it
 * was unset going in). This file already redirects these vars once, at IMPORT time, for every
 * test in it — but B2's `createDefaultKeyStore` and B6's `appendRow`/`readAllRows` both call
 * `os.homedir()` directly (neither takes an injectable path), so a test that exercises them FOR
 * REAL has no other way to prove its isolation than mutating the real `process.env` itself.
 * Wrapping that mutation here — one call, one dedicated dir, always restored in a `finally` —
 * makes the isolation visible and self-contained at the ONE call site that needs it (fix round 1
 * BLOCKER: a real-key-store / real-ledger test must never be able to reach `~/.code-forge` even
 * if this file's import-time guard were ever reordered away).
 * @param {(dir: string) => Promise<void>} fn
 */
export async function withIsolatedHome(fn) {
  const dir = await tempDir();
  const overrides = isolationEnv(dir);
  /** @type {Record<string, string|undefined>} */
  const originals = {};
  for (const [key, value] of Object.entries(overrides)) {
    originals[key] = process.env[key];
    process.env[key] = value;
  }
  try {
    await fn(dir);
  } finally {
    for (const [key, original] of Object.entries(originals)) {
      if (original === undefined) {
        delete process.env[key];
      } else {
        process.env[key] = original;
      }
    }
  }
}

/**
 * A MINIMAL, explicit env object for a test that resolves a key/ledger through the REAL B2/B6
 * code — never a `{...process.env, ...}` spread (fix round 2 finding: spreading the real
 * environment can carry a real Jev key under `CODE_FORGE_KEY_JEV`, `TYPESAFE_API_KEY`, or any
 * other name straight into the resolution chain, ahead of the store; the fix the review called
 * "better" is to stop mutating/spreading `process.env` for the key lookup at all). Only `PATH`
 * survives from the real environment (harmless, and kept in case anything ever needs to find a
 * binary); everything else is exactly {@link isolationEnv}'s isolated set plus
 * `CODE_FORGE_KEY_BACKEND: 'file'`. No other var — meaning no possible real API key — can reach
 * `resolveKey`'s `env[envName]` lookup through this object.
 * @param {string} root
 * @returns {NodeJS.ProcessEnv}
 */
export function isolatedEnvFor(root) {
  return { PATH: process.env.PATH, ...isolationEnv(root), CODE_FORGE_KEY_BACKEND: 'file' };
}

/**
 * @param {string} dir @param {string} name @param {string} content
 * @returns {Promise<string>} the written file's full path.
 */
export async function writeTempFile(dir, name, content) {
  const full = path.join(dir, name);
  await writeFile(full, content, 'utf8');
  return full;
}

/** @returns {{write: (s: string) => boolean, text: string}} */
export function sink() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

/** Fake secrets — obviously fake, long enough to be unambiguous when counted in output. */
export const FAKE_KEY = 'FAKE-cf-decide-jev-key-7a1e9c3f5b0d2684';

/**
 * A minimal in-memory `KeyStore` double satisfying exactly the surface `resolveKey` uses
 * (`read`/`put`) — no real backend, no real file I/O.
 * @param {Record<string, {value: string, exp: number|null}>} [data]
 */
export function memoryStore(data = {}) {
  return {
    warning: null,
    writer: 'memory',
    backends: ['memory'],
    async read(name, now) {
      const entry = data[name];
      if (!entry) return null;
      if (entry.exp !== null && entry.exp <= now) {
        delete data[name];
        return null;
      }
      return { value: entry.value, exp: entry.exp, backend: 'memory' };
    },
    async put(name, value, { exp }) {
      data[name] = { value, exp };
    },
    async remove(name) {
      const had = name in data;
      delete data[name];
      return had ? 1 : 0;
    },
    async list() {
      return Object.keys(data).map((name) => ({ name, source: 'user', backend: 'memory', exp: data[name].exp }));
    },
  };
}
