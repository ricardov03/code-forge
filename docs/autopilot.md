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
- `--delegate` is `L2` or `L3`. The delegate is a closed-book session and never Codex.
- `--allow` lists the scopes it may decide (below). `--deny` takes some away again.
- `--budget` sets a cap in USD per category (`coding`, `review`). `--stop-at` is the share of the
  cap at which that category pauses (default `0.9`).
- Without a terminal the start is refused, unless you pass `--yes` yourself.

Only one grant is active per run. `code-forge autopilot status --run <run>` shows it: time left,
scopes, the deny list, the spend per category, and where the log is.

## Scopes and the fixed deny list

| Scope | What the delegate may decide |
|---|---|
| `waive:warning` | waive a warning finding |
| `waive:nit` | waive a nit finding |
| `round:extra` | one fix round past the review cap for a file |
| `model:choose` | choose the coder level for a block |

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
the grant expires by itself; the next autopilot or block command writes one `autopilot.expire`
row.

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
