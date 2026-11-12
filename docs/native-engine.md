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
  Since B452, Go also prints a run record that is a JSON array, a spend that
  adds up to infinity, and every error Node prints for a broken run file.
  Go hands over to Node only for a run with a non-ASCII block id (Node sorts
  those with ICU) or a ledger number Go cannot read.
- `logs path`, `logs [--last] [--json]` and `logs summary`. `logs clear` and
  `logs report` hand over to Node.
- `ledger tail`. Other `ledger` commands hand over to Node.
- `review-file --wait <ticket> [--max <n>s]`, `review-file --help`, `--cwd`,
  usage errors and the "not inside a git repository" refusal.
- `review-file --wait-block <id> --run <id> [--max <n>s]`. When the daemon is
  turned off, cannot be reached or gives an unusual answer, Go joins the block
  itself: it enqueues each missing ticket and prints the same JSON as Node.
  Go hands over to Node only when a live worker must restart a stopped review
  that has findings, or when the run files have a shape Go does not read.

Since 0.19.0, it also serves these write commands. Go writes the same files,
locks and signed ledger rows as Node.

| Command | Runs in Go | Hands over to Node |
|---|---|---|
| `review-file <file> --block <id>` | Enqueue, with `--fresh`, `--answers`, `--of` and `--rebase-check`, and a cached result that needs a fresh ticket. Since B452, enqueue in a block worktree also prints Node's refusals in Go (`worktree-malformed`, `worktree-missing`, `worktree-not-dir`, `worktree-branch-mismatch`, `worktree-foreign`, `worktree-workspace-missing`), and a private run record that is not JSON gives `run-record-unreadable`. | A git that cannot run in the worktree, and other state that Go cannot read or does not expect. |
| `block claim`, `block stop` | Plain claim and stop, active autopilot grants, pending approvals and `hooks.on_block_end`. Errors print and go to the error log in Go; `--auto` and `--auto-claim` are unknown flags (exit 2), as in Node. | State that Go cannot read or does not expect. `block stop` with a daemon socket runs in Go and the done file takes the serving daemon's rows (B470). Go writes nothing before it hands over. |
| `block open` | Open with or without `--level`, `--after`, `--after-outcome` and autopilot grants, including the worktree. `--help` and `-h` print the usage; an unknown flag is a usage error (exit 2); errors, including a missing `--acceptance` file, print and go to the error log in Go. | Worktree settings Go does not expect (for example an empty `proof.worktree`), `--after` cases Go does not serve, and state that Go cannot read. A `--brief` that is missing, under a file or a directory prints Node's realpath or read error in Go (B470). |
| `block close` | Agent close, `--decide`, `--no-require-reviews`, `--auto-claim`, `--remove-worktree`, human close, external review, hooks and grants. The review gate also judges review caps, reopened and unwaived findings, agent and autopilot waivers, `review.agent_waivers.require_ack`, accepted rule hits, old result files and an amended acceptance in Go. Close prints the agent waiver, agent reopen and accepted hit lines and JSON keys. `--help` and `-h` print the usage; `--help=value` is an unknown flag (exit 2), as in Node. A light block is checked against `light.max_files` and `light.max_lines` in Go (B451). The gate reads only the closing block's rows, as Node does, so another block's rows never send the close to Node, and a `rule_break` row whose id or rule is not text gets Node's `rule_break row: <String(id)>` detail (B451). | This block's review rows that Go cannot read exactly like Node: an object or array `decision` that is not read in ledger order, a review date in a legacy form with a day outside 1–31 or minutes or seconds over 59, a late or cap row whose file, finding or open id is an object or array (or a non-text file Node cannot quote, such as a number from 1e21 up), an ack digest in a non-Latin script that matches only in an order other than Node's sorted one (`close-ack-digest-order`), and `--decide` with a late row whose file and finding are not text. Since B498 an ack digest in Node's Latin `localeCompare` order, an object or array `decision` (printed with Node's key order), V8 `Date.parse` rollover and 2-digit forms (from a Node table), late and cap rows whose file is empty, a number or false or whose finding is missing or not text, the stderr and stdout line order on a shared terminal, and `--auto-claim` claiming before a later refusal (transcript read, `--worker-pid`) run in Go as in Node. Since B493 a late finding with no file, the legacy `toUTCString`/`toString` date forms, a non-ASCII file name in an ack digest, `--auto-claim` on a run with an autopilot record (the grant expiry check runs before the claims), a transcript that runs the `hooks.on_block_end` command (Node's `rule_break`; Go close used to skip this check and close) and `--auto-claim` with `close.require_human` at a terminal (Node's prompt and answers) run in Go. Codex JSON transcripts, the `code-forge-reviews-write` rule and an unreadable `--transcript` run in Go (B466). The block worktree refusals (malformed, missing, not a directory, other branch, another repository, no workspace directory), ledger null and scalar rows and an unreadable rotated ledger file run in Go with Node's messages (B470). Go writes nothing before it hands over; no case is refused. |
| `block ready`, `block waive`, `block reopen`, `block human close`, `block human-pass` | These commands. `block ready` uses the same review gate as `block close`. `block ready --preflight` (B392) is Go only: it lists stale base, open findings, rule breaks and coder report blockers without a gate result, never hands over, and names each check it cannot decide as `skipped`. It exits 0 and prints `block <id> preflight clean` when clean, and exits 1 when it lists blockers. Since B449 `block ready` also runs in Go with `hooks.on_block_end` and with `close.require_human` under an active autopilot grant (the `close-not-routine` blocker). `block human-pass --help` and `-h` print the usage. | State that Go cannot read or does not expect. A `block ready` transcript that names the hook's command runs in Go with Node's `rule_break` blockers (B493). `waive --yes` and `reopen --yes` still run in Node. |
| `context`, `context pack` | Every form since B452: `--help` and `-h`, usage errors, unknown subcommands and flags, the breadcrumb and failure log, and a missing run root. The pack header names the Node from `CODE_FORGE_NODE` when that resolves, else `node` on `PATH` (B469). | Nothing. |
| `gates run` | Every form (B450): built-in, custom and formatter gates (pint and prettier check the supported Git files in argv batches), `repeat_new_tests` (changed sources add the tests that import them), light blocks, flaky detection and a run with no `--run` and no `--block`. A stop signal (SIGINT or SIGTERM) writes the same cancel rows. With `--run` and `--block` the B392 preflight runs first by default (`--no-preflight` skips it); a blocker stops `gates run` with exit 1 before any gate command runs. With no `HOME` the user's home folder is used, as Node does. A `--timeout-ms` over the 2147483647 ms timer limit runs with a 1 ms timer and prints Node's `TimeoutOverflowWarning`. A `--cwd` that is not a folder skips every gate, as Node does. An empty `--run` or `--block` prints Node's message with exit 1. A case Node also refuses is a one-line error with exit 1 or 2 that names the cause and the fix: a bad gate config (the same messages as config validation), a damaged run record, or `repeat_new_tests` on a block with no base commit. A command that cannot start is a red gate with reason `spawn-error`. | Other `gates` subcommands; a gate command that is an executable file with no `#!` line (Node runs it with `/bin/sh` on Linux and stops with `spawn ENOEXEC` on macOS, B469); a run record whose `run_id` does not match `--run`, or with no `workspace` string (also the `block land` gates step for these records). |
| `block land` | Plain land, `--merge` with the selected-test merge gate, `--no-merge-gate`, active grants and `hooks.on_block_end`. | The gates step (B450), no compatible join daemon, the join, claim and close steps Go does not serve, and run state Go does not expect. A run file Go cannot read prints Node's read error in Go (B470). Read-only pre-checks finish before the first write. The B282 reason code names the step. `--help` and `-h` print the usage in Go (B451). |
| `run status`, `run end`, `run start --reattach` | `run status --run <id>` (the same JSON, spend and budget). `run end --run <id>`: the pin check, the stop of a verified worker and the signed `worker.down`, `worker.replaced` and `run.end` rows. `run start --reattach --run <id>` with `--worker-pid`, or without it to re-pin the live worker that the queue announces (B454). `run --help`, `run <sub> --help`, `-h`, usage errors and unknown flags. | A plain `run start`, `run start --add-worker` and `run reload` (the worker launch, the process sweep and the config snapshot). A run record that is not JSON, or with a shape that makes Node fail, a ledger Go cannot read, a worker registry Go cannot judge, and no `HOME`. |
| `resolve <level>` | The level lookup with its fallbacks (B455), `--help`, usage errors and an unknown level. | A `.code-forge.yml` with a YAML syntax error (`config-parse-error`) or a `fallback` that is not a list of maps (`resolve-fallback-shape`). |
| `models` | The model list and `--refresh --from-cli-caches` (B455), with the same models cache file, `--help` and usage errors. | A YAML syntax error (`config-parse-error`), a models cache Go cannot write (`models-refresh-unwritable`) and a `models_cache.json` that is not valid JSON (`models-refresh-invalid-json`). |
| `list`, `remove` | Both commands, `--help` and their usage errors (B455); `remove` deletes the same installed targets and rewrites the installs file. | `remove` with a target outside the safe places (`remove-unsafe-target`) or a target or installs file Go cannot change (`remove-unfinishable`). |
| `pipeline status`, `pipeline next` | Both forms with `--json` and `--root` (B455): step states, duplicate, unknown-need and cycle problems, and a missing pipeline. | A pipeline or outcome with a YAML syntax error or a shape the schema rejects, and a read error other than a missing file or a folder (`pipeline-unsupported`). |
| `outcome seal`, `outcome check`, `outcome record` | `seal` and `check` (B455), and `record` (B471): every problem line, `--json`, `--root`, `--step` (else the pipeline step whose outcome is the file), usage errors, and a passing outcome signed as Node's `outcome.recorded` row under the run lock (`already recorded` when it is). | A schema error, an outcome Go cannot rewrite or decode (`outcome-unsupported`), and for `record` no home folder, a pipeline Go cannot read or a config with a YAML syntax error (`outcome-record-unported`). |
| `validate` | Every rule with Node's messages, order and exit codes (B471): errors and warnings, seen-in-cache model ids, CLIs on `PATH`, pipeline checks, `--file`, usage errors and a missing config. | A config the schema rejects (`validate-schema-issues`), a config holding a secret-shaped value (`validate-secret-values`), a non-empty `coder.sandbox.add_dirs` (`validate-sandbox-dirs`), a YAML syntax error (`config-parse-error`) and a pipeline Go cannot read (`pipeline-unsupported`). |
| `plan check` | `--help`, usage errors, flag errors and a plan or `--facts` file that cannot be read (B471). | The check itself: facts, project config and ledger lanes (`plan-check-unported`). |
| `sweep` | `--help`, `-h` and usage errors (B471). | The process scan, dry run or `--apply` (`sweep-unported`). |
| `upgrade` | `--help`, `-h`, usage errors and a `--source` that is missing or not a folder (B471). | Re-pointing the recorded installs with the release notes and docs sync, also with no `--source` (`upgrade-relink`), and a `--source` path holding control characters (`upgrade-control-chars`). |
| `tools`, `tools install` | The tool list in text and `--json`, `install` with `--dry-run` and `--yes`, the plan, each install command and its result, and every usage error (B455). | `install` at a terminal without `--yes`, which asks Node's yes prompt (`tools-confirm`), and a `--version` line whose cut splits an emoji (`tools-unsupported`). |
| `doctor` | `--help`, `-h`, usage errors and unknown flags, and the whole `--quick` run in text and `--json` (B474): the config row, harness links, the Jev key from its env variable or the file store (`CODE_FORGE_KEY_BACKEND=file`), its expiry warning, a malformed key reference, an expired cached key (deleted with its index row), a cached key cut to the cache hours (entry and index row rewritten), a 1Password `op://` read with `op read` (retried once after a first-call timeout, logged as an `op_retry` warning) cached with its index row, each 1Password failure reason (B483), and the reviewer rows from the project ledger, with the resolved key masked as Node does. | The full run with its live checks (`doctor-full-run`: the live Jev call, provider CLI probes, worker start and stop, signer key, process sweep and memory rows, native binary and daemon rows, tools, ledger, locks, tmp and review state); a config the schema rejects, one holding a secret-shaped value or a non-empty `coder.sandbox.add_dirs` (`doctor-config-schema`, `doctor-config-secrets`, `doctor-sandbox-dirs`); a YAML syntax error (`config-parse-error`); the OS keychain key store (`doctor-key-store`); a key store `index.json` Go cannot read back exactly like `JSON.parse` (a lone surrogate escape, a number out of range, or a non-object row to shorten) when the key read must rewrite it (`doctor-key-refresh`); ledger rows whose dates or keys Go does not read exactly like Node (`doctor-ledger-shape`); and no `HOME` (`doctor-no-home`). |

The Go engine skips the test gate for proven text-only `gates run --changed` changes.
`gates run --run R --block B --changed` always stays in Go: it selects the block's tests,
the tests that import a changed file, and for a native change `test/compat/go.test.mjs` and
every test that builds or runs the native binary (reason `go_only` or `import_graph`). A
change under `test/fixtures/**`, `schema/**`, `scripts/compat-golden.mjs` or `src/config/**`
also selects `test/compat/config-golden.test.mjs` (B409). A test command without
`{test_files}` gets the selection appended. It never
runs the full Node suite and never hands over; an empty selection skips the test gate. The
merge gates of `block land --merge` and of a batch land use the same selection (B404).
`gates run --run R --block B --full` is the only local full suite.
Its merge gate can also reuse a green full run for the exact merged tree.
See [how it works](how-it-works.md) for the scope rules and reuse checks.

A hand-over always passes the original arguments. Node then runs the command
exactly as before. When Go is not sure it gives the same answer, it hands over.
Commands such as `code-forge version` and the full `code-forge doctor` also go through
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
Since B416, `block close` and `block ready` check the `code-forge-runs-access` transcript rule in Go,
for plain-text logs and Claude Code JSONL logs, with the same hits as Node: text a Bash command only
writes to a file does not count, a home-relative spelling in a command word or tool path does.
Go also decides the git, gh, rm and code-forge launcher rules (`npx`, `./bin/code-forge`, a bare
`forge` verb), `sh -c` wrappers, interpreter scripts and non-ASCII text, so a `coder-forge` path or
a `forge/r-…` branch name no longer hands over.
Since B466, Go also reads Codex `exec --json` logs and decides the `code-forge-reviews-write` rule
(writes into `.code-forge/reviews` or a `.code-forge/queue/*.done` file) with Node's hits.
`close-transcript-format` now names only invalid UTF-8, a lone surrogate escape, a number JavaScript
reads as infinite, and line separators JavaScript reads differently. An unreadable `--transcript`
file prints Node's error (for example `ENOENT: no such file or directory, open '<path>'`), exits 1
and writes nothing, in Go. Review dates in every ECMAScript date-time form (year or month only,
`±YYYYYY` years, `T24:00`, local time) are read like V8; only legacy forms stay `close-gates-format`.
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
copy of its settings. Block waits then run in Go without the daemon: Go reads the queue and ledger files and hands over to Node only in the cases listed above. Status in Go does not use the daemon.

### Choose Node for the hand-over

`CODE_FORGE_NODE` selects the Node executable that Go starts. For example,
in a shell where `node` already works:

```sh
CODE_FORGE_NODE="$(command -v node)" code-forge version
```

Use an executable path or a command name found on `PATH`. If the setting is
empty or absent, Go looks for `node` on `PATH`. This setting applies only to
the Go hand-over. When the shell skips Go, it uses `node` on `PATH` instead.

## A stale binary in a code-forge worktree

This matters only when you work on code-forge itself. Each block worktree of
the code-forge repo has its own `native/bin/<os>-<arch>/code-forge-native`,
and the Node compat tests run that binary. Two commands keep it current:

- `block rebase` rebuilds it when the rebase brought in a change to
  `native/**` or `scripts/build-native.mjs`.
- `gates run --run <run> --block <id>` rebuilds it before any gate when it is
  missing or older than `native/go.mod` or a non-test `native/**/*.go` file.
  The gate JSON then shows `native_rebuilt: true`.

Both run `node scripts/build-native.mjs --host` in the worktree, with the
Node from `CODE_FORGE_NODE` when it is set. A failed build stops the command
with exit 1 and a line naming `native-rebuild-failed`. Other projects have no
`native/` folder, so nothing is built for them (B408).

## Run lock

The run lock (`<run>.lock` next to the run record, shared with Node, 5 s wait)
guards only a short read-modify-write of the run record, under a second (B426).
A Go verb that does long work (git, tests, builds) reads the record first, does
the work with no run lock, then commits only the blocks it owns in one short
write. So a batch land in its merge gate, a `block rebase` or a
`gates run --auto-rebase` never blocks a verb on another block of the run.

- Two long verbs on the same block cannot overlap: each holds that block's own
  lock (`<run>.block-<hex id>.lock`, same wait and stale-owner rules as the run
  lock). The second one waits up to 5 s, then stops with `block-busy`.
- If another verb changed the block while the verb worked, its commit saves
  nothing for that block and stops with `block-changed`, naming the block. Run
  the verb again.
- Batch land: the rebases, each block's gates and the merge gate run without
  the run lock; each new base, the final "mark landed" and any restore is one
  short commit of the batch's blocks.

## Heal queue

Some failures pass by themselves: a run lock that another command holds for a
moment, a ledger lock, a git `index.lock`, or a network error from git. Before
B429 these stopped the work until a person ran the command again. Now the
command adds the problem to a queue and still fails, so a script sees the same
exit code as before. The last stderr line names the item:

```text
gates run: run r-1 is locked (…/runs/r-1.lock); remove it if no forge verb is running
heal: queued h20261111t080000-1a2b3c
```

Three commands queue a problem: `gates run --run R --block B`,
`block rebase <id> --run R` and `block land <id>… --run R` (one block or a
batch). They queue only when they name a run, and only for these texts: a lock
(`is locked (`, `lock timeout`, `index.lock`, `cannot lock ref`) or a
transient git error (`could not resolve host`, `connection reset`,
`the remote end hung up`, `early EOF`). Any other failure is not queued. A
stale base is not queued either: `gates run` keeps its one-line refusal, and
`--auto-rebase` fixes it.

The queue of a run is one file, `~/.code-forge/heal/<run>.jsonl`. Each line is
a full copy of one item, and the last line of an item wins. An item keeps its
`id`, `run`, `block`, `kind` (`lock`, `gate`, `land`; `ci` and `worker` are
reserved), the exact `argv` to run again, the `cwd`, the failure `detail`,
`tries`, `status` (`open`, `retrying`, `healed`, `needs_coder`) and its times.

```sh
code-forge heal list [--run R] [--json]      # all items, or one run's
code-forge heal show <id> [--run R] [--json] # one item with its failure text
code-forge heal run --run R                  # retry until every item ends
code-forge heal run --run R --once           # one pass over the items that are due
code-forge heal run --run R --watch          # keep going (every 15 s) until stopped
```

`heal run` takes the items one by one and runs the stored argv again as a
child of the same binary, in the stored `cwd`. Exit 0 marks the item
`healed`. A failure waits 15 s, then 60 s, then 240 s before the next try.
After `--max-tries` tries (3 by default), or at once when the new failure is
not a retryable one, the item becomes `needs_coder`. `heal run` exits 1 when an
item needed a coder in that call. A retry never queues a new item: the child
updates its own.

For a `lock` item, `heal run` first removes the named lock when its owner is
proven dead: a lock file whose JSON `pid` no longer runs (or runs with another
start time), or a lock directory whose `owner.json` says so, such as the run
and block lock directories with the owner file B423/B426 write. A live owner's
lock stays. A lock without an owner pid (a Node holder) is never removed by
`heal run`; the retry just tries again. Only paths under `~/.code-forge` are
touched.

Only one `heal run` works on a run at a time. A second one stops at once with
`heal run: busy: another heal run (pid N) is healing run R; this one stops`
and exit 1.

## Gate children start at nice 10

Every gate child that the Go engine starts (the full test suite above all)
runs at nice 10, or at the engine's own nice when that is higher, so a gate
cannot starve the rest of the machine. The engine starts the child through a
short copy of itself that sets its own nice and then runs the gate program,
so the gate's first instruction already runs at nice 10 and every process it
forks inherits that value. The gate keeps its argv, its environment and the
same spawn errors (for example `gate terminated: spawn <path> ENOENT`). The
engine used to lower the child after it started; on macOS that change could
be lost while the child was still starting, and the gate then ran at nice 0
(B420).

## Rebase runs in Go (B431)

`block rebase <id> --run <run>` (with only `--json`) of an open block with a
worktree and a recorded base branch runs in Go. Since B449 every form runs in
Go: `--help`, a closed or awaiting block, `--head` for a block without a
worktree, `--accept-changed <file>… --reason <text>` (the owner confirms at
a terminal; refused while an autopilot grant is active) and
`--accept-landed`. Only a run record Go does not expect (a pending or
malformed autopilot approval, malformed blocks, unusual storage, an
unreadable review queue) still hands over to Node. The Go rebase, `block rebase --accept-landed`,
`review-file --rebase-check` and the batch land rebase share one rule: replay
first, refuse only a real conflict. Owned files main changed are named
`upstream_owned` in the signed row and queued for review again. A conflict
restores the old head and refuses `rebase-conflict` naming the files. A
`CHANGELOG.md` where both sides only added lines keeps both sides, main's
lines first; the replay runs with `-c merge.union.driver=false`, so a
`merge=union` attribute cannot bring back a line the block removed.

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
| The doctor shows daemon `fallback`. | Check that a review worker is active and that the package versions match. With no active worker, `fallback` can be normal. Block waits and status still run in Go from the files on disk. To keep the daemon off, set `native.daemon: false` before the next worker starts. |

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
