import {
  countOccurrences,
  FAKE_ASK_KEY,
  FAKE_ENV_KEY,
  FAKE_KEY,
  FAKE_OP_KEY,
  memoryBackend,
  REAL_HOME,
  tempHome,
} from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFile, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { afterEach, test } from 'node:test';
import {
  assertKeyName,
  codeForgeHome,
  createDefaultKeyStore,
  createKeyStore,
  decodeEntry,
  encodeEntry,
  FILE_FALLBACK_WARNING,
  parseRef,
  resolveKey,
} from '../../src/keys/store.mjs';
import { createFileBackend } from '../../src/keys/backends/file.mjs';
import { clearSecrets, redact } from '../../src/util/redact.mjs';

const NOW = 1_800_000_000_000;
const HOUR = 3_600_000;
const REF = 'op://Private/jev/credential';

afterEach(() => clearSecrets());

/**
 * Four mocked sources — env, keychain, 1Password, ask — sharing one call log.
 * @param {{env?: boolean, keychain?: boolean, op?: boolean, ask?: boolean}} present
 */
async function chainFixture(present) {
  /** @type {string[]} */
  const calls = [];
  const keychain = memoryBackend('keychain', {
    calls,
    data: present.keychain ? { jev: encodeEntry(FAKE_KEY, null) } : {},
  });
  const dir = await tempHome();
  const store = await createKeyStore({ backends: [keychain], dir });
  const env = present.env ? { CODE_FORGE_KEY_JEV: FAKE_ENV_KEY } : {};
  const opRead = async (/** @type {string} */ ref) => {
    calls.push(`op:${ref}`);
    return present.op ? { value: FAKE_OP_KEY, attempts: 1 } : { value: null, attempts: 1, error: 'op read failed (exit 1)' };
  };
  const ask = async (/** @type {string} */ name) => {
    calls.push(`ask:${name}`);
    return present.ask ? FAKE_ASK_KEY : null;
  };
  const res = await resolveKey('jev', { store, ref: REF, env, opRead, ask, now: NOW });
  return { res, calls, keychain, store };
}

test('chain resolves env → keychain → 1Password → ask, stopping at the first hit', async () => {
  const all = await chainFixture({ env: true, keychain: true, op: true, ask: true });
  assert.deepEqual([all.res.source, all.res.value, all.calls], ['env', FAKE_ENV_KEY, []]);

  const noEnv = await chainFixture({ keychain: true, op: true, ask: true });
  assert.deepEqual([noEnv.res.source, noEnv.res.value, noEnv.calls], ['keychain', FAKE_KEY, ['keychain.get:jev']]);

  const opOnly = await chainFixture({ op: true, ask: true });
  assert.deepEqual(
    [opOnly.res.source, opOnly.res.value, opOnly.calls],
    ['op', FAKE_OP_KEY, ['keychain.get:jev', `op:${REF}`, 'keychain.set:jev']],
  );

  const askOnly = await chainFixture({ ask: true });
  assert.deepEqual(
    [askOnly.res.source, askOnly.res.value, askOnly.calls],
    ['ask', FAKE_ASK_KEY, ['keychain.get:jev', `op:${REF}`, 'ask:jev', 'keychain.set:jev']],
  );
  assert.deepEqual(await askOnly.store.list(), [{ name: 'jev', source: 'user', backend: 'keychain', exp: null }]);
});

test('nothing anywhere resolves to null with the op error reported', async () => {
  const none = await chainFixture({});
  assert.deepEqual([none.res.value, none.res.source, none.res.errors], [null, null, ['op read failed (exit 1)']]);
});

