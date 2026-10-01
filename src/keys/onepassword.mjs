/**
 * 1Password source: `op read <op://ref>` (plan §8.1), and `op item get <id> --format json` to turn
 * a bare item ID or item link into a full `op://<vaultId>/<itemId>/<fieldId>` reference (B25).
 *
 * Every `op` call is retried exactly once, and only when the FIRST call timed out (a cold `op`
 * waiting on biometric unlock is the common case); any other failure is final. A failure is
 * classified into one of a few kinds (`OP_ERROR_KINDS`) from the exit code, ENOENT and the timeout
 * flag; well-known stderr phrases are matched for classification ONLY. No message ever carries
 * stdout or stderr: `op item get` stdout holds the item's secret values, so only the vault ID, the
 * item ID and field id/label/type are taken from it, and the parsed object is dropped once the
 * reference is built.
 */

import { reportErrorKind } from '../util/error-kind.mjs';
import { exec as realExec } from '../util/exec.mjs';

// Long enough for a person to see and approve the 1Password prompt (Touch ID or password).
export const OP_TIMEOUT_MS = 60_000;

/** Shown just before a call that may make 1Password ask for approval. */
export const OP_APPROVE_NOTE = '1Password may ask you to approve access: check the 1Password window (Touch ID or password).';

/** The 1Password failure kinds; agent JSON carries the kind. */
export const OP_ERROR_KINDS = Object.freeze(['op_missing', 'op_locked', 'op_timeout', 'op_not_found', 'op_bad_output', 'op_no_field', 'op_bad_input', 'op_failed']);

/**
 * The fixed user-facing messages, one per kind. None of them carries `op` output. `op_not_found`
 * may get the item ID as a suffix, `op_no_field` the (sanitised) concealed field labels, and
 * `op_failed` is built by {@link opFailedMessage} from the exit code or signal only.
 */
export const OP_MESSAGES = Object.freeze({
  op_missing: '1Password CLI not found; install it with: brew install 1password-cli',
  op_locked: '1Password is locked or not signed in; unlock the app (or run `op signin`) and try again',
  op_timeout: '1Password did not answer in time; unlock the app and try again',
  op_not_found: '1Password item or field not found, or no access',
  op_bad_output: 'unexpected answer from op',
  op_no_field: 'the 1Password item has no single key field; pass a full op://vault/item/field reference',
  op_bad_input: 'not an op:// reference, 1Password item ID or item link',
  op_failed: '1Password CLI failed',
});

/** Hosts a 1Password item link may come from (the host itself or a subdomain). */
const LINK_HOSTS = ['1password.com', '1password.ca', '1password.eu'];

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
 * @param {unknown} s
 * @returns {boolean} a 1Password item ID: 26 lowercase letters and digits.
 */
export function isOpItemId(s) {
  return typeof s === 'string' && /^[a-z0-9]{26}$/.test(s);
}

/**
 * The item ID in a bare ID or a 1Password item link (`https://start.1password.com/open/i?…&i=<id>`).
 * @param {unknown} s
 * @returns {string|null}
 */
