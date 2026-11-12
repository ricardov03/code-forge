/**
 * B23: `scripts/release.mjs`, run end to end as a child process. Every test works on a temp git
 * repository with a bare `origin` beside it, never on this repository; `npm`, `gh` and `claude`
 * are fakes on PATH (the fake `npm` never publishes and answers `view` with a fixed version).
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { accessSync, chmodSync, constants, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, describe, test } from 'node:test';
import { parse } from 'yaml';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SCRIPT = path.join(REPO, 'scripts', 'release.mjs');
const PARENT = mkdtempSync(path.join(os.tmpdir(), 'release-test-'));
writeFileSync(path.join(PARENT, 'package.json'), '{"type":"commonjs"}\n');
const FAKE_BIN = path.join(PARENT, 'bin');
let counter = 0;

/**
 * The first executable file `bin` on PATH, found in node: no shell and no child process.
 * @param {string} bin @returns {string}
 */
function which(bin) {
  for (const dir of (process.env.PATH ?? '').split(path.delimiter)) {
    if (!dir) continue;
    const candidate = path.join(dir, bin);
    try {
      accessSync(candidate, constants.X_OK);
      if (statSync(candidate).isFile()) return candidate;
    } catch { /* not here */ }
  }
  throw new Error(`test/release: no ${bin} on PATH`);
}

/** Every inherited `GIT_*` stripped, no system/global config, a fixed identity. */
function baseEnv() {
  const env = { ...process.env };
  for (const k of Object.keys(env)) if (k.startsWith('GIT_')) delete env[k];
  return {
    ...env,
    PATH: [FAKE_BIN, path.dirname(which('git')), path.dirname(process.execPath)].join(path.delimiter),
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: os.devNull,
    GIT_AUTHOR_NAME: 'Test', GIT_AUTHOR_EMAIL: 'test@example.invalid',
    GIT_COMMITTER_NAME: 'Test', GIT_COMMITTER_EMAIL: 'test@example.invalid',
  };
}
const ENV = baseEnv();

/** @param {string} cwd @param {string[]} args */
function git(cwd, args) {
  const r = spawnSync('git', args, { cwd, env: ENV, encoding: 'utf8', timeout: 60000 });
  assert.equal(r.status, 0, `git ${args.join(' ')}: ${r.stderr}`);
  return r.stdout;
}

/** @param {string} cwd @param {string[]} args @param {Record<string, string>} [extraEnv] */
function release(cwd, args, extraEnv = {}) {
  const r = spawnSync(process.execPath, [SCRIPT, ...args], { cwd, env: { ...ENV, ...extraEnv }, encoding: 'utf8', timeout: 120000 });
  return { code: r.status, stdout: r.stdout, stderr: r.stderr };
}

const FAKE_NPM = `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const a = process.argv.slice(2);
// FAKE_NPM_SEEN=<dir>: copy the release files as this check sees them into <dir>/<check>/
const seen = (check) => {
  const dir = process.env.FAKE_NPM_SEEN;
  if (!dir) return;
  fs.mkdirSync(path.join(dir, check), { recursive: true });
  for (const f of ['README.md', 'CHANGELOG.md', 'package.json', 'package-lock.json', 'docs/step-by-step.md']) {
    if (fs.existsSync(f)) {
      fs.mkdirSync(path.dirname(path.join(dir, check, f)), { recursive: true });
      fs.copyFileSync(f, path.join(dir, check, f));
    }
  }
};
if (a[0] === 'view') { process.stdout.write((process.env.FAKE_NPM_VIEW || '0.1.0') + '\\n'); process.exit(0); }
if (a[0] === 'test') {
  seen('test');
  if (process.env.FAKE_NPM_RETRY_LOG) {
    fs.appendFileSync(process.env.FAKE_NPM_RETRY_LOG, JSON.stringify(a) + '\\n');
    if (a.length > 1) {
      process.stdout.write('alone ' + a.at(-1) + '\\n');
      process.exit(Number(process.env.FAKE_NPM_ALONE_EXIT || 0));
    }
  }
  process.stdout.write(process.env.FAKE_NPM_TEST_STDOUT ?? 'fake npm test\\n');
  process.stderr.write(process.env.FAKE_NPM_TEST_STDERR ?? '');
  process.exit(Number(process.env.FAKE_NPM_TEST_EXIT || 0));
}
if (a[0] === 'run' && a[1] === 'typecheck') { seen('typecheck'); process.exit(Number(process.env.FAKE_NPM_TYPECHECK_EXIT || 0)); }
if (a[0] === 'pack') {
  const p = JSON.parse(fs.readFileSync('package.json', 'utf8'));
  process.stdout.write(JSON.stringify([{ name: p.name, version: p.version, entryCount: 3, files: [] }]));
  process.exit(0);
}
if (a[0] === 'ls') { process.stdout.write('{"name":"x","dependencies":{}}'); process.exit(0); }
process.stderr.write('fake npm: unexpected ' + a.join(' ') + '\\n'); process.exit(9);
`;
const FAKE_GH = `#!/usr/bin/env node
const fs = require('node:fs');
const { execFileSync } = require('node:child_process');
if (process.env.FAKE_GH_LOG) fs.appendFileSync(process.env.FAKE_GH_LOG, JSON.stringify(process.argv.slice(2)) + '\\n');
if (process.env.FAKE_GH_RUNS) {
  const headSha = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
  process.stdout.write(process.env.FAKE_GH_RUNS.replaceAll('<HEAD>', headSha));
  process.exit(0);
}
process.stderr.write('fake gh: unexpected arguments\\n'); process.exit(9);
`;
const FAKE_CLAUDE = `#!/bin/sh
if [ "$1" = plugin ] && [ "$2" = validate ]; then echo "Validation passed"; exit 0; fi
exit 9
`;

const CHANGELOG = `# Changelog

## [Unreleased]

### Added

- A new verb.

## [0.1.0] — first release

- First.
`;
const PKG = `${JSON.stringify({ name: '@example/pkg', version: '0.1.0', type: 'module',
  repository: { type: 'git', url: 'git+https://github.com/example/pkg.git' } }, null, 2)}\n`;
const SHRINKWRAP = `${JSON.stringify({ name: '@example/pkg', version: '0.1.0', lockfileVersion: 3, requires: true,
  packages: { '': { name: '@example/pkg', version: '0.1.0' } } }, null, 2)}\n`;
const PLUGIN = `${JSON.stringify({ name: 'pkg', version: '0.1.0' }, null, 2)}\n`;
const SHIM = '#!/bin/sh\n# shim: PINNED_VERSION="0.1.0" is set below\nPINNED_VERSION="0.1.0"\nset -eu\necho "npx pkg@$PINNED_VERSION"\n';
const NOTES_020 = '### Added\n\n- A new verb.\n\nnpm: https://www.npmjs.com/package/@example/pkg/v/0.2.0\n' +
  'Full changelog: https://github.com/example/pkg/blob/v0.2.0/CHANGELOG.md';
const FILES = ['package.json', 'npm-shrinkwrap.json', '.claude-plugin/plugin.json', 'skill/scripts/forge', 'CHANGELOG.md', 'README.md'];
const README = '# pkg\n\nA useful tool.\n\nLatest: 0.1.0 - see [CHANGELOG.md](CHANGELOG.md)\n';
/** Expected complete README bytes after minor and patch releases. */
const README_020 = '# pkg\n\nA useful tool.\n\nLatest: 0.2.0 - see [CHANGELOG.md](CHANGELOG.md)\n';
const README_011 = '# pkg\n\nA useful tool.\n\nLatest: 0.1.1 - see [CHANGELOG.md](CHANGELOG.md)\n';
const LOCK = JSON.stringify({ name: '@example/pkg', version: '0.1.0', lockfileVersion: 3, requires: true,
  packages: { '': { name: '@example/pkg', version: '0.1.0' } } }, null, 2) + '\n';
