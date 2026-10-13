/**
 * B93: the Codex coder sandbox profile (`coder.sandbox.{profile, add_dirs}`).
 *
 * The session helpers are imported FIRST (through the doctor helpers): `$HOME` and the cwd point
 * at one per-test-file temp parent, removed in `after()`. Every CLI is a fake from
 * `test/fixtures/bin/`; no real `codex`, no key, no network. Every directory a test names lives
 * under that parent (the "home" the tests protect is the temp HOME, never the real one).
 */
import { doctorDeps, makeProject } from '../doctor/helpers.mjs';
import { fakeDeps, freshDir, readRecords, sink, writeIn } from '../session/helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readdirSync, readFileSync, symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { after, test } from 'node:test';
import { buildTwoBlocksRepo } from '../fixtures/repos/two-blocks/build.mjs';

const { buildCodexArgv } = await import('../../src/engines/builders/codex.mjs');
const { spawnSession } = await import('../../src/session/spawn.mjs');
const { runSpawn } = await import('../../src/cli/spawn.mjs');
const { runRun } = await import('../../src/cli/run.mjs');
const { runDoctorCli } = await import('../../src/cli/doctor.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');
const { currentRunRoot, runRoot, setRunRoot } = await import('../../src/util/tmp.mjs');
const { runRecordPath } = await import('../../src/state/paths.mjs');

const HOME = /** @type {string} */ (process.env.HOME);
/** A unique directory name: no output, message or row may ever carry it. */
const MARKER = 'zq-marker-FAKE-b93-7f3a9c';
const EXCL = ['-c', 'sandbox_workspace_write.exclude_tmpdir_env_var=true', '-c', 'sandbox_workspace_write.exclude_slash_tmp=true'];
const APPROVAL = ['-c', 'approval_policy="never"'];
const PROTECTED_WHY = 'it is, holds or sits inside a protected directory (~/.code-forge, ~/.codex, ~/.ssh, ~/.gnupg, ~/.aws, ~/.config, ~/Library/Keychains)';
const RUN_WHY = 'it is, holds or sits inside the run temp root or the session CODEX_HOME';
const LOOP_WHY = 'its symlinks cannot be resolved';
const CLAUDE = { provider: 'anthropic', model: 'fake-opus', effort: 'high' };
const GROK = { provider: 'xai', model: 'fake-grok' };
const GPT = { provider: 'openai', model: 'fake-gpt' };

// The isolate preload makes its own root this process's run root, and the temp HOME sits inside
// it; in a real run the run root never holds HOME. This file gets a run root of its own, beside
// HOME, so the run-root guard is judged as it is in production. The isolate root comes back after.
const ISOLATE_ROOT = currentRunRoot();
setRunRoot(runRoot('b93-run-root', { root: freshDir('run-base') }));
after(() => setRunRoot(ISOLATE_ROOT));

// the protected dirs exist in the temp HOME, and a symlink named with the marker points at ~/.ssh
mkdirSync(path.join(HOME, '.ssh'), { recursive: true });
symlinkSync(path.join(HOME, '.ssh'), path.join(HOME, `${MARKER}-link`));
// a dangling symlink whose target (missing) sits inside ~/.config; two symlinks that point at each other
symlinkSync(path.join(HOME, '.config', 'newdir'), path.join(HOME, 'cache-dangle'));
symlinkSync(path.join(HOME, 'loop-b'), path.join(HOME, 'loop-a'));
symlinkSync(path.join(HOME, 'loop-a'), path.join(HOME, 'loop-b'));
/** The temp HOME's FS folds case when `~/.SSH` names the `~/.ssh` just made. */
const CASE_INSENSITIVE = existsSync(path.join(HOME, '.SSH'));

/** @param {{profile?: string, add_dirs?: string[]} | undefined} sandbox @param {Record<string, any>} [extra] */
function coderBuild(sandbox, extra = {}) {
  const cwd = freshDir('cwd');
  const params = { role: /** @type {const} */ ('coder'), model: 'gpt-6-astra', promptPath: '/brief.md', cwd, outPath: '/out.json', ...extra };
  if (sandbox !== undefined) /** @type {any} */ (params).sandbox = sandbox;
  return { cwd, built: /** @type {any} */ (buildCodexArgv(/** @type {any} */ (params))) };
}

/** @param {string} cwd @param {string[]} sandboxPart */
const expectedArgv = (cwd, sandboxPart) => ['codex', 'exec', '-m', 'gpt-6-astra', ...sandboxPart, ...EXCL, ...APPROVAL, '-C', cwd, '--json', '-o', '/out.json', '/brief.md'];

