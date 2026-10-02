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

test('B36: the coder brief template states the fail-closed report rule for first attempts and fix rounds', () => {
  const brief = read('skill/templates/coder-brief.md');
  assert.equal(brief.split('`reviewed <path> ticket <ticket-id>`').length - 1, 2);
  assert.equal(
    brief.includes('**FAILED unless:** the report lists the `review-file` ticket id of every changed file. A report with no ticket ids, or one that misses a changed file, counts as FAILED whatever its sentinel says'),
    true,
  );
  assert.equal(brief.includes('one `reviewed <path> ticket <ticket-id>` line per file changed in the round, or the round counts as FAILED.'), true);
  assert.equal(read('skill/references/code.md').includes('5. **FAILED unless** (`templates/coder-brief.md`)'), true);
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
