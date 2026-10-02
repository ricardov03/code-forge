// B37 breadcrumbs: the router keeps the names of the last 5 commands (verb + known subcommand,
// never a flag or a value) in `~/.code-forge/logs/breadcrumbs.json`, and an error line names the
// commands before it. Router runs use a hermetic copy of the package (bin, src/util and a few fake
// verbs) spawned with HOME and cwd inside one per-file temp parent, removed in `after()`.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, describe, test } from 'node:test';

const PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-crumbs-')));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { BREADCRUMB_MAX, noteCommand, previousCommands, readBreadcrumbs, resetBreadcrumbs } = await import('../../src/util/breadcrumbs.mjs');
const { noteVerb } = await import('../../src/util/error-log.mjs');

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const PKG = path.join(PARENT, 'pkg');
mkdirSync(path.join(PKG, 'src', 'cli'), { recursive: true });
cpSync(path.join(ROOT, 'bin'), path.join(PKG, 'bin'), { recursive: true });
cpSync(path.join(ROOT, 'src', 'util'), path.join(PKG, 'src', 'util'), { recursive: true });
cpSync(path.join(ROOT, 'package.json'), path.join(PKG, 'package.json'));
for (const name of ['keys', 'init', 'tools', 'plan']) writeFileSync(path.join(PKG, 'src', 'cli', `${name}.mjs`), 'export default async function () { return 0; }');
writeFileSync(path.join(PKG, 'src', 'cli', 'fail.mjs'), "export default async function () { process.stderr.write('fail: boom\\n'); return 1; }");

const FAKE_VALUE = 'FAKE-flag-value-7731';
const FAKE_POS = 'fake-positional-name';

let n = 0;
function fresh() {
  n += 1;
  const home = path.join(PARENT, `h${n}`);
  const cwd = path.join(home, 'proj');
  mkdirSync(cwd, { recursive: true });
  const logs = path.join(home, '.code-forge', 'logs');
  return { home, cwd, crumbs: path.join(logs, 'breadcrumbs.json'), log: path.join(logs, 'errors.jsonl') };
}

/** @param {string[]} args @param {{home: string, cwd: string}} w @param {Record<string, string>} [extra] */
function cli(args, w, extra = {}) {
  return spawnSync(process.execPath, [path.join(PKG, 'bin', 'code-forge.mjs'), ...args], { cwd: w.cwd, env: { PATH: process.env.PATH ?? '', HOME: w.home, ...extra }, encoding: 'utf8' });
}

describe('breadcrumbs through the router', () => {
  test('a ring of the last 5 commands, oldest first; names and known subcommands only (0 flags, 0 values); 0600', () => {
    const w = fresh();
    cli(['init', '--yes'], w);
    cli(['keys', 'set', FAKE_POS, '--op', FAKE_VALUE], w);
    cli(['keys', 'test', `--ref=${FAKE_VALUE}`], w);
    cli(['tools', FAKE_POS], w);
    cli(['plan', 'check', '-q'], w);
    cli(['keys', '--json', 'list'], w);
    const text = readFileSync(w.crumbs, 'utf8');
    assert.deepEqual(JSON.parse(text), ['keys set', 'keys test', 'tools', 'plan check', 'keys list']);
    assert.equal(text.split(FAKE_VALUE).length - 1, 0);
    assert.equal(text.split(FAKE_POS).length - 1, 0);
    assert.equal(text.split('-').length - 1, 0); // no flag of any kind
    assert.equal(statSync(w.crumbs).mode & 0o777, 0o600);
    assert.deepEqual(readdirSync(path.dirname(w.crumbs)), ['breadcrumbs.json']); // no temp file left
  });

  test('an error line names the commands before it (up to 5, oldest first), never itself', () => {
    const w = fresh();
    cli(['init'], w);
    cli(['keys', 'set', FAKE_POS, '--op', FAKE_VALUE], w);
    assert.equal(cli(['fail', '--x', FAKE_VALUE], w).status, 1);
    const [e] = readFileSync(w.log, 'utf8').split('\n').filter((l) => l.length > 0).map((l) => JSON.parse(l));
    assert.deepEqual(e.before, ['init', 'keys set']);
    assert.deepEqual(JSON.parse(readFileSync(w.crumbs, 'utf8')), ['init', 'keys set', 'fail']);
    for (let i = 0; i < 6; i += 1) cli(['init'], w);
    cli(['fail'], w);
    const last = readFileSync(w.log, 'utf8').split('\n').filter((l) => l.length > 0).map((l) => JSON.parse(l)).at(-1);
    assert.deepEqual(last.before, ['init', 'init', 'init', 'init', 'init']);
  });

  test('CODE_FORGE_NO_ERROR_LOG=1 writes nothing and the CLI runs as usual', () => {
    const w = fresh();
    const ok = cli(['init'], w, { CODE_FORGE_NO_ERROR_LOG: '1' });
    assert.deepEqual([ok.status, ok.stderr], [0, '']);
    assert.equal(cli(['fail'], w, { CODE_FORGE_NO_ERROR_LOG: '1' }).status, 1);
    assert.equal(existsSync(path.join(w.home, '.code-forge')), false);
  });

  test('an unknown verb is never recorded', () => {
    const w = fresh();
    assert.equal(cli([FAKE_POS], w).status, 1);
    assert.equal(existsSync(w.crumbs), false);
  });
});

