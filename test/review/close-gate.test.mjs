import { commitAll, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import { chmodSync, mkdirSync, symlinkSync, unlinkSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

// B52 (issue #2 field note): `block close` closed a block whose latest review results said
// `approved: false` with open critical findings. Each path that let a non-approved file through is
// pinned here: (a) a stale approval, (b) an unowned change, (c) `--no-require-reviews` without a
// human at a terminal, (d) triage dropping a critical, (e) a late ruling clearing a critical.

const { checkBlockReviews, openFindings } = await import('../../src/review/gate-check.mjs');
const { triageFindings } = await import('../../src/review/triage.mjs');
const { signRow, loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { readRun, saveRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock, sameStamp, treeAtOpen, treeStamp } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { startGrant } = await import('../../src/autopilot/grant.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
const { writeResult } = await import('../../src/worker/queue.mjs');
const { recordedVerdict } = await import('../../src/worker/loop.mjs');

const KEY = randomBytes(32);
const HASH = 'a'.repeat(64);
const FILE = 'src/a.mjs';
const FILES = [{ file: FILE, content_hash: HASH }];
const signed = (/** @type {Record<string, any>} */ row) => signRow({ run: 'r1', block: 'B1', ...row }, KEY);
const APPROVED = signed({ event: 'review.approved', file: FILE, content_hash: HASH, round: 1 });
const ROUND_OPEN = signed({ event: 'review.round', file: FILE, content_hash: HASH, round: 1, kind: 'full', open_after: 2 });
const RESULT_OPEN = signed({
  event: 'review.result',
  ticket: 't2',
  file: FILE,
  content_hash: HASH,
  status: 'reviewed',
  approved: false,
  open: [{ id: 'C1', severity: 'critical' }, { id: 'W1', severity: 'warning' }],
});
const TRIAGE = [signed({ event: 'review.triage', file: FILE, finding: 'C1', severity: 'critical', verdict: 'fix_now' }), signed({ event: 'review.triage', file: FILE, finding: 'W1', severity: 'warning', verdict: 'fix_now' })];
const check = (/** @type {Array<Record<string, any>>} */ rows, /** @type {Record<string, any>} */ extra = {}) =>
  checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows, key: KEY, proofFiles: FILES, highPaths: [], ...extra }).refusals.map((r) => [r.code, r.file ?? null, r.finding ?? null]);
const human = (/** @type {string} */ finding) => signed({ event: 'review.waived', file: FILE, finding, by: 'human', reason: 'owner said so in chat' });
/** @param {string} id @param {'critical' | 'warning' | 'nit'} severity */
const finding = (id, severity) => ({ id, file: FILE, line_start: 3, line_end: 4, severity, category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

test('B52 (a): an approval followed by a non-approved review of the SAME content is stale — not_approved per open finding', () => {
  assert.deepEqual(check([APPROVED]), []);
  assert.deepEqual(check([APPROVED, ...TRIAGE, ROUND_OPEN, RESULT_OPEN]), [['not_approved', FILE, 'C1'], ['not_approved', FILE, 'W1']]);
  // the order decides: a later approval of the same content stands again
  assert.deepEqual(check([ROUND_OPEN, RESULT_OPEN, APPROVED]), []);
  // a non-approved review of OTHER content never touches this hash's approval
  const { mac: _mac, ...other } = RESULT_OPEN;
  assert.deepEqual(check([APPROVED, signed({ ...other, content_hash: 'b'.repeat(64) })]), []);
  // a result that is not a verdict (unavailable, stale) leaves the approval standing
  assert.deepEqual(check([APPROVED, signed({ event: 'review.result', file: FILE, content_hash: HASH, status: 'unavailable', approved: false })]), []);
});

test('B52 (a): every open finding needs a valid waiver; a critical needs a human one (autopilot waivers never clear it)', () => {
  const rows = [APPROVED, ...TRIAGE, ROUND_OPEN, RESULT_OPEN];
  assert.deepEqual(check([...rows, human('W1')]), [['not_approved', FILE, 'C1']]);
  assert.deepEqual(check([...rows, human('W1'), human('C1')]), []);
  const autopilot = signed({ event: 'review.waived', file: FILE, finding: 'C1', by: 'autopilot', severity: 'critical', grant_id: 'g', decision_id: 'd', ts: '2026-10-07T10:00:00Z', reason: 'r' });
  assert.deepEqual(check([...rows, human('W1'), autopilot]), [['autopilot_waiver_invalid', FILE, 'C1']]);
  // an unsigned human waiver clears nothing
  const { mac: _mac, ...unsigned } = human('C1');
  assert.deepEqual(check([...rows, human('W1'), unsigned]), [['not_approved', FILE, 'C1'], ['unproven', FILE, null]]);
});

test('B52 (a) fix 5: an old ledger — review.result without `approved` — is judged by the ticket\'s signed result file; no verified file ⇒ not_approved', () => {
  const { approved: _a, open: _o, ...legacy } = RESULT_OPEN;
  const legacyRow = signed(legacy); // ticket t2, status reviewed
  // issue #2's shape: triage demoted the critical, so the round closed clean, the approval was written, then the result
  const roundClean = signed({ event: 'review.round', file: FILE, content_hash: HASH, round: 1, kind: 'full', open_after: 0 });
  const rows = [roundClean, APPROVED, legacyRow];
  const fileSays = (/** @type {Record<string, any> | null} */ file) => check(rows, { resultFiles: { t2: file } });
  assert.deepEqual(fileSays({ status: 'reviewed', approved: true, findings: [] }), []);
  assert.deepEqual(fileSays({ status: 'reviewed', approved: false, findings: [finding('C1', 'critical'), finding('W1', 'warning')] }), [['not_approved', FILE, 'C1'], ['not_approved', FILE, 'W1']]);
  // a file that says approved but names a critical is not an approval of that critical
  assert.deepEqual(fileSays({ status: 'reviewed', approved: true, findings: [finding('C1', 'critical'), finding('N1', 'nit')] }), [['not_approved', FILE, 'C1'], ['not_approved', FILE, 'N1']]);
  // missing, MAC failed (null), or a file for another outcome of the ticket: fail closed
  assert.deepEqual(check(rows), [['not_approved', FILE, null]]);
  assert.deepEqual(fileSays(null), [['not_approved', FILE, null]]);
  assert.deepEqual(fileSays({ status: 'unavailable', approved: true, findings: [] }), [['not_approved', FILE, null]]);
  // a stopped result is never an approval, whatever it carries
  assert.deepEqual(check([APPROVED, signed({ event: 'review.result', file: FILE, content_hash: HASH, status: 'stopped', stopped: 'review_cap' })]), [['not_approved', FILE, null]]);
  // a refusal row counts unsigned (fail closed): a forged "not approved" can only refuse
  const { mac: _m, ...unsignedRound } = ROUND_OPEN;
  assert.deepEqual(check([APPROVED, unsignedRound]).filter((r) => r[0] === 'not_approved'), [['not_approved', FILE, null]]);
});

test('B52 fix 8: a later review.result with `approved: false` is stale-making whatever its status, except the outcomes that never reviewed the content', () => {
  const result = (/** @type {Record<string, any>} */ extra) => signed({ event: 'review.result', file: FILE, content_hash: HASH, approved: false, open: [], ...extra });
  assert.deepEqual(check([APPROVED, result({ status: 'refused', reason: 'review-state-tampered' })]), [['not_approved', FILE, null]]);
  assert.deepEqual(check([APPROVED, result({ status: 'no_change' })]), [['not_approved', FILE, null]]);
  assert.deepEqual(check([APPROVED, result({ status: 'split_required' })]), [['not_approved', FILE, null]]);
  assert.deepEqual(check([APPROVED, result({ status: 'stale' })]), []);
  assert.deepEqual(check([APPROVED, result({ status: 'unavailable', reason: 'timeout' })]), []);
  assert.deepEqual(check([APPROVED, result({ status: 'refused', reason: 'other-run' })]), []);
  // a not-approved result that names no finding: only a fresh approval clears it
  assert.deepEqual(check([APPROVED, result({ status: 'reviewed' })]), [['not_approved', FILE, null]]);
  assert.deepEqual(check([APPROVED, result({ status: 'reviewed' }), APPROVED]), []);
});

test('B52 fix 6: the severity the refusal row records wins — an autopilot waiver claiming warning for a recorded critical does not clear it', () => {
  const triageWarning = signed({ event: 'review.triage', file: FILE, finding: 'C1', severity: 'warning', verdict: 'fix_now' });
  const result = signed({ event: 'review.result', ticket: 't3', file: FILE, content_hash: HASH, status: 'reviewed', approved: false, open: [{ id: 'C1', severity: 'critical' }] });
  const autopilot = signed({ event: 'review.waived', file: FILE, finding: 'C1', by: 'autopilot', severity: 'warning', grant_id: 'g', decision_id: 'd', ts: '2026-10-07T10:00:00Z', reason: 'r' });
  const out = checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows: [APPROVED, triageWarning, result, autopilot], key: KEY, proofFiles: FILES, highPaths: [] }).refusals;
  assert.deepEqual(out, [{ code: 'autopilot_waiver_invalid', file: FILE, finding: 'C1', detail: 'the autopilot waiver of C1 in src/a.mjs does not count: the latest review records it critical; only the human waives it' }]);
  assert.deepEqual(check([APPROVED, triageWarning, result, autopilot, human('C1')]), []);
  // the same rule for a cap finding: recordedSeverity reads review.result.open too (worst wins)
  const cap = signed({ event: 'review.cap', file: 'src/b.mjs', round: 4, reason: 'review_cap', open: ['C9'] });
  const rows = [cap, signed({ event: 'review.triage', file: 'src/b.mjs', finding: 'C9', severity: 'warning' }), signed({ event: 'review.result', file: 'src/b.mjs', content_hash: 'c'.repeat(64), status: 'stopped', approved: false, open: [{ id: 'C9', severity: 'critical' }] })];
  const capWaiver = signed({ event: 'review.waived', file: 'src/b.mjs', finding: 'C9', by: 'autopilot', severity: 'warning', grant_id: 'g', decision_id: 'd', ts: '2026-10-07T10:00:00Z', reason: 'r' });
  const capOut = checkBlockReviews({ block: 'B1', runId: 'r1', files: FILES, rows: [APPROVED, ...rows, capWaiver], key: KEY, proofFiles: FILES, highPaths: [] }).refusals;
  assert.deepEqual(capOut.map((r) => [r.code, r.file, r.finding, r.detail]), [
    ['autopilot_waiver_invalid', 'src/b.mjs', 'C9', 'the autopilot waiver of C9 in src/b.mjs does not count: the finding is critical; critical findings are never delegated'],
  ]);
});

test('B52 fix 7 and 9: review_cap stands unless a non-stale approval of the current hash comes AFTER it; a cap row without content_hash counts for every hash', () => {
  const capOther = signed({ event: 'review.cap', file: FILE, content_hash: 'b'.repeat(64), round: 4, reason: 'review_cap', open: ['C1'] });
  assert.deepEqual(check([capOther, APPROVED]), []);
  assert.deepEqual(check([APPROVED, capOther]), [['review_cap', FILE, 'C1']]);
  const capNoHash = signed({ event: 'review.cap', file: FILE, round: 4, reason: 'review_cap', open: ['C1'] });
  assert.deepEqual(check([APPROVED, capNoHash]), [['not_approved', FILE, 'C1']]);
  assert.deepEqual(check([capNoHash, APPROVED]), []);
  const roundNoHash = signed({ event: 'review.round', file: FILE, round: 2, kind: 'recheck', open_after: 1 });
  assert.deepEqual(check([APPROVED, roundNoHash]), [['not_approved', FILE, null]]);
});

test('B52 (e): a review_cap at the current hash AFTER the approval is not cleared by that approval', () => {
  const cap = signed({ event: 'review.cap', file: FILE, content_hash: HASH, round: 4, reason: 'review_cap', open: ['C1'] });
  assert.deepEqual(check([cap, APPROVED]), []);
  assert.deepEqual(check([APPROVED, ...TRIAGE, cap]), [['not_approved', FILE, 'C1']]);
  assert.deepEqual(check([APPROVED, ...TRIAGE, cap, human('C1')]), []);
});

test('B52 (e): a late ruling clears only a non-critical finding ruled nit; fix_now or a critical needs more', () => {
  const late = (/** @type {string} */ severity) => signed({ event: 'review.late_finding', file: FILE, finding: 'L1', severity });
  const ruling = (/** @type {string} */ r) => signed({ event: 'review.late_ruling', file: FILE, finding: 'L1', ruling: r });
  assert.deepEqual(check([APPROVED, late('warning'), ruling('nit')]), []);
  assert.deepEqual(check([APPROVED, late('warning'), ruling('fix_now')]), [['late_unruled', FILE, 'L1']]);
  assert.deepEqual(check([APPROVED, late('critical'), ruling('nit')]), [['late_unruled', FILE, 'L1']]);
  assert.deepEqual(check([APPROVED, late('critical'), ruling('nit'), human('L1')]), []);
});

test('B52 (b): checkBlockReviews refuses each unowned change it is given', () => {
  assert.deepEqual(check([APPROVED], { unowned: ['src/b.mjs', 'z.txt'] }), [['unowned_change', 'src/b.mjs', null], ['unowned_change', 'z.txt', null]]);
});


test('B52 (d): triage never makes a critical a nit — not S1 (low defect p) and not the L3 ruling', async () => {
  const jev = async (/** @type {{state: Record<string, any>}} */ req) => ({ ok: true, answers: { defect: { type: 'noul', noul: { C1: 0.05, C2: 0.6, W1: 0.05 }[/** @type {'C1'} */ (req.state.finding.id)] } } });
  const rule = async () => ({ C2: /** @type {'nit'} */ ('nit') });
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const out = await triageFindings({ file: FILE, findings: [finding('C1', 'critical'), finding('C2', 'critical'), finding('W1', 'warning')], fromJudge: false, jev, rule, writeRow: async (r) => void rows.push(r) });
  assert.deepEqual(out.fix_now.map((f) => f.id), ['C1', 'C2']);
  assert.deepEqual(out.nit.map((f) => f.id), ['W1']);
  assert.deepEqual(rows.map((r) => [r.finding, r.band, r.verdict]), [['C1', 'nit', 'fix_now'], ['W1', 'nit', 'nit'], ['C2', 'judge', 'fix_now']]);
});

/** @returns {{write: (s: string) => boolean, text: string}} */
function capture() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

const T0 = new Date('2026-10-07T10:00:00.000Z');

/**
 * A run with block B1 owning `src/a.mjs` and a committed transcript; `beforeOpen` files are
 * written after the base commit and before `block open` (plan files and the like).
 * @param {string} slug @param {string} runId @param {Record<string, string>} [beforeOpen]
 */
async function closeFixture(slug, runId, beforeOpen = {}) {
  const repo = await makeRepo();
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  writeFile(repo, 'README.md', '# demo\n');
  await commitAll(repo);
  for (const [file, text] of Object.entries(beforeOpen)) writeFile(repo, file, text);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe });
  /** @param {string} id @param {string[]} owned */
  const open = (id, owned) => openBlock({ runId, id, level: 'L2', owned, acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  await open('B1', ['src/a.mjs']);
  /** @param {string[]} extra @param {Record<string, any>} [deps] @param {string} [id] */
  const close = async (extra, deps = {}, id = 'B1') => {
    const stream = capture();
    const code = await runBlock(['close', id, '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...extra], { stdout: stream, stderr: stream, probe, now: () => T0, ...deps });
    return [code, stream.text];
  };
  const approve = async (/** @type {string} */ file, block = 'B1') => writeSigned(runId, writeRow, { event: 'review.approved', block, file, content_hash: contentHash(repo, file), round: 1 });
  return { repo, writeRow, open, close, approve, probe };
}

test('B52 (b) CLI: a file changed or created after block open outside owned_files refuses the close as unowned_change; one an open block of the run owns does not', async () => {
  const { repo, open, close, approve } = await closeFixture('b52-unowned', 'r-b52-unowned');
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(repo, 'src/b.mjs', 'export const b = 1;\n');
  writeFile(repo, 'README.md', '# demo, edited\n'); // a tracked file outside the block changed too
  await approve('src/a.mjs');
  assert.deepEqual(await close([]), [1, 'block B1 open: unowned_change README.md; unowned_change src/b.mjs\n']);
  writeFile(repo, 'README.md', '# demo\n'); // put back: no longer a change
  // a parallel block of the same run owns src/b.mjs: B1 closes on its own files
  await open('B2', ['src/b.mjs']);
  assert.deepEqual(await close([]), [0, 'WARN block B1: src/b.mjs changed and is owned by open block B2; not checked here (its own gate reviews it)\nblock B1 closed\n']);
  await approve('src/b.mjs', 'B2');
  assert.deepEqual(await close([], {}, 'B2'), [0, 'block B2 closed\n']);
});

test('B52 (b) CLI: a file already changed when the block opened (a plan file) is not the coder\'s — until the coder edits it', async () => {
  const { repo, close, approve } = await closeFixture('b52-preopen', 'r-b52-preopen', { 'plans/p.plan.md': '# plan\n', 'notes.txt': 'n\n' });
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  await approve('src/a.mjs');
  writeFile(repo, 'plans/p.plan.md', '# plan, edited by the coder\n');
  assert.deepEqual(await close([]), [1, 'block B1 open: unowned_change plans/p.plan.md\n']);
  writeFile(repo, 'plans/p.plan.md', '# plan\n');
  assert.deepEqual(await close([]), [0, 'block B1 closed\n']);
});

test('B52 (b) CLI: a parallel block that closed covers its file only while the file is the content its gate approved', async () => {
  const { repo, open, close, approve } = await closeFixture('b52-closed', 'r-b52-closed');
  await open('B2', ['src/b.mjs']); // opened alongside B1, before either coder edited anything
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(repo, 'src/b.mjs', 'export const b = 1;\n');
  await approve('src/a.mjs');
  await approve('src/b.mjs', 'B2');
  assert.deepEqual(await close([]), [0, 'WARN block B1: src/b.mjs changed and is owned by open block B2; not checked here (its own gate reviews it)\nblock B1 closed\n']);
  writeFile(repo, 'src/a.mjs', 'export const a = 3;\n'); // B2's coder touched B1's file after B1 closed
  assert.deepEqual(await close([], {}, 'B2'), [1, 'block B2 open: unowned_change src/a.mjs\n']);
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n'); // exactly what B1's gate approved again
  assert.deepEqual(await close([], {}, 'B2'), [0, 'block B2 closed\n']);
});

test('B52 (b) CLI: a block opened before B52 (no tree_at_open) closes with a WARN that the unowned check did not run', async () => {
  const { repo, close, approve } = await closeFixture('b52-old', 'r-b52-old');
  const record = await readRun('r-b52-old');
  delete record.blocks.B1.tree_at_open;
  await saveRun(record);
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(repo, 'src/b.mjs', 'export const b = 1;\n');
  await approve('src/a.mjs');
  assert.deepEqual(await close([]), [0, 'WARN block B1: opened before code-forge recorded the tree at block open; unowned changes were not checked\nblock B1 closed\n']);
});

const ui = (/** @type {unknown} */ answer) => ({ confirm: async () => answer, isCancel: (/** @type {unknown} */ v) => v === CANCEL });
const CANCEL = Symbol('cancel');

test('B52 (c) CLI: --no-require-reviews needs the human at a terminal — no TTY, an active autopilot grant, a cancelled or declined prompt all refuse with nothing written', async () => {
  const { repo, writeRow, close } = await closeFixture('b52-nrr', 'r-b52-nrr');
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  const waivedRows = async () => (await readAllRows('b52-nrr')).filter((r) => r.event === 'gate.reviews_waived').length;

  assert.deepEqual(await close(['--no-require-reviews'], { isTTY: false }), [2, 'block close: --no-require-reviews needs the human at a terminal to confirm; the block stays open (there is no --yes)\n']);
  assert.deepEqual(await close(['--no-require-reviews', '--yes'], { isTTY: true, ui: ui(true) }), [2, 'block close: unknown flag "--yes"\n']);
  assert.deepEqual(await close(['--no-require-reviews'], { isTTY: true, ui: ui(false) }), [1, 'block B1 close: cancelled — the review checks stay on\n']);
  assert.deepEqual(await close(['--no-require-reviews'], { isTTY: true, ui: ui(CANCEL) }), [1, 'block B1 close: cancelled — the review checks stay on\n']);
  assert.equal(await waivedRows(), 0);

  await startGrant({ runId: 'r-b52-nrr', input: { until: '2026-10-07T12:00:00Z', delegate: 'L2', allow: 'waive:nit' }, now: T0, writeRow, grantId: 'ap-b52' });
  assert.deepEqual(await close(['--no-require-reviews'], { isTTY: true, ui: ui(true) }), [1, 'block close: --no-require-reviews is refused while an autopilot grant is active on run r-b52-nrr (stop it first: code-forge autopilot stop --run r-b52-nrr)\n']);
  assert.equal(await waivedRows(), 0);
});

test('B52 (c) CLI: the human confirms at a terminal ⇒ one signed gate.reviews_waived {by: human} row, then the close', async () => {
  const { repo, close } = await closeFixture('b52-nrr-ok', 'r-b52-nrr-ok');
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  /** @type {string[]} */
  const asked = [];
  const prompt = { confirm: async (/** @type {{message: string}} */ o) => (asked.push(o.message), true), isCancel: () => false };
  assert.deepEqual(await close(['--no-require-reviews'], { isTTY: true, ui: prompt }), [0, 'block B1 closed\n']);
  assert.deepEqual(asked, ['Close block B1 of run r-b52-nrr-ok WITHOUT the per-file review checks? Only the owner may say yes.']);
  const rows = await readAllRows('b52-nrr-ok');
  const audit = rows.filter((r) => r.event === 'gate.reviews_waived');
  assert.deepEqual(audit.map((r) => [r.run, r.block, r.by]), [['r-b52-nrr-ok', 'B1', 'human']]);
  assert.equal(verifyRow(audit[0], await loadKey('r-b52-nrr-ok')).ok, true);
});

test('B52 (a) CLI: the field case — approved, then a later review of the same content with open criticals — stays open until a human waives each', async () => {
  const { repo, writeRow, close, approve } = await closeFixture('b52-stale', 'r-b52-stale');
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  await approve('src/a.mjs');
  const hash = contentHash(repo, 'src/a.mjs');
  for (const id of ['C1', 'C2']) await writeSigned('r-b52-stale', writeRow, { event: 'review.triage', block: 'B1', file: 'src/a.mjs', content_hash: hash, finding: id, severity: 'critical', verdict: 'fix_now' });
  await writeSigned('r-b52-stale', writeRow, { event: 'review.round', block: 'B1', file: 'src/a.mjs', content_hash: hash, round: 1, kind: 'full', open_after: 2 });
  await writeSigned('r-b52-stale', writeRow, { event: 'review.result', block: 'B1', file: 'src/a.mjs', content_hash: hash, ticket: 't', status: 'reviewed', approved: false, open: [{ id: 'C1', severity: 'critical' }, { id: 'C2', severity: 'critical' }] });
  assert.deepEqual(await close([]), [1, 'block B1 open: not_approved src/a.mjs C1; not_approved src/a.mjs C2\n']);
  for (const id of ['C1', 'C2']) assert.equal(await runBlock(['waive', 'B1', id, '--run', 'r-b52-stale', '--file', 'src/a.mjs', '--reason', 'owner accepted in chat'], { stdout: capture(), stderr: capture(), now: () => T0 }), 0);
  assert.deepEqual(await close([]), [0, 'block B1 closed\n']);
});

test('B52 (d): the fix loop writes no review.approved for a single reviewer\'s critical that S1 scores as a likely non-defect', async () => {
  const { freshDir, cfgFor, lines } = await import('./helpers.mjs');
  const { newFileState, runRound } = await import('../../src/review/fixloop.mjs');
  const repoRoot = freshDir('b52-fixloop');
  writeFile(repoRoot, FILE, lines(20));
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const deps = {
    repoRoot,
    cfg: cfgFor(),
    workDir: `${freshDir('b52-work')}/w`,
    writeRow: async (/** @type {Record<string, any>} */ r) => void rows.push(r),
    review: async () => ({ status: 'reviewed', engine: 'adaptive', approved: false, findings: [finding('C1', 'critical')], sessions: [{ role: 'reviewer', lens: 'A' }] }),
    jev: async () => ({ ok: true, answers: { defect: { type: 'noul', noul: 0.05 } } }),
  };
  const state = await runRound(newFileState({ file: FILE, level: 'L2' }), deps);
  assert.deepEqual([state.status, state.open.map((f) => f.id)], ['open', ['C1']]);
  assert.equal(rows.filter((r) => r.event === 'review.approved').length, 0);
});

// ── B52 fix round 1 ──

test('B52 fix 1 CLI: a --transcript or --report path in the source tree hides nothing — it is still an unowned change', async () => {
  const { repo, approve, probe } = await closeFixture('b52-input', 'r-b52-input');
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(repo, 'src/evil.mjs', '$ node --test\n');
  writeFile(repo, 'src/report.md', '===BLOCK B1 COMPLETE===\n');
  await approve('src/a.mjs');
  const stream = capture();
  const code = await runBlock(['close', 'B1', '--run', 'r-b52-input', '--worker-pid', '4242', '--transcript', `${repo}/src/evil.mjs`, '--report', `${repo}/src/report.md`], { stdout: stream, stderr: stream, probe, now: () => T0 });
  assert.deepEqual([code, stream.text], [1, 'block B1 open: unowned_change src/evil.mjs; unowned_change src/report.md\n']);
});

test('B52 fix 11 CLI: a file whose stamp at open was unreadable is never "unchanged since open" — it refuses', async () => {
  const { close, approve, repo } = await closeFixture('b52-unread', 'r-b52-unread', { 'plans/p.plan.md': '# plan\n' });
  const record = await readRun('r-b52-unread');
  assert.equal(record.blocks.B1.tree_at_open['plans/p.plan.md'].startsWith('file:-:'), true);
  record.blocks.B1.tree_at_open['plans/p.plan.md'] = 'unreadable';
  await saveRun(record);
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  await approve('src/a.mjs');
  assert.deepEqual(await close([]), [1, 'block B1 open: unowned_change plans/p.plan.md\n']);
});

test('B52 fix 11–13: treeStamp — the executable bit, a symlink target and a nested repo\'s HEAD and changes all change it; a plain directory is `unreadable`, which never matches', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'run.sh', 'echo hi\n');
  const sha = (/** @type {string} */ text) => createHash('sha256').update(text).digest('hex');
  assert.equal(await treeStamp(repo, 'run.sh'), `file:-:${sha('echo hi\n')}`);
  chmodSync(path.join(repo, 'run.sh'), 0o755);
  assert.equal(await treeStamp(repo, 'run.sh'), `file:x:${sha('echo hi\n')}`);
  symlinkSync('run.sh', path.join(repo, 'link'));
  assert.equal(await treeStamp(repo, 'link'), `link:${sha('run.sh')}`);
  unlinkSync(path.join(repo, 'link'));
  symlinkSync('other.sh', path.join(repo, 'link'));
  assert.equal(await treeStamp(repo, 'link'), `link:${sha('other.sh')}`);
  assert.equal(await treeStamp(repo, 'gone.txt'), 'deleted');
  mkdirSync(path.join(repo, 'plain'));
  assert.equal(await treeStamp(repo, 'plain'), 'unreadable');
  const nested = path.join(repo, 'vendor', 'lib');
  mkdirSync(nested, { recursive: true });
  await git(['init', '-q'], nested);
  writeFile(nested, 'x.txt', 'x\n');
  await git(['add', 'x.txt'], nested);
  await git(['commit', '-q', '-m', 'one'], nested);
  const head = (await git(['rev-parse', 'HEAD'], nested)).trim();
  // round 2 item 7: status, the binary diff against HEAD, and each untracked file's path and sha256
  assert.equal(await treeStamp(repo, 'vendor/lib'), `repo:${head}:${sha('\0diff\0')}`);
  writeFile(nested, 'x.txt', 'x changed\n');
  const diff1 = await git(['diff', 'HEAD', '--binary'], nested);
  assert.equal(await treeStamp(repo, 'vendor/lib'), `repo:${head}:${sha(` M x.txt\0\0diff\0${diff1}`)}`);
  writeFile(nested, 'x.txt', 'x changed again\n'); // same status line, different content
  const diff2 = await git(['diff', 'HEAD', '--binary'], nested);
  assert.notEqual(diff1, diff2);
  assert.equal(await treeStamp(repo, 'vendor/lib'), `repo:${head}:${sha(` M x.txt\0\0diff\0${diff2}`)}`);
  writeFile(nested, 'new.txt', 'n\n');
  assert.equal(await treeStamp(repo, 'vendor/lib'), `repo:${head}:${sha(` M x.txt\0?? new.txt\0\0diff\0${diff2}\0new.txt\0${sha('n\n')}`)}`);
  assert.deepEqual([sameStamp('unreadable', 'unreadable'), sameStamp('deleted', 'deleted'), sameStamp(undefined, undefined)], [false, true, false]);
});

test('B52 fix 14: treeAtOpen lists and stamps from the repository top level — a workspace in a subdirectory gets repo-root-relative paths', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'sub/keep.txt', 'k\n');
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  writeFile(repo, 'top.txt', 't\n');
  writeFile(repo, 'sub/new.txt', 'n\n');
  const sha = (/** @type {string} */ text) => createHash('sha256').update(text).digest('hex');
  assert.deepEqual(await treeAtOpen(path.join(repo, 'sub'), base), { 'sub/new.txt': `file:-:${sha('n\n')}`, 'top.txt': `file:-:${sha('t\n')}` });
});

