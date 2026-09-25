# Facts sheet — `<slug>`

<!-- rendered by `forge facts`; edit nothing below the header by hand -->

| field | value |
|---|---|
| brief | `<path>` |
| brief sha | `<sha256>` |
| brief mtime | `<iso-8601>` |
| built at | `<iso-8601>` |
| delegate | L0, read-only tools, empty cwd |
| claims | `<n>` — VERIFIED `<v>` · NOT-FOUND `<nf>` · UNVERIFIABLE `<u>` |

## Claims

| fact id | claim | kind | command | output excerpt | tag |
|---|---|---|---|---|---|
| F1 | `<claim token as written in the brief>` | flag \| command \| path \| version \| endpoint \| package \| env | `<the read-only command that was run>` | `<≤ 200 characters of its output>` | VERIFIED |
| F2 | … | … | … | `<why it could not be checked>` | UNVERIFIABLE |
| F3 | … | … | … | `<the empty or negative output>` | NOT-FOUND |

Rules the sheet was refused against: every `command` is read-only (checked against the forbidden list and a write-verb list); a VERIFIED tag has a non-empty excerpt; a tag is never inferred. An acceptance clause in the plan cites a fact by id (`F<n>`); a clause whose claim is NOT-FOUND or UNVERIFIABLE must appear in the plan's "Acceptance clauses the facts sheet cannot back" list with a tolerance naming a block, or `forge plan check` refuses it.
