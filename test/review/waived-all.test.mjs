import { commitAll, freshDir, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

// B70 (issue #9 a): a file whose real review (a signed review.round at its current hash) left
// findings that are ALL waived counts as reviewed — `reviewed_by_waiver`, never approved — and the
// close writes one signed `gate.review_waived_all` row for it just before `block.close`. Every
// scenario drives the real `block close` path (temp HOME from helpers.mjs, temp repo, temp run).

const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
const { writeResult } = await import('../../src/worker/queue.mjs');
const { autopilotWaiverProblem, delegableSeverity } = await import('../../src/review/gate-check.mjs');

const FILE = 'src/a.mjs';
const T0 = new Date('2026-10-07T10:00:00.000Z');

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

/**
 * A run with block B1 owning `src/a.mjs`, edited after block open; `sign(row)` writes a signed row
 * of B1 for `src/a.mjs` (the worker's shape: `content_hash` is the current hash unless given).
 * @param {string} slug
 */
async function fixture(slug) {
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeFile(repo, FILE, 'export const a = 2;\n');
  const hash = contentHash(repo, FILE);
  const sign = (/** @type {Record<string, any>} */ row) => writeSigned(runId, writeRow, { block: 'B1', file: FILE, content_hash: hash, ...row });
  /** @param {string[]} extra @returns {Promise<[number, string]>} */
  const close = async (extra = []) => {
    const stream = capture();
    const code = await runBlock(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...extra], { stdout: stream, stderr: stream, probe, now: () => T0 });
    return [code, stream.text];
  };
  const waive = async (/** @type {string} */ finding) => {
    const stream = capture();
    const code = await runBlock(['waive', 'B1', finding, '--run', runId, '--file', FILE, '--reason', 'owner accepted it in chat'], { stdout: stream, stderr: stream, now: () => T0 });
    assert.deepEqual([code, stream.text], [0, `block B1 waived ${finding} in ${FILE} (by: human)\n`]);
  };
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  const waivedAllRows = async () => (await rows()).filter((r) => r.event === 'gate.review_waived_all');
  /** An autopilot grant and an acted delegate decision covering `waive:<severity>` of `finding`, then its waiver. */
  const autopilotWaive = async (/** @type {string} */ finding, /** @type {string} */ severity) => {
    await writeSigned(runId, writeRow, { event: 'autopilot.grant', grant_id: `ap-${finding}`, scopes: ['waive:warning', 'waive:nit'], deny: [], delegate: 'L2', until: '2026-10-07T12:00:00.000Z', ts: '2026-10-07T09:00:00.000Z' });
    await writeSigned(runId, writeRow, { event: 'autopilot.decision', decision_id: `ad-${finding}`, grant_id: `ap-${finding}`, scope: `waive:${severity}`, subject: { block: 'B1', file: FILE, finding }, decision: 'waive', acted: true, ts: '2026-10-07T10:00:00.000Z' });
    await writeSigned(runId, writeRow, { event: 'review.waived', block: 'B1', file: FILE, finding, by: 'autopilot', severity, grant_id: `ap-${finding}`, decision_id: `ad-${finding}`, reason: 'delegate: style only', ts: '2026-10-07T10:05:00.000Z' });
  };
  return { runId, repo, hash, sign, close, waive, rows, waivedAllRows, autopilotWaive, writeRow };
}

test('B70 (1)+(4): a signed round at the current hash whose findings the human all waived closes with one signed gate.review_waived_all row just before block.close; refused closes write none', async () => {
  const f = await fixture('b70-human');
  await f.sign({ event: 'review.round', round: 1, kind: 'full', open_after: 2 });
  await f.sign({ event: 'review.result', ticket: 't1', status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }, { id: 'C1', severity: 'critical' }] });
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs; not_approved src/a.mjs W1; not_approved src/a.mjs C1\n']);
  // (3) one named finding not waived: still unreviewed
  await f.waive('W1');
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs; not_approved src/a.mjs C1\n']);
  await f.waive('C1');
  // (4) the review check passes, a later check refuses: no row is left behind
  const reportDir = freshDir('b70-report');
  writeFile(reportDir, 'report.md', '===BLOCK B1 COMPLETE===\n');
  const report = `${reportDir}/report.md`;
  assert.deepEqual(await f.close(['--report', report]), [1, 'block B1 open: coder report FAILED: no review-file ticket id for src/a.mjs\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
  // (1) the close
  assert.deepEqual(await f.close(), [0, 'block B1: src/a.mjs reviewed_by_waiver (C1, W1; by human)\nblock B1 closed\n']);
  const all = await f.rows();
  const waived = all.filter((r) => r.event === 'gate.review_waived_all');
  assert.equal(waived.length, 1);
  const { mac: _mac, ts: _ts, tokens_source: _t, cost_source: _c, ...fields } = waived[0];
  assert.deepEqual(fields, { run: f.runId, event: 'gate.review_waived_all', block: 'B1', file: FILE, content_hash: f.hash, findings: ['C1', 'W1'], by: 'human' });
  assert.equal(verifyRow(waived[0], await loadKey(f.runId)).ok, true);
  const at = all.indexOf(waived[0]);
  assert.deepEqual(all.slice(at + 1).map((r) => r.event), ['block.close']);
  // never an approval
  assert.equal(all.filter((r) => r.event === 'review.approved').length, 0);
});

