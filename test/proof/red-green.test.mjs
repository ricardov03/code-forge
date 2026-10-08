/**
 * B10b: the journaled red→green runner on a script-built fixture repo (never a committed nested
 * `.git`). The repo is built from `test/fixtures/repos/red-green/{base,current}/` under ONE
 * `mkdtemp` parent per file, removed in `after()`:
 *
 *   base commit:   src/math.mjs (`clamp` not implemented) · test/add.spec.mjs (pins `add`) ·
 *                  assets/logo.bin (bytes 0..255, generated here)
 *   working tree:  src/math.mjs (`clamp` implemented) · test/clamp.spec.mjs (new case) ·
 *                  src/format.mjs + test/format.spec.mjs (new module + its test) ·
 *                  tools/slow.mjs (a test command that waits) · assets/logo.bin (bytes 255..0)
 */

import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { after, before, mock, test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { buildExport } from '../../src/proof/export.mjs';
import { MECHANISMS, deleteAssertions, journalPath, readJournal, restoreJournal, runRedGreen } from '../../src/proof/red-green.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const FIXTURE = path.join(HERE, '..', 'fixtures', 'repos', 'red-green');
const RED_GREEN_URL = pathToFileURL(path.join(HERE, '..', '..', 'src', 'proof', 'red-green.mjs')).href;
const SLUG = 'red-green';
const RUN = 'r-b10b-red-green';
const NODE = process.execPath;
const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: SLUG });

const TEMP_PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b10b-')));
after(() => rmSync(TEMP_PARENT, { recursive: true, force: true }));

/** git with every inherited GIT_* stripped, no system/global config, a fixed identity. */
function git(/** @type {string[]} */ args, /** @type {string} */ cwd) {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) if (!key.startsWith('GIT_')) env[key] = value;
  Object.assign(env, {
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: os.devNull,
    GIT_AUTHOR_NAME: 'B10b Fixture',
    GIT_AUTHOR_EMAIL: 'b10b@example.test',
    GIT_COMMITTER_NAME: 'B10b Fixture',
    GIT_COMMITTER_EMAIL: 'b10b@example.test',
  });
  return execFileSync('git', args, { cwd, env, stdio: 'pipe', encoding: 'utf8' }).trimEnd();
}

const BASE_BIN = Buffer.from(Array.from({ length: 256 }, (_, i) => i));
const CURRENT_BIN = Buffer.from(Array.from({ length: 256 }, (_, i) => 255 - i));

/** @param {string} name @returns {{dir: string, baseSha: string}} */
function buildRepo(name) {
  const dir = path.join(TEMP_PARENT, name);
  mkdirSync(path.join(dir, 'assets'), { recursive: true });
  cpSync(path.join(FIXTURE, 'base'), dir, { recursive: true });
  writeFileSync(path.join(dir, '.gitignore'), '.code-forge/\n');
  writeFileSync(path.join(dir, 'assets', 'logo.bin'), BASE_BIN);
  git(['init', '-q'], dir);
  git(['add', '-A'], dir);
  git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], dir);
  const baseSha = git(['rev-parse', 'HEAD'], dir);
  cpSync(path.join(FIXTURE, 'current'), dir, { recursive: true });
  writeFileSync(path.join(dir, 'assets', 'logo.bin'), CURRENT_BIN);
  return { dir, baseSha };
}

const hashOf = (/** @type {string} */ file) => createHash('sha256').update(readFileSync(file)).digest('hex');

/** @param {string[]} args */
async function cli(args) {
  let stdout = '';
  let stderr = '';
  const code = await runProof(args, { stdout: { write: (s) => (stdout += s) }, stderr: { write: (s) => (stderr += s) } });
  return { code, stdout, stderr };
}

/** @type {{dir: string, baseSha: string}} */
let repo;

before(async () => {
  repo = buildRepo('ws');
  await startRun({ workspace: repo.dir, project: SLUG, runId: RUN, writeRow });
});

