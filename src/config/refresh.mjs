/**
 * `models --refresh --from-cli-caches` (plan §2 step 3, §1.3, C15 — un-cut from v1.1's original
 * "returns as an opt-in"). Reads each CLI's own model cache off disk (Codex's `~/.codex/
 * models_cache.json`, Grok's `~/.grok/models_cache.json` — whichever are present) and merges the
 * ids into a **user-level** cache written by THIS module, `~/.code-forge/cache/models.json`,
 * shaped `{status: "seen-in-cache", updated_at, seen: {<provider>: [<id>, ...]}}`. It never
 * touches the project's `.code-forge.yml` — a model seen this way is only ever a WARNING-
 * suppressing fact for `validate.mjs`'s `unknown-model-id` rule (like `known_extra`, but
 * per-machine instead of per-project), never something this module writes into the config a
 * project commits.
 *
 * `home` is always an explicit parameter (default `os.homedir()`), never hardcoded — the only way
 * a test can point this at a fixture HOME without touching the real `~/.codex`/`~/.grok`.
 *
 * **Real cache shapes** (read once from this machine's own `~/.codex/models_cache.json` and
 * `~/.grok/models_cache.json`, 2026-09-24, to build the fixtures under `test/fixtures/cli-caches/`
 * — no secret, identity, etag or auth-looking field was copied into either fixture):
 *  - Codex: `{fetched_at, etag, client_version, identity, models: [{slug, display_name,
 *    supported_reasoning_levels: [{effort, description}, ...], ...many more fields}, ...]}` — an
 *    ARRAY of model OBJECTS, id at `.slug`.
 *  - Grok: `{fetched_at, grok_version, auth_method, origin, identity, etag, models: {<id>:
 *    {info: {id, model, model_family, reasoning_efforts: [{id, value, label, ...}], ...}, api_key,
 *    env_key, api_base_url}, ...}}` — an OBJECT keyed by id, id at `.info.id` (falls back to the
 *    object's own key if `info.id` is absent).
 * Both extractors also accept a bare `["id", ...]` array or `{"models": ["id", ...]}` — a
 * deliberately simple form kept for hand-written test fixtures and for forward compatibility with
 * a future CLI cache that already lists bare id strings.
 */

