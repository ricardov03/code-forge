/**
 * B103 live check: `proof red-green` through the `xcodebuild` adapter on a real iOS simulator.
 *
 * Runs ONLY with `CODE_FORGE_LIVE_XCODE=1`, a full Xcode selected (`xcode-select -p` not the
 * Command Line Tools) and an available iPhone simulator (`xcrun simctl list devices available -j`);
 * otherwise it is skipped with the reason, so `npm test` never needs Xcode. Re-run it with:
 *
 *   CODE_FORGE_LIVE_XCODE=1 node --import ./test/helpers/isolate.mjs --test test/proof/xcodebuild-live.test.mjs
 *
 * With `CODE_FORGE_XCODE_CAPTURE=<dir>` set too, every step's capture (`<n>-<label>/` with
 * `step.json`, `console.txt` and the `xcresulttool` JSON `tests.json`) is copied there, also when
 * the check fails.
 *
 * The simulator: an already booted iPhone on an iOS runtime when there is one, else the first
 * available iPhone in `simctl`'s order. The test never boots, creates, erases or deletes a
 * simulator itself; `xcodebuild` may boot the one it is given. It needs no network.
 *
 * What it does, in one `mkdtemp` dir (removed afterwards):
 *   1. `swift package init --type library --name Kit` with `platforms: [.iOS(.v17), .macOS(.v14)]`;
 *      the base `clamp` is broken (it returns its value); `.code-forge/` ignored, `.code-forge.yml`
 *      with two xcodebuild runners (below), all committed;
 *   2. the block's work: `clamp` fixed, an XCTest class and a Swift Testing suite, each with one
 *      test that fails on the base (`capsHigh`) and one that passes on it (`keepsInside`); near-name
 *      suites that always fail (`ClampXCTestsExtra`, `MoreClampSwiftTests`), which `-only-testing`
 *      must not select; a `GhostTests` suite matched by a runner whose destination names a
 *      simulator that does not exist;
 *   3. through `runProof` (the CLI verb, export isolation): the ghost test ⇒ exit 1
 *      `destination-unavailable`, no row, no runner step, the package tree unchanged (hash);
 *   4. the XCTest file, the XCTest file with `::ClampXCTests/testCapsHigh`, the Swift Testing file,
 *      and the Swift Testing file with `::ClampSwiftTests/capsHigh()`: each RED assertion, then
 *      GREEN, exit 0, one signed row that covers `Sources/Kit/Kit.swift` with exactly the failed
 *      test in `failed_tests`; each step's `xcresulttool` JSON has the node shape of the B102
 *      fixture and reads to the same tests;
 *   5. a test of a new function `wrap` (added to the source in the same block): at RED the source
 *      is back at base, the test does not build ⇒ RED_INVALID compile, exit 1.
 * The Xcode version, the simulator and every step's duration are printed as test diagnostics.
 *
 * The runner argv is wrapped in a pass-through recorder (Node, argv only): it runs the real
 * `xcodebuild test …` argv, keeps its console, reads the result bundle with `xcresulttool` before
 * code-forge reads and removes it, and forwards stdout, stderr and the exit code unchanged. Its
 * timeout nests inside the runner's `timeout_s`, which nests inside the test's timeout; it records
 * its pid and its child's, and whatever is still running is killed after the test.
 * `-derivedDataPath` points into the temp dir, so nothing is built under the real home.
 */

import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFileSync, cpSync, existsSync, lstatSync, mkdirSync, readdirSync, readFileSync, readlinkSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { loadConfigFile } from '../../src/config/load.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { runnerHook } from '../../src/proof/runners.mjs';
import { readXcresult } from '../../src/proof/xcresult.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { git, tempDir } from './helpers.mjs';

const OPT_IN = 'live Xcode check: set CODE_FORGE_LIVE_XCODE=1 to run it (needs a full Xcode and an available iPhone simulator)';

/**
 * The simulator the live check uses: a booted iPhone on an iOS runtime, else the first available one.
 * @param {string | null} simctlJson - what `xcrun simctl list devices available -j` printed (null: it failed)
 * @returns {{name: string, udid: string, os: string} | null}
 */
function pickSimulator(simctlJson) {
  if (simctlJson === null) return null;
  let doc;
  try {
    doc = JSON.parse(simctlJson);
  } catch {
    return null;
  }
  if (doc === null || typeof doc !== 'object' || doc.devices === null || typeof doc.devices !== 'object') return null;
  /** @type {{name: string, udid: string, os: string, booted: boolean}[]} */
  const phones = [];
  for (const [runtime, devices] of Object.entries(doc.devices)) {
    const m = /^com\.apple\.CoreSimulator\.SimRuntime\.iOS-(\d+(?:-\d+)*)$/.exec(runtime);
    if (!m || !Array.isArray(devices)) continue;
    for (const d of devices) {
      if (d === null || typeof d !== 'object' || d.isAvailable !== true || typeof d.name !== 'string' || typeof d.udid !== 'string') continue;
      if (!d.name.startsWith('iPhone')) continue;
      phones.push({ name: d.name, udid: d.udid, os: m[1].replaceAll('-', '.'), booted: d.state === 'Booted' });
    }
  }
  const pick = phones.find((p) => p.booted) ?? phones[0];
  return pick ? { name: pick.name, udid: pick.udid, os: pick.os } : null;
}

/**
 * The facts in the `simctl` JSON that a `destination-unavailable` refusal for an iOS simulator
 * destination can name: the names of every available device on an iOS runtime (any kind).
 * `src/proof/runners.mjs` does not export the function that builds the refusal's list (and `src/`
 * is not changed for a test), so the live check does not copy its order or its cap: it compares the
 * printed names as a set with an exact count against these facts (`assertNamesAvailable`); the
 * order and the cap of 10 are B102's unit tests.
 * @param {string} simctlJson
 * @returns {Set<string>}
 */
