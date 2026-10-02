# Block `<id>` — `<title>` (coder brief)

| field | value |
|---|---|
| level | `L<n>` (the lane recorded by `forge jev ask lane --block <id>`) |
| owned_files | `<paths…>` — edit ONLY these |
| acceptance | `<clause (test ids)>` · … |
| test_command | `<quoted glob or explicit file list>` |

## Reply contract

1. `ACK <sha8> lines=<n>` before any tool call.
2. Your facts diff: every contradiction between this brief and the code, in one message.
3. Your forecast: cases and lines.

## The file loop

After you finish each file in `owned_files` (tests written and green) run `forge review-file <path> --block <id> --run <r>`; before the next file run `forge review-file --wait <ticket> [--max 90s]` and act on the packet. A `worker_down` answer ⇒ print `===BLOCK <id> FAILED: worker down===` (never approval).

## Final report — FAILED unless it lists the ticket ids

End with `===BLOCK <id> COMPLETE===`, the test summary and `git diff --stat`, then one line per changed file:

`reviewed <path> ticket <ticket-id>`

**FAILED unless:** the report lists the `review-file` ticket id of every changed file. A report with no ticket ids, or one that misses a changed file, counts as FAILED whatever its sentinel says: the orchestrator treats it as `===BLOCK <id> FAILED: no review-file ticket ids===` and `forge block close <id> --run <r> --report <file>` refuses it (`coder report FAILED: no review-file ticket id for <files>`). "All findings addressed" is not a report.

Or `===BLOCK <id> FAILED: <reason>===`.

## Fix-round brief (round ≥ 2, or a respawn after a climb)

The open findings by id, the fix history, the files they sit in. Fix only those hunks, then run `forge review-file <path> --block <id> --run <r>` and `--wait` on **every file the round changed** — a fix the reviewer never saw is not a fix. The final report follows the same rule: one `reviewed <path> ticket <ticket-id>` line per file changed in the round, or the round counts as FAILED.

## Project rules

<!-- the project's rules digest (≤ 60 lines) and the forbidden list verbatim (`references/security.md` §2); what a brief never carries is in `references/code.md` §3 -->
