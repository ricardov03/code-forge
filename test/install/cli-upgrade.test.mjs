import assert from 'node:assert/strict';
import { readFile, readlink, realpath, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { sink, tempDir, writeMarker } from './helpers.mjs';
import { DEFAULT_SKILL_SOURCE, parseUpgradeArgs, runUpgrade } from '../../src/cli/upgrade.mjs';
import { install, installsPath, readInstalls, writeInstallsAtomically } from '../../src/install/link.mjs';

/** The package root, computed independently of `src/cli/upgrade.mjs`'s own `__dirname` math. */
const ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

/** @param {(string | undefined)[]} [versions] */
async function summaryFixture(versions = []) {
  const home = await tempDir('cf-summary-home-');
  const source = await tempDir('cf-summary-src-');
  const records = versions.map((version, i) => ({
    harness: `agent${i}`, scope: /** @type {const} */ ('project'),
    method: /** @type {'copy' | 'symlink'} */ (i % 2 ? 'copy' : 'symlink'), source,
    target: path.join(home, `target${i}`), linked_at: '2026-01-01T00:00:00Z',
    ...(version === undefined ? {} : { version }),
  }));
  await writeInstallsAtomically(installsPath(home), records);
  return { home, defaultSource: source, packageVersion: '2.0.0', changelogPath: path.join(home, 'CHANGELOG.md') };
}

test('successful upgrade starts with a compact ASCII logo and the injected running version; defaults use package.json', async () => {
  const deps = await summaryFixture();
  const stdout = sink();
  assert.equal(await runUpgrade([], { ...deps, stdout, stderr: sink() }), 0);
  const lines = stdout.text.trimEnd().split('\n');
  const versionLine = lines.indexOf('code-forge 2.0.0');
  assert.ok(versionLine >= 1 && versionLine <= 7, `logo has ${versionLine} lines; expected 1–7`);
  const logo = lines.slice(0, versionLine);
  assert.deepEqual(logo, [
    ' ___  __   __   ___       ___  __   __   __   ___',
    '|    |  | |  \\ |__   __  |__  |  | |__/ | _  |__',
    '|___ |__| |__/ |___      |    |__| |  \\ |__] |___',
  ], 'output starts with the exact three-line code-forge wordmark');
  for (const line of logo) {
    assert.ok(line.length <= 60, `logo line has ${line.length} columns`);
    assert.match(line, /^[\x20-\x7e]*$/, 'logo is printable ASCII');
  }
  assert.equal(lines.filter((line) => line === 'code-forge 2.0.0').length, 1);
  assert.doesNotMatch(stdout.text, /\x1b/, 'summary has no ANSI escapes');
  const real = sink();
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  assert.equal(await runUpgrade([], { ...deps, packageVersion: undefined, stdout: real, stderr: sink() }), 0);
  assert.equal(real.text.split('\n').filter((line) => line === `code-forge ${version}`).length, 1);
});

test('version change uses the oldest recorded version and a second upgrade sees the version persisted by the first', async () => {
  const deps = await summaryFixture(['1.10.0', '1.9.0', undefined]);
  const stdout = sink();
  assert.equal(await runUpgrade([], { ...deps, stdout, stderr: sink() }), 0);
  assert.equal(stdout.text.split('\n').filter((line) => line === '1.9.0 -> 2.0.0').length, 1);
  assert.deepEqual((await readInstalls(installsPath(deps.home))).map((r) => r.version), ['2.0.0', '2.0.0', '2.0.0']);
  const next = sink();
  assert.equal(await runUpgrade([], { ...deps, packageVersion: '2.1.0', stdout: next, stderr: sink() }), 0);
  assert.equal(next.text.split('\n').filter((line) => line === '2.0.0 -> 2.1.0').length, 1);
});

test('version summary handles equal versions, legacy records and prerelease ordering', async () => {
  for (const [versions, expected] of [
    [['2.0.0'], 'already on 2.0.0'],
    [[undefined], 'first versioned upgrade'],
    [['1.0.0', '1.0.0-rc.10', '1.0.0-rc.2'], '1.0.0-rc.2 -> 2.0.0'],
  ]) {
    const deps = await summaryFixture(/** @type {(string | undefined)[]} */ (versions));
    const stdout = sink();
    assert.equal(await runUpgrade([], { ...deps, stdout, stderr: sink() }), 0);
    assert.equal(stdout.text.split('\n').filter((line) => line === expected).length, 1, String(expected));
    assert.equal((await readInstalls(installsPath(deps.home))).length, versions.length);
  }
});

test('release notes select exactly the first five bullets of the running version and truncate to 100 columns', async () => {
  const deps = await summaryFixture(['1.0.0']);
  const long = 'x'.repeat(101);
  await writeFile(deps.changelogPath, `# Changelog\n## [Unreleased]\n- unreleased\n## [2.0.0] — today\n### Added\n- first\n- ${long}\n### Changed\n- ${'y'.repeat(100)}\n- fourth\n- fifth\n- sixth\n## [1.0.0]\n- old\n`);
  const stdout = sink();
  assert.equal(await runUpgrade([], { ...deps, stdout, stderr: sink() }), 0);
  assert.equal(stdout.text.split("What's new in 2.0.0:\n").length, 2, 'one notes heading');
  assert.deepEqual(stdout.text.split("What's new in 2.0.0:\n")[1].trimEnd().split('\n'), [
    'first', `${'x'.repeat(97)}...`, 'y'.repeat(100), 'fourth', 'fifth', `Full notes: ${deps.changelogPath}`,
  ]);
  assert.ok(stdout.text.indexOf('upgraded agent0 ') < stdout.text.indexOf("What's new"), 'records precede notes');
  const packaged = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  assert.equal(packaged.files.filter((file) => file === 'CHANGELOG.md').length, 1);
});

test('missing, malformed, empty or other-version changelog omits the whole notes part without errors', async () => {
  for (const content of [undefined, 'not a changelog\n- stray', '## [2.0.0]\n### Changed\n', '## [2.0.00]\n- wrong']) {
    const deps = await summaryFixture();
    if (content !== undefined) await writeFile(deps.changelogPath, content);
    const stdout = sink();
    const stderr = sink();
    assert.equal(await runUpgrade([], { ...deps, stdout, stderr }), 0);
    assert.equal(stdout.text.split('\n')[0], ' ___  __   __   ___       ___  __   __   __   ___', 'successful empty upgrade still starts with its logo');
    assert.doesNotMatch(stdout.text, /What's new|Full notes:/);
    assert.equal(stderr.text, '');
  }
});

test('--plain alone and with --source in either order prints only unchanged record lines, with no ANSI', async () => {
  for (const order of ['default', 'before', 'after']) {
    const deps = await summaryFixture(['1.0.0']);
    await writeFile(deps.changelogPath, '## [2.0.0]\n- note\n');
    const source = await tempDir('cf-plain-src-');
    const args = order === 'default' ? ['--plain'] : order === 'before' ? ['--plain', '--source', source] : ['--source', source, '--plain'];
    const stdout = sink();
    const stderr = sink();
    assert.equal(await runUpgrade(args, { ...deps, stdout, stderr }), 0, stderr.text);
    assert.equal(stdout.text, `upgraded agent0 (project) -> ${order === 'default' ? deps.defaultSource : source}\n`);
    assert.doesNotMatch(stdout.text + stderr.text, /\x1b/);
    const empty = await summaryFixture();
    const emptyOut = sink();
    assert.equal(await runUpgrade(['--plain'], { ...empty, stdout: emptyOut, stderr }), 0);
    assert.equal(emptyOut.text, 'nothing to upgrade\n');
  }
});

test('usage and missing-source failures have no banner and keep exits 2 and 1, including --plain', async () => {
  const deps = await summaryFixture();
  for (const args of [['--plain', '--plain'], ['--source', '--plain'], ['--plain', 'extra'], ['--source', '/missing-upgrade-source'], ['--plain', '--source', '/missing-upgrade-source']]) {
    const stdout = sink();
    const stderr = sink();
    const missing = args.includes('/missing-upgrade-source');
    assert.equal(await runUpgrade(args, { ...deps, stdout, stderr }), missing ? 1 : 2);
    assert.equal(stdout.text, '', 'errors write zero stdout');
    if (!missing) assert.equal(stderr.text, 'usage: code-forge upgrade [--source <path>] [--plain]\n');
    else assert.match(stderr.text, /does not exist/);
    assert.doesNotMatch(stderr.text, /\x1b/);
  }
});

test('upgrade docs describe the summary and --plain in both the README upgrade line and getting-started section', async () => {
  const readme = await readFile(path.join(ROOT, 'README.md'), 'utf8');
  const line = readme.split('\n').find((line) => line.startsWith('To upgrade, run '));
  assert.match(line ?? '', /summary.*--plain/);
  const guide = (await readFile(path.join(ROOT, 'docs/getting-started.md'), 'utf8')).split('## Keeping it up to date\n')[1].split('\n## ')[0];
  assert.match(guide, /summary/);
  assert.match(guide, /--plain/);
  assert.ok(readme.trimEnd().split('\n').length < 250);
});

// ── parseUpgradeArgs ────────────────────────────────────────────────────────

test('parseUpgradeArgs: no args', () => {
  assert.deepEqual(parseUpgradeArgs([]), {});
});

test('parseUpgradeArgs: --source <path>', () => {
  assert.deepEqual(parseUpgradeArgs(['--source', '/some/path']), { source: '/some/path' });
});

for (const bad of [['--source'], ['--bogus', '/x'], ['--source', '/x', 'extra']]) {
  test(`parseUpgradeArgs rejects ${JSON.stringify(bad)}`, () => {
    assert.equal(parseUpgradeArgs(bad), null);
  });
}

// ── DEFAULT_SKILL_SOURCE ──────────────────────────────────────────────────────

test('DEFAULT_SKILL_SOURCE resolves to <package root>/skill', () => {
  assert.equal(DEFAULT_SKILL_SOURCE, path.join(ROOT, 'skill'));
});

// ── runUpgrade wiring: proves the CLI verb, not just the library it calls ────

test('runUpgrade re-points ALL 3 recorded installs at the given --source, via the VERB', async () => {
  const home = await tempDir('cf-home-');
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');
  const installsFile = installsPath(home);

  const targets = await Promise.all([1, 2, 3].map(async (i) => path.join(await tempDir(`cf-tgt${i}-`), 'code-forge')));
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target: targets[0] });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source: sourceA, target: targets[1] });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'symlink', source: sourceA, target: targets[2] });

  const sourceB = await tempDir('cf-src-b-');
  await writeMarker(sourceB, 'B');

  const stdout = sink();
  const code = await runUpgrade(['--source', sourceB], { stdout, stderr: sink(), home });

  assert.equal(code, 0);
  for (const t of targets) {
    assert.equal(await readlink(t), sourceB);
  }
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 3);
  assert.ok(records.every((r) => r.source === sourceB));
  const lines = stdout.text.split('\n');
  assert.deepEqual(lines.filter((l) => l.startsWith('upgraded ')), [
    `upgraded claude (project) -> ${sourceB}`,
    `upgraded codex (project) -> ${sourceB}`,
    `upgraded grok (global) -> ${sourceB}`,
  ]);
  assert.ok(lines.indexOf(`upgraded claude (project) -> ${sourceB}`) > 0, 'banner precedes records');
});