test('B52 fix 15–16: openFindings keeps every finding — a missing or malformed id becomes X<n> by position; a repeated id keeps its worst severity', () => {
  assert.deepEqual(
    openFindings([
      { id: 'W1', severity: 'warning' },
      { severity: 'critical' },
      { id: '../bad id', severity: 'nit' },
      { id: 'W1', severity: 'critical' },
      { id: 'W1', severity: 'nit' },
      { id: 'Q1', severity: 'blocker' },
    ]),
    [
      { id: 'W1', severity: 'critical' },
      { id: 'X2', severity: 'critical' },
      { id: 'X3', severity: 'nit' },
      { id: 'Q1', severity: 'unknown' },
    ],
  );
  assert.deepEqual(openFindings(undefined), []);
  // a not-approved result that names nothing is recorded with open: [] — the gate refuses it until a fresh approval
  assert.deepEqual(recordedVerdict({ status: 'reviewed', approved: false }), { status: 'reviewed', approved: false, open: [] });
  assert.deepEqual(check([APPROVED, signed({ event: 'review.result', file: FILE, content_hash: HASH, status: 'reviewed', approved: false, open: [] })]), [['not_approved', FILE, null]]);
  // a synthetic id is waivable like any other; an unknown severity only by the human
  assert.deepEqual(check([APPROVED, signed({ event: 'review.result', file: FILE, content_hash: HASH, status: 'reviewed', approved: false, open: [{ id: 'X2', severity: 'warning' }] }), human('X2')]), []);
  const unknownSeverity = signed({ event: 'review.result', file: FILE, content_hash: HASH, status: 'reviewed', approved: false, open: [{ id: 'Q1', severity: 'unknown' }] });
  assert.deepEqual(check([APPROVED, unknownSeverity]), [['not_approved', FILE, 'Q1']]);
  assert.deepEqual(check([APPROVED, unknownSeverity, human('Q1')]), []);
});

