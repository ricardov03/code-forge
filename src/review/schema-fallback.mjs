/**
 * The second-level retry after repeated `schema` misses on ONE packet (block B55, issue #2).
 *
 * Field data: one L2 reviewer model answered `unavailable: schema` four times in a row on the same
 * packet (round 1, its retry, a patch_check recheck, a fresh review), while the model configured in
 * `review.second_levels.L2` reviewed that packet cleanly on its first try.
 *
 * What counts as "2 schema failures on the same packet": a stub-guard verdict `schema` from a
 * session of the level's OWN ladder (never one from a second-level try) on the same packet — the
 * same run, file, content hash and exact packet text (`packet_hash`, sha256 of the text) — at the
 * same level and role (one level's misses or mark never count for, or block, another's). Every
 * such miss leaves one signed `review.schema_invalid` row; the count is those rows (read through
 * `history`, which only counts rows whose MAC verifies) plus the miss in hand. In practice: the
 * round's first session is miss 1; the worker's existing retry of that round (`next: retry`, the
 * next ticket re-runs the SAME packet) is miss 2, and that ticket then tries the second level once.
 * A session that times out is retried first by `session-retry.mjs` and counts once, with its last
 * result; a `needs_file` round or a Markdown section is another packet text, so another count, and
 * so is a recheck whose pending set changed between tries (S1 `resolved` closed another finding,
 * so the open-findings list differs): a different packet starts its own count, by design.
 *
 * The rows written here carry no `run`, `file` or `content_hash`: the worker's `writeRow` stamps
 * them (`worker/loop.mjs`: `{...row, block, file, content_hash: ticket.content_hash}`, signed with
 * the run key), and the worker's history reads them back by the same ticket values — so outside
 * the worker (no history) nothing is ever tried.
 *
 * At most ONCE per packet: a signed `review.schema_fallback` row is written BEFORE the second level
 * is spawned, and any such row for the packet (whatever its outcome) stops every later try; when
 * that row cannot be written nothing is spawned. The second level is `review.second_levels.<level>`
 * only — nothing is tried without one; when every model of it is the one that just failed, the
 * mark says `refused: same-model` and nothing is spawned. The try goes through the same `spawn` as
 * any session, so the budget gate (`budget.usd`, B33) refuses it like any other (its own
 * `budget.refused` row; the review is `unavailable: budget`). For a closed-book role every Codex
 * model of the second level (its own and its ladder) is dropped unless
 * `review.allow_open_book_codex`; when none is left the mark says `refused: closed-book` and
 * nothing is spawned — the spawner's own refusal stays as it was. Its answer goes
 * through the same stub guard; a valid one is a normal answer, a failed one leaves the file
 * unavailable as before.
 *
 * Every schema miss (second-level ones included) is logged as `review.schema_invalid {lens, role,
 * level, provider, model, fallback_step, packet_hash, second_level, ...diagnosis}` — a STRUCTURAL
 * diagnosis of the answer, never its text ({@link diagnoseAnswer}): its size and kind, for JSON the
 * top-level keys with their JSON types and the finding-schema errors (path, keyword, a few
 * schema-side params, Ajv's message), for non-JSON the kind of parse error and its byte offset.
 * Text a model copies from the packet into a JSON string can never reach the ledger this way.
 *
 * Concurrency: the worker never runs two tickets for one (block, file) at once (its file lock,
 * `worker/loop.mjs`), and round 1, its retry, every recheck and the patch_check of a file are all
 * tickets for that file, so one packet's history is read and written by one ticket at a time. The
 * read-history → write-mark section here also runs under an in-process lock per (packet hash,
 * level, role), so even the same file in two blocks cannot take a packet's one try twice.
 *
 * No ledger writer (`writeRow` absent) ⇒ nothing is logged and nothing is tried. The helpers never
 * throw: a history that cannot be read disables the try (with one warning line), and a
 * second-level spawn that throws is a session with no result (`exit`) after its mark row.
 */

