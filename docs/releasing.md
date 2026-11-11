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

## Record a known fix

When a release fixes an error users report through `code-forge logs report`, record its
fingerprint (the 12 characters `code-forge logs` shows, and the `<!-- code-forge-fp: … -->` line of
the issue) before you release:

```
npm run known-fix -- add 0123456789ab 0.4.0 "review no longer hangs on an empty diff." --issue 42
npm run known-fix -- check               # validate the whole table
```

The version is the release that carries the fix (usually the one you are about to make). This
appends one entry to `src/util/known-fixes.json`, which ships in the package:

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

## The one-command flow

```
npm run known-fix -- check              # if this release fixes reported errors: table is valid
npm run release -- minor --dry-run      # see the plan and the diffs; writes nothing
npm run release -- minor                # bump, CHANGELOG, README, checks, commit, tag
npm publish                             # publish to npm from your machine
npm run release:site                    # wait for npm, check and deploy the site
git push origin main --follow-tags      # then push; CI only creates the GitHub release
```

CI never publishes to npm. Publish first, then push the tag.

The version argument is `patch`, `minor`, `major` or an explicit `x.y.z`. Options:

| Option | Effect |
|---|---|
| `--dry-run` | run the preflight, print the plan and the diffs, change nothing (no file, no commit, no tag, no fetch). Preflight failures are listed and the exit code is 1 |
| `--date YYYY-MM-DD` | the date for the CHANGELOG heading (default: today, local time) |
| `--no-checks` | skip the docs block preflight and the checks in step 4, with loud warnings. For emergencies only |
| `--allow-branch <name>` | release from a branch other than `main` |

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
Run both generated-doc checks:

```sh
node scripts/gen-config-doc.mjs --check
node scripts/gen-commands-doc.mjs --check
```

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

Build the optional native binaries with `node scripts/build-native.mjs` (`--host` for local
testing). The native code is pure Go and standard library only; builds and compatibility tests
force `CGO_ENABLED=0`, avoiding the macOS 26 LC_UUID linker error without caller configuration.