/** @param {Record<string, any>} level @param {Record<string, any>} [coder] */
function cfgFor(level, coder) {
  const entry = { ...level };
  /** @type {Record<string, any>} */
  const cfg = { provider: level.provider, levels: { L0: entry, L1: entry, L2: entry, L3: entry } };
  if (coder !== undefined) cfg.coder = coder;
  return cfg;
}

/** One foreground coder session on the fakes; the argv the fake saw and the row written. */
async function coderSession(/** @type {Record<string, any>} */ cfg, /** @type {Record<string, string>} */ env = {}) {
  const { deps, records } = fakeDeps(env);
  /** @type {Record<string, any>[]} */
  const rows = [];
  const brief = writeIn(freshDir('brief'), 'brief.md', 'do nothing');
  const cwd = freshDir('coder-cwd');
  const result = await spawnSession({ cfg, level: 'L1', role: 'coder', promptPath: brief, cwd }, { ...deps, writeRow: async (row) => void rows.push(row) });
  const seen = readRecords(records);
  return { result, rows, argv: seen.length === 1 ? seen[0].argv : null, count: seen.length, cwd, brief };
}

// ── clause 1: toolchain ──────────────────────────────────────────────────────

test('B93 (1): toolchain puts one --add-dir <abs> per entry right after -s workspace-write and keeps both tmp exclusions', () => {
  const abs = freshDir('abs-cache');
  const { cwd, built } = coderBuild({ profile: 'toolchain', add_dirs: ['~/cache-x', abs] });
  assert.deepEqual(built.argv, expectedArgv(cwd, ['-s', 'workspace-write', '--add-dir', path.join(HOME, 'cache-x'), '--add-dir', abs]));
});

// ── clause 6: full ───────────────────────────────────────────────────────────

test('B93 (6): full builds -s danger-full-access, keeps the tmp exclusions, and adds no --add-dir even with add_dirs', () => {
  const { cwd, built } = coderBuild({ profile: 'full', add_dirs: ['~/cache-x'] });
  assert.deepEqual(built.argv, expectedArgv(cwd, ['-s', 'danger-full-access']));
});

// ── clause 3: no key / workspace = today ─────────────────────────────────────

test('B93 (3): no key, workspace, and workspace with add_dirs all build today\'s argv (no --add-dir, -s workspace-write)', () => {
  for (const sandbox of [undefined, { profile: 'workspace' }, { profile: 'workspace', add_dirs: ['~/cache-x'] }, { add_dirs: ['~/cache-x'] }]) {
    const { cwd, built } = coderBuild(sandbox);
    assert.deepEqual(built.argv, expectedArgv(cwd, ['-s', 'workspace-write']), JSON.stringify(sandbox));
  }
});

test('B93 (3): a Codex coder session row carries sandbox only when the key is set; without it the row keys are today\'s', async () => {
  const plain = await coderSession(cfgFor(GPT));
  assert.equal(plain.result.status, 'ok');
  // the -o value is the session dir's out.json under this file's run root, matched by shape
  const outRe = new RegExp(`^${path.join(currentRunRoot(), 'sessions').replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}/coder-\\d+-[0-9a-f]{8}/out\\.json$`);
  assert.match(String(plain.argv?.[15]), outRe);
  assert.deepEqual(plain.argv?.with(15, '<out>'), ['exec', '-m', 'fake-gpt', '-s', 'workspace-write', ...EXCL, ...APPROVAL, '-C', plain.cwd, '--json', '-o', '<out>', plain.brief]);
  assert.equal(plain.rows.length, 1);
  assert.deepEqual(Object.keys(plain.rows[0]), ['event', 'role', 'level', 'provider', 'model', 'effort', 'fallback_step', 'run', 'block', 'status', 'tokens_in', 'tokens_out', 'tokens_source', 'usd', 'cost_source', 'duration_ms']);

  const tool = await coderSession(cfgFor(GPT, { sandbox: { profile: 'toolchain', add_dirs: ['~/cache-x'] } }));
  assert.equal(tool.result.status, 'ok');
  assert.deepEqual(tool.argv?.slice(3, 7), ['-s', 'workspace-write', '--add-dir', path.join(HOME, 'cache-x')]);
  assert.equal(tool.rows.length, 1);
  assert.equal(tool.rows[0].sandbox, 'toolchain');
  assert.deepEqual(Object.keys(tool.rows[0]), ['event', 'role', 'level', 'provider', 'model', 'effort', 'fallback_step', 'run', 'block', 'sandbox', 'status', 'tokens_in', 'tokens_out', 'tokens_source', 'usd', 'cost_source', 'duration_ms']);
});

// ── clause 5: Claude and Grok coders do not change ───────────────────────────

