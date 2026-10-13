/**
 * B84 (issue #14): `keys.cache_hours` and `keys set --cache-hours` set how long a key read from
 * 1Password stays cached, and a cached entry is never trusted for longer than the reader's value.
 * Every 1Password read is a fake `opRead`; every store is a temp store (the helper points HOME at
 * a temp dir before any `src` module loads).
 */

import { countOccurrences, FAKE_OP_KEY, memoryBackend, sink, tempHome } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, test } from 'node:test';
import { runKeys } from '../../src/cli/keys.mjs';
import { runJev } from '../../src/cli/jev.mjs';
import { validateConfig } from '../../src/config/validate.mjs';
import { checkKeys } from '../../src/doctor/local.mjs';
import { mergeConfig } from '../../src/install/wizard/project-file.mjs';
import { cacheHoursFrom, createKeyStore, DEFAULT_CACHE_HOURS, encodeEntry, MAX_CACHE_HOURS, MIN_CACHE_HOURS, resolveKey } from '../../src/keys/store.mjs';
import { clearSecrets } from '../../src/util/redact.mjs';
import { resolveJevKey } from '../../src/worker/loop.mjs';

const SCHEMA = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'schema', 'code-forge.schema.json');
const NOW = 1_800_000_000_000;
const HOUR = 3_600_000;
const REF = 'op://Private/jev/credential';
const NOTE = '1Password may ask you to approve access: check the 1Password window (Touch ID or password).\n';
const iso = (/** @type {number} */ ms) => new Date(ms).toISOString();

afterEach(() => clearSecrets());

/** A fake 1Password reader that counts its calls. */
function fakeOp() {
  /** @type {string[]} */
  const refs = [];
  const opRead = async (/** @type {string} */ ref) => {
    refs.push(ref);
    return { value: FAKE_OP_KEY, attempts: 1 };
  };
  return { opRead, refs };
}

/**
 * A temp store, a temp project dir (with `.code-forge.yml` when `yml` is given), and a runner.
 * @param {{yml?: string, data?: Record<string, string>}} [opts]
 */
async function harness({ yml, data = {} } = {}) {
  const backend = memoryBackend('keychain', { data });
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  const cwd = await tempHome();
  if (yml !== undefined) {
    await writeFile(path.join(cwd, '.code-forge.yml'), yml);
  }
  const stdout = sink();
  const stderr = sink();
  const op = fakeOp();
  /** @param {string[]} args */
  const run = (args) => runKeys(args, { store, stdout, stderr, env: {}, now: NOW, cwd, opRead: op.opRead });
  return { backend, store, cwd, stdout, stderr, op, run };
}

/** @param {ReturnType<typeof memoryBackend>} backend */
const storedExp = (backend) => JSON.parse(/** @type {string} */ (backend.store.get('jev'))).exp;

// ---- (1) keys.cache_hours: 24 in the project config ----

test('(1) keys.cache_hours: 24 — keys set --op stores exp = now + 24 h and keys list shows that time', async () => {
  const h = await harness({ yml: 'keys:\n  cache_hours: 24\n' });
  assert.equal(await h.run(['set', 'jev', '--op', REF]), 0);
  assert.deepEqual(h.op.refs, [REF]);
  assert.equal(storedExp(h.backend), NOW + 24 * HOUR);
  assert.equal(h.stdout.text, `stored jev in keychain (source=op, expires=${iso(NOW + 24 * HOUR)}) (cache 24 h from keys.cache_hours)\n`);
  assert.equal(h.stderr.text, NOTE);

  const list = sink();
  assert.equal(await runKeys(['list'], { store: h.store, stdout: list, stderr: sink(), env: {}, now: NOW, cwd: h.cwd }), 0);
  assert.deepEqual(list.text.trimEnd().split('\n'), ['NAME\tSOURCE\tBACKEND\tEXPIRES', `jev\top\tkeychain\t${iso(NOW + 24 * HOUR)}`]);
  assert.equal(countOccurrences(h.stdout.text + h.stderr.text + list.text, FAKE_OP_KEY), 0);
});

// ---- (2) precedence and the unchanged default ----

