// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { answerFor, cfgFor, freshDir, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { isMarkdownFile, markdownHeadings, packSections, sectionGroups, sectionOf, sectionFindings } = await import('../../src/review/sections.mjs');
const { parseDiff } = await import('../../src/review/context.mjs');
const { diffOnlyTokens, readFileDiff, DEFAULT_BUDGETS } = await import('../../src/review/packet.mjs');
const { reviewFile } = await import('../../src/review/engine.mjs');
const { fixHunkDiff, mergePacketInfo, newFileState, packRecheck, recheckTokens, runRound, sum } = await import('../../src/review/fixloop.mjs');

/**
 * A pure FileDiff from a diff body (the hunks) and the current content.
 * @param {string} body @param {string} content
 */
const diffOf = (body, content) => {
  const diffText = `--- a/doc.md\n+++ b/doc.md\n${body}`;
  return /** @type {any} */ ({ file: 'doc.md', kind: 'tracked', diffText, content, ...parseDiff(diffText) });
};

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
}

/**
 * A stub session spawner: echoes the packet's hunks, adds `extra(text)`, records the packets.
 * @param {(text: string) => Record<string, any>} [extra]
 */
function stubSpawn(extra = () => ({})) {
  /** @type {string[]} */
  const packets = [];
  const spawn = async (/** @type {{promptPath: string}} */ o) => {
    const text = readFileSync(o.promptPath, 'utf8');
    packets.push(text);
    return { status: 'ok', exit_code: 0, answer: answerFor(hunksOf(text), extra(text)), usage: { tokens_in: 900, tokens_out: 300 } };
  };
  return { spawn, packets };
}

