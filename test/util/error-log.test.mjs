// B27 error log: the router writes one scrubbed JSON line per failed verb. Router runs use a
// hermetic copy of the package (bin, src/util, src/keys/onepassword.mjs and a few fake verbs)
// spawned with HOME and cwd inside one per-file temp parent, removed in `after()`.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, describe, test } from 'node:test';

const PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-errlog-')));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { appendLine, capBytes, createScrubber, describeCounts, fingerprint, fingerprintOf, flagNames, logVerbFailure, normalizeMessage, subcommandOf } = await import('../../src/util/error-log.mjs');
const { HINT_LINE, maybeHint } = await import('../../src/util/error-hint.mjs');
const { clearErrorKind, reportErrorKind, takeErrorKind } = await import('../../src/util/error-kind.mjs');

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const VERSION = JSON.parse(readFileSync(path.join(ROOT, 'package.json'), 'utf8')).version;
const FAKE_KEY = 'sk-ant-FAKE0123456789abcdef';

// The package copy lives under PKG_HOME, so a crash stack's frames are under that HOME and scrubbed.
const PKG_HOME = path.join(PARENT, 'hpkg');
const PKG = path.join(PKG_HOME, 'pkg');
mkdirSync(path.join(PKG_HOME, 'proj'), { recursive: true });
mkdirSync(path.join(PKG, 'src', 'cli'), { recursive: true });
mkdirSync(path.join(PKG, 'src', 'keys'), { recursive: true });
cpSync(path.join(ROOT, 'bin'), path.join(PKG, 'bin'), { recursive: true });
cpSync(path.join(ROOT, 'src', 'util'), path.join(PKG, 'src', 'util'), { recursive: true });
cpSync(path.join(ROOT, 'src', 'keys', 'onepassword.mjs'), path.join(PKG, 'src', 'keys', 'onepassword.mjs'));
cpSync(path.join(ROOT, 'package.json'), path.join(PKG, 'package.json'));
const VERBS = {
  fail: "export default async function () { process.stderr.write('progress note\\n'); process.stderr.write(`fail: boom in ${process.env.HOME}/notes and ${process.cwd()}/src\\n`); return 1; }",
  ok: 'export default async function () { return 0; }',
  crash: "export default async function () { throw new Error(`kaboom at ${process.cwd()}/a.mjs`); }",
  long: "export default async function () { process.stderr.write('\u00e9'.repeat(1500)); return 1; }",
  tools: 'export default async function () { return 1; }',
  mykind: "export default async function (_args, ctx) { ctx.reportErrorKind('quota_hit'); return 1; }",
  weird: "export default async function () { throw { toString() { throw new Error('no'); } }; }",
  usage: "export default async function () { process.stderr.write('usage: code-forge usage <x>\\n'); return 2; }",
  optime:
    "import { opRead } from '../keys/onepassword.mjs';\n" +
    "export default async function () {\n" +
    "  const res = await opRead('op://Private/Jev/credential', { exec: async () => ({ result: 'failed', code: null, signal: 'SIGKILL', timedOut: true, stdout: '', stderr: '' }) });\n" +
    "  process.stderr.write(`keys: ${res.error}\\n`);\n  return 1;\n}\n",
};
for (const [name, src] of Object.entries(VERBS)) writeFileSync(path.join(PKG, 'src', 'cli', `${name}.mjs`), src);

let n = 0;
/** A fresh HOME and a project dir inside it. */
function fresh() {
  n += 1;
  const home = path.join(PARENT, `h${n}`);
  const cwd = path.join(home, 'proj');
  mkdirSync(cwd, { recursive: true });
  return { home, cwd, log: path.join(home, '.code-forge', 'logs', 'errors.jsonl') };
}

/** @param {string[]} args @param {{home: string, cwd: string}} where @param {Record<string, string>} [extraEnv] */
function cli(args, where, extraEnv = {}) {
  const env = { PATH: process.env.PATH ?? '', HOME: where.home, ...extraEnv };
  return spawnSync(process.execPath, [path.join(PKG, 'bin', 'code-forge.mjs'), ...args], { cwd: where.cwd, env, encoding: 'utf8' });
}

