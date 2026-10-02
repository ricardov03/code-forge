import { answerFor, cfgFor, freshDir, harness, lines, readRecords, stdinOf, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { buildRecheckPacket, converge, fixHunkDiff, newFileState, runRound } = await import('../../src/review/fixloop.mjs');

const FILE = 'src/a.mjs';

/** Rule 2c (B34, heavy rounds) off: these tests pin the rule-2 ladder on an L1 block with critical findings. */
const ladderOnly = () => ({ ...cfgFor(), escalation: { after_rounds_with_warnings: 0 } });

/** @param {string} id @param {number} line @param {'critical' | 'warning' | 'nit'} [severity] */
const finding = (id, line, severity = 'critical') => ({ id, file: FILE, line_start: line, line_end: line, severity, category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

/** Round 1 = a judge's final answer with these findings (judge findings are not re-triaged). */
const judgeReview = (/** @type {Array<Record<string, any>>} */ findings) => async () => ({
  status: 'reviewed',
  engine: 'adaptive',
  depth: 'dual',
  findings,
  sessions: [{ role: 'reviewer', lens: 'A' }, { role: 'reviewer', lens: 'B' }, { role: 'judge', lens: 'judge' }],
});

/** A mocked Jev answering `resolved` from `closeIds()` (0.95 ⇒ closes) else 0.2. */
const jevResolving = (/** @type {() => Set<string>} */ closeIds) => async (/** @type {{state: Record<string, any>}} */ req) => ({
  ok: true,
  answers: { resolved: { type: 'noul', noul: closeIds().has(req.state.finding.id) ? 0.95 : 0.2 } },
});

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  const end = listed.findIndex((l) => !l.startsWith('- @@'));
  return listed.slice(0, end).map((l) => l.slice(2));
}

/**
 * A stub session spawner: echoes the packet's hunks and adds `extra(text)`; records the packets.
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

/** A repo dir with a 380-line `src/a.mjs`, and a `fix` that edits one line per round. */
function fixture() {
  const repoRoot = freshDir('fixloop');
  writeFile(repoRoot, FILE, lines(380));
  /** @param {number} line @param {string} tag */
  const edit = (line, tag) => {
    const full = path.join(repoRoot, FILE);
    const all = readFileSync(full, 'utf8').split('\n');
    all[line - 1] = `export const v${line} = ${line} + ${JSON.stringify(tag)};`;
    writeFileSync(full, all.join('\n'));
  };
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const base = { repoRoot, cfg: cfgFor(), workDir: path.join(freshDir('work'), 'w'), writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r) };
  return { repoRoot, edit, rows, base };
}

test('S1 resolved ≥ 0.90 closes the finding with NO recheck session', async () => {
  const { edit, rows, base } = fixture();
  const { spawn, packets } = stubSpawn();
  const deps = { ...base, spawn, review: judgeReview([finding('F1', 200)]), jev: jevResolving(() => new Set(['F1'])) };
  const state = await runRound(newFileState({ file: FILE, level: 'L2' }), deps);
  assert.deepEqual(state.open.map((f) => f.id), ['F1']);
  edit(200, 'fixed');
  await runRound(state, deps);
  assert.equal(packets.length, 0);
  assert.equal(state.status, 'complete');
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind, r.open_before, r.closed, r.open_after]), [[1, 'full', 0, 0, 1], [2, 'recheck', 1, 1, 0]]);
  assert.equal(rows.filter((r) => r.event === 'review.approved').length, 1);
});

