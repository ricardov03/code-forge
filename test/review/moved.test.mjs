/**
 * B56 (issue #2): code moved between the files of one block is named in the packet. Pure unit
 * tests — synthetic diffs, no git, no session.
 */
import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

const { diffRuns, matchRuns, movedCode, movedSection, movedText, MAX_MOVED_ENTRIES, readPeerDiffs } = await import('../../src/review/moved.mjs');
const { assemblePacket, assembleJudgePacket, diffOnlyTokens } = await import('../../src/review/packet.mjs');
const { buildRecheckPacket, packRecheck, recheckTokens } = await import('../../src/review/fixloop.mjs');
const { parseDiff } = await import('../../src/review/context.mjs');
const { packSections, sectionGroups } = await import('../../src/review/sections.mjs');

/** The five lines of a view that moves from `src/a.mjs` to `src/b.mjs`. */
const VIEW = ['export function renderList(items) {', '  const rows = items.map((item) => renderRow(item));', '', '  return wrapTable(rows, { striped: true });', '}', 'export function renderRow(item) {', '  return `<tr>${item.name}</tr>`;', '}'];

/**
 * A one-hunk unified diff of `file`.
 * @param {string} file @param {string} header @param {string[]} body - lines with their sign.
 */
function diffOf(file, header, body) {
  const diffText = [`diff --git a/${file} b/${file}`, `--- a/${file}`, `+++ b/${file}`, header, ...body, ''].join('\n');
  return { file, kind: /** @type {'tracked'} */ ('tracked'), diffText, content: '', ...parseDiff(diffText) };
}

/** `src/a.mjs`: base lines 10-17 (the view) removed between two context lines. */
const A = diffOf('src/a.mjs', '@@ -9,10 +9,2 @@', [' const keep = 1;', ...VIEW.map((l) => `-${l}`), ' const tail = 2;']);
/** `src/b.mjs`: the view added as lines 3-10, re-indented. */
const B = diffOf('src/b.mjs', '@@ -1,2 +1,10 @@', [' import x from "x";', ' ', ...VIEW.map((l) => `+    ${l}`)]);