import { createHash } from 'node:crypto';
import { closedBookRefused, openBookCodexAllowed } from '../config/closed-book.mjs';
import { resolveLevel } from '../config/known-ids.mjs';
import { logWarning } from '../util/error-log.mjs';
import { keyedLock } from '../util/locks.mjs';
import { schemaErrorsOf } from './validate-review.mjs';

/** Schema misses of the level's own ladder on one packet before the second level is tried. */
export const SCHEMA_FAILURES_BEFORE_FALLBACK = 2;

/** At most this many schema errors are kept in a diagnosis (`errors_total` has the full count). */
export const DIAGNOSIS_MAX_ERRORS = 20;
/** At most this many top-level keys are kept in a diagnosis (the rest counted in `other_keys`). */
export const DIAGNOSIS_MAX_KEYS = 30;
/** Ajv's message, and a parse error, are kept up to this length. */
const MESSAGE_MAX = 120;
/** A key or path segment is shown only when it is this plain; any other is `*` (it could be content). */
const PLAIN_KEY = /^[A-Za-z0-9_.-]{1,40}$/;
/** The Ajv params kept: they come from the schema, never from the answer's data. */
const KEPT_PARAMS = Object.freeze(['missingProperty', 'additionalProperty', 'type', 'limit', 'comparison', 'format']);

/**
 * @typedef {(packetHash: string, who: {level: string, role: string}) => Promise<{failures: number, fellBack: boolean}>} SchemaHistory -
 *   the signed history of ONE packet (`packetHash`) at one level and role in this run for this
 *   file at this content hash: `failures` = `review.schema_invalid` rows of the level's own ladder
 *   (`second_level` not true), `fellBack` = a `review.schema_fallback` row exists. Built by the
 *   worker; absent ⇒ no second-level try.
 */

/** @param {string} text @returns {string} the packet's identity: sha256 hex of its exact text. */
export function packetHash(text) {
  return createHash('sha256').update(text, 'utf8').digest('hex');
}

/** @param {unknown} v @returns {string} the JSON type of `v`. */
function jsonType(v) {
  if (v === null) return 'null';
  if (Array.isArray(v)) return 'array';
  if (typeof v === 'number') return Number.isInteger(v) ? 'integer' : 'number';
  return typeof v;
}

/** @param {unknown} key @returns {string} the key when plain (or an array index), else `*`. */
function plainKey(key) {
  if (typeof key === 'number') return String(key);
  return typeof key === 'string' && PLAIN_KEY.test(key) ? key : '*';
}

/** @param {string} pointer @returns {string} the JSON pointer with every non-plain segment as `*`. */
function plainPath(pointer) {
  if (pointer === '') return '';
  const segments = pointer.slice(1).split('/').map((seg) => (/^\d+$/.test(seg) ? seg : plainKey(seg.replace(/~1/g, '/').replace(/~0/g, '~'))));
  return `/${segments.join('/')}`;
}

/**
 * A parse error without any of the text: V8 quotes the input after a `"` and names the bad
 * character, so the message is cut at its first quote, the token and the line/column dropped.
 * @param {string} message @returns {string}
 */
function plainParseError(message) {
  const cut = message
    .split('"')[0]
    .replace(/Unexpected token '.*?'/, 'Unexpected token')
    .replace(/\s*\(line \d+ column \d+\)/, '')
    .replace(/[,\s]+$/, '');
  return cut.slice(0, MESSAGE_MAX);
}

/**
 * The structural diagnosis of a schema miss (B55) — never the answer's text: `answer_bytes`,
 * `answer_kind` (`json`, `not-json`, `empty`); for JSON `top_type`, `top_keys` (plain keys ⇒ their
 * JSON type, at most {@link DIAGNOSIS_MAX_KEYS}; any other key only counted in `other_keys`),
 * `schema_errors` (at most {@link DIAGNOSIS_MAX_ERRORS}, from the stub guard's own validator:
 * `path` with non-plain segments as `*`, `keyword`, the schema-side `params`, Ajv's `message` ≤ 120
 * chars) and `errors_total`; for non-JSON `parse_error` (V8's message cut before any quoted input)
 * and `parse_offset` (bytes; null when V8 gives no position).
 * @param {Record<string, any> | null} res - the session result (`text`, else `answer`).
 * @returns {Record<string, any>}
 */