test('S1 resolved below 0.90 ⇒ exactly one recheck session (fake CLI), which closes it', async () => {
  const { repoRoot, edit, rows, base } = fixture();
  const before = readFileSync(path.join(repoRoot, FILE), 'utf8');
  edit(200, 'fixed');
  const diff = await fixHunkDiff({ file: FILE, previous: before, current: readFileSync(path.join(repoRoot, FILE), 'utf8'), workDir: base.workDir });
  writeFileSync(path.join(repoRoot, FILE), before);
  const answer = answerFor(diff.hunks.map((h) => h.header), { resolved: [{ id: 'F1', resolved: true, why: 'the fix handles it' }] });
  const h = harness({ repoRoot, cfg: base.cfg, env: { FAKE_ANSWER: JSON.stringify(answer) } });
  const deps = { ...base, spawn: h.spawn, review: judgeReview([finding('F1', 200)]), jev: jevResolving(() => new Set()) };
  const state = await runRound(newFileState({ file: FILE, level: 'L2' }), deps);
  edit(200, 'fixed');
  await runRound(state, deps);
  assert.equal(readRecords(h.records).length, 1);
  assert.equal(state.status, 'complete');
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.closed, r.open_after]), [[1, 0, 1], [2, 1, 0]]);
  assert.match(stdinOf(readRecords(h.records)[0]), /## open findings\n- F1 \(critical, lines 200-200\)/);
});

test('the round-2 packet carries only the fix-hunk windows (380-line file ⇒ one 87-line window)', async () => {
  const { edit, base } = fixture();
  const { spawn, packets } = stubSpawn(() => ({ resolved: [{ id: 'F1', resolved: true, why: 'ok' }] }));
  const deps = { ...base, spawn, review: judgeReview([finding('F1', 200)]), jev: jevResolving(() => new Set()) };
  const state = await runRound(newFileState({ file: FILE, level: 'L2' }), deps);
  edit(200, 'fixed');
  await runRound(state, deps);
  assert.equal(packets.length, 1);
  assert.deepEqual(state.last_packet, { context_mode: 'recheck', context_lines: 87 });
  const context = packets[0].slice(packets[0].indexOf('## context\n'), packets[0].indexOf('## diff\n'));
  assert.equal(context.split('\n').filter((l) => /^\d+\| /.test(l)).length, 87);
  assert.ok(context.includes('### src/a.mjs lines 157-243'));
  assert.equal(packets[0].includes('\n1| export const v1 = 1;\n'), false);
});

test('a new finding outside the fix hunks is a review.late_finding and the round still closes', async () => {
  const { edit, rows, base } = fixture();
  const { spawn } = stubSpawn(() => ({ resolved: [{ id: 'F1', resolved: true, why: 'ok' }], findings: [finding('L1', 5, 'warning')] }));
  const deps = { ...base, spawn, review: judgeReview([finding('F1', 200)]), jev: jevResolving(() => new Set()) };
  const state = await runRound(newFileState({ file: FILE, level: 'L2' }), deps);
  edit(200, 'fixed');
  await runRound(state, deps);
  assert.deepEqual(rows.filter((r) => r.event === 'review.late_finding').map((r) => [r.file, r.finding, r.line_start]), [[FILE, 'L1', 5]]);
  assert.equal(state.status, 'complete');
  assert.deepEqual(state.late.map((f) => f.id), ['L1']);
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').at(-1), {
    event: 'review.round', file: FILE, round: 2, level: 'L2', kind: 'recheck', open_before: 1, closed: 1, new_in_hunks: 0, late: 1, open_after: 0, tokens_in: 900, tokens_out: 300, context_mode: 'recheck',
  });
});

/**
 * Rounds 1–3 on a block at L1: 4 open ⇒ round 2 closes one (3, ladder ⇒ L2) ⇒ round 3 closes
 * `closeAtRound3` of the 3 still open.
 * @param {string[]} closeAtRound3
 */
async function threeRounds(closeAtRound3) {
  const { edit, base } = fixture();
  const { spawn } = stubSpawn();
  let closing = new Set(['F1']);
  const deps = { ...base, cfg: ladderOnly(), spawn, review: judgeReview([finding('F1', 10), finding('F2', 20), finding('F3', 30), finding('F4', 40)]), jev: jevResolving(() => closing) };
  const state = await runRound(newFileState({ file: FILE, level: 'L1' }), deps);
  edit(100, 'r2');
  await runRound(state, deps);
  assert.deepEqual([state.open.length, state.next?.trigger, state.level], [3, 'review_rounds', 'L2']);
  closing = new Set(closeAtRound3);
  edit(101, 'r3');
  await runRound(state, deps);
  return state;
}