/** @param {string} file */
function lines(file) {
  return existsSync(file) ? readFileSync(file, 'utf8').split('\n').filter((l) => l.length > 0) : [];
}

describe('the router logs failed verbs', () => {
  test('a verb returning 1 appends exactly 1 line with the exact fields; flag values and positionals are left out', () => {
    const w = fresh();
    const r = cli(['fail', '-q', `--key=${FAKE_KEY}`, '--yes', FAKE_KEY, 'positional-word'], w);
    assert.equal(r.status, 1);
    const got = lines(w.log);
    assert.equal(got.length, 1);
    assert.equal(got[0].split(FAKE_KEY).length - 1, 0);
    assert.equal(got[0].split('positional-word').length - 1, 0);
    const e = JSON.parse(got[0]);
    assert.deepEqual(Object.keys(e), ['ts', 'version', 'node', 'platform', 'arch', 'verb', 'sub', 'flags', 'exit', 'kind', 'message', 'stack', 'cleaned', 'fp']);
    assert.match(e.ts, /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/);
    assert.deepEqual(
      { ...e, ts: 'T' },
      {
        ts: 'T', version: VERSION, node: process.version, platform: process.platform, arch: process.arch,
        verb: 'fail', sub: null, flags: ['-q', '--key', '--yes'], exit: 1, kind: 'error',
        message: 'fail: boom in ~/notes and <project>/src', stack: null,
        cleaned: [{ rule: 'home', count: 1 }, { rule: 'project_path', count: 1 }],
        // sha256("fail\n\nerror\nfail: boom in and"), first 12 hex (checked with shasum)
        fp: '47df05de5817',
      },
    );
    // non-TTY (spawnSync pipes stderr): no hint line, no hint state
    assert.equal(r.stderr.includes('code-forge logs report'), false);
    assert.equal(existsSync(path.join(w.home, '.code-forge', 'logs', 'hints.json')), false);
  });

  test('exit 0 appends 0 lines; exit 2 logs kind usage', () => {
    const w = fresh();
    assert.equal(cli(['ok', '--yes'], w).status, 0);
    assert.equal(lines(w.log).length, 0);
    assert.equal(cli(['usage', '--bad'], w).status, 2);
    const got = lines(w.log).map((l) => JSON.parse(l));
    assert.equal(got.length, 1);
    assert.deepEqual([got[0].kind, got[0].exit, got[0].message, got[0].flags], ['usage', 2, 'usage: code-forge usage <x>', ['--bad']]);
  });

  test('a throw logs kind crash with a scrubbed stack; the exit stays 1', () => {
    const w = { home: PKG_HOME, cwd: path.join(PKG_HOME, 'proj'), log: path.join(PKG_HOME, '.code-forge', 'logs', 'errors.jsonl') };
    const r = cli(['crash'], w);
    assert.equal(r.status, 1);
    const got = lines(w.log).map((l) => JSON.parse(l));
    assert.equal(got.length, 1);
    assert.equal(got[0].kind, 'crash');
    assert.equal(got[0].message, 'kaboom at <project>/a.mjs');
    const stack = got[0].stack.split('\n');
    assert.equal(stack[0], 'Error: kaboom at <project>/a.mjs');
    assert.equal(stack[1].startsWith('    at default (file://~/pkg/src/cli/crash.mjs:1:'), true);
    assert.equal(got[0].stack.split(PARENT).length - 1, 0);
    assert.equal(got[0].stack.split('file://~/pkg/').length - 1, 3); // crash.mjs, run, the bin's top level
    assert.deepEqual(got[0].cleaned, [{ rule: 'home', count: 3 }, { rule: 'project_path', count: 2 }]);
    rmSync(path.join(PKG_HOME, '.code-forge'), { recursive: true, force: true });
  });

  test('a kind the verb reports through its context is carried; an unprintable throw is a crash with a placeholder message', () => {
    const w = fresh();
    assert.equal(cli(['mykind'], w).status, 1);
    assert.equal(cli(['weird'], w).status, 1);
    const got = lines(w.log).map((l) => JSON.parse(l));
    assert.deepEqual(got.map((e) => [e.kind, e.message, e.stack]), [['quota_hit', null, null], ['crash', '<unprintable error>', null]]);
  });

  test('a verb-reported kind that is not a snake_case word is logged as error', async () => {
    const w = fresh();
    const env = { HOME: w.home };
    await logVerbFailure({ verb: 'x', args: [], exit: 2, kind: 'Bad Kind!', env, cwd: w.cwd });
    await logVerbFailure({ verb: 'x', args: [], exit: 2, kind: 'op_locked', env, cwd: w.cwd });
    assert.deepEqual(lines(w.log).map((l) => JSON.parse(l).kind), ['error', 'op_locked']);
  });

  test('the message is capped at 2,048 bytes, never inside a character', () => {
    const w = fresh();
    assert.equal(cli(['long'], w).status, 1);
    const e = JSON.parse(lines(w.log)[0]);
    assert.equal(e.message, '\u00e9'.repeat(1024));
    assert.equal(Buffer.byteLength(e.message), 2048);
  });

  test('sub is the first positional only when it is a known subcommand of the verb', () => {
    const w = fresh();
    cli(['tools', 'install', '--yes'], w);
    cli(['tools', 'my-own-word'], w);
    assert.deepEqual(lines(w.log).map((l) => JSON.parse(l).sub), ['install', null]);
  });

  test('logVerbFailure: exit 0 without a throw writes nothing; a thrown undefined is a crash', async () => {
    const w = fresh();
    const env = { HOME: w.home };
    assert.equal(await logVerbFailure({ verb: 'x', args: [], exit: 0, env, cwd: w.cwd }), false);
    assert.equal(existsSync(w.log), false);
    assert.equal(await logVerbFailure({ verb: 'x', args: [], exit: 1, threw: true, thrown: undefined, env, cwd: w.cwd }), true);
    const e = JSON.parse(lines(w.log)[0]);
    assert.deepEqual([e.kind, e.message, e.stack], ['crash', 'undefined', null]);
  });

  test('the op_timeout kind of a 1Password failure is carried into the entry', () => {
    const w = fresh();
    assert.equal(cli(['optime'], w).status, 1);
    const got = lines(w.log).map((l) => JSON.parse(l));
    assert.equal(got.length, 1);
    assert.deepEqual([got[0].kind, got[0].message], ['op_timeout', 'keys: 1Password did not answer in time; unlock the app and try again']);
  });

  test('a logging failure (the log dir is a file) leaves the exit code and stderr unchanged', () => {
    const w = fresh();
    mkdirSync(path.join(w.home, '.code-forge'), { recursive: true });
    writeFileSync(path.join(w.home, '.code-forge', 'logs'), 'not a dir');
    const r = cli(['fail'], w);
    assert.equal(r.status, 1);
    assert.equal(r.stderr, `progress note\nfail: boom in ${w.home}/notes and ${w.cwd}/src\n`);
    const u = cli(['usage'], w);
    assert.equal(u.status, 2);
  });

  test('CODE_FORGE_NO_ERROR_LOG=1 writes nothing', () => {
    const w = fresh();
    assert.equal(cli(['fail'], w, { CODE_FORGE_NO_ERROR_LOG: '1' }).status, 1);
    assert.equal(cli(['crash'], w, { CODE_FORGE_NO_ERROR_LOG: '1' }).status, 1);
    assert.equal(existsSync(path.join(w.home, '.code-forge')), false);
  });
});

