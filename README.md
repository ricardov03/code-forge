# @codedology/code-forge

A CLI (`code-forge`) plus one Agent Skill that runs the **plan → harden → code → review** pipeline
for cross-model, parallel, evidence-gated feature delivery — on Claude Code, Codex, Grok, and Solo.

> **Status:** published on npm. The CLI, the decision layer, the review engine, the worker, the
> proof policy, the installer, the Agent Skill under `skill/` and the example repo under
> `examples/` have all landed (`docs/reference/blocks.json` lists every build block). What changed
> in each version is in [CHANGELOG.md](CHANGELOG.md).

## What's new

- **Parallel review.** The worker reviews `review.parallel_tickets` files at once (default 3), with
  a session limit per provider (`review.provider_concurrency`), backoff on rate limits, and
  `budget.usd` still holding. `code-forge review` now waits on all files under one `--max`
  deadline and prints each result as it finishes. `code-forge doctor` shows the numbers.

## What's new in 0.4

- **Cost control.** Set `budget.usd` per run: one warning at 80%, no new session at 100%. Every
  session row carries an estimated `usd`; `report` and `run status` show the spend, and
  `ledger add coder --usd` records spend code-forge did not see.
- **Cheaper, faster fixes.** One review round that leaves 2 or more warnings climbs the coder one
  level (`escalation.*`). Docs and contract blocks code at L1 or higher and skip multimodel review;
  a file with risk 0–1 gets one reviewer (`review.single_reviewer_max_risk`).
- **Change the config mid-run.** `code-forge run reload --run <id>` re-reads `.code-forge.yml`
  without stopping open blocks.
- **Stricter plans.** `plan check` refuses a block whose level is not the lane recorded with
  `jev ask lane --block <id>` (or `--rules` without Jev), and prints the lane of every block.
  `block close --report <file>` fails a coder report that does not name a review ticket per file.
- **Codex is refused for closed-book roles** (reviewer, judge, System 2, plan author): it always
  has a shell. See [Security model](#security-model).
- **Steadier reviews.** A timed-out review session is retried once; `review.session_timeout_s`
  defaults to 300; a live worker with a fresh heartbeat is never reported down.
- **Better error reports.** `logs report --with-doctor` adds a cleaned setup check; errors list the
  last 5 command names; recovered problems are logged as warnings; an error that a newer version
  fixes tells you to upgrade instead of filing it.
- **Earlier checks.** `validate` refuses an effort the level's provider cannot take (for example
  `xhigh` on an openai level), so nothing fails when the session starts.

Fixed in 0.3.1 and 0.3.2: Claude reviewer sessions no longer fail at once on the `--json-schema`
flag; Grok answers are read from Grok's own answer envelope; Jev `score` questions (such as `risk`)
no longer fall back to rules; and a clean review of a tiny diff (20 added lines or fewer) needs only
40 output tokens, so it is no longer refused as `too_short`.

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
| `run` | `code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>]` · `run start --reattach --run <id> [--worker-pid <pid>]` · `run status --run <id>` (with spent and budget USD) · `run reload --run <id>` (re-read `.code-forge.yml` mid-run) · `run end --run <id>` |
| `block` | `code-forge block open <id> --run <r> --level L<n> --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code\|docs\|contract]` · `block attempt\|rebase <id> --run <r>` · `block close <id> --run <r> [--transcript <file>] [--report <file>] [--no-require-reviews]` · `block claim <id> <path> --run <r>` · `block stop <id> --run <r> --reason <text>` · `block waive <id> <finding-id> --run <r> --file <path> --reason <text>` (human-only) |
| `worker` | `code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]` — started detached by `run start`, never by a coder |
| `review` | `code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> \| --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]` — review only: no coder, no proof, never closes a block |
| `review-file` | `code-forge review-file <path> --block <id> [--run <id>]` (enqueue) · `review-file --wait <ticket> [--max <seconds>s]` (poll, default 90s; still running at `--max` prints `status: pending, reason: wait_timeout`) |
| `spawn` | `code-forge spawn --level L0\|L1\|L2\|L3 --role <role> --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <seconds>] [--background]` |
| `s2` | `code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]` |
| `author` | `code-forge author --job plan\|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]` |
| `facts` | `code-forge facts --brief <file> [--sources <path>…] [--out <file>] [--run <id>] [--timeout <seconds>]` |
| `plan` | `code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]` — every block's level must be the lane recorded by `jev ask lane --block <id>`; prints `lanes: B1 L1 (rules) · …` |
| `proof` | `code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]` · `proof export <block> --run <r> [--remove]` · `proof lock\|unlock\|restore <block> --run <r>` · `proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert\|assertion-deletion]` |
| `gates` | `code-forge gates detect\|run\|secret-scan\|safe-edit\|scope\|acceptance\|transcript-grep --cwd <dir> …` |
| `jev` | `code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]` — `--rules` answers `lane`, `risk` or `security_sensitive` without Jev; writes to the project's ledger slug |
| `keys` | `code-forge keys list \| set <name> [--op <item-id\|link\|op://ref>] \| test <name> [--ref <ref>] \| remove <name>` |
| `ledger` | `code-forge ledger tail --slug <slug> [--n\|-n <count>]` · `ledger calibration --slug <slug> [--question <id>]` · `ledger outcome --slug <slug> --pr <n> --ci red\|green\|reverted` · `ledger outcome --slug <slug> --scan-git [--cwd <dir>] [--days <n>]` · `ledger add coder --run <r> --block <b> --usd <n> [--note "..."]` |
| `report` | `code-forge report --slug <slug> [--json] [--export <dir>]` — 13 sections, then the spend per run (open blocks included) and the total |
| `models` | `code-forge models [--refresh --from-cli-caches]` |
| `list` | `code-forge list` |
| `remove` | `code-forge remove [<harness>] [--scope project\|global]` |
| `upgrade` | `code-forge upgrade [--source <path>]` |
| `logs` | `code-forge logs [--last N] [--json]` · `logs summary [--days N] [--json]` · `logs clear [--yes]` · `logs path` — the local error log (`~/.code-forge/logs/errors.jsonl`): errors, warnings, and `[fixed in X.Y.Z]` marks |
| `logs report` | `code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]` — share selected errors as a public GitHub issue; you see the full text first and say yes once. An error a newer version fixes is not filed unless `--force` |
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
- **Closed-book reviews.** Reviewers, judges, System 2 and the plan author see the packet and
  nothing else: Claude runs them with no tools, Grok with every tool denied. Codex (provider
  `openai`) has no mode without a shell, so code-forge refuses it for those roles: `validate`
  warns, `resolve` marks the level `closed_book: "refused"`, `doctor` fails its isolation row, and
  the session is never started. Codex coders are unchanged. `review.allow_open_book_codex: true`
  accepts the risk and keeps Codex in those roles, with a warning on every surface.
