# Releasing

For maintainers. One command prepares a release on your machine; pushing and publishing stay
manual, so nothing leaves your machine until you say so.

## Record changes as you go

Add one CHANGELOG entry per change, while you work, instead of writing them all at release time:

```
npm run changelog -- added "**`code-forge <verb>`** — what it does, in one sentence."
npm run changelog -- fixed "What was wrong and what happens now."
npm run changelog -- list                # the current [Unreleased] entries
```

The type is `added`, `changed`, `deprecated`, `removed`, `fixed` or `security`. The entry goes
at the end of that `### <Type>` subsection of `## [Unreleased]`; a missing subsection is created
in Keep-a-Changelog order (Added, Changed, Deprecated, Removed, Fixed, Security), and a missing
`## [Unreleased]` above the latest release. Long text wraps at the column the file already uses.
Nothing else in the file changes. Empty text, an unknown type and an exact duplicate in the same
subsection are refused.

The repository's `.gitattributes` sets `CHANGELOG.md merge=union`. Parallel blocks often add their
lines at the same place in `## [Unreleased]`, and a plain merge or rebase then stops on a conflict.
The union driver keeps the lines from both sides, so these lands go through on every clone and in
CI. It does not sort lines or remove duplicates, so read the `[Unreleased]` section before you
release. The file is not in the npm package.

Union can still leave two `### Added` headings in `[Unreleased]` when each side added its own group
in a different place. code-forge's own merges and replays (`block land`, batch rebase,
`block rebase`) fold them into one (B813). For merges you make with plain git, such as combining
lane branches with `git merge --no-ff`, code-forge is the union driver (B990). `run start` and
`block land` set it in the clone's shared `.git/config` (every linked worktree uses it) when
`CHANGELOG.md` has `merge=union` (B990b), so there is no manual step:

```sh
git config merge.union.driver   # '/path/to/code-forge' land merge-driver %O %A %B %P 2>/dev/null || git merge-file --union %A %O %B
```

The line names the binary that set it and falls back to git's own union merge when that binary is
gone or older than `land merge-driver`, so it never breaks a merge. A driver of your own is kept;
the older manual line is replaced. It merges like git's union driver and then folds a `CHANGELOG.md`'s repeated `###` headings into the
first one of each kind, every line kept; other `merge=union` files get the plain union result.

Every `[Unreleased]` entry must sit under a `### <Type>` heading, and each type has one heading:
`npm run release` refuses a section with an entry before its first `###` heading or a `###` type
that appears twice, and names the line to fix (the site's changelog page would show a nameless or
repeated group).

## Check generated docs and test pins

Run `sh scripts/cf-release.sh regen-docs --check` before release (B959).
It checks the commands reference, config reference, then the recorded command
summaries, B801 doc examples and init goldens. It lists every stale step and
exits 1 if any step fails. Check mode removes the recording switches from the
test child, so a recording switch in your shell cannot rewrite a pin.

To update them, run `sh scripts/cf-release.sh regen-docs` once on the combined
tip. It writes them in that order and stops at the first failure. Review and
commit the generated diff. The init goldens use
`CODE_FORGE_INIT_GOLDEN_RECORD=1` to record only changed output (B989).

## Record a known fix

When a release fixes an error users report through `code-forge logs report`, record its
fingerprint (the 12 characters `code-forge logs` shows, and the `<!-- code-forge-fp: … -->` line of
the issue) before you release:

Add one entry to `native/cmd/code-forge-native/data/known-fixes.json`, the table built into the
Go binary (the only copy since B690b), then check it:

```
cd native && go test -count=1 -run TestB690bKnownFixes ./cmd/code-forge-native
```

The version is the release that carries the fix (usually the one you are about to make). The
test refuses a table that is not an array, an unknown key, a fingerprint that is not 12
lowercase hex characters or is listed twice, a `fixed_in` that is not `x.y.z`, a summary that is
empty, longer than 200 characters or not one plain line, and an `issue` that is not a positive
number or `null`.

```json
[
  {
    "fp": "0123456789ab",
    "fixed_in": "0.4.0",
    "summary": "review no longer hangs on an empty diff.",
    "issue": 42
  }
]
```