test('B52 fix 17: a result that says approved but lists a critical is recorded approved: false with that critical open', () => {
  assert.deepEqual(recordedVerdict({ status: 'reviewed', approved: true, findings: [{ id: 'N1', severity: 'nit' }, { id: 'C1', severity: 'critical' }] }), {
    status: 'reviewed',
    approved: false,
    findings: [{ id: 'N1', severity: 'nit' }, { id: 'C1', severity: 'critical' }],
    open: [{ id: 'N1', severity: 'nit' }, { id: 'C1', severity: 'critical' }],
  });
  assert.deepEqual(recordedVerdict({ status: 'reviewed', approved: true, findings: [{ id: 'N1', severity: 'nit' }] }), { status: 'reviewed', approved: true, findings: [{ id: 'N1', severity: 'nit' }], open: [] });
  assert.deepEqual(recordedVerdict({ status: 'stopped', approved: false, findings: [{ id: 'W1', severity: 'warning' }] }).open, [{ id: 'W1', severity: 'warning' }]);
});

test('B52 fix 5 CLI: an old-ledger review.result is judged by its signed result file — approved:false refuses with its ids, a tampered file refuses, a signed approval passes', async () => {
  const runId = 'r-b52-legacy';
  const { repo, writeRow, close, approve } = await closeFixture('b52-legacy', runId);
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  const hash = contentHash(repo, 'src/a.mjs');
  const ticket = ticketId({ run: runId, block: 'B1', file: 'src/a.mjs', content_hash: hash });
  await writeSigned(runId, writeRow, { event: 'review.round', block: 'B1', file: 'src/a.mjs', content_hash: hash, round: 1, kind: 'full', open_after: 0 });
  await approve('src/a.mjs');
  // the row an old worker wrote: no `approved`, no `open`
  await writeSigned(runId, writeRow, { event: 'review.result', ticket, block: 'B1', file: 'src/a.mjs', content_hash: hash, status: 'reviewed' });
  const key = await loadKey(runId);
  const base = { event: 'review.result', block: 'B1', file: 'src/a.mjs', content_hash: hash, status: 'reviewed' };
  writeResult({ repoRoot: repo, runId, ticket, key, result: { ...base, approved: false, findings: [finding('C1', 'critical')] } });
  assert.deepEqual(await close([]), [1, 'block B1 open: not_approved src/a.mjs C1\n']);
  writeFile(repo, `.code-forge/reviews/${runId}/${ticket}.json`, `${JSON.stringify({ ...base, run: runId, ticket, approved: true, findings: [], mac: 'f'.repeat(64) })}\n`);
  assert.deepEqual(await close([]), [1, 'block B1 open: not_approved src/a.mjs\n']);
  writeResult({ repoRoot: repo, runId, ticket, key, result: { ...base, approved: true, findings: [] } });
  assert.deepEqual(await close([]), [0, 'block B1 closed\n']);
});