export function diagnoseAnswer(res) {
  const text = typeof res?.text === 'string' && res.text.length > 0 ? res.text : typeof res?.answer === 'string' ? res.answer : '';
  const answer_bytes = Buffer.byteLength(text);
  /** @type {unknown} */
  let value = res?.answer !== undefined && res?.answer !== null && typeof res.answer !== 'string' ? res.answer : undefined;
  if (value === undefined) {
    if (text.trim().length === 0) return { answer_bytes, answer_kind: 'empty' };
    try {
      value = JSON.parse(text);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      const at = /at position (\d+)/.exec(message);
      const parse_offset = at ? Buffer.byteLength(text.slice(0, Number(at[1]))) : /end of JSON input/.test(message) ? answer_bytes : null;
      return { answer_bytes, answer_kind: 'not-json', parse_error: plainParseError(message), parse_offset };
    }
  }
  /** @type {Record<string, string>} */
  const top_keys = {};
  let other_keys = 0;
  if (jsonType(value) === 'object') {
    for (const [k, v] of Object.entries(/** @type {Record<string, unknown>} */ (value))) {
      if (PLAIN_KEY.test(k) && k !== '__proto__' && Object.keys(top_keys).length < DIAGNOSIS_MAX_KEYS) top_keys[k] = jsonType(v);
      else other_keys += 1;
    }
  }
  const errors = schemaErrorsOf(value);
  const schema_errors = errors.slice(0, DIAGNOSIS_MAX_ERRORS).map((e) => {
    /** @type {Record<string, unknown>} */
    const given = e.params ?? {};
    /** @type {Record<string, unknown>} */
    const params = {};
    for (const name of KEPT_PARAMS) {
      const v = given[name];
      if (v === undefined) continue;
      if (name === 'missingProperty' || name === 'additionalProperty') params[name] = plainKey(v);
      else if (typeof v === 'number' || typeof v === 'string') params[name] = v;
      else if (Array.isArray(v)) params[name] = v.filter((x) => typeof x === 'string');
    }
    return { path: plainPath(e.instancePath ?? ''), keyword: e.keyword, params, message: String(e.message ?? '').slice(0, MESSAGE_MAX) };
  });
  return { answer_bytes, answer_kind: 'json', top_type: jsonType(value), top_keys, other_keys, schema_errors, errors_total: errors.length };
}

/**
 * The config a second-level try runs with: `levels.<level>` replaced by
 * `review.second_levels.<level>`. Every model of that level — its own, then its fallback ladder —
 * gets its effective provider (the entry's own, else the level's: its `provider`, else
 * `review.second_provider`, else the top-level one), and every one equal to the model that just
 * failed is dropped; for a closed-book `role` every model Codex cannot run closed-book is dropped
 * too (unless `review.allow_open_book_codex`). The first one left runs, the rest are its ladder.
 * `ok: false` with `no-second-level` when none is configured for the level, `same-model` when
 * nothing but the failed model is configured, `closed-book` when only refused models are left.
 * @param {Record<string, any>} cfg @param {string} level
 * @param {{provider: string | null, model: string | null}} from - the model that missed.
 * @param {string} [role] - the session's role (closed-book check); absent ⇒ no such check.
 * @returns {{ok: true, cfg: Record<string, any>, provider: string, model: string} | {ok: false, reason: 'no-second-level' | 'same-model' | 'closed-book'}}
 */
