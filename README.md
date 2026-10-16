# @codedology/code-forge

A CLI and Agent Skill for cross-model, parallel, evidence-gated feature delivery.

Latest: 0.14.0 - see [CHANGELOG.md](CHANGELOG.md)

## The problem

An agent can write code quickly, but you still have to decide what to build and how to check it.
Long tasks lose context, parallel edits collide, and a green test can miss the intended behavior.
code-forge splits work into owned blocks, gives each change a fresh review, and records proof.
You approve the plan and keep control of budgets, permissions, and landing the result.

## Install

Requires Node.js >= 22 and git. Install the CLI with `npm install -g @codedology/code-forge`.
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
and warns with less than one hour left.

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
`--json`. Exit 0 means all approved or nothing to review; 1 means a finding, stop, or unavailable
review; 2 means usage. See [review-only details](docs/review-only.md).

## How it works

```text
plan -> code -> review -> proof -> land
  ^       |       |
  |       +<--fix-+
  +-- human approval after hardening
```

The skill checks and hardens the plan before your approval. Coders work in blocks with explicit
owned files and acceptance clauses; independent blocks can run in parallel. A worker runs fresh
reviewer sessions outside the coder's sandbox. Fix rounds are bounded; unresolved work stops.
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
| `code-forge block open\|close\|stop` | Track owned work and enforce its acceptance gates |
| `code-forge review-file` | Enqueue a file review or wait for its ticket |
| `code-forge proof tier\|red-green` | Choose proof strength and prove a test fails before the fix |
| `code-forge gates detect\|run` | Discover and run project checks |
| `code-forge keys list\|set\|test\|remove` | Manage key references without printing secrets |
| `code-forge report` | Inspect recorded outcomes and costs |
| `code-forge logs` | Inspect local errors; `logs report` previews a public issue |
| `code-forge upgrade` | Refresh installed skill copies after upgrading the package |
| `code-forge --help` | List every verb; each verb prints its own usage on bad arguments |

The skill normally calls `run`, `block`, `worker`, `spawn`, `s2`, `author`, `facts`, and
`review-file` for you. See the [workflow](docs/how-it-works.md) for these lower-level operations.
To upgrade, run `npm install -g @codedology/code-forge@latest`, then `code-forge upgrade` for a version summary and release notes; add `--plain` for just the install lines.

### Configuration reference

[schema/code-forge.schema.json](schema/code-forge.schema.json) is the source of truth.
The reference lists every key, type, and default, including budget, escalation, coder floors,
review risk, multimodel, concurrency, and timeout settings.
`review.routes` sends big files to another model with its own timeout and second level.
`block open --kind code|docs|contract` overrides the kind inferred from filenames.
Regenerate the reference with `node scripts/gen-config-doc.mjs`; add `--check` to check it.

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

The package ships plain ESM `.mjs` files with no build step. In a checkout, run `npm install`,
`npm test`, and `npm run typecheck`. Check the generated config reference before releasing.
Record changes with `npm run changelog -- <type> "<text>"`; record fixed error reports with
`npm run known-fix -- add <fp> <version> "<summary>"`. `npm run release -- <bump> [--dry-run]`
bumps versions, releases the CHANGELOG, updates the optional Latest line, checks, commits, and
tags. Publish with `npm publish`, then push the tag; CI creates the GitHub release.
See [releasing](docs/releasing.md).