test('red→green prints RED then GREEN for a real new case, measured in the block export', async () => {
  const exp = await buildExport({
    cwd: repo.dir,
    blockId: 'A',
    baseSha: repo.baseSha,
    owned: ['src/math.mjs', 'test/clamp.spec.mjs'],
    ownedKinds: {},
    linkDirs: [],
    copyUntracked: [],
  });
  const printed = [];
  const result = await runRedGreen({
    workDir: exp.dir,
    repoDir: repo.dir,
    base: repo.baseSha,
    argv: [NODE, '--test', 'test/clamp.spec.mjs'],
    test: { file: 'test/clamp.spec.mjs' },
    sources: ['src/math.mjs'],
    isolation: 'export',
    print: (line) => printed.push(line),
    record: { runId: RUN, blockId: 'A', writeRow },
  });
  assert.deepEqual(printed, ['RED test/clamp.spec.mjs', 'GREEN test/clamp.spec.mjs']);
  // the export is back at the current content and the journal is gone; the main tree was never touched
  assert.deepEqual(
    [hashOf(path.join(exp.dir, 'src/math.mjs')), existsSync(journalPath(exp.dir)), result.proven],
    [hashOf(path.join(FIXTURE, 'current/src/math.mjs')), false, true],
  );
});

test('a fatal at the red step (the new module is absent at base) is RED_INVALID, never RED', async () => {
  const printed = [];
  const result = await runRedGreen({
    workDir: repo.dir,
    base: repo.baseSha,
    argv: [NODE, '--test', 'test/format.spec.mjs'],
    test: { file: 'test/format.spec.mjs' },
    sources: ['src/format.mjs'],
    isolation: 'lock',
    print: (line) => printed.push(line),
    record: { runId: RUN, blockId: 'B', writeRow },
  });
  assert.deepEqual([printed, result.red.verdict, result.red.red_kind, result.green], [['RED_INVALID test/format.spec.mjs (import)'], 'RED_INVALID', 'import', null]);
  assert.equal(hashOf(path.join(repo.dir, 'src/format.mjs')), hashOf(path.join(FIXTURE, 'current/src/format.mjs')));
});

test('a characterization test is proven by assertion deletion: RED, then GREEN on the restored file', async () => {
  const before = hashOf(path.join(repo.dir, 'test/add.spec.mjs'));
  const printed = [];
  const result = await runRedGreen({
    workDir: repo.dir,
    base: repo.baseSha,
    argv: [NODE, '--test', 'test/add.spec.mjs'],
    test: { file: 'test/add.spec.mjs', characterization: true },
    isolation: 'lock',
    print: (line) => printed.push(line),
    record: { runId: RUN, blockId: 'B', writeRow },
  });
  assert.deepEqual([result.mechanism, result.red.verdict, printed], ['assertion-deletion', 'RED', ['RED test/add.spec.mjs', 'GREEN test/add.spec.mjs']]);
  assert.equal(hashOf(path.join(repo.dir, 'test/add.spec.mjs')), before);
});

test('the proof rows record mechanism and red_kind, signed with the run key', async () => {
  const key = await loadKey(RUN);
  const rows = (await readAllRows(SLUG)).filter((r) => r.event === 'proof' && r.step === 'red-green');
  assert.deepEqual(
    rows.map((r) => [r.block, r.isolation, r.test, r.mechanism, r.red_kind, r.red, r.green, verifyRow(r, key).ok]),
    [
      ['A', 'export', 'test/clamp.spec.mjs', 'revert', 'assertion', 'RED', 'GREEN', true],
      ['B', 'lock', 'test/format.spec.mjs', 'revert', 'import', 'RED_INVALID', null, true],
      ['B', 'lock', 'test/add.spec.mjs', 'assertion-deletion', 'assertion', 'RED', 'GREEN', true],
    ],
  );
});

test('proof restore without a journal refuses (exit 1) and changes nothing', async () => {
  const res = await cli(['restore', 'A', '--run', RUN]);
  assert.deepEqual([res.code, res.stdout, res.stderr], [1, '', 'proof restore: no red→green journal for block A — nothing to restore\n']);
});