- **The error log stays on your machine.** A failed verb is logged to
  `~/.code-forge/logs/errors.jsonl` with flag names but never flag values, and with home folder,
  project, emails, `op://` references and 1Password item IDs replaced. `logs report` cleans the
  text twice (built-in rules, then an AI check), stops on anything that looks like a key, shows
  you the full issue and sends nothing without your yes. `CODE_FORGE_NO_ERROR_LOG=1` turns the log,
  the warnings and the command list off. Every field is listed in [docs/privacy.md](docs/privacy.md).
- **No mutation testing.** Neither this package's own tests nor the product it ships run mutation
  testing (Ricardo's ruling R14/Q16, 2026-09-25). Proof is plain unit and feature tests with exact
  assertions, plus a red→green runner that proves a new test fails before the fix and passes after.

## Configuration reference

`schema/code-forge.schema.json` is the source of truth for `.code-forge.yml`.
[docs/reference/config.md](docs/reference/config.md) lists every key with its type and default.
Keys added for 0.4:

| Key | Default | What it does |
|---|---|---|
| `budget.usd` | none | per-run spend stop in USD: one warning at 80%, no new session at 100% |
| `escalation.after_rounds_with_warnings` | `1` | rounds with many warnings at one level before the coder climbs (`0` turns it off) |
| `escalation.warning_threshold` | `2` | how many open warnings make a round "heavy" (any critical counts too) |
| `levels.coder_floor_docs` | `L1` | the lowest level that codes a docs or contract block |
| `review.single_reviewer_max_risk` | `1` | at or below this risk a file gets one reviewer and no judge |
| `review.multimodel_for_docs` | `false` | let docs and contract blocks use multimodel review too |
| `review.allow_open_book_codex` | `false` | let Codex run reviewer, judge, System 2 and plan author anyway |
| `review.session_timeout_s` | `300` | a review session that runs longer is killed and retried once |
| `review.parallel_tickets` | `3` | review tickets the worker runs at once (1 to 16); `1` reviews one file at a time |
| `review.provider_concurrency` | anthropic `4`, openai `2`, xai `2` | review sessions at once per provider |

`code-forge block open … --kind code|docs|contract` sets a block's kind when its file endings do
not say it. The reference page is generated:

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

Releasing: record changes with `npm run changelog -- <type> "<text>"` and fixed error reports with
`npm run known-fix -- add <fp> <version> "<summary>"`, then `npm run release -- <patch|minor|major|x.y.z> [--dry-run]`
bumps, commits and tags (it never pushes or publishes). Publish with `npm publish` from your
machine, then push the tag: CI only creates the GitHub release. See [docs/releasing.md](docs/releasing.md).