test('an open set of 3 → 3 ⇒ review_stall (at L2, the running ceiling, that is the L3 patch rung — R1); 3 → 2 ⇒ no escalation', async () => {
  const stalled = await threeRounds([]);
  assert.deepEqual(stalled.next, { action: 'patch', level: 'L3', trigger: 'review_stall' });
  const shrunk = await threeRounds(['F2']);
  assert.deepEqual(shrunk.next, { action: 'fix', level: 'L2', trigger: null });
});

test('3 → 2 → 1 → 0 converges with exactly 4 review.round rows and one review.approved', async () => {
  const { edit, rows, base } = fixture();
  const { spawn } = stubSpawn();
  let round = 1;
  const order = ['F1', 'F2', 'F3'];
  const deps = {
    ...base,
    cfg: ladderOnly(),
    spawn,
    review: judgeReview([finding('F1', 10), finding('F2', 20), finding('F3', 30)]),
    jev: jevResolving(() => new Set(order.slice(0, round - 1))),
    fix: async (/** @type {{round: number}} */ req) => {
      round = req.round;
      edit(100 + req.round, `r${req.round}`);
    },
  };
  // L1, not L2: an L2 block's +1 at round 2 is the L3 patch rung (R1), never an L3 coding level
  const state = await converge(newFileState({ file: FILE, level: 'L1' }), deps);
  assert.equal(state.status, 'complete');
  const roundRows = rows.filter((r) => r.event === 'review.round');
  assert.equal(roundRows.length, 4);
  assert.deepEqual(roundRows.map((r) => [r.round, r.level, r.kind, r.open_after]), [[1, 'L1', 'full', 3], [2, 'L1', 'recheck', 2], [3, 'L2', 'recheck', 1], [4, 'L2', 'recheck', 0]]);
  assert.equal(rows.filter((r) => r.event === 'review.approved').length, 1);
});

test('round 3 open ⇒ no patch session yet; round 4 open ⇒ exactly one L3 patch session; a patch_check that does not shrink the set ⇒ stopped: l3_patch_exhausted (stall before cap)', async () => {
  const { edit, rows, base } = fixture();
  const { spawn } = stubSpawn();
  let closing = new Set();
  /** @type {Array<Record<string, any>>} */
  const patches = [];
  const deps = {
    ...base,
    cfg: ladderOnly(),
    spawn,
    review: judgeReview([finding('F1', 10), finding('F2', 20), finding('F3', 30)]),
    jev: jevResolving(() => closing),
    patch: async (/** @type {Record<string, any>} */ req) => {
      patches.push(req);
      edit(150, 'patch');
    },
  };
  // L1, not L2: rounds 3–4 run at L2; an L2 block would take the L3 rung at round 2 (R1)
  const state = await runRound(newFileState({ file: FILE, level: 'L1' }), deps);
  for (const [n, close] of /** @type {Array<[number, string[]]>} */ ([[2, ['F1']], [3, ['F1', 'F2']]])) {
    closing = new Set(close);
    edit(100 + n, `r${n}`);
    await runRound(state, deps);
  }
  assert.deepEqual([state.round, state.open.length, state.next?.action, patches.length], [3, 1, 'fix', 0]);

  edit(104, 'r4');
  await converge(state, deps);
  assert.equal(patches.length, 1);
  assert.deepEqual(patches[0].level, 'L3');
  assert.equal(state.status, 'stopped');
  assert.deepEqual(state.next, { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' });
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind]), [[1, 'full'], [2, 'recheck'], [3, 'recheck'], [4, 'recheck'], [4, 'patch_check']]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.cap').map((r) => [r.file, r.reason, r.open]), [[FILE, 'l3_patch_exhausted', ['F3']]]);
});

