import assert from 'node:assert/strict';
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';

// A developer's own CODEX_HOME must not leak in: the tests pin the real home to $HOME/.codex.
delete process.env.CODEX_HOME;
// HOME is pinned to a temp dir by the isolate preload before this module loads; src is imported after.
// Nothing here writes under os.homedir(): the "real" Codex homes with an auth.json are fakes under PARENT.
const { FORBIDDEN, renderForCodex } = await import('../../src/util/forbidden.mjs');
const { buildCodexArgv, SANDBOX_TMP_EXCLUSIONS } = await import('../../src/engines/builders/codex.mjs');
const { isInsideRealCodexHome, isSessionCodexHome, prepareCodexHome, realCodexHome, removeCodexHome, renderCodexRules, sessionCodexHomesDir } = await import('../../src/engines/codex-home.mjs');
const { currentRunRoot } = await import('../../src/util/tmp.mjs');

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'b4-1-codex-home-'));
/** @type {string[]} */
const built_homes = [];
after(() => {
  for (const h of built_homes) removeCodexHome(h);
  rmSync(PARENT, { recursive: true, force: true });
});

// The expected rule count comes from the renderer itself (fix round 3), pinned to 46 separately.
const EXPECTED_RULES = renderCodexRules(renderForCodex(FORBIDDEN)).count;
test('the default forbidden list renders exactly 46 Codex rules (pin)', () => {
  assert.equal(EXPECTED_RULES, 46);
});

/** A session home path where `prepareCodexHome` allows one: strictly inside <run root>/codex-homes/; removed in after(). */
const sessionPath = (/** @type {string} */ name) => {
  const p = path.join(sessionCodexHomesDir(), `t-${name}`);
  built_homes.push(p);
  return p;
};

const coder = (/** @type {Record<string, any>} */ extra = {}) => {
  const built = /** @type {any} */ (buildCodexArgv({ role: 'coder', model: 'gpt-6-astra', promptPath: '/p/brief.md', cwd: '/c', outPath: '/o/out.json', ...extra }));
  built_homes.push(built.env.CODEX_HOME);
  return built;
};

test('coder argv: exact sandbox elements — workspace-write then both tmp exclusions, once each', () => {
  const built = coder();
  assert.deepEqual([...SANDBOX_TMP_EXCLUSIONS], ['-c', 'sandbox_workspace_write.exclude_tmpdir_env_var=true', '-c', 'sandbox_workspace_write.exclude_slash_tmp=true']);
  assert.deepEqual(built.argv.slice(0, 11), [
    'codex', 'exec', '-m', 'gpt-6-astra', '-s', 'workspace-write',
    '-c', 'sandbox_workspace_write.exclude_tmpdir_env_var=true',
    '-c', 'sandbox_workspace_write.exclude_slash_tmp=true',
    '--approve-for-me',
  ]);
  assert.equal(built.argv.filter((/** @type {string} */ t) => t.startsWith('sandbox_workspace_write.')).length, 2);
});

test('coder build: env CODEX_HOME is a fresh dir under the run temp root; rules/code-forge.rules has the rendered count of rules, file 0444, dirs 0700', () => {
  const built = coder();
  const home = built.env.CODEX_HOME;
  assert.equal(path.relative(currentRunRoot(), home).startsWith('..'), false, home);
  assert.equal(path.basename(path.dirname(home)), 'codex-homes');
  assert.equal(isSessionCodexHome(home), true);
  const rulesPath = path.join(home, 'rules', 'code-forge.rules');
  assert.equal(built.rulesFile.path, rulesPath);
  assert.equal(built.rulesFile.count, EXPECTED_RULES);
  const text = readFileSync(rulesPath, 'utf8');
  assert.equal(text.split('\n').filter((l) => l.startsWith('prefix_rule(')).length, EXPECTED_RULES);
  assert.equal(text.split('\n').filter((l) => l.includes('decision="forbidden"')).length, EXPECTED_RULES);
  assert.equal(statSync(rulesPath).mode & 0o777, 0o444);
  assert.equal(statSync(path.join(home, 'rules')).mode & 0o777, 0o700);
  assert.equal(statSync(home).mode & 0o777, 0o700);
  // removable by a plain recursive rm (the run-root sweep relies on it)
  removeCodexHome(home);
  assert.equal(existsSync(home), false);
});

