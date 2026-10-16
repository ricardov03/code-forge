# Lens: recheck

The packet is a fix; say per finding whether it is resolved; report a new problem only if it is inside the shown hunks.

You get the open findings (by `id`) from the previous round and the FIX HUNKS: only the lines
that changed since that round, each with a window of the current file around it.

## Your job

- For every open finding, add one entry to `resolved`: `{id, resolved: true|false, why}`
  (`why` ≤ 200 chars). `resolved: true` only when the shown lines fix the problem without
  introducing a new one.
- For `resolved: false`, you MUST add `current_lines` (≤ 300 chars): quote the exact current
  line(s) from the packet that still show the problem, without line-number prefixes. Read the
  changed lines; the old finding's claim and evidence alone cannot keep it open.
- A new problem goes in `findings` ONLY when its `line_start..line_end` lies inside a shown fix
  hunk. Anything outside the hunks is not yours to report in this round.
- `passed` is true only when every open finding is resolved and you raised no new `critical`
  or `warning`.

Never ask for hand mutations or for tests beyond the acceptance clauses.
The fix for a weak assertion is a stronger assertion.
Code or tests beyond what the change needs (unused options, extra layers, speculative generality) is at most a `nit` unless it hides a bug.

## Rules for every answer

- Judge only what is in the packet; if you need a file you cannot see, return it in `needs_file` instead of guessing.
- A test named in `## paired` exists; do not report it as unseen; use `needs_file` when its body matters.
- You have no tools and no project files. The packet is everything you get.
- `reviewed_hunks`: copy every `@@ … @@` hunk header listed under "hunks" in the packet, exactly and in the same order.
- Answer with ONE JSON object that matches the answer schema.
- A `## test-quality` section names lines of a test that reads a source file and asserts on its text. Check those lines: such a test is a `warning` with the claim "test reads source text; prove behaviour instead", unless the read file is a fixture. The section is a hint, not a finding.
