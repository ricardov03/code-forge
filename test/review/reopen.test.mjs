import { commitAll, makeRepo, writeFile, freshDir } from './helpers.mjs';
import { humanSaysYes } from '../helpers/human-confirm.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

// B72 (issues #12, #15): `block reopen <id> --file <path> --run <r> --reason <text>` — the human
// restarts a stopped or lost file on new content. Every scenario drives the real `block` and
// `review-file` verbs, an in-process worker on a stub session spawner (never a real CLI) and the
// real `block close`, in a temp HOME and a temp repo.

const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { reopenFile } = await import('../../src/review/reopen.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, queueDir, readResult } = await import('../../src/worker/queue.mjs');
const { saveState, statePath } = await import('../../src/worker/review-state.mjs');
const { ledgerPath } = await import('../../src/ledger/paths.mjs');
const { runRecordPath, StateError } = await import('../../src/state/paths.mjs');
const { initialConfigState } = await import('../../src/state/config-snapshot.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { isForbidden, mergeForbidden } = await import('../../src/util/forbidden.mjs');

const SRC = 'src/a.mjs';
const OTHER = 'src/other.mjs';
const T0 = new Date('2026-10-12T10:00:00.000Z');
const V = (/** @type {number} */ n) => `export const a = ${n};\nexport function add(x, y) {\n  return x - y;\n}\n`;
const C1 = { id: 'C1', file: SRC, line_start: 3, line_end: 3, severity: 'warning', category: 'correctness', claim: 'add subtracts', evidence: 'return x - y', fix: 'return x + y' };

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
  const at = text.indexOf('\nhunks:\n');
  if (at < 0) return [];
  const listed = text.slice(at + 8).split('\n');
  const end = listed.findIndex((l) => !l.startsWith('- @@'));
  return listed.slice(0, end < 0 ? listed.length : end).map((l) => l.slice(2));
}

/**
 * A run with block B1 owning `src/a.mjs` (changed after block open to V(2)), `review.max_rounds_per_file: 1`
 * (round 1 with an open finding takes the block's L3 rung; the patch check that does not shrink
 * the set stops `l3_patch_exhausted`), an in-process worker on a stub spawner whose sessions report
 * `script.findings`, and an announced live worker for `review-file`.
 * @param {string} slug
 */
async function fixture(slug) {
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, SRC, V(1));
  writeFile(repo, OTHER, 'export const o = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [SRC], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeFile(repo, SRC, V(2));
  const key = await loadKey(runId);
  /** @type {{findings: Array<Record<string, any>>}} */
  const script = { findings: [] };
  /** @type {Array<{lens: string, level: string, text: string}>} */
  const sessions = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    sessions.push({ lens: opts.rowExtra?.lens, level: opts.level, text });
    const resolved = [...text.slice(text.indexOf('## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => ({ id: m[1], resolved: false, why: 'still open' }));
    const answer = { passed: script.findings.length === 0, summary: 's', reviewed_hunks: hunksOf(text), findings: script.findings, resolved, needs_file: [] };
    return { status: 'ok', exit_code: 0, answer, usage: { tokens_in: 900, tokens_out: 300 } };
  };
  const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
    const [id] = Object.keys(req.questions);
    return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
  };
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: { review: { max_rounds_per_file: 1 } }, runRootDir: freshDir('runroot'), slug, key },
    { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow, readRows: () => readAllRows(slug), spawn: /** @type {any} */ (spawn), jev },
  );
  announceWorker(repo, { pid: process.pid, run: runId });
  /** One `review-file` of SRC, then a drain: the queue answer, the drained count and the signed result. */
  const review = async () => {
    const stdout = capture();
    const code = await runReviewFile([SRC, '--block', 'B1'], { cwd: repo, stdout, stderr: capture() });
    const out = JSON.parse(stdout.text);
    const drained = await worker.drain();
    return { code, out, drained, result: /** @type {Record<string, any>} */ (readResult(repo, runId, out.ticket)) };
  };
  /** @param {string[]} args @returns {Promise<{code: number, out: string, err: string}>} */
  const block = async (args) => {
    const stdout = capture();
    const stderr = capture();
    const code = await runBlock(args, humanSaysYes({ stdout, stderr, probe, now: () => T0 }));
    return { code, out: stdout.text, err: stderr.text };
  };
  const reopen = (/** @type {string[]} */ extra = []) => block(['reopen', 'B1', '--run', runId, '--file', SRC, '--reason', 'the owner split the fix', ...extra]);
  const close = () => block(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`]);
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  const reopenedRows = async () => (await readAllRows(slug)).filter((r) => r.run === runId && r.event === 'review.reopened');
  const anchors = async () => (await rows()).filter((r) => r.event === 'review.state' && r.file === SRC);
  const hashOf = () => contentHash(repo, SRC);
  return { runId, slug, repo, key, script, sessions, review, block, reopen, close, rows, reopenedRows, anchors, hashOf, writeRow };
}

/** Round 1 on V(2) (C1 open ⇒ the L3 rung), then the patch check on V(3) (C1 again ⇒ stop). */
async function stopOnPatch(/** @type {Awaited<ReturnType<typeof fixture>>} */ f) {
  f.script.findings = [C1];
  const r1 = await f.review();
  assert.deepEqual([r1.drained, r1.result.status, r1.result.round, r1.result.kind, r1.result.next?.action], [1, 'reviewed', 1, 'full', 'patch']);
  writeFile(f.repo, SRC, V(3));
  const r2 = await f.review();
  assert.deepEqual([r2.drained, r2.result.status, r2.result.stopped, r2.result.approved], [1, 'stopped', 'l3_patch_exhausted', false]);
  return r2;
}

/** The row's fields without its MAC, timestamp, `tokens_source` and `cost_source`. */
const fieldsOf = (/** @type {Record<string, any>} */ row) => {
  const { mac: _m, ts: _t, tokens_source: _ts, cost_source: _cs, ...rest } = row;
  return rest;
};

test('B72 (1)(2)(3)(8): stopped ⇒ same content refused ⇒ attempt keeps the stop ⇒ changed content reopens a full round 1 ⇒ an approval closes the block', async () => {
  const f = await fixture('b72-main');
  await stopOnPatch(f);
  const stopHash = f.hashOf();
  const sessionsAtStop = f.sessions.length;

  // (2) the stop's own content: refused, no row
  const same = await f.reopen();
  assert.deepEqual([same.code, same.out], [1, '']);
  assert.equal(same.err.startsWith('block reopen: reopen-same-content: '), true, same.err);
  assert.deepEqual(await f.reopenedRows(), []);

  // (3) a new attempt and new content: still stopped, no session
  assert.equal((await f.block(['attempt', 'B1', '--run', f.runId])).code, 0);
  writeFile(f.repo, SRC, V(4));
  const again = await f.review();
  assert.deepEqual([again.drained, again.result.status, again.result.stopped, again.result.approved], [1, 'stopped', 'l3_patch_exhausted', false]);
  assert.equal(f.sessions.length, sessionsAtStop);
  // the cached stop of this content would repeat on the next review-file
  assert.equal(existsSync(path.join(queueDir(f.repo), `${again.out.ticket}.done`)), true);
  const atSeq = Math.max(...(await f.anchors()).map((a) => a.seq));
  assert.equal(atSeq, 2);

  // (1) reopened on changed content: one signed human row, the cached stop cleared
  const ok = await f.reopen(['--json']);
  assert.deepEqual([ok.code, ok.err], [0, '']);
  assert.deepEqual(JSON.parse(ok.out), { block: 'B1', event: 'reopened', file: SRC, from: 'stopped', stop_reason: 'l3_patch_exhausted', at_seq: 2, from_hash: stopHash, to_hash: f.hashOf(), ticket_cleared: true, by: 'human' });
  const reopened = await f.reopenedRows();
  assert.equal(reopened.length, 1);
  assert.equal(verifyRow(reopened[0], f.key).ok, true);
  assert.deepEqual(fieldsOf(reopened[0]), { run: f.runId, event: 'review.reopened', block: 'B1', file: SRC, from: 'stopped', stop_reason: 'l3_patch_exhausted', at_seq: 2, from_hash: stopHash, to_hash: f.hashOf(), reason: 'the owner split the fix', by: 'human' });
  assert.equal(existsSync(path.join(queueDir(f.repo), `${again.out.ticket}.done`)), false);

  // a retry on the same seq and content finds the row: no second row, nothing left to clear
  const retry = await f.reopen(['--json']);
  assert.deepEqual([retry.code, retry.err], [0, '']);
  assert.deepEqual(JSON.parse(retry.out), { block: 'B1', event: 'reopened', file: SRC, from: 'stopped', stop_reason: 'l3_patch_exhausted', at_seq: 2, from_hash: stopHash, to_hash: f.hashOf(), ticket_cleared: false, by: 'human' });
  assert.equal((await f.reopenedRows()).length, 1);
  // other content at the same seq: refused, still one row; V(4) back and the row stands
  writeFile(f.repo, SRC, V(5));
  const other = await f.reopen();
  assert.deepEqual([other.code, other.out], [1, '']);
  assert.equal(other.err, `block reopen: reopen-already-recorded: ${SRC} was already reopened at state seq 2 for other content; run review-file, or the content changed since the reopen; nothing was recorded\n`);
  assert.equal((await f.reopenedRows()).length, 1);
  writeFile(f.repo, SRC, V(4));

  // the next review-file runs: a full round 1 with one session, approved
  f.script.findings = [];
  const fresh = await f.review();
  assert.equal(fresh.out.status, 'queued');
  assert.deepEqual([fresh.drained, fresh.result.status, fresh.result.approved, fresh.result.round, fresh.result.kind], [1, 'reviewed', true, 1, 'full']);
  assert.equal(f.sessions.length, sessionsAtStop + 1);
  assert.equal(f.sessions.at(-1)?.level, 'L2');
  // (8) the new state is saved at at_seq + 1 and the block's L3 rung stays spent
  const last = (await f.anchors()).at(-1);
  assert.deepEqual([last?.seq, last?.l3_rung_used], [3, true]);
  assert.equal((await f.rows()).filter((r) => r.event === 'review.state_restored').length, 0);

  const closed = await f.close();
  // B96: the close names the file with the ticket the fresh review-file gave
  assert.deepEqual([closed.code, closed.out, closed.err], [0, `reviewed src/a.mjs ticket ${fresh.out.ticket}\nblock B1 closed\n`, '']);
});

test('B72 (6)(8): a reopened row works once, the spent rung makes the next cap a stop; forged, unsigned, other-run, non-human and stale rows are ignored', async () => {
  const f = await fixture('b72-once');
  await stopOnPatch(f);
  const stopHash = f.hashOf();
  const sessionsAtStop = f.sessions.length;
  writeFile(f.repo, SRC, V(4));
  const base = { block: 'B1', file: SRC, from: 'stopped', stop_reason: 'l3_patch_exhausted', at_seq: 2, from_hash: f.hashOf(), to_hash: f.hashOf(), reason: 'r' };
  // non-human, a stale seq: signed but not counted
  await writeSigned(f.runId, f.writeRow, { event: 'review.reopened', ...base, by: 'agent' });
  await writeSigned(f.runId, f.writeRow, { event: 'review.reopened', ...base, at_seq: 1, by: 'human' });
  // a bad MAC, no MAC, another run's row signed with this key
  const forged = signRow({ run: f.runId, event: 'review.reopened', ...base, by: 'human' }, f.key);
  await f.writeRow({ ...forged, reason: 'edited after signing' });
  const { mac: _mac, ...unsigned } = signRow({ run: f.runId, event: 'review.reopened', ...base, by: 'human' }, f.key);
  await f.writeRow(unsigned);
  await f.writeRow(signRow({ run: 'r-other', event: 'review.reopened', ...base, by: 'human' }, f.key));
  writeFile(f.repo, SRC, V(5));
  const ignored = await f.review();
  assert.deepEqual([ignored.drained, ignored.result.status, ignored.result.stopped], [1, 'stopped', 'l3_patch_exhausted']);
  assert.equal(f.sessions.length, sessionsAtStop);

  // the human's row: exactly one verified human row of this run at seq 2 is written
  const real = await f.reopen(['--json']);
  assert.deepEqual([real.code, real.err], [0, '']);
  const realOut = JSON.parse(real.out);
  assert.deepEqual([realOut.at_seq, realOut.to_hash, realOut.ticket_cleared], [2, f.hashOf(), true]);
  const counted = (await f.reopenedRows()).filter((r) => r.by === 'human' && r.at_seq === 2 && verifyRow(r, f.key).ok);
  assert.equal(counted.length, 1);
  assert.deepEqual(fieldsOf(counted[0]), { run: f.runId, event: 'review.reopened', block: 'B1', file: SRC, from: 'stopped', stop_reason: 'l3_patch_exhausted', at_seq: 2, from_hash: stopHash, to_hash: f.hashOf(), reason: 'the owner split the fix', by: 'human' });
  // round 1 runs with C1 again — the cap now stops (the rung is spent, no patch)
  const first = await f.review();
  assert.deepEqual([first.drained, first.result.status, first.result.round, first.result.kind, first.result.stopped, first.result.next?.action], [1, 'stopped', 1, 'full', 'review_cap', 'stop']);
  assert.equal(f.sessions.length, sessionsAtStop + 1);
  // the row is spent: new content stops with no session
  writeFile(f.repo, SRC, V(6));
  const spent = await f.review();
  assert.deepEqual([spent.drained, spent.result.status, spent.result.stopped], [1, 'stopped', 'review_cap']);
  assert.equal(f.sessions.length, sessionsAtStop + 1);
  // a second reopen (new latest seq) works once more
  const second = await f.reopen(['--json']);
  assert.deepEqual([second.code, JSON.parse(second.out).at_seq, JSON.parse(second.out).stop_reason], [0, 3, 'review_cap']);
  f.script.findings = [];
  const ok = await f.review();
  assert.deepEqual([ok.result.status, ok.result.approved, ok.result.round], ['reviewed', true, 1]);
  assert.equal(f.sessions.length, sessionsAtStop + 2);
});

test('B72 (4): a lost state is refused until the human reopens it; then round 1 runs on the current content', async () => {
  const f = await fixture('b72-lost');
  f.script.findings = [C1];
  const r1 = await f.review();
  assert.deepEqual([r1.result.status, r1.result.round], ['reviewed', 1]);
  // the state file is gone and the latest anchor carries no body (too large to ride on the ledger)
  rmSync(statePath(f.runId, 'B1', SRC), { force: true });
  await writeSigned(f.runId, f.writeRow, { event: 'review.state', block: 'B1', file: SRC, seq: 2, state_sha: 'f'.repeat(64), l3_rung_used: false, state_omitted: 'too_large' });
  writeFile(f.repo, SRC, V(3));
  const lost = await f.review();
  assert.deepEqual([lost.result.status, lost.result.reason], ['refused', 'review-state-lost']);
  const sessionsBefore = f.sessions.length;

  const ok = await f.reopen();
  assert.deepEqual([ok.code, ok.out, ok.err], [0, `block B1 reopened ${SRC} (from lost; by: human)\n`, '']);
  const rows = await f.reopenedRows();
  assert.equal(rows.length, 1);
  assert.deepEqual(fieldsOf(rows[0]), { run: f.runId, event: 'review.reopened', block: 'B1', file: SRC, from: 'lost', stop_reason: null, at_seq: 2, from_hash: null, to_hash: f.hashOf(), reason: 'the owner split the fix', by: 'human' });

  f.script.findings = [];
  const fresh = await f.review();
  assert.deepEqual([fresh.drained, fresh.result.status, fresh.result.approved, fresh.result.round, fresh.result.kind, fresh.result.content_hash], [1, 'reviewed', true, 1, 'full', f.hashOf()]);
  assert.equal(f.sessions.length, sessionsBefore + 1);
  assert.equal((await f.anchors()).at(-1)?.seq, 3);
});

test('B72 (5): refused with no row — tampered, never reviewed, open, complete, unowned, unknown or stopped block; usage errors exit 2', async () => {
  const f = await fixture('b72-refused');
  /** @param {string} code @param {string[] | undefined} extra @param {string[] | undefined} args */
  const refused = async (code, extra = undefined, args = undefined) => {
    const r = args ? await f.block(args) : await f.reopen(extra);
    assert.deepEqual([r.code, r.out], [code === 'usage' ? 2 : 1, '']);
    assert.equal(r.err.startsWith(code === 'usage' || code === 'unknown_block' ? 'block reopen: ' : `block reopen: ${code}: `), true, r.err);
    assert.deepEqual(await f.reopenedRows(), []);
    return r.err;
  };
  // never reviewed
  assert.equal((await refused('reopen-not-stopped')).includes('has no review state'), true);
  // a run record whose config snapshot does not match its hash: the state root is not guessed
  const recordFile = runRecordPath(f.runId);
  const recordText = readFileSync(recordFile, 'utf8');
  const forged = JSON.parse(recordText);
  forged.config = { hash: 'f'.repeat(64), snapshots: { ['f'.repeat(64)]: { tmp: { root: '/FAKE-root' } } } };
  writeFileSync(recordFile, JSON.stringify(forged));
  const config = await refused('reopen-config');
  assert.deepEqual([config.includes('tmp.root'), config.includes('FAKE')], [true, false]);
  writeFileSync(recordFile, recordText);
  // complete: an approved round 1
  const done = await f.review();
  assert.deepEqual([done.result.status, done.result.approved], ['reviewed', true]);
  assert.equal((await refused('reopen-not-stopped')).includes('is complete'), true);
  // open: new content, round 1 leaves C1 open
  writeFile(f.repo, SRC, V(3));
  f.script.findings = [C1];
  const open = await f.review();
  assert.deepEqual([open.result.status, open.result.approved, open.result.round], ['reviewed', false, 1]);
  assert.equal((await refused('reopen-not-stopped')).includes('is open'), true);
  // unowned file, unknown block
  await refused('reopen-not-owned', undefined, ['reopen', 'B1', '--run', f.runId, '--file', OTHER, '--reason', 'r']);
  assert.equal((await refused('unknown_block', undefined, ['reopen', 'B9', '--run', f.runId, '--file', SRC, '--reason', 'r'])).includes('B9 is not in run'), true);
  // usage: no --reason, no --file, a path that is not repo-root-relative
  await refused('usage', undefined, ['reopen', 'B1', '--run', f.runId, '--file', SRC]);
  await refused('usage', undefined, ['reopen', 'B1', '--run', f.runId, '--reason', 'r']);
  await refused('usage', undefined, ['reopen', 'B1', '--run', f.runId, '--file', '../x.mjs', '--reason', 'r']);
  await refused('usage', undefined, ['reopen', 'B1', '--run', f.runId, '--file', 'src//a.mjs', '--reason', 'r']);
  await refused('usage', undefined, ['reopen', 'B1', '--run', f.runId, '--file', 'src/./a.mjs', '--reason', 'r']);
  // tampered: the state file no longer matches its anchor
  const target = statePath(f.runId, 'B1', SRC);
  const saved = readFileSync(target, 'utf8');
  writeFileSync(target, saved.replace('"round":1', '"round":0'));
  assert.notEqual(readFileSync(target, 'utf8'), saved);
  await refused('reopen-tampered');
  writeFileSync(target, saved);
  // a block that is not open (stopped by the human)
  assert.equal((await f.block(['stop', 'B1', '--run', f.runId, '--reason', 'r'])).code, 0);
  await refused('reopen-block-not-open');
});

test('B79 (d): a tmp.root in the config snapshot that is set but not a string is refused reopen-config with no row; null or absent is the default root', async () => {
  const f = await fixture('b79-tmp-root');
  const recordFile = runRecordPath(f.runId);
  const recordText = readFileSync(recordFile, 'utf8');
  const record = JSON.parse(recordText);
  for (const root of [42, true, ['/FAKE-root'], { dir: '/FAKE-root' }]) {
    writeFileSync(recordFile, JSON.stringify({ ...record, config: initialConfigState({ tmp: { root } }) }));
    const r = await f.reopen();
    assert.deepEqual([r.code, r.out], [1, ''], String(root));
    assert.equal(r.err, `block reopen: reopen-config: the run's config snapshot or its tmp.root cannot be read, so the review state of ${SRC} cannot be found; nothing was recorded\n`);
    assert.deepEqual(await f.reopenedRows(), []);
  }
  // null and absent: the default root, so the reopen goes on to the file's own check (no state yet)
  for (const config of [{ tmp: { root: null } }, { tmp: {} }]) {
    writeFileSync(recordFile, JSON.stringify({ ...record, config: initialConfigState(config) }));
    const r = await f.reopen();
    assert.deepEqual([r.code, r.err], [1, `block reopen: reopen-not-stopped: ${SRC} has no review state; only a stopped or lost file is reopened; nothing was recorded\n`], JSON.stringify(config));
  }
  writeFileSync(recordFile, recordText);
  assert.deepEqual(await f.reopenedRows(), []);
});