test('fix 3 (rule 5): the rung reached through the running ceiling at round 2 ⇒ one patch that does not shrink the set ⇒ stopped: l3_patch_exhausted, never back to fix', async () => {
  const { edit, rows, base } = fixture();
  const { spawn } = stubSpawn();
  let fixes = 0;
  /** @type {Array<Record<string, any>>} */
  const patches = [];
  const deps = {
    ...base,
    spawn,
    review: judgeReview([finding('F1', 10)]),
    jev: jevResolving(() => new Set()),
    fix: async (/** @type {{round: number}} */ req) => {
      fixes += 1;
      edit(100 + req.round, `r${req.round}`);
    },
    patch: async (/** @type {Record<string, any>} */ req) => {
      patches.push(req);
      edit(150, 'patch');
    },
  };
  const state = await converge(newFileState({ file: FILE, level: 'L2' }), deps);
  assert.deepEqual([fixes, patches.length, patches[0]?.level, state.round, state.l3_rung_used], [1, 1, 'L3', 2, true]);
  assert.equal(state.status, 'stopped');
  assert.deepEqual(state.next, { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' });
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind, r.open_after]), [[1, 'full', 1], [2, 'recheck', 1], [2, 'patch_check', 1]]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.cap').map((r) => [r.file, r.round, r.reason, r.open]), [[FILE, 2, 'l3_patch_exhausted', ['F1']]]);
  await converge(state, deps);
  assert.deepEqual([fixes, patches.length, state.status], [1, 1, 'stopped']);
});

/**
 * An L2 block whose round 2 stalls (nothing closes) ⇒ the L3 rung; the patch closes F1, and each
 * later fix closes the next id in `afterPatch` (§3.6, Fable ruling A: the block continues at L2).
 * @param {string[]} ids @param {string[]} afterPatch
 */
async function rungThenL2(ids, afterPatch) {
  const { edit, rows, base } = fixture();
  const { spawn } = stubSpawn();
  const closing = new Set();
  /** @type {Array<Record<string, any>>} */
  const fixes = [];
  /** @type {Array<Record<string, any>>} */
  const patches = [];
  const deps = {
    ...base,
    spawn,
    review: judgeReview(ids.map((id, i) => finding(id, 10 * (i + 1)))),
    jev: jevResolving(() => closing),
    fix: async (/** @type {{round: number, level: string, l3_patch: boolean}} */ req) => {
      fixes.push({ round: req.round, level: req.level, l3_patch: req.l3_patch });
      const next = req.l3_patch ? afterPatch.shift() : undefined;
      if (next) closing.add(next);
      edit(100 + req.round, `r${req.round}`);
    },
    patch: async (/** @type {Record<string, any>} */ req) => {
      patches.push(req);
      closing.add('F1');
      edit(150, 'patch');
    },
  };
  const state = await converge(newFileState({ file: FILE, level: 'L2' }), deps);
  return { state, rows, fixes, patches };
}

test('rung at round 2, the patch closes 1 ⇒ fix at L2; round 3 closes the last ⇒ complete (§3.6: the block continues at L2)', async () => {
  const { state, rows, fixes, patches } = await rungThenL2(['F1', 'F2'], ['F2']);
  assert.equal(state.status, 'complete');
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.level, r.kind, r.open_after]), [[1, 'L2', 'full', 2], [2, 'L2', 'recheck', 2], [2, 'L2', 'patch_check', 1], [3, 'L2', 'recheck', 0]]);
  assert.equal(rows.filter((r) => r.event === 'review.approved').length, 1);
  assert.deepEqual(fixes, [{ round: 2, level: 'L2', l3_patch: false }, { round: 3, level: 'L2', l3_patch: true }]);
  assert.equal(patches.length, 1);
});

test('a second stall after the rung (round 3 closes nothing) ⇒ stopped: l3_patch_exhausted, exactly 1 patch, every fix at L2', async () => {
  const { state, rows, fixes, patches } = await rungThenL2(['F1', 'F2', 'F3'], []);
  assert.equal(patches.length, 1);
  assert.equal(state.status, 'stopped');
  assert.deepEqual(state.next, { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' });
  assert.deepEqual(fixes, [{ round: 2, level: 'L2', l3_patch: false }, { round: 3, level: 'L2', l3_patch: true }]);
  assert.deepEqual(fixes.map((f) => f.level), ['L2', 'L2']);
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.level, r.kind, r.open_after]), [[1, 'L2', 'full', 3], [2, 'L2', 'recheck', 3], [2, 'L2', 'patch_check', 2], [3, 'L2', 'recheck', 2]]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.cap').map((r) => [r.round, r.reason, r.open]), [[3, 'l3_patch_exhausted', ['F2', 'F3']]]);
});