export function secondLevelFor(cfg, level, from, role) {
  const override = cfg?.review?.second_levels?.[level] ?? null;
  if (!override || typeof override !== 'object' || typeof override.model !== 'string' || override.model.length === 0) return { ok: false, reason: 'no-second-level' };
  const levelProvider = override.provider ?? cfg?.review?.second_provider ?? cfg?.provider;
  if (typeof levelProvider !== 'string' || levelProvider.length === 0) return { ok: false, reason: 'no-second-level' };
  const { fallback: ladder, effort: _effort, ...own } = override;
  /** @type {Array<{provider: string, model: string, effort?: string}>} */
  const models = [override, ...(Array.isArray(ladder) ? ladder : [])]
    .filter((e) => e && typeof e === 'object' && typeof e.model === 'string' && e.model.length > 0)
    .map((e) => ({ provider: typeof e.provider === 'string' && e.provider.length > 0 ? e.provider : levelProvider, model: e.model, ...(typeof e.effort === 'string' ? { effort: e.effort } : {}) }))
    .filter((e) => !(e.provider === from.provider && e.model === from.model));
  if (models.length === 0) return { ok: false, reason: 'same-model' };
  const openBook = openBookCodexAllowed(cfg);
  const runnable = role === undefined ? models : models.filter((e) => !closedBookRefused(e.provider, role, openBook));
  if (runnable.length === 0) return { ok: false, reason: 'closed-book' };
  const [first, ...rest] = runnable;
  const entry = { ...own, provider: first.provider, model: first.model, ...(first.effort ? { effort: first.effort } : {}), fallback: rest };
  const next = { ...cfg, levels: { ...cfg.levels, [level]: entry } };
  try {
    resolveLevel(next, /** @type {any} */ (level));
  } catch {
    return { ok: false, reason: 'no-second-level' };
  }
  return { ok: true, cfg: next, provider: first.provider, model: first.model };
}

/**
 * Log a schema miss and, when it is the packet's second (see the module doc), try the second level
 * once. Never throws (see the module doc).
 * @param {object} p
 * @param {Record<string, any> | null} p.res - the missed session's result.
 * @param {{lens: string, role: string, level: string}} p.session
 * @param {string} p.packetText - the exact packet the session read (still on disk for `spawnAt`).
 * @param {Record<string, any>} p.cfg - the config the missed session ran with.
 * @param {boolean} p.allowed - false where a second-level try is never made (consensus mode).
 * @param {SchemaHistory | undefined} p.history
 * @param {((row: Record<string, any>) => Promise<unknown>) | undefined} p.writeRow - throws on a
 *   failed write; absent ⇒ nothing is logged or tried.
 * @param {(cfg: Record<string, any>) => Promise<{res: Record<string, any> | null, attempts: number}>} p.spawnAt
 *   - the same session on the same packet with another config (timeout retry included).
 * @returns {Promise<{res: Record<string, any> | null, attempts: number, to: {level: string, provider: string, model: string}} | null>}
 *   the second level's result, or null when none was spawned.
 */
