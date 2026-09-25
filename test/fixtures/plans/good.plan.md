# Fixture plan: a small review tool

## 0. Facts (the sheet, verbatim)

# Facts sheet

- brief: `../briefs/tool-brief.md`
- brief_sha256: `f84545299f1f5c4016e26cacdef056b08b4ccabbf7819124184180384d14ab17`
- brief_mtime: `2026-09-25T00:00:00.000Z`
- built_at: `2026-09-25T00:00:00.000Z`
- claims: 7 (VERIFIED 3 · NOT-FOUND 2 · UNVERIFIABLE 2)

| fact_id | tag | kind | claim | command | output | why |
|---|---|---|---|---|---|---|
| F1 | VERIFIED | command | `claude plugin` | `claude --help \| grep -c plugin` | 3 | — |
| F2 | VERIFIED | flag | `--json-schema` | `claude --help \| grep -c -- --json-schema` | 1 | — |
| F3 | NOT-FOUND | flag | `--max-turns` | `claude --help \| grep -c -- --max-turns` | 0 | — |
| F4 | UNVERIFIABLE | path | `~/.code-forge/ledger` | — | — | the delegate may not read .code-forge paths |
| F5 | VERIFIED | package | `@types/node` | `npm view @types/node version` | 22.20.4 | — |
| F6 | UNVERIFIABLE | endpoint | `https://api.typesafe.ai/v1/systemone` | — | — | a POST endpoint; no read-only check reaches it |
| F7 | NOT-FOUND | env | `DO_NOT_TRACK` | `printenv DO_NOT_TRACK >/dev/null` | — | — |

<!-- facts-json
[
{"fact_id":"F1","claim":"claude plugin","kind":"command","command":"claude --help | grep -c plugin","output_excerpt":"3","tag":"VERIFIED","why":null},
{"fact_id":"F2","claim":"--json-schema","kind":"flag","command":"claude --help | grep -c -- --json-schema","output_excerpt":"1","tag":"VERIFIED","why":null},
{"fact_id":"F3","claim":"--max-turns","kind":"flag","command":"claude --help | grep -c -- --max-turns","output_excerpt":"0","tag":"NOT-FOUND","why":null},
{"fact_id":"F4","claim":"~/.code-forge/ledger","kind":"path","command":"","output_excerpt":"","tag":"UNVERIFIABLE","why":"the delegate may not read .code-forge paths"},
{"fact_id":"F5","claim":"@types/node","kind":"package","command":"npm view @types/node version","output_excerpt":"22.20.4","tag":"VERIFIED","why":null},
{"fact_id":"F6","claim":"https://api.typesafe.ai/v1/systemone","kind":"endpoint","command":"","output_excerpt":"","tag":"UNVERIFIABLE","why":"a POST endpoint; no read-only check reaches it"},
{"fact_id":"F7","claim":"DO_NOT_TRACK","kind":"env","command":"printenv DO_NOT_TRACK \u003e/dev/null","output_excerpt":"","tag":"NOT-FOUND","why":null}
]
-->

## 0.6 Acceptance clauses the facts sheet cannot back

1. `--max-turns` is NOT-FOUND — B2 cites it only to prove the reviewer never passes it; tolerance: B2 asserts against the help fixture, never a live CLI.
2. `~/.code-forge/ledger` is UNVERIFIABLE (the delegate may not read `.code-forge` paths) — B1; tolerance: B1 asserts the path under a temp HOME.

## Wave 1

**Dispatch order:** 1. B1 ∥ 2. B2 → 3. B3 (when B1 lands).

| id | title | level | depends_on | owned_files | imports | items → cases → lines | acceptance |
|---|---|---|---|---|---|---|---|
| **B1** | Config loader | L1 | — | `src/config/**`, `test/config/**` | — | 4 → 8 → **1 200** | the reviewer argv carries `--json-schema` (1); rows land under `~/.code-forge/ledger` (1) |
| **B2** | Reviewer session | L2 | — | `src/session/**`, `test/session/**` | `src/util/log.mjs` | 3 → 6 → **1 200** | the reviewer argv never carries `--max-turns` (1); an empty brief is refused (1) |
| **B3** | Status report | L1 | B1 | `src/report/**`, `types/node-extra.d.ts` | `src/config/load.mjs` | 2 → 4 → **1 200** | the report prints the `claude plugin` status (1) |

## Caller map

B1 config → B2, B3 · B2 reviewer → the review verb · B3 report → the human.
