# Changelog

All notable changes to `@codedology/code-forge` are recorded here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions follow semver ahead of a 1.0.0 that
waits on a second real consumer (plan §10.4, Q17).

## [Unreleased]

### Added

- Skill rule R17 "Never relax a rule alone": the orchestrator and any delegate never change, relax or remove a limit,
  threshold, budget, forbidden entry, gate or review requirement without the owner; when a limit blocks work they stop,
  show the data, propose options and wait. The skill also asks to supervise cost continuously (budget.usd, spend after
  every block close).

### Fixed

- A verb run from a subfolder uses the project root (the git root, or the nearest regular `.code-forge.yml` below it;
  outside git never above HOME) for its config and ledger slug: `jev`, `plan`, `author`, `facts`, `spawn`, `s2` and
  `run start`. A `.git` at HOME (a dotfiles repo) does not make HOME a project.
- `plan check` reads the list form of the "cannot back" section that the template shows, still reads the old table
  form, and splits clauses on `;` and on ` · (n)`.
- `facts`: a flag claim is proved only by `grep -rn -- <flag> <source folder>` (hits in source files under that
  folder; docs, plans, tests, fixtures and the brief never count). The delegate never runs `--help`, `-h`, `--version`,
  a pipe, a list or `rg`; grep options come from a fixed allow-list. No Claude allow rules are added.
- A command claim written without its CLI (`run reload`) gets the project's own bin only when the brief names that bin
  and the first word is not a well-known CLI.

## [0.6.0] — 2026-10-06

### Added

- `code-forge autopilot start|status|stop` (issue #5): a time-boxed grant (at most 24 h) of owner decisions — allow
  `waive:warning`, `waive:nit`, `round:extra`, `model:choose` — with a fixed deny list in code (critical or proof
  waivers, skipping reviews, any limit or rule change incl. parent keys, plan/design approval, merges, destructive
  actions, budget raises). Signed grant, stop and expire rows; expiry is checked at every command. Coders may not run
  it.
- `code-forge autopilot ask`: a closed-book delegate (never Codex) answers one owner question inside the grant; every
  answer is one signed `autopilot.decision` row. It acts only when the question offers at least 2 options, the answer is
  one of them, it is within scope, does not ask to escalate, and its confidence is at least `autopilot.min_confidence`
  (default 0.7); otherwise the question goes to the owner (exit 3). `autopilot.*` counts as a limit key.
- `code-forge autopilot waive|round|level`: actions that need the delegate's acted decision (`--decision <id>`, same
  grant, scope and block/file/finding, at most 30 minutes old, with the matching word: `waive`, `allow` or the level).
  Waivers cover warnings and nits only (severity read from the signed review rows) and are written `by: autopilot`; each
  opens one tracked GitHub issue in the project's repo. The block gate accepts an autopilot waiver only under a grant
  and decision that covered it. One extra fix round per file per grant; the coder level comes from the Jev lane, capped
  at the plan level + 1 and L2.
