/**
 * Size-based review routing (block B90): `review.routes` sends a big file to a named model with
 * its own session timeout and its own second level, with no `.code-forge.yml` edit and no reload.
 *
 * A rule is `{min_lines, level?, model, provider?, effort?, fallback?, session_timeout_s?, second?}`.
 * `level` is the level it replaces (`L2` default, or `L3`, the judge). The worker picks the route
 * once per ticket, before any session, from the line count of the file's content in the reviewed
 * tree (the bytes it hashed). A deleted file has no content and is never routed. Among the rules
 * of one level, the one with the largest `min_lines` that is at most the count wins.
 *
 * The routed ticket runs with a derived config: `levels.<level>` = the rule's model entry
 * (`model`, `provider`, `effort`, `fallback`; with no `provider` the top-level one, like any level
 * entry), `review.second_levels.<level>` = `second` when given — except an L2 rule while
 * `review.multimodel` is on: the consensus second reviewer is `review.second_levels.L2`, so it is
 * kept. The timeout is ONE value for every session of the ticket: the largest `session_timeout_s`
 * among the matched rules that set one, else the unrouted timeout (`review.session_timeout_s`).
 *
 * No `review.routes` (or no rule that matches) leaves the config object itself untouched. Pure:
 * no I/O; the worker (`src/worker/loop.mjs`) reads the bytes, writes the signed `review.route` row
 * and pins the timeout. This module never imports `src/worker`.
 */

import { resolveLevel } from '../config/known-ids.mjs';

/** The levels a rule may replace: the reviewers (L2) and the judge (L3). */
export const ROUTE_LEVELS = Object.freeze(['L2', 'L3']);

/** The level a rule replaces when it names none. */
export const DEFAULT_ROUTE_LEVEL = 'L2';

/** The keys of a rule that form the routed level entry (`$defs/level`). */
const ENTRY_KEYS = Object.freeze(['model', 'provider', 'effort', 'fallback']);

/** A route list that cannot be used: the ticket is `unavailable` with this code, never reviewed on another model. */
export class RouteError extends Error {
  constructor() {
    super('review.routes is not a list of valid rules');
    this.code = 'route-invalid';
  }
}

/**
 * The number of lines of a file's content: its newline bytes, plus one for a last line with no
 * newline. Counted on the bytes, never on decoded text. `null` (a deleted file) and an empty file
 * have 0 lines.
 * @param {Buffer | null} bytes @returns {number}
 */
export function countLines(bytes) {
  if (bytes === null || bytes.length === 0) return 0;
  let n = 0;
  for (let at = bytes.indexOf(0x0a); at !== -1; at = bytes.indexOf(0x0a, at + 1)) n += 1;
  return bytes[bytes.length - 1] === 0x0a ? n : n + 1;
}

/** @param {unknown} v @returns {v is Record<string, any>} */
const isObject = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);

/** @param {unknown} v @returns {boolean} */
const isPositiveInt = (v) => Number.isInteger(v) && /** @type {number} */ (v) >= 1;

/** @param {unknown} v @returns {boolean} a non-empty string. */
const isText = (v) => typeof v === 'string' && v.length > 0;

/** @param {unknown} v @returns {boolean} absent, or a string (the schema's `effort`/`provider` types). */
const optString = (v) => v === undefined || typeof v === 'string';

/**
 * A fallback item (`$defs/level` fallback): an object with a model and a provider, and an
 * `effort` string when present.
 * @param {unknown} v @returns {boolean}
 */
const isFallbackItem = (v) => isObject(v) && isText(v.model) && isText(v.provider) && optString(v.effort);

/**
 * A `$defs/level`-shaped entry: a model string, `provider` and `effort` strings when present, and
 * `fallback` (when present) a list of fallback items. Used for a rule and for its `second`.
 * @param {unknown} v @returns {boolean}
 */
function isEntry(v) {
  if (!isObject(v) || !isText(v.model) || !optString(v.provider) || !optString(v.effort)) return false;
  return v.fallback === undefined || (Array.isArray(v.fallback) && v.fallback.every(isFallbackItem));
}

/**
 * Is `rule` usable? The schema and config rule 18 refuse everything else at `run start` and
 * `run reload`; this guards a snapshot that never went through them.
 * @param {unknown} rule @returns {boolean}
 */
function usable(rule) {
  if (!isEntry(rule)) return false;
  const r = /** @type {Record<string, any>} */ (rule);
  if (!Number.isInteger(r.min_lines) || r.min_lines < 0) return false;
  if (r.level !== undefined && !ROUTE_LEVELS.includes(r.level)) return false;
  if (r.session_timeout_s !== undefined && !isPositiveInt(r.session_timeout_s)) return false;
  if (r.second !== undefined && !isEntry(r.second)) return false;
  return true;
}

/** @param {Record<string, any>} rule @returns {Record<string, any>} the level entry a rule routes to. */
function entryOf(rule) {
  /** @type {Record<string, any>} */
  const entry = {};
  for (const key of ENTRY_KEYS) if (rule[key] !== undefined) entry[key] = rule[key];
  return entry;
}

/**
 * The provider a rule's routed level runs on — the one place it is decided, shared with config
 * validation (rules 16 and 18) so the two cannot drift: the rule's entry REPLACES
 * `levels.<level>`, so a rule with no `provider` inherits the top-level `provider` (never the
 * replaced level's own `provider`), resolved exactly as the spawner resolves any level.
 * @param {Record<string, any> | null | undefined} cfg @param {Record<string, any>} rule
 * @returns {string | undefined} undefined when the routed level cannot be resolved.
 */