export function opItemIdFromInput(s) {
  if (typeof s !== 'string') return null;
  const text = s.trim();
  if (isOpItemId(text)) return text;
  if (!/^https:\/\//i.test(text)) return null;
  let url;
  try {
    url = new URL(text);
  } catch {
    return null;
  }
  if (url.protocol !== 'https:') return null;
  const host = url.hostname.toLowerCase();
  if (!LINK_HOSTS.some((h) => host === h || host.endsWith(`.${h}`))) return null;
  const id = url.searchParams.get('i');
  return isOpItemId(id) ? id : null;
}

/**
 * @param {unknown} s
 * @returns {boolean} whether `s` has the shape of something {@link toOpRef} accepts (no `op` call).
 */
export function isOpInput(s) {
  return (typeof s === 'string' && isOpRef(s.trim())) || opItemIdFromInput(s) !== null;
}

/** stderr phrases `op` prints when it is locked, signed out or cannot reach the desktop app. */
const LOCKED = /not (currently )?signed in|signin|sign in|locked|unlock|session (has )?expired|desktop app|biometric/i;
/** stderr phrases `op` prints when an item, vault or field does not exist or is not shared with you. */
const NOT_FOUND = /isn't an item|isn't a vault|isn't a field|no item found|(item|vault|field) not found|could not find (the )?(item|vault|field)|not authori[sz]ed|unauthori[sz]ed|no access|permission/i;

/**
 * Classify a failed `op` call. stderr is matched, never returned. Sign-in wording wins over
 * not-found wording; anything unrecognised is `op_failed`.
 * @param {{code: number|null, timedOut: boolean, stderr?: string, error?: string}} res
 * @returns {'op_missing'|'op_locked'|'op_timeout'|'op_not_found'|'op_failed'}
 */
export function classifyOpFailure(res) {
  if (res.timedOut) return 'op_timeout';
  if (typeof res.error === 'string' && /ENOENT/.test(res.error)) return 'op_missing';
  if (res.code === 127) return 'op_missing';
  const stderr = typeof res.stderr === 'string' ? res.stderr : '';
  if (LOCKED.test(stderr)) return 'op_locked';
  if (NOT_FOUND.test(stderr)) return 'op_not_found';
  return 'op_failed';
}

/**
 * The `op_failed` message: exit code or signal only, plus the command to run by hand.
 * @param {{code: number|null, signal?: string|null}} res
 * @param {string} command - e.g. `op item get <id>` (IDs/references only, never output).
 * @returns {string}
 */
export function opFailedMessage(res, command) {
  const how = res.code !== null && res.code !== undefined ? `exit ${res.code}` : res.signal ? `signal ${res.signal}` : 'no exit code';
  return `${OP_MESSAGES.op_failed} (${how}); run \`${command}\` yourself to see why`;
}

/**
 * @param {'op_missing'|'op_locked'|'op_timeout'|'op_not_found'|'op_failed'} kind
 * @param {{code: number|null, signal?: string|null}} res
 * @param {string} command
 * @param {string} [notFoundSuffix]
 * @returns {string}
 */
function failureMessage(kind, res, command, notFoundSuffix = '') {
  if (kind === 'op_failed') return opFailedMessage(res, command);
  if (kind === 'op_not_found') return `${OP_MESSAGES.op_not_found}${notFoundSuffix}`;
  return OP_MESSAGES[kind];
}

/**
 * A field label made safe for a message: control characters removed, at most 64 characters,
 * JSON-quoted.
 * @param {unknown} label
 * @returns {string}
 */
function safeLabel(label) {
  return JSON.stringify(cleanText(label));
}

/**
 * Text from `op` output (a title, vault name or field label) made safe to print: control
 * characters removed, at most 64 characters.
 * @param {unknown} text
 * @returns {string}
 */
function cleanText(text) {
  // eslint-disable-next-line no-control-regex
  return String(text).replace(/[\x00-\x1f\x7f]/g, '').slice(0, 64);
}

/** At most this many labels are listed in an `op_no_field` message. */
const MAX_LABELS = 10;

/**
 * @param {string[]} argv
 * @param {typeof realExec} exec
 * @param {number} timeoutMs
 */
async function runOp(argv, exec, timeoutMs) {
  let attempts = 0;
  let res;
  do {
    attempts += 1;
    res = await exec(argv, { timeoutMs });
  } while (res.result !== 'ok' && res.timedOut && attempts === 1);
  return { res, attempts };
}

/**
 * @param {string} ref - an `op://vault/item/field` reference.
 * @param {object} [opts]
 * @param {typeof realExec} [opts.exec]
 * @param {number} [opts.timeoutMs]
 * @returns {Promise<{value: string|null, attempts: number, error?: string, kind?: string}>}
 */
export async function opRead(ref, { exec = realExec, timeoutMs = OP_TIMEOUT_MS } = {}) {
  if (!isOpRef(ref)) {
    throw new TypeError('opRead: reference must look like op://vault/item/field');
  }
  const { res, attempts } = await runOp(['op', 'read', ref], exec, timeoutMs);
  if (res.result !== 'ok') {
    const kind = classifyOpFailure(res);
    reportErrorKind(kind); // the error log's kind if the verb then fails (B27)
    return { value: null, attempts, error: failureMessage(kind, res, `op read ${ref}`, '; check the reference'), kind };
  }
  const value = res.stdout.replace(/\r?\n$/, '');
  if (value.length > 0) return { value, attempts };
  reportErrorKind('op_bad_output');
  return { value: null, attempts, error: OP_MESSAGES.op_bad_output, kind: 'op_bad_output' };
}

/**
 * @typedef {{ref: string|null, error?: string, kind?: string, field?: string, vault?: string, title?: string}} OpRefResult
 *   `ref` is null exactly when `error` and `kind` are set.
 */

/**
 * Pick the key field: id `credential`; else purpose PASSWORD; else the single CONCEALED field
 * with a value.
 * @param {any[]} fields
 * @returns {{field: any} | {error: string}}
 */
function pickField(fields) {
  const hasValue = (/** @type {any} */ f) => typeof f?.value === 'string' && f.value.length > 0;
  const byId = fields.find((f) => f?.id === 'credential' && hasValue(f));
  if (byId) return { field: byId };
  const byPurpose = fields.find((f) => f?.purpose === 'PASSWORD');
  if (byPurpose) return { field: byPurpose };
  const concealed = fields.filter((f) => f?.type === 'CONCEALED');
  const filled = concealed.filter(hasValue);
  if (filled.length === 1) return { field: filled[0] };
  const labels = concealed.slice(0, MAX_LABELS).map((f) => safeLabel(typeof f.label === 'string' ? f.label : f.id));
  const more = concealed.length > MAX_LABELS ? ` and ${concealed.length - MAX_LABELS} more` : '';
  const which = labels.length > 0 ? `concealed fields: ${labels.join(', ')}${more}` : 'no concealed field';
  return { error: `${OP_MESSAGES.op_no_field} (${which})` };
}

/**
 * Resolve a 1Password item ID into `op://<vaultId>/<itemId>/<fieldId>`.
 * @param {string} itemId
 * @param {object} [opts]
 * @param {typeof realExec} [opts.exec]
 * @param {number} [opts.timeoutMs]
 * @returns {Promise<OpRefResult>}
 */
export async function resolveOpItemRef(itemId, { exec = realExec, timeoutMs = OP_TIMEOUT_MS } = {}) {
  if (!isOpItemId(itemId)) return { ref: null, error: OP_MESSAGES.op_bad_input, kind: 'op_bad_input' };
  const { res } = await runOp(['op', 'item', 'get', itemId, '--format', 'json'], exec, timeoutMs);
  if (res.result !== 'ok') {
    const kind = classifyOpFailure(res);
    return { ref: null, error: failureMessage(kind, res, `op item get ${itemId}`, ` (item ${itemId})`), kind };
  }
  /** @type {any} */
  let item;
  try {
    item = JSON.parse(res.stdout);
  } catch {
    // never pass the parser's message on: it can quote the input
    item = null;
  }
  const vaultId = item?.vault?.id;
  const fields = item?.fields;
  const id = item?.id;
  if (typeof vaultId !== 'string' || !/^[a-z0-9]+$/.test(vaultId) || typeof id !== 'string' || id !== itemId || !Array.isArray(fields)) {
    return { ref: null, error: OP_MESSAGES.op_bad_output, kind: 'op_bad_output' };
  }
  const picked = pickField(fields);
  if ('error' in picked) return { ref: null, error: picked.error, kind: 'op_no_field' };
  const fieldId = picked.field.id;
  if (typeof fieldId !== 'string' || fieldId.length === 0 || /[/\r\n]/.test(fieldId)) {
    return { ref: null, error: OP_MESSAGES.op_bad_output, kind: 'op_bad_output' };
  }
  const label = typeof picked.field.label === 'string' && picked.field.label.length > 0 ? picked.field.label : fieldId;
  return {
    ref: `op://${vaultId}/${itemId}/${fieldId}`,
    field: cleanText(label),
    vault: cleanText(typeof item.vault.name === 'string' ? item.vault.name : vaultId),
    title: cleanText(typeof item.title === 'string' ? item.title : itemId),
  };
}

/**
 * An `op://` reference passes unchanged (no `op` call); an item ID or link is resolved.
 * @param {string} input
 * @param {object} [opts]
 * @param {typeof realExec} [opts.exec]
 * @param {number} [opts.timeoutMs]
 * @returns {Promise<OpRefResult>}
 */
export async function toOpRef(input, opts = {}) {
  const text = typeof input === 'string' ? input.trim() : input;
  if (isOpRef(text)) return { ref: /** @type {string} */ (text) };
  const id = opItemIdFromInput(text);
  /** @type {OpRefResult} */
  const res = id !== null ? await resolveOpItemRef(id, opts) : { ref: null, error: OP_MESSAGES.op_bad_input, kind: 'op_bad_input' };
  if (res.ref === null) reportErrorKind(res.kind); // the error log's kind if the verb then fails (B27)
  return res;
}

/**
 * `1Password item "<title>" (vault <name>, field <label>)`, leaving out any part that is missing;
 * null when nothing is known (a plain op:// input).
 * @param {{title?: string, vault?: string, field?: string}} res
 * @returns {string|null}
 */
export function describeOpItem(res) {
  const parts = [res.vault ? `vault ${res.vault}` : null, res.field ? `field ${res.field}` : null].filter(Boolean);
  if (!res.title && parts.length === 0) return null;
  return `1Password item${res.title ? ` "${res.title}"` : ''}${parts.length > 0 ? ` (${parts.join(', ')})` : ''}`;
}