/** `## [Unreleased]` with an empty `### Added`. */
const NO_ENTRIES = '# Changelog\n\n## [Unreleased]\n\n### Added\n\n## [0.1.0] — first release\n\n- First.\n';
/** Every file a snapshot holds: the release files and a `package-lock.json` (null when absent). */
const SNAP_FILES = [...FILES, 'package-lock.json'];
/** A fake config-doc generator: exit 0 only for `--check` (exit 3 otherwise, so a missing flag fails). */
const GEN_DOC_OK = "process.exit(process.argv.slice(2).join(' ') === '--check' ? 0 : 3);\n";
const GEN_DOC_STALE = "process.stderr.write('gen-config-doc --check: docs/reference/config.md is stale (fake)\\n'); process.exit(1);\n";

/**
 * A fresh work repo with one pushed commit on main and a bare origin.
 * @param {{ changelog?: string, genDoc?: string | null, lock?: boolean, readme?: string, docs?: string }} [o] - `genDoc: null` writes no
 *   scripts/gen-config-doc.mjs; `lock: true` also commits a package-lock.json
 */
function makeRepo({ changelog = CHANGELOG, genDoc = GEN_DOC_OK, lock = false, readme = README, docs } = {}) {
  counter += 1;
  const dir = path.join(PARENT, `case-${counter}`);
  const origin = path.join(dir, 'origin.git');
  const work = path.join(dir, 'work');
  mkdirSync(work, { recursive: true });
  git(dir, ['init', '-q', '--bare', '-b', 'main', origin]);
  git(work, ['init', '-q', '-b', 'main']);
  mkdirSync(path.join(work, '.claude-plugin'));
  mkdirSync(path.join(work, 'skill', 'scripts'), { recursive: true });
  writeFileSync(path.join(work, 'package.json'), PKG);
  writeFileSync(path.join(work, 'npm-shrinkwrap.json'), SHRINKWRAP);
  writeFileSync(path.join(work, '.claude-plugin', 'plugin.json'), PLUGIN);
  writeFileSync(path.join(work, 'skill', 'scripts', 'forge'), SHIM);
  writeFileSync(path.join(work, 'CHANGELOG.md'), changelog);
  writeFileSync(path.join(work, 'README.md'), readme);
  writeFileSync(path.join(work, '.gitignore'), '*.log\n');
  if (docs !== undefined) {
    mkdirSync(path.join(work, 'docs'));
    writeFileSync(path.join(work, 'docs', 'step-by-step.md'), docs);
  }
  if (lock) writeFileSync(path.join(work, 'package-lock.json'), LOCK);
  if (genDoc !== null) {
    mkdirSync(path.join(work, 'scripts'));
    writeFileSync(path.join(work, 'scripts', 'gen-config-doc.mjs'), genDoc);
  }
  mkdirSync(path.join(work, 'scripts'), { recursive: true });
  writeFileSync(path.join(work, 'scripts', 'gen-commands-doc.mjs'), GEN_DOC_OK);
  git(work, ['add', '-A']);
  git(work, ['commit', '-q', '-m', 'base']);
  git(work, ['remote', 'add', 'origin', origin]);
  git(work, ['push', '-q', 'origin', 'main']);
  return { dir, origin, work };
}

/** Each file's bytes, or null when it does not exist. @param {string} work */
const snapshot = (work) => SNAP_FILES.map((f) => (existsSync(path.join(work, f)) ? readFileSync(path.join(work, f)) : null));
/** @param {string} work */
const state = (work) => ({
  head: git(work, ['rev-parse', 'HEAD']).trim(),
  commits: Number(git(work, ['rev-list', '--count', 'HEAD']).trim()),
  tags: git(work, ['tag', '--list']).trim(),
  status: git(work, ['status', '--porcelain']),
});
/** @param {string} origin */
const originRefs = (origin) => git(origin, ['for-each-ref', '--format=%(refname) %(objectname)']);

before(() => {
  mkdirSync(FAKE_BIN);
  writeFileSync(path.join(FAKE_BIN, 'package.json'), '{"type":"commonjs"}\n');
  for (const [name, body] of [['npm', FAKE_NPM], ['gh', FAKE_GH], ['claude', FAKE_CLAUDE]]) {
    writeFileSync(path.join(FAKE_BIN, name), body);
    chmodSync(path.join(FAKE_BIN, name), 0o755);
  }
});
after(() => rmSync(PARENT, { recursive: true, force: true }));

