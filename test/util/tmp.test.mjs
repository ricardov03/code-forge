import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { chmodSync, existsSync, mkdirSync, readdirSync, readFileSync, realpathSync, rmSync, symlinkSync, utimesSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { after, before, mock, test } from 'node:test';
import { isAlive, readStartTime, registerPid, UNKNOWN_START_TIME } from '../../src/util/reaper.mjs';
import { ownerAlive, ownerState, pidsDir, runRoot, sweepRoots, tmpBase, untrustedReason } from '../../src/util/tmp.mjs';

let PARENT = '';

before(async () => {
  PARENT = realpathSync(await mkdtemp(path.join(os.tmpdir(), 'code-forge-tmp-')));
});

after(async () => {
  if (PARENT) await rm(PARENT, { recursive: true, force: true });
});

/** A pid that existed and is now dead. */
async function deadPid() {
  const child = spawn(process.execPath, ['-e', ''], { stdio: 'ignore' });
  await once(child, 'exit');
  await sleep(50);
  return /** @type {number} */ (child.pid);
}

test('runRoot(runId) with no tmp.root is <os.tmpdir()>/code-forge/<runId>, owned by this process', () => {
  const dir = runRoot('b01-default');
  try {
    assert.equal(dir, path.join(realpathSync(os.tmpdir()), 'code-forge', 'b01-default'));
    const owner = JSON.parse(readFileSync(path.join(dir, 'owner.json'), 'utf8'));
    assert.equal(owner.pid, process.pid);
    assert.equal(owner.start_time, readStartTime(process.pid));
    assert.equal(pidsDir(dir), path.join(dir, 'pids'));
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('runRoot(runId, {root}) lives directly under tmp.root', () => {
  const base = path.join(PARENT, 'configured');
  assert.equal(runRoot('b01-run', { root: base }), path.join(base, 'b01-run'));
  assert.equal(tmpBase(base), base);
});

test('owner.json is written atomically: after a create and a rewrite the root holds exactly owner.json, no temp file', () => {
  const base = path.join(PARENT, 'atomic');
  const dir = runRoot('atomic', { root: base });
  assert.deepEqual(readdirSync(dir), ['owner.json']);
  const first = JSON.parse(readFileSync(path.join(dir, 'owner.json'), 'utf8'));

  assert.equal(runRoot('atomic', { root: base }), dir);
  assert.deepEqual(readdirSync(dir), ['owner.json']);
  const second = JSON.parse(readFileSync(path.join(dir, 'owner.json'), 'utf8'));
  assert.deepEqual([first.pid, second.pid], [process.pid, process.pid]);
  assert.equal(ownerState(dir), 'alive');
});

test('runRoot refuses a run id that is not one plain path segment, and a relative tmp.root', () => {
  for (const bad of ['', '..', '.', 'a/b', '../x', 'a\\b', '-x', '.reaping-0a1b2c']) {
    assert.throws(() => runRoot(bad, { root: PARENT }), { name: 'TypeError', message: /runId must be one path segment/ });
  }
  assert.throws(() => runRoot('ok', { root: 'relative/dir' }), { name: 'TypeError', message: /tmp.root must be an absolute path/ });
});

test('sweepRoots removes a sibling root whose owner is dead and keeps a live one', async () => {
  const base = path.join(PARENT, 'sweep');
  const live = runRoot('live', { root: base });
  const dead = path.join(base, 'dead');
  mkdirSync(path.join(dead, 'pids'), { recursive: true });
  writeFileSync(path.join(dead, 'owner.json'), JSON.stringify({ pid: await deadPid(), start_time: 'unknown' }));

  const result = await sweepRoots({ root: base, graceMs: 200 });

  assert.deepEqual(result, { removed: [dead], kept: [live], unknown: [] });
  assert.equal(existsSync(dead), false);
  assert.equal(existsSync(live), true);
});

test('a root whose owner pid is alive but with another start time counts as dead; no owner file is unknown and kept', async () => {
  const base = path.join(PARENT, 'reused');
  const reused = path.join(base, 'reused');
  mkdirSync(reused, { recursive: true });
  writeFileSync(path.join(reused, 'owner.json'), JSON.stringify({ pid: process.pid, start_time: 'Mon Jan 1 00:00:00 2001' }));
  const ownerless = path.join(base, 'ownerless');
  mkdirSync(ownerless);

  assert.equal(ownerAlive(reused), false);
  assert.equal(ownerAlive(ownerless), null);
  const result = await sweepRoots({ root: base, prefix: 'o', graceMs: 200 });
  assert.deepEqual(result, { removed: [], kept: [], unknown: [ownerless] });
  assert.equal(existsSync(reused), true, 'the prefix filter must leave "reused" alone');
});

test('an owner that comes alive between the check and the reap: the root is kept, its live child survives, no tombstone is left', { timeout: 8000 }, async () => {
  const base = path.join(PARENT, 'race');
  const root = path.join(base, 'r');
  mkdirSync(path.join(root, 'pids'), { recursive: true, mode: 0o700 });
  const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { detached: true, stdio: 'ignore' });
  const pid = /** @type {number} */ (child.pid);
  try {
    const started = readStartTime(pid);
    assert.notEqual(started, null);
    writeFileSync(path.join(root, 'owner.json'), `${JSON.stringify({ pid, start_time: started })}\n`);
    const entry = registerPid(path.join(root, 'pids'), pid, 'node');
    assert.notEqual(JSON.parse(readFileSync(entry, 'utf8')).start_time, UNKNOWN_START_TIME);

    // First liveness probe of the owner says "gone" (the check), every later one is real (the re-check).
    const original = process.kill.bind(process);
    let probes = 0;
    const spy = mock.method(process, 'kill', (/** @type {number} */ target, /** @type {any} */ signal) => {
      if (target === pid && signal === 0 && probes++ === 0) throw Object.assign(new Error('ESRCH'), { code: 'ESRCH' });
      return original(target, signal);
    });
    let result;
    try {
      result = await sweepRoots({ root: base, graceMs: 200 });
    } finally {
      spy.mock.restore();
    }

    assert.equal(probes, 2, 'one probe for the check, one for the re-check on the tombstone');
    assert.deepEqual(result, { removed: [], kept: [root], unknown: [] });
    assert.equal(isAlive(pid), true);
    assert.deepEqual(readdirSync(base), ['r']);
    assert.deepEqual(readdirSync(root).sort(), ['owner.json', 'pids']);
    assert.equal(existsSync(entry), true);
  } finally {
    process.kill(-pid, 'SIGKILL');
  }
});

/** A root with a dead owner, planted by hand. @param {string} dir */
async function plantDeadRoot(dir) {
  mkdirSync(path.join(dir, 'pids'), { recursive: true, mode: 0o700 });
  writeFileSync(path.join(dir, 'owner.json'), JSON.stringify({ pid: await deadPid(), start_time: 'unknown' }));
}

test('untrusted roots are never swept: group/other-writable, a symlink, or a foreign owner (mocked)', async () => {
  const base = path.join(PARENT, 'untrusted');
  mkdirSync(base, { mode: 0o700 });
  const writable = path.join(base, 'writable');
  await plantDeadRoot(writable);
  chmodSync(writable, 0o777);
  const target = path.join(PARENT, 'link-target');
  await plantDeadRoot(target);
  const link = path.join(base, 'link');
  symlinkSync(target, link);

  const result = await sweepRoots({ root: base, graceMs: 200 });
  assert.deepEqual(result, { removed: [], kept: [], unknown: [link, writable] });
  assert.equal(existsSync(path.join(writable, 'owner.json')), true);
  assert.equal(existsSync(path.join(target, 'owner.json')), true);

  // Foreign owner: a real dead root that would be removed, but getuid() says it is not ours.
  const foreign = path.join(PARENT, 'foreign-base');
  mkdirSync(foreign, { mode: 0o700 });
  await plantDeadRoot(path.join(foreign, 'r'));
  const realUid = process.getuid?.() ?? 0;
  const spy = mock.method(process, 'getuid', () => realUid + 1);
  try {
    assert.equal(untrustedReason(path.join(foreign, 'r')), 'foreign owner');
    assert.deepEqual(await sweepRoots({ root: foreign, graceMs: 200 }), { removed: [], kept: [], unknown: [path.join(foreign, 'r')] });
  } finally {
    spy.mock.restore();
  }
  assert.equal(existsSync(path.join(foreign, 'r')), true);
});

test('runRoot throws on an untrusted (group/other-writable) base or root', () => {
  const base = path.join(PARENT, 'open-base');
  mkdirSync(base);
  chmodSync(base, 0o777);
  assert.throws(() => runRoot('x', { root: base }), { name: 'Error', message: /untrusted tmp base \(group\/other writable\)/ });
  const okBase = path.join(PARENT, 'ok-base');
  mkdirSync(path.join(okBase, 'open-root'), { recursive: true, mode: 0o700 });
  chmodSync(path.join(okBase, 'open-root'), 0o757);
  assert.throws(() => runRoot('open-root', { root: okBase }), { name: 'Error', message: /untrusted run root \(group\/other writable\)/ });
});

test('an ownerless root is kept while recent (a fresh pids/ entry counts as activity) and removed once older than ownerlessGraceMs', async () => {
  const base = path.join(PARENT, 'ownerless-grace');
  const fresh = path.join(base, 'fresh');
  const old = path.join(base, 'old');
  const busy = path.join(base, 'busy');
  mkdirSync(fresh, { recursive: true, mode: 0o700 });
  mkdirSync(old, { mode: 0o700 });
  mkdirSync(path.join(busy, 'pids'), { recursive: true, mode: 0o700 });
  writeFileSync(path.join(busy, 'pids', '99999.json'), '{}\n');
  const hourAgo = new Date(Date.now() - 60 * 60 * 1000);
  utimesSync(old, hourAgo, hourAgo);
  // Only the pids/ entry of `busy` is recent: the directory itself and pids/ are an hour old.
  utimesSync(path.join(busy, 'pids'), hourAgo, hourAgo);
  utimesSync(busy, hourAgo, hourAgo);

  const result = await sweepRoots({ root: base, graceMs: 200, ownerlessGraceMs: 60 * 1000 });
  assert.deepEqual(result, { removed: [old], kept: [], unknown: [busy, fresh] });
  assert.equal(existsSync(fresh), true);
  assert.equal(existsSync(busy), true);
  assert.equal(existsSync(old), false);
});

test('an owner.json that exists but is truncated JSON or has an invalid pid is `invalid`: kept as unknown even when old', async () => {
  const base = path.join(PARENT, 'corrupt-owner');
  const truncated = path.join(base, 'truncated');
  const badPid = path.join(base, 'bad-pid');
  mkdirSync(truncated, { recursive: true, mode: 0o700 });
  mkdirSync(badPid, { mode: 0o700 });
  writeFileSync(path.join(truncated, 'owner.json'), '{"pid":');
  writeFileSync(path.join(badPid, 'owner.json'), '{"pid":"x","start_time":"unknown"}\n');
  const hourAgo = new Date(Date.now() - 60 * 60 * 1000);
  for (const dir of [truncated, badPid]) {
    utimesSync(path.join(dir, 'owner.json'), hourAgo, hourAgo);
    utimesSync(dir, hourAgo, hourAgo);
  }

  assert.deepEqual([ownerState(truncated), ownerState(badPid), ownerAlive(truncated)], ['invalid', 'invalid', null]);
  const result = await sweepRoots({ root: base, graceMs: 200, ownerlessGraceMs: 60 * 1000 });
  assert.deepEqual(result, { removed: [], kept: [], unknown: [badPid, truncated] });
  assert.equal(existsSync(path.join(truncated, 'owner.json')), true);
  assert.equal(existsSync(path.join(badPid, 'owner.json')), true);
});

// ---- B19: the lazy root honours `tmp.root` and is removed on a normal exit ----

const REPO_ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), '..', '..');
const BIN = path.join(REPO_ROOT, 'bin', 'code-forge.mjs');
const TMP_MODULE = path.join(REPO_ROOT, 'src', 'util', 'tmp.mjs');
const REAPER_MODULE = path.join(REPO_ROOT, 'src', 'util', 'reaper.mjs');

/** `process.env` minus every inherited `GIT_*`, no system/global git config, plus `extra`. */
function childEnv(extra) {
  /** @type {Record<string, string>} */
  const env = { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull };
  for (const [k, v] of Object.entries(process.env)) if (!k.startsWith('GIT_') && k !== 'CODE_FORGE_TMP_ROOT' && v !== undefined) env[k] = v;
  return { ...env, ...extra };
}

/** @param {string[]} argv @param {{cwd: string, env: Record<string, string>}} opts */
async function runNode(argv, opts) {
  const child = spawn(process.execPath, argv, { cwd: opts.cwd, env: opts.env, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', (d) => (stdout += d));
  child.stderr.on('data', (d) => (stderr += d));
  const [code] = await once(child, 'exit');
  return { code, stdout, stderr };
}

/** A fresh case dir with a git repo, its own HOME and TMPDIR. @param {string} name */
async function cliCase(name) {
  const dir = path.join(PARENT, name);
  const repo = path.join(dir, 'repo');
  const home = path.join(dir, 'home');
  const tmpdir = path.join(dir, 'tmpdir');
  for (const d of [repo, home, tmpdir]) mkdirSync(d, { recursive: true, mode: 0o700 });
  const env = childEnv({ HOME: home, TMPDIR: tmpdir });
  for (const args of [['init', '-q'], ['-c', 'user.name=Fake', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-q', '--allow-empty', '-m', 'base']]) {
    const res = await runNode(['-e', `require('node:child_process').execFileSync('git', ${JSON.stringify(args)}, {stdio: 'ignore'})`], { cwd: repo, env });
    assert.equal(res.code, 0, res.stderr);
  }
  return { dir, repo, env, defaultBase: path.join(tmpdir, 'code-forge') };
}

test('B19: a CLI verb run with a config whose tmp.root is a temp dir leaves 0 proc-* roots under <os.tmpdir()>/code-forge and 0 roots under tmp.root after exit', async () => {
  const c = await cliCase('cli-tmp-root');
  const configured = path.join(c.dir, 'configured-root');
  writeFileSync(path.join(c.repo, '.code-forge.yml'), `tmp:\n  root: ${JSON.stringify(configured)}\n`);
  const res = await runNode([BIN, 'gates', 'scope', '--cwd', c.repo, '--base', 'HEAD'], { cwd: c.repo, env: c.env });
  assert.equal(res.code, 0, res.stderr);
  assert.deepEqual(JSON.parse(res.stdout).all, ['.code-forge.yml']); // the verb ran git through exec
  assert.equal(existsSync(c.defaultBase), false); // nothing under <os.tmpdir()>/code-forge at all
  assert.equal(existsSync(configured), true); // the lazy root was created under tmp.root …
  assert.deepEqual(readdirSync(configured), []); // … and removed on exit
});

test('B19: without a tmp.root the same verb uses <os.tmpdir()>/code-forge and still leaves 0 proc-* roots there after exit', async () => {
  const c = await cliCase('cli-default-root');
  const res = await runNode([BIN, 'gates', 'scope', '--cwd', c.repo, '--base', 'HEAD'], { cwd: c.repo, env: c.env });
  assert.equal(res.code, 0, res.stderr);
  assert.equal(existsSync(c.defaultBase), true);
  assert.deepEqual(readdirSync(c.defaultBase), []);
});

test('B19: tmpBase() honours an absolute CODE_FORGE_TMP_ROOT and ignores a relative one; an explicit root still wins', () => {
  const before = process.env.CODE_FORGE_TMP_ROOT;
  try {
    process.env.CODE_FORGE_TMP_ROOT = path.join(PARENT, 'env-base');
    assert.equal(tmpBase(), path.join(PARENT, 'env-base'));
    assert.equal(tmpBase(path.join(PARENT, 'explicit')), path.join(PARENT, 'explicit'));
    process.env.CODE_FORGE_TMP_ROOT = 'relative/base';
    assert.equal(tmpBase(), path.join(os.tmpdir(), 'code-forge'));
  } finally {
    if (before === undefined) delete process.env.CODE_FORGE_TMP_ROOT;
    else process.env.CODE_FORGE_TMP_ROOT = before;
  }
});

test('B19: a lazy root whose pids/ holds a LIVE entry is kept on exit; one with only a dead entry is removed', async () => {
  const base = path.join(PARENT, 'lazy-exit');
  const dead = await deadPid();
  const script = (pid) =>
    `const t = await import(${JSON.stringify(TMP_MODULE)}); const r = await import(${JSON.stringify(REAPER_MODULE)});` +
    ` const root = t.currentRunRoot(); r.registerPid(t.pidsDir(), ${pid}, 'fake'); console.log(root);`;
  const env = childEnv({ CODE_FORGE_TMP_ROOT: base });
  const live = await runNode(['--input-type=module', '-e', script(process.pid)], { cwd: PARENT, env });
  const gone = await runNode(['--input-type=module', '-e', script(dead)], { cwd: PARENT, env });
  assert.equal(live.code, 0, live.stderr);
  assert.equal(gone.code, 0, gone.stderr);
  const liveRoot = live.stdout.trim();
  assert.equal(path.dirname(liveRoot), realpathSync(base));
  assert.deepEqual(readdirSync(base), [path.basename(liveRoot)]);
  assert.deepEqual(readdirSync(path.join(liveRoot, 'pids')), [`${process.pid}.json`]);
});

test('B19 R1: releaseLazyRoot never throws — an injected listEntries or rmSync failure returns false and keeps the root', async () => {
  const { currentRunRoot, releaseLazyRoot, setRunRoot } = await import('../../src/util/tmp.mjs');
  const before = process.env.CODE_FORGE_TMP_ROOT;
  process.env.CODE_FORGE_TMP_ROOT = path.join(PARENT, 'lazy-inproc');
  setRunRoot(null);
  try {
    const root = currentRunRoot();
    assert.equal(path.dirname(root), realpathSync(path.join(PARENT, 'lazy-inproc')));
    const boom = () => {
      throw new Error('EACCES fake');
    };
    assert.equal(releaseLazyRoot({ listEntries: boom }), false);
    assert.equal(releaseLazyRoot({ rmSync: boom }), false);
    assert.equal(existsSync(root), true);
    assert.equal(releaseLazyRoot(), true);
    assert.equal(existsSync(root), false);
    assert.equal(releaseLazyRoot(), false); // nothing lazy left
  } finally {
    setRunRoot(null);
    if (before === undefined) delete process.env.CODE_FORGE_TMP_ROOT;
    else process.env.CODE_FORGE_TMP_ROOT = before;
  }
});

test('B19 R1: applyTmpRoot sets CODE_FORGE_TMP_ROOT exactly for 5 cases (absolute, env already set, relative, missing config, ok:false)', async () => {
  const { applyTmpRoot } = await import('../../bin/code-forge.mjs');
  const dirFor = (/** @type {string} */ name, /** @type {string | null} */ yml) => {
    const dir = path.join(PARENT, 'apply', name);
    mkdirSync(dir, { recursive: true });
    if (yml !== null) writeFileSync(path.join(dir, '.code-forge.yml'), yml);
    return dir;
  };
  const abs = path.join(PARENT, 'apply-abs-root');
  const absDir = dirFor('absolute', `tmp:\n  root: ${JSON.stringify(abs)}\n`);
  /** @type {Record<string, string>} */
  const e1 = {};
  assert.equal(await applyTmpRoot({ env: e1, cwd: absDir }), abs);
  assert.deepEqual(e1, { CODE_FORGE_TMP_ROOT: abs });

  const e2 = { CODE_FORGE_TMP_ROOT: '/already/set' };
  assert.equal(await applyTmpRoot({ env: e2, cwd: absDir }), '/already/set');
  assert.deepEqual(e2, { CODE_FORGE_TMP_ROOT: '/already/set' });

  const relDir = dirFor('relative', 'tmp:\n  root: scratch/tmp\n');
  /** @type {Record<string, string>} */
  const e3 = {};
  assert.equal(await applyTmpRoot({ env: e3, cwd: relDir }), path.join(relDir, 'scratch', 'tmp'));
  assert.deepEqual(e3, { CODE_FORGE_TMP_ROOT: path.join(relDir, 'scratch', 'tmp') });

  /** @type {Record<string, string>} */
  const e4 = {};
  assert.equal(await applyTmpRoot({ env: e4, cwd: dirFor('missing', null) }), undefined);
  assert.deepEqual(e4, {});

  /** @type {Record<string, string>} */
  const e5 = {};
  assert.equal(await applyTmpRoot({ env: e5, cwd: dirFor('broken', 'tmp: [unclosed\n') }), undefined);
  assert.deepEqual(e5, {});
});
