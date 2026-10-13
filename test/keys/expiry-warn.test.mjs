/**
 * B85 (issue #14 part 2): `keys list` and `doctor` warn when a key from 1Password has less than
 * 1 hour left, and `jev ask` prints one re-link line when the cached key expired and 1Password
 * did not answer. Every 1Password read is a fake `opRead`; every store is a temp store (the
 * helper points HOME at a temp dir before any `src` module loads).
 */

import { countOccurrences, FAKE_OP_KEY, memoryBackend, sink, tempHome } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { afterEach, test } from 'node:test';
import { expiryWarning, runKeys } from '../../src/cli/keys.mjs';
import { runJev } from '../../src/cli/jev.mjs';
import { checkKeys } from '../../src/doctor/local.mjs';
import { createKeyStore, encodeEntry } from '../../src/keys/store.mjs';
import { clearSecrets } from '../../src/util/redact.mjs';

const NOW = 1_800_000_000_000;
const MIN = 60_000;
const HOUR = 3_600_000;
const REF = 'op://Private/jev/credential';
const iso = (/** @type {number} */ ms) => new Date(ms).toISOString();
const LOCKED = '1Password is locked or not signed in; unlock the app (or run `op signin`) and try again';

afterEach(() => clearSecrets());

/** A fake 1Password reader that counts its calls; `fail` answers like a locked 1Password. */
function fakeOp({ fail = false } = {}) {
  /** @type {string[]} */
  const refs = [];
  const opRead = async (/** @type {string} */ ref) => {
    refs.push(ref);
    return fail ? { value: null, error: LOCKED, kind: 'op_locked', attempts: 1 } : { value: FAKE_OP_KEY, attempts: 1 };
  };
  return { opRead, refs };
}

/** An index writer that always fails, so only the index WRITE breaks (reads still work). */
const failingWrite = async () => {
  throw Object.assign(new Error('fake write failure'), { code: 'EACCES' });
};

/**
 * A temp store with a cached jev entry and its index row.
 * @param {number} exp
 * @param {{ref?: string}} [row] - extra index fields (none = a row written by 0.9.0)
 * @param {{writeIndexFile?: () => Promise<void>}} [opts]
 */
async function cachedStore(exp, row = {}, opts = {}) {
  const backend = memoryBackend('keychain', { data: { jev: encodeEntry(FAKE_OP_KEY, exp) } });
  const dir = await tempHome();
  await writeFile(path.join(dir, 'index.json'), JSON.stringify({ jev: { source: 'op', backend: 'keychain', exp, ...row } }));
  const store = await createKeyStore({ backends: [backend], dir, ...opts });
  return { backend, store, dir };
}

/** @param {import('../../src/keys/store.mjs').KeyStore} store */
async function list(store) {
  const stdout = sink();
  const stderr = sink();
  const code = await runKeys(['list'], { store, stdout, stderr, env: {}, now: NOW, cwd: await tempHome() });
  return { code, out: stdout.text, err: stderr.text };
}

// ---- (1) keys list: a WARN line under 1 hour, none above ----

test('(1) keys list with 40 min left: stdout unchanged, one stderr WARN with the stored op:// reference', async () => {
  const { store } = await cachedStore(NOW + 40 * MIN, { ref: REF });
  const res = await list(store);
  assert.equal(res.code, 0);
  assert.deepEqual(res.out.trimEnd().split('\n'), ['NAME\tSOURCE\tBACKEND\tEXPIRES', `jev\top\tkeychain\t${iso(NOW + 40 * MIN)}`]);
  assert.equal(res.err, `WARN jev expires in 40 min; re-link: code-forge keys set jev --op ${REF}\n`);
  assert.equal(countOccurrences(res.out + res.err, FAKE_OP_KEY), 0);
});

test('(1) keys list with 2 h left, or a key that never expires, or one already expired: no WARN line', async () => {
  for (const exp of [NOW + 2 * HOUR, NOW + 60 * MIN, null, NOW - MIN]) {
    const { store } = await cachedStore(/** @type {number} */ (exp), { ref: REF });
    const res = await list(store);
    assert.equal(res.code, 0, String(exp));
    assert.equal(res.out.trimEnd().split('\n').length, 2, String(exp));
    assert.equal(res.err, '', String(exp));
  }
});

test('(1) keys list minutes: 59 min 59 s ⇒ 59, 30 s ⇒ 1, exactly 1 h ⇒ no WARN', async () => {
  const cases = [
    [59 * MIN + 59_000, `WARN jev expires in 59 min; re-link: code-forge keys set jev --op ${REF}\n`],
    [30_000, `WARN jev expires in 1 min; re-link: code-forge keys set jev --op ${REF}\n`],
    [HOUR, ''],
  ];
  for (const [left, expected] of cases) {
    const { store } = await cachedStore(NOW + /** @type {number} */ (left), { ref: REF });
    assert.equal((await list(store)).err, expected, String(left));
  }
  assert.equal(cases.length, 3);
});