test('(2) --cache-hours 3 wins over keys.cache_hours 24: exp = now + 3 h (either flag order)', async () => {
  for (const args of [
    ['set', 'jev', '--op', REF, '--cache-hours', '3'],
    ['set', 'jev', '--cache-hours', '3', '--op', REF],
  ]) {
    const h = await harness({ yml: 'keys:\n  cache_hours: 24\n' });
    assert.equal(await h.run(args), 0, JSON.stringify(args));
    assert.equal(storedExp(h.backend), NOW + 3 * HOUR);
    assert.equal(h.stdout.text, `stored jev in keychain (source=op, expires=${iso(NOW + 3 * HOUR)}) (cache 3 h from --cache-hours)\n`);
  }
});

test('(2) no config and no flag: exp = now + 8 h and the stored line is byte-identical to 0.9.0', async () => {
  const h = await harness();
  assert.equal(await h.run(['set', 'jev', '--op', REF]), 0);
  assert.equal(storedExp(h.backend), NOW + 8 * HOUR);
  // the raw entry in the backend is the 0.9.0 shape, byte for byte
  assert.equal(h.backend.store.get('jev'), `{"v":"${FAKE_OP_KEY}","exp":${NOW + 8 * HOUR}}`);
  assert.equal(h.stdout.text, `stored jev in keychain (source=op, expires=${iso(NOW + 8 * HOUR)})\n`);
  assert.equal(h.stderr.text, NOTE);
});

// ---- (3) refusals: exit 2 and no op call ----

test('(3) --cache-hours 0, 25, 1.5 and abc are refused with exit 2, nothing stored, no op call', async () => {
  const values = ['0', '25', '1.5', 'abc'];
  for (const value of values) {
    const h = await harness();
    assert.equal(await h.run(['set', 'jev', '--op', REF, '--cache-hours', value]), 2, value);
    assert.deepEqual([h.op.refs, h.backend.store.size, h.stdout.text], [[], 0, ''], value);
    assert.equal(h.stderr.text, 'keys: --cache-hours must be a whole number of hours from 1 to 24\n', value);
  }
  assert.equal(values.length, 4);
});

test('(3) --cache-hours without --op is a usage error: exit 2, the usage line, no op call, no prompt', async () => {
  const h = await harness();
  let asked = 0;
  const code = await runKeys(['set', 'jev', '--cache-hours', '3'], {
    store: h.store,
    stdout: h.stdout,
    stderr: h.stderr,
    env: {},
    now: NOW,
    cwd: h.cwd,
    opRead: h.op.opRead,
    ask: async () => {
      asked += 1;
      return 'FAKE-typed';
    },
  });
  assert.deepEqual([code, h.op.refs, asked, h.backend.calls], [2, [], 0, []]);
  assert.equal(h.stderr.text, 'usage: code-forge keys list | set <name> [--op <item-id|link|op://ref> [--cache-hours <1-24>]] | test <name> [--ref <ref>] | remove <name>\n');
});

test('(3) an invalid keys.cache_hours in the config: keys set --op exits 2, no op call, nothing stored', async () => {
  const bad = ['0', '25', '1.5', '"24"'];
  for (const value of bad) {
    const h = await harness({ yml: `keys:\n  cache_hours: ${value}\n` });
    assert.equal(await h.run(['set', 'jev', '--op', REF]), 2, value);
    assert.deepEqual([h.op.refs, h.backend.calls, h.stdout.text], [[], [], ''], value);
    assert.equal(h.stderr.text, 'keys: keys.cache_hours must be a whole number of hours from 1 to 24 (.code-forge.yml)\n', value);
  }
  assert.equal(bad.length, 4);
});

test('(3) an invalid keys.cache_hours in the config: keys set --op refuses even with a valid --cache-hours (fail closed)', async () => {
  const h = await harness({ yml: 'keys:\n  cache_hours: 25\n' });
  assert.equal(await h.run(['set', 'jev', '--op', REF, '--cache-hours', '3']), 2);
  assert.deepEqual([h.op.refs, h.backend.calls, h.backend.store.size, h.stdout.text], [[], [], 0, '']);
  assert.equal(h.stderr.text, 'keys: keys.cache_hours must be a whole number of hours from 1 to 24 (.code-forge.yml)\n');
});

const BAD_CONFIG_WARN ='WARN keys: keys.cache_hours must be a whole number of hours from 1 to 24 (.code-forge.yml); caching for the default 8 h\n';