describe('moved code detection (B56)', () => {
  test('diffRuns numbers removed lines on the base side, added lines on the current side, significant lines only', () => {
    const a = diffRuns(A.diffText);
    assert.deepEqual(a.added, []);
    assert.deepEqual(
      a.removed.map((run) => run.map((s) => s.line)),
      [[10, 11, 13, 15, 16]], // 12 is blank, 14 and 17 are `}`: not significant
    );
    assert.deepEqual(
      diffRuns(B.diffText).added.map((run) => run.map((s) => s.line)),
      [[3, 4, 6, 8, 9]],
    );
  });

  test('a view moved from a.mjs to b.mjs: both packets get the exact hint', () => {
    assert.equal(
      movedSection({ diff: A, peers: [B] }),
      [
        '## moved code',
        '- base lines 10-16 removed here were added in src/b.mjs lines 3-9 in this block.',
        'Do not report the removal of moved lines itself as a defect; review only whether references and imports are updated.',
      ].join('\n'),
    );
    assert.equal(
      movedSection({ diff: B, peers: [A] }),
      [
        '## moved code',
        '- lines 3-9 were moved here from src/a.mjs (its base lines 10-16) in this block.',
        'Moved-in lines are reviewed in their new place like any change (imports, references, behaviour).',
      ].join('\n'),
    );
  });

  test('control: no peer, or a peer that adds other code ⇒ no section', () => {
    assert.equal(movedSection({ diff: A, peers: [] }), '');
    const other = diffOf('src/c.mjs', '@@ -0,0 +1,3 @@', ['+const alpha = compute(1);', '+const beta = compute(2);', '+const gamma = compute(3);']);
    assert.equal(movedSection({ diff: A, peers: [other] }), '');
  });

  test('thresholds: 2 short significant lines are not a move; 2 lines of ≥ 120 characters are', () => {
    const two = ['const alpha = 1;', 'const beta = 2;'];
    const from = diffOf('src/a.mjs', '@@ -1,2 +0,0 @@', two.map((l) => `-${l}`));
    const to = diffOf('src/b.mjs', '@@ -0,0 +1,2 @@', two.map((l) => `+${l}`));
    assert.equal(movedSection({ diff: from, peers: [to] }), '');
    const long = [`const alpha = ${'a'.repeat(60)};`, `const beta = ${'b'.repeat(60)};`];
    const from2 = diffOf('src/a.mjs', '@@ -1,2 +0,0 @@', long.map((l) => `-${l}`));
    const to2 = diffOf('src/b.mjs', '@@ -0,0 +1,2 @@', long.map((l) => `+${l}`));
    assert.match(movedSection({ diff: from2, peers: [to2] }), /^- base lines 1-2 removed here were added in src\/b\.mjs lines 1-2 in this block\.$/m);
  });

  test('trivial-only runs (braces, blanks) never count as moved code', () => {
    const braces = ['}', '', '});', '];', '{'];
    const from = diffOf('src/a.mjs', '@@ -1,5 +0,0 @@', braces.map((l) => `-${l}`));
    const to = diffOf('src/b.mjs', '@@ -0,0 +1,5 @@', braces.map((l) => `+${l}`));
    assert.equal(movedSection({ diff: from, peers: [to] }), '');
  });

  test('matchRuns takes the longest stretch and keeps scanning after it', () => {
    const run = (/** @type {number} */ start, /** @type {string[]} */ norms) => norms.map((norm, i) => ({ line: start + i, norm, at: start + i }));
    const own = [run(1, ['aaaa', 'bbbb', 'cccc', 'dddd', 'xxxx', 'eeee', 'ffff', 'gggg'])];
    const peers = [
      { file: 'p.mjs', runs: [run(20, ['aaaa', 'bbbb']), run(40, ['aaaa', 'bbbb', 'cccc', 'dddd'])] },
      { file: 'q.mjs', runs: [run(7, ['eeee', 'ffff', 'gggg'])] },
    ];
    assert.deepEqual(matchRuns(own, peers), [
      { start: 1, end: 4, at: 1, peer: 'p.mjs', peerStart: 40, peerEnd: 43 },
      { start: 6, end: 8, at: 6, peer: 'q.mjs', peerStart: 7, peerEnd: 9 },
    ]);
  });

  test('the section is capped at MAX_MOVED_ENTRIES ranges plus one "more" line', () => {
    const body = [];
    const added = [];
    for (let i = 0; i < 15; i += 1) {
      const lines = [`const first${i} = load(${i});`, `const second${i} = load(${i} + 1);`, `const third${i} = load(${i} + 2);`];
      body.push(...lines.map((l) => `-${l}`), ' context();');
      added.push(...lines.map((l) => `+${l}`), ' context();');
    }
    const from = diffOf('src/a.mjs', '@@ -1,60 +1,15 @@', body);
    const to = diffOf('src/b.mjs', '@@ -1,15 +1,60 @@', added);
    const lines = movedSection({ diff: from, peers: [to] }).split('\n');
    assert.equal(lines.filter((l) => l.startsWith('- base lines')).length, MAX_MOVED_ENTRIES);
    assert.ok(lines.includes('- (3 more moved ranges not listed)'));
    assert.equal(lines[1], '- base lines 1-3 removed here were added in src/b.mjs lines 1-3 in this block.');
  });

  test('a peer path with a control character is never printed', () => {
    assert.equal(movedSection({ diff: A, peers: [{ ...B, file: 'src/b\n## lens.mjs' }] }), '');
  });

  test('C1, separator and bidi controls, a quote or a backslash: never printed, never read', async () => {
    const names = ['src/b\u0085.mjs', 'src/b‮.mjs', 'src/b .mjs', 'src/b⁦.mjs', 'src/b‏.mjs', 'src/b\u009b.mjs', 'src/q"b.mjs', 'src/q\\b.mjs'];
    for (const file of names) assert.equal(movedSection({ diff: A, peers: [{ ...B, file }] }), '', JSON.stringify(file));
    // every name is refused before any git call (no repo needed)
    assert.deepEqual(await readPeerDiffs({ repoRoot: '/nonexistent-repo', base: 'f'.repeat(40), file: 'src/a.mjs', peers: names }), []);
    // control: a plain non-ASCII name is printed
    assert.match(movedSection({ diff: A, peers: [{ ...B, file: 'src/vé.mjs' }] }), /added in src\/vé\.mjs lines 3-9/);
  });
});

