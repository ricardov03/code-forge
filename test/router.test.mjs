import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readdirSync, realpathSync } from 'node:fs';
import { cp, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { after, before, test } from 'node:test';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(__dirname, '..');
const BIN = path.join(ROOT, 'bin', 'code-forge.mjs');
const CLI_DIR = path.join(ROOT, 'src', 'cli');

/** An exact `major.minor.patch[-prerelease]` version, no ranges, no `^`/`~`, no partial forms. */
const EXACT_SEMVER = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;

/**
 * A hermetic copy of the B0 package — `bin/`, `src/util/`, and ONLY the two verbs B0 ships
 * (`help`, `version`) — under a temp path that contains a space. Later blocks add verbs to the
 * real `src/cli/` while this test runs (other coders share the tree), so exact verb-list
 * assertions and verb-file drops happen here, never in the real tree.
 */
let PKG = '';
let PKG_BIN = '';
let PKG_CLI = '';

before(async () => {
  PKG = await mkdtemp(path.join(os.tmpdir(), 'code-forge pkg-'));
  await mkdir(path.join(PKG, 'src', 'cli'), { recursive: true });
  await cp(path.join(ROOT, 'bin'), path.join(PKG, 'bin'), { recursive: true });
  await cp(path.join(ROOT, 'src', 'util'), path.join(PKG, 'src', 'util'), { recursive: true });
  for (const verb of ['help.mjs', 'version.mjs']) {
    await cp(path.join(CLI_DIR, verb), path.join(PKG, 'src', 'cli', verb));
  }
  await cp(path.join(ROOT, 'package.json'), path.join(PKG, 'package.json'));
  PKG_BIN = path.join(PKG, 'bin', 'code-forge.mjs');
  PKG_CLI = path.join(PKG, 'src', 'cli');
});

after(async () => {
  if (PKG) await rm(PKG, { recursive: true, force: true });
});

/** @param {string[]} args @param {string} [bin] */
function runCli(args, bin = PKG_BIN) {
  return spawnSync(process.execPath, [bin, ...args], { encoding: 'utf8' });
}

/** @param {string} haystack @param {string} needle */
function occurrences(haystack, needle) {
  return haystack.split(needle).length - 1;
}

/**
 * The verb names listed under the `Verbs:` header of a `code-forge --help`-shaped output.
 * Stops at the first line that isn't a `  <name>` entry (a blank line, a future extra section
 * header, or end of output) instead of counting every two-space-indented line to the end of
 * stdout, so a stray extra section couldn't inflate the count.
 * @param {string} stdout
 * @returns {string[]}
 */
function listedVerbNames(stdout) {
  const lines = stdout.split('\n');
  const start = lines.findIndex((l) => l.trim() === 'Verbs:');
  assert.notEqual(start, -1, `expected a "Verbs:" header in:\n${stdout}`);
  const names = [];
  for (const line of lines.slice(start + 1)) {
    const match = /^ {2}(\S+)$/.exec(line);
    if (!match) break;
    names.push(match[1]);
  }
  return names;
}

// ── Acceptance (1): package.json dependency set, exact versions ────────────

test('package.json declares exactly the specified runtime/optional/dev dependencies', async () => {
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));

  assert.deepEqual(Object.keys(pkg.dependencies).sort(), ['@clack/prompts', 'ajv', 'yaml']);
  assert.deepEqual(Object.keys(pkg.optionalDependencies).sort(), ['@napi-rs/keyring']);
  // R14 (B0.1): no mutation tooling — dev deps are exactly typescript + @types/node.
  assert.deepEqual(Object.keys(pkg.devDependencies).sort(), ['@types/node', 'typescript']);
});

test('R14: stryker.config.mjs does not exist and no npm script runs stryker', async () => {
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  assert.equal(existsSync(path.join(ROOT, 'stryker.config.mjs')), false);
  assert.deepEqual(Object.keys(pkg.scripts).sort(), ['bench:review', 'changelog', 'known-fix', 'release', 'test', 'typecheck']);
  assert.equal(pkg.scripts['bench:review'], 'node scripts/bench-review.mjs');
  assert.equal(pkg.scripts.release, 'node scripts/release.mjs');
  assert.equal(pkg.scripts.changelog, 'node scripts/changelog.mjs');
  assert.equal(pkg.scripts['known-fix'], 'node scripts/known-fix.mjs');
});

