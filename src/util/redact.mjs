/**
 * Secret redaction — the one gate every output path must pass through before a resolved key
 * can reach a log line, a raw stdout write, or a JSON dump (plan §8.2: "as v1 §8.2 — unit test
 * feeds a fake key through every path, mutation-verified").
 *
 * Secrets are opt-in: whatever resolves a key (`src/keys/**`, a later block) calls
 * `registerSecret(value)` once it has the plaintext. Nothing here guesses at what "looks like" a
 * secret — that would either over-redact ordinary text or under-redact a key shaped unlike the
 * guess. A registered secret is scrubbed everywhere, forever, until `clearSecrets()` (tests only).
 */

/** @type {Set<string>} */
const secrets = new Set();

const MASK = '[REDACTED]';

/**
 * Register a value that must never appear verbatim in any output. No-ops for empty/non-string
 * input so an unresolved key can't accidentally register `undefined` as a literal string.
 *
 * @param {unknown} value
 */
export function registerSecret(value) {
  if (typeof value === 'string' && value.length > 0) {
    secrets.add(value);
  }
}

/** Forget every registered secret. Exists for tests; production code never needs it. */
export function clearSecrets() {
  secrets.clear();
}

/**
 * Redact every registered secret out of a string, or out of every string leaf (and every key) of
 * an object/array. For a non-string input the result is a JSON-SAFE COPY, not a value of the
 * input's type: `Map` becomes a plain object, `Set` an array, anything with `toJSON` (e.g. `Date`)
 * its `toJSON()` result, and a cycle the string `'[Circular]'` — see `redactDeep`. A string in
 * gives a string out.
 *
 * @overload
 * @param {string} input
 * @returns {string}
 */
/**
 * @overload
 * @param {unknown} input
 * @returns {unknown}
 */
/**
 * @param {unknown} input
 * @returns {unknown}
 */
export function redact(input) {
  if (typeof input === 'string') {
    return redactString(input);
  }
  if (input !== null && typeof input === 'object') {
    return redactDeep(input, new WeakSet());
  }
  return input;
}

/**
 * Mask every character covered by any registered secret, in one pass over the ORIGINAL text:
 * first record the [start, end) range of every occurrence of every secret, then merge
 * overlapping/adjacent ranges and replace each merged range with one mask. Replacing secrets one
 * after another instead would leak the tail of a secret that overlaps another without being
 * nested in it ("abcd" + "cdef" in "abcdef" → "[REDACTED]ef"), and let a short secret match
 * inside an earlier mask.
 * @param {string} text
 * @returns {string}
 */
function redactString(text) {
  /** @type {[number, number][]} */
  const ranges = [];
  for (const secret of secrets) {
    let at = text.indexOf(secret);
    while (at !== -1) {
      ranges.push([at, at + secret.length]);
      at = text.indexOf(secret, at + 1);
    }
  }
  if (ranges.length === 0) {
    return text;
  }
  ranges.sort((a, b) => a[0] - b[0]);
  let out = '';
  let cursor = 0;
  let [start, end] = ranges[0];
  for (const [s, e] of ranges.slice(1)) {
    if (s <= end) {
      end = Math.max(end, e);
      continue;
    }
    out += text.slice(cursor, start) + MASK;
    cursor = end;
    [start, end] = [s, e];
  }
  out += text.slice(cursor, start) + MASK + text.slice(end);
  return out;
}

/**
 * Write `value` under `key`, never overwriting: two different keys that redact to the same text
 * (two secrets → `[REDACTED]`, or two non-string Map keys → `[object Object]`) get `#2`, `#3`, …
 * suffixes so no entry is silently dropped.
 * @param {Record<string, unknown>} out
 * @param {string} key
 * @param {unknown} value
 */
function putUnique(out, key, value) {
  let candidate = key;
  let n = 2;
  while (Object.hasOwn(out, candidate)) {
    candidate = `${key}#${n}`;
    n += 1;
  }
  out[candidate] = value;
}

/**
 * The structural half of `redact`/`redactJSON`. Deliberately NOT a bare `JSON.stringify`
 * replacer: a replacer callback can rewrite a *value* but not the *key* it sits under, and a
 * secret used as an object key (e.g. a map keyed by token) needs redacting too.
 *
 * - Calls `.toJSON()` when present (before recursing into its result) so `Date`, and anything
 *   else that defines it, serializes as it would under a plain `JSON.stringify` — a raw
 *   `Object.entries` walk over a `Date` sees no own enumerable properties and would otherwise
 *   silently corrupt it to `{}`.
 * - `Map`/`Set` get a JSON-safe shape (a plain object / array respectively) — `JSON.stringify`
 *   has no built-in behavior for either (both would also come out as `{}` natively), so this is
 *   strictly better, not just parity.
 * - A `WeakSet` of objects currently being visited on the CURRENT path (not globally) catches a
 *   circular reference and substitutes `'[Circular]'`, so a cycle degrades to a marker instead of
 *   overflowing the stack — the same object appearing twice in unrelated, non-overlapping
 *   branches (not a real cycle) is not flagged, because it's removed from the set once that
 *   branch finishes.
 *
 * @param {unknown} value
 * @param {WeakSet<object>} seen
 * @returns {unknown}
 */
function redactDeep(value, seen) {
  if (typeof value === 'string') {
    return redactString(value);
  }
  if (value === null || typeof value !== 'object') {
    return value;
  }
  if (typeof (/** @type {{toJSON?: unknown}} */ (value).toJSON) === 'function') {
    return redactDeep(/** @type {{toJSON: () => unknown}} */ (value).toJSON(), seen);
  }
  if (seen.has(value)) {
    return '[Circular]';
  }
  seen.add(value);
  try {
    if (Array.isArray(value)) {
      return value.map((item) => redactDeep(item, seen));
    }
    if (value instanceof Map) {
      /** @type {Record<string, unknown>} */
      const out = {};
      for (const [key, v] of value.entries()) {
        putUnique(out, redactString(String(key)), redactDeep(v, seen));
      }
      return out;
    }
    if (value instanceof Set) {
      return [...value].map((item) => redactDeep(item, seen));
    }
    /** @type {Record<string, unknown>} */
    const out = {};
    for (const [key, v] of Object.entries(value)) {
      putUnique(out, redactString(key), redactDeep(v, seen));
    }
    return out;
  } finally {
    seen.delete(value);
  }
}

/**
 * Redact-then-serialize — the "JSON path": callers that dump a structured value (a ledger row,
 * a `--json` CLI response) must go through this instead of a bare `JSON.stringify`.
 *
 * @param {unknown} value
 * @param {number} [space]
 * @returns {string}
 */
export function redactJSON(value, space) {
  return JSON.stringify(redact(value), null, space);
}

/**
 * Redact-then-write — the "stdout path": a raw write to a stream (e.g. a `--json` flag piping
 * straight to `process.stdout`) that does not go through `./log.mjs`.
 *
 * Redacts each call's `text` independently — a secret split across two separate `write` calls
 * (a chunk boundary landing mid-secret) will NOT be caught. Callers must pass complete messages,
 * not partial chunks, the same way `./log.mjs` does (one full line per call).
 *
 * The parameter type is the minimal shape this function actually uses (just `.write`), not the
 * full `NodeJS.WritableStream` — so a plain `{ write(text) {...} }` test double type-checks
 * without needing to fake the other dozen `WritableStream` members.
 * @param {{write: (chunk: string) => unknown}} stream
 * @param {string} text
 */
export function writeSafe(stream, text) {
  stream.write(redactString(text));
}
