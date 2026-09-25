/**
 * The model catalog and the model-id lint set (plan §1.3, §1.4, C1).
 *
 * Two independent jobs live here on purpose:
 *  1. `KNOWN_IDS` / `knownIdsForProvider()` — the "is this a real model id" catalog `validate.mjs`
 *     checks a `levels.Lx.model` / `levels.Lx.fallback[].model` value against.
 *  2. `LINT_PATTERNS` — the pattern set `test/skill/lint-no-model-ids.test.mjs` (B14, a later
 *     block) scans `skill/**` with, so no model id or alias ever leaks into the shipped skill
 *     prose. C1's fix for the old bare-token list (`sol`, `fable`, …) matching "Solo", "resolve",
 *     "console", "fable-forge": every pattern here is either a full catalog id (escaped literally
 *     — can't false-positive on an unrelated word) or requires a vendor-prefixed shape
 *     (`claude-9`, not bare `claude`) or a `--model <alias>` / `model: <alias>` value position
 *     (not a bare alias word appearing in prose).
 *
 * `resolveLevel()` is the third thing this module exports: a pure function (no I/O, no CLI
 * spawning) that answers "what provider/model/effort does level Lx actually resolve to", given a
 * loaded config — the per-level `provider` override (R5) wins over the top-level `provider`, and
 * the id/effort always come from `levels.Lx` (config is the only source; this module never
 * invents a default id for a level the config didn't set — that is `defaults/*.mjs`'s job, merged
 * into the config by `load.mjs` before anything calls `resolveLevel`).
 */

/**
 * @typedef {"anthropic" | "openai" | "xai"} Provider
 */

/**
 * The full catalog, by provider, as read from each CLI's own model cache on this Mac
 * (plan brief §3, 2026-09-24; A8). Four Anthropic ids, three OpenAI ids, four xAI ids — eleven in
 * total, matching the "≥ 11 ids" the acceptance clause counts.
 * @type {Record<Provider, ReadonlyArray<string>>}
 */
export const KNOWN_IDS = Object.freeze({
  anthropic: Object.freeze([
    'claude-haiku-4-5-20251001',
    'claude-sonnet-5',
    'claude-opus-5-5',
    'claude-fable-5-1',
  ]),
  openai: Object.freeze(['gpt-6-luna', 'gpt-6-sol', 'gpt-6-astra']),
  xai: Object.freeze(['grok-4.7', 'grok-4.7-build-fast', 'grok-4.6', 'grok-4.5']),
});

/** Every catalog id, flattened, in a stable (provider-then-declaration) order. */
export const ALL_KNOWN_IDS = Object.freeze(Object.values(KNOWN_IDS).flat());

/** The frozen empty array every "no ids for this provider" path returns — one shared instance. */
const EMPTY_IDS = Object.freeze([]);

/**
 * `Object.hasOwn`-guarded, not a bare `KNOWN_IDS[provider]` index: an UNGUARDED lookup walks the
 * prototype chain, so `provider === 'constructor'` (or `'toString'`, `'__proto__'`, …) would
 * return a built-in `Object` member instead of `undefined` — a config-supplied string reaching
 * this before the schema's `enum` has rejected it (e.g. inside a still-being-validated domain
 * rule) must never resolve to a function.
 * @param {string} provider
 * @returns {ReadonlyArray<string>} the catalog for `provider`, or an empty array for an unknown one.
 */
export function knownIdsForProvider(provider) {
  return typeof provider === 'string' && Object.hasOwn(KNOWN_IDS, provider)
    ? KNOWN_IDS[/** @type {Provider} */ (provider)]
    : EMPTY_IDS;
}

/**
 * @param {string} provider
 * @param {string} model
 * @returns {boolean}
 */
export function isKnownId(provider, model) {
  return knownIdsForProvider(provider).includes(model);
}

/**
 * Vendor prefixes the lint also flags even for an id NOT in the catalog yet (a future release).
 * Each pattern requires a DIGIT to appear within the SAME hyphen-word run right after the
 * vendor's own hyphen — `<vendor>-<letters>?-?<digit>` — never a bare `\bclaude-[0-9a-z]`, which
 * also matched ordinary prose like "Claude-compatible", "GPT-style", "grok-cli", "claude-code"
 * (fix round 1, MAJOR §35/36 in the review). An earlier tightened draft enumerated only the four
 * KNOWN family words (opus/sonnet/haiku/fable) — that excluded the false positives too, but also
 * defeated this module's own stated purpose ("flags even for an id NOT in the catalog yet"): a
 * genuinely NEW family name (`claude-nova-9`) would have gone unflagged. `[a-z]*` accepts ANY
 * family word (or none), so the pattern stays open to a future release while still requiring the
 * digit that separates a real id from ordinary hyphenated prose.
 * @type {ReadonlyArray<RegExp>}
 */
