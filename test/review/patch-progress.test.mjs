// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { cfgFor, answerFor, freshDir, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { converge, newFileState } = await import('../../src/review/fixloop.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { queueDir } = await import('../../src/worker/queue.mjs');
const { STOP_EXITS } = await import('../../src/review/stop-exits.mjs');
const { checkBlockReviews } = await import('../../src/review/gate-check.mjs');

const FILE = 'src/a.mjs';
const EXITS = ['block reopen', 'block waive', 'review-file --answers', 're-decompose'];

/** @param {string} id @param {number} line */
const finding = (id, line) => ({ id, file: FILE, line_start: line, line_end: line, severity: 'critical', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  const end = listed.findIndex((l) => !l.startsWith('- @@'));
  return listed.slice(0, end).map((l) => l.slice(2));
}

/**
 * An L2 file through converge(): round 1 opens `ids`, round 2 closes nothing (a stall at the
 * running ceiling ⇒ the L3 rung, R1). The patch closes `patchCloses` and its patch_check session
 * reports `patchFinds` (inside the patch's fix hunk, line 150). Each later L2 fix closes the next
 * id of `afterPatch` (none left ⇒ it closes nothing). `cfg` overrides the config.
 * @param {{ids: string[], patchCloses: string[], patchFinds: string[], afterPatch?: string[], cfg?: Record<string, any>, startLevel?: string, fixCloses?: (round: number) => string | undefined}} opts
 */
async function run({ ids, patchCloses, patchFinds, afterPatch = [], cfg = cfgFor(), startLevel = 'L2', fixCloses }) {
  const repoRoot = freshDir('pp');
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
  const closing = new Set();
  /** @type {string[]} the findings the NEXT recheck session reports (the patch_check's) */
  let reportNext = [];
  /** @type {Array<{round: number, level: string, l3_patch: boolean}>} */
  const fixes = [];
  /** @type {Array<Record<string, any> | null>} the state's `next` each time a fix is asked for */
  const nexts = [];
  const state = newFileState({ file: FILE, level: startLevel });
  let patches = 0;
  const spawn = async (/** @type {{promptPath: string}} */ o) => {
    const text = readFileSync(o.promptPath, 'utf8');
    const found = reportNext.map((id) => finding(id, 150));
    reportNext = [];
    return { status: 'ok', exit_code: 0, answer: answerFor(hunksOf(text), { findings: found }), usage: { tokens_in: 900, tokens_out: 300 } };
  };
  const deps = {
    repoRoot,
    cfg,
    workDir: path.join(freshDir('work'), 'w'),
    writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r),
    spawn,
    review: async () => ({ status: 'reviewed', engine: 'adaptive', depth: 'dual', findings: ids.map((id, i) => finding(id, 10 * (i + 1))), sessions: [{ role: 'judge', lens: 'judge' }] }),
    jev: async (/** @type {{state: Record<string, any>}} */ req) => ({ ok: true, answers: { resolved: { type: 'noul', noul: closing.has(req.state.finding.id) ? 0.95 : 0.2 } } }),
    fix: async (/** @type {{round: number, level: string, l3_patch: boolean}} */ req) => {
      fixes.push({ round: req.round, level: req.level, l3_patch: req.l3_patch });
      nexts.push(structuredClone(state.next));
      const next = fixCloses ? fixCloses(req.round) : req.l3_patch ? afterPatch.shift() : undefined;
      if (next) closing.add(next);
      edit(100 + req.round, `r${req.round}`);
    },
    patch: async () => {
      patches += 1;
      for (const id of patchCloses) closing.add(id);
      reportNext = [...patchFinds];
      edit(150, 'patch');
    },
  };
  await converge(state, deps);
  const roundsOf = () => rows.filter((r) => r.event === 'review.round').map((r) => [r.round, r.level, r.kind, r.closed, r.open_after]);
  const capsOf = () => rows.filter((r) => r.event === 'review.cap').map((r) => [r.round, r.reason, r.open]);
  return { state, rows, fixes, nexts, patches: () => patches, roundsOf, capsOf };
}

test('B73 (1): a patch_check that closes 1 old finding and finds 1 new one in its fix hunk (2 → 2), below the cap ⇒ fix at L2, not l3_patch_exhausted; the block then converges with exactly 1 patch', async () => {
  const r = await run({ ids: ['F1', 'F2'], patchCloses: ['F1'], patchFinds: ['N1'], afterPatch: ['F2', 'N1'] });
  // the patch_check row: 1 closed, open stays 2 — progress, the next fix is at L2 with the patch in the brief
  assert.deepEqual(r.roundsOf().slice(0, 3), [
    [1, 'L2', 'full', 0, 2],
    [2, 'L2', 'recheck', 0, 2],
    [2, 'L2', 'patch_check', 1, 2],
  ]);
  assert.deepEqual(r.fixes.slice(0, 2), [{ round: 2, level: 'L2', l3_patch: false }, { round: 3, level: 'L2', l3_patch: true }]);
  assert.equal(r.patches(), 1);
  assert.equal(r.state.l3_rung_used, true);
  assert.equal(r.state.status, 'complete');
  assert.deepEqual(r.capsOf(), []);
});

