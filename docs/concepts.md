# Concepts

A glossary. Each term links the idea to the command or config key that makes it real.

| Term | Meaning | Where you see it |
|---|---|---|
| **Brief** | A short text that says what to build. The input to the Plan job. | `plans/<name>.md` |
| **Facts sheet** | Every claim in the brief (flags, paths, versions, commands, env names) checked by an L0 delegate with a read-only command, tagged VERIFIED, NOT-FOUND or UNVERIFIABLE, with the command and its output line. The delegate works on a read-only snapshot of HEAD, so uncommitted files are invisible unless passed with `--sources`. | `code-forge facts` → `plans/<name>.facts.md` |
| **Plan** | The L3 draft: design, blocks, a caller map, acceptance clauses, forecasts. Its §0 is the facts sheet, verbatim. | `code-forge author --job plan` → `plans/<name>.plan.md` |
| **Block** | One deliverable of the plan, coded by one coder and closed by one gate. Ends in one commit. | `code-forge block open <id> …`, `block close <id> …` |
| **Owned files** | The exact paths (or disjoint globs) a block may change. Blocks that run together never share one. A changed file outside the set is an *orphan* and stops the gate. | `--owned <paths…>`, `block claim <id> <path>` |
| **Acceptance clause** | One testable sentence of a block. Each clause names at least one test id (`<file>::<case>`). | `--acceptance <file>` (YAML or JSON list of `clause` + `tests`) |
| **Level** | A rung of the model ladder: `L0` small, `L1` medium, `L2` high, `L3` top. The config maps each level to provider, model, effort and fallbacks. Skills name levels, never models. | `levels.L0`…`levels.L3`, `code-forge resolve L<n>` |
| **Lane** | The level S1 picks for a block's coder: `L0`, `L1`, `L2`, or `split` (too big). L3 is never a lane. | S1 question `lane` |
| **Running level** | The level a coder actually runs at. It starts at the lane and climbs one step per escalation. **L2 is the highest running level.** | `dispatch` and `escalation` ledger rows |
| **L3 rung** | The step above L2: one fresh L3 session writes a patch of at most 80 lines in the owned files, once per block. Then the block continues at L2. | `escalation.l3_mode: patch` (default) |
| **Tier** | The proof class of a file. **High** when S1 risk ≥ 2, a `proof.tiers.high.paths` match, or security-sensitive. Otherwise **light**. | `code-forge proof tier --file <path> --risk <0-3>` |
| **S1 (System 1)** | Jev, a small fast classifier model from TypeSafe. Returns an answer plus a probability in about half a second. Never writes code or prose. | `code-forge jev ask <question-id> --state <file>`, `keys.jev` |
| **S2 (System 2)** | A fresh L3 session that checks or makes a decision when S1 is not sure (confidence below `act`, default 0.90, or a small margin). | `code-forge s2 --packet <file>` |
| **Judge** | An L3 session that reads two review reports and rules which findings are real. In multimodel mode it comes from a third provider. | `review.multimodel`, `review.judge_sees_diff` |
| **Orchestrator** | The session you talk to. It dispatches, re-measures and records. It never codes, reviews or judges. | the skill, `skill/SKILL.md` |
| **Worker** | The background process that serves review tickets, runs closed-book sessions and signs ledger rows. Started only by `run start`. | `code-forge worker` (never by a coder) |
| **Finding severity** | A reviewer's finding is triaged into **fix now** (`fix_now`: must be fixed before the file is approved), a middle band sent to an L3 ruling, or a **nit** (logged, not blocking). A finding raised outside a fix hunk in a re-check is a **late finding**, ruled at `block close`. | `thresholds.findings.fix`, `.nit`, `.resolved`; `review.late_findings` |
| **Fix round** | One review pass on a file. Round 1 is the full tiered review; round 2 and later re-check only the fix hunks. The open `fix_now` set must shrink each round. | `review.round` ledger rows |
| **Review cap** | The most rounds a file gets: `review.max_rounds_per_file`, default 4 (range 2–6). Then the L3 rung once, then `stopped: review_cap` for the human. | `review_cap` in `report` |
| **Waiver** | A human-only decision to accept an open finding or an unproven high-tier file. Written as a signed row with `by: human`. A coder can never run it. | `code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text>` |
| **Ledger** | The append-only event log per project: one JSON row per dispatch, review, decision, proof, escalation, stop. Gate-relevant rows are signed. Never committed. | `~/.code-forge/ledger/<slug>.jsonl`, `code-forge ledger tail --slug <slug>` |
| **Signed row** | A ledger row carrying an HMAC made with the per-run key in `~/.code-forge/runs/<run>.key`. `block close` trusts only rows whose MAC verifies. Same-user boundary only. | `mac` field, `ledger.tamper` row |
| **Proof row** | A signed `proof` ledger row from the red→green runner: the test failed with an assertion against the base code (`red_kind: assertion`) and passed against the new code. Required at `block close` for each changed high-tier file. | `code-forge proof red-green <block> --run <r> --test <file::case>` |
| **Measurement export** | A clean copy of the block at its base sha plus its owned files, where gates and red→green run so parallel blocks do not disturb each other. `proof.isolation: lock` is the fallback. | `code-forge proof export <block> --run <r>`, `.code-forge/export/<block>/` |
| **Gates** | The project's `test`, `lint`, `types` and `format` commands, detected by `init` and stored as argv arrays. | `gates.*`, `code-forge gates detect\|run` |
| **Run** | One orchestration session with a worker, a signing key and a run record. Every block belongs to a run. | `code-forge run start\|status\|end` |
| **Engine** | How coders are started. **`solo`**: Solo processes you can watch (picked by `engine: auto` when Solo is present). **`harness`**: the harness's own subagent tool, one coder at a time (Claude Code). **`subprocess`**: detached CLI processes; only when you write `engine: subprocess` yourself. Reviews run the same way in every engine. | `engine` key, printed by `run start` |
| **Harness** | An agent tool the skill is installed into: Claude Code, Codex CLI, Grok CLI, Gemini CLI, Cursor, Copilot. | `code-forge init --harness a,b`, `code-forge list` |
| **Sentinel** | The line that ends a coder's turn: `===BLOCK <id> COMPLETE===` or `===BLOCK <id> FAILED: <reason>===`. | the coder's report or log |
| **Pointer** | The short message a coder receives: `BRIEF <path> lines=<n> sha=<sha8> <<<EOM>>>`. The coder must answer `ACK <sha8> lines=<n>` first, so a truncated message is detected. | printed by `block open` |
| **Forecast** | The coder's estimate of test cases and net lines. Over 1.25 × the line forecast stops the block and asks for a split. | `block open --lines <n>`, `forecast_vs_actual_lines` in `report` |