describe('scrub', () => {
  test('home, cwd, slug, email, op:// ref and item ID are each replaced, with counts', () => {
    const s = createScrubber({ home: '/Users/jane', cwd: '/Users/jane/Code/acme-app', slug: 'acme-app' });
    const text = [
      'cwd /Users/jane/Code/acme-app/src/x.mjs',
      'home /Users/jane/.code-forge/user.yml',
      'slug acme-app here, not acme-apps',
      'mail jane.doe@example.co please',
      'ref op://My Vault/Jev key/credential end',
      'item abcdefghijklmnopqrstuvwxyz and 0123456789abcdefghijklmnopq',
    ].join('\n');
    assert.equal(
      s.scrub(text),
      [
        'cwd <project>/src/x.mjs',
        'home ~/.code-forge/user.yml',
        'slug <slug> here, not acme-apps',
        'mail <email> please',
        'ref op://<ref> end',
        'item <item-id> and 0123456789abcdefghijklmnopq',
      ].join('\n'),
    );
    assert.deepEqual(s.counts(), [
      { rule: 'home', count: 1 },
      { rule: 'project_path', count: 1 },
      { rule: 'project_name', count: 1 },
      { rule: 'email', count: 1 },
      { rule: 'op_ref', count: 1 },
      { rule: 'item_id', count: 1 },
    ]);
    assert.equal(describeCounts(s.counts()), 'Cleaned: 1 home path, 1 project path, 1 project name, 1 email, 1 1Password reference, 1 1Password item ID.');
    assert.equal(describeCounts([]), 'Cleaned: nothing needed cleaning.');
  });

  test('paths are replaced only at a path boundary', () => {
    const s = createScrubber({ home: '/Users/bob', cwd: '/w/proj' });
    assert.equal(
      s.scrub('/Users/bob/x /Users/bobby/y "/Users/bob" /w/proj:3 /w/proj2/z /w/proj'),
      '~/x /Users/bobby/y "~" <project>:3 /w/proj2/z <project>',
    );
    assert.deepEqual(s.counts(), [{ rule: 'home', count: 2 }, { rule: 'project_path', count: 2 }]);
    const t = createScrubber({ home: '/Users/bob' });
    assert.equal(t.scrub('see /Users/bob. (/Users/bob/proj) `/Users/bob`, /Users/bob, /Users/bob_x'), 'see ~. (~/proj) `~`, ~, /Users/bob_x');
    assert.deepEqual(t.counts(), [{ rule: 'home', count: 4 }]);
  });

  test('the longest path wins: a cwd above the home dir does not eat the home dir', () => {
    const s = createScrubber({ home: '/Users/bob', cwd: '/Users' });
    assert.equal(s.scrub('/Users/bob/x and /Users/ann'), '~/x and <project>/ann');
    assert.deepEqual(s.counts(), [{ rule: 'home', count: 1 }, { rule: 'project_path', count: 1 }]);
  });

  test('capBytes cuts only at a character start and leaves uncut text alone', () => {
    assert.equal(capBytes('ab\uFFFD', 10), 'ab\uFFFD');
    assert.equal(capBytes('\u00e9\u00e9\u00e9', 3), '\u00e9');
    assert.equal(capBytes('abcdef', 4), 'abcd');
  });

  test('a slug "op" never touches op:// references', () => {
    const s = createScrubber({ slug: 'op' });
    assert.equal(s.scrub('ran op with op://Vault/item/field in op/dir and (op)'), 'ran <slug> with op://<ref> in <slug>/dir and (<slug>)');
    assert.deepEqual(s.counts(), [{ rule: 'project_name', count: 3 }, { rule: 'op_ref', count: 1 }]);
  });

  test('a slug inside a URL path or a temp path is replaced', () => {
    const s = createScrubber({ slug: 'acme' });
    assert.equal(s.scrub('https://github.com/org/acme/issues and /tmp/acme/x and acme://x'), 'https://github.com/org/<slug>/issues and /tmp/<slug>/x and acme://x');
    assert.deepEqual(s.counts(), [{ rule: 'project_name', count: 2 }]);
  });

  test('error kinds: reserved kinds are ignored; clear forgets', () => {
    clearErrorKind();
    reportErrorKind('crash');
    reportErrorKind('usage');
    reportErrorKind('error');
    assert.equal(takeErrorKind(), null);
    reportErrorKind('op_locked');
    clearErrorKind();
    assert.equal(takeErrorKind(), null);
    reportErrorKind('op_locked');
    assert.equal(takeErrorKind(), 'op_locked');
    assert.equal(takeErrorKind(), null);
  });

  test('flag names and subcommand words', () => {
    assert.deepEqual(flagNames(['set', 'jev', '--op', 'op://a/b/c', '--x=1', '-y', '--', '-5']), ['--op', '--x', '-y']);
    assert.deepEqual(flagNames(['--msg', '-Hello']), ['--msg']);
    assert.deepEqual(flagNames(['-pSECRET']), ['-p']);
    assert.deepEqual(flagNames(['--', '--x']), []);
    assert.deepEqual(flagNames(['--a=1', '-b', '--c', '--d']), ['--a', '-b', '--c', '--d']);
    assert.equal(subcommandOf('keys', ['set', 'jev']), 'set');
    assert.equal(subcommandOf('keys', ['--json', 'list']), 'list');
    assert.equal(subcommandOf('review-file', ['test']), null);
    assert.equal(subcommandOf('keys', ['my-secret']), null);
  });
});