test('B93 (5): the Claude and Grok coder argv and rows are the same with and without coder.sandbox (full)', async () => {
  for (const level of [CLAUDE, GROK]) {
    const without = await coderSession(cfgFor(level));
    const withKey = await coderSession(cfgFor(level, { sandbox: { profile: 'full', add_dirs: ['~/cache-x'] } }));
    assert.equal(without.count, 1, level.provider);
    assert.equal(withKey.count, 1, level.provider);
    const norm = (/** @type {string[] | null} */ argv, /** @type {string} */ cwd, /** @type {string} */ brief) => (argv ?? []).map((t) => (t === cwd ? '<cwd>' : t === brief ? '<brief>' : t));
    assert.deepEqual(norm(withKey.argv, withKey.cwd, withKey.brief), norm(without.argv, without.cwd, without.brief), level.provider);
    assert.equal(withKey.argv?.includes('danger-full-access'), false);
    assert.equal(withKey.argv?.includes('--add-dir'), false);
    assert.deepEqual(Object.keys(withKey.rows[0]), Object.keys(without.rows[0]), level.provider);
    assert.equal('sandbox' in withKey.rows[0], false, level.provider);
  }
});

/** The `coder-sandbox*` rows of one `doctor --json` run in `proj`, and the `probes` row. */
async function doctorSandboxRows(/** @type {string} */ proj) {
  const d = doctorDeps();
  await runDoctorCli(['--json', '--cwd', proj], /** @type {any} */ (d.deps));
  const rows = JSON.parse(d.stdout.text()).rows;
  return { sandbox: rows.filter((/** @type {any} */ r) => r.id.startsWith('coder-sandbox')), probes: rows.filter((/** @type {any} */ r) => r.id === 'probes') };
}

const INFO_FULL = { id: 'coder-sandbox', status: 'INFO', label: 'coder sandbox', detail: 'profile full; applies to the Codex (openai) coder only' };
const WARN_FULL = { id: 'coder-sandbox-full', status: 'WARN', label: 'coder sandbox', detail: 'profile full runs the Codex coder with -s danger-full-access: it can write anywhere you can; only the forbidden list, the transcript grep and the gates limit it' };
const INFO_TOOLCHAIN_2 = { id: 'coder-sandbox', status: 'INFO', label: 'coder sandbox', detail: 'profile toolchain, 2 extra writable dirs; applies to the Codex (openai) coder only' };
const TOOLCHAIN_2 = `coder:\n  sandbox:\n    profile: toolchain\n    add_dirs: ["~/cache-x", "~/${MARKER}-ok"]\n`;

test('B93 (5, 6): doctor on a Codex-only config: toolchain gives exactly one INFO row, full one INFO and one WARN; no row names a dir', async () => {
  const proj = freshDir('doctor-openai');
  const base = `version: 1\nproject:\n  slug: b93-doctor\nprovider: openai\nlevels:\n  L0: {model: gpt-6-astra, effort: minimal}\n  L1: {model: gpt-6-astra, effort: low}\n  L2: {model: gpt-6-astra, effort: medium}\n  L3: {model: gpt-6-astra, effort: high}\ntmp:\n  root: ${JSON.stringify(freshDir('doctor-openai-tmp'))}\n`;
  const file = path.join(proj, '.code-forge.yml');
  writeFileSync(file, base);
  assert.deepEqual((await doctorSandboxRows(proj)).sandbox, []);
  writeFileSync(file, `${base}${TOOLCHAIN_2}`);
  const tool = (await doctorSandboxRows(proj)).sandbox;
  assert.deepEqual(tool, [INFO_TOOLCHAIN_2]);
  assert.equal(JSON.stringify(tool).includes(MARKER), false);
  writeFileSync(file, `${base}coder:\n  sandbox:\n    profile: full\n`);
  assert.deepEqual((await doctorSandboxRows(proj)).sandbox, [INFO_FULL, WARN_FULL]);
});

test('B93 (5): doctor adds the no-effect row only when a Claude coder is configured; none without the key', async () => {
  const { proj } = makeProject(); // provider anthropic on every level
  assert.deepEqual((await doctorSandboxRows(proj)).sandbox, []);
  const file = path.join(proj, '.code-forge.yml');
  const base = readFileSync(file, 'utf8');
  writeFileSync(file, `${base}${TOOLCHAIN_2}`);
  assert.deepEqual((await doctorSandboxRows(proj)).sandbox, [
    INFO_TOOLCHAIN_2,
    { id: 'coder-sandbox-no-effect', status: 'INFO', label: 'coder sandbox', detail: 'coder.sandbox has no effect on the Claude (anthropic) coder: the argv does not change' },
  ]);
});