const VENDOR_PREFIX_PATTERNS = Object.freeze([
  /\bclaude-[a-z]*-?\d/i,
  /\bgpt-[a-z]*-?\d/i,
  /\bgrok-[a-z]*-?\d/i,
  /\bgemini-[a-z]*-?\d/i,
]);

/**
 * Escape every regex metacharacter in `text` so it can be embedded literally inside a larger
 * pattern (a raw catalog id like `grok-4.7` contains a `.`, which is "any character" unescaped).
 * @param {string} text
 * @returns {string}
 */
function escapeRegExp(text) {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** Aliases short enough to appear as a bare word in prose — only flagged in VALUE position (C1). */
const VALUE_POSITION_ALIASES = Object.freeze(['opus', 'sonnet', 'haiku', 'fable', 'luna', 'sol', 'astra']);

/**
 * The lint pattern set (C1): every catalog id, escaped literally, as its own whole-word pattern;
 * one pattern per vendor prefix (`\bclaude-[0-9a-z]`, …); one pattern for an alias that only
 * appears after `--model`/`model:` (so "Solo", "resolve", "console", "fable-forge", "Jev" — none
 * of which are `--model <alias>` — stay at 0 hits, the positive-control requirement B14's test
 * carries over from this module).
 *
 * `>= 11` catalog patterns + `4` vendor-prefix patterns, both counted by B14's test against this
 * export directly (not a hardcoded copy), so the set can never silently go stale.
 * @type {ReadonlyArray<RegExp>}
 */
export const LINT_PATTERNS = Object.freeze([
  ...ALL_KNOWN_IDS.map((id) => new RegExp(`\\b${escapeRegExp(id)}\\b`)),
  ...VENDOR_PREFIX_PATTERNS,
  // `--model` requires an ACTUAL separator (`=` or whitespace) before the alias — the old bare
  // `\s*` (zero-or-more) let `=` slip through unconsumed, so `--model=opus` never matched
  // (fix round 1, MINOR §37/38). The `model:` YAML-key form keeps `\s*` (zero-or-more): a bare
  // `model:"opus"` with no space is still a real value-position use.
  new RegExp(`(?:--model[=\\s]+|model:\\s*)["']?(?:${VALUE_POSITION_ALIASES.join('|')})\\b`, 'i'),
]);

/**
 * @typedef {object} ResolvedLevel
 * @property {Provider} provider - the per-level `provider` override when set, else the top-level `provider`.
 * @property {string} model
 * @property {string | undefined} effort
 * @property {ReadonlyArray<{provider: string, model: string, effort?: string}>} fallback - left as
 *   plain `string` (not the `Provider` union) on purpose: a fallback entry comes straight from the
 *   user's config and isn't narrowed/validated by this pure function — that's `validate.mjs`'s job.
 */

/**
 * Pure: resolves level `levelName` (`"L0"`..`"L3"`) from an already-loaded config object. Never
 * reads a file, never spawns a process, never applies a provider default the config itself didn't
 * set — `load.mjs` is responsible for merging `defaults/*.mjs` into `cfg` before this is called.
 *
 * @param {Record<string, any>} cfg - shaped like `{provider?, levels: {L0..L3: {model, effort?,
 *   provider?, fallback?}}}`, loosely typed so a hand-built (possibly partial/malformed) test
 *   fixture and a real loaded config both type-check the same way.
 * @param {"L0" | "L1" | "L2" | "L3"} levelName
 * @returns {ResolvedLevel}
 * @throws {Error} when `cfg.levels[levelName]` is missing or has no `model` — resolving an absent
 *   level is a caller bug (the schema already requires all four; a hand-built `cfg` in a test
 *   that skips validation is the only way to hit this).
 * @throws {Error} when neither the level's own `provider` nor the top-level `cfg.provider` is set
 *   — resolving to `provider: undefined` would hand every caller a `ResolvedLevel` whose declared
 *   type (`Provider`) it doesn't actually satisfy (MINOR §39/40 fix).
 */
export function resolveLevel(cfg, levelName) {
  const level = cfg?.levels?.[levelName];
  if (!level || typeof level.model !== 'string' || level.model.length === 0) {
    throw new Error(`resolveLevel: levels.${levelName} is missing or has no model`);
  }
  const rawProvider = level.provider ?? cfg?.provider;
  if (typeof rawProvider !== 'string' || rawProvider.length === 0) {
    throw new Error(`resolveLevel: levels.${levelName} has no provider (neither per-level nor top-level)`);
  }
  const provider = /** @type {Provider} */ (rawProvider);
  return Object.freeze({
    provider,
    model: level.model,
    effort: level.effort,
    fallback: Object.freeze((level.fallback ?? []).map((f) => Object.freeze({ ...f }))),
  });
}
