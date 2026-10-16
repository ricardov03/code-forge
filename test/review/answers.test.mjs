import { commitAll, freshDir, makeRepo, writeFile } from './helpers.mjs';
import { humanSaysYes } from '../helpers/human-confirm.mjs';
import assert from 'node:assert/strict';
import { existsSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

// B71 (issue #9 b): `review-file <answering file> --block <id> --answers <ids> --of <source>` asks
// one fresh L2 reviewer session whether the answering file resolves open findings of the source.
// A resolved id is a signed `review.answered` row the close counts (through the B70 rule) until
// either file changes. Every scenario drives the real `review-file` CLI, an in-process worker (a
// stub session spawner, never a real CLI) and the real `block close` path, in a temp HOME and a
// temp repo.

const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { processStartTime, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { contentHash, readRegularFileNoFollow, ticketId } = await import('../../src/worker/ticket.mjs');
const { announceWorker, answersTicketId, assertAnswers, enqueue, enqueueAnswers, queueDir, readResult } = await import('../../src/worker/queue.mjs');
const { SHORT_MARKER, TRUNCATED_MARKER, reviewAnswers } = await import('../../src/review/answers.mjs');
const { clearSecrets, registerSecret } = await import('../../src/util/redact.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

const SRC = 'src/a.mjs';
const ANS = 'test/a.test.mjs';
const T0 = new Date('2026-10-12T10:00:00.000Z');
const SRC_V2 = 'export const a = 2;\nexport function add(x, y) {\n  return x - y;\n}\n';
const ANS_V1 = "import { add } from '../src/a.mjs';\nif (add(1, 1) !== 2) throw new Error('add');\n";

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

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
}

/**
 * A run with block B1 owning `src/a.mjs` and `test/a.test.mjs` (both changed after block open), an
 * in-process worker on a stub spawner and an announced live worker for `review-file`. A round-1
 * session reports `script.round1`; an answers session answers `script.answer` (a function of the
 * packet text) or fails with `script.fail`.
 * @param {string} slug
 */
async function fixture(slug) {
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, SRC, 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  // B107: pin the in-process worker whose identity is signed into the results.
  const probe = processStartTime;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, writeRow, probe });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [SRC, ANS], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeFile(repo, SRC, SRC_V2);
  writeFile(repo, ANS, ANS_V1);
  const key = await loadKey(runId);
  /** @type {{round1: Array<Record<string, any>>, answer: (text: string) => unknown, fail: Record<string, any> | null, failAnsweredWrites: Set<number>, answeredWrites: number, throwSession: boolean, during: (() => void) | null}} */
  const script = { round1: [], answer: () => ({ verdicts: [] }), fail: null, failAnsweredWrites: new Set(), answeredWrites: 0, throwSession: false, during: null };
  // the worker's ledger writer: the n-th review.answered write (from 1) throws when n is in failAnsweredWrites
  const workerWriteRow = async (/** @type {Record<string, any>} */ row) => {
    if (row.event === 'review.answered') {
      script.answeredWrites += 1;
      if (script.failAnsweredWrites.has(script.answeredWrites)) throw new Error('FAKE ledger write failure');
    }
    return writeRow(row);
  };
  /** @type {Array<{lens: string, level: string, role: string, text: string}>} */
  const sessions = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    const lens = opts.rowExtra?.lens;
    sessions.push({ lens, level: opts.level, role: opts.role, text });
    if (lens === 'answers') {
      if (script.throwSession) throw new Error('FAKE adapter crash');
      script.during?.();
      if (script.fail) return script.fail;
      return { status: 'ok', exit_code: 0, answer: script.answer(text), usage: { tokens_in: 900, tokens_out: 300 } };
    }
    const answer = { passed: false, summary: 's', reviewed_hunks: hunksOf(text), findings: script.round1, resolved: [], needs_file: [] };
    return { status: 'ok', exit_code: 0, answer, usage: { tokens_in: 900, tokens_out: 300 } };
  };
  const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
    const [id] = Object.keys(req.questions);
    return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
  };
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: {}, runRootDir: freshDir('runroot'), slug, key },
    { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow: workerWriteRow, readRows: () => readAllRows(slug), spawn: /** @type {any} */ (spawn), jev },
  );
  announceWorker(repo, { pid: process.pid, run: runId });
  /** @param {string[]} args @returns {Promise<{code: number, out: Record<string, any> | null, err: string}>} */
  const reviewFile = async (args) => {
    const stdout = capture();
    const stderr = capture();
    const code = await runReviewFile(args, { cwd: repo, stdout, stderr });
    return { code, out: stdout.text === '' ? null : JSON.parse(stdout.text), err: stderr.text };
  };
  /** @param {string[]} ids @returns {Promise<{code: number, out: Record<string, any> | null, err: string}>} */
  const answers = (ids, by = ANS, of = SRC) => reviewFile([by, '--block', 'B1', '--answers', ids.join(','), '--of', of]);
  const drain = () => worker.drain();
  const hashOf = (/** @type {string} */ file) => contentHash(repo, file);
  /** A signed worker-shaped row of B1 for `file` at its current hash unless `content_hash` is given. */
  const sign = (/** @type {Record<string, any>} */ row, file = SRC) => writeSigned(runId, writeRow, { block: 'B1', file, content_hash: hashOf(file), ...row });
  /** @param {string[]} extra @returns {Promise<[number, string]>} */
  const close = async (extra = []) => {
    const stream = capture();
    const code = await runBlock(['close', 'B1', '--run', runId, '--worker-pid', String(process.pid), '--transcript', `${repo}/coder.log`, ...extra], { stdout: stream, stderr: stream, probe, now: () => T0 });
    return [code, stream.text];
  };
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  const answeredRows = async () => (await rows()).filter((r) => r.event === 'review.answered');
  const queued = () => readdirSync(queueDir(repo)).filter((n) => n.endsWith('.json') && n !== 'worker.json').sort();
  /** B96: the close's reviewed lines for both owned files at their current hash, sorted by path */
  const reviewed = () => [SRC, ANS].map((file) => `reviewed ${file} ticket ${ticketId({ run: runId, block: 'B1', file, content_hash: hashOf(file) })}\n`).join('');
  return { runId, repo, key, script, sessions, reviewFile, answers, drain, hashOf, sign, close, rows, answeredRows, queued, writeRow, reviewed };
}

/** Round 1 of the source through the real worker: one open finding C1 of `severity` at line 3. */
async function reviewSource(/** @type {Awaited<ReturnType<typeof fixture>>} */ f, severity = 'warning') {
  f.script.round1 = [{ id: 'C1', file: SRC, line_start: 3, line_end: 3, severity, category: 'correctness', claim: 'add subtracts', evidence: 'return x - y', fix: 'return x + y' }];
  const r = await f.reviewFile([SRC, '--block', 'B1']);
  assert.equal(r.code, 0);
  assert.equal(await f.drain(), 1);
  const result = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, r.out?.ticket));
  assert.deepEqual([result.status, result.approved, result.findings.map((/** @type {any} */ x) => x.id)], ['reviewed', false, ['C1']]);
}

/** Signed rows of a real review of the source at its current hash that left `open` open. */
async function signedReview(/** @type {Awaited<ReturnType<typeof fixture>>} */ f, /** @type {Array<{id: string, severity: string}>} */ open, ticket = 't1') {
  await f.sign({ event: 'review.round', round: 1, kind: 'full', open_after: open.length });
  await f.sign({ event: 'review.result', ticket, status: 'reviewed', approved: false, open });
}

