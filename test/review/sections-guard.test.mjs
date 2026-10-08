// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { answerFor, cfgFor, freshDir, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { beforeEach, describe, mock, test } from 'node:test';

// B54 guards that the real packer never reaches (it is never `ok` with no section, and it packs
// with the packet's own measure): `packSections` is swapped through `mock.module`, which needs
// `node --experimental-test-module-mocks` (as `npm test` runs).
if (typeof mock.module !== 'function') throw new Error('test/review/sections-guard.test.mjs requires `node --experimental-test-module-mocks --test …` (run it via `npm test`).');

const real = await import('../../src/review/sections.mjs');
const realPacket = await import('../../src/review/packet.mjs');
const { diffOnlyTokens, readFileDiff } = realPacket;

/** What the mocked `packSections`, `sectionGroups` and `assemblePacket` answer (default: the real ones). */
let answer = (/** @type {any} */ opts) => real.packSections(opts);
let groupsAnswer = (/** @type {any} */ diff) => real.sectionGroups(diff);
let packetAnswer = /** @type {(opts: any) => any} */ ((/** @type {any} */ opts) => realPacket.assemblePacket(opts));
let packCalls = 0;
mock.module(new URL('../../src/review/sections.mjs', import.meta.url).href, {
  namedExports: {
    ...real,
    packSections: (/** @type {any} */ opts) => {
      packCalls += 1;
      return answer(opts);
    },
    sectionGroups: (/** @type {any} */ diff) => groupsAnswer(diff),
  },
});
mock.module(new URL('../../src/review/packet.mjs', import.meta.url).href, {
  namedExports: { ...realPacket, assemblePacket: (/** @type {any} */ opts) => packetAnswer(opts) },
});
const tag = `guard-${Date.now()}`;
const { reviewFile } = await import(`../../src/review/engine.mjs?${tag}`);
const { fixHunkDiff, newFileState, packRecheck, recheckTokens, runRound } = await import(`../../src/review/fixloop.mjs?${tag}`);

/** Back to the real packer, groups and packet. */
function reset() {
  answer = (/** @type {any} */ opts) => real.packSections(opts);
  groupsAnswer = (/** @type {any} */ diff) => real.sectionGroups(diff);
  packetAnswer = (/** @type {any} */ opts) => realPacket.assemblePacket(opts);
  packCalls = 0;
}

/** 44 lines: `# One` and `## Two`, 21 lines of text each. */
function doc2(suffix = '') {
  let out = '';
  for (const h of ['# One', '## Two']) {
    out += `${h}\n`;
    for (let i = 1; i <= 21; i += 1) out += `${h.replace(/#+ /, '')} line ${String(i).padStart(2, '0')}: words that make the packet bigger${suffix}.\n`;
  }
  return out;
}

