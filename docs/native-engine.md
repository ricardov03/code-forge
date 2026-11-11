# Go engine

## What it is

In 0.17.0, the npm `code-forge` command starts the Go engine first.
The engine is a binary: a program that is already built for your computer.
You do not need to install Go. You still need Node.js 22 or newer.

The command works in three steps:

1. The shell entry finds `native/bin/<platform>-<arch>/code-forge-native`
   inside the installed package. The platform is the operating system.
   The architecture, or arch, is the processor type.
2. If that file exists and can run, the entry starts it.
3. Go runs a command if its public route table lists that command.
   Otherwise, Go hands the arguments to `bin/code-forge.mjs` in Node.
   This hand-over happens by itself. It keeps the same standard output,
   error output and exit code. The exit code is the number that tells the
   shell whether a command succeeded.

The public route table currently serves these read commands:

- `status`, `status --json` and `status --run <id>`. Go reads the run files
  from disk. It does not need the daemon and it never takes a lock.
  If Go cannot read the state, it hands over to Node.
- `logs path`, `logs [--last] [--json]` and `logs summary`. `logs clear` and
  `logs report` hand over to Node.
- `ledger tail`. Other `ledger` commands hand over to Node.
- `review-file --wait <ticket> [--max <n>s]`.
- `review-file --wait-block <id> --run <id> [--max <n>s]` when a compatible
  daemon answers. A stopped file, an unusual answer or no daemon hands over
  to Node.

Since 0.19.0, it also serves these write commands. Go writes the same files,
locks and signed ledger rows as Node.

| Command | Runs in Go | Hands over to Node |
|---|---|---|
| `review-file <file> --block <id>` | Enqueue, with `--fresh`, `--answers`, `--of` and `--rebase-check`, and a cached result that needs a fresh ticket. | State that Go cannot read or does not expect. |
| `block claim`, `block stop` | Plain claim and stop, active autopilot grants, pending approvals and `hooks.on_block_end`. | State that Go cannot read or does not expect. |
| `block open` | Open with or without `--level`, `--after`, `--after-outcome` and autopilot grants, including the worktree. | State that Go cannot read or does not expect. |
| `block close` | Agent close, `--decide`, `--no-require-reviews`, `--auto-claim`, `--remove-worktree`, human close, external review, hooks and grants. The review gate also judges review caps, reopened and unwaived findings, agent and autopilot waivers, `review.agent_waivers.require_ack`, accepted rule hits, old result files and an amended acceptance in Go. Close prints the agent waiver, agent reopen and accepted hit lines and JSON keys. `--help` and `-h` print the usage. | A light block, `--auto-claim` on a run with an autopilot record, `--auto-claim` with `close.require_human` at a terminal, a `--help=value` spelling, and review rows or dates Go cannot read exactly like Node (for example a date that is not ISO, or a non-ASCII file name in an ack digest). |
| `block ready`, `block waive`, `block reopen`, `block human close`, `block human-pass` | These commands. `block ready` uses the same review gate as `block close`. `block human-pass --help` and `-h` print the usage. | State that Go cannot read or does not expect. `waive --yes` and `reopen --yes` still run in Node. |
| `context pack` | This command. | Other `context` subcommands and state that Go cannot read. |
| `gates run` | Built-in and custom gates, `repeat_new_tests`, light blocks and flaky detection. A stop signal (SIGINT or SIGTERM) writes the same cancel rows. | A run with no `--run` and no `--block`, and other `gates` subcommands. |
| `block land` | Plain land, `--merge` with the full merge gate, `--no-merge-gate`, active grants and `hooks.on_block_end`. | No compatible join daemon, unsupported state, a light block, or a run with `close.require_human` or an external reviewer. Read-only pre-checks finish before the first write. The B282 reason code names the step. |

The Go engine skips the test gate for proven text-only `gates run --changed` changes.
Its merge gate can also reuse a green full run for the exact merged tree.
See [how it works](how-it-works.md) for the scope rules and reuse checks.

A hand-over always passes the original arguments. Node then runs the command
exactly as before. When Go is not sure it gives the same answer, it hands over.
Commands such as `code-forge version` and `code-forge doctor` also go through
Go to Node. Go additionally runs private helpers and the optional background
daemon.

