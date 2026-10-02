/**
 * The error kind a verb reports for the error log (B27). A module that classifies a failure (for
 * example a 1Password failure: `op_timeout`) calls `reportErrorKind(kind)`; the router clears it
 * before a verb runs and takes the last one reported when the verb fails. A verb that reports
 * nothing is logged as `error` (or `usage`/`crash`, see `bin/code-forge.mjs`).
 */

/** A kind is a short snake_case word; anything else is ignored. */
const KIND = /^[a-z][a-z0-9_]{0,39}$/;

/** Kinds the router (and `logWarning`, B37) set themselves; a verb reporting one of these is ignored. */
const RESERVED = new Set(['crash', 'usage', 'error', 'warning']);

/** @type {string|null} */
let current = null;

/** @param {unknown} kind */
export function reportErrorKind(kind) {
  if (typeof kind === 'string' && KIND.test(kind) && !RESERVED.has(kind)) current = kind;
}

/** Forget any reported kind (the router calls this before a verb runs). */
export function clearErrorKind() {
  current = null;
}

/** @returns {string|null} the last kind reported since the last clear; clears it. */
export function takeErrorKind() {
  const kind = current;
  current = null;
  return kind;
}