describe('fingerprint (B28)', () => {
  test('the same error with other ids, times, quoted names and paths gives the same fp; another kind, verb or status does not', () => {
    const a = { verb: 'keys', sub: 'set', kind: 'op_timeout', message: 'keys: 1Password did not answer after 20000 ms for "Jev key" at ~/a/b.yml (<item-id>) pid 4242 at 2026-10-01T12:00:00Z id 9f8e7d6c5b' };
    const b = { ...a, message: "keys: 1Password did not answer after 60000 ms for 'Other' at <project>/x/y.yml  (<item-id>) pid 9191 at 2026-09-30T08:01:02.123Z id 0a1b2c3d4e5f" };
    assert.equal(normalizeMessage(a.message), 'keys: 1Password did not answer after ms for at ( ) pid at id');
    assert.equal(fingerprint(a), fingerprint(b));
    assert.match(fingerprint(a), /^[0-9a-f]{12}$/);
    assert.notEqual(fingerprint({ ...a, kind: 'op_locked' }), fingerprint(a));
    assert.notEqual(fingerprint({ ...a, verb: 'init' }), fingerprint(a));
    assert.notEqual(fingerprint({ ...a, message: 'keys: the vault is locked' }), fingerprint(a));
    const http = (/** @type {string} */ m) => fingerprint({ verb: 'jev', sub: 'ask', kind: 'error', message: m });
    assert.notEqual(http('jev: HTTP 404 from the API'), http('jev: HTTP 500 from the API'));
    assert.equal(normalizeMessage('E404 at 12:30:01 after 1234.5 ms, 3 tries'), 'E404 at after ms, 3 tries');
  });

  test('fingerprintOf keeps a stored fp and computes one for an entry written before B28', () => {
    const old = { verb: 'fail', sub: null, kind: 'error', message: 'fail: boom in ~/notes and <project>/src' };
    assert.equal(fingerprintOf(/** @type {any} */ (old)), '47df05de5817');
    assert.equal(fingerprintOf(/** @type {any} */ ({ ...old, fp: 'abcdefabcdef' })), 'abcdefabcdef');
  });
});