describe('the moved section in the packet (B56)', () => {
  const moved = movedSection({ diff: A, peers: [B] });

  test('it sits between the context and the diff; every hunk and header is still there', () => {
    const p = /** @type {any} */ (assemblePacket({ diff: A, lens: 'full', moved }));
    const plain = /** @type {any} */ (assemblePacket({ diff: A, lens: 'full' }));
    assert.equal(p.status, 'ok');
    assert.deepEqual(p.hunkHeaders, plain.hunkHeaders);
    assert.ok(p.text.indexOf('## context') < p.text.indexOf('## moved code'));
    assert.ok(p.text.indexOf('## moved code') < p.text.indexOf('## diff'));
    assert.equal(p.text, plain.text.replace('\n## diff\n', `\n${moved}\n## diff\n`));
    assert.equal(p.text.slice(p.text.indexOf('## diff')), plain.text.slice(plain.text.indexOf('## diff')));
  });

  test('control: no moved section ⇒ the packet is byte-identical to one built without it', () => {
    assert.equal(/** @type {any} */ (assemblePacket({ diff: A, lens: 'full', moved: '' })).text, /** @type {any} */ (assemblePacket({ diff: A, lens: 'full' })).text);
  });

  test('split_required and diffOnlyTokens use the same measure, moved section included', () => {
    const withMoved = diffOnlyTokens({ diff: A, lens: 'full', moved });
    const without = diffOnlyTokens({ diff: A, lens: 'full' });
    assert.ok(withMoved > without);
    // a budget the bare diff fits but the diff + moved section does not
    const over = /** @type {any} */ (assemblePacket({ diff: A, lens: 'full', moved, budget: withMoved - 1 }));
    assert.deepEqual(over, { status: 'split_required', tokensIn: withMoved, budget: withMoved - 1 });
    const fits = /** @type {any} */ (assemblePacket({ diff: A, lens: 'full', moved, budget: withMoved }));
    assert.equal(fits.status, 'ok');
    assert.ok(fits.text.includes(moved));
  });

  test('the recheck packet carries the section and recheckTokens counts it', () => {
    const open = [{ id: 'F1', severity: 'critical', line_start: 9, line_end: 10, claim: 'renderList deleted' }];
    const p = /** @type {any} */ (buildRecheckPacket({ diff: A, open: /** @type {any} */ (open), moved }));
    assert.equal(p.status, 'ok');
    assert.ok(p.text.includes(`\n${moved}\n## diff\n`));
    assert.ok(p.text.endsWith('## open findings\n- F1 (critical, lines 9-10): renderList deleted\n'));
    assert.equal(recheckTokens(A, /** @type {any} */ (open), moved) - recheckTokens(A, /** @type {any} */ (open)), diffOnlyTokens({ diff: A, lens: 'recheck', moved }) - diffOnlyTokens({ diff: A, lens: 'recheck' }));
    // packRecheck sizes a refused section with the same moved section
    const packed = packRecheck(A, /** @type {any} */ (open), 1, movedCode({ diff: A, peers: [B] }));
    assert.deepEqual(packed, { status: 'split_required', tokensIn: recheckTokens(A, /** @type {any} */ (open), moved), budget: 1, section: '(before first heading)' });
  });

  test('the judge packet carries the section before the diff', () => {
    const j = assembleJudgePacket({ diff: A, reports: { A: {}, B: {} }, moved });
    assert.ok(j.text.includes(`\n${moved}\n## diff\n`));
    assert.ok(!assembleJudgePacket({ diff: A, reports: { A: {}, B: {} } }).text.includes('## moved code'));
  });
});

