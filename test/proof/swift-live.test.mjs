/**
 * B88 live check: `proof red-green` through the `swift` adapter on a real SwiftPM package.
 *
 * Runs ONLY with `CODE_FORGE_LIVE_SWIFT=1` and `swift` on PATH; otherwise it is skipped with the
 * reason, so `npm test` never needs Swift. Re-run it with:
 *
 *   CODE_FORGE_LIVE_SWIFT=1 node --import ./test/helpers/isolate.mjs --test test/proof/swift-live.test.mjs
 *
 * With `CODE_FORGE_SWIFT_CAPTURE=<dir>` set too, every step's report dir is copied there
 * (`<n>-<label>/`, with `step.json` and `console.txt`), also when the check fails; that is how the
 * Swift fixtures in `test/fixtures/junit/` were made (see `swift-adapter.test.mjs`).
 *
 * What it does, in one `mkdtemp` dir (removed afterwards):
 *   1. `swift package init --type library --name Kit`; the base `clamp` is broken (it returns its
 *      value); `.code-forge/` ignored, `.code-forge.yml` with the swift runner below, all committed;
 *   2. the block's work: `clamp` and `half` fixed, an XCTest class and a Swift Testing suite, each
 *      with one test that fails on the base (`capsHigh`) and one that passes on it (`keepsInside`);
 *      near-name suites that always fail (`ClampXCTestsExtra`, `MoreClampXCTests` with its own
 *      `testCapsHigh`, `MoreClampSwiftTests` with its own `capsHigh()`), which neither anchored
 *      filter may select; a test of `half` (it crashes on the base); a file whose suite is not
 *      named after it; without XCTest no XCTest file is written;
 *   3. through `runProof` (the CLI verb, export isolation): the XCTest file (no `::case`, so the
 *      filter is the file's anchored base name), the XCTest file with
 *      `::ClampXCTests/testCapsHigh`, the Swift Testing file, and the Swift Testing file with
 *      `::ClampSwiftTests/capsHigh()`: each RED assertion, then GREEN, exit 0, one signed row that
 *      covers `Sources/Kit/Kit.swift`, with the exact failed test and no near-name test in it;
 *   4. the `half` test crashes at RED (`Fatal error`, signal 5; SwiftPM leaves the Swift Testing
 *      report cut off) ⇒ RED_INVALID error, never compile; the misnamed file selects no test:
 *      `swift test` warns "No matching test cases were run", exits 0 and writes both reports with
 *      zero tests ⇒ NOT_RED, exit 1;
 *   5. then a test of a new function `wrap` (added to the source in the same block): at RED the
 *      source is back at base, the test does not compile ⇒ RED_INVALID compile, exit 1;
 *   6. SwiftPM built in the export (`.code-forge/export/A/.build`), never in the package.
 * XCTest is skipped with the reason when only the Command Line Tools are selected (no XCTest).
 * The versions and every step's duration are printed as test diagnostics.
 *
 * The runner argv is wrapped in a pass-through recorder (Node, argv only): it runs the real
 * `swift test …` argv, forwards its stdout, stderr and exit code unchanged, and keeps a copy of the
 * report dir before code-forge reads and removes it.
 */

