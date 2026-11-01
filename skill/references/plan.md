# Plan — facts first, then an L3 draft, then a deterministic check

## §0 Facts before design (step 0 of every plan)

Run `forge facts --brief <brief> [--sources <path>...] --out <plans_dir>/<slug>.facts.md` before anything else.

Claims come from the brief only. `--sources <path>` chooses where checks run. Source text adds no claims.

What it does:

1. The CLI extracts claim tokens from the brief: flags, commands, paths, versions, endpoints, packages and environment names. Whole code spans also name `symbol` claims: PascalCase types with two or more humps, snake_case identifiers and code file names such as `SigmaClient.php`. A prose word such as Model adds no symbol. Command prefixes still come from the brief and the project's `package.json` bin names. For example, `run reload` becomes `code-forge run reload` when the brief names that project CLI. A known CLI or a CLI named separately in the brief keeps its own name.
2. One L0 delegate checks a read-only snapshot. The project uses its git HEAD tree plus sources inside it. Each outside source directory appears under `_sources/<label>/`. Its label is its folder name; repeated names get `-2`, `-3`. Git sources use HEAD, limited to the named subfolder. Non-git sources use a copy. An outside source file appears at `_sources/<label>/<file name>`. Symlinks and secret files are dropped. Files and directories are sealed. The snapshot is removed when the session ends.

   Absolute paths inside a root map to its snapshot path. Relative paths get one row per root when several roots are present. Flags and symbols also get a row per root. These rows have a `source` label. The sheet adds a `source` column after `kind` when any row has that label. A single-root relative claim keeps the old row format. Routes such as `/api/v1/*`, HTML fragments such as `/h1`, and `/dev/null` are not claims. An absolute path outside every source root and the tool roots is UNVERIFIABLE when its first directory exists on this machine.

   Before the packet is sent, one `git check-ignore` batch checks each root's paths. Ignored paths are UNVERIFIABLE: `git-ignored in <label>: the snapshot leaves ignored paths out`. A path present on disk but absent from the snapshot is UNVERIFIABLE: `not at HEAD in <label>: outside the snapshot`. Neither goes to the delegate. Sources inside the project may still supply untracked files to the snapshot.

   The delegate runs one read-only command per row and quotes its literal output. A flag needs `grep -rn -- <flag> <source folder>`. An identifier symbol needs `grep -rn -- 'TenantSubscription' app` or `grep -rn -- 'class TenantSubscription' app`. The quoted pattern is the token alone or one of `class interface trait enum struct type function func def` followed by the token. VERIFIED needs a `file:line:text` source-file hit holding the whole token. Markdown, text, docs, plans, tests, fixtures and the brief do not count. Symbols search one whole top-level source folder. A flag may use a nested source folder for VERIFIED; NOT-FOUND needs a whole top-level folder. Top-level means directly below a root: `src` or `_sources/<label>/app`. `_sources` itself never counts.

   A file-name symbol needs `find <root> -name SigmaClient.php`, where the root is `.` or `_sources/<label>`. VERIFIED needs an output path ending in `/SigmaClient.php`. A glob path needs `find lang/es -name '*.php'`. Its pattern is quoted. Only that claim may also use `ls -d lang/es/*.php`, quoted or unquoted. VERIFIED needs a matching output path. NOT-FOUND needs `No such file` output. Unquoted globs on other claims are refused.

   The session gets the shell tool with write deny rules and provider restrictions. Node validates the reported command afterward. Allowed reads include `ls cat head tail wc grep stat file uname sw_vers jq which`, `command -v`, the restricted `find` predicates, `git log|show|status|rev-parse|ls-files|cat-file`, `npm view|ls`, `test -e|-f|-d|-n`, and a single-URL HEAD `curl` request. No pipes or command lists. No program runs with `--help`, `-h` or `--version`. Versions use `npm view`, `which` or `ls`. Environment claims use only `printenv NAME >/dev/null`; their excerpts are blanked. Backticks, `$`, other redirections, `~`, `..`, secret paths and absolute paths outside `/usr`, `/opt/homebrew`, `/bin` are refused. Root paths in the brief use their mapped snapshot paths for checks. `npm view` and `curl -I` may reach the network.
3. Node validates the closed delegate schema. A bad proof or an empty VERIFIED excerpt downgrades that row to UNVERIFIABLE with a reason. It does not count. Source labels are added by Node. Every claim, command, excerpt and reason passes through the redactor. The rendered sheet records the brief's sha and mtime.

**Do not read the sources yourself; read the sheet.** Your context is spent on design, never on `--help` output. A fact is never a model's opinion: every claim carries the command that produced it.

## §1 The author job

`forge author --job plan --brief <brief> --facts <sheet> --out <plan>` runs a fresh L3 closed-book session (empty cwd, the packet on stdin) and returns `{draft, questions[], cost}`. It refuses without `--facts` (exit 2, `facts sheet required: run forge facts first`) and refuses a sheet older than the brief. The draft follows `templates/plan.md`; its §0 **is** the facts sheet, verbatim, and it must contain the section "Acceptance clauses the facts sheet cannot back" — `none` when empty, never absent. The author's packet carries the exact required headings from the same list `plan check` reads (`src/session/plan-sections.mjs`).

## §2 Decomposition — what every block carries