const resolvedAll = (/** @type {string} */ text) => ({ verdicts: [...text.matchAll(/^## Finding (\S+)$/gm)].map((m) => ({ id: m[1], verdict: 'resolved', reason: 'the test now pins add' })) });

test('B71 (1): a resolved answer writes one signed review.answered row; the source closes reviewed_by_waiver with C1 under answered', async () => {
  const f = await fixture('b71-resolved');
  await reviewSource(f);
  f.script.answer = resolvedAll;
  const srcHash = f.hashOf(SRC);
  const ansHash = f.hashOf(ANS);
  const q = await f.answers(['C1']);
  assert.equal(q.code, 0);
  assert.deepEqual({ ...q.out, ticket: typeof q.out?.ticket }, { ticket: 'string', status: 'queued', file: SRC, content_hash: srcHash, by_file: ANS, by_hash: ansHash, answers: ['C1'], kind: 'answers' });
  assert.equal(await f.drain(), 1);
  // one fresh L2 reviewer session; its packet has the finding, the source lines and the new file's content
  const s = f.sessions.filter((x) => x.lens === 'answers');
  assert.equal(s.length, 1);
  assert.deepEqual([s[0].level, s[0].role], ['L2', 'reviewer']);
  for (const part of ['## Finding C1', `- file: ${SRC}`, '- lines: 3-3', '- severity: warning', '- claim: add subtracts', '- evidence: return x - y', '- fix: return x + y', '3:   return x - y;', `## The change in ${ANS}`, '(a new file: its full content)', "if (add(1, 1) !== 2) throw new Error('add');"]) {
    assert.equal(s[0].text.includes(part), true, part);
  }
  const result = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, q.out?.ticket));
  assert.deepEqual([result.status, result.approved, result.kind, result.findings], ['answers', false, 'answers', [{ id: 'C1', severity: 'warning', verdict: 'resolved', reason: 'the test now pins add' }]]);
  const rows = await f.answeredRows();
  assert.equal(rows.length, 1);
  assert.equal(verifyRow(rows[0], f.key).ok, true);
  const { mac: _m, ts: _t, tokens_source: _ts, cost_source: _cs, ...fields } = rows[0];
  assert.deepEqual(fields, { run: f.runId, event: 'review.answered', block: 'B1', file: SRC, content_hash: srcHash, finding: 'C1', by_file: ANS, by_hash: ansHash, ticket: q.out?.ticket });
  // the coder's --wait sees the verdicts, not a fix list
  const w = await f.reviewFile(['--wait', q.out?.ticket]);
  assert.deepEqual([w.code, w.out?.status, w.out?.answers, w.out?.fix_list], [0, 'done', [{ id: 'C1', verdict: 'resolved', reason: 'the test now pins add' }], undefined]);
  // the answering file still needs its own approval; the source no longer refuses
  assert.deepEqual(await f.close(), [1, `block B1 open: unreviewed ${ANS}\n`]);
  await f.sign({ event: 'review.approved' }, ANS);
  assert.deepEqual(await f.close(), [0, `${f.reviewed()}block B1: ${SRC} reviewed_by_waiver (answered C1)\nblock B1 closed\n`]);
  const all = await f.rows();
  const waived = all.filter((r) => r.event === 'gate.review_waived_all');
  assert.deepEqual(waived.map((r) => [r.file, r.content_hash, r.findings, r.by, r.answered]), [[SRC, srcHash, [], null, ['C1']]]);
  assert.equal(verifyRow(waived[0], f.key).ok, true);
  assert.deepEqual(all.slice(all.indexOf(waived[0]) + 1).map((r) => r.event), ['block.close']);
  assert.equal(all.filter((r) => r.event === 'review.approved' && r.file === SRC).length, 0);
});

test('B71 (2): not_resolved, no verdict, a broken answer or a failed session writes no row and the source stays unreviewed', async () => {
  const f = await fixture('b71-not');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }, { id: 'W2', severity: 'warning' }, { id: 'W3', severity: 'warning' }, { id: 'N4', severity: 'nit' }]);
  await f.sign({ event: 'review.approved' }, ANS);
  const refusedText = `block B1 open: unreviewed ${SRC}; not_approved ${SRC} W1; not_approved ${SRC} W2; not_approved ${SRC} W3; not_approved ${SRC} N4\n`;
  // W1 not_resolved, W2 missing, W3 given twice (resolved and not), N4 a broken entry
  f.script.answer = () => ({
    verdicts: [
      { id: 'W1', verdict: 'not_resolved', reason: 'the test does not cover it' },
      { id: 'W3', verdict: 'resolved', reason: 'r' },
      { id: 'W3', verdict: 'not_resolved', reason: 'r' },
      { id: 'N4', verdict: 'fixed', reason: 'r' },
    ],
  });
  const q = await f.answers(['W1', 'W2', 'W3', 'N4']);
  assert.equal(q.code, 0);
  assert.equal(await f.drain(), 1);
  const result = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, q.out?.ticket));
  assert.deepEqual(
    result.findings.map((/** @type {any} */ x) => [x.id, x.verdict]),
    [['N4', 'not_resolved'], ['W1', 'not_resolved'], ['W2', 'not_resolved'], ['W3', 'not_resolved']],
  );
  assert.equal((await f.answeredRows()).length, 0);
  assert.deepEqual(await f.close(), [1, refusedText]);
  // an answer that is not JSON at all: no row
  writeFile(f.repo, ANS, `${ANS_V1}// v2\n`);
  await f.sign({ event: 'review.approved' }, ANS);
  f.script.answer = () => 'nope';
  assert.equal((await f.answers(['W1'])).code, 0);
  assert.equal(await f.drain(), 1);
  assert.equal((await f.answeredRows()).length, 0);
  // a failed session is unavailable: no row, and the same content is a fresh attempt (retry)
  writeFile(f.repo, ANS, `${ANS_V1}// v3\n`);
  await f.sign({ event: 'review.approved' }, ANS);
  f.script.fail = { status: 'exit', exit_code: 1, answer: null };
  const failed = await f.answers(['W1']);
  assert.equal(await f.drain(), 1);
  const r = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, failed.out?.ticket));
  assert.deepEqual([r.status, r.reason, r.approved], ['unavailable', 'exit', false]);
  assert.equal((await f.answeredRows()).length, 0);
  f.script.fail = null;
  f.script.answer = resolvedAll;
  const again = await f.answers(['W1']);
  assert.deepEqual([again.code, again.out?.ticket, again.out?.retry, again.out?.status], [0, failed.out?.ticket, true, 'queued']);
  assert.equal(await f.drain(), 1);
  assert.deepEqual((await f.answeredRows()).map((x) => x.finding), ['W1']);
  assert.deepEqual(await f.close(), [1, `block B1 open: unreviewed ${SRC}; not_approved ${SRC} W2; not_approved ${SRC} W3; not_approved ${SRC} N4\n`]);
});

