# Command reference

This page lists the CLI commands and their usage. It is generated from the CLI. Regenerate it with `node scripts/gen-commands-doc.mjs`.

- [author](#author)
- [autopilot](#autopilot)
- [block](#block)
- [context](#context)
- [doctor](#doctor)
- [facts](#facts)
- [gates](#gates)
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
- [remove](#remove)
- [report](#report)
- [resolve](#resolve)
- [review](#review)
- [review-file](#review-file)
- [run](#run)
- [s2](#s2)
- [spawn](#spawn)
- [status](#status)
- [sweep](#sweep)
- [tools](#tools)
- [upgrade](#upgrade)
- [validate](#validate)
- [version](#version)
- [worker](#worker)

## author

usage: code-forge author --job plan|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]

No usage text yet.

## autopilot

usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]

No usage text yet.

### autopilot start

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot status

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot stop

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot ask

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot waive

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot round

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot level

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot approve

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot binnacle

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

### autopilot log

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]
```

## block

usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract|light] [--tests <paths…>] [--after <ids…>] [--after-outcome <ids…>]

No usage text yet.

### block open

```text
usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract|light] [--tests <paths…>] [--after <ids…>] [--after-outcome <ids…>] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.
                       Human checks use {clause, kind: human_check} without tests.
  --brief <file>       A file inside the repository, not inline text.
```

### block ready

```text
usage: code-forge block ready <id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  Read-only close checks: list every blocker and its fix; exit 1 while any remain.
```

### block rebase

```text
usage: code-forge block rebase <id> --run <r> [--head <ref>] [--accept-changed <file>… --reason <text>] [--accept-landed] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block attempt

```text
usage: code-forge block attempt <id> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block close

```text
usage: code-forge block close <id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] [--decide] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

### block claim

```text
usage: code-forge block claim <id> <path> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
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

### block land

```text
usage: code-forge block land <id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  Join block reviews, run changed gates in its worktree, then close.
  --max <dur>  Total review wait (default 30m); accepts 90s, 30m or 1500ms.
  --merge     Check the merged full suite, then merge with --no-ff in the clean main checkout.
  --no-merge-gate  Skip the merged full suite with a warning.
```

## context

usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]

No usage text yet.

### context pack

```text
usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]
```

## doctor

usage: code-forge doctor [--quick] [--json] [--cwd <dir>]

No usage text yet.

## facts

usage: code-forge facts --brief <file> [--sources <path>...] [--out <file>] [--run <id>] [--timeout <seconds>]

No usage text yet.

## gates

usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]

No usage text yet.

### gates detect

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
```

### gates run

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
```

### gates secret-scan

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
```

### gates safe-edit

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
```

### gates scope

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
```

### gates acceptance

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
```

### gates transcript-grep

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
```

## help

Block usage: code-forge block <verb> --help (or -h).

```text
Usage: code-forge <verb> [args]

Verbs:
  author
  autopilot
  block
  context
  doctor
  facts
  gates
  help
  init
  jev
  keys
  land
  ledger
  list
  logs
  models
  native-status
  outcome
  pipeline
  plan
  proof
  remove
  report
  resolve
  review
  review-file
  run
  s2
  spawn
  status
  sweep
  tools
  upgrade
  validate
  version
  worker

Block usage: code-forge block <verb> --help (or -h).
  block open --acceptance <file>: JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.
  block open --brief <file>: a file inside the repository, not inline text.
  block land <id> --run <r> --report <file> --transcript <file> [--merge] [--json]: join reviews, run changed gates, close, then optionally merge with --no-ff.
  Configuration fallback entries must be {provider, model} objects, not model-name strings (optional effort).
```

## init

usage: code-forge init [--no-interaction] [--tools recommended|current] [--yes-tool <tool>]…

No usage text yet.

## jev

usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]

No usage text yet.

### jev ask

```text
usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]
```

## keys

usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>

No usage text yet.

### keys list

```text
usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>
```

### keys set

```text
usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>
```

### keys test

```text
usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>
```

### keys remove

```text
usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>
```

## land

No usage text yet.

## ledger

usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...

No usage text yet.

### ledger calibration

```text
usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

### ledger outcome

```text
usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

### ledger tail

```text
usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

### ledger add

```text
usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

### ledger outside-review

```text
usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

## list

usage: code-forge list

No usage text yet.

## logs

usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path

No usage text yet.

### logs summary

```text
usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path
       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

### logs clear

```text
usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path
       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

### logs path

```text
usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path
       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

### logs report

```text
usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path
       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

## models

No usage text yet.

## native-status

No usage text yet.

## outcome

usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]

No usage text yet.

### outcome seal

```text
usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]
```

### outcome check

```text
usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]
```

### outcome record

```text
usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]
```

## pipeline

usage: code-forge pipeline status|next [--json] [--root <dir>]

No usage text yet.

### pipeline status

```text
usage: code-forge pipeline status|next [--json] [--root <dir>]
```

### pipeline next

```text
usage: code-forge pipeline status|next [--json] [--root <dir>]
```

## plan

usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]

No usage text yet.

### plan check

```text
usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]
```

## proof

usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]

No usage text yet.

### proof tier

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

### proof export

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

### proof lock

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

### proof unlock

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

### proof restore

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

### proof red-green

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

## remove

usage: code-forge remove [<harness>] [--scope project|global]

No usage text yet.

## report

usage: code-forge report --slug <slug> [--json] [--export <dir>]

No usage text yet.

## resolve

No usage text yet.

## review

usage: code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]

```text
usage: code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]
```

## review-file

usage: code-forge review-file [--cwd <dir>] <path> --block <id> [--run <id>] [--fresh] | review-file --wait <ticket> [--max <seconds>s]

No usage text yet.

## run

usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id>

No usage text yet.

### run start

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id>
```

### run status

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id>
```

### run reload

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id>
```

### run end

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id>
```

## s2

usage: code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]

No usage text yet.

## spawn

usage: code-forge spawn --level L0|L1|L2|L3 --role coder|reviewer|judge|s2|author|facts|delegate --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <seconds>] [--background] [--print-argv]

No usage text yet.

## status

usage: code-forge status [--run <id>] [--json]

No usage text yet.

## sweep

Dry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.

```text
usage: code-forge sweep [--apply] [--json]
Dry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.
--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.
Zombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.
```

## tools

usage: code-forge tools [--json] | tools install [<id>...] [--yes] [--dry-run]

No usage text yet.

### tools install

```text
usage: code-forge tools [--json] | tools install [<id>...] [--yes] [--dry-run]
  (install asks one yes, default no; answering no prints "cancelled — nothing installed" and exits 1)
```

## upgrade

usage: code-forge upgrade [--source <path>] [--plain]

No usage text yet.

## validate

No usage text yet.

## version

@codedology/code-forge 0.16.3

```text
@codedology/code-forge 0.16.3
```

## worker

usage: code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]

No usage text yet.