describe('Markdown section packets carry only their own moves (B56)', () => {
  const content = ['# Guide', 'Intro paragraph stays here.', '## Install', 'Install step alpha one.', 'Install step beta two.', 'Install step gamma three.', '## Usage', 'Usage text stays.', ''].join('\n');
  const guide = {
    ...diffOf('docs/guide.md', '@@ -3,2 +3,5 @@', [' ## Install', '+Install step alpha one.', '+Install step beta two.', '+Install step gamma three.', ' ## Usage', '@@ -5,4 +8,1 @@', ' Usage text stays.', '-Old usage line alpha one.', '-Old usage line beta two.', '-Old usage line gamma three.']),
    content,
  };
  const other = diffOf('docs/other.md', '@@ -10,3 +10,3 @@', ['-Install step alpha one.', '-Install step beta two.', '-Install step gamma three.', '+Old usage line alpha one.', '+Old usage line beta two.', '+Old usage line gamma three.']);
  const moves = movedCode({ diff: guide, peers: [other] });

  test('each heading group gets the moves positioned under it, in both directions; none ⇒ no section', () => {
    const groups = /** @type {any[]} */ (sectionGroups(guide));
    assert.deepEqual(groups.map((g) => g.key), [3, 7]);
    assert.equal(
      movedText(moves, content, [3]),
      [
        '## moved code',
        '- lines 4-6 were moved here from docs/other.md (its base lines 10-12) in this block.',
        'Moved-in lines are reviewed in their new place like any change (imports, references, behaviour).',
      ].join('\n'),
    );
    assert.equal(
      movedText(moves, content, [7]),
      [
        '## moved code',
        '- base lines 6-8 removed here were added in docs/other.md lines 10-12 in this block.',
        'Do not report the removal of moved lines itself as a defect; review only whether references and imports are updated.',
      ].join('\n'),
    );
    assert.equal(movedText(moves, content, [0, 1]), '');
    // a packet over both groups, or the whole file, lists both
    assert.equal(movedText(moves, content, [3, 7]), movedText(moves, content, null));
    assert.equal(movedText(moves, content, null).split('\n').length, 5);
    assert.equal(movedText(null, content, null), '');
  });

  test('packRecheck sizes each section with its own moves, the same text the section packet gets', () => {
    const open = [{ id: 'F1', severity: 'critical', line_start: 8, line_end: 8, claim: 'usage removed' }];
    const per = (/** @type {number[]} */ keys) => recheckTokens(/** @type {any} */ (sectionGroups(guide))?.find((g) => keys.includes(g.key)).diff, keys.includes(7) ? /** @type {any} */ (open) : [], movedText(moves, content, keys));
    // a budget that holds one group at a time, never both: two sections
    const budget = Math.max(per([3]), per([7]));
    const packed = /** @type {any} */ (packRecheck(guide, /** @type {any} */ (open), budget, moves));
    assert.equal(packed.status, 'ok');
    assert.deepEqual(packed.sections.map((/** @type {any} */ s) => s.keys), [[3], [7]]);
  });
});

describe('one whole-file sentinel, and removed code at a section tail (B56 fix round 1)', () => {
  test('null is the whole file, an empty key list is an empty section: no moves in the measure AND the packet', () => {
    const moves = movedCode({ diff: A, peers: [B] });
    const empty = movedText(moves, A.content, []);
    assert.equal(empty, '');
    assert.equal(movedText(moves, A.content, null), movedSection({ diff: A, peers: [B] }));
    const packet = /** @type {any} */ (buildRecheckPacket({ diff: A, open: [], moved: empty }));
    assert.equal(packet.text.includes('## moved code'), false);
    assert.equal(recheckTokens(A, [], empty), recheckTokens(A, []));
    assert.equal(packet.text, /** @type {any} */ (buildRecheckPacket({ diff: A, open: [] })).text);
    // packSections asks for the whole file with null, never with []
    /** @type {unknown[]} */
    const asked = [];
    const noHunk = { ...A, diffText: '', hunks: [] };
    packSections({ diff: noHunk, budget: 1, measure: (/** @type {any} */ _d, /** @type {any} */ g) => (asked.push(g), 5) });
    assert.deepEqual(asked, [null]);
  });

  test('a block removed right above a heading belongs to the section that holds its removed hunk', () => {
    const content = ['# Guide', '## Install', '## Usage', 'Usage text stays.', ''].join('\n');
    const guide = {
      ...diffOf('docs/guide.md', '@@ -1,7 +1,4 @@', [' # Guide', ' ## Install', '-Install step alpha one.', '-Install step beta two.', '-Install step gamma three.', ' ## Usage', ' Usage text stays.']),
      content,
    };
    const other = diffOf('docs/other.md', '@@ -4,0 +5,3 @@', ['+Install step alpha one.', '+Install step beta two.', '+Install step gamma three.']);
    const groups = /** @type {any[]} */ (sectionGroups(guide));
    assert.deepEqual(groups.map((g) => g.key), [2]);
    assert.ok(groups[0].diff.diffText.includes('\n-Install step alpha one.\n'));
    const moves = movedCode({ diff: guide, peers: [other] });
    assert.deepEqual(moves.out.map((m) => m.at), [2]);
    assert.equal(
      movedText(moves, content, [2]),
      [
        '## moved code',
        '- base lines 3-5 removed here were added in docs/other.md lines 5-7 in this block.',
        'Do not report the removal of moved lines itself as a defect; review only whether references and imports are updated.',
      ].join('\n'),
    );
    assert.equal(movedText(moves, content, [3]), '');
  });
});