- Autopilot budget stop per category: with `--budget review=<usd>,coding=<usd>`, new sessions in a category are refused
  once its spend since the grant started plus running reservations reaches `--stop-at` x cap ("autopilot paused: ...
  waiting for the owner"), with one signed `autopilot.pause` row; `autopilot status` shows spend per category.
  `code-forge autopilot approve --key --value --until` (owner only, in a terminal, no `--yes`) changes one setting until
  a time and restores it automatically, crash-safe, with signed rows; a value changed by hand meanwhile is left alone.
  While a grant is active, `run reload` refuses any limit key.
- `code-forge autopilot binnacle` and `autopilot log`: the logbook of an autopilot run, in the shape of an overnight-run
  page (status at a glance, decisions with why and what would reverse them, blocks, open questions, actions only you can
  take, incidents, timeline newest first) plus a full log of every autopilot event. As JSON or as
  `autopilot-binnacle.md` / `autopilot-log.md` in the run dir, rewritten after each delegate decision and scrubbed.
  `--link <https url>` stores the live page's link.
- Autopilot log page for your return: the skill (`skill/references/autopilot.md`) creates one Claude Docs doc per run
  with a Binnacle tab (the title and byline, then 7 sections) and a Full log tab, stores its link first, updates only
  the changed sections after each decision, block close or incident, and at stop adds the final rows and asks you to
  review the open questions and your actions. `autopilot status` and `stop` print the link (or the Markdown file paths
  without the Docs connector). Every owner stop in the skill says what autopilot may do there or that it is never
  delegated. New `docs/autopilot.md`.

## [0.5.1] — 2026-10-04

### Changed

- Docs: README "What's new in 0.5" (with the bench numbers), the 0.5 config keys marked, how to install a tagged release
  before it is on npm (getting-started), and how maintainers update their own install after a release (releasing).

## [0.5.0] — 2026-10-04

### Added

- Per-provider session limit `review.provider_concurrency` (defaults anthropic 4, openai 2, xai 2) and rate-limit
  backoff: a rate-limited session waits about 2 s, then about 6 s (±30 %), before retrying the same step, instead of
  retrying at once. New in-process keyed locks and semaphores (`src/util/locks.mjs`) prepare parallel review.
- Parallel review in the worker: `review.parallel_tickets` (1–16, default 3) review tickets run at once, oldest first.
  The same file is never reviewed twice at once; a block's budget row and its one L3 rung are taken under a block lock;
  a crashed ticket never stops the others; on stop, running tickets finish. `run reload` can change the pool size. `1`
  keeps the serial behaviour.
- `code-forge doctor` shows a `review concurrency` row (tickets at once and provider slots); maintainers can measure the
  pool with `npm run bench:review` (6 fake 2 s reviews: about 12 s at 1, about 4 s at 3).

### Changed

- Skill: a plan that passed `plan check` is shown to the owner for approval in the harness's plan mode (Claude Code:
  EnterPlanMode → plan file → ExitPlanMode); no block is dispatched before that approval.
- `budget.usd` holds when sessions start together: each session reserves its estimated cost under one lock (after taking
  its provider slot) and releases it when its row is written or it fails. A refusal caused by running sessions says how
  much they hold and this session's estimate; a session whose estimate alone is above the budget says so.
- `code-forge review` enqueues every file first, then waits on all of them together under ONE deadline for the whole run
  (`--max`, default 900 s, now a run deadline, not per file), prints each result on stderr as it finishes, and keeps the
  final table in file order. At most 8 wait children run at once.

### Fixed

- `code-forge facts` verified nothing: delegates echo claims as `<kind>: <token>` and every answer was dropped as "no
  answer". Answers now match by that form or by fact id (one answer per claim); a check that breaks the read-only rules,
  or a silent command (`test`, `grep -q`), marks only that claim unverifiable with the reason instead of refusing the
  whole sheet; the `<cli> <subcommand> --help | grep -c -- <flag>` check is allowed only for CLIs the brief names, never
  interpreters or launchers.
- Two files of one block could both take the block's single L3 patch rung (the rung was recorded only when the patch
  check saved); a pending `next: patch` on a sibling file now counts as used.

## [0.4.0] — 2026-10-02

### Added

- `budget.usd` per run: every session row carries an estimated `usd`; at 80% one warning, at 100% no new session starts
  (fail closed if the spend cannot be read). `report` shows running totals for open blocks and per run; `run status`
  shows spent and budget; `ledger add coder --usd` records cloud or manual coder spend.
- `code-forge run reload --run <r>`: re-read `.code-forge.yml` mid-run without stopping blocks. Queued reviews keep the
  old config, new ones use the new one; keys fixed for the run are refused by name; a signed `run.reload` row lists the
  changed key paths (never values). Coders may not run it.
- `block close --report <file>`: a coder report without a `reviewed <path> ticket <id>` line for every changed file
  counts as FAILED (new `skill/templates/coder-brief.md`).
