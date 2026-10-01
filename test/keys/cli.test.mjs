import { countOccurrences, FAKE_KEY, FAKE_OP_KEY, memoryBackend, sink, tempHome } from './helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, test } from 'node:test';
import { formatExpiry, parseKeysArgs, runKeys } from '../../src/cli/keys.mjs';
import { createFileBackend } from '../../src/keys/backends/file.mjs';
import { createKeyStore, encodeEntry } from '../../src/keys/store.mjs';
import { exec } from '../../src/util/exec.mjs';
import { clearSecrets } from '../../src/util/redact.mjs';

const BIN = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');
const NOW = 1_800_000_000_000;
const HOUR = 3_600_000;
const REF = 'op://Private/jev/credential';
const USAGE = 'usage: code-forge keys list | set <name> [--op <item-id|link|op://ref>] | test <name> [--ref <ref>] | remove <name>\n';

afterEach(() => clearSecrets());

/** @param {Record<string, string>} [data] @param {string} [backendName] */
async function harness(data = {}, backendName = 'keychain') {
  const backend = memoryBackend(backendName, { data });
  const store = await createKeyStore({ backends: [backend], dir: await tempHome() });
  const stdout = sink();
  const stderr = sink();
  /** @param {string[]} args @param {object} [extra] */
  const run = (args, extra = {}) => runKeys(args, { store, stdout, stderr, env: {}, now: NOW, ...extra });
  return { store, backend, stdout, stderr, run };
}

// printed on stderr before any call that may make 1Password ask for approval
const NOTE = '1Password may ask you to approve access: check the 1Password window (Touch ID or password).\n';

test('keys list prints name, source, backend and expiry — the fake key 0 times', async () => {
  const h = await harness();
  await h.store.put('jev', FAKE_KEY, { source: 'op', exp: NOW + 8 * HOUR });
  await h.store.put('other', FAKE_OP_KEY, { source: 'user', exp: null });

  assert.equal(await h.run(['list']), 0);

  assert.deepEqual(h.stdout.text.trimEnd().split('\n'), [
    'NAME\tSOURCE\tBACKEND\tEXPIRES',
    `jev\top\tkeychain\t${new Date(NOW + 8 * HOUR).toISOString()}`,
    'other\tuser\tkeychain\tnever',
  ]);
  const all = h.stdout.text + h.stderr.text;
  assert.equal(countOccurrences(all, FAKE_KEY), 0);
  assert.equal(countOccurrences(all, FAKE_OP_KEY), 0);
});

test('formatExpiry: never / expired / ISO', () => {
  assert.equal(formatExpiry(null, NOW), 'never');
  assert.equal(formatExpiry(NOW, NOW), 'expired');
  assert.equal(formatExpiry(NOW + 1000, NOW), new Date(NOW + 1000).toISOString());
});

test('keys set prompts (hidden) and stores with source=user; the typed value is printed 0 times', async () => {
  const h = await harness();
  /** @type {string[]} */
  const asked = [];
  const code = await h.run(['set', 'jev'], {
    ask: async (/** @type {string} */ n) => {
      asked.push(n);
      return FAKE_KEY;
    },
  });
  assert.equal(code, 0);
  assert.deepEqual(asked, ['jev']);
  assert.deepEqual(JSON.parse(h.backend.store.get('jev')), { v: FAKE_KEY, exp: null });
  assert.equal(h.stdout.text, 'stored jev in keychain (source=user, expires=never)\n');
});

test('keys set overwrites an existing entry instead of returning it', async () => {
  const h = await harness({ jev: encodeEntry('FAKE-old-value-111', null) });
  assert.equal(await h.run(['set', 'jev'], { ask: async () => FAKE_KEY }), 0);
  assert.equal(JSON.parse(h.backend.store.get('jev')).v, FAKE_KEY);
});

test('keys set with an empty/cancelled prompt stores nothing and exits 1', async () => {
  const h = await harness();
  assert.equal(await h.run(['set', 'jev'], { ask: async () => null }), 1);
  assert.equal(h.backend.store.size, 0);
  assert.equal(h.stderr.text, 'jev: nothing stored\n');
});