test('B71 (3): refusals — not owned, same file, not open, more than 10 ids, no --of — queue nothing; the worker refuses the same with signed rows', async () => {
  const f = await fixture('b71-refuse');
  await signedReview(f, [{ id: 'C1', severity: 'critical' }, { id: 'C2', severity: 'warning' }]);
  writeFile(f.repo, 'other.mjs', 'export {};\n');
  const before = f.queued();
  const notOwned = await f.answers(['C1'], 'other.mjs');
  assert.deepEqual([notOwned.code, notOwned.out?.status, notOwned.out?.reason], [1, 'refused', 'answers-not-owned']);
  // a source (--of) the block does not own: refused the same way
  const srcNotOwned = await f.answers(['C1'], ANS, 'other.mjs');
  assert.deepEqual([srcNotOwned.code, srcNotOwned.out?.status, srcNotOwned.out?.reason], [1, 'refused', 'answers-not-owned']);
  const repeated = await f.answers(['C1', 'C1']);
  assert.deepEqual([repeated.code, repeated.out], [2, null]);
  assert.equal(repeated.err.startsWith('review-file: --answers names a finding id twice\n'), true);
  const same = await f.answers(['C1'], SRC);
  assert.deepEqual([same.code, same.out?.status, same.out?.reason], [1, 'refused', 'answers-same-file']);
  const notOpen = await f.answers(['C1', 'Z9']);
  assert.deepEqual([notOpen.code, notOpen.out?.status, notOpen.out?.reason], [1, 'refused', 'answers-not-open']);
  const eleven = await f.answers(Array.from({ length: 11 }, (_, i) => `C${i + 1}`));
  assert.deepEqual([eleven.code, eleven.out], [2, null]);
  assert.equal(eleven.err.startsWith('review-file: --answers takes at most 10 finding ids\n'), true);
  const noOf = await f.reviewFile([ANS, '--block', 'B1', '--answers', 'C1']);
  assert.deepEqual([noOf.code, noOf.out], [2, null]);
  const badId = await f.answers(['C1', '']);
  assert.deepEqual([badId.code, badId.out], [2, null]);
  assert.deepEqual(f.queued(), before);
  // ten ids is the limit, not a refusal of its shape (the open check refuses the unknown nine)
  const ten = await f.answers(['C1', ...Array.from({ length: 9 }, (_, i) => `W${i + 1}`)]);
  assert.deepEqual([ten.code, ten.out?.reason], [1, 'answers-not-open']);
  // the worker's check is the one that counts: tickets written past the CLI are refused, no session
  const notOpenTicket = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: ANS, answers: ['Z9'] });
  const notOwnedTicket = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: 'other.mjs', answers: ['C1'] });
  const srcNotOwnedTicket = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: 'other.mjs', byFile: ANS, answers: ['C1'] });
  assert.equal(await f.drain(), 3);
  for (const [t, reason] of /** @type {const} */ ([[notOpenTicket, 'answers-not-open'], [notOwnedTicket, 'answers-not-owned'], [srcNotOwnedTicket, 'answers-not-owned']])) {
    const r = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, t.ticket));
    assert.deepEqual([r.status, r.reason, r.approved], ['answers', reason, false]);
  }
  assert.equal(f.sessions.length, 0);
  assert.equal((await f.answeredRows()).length, 0);
  // an id already answered is no longer open
  f.script.answer = resolvedAll;
  assert.equal((await f.answers(['C1'])).code, 0);
  assert.equal(await f.drain(), 1);
  assert.equal((await f.answeredRows()).length, 1);
  writeFile(f.repo, ANS, `${ANS_V1}// v2\n`);
  const twice = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: ANS, answers: ['C1'] });
  // the old answer is stale for the new answering content, so C1 is open again and may be answered
  assert.equal(await f.drain(), 1);
  assert.equal(/** @type {Record<string, any>} */ (readResult(f.repo, f.runId, twice.ticket)).status, 'answers');
  assert.equal((await f.answeredRows()).length, 2);
  // another request naming the answered C1 is refused; the same request again is its done ticket
  const third = await f.answers(['C1', 'C2']);
  assert.deepEqual([third.code, third.out?.reason], [1, 'answers-not-open']);
  const again = await f.answers(['C1']);
  assert.deepEqual([again.code, again.out?.ticket, again.out?.status], [0, twice.ticket, 'done']);
  assert.equal(await f.drain(), 0);
  assert.equal((await f.answeredRows()).length, 2);
});

test('B71 (4): a change to the source or to the answering file makes the answer stale', async () => {
  const f = await fixture('b71-stale');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  await f.sign({ event: 'review.approved' }, ANS);
  f.script.answer = resolvedAll;
  // the answering file moves between enqueue and the worker: stale, no session, no row
  const moved = await f.answers(['W1']);
  writeFile(f.repo, ANS, `${ANS_V1}// moved\n`);
  assert.equal(await f.drain(), 1);
  const r = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, moved.out?.ticket));
  assert.deepEqual([r.status, r.reason], ['stale', 'answering-file-changed']);
  // the source moves: stale too
  const srcMoved = await f.answers(['W1']);
  const keep = readFileSync(path.join(f.repo, SRC), 'utf8');
  writeFile(f.repo, SRC, `${keep}// moved\n`);
  assert.equal(await f.drain(), 1);
  const sm = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, srcMoved.out?.ticket));
  assert.deepEqual([sm.status, sm.reason], ['stale', 'source-changed']);
  writeFile(f.repo, SRC, keep);
  // the answering file is gone before the worker runs: answers-file-missing
  const ansKeep = readFileSync(path.join(f.repo, ANS), 'utf8');
  writeFile(f.repo, ANS, `${ANS_V1}// gone next\n`);
  const gone = await f.answers(['W1']);
  assert.equal(gone.out?.status, 'queued');
  rmSync(path.join(f.repo, ANS));
  assert.equal(await f.drain(), 1);
  const g = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, gone.out?.ticket));
  assert.deepEqual([g.status, g.reason], ['answers', 'answers-file-missing']);
  writeFile(f.repo, ANS, ansKeep);
  assert.equal(f.sessions.length, 0);
  assert.equal((await f.answeredRows()).length, 0);
  // a real answer (new answering content: the stale ticket of the same content is final, like any other)
  writeFile(f.repo, ANS, `${ANS_V1}// v3\n`);
  await f.sign({ event: 'review.approved' }, ANS);
  assert.equal((await f.answers(['W1'])).code, 0);
  assert.equal(await f.drain(), 1);
  assert.equal((await f.answeredRows()).length, 1);
  const [firstCode, firstOut] = await f.close(['--json']);
  assert.equal(firstCode, 0);
  assert.deepEqual(JSON.parse(firstOut).reviewed_by_waiver, [{ file: SRC, content_hash: f.hashOf(SRC), status: 'reviewed_by_waiver', findings: [], by: null, answered: ['W1'] }]);
  // (a) the answering file changes after the answer (and is approved again): the answer is stale
  const f2 = await fixture('b71-stale-ans');
  await signedReview(f2, [{ id: 'W1', severity: 'warning' }]);
  f2.script.answer = resolvedAll;
  const ansBefore = f2.hashOf(ANS);
  await f2.answers(['W1']);
  assert.equal(await f2.drain(), 1);
  // the answer is there and signed, bound to the answering file's hash at the time
  const a2 = await f2.answeredRows();
  assert.deepEqual(a2.map((r) => [r.finding, r.by_hash, verifyRow(r, f2.key).ok]), [['W1', ansBefore, true]]);
  writeFile(f2.repo, ANS, `${ANS_V1}// later\n`);
  await f2.sign({ event: 'review.approved' }, ANS);
  assert.deepEqual(await f2.close(), [1, `block B1 open: unreviewed ${SRC}; not_approved ${SRC} W1\n`]);
  // the control: the same steps without the change close
  const c2 = await fixture('b71-stale-ctl');
  await signedReview(c2, [{ id: 'W1', severity: 'warning' }]);
  c2.script.answer = resolvedAll;
  await c2.answers(['W1']);
  assert.equal(await c2.drain(), 1);
  await c2.sign({ event: 'review.approved' }, ANS);
  assert.deepEqual(await c2.close(), [0, `${c2.reviewed()}block B1: ${SRC} reviewed_by_waiver (answered W1)\nblock B1 closed\n`]);
  // (b) the source changes after the answer and is reviewed again with W1 open: the old answer does not count
  const f3 = await fixture('b71-stale-src');
  await signedReview(f3, [{ id: 'W1', severity: 'warning' }]);
  await f3.sign({ event: 'review.approved' }, ANS);
  f3.script.answer = resolvedAll;
  await f3.answers(['W1']);
  assert.equal(await f3.drain(), 1);
  assert.equal((await f3.answeredRows()).length, 1);
  writeFile(f3.repo, SRC, `${SRC_V2}// later\n`);
  await signedReview(f3, [{ id: 'W1', severity: 'warning' }], 't2');
  assert.deepEqual(await f3.close(), [1, `block B1 open: unreviewed ${SRC}; not_approved ${SRC} W1\n`]);
});

