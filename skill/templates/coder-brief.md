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

## Proof

- Red→green: each new or changed test must FAIL on the old code first (red), then PASS on the new code (green). A red caused by an import, compile or crash error proves nothing; the red must be an assertion failure. `code-forge proof red-green` does this with the block's base code.
- No hand mutations. Do not break the code on purpose to test the tests. Red→green is the proof.
- One exact test per acceptance clause, plus one negative or boundary test where the clause can fail. Assertions say which element and how many, never only "it exists".
- Proof scales with risk: a light-tier file needs red→green per new test; a high-tier file must have it at the gate. Do not add tests, mutants, layers, options or abstractions that the acceptance does not ask for.
- A weak assertion is fixed by a stronger assertion, not by more tests or mutants.

## The file loop

After you finish each file in `owned_files` (tests written and green) run `forge review-file <path> --block <id> --run <r>`; before the next file run `forge review-file --wait <ticket> [--max 90s]` and act on the packet. A finding whose fix belongs in another owned file (a missing test, say) is fixed there and sent with `forge review-file <that file> --block <id> --run <r> --answers <ids> --of <source>`. A `worker_down` answer ⇒ print `===BLOCK <id> FAILED: worker down===` (never approval).

With `review.file_loop: overlap`, enqueue after each green file and continue coding. Before the completion sentinel, run `forge review-file --wait-block <id> --run <r> [--max 90s]`. It prints one JSON line `{block, files: [{file, ticket, status}], done}`. Inspect each finished ticket with `--wait <ticket>` and act on its findings; `done: true` means finished, not approved. A timeout has `done: false`, `reason: wait_timeout` and pending files (exit 0): wait again. `not_queued` names an owned changed file you must enqueue. Changed content with an earlier ticket is queued automatically; the worker marks an old pending ticket stale when it starts it. `worker_down` exits 3, always with `done: false`: print the FAILED sentinel. Without a `review.file_loop` setting, or with `wait`, use the per-file loop above. Fix rounds use the same selected loop before their sentinel.

With `review.file_loop: watch` (B104), commit each owned file after its tests are green and continue coding. Watch replaces per-file enqueueing. Before the completion sentinel, including fix rounds, run `forge review-file --wait-block <id> --run <r> [--max 90s]`, inspect finished tickets with `--wait <ticket>`, and fix findings. The worker watches the block worktree HEAD (otherwise repository HEAD), queues owned content from git objects with the same ticket ids as a manual request, and marks older pending tickets `refused`, `reason: superseded`. A running ticket finishes for its old hash, which cannot approve newer bytes; `--wait-block` joins the current ticket per file. `--fresh` refuses a superseded marker; enqueueing that content again removes only its superseded marker so it can run, while a real result stays cached. `queue/watch/<block>.json` stores the last seen commit; missing or unreadable state restarts at `base_sha` with idempotent tickets. A rewound tick queues nothing and drops the last seen commit so the next tick restarts at `base_sha`. A `watch.skipped {block, reason}` row is written once per reason until a healthy head clears the skip reasons: `unreadable_head` (head, tree or objects cannot be read), `not_descendant` (head does not descend from the block base), `rewound` (head no longer descends from the last seen commit), or `unowned_path` (unowned paths are ignored; owned paths still queue). A skip never fails the block. After a `watch.skipped` row, use `forge review-file <path> --block <id> --run <r>` when the file needs a manual review; use `forge review-file <fix file> --block <id> --run <r> --answers <ids> --of <source>` for a fix in another owned file that answers findings. Set watch before starting the worker. With any `review.file_loop` value other than `watch` (`wait`, the default, or `overlap`), the worker does no watch work and runs no git for it.

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

The final report needs no `reviewed <path> ticket <id>` lines: `block close` prints one for every file the review gate accepts (approved, waived or answered), with the queue's ticket. The close prints the reviewed lines, then the `reviewed_by_waiver` lines, then the `closed` line. With `--report`, a hand line that names another ticket prints `WARN report ticket mismatch <path>` on stderr when the block closes, and the close goes on. A file with a `review.waived` finding still gets a line; only a close whose every review the human waived, or a `review.external` close, prints none. The printed line has no `via`: `--json` and the block's done file carry the lines as `reviewed: [{file, ticket, via}]`, with `via: approved` for a file with a signed approval, `via: waiver` when at least one of its open findings was waived (also when the others were answered), and `via: answered` when every one was answered. Every changed file still needs a review through the selected file loop: a file the review did not accept keeps the block open. "All findings addressed" is not a report.

Or `===BLOCK <id> FAILED: <reason>===`.

## Fix-round brief (round ≥ 2, or a respawn after a climb)

The open findings by id, the fix history, the files they sit in. Fix only those hunks, then follow the selected file loop on **every file the round changed** (`wait`: run `forge review-file <path> --block <id> --run <r>` and `--wait`; `overlap`: enqueue each green file and continue; `watch`: commit each green file; both `overlap` and `watch`: run `forge review-file --wait-block <id> --run <r>` before the sentinel) — a fix the reviewer never saw is not a fix. A fix made in another owned file is sent with `--answers <ids> --of <source>` (The file loop). The round's final report follows the same rule as the first: it needs no ticket lines. Re-run the block's test command after the fixes and print a fresh `TESTS <command> exit=<n>` line just before the round's sentinel: a TESTS line from an earlier report does not count. In a fix round, you may use `code-forge gates run --run <r> --block <id> --changed` as the block's test command; the TESTS line names the command actually run (substitute the run and block ids). Any green `code-forge gates run --run <r> --block <id>` records the owned file hashes on `gate.done`, including runs without `--changed`; when `--slug` is omitted, these rows go to the run's project ledger. More than 200 owned files means no record, so the next `--changed` falls back with `no_baseline`; unreadable inputs also leave no record. `{test_files}` expands to changed tests and stem-paired tests of changed sources; `{changed_files}` expands to changed lint/format paths. Deleted files still count for pairing and the threshold, but both tokens receive only files that exist now. Both tokens must be whole argv items and cannot be the command or the only item; config validation refuses a gate whose command or only item is `{changed_files}`. Without `--changed`, `{changed_files}` is dropped. Gates without tokens run as before. Rows carry `scope: changed`, including fallback rows with a `reason`: `no_baseline` uses today's scope, `unpaired` uses today's test scope when a changed source has no test pair, `threshold` runs the full suite above `gates.full_suite_threshold_files` changed files, and `no_changes` uses today's scope when nothing is left to run. The orchestrator runs the full `code-forge gates run` before `block close` (D2).

## Context pack

<!-- the block's context pack, pasted as text: the block header (run, block, level, owned and test files, worktree), the project's build, test, lint and format commands, the built-in proof rule, the rules files and the lessons file. `forge context pack --run <r> --block <id>` writes it; paste its contents, never its run-root path (`references/code.md` §3). Without a `context` key, leave this section out. -->

## Project rules

<!-- the project's rules digest (≤ 60 lines) and the forbidden list verbatim (`references/security.md` §2); what a brief never carries is in `references/code.md` §3 -->
