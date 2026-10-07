# Decisions — tools first, System 1 in half a second, System 2 when unsure, a deterministic ladder

Deterministic tools answer facts (gates, diffs, counts). **System 1 (S1)** answers typed questions from a small JSON state built from facts. **System 2 (S2)** is a fresh L3 session that answers only when S1 is unsure. Same input ⇒ same answer; every answer and its outcome go to the ledger. 4–7 S1 calls per block is a target that is reported, never gated. A fact is never a model's opinion.

## §1 The S1 questions (`forge jev ask <question-id> --state <file>`)

| id | type | fires | answers |
|---|---|---|---|
| `lane` | choice | plan, per block (`--block <id>`: `plan check` reads it); code, on re-decomposition | `L0` trivial · `L1` a plain feature following an existing pattern · `L2` money, dates, tenancy, migrations, concurrency, unknown failure · `split` too large or two concerns. **L3 is not a lane** |
| `risk` | score 0–3 | plan, per block; code, per finished file | `0` cosmetic · `1` wrong behaviour, easy to notice and revert · `2` silent wrong data for some users · `3` silent wrong money or a cross-tenant leak |
| `security_sensitive` | yes/no | plan, per block; code, per file unless path floors already forced `high` | touches authentication, authorization, tenancy, secrets, money movement or webhook verification |
| `next` | choice | code, after each attempt's tool gate | `complete` all green, tests added or `no_new_tests_reason` given · `retry` a contained failure the same coder fixes in one more attempt · `escalate` repeated or spreading failures, or a risky area · `stop` the evidence contradicts the brief — the human decides |
| `scope` | yes/no | block gate, once, on the hunks outside the named tests | a change unrelated to the acceptance (clause coverage itself is deterministic) |
| `defect` | yes/no | review, per finding that has not passed a judge | a real defect that must be fixed before merge, not a style preference |
| `resolved` | yes/no | fix loop, per open finding after a fix (state = the fix hunk) | the fix hunk resolves the finding without a new problem in the shown lines |

The state never contains a secret or a whole file; `system1.disable` turns a question off for projects that will not send diff hunks (`defect`, `resolved` then go to S2). Only the worker or the orchestrator's own CLI call sends the question — never a coder process.

## §2 Thresholds — when S1 acts and when S2 checks

