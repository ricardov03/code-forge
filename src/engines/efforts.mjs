/**
 * The one source of the reasoning-effort values each provider's CLI accepts (B29, issue #2). The
 * argv builders (`builders/*.mjs`) refuse a bad effort at spawn time and `validateConfig` refuses
 * it at config time — both read THIS table, so the two can never disagree again (the bug: validate
 * accepted `effort: xhigh` on an openai level, then the codex builder threw at spawn).
 *
 * A provider's entry is one of:
 *  - a non-empty list: the closed set of accepted values;
 *  - `null`: the CLI takes an effort but publishes no closed list, so any non-empty string passes;
 *  - an empty list: the provider has no effort concept — an effort set there is refused (the
 *    builder throws, `validateConfig` reports an error). No current provider uses this form.
 *
 * Sources: anthropic — `claude --help` 2.1.282 (`--effort <level>`: low, medium, high, xhigh, max);
 * openai — the `model_reasoning_effort` subset the codex builder has always allowed (it lands in a
 * `-c` TOML override, so it must stay a closed list); xai — `grok --help` 1.0.34 lists
 * `--reasoning-effort <EFFORT>` with no value list.
 *
 * **No raw effort in a message (fix round 1).** An effort is user config and could be a pasted
 * secret, so {@link displayEffort} echoes it only when it is a word from the closed lists below
 * (a constant vocabulary); anything else renders as `(unrecognised value)`.
 */

/** @typedef {"anthropic" | "openai" | "xai"} Provider */

/** @type {Readonly<Record<Provider, ReadonlyArray<string> | null>>} */
export const PROVIDER_EFFORTS = Object.freeze({
  anthropic: Object.freeze(['low', 'medium', 'high', 'xhigh', 'max']),
  openai: Object.freeze(['minimal', 'low', 'medium', 'high']),
  xai: null,
});

/**
 * Every effort word any provider's closed list names — a constant vocabulary, so a value found
 * here may be echoed in a message (it is not user data that could be a secret).
 * @param {Readonly<Record<string, ReadonlyArray<string> | null>>} [table]
 * @returns {ReadonlyArray<string>}
 */
export function knownEffortWords(table = PROVIDER_EFFORTS) {
  return Object.freeze([...new Set(Object.values(table).flatMap((list) => list ?? []))]);
}

const KNOWN_WORDS = knownEffortWords();

/**
 * @param {unknown} effort
 * @returns {string} `"xhigh"` (quoted) for a known effort word, `(unrecognised value)` otherwise.
 */
export function displayEffort(effort) {
  return typeof effort === 'string' && KNOWN_WORDS.includes(effort) ? `"${effort}"` : '(unrecognised value)';
}

/**
 * `kind`: `"ok"`; `"invalid"` (not in the provider's closed list — `allowed` names the list);
 * `"unsupported"` (the provider takes no effort); `"empty"` (free-form provider, value not a
 * non-empty string); `"unknown-provider"` (not a table key — another check reports the provider).
 * @typedef {{kind: "ok" | "invalid" | "unsupported" | "empty" | "unknown-provider", allowed: ReadonlyArray<string>}} EffortCheck
 */

const NONE = Object.freeze(/** @type {string[]} */ ([]));

/**
 * Checks one effort against one provider. `undefined` (no effort set) always passes.
 * @param {unknown} provider
 * @param {unknown} effort
 * @param {Readonly<Record<string, ReadonlyArray<string> | null>>} [table]
 * @returns {EffortCheck}
 */
export function checkEffort(provider, effort, table = PROVIDER_EFFORTS) {
  if (effort === undefined) {
    return { kind: 'ok', allowed: NONE };
  }
  if (typeof provider !== 'string' || !Object.hasOwn(table, provider)) {
    return { kind: 'unknown-provider', allowed: NONE };
  }
  const allowed = table[provider];
  if (allowed === null) {
    return { kind: typeof effort === 'string' && effort.length > 0 ? 'ok' : 'empty', allowed: NONE };
  }
  if (allowed.length === 0) {
    return { kind: 'unsupported', allowed: NONE };
  }
  return { kind: typeof effort === 'string' && allowed.includes(effort) ? 'ok' : 'invalid', allowed };
}

/**
 * The builders' guard: throws the builder-named TypeError for an effort `checkEffort` refuses.
 * The effort is shown through {@link displayEffort} only. An unknown provider is not this guard's
 * concern (each builder is called with its own fixed provider; `buildArgv` refuses unknown ones).
 * @param {string} builderName - e.g. `"buildCodexArgv"`.
 * @param {Provider} provider
 * @param {unknown} effort
 * @param {Readonly<Record<string, ReadonlyArray<string> | null>>} [table]
 * @throws {TypeError}
 */
export function assertEffortForProvider(builderName, provider, effort, table = PROVIDER_EFFORTS) {
  const result = checkEffort(provider, effort, table);
  const shown = displayEffort(effort);
  switch (result.kind) {
    case 'ok':
    case 'unknown-provider':
      return;
    case 'invalid':
      throw new TypeError(`${builderName}: effort must be one of ${result.allowed.join(', ')} when given, got ${shown}`);
    case 'unsupported':
      throw new TypeError(`${builderName}: provider ${provider} takes no effort, got ${shown}`);
    default:
      throw new TypeError(`${builderName}: effort must be a non-empty string when given, got ${shown}`);
  }
}