test('assertion deletion is fail-closed: comments ignored, a trailing line comment accepted, two statements refused', () => {
  const js = (/** @type {string[]} */ lines) => Buffer.from(lines.join('\n'));
  const fixed = deleteAssertions(
    'test/x.spec.mjs',
    js([
      "import assert from 'node:assert/strict';",
      '/* assert.equal(1, 2); */',
      '/*',
      '  assert.equal(3, 4);',
      '*/',
      "test('assert names are not code', (t) => {",
      '  assert.ok(x); // note',
      '  t.assert.equal(y, 1);',
      '  expect(z).toBe(2);',
      '});',
    ]),
  );
  assert.equal(fixed.deleted, 3);
  assert.deepEqual(fixed.bytes.toString('utf8').split('\n').slice(1, 9), [
    '/* assert.equal(1, 2); */',
    '/*',
    '  assert.equal(3, 4);',
    '*/',
    "test('assert names are not code', (t) => {",
    "  assert.fail('code-forge: assertion deleted');",
    "  t.assert.fail('code-forge: assertion deleted');",
    "  expect('code-forge: assertion deleted').toBe('');",
  ]);
  assert.throws(() => deleteAssertions('test/x.spec.mjs', js(['  assert.ok(a); cleanup();'])), { code: 'unclassified-assertion' });
  assert.throws(() => deleteAssertions('test/x.spec.mjs', js(['  assert.equal(s, `a`);'])), { code: 'unclassified-assertion' });
  assert.throws(() => deleteAssertions('test/x.spec.mjs', js(['  await assert.rejects(p);'])), { code: 'unclassified-assertion' });
});

test('assertion deletion refuses an assertion that is not at statement position: arrow body, array element, call argument', () => {
  const src = (/** @type {string[]} */ lines) => Buffer.from(lines.join('\n'));
  // an expression body: the rewrite would leave `assert.fail(…);` followed by `)`
  assert.throws(() => deleteAssertions('test/x.spec.mjs', src(["test('x', () =>", '  assert.ok(y)', ');'])), { code: 'unclassified-assertion' });
  assert.throws(() => deleteAssertions('test/x.spec.mjs', src(['const checks = [', '  assert.ok(y)', '];'])), { code: 'unclassified-assertion' });
  assert.throws(() => deleteAssertions('test/x.spec.mjs', src(['run(', '  assert.ok(y)', ');'])), { code: 'unclassified-assertion' });
  assert.throws(() => deleteAssertions('tests/XTest.php', src(['<?php', "it('x', fn () =>", '  expect($y)->toBeTrue()', ');'])), { code: 'unclassified-assertion' });
  // a braced body stays accepted, with and without the `;`
  const braced = deleteAssertions('test/x.spec.mjs', src(["test('x', () => {", '  assert.ok(x);', '});']));
  const bare = deleteAssertions('test/x.spec.mjs', src(["test('x', () => {", '  assert.ok(x)', '})']));
  assert.deepEqual(
    [braced.deleted, braced.bytes.toString('utf8').split('\n')[1], bare.deleted, bare.bytes.toString('utf8').split('\n')[1]],
    [1, "  assert.fail('code-forge: assertion deleted');", 1, "  assert.fail('code-forge: assertion deleted');"],
  );
});

test('a characterization test whose assertion is an arrow body is refused before the tree is touched', async () => {
  const rel = 'test/arrow.spec.mjs';
  const abs = path.join(repo.dir, rel);
  const source = ["import assert from 'node:assert/strict';", "import { test } from 'node:test';", "import { add } from '../src/math.mjs';", '', "test('add', () =>", '  assert.equal(add(1, 1), 2)', ');', ''].join('\n');
  writeFileSync(abs, source);
  try {
    await assert.rejects(
      runRedGreen({ workDir: repo.dir, base: repo.baseSha, argv: [NODE, '--test', rel], test: { file: rel, characterization: true }, isolation: 'lock' }),
      { code: 'unclassified-assertion' },
    );
    assert.deepEqual([readFileSync(abs, 'utf8'), existsSync(journalPath(repo.dir))], [source, false]);
  } finally {
    rmSync(abs, { force: true });
  }
});