test('B52 item 18c/19: ownsPath — a directory entry owns the paths below it (never a name that only shares its prefix); reviewedFiles lists the block\'s reviewed paths', async () => {
  const { ownsPath, reviewedFiles } = await import('../../src/review/gate-check.mjs');
  const owned = ['src/feature', 'test/feature/', 'lib/*.mjs'];
  assert.deepEqual(
    ['src/feature/Fan.swift', 'src/feature/deep/A.swift', 'src/featureX/B.swift', 'test/feature/T.swift', 'lib/a.mjs', 'lib/sub/a.mjs', 'src/feature'].map((f) => ownsPath(owned, f)),
    [true, true, false, true, true, false, true],
  );
  const rows = [
    { event: 'review.triage', block: 'B1', file: 'src/b.mjs' },
    { event: 'review.result', block: 'B1', file: './src/a.mjs' },
    { event: 'review.approved', block: 'B2', file: 'src/c.mjs' },
    { event: 'review.late_finding', block: 'B1', file: 'src/d.mjs' },
    { event: 'review.round', block: 'B1', file: '../escape.mjs' },
    { event: 'review.round', block: 'B1', file: 'src/b.mjs' },
  ];
  assert.deepEqual(reviewedFiles(rows, 'B1'), ['src/a.mjs', 'src/b.mjs', 'src/d.mjs']);
});

