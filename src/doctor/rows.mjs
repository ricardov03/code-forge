/**
 * Doctor rows (plan §2.3; block B13b): one row per check, `{id, status, label, detail}`.
 * `status` is `OK`, `WARN`, `FAIL` or `INFO`; only a `FAIL` makes `doctor` exit 1. A row's text
 * form is `<STATUS> <label>: <detail>`, so the signer row reads `signer: same-user boundary only`
 * and the tmp row `tmp: <n> stale roots, <m> stale pids`. Details never carry a key or a config
 * value — they name a key path, a count, a CLI or a model id only; every line goes through
 * `redact` on its way out anyway.
 */

import { redact } from '../util/redact.mjs';

/** @typedef {'OK' | 'WARN' | 'FAIL' | 'INFO'} Status */
/** @typedef {{id: string, status: Status, label: string, detail: string}} Row */

export const STATUSES = Object.freeze(['OK', 'WARN', 'FAIL', 'INFO']);

/**
 * @param {string} id @param {Status} status @param {string} label @param {string} detail
 * @returns {Row}
 */
export function row(id, status, label, detail) {
  return { id, status, label, detail };
}

/** @param {Row} r @returns {string} one line, no newline. */
export function renderRow(r) {
  return redact(`${r.status.padEnd(4)} ${r.label}: ${r.detail}`);
}

/** @param {Row[]} rows @returns {string} the text table, one row per line. */
export function renderText(rows) {
  return rows.map((r) => `${renderRow(r)}\n`).join('');
}

/** @param {Row[]} rows @returns {string} ONE JSON document (one line) for agents. */
export function renderJSON(rows) {
  return `${redact(JSON.stringify({ ok: exitCode(rows) === 0, counts: counts(rows), rows }))}\n`;
}

/** @param {Row[]} rows @returns {Record<Status, number>} */
export function counts(rows) {
  /** @type {Record<Status, number>} */
  const out = { OK: 0, WARN: 0, FAIL: 0, INFO: 0 };
  for (const r of rows) out[r.status] += 1;
  return out;
}

/** @param {Row[]} rows @returns {0 | 1} 1 when any row FAILs. */
export function exitCode(rows) {
  return rows.some((r) => r.status === 'FAIL') ? 1 : 0;
}
