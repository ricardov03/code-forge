# Overview

`@codedology/code-forge` is two things that ship together:

1. **A CLI**, `code-forge`. It holds every fact: model ids, efforts, thresholds, paths, gates,
   keys, the ledger. It starts every model session that is not your own.
2. **One Agent Skill**, `code-forge` (`skill/SKILL.md`). It is prose only. It tells the session
   you talk to (the *orchestrator*) what to call and in which order.

Together they run a feature from a short written brief to reviewed, proven, committed blocks of
code, with cheap models on easy work and strong models on hard work.

## The problem

Coding agents are good at writing code. The workflow around them is where things go wrong:

| Pain | What usually happens |
|---|---|
| Cost | The top model does every job, even a rename. |
| Trust | The agent says "tests pass". They pass for the wrong reason, or they were never run. |
| Late review | One big review at the end. Every bug costs a full rework loop. |
| Fragile workflow | The workflow names models. A model changes and the workflow breaks. |
| One harness | It works in one agent tool. A teammate uses another. |
| Lost state | A session dies mid-run and the plan lives only in its context. |

## How code-forge answers it

- **Levels, not model names.** Work is routed to a level, `L0` to `L3`. The config maps each
  level to a provider, model and effort. Change one line of config, not the workflow.
- **Coders run at most at L2.** A coder starts at L0, L1 or L2 and climbs one level at a time,
  never past L2. L3 is used only for planning and hardening (the plan author), System 2, the
  review judge, and a one-time coder patch rung: at most 80 lines, once per block, when a block
  is stuck at L2. (`escalation.l3_mode: code` lets L3 code a block; it is opt-in, not the default.)
- **Tools first, then a fast decision model, then a slow one.** Tests, types, lint and `git diff`
  answer facts. A fast decision model (System 1: Jev, a small fast classifier model from TypeSafe) answers small typed questions with a
  probability. A strong model (System 2) is asked only when System 1 is not sure.
- **Review each file when it is done.** A fresh, isolated L2 session reviews each finished file
  while the coder moves on. Fix rounds are bounded: at most 4 per file.
- **The coder's report is a claim sheet.** `block close` re-measures everything itself: files
  touched, reviews signed, gates green, clauses covered, line forecast, and red→green proof for
  high-tier files.
- **State lives outside the conversation.** A run record, a signed ledger and the plan file let a
  new session take over.

## The three jobs

| Job | What happens | Output |
|---|---|---|
| **Plan** | An L0 delegate checks every claim in the brief against the repository (the *facts sheet*). The plan author, always an L3 session (`code-forge author --job plan`), drafts a plan split into blocks. | `plans/<name>.facts.md`, `plans/<name>.plan.md` |
| **Harden** | The same L3 author (`--job harden`) asks the hard questions. You answer. `code-forge plan check` must be green. | a hardened plan |
| **Code** | One coder per block at the level System 1 picks (L0–L2). Each finished file gets its own review. `block close` is the gate. | one commit per block, ledger rows, a report |

In the skill: `/code-forge plan|harden|code|full <file>`. `full` runs all three with one human
checkpoint after `harden`.

## Harnesses and engines

`code-forge init` installs the skill where each agent tool already looks:

| Harness you work in | Skill installed | With `engine: auto` (the default), coders run as… |
|---|---|---|
| Claude Code | yes | Solo processes when Solo is present, else Claude Code subagents (`harness`) |
| Codex CLI, Grok CLI | yes | Solo processes when Solo is present. **Without Solo the run stops** with a short text: these CLIs have no subagent tool code-forge can use. Add `engine: subprocess` to `.code-forge.yml` to run coders as detached CLI processes instead |
| Gemini CLI, Cursor, Copilot | yes (detect and link) | same as Codex/Grok: Solo, or `engine: subprocess` |

Reviewers, judges, System 2, the plan author and the facts delegate are always isolated CLI
sessions started by code-forge, built for the `claude`, `codex` or `grok` CLI. The *engine* only
decides how the coder runs:

| Engine | Chosen when | Coders run as |
|---|---|---|
| `solo` | `engine: auto` and Solo is present, or `engine: solo` | Solo processes you can watch |
| `harness` | `engine: auto`, no Solo, and the harness has a subagent tool (only Claude Code in this version), or `engine: harness` | the harness's own subagents, one at a time |
| `subprocess` | only when you write `engine: subprocess` in the config; never picked by `auto` | detached `claude`, `codex` or `grok` processes, per the provider each level resolves to |

With `engine: auto` and neither Solo nor a subagent tool, nothing is picked: the run stops with
the text in `skill/references/degraded.md` §1. `init` prints the same text at setup when it detects
that case. The orchestrator resolves `auto` at preflight and records the engine with
`code-forge run start --engine <e>`; a run started by hand without `--engine` keeps `auto` in
its record.

Providers in the level matrix: `anthropic` (default), `openai`, `xai`.

## Who it is for

- Solo developers and small teams who ship real products with coding agents.
- Teams that use more than one agent tool.
- Leads who need evidence, not "the agent said it passed".

## What it is NOT

- **Not a model or a coding agent.** It orchestrates the agents and CLIs you already have.
- **Not a hosted service.** Everything runs on your machine. There is no telemetry.
- **Not a sandbox.** Signed ledger rows stop accidents and make tampering visible, but the
  boundary is the same OS user only. See the security section of
  [how-it-works.md](how-it-works.md#security-in-short).
- **Not mutation testing.** Proof is exact tests plus a red→green runner. Mutation testing was
  considered and cut.
- **Not a CI system or merge bot.** A coder may open a draft PR when the rules allow it. Marking
  it ready is the orchestrator's job; merging is the human's.
- **Not a promise of a cost saving.** The ledger records real tokens per block. No calibrated
  saving number is published yet.

Next: [how-it-works.md](how-it-works.md).
