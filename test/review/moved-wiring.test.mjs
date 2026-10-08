import { answerFor, cfgFor, commitAll, freshDir, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { reviewFile } = await import('../../src/review/engine.mjs');
const { newFileState, runRound } = await import('../../src/review/fixloop.mjs');
const { blockPeerDiffs, blockPeers } = await import('../../src/worker/engine.mjs');
const { GIT_DIFF, MAX_DIFF_BYTES, movedText, movesFor, peerDiffReader, readPeerDiffs } = await import('../../src/review/moved.mjs');
const { diffOnlyTokens, readFileDiff } = await import('../../src/review/packet.mjs');

/** The view that moves from `src/a.mjs` (base lines 4-11) to `src/b.mjs` (lines 3-10). */
const VIEW = ['export function renderList(items) {', '  const rows = items.map((item) => renderRow(item));', '', '  return wrapTable(rows, { striped: true });', '}', 'export function renderRow(item) {', '  return `<tr>${item.name}</tr>`;', '}'];

const OUT_SECTION = [
  '## moved code',
  '- base lines 4-10 removed here were added in src/b.mjs lines 3-9 in this block.',
  'Do not report the removal of moved lines itself as a defect; review only whether references and imports are updated.',
].join('\n');
const IN_SECTION = [
  '## moved code',
  '- lines 3-9 were moved here from src/a.mjs (its base lines 4-10) in this block.',
  'Moved-in lines are reviewed in their new place like any change (imports, references, behaviour).',
].join('\n');

/** @param {string[]} rows @returns {string} */
const text = (rows) => `${rows.join('\n')}\n`;

/** A repo whose base commit has the view in `src/a.mjs`; the working tree moved it to a new `src/b.mjs`. */
async function movedRepo() {
  const repo = await makeRepo();
  writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';", '', 'export const keep = 1;', ...VIEW, 'export const tail = 2;']));
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';", '', 'export const keep = 1;', 'export const tail = 2;']));
  writeFile(repo, 'src/b.mjs', text(['// views', '', ...VIEW]));
  return { repo, base };
}

/** @param {string} t @returns {string[]} the packet's hunk list. */
function hunksOf(t) {
  const listed = t.slice(t.indexOf('\nhunks:\n') + 8).split('\n');
  const end = listed.findIndex((l) => !l.startsWith('- @@'));
  return listed.slice(0, end).map((l) => l.slice(2));
}

/** A stub session: echoes the packet's hunks (plus `extra`) and records every packet. */
function stubSpawn(extra = {}) {
  /** @type {string[]} */
  const packets = [];
  const spawn = async (/** @type {{promptPath: string}} */ o) => {
    const t = readFileSync(o.promptPath, 'utf8');
    packets.push(t);
    return { status: 'ok', exit_code: 0, answer: answerFor(hunksOf(t), extra), usage: { tokens_in: 900, tokens_out: 300 } };
  };
  return { spawn, packets };
}

describe('moved code reaches the reviewer (B56)', () => {
  test('round 1: the source file names where its removed lines went, the destination where they came from', async () => {
    const { repo, base } = await movedRepo();
    const cfg = cfgFor();
    const a = stubSpawn();
    const outA = await reviewFile({ repoRoot: repo, file: 'src/a.mjs', base, cfg, risk: 0.5, workDir: path.join(freshDir('w'), 'a'), peers: ['src/b.mjs'] }, { spawn: a.spawn });
    assert.deepEqual([outA.status, outA.approved], ['reviewed', true]);
    assert.equal(a.packets.length, 1);
    assert.ok(a.packets[0].includes(`\n${OUT_SECTION}\n## diff\n`));
    // every removed line still reaches the reviewer
    for (const line of VIEW) assert.ok(a.packets[0].includes(`\n-${line}\n`));
    const b = stubSpawn();
    await reviewFile({ repoRoot: repo, file: 'src/b.mjs', base, cfg, risk: 0.5, workDir: path.join(freshDir('w'), 'b'), peers: ['src/a.mjs'] }, { spawn: b.spawn });
    assert.ok(b.packets[0].includes(`\n${IN_SECTION}\n## diff\n`));
  });

  test('control: no peers, or a peer that is not available ⇒ no section, the review is unchanged', async () => {
    const { repo, base } = await movedRepo();
    const cfg = cfgFor();
    for (const peers of [undefined, [], ['src/gone.mjs'], ['../outside.mjs'], ['.env']]) {
      const s = stubSpawn();
      const out = await reviewFile({ repoRoot: repo, file: 'src/a.mjs', base, cfg, risk: 0.5, workDir: path.join(freshDir('w'), 'c'), ...(peers ? { peers } : {}) }, { spawn: s.spawn });
      assert.equal(out.status, 'reviewed');
      assert.equal(s.packets[0].includes('## moved code'), false);
    }
  });

  test('the recheck packet (fix hunks only) still carries the hint, from the diff against the base', async () => {
    const { repo, base } = await movedRepo();
    const cfg = cfgFor();
    const stale = { id: 'F1', file: 'src/a.mjs', line_start: 3, line_end: 4, severity: 'critical', category: 'correctness', claim: 'renderList was deleted; callers break', evidence: 'e', fix: 'f' };
    const { spawn, packets } = stubSpawn({ resolved: [{ id: 'F1', resolved: true, why: 'moved to src/b.mjs' }] });
    const deps = {
      repoRoot: repo,
      cfg,
      base,
      peers: ['src/b.mjs'],
      workDir: path.join(freshDir('work'), 'w'),
      writeRow: async () => undefined,
      spawn,
      review: async () => ({ status: 'reviewed', engine: 'adaptive', depth: 'dual', findings: [stale], sessions: [{ role: 'judge', lens: 'judge' }] }),
      jev: async () => ({ ok: true, answers: { resolved: { type: 'noul', noul: 0.2 } } }),
    };
    const state = await runRound(newFileState({ file: 'src/a.mjs', level: 'L2' }), deps);
    assert.deepEqual(state.open.map((f) => f.id), ['F1']);
    writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';", '', 'export const keep = 3;', 'export const tail = 2;']));
    await runRound(state, deps);
    assert.equal(packets.length, 1);
    assert.ok(packets[0].includes(`\n${OUT_SECTION}\n## diff\n`));
    assert.deepEqual(hunksOf(packets[0]), ['@@ -1,4 +1,4 @@']); // the fix hunk only, never the base hunks
    assert.equal(state.status, 'complete');
  });

  test('blockPeers: the block\'s other changed owned files; no base ⇒ none', async () => {
    const { repo, base } = await movedRepo();
    writeFile(repo, 'src/c.mjs', text(['export const notOwned = 1;']));
    assert.deepEqual(await blockPeers(repo, { base, owned: ['src/a.mjs', 'src/b.mjs'], file: 'src/a.mjs' }), ['src/b.mjs']);
    assert.deepEqual(await blockPeers(repo, { base, owned: ['src/**'], file: 'src/b.mjs' }), ['src/a.mjs', 'src/c.mjs']);
    assert.deepEqual(await blockPeers(repo, { base: null, owned: ['src/a.mjs', 'src/b.mjs'], file: 'src/a.mjs' }), []);
    assert.deepEqual(await blockPeers(repo, { base: 'f'.repeat(40), owned: ['src/a.mjs', 'src/b.mjs'], file: 'src/a.mjs' }), []);
  });

  test('readPeerDiffs: one listing gives each readable peer its own diff; ignored, secret-like, missing and outside paths give none', async () => {
    const { repo, base } = await movedRepo();
    writeFile(repo, '.gitignore', 'build/\n');
    writeFile(repo, 'build/out.mjs', text(VIEW));
    writeFile(repo, '.env.local', 'FAKE_TOKEN=not-a-secret\n');
    const peers = ['src/b.mjs', 'src/a.mjs', 'build/out.mjs', '.env.local', '../x.mjs', 'src/gone.mjs', 'src/b.mjs'];
    const got = await readPeerDiffs({ repoRoot: repo, base, file: 'src/c.mjs', peers });
    assert.deepEqual(got.map((p) => p.file), ['src/a.mjs', 'src/b.mjs']);
    for (const p of got) assert.equal(p.diffText, (await readFileDiff({ repoRoot: repo, file: p.file, base })).diffText);
    // the reviewed file itself is never its own peer
    assert.deepEqual((await readPeerDiffs({ repoRoot: repo, base, file: 'src/a.mjs', peers })).map((p) => p.file), ['src/b.mjs']);
  });

  test('the peer readers are memoised: one listing per ticket, however many packets ask', async () => {
    const { repo, base } = await movedRepo();
    const reader = peerDiffReader({ repoRoot: repo, base, file: 'src/a.mjs', peers: ['src/b.mjs'] });
    const first = reader();
    assert.equal(reader(), first);
    assert.deepEqual((await first).map((p) => p.file), ['src/b.mjs']);
    const block = blockPeerDiffs(repo, { base, owned: ['src/a.mjs', 'src/b.mjs'], file: 'src/a.mjs' });
    const once = block();
    assert.equal(block(), once);
    assert.deepEqual((await once).map((p) => p.file), ['src/b.mjs']);
    assert.deepEqual(await peerDiffReader({ repoRoot: repo, base: null, file: 'src/a.mjs', peers: ['src/b.mjs'] })(), []);
  });

  test('a Markdown file reviewed by section: each section packet carries only its own moves', async () => {
    const repo = await makeRepo();
    const filler = (/** @type {string} */ tag) => Array.from({ length: 40 }, (_, i) => `Paragraph ${tag} line ${i + 1} explains the step in some detail.`);
    const install = ['Install step alpha one.', 'Install step beta two.', 'Install step gamma three.'];
    const usage = ['Old usage line alpha one.', 'Old usage line beta two.', 'Old usage line gamma three.'];
    writeFile(repo, 'docs/guide.md', text(['# Guide', '', '## Install', ...filler('I'), '## Usage', ...filler('U'), ...usage]));
    writeFile(repo, 'docs/other.md', text(['# Other', ...install]));
    await commitAll(repo);
    const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
    writeFile(repo, 'docs/guide.md', text(['# Guide', '', '## Install', ...filler('i'), ...install, '## Usage', ...filler('u')]));
    writeFile(repo, 'docs/other.md', text(['# Other', ...usage]));
    const diff = await readFileDiff({ repoRoot: repo, file: 'docs/guide.md', base });
    const moves = await movesFor({ diff, peerDiffs: peerDiffReader({ repoRoot: repo, base, file: 'docs/guide.md', peers: ['docs/other.md'] }) });
    // a budget the whole diff (with its moved section) does not fit: reviewed by section
    const budget = diffOnlyTokens({ diff, lens: 'quick', moved: movedText(moves, diff.content, null) }) - 1;
    const cfg = { ...cfgFor(), review: { ...cfgFor().review, budgets: { quick_in: budget, full_in: budget } } };
    const { spawn, packets } = stubSpawn();
    const out = await reviewFile({ repoRoot: repo, file: 'docs/guide.md', base, cfg, risk: 0.5, workDir: path.join(freshDir('w'), 'md'), peers: ['docs/other.md'] }, { spawn });
    assert.equal(out.status, 'reviewed');
    assert.equal(packets.length, 2);
    const section = (/** @type {string} */ t) => (t.includes('## moved code') ? t.slice(t.indexOf('## moved code'), t.indexOf('\n## diff\n')) : '');
    assert.deepEqual(packets.map(section), [
      ['## moved code', '- lines 44-46 were moved here from docs/other.md (its base lines 2-4) in this block.', 'Moved-in lines are reviewed in their new place like any change (imports, references, behaviour).'].join('\n'),
      ['## moved code', '- base lines 85-87 removed here were added in docs/other.md lines 2-4 in this block.', 'Do not report the removal of moved lines itself as a defect; review only whether references and imports are updated.'].join('\n'),
    ]);
  });
});

/** A peer list that counts how often it is read (iterated). */
class CountingList extends Array {
  reads = 0;
  [Symbol.iterator]() {
    this.reads += 1;
    return super[Symbol.iterator]();
  }
}

/** Recheck deps on `repo` whose round 1 leaves one stale critical open (S1 never closes it). */
function recheckDeps(/** @type {string} */ repo, /** @type {string} */ base, /** @type {Record<string, any>} */ extra, /** @type {(t: string) => void} */ seen) {
  const stale = { id: 'F1', file: 'src/a.mjs', line_start: 3, line_end: 4, severity: 'critical', category: 'correctness', claim: 'renderList was deleted; callers break', evidence: 'e', fix: 'f' };
  return {
    repoRoot: repo,
    cfg: cfgFor(),
    base,
    workDir: path.join(freshDir('work'), 'w'),
    writeRow: async () => undefined,
    spawn: async (/** @type {{promptPath: string}} */ o) => {
      const t = readFileSync(o.promptPath, 'utf8');
      seen(t);
      return { status: 'ok', exit_code: 0, answer: answerFor(hunksOf(t)), usage: { tokens_in: 900, tokens_out: 300 } };
    },
    review: async () => ({ status: 'reviewed', engine: 'adaptive', depth: 'dual', findings: [stale], sessions: [{ role: 'judge', lens: 'judge' }] }),
    jev: async () => ({ ok: true, answers: { resolved: { type: 'noul', noul: 0.2 } } }),
    ...extra,
  };
}

describe('B56 fix round 1: peers read once, failures mean no hint', () => {
  test('two rechecks on one deps object (one ticket) read the peers once; a new deps object reads again', async () => {
    const { repo, base } = await movedRepo();
    const peers = new CountingList();
    peers.push('src/b.mjs');
    /** @type {string[]} */
    const packets = [];
    const deps = recheckDeps(repo, base, { peers }, (t) => packets.push(t));
    const state = await runRound(newFileState({ file: 'src/a.mjs', level: 'L2' }), deps);
    for (const keep of [3, 4]) {
      writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';", '', `export const keep = ${keep};`, 'export const tail = 2;']));
      await runRound(state, deps, { kind: 'recheck' });
    }
    assert.equal(packets.length, 2);
    for (const p of packets) assert.ok(p.includes(`\n${OUT_SECTION}\n## diff\n`));
    assert.equal(peers.reads, 1);
    const again = recheckDeps(repo, base, { peers }, () => undefined);
    const state2 = await runRound(newFileState({ file: 'src/a.mjs', level: 'L2' }), again);
    writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';", '', 'export const keep = 5;', 'export const tail = 2;']));
    await runRound(state2, again, { kind: 'recheck' });
    assert.equal(peers.reads, 2);
  });

  test('a peer list that throws while the reader is built: the recheck goes on with no hint', async () => {
    const { repo, base } = await movedRepo();
    /** @type {string[]} */
    const packets = [];
    const deps = recheckDeps(repo, base, {}, (t) => packets.push(t));
    Object.defineProperty(deps, 'peers', {
      get() {
        throw new Error('peer list unreadable');
      },
    });
    const state = await runRound(newFileState({ file: 'src/a.mjs', level: 'L2' }), deps);
    writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';", '', 'export const keep = 3;', 'export const tail = 2;']));
    await runRound(state, deps, { kind: 'recheck' });
    assert.equal(packets.length, 1);
    assert.equal(packets[0].includes('## moved code'), false);
    assert.equal(state.next?.action === 'retry', false);
  });

  test('reviewFile: a peer reader that rejects or throws means no hint, the review goes on', async () => {
    const { repo, base } = await movedRepo();
    const readers = [() => Promise.reject(new Error('peer diff failed')), () => {
      throw new Error('peer diff threw');
    }];
    for (const peerDiffs of readers) {
      const s = stubSpawn();
      const out = await reviewFile({ repoRoot: repo, file: 'src/a.mjs', base, cfg: cfgFor(), risk: 0.5, workDir: path.join(freshDir('w'), 'r'), peerDiffs: /** @type {any} */ (peerDiffs) }, { spawn: s.spawn });
      assert.deepEqual([out.status, out.approved], ['reviewed', true]);
      assert.equal(s.packets[0].includes('## moved code'), false);
    }
  });

  test('peer diffs are read with a/ b/ prefixes forced, whatever diff.noprefix or diff.mnemonicPrefix say', async () => {
    const { repo, base } = await movedRepo();
    writeFile(repo, 'src/c.mjs', text(VIEW)); // a second tracked peer: the shared listing is split by its headers
    await git(['add', 'src/c.mjs'], repo);
    await git(['config', 'diff.noprefix', 'true'], repo);
    await git(['config', 'diff.mnemonicPrefix', 'true'], repo);
    assert.deepEqual(GIT_DIFF, ['git', '-c', 'core.quotePath=false', '-c', 'diff.noprefix=false', '-c', 'diff.mnemonicPrefix=false', 'diff', '--no-color', '--no-ext-diff', '--src-prefix=a/', '--dst-prefix=b/']);
    const got = await readPeerDiffs({ repoRoot: repo, base, file: 'src/x.mjs', peers: ['src/a.mjs', 'src/c.mjs'] });
    assert.deepEqual(got.map((p) => p.file), ['src/a.mjs', 'src/c.mjs']);
    assert.ok(got[0].diffText.startsWith('diff --git a/src/a.mjs b/src/a.mjs\n'));
    assert.ok(got[1].diffText.startsWith('diff --git a/src/c.mjs b/src/c.mjs\n'));
  });

  test('a peer whose diff is over MAX_DIFF_BYTES is skipped, tracked or new; the others still give their diffs', async () => {
    const repo = await makeRepo();
    const big = Array.from({ length: Math.ceil(MAX_DIFF_BYTES / 60) + 10 }, (_, i) => `export const value${i} = 'padding padding padding padding padding';`);
    writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';", ...VIEW]));
    writeFile(repo, 'src/tracked-big.mjs', text(big));
    await commitAll(repo);
    const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
    writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';"]));
    writeFile(repo, 'src/tracked-big.mjs', text(['export const small = 1;'])); // its diff: every big line removed
    writeFile(repo, 'src/new-big.mjs', text(big));
    writeFile(repo, 'src/b.mjs', text(VIEW));
    const got = await readPeerDiffs({ repoRoot: repo, base, file: 'src/x.mjs', peers: ['src/a.mjs', 'src/tracked-big.mjs', 'src/new-big.mjs', 'src/b.mjs'] });
    assert.deepEqual(got.map((p) => p.file), ['src/a.mjs', 'src/b.mjs']);
    for (const p of got) assert.ok(Buffer.byteLength(p.diffText) <= MAX_DIFF_BYTES);
  });
});

describe('B56 fix round 2: deleted peers, non-ASCII and quoted names, readers per file', () => {
  for (const how of /** @type {const} */ (['git rm', 'plain delete'])) {
    test(`code moved out of a file that is then deleted (${how}): both packets get their note`, async () => {
      const { repo, base } = await movedRepo();
      if (how === 'git rm') await git(['rm', '-q', '-f', 'src/a.mjs'], repo);
      else rmSync(path.join(repo, 'src', 'a.mjs'));
      const cfg = cfgFor();
      const b = stubSpawn();
      await reviewFile({ repoRoot: repo, file: 'src/b.mjs', base, cfg, risk: 0.5, workDir: path.join(freshDir('w'), 'b'), peers: ['src/a.mjs'] }, { spawn: b.spawn });
      assert.ok(b.packets[0].includes(`\n${IN_SECTION}\n## diff\n`));
      const a = stubSpawn();
      const outA = await reviewFile({ repoRoot: repo, file: 'src/a.mjs', base, cfg, risk: 0.5, workDir: path.join(freshDir('w'), 'a'), peers: ['src/b.mjs'] }, { spawn: a.spawn });
      assert.equal(outA.status, 'reviewed');
      assert.ok(a.packets[0].includes(`\n${OUT_SECTION}\n## diff\n`));
    });
  }

  test('a peer with a non-ASCII name is matched in the shared listing and printed as is', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/vé.mjs', text(['// old place', ...VIEW]));
    writeFile(repo, 'src/c.mjs', text(['export const c = 1;']));
    await commitAll(repo);
    const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
    writeFile(repo, 'src/vé.mjs', text(['// old place']));
    writeFile(repo, 'src/c.mjs', text(['export const c = 2;']));
    writeFile(repo, 'src/b.mjs', text(['// views', '', ...VIEW]));
    const got = await readPeerDiffs({ repoRoot: repo, base, file: 'src/b.mjs', peers: ['src/vé.mjs', 'src/c.mjs'] });
    assert.deepEqual(got.map((p) => p.file), ['src/c.mjs', 'src/vé.mjs']);
    const s = stubSpawn();
    await reviewFile({ repoRoot: repo, file: 'src/b.mjs', base, cfg: cfgFor(), risk: 0.5, workDir: path.join(freshDir('w'), 'v'), peers: ['src/vé.mjs', 'src/c.mjs'] }, { spawn: s.spawn });
    assert.ok(
      s.packets[0].includes(
        `\n${['## moved code', '- lines 3-9 were moved here from src/vé.mjs (its base lines 2-8) in this block.', 'Moved-in lines are reviewed in their new place like any change (imports, references, behaviour).'].join('\n')}\n## diff\n`,
      ),
    );
  });

  test('a peer whose name holds a quote is skipped: never read, never printed', async () => {
    const { repo, base } = await movedRepo();
    writeFile(repo, 'src/q"b.mjs', text(VIEW));
    rmSync(path.join(repo, 'src', 'b.mjs'));
    const got = await readPeerDiffs({ repoRoot: repo, base, file: 'src/a.mjs', peers: ['src/q"b.mjs'] });
    assert.deepEqual(got, []);
    const s = stubSpawn();
    await reviewFile({ repoRoot: repo, file: 'src/a.mjs', base, cfg: cfgFor(), risk: 0.5, workDir: path.join(freshDir('w'), 'q'), peers: ['src/q"b.mjs'] }, { spawn: s.spawn });
    assert.equal(s.packets[0].includes('## moved code'), false);
    assert.equal(s.packets[0].includes('q"b'), false);
  });

  test('rechecks of A, then B, then A on one deps object read each file\'s peers once', async () => {
    const { repo, base } = await movedRepo();
    const peers = new CountingList();
    peers.push('src/a.mjs', 'src/b.mjs');
    /** @type {string[]} */
    const packets = [];
    const deps = recheckDeps(repo, base, { peers }, (t) => packets.push(t));
    const stateA = await runRound(newFileState({ file: 'src/a.mjs', level: 'L2' }), deps);
    const stateB = await runRound(newFileState({ file: 'src/b.mjs', level: 'L2' }), deps);
    const editA = (/** @type {number} */ keep) => writeFile(repo, 'src/a.mjs', text(["import { wrapTable } from './table.mjs';", '', `export const keep = ${keep};`, 'export const tail = 2;']));
    editA(3);
    await runRound(stateA, deps, { kind: 'recheck' });
    writeFile(repo, 'src/b.mjs', text(['// views (moved)', '', ...VIEW]));
    await runRound(stateB, deps, { kind: 'recheck' });
    editA(4);
    await runRound(stateA, deps, { kind: 'recheck' });
    assert.equal(packets.length, 3);
    assert.ok(packets[0].includes(`\n${OUT_SECTION}\n## diff\n`));
    assert.ok(packets[1].includes('- lines 3-9 were moved here from src/a.mjs (its base lines 4-10) in this block.'));
    assert.ok(packets[2].includes(`\n${OUT_SECTION}\n## diff\n`));
    assert.equal(peers.reads, 2); // A's peers once, B's peers once
  });
});