### See what is available

Run the full doctor in your project:

```sh
code-forge doctor --json
```

Read these rows in the JSON `rows` list:

| Row id | Meaning |
|---|---|
| `native` | `OK` gives the binary path and version. `INFO` with `not found, using Node` means the helper is absent, cannot run, or is disabled. A path followed by `unavailable, using Node` means its version check failed. |
| `native_daemon` | `live` means the daemon answered for the active worker. `off` means it is disabled or no usable config was found. `fallback` means it is enabled but no compatible daemon answered. |

The text form, `code-forge doctor`, labels these rows `native binary` and
`native daemon`. The quick doctor omits them. Other setup checks can fail
even when the native binary row is `OK`.

These rows check availability. They do not say that a public verb ran fully
in Go. The `native-status.mjs` helper reads data from the daemon on standard
input. It does not report which engine started a command. Although help
lists `native-status`, it is a private helper with no public command handler.
Use `code-forge status` to check run progress.

### See hand-overs

Go records one row before it hands a command to Node.
Each row has a UTC time, the command, a known subcommand, a reason and the version.
It does not save argument values, paths or IDs. Unknown subcommands are empty.
The file is `$HOME/.code-forge/native/handovers.jsonl`.
Go uses the current `HOME` setting. New folders use mode `0700`.
The file uses mode `0600`. It holds at most 5000 rows.
When it grows past this limit, Go keeps the newest 4000 rows.
Write errors are silent. They do not change the command result.

Show the counts, with the largest count first:

```sh
code-forge logs handovers
code-forge logs handovers --json --since 2026-11-01
```

`--since` accepts an ISO date or a date and time with a time zone.
JSON also shows the last time for each group.
This command runs in Go. It is not in the Node-only command set.
Set `CODE_FORGE_HANDOVER_LOG=0` to stop recording.
`CODE_FORGE_NATIVE=0` also records no rows.

Reason codes are fixed names. For example, `unrouted` means Go has no route.
`close-auto-claim` and `enqueue-fresh` name cases that still run in Node.
`land-step-handover:close` means the close step needs Node.
Grants and end hooks supported by native close no longer cause a land hand-over.
Since B380, `block close` and `block ready` no longer hand over for review caps,
waivers, reopens, accepted hits, acks or an amended acceptance.
`close-gates-format` now means a review row or date Go cannot read exactly like Node.
`close-help` now means only a `--help=value` or `-h=value` spelling.
`land-merge-gate` is reserved for a full-suite gate configuration Go cannot run.
The full code list is the `handoverReasons` table in
[`handover_log.go`](../native/cmd/code-forge-native/handover_log.go).

### Measure it

Build the binary with `node scripts/build-native.mjs --host`.
Then run `npm run bench:verbs` or `npm run bench:block`.

`bench:verbs` compares the Node lane and the Go lane of the npm entry for each
verb, after two warmup runs. The columns show the median, p90 and minimum time
in milliseconds, the peak memory (RSS) in MB, the engine that ran, the speedup
(Node median / Go median), and whether output and exit code are the same.
The `block land` row measures the already-closed refusal, including its signed
timing row, with Node unavailable in the engine probe. Successful land shapes
and merge gates are checked by `parity-block-land-full.test.mjs`.

`bench:block` compares three lanes: `node` (Node only), `go` (Node with the Go
helper) and `entry` (Go first). It reports child processes, CLI calls, peak
memory, wall time and leftover processes.

Both benchmarks use isolated state. Both fail when a process or a daemon socket
stays after the run. Use `--json`, or `--out <file>`, to get JSON.

## Platforms with a binary

Release builds supply these four targets:

| System | Processor | Package directory |
|---|---|---|
| macOS | Apple silicon | `native/bin/darwin-arm64/` |
| macOS | Intel 64-bit | `native/bin/darwin-x64/` |
| Linux | x86 64-bit | `native/bin/linux-x64/` |
| Linux | ARM 64-bit | `native/bin/linux-arm64/` |