function availableIosNames(simctlJson) {
  const doc = JSON.parse(simctlJson);
  /** @type {Set<string>} */
  const names = new Set();
  for (const [runtime, devices] of Object.entries(doc.devices)) {
    if (!/^com\.apple\.CoreSimulator\.SimRuntime\.iOS-\d+(?:-\d+)*$/.test(runtime)) continue;
    for (const d of /** @type {any[]} */ (devices)) if (d.isAvailable === true) names.add(d.name);
  }
  return names;
}

/** The refusal names at most this many simulators (proof.md §2 Xcode). */
const NAMED_AT_MOST = 10;

/**
 * Order-free: each printed name once, every one an available iOS-runtime device, and exactly all
 * of them when there are at most `NAMED_AT_MOST` (else exactly `NAMED_AT_MOST` of them).
 * @param {string[]} named - the names the refusal printed
 * @param {Set<string>} available - `availableIosNames` of the same `simctl` JSON
 */
function assertNamesAvailable(named, available) {
  const set = new Set(named);
  assert.equal(set.size, named.length, `a name is printed twice: ${named.join(', ')}`);
  assert.deepEqual(named.filter((n) => !available.has(n)), [], 'a printed name is not an available iOS-runtime device');
  assert.equal(named.length, Math.min(available.size, NAMED_AT_MOST), `printed ${named.length} of ${available.size} available names`);
}

/**
 * @param {{env?: Record<string, string | undefined>, developerDir?: string | null, simctlJson?: string | null}} where -
 *   the environment, what `xcode-select -p` printed (null: it failed) and what `simctl` listed
 * @returns {string | null} why the live test cannot run there, or null when it can
 */
function skipReason({ env = process.env, developerDir = null, simctlJson = null }) {
  if (env.CODE_FORGE_LIVE_XCODE !== '1') return OPT_IN;
  if (developerDir === null || developerDir === '') return 'live Xcode check: no developer directory is selected (xcode-select -p failed)';
  if (developerDir.includes('CommandLineTools')) return `live Xcode check: only the Command Line Tools are selected (${developerDir}); xcodebuild test needs a full Xcode`;
  if (pickSimulator(simctlJson) === null) return 'live Xcode check: xcrun simctl lists no available iPhone simulator on an iOS runtime (the check never creates or boots one)';
  return null;
}

/** @param {string} cmd @param {string[]} args @returns {string | null} stdout, or null when it fails */
function probe(cmd, args) {
  try {
    return execFileSync(cmd, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 60_000 });
  } catch {
    return null;
  }
}

const FLAG_SET = process.env.CODE_FORGE_LIVE_XCODE === '1';
// the probes only read, and run only with the flag set
const DEVELOPER_DIR = FLAG_SET ? (probe('xcode-select', ['-p'])?.trim() ?? null) : null;
const SIMCTL_JSON = FLAG_SET && DEVELOPER_DIR !== null && !DEVELOPER_DIR.includes('CommandLineTools') ? probe('xcrun', ['simctl', 'list', 'devices', 'available', '-j']) : null;
const reason = skipReason({ env: process.env, developerDir: DEVELOPER_DIR, simctlJson: SIMCTL_JSON });

const SLUG = 'xcode-live';
const RUN = `r-b103-xcode-${process.pid}-${Date.now().toString(36)}`;
const NODE = process.execPath;
const FIXTURES = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'xcresult');
const SOURCE = 'Sources/Kit/Kit.swift';
const XC_TEST = 'Tests/KitTests/ClampXCTests.swift';
const ST_TEST = 'Tests/KitTests/ClampSwiftTests.swift';
const GHOST_TEST = 'Tests/KitTests/GhostTests.swift';
const WRAP_TEST = 'Tests/KitTests/WrapTests.swift';
const GHOST_SIMULATOR = 'No Such iPhone 0';

