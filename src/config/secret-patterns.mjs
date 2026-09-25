/**
 * Secret-SHAPED text detection, shared by the validator's `secret-looking-value` rule and by every
 * B1 path that prints text derived from a user's files (`validate.mjs` messages, `load.mjs` parse
 * errors, `refresh.mjs` skip reasons, the `validate`/`models` verbs).
 *
 * This is the SHAPE layer. B0's `redact()` is the other layer: it masks secrets a key resolver
 * has explicitly registered, and deliberately never guesses. Config text is never registered — it
 * is exactly where a user pastes a key by mistake — so B1 masks anything shaped like a vendor
 * secret on top of `redact()`.
 */

/**
 * Conservative, prefix-shaped patterns for common vendor secret formats. Deliberately NOT a
 * generic high-entropy/long-string heuristic: a model id like `claude-haiku-4-5-20251001` or a
 * `keys.jev` reference like `op://vault/item/field` is exactly the kind of long, dash-heavy,
 * mixed-alnum string a naive entropy check would also flag — refusing every valid config forever.
 * Every pattern anchors on a real-world secret PREFIX no legitimate config value shares.
 *
 * NOT `^…$`-anchored: a token embedded in a longer string (a gates argv element
 * `Authorization: Bearer sk-…`) must be caught too. The lookbehind is the word-boundary stand-in —
 * the prefix may not be glued to a preceding identifier character, so `task-…` or `disk-…` never
 * match the `sk-` pattern. No `g`/`y` flag: `.test()` must carry no `lastIndex` state.
 */
export const SECRET_LOOKING_PATTERNS = Object.freeze([
  /(?<![A-Za-z0-9_-])sk-[A-Za-z0-9_-]{10,}/, // OpenAI / Anthropic API keys
  /(?<![A-Za-z0-9_-])xai-[A-Za-z0-9]{20,}/, // xAI API keys
  /(?<![A-Za-z0-9_-])xox[baprs]-[A-Za-z0-9-]{10,}/, // Slack tokens
  /(?<![A-Za-z0-9_-])gh[opsu]_[A-Za-z0-9]{20,}/, // GitHub tokens (classic prefixes)
  /(?<![A-Za-z0-9_-])github_pat_[A-Za-z0-9_]{20,}/, // GitHub fine-grained PATs
  /(?<![A-Za-z0-9_-])AKIA[0-9A-Z]{12,}/, // AWS access key id
  /(?<![A-Za-z0-9_-])AIza[0-9A-Za-z_-]{20,}/, // Google API key
  /(?<![A-Za-z0-9_-])eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}/, // JWT
]);

/** The mask every B1 scrub writes in place of a secret-shaped token (same text as B0's `redact`). */
export const SECRET_MASK = '[REDACTED]';

/**
 * A `keys.<name>` value is a REFERENCE (`env:NAME`, `op://…`, `user`), never a secret, by design.
 * @param {unknown} value
 * @returns {boolean}
 */
export function isKeyReferenceValue(value) {
  return typeof value === 'string' && (value.startsWith('env:') || value.startsWith('op://') || value === 'user');
}

/**
 * @param {unknown} value
 * @returns {boolean} true when `value` is a string holding at least one secret-shaped token and is
 *   not itself a key reference.
 */
export function looksLikeSecret(value) {
  if (typeof value !== 'string' || isKeyReferenceValue(value)) {
    return false;
  }
  return SECRET_LOOKING_PATTERNS.some((pattern) => pattern.test(value));
}

/**
 * @param {string} text
 * @returns {string[]} every secret-shaped token inside `text` (the matched substrings).
 */
export function secretTokensIn(text) {
  const tokens = [];
  for (const pattern of SECRET_LOOKING_PATTERNS) {
    for (const match of text.matchAll(new RegExp(pattern.source, 'g'))) {
      tokens.push(match[0]);
    }
  }
  return tokens;
}

/**
 * Masks every secret-shaped token in `text`. Applied to OUTPUT text (a message, a reason), so no
 * key-reference exemption: a message never needs to show a token in full.
 * @param {string} text
 * @returns {string}
 */
export function maskSecretTokens(text) {
  let out = text;
  for (const pattern of SECRET_LOOKING_PATTERNS) {
    out = out.replace(new RegExp(pattern.source, 'g'), SECRET_MASK);
  }
  return out;
}