test('without ask the chain never prompts: exactly one store read and one op call, then null', async () => {
  const keychain = memoryBackend('keychain');
  const store = await createKeyStore({ backends: [keychain], dir: await tempHome() });
  /** @type {string[]} */
  const opCalls = [];
  const opRead = async (/** @type {string} */ ref) => {
    opCalls.push(ref);
    return { value: null, attempts: 1, error: 'op read failed (exit 1)' };
  };
  const res = await resolveKey('jev', { store, ref: REF, env: {}, opRead, now: NOW });
  assert.deepEqual([res.value, res.source], [null, null]);
  assert.deepEqual(keychain.calls, ['keychain.get:jev']);
  assert.deepEqual(opCalls, [REF]);
});

test('a 1Password hit is cached as {v, exp} with exp = now + 8 h, and the next read comes from the keychain', async () => {
  const { keychain } = await chainFixture({ op: true });
  assert.deepEqual(JSON.parse(keychain.store.get('jev')), { v: FAKE_OP_KEY, exp: NOW + 8 * HOUR });

  const store = await createKeyStore({ backends: [keychain], dir: await tempHome() });
  let opCalls = 0;
  const opRead = async () => {
    opCalls += 1;
    return { value: 'unused', attempts: 1 };
  };
  const again = await resolveKey('jev', { store, ref: REF, env: {}, opRead, now: NOW + 8 * HOUR - 1 });
  assert.deepEqual([again.source, again.value, again.exp, opCalls], ['keychain', FAKE_OP_KEY, NOW + 8 * HOUR, 0]);
});

test('cacheHours overrides the 8 h default', async () => {
  const store = await createKeyStore({ backends: [memoryBackend('keychain')], dir: await tempHome() });
  const opRead = async () => ({ value: FAKE_OP_KEY, attempts: 1 });
  const res = await resolveKey('jev', { store, ref: REF, env: {}, opRead, now: NOW, cacheHours: 2 });
  assert.equal(res.exp, NOW + 2 * HOUR);
});

test('an expired {v, exp} entry reads as none and the backend delete is called exactly once', async () => {
  const keychain = memoryBackend('keychain', { data: { jev: encodeEntry(FAKE_KEY, NOW - 1) } });
  const store = await createKeyStore({ backends: [keychain], dir: await tempHome() });

  const entry = await store.read('jev', NOW);

  assert.equal(entry, null);
  assert.equal(keychain.calls.filter((c) => c === 'keychain.delete:jev').length, 1);
  assert.equal(keychain.store.has('jev'), false);
});

test('an expired entry whose delete throws still reads as none, delete tried once, reason recorded', async () => {
  const keychain = memoryBackend('keychain', { data: { jev: encodeEntry(FAKE_KEY, NOW - 1) } });
  keychain.delete = async (/** @type {string} */ key) => {
    keychain.calls.push(`keychain.delete:${key}`);
    throw Object.assign(new Error(`denied ${FAKE_KEY}`), { code: 'EACCES' });
  };
  const store = await createKeyStore({ backends: [keychain], dir: await tempHome() });
  /** @type {string[]} */
  const errors = [];
  assert.equal(await store.read('jev', NOW, errors), null);
  assert.deepEqual(keychain.calls, ['keychain.get:jev', 'keychain.delete:jev']);
  assert.deepEqual(errors, ['keychain: delete of expired entry failed (EACCES)']);
});

test('an entry expiring exactly now is expired; one expiring a millisecond later is not', async () => {
  const keychain = memoryBackend('keychain', { data: { a: encodeEntry(FAKE_KEY, NOW), b: encodeEntry(FAKE_KEY, NOW + 1) } });
  const store = await createKeyStore({ backends: [keychain], dir: await tempHome() });
  assert.equal(await store.read('a', NOW), null);
  assert.deepEqual(await store.read('b', NOW), { value: FAKE_KEY, exp: NOW + 1, backend: 'keychain' });
  assert.deepEqual(keychain.calls.filter((c) => c.includes('delete')), ['keychain.delete:a']);
});

