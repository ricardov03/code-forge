/**
 * Acceptance-clause coverage (plan §4.6 row 4, O17): "every clause names ≥ 1 test id; each named
 * test exists in the tree and passed in this gate run (parsed from the runner output)." This is
 * DETERMINISTIC — S1's `scope` question (§3.2) judges only whether hunks OUTSIDE the named tests
 * are unrelated; whether a named test id actually passed is a fact, never a model's opinion.
 *
 * `parseTapPassed` reads real TAP (RFC-ish "TAP version 13") output — the default `node --test`
 * reporter on this Mac (verified: `node --test` with no `--test-reporter` flag prints
 * `TAP version 13` / `ok N - <name>` / `not ok N - <name>`, 2026-09-24) — into passed/failed name
 * sets. A project on a different runner supplies its own `passed` set built however fits that
 * runner's output; `checkAcceptance` itself does not care where the set came from.
 */

/**
 * @typedef {{clause: string, tests: string[]}} AcceptanceClause
 * @typedef {{ok: true} | {ok: false, reason: string, clause: string, test: string}} AcceptanceResult
 */

/** A TAP directive suffix on a test description (`name # SKIP reason`, `name # TODO reason`). */
const TAP_DIRECTIVE = /^(.*?)\s*#\s*(SKIP|TODO)\b/i;

/**
 * @param {string} text - the captured `<name...>` portion of an `ok`/`not ok` TAP line.
 * @returns {{name: string, directive: 'SKIP' | 'TODO' | null}}
 */
function parseNameAndDirective(text) {
  const m = TAP_DIRECTIVE.exec(text);
  return m ? { name: m[1].trim(), directive: /** @type {'SKIP'|'TODO'} */ (m[2].toUpperCase()) } : { name: text.trim(), directive: null };
}

/**
 * @param {string} output - raw TAP text from a test-runner invocation.
 * @returns {{passed: Set<string>, failed: Set<string>}}
 */
export function parseTapPassed(output) {
  /** @type {Set<string>} */
  const passed = new Set();
  /** @type {Set<string>} */
  const failed = new Set();
  for (const rawLine of String(output).split('\n')) {
    const line = rawLine.trim();
    const ok = /^ok\s+\d+\s+-\s+(.+)$/.exec(line);
    if (ok) {
      const { name, directive } = parseNameAndDirective(ok[1]);
      // `node --test` reports a skipped or todo test as an "ok" TAP line with a `# SKIP`/`# TODO`
      // directive — it did not actually run to a real pass, so it must NOT satisfy an acceptance
      // clause (O17: "passed in this gate run", not merely "not reported as failed").
      if (directive === null) passed.add(name);
      continue;
    }
    const notOk = /^not ok\s+\d+\s+-\s+(.+)$/.exec(line);
    if (notOk) {
      failed.add(parseNameAndDirective(notOk[1]).name);
    }
  }
  return { passed, failed };
}

/**
 * @param {ReadonlyArray<AcceptanceClause>} clauses - each must name ≥ 1 test id.
 * @param {Set<string> | ReadonlyArray<string>} passed - test ids that passed in THIS gate run.
 * @param {Set<string> | ReadonlyArray<string>} [failed] - test ids that FAILED in this gate run.
 *   `parseTapPassed` flattens every file's/describe-block's TAP subtests into one global name set,
 *   so two unrelated tests can share a name; when one passes and the other fails, that name lands
 *   in BOTH sets. `failed` is checked independently of `passed` so that ambiguity never accepts a
 *   clause on the strength of some OTHER test with the same name having passed.
 * @returns {AcceptanceResult} refused (naming the offending clause and test) on the FIRST clause
 *   whose acceptance is not fully covered; `{ok: true}` only when every clause's every named test
 *   id is in `passed` and NOT in `failed`.
 */
export function checkAcceptance(clauses, passed, failed = new Set()) {
  if (!Array.isArray(clauses) || clauses.length === 0) {
    return { ok: false, reason: 'no acceptance clauses given', clause: '', test: '' };
  }
  const passedSet = passed instanceof Set ? passed : new Set(passed);
  const failedSet = failed instanceof Set ? failed : new Set(failed);
  for (const entry of clauses) {
    const clause = entry?.clause;
    const tests = entry?.tests;
    if (typeof clause !== 'string' || clause.length === 0 || !Array.isArray(tests) || tests.length === 0) {
      return { ok: false, reason: 'clause names no test id', clause: String(clause ?? ''), test: '' };
    }
    for (const test of tests) {
      if (failedSet.has(test) || !passedSet.has(test)) {
        return {
          ok: false,
          reason: `clause "${clause}" names test "${test}", which did not exist or did not pass in this gate run`,
          clause,
          test,
        };
      }
    }
  }
  return { ok: true };
}
