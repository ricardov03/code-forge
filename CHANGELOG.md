# Changelog

All notable changes to `@codedology/code-forge` are recorded here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions follow semver ahead of a 1.0.0 that
waits on a second real consumer (plan §10.4, Q17).

## [Unreleased]

### Changed

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