test('runUpgrade resolves a RELATIVE --source against cwd — links AND the ledger store the ABSOLUTE path, not the typed relative one', async () => {
  const home = await tempDir('cf-home-');
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');
  const installsFile = installsPath(home);
  const targets = await Promise.all([1, 2, 3].map(async (i) => path.join(await tempDir(`cf-tgt${i}-`), 'code-forge')));
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target: targets[0] });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source: sourceA, target: targets[1] });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'symlink', source: sourceA, target: targets[2] });

  const projectDir = await tempDir('cf-project-');
  await writeMarker(path.join(projectDir, 'my-skill'), 'B');
  const absoluteSource = path.resolve(projectDir, 'my-skill');

  const code = await runUpgrade(['--source', './my-skill'], { stdout: sink(), stderr: sink(), home, cwd: projectDir });

  assert.equal(code, 0);
  for (const t of targets) {
    assert.equal(await readlink(t), absoluteSource, 'the recorded symlink target must be the ABSOLUTE source, not the typed relative one');
    // realpath must resolve without throwing — proves the link is genuinely reachable, not dangling.
    assert.equal(await realpath(t), await realpath(absoluteSource));
  }
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 3);
  for (const record of records) {
    assert.equal(record.source, absoluteSource);
    assert.equal(path.isAbsolute(record.source), true);
  }
});

