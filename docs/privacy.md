# Privacy: the error log and error reports

code-forge has no telemetry. When a verb fails it writes one line to a file on your machine, and
it only sends anything when you run `code-forge logs report`, read the full text, and say yes.

## The local error log

File: `~/.code-forge/logs/errors.jsonl` (folder mode 0700, file mode 0600). One JSON line per
failed verb (a non-zero exit or a crash). When it grows over 1 MB it keeps its newest half.

| Field | What it holds |
|---|---|
| `ts` | the time (UTC) |
| `version` | the code-forge version |
| `node` | the Node version |
| `platform`, `arch` | the OS name and CPU type (for example `darwin`, `arm64`) |
| `verb` | the command (`keys`, `init`…) |
| `sub` | the subcommand, only when it is one of the verb's own words (`set`, `install`…); anything else could be your value and is left out |
| `flags` | flag NAMES only (`--op`, `-y`); never a value, never a positional argument |
| `exit` | the exit code |
| `kind` | `usage`, `crash`, `error`, or a kind the verb reports (such as `op_timeout`) |
| `message` | the last text the verb wrote to stderr (a crash: its message), cleaned, at most 2 KB |
| `stack` | a crash's stack trace, cleaned, at most 4 KB; otherwise empty |
| `cleaned` | how many replacements each cleaning rule made (counts only) |
| `fp` | the fingerprint: 12 hex characters of a SHA-256 of the verb, subcommand, kind and the message with quoted text, `<…>` placeholders, paths, dates and times, long hex ids and numbers of 4+ digits removed (short numbers such as an HTTP status stay). The same error gives the same fingerprint, so reports can be grouped and duplicates found |

## The hint after a failure

After a failed verb is logged, one line on stderr says how to send it: `code-forge: this error was
saved to the local log. To send it to us: code-forge logs report`. It is shown only when stderr is
a terminal; never for a usage error (exit 2), never when an agent runs code-forge (`CLAUDECODE`,
`CLAUDE_CODE`, `CURSOR_AGENT` or `AI_AGENT` set), never when logging is off, and at most once per
24 hours for the same fingerprint. `hints.json`, next to the log, remembers when it was last shown
for each fingerprint (fingerprints and times only).

## The built-in cleaning rules

Every message and stack is first passed through code-forge's secret redaction, then these rules,
in this order. They run when a line is written, on the whole report, again after the AI check's
replacements, and again after you edit the text.

| Rule | Replaced by |
|---|---|
| `op://` references (1Password) | `op://<ref>` |
| your project folder (the folder you ran code-forge in, and its real path) | `<project>` |
| your home folder (and its real path) | `~` |
| email addresses | `<email>` |
| your project slug (`project.slug` in `.code-forge.yml`), as a whole word | `<slug>` |
| 1Password item IDs (26 lowercase letters and digits) | `<item-id>` |

A path is only replaced at a path boundary: `/Users/bob` never changes `/Users/bobby`.

## What `logs report` does, in order

1. **Version check.** It runs `npm view @codedology/code-forge version` (10 s at most). This asks
   npm for the newest version; it sends only the package name. If yours is older, it says so and
   asks "Report anyway?" (default no). Without a terminal it stops (exit 2) unless you pass
   `--allow-old`; `--yes` does not skip this question in a terminal. If npm cannot be asked (no
   npm, a timeout, an error, an answer that is not a version) it prints a note and goes on; the
   report then says "latest on npm: unknown".
2. **Built-in cleaning** of the whole report (the rules above).
3. **Size cap.** The report is cut so the text sent to the AI check stays within 16 KB; the cut
   text is also exactly what you would send.
4. **Secret check, before the AI sees anything.** If anything still looks like a key or token
   (API keys, GitHub and Slack tokens, AWS keys, private keys, JWTs, long random strings) the report
   stops here; the match is never printed and the AI check never runs.
5. **AI check** (skipped with `--no-ai`, see below).
6. **Built-in cleaning again**, over the text with the AI's replacements.
7. **Secret check again.**
8. It prints what is shared and never shared, the cleaning counts, and the full text.
9. **You choose:** Send, Edit in my editor, or Cancel. An edit is cleaned again (built-in rules)
   and checked for secrets again, then shown, and you choose once more (Send or Cancel).
10. **The extra yes.** When the AI check did not run — skipped with `--no-ai`, or unavailable —
    Cancel is the default and Send needs one more yes ("Send without the AI check?", default no).
    `--yes` alone never skips it: when the AI check was unavailable, `--yes` in a terminal still
    asks this question, and without a terminal the report is not sent. `--no-ai --yes` together
    are the explicit extra yes: you chose to skip the AI check and to send, so it sends with only
    the built-in cleaning.
11. **Sending.** With the GitHub CLI (`gh`) signed in, it first runs `gh issue list --search
    "<fingerprint> in:body"`, which sends only the fingerprint, to find an earlier issue; if one
    exists you can add a "happened again" comment (versions and how many times the error is in
    your log), create a new issue anyway, or cancel. If the search fails it says so and creates a
    new issue. The issue gets the labels `error-report` and `kind:<kind>`; when GitHub refuses a
    label (it does not exist in the repository), the issue is created once more without labels.
    Without `gh`, nothing is sent by code-forge:
    it prints a search link for the fingerprint and a link to a prefilled issue form that holds the
    report text — you open it yourself and submit it on GitHub.

The npm and `gh issue list` calls both happen before anything is sent, and neither carries any
report text.

## The AI check

It catches what fixed rules cannot: a person's or company's name, a private project, a host name,
an internal URL, an account, a path, or anything that looks like a secret.

- What it sees: only the report text after the built-in cleaning and the first secret check (at
  most 16 KB, prompt included). Never the raw log, your files or your code.
- How it runs: the same closed-book way code-forge runs reviewers — the model CLI in print mode,
  in a new, empty temporary folder, the text on stdin, with no tools. It uses level L1 of your
  project's setup (`code-forge resolve L1` shows it); outside a project, or when your project's L1
  does not resolve, the shipped default L1 model.
- What it does: it only returns a list of exact pieces of text and a kind for each. It never
  rewrites the report. code-forge replaces every listed piece with `<kind>` (for example `<host>`),
  longest first and never inside an existing `<…>` placeholder, ignores any piece that is not in
  the text, and then runs the built-in cleaning again.
- What you see: the number of pieces per kind ("AI pass: 1 company, 1 host") — never the pieces
  themselves — and then the whole final text.
- When it cannot run (no model CLI, not logged in, a timeout, an error, an answer that is not
  JSON, an unreadable project config), you are told so plainly and the extra yes above applies.

## What a report shares

The issue is public on GitHub. It holds the versions (code-forge, Node, OS, the newest code-forge
on npm), and for each selected error the time, command, flag names, exit code, kind, fingerprint,
message and stack after cleaning, plus your note. Labels: `error-report` and one `kind:<kind>` per
kind in the report (at most 3).

## Turn the log off

Set `CODE_FORGE_NO_ERROR_LOG=1` in your environment. Nothing is written and no hint is shown.

## Delete the log

Run `code-forge logs clear` (it asks first; `--yes` skips the question), or delete the folder
`~/.code-forge/logs/`. `code-forge logs path` prints where the log is.