test('rung at round 4 (rule 6): the patch shrinks 2 → 1 but the cap is reached ⇒ stopped: review_cap, exactly 1 patch, no round-5 fix', async () => {
  const { edit, rows, base } = fixture();
  const { spawn } = stubSpawn();
  const closing = new Set();
  const order = ['F1', 'F2', 'F3'];
  /** @type {Array<Record<string, any>>} */
  const fixes = [];
  /** @type {Array<Record<string, any>>} */
  const patches = [];
  const deps = {
    ...base,
    cfg: ladderOnly(),
    spawn,
    review: judgeReview(['F1', 'F2', 'F3', 'F4', 'F5'].map((id, i) => finding(id, 10 * (i + 1)))),
    jev: jevResolving(() => closing),
    fix: async (/** @type {{round: number, level: string, l3_patch: boolean}} */ req) => {
      fixes.push({ round: req.round, level: req.level, l3_patch: req.l3_patch });
      closing.add(order[req.round - 2]); // rounds 2–4 close F1, F2, F3: 5 → 4 → 3 → 2
      edit(100 + req.round, `r${req.round}`);
    },
    patch: async (/** @type {Record<string, any>} */ req) => {
      patches.push(req);
      closing.add('F4');
      edit(150, 'patch');
    },
  };
  // L1: round 2 exhausts L1 (rule 2 ⇒ L2); rounds 3–4 shrink at L2; round 4 open ⇒ the rung (rule 6)
  const state = await converge(newFileState({ file: FILE, level: 'L1' }), deps);
  assert.equal(state.status, 'stopped');
  assert.deepEqual(state.next, { action: 'stop', reason: 'review_cap', trigger: 'review_cap' });
  assert.equal(patches.length, 1);
  assert.deepEqual([patches[0].level, patches[0].open.map((/** @type {{id: string}} */ f) => f.id)], ['L3', ['F4', 'F5']]);
  assert.deepEqual(fixes.map((f) => f.round), [2, 3, 4]);
  assert.deepEqual(fixes.map((f) => f.level), ['L1', 'L2', 'L2']);
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.level, r.kind, r.open_after]), [[1, 'L1', 'full', 5], [2, 'L1', 'recheck', 4], [3, 'L2', 'recheck', 3], [4, 'L2', 'recheck', 2], [4, 'L2', 'patch_check', 1]]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.cap').map((r) => [r.round, r.reason, r.open]), [[4, 'review_cap', ['F5']]]);
});

test('rung at round 2, the patch closes 1 ⇒ fix at L2; rounds 3–4 shrink but stay open ⇒ stopped: review_cap with exactly 1 patch', async () => {
  const { state, rows, fixes, patches } = await rungThenL2(['F1', 'F2', 'F3', 'F4'], ['F2', 'F3']);
  assert.equal(state.status, 'stopped');
  assert.deepEqual(state.next, { action: 'stop', reason: 'review_cap', trigger: 'review_cap' });
  assert.equal(state.l3_rung_used, true);
  assert.equal(patches.length, 1);
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.level, r.kind, r.open_after]), [[1, 'L2', 'full', 4], [2, 'L2', 'recheck', 4], [2, 'L2', 'patch_check', 3], [3, 'L2', 'recheck', 2], [4, 'L2', 'recheck', 1]]);
  assert.equal(fixes.length, 3);
  assert.deepEqual(rows.filter((r) => r.event === 'review.cap').map((r) => [r.round, r.reason, r.open]), [[4, 'review_cap', ['F4']]]);
});

