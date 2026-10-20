# Specialist pipelines

A pipeline names the specialist steps in a project and the outcomes they need.
Save `code-forge.pipeline.yml` in the project root, next to `.code-forge.yml`.
The format is defined in `schema/pipeline.schema.json`.

```yaml
root: website-kit
steps:
  - id: logo
    skill: logo-designer
    outcome: brand/logo/outcome.json
    approval: human
  - id: design
    skill: website-kit
    needs: [logo]
    outcome: design/outcome.json
    approval: human
  - id: docs
    skill: docs-designer
    needs: [design]
    outcome: docs-site/outcome.json
    approval: none
```

| Field | Meaning |
|---|---|
| `root` | Required string. The root skill name, such as `website-kit`. It is not a folder path. |
| `steps` | Required non-empty array. Commands print steps in this order. |
| `steps[].id` | Required unique string. Starts with a lowercase letter or digit. Uses only lowercase letters, digits and hyphens. |
| `steps[].skill` | Required string. The skill that runs the step. |
| `steps[].needs` | Optional array of step IDs. Defaults to `[]`. Each named step must be approved first. Forward references are allowed. |
| `steps[].outcome` | Required relative path to an outcome JSON file. Absolute paths and `..` segments are refused. |
| `steps[].approval` | Required. `human` means the step calls for human approval. `none` means no human approval is requested. |

Unknown keys, duplicate IDs, unknown needs and dependency cycles are refused.
Unknown needs and cycle errors name a step ID.
Outcome paths and the file paths inside outcomes resolve against the folder that holds the pipeline.
See [outcomes.md](outcomes.md) for the outcome fields and seal command.

Both commands below only read files. They do not run skills or grant approval.
An outcome must have status `approved` and pass its outcome check before a dependent step is ready.
This rule applies to both approval values.

## Status

```sh
code-forge pipeline status
code-forge pipeline status --root ./my-project --json
```

With no outcomes yet, the example prints:

```text
logo  ready  logo-designer
design  waiting  website-kit
docs  waiting  docs-designer
```

Each step has one state:

| State | Meaning |
|---|---|
| `waiting` | At least one need is not approved. This takes priority over the step's own outcome. |
| `ready` | All needs are approved and the outcome file does not exist. |
| `draft` | The outcome is valid and has status `draft`. |
| `blocked` | The outcome is valid and has status `blocked`. |
| `failed` | The outcome is valid and has status `failed`. |
| `approved` | The outcome has status `approved` and passes `checkOutcome`. |
| `invalid` | The outcome is unreadable, is invalid JSON, fails its schema, or is approved but fails its outcome check. |

An invalid step includes its first problem:

```text
logo  invalid  logo-designer  — file "brand/logo.svg": sha256 does not match
design  waiting  website-kit
docs  waiting  docs-designer
```

JSON has the shape `{"exists":true,"steps":[{"id":"logo","skill":"logo-designer","state":"ready"},...]}`.
An invalid row also has a `problem` string.

## Next

```sh
code-forge pipeline next
code-forge pipeline next --root ./my-project --json
```

With no outcomes yet, the example prints `logo` on one line.
When several steps are ready, it prints one ID per line in file order.
When every step is approved, it prints `all steps approved`.
When no step is ready and some work remains, it prints:

```text
nothing ready
design  waiting for logo
docs  waiting for design
```

Only needs that are not approved appear in the waiting lines.
JSON always includes ready IDs, waiting steps and a done flag:

```json
{"exists":true,"ready":["logo"],"waiting":[{"id":"design","needs":["logo"]},{"id":"docs","needs":["design"]}],"done":false}
```

Both commands exit 0 for a valid pipeline, including when a step is invalid or nothing is ready.
If the pipeline file is absent, they print `no code-forge.pipeline.yml in <root>` and exit 0.
With `--json`, that result is `{"exists":false}`.
An invalid pipeline prints one problem per line to stderr and exits 1.
Unknown subcommands or bad flags print usage to stderr and exit 2.
Without `--root`, the project folder is the current directory.

## Wait for an outcome before opening a block

Use pipeline step IDs with `block open --after-outcome`:

```sh
code-forge block open SITE --run <r> --level L2 --owned src/site.mjs --acceptance site.acc.yml --after-outcome logo
```

The pipeline and its outcomes are read from the run's project root, the main checkout where
`pipeline status --root <project-root>` runs, even when the block uses its own worktree.
The `logo` outcome must exist, have status `approved`, and pass `outcome check` against that root.
Until then, open exits 1 with `outcome-not-ready`, naming the step and its first problem.
A missing or invalid pipeline, or an unknown step ID, exits 1 with `bad-after-outcome`.
These refusals create no rows or worktree. With `--json`, they print
`{"block":"SITE","event":"refused","code":"outcome-not-ready","message":"..."}`
(or code `bad-after-outcome`).

Supply several IDs or repeat the flag to wait for multiple outcomes; duplicate IDs are removed.
The block entry and signed `dispatch` row record `after_outcome: [{step, sha256}]`, with the
SHA-256 of each accepted outcome JSON file's exact bytes. Combine `--after <block ids>` with
`--after-outcome <step ids>` to require both closed blocks and approved specialist outcomes.
The existing `--after` refusals take priority. Each open attempt checks the flags supplied on that call.

## Validate

After the config passes, `code-forge validate` also validates the pipeline beside that config.
It checks the schema, duplicate IDs, unknown needs and cycles. It does not check outcome files.
Each pipeline problem prints as an error line and makes the command exit 1.
With `validate --file <path>`, the pipeline is read from the selected config's folder.
If no pipeline file is there, validation behaves and prints as before.
