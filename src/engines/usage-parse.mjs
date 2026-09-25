/**
 * Token-usage extraction from a provider CLI's own JSON output (plan §6.1: "Tokens: reported when
 * the CLI's JSON carries usage (`claude -p --output-format json` does `[A2]`; Codex/Grok
 * best-effort `[A14]`), else `estimated`. **Tokens are facts, dollars are estimates.**").
 *
 * **Not independently verified on this Mac** — the plan's own `[A2]`/`[A14]` tags mean the exact
 * JSON field names were not read from a real run for this revision (only `--help` text was; §0.6.8
 * tolerates this for the ledger's `tokens_source` field). This parser is deliberately lenient: it
 * tries the field names each vendor's own API/CLI documentation uses elsewhere, and falls back to
 * `tokensSource: "estimated"` rather than throwing when none match — a shape drift here must never
 * crash a review or a coder run, only degrade the ledger's tokens field, exactly as §6.1 describes.
 *
 * **Fix round 1 (isolated per-file review):**
 *  - **Claude**: `input_tokens` alone undercounts a cached run — Anthropic's Messages API (which
 *    Claude Code's own JSON result reuses) reports prompt-cache reads/writes as SEPARATE fields
 *    (`cache_creation_input_tokens`, `cache_read_input_tokens`), NOT folded into `input_tokens`.
 *    `tokensIn` is now their sum, and the individual cache fields are kept on the returned record
 *    (`cacheCreationTokens`/`cacheReadTokens`) so a caller doesn't lose the split.
 *  - `PARSERS[cli]`/`Object.hasOwn`: the cli lookup is now guarded so `parseUsage('constructor',
 *    …)` (and `'toString'`, `'__proto__'`, …) hits the documented "unknown cli" error instead of a
 *    prototype-chain member.
 *  - Codex/Grok no longer mix naming schemes within one payload: `tokensIn` from `input_tokens` and
 *    `tokensOut` from `completion_tokens` (two different vendors' field names) never happens now —
 *    one scheme is picked (input_tokens/output_tokens if BOTH present, else prompt_tokens/
 *    completion_tokens if BOTH present, else `estimated`).
 *  - JSDoc on `parseCodexUsage` now names exactly which Codex `--json` event a caller must pass.
 */

/**
 * @typedef {{tokensIn: number, tokensOut: number, tokensSource: "reported", cacheCreationTokens?: number, cacheReadTokens?: number}
 *   | {tokensIn: null, tokensOut: null, tokensSource: "estimated"}} UsageResult
 */

const ESTIMATED = Object.freeze({ tokensIn: null, tokensOut: null, tokensSource: /** @type {"estimated"} */ ('estimated') });

/**
 * @param {unknown} value
 * @returns {value is Record<string, unknown>}
 */
function isPlainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

/**
 * @param {unknown} value
 * @returns {number | undefined}
 */
function asNonNegativeInt(value) {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : undefined;
}

/**
 * Claude Code `-p --output-format json` result shape: a top-level `usage` object with
 * `input_tokens`/`output_tokens` (Anthropic Messages API field names, which Claude Code's own JSON
 * result reuses), PLUS `cache_creation_input_tokens`/`cache_read_input_tokens` when prompt caching
 * was used — both optional, defaulting to 0, folded into `tokensIn` and also kept split.
 * @param {unknown} parsed
 * @returns {UsageResult}
 */
function parseClaudeUsage(parsed) {
  if (!isPlainObject(parsed) || !isPlainObject(parsed.usage)) return ESTIMATED;
  const usage = parsed.usage;
  const baseIn = asNonNegativeInt(usage.input_tokens);
  const tokensOut = asNonNegativeInt(usage.output_tokens);
  if (baseIn === undefined || tokensOut === undefined) return ESTIMATED;
  // Optional cache fields: absent OR `null` is legitimate (no caching happened; the Messages API
  // types both as `integer | null`) and treated as 0 without setting the split field. A
  // present, non-null, malformed value (negative, non-integer) must not be silently ignored — it
  // falls back to the whole result being `estimated`, same as a malformed base field would.
  const hasCacheCreation = usage.cache_creation_input_tokens != null;
  const hasCacheRead = usage.cache_read_input_tokens != null;
  const cacheCreation = hasCacheCreation ? asNonNegativeInt(usage.cache_creation_input_tokens) : 0;
  const cacheRead = hasCacheRead ? asNonNegativeInt(usage.cache_read_input_tokens) : 0;
  if (cacheCreation === undefined || cacheRead === undefined) return ESTIMATED;
  return {
    tokensIn: baseIn + cacheCreation + cacheRead,
    tokensOut,
    tokensSource: 'reported',
    ...(hasCacheCreation ? { cacheCreationTokens: cacheCreation } : {}),
    ...(hasCacheRead ? { cacheReadTokens: cacheRead } : {}),
  };
}

