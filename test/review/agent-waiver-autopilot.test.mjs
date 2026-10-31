import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { cfgFor, commitAll, makeRepo, writeFile } from './helpers.mjs';
import { humanSaysYes } from '../helpers/human-confirm.mjs';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, readRun, saveRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { fileBaseline } = await import('../../src/gates/expand.mjs');
const { checkBlockReviews } = await import('../../src/review/gate-check.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');

const FILE = 'src/a.mjs';
const TEST = 'test/a.test.mjs';
const REASON = `${TEST}:1 proves the bounded case at ${FILE}:1-2`;
let sequence = 0;

async function fixture({ grant = 'active', severity = 'critical' } = {}) {
  const slug = `critical-agent-${++sequence}`;
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 1;\n// bounded\n');
  writeFile(repo, TEST, '// bounded case passes\n');
  writeFile(repo, 'test/other.test.mjs', '// not acceptance evidence\n');
  await commitAll(repo);
  const writeRow = (row) => appendRow(row, { slug });
  const probe = async (pid) => pid === 4242 ? 'start-A' : null;
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe, config: cfgFor() });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE, TEST], tests: [TEST],
    acceptance: [{ clause: 'bounded case', tests: [TEST] }], writeRow });
  const record = await readRun(runId);
  if (grant !== 'none') record.autopilot = { status: grant === 'ended' ? 'active' : grant,
    until: new Date(Date.now() + (grant === 'expired' ? -60000 : 600000)).toISOString() };
  if (grant === 'ended') record.status = 'ended';
  await saveRun(record);
  const hash = contentHash(repo, FILE);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', content_hash: hash, ...row });
  await sign({ event: 'review.round', file: FILE, round: 1, kind: 'full', open_after: 1 });
  await sign({ event: 'review.result', file: FILE, status: 'reviewed', approved: false,
    open: [{ id: 'C1', severity, claim: 'Bounded case is unsafe.', evidence: `${FILE}:1` }] });
  const rows = async () => (await readAllRows(slug)).filter((row) => row.run === runId);
  const calls = [];
  const invoke = async (args, deps = {}) => {
    let output = '';
    const stream = { write(text) { output += text; return true; } };
    const code = await runBlock(args, { stdout: stream, stderr: stream, probe, isTTY: false,
      waiverJudge: async (opts) => {
        calls.push({ ...opts, packet: JSON.parse(readFileSync(opts.promptPath, 'utf8')) });
        return { status: 'ok', answer: { verdict: 'agree', reason: 'Bounded case is covered.' } };
      }, ...deps });
    return { code, output };
  };
  const waive = (reason = REASON, deps = {}) => invoke(['waive', 'B1', 'C1', '--run', runId,
    '--file', FILE, '--reason', reason, '--yes'], deps);
  const green = async (extra = {}) => sign({ event: 'gate.done', all_ok: true, scope: 'related', test_count: 1,
    results: [{ gate: 'test', ok: true, skipped: false }],
    files: (await fileBaseline(repo, [FILE, TEST])).files, ...extra });
  const key = await loadKey(runId);
  const gate = async () => checkBlockReviews({ block: 'B1', runId, key, rows: await rows(),
    files: [{ file: FILE, content_hash: hash }], proofFiles: [], highPaths: [] });
  return { repo, runId, rows, sign, writeRow, calls, invoke, waive, green, key, gate };
}

test('without an active grant critical --yes refuses and records zero rows', async () => {
  for (const grant of ['none', 'stopped', 'expired', 'ended']) {
    const f = await fixture({ grant });
    await f.green();
    const before = await f.rows();
    const result = await f.waive();
    assert.equal(result.code, 1, grant);
    assert.match(result.output, /waive-yes-critical:.*active autopilot grant/, grant);
    assert.deepEqual(await f.rows(), before, `${grant}: zero rows, including expiry rows`);
    assert.equal(f.calls.length, 0, `${grant}: zero judge calls`);
  }
});