(The fingerprint above is an example, not a real one.) The fingerprint must be 12 lowercase hex
characters, the version `x.y.z`, the summary one plain line of at most 200 characters, and
`--issue` a positive issue number (left out, it is `null`). A fingerprint already in the table, and
a table that does not validate, are refused, and the file is not changed. The script is not in the
npm package.

What it does for users: `code-forge logs` marks the error `[fixed in X.Y.Z]`; on an older version
`logs report` prints the upgrade command and does not file it (unless `--force`); on that version
or newer it files it with a "may be a regression" note.

On GitHub, `.github/workflows/error-triage.yml` reads the same file from the default branch: when an
issue labelled `error-report` is opened or edited, it adds an `fp:<first 7 hex>` label per
fingerprint in the body and, when a fingerprint is in the table, comments once "Known issue: fixed
in X.Y.Z…" (a second run finds its own marker and does nothing). Every Monday a scheduled run counts
the open `error-report` issues by `kind:` label and by fingerprint label and writes the tables to
that run's job summary; it never changes an issue.

## Major release: run the full audit

A major release (`npm run release -- major`, or an explicit `x.y.z` with a higher major number)
needs the command audit of the new version committed first (B802). Run it from the repository
root with the binary you are about to release, then commit the report:

```
code-forge audit commands --out docs/audits/audit-<version>.md   # e.g. docs/audits/audit-1.0.0.md
git add docs/audits/audit-<version>.md && git commit -m "docs: command audit <version>"
npm run release -- major --dry-run
```

`code-forge audit commands [--json] [--out <file>]` runs every command of `code-forge help` with
`--help` and with an unknown flag, each in a throwaway HOME and git repository. `op`, `security`,
`claude`, `codex`, `grok`, `gemini`, `gh`, `curl`, `npm`, `npx` and `node` are stubs that exit 1,
the network goes to a closed proxy port, and keys use the file store, so no paid API, keychain,
1Password or real `~/.code-forge` is touched. Each check is a row of the report with a result
(`ok` or `problem`), a severity (`high`, `medium`, `low`), a detail and a Block column. A panic, a
hand-over to the removed Node engine, a binary that does not start, a `--help` that hangs or exits
non-zero with no usage is `high`. It exits 1 while any high problem is in the report, 2 on a usage
error. With no `--out` the Markdown goes to stdout; `--json` prints one line with the counts and
rows.

`npm run release` refuses the major release, naming the command to run, when the file is missing,
not committed at HEAD, not written by the audit tool, or has an open high problem. A high problem
is closed when its Block column names a block whose commit has landed (a commit subject on HEAD
starting `<block>:`, for example `B900: fix status --help`): edit the Block cells, commit, release
again. Patch and minor releases do not need the audit. `--no-checks` does not skip it; only
`--allow-no-audit` does, with a loud warning.

## The one-command flow

```
(cd native && go test -count=1 -run TestB690bKnownFixes ./cmd/code-forge-native)  # if this release fixes reported errors
code-forge audit commands --out docs/audits/audit-<version>.md  # major only: run, commit (see above)
npm run release -- minor --dry-run      # see the plan and the diffs; writes nothing
npm run release -- minor                # bump, CHANGELOG, README, checks, commit, tag
npm publish dist/npm/darwin-arm64       # the four platform packages first (B606)
npm publish dist/npm/darwin-x64
npm publish dist/npm/linux-x64
npm publish dist/npm/linux-arm64
npm publish                             # then the main package, from your machine
npm run release:site                    # wait for npm, move the docs pin, build, smoke check, deploy
git push origin main --follow-tags      # then push; CI only creates the GitHub release
```

CI never publishes to npm. Publish first, then push the tag.

The version argument is `patch`, `minor`, `major` or an explicit `x.y.z`. Options:

