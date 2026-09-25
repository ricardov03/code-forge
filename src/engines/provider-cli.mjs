/**
 * The one place that maps a config `provider` to the CLI binary `engine: subprocess` spawns for
 * it (plan §5.2). `src/cli/validate.mjs` (B1) keeps its own private copy for `hasCliOnPath` — this
 * module is the shared, exported version every `src/engines/**` builder and `src/cli/resolve.mjs`
 * use, so the mapping is written once on this side of the seam (B1 landed first; B4 does not edit
 * B1's file to add an export it didn't already have).
 */

/** @typedef {"anthropic" | "openai" | "xai"} Provider */

/** @type {Readonly<Record<Provider, string>>} */
export const PROVIDER_CLI_NAMES = Object.freeze({ anthropic: 'claude', openai: 'codex', xai: 'grok' });

/**
 * `Object.hasOwn`-guarded (see `known-ids.mjs`'s `knownIdsForProvider` for why a bare index lookup
 * is unsafe for a string that might be `"constructor"` or another `Object.prototype` member).
 * @param {string} provider
 * @returns {string | undefined} the CLI binary name, or `undefined` for an unknown provider.
 */
export function cliNameForProvider(provider) {
  return typeof provider === 'string' && Object.hasOwn(PROVIDER_CLI_NAMES, provider)
    ? PROVIDER_CLI_NAMES[/** @type {Provider} */ (provider)]
    : undefined;
}