/**
 * @param {Record<string, unknown>} usage
 * @returns {{tokensIn: number, tokensOut: number} | undefined} both fields from ONE naming scheme
 *   only — `input_tokens`/`output_tokens` if BOTH are present and valid, else `prompt_tokens`/
 *   `completion_tokens` if BOTH are, else `undefined`. Never mixes a field from one scheme with a
 *   field from the other (fix round 1, MINOR).
 */
function oneSchemeTokens(usage) {
  const anthropicIn = asNonNegativeInt(usage.input_tokens);
  const anthropicOut = asNonNegativeInt(usage.output_tokens);
  if (anthropicIn !== undefined && anthropicOut !== undefined) {
    return { tokensIn: anthropicIn, tokensOut: anthropicOut };
  }
  const openaiIn = asNonNegativeInt(usage.prompt_tokens);
  const openaiOut = asNonNegativeInt(usage.completion_tokens);
  if (openaiIn !== undefined && openaiOut !== undefined) {
    return { tokensIn: openaiIn, tokensOut: openaiOut };
  }
  return undefined;
}

/**
 * Codex `--json` prints one JSONL EVENT per line, not a single result object; usage lives on the
 * turn-completion event (the one carrying a `usage` object — Codex's own docs do not name a fixed
 * event-type string this build can pin against yet, `[A14]`). **The caller is responsible for
 * locating and passing that ONE parsed event object** — this function does not scan a list of
 * events or sum usage across turns; a caller with a multi-turn run must do that summation itself
 * before calling this, or accept per-turn `estimated`/`reported` rows.
 * @param {unknown} parsed - one already-located, already-parsed Codex event object.
 * @returns {UsageResult}
 */
function parseCodexUsage(parsed) {
  if (!isPlainObject(parsed) || !isPlainObject(parsed.usage)) return ESTIMATED;
  const tokens = oneSchemeTokens(parsed.usage);
  return tokens ? { ...tokens, tokensSource: 'reported' } : ESTIMATED;
}

/**
 * Grok `usage` subcommand / JSON output: best-effort, same single-scheme field acceptance as
 * Codex — no verified sample exists for either.
 * @param {unknown} parsed
 * @returns {UsageResult}
 */
function parseGrokUsage(parsed) {
  return parseCodexUsage(parsed);
}

/** @type {Readonly<Record<"claude"|"codex"|"grok", (parsed: unknown) => UsageResult>>} */
const PARSERS = Object.freeze({ claude: parseClaudeUsage, codex: parseCodexUsage, grok: parseGrokUsage });

/**
 * @param {"claude"|"codex"|"grok"} cli
 * @param {unknown} parsedJSON - already `JSON.parse`d CLI output (this module never parses raw
 *   text itself — a malformed JSON string is the caller's problem to report, not silently degrade
 *   into "estimated" here).
 * @returns {UsageResult}
 * @throws {Error} for an unknown `cli` — guarded with `Object.hasOwn` so a prototype-chain name
 *   (`"constructor"`, `"toString"`, `"__proto__"`, …) hits this same error instead of an unrelated
 *   crash or a wrong result (fix round 1, MINOR).
 */
export function parseUsage(cli, parsedJSON) {
  if (typeof cli !== 'string' || !Object.hasOwn(PARSERS, cli)) {
    throw new Error(`parseUsage: unknown cli "${cli}"`);
  }
  return PARSERS[/** @type {"claude"|"codex"|"grok"} */ (cli)](parsedJSON);
}