describe('scripts/release.mjs (B23)', () => {
  test('B345: reruns exactly two failing files once each, serially, and accepts only green HEAD CI', () => {
    for (const reporter of ['tap', 'spec']) {
      const { dir, work } = makeRepo();
      mkdirSync(path.join(work, 'test'));
      for (const file of ['a.test.mjs', 'b.test.mjs', 'passing.test.mjs']) writeFileSync(path.join(work, 'test', file), '// fixture\n');
      git(work, ['add', 'test']);
      git(work, ['commit', '-q', '-m', 'tests']);
      const head = git(work, ['rev-parse', 'HEAD']).trim();
      const log = path.join(dir, 'npm.jsonl');
      const ghLog = path.join(dir, 'gh.jsonl');
      const output = reporter === 'tap'
        ? "not ok 1 - first\n  location: 'test/a.test.mjs:2:1'\nnot ok 2 - second\n  location: 'test/b.test.mjs:3:1'\nnot ok 3 - duplicate\n  location: 'test/a.test.mjs:4:1'\n"
        : `✖ first (1ms)\nℹ failing tests:\n\ntest at test/a.test.mjs:2:1\n✖ first (1ms)\ntest at ${path.join(work, 'test/b.test.mjs')}:3:1\n✖ second (2ms)\ntest at test/a.test.mjs:4:1\n✖ duplicate (1ms)\n`;
      const r = release(work, ['patch'], {
        FAKE_NPM_TEST_EXIT: '1', FAKE_NPM_TEST_STDOUT: output, FAKE_NPM_RETRY_LOG: log,
        FAKE_GH_LOG: ghLog, FAKE_GH_RUNS: '[{"headSha":"<HEAD>","conclusion":"success","status":"completed"}]',
      });
      assert.equal(r.code, 0, r.stdout + r.stderr);
      assert.deepEqual(readFileSync(log, 'utf8').trim().split('\n').map((line) => JSON.parse(line)), [
        ['test'], ['test', '--', '--test-concurrency=1', 'test/a.test.mjs'], ['test', '--', '--test-concurrency=1', 'test/b.test.mjs'],
      ], 'one full run, then exactly one isolated serial run per failing file; passing file never reruns');
      assert.deepEqual(readFileSync(ghLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line)), [
        ['run', 'list', '--commit', head, '--workflow', 'CI', '--limit', '1', '--json', 'headSha,status,conclusion'],
      ], 'one CI query for the pre-release HEAD');
      assert.match(r.stdout, /npm test failed \(exit 1\)/);
      for (const file of ['a', 'b']) assert.match(r.stdout, new RegExp(`alone test/${file}\\.test\\.mjs`));
      assert.equal(r.stdout.split('\n').filter((line) => line === 'local flaky (passed alone, CI green): test/a.test.mjs, test/b.test.mjs').length, 1);
      assert.equal(state(work).tags, 'v0.1.1', 'successful retry releases exactly one tag');
    }
  });

  test('B345 boundary: red, absent, pending or foreign CI and a failing isolated rerun all refuse and roll back', () => {
    const green = '[{"headSha":"<HEAD>","conclusion":"success","status":"completed"}]';
    for (const [runs, alone] of [
      ['[{"headSha":"<HEAD>","conclusion":"failure","status":"completed"}]', '0'],
      ['[]', '0'],
      ['[{"headSha":"<HEAD>","conclusion":null,"status":"in_progress"}]', '0'],
      ['[{"headSha":"wrong","conclusion":"success","status":"completed"}]', '0'],
      [green, '1'],
    ]) {
      const { dir, work } = makeRepo();
      mkdirSync(path.join(work, 'test'));
      writeFileSync(path.join(work, 'test/a.test.mjs'), '// fixture\n');
      git(work, ['add', 'test']);
      git(work, ['commit', '-q', '-m', 'test']);
      const before = snapshot(work);
      const head = state(work).head;
      const log = path.join(dir, 'npm.jsonl');
      const r = release(work, ['patch'], {
        FAKE_NPM_TEST_EXIT: '1', FAKE_NPM_TEST_STDOUT: "not ok 1 - first\n  location: 'test/a.test.mjs:2:1'\n",
        FAKE_NPM_RETRY_LOG: log, FAKE_NPM_ALONE_EXIT: alone, FAKE_GH_RUNS: runs,
      });
      assert.equal(r.code, 1, r.stdout + r.stderr);
      assert.equal(existsSync(log), true, 'fake npm records the initial run');
      assert.equal(readFileSync(log, 'utf8').trim().split('\n').length, 2, 'full run plus exactly one rerun even when it stays red');
      assert.deepEqual(snapshot(work), before, 'all six release files restored byte-for-byte');
      assert.deepEqual([state(work).head, state(work).tags], [head, ''], 'zero commits and zero tags after refusal');
      assert.equal(r.stdout.includes('local flaky (passed alone, CI green):'), false, 'zero flaky success lines for rejected release');
      assert.match(r.stderr, /FAILED:/);
    }
  });

  test('B263: releases plan and bump both docs pins; npm test names every failure once, saves full output, and rolls back the docs', () => {
    const docs = '# Guide\n\nnpm install --save-dev --save-exact @codedology/code-forge@0.1.0\n\n@codedology/code-forge 0.1.0\n';
    const bumped = '# Guide\n\nnpm install --save-dev --save-exact @codedology/code-forge@0.2.0\n\n@codedology/code-forge 0.2.0\n';
    const { dir, work } = makeRepo({ docs });
    const files = snapshot(work);
    const st = state(work);
    const seen = path.join(dir, 'seen');
    const noise = Array.from({ length: 100 }, (_, i) => `noise ${i + 1}`);
    const stdout = [
      '✖ outside summary (1ms)',
      '✖ failing tests:',
      '✖ keeps docs current (1.25ms)',
      '✖ keeps docs current (2ms)',
      '  ✖ reports nested failure (3ms)',
      'not ok 1 - keeps docs current',
      '    not ok 2 - TAP only failure',
      ...noise,
      '',
    ].join('\n');
    const stderr = 'stderr sentinel\n';
    const failed = release(work, ['minor'], {
      FAKE_NPM_TEST_EXIT: '1', FAKE_NPM_TEST_STDOUT: stdout, FAKE_NPM_TEST_STDERR: stderr, FAKE_NPM_SEEN: seen,
    });
    assert.equal(failed.code, 1, failed.stderr);
    const names = ['keeps docs current', 'reports nested failure', 'TAP only failure'];
    assert.deepEqual(failed.stderr.split('\n').filter((l) => l.startsWith('  - ')), names.map((n) => `  - ${n}`), 'exactly 3 distinct failing names in first-seen order');
    const logs = [...failed.stderr.matchAll(/^Full output: (.+)$/gm)];
    assert.equal(logs.length, 1, 'exactly 1 full-output log path');
    const log = logs[0][1];
    assert.equal(path.dirname(log), path.join(work, '.code-forge-tmp'), 'log stays in the fixture repo temp area');
    assert.equal(git(work, ['check-ignore', '--', log]).trim(), log, 'the full log is ignored by git');
    assert.equal(readFileSync(log, 'utf8'), stdout + stderr, 'full log preserves all stdout and stderr bytes');
    assert.equal(failed.stderr, `\nFAILED: npm test failed (exit 1)\nfailing tests:\n${names.map((n) => `  - ${n}`).join('\n')}\nFull output: ${log}\nLast 30 lines:\n${[...noise.slice(-29), 'stderr sentinel'].join('\n')}\nrolled back 7 files; nothing was committed or tagged\n`);
    assert.equal(readFileSync(path.join(seen, 'test', 'docs', 'step-by-step.md'), 'utf8'), bumped, 'npm test saw both bumped pins');
    assert.equal(readFileSync(path.join(work, 'docs', 'step-by-step.md'), 'utf8'), docs, 'rollback restores every docs byte');
    assert.deepEqual(snapshot(work), files);
    assert.deepEqual(state(work), st, 'rollback leaves the same HEAD, tags and clean tree');

    const dry = release(work, ['minor', '--dry-run']);
    assert.equal(dry.code, 0, dry.stderr);
    assert.equal(dry.stdout.split('\ndocs/step-by-step.md: ').length - 1, 1, 'docs appears once in the plan');
    assert.match(dry.stdout, /^    \+ npm install --save-dev --save-exact @codedology\/code-forge@0\.2\.0$/m);
    assert.match(dry.stdout, /^    \+ @codedology\/code-forge 0\.2\.0$/m);
    assert.match(dry.stdout, /would commit "Release v0\.2\.0" \(7 files\)/);
    assert.equal(readFileSync(path.join(work, 'docs', 'step-by-step.md'), 'utf8'), docs, 'dry run writes zero docs bytes');
    assert.deepEqual(state(work), st);
    const passed = release(work, ['minor']);
    assert.equal(passed.code, 0, passed.stderr);
    assert.equal(readFileSync(path.join(work, 'docs', 'step-by-step.md'), 'utf8'), bumped, 'only the two pins change');
    assert.deepEqual(git(work, ['show', '--name-only', '--format=', 'HEAD']).trim().split('\n').sort(), [...FILES, 'docs/step-by-step.md'].sort(), 'commit includes exactly 7 release files');
    assert.equal(state(work).status, '', 'ignored failure log does not dirty the next release');
  });

  test('B263 boundary: docs edits preserve CRLF and unrelated versions; npm test without named failures still saves all output and the tail', () => {
    const unchanged = 'history 0.1.0\r\n@codedology/code-forge 0.1.0 extra\r\n@codedology/code-forge 0.0.9\r\n@codedology/code-forge@0.1.00\r\n@codedology/code-forge@0.1.0-beta\r\n@codedology/code-forge@0x1x0\r\n';
    const docs = `npm install --save-dev --save-exact @codedology/code-forge@0.1.0\r\n@codedology/code-forge 0.1.0\r\n${unchanged}`;
    const bumped = `npm install --save-dev --save-exact @codedology/code-forge@0.2.0\r\n@codedology/code-forge 0.2.0\r\n${unchanged}`;
    const { work } = makeRepo({ docs });
    const passed = release(work, ['minor']);
    assert.equal(passed.code, 0, passed.stderr);
    assert.equal(readFileSync(path.join(work, 'docs', 'step-by-step.md'), 'utf8'), bumped, 'exactly two complete current-version pins change, preserving all other bytes');
    const changelog = path.join(work, 'CHANGELOG.md');
    writeFileSync(changelog, readFileSync(changelog, 'utf8').replace('## [Unreleased]\n', '## [Unreleased]\n\n### Fixed\n\n- Next fix.\n'));
    git(work, ['commit', '-q', '-am', 'next release entry']);
    stampRepo(work, 'v0.2.0');
    const st = state(work);
    const failed = release(work, ['patch'], { FAKE_NPM_TEST_EXIT: '1' });
    assert.equal(failed.code, 1, failed.stderr);
    const logs = [...failed.stderr.matchAll(/^Full output: (.+)$/gm)];
    assert.equal(logs.length, 1, 'exactly 1 log even with zero named failures');
    const log = logs[0][1];
    assert.equal(path.dirname(log), path.join(work, '.code-forge-tmp'));
    assert.equal(readFileSync(log, 'utf8'), 'fake npm test\n');
    assert.equal(failed.stderr, `\nFAILED: npm test failed (exit 1)\nFull output: ${log}\nLast 30 lines:\nfake npm test\nrolled back 7 files; nothing was committed or tagged\n`, 'zero invented names and the complete short tail');
    assert.equal(readFileSync(path.join(work, 'docs', 'step-by-step.md'), 'utf8'), bumped, 'second-release rollback restores the current docs pins');
    assert.deepEqual(state(work), st);
  });

  test('--dry-run shows 0.1.0 → 0.2.0 and the README diff, and changes 0 files, 0 commits, 0 tags; the same release then passes', () => {
    const { work, origin } = makeRepo();
    const files = snapshot(work);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor', '--dry-run', '--date', '2026-09-26']);
    assert.equal(r.code, 0, r.stderr);
    assert.match(r.stdout, /0\.1\.0 → 0\.2\.0/);
    assert.match(r.stdout, /\+ ## \[0\.2\.0\] — 2026-09-26/);
    assert.match(r.stdout, /\+ PINNED_VERSION="0\.2\.0"/);
    // The README diff must contain exactly the Latest update and its preceding blank line.
    assert.equal(r.stdout.split('\nREADME.md: ').length - 1, 1, r.stdout);
    assert.equal(r.stdout.slice(r.stdout.indexOf('\nREADME.md: ') + 1, r.stdout.indexOf('\n== plan (dry run) ==\n')), [
      'README.md: Latest: 0.1.0 → 0.2.0',
      '      ',
      '    - Latest: 0.1.0 - see [CHANGELOG.md](CHANGELOG.md)',
      '    + Latest: 0.2.0 - see [CHANGELOG.md](CHANGELOG.md)',
      '',
    ].join('\n'));
    assert.match(r.stdout, /^would run the checks: gen-config-doc --check, gen-commands-doc --check, npm test, npm run typecheck, npm pack --dry-run, npm ls --json, claude plugin validate \.$/m);
    assert.match(r.stdout, /^would commit "Release v0\.2\.0" \(6 files\)/m);
    // nothing written: every file byte-equal, no commit, no tag, origin untouched
    assert.deepEqual(snapshot(work), files);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README);
    assert.deepEqual(state(work), st);
    assert.equal(st.tags, '');
    assert.equal(originRefs(origin), refs);
    // a release that passes: the same command without --dry-run succeeds and writes the README the diff showed
    const real = release(work, ['minor', '--date', '2026-09-26']);
    assert.equal(real.code, 0, `${real.stdout}\n${real.stderr}`);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README_020);
  });

  test('minor: 0.2.0 in all 4 locations, CHANGELOG released and Latest updated, 1 commit of 6 files, 1 annotated tag, nothing pushed', () => {
    const { work, origin } = makeRepo();
    const before = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor', '--date', '2026-09-26']);
    assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);

    assert.equal(readFileSync(path.join(work, 'package.json'), 'utf8'), PKG.replace('"0.1.0"', '"0.2.0"'));
    assert.equal(readFileSync(path.join(work, 'npm-shrinkwrap.json'), 'utf8'), SHRINKWRAP.replaceAll('"0.1.0"', '"0.2.0"'));
    assert.equal(readFileSync(path.join(work, '.claude-plugin', 'plugin.json'), 'utf8'), PLUGIN.replace('"0.1.0"', '"0.2.0"'));
    assert.equal(readFileSync(path.join(work, 'skill', 'scripts', 'forge'), 'utf8'), SHIM.replace('\nPINNED_VERSION="0.1.0"\n', '\nPINNED_VERSION="0.2.0"\n'));
    assert.equal(
      readFileSync(path.join(work, 'CHANGELOG.md'), 'utf8'),
      CHANGELOG.replace('## [Unreleased]\n', '## [Unreleased]\n\n## [0.2.0] — 2026-09-26\n'),
    );
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README_020);
    assert.match(r.stdout, /^ok {4}gen-config-doc --check$/m);

    const after = state(work);
    assert.equal(after.commits, before.commits + 1);
    assert.equal(git(work, ['log', '-1', '--format=%s']).trim(), 'Release v0.2.0');
    assert.equal(git(work, ['show', '--name-only', '--format=', 'HEAD']).trim().split('\n').sort().join(','), [...FILES].sort().join(','));
    assert.equal(after.status, '');
    assert.equal(after.tags, 'v0.2.0');
    assert.equal(git(work, ['cat-file', '-t', 'v0.2.0']).trim(), 'tag');
    assert.equal(git(work, ['rev-parse', 'v0.2.0^{commit}']).trim(), after.head);
    const tagObject = git(work, ['cat-file', 'tag', 'v0.2.0']);
    assert.equal(tagObject.slice(tagObject.indexOf('\n\n') + 2), 'Release v0.2.0\n\n### Added\n\n- A new verb.\n');

    // one path: publish by hand, then push (CI never publishes; the tag push only makes the GitHub release)
    assert.match(r.stdout, /^ {2}publish to npm first, then push \(the tag push only creates the GitHub release; CI never publishes\):\n {6}npm publish\n {6}npm run release:site\n {6}git push origin main --follow-tags$/m);
    assert.equal(r.stdout.split('git push origin main --follow-tags').length - 1, 1);
    assert.equal(r.stdout.includes('NPM_TOKEN'), false);
    const cmd = /^ {6}(gh release create .*)$/m.exec(r.stdout)?.[1];
    const notes = /--notes-file (\S+)/.exec(cmd)?.[1];
    assert.equal(cmd, `gh release create v0.2.0 --title v0.2.0 --notes-file ${notes} --verify-tag`);
    assert.equal(readFileSync(notes, 'utf8'), `${NOTES_020}\n`);
    assert.equal(originRefs(origin), refs);
    assert.equal(git(origin, ['tag', '--list']).trim(), '');
  });

  test('an explicit version not greater than the current one is refused (equal and lower)', () => {
    const { work } = makeRepo();
    for (const v of ['0.1.0', '0.0.9']) {
      const r = release(work, [v]);
      assert.equal(r.code, 1);
      assert.match(r.stderr, new RegExp(`REFUSED: the new version ${v.replaceAll('.', '\\.')} is not greater than the current 0\\.1\\.0`));
    }
    const r = release(work, ['0.1.5'], { FAKE_NPM_VIEW: '0.3.0' });
    assert.equal(r.code, 1);
    assert.match(r.stderr, /not greater than the latest on npm \(0\.3\.0\)/);
    assert.equal(state(work).commits, 1);
  });

  test('a dirty tree is refused', () => {
    const { work } = makeRepo();
    writeFileSync(path.join(work, 'stray.txt'), 'x');
    const r = release(work, ['patch']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /REFUSED: the working tree is not clean:\n\?\? stray\.txt/);
    assert.equal(readFileSync(path.join(work, 'package.json'), 'utf8'), PKG);
  });

  test('behind origin is refused', () => {
    const { dir, work, origin } = makeRepo();
    const other = path.join(dir, 'other');
    git(dir, ['clone', '-q', origin, other]);
    writeFileSync(path.join(other, 'new.txt'), 'x');
    git(other, ['add', 'new.txt']);
    git(other, ['commit', '-q', '-m', 'upstream']);
    git(other, ['push', '-q', 'origin', 'main']);
    const r = release(work, ['patch']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /REFUSED: main is behind origin\/main/);
  });

  test('an existing tag is refused, local or only on origin', () => {
    const local = makeRepo();
    git(local.work, ['tag', 'v0.1.1']);
    let r = release(local.work, ['patch']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /REFUSED: the tag v0\.1\.1 already exists locally/);

    const remote = makeRepo();
    const other = path.join(remote.dir, 'other');
    git(remote.dir, ['clone', '-q', remote.origin, other]);
    git(other, ['tag', 'v0.1.1']);
    git(other, ['push', '-q', 'origin', 'v0.1.1']);
    r = release(remote.work, ['patch']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /REFUSED: the tag v0\.1\.1 already exists on origin/);
  });

  test('an empty [Unreleased] is refused in preflight: README, CHANGELOG and the package files byte-unchanged; HEAD and tags unchanged', () => {
    const readme = README + "\n## What's new in 0.1\n\nIn the next release:\n\n- A pending change.\n";
    const { work, origin } = makeRepo({ changelog: NO_ENTRIES, lock: true, readme });
    const files = snapshot(work);
    assert.equal(files.filter((b) => b === null).length, 0);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['patch']);
    assert.equal(r.code, 1);
    assert.equal(r.stderr, '\nREFUSED: CHANGELOG.md: "## [Unreleased]" has no entries: add one with npm run changelog -- added "…"\n');
    // all 7 files byte-for-byte as before, and each of the B108 ones against its written-out text
    assert.deepEqual(snapshot(work), files);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), readme);
    assert.equal(readFileSync(path.join(work, 'CHANGELOG.md'), 'utf8'), NO_ENTRIES);
    assert.equal(readFileSync(path.join(work, 'package.json'), 'utf8'), PKG);
    assert.equal(readFileSync(path.join(work, 'npm-shrinkwrap.json'), 'utf8'), SHRINKWRAP);
    assert.equal(readFileSync(path.join(work, 'package-lock.json'), 'utf8'), LOCK);
    // the same HEAD, still 1 commit, no tag, a clean tree; origin untouched
    assert.deepEqual(state(work), st);
    assert.equal(st.commits, 1);
    assert.equal(st.tags, '');
    assert.equal(st.status, '');
    assert.equal(originRefs(origin), refs);
  });

  test('a failing check (npm test exits 1) rolls back all 6 files byte-for-byte: no commit, no tag', () => {
    const { work, origin } = makeRepo();
    const files = snapshot(work);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor'], { FAKE_NPM_TEST_EXIT: '1' });
    assert.equal(r.code, 1);
    assert.match(r.stderr, /FAILED: npm test failed \(exit 1\)/);
    assert.match(r.stderr, /rolled back 6 files; nothing was committed or tagged/);
    assert.deepEqual(snapshot(work), files);
    assert.deepEqual(state(work), st);
    assert.equal(originRefs(origin), refs);
  });

  test('B108: a check that fails after README.md and the other files were rewritten (npm test or npm run typecheck exits 1) restores README, CHANGELOG, package.json and package-lock.json byte-for-byte: 7 files, no commit, no tag', () => {
    const cases = [
      ['test', 'FAKE_NPM_TEST_EXIT', 'npm test failed (exit 1)'],
      ['typecheck', 'FAKE_NPM_TYPECHECK_EXIT', 'npm run typecheck failed (exit 1)'],
    ];
    for (const [check, exitVar, why] of cases) {
      const { dir, work, origin } = makeRepo({ lock: true });
      const files = snapshot(work);
      assert.equal(files.filter((b) => b === null).length, 0);
      const st = state(work);
      const refs = originRefs(origin);
      const seen = path.join(dir, 'seen');
      const r = release(work, ['minor', '--date', '2026-09-26'], { [exitVar]: '1', FAKE_NPM_SEEN: seen });
      assert.equal(r.code, 1, `${check}: ${r.stdout}\n${r.stderr}`);
      let detail = why;
      if (check === 'test') {
        const logs = [...r.stderr.matchAll(/^Full output: (.+)$/gm)];
        assert.equal(logs.length, 1, 'npm test failure prints exactly 1 full-output path');
        detail += `\nFull output: ${logs[0][1]}\nLast 30 lines:\nfake npm test`;
      }
      assert.equal(r.stderr, `\nFAILED: ${detail}\nrolled back 7 files; nothing was committed or tagged\n`);
      // the failing check ran on the rewritten files (the fake npm copied them as it saw them)
      const saw = (/** @type {string} */ f) => readFileSync(path.join(seen, check, f), 'utf8');
      assert.equal(saw('README.md'), README_020, check);
      assert.equal(saw('CHANGELOG.md'), CHANGELOG.replace('## [Unreleased]\n', '## [Unreleased]\n\n## [0.2.0] — 2026-09-26\n'), check);
      assert.equal(saw('package.json'), PKG.replace('"0.1.0"', '"0.2.0"'), check);
      assert.equal(saw('package-lock.json'), LOCK.replaceAll('"0.1.0"', '"0.2.0"'), check);
      // after the failure: all 7 files byte-for-byte as before, the 4 named ones against their written-out text
      assert.deepEqual(snapshot(work), files, check);
      assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README, check);
      assert.equal(readFileSync(path.join(work, 'CHANGELOG.md'), 'utf8'), CHANGELOG, check);
      assert.equal(readFileSync(path.join(work, 'package.json'), 'utf8'), PKG, check);
      assert.equal(readFileSync(path.join(work, 'package-lock.json'), 'utf8'), LOCK, check);
      // no commit, no tag, a clean tree, origin untouched
      assert.deepEqual(state(work), st, check);
      assert.equal(st.tags, '');
      assert.equal(originRefs(origin), refs, check);
    }
  });

  test('B108: a stale config doc (gen-config-doc --check exits 1) or no script fails the checks: 6 files rolled back, README byte-identical, no commit, no tag', () => {
    const cases = /** @type {Array<[string | null, string]>} */ ([
      [GEN_DOC_STALE, 'gen-config-doc --check failed (exit 1):\ngen-config-doc --check: docs/reference/config.md is stale (fake)'],
      [null, 'gen-config-doc --check failed: scripts/gen-config-doc.mjs not found'],
    ]);
    for (const [genDoc, why] of cases) {
      const { work, origin } = makeRepo({ genDoc });
      const files = snapshot(work);
      const st = state(work);
      const refs = originRefs(origin);
      const r = release(work, ['minor']);
      assert.equal(r.code, 1);
      assert.equal(r.stderr, `\nFAILED: ${why}\nrolled back 6 files; nothing was committed or tagged\n`);
      assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README);
      assert.deepEqual(snapshot(work), files);
      assert.deepEqual(state(work), st);
      assert.equal(st.tags, '');
      assert.equal(originRefs(origin), refs);
    }
  });

  test('B313: stale command reference fails after config check and restores all eight changed files', () => {
    const { work, origin } = makeRepo({ lock: true, docs: '@codedology/code-forge 0.1.0\n' });
    const files = {
      'scripts/gen-commands-doc.mjs': readFileSync(path.join(REPO, 'scripts/gen-commands-doc.mjs'), 'utf8'),
      'bin/code-forge.mjs': "export async function listVerbs() { return ['sample']; }\n",
      'src/cli/sample.mjs': "export const USAGE = 'usage: sample'; export const SUMMARY = 'Sample command.';\n",
      'docs/reference/commands.md': '# Stale command reference\n',
    };
    for (const [file, text] of Object.entries(files)) {
      mkdirSync(path.dirname(path.join(work, file)), { recursive: true });
      writeFileSync(path.join(work, file), text);
    }
    git(work, ['add', '-A']);
    git(work, ['commit', '-qm', 'stale command reference']);
    const before = snapshot(work);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor']);
    assert.equal(r.code, 1, 'stale commands must fail the release');
    assert.match(r.stdout, /^ok {4}gen-config-doc --check$/m, 'config check runs first');
    assert.match(r.stderr, /gen-commands-doc --check failed \(exit 1\):\ngen-commands-doc --check: command reference is stale/);
    assert.match(r.stderr, /rolled back 8 files; nothing was committed or tagged/);
    assert.equal(r.stdout.includes('check npm test'), false, 'zero npm tests after stale docs');
    assert.deepEqual(snapshot(work), before, 'all seven version/README files restored');
    assert.equal(readFileSync(path.join(work, 'docs/step-by-step.md'), 'utf8'), '@codedology/code-forge 0.1.0\n', 'eighth file restored');
    for (const [file, text] of Object.entries(files)) assert.equal(readFileSync(path.join(work, file), 'utf8'), text, file);
    assert.deepEqual(state(work), st, 'zero new commits, tags or changed files');
    assert.equal(originRefs(origin), refs, 'zero remote changes');
  });

  test('B313 boundary: missing command generator refuses and restores every release file', () => {
    const { work } = makeRepo();
    git(work, ['rm', 'scripts/gen-commands-doc.mjs']);
    git(work, ['commit', '-qm', 'missing command generator']);
    const before = snapshot(work);
    const st = state(work);
    const r = release(work, ['minor']);
    assert.equal(r.code, 1, 'missing generator must fail the release');
    assert.match(r.stderr, /gen-commands-doc --check failed: scripts\/gen-commands-doc.mjs not found/);
    assert.match(r.stderr, /rolled back 6 files/);
    assert.deepEqual(snapshot(work), before, 'all six changed files restored');
    assert.deepEqual(state(work), st, 'zero new commits, tags or changed files');
  });

  test('notes <vX.Y.Z> prints the section plus the npm and changelog links; --out writes it; a missing version exits 1', () => {
    const { work } = makeRepo();
    let r = release(work, ['notes', 'v0.1.0']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(r.stdout, '- First.\n\nnpm: https://www.npmjs.com/package/@example/pkg/v/0.1.0\n' +
      'Full changelog: https://github.com/example/pkg/blob/v0.1.0/CHANGELOG.md\n');
    r = release(work, ['notes', 'v0.1.0', '--out', 'notes.md']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(readFileSync(path.join(work, 'notes.md'), 'utf8'), '- First.\n\nnpm: https://www.npmjs.com/package/@example/pkg/v/0.1.0\n' +
      'Full changelog: https://github.com/example/pkg/blob/v0.1.0/CHANGELOG.md\n');
    r = release(work, ['notes', 'v0.9.0']);
    assert.equal(r.code, 1);
    assert.equal(r.stderr, 'REFUSED: CHANGELOG.md has no "## [0.9.0]" section\n');
  });

  test('release.yml: one github-release job with exactly {contents: write}; it never publishes to npm; every run block parses', () => {
    const raw = readFileSync(path.join(REPO, '.github', 'workflows', 'release.yml'), 'utf8');
    assert.equal(existsSync(path.join(REPO, '.github', 'workflows', 'publish.yml')), false);
    const wf = parse(raw);
    assert.deepEqual(wf.on, { push: { tags: ['v*'] } });
    assert.deepEqual(Object.keys(wf.jobs), ['github-release']);
    const job = wf.jobs['github-release'];
    assert.deepEqual(job.permissions, { contents: 'write' });
    for (const word of ['npm publish', 'NPM_TOKEN', 'id-token', 'NODE_AUTH_TOKEN']) assert.equal(raw.includes(word), false, word);
    const runs = job.steps.filter((st) => st.run).map((st) => st.run);
    assert.equal(runs.length, 3);
    assert.match(runs[0], /if \[ "v\$\{version\}" != "\$\{GITHUB_REF_NAME\}" \]; then/);
    assert.equal(runs[1], 'node scripts/release.mjs notes "$GITHUB_REF_NAME" --out notes.md');
    assert.match(runs[2], /gh release create "\$GITHUB_REF_NAME" --title "\$GITHUB_REF_NAME" --notes-file notes\.md --verify-tag/);
    assert.match(runs[2], /gh release edit "\$GITHUB_REF_NAME"/);
    assert.equal(job.steps.find((st) => st.run === runs[2]).env.GH_TOKEN, '${{ github.token }}');
  });

  test('the shim bump is a line edit: every other byte is the same and exactly 1 PINNED_VERSION line remains', () => {
    const { work } = makeRepo();
    const r = release(work, ['patch', '--no-checks']);
    assert.equal(r.code, 0, r.stderr);
    const shim = readFileSync(path.join(work, 'skill', 'scripts', 'forge'), 'utf8');
    assert.equal(shim, '#!/bin/sh\n# shim: PINNED_VERSION="0.1.0" is set below\nPINNED_VERSION="0.1.1"\nset -eu\necho "npx pkg@$PINNED_VERSION"\n');
    assert.equal(shim.split('\n').filter((l) => l.startsWith('PINNED_VERSION=')).length, 1);
    assert.match(r.stdout, /--no-checks: gen-config-doc --check, gen-commands-doc --check, npm test, typecheck, npm pack, npm ls and plugin validate were NOT run/);
  });

  test('patch updates only Latest in README (exact bytes), releases CHANGELOG, and creates 1 commit of 6 files and 1 tag', () => {
    const { work, origin } = makeRepo();
    const before = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['patch', '--date', '2026-09-26']);
    assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);
    assert.equal(readFileSync(path.join(work, 'README.md'), 'utf8'), README_011);
    assert.equal(
      readFileSync(path.join(work, 'CHANGELOG.md'), 'utf8'),
      CHANGELOG.replace('## [Unreleased]\n', '## [Unreleased]\n\n## [0.1.1] — 2026-09-26\n'),
    );
    const after = state(work);
    assert.equal(after.commits, before.commits + 1);
    assert.equal(git(work, ['log', '-1', '--format=%s']).trim(), 'Release v0.1.1');
    assert.equal(git(work, ['show', '--name-only', '--format=', 'HEAD']).trim().split('\n').sort().join(','), [...FILES].sort().join(','));
    assert.equal(git(work, ['show', 'HEAD:README.md']), README_011);
    assert.equal(after.status, '');
    assert.equal(after.tags, 'v0.1.1');
    assert.equal(originRefs(origin), refs);
  });

  test('a second PINNED_VERSION line is refused and nothing changes', () => {
    const { work } = makeRepo();
    writeFileSync(path.join(work, 'skill', 'scripts', 'forge'), `${SHIM}PINNED_VERSION="0.1.0"\n`);
    git(work, ['commit', '-q', '-am', 'two pins']);
    const st = state(work);
    const r = release(work, ['patch']);
    assert.equal(r.code, 1);
    assert.equal(r.stderr, '\nREFUSED: skill/scripts/forge: expected exactly 1 PINNED_VERSION="…" line, found 2\n');
    assert.deepEqual(state(work), st);
  });

  test('on a feature branch the release is refused; nothing changes', () => {
    const { work, origin } = makeRepo();
    git(work, ['checkout', '-q', '-b', 'feat']);
    const files = snapshot(work);
    const st = state(work);
    const refs = originRefs(origin);
    const r = release(work, ['minor']);
    assert.equal(r.code, 1);
    assert.equal(r.stderr, '\nREFUSED: on branch "feat", expected "main" (use --allow-branch feat to release from it)\n');
    assert.deepEqual(snapshot(work), files);
    assert.deepEqual(state(work), st);
    assert.equal(originRefs(origin), refs);
  });

  test('--allow-branch feat gets past the branch check (dry run)', () => {
    const { work } = makeRepo();
    git(work, ['checkout', '-q', '-b', 'feat']);
    git(work, ['push', '-q', 'origin', 'feat']);
    const st = state(work);
    const r = release(work, ['minor', '--dry-run', '--allow-branch', 'feat']);
    assert.equal(r.code, 0, `${r.stdout}\n${r.stderr}`);
    assert.match(r.stdout, /^ok {4}on branch feat$/m);
    assert.match(r.stdout, /^ok {4}up to date with origin\/feat$/m);
    assert.match(r.stdout, /^would print: git push origin feat --follow-tags$/m);
    assert.deepEqual(state(work), st);
  });

  test('npm pack --dry-run ships neither release tool but still ships scripts/first-use.mjs, scripts/install.sh (B569) and scripts/setup.mjs (B463)', () => {
    const res = spawnSync('npm', ['pack', '--dry-run', '--json'], { cwd: REPO, encoding: 'utf8', timeout: 120000 });
    assert.equal(res.status, 0, res.stderr);
    const paths = JSON.parse(res.stdout)[0].files.map((/** @type {{path: string}} */ f) => f.path);
    assert.deepEqual(paths.filter((p) => p.startsWith('scripts/')).sort(), ['scripts/first-use.mjs', 'scripts/gen-config-doc.mjs', 'scripts/install.sh', 'scripts/setup.mjs']);
  });
});