import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { accessSync, appendFileSync, constants, cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { loadConfigFile } from '../../src/config/load.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { swiftCompileError } from '../../src/proof/junit.mjs';
import { runnerHook } from '../../src/proof/runners.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { git, tempDir } from './helpers.mjs';

/**
 * @param {string} cmd @param {string} searchPath - a PATH value
 * @returns {boolean} an executable `cmd` is in one of the `searchPath` directories
 */
function onPath(cmd, searchPath) {
  for (const dir of searchPath.split(path.delimiter)) {
    if (dir === '') continue;
    try {
      accessSync(path.join(dir, cmd), constants.X_OK);
      return true;
    } catch {
      // not in this directory
    }
  }
  return false;
}

/**
 * @param {{env?: Record<string, string | undefined>, path?: string}} [where] - the environment and
 *   PATH to judge (default: this process's)
 * @returns {string | null} why the live test cannot run there, or null when it can
 */
function skipReason({ env = process.env, path: searchPath = env.PATH ?? '' } = {}) {
  if (env.CODE_FORGE_LIVE_SWIFT !== '1') return 'live Swift check: set CODE_FORGE_LIVE_SWIFT=1 to run it (needs swift on PATH)';
  if (!onPath('swift', searchPath)) return 'live Swift check: swift not on PATH';
  return null;
}

/**
 * @param {string} developerDir - what `xcode-select -p` printed ('' off macOS)
 * @param {string} platform
 * @returns {string | null} why XCTest cannot run here, or null when it can
 */
function xctestSkipReason(developerDir, platform) {
  if (platform !== 'darwin') return null; // swift-corelibs-xctest ships with the toolchain
  if (developerDir === '') return 'XCTest: no developer directory is selected (xcode-select -p failed)';
  if (developerDir.includes('CommandLineTools')) return `XCTest: only the Command Line Tools are selected (${developerDir}); XCTest needs Xcode`;
  return null;
}

const SLUG = 'swift-live';
const RUN = 'r-b88-swift-live';
const NODE = process.execPath;
const SOURCE = 'Sources/Kit/Kit.swift';
const XC_TEST = 'Tests/KitTests/ClampXCTests.swift';
const ST_TEST = 'Tests/KitTests/ClampSwiftTests.swift';
const WRAP_TEST = 'Tests/KitTests/WrapTests.swift';
const HALF_TEST = 'Tests/KitTests/HalfTests.swift';
const MISNAMED_TEST = 'Tests/KitTests/Misnamed.swift';
const SWIFT_ARGV = Object.freeze(['swift', 'test', '--parallel', '--filter', '{test_filter}', '--xunit-output', '{report}']);

const CLAMP_BROKEN = 'public func clamp(_ value: Int, _ low: Int, _ high: Int) -> Int {\n    return value\n}\n';
const CLAMP_FIXED = 'public func clamp(_ value: Int, _ low: Int, _ high: Int) -> Int {\n    return min(max(value, low), high)\n}\n';
const HALF_BROKEN = '\npublic func half(_ value: Int) -> Int {\n    fatalError("half is not written yet")\n}\n';
const HALF_FIXED = '\npublic func half(_ value: Int) -> Int {\n    return value / 2\n}\n';
const WRAP = '\npublic func wrap(_ value: Int, _ size: Int) -> Int {\n    return ((value % size) + size) % size\n}\n';
const XC_SOURCE = `import XCTest
@testable import Kit

final class ClampXCTests: XCTestCase {
    func testCapsHigh() {
        XCTAssertEqual(clamp(12, 0, 10), 10)
    }

    func testKeepsInside() {
        XCTAssertEqual(clamp(5, 0, 10), 5)
    }
}
`;
const ST_SOURCE = `import Testing
@testable import Kit

@Suite struct ClampSwiftTests {
    @Test func capsHigh() {
        #expect(clamp(12, 0, 10) == 10)
    }

    @Test func keepsInside() {
        #expect(clamp(5, 0, 10) == 5)
    }
}
`;
/** Near-name suites: an unanchored filter would select them, and they always fail. */
const NEAR_XC_SOURCE = `import XCTest

final class ClampXCTestsExtra: XCTestCase {
    func testAlwaysFails() {
        XCTFail("a near-name suite was selected")
    }

}

final class MoreClampXCTests: XCTestCase {
    func testCapsHigh() {
        XCTFail("a near-name suite was selected")
    }
}
`;
const NEAR_ST_SOURCE = `import Testing

@Suite struct MoreClampSwiftTests {
    @Test func alwaysFails() {
        #expect(Bool(false), "a near-name suite was selected")
    }

    @Test func capsHigh() {
        #expect(Bool(false), "a near-name suite was selected")
    }
}
`;
const HALF_SOURCE = `import Testing
@testable import Kit

@Suite struct HalfTests {
    @Test func halves() {
        #expect(half(8) == 4)
    }
}
`;
const MISNAMED_SOURCE = `import Testing
@testable import Kit

@Suite struct SomeOtherName {
    @Test func keepsInside() {
        #expect(clamp(5, 0, 10) == 5)
    }
}
`;
const WRAP_SOURCE = `import Testing
@testable import Kit

@Suite struct WrapTests {
    @Test func wrapsAround() {
        #expect(wrap(12, 10) == 2)
    }
}
`;

/**
 * The pass-through recorder: argv `<capture dir> <label file> <argv…>`. It runs `argv` (no shell),
 * forwards stdout, stderr and the exit code, and copies the `--xunit-output` dir into
 * `<capture dir>/<n>-<label>/` with a `step.json` (exit code, duration, file names).
 */
const RECORDER = `
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
const [captureDir, labelFile, ...argv] = process.argv.slice(2);
const started = Date.now();
const r = spawnSync(argv[0], argv.slice(1), { stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 64 * 1024 * 1024 });
const ms = Date.now() - started;
// never process.exit(): it can cut a pipe write short; the exit code is set, and the process ends
// once both streams are flushed (each write's callback)
process.exitCode = 1;
if (r.error) {
  process.stderr.write('recorder: could not run ' + argv[0] + ': ' + r.error.message + '\\n');
} else {
  const reportDir = path.dirname(argv[argv.indexOf('--xunit-output') + 1]);
  const files = readdirSync(reportDir).sort();
  const dest = path.join(captureDir, String(readdirSync(captureDir).length + 1).padStart(2, '0') + '-' + readFileSync(labelFile, 'utf8').trim());
  mkdirSync(dest);
  for (const f of files) copyFileSync(path.join(reportDir, f), path.join(dest, f));
  writeFileSync(path.join(dest, 'step.json'), JSON.stringify({ code: r.status, signal: r.signal, ms, files }));
  writeFileSync(path.join(dest, 'console.txt'), String(r.stdout ?? '') + '\\n--- stderr\\n' + String(r.stderr ?? ''));
  process.stdout.write(r.stdout, () =>
    process.stderr.write(r.stderr, () => {
      // a child killed by a signal: die of the same signal (exit code 1 if it is caught or ignored)
      if (r.signal) process.kill(process.pid, r.signal);
      else process.exitCode = r.status ?? 1;
    }),
  );
}
`;

/** @param {string} root @param {string} rel @param {string} text */
function write(root, rel, text) {
  mkdirSync(path.dirname(path.join(root, rel)), { recursive: true });
  writeFileSync(path.join(root, rel), text);
}

/** @param {string} cmd @param {string[]} args @param {string} cwd @returns {string} stdout and stderr */
function run(cmd, args, cwd) {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) if (!key.startsWith('GIT_')) env[key] = value;
  const out = execFileSync(cmd, args, { cwd, env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  return out;
}

/** @param {string} cmd @param {string[]} args @returns {string} the first line it prints, or '' when it fails */
function firstLine(cmd, args) {
  try {
    return execFileSync(cmd, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim().split('\n')[0];
  } catch {
    return '';
  }
}

const reason = skipReason();
const LIVE = { skip: reason ?? false, timeout: 20 * 60 * 1000 };

test('B88 live: swift test through the swift adapter — XCTest and Swift Testing prove RED assertion then GREEN; a test of a new function is RED_INVALID compile', LIVE, async (t) => {
  const started = Date.now();
  const parent = tempDir('swift-live');
  const pkg = path.join(parent, 'Kit');
  const tools = path.join(parent, 'tools');
  const captureDir = path.join(tools, 'capture');
  const labelFile = path.join(tools, 'label.txt');
  mkdirSync(captureDir, { recursive: true });
  writeFileSync(path.join(tools, 'recorder.mjs'), RECORDER);
  try {
    t.diagnostic(`swift: ${firstLine('swift', ['--version'])}`);
    t.diagnostic(`platform: ${process.platform} ${process.arch}`);
    const developerDir = process.platform === 'darwin' ? firstLine('xcode-select', ['-p']) : '';
    if (developerDir) t.diagnostic(`developer dir: ${developerDir}`);
    const xcodeVersion = process.platform === 'darwin' ? firstLine('xcodebuild', ['-version']) : '';
    if (xcodeVersion) t.diagnostic(`xcode: ${xcodeVersion}`);
    const xctestSkip = xctestSkipReason(developerDir, process.platform);
    if (xctestSkip) t.diagnostic(`skipped: ${xctestSkip}`);

    // 1. the base
    mkdirSync(pkg);
    run('swift', ['package', 'init', '--type', 'library', '--name', 'Kit'], pkg);
    rmSync(path.join(pkg, 'Tests/KitTests/KitTests.swift'), { force: true });
    write(pkg, SOURCE, `${CLAMP_BROKEN}${HALF_BROKEN}`);
    appendFileSync(path.join(pkg, '.gitignore'), '\n.code-forge/\n');
    const runners = [{ match: ['Tests/**/*.swift'], adapter: 'swift', argv: [NODE, path.join(tools, 'recorder.mjs'), captureDir, labelFile, ...SWIFT_ARGV], timeout_s: 1800 }];
    write(
      pkg,
      '.code-forge.yml',
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
      ].join('\n'),
    );
    git(['init', '-q'], pkg);
    git(['symbolic-ref', 'HEAD', 'refs/heads/main'], pkg);
    git(['add', '-A'], pkg);
    git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], pkg);

    // 2. the block's work
    write(pkg, SOURCE, `${CLAMP_FIXED}${HALF_FIXED}`);
    write(pkg, ST_TEST, ST_SOURCE);
    // without XCTest (Command Line Tools only) no XCTest file is written: it would not build
    if (xctestSkip === null) {
      write(pkg, XC_TEST, XC_SOURCE);
      write(pkg, 'Tests/KitTests/ClampXCTestsExtra.swift', NEAR_XC_SOURCE);
    }
    write(pkg, 'Tests/KitTests/MoreClampSwiftTests.swift', NEAR_ST_SOURCE);
    write(pkg, HALF_TEST, HALF_SOURCE);
    write(pkg, MISNAMED_TEST, MISNAMED_SOURCE);
    const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: SLUG });
    const loaded = await loadConfigFile(path.join(pkg, '.code-forge.yml'));
    assert.equal(loaded.ok, true);
    await startRun({ workspace: pkg, project: SLUG, runId: RUN, writeRow, config: /** @type {Record<string, any>} */ (loaded.config) });
    await openBlock({ runId: RUN, id: 'A', level: 'L1', owned: [SOURCE, 'Tests/**'], acceptance: [{ clause: 'clamp caps at hi', tests: [...(xctestSkip === null ? [XC_TEST] : []), ST_TEST, HALF_TEST, MISNAMED_TEST, WRAP_TEST] }], writeRow });
    const key = await loadKey(RUN);

    /**
     * One `proof red-green` through the CLI verb (the real `runnerHook`).
     * @param {string} label @param {string} testArg
     */
    const prove = async (label, testArg) => {
      writeFileSync(labelFile, label);
      const stepsBefore = readdirSync(captureDir).length;
      const rowsBefore = (await readAllRows(SLUG)).length;
      let stdout = '';
      let stderr = '';
      const proofStarted = Date.now();
      const code = await runProof(['red-green', 'A', '--run', RUN, '--test', testArg], {
        hook: runnerHook,
        stdout: { write: (s) => (stdout += s) },
        stderr: { write: (s) => (stderr += s) },
      });
      const steps = readdirSync(captureDir).sort().slice(stepsBefore).map((name) => ({ name, ...JSON.parse(readFileSync(path.join(captureDir, name, 'step.json'), 'utf8')) }));
      t.diagnostic(`${label}: exit ${code} in ${Date.now() - proofStarted} ms; steps ${steps.map((s) => `${s.name} exit ${s.code} ${s.ms} ms [${s.files.join(', ')}]`).join('; ')}`);
      return { code, stdout, stderr, steps, added: (await readAllRows(SLUG)).slice(rowsBefore) };
    };

    /**
     * RED assertion, GREEN, exit 0, one signed row covering the source.
     * @param {Awaited<ReturnType<typeof prove>>} res @param {string} label @param {string[]} failed
     */
    const assertProven = (res, label, failed) => {
      assert.deepEqual([res.code, res.stderr], [0, `RED ${label}\nGREEN ${label}\n`], res.stdout);
      assert.deepEqual(
        res.added.map((r) => [r.step, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed_tests ?? null, r.covers ?? null]),
        [
          ['export', null, null, null, null, null, null, null],
          ['red-green', 'RED', 'assertion', 'GREEN', true, 'swift', failed, [SOURCE]],
        ],
      );
      assert.equal(verifyRow(res.added[1], key).ok, true);
      // both reports are written at every step: the XCTest one and its Swift Testing sibling
      assert.deepEqual(res.steps.map((s) => s.files), [['report-swift-testing.xml', 'report.xml'], ['report-swift-testing.xml', 'report.xml']]);
    };

    // 3. XCTest (the filter is the file's base name), then Swift Testing, then one case
    if (xctestSkip === null) {
      const xc = await prove('xctest', XC_TEST);
      assertProven(xc, XC_TEST, ['KitTests.ClampXCTests.testCapsHigh']);
      // the case filter never selects MoreClampXCTests/testCapsHigh (it always fails)
      const xcOne = await prove('xctest-case', `${XC_TEST}::ClampXCTests/testCapsHigh`);
      assertProven(xcOne, `${XC_TEST}::ClampXCTests/testCapsHigh`, ['KitTests.ClampXCTests.testCapsHigh']);
    }
    const st = await prove('swift-testing', ST_TEST);
    assertProven(st, ST_TEST, ['KitTests.ClampSwiftTests.capsHigh()']);
    // the case filter never selects MoreClampSwiftTests/capsHigh() (it always fails)
    const one = await prove('swift-testing-case', `${ST_TEST}::ClampSwiftTests/capsHigh()`);
    assertProven(one, `${ST_TEST}::ClampSwiftTests/capsHigh()`, ['KitTests.ClampSwiftTests.capsHigh()']);

    /**
     * Not proven: exit 1, the stderr line, and one red-green row with these values.
     * @param {Awaited<ReturnType<typeof prove>>} res @param {string} line @param {string} redValue @param {string} kind
     */
    const assertNotProven = (res, line, redValue, kind) => {
      assert.deepEqual([res.code, res.stderr], [1, `${line}\n`], res.stdout);
      assert.deepEqual(
        res.added.map((r) => [r.step, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed_tests ?? null]),
        [
          ['export', null, null, null, null, null, null],
          ['red-green', redValue, kind, null, false, 'swift', []],
        ],
      );
    };

    // 4. a crash at RED is an error, never a compile error; a filter that selects no test exits 0: NOT_RED
    const crash = await prove('crash', HALF_TEST);
    assertNotProven(crash, `RED_INVALID ${HALF_TEST} (error)`, 'RED_INVALID', 'error');
    assert.equal(swiftCompileError(readFileSync(path.join(captureDir, crash.steps[0].name, 'console.txt'), 'utf8')), false);
    const misnamed = await prove('misnamed', MISNAMED_TEST);
    assertNotProven(misnamed, `NOT_RED ${MISNAMED_TEST}`, 'NOT_RED', 'none');
    assert.deepEqual(misnamed.steps.map((s) => [s.code, s.files]), [[0, ['report-swift-testing.xml', 'report.xml']]]);

    // 5. a test of a new function: at RED the source is at base, so the test does not compile
    write(pkg, SOURCE, `${CLAMP_FIXED}${HALF_FIXED}${WRAP}`);
    write(pkg, WRAP_TEST, WRAP_SOURCE);
    const wrap = await prove('compile', WRAP_TEST);
    assertNotProven(wrap, `RED_INVALID ${WRAP_TEST} (compile)`, 'RED_INVALID', 'compile');
    assert.deepEqual(
      wrap.steps.map((s) => [s.code, s.files]),
      [[1, []]],
    );

    // 6. proven in the export: SwiftPM built there, never in the package itself
    assert.deepEqual([existsSync(path.join(pkg, '.code-forge', 'export', 'A', '.build')), existsSync(path.join(pkg, '.build'))], [true, false]);
  } finally {
    // kept on failure too: the capture is what explains a failed live run
    const keep = process.env.CODE_FORGE_SWIFT_CAPTURE;
    if (keep && existsSync(captureDir)) {
      cpSync(captureDir, keep, { recursive: true });
      t.diagnostic(`reports copied to ${keep}`);
    }
    rmSync(parent, { recursive: true, force: true });
    t.diagnostic(`duration: ${Math.round((Date.now() - started) / 1000)} s`);
  }
});

