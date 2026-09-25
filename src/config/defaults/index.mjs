/**
 * One lookup table over the three per-provider default matrices, so `load.mjs` (and anything
 * else that needs "the shipped defaults for provider X") imports one thing instead of three.
 */
import anthropic from './anthropic.mjs';
import openai from './openai.mjs';
import xai from './xai.mjs';

/** @type {Record<"anthropic" | "openai" | "xai", {provider: string, levels: object, caps: {coders: number}}>} */
export const PROVIDER_DEFAULTS = Object.freeze({ anthropic, openai, xai });

/**
 * `Object.hasOwn`-guarded (fix round 1, MINOR §31/32): an unguarded `PROVIDER_DEFAULTS[provider]`
 * walks the prototype chain, so `provider === 'constructor'` would return the `Object` function
 * (truthy, with no `.levels`) instead of `undefined` — a caller a few frames up the loader might
 * treat that as "found the defaults" and hand back a levels-less object.
 * @param {string} provider
 * @returns {{provider: string, levels: object, caps: {coders: number}} | undefined}
 */
export function defaultsForProvider(provider) {
  return typeof provider === 'string' && Object.hasOwn(PROVIDER_DEFAULTS, provider)
    ? PROVIDER_DEFAULTS[/** @type {"anthropic" | "openai" | "xai"} */ (provider)]
    : undefined;
}