test('runUpgrade with a MISSING --source exits 1, writes nothing, and leaves all 3 links resolving at their OLD source', async () => {
  const home = await tempDir('cf-home-');
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');
  const installsFile = installsPath(home);
  const targets = await Promise.all([1, 2, 3].map(async (i) => path.join(await tempDir(`cf-tgt${i}-`), 'code-forge')));
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target: targets[0] });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source: sourceA, target: targets[1] });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'symlink', source: sourceA, target: targets[2] });

  const stderr = sink();
  const code = await runUpgrade(['--source', '/definitely/does/not/exist/anywhere/code-forge'], { stdout: sink(), stderr, home });

  assert.equal(code, 1);
  assert.match(stderr.text, /does not exist/);
  for (const t of targets) {
    assert.equal(await readlink(t), sourceA, 'every link must still point at the OLD source');
  }
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 3);
  assert.ok(records.every((r) => r.source === sourceA));
});

test('runUpgrade with a --source that EXISTS but is a FILE (not a directory) exits 1 and touches nothing', async () => {
  const home = await tempDir('cf-home-');
  const filePath = path.join(await tempDir('cf-file-'), 'not-a-dir');
  await writeFile(filePath, 'x', 'utf8');
  const stderr = sink();
  const code = await runUpgrade(['--source', filePath], { stdout: sink(), stderr, home });
  assert.equal(code, 1);
  assert.match(stderr.text, /not a directory/);
});