test('package.json declares no peer or bundled dependencies, under either npm spelling', async () => {
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  assert.equal(pkg.peerDependencies, undefined);
  assert.equal(pkg.bundleDependencies, undefined);
  assert.equal(pkg.bundledDependencies, undefined);
});

/** Run the package test entry with a fake node; inspect phase argv and statuses, not source. */
async function testPhases(snapshot, main, final, extraArgs = [], suite = 'b115-shell') {
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const dir = path.join(PKG, 'test-script-bin');
  await mkdir(dir, { recursive: true });
  await writeFile(path.join(dir, 'node'), `#!/bin/sh
if [ "$1" = "-e" ]; then printf '%s' 'b115-generated-suite'; exit 0; fi
case "$CODE_FORGE_NO_LEAK" in
  snapshot) phase=snapshot; status=$SNAPSHOT_STATUS;;
  final) phase=final; status=$FINAL_STATUS;;
  *) phase=main; status=$MAIN_STATUS;;
esac
printf '%s|%s' "$phase" "$CODE_FORGE_TEST_SUITE"
for arg do printf '|%s' "$arg"; done
printf '\n'
exit "$status"
`, { mode: 0o755 });
  const env = { ...process.env, PATH: dir + path.delimiter + process.env.PATH, SNAPSHOT_STATUS: String(snapshot), CODE_FORGE_TEST_SUITE: suite, CODE_FORGE_NO_LEAK: '', MAIN_STATUS: String(main), FINAL_STATUS: String(final) };
  if (suite === null) delete env.CODE_FORGE_TEST_SUITE;
  if (extraArgs.length === 0) return spawnSync('sh', ['-c', pkg.scripts.test], { cwd: ROOT, timeout: 20000, encoding: 'utf8', env });
  return spawnSync(process.execPath, [npmJsPath(), 'test', '--', ...extraArgs], { cwd: ROOT, timeout: 20000, encoding: 'utf8', env: { ...env, npm_config_loglevel: 'silent' } });
}

/** Locate the npm JS entry independently of the fake node used by phase fixtures.
 * @param {NodeJS.ProcessEnv} [env]
 */
function npmJsPath(env = process.env) {
  const source = env.npm_execpath ? 'process.env.npm_execpath' : 'command -v npm';
  const candidate = env.npm_execpath || spawnSync('/bin/sh', ['-c', 'command -v npm'], { encoding: 'utf8', env }).stdout.trim();
  try {
    const resolved = realpathSync(candidate);
    assert.match(resolved, /\.(?:[cm]?js)$/);
    return resolved;
  } catch {
    throw new Error(`npm JS entry not found via ${source}: ${candidate || '(empty result)'}; expected a JavaScript file`);
  }
}

const PHASE_ARGV = [
  'snapshot|b115-shell|--import|./test/helpers/isolate.mjs|--test|test/no-leak.test.mjs',
  'main|b115-shell|--import|./test/helpers/isolate.mjs|--experimental-test-module-mocks|--test|test/**/*.test.mjs',
  'final|b115-shell|--import|./test/helpers/isolate.mjs|--test|test/no-leak.test.mjs',
];

test('package.json requires Node >= 22 and runs tests under the isolate preload with module mocks enabled, between the no-leak snapshot and final steps', async () => {
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  assert.equal(pkg.engines.node, '>=22');
  const result = await testPhases(0, 0, 0);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.stdout.trim().split('\n'), PHASE_ARGV);
});

test('B113: the test script preserves each failing phase status and runs final after main fails and skips main after snapshot fails', async () => {
  for (const [snapshot, main, final, expected] of [[0, 0, 0, 0], [0, 7, 0, 1], [0, 0, 9, 1], [0, 7, 9, 1], [5, 0, 0, 5]]) {
    const result = await testPhases(snapshot, main, final);
    assert.equal(result.status, expected, result.stderr);
    assert.deepEqual(result.stdout.trim().split('\n'), snapshot === 0 ? PHASE_ARGV : PHASE_ARGV.slice(0, 1));
  }
});