test('B71 (5): an unsigned review.answered row or one with a bad MAC clears nothing; the same row signed does', async () => {
  /** @param {string} slug */
  const setup = async (slug) => {
    const f = await fixture(slug);
    await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
    await f.sign({ event: 'review.approved' }, ANS);
    const body = { run: f.runId, event: 'review.answered', block: 'B1', file: SRC, content_hash: f.hashOf(SRC), finding: 'W1', by_file: ANS, by_hash: f.hashOf(ANS), ticket: 'a'.repeat(24) };
    return { f, body };
  };
  // unsigned: not counted (and the file's unsigned row leaves it unproven too)
  const u = await setup('b71-unsigned');
  await u.f.writeRow(u.body);
  assert.deepEqual(await u.f.close(), [1, `block B1 open: unreviewed ${SRC}; not_approved ${SRC} W1; unproven ${SRC}\n`]);
  // a bad MAC: not counted
  const b = await setup('b71-badmac');
  const signed = signRow(b.body, b.f.key);
  const flipped = `${signed.mac.slice(0, -1)}${signed.mac.endsWith('0') ? '1' : '0'}`;
  await b.f.writeRow({ ...signed, mac: flipped });
  assert.deepEqual(await b.f.close(), [1, `block B1 open: unreviewed ${SRC}; not_approved ${SRC} W1; unproven ${SRC}\n`]);
  // signed, but naming the source as its own answering file: not counted
  const s = await setup('b71-selfans');
  await writeSigned(s.f.runId, s.f.writeRow, { ...s.body, by_file: SRC, by_hash: s.f.hashOf(SRC) });
  assert.deepEqual(await s.f.close(), [1, `block B1 open: unreviewed ${SRC}; not_approved ${SRC} W1\n`]);
  // the same row, signed: the source closes
  await writeSigned(s.f.runId, s.f.writeRow, s.body);
  assert.deepEqual(await s.f.close(), [0, `${s.f.reviewed()}block B1: ${SRC} reviewed_by_waiver (answered W1)\nblock B1 closed\n`]);
});

test('B71 (6): an answer clears a critical (the answering file still needs its own approval); an unknown severity or a cap-only id is never open for an answer', async () => {
  const f = await fixture('b71-critical');
  // K1: open by a signed cap row only (an earlier one: the latest refusal is the review below), no
  // signed refusal at this hash records its severity
  await f.sign({ event: 'review.cap', open: ['K1'] });
  await signedReview(f, [{ id: 'C1', severity: 'critical' }, { id: 'U1', severity: 'mystery' }]);
  f.script.answer = resolvedAll;
  // U1 and K1 can never be counted as answered: refused before any session, by the CLI and the worker
  for (const id of ['U1', 'K1']) {
    const r = await f.answers(['C1', id]);
    assert.deepEqual([r.code, r.out?.reason], [1, 'answers-not-open'], id);
  }
  const forced = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: ANS, answers: ['C1', 'K1', 'U1'] });
  assert.equal(await f.drain(), 1);
  const fr = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, forced.ticket));
  assert.deepEqual([fr.status, fr.reason], ['answers', 'answers-not-open']);
  assert.equal(f.sessions.length, 0);
  assert.equal((await f.answeredRows()).length, 0);
  // C1 (a critical) is answered
  assert.equal((await f.answers(['C1'])).code, 0);
  assert.equal(await f.drain(), 1);
  assert.deepEqual((await f.answeredRows()).map((r) => r.finding), ['C1']);
  // U1 and K1 stay the human's (the cap refusal names K1); once the human waives both, the close
  // passes with C1 under answered
  assert.deepEqual(await f.close(), [1, `block B1 open: unreviewed ${SRC}; unreviewed ${ANS}; review_cap ${SRC} K1\n`]);
  for (const id of ['U1', 'K1']) {
    const w = capture();
    assert.equal(await runBlock(['waive', 'B1', id, '--run', f.runId, '--file', SRC, '--reason', 'owner accepted it in chat'], humanSaysYes({ stdout: w, stderr: w, now: () => T0 })), 0);
  }
  assert.deepEqual(await f.close(), [1, `block B1 open: unreviewed ${ANS}\n`]);
  await f.sign({ event: 'review.approved' }, ANS);
  const [code, out] = await f.close(['--json']);
  assert.equal(code, 0);
  assert.deepEqual(JSON.parse(out).reviewed_by_waiver, [{ file: SRC, content_hash: f.hashOf(SRC), status: 'reviewed_by_waiver', findings: ['K1', 'U1'], by: 'human', answered: ['C1'] }]);
});

test('B71 (7): the answers ticket id differs from the normal ticket id of the same content; a ticket edited on disk is refused', async () => {
  const f = await fixture('b71-ids');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }, { id: 'W2', severity: 'warning' }]);
  const normal = enqueue({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC });
  const a1 = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: ANS, answers: ['W2', 'W1'] });
  const a2 = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: ANS, answers: ['W1', 'W2'] });
  const a3 = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: ANS, answers: ['W1'] });
  assert.equal(normal.ticket, ticketId({ run: f.runId, block: 'B1', file: SRC, content_hash: f.hashOf(SRC) }));
  assert.notEqual(a1.ticket, normal.ticket);
  assert.equal(a1.ticket, a2.ticket); // the ids are sorted: the same set is the same ticket
  assert.deepEqual(a1.answers, ['W1', 'W2']);
  assert.notEqual(a3.ticket, a1.ticket);
  assert.match(a1.ticket, /^[0-9a-f]{24}$/);
  // a coder edits the answers ticket on disk: its id no longer matches, the worker refuses it
  const file = path.join(queueDir(f.repo), `${a3.ticket}.json`);
  const t = JSON.parse(readFileSync(file, 'utf8'));
  writeFileSync(file, `${JSON.stringify({ ...t, answers: ['W2'] })}\n`);
  writeFileSync(path.join(queueDir(f.repo), `${normal.ticket}.done`), '{}\n'); // only the answers tickets run
  assert.equal(existsSync(path.join(queueDir(f.repo), `${a1.ticket}.json`)), true);
  f.script.answer = resolvedAll;
  assert.equal(await f.drain(), 2);
  const bad = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, a3.ticket));
  assert.deepEqual([bad.status, bad.reason], ['refused', 'bad-ticket']);
  assert.deepEqual((await f.answeredRows()).map((r) => [r.finding, r.ticket]), [['W1', a1.ticket], ['W2', a1.ticket]]);
});

test('B71 (8): an id open only on an OLDER hash of the source, or the proof finding, is not open: the CLI and the worker refuse it, no session', async () => {
  const f = await fixture('b71-older');
  await signedReview(f, [{ id: 'C1', severity: 'warning' }]);
  writeFile(f.repo, SRC, `${SRC_V2}// v3\n`);
  await signedReview(f, [{ id: 'W5', severity: 'warning' }, { id: 'proof', severity: 'warning' }], 't2');
  const cli = await f.answers(['C1']);
  assert.deepEqual([cli.code, cli.out?.status, cli.out?.reason], [1, 'refused', 'answers-not-open']);
  // the proof finding is open on this hash, but only its proof clears it, never an answer
  const proof = await f.answers(['proof']);
  assert.deepEqual([proof.code, proof.out?.status, proof.out?.reason], [1, 'refused', 'answers-not-open']);
  const t = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: ANS, answers: ['C1'] });
  assert.equal(await f.drain(), 1);
  const r = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, t.ticket));
  assert.deepEqual([r.status, r.reason], ['answers', 'answers-not-open']);
  assert.equal(f.sessions.length, 0);
  // W5, open on the current hash, is accepted (the positive control)
  f.script.answer = resolvedAll;
  assert.equal((await f.answers(['W5'])).code, 0);
  assert.equal(await f.drain(), 1);
  assert.deepEqual((await f.answeredRows()).map((x) => x.finding), ['W5']);
});