test('runUpgrade with no --source falls back to deps.defaultSource', async () => {
  const home = await tempDir('cf-home-');
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');
  const installsFile = installsPath(home);
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target });

  const fallback = await tempDir('cf-src-fallback-');
  await writeMarker(fallback, 'fallback');

  const code = await runUpgrade([], { stdout: sink(), stderr: sink(), home, defaultSource: fallback });
  assert.equal(code, 0);
  assert.equal(await readlink(target), fallback);
});

test('runUpgrade on an empty installs.json prints "nothing to upgrade" and exits 0', async () => {
  const home = await tempDir('cf-home-');
  const stdout = sink();
  const code = await runUpgrade([], { stdout, stderr: sink(), home, defaultSource: await tempDir('cf-src-') });
  assert.equal(code, 0);
  assert.equal(stdout.text.split('\n')[0], ' ___  __   __   ___       ___  __   __   __   ___');
  assert.equal(stdout.text.split('\n').filter((line) => line === 'nothing to upgrade').length, 1);
});

test('runUpgrade with bad args exits 2 with the usage line on stderr', async () => {
  const home = await tempDir('cf-home-');
  const stderr = sink();
  const code = await runUpgrade(['--bogus'], { stdout: sink(), stderr, home });
  assert.equal(code, 2);
  assert.equal(stderr.text, 'usage: code-forge upgrade [--source <path>] [--plain]\n');
});
