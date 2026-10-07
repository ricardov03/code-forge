---
name: code-forge
description: Plan, harden and code a feature with levels and roles instead of model names — an L0 delegate builds the facts sheet, an L3 session authors the plan, coders run at the level System 1 picks, a fresh L2 session reviews every finished file, and the block gate re-measures everything. Use when the user says "/code-forge", "forge this feature", or "code-forge plan|harden|code|full".
---

# code-forge — the orchestrator's skill

You are the **orchestrator**: the session that invoked this skill. You dispatch, re-measure and record. You **never code, never review, never judge** — every model call that is not your own turn goes through the CLI as a fresh, isolated session. `forge` below is `scripts/forge` next to this file (a shim for the `code-forge` CLI). Every fact — model ids, efforts, thresholds, paths — lives in `.code-forge.yml` or in the CLI, never in this text; when a verb's usage line disagrees with this file, the verb is right.

## §0 Preflight (every invocation)

1. `forge validate` — exit 0, or stop and show the refusal (it names the key, never the value).
2. `forge doctor --quick` — config, links, key resolution. On the first run of a day run `forge doctor` in full and read its `engine` row and its `signer: same-user boundary only` row (`references/security.md`).
3. `forge run start` — prints `run <id> started · engine <e> · worker pid <n>`. The engine is `solo`, `harness` or `subprocess` (`engine: auto` picks; `subprocess` only by explicit config). No engine ⇒ the four-line stop text in `references/degraded.md` §1 is printed and you stop. Keep `<id>`: every later verb takes `--run <id>`.
4. When the engine is not `solo`, print the banner of `references/degraded.md` §2 once.

## §1 Invocation and state

`/code-forge plan|harden|code|full [brief file | plan file]`. No job given ⇒ infer it: a brief ⇒ `plan`; a plan whose header says `status: planned` ⇒ `harden`; `status: hardened` ⇒ `code`. `full` runs the three in order with one human checkpoint after `harden`.

State lives in the plan file's header — `facts → planned → hardened → coding → done` — and in the run record: `forge run status --run <id>` prints the active blocks, `forge ledger tail --slug <slug>` the last rows. Takeover after a lost session: `references/continuity.md`. A config change mid-run (a model swap, the review topology) is `forge run reload --run <id>` — never `block stop` + `run end` + `run start` (`references/continuity.md` §4).

## §2 Roles by level (never by model name)

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
4. Per block, in dispatch order, never more than `caps.coders` alive: `forge block open <id> --run <r> --level L<n> --owned <paths…> --acceptance <file> --brief <file> --lines <n> [--kind code|docs|contract]` prints the pointer `BRIEF <path> lines=<n> sha=<sha8> <<<EOM>>>`. Send the pointer per the engine's adapter — `solo` → `references/adapters/solo.md`, `harness` → `references/adapters/claude-code.md`, `subprocess` → `references/adapters/subprocess.md` — and wait for `ACK <sha8> lines=<n>`.
5. The coder's first message is its **facts diff**, then its forecast (cases and lines, `templates/block-record.md`), then code, file by file: after each file `forge review-file <path> --block <id> --run <r>`, then `forge review-file --wait <ticket>` before the next file. Rounds ≥ 2 re-check only the fix hunks (`references/review.md`).
6. `===BLOCK <id> COMPLETE===` or `===BLOCK <id> FAILED: <reason>===` ends the coder's turn. Its report is a claim sheet: re-measure it (`references/code.md` §5).
7. `forge block close <id> --run <r> --report <coder report>` is the gate: a report without a `reviewed <path> ticket <id>` line per changed file is FAILED; a signed `review.approved` row per changed file, gates green, every clause covered, forecast vs actual, no `rule_break`. Red ⇒ `references/decisions.md` (S1 `next`, escalation). Green ⇒ one commit for the block in the project's house style; `forge block rebase <id> --run <r>` first when HEAD moved.
8. `forge run end --run <r>` when every block is `done`; `forge report --slug <slug>` prints cost per block, escalations, review rounds and the spend per run (`budget.usd`, `references/ledger.md` §3.1).

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
