/**
 * B102: the `xcodebuild` runner adapter — its tokens, its precheck (full Xcode, an available
 * simulator) and its fail-closed result-bundle reader, read from real captured output.
 *
 * Fake `xcode-select`, `xcrun` and `xcodebuild` scripts (Node, outside the repo) sit first on PATH
 * for this file, so no test here needs Xcode. A mode file picks how they behave:
 *   - `xcode-select -p` prints `select.txt`;
 *   - `xcrun simctl list devices available -j` prints `simctl.json`;
 *   - `xcrun xcresulttool get test-results tests --path <bundle>` prints the captured fixture the
 *     bundle's `fixture.txt` names;
 *   - `xcodebuild` makes the `-resultBundlePath` dir (a fake bundle) naming the RED fixture when
 *     `Sources/Kit/Clamp.swift` is BROKEN and the passing one when it is FIXED, and exits 65 / 0.
 * Each fake logs its argv and the `GIT_*` names it inherited.
 *
 * Fixtures (`test/fixtures/xcresult/`) are real captured output, byte for byte: Xcode 26.6
 * (17F113), the "iPhone 17" simulator (iOS 26.5, 23F77), xcresulttool schema 0.1.0, on a
 * `swift package init` package `Kit` (Package.swift `platforms: [.iOS(.v17), .macOS(.v14)]`) with
 * the test target `KitTests`. Every `<name>.json` and every console except one came from one
 * script, run from `<scratch>/Kit` as `run.sh <name> <id>...`, which ran exactly
 *
 *   xcodebuild test -scheme Kit -destination 'platform=iOS Simulator,name=iPhone 17' \
 *     -only-testing:<id> [-only-testing:<id>] -derivedDataPath <scratch>/dd \
 *     -resultBundlePath <scratch>/out/<name>.xcresult > <scratch>/out/<name>.console 2>&1
 *   xcrun xcresulttool get test-results tests --path <scratch>/out/<name>.xcresult > <scratch>/out/<name>.json
 *
 * with these `-only-testing` ids (only the consoles listed were kept):
 *   xctest.json / xctest.console               KitTests/ClampXCTests (clamp BROKEN, exit 65)
 *   swift-testing.json / swift-testing.console KitTests/ClampSwiftTests (clamp BROKEN, exit 65)
 *   thrown.json / thrown.console               KitTests/ThrowXCTests KitTests/ThrowSwiftTests (thrown errors)
 *   near-names.json                            KitTests/ClampXCTestsExtra KitTests/MoreClampSwiftTests
 *   xctest-case.json                           KitTests/ClampXCTests/testCapsHigh
 *   swift-testing-case.json                    KitTests/ClampSwiftTests/capsHigh()
 *   swift-testing-noparens.json                KitTests/ClampSwiftTests/capsHigh (selects nothing, exit 0)
 *   xctest-pass.json, swift-testing-pass.json  KitTests/ClampXCTests, KitTests/ClampSwiftTests (clamp FIXED, exit 0)
 *   bare-fail.json                             KitTests/BareFailXCTests KitTests/BareIssueTests (XCTFail(), Issue.record)
 *   build-failed.json / build-failed.console   KitTests/WrapTests (a test of a function that does not exist):
 *                                              so build-failed.console IS the console of a failing
 *                                              `xcodebuild test ... -resultBundlePath` build
 * The one other console, build-for-testing-failed.console, came from `build-for-testing`, not
 * `test`, with no result bundle (same missing function), run from `<scratch>/Kit` as
 *
 *   xcodebuild build-for-testing -scheme Kit -destination 'platform=iOS Simulator,name=iPhone 17' \
 *     -derivedDataPath <scratch>/dd > <scratch>/out/build-for-testing-failed.console 2>&1
 *
 * The `simctl` reply below is NOT a capture: it copies the shape of `xcrun simctl list devices
 * available -j` on that machine (runtime keys `com.apple.CoreSimulator.SimRuntime.iOS-26-5`, empty
 * lists for old runtimes, devices with `udid`, `isAvailable`, `deviceTypeIdentifier`, `state`,
 * `name`) without its `dataPath`/`logPath` keys, which hold local paths.
 *
 * Every temp dir lives under ONE `mkdtemp` parent (helpers.mjs, removed in `after()`).
 */

import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { loadConfigFile } from '../../src/config/load.mjs';
import { validateConfig } from '../../src/config/validate.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { NO_REPORT, classifyReport } from '../../src/proof/junit.mjs';
import { journalPath } from '../../src/proof/red-green.mjs';
import { adapterPrecheck, expandRunnerArgv, xcodeScope } from '../../src/proof/runners.mjs';
import { classifyXcodeGreen, classifyXcodeRed, failureMessageKind, loadXcresult, parseXcresult, readXcresult, xcodeBuildFailed } from '../../src/proof/xcresult.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { TEMP_PARENT, git } from './helpers.mjs';

const SLUG = 'xcodebuild-proof';
const RUN = 'r-b102-xcode';
const NODE = process.execPath;
const FIXTURES = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'xcresult');
const fixture = (/** @type {string} */ name) => readFileSync(path.join(FIXTURES, name), 'utf8');
const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: SLUG });

const XC_TEST = 'Tests/KitTests/ClampXCTests.swift';
const SWIFT_TEST = 'Tests/KitTests/ClampSwiftTests.swift';
const MAC_TEST = 'MacTests/ClampXCTests.swift';
const GONE_TEST = 'GoneTests/ClampXCTests.swift';
const SOURCE = 'Sources/Kit/Clamp.swift';
const XCODE_DIR = '/Applications/Xcode.app/Contents/Developer';
const CLT_DIR = '/Library/Developer/CommandLineTools';
/** Every key of a signed red-green row from a runner, sorted. */
const ROW_KEYS = ['block', 'cost_source', 'covers', 'env_keys', 'event', 'failed', 'failed_tests', 'green', 'isolation', 'mac', 'mechanism', 'proven', 'red', 'red_kind', 'run', 'runner', 'step', 'test', 'tokens_source', 'ts'];

const TOOLS = path.join(TEMP_PARENT, 'tools');
const BIN = path.join(TOOLS, 'bin');
const MODE = path.join(TOOLS, 'mode.txt');
const SELECT = path.join(TOOLS, 'select.txt');
const SIMCTL = path.join(TOOLS, 'simctl.json');
const LOG = path.join(TOOLS, 'calls.log');
const REPO = path.join(TEMP_PARENT, 'ws');
const EXPORT_DIR = path.join(REPO, '.code-forge', 'export', 'A');

const SIMCTL_REPLY = {
  devices: {
    'com.apple.CoreSimulator.SimRuntime.iOS-16-1': [],
    'com.apple.CoreSimulator.SimRuntime.iOS-26-5': [
      { udid: 'FAKE-UDID-0001', isAvailable: true, deviceTypeIdentifier: 'com.apple.CoreSimulator.SimDeviceType.iPhone-17', state: 'Shutdown', name: 'iPhone 17' },
      { udid: 'FAKE-UDID-0002', isAvailable: true, deviceTypeIdentifier: 'com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro', state: 'Shutdown', name: 'iPhone 17 Pro' },
      { udid: 'FAKE-UDID-0003', isAvailable: true, deviceTypeIdentifier: 'com.apple.CoreSimulator.SimDeviceType.iPad-Air-11-inch-M3', state: 'Shutdown', name: 'iPad Air 11-inch (M3)' },
      { udid: 'FAKE-UDID-0004', isAvailable: false, deviceTypeIdentifier: 'com.apple.CoreSimulator.SimDeviceType.iPhone-15', state: 'Shutdown', name: 'iPhone 15' },
    ],
    'com.apple.CoreSimulator.SimRuntime.watchOS-26-0': [
      { udid: 'FAKE-UDID-0005', isAvailable: true, deviceTypeIdentifier: 'com.apple.CoreSimulator.SimDeviceType.Apple-Watch-Series-11-46mm', state: 'Shutdown', name: 'Apple Watch Series 11 (46mm)' },
    ],
  },
};

const FAKE_HEAD = `#!${NODE}
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const FIXTURES = ${JSON.stringify(FIXTURES)};
const mode = fs.readFileSync(${JSON.stringify(MODE)}, 'utf8').trim();
const args = process.argv.slice(2);
const log = (entry) => fs.appendFileSync(${JSON.stringify(LOG)}, JSON.stringify({ ...entry, args, git_keys: Object.keys(process.env).filter((k) => k.startsWith('GIT_')).sort() }) + '\\n');
const print = (file) => process.stdout.write(fs.readFileSync(file));
`;

