import { commitAll, makeRepo, writeFile, freshDir, cfgFor } from './helpers.mjs';
import { humanSaysYes } from '../helpers/human-confirm.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { newFileState, runRound } = await import('../../src/review/fixloop.mjs');
const { saveState, loadState } = await import('../../src/worker/review-state.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');

const FILE = 'src/a.mjs';
const F1 = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'critical', category: 'correctness', claim: 'wrong', evidence: 'e', fix: 'f' };
const F2 = { ...F1, id: 'F2' };
const probe = async () => 'start-A';

async function fixture(slug) {
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 0;\n');
  await commitAll(repo);
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'reopen', tests: ['b114'] }], writeRow });
  const key = await loadKey(runId);
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  const hash = () => contentHash(repo, FILE);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', file: FILE, content_hash: hash(), ...row });
  let version = 0;
  const edit = (extra = '') => writeFile(repo, FILE, `export const a = ${++version};\n${extra}`);
  edit();
  const where = { runId, block: 'B1', file: FILE, key, runRootDir: freshDir('runroot'), contentRoot: repo };
  let seq = 0;
  const save = async (state) => {
    await saveState({ ...where, rows: await rows(), state, seq, writeRow: sign });
    seq += 1;
  };
  const reopen = async () => {
    let out = '', err = '';
    const code = await runBlock(['reopen', 'B1', '--run', runId, '--file', FILE, '--reason', 'fixed', '--json'], humanSaysYes({ probe, stdout: { write: (s) => { out += s; return true; } }, stderr: { write: (s) => { err += s; return true; } } }));
    return { code, out, err };
  };
  const restarted = async () => {
    let sessions = 0;
    const worker = await createWorker({ runId, repoRoot: repo, cfg: cfgFor(), runRootDir: where.runRootDir, slug, key }, {
      store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow, readRows: rows,
      spawn: async (opts) => {
        sessions += 1;
        const text = readFileSync(opts.promptPath, 'utf8');
        const hunks = [...text.matchAll(/^- (@@.*)$/gm)].map((m) => m[1]);
        return { status: 'ok', exit_code: 0, answer: { passed: true, summary: 'clean', reviewed_hunks: hunks, findings: [], needs_file: [] }, usage: { tokens_in: 900, tokens_out: 300 } };
      },
    });
    const queued = enqueue({ repoRoot: repo, run: runId, block: 'B1', file: FILE });
    assert.equal(await worker.drain(), 1);
    const result = readResult(repo, runId, queued.ticket);
    assert.deepEqual([result.status, result.approved, result.round, result.kind, sessions], ['reviewed', true, 1, 'full', 1]);
    return loadState({ ...where, rows: await rows() });
  };
  return { repo, runId, writeRow, rows, hash, sign, edit, key, where, save, reopen, restarted };
}

