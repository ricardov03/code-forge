import { commitAll, makeRepo, writeFile } from '../review/helpers.mjs';
import { humanSaysYes } from '../helpers/human-confirm.mjs';
import assert from 'node:assert/strict';
import { readFileSync, existsSync, symlinkSync } from 'node:fs';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
const { writeResult } = await import('../../src/worker/queue.mjs');
const { cfgFor } = await import('../review/helpers.mjs');

const FILE = 'src/a.mjs';
const CURRENT = 'export const a = 2;\n';
const REASON = 'src/a.mjs:1 shows the bounded case';
const AGREE = { status: 'ok', answer: { verdict: 'agree', reason: 'The cited case is bounded.' } };
let seq = 0;

async function fixture(severity = 'warning', recorded = true) {
  const slug = `waive-yes-${++seq}`;
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, 'test/a.test.mjs', '// evidence\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (pid) => pid === 4242 ? 'start-A' : null;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe, config: cfgFor() });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'first', tests: ['t'] }, { clause: 'second', tests: ['t'] }], writeRow });
  writeFile(repo, FILE, CURRENT);
  const hash = contentHash(repo, FILE);
  const key = await loadKey(runId);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', file: FILE, content_hash: hash, ...row });
  const ticket = ticketId({ run: runId, block: 'B1', file: FILE, content_hash: hash });
  const finding = { id: 'W1', severity, claim: 'The bounded case is too narrow.', evidence: 'src/a.mjs:1', file: FILE, line_start: 1, line_end: 1 };
  if (recorded) {
    await sign({ event: 'review.round', round: 1, kind: 'full', open_after: 1 });
    await sign({ event: 'review.result', ticket, status: 'reviewed', approved: false, open: [{ id: 'W1', severity }] });
    writeResult({ repoRoot: repo, runId, ticket, key, result: { event: 'review.result', block: 'B1', file: FILE, content_hash: hash, status: 'reviewed', approved: false, findings: [finding] } });
  }
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  const waivers = async () => (await rows()).filter((r) => r.event === 'review.waived');
  const calls = [];
  let prompts = 0;
  const judge = async (opts) => {
    calls.push({ ...opts, packet: JSON.parse(readFileSync(opts.promptPath, 'utf8')) });
    return AGREE;
  };
  /** @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string]>} */
  const invoke = async (args, deps = {}) => {
    let out = '';
    const stream = { write(s) { out += s; return true; } };
    const code = await runBlock(args, /** @type {any} */ ({ stdout: stream, stderr: stream, probe, isTTY: false,
      ui: { confirm: async () => { prompts++; return true; }, isCancel: () => false }, waiverJudge: judge, ...deps }));
    return [code, out];
  };
  const waive = (reason = REASON, deps = {}, extra = ['--yes']) => invoke(['waive', 'B1', 'W1', '--run', runId, '--file', FILE, '--reason', reason, ...extra], deps);
  const close = (extra = []) => invoke(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...extra]);
  return { repo, runId, hash, key, finding, ticket, sign, writeRow, rows, waivers, calls, invoke, waive, close, prompts: () => prompts };
}

test('clause 1: warning and nit --yes need no terminal or prompt and write exactly one signed agent waiver', async () => {
  for (const severity of ['warning', 'nit']) {
    const f = await fixture(severity);
    const before = (await f.rows()).length;
    assert.deepEqual(await f.waive(), [0, `block B1 waived W1 in ${FILE} (by: agent)\n`]);
    const rows = await f.rows();
    assert.equal(rows.length - before, 1, `${severity}: exactly one row written`);
    const ws = await f.waivers();
    assert.equal(ws.length, 1);
    assert.deepEqual([ws[0].by, ws[0].reason, ws[0].severity, ws[0].verdict, ws[0].judge_reason], ['agent', REASON, severity, 'agree', AGREE.answer.reason]);
    assert.deepEqual(verifyRow(ws[0], f.key), { ok: true });
    assert.equal(f.prompts(), 0);
    assert.equal(f.calls.length, 1);
  }
});

test('clause 2: critical, unknown and unrecorded severities refuse exactly waive-yes-critical with zero rows and zero judge calls', async () => {
  for (const severity of ['critical', 'unknown', 'missing', 'unsigned', 'tampered']) {
    // A forged nit row attempts to downgrade a signed critical finding. Trusting that row
    // would let the valid evidence and agreeing judge waive it; the recorded critical must win.
    const f = await fixture(severity === 'unsigned' || severity === 'tampered' ? 'critical' : severity, severity !== 'missing');
    if (severity === 'unsigned' || severity === 'tampered') {
      const row = { run: f.runId, block: 'B1', file: FILE, event: 'review.triage', finding: 'W1', severity: 'nit' };
      await f.writeRow(severity === 'unsigned' ? row : { ...row, mac: 'bad' });
    }
    const before = (await f.rows()).length;
    const [code, text] = await f.waive();
    assert.equal(code, 1, severity);
    assert.match(text, /^block waive: waive-yes-critical:/);
    assert.equal((await f.rows()).length - before, 0);
    assert.equal((await f.waivers()).length, 0);
    assert.equal(f.calls.length, 0);
  }
  const human = await fixture('critical');
  const [noTTY] = await human.waive(REASON, {}, []);
  assert.equal(noTTY, 2);
  assert.equal((await human.waivers()).length, 0);
  assert.deepEqual(await human.waive(REASON, humanSaysYes(), []), [0, `block B1 waived W1 in ${FILE} (by: human)\n`]);
  assert.deepEqual((await human.waivers()).map((r) => r.by), ['human']);
  assert.equal(human.calls.length, 0);
});

