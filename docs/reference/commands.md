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

Write or harden a plan draft from a brief

```text
usage: code-forge author --job plan|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]
```

## autopilot

Let a delegate decide for the owner within a grant

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot start

Grant the autopilot a time window, scopes and budget

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes]
```

### autopilot status

Show the grant, spend per category and pauses

```text
usage: code-forge autopilot status --run <id> [--json]
```

### autopilot stop

End the autopilot grant now

```text
usage: code-forge autopilot stop --run <id>
```

### autopilot ask

Ask the delegate one question inside the grant

```text
usage: code-forge autopilot ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2)
```

### autopilot waive

Waive a warning or nit finding by a decision

```text
usage: code-forge autopilot waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id>
```

### autopilot round

Record one extra review round for a file

```text
usage: code-forge autopilot round --run <id> --block <id> --file <path> --decision <id>
```

### autopilot level

Record the lane (level) the delegate chose

```text
usage: code-forge autopilot level --run <id> --block <id> --plan <file> --decision <id>
```

### autopilot approve

Change one setting until a set time

```text
usage: code-forge autopilot approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset>
```

### autopilot binnacle

Print or write the autopilot run summary

```text
usage: code-forge autopilot binnacle --run <id> [--json|--markdown] [--link <https url>]
```

### autopilot log

Print every autopilot event of a run

```text
usage: code-forge autopilot log --run <id> [--json|--markdown]
```

## block

Open, check, close and land the blocks of a run

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

Start a block: owned files, acceptance tests, brief

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

Start the next attempt of an open block

```text
usage: code-forge block attempt <id> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block rebase

Move a block onto the new base of its branch

```text
usage: code-forge block rebase <id> --run <r> [--head <ref>] [--accept-changed <file>… --reason <text>] [--accept-landed] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block claim

Add one more owned path to an open block

```text
usage: code-forge block claim <id> <path> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block ready

List what still blocks the close of a block

```text
usage: code-forge block ready <id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  Read-only close checks: list every blocker and its fix; exit 1 while any remain.
```

### block close

Run the final checks and close the block

```text
usage: code-forge block close <id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] [--decide] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block land

Join reviews, run gates, close, then merge

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

Stop a block and record why

```text
usage: code-forge block stop <id> --run <r> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block waive

Accept one review finding without a fix

```text
usage: code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block reopen

Reopen a stopped file review for a new round

```text
usage: code-forge block reopen <id> --run <r> --file <path> --reason <text> [--yes] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block accept-hit

Accept one transcript rule hit as safe

```text
usage: code-forge block accept-hit <id> --run <r> [--rule <rule-id>] --line <n> --transcript <path> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block unwaive

Undo an agent waiver of a finding

```text
usage: code-forge block unwaive <id> <finding-id> --run <r> --file <path> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block ack-waivers

Acknowledge the agent waivers of a block

```text
usage: code-forge block ack-waivers <id> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block human-pass

Record the owner's device check and close

```text
usage: code-forge block human-pass <id> --run <r> --note <text> [--proof <dir-or-file>] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block amend-acceptance

Change the acceptance clauses of a block

```text
usage: code-forge block amend-acceptance <id> --run <r> --acceptance <file> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.
                       Human checks use {clause, kind: human_check} without tests.
```

## ci

Watch the CI runs of one commit

```text
usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]
```

## cloud-coder

Send a block to the cloud coder and commit its patch

```text
usage: code-forge cloud-coder --run <r> --block <id>
```

## context

Build the context a coder gets

```text
usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]
```

### context pack

Build the context pack of a block

```text
usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]
```

## decisions

Show the decisions the autopilot signed

```text
usage: code-forge decisions list [--run <r>] [--json]
```

## docs

Get the docs of the installed version

```text
usage: code-forge docs sync|path [--json]
```

## doctor

Check this machine and project are ready

```text
usage: code-forge doctor [--quick] [--json] [--cwd <dir>]
```

## facts

Build a facts sheet from a brief and the sources

```text
usage: code-forge facts --brief <file> [--sources <path>...] [--out <file>] [--run <id>] [--timeout <seconds>]
```

## gates

Run the project gates and the safety checks

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

Find the test, lint, type and format commands

```text
usage: code-forge gates detect [--cwd <dir>]
```

### gates run

Run the gates for a block or the project

