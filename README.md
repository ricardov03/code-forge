# @codedology/code-forge

A CLI and Agent Skill for cross-model, parallel, evidence-gated feature delivery.

Latest: 0.27.0 - see [CHANGELOG.md](CHANGELOG.md)

## The problem

An agent can write code quickly, but you still have to decide what to build and how to check it.
Long tasks lose context, parallel edits collide, and a green test can miss the intended behavior.
code-forge splits work into owned blocks, gives each change a fresh review, and records proof.
You approve the plan and keep control of budgets, permissions, and landing the result.

Projects may opt in to automatic clearance of identical rebased binaries with a frozen approved
source list and the `rebase:accept-identical` grant. See [approved binary sources](docs/autopilot.md#scopes-and-the-fixed-deny-list).

## Install

Requires git, and npm to install (any Node that runs npm; code-forge itself never runs Node). Install the CLI with `npm install -g @codedology/code-forge`.
The Go binary for your system comes in its own small package, `@codedology/code-forge-<platform>-<arch>` (darwin-arm64, darwin-x64, linux-x64 or linux-arm64). npm installs only the one that matches your machine, as an optional dependency, so the main package stays under 5 MB.
npm runs no install script, so then run `code-forge setup`: it checks the Go binary for your system and prints `OK` or the one command that fixes it.
The Go binary is required: with no usable binary, `code-forge` stops with exit 69 and names the fix. To check or repair the binary on its own, run `sh "$(npm root -g)/@codedology/code-forge/scripts/install.sh"`; if that fails, run `npm i -g @codedology/code-forge` again.
macOS: worktrees default to a `.noindex` folder so Spotlight does not index them.
The scoped npm name is only for installation; the command is `code-forge`.
The unscoped npm package `code-forge` is an unrelated tool.

Inside your repository, run the setup wizard:

```bash
code-forge init
```

Press Enter for defaults for tools, harnesses, providers, multimodel review, keys, and engine.
The wizard detects gates and proof settings, marks blanks, and lets you use, customize, or defer
them. It writes `.code-forge.yml` and links the skill into detected harnesses. Re-running it
keeps what it already wrote. For a first try without a System-1 (Jev) key:

```bash
code-forge init --no-interaction --no-jev
```

When ready, use `--jev-ref <1Password item ID|link|op://vault/item/field>` or
`--jev-env MY_JEV_KEY` instead of `--no-jev`. See [setup details](docs/getting-started.md).

### What ships

- The `code-forge` CLI and one prose-only [Agent Skill](skill/SKILL.md), with supporting
  [references](skill/references/). Model IDs, efforts, commands, thresholds, and paths come from
  config or the CLI.
- A JSON Schema for `.code-forge.yml` and its generated [configuration reference](docs/reference/config.md).
- A generated [command reference](docs/reference/commands.md): per command its purpose, when to use it,
  flags, examples with real output, exit codes, the `--json` shape and related commands; agents read the
  same data in [commands.json](docs/reference/commands.json).
- An [example repository](examples/) and a [build-block map](docs/reference/blocks.json).

## 5-minute quickstart

After setup, check the configuration, selected model, and installed links:

```bash
code-forge validate
code-forge resolve L1
code-forge doctor --quick
code-forge list
code-forge keys list
code-forge models
code-forge --help
code-forge version
```

`resolve` prints the provider, model, effort, fallback, and CLI for a level. `doctor --quick`
checks config, links, and key resolution; `keys list` prints sources and expiry, never values,
and warns with less than one hour left. `keys set <name>` asks for the value with the input
hidden, so it needs a terminal; from a script, store a 1Password reference with
`keys set <name> --op <item-id|link|op://ref>`.

Write a brief describing the change and how you will know it works. In your agent, invoke
`/code-forge full path/to/brief.md`: it plans, hardens, asks you to approve the plan, then codes.
Use `/code-forge plan|harden|code <file>` for one phase at a time.
Set a per-run `budget.usd` before starting paid sessions: one warning at 80%, no new session at 100%.
The [first-test tutorial](docs/tutorial-first-test.md) walks through a complete delivery;
[brief mode](docs/brief-mode.md) covers one small task without a plan.

### Review only

For code you already wrote, run this in the repository:

```bash
code-forge review
```

It reviews the branch against the merge base with the default branch, or uncommitted work on
the default branch, using fresh closed-book sessions. It runs no planner, coder, or proof.
Narrow with `--files <paths…>`, choose `--base <ref>`, explain with `--intent "<text>"`, or use
`--json`. Exit 0 means every file approved by this review, or nothing to review; 1 means a
finding, stop, unavailable review, or a file not reviewed (no change against the base); 2 means
usage. In a git worktree it reviews that worktree's files. See [review-only details](docs/review-only.md).

## How it works

```text
plan -> code -> review -> proof -> land
  ^       |       |
  |       +<--fix-+
  +-- human approval after hardening
```

The skill checks and hardens the plan before your approval. Coders work in blocks with explicit
owned files and acceptance clauses; independent blocks can run in parallel. A worker runs fresh
reviewer sessions outside the coder's sandbox. A file the worker cannot review fails only itself
(`unsupported`, with the reason in `block ready`); the worker keeps serving, and every worker exit
leaves a signed `worker.exit` row and `.code-forge/logs/worker-<pid>.log`. `run status` says whether a
pinned worker is alive (`worker_live`), the same way `block ready` checks it. Fix rounds are bounded;
unresolved work stops.
Under autopilot, a problem found during the work (a red gate, a failed land, a hand-over, a coder's
leftover, a flaky test) is recorded with `code-forge autopilot found`, by itself for the first four.
It goes to the backlog and the workspace's `.code-forge/next-release.md` plan; only a customer-breaking finding becomes a queued block
of the same run. Findings about scope,
security, deletes or publishing wait for you ([autopilot](docs/autopilot.md)). Each land, gate,
coder session and CI wait gets a speed budget after five samples by default (its measured median
plus a margin), or from fixed minutes in `autopilot.step_budgets`. A step over it prints a WARN
and is recorded as found work too.
Tests and red→green proof establish that a change meets its acceptance clauses before closing.
You control the final merge and publish permissions; coders cannot push or open draft PRs by
default. See [the workflow](docs/how-it-works.md) and [concepts](docs/concepts.md).

## Key commands

| Command | Use it to |
|---|---|
| `code-forge init` | Configure the project and install the skill |
| `code-forge doctor --quick` | Check config, links, and keys |
| `code-forge review` | Review existing changes without a coding run |
| `code-forge run start\|status\|reload\|end` | Manage a run and inspect its spend |
| `code-forge block open\|close\|stop` | Track owned work and enforce its acceptance gates; `close --landed <sha>` reconciles a block commit (including a merge) already integrated into `main` or a separate combine worktree |
| `code-forge review-file` | Enqueue a file review or wait for its ticket |
| `code-forge proof tier\|red-green` | Choose proof strength and prove a test fails before the fix |
| `code-forge gates detect\|run` | Discover and run project checks |
| `code-forge keys list\|set\|test\|remove` | Manage key references without printing secrets |
| `code-forge report` | Inspect recorded outcomes and costs |
| `code-forge outcome seal\|check` | Seal a specialist result and check its files before a caller uses it |
| `code-forge pipeline status\|next` | See specialist step states and the next ready step |
| `code-forge logs` | Inspect local errors; `logs report` previews a public issue |
| `code-forge upgrade` | Refresh installed skill copies after upgrading the package, then fix the project config |
| `code-forge release check [--json]` | Check the project is ready to release (clean tree, HEAD pushed, green CI, CHANGELOG shape, `npm ls`), suggest the bump and print the release steps; says `RELEASE NOW` at `release.max_blocks` blocks (default 8) since the last tag |
| `code-forge config fix [--dry-run]` | Rewrite `.code-forge.yml` shapes an older release accepted and this one refuses |
| `code-forge audit commands [--out <file>]` | Maintainers: run every command in a sandbox and write the audit a major release needs |
| `code-forge --help` | List every verb; each verb prints its own usage on bad arguments |

The skill normally calls `run`, `block`, `worker`, `spawn`, `s2`, `author`, `facts`, and
`review-file` for you. See the [workflow](docs/how-it-works.md) for these lower-level operations.
To upgrade, run `npm install -g @codedology/code-forge@latest`, then `code-forge upgrade` for a version summary and release notes; add `--plain` for just the install lines. Run it in the project folder: it then fixes old shapes in `.code-forge.yml` (as `code-forge config fix` does) and prints what it changed, or `config ok`.

### Configuration reference

[schema/code-forge.schema.json](schema/code-forge.schema.json) is the source of truth.
The reference lists every key, type, and default, including budget, escalation, coder floors,
review risk, multimodel, concurrency, and timeout settings.
`review.routes` sends big files to another model with its own timeout and second level.
See the [routing and fallback examples](docs/how-it-works.md) for a long-context reviewer above
400 lines and fallback lists of `{provider, model}` objects (optional `effort`, never strings).
`block open --kind code|docs|contract` overrides the kind inferred from filenames.
Regenerate the reference with `sh scripts/cf-release.sh config-doc`; add `--check` to check it.
`sh scripts/cf-release.sh regen-docs` regenerates every generated doc and recorded test pin once
(commands reference, config reference, command summaries, doc example pins, init goldens);
`regen-docs --check` lists the stale ones and exits 1.
With `land.regenerate: [sh scripts/cf-release.sh regen-docs]` a batch land runs it once on the combined
tip before the push and commits the result (B959b).

## Supported agents

Use Claude Code, Codex, or Grok as agent harnesses, or Solo as the execution engine.
Gemini, Cursor, and Copilot have detection and skill linking only.
See [agent support and limits](docs/overview.md) for adapters and degraded modes.
Closed-book role restrictions are described below.

## Security model

Review and gate rows are HMAC-signed: `signer: same-user boundary only`. A process running as
you can read the signing key; this does not defend against that process. `doctor` states the limit.
Provider and Jev keys resolve only in the worker or orchestrator: environment → OS keychain →
1Password (cached for 8 hours by default, at most 24) → setup prompt. Keys never reach coders;
key commands print no values and output is redacted. Provider deny lists restrict coder actions.

Reviewers, judges, System 2, and the plan author see only their packet: Claude has no tools and
Grok has every tool denied. Codex always has a shell, so those roles are refused by default;
`review.allow_open_book_codex: true` opts into the risk with warnings.
Errors stay in a local, cleaned log. `logs report` cleans twice, checks for keys, previews the
issue, and sends only with your yes; `CODE_FORGE_NO_ERROR_LOG=1` disables logging.
Proof uses unit/feature tests and red→green, with no mutation testing or hand mutations.
See [security details](skill/references/security.md) and [privacy](docs/privacy.md).

## Links

- [Documentation](docs/): setup, concepts, tutorials, and reference pages.
- [CHANGELOG.md](CHANGELOG.md): all per-version release history.
- Roadmap: [public feature ideas](https://claude.ai/artifact/BmeWyqRxZYsWr7eH5vBtdF)

### Development

The engine is Go, in `native/` (B690b removed the Node code). In a checkout, run
`cd native && go build ./... && go test ./<package>` for the packages you change; CI runs the
Go suite (`cmd/code-forge-native` in parallel shards, `-race` on Go stable, the 1.22.x floor
without it) and checks the generated config and command references. Record changes with
`npm run changelog -- <type> "<text>"`; record fixed error reports by adding an entry to
`native/cmd/code-forge-native/data/known-fixes.json`. `npm run release -- <bump> [--dry-run]`
bumps versions, releases the CHANGELOG, updates the optional Latest line, checks, commits, and
tags. Publish with `npm publish`, then push the tag; CI creates the GitHub release.
The merge driver and release checks flag repeated block IDs in `[Unreleased]`; combine near-duplicate entries into one before releasing.
See [releasing](docs/releasing.md).

<!-- B1011 -->
Evidence passes require the owner's opt-in `human_check.autopilot` section before
`run start`; the entire section is frozen and `run reload` refuses changes.
Defaults: `enabled: false`, `require_proof: true`, `proof_dirs: []`,
`exclude_waits_for: []`, `require_separate_prover: true`. Enabling requires a
non-empty list of proof roots; v1 refuses `require_proof: false`.
Human-check clauses may set `approval: design|device` (only on `kind: human_check`).
Design approvals remain owner-only. See [the opt-in example](examples/autopass.yml).

Owners may opt in before `run start` to evidence-based autopilot passes for device
checks. With `human-pass:evidence`, use `block auto-pass <id> --run <r> --proof
<file> --note <text>`; design approvals stay owner-only. See
[Auto-pass on green evidence](docs/autopilot.md#auto-pass-on-green-evidence).

Autopilot evidence passes stay visible for owner audit. Use `code-forge run human-checks audit --run <r> [--json]` to verify the recorded proof and evidence hashes, and `run human-checks reopen --run <r> --block <id> [--clause C<n>] --note "<reason>"` at your terminal to revoke a pass after stopping the grant.
