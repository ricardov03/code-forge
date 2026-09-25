/**
 * macOS `security` CLI backend — the fallback when `@napi-rs/keyring` cannot load (plan §8.1).
 *
 * READ and DELETE only. Writing through this CLI means `add-generic-password -w <value>`, which
 * puts the secret in argv where any process listing can see it, and B0's `exec` has no stdin
 * input to feed `security -i` instead. So `writable: false`: the store sends writes to the next
 * writable backend (the 0600 file) rather than leak the value through `ps`.
 *
 * It imports nothing from `keychain.mjs`, so it loads even when the keyring binding cannot.
 * The binary is called by absolute path so a `security` earlier on PATH cannot intercept it.
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
 * @returns {Backend}
 */
export function createSecurityCliBackend({ exec = realExec, platform = process.platform, service = SERVICE } = {}) {
  return {
    name: 'security-cli',
    writable: false,
    async available() {
      if (platform !== 'darwin') {
        return false;
      }
      const res = await exec([SECURITY_BIN, 'help'], { timeoutMs: TIMEOUT_MS, okExitCodes: [0, 1] });
      return res.result === 'ok';
    },
    async get(name) {
      const res = await exec([SECURITY_BIN, 'find-generic-password', '-s', service, '-a', name, '-w'], {
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
    async set() {
      throw new Error('security-cli: writes are not supported (the value would be visible in argv)');
    },
    async delete(name) {
      const res = await exec([SECURITY_BIN, 'delete-generic-password', '-s', service, '-a', name], {
        timeoutMs: TIMEOUT_MS,
        okExitCodes: [0, NOT_FOUND_EXIT],
      });
      if (res.result !== 'ok') {
        throw new Error(`security-cli: delete-generic-password failed (exit ${res.code})`);
      }
      return res.code === 0;
    },
  };
}