/** Refs a shell would split, with the single word the re-link line must show. */
const SPACED = "op://Private Vault/Jev API/credential";
const SPACED_WORD = "'op://Private Vault/Jev API/credential'";
const QUOTED = "op://Private/Jev's key/credential";
const QUOTED_WORD = "'op://Private/Jev'\\''s key/credential'";

test('(1) keys list quotes a ref with a space or a single quote as one shell word', async () => {
  for (const [ref, word] of [[SPACED, SPACED_WORD], [QUOTED, QUOTED_WORD]]) {
    const { store } = await cachedStore(NOW + 40 * MIN, { ref });
    assert.equal((await list(store)).err, `WARN jev expires in 40 min; re-link: code-forge keys set jev --op ${word}\n`, ref);
  }
});

test('(1) an exp that is not a valid time (NaN, undefined, Infinity, 1e16) gives no WARN line', async () => {
  const bad = [NaN, undefined, Infinity, 1e16];
  for (const exp of bad) {
    assert.equal(expiryWarning({ name: 'jev', exp: /** @type {any} */ (exp), ref: REF }, NOW), null, String(exp));
  }
  assert.equal(bad.length, 4);
  // end to end: an index row with exp 1e16 lists as unknown, with no WARN and no crash
  const { store } = await cachedStore(1e16, { ref: REF });
  const res = await list(store);
  assert.equal(res.code, 0);
  assert.deepEqual(res.out.trimEnd().split('\n'), ['NAME\tSOURCE\tBACKEND\tEXPIRES', 'jev\top\tkeychain\tunknown']);
  assert.equal(res.err, '');
});

// ---- (2) an index row from 0.9.0 (no ref) ----

test('(2) an index row with no ref lists and warns with the placeholder reference', async () => {
  const { store } = await cachedStore(NOW + 40 * MIN);
  const res = await list(store);
  assert.equal(res.code, 0);
  assert.deepEqual(res.out.trimEnd().split('\n'), ['NAME\tSOURCE\tBACKEND\tEXPIRES', `jev\top\tkeychain\t${iso(NOW + 40 * MIN)}`]);
  assert.equal(res.err, 'WARN jev expires in 40 min; re-link: code-forge keys set jev --op <your 1Password reference>\n');
});

// ---- (3) doctor ----

test('(3) checkKeys with 40 min left: a WARN row with the fixed re-link text and no reference', async () => {
  const { store } = await cachedStore(NOW + 40 * MIN, { ref: REF });
  const op = fakeOp();
  const res = await checkKeys({ keys: { jev: REF } }, { env: {}, store, opRead: op.opRead, now: NOW });
  assert.equal(res.key, FAKE_OP_KEY);
  assert.deepEqual(op.refs, []);
  assert.equal(res.rows.length, 1);
  assert.deepEqual([res.rows[0].status, res.rows[0].detail], ['WARN', 'jev key from keychain expires in 40 min; re-link with code-forge keys set jev --op <ref from keys.jev>']);
  assert.equal(countOccurrences(JSON.stringify(res.rows), 'op://'), 0);
});

test('(3) checkKeys minutes: 59 min 59 s ⇒ 59, 30 s ⇒ 1, exactly 1 h ⇒ OK', async () => {
  const fixed = 'min; re-link with code-forge keys set jev --op <ref from keys.jev>';
  const cases = [
    [59 * MIN + 59_000, 'WARN', `jev key from keychain expires in 59 ${fixed}`],
    [30_000, 'WARN', `jev key from keychain expires in 1 ${fixed}`],
    [HOUR, 'OK', `jev key from keychain, expires ${iso(NOW + HOUR)}`],
  ];
  for (const [left, status, detail] of cases) {
    const { store } = await cachedStore(NOW + /** @type {number} */ (left), { ref: REF });
    const res = await checkKeys({ keys: { jev: REF } }, { env: {}, store, opRead: fakeOp().opRead, now: NOW });
    assert.deepEqual(res.rows.map((r) => [r.status, r.detail]), [[status, detail]], String(left));
  }
  assert.equal(cases.length, 3);
});