const FAKE_XCODE_SELECT = `${FAKE_HEAD}
log({ tool: 'xcode-select', pid: process.pid });
if (mode === 'select-hang') setTimeout(() => {}, 30000);
else process.stdout.write(fs.readFileSync(${JSON.stringify(SELECT)}, 'utf8'));
`;

const FAKE_XCRUN = `${FAKE_HEAD}
if (args[0] === 'simctl') {
  log({ tool: 'simctl', pid: process.pid });
  if (mode === 'simctl-fail') process.exitCode = 1;
  else if (mode === 'simctl-hang') setTimeout(() => {}, 30000);
  else print(${JSON.stringify(SIMCTL)});
} else if (args[0] === 'xcresulttool') {
  log({ tool: 'xcresulttool', cwd: process.cwd() });
  const bundle = args[args.indexOf('--path') + 1];
  if (mode === 'toolfail') process.exitCode = 1;
  else if (mode === 'broken') process.stdout.write('{"testNodes" : [');
  else print(path.join(FIXTURES, fs.readFileSync(path.join(bundle, 'fixture.txt'), 'utf8')));
} else {
  log({ tool: 'xcrun' });
  process.exitCode = 2;
}
`;

const FAKE_XCODEBUILD = `${FAKE_HEAD}
const bundle = args[args.indexOf('-resultBundlePath') + 1];
const fixed = fs.readFileSync(${JSON.stringify(SOURCE)}, 'utf8').includes('FIXED');
log({ tool: 'xcodebuild', cwd: process.cwd(), fixed, bundle, bundle_existed: fs.existsSync(bundle) });
const make = (name) => { fs.mkdirSync(bundle); fs.writeFileSync(path.join(bundle, 'fixture.txt'), name); };
if (!fixed && mode === 'build-failed') {
  print(path.join(FIXTURES, 'build-failed.console'));
  make('build-failed.json');
  process.exitCode = 65;
} else if (!fixed && mode === 'nobundle') {
  process.exitCode = 65;
} else if (fixed) {
  make(mode === 'swift' ? 'swift-testing-pass.json' : 'xctest-pass.json');
  process.exitCode = 0;
} else {
  make({ swift: 'swift-testing.json', near: 'near-names.json', thrown: 'thrown.json' }[mode] || 'xctest.json');
  process.exitCode = 65;
}
`;

const XCODE = Object.freeze({ scheme: 'Kit', destination: 'platform=iOS Simulator,name=iPhone 17', test_target: 'KitTests' });
const ARGV = ['xcodebuild', 'test', '-scheme', '{scheme}', '-destination', '{destination}', '{only_testing}', '-resultBundlePath', '{result_bundle}'];
const RUNNERS = [
  { match: ['Tests/**/*.swift'], adapter: 'xcodebuild', xcode: XCODE, argv: ARGV },
  { match: ['MacTests/**/*.swift'], adapter: 'xcodebuild', xcode: { ...XCODE, destination: 'platform=macOS,arch=arm64' }, argv: ARGV },
  { match: ['GoneTests/**/*.swift'], adapter: 'xcodebuild', xcode: { ...XCODE, destination: 'platform=iOS Simulator,name=iPhone 99' }, argv: ARGV },
];

/** @param {unknown} runners @returns {string} a complete, valid `.code-forge.yml` (JSON is YAML flow) */
const configYaml = (runners) =>
  [
    'version: 1',
    'project:',
    `  slug: ${SLUG}`,
    'provider: anthropic',
    'engine: harness',
    'levels:',
    '  L0: {model: claude-haiku-4-5-20251001}',
    '  L1: {model: claude-sonnet-5}',
    '  L2: {model: claude-opus-5-5}',
    '  L3: {model: claude-fable-5-1}',
    'proof:',
    `  runners: ${JSON.stringify(runners)}`,
    '',
  ].join('\n');

const BASE_CONFIG = Object.freeze({
  version: 1,
  project: { slug: SLUG },
  provider: 'anthropic',
  engine: 'harness',
  levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } },
});

const hashOf = (/** @type {string} */ file) => createHash('sha256').update(readFileSync(file)).digest('hex');
const write = (/** @type {string} */ rel, /** @type {string} */ text) => {
  mkdirSync(path.dirname(path.join(REPO, rel)), { recursive: true });
  writeFileSync(path.join(REPO, rel), text);
};
const treeHashes = () => [SOURCE, XC_TEST, SWIFT_TEST, MAC_TEST, GONE_TEST, '.code-forge.yml'].map((rel) => hashOf(path.join(REPO, rel)));
/** @returns {Record<string, any>[]} every fake call so far */
const calls = () => (existsSync(LOG) ? readFileSync(LOG, 'utf8').trim().split('\n').filter(Boolean).map((l) => JSON.parse(l)) : []);
const setMode = (/** @type {string} */ mode) => writeFileSync(MODE, `${mode}\n`);

/** @param {string[]} args */
async function cli(args) {
  let stdout = '';
  let stderr = '';
  const code = await runProof(args, { stdout: { write: (s) => (stdout += s) }, stderr: { write: (s) => (stderr += s) } });
  return { code, stdout, stderr };
}
const proveArgs = (/** @type {string} */ testArg) => ['red-green', 'A', '--run', RUN, '--test', testArg];

const SAVED_PATH = process.env.PATH;
const SAVED_GIT = process.env.GIT_CF_FAKE;

before(async () => {
  mkdirSync(BIN, { recursive: true });
  for (const [name, source] of [['xcode-select', FAKE_XCODE_SELECT], ['xcrun', FAKE_XCRUN], ['xcodebuild', FAKE_XCODEBUILD]]) {
    writeFileSync(path.join(BIN, name), source);
    chmodSync(path.join(BIN, name), 0o755);
  }
  setMode('normal');
  writeFileSync(SELECT, `${XCODE_DIR}\n`);
  writeFileSync(SIMCTL, JSON.stringify(SIMCTL_REPLY, null, 2));
  process.env.PATH = `${BIN}${path.delimiter}${SAVED_PATH}`;

  mkdirSync(REPO);
  write('.gitignore', '.code-forge/\n');
  write('.code-forge.yml', configYaml(RUNNERS));
  write(SOURCE, '// clamp BROKEN\n');
  git(['init', '-q'], REPO);
  git(['symbolic-ref', 'HEAD', 'refs/heads/main'], REPO);
  git(['add', '-A'], REPO);
  git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], REPO);
  write(SOURCE, '// clamp FIXED\n');
  for (const rel of [XC_TEST, SWIFT_TEST, MAC_TEST, GONE_TEST]) write(rel, '// a test of clamp\n');
  const loaded = await loadConfigFile(path.join(REPO, '.code-forge.yml'));
  assert.equal(loaded.ok, true);
  assert.equal(validateConfig(loaded.config).valid, true);
  await startRun({ workspace: REPO, project: SLUG, runId: RUN, writeRow, config: /** @type {Record<string, any>} */ (loaded.config) });
  await openBlock({
    runId: RUN,
    id: 'A',
    level: 'L1',
    owned: ['Sources/**', 'Tests/**', 'MacTests/**', 'GoneTests/**'],
    acceptance: [{ clause: 'clamp caps at hi', tests: [XC_TEST, SWIFT_TEST, MAC_TEST, GONE_TEST] }],
    writeRow,
  });
});

after(() => {
  process.env.PATH = SAVED_PATH;
  if (SAVED_GIT === undefined) delete process.env.GIT_CF_FAKE;
  else process.env.GIT_CF_FAKE = SAVED_GIT;
});

// ── (3) the precheck ────────────────────────────────────────────────────────
// These run first: no proof has run yet, so "no export was built" is checkable.

test('(3) Command Line Tools only ⇒ exit 1 xcode-missing; no xcodebuild, no row, no export, no journal, tree unchanged', async () => {
  writeFileSync(SELECT, `${CLT_DIR}\n`);
  try {
    const before = calls().length;
    const rowsBefore = (await readAllRows(SLUG)).length;
    const tree = treeHashes();
    const res = await cli(proveArgs(XC_TEST));
    assert.deepEqual([res.code, res.stdout], [1, '']);
    assert.equal(
      res.stderr,
      'proof red-green: xcode-missing: xcode-select -p does not point at a full Xcode (Command Line Tools only, or no developer directory); the xcodebuild runner needs Xcode — install it and run xcode-select -s; no proof was run\n',
    );
    assert.deepEqual(calls().slice(before).map((c) => [c.tool, c.args]), [['xcode-select', ['-p']]]);
    assert.equal((await readAllRows(SLUG)).length, rowsBefore);
    assert.equal(existsSync(EXPORT_DIR), false);
    assert.equal(existsSync(journalPath(REPO)), false);
    assert.deepEqual(treeHashes(), tree);
  } finally {
    writeFileSync(SELECT, `${XCODE_DIR}\n`);
  }
});