test('keys set --op reads 1Password once and caches for 8 h', async () => {
  const h = await harness();
  /** @type {string[]} */
  const refs = [];
  const opRead = async (/** @type {string} */ ref) => {
    refs.push(ref);
    return { value: FAKE_OP_KEY, attempts: 1 };
  };
  assert.equal(await h.run(['set', 'jev', '--op', REF], { opRead }), 0);
  assert.deepEqual(refs, [REF]);
  assert.deepEqual(JSON.parse(h.backend.store.get('jev')), { v: FAKE_OP_KEY, exp: NOW + 8 * HOUR });
  assert.equal(countOccurrences(h.stdout.text + h.stderr.text, FAKE_OP_KEY), 0);
});

test('keys set --op with a bad reference exits 2 without calling op', async () => {
  const h = await harness();
  let called = 0;
  const code = await h.run(['set', 'jev', '--op', 'op://vault/item'], {
    opRead: async () => {
      called += 1;
      return { value: FAKE_OP_KEY, attempts: 1 };
    },
  });
  assert.deepEqual([code, called, h.stderr.text], [2, 0, 'jev: --op must be a 1Password item ID, item link or op://vault/item/field reference\n']);
});

test('keys set --op failure exits 1 and reports the error', async () => {
  const h = await harness();
  const code = await h.run(['set', 'jev', '--op', REF], { opRead: async () => ({ value: null, attempts: 2, error: 'op read failed (timed out)' }) });
  assert.deepEqual([code, h.stderr.text], [1, NOTE + 'jev: op read failed (timed out)\n']);
});

test('keys test reports the backend that held it as the source, and the expiry — never the value', async () => {
  const h = await harness({ jev: encodeEntry(FAKE_KEY, NOW + HOUR) });
  assert.equal(await h.run(['test', 'jev']), 0);
  assert.equal(h.stdout.text, `jev: ok source=keychain expires=${new Date(NOW + HOUR).toISOString()}\n`);
  assert.equal(countOccurrences(h.stdout.text + h.stderr.text, FAKE_KEY), 0);
});

test('keys test on a backend not named keychain prints that backend name (no hardcoded label)', async () => {
  const h = await harness({ jev: encodeEntry(FAKE_KEY, null) }, 'file');
  assert.equal(await h.run(['test', 'jev']), 0);
  assert.equal(h.stdout.text, 'jev: ok source=file expires=never\n');
});

test('keys test --ref env:NAME resolves from that env var', async () => {
  const h = await harness();
  assert.equal(await h.run(['test', 'jev', '--ref', 'env:MY_JEV'], { env: { MY_JEV: FAKE_KEY } }), 0);
  assert.equal(h.stdout.text, 'jev: ok source=env expires=never\n');
});

test('keys test on a missing key exits 1', async () => {
  const h = await harness();
  assert.equal(await h.run(['test', 'jev']), 1);
  assert.equal(h.stderr.text, 'jev: not found\n');
});

test('keys remove deletes the entry and the index row', async () => {
  const h = await harness();
  await h.store.put('jev', FAKE_KEY, { source: 'user', exp: null });
  assert.equal(await h.run(['remove', 'jev']), 0);
  assert.equal(h.stdout.text, 'removed jev (1 backend)\n');
  assert.deepEqual(await h.store.list(), []);
});

test('an invalid name exits 2 on set, test and remove, before any store call', async () => {
  for (const sub of ['set', 'test', 'remove']) {
    for (const bad of ['../x', 'Jev']) {
      const h = await harness();
      let asked = 0;
      const code = await h.run([sub, bad], {
        ask: async () => {
          asked += 1;
          return FAKE_KEY;
        },
      });
      assert.equal(code, 2, `${sub} ${bad}`);
      assert.deepEqual([h.backend.calls, asked], [[], 0], `${sub} ${bad}`);
      assert.equal(h.stderr.text, 'keys: key name must match /^[a-z][a-z0-9_-]{0,63}$/\n');
    }
  }
});

test('keys test with a malformed --ref exits 2 before any store call', async () => {
  const h = await harness();
  assert.equal(await h.run(['test', 'jev', '--ref', 'op://nope']), 2);
  assert.deepEqual(h.backend.calls, []);
});

