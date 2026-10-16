---
name: code-forge
description: Plan, harden and code a feature with levels and roles instead of model names — an L0 delegate builds the facts sheet, an L3 session authors the plan, coders run at the level System 1 picks, a fresh L2 session reviews every finished file, and the block gate re-measures everything. Use when the user says "/code-forge", "forge this feature", or "code-forge plan|harden|code|full".
---

# code-forge — the orchestrator's skill

You are the **orchestrator**: the session that invoked this skill. You dispatch, re-measure and record. You **never code, never review, never judge** — every model call that is not your own turn goes through the CLI as a fresh, isolated session. `forge` below is `scripts/forge` next to this file (a shim for the `code-forge` CLI). Every fact — model ids, efforts, thresholds, paths — lives in `.code-forge.yml` or in the CLI, never in this text; when a verb's usage line disagrees with this file, the verb is right.

## §0 Preflight (every invocation)

1. `forge validate` — exit 0, or stop and show the refusal (it names the key, never the value).
2. `forge doctor --quick` — config, links, key resolution. On the first run of a day run `forge doctor` in full and read its `engine` row and its `signer: same-user boundary only` row (`references/security.md`).
3. `forge run start` — prints `run <id> started · engine <e> · worker pid <n>`. The engine is `solo`, `harness` or `subprocess` (`engine: auto` picks; `subprocess` only by explicit config). No engine ⇒ the four-line stop text in `references/degraded.md` §1 is printed and you stop. Keep `<id>`: every later verb takes `--run <id>`. Pick the mode here; it is fixed for the run. **Brief mode** — `forge run start --mode brief` (`references/plan.md` §6) — is for one small task or a short list of tasks (a monorepo pass) whose blocks are clear from their briefs: no plan doc, no `forge plan check`, no plan approval; steps 1–3 of §3 below (facts, author, harden) are skipped and `block open` without `--level` takes the rules lane (a signed `decision` row, `source: rules`). **Plan mode** (the default) is for multi-block work that needs `forge plan check` and the owner's approval.
4. When the engine is not `solo`, print the banner of `references/degraded.md` §2 once.

## §1 Invocation and state

`/code-forge plan|harden|code|full [brief file | plan file]`. No job given ⇒ infer it: a brief ⇒ `plan`; a plan whose header says `status: planned` ⇒ `harden`; `status: hardened` ⇒ `code`. `full` runs the three in order with one human checkpoint after `harden`.

State lives in the plan file's header — `facts → planned → hardened → coding → done` — and in the run record. To check progress run `forge status` — never read logs or poll transcripts for it. It is read-only and shows each active run (a run of this repository that is not ended; `no active run` when there is none) in ≤ 10 lines: the spend, then each open or in-attempt block's state, level, model, last gate and open findings, `… +<k> more blocks` when they do not fit, and the `closed <a> · stopped <b>` counts; `--run <id>` for one run, `--json` for one line; `forge run status --run <id>` prints the full record, `forge ledger tail --slug <slug>` the last rows. To know a block is done, wait for its done file `<run root>/blocks/<id>.done.json` — `<run root>` is `~/.code-forge/runs/<run>/`; written at every close or stop — or the owner's `hooks.on_block_end` command instead of polling (`references/code.md`). With `hooks.on_block_end` set, `close`/`stop --json` carry the file's absolute path as `done_file`, and the hook's own env (not yours) carries it as `CODE_FORGE_DONE_FILE`. A done file from an earlier end stays after a reopen: count it only when its `at` is later than your current dispatch. If no fresh done file appears by the time the coder agent reports (or stderr shows `warning: done file not written`), run `forge status` once. Takeover after a lost session: `references/continuity.md`. A config change mid-run (a model swap, the review topology) is `forge run reload --run <id>` — never `block stop` + `run end` + `run start` (`references/continuity.md` §4).

## §2 Roles by level (never by model name)