| field | rule |
|---|---|
| `id`, `title` | one deliverable per block |
| `owned_files` | exact paths or disjoint globs; a glob never contains `[ ] ( ) ! + @` (an exact path may); pairwise disjoint across blocks that can run together |
| `depends_on` | acyclic; a block imports only modules of blocks it depends on — a batch-mate's module is a `next = stop` |
| `level` | the S1 `lane` answer recorded for the block — `forge jev ask lane --block <id> --plan <plan> --state <file>`, or `--rules` (the deterministic fallback) when Jev is unavailable; never a block category (`L0` trivial · `L1` a plain feature following a pattern · `L2` money, dates, tenancy, migrations, concurrency · `split` too large or two concerns); **L3 is not a lane** |
| `acceptance` | clauses that each name ≥ 1 test id, numbered `(1) … · (2) …` or separated by `;` (`plan check` splits the cell on both and checks each clause on its own); a clause citing a flag, path, version or API cites a VERIFIED fact id or sits in the unbackable list with a tolerance naming this block |
| `cases`, `lines` | forecasts: `cases = 2 × counted clauses`, `lines = 26 × cases` with a floor of 1 200 for a greenfield block and 500 for an amendment; above `budget.block_cases` or `budget.block_lines` the block is split in the plan, never "split-ready" |
| `test_command` | a quoted glob or an explicit file list, never a bare directory |
| `commit_message` | house style |

A file everyone needs (a route file, an enum, a registry) is a serialization point: one early block owns it, the others depend on it. Never more than `caps.coders` blocks are marked concurrent in one dispatch slot.

## §3 A mechanism needs a caller (R8)

Every clause of the parent's acceptance maps to exactly one block; an unmapped clause keeps the parent open whatever the sub-blocks report. A block that ships a mechanism is not shipped until a caller block is dispatched or deferred with a named id. The plan carries a **caller map** (mechanism → block that invokes it); `plan check` refuses a block table whose id is missing from it.

## §4 The deterministic exit — `forge plan check <plan> [--facts <sheet>]`

Prints every failing row by name and exits 1 on any of: an acceptance clause with a claim token that resolves to no VERIFIED fact and no tolerance line (`unbackable clause without tolerance: <block> <clause>`) — a tolerance line is a list item of §0.x naming the block, the token and the word `tolerance`, as `templates/plan.md` shows (`- B3 \`--flag\` (F4, UNVERIFIABLE) — tolerance: …`); a row of the older table form with a filled tolerance column still counts (`—`, `-`, `…`, `n/a` and the like are empty); a missing caller map or a block absent from it; a block without `cases` and `lines`; a forecast above `budget.block_cases` / `budget.block_lines` without a named split; overlapping `owned_files` in one wave; a cyclic `depends_on` or an import of a batch-mate; a glob with `[ ] ( ) ! + @`; more than `caps.coders` concurrent blocks in a slot; `questions[]` still open; the facts sheet's sha absent from the draft; a block whose level has no recorded lane, or differs from it (`block <id>: level L2 differs from the recorded lane L1 (jev)`). A required section under a near-miss heading at its template position (§0.x, §3) passes with a `WARN` naming the exact heading. stdout ends with the lane recorded per block (`lanes: B1 L1 (jev) · B2 L0 (rules)`). `plan check` and `jev ask` may run from any folder of the project: both read the project root's config and ledger (the nearest `.code-forge.yml` file up to the git top level, else the git top level; outside git, never above the home directory, and only the folder itself when it is not under home), never a subfolder's name. `author`, `facts`, `spawn`, `s2` and `run start` read the same root. Green ⇒ set `status: hardened` after the harden job (`harden.md`), never before.

### §4.1 Owner approval in plan mode

A plan that passed `forge plan check` still needs the owner's yes before any block is dispatched. Show it in the harness's plan mode so the owner reviews the whole plan in one place and approves or edits it there:

- **Claude Code:** call `EnterPlanMode`, write the plan (context, blocks with their levels and lanes, dispatch order, risks, verification) to the plan file, then call `ExitPlanMode`. Its approval is the go signal; a rejection or an edit sends you back to `harden`.
- **A harness without a plan mode:** print the plan's §1–§4 and §6 and wait for an explicit yes. Silence is not approval.

Never treat a chat summary of the plan as the approval step, and never start `forge block open` before it.

**Autopilot:** plan approval is never delegated. Under a grant the plan waits for the owner (`autopilot.md` §5).

## §5 Cost

One L0 session per plan for the facts (typically a claim list of 40–80 items) and one L3 session per author round; `forge report --slug <slug>` shows both as `facts $` and coder/review splits.

## §6 Brief mode — briefs, no plan

For a small change whose blocks are clear from their briefs (a fix, a doc pass, one or two blocks with no ordering to argue about), skip §0–§4: `forge run start --mode brief`. The run record stores `mode: brief` and the ledger gets one signed `run.mode {mode: brief, plan: none}` row right after `run.start`; the mode is fixed for the run (`run reload` never changes it) and `forge report` lists each run as `brief` or `plan`. Plan mode (`--mode plan`, or no flag) is the default and is unchanged. There is no separate verb for a task: each block is still `forge block open` with its brief, owned files and acceptance clauses, and is closed by the same gate.

In a brief-mode run `forge block open <id> --run <r> --owned <paths…> --acceptance <file> --brief <file> --lines <n>` may leave out `--level`: the lane then comes from the deterministic rules (the `--rules` fallback of `jev ask lane`), over these facts only —

- an owned entry under `proof.tiers.high.paths` of the run's config snapshot (or a directory entry above one), a migration path, or a policy/middleware path ⇒ `L2`;
- else more than 4 owned entries or more than 200 estimated changed lines (`--lines`) ⇒ `L1`;
- else `L0` (a docs or contract block is still raised to `levels.coder_floor_docs`).

`block open` writes one signed `decision {question: lane, answer: <lane>, source: rules}` row naming the block, then the `dispatch` row, and prints `(rules lane)` after the level. An explicit `--level` still wins and writes no decision row. Give `--lines` whenever you can: without it the size rule sees only the number of owned entries. Outside brief mode a missing `--level` with no autopilot level stays a usage error, and `--lines` stays the forecast it always was.