test('fix 2: a fix hunk too large for the budget (non-ok recheck packet) is terminal — exactly one review.cap row, no retry, no session', async () => {
  const { edit, rows, base } = fixture();
  const { spawn, packets } = stubSpawn();
  let fixes = 0;
  const deps = {
    ...base,
    cfg: cfgFor({ budgets: { full_in: 40 } }),
    spawn,
    review: judgeReview([finding('F1', 200)]),
    jev: jevResolving(() => new Set()),
    fix: async () => {
      fixes += 1;
      edit(200, 'fixed');
    },
  };
  const state = await converge(newFileState({ file: FILE, level: 'L2' }), deps);
  assert.deepEqual([state.status, state.next, state.pending_kind, state.round, fixes, packets.length], ['stopped', { action: 'stop', reason: 'split_required' }, null, 1, 1, 0]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.cap').map((r) => [r.file, r.round, r.reason, r.open]), [[FILE, 1, 'split_required', ['F1']]]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.kind]), [[1, 'full']]);
  await converge(state, deps);
  assert.deepEqual([state.status, fixes, rows.filter((r) => r.event === 'review.cap').length], ['stopped', 1, 1]);
});

test('decision (B12a follow-up): a recheck packet over full_in falls to `minimal` on the same fix hunk, never the whole file', async () => {
  const { repoRoot, edit, base } = fixture();
  const before = readFileSync(path.join(repoRoot, FILE), 'utf8');
  edit(200, 'fixed');
  const diff = await fixHunkDiff({ file: FILE, previous: before, current: readFileSync(path.join(repoRoot, FILE), 'utf8'), workDir: base.workDir });
  const open = [finding('F1', 200)];
  const roomy = /** @type {any} */ (buildRecheckPacket({ diff, open, cfg: cfgFor() }));
  assert.deepEqual([roomy.status, roomy.contextMode, roomy.contextLines], ['ok', 'recheck', 87]);
  const tight = /** @type {any} */ (buildRecheckPacket({ diff, open, cfg: cfgFor({ budgets: { full_in: roomy.tokensIn - 200 } }) }));
  assert.deepEqual([tight.status, tight.contextMode, tight.contextLines], ['ok', 'minimal', 27]);
});

test('an unavailable recheck session leaves retry; the next converge() re-runs that round (no new fix) and finishes the file', async () => {
  const { edit, rows, base } = fixture();
  const good = stubSpawn(() => ({ resolved: [{ id: 'F1', resolved: true, why: 'ok' }] }));
  let spawns = 0;
  const spawn = async (/** @type {{promptPath: string}} */ o) => {
    spawns += 1;
    return spawns === 1 ? { status: 'failed', exit_code: 1, answer: null, usage: { tokens_in: 0, tokens_out: 0 } } : good.spawn(o);
  };
  let fixes = 0;
  const deps = {
    ...base,
    spawn,
    review: judgeReview([finding('F1', 200)]),
    jev: jevResolving(() => new Set()),
    fix: async () => {
      fixes += 1;
      edit(200, 'fixed');
    },
  };
  const state = await converge(newFileState({ file: FILE, level: 'L2' }), deps);
  assert.deepEqual([state.next, state.pending_kind, state.round, state.status], [{ action: 'retry', reason: 'exit' }, 'recheck', 1, 'open']);
  await converge(state, deps);
  assert.deepEqual([state.status, state.round, spawns, fixes], ['complete', 2, 2, 1]);
  assert.equal(rows.filter((r) => r.event === 'review.approved').length, 1);
});

test('a failing ledger write of review.late_finding never lets the round close as clean', async () => {
  const { edit, rows, base } = fixture();
  const { spawn } = stubSpawn(() => ({ resolved: [{ id: 'F1', resolved: true, why: 'ok' }], findings: [finding('L1', 5, 'warning')] }));
  let failLate = true;
  const writeRow = async (/** @type {Record<string, any>} */ r) => {
    if (failLate && r.event === 'review.late_finding') throw new Error('disk full');
    rows.push(r);
  };
  const deps = { ...base, writeRow, spawn, review: judgeReview([finding('F1', 200)]), jev: jevResolving(() => new Set()), fix: async () => edit(200, 'fixed') };
  const state = await converge(newFileState({ file: FILE, level: 'L2' }), deps);
  assert.deepEqual([state.next, state.status, state.round, state.open.map((f) => f.id), state.late.length], [{ action: 'retry', reason: 'ledger_write_failed' }, 'open', 1, ['F1'], 0]);
  assert.deepEqual(rows.filter((r) => ['review.round', 'review.approved', 'review.late_finding'].includes(r.event)).map((r) => [r.event, r.round ?? null]), [['review.round', 1]]);
  failLate = false;
  await converge(state, deps);
  assert.equal(state.status, 'complete');
  assert.deepEqual(rows.filter((r) => ['review.round', 'review.approved', 'review.late_finding'].includes(r.event)).map((r) => r.event), ['review.round', 'review.late_finding', 'review.round', 'review.approved']);
});

// ── B34 rule 2c: a cheap coder whose round fails with several warnings climbs at once ─────────

/**
 * Round 1 on a block at `level` whose review leaves `findings` open (nothing closes).
 * @param {string} level @param {Array<Record<string, any>>} findings @param {Record<string, any>} [cfg]
 */
async function roundOne(level, findings, cfg = cfgFor()) {
  const { rows, base } = fixture();
  const { spawn } = stubSpawn();
  const deps = { ...base, cfg, spawn, review: judgeReview(findings), jev: jevResolving(() => new Set()) };
  const state = await runRound(newFileState({ file: FILE, level }), deps);
  return { state, rows };
}

test('rule 2c: L1, round 1 leaves 2 warnings open ⇒ the next fix is at L2 (review_warnings), counters reset', async () => {
  const { state, rows } = await roundOne('L1', [finding('W1', 10, 'warning'), finding('W2', 20, 'warning')]);
  assert.deepEqual(state.next, { action: 'fix', level: 'L2', trigger: 'review_warnings' });
  assert.deepEqual([state.level, state.round, state.rounds_at_level, state.warn_rounds_at_level], ['L2', 1, 0, 0]);
  assert.deepEqual(rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.level, r.open_after]), [[1, 'L1', 2]]);
});

