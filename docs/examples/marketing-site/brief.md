# Brief — the code-forge marketing site

> Copy this file to `plans/site.md` in a **new, separate repository** (for example
> `code-forge-site`), never inside the code-forge package repository. The tutorial
> [`docs/tutorial-first-test.md`](../../tutorial-first-test.md) walks through it.

## Goal

A small static site that explains code-forge to a developer in two minutes and gives them the one
install command. It is the first real project code-forge builds for itself, so it must stay small
enough to finish in two or three blocks.

## Audience

Developers and small teams who already use coding agents (Claude Code, Codex CLI, Grok CLI) and
want lower cost and real evidence that the work is right.

## Stack

- Plain HTML and CSS in `./site/`. No build step, no framework, no JavaScript required to read
  the page, no external fonts, scripts or trackers.
- Tests with Node's built-in runner (`node:test`, `node:assert/strict`) in `./test/`, run by
  `npm test` (`"test": "node --test"` in `./package.json`). No dependencies.
- Node 22 or newer.

The repository already has `./package.json` and one commit before the first block. Everything
under `./site/` and the test files below are new.

## Content

Tone: plain, short sentences, active voice. No hype, no invented numbers.

1. **Hero** (`site/index.html`)
   - Headline: "Your coding agents, organised like an engineering team."
   - Sub-line: "Cheap models for easy work. Strong models for hard work. Tools for facts. A fast
     decision model in between."
   - The install commands, each in a `<code>` element: `npm install -g @codedology/code-forge`, then
     `code-forge init` in the project folder.
   - A link to the how-it-works page and to the GitHub repository.
2. **The problem** (on `site/index.html`): the top model renames buttons; tests pass for the
   wrong reason; review comes too late; workflows name models and break; one harness only.
3. **How it works** (`site/how-it-works.html`): five steps in this order, as an ordered list
   `<ol id="steps">`:
   1. Facts — a small model checks every claim in your brief against the repository.
   2. Plan and harden — a strong model splits the work into blocks and asks the hard questions.
   3. Code — one coder per block, at the cheapest level that can do it.
   4. Review per file — a fresh reviewer checks each finished file; at most four fix rounds.
   5. Proof and close — the gate re-measures everything before the block is done.
4. **Cost control** (on `site/how-it-works.html`): levels L0–L3 instead of model names; L2 is the
   highest level a coder runs at; L3 only plans, judges, and writes one small patch when a block is
   stuck; the ledger records real tokens per block. Say plainly that no saving percentage is
   published yet.
5. **Honest limits** (on `site/how-it-works.html`): signed ledger rows protect against accidents,
   not against a process running as your own user; Gemini CLI, Cursor and Copilot get the skill
   but do not run coders in this version; no mutation testing.
6. **Call to action** on both pages: the install commands and a link to the documentation in the
   GitHub repository.

## Acceptance clauses

Each clause names the test that proves it. Test names are exact.

| # | Clause | Test |
|---|---|---|
| C1 | `site/index.html` contains the scoped install command `npm install -g @codedology/code-forge` inside a `<code>` element, and `code-forge init` inside another `<code>` element | `test/hero.test.mjs::the hero shows the scoped install command` |
| C2 | The HTML pages under `site/` are exactly `how-it-works.html` and `index.html`, each with non-empty visible text, and no page contains an unscoped install: neither `npm install -g code-forge` nor `npx code-forge init` (the unscoped npm name is a different, unrelated package) | `test/links.test.mjs::no page shows the unscoped install command` |
| C3 | The HTML pages under `site/` are exactly `how-it-works.html` and `index.html`, each with non-empty visible text, and every internal `href` and `src` in every page resolves to a file under `site/`, and every `#fragment` resolves to an `id` in the target page | `test/links.test.mjs::every internal link resolves` |
| C4 | `site/how-it-works.html` has an `<ol id="steps">` with exactly five `<li>` items whose text starts, in order, with Facts, Plan and harden, Code, Review per file, Proof and close | `test/how-it-works.test.mjs::the five steps appear in order` |
| C5 | The HTML pages under `site/` are exactly `how-it-works.html` and `index.html`, each with non-empty visible text, and no page makes a percentage saving claim: no `%` followed within 60 characters by `saving`, `savings` or `cheaper` | `test/how-it-works.test.mjs::no page claims a percentage saving` |

### How to write the tests (so red→green proof works)

- **Every site-wide test (C2, C3, C5) first asserts the exact page set and non-empty text.** List
  `site/*.html`, sort the names, and `assert.deepStrictEqual` them with
  `['how-it-works.html', 'index.html']`; then assert each page has non-empty text once tags are
  stripped. Only then run the clause's own check. Without this, "no page contains X" and "every
  link resolves" pass on an empty `site/` and prove nothing.
- Read pages with a small helper that returns `''` when the file does not exist yet, then assert
  on the text. A missing page must fail with an **assertion**, never with an `ENOENT` crash; a
  crash counts as `RED_INVALID` and proves nothing.
- Count exactly: "exactly five `<li>`", "exactly one match", never "at least one" when the clause
  says a number.
- Parse HTML with plain regular expressions; the pages are small and hand-written. No parser
  dependency.

## Suggested blocks

| Block | Title | Owned files | Clauses | Level | Depends on |
|---|---|---|---|---|---|
| B1 | Hero section and install command | `site/index.html`, `site/styles.css`, `test/hero.test.mjs` | C1 | L1 | — |
| B2 | How-it-works page: five steps, cost control, limits | `site/how-it-works.html`, `test/how-it-works.test.mjs` | C4, C5 | L1 | B1 (shares `styles.css`) |
| B3 | Site-wide checks: links and the unscoped command | `test/links.test.mjs` | C2, C3 | L0 | B1, B2 |

B1 is the serialization point: it owns the stylesheet and the navigation links. Because every
site-wide test first checks the exact page set and non-empty text, each one fails on an empty or
incomplete `site/`. For B2, the default `revert` mechanism removes its new page for the red run,
so the C5 test fails on the page-set assertion: a valid red. B3 owns tests only, so `revert` has no
source file to put back and its tests stay green at the base; prove them with
`--mechanism assertion-deletion`. Every file here is light tier, so red→green is optional at the
gate, but run it: it is what shows the tests can fail.

## Out of scope

- Hosting, a domain, analytics, a blog, a contact form.
- Any JavaScript that changes the content.
- Any number about cost savings.