test('(3) a simulator name simctl does not list ⇒ exit 1 destination-unavailable naming the available ones; no xcodebuild, no row, no export, tree unchanged', async () => {
  const before = calls().length;
  const rowsBefore = (await readAllRows(SLUG)).length;
  const tree = treeHashes();
  const res = await cli(proveArgs(GONE_TEST));
  assert.deepEqual([res.code, res.stdout], [1, '']);
  assert.equal(
    res.stderr,
    "proof red-green: destination-unavailable: the runner's xcode.destination names no simulator that xcrun simctl lists as available (by name or id, and OS when given); available: iPad Air 11-inch (M3), iPhone 17, iPhone 17 Pro — code-forge never boots, creates or deletes a simulator; no proof was run\n",
  );
  assert.deepEqual(calls().slice(before).map((c) => [c.tool, c.args]), [
    ['xcode-select', ['-p']],
    ['simctl', ['simctl', 'list', 'devices', 'available', '-j']],
  ]);
  assert.equal((await readAllRows(SLUG)).length, rowsBefore);
  assert.equal(existsSync(EXPORT_DIR), false);
  assert.equal(existsSync(journalPath(REPO)), false);
  assert.deepEqual(treeHashes(), tree);
});

test('(3) adapterPrecheck: name, id and OS match; platform=macOS never asks simctl; other adapters run nothing; GIT_* is stripped', async () => {
  process.env.GIT_CF_FAKE = 'FAKE';
  try {
    const entry = (/** @type {string} */ destination) => ({ ...RUNNERS[0], xcode: { ...XCODE, destination } });
    const fits = [
      'platform=iOS Simulator,name=iPhone 17',
      'platform=iOS Simulator,name=iPhone 17,OS=26.5',
      'platform=iOS Simulator,name=iPhone 17,OS=26',
      'platform=iOS Simulator,name=iPhone 17,OS=latest',
      'platform=iOS Simulator,id=FAKE-UDID-0002',
      'platform=watchOS Simulator,name=Apple Watch Series 11 (46mm)',
    ];
    for (const destination of fits) {
      const before = calls().length;
      await adapterPrecheck(/** @type {any} */ (entry(destination)));
      assert.deepEqual([destination, calls().slice(before).map((c) => [c.tool, c.git_keys])], [destination, [['xcode-select', []], ['simctl', []]]]);
    }
    const before = calls().length;
    await adapterPrecheck(/** @type {any} */ (entry('platform=macOS,arch=arm64')));
    assert.deepEqual(calls().slice(before).map((c) => c.tool), ['xcode-select']);
    const junitBefore = calls().length;
    await adapterPrecheck(/** @type {any} */ ({ match: ['x/**'], adapter: 'junit', argv: ['phpunit', '{test_file}', '{report}'] }));
    assert.equal(calls().length, junitBefore);

    const AVAILABLE = 'available: iPad Air 11-inch (M3), iPhone 17, iPhone 17 Pro';
    const refused = [
      // an unavailable device is never offered, nor one of another runtime family
      ['platform=iOS Simulator,name=iPhone 15', AVAILABLE],
      ['platform=iOS Simulator,name=Apple Watch Series 11 (46mm)', AVAILABLE],
      ['platform=iOS Simulator,name=iPhone 17,OS=25.0', AVAILABLE],
      ['platform=iOS Simulator,name=iPhone 17,OS=26.50', AVAILABLE],
      ['platform=iOS Simulator,id=FAKE-UDID-0004', AVAILABLE],
      ['platform=iOS Simulator,id=FAKE-UDID-0002,name=iPhone 17', AVAILABLE],
      ['platform=tvOS Simulator,name=iPhone 17', 'available: none'],
    ];
    for (const [destination, names] of refused) {
      await assert.rejects(adapterPrecheck(/** @type {any} */ (entry(destination))), (err) => {
        assert.deepEqual([destination, /** @type {any} */ (err).code], [destination, 'destination-unavailable']);
        assert.equal(/** @type {Error} */ (err).message.includes(` ${names} — `), true, `${destination}: ${/** @type {Error} */ (err).message}`);
        return true;
      });
    }
    // a simulator destination with neither name nor id: refused before simctl runs
    for (const destination of ['platform=iOS Simulator', 'platform=iOS Simulator,OS=26.5']) {
      const noName = calls().length;
      await assert.rejects(adapterPrecheck(/** @type {any} */ (entry(destination))), {
        code: 'destination-unavailable',
        message: "destination-unavailable: the runner's xcode.destination names no simulator: a simulator destination needs name= or id=; no proof was run",
      });
      assert.deepEqual([destination, calls().slice(noName).map((c) => c.tool)], [destination, ['xcode-select']]);
    }
    await assert.rejects(adapterPrecheck(/** @type {any} */ (entry('iPhone 17'))), {
      code: 'destination-unavailable',
      message: "destination-unavailable: the runner's xcode.destination is not a list of key=value pairs; no proof was run",
    });
    setMode('simctl-fail');
    try {
      await assert.rejects(adapterPrecheck(/** @type {any} */ (entry(XCODE.destination))), {
        code: 'destination-unavailable',
        message: 'destination-unavailable: xcrun simctl did not list the available simulators; no proof was run',
      });
    } finally {
      setMode('normal');
    }
    for (const reply of ['not json', '{"devices": []}', '{"devices": {"com.apple.CoreSimulator.SimRuntime.iOS-26-5": [{"name": "iPhone 17"}]}}']) {
      writeFileSync(SIMCTL, reply);
      try {
        await assert.rejects(adapterPrecheck(/** @type {any} */ (entry(XCODE.destination))), {
          code: 'destination-unavailable',
          message: 'destination-unavailable: xcrun simctl did not list the available simulators; no proof was run',
        });
      } finally {
        writeFileSync(SIMCTL, JSON.stringify(SIMCTL_REPLY, null, 2));
      }
    }
    for (const select of ['', `${CLT_DIR}\n`]) {
      writeFileSync(SELECT, select);
      try {
        await assert.rejects(adapterPrecheck(/** @type {any} */ (entry('platform=macOS'))), { code: 'xcode-missing' });
      } finally {
        writeFileSync(SELECT, `${XCODE_DIR}\n`);
      }
    }
  } finally {
    delete process.env.GIT_CF_FAKE;
  }
});

test('(3) a hung precheck command is stopped by its timeout and read as failed: xcode-missing, destination-unavailable', async () => {
  const entry = { ...RUNNERS[0], xcode: XCODE };
  /** @type {[string, string][]} */
  const hangs = [['select-hang', 'xcode-missing'], ['simctl-hang', 'destination-unavailable']];
  try {
    for (const [mode, code] of hangs) {
      setMode(mode);
      const before = calls().length;
      const started = Date.now();
      await assert.rejects(adapterPrecheck(/** @type {any} */ (entry), { timeoutMs: 300 }), (err) => {
        assert.deepEqual([mode, /** @type {any} */ (err).code], [mode, code]);
        return true;
      });
      // the fake sleeps 30 s: only the 300 ms timeout ends the call this soon
      const elapsed = Date.now() - started;
      assert.equal(elapsed < 10_000, true, `${mode}: ${elapsed} ms`);
      const hung = calls().slice(before).at(-1);
      assert.equal(hung.tool, mode === 'select-hang' ? 'xcode-select' : 'simctl');
      // and the hung child is gone
      assert.throws(() => process.kill(hung.pid, 0), { code: 'ESRCH' });
    }
  } finally {
    setMode('normal');
  }
});

// ── (1) the tokens ──────────────────────────────────────────────────────────