test('an expired cache falls through to 1Password and is refreshed', async () => {
  const keychain = memoryBackend('keychain', { data: { jev: encodeEntry('FAKE-stale-value-000', NOW - HOUR) } });
  const store = await createKeyStore({ backends: [keychain], dir: await tempHome() });
  const opRead = async () => ({ value: FAKE_OP_KEY, attempts: 1 });
  const res = await resolveKey('jev', { store, ref: REF, env: {}, opRead, now: NOW });
  assert.deepEqual([res.source, res.value], ['op', FAKE_OP_KEY]);
  assert.deepEqual(keychain.calls, ['keychain.get:jev', 'keychain.delete:jev', 'keychain.set:jev']);
});

test('a backend whose read throws is skipped: the next backend supplies the value, the reason names no value', async () => {
  const broken = memoryBackend('keychain');
  broken.get = async () => {
    throw Object.assign(new Error(`locked while reading ${FAKE_OP_KEY}`), { code: 'ELOCKED' });
  };
  const second = memoryBackend('security-cli', { writable: false, data: { jev: encodeEntry(FAKE_KEY, null) } });
  const store = await createKeyStore({ backends: [broken, second], dir: await tempHome() });
  const res = await resolveKey('jev', { store, env: {}, now: NOW });
  assert.deepEqual([res.value, res.source, res.errors], [FAKE_KEY, 'security-cli', ['keychain: read failed (ELOCKED)']]);
});

test('a failed read with no code is reported by error class', async () => {
  const broken = memoryBackend('keychain');
  broken.get = async () => {
    throw new TypeError('x');
  };
  const store = await createKeyStore({ backends: [broken], dir: await tempHome() });
  /** @type {string[]} */
  const errors = [];
  assert.equal(await store.read('jev', NOW, errors), null);
  assert.deepEqual(errors, ['keychain: read failed (TypeError)']);
});

test('the writer is read first: a fresh cached value beats a stale copy in a read-only backend', async () => {
  const stale = memoryBackend('security-cli', { writable: false, data: { jev: encodeEntry('FAKE-stale-value-111', null) } });
  const writer = memoryBackend('file', { data: { jev: encodeEntry(FAKE_OP_KEY, NOW + HOUR) } });
  const store = await createKeyStore({ backends: [stale, writer], dir: await tempHome() });
  assert.deepEqual(await store.read('jev', NOW), { value: FAKE_OP_KEY, exp: NOW + HOUR, backend: 'file' });
  assert.deepEqual(stale.calls, []);
});

test('every resolved value is registered with redact, whichever source produced it', async () => {
  const cases = [
    [{ env: true }, 'env', FAKE_ENV_KEY],
    [{ keychain: true }, 'keychain', FAKE_KEY],
    [{ op: true }, 'op', FAKE_OP_KEY],
    [{ ask: true }, 'ask', FAKE_ASK_KEY],
  ];
  for (const [present, source, value] of cases) {
    clearSecrets();
    const { res } = await chainFixture(/** @type {any} */ (present));
    assert.deepEqual([res.source, res.value], [source, value]);
    const line = `resolved ${res.value} via ${res.source}`;
    assert.equal(countOccurrences(line, /** @type {string} */ (value)), 1);
    assert.equal(countOccurrences(redact(line), /** @type {string} */ (value)), 0, String(source));
  }
});

test('a cache write failure keeps the 1Password value and reports a value-free reason', async () => {
  const broken = memoryBackend('keychain');
  broken.set = async () => {
    throw new Error(`locked ${FAKE_OP_KEY}`);
  };
  const store = await createKeyStore({ backends: [broken], dir: await tempHome() });
  const res = await resolveKey('jev', { store, ref: REF, env: {}, opRead: async () => ({ value: FAKE_OP_KEY, attempts: 1 }), now: NOW });
  assert.deepEqual([res.value, res.errors], [FAKE_OP_KEY, ['cache write failed (Error)']]);
});

