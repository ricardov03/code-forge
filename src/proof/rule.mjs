/** The built-in proof rule carried by every coder context pack. */
export const PROOF_RULE = [
  "## proof rule (code-forge)",
  "- Red→green: each new or changed test must FAIL on the old code first (red), then PASS on the new code (green). A red caused by an import, compile or crash error proves nothing; the red must be an assertion failure. `code-forge proof red-green` does this with the block's base code.",
  "- No hand mutations. Do not break the code on purpose to test the tests. Red→green is the proof.",
  "- One exact test per acceptance clause, plus one negative or boundary test where the clause can fail. Assertions say which element and how many, never only \"it exists\".",
  "- Proof scales with risk: a light-tier file needs red→green per new test; a high-tier file must have it at the gate. Do not add tests, mutants, layers, options or abstractions that the acceptance does not ask for.",
  "- A weak assertion is fixed by a stronger assertion, not by more tests or mutants.",
  "- Facts, not guesses: every cause, pass or fail you report names its proof (the error text, the log path, or the command and its exit code). No proof means \"not known yet\": get the proof now; never name load, flakiness or old tests as a cause without it. No work stops because of a guess.",
].join('\n');