test('rule 2c: L0, round 1 leaves 1 critical ⇒ L1; L1 with 1 warning ⇒ no climb (fix at L1, no trigger)', async () => {
  const critical = await roundOne('L0', [finding('C1', 10, 'critical')]);
  assert.deepEqual(critical.state.next, { action: 'fix', level: 'L1', trigger: 'review_warnings' });
  const one = await roundOne('L1', [finding('W1', 10, 'warning')]);
  assert.deepEqual(one.state.next, { action: 'fix', level: 'L1', trigger: null });
  assert.deepEqual([one.state.level, one.state.warn_rounds_at_level], ['L1', 0]);
});

test('rule 2c: L2, round 1 with 2 warnings ⇒ fix at L2 — the L3 rung is not spent on round 1; threshold 3 keeps L1 at L1', async () => {
  const l2 = await roundOne('L2', [finding('W1', 10, 'warning'), finding('W2', 20, 'warning')]);
  assert.deepEqual(l2.state.next, { action: 'fix', level: 'L2', trigger: null });
  assert.equal(l2.state.warn_rounds_at_level, 1);
  const cfg = { ...cfgFor(), escalation: { warning_threshold: 3 } };
  const l1 = await roundOne('L1', [finding('W1', 10, 'warning'), finding('W2', 20, 'warning')], cfg);
  assert.deepEqual(l1.state.next, { action: 'fix', level: 'L1', trigger: null });
});

// ── B34 fix round 1: rule 2c counters, the cap, after_rounds_with_warnings 2 ─────────────────

const warnings = (/** @type {string[]} */ ids) => ids.map((id, i) => finding(id, 10 * (i + 1), 'warning'));

test('rule 2c: a state saved before B34 (no warn_rounds_at_level) counts from 0 and climbs on its first heavy round', async () => {
  const { base } = fixture();
  const { spawn } = stubSpawn();
  const old = /** @type {Record<string, any>} */ (newFileState({ file: FILE, level: 'L1' }));
  delete old.warn_rounds_at_level;
  const deps = { ...base, spawn, review: judgeReview(warnings(['W1', 'W2'])), jev: jevResolving(() => new Set()) };
  const state = await runRound(/** @type {any} */ (old), deps);
  assert.deepEqual(state.next, { action: 'fix', level: 'L2', trigger: 'review_warnings' });
  assert.equal(state.warn_rounds_at_level, 0);
});

