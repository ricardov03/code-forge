# Command reference

This page lists every CLI command: its purpose, when to use it, its usage and flags, examples with their real output, exit codes, the `--json` shape and related commands. It is generated from the Go help table; agents read the same data in [`commands.json`](commands.json). Regenerate both with `sh scripts/gen-commands-doc.sh`.

- [audit](#audit)
- [author](#author)
- [autopilot](#autopilot)
- [block](#block)
- [ci](#ci)
- [cloud-coder](#cloud-coder)
- [config](#config)
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
- [release](#release)
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

## audit

Audit every command before a major release

```text
usage: code-forge audit commands [--json] [--out <file>]
```

#### When to use

Before a major release: check every command's help and refusals in a throwaway HOME.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge audit --help
```

Exit 0. Output:

```text
Description:
  Audit every command before a major release

Usage:
  usage: code-forge audit commands [--json] [--out <file>]

Available commands:
  audit commands  Run every command in a throwaway HOME and write the audit report

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`audit commands`](#audit-commands)
- [`doctor`](#doctor)
- [`version`](#version)

### audit commands

Run every command in a throwaway HOME and write the audit report

```text
usage: code-forge audit commands [--json] [--out <file>]
```

#### When to use

Before `npm run release -- major`: run every command of this build with --help and with an unknown flag in a throwaway HOME (stubbed tools, network blocked), and commit the report as docs/audits/audit-<version>.md.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |
| `--out <file>` | path | not set | Also write the Markdown report to this file (docs/audits/audit-<version>.md for a major release); prints a one-line summary instead of the report. |

#### Examples

```sh
code-forge audit commands --json
```

Exit 0. Output:

```text
{"version":"dev","commit":"<sha>","date":"2026-01-01T00:00:00Z","commands":127,"checks":254,"problems":{"high":0,"low":0,"medium":0},"out":"","rows":[{"command":"audit","check":"help","result":"ok","severity":"","detail":"","block":""},{"command":"audit","check":"bad-flag","result":"ok","severity":"…
```

```sh
code-forge audit commands --out
```

Exit 2. Output:

```text
code-forge audit: --out needs a file
usage: code-forge audit commands [--json] [--out <file>]
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | At least one high problem (a crash, a hand-over or a --help with no usage), or the report could not be written; fix or give each a block id before a major release. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `version` | string |
| `commit` | string |
| `date` | string |
| `commands` | number |
| `checks` | number |
| `problems` | object |
| `out` | string |
| `rows` | array |

#### Related

- [`audit`](#audit)
- [`doctor`](#doctor)
- [`help`](#help)

## author

Write or harden a plan draft from a brief

```text
usage: code-forge author --job plan|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]
```

#### When to use

Turn a brief and a facts sheet into a plan draft (--job plan), or check and improve a draft (--job harden).

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--job plan\|harden` | enum | not set | plan writes a draft; harden checks and improves one. |
| `--brief <file>` | path | not set | Brief file inside the repository. |
| `--facts <file>` | path | not set | Facts sheet file. |
| `--draft <file>` | path | not set | Plan draft to harden. |
| `--answers <file>` | path | not set | File with the answers to earlier questions. |
| `--out <file>` | path | not set | Output file. |
| `--run <id>` | string | not set | Run id (r-...). |
| `--timeout <seconds>` | number | not set | Longest run time. |

#### Examples

```sh
code-forge author --job plan --brief plans/brief.md --timeout 5
```

Exit 2. Output:

```text
author: facts sheet required: run code-forge facts --brief plans/brief.md first, then pass the sheet with --facts <file>
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`facts`](#facts)
- [`plan check`](#plan-check)
- [`s2`](#s2)

## autopilot

Let a delegate decide for the owner within a grant

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset>|--until-done [--deadline <ISO-8601 with offset>] [--blocks <id,…>] --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown] | extend --run <id> --until <ISO-8601 with offset>|--deadline <ISO-8601 with offset> [--yes] | digest --run <id> [--json] | ratify <row-id> --run <id> [--json] | revert <row-id> --run <id> [--json] | found --run <id> (--source <gate|land|coder|flaky|handover|review> --title <text> --evidence <text>|--evidence-file <path> [--label <label,…>] [--fingerprint <key>] | --report <file> | --list) [--from <block>] [--json]
```

#### When to use

Let a delegate level answer the owner's questions for a set time, within allowed scopes.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge autopilot --help
```

Exit 0. Output:

```text
Description:
  Let a delegate decide for the owner within a grant

Usage:
  usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset>|--until-done [--deadline <ISO-8601 with offset>] [--blocks <id,…>] --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run …

Available commands:
  autopilot approve   Change one setting until a set time (owner)
  autopilot ask       Ask the delegate one question inside the grant
  autopilot binnacle  Print or write the autopilot run summary
  autopilot digest    List what the autopilot decided, with ratify and revert commands
  autopilot extend    Move the grant end time or deadline later (owner)
  autopilot found     Turn a problem found during the run into a queued block of the same run
  autopilot level     Record the lane (level) the delegate chose
  autopilot log       Print every autopilot event of a run
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`autopilot start`](#autopilot-start)
- [`autopilot status`](#autopilot-status)
- [`autopilot stop`](#autopilot-stop)

### autopilot start

Grant the autopilot a time window, scopes and budget

```text
usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset>|--until-done [--deadline <ISO-8601 with offset>] [--blocks <id,…>] --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes]
```

#### When to use

Before you leave a run alone: grant a delegate the scopes it may decide until a time.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--until <ISO-8601 with offset>` | time | not set | End time of the grant. |
| `--until-done` | bool | off | Keep the grant until the blocks are done. |
| `--deadline <ISO-8601 with offset>` | time | not set | Hard end time of the grant. |
| `--blocks <id,…>` | list | not set | Only these blocks are in the grant. |
| `--delegate <L2\|L3>` | enum | not set | Level that decides for the owner. |
| `--allow <scope,…>` | list | not set | Scopes the delegate may decide (for example waive:nit). |
| `--deny <scope,…>` | list | not set | Scopes the delegate may never decide. |
| `--budget <category>=<usd>,…` | list | not set | Spend limit per category, in USD. |
| `--stop-at <0..1>` | number | not set | Stop when this share of the budget is spent. |
| `--yes` | bool | off | Answer yes to the confirmation. |

#### Examples

```sh
code-forge autopilot start --run <run> --until <time> --delegate L2 --allow waive:nit --yes
```

Exit 0. Output:

```text
autopilot run <run>: grant ap-<id> active until 2026-01-01T00:00:00Z (0h 59m)
  delegate L2 · allow waive:nit · budget no caps · stop at 90%
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`autopilot status`](#autopilot-status)
- [`autopilot extend`](#autopilot-extend)
- [`autopilot stop`](#autopilot-stop)

### autopilot status

Show the grant, spend per category and pauses

```text
usage: code-forge autopilot status --run <id> [--json]
```

#### When to use

Check whether a grant is active, until when, and what it allows.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge autopilot status --run <run> --json
```

Exit 0. Output:

```text
{
  "run": "<run>",
  "state": "active",
  "grant_id": "ap-<id>",
  "delegate": "L2",
  "until": "2026-01-01T00:00:00Z",
  "time_left_s": 3599,
  "scopes": [
    "waive:nit"
  ],
  "deny": [],
  "fixed_deny": [
    "waive:critical",
    "waive:proof",
    "reviews:skip",
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints JSON over several lines (pretty-printed): an object with these keys.

| Key | Type |
|---|---|
| `run` | string |
| `state` | string |
| `grant_id` | string |
| `delegate` | string |
| `until` | string |
| `time_left_s` | number |
| `scopes` | array |
| `deny` | array (empty in the example) |
| `fixed_deny` | array |
| `caps` | object |
| `stop_at` | number |
| `spend` | object |
| `paused` | array (empty in the example) |
| `link` | null |
| `files` | object |
| `started_at` | string |
| `stopped_at` | null |
| `expired_at` | null |

#### Related

- [`autopilot start`](#autopilot-start)
- [`autopilot digest`](#autopilot-digest)
- [`autopilot log`](#autopilot-log)

### autopilot stop

End the autopilot grant now

```text
usage: code-forge autopilot stop --run <id>
```

#### When to use

End a grant now; later questions go to the owner again.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |

#### Examples

```sh
code-forge autopilot stop --run <run>
```

Exit 0. Output:

```text
autopilot run <run>: grant ap-<id> stopped
  link      none
  binnacle  ~/.code-forge/runs/<run>/autopilot-binnacle.md
  full log  ~/.code-forge/runs/<run>/autopilot-log.md
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`autopilot start`](#autopilot-start)
- [`autopilot status`](#autopilot-status)
- [`autopilot binnacle`](#autopilot-binnacle)

### autopilot extend

Move the grant end time or deadline later

```text
usage: code-forge autopilot extend --run <id> --until <ISO-8601 with offset>|--deadline <ISO-8601 with offset> [--yes]
```

#### When to use

Give an active grant more time without starting a new one.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--until <ISO-8601 with offset>` | time | not set | End time of the grant. |
| `--deadline <ISO-8601 with offset>` | time | not set | Hard end time of the grant. |
| `--yes` | bool | off | Answer yes to the confirmation. |

#### Examples

```sh
code-forge autopilot extend --run <run> --until <time> --yes
```

Exit 0. Output:

```text
autopilot run <run>: grant ap-<id> now runs until 2026-01-01T00:00:00Z (1h 59m; was 2026-01-01T00:00:00Z)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`autopilot start`](#autopilot-start)
- [`autopilot status`](#autopilot-status)

### autopilot ask

Ask the delegate one question inside the grant

```text
usage: code-forge autopilot ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2)
```

#### When to use

An agent needs an owner decision during a grant: the delegate answers it or it goes to the owner.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--scope <scope>` | string | not set | Scope of the question or install. |
| `--question <text>` | string | not set | Question text or id. |
| `--options a,b,…` | list | not set | Answers the delegate may choose from. |
| `--context-file <path>` | path | not set | File with context for the question. |
| `--block <id>` | string | not set | Block id. |
| `--file <path>` | path | not set | File path inside the repository. |
| `--finding <id>` | string | not set | Review finding id. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge autopilot ask --run <run> --scope waive:nit --question 'waive it?' --options waive,fix --json
```

Exit 3. Output:

```text
{"run":"<run>","scope":"waive:nit","grant_id":"ap-<id>","decision_id":"ad-<id>","answered":false,"acted":false,"to_owner":true,"reason":"session failed (exit 1)","decision":null,"confidence":null,"within_scope":null,"escalate":null,"answer_reason":null}
level=L2 provider=anthropic model=claude-opus-5-5 effort=none fallback_step=0
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |
| 3 | The question went to the owner; no decision yet. |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `run` | string |
| `scope` | string |
| `grant_id` | string |
| `decision_id` | string |
| `answered` | boolean |
| `acted` | boolean |
| `to_owner` | boolean |
| `reason` | string |
| `decision` | null |
| `confidence` | null |
| `within_scope` | null |
| `escalate` | null |
| `answer_reason` | null |

#### Related

- [`autopilot digest`](#autopilot-digest)
- [`autopilot ratify`](#autopilot-ratify)
- [`decisions list`](#decisions-list)

### autopilot waive

Waive a warning or nit finding by a decision

```text
usage: code-forge autopilot waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id>
```

#### When to use

Record a waiver the delegate decided, for a warning or nit finding.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--block <id>` | string | not set | Block id. |
| `--file <path>` | path | not set | File path inside the repository. |
| `--finding <id>` | string | not set | Review finding id. |
| `--severity <warning\|nit>` | enum | not set | Severity of the finding. |
| `--reason <text>` | string | not set | Why; written to the ledger. |
| `--decision <id>` | string | not set | Decision id the delegate answered. |

#### Examples

```sh
code-forge autopilot waive --run <run> --block B1 --file src/a.txt --finding F1 --severity nit --reason r --decision d-missing
```

Exit 1. Output:

```text
autopilot waive: refused — finding F1 in src/a.txt has no verifiable recorded severity (not-recorded); critical findings are never delegated
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`autopilot ask`](#autopilot-ask)
- [`block waive`](#block-waive)

### autopilot round

Record one extra review round for a file

```text
usage: code-forge autopilot round --run <id> --block <id> --file <path> --decision <id>
```

#### When to use

Record that the delegate allowed one more review round for a file.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--block <id>` | string | not set | Block id. |
| `--file <path>` | path | not set | File path inside the repository. |
| `--decision <id>` | string | not set | Decision id the delegate answered. |

#### Examples

```sh
code-forge autopilot round --run <run> --block B1 --file src/a.txt --decision d-missing
```

Exit 1. Output:

```text
autopilot round: refused — the grant refuses (not-allowed); the question goes to the owner
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`autopilot ask`](#autopilot-ask)
- [`review-file`](#review-file)

### autopilot level

Record the lane (level) the delegate chose

```text
usage: code-forge autopilot level --run <id> --block <id> --plan <file> --decision <id>
```

#### When to use

Record the level the delegate picked for a block of the plan.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--block <id>` | string | not set | Block id. |
| `--plan <file>` | path | not set | Plan file. |
| `--decision <id>` | string | not set | Decision id the delegate answered. |

#### Examples

```sh
code-forge autopilot level --run <run> --block B1 --plan plans/plan.md --decision d-missing
```

Exit 2. Output:

```text
autopilot level: block B1 is not in the plan's block table
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`autopilot ask`](#autopilot-ask)
- [`block open`](#block-open)

### autopilot approve

Change one setting until a set time

```text
usage: code-forge autopilot approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset>
```

#### When to use

The owner approves a config change for the grant's time, at a terminal.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--key <dot.path>` | string | not set | Config key, as a dot path. |
| `--value <json>` | string | not set | New value, as JSON. |
| `--until <ISO-8601 with offset>` | time | not set | End time of the grant. |

#### Examples

```sh
code-forge autopilot approve --run <run> --key review.close_wait_s --value 5 --until <time>
```

Exit 2. Output:

```text
autopilot approve: no terminal to confirm; only the owner approves a change, in a terminal (approve has no --yes)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`autopilot start`](#autopilot-start)
- [`run reload`](#run-reload)

### autopilot binnacle

Print or write the autopilot run summary

```text
usage: code-forge autopilot binnacle --run <id> [--json|--markdown] [--link <https url>]
```

#### When to use

Read the owner's log of a grant: what was decided, by whom, and why.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |
| `--markdown` | bool | off | Print Markdown instead of text. |
| `--link <https url>` | string | not set | Link to show next to the entry. |

#### Examples

```sh
code-forge autopilot binnacle --run <run> --json
```

Exit 0. Output:

```text
{
  "title": "Autopilot run <run>",
  "byline": {
    "date": "2026-11-19",
    "owner": "T"
  },
  "status": [
    {
      "item": "Result",
      "value": "0 of 1 block closed"
    },
    {
      "item": "Stack/PRs",
      "value": "none recorded"
    },
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints JSON over several lines (pretty-printed): an object with these keys.

| Key | Type |
|---|---|
| `title` | string |
| `byline` | object |
| `status` | array |
| `decisions` | array |
| `blocks` | array |
| `open_questions` | array |
| `owner_actions` | array |
| `incidents` | array (empty in the example) |
| `timeline` | array |

#### Related

- [`autopilot log`](#autopilot-log)
- [`autopilot digest`](#autopilot-digest)

### autopilot log

Print every autopilot event of a run

```text
usage: code-forge autopilot log --run <id> [--json|--markdown]
```

#### When to use

List every grant event in time order.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |
| `--markdown` | bool | off | Print Markdown instead of text. |

#### Examples

```sh
code-forge autopilot log --run <run> --json
```

Exit 0. Output:

```text
[
  {
    "time": "2026-01-01T00:00:00Z",
    "event": "Question to the owner",
    "block": null,
    "file": null,
    "detail": "waive:nit · to owner (session-failed) · ad-<id>",
    "link": null
  },
  {
    "time": "2026-01-01T00:00:00Z",
    "event": "Delegate session",
    "block": null,
    "file": null,
    "detail": "L2 · anthropic · status failed",
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints JSON over several lines (pretty-printed): an array of objects with these keys.

| Key | Type |
|---|---|
| `time` | string |
| `event` | string |
| `block` | null |
| `file` | null |
| `detail` | string |
| `link` | null |

#### Related

- [`autopilot binnacle`](#autopilot-binnacle)
- [`autopilot digest`](#autopilot-digest)

### autopilot digest

List what the autopilot decided, with ratify and revert commands

```text
usage: code-forge autopilot digest --run <id> [--json]
```

#### When to use

See the delegate's decisions that wait for the owner to ratify or revert.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge autopilot digest --run <run> --json
```

Exit 0. Output:

```text
{"run":"<run>","rows":[{"id":"dg-<id>","kind":"extend","block":null,"file":null,"when":"2026-01-01T00:00:00Z","why":"grant ap-<id> extended from 2026-01-01T00:00:00Z to 2026-01-01T00:00:00Z","ratify":"code-forge autopilot ratify dg-<id> --run <run>","revert":null,"revert_note":"an extend is undone b…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `run` | string |
| `rows` | array |

#### Related

- [`autopilot ratify`](#autopilot-ratify)
- [`autopilot revert`](#autopilot-revert)

### autopilot ratify

Accept one autopilot decision from the digest

```text
usage: code-forge autopilot ratify <row-id> --run <id> [--json]
```

#### When to use

The owner keeps a delegate decision from the digest.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge autopilot ratify row-missing --run <run> --json
```

Exit 1. Output:

```text
autopilot ratify: digest-row-not-open: run <run> has no open digest row row-missing (autopilot digest --run <run> lists them); nothing was recorded
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`autopilot digest`](#autopilot-digest)
- [`autopilot revert`](#autopilot-revert)

### autopilot revert

Undo one autopilot decision from the digest

```text
usage: code-forge autopilot revert <row-id> --run <id> [--json]
```

#### When to use

The owner undoes a delegate decision from the digest.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge autopilot revert row-missing --run <run> --json
```

Exit 1. Output:

```text
autopilot revert: digest-row-not-open: run <run> has no open digest row row-missing (autopilot digest --run <run> lists them); nothing was recorded
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`autopilot digest`](#autopilot-digest)
- [`autopilot ratify`](#autopilot-ratify)

### autopilot found

Turn a problem found during the run into a queued block of the same run

```text
usage: code-forge autopilot found --run <id> (--source <gate|land|coder|flaky|handover|review> --title <text> --evidence <text>|--evidence-file <path> [--label <label,…>] [--fingerprint <key>]
```

#### When to use

During an autopilot run: turn a problem you found (a red gate, a land failure, a coder's leftover, a flaky test, a hand-over, a review) into a queued block of the same run instead of fixing it on the side.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--source <gate\|land\|coder\|flaky\|handover\|review>` | enum | not set | Where the problem was found: gate, land, coder, flaky, handover or review. |
| `--title <text>` | string | not set | One line that names the problem; it becomes the block title. |
| `--evidence <text>` | string | not set | What shows the problem (a failing test, an error line); goes into the brief. |
| `--evidence-file <path>` | path | not set | File with the evidence, instead of --evidence. |
| `--label <label,…>` | list | not set | Labels for the found block, comma separated. |
| `--fingerprint <key>` | string | not set | Key that marks repeats of the same problem (default: made from source and title). |

#### Examples

```sh
code-forge autopilot found --run <run> --source gate --title 'gates run is red on main' --evidence 'TestB1 failed' --json
```

Exit 0. Output:

```text
{"finding":"F1","fingerprint":"fp-56e705e225e22ac0","source":"gate","title":"gates run is red on main","labels":[],"from_block":null,"block":"B2","state":"queued","reason":null,"seen":1,"brief":"~/.code-forge/runs/<run>/found/B2.brief.md","acceptance":"~/.code-forge/runs/<run>/found/B2.acceptance.ym…
```

```sh
code-forge autopilot found --run <run> --list
```

Exit 0. Output:

```text
run <run>: 1 found (found → block → state)
F1 gate → B2 → not opened: gates run is red on main
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`autopilot status`](#autopilot-status)
- [`autopilot binnacle`](#autopilot-binnacle)
- [`autopilot start`](#autopilot-start)

## block

Open, check, close and land the blocks of a run

```text
usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract|light] [--tests <paths…>] [--after <ids…>] [--after-outcome <ids…>]
       code-forge block ready <id> --run <r> [--transcript <file>] [--report <file>]
       code-forge block rebase <id> --run <r> --accept-landed
       code-forge block attempt|rebase <id> --run <r> · block close <id> --run <r> [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] [--dry-run] · block claim <id> <path> --run <r> · block stop <id> --run <r> --reason <text>
       code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes]
       code-forge block reopen <id> --run <r> --file <path> --reason <text> [--yes]
       code-forge block accept-hit <id> --run <r> [--rule <rule-id>] --line <n> --transcript <path> --reason <text>
       code-forge block unwaive <id> <finding-id> --run <r> --file <path> --reason <text>
       code-forge block ack-waivers <id> --run <r>
       code-forge block human-pass <id> --run <r> --note <text> [--proof <dir-or-file>] [--dry-run]
       code-forge block settle <id> --run <r> [--report <file>] [--transcript <file>]
       code-forge block amend-acceptance <id> --run <r> --acceptance <file> --reason <text>
       code-forge block land <id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--push] [--watch-ci] [--copy-links]
       code-forge block land <id> <id>... --run <r> [--max <dur>] [--push] [--watch-ci] [--copy-links]
       every block verb takes --json: one JSON line on stdout ({"block","event",…}) instead of the text
```

#### When to use

Work on one block of a run: open it, check it, close or land it.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge block --help
```

Exit 0. Output:

```text
Description:
  Open, check, close and land the blocks of a run

Usage:
  usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract|light] [--tests <paths…>] [--after <ids…>] [--after-outcome <ids…>]
         code-forge block ready <id> --run <r> [--transcript <file>] [--report <file>]
         code-forge block rebase <id> --run <r> --accept-landed
         code-forge block attempt|rebase <id> --run <r> · block close <id> --run <r> [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] [--dry-run] · block claim <id> <path> --run <r> · block stop <id> --run <r> --reason <text>
         code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes]
         code-forge block reopen <id> --run <r> --file <path> --reason <text> [--yes]
         code-forge block accept-hit <id> --run <r> [--rule <rule-id>] --line <n> --transcript <path> --reason <text>
         code-forge block unwaive <id> <finding-id> --run <r> --file <path> --reason <text>
         code-forge block ack-waivers <id> --run <r>
         code-forge block human-pass <id> --run <r> --note <text> [--proof <dir-or-file>] [--dry-run]
         code-forge block settle <id> --run <r> [--report <file>] [--transcript <file>]
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`block open`](#block-open)
- [`block ready`](#block-ready)
- [`block close`](#block-close)
- [`block land`](#block-land)

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

#### When to use

Start a block in a live run, with its owned files and acceptance tests.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--level L<n>` | string | not set | Level (L0 to L3) that picks the model. |
| `--owned <paths…>` | list | not set | Files the block may change. |
| `--acceptance <file>` | path | not set | Acceptance file: a JSON or YAML list of {clause, tests}. |
| `--brief <file>` | path | not set | Brief file inside the repository. |
| `--attempt <n>` | number | not set | Attempt number of the block. |
| `--base <sha>` | string | not set | Base commit or ref to compare against. |
| `--lines <n>` | number | not set | Expected size of the change in lines. |
| `--kind code\|docs\|contract\|light` | enum | not set | Kind of item. |
| `--tests <paths…>` | list | not set | Test files of the block. |
| `--after <ids…>` | list | not set | Open only after these blocks are closed. |
| `--after-outcome <ids…>` | list | not set | Open only after these blocks have an outcome. |
| `--allow-api-mismatch` | bool | off | Open even when the brief names an API the code does not have. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block open B1 --run <run> --level L1 --owned src/a.txt test/a.test.sh --acceptance plans/acc.yml --brief plans/brief.md --json
```

Exit 0. Output:

```text
{"block":"B1","event":"open","level":"L1","asked":"L1","source":"flag","kind":"code","attempt":1,"base_sha":"<sha>","tests":0,"worktree":null,"brief":"BRIEF plans/brief.md lines=1 sha=651ebc00 <<<EOM>>>"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `event` | string |
| `level` | string |
| `asked` | string |
| `source` | string |
| `kind` | string |
| `attempt` | number |
| `base_sha` | string |
| `tests` | number |
| `worktree` | null |
| `brief` | string |

#### Related

- [`run start`](#run-start)
- [`block ready`](#block-ready)
- [`block stop`](#block-stop)

### block attempt

Start the next attempt of an open block

```text
usage: code-forge block attempt <id> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

Start the next attempt of a block after a failed one, keeping its claims.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block attempt B1 --run <run> --json
```

Exit 0. Output:

```text
{"block":"B1","event":"attempt","attempt":2}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `event` | string |
| `attempt` | number |

#### Related

- [`block open`](#block-open)
- [`block stop`](#block-stop)

### block rebase

Move a block onto the new base of its branch

```text
usage: code-forge block rebase <id> --run <r> [--head <ref>] [--accept-changed <file>… --reason <text>] [--accept-landed] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

Move an open block onto the current main.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--head <ref>` | string | not set | Ref to rebase onto (default: main). |
| `--accept-changed <file>…` | list | not set | Accept these changed files after a rebase; needs --reason. |
| `--reason <text>` | string | not set | Why; written to the ledger. |
| `--accept-landed` | bool | off | Accept that the block's work already landed on main. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block rebase B1 --run <run> --json
```

Exit 0. Output:

```text
{"block":"B1","event":"rebased","old_base":"<sha>","new_base":"<sha>"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `event` | string |
| `old_base` | string |
| `new_base` | string |

#### Related

- [`review-file`](#review-file)
- [`block land`](#block-land)

### block claim

Add one more owned path to an open block

```text
usage: code-forge block claim <id> <path> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

Add a file the block must change but did not own at open.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block claim B1 src/b.txt --run <run> --json
```

Exit 0. Output:

```text
{"block":"B1","event":"claimed","path":"src/b.txt"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `event` | string |
| `path` | string |

#### Related

- [`block open`](#block-open)
- [`gates scope`](#gates-scope)

### block ready

List what still blocks the close of a block

```text
usage: code-forge block ready <id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  Read-only close checks: list every blocker and its fix; exit 1 while any remain.
```

#### When to use

Before a close: list what still blocks it and the command that fixes each.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--worker-pid <pid>` | number | not set | Pid of the worker that serves the run. |
| `--transcript <file>` | path | not set | Coder transcript file. |
| `--report <file>` | path | not set | Coder report file (JSON). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block ready B1 --run <run> --json
```

Exit 1. Output:

```text
{"block":"B1","ready":false,"blockers":[{"code":"worker.down","file":null,"detail":"pinned worker pid <pid> is not running; last worker exit: signal (stopped by signal terminated) at 2026-01-01T00:00:00Z","fix":"code-forge run start --add-worker --run <run>","clears_by":"orchestrator"},{"code":"no_g…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Not ready; each blocker is listed with its fix. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `ready` | boolean |
| `blockers` | array |

#### Related

- [`block settle`](#block-settle)
- [`block close`](#block-close)
- [`review-file`](#review-file)

### block settle

Run the orchestrator fixes that clear a block, then report

```text
usage: code-forge block settle <id> --run <r> [--report <file>] [--transcript <file>] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

Run the fixes block ready lists, then check again.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--report <file>` | path | not set | Coder report file (JSON). |
| `--transcript <file>` | path | not set | Coder transcript file. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block settle B1 --run <run> --json
```

Exit 1. Output:

```text
{"block":"B1","outcome":"failed","exit":1,"rounds":1,"actions":[{"fix":"gates","code":"no_green_gates","command":"code-forge gates run --run <run> --block B1","result":"ok"},{"fix":"reviews","code":"unreviewed","command":"code-forge review-file --wait-block B1 --run <run>","result":"failed","detail"…
settle B1: gates ok
settle B1: reviews failed: the run worker is down; run code-forge run start --reattach --run <run>
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `outcome` | string |
| `exit` | number |
| `rounds` | number |
| `actions` | array |
| `remaining` | array |
| `failed_fix` | string |
| `detail` | string |

#### Related

- [`block ready`](#block-ready)
- [`block close`](#block-close)

### block close

Run the final checks and close the block

```text
usage: code-forge block close <id> --run <r> [--worker-pid <pid>] [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] [--auto-claim] [--decide] [--dry-run] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

Finish a block: re-check gates, reviews and acceptance, then record the close.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--worker-pid <pid>` | number | not set | Pid of the worker that serves the run. |
| `--transcript <file>` | path | not set | Coder transcript file. |
| `--report <file>` | path | not set | Coder report file (JSON). |
| `--no-require-reviews` | bool | off | Close without approved reviews (owner only). |
| `--remove-worktree` | bool | off | Remove the block's worktree after the close. |
| `--auto-claim` | bool | off | Claim changed files outside the owned list before closing. |
| `--decide` | bool | off | Let the owner decide open findings at the prompt. |
| `--dry-run` | bool | off | Show what would happen; change nothing. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block close B1 --run <run> --dry-run --json
```

Exit 1. Output:

```text
{"block":"B1","dry_run":true,"verb":"close","ready":false,"blockers":[{"code":"worker.down","file":null,"detail":"pinned worker pid <pid> is not running; last worker exit: signal (stopped by signal terminated) at 2026-01-01T00:00:00Z","fix":"code-forge run start --add-worker --run <run>","clears_by"…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `dry_run` | boolean |
| `verb` | string |
| `ready` | boolean |
| `blockers` | array |

#### Related

- [`block ready`](#block-ready)
- [`block land`](#block-land)
- [`gates run`](#gates-run)

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

#### When to use

Finish a worktree block in one step: join reviews, run gates, close, and merge when asked.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--report <file>` | path | not set | Coder report file (JSON). |
| `--transcript <file>` | path | not set | Coder transcript file. |
| `--max <dur>` | duration | not set | Longest wait (90s, 30m or 1500ms). |
| `--merge` | bool | off | Merge the block into main after the merge gate passes. |
| `--no-merge-gate` | bool | off | Skip the merge gate. |
| `--push` | bool | off | Push main after the merge. |
| `--watch-ci` | bool | off | Wait for CI on the pushed commit. |
| `--copy-links` | bool | off | Copy linked folders into the worktree instead of linking them. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block land B1 --run <run> --report plans/report.json --transcript plans/t.txt --max 2s --json
```

Exit 1. Output:

```text
{"block":"B1","event":"refused","step":"join","reason":"block has no recorded worktree and branch","next":"code-forge review-file --wait-block B1 --run <run>"}
block B1 land stopped at join: block has no recorded worktree and branch
next: code-forge review-file --wait-block B1 --run <run>
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `event` | string |
| `step` | string |
| `reason` | string |
| `next` | string |

#### Related

- [`block close`](#block-close)
- [`review-file`](#review-file)
- [`ci watch`](#ci-watch)

### block stop

Stop a block and record why

```text
usage: code-forge block stop <id> --run <r> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

Stop a block without closing it; its files go back to the run.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--reason <text>` | string | not set | Why; written to the ledger. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block stop B1 --run <run> --reason done --json
```

Exit 0. Output:

```text
{"block":"B1","event":"stopped"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `event` | string |

#### Related

- [`block open`](#block-open)
- [`block reopen`](#block-reopen)
- [`run end`](#run-end)

### block waive

Accept one review finding without a fix

```text
usage: code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

The owner accepts a review finding without a fix, at a terminal.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--file <path>` | path | not set | File path inside the repository. |
| `--reason <text>` | string | not set | Why; written to the ledger. |
| `--yes` | bool | off | Answer yes to the confirmation. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block waive B1 F1 --run <run> --file src/a.txt --reason test --json
```

Exit 2. Output:

```text
block waive needs the human at a terminal to confirm; nothing was recorded (--yes works only for a recorded warning or nit finding, with evidence)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`block unwaive`](#block-unwaive)
- [`block ack-waivers`](#block-ack-waivers)
- [`autopilot waive`](#autopilot-waive)

### block reopen

Reopen a stopped file review for a new round

```text
usage: code-forge block reopen <id> --run <r> --file <path> --reason <text> [--yes] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

The owner reopens a stopped or lost file of a block.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--file <path>` | path | not set | File path inside the repository. |
| `--reason <text>` | string | not set | Why; written to the ledger. |
| `--yes` | bool | off | Answer yes to the confirmation. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block reopen B1 --run <run> --file src/a.txt --reason again --yes --json
```

Exit 1. Output:

```text
block reopen: reopen-not-stopped: src/a.txt has no review state; only a stopped or lost file is reopened; nothing was recorded
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`block stop`](#block-stop)
- [`block open`](#block-open)

### block accept-hit

Accept one transcript rule hit as safe

```text
usage: code-forge block accept-hit <id> --run <r> [--rule <rule-id>] --line <n> --transcript <path> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

The owner accepts one transcript rule hit as safe, at a terminal.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--rule <rule-id>` | string | not set | Transcript rule id that matched. |
| `--line <n>` | number | not set | Line number in the transcript. |
| `--transcript <path>` | path | not set | Coder transcript file. |
| `--reason <text>` | string | not set | Why; written to the ledger. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block accept-hit B1 --run <run> --line 1 --transcript plans/tr.txt --reason ok --json
```

Exit 2. Output:

```text
block accept-hit needs the human at a terminal to confirm; nothing was recorded (there is no --yes)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`gates transcript-grep`](#gates-transcript-grep)
- [`block close`](#block-close)

### block unwaive

Undo an agent waiver of a finding

```text
usage: code-forge block unwaive <id> <finding-id> --run <r> --file <path> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

The owner withdraws an agent waiver so the finding needs a fix again.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--file <path>` | path | not set | File path inside the repository. |
| `--reason <text>` | string | not set | Why; written to the ledger. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block unwaive B1 F1 --run <run> --file src/a.txt --reason undo --json
```

Exit 1. Output:

```text
block unwaive: unwaive-no-agent-waiver: no verified agent waiver for this block, file and finding; nothing was recorded
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`block waive`](#block-waive)
- [`block ack-waivers`](#block-ack-waivers)

### block ack-waivers

Acknowledge the agent waivers of a block

```text
usage: code-forge block ack-waivers <id> --run <r> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

The owner confirms the agent waivers of a block before its close.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block ack-waivers B1 --run <run> --json
```

Exit 1. Output:

```text
block ack-waivers: ack-waivers-none: the block has no counted agent waivers; nothing was recorded
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`block waive`](#block-waive)
- [`block unwaive`](#block-unwaive)

### block human-pass

Record the owner's device check and close

```text
usage: code-forge block human-pass <id> --run <r> --note <text> [--proof <dir-or-file>] [--dry-run] [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
```

#### When to use

The owner records that the human checks of a block passed.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--note <text>` | string | not set | Free text note. |
| `--proof <dir-or-file>` | path | not set | Proof file or folder for the owner's pass. |
| `--dry-run` | bool | off | Show what would happen; change nothing. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block human-pass B1 --run <run> --note looked --dry-run --json
```

Exit 1. Output:

```text
{"block":"B1","dry_run":true,"verb":"human-pass","ready":false,"blockers":[{"code":"not-awaiting","file":null,"detail":"block B1 is not awaiting a human check","fix":"code-forge block close B1 --run <run>","clears_by":"orchestrator"}]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `block` | string |
| `dry_run` | boolean |
| `verb` | string |
| `ready` | boolean |
| `blockers` | array |

#### Related

- [`run human-checks`](#run-human-checks)
- [`block close`](#block-close)

### block amend-acceptance

Change the acceptance clauses of a block

```text
usage: code-forge block amend-acceptance <id> --run <r> --acceptance <file> --reason <text> [--json] [--help|-h]
  --help, -h  Print this usage and exit without changing state.
  --json      Print one JSON line on success instead of text.
  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.
                       Human checks use {clause, kind: human_check} without tests.
```

#### When to use

The owner changes the acceptance tests of an open block.

Only the owner runs it, at a terminal.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--acceptance <file>` | path | not set | Acceptance file: a JSON or YAML list of {clause, tests}. |
| `--reason <text>` | string | not set | Why; written to the ledger. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge block amend-acceptance B1 --run <run> --acceptance plans/acc.yml --reason same --json
```

Exit 2. Output:

```text
block amend-acceptance needs the human at a terminal to confirm; nothing was recorded (there is no --yes)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`block open`](#block-open)
- [`gates acceptance`](#gates-acceptance)

## ci

Watch the CI runs of one commit

```text
usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]
```

#### When to use

Wait for CI results from the command line.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge ci --help
```

Exit 0. Output:

```text
Description:
  Watch the CI runs of one commit

Usage:
  usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]

Available commands:
  ci watch  Wait for the CI runs of a commit; name failed tests

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`ci watch`](#ci-watch)
- [`block land`](#block-land)

### ci watch

Wait for the CI runs of a commit; name failed tests

```text
usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]
```

#### When to use

After a push: wait until the CI runs for a commit finish.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--sha <sha>` | string | not set | Commit sha to watch. |
| `--run <r>` | string | not set | Run id (r-...). |
| `--timeout 30m` | duration | not set | Longest run time. |
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge ci watch
```

Exit 2. Output:

```text
ci watch: --sha is required
usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]
```

```sh
code-forge ci watch --sha <sha> --json --timeout 1s
```

Exit 3. Output:

```text
{"sha":"<sha>","status":"timeout","runs":[],"message":"CI still running for <sha> after 1s (last gh error: gh run list: exit status 1)"}
ci watch: gh run list: exit status 1
ci watch: gh run list: exit status 1
ci watch: CI still running for <sha> after 1s (last gh error: gh run list: exit status 1)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | CI failed. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |
| 3 | CI still running when the timeout ended. |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `sha` | string |
| `status` | string |
| `runs` | array (empty in the example) |
| `message` | string |

#### Related

- [`block land`](#block-land)
- [`ledger outcome`](#ledger-outcome)

## cloud-coder

Send a block to the cloud coder and commit its patch

```text
usage: code-forge cloud-coder --run <r> --block <id>
```

#### When to use

Hand a worktree block to a cloud coder.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--block <id>` | string | not set | Block id. |

#### Examples

```sh
code-forge cloud-coder --run <run> --block B1
```

Exit 1. Output:

```text
{"block":"B1","status":"refused","reason":"no-worktree","message":"block B1 has no worktree; open it with a worktree first"}
cloud-coder: block B1 has no worktree; open it with a worktree first
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`spawn`](#spawn)
- [`block open`](#block-open)

## config

Check and fix the project configuration

```text
usage: code-forge config fix [--dry-run] [--file <path>]
```

#### When to use

Repair the project's .code-forge.yml.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge config --help
```

Exit 0. Output:

```text
Description:
  Check and fix the project configuration

Usage:
  usage: code-forge config fix [--dry-run] [--file <path>]

Available commands:
  config fix  Rewrite old config shapes this release refuses

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`config fix`](#config-fix)
- [`validate`](#validate)

### config fix

Rewrite old config shapes this release refuses

```text
usage: code-forge config fix [--dry-run] [--file <path>]
```

#### When to use

After an upgrade, or when validate fails: rewrite old or bad config keys.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dry-run` | bool | off | Show what would happen; change nothing. |
| `--file <path>` | path | not set | File path inside the repository. |

#### Examples

```sh
code-forge config fix --dry-run
```

Exit 0. Output:

```text
<repo>/.code-forge.yml: config ok
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`validate`](#validate)
- [`doctor`](#doctor)

## context

Build the context a coder gets

```text
usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]
```

#### When to use

Build the context a coder gets with a task.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge context --help
```

Exit 0. Output:

```text
Description:
  Build the context a coder gets

Usage:
  usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]

Available commands:
  context pack  Build the context pack of a block

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`context pack`](#context-pack)
- [`spawn`](#spawn)

### context pack

Build the context pack of a block

```text
usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]
```

#### When to use

Before you brief a coder: write the project rules, commands and lessons for its block.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--block <id>` | string | not set | Block id. |
| `--json` | bool | off | Print JSON instead of text. |
| `--path-only` | bool | off | Print only the path of the pack. |

#### Examples

```sh
code-forge context pack --run <run> --block B1 --json
```

Exit 0. Output:

```text
{"path":"<tmp>/cf-tmp/<run>/context/B1.md","tokens":1499,"lines":65,"cut":0,"sha256":"<sha256>"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `path` | string |
| `tokens` | number |
| `lines` | number |
| `cut` | number |
| `sha256` | string |

#### Related

- [`spawn`](#spawn)
- [`block open`](#block-open)

## decisions

Show the decisions the autopilot signed

```text
usage: code-forge decisions list [--run <r>] [--json]
```

#### When to use

Read the decisions recorded for the owner.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge decisions --help
```

Exit 0. Output:

```text
Description:
  Show the decisions the autopilot signed

Usage:
  usage: code-forge decisions list [--run <r>] [--json]

Available commands:
  decisions list  List the close decisions the autopilot signed

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`decisions list`](#decisions-list)
- [`autopilot digest`](#autopilot-digest)

### decisions list

List the close decisions the autopilot signed

```text
usage: code-forge decisions list [--run <r>] [--json]
```

#### When to use

See which decisions were made, by whom, in a run or in all runs.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge decisions list --json
```

Exit 0. Output:

```text
[]
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints JSON: an array (empty in the example).

#### Related

- [`autopilot digest`](#autopilot-digest)
- [`status`](#status)

## docs

Get the docs of the installed version

```text
usage: code-forge docs sync|path [--json]
```

#### When to use

Keep a local copy of the code-forge docs for agents.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge docs --help
```

Exit 0. Output:

```text
Description:
  Get the docs of the installed version

Usage:
  usage: code-forge docs sync|path [--json]

Available commands:
  docs path  Print the path of the best local docs copy
  docs sync  Fetch the docs for the installed version

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`docs sync`](#docs-sync)
- [`docs path`](#docs-path)

### docs sync

Fetch the docs for the installed version

```text
usage: code-forge docs sync [--json]
```

#### When to use

Fetch or refresh the local docs copy.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge docs sync --json
```

Exit 0. Output:

```text
{"status":"none","reason":"offline or unreachable; no packed copy"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `status` | string |
| `reason` | string |

#### Related

- [`docs path`](#docs-path)

### docs path

Print the path of the best local docs copy

```text
usage: code-forge docs path [--json]
```

#### When to use

Find the folder of the local docs copy.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge docs path
```

Exit 1. Output:

```text
docs: none; run code-forge docs sync
```

```sh
code-forge docs path --json
```

Exit 1. Output:

```text
docs: none; run code-forge docs sync
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`docs sync`](#docs-sync)

## doctor

Check this machine and project are ready

```text
usage: code-forge doctor [--quick] [--json] [--cwd <dir>]
```

#### When to use

After setup or when something fails: check config, links, keys and tools.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--quick` | bool | off | Run only the fast checks. |
| `--json` | bool | off | Print JSON instead of text. |
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |

#### Examples

```sh
code-forge doctor --quick --json
```

Exit 0. Output:

```text
{"ok":true,"counts":{"OK":1,"WARN":2,"FAIL":0,"INFO":0},"rows":[{"id":"config","status":"OK","label":"config","detail":"valid"},{"id":"links","status":"WARN","label":"links","detail":"no harness link recorded (run code-forge init)"},{"id":"keys","status":"WARN","label":"keys","detail":"jev key not r…
doctor: 1 ok, 2 warn, 0 fail
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | At least one check failed. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `ok` | boolean |
| `counts` | object |
| `rows` | array |

#### Related

- [`validate`](#validate)
- [`setup`](#setup)
- [`keys list`](#keys-list)

## facts

Build a facts sheet from a brief and the sources

```text
usage: code-forge facts --brief <file> [--sources <path>...] [--out <file>] [--run <id>] [--timeout <seconds>]
```

#### When to use

Before planning: have an L0 session write a facts sheet from a brief.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--brief <file>` | path | not set | Brief file inside the repository. |
| `--sources <path>...` | list | not set | Files the facts sheet may read. |
| `--out <file>` | path | not set | Output file. |
| `--run <id>` | string | not set | Run id (r-...). |
| `--timeout <seconds>` | number | not set | Longest run time. |

#### Examples

```sh
code-forge facts --brief plans/brief.md --timeout 5
```

Exit 1. Output:

```text
level=L0 provider=anthropic model=claude-haiku-4-5-20251001 effort=none fallback_step=0
facts: the provider CLI printed nothing on stderr
facts: failed (exit 1)
next: code-forge doctor (checks each provider CLI and its login; code-forge logs --last 5 shows the error log)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`author`](#author)
- [`plan check`](#plan-check)

## gates

Run the project gates and the safety checks

```text
usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); without --changed, over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
  gates run --run --block: when the block's base branch moved past its recorded base, no gate runs (exit 1) and the fix is printed: code-forge block rebase <id> --run <r> --accept-landed. --auto-rebase runs that rebase first; --allow-stale-base gates on the old base with a WARN line.
  gates run --run --block --changed: the Go test selection (the block's tests, tests that import a changed file); never the full suite; an empty selection skips the test gate.
  gates run --run --block --full: the only local full suite of a block (scope full, reason explicit); cannot go with --changed.
  gates run --run --block: a full suite in the shared checkout while other blocks of the run are open there prints 'full suite in a shared tree (N other open blocks)' and records shared_tree: N on gate.done; use --changed for the block's tests, or proof.isolation: worktree.
  gates run --run --block: the close-blocker preflight runs first by default; --no-preflight skips it, --preflight asks for it.
```

#### When to use

Run the quality gates and checks a close relies on.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge gates --help
```

Exit 0. Output:

```text
Description:
  Run the project gates and the safety checks

Usage:
  usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]
    gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); without --changed, over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.
    gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.
    gates run --run --block: when the block's base branch moved past its recorded base, no gate runs (exit 1) and the fix is printed: code-forge block rebase <id> --run <r> --accept-landed. --auto-rebase runs that rebase first; --allow-stale-base gates on the old base with a WARN line.
    gates run --run --block --changed: the Go test selection (the block's tests, tests that import a changed file); never the full suite; an empty selection skips the test gate.
    gates run --run --block --full: the only local full suite of a block (scope full, reason explicit); cannot go with --changed.
    gates run --run --block: a full suite in the shared checkout while other blocks of the run are open there prints 'full suite in a shared tree (N other open blocks)' and records shared_tree: N on gate.done; use --changed for the block's tests, or proof.isolation: worktree.
    gates run --run --block: the close-blocker preflight runs first by default; --no-preflight skips it, --preflight asks for it.

Available commands:
  gates acceptance       Check each acceptance clause has a passing test
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`gates run`](#gates-run)
- [`gates detect`](#gates-detect)

### gates detect

Find the test, lint, type and format commands

```text
usage: code-forge gates detect [--cwd <dir>]
```

#### When to use

See which test, lint, type and format commands code-forge found for the project.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |

#### Examples

```sh
code-forge gates detect
```

Exit 0. Output:

```text
{"stack":"unknown","test":["sh","test/a.test.sh"],"lint":null,"types":null,"format":null,"rerun_failed":1}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`gates run`](#gates-run)
- [`init`](#init)

### gates run

Run the gates for a block or the project

```text
usage: code-forge gates run [--cwd <dir>] [--timeout-ms <n>] [--slug <slug>] [--run <id>] [--block <id>] [--changed | --full] [--auto-rebase | --allow-stale-base] [--preflight | --no-preflight]
```

#### When to use

Run the test, lint, type and format gates, for a block or the whole project.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--timeout-ms <n>` | number | not set | Longest run time per gate, in milliseconds. |
| `--slug <slug>` | string | not set | Project slug (from .code-forge.yml). |
| `--run <id>` | string | not set | Run id (r-...). |
| `--block <id>` | string | not set | Block id. |
| `--changed` | bool | off | Run only the tests the change touches. |
| `--full` | bool | off | Run the full test suite. |
| `--auto-rebase` | bool | off | Rebase the block onto main first when its base is behind. |
| `--allow-stale-base` | bool | off | Run on a base behind main without rebasing. |
| `--preflight` | bool | off | Run the preflight checks first. |
| `--no-preflight` | bool | off | Skip the preflight checks. |

#### Examples

```sh
code-forge gates run --run <run> --block B1
```

Exit 0. Output:

```text
{"stack":"unknown","results":[{"gate":"test","duration_ms":0,"argv":["sh","test/a.test.sh"],"skipped":false,"ok":true,"code":0,"stdout":"","stderr":""},{"gate":"lint","duration_ms":0,"argv":null,"skipped":true,"ok":true,"code":null,"stdout":"","stderr":""},{"gate":"types","duration_ms":0,"argv":null…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | A gate failed. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`gates detect`](#gates-detect)
- [`block close`](#block-close)
- [`queue run`](#queue-run)

### gates secret-scan

Check files for secrets

```text
usage: code-forge gates secret-scan [--cwd <dir>] --file <path> [--file <path>...]
```

#### When to use

Check files for secrets before they are committed.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--file <path>` | path | not set | File path inside the repository. |

#### Examples

```sh
code-forge gates secret-scan --file src/a.txt
```

Exit 0. Output:

```text
{"hits":[],"count":0}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`gates run`](#gates-run)

### gates safe-edit

Check the change keeps protected files safe

```text
usage: code-forge gates safe-edit [--cwd <dir>] --base <sha>
```

#### When to use

Check that the change since a base deletes no tests and weakens no checks.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--base <sha>` | string | not set | Base commit or ref to compare against. |

#### Examples

```sh
code-forge gates safe-edit --base <sha>
```

Exit 0. Output:

```text
{"ok":true}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`gates scope`](#gates-scope)
- [`block close`](#block-close)

### gates scope

Check the change stays inside the owned files

```text
usage: code-forge gates scope [--cwd <dir>] --base <sha>
```

#### When to use

List the files changed since a base, to compare with the owned files.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--base <sha>` | string | not set | Base commit or ref to compare against. |

#### Examples

```sh
code-forge gates scope --base <sha>
```

Exit 0. Output:

```text
{"tracked":[],"untracked":[],"all":[]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`block claim`](#block-claim)
- [`gates safe-edit`](#gates-safe-edit)

### gates acceptance

Check each acceptance clause has a passing test

```text
usage: code-forge gates acceptance --clauses <file.json> --tap <file>
```

#### When to use

Check that each acceptance clause has a passing test in a TAP run.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--clauses <file.json>` | path | not set | JSON file with the acceptance clauses. |
| `--tap <file>` | path | not set | TAP output of the test run. |

#### Examples

```sh
code-forge gates acceptance --clauses plans/c.json --tap plans/t.tap
```

Exit 0. Output:

```text
{"ok":true}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`block amend-acceptance`](#block-amend-acceptance)
- [`block close`](#block-close)

### gates transcript-grep

Check a coder transcript for forbidden commands

```text
usage: code-forge gates transcript-grep --file <path> [--cwd <dir>]
```

#### When to use

Check a coder transcript for forbidden commands.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--file <path>` | path | not set | File path inside the repository. |
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |

#### Examples

```sh
code-forge gates transcript-grep --file plans/t.txt
```

Exit 0. Output:

```text
{"ok":true,"hits":[]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`block accept-hit`](#block-accept-hit)
- [`block close`](#block-close)

## heal

Retry failures that pass by themselves

```text
usage: code-forge heal list [--run R] [--json] | show <id> [--run R] [--json] | run --run R [--once|--watch] [--max-tries N] [--json]
```

#### When to use

Retry failures that tend to pass on their own, such as a flaky review.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge heal --help
```

Exit 0. Output:

```text
Description:
  Retry failures that pass by themselves

Usage:
  usage: code-forge heal list [--run R] [--json] | show <id> [--run R] [--json] | run --run R [--once|--watch] [--max-tries N] [--json]

Available commands:
  heal list  List the heal queue
  heal run   Retry the heal items of a run
  heal show  Show one heal item

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`heal list`](#heal-list)
- [`heal run`](#heal-run)

### heal list

List the heal queue

```text
usage: code-forge heal list [--run R] [--json]
```

#### When to use

See the items waiting for a retry.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run R` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge heal list --json
```

Exit 0. Output:

```text
[]
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints JSON: an array (empty in the example).

#### Related

- [`heal show`](#heal-show)
- [`heal run`](#heal-run)

### heal show

Show one heal item

```text
usage: code-forge heal show <id> [--run R] [--json]
```

#### When to use

Read one heal item and its tries.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run R` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge heal show h-missing --json
```

Exit 1. Output:

```text
heal: no item h-missing
next: code-forge heal list
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`heal list`](#heal-list)
- [`heal run`](#heal-run)

### heal run

Retry the heal items of a run

```text
usage: code-forge heal run --run R [--once|--watch] [--max-tries N] [--json]
```

#### When to use

Retry the heal items of a run now, once or while watching.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run R` | string | not set | Run id (r-...). |
| `--once` | bool | off | Run one pass and exit. |
| `--watch` | bool | off | Keep running and retry as items arrive. |
| `--max-tries N` | number | not set | Most retries per item. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge heal run --run <run> --once --json
```

Exit 0. Output:

```text
{"healed":0,"needs_coder":0,"run":"<run>"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `healed` | number |
| `needs_coder` | number |
| `run` | string |

#### Related

- [`heal list`](#heal-list)
- [`heal show`](#heal-show)

## help

Show the commands, or the help of one command

```text
usage: code-forge help [<command> [<sub-command>]] [--json]
```

#### When to use

List the commands, or read one command's description and usage.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge help block open
```

Exit 0. Output:

```text
Description:
  Start a block: owned files, acceptance tests, brief

Usage:
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

…
```

```sh
code-forge help --json
```

Exit 0. Output:

```text
[{"name":"audit","summary":"Audit every command before a major release","owner":false,"group":"audit"},{"name":"audit commands","summary":"Run every command in a throwaway HOME and write the audit report","owner":false,"group":"audit"},{"name":"author","summary":"Write or harden a plan draft from a …
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an array of objects with these keys.

| Key | Type |
|---|---|
| `name` | string |
| `summary` | string |
| `owner` | boolean |
| `group` | string |

#### Related

- [`version`](#version)
- [`doctor`](#doctor)

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

#### When to use

Once per project: write .code-forge.yml and install the skill.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--no-interaction` | bool | off | Never prompt; use flags and defaults. |
| `--tools recommended\|current` | enum | not set | Which tools to set up. |
| `--yes-tool <tool>` | string | not set | Install this tool without asking. |
| `--harness a,b` | list | not set | Agent harnesses to install the skill for. |
| `--copy` | bool | off | Copy the skill instead of linking it. |
| `--provider P` | string | not set | Model provider. |
| `--level Ln=model[:effort][@provider]` | string | not set | Level (L0 to L3) that picks the model. |
| `--refresh-models` | bool | off | Refresh the model list during setup. |
| `--multimodel on\|off` | enum | not set | Use a second provider for reviews. |
| `--second-provider P` | string | not set | Second provider for reviews. |
| `--jev-ref <item-id\|link\|op://…>` | list | not set | 1Password reference of the Jev key. |
| `--jev-env NAME` | string | not set | Environment variable that holds the Jev key. |
| `--no-jev` | bool | off | Set up without a Jev key. |
| `--engine auto\|solo\|harness` | enum | not set | Engine that runs the coders. |
| `--solo-project N` | number | not set | Solo project number. |
| `--coder-may-push yes\|no` | enum | not set | Let coders push their branch. |
| `--coder-may-open-draft-pr yes\|no` | enum | not set | Let coders open draft pull requests. |
| `--gate name=cmd` | string | not set | Gate command, as name=command. |
| `--proof isolation=export\|lock` | enum | not set | Proof file or folder for the owner's pass. |
| `--skip-doctor` | bool | off | Do not run doctor at the end. |

#### Examples

```sh
code-forge init --no-interaction --provider anthropic --no-jev --skip-doctor
```

Exit 1. Output:

```text
code-forge: init is not supported in this case yet (init-links). Please report it: code-forge logs report
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`doctor`](#doctor)
- [`validate`](#validate)
- [`upgrade`](#upgrade)

## jev

Ask Jev a recorded decision question

```text
usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]
```

#### When to use

Ask Jev, the System 1 judge, a fixed question.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge jev --help
```

Exit 0. Output:

```text
Description:
  Ask Jev a recorded decision question

Usage:
  usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]

Available commands:
  jev ask  Ask Jev one decision question

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`jev ask`](#jev-ask)
- [`keys test`](#keys-test)

### jev ask

Ask Jev one decision question

```text
usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]
```

#### When to use

Get a quick yes/no style judgment from Jev on a known question id.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--state <file>` | path | not set | JSON file with the state for the question. |
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--slug <slug>` | string | not set | Project slug (from .code-forge.yml). |
| `--key-ref <ref>` | string | not set | Reference of the Jev key to use. |
| `--block <id>` | string | not set | Block id. |
| `--plan <file>` | path | not set | Plan file. |
| `--rules` | bool | off | Use the rules only; no Jev call. |

#### Examples

```sh
code-forge jev ask q1 --state plans/st.json
```

Exit 2. Output:

```text
jev ask: unknown question id "q1"
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`keys test`](#keys-test)
- [`resolve`](#resolve)

## keys

Store, list, test or remove credential references

```text
usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>
```

#### When to use

Store, test and remove the API keys code-forge uses.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge keys --help
```

Exit 0. Output:

```text
Description:
  Store, list, test or remove credential references

Usage:
  usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>

Available commands:
  keys list    List the stored credential references
  keys remove  Remove a stored credential reference
  keys set     Store a credential reference
  keys test    Check a credential reference works

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`keys list`](#keys-list)
- [`keys set`](#keys-set)

### keys list

List the stored credential references

```text
usage: code-forge keys list
```

#### When to use

See which keys are stored, where, and when they expire.

#### Flags

None.

#### Examples

```sh
code-forge keys list
```

Exit 0. Output:

```text
NAME	SOURCE	BACKEND	EXPIRES
WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`keys set`](#keys-set)
- [`keys test`](#keys-test)

### keys set

Store a credential reference

```text
usage: code-forge keys set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]]
```

#### When to use

Store a key, typed at a prompt or read from 1Password.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--op <item-id\|link\|op://ref>` | enum | not set | Read the value from 1Password (item id, link or op:// ref). |
| `--cache-hours <1-24>` | number | not set | Hours to keep the 1Password value cached (1-24). |

#### Examples

```sh
code-forge keys set jev --op op://vault/item/field
```

Exit 1. Output:

```text
1Password may ask you to approve access: check the 1Password window (Touch ID or password).
WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store
jev: 1Password CLI failed (exit 1); run `op read op://vault/item/field` yourself to see why
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |
| 13 | No terminal to read the hidden value. |

#### Related

- [`keys test`](#keys-test)
- [`keys remove`](#keys-remove)

### keys test

Check a credential reference works

```text
usage: code-forge keys test <name> [--ref <ref>]
```

#### When to use

Check that a stored key can be read.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--ref <ref>` | string | not set | Reference to test instead of the stored one. |

#### Examples

```sh
code-forge keys test jev
```

Exit 1. Output:

```text
WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store
jev: not found
next: code-forge keys set jev
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`keys set`](#keys-set)
- [`doctor`](#doctor)

### keys remove

Remove a stored credential reference

```text
usage: code-forge keys remove <name>
```

#### When to use

Delete a stored key from every backend.

#### Flags

None.

#### Examples

```sh
code-forge keys remove jev
```

Exit 0. Output:

```text
removed jev (0 backends)
WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`keys list`](#keys-list)
- [`keys set`](#keys-set)

## land

Landing code that block land uses

```text
usage: code-forge land
  Used internally by code-forge block land; this module has no standalone handler.
```

#### When to use

Internal: the landing code block land uses. Run block land instead.

#### Flags

None.

#### Examples

```sh
code-forge land --help
```

Exit 0. Output:

```text
Description:
  Landing code that block land uses

Usage:
  usage: code-forge land
    Used internally by code-forge block land; this module has no standalone handler.

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`block land`](#block-land)

## ledger

Read the ledger or record outcomes and spend

```text
usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

#### When to use

Read and add to the signed spend and outcome ledger.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge ledger --help
```

Exit 0. Output:

```text
Description:
  Read the ledger or record outcomes and spend

Usage:
  usage: code-forge ledger calibration|outcome|tail --slug <slug> [...]  (tail: [--n|-n <count>]) | add coder --run <r> --block <b> --usd <n> [--note "..."] | outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...

Available commands:
  ledger add             Add coder spend that code-forge did not see
  ledger calibration     Show how reliable the recorded answers were
  ledger outcome         Record the CI result of a pull request
  ledger outside-review  Record an outside review of a pull request
  ledger tail            Print the last ledger rows

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`ledger tail`](#ledger-tail)
- [`report`](#report)

### ledger calibration

Show how reliable the recorded answers were

```text
usage: code-forge ledger calibration --slug <slug> [--question <id>]
```

#### When to use

Check how well the System 1 answers matched the real outcomes.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--slug <slug>` | string | not set | Project slug (from .code-forge.yml). |
| `--question <id>` | string | not set | Question text or id. |

#### Examples

```sh
code-forge ledger calibration --slug demo
```

Exit 0. Output:

```text
{
  "buckets": {
    "0.50-0.60": {
      "correct": 0,
      "wrong": 0,
      "unknown": 0
    },
    "0.60-0.70": {
      "correct": 0,
      "wrong": 0,
      "unknown": 0
    },
    "0.70-0.80": {
      "correct": 0,
      "wrong": 0,
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`ledger outcome`](#ledger-outcome)
- [`report`](#report)

### ledger outcome

Record the CI result of a pull request

```text
usage: code-forge ledger outcome --scan-git --slug <slug> [--cwd <dir>] [--days <n>]
usage: code-forge ledger outcome --pr <n> --ci red|green|reverted --slug <slug>
```

#### When to use

Record what happened to landed work: CI result of a PR, or reverts found in git.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--scan-git` | bool | off | Mark landed blocks that were reverted in git. |
| `--slug <slug>` | string | not set | Project slug (from .code-forge.yml). |
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--days <n>` | number | not set | Number of days to look back. |
| `--pr <n>` | number | not set | Pull request number. |
| `--ci red\|green\|reverted` | enum | not set | CI result of the pull request. |

#### Examples

```sh
code-forge ledger outcome --pr 1 --ci green --slug demo
```

Exit 0. Output:

```text
recorded outcome for PR #1: green
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`ledger calibration`](#ledger-calibration)
- [`ci watch`](#ci-watch)

### ledger tail

Print the last ledger rows

```text
usage: code-forge ledger tail --slug <slug> [--n <count> | -n <count>]
```

#### When to use

Read the last ledger rows of a project.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--slug <slug>` | string | not set | Project slug (from .code-forge.yml). |
| `--n <count>` | number | not set | Number of rows. |

#### Examples

```sh
code-forge ledger tail --slug demo --n 1
```

Exit 0. Output:

```text
{"event":"outcome","outcome":"correct","outcome_source":"ci","pr":1,"ts":"2026-01-01T00:00:00Z","tokens_source":null,"cost_source":null}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`report`](#report)
- [`ledger add`](#ledger-add)

### ledger add

Add coder spend that code-forge did not see

```text
usage: code-forge ledger add coder --run <r> --block <b> --usd <n> [--note "..."]
```

#### When to use

Record coder spend that code-forge did not measure.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--block <b>` | string | not set | Block id. |
| `--usd <n>` | number | not set | Amount in USD. |
| `--note "..."` | list | not set | Free text note. |

#### Examples

```sh
code-forge ledger add coder --run <run> --block B1 --usd 0.5 --note manual
```

Exit 0. Output:

```text
recorded coder spend $0.5000 for run <run> block B1 · run total $0.5000
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`ledger tail`](#ledger-tail)
- [`report`](#report)

### ledger outside-review

Record an outside review of a pull request

```text
usage: code-forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...
```

#### When to use

Record the result of a review done outside code-forge, such as on a pull request.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--pr <n>` | number | not set | Pull request number. |
| `--rounds <n>` | number | not set | Number of review rounds. |
| `--findings <n>` | number | not set | Number of findings. |
| `--link <url>` | string | not set | Link to show next to the entry. |
| `--block <id>` | string | not set | Block id. |

#### Examples

```sh
code-forge ledger outside-review --run <run> --pr 1 --rounds 1 --findings 0
```

Exit 0. Output:

```text
outside_review pr 1 · rounds 1 · findings 0 · blocks -
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`ledger tail`](#ledger-tail)
- [`report`](#report)

## list

List the skill installations and harness paths

```text
usage: code-forge list
```

#### When to use

See where the code-forge skill is installed and whether each link resolves.

#### Flags

None.

#### Examples

```sh
code-forge list
```

Exit 0. Output:

```text
INSTALLED
HARNESS	SCOPE	METHOD	TARGET	RESOLVES
(none)

HARNESSES
ID	LABEL	DETECTED	VERIFIED	PROJECT PATH
claude	Claude Code	yes	yes	.claude/skills
codex	Codex CLI	yes	yes	.agents/skills
grok	Grok CLI	yes	yes	.agents/skills
gemini	Gemini CLI	yes	UNVERIFIED	.agents/skills
cursor	Cursor	no	yes	.agents/skills
copilot	Copilot	no	yes	.agents/skills
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`init`](#init)
- [`remove`](#remove)
- [`upgrade`](#upgrade)

## logs

Show the last local CLI errors

```text
usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path
       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

#### When to use

Read the local log of CLI errors.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--last N` | number | not set | Number of most recent errors. |
| `--json` | bool | off | Print JSON instead of text. |
| `--days N` | number | not set | Number of days to look back. |
| `--yes` | bool | off | Answer yes to the confirmation. |
| `--kind K` | string | not set | Kind of item. |
| `--verb V` | string | not set | Only errors of this verb. |
| `--note "text"` | string | not set | Free text note. |
| `--include-warnings` | bool | off | Include warnings, not only errors. |
| `--with-doctor` | bool | off | Attach the doctor output. |
| `--no-ai` | bool | off | Do not ask an agent to summarize the errors. |
| `--allow-old` | bool | off | Include errors older than the default window. |
| `--force` | bool | off | Report even when the same errors were reported before. |
| `--dry-run` | bool | off | Show what would happen; change nothing. |

#### Examples

```sh
code-forge logs --json
```

Exit 0. Output:

```text
{"errors":[]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `errors` | array (empty in the example) |

#### Related

- [`logs summary`](#logs-summary)
- [`logs report`](#logs-report)

### logs summary

Count the CLI errors of the last days

```text
usage: code-forge logs summary [--days N] [--json]
```

#### When to use

Count the CLI errors of the last days by kind.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--days N` | number | not set | Number of days to look back. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge logs summary --json
```

Exit 0. Output:

```text
{"days":30,"counts":[],"warnings":[]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `days` | number |
| `counts` | array (empty in the example) |
| `warnings` | array (empty in the example) |

#### Related

- [`logs`](#logs)
- [`logs report`](#logs-report)

### logs clear

Delete the local CLI error log

```text
usage: code-forge logs clear [--yes]
```

#### When to use

Delete the local CLI error log.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--yes` | bool | off | Answer yes to the confirmation. |

#### Examples

```sh
code-forge logs clear --yes
```

Exit 0. Output:

```text
no errors logged
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`logs`](#logs)
- [`logs path`](#logs-path)

### logs path

Print the path of the CLI error log

```text
usage: code-forge logs path
```

#### When to use

Find the CLI error log file.

#### Flags

None.

#### Examples

```sh
code-forge logs path
```

Exit 0. Output:

```text
~/.code-forge/logs/errors.jsonl
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`logs`](#logs)
- [`logs clear`](#logs-clear)

### logs report

Report CLI errors as a GitHub issue

```text
usage: code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
```

#### When to use

Send the logged CLI errors to the maintainers as a GitHub issue.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--last N` | number | not set | Number of most recent errors. |
| `--kind K` | string | not set | Kind of item. |
| `--verb V` | string | not set | Only errors of this verb. |
| `--note "text"` | string | not set | Free text note. |
| `--include-warnings` | bool | off | Include warnings, not only errors. |
| `--with-doctor` | bool | off | Attach the doctor output. |
| `--no-ai` | bool | off | Do not ask an agent to summarize the errors. |
| `--allow-old` | bool | off | Include errors older than the default window. |
| `--force` | bool | off | Report even when the same errors were reported before. |
| `--dry-run` | bool | off | Show what would happen; change nothing. |
| `--yes` | bool | off | Answer yes to the confirmation. |

#### Examples

```sh
code-forge logs report --dry-run --no-ai
```

Exit 0. Output:

```text
no errors logged
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`logs`](#logs)
- [`logs summary`](#logs-summary)

## models

List the provider model catalogs

```text
usage: code-forge models [--refresh --from-cli-caches]
```

#### When to use

See the models each provider offers, to pick levels in the config.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--refresh` | bool | off | Refresh the model list. |
| `--from-cli-caches` | bool | off | Read the model lists from the provider CLIs' caches. |

#### Examples

```sh
code-forge models
```

Exit 0. Output:

```text
[code-forge:info] anthropic:
[code-forge:info]   claude-haiku-4-5-20251001
[code-forge:info]   claude-sonnet-5
[code-forge:info]   claude-opus-5-5
[code-forge:info]   claude-fable-5-1
[code-forge:info]   claude-fable-5-2
[code-forge:info] openai:
[code-forge:info]   gpt-6-luna
[code-forge:info]   gpt-6-sol
[code-forge:info]   gpt-6-astra
[code-forge:info] xai:
[code-forge:info]   grok-4.7
[code-forge:info]   grok-4.7-build-fast
[code-forge:info]   grok-4.6
[code-forge:info]   grok-4.5
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`resolve`](#resolve)
- [`init`](#init)

## native-status

Daemon companion for run status

```text
usage: code-forge native-status
  Internal daemon companion: reads JSON records and rows from standard input.
```

#### When to use

Internal: the daemon side of run status. Run run status instead.

#### Flags

None.

#### Examples

```sh
code-forge native-status --help
```

Exit 0. Output:

```text
Description:
  Daemon companion for run status

Usage:
  usage: code-forge native-status
    Internal daemon companion: reads JSON records and rows from standard input.

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`run status`](#run-status)

## outcome

Seal, check or record a specialist outcome file

```text
usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]
```

#### When to use

Seal, check and record the outcome.json a skill step returns.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge outcome --help
```

Exit 0. Output:

```text
Description:
  Seal, check or record a specialist outcome file

Usage:
  usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]

Available commands:
  outcome check   Check an outcome file and its files
  outcome record  Check an outcome and save it in the ledger
  outcome seal    Fill the file hashes of an outcome file

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`outcome seal`](#outcome-seal)
- [`outcome check`](#outcome-check)
- [`outcome record`](#outcome-record)

### outcome seal

Fill the file hashes of an outcome file

```text
usage: code-forge outcome seal <outcome.json> [--root <dir>]
```

#### When to use

After a skill step: add the file hashes to its outcome.json.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--root <dir>` | path | not set | Project root (default: the current directory). |

#### Examples

```sh
code-forge outcome seal plans/outcome.json
```

Exit 0. Output:

(none)

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`outcome check`](#outcome-check)
- [`outcome record`](#outcome-record)

### outcome check

Check an outcome file and its files

```text
usage: code-forge outcome check <outcome.json> [--root <dir>] [--json]
```

#### When to use

Check an outcome.json: its shape and that its files still match their hashes.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--root <dir>` | path | not set | Project root (default: the current directory). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge outcome check plans/outcome.json --json
```

Exit 0. Output:

```text
{"ok":true,"problems":[]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `ok` | boolean |
| `problems` | array (empty in the example) |

#### Related

- [`outcome seal`](#outcome-seal)
- [`outcome record`](#outcome-record)

### outcome record

Check an outcome and save it in the ledger

```text
usage: code-forge outcome record <outcome.json> [--root <dir>] [--step <id>] [--json]
```

#### When to use

Record a checked outcome as done for a pipeline step.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--root <dir>` | path | not set | Project root (default: the current directory). |
| `--step <id>` | string | not set | Pipeline step id. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge outcome record plans/outcome.json --step s1 --json
```

Exit 1. Output:

```text
{"ok":false,"problems":["no project run to sign the outcome; start a run first"]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `ok` | boolean |
| `problems` | array |

#### Related

- [`outcome check`](#outcome-check)
- [`pipeline next`](#pipeline-next)

## pipeline

Show the steps of a pipeline

```text
usage: code-forge pipeline status|next [--json] [--root <dir>]
```

#### When to use

Follow a skill pipeline (code-forge.pipeline.yml) step by step.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge pipeline --help
```

Exit 0. Output:

```text
Description:
  Show the steps of a pipeline

Usage:
  usage: code-forge pipeline status|next [--json] [--root <dir>]

Available commands:
  pipeline next    List the pipeline steps ready to run
  pipeline status  Show the state of each pipeline step

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`pipeline status`](#pipeline-status)
- [`pipeline next`](#pipeline-next)

### pipeline status

Show the state of each pipeline step

```text
usage: code-forge pipeline status [--json] [--root <dir>]
```

#### When to use

See which pipeline steps are done and which wait.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |
| `--root <dir>` | path | not set | Project root (default: the current directory). |

#### Examples

```sh
code-forge pipeline status --json
```

Exit 0. Output:

```text
{"exists":false}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `exists` | boolean |

#### Related

- [`pipeline next`](#pipeline-next)
- [`outcome record`](#outcome-record)

### pipeline next

List the pipeline steps ready to run

```text
usage: code-forge pipeline next [--json] [--root <dir>]
```

#### When to use

Find the next pipeline step to run.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |
| `--root <dir>` | path | not set | Project root (default: the current directory). |

#### Examples

```sh
code-forge pipeline next --json
```

Exit 0. Output:

```text
{"exists":false}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `exists` | boolean |

#### Related

- [`pipeline status`](#pipeline-status)
- [`outcome record`](#outcome-record)

## plan

Check a plan before coding

```text
usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]
```

#### When to use

Check a plan before a run uses it.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge plan --help
```

Exit 0. Output:

```text
Description:
  Check a plan before coding

Usage:
  usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]

Available commands:
  plan check  Check a plan against its facts and rules

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`plan check`](#plan-check)
- [`author`](#author)

### plan check

Check a plan against its facts and rules

```text
usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]
```

#### When to use

Before run start: check that a plan's block table is complete and consistent.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--facts <sheet>` | string | not set | Facts sheet file. |
| `--slug <slug>` | string | not set | Project slug (from .code-forge.yml). |

#### Examples

```sh
code-forge plan check plans/brief.md
```

Exit 1. Output:

```text
no block table found (a table with id, owned_files and acceptance columns)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`author`](#author)
- [`run start`](#run-start)

## proof

Prove the tests: tiers, exports and red-green

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
       code-forge proof restore <block> --run <r>
       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

#### When to use

Prove that a block's tests test its change.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge proof --help
```

Exit 0. Output:

```text
Description:
  Prove the tests: tiers, exports and red-green

Usage:
  usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
         code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>
         code-forge proof restore <block> --run <r>
         code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]

Available commands:
  proof export     Export a block's tests to a clean copy
  proof lock       Lock a block's test files
  proof red-green  Prove a test fails without the fix
  proof restore    Put back the files a proof changed
  proof tier       Show the proof tier of a file
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`proof red-green`](#proof-red-green)
- [`proof tier`](#proof-tier)

### proof tier

Show the proof tier of a file

```text
usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
```

#### When to use

Find how strict the proof must be for a file.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--file <path>` | path | not set | File path inside the repository. |
| `--risk <0-3>` | number | not set | Risk level of the change (0-3). |
| `--security` | bool | off | The file touches security. |
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |

#### Examples

```sh
code-forge proof tier --file src/a.txt --risk 1
```

Exit 0. Output:

```text
{"file":"src/a.txt","tier":"light","reason":"default"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`proof red-green`](#proof-red-green)

### proof export

Export a block's tests to a clean copy

```text
usage: code-forge proof export <block> --run <r> [--remove]
```

#### When to use

Copy a block's tests to a clean checkout of its base, for a red run.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--remove` | bool | off | Remove the export instead of making it. |

#### Examples

```sh
code-forge proof export B1 --run <run>
```

Exit 0. Output:

```text
{"dir":"<repo>/.code-forge/export/B1","base":"<sha>","restored":[],"untracked":[],"owned":["src/a.txt","test/a.test.sh"],"removed":[],"linked":[],"copied":[]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`proof red-green`](#proof-red-green)
- [`proof restore`](#proof-restore)

### proof lock

Lock a block's test files

```text
usage: code-forge proof lock <block> --run <r>
```

#### When to use

Hold the block's proof lock while you change files for a red run.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |

#### Examples

```sh
code-forge proof lock B1 --run <run>
```

Exit 0. Output:

```text
{"locked":{"block":"B1","acquired_at":"2026-01-01T00:00:00Z"}}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`proof unlock`](#proof-unlock)
- [`proof red-green`](#proof-red-green)

### proof unlock

Unlock a block's test files

```text
usage: code-forge proof unlock <block> --run <r>
```

#### When to use

Release the block's proof lock.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |

#### Examples

```sh
code-forge proof unlock B1 --run <run>
```

Exit 0. Output:

```text
{"unlocked":"B1"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`proof lock`](#proof-lock)

### proof restore

Put back the files a proof changed

```text
usage: code-forge proof restore <block> --run <r>
```

#### When to use

Put back the files a red run changed, after a crash.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |

#### Examples

```sh
code-forge proof restore B1 --run <run>
```

Exit 1. Output:

```text
proof restore: no red→green journal for block B1 — nothing to restore
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`proof red-green`](#proof-red-green)
- [`proof export`](#proof-export)

### proof red-green

Prove a test fails without the fix

```text
usage: code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
```

#### When to use

Show a test fails without the change and passes with it.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <r>` | string | not set | Run id (r-...). |
| `--test <file[::case]>` | path | not set | Test file, optionally ::case. |
| `--mechanism revert\|assertion-deletion` | enum | not set | How the test is made red: revert the code or delete the assertion. |

#### Examples

```sh
code-forge proof red-green B1 --run <run> --test test/a.test.sh
```

Exit 1. Output:

```text
proof red-green: only node:test files (.mjs, .cjs, .js) have a filtered test command in this version — add a proof.runners entry for it (see proof.md §2)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`proof export`](#proof-export)
- [`proof tier`](#proof-tier)

## queue

Show the machine-wide test queue

```text
usage: code-forge queue [status] [--json]
       code-forge queue run [--kind full|changed] [--label X] -- <cmd> [args]
  The machine-wide FIFO test queue ($HOME/.code-forge/queue). Gates take a slot by themselves;
  wrap any other test command in `queue run`. CODE_FORGE_TEST_QUEUE=off disables it.
```

#### When to use

See the machine-wide test queue that keeps test runs from overlapping.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |
| `--kind full\|changed` | enum | not set | Kind of item. |
| `--label X` | string | not set | Label shown in the queue. |

#### Examples

```sh
code-forge queue --json
```

Exit 0. Output:

```text
{"holders":[],"waiters":[],"changed_slots":1}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `holders` | array (empty in the example) |
| `waiters` | array (empty in the example) |
| `changed_slots` | number |

#### Related

- [`queue run`](#queue-run)
- [`gates run`](#gates-run)

### queue status

Show the test queue (the same as queue)

```text
usage: code-forge queue [status] [--json]
       code-forge queue run [--kind full|changed] [--label X] -- <cmd> [args]
  The machine-wide FIFO test queue ($HOME/.code-forge/queue). Gates take a slot by themselves;
  wrap any other test command in `queue run`. CODE_FORGE_TEST_QUEUE=off disables it.
```

#### When to use

See who holds a test slot and who waits.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |
| `--kind full\|changed` | enum | not set | Kind of item. |
| `--label X` | string | not set | Label shown in the queue. |

#### Examples

```sh
code-forge queue status --json
```

Exit 0. Output:

```text
{"holders":[],"waiters":[],"changed_slots":1}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `holders` | array (empty in the example) |
| `waiters` | array (empty in the example) |
| `changed_slots` | number |

#### Related

- [`queue run`](#queue-run)

### queue run

Run a test command in a queue slot

```text
usage: code-forge queue [status] [--json]
       code-forge queue run [--kind full|changed] [--label X] -- <cmd> [args]
  The machine-wide FIFO test queue ($HOME/.code-forge/queue). Gates take a slot by themselves;
  wrap any other test command in `queue run`. CODE_FORGE_TEST_QUEUE=off disables it.
```

#### When to use

Run a test command that is not a gate inside a queue slot.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |
| `--kind full\|changed` | enum | not set | Kind of item. |
| `--label X` | string | not set | Label shown in the queue. |

#### Examples

```sh
code-forge queue run -- true
```

Exit 0. Output:

(none)

```sh
code-forge queue run -- false
```

Exit 1. Output:

(none)

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | The command's own exit code (shown here for a failing command). |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`queue status`](#queue-status)
- [`gates run`](#gates-run)

## release

Check a project is ready to release

```text
usage: code-forge release check [--cwd <dir>] [--json]
```

#### When to use

Before you cut a release: ask whether this project is ready, in one read-only command.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge release --help
```

Exit 0. Output:

```text
Description:
  Check a project is ready to release

Usage:
  usage: code-forge release check [--cwd <dir>] [--json]

Available commands:
  release check  Check tree, push, CI, CHANGELOG and npm ls; print the bump and steps

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`release check`](#release-check)
- [`status`](#status)
- [`doctor`](#doctor)

### release check

Check tree, push, CI, CHANGELOG and npm ls; print the bump and steps

```text
usage: code-forge release check [--cwd <dir>] [--json]
```

#### When to use

Before `npm run release`: check the tree is clean, HEAD is pushed, GitHub CI is green for the HEAD sha, CHANGELOG [Unreleased] has one heading per kind and `npm ls` is clean; it suggests the bump and prints the release steps in order. It only reads; it never releases.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cwd <dir>` | path | not set | Project folder to check (default: the current folder); the check runs at its git top level. |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge release check --json
```

Exit 1. Output:

```text
{"ready":false,"root":"<repo>","branch":"main","head":"<sha>","package":"","current":"","bump":"patch","bump_reason":"no CHANGELOG.md","next":"","checks":[{"name":"tree","status":"fail","detail":"the working tree is not clean (1 changed):  M plans/outcome.json","fix":"commit or remove the changes, t…
```

```sh
code-forge release check
```

Exit 1. Output:

```text
release check: <repo> (branch HEAD, HEAD HEAD)
ok    tree      working tree is clean
FAIL  pushed    HEAD is detached
                fix: check out the release branch (git switch main)
FAIL  ci        gh run list failed (exit 1): no output
                fix: check gh auth status and that origin is a GitHub repository
skip  changelog no CHANGELOG.md: the bump is patch by default
skip  npm-ls    no package.json
bump: patch; no CHANGELOG.md
steps, in order, once every check passes:
  1. git tag -a v<x.y.z> -m "Release v<x.y.z>"
  2. git push origin HEAD --follow-tags
NOT READY: 2 check(s) failed: pushed, ci
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Not ready: at least one check failed; each failing check names what to do (for example push HEAD, wait for CI, npm prune). |
| 2 | Usage error: no check sub-command, an unknown or repeated option, or --cwd is not a folder. |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `ready` | boolean |
| `root` | string |
| `branch` | string |
| `head` | string |
| `package` | string |
| `current` | string |
| `bump` | string |
| `bump_reason` | string |
| `next` | string |
| `checks` | array |
| `failed` | array |
| `steps` | array |

#### Related

- [`status`](#status)
- [`doctor`](#doctor)
- [`audit commands`](#audit-commands)

## remove

Remove skill installations

```text
usage: code-forge remove [<harness>] [--scope project|global]
```

#### When to use

Uninstall the code-forge skill from a harness.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--scope project\|global` | enum | not set | Scope of the question or install. |

#### Examples

```sh
code-forge remove --help
```

Exit 0. Output:

```text
Description:
  Remove skill installations

Usage:
  usage: code-forge remove [<harness>] [--scope project|global]

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`list`](#list)
- [`init`](#init)

## report

Show or export the project ledger report

```text
usage: code-forge report --slug <slug> [--json] [--export <dir>]
```

#### When to use

Read a project's spend, reviews and outcomes from its ledger.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--slug <slug>` | string | not set | Project slug (from .code-forge.yml). |
| `--json` | bool | off | Print JSON instead of text. |
| `--export <dir>` | path | not set | Write each report section to a file in this folder. |

#### Examples

```sh
code-forge report --slug demo --json
```

Exit 0. Output:

```text
{
  "cost_per_block": [
    {
      "run": "<run>",
      "block": "R-<id>",
      "level": "L2",
      "lane": null,
      "completed": false,
      "reviewUsd": 0.0134,
      "coderUsd": 0,
      "s1s2Usd": 0,
      "factsUsd": 0,
      "otherUsd": 0,
      "proofTimeMs": 0,
      "wallMs": 0,
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints JSON over several lines (pretty-printed): an object with these keys.

| Key | Type |
|---|---|
| `cost_per_block` | array |
| `lane_distribution` | array |
| `escalations` | array (empty in the example) |
| `s1_calls_per_block` | object |
| `s2_rate` | object |
| `review_budget` | object |
| `review_depth_and_findings` | object |
| `fix_rounds` | object |
| `review_unavailable` | object |
| `proof_time` | object |
| `forecast_vs_actual_lines` | array (empty in the example) |
| `blocks_stopped_at_l3` | array (empty in the example) |
| `run_stop_counts` | object |
| `outside_review` | array |
| `agent_waivers` | array (empty in the example) |
| `pace` | object |
| `run_time` | object |
| `specialist_outcomes` | object |

#### Related

- [`ledger tail`](#ledger-tail)
- [`status`](#status)

## resolve

Show the model, effort and CLI of a level

```text
usage: code-forge resolve <L0|L1|L2|L3>
```

#### When to use

See which provider, model and CLI a level uses.

#### Flags

None.

#### Examples

```sh
code-forge resolve L1
```

Exit 0. Output:

```text
{"provider":"anthropic","model":"claude-sonnet-5","effort":null,"fallback":[],"cli":"claude"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`models`](#models)
- [`spawn`](#spawn)

## review

Review changed files against intent or acceptance

```text
usage: code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent "<text>"] [--run <id>] [--max <seconds>] [--json] [--keep-run]
```

#### When to use

Review a change outside a run, against a base and an intent or acceptance file.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--base <ref>` | string | not set | Base commit or ref to compare against. |
| `--files <path…>` | list | not set | Files to review (default: the changed files). |
| `--acceptance <file>` | path | not set | Acceptance file: a JSON or YAML list of {clause, tests}. |
| `--intent "<text>"` | string | not set | What the change should do, in words. |
| `--run <id>` | string | not set | Run id (r-...). |
| `--max <seconds>` | number | not set | Longest wait (90s, 30m or 1500ms). |
| `--json` | bool | off | Print JSON instead of text. |
| `--keep-run` | bool | off | Keep the review run after it ends. |

#### Examples

```sh
code-forge review --base HEAD --intent nothing --json --max 5
```

Exit 1. Output:

```text
{"ok":false,"base":"<sha>","base_from":"HEAD","run":"<run>","block":"R-<id>","files":[{"file":"plans/outcome.json","result":"unavailable","reason":"exit","findings":[]}],"skipped":[],"totals":{"files":1,"approved":0,"unchanged":0,"with_findings":0,"findings":0,"stopped":0,"unavailable":1}}
review: base 45585b9cf037 (HEAD) · 1 file · run <run>
review: plans/outcome.json  unavailable: exit
review: plans/outcome.json  session failed (exit 1); the provider CLI printed nothing on stderr
next: code-forge doctor (checks each provider CLI and its login; code-forge logs --last 5 shows the error log)
review: run end exited 1: run end: kill EPERM
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `ok` | boolean |
| `base` | string |
| `base_from` | string |
| `run` | string |
| `block` | string |
| `files` | array |
| `skipped` | array (empty in the example) |
| `totals` | object |

#### Related

- [`review-file`](#review-file)
- [`gates scope`](#gates-scope)

## review-file

Queue a file review or wait for the results

```text
usage: code-forge review-file [--cwd <dir>] <path> --block <id> [--run <id>] [--fresh] | review-file --wait <ticket> [--max <seconds>s]
       code-forge review-file --wait-block <id> --run <id> [--max <seconds>s]
       code-forge review-file --rebase-check --block <id> --run <id>
       code-forge review-file <answering file> --block <id> --answers <id,id,…> --of <source> [--run <id>]
       --cwd <dir> applies to queueing and both wait modes
```

#### When to use

Queue one finished file for review, or wait for the reviews of a block.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--block <id>` | string | not set | Block id. |
| `--run <id>` | string | not set | Run id (r-...). |
| `--fresh` | bool | off | Ask for a new review even when one exists for this content. |
| `--wait <ticket>` | string | not set | Wait for this review ticket. |
| `--max <seconds>s` | number | not set | Longest wait (90s, 30m or 1500ms). |
| `--wait-block <id>` | string | not set | Wait for every review of this block. |
| `--rebase-check` | bool | off | Check whether reviews still hold after a rebase. |
| `--answers <id,id,…>` | list | not set | File with the answers to earlier questions. |
| `--of <source>` | string | not set | File the answers belong to. |

#### Examples

```sh
code-forge review-file src/a.txt --block B1 --run <run>
```

Exit 3. Output:

```text
{"status":"worker_down","file":"src/a.txt"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |
| 3 | No review worker runs for the run (status worker_down); start one with run start --reattach. |

#### Related

- [`block ready`](#block-ready)
- [`block land`](#block-land)
- [`worker`](#worker)

## run

Start, check, reload or end a run

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id> [--json] | human-checks list --run <id> [--json] | human-checks clear --run <id> --block <id> --clause <C<n>> --note <text> [--json]
```

#### When to use

Start, check, reload and end a run.

#### Flags

None of its own; see its sub-commands.

#### Examples

```sh
code-forge run --help
```

Exit 0. Output:

```text
Description:
  Start, check, reload or end a run

Usage:
  usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief] | start --add-worker --run <id> | start --reattach --run <id> [--worker-pid <pid>] | status --run <id> | reload --run <id> | end --run <id> [--json] | human-checks list --run <id> [--json…

Available commands:
  run end           End a run
  run human-checks  List the open human checks of a run, or clear one (owner)
  run reload        Load the changed settings into a run
  run start         Start a run, or add or reattach a worker
  run status        Show the state of a run and whether its worker is alive

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; the examples above refuse, so no shape is recorded.

#### Related

- [`run start`](#run-start)
- [`run status`](#run-status)
- [`run end`](#run-end)

### run start

Start a run, or add or reattach a worker

```text
usage: code-forge run start [--cwd <dir>] [--run <id>] [--engine <e>] [--worker-pid <pid>] [--mode plan|brief]
usage: code-forge run start --add-worker --run <id>
usage: code-forge run start --reattach --run <id> [--worker-pid <pid>]
```

#### When to use

Before any block: start a run and its review worker.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--run <id>` | string | not set | Run id (r-...). |
| `--engine <e>` | string | not set | Engine that runs the coders. |
| `--worker-pid <pid>` | number | not set | Pid of the worker that serves the run. |
| `--mode plan\|brief` | enum | not set | plan runs the whole pipeline; brief runs one brief. |
| `--add-worker` | bool | off | Add one more review worker to a live run. |
| `--reattach` | bool | off | Attach a new worker to a run whose worker stopped. |

#### Examples

```sh
code-forge run start
```

Exit 0. Output:

```text
run <run> started · engine auto · worker pid <pid>
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`run end`](#run-end)
- [`block open`](#block-open)
- [`status`](#status)

### run status

Show the state of a run and whether its worker is alive

```text
usage: code-forge run status --run <id>
```

#### When to use

Read the full record of one run.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |

#### Examples

```sh
code-forge run status --run <run>
```

Exit 0. Output:

```text
{
  "run_id": "<run>",
  "status": "active",
  "engine": "auto",
  "worker_pinned": true,
  "worker_live": false,
  "workers": [
    {
      "pid": <pid>,
      "alive": false,
      "started_at": "Thu Jan  1 00:00:00 2026"
    }
  ],
  "blocks": {},
  "orphans": [],
…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`status`](#status)
- [`run start`](#run-start)

### run reload

Load the changed settings into a run

```text
usage: code-forge run reload --run <id>
```

#### When to use

After you change .code-forge.yml during a run: apply it to the run.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |

#### Examples

```sh
code-forge run reload --run <run>
```

Exit 0. Output:

```text
run <run>: no config change
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`validate`](#validate)
- [`autopilot approve`](#autopilot-approve)

### run end

End a run

```text
usage: code-forge run end --run <id> [--json]
```

#### When to use

After the last block: end the run and stop its worker.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge run end --run <run> --json
```

Exit 0. Output:

```text
{"run":"<run>","ended":true,"ended_at":"2026-01-01T00:00:00Z"}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `run` | string |
| `ended` | boolean |
| `ended_at` | string |

#### Related

- [`run start`](#run-start)
- [`block stop`](#block-stop)
- [`report`](#report)

### run human-checks

List the open human checks of a run, or clear one (owner)

```text
usage: code-forge run human-checks list --run <id> [--json]
usage: code-forge run human-checks clear --run <id> --block <id> --clause <C<n>> --note <text> [--json]
```

#### When to use

List or clear the acceptance clauses only a human can check.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |
| `--block <id>` | string | not set | Block id. |
| `--clause <C<n>>` | string | not set | Acceptance clause id (C1, C2, ...). |
| `--note <text>` | string | not set | Free text note. |

#### Examples

```sh
code-forge run human-checks list --run <run> --json
```

Exit 0. Output:

```text
{"run":"<run>","open":[]}
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `run` | string |
| `open` | array (empty in the example) |

#### Related

- [`block human-pass`](#block-human-pass)
- [`block close`](#block-close)

## s2

Answer a decision packet with a System 2 session

```text
usage: code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]
```

#### When to use

Ask an L3 session (System 2) a hard question as a JSON packet.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--packet <file.json>` | path | not set | JSON packet with the question. |
| `--run <id>` | string | not set | Run id (r-...). |
| `--block <id>` | string | not set | Block id. |
| `--timeout <seconds>` | number | not set | Longest run time. |

#### Examples

```sh
code-forge s2 --packet plans/p.json --timeout 5
```

Exit 1. Output:

```text
level=L3 provider=anthropic model=claude-fable-5-2 effort=none fallback_step=0
s2: the provider CLI printed nothing on stderr
s2: failed (exit 1)
next: code-forge doctor (checks each provider CLI and its login; code-forge logs --last 5 shows the error log)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`jev ask`](#jev-ask)
- [`spawn`](#spawn)

## setup

Check the Go binary for this system

```text
usage: code-forge setup
```

#### When to use

After install: check that the Go binary for this system is in place.

#### Flags

None.

#### Examples

```sh
code-forge setup --help
```

Exit 0. Output:

```text
Description:
  Check the Go binary for this system

Usage:
  usage: code-forge setup

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`doctor`](#doctor)
- [`version`](#version)

## spawn

Run a model session at a level and role

```text
usage: code-forge spawn --level L0|L1|L2|L3 --role coder|reviewer|judge|s2|author|facts|delegate --brief <file> [--schema <file>] [--cwd <dir>] [--run <id>] [--block <id>] [--timeout <seconds>] [--background] [--print-argv]
```

#### When to use

Start a session at a level and in a role, with a brief.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--level L0\|L1\|L2\|L3` | enum | not set | Level (L0 to L3) that picks the model. |
| `--role coder\|reviewer\|judge\|s2\|author\|facts\|delegate` | enum | not set | Role of the session. |
| `--brief <file>` | path | not set | Brief file inside the repository. |
| `--schema <file>` | path | not set | JSON schema the answer must match. |
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--run <id>` | string | not set | Run id (r-...). |
| `--block <id>` | string | not set | Block id. |
| `--timeout <seconds>` | number | not set | Longest run time. |
| `--background` | bool | off | Start the session and return at once. |
| `--print-argv` | bool | off | Print the command that would run, then exit. |

#### Examples

```sh
code-forge spawn --level L1 --role coder --brief plans/brief.md --print-argv
```

Exit 0. Output:

```text
{"argv":["claude","-p","--model","claude-sonnet-5","--permission-mode","bypassPermissions","--output-format","json","--no-session-persistence","--disallowedTools","Bash(gh pr ready:*)","Bash(gh pr merge:*)","Bash(gh pr close:*)","Bash(gh pr edit --base:*)","Bash(gh pr edit -B:*)","Bash(git push --fo…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`resolve`](#resolve)
- [`context pack`](#context-pack)
- [`cloud-coder`](#cloud-coder)

## status

Show the active runs and their blocks

```text
usage: code-forge status [--run <id>] [--json]
```

#### When to use

See the active runs, their mode and spend.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge status --json
```

Exit 0. Output:

```text
{"runs":[{"run_id":"<run>","status":"active","mode":"plan","spent_usd":0.5,"unknown_usd_sessions":0,"budget_usd":null,"blocks":[],"more":0,"closed":0,"stopped":0},{"run_id":"<run>","status":"active","mode":"plan","spent_usd":0.0134,"unknown_usd_sessions":0,"budget_usd":null,"blocks":[],"more":0,"clo…
run <run>: no live worker; fix: code-forge run start --add-worker --run <run>
run <run>: no live worker; fix: code-forge run start --add-worker --run <run>
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `runs` | array |

#### Related

- [`run status`](#run-status)
- [`report`](#report)

## sweep

Find stale processes; stop them with --apply

```text
usage: code-forge sweep [--apply] [--json]
Dry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.
--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.
Zombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.
```

#### When to use

Find stale code-forge processes; --apply stops them.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--apply` | bool | off | Stop the processes found (the default is a dry run). |
| `--json` | bool | off | Print JSON instead of text. |

#### Examples

```sh
code-forge sweep --help
```

Exit 0. Output:

```text
Description:
  Find stale processes; stop them with --apply

Usage:
  usage: code-forge sweep [--apply] [--json]
  Dry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.
  --apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.
  Zombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Takes `--json`; not run here: its output lists the processes of the machine it runs on.

#### Related

- [`status`](#status)
- [`run end`](#run-end)

## tools

List the tools code-forge found

```text
usage: code-forge tools [--json] | tools install [<id>...] [--yes] [--dry-run]
  (install asks one yes, default no; answering no prints "cancelled — nothing installed" and exits 1)
```

#### When to use

See which recommended tools (agent CLIs and helpers) are installed.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--json` | bool | off | Print JSON instead of text. |
| `--yes` | bool | off | Answer yes to the confirmation. |
| `--dry-run` | bool | off | Show what would happen; change nothing. |

#### Examples

```sh
code-forge tools --json
```

Exit 0. Output:

```text
{"tools":[{"id":"claude","status":"installed","version":null,"install":null,"hint":null,"note":null},{"id":"codex","status":"installed","version":null,"install":null,"hint":null,"note":null},{"id":"gemini","status":"installed","version":null,"install":null,"hint":null,"note":null},{"id":"grok","stat…
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### JSON output

Prints one JSON line: an object with these keys.

| Key | Type |
|---|---|
| `tools` | array |

#### Related

- [`tools install`](#tools-install)
- [`doctor`](#doctor)

### tools install

Install selected tools after you say yes

```text
usage: code-forge tools install [<id>...] [--yes] [--dry-run]
  (install asks one yes, default no; answering no prints "cancelled — nothing installed" and exits 1)
```

#### When to use

Install the recommended tools that are missing, after you say yes.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--yes` | bool | off | Answer yes to the confirmation. |
| `--dry-run` | bool | off | Show what would happen; change nothing. |

#### Examples

```sh
code-forge tools install --dry-run
```

Exit 0. Output:

```text
all recommended tools are installed
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`tools`](#tools)
- [`init`](#init)

## upgrade

Refresh the skill installations

```text
usage: code-forge upgrade [--source <path>] [--plain]
```

#### When to use

After a new code-forge version: refresh the skill installations.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--source <path>` | path | not set | Package folder to install from. |
| `--plain` | bool | off | Print without the banner. |

#### Examples

```sh
code-forge upgrade --help
```

Exit 0. Output:

```text
Description:
  Refresh the skill installations

Usage:
  usage: code-forge upgrade [--source <path>] [--plain]

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`list`](#list)
- [`config fix`](#config-fix)

## validate

Check the project configuration

```text
usage: code-forge validate [--file <path>]
```

#### When to use

Check .code-forge.yml against the schema.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--file <path>` | path | not set | File path inside the repository. |

#### Examples

```sh
code-forge validate
```

Exit 0. Output:

```text
[code-forge:info] <repo>/.code-forge.yml: valid (0 warnings)
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`config fix`](#config-fix)
- [`doctor`](#doctor)

## version

Show the version

```text
usage: code-forge version
```

#### When to use

Print the installed code-forge version.

#### Flags

None.

#### Examples

```sh
code-forge version
```

Exit 0. Output:

```text
@codedology/code-forge <version>
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`setup`](#setup)
- [`help`](#help)

## worker

Serve a run's review queue

```text
usage: code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]
```

#### When to use

Internal: run start launches it to serve a run's review queue.

#### Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--run <id>` | string | not set | Run id (r-...). |
| `--cwd <dir>` | path | not set | Directory to run in (default: the current directory). |
| `--poll-ms <n>` | number | not set | Poll interval in milliseconds. |
| `--once` | bool | off | Run one pass and exit. |

#### Examples

```sh
code-forge worker --help
```

Exit 0. Output:

```text
Description:
  Serve a run's review queue

Usage:
  usage: code-forge worker --run <id> [--cwd <dir>] [--poll-ms <n>] [--once]

Options:
  -h, --help  Show this help
```

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Refused or failed; the message says why. |
| 2 | Usage error: a missing, unknown or bad argument (the usage is printed). |

#### Related

- [`run start`](#run-start)
- [`review-file`](#review-file)