/** @param {string} id @param {number} line @param {'critical' | 'warning' | 'nit'} [severity] */
const finding = (id, line, severity = 'warning') => ({ id, file: 'docs/brief.md', line_start: line, line_end: line, severity, category: 'docs', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

/**
 * 125 lines: `# Title`, `intro`, then `## Alpha`, `## Bravo`, `## Delta` with 40 lines each;
 * `tag` goes at the end of every line of the sections named in `only`.
 * @param {string} [tag] @param {string[]} [only]
 */
function brief(tag = '', only = ['Alpha', 'Bravo', 'Delta']) {
  let out = '# Title\nintro\n';
  for (const name of ['Alpha', 'Bravo', 'Delta']) {
    out += `## ${name}\n`;
    for (let i = 1; i <= 40; i += 1) out += `${name} line ${String(i).padStart(2, '0')}: the brief says what this part does${only.includes(name) ? tag : ''}.\n`;
  }
  return out;
}

describe('Markdown sections (B54): the pure split', () => {
  test('isMarkdownFile: .md and .markdown (any case) only', () => {
    assert.deepEqual(['a.md', 'docs/B.MD', 'x.markdown', 'a.mdx', 'md', 'a.txt', 'a.md.bak'].map(isMarkdownFile), [true, true, true, false, false, false, false]);
  });

  test('markdownHeadings: ATX # to ######, never inside ``` or ~~~ fences; 4-space indents, #tags and 7 #s are not headings', () => {
    const content = ['# One', 'text', '```js', '# in code', '```', '~~~', '## in tilde', '````', '~~~', '## Two', '    # indented code', '#tag', '####### seven', '###### Six ', '#', '``` not ` a fence', '### Three'].join('\n');
    assert.deepEqual([...markdownHeadings(content)], [[1, '# One'], [10, '## Two'], [14, '###### Six'], [15, '#'], [17, '### Three']]);
  });

  test('a new file (ONE hunk) is cut at every heading into valid hunks with real line numbers', () => {
    const content = 'intro\n# A\na1\n```\n# not\n```\n## B\nb1\nb2\n';
    const diff = diffOf(`@@ -0,0 +1,9 @@\n${content.trimEnd().split('\n').map((l) => `+${l}`).join('\n')}\n`, content);
    const groups = sectionGroups(diff);
    assert.deepEqual(groups.map((g) => [g.key, g.heading, g.diff.hunks.map((h) => h.header), g.diff.plusCount]), [
      [0, '(before first heading)', ['@@ -0,0 +1,1 @@'], 1],
      [2, '# A', ['@@ -0,0 +2,5 @@'], 5],
      [7, '## B', ['@@ -0,0 +7,3 @@'], 3],
    ]);
    assert.equal(groups[1].diff.diffText, '--- a/doc.md\n+++ b/doc.md\n@@ -0,0 +2,5 @@\n+# A\n+a1\n+```\n+# not\n+```\n');
  });

  test('a changed file: a hunk that crosses a heading is cut there, with both sides counted', () => {
    const content = 'x\n# A\na1\na2\na3\na4 changed\na5\na6\n# B\nb1 changed\n';
    const diff = diffOf('@@ -3,8 +3,8 @@\n a1\n a2\n a3\n-a4\n+a4 changed\n a5\n a6\n # B\n-b1\n+b1 changed\n', content);
    assert.deepEqual(sectionGroups(diff).map((g) => [g.heading, g.diff.hunks.map((h) => h.header), g.diff.plusCount, g.diff.minusCount]), [
      ['# A', ['@@ -3,6 +3,6 @@'], 1, 1],
      ['# B', ['@@ -9,2 +9,2 @@'], 1, 1],
    ]);
  });

  test('a piece with context only joins its neighbour: the hunk keeps its own header', () => {
    const lead = diffOf('@@ -1,2 +1,3 @@\n p\n # H\n+new\n', 'p\n# H\nnew\n');
    const tail = diffOf('@@ -1,1 +1,3 @@\n # A\n+b\n # H\n', '# A\nb\n# H\n');
    assert.deepEqual([...sectionGroups(lead), ...sectionGroups(tail)].map((g) => [g.heading, g.diff.hunks.map((h) => h.header)]), [
      ['# H', ['@@ -1,2 +1,3 @@']],
      ['# A', ['@@ -1,1 +1,3 @@']],
    ]);
  });

  test('packSections: consecutive groups share a packet while it fits; one group over the budget alone ⇒ split_required with its heading', () => {
    const content = 'intro\n# A\na1\n## B\nb1\n';
    const diff = diffOf(`@@ -0,0 +1,5 @@\n${content.trimEnd().split('\n').map((l) => `+${l}`).join('\n')}\n`, content);
    const measure = (/** @type {any} */ d) => 10 * d.hunks.length;
    const packed = /** @type {any} */ (packSections({ diff, budget: 20, measure }));
    assert.deepEqual(packed.sections.map((/** @type {any} */ s) => [s.index, s.headings, s.diff.hunks.map((/** @type {any} */ h) => h.header)]), [
      [1, ['(before first heading)', '# A'], ['@@ -0,0 +1,1 @@', '@@ -0,0 +2,2 @@']],
      [2, ['## B'], ['@@ -0,0 +4,2 @@']],
    ]);
    assert.deepEqual(packSections({ diff, budget: 9, measure }), { status: 'split_required', tokensIn: 10, budget: 9, section: '(before first heading)' });
  });

  test('sectionOf: the section spanning the line, else the nearest by line (ties to the earlier), -1 only with no section; sectionFindings prefixes ids', () => {
    const content = 'intro\n# A\na1\n## B\nb1\n';
    const diff = diffOf(`@@ -0,0 +1,5 @@\n${content.trimEnd().split('\n').map((l) => `+${l}`).join('\n')}\n`, content);
    const { sections } = /** @type {any} */ (packSections({ diff, budget: 20, measure: (/** @type {any} */ d) => 10 * d.hunks.length }));
    assert.deepEqual([2, 5, 99].map((line) => sectionOf(sections, { line_start: line })), [0, 1, 1]);
    // two sections with a gap (lines 1-3 and 9-11): 0 and 5 are nearer the first, 6 is a tie (the earlier), 7 nearer the second
    const gap = [{ diff: diffOf('@@ -0,0 +1,3 @@\n+a\n+b\n+c\n', '') }, { diff: diffOf('@@ -8,0 +9,3 @@\n+x\n+y\n+z\n', '') }];
    assert.deepEqual([0, 5, 6, 7, 10, 500].map((line) => sectionOf(gap, { line_start: line })), [0, 0, 0, 1, 1, 1]);
    assert.equal(sectionOf([], { line_start: 1 }), -1);
    assert.deepEqual(sectionFindings([{ id: 'F1' }, { id: 'F2' }], 3).map((f) => f.id), ['S3.F1', 'S3.F2']);
  });

  test('a CRLF file: fences and headings match as in an LF file (a `# not a heading` inside a fence is skipped)', () => {
    const content = ['# One', '```', '# not a heading', '```', '## Two', 'text'].join('\r\n');
    assert.deepEqual([...markdownHeadings(content)], [[1, '# One'], [5, '## Two']]);
  });

  test('YAML front matter on line 1 holds no heading; an unclosed `---` is not front matter', () => {
    assert.deepEqual([...markdownHeadings('---\ntitle: x\n# not a heading\n---\n# Real\n')], [[5, '# Real']]);
    assert.deepEqual([...markdownHeadings('---\r\n# no\r\n...\r\n## Yes\r\n')], [[4, '## Yes']]);
    assert.deepEqual([...markdownHeadings('---\n# A\n')], [[2, '# A']]);
  });

  test('a hunk line that does not parse (no matching hunk) is never dropped: sectionGroups null, packSections split_required with the whole size and no section', () => {
    const content = '# A\na\n';
    const diff = diffOf('@@ -0,0 +1,1 @@\n+# A\n@@ bogus @@\n+a\n', content);
    const measure = (/** @type {any} */ d) => d.diffText.length;
    assert.deepEqual([diff.hunks.length, sectionGroups(diff)], [1, null]);
    assert.deepEqual(packSections({ diff, budget: 5, measure }), { status: 'split_required', tokensIn: diff.diffText.length, budget: 5, section: null });
  });

  test('a diff with no hunk never yields an empty, approvable section list: split_required at the whole size', () => {
    const diff = diffOf('', '# A\n');
    assert.deepEqual(sectionGroups(diff), []);
    assert.deepEqual(packSections({ diff, budget: 1, measure: () => 7 }), { status: 'split_required', tokensIn: 7, budget: 1, section: null });
  });

  test('sum: a missing token count adds nothing; none at all stays null', () => {
    assert.deepEqual([sum(null, null), sum(null, undefined), sum(undefined, 5), sum(4, null), sum(4, 5)], [null, null, 5, 4, 9]);
  });

  test('mergePacketInfo: lines add up; one shared context mode is kept, two different modes are `mixed`', () => {
    const a = mergePacketInfo(null, { contextMode: 'recheck', contextLines: 10 });
    const b = mergePacketInfo(a, { contextMode: 'recheck', contextLines: 5 });
    assert.deepEqual([a, b, mergePacketInfo(b, { contextMode: 'minimal', contextLines: 1 })], [
      { context_mode: 'recheck', context_lines: 10 },
      { context_mode: 'recheck', context_lines: 15 },
      { context_mode: 'mixed', context_lines: 16 },
    ]);
  });

  test('the default review.budgets.full_in is 32 000 tokens (quick_in and judge_in unchanged)', () => {
    assert.deepEqual({ ...DEFAULT_BUDGETS }, { quick_in: 6000, full_in: 32000, judge_in: 8000 });
  });
});

/**
 * `docs/brief.md` (or `rel`) written as a new file, and the budget that fits one heading group
 * alone but never two of the 40-line sections: max(group alone) + `slack`.
 * @param {{rel?: string, slack?: number}} [opts]
 */
async function briefRepo({ rel = 'docs/brief.md', slack = 30 } = {}) {
  const repo = await makeRepo();
  writeFile(repo, rel, brief());
  const diff = await readFileDiff({ repoRoot: repo, file: rel, base: null });
  const alone = sectionGroups(diff).map((g) => diffOnlyTokens({ diff: g.diff, lens: 'full' }));
  return { repo, rel, diff, alone, budget: Math.max(...alone) + slack, whole: diffOnlyTokens({ diff, lens: 'full' }) };
}

/**
 * @param {string} repo @param {string} rel @param {Record<string, any>} cfg @param {ReturnType<typeof stubSpawn>['spawn']} spawn
 */
async function review(repo, rel, cfg, spawn) {
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const outcome = await reviewFile({ repoRoot: repo, file: rel, base: null, cfg, risk: 1.5, workDir: path.join(freshDir('work'), 'w') }, { spawn, writeRow: async (row) => void rows.push(row) });
  return { outcome, rows };
}

describe('Markdown sections (B54): the review engine', () => {
  test('a Markdown diff over full_in is reviewed in 3 section packets (title + Alpha share one); approved only when all are', async () => {
    const { repo, rel, budget, whole } = await briefRepo();
    assert.ok(whole > budget);
    const { spawn, packets } = stubSpawn();
    const { outcome, rows } = await review(repo, rel, cfgFor({ budgets: { full_in: budget } }), spawn);
    assert.deepEqual(packets.map(hunksOf), [['@@ -0,0 +1,2 @@', '@@ -0,0 +3,41 @@'], ['@@ -0,0 +44,41 @@'], ['@@ -0,0 +85,41 @@']]);
    assert.deepEqual([outcome.status, outcome.approved, outcome.findings], ['reviewed', true, []]);
    assert.deepEqual(outcome.sections, [
      { index: 1, headings: ['# Title', '## Alpha'], hunks: 2 },
      { index: 2, headings: ['## Bravo'], hunks: 1 },
      { index: 3, headings: ['## Delta'], hunks: 1 },
    ]);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.section, s.lens, s.status]), [[1, 'full', 'ok'], [2, 'full', 'ok'], [3, 'full', 'ok']]);
    assert.deepEqual(rows.filter((r) => r.event === 'review.plan').map((r) => [r.section, r.sections]), [[1, 3], [2, 3], [3, 3]]);
    assert.equal(outcome.summary, 'S1: fake review: no defect found\nS2: fake review: no defect found\nS3: fake review: no defect found');
  });

  test('a finding in one section: not approved, its id prefixed with the section (S2.F1)', async () => {
    const { repo, rel, budget } = await briefRepo();
    const { spawn } = stubSpawn((text) => (text.includes('+## Bravo') ? { passed: false, findings: [finding('F1', 50)] } : {}));
    const { outcome } = await review(repo, rel, cfgFor({ budgets: { full_in: budget } }), spawn);
    assert.deepEqual([outcome.status, outcome.approved, outcome.findings.map((/** @type {any} */ f) => [f.id, f.line_start])], ['reviewed', false, [['S2.F1', 50]]]);
  });

  test('a section that fails the stub guard: the file is unavailable and the later sections never run', async () => {
    const { repo, rel, budget } = await briefRepo();
    const { spawn, packets } = stubSpawn((text) => (text.includes('+## Bravo') ? { reviewed_hunks: [] } : {}));
    const { outcome, rows } = await review(repo, rel, cfgFor({ budgets: { full_in: budget } }), spawn);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved, packets.length], ['unavailable', 'hunks_mismatch', false, 2]);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.section, s.status]), [[1, 'ok'], [2, 'unavailable']]);
    assert.deepEqual(rows.filter((r) => r.event === 'review.unavailable').map((r) => [r.reason, r.section]), [['hunks_mismatch', 2]]);
  });

  test('one section alone over the budget: split_required with its tokens_in, the budget and the section, no session', async () => {
    const { repo, rel, alone } = await briefRepo();
    const budget = Math.max(...alone) - 1;
    const { spawn, packets } = stubSpawn();
    const { outcome } = await review(repo, rel, cfgFor({ budgets: { full_in: budget } }), spawn);
    assert.deepEqual([outcome.status, outcome.approved, outcome.tokens_in, outcome.budget, outcome.section, packets.length], ['split_required', false, alone[1], budget, '## Alpha', 0]);
  });

  test('a non-Markdown file over the budget stays split_required, with the whole diff-only packet as tokens_in', async () => {
    const { repo, rel, budget, whole } = await briefRepo({ rel: 'docs/brief.txt' });
    const { spawn, packets } = stubSpawn();
    const { outcome } = await review(repo, rel, cfgFor({ budgets: { full_in: budget } }), spawn);
    assert.deepEqual([outcome.status, outcome.tokens_in, outcome.budget, packets.length], ['split_required', whole, budget, 0]);
  });
});