1. Every landed block has a CHANGELOG entry under `## [Unreleased]`.
2. `npm run known-fix -- check` when the release fixes reported errors.
3. Complete [the docs block](#the-docs-block), commit its stamp last, and land it on `main`.
4. `npm run release -- <bump> --dry-run`, and read the README and CHANGELOG diffs.
5. `npm run release -- <bump>`.
6. `npm publish`, then `npm run release:site` to update the website.
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

   JSON files are parsed and written back with their own indentation and trailing newline; only
   the version fields change.
3. **CHANGELOG.** `## [Unreleased]` becomes `## [X.Y.Z] — <date>`, with a fresh empty
   `## [Unreleased]` above it. Compare links at the bottom are updated only when the file already
   has an `[Unreleased]: …/compare/…...HEAD` link; none are invented.

   **README.** Only the version in the optional `Latest:` line is updated; all other bytes stay the same.
   Without that line, README is unchanged and omitted from the release commit.
   Per-version notes live only in CHANGELOG, with no per-block additions to README.
4. **Checks.** `node scripts/gen-config-doc.mjs --check` first, immediately followed by
   `node scripts/gen-commands-doc.mjs --check` (a stale reference or a missing script fails),
   then `npm test`, `npm run typecheck`,
   `npm pack --dry-run` (the name and version must match; the file count is printed),
   `npm ls --json` (0 invalid, 0 missing), and `claude plugin validate .` when the `claude` CLI
   is installed (otherwise a warning).
   If local `npm test` fails with identifiable Node TAP/spec file locations, the tool prints
   the original result and reruns only those files, once each in separate serial invocations
   (`npm test -- --test-concurrency=1 <file>`). It prints each rerun's output and exit code.
   It accepts the failure only when every isolated rerun passes and the latest `CI` workflow
   run returned by `gh run list --commit <HEAD>` is completed with conclusion `success` for
   that exact pre-release HEAD. It then prints
   `local flaky (passed alone, CI green): <files>`. Missing `gh`, unreadable or absent CI,
   pending/failed CI, unknown file locations, or any failing rerun still fail and roll back.
   No retry runs after a green local test run.

   Real-process worker test deadlines use `test/helpers/load.mjs`: the one-minute host load
   divided by available CPUs, rounded up with a minimum of one (load 25 on 10 CPUs gives
   three). This scales scheduling allowances while preserving the one-second coder wait
   and injected backoff behavior. The helper performs no calibration sleep.
5. **Commit and tag.** One commit `Release vX.Y.Z` with only the changed version files,
   `CHANGELOG.md`, and `README.md` when its Latest line changed, then the annotated tag `vX.Y.Z`,
   whose message is that version's CHANGELOG section.
6. **Next steps.** It prints `npm publish`, `npm run release:site`, then the push command (publish first; the tag push
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

Once npm serves the version, it generates data with `scripts/gen-site-data.mjs --tag vX.Y.Z`.
The site defaults to `../code-forge-site`. A clean detached worktree of the site's HEAD is
created at `<site>/.code-forge-tmp/release-site-X.Y.Z`; the main site's files, staging index,
branch and open work stay untouched. The command writes `src/data/site-data.json` there,
links installed dependencies from the main checkout's `node_modules` if available, or runs
`npm ci` when dependencies are missing. Cache directories such as `node_modules/.vite`,
`.astro`, and `.cache` stay local to the temporary tree. It runs `npm run build:site`, every `gates.custom` entry (including its environment),
and `gates.test` from that worktree's `.code-forge.yml`.

Any failing gate is named, prevents both the commit and deploy, and leaves the old site live.
After all gates pass, the deploy folder must exist and hold `index.html`; otherwise the command
exits 1 with no commit and no deploy. The deploy folder defaults to `docs-site/site/.pages-out`,
the full site that `build:site` merges (marketing pages plus `/docs`, `llms.txt` and `_headers`);
`dist` alone holds only the marketing pages. `--deploy-dir <path>` picks another folder; it must
be relative to the site root and may not contain `..`. Then it creates `release-site/vX.Y.Z`,
commits only `src/data/site-data.json`, and runs
`npx wrangler pages deploy <deploy folder> --project-name code-forge-ai --branch main`, printing
the deploy URL. `--dry-run` prints this command as its `Would deploy:` line. Wrangler must already be authenticated. The temporary worktree is removed
on success or failure. An existing release branch is refused; a deployment failure keeps
the commit available for the owner to inspect and retry manually.

`--dry-run` still waits for npm, generates data, installs or links dependencies, builds, and
runs every gate; it prints the commit and deploy plan with no new branch, commit or deployment.
The command never pushes. After a real release, run the printed commands to merge
`release-site/vX.Y.Z` in the main site checkout and push it when your open work allows it.

## Local site data

After tagging a release, run this maintainer tool from the repository. It writes data for a
local website update. It does not edit or publish the site. No CI job or token is needed.

```sh
node scripts/gen-site-data.mjs --tag vX.Y.Z --out site-data.json
```

Omit `--out` to print JSON to stdout. Version, date, released changelog entries and commands
come from the tag's `CHANGELOG.md` and `docs/reference/commands.md`, even with local edits.
Changelog sections contain `name` and Markdown `entries`; commands contain `name`, `description`,
`usage` and `subcommands` (`name` and `usage`).

Proof reads `code-forge.jsonl` and its numbered rotations in `~/.code-forge/ledger`; use
`--ledger-dir <dir>` for another directory. A missing directory fails. It exports only distinct
run counts, distinct run/block pairs with a `block.close` status of `complete`, and estimated
USD using the report's `buildSpend` pricing, rounded to two decimals. Unknown-price sessions
contribute no spend. Proof uses the release date. Identical tag and ledger inputs give identical
bytes. It includes all supplied ledger history, including work after the tag. No other ledger
fields are copied. The script is excluded from the npm package.

## Rollback

If a check fails (or the commit fails), every file the tool changed is written back byte for
byte, and there is no commit and no tag. Fix the problem and run the command again. If the tag
step itself fails after the commit, the commit stays and the tool prints the exact `git tag`
command to finish by hand.

## What stays manual

- **Push:** `git push origin main --follow-tags` sends the commit and the tag together.
- **Publish:** `npm publish` from the tagged commit, before you push. Only you publish; CI never
  does, so no npm token is stored in GitHub.
- **GitHub release:** automatic when the tag is pushed. See below.
- **Your own install:** after `npm publish`, run `npm install -g @codedology/code-forge@latest` and
  `code-forge upgrade`. To try the release before it is on npm, `npm pack` at the tag and
  `npm install -g ./codedology-code-forge-X.Y.Z.tgz`.

## GitHub Releases

Each version gets a GitHub release whose body is its CHANGELOG section plus two links: the
version's npm page and the CHANGELOG at the tag. `node scripts/release.mjs notes vX.Y.Z [--out
<file>]` prints that body (exit 1 when the CHANGELOG has no such version).

- **Automatic (tag push).** `.github/workflows/release.yml` runs on every `v*` tag. It checks
  that the tag equals `v` + the `package.json` version, writes the notes with the command above,
  and runs `gh release create vX.Y.Z --verify-tag`. It has `contents: write` and nothing else (no
  npm token, no id-token). If the release already exists (a re-run), it edits it with the same notes.
- **By hand.** If the tag run fails (for example GitHub Actions is off), run the command the release
  tool printed: `gh release create vX.Y.Z --title vX.Y.Z --notes-file <path> --verify-tag`.

The other direction needs nothing: the npm page already links to the repository through the
`repository` and `homepage` fields of `package.json`.

The retire tool in `tools/retire-fable-forge/` has its own version and is not touched by this
command.
