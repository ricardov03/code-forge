# Facts sheet — `<slug>`

<!-- rendered by `forge facts`; edit nothing below the header by hand -->

| field | value |
|---|---|
| brief | `<path>` |
| brief sha | `<sha256>` |
| brief mtime | `<iso-8601>` |
| built at | `<iso-8601>` |
| delegate | L0, read-only tools, sealed source snapshot |
| claims | `<n>` — VERIFIED `<v>` · NOT-FOUND `<nf>` · UNVERIFIABLE `<u>` |

## Claims

| fact id | claim | kind | command | output excerpt | tag |
|---|---|---|---|---|---|
| F1 | `<claim token as written in the brief>` | flag \| command \| path \| symbol \| version \| endpoint \| package \| env | `<the read-only command that was run>` | `<≤ 200 characters of its output>` | VERIFIED |
| F2 | … | … | … | `<why it could not be checked>` | UNVERIFIABLE |
| F3 | … | … | … | `<the empty or negative output>` | NOT-FOUND |

Claims come from the brief only. `--sources` chooses roots for checks. Outside roots appear under `_sources/<label>/`. Rows with a root label have a `source` field. When present, the table adds a `source` column after `kind`. Single-root relative rows omit it.

A `symbol` is a whole code span naming a PascalCase type, snake_case identifier or code file name. Identifiers use `grep -rn -- 'TenantSubscription' app` or a quoted keyword and token such as `'class TenantSubscription'`. VERIFIED needs a source-file `file:line:` hit holding the whole token. NOT-FOUND needs a whole top-level source folder. File names use `find . -name SigmaClient.php` (or `_sources/<label>` as the root). VERIFIED needs a path ending in `/SigmaClient.php`.

Ignored paths are UNVERIFIABLE with `git-ignored in <label>: the snapshot leaves ignored paths out`. Untracked paths absent from the snapshot are UNVERIFIABLE with `not at HEAD in <label>: outside the snapshot`. Neither is sent to the delegate. Routes and `/dev/null` add no claims. Glob paths use a quoted `find -name` pattern or `ls -d` of the exact glob.

Every command is read-only. A bad proof downgrades its row to UNVERIFIABLE. A VERIFIED tag needs a non-empty excerpt. A tag is never inferred. An acceptance clause in the plan cites a fact by id (`F<n>`). A clause whose claim is NOT-FOUND or UNVERIFIABLE must appear in the plan's "Acceptance clauses the facts sheet cannot back" list with a tolerance naming a block, or `forge plan check` refuses it.
