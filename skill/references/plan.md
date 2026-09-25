# Plan — facts first, then an L3 draft, then a deterministic check

## §0 Facts before design (step 0 of every plan)

Run `forge facts --brief <brief> [--sources <path>...] --out <plans_dir>/<slug>.facts.md` before anything else. What it does:

1. Deterministic: the CLI extracts every *claim token* from the brief and the sources it names — CLI flags, `<cli> <verb>` commands, paths, versions, endpoints, package names, environment variable names — and writes a claim list.
2. One L0 session whose cwd is a **read-only snapshot of the repository at HEAD plus the `--sources` files**, built under the run's temp root and removed at session end — so `./path` claims are checkable, while **uncommitted files are not visible unless named as sources** (the snapshot is HEAD, not the working tree) — and whose packet is the claim list and the rule "run the cheapest read-only check, quote the command and the output line, tag VERIFIED / NOT-FOUND / UNVERIFIABLE, never infer". Two lines of defence, exactly as built. **First, at spawn:** the session gets the shell tool only, with the forbidden list plus every write verb rendered as deny rules and the provider's restricted mode (file tools confined to the working directory). There is no pre-execution gate on individual commands beyond those deny rules: the delegate runs what it runs. **Second, after the fact:** the CLI checks every `command` the delegate reports against an allow-list, fail closed, and refuses the whole sheet on the first violation. Allowed forms: `ls cat head tail wc grep rg stat file uname sw_vers jq which` with in-cwd arguments (`grep` never recursive), `find` with only these predicates — `-name -iname -path -ipath -type -maxdepth -mindepth -print -print0 -newer -size -empty -mtime -mmin -not ! -a -o -and -or` — every other predicate refused, `git log|show|status|rev-parse|ls-files|cat-file|--version`, `npm view|ls|--version`, `node --version`, `test -e|-f|-d|-n <one operand>`, `curl` as a HEAD request of exactly one http(s) URL with `-s -S -I -L -f --max-time <N>` only, and `<cli> --help|-h|--version` for a bare CLI name that is not a build runner; an env claim is only `printenv NAME >/dev/null` with its excerpt blanked. Refused anywhere: backticks, `$`, redirection, unquoted globs, `~`, `..`, absolute paths outside `/usr`, `/opt/homebrew`, `/bin`, and secret-looking paths. **`npm view` and `curl -I` reach the network** — they are what makes a package version or an endpoint checkable; nothing else the delegate may run does.
   **Path claims:** `./path` claims resolve against the HEAD snapshot, so a committed file is VERIFIED or NOT-FOUND by a plain `ls`/`test -e`; a file that exists only uncommitted in the working tree reads NOT-FOUND unless you passed it with `--sources`. `~/…` claims stay UNVERIFIABLE (`~` is refused), and absolute paths are allowed only under `/usr`, `/opt/homebrew` and `/bin`; a home-directory path that matters to a block is checked by you with one read-only command and recorded in the unbackable list with that command as the tolerance.
3. Deterministic again: the answer is validated against the facts schema, rendered with `templates/facts-sheet.md`, and refused when a VERIFIED tag has no output excerpt. Every claim, command and excerpt passes through the redactor. The header records the brief's sha and mtime.

**Do not read the sources yourself; read the sheet.** Your context is spent on design, never on `--help` output. A fact is never a model's opinion: every claim carries the command that produced it.

## §1 The author job

`forge author --job plan --brief <brief> --facts <sheet> --out <plan>` runs a fresh L3 closed-book session (empty cwd, the packet on stdin) and returns `{draft, questions[], cost}`. It refuses without `--facts` (exit 2, `facts sheet required: run forge facts first`) and refuses a sheet older than the brief. The draft follows `templates/plan.md`; its §0 **is** the facts sheet, verbatim, and it must contain the section "Acceptance clauses the facts sheet cannot back" — `none` when empty, never absent.

## §2 Decomposition — what every block carries

| field | rule |
|---|---|
| `id`, `title` | one deliverable per block |
| `owned_files` | exact paths or disjoint globs; a glob never contains `[ ] ( ) ! + @` (an exact path may); pairwise disjoint across blocks that can run together |
| `depends_on` | acyclic; a block imports only modules of blocks it depends on — a batch-mate's module is a `next = stop` |
| `level` | the S1 `lane` answer (`L0` trivial · `L1` a plain feature following a pattern · `L2` money, dates, tenancy, migrations, concurrency · `split` too large or two concerns); **L3 is not a lane** |
| `acceptance` | clauses that each name ≥ 1 test id; a clause citing a flag, path, version or API cites a VERIFIED fact id or sits in the unbackable list with a tolerance naming this block |
| `cases`, `lines` | forecasts: `cases = 2 × counted clauses`, `lines = 26 × cases` with a floor of 1 200 for a greenfield block and 500 for an amendment; above `budget.block_cases` or `budget.block_lines` the block is split in the plan, never "split-ready" |
| `test_command` | a quoted glob or an explicit file list, never a bare directory |
| `commit_message` | house style |

A file everyone needs (a route file, an enum, a registry) is a serialization point: one early block owns it, the others depend on it. Never more than `caps.coders` blocks are marked concurrent in one dispatch slot.

## §3 A mechanism needs a caller (R8)

Every clause of the parent's acceptance maps to exactly one block; an unmapped clause keeps the parent open whatever the sub-blocks report. A block that ships a mechanism is not shipped until a caller block is dispatched or deferred with a named id. The plan carries a **caller map** (mechanism → block that invokes it); `plan check` refuses a block table whose id is missing from it.

## §4 The deterministic exit — `forge plan check <plan> [--facts <sheet>]`

Prints every failing row by name and exits 1 on any of: an acceptance clause with a claim token that resolves to no VERIFIED fact and no tolerance row (`unbackable clause without tolerance: <block> <clause>`); a missing caller map or a block absent from it; a block without `cases` and `lines`; a forecast above `budget.block_cases` / `budget.block_lines` without a named split; overlapping `owned_files` in one wave; a cyclic `depends_on` or an import of a batch-mate; a glob with `[ ] ( ) ! + @`; more than `caps.coders` concurrent blocks in a slot; `questions[]` still open; the facts sheet's sha absent from the draft. Green ⇒ set `status: hardened` after the harden job (`harden.md`), never before.

## §5 Cost

One L0 session per plan for the facts (typically a claim list of 40–80 items) and one L3 session per author round; `forge report --slug <slug>` shows both as `facts $` and coder/review splits.
