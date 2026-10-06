# Getting started

## Requirements

- Node.js 22 or newer.
- `git`. code-forge works inside a git repository: blocks record a base sha.
- At least one provider CLI for the sessions it starts: `claude`, `codex` or `grok`.
  `code-forge resolve L<n>` shows which CLI each level uses. Reviews, System 2 and the plan
  author run at L2 and L3 and need `claude` or `grok`: Codex always has a shell, so code-forge
  refuses it for those closed-book roles (see [Codex and closed-book roles](#codex-and-closed-book-roles)).
  Codex is fine for coders.
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
| 1 | Install the recommended tools, or use only the current harness? | `current harness` · `recommended tools` (Claude Code, Codex CLI, Gemini CLI, Grok CLI, 1Password CLI, Solo; each installed only after its own yes — or later with `code-forge tools install`, see [Install the recommended tools](#install-the-recommended-tools)) | `--tools recommended\|current`, `--yes-tool <tool>` |
| 2 | Install the skill into which harnesses? Scope? Method? | detected harnesses · `project` or `global` · `symlink` or `copy` | `--harness a,b`, `-p` / `-g`, `--copy` |
| 3 | Default provider, and keep the level matrix? | `anthropic` · `openai` · `xai`; edit any level as `model[:effort][@provider]` | `--provider P`, `--level Ln=model[:effort][@provider]`, `--refresh-models` |
| 4 | Multimodel review (consensus)? | off. On: pick a second provider; the L3 judge must come from a third provider. It is used only for files above `review.single_reviewer_max_risk` (default 1), and not for docs blocks | `--multimodel on\|off`, `--second-provider P` |
| 5 | Engine | `auto` · `solo` · `harness` (`subprocess` is never offered; write it in the config yourself) | `--engine auto\|solo\|harness`, `--solo-project N` |
| 6 | Gates and proof settings (after the summary below) | **Use these** (the default: Enter keeps it; keep the detected values, blanks stay blank) · **Customize now** (asks gates `test`, `lint`, `types`, `format`, then high-risk paths, proof isolation, export directories to link and untracked files to copy, each pre-filled; Enter keeps it) · **Leave for later** (keeps them and prints the keys to edit) | `--gate name=cmd`, `--proof key=value` (a value set by a flag is not asked) |
| 7 | Where is the Jev key? (asked only when none is found) | 1Password · environment variable · paste now (hidden, goes to the key store) · skip. For 1Password, paste only the item ID (or the item link, or an `op://vault/item/field` reference): code-forge finds the vault and the key field and saves a full reference. If 1Password is locked or not found, you can try again, type a full reference, pick another source or skip (see [Keys](#keys)) | `--jev-ref <item-id\|link\|op://…>`, `--jev-env NAME`, `--no-jev` |

At the end `init` runs `doctor --quick`; `--skip-doctor` skips it.

### Codex and closed-book roles

Reviewers, the judge, System 2 and the plan author are *closed book*: they see the packet on stdin
and nothing else. Claude and Grok can run with no tools. Codex (provider `openai`) cannot: even its
read-only mode has a shell that can read any file on your machine. So with `--provider openai`, or
an openai L2 or L3:

- `code-forge validate` warns `levels.L2 resolves to openai — codex cannot run closed-book yet: it
  always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author`;
- `code-forge resolve L2` adds `closed_book: "refused"`;
- the full `doctor` fails its isolation row;
- a review, judge, System 2 or plan-author session on that level is refused (an openai fallback is
  skipped).

Give L2 and L3 an anthropic or xai model (`--level L2=<model>@anthropic`), or keep openai only for
L0 and L1, where coders run. To keep Codex in those roles anyway, set
`review.allow_open_book_codex: true`: every surface then warns that the reviewer can read files on
this machine.

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

## Install the recommended tools

`code-forge tools` shows each recommended tool (Claude Code, Codex CLI, Gemini CLI, Grok CLI,
1Password CLI, Solo): installed with its version, or missing with the command that installs it.
`code-forge tools install` installs the missing ones:

```bash
code-forge tools
code-forge tools install
```

It prints the plan first and asks one yes (default no; a no prints "cancelled — nothing installed"
and exits 1, as in `init`); `--yes` skips the question, `--dry-run`
only prints the plan, and `code-forge tools install codex gemini` installs just those. npm tools
need `npm` on PATH; Grok CLI and 1Password CLI install with Homebrew on macOS (elsewhere you get
the vendor link). Solo is a desktop app: you get its link, nothing opens on its own. Once the
1Password CLI is installed, turn on 1Password app → Settings → Developer → "Integrate with
1Password CLI". `doctor` lists the missing tools as an INFO line; it never fails for them.

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
| isolation | the closed-book reviewer saw no project file. An openai L2 fails it without running (see [above](#codex-and-closed-book-roles)) |
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
| `code-forge ledger tail --slug <slug> -n 20` | the last 20 ledger rows (`--n 20` works too) |
| `code-forge logs` | the last errors and warnings in the local log |

## Set a budget

Each session code-forge starts writes a ledger row with an estimated cost in US dollars (`usd`),
from a price table in the package. A model with no known price gets `usd: null` and is named as
unknown, never guessed. To cap a run, add this to `.code-forge.yml`:

```yaml
budget:
  usd: 20
```

At 80% of it you get one warning. At 100% no new session starts: the message names the budget
and the spend, and a review in flight is reported `unavailable: budget`. If the spend cannot be
read, no session starts either. Raise `budget.usd` (then `code-forge run reload --run <run>`) or
end the run.

| Command | Shows |
|---|---|
| `code-forge run status --run <run>` | `spent_usd`, `budget_usd` and how many sessions have no known price |
| `code-forge report --slug <slug>` | cost per block, open blocks included, then the spend per run and the total |
| `code-forge ledger add coder --run <run> --block <id> --usd <n> [--note "..."]` | records spend code-forge did not see, such as a coder you ran in the cloud or by hand |


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

The first `op` call of a session can make 1Password ask you to approve (Touch ID or password).
code-forge prints a line before that call and waits up to 60 seconds, then tries once more.

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

Tagged but not on npm yet? A maintainer can install the exact release from the repository:
`git checkout vX.Y.Z && npm pack && npm install -g ./codedology-code-forge-X.Y.Z.tgz`, then
`code-forge upgrade`. Switch back to the npm package with the first command above once it is published.

| Command | Does |
|---|---|
| `code-forge upgrade [--source <path>]` | re-points every recorded skill install at this package's `skill/` folder (or at `--source`): symlinks are re-made, copies re-copied |
| `code-forge models --refresh --from-cli-caches` | refreshes the model catalog from the Codex and Grok CLI caches on this machine |
| `code-forge remove [<harness>] [--scope project\|global]` | removes the skill links that `init` recorded |

## When something fails

Every verb that fails (a non-zero exit or a crash) adds one line to
`~/.code-forge/logs/errors.jsonl`: the time, the versions, the verb and its subcommand, the flag
names (never their values), the exit code, a kind (`usage`, `crash`, a 1Password kind such as
`op_timeout`, or `error`), the last error text and a fingerprint (12 characters that stay the same
when the same error comes back with other numbers, names or paths). Before it is written, your home
folder becomes `~`, the project folder `<project>`, the project slug `<slug>`, and emails, `op://`
references and 1Password item IDs are replaced too. Nothing leaves your machine on its own. The file
keeps its newest half when it grows over 1 MB.

Each line also names the commands you ran just before it (`before`, up to 5, oldest first): only the
command name and its own subcommand word, such as `keys test` or `init` — never a flag, a flag value
or anything else you typed. code-forge keeps that short list in `~/.code-forge/logs/breadcrumbs.json`.

Problems code-forge recovers from on its own are logged too, as **warnings** (`kind: warning`): a
1Password call that needed its retry, a review session retried after a timeout, System 1 (Jev)
falling back to the rules, and the 80% budget warning. A warning never prints the hint below.

In a terminal, a failure (not a usage mistake) prints one more line: `code-forge: this error was
saved to the local log. To send it to us: code-forge logs report` — at most once a day for the same
error, and never when an agent runs code-forge.

| Command | Does |
|---|---|
| `code-forge logs [--last N] [--json]` | the last errors and warnings, newest first (10 by default), with their fingerprints; a warning is marked `[warning]`, an error with a known fix `[fixed in X.Y.Z]` |
| `code-forge logs summary [--days N]` | each distinct error over the last 30 days and how many times it was seen; warnings are counted in their own list |
| `code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run]` | shares the selected errors (5 by default) with us as a GitHub issue; `--include-warnings` adds warnings, `--with-doctor` adds a setup check, `--force` reports an error that a newer version already fixes |
| `code-forge logs clear [--yes]` | deletes the log |
| `code-forge logs path` | prints where the log is |

**Known fixes.** code-forge ships a small list of errors already fixed, by fingerprint, with the
version that fixed each one. `code-forge logs` marks such an error `[fixed in X.Y.Z]`. Before
anything else, `logs report` checks the selected errors against that list (on your machine; nothing
is asked over the network for this):

- If the fix is in a newer version than yours, it prints `This error is fixed in X.Y.Z: <what was
  fixed>. Upgrade with: npm install -g @codedology/code-forge@latest` and does not report that
  error. When no other error is left, it stops there (exit 0). `--force` reports it anyway, with a
  note that the report comes from an older version.
- If your version already has the fix, the error is reported as usual, with the note "a fix for
  this shipped in X.Y.Z; it may be a regression."

What `logs report` does, in order:

1. It asks npm for the newest code-forge. If yours is older it says so (many errors are fixed in
   newer versions) and asks "Report anyway?" — the default is no. Without a terminal it stops
   unless you pass `--allow-old`.
   With `--with-doctor` it then runs `code-forge doctor --json` (60 seconds at most) and adds a
   "Setup check" section: each check's name, its status and a short detail, cleaned like the rest.
   If the doctor cannot run, it says so and the report goes on without it.
2. It builds the issue and cleans it twice. First the built-in cleaning (the same rules as the log).
   Then an AI check: a short model session (level L1 of your setup, with no tools and no files)
   reads the already-cleaned text and lists anything that could still identify a person, a company,
   a private project, a host, an internal URL, an account, a path or a secret. The AI never rewrites
   the text: code-forge replaces each listed piece with a label such as `<host>`, then runs the
   built-in cleaning once more. The AI only sees up to 16 KB; a longer report is cut first. Before
   the AI sees anything, a check stops the report if something still looks like a key or token.
3. That check runs again after the AI pass; it never prints what it found.
4. It says what is shared and what is never shared, how many things each pass cleaned (counts only),
   and prints the whole issue.
5. You choose: Send, Edit in my editor, or Cancel. Edit opens the text in `$VISUAL`, or `$EDITOR`
   when `$VISUAL` is not usable (each must be a plain command such as `vim`, or an absolute path,
   with no spaces or flags); what you save is
   cleaned and checked again and shown before you choose once more.
6. If the AI check could not run (no model CLI, not logged in, a timeout, an answer that is not
   JSON) it says so, Cancel is the default, and sending needs one more yes ("Send without the AI
   check?", default no). `--no-ai` skips the AI check on purpose, with the same extra yes.
7. With the GitHub CLI (`gh`) signed in, it first searches for an issue with the same fingerprint.
   When there is one, you can add a "happened again" comment with your versions and the count
   (the default), create a new issue anyway, or cancel. Without `gh` you get two links: a search for
   the fingerprint and a prefilled issue form (a long report is cut, and saved in full under
   `~/.code-forge/logs/`).

What is shared: code-forge, Node and OS versions, the newest version on npm, command names and flag
names, the names of the last commands you ran (no flags or values), exit codes, error kinds,
fingerprints, and error messages and crash stacks after both cleaning passes, plus your note; with
`--include-warnings` also the warnings, and with `--with-doctor` the setup check results. What is never shared: flag values, file contents, your code, keys
or tokens, your home folder, project folder or project name. You always see the full text, you can
edit it, and nothing is sent without your yes. The issue is **public**. `--dry-run` only prints;
`--yes` answers the questions for scripts, but not "Send without the AI check?": when the AI check
was unavailable it is still asked in a terminal, and without one nothing is sent. `--no-ai --yes`
is the explicit choice to send with only the built-in cleaning.

To turn it all off, set `CODE_FORGE_NO_ERROR_LOG=1`: this one switch turns off the error log, the
warnings and the command list (breadcrumbs) together. Every field and cleaning rule is listed in
[privacy.md](privacy.md).

The error log and the `gh` path are tested on macOS and Linux.

## Review speed

Reviews run several files at once. Three keys in `.code-forge.yml` tune it:

```yaml
review:
  parallel_tickets: 3          # files reviewed at once (1 to 16)
  provider_concurrency:        # sessions at once per provider
    anthropic: 4
    openai: 2
    xai: 2
```

The defaults are the values above. Set `parallel_tickets: 1` to review one file at a time. Run
`code-forge doctor` to see the effective numbers. With `code-forge review`, `--max` is one deadline
for the whole run. See [review-only.md](review-only.md#speed).

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

Going away while it runs? Ask for autopilot: a delegate makes a few of your decisions for a set
time, and a live log shows you everything when you return. See [autopilot.md](autopilot.md).

Next: the [first end-to-end test](tutorial-first-test.md).