test('B72 (5): a block closed after its approval is refused reopen-block-not-open, with no row', async () => {
  const f = await fixture('b72-closed');
  const done = await f.review();
  assert.deepEqual([done.result.status, done.result.approved], ['reviewed', true]);
  const closed = await f.close();
  assert.deepEqual([closed.code, closed.err], [0, '']);
  const r = await f.reopen();
  assert.deepEqual([r.code, r.out, r.err], [1, '', `block reopen: reopen-block-not-open: block B1 is not open in run ${f.runId}; nothing was recorded\n`]);
  assert.deepEqual(await f.reopenedRows(), []);
  // a broken config snapshot (tmp.root unreadable) does not hide the block's own code
  const recordFile = runRecordPath(f.runId);
  const forged = JSON.parse(readFileSync(recordFile, 'utf8'));
  forged.config = { hash: 'f'.repeat(64), snapshots: { ['f'.repeat(64)]: { tmp: { root: '/FAKE-root' } } } };
  writeFileSync(recordFile, JSON.stringify(forged));
  const broken = await f.reopen();
  assert.deepEqual([broken.code, broken.out, broken.err], [1, '', `block reopen: reopen-block-not-open: block B1 is not open in run ${f.runId}; nothing was recorded\n`]);
  assert.deepEqual(await f.reopenedRows(), []);
});

