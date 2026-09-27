# Releasing

For maintainers. One command prepares a release on your machine; pushing and publishing stay
manual, so nothing leaves your machine until you say so.

## Record changes as you go

Add one CHANGELOG entry per change, while you work, instead of writing them all at release time:

```
npm run changelog -- added "**`code-forge foo`** — what it does, in one sentence."
npm run changelog -- fixed "What was wrong and what happens now."
npm run changelog -- list                # the current [Unreleased] entries
```

The type is `added`, `changed`, `deprecated`, `removed`, `fixed` or `security`. The entry goes
at the end of that `### <Type>` subsection of `## [Unreleased]`; a missing subsection is created
in Keep-a-Changelog order (Added, Changed, Deprecated, Removed, Fixed, Security), and a missing
`## [Unreleased]` above the latest release. Long text wraps at the column the file already uses.
Nothing else in the file changes. Empty text, an unknown type and an exact duplicate in the same
subsection are refused.

## The one-command flow

```
npm run release -- minor --dry-run      # see the plan and the diffs; writes nothing
npm run release -- minor                # bump, CHANGELOG, checks, commit, tag
git push origin main --follow-tags      # printed by the tool; you run it
```

The version argument is `patch`, `minor`, `major` or an explicit `x.y.z`. Options:

| Option | Effect |
|---|---|
| `--dry-run` | run the preflight, print the plan and the diffs, change nothing (no file, no commit, no tag, no fetch). Preflight failures are listed and the exit code is 1 |
| `--date YYYY-MM-DD` | the date for the CHANGELOG heading (default: today, local time) |
| `--no-checks` | skip the checks in step 4, with a loud warning. For emergencies only |
| `--allow-branch <name>` | release from a branch other than `main` |

## What it does, in order

1. **Preflight.** Refuses (exit 1) unless the tree is clean, you are on `main`, your branch is
   neither behind nor diverged from `origin` (read with `git ls-remote`; being ahead only warns),
   `## [Unreleased]` in `CHANGELOG.md` has at least one entry, the new version is greater than
   the current one and the latest on npm (`npm view`; a network failure only warns), and the tag
   `vX.Y.Z` exists neither locally nor on origin. Every version location must also agree with
   `package.json` before anything is bumped.
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
4. **Checks.** `npm test`, `npm run typecheck`, `npm pack --dry-run` (the name and version must
   match; the file count is printed), `npm ls --json` (0 invalid, 0 missing), and
   `claude plugin validate .` when the `claude` CLI is installed (otherwise a warning).
5. **Commit and tag.** One commit `Release vX.Y.Z` with only the changed files, then the annotated
   tag `vX.Y.Z`, whose message is that version's CHANGELOG section.
6. **Next steps.** It prints the push command, the two ways to publish, and the exact
   `gh release create … --verify-tag` command, with the release notes already written to a temp
   file.

The tool never pushes, never publishes and never creates a GitHub release.

## Rollback

If a check fails (or the commit fails), every file the tool changed is written back byte for
byte, and there is no commit and no tag. Fix the problem and run the command again. If the tag
step itself fails after the commit, the commit stays and the tool prints the exact `git tag`
command to finish by hand.

## What stays manual

- **Push:** `git push origin main --follow-tags` sends the commit and the tag together.
- **Publish, one of:**
  - **CI (recommended).** The pushed `v*` tag runs `.github/workflows/publish.yml`: plugin
    validation, `npm ci`, tests, typecheck, `npm pack --dry-run`, `npm audit`, a check that the tag
    equals `v` + the `package.json` version, `npm publish --provenance`, then a smoke test of the
    published package. It needs the repository secret `NPM_TOKEN`. The tool tells you whether
    `gh secret list` shows it (`set`, `not set`, or `unknown` when `gh` is missing or not logged in).
  - **By hand:** `npm publish` from the tagged commit.
- **`NPM_TOKEN` setup (once):** on npmjs.com create an automation (or granular, publish-only)
  access token, then add it to the GitHub repository with `gh secret set NPM_TOKEN` or under
  Settings → Secrets and variables → Actions.
- **GitHub release:** automatic with the CI path; one printed command with the manual path. See
  below.

## GitHub Releases

Each version gets a GitHub release whose body is its CHANGELOG section plus two links: the
version's npm page and the CHANGELOG at the tag. `node scripts/release.mjs notes vX.Y.Z [--out
<file>]` prints that body (exit 1 when the CHANGELOG has no such version).

- **Automatic (tag push, `NPM_TOKEN` set).** `publish.yml` has a `github-release` job that runs
  only after the `publish` job (npm publish and the post-publish smoke test) succeeded. It has
  `contents: write` and nothing else (no id-token, no `NPM_TOKEN`), writes the notes with the
  command above, and runs `gh release create vX.Y.Z --verify-tag`. If the release already exists
  (a re-run), it edits it with the same notes instead of failing.
- **Manual (no `NPM_TOKEN`).** After `npm publish`, run the command the release tool printed:
  `gh release create vX.Y.Z --title vX.Y.Z --notes-file <path> --verify-tag`. Once `NPM_TOKEN` is
  set, a tag push does all of this by itself.

The other direction needs nothing: the npm page already links to the repository through the
`repository` and `homepage` fields of `package.json`.

The retire tool in `tools/retire-fable-forge/` has its own version and is not touched by this
command.
