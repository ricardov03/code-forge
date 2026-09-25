/**
 * 1Password source: `op read <op://ref>` (plan §8.1). Retried exactly once, and only when the
 * FIRST call timed out (a cold `op` waiting on biometric unlock is the common case); any other
 * failure is final. The value is read from stdout and never logged; a failure message carries
 * the exit code only, never stdout/stderr.
 */

import { exec as realExec } from '../util/exec.mjs';

export const OP_TIMEOUT_MS = 20_000;

/**
 * @param {string} ref
 * @returns {boolean}
 */
export function isOpRef(ref) {
  // Three non-empty segments; spaces are allowed (vault/item names have them, and argv needs no
  // quoting). No line breaks.
  return typeof ref === 'string' && /^op:\/\/[^/\r\n]+\/[^/\r\n]+\/[^\r\n]+$/.test(ref);
}

/**
 * @param {string} ref - an `op://vault/item/field` reference.
 * @param {object} [opts]
 * @param {typeof realExec} [opts.exec]
 * @param {number} [opts.timeoutMs]
 * @returns {Promise<{value: string|null, attempts: number, error?: string}>}
 */
export async function opRead(ref, { exec = realExec, timeoutMs = OP_TIMEOUT_MS } = {}) {
  if (!isOpRef(ref)) {
    throw new TypeError('opRead: reference must look like op://vault/item/field');
  }
  let attempts = 0;
  let res;
  do {
    attempts += 1;
    res = await exec(['op', 'read', ref], { timeoutMs });
  } while (res.result !== 'ok' && res.timedOut && attempts === 1);

  if (res.result !== 'ok') {
    const why = res.timedOut ? 'timed out' : res.code !== null ? `exit ${res.code}` : res.signal ? `signal ${res.signal}` : 'failed';
    return { value: null, attempts, error: `op read failed (${why})` };
  }
  const value = res.stdout.replace(/\r?\n$/, '');
  return value.length > 0 ? { value, attempts } : { value: null, attempts, error: 'op read returned nothing' };
}