test('(3) an invalid keys.cache_hours in the config: keys test with the key in env works, with one WARN line', async () => {
  const bad = ['0', '25', '1.5', '"24"'];
  for (const value of bad) {
    const h = await harness({ yml: `keys:\n  cache_hours: ${value}\n` });
    const code = await runKeys(['test', 'jev'], {
      store: h.store,
      stdout: h.stdout,
      stderr: h.stderr,
      env: { CODE_FORGE_KEY_JEV: FAKE_OP_KEY },
      now: NOW,
      cwd: h.cwd,
      opRead: h.op.opRead,
    });
    assert.equal(code, 0, value);
    assert.equal(h.stderr.text, BAD_CONFIG_WARN, value);
    assert.deepEqual([h.op.refs, h.backend.store.size], [[], 0], value);
    assert.equal(countOccurrences(h.stdout.text + h.stderr.text, FAKE_OP_KEY), 0, value);
  }
  assert.equal(bad.length, 4);
});

test('(3) an invalid keys.cache_hours in the config: keys test that reads 1Password caches for 8 h, with one WARN line', async () => {
  const h = await harness({ yml: 'keys:\n  cache_hours: 25\n' });
  assert.equal(await h.run(['test', 'jev', '--ref', REF]), 0);
  assert.deepEqual(h.op.refs, [REF]);
  assert.equal(storedExp(h.backend), NOW + 8 * HOUR);
  assert.equal(h.stderr.text, `${BAD_CONFIG_WARN}${NOTE}`);
});

// ---- (4) validate ----

function baseCfg() {
  return structuredClone({
    version: 1,
    provider: 'anthropic',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' },
      L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' },
      L3: { model: 'claude-fable-5-1' },
    },
    keys: { jev: REF },
  });
}

test('(4) validateConfig refuses keys.cache_hours 0, 25, 1.5 and "24" as rule schema at /keys/cache_hours', () => {
  const cases = [
    [0, 'minimum'],
    [25, 'maximum'],
    [1.5, 'type'],
    ['24', 'type'],
  ];
  for (const [value, keyword] of cases) {
    const cfg = baseCfg();
    cfg.keys.cache_hours = value;
    const result = validateConfig(cfg);
    assert.equal(result.valid, false, String(value));
    assert.deepEqual(result.errors.map((e) => [e.rule, e.path, e.keyword]), [['schema', '/keys/cache_hours', keyword]], String(value));
  }
});

test('(4) validateConfig accepts keys.cache_hours 1 and 24 next to keys.jev: op://… (0 errors)', () => {
  for (const value of [1, 24]) {
    const cfg = baseCfg();
    cfg.keys.cache_hours = value;
    const result = validateConfig(cfg);
    assert.deepEqual([result.valid, result.errors.length], [true, 0], String(value));
  }
});

// ---- (5) the schema and the constants agree ----

test('(5) the schema minimum/maximum/default of keys.cache_hours equal MIN/MAX/DEFAULT_CACHE_HOURS (1, 24, 8)', async () => {
  const schema = JSON.parse(await readFile(SCHEMA, 'utf8'));
  const node = schema.properties.keys.properties.cache_hours;
  assert.deepEqual([node.type, node.minimum, node.maximum, node.default], ['integer', MIN_CACHE_HOURS, MAX_CACHE_HOURS, DEFAULT_CACHE_HOURS]);
  assert.deepEqual([MIN_CACHE_HOURS, MAX_CACHE_HOURS, DEFAULT_CACHE_HOURS], [1, 24, 8]);
});

test('cacheHoursFrom: absent ⇒ 8 default; valid ⇒ the value; invalid ⇒ 8 with a problem that never carries the value', () => {
  assert.deepEqual(cacheHoursFrom(null), { hours: 8, source: 'default', problem: null });
  assert.deepEqual(cacheHoursFrom({ keys: { jev: REF } }), { hours: 8, source: 'default', problem: null });
  assert.deepEqual(cacheHoursFrom({ keys: { cache_hours: 24 } }), { hours: 24, source: 'config', problem: null });
  assert.deepEqual(cacheHoursFrom({ keys: { cache_hours: 99 } }), { hours: 8, source: 'default', problem: 'keys.cache_hours must be a whole number of hours from 1 to 24' });
});

