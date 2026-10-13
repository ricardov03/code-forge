// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { appendFileSync, existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { enqueue, queueDir, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { loadState, saveState, statePath } = await import('../../src/worker/review-state.mjs');
const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');

const FILE = 'src/a.mjs';
const BLOCK = 'B11';
const sha = (/** @type {string} */ text) => createHash('sha256').update(text).digest('hex');

/** @param {string} text @returns {string} the git (SHA-1 repo) blob id of `text`'s UTF-8 bytes. */
const blobId = (text) => createHash('sha1').update(`blob ${Buffer.byteLength(text, 'utf8')}\0${text}`).digest('hex');

/**
 * Delete a blob's loose object file (what `git gc --prune` does to an unreachable blob).
 * @param {string} repo @param {string} id
 */
function dropBlob(repo, id) {
  const loose = path.join(repo, '.git', 'objects', id.slice(0, 2), id.slice(2));
  assert.equal(existsSync(loose), true);
  rmSync(loose);
}

/**
 * Write `text` as a blob into `repo`'s object store (argv only, GIT_* stripped).
 * @param {string} repo @param {string} text @returns {Promise<string>} its id.
 */
async function hashObject(repo, text) {
  const res = await exec(['git', 'hash-object', '-w', '--no-filters', '--stdin'], { cwd: repo, env: gitChildEnv(), input: text, timeoutMs: 20000 });
  assert.equal(res.result, 'ok');
  return res.stdout.trim();
}

/** @param {string} id @param {string} [pad] */
const finding = (id, pad = '') => ({ id, file: FILE, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: `claim ${id}${pad}`, evidence: `e${pad}`, fix: `f${pad}` });

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
  return listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
}

/** @param {string} text @returns {string[]} the open finding ids a recheck packet lists. */
function openIdsOf(text) {
  const at = text.indexOf('\n## open findings\n');
  return at >= 0 ? [...text.slice(at).matchAll(/^- (\S+) \(/gm)].map((m) => m[1]) : [];
}

/**
 * An in-process worker on a stub session spawner (round 1: `firstFindings`; a recheck resolves the
 * FIRST listed open id while `script.resolveOne` is true), a mocked Jev, and the ledger rows in an
 * array the test can rewrite. Every packet text is kept in `packets`.
 * @param {Array<Record<string, any>>} firstFindings
 */
async function wired(firstFindings) {
  const { repo, runId } = await makeRepo();
  const key = await loadKey(runId);
  const runRootDir = freshDir('runroot');
  const script = { resolveOne: true };
  /** @type {string[]} */
  const lenses = [];
  /** @type {string[]} */
  const packets = [];
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    const lens = opts.rowExtra?.lens;
    lenses.push(lens);
    packets.push(text);
    const open = openIdsOf(text);
    const resolved = open.map((id, i) => ({ id, resolved: lens === 'recheck' && script.resolveOne && i === 0, why: i === 0 && script.resolveOne ? 'fixed' : 'still open' }));
    const answer = { passed: false, summary: 's', reviewed_hunks: hunksOf(text), findings: lens === 'recheck' ? [] : firstFindings, resolved, needs_file: [] };
    return { status: 'ok', exit_code: 0, answer, usage: { tokens_in: 900, tokens_out: 300 } };
  };
  const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
    const [id] = Object.keys(req.questions);
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
  /**
   * One coder edit (unless `edit: false`) + the ticket + the worker's drain: the signed result.
   * `crash: true` makes the round throw inside `runRound`: its packet dir path is taken by a
   * regular file, so `fixHunkDiff`'s mkdir fails (EEXIST) before any session.
   */
  const round = async ({ edit = true, crash = false } = {}) => {
    if (edit) {
      n += 1;
      appendFileSync(path.join(repo, FILE), `export const fix${n} = ${n};\n`);
    }
    const t = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: FILE });
    // the same content again: drop the done marker, as `review-file` does for a re-run
    rmSync(path.join(queueDir(repo), `${t.ticket}.done`), { force: true });
    if (crash) {
      mkdirSync(path.join(runRootDir, 'packets'), { recursive: true });
      writeFileSync(path.join(runRootDir, 'packets', t.ticket), '');
    }
    assert.equal(await worker.drain(), 1);
    return /** @type {Record<string, any>} */ (readResult(repo, runId, t.ticket));
  };
  const stateFile = statePath(runId, BLOCK, FILE);
  // B69: the state lives under the code-forge home; `runRootDir` stays only for the old-path lookup
  const where = () => ({ runRootDir, runId, block: BLOCK, file: FILE, key, rows });
  const anchors = () => rows.filter((r) => r.event === 'review.state');
  const restoredRows = () => rows.filter((r) => r.event === 'review.state_restored');
  /**
   * Replace the ledger row `original` with `edited` signed with the run key (a valid MAC on edited content).
   * @param {Record<string, any>} original @param {Record<string, any>} edited
   */
  const resign = (original, edited) => {
    const i = rows.indexOf(original);
    assert.equal(i >= 0, true);
    const { mac: _mac, ...rest } = edited;
    rows[i] = signRow(rest, key);
  };
  return { repo, runId, key, runRootDir, rows, lenses, packets, script, round, stateFile, where, anchors, restoredRows, resign };
}