If the shell entry finds no binary for your platform and architecture,
it starts Node directly. It does the same if the file is missing or lacks
execute permission. There is no extra option to set. Node must be on `PATH`,
the list of directories where the shell looks for programs.

## Switches

### Use Node only

```sh
CODE_FORGE_NATIVE=0 code-forge version
```

Set `CODE_FORGE_NATIVE` to exactly `0` to skip the Go entry and disable native
helpers and the daemon. The shell starts `node` from `PATH`.
Remove the setting to allow Go again.

### Turn off the daemon

In `.code-forge.yml`, set:

```yaml
native:
  daemon: false
```

The daemon is enabled by default. This setting disables only the daemon.
The Go entry and other native helpers can still run. Workers read this setting
at startup. Clients use the run's current config snapshot, which is the stored
copy of its settings. The setting makes block waits use Node. Status in Go does not use the daemon.

### Choose Node for the hand-over

`CODE_FORGE_NODE` selects the Node executable that Go starts. For example,
in a shell where `node` already works:

```sh
CODE_FORGE_NODE="$(command -v node)" code-forge version
```

Use an executable path or a command name found on `PATH`. If the setting is
empty or absent, Go looks for `node` on `PATH`. This setting applies only to
the Go hand-over. When the shell skips Go, it uses `node` on `PATH` instead.

## Internal verbs

The native binary reserves the `internal` namespace for its helpers.
These include `version`, `verify-ledger`, `serve`, `state-golden` and
`block-rebase-landed`. They support version checks, ledger checks, the daemon,
compatibility tests and block updates. Users do not call them.
Keep using the public commands. The worker starts and stops the daemon for you.

## Native daemon

A daemon is a program that keeps running in the background.
When the optional native binary is installed and the daemon is enabled,
the review worker starts one Go daemon for its run.
It stops the daemon it started when the worker exits.
Another worker can share a daemon that is already running.

The private `native.sock` Unix socket carries requests between local programs.
The daemon serves block waits and Node status from cached ledger and queue data.
Status in Go reads the disk directly and does not use the daemon.
Cached data is a copy kept ready in memory. It avoids repeated reads in Node
clients. A missing, crashed or incompatible daemon causes clients to use Node
by themselves, with the same answers. Both text and JSON status output have
the same bytes on the daemon and Node paths.

The full doctor shows `native daemon` as `live`, `off` or `fallback`.
Use the switches above to turn off all native code or just the daemon.
The daemon config setting takes effect for workers at startup;
clients use the run's current snapshot.

## Troubleshooting

| Problem | What to do |
|---|---|
| The binary lacks execute permission. | The entry uses Node directly. Reinstall the npm package to restore its executable file. For a source checkout, rebuild the host binary. Check the full doctor's `native` row again. |
| The binary is for the wrong platform or processor. | Install the package for this machine, or rebuild for this host. A missing matching file makes the entry use Node. If a wrong binary was put at the matching path, it can fail to start; set `CODE_FORGE_NATIVE=0` while you replace it. |
| The Go hand-over says `node executable … not found`. | Put Node on `PATH`, or set `CODE_FORGE_NODE` to its real executable path. This failure exits 127. If Go is disabled or missing, fix `PATH`: the shell does not use `CODE_FORGE_NODE`. |
| The doctor shows daemon `fallback`. | Check that a review worker is active and that the package versions match. With no active worker, `fallback` can be normal. Block waits still use Node; status in Go reads the disk. To keep the daemon off, set `native.daemon: false` before the next worker starts. |

Contributors can rebuild from the repository root with Go installed:

```sh
node scripts/build-native.mjs --host
```

## How we keep Go and Node the same

Contributors use parity tests. Parity means that both paths give the same
result. The tests run commands in separate copies of a test project and home
folder. They compare output bytes, error output, exit codes and saved state.
They allow only known changes such as timestamps. Entry tests also check
arguments, standard input, the working directory, environment settings,
symbolic links, Node-only mode and missing executables. Daemon tests compare
status and block waits with Node and check fallback when the daemon fails.

`parity-gates-custom` waits for all fixture files to be written before starting Go.
This prevents random failures under load from reading a partial fixture.