test('B70 (2): a file stopped at review_cap with every open id waived closes — by autopilot when a covered autopilot waiver cleared one; --json names it reviewed_by_waiver', async () => {
  const f = await fixture('b70-cap');
  await f.sign({ event: 'review.round', round: 4, kind: 'recheck', open_after: 2 });
  await f.sign({ event: 'review.cap', round: 4, reason: 'review_cap', open: ['W2', 'N3'] });
  await f.sign({ event: 'review.result', ticket: 't2', status: 'stopped', stopped: 'review_cap', approved: false, open: [{ id: 'W2', severity: 'warning' }, { id: 'N3', severity: 'nit' }] });
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs; review_cap src/a.mjs W2; review_cap src/a.mjs N3\n']);
  await f.waive('N3');
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs; review_cap src/a.mjs W2\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
  await f.autopilotWaive('W2', 'warning');
  const [code, out] = await f.close(['--json']);
  assert.equal(code, 0);
  assert.deepEqual(JSON.parse(out), {
    block: 'B1',
    event: 'closed',
    status: 'closed',
    reason: null,
    close_event: null,
    reviewed_by_waiver: [{ file: FILE, content_hash: f.hash, status: 'reviewed_by_waiver', findings: ['N3', 'W2'], by: 'autopilot' }],
  });
  const all = await f.rows();
  const waived = all.filter((r) => r.event === 'gate.review_waived_all');
  assert.deepEqual(waived.map((r) => [r.block, r.file, r.content_hash, r.findings, r.by]), [['B1', FILE, f.hash, ['N3', 'W2'], 'autopilot']]);
  assert.equal(verifyRow(waived[0], await loadKey(f.runId)).ok, true);
  assert.deepEqual(all.slice(all.indexOf(waived[0]) + 1).map((r) => r.event), ['block.close']);
});