test('B93 (5): doctor with a Grok coder adds the Grok no-effect row; with Claude and Grok levels it adds ONE row naming both', async () => {
  const proj = freshDir('doctor-xai');
  const file = path.join(proj, '.code-forge.yml');
  const xai = `version: 1\nproject:\n  slug: b93-doctor-xai\nprovider: xai\nlevels:\n  L0: {model: grok-4.5}\n  L1: {model: grok-4.6}\n  L2: {model: grok-4.7}\n  L3: {model: grok-4.7-build-fast}\ntmp:\n  root: ${JSON.stringify(freshDir('doctor-xai-tmp'))}\n`;
  writeFileSync(file, `${xai}${TOOLCHAIN_2}`);
  assert.deepEqual((await doctorSandboxRows(proj)).sandbox, [
    INFO_TOOLCHAIN_2,
    { id: 'coder-sandbox-no-effect', status: 'INFO', label: 'coder sandbox', detail: 'coder.sandbox has no effect on the Grok (xai) coder: the argv does not change' },
  ]);
  const { proj: mixed } = makeProject(); // anthropic, with L1 moved to xai below
  const mixedFile = path.join(mixed, '.code-forge.yml');
  const base = readFileSync(mixedFile, 'utf8').replace('  L1:\n    model: claude-sonnet-5\n', '  L1:\n    provider: xai\n    model: grok-4.6\n');
  assert.equal(base.includes('grok-4.6'), true);
  writeFileSync(mixedFile, `${base}coder:\n  sandbox:\n    profile: full\n`);
  assert.deepEqual((await doctorSandboxRows(mixed)).sandbox, [
    INFO_FULL,
    { id: 'coder-sandbox-no-effect', status: 'INFO', label: 'coder sandbox', detail: 'coder.sandbox has no effect on the Claude (anthropic) and Grok (xai) coders: the argv does not change' },
    WARN_FULL,
  ]);
});

test('B93 (5): doctor with an invalid config (a refused add_dirs entry) does not crash, skips the probes and prints no coder-sandbox row', async () => {
  const { proj } = makeProject();
  const file = path.join(proj, '.code-forge.yml');
  writeFileSync(file, `${readFileSync(file, 'utf8')}coder:\n  sandbox:\n    profile: toolchain\n    add_dirs: ["~/.ssh"]\n`);
  const { sandbox, probes } = await doctorSandboxRows(proj);
  assert.deepEqual(sandbox, []);
  assert.deepEqual(probes, [{ id: 'probes', status: 'FAIL', label: 'probes', detail: 'skipped: no valid config' }]);
});

// ── clause 2: refusals, by the builder and by validation ─────────────────────

/** The entries both the builder and validation refuse (at index 1, after a valid one), and why. */
const COMMON_CASES = /** @type {Array<[string, string, string]>} */ ([
  ['root', '/', 'it is the filesystem root'],
  ['home ~/', '~/', 'it is the home directory'],
  ['home abs', HOME, 'it is the home directory'],
  ['parent of home', path.dirname(HOME), 'it holds the home directory'],
  ['~/.code-forge', '~/.code-forge', PROTECTED_WHY],
  ['~/.codex', '~/.codex', PROTECTED_WHY],
  ['~/.ssh', '~/.ssh', PROTECTED_WHY],
  ['inside ~/.ssh', `~/.ssh/${MARKER}`, PROTECTED_WHY],
  ['~/.gnupg', '~/.gnupg', PROTECTED_WHY],
  ['~/.aws', '~/.aws', PROTECTED_WHY],
  ['~/.config', '~/.config', PROTECTED_WHY],
  ['~/Library/Keychains', '~/Library/Keychains', PROTECTED_WHY],
  ['~/Library holds Keychains', '~/Library', PROTECTED_WHY],
  ['symlink to ~/.ssh', `~/${MARKER}-link`, PROTECTED_WHY],
  ['missing child under the symlink', `~/${MARKER}-link/sub`, PROTECTED_WHY],
  ['child of ~/.ssh named ..keys', '~/.ssh/..keys', PROTECTED_WHY],
  ['dangling symlink into ~/.config', '~/cache-dangle', PROTECTED_WHY],
  ['symlink loop', '~/loop-a', LOOP_WHY],
  ['relative', `${MARKER}/cache`, 'it must be an absolute path or start with ~/'],
]);

/** @param {string} root @returns {Array<[string, string, string]>} the run-tree entries for one run temp root. */
const runCases = (root) => [
  ['run temp root', root, RUN_WHY],
  ['parent of the run temp root', path.dirname(root), RUN_WHY],
  ['inside the run temp root', path.join(root, MARKER), RUN_WHY],
];

