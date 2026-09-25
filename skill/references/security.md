# Security — keys, the forbidden list, signed rows and where their guarantee ends

## §1 Keys never reach a coder

`keys.<name>` in `.code-forge.yml` holds a reference only (`user`, `op://…`, `env:NAME`, `keychain:<name>`), never a value. Resolution runs the chain — environment → OS keychain → 1Password `op read` (cached 8 h) → ask at setup — and runs **only inside the worker or the orchestrator's own CLI calls, never in a coder process**. `forge keys list | set <name> [--op <ref>] | test <name> [--ref <ref>] | remove <name>`; `list` never prints a value. Every output path is redacted: a message names the key path, never the value. Coder sessions receive neither the S1 key nor the signer key in their environment (subprocess env allow-list; Solo agent env likewise); reviewer sessions keep only the provider login variables they need to authenticate.

## §2 The forbidden list — one list, rendered per CLI at spawn, grepped from every transcript

`gh pr ready|merge|close` and base retargets · `git push --force*` · `git reset --hard` · `git checkout --` · `git restore` · `git clean` · `git stash` · `git branch -D` · `git rm` · `rm -rf` outside `.code-forge/` · any argv containing a `production.markers` token or a `production.names` name · any read or write of `~/.code-forge/runs/` · `forge worker`, `run start` and `block waive` from a coder (also via `npx`) · any write under `.code-forge/reviews/` or `.code-forge/queue/*.done`.

Rendered into every coder spawn as the CLI's own deny rules (path rules widen a directory to `<dir>/**`; a read-write path denies read, edit and write); carried verbatim in the brief; refused by the package's own process spawner; and at block close the transcript (captured stdout, the harness log, or the Solo output) is grepped for the forbidden verbs **and paths** — a hit is a `rule_break` row that fails the gate. Where a CLI cannot load a rules file, `doctor` says so and the transcript grep remains the detector.

## §3 Signed ledger rows — the threat model, stated honestly

**Mechanism.** A per-run HMAC key (32 random bytes) lives in `~/.code-forge/runs/<run>.key`, mode 0600, outside the workspace; `forge run start` generates it, the worker and `forge block close` read it, and it is never placed in a coder's environment or workspace. Gate-relevant rows carry `mac`; a verification failure is a `ledger.tamper` row and the block is `stopped`. Key rotation = a new run.

**What it defends against.** A coder that never called `review-file` and appends or edits rows or result files with its ordinary tools — no valid MAC, refused. A coder that edits a review result after the worker wrote it — the MAC no longer matches. A stale approval reused after the file changed — rows are keyed by content hash. A coder that starts its own worker to sign rows — the pinned pid and start time in the run record no longer match, `worker.replaced`, block stopped. A coder that waives its own finding — `block waive` is forbidden and the row requires `by: human` signed by the worker's key.

**What it does not defend against, and the product does not claim.** A coder running as the same OS user with a shell can read the key file and mint a MAC, or edit the run record. Within one OS user there is no boundary that a determined bypass-permissions process cannot cross; the deny rules make it a deliberate, named rule break rather than an accident, and the transcript grep makes the attempt visible (`rule_break: key_path`). `doctor` prints `signer: same-user boundary only` on every full run so nobody reads "signed" as "unforgeable".

**The real boundary is the consumer's choice:** run coders as a second OS user, or in a container that cannot reach `~/.code-forge/runs/` (a Codex `workspace-write` sandbox already blocks writes there; reads remain open). An in-memory key over a socket was considered and rejected: a same-user process can still replace the worker or edit the pin.

## §4 Isolation of closed-book sessions

Reviewer, recheck, judge, S2, author and facts sessions run in print mode with the provider's own isolation flags, cwd = an empty temp dir under the run's temp root, and the packet on stdin; `doctor` proves that no project document is visible (it asks the reviewer builder to echo the first line of the project's rules file and expects nothing) and that the coder builder cannot read a known file outside the workspace (`claude: path deny rules not honoured` when it can). The facts delegate gets read-only shell tools with every write verb denied.

## §5 What the package itself never does

No telemetry (`telemetry: off`, reserved). No `postinstall`. No secret in argv — a keychain write goes through stdin. Temp dirs live under one per-run root and are swept by the next `run start`; the reaper kills only processes this package registered whose start time still matches — never a foreign pid. Every child `git` runs with inherited `GIT_*` variables stripped, so a gate invoked from a hook still sees the right repository.
