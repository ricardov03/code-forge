# @codedology/code-forge

A CLI (`code-forge`) plus one Agent Skill that runs the **plan → harden → code → review** pipeline
for cross-model, parallel, evidence-gated feature delivery — on Claude Code, Codex, Grok, and Solo.

> **Status:** published on npm. The CLI, the decision layer, the review engine, the worker, the
> proof policy, the installer, the Agent Skill under `skill/` and the example repo under
> `examples/` have all landed (`docs/reference/blocks.json` lists every build block). What changed
> in each version is in [CHANGELOG.md](CHANGELOG.md).

## What's new in 0.11

In the next release:

- **Faster fix rounds (B99).** `code-forge gates run --run <r> --block <id> --changed` runs only the tests of files changed since the last green gate run, and `{changed_files}` passes only the changed files to lint or format gates. It falls back to the normal scope when it cannot be sure.

In 0.11.0:

- **The release moves the README What's new list (B108).** `npm run release` now moves the "In the next release:" bullets under "In X.Y.Z:". A minor or major release starts a new "What's new in X.Y" heading. The empty anchor stays for the next block. The release refuses an empty CHANGELOG [Unreleased] or a missing anchor before it changes anything, and a failed check puts the README back with the other files. The checks now include `gen-config-doc --check`. `docs/releasing.md` has a releasing checklist.
- **The close writes the ticket lines (B96).** The coder's final report no longer needs a `reviewed <path> ticket <id>` line per file. `block close` prints one for every file the review gate accepts (approved, waived or answered), with the queue's ticket. `--json` and the block's done file carry them as `reviewed: [{file, ticket, via}]`. With `--report`, a hand line that names another ticket prints `WARN report ticket mismatch <path>` on stderr, and the close goes on.
- **A big-diff file steps up to a full review (B109).** A low-risk file whose diff is too big for the quick review budget used to stop with `split_required`, and splitting it only made the diff bigger. Now the review plans again as one full L2 session. If the diff fits the full budget, the file is reviewed. The `review.plan` row records the step as `step_up`.
- **A file that was too big can be reviewed again (B110).** A first-round `split_required` result ran no review. After you raise the budget and run `run reload`, `review-file` (or `review-file --fresh`) tries again with the same ticket. A recheck stop still needs `block reopen` or `block waive`.
- **A recheck that did not really review never stalls (B111).** A recheck on unchanged content runs no session and counts no round; it returns `reason: 'no-fix-hunks'`. A recheck answer that skips an open finding is a broken answer, not a "still open". So the stall rule sees only real rechecks. The recheck summary now shows each session's status and tokens.
- **Paired files for the reviewer, part 1 (B97).** code-forge can now find a changed file's test file (or a test's source file) by name, and list its test names or exported symbols in a small `## paired` packet section. The section is dropped first when the packet is too big. A reviewer can also ask for a new, untracked test file when the block owns and changed it. B98 sends these to the reviewers.
- **Reviewers see the paired test file (B98).** When a block changes `src/a.mjs` and `test/a.test.mjs`, the reviewer of `src/a.mjs` now gets the test names in a `## paired` section, in the first review and in rechecks. Tests named with `block open --tests` count too. The reviewer is told those tests exist, so it no longer reports them as missing. It can still ask for a test file's body, even a new untracked one the block owns.
- **Faster reviews (B112).** A review ticket reads the diffs of the block's other changed files only when the review asks for them, and only once.
- **No leftover workers (B113).** A worker stops by itself when its run record or its HOME is removed. `npm test` now fails when a test leaves a worker running.

## What's new in 0.10

In 0.10.0:

- **Choose how long a 1Password key stays cached (B84).** Set `keys.cache_hours` (1 to 24, default 8) in `.code-forge.yml`, or `keys set <name> --op <ref> --cache-hours <n>` for one key. A lower value shortens keys that are already cached at the next read.
- **Warnings before a cached key runs out (B85).** `keys list` and `doctor` warn when a 1Password key has less than 1 hour left. When the cached key expired and 1Password does not answer, `jev ask` prints the exact command to re-link it: `code-forge keys set jev --op <ref>`.
- **A test report reader for red-green proof (B86).** code-forge can now read a JUnit XML test report (XCTest, Swift Testing, PHPUnit, Pest, pytest). A missing, broken or empty report, or an error that is not a failed assertion, never counts as red. B87 connects it to `proof red-green`.
- **Red-green proof for any test runner (B87).** A new `proof.runners` list in `.code-forge.yml` names a runner per test-file glob: an adapter (`junit` or `swift`), an argv with `{test_file}`, `{test_id}`, `{test_filter}` and `{report}`, optional `env` and `timeout_s`. `proof red-green` runs that argv and reads its JUnit report, so PHPUnit, Pest, XCTest or pytest tests can be proven. The list is fixed at `run start`; a bad entry refuses the proof and never falls back to `node:test`.
- **Red-green proof for Swift (B88).** A `proof.runners` entry with `adapter: swift` runs `swift test --parallel --filter {test_filter} --xunit-output {report}` and reads both the XCTest and the Swift Testing reports. The test filter is anchored, so a similar suite or test name is never picked up. A test that does not compile at base is `RED_INVALID compile` (a test of a new API needs the human waiver). Checked live with Swift 6.3.3 and Xcode 26.6.
- **Red-green proof for PHP (B89).** A `proof.runners` entry with `adapter: junit` and `[php, artisan, test, "{test_file}", --filter, "{test_filter}", --log-junit, "{report}"]` proves PHPUnit and Pest tests in a Laravel app. A test of a class the block added is `RED_INVALID import`, also when Laravel prints its AI-agent JSON summary. Checked live with PHP 8.4, Laravel 13, PHPUnit 12 and Pest 4.
- **Red-green proof for Xcode projects (B102).** A `proof.runners` entry with `adapter: xcodebuild` and an `xcode` object (`scheme`, `destination`, `test_target`) proves XCTest and Swift Testing tests through `xcodebuild test` on a simulator or on macOS. code-forge checks first that full Xcode and the simulator are there, runs only the named suite with `-only-testing`, and reads the result bundle. A build failure is `RED_INVALID compile`. code-forge never boots, creates or deletes a simulator.
- **Xcode red-green checked live (B103).** An opt-in live check (`CODE_FORGE_LIVE_XCODE=1`) proves XCTest and Swift Testing tests red→green through `xcodebuild test` on an iPhone simulator. It skips with a reason when full Xcode or a simulator is missing. Verified with Xcode 26.6.
- **Big files go to a big-context reviewer (B90).** Set `review.routes` once, for example `{min_lines: 400, model: <id>, session_timeout_s: 900}`. Files with that many lines get that model and timeout. No more editing `.code-forge.yml` per file. Each routed file gets a signed `review.route` row.
- **Timeouts reach the second level (B91).** A timeout now counts as a broken result, like an answer that fails the schema. At the second broken result on one file, the second-level model is tried at once, in the same ticket. You no longer wait for a second ticket.
- **Test-quality hint (B92).** A test that reads a source file and checks its text is a weak test. The review packet now lists those lines for the reviewer. The reviewer decides if it is a problem. The list is a hint, not a finding.
- **More room for the Codex coder (B93).** New setting `coder.sandbox`. With `profile: toolchain`, the Codex coder may also write to the folders you list in `add_dirs`, such as a build cache. With `profile: full`, the Codex sandbox is off and `doctor` warns you. Secret folders like `~/.ssh` are always refused. The setting is fixed for the whole run.
- **One fix round per block (B94).** The skill now waits for every review and test of a block. Then it sends the coder ONE fix message with every failing test and every open finding. It never sends findings one at a time.
- **Waive and reopen ask you at a terminal (B80).** `block waive` and `block reopen` now ask the human for a yes in their own terminal. There is no `--yes`. With no terminal they record nothing. The prompt is a speed bump, not proof of a human: a pseudo-terminal passes it.

## What's new in 0.9

In 0.9.0:

- **A lost review state is rebuilt, not reset.** When the review state file of a file is gone (for example, macOS cleaned its temp folder), the worker rebuilds it from the signed ledger and writes a `review.state_restored` row. Rounds, open findings and stops stay as they were. If the ledger cannot rebuild it, the ticket is refused as `review-state-lost`, not `tampered` (#15).
- **Review state no longer lives in the temp folder.** It is saved under `~/.code-forge/runs/<run>/review-state/`, so a temp cleanup or a worker restart does not touch it. A state file from an older run is moved there once, after a check against its signed anchor. `code-forge doctor` warns when a state file of an active run is missing and says whether the ledger can rebuild it (#15).
- **Waiving every finding is a real way out.** A file whose signed review on its current content left findings, all cleared by accepted waivers, now counts as reviewed (`reviewed_by_waiver`, never approved). `block close` writes one signed `gate.review_waived_all` row for it. A critical still needs a human waiver, and a finding with an unknown severity does too (#9).
- **Tests no longer fail because of your own live run.** The test guard used to fail when a code-forge run you had open wrote its lock file during `npm test`. Now new files of a run that existed before the tests are not counted as leaks. A new run or any other new file still fails the tests.
- **A restore brings back the exact reviewed text.** The reviewed text is now also kept as a git object, and the signed ledger points to it. After a lost state file is rebuilt, the next round reviews only what changed since the last review, even when the file changed. If git has removed the object, the round reviews the full file, as before (#15).
- **A fix in another file can answer a finding.** `review-file <file> --block <id> --answers <ids> --of <source>` asks a fresh reviewer whether a change in another owned file of the block fixes findings of the source file. Each finding it judges `resolved` gets a signed `review.answered` row, and the source file can then close as `reviewed_by_waiver` with an `answered` list. If either file changes later, the answer no longer counts. The answering file still needs its own approval (#9).
- **`block reopen` restarts a stopped file.** After you change a file whose review stopped, `code-forge block reopen <id> --run <r> --file <path> --reason <text>` starts a fresh round 1 for it. A file whose review state is lost can be reopened on its current content. Only a human may run it: a coder's command line is blocked, like `block waive`. The block's one L3 step stays used. The same content is refused, so you cannot simply roll again (#12, #15).
- **Safer answers packets and clearer reopen errors (B79).** An id named only by a `review.cap` row with no recorded severity is refused before any reviewer session. The answers packet checks the source file for secrets, stays inside the packet budget and refuses `packet-too-large` instead of going over it. `block reopen` refuses a bad `tmp.root` and names the cause of a failed write.
- **A last-chance patch that fixes something is progress (B73).** When the L3 patch closes at least one old finding and the file is under the round cap, the fix loop goes on at L2 instead of stopping `l3_patch_exhausted`. A stopped answer from `review-file` and the gate's `review_cap` detail now list the exits: `block reopen`, `block waive`, `review-file --answers`, re-decompose.
- **A steadier test suite (B81).** The ledger rotation race test forces the race order through a test hook, so it no longer fails at random under load.
- **Deleted files get a useful review (B74).** When a block deletes an owned file, the review packet says the file is deleted and lists up to 20 files that still name it, so the reviewer can look for dangling references. A deletion still needs an approval.
- **`review-file --fresh` (B75).** Runs a file's review again only when the cached result never counted (`stale`, `unavailable`, or a non-verdict refusal such as `other-run`). A counted result is refused `fresh-needs-reopen` and names `block reopen` and `block waive`; a tampered one is refused `fresh-tampered`. A coder may use it.
- **Another steadier test (B82).** The worker restart test waits for killed sessions to exit instead of checking once, so it no longer fails at random under load.
- **The review exits, in one place (B76).** `skill/references/review.md` §5, `code.md` and `docs/how-it-works.md` name the ways out of a stopped file in order: fix and the human's `block reopen`, `review-file --answers`, `block waive`, re-decompose. They also say `block attempt` never resets review state and that a lost state file is rebuilt from the ledger.
- **`review-file --fresh` fails closed (B83).** When a cached result cannot be put back, the ticket gets a refused `fresh-restore-failed` marker (or keeps its claim), so the worker does not run it by itself. `block reopen` clears it. Messages name the error code only.

## What's new in 0.8

In 0.8.0:

- **Run only the tests a block touches.** `block open --tests <paths>` names the block's test
  files. A gate argv item exactly `{test_files}` becomes those files. When the block owns more
  files than `gates.full_suite_threshold_files`, or no test file is known, the full suite runs.
  The ledger row says which one ran. A gate can also be `{argv, env}`. Env values are never
  printed. Gate env is not a secret store.
- **Laravel works under export, checked live.** A Laravel app on PHPUnit (no Pest) is now
  detected. Export copies the Composer `vendor` folder instead of linking it, so the tests load
  the block's code, not the main tree's. A live check (`CODE_FORGE_LIVE_PHP=1`) runs a Laravel
  app on PHPUnit and on Pest through the export.
- **One git worktree per block.** With `proof.isolation: worktree`, `block open` gives each block
  its own git worktree and branch (`proof.worktree.dir`, `branch`, `base`). Blocks in different
  trees may own the same files. Nothing is pushed. `block close --remove-worktree` removes the
  tree and keeps the branch. Gates, reviews, the close check and the mutation proof of that
  block all run on the block's tree, never on the main checkout.
- **A context pack for coders.** Set `context: {rules, lessons, commands, max_lines}`.
  `code-forge context pack --run <r> --block <id>` writes one file with the block's header,
  the build and test commands, the project rules and the lessons, cut to `max_lines`. It prints
  the path and a token estimate, and writes a signed `context.pack` row. Review packets now
  carry the same rules and lessons. With no `context` key, nothing changes.
- **Coders prove a green test run.** A coder prints `TESTS <command> exit=<n>` just before its
  COMPLETE sentinel. With `context.require_coder_tests: true` (fixed at `run start`),
  `block close --report` refuses a report with no such line or a non-zero exit, even when
  reviews are waived, and writes a signed `coder.tests` row when it passes.
- **Short status.** `code-forge status [--run <id>] [--json]` prints at most 10 lines per run:
  state, mode, spend, and one line per open block with its level, model, last gate and open
  findings. It is read-only. Every `block` verb takes `--json` and prints one JSON line, so an
  orchestrator never has to read logs or poll transcripts.
- **A signal when a block ends.** Every block end writes `~/.code-forge/runs/<run>/blocks/<id>.done.json`
  (status, commit, spend, open findings). With `hooks.on_block_end` (fixed at `run start`),
  code-forge also runs your command once per block end, with the block id, status and file path
  in its env, under a timeout, and writes a signed `hook.ran` row. A failing hook only warns.
- **Breaking: coders no longer push or open draft PRs by default.** `autonomy.coder_may_push` and
  `autonomy.coder_may_open_draft_pr` now default to `false`. `init` asks both (default no) and
  writes your answers. To keep the old behaviour, set both to `true` in `.code-forge.yml`.
- **Outside review rounds next to code-forge cost.** `code-forge ledger outside-review --run <r>
  --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]…` records how many rounds an
  outside reviewer needed on a PR (a later call for the same PR replaces it). `report` gets a
  14th section, `outside_review`: per PR, the rounds and findings next to the code-forge cost
  of its blocks.
- **A walk-through for brief-only runs.** [docs/brief-mode.md](docs/brief-mode.md) walks one task
  from a brief to a closed block with an outside reviewer: related tests, a worktree, the context
  pack, the done file and hook, `status`, and the outside review rounds in `report`. A test runs
  the page's own config and steps, so the page and the CLI stay the same.

## What's new in 0.7

In 0.7.7:

- **Work from a brief, with no plan.** `run start --mode brief` marks the run as brief-only.
  Then `block open` without `--level` picks the lane from the rules: the owned files,
  `--lines` and `proof.tiers.high.paths`. A signed `decision` row records it with
  `source: rules`. `--level` still wins, and `report` shows the mode of each run.

In 0.7.6:

- **An outside reviewer can replace the local review.** Set `review.external: {required: true,
  name: <reviewer>}` in `.code-forge.yml`. `block close` then skips the per-file review check
  and writes a signed `gate.reviews_waived` row with `by: config`. It needs no TTY and works
  under autopilot. All other close checks still run. The key is fixed at `run start`, and a
  coder can never own `.code-forge.yml`.

In 0.7.5:

- **An owned directory owns the files inside it, everywhere.** A block that owns `src/feature`
  now owns `src/feature/a.swift` for `block open`, `block claim`, `block rebase`, the scope gate,
  proof and the review worker, not only for `block close`. `block open` refuses a second block
  that owns a file or folder inside it (or a folder around it), so two coders can no longer edit
  the same files at once. A path that is already a file when the block opens owns only itself.

In 0.7.4:

- **Code moved between files is no longer read as deleted.** When a block moves code from one
  file to another, each file's review packet now says where the lines went (or came from), so the
  reviewer of the old file stops reporting the removal as a lost feature, also on rechecks. Every
  hunk is still reviewed.

In 0.7.3:

- **A model that keeps failing the answer format gets a stand-in.** When a reviewer's answer
  fails the schema twice on the same packet and you set `review.second_levels` for that level, the
  worker retries that packet once with the second model, and the ledger records it. Each failed
  answer's shape (its keys, types and schema errors, never its text) is logged, so you can see
  what went wrong.

In 0.7.2:

- **Long documents get reviewed.** The packet budget (`review.budgets.full_in`) is now 32,000
  tokens, and a Markdown file still too large is reviewed section by section instead of refused;
  a `split_required` result now says its size and the budget.

In 0.7.1:

- **Short clean reviews count.** A clean pass (no findings, every hunk acknowledged, a summary)
  needs only 40 output tokens on any diff size, so a correct short "no defect" review is no
  longer refused as `too_short`.

In 0.7.0:

- **The close gate now checks every file.** `block close` could close a block with critical
  findings still open: files inside an owned folder were skipped, and so was work committed
  before `block open` or done in another worktree. Now every reviewed file is checked, a newer
  failed review beats an older approval, a critical needs your waiver, a change outside the
  block's files is refused, and `--no-require-reviews` asks you at a terminal.

## What's new in 0.6

In 0.6.1:

- **Never relax a rule alone.** A new skill rule (R17): the orchestrator and the delegate never
  change, relax or remove a limit, threshold, budget, gate or review requirement. When a limit
  blocks work they stop, show the data, propose options and wait for you.
- **Fixes from using code-forge on itself.** A verb run from a subfolder now uses the project's
  ledger and config. `plan check` reads the list form the template shows. `facts` checks a flag
  with `grep -rn -- <flag> <source folder>` instead of `--help`, so the check runs in the
  delegate's sandbox.

In 0.6.0:

- **Autopilot.** Step away for up to 24 hours. `code-forge autopilot start` grants a delegate (a
  fresh closed-book L2 or L3 session, never Codex) a few of your decisions: waive a warning or a
  nit, one extra fix round past the review cap, the coder level. The orchestrator asks the
  delegate first (`autopilot ask`) and acts only on an answer it acted on. A fixed deny list in
  the code keeps the rest yours: critical and proof waivers, skipping reviews, plan and design
  approval, any limit or rule change, budget raises, merges, destructive actions. Every answer is
  a signed ledger row; each waiver opens a GitHub issue. See [docs/autopilot.md](docs/autopilot.md).
- **Budget per category.** `--budget review=<usd>,coding=<usd>` pauses a category at `--stop-at`
  (default 0.9) of its cap until you return. `autopilot approve` (only you, at a terminal) changes
  one setting until a time and puts it back by itself. While a grant is active, `run reload`
  refuses any limit key.
- **A live log for your return.** In Claude Code the orchestrator keeps one Claude Docs page per
  run with two tabs: Binnacle (status at a glance, decisions, blocks, open questions, actions only
  you can take, incidents, timeline) and Full log (every event, newest first). `autopilot status`
  and `autopilot stop` print its link; without the connector they print two Markdown files in the
  run dir (`autopilot binnacle`, `autopilot log`).
- **From 0.5.x, not listed before:** two files of one block can no longer both take the block's one
  L3 patch rung (0.5.0), and a maintainer can install a tagged release before it is on npm
  ([getting-started](docs/getting-started.md#keeping-it-up-to-date), 0.5.1).

## What's new in 0.5

- **Parallel review.** The worker reviews `review.parallel_tickets` files at once (default 3), with
  a session limit per provider (`review.provider_concurrency`), backoff on rate limits, and
  `budget.usd` still holding. `code-forge review` now waits on all files under one `--max`
  deadline and prints each result as it finishes. `code-forge doctor` shows the numbers;
  `npm run bench:review` measured 6 reviews in 12 s at 1 and 4 s at 3.
- **`code-forge facts` verifies again.** It dropped every answer in 0.4.0; now answers match, and a check
  that breaks the read-only rules marks only its own claim as not counted, with the reason.
- **Clearer budget refusals.** A session refused because other running sessions hold the budget says
  how much they hold and what this session would cost.
- **Plan approval in plan mode.** The skill shows a checked plan to you in the harness's plan mode
  (Claude Code: EnterPlanMode → ExitPlanMode) before any block starts.

## What's new in 0.4

- **Cost control.** Set `budget.usd` per run: one warning at 80%, no new session at 100%. Every
  session row carries an estimated `usd`; `report` and `run status` show the spend, and
  `ledger add coder --usd` records spend code-forge did not see.
- **Cheaper, faster fixes.** One review round that leaves 2 or more warnings climbs the coder one
  level (`escalation.*`). Docs and contract blocks code at L1 or higher and skip multimodel review;
  a file with risk 0–1 gets one reviewer (`review.single_reviewer_max_risk`).
- **Change the config mid-run.** `code-forge run reload --run <id>` re-reads `.code-forge.yml`
  without stopping open blocks.
- **Stricter plans.** `plan check` refuses a block whose level is not the lane recorded with
  `jev ask lane --block <id>` (or `--rules` without Jev), and prints the lane of every block.
  `block close --report <file>` fails a coder report that does not name a review ticket per file.
- **Codex is refused for closed-book roles** (reviewer, judge, System 2, plan author): it always
  has a shell. See [Security model](#security-model).
- **Steadier reviews.** A timed-out review session is retried once; `review.session_timeout_s`
  defaults to 300; a live worker with a fresh heartbeat is never reported down.
- **Better error reports.** `logs report --with-doctor` adds a cleaned setup check; errors list the
  last 5 command names; recovered problems are logged as warnings; an error that a newer version
  fixes tells you to upgrade instead of filing it.
- **Earlier checks.** `validate` refuses an effort the level's provider cannot take (for example
  `xhigh` on an openai level), so nothing fails when the session starts.

Fixed in 0.3.1 and 0.3.2: Claude reviewer sessions no longer fail at once on the `--json-schema`
flag; Grok answers are read from Grok's own answer envelope; Jev `score` questions (such as `risk`)
no longer fall back to rules; and a clean review of a tiny diff (20 added lines or fewer) needs only
40 output tokens, so it is no longer refused as `too_short`.

## What ships

- A CLI (`code-forge`), installed globally with `npm install -g @codedology/code-forge`; `code-forge
  init` then links the skill into every detected harness (Claude Code, Codex, Grok, …).
- One Agent Skill (`skill/SKILL.md` + `references/`) that is prose only — every fact (model ids,
  efforts, commands, thresholds, paths) lives in config or in the CLI, never in the skill text.
- A JSON Schema for `.code-forge.yml` (`schema/code-forge.schema.json`), documented in
  `docs/reference/config.md` (generated — see below).

See the skill's `skill/references/` for the full architecture, decision layer, review engine, proof
policy and security model; `docs/reference/blocks.json` lists the build blocks.

## Install

```bash
npm install -g @codedology/code-forge
code-forge init
```

The scoped name is used only to install; the command is `code-forge`. The unscoped npm package
`code-forge` is a different, unrelated tool.

The wizard asks a few short questions (tools, harnesses, provider matrix, multimodel, keys,
engine), every one with a default on Enter. Gates and proof settings come from the project:
`init` prints what it found, marks what it left blank, and lets you use, customize or leave them
for later. Non-interactively:

```bash
code-forge init --no-interaction --no-jev
```

`--no-jev` skips the System-1 key for a first try; drop it and pass `--jev-ref <1Password item ID>`
(or the item link, or `op://vault/item/field`; code-forge finds the vault and the key field) or
`--jev-env MY_JEV_KEY` once you have one. A second run of `init` changes nothing it already wrote.

To upgrade: `npm install -g @codedology/code-forge@latest`, then `code-forge upgrade` (re-copies
skill installs made with `--copy`). See [docs/getting-started.md](docs/getting-started.md#keeping-it-up-to-date).

## Quickstart

Once `init` has written `.code-forge.yml`, these all run for real:

```bash
code-forge validate                 # loads and validates .code-forge.yml
code-forge resolve L1                # {provider, model, effort, fallback, cli} for a level
code-forge doctor --quick            # config + links + key resolution only
code-forge list                      # what is installed where, and whether it still resolves
code-forge keys list                 # NAME / SOURCE / BACKEND / EXPIRES — never a value; WARN under 1 h left
code-forge models                    # the model catalog per provider
code-forge --help                    # every verb, one per line
code-forge version
```

## Review only

To review code you already wrote — no plan, no coder, no proof — run this in the repository:

```bash
code-forge review                    # verdict + fix list per changed file; exit 0 only if all approved
```

It reviews your branch against the merge base with the default branch (on the default branch: your
uncommitted work), with the same fresh, closed-book reviewer sessions a block gets. Narrow it with
`--files <path…>`, state the change with `--intent "<text>"`, pick the base with `--base <ref>`, or
get one JSON document with `--json`. Exit codes: `0` all approved or nothing to review, `1` any
finding, stop or unavailable review, `2` usage. See [docs/review-only.md](docs/review-only.md).

## The verbs

Verbs are discovered from `src/cli/*.mjs` — adding one never touches the router
(`bin/code-forge.mjs`), so this list is exactly `code-forge --help`'s output plus each verb's own
usage line.

| Verb | Usage |
|---|---|
| `init` | `code-forge init [--no-interaction] [--tools recommended\|current] [--yes-tool <tool>]… [--harness a,b] [-g\|-p] [--copy] [--provider P] [--level Ln=model[:effort][@provider]]… [--refresh-models] [--multimodel on\|off] [--second-provider P] [--jev-ref <item-id\|link\|op://…> \| --jev-env NAME \| --no-jev] [--engine auto\|solo\|harness] [--solo-project N] [--gate name=cmd]… [--proof isolation=export\|lock \| high=a,b \| link_dirs=a,b \| copy_untracked=a,b]… [--coder-may-push yes\|no] [--coder-may-open-draft-pr yes\|no] [--skip-doctor]` |
| `doctor` | `code-forge doctor [--quick] [--json] [--cwd <dir>]` |
| `tools` | `code-forge tools [--json]` · `code-forge tools install [<id>…] [--yes] [--dry-run]` — see and install the recommended tools (claude, codex, gemini, grok, op, solo) |
| `validate` | `code-forge validate [--file <path>]` |
| `resolve` | `code-forge resolve <L0\|L1\|L2\|L3>` |
| `run` | `code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>]` · `run start --reattach --run <id> [--worker-pid <pid>]` · `run status --run <id>` (with spent and budget USD) · `run reload --run <id>` (re-read `.code-forge.yml` mid-run) · `run end --run <id>` |
| `block` | `code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code\|docs\|contract]` · `block attempt\|rebase <id> --run <r>` · `block close <id> --run <r> [--transcript <file>] [--report <file>] [--no-require-reviews]` · `block claim <id> <path> --run <r>` · `block stop <id> --run <r> --reason <text>` · `block waive <id> <finding-id> --run <r> --file <path> --reason <text>` · `block reopen <id> --run <r> --file <path> --reason <text>` (both human-only: you run them in your own terminal, they ask for a yes, and there is no `--yes`) |
| `worker` | `code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]` — started detached by `run start`, never by a coder |
| `review` | `code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> \| --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]` — review only: no coder, no proof, never closes a block |
| `review-file` | `code-forge review-file <path> --block <id> [--run <id>]` (enqueue) · `review-file --wait <ticket> [--max <seconds>s]` (poll, default 90s; still running at `--max` prints `status: pending, reason: wait_timeout`) |
| `spawn` | `code-forge spawn --level L0\|L1\|L2\|L3 --role <role> --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <seconds>] [--background]` |
| `s2` | `code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]` |
| `author` | `code-forge author --job plan\|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]` |
| `facts` | `code-forge facts --brief <file> [--sources <path>…] [--out <file>] [--run <id>] [--timeout <seconds>]` |
| `plan` | `code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]` — every block's level must be the lane recorded by `jev ask lane --block <id>`; prints `lanes: B1 L1 (rules) · …` |
| `proof` | `code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]` · `proof export <block> --run <r> [--remove]` · `proof lock\|unlock\|restore <block> --run <r>` · `proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert\|assertion-deletion]` (a non-`node:test` file runs through its `proof.runners` entry) |
| `gates` | `code-forge gates detect\|run\|secret-scan\|safe-edit\|scope\|acceptance\|transcript-grep --cwd <dir> …` |
| `jev` | `code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]` — `--rules` answers `lane`, `risk` or `security_sensitive` without Jev; writes to the project's ledger slug |
| `keys` | `code-forge keys list \| set <name> [--op <item-id\|link\|op://ref> [--cache-hours <1-24>]] \| test <name> [--ref <ref>] \| remove <name>` |
| `ledger` | `code-forge ledger tail --slug <slug> [--n\|-n <count>]` · `ledger calibration --slug <slug> [--question <id>]` · `ledger outcome --slug <slug> --pr <n> --ci red\|green\|reverted` · `ledger outcome --slug <slug> --scan-git [--cwd <dir>] [--days <n>]` · `ledger add coder --run <r> --block <b> --usd <n> [--note "..."]` |
| `report` | `code-forge report --slug <slug> [--json] [--export <dir>]` — 13 sections, then the spend per run (open blocks included) and the total |
| `models` | `code-forge models [--refresh --from-cli-caches]` |
| `list` | `code-forge list` |
| `remove` | `code-forge remove [<harness>] [--scope project\|global]` |
| `upgrade` | `code-forge upgrade [--source <path>]` |
| `logs` | `code-forge logs [--last N] [--json]` · `logs summary [--days N] [--json]` · `logs clear [--yes]` · `logs path` — the local error log (`~/.code-forge/logs/errors.jsonl`): errors, warnings, and `[fixed in X.Y.Z]` marks |
| `logs report` | `code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]` — share selected errors as a public GitHub issue; you see the full text first and say yes once. An error a newer version fixes is not filed unless `--force` |
| `autopilot` | `code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2\|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes]` · `autopilot status --run <id> [--json]` · `autopilot stop --run <id>` (both print the live log's link, or the two Markdown files) · `autopilot ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json]` · `autopilot waive\|round\|level … --decision <id>` · `autopilot approve --run <id> --key <dot.path> --value <json> --until <time>` (owner only, at a terminal) · `autopilot binnacle --run <id> [--json\|--markdown] [--link <https url>]` · `autopilot log --run <id> [--json\|--markdown]` — the owner's time-boxed grant to a delegate; see [docs/autopilot.md](docs/autopilot.md) |
| `help` / `version` | `code-forge --help` (or no verb) · `code-forge --version` |

`run`, `block`, `worker`, `spawn`, `s2`, `author`, `facts` and `review-file` are what the Agent
Skill's loop calls during a real block; you will not usually type them by hand, but they are plain
CLI verbs like any other and every one of them is exercised by this package's own test suite.

## Security model

- **`signer: same-user boundary only`.** Every review/gate-relevant ledger row is HMAC-signed with
  a per-project key, so tampering by a different user or a rewritten history is detectable.
  `doctor` always prints this line — the signature is not a defense against an attacker who
  already runs code as you, and no part of code-forge claims otherwise.
- **Keys never reach a coder process.** Provider and Jev keys resolve only inside the worker or the
  orchestrator's own CLI calls (`env:<NAME>` → OS keychain → 1Password, cached for
  `keys.cache_hours`: default 8 h, at most 24 h → ask at setup only). `keys list`/`keys test` never print a value; every stdout/stderr path is redacted.
- **A forbidden-command list** stops a coder from starting its own worker, waiving a review, or
  running other orchestrator-only actions — rendered into each provider's own deny-list mechanism
  (`--disallowedTools` for Claude, an execpolicy rules file plus prose for Codex).
- **Closed-book reviews.** Reviewers, judges, System 2 and the plan author see the packet and
  nothing else: Claude runs them with no tools, Grok with every tool denied. Codex (provider
  `openai`) has no mode without a shell, so code-forge refuses it for those roles: `validate`
  warns, `resolve` marks the level `closed_book: "refused"`, `doctor` fails its isolation row, and
  the session is never started. Codex coders are unchanged. `review.allow_open_book_codex: true`
  accepts the risk and keeps Codex in those roles, with a warning on every surface.
- **The error log stays on your machine.** A failed verb is logged to
  `~/.code-forge/logs/errors.jsonl` with flag names but never flag values, and with home folder,
  project, emails, `op://` references and 1Password item IDs replaced. `logs report` cleans the
  text twice (built-in rules, then an AI check), stops on anything that looks like a key, shows
  you the full issue and sends nothing without your yes. `CODE_FORGE_NO_ERROR_LOG=1` turns the log,
  the warnings and the command list off. Every field is listed in [docs/privacy.md](docs/privacy.md).
- **No mutation testing.** Neither this package's own tests nor the product it ships run mutation
  testing (Ricardo's ruling R14/Q16, 2026-09-25). Proof is plain unit and feature tests with exact
  assertions, plus a red→green runner that proves a new test fails before the fix and passes after (`node:test`
  built in; any other runner through a `proof.runners` entry that writes a JUnit report).

## Configuration reference

`schema/code-forge.schema.json` is the source of truth for `.code-forge.yml`.
[docs/reference/config.md](docs/reference/config.md) lists every key with its type and default.
Keys added in 0.4, 0.5 and 0.6 (`review.parallel_tickets` and `review.provider_concurrency` are
new in 0.5, `autopilot.min_confidence` in 0.6):

| Key | Default | What it does |
|---|---|---|
| `budget.usd` | none | per-run spend stop in USD: one warning at 80%, no new session at 100% |
| `escalation.after_rounds_with_warnings` | `1` | rounds with many warnings at one level before the coder climbs (`0` turns it off) |
| `escalation.warning_threshold` | `2` | how many open warnings make a round "heavy" (any critical counts too) |
| `levels.coder_floor_docs` | `L1` | the lowest level that codes a docs or contract block |
| `review.single_reviewer_max_risk` | `1` | at or below this risk a file gets one reviewer and no judge |
| `review.multimodel_for_docs` | `false` | let docs and contract blocks use multimodel review too |
| `review.allow_open_book_codex` | `false` | let Codex run reviewer, judge, System 2 and plan author anyway |
| `review.session_timeout_s` | `300` | a review session that runs longer is killed and retried once |
| `review.routes` | none | big files go to another model: a file with at least `min_lines` lines is reviewed by the rule's `model`, with its own `session_timeout_s` and `second` level |
| `review.parallel_tickets` | `3` | review tickets the worker runs at once (1 to 16); `1` reviews one file at a time |
| `review.provider_concurrency` | anthropic `4`, openai `2`, xai `2` | review sessions at once per provider |
| `autopilot.min_confidence` | `0.7` | the lowest delegate confidence that is acted on; below it the question goes to you |

`code-forge block open … --kind code|docs|contract` sets a block's kind when its file endings do
not say it. The reference page is generated:

```bash
node scripts/gen-config-doc.mjs          # regenerate docs/reference/config.md
node scripts/gen-config-doc.mjs --check  # exit 0 if it's already up to date, 1 otherwise
```

## Development

```bash
npm install
npm test              # node --import ./test/helpers/isolate.mjs --test 'test/**/*.test.mjs'
npm run typecheck     # tsc --checkJs --noEmit
```

Node >= 22 required (ruling R7). No build step — the package ships plain ESM `.mjs`, run directly
by Node. `docs/reference/blocks.json` maps each build block to the files it owns and what it
depends on, kept in sync with the maintainer's design plan.

Releasing: record changes with `npm run changelog -- <type> "<text>"` and fixed error reports with
`npm run known-fix -- add <fp> <version> "<summary>"`, then `npm run release -- <patch|minor|major|x.y.z> [--dry-run]`
bumps, moves the README's "In the next release:" bullets under the new version, checks the config
doc, commits and tags (it never pushes or publishes). Publish with `npm publish` from your
machine, then push the tag: CI only creates the GitHub release. See [docs/releasing.md](docs/releasing.md).