/** Commit a fixture stamp, covering the current HEAD and its latest release tag. @param {string} work @param {string} [tag] */
function stampRepo(work, tag = 'v0.1.0') {
  const through = git(work, ['rev-parse', 'HEAD']).trim();
  mkdirSync(path.join(work, 'docs'), { recursive: true });
  writeFileSync(path.join(work, 'docs/release-docs.json'), JSON.stringify({ through, tag }) + '\n');
  git(work, ['add', 'docs/release-docs.json']);
  git(work, ['commit', '-qm', 'docs stamp']);
  return through;
}

/** @param {string} work @param {string} file @param {string} text */
function commitFile(work, file, text) {
  mkdirSync(path.dirname(path.join(work, file)), { recursive: true });
  writeFileSync(path.join(work, file), text);
  git(work, ['add', '--', file]);
  git(work, ['commit', '-qm', `change ${file}`]);
}

describe('B312 release docs preflight', () => {
  test('refuses missing, invalid and pre-release stamps in real and dry-run mode with zero commits and tags', () => {
    for (const kind of ['missing', 'invalid JSON', 'null', 'missing fields', 'wrong field type', 'older than release']) {
      const { work, origin } = makeRepo();
      const through = git(work, ['rev-parse', 'HEAD']).trim();
      git(work, ['tag', 'v0.1.0']);
      if (kind === 'older than release') {
        git(work, ['tag', '-d', 'v0.1.0']);
        git(work, ['tag', 'v0.0.9']);
        commitFile(work, 'README.md', README + '\nReleased update.\n');
        git(work, ['tag', '-a', 'v0.1.0', '-m', 'latest release']);
      }
      if (kind !== 'missing') {
        const text = kind === 'invalid JSON' ? '{broken' : kind === 'null' ? 'null' :
          kind === 'missing fields' ? '{}' : kind === 'wrong field type' ? JSON.stringify({ through: 42, tag: 'v0.1.0' }) :
            JSON.stringify({ through, tag: 'v0.0.9' });
        commitFile(work, 'docs/release-docs.json', text);
      }
      const st = state(work);
      const files = snapshot(work);
      const refs = originRefs(origin);
      for (const args of [['patch'], ['patch', '--dry-run']]) {
        const r = release(work, args);
        assert.equal(r.code, 1, `${kind}: ${r.stdout}\n${r.stderr}`);
        assert.match(r.stderr, /npm run docs-block -- brief/, kind);
        assert.match(r.stderr, /docs\/release-docs\.json/, kind);
        if (kind === 'older than release') assert.match(r.stderr, /older than.*v0\.1\.0/, kind);
        if (args.includes('--dry-run')) {
          assert.equal(r.stdout.split('\n').filter((line) => line.startsWith('FAIL  ') && line.includes('docs/release-docs.json')).length, 1, 'one docs preflight failure in dry-run list');
          assert.match(r.stdout, /dry run: 0 files changed, 0 commits, 0 tags/);
        }
        assert.deepEqual(state(work), st, `${kind}: zero commits, zero tags, zero worktree changes`);
        assert.deepEqual(snapshot(work), files, `${kind}: zero release file edits`);
        assert.equal(originRefs(origin), refs, 'zero remote mutations');
      }
    }
  });

  test('boundary: refuses unknown, non-commit and non-ancestor through values and a mismatched stamp tag', () => {
    for (const kind of ['unknown', 'blob', 'future commit', 'wrong tag']) {
      const { work } = makeRepo();
      git(work, ['tag', 'v0.1.0']);
      let through = git(work, ['rev-parse', 'HEAD']).trim();
      if (kind === 'unknown') through = '0'.repeat(40);
      if (kind === 'blob') through = git(work, ['rev-parse', 'HEAD:README.md']).trim();
      if (kind === 'future commit') {
        git(work, ['checkout', '-qb', 'future']);
        git(work, ['commit', '--allow-empty', '-qm', 'future']);
        through = git(work, ['rev-parse', 'HEAD']).trim();
        git(work, ['checkout', '-q', 'main']);
      }
      commitFile(work, 'docs/release-docs.json', JSON.stringify({ through, tag: kind === 'wrong tag' ? 'v0.0.9' : 'v0.1.0' }));
      const st = state(work);
      for (const args of [['patch'], ['patch', '--dry-run']]) {
        const r = release(work, args);
        assert.equal(r.code, 1, `${kind}: ${r.stdout}\n${r.stderr}`);
        assert.match(r.stderr, /npm run docs-block -- brief/, kind);
        assert.deepEqual(state(work), st, `${kind}: zero commits and tags`);
      }
    }
  });

  test('code after a stamp refuses and names the file; only docs, README and CHANGELOG changes pass', () => {
    for (const code of [true, false]) {
      const { work } = makeRepo();
      git(work, ['tag', 'v0.1.0']);
      stampRepo(work);
      if (code) commitFile(work, 'src/feature.mjs', 'export const feature = true;\n');
      else {
        commitFile(work, 'docs/reference/guide.md', '# New guide\n');
        commitFile(work, 'README.md', README + '\nNew usage.\n');
        commitFile(work, 'CHANGELOG.md', CHANGELOG.replace('- A new verb.', '- A documented new verb.'));
      }
      const st = state(work);
      const files = snapshot(work);
      for (const args of [['patch', '--dry-run'], ['patch']]) {
        const r = release(work, args);
        assert.equal(r.code, code ? 1 : 0, r.stdout + r.stderr);
        if (code) {
          assert.match(r.stderr, /src\/feature\.mjs/);
          assert.match(r.stderr, /run the docs block again.*npm run docs-block -- brief/);
          assert.deepEqual(state(work), st, 'code refusal makes zero commits and tags');
          assert.deepEqual(snapshot(work), files);
        } else {
          assert.match(r.stdout, /^ok {4}docs block covers.*v0\.1\.0/m);
          if (args.includes('--dry-run')) assert.deepEqual(state(work), st);
          else {
            assert.equal(state(work).commits, st.commits + 1, 'one release commit after allowed docs changes');
            assert.equal(state(work).tags, 'v0.1.0\nv0.1.1', 'one new release tag');
          }
        }
      }
    }
  });

  test('code renamed into docs after a stamp refuses and names the original code path', () => {
    const { work, origin } = makeRepo();
    git(work, ['tag', 'v0.1.0']);
    commitFile(work, 'src/feature.mjs', 'export const feature = true;\n');
    stampRepo(work);
    git(work, ['config', 'diff.renames', 'true']);
    git(work, ['mv', 'src/feature.mjs', 'docs/feature.mjs']);
    git(work, ['commit', '-qm', 'move code into docs']);
    const st = state(work);
    const files = snapshot(work);
    const refs = originRefs(origin);
    for (const args of [['patch'], ['patch', '--dry-run']]) {
      const r = release(work, args);
      assert.equal(r.code, 1, r.stdout + r.stderr);
      assert.match(r.stderr, /files changed after the stamp: src\/feature\.mjs/);
      assert.match(r.stderr, /run the docs block again.*npm run docs-block -- brief/);
      assert.deepEqual(state(work), st, 'rename refusal makes zero commits, tags or worktree changes');
      assert.deepEqual(snapshot(work), files, 'zero release file edits');
      assert.equal(originRefs(origin), refs, 'zero remote mutations');
    }
  });

  test('boundary: disallowed docs-like paths fail and the refusal lists at most five changed files', () => {
    const { work } = makeRepo();
    git(work, ['tag', 'v0.1.0']);
    stampRepo(work);
    const files = ['README.md.bak', 'docs-other.mjs', 'src/a.mjs', 'src/b.mjs', 'src/c.mjs', 'src/d.mjs'];
    for (const file of files) {
      mkdirSync(path.dirname(path.join(work, file)), { recursive: true });
      writeFileSync(path.join(work, file), 'changed\n');
    }
    git(work, ['add', '-A']);
    git(work, ['commit', '-qm', 'six changes']);
    const st = state(work);
    const r = release(work, ['patch']);
    assert.equal(r.code, 1, r.stdout + r.stderr);
    assert.match(r.stderr, /npm run docs-block -- brief/);
    assert.deepEqual(files.filter((file) => r.stderr.includes(file)), files.slice(0, 5), 'exactly the first five disallowed paths, no sixth');
    assert.deepEqual(state(work), st, 'zero commits and tags');
  });

  test('no earlier v* tag warns and skips; --no-checks skips the docs block with a loud named warning', () => {
    const first = makeRepo();
    const dry = release(first.work, ['patch', '--dry-run']);
    assert.equal(dry.code, 0, dry.stderr);
    assert.equal(dry.stdout.split('\n').filter((line) => /^WARN .*docs block.*no.*v\* tag/i.test(line)).length, 1, 'exactly one no-tag docs warning');
    const real = release(first.work, ['patch']);
    assert.equal(real.code, 0, real.stderr);
    assert.match(real.stdout, /^WARN .*docs block.*no.*v\* tag/im);
    assert.equal(state(first.work).commits, 2, 'one first-release commit');
    assert.equal(state(first.work).tags, 'v0.1.1', 'one first-release tag');
    for (const dryRun of [true, false]) {
      const { work } = makeRepo();
      git(work, ['tag', 'v0.1.0']);
      const st = state(work);
      const r = release(work, ['patch', '--no-checks', ...(dryRun ? ['--dry-run'] : [])]);
      assert.equal(r.code, 0, r.stderr);
      assert.equal(r.stdout.split('\n').filter((line) => /^WARN .*!!!.*--no-checks.*docs block.*!!!/.test(line)).length, 1, 'exactly one loud warning naming the skipped docs block');
      if (dryRun) assert.deepEqual(state(work), st);
      else {
        assert.equal(state(work).commits, st.commits + 1);
        assert.equal(state(work).tags, 'v0.1.0\nv0.1.1');
      }
    }
  });

  test('boundary: ignores v* tags outside HEAD ancestry and non-v tags on HEAD', () => {
    const { work } = makeRepo();
    git(work, ['checkout', '-qb', 'other']);
    git(work, ['commit', '--allow-empty', '-qm', 'unmerged release']);
    git(work, ['tag', 'v9.0.0']);
    git(work, ['checkout', '-q', 'main']);
    git(work, ['tag', 'preview']);
    const st = state(work);
    const noRelease = release(work, ['patch', '--dry-run']);
    assert.equal(noRelease.code, 0, noRelease.stderr);
    assert.match(noRelease.stdout, /^WARN .*docs block.*no.*v\* tag/im);
    git(work, ['tag', '-a', 'v0.1.0', '-m', 'reachable release']);
    stampRepo(work);
    const covered = state(work);
    const r = release(work, ['patch', '--dry-run']);
    assert.equal(r.code, 0, r.stdout + r.stderr);
    assert.match(r.stdout, /^ok {4}docs block covers.*v0\.1\.0/m);
    assert.equal(r.stdout.split('\n').filter((line) => /^WARN .*docs block/.test(line)).length, 0, 'zero docs skip warnings once a release is reachable');
    assert.deepEqual(state(work), covered);
    assert.equal(covered.commits, st.commits + 1, 'only the fixture stamp commit was made');
  });
});

test('setJsonFields refuses a non-canonical JSON layout instead of reformatting unrelated lines', async () => {
  const { setJsonFields } = await import('../scripts/release.mjs');
  const inline = '{\n  "version": "0.1.0",\n  "files": ["bin", "src"]\n}\n';
  assert.throws(() => setJsonFields(inline, [{ keys: ['version'], value: '0.2.0' }]), /not in canonical JSON\.stringify layout/);
  const canonical = '{\n  "version": "0.1.0",\n  "files": [\n    "bin",\n    "src"\n  ]\n}\n';
  assert.equal(setJsonFields(canonical, [{ keys: ['version'], value: '0.2.0' }]), canonical.replace('"0.1.0"', '"0.2.0"'));
});
