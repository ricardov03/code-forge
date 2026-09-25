import { countOccurrences, FAKE_KEY, tempHome } from './helpers.mjs';
import assert from 'node:assert/strict';
import { chmod, link, mkdir, readdir, readFile, stat, symlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { SERVICE } from '../../src/keys/backends/constants.mjs';
import { createFileBackend, writePrivateFile } from '../../src/keys/backends/file.mjs';
import { createKeychainBackend, PROBE_ACCOUNT } from '../../src/keys/backends/keychain.mjs';
import { createSecurityCliBackend, NOT_FOUND_EXIT, SECURITY_BIN } from '../../src/keys/backends/security-cli.mjs';

// ── file backend ────────────────────────────────────────────────────────────

test('file backend writes mode 0o600 inside a 0o700 directory, and leaves no temp file behind', async () => {
  const dir = path.join(await tempHome(), '.code-forge', 'store');
  const file = createFileBackend({ dir });
  await file.set('jev', FAKE_KEY);
  assert.equal((await stat(path.join(dir, 'jev.key'))).mode & 0o777, 0o600);
  assert.equal((await stat(dir)).mode & 0o777, 0o700);
  assert.deepEqual(await readdir(dir), ['jev.key']);
  assert.equal(await file.get('jev'), FAKE_KEY);
});

test('a pre-existing 0644 key file is replaced, never written through: the secret never exists at a loose mode', async () => {
  const home = await tempHome();
  const dir = path.join(home, 'store');
  await mkdir(dir, { recursive: true });
  const target = path.join(dir, 'jev.key');
  await writeFile(target, 'FAKE-old-value');
  await chmod(target, 0o644);
  // A hard link to the old inode: whatever was written into that 0644 inode would show up here.
  const witness = path.join(home, 'witness');
  await link(target, witness);

  await createFileBackend({ dir }).set('jev', FAKE_KEY);

  assert.equal(await readFile(witness, 'utf8'), 'FAKE-old-value');
  assert.equal((await stat(witness)).mode & 0o777, 0o644);
  assert.equal((await stat(target)).mode & 0o777, 0o600);
  assert.equal(await readFile(target, 'utf8'), FAKE_KEY);
});

test('a symlink planted at the key path is replaced, not followed', async () => {
  const home = await tempHome();
  const dir = path.join(home, 'store');
  await mkdir(dir, { recursive: true });
  const outside = path.join(home, 'outside.txt');
  await writeFile(outside, 'untouched');
  await symlink(outside, path.join(dir, 'jev.key'));

  await createFileBackend({ dir }).set('jev', FAKE_KEY);

  assert.equal(await readFile(outside, 'utf8'), 'untouched');
  assert.equal((await stat(path.join(dir, 'jev.key'))).mode & 0o777, 0o600);
});

test('writePrivateFile creates the directory and a 0600 file, leaving only that file', async () => {
  const dir = path.join(await tempHome(), 'store');
  await writePrivateFile(path.join(dir, 'index.json'), '{}');
  assert.equal((await stat(path.join(dir, 'index.json'))).mode & 0o777, 0o600);
  assert.deepEqual(await readdir(dir), ['index.json']);
});

test('file backend: missing key is null, delete reports whether it existed', async () => {
  const file = createFileBackend({ dir: path.join(await tempHome(), 'store') });
  assert.equal(await file.get('nope'), null);
  assert.equal(await file.delete('nope'), false);
  await file.set('jev', FAKE_KEY);
  assert.equal(await file.delete('jev'), true);
  assert.equal(await file.get('jev'), null);
  assert.equal(await file.available(), true);
});

test('file backend: an empty file is absent; one trailing newline is dropped, inner spaces kept', async () => {
  const dir = path.join(await tempHome(), 'store');
  await mkdir(dir, { recursive: true });
  await writeFile(path.join(dir, 'empty.key'), '');
  await writeFile(path.join(dir, 'nl.key'), 'ab c \n');
  const file = createFileBackend({ dir });
  assert.equal(await file.get('empty'), null);
  assert.equal(await file.get('nl'), 'ab c ');
});

test('file backend refuses names that would leave the store: 4 cases, for get, set and delete', async () => {
  const file = createFileBackend({ dir: path.join(await tempHome(), 'store') });
  for (const bad of ['../x', 'a/b', '.hidden', '']) {
    await assert.rejects(file.get(bad), TypeError, `get ${bad}`);
    await assert.rejects(file.set(bad, FAKE_KEY), TypeError, `set ${bad}`);
    await assert.rejects(file.delete(bad), TypeError, `delete ${bad}`);
  }
});

test('file backend rethrows errors other than ENOENT', async () => {
  const dir = path.join(await tempHome(), 'store');
  await mkdir(path.join(dir, 'jev.key'), { recursive: true });
  await assert.rejects(createFileBackend({ dir }).get('jev'), { code: 'EISDIR' });
});

// ── keychain backend (fake module; the real login keychain is never touched) ─

/** @param {{probeThrows?: boolean}} [opts] */
function fakeKeyring({ probeThrows = false } = {}) {
  /** @type {Map<string, string>} */
  const vault = new Map();
  /** @type {Array<[string, string]>} */
  const opened = [];
  class Entry {
    constructor(/** @type {string} */ service, /** @type {string} */ account) {
      opened.push([service, account]);
      this.id = `${service}/${account}`;
    }
    getPassword() {
      if (probeThrows) {
        throw new Error('Platform secure storage failure');
      }
      return vault.get(this.id) ?? null;
    }
    setPassword(/** @type {string} */ value) {
      vault.set(this.id, value);
    }
    deletePassword() {
      return vault.delete(this.id);
    }
  }
  return { mod: { Entry }, vault, opened };
}

test('keychain backend probes usability once, then stores under service "code-forge", account = key name', async () => {
  const { mod, vault, opened } = fakeKeyring();
  const kc = createKeychainBackend({ loadModule: async () => mod });
  assert.equal(await kc.available(), true);
  assert.deepEqual(opened, [[SERVICE, PROBE_ACCOUNT]]);
  await kc.set('jev', FAKE_KEY);
  assert.equal(await kc.get('jev'), FAKE_KEY);
  assert.deepEqual(opened.slice(1), [[SERVICE, 'jev'], [SERVICE, 'jev']]);
  assert.deepEqual([...vault.keys()], ['code-forge/jev']);
  assert.equal(await kc.delete('jev'), true);
  assert.equal(await kc.get('jev'), null);
});

test('keychain backend: the binding loads but the store is unusable ⇒ available() is false', async () => {
  const { mod } = fakeKeyring({ probeThrows: true });
  const kc = createKeychainBackend({ loadModule: async () => mod });
  assert.equal(await kc.available(), false);
  await assert.rejects(kc.get('jev'), /not available/);
});

test('keychain backend accepts a CommonJS-style default export; a module without Entry is unavailable', async () => {
  const { mod } = fakeKeyring();
  assert.equal(await createKeychainBackend({ loadModule: async () => ({ default: mod }) }).available(), true);
  assert.equal(await createKeychainBackend({ loadModule: async () => ({}) }).available(), false);
});

test('keychain backend is unavailable when the optional module fails to load, and loads once', async () => {
  let loads = 0;
  const kc = createKeychainBackend({
    loadModule: async () => {
      loads += 1;
      throw new Error("Cannot find module '@napi-rs/keyring'");
    },
  });
  assert.equal(await kc.available(), false);
  assert.equal(await kc.available(), false);
  assert.equal(loads, 1);
  await assert.rejects(kc.get('jev'), /not available/);
});

// ── security CLI backend (fake exec; /usr/bin/security is never run) ──────────

/**
 * Mirrors B0's `exec`: `result` is `ok` exactly when `code` is in the caller's `okExitCodes`
 * (default `[0]`), unless a step overrides it.
 * @param {Array<Record<string, unknown>>} script - partial `ExecResult`s, one per call.
 */
function fakeExec(script) {
  /** @type {string[][]} */
  const argvs = [];
  /** @type {any[]} */
  const optsSeen = [];
  const exec = async (/** @type {string[]} */ argv, /** @type {any} */ opts = {}) => {
    argvs.push(argv);
    optsSeen.push(opts);
    const step = script[argvs.length - 1] ?? {};
    const code = 'code' in step ? step.code : 0;
    const okCodes = opts.okExitCodes ?? [0];
    const result = code !== null && okCodes.includes(code) ? 'ok' : 'failed';
    return { result, code, signal: null, stdout: '', stderr: '', timedOut: false, ...step };
  };
  return { exec: /** @type {any} */ (exec), argvs, optsSeen };
}

test('security-cli module loads with no import from keychain.mjs (so a broken binding cannot block it)', async () => {
  const src = await readFile(new URL('../../src/keys/backends/security-cli.mjs', import.meta.url), 'utf8');
  const imports = src.split('\n').filter((line) => line.startsWith('import '));
  assert.deepEqual(imports, ["import { exec as realExec } from '../../util/exec.mjs';", "import { SERVICE } from './constants.mjs';"]);
});

test('security-cli get: argv array by absolute path, value from stdout, exit 44 ⇒ null', async () => {
  const { exec, argvs, optsSeen } = fakeExec([{ code: 0, stdout: `${FAKE_KEY}\n` }, { code: NOT_FOUND_EXIT }]);
  const sec = createSecurityCliBackend({ exec, platform: 'darwin' });
  assert.equal(await sec.get('jev'), FAKE_KEY);
  assert.equal(await sec.get('missing'), null);
  assert.equal(argvs.length, 2);
  assert.deepEqual(argvs[0], [SECURITY_BIN, 'find-generic-password', '-s', 'code-forge', '-a', 'jev', '-w']);
  assert.deepEqual(argvs[1], [SECURITY_BIN, 'find-generic-password', '-s', 'code-forge', '-a', 'missing', '-w']);
  assert.deepEqual(optsSeen[1].okExitCodes, [0, 44]);
});

test('security-cli get: an item with an empty password reads as absent', async () => {
  const { exec } = fakeExec([{ code: 0, stdout: '\n' }]);
  assert.equal(await createSecurityCliBackend({ exec, platform: 'darwin' }).get('jev'), null);
});

test('security-cli is read/delete only: set refuses and spawns nothing, so the value never reaches argv', async () => {
  const { exec, argvs } = fakeExec([]);
  const sec = createSecurityCliBackend({ exec, platform: 'darwin' });
  assert.equal(sec.writable, false);
  await assert.rejects(sec.set('jev', FAKE_KEY), /not supported/);
  assert.equal(argvs.length, 0);
});

test('security-cli delete: exit 0 ⇒ true, exit 44 ⇒ false, other failure throws', async () => {
  const { exec, argvs } = fakeExec([{ code: 0 }, { code: NOT_FOUND_EXIT }, { code: 1 }]);
  const sec = createSecurityCliBackend({ exec, platform: 'darwin' });
  assert.equal(await sec.delete('jev'), true);
  assert.equal(await sec.delete('jev'), false);
  await assert.rejects(sec.delete('jev'), /exit 1/);
  assert.equal(argvs.length, 3);
  assert.deepEqual(argvs[0], [SECURITY_BIN, 'delete-generic-password', '-s', 'code-forge', '-a', 'jev']);
});

test('security-cli get failure throws without echoing output', async () => {
  const { exec } = fakeExec([{ code: 51, stdout: FAKE_KEY, stderr: FAKE_KEY }]);
  const sec = createSecurityCliBackend({ exec, platform: 'darwin' });
  await assert.rejects(sec.get('jev'), (err) => countOccurrences(String(err), FAKE_KEY) === 0 && /exit 51/.test(String(err)));
});

test('security-cli is available only on darwin, and only when the binary answers', async () => {
  const linux = fakeExec([]);
  assert.equal(await createSecurityCliBackend({ exec: linux.exec, platform: 'linux' }).available(), false);
  assert.equal(linux.argvs.length, 0);
  assert.equal(await createSecurityCliBackend({ exec: fakeExec([{ code: 0 }]).exec, platform: 'darwin' }).available(), true);
  const broken = fakeExec([{ code: null }]);
  assert.equal(await createSecurityCliBackend({ exec: broken.exec, platform: 'darwin' }).available(), false);
  assert.deepEqual(broken.argvs[0], [SECURITY_BIN, 'help']);
});