describe('the hint after a failure (B28)', () => {
  /** @param {string} home */
  function hint(home, over = {}) {
    const chunks = [];
    const stderr = { write: (s) => chunks.push(s) };
    return maybeHint({ entry: { fp: 'abcdefabcdef' }, exit: 1, isTTY: true, env: { HOME: home }, now: new Date('2026-10-01T12:00:00.000Z'), stderr, ...over }).then((shown) => ({ shown, text: chunks.join('') }));
  }

  test('TTY + exit 1: exactly 1 line; the same fp again within 24 h: 0 more; after 24 h: 1 more', async () => {
    const w = fresh();
    const first = await hint(w.home);
    assert.deepEqual(first, { shown: true, text: 'code-forge: this error was saved to the local log. To send it to us: code-forge logs report\n' });
    assert.equal(HINT_LINE.split('\n').length - 1, 1);
    const again = await hint(w.home, { now: new Date('2026-10-02T11:59:59.000Z') });
    assert.deepEqual(again, { shown: false, text: '' });
    const other = await hint(w.home, { entry: { fp: '111111111111' } });
    assert.equal(other.shown, true);
    const later = await hint(w.home, { now: new Date('2026-10-02T12:00:01.000Z') });
    assert.equal(later.shown, true);
    const state = JSON.parse(readFileSync(path.join(w.home, '.code-forge', 'logs', 'hints.json'), 'utf8'));
    assert.deepEqual(state, { abcdefabcdef: '2026-10-02T12:00:01.000Z' });
  });

  test('a malformed fp gives no hint and saves nothing; a non-string state value counts as never shown; a failed save leaves no temp file', async () => {
    const w = fresh();
    assert.deepEqual(await hint(w.home, { entry: { fp: 'NOT-A-FP' } }), { shown: false, text: '' });
    assert.equal(existsSync(path.join(w.home, '.code-forge', 'logs')), false);
    mkdirSync(path.join(w.home, '.code-forge', 'logs'), { recursive: true });
    writeFileSync(path.join(w.home, '.code-forge', 'logs', 'hints.json'), JSON.stringify({ abcdefabcdef: 12345, bad: '2026-10-01T11:00:00.000Z' }));
    assert.equal((await hint(w.home)).shown, true);
    assert.deepEqual(JSON.parse(readFileSync(path.join(w.home, '.code-forge', 'logs', 'hints.json'), 'utf8')), { abcdefabcdef: '2026-10-01T12:00:00.000Z' });
    const v = fresh();
    mkdirSync(path.join(v.home, '.code-forge', 'logs', 'hints.json'), { recursive: true }); // a directory: the rename fails
    assert.equal((await hint(v.home)).shown, true);
    assert.deepEqual(readdirSync(path.join(v.home, '.code-forge', 'logs')), ['hints.json']);
  });

  test('exit 2, non-TTY, agent mode, logging off (no entry): 0 lines', async () => {
    const w = fresh();
    assert.deepEqual(await hint(w.home, { exit: 2 }), { shown: false, text: '' });
    assert.deepEqual(await hint(w.home, { isTTY: false }), { shown: false, text: '' });
    assert.deepEqual(await hint(w.home, { env: { HOME: w.home, CLAUDECODE: '1' } }), { shown: false, text: '' });
    assert.deepEqual(await hint(w.home, { entry: null }), { shown: false, text: '' });
    assert.equal(existsSync(path.join(w.home, '.code-forge', 'logs', 'hints.json')), false);
  });

  test('the agent variables are the ones install/agent-env.mjs checks', async () => {
    const { AGENT_ENV_VARS } = await import('../../src/util/error-hint.mjs');
    const install = await import('../../src/install/agent-env.mjs');
    assert.deepEqual(AGENT_ENV_VARS, install.AGENT_ENV_VARS);
  });
});

describe('size cap', () => {
  test('an over-1MB file is cut to its newest half; the line just written is kept', async () => {
    const file = path.join(PARENT, 'cap', 'errors.jsonl');
    mkdirSync(path.dirname(file), { recursive: true });
    const old = Array.from({ length: 1048 }, (_, i) => `${String(i).padStart(4, '0')}${'x'.repeat(995)}`);
    writeFileSync(file, `${old.join('\n')}\n`); // 1,048,000 bytes: under 1 MB
    const newest = `NEW!${'y'.repeat(995)}`;
    await appendLine(file, newest); // 1,049,000 bytes: over, budget 524,500 bytes
    const kept = lines(file);
    assert.equal(kept.length, 524);
    assert.equal(kept[0].slice(0, 4), '0525');
    assert.equal(kept[522].slice(0, 4), '1047');
    assert.equal(kept[523], newest);
    assert.equal(readFileSync(file).length, 524_000);
    assert.deepEqual(readdirSync(path.dirname(file)), ['errors.jsonl']);
  });
});