/** @param {string} id @param {number} line @param {string} file */
const crit = (id, line, file) => ({ id, file, line_start: line, line_end: line, severity: /** @type {const} */ ('critical'), category: 'docs', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
}

/**
 * A fix loop on `docs/a.md` (doc2): round 1 opens `open`; the fix rewrites every text line.
 * @param {{open: Array<Record<string, any>>, budget: number, resolve?: boolean}} opts
 */
async function recheckRound({ open, budget, resolve = false }) {
  const repoRoot = freshDir('guard-recheck');
  const rel = 'docs/a.md';
  writeFile(repoRoot, rel, doc2());
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  /** @type {string[]} */
  const packets = [];
  const spawn = async (/** @type {{promptPath: string}} */ o) => {
    const text = readFileSync(o.promptPath, 'utf8');
    packets.push(text);
    const ids = [...text.slice(text.indexOf('\n## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => m[1]);
    return { status: 'ok', exit_code: 0, answer: answerFor(hunksOf(text), { resolved: ids.map((id) => ({ id, resolved: resolve, why: 'x' })) }), usage: { tokens_in: 1, tokens_out: 300 } };
  };
  const deps = {
    repoRoot,
    cfg: cfgFor({ budgets: { full_in: budget } }),
    workDir: path.join(freshDir('work'), 'w'),
    writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r),
    spawn,
    review: async () => ({ status: 'reviewed', engine: 'adaptive', depth: 'full', findings: open, sessions: [{ role: 'judge', lens: 'judge' }] }),
    jev: async () => ({ ok: true, answers: { resolved: { type: 'noul', noul: 0.2 } } }),
  };
  const state = await runRound(newFileState({ file: rel, level: 'L2' }), deps);
  writeFileSync(path.join(repoRoot, rel), doc2(' (fixed)'));
  return { state, deps, rows, packets, rel, run: () => runRound(state, deps) };
}

/** 42 lines: `# Title`, then 40 lines of text. */
function doc(suffix = '') {
  let out = '# Title\n';
  for (let i = 1; i <= 41; i += 1) out += `line ${String(i).padStart(2, '0')}: words that make the packet bigger${suffix}.\n`;
  return out;
}

/** A session spawner that must never run; it counts the calls. */
function noSession() {
  const calls = { n: 0 };
  const spawn = async (/** @type {{promptPath: string}} */ o) => {
    calls.n += 1;
    return { status: 'ok', exit_code: 0, answer: answerFor([], {}), usage: { tokens_in: 1, tokens_out: 300 }, text: readFileSync(o.promptPath, 'utf8') };
  };
  return { spawn, calls };
}

describe('B54 guards behind the section packer', () => {
  beforeEach(() => reset());

  test('engine: a packer that says ok with ZERO sections never approves — split_required at the whole size, no session', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'docs/a.md', doc());
    const diff = await readFileDiff({ repoRoot: repo, file: 'docs/a.md', base: null });
    const whole = diffOnlyTokens({ diff, lens: 'full' });
    answer = () => ({ status: 'ok', sections: [] });
    const { spawn, calls } = noSession();
    const budget = whole - 1;
    const outcome = await reviewFile({ repoRoot: repo, file: 'docs/a.md', base: null, cfg: cfgFor({ budgets: { full_in: budget } }), risk: 1.5, workDir: path.join(freshDir('work'), 'w') }, { spawn });
    assert.deepEqual([outcome.status, outcome.approved, outcome.tokens_in, outcome.budget, Object.hasOwn(outcome, 'section'), calls.n], ['split_required', false, whole, budget, false, 0]);
  });

  test('engine: a section packet that is still over the budget stops the file before any session, with its size and heading', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'docs/a.md', doc());
    const diff = await readFileDiff({ repoRoot: repo, file: 'docs/a.md', base: null });
    const whole = diffOnlyTokens({ diff, lens: 'full' });
    answer = () => ({ status: 'ok', sections: [{ index: 1, headings: ['# Title'], keys: [1], diff }] });
    const { spawn, calls } = noSession();
    const budget = whole - 1;
    const outcome = await reviewFile({ repoRoot: repo, file: 'docs/a.md', base: null, cfg: cfgFor({ budgets: { full_in: budget } }), risk: 1.5, workDir: path.join(freshDir('work'), 'w') }, { spawn });
    assert.deepEqual([outcome.status, outcome.tokens_in, outcome.budget, outcome.section, calls.n], ['split_required', whole, budget, '# Title', 0]);
  });

  test('fix loop: a section recheck packet over the budget stops split_required with its heading BEFORE any recheck session', async () => {
    const repoRoot = freshDir('guard-fixloop');
    const rel = 'docs/a.md';
    writeFile(repoRoot, rel, doc());
    const workDir = path.join(freshDir('work'), 'w');
    const fixed = await fixHunkDiff({ file: rel, previous: doc(), current: doc(' (fixed)'), workDir });
    const open = [{ id: 'F1', file: rel, line_start: 5, line_end: 5, severity: /** @type {const} */ ('critical'), category: 'docs', claim: 'claim F1', evidence: 'e', fix: 'f' }];
    // the section packet (here the whole diff) fails to build: the stop carries that packet's own size
    const size = diffOnlyTokens({ diff: fixed, lens: 'recheck' });
    const budget = diffOnlyTokens({ diff: fixed, lens: 'recheck' }) - 1;
    answer = (/** @type {any} */ opts) => ({ status: 'ok', sections: [{ index: 1, headings: ['# Title'], keys: [1], diff: opts.diff }] });
    /** @type {Array<Record<string, any>>} */
    const rows = [];
    const { spawn, calls } = noSession();
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
    writeFileSync(path.join(repoRoot, rel), doc(' (fixed)'));
    await runRound(state, deps);
    assert.deepEqual([state.status, state.next, calls.n], ['stopped', { action: 'stop', reason: 'split_required', tokens_in: size, budget, section: '# Title' }, 0]);
    assert.deepEqual(rows.filter((r) => r.event === 'review.cap').map((r) => [r.reason, r.tokens_in, r.budget, r.section, r.open]), [['split_required', size, budget, '# Title', ['F1']]]);
  });

  test('engine: a packer split naming a section with no size is measured on THAT section, not the whole file', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'docs/a.md', doc2());
    const diff = await readFileDiff({ repoRoot: repo, file: 'docs/a.md', base: null });
    const two = /** @type {any[]} */ (real.sectionGroups(diff)).find((g) => g.heading === '## Two');
    const budget = diffOnlyTokens({ diff, lens: 'full' }) - 1;
    answer = () => ({ status: 'split_required', tokensIn: Number.NaN, budget, section: '## Two' });
    const { spawn, calls } = noSession();
    const outcome = await reviewFile({ repoRoot: repo, file: 'docs/a.md', base: null, cfg: cfgFor({ budgets: { full_in: budget } }), risk: 1.5, workDir: path.join(freshDir('work'), 'w') }, { spawn });
    assert.deepEqual([outcome.status, outcome.tokens_in, outcome.budget, outcome.section, calls.n], ['split_required', diffOnlyTokens({ diff: two.diff, lens: 'full' }), budget, '## Two', 0]);
  });

  test("engine: the guard on a section packet sizes it on that section's own diff", async () => {
    const repo = await makeRepo();
    writeFile(repo, 'docs/a.md', doc2());
    const diff = await readFileDiff({ repoRoot: repo, file: 'docs/a.md', base: null });
    const two = /** @type {any[]} */ (real.sectionGroups(diff))[1];
    const own = diffOnlyTokens({ diff: two.diff, lens: 'full' });
    answer = () => ({ status: 'ok', sections: [{ index: 1, headings: ['## Two'], keys: [two.key], diff: two.diff }] });
    const { spawn, calls } = noSession();
    const outcome = await reviewFile({ repoRoot: repo, file: 'docs/a.md', base: null, cfg: cfgFor({ budgets: { full_in: own - 1 } }), risk: 1.5, workDir: path.join(freshDir('work'), 'w') }, { spawn });
    assert.deepEqual([outcome.status, outcome.tokens_in, outcome.budget, outcome.section, calls.n], ['split_required', own, own - 1, '## Two', 0]);
  });

  test('packRecheck: no heading group at all (sectionGroups null) => split_required at the whole size, never sectioned', async () => {
    const workDir = path.join(freshDir('work'), 'w');
    const fixed = await fixHunkDiff({ file: 'docs/a.md', previous: doc2(), current: doc2(' (fixed)'), workDir });
    const open = [crit('F1', 5, 'docs/a.md')];
    groupsAnswer = () => null;
    assert.deepEqual([packRecheck(fixed, open, 10), packCalls], [{ status: 'split_required', tokensIn: recheckTokens(fixed, open), budget: 10, section: null }, 0]);
  });

  test('fix loop: groups null => the round stops split_required at the whole size with no session; the finding stays open', async () => {
    groupsAnswer = () => null;
    const open = [crit('F1', 5, 'docs/a.md')];
    const r = await recheckRound({ open, budget: 300 });
    await r.run();
    const fixed = await fixHunkDiff({ file: r.rel, previous: doc2(), current: doc2(' (fixed)'), workDir: path.join(freshDir('work'), 'w') });
    assert.deepEqual([r.state.status, r.state.next, r.state.open.map((f) => f.id), r.packets.length], ['stopped', { action: 'stop', reason: 'split_required', tokens_in: recheckTokens(fixed, open), budget: 300 }, ['F1'], 0]);
  });

  test('fix loop: a finding no packet holds is listed in the last packet and closes only if THAT session resolves it', async () => {
    // the packer keeps only the `## Two` group and names no group for line 5
    answer = (/** @type {any} */ opts) => {
      const groups = /** @type {any[]} */ (real.sectionGroups(opts.diff));
      return { status: 'ok', sections: [{ index: 1, headings: ['## Two'], keys: [groups[1].key], diff: groups[1].diff }] };
    };
    const open = [crit('F1', 5, 'docs/a.md')];
    const kept = await recheckRound({ open, budget: 1200 });
    await kept.run();
    assert.equal(kept.packets.length, 1);
    assert.match(kept.packets[0], /## open findings\n- F1 \(critical, lines 5-5\): claim F1\n$/);
    assert.deepEqual([kept.state.status, kept.state.open.map((f) => f.id)], ['open', ['F1']]);
    const closed = await recheckRound({ open, budget: 1200, resolve: true });
    await closed.run();
    assert.deepEqual([closed.state.status, closed.state.open.length], ['complete', 0]);
  });

  test('fix loop: a whole recheck packet with a status other than split_required stops exactly as before, never sectioned', async () => {
    packetAnswer = () => ({ status: 'refused_packet' });
    const r = await recheckRound({ open: [crit('F1', 5, 'docs/a.md')], budget: 300 });
    await r.run();
    assert.deepEqual([r.state.status, r.state.next, packCalls, r.packets.length], ['stopped', { action: 'stop', reason: 'refused_packet' }, 0, 0]);
    assert.deepEqual(r.rows.filter((row) => row.event === 'review.cap').map((row) => [row.reason, Object.hasOwn(row, 'tokens_in')]), [['refused_packet', false]]);
  });

  test('fix loop: a section packet that builds but whose open-findings list puts it over the budget stops before any session, sized with recheckTokens', async () => {
    const workDir = path.join(freshDir('work'), 'w');
    const fixed = await fixHunkDiff({ file: 'docs/a.md', previous: doc2(), current: doc2(' (fixed)'), workDir });
    const two = /** @type {any[]} */ (real.sectionGroups(fixed))[1];
    answer = (/** @type {any} */ opts) => {
      const groups = /** @type {any[]} */ (real.sectionGroups(opts.diff));
      return { status: 'ok', sections: [{ index: 1, headings: ['## Two'], keys: [groups[1].key], diff: groups[1].diff }] };
    };
    const open = [crit('F1', 30, 'docs/a.md'), crit('F2', 31, 'docs/a.md'), crit('F3', 32, 'docs/a.md')];
    const budget = diffOnlyTokens({ diff: two.diff, lens: 'recheck' }) + 2;
    const r = await recheckRound({ open, budget });
    await r.run();
    assert.deepEqual([r.state.status, r.state.next, r.packets.length], ['stopped', { action: 'stop', reason: 'split_required', tokens_in: recheckTokens(two.diff, open), budget, section: '## Two' }, 0]);
  });

  test('fix loop: a section packet that fails to build stops with ITS status, not split_required', async () => {
    let calls = 0;
    packetAnswer = (/** @type {any} */ opts) => {
      calls += 1;
      return calls === 1 ? realPacket.assemblePacket(opts) : { status: 'refused_packet' };
    };
    const r = await recheckRound({ open: [crit('F1', 5, 'docs/a.md')], budget: 1200 });
    await r.run();
    assert.deepEqual([r.state.status, r.state.next, r.packets.length], ['stopped', { action: 'stop', reason: 'refused_packet' }, 0]);
  });
});
