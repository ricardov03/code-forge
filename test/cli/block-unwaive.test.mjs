import { cfgFor, commitAll, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
const { writeResult } = await import('../../src/worker/queue.mjs');
const FILE = 'src/a.mjs';
let seq = 0;

async function fixture() {
  const slug = `unwaive-${++seq}`, runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (pid) => pid === 4242 ? 'start-A' : null;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe, config: cfgFor() });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'bounded', tests: ['t'] }], writeRow });
  writeFile(repo, FILE, 'export const a = 2;\n');
  const hash = contentHash(repo, FILE), key = await loadKey(runId);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', file: FILE, content_hash: hash, ...row });
  const ticket = ticketId({ run: runId, block: 'B1', file: FILE, content_hash: hash });
  await sign({ event: 'review.round', round: 1, kind: 'full', open_after: 1 });
  await sign({ event: 'review.result', ticket, status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
  writeResult({ repoRoot: repo, runId, ticket, key, result: { event: 'review.result', block: 'B1', file: FILE, content_hash: hash, status: 'reviewed', approved: false, findings: [{ id: 'W1', severity: 'warning', claim: 'bounded?', evidence: `${FILE}:1` }] } });
  // Isolate cancellation from the separate rule that unsigned rows raise the proof tier.
  await sign({ event: 'review.waived', finding: 'proof', by: 'human', reason: 'proof covered by this fixture' });
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  let prompts = 0, judges = 0;
  /** @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string, string]>} */
  const invoke = async (args, deps = {}) => {
    let out = '', err = '';
    const code = await runBlock(args, { probe, isTTY: true, stdout: { write(s) { out += s; } }, stderr: { write(s) { err += s; } },
      ui: { confirm: async () => { prompts++; return true; }, isCancel: (v) => typeof v === 'symbol' },
      waiverJudge: async () => { judges++; return { status: 'ok', answer: { verdict: 'agree', reason: 'bounded' } }; }, ...deps });
    return [code, out, err];
  };
  const args = ['unwaive', 'B1', 'W1', '--run', runId, '--file', FILE, '--reason', 'Keep this finding open.'];
  const waive = () => invoke(['waive', 'B1', 'W1', '--run', runId, '--file', FILE, '--reason', `${FILE}:1 bounds it`, '--yes']);
  const close = () => invoke(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`]);
  return { runId, key, args, invoke, waive, close, sign, writeRow, rows, prompts: () => prompts, judges: () => judges };
}

test('B124b clause 1: yes writes exactly one signed human unwaive row', async () => {
  const f = await fixture();
  assert.equal((await f.waive())[0], 0);
  const before = (await f.rows()).length;
  assert.deepEqual(await f.invoke(f.args), [0, `block B1 unwaived W1 in ${FILE} (by: human)\n`, '']);
  const rows = (await f.rows()).slice(before);
  assert.equal(rows.length, 1);
  const { mac, ts, tokens_source, cost_source, ...row } = rows[0];
  assert.deepEqual(row, { run: f.runId, event: 'review.unwaived', block: 'B1', file: FILE, finding: 'W1', reason: 'Keep this finding open.', by: 'human' });
  assert.deepEqual(verifyRow(rows[0], f.key), { ok: true });
  assert.equal(f.prompts(), 1);
  assert.equal(f.judges(), 1);
});

test('B124b clause 1 boundary: no, cancel, no terminal and --yes write zero rows', async () => {
  const f = await fixture();
  await f.waive();
  const before = (await f.rows()).length;
  for (const answer of [false, Symbol('cancel')]) {
    let calls = 0;
    assert.deepEqual(await f.invoke(f.args, { ui: { confirm: async () => { calls++; return answer; }, isCancel: (v) => typeof v === 'symbol' } }), [1, '', 'block B1 unwaive: cancelled — nothing was recorded\n']);
    assert.equal(calls, 1);
  }
  for (const deps of [{ isTTY: false }, { stdinIsTTY: false }, { stdoutIsTTY: false }]) {
    assert.deepEqual(await f.invoke(f.args, deps), [2, '', 'block unwaive needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n']);
  }
  assert.deepEqual(await f.invoke([...f.args, '--yes']), [2, '', 'block unwaive: unknown flag "--yes"\n']);
  assert.equal(f.prompts(), 0);
  assert.equal((await f.rows()).length - before, 0);
});

test('B124b clause 2: unknown block, unowned file and missing verified agent waiver refuse before prompting', async () => {
  for (const kind of ['block', 'file', 'missing', 'human', 'unsigned', 'tampered']) {
    const f = await fixture();
    const row = { event: 'review.waived', block: 'B1', file: FILE, finding: 'W1', by: kind === 'human' ? 'human' : 'agent', reason: 'bounded', severity: 'warning', verdict: 'agree' };
    if (kind === 'unsigned' || kind === 'tampered') await f.writeRow({ run: f.runId, ...row, ...(kind === 'tampered' ? { mac: 'bad' } : {}) });
    else if (kind !== 'missing') await f.sign(row);
    const before = (await f.rows()).length;
    const args = f.args.map((v) => kind === 'block' && v === 'B1' ? 'B9' : kind === 'file' && v === FILE ? 'src/other.mjs' : v);
    const [code, out, err] = await f.invoke(args);
    assert.deepEqual([code, out], [1, ''], kind);
    assert.match(err, /^block unwaive: unwaive-no-agent-waiver:/, kind);
    assert.equal(f.prompts(), 0, kind);
    assert.equal((await f.rows()).length - before, 0, kind);
  }
});

test('B124b clause 3: unwaive reopens the finding, cancels all earlier agent waivers, and a later judged waiver counts', async () => {
  const f = await fixture();
  assert.equal((await f.waive())[0], 0);
  assert.equal((await f.waive())[0], 0);
  assert.equal((await f.invoke(f.args))[0], 0);
  const before = (await f.rows()).length;
  const [code, , err] = await f.close();
  assert.equal(code, 1);
  assert.match(err, /not_approved src\/a.mjs W1/);
  assert.equal((await f.rows()).length - before, 0);
  assert.equal((await f.waive())[0], 0);
  const [closed, out] = await f.close();
  assert.equal(closed, 0);
  assert.equal(out.split('\n').filter((l) => l.includes('agent waiver ')).length, 1);
  assert.equal(f.prompts(), 1);
  assert.equal(f.judges(), 3);
});

test('B124b clause 3 boundary: unsigned, tampered and other-scope unwaives do not cancel; human waivers survive', async () => {
  for (const kind of ['unsigned', 'tampered', 'other-block', 'other-run', 'other-file', 'other-finding', 'human']) {
    const f = await fixture();
    await f.waive();
    if (kind === 'human') await f.sign({ event: 'review.waived', finding: 'W1', by: 'human', reason: 'owner accepts' });
    const row = { event: 'review.unwaived', block: 'B1', file: FILE, finding: 'W1', by: 'human', reason: 'undo' };
    if (kind === 'unsigned' || kind === 'tampered') await f.writeRow({ run: f.runId, ...row, ...(kind === 'tampered' ? { mac: 'bad' } : {}) });
    else await f.sign({ ...row, ...(kind === 'other-block' ? { block: 'B2' } : kind === 'other-run' ? { run: 'r-other' } : kind === 'other-file' ? { file: 'src/other.mjs' } : kind === 'other-finding' ? { finding: 'W2' } : {}) });
    const [code, out, err] = await f.close();
    assert.equal(code, 0, `${kind}: ${err}`);
    assert.equal(out.includes('by human'), kind === 'human', kind);
    assert.equal(out.split('\n').filter((l) => l.includes('agent waiver ')).length, kind === 'human' ? 0 : 1, kind);
  }
});