export function ruleProvider(cfg, rule) {
  const level = rule?.level ?? DEFAULT_ROUTE_LEVEL;
  const levels = { ...(isObject(cfg?.levels) ? cfg?.levels : {}), [level]: isObject(rule) ? entryOf(rule) : {} };
  try {
    return resolveLevel({ ...(cfg ?? {}), levels }, /** @type {any} */ (level)).provider;
  } catch {
    return undefined;
  }
}

/**
 * The rules of `cfg.review.routes`: `[]` when the key is absent or null.
 * @param {Record<string, any> | null | undefined} cfg
 * @returns {Array<Record<string, any>>}
 * @throws {RouteError} the key is set but is not a list of usable rules.
 */
export function routeRules(cfg) {
  const routes = cfg?.review?.routes;
  if (routes === undefined || routes === null) return [];
  if (!Array.isArray(routes) || !routes.every(usable)) throw new RouteError();
  // config rule 18 refuses the same level and min_lines twice; a snapshot that skipped it fails
  // closed here rather than letting list order pick the rule
  const seen = new Set(routes.map((r) => `${r.level ?? DEFAULT_ROUTE_LEVEL}\0${r.min_lines}`));
  if (seen.size !== routes.length) throw new RouteError();
  return routes;
}

/**
 * The winning rule per level for a file of `lines` lines: among the rules of a level, the largest
 * `min_lines` that is at most `lines`. A deleted file (`deleted: true`) is never routed.
 * @param {Record<string, any> | null | undefined} cfg @param {number} lines - a non-negative integer.
 * @param {{deleted?: boolean}} [opts]
 * @returns {Array<{level: string, rule: Record<string, any>}>} in `ROUTE_LEVELS` order; `[]` ⇒ no route.
 * @throws {RouteError}
 */
export function pickRoutes(cfg, lines, { deleted = false } = {}) {
  const rules = routeRules(cfg);
  // a count that is not a line count never picks (or skips) a route: fail closed
  if (!Number.isInteger(lines) || lines < 0) throw new RouteError();
  if (deleted || rules.length === 0) return [];
  /** @type {Array<{level: string, rule: Record<string, any>}>} */
  const out = [];
  for (const level of ROUTE_LEVELS) {
    let best = null;
    for (const rule of rules) {
      if ((rule.level ?? DEFAULT_ROUTE_LEVEL) !== level || rule.min_lines > lines) continue;
      if (best === null || rule.min_lines > best.min_lines) best = rule;
    }
    if (best !== null) out.push({ level, rule: best });
  }
  return out;
}

/**
 * @typedef {object} RoutedTicket
 * @property {Record<string, any>} cfg - the config every session of the ticket runs with; the SAME
 *   object as the input when nothing is routed.
 * @property {number} timeoutS - the session timeout of every session of the ticket.
 * @property {Array<Record<string, any>>} rows - one `review.route` row body per routed level
 *   (`{event, level, min_lines, lines, provider, model, session_timeout_s, second_model}`), unsigned.
 */

/**
 * The ticket's routed config, timeout and `review.route` rows.
 * @param {Record<string, any>} cfg - the ticket's config snapshot.
 * @param {{lines: number, deleted?: boolean, timeoutS: number}} file - `timeoutS`: the unrouted timeout.
 * @returns {RoutedTicket}
 * @throws {RouteError}
 */
export function routeTicket(cfg, { lines, deleted = false, timeoutS }) {
  const picked = pickRoutes(cfg, lines, { deleted });
  if (picked.length === 0) return { cfg, timeoutS, rows: [] };
  const given = picked.map(({ rule }) => rule.session_timeout_s).filter((t) => t !== undefined);
  const routedTimeout = given.length > 0 ? Math.max(...given) : timeoutS;
  const levels = { ...(isObject(cfg.levels) ? cfg.levels : {}) };
  const review = { ...(isObject(cfg.review) ? cfg.review : {}) };
  /** @type {Record<string, any>} */
  const seconds = { ...(isObject(review.second_levels) ? review.second_levels : {}) };
  let secondsChanged = false;
  /** @type {Array<{level: string, rule: Record<string, any>, second: boolean}>} */
  const applied = [];
  for (const { level, rule } of picked) {
    levels[level] = entryOf(rule);
    // the consensus second reviewer is `second_levels.L2`: an L2 rule never replaces it then
    const second = rule.second !== undefined && !(level === 'L2' && review.multimodel === true);
    if (second) {
      seconds[level] = rule.second;
      secondsChanged = true;
    }
    applied.push({ level, rule, second });
  }
  if (secondsChanged) review.second_levels = seconds;
  const routed = { ...cfg, levels, review };
  const rows = applied.map(({ level, rule, second }) => ({
    event: 'review.route',
    level,
    min_lines: rule.min_lines,
    lines,
    provider: providerOf(cfg, rule),
    model: rule.model,
    session_timeout_s: routedTimeout,
    second_model: second ? rule.second.model : null,
  }));
  return { cfg: routed, timeoutS: routedTimeout, rows };
}

/**
 * The provider the routed level resolves to — the one its sessions spawn on (`ruleProvider`).
 * @param {Record<string, any>} cfg - the unrouted snapshot. @param {Record<string, any>} rule
 * @returns {string}
 * @throws {RouteError} the routed level cannot be resolved (e.g. no provider anywhere): the ticket
 *   is `route-invalid`, never a row with no provider.
 */
function providerOf(cfg, rule) {
  const provider = ruleProvider(cfg, rule);
  if (provider === undefined) throw new RouteError();
  return provider;
}