test('an ordinary error in the red step (the command does not exist) still restores the tree and drops the journal', async () => {
  const files = ['assets/logo.bin', 'src/math.mjs'];
  const original = files.map((f) => hashOf(path.join(repo.dir, f)));
  const result = await runRedGreen({
    workDir: repo.dir,
    base: repo.baseSha,
    argv: [path.join(TEMP_PARENT, 'no-such-command')],
    test: { file: 'test/clamp.spec.mjs' },
    sources: files,
    isolation: 'lock',
  });
  assert.deepEqual(
    [result.red.verdict, files.map((f) => hashOf(path.join(repo.dir, f))), existsSync(journalPath(repo.dir))],
    ['RED_INVALID', original, false],
  );
});

test('SIGTERM mid-run leaves the journal; a second run refuses; proof restore brings back the exact hashes', async () => {
  const files = ['assets/logo.bin', 'src/math.mjs']; // journal order = sorted
  const original = files.map((f) => hashOf(path.join(repo.dir, f)));
  const marker = path.join(TEMP_PARENT, 'slow.pid');
  const driver = path.join(TEMP_PARENT, 'driver.mjs');
  writeFileSync(
    driver,
    [
      `import { runRedGreen } from ${JSON.stringify(RED_GREEN_URL)};`,
      'await runRedGreen({',
      `  workDir: ${JSON.stringify(repo.dir)}, base: ${JSON.stringify(repo.baseSha)},`,
      `  argv: [${JSON.stringify(NODE)}, 'tools/slow.mjs', ${JSON.stringify(marker)}],`,
      `  test: { file: 'test/clamp.spec.mjs' }, sources: ${JSON.stringify(files)}, isolation: 'lock',`,
      '});',
      '',
    ].join('\n'),
  );
  const { NODE_TEST_CONTEXT, ...env } = process.env;
  const child = spawn(NODE, [driver], { cwd: TEMP_PARENT, env, stdio: 'ignore' });
  const exited = new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })));
  /** @returns {number} the pid in the marker once it holds a whole positive integer, else 0 */
  const markerPid = () => {
    try {
      const text = readFileSync(marker, 'utf8');
      return /^[1-9]\d*$/.test(text) ? Number(text) : 0;
    } catch {
      return 0;
    }
  };
  let slowPid = 0;
  try {
    for (let i = 0; i < 200 && slowPid === 0; i += 1) {
      slowPid = markerPid();
      if (slowPid === 0) await new Promise((r) => setTimeout(r, 50));
    }
    assert.ok(slowPid > 0, 'the test child recorded its pid');
    // mid-run: the tree holds the base versions (binary included)
    assert.deepEqual(
      [hashOf(path.join(repo.dir, 'assets/logo.bin')), readFileSync(path.join(repo.dir, 'src/math.mjs'), 'utf8')],
      [createHash('sha256').update(BASE_BIN).digest('hex'), readFileSync(path.join(FIXTURE, 'base/src/math.mjs'), 'utf8')],
    );
    child.kill('SIGTERM');
    assert.deepEqual(await exited, { code: null, signal: 'SIGTERM' });
    assert.equal(existsSync(journalPath(repo.dir)), true);
    // the runner forwarded the signal to the test child's group: it is gone too
    const alive = () => {
      try {
        process.kill(slowPid, 0);
        return true;
      } catch {
        return false;
      }
    };
    for (let i = 0; i < 40 && alive(); i += 1) await new Promise((r) => setTimeout(r, 50));
    assert.equal(alive(), false);

    await assert.rejects(
      runRedGreen({ workDir: repo.dir, base: repo.baseSha, argv: [NODE, '--version'], test: { file: 'test/clamp.spec.mjs' }, sources: files, isolation: 'lock' }),
      { code: 'journal-present' },
    );

    const restored = await cli(['restore', 'A', '--run', RUN]);
    assert.deepEqual([restored.code, JSON.parse(restored.stdout)], [0, { restored: [{ dir: repo.dir, files }] }]);
    assert.deepEqual([files.map((f) => hashOf(path.join(repo.dir, f))), existsSync(journalPath(repo.dir))], [original, false]);
  } finally {
    // The driver first, then the test child's whole group (exec spawns it as a group leader). The
    // marker is re-read here so a pid written after the poll gave up is still killed.
    try {
      child.kill('SIGKILL');
    } catch {
      // already gone
    }
    const pid = slowPid || markerPid();
    for (const target of pid > 0 ? [-pid, pid] : []) {
      try {
        process.kill(target, 'SIGKILL');
      } catch {
        // already gone
      }
    }
  }
});

