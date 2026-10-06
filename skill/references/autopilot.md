# Autopilot — the owner steps away; a delegate decides inside a grant; one live doc records it all

Autopilot lets the owner leave for a set time. A delegate (a fresh L2 or L3 session) answers some owner questions inside a time-boxed grant. Everything it does is a signed ledger row, and one live doc shows it. You start nothing on your own: autopilot exists only when the owner asks for it.

## §1 Start — only after the owner asks

1. The owner asks for autopilot and names the window, the delegate level and the scopes. Run `forge autopilot start --run <r> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>]`. The owner confirms it at the prompt. Never pass `--yes` unless the owner said so in chat.
2. **One doc per run.** First run `forge autopilot status --run <r>`: a link there means the doc exists (a lost session left it) — continue in it, never create a second doc for the same run. No link: create ONE Claude Docs doc titled `<project> autopilot run — <date>` with two tabs, born in one call (the exact steps: `adapters/claude-code.md` §4).
   - **Binnacle** (the main tab): its outline is `forge autopilot binnacle --run <r> --json` — the title and byline, then 7 sections: Status at a glance, Decisions, Blocks, Open questions, Actions only you can take, Incidents, Timeline. One pending block per section.
   - **Full log**: one table from `forge autopilot log --run <r> --json` — Time, Event, Block, File, Detail, Link — newest first. One pending block for it.
3. **Store the link first**, right after the birth (its ack gives the link): `forge autopilot binnacle --run <r> --link <url>`. From now on `forge autopilot status` and `forge autopilot stop` print it.
4. Open the doc for the owner, then fill one section per call, in reading order: the 7 Binnacle sections, then the Full log table. Never write the whole doc in one call.

## §2 During the window

- **Every owner-level question goes to the delegate first**: `forge autopilot ask --run <r> --scope <scope> --question <text> --options <words> [--block <id>] [--file <path>] [--finding <id>]`. Use the fixed option words, or the action will refuse the answer: a waiver `waive,fix` · an extra round `allow,deny` · a coder level the candidate levels, e.g. `L1,L2`.
- **Act only when it acted** (exit 0): run the action with `--decision <id>` from the answer — `forge autopilot waive|round|level … --decision <id>`. Exit 3 means the question goes to the owner: leave it open, it shows under Open questions. Exit 1 means the grant refused it: also the owner's.
- **A paused category** (`autopilot paused: … waiting for the owner`): wait. Never raise the cap, never route around it.
- **Keep the doc current — only what changed.** After every decision, block close or incident: re-read `forge autopilot binnacle --run <r> --json` and `forge autopilot log --run <r> --json`, then
  - append the new row to the Binnacle's Decisions table;
  - put the new row at the top of the Timeline (newest first);
  - update the Status at a glance cells that changed (and Blocks, Open questions, Actions or Incidents when they changed);
  - add the new rows to the Full log tab's table, at the top (it is newest first too).
  Never rewrite the whole doc and never replace a section that did not change.
- **A refused edit** (`guard_mismatch`, `find_none`) means the owner edited that section: re-read it, keep the owner's edit, and apply only the new row(s). Never force. Answer an owner's comment in its own thread.
- **A lost session:** the next one reads the link from `forge autopilot status --run <r>` and continues in the same doc.

## §3 Stop — at the owner's return or `until`

1. `forge autopilot stop --run <r>` when the owner is back (at `until` the grant expires by itself; `forge autopilot status --run <r>` then says so).
2. Add the stop (or expiry) event as the newest Timeline row and the newest Full log row, then write the final Status at a glance (the window's end, the result, the spend). Leave the doc as the record of the run; do not delete or move it.
3. Tell the owner in chat: the link, and "review Open questions and Actions only you can take before work resumes".

## §4 Without the Docs connector

Another harness, or the connector is missing: the two Markdown files in the run dir are the record. At start, run `forge autopilot binnacle --run <r> --markdown` once: it creates `autopilot-binnacle.md` and `autopilot-log.md`; print both paths for the owner. After that the CLI rewrites both files by itself, after every delegate decision and at every `forge autopilot status` and `forge autopilot stop`, and those two commands print both paths. At stop, tell the owner the two paths and the same review line as §3.

## §5 Never delegated — always the owner

These never go to the delegate, whatever the grant says (the CLI's fixed deny list refuses them too):

- plan approval (`plan.md` §4.1) and design approval;
- a budget raise (`code.md` §6, `review.md` §7);
- a threshold edit or any other limit or rule change — only the owner's own `forge autopilot approve` at a terminal;
- a critical waiver, a proof waiver, or closing a block without its reviews;
- merges and destructive actions.

When one of these comes up during the window: stop that line of work, add it to Actions only you can take, and wait.