test('coder build never points at the real Codex home: not in env, not in argv; a codexHome inside it, or inside the cwd, throws', () => {
  const real = realCodexHome();
  assert.equal(real, path.join(os.homedir(), '.codex'));
  const built = coder();
  assert.equal(isInsideRealCodexHome(built.env.CODEX_HOME), false);
  assert.equal(built.argv.filter((/** @type {string} */ t) => t.includes(real)).length, 0);
  for (const bad of [real, path.join(real, 'rules'), path.join(real, 'x', 'y')]) {
    assert.throws(() => buildCodexArgv({ role: 'coder', model: 'm', promptPath: '/p', cwd: '/c', codexHome: bad }), { message: 'prepareCodexHome: codexHome must not be the real Codex home or inside it' });
  }
  assert.equal(existsSync(path.join(real, 'rules', 'code-forge.rules')), false);
  const cwd = path.join(PARENT, 'project');
  mkdirSync(cwd);
  assert.throws(
    () => buildCodexArgv({ role: 'coder', model: 'm', promptPath: '/p', cwd, codexHome: path.join(cwd, '.cf', 'home') }),
    { message: 'buildCodexArgv: the session CODEX_HOME must not be inside the coder cwd (the sandbox can write there)' },
  );
  assert.equal(existsSync(path.join(cwd, '.cf')), false);
  assert.throws(() => buildCodexArgv({ role: 'coder', model: 'm', promptPath: '/p', cwd: '/c', codexHome: 'relative/home' }), { name: 'TypeError', message: 'buildCodexArgv: codexHome must be an absolute path' });
  assert.throws(() => prepareCodexHome('relative/home', renderForCodex(FORBIDDEN)), { name: 'TypeError', message: 'prepareCodexHome: codexHome must be an absolute path' });
});

test('an ANCESTOR of the real home is refused too: $HOME, /, the run root and a dir beside codex-homes throw, and no file is created', () => {
  const home = /** @type {string} */ (process.env.HOME);
  assert.equal(path.relative(home, realCodexHome()), '.codex'); // $HOME is the real home's parent
  const outside = [home, '/', currentRunRoot(), sessionCodexHomesDir(), path.join(PARENT, 'beside')];
  let thrown = 0;
  for (const bad of outside) {
    assert.throws(() => buildCodexArgv({ role: 'coder', model: 'm', promptPath: '/p', cwd: '/c', codexHome: bad }), { message: 'prepareCodexHome: codexHome must be strictly inside <run root>/codex-homes/' }, bad);
    assert.throws(() => prepareCodexHome(bad, renderForCodex(FORBIDDEN)), { message: 'prepareCodexHome: codexHome must be strictly inside <run root>/codex-homes/' }, bad);
    thrown += 2;
    assert.equal(existsSync(path.join(bad, 'rules', 'code-forge.rules')), false, bad);
    assert.equal(existsSync(path.join(bad, 'auth.json')), false, bad);
  }
  assert.equal(thrown, 10);
  assert.equal(existsSync(path.join(home, 'rules')), false);
  assert.equal(existsSync('/rules'), false);
  assert.equal(existsSync(path.join(PARENT, 'beside')), false);
});

test('cwd containment is realpath-based: a /var cwd with a /private/var home inside it throws, and a symlinked cwd too; nothing is created', () => {
  // A symlink to the project: the cwd string and the home string share no prefix, the trees do.
  const project = path.join(PARENT, 'sym-project');
  const link = path.join(PARENT, 'sym-link');
  mkdirSync(project);
  symlinkSync(project, link);
  assert.equal(realpathSync(link), realpathSync(project));
  const homeUnderReal = path.join(realpathSync(project), '.cf', 'home');
  assert.equal(homeUnderReal.startsWith(link), false);
  assert.throws(
    () => buildCodexArgv({ role: 'coder', model: 'm', promptPath: '/p', cwd: link, codexHome: homeUnderReal }),
    { message: 'buildCodexArgv: the session CODEX_HOME must not be inside the coder cwd (the sandbox can write there)' },
  );
  assert.equal(existsSync(path.join(project, '.cf')), false);
  // macOS: `/var` -> `/private/var`; the test parent lives under the resolved form there.
  const realParent = realpathSync(PARENT);
  if (realParent.startsWith('/private/var/') && realpathSync('/var') === '/private/var') {
    const varCwd = path.join(realParent.slice('/private'.length), 'var-project');
    assert.equal(varCwd.startsWith('/var/'), true, varCwd);
    mkdirSync(varCwd);
    const privateHome = path.join(realParent, 'var-project', '.cf', 'home');
    assert.equal(privateHome.startsWith('/private/var/'), true, privateHome);
    assert.throws(
      () => buildCodexArgv({ role: 'coder', model: 'm', promptPath: '/p', cwd: varCwd, codexHome: privateHome }),
      { message: 'buildCodexArgv: the session CODEX_HOME must not be inside the coder cwd (the sandbox can write there)' },
    );
    assert.equal(existsSync(path.join(varCwd, '.cf')), false);
  }
});

