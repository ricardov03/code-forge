# Brief mode

A brief-mode run codes one small task, or a short list of tasks, from briefs alone. There is no
plan document, no `plan check` and no plan approval. Each block is still opened, coded, measured
and closed by the same gate as in a plan run.

For design first, open code blocks with `--after <design-id>`: the design must be closed, including its owner's `block human-pass` when it has a `human_check` clause.

This page walks one brief-mode run from start to end, in a repository where an outside reviewer
(a person or a CI review bot) reviews every pull request. Every command and every output on this
page comes from the CLI as built. When a verb's usage line disagrees with this page, the verb is
right.

## Brief mode or plan mode

| | Brief mode | Plan mode (the default) |
|---|---|---|
| Start | `code-forge run start --mode brief` | `code-forge run start` |
| Use it for | one small task, or a short list of tasks (for example in a monorepo), when each block is clear from its brief | multi-block work with an order to argue about, shared files or a design to approve |
| Before coding | a brief and an acceptance file per block | facts sheet, plan, hardening, `plan check`, the owner's approval |
| The coder's level | `block open` without `--level`: the rules pick the lane | `block open --level L<n>`: the level the plan recorded |
| The gate at close | the same | the same |

The mode is fixed for the run. `run reload` never changes it, and `report` lists each run as
`brief` or `plan`. For plan mode, read [how-it-works.md](how-it-works.md).

## The repository in this walk-through

A small Node project, `shop-api`. Its tests run with `node --test`. An outside reviewer reviews
every pull request, so the project does not want code-forge's own per-file reviews on top. The
task is one block, `B1`: make `total()` in `src/cart.mjs` sum the item prices, with its test in
`test/cart.test.mjs`.

The repository holds:

- `AGENTS.md`: the project's rules for coders;
- `docs/lessons.md`: lessons from earlier work;
- `plans/cart-total.brief.md`: the brief for B1;
- `plans/cart-total.acc.yml`: its acceptance clauses, a YAML list of `{clause, tests}`;
- `scripts/on-block-end.mjs`: the hook below.

## Step 1: the config

Write `.code-forge.yml` at the repository root and commit it before `run start`.

```yaml
version: 1
project:
  slug: shop-api
provider: anthropic
engine: harness
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: claude-fable-5-1}
review:
  external: {required: true, name: Outside reviewer}
gates:
  test:
    argv: [node, --test, "{test_files}"]
    env: {NODE_ENV: test}
  lint: null
  types: null
  format: null
  full_suite_threshold_files: 10
proof:
  isolation: worktree
  worktree:
    branch: forge/{run}/{block}
context:
  rules: [AGENTS.md]
  lessons: docs/lessons.md
  commands:
    test: [node, --test]
  max_lines: 300
  require_coder_tests: true
hooks:
  on_block_end:
    argv: [node, scripts/on-block-end.mjs]
    timeout_s: 30
autonomy:
  coder_may_push: false
  coder_may_open_draft_pr: false
```

The `levels` are the `provider: anthropic` defaults that `code-forge init` writes.

What each new key does:

| Key | What it does |
|---|---|
| `review.external` | The outside reviewer replaces code-forge's per-file reviews at `block close`. The close writes a signed `gate.reviews_waived {by: config}` row with the reviewer's name. `name` is required |
| `gates.test` as `{argv, env}` | The test gate. The item `{test_files}` becomes the block's test files. `env` adds variables to the gate's environment. code-forge shows only their names (`env_keys`), never their values |
| `gates.full_suite_threshold_files` | A block that owns more files than this runs the full suite instead of its related tests |
| `proof.isolation: worktree` | `block open` gives each block its own git worktree and branch. The default tree folder is `../<repo-name>-worktrees` |
| `proof.worktree.branch` | The branch name. Only `{run}` and `{block}` are filled in |
| `context` | What goes in the coder's context pack: rules files, a lessons file, the project's commands (code-forge never runs them) and a line cap |
| `context.require_coder_tests` | `block close` refuses a coder report without a `TESTS <command> exit=0` line before its COMPLETE sentinel |
| `hooks.on_block_end` | A command code-forge runs once after every block end. No shell. Its cwd is the repository top |
| `autonomy.*` | Both false (the default): coders never push and never open a PR. You (or the owner) push and open the draft PR |

