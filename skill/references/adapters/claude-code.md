# Claude Code adapter — engine `harness` (the default without Solo)

The harness's `Agent` tool runs the coder as a subagent; everything else — the worker, reviewers, judges, S2, author, facts — still runs as isolated CLI sessions.

## §1 Spawn

1. `forge run start` once; `forge block open <id> --run <r> …` before every spawn (`code.md` §3). `forge resolve L<n>` prints the level's provider, model and effort; the `Agent` tool takes a family **alias** for its model, derived from the configured id's family prefix — effort cannot be passed to the tool, so the ledger records `effort_effective: null` and `doctor` prints it.
2. `Agent(subagent_type: general-purpose, model: <the alias `forge resolve L<n>` maps to>, prompt: <the pointer>)` — the prompt is the pointer `forge block open` printed (`BRIEF <path> lines=<n> sha=<sha8> <<<EOM>>>`) preceded by one line: *read the brief at the pointer; reply `ACK <sha8> lines=<n>` as the first line of your report; append a progress line to `.code-forge/runs/<run>/<block>.log` after every file.* No worktree (`harness.claude.worktree` stays false): parallelism rests on disjoint `owned_files`, and without locks `caps.coders` is 1.
3. The brief file carries the forbidden list verbatim; the harness's own permission rules are set from the same list by `forge init`.

## §2 Completion and evidence

The tool return is the completion signal; the report must begin with `ACK <sha8>` and end with `===BLOCK <id> COMPLETE===` (+ test summary + `git diff --stat`) or `===BLOCK <id> FAILED: <reason>===`. A report without the sentinel is a failed block, never a partial success.

**Between completion and close come the review rounds.** The subagent ran `forge review-file <path> --block <id> --run <r>` per file and `--wait`ed on each ticket inside its turn, so most rounds happen before the sentinel; when the tool returns, first drain every `pending` ticket with `forge review-file --wait <ticket>` yourself — a pending ticket is a review in flight, not an open finding, and waiting on it costs no round. Then only an open `fix_now` finding, or a file the coder never submitted, starts a fix round (each one counts toward the file's cap): dispatch a new `Agent` call at the level the ladder says (`review.md` §5 — the coder is gone with its tool return, so the fix brief carries the open findings and the fix history) and it runs `review-file` again on the fixed file. Rounds are bounded: four rounds per file, then the L3 rung once, then `stopped: review_cap` for the human. Only when every changed file has a signed `review.approved` row do you run the re-measurement of `code.md` §5 and `forge block close <id> --run <r> --report <file>` (the subagent's final report, saved to a file) — the transcript grep reads `.code-forge/runs/<run>/<block>.log`; pass `--transcript <file>` when the harness saved the subagent's transcript elsewhere. Closing with a file still unreviewed is a human decision: `forge block waive <id> <finding> --run <r> --file <path> --reason "<why>"` per finding, or `forge block close … --no-require-reviews`, which the human runs at their own terminal (it asks for a yes, has no `--yes`, and is refused under an active autopilot grant), both audited as their own rows.

## §2.1 Plan approval

The plan goes to the owner through plan mode (`plan.md` §4.1): `EnterPlanMode` → write the plan file → `ExitPlanMode`. The approval returned by `ExitPlanMode` is the go signal for the first `forge block open`.

## §3 What is lost and what is not

Lost: live observation of the coder (only the progress log and the final report), a resumable transcript (a lost subagent is re-dispatched with the fix history in a new brief — `continuity.md` §2). Not lost: session isolation, signed rows, the gate, the ladder, the facts rule, the convergence rule (`degraded.md` §2).

## §4 Autopilot — the live log doc (Claude Docs connector)

At `forge autopilot start` (`autopilot.md` §1) the run gets ONE doc through the Claude Docs connector. Follow the connector's own guide (`guide` → `topic.index`, `topic.tabs`). The steps:

0. **One doc per run:** `forge autopilot status --run <r>` first. A link there ⇒ the doc exists (a lost session left it): continue in it from step 5, never birth a second one.
1. **Birth, one `batch`:** a new doc named `<project> autopilot run — <date>`. The main tab **Binnacle** follows `forge autopilot binnacle --run <r> --json`: the title and byline, then 7 sections: Status at a glance, Decisions, Blocks, Open questions, Actions only you can take, Incidents, Timeline — one `pending` block per section. A second tab **Full log** is born in the same batch with one `pending` block for its table. The last member places both: Binnacle `order "a0"`, Full log `order "a1"`.
2. **Store the link at once,** from the birth ack: `forge autopilot binnacle --run <r> --link <url>`.
3. **Open it** with the Artifact tool's `open` action and that link (no `open` action: give the link in your next message, once).
4. **Fill one section per `update`**, in reading order: replace each Binnacle pending block with `## <heading>` and its table or list from the JSON; then the Full log tab's pending block with one pipe table from `forge autopilot log --run <r> --json`, newest first.
5. **During the window:** after each decision, block close or incident, one `update` per changed section — insert the new Decisions row, insert the new Timeline row at the top, replace only the Status cells that changed — and insert the new rows at the top of the Full log table. Never resend the whole doc. A refused edit (`guard_mismatch`, `find_none`) means the owner edited it: re-read that section, keep their edit, apply only the new row(s); never `force`.
6. **At stop:** after `forge autopilot stop --run <r>`, insert the stop event as the newest Timeline row and the newest Full log row, then replace the Status at a glance section with the final values. The doc stays as the run's record. Tell the owner in chat: the link, and "review Open questions and Actions only you can take before work resumes".

No Claude Docs connector: skip the doc. The Markdown files in the run dir are the record, and `status` and `stop` print their paths (`autopilot.md` §4).
