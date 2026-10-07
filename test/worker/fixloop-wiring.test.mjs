// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { enqueue, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { blockRungUsed, blockStateDir, loadState } = await import('../../src/worker/review-state.mjs');

const FILE = 'src/a.mjs';

/** @param {string} id */
const finding = (id) => ({ id, file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
}

/**
 * An in-process worker on a stub session spawner (round 1: `firstFindings`; a recheck resolves
 * the FIRST listed open id while `script.resolveOne` is true) and a mocked Jev (`defect` 0.95 ⇒
 * fix_now; `resolved` 0.2 ⇒ the recheck session decides).
 * @param {Array<Record<string, any>>} firstFindings
 */
async function wired(firstFindings) {
  const { repo, runId } = await makeRepo();
  const key = await loadKey(runId);
  const runRootDir = freshDir('runroot');
  const script = { resolveOne: true, crash: false };
  /** @type {string[]} */
  const lenses = [];
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  /** @type {string[]} */
  const asked = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    const lens = opts.rowExtra?.lens;
    lenses.push(lens);
    const at = text.indexOf('\n## open findings\n');
    const open = at >= 0 ? [...text.slice(at).matchAll(/^- (\S+) \(/gm)].map((m) => m[1]) : [];
    const resolved = lens === 'recheck' && script.resolveOne && open.length > 0 ? [{ id: open[0], resolved: true, why: 'fixed' }] : [];
    const answer = { passed: false, summary: 's', reviewed_hunks: hunksOf(text), findings: lens === 'recheck' ? [] : firstFindings, resolved, needs_file: [] };
    return { status: 'ok', exit_code: 0, answer, usage: { tokens_in: 900, tokens_out: 300 } };
  };
  const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
    const [id] = Object.keys(req.questions);
    asked.push(id);
    return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
  };
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: {}, runRootDir, slug: 'worker-test', key },
    {
      store: await createKeyStore({ backends: [], dir: freshDir('store') }),
      env: {},
      writeRow: async (row) => void rows.push(row),
      readRows: async () => rows,
      spawn: /** @type {any} */ (spawn),
      jev,
    },
  );
  let n = 0;
  /** One coder edit + `review-file` + the worker's drain: the signed result. */
  const round = async () => {
    n += 1;
    appendFileSync(path.join(repo, FILE), `export const fix${n} = ${n};\n`);
    const t = enqueue({ repoRoot: repo, run: runId, block: 'B11', file: FILE });
    if (script.crash) {
      // ONE round throws from inside `runRound`: its packet dir path is taken by a regular file, so
      // `fixHunkDiff`'s mkdir fails (EEXIST) — an I/O error the loop does not catch itself.
      script.crash = false;
      mkdirSync(path.join(runRootDir, 'packets'), { recursive: true });
      writeFileSync(path.join(runRootDir, 'packets', t.ticket), '');
    }
    assert.equal(await worker.drain(), 1);
    return /** @type {Record<string, any>} */ (readResult(repo, runId, t.ticket));
  };
  /** @returns {string} the file's one state file. */
  const stateFile = () => {
    const dir = blockStateDir(runRootDir, 'B11');
    const names = readdirSync(dir);
    assert.equal(names.length, 1);
    return path.join(dir, names[0]);
  };
  return { repo, runId, key, runRootDir, rows, lenses, asked, script, round, stateFile };
}

