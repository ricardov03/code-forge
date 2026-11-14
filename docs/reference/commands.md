# Command reference

This page lists the CLI commands and their usage. It is generated from the CLI. Regenerate it with `node scripts/gen-commands-doc.mjs`.

- [author](#author)
- [autopilot](#autopilot)
- [block](#block)
- [ci](#ci)
- [cloud-coder](#cloud-coder)
- [context](#context)
- [decisions](#decisions)
- [docs](#docs)
- [doctor](#doctor)
- [facts](#facts)
- [gates](#gates)
- [heal](#heal)
- [help](#help)
- [init](#init)
- [jev](#jev)
- [keys](#keys)
- [land](#land)
- [ledger](#ledger)
- [list](#list)
- [logs](#logs)
- [models](#models)
- [native-status](#native-status)
- [outcome](#outcome)
- [pipeline](#pipeline)
- [plan](#plan)
- [proof](#proof)
- [queue](#queue)
- [remove](#remove)
- [report](#report)
- [resolve](#resolve)
- [review](#review)
- [review-file](#review-file)
- [run](#run)
- [s2](#s2)
- [setup](#setup)
- [spawn](#spawn)
- [status](#status)
- [sweep](#sweep)
- [tools](#tools)
- [upgrade](#upgrade)
- [validate](#validate)
- [version](#version)
- [worker](#worker)

## author

Write or harden a plan draft from a brief using an author session.

```text
usage: code-forge author --job plan|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]
```

## autopilot

Manage delegated run approvals, decisions, budgets, and activity records.

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot start

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes]
```

### autopilot status

```text
usage: code-forge autopilot status --run <id> [--json]
```

### autopilot stop

```text
usage: code-forge autopilot stop --run <id>
```

### autopilot ask

```text
usage: code-forge autopilot ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2)
```

### autopilot waive

```text
usage: code-forge autopilot waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id>
```

### autopilot round

```text
usage: code-forge autopilot round --run <id> --block <id> --file <path> --decision <id>
```

### autopilot level

```text
usage: code-forge autopilot level --run <id> --block <id> --plan <file> --decision <id>
```

### autopilot approve

```text
usage: code-forge autopilot approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset>
```

### autopilot binnacle

```text
usage: code-forge autopilot binnacle --run <id> [--json|--markdown] [--link <https url>]
```

### autopilot log

```text
usage: code-forge autopilot log --run <id> [--json|--markdown]
```

## block

Manage block worktrees, ownership, reviews, acceptance evidence, and closure.

```text
usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract|light] [--tests <paths…>] [--after <ids…>] [--after-outcome <ids…>]
       code-forge block ready <id> --run <r> [--transcript <file>] [--report <file>]
       code-forge block rebase <id> --run <r> --accept-landed
       code-forge block attempt|rebase <id> --run <r> · block close <id> --run <r> [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] · block claim <id> <path> --run <r> · block stop <id> --run <r> --reason <text>
       code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes]
       code-forge block reopen <id> --run <r> --file <path> --reason <text> [--yes]
       code-forge block accept-hit <id> --run <r> [--rule <rule-id>] --line <n> --transcript <path> --reason <text>
       code-forge block unwaive <id> <finding-id> --run <r> --file <path> --reason <text>
       code-forge block ack-waivers <id> --run <r>
       code-forge block human-pass <id> --run <r> --note <text> [--proof <dir-or-file>]
       code-forge block amend-acceptance <id> --run <r> --acceptance <file> --reason <text>
       code-forge block land <id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--push] [--watch-ci] [--copy-links]
       code-forge block land <id> <id>... --run <r> [--max <dur>] [--push] [--watch-ci] [--copy-links]
       every block verb takes --json: one JSON line on stdout ({"block","event",…}) instead of the text
```

### block open

```text
usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract|light] [--tests <paths…>] [--after <ids…>] [--after-outcome <ids…>] [--allow-api-mismatch] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.
                       Human checks use {clause, kind: human_check} without tests.
  --brief <file>       A file inside the repository, not inline text.
                       Its uses: lines (<file>: <symbol>(<param names>)) must fit the base:
                       each named parameter exists, else exit 2 with an api-fit: line.
                       Its extends: lines (<file>: <symbol>) own that file for additive change.
  --allow-api-mismatch  Open anyway; print each api-fit: line as a note (native only).
```

### block attempt

```text
usage: code-forge block attempt <id> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block rebase

```text
usage: code-forge block rebase <id> --run <r> [--head <ref>] [--accept-changed <file>… --reason <text>] [--accept-landed] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block claim

```text
usage: code-forge block claim <id> <path> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block ready

```text
usage: code-forge block ready <id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  Read-only close checks: list every blocker and its fix; exit 1 while any remain.
```

### block close

```text
usage: code-forge block close <id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] [--decide] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block land

```text
usage: code-forge block land <id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--push] [--watch-ci] [--copy-links] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  Join block reviews, run changed gates in its worktree, then close.
  --max <dur>  Total review wait (default 30m); accepts 90s, 30m or 1500ms.
  --merge     Run the merge gate on a trial merge, then merge with --no-ff in the clean main checkout.
              The merge gate runs the Go test selection for the merge, never the full suite;
              it runs nothing when a green gate already covers the merged tree (gate_reused).
  --no-merge-gate  Skip the merge gate with a warning (one block only; a batch refuses it).
  --push      After the merge, git push origin <base> from the main checkout
              (one block: needs --merge; land.push: true does the same).
  --watch-ci  After the push, start a detached code-forge ci watch (needs --push or land.push).
  --copy-links  Copy gates.merge_gate_links paths into the merge-gate tree instead of linking them.
  block land <id> <id>... --run <r> lands closed blocks in order with one merge gate (--merge implied).
```

### block stop

```text
usage: code-forge block stop <id> --run <r> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block waive

```text
usage: code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block reopen

```text
usage: code-forge block reopen <id> --run <r> --file <path> --reason <text> [--yes] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block accept-hit

```text
usage: code-forge block accept-hit <id> --run <r> [--rule <rule-id>] --line <n> --transcript <path> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block unwaive

```text
usage: code-forge block unwaive <id> <finding-id> --run <r> --file <path> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block ack-waivers

```text
usage: code-forge block ack-waivers <id> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block human-pass

```text
usage: code-forge block human-pass <id> --run <r> --note <text> [--proof <dir-or-file>] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block amend-acceptance

```text
usage: code-forge block amend-acceptance <id> --run <r> --acceptance <file> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.
                       Human checks use {clause, kind: human_check} without tests.
```

## ci

Watch the GitHub Actions runs of one commit; a red run names the failing jobs and tests and queues a heal item (served by the Go binary).

```text
usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]
```

## cloud-coder

Hand a block to the configured cloud coder and commit its returned patch in the block worktree (served by the Go binary).

```text
usage: code-forge cloud-coder --run <r> --block <id>
```

## context

Build a context pack for a block in a run.

```text
usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]
```

### context pack

```text
usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]
```

## decisions

List the block-close decisions the autopilot signed for the owner (rule_break hits, warning and nit waivers), served by the Go binary.

```text
usage: code-forge decisions list [--run <r>] [--json]
```

## docs

Fetch the docs for the installed version (sync) or print the best local copy (path), served by the Go binary.

```text
usage: code-forge docs sync|path [--json]
```

## doctor

Check the project configuration, tools, credentials, and runtime setup.

```text
usage: code-forge doctor [--quick] [--json] [--cwd <dir>]
```

## facts

Build and validate a facts sheet from a brief and project sources.

```text
usage: code-forge facts --brief <file> [--sources <path>...] [--out <file>] [--run <id>] [--timeout <seconds>]
```

## gates

Detect and run project gates or check secrets, scope, acceptance, and transcripts.

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); without --changed, over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
  gates run --run --block: when the block's base branch moved past its recorded base, no gate runs (exit 1) and the fix is printed: code-forge block rebase <id> --run <r> --accept-landed. --auto-rebase runs that rebase first; --allow-stale-base gates on the old base with a WARN line.
  gates run --run --block --changed: the Go test selection (the block's tests, tests that import a changed file); never the full suite; an empty selection skips the test gate.
  gates run --run --block --full: the only local full suite of a block (scope full, reason explicit); cannot go with --changed.
  gates run --run --block: the close-blocker preflight runs first by default; --no-preflight skips it, --preflight asks for it.
```

### gates detect

```text
usage: code-forge gates detect [--cwd <dir>]
```

### gates run

```text
usage: code-forge gates run [--cwd <dir>] [--timeout-ms <n>] [--slug <slug>] [--run <id>] [--block <id>] [--changed | --full] [--auto-rebase | --allow-stale-base] [--preflight | --no-preflight]
```

### gates secret-scan

```text
usage: code-forge gates secret-scan [--cwd <dir>] --file <path> [--file <path>...]
```

### gates safe-edit

```text
usage: code-forge gates safe-edit [--cwd <dir>] --base <sha>
```

### gates scope

```text
usage: code-forge gates scope [--cwd <dir>] --base <sha>
```

### gates acceptance

```text
usage: code-forge gates acceptance --clauses <file.json> --tap <file>
```

### gates transcript-grep

```text
usage: code-forge gates transcript-grep --file <path> [--cwd <dir>]
```

## heal

List, show and retry the heal queue: failures that pass by themselves (a held lock, a transient git error), served by the Go binary.

```text
usage: code-forge heal list [--run R] [--json] | show <id> [--run R] [--json] | run --run R [--once|--watch] [--max-tries N] [--json]
```

## help

List the available CLI verbs and shared command guidance.

```text
usage: code-forge help
```

## init

Configure a project and install the skill for selected harnesses.

```text
usage: code-forge init [--no-interaction] [--tools recommended|current] [--yes-tool <tool>]…
  [--harness a,b] [-g|-p] [--copy] [--provider P] [--level Ln=model[:effort][@provider]]…
  [--refresh-models] [--multimodel on|off] [--second-provider P]
  [--jev-ref <item-id|link|op://…> | --jev-env NAME | --no-jev] [--engine auto|solo|harness] [--solo-project N]
  [--coder-may-push yes|no] [--coder-may-open-draft-pr yes|no]
  [--gate name=cmd]… [--proof isolation=export|lock | high=a,b | link_dirs=a,b | copy_untracked=a,b]… [--skip-doctor]
```

## jev

Ask a recorded decision question through the Jev service.

```text
usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]
```

### jev ask

```text
usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]
```

## keys

Store, list, test, or remove named credential references.

```text
usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>
```

### keys list

```text
usage: code-forge keys list
```

### keys set

```text
usage: code-forge keys set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]]
```

### keys test

```text
usage: code-forge keys test <name> [--ref <ref>]
```

### keys remove

```text
usage: code-forge keys remove <name>
```

## land

Provide the block landing implementation used by block land.

```text
usage: code-forge land
  Used internally by code-forge block land; this module has no standalone handler.
```

## ledger

Read calibration and ledger history or record outcomes, spend, and outside reviews.

```text
usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

### ledger calibration

```text
usage: code-forge ledger calibration --slug <slug> [--question <id>]
```

### ledger outcome

```text
usage: code-forge ledger outcome --scan-git --slug <slug> [--cwd <dir>] [--days <n>]
usage: code-forge ledger outcome --pr <n> --ci red|green|reverted --slug <slug>
```

### ledger tail

```text
usage: code-forge ledger tail --slug <slug> [--n <count> | -n <count>]
```

### ledger add

```text
usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note "..."]
```

### ledger outside-review

```text
usage: code-forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

## list

List the recorded skill installations.

```text
usage: code-forge list
```

## logs

Read, summarize, clear, or report local CLI errors.

```text
usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path
       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

### logs summary

```text
usage: code-forge logs summary [--days N] [--json]
```

### logs clear

```text
usage: code-forge logs clear [--yes]
```

### logs path

```text
usage: code-forge logs path
```

### logs report

```text
usage: code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

## models

List provider model catalogs and optionally refresh them from local CLI caches.

```text
usage: code-forge models [--refresh --from-cli-caches]
```

## native-status

Convert cached daemon records and rows from standard input into run status JSON.

```text
usage: code-forge native-status
  Internal daemon companion: reads JSON records and rows from standard input.
```

## outcome

Seal, validate, or record a specialist outcome file.

```text
usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]
```

### outcome seal

```text
usage: code-forge outcome seal <outcome.json> [--root <dir>]
```

### outcome check

```text
usage: code-forge outcome check <outcome.json> [--root <dir>] [--json]
```

### outcome record

```text
usage: code-forge outcome record <outcome.json> [--root <dir>] [--step <id>] [--json]
```

## pipeline

Show pipeline step status or list the steps ready to run.

```text
usage: code-forge pipeline status|next [--json] [--root <dir>]
```

### pipeline status

```text
usage: code-forge pipeline status [--json] [--root <dir>]
```

### pipeline next

```text
usage: code-forge pipeline next [--json] [--root <dir>]
```

## plan

Check a plan against its facts, project rules, and recorded decisions.

```text
usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]
```

### plan check

```text
usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]
```

## proof

Manage proof tiers, isolated exports, locks, restoration, and red-green evidence.

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

### proof tier

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
```

### proof export

```text
usage: code-forge proof export <block> --run <r> [--remove]
```

### proof lock

```text
usage: code-forge proof lock <block> --run <r>
```

### proof unlock

```text
usage: code-forge proof unlock <block> --run <r>
```

### proof restore

```text
usage: code-forge proof restore <block> --run <r>
```

### proof red-green

```text
usage: code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

## queue

Show the machine-wide test queue or run a test command inside a queue slot.

```text
usage: code-forge queue [status] [--json]
       code-forge queue run [--kind full|changed] [--label X] -- <cmd> [args]
  The machine-wide FIFO test queue ($HOME/.code-forge/queue). Gates take a slot by themselves;
  wrap any other test command in `queue run`. CODE_FORGE_TEST_QUEUE=off disables it.
```

## remove

Remove recorded skill installations for a harness or scope.

```text
usage: code-forge remove [<harness>] [--scope project|global]
```

## report

Display or export project ledger reports, including per-block pace and run totals. Text, JSON and export carry the same 18 sections; pace comes before run_time. Pace durations are minutes; absent row kinds contribute 0. JSON includes the same pace numbers. Report currently runs in Node; no Go report exists yet.

```text
usage: code-forge report --slug <slug> [--json] [--export <dir>]
```

## resolve

Show the provider, model, effort, fallbacks, and CLI resolved for a level.

```text
usage: code-forge resolve <L0|L1|L2|L3>
```

## review

Review changed files against acceptance criteria or stated intent.

```text
usage: code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]
```

## review-file

Queue file reviews, wait for results, or check review evidence after a rebase.

```text
usage: code-forge review-file [--cwd <dir>] <path> --block <id> [--run <id>] [--fresh] | review-file --wait <ticket> [--max <seconds>s]
       code-forge review-file --wait-block <id> --run <id> [--max <seconds>s]
       code-forge review-file --rebase-check --block <id> --run <id>
       code-forge review-file <answering file> --block <id> --answers <id,id,…> --of <source> [--run <id>]
       --cwd <dir> applies to queueing and both wait modes
```

## run

Start, inspect, reload, or end a run, manage its workers, and list or clear its open human checks.

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id> | human-checks list --run <id> [--json] | human-checks clear --run <id> --block <id> --clause <C<n>> --note <text> [--json]
```

### run start

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief]
usage: code-forge run start --add-worker --run <id>
usage: code-forge run start --reattach --run <id> [--worker-pid <pid>]
```

### run status

```text
usage: code-forge run status --run <id>
```

### run reload

```text
usage: code-forge run reload --run <id>
```

### run end

```text
usage: code-forge run end --run <id>
```

### run human-checks

```text
usage: code-forge run human-checks list --run <id> [--json]
usage: code-forge run human-checks clear --run <id> --block <id> --clause <C<n>> --note <text> [--json]
```

## s2

Answer a decision packet using a System 2 session.

```text
usage: code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]
```

## setup

Check the Go binary for this system: print OK, or the problem and one fix command.

```text
usage: code-forge setup
```

## spawn

Run a session at a selected level and role from a brief.

```text
usage: code-forge spawn --level L0|L1|L2|L3 --role coder|reviewer|judge|s2|author|facts|delegate --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <seconds>] [--background] [--print-argv]
```

## status

Show active runs, block progress, review gates, and spend.

```text
usage: code-forge status [--run <id>] [--json]
```

## sweep

Report stale processes and optionally stop those proven stale.

```text
usage: code-forge sweep [--apply] [--json]
Dry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.
--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.
Zombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.
```

## tools

List detected tools or install selected tools after confirmation.

```text
usage: code-forge tools [--json] | tools install [<id>...] [--yes] [--dry-run]
  (install asks one yes, default no; answering no prints "cancelled — nothing installed" and exits 1)
```

### tools install

```text
usage: code-forge tools install [<id>...] [--yes] [--dry-run]
  (install asks one yes, default no; answering no prints "cancelled — nothing installed" and exits 1)
```

## upgrade

Refresh recorded skill installations from a skill source.

```text
usage: code-forge upgrade [--source <path>] [--plain]
```

## validate

Validate project configuration and pipeline definitions.

```text
usage: code-forge validate [--file <path>]
```

## version

Print the package name and version.

```text
usage: code-forge version
```

## worker

Serve a run's review queue until stopped or drain it once.

```text
usage: code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]
```
