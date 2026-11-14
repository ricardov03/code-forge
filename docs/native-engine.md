# Go engine

## What it is

In 0.17.0, the npm `code-forge` command starts the Go engine first.
The engine is a binary: a program that is already built for your computer.
You do not need to install Go. You still need Node.js 22 or newer.

The command works in three steps:

1. The shell entry finds `native/bin/<platform>-<arch>/code-forge-native`
   inside the installed package. The platform is the operating system.
   The architecture, or arch, is the processor type.
2. If that file exists and can run, the entry starts it. The binary is
   required (B569): with no binary for this platform and arch, a missing file
   or one without execute permission, the entry runs no Node. It prints one
   message on stderr that names `<platform>-<arch>`, the expected path and the
   fix (`sh <package>/scripts/install.sh`, then `npm i -g @codedology/code-forge`
   if that fails), and exits 69. No verb exits 69.
3. Go runs a command if its public route table lists that command.
   Otherwise, Go hands the arguments to `bin/code-forge.mjs` in Node.
   This hand-over happens by itself. It keeps the same standard output,
   error output and exit code. The exit code is the number that tells the
   shell whether a command succeeded.

The skill's `forge <verb>` shim (`skill/scripts/forge`) starts this same shell
entry, `bin/code-forge`, so skill verbs run in Go too (B547). With
`CODE_FORGE_BIN` set to `bin/code-forge`, or to a `bin/code-forge.mjs` that has
the `code-forge` entry next to it, the shim starts the entry as well. Any other
`CODE_FORGE_BIN` file, and an old package that has only `bin/code-forge.mjs`
next to the skill, still runs in Node.

The public route table currently serves these read commands:

- `status`, `status --json` and `status --run <id>`. Go reads the run files
  from disk. It does not need the daemon and it never takes a lock.
  Since B452, Go also prints a run record that is a JSON array, a spend that
  adds up to infinity, and every error Node prints for a broken run file.
  Go hands over to Node only for a run with a block id outside printable
  ASCII among two or more listed (not closed, not stopped) blocks (Node sorts
  those with ICU) or a ledger number Go cannot read. Since B576 one listed
  block, or such ids on closed and stopped blocks only, run in Go.