for (const mode of ['patch-stall', 'recheck-stall', 'full-cap', 'recheck-cap', 'patch-cap', 'split', 'stop-at', 'code-exhausted']) {
  test(`B114: ${mode} stamps its stop, then changed content reopens`, async () => {
    const f = await fixture(`b114-${mode}`);
    const state = newFileState({ file: FILE, level: 'L2', l3RungUsed: !['patch-stall', 'patch-cap', 'stop-at'].includes(mode) });
    /** @type {Record<string, any>} */
    const cfg = cfgFor({ max_rounds_per_file: ['full-cap', 'patch-cap'].includes(mode) ? 1 : 6 });
    cfg.escalation = { review_rounds_per_level: 6, after_rounds_with_warnings: 0, ...(mode === 'stop-at' ? { stop_at: 'L2' } : {}), ...(mode === 'code-exhausted' ? { l3_mode: 'code' } : {}) };
    const deps = { repoRoot: f.repo, cfg, workDir: freshDir('packets'), writeRow: f.sign,
      review: async () => ({ status: 'reviewed', depth: 'dual', findings: mode === 'patch-cap' ? [F1, F2] : [F1], sessions: [{ role: 'judge' }] }),
      jev: async (req) => ({ ok: true, answers: { resolved: { type: 'noul', noul: mode === 'patch-cap' ? 0.95 : 0.2 } } }),
      spawn: async (opts) => {
        const text = readFileSync(opts.promptPath, 'utf8');
        const resolved = [...text.matchAll(/^- (F\d) \(/gm)].map((m) => ({ id: m[1], resolved: false, why: 'still open' }));
        return { status: 'ok', exit_code: 0, answer: { passed: false, summary: 'open', reviewed_hunks: [...text.matchAll(/^- (@@.*)$/gm)].map((m) => m[1]), resolved, findings: [], needs_file: [] }, usage: { tokens_in: 900, tokens_out: 300 } };
      },
    };
    await runRound(state, deps);
    if (mode !== 'full-cap') {
      f.edit(mode === 'split' ? `// ${'x'.repeat(8000)}\n` : '');
      if (mode === 'split') cfg.review.budgets = { full_in: 1 };
      if (mode === 'patch-cap') deps.jev = async (req) => ({ ok: true, answers: { resolved: { type: 'noul', noul: req.state.finding.id === 'F1' ? 0.95 : 0.2 } } });
      if (mode === 'recheck-cap') cfg.review.max_rounds_per_file = 2;
      await runRound(state, deps, { kind: mode.startsWith('patch') ? 'patch_check' : 'recheck' });
    }
    const reason = mode.includes('cap') ? 'review_cap' : mode === 'split' ? 'split_required' : mode === 'stop-at' ? 'stop_at' : mode === 'code-exhausted' ? 'l3_code_exhausted' : 'l3_patch_exhausted';
    assert.deepEqual([state.status, state.next.reason, state.stop_content_hash], ['stopped', reason, f.hash()]);
    await f.save(state);
    const stoppedHash = f.hash();
    f.edit();
    const reopened = await f.reopen();
    assert.deepEqual([reopened.code, reopened.err], [0, '']);
    assert.deepEqual([JSON.parse(reopened.out).from_hash, JSON.parse(reopened.out).to_hash], [stoppedHash, f.hash()]);
    const rows = (await f.rows()).filter((r) => r.event === 'review.reopened');
    assert.equal(rows.length, 1);
    assert.equal(verifyRow(rows[0], f.key).ok, true);
    assert.equal((await f.restarted()).state.stop_content_hash, undefined);
  });
}

test('B114: unknown legacy content accepts only a hash without a verified round of this run/block/file; the worker consumes the signed reopen', async () => {
  const f = await fixture('b114-legacy');
  const state = { ...newFileState({ file: FILE, level: 'L2', l3RungUsed: true }), status: 'stopped', round: 2, next: { action: 'stop', reason: 'l3_patch_exhausted' } };
  await f.save(state);
  const round = { run: f.runId, event: 'review.round', block: 'B1', file: FILE, content_hash: f.hash(), round: 2 };
  await f.writeRow(signRow(round, f.key));
  const refused = await f.reopen();
  assert.deepEqual(refused, { code: 1, out: '', err: `block reopen: reopen-same-content: the content the stop of ${FILE} reviewed is unknown and its current content has already been reviewed; change the file and run block reopen, or the owner can run block close --no-require-reviews; nothing was recorded\n` });
  assert.equal((await f.rows()).filter((r) => r.event === 'review.reopened').length, 0);
  f.edit();
  const currentRound = { ...round, content_hash: f.hash() };
  await f.writeRow(currentRound); // unsigned
  await f.writeRow({ ...signRow(currentRound, f.key), round: 99 }); // bad MAC
  for (const override of [{ run: 'other' }, { block: 'B2' }, { file: 'src/b.mjs' }, { content_hash: 'old' }, { event: 'review.result' }]) await f.writeRow(signRow({ ...currentRound, ...override }, f.key));
  const accepted = await f.reopen();
  assert.deepEqual([accepted.code, accepted.err], [0, '']);
  assert.deepEqual(JSON.parse(accepted.out), { block: 'B1', event: 'reopened', file: FILE, from: 'stopped', stop_reason: 'l3_patch_exhausted', at_seq: 1, from_hash: null, to_hash: f.hash(), ticket_cleared: false, by: 'human' });
  assert.equal((await f.reopen()).code, 0);
  const reopened = (await f.rows()).filter((r) => r.event === 'review.reopened');
  assert.equal(reopened.length, 1);
  assert.equal(verifyRow(reopened[0], f.key).ok, true);
  assert.equal((await f.restarted()).seq, 2);
});

test('B114: worker legacy reopen refuses reviewed hashes and a signed row for other bytes', async () => {
  const { reopenRowFor } = await import('../../src/review/reopen.mjs');
  const f = await fixture('b114-worker-guard');
  const state = { status: 'stopped', round: 2, next: { reason: 'l3_patch_exhausted' } };
  const at = { runId: f.runId, block: 'B1', file: FILE, key: f.key, seq: 1, status: 'ok', state, contentHash: f.hash() };
  const row = signRow({ run: f.runId, event: 'review.reopened', block: 'B1', file: FILE, from: 'stopped', from_hash: null, to_hash: f.hash(), at_seq: 1, by: 'human' }, f.key);
  assert.deepEqual(reopenRowFor([row], at), row);
  const round = signRow({ run: f.runId, event: 'review.round', block: 'B1', file: FILE, content_hash: f.hash() }, f.key);
  assert.equal(reopenRowFor([round, row], at), null);
  assert.equal(reopenRowFor([row], { ...at, contentHash: 'other' }), null);
  const withFrom = signRow({ ...row, from_hash: 'old' }, f.key);
  assert.equal(reopenRowFor([withFrom], at), null);
});
