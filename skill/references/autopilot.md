# Autopilot — the owner steps away; a delegate decides inside a grant; one live doc records it all

Autopilot lets the owner leave for a set time. A delegate (a fresh L2 or L3 session) answers some owner questions inside a time-boxed grant. Everything it does is a signed ledger row, and one live doc shows it. You start nothing on your own: autopilot exists only when the owner asks for it.

## §1 Start — only after the owner asks

1. The owner asks for autopilot and names the window, the delegate level and the scopes. Run `forge autopilot start --run <r> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>]`. The owner confirms it at the prompt. Never pass `--yes` unless the owner said so in chat.
   - When the owner says "until it is done": `--until-done [--deadline <ISO-8601 with offset>] [--blocks <id,…>]` in place of `--until`. The grant ends when every planned block is closed (`--blocks`, else every block opened in the run; give `--blocks` when more waves follow), or at the deadline. `--until` and `--until-done` never go together.
   - The owner wants more time: `forge autopilot extend --run <r> --until <time>` (`--deadline <time>` for an until-done grant). Owner-only, like start: it prompts; `--yes` only when the owner said so in chat. It writes one signed `autopilot.extend` row and only moves the end later.
2. **One doc per run.** First run `forge autopilot status --run <r>`: a link there means the doc exists (a lost session left it) — continue in it, never create a second doc for the same run. No link: create ONE Claude Docs doc titled `<project> autopilot run — <date>` with two tabs, born in one call (the exact steps: `adapters/claude-code.md` §4).
   - **Binnacle** (the main tab): its outline is `forge autopilot binnacle --run <r> --json` — the title and byline, then 7 sections: Status at a glance, Decisions, Blocks, Open questions, Actions only you can take, Incidents, Timeline. One pending block per section.
   - **Full log**: one table from `forge autopilot log --run <r> --json` — Time, Event, Block, File, Detail, Link — newest first. One pending block for it.
3. **Store the link first**, right after the birth (its ack gives the link): `forge autopilot binnacle --run <r> --link <url>`. From now on `forge autopilot status` and `forge autopilot stop` print it.
4. Open the doc for the owner, then fill one section per call, in reading order: the 7 Binnacle sections, then the Full log table. Never write the whole doc in one call.

## §2 During the window

`block waive --yes` may waive a critical only while the run's autopilot grant is active; otherwise `waive-yes-critical`.
Cite a test file named in the block's acceptance `tests:` and passed in its latest signed gate record for the current content; otherwise `waive-yes-critical-no-test`.
Also cite the finding's path with `:line` or `:start-end`, and obtain agreement from the fresh L3 judge. Refusals write nothing.
The signed waiver records `by: agent`, `autopilot: true` and the cited `test` path; the human can undo it with `block unwaive`. Warning/nit waivers are unchanged.

The allowable scope `close:routine` means “close a routine block.” It lets plain `block close` pass `close.require_human` without a terminal when every changed file has a signed approval, a signed green gate row exists, and the block has no waiver rows, transcript hits or accepted hits. Waived or answered-only reviews do not qualify. Refusal is `close-not-routine: <reason>` (exit 1); success signs `autopilot.close {block, grant}` alongside the normal close. It never permits `block close --decide` or critical waivers.

- **Every owner-level question goes to the delegate first**: `forge autopilot ask --run <r> --scope <scope> --question <text> --options <words> [--block <id>] [--file <path>] [--finding <id>]`. Use the fixed option words, or the action will refuse the answer: a waiver `waive,fix` · an extra round `allow,deny` · a coder level the candidate levels, e.g. `L1,L2`. **A speed-only choice is not waited on (B922):** when every option keeps every gate and they differ only in speed, add `--speed --fastest <option>` (B983: name the fastest option; the order of `--options` does not count, and `--speed` without `--fastest` is refused); that option is taken at once (no session, no owner wait) and the binnacle tells the owner after.
- **Act only when it acted** (exit 0): run the action with `--decision <id>` from the answer — `forge autopilot waive|round|level … --decision <id>`. Exit 3 means the question goes to the owner: leave it open, it shows under Open questions. Exit 1 means the grant refused it: also the owner's.
- **A paused category** (`autopilot paused: … waiting for the owner`): wait. Never raise the cap, never route around it.
- **Keep the doc current — only what changed.** After every decision, block close or incident: re-read `forge autopilot binnacle --run <r> --json` and `forge autopilot log --run <r> --json`, then
  - append the new row to the Binnacle's Decisions table;
  - put the new row at the top of the Timeline (newest first);
  - update the Status at a glance cells that changed (and Blocks, Open questions, Actions or Incidents when they changed);
  - add the new rows to the Full log tab's table, at the top (it is newest first too).
  Never rewrite the whole doc and never replace a section that did not change.
