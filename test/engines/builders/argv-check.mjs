/**
 * Test-only argv verification helpers (fix round 1). Moved here from `src/engines/render-rules.mjs`
 * (MINOR finding: nothing under `src/` imported `idsMissingFromArgv` — it existed only for tests).
 * Two review fixes land here too:
 *  - MAJOR: `idsMissingFromArgv` used `entry.rules.some(...)` — an entry with SOME but not ALL of
 *    its rules present in argv (e.g. Read(…) kept, Edit(…)/Write(…) dropped) was wrongly reported
 *    as "contains every forbidden entry". Now `.every`.
 *  - MINOR: an entry with `rules.length === 0` was always treated as legitimately absent. Now only
 *    an `enforced: false` entry is — an `enforced: true` entry with 0 rules (a renderer bug) counts
 *    as missing.
 *  - MINOR: presence was checked against the WHOLE argv (a `Set` of every token), so a rule string
 *    that coincidentally equalled some unrelated argv value would false-positive. `valuesAfterFlag`
 *    / `valuesAfterRepeatedFlag` scope the search to the actual flag's value slot(s) first.
 */

/** @typedef {{id: string, rules: ReadonlyArray<string>, enforced: boolean}} RenderedEntry */

/**
 * @param {ReadonlyArray<RenderedEntry>} rendered
 * @param {ReadonlyArray<string>} candidateValues - the argv VALUES actually attached to the
 *   relevant flag (from {@link valuesAfterFlag} or {@link valuesAfterRepeatedFlag}), never the raw
 *   whole argv array.
 * @returns {string[]} entry ids missing from `candidateValues`, in `rendered` order.
 */
export function idsMissingFromArgv(rendered, candidateValues) {
  const present = new Set(candidateValues);
  return rendered
    .filter((entry) => {
      if (entry.rules.length === 0) {
        // Legitimately unenforceable (e.g. `production-marker`, or a `path` entry under Codex) —
        // never "missing". An ENFORCED entry with 0 rules is a renderer regression and DOES count.
        return entry.enforced === true;
      }
      return !entry.rules.every((rule) => present.has(rule));
    })
    .map((entry) => entry.id);
}

/**
 * The argv VALUES owned by a single-occurrence variadic-or-scalar flag like Claude's
 * `--disallowedTools <tools...>`: every token after `flagName` up to (not including) the next
 * token that starts with `-` or is the literal `--` end-of-options marker, or the end of argv.
 * @param {ReadonlyArray<string>} argv
 * @param {string} flagName
 * @returns {string[]}
 */
export function valuesAfterFlag(argv, flagName) {
  const start = argv.indexOf(flagName);
  if (start === -1) return [];
  const values = [];
  for (let i = start + 1; i < argv.length; i += 1) {
    const token = argv[i];
    if (token === '--' || token.startsWith('-')) break;
    values.push(token);
  }
  return values;
}

/**
 * The argv value attached to EVERY occurrence of a repeated single-value flag like Grok's
 * `--deny <rule>` (one value immediately following each occurrence).
 * @param {ReadonlyArray<string>} argv
 * @param {string} flagName
 * @returns {string[]}
 */
export function valuesAfterRepeatedFlag(argv, flagName) {
  const values = [];
  for (let i = 0; i < argv.length; i += 1) {
    if (argv[i] === flagName && i + 1 < argv.length) {
      values.push(argv[i + 1]);
    }
  }
  return values;
}