test('B70 (3): no signed review.round for the current hash — every finding waived — is still unreviewed', async () => {
  const f = await fixture('b70-noround');
  await f.sign({ event: 'review.round', content_hash: 'b'.repeat(64), round: 1, kind: 'full', open_after: 1 });
  await f.sign({ event: 'review.result', ticket: 't3', status: 'stopped', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  await f.waive('W1');
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
});

test('B70 (3): the review.round at the current hash is not signed — every finding waived by the human — is still unreviewed', async () => {
  const f = await fixture('b70-unsignedround');
  await f.writeRow({ run: f.runId, event: 'review.round', block: 'B1', file: FILE, content_hash: f.hash, round: 1, kind: 'full', open_after: 1 });
  await f.sign({ event: 'review.result', ticket: 't12', status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  await f.waive('W1');
  // an unsigned row naming the file also makes it high tier (closeTier fails closed): unproven too
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs; unproven src/a.mjs\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
});

test('B70 (3): the latest refusal row has no content_hash — still unreviewed', async () => {
  const f = await fixture('b70-nohash');
  await f.sign({ event: 'review.round', round: 1, kind: 'full', open_after: 1 });
  await f.sign({ event: 'review.result', ticket: 't4', status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  await writeSigned(f.runId, f.writeRow, { event: 'review.result', block: 'B1', file: FILE, ticket: 't5', status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  await f.waive('W1');
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
});

test('B70 (3): the latest refusal names no finding — still unreviewed', async () => {
  const f = await fixture('b70-nofinding');
  await f.sign({ event: 'review.round', round: 1, kind: 'full', open_after: 1 });
  await f.sign({ event: 'review.result', ticket: 't6', status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  await f.waive('W1');
  await f.sign({ event: 'review.result', ticket: 't7', status: 'reviewed', approved: false, open: [] });
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
});

test('B70 (3): the latest refusal row is not signed — still unreviewed', async () => {
  const f = await fixture('b70-unsigned');
  await f.sign({ event: 'review.round', round: 1, kind: 'full', open_after: 1 });
  await f.writeRow({ run: f.runId, event: 'review.result', block: 'B1', file: FILE, content_hash: f.hash, ticket: 't8', status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  await f.waive('W1');
  // an unsigned row naming the file also makes it high tier (closeTier fails closed): unproven too
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs; unproven src/a.mjs\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
});

test('B70 (3): a critical waived only by autopilot — still unreviewed only because the refusal records it critical', async () => {
  const f = await fixture('b70-critical');
  await f.sign({ event: 'review.round', round: 1, kind: 'full', open_after: 1 });
  // an old worker's row (no `approved`, no `open`): the critical is recorded only in its signed
  // result file, so the review rows autopilotWaiverProblem reads record C1 as a warning (triage)
  const ticket = ticketId({ run: f.runId, block: 'B1', file: FILE, content_hash: f.hash });
  await f.sign({ event: 'review.result', ticket, status: 'reviewed' });
  const key = await loadKey(f.runId);
  const C1 = { id: 'C1', file: FILE, line_start: 1, line_end: 1, severity: 'critical', category: 'correctness', claim: 'c', evidence: 'e', fix: 'f' };
  writeResult({ repoRoot: f.repo, runId: f.runId, ticket, key, result: { event: 'review.result', block: 'B1', file: FILE, content_hash: f.hash, status: 'reviewed', approved: false, findings: [C1] } });
  await f.sign({ event: 'review.triage', finding: 'C1', severity: 'warning' });
  await f.autopilotWaive('C1', 'warning');
  // the autopilot waiver itself is valid (signed grant, acted decision, recorded warning) ...
  const runRows = await f.rows();
  const waiver = runRows.find((r) => r.event === 'review.waived' && r.by === 'autopilot');
  assert.equal(autopilotWaiverProblem(waiver, { blockRows: runRows.filter((r) => r.block === 'B1'), runRows, key }), null);
  // ... yet both checks refuse it, each only because the latest refusal (its signed result file)
  // records C1 critical, which only the human waives: the stale-findings check names the waiver
  // invalid, and the waived-all check leaves the file unreviewed
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs; autopilot_waiver_invalid src/a.mjs C1\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
  // the human's waiver of the same critical makes it reviewed_by_waiver
  await f.waive('C1');
  assert.deepEqual(await f.close(), [0, 'block B1: src/a.mjs reviewed_by_waiver (C1; by human)\nblock B1 closed\n']);
  assert.deepEqual((await f.waivedAllRows()).map((r) => [r.file, r.findings, r.by]), [[FILE, ['C1'], 'human']]);
});

test('B70: a close where no file needs it writes no gate.review_waived_all row', async () => {
  const f = await fixture('b70-approved');
  await f.sign({ event: 'review.round', round: 1, kind: 'full', open_after: 0 });
  await f.sign({ event: 'review.approved', round: 1 });
  assert.deepEqual(await f.close(), [0, 'block B1 closed\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
});

test('B70 (2)+(3): a review.cap id the latest refusal does not name needs a severity a signed refusal records — else only the human clears it', async () => {
  const f = await fixture('b70-capid');
  await f.sign({ event: 'review.round', round: 4, kind: 'recheck', open_after: 2 });
  await f.sign({ event: 'review.cap', round: 4, reason: 'review_cap', open: ['W2', 'N3'] });
  await f.sign({ event: 'review.result', ticket: 't10', status: 'stopped', stopped: 'review_cap', approved: false, open: [{ id: 'N3', severity: 'nit' }] });
  await f.waive('N3');
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs; review_cap src/a.mjs W2\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
  // a valid autopilot waiver of W2 (triage records it warning) clears the cap check, but no refusal
  // row records W2's severity: it stays the human's, so the file is still unreviewed
  await f.sign({ event: 'review.triage', finding: 'W2', severity: 'warning' });
  await f.autopilotWaive('W2', 'warning');
  assert.deepEqual(await f.close(), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  assert.equal((await f.waivedAllRows()).length, 0);
  await f.waive('W2');
  assert.deepEqual(await f.close(), [0, 'block B1: src/a.mjs reviewed_by_waiver (N3, W2; by human)\nblock B1 closed\n']);
  assert.deepEqual((await f.waivedAllRows()).map((r) => [r.file, r.findings, r.by]), [[FILE, ['N3', 'W2'], 'human']]);
});

// `rest`: what the existing review_cap check also says of the malformed entry (it fails closed too)
for (const [name, open, rest] of /** @type {Array<[string, unknown, string]>} */ ([
  ['a string', 'W1', ''],
  ['an object entry', ['W1', { id: 'W1' }], '; review_cap src/a.mjs [object Object]'],
  ['an empty id', ['W1', ''], '; review_cap src/a.mjs'],
])) {
  test(`B70 (3): a review.cap row whose open is ${name} fails closed — still unreviewed`, async () => {
    const f = await fixture(`b70-badcap-${name.replace(/\W+/g, '-')}`);
    await f.sign({ event: 'review.round', round: 4, kind: 'recheck', open_after: 1 });
    await f.sign({ event: 'review.cap', round: 4, reason: 'review_cap', open });
    await f.sign({ event: 'review.result', ticket: 't11', status: 'stopped', stopped: 'review_cap', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
    await f.waive('W1');
    assert.deepEqual(await f.close(), [1, `block B1 open: unreviewed src/a.mjs${rest}\n`]);
    assert.equal((await f.waivedAllRows()).length, 0);
  });
}

test('B70: delegableSeverity — only an explicit null or warning/nit may be delegated; anything else is the human\'s', () => {
  assert.deepEqual(
    [null, 'warning', 'nit', undefined, 'critical', 'unknown', 'blocker', 5, '', {}].map((s) => delegableSeverity(s)),
    [true, true, true, false, false, false, false, false, false, false],
  );
});