test('B115: npm test forwards extra argv to main only and still runs snapshot and final', async () => {
  const args = ['--test-name-pattern=a b', '--test-name-pattern=x*', '*', '--test-reporter=spec'];
  const result = await testPhases(0, 0, 0, args);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.stdout.trim().split('\n'), [PHASE_ARGV[0], 'main|b115-shell|--import|./test/helpers/isolate.mjs|--experimental-test-module-mocks|--test|' + args.join('|') + '|test/**/*.test.mjs', PHASE_ARGV[2]]);
});

test('B115: the test script generates one suite marker when unset and preserves a preset marker in all three phases', async () => {
  for (const suite of [null, 'b115-preset-suite']) {
    const result = await testPhases(0, 0, 0, [], suite);
    assert.equal(result.status, 0, result.stderr);
    const lines = result.stdout.trim().split('\n');
    assert.equal(lines.length, 3);
    const markers = lines.map((line) => line.split('|')[1]);
    assert.notEqual(markers[0], '');
    assert.deepEqual(markers, Array(3).fill(suite ?? markers[0]));
    assert.deepEqual(lines, PHASE_ARGV.map((line) => line.replace('|b115-shell|', `|${markers[0]}|`)));
  }
});

test('B115: npm lookup prefers npm_execpath, falls back to command -v npm, and names invalid JS entries', async () => {
  const dir = path.join(PKG, 'npm-lookup');
  await mkdir(dir);
  const entry = path.join(dir, 'npm-cli.js');
  const wrapper = path.join(dir, 'npm');
  await writeFile(entry, '// fixture npm entry\n', { mode: 0o755 });
  await symlink(entry, wrapper);
  assert.equal(npmJsPath({ npm_execpath: entry, PATH: '' }), realpathSync(entry));
  assert.equal(npmJsPath({ PATH: dir }), realpathSync(entry));
  assert.throws(() => npmJsPath({ PATH: '' }), /npm JS entry not found via command -v npm: \(empty result\); expected a JavaScript file/);
  const nonJs = path.join(dir, 'shell-wrapper');
  await writeFile(nonJs, '#!/bin/sh\n', { mode: 0o755 });
  assert.throws(() => npmJsPath({ npm_execpath: nonJs, PATH: dir }), (err) => {
    assert.ok(err instanceof Error);
    assert.equal(err.message, `npm JS entry not found via process.env.npm_execpath: ${nonJs}; expected a JavaScript file`);
    return true;
  });
  await rm(wrapper);
  await symlink(nonJs, wrapper);
  assert.throws(() => npmJsPath({ PATH: dir }), (err) => {
    assert.ok(err instanceof Error);
    assert.equal(err.message, `npm JS entry not found via command -v npm: ${wrapper}; expected a JavaScript file`);
    return true;
  });
});

test('every dependency version is an exact semver — no ^, ~, *, "latest", or partial form', async () => {
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const allVersions = [
    ...Object.values(pkg.dependencies ?? {}),
    ...Object.values(pkg.optionalDependencies ?? {}),
    ...Object.values(pkg.devDependencies ?? {}),
  ];

  assert.equal(allVersions.length, 6);
  for (const version of allVersions) {
    assert.match(version, EXACT_SEMVER, `"${version}" is not an exact semver`);
  }
});