test('B73 (1): the decision right after a progressing patch_check (3 → 4) is exactly {action: fix, level: L2, trigger: null}; a later L2 stall stops l3_patch_exhausted with no second patch', async () => {
  const r = await run({ ids: ['F1', 'F2', 'F3'], patchCloses: ['F1'], patchFinds: ['N1', 'N2'] });
  // 3 open before the patch, F1 closed, N1 and N2 new in the hunk ⇒ 4 open after: still progress
  assert.deepEqual(r.roundsOf()[2], [2, 'L2', 'patch_check', 1, 4]);
  assert.deepEqual(r.fixes[1], { round: 3, level: 'L2', l3_patch: true });
  // fix 1 follows round 1; fix 2 follows the patch_check
  assert.deepEqual(r.nexts, [{ action: 'fix', level: 'L2', trigger: null }, { action: 'fix', level: 'L2', trigger: null }]);
  // round 3 then closes nothing at L2: the normal L2 stall rule, the rung is spent — no second patch
  assert.equal(r.patches(), 1);
  assert.deepEqual(r.state.next, { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' });
  assert.deepEqual(r.capsOf(), [[3, 'l3_patch_exhausted', ['F2', 'F3', 'N1', 'N2']]]);
});

test('B73 (2): a patch_check that closes nothing (and finds 1 new) still stops l3_patch_exhausted, exactly 1 patch, no fix after it', async () => {
  const r = await run({ ids: ['F1', 'F2'], patchCloses: [], patchFinds: ['N1'] });
  assert.equal(r.state.status, 'stopped');
  assert.deepEqual(r.state.next, { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' });
  assert.deepEqual(r.roundsOf(), [
    [1, 'L2', 'full', 0, 2],
    [2, 'L2', 'recheck', 0, 2],
    [2, 'L2', 'patch_check', 0, 3],
  ]);
  assert.deepEqual(r.capsOf(), [[2, 'l3_patch_exhausted', ['F1', 'F2', 'N1']]]);
  assert.deepEqual([r.patches(), r.fixes.length], [1, 1]);
});

test('B73 (2): ids, not counts — a new finding that reuses the closed id F1 is not progress ⇒ l3_patch_exhausted', async () => {
  const r = await run({ ids: ['F1', 'F2'], patchCloses: ['F1'], patchFinds: ['F1'] });
  assert.deepEqual(r.roundsOf()[2], [2, 'L2', 'patch_check', 1, 2]);
  assert.deepEqual(r.state.next, { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' });
  assert.deepEqual(r.capsOf(), [[2, 'l3_patch_exhausted', ['F2', 'F1']]]);
});

test('B73 (2): at the cap a patch_check that closes 1 old finding stops review_cap; one that closes nothing stops l3_patch_exhausted', async () => {
  // an L1 block: rounds 2-4 each close one (5 → 4 → 3 → 2), round 4 open ⇒ the rung at the cap (rule 6)
  const order = ['F1', 'F2', 'F3'];
  const cfg = { ...cfgFor(), escalation: { after_rounds_with_warnings: 0 } };
  const ids = ['F1', 'F2', 'F3', 'F4', 'F5'];
  const closes = await run({ ids, patchCloses: ['F4'], patchFinds: ['N1'], cfg, startLevel: 'L1', fixCloses: (round) => order[round - 2] });
  assert.equal(closes.state.status, 'stopped');
  assert.deepEqual(closes.state.next, { action: 'stop', reason: 'review_cap', trigger: 'review_cap' });
  assert.deepEqual(closes.roundsOf().at(-1), [4, 'L2', 'patch_check', 1, 2]);
  assert.deepEqual(closes.capsOf(), [[4, 'review_cap', ['F5', 'N1']]]);
  assert.deepEqual([closes.patches(), closes.fixes.length], [1, 3]);

  const nothing = await run({ ids, patchCloses: [], patchFinds: [], cfg, startLevel: 'L1', fixCloses: (round) => order[round - 2] });
  assert.deepEqual(nothing.state.next, { action: 'stop', reason: 'l3_patch_exhausted', trigger: 'review_stall' });
  assert.deepEqual(nothing.capsOf(), [[4, 'l3_patch_exhausted', ['F4', 'F5']]]);
  assert.deepEqual([nothing.patches(), nothing.fixes.length], [1, 3]);
});

/**
 * A repo whose queue holds a done ticket with `result`; `review-file --wait` on it.
 * @param {Record<string, any>} result
 */
async function waitOn(result) {
  const repo = await makeRepo();
  const ticket = 'ab'.repeat(12);
  const rel = path.join('.code-forge', 'reviews', `${ticket}.json`);
  mkdirSync(path.join(repo, '.code-forge', 'reviews'), { recursive: true });
  writeFileSync(path.join(repo, rel), `${JSON.stringify(result)}\n`);
  mkdirSync(queueDir(repo), { recursive: true });
  writeFileSync(path.join(queueDir(repo), `${ticket}.done`), `${JSON.stringify({ result: rel })}\n`);
  let out = '';
  const code = await runReviewFile(['--wait', ticket, '--max', '1s'], { cwd: repo, pollMs: 10, stdout: { write: (/** @type {string} */ s) => (out += s) }, stderr: { write: () => true } });
  const answers = out.trim().split('\n');
  return { code, answers: answers.length, json: JSON.parse(answers[0]) };
}

test('B73 (3): a stopped answer from review-file --wait carries exits in order; an approved or a refused (tampered) answer carries none', async () => {
  assert.deepEqual([...STOP_EXITS], EXITS);
  const stopped = await waitOn({ status: 'stopped', stopped: 'l3_patch_exhausted', approved: false, findings: [finding('F2', 20)] });
  assert.deepEqual([stopped.code, stopped.answers, stopped.json.status, stopped.json.stop], [0, 1, 'done', 'l3_patch_exhausted']);
  assert.deepEqual(stopped.json.exits, EXITS);
  assert.deepEqual(stopped.json.fix_list.map((/** @type {{id: string}} */ f) => f.id), ['F2']);
  const cap = await waitOn({ status: 'stopped', stopped: 'review_cap', approved: false, findings: [] });
  assert.deepEqual([cap.json.stop, cap.json.exits], ['review_cap', EXITS]);

  const tampered = await waitOn({ status: 'refused', reason: 'review-state-tampered', approved: false, engine: 'adaptive', sessions: [] });
  assert.deepEqual([tampered.code, tampered.json.status, Object.hasOwn(tampered.json, 'exits'), Object.hasOwn(tampered.json, 'stop')], [0, 'done', false, false]);
  const approved = await waitOn({ status: 'reviewed', approved: true, findings: [] });
  assert.equal(Object.hasOwn(approved.json, 'exits'), false);
});

test('B73 (exits at the gate): a stopped file\'s review_cap and unreviewed details end with the exits line; a file with no cap row does not', () => {
  const LINE = 'exits: block reopen, block waive, review-file --answers, re-decompose';
  const files = [
    { file: FILE, content_hash: 'a'.repeat(64) },
    { file: 'src/b.mjs', content_hash: 'b'.repeat(64) },
  ];
  const rows = [{ event: 'review.cap', block: 'B1', run: 'r1', file: FILE, round: 4, reason: 'review_cap', open: ['F1'] }];
  const out = checkBlockReviews({ block: 'B1', runId: 'r1', files, rows, key: null, proofFiles: [], highPaths: [] }).refusals;
  assert.deepEqual(out.map((r) => [r.code, r.file, r.finding ?? null, r.detail]), [
    ['unreviewed', FILE, null, `no review.approved row for ${FILE} at its current content; ${LINE}`],
    ['unreviewed', 'src/b.mjs', null, 'no review.approved row for src/b.mjs at its current content'],
    ['review_cap', FILE, 'F1', `${FILE} stopped at review_cap; finding F1 is open and not waived; ${LINE}`],
  ]);
});

test('B73 (exits at the gate): a file whose review state is tampered (the worker\'s refused review-state-tampered result row) gets exactly the plain unreviewed detail, never the exits line', () => {
  const files = [{ file: FILE, content_hash: 'a'.repeat(64) }];
  const rows = [{ event: 'review.result', block: 'B1', run: 'r1', file: FILE, content_hash: 'a'.repeat(64), status: 'refused', reason: 'review-state-tampered', approved: false }];
  const out = checkBlockReviews({ block: 'B1', runId: 'r1', files, rows, key: null, proofFiles: [], highPaths: [] }).refusals;
  assert.deepEqual(out.map((r) => [r.code, r.file, r.finding ?? null, r.detail]), [['unreviewed', FILE, null, `no review.approved row for ${FILE} at its current content`]]);
  assert.equal(out.filter((r) => String(r.detail).includes('exits:')).length, 0);
});
