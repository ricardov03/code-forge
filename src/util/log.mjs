/**
 * The leveled logger — the "log path" of `./redact.mjs`'s three guarded output paths (the other
 * two are `redact.writeSafe` for raw stream writes and `redact.redactJSON` for structured dumps).
 * Every line printed here is redacted first; nothing in this module bypasses that.
 *
 * Each argument is redacted according to its own shape, not by JSON-serializing everything and
 * redacting the joined string as one blob:
 *  - a `string` goes through `redact()` directly.
 *  - an `Error` (including one nested as `.cause`) is formatted to `name: message` + stack +
 *    cause chain, each part redacted.
 *  - anything else goes through `redactJSON()` (structural — also redacts by key, once
 *    `redact.mjs` does; a plain post-hoc string redact of an already-serialized object cannot).
 * That split exists because a value-blind, string-only redact of a value the caller is trying to
 * see can otherwise vanish outright: `JSON.stringify(undefined)` is `undefined`, not `"undefined"`
 * — joining it into a message silently drops the value instead of printing it.
 */

import { redact, redactJSON } from './redact.mjs';

/**
 * @param {unknown} value
 * @returns {string}
 */
function safeJSONString(value) {
  const serialized = JSON.stringify(value);
  return typeof serialized === 'string' ? serialized : String(value);
}

/**
 * @param {Error} err
 * @returns {string}
 */
function formatError(err) {
  const parts = [err.stack ?? `${err.name}: ${err.message}`];
  if (err.cause !== undefined) {
    parts.push(`Caused by: ${err.cause instanceof Error ? formatError(err.cause) : safeJSONString(err.cause)}`);
  }
  return parts.join('\n');
}

/**
 * @param {unknown} value
 * @returns {string}
 */
function stringifyArg(value) {
  if (typeof value === 'string') {
    return redact(value);
  }
  if (value instanceof Error) {
    return redact(formatError(value));
  }
  try {
    return redactJSON(value);
  } catch {
    return redact(safeJSONString(value));
  }
}

/**
 * @param {NodeJS.WritableStream} stream
 * @param {string} level
 * @param {unknown[]} args
 */
function write(stream, level, args) {
  const message = args.map(stringifyArg).join(' ');
  // Prefix EVERY line, not just the first: an unprefixed line inside a value (a real newline in
  // a caller's string, or a multi-line stack trace) would otherwise look like a second, forged
  // log entry — log-line injection.
  const prefixed = message
    .split('\n')
    .map((line) => `[code-forge:${level}] ${line}`)
    .join('\n');
  stream.write(`${prefixed}\n`);
}

/** @param {...unknown} args */
export function info(...args) {
  write(process.stdout, 'info', args);
}

/** @param {...unknown} args */
export function warn(...args) {
  write(process.stderr, 'warn', args);
}

/** @param {...unknown} args */
export function error(...args) {
  write(process.stderr, 'error', args);
}

/** Values that turn CODE_FORGE_DEBUG on; anything else (including "0" or "false") leaves it off. */
const DEBUG_ENABLED_VALUES = new Set(['1', 'true', 'yes']);

/** @param {...unknown} args */
export function debug(...args) {
  const flag = process.env.CODE_FORGE_DEBUG;
  if (flag !== undefined && DEBUG_ENABLED_VALUES.has(flag.toLowerCase())) {
    write(process.stderr, 'debug', args);
  }
}