test('npm-shrinkwrap.json matches package.json exactly — same dependency sets and resolved versions', async () => {
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const shrinkwrap = JSON.parse(await readFile(path.join(ROOT, 'npm-shrinkwrap.json'), 'utf8'));

  const root = shrinkwrap.packages?.[''];
  assert.ok(root, 'npm-shrinkwrap.json has no root ("") package entry');

  assert.deepEqual(root.dependencies ?? {}, pkg.dependencies ?? {});
  assert.deepEqual(root.optionalDependencies ?? {}, pkg.optionalDependencies ?? {});
  assert.deepEqual(root.devDependencies ?? {}, pkg.devDependencies ?? {});

  const allDeps = {
    ...pkg.dependencies,
    ...pkg.optionalDependencies,
    ...pkg.devDependencies,
  };
  for (const [name, pinnedVersion] of Object.entries(allDeps)) {
    const installed = shrinkwrap.packages?.[`node_modules/${name}`];
    assert.ok(installed, `npm-shrinkwrap.json has no node_modules/${name} entry`);
    assert.equal(installed.version, pinnedVersion, `${name} pinned at ${pinnedVersion} but shrinkwrap resolved ${installed.version}`);
  }
});

// ── Acceptance (2): verb discovery by readdir(src/cli), no router edit needed ─

test('with the 2 shipped verbs, --help lists exactly ["help", "version"]', () => {
  const { status, stdout, stderr } = runCli(['--help']);
  assert.equal(status, 0, stderr);
  assert.deepEqual(listedVerbNames(stdout), ['help', 'version']);
});

test('run through a SYMLINK in a directory with a space (the npx/npm bin-shim case), --help lists exactly ["help", "version"]', async () => {
  const linkDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-link-'));
  try {
    await mkdir(path.join(linkDir, 'a b'));
    const link = path.join(linkDir, 'a b', 'code-forge.mjs');
    await symlink(PKG_BIN, link);

    const { status, stdout, stderr } = runCli(['--help'], link);

    assert.equal(status, 0, stderr);
    assert.deepEqual(listedVerbNames(stdout), ['help', 'version']);
  } finally {
    await rm(linkDir, { recursive: true, force: true });
  }
});

test('the real tree lists exactly the verb files present in src/cli (an independent readdir), help and version included', () => {
  const onDisk = readdirSync(CLI_DIR)
    .filter((name) => /^[a-z][a-z0-9-]*\.mjs$/.test(name))
    .map((name) => name.replace(/\.mjs$/, ''))
    .sort();
  const { status, stdout, stderr } = runCli(['--help'], BIN);

  assert.equal(status, 0, stderr);
  assert.deepEqual(listedVerbNames(stdout), onDisk);
  assert.deepEqual(['help', 'version'].filter((v) => onDisk.includes(v)), ['help', 'version']);
});

test('bare "help" exits 0 and lists the exact same verb names as --help', () => {
  const withHelp = runCli(['--help']);
  const bareHelp = runCli(['help']);
  assert.equal(bareHelp.status, 0, bareHelp.stderr);
  assert.deepEqual(listedVerbNames(bareHelp.stdout), ['help', 'version']);
  assert.deepEqual(listedVerbNames(bareHelp.stdout), listedVerbNames(withHelp.stdout));
});

test('dropping a new verb file makes it discoverable, by name, with no router edit', async () => {
  const zzzPath = path.join(PKG_CLI, 'zzz.mjs');
  await writeFile(zzzPath, 'export default async function zzz() { return 0; }\n', { flag: 'wx' });
  try {
    const { status, stdout, stderr } = runCli(['--help']);
    assert.equal(status, 0, stderr);
    assert.deepEqual(listedVerbNames(stdout), ['help', 'version', 'zzz']);
  } finally {
    await rm(zzzPath, { force: true });
  }
});

test('help.mjs falls back to its own readdir-based discovery when called with no router context', () => {
  // Exercised via a real subprocess, not an in-process monkey-patch of process.stdout.write:
  // node:test's own reporter also writes to process.stdout asynchronously, and patching it across
  // an await can swallow a neighbouring test's result line.
  const helpUrl = pathToFileURL(path.join(PKG_CLI, 'help.mjs')).href;
  const script = `
    (async () => {
      const { default: help } = await import(${JSON.stringify(helpUrl)});
      const code = await help([]);
      process.exitCode = code ?? 0;
    })();
  `;
  const { status, stdout, stderr } = spawnSync(process.execPath, ['-e', script], { encoding: 'utf8' });

  assert.equal(status, 0, stderr);
  assert.deepEqual(listedVerbNames(stdout), ['help', 'version']);
});