- Error reports: `logs report --with-doctor` adds a cleaned setup check; each logged error lists the names of the last 5
  commands (no flags or values); recovered problems are logged as warnings (1Password retry, review retry, System 1
  fallback, budget at 80%), shown by `logs` and `logs summary`, and included in a report only with `--include-warnings`.
  `CODE_FORGE_NO_ERROR_LOG=1` turns all of it off.
- Known fixes: `logs report` checks a shipped table of fixed errors and, on an older version, tells you to upgrade
  instead of filing (`--force` files anyway). An `error-triage` GitHub Action labels error reports by fingerprint,
  comments once when the error is already fixed, and writes a weekly count. Maintainers record fixes with `npm run
  known-fix -- add`.

### Changed

- Escalation and review cost rules from the first real run: after one review round that leaves 2+ warnings (or any
  critical), the next fix climbs one level (`escalation.*`); docs and contract blocks code at L1 or higher
  (`levels.coder_floor_docs`); risk 0–1 gets a single reviewer (`review.single_reviewer_max_risk`); docs blocks skip
  multimodel review unless `review.multimodel_for_docs` is set.
- `plan check` refuses a block whose level does not match its recorded lane (`jev ask lane --block <id> [--plan
  <file>]`, or `--rules` without Jev) and prints the lane per block. The plan author gets the exact required headings
  from one shared list; a close heading at the right position is accepted with a WARN. `jev ask` now writes to the
  project's ledger slug (it used to default to `code-forge`).

### Fixed

- `validate` rejects an effort the level's provider cannot take (for example `xhigh` on an openai level), for levels and
  fallback entries, instead of failing when the session starts. The builders share one list of allowed efforts, and
  messages never repeat an unknown value.
- Stuck review sessions: a session that times out is retried once before the review is reported unavailable;
  `review.session_timeout_s` now defaults to 300; the ledger note keeps a cleaned stderr tail and the stdout event types
  (never stdout text) so a hang can be diagnosed; a live worker with a fresh heartbeat is never reported as
  `worker_down`.
- `ledger tail -n <count>` limits the output (only `--n` worked). Jev score criteria keys must be exactly 0..n, with a
  clear error otherwise.

### Security

- Codex (openai) is refused for closed-book roles — reviewer, judge, S2 and plan author — because it always has a shell
  that can read files. `validate`, `resolve` and `doctor` say so. Set `review.allow_open_book_codex: true` to accept
  that risk and keep using it (with warnings). Codex coders are unchanged.

## [0.3.2] — 2026-10-01

### Fixed

- Jev: `score` questions (such as `risk`) send their criteria as a list indexed by score. Jev refused the keyed object
  with 422, so `doctor`'s Jev check failed and every `risk` call silently fell back to rules.
- Review: on a small diff (20 added lines or fewer), a clean pass (passed, no findings, every hunk acknowledged) needs
  only 40 output tokens instead of `review.min_tokens_out`, so a correct short review of a tiny file is no longer
  refused as `too_short`. Any finding, or a larger diff, keeps the full floor.

## [0.3.1] — 2026-10-01

### Fixed

- Claude sessions: the `--json-schema` passed to the Claude CLI no longer carries the top-level `$schema` pointer, which
  the CLI rejected ("no schema with key or ref"), so every Claude reviewer session failed at once and each review came
  back unavailable. Validation still uses the full schema.
- Grok sessions: the answer is read from Grok's `structuredOutput`/`text` envelope, so Grok reviewer, author and System
  2 calls no longer fail schema validation (`invalid-output`).

## [0.3.0] — 2026-10-01

### Added

- `code-forge logs`: every failed verb is logged (scrubbed, flag names only) to `~/.code-forge/logs/errors.jsonl`;
  `logs`, `logs summary`, `logs clear`, `logs path`, and `logs report` to share selected errors as a public GitHub issue
  after you see the full text and say yes (`gh` or a prefilled link; a last secret check stops it). Opt out with
  `CODE_FORGE_NO_ERROR_LOG=1`.
