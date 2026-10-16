import { cfgFor, commitAll, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { writeResult } = await import('../../src/worker/queue.mjs');
const { startGrant } = await import('../../src/autopilot/grant.mjs');
let seq = 0;

async function fixture(gates = false) {
  const slug = `decide-${++seq}`, runId = `r-${slug}`;
  const repo = await makeRepo(), logs = freshDir('logs'), transcript = `${logs}/coder.log`;
  writeFile(repo, 'a.mjs', 'old');
  writeFile(logs, 'coder.log', 'git stash\n');
  await commitAll(repo);
  const probe = async () => 'start';
  const writeRow = (row) => appendRow(row, { slug });
  const config = cfgFor({ agent_waivers: { require_ack: true } });
  if (gates) config.gates = { test: ['node', '--test'] };
  await startRun({ workspace: repo, project: slug, runId, workerPid: 42, probe, writeRow, config });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['a.mjs'], acceptance: [{ clause: 'close', tests: ['t'] }], writeRow });
  writeFile(repo, 'a.mjs', 'new');
  const hash = contentHash(repo, 'a.mjs');
  const key = await loadKey(runId);
  const ticket = 'a'.repeat(24);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', file: 'a.mjs', content_hash: hash, ...row });
  await sign({ event: 'review.approved' });
  await sign({ event: 'review.round', open_after: 3, open: ['F1', 'F2', 'F3'] });
  writeResult({ repoRoot: repo, runId, ticket, key, result: { block: 'B1', file: 'a.mjs', content_hash: hash, status: 'stopped', approved: false, findings: [{ id: 'F1', severity: 'critical', claim: 'critical claim' }] } });
  await sign({ event: 'review.result', ticket, approved: false, open: [{ id: 'F1', severity: 'critical' }, { id: 'F2', severity: 'warning', claim: 'warning claim' }, { id: 'F3', severity: 'nit', claim: 'nit claim' }] });
  await sign({ event: 'review.triage', finding: 'F3', severity: 'nit', claim: 'nit claim' });
  await sign({ event: 'review.waived', finding: 'F3', severity: 'nit', by: 'agent', reason: 'evidence', verdict: 'agree' });
  const rows = () => readAllRows(slug);
  const asked = [];
  const invoke = async (extra = [], deps = {}) => {
    let out = '', err = '';
    const code = await runBlock(['close', 'B1', '--run', runId, '--worker-pid', '42', '--transcript', transcript, '--decide', '--reason', 'Owner decision', ...extra], {
      probe, isTTY: true, ui: { confirm: async (o) => { asked.push(o); return true; }, isCancel: () => false },
      stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, ...deps,
    });
    return { code, out, err };
  };
  return { repo, logs, transcript, runId, writeRow, sign, rows, key, asked, invoke };
}

test('B138 decide: one numbered list and one confirm write the shared signed decisions then close', async () => {
  const f = await fixture(), before = (await f.rows()).length;
  const result = await f.invoke();
  assert.equal(result.code, 0, result.err);
  assert.equal(f.asked.length, 1);
  assert.equal(f.asked[0].initialValue, false);
  const list = result.out.split('\n').filter((line) => /^\d+\. /.test(line));
  assert.equal(list.length, 4, result.out);
  assert.match(list[0], /a.mjs.*F1.*critical.*critical claim/);
  assert.match(list[1], /a.mjs.*F2.*warning.*warning claim/);
  assert.match(list[2], /a.mjs.*F3.*nit.*agent-waivers-not-acked/);
  assert.match(list[3], /git-stash.*git stash/);
  const added = (await f.rows()).slice(before);
  assert.deepEqual(added.map((r) => r.event), ['review.waived', 'review.waived', 'agent_waivers.acked', 'rule_break.accepted', 'block.close']);
  assert.deepEqual(added.slice(0, 2).map((r) => [r.finding, r.by, r.reason]), [['F1', 'human', 'Owner decision'], ['F2', 'human', 'Owner decision']]);
  for (const row of added) assert.deepEqual(verifyRow(row, f.key), { ok: true });
});

test('B138 decide boundary: unreviewed, stopped, failed gates, unowned and proof refuse without any write', async () => {
  for (const kind of ['unreviewed', 'stopped', 'gates', 'unowned', 'proof']) {
    const f = await fixture(kind === 'gates');
    if (kind === 'unreviewed' || kind === 'stopped') writeFile(f.repo, 'a.mjs', 'unreviewed content');
    if (kind === 'stopped') await f.sign({ event: 'review.cap', open: ['F1'] });
    if (kind === 'gates') await f.sign({ event: 'gate.done', all_ok: false });
    if (kind === 'unowned') writeFile(f.repo, 'other.mjs', 'unowned');
    if (kind === 'proof') await f.sign({ event: 'review.plan', risk: 3 });
    const before = (await f.rows()).length;
    const result = await f.invoke();
    assert.equal(result.code, 1, `${kind}: ${result.err}`);
    assert.match(result.err, /cannot decide here:/, kind);
    assert.equal(f.asked.length, 0, kind);
    assert.equal((await f.rows()).length, before, kind);
  }
});

test('B138 decide boundary: no, no terminal, yes flag and active grant write nothing', async () => {
  for (const kind of ['no', 'tty', 'yes', 'grant']) {
    const f = await fixture();
    if (kind === 'grant') await startGrant({ runId: f.runId, writeRow: f.writeRow, input: { allow: 'waive:nit', delegate: 'L2', until: new Date(Date.now() + 60000).toISOString() } });
    const before = (await f.rows()).length;
    const result = await f.invoke(kind === 'yes' ? ['--yes'] : [], kind === 'tty' ? { isTTY: false } : kind === 'no' ? { ui: { confirm: async () => false, isCancel: () => false } } : {});
    assert.equal(result.code, ['tty', 'yes'].includes(kind) ? 2 : 1, result.err);
    assert.equal((await f.rows()).length, before, kind);
    if (kind === 'grant') assert.match(result.err, /active/);
  }
});
