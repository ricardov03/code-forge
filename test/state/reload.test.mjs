// helpers FIRST: its import-time guard moves $HOME and cwd to a temp dir before any src module loads.
import { captureStream, countOccurrences, rowSink, withFixture } from './helpers.mjs';
import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { parse as parseYAML } from 'yaml';
import { runRun } from '../../src/cli/run.mjs';
import { readAllRows } from '../../src/ledger/write.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { changedKeyPaths, configHash, immutableChanges, snapshotFor } from '../../src/state/config-snapshot.mjs';
import { readRun, reloadRun, saveRun, startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { enqueue } from '../../src/worker/queue.mjs';
import { CONFIG_YAML } from '../fixtures/repos/two-blocks/build.mjs';

const ACCEPTANCE = [{ clause: 'c1', tests: ['t1'] }];
const NEW_TIMEOUT = 4242;

/** The fixture config with L2 and L3 models swapped and a review timeout added. */
const CHANGED_YAML = CONFIG_YAML.replace('L2: {model: claude-opus-5-5}', 'L2: {model: claude-fable-5-1}').replace('L3: {model: claude-fable-5-1}', 'L3: {model: claude-opus-5-5}') + `review:\n  session_timeout_s: ${NEW_TIMEOUT}\n`;

/** @param {string} ws @param {string} text */
const writeConfig = (ws, text) => writeFile(path.join(ws, '.code-forge.yml'), text);

/** @param {string} ws @param {string} runId */
async function started(ws, runId) {
  assert.equal(await runRun(['start', '--cwd', ws, '--run', runId], { stdout: captureStream(), stderr: captureStream() }), 0);
}

/** @param {string} runId */
async function reload(runId) {
  const stdout = captureStream();
  const stderr = captureStream();
  const code = await runRun(['reload', '--run', runId], { stdout, stderr });
  return { code, out: stdout.text, err: stderr.text };
}

test('`run reload` prints exactly the changed key paths, never a value; one signed run.reload row; open blocks and queued tickets kept', async () => {
  await withFixture(async ({ ws }) => {
    await started(ws, 'r-rl');
    await openBlock({ runId: 'r-rl', id: 'B1', level: 'L1', owned: ['a.txt'], acceptance: ACCEPTANCE, writeRow: async () => {} });
    const queued = enqueue({ repoRoot: ws, run: 'r-rl', block: 'B1', file: 'a.txt' });
    const before = await readRun('r-rl');
    await writeConfig(ws, CHANGED_YAML);

    const res = await reload('r-rl');
    assert.equal(res.code, 0, res.err);
    assert.equal(res.err, '');
    assert.equal(
      res.out,
      'run r-rl: config reloaded · 3 keys changed\n' +
        '  levels.L2.model\n' +
        '  levels.L3.model\n' +
        '  review\n' +
        'queued review tickets (1) keep the config they were enqueued with; new tickets use the new config\n',
    );
    for (const value of ['claude-opus-5-5', 'claude-fable-5-1', String(NEW_TIMEOUT)]) assert.equal(countOccurrences(res.out, value), 0, value);

    const oldHash = configHash(parseYAML(CONFIG_YAML));
    const newHash = configHash(parseYAML(CHANGED_YAML));
    const after = await readRun('r-rl');
    assert.deepEqual(after.blocks, before.blocks);
    assert.equal(after.blocks.B1.attempt, 1);
    assert.equal(after.blocks.B1.status, 'open');
    assert.equal(after.config.hash, newHash);
    assert.deepEqual(after.config.pins, { [queued.ticket]: oldHash });
    assert.deepEqual(Object.keys(after.config.snapshots).sort(), [oldHash, newHash].sort());
    assert.equal(after.levels.L2.model, 'claude-fable-5-1');

    const rows = await readAllRows('two-blocks');
    assert.deepEqual(rows.map((r) => r.event), ['run.start', 'run.reload']);
    const row = rows[1];
    assert.deepEqual(row.changed_keys, ['levels.L2.model', 'levels.L3.model', 'review']);
    assert.equal(row.old_hash, oldHash);
    assert.equal(row.new_hash, newHash);
    assert.deepEqual(verifyRow(row, await loadKey('r-rl')), { ok: true });
    // the mac, hashes and ts are random hex/digits that can contain the timeout digits by chance
    const { mac: _mac, old_hash: _oh, new_hash: _nh, ts: _ts, ...rowText } = row;
    for (const value of ['claude-opus-5-5', 'claude-fable-5-1', String(NEW_TIMEOUT)]) assert.equal(countOccurrences(JSON.stringify(rowText), value), 0, value);

    const again = await reload('r-rl');
    assert.equal(again.code, 0);
    assert.equal(again.out, 'run r-rl: no config change\n');
    assert.equal((await readAllRows('two-blocks')).length, 2);
  });
});

test('`run reload` refuses an invalid config: exit 1, rule and key path only, record and ledger unchanged', async () => {
  await withFixture(async ({ ws }) => {
    await started(ws, 'r-bad');
    const before = await readRun('r-bad');
    await writeConfig(ws, `${CONFIG_YAML}review:\n  session_timeout_s: not-a-number-FAKE\n`);
    const res = await reload('r-bad');
    assert.equal(res.code, 1);
    assert.equal(res.out, '');
    assert.equal(res.err, 'run reload: .code-forge.yml is invalid (1 error) — nothing changed; run code-forge validate\n  [schema] /review/session_timeout_s\n');
    assert.equal(countOccurrences(res.err, 'not-a-number-FAKE'), 0);
    assert.deepEqual(await readRun('r-bad'), before);
    assert.deepEqual((await readAllRows('two-blocks')).map((r) => r.event), ['run.start']);
  });
});

test('`run reload` refuses a key fixed for the run, naming EVERY changed fixed key; nothing written', async () => {
  await withFixture(async ({ ws }) => {
    await started(ws, 'r-imm');
    const before = await readRun('r-imm');
    await writeConfig(ws, CHANGED_YAML.replace('slug: two-blocks', 'slug: other-slug'));
    const slug = await reload('r-imm');
    assert.equal(slug.code, 1);
    assert.equal(slug.out, '');
    assert.equal(slug.err, 'run reload: project.slug cannot change mid-run (the ledger file of the run is chosen at run start) — put it back, or end the run and start a new one\n');

    await writeConfig(ws, CHANGED_YAML.replace('engine: harness', 'engine: solo'));
    const engine = await reload('r-imm');
    assert.equal(engine.code, 1);
    assert.equal(engine.out, '');
    assert.equal(engine.err, 'run reload: engine cannot change mid-run (the run engine is fixed at run start) — put it back, or end the run and start a new one\n');
    assert.equal(countOccurrences(engine.err, 'solo'), 0);

    await writeConfig(ws, CHANGED_YAML.replace('engine: harness', 'engine: solo').replace('slug: two-blocks', 'slug: other-slug'));
    const both = await reload('r-imm');
    assert.equal(both.code, 1);
    assert.equal(both.out, '');
    assert.equal(
      both.err,
      'run reload: project.slug cannot change mid-run (the ledger file of the run is chosen at run start); engine cannot change mid-run (the run engine is fixed at run start) — put them back, or end the run and start a new one\n',
    );

    assert.deepEqual(await readRun('r-imm'), before);
    assert.deepEqual((await readAllRows('two-blocks')).map((r) => r.event), ['run.start']);
  });
});

test('a run from before snapshots: fixed keys are checked against the record (engine, slug); a free change records the first snapshot', async () => {
  await withFixture(async ({ ws }) => {
    const { writeRow } = rowSink();
    await startRun({ workspace: ws, project: 'two-blocks', engine: 'harness', runId: 'r-old', writeRow }); // no config ⇒ no snapshot
    await writeConfig(ws, CHANGED_YAML.replace('engine: harness', 'engine: solo').replace('slug: two-blocks', 'slug: other-slug'));
    const refused = await reload('r-old');
    assert.equal(refused.code, 1);
    assert.equal(refused.out, '');
    assert.equal(
      refused.err,
      'run reload: project.slug cannot change mid-run (the ledger file of the run is chosen at run start); engine cannot change mid-run (the run engine is fixed at run start) — put them back, or end the run and start a new one\n',
    );
    assert.equal(Object.hasOwn(await readRun('r-old'), 'config'), false);

    await writeConfig(ws, CHANGED_YAML);
    const res = await reload('r-old');
    assert.equal(res.code, 0, res.err);
    assert.equal(
      res.out,
      'run r-old: config snapshot recorded (the run predates run reload; no earlier snapshot to compare)\n' +
        'queued review tickets (0) keep the config they were enqueued with; new tickets use the new config\n',
    );
    assert.equal((await readRun('r-old')).config.hash, configHash(parseYAML(CHANGED_YAML)));
    const row = (await readAllRows('two-blocks')).at(-1);
    assert.equal(row?.event, 'run.reload');
    assert.equal(row?.changed_keys, null);
    assert.equal(row?.old_hash, null);
  });
});

test('`run reload` on a file that does not parse prints only the file and line, never the parser text', async () => {
  await withFixture(async ({ ws }) => {
    await started(ws, 'r-parse');
    const before = await readRun('r-parse');
    await writeConfig(ws, 'version: 1\nproject:\n  slug: [two-blocks, sk-ant-FAKE0123456789\n');
    const res = await reload('r-parse');
    assert.equal(res.code, 1);
    assert.equal(res.out, '');
    assert.equal(res.err, 'run reload: the config could not be parsed (.code-forge.yml:4) — nothing changed\n');
    assert.deepEqual(await readRun('r-parse'), before);
  });
});

test('`run reload` refuses when the review queue cannot be read; nothing changed', async () => {
  await withFixture(async ({ ws }) => {
    await started(ws, 'r-queue');
    const before = await readRun('r-queue');
    await writeFile(path.join(ws, '.code-forge', 'queue'), 'not a directory\n'); // readdir ⇒ ENOTDIR
    await writeConfig(ws, CHANGED_YAML);
    const res = await reload('r-queue');
    assert.equal(res.code, 1);
    assert.equal(res.out, '');
    assert.equal(res.err, 'run reload: cannot read the review queue; nothing changed\n');
    assert.deepEqual(await readRun('r-queue'), before);
    assert.deepEqual((await readAllRows('two-blocks')).map((r) => r.event), ['run.start']);
  });
});

test('reloadRun: pins only the pending tickets to the old snapshot, keeps an earlier pin, drops pins no longer pending', async () => {
  await withFixture(async ({ ws }) => {
    const a = { version: 1, provider: 'anthropic', review: { session_timeout_s: 10 } };
    const b = { ...a, review: { session_timeout_s: 20 } };
    const c = { ...a, review: { session_timeout_s: 30 } };
    const { rows, writeRow } = rowSink();
    await startRun({ workspace: ws, project: 'two-blocks', config: a, runId: 'r-pin', writeRow });
    const t1 = 'a'.repeat(24);
    const t2 = 'b'.repeat(24);

    assert.deepEqual(await reloadRun({ runId: 'r-pin', config: b, readPending: () => [t1], writeRow }), {
      changed: ['review.session_timeout_s'],
      oldHash: configHash(a),
      newHash: configHash(b),
      pinned: 1,
      rowError: null,
    });
    await reloadRun({ runId: 'r-pin', config: c, readPending: () => [t1, t2], writeRow });
    let state = (await readRun('r-pin')).config;
    assert.deepEqual(state.pins, { [t1]: configHash(a), [t2]: configHash(b) });
    assert.deepEqual(Object.keys(state.snapshots).sort(), [configHash(a), configHash(b), configHash(c)].sort());

    await reloadRun({ runId: 'r-pin', config: a, readPending: () => [t2], writeRow });
    state = (await readRun('r-pin')).config;
    assert.deepEqual(state.pins, { [t2]: configHash(b) });
    assert.deepEqual(Object.keys(state.snapshots).sort(), [configHash(a), configHash(b)].sort());
    assert.deepEqual(rows.map((r) => r.event), ['run.start', 'run.reload', 'run.reload', 'run.reload']);
  });
});

test('reloadRun: a pin whose snapshot went missing is re-pinned to the snapshot in force, never left dangling', async () => {
  await withFixture(async ({ ws }) => {
    const a = { version: 1, review: { session_timeout_s: 10 } };
    const b = { version: 1, review: { session_timeout_s: 20 } };
    const c = { version: 1, review: { session_timeout_s: 30 } };
    const { writeRow } = rowSink();
    const t1 = 'c'.repeat(24);
    await startRun({ workspace: ws, project: 'two-blocks', config: a, runId: 'r-dangle', writeRow });
    await reloadRun({ runId: 'r-dangle', config: b, readPending: () => [t1], writeRow });
    const record = await readRun('r-dangle');
    delete record.config.snapshots[configHash(a)]; // t1's pinned snapshot is gone
    await saveRun(record);

    await reloadRun({ runId: 'r-dangle', config: c, readPending: () => [t1], writeRow });
    const state = (await readRun('r-dangle')).config;
    assert.deepEqual(state.pins, { [t1]: configHash(b) });
    assert.deepEqual(Object.keys(state.snapshots).sort(), [configHash(b), configHash(c)].sort());
    assert.equal(
      Object.values(state.pins).filter((h) => !Object.hasOwn(state.snapshots, h)).length,
      0,
    );
  });
});

test('reloadRun saves the record before the row; a row that cannot be written is reported, the reload stands', async () => {
  await withFixture(async ({ ws }) => {
    const a = { version: 1, review: { session_timeout_s: 10 } };
    const b = { version: 1, review: { session_timeout_s: 20 } };
    await startRun({ workspace: ws, project: 'two-blocks', config: a, runId: 'r-row', writeRow: async () => {} });
    const failing = async () => {
      throw Object.assign(new Error('disk full'), { code: 'ENOSPC' });
    };
    const res = await reloadRun({ runId: 'r-row', config: b, writeRow: failing });
    assert.equal(res.rowError, 'ENOSPC');
    assert.equal((await readRun('r-row')).config.hash, configHash(b));
  });
});

test('changedKeyPaths: nested, added, removed and list keys, one path each; own properties only', () => {
  const before = { a: { b: 1, c: [1, 2] }, d: 'x', keys: { 'jev.alt': 'env:A' } };
  const after = { a: { b: 2, c: [2, 1] }, e: { f: 1 }, keys: { 'jev.alt': 'env:B' } };
  assert.deepEqual(changedKeyPaths(before, after), ['a.b', 'a.c', 'd', 'e', 'keys["jev.alt"]']);
  assert.deepEqual(changedKeyPaths(before, structuredClone(before)), []);
  const inherited = Object.create({ engine: 'solo' });
  assert.deepEqual(immutableChanges(inherited, {}), []);
  assert.deepEqual(immutableChanges({ engine: 'solo' }, {}), ['engine']);
});

test('a YAML timestamp (a Date) hashes the same after the run record round trip', async () => {
  await withFixture(async ({ ws }) => {
    const cfg = parseYAML('version: 1\nreview:\n  session_timeout_s: 10\nnote: 2026-10-01T10:00:00Z\n', { schema: 'yaml-1.1' });
    assert.equal(cfg.note instanceof Date, true);
    await startRun({ workspace: ws, project: 'two-blocks', config: cfg, runId: 'r-date', writeRow: async () => {} });
    const record = await readRun('r-date');
    assert.equal(record.config.hash, configHash(cfg));
    assert.deepEqual(snapshotFor(record, 'd'.repeat(24))?.hash, configHash(cfg));
    assert.equal(configHash(cfg), configHash({ ...cfg, note: '2026-10-01T10:00:00.000Z' }));
  });
});
