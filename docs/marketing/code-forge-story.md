# Code Forge — content bank

> Source material for marketing the `code-forge` skill: landing copy, posts, threads, talks.
> Built from the 2026-09-24 design session. Every number here has a source line. Numbers marked **(claim)** come from third parties and are not verified by us.
> Status of the product: **published on npm** as `@codedology/code-forge` (install with `npm install -g @codedology/code-forge`, then `code-forge init`). Where this bank and [../how-it-works.md](../how-it-works.md) differ on a mechanism, the built behaviour wins.

---

## 1. One-line positioning

**Code Forge turns your AI coding agents into an engineering team: a cheap model does the easy work, a strong model does the hard work, tools check the facts, and a fast decision model tells them when to move.**

Short variants:
- "An engineering org for your agents, not one giant model."
- "The cheapest model that can do the job. Proof before every step."
- "Plan. Harden. Code. With receipts."

## 2. The problem (in the reader's words)

- "I pay for the top model to rename a button."
- "My agent says tests pass. They pass for the wrong reason."
- "Review happens at the end, so every bug costs a whole rework loop."
- "My workflow names models. A model changes, and my workflow breaks."
- "It works in Claude Code. My teammate uses Codex."

## 3. The idea: three layers, one job each

| Layer | Who | Job |
|---|---|---|
| Generate | The model ladder (L0 → L3) | Writes code at the lowest level that can do it |
| Decide | Jev (System 1) + a top model (System 2) | Classifies, routes, retries, escalates, completes |
| Verify | Tests, types, lint, diff | Facts. No model is asked what a tool can answer |

Quote to reuse: **"Jev handles uncertainty. Software handles facts."** (from @sairahul1's article, credited)

## 4. Features — each with its "why"

### 4.1 One matrix, three providers
Anthropic, OpenAI and xAI, each on four levels ranked by intelligence. Default: Anthropic.

| Level | Job | Anthropic | OpenAI | xAI |
|---|---|---|---|---|
| L0 | triage, reads, renames | Haiku 4.5 | GPT-6 Luna (low) | Grok 4.7 (low) |
| L1 | plain features | Sonnet 5 | GPT-6 Luna (high) | Grok 4.7 (medium) |
| L2 | hard code + file review | Opus 5.5 | GPT-6 Sol (high) | Grok 4.7 (high) |
| L3 | plan, harden, rulings, System 2 | Fable 5.1 | GPT-6 Astra (high) | Grok 4.7 (xhigh) |

Why: skills name **levels**, never models. When a model changes, you edit one line of config, not your workflow.
Escalation: start at the level Jev picks, go up one level after two failed tries, a review round that leaves two or more warnings, a security change, or a System 2 ruling. Never skip a level. Coders stop at L2; a stuck block gets one L3 patch of at most 80 lines, then the human decides.
Note on OpenAI: Codex always has a shell, so it cannot run the closed-book roles (reviewer, judge, System 2, plan author). With the OpenAI matrix, use Claude or Grok for L2 and L3 reviews and plans; Codex still codes.

### 4.2 System 1 / System 2 decisions
- **System 1 = Jev** (TypeSafe): typed answers (choice / yes-no / score) with a probability, in about half a second.
- **System 2 = the L3 model**: wakes up only when System 1 is not sure.
- Starting thresholds: p ≥ 0.90 System 1 acts · 0.60–0.90 System 2 checks · < 0.60 System 2 decides.
Why: most decisions in a coding run are small and frequent. Paying a frontier model to answer "retry or escalate?" 40 times a day is waste.

### 4.3 Review each file when it is done — not everything at the end
The coder says "file done". A new, blind reviewer session checks only that file's diff. The coder keeps working and gets the findings while the file is still fresh.

**Adaptive review (one model family):**
1. Tools on the file first.
2. Jev scores the file's risk (0–3). Low: one quick L2 review. Medium: one full L2 review. High: two blind L2 sessions with different lenses (correctness vs contracts/security) plus an L3 judge.
3. Jev triages every finding: real defect ⇒ fix now; unclear ⇒ L3 judge; preference ⇒ logged as a nit.
4. After each fix, Jev asks "resolved?". One round that leaves two or more warnings ⇒ the next fix runs one level up. A file gets at most four rounds.
5. The ledger tracks Jev's answers against outcomes, so thresholds are tuned from data.

**Consensus review (multimodel on):** for a file above risk 1, two L2 reviewers from two different providers, blind to each other, then a new L3 session from a third provider reads both reports and rules. Low-risk files and docs blocks keep one reviewer: a consensus review of a docs block costs a lot and finds little.

Why: isolated sessions carry only a diff or two reports, so tokens stay small; two different lenses find different bugs; nobody reviews their own work.

### 4.4 Works in any harness
Built on the open Agent Skills standard (`SKILL.md`). The installer finds Claude Code, Codex, Grok, Gemini and Cursor and puts the skill where each already looks. With Solo, agents run as processes you can watch; without it, Claude Code's own subagents do the job (other harnesses need Solo or `engine: subprocess`).

### 4.5 Install once, ready on day 1
`npm install -g @codedology/code-forge` then `code-forge init` — patterns borrowed from Vite+, the Laravel installer and `npx skills`:
- short wizard, every question has a default;
- `--no-interaction` plus override flags for scripts;
- one line of JSON when an agent runs it;
- detects your test/lint/type/format commands from the project's manifest (`package.json`, `composer.json`, `Cargo.toml`, `pyproject.toml`, `go.mod`);
- ends with `doctor --quick` (config, links, keys); the full `doctor` makes one real Jev call and pings every model level, so you never discover a broken setup mid-run.

### 4.6 Keys: yours, local, never in the repo
Per-user key store on the OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service). Setup asks for the key if it is missing. 1Password is optional: paste the item ID or link (or an `op://` reference); code-forge reads it once and caches the key in the keychain for 8 hours. The config file stores **references**, never values.