describe('the worker drives the §4.11 fix loop one ticket per round (B12c)', () => {
  test('4 → 3 open on an L2 block: review_rounds at round 2 is the L3 patch rung (R1: L3 is never a running level), then stopped: l3_patch_exhausted; a later ticket runs 0 sessions', async () => {
    const w = await wired(['F1', 'F2', 'F3', 'F4'].map(finding));
    const seen = [];
    for (let i = 0; i < 2; i += 1) {
      const r = await w.round();
      seen.push([r.status, r.approved, r.round, r.kind, r.level, r.next.action, r.trigger, r.findings.length]);
    }
    assert.deepEqual(seen, [
      ['reviewed', false, 1, 'full', 'L2', 'fix', null, 4],
      ['reviewed', false, 2, 'recheck', 'L2', 'patch', 'review_rounds', 3],
    ]);
    w.script.resolveOne = false; // the L3 patch did not fix F2, F3 or F4
    const check = await w.round();
    assert.deepEqual([check.status, check.kind, check.stopped, check.approved, check.findings.map((/** @type {any} */ f) => f.id)], ['stopped', 'patch_check', 'l3_patch_exhausted', false, ['F2', 'F3', 'F4']]);
    const lensesBefore = w.lenses.length;
    const after = await w.round();
    assert.deepEqual([after.status, after.stopped, after.approved], ['stopped', 'l3_patch_exhausted', false]);
    assert.equal(w.lenses.length, lensesBefore); // a stopped file is never reviewed again by the loop
    assert.deepEqual(w.lenses, ['quick', 'recheck', 'recheck']);
    assert.equal(w.rows.filter((r) => r.event === 'review.approved').length, 0);
    // B52: the ledger's review.result row carries the verdict and the open findings (the gate reads them)
    const results = w.rows.filter((r) => r.event === 'review.result');
    assert.deepEqual(
      results.map((r) => [r.status, r.approved, r.open.map((/** @type {any} */ o) => `${o.id}:${o.severity}`).join(',')]),
      [
        ['reviewed', false, 'F1:warning,F2:warning,F3:warning,F4:warning'],
        ['reviewed', false, 'F2:warning,F3:warning,F4:warning'],
        ['stopped', false, 'F2:warning,F3:warning,F4:warning'],
        ['stopped', false, 'F2:warning,F3:warning,F4:warning'],
      ],
    );
    assert.equal(results.every((r) => verifyRow(r, w.key).ok), true);
    const caps = w.rows.filter((r) => r.event === 'review.cap');
    assert.deepEqual(caps.map((r) => [r.block, r.file, r.reason, r.open]), [['B11', FILE, 'l3_patch_exhausted', ['F2', 'F3', 'F4']]]);
    assert.equal(verifyRow(caps[0], w.key).ok, true);
    assert.equal(w.asked.filter((q) => q === 'defect').length, 4); // the mocked Jev triaged round 1

    // the rung is read from the signed ledger anchors: corrupting the state file that recorded it
    // does not make it available again; a forged anchor fails closed
    writeFileSync(w.stateFile(), '{"state": {"l3_rung_used": false}}');
    const anchors = { runId: w.runId, block: 'B11', key: w.key };
    assert.deepEqual(blockRungUsed({ ...anchors, rows: w.rows }), { status: 'ok', used: true });
    const forged = w.rows.map((r) => (r.event === 'review.state' ? { ...r, l3_rung_used: false } : r));
    assert.deepEqual(blockRungUsed({ ...anchors, rows: forged }), { status: 'tampered', used: true });
  });

  test('5 → 4 → 3 → 2 → 1 open on an L2 block: the rung at round 2 shrinks the set ⇒ fix at L2; rounds 3–4 shrink but stay open ⇒ stopped: review_cap', async () => {
    const w = await wired(['F1', 'F2', 'F3', 'F4', 'F5'].map(finding));
    const seen = [];
    for (let i = 0; i < 5; i += 1) {
      const r = await w.round();
      seen.push([r.status, r.round, r.kind, r.level, r.next?.action ?? null, r.stopped ?? null, r.findings.length]);
    }
    assert.deepEqual(seen, [
      ['reviewed', 1, 'full', 'L2', 'fix', null, 5],
      ['reviewed', 2, 'recheck', 'L2', 'patch', null, 4],
      ['reviewed', 2, 'patch_check', 'L2', 'fix', null, 3],
      ['reviewed', 3, 'recheck', 'L2', 'fix', null, 2],
      ['stopped', 4, 'recheck', 'L2', 'stop', 'review_cap', 1],
    ]);
    assert.deepEqual(w.lenses, ['quick', 'recheck', 'recheck', 'recheck', 'recheck']);
    const rounds = w.rows.filter((r) => r.event === 'review.round');
    assert.equal(rounds.filter((r) => r.kind === 'patch_check').length, 1); // exactly one patch session was checked
    const caps = w.rows.filter((r) => r.event === 'review.cap');
    assert.deepEqual(caps.map((r) => [r.file, r.round, r.reason, r.open]), [[FILE, 4, 'review_cap', ['F5']]]);
    assert.equal(w.rows.filter((r) => r.event === 'review.approved').length, 0);
  });

  /**
   * @param {Awaited<ReturnType<typeof wired>>} w
   * @returns {Promise<void>} the next ticket is refused `review-state-tampered` with 0 sessions.
   */
  async function assertRefused(w) {
    const before = w.lenses.length;
    const r = await w.round();
    assert.deepEqual([r.status, r.reason, r.approved], ['refused', 'review-state-tampered', false]);
    assert.equal(w.lenses.length, before);
    assert.equal(w.rows.filter((row) => row.event === 'review.approved').length, 0);
  }

  test('an edited state file (open findings cleared) is refused: 0 sessions, never approval', async () => {
    const w = await wired([finding('F1')]);
    assert.deepEqual((await w.round()).findings.length, 1);
    const body = JSON.parse(readFileSync(w.stateFile(), 'utf8'));
    body.state.open = []; // a coder clearing its own open findings
    writeFileSync(w.stateFile(), JSON.stringify(body));
    await assertRefused(w);
  });

  test('the state file deleted after round 1 is refused (the ledger anchor says round 1 happened), never a fresh round 1', async () => {
    const w = await wired([finding('F1')]);
    assert.equal((await w.round()).round, 1);
    rmSync(w.stateFile());
    await assertRefused(w);
  });

  test('an older signed state restored over the current one (rollback) is refused', async () => {
    const w = await wired([finding('F1'), finding('F2')]);
    assert.equal((await w.round()).round, 1);
    const older = readFileSync(w.stateFile(), 'utf8');
    const second = await w.round();
    assert.deepEqual([second.round, second.findings.length], [2, 1]);
    writeFileSync(w.stateFile(), older); // validly signed, but seq 1 < the latest anchor's seq 2
    await assertRefused(w);
  });

  test('the newest anchor with its block edited plus the older state file restored is tampered (never the rollback): loadState, blockRungUsed, and the ticket', async () => {
    const w = await wired([finding('F1'), finding('F2')]);
    assert.equal((await w.round()).round, 1);
    const older = readFileSync(w.stateFile(), 'utf8');
    assert.equal((await w.round()).round, 2);
    const anchors = w.rows.filter((r) => r.event === 'review.state');
    assert.deepEqual(anchors.map((r) => r.seq), [1, 2]);
    anchors[1].block = 'B99'; // the newest anchor no longer names B11: filtered first, the seq-1 file would validate
    writeFileSync(w.stateFile(), older);
    const where = { runRootDir: w.runRootDir, runId: w.runId, block: 'B11', file: FILE, key: w.key, rows: w.rows };
    assert.deepEqual(loadState(where), { status: 'tampered', state: null, seq: 0 });
    assert.deepEqual(blockRungUsed(where), { status: 'tampered', used: true });
    await assertRefused(w);
  });

  test('a throw inside the round is unavailable with the state saved as retry: the next ticket re-runs that round and review.round rows are not duplicated', async () => {
    const w = await wired([finding('F1'), finding('F2')]);
    assert.deepEqual((await w.round()).findings.length, 2);
    w.script.crash = true;
    const r = await w.round();
    assert.deepEqual([r.status, r.reason, r.approved, r.round, r.kind, r.level, r.next, r.trigger, Object.hasOwn(r, 'findings')], [
      'unavailable',
      'EEXIST',
      false,
      1,
      'recheck',
      'L2',
      { action: 'retry', reason: 'EEXIST' },
      null,
      false,
    ]);
    const where = { runRootDir: w.runRootDir, runId: w.runId, block: 'B11', file: FILE, key: w.key, rows: w.rows };
    const saved = loadState(where);
    assert.deepEqual([saved.status, saved.seq, saved.state?.round, saved.state?.next, saved.state?.pending_kind, saved.state?.open.length], ['ok', 2, 1, { action: 'retry', reason: 'EEXIST' }, 'recheck', 2]);
    assert.deepEqual(w.rows.filter((row) => row.event === 'review.state').map((row) => row.seq), [1, 2]);
    assert.deepEqual(w.rows.filter((row) => row.event === 'review.round').map((row) => row.round), [1]);

    const again = await w.round(); // the session is back: the pending recheck runs as round 2
    assert.deepEqual([again.status, again.approved, again.round, again.kind, again.findings.length], ['reviewed', false, 2, 'recheck', 1]);
    assert.deepEqual(w.rows.filter((row) => row.event === 'review.round').map((row) => [row.round, row.kind]), [[1, 'full'], [2, 'recheck']]);
    assert.deepEqual(w.rows.filter((row) => row.event === 'review.state').map((row) => row.seq), [1, 2, 3]);
    assert.deepEqual(w.lenses, ['quick', 'recheck']);
    assert.equal(w.rows.filter((row) => row.event === 'review.approved').length, 0);

    // B52: the last open finding is fixed ⇒ approved; each ledger review.result row says so itself
    const done = await w.round();
    assert.deepEqual([done.status, done.approved], ['reviewed', true]);
    const ids = (/** @type {Record<string, any>} */ row) => (Object.hasOwn(row, 'open') ? row.open.map((/** @type {any} */ o) => o.id).join(',') : 'no open field');
    assert.deepEqual(w.rows.filter((row) => row.event === 'review.result').map((row) => [row.status, row.approved, ids(row)]), [
      ['reviewed', false, 'F1,F2'],
      ['unavailable', false, ''],
      ['reviewed', false, 'F2'],
      ['reviewed', true, ''],
    ]);
  });
});
