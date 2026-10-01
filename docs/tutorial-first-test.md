# Tutorial: the first end-to-end test

This tutorial runs the whole workflow once, on a real but small project: **the code-forge
marketing site**. You write a brief, build a facts sheet, author and check a plan, open one block
("hero section and install command"), let a coder write it, review each file, prove it, close it,
and read the report.

It takes one block, so you see every step once. The site's other blocks are yours to run after.

> Want a fully automated rehearsal first? See [the rehearsal](#the-automated-rehearsal) at the
> end. It runs the same steps on the example library shipped with the package.

## Before you start

- Node 22+, `git`, and Claude Code (the `harness` engine) or Solo.
- A provider CLI that `code-forge resolve L0` … `L3` can use (`claude` for the default
  `anthropic` matrix).
- A key for Jev, a small fast classifier model from TypeSafe (System 1), is optional. This tutorial uses `--no-jev`, so rules and System 2 answer instead.

## 1. Install code-forge and make a new, separate repository

Install the CLI once, globally, or check that it is already there:

```bash
npm install -g @codedology/code-forge
code-forge version
```

`code-forge version` prints `@codedology/code-forge <version>`. From here on every command is
`code-forge <verb>`. The scoped name appears only in the install line; the unscoped npm name
`code-forge` is a different, unrelated package.

Never run this tutorial inside the code-forge package repository. Use a fresh folder:

```bash
mkdir code-forge-site
cd code-forge-site
git init
mkdir plans site test
```

The site's own tests use `node:test`, so the site needs a tiny `package.json`: ES modules, and a
`test` script that `init` detects as the test gate. It has no dependencies, so there is no
lockfile:

```json
{
  "name": "code-forge-site",
  "private": true,
  "type": "module",
  "scripts": { "test": "node --test" }
}
```

Create `.gitignore` with one line. code-forge keeps local state in `.code-forge/` (review queue,
review results, coder logs, exports):

```text
.code-forge/
```

Get the brief. It is not in the npm package, so fetch it from GitHub:
[docs/examples/marketing-site/brief.md](https://github.com/ricardov03/code-forge/blob/main/docs/examples/marketing-site/brief.md).
Save it as `plans/site.md`:

```bash
curl -fsSLO https://raw.githubusercontent.com/ricardov03/code-forge/main/docs/examples/marketing-site/brief.md
mv brief.md plans/site.md
```

Then make the base commit:

```bash
git add .gitignore package.json plans/site.md
git commit -m "base: empty site and brief"
```

**Look for:** `git log --oneline` shows one commit, and `git status --short` prints nothing. The
facts delegate reads a read-only snapshot of **HEAD**, so anything it should see must be
committed.

**Why a clean tree matters.** `block close` refuses with `orphans: <paths>` when any file that
changed since the block's base sha, or any untracked file not covered by `.gitignore`, belongs to
no open block's `owned_files`. So everything that is not the block's own work must be committed
(or ignored) **before** `block open` records the base.

## 2. Run `init`

```bash
code-forge init
```

Pick Claude Code as the harness, choose **global** scope, and choose "skip" for the Jev key. Inside
a git repository the default scope is **project**, which would put a skill link at
`.claude/skills/code-forge` in this repo; with a global install, global scope keeps the link under
your home folder and nothing extra in the repo. `init` then prints the settings it found: the
`test` gate from `package.json`, and `lint`, `types` and `format` blank, since the site has no
linter, type checker or formatter. Pick **Use these**. Or, without questions:

```bash
code-forge init --no-interaction --no-jev --harness claude -g --engine harness
```

Then check it:

```bash
code-forge validate
code-forge doctor --quick
code-forge resolve L1
```

**Look for:**

| Where | What |
|---|---|
| `.code-forge.yml` | `project.slug: code-forge-site`, `gates.test: [npm, test]`, `engine: harness` (or `auto`), `review.multimodel` off |
| `doctor --quick` | no FAIL rows. The Jev key row reflects the skip (rules-only System 1) |
| `code-forge list` | the skill linked for `claude`, `RESOLVES` yes |

Commit the config (never `.code-forge/`, which `.gitignore` already covers):

```bash
git add .code-forge.yml
git commit -m "code-forge: project config"
git status --short
```

`git status --short` must print nothing. If `init` wrote any other file into the repo, commit it
or add it to `.gitignore` now.

## 3. Facts

The skill starts here when you type `/code-forge plan plans/site.md` (or `/code-forge full
plans/site.md`) in Claude Code. The orchestrator runs:

```bash
code-forge facts --brief plans/site.md --run site-1
```

**Look for:**

- stdout: `{"out": ".../plans/site.facts.md", "claims": <n>, "verified": <m>}`.
- `plans/site.facts.md`: each claim with a tag and the exact command and output line. Expect
  `./package.json` and `node:test` to be VERIFIED, and `./site/index.html` to be NOT-FOUND: it
  does not exist yet. That is correct.
- `code-forge ledger tail --slug code-forge-site` shows a `facts.built` row.

## 4. Author the plan

```bash
code-forge author --job plan --brief plans/site.md --facts plans/site.facts.md
```

**Look for:**

- `plans/site.plan.md` (the default `--out`). Its §0 is the facts sheet, verbatim.
- A block table close to the brief's suggestion: B1 hero, B2 how-it-works, B3 links. Each block
  has `owned_files`, `cases`, `lines`, `depends_on` and clauses with test ids.
- A caller map, and a section "Acceptance clauses the facts sheet cannot back". The new `site/`
  files belong there with a tolerance naming their block.
- stdout lists `questions` and the round's cost.

Without `--facts` the command refuses with exit 2 (`facts sheet required`). That refusal is the
rule "no design before facts".

## 5. Harden and `plan check`

Answer the questions in a file (plain text, one answer per question id), then:

```bash
code-forge author --job harden --brief plans/site.md --facts plans/site.facts.md --draft plans/site.plan.md --answers plans/site.answers.md --out plans/site.plan.md
code-forge plan check plans/site.plan.md --facts plans/site.facts.md
```

Repeat the harden round until no blocking question is left.

**Look for:** `plan check: ok (3 blocks)` and exit 0. Anything else prints one line per failing
rule, for example `unbackable clause without tolerance: B1 …`. Fix the plan and run it again.

## 6. Start the run

```bash
code-forge run start --run site-1 --engine harness
```

**Look for:**

- `run site-1 started · engine harness · worker pid <n>`. Under the skill, the orchestrator also
  prints the no-Solo banner once (one coder at a time).
- `~/.code-forge/runs/site-1.json` (the run record) and `~/.code-forge/runs/site-1.key` (mode 0600).
- `code-forge run status --run site-1` prints the run, with the worker pinned.

## 7. Open block B1

Write the B1 clauses from the plan to `plans/B1.acceptance.yml`:

```yaml
- clause: site/index.html shows npm install -g @codedology/code-forge and code-forge init, each inside a code element
  tests: [test/hero.test.mjs::the hero shows the scoped install command]
```

Commit every plan artifact now, so the block's base is clean and none of them can become an
orphan at close:

```bash
git add plans/site.facts.md plans/site.plan.md plans/site.answers.md plans/B1.acceptance.yml
git commit -m "plan: marketing site (facts, plan, answers, B1 acceptance)"
git status --short
```

`git status --short` must print nothing. Then open the block. `--lines` is the line forecast from the plan's B1 row (1 200 below is the
plan's floor for a new block; use your plan's own number):

```bash
code-forge block open B1 --run site-1 --level L1 --owned site/index.html site/styles.css test/hero.test.mjs --acceptance plans/B1.acceptance.yml --brief plans/site.plan.md --lines 1200
```

**Look for:**

- `block B1 open · L1 · attempt 1 · base <sha>`
- the pointer line `BRIEF plans/site.plan.md lines=<n> sha=<sha8> <<<EOM>>>`
- a `dispatch` ledger row with the level and the forecast.

## 8. The coder writes the files

With the skill, the orchestrator sends the pointer to a Claude Code subagent at the level's model.
By hand, open a second Claude Code session in the repo and give it the pointer line.

**Look for, in order:**

1. The coder's first line is `ACK <sha8> lines=<n>`.
2. A **facts diff**: every place where the plan and the code disagree, in one message.
3. A forecast of cases and lines.
4. Code, one file at a time. After each file:
   `code-forge review-file <file> --block B1 --run site-1`, then
   `code-forge review-file --wait <ticket>`.
5. A progress line per file in `.code-forge/runs/site-1/B1.log`.
6. The last line: `===BLOCK B1 COMPLETE===`, the test summary and `git diff --stat`.

## 9. Review per file

You do not start reviews yourself; the coder's `review-file` calls do. Watch them:

```bash
code-forge ledger tail --slug code-forge-site --n 40
```

**Look for, per owned file:** `review.plan` (the chosen depth), one `review.round` row per round
(`open_before`, `closed`, `open_after`), `review.done`, and a signed `review.approved` row for the
file's current content hash. The site files should be light tier, low risk: one `quick` or `full`
L2 review each, usually one or two rounds.

If a round stalls or a file reaches four rounds, the ledger shows an `escalation` or
`stopped: review_cap`. That is the ladder working, not a crash. See
[how-it-works.md](how-it-works.md#fix-rounds-and-the-escalation-ladder).

## 10. Proof

Measure in a clean export, not in your working tree:

```bash
code-forge proof export B1 --run site-1
code-forge gates run --cwd .code-forge/export/B1
code-forge proof red-green B1 --run site-1 --test "test/hero.test.mjs::the hero shows the scoped install command"
code-forge proof export B1 --run site-1 --remove
```

**Look for:**

- The export JSON names its `dir` and what it restored.
- `gates run` prints `"allOk": true`.
- `proof red-green` reports `red` with `red_kind: assertion`, then `green`, and `proven: true`.
  With the `revert` mechanism the new `site/index.html` is removed for the red run, so the test
  must fail on an assertion. If it reports `RED_INVALID`, the test crashed instead of asserting:
  fix the test (see the brief's "How to write the tests").
- A signed `proof` row in the ledger.

Red→green is **required** at close only for high-tier files. These files are light tier, so it is
optional here. Run it anyway: it is the step that proves the test can fail.

## 11. Close the block

```bash
code-forge block close B1 --run site-1
```

**Look for:** `block B1 closed` and exit 0, plus a `block.close` ledger row. Then commit the block
(the orchestrator does this in your house style):

```bash
git add site test
git commit -m "B1: hero section and install command"
```

If the close is refused, the message names the reason. Common ones:

| Refusal | Meaning | What to do |
|---|---|---|
| `unreviewed <file>` | a changed file has no signed approval for its current content | run `review-file` for it and wait |
| `orphans: <paths>` | a changed or untracked (not ignored) file that no open block owns | `code-forge block claim B1 <path> --run site-1`, or delete it |
| `unproven <file>` | a high-tier file has no red→green row | run `proof red-green` for its test |
| `WARN … no coder transcript found` | the transcript grep could not run | pass `--transcript <file>` |

## 12. End the run and read the report

```bash
code-forge run end --run site-1
code-forge report --slug code-forge-site
```

**Look for:** the worker process is gone, and the report prints 13 sections, each headed
`== <name> ==`: `cost_per_block`, `lane_distribution`, `escalations`, `s1_calls_per_block`,
`s2_rate`, `review_budget`, `review_depth_and_findings`, `fix_rounds`, `review_unavailable`,
`proof_time`, `forecast_vs_actual_lines`, `blocks_stopped_at_l3`, `run_stop_counts`.
`cost_per_block` shows B1 with its split between coder, review, S1+S2 and facts. Tokens are
facts; dollars are estimates. `report --json` and `report --export <dir>` give the same data as
JSON.

## Pass criteria: "the workflow works"

The first test passes when all of these hold:

1. `code-forge validate` exits 0 and `code-forge doctor --quick` shows no FAIL.
2. `plans/site.facts.md` exists and every VERIFIED claim has a command and an output line.
3. `plans/site.plan.md` exists, starts with the facts sheet, and `plan check` prints `ok`.
4. `run start` pinned a worker, and `run end` stopped it (no leftover worker process).
5. Every file B1 changed has a signed `review.approved` row in the ledger.
6. `proof red-green` proved the hero test with `red_kind: assertion`.
7. `block close B1` printed `block B1 closed`, with no `rule_break` or `ledger.tamper` row.
8. `npm test` is green in the repository, and `site/index.html` shows `npm install -g @codedology/code-forge`.
9. `report --slug code-forge-site` prints all 13 sections, with B1 in `cost_per_block`.

Then run B2 and B3 the same way. B3 is where the link and unscoped-command checks land.

## The automated rehearsal

This package already has an automated version of the same walk-through. It runs on a **copy** of
the shipped example library `examples/node-lib` (block B1 "add `clamp()` with tests"), in a temp
folder, never in this repository. From the package repository:

```bash
node scripts/first-use.mjs --engine harness
```

It prints one `== <n>. <step> ==` line per step (`copy`, `init`, `facts`, `author`, `start`,
`open`, `code`, `review`, `proof`, `close`, `report`), then a final `FIRST-USE {json}` line.

- With `--engine harness` the coder step waits for a harness subagent to write
  `===BLOCK B1 COMPLETE===` into `.code-forge/runs/<run>/B1.log` in the copy (up to one hour,
  `--coder-timeout <s>`).
- `--coder scripted` applies a fixed edit instead of a live coder: the fully automatic form.
- `--until <step>` stops after a step. `--tmp-root <abs dir>` chooses where the copy goes.
- Facts, author and reviews still make real calls to the provider CLI.