test('B72 (5): a tampered STOPPED state is refused reopen-tampered, with no row', async () => {
  const f = await fixture('b72-tampered-stop');
  await stopOnPatch(f);
  writeFile(f.repo, SRC, V(4));
  const target = statePath(f.runId, 'B1', SRC);
  const saved = readFileSync(target, 'utf8');
  const edited = saved.replace('"status":"stopped"', '"status":"open"');
  assert.notEqual(edited, saved);
  writeFileSync(target, edited);
  const r = await f.reopen();
  assert.deepEqual([r.code, r.out], [1, '']);
  assert.equal(r.err.startsWith('block reopen: reopen-tampered: '), true, r.err);
  assert.deepEqual(await f.reopenedRows(), []);
});

test('B72 (5): a row write that does not resolve with the row is reopen-write-failed, never a success', async () => {
  const f = await fixture('b72-write-failed');
  const record = JSON.parse(readFileSync(runRecordPath(f.runId), 'utf8'));
  let cleared = 0;
  const io = {
    owns: () => true,
    loadState: () => ({ status: 'ok', state: { status: 'stopped', stop_content_hash: 'a'.repeat(64), next: { reason: 'review_cap' } }, seq: 2 }),
    contentHash: () => 'b'.repeat(64),
    clearDone: () => {
      cleared += 1;
      return true;
    },
  };
  const base = { runId: f.runId, id: 'B1', file: SRC, reason: 'r', record, rows: [], key: f.key, writeSigned, io };
  const message = (/** @type {string} */ code) => `reopen-write-failed: the signed review.reopened row for ${SRC} was not written (${code}); run block reopen again`;
  for (const writeRow of [async () => undefined, async () => null, async () => false]) {
    await assert.rejects(reopenFile({ ...base, writeRow }), (/** @type {any} */ e) => e.code === 'reopen-write-failed' && e.message === message('no-write') && !('cause' in e));
  }
  // B79: a thrown write keeps its cause and names its code only — never the path or text it carries
  const plain = new Error('FAKE disk full at /FAKE/ledger/path');
  const denied = Object.assign(new Error('EACCES: permission denied, open /FAKE/ledger/path'), { code: 'EACCES', syscall: 'open', path: '/FAKE/ledger/path' });
  const locked = new StateError('ledger-locked', 'the ledger /FAKE/ledger/path is locked');
  // a code that is not code-shaped (a path, a number) is never named: the error's name instead
  const pathCode = Object.assign(new Error('FAKE write failed'), { code: '/FAKE/ledger/path' });
  const numberCode = Object.assign(new Error('FAKE write failed'), { code: 42 });
  // a thrown value with no code-shaped code or name (here: `undefined`, a path string) is `unknown`
  const cases = /** @type {Array<[unknown, string]>} */ ([[plain, 'Error'], [denied, 'EACCES'], [locked, 'ledger-locked'], [pathCode, 'Error'], [numberCode, 'Error'], [undefined, 'unknown'], ['/FAKE/ledger/path', 'unknown']]);
  for (const [thrown, code] of cases) {
    await assert.rejects(
      reopenFile({
        ...base,
        writeRow: async () => {
          throw thrown;
        },
      }),
      (/** @type {any} */ e) => e.code === 'reopen-write-failed' && e.message === message(code) && e.cause === thrown && 'cause' in e && !Object.keys(e).includes('cause') && !JSON.stringify(e).includes('FAKE') && !JSON.stringify(e).includes('cause') && !e.message.includes('FAKE') && !e.message.includes('/ledger/'),
    );
  }
  assert.equal(cleared, 10);
  assert.deepEqual(await f.reopenedRows(), []);
  // the happy path next to it: the ledger's writer resolves with the row
  const ok = await reopenFile({ ...base, writeRow: f.writeRow });
  assert.deepEqual([ok.existing, ok.cleared, ok.row.at_seq, ok.row.to_hash, verifyRow(ok.row, f.key).ok], [false, true, 2, 'b'.repeat(64), true]);
  assert.equal((await f.reopenedRows()).length, 1);
});

