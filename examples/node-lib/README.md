# node-lib (example)

A three-module ESM library with a `node:test` suite. It ships inside `@codedology/code-forge` as the
practice repository of the rehearsal in `docs/tutorial-first-test.md`: copy it to a temp directory,
`git init` it and run one block — "add `clamp()` with tests" — end to end.

- `src/math.mjs` — `add`, `sub`, `mean`
- `src/strings.mjs` — `pad`, `capitalize`, `greet`
- `src/list.mjs` — `chunk`, `unique`

`npm test` runs the suite. `.gitattributes` marks `/.github` and `CHANGELOG.md` `export-ignore`, and
`test/package.test.mjs` reads both, so the suite only passes in code-forge's measurement export when
the export restores them. `.env.example` is copied to `.env` (untracked) before the run; `greet()`
reads `GREETING` from it.

The ignore rules ship as `gitignore` (npm pack drops a nested `.gitignore`); rename it to
`.gitignore` in the copy before `git init`.