Set these keys before `run start`. `review.external`, `hooks`, `proof.isolation`, `proof.worktree`
and `context.require_coder_tests` are fixed for the run: `run reload` refuses a change to them.
The rest of `context` may change mid-run. With `review.external.required` or `hooks.on_block_end`
set, no block may own `.code-forge.yml`.

This walk-through's coder runs on Claude. A Codex coder (provider `openai`) writes only in the
project tree unless you set `coder.sandbox`; the default profile is `workspace`, and no key gives
today's argv. `profile: toolchain` with `add_dirs: [~/.cache/npm]` also opens that directory
(`--add-dir`); `~/` expands against HOME, each entry becomes an absolute realpath, and a symlink
is judged by its target. `profile: full` (`-s danger-full-access`) turns the Codex sandbox off:
the coder writes anywhere you can, with network as the CLI allows; the argv keeps the `$TMPDIR`
and `/tmp` exclusion flags, which Codex ignores there. Under `workspace` or `full`, `add_dirs` is
unused and `validate` warns `sandbox-dirs-unused`. `validate` refuses an `add_dirs` entry that is `/` or your home directory, or that is,
holds or sits inside a secret directory such as `~/.ssh`, `~/.codex` or `~/Library/Keychains`
(so `~/Library` too), the run's temp root or the session `CODEX_HOME`
(`sandbox-dir-refused`). `coder.sandbox` is fixed for the run too: `run reload` refuses a change,
and `spawn --role coder --run <r>` stops with `sandbox-changed` when the live value differs;
without `--run`, spawn reads the live config. The Claude and Grok coders ignore it. `doctor`
prints no `coder sandbox` row without the key, one INFO row with it, a WARN row for `full`, and
a no-effect INFO row only when a Claude or Grok coder is configured.

The hook in this walk-through appends one line per block end to a log. A real hook could post to
chat or wake the orchestrator.

```js
// scripts/on-block-end.mjs
import { appendFileSync, mkdirSync } from 'node:fs';

const { CODE_FORGE_RUN, CODE_FORGE_BLOCK, CODE_FORGE_BLOCK_STATUS, CODE_FORGE_DONE_FILE } = process.env;
mkdirSync('.code-forge', { recursive: true });
appendFileSync('.code-forge/block-ends.log', `${CODE_FORGE_RUN} ${CODE_FORGE_BLOCK} ${CODE_FORGE_BLOCK_STATUS} ${CODE_FORGE_DONE_FILE}\n`);
```

Check the config with `code-forge validate`.

## Step 2: start the run

```text
code-forge run start --mode brief --run cart-1
```

```text
run cart-1 started · engine harness · mode brief (no plan) · worker pid 28321
```

The ledger gets a signed `run.mode {mode: brief, plan: none}` row right after `run.start`, and a
`review.external` row naming the outside reviewer.

## Step 3: open the block

```text
code-forge block open B1 --run cart-1 --owned src/cart.mjs test/cart.test.mjs \
  --tests test/cart.test.mjs --acceptance plans/cart-total.acc.yml \
  --brief plans/cart-total.brief.md --lines 20
```

```text
block B1 open · L0 (rules lane) · code · attempt 1 · base 9a1c858ccd60 · 1 test file
worktree /work/shop-api-worktrees/B1 · branch forge/cart-1/B1
BRIEF plans/cart-total.brief.md lines=3 sha=a5824f50 <<<EOM>>>
```

