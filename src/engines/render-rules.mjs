/**
 * Small shared helpers over a forbidden-list RENDER — the shape `renderForClaude`/`renderForGrok`
 * (`{id, rules: string[], enforced}[]`) or `renderForCodex` (`{id, patterns: string[][],
 * enforced}[]`) from `src/util/forbidden.mjs` (B0/B8) return. Used by `builders/claude.mjs` and
 * `builders/grok.mjs` so the "flatten every entry's rules into one argv run" logic exists once,
 * not twice, and by all three coder builders to refuse an empty render (fix round 1, MAJOR: a
 * coder role must always carry a non-empty forbidden list — a renderer regression that returns
 * nothing must be a thrown error, not a silently-empty `--disallowedTools`/rules file).
 *
 * `idsMissingFromArgv` moved OUT of this file in fix round 1 (MINOR, dead-code finding): nothing
 * under `src/` ever called it — only tests did — so it now lives in
 * `test/engines/builders/argv-check.mjs`, alongside the `.every`-not-`.some` fix and two new
 * position-aware helpers the review asked for.
 */

/**
 * @typedef {{id: string, rules?: ReadonlyArray<string>, patterns?: ReadonlyArray<ReadonlyArray<string>>, enforced: boolean}} RenderedEntry
 */

/**
 * @param {ReadonlyArray<{id: string, rules: ReadonlyArray<string>, enforced: boolean}>} rendered
 * @returns {string[]} every rule string of every entry, in entry order, entry-internal order
 *   preserved (an `enforced: false` entry contributes an empty `rules` array, so it drops out
 *   naturally rather than needing a separate filter).
 */
export function flattenRuleStrings(rendered) {
  return rendered.flatMap((entry) => [...entry.rules]);
}

/**
 * Whether a render has AT LEAST ONE usable rule/pattern anywhere across its entries — the guard a
 * coder-role builder throws on when this is false (a coder must always carry a real forbidden
 * list; an empty one is a renderer regression, never a legitimate state). Works for both the
 * Claude/Grok shape (`rules`) and the Codex shape (`patterns`) without the caller needing to know
 * which.
 * @param {ReadonlyArray<RenderedEntry>} rendered
 * @returns {boolean}
 */
export function hasAnyRenderedRule(rendered) {
  return rendered.some((entry) => (entry.rules?.length ?? 0) > 0 || (entry.patterns?.length ?? 0) > 0);
}