test('clause 3: each supported evidence form permits exactly one waiver', async () => {
  for (const reason of [REASON, 'test/a.test.mjs covers it', 'Acceptance clause 2 permits it']) {
    const f = await fixture();
    assert.equal((await f.waive(reason))[0], 0, reason);
    assert.equal((await f.waivers()).length, 1);
    assert.equal(f.calls.length, 1);
  }
});

test('clause 3 boundary: missing files, escaped paths, symlinks, bare source paths and out-of-range clauses refuse before judging', async () => {
  for (const reason of ['looks fine', 'src/missing.mjs:1', 'src/a.mjs', 'test/missing.test.mjs', 'clause 0', 'clause 3', '/src/a.mjs:1', '../src/a.mjs:1', 'src/link.mjs:1']) {
    const f = await fixture();
    symlinkSync(`${f.repo}/${FILE}`, `${f.repo}/src/link.mjs`);
    const before = (await f.rows()).length;
    const [code, text] = await f.waive(reason);
    assert.equal(code, 1, reason);
    assert.match(text, /^block waive: waive-yes-no-evidence:/);
    assert.equal((await f.rows()).length - before, 0);
    assert.equal((await f.waivers()).length, 0);
    assert.equal(f.calls.length, 0);
  }
});

test('clause 4: one fresh L3 judge gets recorded finding, reason and current file bytes; its agree writes one waiver', async () => {
  const f = await fixture();
  assert.equal((await f.waive())[0], 0);
  assert.equal(f.calls.length, 1);
  const call = f.calls[0];
  assert.deepEqual([call.level, call.role, call.resume, call.sessionId, call.cwd], ['L3', 'judge', undefined, undefined, undefined]);
  assert.deepEqual(call.packet, { finding: { id: 'W1', severity: 'warning', claim: f.finding.claim, evidence: f.finding.evidence }, reason: REASON, file: { path: FILE, content: CURRENT } });
  assert.equal(existsSync(call.promptPath), false, 'packet removed after judge');
  assert.equal((await f.waivers()).length, 1);
});

test('clause 4 boundary: disagreement, broken shape, failed session and thrown judge write zero rows and leave finding open', async () => {
  const cases = [
    { status: 'ok', answer: { verdict: 'disagree', reason: 'The boundary is not covered.' } },
    { status: 'ok', answer: { verdict: 'agree' } },
    { status: 'ok', answer: { verdict: 'agree', reason: 'x', severity: 'nit' } },
    { status: 'ok', answer: { verdict: 'agree', reason: ' ' } },
    { status: 'timeout', answer: AGREE.answer },
    null,
  ];
  for (const result of cases) {
    const f = await fixture();
    let calls = 0;
    const before = (await f.rows()).length;
    const [code, text] = await f.waive(REASON, { waiverJudge: async () => { calls++; if (result === null) throw new Error('failed'); return result; } });
    assert.equal(code, 1);
    assert.match(text, /^block waive: waive-yes-disagreed:/);
    if (result?.answer?.verdict === 'disagree') assert.match(text, /The boundary is not covered\./);
    assert.equal(calls, 1);
    assert.equal((await f.rows()).length - before, 0);
    assert.equal((await f.waivers()).length, 0);
    const [closed, why] = await f.close();
    assert.equal(closed, 1);
    assert.match(why, /not_approved src\/a.mjs W1/);
  }
});

test('clause 5: signed agent agree counts as reviewed_by_waiver and close prints each waiver', async () => {
  for (const kind of ['unsigned', 'tampered', 'disagree', 'critical']) {
    const f = await fixture(kind === 'critical' ? 'critical' : 'warning');
    const row = { event: 'review.waived', block: 'B1', file: FILE, finding: 'W1', by: 'agent', severity: kind === 'critical' ? 'critical' : 'warning', reason: REASON, verdict: kind === 'disagree' ? 'disagree' : 'agree', judge_reason: 'bounded' };
    if (kind === 'unsigned' || kind === 'tampered') await f.writeRow({ run: f.runId, ...row, ...(kind === 'tampered' ? { mac: 'bad' } : {}) });
    else await f.sign(row);
    const [code, text] = await f.close();
    assert.equal(code, 1, kind);
    assert.match(text, /not_approved src\/a.mjs W1/);
    assert.equal((await f.rows()).filter((r) => r.event === 'gate.review_waived_all').length, 0);
  }
  const f = await fixture();
  await f.sign({ event: 'review.waived', finding: 'W1', by: 'agent', severity: 'warning', reason: REASON, verdict: 'agree', judge_reason: 'bounded' });
  const [code, text] = await f.close();
  assert.equal(code, 0);
  assert.equal(text, `reviewed ${FILE} ticket ${f.ticket}\nblock B1: ${FILE} reviewed_by_waiver (W1; by agent)\nblock B1: agent waiver ${FILE} W1 reason: ${REASON}; verdict: agree\nblock B1 closed\n`);
  const rows = (await f.rows()).filter((r) => r.event === 'gate.review_waived_all');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].by, 'agent');
  assert.deepEqual(verifyRow(rows[0], f.key), { ok: true });
});

test('clause 5: close --json lists the exact agent waiver fields', async () => {
  const f = await fixture();
  await f.sign({ event: 'review.waived', finding: 'W1', by: 'agent', severity: 'warning', reason: REASON, verdict: 'agree', judge_reason: 'bounded' });
  const [code, text] = await f.close(['--json']);
  assert.equal(code, 0);
  const json = JSON.parse(text);
  assert.deepEqual(json.agent_waivers, [{ file: FILE, finding: 'W1', severity: 'warning', reason: REASON, verdict: 'agree' }]);
  assert.equal(json.reviewed_by_waiver.length, 1);
  assert.equal(json.reviewed_by_waiver[0].by, 'agent');
});
