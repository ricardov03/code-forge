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
| `block open` | Open with or without `--level`, `--after`, `--after-outcome` and autopilot grants, including the worktree. `--help` and `-h` print the usage; an unknown flag is a usage error (exit 2); errors, including a missing `--acceptance` file, print and go to the error log in Go. | Worktree settings Go does not expect (for example an empty `proof.worktree`), `--after` cases Go does not serve, and state that Go cannot read. A `--brief` that is missing, under a file or a directory prints Node's realpath or read error in Go (B470). |
| `block close` | Agent close, `--decide`, `--no-require-reviews`, `--auto-claim`, `--remove-worktree`, human close, external review, hooks and grants. The review gate also judges review caps, reopened and unwaived findings, agent and autopilot waivers, `review.agent_waivers.require_ack`, accepted rule hits, old result files and an amended acceptance in Go. Close prints the agent waiver, agent reopen and accepted hit lines and JSON keys. `--help` and `-h` print the usage; `--help=value` is an unknown flag (exit 2), as in Node. A light block is checked against `light.max_files` and `light.max_lines` in Go (B451). The gate reads only the closing block's rows, as Node does, so another block's rows never send the close to Node, and a `rule_break` row whose id or rule is not text gets Node's `rule_break row: <String(id)>` detail (B451). | This block's review rows that Go cannot read exactly like Node: an object or array `decision` that is not read in ledger order, a review date in a legacy form with a day outside 1–31 or minutes or seconds over 59, a late or cap row whose file, finding or open id is an object or array (or a non-text file Node cannot quote, such as a number from 1e21 up), an ack digest in a non-Latin script that matches only in an order other than Node's sorted one (`close-ack-digest-order`), and `--decide` with a late row whose file and finding are not text. Since B498 an ack digest in Node's Latin `localeCompare` order, an object or array `decision` (printed with Node's key order), V8 `Date.parse` rollover and 2-digit forms (from a Node table), late and cap rows whose file is empty, a number or false or whose finding is missing or not text, the stderr and stdout line order on a shared terminal, and `--auto-claim` claiming before a later refusal (transcript read, `--worker-pid`) run in Go as in Node. Since B493 a late finding with no file, the legacy `toUTCString`/`toString` date forms, a non-ASCII file name in an ack digest, `--auto-claim` on a run with an autopilot record (the grant expiry check runs before the claims), a transcript that runs the `hooks.on_block_end` command (Node's `rule_break`; Go close used to skip this check and close) and `--auto-claim` with `close.require_human` at a terminal (Node's prompt and answers) run in Go. Codex JSON transcripts, the `code-forge-reviews-write` rule and an unreadable `--transcript` run in Go (B466). The block worktree refusals (malformed, missing, not a directory, other branch, another repository, no workspace directory), ledger null and scalar rows and an unreadable rotated ledger file run in Go with Node's messages (B470). Go writes nothing before it hands over; no case is refused. |
| `block ready`, `block waive`, `block reopen`, `block human close`, `block human-pass` | These commands. `block ready` uses the same review gate as `block close`. `block ready --preflight` (B392) is Go only: it lists stale base, open findings, rule breaks and coder report blockers without a gate result, never hands over, and names each check it cannot decide as `skipped`. It exits 0 and prints `block <id> preflight clean` when clean, and exits 1 when it lists blockers. Since B449 `block ready` also runs in Go with `hooks.on_block_end` and with `close.require_human` under an active autopilot grant (the `close-not-routine` blocker). `block human-pass --help` and `-h` print the usage. | State that Go cannot read or does not expect. A `block ready` transcript that names the hook's command runs in Go with Node's `rule_break` blockers (B493). `waive --yes` and `reopen --yes` still run in Node. |
| `context`, `context pack` | Every form since B452: `--help` and `-h`, usage errors, unknown subcommands and flags, the breadcrumb and failure log, and a missing run root. The pack header names the Node from `CODE_FORGE_NODE` when that resolves, else `node` on `PATH` (B469). | Nothing. |
| `gates run` | Every form (B450): built-in, custom and formatter gates (pint and prettier check the supported Git files in argv batches), `repeat_new_tests` (changed sources add the tests that import them), light blocks, flaky detection and a run with no `--run` and no `--block`. A stop signal (SIGINT or SIGTERM) writes the same cancel rows. With `--run` and `--block` the B392 preflight runs first by default (`--no-preflight` skips it); a blocker stops `gates run` with exit 1 before any gate command runs. With no `HOME` the user's home folder is used, as Node does. A `--timeout-ms` over the 2147483647 ms timer limit runs with a 1 ms timer and prints Node's `TimeoutOverflowWarning`. A `--cwd` that is not a folder skips every gate, as Node does. An empty `--run` or `--block` prints Node's message with exit 1. A case Node also refuses is a one-line error with exit 1 or 2 that names the cause and the fix: a bad gate config (the same messages as config validation), a damaged run record, or `repeat_new_tests` on a block with no base commit. A command that cannot start is a red gate with reason `spawn-error`. | A gate command that is an executable file with no `#!` line (Node runs it with `/bin/sh` on Linux and stops with `spawn ENOEXEC` on macOS, B469; reason `gates-custom`); a run record whose `run_id` does not match `--run` (reason `gates-run-id-mismatch`), or with no `workspace` string (reason `gates-run-no-workspace`); also the `block land` gates step for these records. |
| `block land` | Plain land, `--merge` with the selected-test merge gate, `--no-merge-gate`, active grants and `hooks.on_block_end`. | The gates step (B450), no compatible join daemon, the join, claim and close steps Go does not serve, and run state Go does not expect. A run file Go cannot read prints Node's read error in Go (B470). Read-only pre-checks finish before the first write. The B282 reason code names the step. `--help` and `-h` print the usage in Go (B451). |
| Other `gates` subcommands | `detect`, `secret-scan`, `safe-edit`, `scope`, `acceptance` and `transcript-grep` (B459), with the same stdout, stderr, exit code, `errors.jsonl` rows and breadcrumbs as Node; `gates --help`, `gates <sub> --help` (including `gates run --help`, which prints the usage and runs no gate) and usage errors also run in Go. `classes` is Go only (B335). | Each case hands over with its own reason: a `.code-forge.yml` Go cannot read in Node's key order, or a non-list truthy `gates.custom` (`gates-detect-config`); an acceptance file Node's `JSON.parse` rejects or that holds a lone surrogate (`gates-acceptance-json`); a TAP file that is not valid UTF-8 (`gates-tap-encoding`); a transcript with production markers or names (`gates-transcript-extras`) or in a format Go's transcript reader does not know (`gates-transcript-unsupported`; Codex exec `--json` logs run in Go since B466); both Git steps failing (`gates-git-race`); a Git call that times out, is killed or prints over 10 MiB or non-UTF-8 output (`gates-git-spawn`); a file error Go cannot word like Node (`gates-fs-error`). |
| `run status`, `run end`, `run start --reattach` | `run status --run <id>` (the same JSON, spend and budget). `run end --run <id>`: the pin check, the stop of a verified worker and the signed `worker.down`, `worker.replaced` and `run.end` rows. `run start --reattach --run <id>` with `--worker-pid`, or without it to re-pin the live worker that the queue announces (B454). `run --help`, `run <sub> --help`, `-h`, usage errors and unknown flags. | A plain `run start`, `run start --add-worker` and `run reload` (the worker launch, the process sweep and the config snapshot). A run record that is not JSON, or with a shape that makes Node fail, a ledger Go cannot read, a worker registry Go cannot judge, and no `HOME`. |
| `run start`, `run status`, `run end`, `run reload` refusals | `run status --run <id>` (the same JSON, spend and budget). `run end --run <id>`: the pin check, the stop of a verified worker and the signed `worker.down`, `worker.replaced` and `run.end` rows. `run start --reattach --run <id>` with `--worker-pid`, or without it to re-pin the live worker that the queue announces (B454). `run --help`, `run <sub> --help`, `-h`, usage errors and unknown flags. A plain `run start` (B468): the config snapshot and its hash, the run record, key, mirror and signed `run.start`, `run.mode` and `review.external` rows, the removal of dead temp roots, the worker launch (`code-forge worker --run <id>` under Node) and its pin, or the stop of a launched worker that was not pinned. `run start --add-worker --run <id>` with the `review.workers` limit and the signed `worker.add` row. `run reload` refusals: no run, an ended run, no `.code-forge.yml`, a config that is not a mapping, one that cannot be migrated or read. The process sweep of a plain `run start` (B472): it stops a worker of a finished run, an orphan whose owner is gone and a registered child of a dead temp root with Node's checks (SIGTERM, a 2-second grace, SIGKILL). A duplicate key or a tag that does not resolve in `.code-forge.yml` (B472: `run start` prints Node's message, `run reload` its line). | A temp base the sweep cannot read; any other YAML syntax error in `.code-forge.yml` (Node words it); a `run reload` of a config that loads (validation, changed and fixed keys, the autopilot guard); `run start` without a Node to launch the worker. A run record that is not JSON, or with a shape that makes Node fail, a ledger Go cannot read, a worker registry Go cannot judge, and no `HOME`. |
| `resolve <level>` | The level lookup with its fallbacks (B455), `--help`, usage errors and an unknown level. | A `.code-forge.yml` with a YAML syntax error (`config-parse-error`) or a `fallback` that is not a list of maps (`resolve-fallback-shape`). |
| `models` | The model list and `--refresh --from-cli-caches` (B455), with the same models cache file, `--help` and usage errors. | A YAML syntax error (`config-parse-error`), a models cache Go cannot write (`models-refresh-unwritable`) and a `models_cache.json` that is not valid JSON (`models-refresh-invalid-json`). |
| `list`, `remove` | Both commands, `--help` and their usage errors (B455); `remove` deletes the same installed targets and rewrites the installs file. | `remove` with a target outside the safe places (`remove-unsafe-target`) or a target or installs file Go cannot change (`remove-unfinishable`). |
| `pipeline status`, `pipeline next` | Both forms with `--json` and `--root` (B455): step states, duplicate, unknown-need and cycle problems, and a missing pipeline. | A pipeline or outcome with a YAML syntax error or a shape the schema rejects, and a read error other than a missing file or a folder (`pipeline-unsupported`). |
| `outcome seal`, `outcome check`, `outcome record` | `seal` and `check` (B455), and `record` (B471): every problem line, `--json`, `--root`, `--step` (else the pipeline step whose outcome is the file), usage errors, and a passing outcome signed as Node's `outcome.recorded` row under the run lock (`already recorded` when it is). | A schema error, an outcome Go cannot rewrite or decode (`outcome-unsupported`), and for `record` no home folder, a pipeline Go cannot read or a config with a YAML syntax error (`outcome-record-unported`). |
| `validate` | Every rule with Node's messages, order and exit codes (B471): errors and warnings, seen-in-cache model ids, CLIs on `PATH`, pipeline checks, `--file`, usage errors and a missing config. | A config the schema rejects (`validate-schema-issues`), a config holding a secret-shaped value (`validate-secret-values`), a non-empty `coder.sandbox.add_dirs` (`validate-sandbox-dirs`), a YAML syntax error (`config-parse-error`) and a pipeline Go cannot read (`pipeline-unsupported`). |
| `plan check` | `--help`, usage errors, flag errors and a plan or `--facts` file that cannot be read (B471); the check itself with facts, `--slug`, project config caps and budget, ledger lanes, claims, overlaps, cycles and imports (B475). | Config caps or budget that are not plain numbers (`plan-check-limits`), a broken `.code-forge.yml` (`config-parse-error`), plans or facts sheets on which Node throws (`plan-check-node-throws`) and a ledger that cannot be read (`plan-check-ledger-read`). |
| `sweep` | `--help`, `-h` and usage errors (B471). | The process scan, dry run or `--apply` (`sweep-unported`). |
| `upgrade` | `--help`, `-h`, usage errors and a `--source` that is missing or not a folder (B471); re-pointing the recorded installs (link or copy) with the release notes and docs sync, also with no `--source` (B475). | A Go binary outside a full package (`upgrade-package-root`), paths that are not UTF-8 (`upgrade-path-encoding`), control characters in a path or the output (`upgrade-control-chars`), no usable HOME (`upgrade-no-home`), installs or targets that cannot be written (`upgrade-unfinishable`), an installs file that is not UTF-8 (`upgrade-ledger-encoding`), a target inside the package or nested in another target (`upgrade-unsafe-target`), a copy source holding links or special files (`upgrade-copy-entry`) and versions Node orders differently (`upgrade-version-order`). |
| `tools`, `tools install` | The tool list in text and `--json`, `install` with `--dry-run` and `--yes`, the plan, each install command and its result, and every usage error (B455). | `install` at a terminal without `--yes`, which asks Node's yes prompt (`tools-confirm`), and a `--version` line whose cut splits an emoji (`tools-unsupported`). |
| `doctor` | `--help`, `-h`, usage errors and unknown flags, and the whole `--quick` run in text and `--json` (B474): the config row, harness links, the Jev key from its env variable or the file store (`CODE_FORGE_KEY_BACKEND=file`), its expiry warning, a malformed key reference, an expired cached key (deleted with its index row), a cached key cut to the cache hours (entry and index row rewritten), a 1Password `op://` read with `op read` (retried once after a first-call timeout, logged as an `op_retry` warning) cached with its index row, each 1Password failure reason (B483), and the reviewer rows from the project ledger, with the resolved key masked as Node does. | The full run with its live checks (`doctor-full-run`: the live Jev call, provider CLI probes, worker start and stop, signer key, process sweep and memory rows, native binary and daemon rows, tools, ledger, locks, tmp and review state); a config the schema rejects, one holding a secret-shaped value or a non-empty `coder.sandbox.add_dirs` (`doctor-config-schema`, `doctor-config-secrets`, `doctor-sandbox-dirs`); a YAML syntax error (`config-parse-error`); the OS keychain key store (`doctor-key-store`); a key store `index.json` Go cannot read back exactly like `JSON.parse` (a lone surrogate escape, a number out of range, or a non-object row to shorten) when the key read must rewrite it (`doctor-key-refresh`); ledger rows whose dates or keys Go does not read exactly like Node (`doctor-ledger-shape`); and no `HOME` (`doctor-no-home`). |
| `keys list`, `keys set`, `keys test`, `keys remove` | With the file key store (`CODE_FORGE_KEY_BACKEND=file`): `list` (also the macOS keychain list), `set <name> --cache-hours <h>` and the usage errors, `test` for a key from the environment, an `env:` or `keychain:` reference or the file store, and `remove` with its index update. `keys --help`, `keys <sub> --help`, usage errors and config errors print as in Node; a key value is never printed (B456). | A typed `keys set` (hidden prompt), every 1Password (`op`) path, an `op://` reference with no value in the environment, keychain-backend `set`, `test` and `remove`, the default backend chain off macOS, and an index row Go does not expect. |
| `report` | `report --slug <slug>` as text, `--json` and `--export <dir>`: all 18 sections, `awaiting_human_check` and `flaky`, from the slug's ledger and its rotated files. Help, usage, slug and missing-ledger errors (B456). | A ledger row whose bytes depend on JavaScript coercion (a block or run that is not text, a `null` row, an hour of 24, a lone surrogate, a broken line that is not the last one) and an export folder Go cannot write. |
| `ledger outcome`, `ledger add coder`, `ledger outside-review` | `ledger outcome --pr <n> --ci red\|green\|reverted` and `ledger outcome --scan-git` (with `--cwd` and `--days`), `ledger add coder` and `ledger outside-review`, with the same output, exit codes, signed ledger rows and error-log entries as Node; `ledger --help`, `ledger <sub> --help`, `-h` and usage errors (B457). | Ledger rows Go cannot read like Node (`ledger-rows-format`), a run record or run key Go cannot read and no `HOME` (`ledger-run-record`), a git that cannot run or gives odd output (`ledger-git`), values that JavaScript prints in its own way (`ledger-js-value`) and the once-a-day hint on a terminal (`ledger-error-hint`). |
| `logs clear`, `logs report` | `logs clear` with no log, `--yes`, and the refusal without a terminal; `logs report` with nothing to report; `logs --help`, `logs <sub> --help`, `-h`, unknown subcommands, usage errors and no `HOME` (B457). | The `logs clear` prompt on a terminal (`logs-clear-prompt`) and a `logs report` that would file an issue (`logs-report`: the npm check, the AI scrub and `gh`). |
| `worker` | `--help` and `-h`, usage errors and unknown flags, a bad `--poll-ms`, a bad or unknown run id (exit 1) and a run that has ended (exit 1), with Node's text (B460). | Serving the queue: the boot lock, the registry, the daemon, pins, ticket reviews, signed rows and the stop. Node stays the only process that takes tickets, so no ticket is lost or reviewed twice. Also a run record that is not JSON, and no `HOME`. Since B485 the Go package `native/worker` has the worker's boot and pool: the boot lock, the registry admission, pins and the `worker.reattach` row, the announce and heartbeat files (byte-equal to Node), the clean stop, the queue order, the ticket claim (Node's lock file) and the ticket and provider caps. Since B488 `worker.TicketRunner.RunTicket` runs one ticket's refusals in Go, byte-equal to Node: a bad ticket, a bad path, another run, a stale or missing file, a worktree mismatch, an unreadable file or symlink, a gone or malformed block worktree, an unreadable run record and a missing config snapshot. It holds Node's file lock and writes the signed result, the ticket timings, the done marker and the signed `review.result` row, or the `worker-error` result (poison when even that fails). Since B492 it also routes the ticket like Node: `review.routes` (a list it cannot use is `unavailable` `route-invalid`), adaptive L2 routing (configured order, measured performance, exploration, the ticket's earlier `review.route` row) and the signed `review.route` rows (`route-row` when one cannot be written), then the engine's first refusals: `review.exclude` (`excluded`), an unreadable run record, an unknown block, a block with no base, a malformed block worktree and an amended acceptance (`stale`). A ticket that needs a review session (context, risk and facts, the fix loop), an answers ticket, the other worktree codes and a ledger or route config Go cannot read like Node return an unsupported error with nothing written, so Node takes them. Since B495 the Go packages also have the fix loop's parts, byte-equal to Node but not yet called by the worker: `review.RunRound` (a full round: triage without Jev, the `review.triage`, `review.round`, `review.approved` and `review.cap` rows, escalation and the `ledger_write_failed` retry), `review.BudgetRows` (the block's `review.budget` and `review.over_budget` rows) with `review.RulesRisk`, and `worker.SaveState` (the signed state file, its git blob and the `review.state` anchor row). Recheck and patch-check rounds are not ported. Since B496 `worker.FixLoopRound` wraps one round like Node's `fixLoopRound`, byte-equal to Node's own goldens: the block lock (Node's lock file), the run's rows read once, the block's `review.budget` row written once per block (changed owned files through `git diff` and untracked files), the state load (`review-state-tampered`, `review-state-lost`, a state rebuilt from its signed anchor and saved with `review.state_restored`, an old state file moved to the new path, an amended acceptance restarting the file), the file's L3 rung (a round that could take it runs under the block lock), the approval check (`hash-moved`), a crash saved as `next: retry`, and the outcome (`approval-not-written`, `findings-open`, `no-fix-hunks`, stopped, unavailable). A ticket that needs a `block reopen` row, an autopilot extra round, a stop a raised cap reopens, a recheck or a patch check returns an unsupported error with nothing written. The worker does not call it yet: the review session (risk and facts, context, `review.File`'s unported cases) is still Node's, and the verb still hands over. |
| `spawn` | `--help` and `-h`, usage errors and unknown flags, a config that does not load, a bad `--timeout`, a `--schema` that cannot be read or is not JSON, a bad `--run` id, and a bad `--level` or `--role` when no `--run` is given (B460). A foreground closed-book session with no `--run` for the roles reviewer, judge, s2, author and delegate on anthropic and xai steps, with no schema or the finding schema, and `--print-argv` (B477): the same argv, fallback ladder, rate-limit retries, timeout, codex skip note and closed-book refusal, result line, exit code (0, 1, 2 or 3) and ledger row as Node. The same session with `--run` (B482): the run's temp root, then the run-pinned gate before each step — `budget.usd` (the reached, estimate-above and would-exceed refusals, the spend-unreadable refusal, the 80 % warning row, stderr line and error-log warning) and the autopilot category cap (the paused refusal with the saved pause and one signed `autopilot.pause` row, the unreadable-record refusal) — under Node's `budget:<run>` lock, with the child's `CODE_FORGE_RUN_ID` and pid entry as Node. | `--background` (with or without `--run`), `--run` with the coder role (the run-start `coder.sandbox` check), a `tmp.root` that is not a string or that the run root refuses, an autopilot grant whose `until` or `started_at` date may parse differently, the facts role, a level or fallback step Node rejects later (no level, no model, an unknown provider or effort), a `--schema` other than the finding schema or not UTF-8, a brief that cannot be read, and no `HOME`. |
| `review` | `--help` and `-h`, usage errors, not inside a git repository, a base that does not resolve, a bad `--files` path, and the whole run when nothing is left to review (text or `--json`, with the skipped-file notes) (B460). | Any run with a file left to review (config, run start, tickets, worker, wait, block and run end), a git error while listing files, and no `HOME`. Go writes nothing before it hands over. Since B476 the Go package `native/review` can review one ticket on the single-reviewer path (packet, one L2 session through a spawn seam, the timeout retry, the stub guard, the `review.plan`, `review.unavailable` and `review.session_timeout` rows and Node's result), equal to Node on 25 tickets. Nothing calls it yet (B477); the verb still hands over. Cases it does not cover are refused before any session starts; three that show only after the session (a `needs_file` answer, a second same-model timeout and a schema miss with a ledger) stop with a gap error. |
| `autopilot status`, `autopilot stop`, `autopilot start`, `autopilot round`, `autopilot level`, `autopilot binnacle`, `autopilot log`, `autopilot ask`, `autopilot approve` | `autopilot status --run <id>` (text and `--json`, spend per category, paused categories, the one signed `autopilot.expire` row when the window is over) and `autopilot stop --run <id>` (the signed `autopilot.stop` row and the record update), with or without a link: a grant with no link writes and refreshes the binnacle files as Node does (B478). `autopilot start --run <id> --yes`, with no terminal (refused, exit 2, as in Node), and at a terminal without `--yes` (the same yes/no prompt; anything but yes cancels) (B478): every input check, the signed `autopilot.grant` row and the grant record. `autopilot binnacle` and `autopilot log` (B478). `autopilot round` (the signed `autopilot.extra_round` row) and `autopilot level` (the recorded lane, the level rule and the signed `autopilot.level` row) with Node's checks and refusal texts (B478). Like Node, once a command has read the run's signer key (a signed write, or the decision check of `round` and `level`), every stdout and stderr line and every binnacle field masks the key as `[REDACTED]`; before that point nothing is masked, as in Node (B478). `autopilot --help`, `autopilot <sub> --help`, `-h`, usage errors, unknown flags and the `approve --yes` refusal for every subcommand (B461). A run with active approvals that are not due yet (B484). `autopilot ask` up to the delegate session: every usage, option and subject check, an unknown scope, and every grant refusal (`no-grant`, `run-ended`, `stopped`, `expired` with its one signed `autopilot.expire` row, `denied`, `not-allowed`, `paused`) in text and `--json`, exit 1, as in Node (B484). `autopilot approve` up to the owner's prompt: every input check, fixed keys, an ended run, an active approval on an overlapping key, and the no-terminal refusal (exit 2) (B484). A due approval whose check needs no `.code-forge.yml` write and no run reload: a step whose signed row is still pending, a key with a prototype segment, a config that is missing or unreadable, a value changed by hand (the signed `autopilot.restore_skipped` row) and an ended run whose file already holds the old value (the signed `autopilot.restore` row), with Node's lines, approvals lock and record update (B490). `autopilot approve` at a terminal up to the change: the same yes/no prompt, wrapped as clack wraps it, the cancel text (exit 1), a failed prompt (exit 1), `--until` checked again after the answer, and the refusals for no config, an unreadable config and a key that already holds the value (B490). A due approval of an ended run whose `.code-forge.yml` needs the old value back is written in Go with the `yaml` package's exact bytes (comments, blank lines, quoted text, flow and block lists, document markers, CRLF, no final newline), through a temp file and rename that keep the file's mode; a parent that is not a mapping and a write that fails are the same WARN lines as Node's (B494). | `ask` when the grant lets the question through (it starts a delegate model session), `waive` (human-only grant use and issue tracking), `approve` that changes `.code-forge.yml` (`autopilot-approve-write`: the YAML edit and the run reload), an approve prompt with text that is not printable ASCII or a lone surrogate escape (`autopilot-approve-value`), a config that changed while the owner was asked (`autopilot-approve-changed`: Node asks again), a config Go reads differently from Node, such as a BOM, bytes that are not UTF-8 or a number that is not finite (`autopilot-approve-config`, `autopilot-restore-config`), a due approval of an active run (`autopilot-restore-reload`: the config check and run reload) or of an ended run whose file needs the old value written back in a YAML shape Go does not write byte for byte (`autopilot-restore-write`: tabs, anchors, block scalars, a second document, long text the `yaml` package folds, and a second due approval read after a Go write), stdin at a terminal with stdout not (`autopilot-prompt`), and an approval or grant shape Go does not expect; a `round` or `level` whose block name, file path, plan table, lane answer or decision row Go could read differently from Node (for example a block named `constructor`, a path with `./` or `//`, a non-ASCII table header or lane answer, an object decision); a run record Go cannot read, a ledger or spend row Go cannot read, and no `HOME`. Go writes nothing before it hands over. |
| `help`, `version`, `init`, `native-status`, `facts`, `author`, `s2`, `jev`, `proof` | `help` (also no verb, `--help` and `-h`) and `version` (also `--version` and `-v`) with Node's text. `--help` and `-h` of every one of these verbs, their usage errors and unknown flags, with the same breadcrumb and error log (B458). `jev ask` refusals before the state file is read. `proof baseline`, `proof tier` with or without a project config (the config rules, B462), `proof lock`, `unlock`, `restore` and `export` (also `--remove`) with the same output, exit codes, run record, lock, journal, export tree and signed ledger rows as Node (B462), and the `red-green` argument checks (including the `--test` path rules). `proof red-green` itself (B481): the export isolation and the lock, the revert red and the assertion-deletion red, setup commands, the journal and its restore, the signed `export` and `red-green` rows and the JSON result, with Node's output and exit codes; and every project config rule `proof` checks, including `proof.runners`, `review.routes` under multimodel and `coder.sandbox.add_dirs` (B481). | `init` itself (the wizard), a valid `facts`, `author` or `s2` call (these start a model session), `jev ask` once it reads `--state`, a project config Go cannot load for a reason that is not a load error (`proof-config`), a `proof red-green` whose snapshot Go cannot read (`proof-snapshot`), that uses a `proof.runners` entry or the Pest default for a `.php` test (`proof-runner`), or that meets another error before the export (`proof-red-green`), a `proof export` with a git step, run record or `proof.export` list Go cannot read (`proof-run`), and `help` or `version` when Go cannot find its install. Each hands over with its own reason. |
| `native-status` | The private daemon helper (B501): one status line per standard-input line (`{"runs":[...]}`, or `{"ok":false,"error":"status-unreadable"}` for a line Node cannot read), `\n`, `\r\n` and lone `\r` line ends as Node's readline, a missing `rows` read from the ledger, then the router's `has no default export function` line with exit 1, the breadcrumb and the error log. `--help` and `-h` print the usage. | No `HOME`, and a terminal on stderr with no agent set (the router's hint) hand the whole command to Node. A line with a block id outside ASCII that Node must sort, a number Node reads as `Infinity`, a lone surrogate escape or bytes that are not UTF-8 hands that line and the rest of standard input to Node's helper. |

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
read and a `null` spend that overflows. Only a block id outside printable
ASCII, which needs Node's sort order, still uses the helper. Although help
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

New hand-over reasons go at the end only: one constant at the end of the
`const` block and one entry at the end of the `handoverReasons` array in
`native/cmd/code-forge-native/handover_log.go`. `.gitattributes` merges that
file with `merge=union`, so two such appends land without a conflict (B510).