test('B93 (2): the builder refuses each protected dir with sandbox-dir-refused naming coder.sandbox.add_dirs[1], never the value', () => {
  const runRootDir = currentRunRoot();
  const sessionHome = path.join(freshDir('other'), 'codex-home-x');
  const cases = [...COMMON_CASES, ...runCases(runRootDir), /** @type {[string, string, string]} */ (['session CODEX_HOME', sessionHome, RUN_WHY])];
  assert.equal(cases.length, 23);
  for (const [label, entry, why] of cases) {
    for (const profile of ['toolchain', 'workspace', 'full']) {
      let thrown;
      try {
        coderBuild({ profile, add_dirs: ['~/cache-x', entry] }, { codexHome: sessionHome, runRoot: runRootDir });
      } catch (err) {
        thrown = /** @type {any} */ (err);
      }
      assert.equal(thrown?.code, 'sandbox-dir-refused', `${label} ${profile}`);
      assert.equal(thrown.message, `sandbox-dir-refused: coder.sandbox.add_dirs[1] is refused — ${why}`, `${label} ${profile}`);
      assert.equal(thrown.message.includes(MARKER), false, label);
    }
  }
  // the same run root with a valid list builds (the refusal is the entry, not the guards)
  const { built } = coderBuild({ profile: 'toolchain', add_dirs: ['~/cache-x'] }, { runRoot: runRootDir });
  assert.deepEqual(built.argv.slice(4, 8), ['-s', 'workspace-write', '--add-dir', path.join(HOME, 'cache-x')]);
});

test('B93 (2): validate reports every refused entry as rule sandbox-dir-refused (ERROR) by key path, never the value; a cache dir is valid', () => {
  // tmp.root sits two levels down, as under $TMPDIR: the dir two up from it holds it and is refused too
  const tmpRoot = path.join(freshDir('v-tmp'), 'T', 'base');
  const cases = [...COMMON_CASES, ...runCases(tmpRoot), /** @type {[string, string, string]} */ (['grandparent of the run temp root', path.dirname(path.dirname(tmpRoot)), RUN_WHY])];
  assert.equal(cases.length, 23);
  const levels = { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } };
  const base = { version: 1, provider: 'anthropic', levels, tmp: { root: tmpRoot } };
  const ok = validateConfig({ ...base, coder: { sandbox: { profile: 'toolchain', add_dirs: ['~/cache-x', freshDir('cache-abs')] } } });
  assert.deepEqual(ok.errors, []);
  const bad = validateConfig({ ...base, coder: { sandbox: { profile: 'toolchain', add_dirs: ['~/cache-x', ...cases.map((c) => c[1])] } } });
  assert.equal(bad.valid, false);
  assert.deepEqual(
    bad.errors.filter((e) => e.rule !== 'schema').map((e) => [e.rule, e.severity, e.message]),
    cases.map((c, i) => ['sandbox-dir-refused', 'error', `coder.sandbox.add_dirs[${i + 1}] is refused — ${c[2]}`]),
  );
  // the schema pattern refuses the one relative entry as well (exactly one schema issue)
  assert.equal(bad.errors.filter((e) => e.rule === 'schema').length, 1);
  assert.equal(JSON.stringify(bad).includes(MARKER), false);
});

test('B93 (2): `spawn` with a symlinked marker dir exits 1 sandbox-dir-refused; the marker is in no stdout, stderr, record or row', async () => {
  const ws = freshDir('ws-refuse');
  writeFileSync(path.join(ws, '.code-forge.yml'), `version: 1\nproject:\n  slug: b93-refuse\nprovider: openai\nlevels:\n  L0: {model: fake-gpt}\n  L1: {model: fake-gpt}\n  L2: {model: fake-gpt}\n  L3: {model: fake-gpt}\ncoder:\n  sandbox:\n    profile: toolchain\n    add_dirs: ["~/cache-x", "~/${MARKER}-link"]\n`);
  const brief = writeIn(ws, 'brief.md', 'do nothing');
  const before = process.cwd();
  process.chdir(ws);
  try {
    const { deps, records, stderr } = fakeDeps();
    const stdout = sink();
    const code = await runSpawn(['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', ws], { ...deps, stdout });
    assert.equal(code, 1);
    assert.equal(stdout.text(), '');
    assert.equal(stderr.text(), `spawn: sandbox-dir-refused: coder.sandbox.add_dirs[1] is refused — ${PROTECTED_WHY}\n`);
    assert.deepEqual(readdirSync(records), []);
    const rows = await readAllRows('b93-refuse');
    assert.deepEqual(rows, []);
    for (const text of [stdout.text(), stderr.text(), JSON.stringify(rows)]) assert.equal(text.includes(MARKER), false);
  } finally {
    process.chdir(before);
  }
});