test('(1) the tokens expand from the xcode object; {only_testing} is one -only-testing item; {result_bundle} is the given path', () => {
  const entry = /** @type {any} */ ({
    match: ['Tests/**'],
    adapter: 'xcodebuild',
    xcode: { workspace: 'App.xcworkspace', scheme: 'App', destination: 'platform=iOS Simulator,name=iPhone 17', test_target: 'AppTests' },
    argv: ['xcodebuild', 'test', '-workspace', '{workspace}', '-scheme', '{scheme}', '-destination', '{destination}', '{only_testing}', '-resultBundlePath', '{result_bundle}'],
  });
  const head = ['xcodebuild', 'test', '-workspace', 'App.xcworkspace', '-scheme', 'App', '-destination', 'platform=iOS Simulator,name=iPhone 17'];
  const cases = [
    [undefined, '-only-testing:AppTests/ClampXCTests', 'AppTests/ClampXCTests/'],
    ['ClampXCTests/testCapsHigh', '-only-testing:AppTests/ClampXCTests/testCapsHigh', 'AppTests/ClampXCTests/'],
    ['ClampSwiftTests/capsHigh()', '-only-testing:AppTests/ClampSwiftTests/capsHigh()', 'AppTests/ClampSwiftTests/'],
    ['OtherSuite', '-only-testing:AppTests/OtherSuite', 'AppTests/OtherSuite/'],
  ];
  for (const [testCase, only, scope] of cases) {
    const values = { file: 'Tests/AppTests/ClampXCTests.swift', testCase, report: '/fake/step/report.xml', resultBundle: '/fake/step/r.xcresult' };
    assert.deepEqual(expandRunnerArgv(entry, values), [...head, only, '-resultBundlePath', '/fake/step/r.xcresult']);
    assert.equal(xcodeScope(entry, values), scope);
  }
  // without a given bundle path it sits next to the report, in the step dir
  assert.deepEqual(expandRunnerArgv(entry, { file: 'Tests/AppTests/ClampXCTests.swift', report: '/fake/step/report.xml' }).slice(-1), ['/fake/step/r.xcresult']);
  const project = { ...entry, xcode: { project: 'App.xcodeproj', scheme: 'App', destination: 'platform=macOS', test_target: 'AppTests' }, argv: ['xcodebuild', 'test', '-project', '{project}', '-scheme', '{scheme}', '-destination', '{destination}', '{only_testing}', '-resultBundlePath', '{result_bundle}'] };
  assert.deepEqual(expandRunnerArgv(project, { file: 'Tests/AppTests/ClampXCTests.swift', report: '/r/report.xml', resultBundle: '/r/b.xcresult' }), [
    'xcodebuild', 'test', '-project', 'App.xcodeproj', '-scheme', 'App', '-destination', 'platform=macOS', '-only-testing:AppTests/ClampXCTests', '-resultBundlePath', '/r/b.xcresult',
  ]);
  // a {workspace} token whose key is gone (a tampered record) is refused, never expanded empty
  assert.throws(() => expandRunnerArgv({ ...entry, xcode: { ...entry.xcode, workspace: undefined } }, { file: 'Tests/AppTests/ClampXCTests.swift', report: '/r/report.xml' }), {
    code: 'runner-invalid',
    message: "the xcodebuild runner's {workspace} has no value in its xcode object; no proof was run",
  });
  // no xcode object or no test_target (a tampered record): refused naming xcode.test_target, by both
  const NO_TARGET = { code: 'runner-invalid', message: "the xcodebuild runner's xcode.test_target is not set to one target name; no proof was run" };
  for (const xcode of [undefined, { ...entry.xcode, test_target: undefined }, { ...entry.xcode, test_target: '' }, { ...entry.xcode, test_target: 'App/Tests' }]) {
    const tampered = { ...entry, xcode };
    assert.throws(() => expandRunnerArgv(tampered, { file: 'Tests/AppTests/ClampXCTests.swift', report: '/r/report.xml' }), NO_TARGET);
    assert.throws(() => xcodeScope(tampered, { file: 'Tests/AppTests/ClampXCTests.swift' }), NO_TARGET);
  }
  const usage = [
    ['-only-testing:Other', 'the test case starts with "-" (it would be read as an option)'],
    ['/ClampXCTests', 'an xcodebuild test case is <Suite> or <Suite>/<test>, with no empty part'],
    ['ClampXCTests/', 'an xcodebuild test case is <Suite> or <Suite>/<test>, with no empty part'],
    ['ClampXCTests//testCapsHigh', 'an xcodebuild test case is <Suite> or <Suite>/<test>, with no empty part'],
    ['', '--test <file>::<case> needs a case name after ::'],
    ['Outer/Inner/test()', 'an xcodebuild test case is <Suite> or <Suite>/<test>; a nested Swift Testing suite (Outer/Inner/test()) is not supported — give the outer <Suite> to run all of it'],
  ];
  for (const [testCase, message] of usage) {
    assert.throws(() => expandRunnerArgv(entry, { file: 'Tests/AppTests/ClampXCTests.swift', testCase, report: '/r/report.xml' }), { code: 'usage', message });
    if (testCase !== '') assert.throws(() => xcodeScope(entry, { file: 'Tests/AppTests/ClampXCTests.swift', testCase }), { code: 'usage', message });
  }
});

// ── (2) validate ────────────────────────────────────────────────────────────

test('(2) validate accepts a full xcodebuild entry and refuses each bad one with a message that names the key path', () => {
  const at = 'proof.runners[0]';
  const ok = [
    { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: ARGV },
    { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, workspace: 'App.xcworkspace' }, argv: ['xcodebuild', 'test', '-workspace', '{workspace}', ...ARGV.slice(2)] },
    { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, project: 'App.xcodeproj' }, argv: ['xcodebuild', 'test', '-project', '{project}', ...ARGV.slice(2)], timeout_s: 1800 },
  ];
  for (const entry of ok) assert.deepEqual(validateConfig({ ...BASE_CONFIG, proof: { runners: [entry] } }).errors, []);

  const without = (/** @type {string} */ token) => ARGV.filter((item) => item !== token);
  /** @type {[string, Record<string, any>, string[]][]} */
  const cases = [
    ['no xcode object', { match: ['Tests/**'], adapter: 'xcodebuild', argv: ARGV }, [`${at}.xcode is required for the xcodebuild adapter (scheme, destination, test_target)`]],
    ['no scheme', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { destination: XCODE.destination, test_target: 'KitTests' }, argv: ARGV }, [`${at}.xcode.scheme is required`]],
    ['no destination', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { scheme: 'Kit', test_target: 'KitTests' }, argv: ARGV }, [`${at}.xcode.destination is required`]],
    ['no test_target', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { scheme: 'Kit', destination: XCODE.destination }, argv: ARGV }, [`${at}.xcode.test_target is required`]],
    ['workspace and project', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, workspace: 'A.xcworkspace', project: 'A.xcodeproj' }, argv: ['xcodebuild', '-workspace', '{workspace}', '-project', '{project}', ...ARGV.slice(1)] }, [`${at}.xcode: give workspace or project, not both`]],
    ['a workspace key with no {workspace}', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, workspace: 'A.xcworkspace' }, argv: ARGV }, [`${at}.xcode.workspace is set but ${at}.argv has no {workspace} item`]],
    ['a project key with no {project}', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, project: 'A.xcodeproj' }, argv: ARGV }, [`${at}.xcode.project is set but ${at}.argv has no {project} item`]],
    ['xcode not an object, with {workspace}', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: ['Kit'], argv: ['xcodebuild', '-workspace', '{workspace}', ...ARGV.slice(1)] }, [`${at}.xcode must be an object with scheme, destination, test_target`]],
    ['xcode null', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: null, argv: ['xcodebuild', '-project', '{project}', ...ARGV.slice(1)] }, [`${at}.xcode must be an object with scheme, destination, test_target`]],
    ['no {scheme}', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: without('{scheme}') }, [`${at}.argv needs a {scheme} item (the xcodebuild adapter fills it from xcode.scheme)`]],
    ['no {destination}', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: without('{destination}') }, [`${at}.argv needs a {destination} item (the xcodebuild adapter fills it from xcode.destination, the one the precheck checks)`]],
    ['no {only_testing}', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: without('{only_testing}') }, [`${at}.argv needs a {only_testing} item (a proof must name its test)`]],
    ['no {result_bundle}', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: without('{result_bundle}') }, [`${at}.argv needs a {result_bundle} item (the xcodebuild adapter reads the result bundle code-forge places there)`]],
    ['{workspace} with no workspace key', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: ['xcodebuild', '-workspace', '{workspace}', ...ARGV.slice(1)] }, [`${at}.argv has {workspace} but ${at}.xcode.workspace is not set`]],
    ['{project} with no project key', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: ['xcodebuild', '-project', '{project}', ...ARGV.slice(1)] }, [`${at}.argv has {project} but ${at}.xcode.project is not set`]],
    ['an xcode object on junit', { match: ['Tests/**'], adapter: 'junit', xcode: XCODE, argv: ['phpunit', '{test_file}', '{report}'] }, [`${at}.xcode is only for the xcodebuild adapter`]],
    ['an xcode object on swift', { match: ['Tests/**'], adapter: 'swift', xcode: XCODE, argv: ['swift', 'test', '--filter', '{test_filter}', '--xunit-output', '{report}'] }, [`${at}.xcode is only for the xcodebuild adapter`]],
    ['{report} on xcodebuild', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: [...ARGV, '{report}'] }, [`${at}.argv[9] is not a token of the xcodebuild adapter ({workspace}, {project}, {scheme}, {destination}, {only_testing}, {result_bundle})`]],
    ['{scheme} on junit', { match: ['Tests/**'], adapter: 'junit', argv: ['phpunit', '{scheme}', '{test_file}', '{report}'] }, [`${at}.argv[1] is not a token of the junit adapter ({test_file}, {test_id}, {test_filter}, {report})`]],
    ['xcode not an object', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: 'Kit', argv: ARGV }, [`${at}.xcode must be an object with scheme, destination, test_target`]],
    ['an unknown xcode key', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, target: 'KitTests' }, argv: ARGV }, [`${at}.xcode: "target" is not an xcode key (workspace, project, scheme, destination, test_target)`]],
    ['a value that starts with -', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, scheme: '-quiet' }, argv: ARGV }, [`${at}.xcode.scheme must be a non-empty string that does not start with "-"`]],
    ['an empty value', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, destination: '' }, argv: ARGV }, [`${at}.xcode.destination must be a non-empty string that does not start with "-"`]],
    ['a NUL in a value', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, scheme: 'Kit\0FAKE' }, argv: ARGV }, [`${at}.xcode.scheme contains a NUL character`]],
    ['a test_target path', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, test_target: 'KitTests/ClampXCTests' }, argv: ARGV }, [`${at}.xcode.test_target must be one target name, without "/"`]],
  ];
  for (const [name, entry, messages] of cases) {
    const result = validateConfig({ ...BASE_CONFIG, proof: { runners: [entry] } });
    const ours = result.errors.filter((e) => e.rule === 'proof-runners-invalid').map((e) => e.message);
    assert.deepEqual([name, result.valid, ours], [name, false, messages]);
    assert.deepEqual([name, result.errors.filter((e) => e.rule !== 'proof-runners-invalid' && e.rule !== 'schema')], [name, []]);
    // the message never repeats a config value
    for (const message of ours) assert.equal(/Kit\b|iPhone|xcworkspace|xcodeproj|-quiet/.test(message.replace(/KitTests\/ClampXCTests/, '')), false, message);
  }
});

