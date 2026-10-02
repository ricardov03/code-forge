# Review only

`code-forge review` runs code-forge's review engine on changes you already have: your branch, your
uncommitted work, or a few files. It writes no code, runs no proof and closes no block. You get a
verdict per file, a fix list, and an exit code you can use in CI.

```text
code-forge review
```

## When to use it

- You (or another tool) wrote the code, and you want a second, independent review before you open a
  pull request.
- You want the same review a code-forge block gets (fresh closed-book sessions, depth by risk,
  a stub guard that never counts an empty answer as approval) without planning or coding through
  code-forge.
- You want a review step in CI that fails the job when a file has findings.

Use the full pipeline (`/code-forge plan|harden|code`) when you want code-forge to write the code
too. See [how-it-works.md](how-it-works.md).

## Before the first run

1. `code-forge init` in the repository, so `.code-forge.yml` exists at its root. `review` refuses
   to start without it (exit 2).
2. `code-forge doctor --quick` passes. The reviewer sessions use the providers and keys the config
   names, the same as a normal run. L2 (the reviewers) and L3 (the judge) need an anthropic or xai
   model: Codex always has a shell, so an openai L2 or L3 is refused and every file comes back
   `unavailable` (see [getting-started.md](getting-started.md#codex-and-closed-book-roles)).
3. Add `.code-forge/` to `.gitignore`. `review` writes its review queue, review results and a run
   mirror there (see [What it writes](#what-it-writes)).

## The command

```text
code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent "<text>"]
                  [--run <id>] [--max <seconds>] [--json] [--keep-run]
```

| Flag | Default | What it does |
|---|---|---|
| `--base <ref>` | the merge base of HEAD and the default branch | the commit the diff is taken against. The default branch is `origin/HEAD`'s branch, else `main`, else `master`. On the default branch itself the base is HEAD, so only uncommitted work is reviewed |
| `--files <path…>` | every changed file | review only these paths, given relative to the current directory. A path that is absolute, has a `..` segment, leaves the repository, is a symlink or sits under `.git/` or `.code-forge/` is refused before anything starts (exit 2) |
| `--intent "<text>"` | none | what the change is meant to do. It becomes the review's one acceptance clause, which every reviewer reads |
| `--acceptance <file>` | none | a YAML or JSON list of `{clause, tests}`, the same format `block open` takes. Used as given |
| `--run <id>` | a new run id | the run to use. An active run with that id is reused and left running; otherwise a new run starts with that id |
| `--max <seconds>` | `900` | how long to wait for each file's verdict. A file that is not done in time is reported as `stopped: timeout` |
| `--json` | off | print one JSON document instead of the text summary |
| `--keep-run` | off | do not end the run (and its worker) afterwards. Reuse it with `--run <id>`; end it with `code-forge run end --run <id>` |

With neither `--intent` nor `--acceptance`, the clause is: "Review for correctness, security and
test quality; no intent was stated." The clauses reach every reviewer: each review packet carries
them under "Acceptance clauses", next to the file and its diff, so the reviewer judges the change
against what it is meant to do. Long clause lists are cut to fit the packet budget, with a marker.

### Which files

By default: every file in `git diff --name-only <base>` (committed, staged and unstaged changes
against the working tree), plus untracked files that git does not ignore. These are skipped, and
each skip is named on stderr:

- deleted files, binary files (a NUL byte in the first 8000 bytes), symlinks;
- secret-like names (`.env*`, `*.pem`, `*.key`, `*.p12`, `id_rsa*`, `credentials*`): never sent to
  a reviewer;
- code-forge's own state: `.code-forge/` and, when the files are found for you, `.code-forge.yml`.

No file left means `nothing to review` and exit 0. Nothing is started.

## Examples

Review your branch against `main` (run it on the branch):

```text
code-forge review
```

Review only uncommitted work. On the default branch that is what `review` does by itself; on another
branch, point the base at HEAD:

```text
code-forge review --base HEAD
```

Review only some files:

```text
code-forge review --files src/billing/invoice.ts src/billing/tax.ts
```

Say what the change is for:

```text
code-forge review --intent "Invoices round tax per line, not per total"
```

Use your own acceptance clauses:

```text
code-forge review --acceptance plans/tax-rounding.acceptance.yml
```

In CI, fail the job when any file is not approved, and keep the JSON for the job log:

```text
code-forge review --base origin/main --json > review.json
```

## Output

One line per file, then the fix list for every file that is not approved, then the totals:

```text
src/billing/invoice.ts  approved
src/billing/tax.ts      1 finding

fix list: src/billing/tax.ts
  F1 · warning · lines 42-44
    claim: rounding happens after the sum, so line totals drift by a cent
    fix:   round each line before adding it

totals: 2 files · 1 approved · 1 with findings (1 finding) · 0 stopped · 0 unavailable
```

A file line is one of:

| Line | Meaning |
|---|---|
| `approved` | the review converged with no blocking finding; a signed `review.approved` row is in the ledger |
| `N findings` | the reviewer raised findings that must be fixed; they are in the fix list |
| `stopped: <reason>` | the review stopped: `timeout` (`--max` passed), `split_required` (the diff alone is over the packet budget), or a fix-loop stop |
| `unavailable: <reason>` | no valid review came back (the provider failed, the answer failed the stub guard, the worker was down). Never approval |

`--json` prints one line: `{ok, base, base_from, run, block, files, skipped, totals}`. Each entry of
`files` is `{file, result, reason, findings}`, where `result` is `approved`, `findings`,
`stopped`, `unavailable` or `unchanged`, and each finding is `{id, severity, lines, claim, fix}`.

### Exit codes

| Code | When |
|---|---|
| `0` | every file is approved, or there is nothing to review |
| `1` | any file has findings, or is stopped, unavailable or timed out; or the run could not start |
| `2` | a usage error, not a git repository, a bad `--base`, a refused `--files` path, or no `.code-forge.yml` |

## What it does

1. Starts a run (`run start`), which launches the review worker.
2. Opens one block, `R-<timestamp>`, at level `L2`, with the base sha, the file list as its owned
   files, and the acceptance clause.
3. Queues every file (`review-file`) and waits for each verdict (`review-file --wait`).
4. Prints the summary.
5. Stops the block (`block stop`) and ends the run (`run end`), which stops the worker.

Step 5 always runs: after an error, after a timeout, and on Ctrl-C or SIGTERM. No worker is left
behind. With `--keep-run` the block is still stopped, but the run and its worker stay up.

## What it does not do

- It writes no code and applies no fix. Fix the findings yourself, then run `review` again. Each run
  is a new round 1, so the whole file is reviewed again, not only your fix.
- It runs no proof: no measurement export, no gates, no red→green.
- It never closes a block. The review's block always ends `stopped`, never `closed`.
- It does not post comments to a pull request.

## What it writes

| Place | What |
|---|---|
| `.code-forge/` in the repository | the review queue, the signed review results, the run mirror. Add it to `.gitignore` |
| `~/.code-forge/runs/<run>.json`, `.key` | the run record and its signing key |
| `~/.code-forge/ledger/<slug>.jsonl` | the ledger rows: `review.plan`, `review.round`, `review.approved`, … |
| the temp root | the generated acceptance file (removed when `review` ends) and the worker's run root with the reviewers' empty working directories (swept by the next `run start`) |

Nothing else in your working tree changes.

## Cost

Review depth follows risk, per file, the same as in a block:

| Risk | Sessions |
|---|---|
| below 1 | one L2 `quick` review |
| 1 to below 2 | one L2 `full` review |
| 2 or more | two blind L2 reviews plus an L3 judge |
| `review.multimodel: true` | for a file above `review.single_reviewer_max_risk` (default 1): one L2 review per provider plus an L3 judge from a third provider. Files at or below it keep the rows above. When every reviewed file is docs (`.md` and similar), there is no multimodel review unless `review.multimodel_for_docs` is on |

Without a Jev key the risk comes from rules: a `proof.tiers.high.paths` match is 3, a path with
`migration`, `policy` or `middleware` in it is 2, more than 200 added lines is 1, anything else is
0. So most small files cost one L2 session each, and a file on a high path costs three sessions.
To keep a run cheap, pass `--files` with the files that matter. `code-forge report --slug <slug>`
shows the cost afterwards, and `budget.usd` in `.code-forge.yml` caps it: once the run reaches it,
no new session starts and the files left are reported `unavailable: budget`.

A reviewer session that hangs is killed after `review.session_timeout_s` (default 300 seconds) and
started once more; only a second timeout makes the file `unavailable: timeout`. `--max` is the
outer limit per file.
