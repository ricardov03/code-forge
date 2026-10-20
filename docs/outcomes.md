# Specialist outcomes

A specialist skill writes its result in `outcome.json`. The caller uses the result only when
the outcome is approved, its checks allow it, and every listed file matches its saved hash.
The format is defined in `schema/outcome.schema.json`.
Use a [pipeline](pipelines.md) to name dependent specialist steps and see which step is ready.

| Field | Meaning |
|---|---|
| `skill` | Required string. The skill that made the result. |
| `version` | Optional string. The skill version. |
| `request` | Optional string. The request or brief path. |
| `status` | Required. One of the four statuses below. |
| `approved_by` | Optional string or null. Who approved the result. |
| `artifact` | Optional string or null. A link to the result. |
| `files` | Required array. Each item has a string `path` and a string `sha256`. |
| `decisions` | Optional array of strings. Decisions made during the work. |
| `checks` | Optional array. Each item has a string `name` and a `result`: `pass`, `warn`, or `fail`. Optional `detail` is an array of strings. Optional `evidence` is a string. |
| `cost_usd` | Optional number or null. Cost in US dollars. |
| `minutes` | Optional number or null. Time spent in minutes. |

Unknown fields are refused at the top level and in file and check items. File paths are relative
to the root directory. Absolute paths, paths containing `..`, and symlinks outside root are refused.
The root defaults to the current directory. The outcome file argument is relative to the current
directory, even when `--root` is given.

The statuses mean:

- `draft`: work in progress.
- `approved`: the owner approved it.
- `blocked`: needs the owner.
- `failed`: checks did not pass.

For example, save this as `outcome.json` in a project that contains `brand/logo.svg`:

```json
{
  "skill": "logo-designer",
  "version": "0.1.0",
  "request": "brand/logo-brief.md",
  "status": "approved",
  "approved_by": "owner",
  "files": [{ "path": "brand/logo.svg", "sha256": "" }],
  "checks": [{ "name": "logo-check-final", "result": "pass" }]
}
```

Run `code-forge outcome seal` to fill every file's SHA-256 hash:

```sh
code-forge outcome seal outcome.json --root .
```

Seal keeps key order and writes JSON with two-space indentation and a final newline. It exits 0
on success. A missing file or unsafe path exits 1 and leaves the outcome unchanged. Sealing does
not grant approval. Set the status to `approved` only after the owner approves the files.

Run `code-forge outcome check` before using the result:

```sh
code-forge outcome check outcome.json --root .
code-forge outcome check outcome.json --root . --json
```

Check exits 0 and prints `ok` only when the schema is valid, status is `approved`, every file
exists with the saved SHA-256 hash, and no check is `fail`. A `warn` check is allowed. Other
results exit 1 and print one line per problem to stderr. With `--json`, stdout contains
`{"ok":true,"problems":[]}` or `{"ok":false,"problems":["..."]}`.
Unknown subcommands or missing arguments print usage to stderr and exit 2.

Run `code-forge outcome record` to check the result and save its cost and time in the project ledger:

```sh
code-forge outcome record outcome.json --root .
code-forge outcome record outcome.json --root . --step design --json
```

Recording runs the same check first. Problems exit 1 and write nothing. A passing outcome adds
one signed `outcome.recorded` row with `skill`, `step`, `status`, the SHA-256 of the outcome file
bytes, `cost_usd`, and `minutes`. Missing cost or minutes are stored as null. The command uses
the project slug from its config (or directory name) and the existing signing key of its latest
run, so start a project run before recording; no new key is created.

`--step` sets the step id. Otherwise a matching outcome path in `code-forge.pipeline.yml` supplies
the id; without a match, the step is null. Recording the same bytes for the same step again
prints `already recorded` and adds no row. `--json` returns `ok`, `problems`, `already_recorded`,
and the signed `row` when one was added. Check failures use the same JSON as `outcome check`.

`code-forge report --slug <slug>` ends with `specialist_outcomes`: one entry per recorded outcome
and totals for count, cost, and minutes. The same section is included with `--json` and `--export`.
Calibration rows named `outcome` are separate and do not enter this section.