```text
usage: code-forge gates run [--cwd <dir>] [--timeout-ms <n>] [--slug <slug>] [--run <id>] [--block <id>] [--changed | --full] [--auto-rebase | --allow-stale-base] [--preflight | --no-preflight]
```

### gates secret-scan

Check files for secrets

```text
usage: code-forge gates secret-scan [--cwd <dir>] --file <path> [--file <path>...]
```

### gates safe-edit

Check the change keeps protected files safe

```text
usage: code-forge gates safe-edit [--cwd <dir>] --base <sha>
```

### gates scope

Check the change stays inside the owned files

```text
usage: code-forge gates scope [--cwd <dir>] --base <sha>
```

### gates acceptance

Check each acceptance clause has a passing test

```text
usage: code-forge gates acceptance --clauses <file.json> --tap <file>
```

### gates transcript-grep

Check a coder transcript for forbidden commands

```text
usage: code-forge gates transcript-grep --file <path> [--cwd <dir>]
```

## heal

Retry failures that pass by themselves

```text
usage: code-forge heal list [--run R] [--json] | show <id> [--run R] [--json] | run --run R [--once|--watch] [--max-tries N] [--json]
```

## help

Show the commands, or the help of one command

```text
usage: code-forge help
```

## init

Set up code-forge in this project

```text
usage: code-forge init [--no-interaction] [--tools recommended|current] [--yes-tool <tool>]…
  [--harness a,b] [-g|-p] [--copy] [--provider P] [--level Ln=model[:effort][@provider]]…
  [--refresh-models] [--multimodel on|off] [--second-provider P]
  [--jev-ref <item-id|link|op://…> | --jev-env NAME | --no-jev] [--engine auto|solo|harness] [--solo-project N]
  [--coder-may-push yes|no] [--coder-may-open-draft-pr yes|no]
  [--gate name=cmd]… [--proof isolation=export|lock | high=a,b | link_dirs=a,b | copy_untracked=a,b]… [--skip-doctor]
```

## jev

Ask Jev a recorded decision question

```text
usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]
```

### jev ask

Ask Jev one decision question

```text
usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]
```

## keys

Store, list, test or remove credential references

```text
usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>
```

### keys list

List the stored credential references

```text
usage: code-forge keys list
```

### keys set

Store a credential reference

```text
usage: code-forge keys set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]]
```

### keys test

Check a credential reference works

```text
usage: code-forge keys test <name> [--ref <ref>]
```

### keys remove

Remove a stored credential reference

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

Read the ledger or record outcomes and spend

```text
usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

### ledger calibration

Show how reliable the recorded answers were

```text
usage: code-forge ledger calibration --slug <slug> [--question <id>]
```

### ledger outcome

Record the CI result of a pull request

```text
usage: code-forge ledger outcome --scan-git --slug <slug> [--cwd <dir>] [--days <n>]
usage: code-forge ledger outcome --pr <n> --ci red|green|reverted --slug <slug>
```

### ledger tail

Print the last ledger rows

```text
usage: code-forge ledger tail --slug <slug> [--n <count> | -n <count>]
```

### ledger add

Add coder spend that code-forge did not see

```text
usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note "..."]
```

### ledger outside-review

Record an outside review of a pull request

```text
usage: code-forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

## list

List the skill installations and harness paths

```text
usage: code-forge list
```

## logs

Show the last local CLI errors

```text
usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path
       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

### logs summary

Count the CLI errors of the last days

```text
usage: code-forge logs summary [--days N] [--json]
```

### logs clear

Delete the local CLI error log

```text
usage: code-forge logs clear [--yes]
```

### logs path

Print the path of the CLI error log

```text
usage: code-forge logs path
```

### logs report

Report CLI errors as a GitHub issue

```text
usage: code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

## models

List the provider model catalogs

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

Seal, check or record a specialist outcome file

```text
usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]
```

### outcome seal

Fill the file hashes of an outcome file

```text
usage: code-forge outcome seal <outcome.json> [--root <dir>]
```

### outcome check

Check an outcome file and its files

```text
usage: code-forge outcome check <outcome.json> [--root <dir>] [--json]
```

### outcome record

Check an outcome and save it in the ledger

```text
usage: code-forge outcome record <outcome.json> [--root <dir>] [--step <id>] [--json]
```

## pipeline