test('B71 (9): a ledger write that fails part way leaves the ticket unavailable; the retry finishes with exactly one row per id', async () => {
  for (const [slug, failAt, first] of /** @type {const} */ ([['b71-write1', 1, []], ['b71-write2', 2, ['W1']]])) {
    const f = await fixture(slug);
    await signedReview(f, [{ id: 'W1', severity: 'warning' }, { id: 'W2', severity: 'warning' }]);
    f.script.answer = resolvedAll;
    f.script.failAnsweredWrites = new Set([failAt]);
    const q = await f.answers(['W1', 'W2']);
    assert.equal(q.code, 0);
    assert.equal(await f.drain(), 1);
    const r = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, q.out?.ticket));
    assert.deepEqual([r.status, r.reason], ['unavailable', 'ledger-write-failed'], slug);
    assert.deepEqual((await f.answeredRows()).map((x) => x.finding), [...first], slug);
    // the same request again is a retry of the same ticket; the worker writes only what is missing
    const again = await f.answers(['W1', 'W2']);
    assert.deepEqual([again.code, again.out?.ticket, again.out?.retry], [0, q.out?.ticket, true], slug);
    assert.equal(await f.drain(), 1);
    const done = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, q.out?.ticket));
    assert.deepEqual([done.status, done.findings.map((/** @type {any} */ x) => [x.id, x.verdict])], ['answers', [['W1', 'resolved'], ['W2', 'resolved']]], slug);
    const rows = await f.answeredRows();
    assert.deepEqual(rows.map((x) => [x.finding, x.ticket]).sort(), [['W1', q.out?.ticket], ['W2', q.out?.ticket]], slug);
    assert.equal(f.sessions.filter((x) => x.lens === 'answers').length, 2, slug);
    await f.sign({ event: 'review.approved' }, ANS);
    assert.deepEqual(await f.close(), [0, `${f.reviewed()}block B1: ${SRC} reviewed_by_waiver (answered W1, W2)\nblock B1 closed\n`], slug);
  }
});

test('B71 (10): the worker checks the id list itself — empty, missing, 11 ids or a repeated id is refused before any session', async () => {
  const f = await fixture('b71-ids-shape');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  const srcHash = f.hashOf(SRC);
  const ansHash = f.hashOf(ANS);
  const eleven = Array.from({ length: 11 }, (_, i) => `W${i + 1}`);
  /** @type {Array<[string, unknown]>} */
  const cases = [['empty', []], ['missing', undefined], ['eleven', eleven], ['repeated', ['W1', 'W1']]];
  // (a) a hand-made ticket in the queue: the worker refuses it (bad-ticket), no session, no row
  const made = cases.map(([name, answers]) => {
    // the id is computed over the body's own list ([] for a missing one), so only the id-list check can refuse it
    // 'missing' swaps the two files so its id differs from 'empty' (both lists hash as [])
    const [file, content_hash, by_file, by_hash] = name === 'missing' ? [ANS, ansHash, SRC, srcHash] : [SRC, srcHash, ANS, ansHash];
    const ticket = answersTicketId({ run: f.runId, block: 'B1', file, content_hash, by_file, by_hash, answers: /** @type {string[]} */ (answers ?? []) });
    if (name !== 'repeated') assert.throws(() => assertAnswers(answers), /finding id/, name);
    const body = { ticket, kind: 'answers', run: f.runId, block: 'B1', file, content_hash, by_file, by_hash, enqueued_at: T0.toISOString(), ...(answers === undefined ? {} : { answers }) };
    writeFileSync(path.join(queueDir(f.repo), `${ticket}.json`), `${JSON.stringify(body)}\n`);
    return ticket;
  });
  assert.equal(await f.drain(), 4);
  for (const ticket of made) {
    const r = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, ticket));
    assert.deepEqual([r.status, r.reason], ['refused', 'bad-ticket'], ticket);
  }
  assert.equal(f.sessions.length, 0);
  // (b) the answers review itself, handed the same lists: `usage`, no session, no row
  let spawned = 0;
  /** @type {Array<Record<string, any>>} */
  const written = [];
  const ctx = {
    repoRoot: f.repo,
    runId: f.runId,
    key: f.key,
    cfg: {},
    spawn: /** @type {any} */ (
      async () => {
        spawned += 1;
        return { status: 'ok', exit_code: 0, answer: { verdicts: [] } };
      }
    ),
    readRows: async () => f.rows(),
    writeRow: async (/** @type {Record<string, any>} */ row) => {
      written.push(row);
    },
  };
  const io = { contentHash, readFile: () => null, loadState: () => ({ status: 'missing' }), assertAnswers };
  for (const [name, answers] of cases) {
    const ticket = { ticket: 'a'.repeat(24), block: 'B1', file: SRC, content_hash: srcHash, by_file: ANS, by_hash: ansHash, answers: /** @type {any} */ (answers) };
    const out = await reviewAnswers(ticket, ctx, { entry: { base: null, owned: [SRC, ANS] }, workDir: freshDir('work'), io });
    assert.deepEqual([out.status, out.reason, out.approved], ['answers', 'usage', false], name);
  }
  assert.deepEqual([spawned, written.length], [0, 0]);
  assert.equal((await f.answeredRows()).length, 0);
});

test('B71 (11): an answer never clears a late finding or a cap id whose recorded severity is unknown; a known one is cleared', async () => {
  // (a) a late finding recorded at an unknown severity, with a signed, current answer of the same id
  const f = await fixture('b71-late-unknown');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  f.script.answer = resolvedAll;
  assert.equal((await f.answers(['W1'])).code, 0);
  assert.equal(await f.drain(), 1);
  assert.deepEqual((await f.answeredRows()).map((r) => r.finding), ['W1']);
  await f.sign({ event: 'review.approved' }, ANS);
  await f.sign({ event: 'review.late_finding', round: 2, finding: 'W1', severity: 'mystery', line_start: 1, line_end: 1, mode: 'block' });
  const [lateCode, lateText] = await f.close();
  assert.equal(lateCode, 1);
  assert.equal(lateText.includes(`late_unruled ${SRC} W1`), true, lateText);
  // the control: the same late finding recorded as a warning is cleared by the answer
  const c = await fixture('b71-late-known');
  await signedReview(c, [{ id: 'W1', severity: 'warning' }]);
  c.script.answer = resolvedAll;
  assert.equal((await c.answers(['W1'])).code, 0);
  assert.equal(await c.drain(), 1);
  await c.sign({ event: 'review.approved' }, ANS);
  await c.sign({ event: 'review.late_finding', round: 2, finding: 'W1', severity: 'warning', line_start: 1, line_end: 1, mode: 'block' });
  assert.deepEqual(await c.close(), [0, `${c.reviewed()}block B1: ${SRC} reviewed_by_waiver (answered W1)\nblock B1 closed\n`]);
  // (b) a cap id no signed refusal records: a signed, current answer row naming it clears nothing
  const k = await fixture('b71-cap-unknown');
  await signedReview(k, [{ id: 'W1', severity: 'warning' }]);
  await k.sign({ event: 'review.cap', open: ['K1'] });
  await k.sign({ event: 'review.approved' }, ANS);
  for (const id of ['W1', 'K1']) await k.sign({ event: 'review.answered', finding: id, by_file: ANS, by_hash: k.hashOf(ANS), ticket: 'b'.repeat(24) });
  const [capCode, capText] = await k.close();
  assert.equal(capCode, 1);
  assert.equal(capText.includes(`review_cap ${SRC} K1`), true, capText);
  assert.equal(capText.includes(`${SRC} W1`), false, capText);
});

test('B71 (12): a session that throws leaves the ticket unavailable with no row; the same request retries it', async () => {
  const f = await fixture('b71-throw');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  f.script.throwSession = true;
  const q = await f.answers(['W1']);
  assert.equal(q.code, 0);
  assert.equal(await f.drain(), 1);
  const r = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, q.out?.ticket));
  assert.deepEqual([r.status, r.reason, r.approved], ['unavailable', 'session-error', false]);
  assert.equal((await f.answeredRows()).length, 0);
  f.script.throwSession = false;
  f.script.answer = resolvedAll;
  const again = await f.answers(['W1']);
  assert.deepEqual([again.code, again.out?.ticket, again.out?.retry], [0, q.out?.ticket, true]);
  assert.equal(await f.drain(), 1);
  assert.deepEqual((await f.answeredRows()).map((x) => [x.finding, x.ticket]), [['W1', q.out?.ticket]]);
});