test('code-forge version exits 0 and prints exactly one line: "<name> <version>"', async () => {
  const pkg = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const { status, stdout, stderr } = runCli(['version']);
  assert.equal(status, 0, stderr);
  assert.equal(stdout, `${pkg.name} ${pkg.version}\n`);
});

// ── bin/code-forge.mjs robustness ────────────────────────────────────────────

test('an error thrown inside a verb exits exactly 1 and is printed once, redacted', async () => {
  // redact() only scrubs secrets registered in THIS process, so the verb registers the fake key
  // before throwing, exactly as src/keys/** registers a resolved key.
  const fakeKey = 'sk-fake-9f2c7e1a4b6d8035';
  const zzzPath = path.join(PKG_CLI, 'zzz-throws.mjs');
  await writeFile(
    zzzPath,
    [
      "import { registerSecret } from '../util/redact.mjs';",
      'export default async function zzzThrows() {',
      `  registerSecret(${JSON.stringify(fakeKey)});`,
      `  throw new Error(${JSON.stringify(`leaked ${fakeKey}`)});`,
      '}',
      '',
    ].join('\n'),
    { flag: 'wx' },
  );
  try {
    const { status, stdout, stderr } = runCli(['zzz-throws']);
    assert.equal(status, 1, stderr);
    assert.equal(occurrences(stdout + stderr, fakeKey), 0);
    assert.equal(occurrences(stderr, 'leaked [REDACTED]'), 1, `expected one redacted error line:\n${stderr}`);
    assert.equal(occurrences(stderr, 'code-forge: verb "zzz-throws" failed:'), 1);
  } finally {
    await rm(zzzPath, { force: true });
  }
});

for (const [label, returned, expectedStatus] of [
  ['NaN', 'NaN', 1],
  ['-1', '-1', 1],
  ['1.5', '1.5', 1],
  ['256', '256', 1],
  ['3 (valid, passed through)', '3', 3],
  ['0 (valid)', '0', 0],
]) {
  test(`a verb returning ${label} makes the CLI exit ${expectedStatus}`, async () => {
    const zzzPath = path.join(PKG_CLI, 'zzz-code.mjs');
    await writeFile(zzzPath, `export default async function zzzCode() { return ${returned}; }\n`, { flag: 'wx' });
    try {
      const { status, stderr } = runCli(['zzz-code']);
      assert.equal(status, expectedStatus, stderr);
    } finally {
      await rm(zzzPath, { force: true });
    }
  });
}

// ── Acceptance (7): help fixtures are byte copies of the real --help output ─

test('the Claude help fixture has --safe-mode and does not have --max-turns', async () => {
  const fixture = await readFile(
    path.join(ROOT, 'test', 'fixtures', 'help', 'claude-2.1.282.txt'),
    'utf8',
  );
  assert.ok(fixture.includes('--safe-mode'));
  assert.equal(fixture.includes('--max-turns'), false);
});

test('the Codex help fixture has the flags §5.2 pins it for', async () => {
  const fixture = await readFile(path.join(ROOT, 'test', 'fixtures', 'help', 'codex-0.155.1.txt'), 'utf8');
  for (const flag of ['--ephemeral', '--ignore-rules', '--ignore-user-config', '--approve-for-me', '--output-schema', '--json']) {
    assert.ok(fixture.includes(flag), `codex fixture is missing ${flag}`);
  }
});

test('the Grok help fixture has the flags §5.2 pins it for', async () => {
  const fixture = await readFile(path.join(ROOT, 'test', 'fixtures', 'help', 'grok-1.0.34.txt'), 'utf8');
  for (const flag of ['--deny', '--allow', '--disallowed-tools', '--tools']) {
    assert.ok(fixture.includes(flag), `grok fixture is missing ${flag}`);
  }
});

test('all three help fixtures are non-empty', async () => {
  for (const file of ['claude-2.1.282.txt', 'codex-0.155.1.txt', 'grok-1.0.34.txt']) {
    const content = await readFile(path.join(ROOT, 'test', 'fixtures', 'help', file), 'utf8');
    assert.ok(content.length > 0, `${file} is empty`);
  }
});