test('(2) the schema layer says the same where JSON Schema can; a valid entry trips no schema error', () => {
  const schemaErrors = (/** @type {Record<string, any>} */ entry) => validateConfig({ ...BASE_CONFIG, proof: { runners: [entry] } }).errors.filter((e) => e.rule === 'schema');
  for (const entry of [
    { match: ['Tests/**'], adapter: 'xcodebuild', xcode: XCODE, argv: ARGV },
    { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, workspace: 'App.xcworkspace' }, argv: ['xcodebuild', 'test', '-workspace', '{workspace}', ...ARGV.slice(2)] },
    { match: ['x/**'], adapter: 'junit', argv: ['phpunit', '{test_file}', '{report}'] },
  ]) {
    assert.deepEqual(schemaErrors(entry), []);
  }
  // each refused entry trips exactly its own rule: [keyword, instance path, message] of every schema error
  const at = '/proof/runners/0';
  const pattern = (/** @type {string} */ key, /** @type {string} */ re) => [['pattern', `${at}/xcode/${key}`, `${at}/xcode/${key} must match pattern "${re}"`]];
  const NOT_ON_OTHER = [['propertyNames', at, `${at} property name "xcode" is not allowed (propertyNames)`], ['if', at, `${at} must match "else" schema`]];
  /** @type {[string, Record<string, any>, string[][]][]} */
  const refused = [
    ['a scheme that starts with -', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, scheme: '-quiet' }, argv: ARGV }, pattern('scheme', '^[^-]')],
    ['a destination that starts with -', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, destination: '-x' }, argv: ARGV }, pattern('destination', '^[^-]')],
    ['a workspace that starts with -', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, workspace: '-x' }, argv: ['xcodebuild', '-workspace', '{workspace}', ...ARGV.slice(1)] }, pattern('workspace', '^[^-]')],
    ['a project that starts with -', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, project: '-x' }, argv: ['xcodebuild', '-project', '{project}', ...ARGV.slice(1)] }, pattern('project', '^[^-]')],
    ['a test_target that starts with -', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, test_target: '-x' }, argv: ARGV }, pattern('test_target', '^[^-/][^/]*$')],
    ['a test_target with a /', { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, test_target: 'KitTests/ClampXCTests' }, argv: ARGV }, pattern('test_target', '^[^-/][^/]*$')],
    [
      'workspace and project',
      { match: ['Tests/**'], adapter: 'xcodebuild', xcode: { ...XCODE, workspace: 'A.xcworkspace', project: 'A.xcodeproj' }, argv: ['xcodebuild', '-workspace', '{workspace}', '-project', '{project}', ...ARGV.slice(1)] },
      [['propertyNames', `${at}/xcode`, `${at}/xcode property name "project" is not allowed (propertyNames)`]],
    ],
    ['xcodebuild without xcode', { match: ['Tests/**'], adapter: 'xcodebuild', argv: ARGV }, [['not', at, `${at} must NOT be valid`], ['if', at, `${at} must match "then" schema`]]],
    ['junit with xcode', { match: ['Tests/**'], adapter: 'junit', xcode: XCODE, argv: ['phpunit', '{test_file}', '{report}'] }, NOT_ON_OTHER],
    ['swift with xcode', { match: ['Tests/**'], adapter: 'swift', xcode: XCODE, argv: ['swift', 'test', '--filter', '{test_filter}', '--xunit-output', '{report}'] }, NOT_ON_OTHER],
  ];
  for (const [name, entry, expected] of refused) {
    assert.deepEqual([name, schemaErrors(entry).map((e) => [e.keyword, e.path, e.message])], [name, expected]);
  }
});

// ── (4)(5) the reader ───────────────────────────────────────────────────────

const RUN_65 = Object.freeze({ code: 65, signal: null, timedOut: false, stdout: '', stderr: '' });
const RUN_0 = Object.freeze({ code: 0, signal: null, timedOut: false, stdout: '', stderr: '' });
const XC_SCOPE = 'KitTests/ClampXCTests/';
const SWIFT_SCOPE = 'KitTests/ClampSwiftTests/';
/** @param {Record<string, any>} r */
const verdict = (r) => [r.verdict, r.red_kind, r.failed, r.failed_tests, r.failed_ids, r.reason];

test('(4) readXcresult maps the captured JSON: ids are <bundle>/<nodeIdentifier>, results and kinds as captured', () => {
  assert.deepEqual(readXcresult(fixture('xctest.json')).tests, [
    { id: 'KitTests/ClampXCTests/testCapsHigh()', status: 'failed', kind: 'assertion' },
    { id: 'KitTests/ClampXCTests/testKeepsInside()', status: 'passed' },
  ]);
  assert.deepEqual(readXcresult(fixture('swift-testing.json')).tests, [
    { id: 'KitTests/ClampSwiftTests/capsHigh()', status: 'failed', kind: 'assertion' },
    { id: 'KitTests/ClampSwiftTests/keepsInside()', status: 'passed' },
  ]);
  assert.deepEqual(readXcresult(fixture('xctest-case.json')).tests, [{ id: 'KitTests/ClampXCTests/testCapsHigh()', status: 'failed', kind: 'assertion' }]);
  assert.deepEqual(readXcresult(fixture('swift-testing-case.json')).tests, [{ id: 'KitTests/ClampSwiftTests/capsHigh()', status: 'failed', kind: 'assertion' }]);
  assert.deepEqual(readXcresult(fixture('xctest-pass.json')).tests.map((t) => [t.id, t.status]), [['KitTests/ClampXCTests/testCapsHigh()', 'passed'], ['KitTests/ClampXCTests/testKeepsInside()', 'passed']]);
  // Swift Testing without `()` in -only-testing selects nothing: a Test Plan with no children
  assert.deepEqual(readXcresult(fixture('swift-testing-noparens.json')).tests, []);
  assert.deepEqual(readXcresult(fixture('build-failed.json')).tests, []);
  assert.deepEqual(readXcresult(fixture('bare-fail.json')).tests, [
    { id: 'KitTests/BareFailXCTests/testBareFail()', status: 'failed', kind: 'assertion' },
    { id: 'KitTests/BareIssueTests/recorded()', status: 'failed', kind: 'error' },
  ]);
  assert.deepEqual(readXcresult(fixture('thrown.json')).tests.map((t) => [t.status, t.kind]), [['failed', 'error'], ['failed', 'error'], ['failed', 'error']]);
});

