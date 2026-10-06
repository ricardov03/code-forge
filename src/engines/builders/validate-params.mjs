/**
 * Shared parameter validation for the three provider builders (fix round 1, findings #2 MAJOR ×2
 * + cwd MAJOR). Written once so `claude.mjs`/`codex.mjs`/`grok.mjs` cannot drift from each other on
 * what counts as a valid `role`/`model`/`promptPath`/`cwd` — the review found `codex.mjs` and
 * `grok.mjs` silently routed ANY non-'coder' string (a typo, `undefined`, a stray level name like
 * `'L2'`) into the closed-book branch instead of refusing it.
 */

/**
 * The six roles the plan's engine adapters section (§5.2) declares, plus the autopilot `delegate` (B46, closed-book).
 * @type {ReadonlyArray<"coder"|"reviewer"|"judge"|"s2"|"author"|"facts"|"delegate">}
 */
export const VALID_ROLES = Object.freeze(/** @type {const} */ (['coder', 'reviewer', 'judge', 's2', 'author', 'facts', 'delegate']));

/**
 * @param {string} builderName - for the thrown message, e.g. `"buildClaudeArgv"`.
 * @param {unknown} params
 * @throws {TypeError} for a missing/invalid `role`, `model`, `promptPath` or `cwd`.
 */
export function assertBaseParams(builderName, params) {
  const p = /** @type {Record<string, unknown> | null | undefined} */ (params);
  if (!p || typeof p !== 'object' || !VALID_ROLES.includes(/** @type {any} */ (p.role))) {
    throw new TypeError(`${builderName}: params.role must be one of ${VALID_ROLES.join(', ')}, got ${JSON.stringify(p?.role)}`);
  }
  if (typeof p.model !== 'string' || p.model.length === 0) {
    throw new TypeError(`${builderName}: params.model is required (non-empty string)`);
  }
  if (typeof p.promptPath !== 'string' || p.promptPath.length === 0) {
    throw new TypeError(`${builderName}: params.promptPath is required (non-empty string)`);
  }
  if (typeof p.cwd !== 'string' || p.cwd.length === 0) {
    throw new TypeError(`${builderName}: params.cwd is required (non-empty string)`);
  }
}
