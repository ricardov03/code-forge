# first-use inputs

What `scripts/first-use.mjs` feeds the one block of the first real use ("add `clamp()` with tests")
on a copy of `examples/node-lib/`:

- `clamp.brief.md` — the brief; `code-forge facts` builds its facts sheet, `code-forge author --job
  plan` drafts the plan from both, and `block open --brief` points the coder at the plan.
- `clamp.acceptance.yml` — the block's acceptance clauses (`block open --acceptance`).
- `coder/` — the deterministic edit `--coder scripted` applies (the automated test uses it). In a
  real run the coder is a harness subagent (`--coder harness`) or a `code-forge spawn` coder process
  (`--engine subprocess`, which spawns it with `--coder spawn`).