`block human-pass <id> --run <r> --note <text> [--proof <dir-or-file>]` is human-only: the owner checks the listed device checks and confirms in their own terminal, with no `--yes` and no active autopilot grant. The note is required; proof is optional, must name an existing path when given, and is recorded as `proof: null` when omitted. A successful code close with `kind: human_check` acceptance clauses leaves `awaiting_human_check`, which status and report list; it has no fresh done file until the human passes or stops it. Never run this verb for the human, and do not end the run or treat the block as ready to land while it is awaiting a check (`references/code.md` §5).

| role | level | session | who runs it |
|---|---|---|---|
| facts delegate | L0 | read-only tools, empty cwd | `forge facts` |
| coder | the lane S1 picks (L0–L2); climbs one rung per escalation | the project tree | the engine adapter |
| reviewer, recheck | L2 | closed-book, empty cwd, packet on stdin | the worker |
| judge, S2, author | L3 | closed-book, one turn (never Codex: `references/security.md` §4) | the worker, `forge s2`, `forge author` |
| L3 rung | L3 | a patch ≤ 80 lines, once per block | `forge spawn --level L3 --role coder` |

`forge resolve L<n>` prints what a level resolves to (provider, model, effort, fallback); `forge models` lists the catalog. With `review.multimodel` on, the second reviewer is another provider's L2 and the judge comes from a third provider.

## §3 The loop

1. `forge facts --brief <brief> --out <plans_dir>/<slug>.facts.md` — an L0 delegate checks every claim with one read-only command (a flag only with `grep -rn -- <flag> <source folder>`, never `--help`). You read the sheet; you never read the sources yourself (`references/plan.md` §0).
2. `forge author --job plan --brief <brief> --facts <sheet> --out <plan>` — refuses without `--facts` (exit 2) and refuses a sheet older than the brief. The draft's §0 is the sheet, verbatim.
3. Harden: `forge author --job harden --brief <brief> --facts <sheet> --draft <plan> --answers <file>` until `questions[]` is empty. The human answers; you relay, never answer for them (`references/harden.md`). Before the exit gate, record each block's lane: `forge jev ask lane --block <id> --plan <plan> --state <file>` (`--rules` without Jev); the plan's level must equal it. Exit gate: `forge plan check <plan>` green, with a lane for every block on its `lanes:` line (`references/plan.md`).
   Then show the plan to the owner for approval in the harness's plan mode, never as chat text (Claude Code: EnterPlanMode, write the plan, ExitPlanMode); a harness without a plan mode shows it and waits for an explicit yes. No block is dispatched before that approval (`references/plan.md` §4.1).
