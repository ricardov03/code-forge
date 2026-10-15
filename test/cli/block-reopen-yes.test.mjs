import { commitAll, makeRepo, writeFile, cfgFor, freshDir } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
const { queueDir, writeResult } = await import('../../src/worker/queue.mjs');
const { saveState, loadState } = await import('../../src/worker/review-state.mjs');
const { reopenRowFor } = await import('../../src/review/reopen.mjs');
const { newFileState } = await import('../../src/review/fixloop.mjs');

const FILE = 'src/a.mjs';
const CURRENT = 'export const a = 2;\n';
const REASON = 'src/a.mjs:1 fixes the stopped case';
const FINDING = { id: 'C1', severity: 'critical', claim: 'The old value fails.', evidence: 'src/a.mjs:1', file: FILE, line_start: 1, line_end: 1 };
const AGREE = { status: 'ok', answer: { verdict: 'agree', reason: 'The cited change addresses the stop.' } };
let seq = 0;

async function fixture() {
  const slug = `reopen-yes-${++seq}`;
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, 'test/a.test.mjs', '// evidence\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (pid) => pid === 4242 ? 'start-A' : null;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe, config: cfgFor() });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'first', tests: ['t'] }], writeRow });
  const key = await loadKey(runId);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', file: FILE, ...row });
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  const reopens = async () => (await rows()).filter((r) => r.event === 'review.reopened');
  const runRootDir = freshDir('runroot');
  let stateSeq = 0;
  const stop = async (reason = 'l3_patch_exhausted', status = 'stopped') => {
    const hash = contentHash(repo, FILE);
    const state = { ...newFileState({ file: FILE, level: 'L2' }), status, round: 2, open: [FINDING], stop_content_hash: hash, l3_rung_used: true, next: { action: 'stop', reason } };
    await saveState({ runId, block: 'B1', file: FILE, key, state: /** @type {any} */ (state), seq: stateSeq++, writeRow: sign, runRootDir, rows: [] });
    return hash;
  };
  const fromHash = await stop();
  writeFile(repo, FILE, CURRENT);
  const hash = contentHash(repo, FILE);
  const ticket = ticketId({ run: runId, block: 'B1', file: FILE, content_hash: hash });
  writeResult({ repoRoot: repo, runId, ticket, key, result: { event: 'review.result', block: 'B1', file: FILE, content_hash: hash, status: 'stopped', stopped: 'l3_patch_exhausted', approved: false, findings: [FINDING] } });
  const marker = path.join(queueDir(repo), `${ticket}.done`);
  const calls = [];
  let prompts = 0;
  const judge = async (opts) => { calls.push({ ...opts, packet: JSON.parse(readFileSync(opts.promptPath, 'utf8')) }); return AGREE; };
  const invoke = async (args, deps = {}) => {
    let text = '';
    const stream = { write(s) { text += s; return true; } };
    const code = await runBlock(args, { stdout: stream, stderr: stream, probe, isTTY: false,
      ui: { confirm: async () => { prompts++; return true; }, isCancel: () => false }, waiverJudge: judge, ...deps });
    return /** @type {[number, string]} */ ([code, text]);
  };
  const reopen = (reason = REASON, deps = {}, extra = ['--yes'], file = FILE, id = 'B1') => invoke(['reopen', id, '--run', runId, '--file', file, '--reason', reason, ...extra], deps);
  const close = (extra = []) => invoke(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...extra]);
  const loaded = async () => loadState({ runId, block: 'B1', file: FILE, key, runRootDir, rows: await rows(), migrate: false });
  return { repo, runId, key, sign, writeRow, rows, reopens, stop, fromHash, hash, ticket, marker, calls, reopen, close, loaded, prompts: () => prompts };
}

test('clause 1: --yes without a terminal or confirm clears the marker and writes exactly one signed, usable agent reopen', async () => {
  const f = await fixture();
  assert.equal(existsSync(f.marker), true);
  const before = (await f.rows()).length;
  assert.deepEqual(await f.reopen(), [0, `block B1 reopened ${FILE} (from stopped l3_patch_exhausted; by: agent)\n`]);
  assert.equal((await f.rows()).length - before, 1);
  const rows = await f.reopens();
  assert.equal(rows.length, 1);
  assert.deepEqual([rows[0].by, rows[0].reason, rows[0].verdict], ['agent', REASON, AGREE.answer]);
  assert.deepEqual([rows[0].from, rows[0].stop_reason, rows[0].at_seq, rows[0].from_hash, rows[0].to_hash], ['stopped', 'l3_patch_exhausted', 1, f.fromHash, f.hash]);
  assert.deepEqual(verifyRow(rows[0], f.key), { ok: true });
  assert.equal(existsSync(f.marker), false);
  const loaded = await f.loaded();
  assert.deepEqual(reopenRowFor(await f.rows(), { runId: f.runId, block: 'B1', file: FILE, key: f.key, ...loaded, contentHash: f.hash }), rows[0]);
  assert.equal(f.prompts(), 0);
  assert.equal(f.calls.length, 1);
});