describe('the breadcrumb file', () => {
  test('a broken or tampered file is read as [] or only its command-shaped entries; the ring holds 5', async () => {
    const w = fresh();
    mkdirSync(path.dirname(w.crumbs), { recursive: true });
    writeFileSync(w.crumbs, '{not json');
    assert.deepEqual(await readBreadcrumbs(w.crumbs), []);
    writeFileSync(w.crumbs, JSON.stringify(['init', `keys ${FAKE_VALUE}`, '--op', 42, 'keys test', 'a b c']));
    assert.deepEqual(await readBreadcrumbs(w.crumbs), ['init', 'keys test']);
    resetBreadcrumbs();
    assert.equal(await noteCommand({ verb: 'plan', sub: null, env: { HOME: w.home } }), true);
    assert.deepEqual(previousCommands(), ['init', 'keys test']);
    for (const v of ['a', 'b', 'c', 'd']) await noteCommand({ verb: v, sub: null, env: { HOME: w.home } });
    assert.equal(BREADCRUMB_MAX, 5);
    assert.deepEqual(await readBreadcrumbs(w.crumbs), ['plan', 'a', 'b', 'c', 'd']);
    assert.deepEqual(previousCommands(), ['keys test', 'plan', 'a', 'b', 'c']);
    resetBreadcrumbs();
  });

  test('noteCommand: a sub word not in the verb\'s table is dropped; the opt-out is checked here; an existing logs folder becomes 0700', async () => {
    const w = fresh();
    const logs = path.dirname(w.crumbs);
    mkdirSync(logs, { recursive: true });
    chmodSync(logs, 0o755);
    await noteCommand({ verb: 'keys', sub: FAKE_POS, env: { HOME: w.home } });
    await noteCommand({ verb: 'init', sub: 'set', env: { HOME: w.home } });
    await noteCommand({ verb: 'keys', sub: 'test', env: { HOME: w.home } });
    assert.deepEqual(JSON.parse(readFileSync(w.crumbs, 'utf8')), ['keys', 'init', 'keys test']);
    assert.equal(statSync(logs).mode & 0o777, 0o700);
    const v = fresh();
    assert.equal(await noteCommand({ verb: 'init', sub: null, env: { HOME: v.home, CODE_FORGE_NO_ERROR_LOG: '1' } }), false);
    assert.equal(existsSync(path.join(v.home, '.code-forge')), false);
    const p = noteVerb('init', [], { HOME: v.home, CODE_FORGE_NO_ERROR_LOG: '1' });
    assert.equal(p instanceof Promise, true);
    assert.equal(await p, false);
    assert.equal(existsSync(path.join(v.home, '.code-forge')), false);
    resetBreadcrumbs();
  });
});