- Better error reports (`code-forge logs report`): a second, AI cleaning pass (a closed-book L1 session that only lists
  names, hosts, URLs, accounts, paths or secrets to hide; code-forge replaces them, and you see counts only),
  fingerprints that group the same error in `logs`/`logs summary` and find an earlier issue (add a "happened again"
  comment instead of a duplicate), a version check before reporting, Send · Edit in my editor · Cancel, a GitHub issue
  form, and a one-line hint after a failure in a terminal; see docs/privacy.md. `--no-ai` skips the AI pass with an
  extra yes.

## [0.2.3] — 2026-10-01

### Fixed

- 1Password: code-forge prints a line before each call that may make 1Password ask for approval, and waits 60 s instead
  of 20 s, so a Touch ID or password prompt no longer times out while you look for it.

## [0.2.2] — 2026-10-01

### Added

- 1Password item ID or item link accepted for the Jev key (init question, --jev-ref, keys set --op, keys test --ref):
  code-forge finds the vault and key field and saves a full op:// reference of IDs; 1Password failures (CLI missing,
  locked, timeout, not found, bad output, no key field) get one clear message, a retry/skip menu in init, and never
  write a partial config
- code-forge tools: lists the recommended tools (claude, codex, gemini, grok, op, solo) as installed with their version
  or missing with the install command; code-forge tools install [<id>...] [--yes] [--dry-run] installs the missing ones
  after one yes. init step 1 and doctor use the same tool table; doctor adds an INFO line naming the missing tools.

### Changed

- Releases: CI no longer publishes to npm. Publish with `npm publish` from your machine, then push the tag; the tag push
  only creates the GitHub release (`.github/workflows/release.yml`).

## [0.2.1] — 2026-10-01

### Changed

- **Two publish paths:** publish by hand with `npm publish` before pushing the tag (the tag run sees
  the version on npm, skips its own publish and still creates the GitHub release), or push only and
  let CI publish with provenance (needs `NPM_TOKEN`). See `docs/releasing.md`.
- `init` no longer asks for gates or proof settings one by one: it reads them from the project
  (gates from `detect`, `link_dirs` from the project's manifests, `copy_untracked` only for files
  that exist, high-risk paths blank, isolation `export`), prints a "Project settings (detected)" summary that marks every blank
  and names the keys to edit, then asks one choice: Use these, Customize now (the eight questions,
  pre-filled) or Leave for later. `--no-interaction` asks nothing; agent JSON gains `settings` and
  `blank`. Every question has a one-line hint; a re-run fills a gate that was blank and keeps
  hand-set ones.

## [0.2.0] — 2026-09-26

### Added

- **`code-forge review`** — review only: runs the review engine on changes you already have (your
  branch against the merge base with the default branch, your uncommitted work, or `--files`), with
  no plan, coder or proof, and never closes a block. One verdict per file, a fix list, totals or one
  JSON document (`--json`); exit 0 only when every file is approved. The run and its worker are
  always ended, also on Ctrl-C, SIGTERM and timeouts. See `docs/review-only.md`.
- **Release tools:** `npm run release -- <patch|minor|major|x.y.z>` bumps every version location,
  moves the changelog, runs every check (with rollback), commits and tags; `npm run changelog --
  <type> "text"` records changes as you go. See `docs/releasing.md`.
- **GitHub Releases:** a tag push publishes to npm and then creates the GitHub Release from the
  changelog, with links to the npm version and the changelog.

### Changed

- **Reviewers see the acceptance clauses.** Every review packet now carries the block's acceptance
  clauses (redacted, cut to fit the packet budget); a block without readable clauses is still
  reviewed, with `(acceptance unavailable)`.

### Fixed

- CI is green on Linux: a macOS-only case-insensitivity test now skips on case-sensitive
  filesystems, and the Node 22/24 matrix no longer cancels one job when the other fails.