test('B93 (2): on a case-insensitive FS, ~/.SSH (existing) and ~/.GNUPG/x (missing) are refused like ~/.ssh', { skip: CASE_INSENSITIVE ? false : 'the temp HOME is on a case-sensitive FS: ~/.SSH is another dir there' }, () => {
  for (const entry of ['~/.SSH', '~/.SSH/sub', '~/.GNUPG/x']) {
    assert.throws(() => coderBuild({ profile: 'toolchain', add_dirs: [entry] }, { runRoot: currentRunRoot() }), { code: 'sandbox-dir-refused', message: `sandbox-dir-refused: coder.sandbox.add_dirs[0] is refused — ${PROTECTED_WHY}` }, entry);
  }
});

const VALID_LEVELS = { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } };

test('B93 (2): validate never crashes on a non-list add_dirs or a non-string entry; the schema reports them, rule 19 only the string entries', () => {
  const base = { version: 1, provider: 'anthropic', levels: VALID_LEVELS, tmp: { root: freshDir('v2-tmp') } };
  const str = validateConfig({ ...base, coder: { sandbox: { profile: 'toolchain', add_dirs: '~/x' } } });
  assert.equal(str.errors.filter((e) => e.rule === 'schema').length, 1);
  assert.equal(str.errors.filter((e) => e.rule === 'sandbox-dir-refused').length, 0);
  const mixed = validateConfig({ ...base, coder: { sandbox: { profile: 'toolchain', add_dirs: [7, '~/.ssh', null] } } });
  assert.deepEqual(
    mixed.errors.filter((e) => e.rule === 'sandbox-dir-refused').map((e) => e.message),
    [`coder.sandbox.add_dirs[1] is refused — ${PROTECTED_WHY}`],
  );
  assert.equal(mixed.errors.filter((e) => e.rule === 'schema').length, 2); // items 0 (7) and 2 (null) are not strings
});

test('B93 (2): add_dirs under workspace or full warns sandbox-dirs-unused naming the profile; toolchain does not; a refused entry is still refused', () => {
  const base = { version: 1, provider: 'anthropic', levels: VALID_LEVELS, tmp: { root: freshDir('v3-tmp') } };
  const unused = (/** @type {Record<string, any>} */ r) => r.warnings.filter((/** @type {any} */ w) => w.rule.startsWith('sandbox')).map((/** @type {any} */ w) => [w.rule, w.severity, w.message]);
  for (const profile of ['workspace', 'full']) {
    const r = validateConfig({ ...base, coder: { sandbox: { profile, add_dirs: ['~/cache-x'] } } });
    assert.deepEqual(r.errors, [], profile);
    assert.deepEqual(unused(r), [['sandbox-dirs-unused', 'warning', `coder.sandbox.add_dirs is set but coder.sandbox.profile is ${profile}, which does not use it — only toolchain adds --add-dir`]], profile);
  }
  const noProfile = validateConfig({ ...base, coder: { sandbox: { add_dirs: ['~/cache-x'] } } });
  assert.deepEqual(unused(noProfile).map((w) => w[0]), ['sandbox-dirs-unused']);
  assert.deepEqual(unused(validateConfig({ ...base, coder: { sandbox: { profile: 'toolchain', add_dirs: ['~/cache-x'] } } })), []);
  assert.deepEqual(unused(validateConfig({ ...base, coder: { sandbox: { profile: 'full', add_dirs: [] } } })), []);
  const refused = validateConfig({ ...base, coder: { sandbox: { profile: 'full', add_dirs: ['~/.ssh'] } } });
  assert.deepEqual(refused.errors.map((e) => e.rule), ['sandbox-dir-refused']);
  assert.deepEqual(unused(refused).map((w) => w[0]), ['sandbox-dirs-unused']);
});

// ── clause 3 + fallback: the row and argv follow the step actually built ─────