test('(4) failureMessageKind: XCTAssert, XCTFail and Expectation failed are assertions; a thrown error, Issue.record and a crash are errors', () => {
  const kinds = [
    ['ClampXCTests.swift:7: XCTAssertEqual failed: ("12") is not equal to ("10")', 'assertion'],
    ['XCTAssertTrue failed', 'assertion'],
    ['ClampXCTests.swift:16: failed - near name', 'assertion'],
    ['BareFailTests.swift:7: failed', 'assertion'],
    ['ClampSwiftTests.swift:6: Expectation failed: (clamp(12, 0, 10) → 12) == 10', 'assertion'],
    ['ThrowXCTests.swift:7: XCTAssertEqual failed: threw error "KitError()"', 'error'],
    ['ThrowXCTests.swift:11: failed: caught error: "KitError()"', 'error'],
    ['ThrowSwiftTests.swift:6: Caught error: KitError()', 'error'],
    ['BareFailTests.swift:13: Issue recorded: FAKE issue', 'error'],
    ['Crash: Kit at clamp(_:_:_:)', 'error'],
    ['failed -', 'error'],
    ['failed!', 'error'],
    ['', 'error'],
  ];
  assert.deepEqual(kinds.map(([text]) => [text, failureMessageKind(text)]), kinds);
});

test('(4) RED from the reader: one assertion failure ⇒ RED assertion; a non-assertion failure, a test outside <target>/<Suite> ⇒ RED_INVALID error', () => {
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('xctest.json')), scope: XC_SCOPE })), [
    'RED', 'assertion', 1, ['KitTests/ClampXCTests/testCapsHigh()'], ['KitTests/ClampXCTests/testCapsHigh()'], 'an assertion failed',
  ]);
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('swift-testing.json')), scope: SWIFT_SCOPE })), [
    'RED', 'assertion', 1, ['KitTests/ClampSwiftTests/capsHigh()'], ['KitTests/ClampSwiftTests/capsHigh()'], 'an assertion failed',
  ]);
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('swift-testing-case.json')), scope: SWIFT_SCOPE })).slice(0, 2), ['RED', 'assertion']);
  // the bare XCTFail() suite alone is an assertion
  const bareXc = { tests: readXcresult(fixture('bare-fail.json')).tests.filter((t) => t.id.startsWith('KitTests/BareFailXCTests/')) };
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: bareXc, scope: 'KitTests/BareFailXCTests/' })).slice(0, 2), ['RED', 'assertion']);
  // a thrown error, Issue.record: not an assertion
  const thrownXc = { tests: readXcresult(fixture('thrown.json')).tests.filter((t) => t.id.startsWith('KitTests/ThrowXCTests/')) };
  const thrownIds = ['KitTests/ThrowXCTests/testLoads()', 'KitTests/ThrowXCTests/testLoadsDirect()'];
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: thrownXc, scope: 'KitTests/ThrowXCTests/' })), [
    'RED_INVALID', 'error', 2, thrownIds, thrownIds, 'a test errored or failed without an assertion',
  ]);
  const thrownSwift = { tests: readXcresult(fixture('thrown.json')).tests.filter((t) => t.id.startsWith('KitTests/ThrowSwiftTests/')) };
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: thrownSwift, scope: 'KitTests/ThrowSwiftTests/' })).slice(0, 2), ['RED_INVALID', 'error']);
  const issue = { tests: readXcresult(fixture('bare-fail.json')).tests.filter((t) => t.id.startsWith('KitTests/BareIssueTests/')) };
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: issue, scope: 'KitTests/BareIssueTests/' })).slice(0, 2), ['RED_INVALID', 'error']);
  // the whole bare-fail result: one assertion and one Issue.record ⇒ not RED
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('bare-fail.json')), scope: 'KitTests/' })).slice(0, 2), ['RED_INVALID', 'error']);
  // near names: an assertion failure, but in suites outside the one named
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('near-names.json')), scope: XC_SCOPE })), [
    'RED_INVALID', 'error', null, [], [], 'the filter ran other tests',
  ]);
  // the xctest result read for the Swift suite: every test is outside it
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('xctest.json')), scope: SWIFT_SCOPE })).slice(0, 2), ['RED_INVALID', 'error']);
  // one test outside the scope among assertion failures inside it
  const mixed = { tests: [...readXcresult(fixture('xctest.json')).tests, { id: 'KitTests/ClampXCTestsExtra/testAlwaysFails()', status: /** @type {const} */ ('failed'), kind: /** @type {const} */ ('assertion') }] };
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: mixed, scope: XC_SCOPE })).slice(0, 2), ['RED_INVALID', 'error']);
});

test('(4) a failed xcresulttool, broken JSON, an unknown node shape, no bundle ⇒ RED_INVALID error', async () => {
  const red = (/** @type {any} */ report) => verdict(classifyXcodeRed({ run: RUN_65, report, scope: XC_SCOPE })).slice(0, 5);
  const INVALID = ['RED_INVALID', 'error', null, [], []];
  const doc = JSON.parse(fixture('xctest.json'));
  /** @param {(d: any) => void} edit */
  const mutated = (edit) => {
    const d = structuredClone(doc);
    edit(d);
    return JSON.stringify(d);
  };
  const testCase = (/** @type {any} */ d) => d.testNodes[0].children[0].children[0].children[0];
  const unreadable = [
    ['broken JSON', '{"testNodes" : [', 'the xcresulttool output is not JSON'],
    ['not JSON at all', 'xcresulttool: error', 'the xcresulttool output is not JSON'],
    ['no testNodes', JSON.stringify({ devices: doc.devices }), 'the xcresulttool output has no testNodes list'],
    ['testNodes not a list', JSON.stringify({ testNodes: {} }), 'the xcresulttool output has no testNodes list'],
    ['an unknown node type', mutated((d) => (d.testNodes[0].children[0].nodeType = 'Test Bundle')), 'a node type this reader does not know'],
    ['a node in the wrong place', mutated((d) => (d.testNodes = d.testNodes[0].children)), 'a test node in the wrong place'],
    ['a test case without a nodeIdentifier', mutated((d) => delete testCase(d).nodeIdentifier), 'a test case without a nodeIdentifier'],
    ['an unknown result', mutated((d) => (testCase(d).result = 'Unknown')), 'a test result this reader does not know'],
    ['a failure message on a passed test', mutated((d) => (testCase(d).result = 'Passed')), 'a failure message on a test that did not fail'],
    ['children that are not a list', mutated((d) => (testCase(d).children = {})), 'test node children that are not a list'],
    ['a bundle name with a /', mutated((d) => (d.testNodes[0].children[0].name = 'Kit/Tests')), 'a test bundle without a name'],
    ['over 8 MiB', `${fixture('xctest.json')}${' '.repeat(8 * 1024 * 1024)}`, 'the xcresulttool output is over 8 MiB'],
    ['one test case twice', mutated((d) => d.testNodes[0].children[0].children[0].children.push(structuredClone(testCase(d)))), 'two test cases with the same id'],
    ['one suite twice in one bundle', mutated((d) => d.testNodes[0].children[0].children.push(structuredClone(d.testNodes[0].children[0].children[0]))), 'two test cases with the same id'],
    ['the result Expected Failure', mutated((d) => (testCase(d).result = 'Expected Failure')), 'a test result this reader does not know'],
    ...['Arguments', 'Repetition', 'Test Case Run', 'Device', 'Test Plan Configuration', 'Runtime Warning'].map((type) => [
      `a ${type} node`,
      mutated((d) => (testCase(d).children[0].nodeType = type)),
      'a node type this reader does not know',
    ]),
  ];
  for (const [name, text, reason] of unreadable) {
    assert.deepEqual([name, parseXcresult(text)], [name, { unreadable: reason }]);
    assert.deepEqual([name, red(parseXcresult(text))], [name, INVALID]);
  }
  // the reference: the same file, unmutated, is RED (so each edit above is what made it invalid)
  assert.deepEqual(red(parseXcresult(mutated(() => {}))).slice(0, 2), ['RED', 'assertion']);
  // no bundle at all
  assert.deepEqual(red(NO_REPORT), INVALID);

  // through loadXcresult and the fake xcresulttool
  const dir = path.join(TOOLS, 'bundles');
  mkdirSync(dir);
  const bundle = path.join(dir, 'r.xcresult');
  mkdirSync(bundle);
  writeFileSync(path.join(bundle, 'fixture.txt'), 'xctest.json');
  const opts = { cwd: dir, env: process.env };
  assert.deepEqual(red(await loadXcresult(bundle, opts)).slice(0, 2), ['RED', 'assertion']);
  const before = calls().length;
  assert.equal(await loadXcresult(path.join(dir, 'missing.xcresult'), opts), NO_REPORT);
  assert.equal(calls().length, before); // no bundle: xcresulttool never runs
  writeFileSync(path.join(dir, 'file.xcresult'), 'FAKE');
  assert.deepEqual(await loadXcresult(path.join(dir, 'file.xcresult'), opts), { unreadable: 'the result bundle is not a directory' });
  setMode('toolfail');
  try {
    assert.deepEqual(await loadXcresult(bundle, opts), { unreadable: 'xcresulttool failed' });
    assert.deepEqual(red(await loadXcresult(bundle, opts)), INVALID);
  } finally {
    setMode('normal');
  }
  setMode('broken');
  try {
    assert.deepEqual(red(await loadXcresult(bundle, opts)), INVALID);
  } finally {
    setMode('normal');
  }
  assert.deepEqual(calls().slice(before).filter((c) => c.tool === 'xcresulttool').map((c) => c.args), Array(3).fill(['xcresulttool', 'get', 'test-results', 'tests', '--path', bundle]));

  // a relative bundle path is resolved once, against opts.cwd: the same absolute path is checked and read
  const relBefore = calls().length;
  assert.deepEqual(red(await loadXcresult('r.xcresult', opts)).slice(0, 2), ['RED', 'assertion']);
  assert.equal(await loadXcresult('missing.xcresult', opts), NO_REPORT);
  assert.deepEqual(await loadXcresult('file.xcresult', opts), { unreadable: 'the result bundle is not a directory' });
  assert.deepEqual(calls().slice(relBefore).filter((c) => c.tool === 'xcresulttool').map((c) => c.args.slice(-2)), [['--path', bundle]]);
});