test('rule 2c: after_rounds_with_warnings 2 ⇒ round 1 heavy stays at L1, round 2 (still heavy, shrinking 3 → 2) climbs to L2', async () => {
  const { edit, base } = fixture();
  const { spawn } = stubSpawn();
  let closing = new Set();
  const cfg = { ...cfgFor(), escalation: { after_rounds_with_warnings: 2, review_rounds_per_level: 5 } };
  const deps = { ...base, cfg, spawn, review: judgeReview(warnings(['W1', 'W2', 'W3'])), jev: jevResolving(() => closing) };
  const state = await runRound(newFileState({ file: FILE, level: 'L1' }), deps);
  assert.deepEqual([state.next, state.warn_rounds_at_level], [{ action: 'fix', level: 'L1', trigger: null }, 1]);
  closing = new Set(['W1']);
  edit(100, 'r2');
  await runRound(state, deps);
  assert.deepEqual([state.open.length, state.next, state.level, state.warn_rounds_at_level], [2, { action: 'fix', level: 'L2', trigger: 'review_warnings' }, 'L2', 0]);
});

test('the per-file cap wins over rule 2c: a heavy round AT the cap ⇒ the L3 patch rung (review_cap), not review_warnings', async () => {
  const { edit, base } = fixture();
  const { spawn } = stubSpawn();
  let closing = new Set();
  const cfg = { ...cfgFor({ max_rounds_per_file: 2 }), escalation: { after_rounds_with_warnings: 2, review_rounds_per_level: 5 } };
  const deps = { ...base, cfg, spawn, review: judgeReview(warnings(['W1', 'W2', 'W3'])), jev: jevResolving(() => closing) };
  const state = await runRound(newFileState({ file: FILE, level: 'L1' }), deps);
  closing = new Set(['W1']);
  edit(100, 'r2');
  await runRound(state, deps);
  assert.deepEqual([state.round, state.open.length, state.level], [2, 2, 'L1']);
  assert.deepEqual(state.next, { action: 'patch', level: 'L3', trigger: 'review_cap' });
});

test('rule 2c counter resets after the L3 patch: a patch_check round sets warn_rounds_at_level to 0 (no trigger at L2 either)', async () => {
  const { edit, base } = fixture();
  const { spawn } = stubSpawn();
  const deps = { ...base, spawn, review: judgeReview(warnings(['W1', 'W2', 'W3'])), jev: jevResolving(() => new Set(['W1'])) };
  const state = await runRound(newFileState({ file: FILE, level: 'L2' }), deps);
  assert.deepEqual([state.next, state.warn_rounds_at_level], [{ action: 'fix', level: 'L2', trigger: null }, 1]);
  state.l3_rung_used = true;
  edit(150, 'patch');
  await runRound(state, deps, { kind: 'patch_check' });
  assert.deepEqual([state.warn_rounds_at_level, state.rounds_at_level, state.open.length, state.level], [0, 0, 2, 'L2']);
});

test('rule 2c counts only what stays open after triage: 2 warnings that S1 closes in the recheck do not make the round heavy', async () => {
  const { edit, base } = fixture();
  const { spawn } = stubSpawn();
  let closing = new Set();
  const cfg = { ...cfgFor(), escalation: { after_rounds_with_warnings: 2, review_rounds_per_level: 5 } };
  const deps = { ...base, cfg, spawn, review: judgeReview(warnings(['W1', 'W2', 'W3'])), jev: jevResolving(() => closing) };
  const state = await runRound(newFileState({ file: FILE, level: 'L1' }), deps);
  closing = new Set(['W1', 'W2']);
  edit(100, 'r2');
  await runRound(state, deps);
  assert.deepEqual([state.open.length, state.warn_rounds_at_level, state.next], [1, 1, { action: 'fix', level: 'L1', trigger: null }]);
});