test('B93 (3): a Codex coder that falls back to Claude: the openai row carries sandbox, the anthropic row and argv do not', async () => {
  const cfg = cfgFor({ ...GPT, fallback: [{ provider: 'anthropic', model: 'fake-opus' }] }, { sandbox: { profile: 'toolchain', add_dirs: ['~/cache-x'] } });
  const { deps, records } = fakeDeps({ FAKE_402_MODELS: 'fake-gpt' });
  /** @type {Record<string, any>[]} */
  const rows = [];
  const brief = writeIn(freshDir('brief'), 'brief.md', 'do nothing');
  const result = await spawnSession({ cfg, level: 'L1', role: 'coder', promptPath: brief, cwd: freshDir('coder-cwd') }, { ...deps, writeRow: async (row) => void rows.push(row) });
  assert.equal(result.status, 'ok');
  assert.deepEqual(rows.map((r) => [r.provider, 'sandbox' in r ? r.sandbox : '<none>']), [['openai', 'toolchain'], ['anthropic', '<none>']]);
  const seen = readRecords(records);
  assert.equal(seen.length, 2);
  assert.deepEqual(seen[0].argv.slice(3, 7), ['-s', 'workspace-write', '--add-dir', path.join(HOME, 'cache-x')]);
  assert.equal(seen[1].argv.includes('--add-dir'), false);
  assert.equal(seen[1].argv.includes('workspace-write'), false);
});

test('B93 (2): a refused add_dirs entry stops the spawn even with a fallback configured: no fallback, no session, no row', async () => {
  const cfg = cfgFor({ ...GPT, fallback: [{ provider: 'anthropic', model: 'fake-opus' }] }, { sandbox: { profile: 'toolchain', add_dirs: ['~/.ssh'] } });
  const { deps, records } = fakeDeps();
  /** @type {Record<string, any>[]} */
  const rows = [];
  const brief = writeIn(freshDir('brief'), 'brief.md', 'do nothing');
  await assert.rejects(
    spawnSession({ cfg, level: 'L1', role: 'coder', promptPath: brief, cwd: freshDir('coder-cwd') }, { ...deps, writeRow: async (row) => void rows.push(row) }),
    { name: 'SessionError', code: 'sandbox-dir-refused', message: `sandbox-dir-refused: coder.sandbox.add_dirs[0] is refused — ${PROTECTED_WHY}` },
  );
  assert.deepEqual(readdirSync(records), []);
  assert.deepEqual(rows, []);
});

// ── clause 4: fixed for the run ──────────────────────────────────────────────

test('B93 (4): a run whose snapshot has no coder key, with no live key either, spawns its coder with --run (no false sandbox-changed)', async () => {
  const ws = freshDir('ws-run-plain');
  await buildTwoBlocksRepo(ws);
  writeFileSync(path.join(ws, '.code-forge.yml'), RUN_CONFIG('').replace('slug: b93-run', 'slug: b93-run-plain'));
  const err = sink();
  assert.equal(await runRun(['start', '--cwd', ws, '--run', 'r-b93-plain'], { stdout: sink(), stderr: err }), 0, err.text());
  const brief = writeIn(freshDir('brief'), 'brief.md', 'do nothing');
  const before = process.cwd();
  process.chdir(ws);
  try {
    const plain = fakeDeps();
    assert.equal(await runSpawn(['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', ws, '--run', 'r-b93-plain'], { ...plain.deps, stdout: sink() }), 0, plain.stderr.text());
    const seen = readRecords(plain.records);
    assert.equal(seen.length, 1);
    assert.deepEqual(seen[0].argv.slice(5, 7), ['-s', 'workspace-write']);
    assert.equal(seen[0].argv.includes('--add-dir'), false);
  } finally {
    process.chdir(before);
  }
});

const RUN_CONFIG = (/** @type {string} */ sandbox) =>
  `version: 1\nproject:\n  slug: b93-run\nprovider: openai\nengine: harness\nlevels:\n  L0: {model: gpt-6-astra, effort: minimal}\n  L1: {model: gpt-6-astra, effort: low}\n  L2: {model: gpt-6-astra, effort: medium}\n  L3: {model: gpt-6-astra, effort: high}\nreview:\n  allow_open_book_codex: true\n${sandbox}`;
const TOOLCHAIN = 'coder:\n  sandbox:\n    profile: toolchain\n    add_dirs: ["~/cache-x"]\n';
const FULL = 'coder:\n  sandbox:\n    profile: full\n';