test('B71 (13): the packet and by_hash come from the ONE read of the answering file; a tracked answering file is shown as its diff against the block base', async () => {
  // (a) the answering file changes during the session: the row binds the bytes the reviewer saw
  const f = await fixture('b71-one-read');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  f.script.answer = resolvedAll;
  const seen = f.hashOf(ANS);
  f.script.during = () => writeFile(f.repo, ANS, `${ANS_V1}// during the session\n`);
  assert.equal((await f.answers(['W1'])).code, 0);
  assert.equal(await f.drain(), 1);
  const text = f.sessions.filter((x) => x.lens === 'answers')[0].text;
  assert.equal(text.includes("if (add(1, 1) !== 2) throw new Error('add');"), true);
  assert.equal(text.includes('// during the session'), false);
  assert.deepEqual((await f.answeredRows()).map((x) => [x.finding, x.by_hash]), [['W1', seen]]);
  assert.notEqual(f.hashOf(ANS), seen);
  // the answer is stale for the changed file: the source is not cleared
  await f.sign({ event: 'review.approved' }, ANS);
  assert.deepEqual(await f.close(), [1, `block B1 open: unreviewed ${SRC}; not_approved ${SRC} W1\n`]);
  // (b) the source (tracked since the block base) answers a finding of the new file: its diff
  const t = await fixture('b71-tracked');
  await t.sign({ event: 'review.round', round: 1, kind: 'full', open_after: 1 }, ANS);
  await t.sign({ event: 'review.result', ticket: 't9', status: 'reviewed', approved: false, open: [{ id: 'X1', severity: 'warning' }] }, ANS);
  t.script.answer = resolvedAll;
  assert.equal((await t.answers(['X1'], SRC, ANS)).code, 0);
  assert.equal(await t.drain(), 1);
  const tracked = t.sessions.filter((x) => x.lens === 'answers')[0].text;
  for (const part of [`## The change in ${SRC}`, '(its diff against the block base)', '-export const a = 1;', '+export const a = 2;']) assert.equal(tracked.includes(part), true, part);
  assert.equal(tracked.includes('(a new file: its full content)'), false);
  assert.deepEqual((await t.answeredRows()).map((x) => [x.file, x.finding, x.by_file, x.by_hash]), [[ANS, 'X1', SRC, t.hashOf(SRC)]]);
});

test('B71 (14): a packet that cannot be written to the work dir is unavailable packet-write-failed, with no session and no row', async () => {
  const f = await fixture('b71-workdir');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  let spawned = 0;
  /** @type {Array<Record<string, any>>} */
  const written = [];
  const ctx = {
    repoRoot: f.repo,
    runId: f.runId,
    key: f.key,
    cfg: {},
    spawn: /** @type {any} */ (
      async () => {
        spawned += 1;
        return { status: 'ok', exit_code: 0, answer: { verdicts: [] } };
      }
    ),
    readRows: async () => f.rows(),
    writeRow: async (/** @type {Record<string, any>} */ row) => {
      written.push(row);
    },
  };
  const io = { readFile: readRegularFileNoFollow, loadState: () => ({ status: 'missing' }), assertAnswers };
  const ticket = { ticket: 'c'.repeat(24), block: 'B1', file: SRC, content_hash: f.hashOf(SRC), by_file: ANS, by_hash: f.hashOf(ANS), answers: ['W1'] };
  // the work dir path is a regular file: the prompt cannot be written there
  const blocked = path.join(freshDir('blocked'), 'not-a-dir');
  writeFileSync(blocked, 'x\n');
  const out = await reviewAnswers(ticket, ctx, { entry: { base: null, owned: [SRC, ANS] }, workDir: blocked, io });
  assert.deepEqual([out.status, out.reason, out.approved], ['unavailable', 'packet-write-failed', false]);
  assert.deepEqual([spawned, written.length], [0, 0]);
  // the control: a usable work dir runs the one session
  const ok = await reviewAnswers(ticket, ctx, { entry: { base: null, owned: [SRC, ANS] }, workDir: freshDir('work'), io });
  assert.deepEqual([ok.status, spawned], ['answers', 1]);
});

test('B79 (a): a cap id with no severity recorded at the hash is never open, even when the cap row is the latest refusal; a cap id recorded as a warning is', async () => {
  const f = await fixture('b79-cap-latest');
  // W1 is recorded as a warning by the review; the cap row naming K1 and W1 comes after it (the latest refusal)
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  await f.sign({ event: 'review.cap', open: ['K1', 'W1'] });
  f.script.answer = resolvedAll;
  const cli = await f.answers(['K1']);
  assert.deepEqual([cli.code, cli.out?.status, cli.out?.reason], [1, 'refused', 'answers-not-open']);
  const forced = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file: SRC, byFile: ANS, answers: ['K1'] });
  assert.equal(await f.drain(), 1);
  const fr = /** @type {Record<string, any>} */ (readResult(f.repo, f.runId, forced.ticket));
  assert.deepEqual([fr.status, fr.reason], ['answers', 'answers-not-open']);
  assert.equal(f.sessions.length, 0);
  assert.equal((await f.answeredRows()).length, 0);
  // W1 (named by the cap row too, recorded as a warning at this hash) is open: one session, one row
  assert.equal((await f.answers(['W1'])).code, 0);
  assert.equal(await f.drain(), 1);
  assert.equal(f.sessions.filter((x) => x.lens === 'answers').length, 1);
  assert.deepEqual((await f.answeredRows()).map((r) => r.finding), ['W1']);
  // the close still refuses K1 at the cap
  await f.sign({ event: 'review.approved' }, ANS);
  assert.deepEqual(await f.close(), [1, `block B1 open: unreviewed ${SRC}; review_cap ${SRC} K1\n`]);
});

/**
 * An in-process answers review of the fixture's run (no CLI, no queue): the spawned packets and the
 * rows the worker would write.
 * @param {Awaited<ReturnType<typeof fixture>>} f @param {Record<string, any>} [cfg]
 */
function direct(f, cfg = {}) {
  /** @type {string[]} */
  const packets = [];
  /** @type {Array<Record<string, any>>} */
  const written = [];
  const ctx = {
    repoRoot: f.repo,
    runId: f.runId,
    key: f.key,
    cfg,
    spawn: /** @type {any} */ (
      async (/** @type {Record<string, any>} */ opts) => {
        packets.push(readFileSync(opts.promptPath, 'utf8'));
        return { status: 'ok', exit_code: 0, answer: { verdicts: [] } };
      }
    ),
    readRows: async () => f.rows(),
    writeRow: async (/** @type {Record<string, any>} */ row) => {
      written.push(row);
    },
  };
  return { ctx, packets, written };
}

test('B79 (b): a secret-like source is refused packet-unreadable like a secret-like answering file — no packet, no session, no row', async () => {
  const f = await fixture('b79-secret');
  const SECRET = 'config/credentials.json';
  writeFile(f.repo, SECRET, '{"user": "FAKE"}\n');
  await f.sign({ event: 'review.round', round: 1, kind: 'full', open_after: 1 }, SECRET);
  await f.sign({ event: 'review.result', ticket: 't1', status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] }, SECRET);
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  const io = { readFile: readRegularFileNoFollow, loadState: () => ({ status: 'missing' }), assertAnswers };
  const entry = { base: null, owned: [SRC, ANS, SECRET] };
  const d = direct(f);
  // [source, answering file]: the secret-like file on either side
  for (const [source, byFile] of [[SECRET, ANS], [SRC, SECRET]]) {
    const workDir = freshDir('work');
    const ticket = { ticket: 'd'.repeat(24), block: 'B1', file: source, content_hash: f.hashOf(source), by_file: byFile, by_hash: f.hashOf(byFile), answers: ['W1'] };
    const out = await reviewAnswers(ticket, d.ctx, { entry, workDir, io });
    assert.deepEqual([out.status, out.reason, out.approved], ['unavailable', 'packet-unreadable', false], source);
    assert.deepEqual(readdirSync(workDir), [], source);
  }
  assert.deepEqual([d.packets.length, d.written.length], [0, 0]);
  // the control: two plain files run the one session
  const ticket = { ticket: 'e'.repeat(24), block: 'B1', file: SRC, content_hash: f.hashOf(SRC), by_file: ANS, by_hash: f.hashOf(ANS), answers: ['W1'] };
  const ok = await reviewAnswers(ticket, d.ctx, { entry, workDir: freshDir('work'), io });
  assert.deepEqual([ok.status, d.packets.length], ['answers', 1]);
});