test('(3) checkKeys with an exp that is not a valid time (1e16, NaN): the OK row, no crash, no FAIL', async () => {
  for (const exp of [1e16, NaN]) {
    const fake = /** @type {any} */ ({ warning: null, read: async () => ({ value: FAKE_OP_KEY, exp, backend: 'keychain' }), put: async () => {} });
    const res = await checkKeys({ keys: { jev: REF } }, { env: {}, store: fake, opRead: fakeOp().opRead, now: NOW });
    assert.equal(res.key, FAKE_OP_KEY, String(exp));
    assert.deepEqual(res.rows.map((r) => [r.status, r.detail]), [['OK', 'jev key from keychain, expiry unknown']], String(exp));
  }
});

test('(3) checkKeys with 2 h left and no keys.cache_hours: exactly the 0.9.0 OK row', async () => {
  const { store } = await cachedStore(NOW + 2 * HOUR, { ref: REF });
  const res = await checkKeys({ keys: { jev: REF } }, { env: {}, store, opRead: fakeOp().opRead, now: NOW });
  assert.equal(res.rows.length, 1);
  assert.deepEqual([res.rows[0].status, res.rows[0].detail], ['OK', `jev key from keychain, expires ${iso(NOW + 2 * HOUR)}`]);
});

test('(3) checkKeys with 2 h left and keys.cache_hours: 24: the OK text ends (cache 24 h)', async () => {
  const { store } = await cachedStore(NOW + 2 * HOUR, { ref: REF });
  const res = await checkKeys({ keys: { jev: REF, cache_hours: 24 } }, { env: {}, store, opRead: fakeOp().opRead, now: NOW });
  assert.equal(res.rows.length, 1);
  assert.deepEqual([res.rows[0].status, res.rows[0].detail], ['OK', `jev key from keychain, expires ${iso(NOW + 2 * HOUR)} (cache 24 h)`]);
});

// ---- (4) and (5) jev ask ----

/**
 * @param {import('../../src/keys/store.mjs').KeyStore} store
 * @param {{opRead: any}} op
 * @param {Record<string, any>} config
 */
async function ask(store, op, config) {
  const dir = await tempHome();
  await writeFile(path.join(dir, 'state.json'), '{}');
  const stdout = sink();
  const stderr = sink();
  let asked = 0;
  /** @type {string[]} */
  const keys = [];
  const code = await runJev(['ask', 'lane', '--state', 'state.json', '--cwd', dir, '--slug', 'demo'], {
    store,
    opRead: op.opRead,
    loadConfig: async () => ({ ok: true, path: 'x', config }),
    stdout,
    stderr,
    env: {},
    now: () => NOW,
    makeId: () => 'decision-1',
    appendRow: async (/** @type {any} */ row) => row,
    askJev: async (/** @type {any} */ args) => {
      asked += 1;
      keys.push(args.key);
      return { ok: false, kind: 'network', status: null, requestId: null, attempts: 1 };
    },
  });
  return { code, out: stdout.text, err: stderr.text, asked, keys };
}

const GENERIC = 'jev ask: no Jev key resolved (set one with "code-forge keys set jev")\n';

test('(4) jev ask with an expired cached key and a failing 1Password: exit 1, the reason, then the re-link line', async () => {
  const expiredAt = NOW - 5 * MIN;
  const { store } = await cachedStore(expiredAt, { ref: REF });
  const op = fakeOp({ fail: true });
  const res = await ask(store, op, { keys: { jev: REF } });
  assert.equal(res.code, 1);
  assert.deepEqual([op.refs, res.asked, res.out], [[REF], 0, '']);
  assert.deepEqual(res.err.split('\n'), [
    `jev ask: ${LOCKED}`,
    `jev ask: the cached jev key expired at ${iso(expiredAt)} and 1Password did not answer; re-link: code-forge keys set jev --op ${REF}`,
    '',
  ]);
  assert.equal(countOccurrences(res.err, FAKE_OP_KEY), 0);
});

test('(4) jev ask quotes a ref with a space or a single quote as one shell word', async () => {
  for (const [ref, word] of [[SPACED, SPACED_WORD], [QUOTED, QUOTED_WORD]]) {
    const { store } = await cachedStore(NOW - 5 * MIN);
    const op = fakeOp({ fail: true });
    const res = await ask(store, op, { keys: { jev: ref } });
    assert.equal(res.code, 1, ref);
    assert.deepEqual(op.refs, [ref]);
    assert.equal(
      res.err,
      `jev ask: ${LOCKED}\njev ask: the cached jev key expired at ${iso(NOW - 5 * MIN)} and 1Password did not answer; re-link: code-forge keys set jev --op ${word}\n`,
      ref,
    );
  }
});