test('usage errors exit 2 with the usage line: 11 malformed argument lists', async () => {
  const cases = [
    [],
    ['frob'],
    ['set'],
    ['test'],
    ['remove'],
    ['list', 'extra'],
    ['set', 'jev', '--op'],
    ['set', '--op', REF, 'jev'],
    ['set', 'jev', '--op', '--ref'],
    ['test', 'jev', '--op', REF],
    ['remove', 'jev', 'extra'],
  ];
  for (const args of cases) {
    const h = await harness();
    assert.equal(await h.run(args), 2, JSON.stringify(args));
    assert.equal(h.stderr.text, USAGE, JSON.stringify(args));
    assert.deepEqual(h.backend.calls, [], JSON.stringify(args));
  }
});

test('parseKeysArgs: the accepted shapes', () => {
  assert.deepEqual(parseKeysArgs(['list']), { sub: 'list' });
  assert.deepEqual(parseKeysArgs(['set', 'jev', '--op', REF]), { sub: 'set', name: 'jev', flagArg: REF });
  assert.deepEqual(parseKeysArgs(['test', 'jev', '--ref', 'env:X']), { sub: 'test', name: 'jev', flagArg: 'env:X' });
  assert.deepEqual(parseKeysArgs(['remove', 'jev']), { sub: 'remove', name: 'jev' });
  assert.equal(parseKeysArgs(['toString']), null);
});

test('file fallback: exactly one WARN line on stderr and exit 0', async () => {
  const store = await createKeyStore({ backends: [memoryBackend('file')], dir: await tempHome() });
  const stderr = sink();
  const code = await runKeys(['list'], { store, stdout: sink(), stderr, env: {}, now: NOW });
  assert.equal(code, 0);
  assert.equal(stderr.text, 'WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n');
});

test('end to end through the router under a temp HOME (file backend): list and test name the file backend, the fake key 0 times', async () => {
  const home = await tempHome();
  const env = { PATH: process.env.PATH ?? '', HOME: home, CODE_FORGE_KEY_BACKEND: 'file' };
  const dir = path.join(home, '.code-forge', 'store');
  const store = await createKeyStore({ backends: [createFileBackend({ dir })], dir });
  await store.put('jev', FAKE_KEY, { source: 'op', exp: Date.now() + 8 * HOUR });

  const list = await exec([process.execPath, BIN, 'keys', 'list'], { env, timeoutMs: 20_000 });
  const probe = await exec([process.execPath, BIN, 'keys', 'test', 'jev'], { env, timeoutMs: 20_000 });

  assert.deepEqual([list.result, probe.result], ['ok', 'ok']);
  const rows = list.stdout.trimEnd().split('\n');
  assert.equal(rows.length, 2);
  assert.match(rows[1], /^jev\top\tfile\t\d{4}-\d\d-\d\dT/);
  assert.match(probe.stdout, /^jev: ok source=file expires=\d{4}-/);
  const all = list.stdout + list.stderr + probe.stdout + probe.stderr;
  assert.equal(countOccurrences(all, FAKE_KEY), 0);
});

// ---- B25: --op / --ref take a 1Password item ID or link, resolved to a full op:// ref first ----

const ITEM_ID = 'abcdefghij0123456789klmnop';
const VAULT_ID = 'zyxwvutsrq9876543210ponmlk';
const RESOLVED = `op://${VAULT_ID}/${ITEM_ID}/credential`;
const ITEM_STDOUT = JSON.stringify({
  id: ITEM_ID,
  title: 'Jev API key',
  category: 'API_CREDENTIAL',
  vault: { id: VAULT_ID, name: 'Dev Keys' },
  fields: [{ id: 'credential', label: 'credential', type: 'CONCEALED', value: FAKE_OP_KEY, reference: 'op://Dev Keys/Jev API key/credential' }],
});

/** @param {Array<Record<string, unknown>>} script */
function fakeOpExec(script) {
  /** @type {string[][]} */
  const calls = [];
  const opExec = /** @type {any} */ (async (/** @type {string[]} */ argv) => {
    calls.push(argv);
    return { result: 'failed', code: null, signal: null, stdout: '', stderr: '', timedOut: false, ...(script[calls.length - 1] ?? {}) };
  });
  return { opExec, calls };
}

