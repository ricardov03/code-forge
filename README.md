# @codedology/code-forge

A CLI (`code-forge`) plus one Agent Skill that runs the **plan → harden → code → review** pipeline
for cross-model, parallel, evidence-gated feature delivery — on Claude Code, Codex, Grok, and Solo.

> **Status:** built block by block from a maintainer-only design plan. The CLI, decision layer,
> review engine, worker, proof policy and installer are landed; the Agent Skill under `skill/` and
> the shipped example repo under `examples/` land in later blocks (see
> `docs/reference/blocks.json` for what has landed and what is still pending).

## What ships

- A CLI (`code-forge`), installed globally with `npm install -g @codedology/code-forge`; `code-forge
  init` then links the skill into every detected harness (Claude Code, Codex, Grok, …).
- One Agent Skill (`skill/SKILL.md` + `references/`) that is prose only — every fact (model ids,
  efforts, commands, thresholds, paths) lives in config or in the CLI, never in the skill text.
- A JSON Schema for `.code-forge.yml` (`schema/code-forge.schema.json`), documented in
  `docs/reference/config.md` (generated — see below).

See the skill's `skill/references/` for the full architecture, decision layer, review engine, proof
policy and security model; `docs/reference/blocks.json` lists the build blocks.

## Install

```bash
npm install -g @codedology/code-forge
code-forge init
```

The scoped name is used only to install; the command is `code-forge`. The unscoped npm package
`code-forge` is a different, unrelated tool.

The wizard asks a few short questions (tools, harnesses, provider matrix, multimodel, keys,
engine), every one with a default on Enter. Gates and proof settings come from the project:
`init` prints what it found, marks what it left blank, and lets you use, customize or leave them
for later. Non-interactively:

```bash
code-forge init --no-interaction --no-jev
```

`--no-jev` skips the System-1 key for a first try; drop it and pass `--jev-ref <1Password item ID>`
(or the item link, or `op://vault/item/field`; code-forge finds the vault and the key field) or
`--jev-env MY_JEV_KEY` once you have one. A second run of `init` changes nothing it already wrote.