Show the steps of a pipeline

```text
usage: code-forge pipeline status|next [--json] [--root <dir>]
```

### pipeline status

Show the state of each pipeline step

```text
usage: code-forge pipeline status [--json] [--root <dir>]
```

### pipeline next

List the pipeline steps ready to run

```text
usage: code-forge pipeline next [--json] [--root <dir>]
```

## plan

Check a plan before coding

```text
usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]
```

### plan check

Check a plan against its facts and rules

```text
usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]
```

## proof

Prove the tests: tiers, exports and red-green

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

### proof tier

Show the proof tier of a file

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
```

### proof export

Export a block's tests to a clean copy

```text
usage: code-forge proof export <block> --run <r> [--remove]
```

### proof lock

Lock a block's test files

```text
usage: code-forge proof lock <block> --run <r>
```

### proof unlock

Unlock a block's test files

```text
usage: code-forge proof unlock <block> --run <r>
```

### proof restore

Put back the files a proof changed

```text
usage: code-forge proof restore <block> --run <r>
```

### proof red-green

Prove a test fails without the fix

```text
usage: code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

## queue

Show the machine-wide test queue

```text
usage: code-forge queue [status] [--json]
       code-forge queue run [--kind full|changed] [--label X] -- <cmd> [args]
  The machine-wide FIFO test queue ($HOME/.code-forge/queue). Gates take a slot by themselves;
  wrap any other test command in `queue run`. CODE_FORGE_TEST_QUEUE=off disables it.
```

## remove

Remove skill installations

```text
usage: code-forge remove [<harness>] [--scope project|global]
```

## report

Show or export the project ledger report

```text
usage: code-forge report --slug <slug> [--json] [--export <dir>]
```

## resolve

Show the model, effort and CLI of a level

```text
usage: code-forge resolve <L0|L1|L2|L3>
```

## review

Review changed files against intent or acceptance

```text
usage: code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]
```

## review-file

Queue a file review or wait for the results

```text
usage: code-forge review-file [--cwd <dir>] <path> --block <id> [--run <id>] [--fresh] | review-file --wait <ticket> [--max <seconds>s]
       code-forge review-file --wait-block <id> --run <id> [--max <seconds>s]
       code-forge review-file --rebase-check --block <id> --run <id>
       code-forge review-file <answering file> --block <id> --answers <id,id,…> --of <source> [--run <id>]
       --cwd <dir> applies to queueing and both wait modes
```

## run

Start, check, reload or end a run

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id>
```

### run start

Start a run, or add or reattach a worker

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief]
usage: code-forge run start --add-worker --run <id>
usage: code-forge run start --reattach --run <id> [--worker-pid <pid>]
```

### run status

Show the state of a run

```text
usage: code-forge run status --run <id>
```

### run reload

Load the changed settings into a run

```text
usage: code-forge run reload --run <id>
```

### run end

End a run

```text
usage: code-forge run end --run <id>
```

## s2

Answer a decision packet with a System 2 session

```text
usage: code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]
```

## setup

Check the Go binary for this system

```text
usage: code-forge setup
```

## spawn

Run a model session at a level and role

```text
usage: code-forge spawn --level L0|L1|L2|L3 --role coder|reviewer|judge|s2|author|facts|delegate --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <seconds>] [--background] [--print-argv]
```

## status

Show the active runs and their blocks

```text
usage: code-forge status [--run <id>] [--json]
```

## sweep

Find stale processes; stop them with --apply

```text
usage: code-forge sweep [--apply] [--json]
Dry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.
--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.
Zombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.
```

## tools

List the tools code-forge found

```text
usage: code-forge tools [--json] | tools install [<id>...] [--yes] [--dry-run]
  (install asks one yes, default no; answering no prints "cancelled — nothing installed" and exits 1)
```

### tools install

Install selected tools after you say yes

```text
usage: code-forge tools install [<id>...] [--yes] [--dry-run]
  (install asks one yes, default no; answering no prints "cancelled — nothing installed" and exits 1)
```

## upgrade

Refresh the skill installations

```text
usage: code-forge upgrade [--source <path>] [--plain]
```

## validate

Check the project configuration

```text
usage: code-forge validate [--file <path>]
```

## version

Show the version

```text
usage: code-forge version
```

## worker

Serve a run's review queue

```text
usage: code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]
```
