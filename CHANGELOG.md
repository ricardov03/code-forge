# Changelog

All notable changes to `@codedology/code-forge` are recorded here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions follow semver ahead of a 1.0.0 that
waits on a second real consumer (plan §10.4, Q17).

## [Unreleased]

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