- `logs path`, `logs [--last] [--json]` and `logs summary`.
- `ledger tail` and `ledger calibration` (B457).
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
| `block open` | Open with or without `--level`, `--after`, `--after-outcome` and autopilot grants, including the worktree. `--help` and `-h` print the usage; an unknown flag is a usage error (exit 2); errors, including a missing `--acceptance` file, print and go to the error log in Go. | A YAML form in the acceptance file whose output Go does not reproduce byte-equal (a directive, an explicit tag, a collection as a key, a tab, a quoted scalar over several lines, keys that Node and Go judge duplicate differently, a BOM or bytes that are not UTF-8, a syntax error Go words differently: `open-acceptance-yaml`); a `code-forge.pipeline.yml` of such a form, outside the pipeline schema (AJV's wording) or with an unnamed read error (`open-pipeline-form`); an outcome contract outside the outcome schema, not UTF-8 or with an unnamed read error (`open-outcome-form`); a torn middle ledger line with no `--level` (`open-level-ledger`); no `HOME` and a user database lookup that fails (`open-home-lookup`); a brief path Go cannot make absolute (`open-brief-cwd`); and run state that changes under the lock. A `--brief` that is missing, under a file or a directory prints Node's realpath or read error in Go (B470). Since B581 these run in Go with Node's output: an empty `proof.worktree.dir` or `base`, a `--after` status that is not text (JavaScript's `String()`), a claim row whose path is not text, the signer key and ledger checks with no `--level`, the brief's loop, name-too-long and read errors, the acceptance YAML (Node's message alone), the pipeline read and YAML errors, the outcome read errors, no `HOME` (the user database home) and a block id such as `__proto__`. Go writes nothing before it hands over. |
| `block close` | Agent close, `--decide`, `--no-require-reviews`, `--auto-claim`, `--remove-worktree`, human close, external review, hooks and grants. The review gate also judges review caps, reopened and unwaived findings, agent and autopilot waivers, `review.agent_waivers.require_ack`, accepted rule hits, old result files and an amended acceptance in Go. Close prints the agent waiver, agent reopen and accepted hit lines and JSON keys. `--help` and `-h` print the usage; `--help=value` is an unknown flag (exit 2), as in Node. A light block is checked against `light.max_files` and `light.max_lines` in Go (B451). The gate reads only the closing block's rows, as Node does, so another block's rows never send the close to Node, and a `rule_break` row whose id or rule is not text gets Node's `rule_break row: <String(id)>` detail (B451). | This block's review rows that Go cannot read exactly like Node: an object or array `decision` that is not read in ledger order, a review date in a legacy form with a day outside 1–31 or minutes or seconds over 59, a late or cap row whose file, finding or open id is an object or array (or a non-text file Node cannot quote, such as a number from 1e21 up), an ack digest in a non-Latin script that matches only in an order other than Node's sorted one (`close-ack-digest-order`), and `--decide` with a late row whose file and finding are not text. Since B498 an ack digest in Node's Latin `localeCompare` order, an object or array `decision` (printed with Node's key order), V8 `Date.parse` rollover and 2-digit forms (from a Node table), late and cap rows whose file is empty, a number or false or whose finding is missing or not text, the stderr and stdout line order on a shared terminal, and `--auto-claim` claiming before a later refusal (transcript read, `--worker-pid`) run in Go as in Node. Since B493 a late finding with no file, the legacy `toUTCString`/`toString` date forms, a non-ASCII file name in an ack digest, `--auto-claim` on a run with an autopilot record (the grant expiry check runs before the claims), a transcript that runs the `hooks.on_block_end` command (Node's `rule_break`; Go close used to skip this check and close) and `--auto-claim` with `close.require_human` at a terminal (Node's prompt and answers) run in Go. Codex JSON transcripts, the `code-forge-reviews-write` rule and an unreadable `--transcript` run in Go (B466). The block worktree refusals (malformed, missing, not a directory, other branch, another repository, no workspace directory), ledger null and scalar rows and an unreadable rotated ledger file run in Go with Node's messages (B470). Since B583 a transcript that is not valid UTF-8 (plain text or a Claude Code log, decoded as Node's `readFile(file, 'utf8')` does), a Claude Code log with an escaped surrogate pair, and agent waiver and reopen rows whose reason, verdict, file or finding holds an object or array (printed in the `--json` close audit with the ledger line's key order) run in Go; a Claude Code log with a lone surrogate escape still hands over (`close-transcript-format`). Since B587 a changed file that `review.exclude` (or the default lock-file list) matches closes in Go: it leaves the review set, keeps its proof check, gets a `gate.review_excluded` close row and a `not reviewed (review.exclude <rule>): <file>` line, and an npm `package-lock.json` or `npm-shrinkwrap.json` gets Node's lock check (not JSON, not an object, entries not objects, a `resolved` URL that is not HTTPS on `review.lockfile_registries`, missing integrity) with Node's `lockfile` blocker and fix; `close-excluded-file` now hands over only for a `review.exclude` that is not a list of text, and the new `close-lockfile-format` hands over a lock Go cannot judge like Node (an escaped surrogate, a `resolved` that is an array or a URL form outside the proved WHATWG subset such as user info, IPv6, IPv4 or IDN hosts, `review.lockfile_registries` that is not a list of text). Since B590 a late or cap row whose file, finding or open id is an object or array, or whose non-text file Node cannot quote, closes in Go as in Node (an object prints as `[object Object]`, an array joined; a file Node cannot quote ends with Node's `block close: arg.replaceAll is not a function`, exit 1, nothing written), a plain-text or Codex transcript with JavaScript-only whitespace (NBSP, U+2028, U+3000, a BOM, `\v`) is split on JavaScript's `\s` instead of handing over, and a routine close (`close:routine` grant) prints the `opened before` warning once per gate load and joins every routine failure into the `close-not-routine` reason, as Node does. Go writes nothing before it hands over; no case is refused. |
| `block ready`, `block waive`, `block reopen`, `block human close`, `block human-pass` | These commands. `block ready` uses the same review gate as `block close`. `block ready --preflight` (B392) is Go only: it lists stale base, open findings, rule breaks and coder report blockers without a gate result, never hands over, and names each check it cannot decide as `skipped`. It exits 0 and prints `block <id> preflight clean` when clean, and exits 1 when it lists blockers. Since B565 `block ready` stays in Go when the workspace path holds secret-like text such as `eyJ` or `AKIA` (Node prints it as it is); text holding the run's signer key, which Node masks, still hands over. Since B449 `block ready` also runs in Go with `hooks.on_block_end` and with `close.require_human` under an active autopilot grant (the `close-not-routine` blocker). `block human-pass --help` and `-h` print the usage. | State that Go cannot read or does not expect. A `block ready` transcript that names the hook's command runs in Go with Node's `rule_break` blockers (B493). `waive --yes` and `reopen --yes` (the fresh model judge) and `block reopen` at a terminal still run in Node. Since B453, `block waive` at a terminal asks and writes the signed human `review.waived` row in Go. |
| `block accept-hit`, `block amend-acceptance`, `block unwaive`, `block ack-waivers`, `block attempt` | Since B453, these commands: the usage and refusal checks, the terminal question (no terminal exits 2, a "no" exits 1), the signed `rule_break.accepted`, `block.acceptance_amended`, `review.unwaived`, `agent_waivers.acked` and `block.attempt` rows, the registry change and the same text or `--json` output as Node. | A Codex JSON transcript for `accept-hit` without `--rule`, an `ack-waivers` set whose digest order needs a collation Go does not carry (a non-Latin script, B498 serves Latin), an acceptance file Go cannot read or parse, autopilot approvals or a grant Go cannot read, and state that Go cannot read or does not expect. |
| `context`, `context pack` | Every form since B452: `--help` and `-h`, usage errors, unknown subcommands and flags, the breadcrumb and failure log, and a missing run root. The pack header names the Node from `CODE_FORGE_NODE` when that resolves, else `node` on `PATH` (B469). With no Node at all, the pack is still written and its `## code-forge` line names the install's own `bin/code-forge` launcher alone (it runs this Go binary); only an install with neither a Node nor that launcher refuses, and writes nothing (B572). | Nothing. |
| `cloud-coder` | Every form. Since B572 the review of each landed file (`review-file`) and the coder spend (`ledger add coder`) run as children of this Go binary, so no Node is needed; a case those verbs hand over still goes to Node from that child. | Nothing. |
| `gates run` | Every form (B450): built-in, custom and formatter gates (pint and prettier check the supported Git files in argv batches), `repeat_new_tests` (changed sources add the tests that import them), light blocks, flaky detection and a run with no `--run` and no `--block`. A stop signal (SIGINT or SIGTERM) writes the same cancel rows. With `--run` and `--block` the B392 preflight runs first by default (`--no-preflight` skips it); a blocker stops `gates run` with exit 1 before any gate command runs. With no `HOME` the user's home folder is used, as Node does. A `--timeout-ms` over the 2147483647 ms timer limit runs with a 1 ms timer and prints Node's `TimeoutOverflowWarning`. A `--cwd` that is not a folder skips every gate, as Node does. An empty `--run` or `--block` prints Node's message with exit 1. A case Node also refuses is a one-line error with exit 1 or 2 that names the cause and the fix: a bad gate config (the same messages as config validation), a damaged run record, or `repeat_new_tests` on a block with no base commit. A command that cannot start is a red gate with reason `spawn-error`. | A gate command that is an executable file with no `#!` line (Node runs it with `/bin/sh` on Linux and stops with `spawn ENOEXEC` on macOS, B469; reason `gates-custom`); a run record whose `run_id` does not match `--run` (reason `gates-run-id-mismatch`), or with no `workspace` string (reason `gates-run-no-workspace`); also the `block land` gates step for these records. |
| `block land` | Plain land, `--merge` with the selected-test merge gate, `--no-merge-gate`, active grants and `hooks.on_block_end`. A batch land's merge gate links `gates.merge_gate_links`, runs `gates.merge_gate_setup` and honours `--copy-links` like a single land (B589). A retry of a closed block (a land that failed after close) stays in Go when the run has active autopilot approvals, since the retry ends no block (B589). | The gates step (B450), no compatible join daemon, the join, claim and close steps Go does not serve, and run state Go does not expect. A run file Go cannot read prints Node's read error in Go (B470). Read-only pre-checks finish before the first write. The B282 reason code names the step. `--help` and `-h` print the usage in Go (B451). |
| `block rebase` | Every form (B449), with no Node child since B578: active and due autopilot approvals (a due one is restored first, its line printed before the rebase), run records with no orphans list, an odd sibling block or a config snapshot `status` cannot read, a `rebase_check` that is falsy (no checkpoint) or truthy but not a signed row (Node's `the pending rebase checkpoint is invalid`), and no `HOME` (the user database home, as Node's `os.homedir()`). | Narrow named reasons decided before any write (B578): `rebase-home`, `rebase-run-record`, `rebase-autopilot-shape`, `rebase-record-shape`, `rebase-storage`, `rebase-queue-unreadable`, `rebase-path-unresolved`, `rebase-key-unreadable`, `rebase-ledger-unreadable`, `rebase-content-hash`. A handed-over rebase no longer rebuilds the worktree binary. Before a hand-over Go prints only the expiry lines Node does not repeat (an approval restored or skipped); a failed grant expiry or an approval not restored yet is left to Node's own check, so each line appears once, as with Node alone (B584). |
| Other `gates` subcommands | `detect`, `secret-scan`, `safe-edit`, `scope`, `acceptance` and `transcript-grep` (B459), with the same stdout, stderr, exit code, `errors.jsonl` rows and breadcrumbs as Node; `gates --help`, `gates <sub> --help` (including `gates run --help`, which prints the usage and runs no gate) and usage errors also run in Go. `classes` is Go only (B335). | Each case hands over with its own reason: a `.code-forge.yml` Go cannot read in Node's key order, or a non-list truthy `gates.custom` (`gates-detect-config`); an acceptance file Node's `JSON.parse` rejects or that holds a lone surrogate (`gates-acceptance-json`); a TAP file that is not valid UTF-8 (`gates-tap-encoding`); a transcript with production markers or names (`gates-transcript-extras`) or in a format Go's transcript reader does not know (`gates-transcript-unsupported`; Codex exec `--json` logs run in Go since B466); both Git steps failing (`gates-git-race`); a Git call that times out, is killed or prints over 10 MiB or non-UTF-8 output (`gates-git-spawn`); a file error Go cannot word like Node (`gates-fs-error`). |
| `run start`, `run status`, `run end`, `run reload` | `run status --run <id>` (the same JSON, spend and budget). `run end --run <id>`: the pin check, the stop of a verified worker and the signed `worker.down`, `worker.replaced` and `run.end` rows. `run start --reattach --run <id>` with `--worker-pid`, or without it to re-pin the live worker that the queue announces (B454). `run --help`, `run <sub> --help`, `-h`, usage errors and unknown flags. A plain `run start` (B468): the config snapshot and its hash, the run record, key, mirror and signed `run.start`, `run.mode` and `review.external` rows, the removal of dead temp roots, the worker launch and its pin, or the stop of a launched worker that was not pinned. `run start --add-worker --run <id>` with the `review.workers` limit and the signed `worker.add` row. Since B511/B512 both launch this binary's `code-forge worker --run <id>` (the Go worker, detached, with the caller's environment and no stdio), wait up to 15 seconds for its announcement and pin that pid, so neither needs Node; a run the Go worker does not serve becomes the Node worker in the same process (see `worker`), and the pin and output are the same. The process sweep also stops a Go worker (`code-forge-native worker --run <id>`) of a finished run. `run reload` refusals: no run, an ended run, no `.code-forge.yml`, a config that is not a mapping, one that cannot be migrated or read. The process sweep of a plain `run start` (B472): it stops a worker of a finished run, an orphan whose owner is gone and a registered child of a dead temp root with Node's checks (SIGTERM, a 2-second grace, SIGKILL). A duplicate key or a tag that does not resolve in `.code-forge.yml` (B472: `run start` prints Node's message, `run reload` its line). `run reload` of a config that loads (B473): config rule errors, kept frozen keys, pins for queued review tickets, the run record, its workspace mirror and the signed `run.reload` row under the run lock, and Node's refusals (frozen keys, autopilot limits, a missing snapshot and an unreadable review queue). `run human-checks list --run <id> [--json]` and `run human-checks clear --run <id> --block <id> --clause <C<n>> --note <text> [--json]` (B631) run only in Go: the run's open `human_check` clauses (from the run record, minus verified `human_check.cleared` and `block.human_pass` rows) and the owner's signed `human_check.cleared` row (a terminal yes, refused under an active autopilot grant). | A temp base the sweep cannot read; any other YAML syntax error in `.code-forge.yml` (Node words it); for a `run reload` of a config that loads: schema errors (`run-reload-schema-issues`), secret-shaped values (`run-reload-secret-values`), a non-empty `coder.sandbox.add_dirs` (`run-reload-sandbox-dirs`), an explicit YAML `!!` tag (`run-reload-yaml-tag`), a workspace that holds the runs folder (`run-reload-workspace`), record shapes Go does not expect (`run-reload-record-shape`), an autopilot grant that is not an object (`run-reload-autopilot-grant`) and a `review` value that is not a map (`run-reload-review-shape`); a record that changes between the check and the run lock hands over with the reason the second check finds. A run record that is not JSON, or with a shape that makes Node fail, a ledger Go cannot read, a worker registry Go cannot judge, and no `HOME`. |
| `resolve <level>` | The level lookup with its fallbacks (B455), `--help`, usage errors and an unknown level. | A `.code-forge.yml` with a YAML syntax error (`config-parse-error`) or a `fallback` that is not a list of maps (`resolve-fallback-shape`). |
| `models` | The model list and `--refresh --from-cli-caches` (B455), with the same models cache file, `--help` and usage errors. | A YAML syntax error (`config-parse-error`), a models cache Go cannot write (`models-refresh-unwritable`) and a `models_cache.json` that is not valid JSON (`models-refresh-invalid-json`). |
| `list`, `remove` | Both commands, `--help` and their usage errors (B455); `remove` deletes the same installed targets and rewrites the installs file. | `remove` with a target outside the safe places (`remove-unsafe-target`) or a target or installs file Go cannot change (`remove-unfinishable`). |
| `pipeline status`, `pipeline next` | Both forms with `--json` and `--root` (B455): step states, duplicate, unknown-need and cycle problems, and a missing pipeline. Since B500 also an outcome file Go reads like Node's `checkOutcome` (for example a folder, `EISDIR`) and, on a terminal, the router's once-a-day error-log hint (`~/.code-forge/logs/hints.json`). | A pipeline or outcome with a YAML syntax error or a shape the schema rejects, and a read error other than a missing file or a folder (`pipeline-unsupported`); a hint state Go cannot judge like Node, such as a time only `Date.parse` reads (`router-hint-state`). |
| `outcome seal`, `outcome check`, `outcome record` | `seal` and `check` (B455), and `record` (B471): every problem line, `--json`, `--root`, `--step` (else the pipeline step whose outcome is the file), usage errors, and a passing outcome signed as Node's `outcome.recorded` row under the run lock (`already recorded` when it is). | A schema error, an outcome Go cannot rewrite or decode (`outcome-unsupported`), and for `record` no home folder, a pipeline Go cannot read or a config with a YAML syntax error (`outcome-record-unported`). |
| `validate` | Every rule with Node's messages, order and exit codes (B471): errors and warnings, seen-in-cache model ids, CLIs on `PATH`, pipeline checks, `--file`, usage errors and a missing config. | A config the schema rejects (`validate-schema-issues`), a config holding a secret-shaped value (`validate-secret-values`), a non-empty `coder.sandbox.add_dirs` (`validate-sandbox-dirs`), a YAML syntax error (`config-parse-error`) and a pipeline Go cannot read (`pipeline-unsupported`). |
| `plan check` | `--help`, usage errors, flag errors and a plan or `--facts` file that cannot be read (B471); the check itself with facts, `--slug`, project config caps and budget, ledger lanes, claims, overlaps, cycles and imports (B475). | Config caps or budget that are not plain numbers (`plan-check-limits`), a broken `.code-forge.yml` (`config-parse-error`), plans or facts sheets on which Node throws (`plan-check-node-throws`) and a ledger that cannot be read (`plan-check-ledger-read`). |
| `sweep` | Every form (B486): `--help` and `-h`, usage errors and unknown flags (with the breadcrumb and failure log), the dry run in text and `--json` (workers of ended, unknown or idle runs, orphans whose owner is gone, zombies by parent, children of dead temp roots and the memory totals) and `--apply` with Node's checks and signals (SIGTERM, a 2-second grace, SIGKILL). | No `HOME` or a relative one; a temp base the sweep cannot read; an idle check Go cannot judge as Node does (a date outside the forms Go parses, a run record Go cannot decode, a workspace that is not a string). |
| `upgrade` | `--help`, `-h`, usage errors and a `--source` that is missing or not a folder (B471); re-pointing the recorded installs (link or copy) with the release notes and docs sync, also with no `--source` (B475). | A Go binary outside a full package (`upgrade-package-root`), paths that are not UTF-8 (`upgrade-path-encoding`), control characters in a path or the output (`upgrade-control-chars`), no usable HOME (`upgrade-no-home`), installs or targets that cannot be written (`upgrade-unfinishable`), an installs file that is not UTF-8 (`upgrade-ledger-encoding`), a target inside the package or nested in another target (`upgrade-unsafe-target`), a copy source holding links or special files (`upgrade-copy-entry`) and versions Node orders differently (`upgrade-version-order`). |
| `tools`, `tools install` | The tool list in text and `--json`, `install` with `--dry-run` and `--yes`, the plan, each install command and its result, and every usage error (B455). Since B563 each `--version` probe and install child (and each `init` step 1 install) is recorded in the run root's pid registry like Node's exec: `pids/<pid>.json` with pid, start time and command while it runs, removed when it ends, the lazy run root removed after; a registry that cannot be written never stops the child. | `install` at a terminal without `--yes`, which asks Node's yes prompt (`tools-confirm`), and a `--version` line whose cut splits an emoji (`tools-unsupported`). |
| `doctor` | `--help`, `-h`, usage errors and unknown flags, and the whole `--quick` run in text and `--json` (B474): the config row, harness links, the Jev key from its env variable, the macOS keychain read through `security` (B610: an empty keychain, a stored item named `keychain` or `security-cli` as Node names the backend, an expired item deleted with its index row, an item cut to the cache hours, an `op://` read cached in the keychain) or the file store, its expiry warning, a malformed key reference, an expired cached key (deleted with its index row), a cached key cut to the cache hours (entry and index row rewritten), a 1Password `op://` read with `op read` (retried once after a first-call timeout, logged as an `op_retry` warning) cached with its index row, each 1Password failure reason (B483), and the reviewer rows from the project ledger, with the resolved key masked as Node does. The full run with no `.code-forge.yml` in text and `--json` (B504): config, links, keys, tools, ledger, locks and worker registry, tmp roots, the read-only process sweep and memory rows, review state, review concurrency, the skipped probes, signer, native binary and native daemon rows, with Node's exit code, summary and error log. Since B525 the full run with a valid config, in text and `--json`, byte-equal to Node: the rows above plus the Jev live call (B505: Node's request, retries, timeouts and failure kinds), the `cli.<name>`/`flags.<name>` rows of each configured provider CLI, the gates, tools, sandbox and worktree indexing rows, the ping rows of each role (facts, coder, reviewer, s2, and the delegate pings when `autopilot` is set) on anthropic and xai with Node's argv, budget, timeout, schemas and rate-limit retries (B523), the isolation, path-deny and Codex rules rows, and the worker and signer probe (B524: a throwaway run and repository, its worker started, announced and stopped, nothing left behind), with the key and run keys masked, Node's summary and exit code. Every case Go cannot serve is decided before the first probe writes or starts anything, so a hand-over leaves no file, session or worker. Since B535 the probes' I/O errors finish like Node: a temp root the doctor cannot create gives Node's `probes: no temp root (...)` row with the signer row and no CLI, session or worker; a session that cannot start gives Node's `ping=error(<code>)` and `probe inconclusive (error: <code>)` rows; a prompt file, temp repo, work dir, Codex rules or canary file Node cannot write ends the run with Node's crash line `code-forge: verb "doctor" failed: Error: <message>` and exit 1, the run root removed; an errno Node has no name for prints `Unknown system error -<n>` (Node 24's text). Since B567 every child the doctor starts (each CLI `--version` and `--help`, the pings and isolation and path-deny sessions, the temp repos' and the worker probe's `git init`, the locks check's and the worker launch's repo-top `git`, the worktree indexing `git`, and the quick run's `op read`) is recorded in the pid registry like Node's exec: `pids/<pid>.json` with pid, start time and command while it runs, removed when it ends; the full run's `doctor-<pid>-<hex>` root holds them, and the quick run, or a doctor whose root cannot be made, uses the lazy `proc-<pid>-<ms>` root removed at exit; a registry that cannot be written never stops the child. | With a valid config, before any probe: a relative `tmp.root` (`doctor-run-tmp-root`), a provider CLI whose `--version` line Node cuts through a surrogate pair (`doctor-run-cli-version`), a Codex ping, path-deny or open-book isolation session, or a level or project doc whose row text Go does not reproduce (`doctor-run-ping`, `doctor-run-isolation`, `doctor-run-path-deny`), a Jev key or config Node's call throws on (`doctor-run-jev`), no worker binary or no absolute `HOME` (`doctor-run-worker-probe`), a live worker of the project (`doctor-full-live-worker`); during the probes, only a worker registry Go cannot judge (`doctor-worker-registry`, B532: the probe stops its worker and removes its run first; B535 runs it before the other probes). A config that loads but fails validation (`doctor-full-invalid-config`, B504: its tmp root, daemon setting and worktree folder feed the rows); with no config, a live worker of the project (`doctor-full-live-worker`: the daemon ping), a worker registry Go cannot judge (`doctor-worker-registry`), a native binary that is not this one (`doctor-native-binary`) and a review-state record Go cannot read like Node (`doctor-review-state-record`); a config Go cannot judge keeps `doctor-full-run`; a config the schema rejects, one holding a secret-shaped value or a non-empty `coder.sandbox.add_dirs` (`doctor-config-schema`, `doctor-config-secrets`, `doctor-sandbox-dirs`); a YAML syntax error (`config-parse-error`); the OS keychain key store where Go cannot open it: off macOS (Node's Secret Service or Credential Manager), a `security` that fails, or a keychain item Go cannot decode (`doctor-key-store`); a key store `index.json` Go cannot read back exactly like `JSON.parse` (a lone surrogate escape, a number out of range, or a non-object row to shorten) when the key read must rewrite it (`doctor-key-refresh`); ledger rows whose dates or keys Go does not read exactly like Node (`doctor-ledger-shape`); and no `HOME` (`doctor-no-home`). |
| `keys list`, `keys set`, `keys test`, `keys remove` | With the file key store (`CODE_FORGE_KEY_BACKEND=file`): `list` (also the macOS keychain list), `set <name> --cache-hours <h>` and the usage errors, `test` for a key from the environment, an `env:` or `keychain:` reference or the file store, and `remove` with its index update. Since B610 on macOS with the default backends, `test` reads the keychain through `security` first (an expired item deleted with its index row, an expiry cut to the cache hours), then the file store, and `remove` deletes the keychain item and the key file, counting both. `keys --help`, `keys <sub> --help`, usage errors and config errors print as in Node; a key value is never printed (B456). | A typed `keys set` (hidden prompt), every 1Password (`op`) path, an `op://` reference with no value in the environment, keychain-backend `set`, a `test` whose `security` read fails, the default backend chain off macOS, and an index row Go does not expect. |
| `report` | `report --slug <slug>` as text, `--json` and `--export <dir>`: all 18 sections, `awaiting_human_check` and `flaky`, from the slug's ledger and its rotated files. Help, usage, slug and missing-ledger errors (B456). | A ledger row whose bytes depend on JavaScript coercion (a block or run that is not text, a `null` row, an hour of 24, a lone surrogate, a broken line that is not the last one) and an export folder Go cannot write. |
| `ledger outcome`, `ledger add coder`, `ledger outside-review` | `ledger outcome --pr <n> --ci red\|green\|reverted` and `ledger outcome --scan-git` (with `--cwd` and `--days`), `ledger add coder` and `ledger outside-review`, with the same output, exit codes, signed ledger rows and error-log entries as Node; `ledger --help`, `ledger <sub> --help`, `-h` and usage errors (B457). | Ledger rows Go cannot read like Node (`ledger-rows-format`), a run record or run key Go cannot read and no `HOME` (`ledger-run-record`), a git that cannot run or gives odd output (`ledger-git`), values that JavaScript prints in its own way (`ledger-js-value`) and a once-a-day hint state Go cannot judge like Node (`ledger-error-hint`). Since B500 Go prints the hint on a terminal itself and keeps `hints.json` like Node. |
| `logs clear`, `logs report` | `logs clear` with no log, `--yes`, the refusal without a terminal, and (B500) the confirm prompt when stdin and stdout are terminals with its cancel (exit 1, the error log and the hint); `logs report` with nothing to report; (B554) `logs report --no-ai` with matches: the npm version check (newer, failed or unknown), the built-in scrub and counts, the secret check, the report cut to 15686 bytes, `--dry-run`, `--yes` with `--allow-old`, the `gh issue list` search with the comment on an earlier issue, `gh issue create` with its label retry, the prefilled link when `gh` is missing, not signed in or fails, the saved report and the refusals (exit 1 and 2), and (B559) the pid entry of each `npm` and `gh` child under the temp run root while it runs, like Node, and (B585) the `- Node:` line with the version of the node that runs now (`unknown` when no node answers, where Node cannot run at all); (B588) a long path left in a message after the scrub (a path outside the home and project folder, the tail after `<project>`, or the project folder when run from elsewhere) refuses as a high-entropy string with Node's report line, exit 1 and error-log row, and a short path reports like Node; `logs --help`, `logs <sub> --help`, `-h`, unknown subcommands, usage errors and no `HOME` (B457). | The `logs clear` prompt with a terminal on stdin only, an unanswered prompt or a log Go cannot read safely (`logs-clear-prompt`) and a `logs report` with matches that needs Node (B554): the AI scrub without `--no-ai` (`logs-report`), `--with-doctor` (`logs-report-doctor`), a non-empty known-fixes table (`logs-report-known-fixes`), an unknown OS release (`logs-report-system`), a terminal prompt (`logs-report-prompt`: stdin is a terminal without `--dry-run` or `--yes --allow-old`) and an entry Go cannot read like Node (`logs-format`). |
| `worker` | `--help` and `-h`, usage errors and unknown flags, a bad `--poll-ms`, a bad or unknown run id (exit 1) and a run that has ended (exit 1), with Node's text (B460). Since B489 `worker --run <id>` serves the queue in Go: the boot lock, the registry and announce, the heartbeat, the daemon, the run root (a review session's child pid entry lands under it, like Node), the ticket and provider caps, the autopilot grant expiry, the ticket reviews, the orphan sweep after a drain, the idle exit and a clean stop on SIGINT or SIGTERM (`worker: stopped`); `--once` drains once (`worker: drained N ticket(s)`). Same stderr, rows, queue and run-root files as Node. Since B534 the commit watch (`review.file_loop: watch`) runs in Go too: each pool tick, while the worker is not stopping, reads every open block's HEAD (its worktree, else the repository), waits 15 s without a new commit (at most 120 s per burst), then queues one ticket per owned, not excluded file the commits changed, hashed from git objects, with Node's `watch.skipped` and `watch.held` rows, superseded markers and `.code-forge/queue/watch/<block>.json` cursor, byte-equal to Node, under Node's watch lock. | A watch shape Go does not reproduce (bytes Node decodes with replacements, changed file names that are not UTF-8, `owned_kinds` as a list, a `review.exclude` or reopen time Go cannot read like Node) stops the Go worker before that tick writes a ticket, and Node serves the run (`worker-watch-shape`). Go gives the whole worker to Node, before it writes anything, when the Jev key sits where Go does not read it like Node: the OS keychain with the default key backends where Go cannot open it (`worker-jev-keychain`: off macOS or a failing `security`; since B610 a macOS keychain key is read in Go through `security`), a key store file or index Go cannot read back, or a 1Password `op://` reference that needs a live `op read` (`worker-jev-key-store`), or a key reference Node refuses at boot (`worker-jev-key-ref`); also when the run has no config snapshot (`worker-config`), the record or a boot read is one Go cannot judge (`worker-shape`, `worker-boot`, `worker-record`) and with no `HOME`. The split rule: on the first ticket Go cannot review (or an autopilot approval or grant it does not decide) the Go worker takes no new ticket, lets running ones finish (each writes its own done marker), swaps its announcement for a hand-over one and becomes the Node worker (`worker-ticket-unported`), which reviews every ticket still pending once. Since B561 the hand-over announcement (`<pid>-<start time> hand-over.json`, Node's announcement body) keeps the queue served for a `review-file --wait` while Node starts, so the waiter no longer reads the hand-over as `worker_down`; Node's boot prunes it (its start time is not the process's), and a failed exec into Node removes it. With no `ps` start time Go withdraws its announcement as before. No ticket is lost or reviewed twice. Go polls the queue every `--poll-ms` (no file watch). A plain `run start` and `run start --add-worker` launch this Go worker (B511/B512). Since B485 the Go package `native/worker` has the worker's boot and pool: the boot lock, the registry admission, pins and the `worker.reattach` row, the announce and heartbeat files (byte-equal to Node), the clean stop, the queue order, the ticket claim (Node's lock file) and the ticket and provider caps. Since B488 `worker.TicketRunner.RunTicket` runs one ticket's refusals in Go, byte-equal to Node: a bad ticket, a bad path, another run, a stale or missing file, a worktree mismatch, an unreadable file or symlink, a gone or malformed block worktree, an unreadable run record and a missing config snapshot. It holds Node's file lock and writes the signed result, the ticket timings, the done marker and the signed `review.result` row, or the `worker-error` result (poison when even that fails). Since B492 it also routes the ticket like Node: `review.routes` (a list it cannot use is `unavailable` `route-invalid`), adaptive L2 routing (configured order, measured performance, exploration, the ticket's earlier `review.route` row) and the signed `review.route` rows (`route-row` when one cannot be written), then the engine's first refusals: `review.exclude` (`excluded`), an unreadable run record, an unknown block, a block with no base, a malformed block worktree and an amended acceptance (`stale`). A ticket that needs a review session (context, risk and facts, the fix loop), the other worktree codes and a ledger or route config Go cannot read like Node return an unsupported error with nothing written, so Node takes them. Since B609 Go serves an answers ticket itself, as Node does: the same-file, not-owned, file-missing and not-open refusals, a stale answering file or source, the answers packet (a new file in full or a tracked file's diff), the L2 answers session, the verdicts and the signed `review.answered` row; a non-UTF-8 file, a finding line, severity or state Go prints unlike Node and a missing run key still hand over. Since B495 the Go packages also have the fix loop's parts, byte-equal to Node but not yet called by the worker: `review.RunRound` (a full round: triage without Jev, the `review.triage`, `review.round`, `review.approved` and `review.cap` rows, escalation and the `ledger_write_failed` retry), `review.BudgetRows` (the block's `review.budget` and `review.over_budget` rows) with `review.RulesRisk`, and `worker.SaveState` (the signed state file, its git blob and the `review.state` anchor row). Recheck and patch-check rounds are not ported. Since B496 `worker.FixLoopRound` wraps one round like Node's `fixLoopRound`, byte-equal to Node's own goldens: the block lock (Node's lock file), the run's rows read once, the block's `review.budget` row written once per block (changed owned files through `git diff` and untracked files), the state load (`review-state-tampered`, `review-state-lost`, a state rebuilt from its signed anchor and saved with `review.state_restored`, an old state file moved to the new path, an amended acceptance restarting the file), the file's L3 rung (a round that could take it runs under the block lock), the approval check (`hash-moved`), a crash saved as `next: retry`, and the outcome (`approval-not-written`, `findings-open`, `no-fix-hunks`, stopped, unavailable). A ticket that needs a `block reopen` row, an autopilot extra round, a stop a raised cap reopens, a recheck or a patch check returns an unsupported error with nothing written. Since B497 `RunTicket` calls it and reviews a ticket end to end when a dry pass, before any write, finds every part ported: the rules risk, the block kind, the acceptance excerpt and worktree line, the rules digest (`context-unreadable` when the rules cannot be read), the owned, changed and test files, paired test context, then one `FixLoopRound` with one L2 session. It writes the route, budget, plan, session, triage, round, approved and state rows, the review state, the signed result, the timings and the done marker (with `open_findings`) byte-equal to Node on the same repo and fake reviewer CLI. Any part the dry pass finds unported (a light block, consensus or dual lenses, Markdown sections, `budget.usd` with no warning seam, a dry-pass read that fails) returns an unsupported error with nothing written and no session started. Since B507 Go also serves, byte-equal to Node, the other worktree codes (`worktree-not-dir`, `worktree-branch-mismatch`, `worktree-foreign`, `worktree-workspace-missing`, `git-failed`), Node's config read (one retry after 200 ms on an unreadable or torn run record, then `config-snapshot`) and the `worker-error` result for an I/O error (an unreadable run record, or a ledger Node cannot open at routing, with `detail` `routing: Error: EACCES: ...`). Still with Node: ledger errors other than a permission error and I/O errors inside the engine's own record reads (unsupported, nothing written). Since B514 Go plays the review's broken-answer steps like Node: a `needs_file` answer runs the file round (the requested files attached read-only, with Node's refusals for secret-like, untracked, unowned, unchanged and gitignored paths, then a second session), a schema miss writes the signed `review.schema_invalid` row with its diagnosis (answer kind, parse error) after the ledger history check, and a second same-model timeout asks `timeoutFallbackFor` for a model the ledger confirms in the role and reviews with it. A fallback Go cannot pick like Node (adaptive options it does not model, two config entries for one model with different effort, a null ledger row) is still recorded as `unavailable` with `next: retry` and never handed over. Since B538 the review session also recovers like Node, byte-equal on Node's goldens: the `levels.L2` fallback ladder (a schema miss, timeout or at-capacity answer moves to the next entry; an openai entry is skipped closed book), `review.routes` recovery (each routed entry once, then `review.schema_fallback` and `unavailable`) and `review.second_levels` (after an earlier failure on the packet, one try on the second level and its ladder, with the `review_schema_fallback` warning; a same-model or closed-book second level is `unavailable`); the timeout fallback takes the ladder, second level and route entries as candidates like Node. Before any write the worker checks that the Go session can run every config the review may spawn on. A malformed shape (a `review.routes` or ladder that is not a list, an entry that is not an object or has no model, a provider or effort that is not text) stays unsupported (nothing written), and so do the recheck session's ladder, routes and second level. Since B517 Go plays the recheck round like Node: the fix hunks, the recheck packet and session (the timeout retry, the coverage and hunk checks, the stub guard), stale and repeated findings, late findings, the `patch_check` round with the L3 rung, the `review.round` and `review.cap` rows, and the reopen of a cap stop (a raised `max_rounds_per_file`) and of a split stop (a raised budget) on the next ticket. Still with Node (unsupported, nothing written): a Markdown recheck section by section, a recheck with no `levels.L2`, and the autopilot `extra_round` and `review.reopened` rows. Since B533 a worker with a Jev key serves in Go: it resolves the key at boot like Node (the env variable, the file store and a still-valid 1Password cache entry; the serving line says `jev key resolved`), and the review asks System 1 (Jev) like Node, byte-equal on Node's goldens: the `defect` question in a full round and on the recheck's late findings (with `source` `jev`, `s2`, `s2-fallback` or `shadow` for a judge's findings, `p` and `band` in the `review.triage` rows), the `resolved` question that closes open findings before the recheck session (all closed: no session), Node's request, retries (0.5 s, 2 s, 8 s), per-attempt timeout and failure kinds, `system1.disable`, `system1.criteria_extra` and `thresholds.findings`, and one `s1_fallback` warning when Jev gives no answer. A `criteria_extra` whose key order Go cannot keep, or `thresholds.findings` values that are not numbers, are unsupported for the ticket (nothing written). The key never reaches the session environment, the rows or the output. Since B545 these run in Go too, byte-equal to Node's goldens: a light block's batch round (every changed file in one full-lens L2 session, the batch and cap rows and per-file outcomes), Markdown files over the budget reviewed section by section, the dual lenses with the L3 judge, consensus review (`review.multimodel`, the second reviewer on `review.second_levels.L2` or `review.second_provider`), the recheck session's recovery (the second level after a schema miss or timeout and its ladder, the `levels.L2` ladder's same-model retry, `review.routes`) and an L2 ladder entry with no provider in adaptive routing. Still with Node (unsupported, nothing written): a judge with no `levels.L3`, a Markdown heading that would cut a surrogate pair, and an adaptive pick that chooses the ladder entry with no provider. Since B537 Go builds the rest of the review packet like Node, byte-equal: related files and definitions (with the import-header context), the deletion note, the test-quality hint and moved-code hints with peer files in a full review (also in a Markdown section and its size measure, the judge packet, and a light round after the cap with peer files), and in a recheck `review.recheck_scope: file` (the packet against the block base), moved-code hints and cross-file evidence from the block's other changed files (the cap, the trim and the cut line). Since B564 a fix-loop recheck with a base is checked against Node's own packet builder for one peer, several peers, a moved-code hint alone, a moved hint plus cross-file evidence, over the budget (the cut line, or nothing) and no peers, and `worker --run <id>` serves such a recheck of a .js file end to end with no hand-over. Still with Node: a `recheck_scope: file` diff that fails to read (Node throws). Since B551 a ticket whose review level is on openai runs in Go when `review.allow_open_book_codex: true` lets the Codex reviewer run (Node's `codex exec` argv, the packet on stdin, the OpenAI-strict schema file, the `-o` answer file, the null-drop and coerce of the finding answer, usage and cost rows), byte-equal to Node's worker on the same repo and fake `codex` CLI; an openai level with an effort the codex builder refuses, or a schema whose OpenAI-strict form differs, is unsupported for the ticket (nothing written). Since B573 the Go worker, the daemon (`internal serve`) and every Go verb record each child they start (`git`, gates, `security`, the proof export) in the run root's pid registry like Node's exec (`pids/<pid>.json` while it runs, the owner pid, start time and run id in its environment), so a sweep can stop an orphan; `ps` probes and children Node also starts without its exec stay as they were. Since B602 the daemon keeps SIGTERM and SIGINT handled until it exits, so a second stop signal (a stop of the worker's process group, then the worker's daemon stop) no longer kills it before it removes that temp root. |
| `spawn` | `--help` and `-h`, usage errors and unknown flags, a config that does not load, a bad `--timeout`, a `--schema` that cannot be read or is not JSON, a bad `--run` id, and a bad `--level` or `--role` when no `--run` is given (B460). A foreground closed-book session with no `--run` for the roles reviewer, judge, s2, author and delegate on anthropic and xai steps, with no schema or the finding schema, and `--print-argv` (B477): the same argv, fallback ladder, rate-limit retries, timeout, codex skip note and closed-book refusal, result line, exit code (0, 1, 2 or 3) and ledger row as Node. The same session with `--run` (B482): the run's temp root, then the run-pinned gate before each step — `budget.usd` (the reached, estimate-above and would-exceed refusals, the spend-unreadable refusal, the 80 % warning row, stderr line and error-log warning) and the autopilot category cap (the paused refusal with the saved pause and one signed `autopilot.pause` row, the unreadable-record refusal) — under Node's `budget:<run>` lock, with the child's `CODE_FORGE_RUN_ID` and pid entry as Node. Since B549 the coder and facts roles and `--background` run in Go too: the coder prompt with `--block` (the block brief and missing acceptance lines from the run record, the TESTS line and the sentinel), the coder `sentinel` result field, `--run` with the run-start `coder.sandbox` check (the snapshot-hash and `sandbox-changed` refusals), the facts session's argv, and the background start (the detached child, `session.log`, `session.err.log`, `pid.json` and the run's pid entry), byte-equal to Node. Since B551 the same session on openai steps (Codex) when `review.allow_open_book_codex: true` lets them run (delegate stays refused): Node's `codex exec -m <model> [-c model_reasoning_effort=<e>] -s read-only --ephemeral --ignore-rules --ignore-user-config -C <session>/cwd --skip-git-repo-check [--output-schema <session>/schema.json] -o <session>/out.json --json -` argv, the packet on stdin, the OpenAI-strict schema file (0600), the answer read from the `-o` file, usage from the JSON events (estimated when none), the finding answer's nulls dropped and `passed` coerced, Node's error events (usage limit as `rate-limited` with its retries, 401 login expired, 503 at capacity falling to the next step), timeout, non-zero exit, the result line and ledger row byte-equal to Node. Since B551r a coder step on openai (Codex) runs in Go too, foreground and `--background`, byte-equal to Node: `codex exec -m <model> [-c model_reasoning_effort=<e>] -s workspace-write [--add-dir <worktree git dirs>] -c sandbox_workspace_write.exclude_tmpdir_env_var=true -c sandbox_workspace_write.exclude_slash_tmp=true -c approval_policy="never" -C <cwd> --json -o <session>/out.json <prompt file>`, a fresh `CODEX_HOME` per session under `<run root>/codex-homes/` (the code-forge rules file and the real home's `auth.json`), the answer and `sentinel` read from the `-o` file, and `outPath` in `pid.json`; the facts role on an openai step runs the closed-book Codex session. Since B556 facts with `--cwd` runs in Go too: the cwd must sit in the run root's facts snapshot area, with Node's refusal text and exit 2 otherwise. | A Codex coder step when `coder.sandbox` is set (`spawn-codex-sandbox`, since B551r), a run record Go cannot read like Node or an odd block or acceptance shape (`spawn-coder-run`), a background CLI that cannot be found (`spawn-background-start`), a coder `--cwd` that is not a directory, a `tmp.root` that is not a string or that the run root refuses, an autopilot grant whose `until` or `started_at` date may parse differently, a level or fallback step Node rejects later (no level, no model, an unknown provider or effort), a `--schema` other than the finding schema or not UTF-8, a brief that cannot be read, and no `HOME`; an openai step with an effort the codex builder refuses, or a `--schema` whose OpenAI-strict form differs from it (since B551). |
| `review` | `--help` and `-h`, usage errors, not inside a git repository, a base that does not resolve, a bad `--files` path, and the whole run when nothing is left to review (text or `--json`, with the skipped-file notes) (B460). Since B553 Go runs the review of one file: config, `--acceptance` and `--intent`, `--run` reuse and an ended run, the run start, the review block, the ticket, the wait, block stop, run end and the worker reap, Node's progress lines, fix list, totals, `--json` and exit codes, and the signals (first: block stop and run end; second: a quick stop, exit 1). Since B561b Go also runs several files and test-like files (byte-equal to Node goldens). | No `HOME` (`review-home`), a config Go cannot load like Node (`review-config`), an `--acceptance` file Go cannot check like Node (`review-acceptance-shape`), a `--run` record that cannot be read (`review-run-record`), no own binary path (`review-self-binary`), a temp root that cannot be made (`review-tmp-root`) and a git error (`review-git`). Go decides each before it writes anything. Since B476 the Go package `native/review` reviews the ticket on the single-reviewer path in the Go worker. |
| `autopilot status`, `autopilot stop`, `autopilot start`, `autopilot round`, `autopilot level`, `autopilot binnacle`, `autopilot log`, `autopilot ask`, `autopilot approve` | `autopilot status --run <id>` (text and `--json`, spend per category, paused categories, the one signed `autopilot.expire` row when the window is over) and `autopilot stop --run <id>` (the signed `autopilot.stop` row and the record update), with or without a link: a grant with no link writes and refreshes the binnacle files as Node does (B478). `autopilot start --run <id> --yes`, with no terminal (refused, exit 2, as in Node), and at a terminal without `--yes` (the same yes/no prompt; anything but yes cancels) (B478): every input check, the signed `autopilot.grant` row and the grant record. `autopilot binnacle` and `autopilot log` (B478). `autopilot round` (the signed `autopilot.extra_round` row) and `autopilot level` (the recorded lane, the level rule and the signed `autopilot.level` row) with Node's checks and refusal texts (B478). Like Node, once a command has read the run's signer key (a signed write, or the decision check of `round` and `level`), every stdout and stderr line and every binnacle field masks the key as `[REDACTED]`; before that point nothing is masked, as in Node (B478). `autopilot --help`, `autopilot <sub> --help`, `-h`, usage errors, unknown flags and the `approve --yes` refusal for every subcommand (B461). A run with active approvals that are not due yet (B484). `autopilot ask` up to the delegate session: every usage, option and subject check, an unknown scope, and every grant refusal (`no-grant`, `run-ended`, `stopped`, `expired` with its one signed `autopilot.expire` row, `denied`, `not-allowed`, `paused`) in text and `--json`, exit 1, as in Node (B484). `autopilot approve` up to the owner's prompt: every input check, fixed keys, an ended run, an active approval on an overlapping key, and the no-terminal refusal (exit 2) (B484). A due approval whose check needs no `.code-forge.yml` write and no run reload: a step whose signed row is still pending, a key with a prototype segment, a config that is missing or unreadable, a value changed by hand (the signed `autopilot.restore_skipped` row) and an ended run whose file already holds the old value (the signed `autopilot.restore` row), with Node's lines, approvals lock and record update (B490). `autopilot approve` at a terminal up to the change: the same yes/no prompt, wrapped as clack wraps it, the cancel text (exit 1), a failed prompt (exit 1), `--until` checked again after the answer, and the refusals for no config, an unreadable config and a key that already holds the value (B490). A due approval of an ended run whose `.code-forge.yml` needs the old value back is written in Go with the `yaml` package's exact bytes (comments, blank lines, quoted text, flow and block lists, document markers, CRLF, no final newline), through a temp file and rename that keep the file's mode; a parent that is not a mapping and a write that fails are the same WARN lines as Node's (B494). `autopilot ask` when the grant lets the question through: the delegate model session (the same prompt, lens, schema and CLI arguments, the budget gate, the signed `session` and `autopilot.decision` rows), an answer acted on, a session that fails, times out, is refused by `budget.usd` or answers outside the schema or the options, in text and `--json`, exit 0 or 3, as in Node (B536). `autopilot approve` after the owner's yes: the `.code-forge.yml` change with the `yaml` package's exact bytes, the run reload past the grant's limit guard, the signed `autopilot.approve` row and the stored approval, and the refusal for a key under a value that is not a mapping (exit 2); a due approval of an active run is written back and the run reloaded in Go too (B536). | an `ask` delegate session Go does not run like Node (`autopilot-ask`: a run with no config snapshot, a context file that is not UTF-8, a grant whose scopes or level are not text, a session the Go spawner does not serve), `waive` (human-only grant use and issue tracking), an `approve` change in a YAML shape Go does not write byte for byte (`autopilot-approve-yaml-shape`) or an edited config Go does not load or check like Node, such as one Node calls invalid, `!!` tags, secret-shaped values or sandbox directories (`autopilot-approve-reload-shape`; the same for a due approval of an active run: `autopilot-restore-reload-shape`), an approve prompt with text that is not printable ASCII or a lone surrogate escape (`autopilot-approve-value`), a config that changed while the owner was asked (`autopilot-approve-changed`: Node asks again), a config Go reads differently from Node, such as a BOM, bytes that are not UTF-8 or a number that is not finite (`autopilot-approve-config`, `autopilot-restore-config`), a due approval whose file needs the old value written back in a YAML shape Go does not write byte for byte (`autopilot-restore-write`: tabs, anchors, block scalars, a second document, long text the `yaml` package folds, and a second due approval read after a Go write), stdin at a terminal with stdout not (`autopilot-prompt`), and an approval or grant shape Go does not expect; a `round` or `level` whose block name, file path, plan table, lane answer or decision row Go could read differently from Node (for example a block named `constructor`, a path with `./` or `//`, a non-ASCII table header or lane answer, an object decision); a run record Go cannot read, a ledger or spend row Go cannot read, and no `HOME`. Go writes nothing before it hands over. |
| `help`, `version`, `init`, `native-status`, `facts`, `author`, `s2`, `jev`, `proof` | `help` (also no verb, `--help` and `-h`) and `version` (also `--version` and `-v`) with Node's text. `--help` and `-h` of every one of these verbs, their usage errors and unknown flags, with the same breadcrumb and error log (B458). Since B550 `author`, `s2` and `jev ask` run in Go. `author` and `s2` on an anthropic or xai level: the prompt, the provider CLI call and its argv, the answer schema check, the draft or answer output, the ledger rows and their cost, and Node's refusals (bad `--job`, no or stale facts sheet, an unreadable file, a bad run id, a packet with no question or too long a question). `jev ask`: `--rules` answers, the Jev request with the key from its env variable or the file store (`CODE_FORGE_KEY_BACKEND=file`), its retries and failure lines (`network`, `unavailable`, `unauthorized`, `invalid_request`, `unexpected_status`), a missing answer, the decision bands and `thresholds`, the `keys.cache_hours` warning, the `decision` ledger row, and the refusals (an unreadable `--state`, a disabled question, no key, a config Node cannot load). `proof baseline`, `proof tier` with or without a project config (the config rules, B462), `proof lock`, `unlock`, `restore` and `export` (also `--remove`) with the same output, exit codes, run record, lock, journal, export tree and signed ledger rows as Node (B462), and the `red-green` argument checks (including the `--test` path rules). `proof red-green` itself (B481): the export isolation and the lock, the revert red and the assertion-deletion red, setup commands, the journal and its restore, the signed `export` and `red-green` rows and the JSON result, with Node's output and exit codes; and every project config rule `proof` checks, including `proof.runners`, `review.routes` under multimodel and `coder.sandbox.add_dirs` (B481). Since B502 `init` serves the refusals that come before the wizard writes: every usage error, no `HOME`, a `.code-forge.yml` that cannot be loaded (`read-error`, `migrate-error`, a root that is not a mapping), and the draft-PR refusal (`--coder-may-open-draft-pr yes` without push) when no prompt runs, with Node's stderr, exit code, init log file and agent JSON line. Since B527 `init --no-interaction` (and init with no terminal) sets the project up in Go: gate and proof detection, the settings summary, the merge with an existing `.code-forge.yml`, the config checks and their errors (exit 1 and 2), the `.code-forge.yml` write with the `yaml` package's exact bytes (or `unchanged`, which keeps comments), the skill links (symlink or copy, project or global) and `installs.json`, the user config, the engine stop text, the init log and the agent JSON line, byte-equal to Node. Since B528 `init` at a terminal runs the wizard in Go: every prompt (provider, levels with the re-ask on a bad answer, multimodel, harnesses, link mode, engine, push and draft PR, Solo project), its text, default, choices, keys (arrows, space, enter, y/n, Esc, Ctrl-C, Ctrl-D, line editing), the summary, the gates and proof choice (use, customize, later), the apply confirm for an existing `.code-forge.yml`, then the same setup as B527; the terminal output, files, init log and exit codes (1 on a cancel, 13 with Node's warning when the input ends at a prompt) are byte-equal to Node. Since B548 the setup's steps after the write run in Go too: the Jev key from `CODE_FORGE_KEY_JEV` or the file key store (`CODE_FORGE_KEY_BACKEND=file`, with its 24 h expiry cap and expired-entry removal), `--tools recommended` with `--yes-tool` installs, `--refresh-models`, and the quick doctor (config, links, keys and reviewer rows, its `doctor` lines and JSON summary, exit 1 on a FAIL row), byte-equal to Node. Since B611 the wizard's Jev step and doctor step run in Go too: a Jev key found by the auto step (`CODE_FORGE_KEY_JEV`, the file store or the macOS keychain through `security`) with its `keys:` line, else Node's Jev source prompt (skip, an env variable with its check and re-ask, a pasted key stored masked in the file store or the macOS keychain with its index row, or a 1Password reference, item ID or link resolved with `op item get`, its retry and failure lines), and the doctor run after the setup unless `--skip-doctor`, with the terminal output, files, init log and exit codes byte-equal to Node. Since B555 `facts` serves every refusal Node makes before its session in Go, with Node's stderr, exit 2, breadcrumb and error-log row and nothing written: a `.code-forge.yml` that is missing, unreadable or has a version Node refuses, a `--brief` that is not a readable file, a `--sources` path that is not a readable file or folder, a bad `--timeout` and a bad `--run` id, checked in Node's order. Since B556 the `facts` run itself is Go's too, byte-equal to Node: the claim tokens, the read-only snapshot at HEAD (git archive, the in-project `--sources` under `_sources/`, sealed read-only, secret and ignored files left out), the packet, the L0 facts session on anthropic or xai with its fallback ladder (since B582 also a Codex (openai) step anywhere in the L0 ladder: the read-only `codex exec` argv in the snapshot, the packet on stdin, the OpenAI-strict schema file, the `-o` answer and the usage limit, 401, 503, timeout and exit moves), the answer checks and their downgrade lines, the sheet, the `session` and `facts.built` ledger rows, the breadcrumb and error-log row, and `--run` with its run root. Since B568 these run in Go too, byte-equal to Node: glob path claims (`*`, `**`, `?` and `[...]`, matched like Node's `path.matchesGlob`; since B577 also braces, backslashes, `[:ascii:]` and `[:xdigit:]`, unclosed extglobs and non-ASCII text with Node's case folding), a `--brief` that is not UTF-8 (decoded with replacement characters like Node), a run root inside a git work tree (the snapshot moves to Node's `facts-<hash>` temp root), `--sources` outside the project (mounted at `_sources/<label>` in the snapshot), and Node's crashes with stderr, exit 1 and error-log row: no `levels.L0` or no model or provider on it, a relative or non-text `tmp.root` with `--run`, an untrusted tmp base, and the facts cwd refusal inside a git work tree. | `init` at a terminal Go does not draw like Node (`init-interactive`: `NO_COLOR`, `FORCE_COLOR`, `NODE_DISABLE_COLORS`, `CI`, `TEAMCITY_VERSION`, `ACCESSIBLE` or `NO_UNICODE` set, a `TERM` other than xterm, screen, tmux or rxvt-unicode, fewer than 60 columns or 24 rows); in the wizard, `--yes-tool` (`init-wizard-tools`), `--refresh-models` with a cache Go cannot write or read (`init-wizard-models`), flags or a config that preset levels, Solo project, gates or proof in a form the wizard shows differently (`init-wizard-shape`), and a launcher file whose end-of-input warning Go cannot reproduce (`init-wizard-eof`), all decided before the first prompt, and since B611 a Jev key store or index Go cannot read back like Node (`init-jev-key`) or a keychain key the Jev auto step cannot open (its own reason, as in the setup below), also decided before the first prompt; in the setup, a Jev key from the OS keychain where Go cannot open it (`init-jev-keychain`: no `--no-jev`, `--jev-env`, `--jev-ref`, `keys.jev` or `CODE_FORGE_KEY_JEV`, the default key backend, and off macOS, a failing `security` or an item Go cannot decode; since B610 a macOS keychain key is read in Go through `security`), a file-store key file or index Go cannot read back like Node (`init-jev-key`), a 1Password item ID (`init-jev-op`), `--refresh-models` with a models cache Go cannot write like Node (`init-models`), a doctor that hands over on its own reason, such as `doctor-key-store` when it reads the keychain (decided before init writes anything), a link Go cannot place like Node (`init-links`), an existing `.code-forge.yml` Go cannot rebuild byte for byte, such as anchors, tags or a value of an unexpected type (`init-config-shape`), a merged config with a schema issue, a secret-shaped value or `coder.sandbox.add_dirs` (`init-config-check`), and a user config Go cannot read like Node (`init-user-config`), a `--jev-ref` 1Password link (`init-jev-link`), an argument that is not UTF-8 (`init-arg-encoding`), a `.code-forge.yml` YAML syntax error, BOM or non-UTF-8 bytes (`init-config-syntax`), an existing provider or levels Node's answer step may throw on (`init-answers`), a `HOME` or project path that is not UTF-8 or holds U+2028/U+2029 (`init-output`), a glob path claim whose segment needs a Unicode POSIX class (every `[:class:]` but `[:ascii:]` and `[:xdigit:]`), has an extglob opener (`!(`, `?(`, `+(`, `*(`, `@(`) with a `)` after it, or ends a case-insensitive `*`/`?` suffix with a sigma (`facts-glob-claim`, since B577), and a config, schema, home or session shape Go cannot serve like Node, such as an autopilot grant or ledger row whose date Go may parse differently (`facts-session`, decided before any write since B568), a `--run` root Go cannot create, or a gate whose files change during the run (`facts-session`, everything made removed and the breadcrumb put back), a `facts` call whose `.code-forge.yml` has a YAML syntax error (`facts-config`: Go's parser words it differently); for `author` and `s2`, decided before any session or write: an openai (Codex) level or a provider shape the Go session does not take (`author-provider`, `system-two-provider`), an input file that is not UTF-8, a packet number or value Node words differently, a brief Go cannot name or an output path Node cannot write (`author-input`, `system-two-input`), a YAML syntax error or a config value Node throws on (`author-config`, `system-two-config`), no absolute `HOME` (`author-home`, `system-two-home`); for `jev ask`: a `--state` file that is not UTF-8 or JSON, or not an object (`jev-ask-state`), a YAML syntax error, a relative `--cwd`, a bad slug or band (`jev-ask-config`), a key in an OS keychain Go cannot open (off macOS, a failing `security`; since B610 a macOS keychain key is read in Go through `security`), a 1Password read or an expired or too-long cached key (`jev-ask-key`), a question payload Go cannot build (`jev-ask-payload`), a Jev answer Node's decision throws on (`jev-ask-answer`, after the call, before any write) and no absolute `HOME` (`jev-ask-home`), a project config Go cannot load for a reason that is not a load error (`proof-config`), a `proof red-green` whose snapshot Go cannot read (`proof-snapshot`), that uses a `proof.runners` entry or the Pest default for a `.php` test (`proof-runner`), or that meets another error before the export (`proof-red-green`), a `proof export` with a git step, run record or `proof.export` list Go cannot read (`proof-run`), and `help` or `version` when Go cannot find its install. Each hands over with its own reason. |
| `native-status` | The private daemon helper (B501): one status line per standard-input line (`{"runs":[...]}`, or `{"ok":false,"error":"status-unreadable"}` for a line Node cannot read), `\n`, `\r\n` and lone `\r` line ends as Node's readline, a missing `rows` read from the ledger, then the router's `has no default export function` line with exit 1, the breadcrumb and the error log. `--help` and `-h` print the usage. | No `HOME`, and a terminal on stderr with no agent set (the router's hint) hand the whole command to Node. A line with a block id outside printable ASCII among two or more listed (not closed, not stopped) blocks, which Node must sort (since B576 one listed block or such ids on closed and stopped blocks only are answered in Go), a number Node reads as `Infinity`, a lone surrogate escape or bytes that are not UTF-8 hands that line and the rest of standard input to Node's helper. |
| `setup` | Every form when the binary runs (B463): the OK line, a version mismatch, `--help` and usage errors. | Nothing. With `CODE_FORGE_NATIVE=0` the entry runs the Node router's `setup`. When the binary is missing, not executable, unsupported, or exits other than 0, 1 or 2, the shell entry itself prints the install fix and exits 69; no Node runs (B569). |
| `run status`, `run end`, `run start --reattach` | `run status --run <id>` (the same JSON, spend and budget). `run end --run <id>`: the pin check, the stop of a verified worker and the signed `worker.down`, `worker.replaced` and `run.end` rows. `run start --reattach --run <id>` with `--worker-pid`, or without it to re-pin the live worker that the queue announces (B454). `run --help`, `run <sub> --help`, `-h`, usage errors and unknown flags. | A plain `run start`, `run start --add-worker` and `run reload` (the worker launch, the process sweep and the config snapshot). A run record that is not JSON, or with a shape that makes Node fail, a ledger Go cannot read, a worker registry Go cannot judge, and no `HOME`. |

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
The reuse stamp holds the version of the node that runs the gates (B585): Go
asks it once per process with `node --version` (`CODE_FORGE_NODE`, else `node`
on `PATH`, 10 s limit). With no node that answers, and no Node version built
into the binary, Go writes no stamp, so no full run is reused.
See [how it works](how-it-works.md) for the scope rules and reuse checks.

A hand-over always passes the original arguments. Node then runs the command
exactly as before. When Go is not sure it gives the same answer, it hands over.
Commands such as the full `code-forge doctor` also go through
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
input. It does not report which engine started a command. Since B503 the
daemon's `status` answer no longer starts that helper: Go writes the same
answer, including `status-unreadable` for a run record or ledger Node cannot
read and a `null` spend that overflows. Since B576 the daemon never starts
Node: a block id outside printable ASCII is ordered in Go when it is the one
listed block or sits on a closed or stopped block; with two or more listed
blocks, Node's ICU sort order is needed, so the daemon answers
`status-unreadable` (as it did with no Node) and the status command computes
the status itself. Although help
lists `native-status`, it is a private helper with no public command handler; it runs in Go since B501.
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
Go answers every `--help` spelling of `block close`, so `close-help` is never recorded; B464 removed it and 76 other reason codes no Go path returned.
`land-merge-gate` is reserved for a full-suite gate configuration Go cannot run.
If Go itself fails with an internal error (a Go panic) on the main goroutine, it never
exits with the usage-error code 2 or prints a stack trace (B508). A command that writes no
state in Go (`setup`, `status`, `run status`, `queue` and `queue status`, `heal list`,
`heal show`, `decisions list`, `docs path`, `logs`, `logs summary`, `logs path`,
`logs handovers` and `ledger tail`) goes to Node with its original arguments and a
`go-panic` row. Any other command may already have changed state, so Node never runs it a
second time: Go prints one line that names the command and the code site, never the panic
text or an argument, writes a `go-panic-stopped` row and exits 1. Check `code-forge status`
before you retry. A panic in a goroutine a command starts (for example the gates that
`block land` runs next to its review wait) gets the same answer, rows and exit codes (B509).
The full code list is the `handoverReasons` table in
[`handover_reasons.go`](../native/cmd/code-forge-native/handover_reasons.go).

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

If the shell entry finds no binary for your platform and architecture, or
the file is missing or lacks execute permission, it stops with exit 69 and
prints the fix (B569). It does not start Node. `scripts/install.sh` in the
package checks and repairs the binary (see below).

## Check the binary: `code-forge setup`

npm runs no install script. After `npm install -g @codedology/code-forge`,
run `code-forge setup` yourself. It checks the binary for this system and
processor. It works offline; it downloads nothing.

1. It picks `native/bin/<platform>-<arch>/code-forge-native` from the package.
2. It checks that the file exists. If the file lost its execute permission,
   setup restores mode 755.
3. It compares the binary's version with the package version.

On success it prints one line, `code-forge setup: OK Go binary <platform>-<arch> v<version> (<path>)`,
and exits 0. On a failure it prints the problem and one `fix:` line, then exits 1.
For an npm install the fix is `npm install -g @codedology/code-forge@<version>`.
In a source checkout it is `node scripts/build-native.mjs --host`.
`--help` and `-h` print the usage; any other argument is a usage error (exit 2).
The same check runs as `npm run setup` in a checkout.

The Go binary serves `setup` when it runs (B463). When the binary is
missing, lacks execute permission, has no build for this platform and arch, or
exits with a code other than 0, 1 or 2 (for example a crash), the shell entry
prints the install fix itself and exits 69; it runs no Node (B569). With
`CODE_FORGE_NATIVE=0` the Node router runs `setup` (`scripts/setup.mjs`), which
prints the same lines as Go. An older binary without `setup` hands it to Node
like any other verb, and Node reports the version mismatch.

### Repair the binary: `scripts/install.sh`

When `code-forge` itself stops with exit 69, run the install script that ships
in the package. It needs no Node, no network and no sudo, and writes nothing
outside the package's `native/bin`:

```sh
sh "$(npm root -g)/@codedology/code-forge/scripts/install.sh"
```

It maps `uname -s` and `uname -m` like the entry, checks that the binary
exists, restores a lost execute bit (`chmod +x`), runs `<binary> --version` and
`<binary> internal version`, and compares the binary's version with
`package.json`. On success it prints this line (shown here in quotes)
`"code-forge install: OK Go binary <platform>-<arch> v<version> (<path>)"` and
exits 0. On a problem it prints the problem and one `fix:` line
(`npm i -g @codedology/code-forge@<version>`, or
`node scripts/build-native.mjs --host` in a source checkout) and exits 1.
`--help` and `-h` print the usage; any other argument exits 2. npm runs no
install script (B463), so run it after an install or to repair one (B569).

## Switches

### Use Node only

```sh
CODE_FORGE_NATIVE=0 code-forge version
```

Set `CODE_FORGE_NATIVE` to exactly `0` to skip the Go entry and disable native
helpers and the daemon. The shell starts `node` from `PATH`. This is the
explicit maintainer and test switch; it is the only way the entry runs Node
without Go (B569).
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

The same node gives Go the Node version it writes, like Node's `process.version`
(B585): the `node` field of error-log rows, the gate reuse stamp and the
`logs report` Node line. Go asks it once per process with `node --version` and
takes only a `vX.Y.Z` answer. With no such answer it uses the version built
into the binary, else `unknown`.

## A stale binary in a code-forge worktree

This matters only when you work on code-forge itself. Each block worktree of
the code-forge repo has its own `native/bin/<os>-<arch>/code-forge-native`,
and the Node compat tests run that binary. Two commands keep it current:

- `block rebase` rebuilds it when the rebase brought in a change to
  `native/**` or `scripts/build-native.mjs`.
- `gates run --run <run> --block <id>` rebuilds it before any gate when it is
  missing or older than `native/go.mod` or a non-test `native/**/*.go` file.
  The gate JSON then shows `native_rebuilt: true`.

Both build in Go, with no Node needed (B575). They do what
`node scripts/build-native.mjs --host` does: check `go version`, read the
version from `package.json`, run `go build -trimpath` with the same ldflags and
`CGO_ENABLED=0`, `GOOS`, `GOARCH`, `GOTOOLCHAIN=local` and `GOPROXY=off`, write
`<binary>.<pid>.tmp`, set mode 755 and rename it into place. The Node version
stamped into the binary comes from `<node> --version` of the Node a hand-over
uses (`CODE_FORGE_NODE` when it resolves, else `node` on PATH). With no Node the
stamp is left out, so the binary reports `unknown`, as a plain `go build` does.
A failed build stops the command with exit 1 and a line naming
`native-rebuild-failed` and the script's `build-native:` message. Other
projects have no `native/` folder, so nothing is built for them (B408).

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
`--accept-landed`. Since B578 there is no Node child: active and due
autopilot approvals, odd run records and every `rebase_check` shape run in
Go. Only a narrow state Go cannot read (an approval or grant date Go does not
parse, a block `worktree` that is not an object, unusual storage, an
unreadable review queue, run key, ledger or path) hands over with a named
`rebase-*` reason, decided before any write. The Go rebase, `block rebase --accept-landed`,
`review-file --rebase-check` and the batch land rebase share one rule: replay
first, refuse only a real conflict. Owned files main changed are named
`upstream_owned` in the signed row and queued for review again. A conflict
restores the old head and refuses `rebase-conflict` naming the files. A
`CHANGELOG.md` where both sides only added lines keeps both sides, main's
lines first; the replay runs with `-c merge.union.driver=false`, so a
`merge=union` attribute cannot bring back a line the block removed.
A merge on main is diffed against its first parent only (B589, issue #36):
a feature branch cut before main changed an owned file no longer names that
file in `upstream_owned`, and the open-block `--accept-changed` check uses
`git log -m --first-parent`, so it no longer refuses a false `owned-conflict`.

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
| Not sure the binary works. | Run `code-forge setup`. It prints `OK`, or the problem and the `fix:` command to run. |
| `code-forge` stops with exit 69 (`the Go engine for <platform>-<arch> is missing` or `is not executable`). | Run `sh <package>/scripts/install.sh`: it restores mode 755 and checks the version. If it still fails, reinstall the npm package. For a source checkout, rebuild the host binary. |
| The binary is for the wrong platform or processor. | Install the package for this machine, or rebuild for this host. A platform or arch with no build stops with exit 69 (`no Go engine is built for <platform>-<arch>`). If a wrong binary was put at the matching path, it can fail to start; set `CODE_FORGE_NATIVE=0` while you replace it. |
| The Go hand-over says `node executable … not found`. | Put Node on `PATH`, or set `CODE_FORGE_NODE` to its real executable path. This failure exits 127. If Go is disabled with `CODE_FORGE_NATIVE=0`, fix `PATH`: the shell does not use `CODE_FORGE_NODE`. |
| The doctor shows daemon `fallback`. | Check that a review worker is active and that the package versions match. With no active worker, `fallback` can be normal. Block waits and status still run in Go from the files on disk. To keep the daemon off, set `native.daemon: false` before the next worker starts. |

Contributors can rebuild from the repository root with Go installed:

```sh
node scripts/build-native.mjs --host
```

## Release tools (B570)

The maintainer commands `npm run release`, `release:site`, `docs-block` and `changelog` run a
separate Go program, `native/cmd/cf-release`, built from source by `scripts/cf-release.sh` on each
call. It is not part of the code-forge binary or the npm package, and it has no hand-over: it
does not fall back to Node. Since B574 the Node scripts it replaced are deleted and the release
workflow runs it for the GitHub release notes. Since B580 `npm run release` runs no Node on the
laptop: it builds the four native targets with `go build` itself and accepts a green `CI` run for
HEAD in place of the local `npm test`, `npm run typecheck` and generated-doc checks. See
[Releasing](releasing.md).

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

New hand-over reasons go at the end only: one constant at the end of the
`const` block and one entry at the end of the `handoverReasons` array in
`native/cmd/code-forge-native/handover_reasons.go`. That file holds only the
list; the functions that use it stay in `handover_log.go`. `.gitattributes` merges
`handover_reasons.go` with `merge=union`, so two such appends land without a
conflict (B510, B513).