test('(4) the opposite: the cache expired and 1Password answers ⇒ no re-link line, the fresh key is used', async () => {
  const { store } = await cachedStore(NOW - 5 * MIN, { ref: REF });
  const op = fakeOp();
  const res = await ask(store, op, { keys: { jev: REF } });
  assert.deepEqual([op.refs, res.asked], [[REF], 1]);
  // askJev is a failing double, so the run ends on the request failure, not on the key
  assert.equal(res.code, 1);
  assert.equal(res.err, 'jev ask: request failed (network)\n');
  assert.deepEqual(res.keys, [FAKE_OP_KEY]);
});

test('(4) an expired time that is not a valid time (NaN, Infinity, 1e16) gives the generic line, no crash', async () => {
  for (const bad of [NaN, Infinity, 1e16]) {
    const fake = /** @type {any} */ ({
      warning: null,
      async read(/** @type {string} */ _name, /** @type {number} */ _now, /** @type {string[]} */ _errors, /** @type {any} */ opts) {
        opts?.onExpired?.(bad);
        return null;
      },
      async put() {},
    });
    const op = fakeOp({ fail: true });
    const res = await ask(fake, op, { keys: { jev: REF } });
    assert.equal(res.code, 1, String(bad));
    assert.equal(res.err, `jev ask: ${LOCKED}\n${GENERIC}`, String(bad));
  }
});

test('(4) store.read reports an expired entry when the index write fails: the reason goes to errors, no reject', async () => {
  // only the index write fails; the index read works
  const { store, backend } = await cachedStore(NOW - 5 * MIN, { ref: REF }, { writeIndexFile: failingWrite });
  /** @type {number[]} */
  const seen = [];
  /** @type {string[]} */
  const errors = [];
  assert.equal(await store.read('jev', NOW, errors, { onExpired: (exp) => seen.push(exp) }), null);
  assert.deepEqual(seen, [NOW - 5 * MIN]);
  assert.deepEqual(errors, ['index: removal of the expired entry failed (EACCES)']);
  assert.equal(backend.store.size, 0);
});

test('(4) end to end: the index write fails and 1Password fails ⇒ jev ask still prints the re-link line, exit 1', async () => {
  const { store } = await cachedStore(NOW - 5 * MIN, { ref: REF }, { writeIndexFile: failingWrite });
  const op = fakeOp({ fail: true });
  const res = await ask(store, op, { keys: { jev: REF } });
  assert.equal(res.code, 1);
  assert.deepEqual([op.refs, res.asked], [[REF], 0]);
  assert.deepEqual(res.err.split('\n'), [
    'jev ask: index: removal of the expired entry failed (EACCES)',
    `jev ask: ${LOCKED}`,
    `jev ask: the cached jev key expired at ${iso(NOW - 5 * MIN)} and 1Password did not answer; re-link: code-forge keys set jev --op ${REF}`,
    '',
  ]);
  assert.equal(countOccurrences(res.err, FAKE_OP_KEY), 0);
});

test('(5) jev ask with no cached entry and a failing 1Password: the generic line', async () => {
  const backend = memoryBackend('keychain');
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  const op = fakeOp({ fail: true });
  const res = await ask(store, op, { keys: { jev: REF } });
  assert.equal(res.code, 1);
  assert.deepEqual(op.refs, [REF]);
  assert.equal(res.err, `jev ask: ${LOCKED}\n${GENERIC}`);
});

test('(5) jev ask with an expired cached key and a non-op reference: the generic line', async () => {
  const { store } = await cachedStore(NOW - 5 * MIN, { ref: REF });
  const op = fakeOp({ fail: true });
  const res = await ask(store, op, { keys: { jev: 'env:FAKE_JEV_KEY' } });
  assert.equal(res.code, 1);
  assert.deepEqual(op.refs, []);
  assert.equal(res.err, GENERIC);
});

// ---- (6) the index holds the reference, never the value ----

test('(6) after keys set --op, index.json holds the op:// reference and not the key', async () => {
  const backend = memoryBackend('keychain');
  const dir = await tempHome();
  const store = await createKeyStore({ backends: [backend], dir });
  const op = fakeOp();
  const code = await runKeys(['set', 'jev', '--op', REF], { store, stdout: sink(), stderr: sink(), env: {}, now: NOW, cwd: await tempHome(), opRead: op.opRead });
  assert.equal(code, 0);
  const text = await readFile(path.join(dir, 'index.json'), 'utf8');
  assert.deepEqual(JSON.parse(text), { jev: { source: 'op', backend: 'keychain', exp: NOW + 8 * HOUR, ref: REF } });
  assert.deepEqual([countOccurrences(text, REF), countOccurrences(text, FAKE_OP_KEY)], [1, 0]);
});
