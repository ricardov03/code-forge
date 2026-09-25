// helpers FIRST: its import-time guard moves $HOME and cwd to a temp dir before any src module loads.
import { countOccurrences, withFixture } from './helpers.mjs';
import assert from 'node:assert/strict';
import { chmod, mkdir, readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { canonicalJSON, generateKey, loadKey, signRow, verifyRow, verifyRows } from '../../src/state/signer.mjs';

const ROW = { event: 'review.approved', run: 'r-sig', block: 'B8', path: 'src/a.mjs', content_hash: 'abc123', ts: '2026-09-24T20:00:00.000Z' };

test('generateKey writes 32 bytes, mode 0600, under the temp $HOME runs dir — and refuses to overwrite', async () => {
  await withFixture(async ({ home }) => {
    const key = await generateKey('r-sig');
    const file = path.join(home, '.code-forge', 'runs', 'r-sig.key');
    assert.equal((await stat(file)).mode & 0o777, 0o600);
    assert.equal((await stat(path.dirname(file))).mode & 0o777, 0o700);
    assert.deepEqual(await readFile(file), key);
    assert.equal(key.length, 32);
    assert.deepEqual(await loadKey('r-sig'), key);
    await assert.rejects(() => generateKey('r-sig'), { code: 'key-exists' });
    assert.deepEqual(await readFile(file), key);
  });
});

test('generateKey tightens a pre-existing loose runs dir to 0700; loadKey refuses a group/world-readable key', async () => {
  await withFixture(async ({ home }) => {
    const runs = path.join(home, '.code-forge', 'runs');
    await mkdir(runs, { recursive: true, mode: 0o755 });
    await chmod(runs, 0o755);
    await generateKey('r-mode');
    assert.equal((await stat(runs)).mode & 0o777, 0o700);
    await chmod(path.join(runs, 'r-mode.key'), 0o644);
    await assert.rejects(() => loadKey('r-mode'), { code: 'bad-key-mode' });
  });
});

test('verifyRow is strict about the mac field (junk suffix, uppercase) and signs the JSON form of a Date', async () => {
  await withFixture(async () => {
    const key = await generateKey('r-sig');
    const valid = signRow(ROW, key);
    assert.deepEqual(verifyRow({ ...valid, mac: `${valid.mac}zz` }, key), { ok: false, reason: 'mismatch' });
    assert.deepEqual(verifyRow({ ...valid, mac: valid.mac.toUpperCase() }, key), { ok: false, reason: 'mismatch' });
    const when = new Date('2026-09-24T20:00:00.000Z');
    const dated = signRow({ event: 'x', at: when }, key);
    assert.deepEqual(verifyRow(JSON.parse(JSON.stringify(dated)), key), { ok: true });
    assert.deepEqual(verifyRow({ ...JSON.parse(JSON.stringify(dated)), at: '2026-09-25T00:00:00.000Z' }, key), { ok: false, reason: 'mismatch' });
  });
});

test('signer: 1 valid row verifies, 1 forged row is refused, 1 unsigned row is refused', async () => {
  await withFixture(async () => {
    const key = await generateKey('r-sig');
    const valid = signRow(ROW, key);
    const forged = { ...valid, content_hash: 'def456' };
    const unsigned = { ...ROW };
    assert.deepEqual(verifyRow(valid, key), { ok: true });
    assert.deepEqual(verifyRow(forged, key), { ok: false, reason: 'mismatch' });
    assert.deepEqual(verifyRow(unsigned, key), { ok: false, reason: 'unsigned' });
    const result = verifyRows([valid, forged, unsigned], key);
    assert.deepEqual(result, {
      ok: false,
      failures: [
        { index: 1, event: 'review.approved', reason: 'mismatch' },
        { index: 2, event: 'review.approved', reason: 'unsigned' },
      ],
    });
  });
});

test('a MAC minted with another key, or a truncated MAC, is a mismatch', async () => {
  await withFixture(async () => {
    const key = await generateKey('r-sig');
    const other = Buffer.alloc(32, 7);
    assert.deepEqual(verifyRow(signRow(ROW, other), key), { ok: false, reason: 'mismatch' });
    const valid = signRow(ROW, key);
    assert.deepEqual(verifyRow({ ...valid, mac: valid.mac.slice(0, 32) }, key), { ok: false, reason: 'mismatch' });
  });
});

test('canonical form ignores key order at every depth, and signRow stamps ts before signing', async () => {
  assert.equal(canonicalJSON({ b: 1, a: { d: [2, { z: 1, y: 2 }], c: null } }), '{"a":{"c":null,"d":[2,{"y":2,"z":1}]},"b":1}');
  await withFixture(async () => {
    const key = await generateKey('r-sig');
    const reordered = signRow({ ts: ROW.ts, content_hash: 'abc123', path: 'src/a.mjs', block: 'B8', run: 'r-sig', event: 'review.approved' }, key);
    assert.equal(reordered.mac, signRow(ROW, key).mac);
    const { ts, ...noTs } = ROW;
    const stamped = signRow(noTs, key);
    assert.match(stamped.ts, /^\d{4}-\d\d-\d\dT/);
    assert.deepEqual(verifyRow(stamped, key), { ok: true });
    assert.deepEqual(verifyRow({ ...stamped, ts: ts }, key), { ok: false, reason: 'mismatch' });
  });
});

test('a signed row survives the real B6 ledger writer (tokens_source/cost_source added after signing) and the key never reaches the ledger', async () => {
  await withFixture(async ({ home }) => {
    const key = await generateKey('r-sig');
    await appendRow(signRow({ ...ROW, tokens_in: 900, cost_usd: 0.25 }, key), { slug: 'sig' });
    const [row] = await readAllRows('sig');
    assert.equal(row.tokens_source, 'estimated');
    assert.equal(row.cost_source, 'estimated');
    assert.deepEqual(verifyRow(row, key), { ok: true });
    // exactly tokens_source / cost_source are outside the MAC; every other field is inside
    assert.deepEqual(verifyRow({ ...row, tokens_source: 'reported', cost_source: 'reported' }, key), { ok: true });
    assert.deepEqual(verifyRow({ ...row, tokens_in: 1 }, key), { ok: false, reason: 'mismatch' });
    assert.deepEqual(verifyRow({ ...row, cost_usd: 0 }, key), { ok: false, reason: 'mismatch' });
    assert.deepEqual(verifyRow({ ...row, ts: '2020-01-01T00:00:00.000Z' }, key), { ok: false, reason: 'mismatch' });
    const text = await readFile(path.join(home, '.code-forge', 'ledger', 'sig.jsonl'), 'utf8');
    assert.equal(countOccurrences(text, key.toString('hex')), 0);
    assert.equal(countOccurrences(text, key.toString('base64')), 0);
  });
});