test('B88 live: without CODE_FORGE_LIVE_SWIFT=1 the skip reason says how to opt in', () => {
  const optIn = 'live Swift check: set CODE_FORGE_LIVE_SWIFT=1 to run it (needs swift on PATH)';
  assert.equal(skipReason({ env: {}, path: '' }), optIn);
  assert.equal(skipReason({ env: { CODE_FORGE_LIVE_SWIFT: 'yes' }, path: '' }), optIn);
});

test('B88 live: with the flag set but swift not on PATH the skip reason says so', () => {
  const emptyBin = tempDir('live-swift-empty-bin');
  assert.equal(skipReason({ env: { CODE_FORGE_LIVE_SWIFT: '1' }, path: emptyBin }), 'live Swift check: swift not on PATH');
});

test('B88 live: with the flag set and swift on PATH there is no skip reason', () => {
  const bin = tempDir('live-swift-bin');
  writeFileSync(path.join(bin, 'swift'), '#!/bin/sh\n', { mode: 0o755 });
  assert.equal(skipReason({ env: { CODE_FORGE_LIVE_SWIFT: '1' }, path: bin }), null);
});

test('B88 live: XCTest is skipped with the reason when only the Command Line Tools are selected', () => {
  assert.equal(
    xctestSkipReason('/Library/Developer/CommandLineTools', 'darwin'),
    'XCTest: only the Command Line Tools are selected (/Library/Developer/CommandLineTools); XCTest needs Xcode',
  );
  assert.equal(xctestSkipReason('', 'darwin'), 'XCTest: no developer directory is selected (xcode-select -p failed)');
  assert.equal(xctestSkipReason('/Applications/Xcode.app/Contents/Developer', 'darwin'), null);
  assert.equal(xctestSkipReason('', 'linux'), null);
});
