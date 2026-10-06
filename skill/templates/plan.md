# `<feature>` — plan

| field | value |
|---|---|
| status | facts \| planned \| hardened \| coding \| done |
| brief | `<path>` (sha `<sha8>`) |
| facts sheet | `<path>` (sha `<sha8>`, built `<iso-8601>`) |
| authored at | L3 via `forge author --job plan`; hardened via `forge author --job harden` (`<n>` rounds) |
| run | `<run id>` once `forge run start` has run |

## §0 Facts sheet (verbatim)

<!-- the whole facts sheet, unchanged; `forge plan check` looks for its sha here -->

### §0.x Acceptance clauses the facts sheet cannot back

`none` — or one list item per unbacked claim, naming the block, the claim token, its fact id and tag, and the word `tolerance` (`forge plan check` reads these lines):

- B3 `--flag` (F4, UNVERIFIABLE) — tolerance: <how the block's acceptance survives it>

## §1 Architecture decision

Chosen approach and why · rejected alternatives (one line each) · the project conventions that apply · risk class of this tranche (`harden.md` §3).

## §2 Blocks

| id | title | level | depends_on | owned_files | cases | lines | acceptance (clauses, each naming its test ids and fact ids) | test_command | commit_message |
|---|---|---|---|---|---|---|---|---|---|
| B1 | … | L1 | — | `src/x.mjs`, `test/x.test.mjs` | 12 | 1 200 | (1) … (`test/x.test.mjs`, F3) · (2) … | `node --test 'test/x/**/*.test.mjs'` | … |

Acceptance clauses are numbered `(1) … · (2) …` (or separated by `;`); `forge plan check` splits the cell on both and checks each clause on its own.

`level` is the lane recorded for the block by `forge jev ask lane --block <id>` (or `--rules`), never a category; `forge plan check` refuses any other.

Split rule: `cases = 2 × clauses`, `lines = 26 × cases` (floor 1 200 greenfield, 500 amendment); above `budget.block_cases` or `budget.block_lines` the block is split here. A glob in `owned_files` never contains `[ ] ( ) ! + @`.

## §3 Caller map

| mechanism | shipped by | called by |
|---|---|---|

Every block id above appears in this map; a mechanism with no caller names the block that will call it or is not shipped (`plan.md` §3).

## §4 Dispatch order

Slots = `caps.coders`. A block starts when a slot frees and every block in its `depends_on` has been committed: `1. B1 ∥ 2. B2 → 3. B3 (when B1 lands) → …`.

## §5 Hardening Q&A

<!-- appended by the harden job: question · options · the human's answer · what changed in §2 -->

## §6 Risks and mitigations

## §7 Block records

<!-- one `templates/block-record.md` per dispatched block, appended by the code job -->