export async function afterSchemaMiss({ res, session, packetText, cfg, allowed, history, writeRow, spawnAt }) {
  if (!writeRow) return null;
  const write = writeRow;
  const hash = packetHash(packetText);
  const from = { provider: typeof res?.provider === 'string' ? res.provider : null, model: typeof res?.model === 'string' ? res.model : null };
  /** @type {{cfg: Record<string, any>, to: {level: string, provider: string, model: string}} | null} */
  let go = null;
  try {
    go = await keyedLock(`schema-fallback\0${hash}\0${session.level}\0${session.role}`, async () => {
      // the history is read BEFORE this miss is logged: failures = signed earlier misses + this one
      let seen = null;
      if (allowed && history) {
        try {
          seen = await history(hash, { level: session.level, role: session.role });
        } catch {
          seen = null; // unreadable history ⇒ no try (fail closed), one warning line
          await logWarning({ warning: 'review_schema_history', message: 'the schema-miss history could not be read; no second-level try' }).catch(() => null);
        }
      }
      try {
        await write({ event: 'review.schema_invalid', lens: session.lens, role: session.role, level: session.level, ...from, fallback_step: res?.fallback_step ?? 0, packet_hash: hash, second_level: false, ...diagnoseAnswer(res) });
      } catch {
        // the log is best-effort; the count then misses this one (fewer tries, never more)
      }
      if (!seen || seen.fellBack || seen.failures + 1 < SCHEMA_FAILURES_BEFORE_FALLBACK) return null;
      const second = secondLevelFor(cfg, session.level, from, session.role);
      const to = `review.second_levels.${session.level}`;
      /** @type {string | null} */
      let refused = null;
      if (second.ok === false) {
        if (second.reason === 'no-second-level') return null;
        refused = second.reason; // `same-model` or `closed-book`: nothing of the level may run
      }
      await write({
        event: 'review.schema_fallback',
        lens: session.lens,
        role: session.role,
        level: session.level,
        packet_hash: hash,
        from_provider: from.provider,
        from_model: from.model,
        to,
        to_provider: second.ok ? second.provider : null,
        to_model: second.ok ? second.model : null,
        attempts: seen.failures + 1,
        ...(refused ? { refused } : {}),
      }); // the once-per-packet mark: a failed write throws ⇒ no try
      return !second.ok ? null : { cfg: second.cfg, to: { level: to, provider: second.provider, model: second.model } };
    });
  } catch {
    return null;
  }
  if (!go) return null;
  await logWarning({ warning: 'review_schema_fallback', message: 'a review answer failed the schema twice on one packet; the second level was tried once' }).catch(() => null);
  try {
    const out = await spawnAt(go.cfg);
    return { res: out.res, attempts: out.attempts, to: go.to };
  } catch {
    // a throwing spawn is a session with no result: `exit` through the stub guard, never approval
    return { res: null, attempts: 1, to: go.to };
  }
}

/**
 * Log a schema miss of the second-level try itself (never counted, never tried again).
 * @param {{res: Record<string, any> | null, session: {lens: string, role: string, level: string}, packetText: string, writeRow: ((row: Record<string, any>) => Promise<unknown>) | undefined}} p
 */
export async function logSecondLevelMiss({ res, session, packetText, writeRow }) {
  if (!writeRow) return;
  try {
    await writeRow({
      event: 'review.schema_invalid',
      lens: session.lens,
      role: session.role,
      level: session.level,
      provider: typeof res?.provider === 'string' ? res.provider : null,
      model: typeof res?.model === 'string' ? res.model : null,
      fallback_step: res?.fallback_step ?? 0,
      packet_hash: packetHash(packetText),
      second_level: true,
      ...diagnoseAnswer(res),
    });
  } catch {
    // best-effort
  }
}

/**
 * The worker's {@link SchemaHistory} over the run's ledger: for the packet, level and role asked
 * about, rows of `runId` for `file` at `contentHash` with that `packet_hash`, `level` and `role`
 * whose MAC verifies (`verify`; a row that does not verify, or whose check throws, counts for
 * nothing). A miss on another packet of the same content, or at another level, never counts.
 * @param {{readRows: () => Promise<Array<Record<string, any>>>, verify: (row: Record<string, any>) => boolean, runId: string, file: string, contentHash: string}} opts
 * @returns {SchemaHistory}
 */
export function ledgerSchemaHistory({ readRows, verify, runId, file, contentHash }) {
  return async (hash, who) => {
    const rows = (await readRows()).filter((r) => r?.run === runId && r.file === file && r.content_hash === contentHash && r.packet_hash === hash && r.level === who.level && r.role === who.role);
    let failures = 0;
    let fellBack = false;
    for (const r of rows) {
      if (r.event !== 'review.schema_invalid' && r.event !== 'review.schema_fallback') continue;
      let ok = false;
      try {
        ok = verify(r);
      } catch {
        ok = false;
      }
      if (!ok) continue;
      if (r.event === 'review.schema_fallback') fellBack = true;
      else if (r.second_level !== true) failures += 1;
    }
    return { failures, fellBack };
  };
}