- **A refused edit** (`guard_mismatch`, `find_none`) means the owner edited that section: re-read it, keep the owner's edit, and apply only the new row(s). Never force. Answer an owner's comment in its own thread.
- **A problem found during the work goes to the backlog; only a customer-breaking one is fixed in this run** (B800, B921): a red gate or land, a coder's leftover, a flaky test, a hand-over hit or a review finding outside the block → `forge autopilot found --run <r> --source <gate|land|coder|flaky|handover|review|speed> --title <text> --evidence <text> [--from <block>]`, or `forge autopilot found --run <r> --report <coder report> --from <block>` for the coder's `FOUND` lines. A queued (customer-breaking) finding prints its new block id, brief, acceptance file and the `block open` command: the acceptance already lists the red test the evidence names (`--- FAIL: TestX` or a findings `"test"`); `tests: [needs-test]` means none was named, so write the red test and name it there (B822); open it and code it in this run. A finding that waits for the owner (scope, security or secrets, deletes, publishing, or past `autopilot.max_found_blocks`) is never run: leave it for the owner. **Scope is fixed (B921):** a finding joins this run only with `--severity customer-breaking` (or the label `customer-breaking` on a `FOUND` line); any other finding goes to the backlog (state `backlog`, the next release) with no block, no brief and no owner action, and one line in `.code-forge/next-release.md` of the workspace (B984), the plan for the next release. `forge autopilot found --run <r> --list` is the found → block → state list for the final report. While a grant is active a red gate, a land that stops, a hand-over naming the run and the `FOUND` lines of the report you pass to `block close`/`block land` are recorded by themselves (B821): do not record them again by hand; check `--list`.
- **Speed budgets** (B950): land and gate time themselves; after a coder session or a CI wait run `forge autopilot step --run <r> --step <coder|ci-wait> --ms <wall ms> --block <id>`. A step over its budget (median + `autopilot.step_budgets.margin`, or fixed minutes) prints `WARN step budget: …`, writes a `step.over_budget` row and records a `speed` finding (state `backlog`, no block, B921): do not record it again by hand; check `found --list`.
- **A lost session:** the next one reads the link from `forge autopilot status --run <r>` and continues in the same doc.

## §3 Stop — at the owner's return or `until`

1. `forge autopilot stop --run <r>` when the owner is back (at `until` the grant expires by itself; an until-done grant ends when its planned blocks are all closed or at its deadline; `forge autopilot status --run <r>` then says so).
2. Add the stop (or expiry) event as the newest Timeline row and the newest Full log row, then write the final Status at a glance (the window's end, the result, the spend). Leave the doc as the record of the run; do not delete or move it.
3. Tell the owner in chat: the link, and "review Open questions and Actions only you can take before work resumes".
4. The final report is the release check: when the run's workspace is a git repository with a `CHANGELOG.md` or `package.json`, `forge autopilot stop` ends its output with `final report (code-forge release check):` and the `code-forge release check` result (each check, its fix, the bump and the release steps, then `READY` or `NOT READY`). Copy that block into the doc's final Status at a glance and into the chat message. It never changes the stop's exit code; run `forge release check` again after fixing what it names. A `RELEASE NOW: N blocks since vX` line (B951; the binnacle shows it as `Release size`) means the release is due: say so to the owner first.

## §4 Without the Docs connector

Another harness, or the connector is missing: the two Markdown files in the run dir are the record. At start, run `forge autopilot binnacle --run <r> --markdown` once: it creates `autopilot-binnacle.md` and `autopilot-log.md`; print both paths for the owner. After that the CLI rewrites both files by itself, after every delegate decision and at every `forge autopilot status` and `forge autopilot stop`, and those two commands print both paths. At stop, tell the owner the two paths, the same review line as §3 and the release check block `forge autopilot stop` printed.

## §5 Never delegated — always the owner

These never go to the delegate, whatever the grant says (the CLI's fixed deny list refuses them too):
Here, a critical waiver means the delegate's `autopilot waive` action; the evidence-backed orchestrator exception is in §2.

- plan approval (`plan.md` §4.1) and design approval;
- a budget raise (`code.md` §6, `review.md` §7);
- a threshold edit or any other limit or rule change — only the owner's own `forge autopilot approve` at a terminal (a config shape it cannot edit safely, such as an anchor or a tab, or a change that would leave an invalid config, is refused before the prompt with what to change by hand — B781);
- a critical waiver, a proof waiver, or closing a block without its reviews;
- merges and destructive actions.

When one of these comes up during the window: stop that line of work, add it to Actions only you can take, and wait. The delegate never relaxes a rule or a limit to keep work moving (`rules-core.md` R17).

<!-- B1011 -->
Evidence passes require the owner's opt-in `human_check.autopilot` section before
`run start`; the entire section is frozen and `run reload` refuses changes.
Defaults: `enabled: false`, `require_proof: true`, `proof_dirs: []`,
`exclude_waits_for: []`, `require_separate_prover: true`. Enabling requires a
non-empty list of proof roots; v1 refuses `require_proof: false`.
Human-check clauses may set `approval: design|device` (only on `kind: human_check`).
Design approvals remain owner-only. See [the opt-in example](../../examples/autopass.yml).