import { mkdir, readFile, rename, unlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { randomUUID } from 'node:crypto';

/**
 * @typedef {(parsed: unknown) => string[]} ModelIdExtractor
 */

/**
 * Codex's real shape: `models` is an ARRAY of objects; the id is `.slug`. Also accepts the
 * simple array-of-strings / `{models: [string]}` forms.
 * @type {ModelIdExtractor}
 */
function extractCodexModelIds(parsed) {
  const models = Array.isArray(parsed) ? parsed : /** @type {any} */ (parsed)?.models;
  if (!Array.isArray(models)) {
    return [];
  }
  return models
    .map((entry) => (typeof entry === 'string' ? entry : entry?.slug))
    .filter((id) => typeof id === 'string' && id.length > 0);
}

/**
 * Grok's real shape: `models` is an OBJECT keyed by id; the id is `.info.id` (or the key itself
 * when `info.id` is missing/malformed). Also accepts the simple array-of-strings /
 * `{models: [string]}` forms.
 * @type {ModelIdExtractor}
 */
function extractGrokModelIds(parsed) {
  if (Array.isArray(parsed)) {
    return parsed.filter((id) => typeof id === 'string');
  }
  const models = /** @type {any} */ (parsed)?.models;
  if (Array.isArray(models)) {
    return models.filter((id) => typeof id === 'string');
  }
  if (models && typeof models === 'object') {
    return Object.entries(models)
      .map(([key, entry]) => (typeof entry?.info?.id === 'string' && entry.info.id.length > 0 ? entry.info.id : key))
      .filter((id) => typeof id === 'string' && id.length > 0);
  }
  return [];
}

/** Where each CLI keeps its own model cache, relative to `$HOME`, and how to read its ids. */
const CLI_CACHE_SOURCES = Object.freeze([
  { provider: 'openai', relativePath: path.join('.codex', 'models_cache.json'), extract: extractCodexModelIds },
  { provider: 'xai', relativePath: path.join('.grok', 'models_cache.json'), extract: extractGrokModelIds },
]);

/**
 * @param {string} home
 * @returns {string}
 */
export function userModelsCachePath(home = os.homedir()) {
  return path.join(home, '.code-forge', 'cache', 'models.json');
}

/**
 * @typedef {object} RefreshResult
 * @property {string[]} sourcesRead - absolute paths of the CLI caches that existed, parsed as
 *   valid JSON, AND yielded at least one id.
 * @property {{path: string, reason: string}[]} sourcesSkipped - a CLI cache that existed but
 *   could not be used — unreadable (`unreadable (EISDIR)`), malformed JSON (`not valid JSON`,
 *   optionally `(at position N)` — never a source excerpt), or parsed but yielded 0 ids (a shape
 *   this module doesn't recognise). A source being skipped never aborts the other source.
 * @property {Record<string, string[]>} seenThisRun - provider -> ids read from THIS run's sources only.
 * @property {string} cachePath - where the merged user-level cache was written.
 */

/**
 * @param {{home?: string}} [opts]
 * @returns {Promise<RefreshResult>}
 */
export async function refreshFromCliCaches(opts = {}) {
  const home = opts.home ?? os.homedir();
  /** @type {Record<string, string[]>} */
  const seenThisRun = {};
  /** @type {string[]} */
  const sourcesRead = [];
  /** @type {{path: string, reason: string}[]} */
  const sourcesSkipped = [];

  for (const { provider, relativePath, extract } of CLI_CACHE_SOURCES) {
    const cachePath = path.join(home, relativePath);
    let raw;
    try {
      raw = await readFile(cachePath, 'utf8');
    } catch (err) {
      const code = /** @type {NodeJS.ErrnoException} */ (err).code;
      if (code === 'ENOENT') {
        continue;
      }
      // EACCES, EISDIR, EMFILE, … — skipped like a malformed file, never aborting the OTHER
      // provider's valid cache (round-2 MINOR). Only the errno code is reported.
      sourcesSkipped.push({ path: cachePath, reason: `unreadable (${safeErrnoCode(code)})` });
      continue;
    }

    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch (err) {
      // A malformed source (e.g. the CLI is partway through writing it) never aborts the other
      // provider's valid cache — skip and record why, keep going. NEVER `err.message`: V8's
      // SyntaxError quotes a slice of the source text, and Grok's real cache stores `api_key` in
      // every model entry, so a half-written file would put a key into this reason (round-2
      // MAJOR). Only the numeric position, parsed out, is kept.
      sourcesSkipped.push({ path: cachePath, reason: `not valid JSON${jsonErrorPosition(err)}` });
      continue;
    }

    const ids = extract(parsed);
    if (ids.length === 0) {
      // Parsed fine but yielded nothing — an unrecognised shape is reported, never silently
      // recorded as a successful empty refresh.
      sourcesSkipped.push({ path: cachePath, reason: 'parsed but no model ids could be read from it' });
      continue;
    }
    seenThisRun[provider] = ids;
    sourcesRead.push(cachePath);
  }

  const cachePath = userModelsCachePath(home);
  const existingSeen = await readExistingSeen(cachePath);

  /** @type {Record<string, string[]>} */
  const mergedSeen = { ...existingSeen };
  for (const [provider, ids] of Object.entries(seenThisRun)) {
    const merged = new Set(Object.hasOwn(mergedSeen, provider) ? mergedSeen[provider] : []);
    for (const id of ids) {
      merged.add(id);
    }
    mergedSeen[provider] = [...merged].sort();
  }

  await writeModelsCacheAtomically(cachePath, {
    status: 'seen-in-cache',
    updated_at: new Date().toISOString(),
    seen: mergedSeen,
  });

  return { sourcesRead, sourcesSkipped, seenThisRun, cachePath };
}

/**
 * @param {unknown} code
 * @returns {string} an errno code (`EISDIR`) when it has the errno shape, else `unknown error` —
 *   the reason string must never carry free text.
 */
function safeErrnoCode(code) {
  return typeof code === 'string' && /^E[A-Z0-9]+$/.test(code) ? code : 'unknown error';
}

/**
 * @param {unknown} err - a `JSON.parse` SyntaxError.
 * @returns {string} ` (at position N)` when V8 reported one, else `''` — digits only, never the
 *   quoted source excerpt V8 also puts in the message.
 */
function jsonErrorPosition(err) {
  const message = err instanceof Error ? err.message : '';
  const match = /\bposition (\d+)\b/.exec(message);
  return match ? ` (at position ${match[1]})` : '';
}

/** A provider key that must never be written into a map — it names the prototype, not a provider. */
const FORBIDDEN_PROVIDER_KEYS = new Set(['__proto__', 'constructor', 'prototype']);

/**
 * Reads the existing user-level cache's `seen` map, tolerating a missing, unreadable, corrupt, or
 * wrongly-shaped file by discarding it (never crashing a refresh — or a `validate` — over it).
 * Only keeps `seen.<provider>` entries that are themselves arrays of strings — a string value
 * (`new Set("abc")` would silently split it into characters) or any other shape is dropped rather
 * than trusted. `__proto__`/`constructor`/`prototype` keys are dropped (round-2 MINOR): JSON.parse
 * makes `"__proto__"` an OWN key, and assigning it into a plain object would replace that
 * object's prototype with the id array. (A plain object, not a null-prototype one, on purpose:
 * callers and tests compare the result against `{}` with strict deep equality.)
 * @param {string} cachePath
 * @returns {Promise<Record<string, string[]>>}
 */
async function readExistingSeen(cachePath) {
  /** @type {Record<string, string[]>} */
  const cleaned = {};
  let raw;
  try {
    raw = await readFile(cachePath, 'utf8');
  } catch {
    // ENOENT (never refreshed), EISDIR, EACCES, … — every read error means "nothing usable seen".
    // A refresh then fails loudly at its own write step if the path is genuinely unwritable.
    return cleaned;
  }

  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return cleaned; // corrupt JSON — a refresh is about to overwrite it with a valid file anyway.
  }

  const seen = parsed && typeof parsed === 'object' ? /** @type {any} */ (parsed).seen : undefined;
  if (!seen || typeof seen !== 'object' || Array.isArray(seen)) {
    return cleaned;
  }
  for (const [provider, ids] of Object.entries(seen)) {
    if (FORBIDDEN_PROVIDER_KEYS.has(provider)) {
      continue;
    }
    if (Array.isArray(ids) && ids.every((id) => typeof id === 'string')) {
      cleaned[provider] = ids;
    }
  }
  return cleaned;
}

