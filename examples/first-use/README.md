# first-use inputs

The inputs of the one block of the rehearsal in `docs/tutorial-first-test.md` ("add `clamp()` with
tests") on a copy of `examples/node-lib/`:

- `clamp.brief.md` — the brief; `code-forge facts` builds its facts sheet, `code-forge author --job
  plan` drafts the plan from both, and `block open --brief` points the coder at the plan.
- `clamp.acceptance.yml` — the block's acceptance clauses (`block open --acceptance`).
- `coder/` — a finished edit for comparison. In a real run the coder is a harness subagent or a
  `code-forge spawn` coder process.