test('B93 (4): run reload refuses a coder.sandbox change; spawn --run uses the snapshot and exits 1 sandbox-changed on a differing live value; without --run the live config is read', async () => {
  const ws = freshDir('ws-run');
  await buildTwoBlocksRepo(ws);
  writeFileSync(path.join(ws, '.code-forge.yml'), RUN_CONFIG(TOOLCHAIN));
  const out = sink();
  const err = sink();
  assert.equal(await runRun(['start', '--cwd', ws, '--run', 'r-b93'], { stdout: out, stderr: err }), 0, err.text());
  const brief = writeIn(freshDir('brief'), 'brief.md', 'do nothing');
  const before = process.cwd();
  process.chdir(ws);
  try {
    // same live value as the snapshot: the session runs with --add-dir
    const same = fakeDeps();
    const code = await runSpawn(['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', ws, '--run', 'r-b93'], { ...same.deps, stdout: sink() });
    assert.equal(code, 0, same.stderr.text());
    const seen = readRecords(same.records);
    assert.equal(seen.length, 1);
    assert.deepEqual(seen[0].argv.slice(5, 9), ['-s', 'workspace-write', '--add-dir', path.join(HOME, 'cache-x')]);

    // the live config now says full: reload refuses, spawn --run refuses before any spawn
    writeFileSync(path.join(ws, '.code-forge.yml'), RUN_CONFIG(FULL));
    const rErr = sink();
    const reload = await runRun(['reload', '--run', 'r-b93'], { stdout: sink(), stderr: rErr });
    assert.equal(reload, 1);
    assert.equal(rErr.text(), 'run reload: coder.sandbox cannot change mid-run (the coder sandbox widens what the coder may write; fixed by the owner before the run starts) — put it back, or end the run and start a new one\n');

    const changed = fakeDeps();
    const stdout = sink();
    assert.equal(await runSpawn(['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', ws, '--run', 'r-b93'], { ...changed.deps, stdout }), 1);
    assert.equal(stdout.text(), '');
    assert.equal(changed.stderr.text(), 'spawn: sandbox-changed: coder.sandbox in the live config differs from the run r-b93 snapshot — put it back, or end the run and start a new one\n');
    assert.deepEqual(readdirSync(changed.records), []);

    // the key removed from the live config differs too
    writeFileSync(path.join(ws, '.code-forge.yml'), RUN_CONFIG(''));
    const removed = fakeDeps();
    assert.equal(await runSpawn(['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', ws, '--run', 'r-b93'], { ...removed.deps, stdout: sink() }), 1);
    assert.equal(removed.stderr.text(), 'spawn: sandbox-changed: coder.sandbox in the live config differs from the run r-b93 snapshot — put it back, or end the run and start a new one\n');
    assert.deepEqual(readdirSync(removed.records), []);

    // without --run the live config (full) is read
    writeFileSync(path.join(ws, '.code-forge.yml'), RUN_CONFIG(FULL));
    const live = fakeDeps();
    assert.equal(await runSpawn(['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', ws], { ...live.deps, stdout: sink() }), 0, live.stderr.text());
    const liveSeen = readRecords(live.records);
    assert.equal(liveSeen.length, 1);
    assert.deepEqual(liveSeen[0].argv.slice(5, 7), ['-s', 'danger-full-access']);
    assert.equal(liveSeen[0].argv.includes('--add-dir'), false);

    // a reviewer on the same openai level never reads coder.sandbox: with the live value still
    // differing, --run spawns it (review.allow_open_book_codex lets Codex review)
    const reviewer = fakeDeps();
    assert.equal(await runSpawn(['--level', 'L1', '--role', 'reviewer', '--brief', brief, '--cwd', ws, '--run', 'r-b93'], { ...reviewer.deps, stdout: sink() }), 0, reviewer.stderr.text());
    const reviewerSeen = readRecords(reviewer.records);
    assert.equal(reviewerSeen.length, 1);
    assert.deepEqual(reviewerSeen[0].argv.slice(1, 3), ['-m', 'gpt-6-astra']);
    assert.equal(reviewerSeen[0].argv.includes('read-only'), true);

    // a run record that cannot be parsed: a named error, exit 2, nothing spawned
    writeFileSync(runRecordPath('r-b93-bad'), '{not json');
    const bad = fakeDeps();
    assert.equal(await runSpawn(['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', ws, '--run', 'r-b93-bad'], { ...bad.deps, stdout: sink() }), 2);
    assert.equal(bad.stderr.text(), 'spawn: run-unreadable: the record of run r-b93-bad cannot be read, so coder.sandbox cannot be checked against the run start\n');
    assert.deepEqual(readdirSync(bad.records), []);

    // a run recorded before config snapshots: no run-start value, so the live config (full) is used
    const old = JSON.parse(readFileSync(runRecordPath('r-b93'), 'utf8'));
    delete old.config;
    writeFileSync(runRecordPath('r-b93-old'), JSON.stringify(old));
    const legacy = fakeDeps();
    assert.equal(await runSpawn(['--level', 'L1', '--role', 'coder', '--brief', brief, '--cwd', ws, '--run', 'r-b93-old'], { ...legacy.deps, stdout: sink() }), 0, legacy.stderr.text());
    const legacySeen = readRecords(legacy.records);
    assert.equal(legacySeen.length, 1);
    assert.deepEqual(legacySeen[0].argv.slice(5, 7), ['-s', 'danger-full-access']);
  } finally {
    process.chdir(before);
  }
});
