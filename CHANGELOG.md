# Changelog

All notable changes to `@codedology/code-forge` are recorded here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions follow semver ahead of a 1.0.0 that
waits on a second real consumer (plan §10.4, Q17).

## [Unreleased]

## [0.22.1] — 2026-11-11

### Added

- `block close` runs in Go for an ack digest in Node's Latin `localeCompare` order, an object or array `decision` (Node's key order), V8 `Date.parse` rollover and 2-digit date forms, and late or cap rows whose file is empty, a number or false or whose finding is missing or not text; on a shared terminal its stderr lines (auto-claim, WARN, refusal summary) come before the stdout result as in Node, and `--auto-claim` claims before a later transcript or `--worker-pid` refusal as Node does; non-Latin ack digests, object or array late/cap values and out-of-range legacy dates still hand over (B498)
- `block close --auto-claim` runs in Go on a run with an autopilot record and with `close.require_human` at a terminal (same prompt text and answers; a cancel records nothing); a transcript that runs the `hooks.on_block_end` command is Node's `rule_break` in Go `block ready` and `block close` (Go close skipped this check before and closed where Node refuses); late findings with no file, legacy `toUTCString`/`toString` review dates and non-ASCII ack digests run in Go; an object decision, an unknown date form and an order-dependent ack digest still hand over to Node with their own reason (B493)
- `block close` refuses a malformed, missing, not-a-directory, other-branch or foreign block worktree and one without the run's workspace directory in Go with Node's messages, reads ledger null and scalar rows and an unreadable rotated ledger file like Node, and reads the ledger before the run-ended check; `block open --brief` prints Node's realpath and read errors; `block stop` with a daemon socket runs in Go and its done file takes the serving daemon's rows; `block land` prints Node's read error for a run file it cannot read (B470).
- `keys list`, `keys set --cache-hours`, `keys test` and `keys remove` (file key store), `report --slug` (text, `--json`, `--export`, all 18 sections) and `doctor --help` run in Go with the same output, exit codes and files as Node; 1Password and keychain writes, a typed `keys set`, every doctor check run and ledger rows whose bytes depend on JavaScript coercion still hand over to Node (B456)
- `ledger calibration`, `ledger outcome` (`--pr`/`--ci` and `--scan-git`), `ledger add coder` and `ledger outside-review` run in Go with Node's output, exit codes, signed rows and error-log entries; `logs clear` (no log, `--yes`, no-terminal refusal), `logs report` with nothing to report, and `logs` help, unknown subcommands and usage errors run in Go; the clear prompt on a terminal and a report that would file an issue hand over to Node with their own reasons (B457)
- Go `worker.TicketRunner.RunTicket` routes a ticket like Node (`review.routes`, adaptive L2 routing from the ledger, signed `review.route` rows, `route-invalid` and `route-row`) and serves the engine's first refusals (`review.exclude`, unreadable run record, unknown block, missing base, malformed worktree, amended acceptance), byte-equal to Node; tickets that need a review session still return unsupported with nothing written, and `worker` still hands over (B492)
- Go `native/worker` runs one ticket's refusal outcomes (`TicketRunner.RunTicket`): bad ticket, bad path, other run, stale or missing file, worktree mismatch, unreadable file, gone or malformed worktree, unreadable run record and missing config snapshot, with Node's file lock, signed result, ticket timings, done marker, signed `review.result` row and poison rule, byte-equal to Node; tickets that need a review (routing, risk and facts, fix loop) return unsupported with nothing written, and `worker` still hands over (B488)
- Go package `native/worker` holds the review worker's boot and ticket pool (boot lock, registry admission, pins and the signed `worker.reattach` row, announce and heartbeat files byte-equal to Node, clean stop, queue order, Node's ticket claim lock, `parallel_tickets`, `provider_parallel` and `fair_share`); the per-ticket review is not ported yet, so `worker` still hands the queue over to Node (B485)
- `spawn --run <id>` runs in Go for the roles Go serves without `--run`: the run's temp root, then the run-pinned `budget.usd` and autopilot category gate before each step (Go package `native/session`), with the same refusals, stderr, exit 3, `budget.refused`, `budget.warning` and signed `autopilot.pause` rows, saved pause and error-log warning as Node; `--run` with the coder role and `--background` still hand over (B482)
- `spawn` runs a foreground closed-book session in Go (Go package `native/session`: argv, fallback ladder, rate-limit retries, hard and silence timeouts, pid registry, answer and usage parsing, the ledger row) for the reviewer, judge, s2, author and delegate roles on anthropic and xai steps with no schema or the finding schema, and `--print-argv`, with Node's stdout, stderr, exit code and row; `--run`, `--background`, the coder and facts roles and other schemas still hand over to Node; `review.Deps.Spawn` can now run on this session (B477)
- Go can review one ticket on the single-reviewer path (`native/review`: packet, one L2 session through a spawn seam with the same-model timeout retry, the stub guard, the `review.plan`, `review.unavailable` and `review.session_timeout` rows and Node's result object), equal to Node on 25 tickets; a case it does not cover is refused before a session starts, or stops with a gap error when it shows only after the session (`needs_file`, a second same-model timeout, a schema miss with a ledger), and the `review` verb still hands over to Node until B477 wires it (B476)
- `worker`, `spawn` and `review` run in Go for help, usage errors, unknown flags and the refusals Node makes before work starts (a bad or ended run for `worker`; config, `--timeout`, `--schema`, run id, level and role for `spawn`; no repository, a bad base or `--files` path for `review`), and `review` with nothing left to review runs fully in Go (text and `--json`, with the skipped-file notes); serving the queue, the session and a review run with files still hand over to Node with their own reasons (B460)
- `run status`, `run end` and `run start --reattach` (with or without `--worker-pid`) run in Go with the same output, exit codes, run record, mirror and signed ledger rows as Node, as do `run` help, usage errors and unknown flags; a plain `run start`, `run start --add-worker` and `run reload` still hand over to Node with their own reason (B454)
- `block close` and `block ready` read Codex `exec --json` transcripts and decide the `code-forge-reviews-write` rule in Go with Node's hits, an unreadable `--transcript` prints Node's error and exits 1 in Go, and review dates in every ECMAScript date-time form are read like V8; hand-made late/cap rows with no file, object decisions, non-ASCII ack digests and the open, claim, stop and land edge cases still hand over to Node (B466)
- `doctor --quick` now refreshes the cached Jev key in Go like Node: an expired file-store entry is deleted with its index row, an entry kept longer than `keys.cache_hours` is cut (entry and index row rewritten), and an `op://` reference is read with `op read` (one retry after a first-call timeout, logged as an `op_retry` warning) and cached with its index row, with Node's 1Password reasons; only an index Go cannot read exactly like Node still hands over (`doctor-key-refresh`), and the full run stays in Node (`doctor-full-run`) (B483)
- `doctor --quick` runs in Go with Node's text, `--json`, masking and exit codes: config, harness links, Jev key (env or file store) and reviewer rows; `doctor` help, usage errors and unknown flags too; the full run, the keychain store, a key refresh or 1Password read, schema errors, secret-shaped values, sandbox dirs and ledger rows Go cannot read exactly still hand over to Node with their own reasons (B474)
- `plan check` (facts, slug, config caps and budget, ledger lanes, claims, overlaps, cycles, imports) and `upgrade` re-pointing recorded installs run in Go with Node's output, exit codes and installs file; odd cases keep their own named hand-overs (B475)
- `validate` and signing a passing `outcome record` run in Go with Node's output, exit codes and signed row; `plan check`, `sweep` and `upgrade` answer help, usage errors and bad files in Go, while the plan check itself, the process scan and re-pointing installs still hand over to Node with their own reasons; schema errors, secret-shaped values and sandbox dirs in `validate` hand over too (B471)
- `resolve`, `models`, `list`, `remove`, `pipeline status|next`, `outcome seal|check` (and the check part of `outcome record`) and `tools [install]` run in Go with Node's output, exit codes and files, including help, usage errors and unknown flags; YAML syntax errors, schema errors, files Go cannot write, signing a passing `outcome record` and the `tools install` yes prompt still hand over to Node; `validate`, `plan check`, `sweep` and `upgrade` still run in Node (B455)
- `gates run` runs in Go for every form Node completes: a run with no `--run` and no `--block`, custom, kind and formatter gates (pint and prettier in argv batches), `repeat_new_tests` with changed sources, JS `Number` forms of `--timeout-ms` (`0x`, `0o`, `0b`, exponent), unknown flags, no `HOME` (the user's home folder), a `--timeout-ms` over the timer limit (1 ms timer and Node's warning), an empty `--run` or `--block` and a missing `--cwd`, all as Node; a bad gate config stops with one stderr line that names the cause and the fix; only a run record with a mismatched `run_id` or no `workspace` still hands over; a command that cannot start is a red `spawn-error` gate; the `block land` gates step and the batch land preflight run the same Go path (B450)
- `block open`, `block claim`, `block stop`, `block close` and `block land` no longer hand over to Node: help, unknown flags, errors and the error log run in Go, `block close` checks light limits in Go, and every other case Go does not serve exits 1 with one `block <sub>: refused: <cause> (<code>); <fix>` line and writes nothing; only the land gates step still hands over (B450) (B451)
- `block open`, `block claim`, `block stop`, `block close` and `block land` serve help, unknown flags, errors and the error log in Go; `block close` checks light limits in Go, reads only the closing block's rows (another block's rows no longer hand the close to Node) and prints Node's detail for a `rule_break` row whose id is not text; no case is refused, and the cases Go does not serve yet still hand over to Node (B466 ports them) (B451)
- **`npm run release:site`** — after `npm publish`, waits until npm serves the version, builds the site data in an
  isolated worktree of the site repo, runs every site gate, commits only `src/data/site-data.json` on
  `release-site/v<version>` and deploys; a red gate deploys nothing; `--dry-run` stops before commit and deploy (B315)

### Fixed

- `gates run` hands a gate command that is an executable file with no `#!` line to Node again (Node runs it with `/bin/sh` on Linux and refuses it on macOS; Go had shown a red `spawn-error` gate); `context pack` names `node` on `PATH` in the pack header when `CODE_FORGE_NODE` does not resolve, as before B452, instead of refusing; the Go test for `gates run` with no `HOME` no longer writes `~/.code-forge/queue` in the real home; the Node parity tests now check the B449–B452 Go forms against Node instead of expecting a hand-over (B469)
- **status, review-file, context: fewer Node hand-overs** — Go runs every `context` form, `status` for array run records, an infinite spend and broken run files, and the `review-file` enqueue worktree and `run-record-unreadable` refusals; Node stays only for a non-ASCII block id in `status` and a few review-file states Go cannot read (B452)
- `block rebase` runs in Go in every form (`--help`, closed and awaiting blocks, `--head`, `--accept-changed`, `--accept-landed`), and `block ready` runs in Go with `hooks.on_block_end` and with `close.require_human` under an active autopilot grant; only run state Go does not expect still hands over to Node (B449)
- The config and command references now describe the merge gate as it runs: `gates.merge_gate` runs the Go test selection for the merge, never the full suite, reuses a green gate on the same tree (`gate_reused`) and is always on in a batch; `block land` usage lists `--push`, `--watch-ci`, `--copy-links` and batch land, and `gates run` usage lists `--full`, `--preflight` and `--no-preflight` (B444)
- `npm run release:site` deploys the merged site folder `docs-site/site/.pages-out` (marketing pages plus `/docs`)
  instead of `dist`, which has no `/docs`; `--deploy-dir <path>` picks another folder inside the site root, and a
  missing folder or `index.html` after the gates stops before commit and deploy (B446)
- `block land` output no longer says the merge gate runs the full suite: `--no-merge-gate` warns `merge gate skipped; the merged tree was not checked`, a batch refuses it with `a batch always runs the merge gate; --no-merge-gate is refused`, a red gate stops with `tests failed:` and a missing test command with `test gate unavailable: configure gates.test before merging`; `help` lists every `block land` flag and batch land (B447)
- `code-forge ci watch` survives the GitHub API rate limit: a rate-limit reply is never a CI result; it waits until the reset from `gh api rate_limit` (else backs off 60 s doubling to 15 min), prints `ci watch: GitHub API rate limit; retry at HH:MM UTC` per wait, and past `--timeout` exits 4 with `rate limit, gave up` instead of a failure; polling stays at one call per 30 s, and a second watch for the same repo and commit exits 0 without starting (B448)

## [0.22.0] — 2026-11-11

### Added

- **`gates.merge_gate_links`** — repository-relative git-ignored paths (for example `local.properties`,
  `app/google-services.json`) are linked from the main checkout into the temporary merge-gate tree before setup; each
  link must stay under the repository root. A retried land merges only the closed block head (B343b)
- **`block open --allow-api-mismatch`** — block open (Go) checks the brief's uses/extends lines against the code
  (api-fit). A missing name or parameter exits 2 with an `api-fit:` line; `--allow-api-mismatch` opens anyway and prints
  each line as a note (B338)
- **`code-forge cloud-coder --run <r> --block <id>`** (Go) — runs a cloud coder from config `coder.cloud` (submit, then
  fetch until a patch or timeout), applies the patch to the block's owned files only and queues review; a patch fetched
  at the deadline still lands (B342)
