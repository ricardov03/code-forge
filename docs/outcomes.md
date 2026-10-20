# Specialist outcomes

A specialist skill writes its result in `outcome.json`. The caller uses the result only when
the outcome is approved, its checks allow it, and every listed file matches its saved hash.
The format is defined in `schema/outcome.schema.json`.

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