test('(4) a scope that is not <test_target>/<Suite>/ is refused: RED_INVALID error at RED, NOT_GREEN at GREEN', () => {
  for (const scope of ['KitTests/ClampXCTests', 'KitTests/', 'KitTests/ClampXCTests/testCapsHigh()/', '', '/ClampXCTests/']) {
    assert.deepEqual([scope, verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('xctest.json')), scope }))], [scope, ['RED_INVALID', 'error', null, [], [], 'no readable test result']]);
    assert.deepEqual([scope, classifyXcodeGreen({ run: RUN_0, report: parseXcresult(fixture('xctest-pass.json')), redFailed: ['KitTests/ClampXCTests/testCapsHigh()'], scope }).verdict], [scope, 'NOT_GREEN']);
  }
  // the reference: the same inputs with the right scope are RED and GREEN
  assert.equal(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('xctest.json')), scope: XC_SCOPE }).verdict, 'RED');
  assert.equal(classifyXcodeGreen({ run: RUN_0, report: parseXcresult(fixture('xctest-pass.json')), redFailed: ['KitTests/ClampXCTests/testCapsHigh()'], scope: XC_SCOPE }).verdict, 'GREEN');
});

test('(4) a case is given and another test of the same suite ran: in scope; a pass is ignored, an assertion failure is one more RED failure GREEN must see pass', () => {
  // the case's scope is the whole suite
  assert.equal(xcodeScope(/** @type {any} */ (RUNNERS[0]), { file: XC_TEST, testCase: 'ClampXCTests/testCapsHigh' }), XC_SCOPE);
  // xctest.json: testCapsHigh failed, testKeepsInside (same suite, not the case) passed
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('xctest.json')), scope: XC_SCOPE })), [
    'RED', 'assertion', 1, ['KitTests/ClampXCTests/testCapsHigh()'], ['KitTests/ClampXCTests/testCapsHigh()'], 'an assertion failed',
  ]);
  const both = ['KitTests/ClampXCTests/testCapsHigh()', 'KitTests/ClampXCTests/testKeepsInside()'];
  const bothFailed = { tests: both.map((id) => ({ id, status: /** @type {const} */ ('failed'), kind: /** @type {const} */ ('assertion') })) };
  const red = classifyXcodeRed({ run: RUN_65, report: bothFailed, scope: XC_SCOPE });
  assert.deepEqual(verdict(red), ['RED', 'assertion', 2, both, both, 'an assertion failed']);
  const pass = parseXcresult(fixture('xctest-pass.json'));
  assert.equal(classifyXcodeGreen({ run: RUN_0, report: pass, redFailed: red.failed_ids, scope: XC_SCOPE }).verdict, 'GREEN');
  const onlyCase = { tests: [{ id: both[0], status: /** @type {const} */ ('passed') }] };
  assert.equal(classifyXcodeGreen({ run: RUN_0, report: onlyCase, redFailed: red.failed_ids, scope: XC_SCOPE }).verdict, 'NOT_GREEN');
});

test('(4) a run that exits 0: no test ran ⇒ RED_INVALID error (junit and swift read it NOT_RED); tests passed ⇒ NOT_RED', () => {
  const empty = parseXcresult(fixture('swift-testing-noparens.json'));
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_0, report: empty, scope: SWIFT_SCOPE })), [
    'RED_INVALID', 'error', null, [], [], 'the filter ran no test',
  ]);
  // the same run through the junit/swift classifier: any exit 0 is NOT_RED
  const junitRead = classifyReport({ run: RUN_0, reports: [empty] });
  assert.deepEqual([junitRead.verdict, junitRead.red_kind], ['NOT_RED', 'none']);
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_0, report: parseXcresult(fixture('xctest-pass.json')), scope: XC_SCOPE })).slice(0, 2), ['NOT_RED', 'none']);
  // exit 0 with no bundle or an unreadable one is never NOT_RED
  for (const report of [NO_REPORT, { unreadable: 'two test cases with the same id' }]) {
    assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_0, report, scope: XC_SCOPE })), ['RED_INVALID', 'error', null, [], [], 'no readable test result']);
  }
  // exit 0 with a test outside the suite is never NOT_RED: the test under proof did not run cleanly in scope
  const OTHER = ['RED_INVALID', 'error', null, [], [], 'the filter ran other tests'];
  const outside = { id: 'KitTests/ClampXCTestsExtra/testKeepsInside()', status: /** @type {const} */ ('passed') };
  // only out-of-scope tests ran (and passed)
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_0, report: { tests: [outside] }, scope: XC_SCOPE })), OTHER);
  // the in-scope tests passed, plus one out-of-scope test
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_0, report: { tests: [...readXcresult(fixture('xctest-pass.json')).tests, outside] }, scope: XC_SCOPE })), OTHER);
  // the captured xctest-pass result read for the Swift suite: every test is outside it
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_0, report: parseXcresult(fixture('xctest-pass.json')), scope: SWIFT_SCOPE })), OTHER);
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, timedOut: true }, report: NO_REPORT, scope: XC_SCOPE })).slice(0, 2), ['RED_INVALID', 'fatal']);
});

test('(5) a build failure ⇒ RED_INVALID compile: ** TEST BUILD FAILED **, xcodebuild test\'s "Testing cancelled because the build failed."', () => {
  const bft = fixture('build-for-testing-failed.console');
  const testFailed = fixture('build-failed.console');
  assert.deepEqual([xcodeBuildFailed(bft), xcodeBuildFailed(testFailed)], [true, true]);
  // the consoles of runs that built never read as a build failure
  assert.deepEqual(['xctest.console', 'swift-testing.console', 'thrown.console'].map((name) => xcodeBuildFailed(fixture(name))), [false, false, false]);
  assert.deepEqual([xcodeBuildFailed('** BUILD FAILED **\n'), xcodeBuildFailed('\x1b[1m** TEST BUILD FAILED **\x1b[0m\n'), xcodeBuildFailed('note: ** TEST BUILD FAILED ** later\n')], [true, true, false]);
  const compile = ['RED_INVALID', 'compile', null, [], [], 'the test did not build'];
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, stdout: bft }, report: NO_REPORT, scope: XC_SCOPE })), compile);
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, stdout: testFailed }, report: parseXcresult(fixture('build-failed.json')), scope: XC_SCOPE })), compile);
  // even with a result that holds a failed assertion, a failed build never proves RED
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, stderr: bft }, report: parseXcresult(fixture('xctest.json')), scope: XC_SCOPE })), compile);
  // a run that built and failed its tests is not a compile error
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, stdout: fixture('xctest.console') }, report: parseXcresult(fixture('xctest.json')), scope: XC_SCOPE })).slice(0, 2), ['RED', 'assertion']);
  // no result and a swiftc error line ⇒ compile; the build-failed result without the console ⇒ error
  // a console that holds only xcodebuild test's cancelled line (as captured: tab-indented) ⇒ compile
  const cancelled = '\tTesting cancelled because the build failed.\n';
  assert.equal(testFailed.includes(cancelled), true);
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, stdout: cancelled }, report: NO_REPORT, scope: XC_SCOPE })), compile);
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, stdout: cancelled }, report: parseXcresult(fixture('xctest.json')), scope: XC_SCOPE })), compile);
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, stderr: cancelled }, report: parseXcresult(fixture('build-failed.json')), scope: XC_SCOPE })), compile);
  const swiftcLine = 'Tests/KitTests/WrapTests.swift:6:24: error: cannot find \'wrap\' in scope\n';
  assert.deepEqual(verdict(classifyXcodeRed({ run: { ...RUN_65, stderr: swiftcLine }, report: NO_REPORT, scope: XC_SCOPE })).slice(0, 2), ['RED_INVALID', 'compile']);
  assert.deepEqual(verdict(classifyXcodeRed({ run: RUN_65, report: parseXcresult(fixture('build-failed.json')), scope: XC_SCOPE })).slice(0, 2), ['RED_INVALID', 'error']);
});

