import { cfgFor, commitAll, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, reloadRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { startGrant } = await import('../../src/autopilot/grant.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');
let seq = 0;

async function fixture(requireHuman = true) {
  const slug = `routine-${++seq}`, runId = `r-${slug}`;
  const repo = await makeRepo(), logs = freshDir('logs');
  writeFile(repo, 'a.mjs', 'old');
  writeFile(logs, 'coder.log', 'safe\n');
  await commitAll(repo);
  const config = cfgFor();
  if (requireHuman !== null) config.close = { require_human: requireHuman };
  const probe = async () => 'start';
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 42, probe, writeRow, config });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['a.mjs'], acceptance: [{ clause: 'close', tests: ['t'] }], writeRow });
  writeFile(repo, 'a.mjs', 'new');
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', ...row });
  await sign({ event: 'review.approved', file: 'a.mjs', content_hash: contentHash(repo, 'a.mjs') });
  const rows = () => readAllRows(slug), key = await loadKey(runId), asked = [];
  const invoke = async (deps = {}) => {
    let out = '', err = '';
    const code = await runBlock(['close', 'B1', '--run', runId, '--worker-pid', '42', '--transcript', `${logs}/coder.log`], {
      probe, isTTY: false, ui: { confirm: async (o) => { asked.push(o); return true; }, isCancel: () => false },
      stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, ...deps,
    });
    return { code, out, err };
  };
  return { runId, config, writeRow, sign, rows, key, asked, invoke, repo, logs };
}

test('B138 human close: snapshot requires one terminal confirmation and reload cannot change it', async () => {
  const f = await fixture(), before = (await f.rows()).length;
  assert.equal((await f.invoke()).code, 2, 'nonterminal close must refuse');
  assert.equal((await f.rows()).length, before);
  await assert.rejects(reloadRun({ runId: f.runId, config: { ...f.config, close: { require_human: false } }, writeRow: f.writeRow }), /close.require_human/);
  const result = await f.invoke({ isTTY: true });
  assert.equal(result.code, 0, result.err);
  assert.equal(f.asked.length, 1);
  assert.deepEqual((await f.rows()).slice(before).map((r) => r.event), ['block.close']);
});

test('B138 human close boundary: invalid config, no and either missing terminal refuse', async () => {
  const f = await fixture(), before = (await f.rows()).length;
  const valid = await validateConfig({ version: 1, ...f.config }, { hasCliOnPath: () => true });
  assert.equal(valid.errors.length, 0, JSON.stringify(valid.errors));
  const invalid = await validateConfig({ version: 1, ...f.config, close: { require_human: 'true' } }, { hasCliOnPath: () => true });
  assert.ok(invalid.errors.length > 0);
  for (const deps of [{ isTTY: true, stdinIsTTY: false }, { isTTY: true, stdoutIsTTY: false }, { isTTY: true, ui: { confirm: async () => false, isCancel: () => false } }]) assert.notEqual((await f.invoke(deps)).code, 0);
  assert.equal((await f.rows()).length, before);
});

test('B138 routine: active close scope passes without terminal and signs exactly one grant audit', async () => {
  const f = await fixture();
  await f.sign({ event: 'gate.done', all_ok: true });
  // Use the real grant API once the vocabulary includes this scope; old code still reaches the close assertion.
  const { ALLOWABLE_SCOPES } = await import('../../src/autopilot/scopes.mjs');
  await startGrant({ runId: f.runId, writeRow: f.writeRow, input: { allow: ALLOWABLE_SCOPES.includes('close:routine') ? 'close:routine' : 'waive:nit', delegate: 'L2', until: new Date(Date.now() + 60000).toISOString() } });
  const before = (await f.rows()).length, result = await f.invoke();
  assert.equal(result.code, 0, result.err);
  const added = (await f.rows()).slice(before);
  assert.deepEqual(added.map((r) => r.event).sort(), ['autopilot.close', 'block.close']);
  const audit = added.find((r) => r.event === 'autopilot.close');
  assert.equal(audit.block, 'B1');
  assert.equal(audit.grant, (await f.rows()).find((r) => r.event === 'autopilot.grant').grant_id);
  assert.deepEqual(verifyRow(audit, f.key), { ok: true });
  assert.equal(f.asked.length, 0);
});

test('B138 routine boundary: waiver of any kind, missing green, hit, accepted hit, unapproved and answered-only refuse', async () => {
  for (const kind of ['waiver', 'proof-waiver', 'config-waiver', 'missing-green', 'hit', 'accepted-hit', 'unapproved', 'answered-only']) {
    const f = await fixture();
    if (kind !== 'missing-green') await f.sign({ event: 'gate.done', all_ok: true });
    if (kind === 'waiver' || kind === 'proof-waiver') await f.sign({ event: 'review.waived', file: 'a.mjs', finding: kind === 'waiver' ? 'F1' : 'proof', by: 'human', reason: 'old waiver' });
    if (kind === 'config-waiver') await f.sign({ event: 'gate.reviews_waived', by: 'config' });
    if (kind === 'hit') writeFile(f.logs, 'coder.log', 'git stash\n');
    if (kind === 'accepted-hit') await f.sign({ event: 'rule_break.accepted', rule: 'git-stash', by: 'human' });
    if (kind === 'unapproved' || kind === 'answered-only') writeFile(f.repo, 'a.mjs', 'new content');
    if (kind === 'answered-only') {
      const hash = contentHash(f.repo, 'a.mjs');
      await f.sign({ event: 'review.round', file: 'a.mjs', content_hash: hash, open_after: 1 });
      await f.sign({ event: 'review.result', file: 'a.mjs', content_hash: hash, approved: false, open: [{ id: 'F1', severity: 'warning' }] });
      await f.sign({ event: 'review.answered', file: 'a.mjs', content_hash: hash, finding: 'F1', by_file: 'a.mjs', by_hash: hash });
    }
    const { ALLOWABLE_SCOPES } = await import('../../src/autopilot/scopes.mjs');
    await startGrant({ runId: f.runId, writeRow: f.writeRow, input: { allow: ALLOWABLE_SCOPES.includes('close:routine') ? 'close:routine' : 'waive:nit', delegate: 'L2', until: new Date(Date.now() + 60000).toISOString() } });
    const before = (await f.rows()).length, result = await f.invoke();
    assert.equal(result.code, 1, `${kind}: ${result.err}`);
    assert.match(result.err, /close-not-routine: /, kind);
    assert.equal((await f.rows()).length, before, kind);
  }
});

test('B138 legacy: false or absent human close preserves nonterminal behavior and rows', async () => {
  for (const enabled of [false, null]) {
    const f = await fixture(enabled);
    const before = (await f.rows()).length, result = await f.invoke();
    assert.equal(result.code, 0, result.err);
    assert.equal(f.asked.length, 0);
    assert.deepEqual((await f.rows()).slice(before).map((r) => r.event), ['block.close']);
  }
});