/**
 * @param {Awaited<ReturnType<typeof wired>>} w @param {string} reason
 * @returns {Promise<Record<string, any>>} the refused result; asserts 0 sessions and no approval.
 */
async function refused(w, reason) {
  const before = w.lenses.length;
  const r = await w.round();
  assert.deepEqual([r.status, r.reason, r.approved], ['refused', reason, false]);
  assert.equal(w.lenses.length, before);
  assert.equal(w.rows.filter((row) => row.event === 'review.approved').length, 0);
  assert.equal(w.restoredRows().length, 0);
  return r;
}

describe('a lost review-state file is rebuilt from the signed ledger, never reset (B68)', () => {
  test('(7) the anchor carries the slim signed state: reviewed_content null and the sha of the reviewed text', async () => {
    const w = await wired([finding('F1')]);
    assert.equal((await w.round()).round, 1);
    const [anchor] = w.anchors();
    assert.equal(w.anchors().length, 1);
    assert.equal(verifyRow(anchor, w.key).ok, true);
    const body = anchor.state_body;
    assert.deepEqual(Object.keys(body).sort(), ['block', 'file', 'mac', 'reviewed_content_blob', 'reviewed_content_sha', 'run', 'seq', 'state', 'ts']);
    assert.equal(verifyRow(body, w.key).ok, true);
    assert.deepEqual([body.run, body.block, body.file, body.seq], [w.runId, BLOCK, FILE, 1]);
    assert.equal(body.state.reviewed_content, null);
    const reviewed = 'export const a = 1;\nexport const fix1 = 1;\n';
    assert.equal(readFileSync(path.join(w.repo, FILE), 'utf8'), reviewed);
    assert.equal(body.reviewed_content_sha, sha(reviewed));
    assert.equal(Object.hasOwn(anchor, 'state_omitted'), false);
    // the state file itself still holds the full reviewed text, and its sha is the anchor's state_sha
    const text = readFileSync(w.stateFile, 'utf8');
    assert.equal(sha(text), anchor.state_sha);
    assert.equal(JSON.parse(text).state.reviewed_content, reviewed);
  });

  test('(1) the state file removed after round 1: the next ticket rebuilds it, writes one signed review.state_restored row and runs round 2 with the same open findings', async () => {
    const w = await wired([finding('F1'), finding('F2')]);
    const first = await w.round();
    assert.deepEqual([first.round, first.findings.map((/** @type {any} */ f) => f.id)], [1, ['F1', 'F2']]);
    rmSync(w.stateFile);
    const r = await w.round();
    assert.deepEqual([r.status, r.round, r.kind, r.level, r.approved, r.findings.map((/** @type {any} */ f) => f.id)], ['reviewed', 2, 'recheck', 'L2', false, ['F2']]);
    assert.deepEqual(w.lenses, ['quick', 'recheck']);
    assert.deepEqual(openIdsOf(w.packets[1]), ['F1', 'F2']); // round 2 rechecked both open findings
    const restored = w.restoredRows();
    assert.equal(restored.length, 1);
    assert.equal(verifyRow(restored[0], w.key).ok, true);
    assert.deepEqual(Object.keys(restored[0]).sort(), ['block', 'content_hash', 'event', 'file', 'from_seq', 'mac', 'run', 'seq', 'ts']);
    assert.deepEqual([restored[0].block, restored[0].file, restored[0].seq, restored[0].from_seq, restored[0].run], [BLOCK, FILE, 2, 1, w.runId]);
    // anchors: 1 (round 1), 2 (the restore), 3 (round 2); the restored row sits right after anchor 2
    assert.deepEqual(w.anchors().map((a) => a.seq), [1, 2, 3]);
    assert.equal(w.rows.indexOf(restored[0]), w.rows.indexOf(w.anchors()[1]) + 1);
    assert.equal(statSync(w.stateFile).mode & 0o777, 0o600);
    const saved = loadState(w.where());
    assert.deepEqual([saved.status, saved.seq, saved.state?.round, saved.state?.open.map((f) => f.id)], ['ok', 3, 2, ['F2']]);
  });

  test('(2) the file unchanged since the review: the restored reviewed_content is that text and round 2 diffs only the fix; changed with no blob left: round 2 carries the full file', async () => {
    const same = await wired([finding('F1'), finding('F2')]);
    assert.equal((await same.round()).round, 1);
    const reviewed = readFileSync(path.join(same.repo, FILE), 'utf8');
    rmSync(same.stateFile);
    const probe = loadState({ ...same.where(), contentRoot: same.repo });
    assert.deepEqual([probe.status, probe.seq, probe.state?.reviewed_content, probe.state?.round], ['restored', 1, reviewed, 1]);
    assert.equal(existsSync(same.stateFile), false); // a load never writes
    // the same content again (no coder edit) on a ticket whose round throws before any session:
    // the restore is saved, then the round is saved as a retry — its reviewed_content kept
    const idle = await same.round({ edit: false, crash: true });
    assert.deepEqual([idle.status, idle.reason, idle.round, idle.next], ['unavailable', 'EEXIST', 1, { action: 'retry', reason: 'EEXIST' }]);
    assert.equal(same.restoredRows().length, 1);
    assert.equal(same.lenses.length, 1);
    const kept = loadState(same.where());
    assert.deepEqual([kept.status, kept.seq, kept.state?.reviewed_content], ['ok', 3, reviewed]);
    // the engine's restore save (anchor seq 2) carried the reviewed text's sha, and the file it wrote
    // (kept by the retry save) holds the reviewed text itself
    const restoreAnchor = same.anchors().find((a) => a.seq === 2);
    assert.equal(restoreAnchor?.state_body?.reviewed_content_sha, sha(reviewed));
    assert.equal(JSON.parse(readFileSync(same.stateFile, 'utf8')).state.reviewed_content, reviewed);
    const fixed = await same.round();
    assert.deepEqual([fixed.round, fixed.kind], [2, 'recheck']);
    assert.deepEqual(hunksOf(same.packets[1]), ['@@ -1,2 +1,3 @@']); // the fix diff against the 2 reviewed lines, not the full file

    // changed AND its B78 blob gone from the object store: the B68 fallback, the full file
    const moved = await wired([finding('F1'), finding('F2')]);
    assert.equal((await moved.round()).round, 1);
    rmSync(moved.stateFile);
    dropBlob(moved.repo, moved.anchors()[0].state_body.reviewed_content_blob);
    const r = await moved.round(); // the coder's fix landed while the state was gone
    assert.deepEqual([r.round, r.kind], [2, 'recheck']);
    assert.equal(moved.restoredRows().length, 1);
    assert.deepEqual(hunksOf(moved.packets[1]), ['@@ -0,0 +1,3 @@']); // the full file
  });

  test('(3) a stopped file whose state file is removed stays stopped after the restore: no session, the same stop', async () => {
    const w = await wired(['F1', 'F2', 'F3', 'F4'].map((id) => finding(id)));
    await w.round();
    await w.round();
    w.script.resolveOne = false;
    const stop = await w.round();
    assert.deepEqual([stop.status, stop.stopped, stop.findings.map((/** @type {any} */ f) => f.id)], ['stopped', 'l3_patch_exhausted', ['F2', 'F3', 'F4']]);
    const before = w.lenses.length;
    const pre = loadState(w.where()); // the stop as saved before the loss
    assert.deepEqual([pre.status, pre.state?.status, pre.state?.next?.action, pre.state?.next?.reason], ['ok', 'stopped', 'stop', 'l3_patch_exhausted']);
    rmSync(w.stateFile);
    const after = await w.round();
    assert.deepEqual([after.status, after.stopped, after.approved, after.round, after.findings.map((/** @type {any} */ f) => f.id)], ['stopped', 'l3_patch_exhausted', false, stop.round, ['F2', 'F3', 'F4']]);
    assert.equal(after.stopped, pre.state?.next?.reason);
    assert.deepEqual(after.next, pre.state?.next); // the stop reason and its message, as before the loss
    assert.equal(w.lenses.length, before);
    assert.equal(w.restoredRows().length, 1);
    const saved = loadState(w.where());
    assert.deepEqual([saved.status, saved.state?.status, saved.state?.next], ['ok', 'stopped', pre.state?.next]);
  });

  test('(4) an anchor without state_body and no file is refused review-state-lost, naming block reopen, with no session', async () => {
    const w = await wired([finding('F1')]);
    assert.equal((await w.round()).round, 1);
    const [anchor] = w.anchors();
    const { state_body: _dropped, ...old } = anchor; // an anchor written before B68
    w.resign(anchor, old);
    rmSync(w.stateFile);
    const r = await refused(w, 'review-state-lost');
    assert.equal(typeof r.message, 'string');
    assert.equal(r.message.includes('code-forge block reopen'), true);
    assert.equal(r.message.includes('state file is gone'), true);
    assert.equal(existsSync(w.stateFile), false);
    assert.deepEqual(loadState(w.where()), { status: 'lost', state: null, seq: 1 });
  });

  test('(5) a body over 64 KB: the anchor carries state_omitted too_large, and its loss is lost', async () => {
    const pad = 'x'.repeat(280);
    const many = Array.from({ length: 90 }, (_, i) => finding(`F${i + 1}`, pad));
    const w = await wired(many);
    const first = await w.round();
    assert.equal(first.findings.length, 90);
    const [anchor] = w.anchors();
    assert.equal(anchor.state_omitted, 'too_large');
    assert.equal(Object.hasOwn(anchor, 'state_body'), false);
    assert.equal(verifyRow(anchor, w.key).ok, true);
    rmSync(w.stateFile);
    await refused(w, 'review-state-lost');
  });

  test('(6) still tampered: a changed byte, an older signed file, a bad state_body signature or seq, a file with no anchor; a wrong file is never overwritten', async () => {
    // a changed byte in a present file
    const edited = await wired([finding('F1')]);
    await edited.round();
    const good = readFileSync(edited.stateFile, 'utf8');
    const bad = good.replace('"round":1', '"round":0');
    assert.notEqual(bad, good);
    writeFileSync(edited.stateFile, bad);
    await refused(edited, 'review-state-tampered');
    assert.equal(readFileSync(edited.stateFile, 'utf8'), bad);

    // an older signed file put back
    const rolled = await wired([finding('F1'), finding('F2')]);
    await rolled.round();
    const older = readFileSync(rolled.stateFile, 'utf8');
    await rolled.round();
    writeFileSync(rolled.stateFile, older);
    await refused(rolled, 'review-state-tampered');
    assert.equal(readFileSync(rolled.stateFile, 'utf8'), older);

    // a state_body whose own signature fails (the anchor re-signed around the edit)
    const forged = await wired([finding('F1')]);
    await forged.round();
    const [a1] = forged.anchors();
    forged.resign(a1, { ...a1, state_body: { ...a1.state_body, state: { ...a1.state_body.state, open: [] } } });
    rmSync(forged.stateFile);
    await refused(forged, 'review-state-tampered');
    assert.equal(existsSync(forged.stateFile), false);

    // a validly signed state_body naming the wrong seq
    const reseq = await wired([finding('F1')]);
    await reseq.round();
    const [a2] = reseq.anchors();
    const { mac: _m, ...inner } = a2.state_body;
    reseq.resign(a2, { ...a2, state_body: signRow({ ...inner, seq: 7 }, reseq.key) });
    rmSync(reseq.stateFile);
    await refused(reseq, 'review-state-tampered');
    assert.equal(existsSync(reseq.stateFile), false);

    // a present file with no anchor
    const orphan = await wired([finding('F1')]);
    await orphan.round();
    const kept = readFileSync(orphan.stateFile, 'utf8');
    for (const a of orphan.anchors()) orphan.rows.splice(orphan.rows.indexOf(a), 1);
    assert.equal(orphan.anchors().length, 0);
    await refused(orphan, 'review-state-tampered');
    assert.equal(readFileSync(orphan.stateFile, 'utf8'), kept);
  });
});