- `code-forge report` has a pace section: per-block and run pace in minutes; text, `--json` and `--export` carry the
  same 18 sections (B341, B358)
- heal queue: gates run, block rebase and block land queue a failure that passes by itself (a held lock, a transient git
  error) in ~/.code-forge/heal/<run>.jsonl and print heal: queued <id>; code-forge heal list|show|run retries each item
  with backoff (15 s, 60 s, 240 s), removes a lock whose owner pid is dead, and marks needs_coder after --max-tries
  (B429)
- Main first: `block land --push` (or `land.push: true`) pushes main after the merge; a rejected push queues a heal item
  of kind land (argv `block land <ids> --run <r> --merge --push`; already landed blocks only push) and exits 1; `--watch-ci` starts a detached `code-forge ci watch`, a new Go verb that follows the
  commit's GitHub Actions runs and queues a heal item of kind ci naming the failing jobs, tests and block ids (B430)
- Under an active autopilot grant an agent may run block close --decide without a terminal for transcript rule_break
  hits and warning/nit findings; criticals stay with the owner; each decision is a signed close.decision row shown by
  the new code-forge decisions list (B390)

### Changed

- the skill's `block land` text no longer says the merge gate runs the full suite: it uses the Go test selection
  (B404); new `skill/references/code.md` §5.1 tells the orchestrator how to use the changed-test selection, preflight,
  close review wait, Go rebase replay, batch land, `--push`/`--watch-ci`, the heal queue and the machine test queue,
  with pointer lines in SKILL.md (B443)
- native `block close` queues a review for each changed owned file that has no review (also after an acceptance
  amendment), waits once for all of them (`review.close_wait_s`, default 600), then stops with `not_approved` or
  `review_pending`; close never approves a file itself (B339)
