# Getting started

## Requirements

- Node.js 22 or newer.
- `git`. code-forge works inside a git repository: blocks record a base sha.
- At least one provider CLI for the sessions it starts: `claude`, `codex` or `grok`.
  `code-forge resolve L<n>` shows which CLI each level uses.
- Optional: Solo (engine `solo`), the 1Password CLI `op` (a key source), a key for Jev (System 1: a small fast classifier model from TypeSafe).
  Without a Jev key, deterministic rules plus System 2 answer instead.

## Install

Install the CLI once, globally:

```bash
npm install -g @codedology/code-forge
code-forge version
```

The scoped name `@codedology/code-forge` is used **only in this install line**. The command it
installs is unscoped: `code-forge`. Every page runs it as `code-forge <verb>`, in any folder. Never
install `code-forge` without the scope: the unscoped npm name `code-forge` is a different,
unrelated package.

Then, in the root of the project you want to work on:

```bash
code-forge init
```

`init` writes `.code-forge.yml` in the current directory, links the skill into the harnesses you
pick, and ends with `doctor --quick`. Running it again changes nothing it already wrote.

**Where the skill goes.** By default `init` links the skill with a symlink, at **project** scope
inside a git repository (for Claude Code: `.claude/skills/code-forge` in the repo) and at
**global** scope (under your home folder) outside one. With a global install, pass `-g` inside a
repository too: the link then points at the global package and nothing lands in the repo. `-p`
forces project scope; `--copy` copies instead of linking.

### What `init` asks

Every question has a default: press Enter to keep it. Each question shows a one-line hint under it.