// ---- `proof red-green` (B17): the verb over runRedGreen, on a block opened in the run ----------

test('proof red-green <block> proves a new test by revert in a fresh export: RED then GREEN, exit 0, 1 signed row', async () => {
  await openBlock({ runId: RUN, id: 'C', level: 'L1', owned: ['src/math.mjs', 'test/clamp.spec.mjs'], acceptance: [{ clause: 'clamp caps at hi', tests: ['test/clamp.spec.mjs'] }], writeRow });
  const before = (await readAllRows(SLUG)).length;
  const res = await cli(['red-green', 'C', '--run', RUN, '--test', 'test/clamp.spec.mjs']);
  assert.equal(res.code, 0, res.stderr);
  assert.equal(res.stderr, 'RED test/clamp.spec.mjs\nGREEN test/clamp.spec.mjs\n');
  const printed = JSON.parse(res.stdout);
  assert.deepEqual(
    [printed.block, printed.test, printed.mechanism, printed.isolation, printed.red, printed.red_kind, printed.green, printed.proven, printed.export.dir],
    ['C', 'test/clamp.spec.mjs', 'revert', 'export', 'RED', 'assertion', 'GREEN', true, path.join(realpathSync(repo.dir), '.code-forge', 'export', 'C')],
  );
  const added = (await readAllRows(SLUG)).slice(before);
  assert.deepEqual(
    added.map((r) => [r.event, r.block, r.step, r.test ?? null, r.mechanism ?? null, r.red_kind ?? null]),
    [
      ['proof', 'C', 'export', null, null, null],
      ['proof', 'C', 'red-green', 'test/clamp.spec.mjs', 'revert', 'assertion'],
    ],
  );
  assert.equal(verifyRow(added[1], await loadKey(RUN)).ok, true);
  assert.deepEqual(added[1].covers, ['src/math.mjs']); // B20: revert covers the sources it put back
  // the main tree was never touched: math.mjs is still the current (clamp implemented) version
  assert.equal(hashOf(path.join(repo.dir, 'src/math.mjs')), hashOf(path.join(FIXTURE, 'current/src/math.mjs')));
});

test('proof red-green --mechanism assertion-deletion with a ::case label proves a characterization test', async () => {
  const mainTest = path.join(repo.dir, 'test/add.spec.mjs');
  const hashBefore = hashOf(mainTest);
  const before = (await readAllRows(SLUG)).length;
  const res = await cli(['red-green', 'C', '--run', RUN, '--test', 'test/add.spec.mjs::add pins existing behaviour', '--mechanism', 'assertion-deletion']);
  assert.equal(res.code, 0, res.stderr);
  assert.equal(res.stderr, 'RED test/add.spec.mjs::add pins existing behaviour\nGREEN test/add.spec.mjs::add pins existing behaviour\n');
  const printed = JSON.parse(res.stdout);
  assert.deepEqual([printed.mechanism, printed.red_kind, printed.proven], ['assertion-deletion', 'assertion', true]);
  const added = (await readAllRows(SLUG)).slice(before);
  assert.deepEqual(
    added.map((r) => [r.event, r.block, r.isolation, r.step, r.test ?? null, r.mechanism ?? null, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null]),
    [
      ['proof', 'C', 'export', 'export', null, null, null, null, null, null],
      ['proof', 'C', 'export', 'red-green', 'test/add.spec.mjs::add pins existing behaviour', 'assertion-deletion', 'RED', 'assertion', 'GREEN', true],
    ],
  );
  assert.equal(verifyRow(added[1], await loadKey(RUN)).ok, true);
  assert.deepEqual(added[1].covers, ['test/add.spec.mjs']); // B20: assertion deletion covers the test only
  // measured in the export: the main tree's test file is byte-identical before and after
  assert.equal(hashOf(mainTest), hashBefore);
});