describe('Markdown sections (B54): the fix loop recheck', () => {
  test('a recheck over the budget runs one session per section; each lists only its own open findings; all resolved ⇒ complete', async () => {
    const repoRoot = freshDir('md-fixloop');
    const rel = 'docs/brief.md';
    writeFile(repoRoot, rel, brief());
    const workDir = path.join(freshDir('work'), 'w');
    // the fix rewrites every section line, so the fix hunks are the whole document
    const fixed = await fixHunkDiff({ file: rel, previous: brief(), current: brief(' (fixed)'), workDir });
    const alone = sectionGroups(fixed).map((g) => diffOnlyTokens({ diff: g.diff, lens: 'recheck' }));
    assert.equal(alone.length, 3);
    const budget = Math.max(...alone) + 30;
    assert.ok(diffOnlyTokens({ diff: fixed, lens: 'recheck' }) > budget);
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    const { spawn, packets } = stubSpawn((text) => {
      const ids = [...text.slice(text.indexOf('\n## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => m[1]);
      return { resolved: ids.map((id) => ({ id, resolved: true, why: 'fixed' })) };
    });
    const deps = {
      repoRoot,
      cfg: cfgFor({ budgets: { full_in: budget } }),
      workDir,
      writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r),
      spawn,
      review: async () => ({ status: 'reviewed', engine: 'adaptive', depth: 'full', findings: [finding('S1.F1', 10, 'critical'), finding('S3.F1', 100, 'critical')], sessions: [{ role: 'judge', lens: 'judge' }] }),
      jev: async () => ({ ok: true, answers: { resolved: { type: 'noul', noul: 0.2 } } }),
    };
    const state = await runRound(newFileState({ file: rel, level: 'L2' }), deps);
    assert.deepEqual(state.open.map((f) => f.id), ['S1.F1', 'S3.F1']);
    writeFileSync(path.join(repoRoot, rel), brief(' (fixed)'));
    await runRound(state, deps);
    assert.deepEqual(packets.map(hunksOf), [['@@ -1,43 +1,43 @@'], ['@@ -44,41 +44,41 @@'], ['@@ -85,41 +85,41 @@']]);
    const listed = packets.map((p) => p.slice(p.indexOf('## open findings\n') + 17).trimEnd());
    assert.deepEqual(listed, ['- S1.F1 (critical, lines 10-10): claim S1.F1', '(none)', '- S3.F1 (critical, lines 100-100): claim S3.F1']);
    assert.equal(state.status, 'complete');
    assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind, r.closed, r.open_after, r.tokens_in, r.tokens_out]), [[1, 'full', 0, 2, null, null], [2, 'recheck', 2, 0, 2700, 900]]);
  });
});