test('removeCodexHome deletes only a path strictly inside <run root>/codex-homes/: the real home, HOME, the run root, codex-homes itself, a sibling and a symlink out are refused and kept', () => {
  const homesDir = sessionCodexHomesDir();
  assert.equal(homesDir, path.join(currentRunRoot(), 'codex-homes'));
  const sibling = path.join(PARENT, 'sibling-not-a-home');
  mkdirSync(path.join(sibling, 'rules'), { recursive: true });
  writeFileSync(path.join(sibling, 'rules', 'x.rules'), '# keep\n');
  const realHome = realCodexHome();
  const home = /** @type {string} */ (process.env.HOME);
  mkdirSync(homesDir, { recursive: true });
  const escape = path.join(homesDir, `escape-${process.pid}`);
  symlinkSync(sibling, escape); // a name inside codex-homes whose realpath is outside
  const refused = [realHome, home, currentRunRoot(), homesDir, sibling, escape, path.join(PARENT, 'never-made', 'home'), 'relative/home'];
  let thrown = 0;
  for (const p of refused) {
    assert.equal(isSessionCodexHome(p), false, p);
    assert.throws(() => removeCodexHome(p), { message: 'removeCodexHome: refusing to delete a path outside <run root>/codex-homes/' }, p);
    thrown += 1;
  }
  assert.equal(thrown, 8);
  assert.equal(existsSync(path.join(sibling, 'rules', 'x.rules')), true);
  assert.equal(lstatSync(escape).isSymbolicLink(), true);
  assert.equal(existsSync(home), true);
  assert.equal(existsSync(currentRunRoot()), true);
  rmSync(escape);
  // a confined home is removed; a confined path that does not exist is a no-op
  const built = coder();
  assert.equal(isSessionCodexHome(built.env.CODEX_HOME), true);
  removeCodexHome(built.env.CODEX_HOME);
  assert.equal(existsSync(built.env.CODEX_HOME), false);
  removeCodexHome(built.env.CODEX_HOME);
  assert.equal(existsSync(homesDir), true);
});

test('an explicit $CODEX_HOME is the real home too: a codexHome inside it throws', () => {
  const envHome = path.join(PARENT, 'user-codex-home');
  mkdirSync(envHome);
  const rendered = renderForCodex(FORBIDDEN);
  assert.throws(() => prepareCodexHome(path.join(envHome, 'sub'), rendered, { env: { CODEX_HOME: envHome } }), /must not be the real Codex home/);
  const ok = prepareCodexHome(sessionPath('a'), rendered, { env: { CODEX_HOME: envHome } });
  assert.equal(ok.count, EXPECTED_RULES);
});

test('auth.json of the real Codex home is COPIED in as a regular 0600 file (not a symlink); absent => none', () => {
  const envHome = path.join(PARENT, 'user-with-auth');
  mkdirSync(envHome);
  writeFileSync(path.join(envHome, 'auth.json'), '{"token":"FAKE-auth-b41"}\n', { mode: 0o644 });
  const rendered = renderForCodex(FORBIDDEN);
  const session = prepareCodexHome(sessionPath('b'), rendered, { env: { CODEX_HOME: envHome } });
  const copy = path.join(session.codexHome, 'auth.json');
  assert.equal(lstatSync(copy).isSymbolicLink(), false);
  assert.equal(lstatSync(copy).isFile(), true);
  assert.equal(statSync(copy).mode & 0o777, 0o600);
  assert.equal(readFileSync(copy, 'utf8'), '{"token":"FAKE-auth-b41"}\n');
  // a write to the copy never reaches the real file
  writeFileSync(copy, '{"token":"FAKE-refreshed"}\n');
  assert.equal(readFileSync(path.join(envHome, 'auth.json'), 'utf8'), '{"token":"FAKE-auth-b41"}\n');
  const bare = prepareCodexHome(sessionPath('c'), rendered, { env: { CODEX_HOME: path.join(PARENT, 'no-such-home') } });
  assert.equal(existsSync(path.join(bare.codexHome, 'auth.json')), false);
});