/**
 * Writes `contents` to `cachePath` atomically: write to a sibling temp file, then `rename()` it
 * over the target. `rename()` within the same directory is a single filesystem operation, so a
 * reader never observes a partially-written file, and an interrupted write leaves the ORIGINAL
 * file (or no file) intact rather than a truncated one.
 * @param {string} cachePath
 * @param {Record<string, any>} contents
 */
async function writeModelsCacheAtomically(cachePath, contents) {
  await mkdir(path.dirname(cachePath), { recursive: true });
  const tempPath = path.join(path.dirname(cachePath), `.models.json.${randomUUID()}.tmp`);
  await writeFile(tempPath, `${JSON.stringify(contents, null, 2)}\n`, 'utf8');
  try {
    await rename(tempPath, cachePath);
  } catch (err) {
    await unlink(tempPath).catch(() => {});
    throw err;
  }
}

/**
 * Loads the merged user-level cache written by {@link refreshFromCliCaches}, for `validate.mjs`'s
 * `unknown-model-id` rule to pass in as `opts.seenInCache`. Returns `{}` when the cache has never
 * been written, is corrupt, or is wrongly shaped (a project that never ran `--refresh-models`, or
 * whose cache file got damaged, sees the same warnings it always did — never a crash).
 * @param {{home?: string}} [opts]
 * @returns {Promise<Record<string, string[]>>}
 */
export async function loadSeenInCache(opts = {}) {
  return readExistingSeen(userModelsCachePath(opts.home ?? os.homedir()));
}