## [0.1.0] — first release

The `plan` → `harden` → `code` → `review` pipeline for cross-model, parallel, evidence-gated
feature delivery, as one CLI (`code-forge`) plus one Agent Skill.

### Added

- **CLI verbs:** `init`, `doctor`, `run`, `block`, `worker`, `review-file`, `spawn`, `s2`,
  `author`, `facts`, `plan check`, `proof`, `gates`, `jev`, `keys`, `ledger`, `report`, `resolve`,
  `models`, `list`, `remove`, `upgrade`, `validate`, `help`, `version`. Verbs are discovered from
  `src/cli/*.mjs`, so adding one never touches the router.
- **Setup wizard** (`code-forge init`): nine steps (tools, harnesses, provider matrix, multimodel,
  keys, engine, gates, proof, doctor), every question has a default, `--no-interaction` plus one
  override flag per answer, an agent gets one JSON line, a second run changes nothing.
- **Decision layer:** System 1 (Jev) answers typed questions in well under a second; System 2 is a
  fresh top-level session woken only when System 1 is unsure; deterministic escalation precedence
  (retry → escalate one level → System 2 hand-off → the top level patches once → stop for a human).
- **Review engine:** a file-done trigger starts one or more fresh, isolated reviewer sessions
  (adaptive by risk score, or two-provider consensus with a judge); a worker process runs review
  outside the coder's own sandbox so review keeps moving even when the coder has no network; fix
  rounds converge under a bounded rule (round ≥ 2 reviews only the fix hunks, the open finding
  count must strictly shrink every round, four rounds is the cap).
- **Two-tier proof policy:** `light` and `high` (by risk score, a configured path, or a
  security-sensitive change); a red→green runner proves a new test actually fails before the fix
  and passes after. **No tool-made mutation testing ships in 0.1.0** — see "Removed" below.
- **Engine adapters** for Claude Code, Codex and Grok (subprocess argv snapshots, forbidden-list
  rendering, closed-book sessions with the packet on stdin); Gemini/Cursor/Copilot get detection
  and skill linking only.
- **Ledger and report:** an append-only, signed, per-project ledger (`~/.code-forge/ledger/`) and
  a `code-forge report` verb with a terminal view and a `--export` JSON snapshot.
- **JSON Schema** for `.code-forge.yml` (`schema/code-forge.schema.json`) and a generated reference
  at `docs/reference/config.md` (`node scripts/gen-config-doc.mjs [--check]`).

### Security

- **`signer: same-user boundary only`** — signed ledger rows detect tampering by a different user
  or a rewritten history; they are not a defense against an attacker who already runs code as you.
  `doctor` always prints this line so nobody mistakes the signature for more than that.
- **Keys never reach a coder process.** Provider/Jev keys resolve only inside the worker or the
  orchestrator's own CLI calls (chain: `env:<NAME>` → OS keychain → 1Password, cached briefly →
  ask at setup only); `keys list` never prints a value; every output path is redacted.
- A **forbidden-command list** blocks a coder from starting its own worker, waiving a review, or
  running other orchestrator-only actions, rendered into each provider's own deny-list mechanism.

### Changed / decided late in the build

- **Q16 = cut (2026-09-25):** code-forge ships **no user-facing mutation-testing tool**. Proof for
  both the product and this package's own tests is plain unit/feature tests plus the red→green
  runner — exact assertions on the main behaviour, not "mutation-verified" coverage.
- **R10:** at most 2 coders run at a time (down from 3) for the rest of the build.
- **R13:** code-forge never runs against, installs into, or names any of Ricardo's own projects;
  the first real, end-to-end use is a neutral example repository shipped inside the package.

### Removed

- `@stryker-mutator/core`, `stryker.config.mjs` and the CI mutation-baseline job — this package
  builds itself with unit and feature tests only (R14); nothing in this repository's own history
  ran mutation testing.
