# Autopilot

Autopilot lets you step away while a run keeps going. You give a **delegate** (a fresh L2 or L3
session) the right to make a few of your decisions, for a set time. Everything it does is a signed
ledger row. One live page, the **log**, shows it all, so you can review it when you come back.

You start it. The orchestrator never starts it on its own, and a coder can never run `autopilot`
(it is on the coder's forbidden list).

## Start it

Ask the orchestrator in chat ("autopilot until 7 tomorrow, the delegate may waive nits and pick the
coder level"). It runs `code-forge autopilot start` and you confirm at the prompt:

```bash
code-forge autopilot start --run <run> \
  --until 2026-10-07T07:00:00+02:00 \
  --delegate L2 \
  --allow waive:nit,waive:warning,model:choose \
  --budget review=5,coding=20 --stop-at 0.9
```

- `--until` is a time with an offset, at most 24 hours ahead. The grant ends by itself then.
- Or `--until-done` instead of `--until`: the grant ends when every planned block of the run is
  closed (below). Give one of the two, never both.
- `--delegate` is `L2` or `L3`. The delegate is a closed-book session and never Codex.
- `--allow` lists the scopes it may decide (below). `--deny` takes some away again.
- `--budget` sets a cap in USD per category (`coding`, `review`). `--stop-at` is the share of the
  cap at which that category pauses (default `0.9`).
- Without a terminal the start is refused, unless you pass `--yes` yourself.

### Until the work is done

```bash
code-forge autopilot start --run <run> --until-done \
  --deadline 2026-10-07T07:00:00+02:00 --blocks B1,B2,B3 \
  --delegate L2 --allow waive:nit,close:routine
```

- The grant ends when every planned block is closed. A block closed with its human checks
  deferred (`awaiting_human_check`) counts as closed; a stopped block does not.
- The planned blocks are the ids you give with `--blocks`. Without `--blocks` they are all the
  blocks opened in the run so far. Give `--blocks` when you plan more waves: between waves every
  opened block may be closed, and the grant would end early.
- A run with no blocks yet keeps the grant active until a block opens and closes.
- `--deadline` (optional, at most 24 hours ahead) ends it at that time if the blocks are not all
  closed by then. Without it the grant has no clock end; stop it with `autopilot stop`.
- When it ends because the work is done, the next autopilot or block command, or the worker,
  writes one signed `autopilot.expire` row with `"reason": "done"` and the planned blocks.
  `autopilot status` then says `done at <time> · every planned block closed`, and shows each
  planned block's state while it runs.

### Push the end out

```bash
code-forge autopilot extend --run <run> --until 2026-10-07T09:00:00+02:00
```

It moves a live grant's end later without stopping it: one signed `autopilot.extend` row (the old
end in `from`, the new one in `until`), and you confirm at the prompt as for `start` (`--yes`
without a terminal). For an until-done grant use `--deadline <time>` to set or move its deadline.
It refuses an earlier or equal time, a time more than 24 hours ahead, a grant that is stopped,
expired, done or absent, an ended run, and `--until` on an until-done grant with no deadline.

Only one grant is active per run. `code-forge autopilot status --run <run>` shows it: time left,
scopes, the deny list, the spend per category, and where the log is.

## Scopes and the fixed deny list

| Scope | What the delegate may decide |
|---|---|
| `waive:warning` | waive a warning finding |
| `waive:nit` | waive a nit finding |
| `round:extra` | one fix round past the review cap for a file |
| `model:choose` | choose the coder level for a block |
| `close:routine` | close a routine block when `close.require_human` is on: every changed file approved, a green gate row, no waivers, no transcript hits. Never `block close --decide` |
| `review:substitute` | run one substitute review of a file whose review stays unavailable after the built-in recovery and `review.on_unavailable`: a closed-book reviewer on a provider none of the failed sessions used. Each substitute gets a signed `autopilot.decision` row first, and `block close` counts its approval only under this scope with every gate green |
| `human-pass:deferred` | close a block whose only open items are `human_check` clauses as `closed_deferred_human` (Go engine, B632): every other close gate must pass as for a normal close. The checks stay open in `run human-checks list` (marked deferred) for you; the block may land. One signed `autopilot.decision` row (scope, block, clauses, `waits_for`) and one binnacle line per block with your `run human-checks clear` command. Clearing its last check makes the block `closed`. Never a human pass. `run end` refuses while any of these checks is open, and the binnacle's `Human checks` row names them (B633) |

An extra round is a normal fix round. Fix rounds are one per block: wait for every `review-file` ticket of the block and its tests, then send ONE fix message with every failing test and every open finding; a finding that arrives later waits for the next round (`skill/references/code.md` §4).
The per-file cap still counts per file: one block round is one round for each file it touched.

**Substitute reviews** (`review:substitute`, B630). When a file's review is still unavailable
after the built-in recovery and the `review.on_unavailable` chain, the Go worker looks at the
grant. No active grant: nothing changes, the review stays unavailable. An active grant without
the scope: one signed `autopilot.decision` row to you (`acted: false`) per file and content, whose
`owner_reason` and `owner_command` name the fix: `code-forge autopilot start --run <run> --until
<time> --delegate L2 --allow review:substitute`; else the review retries when its provider
answers again. With the scope: the worker picks the first configured route (the L2 ladder, then
L3, L1, the other levels and the `review.on_unavailable` routes) on a provider none of the failed
sessions used that a closed-book reviewer may use and the session layer can run, writes a signed
`autopilot.decision` row (`decision: substitute`, `failed_providers`, `unavailable_reason`,
`provider`, `model`) and runs one review there. Its `review.result`, `review.round` and
`review.approved` rows carry `source: substitute` and `substitute: {provider, model}`. A critical
finding still blocks: the substitute writes no approval while findings are open. `block close`
counts a substitute approval only with an active grant holding the scope, the signed decision
row for that file and content, no gate blocker and a signed green `gate.done` row; otherwise it
refuses with a `substitute_review` blocker whose fix line is the `autopilot start` command above.
A normal review of the changed file replaces the substitute. When none is runnable, the
decision row says so (`owner_command: null`) and the review retries later.

The **deny list** is fixed in the code. You cannot allow these, whatever you pass:

| Scope | Never delegated |
|---|---|
| `waive:critical` | waive a critical finding |
| `waive:proof` | waive a proof finding |
| `reviews:skip` | close a block without its reviews |
| `limits:change` | change a limit or rule (budgets, thresholds, review caps, escalation, proof, System 1, autopilot itself) |
| `plan:approve` | approve a plan |
| `design:approve` | approve a design |
| `pr:merge` | merge a pull request |
| `destructive` | run a destructive action |
| `budget:raise` | raise a budget |

When one of these comes up while you are away, the work on it waits for you. It shows under
"Actions only you can take" in the log. Neither the orchestrator nor the delegate relaxes a rule
or a limit to keep work moving: when a limit blocks work, they stop, record the data and wait.

## The delegate and its options

For every question that would reach you, the orchestrator first asks the delegate:

```bash
code-forge autopilot ask --run <run> --scope waive:nit --question "Waive nit F3 in src/a.mjs?" \
  --options waive,fix --block B2 --file src/a.mjs --finding F3
```

The delegate only picks between the options it is given. Each action has fixed option words:
`waive,fix` for a waiver, `allow,deny` for an extra round, and the candidate levels (for example
`L1,L2`) for a coder level. Its answer is **acted on** (exit 0) only when it picked one of the
options, stayed within scope, did not ask to escalate, and its confidence is at least
`autopilot.min_confidence` (default 0.7). Otherwise the question goes to you (exit 3). A question
outside the grant is refused without a session (exit 1).

Every answer is one signed `autopilot.decision` row with an id. The action then names it:
`code-forge autopilot waive|round|level … --decision <id>`. An action without a matching acted
decision (same grant, scope and block/file/finding, at most 30 minutes old) is refused. A waiver is
written `by: autopilot` and opens one tracked GitHub issue in the project's repo.

While a grant is active, `block open` and `block amend-acceptance` refuse an acceptance clause
of `kind: human_check`. An unattended run never waits on a human check. Open such blocks before
the grant starts, or after it ends.

## Budget stop

With `--budget`, each category pauses when its spend since the grant started (plus what running
sessions hold) reaches `--stop-at` × its cap. New sessions in that category are refused with
"autopilot paused: … waiting for the owner", and one signed `autopilot.pause` row is written. The
other category keeps going. Only you can raise a cap.

## Approve a change for a while

`code-forge autopilot approve --run <run> --key <dot.path> --value <json> --until <time>` changes
one setting until a time, then puts the old value back by itself. Only you run it, at a terminal;
it has no `--yes`, so no script or session can approve for you. A value you change by hand in the
meantime is left alone. Keys fixed for the run are refused by name.

## The log: one live page with two tabs

At start, in Claude Code with the Claude Docs connector, the orchestrator creates one doc named
`<project> autopilot run — <date>` and keeps it current during the whole window:

- **Binnacle** (the summary): the title and byline, then 7 sections: Status at a glance,
  Decisions, Blocks, Open questions, Actions only you can take, Incidents, Timeline. Each decision
  says when, why, and what would reverse it. The timeline is newest first.
- **Full log**: every autopilot event in time order, newest first. One row per signed ledger row:
  the grant, each question and answer (with confidence and reason), each waiver and its issue
  link, each refusal, each expiry or restore, each block close with its commit, each incident.

The link is stored in the grant right after the doc is created
(`code-forge autopilot binnacle --run <run> --link <url>`), before anything is filled in.
`code-forge autopilot status` and `code-forge autopilot stop` print it. A new session after a lost
one reads the link there and continues in the same doc: one run never gets a second doc.

After each decision, block close or incident the orchestrator updates only the parts that changed.
If you edited a part, your edit stays: it re-reads that part and adds only the new rows.

**Without the Docs connector** (another harness), the same two parts are files in the run dir:
`autopilot-binnacle.md` and `autopilot-log.md`. The orchestrator creates them at start with
`code-forge autopilot binnacle --run <run> --markdown` and prints both paths. After that the CLI
rewrites both files after every delegate decision and at every `status` and `stop`, and those two
commands print both paths. You can also read them any time:

```bash
code-forge autopilot binnacle --run <run>              # the summary as Markdown, to stdout
code-forge autopilot log --run <run> --json            # the full log, as data
code-forge autopilot binnacle --run <run> --markdown   # write both files, print the binnacle path
```

**Settle before you ask.** Before a block close, the orchestrator runs
`code-forge block settle <id> --run <run>`. Settle re-reads the close blocker list each round and
runs every fix an orchestrator may run, in a fixed order: `block rebase --accept-landed` for a stale
base, claim the changed files under a folder the block owns, run the gates, wait for the reviews.
It never runs a verb only you may run (waive, reopen, `close --decide`, human-pass, ack-waivers). It
stops when no orchestrator blocker is left, when a fix fails, or when a round can change nothing
(at most 4 rounds), and writes one signed `block.settle` row. It exits 0 when no blocker is left,
3 when only your blockers are left, and 1 when a fix failed or an orchestrator blocker stays. Only
in the exit 3 case do **Actions only you can take**, `code-forge status` and
`code-forge autopilot status` show one line for the block with the exact command(s) to run, for
example `owner: block B3 needs you: code-forge block waive B3 F1 --run <run> --file src/a.ts --reason '<reason>'`.
While an orchestrator blocker is left, no such line is shown.

### Review it on return

1. Open the link (or the two files). Read **Status at a glance** first: the result, the spend, the
   window.
2. Read **Actions only you can take** and **Open questions**. These are waiting for you.
3. Read **Decisions**. Each row says what would reverse it. To overrule one, undo what it allowed.
4. Use the **Full log** when you want to check one event in detail.
5. Comment on any row of the doc. The orchestrator answers in that thread.

## Stop it

```bash
code-forge autopilot stop --run <run>
```

It ends the grant now, with one signed `autopilot.stop` row, and prints the link or the file
paths. The orchestrator adds the stop as the newest Timeline and Full log row, writes a final
Status at a glance, and leaves the doc as the record. Then it tells you in chat: the link, and
"review Open questions and Actions only you can take before work resumes". At `--until`
(or, for `--until-done`, when every planned block is closed or at the deadline) the grant expires
by itself; the next autopilot or block command writes one `autopilot.expire` row.

## Privacy

- The doc and the files are written from the ledger. Every text passes the same scrub as the error
  log: secret-shaped tokens are masked, your home folder shows as `~`, the project folder as
  `<project>`, and `op://` references as `op://<ref>`.
- The files in the run dir are readable only by you (mode 0600).
- The doc is private to you until you share it. The link must be a plain `https` URL: a link with
  credentials or secret-shaped text is refused.
- The delegate sees only a short packet: the question, the options, the scopes and a bounded
  context. Never a whole file, never a key.

See also: [concepts.md](concepts.md) (grant, delegate, binnacle, full log, scope, deny list,
approval) and [privacy.md](privacy.md).