// ── B52 fix round 2 ──

test('B52 round 2 item 1: a workspace in a subdirectory — one path base (the repo top): owned files (workspace-relative) pass, an unowned file refuses, old-ledger result files are found where the worker writes them', async () => {
  const top = await makeRepo();
  writeFile(top, 'app/src/a.mjs', 'export const a = 1;\n');
  writeFile(top, 'other.txt', 'o\n');
  writeFile(top, 'app/coder.log', '$ node --test\n');
  await commitAll(top);
  const ws = path.join(top, 'app');
  const runId = 'r-b52-subdir';
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'b52-subdir' });
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  await startRun({ workspace: ws, project: 'b52-subdir', runId, workerPid: 4242, writeRow, probe });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  const close = async () => {
    const stream = capture();
    const code = await runBlock(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(ws, 'coder.log')], { stdout: stream, stderr: stream, probe, now: () => T0 });
    return [code, stream.text];
  };
  writeFile(top, 'app/src/a.mjs', 'export const a = 2;\n');
  const hash = contentHash(top, 'app/src/a.mjs');
  // the worker signs repo-root-relative paths
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'app/src/a.mjs', content_hash: hash, round: 1 });
  assert.deepEqual(await close(), [0, 'block B1 closed\n']);

  // a second block: an unowned file inside and outside the workspace, and an old-ledger result
  const run2 = 'r-b52-subdir2';
  await startRun({ workspace: ws, project: 'b52-subdir', runId: run2, workerPid: 4242, writeRow, probe });
  await openBlock({ runId: run2, id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeFile(top, 'app/src/a.mjs', 'export const a = 3;\n');
  writeFile(top, 'app/src/b.mjs', 'export const b = 1;\n');
  writeFile(top, 'other.txt', 'o changed\n');
  const hash3 = contentHash(top, 'app/src/a.mjs');
  const ticket = ticketId({ run: run2, block: 'B1', file: 'app/src/a.mjs', content_hash: hash3 });
  await writeSigned(run2, writeRow, { event: 'review.approved', block: 'B1', file: 'app/src/a.mjs', content_hash: hash3, round: 1 });
  await writeSigned(run2, writeRow, { event: 'review.result', ticket, block: 'B1', file: 'app/src/a.mjs', content_hash: hash3, status: 'reviewed' });
  writeResult({ repoRoot: top, runId: run2, ticket, key: await loadKey(run2), result: { event: 'review.result', block: 'B1', file: 'app/src/a.mjs', content_hash: hash3, status: 'reviewed', approved: false, findings: [finding('C1', 'critical')] } });
  const stream = capture();
  const code = await runBlock(['close', 'B1', '--run', run2, '--worker-pid', '4242', '--transcript', path.join(ws, 'coder.log')], { stdout: stream, stderr: stream, probe, now: () => T0 });
  assert.deepEqual([code, stream.text], [1, 'block B1 open: unowned_change app/src/b.mjs; unowned_change other.txt; not_approved app/src/a.mjs C1\n']);
});

