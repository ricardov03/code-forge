# node-lib (example)

A three-module ESM library with a `node:test` suite. It ships inside `@codedology/code-forge` as the
repository the **first real use** runs on (`scripts/first-use.mjs`): the script copies it to a temp
directory, `git init`s it and runs one block — "add `clamp()` with tests" — end to end.

- `src/math.mjs` — `add`, `sub`, `mean`
- `src/strings.mjs` — `pad`, `capitalize`, `greet`
- `src/list.mjs` — `chunk`, `unique`

`npm test` runs the suite. `.gitattributes` marks `/.github` and `CHANGELOG.md` `export-ignore`, and
`test/package.test.mjs` reads both, so the suite only passes in code-forge's measurement export when
the export restores them. `.env.example` is copied to `.env` (untracked) before the run; `greet()`
reads `GREETING` from it.

The ignore rules ship as `gitignore` (npm pack drops a nested `.gitignore`); `first-use.mjs` renames
it to `.gitignore` in the copy before `git init`.
