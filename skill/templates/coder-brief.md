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

After you finish each file in `owned_files` (tests written and green) run `forge review-file <path> --block <id> --run <r>`; before the next file run `forge review-file --wait <ticket> [--max 90s]` and act on the packet. A finding whose fix belongs in another owned file (a missing test, say) is fixed there and sent with `forge review-file <that file> --block <id> --run <r> --answers <ids> --of <source>`. A `worker_down` answer ⇒ print `===BLOCK <id> FAILED: worker down===` (never approval).

## Final report — FAILED unless it lists the ticket ids

Run the block's test command (`test_command`) and print its result on a line of its own, exactly:

`TESTS <command> exit=<n>`

`<command>` is the command you ran (not empty), `<n>` its exit code as a decimal 0–255 with no leading zeros (`exit=0`, never `exit=00`). Print it just before the sentinel; the last `TESTS` line after your previous sentinel and before this one is the one that counts, and a line inside prose does not count. When the run has `context.require_coder_tests`, `forge block close <id> --run <r> --report <file>` refuses a report without it or with an exit that is not 0 — the full list of refusals is in `references/code.md` §3 (the close check). For example:

```
TESTS npm test exit=0
===BLOCK B7 COMPLETE===
```

The `TESTS` line stays ABOVE the sentinel; only the test summary comes after it.

End with `===BLOCK <id> COMPLETE===`, the test summary and `git diff --stat`, then one line per changed file:

`reviewed <path> ticket <ticket-id>`

**FAILED unless:** the report lists the `review-file` ticket id of every changed file. A report with no ticket ids, or one that misses a changed file, counts as FAILED whatever its sentinel says: the orchestrator treats it as `===BLOCK <id> FAILED: no review-file ticket ids===` and `forge block close <id> --run <r> --report <file>` refuses it (`coder report FAILED: no review-file ticket id for <files>`). "All findings addressed" is not a report.

Or `===BLOCK <id> FAILED: <reason>===`.

## Fix-round brief (round ≥ 2, or a respawn after a climb)

The open findings by id, the fix history, the files they sit in. Fix only those hunks, then run `forge review-file <path> --block <id> --run <r>` and `--wait` on **every file the round changed** — a fix the reviewer never saw is not a fix. A fix made in another owned file is sent with `--answers <ids> --of <source>` (The file loop). The final report follows the same rule: one `reviewed <path> ticket <ticket-id>` line per file changed in the round, or the round counts as FAILED. Re-run the block's test command after the fixes and print a fresh `TESTS <command> exit=<n>` line just before the round's sentinel: a TESTS line from an earlier report does not count.

## Context pack

<!-- the block's context pack, pasted as text: the block header (run, block, level, owned and test files, worktree), the project's build, test, lint and format commands, the rules files and the lessons file. `forge context pack --run <r> --block <id>` writes it; paste its contents, never its run-root path (`references/code.md` §3). Without a `context` key, leave this section out. -->

## Project rules

<!-- the project's rules digest (≤ 60 lines) and the forbidden list verbatim (`references/security.md` §2); what a brief never carries is in `references/code.md` §3 -->