const CLAMP_BROKEN = 'public func clamp(_ value: Int, _ low: Int, _ high: Int) -> Int {\n    return value\n}\n';
const CLAMP_FIXED = 'public func clamp(_ value: Int, _ low: Int, _ high: Int) -> Int {\n    return min(max(value, low), high)\n}\n';
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
/** Near-name suites: a loose filter would select them, and they always fail. */
const NEAR_XC_SOURCE = `import XCTest

final class ClampXCTestsExtra: XCTestCase {
    func testCapsHigh() {
        XCTFail("a near-name suite was selected")
    }
}
`;
const NEAR_ST_SOURCE = `import Testing

@Suite struct MoreClampSwiftTests {
    @Test func capsHigh() {
        #expect(Bool(false), "a near-name suite was selected")
    }
}
`;
const GHOST_SOURCE = `import Testing
@testable import Kit

@Suite struct GhostTests {
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
 * The timeouts nest, so the outer one never fires first and nothing outlives the test:
 * the recorder stops `xcodebuild` after `RECORDER_TIMEOUT_MS` (a live step takes about 35 s);
 * its worst case adds the 10 s SIGKILL grace and the 60 s `xcresulttool` read, and that sum stays
 * below the runner's `timeout_s`; the live test runs `RECORDED_STEPS` runner steps, so its
 * node:test timeout is `RECORDED_STEPS` × `timeout_s` plus `SETUP_ALLOWANCE_MS` (package init,
 * git, the precheck refusal). After the test (also when its timeout fires) every recorder and
 * child pid still running is killed (`killTracked`).
 */
const RECORDER_TIMEOUT_MS = 300_000;
const RUNNER_TIMEOUT_S = 420;
const RECORDER_KILL_GRACE_MS = 10_000;
const XCRESULTTOOL_TIMEOUT_MS = 60_000;
const RECORDED_STEPS = 9;
const SETUP_ALLOWANCE_MS = 10 * 60_000;
const LIVE_TIMEOUT_MS = RECORDED_STEPS * RUNNER_TIMEOUT_S * 1000 + SETUP_ALLOWANCE_MS;
if (!(RECORDER_TIMEOUT_MS + RECORDER_KILL_GRACE_MS + XCRESULTTOOL_TIMEOUT_MS < RUNNER_TIMEOUT_S * 1000)) throw new Error('the recorder worst case must be below the runner timeout_s');

/**
 * The pass-through recorder: argv `<capture dir> <label file> <pid dir> <timeout ms> <argv…>`. It
 * runs `argv` (no shell) and writes `<pid dir>/<its pid>.json` `{recorder, child}` while the child
 * runs (removed when it ends). It copies the console and the `xcresulttool` JSON of the
 * `-resultBundlePath` bundle into `<capture dir>/<n>-<label>/` with a `step.json`, then forwards
 * stdout, stderr and the exit code (or dies of the child's signal). The child never outlives it:
 * after `<timeout ms>` it gets SIGTERM, then SIGKILL 10 s later; a SIGINT, SIGTERM or SIGHUP sent
 * to the recorder is passed on to the child the same way.
 */
const RECORDER = `
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
const [captureDir, labelFile, pidDir, timeoutArg, ...argv] = process.argv.slice(2);
const TIMEOUT_MS = Number(timeoutArg);
if (!Number.isSafeInteger(TIMEOUT_MS) || TIMEOUT_MS <= 0 || argv.length === 0) {
  process.stderr.write("recorder: usage: <capture dir> <label file> <pid dir> <timeout ms> <argv…>\\n");
  process.exit(2);
}
const SIGNALS = ['SIGINT', 'SIGTERM', 'SIGHUP'];
const started = Date.now();
process.exitCode = 1;
const child = spawn(argv[0], argv.slice(1), { stdio: ['ignore', 'pipe', 'pipe'] });
const pidFile = path.join(pidDir, process.pid + '.json');
try {
  if (child.pid !== undefined) writeFileSync(pidFile, JSON.stringify({ recorder: process.pid, child: child.pid }));
} catch (e) {
  process.stderr.write('recorder: could not record the pids: ' + (e && e.message) + '\\n');
}
const out = [];
const err = [];
child.stdout.on('data', (b) => out.push(b));
child.stderr.on('data', (b) => err.push(b));
let killTimer = null;
const stop = (why) => {
  if (child.exitCode !== null || child.signalCode !== null) return;
  process.stderr.write('recorder: stopping ' + argv[0] + ' (' + why + ')\\n');
  child.kill('SIGTERM');
  if (killTimer === null) killTimer = setTimeout(() => child.kill('SIGKILL'), 10000);
};
const timer = setTimeout(() => stop('timed out after ' + TIMEOUT_MS + ' ms'), TIMEOUT_MS);
for (const sig of SIGNALS) process.on(sig, () => stop(sig));
child.on('error', (e) => {
  clearTimeout(timer);
  for (const sig of SIGNALS) process.removeAllListeners(sig);
  process.stderr.write('recorder: could not run ' + argv[0] + ': ' + e.message + '\\n');
});
child.on('close', (status, signal) => {
  clearTimeout(timer);
  if (killTimer !== null) clearTimeout(killTimer);
  for (const sig of SIGNALS) process.removeAllListeners(sig);
  rmSync(pidFile, { force: true });
  if (status === null && signal === null) return;
  const ms = Date.now() - started;
  const stdout = Buffer.concat(out);
  const stderr = Buffer.concat(err);
  try {
    const dest = path.join(captureDir, String(readdirSync(captureDir).length + 1).padStart(2, '0') + '-' + readFileSync(labelFile, 'utf8').trim());
    mkdirSync(dest);
    const at = argv.indexOf('-resultBundlePath');
    const bundle = at >= 0 ? argv[at + 1] : null;
    const hasBundle = bundle !== null && existsSync(bundle);
    let tool = null;
    if (hasBundle) {
      const x = spawnSync('xcrun', ['xcresulttool', 'get', 'test-results', 'tests', '--path', bundle], { stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 64 * 1024 * 1024, timeout: 60000, killSignal: 'SIGKILL' });
      tool = x.status;
      if (x.status === 0) writeFileSync(path.join(dest, 'tests.json'), x.stdout);
    }
    writeFileSync(path.join(dest, 'step.json'), JSON.stringify({ code: status, signal, ms, bundle: hasBundle, tool }));
    writeFileSync(path.join(dest, 'console.txt'), String(stdout) + '\\n--- stderr\\n' + String(stderr));
  } catch (e) {
    process.stderr.write('recorder: could not keep the capture: ' + (e && e.message) + '\\n');
  }
  // never process.exit(): it can cut a pipe write short; the process ends once both streams flush
  process.stdout.write(stdout, () =>
    process.stderr.write(stderr, () => {
      // a child killed by a signal: die of the same signal (exit code 1 if it is caught or ignored)
      if (signal) process.kill(process.pid, signal);
      else process.exitCode = status ?? 1;
    }),
  );
});
`;

/** @param {string} root @param {string} rel @param {string} text */
function write(root, rel, text) {
  mkdirSync(path.dirname(path.join(root, rel)), { recursive: true });
  writeFileSync(path.join(root, rel), text);
}

/** @param {string} cmd @param {string[]} args @param {string} cwd @returns {string} stdout */
function run(cmd, args, cwd) {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) if (!key.startsWith('GIT_')) env[key] = value;
  return execFileSync(cmd, args, { cwd, env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5 * 60_000 });
}

/**
 * A hash of every path, type, mode and content under `root`, `.git` excluded.
 * @param {string} root @returns {string}
 */
function treeHash(root) {
  const hash = createHash('sha256');
  /** @param {string} rel */
  const walk = (rel) => {
    for (const name of readdirSync(path.join(root, rel)).sort()) {
      if (rel === '' && name === '.git') continue;
      const relPath = rel === '' ? name : `${rel}/${name}`;
      const abs = path.join(root, relPath);
      const st = lstatSync(abs);
      if (st.isSymbolicLink()) hash.update(`L\0${relPath}\0${readlinkSync(abs)}\0`);
      else if (st.isDirectory()) {
        hash.update(`D\0${relPath}\0`);
        walk(relPath);
      } else hash.update(`F\0${relPath}\0${st.mode}\0`).update(readFileSync(abs)).update('\0');
    }
  };
  walk('');
  return hash.digest('hex');
}

/**
 * The node shape of `xcresulttool get test-results tests` JSON: its top-level keys, and per node
 * its type, its sorted keys and its children's shapes (values are not compared). Siblings are
 * sorted: Swift Testing runs a suite's tests in parallel, so their order changes from run to run.
 * @param {any} doc @returns {unknown}
 */
function jsonShape(doc) {
  /** @param {any} node @returns {unknown} */
  const shape = (node) => [node.nodeType, Object.keys(node).sort().join(','), sorted((node.children ?? []).map(shape))];
  return [Object.keys(doc).sort().join(','), sorted(doc.testNodes.map(shape))];
}

/** @param {unknown[]} list @returns {unknown[]} the list sorted by each item's JSON */
function sorted(list) {
  return list.map((item) => [JSON.stringify(item), item]).sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0)).map((pair) => pair[1]);
}

/**
 * `readXcresult`'s tests, sorted by id (Swift Testing's order changes from run to run).
 * @param {string} text
 */
const testsOf = (text) => [...readXcresult(text).tests].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));

const fixtureText = (/** @type {string} */ name) => readFileSync(path.join(FIXTURES, name), 'utf8');

/** @param {number} pid @returns {boolean} */
function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/**
 * Kill every recorder and child pid `<pid dir>` still lists: SIGTERM, then SIGKILL to any that
 * is alive 5 s later. Children first, so a recorder never forwards to a dead child id.
 * @param {string} pidDir
 * @returns {Promise<number[]>} the pids that were still alive
 */
async function killTracked(pidDir) {
  if (!existsSync(pidDir)) return [];
  /** @type {number[]} */
  const pids = [];
  for (const name of readdirSync(pidDir)) {
    try {
      const { recorder, child } = JSON.parse(readFileSync(path.join(pidDir, name), 'utf8'));
      for (const pid of [child, recorder]) if (Number.isSafeInteger(pid) && pid > 0 && pid !== process.pid) pids.push(pid);
    } catch {
      // a half-written pid file: its recorder ends with its own timeout
    }
  }
  const living = pids.filter(alive);
  for (const pid of living) {
    try {
      process.kill(pid, 'SIGTERM');
    } catch {}
  }
  const deadline = Date.now() + 5000;
  while (living.some(alive) && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 50));
  for (const pid of living.filter(alive)) {
    try {
      process.kill(pid, 'SIGKILL');
    } catch {}
  }
  return living;
}

/** Every pid dir a test made; `after` kills what they still list, also when a test timed out. */
/** @type {Set<string>} */
const PID_DIRS = new Set();
after(async () => {
  for (const dir of PID_DIRS) await killTracked(dir);
});

const LIVE = { skip: reason ?? false, timeout: LIVE_TIMEOUT_MS };

test('B103 live: xcodebuild test on an iOS simulator — XCTest and Swift Testing prove RED assertion then GREEN; a new function is RED_INVALID compile; a missing destination is refused before anything changes', LIVE, async (t) => {
  const started = Date.now();
  const sim = /** @type {{name: string, udid: string, os: string}} */ (pickSimulator(SIMCTL_JSON));
  const parent = tempDir('xcode-live');
  const pkg = path.join(parent, 'Kit');
  const tools = path.join(parent, 'tools');
  const captureDir = path.join(tools, 'capture');
  const labelFile = path.join(tools, 'label.txt');
  const derived = path.join(parent, 'dd');
  const pidDir = path.join(tools, 'pids');
  mkdirSync(captureDir, { recursive: true });
  mkdirSync(pidDir);
  PID_DIRS.add(pidDir);
  writeFileSync(path.join(tools, 'recorder.mjs'), RECORDER);
  try {
    t.diagnostic(`xcode: ${(probe('xcodebuild', ['-version']) ?? '').trim().split('\n').join(' ')}`);
    t.diagnostic(`developer dir: ${DEVELOPER_DIR}`);
    t.diagnostic(`simulator: ${sim.name} (iOS ${sim.os}, ${sim.udid})`);
    t.diagnostic(`platform: ${process.platform} ${process.arch}`);

    // 1. the base
    mkdirSync(pkg);
    run('swift', ['package', 'init', '--type', 'library', '--name', 'Kit'], pkg);
    rmSync(path.join(pkg, 'Tests/KitTests/KitTests.swift'), { force: true });
    const manifest = readFileSync(path.join(pkg, 'Package.swift'), 'utf8');
    const withPlatforms = manifest.replace('    name: "Kit",\n', '    name: "Kit",\n    platforms: [.iOS(.v17), .macOS(.v14)],\n');
    assert.notEqual(withPlatforms, manifest, 'Package.swift has no `name: "Kit",` line to add platforms after');
    writeFileSync(path.join(pkg, 'Package.swift'), withPlatforms);
    write(pkg, SOURCE, CLAMP_BROKEN);
    appendFileSync(path.join(pkg, '.gitignore'), '\n.code-forge/\n');
    const xcodeArgv = ['xcodebuild', 'test', '-scheme', '{scheme}', '-destination', '{destination}', '{only_testing}', '-derivedDataPath', derived, '-resultBundlePath', '{result_bundle}'];
    const recorded = [NODE, path.join(tools, 'recorder.mjs'), captureDir, labelFile, pidDir, String(RECORDER_TIMEOUT_MS), ...xcodeArgv];
    const runners = [
      {
        match: [GHOST_TEST],
        adapter: 'xcodebuild',
        xcode: { scheme: 'Kit', destination: `platform=iOS Simulator,name=${GHOST_SIMULATOR}`, test_target: 'KitTests' },
        argv: recorded,
        timeout_s: RUNNER_TIMEOUT_S,
      },
      {
        match: ['Tests/**/*.swift'],
        adapter: 'xcodebuild',
        xcode: { scheme: 'Kit', destination: `platform=iOS Simulator,name=${sim.name},OS=${sim.os}`, test_target: 'KitTests' },
        argv: recorded,
        timeout_s: RUNNER_TIMEOUT_S,
      },
    ];
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
    write(pkg, SOURCE, CLAMP_FIXED);
    write(pkg, XC_TEST, XC_SOURCE);
    write(pkg, ST_TEST, ST_SOURCE);
    write(pkg, 'Tests/KitTests/ClampXCTestsExtra.swift', NEAR_XC_SOURCE);
    write(pkg, 'Tests/KitTests/MoreClampSwiftTests.swift', NEAR_ST_SOURCE);
    write(pkg, GHOST_TEST, GHOST_SOURCE);
    const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: SLUG });
    const loaded = await loadConfigFile(path.join(pkg, '.code-forge.yml'));
    assert.equal(loaded.ok, true, JSON.stringify(loaded));
    await startRun({ workspace: pkg, project: SLUG, runId: RUN, writeRow, config: /** @type {Record<string, any>} */ (loaded.config) });
    await openBlock({ runId: RUN, id: 'A', level: 'L1', owned: [SOURCE, 'Tests/**'], acceptance: [{ clause: 'clamp caps at hi', tests: [XC_TEST, ST_TEST, GHOST_TEST, WRAP_TEST] }], writeRow });
    const key = await loadKey(RUN);

    /**
     * One `proof red-green` through the CLI verb (the real `runnerHook` and precheck).
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
      const steps = readdirSync(captureDir)
        .sort()
        .slice(stepsBefore)
        .map((name) => {
          const dir = path.join(captureDir, name);
          const json = existsSync(path.join(dir, 'tests.json')) ? readFileSync(path.join(dir, 'tests.json'), 'utf8') : null;
          return { name, json, ...JSON.parse(readFileSync(path.join(dir, 'step.json'), 'utf8')) };
        });
      t.diagnostic(`${label}: exit ${code} in ${Date.now() - proofStarted} ms; steps ${steps.length === 0 ? 'none' : steps.map((s) => `${s.name} exit ${s.code} ${s.ms} ms`).join('; ')}`);
      return { code, stdout, stderr, steps, added: (await readAllRows(SLUG)).slice(rowsBefore) };
    };

    /**
     * RED assertion, GREEN, exit 0, one signed row covering the source; two steps, each with a
     * result bundle whose JSON has the B102 fixture's shape (and, when one is named, its tests);
     * at GREEN exactly the `passed` test ids ran, each passed.
     * @param {Awaited<ReturnType<typeof prove>>} res @param {string} label @param {string[]} failed
     * @param {string[]} passed - every test id the GREEN step ran, sorted
     * @param {[string, string | null]} fixtures - the B102 fixture for the RED step and the GREEN step (null: none)
     */
    const assertProven = (res, label, failed, passed, fixtures) => {
      assert.deepEqual([res.code, res.stderr], [0, `RED ${label}\nGREEN ${label}\n`], res.stdout);
      assert.deepEqual(
        res.added.map((r) => [r.step, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed_tests ?? null, r.covers ?? null]),
        [
          ['export', null, null, null, null, null, null, null],
          ['red-green', 'RED', 'assertion', 'GREEN', true, 'xcodebuild', failed, [SOURCE]],
        ],
      );
      assert.equal(verifyRow(res.added[1], key).ok, true);
      assert.deepEqual(
        res.steps.map((s) => [s.code, s.signal, s.bundle, s.tool, s.json !== null]),
        [
          [65, null, true, 0, true],
          [0, null, true, 0, true],
        ],
      );
      const [redFixture, greenFixture] = fixtures;
      const red = JSON.parse(/** @type {string} */ (res.steps[0].json));
      const redExpected = JSON.parse(fixtureText(/** @type {string} */ (redFixture)));
      assert.deepEqual(jsonShape(red), jsonShape(redExpected), `${label} RED: the xcresulttool JSON shape differs from ${redFixture}`);
      assert.deepEqual(testsOf(/** @type {string} */ (res.steps[0].json)), testsOf(fixtureText(/** @type {string} */ (redFixture))));
      const green = testsOf(/** @type {string} */ (res.steps[1].json));
      assert.deepEqual(green, passed.map((id) => ({ id, status: 'passed' })), `${label} GREEN`);
      if (greenFixture !== null) {
        assert.deepEqual(jsonShape(JSON.parse(/** @type {string} */ (res.steps[1].json))), jsonShape(JSON.parse(fixtureText(greenFixture))), `${label} GREEN: the xcresulttool JSON shape differs from ${greenFixture}`);
        assert.deepEqual(green, testsOf(fixtureText(greenFixture)));
      }
    };

    // 3. a destination simctl does not list: refused before the export, the tree unchanged
    const before = treeHash(pkg);
    const ghost = await prove('ghost', GHOST_TEST);
    const prefix =
      "proof red-green: destination-unavailable: the runner's xcode.destination names no simulator that xcrun simctl lists as available (by name or id, and OS when given); available: ";
    const suffix = ' — code-forge never boots, creates or deletes a simulator; no proof was run\n';
    assert.deepEqual([ghost.code, ghost.stderr.startsWith(prefix), ghost.stderr.endsWith(suffix), ghost.added.length, ghost.steps.length], [1, true, true, 0, 0], ghost.stderr);
    const named = ghost.stderr.slice(prefix.length, -suffix.length).split(', ');
    // the names checked against the real simctl JSON (the probe the skip check made): a set, an exact count
    assertNamesAvailable(named, availableIosNames(/** @type {string} */ (SIMCTL_JSON)));
    assert.equal(named.includes(GHOST_SIMULATOR), false);
    assert.equal(treeHash(pkg), before);
    // refused before the export is built
    assert.equal(existsSync(path.join(pkg, '.code-forge', 'export')), false);

    // 4. XCTest and Swift Testing, by suite and by case
    const xc = await prove('xctest', XC_TEST);
    const xcIds = ['KitTests/ClampXCTests/testCapsHigh()', 'KitTests/ClampXCTests/testKeepsInside()'];
    const stIds = ['KitTests/ClampSwiftTests/capsHigh()', 'KitTests/ClampSwiftTests/keepsInside()'];
    assertProven(xc, XC_TEST, ['KitTests/ClampXCTests/testCapsHigh()'], xcIds, ['xctest.json', 'xctest-pass.json']);
    const xcOne = await prove('xctest-case', `${XC_TEST}::ClampXCTests/testCapsHigh`);
    assertProven(xcOne, `${XC_TEST}::ClampXCTests/testCapsHigh`, ['KitTests/ClampXCTests/testCapsHigh()'], ['KitTests/ClampXCTests/testCapsHigh()'], ['xctest-case.json', null]);
    const st = await prove('swift-testing', ST_TEST);
    assertProven(st, ST_TEST, ['KitTests/ClampSwiftTests/capsHigh()'], stIds, ['swift-testing.json', 'swift-testing-pass.json']);
    const stOne = await prove('swift-testing-case', `${ST_TEST}::ClampSwiftTests/capsHigh()`);
    assertProven(stOne, `${ST_TEST}::ClampSwiftTests/capsHigh()`, ['KitTests/ClampSwiftTests/capsHigh()'], ['KitTests/ClampSwiftTests/capsHigh()'], ['swift-testing-case.json', null]);

    // 5. a test of a new function: at RED the source is at base, so the test does not build
    write(pkg, SOURCE, `${CLAMP_FIXED}${WRAP}`);
    write(pkg, WRAP_TEST, WRAP_SOURCE);
    const wrap = await prove('compile', WRAP_TEST);
    assert.deepEqual([wrap.code, wrap.stderr], [1, `RED_INVALID ${WRAP_TEST} (compile)\n`], wrap.stdout);
    assert.deepEqual(
      wrap.added.map((r) => [r.step, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed_tests ?? null]),
      [
        ['export', null, null, null, null, null, null],
        ['red-green', 'RED_INVALID', 'compile', null, false, 'xcodebuild', []],
      ],
    );
    // xcodebuild exits 65 and still writes a bundle, with no test in it
    assert.deepEqual(
      wrap.steps.map((s) => [s.code, s.signal, s.bundle, s.tool, s.json !== null]),
      [[65, null, true, 0, true]],
    );
    const compileJson = /** @type {string} */ (wrap.steps[0].json);
    assert.deepEqual(jsonShape(JSON.parse(compileJson)), jsonShape(JSON.parse(fixtureText('build-failed.json'))), 'compile: the xcresulttool JSON shape differs from build-failed.json');
    assert.deepEqual(testsOf(compileJson), []);
    assert.deepEqual(testsOf(fixtureText('build-failed.json')), []);

    // the package itself was never built: xcodebuild ran in the export
    assert.equal(existsSync(path.join(pkg, '.swiftpm', 'xcode')), false);
    // the node:test timeout budgets exactly this many runner steps; every recorder has ended
    assert.deepEqual([readdirSync(captureDir).length, readdirSync(pidDir)], [RECORDED_STEPS, []]);
  } finally {
    const leftover = await killTracked(pidDir);
    if (leftover.length > 0) t.diagnostic(`killed pids still running: ${leftover.join(', ')}`);
    // kept on failure too: the capture is what explains a failed live run
    const keep = process.env.CODE_FORGE_XCODE_CAPTURE;
    if (keep && existsSync(captureDir)) {
      cpSync(captureDir, keep, { recursive: true });
      t.diagnostic(`captures copied to ${keep}`);
    }
    rmSync(parent, { recursive: true, force: true });
    t.diagnostic(`duration: ${Math.round((Date.now() - started) / 1000)} s`);
  }
});

const SIMCTL_SHAPE = (/** @type {Record<string, unknown[]>} */ devices) => JSON.stringify({ devices });
const IPHONE = { udid: 'FAKE-UDID-0001', isAvailable: true, state: 'Shutdown', name: 'iPhone 17' };
const XCODE_DIR = '/Applications/Xcode.app/Contents/Developer';

test('B103 live: without CODE_FORGE_LIVE_XCODE=1 the skip reason says how to opt in', () => {
  const simctlJson = SIMCTL_SHAPE({ 'com.apple.CoreSimulator.SimRuntime.iOS-26-5': [IPHONE] });
  assert.equal(skipReason({ env: {}, developerDir: XCODE_DIR, simctlJson }), OPT_IN);
  assert.equal(skipReason({ env: { CODE_FORGE_LIVE_XCODE: 'yes' }, developerDir: XCODE_DIR, simctlJson }), OPT_IN);
});

test('B103 live: with the flag set and only the Command Line Tools (or no developer dir) the skip reason says so', () => {
  const simctlJson = SIMCTL_SHAPE({ 'com.apple.CoreSimulator.SimRuntime.iOS-26-5': [IPHONE] });
  const env = { CODE_FORGE_LIVE_XCODE: '1' };
  assert.equal(
    skipReason({ env, developerDir: '/Library/Developer/CommandLineTools', simctlJson }),
    'live Xcode check: only the Command Line Tools are selected (/Library/Developer/CommandLineTools); xcodebuild test needs a full Xcode',
  );
  assert.equal(skipReason({ env, developerDir: null, simctlJson }), 'live Xcode check: no developer directory is selected (xcode-select -p failed)');
});

test('B103 live: with the flag set and no available iPhone simulator the skip reason says so', () => {
  const env = { CODE_FORGE_LIVE_XCODE: '1' };
  const none = 'live Xcode check: xcrun simctl lists no available iPhone simulator on an iOS runtime (the check never creates or boots one)';
  assert.equal(skipReason({ env, developerDir: XCODE_DIR, simctlJson: null }), none);
  assert.equal(skipReason({ env, developerDir: XCODE_DIR, simctlJson: 'not json' }), none);
  assert.equal(skipReason({ env, developerDir: XCODE_DIR, simctlJson: SIMCTL_SHAPE({ 'com.apple.CoreSimulator.SimRuntime.iOS-16-1': [] }) }), none);
  // an iPad only, an unavailable iPhone, an iPhone-named watch: none is an iPhone simulator to use
  const others = SIMCTL_SHAPE({
    'com.apple.CoreSimulator.SimRuntime.iOS-26-5': [
      { udid: 'FAKE-UDID-0002', isAvailable: true, state: 'Shutdown', name: 'iPad (A16)' },
      { ...IPHONE, isAvailable: false },
    ],
    'com.apple.CoreSimulator.SimRuntime.watchOS-26-0': [{ udid: 'FAKE-UDID-0003', isAvailable: true, state: 'Shutdown', name: 'iPhone Watch' }],
  });
  assert.equal(skipReason({ env, developerDir: XCODE_DIR, simctlJson: others }), none);
});

test('B103 live: with the flag, full Xcode and an iPhone simulator there is no skip reason; a booted iPhone is picked first', () => {
  const env = { CODE_FORGE_LIVE_XCODE: '1' };
  const simctlJson = SIMCTL_SHAPE({
    'com.apple.CoreSimulator.SimRuntime.iOS-26-5': [
      { udid: 'FAKE-UDID-0004', isAvailable: true, state: 'Shutdown', name: 'iPhone 17 Pro' },
      { udid: 'FAKE-UDID-0005', isAvailable: true, state: 'Booted', name: 'iPhone Air' },
    ],
  });
  assert.equal(skipReason({ env, developerDir: XCODE_DIR, simctlJson }), null);
  assert.deepEqual(pickSimulator(simctlJson), { name: 'iPhone Air', udid: 'FAKE-UDID-0005', os: '26.5' });
  const noneBooted = SIMCTL_SHAPE({ 'com.apple.CoreSimulator.SimRuntime.iOS-26-5': [IPHONE, { udid: 'FAKE-UDID-0004', isAvailable: true, state: 'Shutdown', name: 'iPhone 17 Pro' }] });
  assert.deepEqual(pickSimulator(noneBooted), { name: 'iPhone 17', udid: 'FAKE-UDID-0001', os: '26.5' });
});

test('B103 live: the name check takes every available iOS-runtime device name as a set, and wants each once and an exact count', () => {
  const simctlJson = SIMCTL_SHAPE({
    'com.apple.CoreSimulator.SimRuntime.iOS-26-5': [IPHONE, { udid: 'FAKE-UDID-0002', isAvailable: true, state: 'Shutdown', name: 'iPad (A16)' }, { udid: 'FAKE-UDID-0006', isAvailable: false, state: 'Shutdown', name: 'iPhone 16e' }],
    'com.apple.CoreSimulator.SimRuntime.iOS-18-6': [{ ...IPHONE, udid: 'FAKE-UDID-0007' }],
    'com.apple.CoreSimulator.SimRuntime.watchOS-26-0': [{ udid: 'FAKE-UDID-0003', isAvailable: true, state: 'Shutdown', name: 'Apple Watch Ultra 3' }],
  });
  const available = availableIosNames(simctlJson);
  assert.deepEqual([...available].sort(), ['iPad (A16)', 'iPhone 17']);
  // any order passes; a missing, a doubled or a foreign name fails
  assertNamesAvailable(['iPhone 17', 'iPad (A16)'], available);
  assert.throws(() => assertNamesAvailable(['iPhone 17'], available), /printed 1 of 2 available names/);
  assert.throws(() => assertNamesAvailable(['iPhone 17', 'iPhone 17'], available), /a name is printed twice/);
  assert.throws(() => assertNamesAvailable(['iPhone 17', 'Apple Watch Ultra 3'], available), /not an available iOS-runtime device/);
  // more than NAMED_AT_MOST: exactly that many, each one available
  const many = new Set(Array.from({ length: 12 }, (_, i) => `iPhone ${String(i).padStart(2, '0')}`));
  assertNamesAvailable([...many].slice(2), many);
  assert.throws(() => assertNamesAvailable([...many].slice(3), many), /printed 9 of 12 available names/);
});

/**
 * Write the recorder and a child that records its pid and then waits until it is killed.
 * @param {string} name
 */
function recorderSetup(name) {
  const dir = tempDir(name);
  const captureDir = path.join(dir, 'capture');
  mkdirSync(captureDir);
  const pidDir = path.join(dir, 'pids');
  mkdirSync(pidDir);
  PID_DIRS.add(pidDir);
  const labelFile = path.join(dir, 'label');
  writeFileSync(labelFile, 'hang');
  const recorder = path.join(dir, 'recorder.mjs');
  writeFileSync(recorder, RECORDER);
  const pidFile = path.join(dir, 'child.pid');
  const childArgv = [NODE, '-e', "require('fs').writeFileSync(process.argv[1], String(process.pid)); setInterval(() => {}, 1000);", pidFile];
  return { dir, captureDir, pidDir, labelFile, recorder, pidFile, childArgv };
}

/**
 * Clean up after a recorder test, also when an assertion failed: kill what the pid dir lists, the
 * child by the pid it wrote, and the recorder; then remove the dir.
 * @param {ReturnType<typeof recorderSetup>} s @param {import('node:child_process').ChildProcess} [recorder]
 */
async function recorderCleanup(s, recorder) {
  await killTracked(s.pidDir);
  const pid = existsSync(s.pidFile) ? Number(readFileSync(s.pidFile, 'utf8')) : 0;
  for (const p of [pid, recorder?.pid ?? 0]) {
    if (Number.isSafeInteger(p) && p > 0 && alive(p)) {
      try {
        process.kill(p, 'SIGKILL');
      } catch {}
    }
  }
  rmSync(s.dir, { recursive: true, force: true });
}

/** @param {ReturnType<typeof recorderSetup>} s @returns {Promise<number>} the child's pid once it has started */
async function childStarted(s) {
  const deadline = Date.now() + 20_000;
  while (!existsSync(s.pidFile) || readFileSync(s.pidFile, 'utf8') === '' || readdirSync(s.pidDir).length === 0) {
    assert.equal(Date.now() < deadline, true, 'the child never started');
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  return Number(readFileSync(s.pidFile, 'utf8'));
}

test('B103 live: the recorder stops a child that runs past its timeout and dies of the same signal', async () => {
  const s = recorderSetup('recorder-timeout');
  try {
    const started = Date.now();
    const r = spawnSync(NODE, [s.recorder, s.captureDir, s.labelFile, s.pidDir, '1500', ...s.childArgv], { encoding: 'utf8', timeout: 30_000, killSignal: 'SIGKILL' });
    const took = Date.now() - started;
    const pid = Number(readFileSync(s.pidFile, 'utf8'));
    assert.deepEqual([r.status, r.signal, alive(pid), took < 10_000, readdirSync(s.pidDir)], [null, 'SIGTERM', false, true, []], r.stderr);
    assert.equal(r.stderr, `recorder: stopping ${NODE} (timed out after 1500 ms)\n`);
    const step = JSON.parse(readFileSync(path.join(s.captureDir, '01-hang', 'step.json'), 'utf8'));
    assert.deepEqual([step.code, step.signal, step.bundle, step.tool], [null, 'SIGTERM', false, null]);
  } finally {
    await recorderCleanup(s);
  }
});

test('B103 live: a SIGTERM sent to the recorder (the runner timing out) is passed on, so the child does not outlive it', async () => {
  const s = recorderSetup('recorder-signal');
  const child = spawn(NODE, [s.recorder, s.captureDir, s.labelFile, s.pidDir, '600000', ...s.childArgv], { stdio: ['ignore', 'pipe', 'pipe'] });
  try {
    let stderr = '';
    child.stderr?.on('data', (b) => (stderr += b));
    /** @type {Promise<[number | null, string | null]>} */
    const ended = new Promise((resolve) => child.on('close', (code, signal) => resolve([code, signal])));
    const pid = await childStarted(s);
    assert.deepEqual([alive(pid), JSON.parse(readFileSync(path.join(s.pidDir, `${child.pid}.json`), 'utf8'))], [true, { recorder: child.pid, child: pid }]);
    child.kill('SIGTERM');
    const timer = setTimeout(() => child.kill('SIGKILL'), 20_000);
    const [code, signal] = await ended;
    clearTimeout(timer);
    assert.deepEqual([code, signal, alive(pid), readdirSync(s.pidDir)], [null, 'SIGTERM', false, []], stderr);
    assert.equal(stderr, `recorder: stopping ${NODE} (SIGTERM)\n`);
  } finally {
    await recorderCleanup(s, child);
  }
});

test('B103 live: killTracked ends a recorder and its child that are still running (what the after hook does when a test times out)', async () => {
  const s = recorderSetup('recorder-tracked');
  const child = spawn(NODE, [s.recorder, s.captureDir, s.labelFile, s.pidDir, '600000', ...s.childArgv], { stdio: 'ignore' });
  try {
    /** @type {Promise<[number | null, string | null]>} */
    const ended = new Promise((resolve) => child.on('close', (code, signal) => resolve([code, signal])));
    const pid = await childStarted(s);
    const killed = await killTracked(s.pidDir);
    const [, signal] = await ended;
    assert.deepEqual([[...killed].sort(), alive(pid), alive(/** @type {number} */ (child.pid)), signal], [[pid, /** @type {number} */ (child.pid)].sort(), false, false, 'SIGTERM']);
    assert.deepEqual(await killTracked(s.pidDir), []);
  } finally {
    await recorderCleanup(s, child);
  }
});

test('B103 live: the recorder refuses a missing or bad timeout without running anything', async () => {
  const s = recorderSetup('recorder-usage');
  try {
    for (const bad of ['0', '-5', 'soon', '1.5']) {
      const r = spawnSync(NODE, [s.recorder, s.captureDir, s.labelFile, s.pidDir, bad, ...s.childArgv], { encoding: 'utf8', timeout: 30_000, killSignal: 'SIGKILL' });
      assert.deepEqual(
        [r.status, r.stderr, existsSync(s.pidFile), readdirSync(s.captureDir).length, readdirSync(s.pidDir).length],
        [2, 'recorder: usage: <capture dir> <label file> <pid dir> <timeout ms> <argv…>\n', false, 0, 0],
        bad,
      );
    }
  } finally {
    await recorderCleanup(s);
  }
});
