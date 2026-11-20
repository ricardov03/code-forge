# Changelog

All notable changes to `@codedology/code-forge` are recorded here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions follow semver ahead of a 1.0.0 that
waits on a second real consumer (plan §10.4, Q17).

## [Unreleased]

### Changed

- Clarify the one Go flaky-test retry: a single `go test -count=3` command executes each failed top-level test three times, and all three must pass ([B958 docs](docs/native-engine.md#flaky-test-limit-per-run-b958); B1003).
- Add the [portable deadlines guide](docs/portable-deadlines.md) for `gates run --timeout-ms`, `review --max`, `review-file --wait --max` and `ci watch --timeout`, with the shipped-command audit: this base contains no GNU `timeout` invocation to replace; Go timeout flags and result labels remain valid (B1020).

- `run start` and `block land` set `merge.union.driver` to code-forge's `land merge-driver` in the clone's shared git config when `CHANGELOG.md` is `merge=union`, so a plain `git merge` of lane branches folds repeated CHANGELOG headings with no manual step; the line falls back to git's union merge when the binary is gone or older, and a driver of your own is kept (B990b).
- Go block gates run changed-file, block-id and acceptance tests by name per package with `-count=1 -trimpath`, scope vet/build to those packages, and record `changed tests only (N tests)`; `--changed --packages` restores package runs. Named gates pass big runs through separate bounded FIFO slots; land never reuses their evidence and CI keeps the full gate. When an affected package has no selected test names, the gate runs the declared command with its ordinary changed scope instead of a compile-only `-run '^$'` command, preserving failing tests and flaky rerun evidence (B1009).
- Regression tests cover finish-timing reset on an unavailable answers retry and the one automatic retry (`.retried`): both clear `finished_at` and `duration_ms`, preserve `started_at`, and the worker serves the automatic retry again without recovering an interrupted finish (B944).

### Fixed

- Pin combine-test identity in repository config so trial commits work without machine credentials; lane combine checks author and committer identity up front and gives repo-local configuration steps (c28c).

- Recognize lane start, ready and combine in the handover vocabulary while hiding private argument values (c28c).

- Give the auxiliary Added entry in the B795 batch-land fixture its own block ID, preserving autonomous heading union and the B1018 duplicate-entry guard (c28c).

- Regenerate command references and B801 example pins for the lane ready JSON example (c28b).

- Record the eight former oversized Markdown stops from Go as bounded, approvable review/recheck parts; retain every other golden case unchanged (c28b, B1019).

- Refresh close usage expectations for the supported --landed <sha> option (c28b).

- Keep the unknown-verb regression aligned with the intended lane command (c28b).

- Document a runnable JSON example for lane ready in the command reference (c28b).

- Lane ready/combine usage errors print the command usage on stderr with exit 2 (c28b).

- Adaptive reviewer routing skips closed-book-incompatible Codex candidates before ranking, exploration, pinning and retry reuse, immediately uses a compatible configured alternative and signs replacement route choices (B1027).

- File-only `cf-gotest` calls infer test names per package without `CODE_FORGE_TEST_RUN`; a package with no selected test names refuses before any child starts (B1026).

- The local lane ledger checker accepts findings resolved through answers only when every finding and both file hashes match the committed content (B1024).

- Named block gates include the two B801 help-reference contract tests when CLI flag literals change, catching missing flag meanings before combine (B1023).

- `regen-docs` records examples and other pins before building references, so one write pass reaches a checked fixed point (B1022).

- The CHANGELOG union driver keeps new lane entries in `[Unreleased]` when the lane predates a release, preserving shipped entries and multiline details (B1021).
- Landed close no longer trusts a lane’s recorded `base_branch` as integration evidence, and supports merge commits against their first parent while preserving ownership and signed evidence gates (B1025).

- Merge and release checks flag near-duplicate `[Unreleased]` entries with the same leading or trailing block ID; the installed driver preserves the refusal instead of falling back to plain union (B1018).

- `block close --landed <sha>` reconciles an already landed block commit against its original signed evidence, records `landed_sha`, and preserves human checks despite later combine edits (B1017).
- Echoed `go test` commands must start the output line, allowing a wrapper label or shell trace prefix; prose and test failure text cannot trigger flaky reruns. Regression coverage also rejects unreadable load overrides (B1008).

- Batch land explicitly preserves the rebased tip stopped for CI on a landed prefix, while restoring every other unlanded rebase and its recorded bases after a gate failure (B1007).

- Land searches for older CI PRs whenever the newest 200-row page is full, even with matching PRs there, retaining and deduplicating matches across search and exact-branch results (B1006).
- Oversized Markdown review sections split into paragraph and diff-line parts, including deleted and headingless files, preserving diff coordinates and no-newline markers; every part must pass and each recheck finding belongs to one part (B1019).

- Audit all 55 v0.27.0 entries against the code; fill in maintainer test and generated-doc guidance, correct CI gate reuse and autopilot backlog instructions, and link each entry to its documentation (B1015).
- A queued review ticket of a closed (or landed) block for a deleted file (`content_hash: "deleted"`) is no longer served: the worker drops it before any review session as `stale` with reason `block-closed` (signed result, done marker and one `review.result` row, which the gate counts as never reviewed). A rebase-check ticket and a ticket for a file that exists are still served. B919
- The coder ref guard no longer turns itself off when a project has its own `reference-transaction` hook or a husky `core.hooksPath`: the guard runs first, then that hook with the same arguments and stdin, and the move fails when either fails; the work tree is not written (B809).
- `doctor` in a repository with no `.code-forge.yml` shows the `ref guard` row too, once a coder worktree was opened there (B808).
- The recorded command reference examples (`help_reference_examples.json`) are the same on every machine: `queue --json`'s `changed_slots` (from the load probe) and a context pack's `tokens` (from the temp path length) are masked, and the autopilot examples no longer depend on when a background review worker asks the owner, so `sh scripts/cf-release.sh regen-docs` gives the same file anywhere (B1004).
- In a linked-worktree run (`run start --cwd .` there), the commands that read the run's files use the run root, not the main tree: the context pack and `spawn --run --block` read the block brief and config there, `doctor` reads that worktree's worker registry, the hand-over worker guard lists the lane's workers, and land's review join, close's requeue and the block wait use the run root (B1005).

### Added

- `lane ready` verifies committed lane ownership, signed review/close evidence, history and size; `lane combine` reuses land merging, named tests and one regeneration for local integration without rebasing lane branches (B1010b).
- `lane start --base <combine-tip>` starts a brief run on a clean lane branch with a pinned combine base, at most two blocks, existing named Go gates and local close/review; lane landing refuses so the wave integrator can combine it. Ready-lane verification and combine automation remain B1010b (B1010a).
- `block open --shared <files…>`: two open blocks of one run may both own an append-only file (CHANGELOG.md, a docs table) when both name it with `--shared` (an exact path also in `--owned`); the run record keeps `shared_files`. A file only one block shares, and every other owned file, still overlaps, and each block's reviews and close checks stay its own (B994).
- `land.regenerate`: a list of commands a batch land runs once on the combined tip, after the merge and before the push, committing what they changed as one commit on main (for example `sh scripts/cf-release.sh regen-docs`); a failing command stops the land at `regenerate`, unpushed. Init writes it as `[]` (B959b).
- `human-pass:evidence` and Go-only `block auto-pass` clear non-design checks under a frozen owner opt-in and active signed grant, after proof/evidence hashes, separate prover and green machine checks are verified again under the run lock; partial passes preserve owner checks and all records identify autopilot (B1012).
- Add the off-by-default, snapshot-frozen `human_check.autopilot` configuration and explicit `approval: design|device` human-check markers; init, schemas and config fix preserve the owner's policy (B1011).
- Owner audit and reopen for signed autopilot evidence passes, distinct status/log attribution and report `autoPassed` without human wait; binnacle reads redact the run key once audit verification loads it, including later reads after expiry (B1013).
- Opt-in `block rebase --accept-identical <file> --source <path-or-ref:path>` clears changed owned binaries without a terminal under a signed `rebase:accept-identical` grant only when their bytes equal an approved source; the source list is frozen in the run snapshot, text stays on re-review, and signed audit rows name autopilot and both hashes (B1014).
- A red batch merge gate on the combined tip now queues ONE found fix block of the run (source `land`): its brief lists every failing test (Go, subtests, build failures, TAP, spec) with its suspect blocks (block id in the test name, changed file or package, else the bisect culprit) and each block's changed files; the same red again queues no second block (B963)
- New key `land.min_free_gb` (default 15): `block land`, `gates run` and `run start` check the free disk first; under twice the minimum they remove Go build cache files unused for 24 h, under it they refuse with the next step before anything runs; `doctor` shows a `free disk` row (FAIL under the minimum, WARN under twice it); 0 turns it off (B961)
- New key `land.after` (default `[]`): `block land` (single or batch) runs these shell commands in the main checkout after the merge and before the push (for example `node scripts/build-native.mjs --host`, `npm run -s docs-block -- stamp`) and commits their changes to tracked files on main in one `land.after for <ids>: <commands>` commit; a failing command stops the land at step `after` with its output, the merge kept and nothing pushed. Every landed block gets one signed `block.landed` row, shown as **Block landed** in the autopilot full log (B960)
- `block land` (single or batch) refuses a block that adds a file over the new key `land.max_file_mb` (default 5 MB, `0` off) at step `size` before the merge, and every land push (main with `--push`/`land.push`, the `land.require_ci` CI branch) refuses commits the remote does not have that add one, before anything is sent, naming each file with its size and the fix; a refused main push keeps the local merge and queues no heal item. The binaries a `go build` in `native/` writes are gitignored (B986)

## [0.27.0] — 2026-11-20

### Changed

- code-forge's own test gate (`cf-gotest`) runs `go test` with `-trimpath` added to `GOFLAGS`, so a new worktree reuses the Go build cache of the others instead of compiling every package again (measured: second worktree `go test -c ./cmd/code-forge-native` 1.1 s and 0 compiles with it, 13.5 s and 48 compiles without); CI runs the Go tests the same way, and the B728 golden tests find their fixtures under `-trimpath` (B952)
- `cf-gotest` limits its own load: `go test -p 2` by default (`cf-gotest --p N [file ...]` sets N), and when the 1-minute load average is over the core count (Linux `/proc/loadavg`, macOS `sysctl vm.loadavg`) and no `GOMAXPROCS` is set, the go child gets `GOMAXPROCS=cores/2` and the gate prints `cf-gotest: load <n> is over <c> cores: GOMAXPROCS=<k>`; a bad flag is a usage error, exit 2 (B953)
- `cf-gotest --shards N [--p N] [file ...]` runs one package's tests like CI's shards on a laptop: one `go test -c` build, the test list from that binary, the split of `scripts/ci-shards.sh` (new `pick I N`, the same round-robin as CI), N runs of the binary with disjoint `-test.run` sets at most `--p` at once, each shard's output whole in order, red when any shard is red; no file shards `./cmd/code-forge-native`, a package with no tests is red (B807)
- `CODE_FORGE_INIT_GOLDEN_RECORD=1` records the init goldens (`testdata/b528`, `b611`, `b703`, `b726`, `b762`) again from the Go output in one `go test` run, writing only the goldens that differ, so a config default change no longer means editing about 69 files by script (B989)
- `gates.ram_tmp` defaults to `off`; `auto` stays opt-in. Measured on the `worker`, `review` and `gates` tests (`go test -count=1 -p 2`, 3 interleaved rounds each), the RAM disk was 1.2% slower in the sum of medians (141.08 s vs 139.36 s on the normal disk; per package +3.3%, −2.2%, +0.8%), short of the 10% gain a default-on needs. The measurement was not controlled: other sessions kept the 1-minute load at 18–86 (the spread inside one side, 72–88 s, was larger than any A/B gap), fseventsd was watched by memory only (8.5–11 MB on both sides, no CPU figure), and the volume was not remounted after `.fseventsd/no_log` was written; measure again on a quiet machine, with a remount and fseventsd CPU, before any default-on. `code-forge init` writes `ram_tmp: off` into a new config, and a config that sets `auto` keeps it (B940)
- The doctor `state-ignored` row asks git twice instead of three times: the tracked-files check is one `git ls-files --full-name` with a `:(top)` pathspec, with no separate `rev-parse --show-toplevel`; the row text is the same (also used by init and upgrade) (B898).

### Fixed

- `review-file <file> --answers <ids> --of <source>` queues its answers ticket in Go after an ordinary findings review: the first check of the open ids reads the block's review rows by the close's own rules (unverified, like the old Node check), so a `review.state`, `review.reopened` or `review.cap` row no longer hands the command to the removed Node engine (`enqueue-answers`); an id that is not open is still `answers-not-open` (B992)
- `block close` and `block close --dry-run` of a run started in a linked git worktree (`run start --cwd .` there) no longer refuse it as `other-workspace`: the close's review requeue check takes the worktree's top as the run's root, like `review-file` (B969); a run of another repository is still refused (B991).
- Land merges a docs table row to which both sides only added sentences (the `block land` row of `docs/native-engine.md`): both sets are kept, main's first, instead of a conflict; `block rebase` and batch land refuse a replay whose `CHANGELOG.md` would repeat a `### Kind` heading in `[Unreleased]` (B890)
- A land merge, batch rebase or `block rebase` that git finishes without a conflict but that would leave two `### Kind` headings of one kind in `CHANGELOG.md`'s `[Unreleased]` folds them into one heading, every line kept, instead of red CI on the changelog shape (B813).
- Land keeps both sides of the two merges that dropped B800–B802 and B877/B870 from a batch (a second `### Added` in `[Unreleased]`, a `gates run` row both sides appended to): one heading per kind, every sentence kept; checked on the real files (B936).
- Batch land and `block rebase` take only a block's own commits: a commit main already has by patch-id (an earlier block's commit the branch carried) is no longer replayed into a conflict, and a `land.auto_resolve.generated` doc both blocks added is regenerated in the rebase instead of parking the block (B938).
- `block land --merge` finds a merge conflict it cannot resolve before join, gates and close: it refuses with the conflicting paths, a `land.conflict` row and `next: code-forge block rebase`, and the block stays open instead of being closed first; with `land.auto_resolve: false` every conflict stops it, as land resolves none then (B814).
- This repository's `.code-forge.yml` sets `land.auto_resolve.generated` for `docs/reference/commands.md`, `commands.json` and `config.md`, so a land re-generates them instead of stopping on a conflict there (B815).
- With `land.require_ci: github`, a batch `block land` no longer runs a block's local `test` gate when the sha it lands is the sha GitHub CI proved green: a rebase that made a new sha pushes it to a new CI branch (never forced) and waits for green CI on it, and the land prints `gates: block <id> tip <sha> has green CI; local test gate skipped`. Lint, types, format and the batch merge gate still run (B910)
- A batch `block land` with `land.require_ci: github` waits for CI with no block lock held, so other commands on those blocks no longer get `block-busy` during the wait: each tip's CI is read before the locks are taken, a block whose rebase made a new sha lets the blocks before it land, waits for CI on the new sha and lands in a next round, and a tip that moved during the wait is refused at `ci` (B803)
- A batch `block land` with `land.require_ci: github` lands only a block commit green CI proved: a rebased commit waits for its own green CI first, a last check before main moves refuses any other, and the block's landed marker records the proved sha as `ci_sha` (B804)
- `land.require_ci` finds a block's CI pull requests past the 200 newest: when that page is full and holds none of them, land searches `gh pr list --search head:<prefix><block>`, so an older CI PR is still replaced and closed (B805)
- One short gh network error (timeout, connection reset or refused, EOF, TLS or DNS failure, HTTP 5xx) while `land.require_ci` reads CI no longer refuses the land: the read is retried with a backoff, 3 tries in all, and only then refused at `ci` with gh's message, `(after 3 tries)` and a `next:` line (B806)
- `block open` never lets a coder build on a red base: it reads the base's CI with `gh run list --commit <base>`; with `land.require_ci: github` a red base, a base with no CI run or one still running is refused before anything is recorded, with the next step (`gh run view <id> --log-failed` or `code-forge ci watch --sha <base>`); with the gate off a red base or one with no run prints a `warning:` line and the open goes on (B920).
- Every coder context pack has a generated `## test budget (code-forge)` section and every coder spawned with `--block` a `Test budget (code-forge):` line: run only the test names you add or change (`go test <pkg> -run '^(...)$' -count=1 -p N`), no package-wide, full-suite or stress runs unless the owner asks, GitHub CI is the full gate; N is the new `gates.coder_test_parallel` (default 2), which `code-forge init` writes into a new `.code-forge.yml` after `gates.ram_tmp_mb` and keeps on a re-run (B923).
- A new worktree block with no `--base` starts from the newest base branch (the newer of local and `origin`, fetched; diverged tips refuse `base-diverged` with the next step); the context pack and the spawned coder prompt say to rebase on it just before the CI push, and plain `block ready` lists `stale_base` with the `block rebase` command while the base branch moved past the block's base (B942).
- `autopilot found` keeps the run's scope fixed: a finding goes to the backlog (state `backlog`, the next release; no block, brief or owner action) unless it is `--severity customer-breaking` or a `FOUND [customer-breaking] …` line, and only those join the current run (B921).
- A backlog finding of `autopilot found` (not customer-breaking) is also written to the workspace's next-release plan file `.code-forge/next-release.md`: one line per finding (id, run, source, day, title, first evidence line) with a fingerprint marker, so a finding seen again or found by a later run adds no second line; queued and owner findings are not written. The text output (`plan`) and `--json` (`next_release`) name the file. Before, backlog findings lived only in the run record (B984).
- `autopilot ask --speed` decides a speed-only choice at once under autopilot: the first option (listed fastest first, every option keeping every gate) is taken with no delegate session and no wait on the owner, recorded as a signed `autopilot.decision` row with `speed: true` and shown after in the binnacle; an ask without `--speed` is unchanged (B922).
- `autopilot ask --speed` no longer trusts the option order: the asker names the fastest option with `--fastest <option>` (one of `--options`), and that option is taken; `--speed` without `--fastest`, or with one that is not an option, is a usage error (exit 2) that decides nothing. Before, `--options one-by-one,batch --speed` took `one-by-one` and recorded it as the fastest (B983).
- `review` and `review-file` in a linked git worktree review that worktree: a run started there (`run start --cwd .`, or the run `review` starts itself) keeps its worker and review queue in the worktree's `.code-forge/`, and `review --run <id>`, `review-file --run <id>`, `--wait` and `--wait-block` now look for the worker there, at the top of the run's workspace, instead of the main tree. Before, every file came back `unavailable: worker_down` although the worker was alive (B969)
- Two `code-forge review` calls started together in one repository both finish: the second prints `review: waiting for review run <r> to end: its worker (pid <n>) is this repository's one live worker` and starts its run when the first review ends, up to `--max`. Before, its `run start` failed with `run <r> still has a live worker`. A worker of a run that is not a review run still refuses at once (B997)
- `review` no longer passes a review that reviewed nothing: `ok` is true (exit 0) only when every file was approved by a review of this run. A file the worker answers `unchanged` (no change against the base, no reviewer session) makes `ok` false and exit 1 with `review: not reviewed: ...`, and a file named with `--files` that has no change against `--base` in this tree is refused before anything starts: `review: not reviewed: these files have no change against --base <sha> in this tree: <files>`, exit 1. Before, 28 such files came back `unchanged` with `ok:true` (B970)
- `review-file --wait-block <id>` for a block the run does not have now says `unknown-block`: `block <id> is not a block of run <r>`, with the `block open` command that fixes it and `run status` to list the blocks; a run with no record in the repository says `unknown-run` with the `run start` command. Before, both said `block-scope-unreadable: the block scope cannot be read`. docs/review-only.md now lists `review-file`, `--fresh`, `--wait`, `--wait-block` and points to `review.parallel_tickets` (B971)
- `land.require_ci: github` judges every run of the tip per event: a red `push` run is no longer covered by a green `pull_request` run of the same workflow and sha (B948 had exactly that on main). The refusal names it as `CI (push)`; a green rerun of the same event still counts. CI now also runs on pushes to `ci-*` branches, so a lane or combined tip gets both runs (B956)
- A single-block `block land --merge` with `land.require_ci: github` gets the batch's CI rules: when the tip it lands is the tip green CI proved, the local `test` gate is skipped (`gates: block <id> tip <sha> has green CI; local test gate skipped`; lint, types, format and the merge gate still run), and a tip that moved while land waited for CI, or before the merge, is refused at `ci` with main unchanged (B979)
- A batch `block land` with `land.require_ci: github` no longer runs the merge gate's test suite on a tree green CI already proved: when the batch prefix's tree is the tree of a block tip CI proved (with blocks rebased onto the prefix before them, the batch's tree is the last block's tip tree), land prints `merge gate: tree <tree> is the tree green CI proved at <sha>; full suite skipped` and merges it (B980)
- With `land.require_ci: github`, a `gh pr create` or `gh pr close` that hits a short network error is retried like the CI reads (B806), but only after land checks whether the failed call took effect: an open PR already on the CI branch is used (no duplicate draft PR) and a PR already closed is not closed again. When the B805 `head:` search page is full, land lists each exact CI branch name with `gh pr list --head`, so an older block PR is still found (B981)
- `spawn --role coder --run <id> --block <id>` checks the CI of the block's base again before the coder starts, like `block open` (B920): with `land.require_ci: github` a base that turned red (or has no run, or is still running) refuses the spawn with the B920 text and next step; with the gate off it only warns (B982)

### Added

- `code-forge init` writes the new keys with their defaults after `autonomy`: `land.require_main_ci: false`, `autopilot.step_budgets` (`margin: 0.5`, `min_samples: 5`) and `release.max_blocks: 8`; a re-run keeps values already set, and `config fix` leaves them as they are.
- `sh scripts/cf-release.sh regen-docs` regenerates every generated doc and recorded pin once, in order: `docs/reference/commands.md`/`commands.json`, `docs/reference/config.md`, then one `go test` run recording the command summaries, the B801 doc example pins and the init goldens; it stops at the first failing step. `regen-docs --check` runs each step in check mode, lists every stale one and exits 1 (B959)
- `code-forge land merge-driver %O %A %B %P`, set as `git config merge.union.driver`, makes a plain `git merge` (a wave combine's `--no-ff` merge of lane branches) fold repeated `### Kind` headings in `CHANGELOG.md`'s `[Unreleased]` like `block land` does; other `merge=union` files get git's union result (B990)
- On macOS the gates run with `TMPDIR` on a RAM disk with no file-event log: `gates.ram_tmp: auto` (opt-in; the default is `off`, B940) makes `/Volumes/cf-tmp` once without sudo (`hdiutil attach -nomount` + `diskutil erasevolume APFS`), writes `.fseventsd/no_log` and `.metadata_never_index`, and gives every gate and test command a fresh `TMPDIR=/Volumes/cf-tmp/<run>-<block>-*`; any error prints one warn line and the gates use the normal `TMPDIR`. `gates.ram_tmp: off` or `CODE_FORGE_RAM_TMP=off` turns it off; `doctor` shows the `ram_tmp` row (B924)
- `code-forge init` writes `gates.ram_tmp` (the default mode) into a new `.code-forge.yml`, after `gates.format`; a re-run keeps the value already set (B939).
- `gates.ram_tmp_mb` sets the size in MB of the RAM disk the gates make (default 2048, 64 to 65536); `code-forge init` writes it after `gates.ram_tmp` and keeps a value already set, and `config fix` keeps it. A mounted `/Volumes/cf-tmp` keeps its size until it is detached (B941).
- Two Go tests no longer depend on machine load: the pseudo-terminal test driver types each key once the program's prompt is on screen (a `prompts` list, one text per key) instead of after 400 ms of quiet output, and the B593 strict-launch test waits for the launch and the reap with a 3-minute stop bound instead of failing at 10 s and 2 s; both pass under `-cpu 1` beside a CPU hog (B891)
- A red `go test -json` gate now gets the flaky rerun by name too: the failed top-level tests are read from its test2json events (per package, the same rules as plain output: a panic, a build failure, a failed package with no named test or more than 3 names is a real failure), rerun 3 times without `-json`, and a pass gives the `gates: WARN flaky test` line and the `test.flaky` row (B893)
- The flaky rerun of a `go run` wrapper gate (such as code-forge's own `cf-gotest`) keeps the wrapper's go test flags: `-timeout`, `-p`, `-tags`, `-race` and the other build flags are taken from the first output line where the wrapper names its `go test` (`cf-gotest: go test -count=1 -timeout 75m ./a`); before, the rerun called plain `go test` and dropped them (B894)
- A gate that is not go (`make test`, a shell script) but prints the `go test` it runs gets the flaky rerun by name too: `go test` from `PATH` with that line's build flags, `-p 2` unless set, `-count=3 -run` of the failed names and their packages, and a pass gives the `gates: WARN flaky test` line and the `test.flaky` row; a gate that prints no `go test` line is never rerun by name (B988)
- The flaky rerun of a `go test` gate no longer runs plain `go test` on a loaded machine: it uses `-p 2` unless the gate set `-p`, builds a `go run` wrapper's rerun with `-trimpath` (cf-gotest's build, so the build cache is reused; a direct `go test` keeps its own flags), and gets `GOMAXPROCS=cores/2` when the 1-minute load is over the core count and no `GOMAXPROCS` is set (B973)
- `CODE_FORGE_FLAKY_LOAD=<number>` stands in for the 1-minute load of a gate's flaky rerun, so a test proves the `GOMAXPROCS` load cap through a real `gates run` process; an unset or unreadable value uses the machine's load (B987)
- Every land, gate, coder session and CI wait has a speed budget: its measured median plus a margin, or fixed minutes under the new `autopilot.step_budgets` config key. `block land` and `gates run --run --block` time themselves; `code-forge autopilot step --run <id> --step <coder|ci-wait> --ms <n>` reports the others. A step over its budget prints `WARN step budget: …`, writes a `step.over_budget` row shown in the binnacle, and under an active autopilot grant becomes found work (source `speed`) (B950)
- `code-forge release check` counts the blocks since the last `v*` tag and prints `RELEASE NOW: N blocks since vX` at the new `release.max_blocks` config key (default 8), an info line from 3 below it; the autopilot stop report and binnacle (`Release size`) show the same line (B951)
- While an autopilot grant is active, a red gate, a land that stops, a hand-over that names the run and the coder report's `FOUND` lines (on `block close` and `block land`) record an `autopilot found` finding by themselves, deduped by fingerprint (B821)
- A queued `autopilot found` block's acceptance file lists the red test its evidence names (`--- FAIL: TestX` or a findings.jsonl `"test"`); with none it carries a `needs-test` marker in place of the old placeholder (B822)
- `land.require_main_ci: true` (with `land.require_ci: github`) makes `block land` refuse while the newest completed push run of the base branch is red, unless every failing test of that run matches a `land.ci_red_list` line of a block named in `CODE_FORGE_CI_RED_BLOCKS` (the landing fix block may cover it); a green or missing push run lands (B962)
- Maintainer tool `native/cmd/cf-redgreen --base <sha> [--out <file>] <test files>` proves a block's new Go tests red→green: it runs them on the base sha's module (from `git archive`, nothing touched) and requires an assertion failure, then a pass on the working tree; verdict PROVEN, NO_NEW_TESTS, NOT_RED, RED_INVALID or NOT_GREEN, optionally written as JSON (B957)
- A run may list at most 3 flaky Go tests: the gate whose flaky test makes it 4 or more is red, names the tests, writes a `test.flaky_limit` row and shows `flaky_limit` in its JSON; a later clean gate stays green (B958)
- `block land` runs fast checks before CI, rebase or gate: a block that adds a conflict marker, leaves a testdata/fixtures file ignored by git, adds a duplicate docs table row or a new CHANGELOG [Unreleased] shape fault stops at `fast-checks` with each problem named (B964)

## [0.26.0] — 2026-11-19

### Changed

- A red `go test` gate no longer fails a block for one flaky test: with `gates.rerun_failed` on (the default) it reruns only the failed top-level test names, 3 times in one `go test -count=3 -run '^(A|B)$' <pkgs>` call (also through a `go -C <dir> run` wrapper such as code-forge's own `cf-gotest`). All three pass: the gate passes with `gates: WARN flaky test <name> (<pkgs>): failed, then passed 3/3 reruns (load <n>)`, a `test.flaky` ledger row `{test, packages, gate, load, reruns}` (signed in `block land`) and a `flaky-test` finding in `~/.code-forge/runs/<run>/findings.jsonl`, so the flake gets its own fix. Any rerun failure, more than 3 failed names, a panic or a build failure stays a real failure (B798)
- `proof red-green` proves PHP (Pest and PHPUnit), `proof.runners` (junit, swift, xcodebuild) and the default Laravel/Pest runner in the Go engine: no more `not supported in this case yet (proof-runner)`, so a high-tier Laravel block can pass the close check's `unproven` rule with a proven row instead of a human waiver. Same verdicts (`RED assertion`, `RED_INVALID import|compile|error`, `NOT_RED`, `NOT_GREEN`), the same signed row (`covers`, `red_kind`, `runner`, `failed_tests`, `env_keys`), from the export and from a block worktree (B827).
- One review ticket the Go worker cannot serve no longer stops the worker (the poison loop after `run start --add-worker`): the ticket gets its done marker with `status: "unsupported"` and a signed `review.unsupported` row with the reason, the worker serves the next ticket, and `block ready`/`block close` name the file with the reason and the fix; an autopilot grant or approval Go cannot read skips only the autopilot checks (signed `worker.autopilot_unsupported` row); every worker exit writes a signed `worker.exit {reason, detail}` row and keeps the last 50 stderr lines in `.code-forge/logs/worker-<pid>.log`; `logs handovers` shows the redacted detail; the `worker.down` line names the last exit and the fix `code-forge run start --add-worker --run <r>`. B884
- Small text and usage fixes from the B801 audit: `author` says `run code-forge facts --brief <file> first` (P10); `block rebase --accept-landed` refusals use the `block rebase:` prefix with no binary name or raw code (P11); `run end` with open blocks names `code-forge block stop <id> --run <r>`, and `--json` prints it as `next` in one `{ok:false,error,next}` line (P13); `heal list|show|run --help` and their refusals print that subcommand's own usage (P14); `run status|reload|end` with no `--run` print `run <sub> needs --run <id>` and the sub usage (P16); `block open` of an open block says `block <id> is already open` before the level lookup (P17) (B881)
- `gates run --run <r> --block <id>` whose test gate runs the full suite in the shared checkout while other blocks of the run are open prints `gates run: full suite in a shared tree (N other open blocks); for this block's tests use --changed, or proof.isolation: worktree` and `gate.done` records `shared_tree: N`; `init` for a Laravel project or `review.workers` > 1 prints the worktree hint under the isolation line (JSON `settings."proof.isolation".hint`) (B870)
- `autopilot status --json`, `autopilot log --json`, `autopilot binnacle --json`, `report --json` and `run status --run <id>` now print one compact JSON line instead of indented JSON over many lines, like every other `--json` output; the keys and values are unchanged. Files from `report --export` stay indented (B874).
- `outcome seal|check|record` on an outcome that is valid JSON of the wrong shape (for example `{"step","files"}`) or not UTF-8 now names the problem and exits 1: `Outcome "<file>" does not match the outcome schema: unknown field "step"; missing field "skill"; …` or `Outcome "<file>" is not UTF-8 text` (in `--json`, in `problems`). Before, it stopped with the `outcome-unsupported` refusal. `pipeline status` and `block open --after-outcome` name the same problem (B873).
- Every `--json` refusal now prints exactly one compact line on stdout, `{"ok":false,"error":<the refusal text>,"next":<command or null>}`, with the same exit code; the refusal text still goes to stderr. Before, `docs path --json`, `run end --json`, `block open|waive|ack-waivers --json`, `report --json`, `autopilot ratify|revert --json`, `run human-checks clear --json` and `queue run --json` usage errors left stdout empty (B872).
- `run start` while another run's worker is live in the repository now refuses before it writes anything: no run record, key, workspace mirror or signed row is left behind, so `status` does not list a run that never started (exit 1, the message names the live run and `code-forge run end --run <it>`, then `nothing was recorded`). B871
- `block land` with `land.require_ci: github` no longer needs a human between a rebase and green CI: a tip with no CI run is pushed (never forced) to its CI branch, or after a rebase to a new `ci-<block>-<n>` branch with a draft PR "Replaces #N (rebased onto <sha>)" that closes the old PR; land waits for CI on that sha up to `land.ci_wait` (default `30m`, one progress line a minute) and a timeout refuses with the same land command to resume; a red run names the failed jobs; an SSH push (CI branch or main) that fails on auth is retried once over HTTPS with the gh credential helper; after the land the block's CI PRs are closed. New keys `land.ci_wait`, `land.ci_branch_prefix` (default `ci-`), `land.ci_remote` (default `origin`) (B796).
- `.code-forge/` stays out of git: `init` (also `--no-interaction`) and `upgrade` add `.code-forge/` to the repository's `.gitignore` (created when missing, your lines and order kept, never twice) and print `.gitignore: added .code-forge/`. While git would see the folder, `run start`, `proof export` and `proof red-green` refuse (exit 1) with the fix, so the proof export's copy of `.env` can no longer show up in `git status`. When git already tracks files under `.code-forge/`, `init`, `upgrade` and `doctor` print `git rm -r --cached .code-forge && git commit` and never run it. `doctor` has a new row, `.code-forge/ ignored by git` (B828).
- A proof export or queue under `.code-forge/` no longer grows the run record: `block open` never records paths under a `.code-forge/` folder (any depth) in `tree_at_open`, and the close measure, the ready check and the fix-loop budget never list them, whatever `.gitignore` says. A dirty file elsewhere is still recorded at open and still excused at close. A run record save over the new `caps.run_record_warn_mb` (default 2 MB, `0` turns it off) prints one `WARN run record <run> is <size>, over caps.run_record_warn_mb (<bound>); largest keys: ...` line; the save still happens and old large records still read (B829).
- After the run's worker died (or left idle), `review-file` (enqueue with `--run`, `--wait` on a ticket, `--wait-block`) starts the run's worker itself, as `run start --add-worker` does (same lock, `review.workers` limit and pin; four coders at once start one worker), prints `review-file: no live worker for run <r>; started worker pid <n>` on stderr and goes on; it no longer queues tickets nobody serves. When that start fails the answer stays `worker_down` (exit 3) with `start_error` and `fix: code-forge run start --add-worker --run <r>` in the JSON and on stderr. `run start --reattach --run <r>` with no live worker starts and pins one instead of failing with `no live worker serves this queue` (B826)
- A worker started by `run start --add-worker` after the pinned worker died is pinned once: its boot re-pin and the add no longer write the same pid twice into the run's `workers`, and a reattach keeps only the other pins in `worker_history` (B905)

### Added

- The command reference is an API reference: `docs/reference/commands.md` and the new `docs/reference/commands.json` (for agents) give every command its purpose, when to use it, its flags (name, type, default, meaning), examples with their real output, exit codes, the `--json` shape (keys and types) and related commands, all generated from the Go help table by `sh scripts/gen-commands-doc.sh` (`--check` checks both); a Go test runs every example against the real binary in a temp repository with fake providers and fails when a command has no example; the skill points agents to `commands.json` (B801)
- `run status` says whether the run's worker is alive, the same way `block ready` checks it: new fields `worker_live` and `workers` (each pin with `pid`, `alive`, `started_at`; `worker_pinned` stays and only says a pin is recorded), and with no live pin one stderr line `run <r>: no live worker; fix: code-forge run start --add-worker --run <r>`; `status` prints that line too and `doctor` shows it as a `run-worker` WARN row (one probe for all). B885
- `keys set <name>` with no terminal on stdin (a pipe, `/dev/null`, an agent's shell) refuses before the hidden prompt is drawn (B880, B801 audit P15): the `set` usage, why, and `next: code-forge keys set <name> --op <item-id|link|op://ref>`, exit 2, nothing stored.
- The hidden verbs `land` and `native-status` no longer exit 1 with `has no default export function` (B879, B801 audit P07): `land` is a usage error (exit 2, the usage, `next: code-forge block land <id> --run <run>`); `native-status` serves the status records piped on standard input with exit 0, and with an argument or nothing piped prints its usage and `next: code-forge status` (exit 2).
- `facts`, `s2`, `author` and `review` show why the provider CLI gave no answer (B876, B801 audit P09): the last lines it wrote on stderr (packet lines and PEM blocks removed, keys masked) or that it printed nothing, then `next: code-forge doctor (checks each provider CLI and its login; code-forge logs --last 5 shows the error log)`. A review session that ends on an exit or a timeout keeps `detail` and `stderr_tail` in its session entry; `review --json` stdout is unchanged and the error log keeps the verdict line.
- Each bare refusal ends with one `next:` line that names the command to run (B875, B801 audit P08): a missing run record (`run status`, `run end`, `run reload`, `block open`) → `code-forge status`; a missing ledger (`report`, `ledger tail`, `ledger calibration`) → the report of a known slug, or `code-forge run start` when there is none; `heal show` → `code-forge heal list [--run R]`; `validate` with no config → `code-forge init` (or `code-forge validate` when only the `--file` path is missing); `keys test <name>` not found → `code-forge keys set <name>`. `plan check` names the plan or facts file it cannot read, and `worker` refused by `review.workers` names the holding worker's pid and run with `next: code-forge run status --run <run>`. The error log keeps the refusal itself as the message.
- `code-forge release check [--cwd <dir>] [--json]` says in one command whether a project is ready to release: the tree is clean, HEAD is pushed, GitHub CI is green for the HEAD sha, `## [Unreleased]` has entries each under one `###` heading with no line added under a released version, and `npm ls` is clean (the project's own platform packages at the previous version are ignored; only extraneous packages names `npm prune`; `npm run release` refuses with the same text, B778). It prints the bump (`### Added` entries: minor, else patch), the release steps in order (release, the platform publishes, `npm publish`, `release:site`, `git push --follow-tags`) and each failing check with its fix, and exits 0 when ready, 1 when not; `autopilot stop` ends with it as the run's final report (B799)
- Coder worktrees are guarded: `block open` installs a git `reference-transaction` hook that refuses, from the coder worktree, any move or delete of the base branch (`git branch -f main`, `update-ref`, `fetch ...:main`) and of other block branches, naming the ref and the safe command (`git fetch origin`, then `FETCH_HEAD`); only `block land` and `cf-release release`/`docs-block` pass with the guard token. Every context pack lists the git rules (no moving main, no force push, no `rm -rf`, no `git stash`, no `git reset --hard`) and the one push command. `block land` refuses at step `main-reflog` when main's reflog has entries since the last land that are not a land, release or fast-forward commit, and lists them. `doctor` shows the `ref guard` row (B797)
- `block land` merges append-only conflicts by itself (`land.auto_resolve`, default on): `CHANGELOG.md` bullets (main's lines first, then the block's, one `### Kind` heading per kind, no repeated entry), rows of the markdown tables in `land.auto_resolve.tables` (default `docs/native-engine.md`) and the `land.auto_resolve.generated` files it re-generates; the merge then runs the same gates as a clean one and writes a `land.resolved` ledger row. Any other conflict, in a single land or a batch, aborts the merge with main unchanged, prints the files with their kinds and one `next:` step, and writes a `land.conflict` row with the files and kinds. `land.auto_resolve: false` keeps the old behaviour, except that a single land no longer leaves main mid-merge (B795)
- `code-forge autopilot found --run <id>` turns a problem found during a run (source `gate`, `land`, `coder`, `flaky`, `handover` or `review`, with its evidence) into a block of the same run: `--source/--title/--evidence` records one, `--report <file>` reads every `FOUND [<source>] [<label>,…] <title> :: <evidence>` line of a coder's report, `--list` prints found → block → state. A new finding gets the next free block id, a brief and an acceptance file ("a red test from the evidence") under `~/.code-forge/runs/<run>/found/`, the `block open` command, and joins the run's planned blocks, so an until-done grant runs until it closes; a repeat (same fingerprint: title and evidence with numbers and hashes folded) is counted, not queued again; findings labelled or worded as scope, security or secrets, deletes or publishing, and those past the new `autopilot.max_found_blocks` (default 10), wait for the owner and never run. Each writes a signed `autopilot.found` row; the binnacle adds a Found work table and lists the waiting ones under Actions only you can take (B800)
- **`code-forge audit commands [--json] [--out <file>]`** runs every command with `--help` and an unknown flag in a
  throwaway HOME (stubbed tools, network blocked) and writes `docs/audits/audit-<version>.md`; `npm run release --
  major` refuses a major bump until that file is committed with no open high problem (each one names a landed block);
  `--no-checks` does not skip it, `--allow-no-audit` does with a loud warning (B802).

### Fixed

- The command reference examples no longer carry the day they were recorded: the autopilot report byline date shows as `2026-01-01` like the other times, so the reference check stays green on any day (B947).
- A queue ticket with `finished_at` but no done marker is no longer picked first by every new worker: the worker writes the finish time only after the result and the done marker, re-queues clear it, and the drain closes such a leftover ticket (done marker rebuilt from its result, else `status: "recovered"`, one signed `review.recovered` row) and serves the next ticket. B918
- `TestB788WorkerOpFailureStartsWithoutJev/signed_out` no longer fails on a loaded machine: only the hanging-`op` case uses the short 300 ms budget, and the `run start` worker tests wait up to 60 s for a worker that announces or exits at once (B794, B798)
- Three tests no longer fail at random on a loaded machine (B909). The process-sweep tests (`TestB511SweepStopsGoWorkersOfEndedRuns`, `TestRunStartSweepStops`) wait for a stopped child to be reaped before the fake clock moves on. Before, a late SIGTERM let the fake 2 s grace end after about 80 ms of real time, so the sweep also sent SIGKILL (`[terminated killed]`). The test that builds the native binary now builds it once per test process and retries one failed build. A Go build cache trim running at the same time had failed it with `could not import ... no such file`. The B529 foreign process now lives 1 h instead of 30 s, and the hanging-`op` case of B788 is bounded at 60 s instead of 10 s.
- `status --help`, `decisions [list] --help` and `docs [sync|path] --help` (also `-h`) print the help page (Description, Usage, Options) and exit 0, like every other command; before they exited 2 with `unknown flag "--help"` or the bare usage line on stderr (B877)
- An unknown flag is a usage error everywhere: `gates run`, `gates detect` and the other `gates` subcommands print `gates <sub>: unknown flag "<flag>"` and their usage and exit 2 before any gate runs (a typo such as `--chnaged` used to run the full suite and exit 0), and `version` with any argument prints its usage and exits 2. An unknown verb now exits 2 with a usage line and lists only the visible verbs (no `land` or `native-status`). A test runs every command of the help table with `--help` and with an unknown flag (B878)
- Every command now prints its usage when it refuses an unknown flag, not only `gates`: `run`, `autopilot`, `block`, `proof`, `plan`, `author`, `facts`, `s2`, `spawn` and the rest print `<command>: unknown flag "<flag>"`, then the usage that `<command> --help` shows, and exit 2. `models` and `validate` say unknown flag instead of `unknown argument`, `resolve --x` says unknown flag instead of `unknown level`, and `block settle` quotes the flag (B895)
- `run end` of a test (a binary built with the test signal guard) stops the worker its `run start` launched: the detached worker carries `CODE_FORGE_TEST_SIGNAL_OWNED` naming the test root, so the guard lets that worker be signalled after init adopts it; before, `run end` printed `run end: kill EPERM` (exit 1) and the worker, and a `review` run's worker, outlived the test. Product builds are unchanged. (B882)

## [0.25.1] — 2026-11-19

### Added

- `code-forge config fix [--dry-run] [--file <path>]` rewrites the `.code-forge.yml` shapes an older release accepted and this one refuses, one named rule each with a one-line reason (today `gates-extra-invariants`: `gates.extra.invariants`, refused since 0.22.0, is removed and an emptied `gates.extra` with it, with a hint to move the script to `gates.custom`). Only the old key's lines change (comments, blank lines and key order stay); `--dry-run` prints a diff and writes nothing; unknown keys are listed, never deleted; nothing is added, so no secret is written; a file with YAML anchors, aliases or tags, or one that would not read back as the same config minus the old key, is left unchanged with the step to do by hand. `code-forge upgrade` runs the fixer on the cwd's `.code-forge.yml` after a successful upgrade and prints what it changed or `config ok`; it never fails the upgrade (B791)
- The site's `/proof` page shows real ledger data from each release: `cf-release site-data` (and so `npm run release:site`) adds `overnight`, `single`, `stopped`, `finding` and `gate` samples to `proof` in `site-data.json`, each picked from code-forge's own ledger by a fixed rule in docs/releasing.md and `null` when the ledger has none; only short ids, counts, times and spend are exported (B784)

### Changed

- `npm run release:site` is fast: it builds the site once (`npm run build:site`), checks that the deploy folder holds `index.html` and that a built `.html`, `.md` or `.txt` file names `<package>@<version>`, then commits and deploys; it no longer reads or runs the site's `gates.test` and `gates.custom` (the site's `npm test` and design-fidelity checks run during development as site gates) and prints one line saying so; a failed build reads `Site build failed: npm run build:site (exit N)` (B780)

### Fixed

- `block close` and `block ready` no longer hand over (`close-high-paths`, a dead end since 0.25.0 removed Node) on a normal `.code-forge.yml`: the high-tier path read (`proof.tiers.high.paths`, work tree and base) needs only the schema check, so `keys` with an `op://` or `env:` reference, `review.routes` as a list of mappings, `review.second_provider`, `review.second_levels` and comment lines close as usual. A config that does not load, fails the schema or holds what looks like a raw secret is a refusal (exit 1, nothing recorded) naming the key path, such as `raw secret at keys.jev; remove it and store the key with code-forge keys set <name>`; an `op://` or `env:` reference is not a secret. `autopilot approve --key proof.tiers --value '{}'` (and the restore at until) on a commented config with nested `review:` maps now writes the change with a line edit that keeps every other line, instead of handing over (`autopilot-approve-yaml-shape`); a shape it cannot change safely (an anchor, a tab, CRLF, a key path through a list) is refused before the prompt with what to change by hand; so is a change that would leave a config the run cannot use (a schema issue or a `code-forge validate` error, listed one per line), instead of handing over (`autopilot-approve-reload-shape`); a `!!` in a comment and a non-empty `coder.sandbox.add_dirs` no longer hand approve over; the secret scan matches a key shape only where it does not start inside a word, so `project.name: task-runner-ios` is not a secret (B781)
- CHANGELOG.md: the 0.25.0 section had 61 entries before its first `###` heading and four `### Changed` headings, and 0.24.0 had two `### Fixed` headings, so the site's changelog page showed a nameless group and repeated groups; each entry now sits under one `### Added`, `Changed`, `Fixed` or `Removed` heading (entry text unchanged). `npm run release` now refuses a new section with an entry before its first `###` heading or a `###` kind that appears twice, naming the line (B785)
- `code-forge run status --run <id>` panicked (a `go-panic` refusal, exit 1) for every run whose `.code-forge.yml` has no `budget:` block; it now prints the status and exits 0, with `"budget_usd": null` when no `budget.usd` is set (B786)
- `run start` with a 1Password Jev key (`keys.jev: op://…`) and no unexpired cached key no longer says "the worker did not start": the worker reads the key with `op read` and caches it like `doctor`; when `op` fails it serves without Jev and prints one warning naming the fix (`op signin`, then `code-forge keys test jev`) (B788)
- `validate`, `doctor`, `plan check`, `run reload` and `autopilot approve` print the config error instead of "is not supported in this case yet": an unknown key (`[schema] /review must NOT have additional property "judge" ...`), a YAML error with its file and line (`.code-forge.yml:22: DUPLICATE_KEY: Map keys must be unique (line 22, column 1)`), a domain error (doctor's `FAIL config: 2 error(s); first: ...`, probes skipped), and exit non-zero; `coder.sandbox.add_dirs` validates (rules `sandbox-dir-refused`, `sandbox-dirs-unused`) and reloads in Go; `!!` in a comment or quoted string is text, and only a real YAML tag is refused, naming its line; `autopilot approve` refuses a `.code-forge.yml` that is already invalid before the prompt with `fix .code-forge.yml first: it <why>; nothing was changed; run code-forge validate` (B787)
- `block land` works with `native.daemon: false`: it runs the review join in its own process (the same join as `review-file --wait-block`), and a retry of a block that `block close` already closed needs no join at all. Before, every land with the daemon off stopped with `code-forge: block land is not supported in this case yet (land-config-daemon)`, even right after a passing `block close` in a worktree run (B789)

## [0.25.0] — 2026-11-18

### Added

- `npm run release:site` moves the site docs pin: it sets `"commit"` in the site's `docs-site/site/docs-source.json` to the release tag (other keys, key order, indent and trailing newline kept), so the docs build shows the released docs, and commits it with `src/data/site-data.json` in the one `Release site vX.Y.Z` commit; `--dry-run` names both files. A site without that file releases as before and prints one `Docs pin ... not found` line; a pin that is not a JSON object stops the release before any gate, commit or deploy. Before, the pin moved only by a hand edit, so the site docs kept the old text (B775)
- `block land` can require green GitHub CI before it merges: with `land.require_ci: github` in `.code-forge.yml` (default `off`), `--merge` and a batch land ask `gh run list --commit <sha>` for each block's own tip and refuse at step `ci` (exit 1, nothing merged or written) when there is no run, a run is still going, the run is red, or `gh` is missing; `land.ci_workflow` limits the check to one workflow, and a red run lands only when every failing test matches a `land.ci_red_list` line of a block named in `CODE_FORGE_CI_RED_BLOCKS`, never the landing block itself (B765)

### Changed

- Docs: `getting-started.md` and `step-by-step.md` no longer say commands go to Node or name a Node fallback; the command runs the Go binary only (B774).
- CI is split for wall clock with the same tests: `cmd/code-forge-native` runs in 6 parallel shards per Go version (`scripts/ci-shards.sh`: the sorted `go test -list` names dealt round-robin, so a new test joins a shard by itself), the other packages in one job per Go version, gofmt, vet and a shard coverage guard (each shard's `-run` regex selects exactly its tests, no test in two shards, union == full list) in one `go lint (stable)` job, and the four release builds in one `build-native (stable)` job; `-race` runs on Go stable only, the 1.22.x floor runs the same tests without it; the test jobs use the setup-go cache; `TestB563InitInstallRegistersTheInstaller` clears the process's lazy run root as `init` does at its start, since run in a shard after tests that left one set it handed its npm child a stale root (B777)
- Go review sessions take an optional hard-timeout seam (`session.Deps.HardTimer`, nil = the wall-clock timer as before, so behaviour is unchanged); the B627 needs_file tests use it to time a session out when the fake reviewer signals it hangs, not when 3 s of wall clock run out, so a session slow to start under full-suite load is no longer killed before it logs its call (`TestB627OutsidePathRefused` failed that way at the land of B768) (B771)
- Go `keys` and `block attempt` run more unreadable-file cases with Node's text instead of handing over: a `store/index.json` that is a folder or has no read permission (`keys list` and `remove` fail with the read error, the typed `keys set` stores the key file then fails the same way, `keys test` reports the stored key or the failed removal of an expired one), and a run record that is a folder or unreadable (`block attempt: EISDIR: illegal operation on a directory, read`, exit 1); new Go tests pin `report --export` onto dangling and looping symlinks against Node; two unused hand-over reasons are gone (B759)
- No step needs Node any more: `CODE_FORGE_NATIVE=0` no longer turns off the doctor's `native binary` row (its missing text is `not found`, no longer `not found, using Node`), the review daemon (only `native.daemon: false` does) or the hand-over log (only `CODE_FORGE_HANDOVER_LOG=0` does); code-forge's own gates run Go (`go -C native run ./cmd/cf-gotest {test_files}` for the changed packages, `go vet` for types); `sh scripts/cf-release.sh build-native [--host]` and `config-doc [--check]` replace the deleted `scripts/build-native.mjs`, `gen-config-doc.mjs` and `gen-site-data.mjs`, and every rebuild hint and the CI doc check name them; the release's CI line no longer names `npm test` or a typecheck; the Node golden recorders skip with a message and the goldens are kept, never re-recorded; `scripts/install.sh` sets no Node variable (B760)
- No user-visible change: the native daemon's sweep now counts the run keys it serves from its cache and the ones it reads from disk, and the B755 speed test checks those counts (the first sweep reads every ended run's key, a second sweep over unchanged keys reads none) and that the second sweep takes under half the first, instead of a fixed 3-second budget. Under the race detector the second sweep's one stat per key file took 4-9 s (Linux CI, and macOS under load) although every key came from the cache, so the budget failed both Go CI jobs (B763)
- No user-visible change: the native daemon's sweep now counts the run keys it serves from its cache and the ones it reads from disk, and the B755 speed test checks those counts (the first sweep reads every ended run's key, a second sweep over unchanged keys reads none) instead of a fixed 3-second budget; the sweep times are only logged. Under the race detector the second sweep's one stat per key file took 4-9 s (Linux CI, and macOS under load) although every key came from the cache, so the budget failed both Go CI jobs (B763)
- No user-visible change: Go `run start`'s process sweep now takes its orphans by the same `worker.OrphanOwner` rule as the Go `sweep` command and the Go worker's orphan sweep, instead of its own copy of that rule, so the three sweeps cannot drift apart; a test checks all three take the same orphans on one table of cases (B768)
- In a Go project, the block gate (`gates run --changed`, block land and close) runs `go test` only on the changed packages: the directory of a changed `.go` file or of a changed `testdata/` file. `go vet ./...` stays whole-module, no changed package skips the test gate with `no_changes`, and `gate.done` lists the `packages`. A batch land runs one full `go test ./...` at the end, before any push. `gates.go_changed_packages: false` turns it off. B667
- The Go changed-package gate (B667) also runs `go test` on every package that imports a changed package: in its build (`Deps`, direct or through other packages), in its `_test.go` files (`TestImports`) or in its external test package (`XTestImports`), read in Go from `go list -json ./...`. A `go list` error or an empty listing fails the gate with `go changed packages: cannot list the importer packages of ...` and runs no test. `gate.done` lists `packages` (changed) and `importer_packages` separately (B723b)
- The native daemon's repeat sweeps of old runs are much faster on a large ledger: a sweep reuses each run key an earlier sweep read while the key file is unchanged (same size, modify and change times, mode and file), instead of opening and reading every key again, and refreshes the review queue only for ended runs that still have tickets, instead of once per ended run. On 100,000 `run.end` rows of runs with keys and a queue that holds tickets, the second sweep took 0.86 s instead of 11.1 s. A key file that changes, even in place with its modify time set back, or that is removed or opened to others, is read again on the next sweep. (B755)
- Tests only, no product change: a `code-forge-native` binary built by a Go test (which may signal only the test's own processes, B529) now has `run start`'s process sweep look only at those processes, so it no longer reads the environment of every other process on the machine (315 processes, 4-7 s under load before B748 batched the read; the one `ps -E` call now names only the test's own processes); the review-flow child-registry test no longer depends on the machine's process table. A product binary still checks every process of the user. (B754)
- Go no longer refuses cases where Node exits with its own text: `block close` and `block attempt` with secret-like text in `--run` or `--reason`, and a close whose coder report holds secret-like text, run in Go with Node's output (the `coder.tests` row masks the run's signer key as `[REDACTED]`, as Node does); `block close --dry-run --decide` prints Node's `unknown flag "--dry-run"` (exit 2); `report` with a slug that is not UTF-8, a ledger it cannot read or an export folder it cannot write prints Node's `report: <message>` (exit 1) instead of the not-supported refusal (B744)
- No command starts `node` any more to ask its version: the error log's `node` field, the gate reuse stamp's Node slot and the `logs report` `- Node:` line say `none` (B585 ran `node --version` once per process; a full run stamped before is not reused, the next green one is), `context pack` names this install's `bin/code-forge` launcher and the Go binary's version instead of the Node entry, the binaries no longer stamp a Node version, and `npm run release:site` makes the site data in Go (`cf-release site-data`, the same JSON as `node scripts/gen-site-data.mjs`). The status helper's Unicode block-id order case still runs Node. (B690d)
- A plain `run start` no longer spends seconds sweeping old runs: the process sweep reads every candidate's environment with one `ps -E` call on macOS instead of one per process (342 calls on a busy machine), and a dead temp root with nothing registered is removed without reading the process table (one `ps` per root before); `sweep` uses the same read. What is stopped and printed is unchanged (B748)
- The native daemon starts answering much sooner on a large ledger: it opens its socket before its first full ledger read and sweep, and a request sent meanwhile waits for that read, so no answer comes from a partly read ledger (a failed first read still stops the daemon with its error). The sweep compiles its run-id pattern once instead of once per `run.end` row and reads the runs folder once instead of each key per row; on 100,000 `run.end` rows of runs without a key the sweep took 43 ms instead of 4.3 s, and the socket is up in about 20 ms instead of after the read and sweep. (B749)
- Go never starts Node any more: every case that used to hand over to the Node router is refused in Go with one line, `code-forge: <command> is not supported in this case yet (<reason>). Please report it: code-forge logs report`, exit 1, nothing on stdout and no state, ledger or queue change; the hand-over log row is still written, so `logs handovers` counts these cases. A verb Go does not know prints Node's `code-forge: unknown verb "<verb>"` text with the verb list and exits 1, and no command needs Node on `PATH` to run (B690a)
- Go never starts Node any more: every case that used to hand over to the Node router is refused in Go with one line, `code-forge: <command> is not supported in this case yet (<reason>). Please report it: code-forge logs report`, exit 1, nothing on stdout and no state, ledger or queue change; the hand-over log row is still written, so `logs handovers` counts these cases. A verb Go does not know prints Node's `code-forge: unknown verb "<verb>"` text with the verb list and exits 1, and no command needs Node on `PATH` to run; where Node prints a usage error (exit 2), such as an unknown `block` subcommand, an unknown `review-file`, `block close` or human `block` flag, or `logs` and `init` arguments that are not UTF-8, Go prints the same text and exit 2 instead of refusing, and the refusal and unknown-verb lines mask registered secrets as Node does (B690a)
- The top-level `code-forge land` runs in Go and no longer hands over to Node: it refuses with Node's exact message (`verb "land" has no default export function`), exit 1 and the same error-log entry, and `land --help` shows Node's usage. The worker and session no longer hand a `budget.usd` ticket to Node when no warning hook is set; the 80% warning row is written either way. (B689)
- `autopilot approve --value` with a lone surrogate escape (for example `'"x\ud800y"'`, alone, next to text, in a list or replacing a value) runs in Go: the prompt, the `.code-forge.yml` write (`name: "x\ud800y"`, as the `yaml` package writes it), the reload, the signed row, the run record and the restore at until match Node byte for byte; it no longer hands over (`autopilot-approve-value` stays for a surrogate pair escape). Go also reads a `\udXXX` escape in a double-quoted `.code-forge.yml` scalar, and the run record read, as Node does. (B732)
- Go no longer hands to Node the TypeError Node's `reviewIdentity` throws for two queued attempts of a file's current content (an `enqueued_at` that is a number, `null`, missing or an object): `review-file` enqueue and `--fresh`, the `--wait-block` join and its requeue, `block close` (its requeue and `--auto-claim`) and `block reopen` print Node's message and exit 1; and the Go worker serves a ticket whose `started_at`, `finished_at` or `duration_ms` holds a lone surrogate escape, as Node does. (B730, B731)
- Go serves lone surrogate escapes (`\udXXX`) that handed over to Node: an `s2 --packet` (the prompt and `packet.md` byte-equal to Node's), a `jev ask --state` (the Jev request keeps the escape), a lockfile problem on another entry at `block close`, and Codex item ids at `block accept-hit`; `autopilot approve --value` with one still hands over, since Go cannot write or read it back in YAML (B728)
- `review-file --wait`, `--rebase-check`, enqueue cache checks, `run start` and the until-done guard read a done marker, a review result and a worker registry entry with a lone surrogate escape the way `JSON.parse` does (a result path opens in Node's U+FFFD form; the result is written back as the escape) instead of handing over to Node; a run record with one, and a worker whose `run` holds one, still hand over, since those values reach paths, git argv and stderr text. B727
- Go reads a lone surrogate escape in ledger rows, `errors.jsonl`, gate acceptance files, the key index and the worker's state, ticket and registry files the way `JSON.parse` does and writes it back as Node does (`ledger tail`, `ledger calibration`, `logs`, `logs summary`, `gates acceptance`, key index rewrites, worker reads), and a Markdown recheck stop naming a heading cut inside a surrogate pair runs in Go byte-equal to Node; paths, argv and report text that meet U+FFFD on Node still hand over. B704
- The `init` wizard runs in Go with text of any width: levels, the Solo project, gates, proof lists and the summary with CJK, emoji and their sequences, combining marks, full-width forms and zero-width joiners are measured, normalized and wrapped as clack does in Node, and `.code-forge.yml` and the user config are written with that text byte for byte as Node writes them. `init-wizard-shape` now covers only a level, Solo project id or tools value of a type Node prints as JavaScript would (a number effort, a list as a level, a map as the Solo project id). B726
- The `init` wizard runs in Go in more cases, each proven byte for byte against Node on a real pseudo-terminal (a new Go test driver): terminals Node draws with or without colours (proven for `NO_COLOR`, `TERM=tmux` and `TERM=vt100`; `CI`, `TEAMCITY_VERSION`, `COLORTERM` and the rest follow the same port of Node's colour depth the `keys set` prompt uses; tmux now draws without colours, as Node does) and clack's ASCII symbols on `TERM=linux`; `--yes-tool` installs; a models cache that is not JSON; and the end-of-input warning for a launcher under any path, such as `@codedology` in an npm install. Hand-over reasons `init-wizard-tools` and `init-wizard-models` are gone; an unwritable models cache hands over as `init-models` and an unreadable hint state as `router-hint-state`. B703
- `review-file` with an unknown flag holding a value (a path, a 1Password reference, an email) and `block accept-hit` on a Codex transcript with lone `\uD800`–`\uDFFF` escapes or deep nesting, or with an error text holding a no-break space or vertical tab, run in Go with Node's output, exit and error-log entry; they no longer hand over to Node (`enqueue-flag-redaction`). Go's error log now masks registered secrets before scrubbing, scrubs like Node's JavaScript regexes and trims messages with JavaScript's `trim` (U+FEFF trimmed, U+0085 kept). B706
- `tools install` and `logs clear` with stdin at a terminal and stdout piped (for example `| tee`) ask their question in Go: clack's frames on the pipe at 80 columns, colored only by `FORCE_COLOR`, with the same answers, output, exit codes and error-log row as Node; they no longer hand over to Node (`tools-confirm`, `logs-clear-prompt`). B698
- `tools install` at a terminal asks its one question in Go (`Run <n> install command(s)?`, default no): yes runs the installs, any other answer prints "cancelled — nothing installed" and exits 1, as in Node; it no longer hands over to Node. B682
- `block accept-hit` without `--rule` on a Codex `exec --json` transcript runs in Go: the named item line is paired with its `item.completed` lines and each hit gets its own signed row, as in Node; refusals of the human block verbs whose text has accents, emoji or quotes are printed and logged in Go with Node's message and fingerprint instead of handing over to Node. B685
- The Go error log cleans and fingerprints text like Node: `op://` refs end at any JavaScript space (NBSP, U+2028, U+3000, U+FEFF, `\v`), 1Password item IDs next to `_` or `-` are cleaned, messages are trimmed with JavaScript's trim, home and project paths follow Node's path forms, and every route (block, gates, review-file, spawn and doctor warnings) uses Node's message normalization; `block ready`, `waive` and `reopen` refusals with non-ASCII text or an apostrophe no longer hand over to Node. B699
- `native-status` serves in Go the lines it handed to Node: bytes that are not UTF-8, lone surrogate escapes, numbers read as `Infinity` and signed rows holding them, lines split at U+2028 and U+2029, no `HOME` and the hint at a terminal, byte-equal to Node. Only a line whose listed block ids need ICU ordering still goes to Node. B687
- `block close --decide` no longer has a hand-over to Node for a decision row that does not sign: its rows hold only text and finite line numbers, which always sign and verify, so that case cannot happen; a test signs thousands of rows (invalid UTF-8, controls, lone surrogates, any key) to prove it. The Go ledger reader now keeps a lone surrogate escape (`\ud800`) as Node's `JSON.parse` does, so a signed row with a lone surrogate in its text verifies at the close gate as in Node instead of reading as U+FFFD and failing the check. B716
- `block close` no longer hands the review queue identity or the worker pin check to Node: it sorts a file's queued attempts with V8's sort and `localeCompare` (an `enqueued_at` whose `localeCompare` throws prints `WARN no ticket` as Node does), dates `reopened_at` with V8's `Date.parse` rules, and runs `closeBlock`'s claim check itself, so a claim lock file that cannot be read or parsed, a `null` claim or odd `workers`/`worker_history` values fail with Node's message and a dead pinned worker holding a ticket stops the block, also under `--decide`, `--no-require-reviews`, `--auto-claim` and `block land`; the `close-queue-pins` hand-over is gone and `close-queue-identity` is left only for an order that needs `localeCompare` of text outside the collation measured against Node. B720
- `block close` reads the review queue like Node in Go: a queued ticket Node refuses prints Node's message (`block close: ticket <id> is not readable JSON`, `... does not match its id`, `... has an unknown kind` and the other ticket and path refusals, exit 1, nothing written), a queue that cannot be listed prints Node's `ENOTDIR`/`EACCES ... scandir '<queue>'` error, and a ticket with invalid UTF-8 or a lone surrogate escape is read as Node reads it, so the reviewed line names Node's ticket; the `close-queue-refresh` hand-over is gone. B718
- `review-file --wait-block` and `block close` restart a changed stopped review in Go: when a stopped review has findings and the file's content changed, the join writes the signed automatic `review.reopened` row (`by: auto`), clears the old result, queues the new ticket and prints the `auto-requeue <file>: ...` line as Node does; `block close --auto-claim` no longer hands over for a file whose latest attempt stopped, and close prints `block <id> open: automatic requeue did not finish` in Go when the join does not finish; the `wait-block-stopped` hand-over is gone. B717
- `block close` and `block human-pass` run more cases in Go: `block human-pass` (and its `--dry-run`) reads date-only and legacy autopilot grant `until` forms as Node's `grantState` does; `block close` reads `human_checks` that is an object with its own `length` key as Node's `.length > 0` does (an awaiting one ends with Node's `entry.human_checks is not iterable`, exit 1); `--decide` refuses a late or cap row whose file or finding is not text with Node's waive precheck usage error, and a transcript path with a control character with `--transcript must not contain control characters` (exit 2). B714
- `block reopen` at a terminal runs in Go: the owner's question, the answers, the signed human `review.reopened` row, the cleared done marker and the reopened review ticket match Node; it no longer hands over to Node. B680
- `autopilot waive` runs in Go: Node's checks and refusal texts, the signed `review.waived` row and the tracked GitHub issue (deduped comment, or a create with the label and a retry without it, and the signed `autopilot.issue_failed` row when there is no issue), byte-equal to Node; `autopilot start` without `--yes` with stdin at a terminal and stdout piped now asks in Go with Node's prompt; a waive path Go does not normalise like Node still hands over (B681)
- `logs report` now asks its terminal questions and runs `--with-doctor` in the Go engine instead of handing them to Node: "Report anyway?", the send menu with "Edit in my editor" (`$VISUAL`/`$EDITOR`, secret check of the edited text, fingerprint added back), "Send without the AI check?" and "What now?" are drawn like clack's select and confirm, and the setup check runs `code-forge doctor --json` with Node's 60 s limit, row filter and `### Setup check` table; only a question on a stdout that is not a terminal or a terminal smaller than 40x12 still goes to Node (`logs-report-prompt`), and the `logs-report-doctor` hand-over reason is gone. B677
- `keys set <name>` (the hidden prompt on a terminal), `keys set --op` and `keys test --ref` with an `op://` reference, a 1Password item ID or an item link now run in the Go engine with Node's output, exit codes and stored files; the typed value is read with echo off and the terminal mode comes back on every path, including a signal. B678
- The typed `keys set <name>` runs in the Go engine with a piped stdin, a stdout that is not a terminal, any terminal size, `NO_COLOR`, CI and other `TERM` values, drawn byte-equal to Node, and leaves no `pendin` flag on the terminal; a 1Password link with no `i=` item ID, and `keys list`, `keys test` and `keys remove` on Linux without the keyring binding (file store with its warning) run in Go too. B694
- On Linux, `doctor`, `keys test`, `keys remove`, `init` (auto step and wizard), `jev ask` and `worker` read and write the Jev key in the Secret Service store that `@napi-rs/keyring` uses (same items, labels and index rows as Node) in Go, and without the package's Linux binding use the file store alone like Node; Linux with the binding but no Secret Service (Node's keyutils store) still hands over. B679
- A Markdown recheck over the budget runs in Go section by section, one session per heading section, with the same rows, state and packets as Node; a Markdown heading cut inside an emoji's surrogate pair no longer sends a review to Node unless the review names that heading. B683
- `block close --no-require-reviews` and `block close --decide` at a terminal run in Go: the owner's question and answers, the autopilot grant refusal, the signed `gate.reviews_waived` (by human) and `agent_waivers.acked` rows, the scope warnings and the close output match Node; they no longer hand over to Node. B684
- `init` reads and rewrites a `.code-forge.yml` or user config with YAML anchors, aliases, `<<` keys and the standard tags in Go, byte-equal to Node: a shared object is written once with `&a1` and then as `*a1`, a merged one is written out; custom or unfitting tags, anchors on keys, aliases to empty collections and `%` directives still hand over to Node. B686
- `block close` runs more owner cases in Go: `--auto-claim --no-require-reviews` at a terminal; `--decide` waivers for review caps, late findings and invalid autopilot waivers, claims from a signed ticket result, accepted transcript hits and Node's waive prechecks; and `human_checks` of any shape (a string, a number, an object, mixed values) as Node judges it. B697
- `block close` dates an autopilot grant `until` as Node does in Go: a date-only `until` (UTC midnight) and the plain legacy forms V8's `Date.parse` reads (`Jan 2 2020`, `January 2, 2099`, `2020/01/02 10:20:30`, `1/2/2020 10:20`, `2099-06-01 10:00`, local time with no zone) no longer hand over (`close-autopilot-grant`); the shared Go date reader now reads these forms everywhere, matching Node in UTC, New York and Kathmandu. B709
- A full review of a Markdown file whose heading is cut inside a surrogate pair (an emoji at the 120th UTF-16 unit) runs in Go and no longer hands the ticket to Node: the heading keeps the pair's high half like Node, and results, ledger rows and signed rows write lone surrogates as `\udXXX`, byte-equal to Node's JSON.stringify and signer. B696
- Go reads no data file from the Node `src/` tree: the review lenses, the finding schema and the known-fixes table are built into the binary, and `upgrade` (and `init`, which shares its package-root check) find the package root by its `package.json` name instead of `src/cli/upgrade.mjs`. A known-fixes table under an installed package's `src/util` no longer sends `logs` to Node. B688

### Fixed

- Go `run start` no longer stops an orphan of another `HOME`: its process sweep stopped any process whose `CODE_FORGE_OWNER_PID` named a dead owner, so a `run start` under one `HOME` (a test's temp `HOME`, a second setup) could stop a live process of another. An orphan is now stopped only when its `HOME` is this `run start`'s `HOME`, as workers already were; one with no `HOME` is left alone (B746)
- The Go `sweep` command and the Go worker's orphan sweep now stop only an orphan whose `HOME` is their own `HOME`, the rule workers already had: they took any init-parented process whose owner had died, so a sweep under one `HOME` could stop a live process of another `HOME` (a test temp HOME, a second setup) or of none. One shared rule, `worker.OrphanOwner` (B767)
- The Go worker's orphan sweep and the Go `run start` / `sweep` now read a process's owner markers with one reader, Node's: a marker value stops at a line end. The worker's reader let a value run over a line end to the next variable, so `ps -E` output with a second row after the process made a marker read the next row's head (a start time read that way made a child of a live owner look orphaned) (B757)
- The Go process sweeps (`run start`, `sweep`, the worker's orphan sweep after a drain) now read the last variable of a process's environment on macOS: `ps -E` ends each row with a line end, and the marker reader kept it, so the last variable was never read (`run start` and `sweep`) or carried the line end (the worker), and a run-id filtered orphan sweep never matched a process whose `CODE_FORGE_RUN_ID` came last, as the Go spawners add it (B747)
- The Go worker stops waiting for its new native daemon after 2 seconds, as well as after Node's 100 tries 20 ms apart: under load the tries stretched (84 tries took 14.4 s on a 100k-row ledger) and the worker missed `run start`'s 15-second launch window, so `run start` failed with `the worker did not start`. A daemon that is not ready yet keeps starting and is used once it answers. (B737)
- The Go worker that `run start` (and `review`, `doctor`) launches no longer inherits the caller's `CODE_FORGE_OWNER_PID` and `CODE_FORGE_OWNER_START_TIME`. The worker outlives its caller, so with that dead pid as owner an owner-based orphan sweep, such as Node's in Node `run start` (which knows only the Node worker's command), read the live worker of an open run as an orphan and stopped it with SIGTERM, 2 s, SIGKILL, which left its lazy root `proc-<pid>-<ms>` behind. (B736)
- `block rebase` (and every other reader of the review queue index: `review-file`, the worker, `block close`'s review wait and `--auto-claim` join) again fails with `queue-unreadable` when `.code-forge/queue` cannot be listed, as Node's `blockTickets` and `reviewIdentity` do; since B718 it printed the raw `scandir` error, which only `block close`'s worker-pin check prints (Node's `closeBlock`). B735
- The Go router clears the error kind a verb reports before each verb and takes it when the verb ends, as `bin/code-forge.mjs` does, and a failing verb takes it even while `CODE_FORGE_NO_ERROR_LOG=1` turns the log off; a kind such as `op_timeout` (or a crash stack) left by one verb can no longer label the next verb's `errors.jsonl` entry when several run in one process; a warning line (such as `spawn`'s budget warning) is likewise no longer named after a `worker` run earlier in the same process. (B738)
- A child's pid entry (`<run-root>/pids/<pid>.json`) is written to a temp file and renamed into place, so a sweep or any reader that finds it reads the whole entry, never an empty or cut one; three load-flaky Go tests (facts codex timeout, review-flow child registry, ledger lock identity ps) now wait on events instead of racing the clock. (B691, B692, B693)
- A Go worker that gets SIGTERM or SIGINT during its orphan sweep after a drain ends the sweep and stops at once; before, it first read every init-parented process's environment, which under a background QoS throttle took past 30 s, so a test reap killed it and its lazy root stayed. (B734)
- A config file that is `null` (or a nil map or list inside a signed value) is written as `null` in Go's canonical form again, as Node writes it; since B696 Go wrote `{}` or `[]`. (B722)
- Gates: a gate child started by Go also gets the default action for SIGTSTP, SIGTTIN and SIGTTOU when the engine inherited them as ignored (a job-control shell's background job), and starts with an empty blocked-signal mask, as Node's spawn (libuv) does; before, the gate kept those three ignored and kept PIPE, USR1, USR2, TSTP, TTIN and TTOU blocked when the engine's parent had blocked them. B721
- Gates: a gate child started by Go gets the default action for every signal the engine inherited as ignored, as Node's spawn does; under `nohup` (SIGHUP ignored) or as a script's background job (SIGINT ignored) a gate killed by SIGHUP or SIGINT used to exit 0 and pass, and `TestExecuteSignalNames/HUP` failed in a full suite run with SIGHUP ignored. B719
- Go output: API keys the key store reads or stores (`keys`, `jev ask`, the Jev key of `doctor` and the worker) and gate env values of 8+ characters (registered when `gates run` starts the gates) join the Go safe-output set, so later stdout and stderr lines mask them once as `[REDACTED]` like Node's `writeSafe` (for example `jev ask` naming its key, or the `repeat_new_tests: skipped` line); each command starts with an empty set; tests vs Node goldens, docs. B715
- Go output: every Go command writes its stdout and stderr through Node's `writeSafe` rule, so a run's signer key it has read prints as `[REDACTED]` (for example `block open` failing on a claim path that holds the key); `block ready` with text holding the signer key runs in Go and prints it masked, byte-equal to Node, instead of handing over; tests, docs. B712, B713
- Go error log: every Go command that reads a run's signer key (block open, close, land, context, gates custom rows, ledger, doctor, run start, rebase, status, the worker) registers it as Node's `loadKey` does, so a failure after the read logs the key as `[REDACTED]`, not only in `autopilot`; `gates` keeps its last error text with JavaScript's trim (U+FEFF trimmed, U+0085 kept) like Node's stderr capture; tests, docs. B710, B711
- Tests: the native-install, entry and config-golden Node tests give their private package copies a node_modules without the `@codedology` platform packages, so CI (which installs the published `@codedology/code-forge-<target>` binary) no longer finds the release binary in place of the test's own. B701
- CI: the Go race test job gets a 30-minute package time limit. The big native package passed 10 minutes under the race detector and timed out (B705).

### Removed

- The `evals/` folder is retired: its harness (`evals/run.mjs`) and scenarios 15-19 imported the Node `src/` files B760 removed, so a live eval run failed with `ERR_MODULE_NOT_FOUND`; the folder and the plugin manifest's `experimental.evals` entry are gone (B773)
- code-forge no longer contains or runs Node: the Node CLI (`src/`, `bin/code-forge.mjs`), its tests, `tsconfig.json`, the shipped Node scripts and the `CODE_FORGE_NATIVE=0` Node-only mode are removed, and the package has no Node engine requirement or dependencies (npm, with any Node that runs it, is needed only to install). The skill's `forge` shim never runs Node (a `CODE_FORGE_BIN` naming a `.mjs` file with no `code-forge` entry next to it stops with exit 127). `status` orders listed block ids outside printable ASCII in Go (printable ASCII ids first, then the others by UTF-16 code units). The key store names its macOS backend `keychain`; on Linux it uses the Secret Service when one answers, else the file store (keys stored only in the kernel keyring must be set again). Input ending at an `init` or `keys set` prompt prints `code-forge: the input ended before the prompt was answered; nothing more was asked` (exit 13 as before). The known-fixes table is now only `native/cmd/code-forge-native/data/known-fixes.json`, checked by a Go test; `npm test`, `typecheck`, `known-fix`, `setup` and `bench:*` scripts are gone and CI runs the Go suite plus a package check. (B690b)
- Six hand-over reason codes no Go path could return are gone from the Go table (`close-human`, `close-external-review`, `tools-confirm`, `help-verbs`, `doctor-full-live-worker`, `init-doctor`); the grant, pending-approval and end-hook codes of `block claim|stop|open|close|land` stay, as Go still returns them. (B729)

## [0.24.0] — 2026-11-14

### Added

- `autopilot digest --run <id> [--json]` lists every autopilot act the owner has not looked at yet (deferred human checks, substitute reviews, autopilot and agent waivers, temporary config changes, grant extends and until-done ends), each with its id and exact ratify and revert commands; `autopilot ratify <row-id> --run <id>` and `autopilot revert <row-id> --run <id>` are owner-only (a terminal yes, no `--yes`, refused under an active grant) and write one signed `autopilot.ratify` or `autopilot.revert` row; revert puts a deferred block back to awaiting_human_check, reopens a waived finding, stops a substitute approval from counting (`review-file --fresh` re-runs it) and restores an approval's old value (B635)
- `autopilot start --until-done [--deadline <time>] [--blocks <id,…>]` starts a grant that ends when every planned block of the run is closed (the `--blocks` ids, else the blocks opened in the run), or at the deadline if that comes first; the worker's grant expiry honours it, and `autopilot status` and the log show the end condition. `autopilot extend --run <id> --until <time>` (or `--deadline <time>` for an until-done grant) moves a live grant's end later with one signed `autopilot.extend` row; owner-only, with the same prompt as `start` (B634).

### Changed

- The transcript scan (block close, block ready, `gates transcript-grep`, accept-hit) reads a log that is only the final result object of `claude -p --output-format json` as a Claude result log with no tool calls, in Go and Node, so a flag the coder only quotes in its final answer (such as `--no-require-reviews`) is no longer a `rule_break`; plain-text logs, Claude Code JSONL streams (Bash tool_use still scanned), Codex `exec --json` logs and files that mix a result object with other lines are scanned as before (#37). B628
- Go tests no longer fail on a loaded machine because of a wall-clock upper bound: the close test that queues ten reviews counts one shared 1s wait on a fake clock instead of timing it (a small unexported clock hook in the close review wait; default behaviour unchanged), and the run-lock, docs sync, test-slot and land-join tests prove their behaviour by a held fake build, a stop reason or a wait count. B654
- `worker --run <id>` now runs each drain's autopilot checks in the Go engine instead of handing the worker to Node: the grant's lazy expiry (ISO times with Z or an offset, minutes only, a date, a UTC string, null), and the due approval restore with its config reload, changed-by-hand skip and Node's TypeError warning for a null approval entry; several due reloads in one pass and shapes the restore does not cover still hand over before anything is written (B616)
- `author` and `s2` on a Codex (openai) level, and a Codex coder `spawn` with `coder.sandbox` set (`workspace`, `toolchain` with its checked `add_dirs`, or `full`), now run in the Go engine instead of handing over to Node, with the same output, refusals, ledger rows and exit codes as Node; a Codex effort the builder refuses and a `coder.sandbox` that is not a mapping or names an unknown profile still hand over (B618)
- `doctor` now runs its Codex (openai) probe rows in the Go engine instead of handing over to Node: the ping of each Codex role, the open-book isolation check, the Codex path-deny check and the Codex rules row, and also a full doctor while a worker of the project is live (its run's config snapshot, worktree indexing row and native daemon row), with the same output, files and exit code as Node; a Codex coder under `coder.sandbox` still hands over (B617).
- A file whose review was reopened (a signed `review.reopened` row) now enqueues with `review-file` and closes with `block close` (also the `--auto-claim` join) in the Go engine instead of handing over to Node: the latest reopened attempt is reused, earlier attempts are retired as superseded, and `reopened_at` is read like Node's `Date.parse` (B645).
- `block ready`, `block ready --preflight` and `status` report the new state `COMPLETE_PENDING_REVIEW` when the coder's work is done and gates are green but a file has no review only because the reviewer could not run (timeout, provider down, spawn error); the fix shown is the review command, not a new coder session, and `block close` still waits for the review (B625).
- `run end --run <r> [--json]` (Go engine) refuses while the run has an open human check of a block past `open` (`closed_deferred_human`, `awaiting_human_check`, or `closed` without a verified clear row): exit 1, no record change and no row, and it lists each check with its exact `run human-checks clear` command (`--json`: `{run, ended: false, refused: "human-checks-open", open: [...]}`); the autopilot binnacle adds a `Human checks` row and no longer calls such a closed block ready; with every check cleared, both are as before (B633)
- New autopilot scope `human-pass:deferred` (Go engine): under an active grant that allows it, a block whose only open items are `human_check` clauses closes as `closed_deferred_human` with one signed `autopilot.decision` row (scope, block, clauses, `waits_for`), after every other close gate passes as for a normal close; `block open` accepts `human_check` clauses under that grant. The checks stay open in `run human-checks list` (marked deferred), the binnacle shows one line per deferred block with the owner's `run human-checks clear` command, the block may land, and clearing its last check makes it `closed` with a signed `block.human_checks_cleared` row. Without the scope nothing changes (B632).
- A `human_check` acceptance clause may carry `waits_for: "<text>"` (what the human must check; a non-empty string, only on `human_check` clauses), and the new `run human-checks list --run <r> [--json]` lists every open human check of the run (block, clause id `C<n>`, check, `waits_for`, opened at) while the owner-only `run human-checks clear --run <r> --block <id> --clause <C<n>> --note <text>` clears one with a signed `human_check.cleared` row, guarded like `block human-pass` (B631).
- The new `block settle <id> --run <r> [--report <file>] [--transcript <file>] [--json]` clears every close blocker an orchestrator may clear (rebase `--accept-landed` for a stale base, claim changed files under a folder the block owns, run the gates, wait for the reviews), never runs a human-only verb, stops on no blocker left, a failed fix or a round with no progress (at most 4 rounds), writes one signed `block.settle` row and prints the blockers left; it exits 0 when none are left, 3 when only owner blockers are left and 1 otherwise. Only then do `status`, `autopilot status` and the binnacle's owner actions show one owner line with the exact owner command (B626)
- Every blocker in `block ready --json` and `block ready --preflight --json` now has `clears_by` (`owner` when only a human-only verb such as waive, reopen, `close --decide` or human-pass clears it, else `orchestrator`), and the new `block close <id> --run <r> --dry-run [--json]` and `block human-pass <id> --run <r> --dry-run [--json]` run the verb's checks, write nothing and print the same blocker list, exiting 0 when the verb would pass and 1 when it would not (B622)
- Interactive `init` at a terminal now runs its Jev key step and its doctor step in the Go engine instead of handing over to Node: the Jev key found from `CODE_FORGE_KEY_JEV`, the file store or the macOS keychain, else the Jev source prompt (skip, env variable, pasted key stored in the file store or keychain, or a 1Password reference, item ID or link), and the doctor after the setup unless `--skip-doctor`, with the same output, files and exit codes as Node (B611).
- `block waive --yes` and `block reopen --yes` run the fresh model judge in Go (the `levels.L3` ladder with two tries per model, the judge packet with cited file windows, the disagreed and broken refusals, the signed agent row and the reopen review ticket), byte-equal to Node, instead of handing these runs to Node; a judge or state Go cannot run like Node still hands over (B612)
- `logs report` without `--no-ai` runs in Go: the AI scrub session runs through Go's session layer with Node's packet, schema, lens and timeout, and prints Node's `AI pass:` counts or `AI cleaning was not available (...)` line; `--with-doctor` and a terminal send without `--no-ai` stay in Node (B615)
- The Go worker serves reopened review tickets and autopilot extra rounds: a signed `block reopen` row restarts the file's fix loop and an `autopilot.extra_round` row raises its round cap as in Node, and a ticket's `reopened_at` is read as Node's `Date.parse` reads it; these tickets no longer hand the worker to Node. (B623)
- `code-forge help` (also no verb, `--help`, `-h`) lists every command in one short line each, grouped by verb (Artisan style), marks human-only commands `(owner)` and hides internal ones; `help <command>` and `<command> --help` show Description, Usage and Options; `help --json` prints the same table; the command reference shows each summary under its heading. All in Go, with no hand-over (B613)
- On macOS with the default key backends, the Go engine reads and writes the Jev key in the login keychain through `/usr/bin/security` (service `code-forge`, as Node's backends do) for `doctor`, non-interactive `init`, `worker` boot, `jev ask`, `keys test` and `keys remove`, instead of handing these runs to Node; off macOS or with a failing `security` they still hand over (B610)
- The Go `worker` serves an answers ticket itself (the refusals, the answers packet, the L2 answers session, the verdicts and the signed `review.answered` row) with the same ledger rows and files as Node, instead of handing it to Node; a case Go cannot print like Node still hands over (B609)
- Each Go binary now ships in its own npm package, `@codedology/code-forge-<darwin-arm64|darwin-x64|linux-x64|linux-arm64>`, an optional dependency at the same exact version, so npm installs only your platform's binary and the main package drops `native/bin` (under 5 MB). `bin/code-forge`, `scripts/install.sh`, `code-forge setup` and the Go binary look under the main package, then beside it, then in the dev build `native/bin/<target>`; a source checkout uses its own build only. `npm run release` writes the four packages to `dist/npm/<target>/`, checks every pack (main <5 MB with no `native/bin`, platform <35 MB) and prints the four platform publishes before `npm publish` (B606)
- `docs/reference/commands.md` is now generated from the Go help table (`sh scripts/gen-commands-doc.sh`, also run by `node scripts/gen-commands-doc.mjs`; `--check` exits 1 when the page is stale), so help added only in Go reaches the page; the page gains the `ci watch` and `decisions list` sections, and `block --help` and `-h` print the block usage in Go with no hand-over to Node (B648).

### Fixed

- A batch `block land` whose `--max` deadline runs out during a block's rebase, changed gates or trial merge now refuses the whole batch with "batch timed out after <n>ms during the <step> step; no block landed" and names no culprit, like a timeout in the merge gate; before, the cut-short gates were reported as that block's test failure ("block B1 land stopped at gates: changed gates failed: tests failed:") B662
- The Go ledger writer now gives `ps` 5 seconds to read its own process start time for the append lock, as Node does (was 1 second), and retries a failed read on the next append as Node does, instead of failing every later ledger append in that process after one slow `ps` on a loaded host. B624
- When a review's `needs_file` round times out, the Go worker no longer loses it: it writes one signed `review.needs_file_timeout` ledger row (ticket, lens, requested paths, attempt, retry) and the next ticket for that file starts its reviewer with the requested files already attached, in a new session with its own timeout clock; the attach refuses paths outside the project as the round does, and a second timeout falls back to today's unavailable result (B627)
- A reviewer that is still unavailable after the built-in recovery (a timeout, a provider down, a failed spawn) can now fall back to the routes listed in the new `review.on_unavailable` setting (`level`, `model`, optional `provider` and `effort`), walked in order with the waits in `review.on_unavailable_backoff_s` (default 0, 30, 120 seconds); each step writes a signed `review.unavailable_fallback` ledger row and the first route with a valid review ends the walk. A reviewer route that timed out on 2 tickets in a row in a run is benched for the rest of the run (signed `review.route_timeout`, `review.route_benched` and `review.route_reset` rows) until it answers again or the worker is reattached; this folds B321 (B629)
- Under autopilot, a file whose review stays unavailable after the `review.on_unavailable` chain can get one substitute review when the grant allows the new scope `review:substitute`: a signed `autopilot.decision` row records it, then a closed-book reviewer on a provider none of the failed sessions used reviews the file and its rows carry `source: substitute`; `block close` counts that approval only under the scope with the decision row and green gates, else refuses with `substitute_review` and names `code-forge autopilot start ... --allow review:substitute`. Without the scope one signed row asks the owner and names that command; with no active grant nothing changes (B630)
- `run reload` now takes a change to `review.on_unavailable` or `review.on_unavailable_backoff_s` as a review-only change, like `review.routes`: with a frozen key also changed on disk it keeps that key from the run snapshot instead of refusing the reload, the signed `run.reload` row lists the new keys in `changed_keys` and the kept ones in `kept_keys`, and a live worker uses the new chain from its next ticket (B650)
- `validate` now checks each `review.on_unavailable` route: a model that is not a known id of the route's provider (its own, else its level's, else the top-level one; `known_extra` and seen-in-cache ids count) is a `review-on-unavailable-invalid` error with exit 1, and an `openai` route gets the `closed-book-on-openai` warning that a `review.routes` rule gets; `run reload`, `doctor` and `init` see the same error (B651)
- `doctor` now judges a `review.on_unavailable` route model with the same seen-in-cache ids (`~/.code-forge/cache/models.json`) that `validate` reads, so a chain `validate` passes is no longer a doctor config FAIL, and one it refuses fails both with the same message; a level model known only from that cache stays a doctor warning, as before (B655)
- The Go engine no longer hands a command to Node while the run it names holds a live until-done autopilot grant (`until: null`), which Node cannot read: it refuses with one line naming the run and the grant (handover reason `until-done-grant`), runs nothing and exits 1; `block waive --yes` on a critical finding and the gate check's grant window now read an until-done grant in Go (an `autopilot.extend` row moves the window's end); timed grants work as before (B649).

## [0.23.0] — 2026-11-13

### Changed

- `npm run release` now accepts a package up to 35 MB (was 20 MB): the Go port made the four native binaries about 15 MB each, so the 0.23.0 package is 28.3 MB; the refusal reads `published package must be under 35 MB` (B605)
- `block close` runs in Go for late and cap rows whose file, finding or open id is an object or array (Node's `[object Object]` and joined text, or Node's `arg.replaceAll is not a function` error for a file it cannot quote) and for plain-text or Codex transcripts with JavaScript-only whitespace (NBSP, U+2028, BOM); before, these closes handed over to Node (close-gates-format, close-transcript-format). A routine close under a `close:routine` grant now prints Node's `opened before` warning once per gate load and joins every routine failure into the `close-not-routine` reason, byte-equal to Node (B590)
- `block close` runs in Go when a changed file matches `review.exclude` or the default lock-file list: the file leaves the review set, gets a `gate.review_excluded` row and a `not reviewed` line, and an npm lock gets Node's lock check and `lockfile` blocker; before, every such close handed over to Node (close-excluded-file). A lock shape Go cannot judge like Node hands over with the new reason close-lockfile-format (B587)
- `block close` runs in Go for a transcript that is not valid UTF-8 (decoded as Node does, each bad byte run one U+FFFD), a Claude Code log with an escaped surrogate pair, and agent waiver and reopen rows whose fields hold objects or arrays (`--json` prints them in the ledger line's key order); before, these closes handed over to Node (close-transcript-format, close-gates-format) (B583)
- Go reads the Node version when it runs (`node --version` once per process, `CODE_FORGE_NODE` else `node` on `PATH`), not the build machine's: error-log rows, the gate reuse stamp and the `logs report` Node line match the node that runs; `logs report` no longer hands over when no Node version is known and prints `unknown` (B585)
- `facts` with a Codex (openai) step in the `levels.L0` ladder (first, as a fallback, or from a top-level `provider: openai`) now runs in Go byte-equal to Node instead of handing over: the read-only `codex exec` call in the snapshot, the packet on stdin, the OpenAI-strict schema file, the `-o` answer, the fallback moves on usage limit, 401, 503, timeout and exit, the answer checks, the sheet and the ledger rows; `review.allow_open_book_codex` does not change it, as in Node (B582)
- A fix-loop recheck with a block base runs in Go byte-equal to Node's packet builder for every peer case: one peer, several peers (named and imported kept, the rest dropped), a moved-code hint alone, a moved hint plus cross-file evidence, over the budget (the cut line, or no evidence), no peers and no base; `worker --run <id>` serves such a recheck of a .js file end to end with no hand-over row (B564)
- `block ready` no longer hands over to Node when the workspace path holds secret-like text such as `eyJ`, `AKIA`, `AIza` or `sk-` (a random temp folder name did this once on CI); like Node, it masks only the run's signer key, and output that would show that key still runs in Node (B565)
- Go no longer needs a `node` on PATH for `context pack` and `cloud-coder`: with no Node, the pack's `## code-forge` line names the install's `bin/code-forge` launcher instead of refusing (with Node found, the pack bytes are unchanged), and `cloud-coder` runs `review-file` and `ledger add coder` in this Go binary instead of the Node router (B572)
- The Go binary is required: `bin/code-forge` no longer falls back to Node. With no binary for the platform and arch, a missing file or one without execute permission (and for `setup`, a binary that crashes), it prints one stderr message with `<platform>-<arch>`, the expected path and the fix (`sh <package>/scripts/install.sh`, then `npm i -g @codedology/code-forge` if that fails) and exits 69, a code no verb uses; `CODE_FORGE_NATIVE=0` still runs the Node router. New `scripts/install.sh` (POSIX sh, shipped) checks the binary for this OS and processor, restores a lost execute bit, runs `--version` and `internal version`, compares the version with `package.json` and prints OK or the problem and one `fix:` line (exit 0 / 1); no network, no sudo, no `postinstall` (B569)
- `npm run release` runs no Node on the laptop: it builds the four native release targets with `go build` from Go (same targets, flags, temp name, mode and printed lines as `scripts/build-native.mjs`; the Node version stamp comes from `node --version` when a node is usable and stays `unknown` otherwise), and in place of the local `gen-config-doc --check`, `gen-commands-doc --check`, `npm test` and `npm run typecheck` it requires the latest `CI` run for HEAD to be completed with success, refusing before any write with one line naming HEAD and the run state; `npm pack --dry-run`, `npm ls --json` and `claude plugin validate .` still run; the `--no-checks` warning names the new list and the notes usage line names `sh scripts/cf-release.sh release notes` (B580)
- The Node release scripts are gone: `scripts/release.mjs`, `release-site.mjs`, `docs-block.mjs` and `changelog.mjs` and their Node tests are deleted, after every case those tests checked was ported to the Go tool's goldens (74 new multi-step cases recorded from Node); the GitHub release workflow sets up Go and writes the notes with `sh scripts/cf-release.sh release notes` (same bytes), and `scripts/gen-site-data.mjs` reads CHANGELOG sections itself with the same output; nothing changes in the npm package (B574)
- The maintainer release tools run in Go: `npm run release`, `npm run release:site`, `npm run docs-block` and `npm run changelog` build and run `native/cmd/cf-release` through `scripts/cf-release.sh`, with the same arguments, output, exit codes, file edits and git commands as the Node scripts, checked against goldens recorded from Node; the Node scripts stay because tests, the site data generator and the release workflow still use them, and nothing new ships in the npm package (B570)
- The native daemon's `status` no longer starts a Node helper: a block id outside printable ASCII is answered in Go when it is the one listed block or sits on a closed or stopped block (also for `status` and `native-status`); with two or more such listed blocks the daemon answers `status-unreadable`, as it did with no Node, and the status command computes the status itself (B576)
- The native rebuild in `gates run` and `block rebase` runs `go build` in Go instead of `node scripts/build-native.mjs --host`: same env, ldflags, `<binary>.<pid>.tmp` temp name, mode 755, rename and `native-rebuild-failed` text; it works with no Node on PATH, and then leaves the binary's Node version at `unknown` (B575)
- `facts` checks more glob path claims in Go, like Node's `path.matchesGlob`: braces (`{a,b}`, `{1..3}`), backslashes, `[:ascii:]` and `[:xdigit:]`, extglob openers with no `)` and non-ASCII text with Node's case folding. A Unicode POSIX class, an extglob that can close, a case-insensitive suffix with a sigma, a Codex L0 step (B551 is not on this base) and the gate's non-ISO dates (Node reads them in the local time zone) still hand over before any write (B577)
- `block rebase` that hands a narrow state to Node no longer prints an autopilot expiry warning twice: a failed grant expiry or an approval not restored yet is reported once, by Node's own check; a key or file read error in such a warning names no path, as Node's (B584)
- `block rebase` no longer starts a Node child: active and due autopilot approvals, run records with no orphans list, an odd sibling block or an unreadable config snapshot, falsy or invalid `rebase_check` values and a missing `HOME` run in Go with Node's output, run record and ledger rows; the few states Go cannot read hand over with a narrow named `rebase-*` reason before any write (B578)
- The Go worker, the daemon (`internal serve`) and every Go verb (review, `block close`, `block land`, gates, status, proof export and the rest) record each child they start in the run root's pid registry like Node's exec: `pids/<pid>.json` while it runs, the owner pid, start time and run id in its environment, the lazy root released at exit; stdout and exit codes do not change (B573)
- `block open` serves its former Node fallbacks in Go, byte-equal to Node: an empty `proof.worktree.dir` or `base`, `--after` statuses that are not text, claim rows whose path is not text, the signer key and ledger checks with no `--level`, brief loop, name-too-long and read errors, acceptance and pipeline YAML errors, pipeline and outcome read errors, no `HOME` and block ids such as `__proto__`; YAML forms Go does not reproduce, AJV schema wording, a torn ledger line, a failed home lookup and an unresolvable brief path hand over before any write under the new reasons `open-acceptance-yaml`, `open-pipeline-form`, `open-outcome-form`, `open-level-ledger`, `open-home-lookup` and `open-brief-cwd`; Go's ELOOP and ENAMETOOLONG texts are libuv's, as Node prints them (B581)
- CI on Linux is green again: the tools and init rows follow a platform seam, so the init wizard test replays its Mac golden (Homebrew hints, Solo.app) on Linux too; the `block close` fake reviewer writes its result file then renames it, like the worker, so close never reads a half-written result; the Jev triage test waits for its fake server before it reads the requests (the race detector caught the bare read); the 25 ms Go hand-over check reports its number on CI runners instead of failing, because the same binary measured 10 to 63 ms there, and local runs keep the gate (B560).
- A Go worker that hands its run to Node keeps the queue served while Node starts: it leaves a hand-over announcement that `review-file --wait` reads as live and Node's boot prunes, so a waiting review no longer ends `worker_down` during the hand-over (B561)
- Go `doctor` records each child it starts (CLI probes, pings and probe sessions, `git` and `op read`) in the run root's pid registry like Node, and Go `init` takes the temp root from the config at process start as Node does, so a `tmp.root` found from a subfolder still holds after init writes its own `.code-forge.yml` (B567)
- `code-forge review` of several files or of a test-like file runs in Go, byte-equal to Node, instead of handing the whole run to Node (`review-worker-handoff` is no longer used): the B561 hand-over announcement closed the race that made a waiting review read `worker_down` (B561b)
- Go `tools`, `tools install` and the `init` tool installs record each child in the run root's pid registry like Node (`pids/<pid>.json` with pid, start time and command while it runs, removed when it ends), so a later sweep can stop an orphan; a registry that cannot be written never stops the probe or install (B563)
- Go `logs report` records each `npm` and `gh` child in its run root's pid registry like Node (`pids/<pid>.json` with pid, start time and command while the child runs, removed when it ends), so a later sweep can stop an orphan; a registry that cannot be written never stops the child (B559)
- Tests give the same result on every machine: the doctor and tools rows no longer read this machine's `/Applications` (a seam the tests point at an empty folder), the no-config doctor test seals PATH, the init wizard golden compares copied skill files with the repo's `skill/` folder instead of a frozen copy, the B489 fake reviewer waits for its pid entry, and the Node gates tests expect `gates detect` and `gates scope` to run in Go (since B459); no product change (B552)
- `facts` builds the facts sheet in Go, byte-equal to Node: the claim tokens, the read-only snapshot at HEAD with in-project `--sources`, the L0 facts session on anthropic or xai, the answer checks and downgrade lines, the sheet, the ledger rows, the breadcrumb and error log, and `--run`; `spawn --role facts --cwd` runs in Go too. A Codex L0 step, a glob path claim, a non-UTF-8 brief, a run root inside a git work tree and a `--sources` path outside the project keep narrow hand-overs decided before any write (B556)
- `facts` serves in Go, byte-equal to Node, the cases B556 handed over: glob path claims, a non-UTF-8 `--brief`, a run root inside a git work tree, `--sources` outside the project, and Node's crash for a config with no `levels.L0` (same stderr, exit 1 and error-log row); the gate's Date.parse cases now hand over before any write. A Codex L0 step still hands over until B551 is on main (B568)
- `facts` refuses in Go, like Node, before its session starts: a missing, unreadable or too-new `.code-forge.yml`, an unreadable `--brief` or `--sources` path, a bad `--timeout` and a bad `--run` id give Node's stderr, exit 2, breadcrumb and error-log row with nothing written; a call that passes these checks still builds the sheet in Node (`facts-session`), and a YAML syntax error hands over as `facts-config` (B555)
- `logs report --no-ai` with matching errors runs in Go, byte-equal to Node: the npm version check, the built-in scrub, the secret check, the preview and `--dry-run`, the `gh` search, comment and create with the label retry, and the prefilled link with the saved report when `gh` is missing or fails; the AI scrub, `--with-doctor`, a known-fixes table, an unknown system and a terminal prompt still hand over to Node with named reasons (B554)
- `author`, `s2` and `jev ask` run in Go, byte-equal to Node: `author` and `s2` on anthropic and xai levels (prompt, provider argv, schema check, draft or answer, ledger rows, refusals), `jev ask` with `--rules` and with Jev (request, retries, failure lines, decision bands, `decision` row, refusals) using the env or file-store key; an openai (Codex) level, the keychain or 1Password key, YAML syntax errors and inputs Node words differently hand over before any session or write, each with its own reason (B550)
- `review` of one file runs in Go, byte-equal to Node: config, `--acceptance`/`--intent`, `--run` reuse, run start, the review block, the ticket and its wait, block stop, run end, the worker reap, the fix list, totals, `--json`, exit codes and signals. Several files or a test-like file, and state Go cannot read like Node, keep narrow named hand-overs decided before anything is written (B553)
- Go tests no longer fail on a loaded machine where they waited on a short fixed clock: the `block close` fake reviewer waits until close returns (30 s safety bound) instead of 2 s, and the run start worker-stop, file lock FIFO, test slot admission and leaked-pipe gate waits get long bounds that still end as soon as the awaited event happens; no product change (B539)
- The Go worker reviews light blocks (the batch round), Markdown files section by section, dual lenses with the L3 judge and consensus review, and recovers the recheck session (second level, its ladder, the L2 ladder retry, routes) like Node, byte-equal, instead of handing the ticket to Node; an L2 ladder entry with no provider routes like Node; a judge with no `levels.L3`, a light round after the cap with peer files, a surrogate-cutting Markdown heading and an adaptive pick of a no-provider entry still hand over with nothing written (B545)
- `init` runs its steps after the write in Go, byte-equal to Node: the Jev key from `CODE_FORGE_KEY_JEV` or the file key store (with the 24 h expiry cap and expired-entry removal), `--tools recommended` and `--yes-tool` installs, `--refresh-models`, and the quick doctor (its rows, JSON summary and exit 1 on a FAIL). The keychain key path stays on Node with the new narrow reason `init-jev-keychain`, and a doctor that would hand over makes init hand over before it writes anything (B548)
- `spawn --role coder`, `--role facts` and `--background` run in Go, byte-equal to Node: the coder prompt with `--block` and the run's block brief and acceptance lines, the `sentinel` result field, the `--run` `coder.sandbox` check, the facts session and the detached background start with its logs and pid files; a Codex step, facts with `--cwd`, a run record Go cannot read like Node and a background CLI that is not found still hand over with their own reasons (B549)
- The skill's `forge <verb>` shim now runs the package's `bin/code-forge` sh launcher (Go binary first, Node router with `CODE_FORGE_NATIVE=0` or no Go binary) instead of always the Node router `bin/code-forge.mjs`, so skill verbs ported to Go run in Go; `CODE_FORGE_BIN` naming the launcher, or a `code-forge.mjs` with the launcher next to it, also goes through the launcher, while other `CODE_FORGE_BIN` files and old installs with only `bin/code-forge.mjs` keep running in Node (B547)
- Codex (openai) sessions run in Go where `review.allow_open_book_codex: true` lets them: `spawn` reviewer, judge, s2 and author steps and the worker's ticket review on an openai level, with Node's `codex exec` argv, stdin packet, OpenAI-strict schema file, `-o` answer file, usage and cost rows, null drop and `passed` coerce, error events (usage limit retries, login expired, at capacity), timeout and exit codes, byte-equal to Node; an effort the codex builder refuses or a schema strict mode changes stays with Node, and doctor's Codex pings, isolation and path-deny still hand over (B551)
- `spawn --role coder` on an openai (Codex) step runs in Go, foreground and `--background`, byte-equal to Node: Node's `codex exec -s workspace-write` argv, a fresh `CODEX_HOME` per session under the run root's `codex-homes/`, the answer and sentinel read from the `-o` file and `outPath` in `pid.json`; the facts role on openai runs in Go too. Only a Codex coder step with `coder.sandbox` set still goes to Node (`spawn-codex-sandbox`) (B551r)
- `doctor` with a valid config finishes the probes' I/O errors in Go like Node instead of handing over after the probes started: an unwritable temp root gives Node's `probes: no temp root` row, a session that cannot start gives Node's `error(<code>)` rows, an unwritable prompt file, temp repo or canary ends with Node's `verb "doctor" failed` line and exit 1, and an errno Node has no name for prints Node's `Unknown system error -<n>` instead of `UNKNOWN` (B535)
- `init` at a terminal runs its wizard in Go: every prompt with its text, default, choices, keys and re-ask on a bad answer, the summary, the gates and proof choice and the apply confirm, then the B527 setup; terminal output, files, the init log and exit codes (cancel 1, end of input 13 with Node's warning) are byte-equal to Node in a pseudo-terminal; a terminal Go does not draw like Node, `--yes-tool`, the Jev key store, the doctor and preset shapes the wizard shows differently hand over before the first prompt (B528)
- The Go worker builds the whole review packet itself, byte-equal to Node: related files and definitions with the import-header context, the deletion note, the test-quality hint and moved-code hints with peer files (also in Markdown sections, the dual-lens judge packet and a light round after the cap); a recheck under `review.recheck_scope: file` and a recheck with moved-code hints and cross-file evidence from the block's other changed files run in Go too, so these tickets no longer hand over to Node (B537)
- `doctor` with a valid config runs in Go, text and `--json`, byte-equal to Node: the Jev live call, the provider CLI rows, the role pings, isolation, path deny, Codex rules and the worker and signer probe join the local rows; every case Go cannot serve is decided before the first probe, so a hand-over writes nothing. A worker registry Go cannot judge hands over after the probe cleans up, an errno outside Go's table prints `UNKNOWN` like Node, and one pid registry writer serves sessions and the doctor (B525)
- The Go worker serves runs with a Jev key: it resolves the key at boot like Node (env variable, file store, 1Password cache) instead of handing the whole worker to Node, and the review asks System 1 (Jev) like Node, byte-equal: `defect` triage in full rounds and on late findings, `resolved` closing open findings before the recheck session, the request, retries, timeouts, failure kinds, the `review.triage` rows with `source`, `p` and `band`, and the `s1_fallback` warning; a key in the OS keychain, a store Go cannot read back or a live 1Password read, and a bad key reference still hand over with their own reasons (`worker-jev-keychain`, `worker-jev-key-store`, `worker-jev-key-ref`) (B533)
- The Go worker runs the commit watch (`review.file_loop: watch`) instead of handing the whole worker to Node: the 15 s quiet wait and 120 s burst limit, one ticket per owned changed file hashed from git objects, the `watch.skipped`/`watch.held` rows, superseded markers and the watch cursor, byte-equal to Node; a watch shape Go cannot reproduce stops the Go worker before that tick writes (`worker-watch-shape`) (B534)
- `autopilot ask` runs the delegate session in Go when the grant lets the question through, and `autopilot approve` writes the `.code-forge.yml` change, reloads the run and signs the `autopilot.approve` row in Go, byte-equal to Node; a due approval of an active run is put back and reloaded in Go too. Shapes Go does not reproduce keep a narrow hand-over decided before any write (B536)
- The Go worker's review recovers like Node instead of handing the ticket to Node: the `levels.L2` fallback ladder, `review.routes` recovery and `review.second_levels`, with Node's rows, warnings and session configs byte-equal, and the timeout fallback picks among ladder, second-level and route entries like Node; malformed shapes and the recheck session's recovery still hand over (B538)
- `init --no-interaction` (and init with no terminal) sets the project up in Go, byte-equal to Node: gate and proof detection, the settings summary, the merge with an existing `.code-forge.yml`, the config checks, the `.code-forge.yml` write with the `yaml` package's exact bytes, skill links and `installs.json`, the user config, the engine stop text, the init log and the agent JSON line; a Jev key read, a 1Password item, `--tools recommended`, `--refresh-models`, the doctor, links Go cannot place, configs Go cannot rebuild or check and an unreadable user config hand over before any write, each with its own reason (B527)
- Tests can no longer stop a process of the person running them: every signal to a pid from the process table or a run record (the `run start`/`sweep` process sweep, `run end` and the launched-worker stop, the Go worker's orphan sweep, the gates' and sessions' leftover group members) goes through one guard that, inside `go test`, in a binary a test builds, or with `CODE_FORGE_TEST_SIGNAL_ROOT` set, sends a real signal only to descendants of the test and answers EPERM otherwise; without them nothing changes. `doctor` counts a `.nan` level value as unset, as Node does (B529)
- The doctor's worker and signer probe has a Go part, not wired into the full run yet: the throwaway run and repository, the `signer key`, `worker` and `signer` rows byte-equal to Node (worker announced and stopped on SIGTERM, a worker that never announces, one that ignores SIGTERM, a failed sign/verify round trip, a git or config copy failure), and the run, key, lock, pid entry and temp root removed afterwards; `run start` shares its record and locked create with it (B524)
- `init` serves in Go, byte-equal to Node, every usage error, no `HOME`, a `.code-forge.yml` that cannot be loaded (`read-error`, `migrate-error`, a root that is not a mapping) and the draft-PR refusal without `--coder-may-push yes`, with the init log, the agent JSON line, the breadcrumb and the error log; the rest of the wizard hands over before Go writes anything, each with its own reason (`init-interactive`, `init-setup`, `init-jev-link`, `init-arg-encoding`, `init-config-syntax`, `init-answers`, `init-output`) (B502)
- Plain `run start` and `run start --add-worker` launch this binary's Go worker (`code-forge worker --run <id>`) and pin its pid, so they no longer need Node or hand over when no Node is found; a run the Go worker does not serve still becomes the Node worker in the same process, and the process sweep now also stops a Go worker of a finished run (B511)
- The hand-over reason list (the `handoverReason` constants and the `handoverReasons` array) moved unchanged from `handover_log.go` to its own file `native/cmd/code-forge-native/handover_reasons.go`, and `.gitattributes` now marks only that file `merge=union`, so a union merge can no longer garble the hand-over functions; a Go test checks every reason has one non-empty, unique code (B513)
- `native-status`, the private daemon helper, runs in Go with the same status lines, line splitting, router error line, exit 1, breadcrumb and error log as Node, and `--help`/`-h` print its usage; no `HOME`, a terminal stderr with no agent, and a line Go cannot read exactly like Node (a non-ASCII block id to sort, an `Infinity` number, a lone surrogate, bytes that are not UTF-8) still hand over to Node with their own reason (B501)
- The native daemon answers `status` in Go where it used to start Node's `native-status` helper: a run record or ledger row Node cannot read gives `status-unreadable`, and a spend that overflows is `null`, with the same gate timeout and flaky details; only a block id outside printable ASCII still uses the helper (B503)
- Go has the doctor's Jev client (Node's request, retries, 30 s attempts and failure kinds), its `jev` row and the provider CLI `cli`/`flags` rows; the full doctor run still hands over to Node as `doctor-live-probes` until the model-call, isolation, path-deny, Codex rules and worker probes run in Go (B505)
- The full `doctor` run with no `.code-forge.yml` runs in Go (text and `--json`) with Node's rows, exit code, summary and error log: tools, ledger, locks and worker registry, tmp roots, the read-only process sweep and memory rows, review state, review concurrency, probes skipped, signer, native binary and native daemon; Go functions for the gate command, coder sandbox and darwin worktree-indexing rows match Node's; a config that loads but fails validation (`doctor-full-invalid-config`), a live project worker (`doctor-full-live-worker`), a worker registry Go cannot judge, another native binary and an unreadable review-state record still hand over to Node with their own reasons (B504)
- Go has the doctor's model-call probes with Node's rows: the role pings (facts, coder, reviewer, s2, delegate) on anthropic and xai with the same argv, budget, timeout, schemas and rate-limit retries, and the isolation, path-deny and Codex rules probes; Codex sessions and config shapes Go does not reproduce stay with Node, and the full doctor run still hands over as `doctor-live-probes` until the doctor calls them (B523)

### Added

- `block accept-hit`, `block amend-acceptance`, `block unwaive`, `block ack-waivers` and `block attempt` run in Go with Node's terminal question, refusals, signed ledger rows, registry changes and text or JSON output; `block waive` at a terminal asks and signs the human waiver in Go; the `--yes` judge and `block reopen` at a terminal still hand over to Node (B453)
- `block close` runs in Go for an ack digest in Node's Latin `localeCompare` order, an object or array `decision` (Node's key order), V8 `Date.parse` rollover and 2-digit date forms, and late or cap rows whose file is empty, a number or false or whose finding is missing or not text; on a shared terminal its stderr lines (auto-claim, WARN, refusal summary) come before the stdout result as in Node, and `--auto-claim` claims before a later transcript or `--worker-pid` refusal as Node does; non-Latin ack digests, object or array late/cap values and out-of-range legacy dates still hand over (B498)
- `block close --auto-claim` runs in Go on a run with an autopilot record and with `close.require_human` at a terminal (same prompt text and answers; a cancel records nothing); a transcript that runs the `hooks.on_block_end` command is Node's `rule_break` in Go `block ready` and `block close` (Go close skipped this check before and closed where Node refuses); late findings with no file, legacy `toUTCString`/`toString` review dates and non-ASCII ack digests run in Go; an object decision, an unknown date form and an order-dependent ack digest still hand over to Node with their own reason (B493)
- `block close` refuses a malformed, missing, not-a-directory, other-branch or foreign block worktree and one without the run's workspace directory in Go with Node's messages, reads ledger null and scalar rows and an unreadable rotated ledger file like Node, and reads the ledger before the run-ended check; `block open --brief` prints Node's realpath and read errors; `block stop` with a daemon socket runs in Go and its done file takes the serving daemon's rows; `block land` prints Node's read error for a run file it cannot read (B470).
- `keys list`, `keys set --cache-hours`, `keys test` and `keys remove` (file key store), `report --slug` (text, `--json`, `--export`, all 18 sections) and `doctor --help` run in Go with the same output, exit codes and files as Node; 1Password and keychain writes, a typed `keys set`, every doctor check run and ledger rows whose bytes depend on JavaScript coercion still hand over to Node (B456)
- `ledger calibration`, `ledger outcome` (`--pr`/`--ci` and `--scan-git`), `ledger add coder` and `ledger outside-review` run in Go with Node's output, exit codes, signed rows and error-log entries; `logs clear` (no log, `--yes`, no-terminal refusal), `logs report` with nothing to report, and `logs` help, unknown subcommands and usage errors run in Go; the clear prompt on a terminal and a report that would file an issue hand over to Node with their own reasons (B457)
- Go review plays a `needs_file` answer (the file round with attached files), a schema miss (the signed `review.schema_invalid` row with its diagnosis) and a second same-model timeout (the ledger-confirmed fallback reviewer) like Node instead of recording them as `unavailable` (B514)
- The Go worker plays the recheck and patch-check rounds and reopens a cap stop or a split stop when the config is raised, like Node, instead of handing the ticket over (B517)
- Go `worker --run <id>` serves the queue: boot, registry, heartbeat, run root, ticket reviews, orphan sweep, idle exit and a clean stop on SIGINT/SIGTERM, with `--once`; it gives the whole worker to Node for a Jev key, the commit watch, no config snapshot and on the first ticket Go cannot review, so no ticket is lost or reviewed twice (B489)
- Go worker serves the other worktree codes, Node's one config-read retry and the `worker-error` result for an I/O error (with the routing `detail`) byte-equal to Node, so those tickets no longer go to Node (B507)
- Go `worker.TicketRunner.RunTicket` now reviews a ticket end to end when a dry pass finds every part ported (rules risk, block kind, acceptance facts, rules digest or `context-unreadable`, owned/changed/test files, paired test context, one fix-loop round with one L2 session): rows, review state, signed result, timings and done marker (now with `open_findings`) are byte-equal to Node on the same repo and fake reviewer CLI; anything else stays unsupported with nothing written and `worker` still hands over (B497)
- Go `worker.FixLoopRound` wraps one fix-loop round like Node's `fixLoopRound` (block lock, once-per-block `review.budget` row, state load with tampered / lost / restored / legacy move, L3 rung, `hash-moved` approval check, crash saved as retry, outcomes `approval-not-written`, `findings-open`, `no-fix-hunks`), byte-equal to Node goldens; tickets needing reopen rows, extra rounds, rechecks or patch checks stay unsupported with nothing written, and the worker does not call it yet (B496)
- Go `review.RunRound`, `review.BudgetRows`/`review.RulesRisk` and `worker.SaveState` port the worker's full fix-loop round, block budget rows and signed review state byte-equal to Node (golden tests over Node's own output); tests pin `pickReviewer` exploration and strict date parsing to Node; the worker does not call them yet and still returns unsupported for review sessions (B495)
- Go `worker.TicketRunner.RunTicket` routes a ticket like Node (`review.routes`, adaptive L2 routing from the ledger, signed `review.route` rows, `route-invalid` and `route-row`) and serves the engine's first refusals (`review.exclude`, unreadable run record, unknown block, missing base, malformed worktree, amended acceptance), byte-equal to Node; tickets that need a review session still return unsupported with nothing written, and `worker` still hands over (B492)
- Go `native/worker` runs one ticket's refusal outcomes (`TicketRunner.RunTicket`): bad ticket, bad path, other run, stale or missing file, worktree mismatch, unreadable file, gone or malformed worktree, unreadable run record and missing config snapshot, with Node's file lock, signed result, ticket timings, done marker, signed `review.result` row and poison rule, byte-equal to Node; tickets that need a review (routing, risk and facts, fix loop) return unsupported with nothing written, and `worker` still hands over (B488)
- Go package `native/worker` holds the review worker's boot and ticket pool (boot lock, registry admission, pins and the signed `worker.reattach` row, announce and heartbeat files byte-equal to Node, clean stop, queue order, Node's ticket claim lock, `parallel_tickets`, `provider_parallel` and `fair_share`); the per-ticket review is not ported yet, so `worker` still hands the queue over to Node (B485)
- `spawn --run <id>` runs in Go for the roles Go serves without `--run`: the run's temp root, then the run-pinned `budget.usd` and autopilot category gate before each step (Go package `native/session`), with the same refusals, stderr, exit 3, `budget.refused`, `budget.warning` and signed `autopilot.pause` rows, saved pause and error-log warning as Node; `--run` with the coder role and `--background` still hand over (B482)
- `spawn` runs a foreground closed-book session in Go (Go package `native/session`: argv, fallback ladder, rate-limit retries, hard and silence timeouts, pid registry, answer and usage parsing, the ledger row) for the reviewer, judge, s2, author and delegate roles on anthropic and xai steps with no schema or the finding schema, and `--print-argv`, with Node's stdout, stderr, exit code and row; `--run`, `--background`, the coder and facts roles and other schemas still hand over to Node; `review.Deps.Spawn` can now run on this session (B477)
- Go can review one ticket on the single-reviewer path (`native/review`: packet, one L2 session through a spawn seam with the same-model timeout retry, the stub guard, the `review.plan`, `review.unavailable` and `review.session_timeout` rows and Node's result object), equal to Node on 25 tickets; a case it does not cover is refused before a session starts, or stops with a gap error when it shows only after the session (`needs_file`, a second same-model timeout, a schema miss with a ledger), and the `review` verb still hands over to Node until B477 wires it (B476)
- `worker`, `spawn` and `review` run in Go for help, usage errors, unknown flags and the refusals Node makes before work starts (a bad or ended run for `worker`; config, `--timeout`, `--schema`, run id, level and role for `spawn`; no repository, a bad base or `--files` path for `review`), and `review` with nothing left to review runs fully in Go (text and `--json`, with the skipped-file notes); serving the queue, the session and a review run with files still hand over to Node with their own reasons (B460)
- `autopilot binnacle`, `autopilot log`, `autopilot round`, `autopilot level`, the start prompt at a terminal without `--yes`, and `autopilot status` and `autopilot stop` for a grant with no link (the binnacle files) run in Go with the same output, exit codes, files and signed `autopilot.extra_round` and `autopilot.level` rows as Node; `ask`, `waive`, `approve` and runs with active approvals still hand over to Node with their own reason (B478)
- A due autopilot approval of an ended run now puts the old value back in `.code-forge.yml` in Go, with the same bytes the `yaml` package writes (comments, blank lines, quoting, flow and block lists, document markers, CRLF), the file's mode kept and Node's WARN lines for a parent that is not a mapping or a failed write; YAML shapes Go does not write byte for byte still hand over to Node (B494)
- `autopilot approve` at a terminal (the owner's prompt with clack's wrapping, the cancel text, a failed prompt and the refusals that come after the answer without a config change) and a due approval that needs no `.code-forge.yml` write or run reload (a pending signed row, a value changed by hand, an ended run that already holds the old value, a missing or unreadable config) run in Go with the same output, exit codes, records and signed rows as Node; an approve that changes the config, a due approval of an active run or one that writes the old value back, and config text Go reads differently still hand over to Node with their own reason (B490)
- `autopilot ask` (every refusal before the delegate session, in text and `--json`, with the signed `autopilot.expire` row of an expired grant), `autopilot approve` (every input check and the no-terminal refusal) and runs with active approvals that are not due run in Go with the same output, exit codes, records and rows as Node; `ask` with a grant that lets the question through, `approve` at a terminal, a due approval (`autopilot-restore`) and `waive` still hand over to Node (B484)
- `autopilot binnacle`, `autopilot log`, `autopilot round`, `autopilot level`, the start prompt at a terminal without `--yes`, and `autopilot status` and `autopilot stop` for a grant with no link (the binnacle files) run in Go with the same output, exit codes, files and signed `autopilot.extra_round` and `autopilot.level` rows as Node, and mask the run's signer key in output and binnacle files at the same points as Node; `ask`, `waive`, `approve` and runs with active approvals still hand over to Node with their own reason (B478)
- `autopilot status`, `autopilot stop` and `autopilot start` (with `--yes` or with no terminal) run in Go with the same output, exit codes, run record and signed `autopilot.grant`, `autopilot.stop` and `autopilot.expire` rows as Node, as do `autopilot` help, usage errors, unknown flags and the `approve --yes` refusal; the start prompt at a terminal, `ask`, `waive`, `round`, `level`, `approve`, `binnacle`, `log` and grants with no link still hand over to Node with their own reason (B461)
- `proof red-green` runs in Go with the same output, exit codes, export tree, lock, journal and signed `export` and `red-green` rows as Node, and `proof` counts every project config rule in Go, including `proof.runners`, `review.routes` under multimodel and `coder.sandbox.add_dirs`, and loads the config without the merge-gate check as Node does; a red-green that uses a `proof.runners` entry or the Pest default, has a snapshot Go cannot read, or meets another error before the export still hands over to Node with its own reason (`proof-runner`, `proof-snapshot`, `proof-red-green`) (B481)
- `proof lock`, `unlock`, `restore`, `export` (also `--remove`) and `proof tier` with a project config run in Go with the same output, exit codes, run record, lock, journal, export tree and signed ledger rows as Node; `proof red-green` and the config rules Go does not check yet (`review.routes` under multimodel, `coder.sandbox.add_dirs`, `proof.runners`, a `schema` merge gate) still hand over to Node with their own reason (B462)
- `help`, `version` and the `--help`/`-h`, usage errors and unknown flags of `init`, `native-status`, `facts`, `author`, `s2`, `jev` and `proof` run in Go with the same output, exit codes, breadcrumb and error log as Node, as do `proof baseline`, `proof tier` without a project config and the `jev ask` and `proof` argument checks; model sessions, the init wizard, `native-status`, `jev ask` past `--state`, `proof tier` with a config and the `proof` run work still hand over to Node with their own reason (B458)
- `gates detect`, `gates secret-scan`, `gates safe-edit`, `gates scope`, `gates acceptance` and `gates transcript-grep` run in Go with the same output, exit codes, error rows and breadcrumbs as Node, as do `gates` help and usage errors; `gates run --help` now prints the usage instead of running the gates; the cases Go cannot match exactly hand over to Node with their own named reason; `gates detect` treats a prettier dependency set to 0 as absent, as Node does (B459)
- `code-forge sweep` runs in Go: help, usage errors, the dry run (text and `--json`, idle workers included) and `--apply` give Node's output, exit codes, logs and signals; no `HOME`, an unreadable temp base and an idle check Go cannot judge still hand over to Node (B486)
- `run start` stops in Go what Node's process sweep stops (a worker of a finished run, an orphan whose owner is gone, a registered child of a dead temp root) with Node's checks and signals, and `run start` and `run reload` report a duplicate key or an unresolved tag in `.code-forge.yml` in Go with Node's text; a `run reload` of a config that loads, other YAML syntax errors and `run start` without Node keep the Node hand-over (B472)
- A plain `run start` (config snapshot and hash, run record, key, mirror, signed rows, dead temp root removal, worker launch and pin), `run start --add-worker` and the `run reload` refusals run in Go with Node's output, exit codes, files and rows; a process sweep that would stop a process, a YAML syntax error and a `run reload` of a config that loads still hand over to Node (B468)
- `run status`, `run end` and `run start --reattach` (with or without `--worker-pid`) run in Go with the same output, exit codes, run record, mirror and signed ledger rows as Node, as do `run` help, usage errors and unknown flags; a plain `run start`, `run start --add-worker` and `run reload` still hand over to Node with their own reason (B454)
- `block close` and `block ready` read Codex `exec --json` transcripts and decide the `code-forge-reviews-write` rule in Go with Node's hits, an unreadable `--transcript` prints Node's error and exits 1 in Go, and review dates in every ECMAScript date-time form are read like V8; hand-made late/cap rows with no file, object decisions, non-ASCII ack digests and the open, claim, stop and land edge cases still hand over to Node (B466)
- The full `doctor` run still hands over to Node, but its hand-over now says why: `doctor-live-probes` for a valid config (live Jev call, provider model probes, worker start and stop), `doctor-full-no-config` when no valid config loads (the process sweep, tmp-root and worker-registry checks are not in Go yet), and `doctor-full-run` only for a config Go cannot judge (B487)
- `doctor --quick` now refreshes the cached Jev key in Go like Node: an expired file-store entry is deleted with its index row, an entry kept longer than `keys.cache_hours` is cut (entry and index row rewritten), and an `op://` reference is read with `op read` (one retry after a first-call timeout, logged as an `op_retry` warning) and cached with its index row, with Node's 1Password reasons; only an index Go cannot read exactly like Node still hands over (`doctor-key-refresh`), and the full run stays in Node (`doctor-full-run`) (B483)
- `doctor --quick` runs in Go with Node's text, `--json`, masking and exit codes: config, harness links, Jev key (env or file store) and reviewer rows; `doctor` help, usage errors and unknown flags too; the full run, the keychain store, a key refresh or 1Password read, schema errors, secret-shaped values, sandbox dirs and ledger rows Go cannot read exactly still hand over to Node with their own reasons (B474)
- `run reload` of a config that loads runs in Go with Node's output, exit codes, run record, mirror, ticket pins and signed `run.reload` row under the run lock, inside the one `run` router; YAML syntax errors, schema errors, secret-shaped values, sandbox dirs, `!!` tags and odd record shapes keep their own named hand-overs (B473, landed by B491)
- `plan check` (facts, slug, config caps and budget, ledger lanes, claims, overlaps, cycles, imports) and `upgrade` re-pointing recorded installs run in Go with Node's output, exit codes and installs file; odd cases keep their own named hand-overs (B475)
- `validate` and signing a passing `outcome record` run in Go with Node's output, exit codes and signed row; `plan check`, `sweep` and `upgrade` answer help, usage errors and bad files in Go, while the plan check itself, the process scan and re-pointing installs still hand over to Node with their own reasons; schema errors, secret-shaped values and sandbox dirs in `validate` hand over too (B471)
- `resolve`, `models`, `list`, `remove`, `pipeline status|next`, `outcome seal|check` (and the check part of `outcome record`) and `tools [install]` run in Go with Node's output, exit codes and files, including help, usage errors and unknown flags; YAML syntax errors, schema errors, files Go cannot write, signing a passing `outcome record` and the `tools install` yes prompt still hand over to Node; `validate`, `plan check`, `sweep` and `upgrade` still run in Node (B455)
- Go prints the router's once-a-day error-log hint on a terminal (and keeps `~/.code-forge/logs/hints.json`) for the verbs it serves, so `ledger` no longer hands a failure over for it; `logs clear` asks its confirm prompt in Go; `pipeline status|next` reports outcome files it cannot read (for example a folder) in Go; a hint state Go cannot judge hands over as `router-hint-state` (B500)

### Fixed

- The worker's daemon (`internal serve`) no longer leaves its `proc-<pid>-<ms>` temp root behind when it gets two stop signals: a stop of the worker's process group sends one and the worker's daemon stop a second, which killed the daemon after its first signal handler ended and before it removed the root (seen on Linux CI, where the daemon socket path fits); the daemon now keeps SIGTERM and SIGINT handled until it exits and stops without listening when a signal comes during its start (B602)
- `logs report --no-ai` with a long folder path in an error message (such as a temp scratchpad path) gives the same result in Go and Node: both refuse it as a high-entropy string at the same report line, exit 1, unless the cleaning turns the whole long part into `~` or `<project>`; the earlier report of Node refusing while Go printed came from two different error logs, not from the code (B588)
- Issue #36: `block rebase` diffs merges on main against their first parent only, so a feature branch cut before main changed an owned file no longer reports that file as `upstream_owned` (or, with `--accept-changed`, as a false `owned-conflict`); a batch `block land` merge gate now links `gates.merge_gate_links`, runs `gates.merge_gate_setup` and honours `--copy-links`; a retry of a closed block under active autopilot approvals stays in Go instead of Node's `block is closed, not open`; `native/README.md` names the `internal block-rebase-landed` helper with its prefix (B589)
- Go `doctor` hands a worker registry it cannot judge to Node only after its probe worker is gone: the strict launch now waits (at most 2 s, like Node's stop after SIGKILL) until the killed worker is reaped, so no live or zombie worker pid is left behind on a loaded machine (B593)
- `block land` and `review-file --wait-block` no longer stop at the join with "reviews did not finish" on a busy machine: a git helper that exited 0 counted as failed when its output pipes were read more than 50 ms after it exited (`exec.ErrWaitDelay`); the pipe grace is now 2 s. The doctor golden tests read a sealed process table, so a zombie of another program on the machine no longer changes their `sweep` row (B544)
- CI fixes for the red main runs: the Go job installs the npm packages (`npm ci`, Node 24, npm cache) so the Go parity tests can run the Node reference, and 12 Node tests now expect what moved to Go (`--version`, `logs clear`/empty `logs report`/no `HOME`, ledger calibration/outcome/add/outside-review, an unreadable land transcript), copy every native package the config imports into their fixtures, measure the hand-over budget on a verb that still hands over, and size the npm package for the host binary only (B541)
- `code-forge setup` (also `npm run setup` and the npm `postinstall`) checks the Go binary for this OS and processor: restores lost execute bits, compares `internal version` with the package version, prints one OK line or the problem plus one `fix:` line (exit 1); the postinstall form only warns and exits 0 (B463)
- A Go panic in a goroutine a command starts (the gates `block land` runs next to its review wait, `gates run`'s gate wait and output copies, the daemon, signal forwarders) follows the main-goroutine rules: a read-only command goes to Node with a `go-panic` row, any other command prints one line, writes a `go-panic-stopped` row and exits 1, never exit 2 with a stack trace; the panic line also reaches the terminal for `gates`, `block land` and `block rebase`, whose stderr is copied through the heal pipe (B509)
- A Go panic no longer looks like a usage error (exit 2 with a stack trace): a read-only command (`setup`, `status`, `run status`, `queue status`, `heal list`/`show`, `decisions list`, `docs path`, `logs`, `logs summary`/`path`/`handovers`, `ledger tail`) goes to Node with a `go-panic` hand-over row; any other command is not re-run, prints one line naming the command and code site (never the panic text or arguments), writes a `go-panic-stopped` row and exits 1; a panic in a background goroutine still exits 2 (B508)
- `code-forge setup` (also `npm run setup`) checks the Go binary for this OS and processor: restores lost execute bits, compares the binary's version with the package version, prints one OK line or the problem plus one `fix:` line (exit 1); the Go binary serves it, and the entry hands it to Node's `scripts/setup.mjs` when the binary is missing, not executable or crashes; npm runs no install script (no `postinstall`), so after `npm install -g @codedology/code-forge` run `code-forge setup` (B463)

## [0.22.1] — 2026-11-11

### Added

- `run status`, `run end` and `run start --reattach` (with or without `--worker-pid`) run in Go with the same output, exit codes, run record, mirror and signed ledger rows as Node, as do `run` help, usage errors and unknown flags; a plain `run start`, `run start --add-worker` and `run reload` still hand over to Node with their own reason (B454)
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
