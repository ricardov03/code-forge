# Proof — two tiers, red→green, the measurement export

## §1 Tiers and what the gate enforces (R16)

Tier per file = `high` when `risk ≥ 2`, or a `proof.tiers.high.paths` match, or `security_sensitive`; else `light`. `forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]` prints it; it is decided at `review-file` time, recorded, and the block gate enforces the union.

| tier | proof required | when |
|---|---|---|
| light | red→green recommended for every new or changed test; **optional at the gate** | the coder or orchestrator runs the runner per test (§2) |
| high | red→green **required at the gate** for every changed high-tier file, plus the deeper **review** depth of `review.md` §2 (two lenses and a judge) | as above; `block close` refuses `unproven <file>` |

**`forge block close` enforces the high tier:** tier inputs are `proof.tiers.high.paths` (the union of the config at the block's base and the current one), the recorded `risk`, and `security_sensitive`; a row naming the file whose MAC fails makes it high. Each changed high-tier file needs a signed `proof` row of this run and block with `step: red-green`, `proven: true`, `red_kind: assertion` whose `covers` list names the file (`revert` covers the sources it put back; `assertion-deletion` covers only its test), else `unproven <file>`; only the human's `forge block waive <id> proof --file <path>` clears it.

**Mutation testing is not part of this product.** The question whether tool-made mutants on changed lines (Infection, Stryker) should be a user-facing proof tier was decided: **cut**. There is no `proof mutate`, no nightly mutation run, no `min_msi` floor, and no mutation-tool key in `.code-forge.yml`; a consumer that runs a mutation tool does so outside code-forge and never beside a coder's test run in the same tree. What replaces it as proof is the rule itself: one exact test per acceptance clause plus one negative or boundary where the clause has a failure mode — assertions that say **which** element and **how many**, never merely whether something exists — and the isolated per-file review with the convergence rule, which on this package's own build found the defects green suites had missed.

`proof.extra` (an argv array) keeps a consumer's legacy proof commands running inside the gate.

## §2 Red→green — journaled, killable, RED must mean an assertion

**One call proves one test** — a test file, or one case of it (`<file>::<case>` is the label) — with one of two mechanisms: `revert` (the block's changed source files are put back to their base version, so the test must fail against the code it was written for) or `assertion-deletion` (a characterization test, proven by deleting its assertions). The runner does not distinguish a new test from a changed one: the caller runs it once per test the acceptance names, new or changed alike, and the count of `RED` rows is the count of proven tests — there is no separate "new cases" tally. In this version the runner is invoked per test from the coder's or the orchestrator's side; the block gate reads the resulting signed `proof` rows but does not launch the runner itself.

The runner works in the export (§3) or under the lock with a journal: it refuses when a journal already exists (an earlier run was interrupted) → journals every source file the test covers (bytes, sha256, mode — or `existed: false`) → writes the **base** version of each (`git show <base>:<path>`, never `stash` or `checkout --`); **a file the block added has no base version, so it is removed for the red run** → runs the filtered tests → **parses** the output: an assertion failure ⇒ `RED`; a fatal, compile or import error ⇒ `RED_INVALID`, which proves nothing (a removed module that the test imports is the usual cause — such a test needs an assertion that fails against the base, not an import that crashes) → restores every journaled file byte for byte, **verifies each sha256**, deletes the journal (in a `finally`, so an ordinary error restores too) → when the red step was `RED`, runs again ⇒ `GREEN`. A characterization test (`proof: characterization`) is proven by assertion deletion. The ledger `proof` row records `mechanism` and `red_kind`; one `RED` + `GREEN` pair per proven test. SIGTERM mid-run leaves the journal; `forge proof restore <block> --run <r>` restores the exact hashes and refuses when no journal exists.

## §3 The measurement export (R13) — `proof.isolation: export`, the default

Parallel blocks share one working tree, so nothing is measured there. `forge proof export <block> --run <r>` builds `.code-forge/export/<block>/`: (a) `git archive <base_sha>` extracted; (b) every path the base's `.gitattributes` marks `export-ignore` restored from `git show` — the hazard is real: repositories mark `/.github` and `CHANGELOG.md` that way; (c) every path in `proof.export.copy_untracked` that exists in the main tree added (`.env`, `.env.testing` by default; never anything under `.code-forge/`); (d) the block's owned files at their **current** content — new files included, and an owned file that existed at the base but is deleted in the tree is **removed from the export** too (listed under `removed` in the export's result), so a deletion is measured as a deletion; (e) the dependency dirs in `proof.export.link_dirs` symlinked read-only. Gates, red→green and the test-count delta run there; `--remove` deletes it at block close. A gate command that fails inside the export with a missing-file error is reported as `export.incomplete: <path>` with the sentence *add it to `proof.export.copy_untracked` or set `proof.isolation: lock`* — never as a red test.

**`proof.isolation: lock`** is the fallback for stacks whose tests cannot run from an export: `forge proof lock <block> --run <r>` takes a run-wide lock (Solo `lock_acquire` when present, else a lock file) that pauses other blocks' gate runs (`proof.busy`); the runner works in the shared tree with the journal; `forge proof unlock <block> --run <r>` releases it. The ledger flags `isolation: lock`.

Test-count delta = the count on the block's filtered run in the export minus the same filter at `base_sha` (a second export, cached per base). The full suite runs only above `gates.full_suite_threshold_files`, also in the export.

## §4 Verified and unverified stacks

The export is proven on the package's own example repository (a Node library with a `node:test` suite, `/.github` and `CHANGELOG.md` restored from `export-ignore`). A PHP/Pest or Vitest stack is **UNVERIFIED** until a real second consumer runs it; `proof.isolation: lock` is the documented fallback for those until then. `doctor` says which gate commands are on PATH.
