# Core rules — seventeen, each with its why

The headings below are the list in `SKILL.md` §4. A rule names levels and roles, never a model. Config keys are the ones `forge validate` accepts.

### R1 — Coder ≠ reviewer, always
Every `forge review-file` call starts a new L2 session in an empty directory; the session that wrote a file never judges it. Why: a same-session review reads its own intent, not the diff.

### R2 — The orchestrator never codes, reviews or judges
Plans and patches are L3 sessions (`forge author`, the L3 rung); code is a coder's; verdicts come from the worker's sessions. Why: the orchestrator's context is the scarce resource, and a hand-written "surgical fix" is unreviewed code.

### R3 — State lives outside the conversation
The run record, the ledger, the plan file, then Solo scratchpad/kv when present. Why: a session dies mid-run; anything only in its context is lost (`continuity.md` §1).

### R4 — The evidence gate is a re-measurement
A coder's report is a claim sheet; `forge block close` recomputes every row itself (`code.md` §5). Why: green assertions pass for wrong reasons; a quoted number is not a number.

### R5 — No destructive git, no production, no regex edits
The forbidden list (`security.md` §2) is rendered into every spawn and grepped from every transcript; code is restructured by hand; a check's output is read, never discarded. Why: each entry names a real incident.

### R6 — The budget stop is the feature
A coder that forecasts, overruns and stops did the right thing; the cap moves once per run, by the human, on record (`code.md` §6). Why: a rule that never fires protects nothing.

### R7 — Wire truncation is detectable
The wire carries a pointer ≤ 200 B ending in `<<<EOM>>>`; the coder echoes `ACK <sha8> lines=<n>` before any tool call; no ACK ⇒ resend once, then park. Why: an input channel truncated a message to its last 240 bytes, silently.

### R8 — A mechanism needs a caller
Every acceptance clause of a parent maps to exactly one block; a block that ships a mechanism names the block that calls it or is not shipped (`plan.md` §3). Why: sub-blocks marked done while the parent's acceptance was unmet.

### R9 — Own-family adjudication is visible and asymmetric
When the orchestrator shares a family with the coder it may accept a finding alone but never dismiss, defer or waive one; every such call is a named ledger row (`decisions.md` §5). Why: a judgement inside the family is not independent.

### R10 — A fresh isolated session per job
Reviewer, recheck, judge, S2, author and facts each run as a new print-mode process with an empty cwd and the packet on stdin, whatever the engine. Why: isolation is what makes the harness irrelevant.

### R11 — A review without matching `reviewed_hunks` is not a review
Exit 0, a valid schema, `reviewed_hunks` equal to the packet's hunk headers in order, and enough output; anything else is `review.unavailable`, never approval. Why: three reviewers returned stubs on a green gate that hid a money bug.

### R12 — The L3 rung writes a patch, never a feature
On escalation past L2 an L3 session returns a diff ≤ 80 lines inside the owned files, reviewed as a `patch_check` round; the block then continues at L2. Why: bulk coding at L3 is the cost sink the ladder exists to avoid.

### R13 — Measurement runs in an export or behind the proof lock
Gates, test-count deltas and red→green run in `.code-forge/export/<block>/`, never in the shared tree while other blocks are active (`proof.md` §3). Why: parallel blocks in one tree cannot be measured apart.

### R14 — No design before facts
`forge author --job plan` refuses without `--facts`; every clause that cites a flag, path, version or API cites a VERIFIED fact or sits in the unbackable list with a tolerance (`plan.md` §0). Why: a plan built a reviewer on a flag that did not exist.

### R15 — Re-checks shrink: fix hunks only, four rounds, then a patch, then a human
Round n ≥ 2 reviews the fix hunks and the open findings; the open set shrinks every round or the coder climbs; `review.max_rounds_per_file` rounds, then the L3 rung once, then `stopped: review_cap` (`review.md` §5). Why: whole-file re-checks added new defects every round.

### R16 — Proof is exact assertions on the main behaviour
One exact test per clause plus a negative or boundary where the clause has a failure mode; red→green for every new test; there is no mutation tier (`proof.md` §1). Why: exhaustive suites hid the defects the isolated review found.

### R17 — Never relax a rule alone
The orchestrator and any delegate never change, relax or remove a rule, limit, permission or check without the owner: config limits, thresholds, budgets, the forbidden list, gates, review requirements, or a local patch that loosens a check. When a limit blocks work: stop, show the data, propose options, wait. Only the owner raises a limit (the budget once per run, R6); during autopilot only the owner's own `forge autopilot approve` (`autopilot.md` §5). Why: a limit relaxed to finish the work protects nothing, and the owner learns of it too late.

## What survives from the previous pipeline (14 rows)

| survivor | lands in | kind |
|---|---|---|
| Coder ≠ reviewer session | `rules-core.md` R1; `review.md` §1 | core rule + mechanism |
| State outside the conversation | `rules-core.md` R3; `continuity.md` §1 | core rule |
| Launch verification + completion sentinels | `adapters/solo.md` §3; `adapters/subprocess.md` §2 | mechanism |
| Evidence gate = re-measurement | `rules-core.md` R4; `code.md` §5 | core rule + mechanism |
| Wire truncation detection | `rules-core.md` R7; `adapters/solo.md` §2 | core rule + mechanism |
| Facts sheet before dispatch + coder facts diff | `plan.md` §0; `code.md` §1 | core rule |
| Risk-class gate between tranches | `harden.md` §3 | core rule |
| Budget caps; the stop is the feature | `rules-core.md` R6; `code.md` §6 | config + core rule |
| Mechanism needs a caller | `rules-core.md` R8; `plan.md` §3 | core rule |
| Draft PR only after independent review | `code.md` §7 | config + core rule |
| Continuity / takeover | `continuity.md` §2 | core rule |
| Base-moves rules | `continuity.md` §3 | core rule |
| Definition of Done | `code.md` §8 | core rule |
| Own-family adjudication visible and asymmetric | `rules-core.md` R9; `decisions.md` §5 | core rule |

Working habits folded into the rules above rather than listed: verification chains use `;` not `&&` and arrays for word-splitting; resolve a target before any destructive state command; spawned agents inherit the production ban; test every fix red→green; commit trailers follow `commit.trailers`; one PR ready at a time (`wip.ready_prs`); never close a process this run did not spawn; long input goes to a file plus a pointer; simplest solution first; no duplicate logic; read the source before reviewing it.