// ---- (6) every refresh path caches for keys.cache_hours ----

const EXPIRED = { jev: encodeEntry('FAKE-cf-expired-key-0000', NOW - 1) };

/** @param {Record<string, string>} data */
async function expiredStore(data = EXPIRED) {
  const backend = memoryBackend('keychain', { data });
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  return { backend, store };
}

test('(6) keys test --ref with keys.cache_hours: 24 refreshes an expired cache to now + 24 h', async () => {
  const h = await harness({ yml: 'keys:\n  cache_hours: 24\n', data: EXPIRED });
  assert.equal(await h.run(['test', 'jev', '--ref', REF]), 0);
  assert.deepEqual(h.op.refs, [REF]);
  assert.equal(storedExp(h.backend), NOW + 24 * HOUR);
  assert.equal(h.stdout.text, `jev: ok source=op expires=${iso(NOW + 24 * HOUR)}\n`);
});

test('(6) jev ask with keys.cache_hours: 24 refreshes an expired cache to now + 24 h', async () => {
  const { backend, store } = await expiredStore();
  const dir = await tempHome();
  await writeFile(path.join(dir, '.code-forge.yml'), `keys:\n  jev: ${REF}\n  cache_hours: 24\n`);
  await writeFile(path.join(dir, 'state.json'), JSON.stringify({ block: { task: 'x' } }));
  const op = fakeOp();
  /** @type {string[]} */
  const keysSeen = [];
  const stderr = sink();
  const code = await runJev(['ask', 'lane', '--state', 'state.json', '--cwd', dir, '--slug', 'demo'], {
    store,
    opRead: op.opRead,
    stdout: sink(),
    stderr,
    env: {},
    now: () => NOW,
    makeId: () => 'decision-1',
    appendRow: async (/** @type {any} */ row) => row,
    askJev: async (/** @type {any} */ args) => {
      keysSeen.push(args.key);
      return { ok: true, answers: { lane: { type: 'choice', choice: 'L0', probabilities: { L0: 1, L1: 0 } } }, usage: { input_tokens: 1, output_tokens: 1 }, attempts: 1, ms: 1 };
    },
  });
  assert.equal(code, 0);
  assert.deepEqual([op.refs, keysSeen, stderr.text], [[REF], [FAKE_OP_KEY], '']);
  assert.equal(storedExp(backend), NOW + 24 * HOUR);
});

test('jev ask with an invalid keys.cache_hours warns once and caches for 8 h', async () => {
  const { backend, store } = await expiredStore();
  const op = fakeOp();
  const stderr = sink();
  /** @type {string[]} */
  const keysSeen = [];
  const dir = await tempHome();
  await writeFile(path.join(dir, 'state.json'), '{}');
  const code = await runJev(['ask', 'lane', '--state', 'state.json', '--cwd', dir, '--slug', 'demo'], {
    store,
    opRead: op.opRead,
    loadConfig: async () => ({ ok: true, path: 'x', config: { keys: { jev: REF, cache_hours: 30 } } }),
    stdout: sink(),
    stderr,
    env: {},
    now: () => NOW,
    makeId: () => 'decision-1',
    appendRow: async (/** @type {any} */ row) => row,
    askJev: async (/** @type {any} */ args) => {
      keysSeen.push(args.key);
      return { ok: true, answers: { lane: { type: 'choice', choice: 'L0', probabilities: { L0: 1, L1: 0 } } }, usage: { input_tokens: 1, output_tokens: 1 }, attempts: 1, ms: 1 };
    },
  });
  assert.equal(code, 0);
  assert.deepEqual([op.refs, keysSeen], [[REF], [FAKE_OP_KEY]]);
  assert.equal(stderr.text, 'WARN jev ask: keys.cache_hours must be a whole number of hours from 1 to 24; caching for the default 8 h\n');
  assert.equal(storedExp(backend), NOW + 8 * HOUR);
});