test('clause 1 boundary: usage and all existing reopen pre-checks precede evidence, judging and effects', async () => {
  for (const kind of ['usage', 'unknown_block', 'reopen-block-not-open', 'reopen-not-owned', 'reopen-tampered', 'reopen-not-stopped', 'reopen-same-content', 'reopen-already-recorded']) {
    const f = await fixture();
    if (kind === 'reopen-not-stopped') await f.stop('review_stall', 'complete');
    if (kind === 'reopen-same-content') await f.stop();
    if (kind === 'reopen-already-recorded') await f.sign({ event: 'review.reopened', by: 'human', at_seq: 1, to_hash: 'other' });
    if (kind === 'reopen-tampered') await f.writeRow({ run: f.runId, block: 'B1', file: FILE, event: 'review.state', seq: 99, mac: 'bad' });
    if (kind === 'reopen-block-not-open') {
      // Use the real stop verb; the reopen must refuse the block before looking at evidence.
      let text = '';
      assert.equal(await runBlock(['stop', 'B1', '--run', f.runId, '--reason', 'owner stopped'], { stdout: { write(s) { text += s; } }, probe: async () => 'start-A' }), 0, text);
    }
    const before = (await f.rows()).length;
    const [code, text] = await f.reopen('no evidence', {}, ['--yes'], kind === 'usage' ? '../bad' : kind === 'reopen-not-owned' ? 'src/other.mjs' : FILE, kind === 'unknown_block' ? 'B2' : 'B1');
    assert.equal(code, kind === 'usage' ? 2 : 1, kind);
    if (kind === 'unknown_block') assert.equal(text, `block reopen: block B2 is not in run ${f.runId}\n`);
    else assert.match(text, new RegExp(`^block reopen: ${kind === 'usage' ? '' : `${kind}:`}`), kind);
    assert.equal((await f.rows()).length - before, 0, kind);
    assert.equal((await f.reopens()).length, kind === 'reopen-already-recorded' ? 1 : 0, kind);
    assert.equal(f.calls.length, 0, kind);
    assert.equal(f.prompts(), 0, kind);
    assert.equal(existsSync(f.marker), true, kind);
  }
});

test('clause 2: the shared evidence forms allow one reopen each; no evidence refuses with zero writes or judge calls', async () => {
  for (const reason of [REASON, 'test/a.test.mjs covers the fix', 'acceptance clause 1 permits this', 'looks fixed', 'src/missing.mjs:1', 'src/a.mjs', 'clause 2']) {
    const f = await fixture();
    const valid = [REASON, 'test/a.test.mjs covers the fix', 'acceptance clause 1 permits this'].includes(reason);
    const before = (await f.rows()).length;
    const [code, text] = await f.reopen(reason);
    assert.equal(code, valid ? 0 : 1, reason);
    if (!valid) assert.match(text, /^block reopen: reopen-yes-no-evidence:/);
    assert.equal((await f.rows()).length - before, valid ? 1 : 0, reason);
    assert.equal((await f.reopens()).length, valid ? 1 : 0, reason);
    assert.equal(f.calls.length, valid ? 1 : 0, reason);
    assert.equal(f.prompts(), 0, reason);
    assert.equal(existsSync(f.marker), !valid, reason);
  }
});

test('clause 3: a fresh L3 reopen judge receives the stop, last findings, reason and current bytes', async () => {
  const f = await fixture();
  const [code, text] = await f.reopen(REASON, {}, ['--yes', '--json']);
  assert.equal(code, 0);
  assert.equal(JSON.parse(text).by, 'agent');
  assert.equal(f.calls.length, 1);
  const call = f.calls[0];
  assert.deepEqual([call.level, call.role, call.resume, call.sessionId, call.cwd], ['L3', 'judge', undefined, undefined, undefined]);
  assert.match(call.systemPromptText, /reopen/i);
  assert.deepEqual(call.packet, { stop_reason: 'l3_patch_exhausted', findings: [FINDING], reason: REASON, file: { path: FILE, content: CURRENT } });
  assert.equal(existsSync(call.promptPath), false);
  assert.equal((await f.reopens()).length, 1);
  assert.equal(f.prompts(), 0);
});