### 4.7 Cost you can see, and a stop you set
Every session row in the ledger carries an estimated cost. `code-forge report` shows the cost per block (open blocks too) and the spend per run. `budget.usd` caps a run: one warning at 80% of it, no new session at 100%.

Why: "the budget stop is the feature". A run that stops at a known number is better than a bill you discover later.

### 4.8 Errors you can report in one step
Failed commands go to a local, cleaned log. `code-forge logs report` cleans the text twice, shows the whole issue, and files it only after a yes. An error that a newer version already fixes gets "upgrade" instead of a new issue.

## 5. Proof points we can say today

- **Jev, 8 of 8 correct** on realistic blocks (lane, next step, finding triage), **~0.5 s per call** (486–645 ms), **~600 input tokens per call**. Source: our probe, 2026-09-24, `jev-latest`.
- System 2 hand-off observed in the wild: a medium block's risk came back at confidence 0.40, which routes to System 2 by design.
- The rules come from a real 16-PR production run on a private client codebase: evidence re-measurement, wire truncation detection, facts sheet before dispatch, budget stops. Each rule names the incident that created it.

**(claim)** from @sairahul1's article: Luna $0.10 / M input tokens, Sol $2.00 / M (20×), Jev $0.042 / M input; a hypothesised task split of 55% / 26% / 13% / 6% across the four lanes. Quote as his figures, never as ours.

## 6. Lessons worth a post each

1. **"A green test can lie."** Every defect that escaped our review had the same shape: an assertion that passed for the wrong reason. Measure *how many* and *which element*, not *whether it exists*.
2. **"The agent's report is a claim sheet."** The orchestrator re-measures every number: line counts, test deltas, touched files.
3. **"Messages get cut silently."** A 2,284-byte instruction arrived as its last ~240 bytes. Now every message ends with a sentinel; no sentinel, no action.
4. **"Read the code before you plan it."** A cheap facts sheet (real signatures, validation rules, tests) before dispatch turned four mid-block stops into zero.
5. **"The stop is the feature."** A coder that forecasts, overruns and stops did the right thing.
6. **"Don't ask a model what a compiler knows."**
7. **"Prove the test can fail, not that a mutant can survive."** Our hand-written mutant lists reached ~1,081 mutants and ~13,900 lines, and 30 of them stopped applying the moment code moved — the CPU was never the cost, the upkeep was. Code Forge's proof is a red→green runner instead: a new test must fail before the fix and pass after, on every change; there is no mutation-testing tool in the product (a deliberate cut, not a gap).
8. **"1Password is not a prerequisite."** Its CLI login lasts 10 minutes idle, 12 hours max, per terminal. Agents open many shells. So Code Forge keeps its own per-user key cache on the OS keychain and uses 1Password only as an optional source.

## 7. Ready-to-edit copy

**Landing hero**
> Code Forge
> Your agents, organised like an engineering team.
> Cheap models for easy work. Strong models for hard work. Tools for facts. A fast decision model in between.
> `npm install -g @codedology/code-forge` then `code-forge init`

**Thread opener**
> We stopped paying a frontier model to rename buttons.
> Code Forge routes every block to the cheapest model that can do it, reviews each file the moment it's written, and lets a 0.5-second decision model say when to move on. 🧵

**Talk title:** "System 1, System 2: how we made AI coding agents cheap, fast and honest."

## 8. Audience

- Solo founders and small teams shipping real products with coding agents.
- Teams that use more than one harness (Claude Code + Codex + Grok).
- Leads who need proof, not "the agent said it passed".

## 9. Do not claim (yet)

- Any cost-saving number from our own runs: the ledger records data, but no calibrated baseline is published yet.
- "Works with every harness": only after `doctor` passes on each one.
- Mutation-testing as a product feature: decided against (Q16 = cut, 2026-09-25) — do not promise it, ever.
- Codex as a reviewer, judge or plan author: it is refused for those roles until it has a mode without a shell.
- "Sends nothing": true for telemetry (there is none), but `code-forge logs report` can file a public GitHub issue after the user reads it and says yes. Say "nothing leaves your machine without your yes".

## 10. Sources

- Design page: https://claude.ai/artifact/NURHEVXE1wy7xHZAtHFvvY
- @sairahul1, "How To Code Almost Forever for $20/Month With Codex + GPT-6 Luna": https://x.com/sairahul1/status/2102694818485096803
- TypeSafe API: https://docs.typesafe.ai/api.md
- 1Password CLI session rules: https://developer.1password.com/docs/cli/app-integration-security/
- Agent Skills standard: https://agentskills.io · multi-harness installer: https://github.com/vercel-labs/skills
- Laravel installer agent detection: https://github.com/laravel/agent-detector
