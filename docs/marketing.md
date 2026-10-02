# code-forge: the story

This is the narrative for the site, posts and talks. The longer content bank, with copy variants,
lessons and the "do not claim" list, is [marketing/code-forge-story.md](marketing/code-forge-story.md).
Where the two differ on a mechanism, the built behaviour on this page and in
[how-it-works.md](how-it-works.md) is the one to use.

## Headline

**Your coding agents, organised like an engineering team.**

Cheap models for easy work. Strong models for hard work. Tools for facts. A fast decision model in
between.

```bash
npm install -g @codedology/code-forge
code-forge init
```

## The problem

You already use coding agents. The agent is not the weak part. The workflow around it is.

- You pay the top model to rename a button.
- The agent says the tests pass. They pass for the wrong reason.
- Review happens at the end, so every bug costs a full rework loop.
- Your workflow names models. A model changes, and the workflow breaks.
- It works in one agent tool. Your teammate uses another.

## The promise

code-forge runs every job on the cheapest level that can do it, reviews every file the moment it
is written, and closes a block only when the evidence is re-measured, not quoted.

## How it works, in five steps

1. **Facts.** A small model checks every claim in your brief against the repository, with a
   read-only command and its output. No design starts before the facts.
2. **Plan and harden.** A strong model splits the work into blocks with owned files and testable
   clauses, then asks the hard questions. You answer them. A deterministic `plan check` must pass.
3. **Code.** One coder per block, at the level a fast decision model picks. It climbs one level
   only on evidence: failed attempts, a stalled review, a review round with several warnings, a
   security change.
4. **Review per file.** Each finished file gets a fresh, isolated reviewer that sees only its diff.
   Fix rounds re-check only the fix, must shrink every round, and stop at four.
5. **Proof and close.** The gate re-runs the tests in a clean export, checks a signed approval for
   every changed file, checks that the coder's report names a review for each one, and requires
   red→green proof for high-risk files. Then one commit.

## What makes it different

| | Typical agent workflow | code-forge |
|---|---|---|
| Model choice | one model for everything | levels L0–L3; config maps them to models |
| Decisions | the expensive model decides everything | tools answer facts; a half-second decision model answers small questions; a strong model only when unsure |
| Review | once, at the end | per file, fresh session, bounded rounds |
| "Tests pass" | trusted | re-measured by the gate; high-risk tests must be proven to fail first |
| Getting stuck | loop, or jump to the biggest model | climb one level at a time to L2; one small L3 patch; then a human |
| State | in the chat | run record, signed ledger, plan file |
| Harness | one tool | Claude Code, Codex CLI, Grok CLI; the skill also installs into Gemini CLI, Cursor, Copilot |

**Cost control is built in, not promised.** Coders never run above L2. The top level plans, judges
and writes at most one 80-line patch per stuck block. Reviews see diffs, not whole runs, and a
low-risk file gets one reviewer. The ledger records real tokens and an estimated cost per session;
`code-forge report` shows the cost per block, open blocks included, and the spend per run. Set
`budget.usd` and a run stops starting sessions when it reaches it.

## Honest limits

- **No saving number yet.** The ledger records real data, but no calibrated baseline is
  published. We do not quote a percentage.
- **Signed rows are not a sandbox.** They stop accidents and make tampering visible. A process
  running as your own OS user can still forge them. For a hard boundary, run coders as a second
  user or in a container.
- **Codex does not review yet.** Reviews, rulings and plans run closed book, with no tools. Codex
  always has a shell, so code-forge refuses it for those roles; use Claude or Grok there. Codex
  still codes.
- **Coders run in Claude Code (subagents), Solo, or as Claude, Codex or Grok CLI processes.** Gemini
  CLI, Cursor and Copilot get the skill, not coder runs, in this version.
- **Verified stacks.** The measurement export is proven on a Node library with `node:test`. Other
  stacks can use `proof.isolation: lock` until they are proven.
- **No mutation testing.** Proof is exact tests plus red→green. This is a deliberate choice.

## Call to action

Try it on something small first. The [tutorial](tutorial-first-test.md) builds this very site
with code-forge, one block at a time. The scoped name is only for the install line; the command is
`code-forge`.

```bash
npm install -g @codedology/code-forge
code-forge init
```

Then read [getting-started.md](getting-started.md) and [how-it-works.md](how-it-works.md).
