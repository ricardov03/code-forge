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

Proof is behaviour, not text: never read a source file in a test to assert on its text. Call the code and check what it does; a test may read a fixture. A test file that reads a source file and asserts on its text gets a `## test-quality` section in its review packet. The section is a hint, not a finding: the reviewer checks those lines and, when the test really reads source text to assert on it, raises a `warning` (unless the file read is a fixture).

## Final report — the close prints the ticket ids

Run the block's test command (`test_command`) and print its result on a line of its own, exactly:

`TESTS <command> exit=<n>`

`<command>` is the command you ran (not empty), `<n>` its exit code as a decimal 0–255 with no leading zeros (`exit=0`, never `exit=00`). Print it just before the sentinel; the last `TESTS` line after your previous sentinel and before this one is the one that counts, and a line inside prose does not count. When the run has `context.require_coder_tests`, `forge block close <id> --run <r> --report <file>` refuses a report without it or with an exit that is not 0 — the full list of refusals is in `references/code.md` §3 (the close check). For example:

```
TESTS npm test exit=0
===BLOCK B7 COMPLETE===
```

The `TESTS` line stays ABOVE the sentinel; only the test summary comes after it.

End with `===BLOCK <id> COMPLETE===`, the test summary and `git diff --stat`.

The final report needs no `reviewed <path> ticket <id>` lines: `block close` prints one for every file the review gate accepts (approved, waived or answered), with the queue's ticket. The close prints the reviewed lines, then the `reviewed_by_waiver` lines, then the `closed` line. With `--report`, a hand line that names another ticket prints `WARN report ticket mismatch <path>` on stderr when the block closes, and the close goes on. A file with a `review.waived` finding still gets a line; only a close whose every review the human waived, or a `review.external` close, prints none. The printed line has no `via`: `--json` and the block's done file carry the lines as `reviewed: [{file, ticket, via}]`, with `via: approved` for a file with a signed approval, `via: waiver` when at least one of its open findings was waived (also when the others were answered), and `via: answered` when every one was answered. Every changed file still needs its `review-file` run: a file the review did not accept keeps the block open. "All findings addressed" is not a report.

Or `===BLOCK <id> FAILED: <reason>===`.

## Fix-round brief (round ≥ 2, or a respawn after a climb)

The open findings by id, the fix history, the files they sit in. Fix only those hunks, then run `forge review-file <path> --block <id> --run <r>` and `--wait` on **every file the round changed** — a fix the reviewer never saw is not a fix. A fix made in another owned file is sent with `--answers <ids> --of <source>` (The file loop). The round's final report follows the same rule as the first: it needs no ticket lines. Re-run the block's test command after the fixes and print a fresh `TESTS <command> exit=<n>` line just before the round's sentinel: a TESTS line from an earlier report does not count. In a fix round, you may use `code-forge gates run --run <r> --block <id> --changed` as the block's test command; the TESTS line names the command actually run (substitute the run and block ids). Any green `code-forge gates run --run <r> --block <id>` records the owned file hashes on `gate.done`, including runs without `--changed`; when `--slug` is omitted, these rows go to the run's project ledger. More than 200 owned files means no record, so the next `--changed` falls back with `no_baseline`; unreadable inputs also leave no record. `{test_files}` expands to changed tests and stem-paired tests of changed sources; `{changed_files}` expands to changed lint/format paths. Deleted files still count for pairing and the threshold, but both tokens receive only files that exist now. Both tokens must be whole argv items and cannot be the command or the only item; config validation refuses a gate whose command or only item is `{changed_files}`. Without `--changed`, `{changed_files}` is dropped. Gates without tokens run as before. Rows carry `scope: changed`, including fallback rows with a `reason`: `no_baseline` uses today's scope, `unpaired` uses today's test scope when a changed source has no test pair, `threshold` runs the full suite above `gates.full_suite_threshold_files` changed files, and `no_changes` uses today's scope when nothing is left to run. The orchestrator runs the full `code-forge gates run` before `block close` (D2).

## Context pack

<!-- the block's context pack, pasted as text: the block header (run, block, level, owned and test files, worktree), the project's build, test, lint and format commands, the rules files and the lessons file. `forge context pack --run <r> --block <id>` writes it; paste its contents, never its run-root path (`references/code.md` §3). Without a `context` key, leave this section out. -->

## Project rules

<!-- the project's rules digest (≤ 60 lines) and the forbidden list verbatim (`references/security.md` §2); what a brief never carries is in `references/code.md` §3 -->