/**
 * The text between `from` and the first `to` after it (`to` empty: the rest), or null when `from` is
 * not in `text`.
 * @param {string} text @param {string} from @param {string} to
 */
function between(text, from, to) {
  const at = text.indexOf(from);
  if (at === -1) return null;
  const rest = text.slice(at + from.length);
  return to === '' ? rest : rest.slice(0, rest.indexOf(to));
}

/** The B79 packet layout of one finding's parts: [claim, evidence, fix, source excerpt]. */
const SECTIONS = /** @type {const} */ ([
  ['- claim: ', '\n- evidence: '],
  ['- evidence: ', '\n- fix: '],
  ['- fix: ', '\n\n'],
  ['```\n', '\n```\n'],
]);

test('B79 (c)(c2): the whole packet stays within the budget — claim, evidence, fix, source excerpt and change each cut on a character boundary with its own marker, each within its own cap; a range past the end or recorded backwards shows no lines', async () => {
  const MARKER = '[... truncated at the packet budget]';
  const f = await fixture('b79-budget');
  // line 3 of the source is 30 000 two-byte characters; the budget is full_in 2000 ⇒ 8000 bytes
  writeFile(f.repo, SRC, `export const a = 2;\nexport function add(x, y) {\n  return ${'é'.repeat(30000)};\n}\n`);
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  const limit = 8000;
  const cfg = { review: { budgets: { full_in: 2000 } } };
  /** @param {Record<string, any>} finding */
  const io = (finding) => ({ readFile: readRegularFileNoFollow, loadState: () => ({ status: 'ok', state: { open: [{ id: 'W1', severity: 'warning', ...finding }] } }), assertAnswers });
  const entry = { base: null, owned: [SRC, ANS] };
  const ticket = () => ({ ticket: 'f'.repeat(24), block: 'B1', file: SRC, content_hash: f.hashOf(SRC), by_file: ANS, by_hash: f.hashOf(ANS), answers: ['W1'] });
  // claim, evidence and fix: each huge, of a different multi-byte character (2, 3 and 4 bytes)
  const huge = { line_start: 3, line_end: 3, claim: 'ü'.repeat(50000), evidence: '€'.repeat(40000), fix: '😀'.repeat(30000) };
  const markers = (/** @type {string} */ text) => text.split(MARKER).length - 1;
  const CHANGE = '(a new file: its full content)\n```\n';
  /**
   * Each of the finding's four parts, in place: one marker, at its end, after only its own
   * character (`chars[i]`). Each part fills its own cap, worked out from the packet: the fixed
   * text is the packet less its parts and change; the finding gets half of the rest, each text
   * part a fifth of that, the source excerpt what the three leave. A cut steps back at most 3
   * bytes (a character boundary), so each part is within (cap - 4, cap].
   * @param {string} p @param {string[]} chars
   */
  const partsCut = (p, chars) => {
    const parts = SECTIONS.map(([from, to], i) => {
      const part = /** @type {string} */ (between(p, from, to));
      assert.equal(markers(part), 1, from);
      assert.equal(part.endsWith(`\n${MARKER}`), true, from);
      const body = part.slice(0, -`\n${MARKER}`.length);
      assert.equal(body.length > 0 && [...body].every((c) => chars[i].includes(c)), true, from);
      return Buffer.byteLength(part);
    });
    const change = Buffer.byteLength(/** @type {string} */ (between(p, CHANGE, '\n```\n')));
    const fixed = Buffer.byteLength(p) - parts.reduce((a, b) => a + b, 0) - change;
    const share = Math.floor((limit - fixed) / 2);
    const textCap = Math.floor(share / 5);
    const caps = [textCap, textCap, textCap, share - 3 * textCap];
    parts.forEach((bytes, i) => assert.equal(bytes <= caps[i] && bytes > caps[i] - 4, true, `${SECTIONS[i][0]}${bytes} of ${caps[i]}`));
    // the change gets what the fixed text and the parts leave
    assert.equal(change <= limit - fixed - parts.reduce((a, b) => a + b, 0), true);
  };
  // (1) a huge claim, evidence, fix and source line: four parts cut, the small change whole
  const d = direct(f, cfg);
  assert.equal((await reviewAnswers(ticket(), d.ctx, { entry, workDir: freshDir('work'), io: io(huge) })).status, 'answers');
  assert.equal(d.packets.length, 1);
  const p1 = d.packets[0];
  assert.equal(Buffer.byteLength(p1) <= limit, true, String(Buffer.byteLength(p1)));
  assert.equal(markers(p1), 4);
  assert.equal(p1.includes('�'), false);
  partsCut(p1, ['ü', '€', '😀', '0123456789:;abcdefghijklmnopqrstuvwxyz ()=é{}\n,']);
  assert.equal(between(p1, '```\n', '\n```\n')?.startsWith('1: export const a = 2;\n2: export function add(x, y) {\n3:   return éé'), true);
  for (const part of ['Source lines 1-5:', "if (add(1, 1) !== 2) throw new Error('add');"]) assert.equal(p1.includes(part), true, part);
  assert.equal(p1.endsWith('```\n'), true);
  // (2) the answering file is huge too: five parts cut, still within the budget
  writeFile(f.repo, ANS, `${ANS_V1}// ${'x'.repeat(40000)}\n`);
  const d2 = direct(f, cfg);
  assert.equal((await reviewAnswers(ticket(), d2.ctx, { entry, workDir: freshDir('work'), io: io(huge) })).status, 'answers');
  const p2 = d2.packets[0];
  assert.equal(Buffer.byteLength(p2) <= limit, true, String(Buffer.byteLength(p2)));
  assert.equal(markers(p2), 5);
  partsCut(p2, ['ü', '€', '😀', '0123456789:;abcdefghijklmnopqrstuvwxyz ()=é{}\n,']);
  assert.equal(between(p2, `## The change in ${ANS}`, '')?.endsWith(`xxx\n${MARKER}\n\`\`\`\n`), true);
  assert.equal(p2.includes("if (add(1, 1) !== 2) throw new Error('add');"), true);
  // (c2) a recorded range past the end of the source (5 lines with the empty last one): the lines are named, no source block
  writeFile(f.repo, ANS, ANS_V1);
  const d3 = direct(f, cfg);
  assert.equal((await reviewAnswers(ticket(), d3.ctx, { entry, workDir: freshDir('work'), io: io({ line_start: 50, line_end: 50, claim: 'c' }) })).status, 'answers');
  const p3 = d3.packets[0];
  assert.deepEqual([p3.includes('- lines: 50-50'), p3.includes('Source lines'), markers(p3)], [true, false, 0]);
  // (c2) a range recorded backwards inside the file (4 to 2): the lines are named, no source block
  const d4 = direct(f, cfg);
  assert.equal((await reviewAnswers(ticket(), d4.ctx, { entry, workDir: freshDir('work'), io: io({ line_start: 4, line_end: 2, claim: 'c' }) })).status, 'answers');
  const p4 = d4.packets[0];
  assert.deepEqual([p4.includes('- lines: 4-2'), p4.includes('Source lines'), markers(p4), p4.endsWith('```\n')], [true, false, 0, true]);
});

