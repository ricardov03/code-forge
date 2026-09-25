/**
 * macOS `security` CLI backend — the fallback when `@napi-rs/keyring` cannot load (plan §8.1).
 *
 * Read, write and delete. A write never puts the secret in argv (where any process listing
 * would show it): `add-generic-password … -w` with `-w` as the LAST option and no value makes
 * `security` prompt for the password, and since `exec` spawns the child in a new session with
 * no controlling terminal, the prompt reads from stdin. The value is piped there twice (password,
 * then the "retype" confirmation), one per line, via B0.1 `exec`'s `input` (B2.1). A value that
 * contains a line break cannot be sent that way and is refused before anything spawns. After the
 * write the item is read back; a stored value that differs (truncated, empty) is deleted and
 * the write fails.
 *
 * It imports nothing from `keychain.mjs`, so it loads even when the keyring binding cannot.
 * The binary is called by absolute path so a `security` earlier on PATH cannot intercept it
 * (`bin` exists only so tests can point it at a fake).
 *
 * Values: code-forge stores printable JSON text, which `-w` prints as-is. An item holding
 * non-printable bytes is printed by `security` as hex; such items are not code-forge's and are
 * returned as that text (documented limitation). An empty password reads as absent.
 *
 * @typedef {import('../store.mjs').Backend} Backend
 */

import { exec as realExec } from '../../util/exec.mjs';
import { SERVICE } from './constants.mjs';

export const SECURITY_BIN = '/usr/bin/security';

/** `security` exits 44 when no matching item exists. */
export const NOT_FOUND_EXIT = 44;

const TIMEOUT_MS = 10_000;

/**
 * @param {object} [opts]
 * @param {typeof realExec} [opts.exec]
 * @param {string} [opts.platform]
 * @param {string} [opts.service]
 * @param {string} [opts.bin] - path of the `security` binary (default {@link SECURITY_BIN}).
 * @returns {Backend}
 */
export function createSecurityCliBackend({
  exec = realExec,
  platform = process.platform,
  service = SERVICE,
  bin = SECURITY_BIN,
} = {}) {
  /** @type {Backend} */
  const backend = {
    name: 'security-cli',
    writable: true,
    async available() {
      if (platform !== 'darwin') {
        return false;
      }
      const res = await exec([bin, 'help'], { timeoutMs: TIMEOUT_MS, okExitCodes: [0, 1] });
      return res.result === 'ok';
    },
    async get(name) {
      const res = await exec([bin, 'find-generic-password', '-s', service, '-a', name, '-w'], {
        timeoutMs: TIMEOUT_MS,
        okExitCodes: [0, NOT_FOUND_EXIT],
      });
      if (res.result !== 'ok') {
        throw new Error(`security-cli: find-generic-password failed (exit ${res.code})`);
      }
      if (res.code === NOT_FOUND_EXIT) {
        return null;
      }
      const value = res.stdout.replace(/\n$/, '');
      return value.length > 0 ? value : null;
    },
    async set(name, value) {
      if (/[\r\n]/.test(value)) {
        throw new TypeError('security-cli: a value containing a line break cannot be written through stdin');
      }
      // `-U` updates an existing item; `-w` must stay last so `security` reads the value from stdin.
      const res = await exec([bin, 'add-generic-password', '-U', '-s', service, '-a', name, '-w'], {
        timeoutMs: TIMEOUT_MS,
        input: `${value}\n${value}\n`,
      });
      if (res.result !== 'ok') {
        throw new Error(`security-cli: add-generic-password failed (exit ${res.code})`);
      }
      // A clean exit is not proof: the prompt (getpass/readpassphrase) can truncate long input
      // or store an empty value and still exit 0. Read the item back and compare.
      if ((await backend.get(name)) !== value) {
        // Best effort: drop the bad item so a truncated secret is never served later.
        await backend.delete(name).catch(() => false);
        throw new Error('security-cli: add-generic-password stored value mismatch');
      }
    },
    async delete(name) {
      const res = await exec([bin, 'delete-generic-password', '-s', service, '-a', name], {
        timeoutMs: TIMEOUT_MS,
        okExitCodes: [0, NOT_FOUND_EXIT],
      });
      if (res.result !== 'ok') {
        throw new Error(`security-cli: delete-generic-password failed (exit ${res.code})`);
      }
      return res.code === 0;
    },
  };
  return backend;
}
