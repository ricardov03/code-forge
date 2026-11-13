# The whole tool, step by step

Start here to take a project from install to release. Use Node.js 22 or newer and Git.
Run commands at the root of your Git repository (the folder Git tracks).
The CLI is the command-line tool. A **harness** is the app that runs your coding agent.
An **orchestrator** is the agent that assigns work and checks the results.
See [concepts.md](concepts.md) for these words and the other terms below.

You are the owner. You approve plans and limits. The orchestrator runs the work commands.
The coder edits only its assigned files. It does not start workers, grant rights, or waive findings.

The example uses `guide-demo` as the project **slug** (a short name), `demo-1` as the
**run** (one work session), and `B1` as the **block** (one small unit of work).
Replace them with your own names. Replace text in `<angle brackets>` before you run a command.
Do not type the brackets. Commands in different choices are alternatives.

**About the output:** setup excerpts below came from a tiny local Node project.
Model and live-worker commands were not run for this page. Their excerpts show the CLI's
output form, with variable values in brackets. Each excerpt says **Output shortened**.

## 1. Install

**What it does**

Install the CLI and its agent skill (the instructions your orchestrator follows).
The `code-forge` command starts a fast Go program first; commands not yet in Go go to Node by themselves ([native engine](native-engine.md)).

**Run**

Install once for your user:

```sh
npm install -g @codedology/code-forge
code-forge setup
code-forge version
```

npm runs no install script. `code-forge setup` checks the Go binary for your
system and prints `OK`, or the problem and the one command that fixes it.

Or pin one version in a project that has a `package.json`:

```sh
npm install --save-dev --save-exact @codedology/code-forge@0.23.0
npx code-forge version
```

After the local install, put `npx` before each command below. Use the scoped package name
when you install. The unscoped npm package is a different tool.

**You see** — Output shortened (local CLI check):

```text
@codedology/code-forge 0.23.0
```

**What changed**

The global install adds a command to your command search path, `PATH`.
The local install writes `package.json`, the lockfile, and `node_modules/`.

**If it fails**

If the shell says `command not found`, open a new terminal and check npm's bin path.
Use the local install if the global package folder is not writable.