test('B79 (c): a part is redacted before it is cut — a secret across the cut leaves no prefix of itself in the packet', async () => {
  const f = await fixture('b79-secret-cut');
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  const SECRET = 'FAKE-secret-token-0123456789abcdef';
  registerSecret(SECRET);
  try {
    const cfg = { review: { budgets: { full_in: 2000 } } };
    // every cut point falls inside or right after a secret: the claim is nothing but secrets
    const claim = `${SECRET} `.repeat(2000);
    const io = { readFile: readRegularFileNoFollow, loadState: () => ({ status: 'ok', state: { open: [{ id: 'W1', severity: 'warning', claim }] } }), assertAnswers };
    const ticket = { ticket: 'f'.repeat(24), block: 'B1', file: SRC, content_hash: f.hashOf(SRC), by_file: ANS, by_hash: f.hashOf(ANS), answers: ['W1'] };
    const d = direct(f, cfg);
    assert.equal((await reviewAnswers(ticket, d.ctx, { entry: { base: null, owned: [SRC, ANS] }, workDir: freshDir('work'), io })).status, 'answers');
    const p = d.packets[0];
    assert.equal(Buffer.byteLength(p) <= 8000, true, String(Buffer.byteLength(p)));
    assert.deepEqual([p.includes('FAKE'), p.includes('secret-token'), p.split(TRUNCATED_MARKER).length - 1], [false, false, 1]);
    // the cut claim is a prefix of the redacted claim (masks only), then its marker
    const cut = /** @type {string} */ (between(p, '- claim: ', '\n- evidence: '));
    const body = cut.slice(0, -`\n${TRUNCATED_MARKER}`.length);
    assert.deepEqual([cut.endsWith(`\n${TRUNCATED_MARKER}`), body.length > 0, '[REDACTED] '.repeat(2000).startsWith(body)], [true, true, true]);
  } finally {
    clearSecrets();
  }
});

test('B79 (c): a redaction that grows every part never cuts the packet — the closing fence is the last line and each cut part has its one marker; a secret across a part boundary is refused, never cut', async () => {
  const f = await fixture('b79-secret-grow');
  // a 5-byte secret masked as `[REDACTED]` (10 bytes): every part doubles when redacted
  const SECRET = 'FAKE1';
  writeFile(f.repo, SRC, `export const a = 2;\nexport function add(x, y) {\n  return ${`${SECRET} `.repeat(6000)};\n}\n`);
  writeFile(f.repo, ANS, `${ANS_V1}// ${`${SECRET} `.repeat(8000)}\n`);
  await signedReview(f, [{ id: 'W1', severity: 'warning' }]);
  const many = `${SECRET} `.repeat(4000);
  /** @param {Record<string, any>} finding */
  const io = (finding) => ({ readFile: readRegularFileNoFollow, loadState: () => ({ status: 'ok', state: { open: [{ id: 'W1', severity: 'warning', line_start: 3, line_end: 3, ...finding }] } }), assertAnswers });
  const entry = { base: null, owned: [SRC, ANS] };
  const ticket = { ticket: 'f'.repeat(24), block: 'B1', file: SRC, content_hash: f.hashOf(SRC), by_file: ANS, by_hash: f.hashOf(ANS), answers: ['W1'] };
  registerSecret(SECRET);
  try {
    const d = direct(f, { review: { budgets: { full_in: 2000 } } });
    assert.equal((await reviewAnswers(ticket, d.ctx, { entry, workDir: freshDir('work'), io: io({ claim: many, evidence: many, fix: many }) })).status, 'answers');
    const p = d.packets[0];
    assert.equal(Buffer.byteLength(p) <= 8000, true, String(Buffer.byteLength(p)));
    // claim, evidence, fix, source excerpt and change: one marker each, no other
    assert.deepEqual([p.split(TRUNCATED_MARKER).length - 1, p.includes(SECRET), p.endsWith(`\n${TRUNCATED_MARKER}\n\`\`\`\n`)], [5, false, true]);
    assert.equal(p.split('\n').at(-2), '```');
    for (const [from, to] of [...SECTIONS, ['(a new file: its full content)\n```\n', '\n```\n']]) assert.equal(between(p, from, to)?.split(TRUNCATED_MARKER).length, 2, from);
    // a secret only the joined packet holds (the end of the claim, the evidence label): refused, no session, no row
    clearSecrets();
    registerSecret('FAKE2\n- evidence: x');
    const workDir = freshDir('work');
    const d2 = direct(f, { review: { budgets: { full_in: 2000 } } });
    const out = await reviewAnswers(ticket, d2.ctx, { entry, workDir, io: io({ claim: 'FAKE2', evidence: 'x', fix: 'y' }) });
    assert.deepEqual([out.status, out.reason, out.approved, d2.packets.length, d2.written.length, readdirSync(workDir)], ['unavailable', 'packet-unreadable', false, 0, 0, []]);
  } finally {
    clearSecrets();
  }
});

test('B79 (c): many findings and a small budget — each part within its cap (a short marker when the cap cannot hold the full one); a fixed text over the budget is packet-too-large with no session', async () => {
  const f = await fixture('b79-many');
  writeFile(f.repo, SRC, `export const a = 2;\nexport function add(x, y) {\n  return ${'é'.repeat(30000)};\n}\n`);
  writeFile(f.repo, ANS, `${ANS_V1}// ${'x'.repeat(40000)}\n`);
  const ids = Array.from({ length: 10 }, (_, i) => `W${i + 1}`);
  await signedReview(f, ids.map((id) => ({ id, severity: 'warning' })));
  const open = ids.map((id) => ({ id, severity: 'warning', line_start: 3, line_end: 3, claim: 'ü'.repeat(5000), evidence: '€'.repeat(5000), fix: '😀'.repeat(5000) }));
  const io = { readFile: readRegularFileNoFollow, loadState: () => ({ status: 'ok', state: { open } }), assertAnswers };
  const entry = { base: null, owned: [SRC, ANS] };
  const ticket = { ticket: 'f'.repeat(24), block: 'B1', file: SRC, content_hash: f.hashOf(SRC), by_file: ANS, by_hash: f.hashOf(ANS), answers: ids };
  const count = (/** @type {string} */ text, /** @type {string} */ m) => text.split(m).length - 1;
  /** One finding's block of the packet, its excerpt as given. @param {string} id @param {string} text @param {string} excerpt */
  const block = (id, text, excerpt) => `## Finding ${id}\n\n- file: ${SRC}\n- lines: 3-3\n- severity: warning\n- claim: ${text}\n- evidence: ${text}\n- fix: ${text}\n\nSource lines 1-5:\n\`\`\`\n${excerpt}\n\`\`\`\n\n`;
  /** @param {number} fullIn */
  const run = async (fullIn) => {
    const d = direct(f, { review: { budgets: { full_in: fullIn } } });
    const workDir = freshDir('work');
    const out = await reviewAnswers(ticket, d.ctx, { entry, workDir, io });
    return { out, d, workDir, p: d.packets[0] ?? '' };
  };
  // [full_in, a finding's text part, its excerpt, short markers, full markers]: the fixed text of
  // ten findings fits; each text part's cap is too small for the full marker (a short one when it
  // fits, else nothing); the excerpt's larger cap takes the full marker once it can; the change
  // takes the rest, cut with the full marker
  const cases = /** @type {Array<[number, string, string, number, number]>} */ ([
    [500, '', SHORT_MARKER, 10, 1],
    [600, SHORT_MARKER, SHORT_MARKER, 40, 1],
    [900, SHORT_MARKER, `1\n${TRUNCATED_MARKER}`, 30, 11],
    [1000, SHORT_MARKER, `1: export\n${TRUNCATED_MARKER}`, 30, 11],
  ]);
  for (const [fullIn, text, excerpt, short, full] of cases) {
    const { out, d, p } = await run(fullIn);
    assert.deepEqual([out.status, d.packets.length], ['answers', 1], String(fullIn));
    assert.equal(Buffer.byteLength(p) <= fullIn * 4, true, `${fullIn}: ${Buffer.byteLength(p)}`);
    assert.deepEqual([count(p, SHORT_MARKER), count(p, TRUNCATED_MARKER)], [short, full], String(fullIn));
    for (const id of ids) assert.equal(p.includes(block(id, text, excerpt)), true, `${fullIn} ${id}`);
    assert.equal(p.endsWith(`xxx\n${TRUNCATED_MARKER}\n\`\`\`\n`), true, String(fullIn));
    assert.equal(p.includes('�'), false);
  }
  // the fixed text of ten findings alone is over 1600 bytes: final for these ids, no packet, no session, no row
  const { out, d, workDir } = await run(400);
  assert.deepEqual([out.status, out.reason, out.approved], ['unavailable', 'packet-too-large', false]);
  assert.deepEqual([d.packets.length, d.written.length, readdirSync(workDir)], [0, 0, []]);
});