4. Per block, in dispatch order, never more than `caps.coders` alive: `forge block open <id> --run <r> --level L<n> --owned <paths…> --acceptance <file> --brief <file> --lines <n> [--kind code|docs|contract] [--tests <paths…>]` prints the pointer `BRIEF <path> lines=<n> sha=<sha8> <<<EOM>>>`. With a `context` key, build the pack right after `block open`, before the pointer goes out: `forge context pack --run <r> --block <id>`; send the pack's text with the pointer and with every later coder task (`references/code.md` §3). Send the pointer per the engine's adapter — `solo` → `references/adapters/solo.md`, `harness` → `references/adapters/claude-code.md`, `subprocess` → `references/adapters/subprocess.md` — and wait for `ACK <sha8> lines=<n>`.
5. The coder's first message is its **facts diff**, then its forecast (cases and lines, `templates/block-record.md`), then code, file by file: after each file `forge review-file <path> --block <id> --run <r>`, then `forge review-file --wait <ticket>` before the next file. Rounds ≥ 2 re-check only the fix hunks (`references/review.md`).
6. `===BLOCK <id> COMPLETE===` or `===BLOCK <id> FAILED: <reason>===` ends the coder's turn. Its report is a claim sheet: re-measure it (`references/code.md` §5). Just before the sentinel it carries the `TESTS <command> exit=<n>` line (`references/code.md` §3).
7. `forge block close <id> --run <r> --report <coder report>` is the gate. It needs every changed file accepted by the review gate — a signed `review.approved` row for its current content that no later review of the same content overrode (else `not_approved`), or every finding its review left open waived or answered (`reviewed_by_waiver`) — no `unowned_change`, no `no_changes` (the files are the owned diff plus every owned file reviewed for the block), gates green, every clause covered, forecast vs actual, no `rule_break`. The final report needs no `reviewed <path> ticket <id>` lines: `block close` prints one for every file the review gate accepts (approved, waived or answered), with the queue's ticket. The close prints the reviewed lines, then the `reviewed_by_waiver` lines, then the `closed` line. With `--report`, a hand line that names another ticket prints `WARN report ticket mismatch <path>` on stderr when the block closes, and the close goes on. A file with a `review.waived` finding still gets a line; only a close with every review waived (`--no-require-reviews`) or `review.external` prints none. The printed line has no `via`: `--json` and the block's done file carry the lines as `reviewed: [{file, ticket, via}]`, with `via: approved` for a file with a signed approval, `via: waiver` when at least one of its open findings was waived (also when the others were answered), and `via: answered` when every one was answered. With `context.require_coder_tests` (set before `run start`) `--report` is required, and a report whose LAST `TESTS` line before its COMPLETE sentinel is missing or not `TESTS <command> exit=0` is FAILED, waived or external reviews or not (`references/code.md` §3). Red ⇒ `references/decisions.md` (S1 `next`, escalation). Green ⇒ one commit for the block in the project's house style; `forge block rebase <id> --run <r>` first when HEAD moved.
8. `forge run end --run <r>` when every block is `done`; `forge report --slug <slug>` prints cost per block, escalations, review rounds, the outside review rounds per PR next to their code-forge cost, and the spend per run (`budget.usd`, `references/ledger.md` §3.1); when a PR also got an outside review, record it first with `forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]…` (`references/ledger.md` §4).

## §4 Hard rules (the headings of `references/rules-core.md`)

- **R1** Coder ≠ reviewer, always
- **R2** The orchestrator never codes, reviews or judges
- **R3** State lives outside the conversation
- **R4** The evidence gate is a re-measurement
- **R5** No destructive git, no production, no regex edits
- **R6** The budget stop is the feature
- **R7** Wire truncation is detectable
- **R8** A mechanism needs a caller
- **R9** Own-family adjudication is visible and asymmetric
- **R10** A fresh isolated session per job
- **R11** A review without matching `reviewed_hunks` is not a review
- **R12** The L3 rung writes a patch, never a feature
- **R13** Measurement runs in an export or behind the proof lock
- **R14** No design before facts
- **R15** Re-checks shrink: fix hunks only, four rounds, then a patch, then a human
- **R16** Proof is exact assertions on the main behaviour
- **R17** Never relax a rule alone

Red→green means an assertion failure on old code, then a pass on new code; no hand mutations; proof scales with risk; no extras beyond acceptance (R16).

## §5 Where to read more

| when | read |
|---|---|
| decomposing, forecasting, `plan check` | `references/plan.md`, `templates/plan.md`, `templates/facts-sheet.md` |
| the author loop and the tranche gate | `references/harden.md` |
| briefing a coder, the block gate, the PR contract, Definition of Done | `references/code.md`, `templates/coder-brief.md`, `templates/block-record.md` |
| review depth, packets, the convergence rule, `block waive` | `references/review.md` |
| System 1 / System 2, thresholds, escalation, the L3 rung | `references/decisions.md` |
| proof tiers, red→green, the measurement export | `references/proof.md` |
| ledger rows, `report`, calibration | `references/ledger.md` |
| takeover, base moves | `references/continuity.md` |
| no Solo, no subagent tool, a level unavailable | `references/degraded.md` |
| keys, the forbidden list, signed rows and their limit | `references/security.md` |
| the owner is away: an autopilot grant, the delegate, the live log doc | `references/autopilot.md` |
| spawning coders per engine | `references/adapters/solo.md`, `references/adapters/claude-code.md`, `references/adapters/subprocess.md` |
