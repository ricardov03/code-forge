# Code — brief, dispatch, re-measure, gate

## §1 The coder's first deliverable is a facts diff

Before any forecast or code, the coder reads the code it will call and reports **every contradiction between the plan and the code** in one message — a signature, a flag, a key, a version. One stop for facts, never one per discovery. Contract corrections (a key, a rule, a signature) are the architect's alone and are recorded, not asked; a ruling reaches the human only when it changes user-visible behaviour, money semantics or an approved decision. A coder that stops three times for three true facts did nothing wrong; the protocol did.

## §2 The forecast and its floor

The coder then forecasts **cases** (tests) and **lines**. Compute the floor yourself before reading its number: test lines ≥ cases × the rate measured on the last two gated blocks (the lower of the two) and docblocks ≥ 10 % of source; reject a forecast below the floor for re-pricing. Record both numbers in the block record (`templates/block-record.md`); `forge block open … --lines <n>` stores the line forecast the gate compares against.

## §3 Dispatch — what the brief contains

`forge block open <id> --run <r> --level L<n> --owned <paths…> --acceptance <file> --brief <file> [--attempt <n>] [--base <sha>] [--kind code|docs|contract] --lines <n>` records the base sha, the owned files (overlap with another active block ⇒ refused), the block kind (declared, else detected — `decisions.md` §5), the level (a docs/contract block below `levels.coder_floor_docs`, default L1, is raised to it and the CLI prints the floor), the clauses with their test ids and the forecast, writes the ledger `dispatch` row and prints the pointer `BRIEF <path> lines=<n> sha=<sha8> <<<EOM>>>` (≤ 200 B). Call it before every spawn, in every engine. A docs block that owns a glob (`docs/**`) must declare `--kind docs`: a glob is judged by its literal ending, so undeclared it counts as `code`. The brief file carries, in this order:

1. `id`, `title`, the level, `owned_files` ("edit ONLY these"), the acceptance clauses with test ids, `test_command`.
2. The reply contract: `ACK <sha8> lines=<n>` before any tool call; the facts diff; the forecast.
3. The file loop: *after you finish each file in `owned_files` (tests written and green) run `forge review-file <path> --block <id> --run <r>`; before the next file run `forge review-file --wait <ticket> [--max 90s]` and act on the packet.* A `worker_down` answer ⇒ print `===BLOCK <id> FAILED: worker down===` (never approval).
4. The project's own rules digest (`CLAUDE.md`/`AGENTS.md`, ≤ 60 lines), the forbidden list verbatim (`security.md` §2), and the sentinel contract: `===BLOCK <id> COMPLETE===` followed by the test summary, `git diff --stat` and one `reviewed <path> ticket <ticket-id>` line per changed file, or `===BLOCK <id> FAILED: <reason>===`.
5. **FAILED unless** (`templates/coder-brief.md`): a final report — first attempt or fix round — that does not list the `review-file` ticket id of every changed file counts as FAILED whatever its sentinel says. Pass the report to the gate with `forge block close <id> --run <r> --report <file>`; it refuses `coder report FAILED: no review-file ticket id for <files>`.