test('B72 (6): a valid reopen row, then the stop\'s own content back before its ticket runs ⇒ the stop repeats with no session', async () => {
  const f = await fixture('b72-back');
  const stop = await stopOnPatch(f);
  const stopHash = f.hashOf();
  const sessionsAtStop = f.sessions.length;
  // the human's row (as `block reopen` writes it for V(4)), then V(3) is back and its cached
  // result is gone, so the worker runs the stop's own content again
  await writeSigned(f.runId, f.writeRow, { event: 'review.reopened', block: 'B1', file: SRC, from: 'stopped', stop_reason: 'l3_patch_exhausted', at_seq: 2, from_hash: stopHash, to_hash: 'a'.repeat(64), reason: 'r', by: 'human' });
  rmSync(path.join(queueDir(f.repo), `${stop.out.ticket}.done`));
  const back = await f.review();
  assert.deepEqual([back.out.ticket, back.out.status, back.drained, back.result.status, back.result.stopped], [stop.out.ticket, 'queued', 1, 'stopped', 'l3_patch_exhausted']);
  assert.equal(f.sessions.length, sessionsAtStop);
  assert.equal(Math.max(...(await f.anchors()).map((a) => a.seq)), 2);
  // the same row on changed content restarts the file
  writeFile(f.repo, SRC, V(4));
  f.script.findings = [];
  const fresh = await f.review();
  assert.deepEqual([fresh.result.status, fresh.result.approved, fresh.result.round, fresh.result.kind], ['reviewed', true, 1, 'full']);
  assert.equal(f.sessions.length, sessionsAtStop + 1);
});