test('a planted symlink at <session home>/auth.json (or at the rules file) makes prepareCodexHome throw; the link target is untouched', () => {
  const envHome = path.join(PARENT, 'user-with-auth-planted');
  mkdirSync(envHome);
  writeFileSync(path.join(envHome, 'auth.json'), '{"token":"FAKE-auth-secret-b41"}\n', { mode: 0o600 });
  const target = path.join(PARENT, 'planted-target.json');
  writeFileSync(target, 'PLANTED-KEEP\n', { mode: 0o644 });
  const rendered = renderForCodex(FORBIDDEN);
  // auth.json planted as a symlink to the target
  const homeE = sessionPath('e');
  mkdirSync(homeE, { recursive: true, mode: 0o700 });
  symlinkSync(target, path.join(homeE, 'auth.json'));
  assert.throws(() => prepareCodexHome(homeE, rendered, { env: { CODEX_HOME: envHome } }), { message: 'prepareCodexHome: refusing to write auth.json: something already exists at the destination' });
  assert.equal(readFileSync(target, 'utf8'), 'PLANTED-KEEP\n');
  assert.equal(statSync(target).mode & 0o777, 0o644);
  assert.equal(lstatSync(path.join(homeE, 'auth.json')).isSymbolicLink(), true);
  assert.equal(existsSync(path.join(homeE, 'rules')), false); // refused before any write
  // the rules file planted as a symlink to the target
  const homeF = sessionPath('f');
  mkdirSync(path.join(homeF, 'rules'), { recursive: true, mode: 0o700 });
  symlinkSync(target, path.join(homeF, 'rules', 'code-forge.rules'));
  assert.throws(() => prepareCodexHome(homeF, rendered, { env: { CODEX_HOME: envHome } }), { message: 'prepareCodexHome: refusing to write code-forge.rules: something already exists at the destination' });
  assert.equal(readFileSync(target, 'utf8'), 'PLANTED-KEEP\n');
  assert.equal(statSync(target).mode & 0o777, 0o644);
  assert.equal(existsSync(path.join(homeF, 'auth.json')), false);
  // a dangling planted link is refused too, and the untouched real auth.json is still 0600
  const homeG = sessionPath('g');
  mkdirSync(homeG, { recursive: true, mode: 0o700 });
  symlinkSync(path.join(PARENT, 'no-such-target'), path.join(homeG, 'auth.json'));
  assert.throws(() => prepareCodexHome(homeG, rendered, { env: { CODEX_HOME: envHome } }), /refusing to write auth\.json/);
  assert.equal(existsSync(path.join(PARENT, 'no-such-target')), false);
  assert.equal(readFileSync(path.join(envHome, 'auth.json'), 'utf8'), '{"token":"FAKE-auth-secret-b41"}\n');
  assert.equal(statSync(path.join(envHome, 'auth.json')).mode & 0o777, 0o600);
});

test('renderCodexRules: exact Starlark per pattern; a render with 0 patterns is refused', () => {
  const out = renderCodexRules([{ id: 'x', patterns: [['git', 'clean'], ['rm', '-rf']], enforced: true }, { id: 'y', patterns: [], enforced: false }]);
  assert.equal(out.count, 2);
  assert.deepEqual(out.content.split('\n').filter((l) => l.startsWith('prefix_rule(')), [
    'prefix_rule(pattern=["git", "clean"], decision="forbidden", justification="code-forge: x")',
    'prefix_rule(pattern=["rm", "-rf"], decision="forbidden", justification="code-forge: x")',
  ]);
  assert.throws(() => prepareCodexHome(sessionPath('d'), [{ id: 'y', patterns: [], enforced: false }]), /0 rules/);
});

test('closed-book roles carry no CODEX_HOME and write no rules (they keep --ignore-rules)', () => {
  const built = /** @type {any} */ (buildCodexArgv({ role: 'reviewer', model: 'gpt-6-astra', promptPath: '/p', cwd: '/c', outPath: '/o/x.json' }));
  assert.equal('env' in built, false);
  assert.equal('rulesFile' in built, false);
  assert.equal(built.argv.filter((/** @type {string} */ t) => t === '--ignore-rules').length, 1);
});
