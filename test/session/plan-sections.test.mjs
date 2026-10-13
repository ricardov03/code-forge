import './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { REQUIRED_SECTIONS, findSection, requiredSections, splitHeading } = await import('../../src/session/plan-sections.mjs');
const { buildAuthorPacket } = await import('../../src/session/author.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const read = (/** @type {string} */ rel) => readFileSync(path.join(REPO, rel), 'utf8');

test('B36: the shared list holds exactly the two required headings', () => {
  assert.deepEqual(
    requiredSections().map((s) => [s.id, s.heading, s.label]),
    [
      ['unbackable', 'Acceptance clauses the facts sheet cannot back', '§0.x'],
      ['callerMap', 'Caller map', '§3'],
    ],
  );
});

test('B36: the Plan job packet states every required heading once, with its position, and names --rules and --plan', () => {
  const packet = buildAuthorPacket({ job: 'plan', brief: 'b' });
  assert.deepEqual(
    packet.split('\n').filter((l) => l.startsWith('- "')),
    [
      '- "§0.x Acceptance clauses the facts sheet cannot back" — every clause that cites a claim the sheet marks NOT-FOUND or UNVERIFIABLE, each with a tolerance naming the block; `none` when there is none.',
      '- "§3 Caller map" — mechanism → the block that ships it → the block that calls it; every block id appears.',
    ],
  );
  for (const s of requiredSections()) assert.equal(packet.split(s.heading).length - 1, 1, s.heading);
  assert.deepEqual([packet.includes('`--rules`'), packet.includes('--plan <plan>')], [true, true]);
  const harden = buildAuthorPacket({ job: 'harden', brief: 'b' });
  assert.equal(harden.includes('Caller map'), false);
});

test('B36: skill/templates/plan.md carries every required heading at its position', () => {
  const heads = read('skill/templates/plan.md')
    .split('\n')
    .filter((l) => /^#{2,3} /.test(l))
    .map((l) => ({ title: l.replace(/^#+\s+/, '') }));
  for (const spec of requiredSections()) {
    const hit = findSection(heads, spec);
    assert.deepEqual([hit?.exact, splitHeading(hit?.section.title ?? '').number], [true, spec.label.slice(1)], spec.heading);
  }
});

test('B36: splitHeading reads §0.x, 0.6 and 3. numbers and leaves an unnumbered heading alone', () => {
  assert.deepEqual(
    ['§0.x Acceptance clauses', '0.6 Unbackable', '3. Caller map', 'Caller map'].map(splitHeading),
    [
      { number: '0.x', text: 'Acceptance clauses' },
      { number: '0.6', text: 'Unbackable' },
      { number: '3', text: 'Caller map' },
      { number: null, text: 'Caller map' },
    ],
  );
});

/** @param {string} text @returns {string} the text with every whitespace run one space, lower case */
const flat = (text) => text.replace(/\s+/g, ' ').toLowerCase();

/** The base (1d2d062) brief's ticket-line rule: each phrase as that brief had it, its line there in the comment. */
const BRIEF_GONE = [
  'FAILED unless it lists the ticket ids', // 22, the report heading
  'then one line per changed file', // 37
  '`reviewed <path> ticket <ticket-id>`', // 39 and 47
  '**FAILED unless:** the report lists the `review-file` ticket id of every changed file', // 41
  'A report with no ticket ids, or one that misses a changed file, counts as FAILED whatever its sentinel says', // 41
  'no ticket ids', // 41
  '`===BLOCK <id> FAILED: no review-file ticket ids===`', // 41
  'no review-file ticket id', // 41
  'The final report follows the same rule: one', // 47
  'line per file changed in the round, or the round counts as FAILED', // 47
];
const BRIEF_NO_LINES =
  "The final report needs no `reviewed <path> ticket <id>` lines: `block close` prints one for every file the review gate accepts (approved, waived or answered), with the queue's ticket.";

test('B36 → B96: the coder brief template states the report rule for first attempts and fix rounds — the close prints the ticket lines', () => {
  const brief = read('skill/templates/coder-brief.md');
  // the base brief's rule is gone, in any case and any wrapping; other --report checks (sentinel, TESTS) may keep a FAILED form
  for (const gone of BRIEF_GONE) assert.equal(flat(brief).split(flat(gone)).length - 1, 0, gone);
  assert.equal(brief.split(BRIEF_NO_LINES).length - 1, 1);
  // outside that sentence nothing asks for a ticket line, whatever its placeholder (or path) is called
  const rest = brief.replace(BRIEF_NO_LINES, '');
  assert.deepEqual([rest.match(/reviewed[\s`*]+<[^>]+>[\s`*]+ticket/gi), rest.match(/\breviewed[\s`*]+\S+[\s`*]+ticket\b/gi)], [null, null]);
  assert.equal(brief.split("The round's final report follows the same rule as the first: it needs no ticket lines.").length - 1, 1);
  assert.equal(read('skill/references/code.md').split('5. **The close writes the ticket lines (B96)** (`templates/coder-brief.md`)').length - 1, 1);
});

test('B36: a §0.x heading with no distinctive word is not the unbackable section; "Unbackable" at §0.x is', () => {
  assert.equal(findSection([{ title: '0.1 Acceptance clauses' }], REQUIRED_SECTIONS.unbackable), null);
  // a plain facts-sheet heading at §0 is not the unbackable section either
  assert.equal(findSection([{ title: '0.1 Facts sheet' }], REQUIRED_SECTIONS.unbackable), null);
  assert.equal(findSection([{ title: '0.2 Caller map' }], REQUIRED_SECTIONS.unbackable), null);
  assert.deepEqual(findSection([{ title: '0.1 Acceptance clauses' }, { title: '0.6 Unbackable claims' }], REQUIRED_SECTIONS.unbackable), { section: { title: '0.6 Unbackable claims' }, exact: false });
});

test('B36: an exact match compares the heading text after the number, not a substring', () => {
  assert.equal(findSection([{ title: '5.2 Notes on the caller map' }], REQUIRED_SECTIONS.callerMap), null);
  assert.deepEqual(findSection([{ title: '5.2 Notes on the caller map' }, { title: '§7  caller MAP ' }], REQUIRED_SECTIONS.callerMap), { section: { title: '§7  caller MAP ' }, exact: true });
});
