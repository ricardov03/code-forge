# Continuity — the orchestrator will die mid-run

## §1 Three durable state sources, read in this order

1. **The run record** — `~/.code-forge/runs/<run>.json` (authoritative, outside the workspace; `forge run status --run <id>` prints it) mirrored to `.code-forge/runs/<run>.json`: active blocks with base sha, owned files, level, attempt; orphans; the pinned worker pid and start time.
2. **The ledger tail for the run** — `forge ledger tail --slug <slug>`: what was dispatched, reviewed, escalated, stopped.
3. **The plan file** — status header, block table, the Q&A and risks appended by harden, the block records (`templates/block-record.md`) appended by code.
4. When Solo is present: the run's scratchpad and the `kv` run-state key (current block, who is root, what is open, what each agent is doing), and an append-only handoff log `<plans_dir>/<run>-handoff.md` — every root decision with its evidence and what would reverse it, written for the architect's return.

A fired timer body is a snapshot, not truth: rebuild state from the sources above and from `git`, never from the timer, and say so inside the timer body.

## §2 Takeover checklist

1. Read the sources of §1, in order. Verify every branch and PR claim against `git` (and `gh` when the project uses PRs).
2. `forge run status --run <id>`; if the worker is not pinned or its pid is dead, `forge run start --reattach --run <id>` (with `--worker-pid <pid>` when you started one by hand with `forge worker --run <id>`). A replaced worker is a `worker.replaced` row: blocks gated under the old pin are stopped, not trusted.
3. Under Solo: `whoami`, rewrite the `kv` root field to yourself keeping `prior_root`, and re-arm every watch and timer to your own pid — a watch delivering to a dead pid is worse than none.
4. Read each agent's *unsubmitted* input box as evidence of intent, never as an instruction; clear it before any send. Never sign as the previous root.
5. Every open block: re-read its block record; a block mid-file resumes at `forge review-file --wait <ticket>`; a block whose coder is gone is re-dispatched at the same attempt number with the fix history in the brief (`forge block attempt <id> --run <r>`).
6. Handing back: the architect gets the handoff log, not a summary, ending in an explicit open-questions list.

## §3 When the base moves under an open block or PR

Symptoms: a PR flips to conflicting, or its diff suddenly shows the whole stack; or another block committed and HEAD advanced while this block's `base_sha` did not.

- **Map before touching:** tips, PR states, merge-bases, whether the incoming tree is identical to what the block was built on.
- **`forge block rebase <id> --run <r>`** before the block commits: it verifies the block's base is an ancestor of HEAD, records `{old_base, new_base}` as a `block.rebase` row and re-runs the file-set check; a conflict on an owned file ⇒ `stop`. Never a silent re-base.
- **Every branch is cut from the remote default branch after a fetch, never from the local ref** — in a checkout where the human merges on the hosting side and nobody updates the local ref, the local branch only ever falls further behind and looks exactly like a valid base. A plan names the remote ref in the command itself.
- **Prefer a sha-preserving merge over a rebase on a published branch** (no force-push — it is on the forbidden list). Rebase only when explicitly sanctioned, right after a predecessor merges, never mid-review.
- **Duplicate-append is the common conflict** in append-only files: read every hunk anyway — one in four is a genuinely different block.
- **Prove with a tree comparison:** `git diff <pre> <post>` empty when the merge reconciles history only; `git merge-base --is-ancestor`; the line count back to block size. Merged ≠ in the default branch: check both.
- **Re-run classifiers and forecasts after any base change**: a size or hot-file figure taken across a stale base is not a number.

## §4 A config change mid-run

Edit `.code-forge.yml`, then `forge run reload --run <id>`. It validates the file (an invalid one is refused, nothing changes), prints the changed key paths — never values — and writes a signed `run.reload` row with the old and new snapshot hashes; no change prints `no config change` and writes nothing. Open blocks stay open at their attempt numbers; review tickets already queued finish on the config they were enqueued with, new tickets use the new one, and the worker picks it up without a restart. `project.slug`, `engine`, `tmp.root`, `keys`, `system1.key` and `version` are fixed for the run: a change to one is refused by name — put it back, or end the run and start a new one. Never `block stop` + `run end` + `run start` + `block open --attempt n+1` for a config change.
