// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import { freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { appendFileSync, chmodSync, existsSync, mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { enqueue, queueDir, readResult } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { loadKey, signRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { blockStateDir, legacyStatePath, loadState, statePath } = await import('../../src/worker/review-state.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { runDoctor } = await import('../../src/doctor/index.mjs');
const { runsDir } = await import('../../src/state/paths.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { LISTED_MAX } = await import('../../src/doctor/local.mjs');

const FILE = 'src/a.mjs';
const OTHER = 'src/b.mjs';
const BLOCK = 'B11';
const name24 = (/** @type {string} */ file) => createHash('sha256').update(file).digest('hex').slice(0, 24);

/** @param {string} id @param {string} [file] */
const finding = (id, file = FILE) => ({ id, file, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

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
 * The B68 fixture (test/worker/review-state-restore.test.mjs): an in-process worker on a stub
 * session spawner (round 1: two findings per file; a recheck resolves the FIRST listed open id), a
 * mocked Jev, and the ledger rows in an array. `restart()` builds a new worker on the same run.
 */
async function wired() {
  const { repo, runId } = await makeRepo();
  const key = await loadKey(runId);
  const runRootDir = freshDir('runroot');
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
    const file = text.includes(OTHER) ? OTHER : FILE;
    const resolved = open.map((id, i) => ({ id, resolved: lens === 'recheck' && i === 0, why: i === 0 ? 'fixed' : 'still open' }));
    const answer = { passed: false, summary: 's', reviewed_hunks: hunksOf(text), findings: lens === 'recheck' ? [] : [finding('F1', file), finding('F2', file)], resolved, needs_file: [] };
    return { status: 'ok', exit_code: 0, answer, usage: { tokens_in: 900, tokens_out: 300 } };
  };
  const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
    const [id] = Object.keys(req.questions);
    return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
  };
  const boot = async () =>
    createWorker(
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
  let worker = await boot();
  let n = 0;
  /** One coder edit + the ticket + the worker's drain: the signed result. */
  const round = async (file = FILE) => {
    n += 1;
    appendFileSync(path.join(repo, file), `export const fix${n} = ${n};\n`);
    const t = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file });
    rmSync(path.join(queueDir(repo), `${t.ticket}.done`), { force: true });
    assert.equal(await worker.drain(), 1);
    return /** @type {Record<string, any>} */ (readResult(repo, runId, t.ticket));
  };
  const restart = async () => {
    worker = await boot();
  };
  const stateFile = statePath(runId, BLOCK, FILE);
  const oldFile = legacyStatePath(runRootDir, BLOCK, FILE);
  const where = () => ({ runRootDir, runId, block: BLOCK, file: FILE, key, rows });
  const anchors = () => rows.filter((r) => r.event === 'review.state');
  const restoredRows = () => rows.filter((r) => r.event === 'review.state_restored');
  return { repo, runId, key, runRootDir, rows, lenses, packets, round, restart, stateFile, oldFile, where, anchors, restoredRows };
}

/** @param {string} dir */
const mode = (dir) => statSync(dir).mode & 0o777;