test('(4) GREEN: every RED failure passed ⇒ GREEN; a test outside the suite, a failure, a non-zero exit ⇒ NOT_GREEN', () => {
  const redFailed = ['KitTests/ClampXCTests/testCapsHigh()'];
  const green = (/** @type {any} */ run, /** @type {any} */ report, scope = XC_SCOPE) => classifyXcodeGreen({ run, report, redFailed, scope }).verdict;
  assert.equal(green(RUN_0, parseXcresult(fixture('xctest-pass.json'))), 'GREEN');
  const extra = { tests: [...readXcresult(fixture('xctest-pass.json')).tests, { id: 'KitTests/ClampXCTestsExtra/testKeepsInside()', status: /** @type {const} */ ('passed') }] };
  assert.equal(green(RUN_0, extra), 'NOT_GREEN');
  assert.equal(classifyXcodeGreen({ run: RUN_0, report: extra, redFailed, scope: XC_SCOPE }).reason, 'the filter ran other tests');
  assert.equal(green(RUN_65, parseXcresult(fixture('xctest-pass.json'))), 'NOT_GREEN');
  assert.equal(green(RUN_0, parseXcresult(fixture('xctest.json'))), 'NOT_GREEN');
  assert.equal(green(RUN_0, parseXcresult(fixture('swift-testing-noparens.json'))), 'NOT_GREEN');
  assert.equal(green(RUN_0, NO_REPORT), 'NOT_GREEN');
  assert.equal(green(RUN_0, parseXcresult('{')), 'NOT_GREEN');
});

// ── (6) a full proof red-green ──────────────────────────────────────────────

test('(6)(1) a full proof red-green through fake xcodebuild and xcresulttool: RED assertion, GREEN, exit 0, one signed row with runner xcodebuild', async () => {
  setMode('normal');
  process.env.GIT_CF_FAKE = 'FAKE';
  try {
    const before = calls().length;
    const rowsBefore = (await readAllRows(SLUG)).length;
    const tree = treeHashes();
    const res = await cli(proveArgs(XC_TEST));
    assert.equal(res.code, 0, res.stderr);
    assert.equal(res.stderr, `RED ${XC_TEST}\nGREEN ${XC_TEST}\n`);
    const printed = JSON.parse(res.stdout);
    assert.deepEqual(
      [printed.test, printed.mechanism, printed.isolation, printed.red, printed.red_kind, printed.green, printed.proven, printed.runner, printed.failed_tests, printed.env_keys],
      [XC_TEST, 'revert', 'export', 'RED', 'assertion', 'GREEN', true, 'xcodebuild', ['KitTests/ClampXCTests/testCapsHigh()'], []],
    );
    const added = (await readAllRows(SLUG)).slice(rowsBefore);
    assert.deepEqual(added.map((r) => [r.event, r.block, r.step]), [['proof', 'A', 'export'], ['proof', 'A', 'red-green']]);
    const row = added[1];
    assert.deepEqual(
      [row.test, row.red, row.red_kind, row.green, row.proven, row.failed, row.runner, row.failed_tests, row.covers, row.env_keys],
      [XC_TEST, 'RED', 'assertion', 'GREEN', true, 1, 'xcodebuild', ['KitTests/ClampXCTests/testCapsHigh()'], [SOURCE], []],
    );
    assert.deepEqual(Object.keys(row).sort(), ROW_KEYS);
    assert.equal(verifyRow(row, await loadKey(RUN)).ok, true);

    const made = calls().slice(before);
    assert.deepEqual(made.map((c) => c.tool), ['xcode-select', 'simctl', 'xcodebuild', 'xcresulttool', 'xcodebuild', 'xcresulttool']);
    // no child ever inherits a GIT_* variable
    assert.deepEqual(made.map((c) => c.git_keys), Array(6).fill([]));
    const builds = made.filter((c) => c.tool === 'xcodebuild');
    assert.deepEqual(builds.map((c) => [c.cwd, c.fixed, c.bundle_existed]), [[EXPORT_DIR, false, false], [EXPORT_DIR, true, false]]);
    for (const build of builds) {
      assert.deepEqual(build.args, ['test', '-scheme', 'Kit', '-destination', 'platform=iOS Simulator,name=iPhone 17', '-only-testing:KitTests/ClampXCTests', '-resultBundlePath', build.bundle]);
      // a fresh bundle path, outside the work dir and the repo, gone after its step
      assert.equal(path.basename(build.bundle), 'r.xcresult');
      assert.equal(build.bundle.startsWith(`${REPO}${path.sep}`), false);
      assert.equal(existsSync(path.dirname(build.bundle)), false);
    }
    assert.notEqual(builds[0].bundle, builds[1].bundle);
    assert.deepEqual(made.filter((c) => c.tool === 'xcresulttool').map((c) => c.args.slice(-1)[0]), builds.map((c) => c.bundle));
    assert.deepEqual(treeHashes(), tree);
    assert.equal(readFileSync(path.join(REPO, SOURCE), 'utf8'), '// clamp FIXED\n');
  } finally {
    delete process.env.GIT_CF_FAKE;
  }
});

test('(6) a Swift Testing case: -only-testing keeps the (), RED assertion, GREEN, the row names that test', async () => {
  setMode('swift');
  try {
    const before = calls().length;
    const testArg = `${SWIFT_TEST}::ClampSwiftTests/capsHigh()`;
    const res = await cli(proveArgs(testArg));
    assert.deepEqual([res.code, res.stderr], [0, `RED ${testArg}\nGREEN ${testArg}\n`]);
    const printed = JSON.parse(res.stdout);
    assert.deepEqual([printed.red, printed.red_kind, printed.green, printed.runner, printed.failed_tests], ['RED', 'assertion', 'GREEN', 'xcodebuild', ['KitTests/ClampSwiftTests/capsHigh()']]);
    const only = calls().slice(before).filter((c) => c.tool === 'xcodebuild').map((c) => c.args.find((/** @type {string} */ a) => a.startsWith('-only-testing:')));
    assert.deepEqual(only, ['-only-testing:KitTests/ClampSwiftTests/capsHigh()', '-only-testing:KitTests/ClampSwiftTests/capsHigh()']);
  } finally {
    setMode('normal');
  }
});

test('(3)(6) a platform=macOS runner never asks simctl and still proves', async () => {
  const before = calls().length;
  const res = await cli(proveArgs(MAC_TEST));
  assert.deepEqual([res.code, res.stderr], [0, `RED ${MAC_TEST}\nGREEN ${MAC_TEST}\n`]);
  assert.deepEqual(calls().slice(before).map((c) => c.tool), ['xcode-select', 'xcodebuild', 'xcresulttool', 'xcodebuild', 'xcresulttool']);
});

test('(4)(5)(6) through the CLI: a build failure ⇒ RED_INVALID compile; no bundle, a failed or broken xcresulttool, other tests ⇒ RED_INVALID error; exit 1, the export restored', async () => {
  const cases = [
    ['build-failed', 'compile'],
    ['nobundle', 'error'],
    ['toolfail', 'error'],
    ['broken', 'error'],
    ['near', 'error'],
  ];
  try {
    for (const [mode, kind] of cases) {
      setMode(mode);
      const before = calls().length;
      const rowsBefore = (await readAllRows(SLUG)).length;
      const res = await cli(proveArgs(XC_TEST));
      assert.deepEqual([mode, res.code, res.stderr], [mode, 1, `RED_INVALID ${XC_TEST} (${kind})\n`]);
      const printed = JSON.parse(res.stdout);
      assert.deepEqual([mode, printed.red, printed.red_kind, printed.green, printed.proven, printed.runner, printed.failed_tests], [mode, 'RED_INVALID', kind, null, false, 'xcodebuild', []]);
      const added = (await readAllRows(SLUG)).slice(rowsBefore);
      assert.deepEqual([mode, added.map((r) => [r.step, r.proven ?? null, r.runner ?? null, r.failed ?? null])], [mode, [['export', null, null, null], ['red-green', false, 'xcodebuild', null]]]);
      // RED only: GREEN never ran
      assert.deepEqual([mode, calls().slice(before).filter((c) => c.tool === 'xcodebuild').length], [mode, 1]);
      assert.deepEqual(
        [mode, hashOf(path.join(EXPORT_DIR, SOURCE)), existsSync(journalPath(EXPORT_DIR))],
        [mode, hashOf(path.join(REPO, SOURCE)), false],
      );
    }
  } finally {
    setMode('normal');
  }
});