- A Go verb that holds the run lock writes an owner file (pid, start token, verb) in `<run>.lock` and removes it on release; a waiting Go verb breaks a lock whose owner process is gone or replaced (or, with no owner file, that is older than 10 minutes) once, logs `broke stale run lock (pid N)` and takes it, so a crashed holder no longer blocks every verb until a human removes the dir; a lock with a live owner is never broken (B423)
- `block land` (Go) no longer stops with "land stopped at join: reviews did not finish" when a loaded machine misses the run daemon's fixed 2 s connect/ping deadline: the review join asks the daemon again until land's own `--max` deadline (then `wait_timeout`); a dead daemon (connect refused, connection closed, invalid answer) still stops the join at once with the same message (B417)
- `block close` and `block ready` (Go) check the `code-forge-runs-access` transcript rule themselves instead of handing any transcript with `.code-forge` text to Node: plain-text logs and Claude Code JSONL logs get the same hits as Node after B415 (a Read/Bash/Glob/write-tool path or command word naming the run records, absolute or home-relative, hits; a heredoc body or quoted `echo` text a command only writes to a file does not; `.code-forge.yml` does not). Go also decides the git/gh/rm and code-forge launcher rules, `sh -c` wrappers, interpreter scripts and non-ASCII text, so a `coder-forge` path or `forge/r-…` branch name no longer hands over; only Codex logs, `.code-forge/reviews` and `.code-forge/queue` text, invalid UTF-8, lone surrogates and JavaScript-only line separators still hand over (B416)
- Go gate children start at nice 10 (or the engine's higher nice) before their own code runs: the engine starts each gate through a short copy of itself that sets its own nice and then execs the gate program with the same argv, environment and spawn errors. Before, the engine lowered the child after start, and on macOS that change could be lost while the child was still starting, leaving a gate (and the nice test) at nice 0 (B420)
- `gates run --changed` and the `block land --merge` and batch-land merge gates (Go) run only real test files (`*.test.*`, `*.spec.*`, `*_test.go`, `*Test.php`, `test_*.py`, `*_test.py`, `*Tests.swift`): a changed helper, fixture or script under `test/` is no longer run as a test or picked as a stem-paired test, and selects the tests that import it or name it by path instead. The owned-file test fallback, the no-JS changed scope and `repeat_new_tests` skip such files the same way (B394)
- `gates run --changed` and the land gates select the CLI tests for a change to any verb module: Go's import graph counts `bin/code-forge.mjs` as importing every `src/cli/*.mjs` its router loads by readdir, and `bin/code-forge` as importing the entry (a change to `src/cli/version.mjs` selected 0 tests before) (B373c)
- `gates run` (Go, every mode: `--changed`, full, `block land` and batch merge gates) runs `lint`, `types` and `format` before the test gate. When one of them is red the test command never starts: its entry stays in the result (same order) with `skipped: true`, `ok: false`, `reason: pre_gate_failed` and `pre_gates_failed` naming the red gates, `gate.done` records the same reason, and the exit code stays 1, so two type errors no longer cost a 12-minute suite first (B418)
- `block land <id> --merge` skips the merge-gate run when the merged tree equals the clean tree the block's last green gate ran on and that gate covered the merge selection: `merge gate: reused green gate on tree <id> (gate_reused)`, `merge_gate_reason: gate_reused` in `land.timing` and the `--json` `landed` event; a block on an older base still runs it. `gate.done` rows from a block worktree record the `tree` they ran on (B393)
- Batch `block land <id> <id>...` (Go) runs `git merge-tree --write-tree` for every block against the running result before any rebase, gate or suite. A block that would conflict is parked with reason `block_rebase_conflict` and its conflicting files (`--json` `event: parked`, `land.batch` `parked`); the other blocks still gate and land. When every block conflicts, nothing lands and land exits 1 naming each block (B397)
- Go tests that build a fixture repo (`gates run`, `gates` go-only scope, `review-file --wait`/`--wait-block`, `review wait-block`, `status` direct) run fixture git in the fixture repo without inherited `GIT_*` variables and start their code-forge children without them, so a `GIT_DIR`, `GIT_INDEX_FILE` or `GIT_WORK_TREE` from a hook or worktree no longer breaks them; a guard test reruns them with those variables set (B410)
- Go tests in `native/gates`, `native/blockops`, `native/blockopen` and `native/gatecheck` run fixture git with the fixture repo as the directory and `gitx.ChildEnv()` (no inherited `GIT_*`), with a guard test that reruns them under `GIT_DIR`/`GIT_INDEX_FILE`/`GIT_WORK_TREE` pointing nowhere; `cloud-coder` takes its deadline from a replaceable `cloudDeadline`, so the fetch-timeout test expires after two polls instead of 2 s of wall time and no longer fails on a loaded machine (B411)
- `gates run --run R --block B --changed` (Go) selects its tests in Go and never runs or hands over to the full Node suite: the block's tests, owned and changed tests and the stem-paired tests of changed sources (any language), every test that imports a changed file (static and dynamic imports, `require`, `new URL`, path mentions anywhere in the closure, imports of deleted files), for a native change `test/compat/go.test.mjs`, the compat tests named after the Go files and every test that builds or runs the native binary (B330), and the config golden test for a fixture, schema or `src/config/` change (B409). A changed test the runner cannot take is dropped, never widened; an empty selection skips the test gate. The reason is `go_only` or `import_graph`. The `block land --merge` merge gate and the batch-land merge gate use the same selection (`merge gate: scoped tests passed (...)` for `--merge`) instead of the full suite; a test command without `{test_files}` gets the selection appended and never runs unscoped; `go.test.mjs` runs only for a native change. `gates run --run R --block B --full` is the only local full suite (`scope: full`, `reason: explicit`) (B404)
- In a code-forge worktree, `block rebase` rebuilds the worktree's native binary (`node scripts/build-native.mjs --host`) when the rebase brings a change to `native/**` or `scripts/build-native.mjs`, and `gates run --run --block` rebuilds it before any gate when it is missing or older than `native/go.mod` or a non-test `native/**/*.go` file (the gate JSON shows `native_rebuilt: true`). A failed build exits 1 naming `native-rebuild-failed`; the gate then runs nothing. Compat tests no longer run a binary built on the old base (B408)
- The close transcript check no longer counts a `~/.code-forge/runs` path that a coder only writes as text: the body of a `cat > file <<'EOF'` or `tee file` heredoc (not piped on, no shell substitution) and quoted `echo`/`printf` arguments written into `plans/` give no `code-forge-runs-access` hit. Real access still does: `cat`/`ls`/`cd`/`<` on the path, a Read or other tool on it, a heredoc fed to a shell or piped on, `$(…)` in an unquoted heredoc, and now also the home-relative spelling `.code-forge/runs` (B415)
- New `block ready <id> --run <r> --preflight` (Go) lists in about a second, with no gate result, the close blockers a gate cannot fix: `stale_base` (with the base branch tip sha and the `block rebase --accept-landed` fix), open review findings, `rule_break` and, with `--report`, `coder_report_failed`; exit 0 clean, 1 with the list. `gates run --run --block` runs the same preflight by default and stops before any gate command, one stderr line per blocker; `--no-preflight` skips it (B392)
- `gates.extra.invariants` is now refused instead of silently ignored (no engine ever ran its script): config validation rejects it as an unknown key under `/gates/extra`, and Go `gates run` (scoped, unscoped and inside `block land`) exits 2 before any gate runs with one line naming the key and `gates.custom` as the way to run the script (B402)
- Batch `block land <id> <id>...` no longer runs a full suite per block when a block's changed scope needs the full fallback: it writes a signed `gate.deferred` row (scope `deferred`, reason `merge-gate`) and the one merge-gate suite with its prefix bisect still names a failing block. A batch where no block owns or changes a Node product file (`src/**`, `bin/**`, `scripts/**/*.mjs` except `scripts/build-native.mjs`, `package.json`, `package-lock.json`) runs no full suite at all; `land.batch` records `merge_gate_reason` `go_only` or `node_product` (B398)
- `gates run --run R --block B --changed` no longer falls back to the full Node suite for a Go-only block: every owned path is under `native/`, `docs/`, `plans/` or `.github/`, a runnable test under `test/`, a `*.md` file or `scripts/build-native.mjs` (no `src/`, `bin/`, other `scripts/*.mjs`, `package.json` or `package-lock.json`). Go runs the test gate once with the block's tests, `test/compat/go.test.mjs` and the compat tests named after the block's Go files (`status_direct.go` → `parity-status-direct`); the result and `gate.done` carry `reason: go_only` and `gate.done` lists `test_files`. Blocks with a Node product file keep the old scope; `block land --batch` keeps its own plan (a full fallback defers to the one merge gate, B398) (B399b)
- `block ready` and `block close` (Go) find review tickets by themselves: under `--report`, a changed file with no `reviewed <file> ticket <id>` line is credited by the latest done review-file ticket of the same run, block and file for its current content hash. A ticket for older content still fails with `coder report FAILED: no review-file ticket id for <file>`; report ticket lines stay optional and still win (B405)
- A tracked `.gitattributes` sets `CHANGELOG.md merge=union`, so blocks that each add a CHANGELOG line at the top of `[Unreleased]` merge and rebase without a conflict on every clone and in CI (B396)
- `cloud-coder` and `docs` now have thin Node shims: with `CODE_FORGE_NATIVE=0` (or no Go binary) `--help`/`-h` prints the same usage as Go and exits 0, any other call prints one line naming the missing Go binary and exits 1; with the Go binary the call is handed to it. `help` and the command reference list them from `src/cli` like every other verb; a Go test fails when a new Go route is missing from help (B386)
- New config `gates.test_class_map` (rules `{glob, strip_prefix, strip_suffix, separator}`) maps a block's own test files to a test-class filter: an argv item holding `{test_classes}` in a fixed gate, e.g. `-Pandroid.testInstrumentationRunnerArguments.class={test_classes}`, gets `a.b.FooTest,a.b.BarTest` with `gates run --run --block`, and is dropped (full suite) when no test matches; paths outside every rule are ignored. New Go verb `code-forge gates classes --run <id> --block <id>` prints the filter (B335)
- A failed land suite (single or batch, changed gates or merge gate) now names up to 10 failing tests from the node spec reporter's `✖ failing tests:` list as "name (file:line)" as well as TAP `not ok` lines, and saves the whole suite output to `~/.code-forge/runs/<run>/land-logs/<block>-<time>.log` with one line `full log: <path>`, so no 20-minute rerun is needed to find the failures (B374)
- `review-file` enqueue, `--wait` and `--wait-block` run in Go without Node: `--help`, mixed flag order, `--cwd`, usage errors (exit 2), the no-repo refusal (exit 1) and the block-scope refusal match Node byte for byte. With the daemon off, unreachable or giving an invalid answer, `--wait-block` joins in Go, enqueues missing tickets with Node's ticket bytes and prints Node's JSON. A live worker with a stopped review that has findings still hands over to Node (`wait-block-stopped`), before any queue write (B382)
- Go `block land` and `block close --auto-claim` now refuse to auto-claim `.code-forge.yml` while `hooks.on_block_end` is set, with the same reason, order and text as Node (B385)
- `gates run --run R --block B` checks the block's base first: when the block's base branch moved past its recorded base, no gate runs, it exits 1 at once with one line naming the fix `code-forge block rebase <id> --run <R> --accept-landed`. `--auto-rebase` runs that rebase and then gates; `--allow-stale-base` gates on the old base with a WARN line. Go and Node behave the same (B363)
- Facts, not guesses (owner rule): every coder context pack, Node and Go byte-equal, ends its proof rule with a bullet that every reported cause, pass or fail names its proof (error text, log path, or command and exit code); the skill gets hard rule R18 "Facts, not guesses" for the orchestrator, so there are now eighteen hard rules (B362b)
- New Go verbs `code-forge docs sync|path [--json]`. `docs sync` fetches the published `llms-full.txt` and keeps it in `~/.code-forge/docs/` only when its `version:` line equals the installed version; otherwise, offline or on an HTTP error it keeps the packed copy and still exits 0. `docs path` prints the best copy, or exits 1 when there is none. `upgrade` runs `docs sync` best effort after a successful upgrade (B348)
- The close transcript check reads Claude Code sub-agent JSONL logs by shape: only tool_use Bash commands, write paths and other tool inputs are scanned, so tool help, tool_result text and the coder prompt no longer give false rule_break hits; a broken or mixed log still falls back to the plain fail-closed grep (B361)
- Test runs wait in one machine-wide FIFO queue: `gates run` takes a full or changed slot before its test command, changed runs share `max(1, min(cores/4, free GB/2))` slots, and a killed holder frees its slot within 2 seconds. `code-forge queue` shows holders and waiters, `code-forge queue run -- <cmd>` queues any other test command, `status` shows `queued #k`, and `CODE_FORGE_TEST_QUEUE=off` turns it off (B352)
- `code-forge worker --run <id>` re-pins the run to itself when every pinned worker is dead (no process, or another start time), with the same `worker.reattach` ledger row as `run start --reattach` and one stderr line `worker: re-pinned run <id> from dead pid <old> to <new>`, so a block close after a worker restart no longer stops on `worker.replaced`; a pinned worker that is still alive and not serving this queue is never replaced (exit 1) (B355)
- The anthropic default L3 model is now `claude-fable-5-2` in Node and Go; `claude-fable-5-2` is a known model id, and `claude-fable-5-1` stays known, so configs that name it still validate (B353)
- Every context pack (Node and Go, byte-equal) has a `## forbidden` section: one line per rule the close transcript scan enforces for a coder, built from the rule table, then the safe ways for a red run on base code, clean-up and run facts (B364)
- `block close` no longer refuses `unreviewed` for an owned file the block did not change (equal to its base) when every review of its current content is a `no_change` result, as after `block rebase --accept-landed` onto a main that changed it. A changed file, or one whose current content has a refusal with findings, still needs an approval. Such a file is not counted as work either, so a block with no other change still refuses `no_changes`; Node and Go agree (B365)
- Three tests no longer fail on a busy machine: the Go torn-tail recovery test is bounded only by `go test -timeout` (not 20 s) and `test/compat/go.test.mjs` gives Go 10 minutes and prints every output line of a failed Go test; `native-install` runs `npm pack` on a private package copy, so a file another test adds to and removes from `native/bin` no longer breaks it (B375)
- The package test suite (`npm test`, `test/helpers/run-suite.sh`) runs one test file per core when it holds the machine
  test slot (`CODE_FORGE_TEST_QUEUE_HELD` set by `gates run` and `code-forge queue run`), and keeps `max(1, cores/2)`
  outside a slot; an explicit `--test-concurrency` or `CODE_FORGE_TEST_CONCURRENCY` still wins (B376)
- `block close` and `block ready` judge review caps, reopened and unwaived findings, agent and autopilot waivers, `review.agent_waivers.require_ack` (`agent-waivers-not-acked` until a signed human ack row), accepted rule hits, old result-file rows and an amended acceptance in Go, with the same verdict and output as Node. Close prints the agent waiver, agent reopen and accepted hit lines and the `agent_waivers`, `agent_reopens` and `accepted_hits` JSON keys only when they are not empty. `block close --help|-h` and `block human-pass --help|-h` print the usage in Go and exit 0. Rows or dates Go cannot read exactly like Node still hand over as `close-gates-format` (B380)
- Batch `block land <id> <id>...` (Go) lands closed blocks as one checked batch: blocks that did not land are restored,
  landed ones are marked, and a bad `--max`, a bad timeout or a duplicate id is refused (B340)
- Gate child processes run at low priority (nice 10), and tests share one cached Go test binary (B354)
- `gates run` keeps the full test scope when the block's base moves in an unsafe way or a baseline is rejected;
  text-only blocks stay scoped (B318R)
- The run lock now guards only a short read-modify-write of the run record (Go): batch land (rebases, gates, merge
  gate), block rebase and the rebase check work on a snapshot under the block's own lock and commit only their blocks in
  one short write, so verbs on other blocks of the run never wait; a same-block change meanwhile stops the verb with
  block-changed, a second long verb on the same block with block-busy (B426)
- The changed-test import graph is narrow (Go): comments make no edge, named imports are followed through re-export
  barrels and to the exports of a module that reach the change, and the new gates.import_graph.stop list ends the
  closure at hub files; this repository stops at 5 measured hubs, taking the B362b+B402 batch from 348 of 380 tests to
  65 (B427)

### Fixed

- parity tests follow the Go-only behaviour of B393 (gate `tree` field, merge-gate reuse), B394, B418 and B339, so CI
  is green again; Node parity is still checked for every other field (B437, B439, B440)
- **`block rebase` replays first and refuses only a real conflict** (Go) — owned files main changed no longer refuse
  up front: a clean replay moves the base, names them `upstream_owned` and queues them for review again; a real conflict
  restores HEAD and refuses `rebase-conflict` naming the files; `CHANGELOG.md` lines both sides only added are kept (main
  first), also under `merge=union`, in `--rebase-check`, `--accept-landed` and batch land (B431)
- `npm run release`: when local `npm test` fails, only the failing files rerun alone; the release goes on only when each
  passes alone and the CI run for HEAD is green (`local flaky (passed alone, CI green): <files>`). Worker test deadlines
  scale with host load (B345)

### Removed

- Three Node modules that nothing ever imported, with their tests: `src/engines/alias-map.mjs`, `src/engines/isolation.mjs` and `src/gates/invariants.mjs`. `src/config/schema-paths.mjs` stays, because the config schema tests use it (B400)

## [0.21.0] — 2026-11-09

### Added

- `npm run docs-block` writes the docs brief for a release and stamps `docs/release-docs.json` when the docs block ends. `npm run release` refuses when the docs stamp is not current (B312)
- `node scripts/gen-site-data.mjs --tag vX.Y.Z` exports release notes and commands from the tag for a local website update. It exports only run, completed-block and spend totals from the local ledger. The same tag and ledger inputs give the same JSON (B313)

### Changed

- `block land` does not run the full test suite again in the merge gate when the merged tree is the same as a tree that already had a green full run. It prints the reused tree id and records it on the land.timing row (B317)

### Fixed

- A review that asked for a file the policy refuses now goes on to round 2 with the refusal line, instead of stopping as unavailable. The worker's review ledger row keeps only the needs_file diagnostics, never finding text (B311, #33)
- `parity-gates-custom` no longer fails at random under load: the test writes its fixture files completely before the Go engine reads them (B309)

## [0.20.0] — 2026-11-06

### Added

- The Go engine records each hand-over to Node in `~/.code-forge/native/handovers.jsonl`: the command, the known subcommand and a reason code. Unknown words are stored as `unknown`, never as typed. Set `CODE_FORGE_HANDOVER_LOG=0` to turn it off (B282)

### Changed

- More commands run fully in Go, with the same output and exit codes as Node. `docs/native-engine.md` lists the few cases that still hand over. These cases no longer hand over to Node:
  - `block close` with `--auto-claim`, `--remove-worktree` and `--no-require-reviews` (B283), and with external review (B284)
  - `hooks.on_block_end` on block claim, stop and close (B284)
  - active autopilot grants on block claim, stop, open and close (B285)
  - `block open` with `--after`, `--after-outcome` and no `--level` (B286)
  - `block ready`, `block waive` and `block reopen` (B287)
  - `review-file` with `--fresh`, `--answers`, `--of`, `--rebase-check` and a cached result that needs a fresh ticket (B288)
  - `gates run` with custom gates, `repeat_new_tests`, light blocks and flaky detection (B289)
  - `context pack` (B290)
  - `block land` with plain, `--merge`, `--no-merge-gate`, grant and hook shapes (B291, B307)
  - `block close --decide`, `block human close` and `block human-pass` (B292)

### Fixed

- `npm run bench:block` counts every command it starts, so the process count no longer drops below the number of runs when fast Go commands end between two samples. This made CI fail on Linux (B297)
- A review recheck and the agent waiver judge now see the block's other changed files that a finding names or calls. The context is bounded and recorded. Before, a fix in another file of the block was not seen (B280, B306)
- `gates run --changed` keeps its scope after a rebase when the base move is safe, so one block no longer runs the full suite twice. It resets the scope when the base move is not safe and records the real fallback reason. A change in Go code now selects the affected parity tests (B294, B305)
- `gates run --changed` no longer passes Go `_test.go` files to the Node test runner. Before, every Go-only block failed to land (B301)
- A reviewer that times out twice on the same file now falls back once to another configured model, chosen by measured speed and accuracy. `run reload` now applies review-only keys when frozen keys differ, keeps the frozen keys and prints their names (B296)
- `block rebase --accept-landed` no longer deadlocks on the run lock, shows the Go error, leaves no lock after a refusal and no longer replays the commits of an earlier block (B299)
- A stopping worker no longer creates a removed `HOME` again (B303)
- The merge gate of `block land` now runs in the right folder when the workspace path goes through a symlink, in Node and in Go (B307)
- Tests no longer fail on a busy machine because of wall-clock limits (B300, B302)

## [0.19.1] — 2026-11-05

### Fixed

- `code-forge init` asks for a level again when the answer is unreadable, is empty, or repeats the provider, model and effort of an earlier level, the L3 judge included. Before, init went to the end and stopped with "the resulting config is invalid, nothing written" (#31, B281)
- The forbidden-command scan reads Codex coder logs again when they start with the plain "Reading additional input from stdin..." banner. The coder's stderr now goes to `session.err.log`. Before, the banner switched the scan to plain text: brief text in command output gave false hits, and a real `git stash` inside an escaped shell command was missed (#32, B293)

## [0.19.0] — 2026-11-05

### Added

- Go write kit: file locks, signed ledger rows and atomic state writes with the same bytes and lock rules as Node. No public verb (B272)
- Go runs every `status` form (`status`, `--json`, `--run <id>`) directly from disk, with no daemon and no lock; unreadable or unknown state hands over to Node (B273)
- Go runs `review-file <file> --block <id>` (plain enqueue); `--fresh`, `--answers`, `--of`, `--rebase-check` and fresh-ticket cases hand over to Node (B274)
- Go runs `block claim` and `block stop`; autopilot grants, pending approvals and `hooks.on_block_end` hand over to Node (B275)
- Go runs `block open` with an explicit `--level`, including worktree creation; no `--level`, `--after`/`--after-outcome` and autopilot cases hand over to Node (B276)
- Go runs the plain agent `block close`; `--decide`, `--no-require-reviews`, `--auto-claim`, `--remove-worktree`, `ready`, human-close, external reviewer, hooks and autopilot cases hand over to Node (B277)
- Go runs `gates run` for built-in gates, with the same rows on cancel (SIGINT/SIGTERM); custom gates, `repeat_new_tests`, light blocks, flaky detection and other `gates` subcommands hand over to Node (B278)
- Go runs `block land` (join, gates, close, merge) when every step can run in Go; it checks this before any write, so one land never mixes Go and Node steps (B279)

### Fixed

- `block claim` in Go resolves symbolic links in the workspace path before it checks overlap (B279)

## [0.18.0] — 2026-11-05

### Added

- Go runs `logs path`, `logs [--last] [--json]` and `logs summary`; `logs clear` and `logs report` hand over to Node (B247)
- Go runs `ledger tail`; other `ledger` commands hand over to Node (B248)
- Go runs `review-file --wait <ticket>` (B266), and `review-file --wait-block` through the daemon; stopped files and unusual answers hand over to Node, which writes all signed rows (B267)
- Go runs `status --json --run <id>` through the daemon client `native/daemonclient`, with Node fallback (B268)
- `npm run bench:verbs` compares Node and Go per verb (time, memory, output parity, leftover processes); `bench:block` adds the Go-first `entry` lane (B265)

### Changed

- Land gates run only the tests the block changed or affects; the full suite runs once per wave (B269)
- `block close` finds the final approved review tickets in the ledger, so a report needs no hand ticket lines (B270)

### Fixed

- The CI timing check "Go handover budget rejects a 40 ms delay" no longer fails on a busy machine (B271)

## [0.17.0] — 2026-11-04

### Added

- Go config loader (`native/config`, yaml.v3 pinned and vendored): reads `.code-forge.yml` with the same defaults, schema checks and error lines as the Node loader, checked byte for byte against goldens made by Node (B243)
- Per-verb parity harness: runs each ported verb in Node and in Go on isolated copies of the same state, and compares output and state files field by field (B244)
- `code-forge <verb> --help` for every verb, and a generated command reference `docs/reference/commands.md` (checked by `scripts/gen-commands-doc.mjs --check`); a test fails when a doc names a verb that does not exist (B251)
- The test gate reruns only the failing test files once, one at a time; a pass on rerun makes the gate green and marks those files `flaky` in `status` and `report`. Set `gates.rerun_failed: 0` to turn it off (B255)
- `docs/step-by-step.md`: the whole project workflow, step by step, from install to release (B252)

### Changed

- npm `code-forge` starts the Go binary; unported verbs go to Node; native internal verbs are under `internal` (B242)
- On macOS, `npm test` runs the suite in a sandbox that blocks writes to the real `~/.code-forge` and `$TMPDIR/code-forge`, so a leak fails at once and other runs on the machine cannot cause false leaks; it prints `leak guard: sandbox` or `leak guard: snapshot (<reason>)` (B254)
- The docs explain the Go entry and the Node fallback: `docs/getting-started.md`, `docs/how-it-works.md`, `docs/native-engine.md` (B253)

### Fixed

- A review of a Go file now lists the other Go files of the same package and shows the bodies of the functions its changed lines call, so a reviewer no longer reports a function in a sibling file as missing (B245)
- The test leak check ignores short-lived lock files and live-run control writes made by other code-forge runs on the same machine, fails closed when a process probe errors, and tracks children started with spawnSync/execFileSync, so gates no longer fail on false leaks (B246)
- The leak check ignores atomic temp files of other processes and temp files that vanish during the scan (B250)
- The leak check now sees that a lock file belongs to another live code-forge run on days 1-9 of a month (`ps` pads the date with two spaces), and retries a busy process check before it reports a leak (B258)
- The Go config goldens no longer hold the package version, so a version bump in `npm run release` no longer fails its own test step (B262)
- The Go entry speed test compares the Go start with a matched Node start in pairs, so a busy machine no longer fails it; the 25 ms budget is unchanged (B257)
- `npm run release` now also bumps the version pins in `docs/step-by-step.md`, and when `npm test` fails it lists the failing test names and saves the full log to `.code-forge-tmp/release-npm-test-*.log` (B263)
- A file rerun after a failed gate now finds test names whose duration has a space before the unit, such as `(12.5 ms)` (B259)
- The coder command check no longer flags spawn words that are only data inside a Python `json.loads(...)` literal; a dynamic lookup such as `getattr` or `__import__` keeps the check on (B256)

## [0.16.3] — 2026-11-02

### Added

- `review.silence_timeout_s` (default 60): a reviewer or judge session that prints nothing for this long is killed and counts as a timeout (B206, landed as B240)

### Changed

- A review answer with `passed: null` and findings is read as a failing review with its findings kept; with no findings it is unavailable (schema) and the fallback model reviews the file (B206, landed as B240)
- After a timeout, a silence kill or an at-capacity answer, the next fallback model runs at once instead of a second try of the same model; without a fallback list the old single retry stays (B206, landed as B240)

- CI also runs on a push to a `ci-check-*` branch and by hand (`workflow_dispatch`), so the pre-release check really runs before main moves (B239)
- The queue and parallel review tests check order and state with an injected clock, not short wall-clock deadlines, so they pass on a busy machine (B216, landed as B241)

## [0.16.2] — 2026-11-02

### Added

- CI runs the Go core tests: gofmt, `go vet`, `go test -race` on the go.mod floor (1.22) and the newest Go, and a build of all four native release targets (B237)

## [0.16.1] — 2026-11-02

### Added

- `code-forge block ready <id>` lists every close blocker at once, each with the exact fix command, and writes nothing; `block close` now prints the same full list when it refuses (B205c)
- `code-forge block rebase <id> --accept-landed` (Go core) moves a block onto the new base when a sibling block already landed some of its commits; it credits a commit only when it is in that sibling's own base..branch range, and it puts the old head back on any error after the replay (B220b)
- Under an active autopilot grant, `block open` and `block amend-acceptance` refuse acceptance clauses of `kind: human_check`, so an unattended run never waits on a human check (B222)
- `block land` runs the gates at the same time as the review join, and closes only when both are clean; a finding or a gate timeout stops the gates and never closes or merges. Each land writes one signed `land.timing` row with the join, gates and merge-gate times (B210)

### Fixed

- `block close` leaves out a reviewed file that is now back to its base bytes, and a file the block added and then deleted; a file with other changes still needs review (B228, #30)
- Node and the Go core now use one shared ledger append lock, so both can write the same run at the same time without lost or broken lines (B220a)
- A routed review whose answer breaks the schema now tries the route's fallback models before it gives up; an over-long text field is cut to its limit and recorded instead of failing; `review-file --fresh` and retries use the current config after `run reload` (B234)
- Fixed a test that read a half-written JSON file and failed about 1 run in 10 (B235)
- Timing tests in the worker queue, idle CPU, bench-review and block-open-base suites no longer fail when other suites run at the same time (B236)
- On macOS the default worktree folder is now `../<repo>-worktrees.noindex` with a `.metadata_never_index` file, so Spotlight does not index block worktrees; live worktrees and an explicit `proof.worktree.dir` do not move; `doctor` warns when the folder is not under `.noindex`; a failed `git worktree remove` with "Directory not empty" names the leftover path and the `rm` command (B198, #28)
- An idle review worker now exits by itself after `worker.idle_exit_hours`; `status` prints a hint when a run has ended; `sweep` and `doctor` find and stop stale idle workers (B200, #28)

## [0.16.0] — 2026-11-01

### Added

- `forge spawn --print-argv` prints the exact command it would run and runs nothing; the Solo adapter guide step 2 now uses it (B229)
- `npm run bench:block` measures one block flow under Node and under the Go daemon (process count, peak memory, wall time); each pass runs in its own child process (B184)
- Forge facts checks the code names in a brief, marks ignored or outside-snapshot paths UNVERIFIABLE (never NOT-FOUND), takes claims from the brief only, maps absolute paths inside any `--sources` root to that source, and handles globs, `/dev/null` and route patterns (B227)

### Changed

- Reviewers get the bodies of the functions the changed code calls, inside the packet budget, so they stop raising false findings about code they could not see (B212)
- A second stalled file now gets its own L3 patch rung instead of stopping the whole block with `l3_patch_exhausted` (B213)
- The worker prunes done queue tickets and keeps an index; worker polls and done checks read only new ledger rows instead of the whole ledger each time (B214)

### Fixed

- Worker start times use one format everywhere: `ps` pads days 1-9 with two spaces, so worker pins and route checks failed on those days. Old padded pins still match (B225)
- `gates run --changed` also runs the tests of files that import a changed file, so a change cannot break callers in other folders unseen (B221)
- Daemon tests wait for readiness against a deadline instead of fixed sleeps, so they do not fail on a busy machine (B226)
- `gates run --changed` gives each test runner only its own test files (no more `.go` files passed to `node --test`); when none fit, the gate runs the full suite and reports scope `full` (B203)
- A gate that hits its time limit now says `TIMEOUT after <n> s (not a test failure)` with the flag to raise it, and test files left unfinished by the timeout are no longer listed as failed tests (B223)
- `wait-block` no longer reports done while a file's last review was unavailable; it re-queues the file or names it, so `block close` does not refuse it later as unreviewed (B217)
- `block close` no longer refuses a file that the block's own commits created and then deleted; a missing file with no such history still needs review (B230)
- `status` warns when a live worker of the run is not the pinned one and names `run start --reattach`, before `block close` can stop blocks it served (B224)
- CI is green again on Linux: `block rebase` works when git has no user name or email and keeps each commit's committer, and overlapping `watch` ticks for one block no longer lose state (B231)

## [0.15.4] — 2026-10-31

### Added

- Go core, step 1: `native/` Go library (canonical JSON, row signing, ticket ids, ledger read) and a golden compat kit made from the Node functions; Go tests prove byte-equal output (B180)
- `block land <id>` runs review join, changed-file gates and close in one step, stopping at the first failure with the next command; `--merge` also merges the block branch into its base (B185)
- The npm package ships a prebuilt Go helper (`code-forge-native`, darwin/linux, arm64/x64; built at release) with `version` and `verify-ledger`; `doctor` shows it, and Node is used when it is missing or `CODE_FORGE_NATIVE=0` (B181)
- `block land` and `block close --auto-claim` claim and review a file changed outside the owned list (unless another open block owns it), and claims are kept when a block is reopened with `--attempt N` (B186)
- Review packets always keep the file's import header, the judge packet shows the cited `path:line` windows (ranges like `a-b` too), and `block reopen --yes` works on an open file with findings when the judge agrees (B192)
- `block land --max <dur>` (default 30m) waits for reviews, retries the join up to 3 times while a findings round is open, and on timeout stops with `wait_timeout` and a next line that names a larger `--max` (B190)
- Merge gate: `block land --merge` runs the full suite on the merged result in a temporary worktree first; a red suite or a conflict leaves main unchanged. Skip with `--no-merge-gate` or `gates.merge_gate: false` (B189)
- A file whose review stopped with findings is reviewed again automatically when a fix changes its content; unchanged or waived files are not re-queued (B194)
- Every Go command the tests run sets `CGO_ENABLED=0`, so `go test` passes on macOS without extra setup; `native/README.md` says how to test the Go code (B195)
- `block waive --yes` now says the evidence may cite `:line or :start-end`, the same as `block reopen --yes`; a range citation is accepted (B193)
- `forge facts` cuts an excerpt over 200 characters (ending with `…`) instead of refusing the whole sheet; repo-relative paths with an extension or a top-level folder, and absolute paths, are now path claims; `§ 12.1` is no longer a version claim; a long brief with almost no claims prints a warning (#29, B197)
- Critical findings still need a human waiver by default; under an active autopilot grant, `block waive --yes` may waive a critical when the reason cites a passing acceptance test and the finding's lines and the judge agrees; the row records `by: agent` and `autopilot: true` (B196)
- Go core, step 3: `code-forge-native daemon` serves `wait-block`, `block-tickets` and `status` over a Unix socket with a ledger tail index, a queue index and a cached worker start time; answers match the Node commands (compat tests). Not used by the CLI yet (B182)
- The full test suite is green again: reopen output keeps its old text when no claims are kept, the config doc is current, and a plan clause that only cites a planned test file is no longer refused. (B204)
- The native daemon status helper now lives with the other command files, so the import-order rule passes again. (B218)
- Commit watch waits until a burst of commits ends (15 s quiet, 120 s at most) and reviews only the last commit, so a coder making several quick commits no longer starts extra reviews. (B211)
- The worker and `status` use the native Go daemon when it runs and fall back to Node when it does not; status output is the same in every mode, and `doctor` shows the daemon state. (B183)
- Two block tests now give the commit watch its second quiet tick, so they pass with the B211 debounce. (B219)
- The worker runs up to 6 review tickets at once (was 3), with at most 4 sessions per provider (`review.provider_parallel`); tickets waiting for a provider still count toward the limit. (B208)
- code-forge picks the primary reviewer from this project's measured speed, availability and accuracy (`review.adaptive`, on by default). A matching route still competes on performance unless it has `pin: true`. (B207)

## [0.15.3] — 2026-10-30

### Added

- Gate sets per block kind (`gates.kinds`), and unknown config keys now say to upgrade with the running version (B160)

### Changed

- The test leak check ignores a live code-forge run of another project (its run record names a workspace outside the temp dir, raw or resolved path) and that project's ledger; test-made runs stay leaks (B158)
- Two worker tests no longer fail on a busy machine: the orphan test waits on observed worker events, and the enqueue speed test subtracts a cold node start measured in the same test; both keep their checks (B157)
- Scaffolding lock files skip code review through `review.exclude`; close signs each exclusion and checks excluded npm locks for HTTPS registry URLs and integrity, while other lock formats receive no safety check (B155)
- Named custom gates: `gates.custom` runs named gates after the fixed ones and records evidence files (path, bytes, sha256) on the gate rows (B146)
- Time in the report: `cost_per_block` shows wall time and human wait per block; a new `run_time` section shows each run's wall time, cost and blocks, plus a total (B147b)
- Design before code: `block open --after <ids>` refuses until every named block is closed; a block that waits for a human check is not done (B148)
- Report: spend with no run id is shown as `outside runs`, not as a "(none)" run; stopped blocks show `stopped` and the time to the stop (B149).
- Report: spend per run and cost per block print `outside runs` instead of a "(none)" run (B150).
- Specialist outcome file: `code-forge outcome seal|check` and `schema/outcome.schema.json`; a caller uses a specialist result only when it is approved and every file matches its sealed sha256 (B151)
- Specialist pipelines: validate `code-forge.pipeline.yml` and use `code-forge pipeline status|next` to see step states and ready work (B152)
- Specialist pipelines: `block open --after-outcome <step ids>` waits until each named step's outcome is approved and passes `outcome check`, and records the accepted outcome hashes; `block close --report` no longer asks for a review ticket for a `review.exclude` file (B153)
- `code-forge outcome record` writes a signed `outcome.recorded` row for an approved specialist outcome; `report` shows them in a `specialist_outcomes` section (B154)
- Gates: `gates.timeout_s` (30..7200 s), CI=1 and npm_config_yes=false for every gate, a gate that ends on an
  interactive prompt fails with reason `prompt`, and a warning when the checkout has no installed dependencies (B156)
- Context pack: a `## code-forge` section names the running node, the absolute code-forge bin and its version, and tells
  the coder not to use another code-forge from PATH (B162)
- `block rebase` works for a block with its own worktree: it rebases the block branch onto main, moves the block base,
  and earlier green gates stop counting (B159)
- Reviews see the sibling test and source files of a changed file, so findings about missing coverage are checked
  against real related tests (B161)

### Fixed

- Grok runs no longer start the user's Cursor or Claude MCP servers, so no orphan MCP helpers stay behind and use memory
  (B164)
- An invalid L3 judge answer is now a broken result, not a disagreement: it retries once, then tries the next L3 model;
  a schema-invalid review answer uses every level fallback even when `second_levels` is empty (#24, #26 item 5, B168)
- Codex `spawn --background` keeps its CODEX_HOME after the parent exits, and a Codex coder in a linked worktree can commit:
  it gets write access only to the worktree git dir and the shared objects, refs and logs, never to config or hooks (#26 items 3-4, B167)
- `block open --base <sha>` uses the given commit (also for the proof worktree), and reuses an existing block branch that
  descends from that base instead of refusing it (#26 item 1, B166)
- `block close` accepts review results from a worker that was pinned for the same run before `run start --reattach`; a worker never pinned for the run still stops the block (B176)
- Gates: a gate that ends with no exit code (signal or timeout) now says why and shows the output tail; the detected Pint and
  Prettier format gates check only git-tracked files, in batches; gate rows store `duration_ms` and `report` shows gate time per block (#26 items 11, 14, B173)
- Proof: Pest/Laravel projects get a default junit runner, `run reload` may add new proof runners, a `proof.worktree.setup`
  hook runs before RED (a failing setup fails with reason `setup`), and assertion deletion removes a multi-line chained
  Pest `expect()` (#26 items 6-8, B171)
- Coder context: `context pack` includes the block brief and acceptance and prints the pack itself; `spawn` for a coder
  appends them; the sentinel and TESTS line are found in a JSON-escaped coder answer (#26 items 9-10, B172)
- `block reopen` now restarts the review: it clears the cached done result, queues a fresh ticket, and `review-file --wait-block` waits for the new result instead of returning the old one (#25, B169)
- Process cleanup: `sweep` never stops a worker of an open run or a run it cannot read; zombie parents are cleaned up and an unreadable registry status is reported, not guessed (B163)
- Idle CPU: `review-file --wait-block` and the worker back off from 200/250 ms up to 5 s while nothing changes, and a block file is re-hashed only when its mtime or size changes (B177)
- Process sweep (`sweep`, `run start`, `doctor`) never lists or stops a worker of another code-forge home, or one whose environment cannot be read; before, tests with an isolated HOME stopped the live review worker (B178)
- `--help` and `-h` work on every `block` verb and write nothing; docs explain `review.routes` for files over 400 lines, the L3 judge fallback, the pnpm 11 symlink note and `codex exec </dev/null` (#26 items 12, 15-16, #24, B175)
- Review timing: `review-file --cwd <dir>` runs from any folder; each ticket records queued, started and finished times and `duration_ms`, and `report` shows review time per block (#26 items 13-14, B174)
- A `block close` refused because main moved now prints its exits: `review-file --rebase-check` and the human-only `block rebase --accept-changed <file> --reason`; each rebase writes a signed `block.rebase` row (#23, B170)
- The suite is green again after the B163–B178 merges; the review worker now starts a waiting ticket at once instead of after its idle back-off, so parallel reviews keep their speedup (B179)

## [0.15.2] — 2026-10-30

### Fixed

- OpenAI strict schemas make only optional keys nullable, so `forge facts` no longer refuses an answer with null on a required field; a facts row with a null command or excerpt is downgraded to UNVERIFIABLE on its own (issue #27, B165)

## [0.15.1] — 2026-10-19

### Changed

- The test leak check ignores a lock held by a live process outside the suite (same pid start time), so gates can run while a review worker runs (B142)
- The coder brief keeps only the final test log after a sandbox EPERM rerun, and fix rounds list the typecheck command next to the block tests (B140)
- The first-use and brief-mode walkthrough tests no longer fail when a live code-forge run of the developer writes into ~/.code-forge during the suite (B145)
- Process tests have wider time bounds, so the suite no longer fails from slowness on a busy machine (B144)
- The Codex transcript check ignores rule text inside a node/python/ruby/perl script body that spawns nothing, also inside `zsh -lc` wrappers (B141)
- New human verb `block amend-acceptance` corrects a wrong acceptance clause of an open block; reviews signed before the change no longer count at close (B143b)

## [0.15.0] — 2026-10-19

### Changed

- Worker claim tests back up file-watch barriers with a short poll, so a busy file-event service no longer fails them
- Support up to four pinned review workers per run, add workers through run start, and verify result identities and dead ticket claims at block close (B107)
- Light blocks bound small changes to configurable file and line limits, batch their reviews, and run only block tests (B139)
- Human decision close combines finding waivers, agent acknowledgement and transcript acceptance in one confirmation; immutable `close.require_human` supports delegated `close:routine` grants for approved blocks without waivers or hits (B138)

- Clarify that human pass requires a note and accepts optional proof; verify an omitted proof is recorded as null and closes the block (B137b)
- UI acceptance can require human device checks: code close waits in `awaiting_human_check`, status and report list waiting blocks, and only a terminal-confirmed human pass or stop permits run completion (B137)
- Opt-in `gates.repeat_new_tests` repeats added or changed block tests after a passing gate, records signed repeat evidence and reports failures as `flaky-test` with the files and failing output (B136)
- Block open records the base branch; close warns when it moved, and closed worktree blocks can rebase while preserving reviewed file blobs or restoring the old head on refusal (B134)
- Rechecks include finding evidence, close unsupported current-line claims as stale, and keep repeated claim/evidence findings from escalating the review ladder on their own (B133)
- A changed lens budget after `run reload` refreshes cached `split_required` results and recheck stops on the same content, including with `--fresh`, and reports the old and new budgets (B132)
- Review risk counts added and removed lines; oversized quick Markdown packets step up to full before section packing, and the block join exposes per-file outcomes, reports split recovery steps and queues owned changes without earlier tickets (B131)
- CLI: missing ledger slugs name known projects; block open catches malformed path lists while allowing new plain paths and commas inside braces; worker startup names competing runs; process probes prefer `/bin/ps`; block close requires green gates when configured; human transcript acceptance can cover all rules on a line and its Codex completion (B135, B135b)

## [0.14.1] — 2026-10-15

### Changed

- `upgrade` starts with an ASCII logo and package version, followed by the version change, install lines and release-note summary; `--plain` prints only the install lines, and install records now track their linked package version (B129)

- README: a new-user guide replaces per-version history, which lives only in this CHANGELOG; releases update only the optional `Latest:` line and no longer require a next-release README anchor (B130).

- Tests: the claim fixture in `test/worker/claim.test.mjs` publishes its completion counts with a temp file and rename, so a Linux inotify wake never reads an empty file as 0, and its failure messages print the sessions, rows and claim file; `test/no-leak-final.test.mjs` uses `--experimental-test-isolation=none`, which Node 22 and 24 both accept (B128)

## [0.14.0] — 2026-10-15

### Changed

- `review-file --wait-block`: when the current content's ticket is done with a result that never counted (`stale`, `unavailable`, round-1 `split_required`), the join drops that done marker with the `--fresh` claim and the worker reviews the content once; one drop per ticket at most, kept on disk; a counted result is never dropped (B125)
- `block waive --yes`: a warning or nit finding can be waived without a terminal when the reason cites evidence (path:line, a test file, or an acceptance clause) and a fresh L3 judge agrees; one signed `review.waived {by: agent}` row with the verdict; refusals `waive-yes-critical`, `waive-yes-no-evidence`, `waive-yes-disagreed`; `block close` lists agent waivers and `--json` adds `agent_waivers`; `block reopen` stays human-only (B124a)
- `block accept-hit <id> --run --rule --line --transcript --reason`: human-only; refuses `accept-hit-no-such-hit` before any prompt; writes a signed `rule_break.accepted {block, rule, line, line_sha256, reason, by: human}` row; `block close` drops a transcript hit only on the same rule and the sha256 of that line, prints each accepted hit, and `--json` adds `accepted_hits`; the verb is on the coder forbidden list (B127)
- `block unwaive <id> <finding> --run --file --reason`: human-only; refuses `unwaive-no-agent-waiver` before any prompt; a signed `review.unwaived` row cancels the earlier agent waivers of that finding at the gate and the review-file pre-check; `code-forge report` gets a 15th section `agent_waivers`; new key `review.agent_waivers.require_ack` (frozen at run start): `block close` refuses `agent-waivers-not-acked` until a human `block ack-waivers <id> --run` row matches the current agent waivers (`ack-waivers-none` when there are none); both verbs are on the coder forbidden list (B124b)
- `block reopen <id> --file --reason --yes`: agent reopen; refuses `reopen-yes-no-evidence` without cited evidence, `reopen-yes-disagreed` when a fresh judge disagrees, and `reopen-yes-limit` after one agent reopen per block and file in a run; the row is `by: agent`; an agreed, verified agent reopen supersedes the review cap like a human reopen; `block close --json` lists `agent_reopens` (B124c)
- Docs: the README command table lists `--yes` on `block waive` and `block reopen` and adds `block accept-hit`, `block unwaive` and `block ack-waivers`; `docs/getting-started.md` and `skill/references/review.md` no longer say reopen has no `--yes`


## [0.13.0] — 2026-10-14

### Changed

- The leak test in `test/worker/orphan.test.mjs` takes the current contents of the repo's `.code-forge` folder as its baseline, so `npm test` passes when that folder already holds files (B126).
- Close check: a `review.cap` row is superseded by a later verified human `review.reopened` row followed by a signed
  `review.round` of the file's current content; a superseded cap row adds no ids, so old stop findings need no
  waiver after a reopen (B123)
- `review-file --wait-block`: a file whose newest review left open findings and whose content changed after it is not
  re-queued; the join answers `status: findings` with that ticket and writes one delivered marker; the next join
  queues the latest content once, so a commit made blind to the findings never counts as a stall round (B122)
- Worker registry: each worker writes `<repo>/.code-forge/workers/<pid>-<start_time>.json` next to `queue/worker.json`
  and beats both; boot counts live entries and writes its entry under one cross-process lock (limit 1); boot
  removes dead entries (a known start-time mismatch is dead); an unreadable entry blocks the boot and `doctor`
  lists it; `liveWorker` keeps the B30 fresh-heartbeat rule (B106)
- Watch mode holds rechecks: a commit of a file that already has a ticket gets no ticket, only a `watch.held`
  row; `review-file --wait-block` queues one ticket for the latest content; older pending tickets are still
  superseded (B121)
- Locks now hold across processes (`src/util/file-locks.mjs`, `~/.code-forge/locks`): ticket claims, the per-file and
  block locks, the budget row, schema fallback and autopilot actions; a dead owner's lock (pid + start time) is broken
  under a per-key break mutex; `doctor` lists bad locks and crashed break mutexes (B105)
- New opt-in key `review.fair_share` (boolean, default `false`): the worker pool gives each free slot to the
  pending ticket whose block has the fewest running tickets; ties stay oldest first (B101)
- `context pack` with no `context` key writes a pack with the block header and the built-in proof rule (300-line cap)
  instead of refusing; a bad `context` value still refuses (B118)
- `block close` prints `WARN block <id>: no green gates run for this block; …` when `gates.test` is set and the block's
  last gate row is not a green `gate.done`; it still closes (B118)
- `block close --transcript` reads a Codex JSON log (`codex exec --json`) by its commands and file writes only;
  agent messages, reasoning, command output and non-item events are skipped; other logs are scanned as before (B119)

### Fixed

- `block open` refuses an `--owned` or `--tests` entry that contains whitespace and names no existing file or directory
  (a shell quoting slip), naming the entry; nothing is written (B118)
- `npm test -- --test-name-pattern=<p>` exits 0: six test files no longer call `rm(undefined)` in cleanup when
  their setup was skipped by the pattern (B116, built with `code-forge run` itself)
- The close reason names each transcript rule break once (`rule_break transcript line <n>: <id>`, at most 5,
  then `+<k> more rule_break`) instead of repeating a bare `rule_break` (B119)
- `gates run --run <r> --block <id>` with plain gates (no `{test_files}`, no `--changed`) writes its `gate.done` and
  `gate.red` rows to the run's project ledger; before, it wrote none, so the B118 close warning fired after a green run (B120)

## [0.12.0] — 2026-10-14

### Added

- `gates run --changed` (with `--run` and `--block`) narrows `{test_files}` to changed tests and stem-paired tests of
  changed sources, and a new `{changed_files}` gate token passes the changed paths; it falls back with `reason`
  `no_baseline`, `unpaired`, `threshold` or `no_changes`; a green `gate.done` records the owned file hashes as the next
  baseline (B99, #16)
- `review.file_loop: overlap` and `review-file --wait-block <id> --run <r> [--max]`: one wait for all owned changed
  files of a block, one JSON line `{block, files: [{file, ticket, status}], done}`; a file changed after its ticket gets
  its current content enqueued; timeout exits 0 with `reason: wait_timeout`, `not_queued` files give `reason:
  not_queued`, no live worker exits 3 with `worker_down` (B100)
- `review.file_loop: watch`: the worker watches the block's HEAD and queues owned files from new commits, with the same
  ticket ids as `review-file`; an older pending ticket of the same file is marked `superseded`; problems write one
  `watch.skipped` row per reason (B104)
- every `code-forge context pack` carries a built-in `## proof rule (code-forge)` section that `context.max_lines` never
  cuts: the red→green definition, no hand mutations, one exact test per clause, proof scales with risk, no extras the
  acceptance does not ask for (B117)

### Changed

- `block close` reports `coder report FAILED: TESTS line malformed: <line>` for a `TESTS ` line that does not end in
  `exit=<n>`; no line at all still gives `no TESTS line` (B114)
- every review lens tells the reviewer not to ask for hand mutations or tests beyond the acceptance; a weak assertion is
  fixed by a stronger assertion; over-engineering is at most a `nit` (B117)

### Removed

- the unused `proofRequirements` export in src/proof/tiers.mjs (B117)

### Fixed

- every fix-loop stop (stall, cap, split, stop-at, L3 exhaustion) records `stop_content_hash`, so `block reopen` works
  after it; a stop with unknown content reopens when no verified `review.round` names the current hash (`from_hash:
  null`), and the refusal no longer points to `block waive` (B114, #17)
- paired tests: a Python `<stem>_test.py` file pairs with `<stem>.py`, like `test_<stem>.py` (B115)
- `npm test -- <args>` passes the extra args to the main test phase again; the snapshot and final no-leak steps still
  run (B115)

## [0.11.0] — 2026-10-13

### Added

- `npm run release` moves the README "In the next release:" bullets under the new version, refuses an empty [Unreleased]
  or a missing README anchor in preflight, restores README on a failed check, and runs `gen-config-doc --check`;
  `docs/releasing.md` gets a releasing checklist (B108).
- `pairsFor` and `summarize` (src/review/paired.mjs) and an optional `## paired` packet section after `## context`,
  dropped first over budget; `attachFiles` attaches an untracked file only when it is an owned, changed file of the
  block and not ignored or secret-like (B97).
- The review engine, the fix-loop recheck and the worker send `## paired` (the block's owned changed peers and
  `test_files`); the needs_file path passes the block's owned and changed files so a new untracked owned test file can
  be attached; the lenses say a test named in `## paired` exists (B98).

### Changed

- `block close` prints the `reviewed <path> ticket <id>` lines itself, for every file the review gate accepts; `--json`
  and the done file carry `reviewed: [{file, ticket, via}]`; the coder report no longer needs hand ticket lines, and a
  mismatched hand line under `--report` is a WARN, not a refusal (B96, #16).
- A quick review packet over `quick_in` steps up to one full L2 session when it fits `full_in`, before `split_required`;
  the `review.plan` row carries `step_up: {from, tokens_in, budget}` (B109, #16).
- A cached round-1 `split_required` is re-queued by `review-file` (`retry: true`) and by `review-file --fresh`, with the
  same ticket id; a recheck `split_required` stop still needs `block reopen` or `block waive` (B110, #16).
- A recheck with no fix hunks counts no round and returns `reviewed`, `approved: false`, `reason: 'no-fix-hunks'`; a
  recheck answer that lacks a `resolved` entry for an open finding of its packet is a `schema` miss; the worker's
  recheck summary adds `status`, `tokens_in`, `tokens_out` and `reason` (B111, #16).
- A review ticket reads the diffs of the block's other changed files only when the review asks for them, once per ticket
  (B112)

### Fixed

- A worker stops by itself, without taking a new ticket, when its run record or its HOME is removed (B113)
- Tests stop every worker they start, also when a test fails or times out; the final no-leak step of `npm test` fails
  when a worker from this suite is still alive (B113)

## [0.10.0] — 2026-10-13

### Added

- `keys.cache_hours` (1–24 h, default 8) and `keys set --op … --cache-hours <n>` set how long a key read from 1Password
  stays cached; a lower value shortens an existing cached key at the next read, `keys set --op` refuses an invalid value
  and other readers warn and use 8 h (B84, #14).
- `keys list` and `doctor` warn when a cached 1Password key has less than 1 hour left, and `jev ask` prints a re-link
  command (`code-forge keys set jev --op <ref>`, the ref shell-quoted when needed) when the cached key expired and
  1Password did not answer; the key index keeps the `op://` reference (B85, #14).
- `src/proof/junit.mjs`: a fail-closed JUnit XML reader and RED/GREEN classifier for `proof red-green` (no DTD or
  entities, 8 MiB cap, only failed assertions count as RED; a SwiftPM bare failure needs a matching XCTest console
  line); used by `proof.runners` from B87 (B86, #7).
- `proof.runners`: `proof red-green` proves a non-`node:test` file through the matching runner's argv and its JUnit
  report; the list is frozen in the run snapshot, validated by rule 17 `proof-runners-invalid`, and the row carries
  `runner`, `env_keys` and `failed_tests` (B87, #7).
- `swift` adapter for `proof.runners`: anchored `{test_filter}` (file base name or `Suite/test` case), reads the XCTest
  report and its Swift Testing sibling, `swiftc` compile errors give `RED_INVALID compile`; opt-in live check
  `CODE_FORGE_LIVE_SWIFT=1` verified on Swift 6.3.3 (B88, #7).
- PHP red→green through the `junit` adapter, verified live on Laravel 13 with PHPUnit 12 and Pest 4
  (`CODE_FORGE_LIVE_PHP=1`); captured reports replace the hand-written PHP fixtures (B89, #7).
- The `xcodebuild` adapter for `proof.runners`: `xcode` keys, `{only_testing}` and `{result_bundle}` tokens, a precheck
  for full Xcode and an available simulator (`xcode-missing`, `destination-unavailable`), a fail-closed `xcresulttool`
  reader, and `RED_INVALID compile` for a build failure; fixtures captured from Xcode 26.6 (B102, #7).
- A live `xcodebuild` red→green check on an iOS simulator (`CODE_FORGE_LIVE_XCODE=1`), verified with Xcode 26.6 on an
  iPhone 17 simulator; it skips with a reason without full Xcode or an available simulator (B103, #7).
- `review.routes`: size-based review routing. A file with at least `min_lines` lines is reviewed (L2) or judged (L3) by
  the rule's model, with its own session timeout and second level; one signed `review.route` row per routed level;
  config rule 18 `review-routes-invalid` (B90, #10).
- A `## test-quality` section in review packets lists the lines of a test file that read a source file and assert on its
  text; every reviewer lens gets one rule to judge those lines (a hint, not a finding); recheck packets get the rule but
  no section (B92, #10).
- `coder.sandbox` with `profile: workspace|toolchain|full` and `add_dirs`: `toolchain` gives the Codex coder one
  `--add-dir` per entry, `full` runs it with `-s danger-full-access` and `doctor` warns; entries that are, hold or sit
  inside a protected dir (home, `~/.ssh`, `~/.codex`, `~/.code-forge`, the run temp root, …) are refused
  `sandbox-dir-refused` after symlinks are followed; the key is fixed for the run and `forge spawn --role coder --run`
  refuses a changed live value `sandbox-changed` (B93, #10).

### Changed

- A timed-out review session is a broken result like a schema miss: at a packet's second broken result the second level
  (`review.second_levels.<level>`, or a routed rule's `second`) is tried once in the same ticket;
  `review.session_timeout` notes carry `packet_hash` and are signed, and `review.schema_fallback` gains `trigger:
  schema|timeout` (B91, #10).
- The skill sends ONE fix message per block: after the coder's first COMPLETE it waits for every `review-file` ticket
  and the block's tests, then sends every failing test and every open finding together; a later finding waits for the
  next round, and the per-file round cap still counts per file (B94, #10).
- `block waive` and `block reopen` are human-only: they ask for a yes at a terminal, have no `--yes`, exit 2 and record
  nothing with stdin or stdout not a terminal, and refuse a bad id, path, control character, unowned file or unknown
  finding before asking; an autopilot grant does not change this; the prompt does not prove a human, since a
  pseudo-terminal passes it (B80, #10).

### Fixed

- The JUnit reader accepts the real SwiftPM 6.3 bare XCTest failure (`message="failure"`), not only `failed`; captured
  Swift reports replace the hand-written fixtures (B88, #7).
- A missing class in Laravel 13's JSON-escaped AI-agent console summary is now read as `import`, not `error` (B89, #7).

## [0.9.0] — 2026-10-12

### Added

- review-file --answers <ids> --of <source>: a fresh L2 session judges a fix made in another owned file; resolved ids
  get signed review.answered rows bound to both content hashes, and the close gate counts a still-current answer like a
  waiver (answered list on gate.review_waived_all).
- block reopen <id> --run <r> --file <path> --reason <text>: a human-only verb that restarts a stopped file on changed
  content (or a lost one on its current content) with a full round 1; signed review.reopened row, one per anchor seq;
  refusal codes reopen-same-content, reopen-tampered, reopen-not-stopped, reopen-block-not-open, reopen-not-owned,
  reopen-config, reopen-already-recorded, reopen-write-failed.
- A stopped `review-file` answer carries `exits`, and the gate's `review_cap` and stopped-file `unreviewed` detail end
  with the same exits (B73, issue #12).
- The review packet of a deleted owned file carries a `## deleted file` note: up to 20 tracked or untracked paths that
  still name its basename (one bounded `git grep`, secret-like and control-character paths left out), `no references
  found`, or `references not checked` (B74, issue #8).
- `review-file --fresh` drops a cached result that never counted and queues the ticket again; counted results refuse
  `fresh-needs-reopen`, tampered ones `fresh-tampered`, and the cached marker is never moved (B75, issue #10 item 4).

### Changed

- Review state is saved under ~/.code-forge/runs/<run>/review-state/ instead of the temp root; older state files are
  moved once after an anchor check; doctor warns about missing state files of active runs (#15)
- Review state: the reviewed text is stored as a git blob (reviewed_content_blob in the signed review.state body), so a
  restored state keeps reviewed_content after the file changed; a missing, malformed or mismatching blob falls back to
  the full-file review.
- A `patch_check` that closes at least one finding open before the patch (by id) and is under
  `review.max_rounds_per_file` continues the fix loop at L2; a patch that closes nothing still stops
  `l3_patch_exhausted` (B73, issue #12).
- The skill and docs name the review exits in order, say where review state lives and how the ledger rebuilds it; the
  coder brief names `review-file --answers`, and the security reference lists `block reopen` as forbidden to a coder
  (B76).

### Fixed

- A lost review state file is rebuilt from the signed ledger and recorded as review.state_restored; rounds, findings and
  stops stay; with no ledger copy the ticket is refused as review-state-lost, not tampered (#15)
- A file whose every finding is cleared by accepted waivers counts as reviewed (reviewed_by_waiver) and block close
  writes a signed gate.review_waived_all row; unknown severities are human-only (#9)
- Test isolation guard: new files of a run that existed before the test process (its lock, key or run directory) no
  longer count as leaks; a new run id or any other new path still fails the process.
- `review-file --answers` reads the severity of every `review.cap` id from the signed refusal rows, so an id the close
  cannot count is refused before a reviewer session (B79).
- The answers packet checks the source file with the secret-path rule, redacts each part before it is cut, keeps every
  part and the whole packet inside the budget, and refuses `packet-too-large` when the fixed text alone is over it
  (B79).
- `block reopen` refuses a non-string `tmp.root` as `reopen-config`, and `reopen-write-failed` names the error code,
  never a path (B79).
- The ledger rotation race test forces the order of the two racers with an `afterLink` hook instead of scheduling them,
  so it no longer fails at random under load (B81).
- The worker restart test waits up to 5 s for killed sessions to exit instead of checking once, so it no longer fails at
  random under load (B82).
- `review-file --fresh`: a failed restore of the done marker no longer leaves the ticket looking pending; it writes a
  refused `fresh-restore-failed` marker or keeps the claim, `block reopen` clears leftover claims, and the messages say
  which state was left (B83).

## [0.8.0] — 2026-10-12

### Added

- B60: related-test gates (issue #13 item 3). block open --tests <paths> names the test files of a block; a gate argv
  item exactly {test_files} becomes those files, or the full suite runs when the block owns more than
  gates.full_suite_threshold_files files or no test file is known. Gate rows carry scope, reason and test_count. A gate
  can be {argv, env}; env values are never printed or recorded, and child output masks values of 8 or more characters.
- B61: Laravel under export isolation is verified. A Laravel or plain PHP project on PHPUnit without Pest is detected as
  php-phpunit. Export copies a Composer vendor folder instead of linking it, so PHP autoloads the code of the block and
  not the main tree. An opt-in live check (CODE_FORGE_LIVE_PHP=1) runs a Laravel app on PHPUnit and on Pest through the
  export.
- proof.isolation: worktree — each block gets its own git worktree and branch (proof.worktree.dir/branch/base); owned
  files need to be disjoint only inside one tree; block close --remove-worktree removes the tree and keeps the branch
  (issue #13)
- Worktree blocks: gates run --block, review-file, the worker, block close and proof export/red-green all measure the
  block's own tree (against its base), never the main checkout; a missing, malformed, off-branch or foreign tree is
  refused (issue #13)
- Context pack: a top-level `context` key (rules, lessons, commands, max_lines) and `code-forge context pack --run
  --block [--json]`, which writes the pack, prints its path and token estimate and appends a signed `context.pack` row;
  review packets carry the rules and lessons (issue #13)
- Coder test proof: the coder report may carry `TESTS <command> exit=<n>`; with `context.require_coder_tests: true`
  (fixed at run start) `block close --report` refuses a report without a green TESTS line and writes a signed
  `coder.tests` row (issue #13)
- code-forge status: at most 10 lines per run (state, mode, spend, open blocks with level, model, last gate, open
  findings) or one --json line; every block verb takes --json and prints one JSON line (issue #13)
- Block-end signal: every block end writes <run root>/blocks/<id>.done.json; hooks.on_block_end runs a command once per
  block end (no shell, timeout, signed hook.ran row); a failing hook only warns (issue #13)
- ledger outside-review records an outside reviewer's rounds and findings per PR (signed outside_review row, link
  redacted); report gets a 14th section, outside_review, with the code-forge cost of the PR's blocks next to the rounds
  (issue #13)
- docs/brief-mode.md walks a brief-only run with an outside reviewer, related tests, a worktree, the context pack and
  the no-poll flow; a test runs the page's config and steps; the skill says when to use brief mode or plan mode (issue
  #13)

### Changed

- BREAKING: autonomy.coder_may_push and autonomy.coder_may_open_draft_pr now default to false, so a coder no longer
  pushes or opens a draft PR unless the config allows it; init asks both (default no) and takes --coder-may-push /
  --coder-may-open-draft-pr yes|no; set both to true to keep the old behaviour (issue #13)

## [0.7.7] — 2026-10-11

### Added

- Brief-only mode (B59, issue #13 item 2): run start --mode brief writes a signed run.mode {mode: brief, plan: none}
  row; the mode is fixed for the run. In a brief run, block open without --level takes the lane from the rules
  (fallbackLane) over the owned entries, --lines and proof.tiers.high.paths of the run config snapshot, and writes a
  signed decision row {question: lane, source: rules, facts} before dispatch; an explicit --level wins. report prints
  the mode of each run in its text output (not in --json or --export). --mode plan or no flag keeps the old behaviour.

## [0.7.6] — 2026-10-11

### Added

- review.external {required, name} (B58, issue #11): an owner-approved outside reviewer replaces the local per-file
  review. Read only from the run-start config snapshot (run reload refuses to change it; run start refuses required:
  true without a name) and signed as a review.external row; block close then skips the per-file review check (also
  not_approved) and writes a signed gate.reviews_waived {by: config, reviewer} row just before block.close, only when
  the block closes, with no TTY and under autopilot. Unowned, proof, no_changes, transcript, orphans and closeBlock
  checks still refuse. block open and block claim refuse an owned entry that covers .code-forge.yml. Without the key
  nothing changes.

### Changed

- block open, block claim and block close now read the run's config snapshot; a snapshot that does not match its hash
  refuses them with config-snapshot, with or without review.external (B58).

## [0.7.5] — 2026-10-08

### Fixed

- An owned entry with no glob character now owns every path below it everywhere, not only at `block close`: `block open`
  and the plan check refuse a directory that overlaps a file or directory of another block (parent or child, either way
  round; `src/feature` vs `src/featureX` stay disjoint), and `block claim`, `block rebase`, the scope gate, proof and
  the worker's file set and budget count the files inside an owned directory (B57, issue #2). An entry that is a regular
  file at `block open` or `block claim` owns only itself (recorded as `owned_kinds`); one not there yet stays
  directory-like; a brace entry stays a glob. A refused `block claim` names the entry it overlaps
  (`<path> overlaps <entry> owned by open block <id>`), and `proof export` never copies or removes an owned directory
  itself, only the files in it.

## [0.7.4] — 2026-10-08

### Added

- Review: code moved between two changed files of the same block is named in a ## moved code packet section
  (deterministic line matching against the block's other changed owned files, same base: >= 3 significant lines or >=
  120 significant characters; at most 12 ranges; a Markdown section packet lists only the moves in that section) in
  round 1, rechecks, patch checks and the judge packet, so the source file's reviewer no longer reports the removal as a
  deleted feature; every hunk and the reviewed_hunks check are unchanged, and the section counts toward the packet
  budget (issue #2).

## [0.7.3] — 2026-10-08

### Added

- Review: a packet whose answer fails the schema twice is retried once on `review.second_levels.<level>` when configured
  (signed `review.schema_fallback` row; never in consensus mode; budget and closed-book rules apply), and every schema
  miss logs a structural diagnosis of the answer (`review.schema_invalid`: keys, types, schema errors or the parse error
  offset, never the text); the autopilot binnacle shows each fallback as an incident (issue #2).

## [0.7.2] — 2026-10-07

### Changed

- review.budgets.full_in defaults to 32,000 tokens (was 12,000), and a Markdown file whose diff alone is still over it
  is reviewed section by section (cut at its headings, outside fenced code; approved only when every section is;
  findings as S<section>.<id>; rechecks split the same way) instead of split_required. Only a section that alone is over
  the budget keeps the file split_required (issue #2).

### Fixed

- A split_required result lost its size: the signed result, the ledger review.result row, review-file --wait (now stop:
  split_required) and code-forge review output now carry tokens_in and budget (issue #2).

## [0.7.1] — 2026-10-07

### Fixed

- A structured clean pass (passed: true, no findings, every hunk acknowledged, a non-blank summary) is no longer refused
  as too_short on a larger diff: it needs 40 output tokens whatever the diff size (clean passes measured 108-121 tokens;
  issue #6). An answer with any finding, or a blank summary, keeps review.min_tokens_out.

## [0.7.0] — 2026-10-07

### Fixed

- block close checked no file at all when the block's owned_files were directories, or when the coder's work was
  committed before block open or done in another worktree, and then closed with findings open. The gate's file set is
  now the owned diff plus every owned file with review rows for the block, an owned entry with no glob owns the paths
  below it, an empty diff with review rows prints a WARN naming the likely cause, and a block with no such file refuses
  no_changes.
- block close no longer passes a file whose approval a later review of the same content overrode: it refuses
  not_approved <file> <finding> per open finding until the file is fixed and approved again or the finding is waived (a
  critical only by the human's block waive; the severity the review recorded decides). The ledger review.result row now
  carries approved and every open finding with its worst severity (a finding without a valid id as X<n>); an
  "approved" result that lists a critical is recorded as not approved. An old ledger's review.result row is judged by
  the ticket's signed result file, and refuses when that file is missing or fails its MAC. Triage never turns a
  critical finding into a nit, a late-finding ruling clears only a warning or nit ruled nit, and review_cap stands
  unless an approval of the current content comes after it.

### Security

- block close refuses unowned_change <file> for a file outside the block's owned files that changed after block open
  (block open now records the tree) and that no other block of the run accounts for; it used to drop such a file from
  the gate silently. Only .code-forge/ is exempt, and a file an open sibling block owns gets a WARN line naming it.
  --no-require-reviews now needs the owner to confirm at a terminal: it has no --yes, exits 2 without
  a terminal and is refused while an autopilot grant is active.

## [0.6.2] — 2026-10-07

### Changed

- Docs and skill: the project root rule in `docs/how-it-works.md`, `docs/concepts.md` (new "Project root" row),
  `skill/references/ledger.md` and the `project.slug` note in `docs/reference/config.md`; `skill/SKILL.md` and
  `skill/references/plan.md` say a flag is checked only with `grep -rn -- <flag> <source folder>`.

### Fixed

- README: the 0.6.1 notes are labelled as released (they said "Next release").

## [0.6.1] — 2026-10-07

### Added

- Skill rule R17 "Never relax a rule alone": the orchestrator and any delegate never change, relax or remove a limit,
  threshold, budget, forbidden entry, gate or review requirement without the owner; when a limit blocks work they stop,
  show the data, propose options and wait. The skill also asks to supervise cost continuously (budget.usd, spend after
  every block close).

### Fixed

- A verb run from a subfolder uses the project root (the git root, or the nearest regular `.code-forge.yml` below it;
  outside git never above HOME) for its config and ledger slug: `jev`, `plan`, `author`, `facts`, `spawn`, `s2` and
  `run start`. A `.git` at HOME (a dotfiles repo) does not make HOME a project.
- `plan check` reads the list form of the "cannot back" section that the template shows, still reads the old table
  form, and splits clauses on `;` and on ` · (n)`.
- `facts`: a flag claim is proved only by `grep -rn -- <flag> <source folder>` (hits in source files under that
  folder; docs, plans, tests, fixtures and the brief never count). The delegate never runs `--help`, `-h`, `--version`,
  a pipe, a list or `rg`; grep options come from a fixed allow-list. No Claude allow rules are added.
- A command claim written without its CLI (`run reload`) gets the project's own bin only when the brief names that bin
  and the first word is not a well-known CLI.
- Two CI tests no longer fail on a slow Node 22 runner: the worker-restart test waits for at least one review session
  (the worker runs up to `review.parallel_tickets` at once), and the README test waits for the worker to exit after
  `run end`.

## [0.6.0] — 2026-10-06

### Added

- `code-forge autopilot start|status|stop` (issue #5): a time-boxed grant (at most 24 h) of owner decisions — allow
  `waive:warning`, `waive:nit`, `round:extra`, `model:choose` — with a fixed deny list in code (critical or proof
  waivers, skipping reviews, any limit or rule change incl. parent keys, plan/design approval, merges, destructive
  actions, budget raises). Signed grant, stop and expire rows; expiry is checked at every command. Coders may not run
  it.
- `code-forge autopilot ask`: a closed-book delegate (never Codex) answers one owner question inside the grant; every
  answer is one signed `autopilot.decision` row. It acts only when the question offers at least 2 options, the answer is
  one of them, it is within scope, does not ask to escalate, and its confidence is at least `autopilot.min_confidence`
  (default 0.7); otherwise the question goes to the owner (exit 3). `autopilot.*` counts as a limit key.
- `code-forge autopilot waive|round|level`: actions that need the delegate's acted decision (`--decision <id>`, same
  grant, scope and block/file/finding, at most 30 minutes old, with the matching word: `waive`, `allow` or the level).
  Waivers cover warnings and nits only (severity read from the signed review rows) and are written `by: autopilot`; each
  opens one tracked GitHub issue in the project's repo. The block gate accepts an autopilot waiver only under a grant
  and decision that covered it. One extra fix round per file per grant; the coder level comes from the Jev lane, capped
  at the plan level + 1 and L2.
- Autopilot budget stop per category: with `--budget review=<usd>,coding=<usd>`, new sessions in a category are refused
  once its spend since the grant started plus running reservations reaches `--stop-at` x cap ("autopilot paused: ...
  waiting for the owner"), with one signed `autopilot.pause` row; `autopilot status` shows spend per category.
  `code-forge autopilot approve --key --value --until` (owner only, in a terminal, no `--yes`) changes one setting until
  a time and restores it automatically, crash-safe, with signed rows; a value changed by hand meanwhile is left alone.
  While a grant is active, `run reload` refuses any limit key.
- `code-forge autopilot binnacle` and `autopilot log`: the logbook of an autopilot run, in the shape of an overnight-run
  page (status at a glance, decisions with why and what would reverse them, blocks, open questions, actions only you can
  take, incidents, timeline newest first) plus a full log of every autopilot event. As JSON or as
  `autopilot-binnacle.md` / `autopilot-log.md` in the run dir, rewritten after each delegate decision and scrubbed.
  `--link <https url>` stores the live page's link.
- Autopilot log page for your return: the skill (`skill/references/autopilot.md`) creates one Claude Docs doc per run
  with a Binnacle tab (the title and byline, then 7 sections) and a Full log tab, stores its link first, updates only
  the changed sections after each decision, block close or incident, and at stop adds the final rows and asks you to
  review the open questions and your actions. `autopilot status` and `stop` print the link (or the Markdown file paths
  without the Docs connector). Every owner stop in the skill says what autopilot may do there or that it is never
  delegated. New `docs/autopilot.md`.

## [0.5.1] — 2026-10-04

### Changed

- Docs: README "What's new in 0.5" (with the bench numbers), the 0.5 config keys marked, how to install a tagged release
  before it is on npm (getting-started), and how maintainers update their own install after a release (releasing).

## [0.5.0] — 2026-10-04

### Added

- Per-provider session limit `review.provider_concurrency` (defaults anthropic 4, openai 2, xai 2) and rate-limit
  backoff: a rate-limited session waits about 2 s, then about 6 s (±30 %), before retrying the same step, instead of
  retrying at once. New in-process keyed locks and semaphores (`src/util/locks.mjs`) prepare parallel review.
- Parallel review in the worker: `review.parallel_tickets` (1–16, default 3) review tickets run at once, oldest first.
  The same file is never reviewed twice at once; a block's budget row and its one L3 rung are taken under a block lock;
  a crashed ticket never stops the others; on stop, running tickets finish. `run reload` can change the pool size. `1`
  keeps the serial behaviour.
- `code-forge doctor` shows a `review concurrency` row (tickets at once and provider slots); maintainers can measure the
  pool with `npm run bench:review` (6 fake 2 s reviews: about 12 s at 1, about 4 s at 3).

### Changed

- Skill: a plan that passed `plan check` is shown to the owner for approval in the harness's plan mode (Claude Code:
  EnterPlanMode → plan file → ExitPlanMode); no block is dispatched before that approval.
- `budget.usd` holds when sessions start together: each session reserves its estimated cost under one lock (after taking
  its provider slot) and releases it when its row is written or it fails. A refusal caused by running sessions says how
  much they hold and this session's estimate; a session whose estimate alone is above the budget says so.
- `code-forge review` enqueues every file first, then waits on all of them together under ONE deadline for the whole run
  (`--max`, default 900 s, now a run deadline, not per file), prints each result on stderr as it finishes, and keeps the
  final table in file order. At most 8 wait children run at once.

### Fixed

- `code-forge facts` verified nothing: delegates echo claims as `<kind>: <token>` and every answer was dropped as "no
  answer". Answers now match by that form or by fact id (one answer per claim); a check that breaks the read-only rules,
  or a silent command (`test`, `grep -q`), marks only that claim unverifiable with the reason instead of refusing the
  whole sheet; the `<cli> <subcommand> --help | grep -c -- <flag>` check is allowed only for CLIs the brief names, never
  interpreters or launchers.
- Two files of one block could both take the block's single L3 patch rung (the rung was recorded only when the patch
  check saved); a pending `next: patch` on a sibling file now counts as used.

## [0.4.0] — 2026-10-02

### Added

- `budget.usd` per run: every session row carries an estimated `usd`; at 80% one warning, at 100% no new session starts
  (fail closed if the spend cannot be read). `report` shows running totals for open blocks and per run; `run status`
  shows spent and budget; `ledger add coder --usd` records cloud or manual coder spend.
- `code-forge run reload --run <r>`: re-read `.code-forge.yml` mid-run without stopping blocks. Queued reviews keep the
  old config, new ones use the new one; keys fixed for the run are refused by name; a signed `run.reload` row lists the
  changed key paths (never values). Coders may not run it.
- `block close --report <file>`: a coder report without a `reviewed <path> ticket <id>` line for every changed file
  counts as FAILED (new `skill/templates/coder-brief.md`).
- Error reports: `logs report --with-doctor` adds a cleaned setup check; each logged error lists the names of the last 5
  commands (no flags or values); recovered problems are logged as warnings (1Password retry, review retry, System 1
  fallback, budget at 80%), shown by `logs` and `logs summary`, and included in a report only with `--include-warnings`.
  `CODE_FORGE_NO_ERROR_LOG=1` turns all of it off.
- Known fixes: `logs report` checks a shipped table of fixed errors and, on an older version, tells you to upgrade
  instead of filing (`--force` files anyway). An `error-triage` GitHub Action labels error reports by fingerprint,
  comments once when the error is already fixed, and writes a weekly count. Maintainers record fixes with `npm run
  known-fix -- add`.

### Changed

- Escalation and review cost rules from the first real run: after one review round that leaves 2+ warnings (or any
  critical), the next fix climbs one level (`escalation.*`); docs and contract blocks code at L1 or higher
  (`levels.coder_floor_docs`); risk 0–1 gets a single reviewer (`review.single_reviewer_max_risk`); docs blocks skip
  multimodel review unless `review.multimodel_for_docs` is set.
- `plan check` refuses a block whose level does not match its recorded lane (`jev ask lane --block <id> [--plan
  <file>]`, or `--rules` without Jev) and prints the lane per block. The plan author gets the exact required headings
  from one shared list; a close heading at the right position is accepted with a WARN. `jev ask` now writes to the
  project's ledger slug (it used to default to `code-forge`).

### Fixed

- `validate` rejects an effort the level's provider cannot take (for example `xhigh` on an openai level), for levels and
  fallback entries, instead of failing when the session starts. The builders share one list of allowed efforts, and
  messages never repeat an unknown value.
- Stuck review sessions: a session that times out is retried once before the review is reported unavailable;
  `review.session_timeout_s` now defaults to 300; the ledger note keeps a cleaned stderr tail and the stdout event types
  (never stdout text) so a hang can be diagnosed; a live worker with a fresh heartbeat is never reported as
  `worker_down`.
- `ledger tail -n <count>` limits the output (only `--n` worked). Jev score criteria keys must be exactly 0..n, with a
  clear error otherwise.

### Security

- Codex (openai) is refused for closed-book roles — reviewer, judge, S2 and plan author — because it always has a shell
  that can read files. `validate`, `resolve` and `doctor` say so. Set `review.allow_open_book_codex: true` to accept
  that risk and keep using it (with warnings). Codex coders are unchanged.

## [0.3.2] — 2026-10-01

### Fixed

- Jev: `score` questions (such as `risk`) send their criteria as a list indexed by score. Jev refused the keyed object
  with 422, so `doctor`'s Jev check failed and every `risk` call silently fell back to rules.
- Review: on a small diff (20 added lines or fewer), a clean pass (passed, no findings, every hunk acknowledged) needs
  only 40 output tokens instead of `review.min_tokens_out`, so a correct short review of a tiny file is no longer
  refused as `too_short`. Any finding, or a larger diff, keeps the full floor.

## [0.3.1] — 2026-10-01

### Fixed

- Claude sessions: the `--json-schema` passed to the Claude CLI no longer carries the top-level `$schema` pointer, which
  the CLI rejected ("no schema with key or ref"), so every Claude reviewer session failed at once and each review came
  back unavailable. Validation still uses the full schema.
- Grok sessions: the answer is read from Grok's `structuredOutput`/`text` envelope, so Grok reviewer, author and System
  2 calls no longer fail schema validation (`invalid-output`).

## [0.3.0] — 2026-10-01

### Added

- `code-forge logs`: every failed verb is logged (scrubbed, flag names only) to `~/.code-forge/logs/errors.jsonl`;
  `logs`, `logs summary`, `logs clear`, `logs path`, and `logs report` to share selected errors as a public GitHub issue
  after you see the full text and say yes (`gh` or a prefilled link; a last secret check stops it). Opt out with
  `CODE_FORGE_NO_ERROR_LOG=1`.
- Better error reports (`code-forge logs report`): a second, AI cleaning pass (a closed-book L1 session that only lists
  names, hosts, URLs, accounts, paths or secrets to hide; code-forge replaces them, and you see counts only),
  fingerprints that group the same error in `logs`/`logs summary` and find an earlier issue (add a "happened again"
  comment instead of a duplicate), a version check before reporting, Send · Edit in my editor · Cancel, a GitHub issue
  form, and a one-line hint after a failure in a terminal; see docs/privacy.md. `--no-ai` skips the AI pass with an
  extra yes.

## [0.2.3] — 2026-10-01

### Fixed

- 1Password: code-forge prints a line before each call that may make 1Password ask for approval, and waits 60 s instead
  of 20 s, so a Touch ID or password prompt no longer times out while you look for it.

## [0.2.2] — 2026-10-01

### Added

- 1Password item ID or item link accepted for the Jev key (init question, --jev-ref, keys set --op, keys test --ref):
  code-forge finds the vault and key field and saves a full op:// reference of IDs; 1Password failures (CLI missing,
  locked, timeout, not found, bad output, no key field) get one clear message, a retry/skip menu in init, and never
  write a partial config
- code-forge tools: lists the recommended tools (claude, codex, gemini, grok, op, solo) as installed with their version
  or missing with the install command; code-forge tools install [<id>...] [--yes] [--dry-run] installs the missing ones
  after one yes. init step 1 and doctor use the same tool table; doctor adds an INFO line naming the missing tools.

### Changed

- Releases: CI no longer publishes to npm. Publish with `npm publish` from your machine, then push the tag; the tag push
  only creates the GitHub release (`.github/workflows/release.yml`).

## [0.2.1] — 2026-10-01

### Changed

- **Two publish paths:** publish by hand with `npm publish` before pushing the tag (the tag run sees
  the version on npm, skips its own publish and still creates the GitHub release), or push only and
  let CI publish with provenance (needs `NPM_TOKEN`). See `docs/releasing.md`.
- `init` no longer asks for gates or proof settings one by one: it reads them from the project
  (gates from `detect`, `link_dirs` from the project's manifests, `copy_untracked` only for files
  that exist, high-risk paths blank, isolation `export`), prints a "Project settings (detected)" summary that marks every blank
  and names the keys to edit, then asks one choice: Use these, Customize now (the eight questions,
  pre-filled) or Leave for later. `--no-interaction` asks nothing; agent JSON gains `settings` and
  `blank`. Every question has a one-line hint; a re-run fills a gate that was blank and keeps
  hand-set ones.

## [0.2.0] — 2026-09-26

### Added

- **`code-forge review`** — review only: runs the review engine on changes you already have (your
  branch against the merge base with the default branch, your uncommitted work, or `--files`), with
  no plan, coder or proof, and never closes a block. One verdict per file, a fix list, totals or one
  JSON document (`--json`); exit 0 only when every file is approved. The run and its worker are
  always ended, also on Ctrl-C, SIGTERM and timeouts. See `docs/review-only.md`.
- **Release tools:** `npm run release -- <patch|minor|major|x.y.z>` bumps every version location,
  moves the changelog, runs every check (with rollback), commits and tags; `npm run changelog --
  <type> "text"` records changes as you go. See `docs/releasing.md`.
- **GitHub Releases:** a tag push publishes to npm and then creates the GitHub Release from the
  changelog, with links to the npm version and the changelog.

### Changed

- **Reviewers see the acceptance clauses.** Every review packet now carries the block's acceptance
  clauses (redacted, cut to fit the packet budget); a block without readable clauses is still
  reviewed, with `(acceptance unavailable)`.

### Fixed

- CI is green on Linux: a macOS-only case-insensitivity test now skips on case-sensitive
  filesystems, and the Node 22/24 matrix no longer cancels one job when the other fails.

## [0.1.0] — first release

The `plan` → `harden` → `code` → `review` pipeline for cross-model, parallel, evidence-gated
feature delivery, as one CLI (`code-forge`) plus one Agent Skill.

### Added

- **CLI verbs:** `init`, `doctor`, `run`, `block`, `worker`, `review-file`, `spawn`, `s2`,
  `author`, `facts`, `plan check`, `proof`, `gates`, `jev`, `keys`, `ledger`, `report`, `resolve`,
  `models`, `list`, `remove`, `upgrade`, `validate`, `help`, `version`. Verbs are discovered from
  `src/cli/*.mjs`, so adding one never touches the router.
- **Setup wizard** (`code-forge init`): nine steps (tools, harnesses, provider matrix, multimodel,
  keys, engine, gates, proof, doctor), every question has a default, `--no-interaction` plus one
  override flag per answer, an agent gets one JSON line, a second run changes nothing.
- **Decision layer:** System 1 (Jev) answers typed questions in well under a second; System 2 is a
  fresh top-level session woken only when System 1 is unsure; deterministic escalation precedence
  (retry → escalate one level → System 2 hand-off → the top level patches once → stop for a human).
- **Review engine:** a file-done trigger starts one or more fresh, isolated reviewer sessions
  (adaptive by risk score, or two-provider consensus with a judge); a worker process runs review
  outside the coder's own sandbox so review keeps moving even when the coder has no network; fix
  rounds converge under a bounded rule (round ≥ 2 reviews only the fix hunks, the open finding
  count must strictly shrink every round, four rounds is the cap).
- **Two-tier proof policy:** `light` and `high` (by risk score, a configured path, or a
  security-sensitive change); a red→green runner proves a new test actually fails before the fix
  and passes after. **No tool-made mutation testing ships in 0.1.0** — see "Removed" below.
- **Engine adapters** for Claude Code, Codex and Grok (subprocess argv snapshots, forbidden-list
  rendering, closed-book sessions with the packet on stdin); Gemini/Cursor/Copilot get detection
  and skill linking only.
- **Ledger and report:** an append-only, signed, per-project ledger (`~/.code-forge/ledger/`) and
  a `code-forge report` verb with a terminal view and a `--export` JSON snapshot.
- **JSON Schema** for `.code-forge.yml` (`schema/code-forge.schema.json`) and a generated reference
  at `docs/reference/config.md` (`node scripts/gen-config-doc.mjs [--check]`).

### Security

- **`signer: same-user boundary only`** — signed ledger rows detect tampering by a different user
  or a rewritten history; they are not a defense against an attacker who already runs code as you.
  `doctor` always prints this line so nobody mistakes the signature for more than that.
- **Keys never reach a coder process.** Provider/Jev keys resolve only inside the worker or the
  orchestrator's own CLI calls (chain: `env:<NAME>` → OS keychain → 1Password, cached briefly →
  ask at setup only); `keys list` never prints a value; every output path is redacted.
- A **forbidden-command list** blocks a coder from starting its own worker, waiving a review, or
  running other orchestrator-only actions, rendered into each provider's own deny-list mechanism.

### Changed / decided late in the build

- **Q16 = cut (2026-09-25):** code-forge ships **no user-facing mutation-testing tool**. Proof for
  both the product and this package's own tests is plain unit/feature tests plus the red→green
  runner — exact assertions on the main behaviour, not "mutation-verified" coverage.
- **R10:** at most 2 coders run at a time (down from 3) for the rest of the build.
- **R13:** code-forge never runs against, installs into, or names any of Ricardo's own projects;
  the first real, end-to-end use is a neutral example repository shipped inside the package.

### Removed

- `@stryker-mutator/core`, `stryker.config.mjs` and the CI mutation-baseline job — this package
  builds itself with unit and feature tests only (R14); nothing in this repository's own history
  ran mutation testing.