test('an ask whose store write fails keeps the typed value and reports the reason', async () => {
  const store = await createKeyStore({ backends: [memoryBackend('security-cli', { writable: false })], dir: await tempHome() });
  const res = await resolveKey('jev', { store, env: {}, ask: async () => FAKE_ASK_KEY, now: NOW });
  assert.deepEqual([res.value, res.source, res.errors], [FAKE_ASK_KEY, 'ask', ['cache write failed (Error)']]);
});

test('backend selection: first available writable backend is the writer; a read-only one is still read', async () => {
  /** @type {string[]} */
  const calls = [];
  const gone = memoryBackend('keychain', { available: false, calls });
  const readOnly = memoryBackend('security-cli', { writable: false, calls, data: { old: encodeEntry(FAKE_KEY, null) } });
  const dir = path.join(await tempHome(), 'store');
  const store = await createKeyStore({ backends: [gone, readOnly, createFileBackend({ dir })], dir });

  assert.deepEqual(store.backends, ['security-cli', 'file']);
  assert.equal(store.writer, 'file');
  assert.equal(store.warning, FILE_FALLBACK_WARNING);
  assert.deepEqual(await store.read('old', NOW), { value: FAKE_KEY, exp: null, backend: 'security-cli' });
  await store.put('jev', FAKE_OP_KEY, { source: 'user', exp: null });
  assert.deepEqual(await store.read('jev', NOW), { value: FAKE_OP_KEY, exp: null, backend: 'file' });
  assert.equal((await stat(path.join(dir, 'jev.key'))).mode & 0o777, 0o600);
  assert.equal(readOnly.store.has('jev'), false);
  assert.equal(calls.filter((c) => c.startsWith('keychain.') || c.includes('.set:')).length, 0);
});

test('with a keychain available there is no file-fallback warning', async () => {
  const store = await createKeyStore({ backends: [memoryBackend('keychain')], dir: await tempHome() });
  assert.deepEqual([store.warning, store.writer], [null, 'keychain']);
});

test('no writable backend: put refuses', async () => {
  const store = await createKeyStore({ backends: [memoryBackend('security-cli', { writable: false })], dir: await tempHome() });
  assert.equal(store.writer, null);
  await assert.rejects(store.put('jev', FAKE_KEY, { source: 'user', exp: null }), /no writable backend/);
});

test('the index holds name, source, backend and exp — never the value — and is mode 0600', async () => {
  const dir = await tempHome();
  const store = await createKeyStore({ backends: [memoryBackend('keychain')], dir });
  await store.put('jev', FAKE_KEY, { source: 'op', exp: NOW });
  await store.put('alpha', FAKE_OP_KEY, { source: 'user', exp: null });

  const raw = await readFile(path.join(dir, 'index.json'), 'utf8');
  assert.equal(countOccurrences(raw, FAKE_KEY) + countOccurrences(raw, FAKE_OP_KEY), 0);
  assert.equal((await stat(path.join(dir, 'index.json'))).mode & 0o777, 0o600);
  assert.deepEqual(await store.list(), [
    { name: 'alpha', source: 'user', backend: 'keychain', exp: null },
    { name: 'jev', source: 'op', backend: 'keychain', exp: NOW },
  ]);
});

test('a corrupt index reads as empty and the next write rebuilds it', async () => {
  const dir = await tempHome();
  await writeFile(path.join(dir, 'index.json'), '{not json');
  const store = await createKeyStore({ backends: [memoryBackend('keychain')], dir });
  assert.deepEqual(await store.list(), []);
  await store.put('jev', FAKE_KEY, { source: 'user', exp: null });
  assert.deepEqual(await store.list(), [{ name: 'jev', source: 'user', backend: 'keychain', exp: null }]);
});

test('remove deletes from every backend and drops the index row', async () => {
  const a = memoryBackend('keychain', { data: { jev: 'x' } });
  const b = memoryBackend('file', { data: { jev: 'y' } });
  const store = await createKeyStore({ backends: [a, b], dir: await tempHome() });
  await store.put('jev', FAKE_KEY, { source: 'user', exp: null });
  assert.equal(await store.remove('jev'), 2);
  assert.deepEqual([a.store.has('jev'), b.store.has('jev'), await store.list()], [false, false, []]);
  assert.equal(await store.remove('jev'), 0);
});