To upgrade: `npm install -g @codedology/code-forge@latest`, then `code-forge upgrade` (re-copies
skill installs made with `--copy`). See [docs/getting-started.md](docs/getting-started.md#keeping-it-up-to-date).

## Quickstart

Once `init` has written `.code-forge.yml`, these all run for real:

```bash
code-forge validate                 # loads and validates .code-forge.yml
code-forge resolve L1                # {provider, model, effort, fallback, cli} for a level
code-forge doctor --quick            # config + links + key resolution only
code-forge list                      # what is installed where, and whether it still resolves
code-forge keys list                 # NAME / SOURCE / BACKEND / EXPIRES — never a value
code-forge models                    # the model catalog per provider
code-forge --help                    # every verb, one per line
code-forge version
```

## Review only

To review code you already wrote — no plan, no coder, no proof — run this in the repository:

```bash
code-forge review                    # verdict + fix list per changed file; exit 0 only if all approved
```

It reviews your branch against the merge base with the default branch (on the default branch: your
uncommitted work), with the same fresh, closed-book reviewer sessions a block gets. Narrow it with
`--files <path…>`, state the change with `--intent "<text>"`, pick the base with `--base <ref>`, or
get one JSON document with `--json`. Exit codes: `0` all approved or nothing to review, `1` any
finding, stop or unavailable review, `2` usage. See [docs/review-only.md](docs/review-only.md).

## The verbs

Verbs are discovered from `src/cli/*.mjs` — adding one never touches the router
(`bin/code-forge.mjs`), so this list is exactly `code-forge --help`'s output plus each verb's own
usage line.

| Verb | Usage |
|---|---|
| `init` | `code-forge init [--no-interaction] [--tools recommended\|current] [--yes-tool <tool>]… [--harness a,b] [-g\|-p] [--copy] [--provider P] [--level Ln=model[:effort][@provider]]… [--refresh-models] [--multimodel on\|off] [--second-provider P] [--jev-ref <item-id\|link\|op://…> \| --jev-env NAME \| --no-jev] [--engine auto\|solo\|harness] [--solo-project N] [--gate name=cmd]… [--proof isolation=export\|lock \| high=a,b \| link_dirs=a,b \| copy_untracked=a,b]… [--skip-doctor]` |
| `doctor` | `code-forge doctor [--quick] [--json] [--cwd <dir>]` |
| `tools` | `code-forge tools [--json]` · `code-forge tools install [<id>…] [--yes] [--dry-run]` — see and install the recommended tools (claude, codex, gemini, grok, op, solo) |
| `validate` | `code-forge validate [--file <path>]` |
| `resolve` | `code-forge resolve <L0\|L1\|L2\|L3>` |
| `run` | `code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>]` · `run start --reattach --run <id>` · `run status --run <id>` · `run reload --run <id>` · `run end --run <id>` |
| `block` | `code-forge block open <id> --run <r> --level L<n> --owned <paths…> --acceptance <file>` · `block attempt\|rebase\|claim\|close\|stop\|waive …` (§4.9; `waive` is human-only) |
| `worker` | `code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]` — started detached by `run start`, never by a coder |
| `review` | `code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> \| --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]` — review only: no coder, no proof, never closes a block |
| `review-file` | `code-forge review-file <path> --block <id> [--run <id>]` (enqueue) · `review-file --wait <ticket> [--max <seconds>s]` (poll) |
| `spawn` | `code-forge spawn --level L<n> --role <role> --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <s>] [--background]` |
| `s2` | `code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <s>]` |
| `author` | `code-forge author --job plan\|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <s>]` |
| `facts` | `code-forge facts --brief <file> [--sources <path>…] [--out <file>] [--run <id>] [--timeout <s>]` |
| `plan` | `code-forge plan check <plan-file> [--facts <sheet>]` |
| `proof` | `code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]` · `proof export\|lock\|unlock\|restore <block> --run <r>` |
| `gates` | `code-forge gates detect\|run\|secret-scan\|safe-edit\|scope\|acceptance\|transcript-grep --cwd <dir> …` |
| `jev` | `code-forge jev ask <question-id> --state <file.json> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>]` |
| `keys` | `code-forge keys list \| set <name> [--op <ref>] \| test <name> [--ref <ref>] \| remove <name>` |
| `ledger` | `code-forge ledger calibration\|outcome\|tail --slug <slug> …` |
| `report` | `code-forge report --slug <slug> [--json] [--export <dir>]` |
| `models` | `code-forge models [--refresh --from-cli-caches]` |
| `list` | `code-forge list` |
| `remove` | `code-forge remove [<harness>] [--scope project\|global]` |
| `upgrade` | `code-forge upgrade [--source <path>]` |
| `logs` | `code-forge logs [--last N] [--json]` · `logs summary [--days N] [--json]` · `logs clear [--yes]` · `logs path` — the local error log (`~/.code-forge/logs/errors.jsonl`) |
| `logs report` | `code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--dry-run] [--yes]` — share selected errors as a public GitHub issue; you see the full text first and say yes once |
| `help` / `version` | `code-forge --help` (or no verb) · `code-forge --version` |

`run`, `block`, `worker`, `spawn`, `s2`, `author`, `facts` and `review-file` are what the Agent
Skill's loop calls during a real block; you will not usually type them by hand, but they are plain
CLI verbs like any other and every one of them is exercised by this package's own test suite.

## Security model

- **`signer: same-user boundary only`.** Every review/gate-relevant ledger row is HMAC-signed with
  a per-project key, so tampering by a different user or a rewritten history is detectable.
  `doctor` always prints this line — the signature is not a defense against an attacker who
  already runs code as you, and no part of code-forge claims otherwise.
- **Keys never reach a coder process.** Provider and Jev keys resolve only inside the worker or the
  orchestrator's own CLI calls (`env:<NAME>` → OS keychain → 1Password, cached briefly → ask at
  setup only). `keys list`/`keys test` never print a value; every stdout/stderr path is redacted.
- **A forbidden-command list** stops a coder from starting its own worker, waiving a review, or
  running other orchestrator-only actions — rendered into each provider's own deny-list mechanism
  (`--disallowedTools` for Claude, an execpolicy rules file plus prose for Codex).
- **No mutation testing.** Neither this package's own tests nor the product it ships run mutation
  testing (Ricardo's ruling R14/Q16, 2026-09-25). Proof is plain unit and feature tests with exact
  assertions, plus a red→green runner that proves a new test fails before the fix and passes after.

## Configuration reference

`schema/code-forge.schema.json` is the source of truth for `.code-forge.yml`.
`docs/reference/config.md` is generated from it:

```bash
node scripts/gen-config-doc.mjs          # regenerate docs/reference/config.md
node scripts/gen-config-doc.mjs --check  # exit 0 if it's already up to date, 1 otherwise
```

## Development

```bash
npm install
npm test              # node --import ./test/helpers/isolate.mjs --test 'test/**/*.test.mjs'
npm run typecheck     # tsc --checkJs --noEmit
```

Node >= 22 required (ruling R7). No build step — the package ships plain ESM `.mjs`, run directly
by Node. `docs/reference/blocks.json` maps each build block to the files it owns and what it
depends on, kept in sync with the maintainer's design plan.

Releasing: record changes with `npm run changelog -- <type> "<text>"`, then `npm run release -- <patch|minor|major|x.y.z> [--dry-run]` bumps, commits and tags (it never pushes or publishes). See [docs/releasing.md](docs/releasing.md).