test('B72 (2)(6): the content a stop reviewed is the stop\'s own — a second stop on B (its review.cap row gone) refuses B and accepts C from B', async () => {
  const f = await fixture('b72-second');
  await stopOnPatch(f);
  const hashA = f.hashOf();
  writeFile(f.repo, SRC, V(4));
  const hashB = f.hashOf();
  assert.equal((await f.reopen()).code, 0);
  // round 1 on B with C1 open: the spent rung makes the cap a stop on B
  const stopB = await f.review();
  assert.deepEqual([stopB.result.status, stopB.result.stopped, stopB.result.round], ['stopped', 'review_cap', 1]);
  // the second stop's review.cap row is dropped from the ledger: only the state knows B
  const file = ledgerPath(f.slug);
  const lines = readFileSync(file, 'utf8').split('\n').filter((l) => l.length > 0);
  const kept = lines.filter((l) => {
    const r = JSON.parse(l);
    return !(r.event === 'review.cap' && r.content_hash === hashB);
  });
  assert.equal(lines.length - kept.length, 1);
  writeFileSync(file, `${kept.join('\n')}\n`);
  const caps = (await f.rows()).filter((r) => r.event === 'review.cap');
  assert.deepEqual(caps.map((r) => r.content_hash), [hashA]);

  const same = await f.reopen();
  assert.deepEqual([same.code, same.out], [1, '']);
  assert.equal(same.err.startsWith(`block reopen: reopen-same-content: ${SRC} still has the content its stop (review_cap) reviewed`), true, same.err);
  assert.equal((await f.reopenedRows()).length, 1);

  writeFile(f.repo, SRC, V(5));
  const ok = await f.reopen(['--json']);
  assert.equal(ok.code, 0);
  const out = JSON.parse(ok.out);
  assert.deepEqual([out.from, out.stop_reason, out.at_seq, out.from_hash, out.to_hash], ['stopped', 'review_cap', 3, hashB, f.hashOf()]);
  assert.equal((await f.reopenedRows()).length, 2);
});