test('B52 round 2 item 5: a file with only a review.cap row (and no diff) is still checked', async () => {
  const { close, writeRow } = await closeFixture('b52-caponly', 'r-b52-caponly');
  await writeSigned('r-b52-caponly', writeRow, { event: 'review.cap', block: 'B1', file: 'src/a.mjs', round: 4, reason: 'review_cap', open: ['C1'] });
  const noDiff = 'WARN block B1: no owned file differs from the block base in the workspace, but 1 owned file(s) have review rows — the work was likely committed before block open or done in another worktree; they are checked at their content in the workspace\n';
  assert.deepEqual(await close([]), [1, `${noDiff}block B1 open: unreviewed src/a.mjs; review_cap src/a.mjs C1\n`]);
});

test('B52 round 2 item 6: a current review.result with approved: true whose open names a critical is a refusal, with every finding it lists', () => {
  const lying = signed({ event: 'review.result', file: FILE, content_hash: HASH, status: 'reviewed', approved: true, open: [{ id: 'C1', severity: 'critical' }, { id: 'N1', severity: 'nit' }] });
  assert.deepEqual(check([APPROVED, lying]), [['not_approved', FILE, 'C1'], ['not_approved', FILE, 'N1']]);
  const honest = signed({ event: 'review.result', file: FILE, content_hash: HASH, status: 'reviewed', approved: true, open: [] });
  assert.deepEqual(check([APPROVED, honest]), []);
});

