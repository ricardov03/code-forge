// Pin the fixture HOME before loading any source modules, as in fixloop-wiring.test.mjs.
import { BLOCK, freshDir, makeRepo } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { loadState } = await import('../../src/worker/review-state.mjs');

const A = 'src/a.mjs';
const B = 'src/b.mjs';

/** An in-process engine fixture with deterministic review and Jev answers; no model or network. */
async function wired() {
  const { repo, runId } = await makeRepo();
  const key = await loadKey(runId);
  const runRootDir = freshDir('runroot');
  const store = await createKeyStore({ backends: [], dir: freshDir('store') });
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const script = { resolveOne: false };
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    const file = /^file: (.+)$/m.exec(text)?.[1];
    assert.ok(file === A || file === B, 'the review packet names one of the two fixture files');
    const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
    const hunks = listed.slice(0, listed.findIndex((line) => !line.startsWith('- @@'))).map((line) => line.slice(2));
    const at = text.indexOf('\n## open findings\n');
    const open = at >= 0 ? [...text.slice(at).matchAll(/^- (\S+) \(/gm)].map((match) => match[1]) : [];
    const findings = ['F1', 'F2'].map((id) => ({ id, file, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' }));
    const resolved = open.map((id, i) => ({ id, resolved: script.resolveOne && i === 0, why: script.resolveOne && i === 0 ? 'fixed' : 'still open' }));
    return {
      status: 'ok', exit_code: 0,
      answer: { passed: false, summary: 's', reviewed_hunks: hunks, findings: opts.rowExtra?.lens === 'recheck' ? [] : findings, resolved, needs_file: [] },
      usage: { tokens_in: 900, tokens_out: 300 },
    };
  };
  const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
    const [id] = Object.keys(req.questions);
    return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
  };
  const start = () => createWorker(
    { runId, repoRoot: repo, cfg: {}, runRootDir, slug: 'worker-test', key },
    { store, env: {}, writeRow: async (row) => void rows.push(row), readRows: async () => rows, spawn: /** @type {any} */ (spawn), jev },
  );
  let worker = await start();
  let n = 0;
  /** @param {string} file */
  const round = async (file) => {
    n += 1;
    appendFileSync(path.join(repo, file), `export const fix${n} = ${n};\n`);
    const ticket = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file });
    assert.equal(await worker.drain(), 1, `${file}: exactly one ticket was processed`);
    return /** @type {Record<string, any>} */ (readResult(repo, runId, ticket.ticket));
  };
  /** @param {string} file */
  const saved = (file) => {
    const loaded = loadState({ runRootDir, runId, block: BLOCK, file, key, rows });
    assert.equal(loaded.status, 'ok', `${file}: signed state was read back from disk`);
    return loaded.state;
  };
  return { round, rows, script, saved, restart: async () => { worker = await start(); } };
}

test('a second stalled file gets its own patch rung while the first patch is pending, and the block keeps running', async () => {
  const w = await wired();
  assert.deepEqual([(await w.round(A)).next.action, (await w.round(A)).next.action], ['fix', 'patch']);
  assert.equal((await w.round(B)).next.action, 'fix');
  const second = await w.round(B);
  assert.deepEqual([second.status, second.next.action, second.next.level, second.trigger, second.stopped ?? null, second.findings.length],
    ['reviewed', 'patch', 'L3', 'review_stall', null, 2], 'B gets its own rung with both findings still open');
  assert.deepEqual(w.rows.filter((row) => row.event === 'review.result' && row.trigger === 'review_stall').map((row) => [row.file, row.status]), [[A, 'reviewed'], [B, 'reviewed']]);
  assert.equal(w.rows.filter((row) => row.event === 'review.cap').length, 0, 'neither file stopped the block');
  w.script.resolveOne = true;
  const progress = await w.round(B);
  assert.deepEqual([progress.status, progress.kind, progress.next.action, progress.findings.length], ['reviewed', 'patch_check', 'fix', 1], 'B continues at L2 after its patch makes progress');
});

test('after a worker restart A keeps its spent rung and stops on a second stall, while B still has its own rung', async () => {
  const w = await wired();
  await w.round(A);
  assert.equal((await w.round(A)).next.action, 'patch');
  w.script.resolveOne = true;
  const patch = await w.round(A);
  assert.deepEqual([patch.kind, patch.next.action, patch.findings.length], ['patch_check', 'fix', 1]);
  await w.restart();
  assert.equal(w.saved(A)?.l3_rung_used, true, 'A reloads its spent rung from signed disk state');
  w.script.resolveOne = false;
  const stalledAgain = await w.round(A);
  assert.deepEqual([stalledAgain.status, stalledAgain.next.action, stalledAgain.stopped, stalledAgain.findings.length],
    ['stopped', 'stop', 'l3_patch_exhausted', 1], 'A cannot take a second rung');
  assert.equal((await w.round(B)).next.action, 'fix');
  assert.equal(w.saved(B)?.l3_rung_used, false, 'B reloads its own unused rung, independently of A');
  const second = await w.round(B);
  assert.deepEqual([second.status, second.next.action, second.stopped ?? null, second.findings.length], ['reviewed', 'patch', null, 2]);
  assert.deepEqual(w.rows.filter((row) => row.event === 'review.cap').map((row) => [row.file, row.reason, row.open.length]), [[A, 'l3_patch_exhausted', 1]], 'only A exhausted its rung');
});
