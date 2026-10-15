import { cfgFor, commitAll, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { immutableChanges } = await import('../../src/state/config-snapshot.mjs');
const { runRun } = await import('../../src/cli/run.mjs');
let seq = 0;
const FILE = 'src/a.mjs';

async function fixture(requireAck = true) {
  const slug = `ack-waivers-${++seq}`, runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  /** @type {Record<string, any>} */
  const cfg = { version: 1, ...cfgFor() };
  if (requireAck) cfg.review = { ...cfg.review, agent_waivers: { require_ack: true } };
  writeFile(repo, '.code-forge.yml', JSON.stringify(cfg));
  await commitAll(repo);
  const probe = async (pid) => pid === 4242 ? 'start-A' : null;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe, config: cfg });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'bounded', tests: ['t'] }], writeRow });
  writeFile(repo, FILE, 'export const a = 2;\n');
  const hash = contentHash(repo, FILE), key = await loadKey(runId);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', file: FILE, ...row });
  await sign({ event: 'review.triage', finding: 'W1', severity: 'warning' });
  await sign({ event: 'review.approved', content_hash: hash });
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  let prompts = 0, judges = 0;
  const asked = [];
  /** @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string, string]>} */
  const invoke = async (args, deps = {}) => {
    let out = '', err = '';
    const code = await runBlock(args, { probe, isTTY: true, stdout: { write(s) { out += s; } }, stderr: { write(s) { err += s; } },
      ui: { confirm: async () => { prompts++; asked.push(out); return true; }, isCancel: (v) => typeof v === 'symbol' },
      waiverJudge: async () => { judges++; return { status: 'ok', answer: { verdict: 'agree', reason: 'bounded' } }; }, ...deps });
    return [code, out, err];
  };
  const args = ['ack-waivers', 'B1', '--run', runId];
  const waive = () => invoke(['waive', 'B1', 'W1', '--run', runId, '--file', FILE, '--reason', `${FILE}:1 bounds it`, '--yes']);
  const close = () => invoke(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`]);
  return { cfg, repo, runId, key, args, invoke, waive, close, sign, writeRow, rows, asked, prompts: () => prompts, judges: () => judges };
}

test('B124b clause 4: snapshot ack policy refuses close until the current counted set is acknowledged; reload freezes it', async () => {
  const f = await fixture();
  assert.equal((await f.waive())[0], 0);
  const before = (await f.rows()).length;
  assert.deepEqual(await f.close(), [1, '', 'block B1 open: agent-waivers-not-acked\n']);
  assert.equal((await f.rows()).length - before, 0);
  writeFile(f.repo, '.code-forge.yml', 'version: 1\nreview:\n  agent_waivers:\n    require_ack: false\n');
  assert.equal((await f.close())[0], 1, 'live config cannot turn off snapshot policy');
  const next = { ...f.cfg, review: { ...f.cfg.review, agent_waivers: { require_ack: false } } };
  assert.deepEqual(immutableChanges(f.cfg, next), ['review.agent_waivers.require_ack']);
  // Reload uses the actual CLI with a valid replacement config.
  writeFile(f.repo, '.code-forge.yml', JSON.stringify(next));
  let err = '';
  const code = await runRun(['reload', '--run', f.runId], { stdout: { write() {} }, stderr: { write(s) { err += s; } } });
  assert.equal(code, 1);
  assert.match(err, /review\.agent_waivers\.require_ack/);
  assert.equal((await f.invoke(f.args))[0], 0);
  assert.equal((await f.waive())[0], 0);
  const after = (await f.rows()).length;
  assert.deepEqual(await f.close(), [1, '', 'block B1 open: agent-waivers-not-acked\n']);
  assert.equal((await f.rows()).length - after, 0);
  assert.equal((await f.invoke(f.args))[0], 0);
  await f.sign({ event: 'review.triage', finding: 'A1', severity: 'nit' });
  await f.sign({ event: 'review.waived', finding: 'A1', severity: 'nit', reason: 'another reason', verdict: 'agree', by: 'agent' });
  assert.equal((await f.invoke(f.args))[0], 0);
  await f.sign({ event: 'review.unwaived', finding: 'W1', reason: 'undo', by: 'human' });
  assert.deepEqual(await f.close(), [1, '', 'block B1 open: agent-waivers-not-acked\n'], 'removing one of two findings changes the digest');
  assert.equal((await f.invoke(f.args))[0], 0);
  writeFile(f.repo, '.code-forge.yml', JSON.stringify(f.cfg));
  assert.equal((await f.close())[0], 0);
  assert.equal(f.judges(), 2);
});

test('B124b clause 4 boundary: absent policy, no counted waivers, or matching verified ack permits close; forged acks do not', async () => {
  for (const kind of ['absent', 'none', 'acked', 'unsigned', 'tampered', 'wrong-block', 'wrong-run', 'non-human']) {
    const f = await fixture(kind !== 'absent');
    if (kind !== 'none') await f.waive();
    if (kind !== 'absent' && kind !== 'none') {
      assert.equal((await f.invoke(f.args))[0], 0);
      if (kind !== 'acked') {
        await f.waive();
        const waivers = (await f.rows()).filter((r) => r.event === 'review.waived');
        const digest = createHash('sha256').update(JSON.stringify(waivers.map((r) => [r.file, r.finding, r.mac]).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b))))).digest('hex');
        const row = { event: 'agent_waivers.acked', block: 'B1', digest, count: 2, by: 'human' };
        if (kind === 'unsigned' || kind === 'tampered') await f.writeRow({ run: f.runId, ...row, ...(kind === 'tampered' ? { mac: 'bad' } : {}) });
        else await f.sign({ ...row, ...(kind === 'wrong-block' ? { block: 'B2' } : kind === 'wrong-run' ? { run: 'r-other' } : { by: 'agent' }) });
      }
    }
    const [code, , err] = await f.close();
    assert.equal(code, ['absent', 'none', 'acked'].includes(kind) ? 0 : 1, kind);
    if (code === 1) assert.equal(err, 'block B1 open: agent-waivers-not-acked\n', kind);
  }
});

test('B124b clause 5: prints every counted waiver before one confirmation and writes one signed human ack with the exact sorted digest', async () => {
  const f = await fixture();
  await f.waive();
  await f.sign({ event: 'review.triage', finding: 'A1', severity: 'nit' });
  await f.sign({ event: 'review.waived', finding: 'A1', severity: 'nit', reason: 'another reason', verdict: 'agree', by: 'agent' });
  const waivers = (await f.rows()).filter((r) => r.event === 'review.waived');
  const digest = createHash('sha256').update(JSON.stringify(waivers.map((r) => [r.file, r.finding, r.mac]).sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b))))).digest('hex');
  const listing = `block B1: agent waiver ${FILE} W1 reason: ${FILE}:1 bounds it; verdict: agree\nblock B1: agent waiver ${FILE} A1 reason: another reason; verdict: agree\n`;
  const before = (await f.rows()).length;
  assert.deepEqual(await f.invoke(f.args), [0, `${listing}block B1 acknowledged 2 agent waiver(s) (by: human)\n`, '']);
  assert.deepEqual(f.asked, [listing]);
  const rows = (await f.rows()).slice(before);
  assert.equal(rows.length, 1);
  const { mac, ts, tokens_source, cost_source, ...row } = rows[0];
  assert.deepEqual(row, { run: f.runId, event: 'agent_waivers.acked', block: 'B1', digest, count: 2, by: 'human' });
  assert.deepEqual(verifyRow(rows[0], f.key), { ok: true });
  assert.equal(f.prompts(), 1);
});

test('B124b clause 5 boundary: none, unwaived, invalid agent rows and wrong scope refuse with zero prompts; no, cancel, --yes and no terminal write zero rows', async () => {
  for (const kind of ['none', 'unwaived', 'disagree', 'unsigned', 'tampered', 'critical', 'human']) {
    const f = await fixture();
    if (kind === 'unwaived') {
      await f.waive();
      await f.sign({ event: 'review.unwaived', finding: 'W1', by: 'human', reason: 'undo' });
    } else if (kind !== 'none') {
      const row = { event: 'review.waived', block: 'B1', file: FILE, finding: 'W1', severity: kind === 'critical' ? 'critical' : 'warning', by: kind === 'human' ? 'human' : 'agent', reason: 'bounded', verdict: kind === 'disagree' ? 'disagree' : 'agree' };
      if (kind === 'unsigned' || kind === 'tampered') await f.writeRow({ run: f.runId, ...row, ...(kind === 'tampered' ? { mac: 'bad' } : {}) });
      else await f.sign(row);
    }
    const before = (await f.rows()).length;
    const [code, out, err] = await f.invoke(f.args);
    assert.deepEqual([code, out], [1, ''], kind);
    assert.match(err, /^block ack-waivers: ack-waivers-none:/, kind);
    assert.equal(f.prompts(), 0, kind);
    assert.equal((await f.rows()).length - before, 0, kind);
  }
  const f = await fixture();
  await f.waive();
  const before = (await f.rows()).length;
  for (const answer of [false, Symbol('cancel')]) {
    let calls = 0;
    const [code, , err] = await f.invoke(f.args, { ui: { confirm: async () => { calls++; return answer; }, isCancel: (v) => typeof v === 'symbol' } });
    assert.deepEqual([code, err], [1, 'block B1 ack-waivers: cancelled — nothing was recorded\n']);
    assert.equal(calls, 1);
  }
  for (const deps of [{ isTTY: false }, { stdinIsTTY: false }, { stdoutIsTTY: false }]) {
    const [code, , err] = await f.invoke(f.args, deps);
    assert.deepEqual([code, err], [2, 'block ack-waivers needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n']);
  }
  assert.deepEqual(await f.invoke([...f.args, '--yes']), [2, '', 'block ack-waivers: unknown flag "--yes"\n']);
  assert.equal(f.prompts(), 0);
  assert.equal((await f.rows()).length - before, 0);
});
