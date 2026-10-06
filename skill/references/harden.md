# Harden — the adversary is an L3 session; the human answers; you relay

## §1 The author loop

`forge author --job harden --brief <brief> --facts <sheet> --draft <plan> [--answers <file>] --out <plan>` runs a fresh L3 closed-book session over the brief, the facts sheet, the current draft and the answers so far. It returns `{draft, questions: [{id, question, options[], why, blocking}], cost}`.

Loop: write the draft to the plan file → print `questions[]` to the human in chat (and to the Solo scratchpad when present) → collect answers into `<answers file>` → re-run with `--draft` and `--answers`. Stop when **no blocking question is left**: `questions[]` is empty, or every remaining item is non-blocking and the human chose to skip it — a skipped question is written into the plan's Q&A as `skipped` with the human's word, never silently dropped. Each round prints its cost. **The human answers; the orchestrator never answers for them.** **Autopilot:** never delegated — harden questions wait for the owner, grant or not (`autopilot.md` §5). Under Solo the human may run harden as an interactive L3 session (`harness.solo.interactive_harden`).

## §2 What the L3 session interrogates (so you can judge its questions)

- Correctness and data integrity: idempotency (can it run twice?), rounding and units for money-like values, timezone and date handling, migration reversibility, backfill for existing rows, races between blocks at runtime.
- Authorization and tenancy: every new surface behind the project's policy pattern; cross-tenant and cross-user leak paths.
- Performance and safety: N+1s, heavy work queued, retry and backoff, rate limits on new public endpoints.
- Testing and rollout: happy, failure and weird paths named per block; permission-denial tests; a flag or backout plan where state changes are risky.
- Decomposition audit: `owned_files` truly disjoint, `depends_on` acyclic, every block independently testable, every clause mapped (`plan.md` §3), forecasts within the split rule.
- Benchmark: how comparable products solve the core mechanic — concrete adoptable gaps, not "best practices".

## §3 The tranche gate — a plan is spent when its blocks are

Never roll from one tranche into the next without an architect pass. Risk class is not size: a tranche of pure computation says nothing about the next one's migrations, money or historical data. Hard stops: the plan's blocks are delivered; the next work touches **schema, money, auth/tenancy or historical data** when the current one did not; candidates are tagged planned-not-hardened; or several waves shipped mechanism nothing calls (ask *adoption or more mechanism?*).

Before a coder is spawned the architect answers in writing: which block and why; the risk class and what the plan must therefore carry (down-safety, backfill strategy, idempotency, a dry-run path, proof of correctness without touching production); size, shape, owned paths, split rule; and what would make the block unreviewable, prevented up front. The S1 `security_sensitive` answer and the `proof.tiers.high.paths` floors raise the first-attempt level automatically (`decisions.md` §5); the tranche gate is the human judgement on top.

## §4 Exit

`forge plan check <plan>` green (`plan.md` §4) and no blocking question left (skipped non-blocking ones recorded as `skipped` in the Q&A, §1) ⇒ set `status: hardened` in the plan header, append the Q&A and the risks table to the plan, and say: *next: `/code-forge code <plan>`*.