test('B72 (2): a stopped state from before B72 (no stop_content_hash) uses only the review.cap row of its own stop; else the reopen is refused', async () => {
  const f = await fixture('b72-legacy');
  await stopOnPatch(f);
  const hashA = f.hashOf();
  const signedWrite = (/** @type {Record<string, any>} */ row) => writeSigned(f.runId, f.writeRow, row);
  /** Re-save the stopped state (anchored at `prevSeq`) without its `stop_content_hash`, as a pre-B72 worker would; the new anchor is `prevSeq + 1`. */
  const saveLegacy = async (/** @type {number} */ prevSeq) => {
    const saved = JSON.parse(readFileSync(statePath(f.runId, 'B1', SRC), 'utf8'));
    assert.deepEqual([saved.seq, saved.state.status, typeof saved.state.stop_content_hash], [prevSeq, 'stopped', 'string']);
    const { stop_content_hash: _drop, ...state } = saved.state;
    await saveState({ runRootDir: freshDir('runroot-legacy'), rows: [], runId: f.runId, block: 'B1', file: SRC, key: f.key, state, seq: prevSeq, writeRow: signedWrite, contentRoot: f.repo });
  };
  await saveLegacy(2);
  // the cap row of this stop (same round and reason, no reopen after it) names A
  const same = await f.reopen();
  assert.deepEqual([same.code, same.err.startsWith('block reopen: reopen-same-content: '), same.err.includes('still has the content')], [1, true, true]);
  writeFile(f.repo, SRC, V(4));
  const ok = await f.reopen(['--json']);
  assert.equal(ok.code, 0);
  assert.deepEqual([JSON.parse(ok.out).at_seq, JSON.parse(ok.out).from_hash], [3, hashA]);
  // a legacy stopped state saved after that reopen: the only cap row is older than the reopen
  const anchor = JSON.parse(readFileSync(statePath(f.runId, 'B1', SRC), 'utf8'));
  await saveState({ runRootDir: freshDir('runroot-legacy'), rows: [], runId: f.runId, block: 'B1', file: SRC, key: f.key, state: anchor.state, seq: 3, writeRow: signedWrite, contentRoot: f.repo });
  writeFile(f.repo, SRC, V(5));
  const unknown = await f.reopen();
  assert.deepEqual([unknown.code, unknown.out], [1, '']);
  assert.equal(unknown.err, `block reopen: reopen-same-content: the content the stop of ${SRC} reviewed is unknown, so a change cannot be shown; waive its findings instead (block waive); nothing was recorded\n`);
  assert.equal((await f.reopenedRows()).length, 1);
  // the worker agrees: the row at seq 3 is not usable (seq 4 is latest) and the stop repeats, no session
  const sessions = f.sessions.length;
  const again = await f.review();
  assert.deepEqual([again.result.status, again.result.stopped], ['stopped', 'l3_patch_exhausted']);
  assert.equal(f.sessions.length, sessions);
});

test('B72 (7): forge block reopen in a coder argv is caught by the forbidden list, like block waive', () => {
  const list = mergeForbidden();
  const reopen = isForbidden(['forge', 'block', 'reopen', 'B1', '--run', 'r1', '--file', SRC, '--reason', 'r'], list);
  const full = isForbidden(['code-forge', 'block', 'reopen', 'B1', '--run', 'r1', '--file', SRC, '--reason', 'r'], list);
  const waive = isForbidden(['forge', 'block', 'waive', 'B1', 'C1', '--run', 'r1', '--file', SRC, '--reason', 'r'], list);
  assert.deepEqual([reopen?.id, full?.id, waive?.id], ['code-forge-block-reopen-from-coder', 'code-forge-block-reopen-from-coder', 'code-forge-block-waive-from-coder']);
  // other block verbs stay allowed
  assert.equal(isForbidden(['forge', 'block', 'attempt', 'B1', '--run', 'r1'], list), null);
});