test('clause 3 boundary: disagreement or a failed or invalid judge leaves the file stopped, the marker intact and zero rows', async () => {
  for (const answer of [{ status: 'ok', answer: { verdict: 'disagree', reason: 'The stop is not addressed.' } }, { status: 'ok', answer: { verdict: 'agree' } }, { status: 'timeout', answer: AGREE.answer }, null]) {
    const f = await fixture();
    let calls = 0;
    const before = (await f.rows()).length;
    const [code, text] = await f.reopen(REASON, { waiverJudge: async () => { calls++; if (answer === null) throw new Error('failed'); return answer; } });
    assert.equal(code, 1);
    assert.match(text, /^block reopen: reopen-yes-disagreed:/);
    if (answer?.answer?.verdict === 'disagree') assert.match(text, /The stop is not addressed\./);
    assert.equal((await f.rows()).length - before, 0);
    assert.equal((await f.reopens()).length, 0);
    assert.equal((await f.loaded()).state.status, 'stopped');
    assert.equal(existsSync(f.marker), true);
    assert.equal(calls, 1);
    assert.equal(f.prompts(), 0);
  }
});

test('clause 4: one agent reopen per run/block/file, even at a new stop; the human path still works after the limit', async () => {
  const f = await fixture();
  assert.equal((await f.reopen())[0], 0);
  await f.stop('review_stall');
  writeFile(f.repo, FILE, 'export const a = 3;\n');
  const before = (await f.rows()).length;
  assert.deepEqual(await f.reopen(), [1, `block reopen: reopen-yes-limit: ${FILE} already has an agent reopen in this run; nothing was recorded\n`]);
  assert.equal((await f.rows()).length - before, 0);
  assert.equal((await f.reopens()).length, 1);
  assert.equal(f.calls.length, 1);
  assert.equal(f.prompts(), 0);
  assert.deepEqual(await f.reopen('owner retries', { isTTY: true }, []), [0, `block B1 reopened ${FILE} (from stopped review_stall; by: human)\n`]);
  assert.deepEqual((await f.reopens()).map((r) => r.by), ['agent', 'human']);
  assert.equal(f.calls.length, 1);
  assert.equal(f.prompts(), 1);
});

test('clause 4 boundary: unsigned, tampered and other run/block/file agent rows do not spend the limit', async () => {
  const f = await fixture();
  const row = { event: 'review.reopened', by: 'agent', verdict: AGREE.answer, at_seq: 0, to_hash: 'old' };
  await f.writeRow({ run: f.runId, block: 'B1', file: FILE, ...row });
  await f.writeRow({ run: f.runId, block: 'B1', file: FILE, ...row, mac: 'bad' });
  await f.sign({ ...row, run: 'r-other' });
  await f.sign({ ...row, block: 'B2' });
  await f.sign({ ...row, file: 'src/other.mjs' });
  const before = (await f.rows()).length;
  assert.equal((await f.reopen())[0], 0);
  assert.equal((await f.rows()).length - before, 1);
  assert.equal((await f.reopens()).filter((r) => r.block === 'B1' && r.file === FILE && verifyRow(r, f.key).ok).length, 1);
  assert.equal(f.calls.length, 1);
});

test('clause 5: close prints each verified agent reopen and JSON adds only a nonempty agent_reopens list', async () => {
  for (const kind of ['text', 'json', 'none', 'human', 'unsigned', 'tampered', 'other-block', 'other-run']) {
    const f = await fixture();
    if (kind === 'text' || kind === 'json') assert.equal((await f.reopen())[0], 0);
    else if (kind !== 'none') {
      const row = { event: 'review.reopened', reason: REASON, by: kind === 'human' ? 'human' : 'agent', verdict: AGREE.answer };
      if (kind === 'unsigned' || kind === 'tampered') await f.writeRow({ run: f.runId, block: 'B1', file: FILE, ...row, ...(kind === 'tampered' ? { mac: 'bad' } : {}) });
      else await f.sign({ ...row, ...(kind === 'other-block' ? { block: 'B2' } : {}), ...(kind === 'other-run' ? { run: 'r-other' } : {}) });
    }
    await f.sign({ event: 'review.round', content_hash: f.hash, round: 1, open_after: 0 });
    await f.sign({ event: 'review.approved', content_hash: f.hash, round: 1 });
    const [code, text] = await f.close(kind === 'text' ? [] : ['--json']);
    if (kind === 'unsigned' || kind === 'tampered') {
      assert.equal(code, 1, kind);
      assert.equal(text, `block B1 open: unproven ${FILE}\n${JSON.stringify({ block: 'B1', ok: false, reason: `unproven ${FILE}` })}\n`);
      continue;
    }
    assert.equal(code, 0, `${kind}: ${text}`);
    if (kind === 'text') assert.equal(text, `reviewed ${FILE} ticket ${f.ticket}\nagent reopen ${FILE}: ${REASON} (verdict: agree)\nblock B1 closed\n`);
    else if (kind === 'json') assert.deepEqual(JSON.parse(text).agent_reopens, [{ file: FILE, reason: REASON, verdict: AGREE.answer }]);
    else assert.equal(Object.hasOwn(JSON.parse(text), 'agent_reopens'), false, kind);
  }
});