describe('the review state lives under the code-forge home, not the temp root (B69)', () => {
  test('(1) after round 1 the state file is at runs/<run>/review-state/<block>/<sha24>.json, 0600 in a 0700 dir, nothing under the temp root', async () => {
    const w = await wired();
    assert.equal((await w.round()).round, 1);
    const expected = path.join(runsDir(), w.runId, 'review-state', BLOCK, `${name24(FILE)}.json`);
    assert.equal(w.stateFile, expected);
    assert.equal(existsSync(expected), true);
    assert.equal(mode(expected), 0o600);
    assert.equal(blockStateDir(w.runId, BLOCK), path.dirname(expected));
    assert.equal(mode(path.dirname(expected)), 0o700);
    assert.equal(mode(path.dirname(path.dirname(expected))), 0o700);
    assert.equal(existsSync(path.dirname(path.dirname(w.oldFile))), false);
    assert.equal(existsSync(w.oldFile), false);
    assert.deepEqual(loadState(w.where()).status, 'ok');
  });

  test('(2) the whole temp run root removed and the worker restarted: the next ticket loads ok and runs round 2 with the same findings, 0 restored rows', async () => {
    const w = await wired();
    const first = await w.round();
    assert.deepEqual([first.round, first.findings.map((/** @type {any} */ f) => f.id)], [1, ['F1', 'F2']]);
    rmSync(w.runRootDir, { recursive: true, force: true });
    assert.equal(existsSync(w.runRootDir), false);
    await w.restart();
    assert.deepEqual(loadState(w.where()).status, 'ok');
    const r = await w.round();
    assert.deepEqual([r.status, r.round, r.kind, r.findings.map((/** @type {any} */ f) => f.id)], ['reviewed', 2, 'recheck', ['F2']]);
    assert.deepEqual(w.lenses, ['quick', 'recheck']);
    assert.deepEqual(openIdsOf(w.packets[1]), ['F1', 'F2']);
    assert.equal(w.restoredRows().length, 0);
    assert.deepEqual(w.anchors().map((a) => a.seq), [1, 2]);
  });

  test('(3) a valid state at the old temp path (none at the new one) is moved: same bytes, old gone, 0 restored rows; round 2 runs', async () => {
    const w = await wired();
    await w.round();
    const bytes = readFileSync(w.stateFile);
    mkdirSync(path.dirname(w.oldFile), { recursive: true });
    renameSync(w.stateFile, w.oldFile);
    assert.equal(existsSync(w.stateFile), false);
    const loaded = loadState(w.where());
    assert.deepEqual([loaded.status, loaded.seq, loaded.state?.round], ['ok', 1, 1]);
    assert.equal(Buffer.compare(readFileSync(w.stateFile), bytes), 0);
    assert.equal(mode(w.stateFile), 0o600);
    assert.equal(existsSync(w.oldFile), false);
    assert.equal(w.restoredRows().length, 0);
    const r = await w.round();
    assert.deepEqual([r.round, r.kind], [2, 'recheck']);
    assert.equal(w.restoredRows().length, 0);

    // the same move through the worker's own ticket
    const t = await wired();
    await t.round();
    mkdirSync(path.dirname(t.oldFile), { recursive: true });
    renameSync(t.stateFile, t.oldFile);
    const r2 = await t.round();
    assert.deepEqual([r2.status, r2.round, r2.kind], ['reviewed', 2, 'recheck']);
    assert.equal(existsSync(t.oldFile), false);
    assert.equal(existsSync(t.stateFile), true);
    assert.equal(t.restoredRows().length, 0);
    assert.deepEqual(t.anchors().map((a) => a.seq), [1, 2]);
  });

  test('(4) a wrong file at the old path is refused review-state-tampered: old file unchanged, no new file, no session', async () => {
    const w = await wired();
    await w.round();
    const original = readFileSync(w.stateFile, 'utf8');
    const bad = original.replace('"round":1', '"round":0');
    assert.notEqual(bad, original);
    mkdirSync(path.dirname(w.oldFile), { recursive: true });
    writeFileSync(w.oldFile, bad);
    rmSync(w.stateFile);
    const before = w.lenses.length;
    const r = await w.round();
    assert.deepEqual([r.status, r.reason, r.approved], ['refused', 'review-state-tampered', false]);
    assert.equal(w.lenses.length, before);
    assert.equal(readFileSync(w.oldFile, 'utf8'), bad);
    assert.equal(existsSync(w.stateFile), false);
    assert.equal(w.restoredRows().length, 0);
  });

  test('(5) doctor: an active run with one missing rebuildable and one missing lost state file gives exactly one WARN row; all present gives none', async () => {
    const w = await wired();
    await w.round(FILE);
    await w.round(OTHER);
    assert.equal(w.anchors().length, 2);
    // the second file's anchor carries no body (state_omitted), re-signed with the run key
    const other = /** @type {Record<string, any>} */ (w.anchors().find((a) => a.file === OTHER));
    const { mac: _mac, state_body: _body, ...rest } = other;
    w.rows[w.rows.indexOf(other)] = signRow({ ...rest, state_omitted: 'too_large' }, w.key);
    for (const r of w.rows) await appendRow(r, { slug: 'worker-test' });
    const doctor = async () =>
      (await runDoctor(
        { cwd: freshDir('nocfg') },
        { env: { HOME: process.env.HOME, PATH: '' }, store: await createKeyStore({ backends: [], dir: freshDir('dstore') }), platform: 'linux', toolExists: () => true },
      )).filter((r) => r.id === 'review-state' && r.detail.startsWith(`run ${w.runId}:`));

    assert.deepEqual(await doctor(), []);

    const otherFile = statePath(w.runId, BLOCK, OTHER);
    assert.equal(existsSync(otherFile), true);
    rmSync(w.stateFile);
    rmSync(otherFile);
    const found = await doctor();
    assert.equal(found.length, 1);
    assert.deepEqual(found[0], {
      id: 'review-state',
      status: 'WARN',
      label: 'review state',
      detail: `run ${w.runId}: 2 state file(s) missing — 1 rebuildable from the ledger, 1 lost (needs code-forge block reopen): ${BLOCK} ${FILE} (rebuildable), ${BLOCK} ${OTHER} (lost)`,
    });
    assert.equal(Buffer.isBuffer(w.key), true);
    assert.equal(found[0].detail.includes(w.key.toString('hex')), false);
  });
});