describe('B78: the reviewed text comes back from a git blob after a restore', () => {
  const REVIEWED = 'export const a = 1;\nexport const fix1 = 1;\n';
  const LATER = 'export const later = 2;\n';

  /**
   * Replace the anchor's state_body with `inner` (no mac) signed with the run key, the anchor re-signed too.
   * @param {Awaited<ReturnType<typeof wired>>} w @param {Record<string, any>} anchor @param {Record<string, any>} inner
   */
  const rebody = (w, anchor, inner) => w.resign(anchor, { ...anchor, state_body: signRow(inner, w.key) });

  test('(a) the file CHANGED after the review: the restored reviewed_content is the old reviewed text exactly and round 2 diffs only the change', async () => {
    const w = await wired([finding('F1'), finding('F2')]);
    assert.equal((await w.round()).round, 1);
    const [anchor] = w.anchors();
    assert.equal(anchor.state_body.reviewed_content_blob, blobId(REVIEWED));
    assert.equal(anchor.state_body.reviewed_content_sha, sha(REVIEWED));
    rmSync(w.stateFile);
    appendFileSync(path.join(w.repo, FILE), LATER);
    const probe = loadState({ ...w.where(), contentRoot: w.repo });
    assert.deepEqual([probe.status, probe.seq, probe.state?.reviewed_content, probe.state?.round], ['restored', 1, REVIEWED, 1]);
    // no content root: nothing to read the blob from, null as before
    assert.equal(loadState(w.where()).state?.reviewed_content, null);
    const r = await w.round(); // one more coder edit: the file now has 4 lines, 2 of them reviewed
    assert.deepEqual([r.status, r.round, r.kind], ['reviewed', 2, 'recheck']);
    assert.equal(w.restoredRows().length, 1);
    assert.deepEqual(hunksOf(w.packets[1]), ['@@ -1,2 +1,4 @@']); // the change since the review, not the full file
  });

  test('(b) the blob pruned from the object store: falls back, reviewed_content null for a changed file and the full file next round', async () => {
    const w = await wired([finding('F1'), finding('F2')]);
    assert.equal((await w.round()).round, 1);
    rmSync(w.stateFile);
    appendFileSync(path.join(w.repo, FILE), LATER);
    dropBlob(w.repo, w.anchors()[0].state_body.reviewed_content_blob);
    const probe = loadState({ ...w.where(), contentRoot: w.repo });
    assert.deepEqual([probe.status, probe.seq, probe.state?.reviewed_content], ['restored', 1, null]);
    const r = await w.round();
    assert.deepEqual([r.round, r.kind], [2, 'recheck']);
    assert.deepEqual(hunksOf(w.packets[1]), ['@@ -0,0 +1,4 @@']); // the full file
  });

  test('(c) a signed body whose blob id names another text, or no object id at all, is never used: the sha check rejects it', async () => {
    const w = await wired([finding('F1')]);
    assert.equal((await w.round()).round, 1);
    rmSync(w.stateFile);
    const other = 'export const somethingElse = 3;\n';
    const otherId = await hashObject(w.repo, other);
    assert.equal(otherId, blobId(other)); // the blob is there and readable
    const [anchor] = w.anchors();
    const { mac: _m, ...inner } = anchor.state_body;
    rebody(w, anchor, { ...inner, reviewed_content_blob: otherId });
    // file unchanged: the wrong blob is skipped, the B68 rule gives the current (= reviewed) text
    const unchanged = loadState({ ...w.where(), contentRoot: w.repo });
    assert.deepEqual([unchanged.status, unchanged.state?.reviewed_content], ['restored', REVIEWED]);
    // file changed: the wrong blob is still skipped, nothing matches the sha, null
    appendFileSync(path.join(w.repo, FILE), LATER);
    const wrong = loadState({ ...w.where(), contentRoot: w.repo });
    assert.deepEqual([wrong.status, wrong.state?.reviewed_content], ['restored', null]);
    // ids that are not 40/64 lowercase hex are ignored, even a ref or a prefix that names the right text
    const goodId = inner.reviewed_content_blob;
    assert.equal(goodId, blobId(REVIEWED));
    for (const id of ['HEAD', goodId.toUpperCase(), goodId.slice(0, 39), `-${goodId.slice(1)}`, 42]) {
      const [now] = w.anchors();
      rebody(w, now, { ...inner, reviewed_content_blob: id });
      const got = loadState({ ...w.where(), contentRoot: w.repo });
      assert.deepEqual([got.status, got.state?.reviewed_content], ['restored', null], String(id));
    }
    // the real id again: the text comes back (the loop above failed for the id, not the repo)
    const [last] = w.anchors();
    rebody(w, last, { ...inner, reviewed_content_blob: goodId });
    assert.equal(loadState({ ...w.where(), contentRoot: w.repo }).state?.reviewed_content, REVIEWED);
  });

  test('(d) an old anchor with no reviewed_content_blob restores exactly as B68: the current text when unchanged, else null', async () => {
    const w = await wired([finding('F1')]);
    assert.equal((await w.round()).round, 1);
    rmSync(w.stateFile);
    const [anchor] = w.anchors();
    const { mac: _m, reviewed_content_blob: _b, ...inner } = anchor.state_body;
    rebody(w, anchor, inner);
    assert.equal(Object.hasOwn(w.anchors()[0].state_body, 'reviewed_content_blob'), false);
    const same = loadState({ ...w.where(), contentRoot: w.repo });
    assert.deepEqual([same.status, same.seq, same.state?.reviewed_content], ['restored', 1, REVIEWED]);
    appendFileSync(path.join(w.repo, FILE), LATER);
    const changed = loadState({ ...w.where(), contentRoot: w.repo });
    assert.deepEqual([changed.status, changed.seq, changed.state?.reviewed_content], ['restored', 1, null]);
  });

  test('(e) the blob write fails (content root not a git repo): the save still succeeds with reviewed_content_blob null', async () => {
    const w = await wired([finding('F1')]);
    assert.equal((await w.round()).round, 1);
    const loaded = loadState(w.where());
    assert.deepEqual([loaded.status, loaded.state?.reviewed_content], ['ok', REVIEWED]);
    const state = /** @type {NonNullable<typeof loaded.state>} */ (loaded.state);
    /** @type {Array<Record<string, any>>} */
    const out = [];
    // a plain directory INSIDE the run's git checkout: discovery stops at its parent, so the
    // enclosing repo is never written into
    const notRepo = path.join(w.repo, 'plain', 'tree');
    mkdirSync(notRepo, { recursive: true });
    const expectedId = blobId(REVIEWED);
    const has = async (/** @type {string} */ id) => (await exec(['git', 'cat-file', '-e', id], { cwd: w.repo, env: gitChildEnv(), timeoutMs: 20000 })).code;
    dropBlob(w.repo, expectedId); // round 1 put the reviewed text there; start without it
    assert.notEqual(await has(expectedId), 0);
    assert.equal(await saveState({ ...w.where(), state, seq: 1, writeRow: async (row) => void out.push(row), contentRoot: notRepo }), undefined);
    assert.notEqual(await has(expectedId), 0); // the enclosing repo gained no object for that text
    assert.equal(out.length, 1);
    assert.deepEqual([out[0].event, out[0].seq, out[0].state_body.reviewed_content_blob, out[0].state_body.reviewed_content_sha], ['review.state', 2, null, sha(REVIEWED)]);
    assert.equal(JSON.parse(readFileSync(w.stateFile, 'utf8')).state.reviewed_content, REVIEWED);
    assert.equal(sha(readFileSync(w.stateFile, 'utf8')), out[0].state_sha);
    // the same save into the git repo writes the blob
    assert.equal(await saveState({ ...w.where(), state, seq: 2, writeRow: async (row) => void out.push(row), contentRoot: w.repo }), undefined);
    assert.equal(out.length, 2);
    assert.deepEqual([out[1].seq, out[1].state_body.reviewed_content_blob], [3, expectedId]);
    assert.equal(await has(expectedId), 0);
  });
});