The brief never mentions `block waive`, keys, or `~/.code-forge/runs/`. The coder process gets no Jev key (the System 1 model's key, `keys.jev`) and no signer key.

## §4 The file loop, from your side

Each `review-file` ticket is served by the worker `forge run start` launched: tools first (the project's gates on that file — red ⇒ `tools_red`, no model review), then S1 `risk`, then the planned depth (`review.md` §2). You watch the ledger, not the coder's output: `forge ledger tail --slug <slug>` shows `review.plan`, `review.round`, `review.done`. Idle is a wake, never proof; the sentinel is the only completion signal (`adapters/*.md`).

## §5 The block gate is a re-measurement — `forge block close <id> --run <r>`

The coder's report is a claim sheet. The gate reproduces each number in the measurement export (`proof.md` §3), and so do you when you read the report:

| claim | re-measured by |
|---|---|
| only owned paths touched | file set = tracked changes ∪ untracked, minus other active blocks' owned sets ⊆ `owned_files`; anything else is an orphan that stops every gate until `forge block claim <id> <path> --run <r>` or the human deletes it |
| every file reviewed | the report names a `review-file` ticket id per changed file (`--report`, which the orchestrator always passes) and a **signed** `review.approved` row for the current content hash of every changed or new file, or an architect-ruled disposition to a named successor block |
| tests green | every gate command run once in the export, each exit status read |
| clauses covered | every clause names ≥ 1 test id; each test exists and passed in this run; S1 `scope` on the hunks outside the named tests says `false` |
| test count | the filtered run's delta equals the new tests' own count |
| line and case forecast | `git diff --stat` net lines against `--lines`: > 1.25 × ⇒ stop (`===BLOCK <id> FAILED: forecast exceeded, split plan <id>a/<id>b===`); cases > 1.5 × forecast ⇒ a `forecast.cases_exceeded` WARN row, never a stop |
| no rule break | the transcript grep for forbidden verbs and paths (`--transcript <file>`, else the run record's log); a missing transcript ⇒ a signed `gate.transcript_missing` row and a WARN, never silence |
| high tier proven | every changed high-tier file (`proof.md` §1) has a signed, proven red→green row with `red_kind: assertion` for this block whose `covers` names it (light tier: optional), else `unproven <file>`; only the human's `block waive <id> proof --file <path>` clears it |
| rows genuine | the MAC of every gate-relevant row verifies; the live worker matches the pinned pid |
| nothing left open | no unruled `late_findings`; no file at `review_cap` without a signed `review.waived` row |

`--no-require-reviews` is the human's way out, written as its own audit row; the orchestrator runs it only after the human said so in chat. A finding counts only under the gate's own configuration; a tautology (an expected value computed by the code under test) is a finding. Reproduce disclosures: a coder that reports its own rule break did the hard part.

## §6 Budget — the stop is the feature (R6)

`budget.block_lines` and `budget.block_cases` bind the plan; the gate's 1.25 × stop binds the block. A coder that overruns and stops before expanding did the right thing: never compress or defer coverage to fit. One raise per run, by the human, logged; a second overrun in the same run re-decomposes at the architect level. **Autopilot:** a budget raise is never delegated, and neither is `--no-require-reviews` or a merge (`autopilot.md` §5). A post-acceptance ruling that adds required lines re-baselines the cap at ruling time, priced from the assertions it orders; a STOP defined mid-turn binds from the coder's next reported boundary. A coder never trims to fit; landing exactly on a cap is a tell that is checked. Review tokens count toward `budget.block_tokens_soft` / `budget.block_tokens_hard` (`review.md` §7).

## §7 The PR contract — a draft PR only after independent review

With `autonomy.coder_may_open_draft_pr` and `autonomy.coder_may_push` on, a coder may commit, push and open a **draft** PR on its own when all five hold, each pasted as evidence in the PR body: (1) facts diff delivered and no STOP tripped; (2) the block filter and regression anchors green in one run; formatter, static analysis, safe-edit and invariants clean; (3) red→green rows for every new test (`proof.md` §2); (4) every changed file carries a signed `review.approved` row — a stub, a timeout or `review.unavailable` forbids the push; (5) the commit is path-scoped to owned files, in house style, with trailers per `commit.trailers`, and the PR body carries the acceptance verbatim plus the claim sheet. A coder may not mark the PR ready, merge, dismiss a finding, raise a cap or push after a failed review. A GO carries a checksum (plan file, revision, line count) that the coder echoes back; unsent text in a coder's input box is never an instruction. Re-measurement then runs on the pushed sha; the orchestrator marks ready; the human merges — at most `wip.ready_prs` at a time.

**A finding dispositioned out of the block:** only the architect may rule it, and only when severity is warning or lower, every fix site lies outside the owned paths, no shipped UI or API reaches the failure path after merge, and a **named successor block** carries the failure path as its opening clause. The verdict stays recorded; a disposition without a successor clause is a dismissal, and dismissals do not exist.

## §8 Definition of Done (per block)

1. `owned_files` untouched outside the set (`git status` read, not quoted).
2. The block's test command green, case count read from the runner and written to the block record.
3. Type check and lint green under the project's gates.
4. Every changed or new file reviewed by a fresh L2 session and converged under the convergence rule (≤ `review.max_rounds_per_file`, or a human decision on record).
5. Red→green rows for every new test; proof rows per tier (`proof.md`).
6. Forecast vs actual (cases and lines) recorded; misses attributed (architect-added scope vs coder under-forecast) — a finished green block is accepted and recorded either way.
7. One commit for the block; the draft PR (when the project uses PRs) carries the evidence.
8. Every artifact that proves something is committed, not left in a scratch dir; no claim shipped that the evidence does not carry.
