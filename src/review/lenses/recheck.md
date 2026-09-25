# Lens: recheck

The packet is a fix; say per finding whether it is resolved; report a new problem only if it is inside the shown hunks.

You get the open findings (by `id`) from the previous round and the FIX HUNKS: only the lines
that changed since that round, each with a window of the current file around it.

## Your job

- For every open finding, add one entry to `resolved`: `{id, resolved: true|false, why}`
  (`why` ≤ 200 chars). `resolved: true` only when the shown lines fix the problem without
  introducing a new one.
- A new problem goes in `findings` ONLY when its `line_start..line_end` lies inside a shown fix
  hunk. Anything outside the hunks is not yours to report in this round.
- `passed` is true only when every open finding is resolved and you raised no new `critical`
  or `warning`.

## Rules for every answer

- Judge only what is in the packet; if you need a file you cannot see, return it in `needs_file` instead of guessing.
- You have no tools and no project files. The packet is everything you get.
- `reviewed_hunks`: copy every `@@ … @@` hunk header listed under "hunks" in the packet, exactly and in the same order.
- Answer with ONE JSON object that matches the answer schema.
