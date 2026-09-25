# Degraded — no Solo, no subagent tool, a level unavailable

## §1 Engine selection and the stop text

`engine: auto` at preflight: Solo present (an MCP entry and a working `whoami`) ⇒ `solo`; else the invoking harness exposes a subagent tool ⇒ `harness`; else the run stops with exactly:

```
code-forge: no Solo and no subagent tool in this harness.
  To run coders as detached CLI processes (power-user mode) add to .code-forge.yml:
    engine: subprocess
  Then re-run. (R2: this engine is never selected automatically.)
```

and writes ledger `run.stop {reason: no_engine, harness: <name>}`. `subprocess` is chosen only by explicit config (`adapters/subprocess.md`). The engine is printed by `forge run start`, written to the run record, and never changes mid-run. `forge init` prints the same text at setup when it detects the situation, so nobody discovers it at the first run.

## §2 What degrades without Solo (print this banner once per run)

```
code-forge: engine <harness|subprocess> — no Solo on this machine.
  no locks      ⇒ caps.coders = 1 (one coder at a time)
  completion    ⇒ the tool return (harness) or the process exit (subprocess); the sentinel is still required
  state         ⇒ the run record + the plan file + the handoff log (references/continuity.md)
  live view     ⇒ forge ledger tail --slug <slug>
```

**Unchanged in every engine:** session isolation and the packet on stdin; the worker and signed rows; the block gate; proof; the escalation ladder and the L3 patch rung; the D4 stop at L3; the facts rule; the convergence rule.

## §3 A level unavailable (CLI missing, login expired, HTTP 402, rate-limited twice in a block)

Per level, in `levels.L<n>.fallback` order: the first entry rides on the provider's own fallback flag inside one session; every further entry is a fresh spawn after the previous one reported unavailability. Every spawn prints `level=<Lx> provider=<p> model=<id> effort=<e> fallback_step=<n>`.

- **L3 unavailable** ⇒ banner; `harden` and `author` refuse to run on a fallback below L3's family rank unless the human says so; judge and S2 use the fallback (`l3_fallback: true`); none ⇒ high-tier files are `review.unavailable`, S2 ⇒ stop and ask.
- **L2 unavailable** ⇒ next fallback; none ⇒ reviewers `review.unavailable`, coders stop.
- **L1 / L0 unavailable** ⇒ next fallback; none ⇒ the next level up takes the call and the row is flagged `overspend` (the facts delegate too: L0 ⇒ L1, flagged).

A second provider unavailable at run start in consensus mode ⇒ `consensus unavailable: <reason>`; ask the human once per run whether to continue adaptive; non-interactive ⇒ stop.

## §4 Other degradations `doctor` reports

`ping=skipped(402)` for a provider whose balance is exhausted is a WARN, never a FAIL. A provider with no coder binary on this machine is detection-and-linking only. `codex: forbidden list is prose-only` means the rules file was not honoured — the transcript grep remains the detector. `claude: path deny rules not honoured` likewise. `tmp: <n> stale roots, <m> stale pids` above zero is cleaned by the next `forge run start`.
