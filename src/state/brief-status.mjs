/**
 * B64a (issue #13 item 6): the short status of one run — the data `code-forge status` prints, as
 * text or as one JSON line. A pure function: it reads the run record and the ledger rows it is
 * handed and nothing else (no file, no lock, no row written); the CLI (`src/cli/status.mjs`) reads
 * them and renders the result. The object it returns has exactly the keys of the `--json` run
 * object, in order: run_id, status, mode, spent_usd, unknown_usd_sessions, budget_usd, blocks,
 * more, closed, stopped.
 *
 * Per run: its status (null when the record has none — `run status` then prints no status either;
 * the text shows `-`), its mode (`brief` for a `run start --mode brief` run, else `plan`), the
 * spend (`runSpend`, the same numbers `run status` prints as spent_usd and unknown_usd_sessions;
 * both null when the caller could not read the ledger, `rows: null`) and the budget the caller
 * read, then one entry per block that is neither closed nor stopped — closed and stopped blocks are
 * only counted. A block entry holds:
 *  - `state`: `open` on its first attempt, `attempt-<n>` after `block attempt` raised it to n; a
 *    block whose record status is anything other than open shows that status as its state;
 *  - `level`: the level it was dispatched at (the run record);
 *  - `model`: `<provider>/<model>` (or the model alone) of the block's latest coder `session` /
 *    `session.background` row of the run that names a model — the rows `src/session/spawn.mjs`
 *    writes carry `role`, `provider`, `model`; a `ledger add coder` row (`manual`, no model) is
 *    skipped — null when the ledger names none;
 *  - `gate`: the block's LAST gate run — `gate.done` says pass (`all_ok: true`) or fail, a
 *    `gate.red` with no `gate.done` after it is a fail; the name is the failing gate (the last
 *    `gate.red` of that run), null on a pass; `{result: 'none', name: null}` when no gate ran;
 *  - `findings`: the open review findings of the block, counted by the caller's `findingsOf`
 *    (the CLI counts what the block close gate would refuse, `src/review/gate-check.mjs`).
 * Blocks are in block id order (numeric-aware: B2 before B10).
 */

import { runSpend } from '../ledger/spend.mjs';

/**
 * @typedef {{id: string, state: string, level: string | null, model: string | null, gate: {result: 'pass' | 'fail' | 'none', name: string | null}, findings: number}} BlockStatus
 * @typedef {{run_id: string, status: string | null, mode: 'plan' | 'brief', spent_usd: number | null, unknown_usd_sessions: number | null, budget_usd: number | null, blocks: BlockStatus[], more: number, closed: number, stopped: number}} RunStatus
 */

/** @param {string} a @param {string} b @returns {number} */
const byBlockId = (a, b) => a.localeCompare(b, 'en', { numeric: true }) || (a < b ? -1 : a > b ? 1 : 0);

/**
 * The block's last gate run (see the module doc).
 * @param {Array<Record<string, any>>} mine - the block's rows of the run, in ledger order.
 * @returns {{result: 'pass' | 'fail' | 'none', name: string | null}}
 */
export function lastGate(mine) {
  /** @type {string | null} */
  let red = null;
  /** @type {{result: 'pass' | 'fail' | 'none', name: string | null}} */
  let last = { result: 'none', name: null };
  for (const r of mine) {
    if (r.event === 'gate.red') {
      red = typeof r.gate === 'string' ? r.gate : null;
      last = { result: 'fail', name: red };
    } else if (r.event === 'gate.done') {
      last = r.all_ok === true ? { result: 'pass', name: null } : { result: 'fail', name: red };
      red = null;
    }
  }
  return last;
}

/**
 * The engine/model of the block's latest coder session row, or null.
 * @param {Array<Record<string, any>>} mine @returns {string | null}
 */
export function coderModel(mine) {
  for (let i = mine.length - 1; i >= 0; i -= 1) {
    const r = mine[i];
    if ((r.event === 'session' || r.event === 'session.background') && r.role === 'coder' && typeof r.model === 'string' && r.model.length > 0) {
      return typeof r.provider === 'string' && r.provider.length > 0 ? `${r.provider}/${r.model}` : r.model;
    }
  }
  return null;
}

/**
 * @param {{
 *   record: Record<string, any>, rows: Array<Record<string, any>> | null, budget: number | null,
 *   findingsOf: (blockId: string) => number,
 * }} opts - `rows`: the project's ledger rows (other runs are filtered out here), null when the
 *   ledger could not be read; `budget`: the workspace's `budget.usd` (null when unset), as
 *   `run status` reads it.
 * @returns {RunStatus}
 */
export function briefStatus({ record, rows, budget, findingsOf }) {
  const runId = String(record.run_id);
  /** @type {Map<string, Array<Record<string, any>>>} the run's rows, grouped by block once */
  const byBlock = new Map();
  for (const r of rows ?? []) {
    if (r?.run !== runId || typeof r.block !== 'string') continue;
    const list = byBlock.get(r.block);
    if (list) list.push(r);
    else byBlock.set(r.block, [r]);
  }
  const spend = rows == null ? null : runSpend(rows, runId);
  const entries = record.blocks && typeof record.blocks === 'object' ? record.blocks : {};
  const ids = Object.keys(entries).sort(byBlockId);
  /** @type {BlockStatus[]} */
  const blocks = [];
  let closed = 0;
  let stopped = 0;
  for (const id of ids) {
    const entry = entries[id];
    const status = typeof entry?.status === 'string' ? entry.status : 'open';
    if (status === 'closed') closed += 1;
    else if (status === 'stopped') stopped += 1;
    if (status === 'closed' || status === 'stopped') continue;
    const mine = byBlock.get(id) ?? [];
    const attempt = Number.isInteger(entry?.attempt) ? entry.attempt : 1;
    blocks.push({
      id,
      state: status !== 'open' ? status : attempt > 1 ? `attempt-${attempt}` : 'open',
      level: typeof entry?.level === 'string' ? entry.level : null,
      model: coderModel(mine),
      gate: lastGate(mine),
      findings: findingsOf(id),
    });
  }
  return {
    run_id: runId,
    status: typeof record.status === 'string' ? record.status : null,
    mode: record.mode === 'brief' ? 'brief' : 'plan',
    spent_usd: spend === null ? null : spend.usd,
    unknown_usd_sessions: spend === null ? null : spend.unknown,
    budget_usd: budget,
    blocks,
    more: 0,
    closed,
    stopped,
  };
}
