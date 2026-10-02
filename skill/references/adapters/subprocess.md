# Subprocess adapter — engine `subprocess` (power users, by explicit config only)

Never selected automatically (`degraded.md` §1). Coders are detached CLI processes of the provider the level resolves to, built by the CLI from `.code-forge.yml`: the argv is never a shell string, the forbidden list is rendered as the CLI's own deny rules, and the brief is a file whose pointer travels in the argv.

## §1 Spawn

`forge run start` once; then per block `forge block open <id> --run <r> …` (`code.md` §3) and `forge spawn --level L<n> --role coder --brief <file> --run <r> --block <id> [--cwd <dir>] [--timeout <seconds>] --background`. The spawner writes a pid file and a log under the run's temp root, registers the pid so the reaper can find it, and prints `level=<Lx> provider=<p> model=<id> effort=<e> fallback_step=<n>`; a coder argv that hits the forbidden list is refused before the spawn. A missing or wrong `ACK <sha8> lines=<n>` within the launch window ⇒ `parked`, one resend, then `failed`.

## §2 Completion and evidence

The coder's stdout is captured; completion is the **last** `===BLOCK <id> COMPLETE===` or `===BLOCK <id> FAILED: <reason>===` line in it (a quoted sentinel inside a sentence never matches). Process exit without a sentinel is a failed block. Then the re-measurement of `code.md` §5 and `forge block close <id> --run <r> --transcript <captured stdout> --report <captured stdout>` — the transcript grep runs on the capture. A coder that hangs is killed at `--timeout` together with its process group; the pid entry is cleared.

## §3 What the coder's sandbox looks like per provider

Each provider's coder runs in its non-interactive auto mode with writes limited to the workspace where the CLI supports it; every closed-book role runs read-only with an empty cwd and the packet on stdin. `forge doctor` probes, per provider, that the flags exist, that a one-token call works per role builder, that the closed-book session sees no project document, and that the deny rules are honoured — read its rows before the first real spawn. Where a provider cannot load a rules file, the forbidden list is prose-only in the brief and the transcript grep is the detector (`security.md` §2).