| Option | Effect |
|---|---|
| `--dry-run` | run the preflight, print the plan and the diffs, change nothing (no file, no commit, no tag, no fetch). Preflight failures are listed and the exit code is 1 |
| `--date YYYY-MM-DD` | the date for the CHANGELOG heading (default: today, local time) |
| `--no-checks` | skip the docs block preflight and the checks in step 4 (the CI run for HEAD, npm pack, npm ls, plugin validate), with loud warnings. For emergencies only |
| `--allow-no-audit` | a major release skips the command audit check (see "Major release: run the full audit"), with a loud warning. `--no-checks` alone does not skip it |
| `--allow-branch <name>` | release from a branch other than `main` |

### The release tools run in Go

`npm run release`, `npm run release:site`, `npm run docs-block` and `npm run changelog` run
`scripts/cf-release.sh`, which builds `native/cmd/cf-release` with `go` (CGO off) into
`node_modules/.cache/code-forge/cf-release` and runs it from the current folder. You need Go to
release. The commands, arguments, output, exit codes, file edits and git commands are the same as
the Node scripts they replaced (B570); parity tests in `native/cmd/cf-release` compare each case,
single runs and multi-step flows, with a golden recorded from the Node script. The Node scripts
`scripts/release.mjs`, `release-site.mjs`, `docs-block.mjs` and `changelog.mjs` and their Node
tests are gone (B574): the release workflow runs the Go tool. Since B690d the site data comes
from the Go tool too (`cf-release site-data`), so no release step starts Node. The goldens are never re-recorded: the Node recorders skip with a
message since Node was removed (B760); a checkout that still has the scripts is commit `5c2510a7`. Neither the Go tool nor `scripts/cf-release.sh` ships in the npm
package.

## The docs block

Before each release, open a docs block after the code blocks have landed. Use `--kind docs`
and own the documentation and stamp, for example:

```
code-forge block open DOCS --run <run> --level L1 --kind docs --owned README.md docs CHANGELOG.md --acceptance <acceptance-file>
npm run docs-block -- brief
```

Feed the brief's stdout to the docs coder. It includes the latest ancestor `v*` tag, commits and
changed files since that tag, `[Unreleased]` entries, existing Markdown docs, generated-doc checks,
and **Site pages a person must check**. Review every listed `src/pages/*.astro` page in
`CODE_FORGE_SITE_DIR`, or the sibling `../code-forge-site` by default. A missing site folder is
reported in one line and does not fail the brief. With no release tag, the brief covers the
repository's history and files.

Describe every user-visible `[Unreleased]` change in the page that explains the feature.
Remove stale instructions. Keep version history in CHANGELOG, not README.
Run the generated-doc check:

```sh
sh scripts/cf-release.sh regen-docs --check
```

It checks the commands reference, the config reference, the command summaries, the doc example
pins and the init goldens, and lists each stale one. `sh scripts/cf-release.sh regen-docs`
regenerates all of them once; commit the result as one commit. A batch land can do this by itself
on the combined tip: set `land.regenerate: [sh scripts/cf-release.sh regen-docs]` and `block land
B1 B2 ...` runs it after the merge and before the push, committing what changed as one commit on
main (B959b).

List the site pages from the brief in the final report so a person can check them.
If the site folder is absent, report that instead. Commit the docs so the tree is clean, then run:

```
npm run docs-block -- stamp
git add docs/release-docs.json
git commit -m "Stamp release docs"
```

The stamp records the full HEAD SHA in `through` and the latest release tag in `tag`. Commit it
as the docs block's **last commit**, then close and land the block on `main` before releasing.
Landing happens only after a block closes, so a stamp on `main` is the proof of closure; release
preflight does not read the ledger. The first real docs block creates this file.

After the stamp, only `docs/**`, `README.md` and `CHANGELOG.md` may change. Any other changed
file requires another docs block; start with `npm run docs-block -- brief` and commit a new stamp.
With no ancestor `v*` tag, release preflight skips the docs check with a warning. The emergency
`--no-checks` flag also skips it and prints a loud warning naming the docs block.

## Releasing checklist

Build the native binaries with `sh scripts/cf-release.sh build-native` (`--host` for local
testing; Go, no Node, B760); the release builds the four release targets itself in Go (B580).
The native code is pure Go and standard library only; builds and compatibility tests
force `CGO_ENABLED=0`, avoiding the macOS 26 LC_UUID linker error without caller configuration.

