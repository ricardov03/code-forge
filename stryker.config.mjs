/**
 * Stryker mutation testing config. Shipped by B0, per plan §1.1/§10.2.
 *
 * Test runner is the built-in "command" runner (no extra dependency beyond
 * `@stryker-mutator/core`) invoking `node --test`, because the project's test runner is Node's
 * own `node:test`, for which Stryker has no dedicated plugin. The command runner re-runs the
 * whole suite per mutant (slower, but correct for any test framework) and cannot do per-test
 * coverage analysis, hence `coverageAnalysis: "off"`.
 *
 * `mutate` defaults to every `src/**\/*.mjs` file (not literally "the whole package" — `bin/`,
 * `schema/`, `skill/` etc. are never mutated); every block/CI invocation narrows it further with
 * `--mutate "<glob>"` (e.g. `--mutate "src/util/**\/*.mjs"` for B0's own baseline run). No
 * `thresholds.break` is set: per plan §9.4/O7, Wave 1 runs record a baseline only — they are not
 * a gate. Later waves derive a per-file floor (`baseline - 5`) and enforce it themselves; this
 * file is B0-owned and is not edited by later blocks, so nothing later can add a break threshold
 * here either — the "record, don't gate" property is permanent, not just Wave 1's default.
 *
 * `commandRunner.command` is `node --experimental-test-module-mocks --test 'test/**\/*.test.mjs'`
 * — an explicit glob ROOTED AT `test/`, not a bare directory argument and not bare `node --test`
 * with no path at all. Two separate facts drove this, both hit while writing this file:
 *   1. On Node 22.x, `node --test <directory>` does not recurse into the directory the way it
 *      does on Node 20.x (facts diff, B0 report) — a bare directory argument silently discovers
 *      nothing. A glob works on every Node line this package supports (R7: Node 22+ only).
 *   2. Bare `node --test` with NO path argument at all recurses from the process's cwd and picks
 *      up `*.test.mjs` files ANYWHERE in the tree except `node_modules/` — including
 *      `sources/**` (this repo's local, gitignored research copies), which is invisible in CI
 *      (gitignored, never checked out there) but very much present on a dev machine. That made
 *      `npm test` locally report a different, larger test count than the 5 files this package
 *      actually owns — a real "green for the wrong reason" the CLAUDE.md Verification Rules
 *      warn about, caught only by noticing an unfamiliar test name in the output. `npm test`
 *      (package.json) uses the identical glob for the same reason.
 * `--experimental-test-module-mocks` matches `npm test` too — some tests
 * (`test/util/git.test.mjs`) use `node:test`'s `mock.module()`, which throws without it.
 *
 * `tsconfigFile` is pointed at a name that does not exist in the project on purpose:
 * `@stryker-mutator/core`'s built-in sandbox preprocessor unconditionally tries to rewrite
 * `extends`/`references` paths in whatever file `tsconfigFile` names (default `tsconfig.json`)
 * using `ts.parseConfigFileTextToJson`, an API `typescript` 7.0.2 removed — so with the default,
 * ANY Stryker run crashes outright the moment `tsconfig.json` exists (facts diff, see the B0
 * report). Our tsconfig.json is flat (no `extends`/`references`), so there is nothing for that
 * preprocessor to do; pointing it at a nonexistent file makes it a no-op instead of a crash.
 *
 * `.github/workflows/ci.yml`'s `stryker-baseline` job is what actually produces
 * `stryker-baseline.json` from this config's `jsonReporter` output (trimmed to per-file counts) —
 * this file only shapes the mutation run itself.
 */

/** @type {import('@stryker-mutator/api/core').PartialStrykerOptions} */
const config = {
  packageManager: 'npm',
  testRunner: 'command',
  commandRunner: {
    command: "node --experimental-test-module-mocks --test 'test/**/*.test.mjs'",
  },
  tsconfigFile: 'tsconfig.stryker-unused.json',
  coverageAnalysis: 'off',
  mutate: ['src/**/*.mjs'],
  reporters: ['clear-text', 'progress', 'json'],
  jsonReporter: {
    fileName: 'reports/mutation/mutation-report.json',
  },
  tempDirName: '.stryker-tmp',
  // 'always', not `true`: `true` only cleans up after a successful run, so a crashed/interrupted
  // run leaves .stryker-tmp/sandbox-*/ (a full source + node_modules copy per mutant batch)
  // behind, un-cleaned. `npm test`'s glob (test/**/*.test.mjs, relative to cwd) does NOT reach
  // into .stryker-tmp/sandbox-*/test/ from the package root, so a stale sandbox no longer gets
  // silently re-run by a later root-level `npm test` either way — 'always' here is disk hygiene
  // after a crash, not a correctness requirement.
  cleanTempDir: 'always',
  timeoutMS: 30000,
  logLevel: 'info',
};

export default config;