**Next:** [Set up the project](#2-set-up-the-project).

## 2. Set up the project

**What it does**

`init` asks about tools, harness links, and review choices.
A provider is a service that runs an AI model; `init` asks which provider and models to use.
It asks about the engine (how agents start), coder push and draft-PR rights, checks, and the Jev key.
Jev is an optional small model that picks work levels; rules work without it.

**Run**

```sh
code-forge init
```

Read each hint. Press Enter to keep its default. For a script, this uses defaults and skips Jev:

```sh
code-forge init --no-interaction --no-jev
```

**You see** — Output shortened (local non-interactive check; fields trimmed):

```text
"ok":true
"doctor":{"ok":true,"counts":{"OK":2,"WARN":1,"FAIL":0,"INFO":0}}
```

At a terminal you see questions and a settings summary. An agent or a pipe gets one JSON line.
JSON is a text format for named values.

**What changed**

`init` writes `.code-forge.yml`. It detects test, lint, type, and format commands.
It links the skill into your chosen harnesses and records the links under `~/.code-forge/`.
It may write shared defaults in `~/.code-forge/config.yml`.
It ends with the quick setup check. A rerun keeps settings you already set.
Add `.code-forge/` to your project's `.gitignore` so local work state stays out of commits.

**If it fails**

Without a terminal, a missing Jev answer causes exit 2. Use `--no-jev` or supply its key source.
If no engine can start agents, use a harness with agent support or install Solo.
See [the setup questions](getting-started.md#what-init-asks).

**Next:** [Check the tools and setup](#3-check-the-tools-and-setup).

## 3. Check the tools and setup

**What it does**

`tools` lists helper apps and how to install missing ones.
`doctor --quick` checks local setup. Full `doctor` also makes small live provider calls.

**Run**

```sh
code-forge tools
code-forge tools install
code-forge validate
code-forge doctor --quick
code-forge doctor
```

Use the install command only for missing tools. It shows a plan and asks before it installs.

**You see** — Output shortened (local tools and quick check):

```text
gemini  missing    install: npm install -g @google/gemini-cli
OK   config: valid
OK   links: 1 link(s) resolve; skill listing UNVERIFIED
WARN keys: jev key not resolved; System 1 falls back to rules
doctor: 2 ok, 1 warn, 0 fail
```

`OK` means the check passed. `WARN` means read the detail. `FAIL` needs a fix.
`INFO` gives facts and does not fail the check.

| Check | What it means |
|---|---|
| Config and links | The settings load, and the harness links point to files that exist. |
| Keys and Jev | A key can be read; the full check also tests a Jev request. |
| Commands, engine, and Solo | Needed programs are on `PATH`; the chosen engine can run. |
| Provider probes and isolation | Each provider CLI works; a reviewer cannot read project files. |
| Gates | The configured check programs exist. A gate is a check that work must pass. |
| Ledger, locks, and temp | The event log and work folders are usable; stale locks are reported. |
| Review state and workers | Saved review state can be used; a test worker can start and stop. |
| Signer | Signed rows detect changed data. They protect only within the same user account. |
| Native binary and daemon | Go is available; its background helper is live, off, or using a fallback. |
| Reviewer model rows | Past speed, successful calls, and waived findings help explain the model pick. |

**What changed**

Tool installs change your installed apps. The checks report results and may log warnings.
Full `doctor` creates a private probe folder and removes it when done.

**If it fails**

A missing provider CLI must be installed and signed in. A failed isolation check needs a
provider that can run without tools. Use an Anthropic or xAI model for L2 and L3.
Codex has a shell and is refused for these roles by default.
See [setup checks](getting-started.md#check-the-setup).

**Next:** [Set keys, models, and a budget](#4-set-keys-models-and-a-budget).

## 4. Set keys, models, and a budget

**What it does**

A **provider** runs a model; a **level** names the role's work capacity, from L0 to L3.
The top-level `provider` and `levels` in `.code-forge.yml` pick the model and effort.
A provider on one level overrides the top-level choice; `fallback` lists backup choices.

**Run**

```sh
code-forge keys list
code-forge models
code-forge resolve L1
```

Sign in to each provider CLI you use. To add the optional Jev key, paste it at the hidden prompt:

```sh
code-forge keys set jev
code-forge keys test jev
```

The test checks that the key can be read. Full `doctor` tests the service.
Skip both key commands if you use rules without Jev.
Before you start a run, set a cap and the worktree workflow used below in `.code-forge.yml`.
A **worktree** is another checkout with its own branch. These keys give each block its own tree,
name the Node test command, and require test results in the coder's final report:

```yaml
budget:
  usd: 20
proof:
  isolation: worktree
gates:
  test: [node, --test, "{test_files}"]
context:
  require_coder_tests: true
```

Merge these keys into the existing sections. Keep other detected checks.
The default proof isolation is `export`; choose `worktree` explicitly for this guide.
Run `code-forge validate`, then commit the setup before you start the run.

**You see** — Output shortened (local list and model lookup):

```text
NAME    SOURCE    BACKEND    EXPIRES
{"provider":"anthropic","model":"claude-sonnet-5","effort":null,"fallback":[],"cli":"claude"}
```

**What changed**

The list and lookup commands do not change model settings. Setting a key writes the local key store.
The budget edit changes `.code-forge.yml`. Each model session adds an estimated cost to the
**ledger**, the event log at `~/.code-forge/ledger/<slug>.jsonl`.
A warning comes at 80% of the cap. At 100%, new sessions stop.
Unknown model prices stay unknown and are not counted in the sum.

**If it fails**

If a key cannot be read, set it again or unlock its key store. Do not put keys in Git.
At the budget cap, the owner decides whether to stop or raise `budget.usd`.
After an allowed mid-run edit, use `code-forge run reload --run demo-1`.
An active grant has separate rules for limit changes; see step 15.

**Next:** [Choose a way to work](#5-choose-a-way-to-work).

## 5. Choose a way to work

**What it does**

Pick the flow that fits your task. Brief mode skips the plan and its approval.
Review only checks code you already wrote.

| Way | Use it for | First work command | Continue at |
|---|---|---|---|
| Full plan run | Several blocks with shared choices or an order to approve | `code-forge facts --brief plans/task.brief.md --out plans/task.facts.md` | Step 6 |
| [Brief mode](brief-mode.md) | One small task with clear clauses | `code-forge run start --mode brief --run demo-1` | Step 8 after the start |
| [Review only](review-only.md) | Existing changes | `code-forge review --base main --intent "Check the total calculation"` | Step 10, then your normal project release |

**Run**

If you need the command list before you choose:

```sh
code-forge --help
```

Then run the first command for your chosen row.

**You see** — Output shortened (CLI help):

```text
Usage: code-forge <verb> [args]
```

The flow commands produce a facts sheet, a run with `mode brief`, or a review summary.

**What changed**

Help changes no project files. A run's mode is fixed when it starts.
Review only creates a temporary run and worker, then cleans them up by default.

**If it fails**

You cannot change a run's mode with reload. Finish or stop its blocks and end that run.
Start a new run with the right mode.

**Next:** [Make and approve a plan](#6-make-and-approve-a-plan), or follow your table row.

## 6. Make and approve a plan

**What it does**

An L0 agent checks the brief's claims and writes a facts sheet.
An L3 author makes a plan; **hardening** means asking questions until its choices are clear.
The owner approves the checked plan before any coder starts.

**Run**

Write `plans/task.brief.md` with your goal, limits, and expected results. Then run:

```sh
code-forge facts --brief plans/task.brief.md --out plans/task.facts.md
code-forge author --job plan --brief plans/task.brief.md --facts plans/task.facts.md --out plans/task.plan.md
code-forge author --job harden --brief plans/task.brief.md --facts plans/task.facts.md --draft plans/task.plan.md --answers plans/answers.json --out plans/task.plan.md
```

Save the owner's answers in `plans/answers.json`. Use a list of question IDs and answers,
such as `[{"id":"Q1","answer":"Keep the current API"}]`.
Repeat the harden command until `questions` is empty.
For each block, record its **lane** (the chosen coder level). Without Jev, save these measured
facts in `plans/B1.state.json`; replace the example counts and flags with your block's facts:

```json
{"filesChanged":2,"linesAdded":20,"touchesMigration":false,"touchesPolicyOrMiddleware":false,"pathFloorHit":false,"keywordsFound":[]}
```

```sh
code-forge jev ask lane --block B1 --plan plans/task.plan.md --state plans/B1.state.json --rules
code-forge plan check plans/task.plan.md --facts plans/task.facts.md
```

Make the plan's level match the returned lane. Show the final plan to the owner in the harness's
plan mode. If there is no plan mode, show it and wait for an explicit yes.
Plan approval is a human step; there is no plan-approve CLI command.

**You see** — Output shortened (source form; model commands not run):

```text
{"draft":"<plan path>","questions":[],"cost":<cost>}
plan check: ok (<count> blocks)
lanes: B1 <level> (rules)
```

**What changed**

The commands write the facts sheet and plan. Lane choices and session costs add ledger rows.
The owner supplies the answers file. The approval lets the orchestrator dispatch blocks.

**If it fails**

A missing or stale facts sheet stops the author. Rebuild it after a brief edit.
Fix each failed plan-check rule. Do not treat `lanes: B1 none` as an approved lane.
See [the full workflow](how-it-works.md) and the [plan template](../skill/templates/plan.md).

**Next:** [Start a run and its worker](#7-start-a-run-and-its-worker).

## 7. Start a run and its worker

**What it does**

`run start` saves the settings and starts a **worker**, the background process that serves reviews.
The worker watches its queue, checks files, and starts fresh reviewer sessions.
The orchestrator runs these commands.

**Run**

```sh
code-forge run start --run demo-1
```

For brief mode, use the start command in step 5 instead.
After a worker stops, start its replacement in another terminal; leave that terminal open:

```sh
code-forge worker --run demo-1
```

When the pinned worker is dead, the new worker re-pins the run to itself and prints `worker: re-pinned run <id> from dead pid <old> to <new>`; a pinned worker that is still alive is never replaced.
Then attach the run to that live worker from your first terminal:

```sh
code-forge run start --reattach --run demo-1
```

**You see** — Output shortened (source form; live worker not run):

```text
run demo-1 started · engine <engine> · worker pid <pid>
worker: pid <pid> serving run demo-1 · jev key absent (fallback)
run demo-1: worker re-pinned to pid <pid>
```

**What changed**

The run record is `~/.code-forge/runs/demo-1.json`. Its signing key is beside it.
`.code-forge/runs/demo-1.json` is a project mirror.
The worker uses the project's review queue. Run events go in the project ledger.

**If it fails**

`already serves this queue` means a worker is still live. Do not start another by mistake.
If the worker was replaced, reattach before you try to close a block.

**Next:** [Open a block](#8-open-a-block).

## 8. Open a block

**What it does**

A block names its **owned files**, the files its coder may edit.
An **acceptance clause** states a result that a named test must check.
The level selects coder capacity; the kind selects the block's checks.

**Run**

Save this list in `plans/B1.acceptance.json`:

```json
[{"clause":"total adds each item price","tests":["test/total.test.mjs"]}]
```

Use the level your plan recorded. This example assumes it returned L0:

```sh
code-forge block open B1 --run demo-1 --level L0 --kind code --owned src/total.mjs test/total.test.mjs --tests test/total.test.mjs --acceptance plans/B1.acceptance.json --brief plans/task.brief.md --lines 20
```

In brief mode, leave out `--level` to let rules choose it.
Kinds are `code`, `docs`, `contract`, and `light`. A contract sets shared interfaces and has an L2 floor.
A light block has tight size limits. See [block kinds](concepts.md).
Step 4 sets worktree isolation before the run starts.

**You see** — Output shortened (source form; needs a run):

```text
block B1 open · L0 · code · attempt 1 · base <sha> · 1 test file
worktree <path> · branch forge/demo-1/B1
BRIEF plans/task.brief.md lines=<count> sha=<hash> <<<EOM>>>
```

**What changed**

The run stores ownership, clauses, level, kind, and base commit (the starting Git snapshot).
It saves the brief and creates the worktree and branch when configured.
The default worktree folder is beside the repo; on macOS its name ends in `.noindex`.

**If it fails**

An ownership overlap means another open block owns that path. Wait or split the files.
An acceptance file needs a list of `{clause, tests}` with a nonempty test list per clause.

**Next:** [Pack the context and code](#9-pack-the-context-and-code).

## 9. Pack the context and code

**What it does**

A **context pack** is the short set of instructions sent with the brief to the coder.
It holds ownership, clauses, proof rules, and any project rules you configured.
The coder builds and tests in the block's worktree before it commits.

**Run**

```sh
code-forge context pack --run demo-1 --block B1 --path-only
```

Send the pack's text and the `BRIEF` pointer to the coder through your harness.
The pack gives the exact CLI path to use. The coder changes to the printed worktree path,
edits the owned files, and checks the result. For this Node example:

```sh
cd <worktree-path>
node --test test/total.test.mjs
git add src/total.mjs test/total.test.mjs
git commit -m "Add total calculation"
```

Run the project's build command too if it has one. For each new or changed test, prove
**red→green**: an assertion fails on old code, then passes on new code. A crash is not a valid red.
The proof command checks this without hand edits to old code:

```sh
code-forge proof red-green B1 --run demo-1 --test test/total.test.mjs
```

**You see** — Output shortened (source form; needs a run):

```text
<temp root>/demo-1/context/B1.md
tokens ~<count>
```

The coder ends its report with its real test result and the completion line:

```text
TESTS node --test test/total.test.mjs exit=0
===BLOCK B1 COMPLETE===
```

**What changed**

The pack is saved under `tmp.root`, or the system temp folder's `code-forge/` directory.
Its hash (a content fingerprint) goes in the ledger. Code and tests change on the block branch.
Proof writes a signed result and uses an isolated export for measurement.
Save the coder's report as `.code-forge/B1-report.txt` and its transcript (tool activity log)
as `.code-forge/B1-transcript.log` in the main checkout. Use the harness's real transcript.

**If it fails**

Fix failing tests before commit. `RED_INVALID` means the old-code failure did not prove the claim.
Use a test that reaches a real assertion. Rebuild the context pack before each fix task.
The setting from step 4 makes close enforce the report's test line.

**Next:** [Review the changes](#10-review-the-changes).

## 10. Review the changes

**What it does**

`review-file` queues one block file and returns a **ticket**, its review ID.
`review` checks existing changes without a coding block.
Reviewers start fresh; **closed book** means they see only the supplied review packet and have no tools.

**Run**

For a block, queue each changed file and wait on each returned ticket:

```sh
code-forge review-file src/total.mjs --block B1 --run demo-1
code-forge review-file test/total.test.mjs --block B1 --run demo-1
code-forge review-file --wait <ticket>
code-forge review-file --wait-block B1 --run demo-1
```

The normal file loop waits per file. With `review.file_loop: watch`, commit each green file;
the worker queues it from the commit. With `overlap`, coding can continue while reviews run.
Join all tickets before the final report.
For review only, run this instead:

```sh
code-forge review --base main --intent "Check the total calculation"
```

**You see** — Output shortened (source form; model and worker commands not run):

```text
"status":"queued"
"ticket":"<ticket>"
```

Waiting returns the verdict and any **findings** (problems the reviewer found).
`pending` means wait again. A completed ticket can still contain findings.
Review-only exit codes are 0 for all approved or no changes, 1 for findings, stopped or unavailable
reviews, timeouts, or a failed run start, and 2 for setup or usage errors.

The reviewer pick uses configured models and backups. With enough past data, it weighs speed,
successful calls, and the share of findings later waived. These are useful signs of accuracy,
not a proof of it. With little data it keeps configured order; some tickets try other candidates.
`doctor` shows the model statistics. A route with `pin: true` keeps its chosen model.

**What changed**

Tickets go under `.code-forge/queue/`; results go under `.code-forge/reviews/demo-1/`.
Saved review state goes under `~/.code-forge/runs/demo-1/review-state/`.
The ledger records sessions, findings, and signed approvals for the reviewed content.
Review only ends its temporary run and stops its worker by default. It does not edit your code.

**If it fails**

An `unavailable` result is not approval. Check provider login, budget, and L2/L3 settings.
For `worker_down`, restart and reattach as in step 7. For findings, follow step 12.

**Next:** [Run the gates](#11-run-the-gates).

## 11. Run the gates

**What it does**

Gates run your test, lint, type, format, and configured extra checks.
For a worktree block they run there, using the main checkout's settings.
`--changed` narrows eligible checks to edits since the last green gate run.

**Run**

```sh
code-forge gates detect --cwd .
code-forge gates run --run demo-1 --block B1
```

For a fix round:

```sh
code-forge gates run --run demo-1 --block B1 --changed
```

Run the command without `--changed` again before close. To run only named block tests,
set `gates.test: [node, --test, "{test_files}"]` in the config. Without that whole argument,
the configured test command runs as written. `{changed_files}` can narrow lint and format checks.
The **baseline** is the saved file content from the last passing checks.
Missing test pairs, too many files, or no baseline usually make changed checks use broader checks.

**You see** — Output shortened (local gates check; fields trimmed):

```text
"stack":"node"
"gate":"test"
"ok":true
"allOk":true
```

**What changed**

A block gate run records results and, when possible, owned-file hashes in the ledger.
The last green hashes form the baseline for `--changed`. Check commands may create build files.

**If it fails**

Read the failed gate's captured output. Fix the cause and rerun it.
Do not read `allOk: true` alone as proof that tests ran: a null gate is skipped.
`--changed` needs both `--run` and `--block`.

**Next:** [Handle findings](#12-handle-findings).

## 12. Handle findings

**What it does**

A fix round sends all current test failures and findings to the coder in one task.
A **waiver** records why a finding may stay unfixed.
Reopen starts a fresh review; unwaive reverses an agent waiver.

**Run**

Wait for all block tickets and tests first. Send one fix task with a fresh pack.
Then repeat steps 9–11 for the changed files. The owner can use these alternatives in their terminal:

```sh
code-forge block waive B1 F1 --run demo-1 --file src/total.mjs --reason "Explain why this finding may remain"
code-forge block reopen B1 --run demo-1 --file src/total.mjs --reason "Explain why a fresh review is needed"
code-forge block unwaive B1 F1 --run demo-1 --file src/total.mjs --reason "The agent waiver no longer applies"
```

Use the actual finding ID, including any section prefix. These are choices, not a command sequence.
Waive and reopen ask for the human's confirmation.

`--yes` on waive or reopen selects the orchestrator's evidence path. It does not grant blanket approval.
A citation names a file and line, such as `src/total.mjs:4`.
A warning or nit waiver needs a real line citation, test file, or clause number and a fresh judge's agreement.
A critical agent waiver also needs an active grant, a citation to the finding's file, and a passing
acceptance test measured at the current content. Proof findings cannot use this path.
An agent reopen needs evidence and a judge; it is limited to one per file per run.
An open review with findings needs line citations. Unwaive has no `--yes` path.
If `review.agent_waivers.require_ack` is set, the owner must also acknowledge counted agent waivers:

```sh
code-forge block ack-waivers B1 --run demo-1
```

**You see** — Output shortened (source form; actions not run):

```text
block B1 waived F1 in src/total.mjs (by: human)
block B1 reopened src/total.mjs (from <state>; by: human)
block B1 unwaived F1 in src/total.mjs (by: human)
```

**What changed**

Fixes change code and tests. Review decisions add signed ledger rows.
Reopen resets that file's review state and can return a new ticket. Earlier approvals do not cover new content.

**If it fails**

A refused evidence path changes nothing. Supply real evidence or ask the owner to decide at a terminal.
Do not add rounds or relax a rule just to force a pass.
See [review rules](../skill/references/review.md).

**Next:** [Close and land](#13-close-and-land).

## 13. Close and land

**What it does**

Close checks ownership, current reviews, proof, and the coder's report and transcript.
**Land** means bring the finished branch into the main checkout.
`block land --merge` joins reviews, runs changed gates, closes, runs the merge gate on a trial merge (the Go test selection, never the full suite), and merges.
When the merge gate fails, land names up to 10 failing tests (TAP `not ok` and the node spec `✖ failing tests:` list, with file:line) and prints `full log: <path>`, the whole suite output saved under the run folder in `land-logs/`.

**Run**

From the clean main checkout, use the combined path while B1 is still open:

```sh
code-forge block land B1 --run demo-1 --report .code-forge/B1-report.txt --transcript .code-forge/B1-transcript.log --merge
```

Or close without merging:

```sh
code-forge block close B1 --run demo-1 --report .code-forge/B1-report.txt --transcript .code-forge/B1-transcript.log
```

These are alternatives. Land requires an open block; do not close first and then run land.
`block close` does not run gates. It can warn about missing green test evidence and still close;
run step 11 first and read the result.
If the base branch moved while a block is open, update its base before retrying:

```sh
code-forge block rebase B1 --run demo-1
```

For a worktree block this checks and updates its recorded base against upstream changes.
If same-run closed blocks changed owned files, see the `--accept-landed` path in
[brief mode](brief-mode.md). That path needs the native binary.

**You see** — Output shortened (source form; needs a live run):

```text
block B1 closed
merge gate: scoped tests passed (<n> files, import_graph, <seconds>s)
block B1 landed
```

The Go engine can reuse a green full run for the exact merged tree. In that case it prints
`merge gate: reused green full suite on tree <tree-id>` instead of the scoped-tests line.
See [merge-gate reuse](how-it-works.md#one-worktree-per-block) for the evidence checks.

**What changed**

Close updates block state and writes `~/.code-forge/runs/demo-1/blocks/B1.done.json`.
A configured end hook runs after the block ends. Merge changes the base branch with a merge commit.
Rebase can make old review evidence stale. Rerun reviews and gates when the CLI requires them.

**If it fails**

Follow the reported blocker and `next` command. Fix stale reviews or an owned-file conflict before retrying.
A merged-suite failure leaves the base branch unmerged, but the block has already closed.
Fix the failure through a new block; do not retry land on a closed block.

**Next:** [Watch progress and end the run](#14-watch-progress-and-end-the-run).

## 14. Watch progress and end the run

**What it does**

`status` shows block states, spend, gates, and open findings.
`report` groups costs and review results; `ledger tail` shows recent events.
`logs` shows local errors; `list` shows skill installs, not runs.

**Run**

```sh
code-forge status --run demo-1
code-forge run status --run demo-1
code-forge report --slug guide-demo
code-forge ledger tail --slug guide-demo -n 20
code-forge logs
code-forge list
```

Use these during the run too. Wait for fresh done files or the end hook for block completion.
After every block is closed or stopped:

```sh
code-forge run end --run demo-1
```

**You see** — Output shortened (local checks with no active run):

```text
no active run
no errors logged
INSTALLED
```

With a run, status shows `spent`, one line per open block, and closed/stopped counts.
Run end prints `run demo-1 ended` (source form).

**What changed**

Progress commands show saved state. CLI calls may record command names in the local error log.
`run end` marks the run ended and stops its pinned worker.
It keeps the run record and ledger for later reports.

**If it fails**

An unknown run ID needs the ID from step 7. Use the slug from `.code-forge.yml` for reports.
Run end refuses open blocks; finish or stop them first.

**Next:** [Use an optional grant](#15-use-an-optional-grant), or [upgrade or remove](#16-upgrade-or-remove).

## 15. Use an optional grant

**What it does**

**Autopilot** lets a run continue while you are away.
A **grant** gives a delegate agent a short list of rights for a fixed time.
Use it during an active run, before step 14 ends that run.

**Run**

The owner starts the grant and confirms it. Set `<time-with-offset>` to a time within 24 hours:

```sh
code-forge autopilot start --run demo-1 --until <time-with-offset> --delegate L2 --allow waive:nit,model:choose --budget review=5,coding=20 --stop-at 0.9
code-forge autopilot status --run demo-1
code-forge autopilot binnacle --run demo-1 --markdown
code-forge autopilot log --run demo-1 --json
```

On return, read the log and stop the grant:

```sh
code-forge autopilot stop --run demo-1
```

**You see** — Output shortened (source form; grant not run):

```text
<run directory>/autopilot-binnacle.md
```

Status shows the expiry, scopes (allowed decisions), spend, and live log link or file paths.

**What changed**

The run stores the grant. Signed ledger rows record decisions.
The live log has a summary, called the Binnacle, and a Full log.
Without a docs connector, the CLI writes both Markdown files in the run directory.

**If it fails**

An expired or out-of-scope grant stops that decision for the owner.
At the budget threshold, the affected category pauses.
Read **Open questions** and **Actions only you can take** before work resumes.
See [autopilot.md](autopilot.md) for grant limits and the log workflow.

**Next:** [Upgrade or remove](#16-upgrade-or-remove).

## 16. Upgrade or remove

**What it does**

npm changes the installed package version. `upgrade` refreshes recorded skill links and copies.
`remove` removes recorded skill installs.
Keep the CLI installed through step 17 if you are about to release.

**Run**

For a global install:

```sh
npm install -g @codedology/code-forge@latest
code-forge upgrade --plain
code-forge version
code-forge doctor --quick
```

For a pinned project, choose a version and then refresh its skills:

```sh
npm install --save-dev --save-exact @codedology/code-forge@<version>
npx code-forge upgrade --plain
```

To remove a recorded Claude project skill, use this instead:

```sh
code-forge remove claude --scope project
```

Then remove the package with `npm uninstall -g @codedology/code-forge`, or
`npm uninstall --save-dev @codedology/code-forge` for a local install.

**You see** — Output shortened (local refresh and removal checks):

```text
upgraded claude (project) -> <package>/skill
removed claude (project) <project>/.claude/skills/code-forge
```

**What changed**

Upgrade refreshes installed skill content. Local npm changes update the manifest and lockfile.
Remove deletes only recorded skill paths; it keeps your config, code, and history.

**If it fails**

If a harness still uses an old skill, run `code-forge list` and check its target.
Run upgrade from the intended package version. Do not start a second install by mistake.

**Next:** [Release your project](#17-release-your-project).

## 17. Release your project

**What it does**

code-forge checks and lands work. Your project's own process ships it to users.
Release only after its blocks, reviews, and required human checks pass.

**Run**

Before your normal build, tag, or publish process, check the final checkout:

```sh
code-forge gates run --cwd .
code-forge report --slug guide-demo
git status --short
```

Then follow your project's release instructions. This tool has no release verb.
Maintainers who release code-forge itself should use [releasing.md](releasing.md).

**You see** — Output shortened (local gate check):

```text
"allOk":true
```

Git prints no status lines when the tracked work and untracked-file checks are clean.
Read the report for costs and unresolved work before you ship.

**What changed**

The gate may create build output. Your release process writes its own version, package, or tag.
These checks do not publish your project.

**If it fails**

Stop the release on a failed check. Fix it, rerun the checks, and use the project's release process again.

**Next:** [Where to go next](#where-to-go-next).

## Where to go next

- [Concepts](concepts.md): words, roles, and rules.
- [Getting started](getting-started.md): setup details and key sources.
- [How it works](how-it-works.md): the work loop and review depth.
- [Brief mode](brief-mode.md), [review only](review-only.md), and [autopilot](autopilot.md): alternate flows.
- [Command reference](reference/commands.md): generated CLI usage.
- [Config reference](reference/config.md): settings and defaults.
- [Native engine](native-engine.md): Go startup and Node fallback.
- [Agent skill](../skill/SKILL.md): the orchestrator's full rules.

## Cheat sheet

Use your own run ID, slug, paths, and ticket. The grant row is optional and runs before run end.
The open and land rows abbreviate arguments shown in full above.

| Step | Command |
|---|---|
| 1 Install | `npm install -g @codedology/code-forge`; `code-forge setup` |
| 2 Init | `code-forge init` |
| 3 Check | `code-forge tools`; `code-forge doctor --quick` |
| 4 Keys and models | `code-forge keys list`; `code-forge resolve L1` |
| 5 Choose | Full plan, brief mode, or review only |
| 6 Plan | `code-forge plan check plans/task.plan.md` after authoring and hardening |
| 7 Start | `code-forge run start --run demo-1` |
| 8 Open | `code-forge block open B1 --run demo-1 --level L0 --owned <paths> --acceptance <file>` |
| 9 Context | `code-forge context pack --run demo-1 --block B1` |
| 10 Review | `code-forge review-file <path> --block B1 --run demo-1` |
| 11 Gates | `code-forge gates run --run demo-1 --block B1` |
| 12 Fix | Repeat context, code, review, and gates for one block round |
| 13 Land | `code-forge block land B1 --run demo-1 --report <file> --transcript <file> --merge` |
| 14 Watch and end | `code-forge status --run demo-1`; `code-forge run end --run demo-1` |
| 15 Grant | `code-forge autopilot status --run demo-1` |
| 16 Upgrade or remove | `code-forge upgrade --plain`; `code-forge remove claude --scope project` |
| 17 Release | `code-forge gates run --cwd .`; then your project's release process |
