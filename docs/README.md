# code-forge documentation

These pages explain what `@codedology/code-forge` is, how it works, and how to run it for the first
time. Every command they show exists in the CLI as built. When a page and a verb's own usage line
disagree, the verb is right: each verb prints its own usage line when its arguments are wrong.

## Reading order

| # | Page | Read it when you want to… |
|---|---|---|
| 1 | [overview.md](overview.md) | know what code-forge is, the problem it solves, who it is for, and what it is not |
| 2 | [concepts.md](concepts.md) | look up a word: block, level, lane, tier, S1/S2, waiver, ledger, budget, block kind, closed book, known fix… |
| 3 | [how-it-works.md](how-it-works.md) | follow the full workflow from a brief to a closed block, and see who does what and where state lives |
| 4 | [getting-started.md](getting-started.md) | install it, answer `init`, run `doctor`, try the first commands, and read or report errors (`code-forge logs`) |
| 5 | [tutorial-first-test.md](tutorial-first-test.md) | run one real end-to-end test: build the code-forge marketing site in a new repo |
| 5a | [review-only.md](review-only.md) | only review code you already wrote: a branch, uncommitted work or some files, with CI exit codes |
| 5b | [autopilot.md](autopilot.md) | step away while a run goes on: grant a delegate a few decisions for a set time, and review the live log (Binnacle and Full log) when you return |
| 6 | [examples/marketing-site/brief.md](examples/marketing-site/brief.md) | copy the brief the tutorial uses |
| 7 | [marketing.md](marketing.md) | read the product story: the problem, the promise, the honest limits |

New to the tool? Read 1 → 3 → 4, then do 5. Keep 2 open as a glossary. Only want reviews? Read 4,
then 5a.

## Reference pages (already in the repo)

| Page | What it is |
|---|---|
| [../CHANGELOG.md](../CHANGELOG.md) | what changed in each version; `## [Unreleased]` is the next one |
| [reference/config.md](reference/config.md) | every `.code-forge.yml` key, its type and default. Generated from `schema/code-forge.schema.json` |
| [reference/blocks.json](reference/blocks.json) | the build blocks of this package and what each one owns |
| [marketing/code-forge-story.md](marketing/code-forge-story.md) | the marketing content bank (copy, lessons, what not to claim) |
| [privacy.md](privacy.md) | what the local error log holds (errors, warnings, the last command names), every cleaning rule, the AI check, the setup check (`--with-doctor`), known fixes, and how to turn the log off or delete it |
| [releasing.md](releasing.md) | for maintainers: record changes (`npm run changelog`) and known fixes (`npm run known-fix`), cut a release with one command (`npm run release`), publish by hand, GitHub Releases, the error-triage Action |
| [`skill/SKILL.md`](../skill/SKILL.md) and [`skill/references/`](../skill/references/) | the Agent Skill the orchestrator follows. The deepest and most exact description of the rules |

## Conventions in these pages

- `code-forge <verb>` is the CLI installed globally with `npm install -g @codedology/code-forge`
  (see [getting-started.md](getting-started.md#install); a per-project pinned install is covered
  there too). Inside the skill the same CLI is called `forge` (a shim at `skill/scripts/forge`).
- `<run>` is a run id, `<slug>` is the project slug (`project.slug` in `.code-forge.yml`, else the
  folder name), `<id>` is a block id such as `B1`.
- "Not yet" marks something that is designed but not built. Nothing else on these pages is a plan.