test('proof red-green refuses: no --test (2), a bad mechanism (2), a block not open (1), a non-node test file (1)', async () => {
  const cases = [
    [['red-green', 'C', '--run', RUN], 2, 'proof red-green: proof red-green needs one block id, --run and --test <file[::case]>\n'],
    [['red-green', 'C', '--run', RUN, '--test', 'test/clamp.spec.mjs', '--mechanism', 'mutate'], 2, 'proof red-green: --mechanism must be one of revert, assertion-deletion\n'],
    [['red-green', 'Z', '--run', RUN, '--test', 'test/clamp.spec.mjs'], 1, `proof red-green: block Z is not open in run ${RUN}\n`],
    [['red-green', 'C', '--run', RUN, '--test', 'tests/ClampTest.php'], 1, 'proof red-green: only node:test files (.mjs, .cjs, .js) have a filtered test command in this version\n'],
  ];
  const before = (await readAllRows(SLUG)).length;
  for (const [args, code, stderr] of cases) {
    const res = await cli(/** @type {string[]} */ (args));
    assert.deepEqual([res.code, res.stdout, res.stderr], [code, '', stderr]);
  }
  assert.equal((await readAllRows(SLUG)).length, before);
});

test('proof red-green normalizes --test (./ dropped) and refuses an absolute path, a .. escape and a missing file (exit 2, 0 rows)', async () => {
  const before = (await readAllRows(SLUG)).length;
  const cases = [
    [path.join(repo.dir, 'test/clamp.spec.mjs'), 'proof red-green: --test must be a repo-relative path\n'],
    ['test/../../outside.spec.mjs', 'proof red-green: --test must stay inside the repository (no .. escape)\n'],
    ['test/missing.spec.mjs', `proof red-green: --test test/missing.spec.mjs is not a file in ${repo.dir}\n`],
  ];
  for (const [test_, stderr] of cases) {
    const res = await cli(['red-green', 'C', '--run', RUN, '--test', test_]);
    assert.deepEqual([res.code, res.stdout, res.stderr], [2, '', stderr]);
  }
  assert.equal((await readAllRows(SLUG)).length, before);
  const ok = await cli(['red-green', 'C', '--run', RUN, '--test', './test/./clamp.spec.mjs']);
  assert.equal(ok.code, 0, ok.stderr);
  assert.deepEqual([JSON.parse(ok.stdout).test, ok.stderr], ['test/clamp.spec.mjs', 'RED test/clamp.spec.mjs\nGREEN test/clamp.spec.mjs\n']);
});

test('fix 2: a red→green result with no red phase never throws in the row builder — exit 1, exactly 1 signed row with proven: false and red_kind: null', async () => {
  if (typeof mock.module !== 'function') throw new Error('run with --experimental-test-module-mocks (npm test does)');
  const handle = mock.module(RED_GREEN_URL, {
    namedExports: { MECHANISMS, readJournal, restoreJournal, runRedGreen: async () => ({ label: 'test/clamp.spec.mjs', mechanism: 'revert', green: null, proven: false }) },
  });
  try {
    // a fresh module instance (query string) so the mock is what THIS proof.mjs links against
    const fresh = '../../src/cli/proof.mjs?no-red';
    const { runProof: guarded } = /** @type {typeof import('../../src/cli/proof.mjs')} */ (await import(fresh));
    const before = (await readAllRows(SLUG)).length;
    let stdout = '';
    let stderr = '';
    const code = await guarded(['red-green', 'C', '--run', RUN, '--test', 'test/clamp.spec.mjs'], { stdout: { write: (s) => (stdout += s) }, stderr: { write: (s) => (stderr += s) } });
    assert.deepEqual([code, stderr], [1, '']);
    const printed = JSON.parse(stdout);
    assert.deepEqual([printed.red, printed.red_kind, printed.green, printed.proven], [null, null, null, false]);
    const added = (await readAllRows(SLUG)).slice(before);
    const rows = added.filter((r) => r.event === 'proof' && r.step === 'red-green');
    assert.deepEqual(added.map((r) => r.step), ['export', 'red-green']);
    assert.deepEqual(rows.map((r) => [r.block, r.test, r.mechanism, r.red, r.red_kind, r.green, r.proven, r.failed, r.covers]), [['C', 'test/clamp.spec.mjs', 'revert', null, null, null, false, null, ['src/math.mjs']]]);
    assert.equal(verifyRow(rows[0], await loadKey(RUN)).ok, true);
  } finally {
    handle.restore();
  }
});