1. Every landed block has a CHANGELOG entry under `## [Unreleased]`. `code-forge release check`
   checks the tree is clean, HEAD is pushed, GitHub CI is green for the HEAD sha, the
   `[Unreleased]` shape and `npm ls`; it prints the bump (`### Added` entries: minor, else patch)
   and every step below in order, and exits 0 only when every check passes (`--json` for scripts).
   It also counts the blocks since the last `v*` tag (B951): release every 5–8 blocks. From
   `release.max_blocks` − 3 (default 8) it prints an info line; at `release.max_blocks` or more it
   prints `RELEASE NOW: …` (the autopilot stop report and binnacle show the same line).
2. The known-fixes test above when the release fixes reported errors.
3. Complete [the docs block](#the-docs-block), commit its stamp last, and land it on `main`.
4. `npm run release -- <bump> --dry-run`, and read the README and CHANGELOG diffs.
5. `npm run release -- <bump>`.
6. `npm publish dist/npm/<target>` for each of the four targets, then `npm publish`, then
   `npm run release:site` to update the website.
7. `git push origin main --follow-tags`; merge and push the site's release branch using the printed commands.
8. Comment the fixed version on each issue, and close it when all its items are done.

## What it does, in order

1. **Preflight.** Refuses (exit 1) unless the tree is clean, you are on `main`, your branch is
   neither behind nor diverged from `origin` (read with `git ls-remote`; being ahead only warns),
   `## [Unreleased]` in `CHANGELOG.md` has at least one entry, the new version is greater than
   the current one and the latest on npm (`npm view`; a network failure only warns), and the tag
   `vX.Y.Z` exists neither locally nor on origin. Every version location must also agree with
   `package.json` before anything is bumped. README must be readable, but needs no release anchor.
   `docs/release-docs.json` must contain a valid `through` commit that is an ancestor of HEAD,
   with the latest ancestor `v*` tag also an ancestor of `through` and recorded in `tag`. Only
   `docs/**`, `README.md` and `CHANGELOG.md` may have changed since `through`. A refusal names up
   to five disallowed files and directs you to `npm run docs-block -- brief`. No earlier release
   tag skips this check with a warning; `--no-checks` skips it with a loud warning.
   `--dry-run` lists the same preflight failures and exits 1 without a commit or tag.
2. **Bump** every place that holds the version:
   - `package.json` `version`
   - `npm-shrinkwrap.json` `version` and `packages[""].version` (and `package-lock.json` if present)
   - `.claude-plugin/plugin.json` `version`
   - `skill/scripts/forge` `PINNED_VERSION="x.y.z"`
   - with a native build, the four `@codedology/code-forge-<target>` pins in `package.json`
     `optionalDependencies` and in `npm-shrinkwrap.json` (B606); a missing pin refuses the release

   JSON files are parsed and written back with their own indentation and trailing newline; only
   the version fields change.
3. **CHANGELOG.** `## [Unreleased]` becomes `## [X.Y.Z] — <date>`, with a fresh empty
   `## [Unreleased]` above it. Compare links at the bottom are updated only when the file already
   has an `[Unreleased]: …/compare/…...HEAD` link; none are invented.

   **README.** Only the version in the optional `Latest:` line is updated; all other bytes stay the same.
   Without that line, README is unchanged and omitted from the release commit.
   Per-version notes live only in CHANGELOG, with no per-block additions to README.
4. **Checks.** The release runs no Node suite on the laptop (B580). Before it writes any file it
   reads the latest `CI` workflow run for HEAD (`gh run list --commit <HEAD> --workflow CI
   --limit 1`). CI runs the Go suite, the package check (no Node product file is packed) and the
   `config-doc --check` and `gen-commands-doc --check` doc checks. Since B777 the Go suite is split
   into parallel jobs (`go lint`, `build-native`, `go native` shards of `cmd/code-forge-native` from
   `scripts/ci-shards.sh`, `go other packages`; `-race` on Go stable, plain on the 1.22.x floor);
   the run's conclusion is `success` only when every job passed, so the run must be `completed` with
   conclusion `success` for that exact HEAD. The Go test steps run with `GOFLAGS=-trimpath`, as
   code-forge's own test gate `cf-gotest` does (B952). The same shard split runs on a laptop with
   `go -C native run ./cmd/cf-gotest --shards 6 --p 3`: one `go test -c` build, the
   `scripts/ci-shards.sh pick` split, at most `--p` shards at once, red when any shard is red (B807).
   A missing, running or failed run, or a `gh` error,
   refuses with exit 1 and one line that names HEAD and the run state, for example
   `CI for HEAD <sha> is not green: run status in_progress; push HEAD and wait for a green CI run`.
   Nothing is written or tagged. After the version edits it builds the four native targets with
   `go build` (the same targets, flags and output lines as `sh scripts/cf-release.sh build-native`;
   no Node version is stamped and no `node --version` runs (B690d)), then writes one platform package per target to
   `dist/npm/<target>/` (`package.json` with `os`, `cpu`, `files: ["bin"]` and the same version,
   README, LICENSE, `bin/code-forge-native`; `dist/npm/` is git-ignored) (B606), then runs
   `npm pack --dry-run` on each platform package (must be under 35 MB and hold the binary) and on
   the main package (the name and version must match, the file count is printed, it must be under
   5 MB and ship nothing from `native/bin`), `npm ls --json` (0 invalid, 0 missing, 0 extraneous; the project's own platform packages still
   installed at the previous version are ignored, and a tree whose only fault is extraneous
   packages names `npm prune`, the same rules as `code-forge release check`, B778), and `claude plugin validate .` when the
   `claude` CLI is installed (otherwise a warning). A failure here rolls back the version edits.
   CI covers HEAD before the release commit; the release commit only changes version fields,
   CHANGELOG and README.

   Real-process worker test deadlines use `test/helpers/load.mjs`: the one-minute host load
   divided by available CPUs, rounded up with a minimum of one (load 25 on 10 CPUs gives
   three). This scales scheduling allowances while preserving the one-second coder wait
   and injected backoff behavior. The helper performs no calibration sleep.
5. **Commit and tag.** One commit `Release vX.Y.Z` with only the changed version files,
   `CHANGELOG.md`, and `README.md` when its Latest line changed, then the annotated tag `vX.Y.Z`,
   whose message is that version's CHANGELOG section.
6. **Next steps.** It prints the four platform publishes (`npm publish dist/npm/<target>`) first, then
   `npm publish`, `npm run release:site`, then the push command (publish first; the tag push
   only creates the GitHub release), and the exact `gh release create … --verify-tag` command, with
   the release notes already written to a temp file.

The tool never pushes, never publishes and never creates a GitHub release.

## Release the site

After publishing to npm, run:

```sh
npm run release:site
# Optional: another site checkout, or a check without committing or deploying
npm run release:site -- --site ../code-forge-site --wait-min 20 --dry-run
# Optional: deploy another folder, relative to the site root
npm run release:site -- --deploy-dir dist --dry-run
```

The command reads the package name and version from `package.json` and requires its local
`vX.Y.Z` tag. It polls `npm view <name>@<version> version` every 30 seconds until npm serves
that exact version. `--wait-min` defaults to 20 minutes; a timeout exits 1 with a message and
changes nothing in the site repository.

Once npm serves the version, it generates the site data in Go, the same JSON as
`cf-release site-data --tag vX.Y.Z` with the ledger at `~/.code-forge/ledger` (B690d).
The site defaults to `../code-forge-site`. A clean detached worktree of the site's HEAD is
created at `<site>/.code-forge-tmp/release-site-X.Y.Z`; the main site's files, staging index,
branch and open work stay untouched. The command writes `src/data/site-data.json` there and
moves the docs pin: it sets `"commit"` in `docs-site/site/docs-source.json` to the release tag
`vX.Y.Z`, keeping every other key, the key order, the indent and the trailing newline, and
prints `Docs pin docs-site/site/docs-source.json: <old> -> vX.Y.Z` (B775). The docs build reads
the docs pages from `docs/<page>.md` at that commit, so the site docs show the released text. A
site without that file releases as before, with one line `Docs pin
docs-site/site/docs-source.json not found; the site docs pin was not moved`; a pin file that is
not a JSON object stops the release before any build, commit or deploy. It then
links installed dependencies from the main checkout's `node_modules` if available, or runs
`npm ci` when dependencies are missing. Cache directories such as `node_modules/.vite`,
`.astro`, and `.cache` stay local to the temporary tree. It runs `npm run build:site` once.

The release does not run the site's own tests or design checks: it neither reads nor runs
`gates.test` or `gates.custom` from the site's `.code-forge.yml`, and prints `site tests and
design checks run during development (site gates), not at release` (B780). Those checks run as
the site's code-forge gates on every site block, so a release is a build, a smoke check and a
deploy. A failed build prints `Site build failed: npm run build:site (exit N)`, prevents both the
commit and deploy, and leaves the old site live.
After the build, the deploy folder must exist and hold `index.html`, and the smoke check must
find `<package>@<version>` (for example `@codedology/code-forge@0.25.0`) as a whole word in a
built `.html`, `.md` or `.txt` file of the deploy folder; the docs pages pinned to the release
tag name it through `docs/step-by-step.md`, so the merged site's `llms-full.txt` does. It prints
`Smoke check: <file> names <package>@<version>`. Otherwise the command exits 1 with no commit and
no deploy. The deploy folder defaults to `docs-site/site/.pages-out`,
the full site that `build:site` merges (marketing pages plus `/docs`, `llms.txt` and `_headers`);
`dist` alone holds only the marketing pages. `--deploy-dir <path>` picks another folder; it must
be relative to the site root and may not contain `..`. Then it creates `release-site/vX.Y.Z`,
commits `src/data/site-data.json` and the moved `docs-site/site/docs-source.json` in one commit
(only the site data when the pin file is missing), and runs
`npx wrangler pages deploy <deploy folder> --project-name code-forge-ai --branch main`, printing
the deploy URL. `--dry-run` prints this command as its `Would deploy:` line. Wrangler must already be authenticated. The temporary worktree is removed
on success or failure. An existing release branch is refused; a deployment failure keeps
the commit available for the owner to inspect and retry manually.

`--dry-run` still waits for npm, generates data, installs or links dependencies, builds and
runs the smoke check; it prints the commit and deploy plan with no new branch, commit or deployment.
The command never pushes. After a real release, run the printed commands to merge
`release-site/vX.Y.Z` in the main site checkout and push it when your open work allows it.

## Local site data

After tagging a release, run this maintainer tool from the repository. It writes data for a
local website update. It does not edit or publish the site. No CI job or token is needed.

```sh
sh scripts/cf-release.sh site-data --tag vX.Y.Z --out site-data.json
```

This is the Go tool (B690d); the older Node script it replaced is deleted (B760). `--ledger-dir <dir>` reads another ledger folder than
`~/.code-forge/ledger`.

Omit `--out` to print JSON to stdout. Version, date, released changelog entries and commands
come from the tag's `CHANGELOG.md` and `docs/reference/commands.md`, even with local edits.
Changelog sections contain `name` and Markdown `entries`; commands contain `name`, `description`,
`usage` and `subcommands` (`name` and `usage`).
Since B801 each command section on `commands.md` also holds its flags, examples, exit codes, `--json`
shape and related commands under `####` headings after its usage; the site data still takes only
the description before the first fence and the usage in that fence (`TestB801SiteReadsTheCommandReference`).

Proof reads `code-forge.jsonl` and its numbered rotations in `~/.code-forge/ledger`; use
`--ledger-dir <dir>` for another directory. A missing directory fails. It exports only distinct
run counts, distinct run/block pairs with a `block.close` status of `complete`, and estimated
USD using the report's `buildSpend` pricing, rounded to two decimals. Unknown-price sessions
contribute no spend. Proof uses the release date. Identical tag and ledger inputs give identical
bytes. It includes all supplied ledger history, including work after the tag. No other ledger
fields are copied. The script is excluded from the npm package.

Proof also holds the samples the site's `/proof` page shows (B784). Each is picked by a fixed
rule; a sample with no matching rows is `null`, and the page hides its card instead of showing a
made-up number. Durations use the report's text (`45s`, `13m 19s`, `1h 18m 46s`), `usd` is
rounded to four decimals, and `date` is the UTC day. Only ids that look like `r-...`, `B123` or
`F1` (letters, digits, `-` and `_`, no slash, at most 64 characters) are exported; a row with
any other id is skipped, and no file name, path, prompt, model or finding text leaves the ledger.

- `overnight`: the latest-started ended run (it has `run.end`; its start is `run.start`, else its
  first timed row) with 3 or more blocks closed `complete` and a wall time of 12 hours at most.
  Fields: `run`, `date`, `wall_ms`, `wall`, `usd` (the run's spend), `closed`, `stopped` (blocks
  with `block.stop` and no later complete close in that run).
- `single`: the latest-started ended run with exactly 1 block closed and none stopped; same fields.
- `stopped`: the latest stop of a block whose last state row is `block.stop`, with a `dispatch`
  before it and spend above $0. Fields: `run`, `block`, `date`, `wall_ms` and `wall` (dispatch to
  stop), `usd`.
- `finding`: the latest review where a file had an open `warning` finding, a later
  `review.result` approved that file, the finding was not waived, and the block closed
  `complete`. Fields: `run`, `block`, `finding` (its id), `severity`, `rounds` (review rounds of
  that file up to the approval), `date`. The ledger keeps no finding text.
- `gate`: the latest `gate.done` with `all_ok`, not cancelled, of a block that closed `complete`.
  Fields: `run`, `block`, `date`, `scope`, and `results` in ledger order, each `gate`, `result`
  (`ok`, `skipped` or `failed`), `wall_ms` and `wall` (`null` when skipped). The ledger keeps no
  pass counts.

## Rollback

If a check fails (or the commit fails), every file the tool changed is written back byte for
byte, and there is no commit and no tag. Fix the problem and run the command again. If the tag
step itself fails after the commit, the commit stays and the tool prints the exact `git tag`
command to finish by hand.

## What stays manual

- **Push:** `git push origin main --follow-tags` sends the commit and the tag together.
- **Publish:** `npm publish dist/npm/<target>` for the four targets, then `npm publish`, from the
  tagged commit, before you push. Only you publish; CI never
  does, so no npm token is stored in GitHub.
- **GitHub release:** automatic when the tag is pushed. See below.
- **Your own install:** after `npm publish`, run `npm install -g @codedology/code-forge@latest` and
  `code-forge upgrade`. To try the release before it is on npm, `npm pack` at the tag and
  `npm install -g ./codedology-code-forge-X.Y.Z.tgz`.

## GitHub Releases

Each version gets a GitHub release whose body is its CHANGELOG section plus two links: the
version's npm page and the CHANGELOG at the tag. `npm run release -- notes vX.Y.Z [--out
<file>]` (or `sh scripts/cf-release.sh release notes vX.Y.Z`) prints that body from the Go tool
(exit 1 when the CHANGELOG has no such version).

- **Automatic (tag push).** `.github/workflows/release.yml` runs on every `v*` tag. It checks
  that the tag equals `v` + the `package.json` version, sets up Go from `native/go.mod`, writes
  the notes with `sh scripts/cf-release.sh release notes` (no Node, no `npm ci`; B574),
  and runs `gh release create vX.Y.Z --verify-tag`. It has `contents: write` and nothing else (no
  npm token, no id-token). If the release already exists (a re-run), it edits it with the same notes.
- **By hand.** If the tag run fails (for example GitHub Actions is off), run the command the release
  tool printed: `gh release create vX.Y.Z --title vX.Y.Z --notes-file <path> --verify-tag`.

The other direction needs nothing: the npm page already links to the repository through the
`repository` and `homepage` fields of `package.json`.

The retire tool in `tools/retire-fable-forge/` has its own version and is not touched by this
command.