test('B52 round 2 item 7 CLI: a nested repo already dirty at open and edited again after open refuses unowned_change', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const nested = path.join(repo, 'vendor', 'lib');
  mkdirSync(nested, { recursive: true });
  await git(['init', '-q'], nested);
  writeFile(nested, 'x.txt', 'x\n');
  await git(['add', 'x.txt'], nested);
  await git(['commit', '-q', '-m', 'one'], nested);
  writeFile(nested, 'x.txt', 'x dirty at open\n');
  const runId = 'r-b52-nested';
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'b52-nested' });
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  await startRun({ workspace: repo, project: 'b52-nested', runId, workerPid: 4242, writeRow, probe });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  assert.deepEqual(Object.keys((await readRun(runId)).blocks.B1.tree_at_open), ['.code-forge/runs/r-b52-nested.json', 'vendor/lib/']);
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'src/a.mjs', content_hash: contentHash(repo, 'src/a.mjs'), round: 1 });
  const close = async () => {
    const stream = capture();
    const code = await runBlock(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`], { stdout: stream, stderr: stream, probe, now: () => T0 });
    return [code, stream.text];
  };
  writeFile(nested, 'x.txt', 'x edited after open\n'); // the status line ( M x.txt) is the same as at open
  assert.deepEqual(await close(), [1, 'block B1 open: unowned_change vendor/lib/\n']);
  writeFile(nested, 'x.txt', 'x dirty at open\n');
  assert.deepEqual(await close(), [0, 'block B1 closed\n']);
});

test('B52 round 2 item 8: a directory with no .git that the index records as a gitlink stamps as gitlink:<commit>', async () => {
  const repo = await makeRepo();
  const commit = (await git(['rev-parse', 'HEAD'], repo)).trim();
  await git(['update-index', '--add', '--cacheinfo', `160000,${commit},mods/sub`], repo);
  mkdirSync(path.join(repo, 'mods', 'sub'), { recursive: true });
  assert.equal(await treeStamp(repo, 'mods/sub'), `gitlink:${commit}`);
  mkdirSync(path.join(repo, 'mods', 'plain'));
  assert.equal(await treeStamp(repo, 'mods/plain'), 'unreadable');
});

// ── B52 fix round 3 ──

test('B52 round 3 item 1: with a workspace in app/, only the top-level .code-forge/ is exempt — app/.code-forge/x refuses unowned_change', async () => {
  const top = await makeRepo();
  writeFile(top, 'app/src/a.mjs', 'export const a = 1;\n');
  writeFile(top, 'app/coder.log', '$ node --test\n');
  await commitAll(top);
  const ws = path.join(top, 'app');
  const runId = 'r-b52-statedir';
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'b52-statedir' });
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  await startRun({ workspace: ws, project: 'b52-statedir', runId, workerPid: 4242, writeRow, probe });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeFile(top, 'app/src/a.mjs', 'export const a = 2;\n');
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'app/src/a.mjs', content_hash: contentHash(top, 'app/src/a.mjs'), round: 1 });
  writeFile(top, '.code-forge/x', 'worker state\n');
  writeFile(top, 'app/.code-forge/x', 'not code-forge state\n');
  const close = async () => {
    const stream = capture();
    const code = await runBlock(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(ws, 'coder.log')], { stdout: stream, stderr: stream, probe, now: () => T0 });
    return [code, stream.text];
  };
  // the run mirror (app/.code-forge/runs/<run>.json) is code-forge's own file and stays exempt
  assert.deepEqual(await close(), [1, 'block B1 open: unowned_change app/.code-forge/x\n']);
  unlinkSync(path.join(top, 'app', '.code-forge', 'x'));
  assert.deepEqual(await close(), [0, 'block B1 closed\n']);
});

test('B52 round 3 item 2: --no-require-reviews needs BOTH stdin and stdout to be terminals', async () => {
  const { repo, close } = await closeFixture('b52-tty', 'r-b52-tty');
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  const noTTY = [2, 'block close: --no-require-reviews needs the human at a terminal to confirm; the block stays open (there is no --yes)\n'];
  assert.deepEqual(await close(['--no-require-reviews'], { stdinIsTTY: true, stdoutIsTTY: false, ui: ui(true) }), noTTY);
  assert.deepEqual(await close(['--no-require-reviews'], { stdinIsTTY: false, stdoutIsTTY: true, ui: ui(true) }), noTTY);
  assert.equal((await readAllRows('b52-tty')).filter((r) => r.event === 'gate.reviews_waived').length, 0);
  assert.deepEqual(await close(['--no-require-reviews'], { stdinIsTTY: true, stdoutIsTTY: true, ui: ui(true) }), [0, 'block B1 closed\n']);
});

test('B52 round 3 item 3: a block that is not open is refused the same way with or without --no-require-reviews, and the owner is never asked', async () => {
  const { repo, close, approve } = await closeFixture('b52-notopen', 'r-b52-notopen');
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  await approve('src/a.mjs');
  assert.deepEqual(await close([]), [0, 'block B1 closed\n']);
  let asked = 0;
  const counting = { confirm: async () => ((asked += 1), true), isCancel: () => false };
  const refused = [1, 'block close: block B1 is not open in run r-b52-notopen\n'];
  assert.deepEqual(await close([]), refused);
  assert.deepEqual(await close(['--no-require-reviews'], { isTTY: true, ui: counting }), refused);
  assert.equal(asked, 0);
});
