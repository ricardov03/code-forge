# Subprocess adapter — engine `subprocess` (power users, by explicit config only)

Never selected automatically (`degraded.md` §1). Coders are detached CLI processes of the provider the level resolves to, built by the CLI from `.code-forge.yml`: the argv is never a shell string, the forbidden list is rendered as the CLI's own deny rules, and the brief is a file whose pointer travels in the argv.

## §1 Spawn

`forge run start` once; then per block `forge block open <id> --run <r> …` (`code.md` §3) and `forge spawn --level L<n> --role coder --brief <file> --run <r> --block <id> [--cwd <dir>] [--timeout <seconds>] --background`. The spawner writes a pid file and a log under the run's temp root, registers the pid so the reaper can find it, and prints `level=<Lx> provider=<p> model=<id> effort=<e> fallback_step=<n>`; a coder argv that hits the forbidden list is refused before the spawn. A missing or wrong `ACK <sha8> lines=<n>` within the launch window ⇒ `parked`, one resend, then `failed`.

## §2 Completion and evidence

The coder's stdout is captured; completion is the **last** `===BLOCK <id> COMPLETE===` or `===BLOCK <id> FAILED: <reason>===` line in it (a quoted sentinel inside a sentence never matches). Process exit without a sentinel is a failed block. Then the re-measurement of `code.md` §5 and `forge block close <id> --run <r> --transcript <captured stdout> --report <captured stdout>` — the transcript grep runs on the capture. A coder that hangs is killed at `--timeout` together with its process group; the pid entry is cleared.

## §3 What the coder's sandbox looks like per provider

Each provider's coder runs in its non-interactive auto mode with writes limited to the workspace where the CLI supports it, except under `coder.sandbox.profile: full`; every closed-book role runs read-only with an empty cwd and the packet on stdin. `forge doctor` probes, per provider, that the flags exist, that a one-token call works per role builder, that the closed-book session sees no project document, and that the deny rules are honoured — read its rows before the first real spawn. Where a provider cannot load a rules file, the forbidden list is prose-only in the brief and the transcript grep is the detector (`security.md` §2).

**The Codex coder's sandbox profile (`coder.sandbox`, B93).** Only the Codex (openai) coder reads it; it has no effect on the Claude and Grok coders, whose argv does not change. `doctor` prints no `coder sandbox` row without the key, one INFO row with it, a WARN row for `full`, and a no-effect INFO row only when a Claude or Grok coder is configured.

- `workspace` (the default, and what no `coder` key means): `-s workspace-write`, today's argv. The project tree (`-C <cwd>`) is the only writable root; `$TMPDIR` and `/tmp` stay excluded, so the session `CODEX_HOME` under the run temp root cannot be written.
- `toolchain`: the same, plus one `--add-dir <abs>` per `coder.sandbox.add_dirs` entry right after `-s workspace-write`, so a build cache or a package store is writable too.
- `full`: `-s danger-full-access`, which turns the Codex sandbox off: the coder writes anywhere you can, with network as the CLI allows. Like the Claude coder, the deny rules, the transcript grep and the gates are the only limits; `doctor` prints a WARN row. The argv keeps the `$TMPDIR` and `/tmp` exclusion flags, which Codex ignores there.

**`add_dirs` entries.** Each entry is absolute or starts with `~/`, which expands against HOME, and becomes an absolute realpath: symlinks are judged by their target, a dangling symlink too. `validate` (rule `sandbox-dir-refused`) and the builder refuse `/`, the home directory itself, and an entry that is, holds or sits inside any refused dir: `~/.code-forge`, `~/.codex` (and `$CODEX_HOME`), `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config`, `~/Library/Keychains`, the run temp root or the session `CODEX_HOME`. So `~/Library` is refused (it holds `~/Library/Keychains`), and so is `$TMPDIR` or `/tmp` when it holds the run temp root. The message names the key path (`coder.sandbox.add_dirs[<i>]`), never the value. Under `workspace` or `full` the entries are unused: `validate` warns `sandbox-dirs-unused`, and a refused entry is still refused.

The key is fixed for the run: `run reload` refuses a change, and `forge spawn --role coder --run <r>` exits 1 `sandbox-changed` when the live value differs from the run-start snapshot. Without `--run`, spawn reads the live config, so give a run's coder `--run <r>`. A run recorded before config snapshots has no run-start value; spawn then uses the live config. A Codex coder session row carries `sandbox: <profile>` only when the key is set.