- There is no `--level`. In a brief-mode run the rules pick the lane from the owned entries,
  `--lines` and `proof.tiers.high.paths`: a high, migration or policy/middleware path gives L2;
  more than 4 entries or more than 200 lines gives L1; anything else gives L0. Here it is L0. A
  signed `decision` row records it with `question: lane`, `answer: L0` and `source: rules`. An
  explicit `--level` still wins.
- `--tests` names the block's test files. The `{test_files}` gate runs only these.
- The second line is the block's worktree and branch. The coder works there.
- The last line is the pointer you send to the coder.

Add `--json` to any `block` verb for one JSON line instead of the text.

## Step 4: build the context pack

```text
code-forge context pack --run cart-1 --block B1
```

```text
/tmp/code-forge/cart-1/context/B1.md
tokens ~125
```

The pack holds the block header (run, block, level, owned files, test files, worktree), a
`## code-forge` section, the project's commands, the built-in proof rule, `AGENTS.md` and
`docs/lessons.md`. The `## code-forge` section names the exact Node executable, absolute
code-forge bin path and version of the install that built the pack. Use that exact command for
code-forge verbs; do not use another code-forge from PATH. Without a
`context` key, it holds the block header, code-forge section and proof rule, with a 300-line cap and no
commands, rules or lessons section. A bad `context` value still refuses the pack. It lives in the run's temp folder
(`tmp.root`, by default under the system temp folder). A signed `context.pack` row records its
sha and token estimate. Paste the pack's text into the coder's task together with the pointer.
Build it again before every later coder task, fix rounds too.

## Step 5: the coder

The coder gets the pointer and the pack. It works in the worktree:

1. edits `src/cart.mjs` and `test/cart.test.mjs`;
2. runs the block's tests;
3. commits on `forge/cart-1/B1`. It does not push and opens no PR, because both `autonomy` keys
   are false;
4. ends with its report. Just before the sentinel it prints the `TESTS` line. The sentinel
   `===BLOCK B1 COMPLETE===` is the report's last line:

```text
1 test, 1 pass, 0 fail
TESTS node --test test/cart.test.mjs exit=0
===BLOCK B1 COMPLETE===
```

Save the report to a file, for example `.code-forge/B1-report.txt`, and the coder's transcript to
`.code-forge/B1-transcript.log`. Files under the repository's `.code-forge/` never count as changes.

For a Codex coder, run `codex exec --json` in its worktree and save stdout as that transcript:

```sh
codex exec --json 'Implement B1 using its brief and context pack' > .code-forge/B1-transcript.log
```

Pass the saved log as `--transcript` at close. A Codex JSON log (every non-empty line is an
object with a string `type`, first event `thread.started`) is checked by its commands:
`command_execution` commands, `file_change` paths as writes, and other items whole (fail closed).
Agent messages, reasoning, command output (`aggregated_output`) and non-item events are skipped. Any other log
is checked line by line; a mention of a forbidden command or path is a hit.

## Step 6: run the gates

```text
code-forge gates run --slug shop-api --run cart-1 --block B1
```

The gates run in the block's worktree. The test gate runs `node --test test/cart.test.mjs`: its
result says `"scope":"related"`, `"test_count":1` and `"env_keys":["NODE_ENV"]`. The value of
`NODE_ENV` is never printed or recorded. With `--slug` the run gets a `gate.done` row. A block
that owns more than 10 files, or has no known test file, runs the full suite instead
(`"scope":"full"` with a `reason`).

Check progress now with `code-forge status --run cart-1`. B1 is still open and its gates passed:

```text
run cart-1 · active · brief · spent $0.00
B1 open L0 · - · gate pass · findings 0
```

When a test fails or a review leaves a finding open, the coder gets a fix round.
Fix rounds are one per block: wait for every `review-file` ticket of the block and its tests, then send ONE fix message with every failing test and every open finding; a finding that arrives later waits for the next round (`skill/references/code.md` §4).

## Step 7: close the block