describe('Markdown sections (B54) fix round 1', () => {
  test('engine: packing and the packet share one measure — a budget exactly at the largest group reviews 4 packets, never split_required', async () => {
    const { repo, rel, alone } = await briefRepo();
    const budget = Math.max(...alone);
    const { spawn, packets } = stubSpawn();
    const { outcome } = await review(repo, rel, cfgFor({ budgets: { full_in: budget } }), spawn);
    assert.deepEqual([outcome.status, outcome.approved, packets.length], ['reviewed', true, 4]);
    assert.deepEqual(outcome.sections.map((/** @type {any} */ s) => s.headings), [['# Title'], ['## Alpha'], ['## Bravo'], ['## Delta']]);
  });

  test('packRecheck: a group\'s open findings count toward its packet — the finding tips the group over ⇒ split_required with that section', async () => {
    const workDir = path.join(freshDir('work'), 'w');
    const rel = 'docs/brief.md';
    const diff = await fixHunkDiff({ file: rel, previous: brief(), current: brief(' (fixed)'), workDir });
    const groups = /** @type {any[]} */ (sectionGroups(diff));
    const alone = groups.map((g) => recheckTokens(g.diff, []));
    const budget = Math.max(...alone);
    assert.equal(alone.indexOf(budget), 0);
    assert.deepEqual(/** @type {any} */ (packRecheck(diff, [], budget)).sections.length, 3);
    const f = finding('S1.F1', 10, 'critical');
    assert.deepEqual(packRecheck(diff, [f], budget), { status: 'split_required', tokensIn: recheckTokens(groups[0].diff, [f]), budget, section: '## Alpha' });
    assert.ok(recheckTokens(groups[0].diff, [f]) > budget);
  });

  test('fix loop: a Markdown section alone over the budget stops with tokens_in, budget and the section heading (next and review.cap)', async () => {
    const repoRoot = freshDir('md-stop');
    const rel = 'docs/brief.md';
    writeFile(repoRoot, rel, brief());
    const workDir = path.join(freshDir('work'), 'w');
    const diff = await fixHunkDiff({ file: rel, previous: brief(), current: brief(' (fixed)'), workDir });
    const open = [finding('S1.F1', 10, 'critical')];
    const budget = 600;
    const expected = /** @type {any} */ (packRecheck(diff, open, budget));
    assert.equal(expected.status, 'split_required');
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    const { spawn, packets } = stubSpawn();
    const deps = {
      repoRoot,
      cfg: cfgFor({ budgets: { full_in: budget } }),
      workDir,
      writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r),
      spawn,
      review: async () => ({ status: 'reviewed', engine: 'adaptive', depth: 'full', findings: open, sessions: [{ role: 'judge', lens: 'judge' }] }),
      jev: async () => ({ ok: true, answers: { resolved: { type: 'noul', noul: 0.2 } } }),
    };
    const state = await runRound(newFileState({ file: rel, level: 'L2' }), deps);
    writeFileSync(path.join(repoRoot, rel), brief(' (fixed)'));
    await runRound(state, deps);
    assert.deepEqual([state.status, state.next, packets.length], ['stopped', { action: 'stop', reason: 'split_required', tokens_in: expected.tokensIn, budget, section: expected.section }, 0]);
    // the title's context lines join the Alpha group, so the section is named `## Alpha`
    assert.equal(expected.section, '## Alpha');
    assert.deepEqual(rows.filter((r) => r.event === 'review.cap').map((r) => [r.reason, r.tokens_in, r.budget, r.section]), [['split_required', expected.tokensIn, budget, '## Alpha']]);
  });

  test('fix loop: a moved line (past the end) and a late finding kept open by `late_findings: block` (between fix hunks) each land in the nearest section', async () => {
    const repoRoot = freshDir('md-late');
    const rel = 'docs/brief.md';
    const only = ['Alpha', 'Delta'];
    writeFile(repoRoot, rel, brief());
    const workDir = path.join(freshDir('work'), 'w');
    // the fixes touch Alpha and Delta only: two fix hunks (lines 1-46 and 83-125), Bravo in between untouched
    const fixDiff = await fixHunkDiff({ file: rel, previous: brief(), current: brief(' (fixed)', only), workDir });
    assert.deepEqual(fixDiff.hunks.map((h) => h.header), ['@@ -1,46 +1,46 @@', '@@ -83,43 +83,43 @@']);
    const groups = /** @type {any[]} */ (sectionGroups(fixDiff));
    const budget = Math.max(...groups.map((g) => diffOnlyTokens({ diff: g.diff, lens: 'recheck' }))) + 200;
    assert.ok(diffOnlyTokens({ diff: fixDiff, lens: 'recheck' }) > budget);
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    let round = 1;
    const { spawn, packets } = stubSpawn((text) => {
      const ids = [...text.slice(text.indexOf('\n## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => m[1]);
      // round 2, the Alpha packet: a new warning on line 60 (Bravo), outside every fix hunk
      const late = round === 2 && text.includes('+Alpha line') ? [finding('L1', 60)] : [];
      return { passed: late.length === 0, findings: late, resolved: ids.map((id) => ({ id, resolved: true, why: 'fixed' })) };
    });
    const deps = {
      repoRoot,
      cfg: cfgFor({ budgets: { full_in: budget }, late_findings: 'block' }),
      workDir,
      writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r),
      spawn,
      // S3.F1 names line 130: the document has 125 lines (the line moved)
      review: async () => ({ status: 'reviewed', engine: 'adaptive', depth: 'full', findings: [finding('S1.F1', 10, 'critical'), finding('S3.F1', 130, 'critical')], sessions: [{ role: 'judge', lens: 'judge' }] }),
      jev: async () => ({ ok: true, answers: { resolved: { type: 'noul', noul: 0.2 } } }),
    };
    const state = await runRound(newFileState({ file: rel, level: 'L2' }), deps);
    round = 2;
    writeFileSync(path.join(repoRoot, rel), brief(' (fixed)', only));
    await runRound(state, deps);
    assert.deepEqual([state.status, state.open.map((f) => [f.id, f.line_start])], ['open', [['S1.L1', 60]]]);
    assert.deepEqual(rows.filter((r) => r.event === 'review.late_finding').map((r) => [r.round, r.finding, r.mode]), [[2, 'S1.L1', 'block']]);
    round = 3;
    writeFileSync(path.join(repoRoot, rel), brief(' (again)', only));
    await runRound(state, deps);
    const listed = packets.map((p) => p.slice(p.indexOf('## open findings\n') + 17).trimEnd());
    assert.deepEqual(listed, [
      '- S1.F1 (critical, lines 10-10): claim S1.F1',
      '- S3.F1 (critical, lines 130-130): claim S3.F1',
      '- S1.L1 (warning, lines 60-60): claim L1',
      '(none)',
    ]);
    assert.equal(state.status, 'complete');
    // both section packets fell to ± min_context_lines under this tight budget: one shared mode
    assert.deepEqual(state.last_packet?.context_mode, 'minimal');
  });

  test('fix loop: a non-Markdown recheck whose session reports no usage keeps tokens_in and tokens_out null', async () => {
    const repoRoot = freshDir('mjs-null');
    const rel = 'src/a.mjs';
    writeFile(repoRoot, rel, 'export const a = 1;\nexport const b = 2;\n');
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    const spawn = async (/** @type {{promptPath: string}} */ o) => {
      const text = readFileSync(o.promptPath, 'utf8');
      return { status: 'ok', exit_code: 0, answer: answerFor(hunksOf(text), { resolved: [{ id: 'F1', resolved: true, why: 'ok' }] }), row: { tokens_out: 300 } };
    };
    const deps = {
      repoRoot,
      cfg: cfgFor(),
      workDir: path.join(freshDir('work'), 'w'),
      writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r),
      spawn,
      review: async () => ({ status: 'reviewed', engine: 'adaptive', depth: 'full', findings: [{ ...finding('F1', 1, 'critical'), file: rel }], sessions: [{ role: 'judge', lens: 'judge' }] }),
      jev: async () => ({ ok: true, answers: { resolved: { type: 'noul', noul: 0.2 } } }),
    };
    const state = await runRound(newFileState({ file: rel, level: 'L2' }), deps);
    writeFileSync(path.join(repoRoot, rel), 'export const a = 3;\nexport const b = 2;\n');
    await runRound(state, deps);
    assert.equal(state.status, 'complete');
    assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.tokens_in, r.tokens_out, r.context_mode]), [[1, null, null, undefined], [2, null, null, 'recheck']]);
  });
});