Per answer the CLI computes `confidence` and `margin`: for a choice or score `confidence = p(top1)`, `margin = p(top1) − p(top2)`; for a yes/no question `answer = p ≥ 0.5`, `confidence = max(p, 1 − p)`, `margin = |2p − 1|` — a confident **no** at `p = 0.03` is `confidence 0.97` and S1 decides. Bands per question (`thresholds.default` or `thresholds.<question>`): `confidence ≥ act` and `margin ≥ close_margin` ⇒ S1 acts (`s1.decided`) · `check ≤ confidence < act` or a small margin ⇒ **S2 checks** (sees state, S1's answer and probabilities; returns `confirm|overrule`) · `confidence < check` ⇒ **S2 decides** from the state alone. Findings keep their own bands (`thresholds.findings.fix`, `.nit`, `.resolved`).

## §3 System 2 — `forge s2 --packet <file.json> [--run <id>] [--block <id>]`

A fresh L3 closed-book session, empty cwd, packet on stdin, one turn, the compiled schema for the provider. Output `{decision, confidence, reason, overrule, ask_human, human_question}`. More than 6 non-fallback S2 calls in one block ⇒ the block is flagged `s2-heavy`: re-check its decomposition. `ask_human` routes to the human with `human_question` verbatim.

## §4 When S1 is unavailable (`system1.fallback`)

No key ⇒ a banner at run start; 401 ⇒ stop; 422 ⇒ stop with the request id; 429/529/network ⇒ backoff, then per-call fallback with one banner. `rules` (default): deterministic rules answer `lane`, `risk`, `security_sensitive` and an unambiguous `next` — path floors (`proof.tiers.high.paths` ⇒ risk 3, security yes), diff size (> 200 added lines or > 4 files ⇒ ≥ L1; migrations, policies, middleware ⇒ L2), keywords (money, policy, auth, webhook, migration); a rule that fires is final, no S2 check; the questions no rule covers go to S2 (`source: s2-fallback`, exempt from the `s2-heavy` counter). `s2`: every question to S2. `stop`: refuse to run without S1. The block gate refuses a block whose S1 calls fell back unexpectedly unless the human accepts it in chat.

## §5 Escalation precedence — deterministic, first rule wins, `trigger` recorded

1. **Attempts:** attempts at the current level equal `escalation.retries_per_level` and not green ⇒ +1 level, whatever S1 said (`retries`).
2. **Review rounds:** `escalation.review_rounds_per_level` exhausted with an open `fix_now` ⇒ +1 level (`review_rounds`).
3. **Review stall:** a re-check round whose open `fix_now` set did not strictly shrink ⇒ +1 level at once (`review_stall`, `review.md` §5).
3a. **Heavy rounds:** below L2, `escalation.after_rounds_with_warnings` (default 1) rounds at the current level whose open `fix_now` set holds ≥ `escalation.warning_threshold` (default 2) warnings or any `critical` ⇒ +1 level at once (`review_warnings`). A cheap coder that fails a round with several warnings climbs; it never spends three rounds at the same level. At L2 it does not fire — the L3 rung keeps its own triggers (rules 2, 3, 6). `after_rounds_with_warnings: 0` turns it off.
4. **Security floor, at dispatch:** `security_sensitive` ⇒ first-attempt level `min(max(lane, L1) + 1, L2)` (`security`). Then the **docs floor**: a `docs` or `contract` block (below) is never dispatched under `levels.coder_floor_docs` (default L1) — `forge block open` raises the level and the `dispatch` row carries `requested_level` and `trigger: docs_floor`. L0 never codes docs or contracts.
5. **S2 ruling:** S1 `next = escalate` is never acted on directly — it goes to S2 as a check (`s2_ruling`); S1 `next = stop` ⇒ the human.
6. **Review cap:** `review.max_rounds_per_file` reached on a file with an open `fix_now` ⇒ the L3 rung for that file, once per block, always as a patch whatever `escalation.l3_mode` says (`review_cap`); checked last, only when no rule above returned an action this attempt.

**Block kind.** `forge block open --kind code|docs|contract` declares it; without the flag it is detected from the owned paths: every path ends in `.md .mdx .markdown .rst .adoc` ⇒ `docs` (`.txt` is not docs: `requirements.txt`); every path is docs or a contract (`*.schema.json|yaml|yml`, `.proto`, `.graphql`, `.gql`, `.avsc`, `*.openapi.json|yaml|yml`, or a `.json/.yaml/.yml` under a `schema/`, `schemas/`, `contract/` or `contracts/` directory) and one is a contract ⇒ `contract`; anything else ⇒ `code`. A glob is judged by its literal ending, so a block owning `docs/**` declares its kind. The kind is recorded in the run record and drives the docs floor (rule 4) and the review topology (`review.md` §2).

**The ceiling constraint (not a ranked rule).** *Never skip a level* constrains every rule above: a "+1 level" is always the next level, and **the highest running level is L2**. When rule 1, 2 or 3 fires at L2 there is no coding level above, and the **L3 rung** takes over (§6): a patch session, never a coder at L3 — the block continues at L2 afterwards. `escalation.stop_at` = L3 (fixed) names that rung, not a level a coder runs at; `l3_mode: code` is the only way a coder runs at L3, and it is not the default. The rung is one per block from either source (a trigger at L2 or the review cap, rule 6): a second hit from either always `stop`s (`review_cap` for the cap; the ceiling reason for the trigger), so `stop_at` means something in every mode. A fresh coder spawned by a level climb inherits the file's round count: the cap counts rounds per file, not per coder.

**Own-family adjudication (R9).** When the orchestrator and the coder share a model family, every ruling the orchestrator makes about that coder's work is a judgement inside the family: it is written as a ledger row named as such, and it is asymmetric — the orchestrator may **accept** a finding alone; it may **not dismiss, defer or waive** one. Dismissals, cap raises and scope changes go to the architect (an L3 session) or the human.

## §6 The L3 rung (`escalation.l3_mode: patch`, R12)

A fresh L3 session receives the failure evidence — the last attempt's diff, the red gate output, the open findings, the brief — and returns `{kind: patch|redecompose|stop, patch, reason}`. `patch` (a unified diff ≤ 80 lines, owned files only): the CLI applies it after `git apply --check`, a fresh L2 `full` review of the patch runs as a `patch_check` round outside the per-file cap, and the block **continues at L2** with the attempt counter reset and `l3_patch: true`; a second L3 rung on the same block ⇒ `stop`. `redecompose` ⇒ `stopped: redecompose`, the human decides. `stop` ⇒ stop and ask. `l3_mode: code` lets L3 code the block; it is not the default.

## §7 What reaches the human

`next = stop`, `review.over_budget`, an orphan file or `unowned_change`, a `not_approved` critical finding, `--no-require-reviews`, `worker_down`, `review_cap`, `redecompose`, `ask_human`, a security question, consensus unavailable, and one budget raise per run. Everything else is decided by S1, S2 or the rules above and recorded. A change to a rule, limit or check is never decided here: it always reaches the owner (`rules-core.md` R17).

**Autopilot:** while a grant is active, a warning or nit waiver, one extra round at `review_cap` and a coder level go first to the delegate (`forge autopilot ask`, then `forge autopilot waive|round|level --decision <id>` only when it acted, `autopilot.md` §2). Never delegated: `next = stop`, `ask_human`, a security question, a critical finding, a budget raise, `redecompose` — they wait for the owner (`autopilot.md` §5).