/** @param {string} runId @returns {Promise<Array<Record<string, any>>>} doctor's review-state rows for this run. */
async function doctorRowsOf(runId) {
  const all = await runDoctor(
    { cwd: freshDir('nocfg') },
    { env: { HOME: process.env.HOME, PATH: '' }, store: await createKeyStore({ backends: [], dir: freshDir('dstore') }), platform: 'linux', toolExists: () => true },
  );
  return all.filter((r) => r.id === 'review-state' && r.detail.startsWith(`run ${runId}:`));
}

/** @param {string} runId @param {Buffer} key @param {string} file @returns {Record<string, any>} a signed anchor with a body. */
const craftedAnchor = (runId, key, file) =>
  signRow({ run: runId, event: 'review.state', block: BLOCK, file, seq: 1, state_sha: '0'.repeat(64), l3_rung_used: false, state_body: {} }, key);

describe('B69 review round 1: edges of the move and of the doctor row', () => {
  test('a bad file or block id is review-state-tampered, never a throw', async () => {
    const w = await wired();
    await w.round();
    assert.deepEqual(loadState({ ...w.where(), file: '../x' }), { status: 'tampered', state: null, seq: 0 });
    assert.deepEqual(loadState({ ...w.where(), block: '../B' }), { status: 'tampered', state: null, seq: 0 });
  });

  test('a state saved at the new path between the two reads is loaded ok, not restored', async () => {
    const w = await wired();
    await w.round();
    const bytes = readFileSync(w.stateFile);
    rmSync(w.stateFile);
    let called = 0;
    const loaded = loadState({
      ...w.where(),
      afterMiss: () => {
        called += 1;
        writeFileSync(w.stateFile, bytes, { mode: 0o600 });
      },
    });
    assert.equal(called, 1);
    assert.deepEqual([loaded.status, loaded.seq, loaded.state?.round], ['ok', 1, 1]);
    assert.equal(w.restoredRows().length, 0);
    assert.equal(Buffer.compare(readFileSync(w.stateFile), bytes), 0);
  });

  // root ignores a 0500 dir's write bit, so the move would succeed
  test('a move that cannot write the new path loads ok and leaves the old file where it is', { skip: process.getuid?.() === 0 }, async () => {
    const w = await wired();
    await w.round();
    const bytes = readFileSync(w.stateFile);
    mkdirSync(path.dirname(w.oldFile), { recursive: true });
    renameSync(w.stateFile, w.oldFile);
    const dir = blockStateDir(w.runId, BLOCK);
    chmodSync(dir, 0o500);
    try {
      const loaded = loadState(w.where());
      assert.deepEqual([loaded.status, loaded.seq, loaded.state?.round], ['ok', 1, 1]);
    } finally {
      chmodSync(dir, 0o700);
    }
    assert.equal(Buffer.compare(readFileSync(w.oldFile), bytes), 0);
    assert.equal(existsSync(w.stateFile), false);
    assert.equal(w.restoredRows().length, 0);
  });

  test('a ticket for one file never moves a sibling file\'s old state (rung check reads it with migrate off)', async () => {
    const w = await wired();
    await w.round(FILE);
    await w.round(OTHER);
    const otherNew = statePath(w.runId, BLOCK, OTHER);
    const otherOld = legacyStatePath(w.runRootDir, BLOCK, OTHER);
    const bytes = readFileSync(otherNew);
    mkdirSync(path.dirname(otherOld), { recursive: true });
    renameSync(otherNew, otherOld);
    const r = await w.round(FILE);
    assert.deepEqual([r.status, r.round], ['reviewed', 2]);
    assert.equal(Buffer.compare(readFileSync(otherOld), bytes), 0);
    assert.equal(existsSync(otherNew), false);
  });

  test(`doctor lists at most ${LISTED_MAX} missing files, then "… and K more"`, async () => {
    const { runId } = await makeRepo();
    const key = await loadKey(runId);
    const files = Array.from({ length: 12 }, (_, i) => `src/f${String(i + 1).padStart(2, '0')}.mjs`);
    for (const file of files) await appendRow(craftedAnchor(runId, key, file), { slug: 'worker-test' });
    const found = await doctorRowsOf(runId);
    assert.equal(files.length > LISTED_MAX, true);
    const listed = files.slice(0, LISTED_MAX).map((f) => `${BLOCK} ${f} (rebuildable)`).join(', ');
    assert.deepEqual(found, [
      {
        id: 'review-state',
        status: 'WARN',
        label: 'review state',
        detail: `run ${runId}: ${files.length} state file(s) missing — ${files.length} rebuildable from the ledger, 0 lost (needs code-forge block reopen): ${listed} … and ${files.length - LISTED_MAX} more`,
      },
    ]);
  });

  test('doctor says so when an anchor of the run does not verify, instead of staying silent', async () => {
    const { runId } = await makeRepo();
    const key = await loadKey(runId);
    const forged = { ...craftedAnchor(runId, key, FILE), seq: 2 }; // changed after signing: its MAC fails
    await appendRow(forged, { slug: 'worker-test' });
    assert.deepEqual(await doctorRowsOf(runId), [
      { id: 'review-state', status: 'WARN', label: 'review state', detail: `run ${runId}: review.state anchors do not verify; review state not checked` },
    ]);
  });

  test('doctor does not count files when the run\'s tmp.root is not absolute: the row says the old location could not be checked', async () => {
    const runId = `r-b69-${process.pid}-rel`;
    await startRun({ workspace: freshDir('relws'), project: 'worker-test', runId, config: { tmp: { root: 'relative/x' } }, writeRow: async () => {} });
    const key = await loadKey(runId);
    await appendRow(craftedAnchor(runId, key, FILE), { slug: 'worker-test' });
    assert.deepEqual(await doctorRowsOf(runId), [
      {
        id: 'review-state',
        status: 'WARN',
        label: 'review state',
        detail: `run ${runId}: the old temp location could not be checked (tmp.root is not an absolute path); review state files are not counted`,
      },
    ]);
  });
});