```text
code-forge block close B1 --run cart-1 --report .code-forge/B1-report.txt \
  --transcript .code-forge/B1-transcript.log --remove-worktree
```

```text
block B1 closed
block B1 worktree removed: /work/shop-api-worktrees/B1 (branch forge/cart-1/B1 kept)
```

Because of `review.external`, the close skips the per-file review rows and the report's ticket
ids. It writes a signed `gate.reviews_waived {by: config}` row with the reviewer's name instead.
Every other check still runs:

- every changed file must be owned (`unowned_change` otherwise), and an empty change refuses
  (`no_changes`);
- the proof tiers: a changed high-tier file needs its proof row;
- the transcript grep for forbidden commands;
- the `TESTS` line, because of `context.require_coder_tests`. A passing close writes a signed
  `coder.tests` row. Waived reviews never lift this check.

`block close` does not run the gates. Run them in step 6 and read their result before you close.
With `gates.test` set in the config in force, close warns if the block's last gate result is not
a green `gate.done`, and still closes. With `gates.test: null`, it prints no such warning.

`--remove-worktree` removes the tree after the close and keeps the branch, with a signed
`worktree.removed` row. Push the branch and open the draft PR yourself.

## Step 8: the done file and the hook

Every block end writes a done file at `~/.code-forge/runs/cart-1/blocks/B1.done.json`:

```json
{"block":"B1","run_id":"cart-1","status":"closed","event":"block.close","reason":null,"commit":"c0897d187f2a1445086fa91ccc5cb9a502ead1e7","spent_usd":0,"unknown_usd_sessions":0,"findings":0,"at":"2026-10-12T09:11:34.671Z"}
```

Then the hook runs once, with `CODE_FORGE_RUN`, `CODE_FORGE_BLOCK`, `CODE_FORGE_BLOCK_STATUS` and
`CODE_FORGE_DONE_FILE` in its environment. Here it appends this line to
`.code-forge/block-ends.log`:

```text
cart-1 B1 closed /home/you/.code-forge/runs/cart-1/blocks/B1.done.json
```

A signed `hook.ran` row records the hook's exit. A failing hook only prints a warning. Wait for the
done file or the hook. Do not poll the coder's transcript.

## Step 9: check progress after the close

```text
code-forge status --run cart-1
```

No block is open now:

```text
run cart-1 · active · brief · spent $0.00
closed 1 · stopped 0
```

`status` prints at most 10 lines per run and never prints transcript or diff text. `--json`
prints the same facts as one line.

## Step 10: record the outside review

When the outside reviewer is done with the pull request, record its rounds and findings:

```text
code-forge ledger outside-review --run cart-1 --pr 42 --rounds 2 --findings 3 \
  --link https://example.com/pr/42 --block B1
```

```text
outside_review pr 42 · rounds 2 · findings 3 · blocks B1
```

A second call for the same PR replaces the first in the report. The ledger keeps both rows.

## Step 11: the report

```text
code-forge report --slug shop-api
```

`report` works while the run is still active. Its `outside_review` section puts the outside
rounds next to what code-forge spent on the PR's blocks. Its last lines show the run mode:

```text
== outside_review ==
PR #42 · 2 outside rounds · 3 findings · code-forge $0.00 · B1
spend per run (estimated USD, open blocks included):
  total: $0.0000 · 0 session(s)
run mode:
  run cart-1: brief
```

`report --json` has the same section as `outside_review`, one entry per PR with `pr`, `rounds`,
`findings`, `link`, `blocks`, `code_forge_usd` and `unknown_usd_sessions`.

## Step 12: end the run

```text
code-forge run end --run cart-1
```

```text
run cart-1 ended
```

`run end` refuses while a block is still open.

## Read next

- [how-it-works.md](how-it-works.md): the full workflow, plan mode included.
- [review-only.md](review-only.md): only review code you already wrote.
- [reference/config.md](reference/config.md): every `.code-forge.yml` key.