test('(6) doctor checkKeys with keys.cache_hours: 24 refreshes an expired cache to now + 24 h', async () => {
  const { backend, store } = await expiredStore();
  const op = fakeOp();
  const res = await checkKeys({ keys: { jev: REF, cache_hours: 24 } }, { env: {}, store, opRead: op.opRead, now: NOW });
  assert.equal(res.key, FAKE_OP_KEY);
  assert.deepEqual(op.refs, [REF]);
  assert.equal(storedExp(backend), NOW + 24 * HOUR);
});

test('(6) the worker resolveJevKey with a run snapshot keys.cache_hours: 24 refreshes to now + 24 h; a snapshot without it ⇒ 8 h', async () => {
  const snapshot = { version: 1, keys: { jev: REF, cache_hours: 24 } };
  const a = await expiredStore();
  const opA = fakeOp();
  assert.equal(await resolveJevKey(snapshot, { store: a.store, env: {}, opRead: opA.opRead, now: NOW }), FAKE_OP_KEY);
  assert.equal(storedExp(a.backend), NOW + 24 * HOUR);

  const b = await expiredStore();
  const opB = fakeOp();
  assert.equal(await resolveJevKey({ version: 1, keys: { jev: REF } }, { store: b.store, env: {}, opRead: opB.opRead, now: NOW }), FAKE_OP_KEY);
  assert.equal(storedExp(b.backend), NOW + 8 * HOUR);
  assert.deepEqual([opA.refs, opB.refs], [[REF], [REF]]);
});

// ---- (7) a shorter value applies at the next read (D2) ----

test('(7) a cached entry with 20 h left read with 8 h: returned with exp = now + 8 h, store and index rewritten, no op call', async () => {
  const backend = memoryBackend('keychain');
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  await store.put('jev', FAKE_OP_KEY, { source: 'op', exp: NOW + 20 * HOUR });
  const op = fakeOp();
  const res = await resolveKey('jev', { store, ref: REF, env: {}, opRead: op.opRead, now: NOW, cacheHours: 8 });
  assert.deepEqual([res.value, res.source, res.exp, res.errors], [FAKE_OP_KEY, 'keychain', NOW + 8 * HOUR, []]);
  assert.deepEqual(op.refs, []);
  assert.deepEqual(JSON.parse(/** @type {string} */ (backend.store.get('jev'))), { v: FAKE_OP_KEY, exp: NOW + 8 * HOUR });
  assert.deepEqual(await store.list(), [{ name: 'jev', source: 'op', backend: 'keychain', exp: NOW + 8 * HOUR }]);
});

test('(7) the same entry read with 24 h is unchanged: no rewrite, no op call', async () => {
  const backend = memoryBackend('keychain');
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  await store.put('jev', FAKE_OP_KEY, { source: 'op', exp: NOW + 20 * HOUR });
  backend.calls.length = 0;
  const op = fakeOp();
  const res = await resolveKey('jev', { store, ref: REF, env: {}, opRead: op.opRead, now: NOW, cacheHours: 24 });
  assert.equal(res.exp, NOW + 20 * HOUR);
  assert.deepEqual([op.refs, backend.calls], [[], ['keychain.get:jev']]);
  assert.deepEqual(await store.list(), [{ name: 'jev', source: 'op', backend: 'keychain', exp: NOW + 20 * HOUR }]);
});

test('(7) an entry with exp far past the max, read with no hours, is cut to now + 24 h in the store and the index (store.read and resolveKey)', async () => {
  for (const viaResolve of [false, true]) {
    const backend = memoryBackend('keychain');
    const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
    await store.put('jev', FAKE_OP_KEY, { source: 'op', exp: NOW + 1000 * HOUR });
    const op = fakeOp();
    const exp = viaResolve
      ? (await resolveKey('jev', { store, env: {}, opRead: op.opRead, now: NOW })).exp
      : (await store.read('jev', NOW))?.exp;
    assert.equal(exp, NOW + 24 * HOUR, String(viaResolve));
    assert.equal(JSON.parse(/** @type {string} */ (backend.store.get('jev'))).exp, NOW + 24 * HOUR);
    assert.deepEqual(await store.list(), [{ name: 'jev', source: 'op', backend: 'keychain', exp: NOW + 24 * HOUR }], String(viaResolve));
    assert.deepEqual(op.refs, []);
  }
});