test('active grant with passing acceptance evidence and L3 agreement writes one undoable agent waiver; warnings retain their evidence forms', async () => {
  const f = await fixture();
  await f.green();
  const before = (await f.rows()).length;
  assert.equal((await f.waive()).code, 0, 'critical waiver succeeds');
  const rows = await f.rows();
  assert.equal(rows.length - before, 1, 'exactly one row written');
  const waiver = rows.at(-1);
  assert.deepEqual([waiver.event, waiver.by, waiver.autopilot, waiver.test, waiver.severity, waiver.verdict],
    ['review.waived', 'agent', true, TEST, 'critical', 'agree']);
  assert.deepEqual(verifyRow(waiver, f.key), { ok: true }, 'waiver is signed');
  assert.equal(f.calls.length, 1, 'one fresh judge call');
  assert.equal(f.calls[0].level, 'L3');
  assert.deepEqual(f.calls[0].packet.evidence.map((item) => item.path), [TEST, FILE]);
  assert.deepEqual(await f.gate(), { ok: true, refusals: [], waivedAll: [
    { file: FILE, content_hash: contentHash(f.repo, FILE), status: 'reviewed_by_waiver', findings: ['C1'], by: 'agent' },
  ] }, 'the critical waiver clears exactly C1 at close');
  const undone = await f.invoke(['unwaive', 'B1', 'C1', '--run', f.runId, '--file', FILE,
    '--reason', 'Owner requests a fix'], humanSaysYes());
  assert.equal(undone.code, 0, undone.output);
  assert.deepEqual((await f.rows()).filter((row) => row.event === 'review.unwaived').map((row) => row.by), ['human']);
  assert.equal((await f.gate()).ok, false, 'human undo reopens the critical finding at close');
  for (const severity of ['warning', 'nit']) {
    for (const reason of [`${FILE}:1`, TEST, 'clause 1']) {
      const warning = await fixture({ grant: 'none', severity });
      const count = (await warning.rows()).length;
      assert.equal((await warning.waive(reason)).code, 0, `${severity}: ${reason}`);
      const added = (await warning.rows()).slice(count);
      assert.equal(added.length, 1, 'one warning/nit waiver');
      assert.deepEqual([added[0].by, added[0].severity, added[0].autopilot], ['agent', severity, undefined]);
      assert.equal(warning.calls.length, 1);
    }
  }
});

test('critical boundaries refuse missing or stale passing acceptance evidence, missing finding citation, and judge disagreement without writes', async () => {
  for (const kind of ['no-record', 'failed', 'skipped', 'stale-source', 'stale-test', 'latest-red', 'unsigned', 'wrong-block', 'no-test-citation', 'not-acceptance', 'no-path', 'other-path', 'disagree', 'grant-stopped-in-judge']) {
    const f = await fixture();
    if (kind !== 'no-record') await f.green();
    if (kind === 'failed') await f.green({ all_ok: false, results: [{ gate: 'test', ok: false, skipped: false }] });
    if (kind === 'skipped') await f.green({ results: [{ gate: 'test', ok: true, skipped: true }] });
    if (kind === 'stale-source') writeFile(f.repo, FILE, 'export const a = 3;\n');
    if (kind === 'stale-test') writeFile(f.repo, TEST, '// changed test\n');
    if (kind === 'latest-red') await f.sign({ event: 'gate.red', gate: 'test', code: 1 });
    if (kind === 'unsigned') await f.writeRow({ run: f.runId, block: 'B1', event: 'gate.done', all_ok: true });
    if (kind === 'wrong-block') {
      await f.green({ all_ok: false });
      await f.green({ block: 'B2' });
    }
    const reason = kind === 'no-test-citation' ? `${FILE}:1`
      : kind === 'not-acceptance' ? `test/other.test.mjs:1 ${FILE}:1`
      : kind === 'no-path' ? TEST : kind === 'other-path' ? `${TEST}:1` : REASON;
    const before = await f.rows();
    let disagreed = 0;
    const result = await f.waive(reason, ['disagree', 'grant-stopped-in-judge'].includes(kind) ? { waiverJudge: async () => {
      disagreed++;
      if (kind === 'grant-stopped-in-judge') {
        const record = await readRun(f.runId);
        record.autopilot.status = 'stopped';
        await saveRun(record);
      }
      return { status: 'ok', answer: { verdict: kind === 'disagree' ? 'disagree' : 'agree', reason: 'The test misses the boundary.' } };
    } } : {});
    assert.equal(result.code, 1, kind);
    const code = ['no-path', 'other-path'].includes(kind) ? 'waive-yes-no-evidence'
      : kind === 'disagree' ? 'waive-yes-disagreed'
        : kind === 'grant-stopped-in-judge' ? 'waive-yes-critical' : 'waive-yes-critical-no-test';
    assert.ok(result.output.includes(`${code}:`), `${kind}: ${result.output}`);
    if (kind === 'disagree') assert.match(result.output, /The test misses the boundary\./);
    else if (code === 'waive-yes-no-evidence') assert.match(result.output, /src\/a\.mjs.*:line/);
    else if (kind === 'grant-stopped-in-judge') assert.match(result.output, /active autopilot grant/);
    else assert.match(result.output, /acceptance test|acceptance.*test/);
    assert.deepEqual(await f.rows(), before, `${kind}: zero rows`);
    assert.equal(f.calls.length, 0, `${kind}: zero agreeing judge calls`);
    assert.equal(disagreed, ['disagree', 'grant-stopped-in-judge'].includes(kind) ? 1 : 0, `${kind}: custom judge calls`);
  }
});