test('an available() that throws counts as unavailable', async () => {
  const bad = memoryBackend('keychain');
  bad.available = async () => {
    throw new Error('boom');
  };
  const store = await createKeyStore({ backends: [bad, memoryBackend('file')], dir: await tempHome() });
  assert.deepEqual(store.backends, ['file']);
});

test('createDefaultKeyStore: order keychain → security-cli → file, and CODE_FORGE_KEY_BACKEND=file pins the file', async () => {
  const home = await tempHome();
  const factories = {
    keychain: () => memoryBackend('keychain'),
    securityCli: () => memoryBackend('security-cli', { writable: false }),
  };
  const full = await createDefaultKeyStore({ HOME: home }, factories);
  assert.deepEqual([full.backends, full.writer], [['keychain', 'security-cli', 'file'], 'keychain']);
  const pinned = await createDefaultKeyStore({ HOME: home, CODE_FORGE_KEY_BACKEND: 'file' }, factories);
  assert.deepEqual([pinned.backends, pinned.writer], [['file'], 'file']);
  await pinned.put('jev', FAKE_KEY, { source: 'user', exp: null });
  assert.equal((await stat(path.join(home, '.code-forge', 'store', 'jev.key'))).mode & 0o777, 0o600);
});

test('codeForgeHome follows $HOME (the helper points the test process HOME away from the real one)', () => {
  assert.equal(codeForgeHome({ HOME: '/tmp/x' }), path.join('/tmp/x', '.code-forge'));
  assert.notEqual(codeForgeHome({}), path.join(REAL_HOME, '.code-forge'));
});

test('key names are validated: 3 bad names refused, path traversal included', () => {
  for (const bad of ['../etc/passwd', 'Jev', '']) {
    assert.throws(() => assertKeyName(bad), TypeError, bad);
  }
  assert.equal(assertKeyName('jev_2-b'), 'jev_2-b');
});

test('parseRef maps the four reference kinds', () => {
  assert.deepEqual(parseRef('jev', undefined), { envName: 'CODE_FORGE_KEY_JEV', storeName: 'jev', opRef: null });
  assert.deepEqual(parseRef('my-key', 'env:MY_JEV'), { envName: 'MY_JEV', storeName: 'my-key', opRef: null });
  assert.deepEqual(parseRef('jev', 'keychain:other'), { envName: 'CODE_FORGE_KEY_JEV', storeName: 'other', opRef: null });
  assert.deepEqual(parseRef('jev', REF), { envName: 'CODE_FORGE_KEY_JEV', storeName: 'jev', opRef: REF });
  assert.throws(() => parseRef('jev', 'op://nope'), /malformed 1Password reference/);
});

test('an empty env var does not count as a hit', async () => {
  const store = await createKeyStore({ backends: [memoryBackend('keychain', { data: { jev: FAKE_KEY } })], dir: await tempHome() });
  const res = await resolveKey('jev', { store, env: { CODE_FORGE_KEY_JEV: '' }, now: NOW });
  assert.deepEqual([res.source, res.value], ['keychain', FAKE_KEY]);
});

test('decodeEntry: our JSON shape, a raw value, and JSON that is not ours', () => {
  assert.deepEqual(decodeEntry(encodeEntry(FAKE_KEY, 5)), { value: FAKE_KEY, exp: 5 });
  assert.deepEqual(decodeEntry(FAKE_KEY), { value: FAKE_KEY, exp: null });
  assert.deepEqual(decodeEntry('{"x":1}'), { value: '{"x":1}', exp: null });
  assert.deepEqual(decodeEntry('{"v":"a","exp":"soon"}'), { value: 'a', exp: null });
});