test('(7) a shortened entry with no index row: the backend is rewritten and no index row is invented', async () => {
  const backend = memoryBackend('keychain', { data: { jev: encodeEntry(FAKE_OP_KEY, NOW + 1000 * HOUR) } });
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  /** @type {string[]} */
  const errors = [];
  assert.equal((await store.read('jev', NOW, errors))?.exp, NOW + 24 * HOUR);
  assert.equal(JSON.parse(/** @type {string} */ (backend.store.get('jev'))).exp, NOW + 24 * HOUR);
  assert.deepEqual([await store.list(), errors], [[], []]);
});

test('(7) a rewrite that fails is reported in errors, the shorter exp is still returned, and the index keeps its old exp', async () => {
  const backend = memoryBackend('keychain');
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  await store.put('jev', FAKE_OP_KEY, { source: 'op', exp: NOW + 20 * HOUR });
  const stored = backend.store.get('jev');
  // the backend accepted the put; from now on every write fails
  backend.set = async () => {
    throw new Error('keychain is locked');
  };
  /** @type {string[]} */
  const errors = [];
  const got = await store.read('jev', NOW, errors, { maxHours: 8 });
  assert.equal(got?.exp, NOW + 8 * HOUR);
  assert.deepEqual(errors, ['keychain: rewrite of the shortened expiry failed (Error)']);
  assert.equal(backend.store.get('jev'), stored);
  assert.deepEqual(await store.list(), [{ name: 'jev', source: 'op', backend: 'keychain', exp: NOW + 20 * HOUR }]);
});

test('(7) a read-only backend: the rewrite failure is reported once and no index row is invented', async () => {
  const backend = memoryBackend('security-cli', { data: { jev: encodeEntry(FAKE_OP_KEY, NOW + 20 * HOUR) }, writable: false });
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  /** @type {string[]} */
  const errors = [];
  const got = await store.read('jev', NOW, errors, { maxHours: 8 });
  assert.equal(got?.exp, NOW + 8 * HOUR);
  assert.deepEqual(errors, ['security-cli: rewrite of the shortened expiry failed (Error)']);
  assert.deepEqual(await store.list(), []);
});

// ---- (8) a config that cannot be parsed ----

test('(8) an unparseable .code-forge.yml: keys set --op warns once and caches for 8 h', async () => {
  const h = await harness({ yml: 'keys: [unclosed\n  cache_hours: 24\n' });
  assert.equal(await h.run(['set', 'jev', '--op', REF]), 0);
  assert.equal(storedExp(h.backend), NOW + 8 * HOUR);
  assert.equal(h.stderr.text, `WARN keys: .code-forge.yml cannot be loaded (parse-error); caching for the default 8 h\n${NOTE}`);
  assert.equal(h.stdout.text, `stored jev in keychain (source=op, expires=${iso(NOW + 8 * HOUR)})\n`);
});

test('(8) an unparseable .code-forge.yml with --cache-hours 3: the WARN names the 3 h really used and the key caches for 3 h', async () => {
  const h = await harness({ yml: 'keys: [unclosed\n  cache_hours: 24\n' });
  assert.equal(await h.run(['set', 'jev', '--op', REF, '--cache-hours', '3']), 0);
  assert.deepEqual(h.op.refs, [REF]);
  assert.equal(storedExp(h.backend), NOW + 3 * HOUR);
  assert.equal(h.stderr.text, `WARN keys: .code-forge.yml cannot be loaded (parse-error); caching for 3 h from --cache-hours\n${NOTE}`);
  assert.equal(h.stdout.text, `stored jev in keychain (source=op, expires=${iso(NOW + 3 * HOUR)}) (cache 3 h from --cache-hours)\n`);
});

// ---- (9) init keeps keys.cache_hours ----

test('(9) init mergeConfig keeps keys.cache_hours on a re-run and on --no-jev', () => {
  const existing = { version: 1, keys: { jev: REF, cache_hours: 24 } };
  const rerun = mergeConfig(existing, { version: 1, keys: { jev: 'op://Private/jev2/credential' } }, { dropJev: false });
  assert.deepEqual(rerun.keys, { jev: 'op://Private/jev2/credential', cache_hours: 24 });
  const noJev = mergeConfig(existing, { version: 1, system1: { fallback: 'rules' } }, { dropJev: true });
  assert.deepEqual(noJev.keys, { cache_hours: 24 });
});
