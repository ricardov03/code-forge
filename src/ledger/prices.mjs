/**
 * 0.1's static cost table (plan §4.10, R4): forecast tokens per review depth, and a $/1K-token
 * price table used ONLY to estimate `cost_usd`. Fixture figures, never a live price feed — no
 * network call ever backs a number here; the 0.2 ledger-averaged forecast replaces this table once
 * ≥ 10 real samples exist per depth (§4.10).
 */

/** Estimated total (in+out) review tokens per depth — §4.10's static table, verbatim. */
export const REVIEW_DEPTH_TOKENS = Object.freeze({
  quick: 7000,
  full: 13000,
  'a-b-judge': 34000,
  consensus: 30000,
});

/** $ per 1000 tokens (blended in+out) by provider and level — placeholder fixture prices. */
export const PRICE_PER_1K_TOKENS_USD = Object.freeze({
  anthropic: Object.freeze({ L0: 0.001, L1: 0.003, L2: 0.015, L3: 0.03 }),
  openai: Object.freeze({ L0: 0.001, L1: 0.0025, L2: 0.012, L3: 0.025 }),
  xai: Object.freeze({ L0: 0.001, L1: 0.002, L2: 0.01, L3: 0.02 }),
});

/**
 * @param {{provider: string, level: string, tokensIn?: number, tokensOut?: number}} args
 * @returns {number} an ESTIMATED cost in USD, rounded to 4 decimal places.
 */
export function estimateCostUsd({ provider, level, tokensIn = 0, tokensOut = 0 }) {
  const table = PRICE_PER_1K_TOKENS_USD[provider];
  if (!table) throw new RangeError(`estimateCostUsd: unknown provider "${provider}"`);
  const perK = table[level];
  if (typeof perK !== 'number') throw new RangeError(`estimateCostUsd: unknown level "${level}" for "${provider}"`);
  // A ledger row's counts come from a JSON.parse'd file, not a type system — a string ("100"+"50"
  // string-concatenates instead of adding), NaN, or a negative count must fail loudly here rather
  // than quietly corrupting every downstream cost total.
  if (!Number.isFinite(tokensIn) || tokensIn < 0) throw new RangeError(`estimateCostUsd: tokensIn must be a finite number >= 0, got ${JSON.stringify(tokensIn)}`);
  if (!Number.isFinite(tokensOut) || tokensOut < 0) throw new RangeError(`estimateCostUsd: tokensOut must be a finite number >= 0, got ${JSON.stringify(tokensOut)}`);
  return Math.round(((tokensIn + tokensOut) / 1000) * perK * 10000) / 10000;
}

/**
 * @param {string} depth - one of `quick`, `full`, `a-b-judge`, `consensus`; anything else throws
 *   (a `string`, not the literal union, since a CLI/ledger caller has no compile-time guarantee).
 * @returns {number}
 */
export function forecastTokensForDepth(depth) {
  const tokens = REVIEW_DEPTH_TOKENS[depth];
  if (typeof tokens !== 'number') throw new RangeError(`forecastTokensForDepth: unknown depth "${depth}"`);
  return tokens;
}