| # | Question | Default / choices | Flag to answer it |
|---|---|---|---|
| 1 | Install the recommended tools, or use only the current harness? | `current harness` · `recommended tools` (Solo, Codex CLI, Grok CLI, Gemini CLI, 1Password CLI; each installed only after its own yes) | `--tools recommended\|current`, `--yes-tool <tool>` |
| 2 | Install the skill into which harnesses? Scope? Method? | detected harnesses · `project` or `global` · `symlink` or `copy` | `--harness a,b`, `-p` / `-g`, `--copy` |
| 3 | Default provider, and keep the level matrix? | `anthropic` · `openai` · `xai`; edit any level as `model[:effort][@provider]` | `--provider P`, `--level Ln=model[:effort][@provider]`, `--refresh-models` |
| 4 | Multimodel review (consensus)? | off. On: pick a second provider; the L3 judge must come from a third provider | `--multimodel on\|off`, `--second-provider P` |
| 5 | Engine | `auto` · `solo` · `harness` (`subprocess` is never offered; write it in the config yourself) | `--engine auto\|solo\|harness`, `--solo-project N` |
| 6 | Gates and proof settings (after the summary below) | **Use these** (the default: Enter keeps it; keep the detected values, blanks stay blank) · **Customize now** (asks gates `test`, `lint`, `types`, `format`, then high-risk paths, proof isolation, export directories to link and untracked files to copy, each pre-filled; Enter keeps it) · **Leave for later** (keeps them and prints the keys to edit) | `--gate name=cmd`, `--proof key=value` (a value set by a flag is not asked) |
| 7 | Where is the Jev key? (asked only when none is found) | 1Password · environment variable · paste now (hidden, goes to the key store) · skip. For 1Password, paste only the item ID (or the item link, or an `op://vault/item/field` reference): code-forge finds the vault and the key field and saves a full reference. If 1Password is locked or not found, you can try again, type a full reference, pick another source or skip (see [Keys](#keys)) | `--jev-ref <item-id\|link\|op://…>`, `--jev-env NAME`, `--no-jev` |

At the end `init` runs `doctor --quick`; `--skip-doctor` skips it.

### What `init` takes from the project

`init` reads the gates and proof settings from the project and prints them before it writes
anything; it asks about them only if you pick **Customize now**:

| Setting | Where it comes from | When nothing is found | Flag to set it |
|---|---|---|---|
| `gates.test`, `gates.lint`, `gates.types`, `gates.format` | detected from `package.json`, `composer.json`, `Cargo.toml`, `pyproject.toml`, `go.mod` | blank (`null`) | `--gate name=cmd` |
| `proof.tiers.high.paths` | never detected: the project does not say which paths are high-risk | blank (`[]`): no file is high tier because of its path (risk and security still can make it high) | `--proof high=a,b` |
| `proof.isolation` | `export` | — | `--proof isolation=export\|lock` |
| `proof.export.link_dirs` | each dependency folder whose manifest exists, installed or not: `composer.json` or `go.mod` ⇒ `vendor`; `package.json` ⇒ `node_modules`; `pyproject.toml`, `requirements.txt`, `setup.py` or `Pipfile` ⇒ `.venv`; `Cargo.toml` ⇒ `target` | blank (`[]`) | `--proof link_dirs=a,b` |
| `proof.export.copy_untracked` | the stack's env files that exist: Laravel `.env`, `.env.testing`; Node `.env`, `.env.test`; other PHP and Python `.env`. A PHP project (`composer.json`) counts as Laravel when it has an `artisan` file in its root, whatever its test runner | blank (`[]`) | `--proof copy_untracked=a,b` |

For a Node project with no type checker the summary reads:

```text
Project settings (detected)
  test: npm test (from package.json)
  lint: npm run lint (from package.json)
  types: blank — no type checker found
  format: npx prettier --check . (from package.json)
  high-risk paths: blank — none set (all files light tier by path)
  isolation: export (from default)
  link dirs: node_modules (from package.json)
  copy untracked: .env (found in the project)
Set the blanks later: edit .code-forge.yml (gates.types, proof.tiers.high.paths), then run `code-forge validate`.
```

After the summary `init` asks question 6 once. With `--no-interaction` at a terminal nothing is
asked and the summary is still printed. In agent mode or without a terminal no text summary is
printed: the same facts appear only as `settings` and `blank` in the one JSON line. A re-run keeps
every value you set by hand; a gate that was blank is filled when the project now shows one.

For scripts and CI, take every default:

```bash
code-forge init --no-interaction --no-jev
```

When an agent runs `init` (it detects `CLAUDECODE`, `AI_AGENT` and similar), or stdin is not a
terminal, `init` prints exactly one JSON line: `{ok, wrote, harnesses, engine_stop, doctor, settings, blank, …}`.
`settings` holds each gate and proof value with its source; `blank` lists the keys left blank.
Without `--no-jev`, `--jev-ref` or `--jev-env` and without a terminal, it refuses with exit 2
instead of hanging. The full log goes to `~/.code-forge/logs/init-<ts>.log`.

If your harness has no subagent tool and there is no Solo, `init` prints the stop text now, so you
do not discover it at the first run.

## Teams and CI: pin per project

Prefer a per-project install when every contributor and every CI image must use the same pinned
version, or when you cannot install global npm packages (for example in a CI image):

```bash
npm install --save-dev @codedology/code-forge
npx @codedology/code-forge init
npx code-forge validate
```

The project needs a `package.json`. After the local install, `npx` finds the `code-forge` binary
in `node_modules/.bin`, so every command on these pages works with `npx` in front of it. Run
`npx code-forge …` only **after** that install: without it, `npx` would download the unrelated
unscoped package. The `init` line keeps the scope for the same reason. The version
is pinned in `package.json` and the lockfile.

## Check the setup

```bash
code-forge validate                 # .code-forge.yml loads and validates
code-forge doctor --quick           # config, harness links, key resolution
code-forge doctor                   # full: PATH, Solo, gates, a live Jev call, provider CLI probes,
                                    # isolation, worker start/stop, signer
```

Read these rows of the full `doctor`:

| Row | What it tells you |
|---|---|
| `solo` | whether Solo is present; with `engine: auto` that decides between `solo` and `harness` |
| provider probes | each CLI exists, its flags work, one tiny call per role succeeds, closed-book sessions see no project file |
| `ping=skipped(402)` | a provider with no balance. A warning, not a failure |
| `signer: same-user boundary only` | always printed. Signed rows are not a sandbox |

`doctor --json` prints the same rows as JSON. `doctor --cwd <dir>` checks another project.

## First commands

All of these are safe to run in any project that has a `.code-forge.yml`:

| Command | What it shows |
|---|---|
| `code-forge --help` | every verb, one per line |
| `code-forge version` | the installed version |
| `code-forge resolve L1` | `{provider, model, effort, fallback, cli}` for a level |
| `code-forge models` | the model catalog per provider |
| `code-forge list` | where the skill is installed, and whether each link still resolves |
| `code-forge keys list` | `NAME / SOURCE / BACKEND / EXPIRES` for each key. Never a value |
| `code-forge keys test jev` | resolves the Jev key and tests it |
| `code-forge gates detect --cwd .` | the gate commands detected for this stack |

## Keys

| Command | Does |
|---|---|
| `code-forge keys set <name> [--op <item-id\|link\|ref>]` | stores a key in the OS keychain from a hidden prompt, or reads it once from 1Password and caches it for 8 hours. The value never goes into argv |
| `code-forge keys test <name> [--ref <ref>]` | resolves and checks one key |
| `code-forge keys remove <name>` | deletes a stored key |

Lookup order: environment → OS keychain → 1Password (cached for 8 hours) → ask at setup.
`.code-forge.yml` only ever holds the reference.

**1Password: names or IDs.** You can paste only the item ID (`op item list` prints it in the
first column) or the item's link. code-forge asks `op`
for the vault and the key field (`credential` on an API Credential item) and saves
`op://<vault-id>/<item-id>/<field-id>`. IDs are safer than names: renaming the item does not break
them. A full `op://vault/item/field` reference still works as is. The same goes for
`keys set <name> --op` and `keys test <name> --ref`.

## Keeping it up to date

To move to a new version:

```bash
npm install -g @codedology/code-forge@latest
code-forge version
code-forge upgrade
```

`upgrade` re-copies skill installs made with `--copy`; symlinked installs already point at the
new package. With nvm, the global install belongs to one Node version: install again after you
switch. Your `.code-forge.yml` files and `~/.code-forge` are not changed. To see the settings
summary of a newer `init`, run `code-forge init` again in the project; it keeps the values you set
by hand. What changed in each version is in [CHANGELOG.md](../CHANGELOG.md).

| Command | Does |
|---|---|
| `code-forge upgrade [--source <path>]` | re-points every recorded skill install at this package's `skill/` folder (or at `--source`): symlinks are re-made, copies re-copied |
| `code-forge models --refresh --from-cli-caches` | refreshes the model catalog from the Codex and Grok CLI caches on this machine |
| `code-forge remove [<harness>] [--scope project\|global]` | removes the skill links that `init` recorded |

## Review only

To review code you already wrote, without planning or coding through code-forge, run
`code-forge review` in the repository. It reviews your branch against the default branch (or your
uncommitted work, when you are on the default branch), prints a verdict and a fix list per file, and
exits 0 only when every file is approved. See [review-only.md](review-only.md).

## Use it

In a harness where the skill is installed, write a brief and ask for a job:

```text
/code-forge plan plans/my-feature.md
/code-forge harden plans/my-feature.plan.md
/code-forge code plans/my-feature.plan.md
```

or `/code-forge full plans/my-feature.md` for all three with one checkpoint after harden.

Next: the [first end-to-end test](tutorial-first-test.md).