test('keys set --op <item-id> resolves the item, reads the resolved ref once and caches it', async () => {
  const h = await harness();
  const { opExec, calls } = fakeOpExec([{ result: 'ok', code: 0, stdout: ITEM_STDOUT }]);
  /** @type {string[]} */
  const refs = [];
  const opRead = async (/** @type {string} */ ref) => (refs.push(ref), { value: FAKE_OP_KEY, attempts: 1 });
  assert.equal(await h.run(['set', 'jev', '--op', ITEM_ID], { opRead, opExec }), 0);
  assert.deepEqual(calls, [['op', 'item', 'get', ITEM_ID, '--format', 'json']]);
  assert.deepEqual(refs, [RESOLVED]);
  assert.deepEqual(JSON.parse(h.backend.store.get('jev')), { v: FAKE_OP_KEY, exp: NOW + 8 * HOUR });
  assert.equal(h.stdout.text.split('\n')[0], `jev: 1Password item "Jev API key" (vault Dev Keys, field credential) -> ${RESOLVED}`);
  assert.equal(countOccurrences(h.stdout.text + h.stderr.text, FAKE_OP_KEY), 0);
});

test('keys set --op <item-id> when 1Password is locked: exit 1, the classified message, nothing stored, op read never called', async () => {
  const h = await harness();
  const { opExec } = fakeOpExec([{ result: 'failed', code: 1, stderr: `not signed in ${FAKE_OP_KEY}` }]);
  let reads = 0;
  const opRead = async () => (reads += 1, { value: FAKE_OP_KEY, attempts: 1 });
  const code = await h.run(['set', 'jev', '--op', ITEM_ID], { opRead, opExec });
  assert.deepEqual([code, reads, h.stderr.text], [1, 0, NOTE + 'jev: 1Password is locked or not signed in; unlock the app (or run `op signin`) and try again\n']);
  assert.equal(h.backend.store.has('jev'), false);
  assert.equal(countOccurrences(h.stdout.text + h.stderr.text, FAKE_OP_KEY), 0);
});

test('keys set --op: the op CLI missing gives the install hint, exit 1', async () => {
  const h = await harness();
  const { opExec } = fakeOpExec([{ result: 'failed', code: null, error: 'spawn op ENOENT' }]);
  assert.equal(await h.run(['set', 'jev', '--op', ITEM_ID], { opExec }), 1);
  assert.equal(h.stderr.text, NOTE + 'jev: 1Password CLI not found; install it with: brew install 1password-cli\n');
});

test('keys test --ref <item-id> resolves first, then reads the resolved op:// ref', async () => {
  const h = await harness();
  const { opExec } = fakeOpExec([{ result: 'ok', code: 0, stdout: ITEM_STDOUT }]);
  /** @type {string[]} */
  const refs = [];
  const opRead = async (/** @type {string} */ ref) => (refs.push(ref), { value: FAKE_OP_KEY, attempts: 1 });
  assert.equal(await h.run(['test', 'jev', '--ref', ITEM_ID], { opRead, opExec }), 0);
  assert.deepEqual(refs, [RESOLVED]);
  assert.equal(countOccurrences(h.stdout.text + h.stderr.text, FAKE_OP_KEY), 0);
});

test('keys test: a timed-out op read reports the classified message and exits 1', async () => {
  const h = await harness();
  const opRead = async () => ({ value: null, attempts: 2, error: '1Password did not answer in time; unlock the app and try again', kind: 'op_timeout' });
  assert.equal(await h.run(['test', 'jev', '--ref', REF], { opRead }), 1);
  assert.equal(h.stderr.text, NOTE + 'jev: 1Password did not answer in time; unlock the app and try again\njev: not found\n');
});

test('keys set --op <item-id>: an exec that throws gives the fixed op_failed message, exit 1, nothing stored, no thrown text', async () => {
  const h = await harness();
  const opExec = /** @type {any} */ (async () => {
    throw new Error(`boom ${FAKE_OP_KEY}`);
  });
  assert.equal(await h.run(['set', 'jev', '--op', ITEM_ID], { opExec }), 1);
  assert.equal(h.stderr.text, `${NOTE}jev: 1Password CLI failed (unexpected error); run \`op item get ${ITEM_ID}\` yourself to see why\n`);
  assert.equal(h.backend.store.has('jev'), false);
  assert.equal(countOccurrences(h.stdout.text + h.stderr.text, FAKE_OP_KEY), 0);
});
