/**
 * Doctor rows that need no provider CLI (plan §2.3; block B13b): config, harness links, Jev key
 * resolution (the three `--quick` rows), `code-forge` on PATH, Solo, gate commands, the live Jev
 * call, ledger writability, the `tmp` row and (B69) the `review state` row. Every check returns
 * rows; none throws.
 */

import { constants, readdirSync } from 'node:fs';
import { access, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { validateConfig } from '../config/validate.mjs';
import { askJev as realAskJev } from '../decide/jev-client.mjs';
import { buildQuestionPayload } from '../decide/questions.mjs';
import { commandOnPath } from '../install/detect.mjs';
import { installsPath, readInstalls, targetResolves } from '../install/link.mjs';
import { missingTools } from '../install/tools.mjs';
import { OP_MESSAGES } from '../keys/onepassword.mjs';
import { createDefaultKeyStore, resolveKey } from '../keys/store.mjs';
import { ledgerDir } from '../ledger/paths.mjs';
import { readAllRows } from '../ledger/write.mjs';
import { DEFAULT_PROVIDER_CONCURRENCY, providerLimit } from '../session/spawn.mjs';
import { snapshotFor } from '../state/config-snapshot.mjs';
import { assertRunId, runsDir } from '../state/paths.mjs';
import { readRun } from '../state/run.mjs';
import { loadKey } from '../state/signer.mjs';
import { listEntries } from '../util/reaper.mjs';
import { ownerState, runRootPath, tmpBase } from '../util/tmp.mjs';
import { parallelTickets } from '../worker/loop.mjs';
import { missingStateFiles } from '../worker/review-state.mjs';
import { row } from './rows.mjs';

/** @typedef {import('./rows.mjs').Row} Row */

/** The ids of the `--quick` rows, in print order. */
export const QUICK_IDS = Object.freeze(['config', 'links', 'keys']);

/**
 * @param {string} cwd
 * @returns {Promise<{rows: Row[], cfg: Record<string, any> | null, file: string}>} `cfg` is null
 *   when the file is missing, unreadable or invalid.
 */
export async function checkConfig(cwd) {
  const loaded = await loadProjectConfig(cwd);
  if (!loaded.ok) return { rows: [row('config', 'FAIL', 'config', `${loaded.error}: .code-forge.yml cannot be loaded (run code-forge init)`)], cfg: null, file: loaded.path };
  const result = validateConfig(loaded.config);
  if (!result.valid) {
    return { rows: [row('config', 'FAIL', 'config', `${result.errors.length} error(s); first: ${result.errors[0].message}`)], cfg: null, file: loaded.path };
  }
  const warn = result.warnings.length;
  return {
    rows: [row('config', warn > 0 ? 'WARN' : 'OK', 'config', warn > 0 ? `valid with ${warn} warning(s); first: ${result.warnings[0].message}` : 'valid')],
    cfg: loaded.config,
    file: loaded.path,
  };
}

/**
 * Each recorded harness link must resolve. Whether the harness itself lists the skill is not
 * probed: no pinned CLI help documents a skill-listing command, so it is reported UNVERIFIED.
 * @param {string} home
 * @returns {Promise<Row[]>}
 */
export async function checkLinks(home) {
  const records = await readInstalls(installsPath(home));
  if (records.length === 0) return [row('links', 'WARN', 'links', 'no harness link recorded (run code-forge init)')];
  const broken = [];
  for (const r of records) if (!(await targetResolves(r.target))) broken.push(`${r.harness}/${r.scope}`);
  if (broken.length > 0) return [row('links', 'FAIL', 'links', `${broken.length} of ${records.length} link(s) do not resolve: ${broken.join(', ')}`)];
  return [row('links', 'OK', 'links', `${records.length} link(s) resolve; skill listing UNVERIFIED`)];
}

/**
 * Resolve the Jev key through B2's chain (env → keychain/file → 1Password). The value never
 * leaves this function except to the caller that makes the live call.
 * @param {Record<string, any> | null} cfg
 * @param {{env: NodeJS.ProcessEnv, store?: any, opRead?: any}} deps
 * @returns {Promise<{rows: Row[], key: string | null}>}
 */
export async function checkKeys(cfg, deps) {
  try {
    const store = deps.store ?? (await createDefaultKeyStore(deps.env));
    const ref = cfg?.system1?.key ?? cfg?.keys?.jev;
    const res = await resolveKey('jev', { store, env: deps.env, ref: typeof ref === 'string' ? ref : undefined, ...(deps.opRead ? { opRead: deps.opRead } : {}) });
    if (res.value === null) {
      // only fixed texts are shown: a 1Password failure by its classified message (B25), any other
      // reason as a fixed "key store error" — never a reference, a path or tool output
      const known = Object.values(OP_MESSAGES);
      const reasons = Array.isArray(res.errors)
        ? [...new Set(res.errors.map((e) => known.find((m) => typeof e === 'string' && (e === m || e.startsWith(m))) ?? 'key store error'))]
        : [];
      const why = reasons.length > 0 ? ` (${reasons.join('; ')})` : '';
      return { rows: [row('keys', 'WARN', 'keys', `jev key not resolved${why}; System 1 falls back to rules`)], key: null };
    }
    const exp = res.exp === null ? 'no expiry' : `expires ${new Date(res.exp).toISOString()}`;
    return { rows: [row('keys', 'OK', 'keys', `jev key from ${res.source}, ${exp}`)], key: res.value };
  } catch (err) {
    return { rows: [row('keys', 'FAIL', 'keys', `jev key resolution failed (${err?.code ?? err?.name ?? 'error'})`)], key: null };
  }
}

/**
 * @param {Record<string, any>} cfg @param {string} pathEnv
 * @returns {Promise<Row[]>} `code-forge` on PATH, Solo, and one row per configured gate command.
 */
export async function checkCommands(cfg, pathEnv) {
  const rows = [];
  rows.push((await commandOnPath('code-forge', { pathEnv }))
    ? row('path', 'OK', 'code-forge', 'on PATH')
    : row('path', 'WARN', 'code-forge', 'not on PATH (the skill shim calls it)'));
  rows.push(row('solo', 'INFO', 'solo', 'UNVERIFIED (no Solo probe in this version)'));
  const gates = cfg?.gates && typeof cfg.gates === 'object' ? cfg.gates : {};
  let any = false;
  for (const [name, command] of Object.entries(gates)) {
    const argv = command && !Array.isArray(command) && typeof command === 'object' ? command.argv : command; // B60: {argv, env}
    if (!Array.isArray(argv) || argv.length === 0) continue;
    any = true;
    const found = await commandOnPath(String(argv[0]), { pathEnv });
    rows.push(row(`gate.${name}`, found ? 'OK' : 'FAIL', `gate ${name}`, found ? `${argv[0]} on PATH` : `${argv[0]} not on PATH`));
  }
  if (!any) rows.push(row('gates', 'WARN', 'gates', 'no gate command configured'));
  return rows;
}

/**
 * The recommended tools of the shared table (B26): one INFO row naming the missing ones, none
 * when all are there. A missing optional tool never WARNs or FAILs.
 * @param {import('../install/tools.mjs').ToolEnv} opts
 * @returns {Promise<Row[]>}
 */
export async function checkTools(opts) {
  let missing;
  try {
    missing = await missingTools(opts);
  } catch {
    return [row('tools', 'INFO', 'tools', 'could not check recommended tools')];
  }
  if (missing.length === 0) return [];
  return [row('tools', 'INFO', 'tools', `missing recommended tools: ${missing.join(', ')} — run code-forge tools install`)];
}

/**
 * One live Jev call (a `risk` question on a two-line state). Without a key it is skipped (WARN);
 * a bad key or a malformed answer FAILs; a transient failure WARNs.
 * @param {Record<string, any>} cfg @param {string | null} key @param {{askJev?: typeof realAskJev}} deps
 * @returns {Promise<Row[]>}
 */
export async function checkJev(cfg, key, deps) {
  if (key === null) return [row('jev', 'WARN', 'jev', 'live call skipped (no key)')];
  const ask = deps.askJev ?? realAskJev;
  const res = await ask({ state: { paths: ['README.md'], diff_stat: '+1 -0' }, questions: buildQuestionPayload(['risk'], cfg), key });
  if (res.ok) return [row('jev', 'OK', 'jev', `live call ok (${res.attempts} attempt(s))`)];
  const failure = /** @type {import('../decide/jev-client.mjs').JevFailure} */ (res);
  const transient = ['rate_limited', 'unavailable', 'network', 'timeout'].includes(failure.kind);
  return [row('jev', transient ? 'WARN' : 'FAIL', 'jev', `live call failed (${failure.kind}${failure.status ? `, status ${failure.status}` : ''})`)];
}

/** @returns {Promise<Row[]>} */
export async function checkLedger() {
  const dir = ledgerDir();
  try {
    await mkdir(dir, { recursive: true, mode: 0o700 });
    await access(dir, constants.W_OK);
    return [row('ledger', 'OK', 'ledger', 'writable')];
  } catch (err) {
    return [row('ledger', 'FAIL', 'ledger', `not writable (${err?.code ?? 'error'})`)];
  }
}

/**
 * `tmp: <n> stale roots, <m> stale pids` under `tmp.root` (V12): a stale root is a run root whose
 * recorded owner is dead; its pid-registry entries are the stale pids `run start` would reap.
 * @param {Record<string, any> | null} cfg
 * @returns {Row[]}
 */
export function checkTmp(cfg) {
  const root = typeof cfg?.tmp?.root === 'string' ? cfg.tmp.root : undefined;
  let base;
  try {
    base = tmpBase(root);
  } catch {
    return [row('tmp', 'FAIL', 'tmp', 'tmp.root is not an absolute path')];
  }
  let names = [];
  try {
    names = readdirSync(base, { withFileTypes: true }).filter((e) => e.isDirectory() && !e.name.startsWith('.')).map((e) => e.name);
  } catch {
    names = [];
  }
  let roots = 0;
  let pids = 0;
  for (const name of names) {
    const dir = path.join(base, name);
    if (ownerState(dir) !== 'dead') continue; // a live owner (this doctor's own root included) is not stale
    roots += 1;
    pids += listEntries(path.join(dir, 'pids')).length;
  }
  const detail = `${roots} stale roots, ${pids} stale pids`;
  return [row('tmp', roots + pids > 0 ? 'WARN' : 'OK', 'tmp', roots + pids > 0 ? `${detail} (the next code-forge run start removes them)` : detail)];
}

/** B69: the most missing state files the `review state` row names. */
export const LISTED_MAX = 10;

/**
 * B69: one WARN row per ACTIVE run (its record's `status` is `active`, as `run end` and the worker
 * decide it) whose ledger has `review.state` anchors whose latest state file is missing — neither
 * at `<code-forge home>/runs/<run>/review-state/` nor at the old temp-root path. The row counts the
 * files the ledger can rebuild (the latest anchor carries `state_body`) and the lost ones (no body:
 * the next ticket is refused `review-state-lost`; the owner needs `code-forge block reopen`), and
 * names the first {@link LISTED_MAX} blocks and files (then `… and K more`). No row when nothing
 * is missing. When the old temp location cannot be named (`tmp.root` not absolute), nothing is
 * counted and the row says so; when an anchor of the run does not verify, the row says the review
 * state was not checked. Each project's ledger is read once. Never throws: a run whose record, key
 * or ledger cannot be read is skipped. No file content is read or printed.
 * @param {Record<string, any> | null} cfg
 * @returns {Promise<Row[]>}
 */
export async function checkReviewState(cfg) {
  let names = [];
  try {
    names = readdirSync(runsDir()).filter((n) => n.endsWith('.json'));
  } catch {
    return [];
  }
  /** @type {Row[]} */
  const rows = [];
  /** @type {Map<string, Array<Record<string, any>>>} the ledger rows per project, read once */
  const ledgers = new Map();
  for (const name of names.sort()) {
    const runId = name.slice(0, -'.json'.length);
    try {
      assertRunId(runId);
      const record = await readRun(runId);
      if (record?.status !== 'active' || typeof record.project !== 'string') continue;
      const key = await loadKey(runId);
      let ledger = ledgers.get(record.project);
      if (ledger === undefined) {
        ledger = await readAllRows(record.project);
        ledgers.set(record.project, ledger);
      }
      const atNew = missingStateFiles({ runId, key, rows: ledger });
      if (atNew === null) {
        rows.push(row('review-state', 'WARN', 'review state', `run ${runId}: review.state anchors do not verify; review state not checked`));
        continue;
      }
      let root;
      try {
        root = snapshotFor(record, '')?.config?.tmp?.root;
      } catch {
        root = undefined;
      }
      if (typeof root !== 'string') root = typeof cfg?.tmp?.root === 'string' ? cfg.tmp.root : undefined;
      let runRootDir;
      try {
        runRootDir = runRootPath(runId, root === undefined ? {} : { root });
      } catch {
        runRootDir = undefined;
      }
      if (runRootDir === undefined) {
        // the old place cannot be named: nothing is counted as missing, the row says why
        if (atNew.length > 0) {
          rows.push(row('review-state', 'WARN', 'review state', `run ${runId}: the old temp location could not be checked (tmp.root is not an absolute path); review state files are not counted`));
        }
        continue;
      }
      const missing = missingStateFiles({ runId, key, rows: ledger, runRootDir });
      if (missing === null || missing.length === 0) continue;
      const rebuildable = missing.filter((m) => m.rebuildable).length;
      const lost = missing.length - rebuildable;
      const shown = missing.slice(0, LISTED_MAX).map((m) => `${m.block} ${m.file} (${m.rebuildable ? 'rebuildable' : 'lost'})`).join(', ');
      const more = missing.length > LISTED_MAX ? ` … and ${missing.length - LISTED_MAX} more` : '';
      rows.push(row('review-state', 'WARN', 'review state',
        `run ${runId}: ${missing.length} state file(s) missing — ${rebuildable} rebuildable from the ledger, ${lost} lost (needs code-forge block reopen): ${shown}${more}`));
    } catch {
      // an unreadable record, key or ledger: not this row's business
    }
  }
  return rows;
}

/**
 * B44: the `review concurrency` INFO row — how many review tickets run at once and each provider's
 * session slots, from the effective config (defaults when a key is unset). Never throws.
 * @param {Record<string, any> | null} cfg @returns {import('./rows.mjs').Row}
 */
export function checkReviewConcurrency(cfg) {
  const c = cfg ?? {};
  const n = parallelTickets(c);
  const extra = Object.keys(c.review?.provider_concurrency ?? {}).filter((p) => !Object.hasOwn(DEFAULT_PROVIDER_CONCURRENCY, p));
  const slots = [...Object.keys(DEFAULT_PROVIDER_CONCURRENCY), ...extra].map((p) => `${p} ${providerLimit(c, p)}`).join(' · ');
  return row('review-concurrency', 'INFO', 'review concurrency', `${n} ${n === 1 ? 'ticket' : 'tickets'} at once; provider slots ${slots}`);
}
