/**
 * B87: `proof.runners` — `proof red-green` on a test file a runner of the run snapshot matches,
 * read from the JUnit report the runner writes. The runner is a fake Node script (outside the
 * repo) that behaves like a PHP test runner: it fails on the base source and passes on the new
 * one, and writes a JUnit report at the `{report}` path. A mode file (also outside the repo)
 * picks how it misbehaves, so one frozen snapshot covers every case.
 *
 * The repo is script-built under ONE `mkdtemp` parent (helpers.mjs, removed in `after()`):
 *
 *   base commit:   src/clamp.php (BROKEN) · checks/Support.php (BASE) · nodesrc/clamp.mjs (BROKEN) · .code-forge.yml (runners)
 *   working tree:  src/clamp.php (FIXED) · checks/ClampCheck.php (new) · checks/Support.php (CURRENT)
 *                  nodesrc/clamp.mjs (FIXED) · nodetests/clamp.test.mjs (new, no runner matches it)
 *
 * `checks/**` is no test path by name; only the runner's `match` makes it a test, so a reverted
 * `checks/Support.php` would show (the fake runner then reports an `<error>`).
 */

import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { before, test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { loadConfigFile } from '../../src/config/load.mjs';
import { validateConfig } from '../../src/config/validate.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { journalPath, runRedGreen } from '../../src/proof/red-green.mjs';
import { expandRunnerArgv, runnerCovers, selectRunner } from '../../src/proof/runners.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { initialConfigState, proofRunners } from '../../src/state/config-snapshot.mjs';
import { StateError } from '../../src/state/paths.mjs';
import { readRun, reloadRun, startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { TEMP_PARENT, git } from './helpers.mjs';

const SLUG = 'runners-proof';
const RUN = 'r-b87-runners';
const BARE_RUN = 'r-b87-no-snapshot';
const NODE = process.execPath;
const TEST = 'checks/ClampCheck.php';
const SWIFT_TEST = 'swiftchecks/ClampSwiftCheck.php';
const NODE_TEST = 'nodetests/clamp.test.mjs';
const TOKEN = 'FAKE-runner-token-value-0123456789';
const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: SLUG });
/** Every key of a signed red-green row from a runner, sorted. */
const ROW_KEYS = ['block', 'cost_source', 'covers', 'env_keys', 'event', 'failed', 'failed_tests', 'green', 'isolation', 'mac', 'mechanism', 'proven', 'red', 'red_kind', 'run', 'runner', 'step', 'test', 'tokens_source', 'ts'];

const TOOLS = path.join(TEMP_PARENT, 'tools');
const FAKE = path.join(TOOLS, 'fake-runner.mjs');
const MODE = path.join(TOOLS, 'mode.txt');
const LOG = path.join(TOOLS, 'runner.log');
const REPO = path.join(TEMP_PARENT, 'ws');
const EXPORT_DIR = path.join(REPO, '.code-forge', 'export', 'A');

const CONTENT = Object.freeze({
  clampBase: "<?php // clamp BROKEN\n",
  clampNew: "<?php // clamp FIXED\n",
  supportBase: "<?php // support BASE\n",
  supportNew: "<?php // support CURRENT\n",
  check: "<?php // ClampCheck: caps at hi\n",
  nodeBase: 'export const clamp = (v) => v; // BROKEN\n',
  nodeNew: 'export const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);\n',
  nodeTest: "import assert from 'node:assert/strict';\nimport { test } from 'node:test';\nimport { clamp } from '../nodesrc/clamp.mjs';\ntest('caps at hi', () => assert.equal(clamp(12, 0, 10), 10));\n",
});

/** A Linux XCTest assertion line, as `swift test` prints it on the console. */
const XCTEST_LINE = '/x/ClampCheck.swift:3: error: ClampCheck.caps : XCTAssertEqual failed: ("12") is not equal to ("10")\n';

/**
 * The fake runner: argv `<mode file> <log> <test file> <report>`. It logs one JSON line per call
 * (never an env value), then writes the report the mode asks for.
 */
const FAKE_SOURCE = `
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs';
const [modeFile, logFile, testFile, report] = process.argv.slice(2);
const mode = readFileSync(modeFile, 'utf8').trim();
const fixed = readFileSync('src/clamp.php', 'utf8').includes('FIXED');
const support = readFileSync('checks/Support.php', 'utf8').includes('CURRENT');
appendFileSync(logFile, JSON.stringify({
  cwd: process.cwd(), report, test: testFile, test_exists: existsSync(testFile), fixed,
  token_ok: process.env.FAKE_TOKEN === ${JSON.stringify(TOKEN)},
  git_keys: Object.keys(process.env).filter((k) => k.startsWith('GIT_')).sort(),
}) + '\\n');
const suite = (cases, n) => '<?xml version="1.0" encoding="UTF-8"?>\\n<testsuites><testsuite name="ClampCheck" tests="' + n + '">' + cases + '</testsuite></testsuites>\\n';
const pass = (name) => '<testcase classname="ClampCheck" name="' + name + '"></testcase>';
const fail = '<testcase classname="ClampCheck" name="caps"><failure type="PHPUnit\\\\Framework\\\\ExpectationFailedException" message="Failed asserting that 12 is identical to 10."></failure></testcase>';
const error = '<testcase classname="ClampCheck" name="caps"><error type="Error" message="Call to undefined function"></error></testcase>';
if (!support || (mode === 'error' && !fixed)) { writeFileSync(report, suite(error, 1)); process.exit(1); }
if (mode === 'noreport' && !fixed) process.exit(1);
if (mode === 'notest' && !fixed) { writeFileSync(report, suite('', 0)); process.exit(1); }
const bare = '<testcase classname="ClampCheck" name="caps"><failure message="failed"></failure></testcase>';
if ((mode === 'xctest' || mode === 'barenoline') && !fixed) {
  if (mode === 'xctest') process.stderr.write(${JSON.stringify(XCTEST_LINE)});
  writeFileSync(report, suite(bare, 1));
  process.exit(1);
}
if (!fixed) { writeFileSync(report, suite(fail, 1)); process.exit(1); }
if (mode === 'greenmissing') { writeFileSync(report, suite(pass('other'), 1)); process.exit(0); }
writeFileSync(report, suite(pass('caps'), 1));
process.exit(0);
`;

const RUNNERS = [
  { match: ['checks/**/*.php'], adapter: 'junit', argv: [NODE, FAKE, MODE, LOG, '{test_file}', '{report}'], env: { FAKE_TOKEN: TOKEN }, timeout_s: 60 },
  { match: ['filtered/**/*.php'], adapter: 'junit', argv: [NODE, FAKE, MODE, LOG, '{test_filter}', '{report}', '{test_file}'] },
  { match: ['swiftchecks/**/*.php'], adapter: 'swift', argv: [NODE, FAKE, MODE, LOG, '{test_file}', '{report}'] },
  { match: ['*.php'], adapter: 'junit', argv: [NODE, FAKE, MODE, LOG, '{test_file}', '{report}'] },
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
/** @returns {Record<string, any>[]} the fake runner's calls so far */
const logLines = () => (existsSync(LOG) ? readFileSync(LOG, 'utf8').trim().split('\n').filter(Boolean).map((l) => JSON.parse(l)) : []);
const treeHashes = () => ['src/clamp.php', 'checks/Support.php', TEST, '.code-forge.yml'].map((rel) => hashOf(path.join(REPO, rel)));

/** @param {string[]} args @param {Parameters<typeof runProof>[1]} [deps] */
async function cli(args, deps = {}) {
  let stdout = '';
  let stderr = '';
  const code = await runProof(args, { ...deps, stdout: { write: (s) => (stdout += s) }, stderr: { write: (s) => (stderr += s) } });
  return { code, stdout, stderr };
}
const proveArgs = (/** @type {string} */ testArg) => ['red-green', 'A', '--run', RUN, '--test', testArg];

before(async () => {
  mkdirSync(TOOLS);
  writeFileSync(FAKE, FAKE_SOURCE);
  writeFileSync(MODE, 'normal\n');
  mkdirSync(REPO);
  write('.gitignore', '.code-forge/\n');
  write('.code-forge.yml', configYaml(RUNNERS));
  write('src/clamp.php', CONTENT.clampBase);
  write('checks/Support.php', CONTENT.supportBase);
  write('nodesrc/clamp.mjs', CONTENT.nodeBase);
  git(['init', '-q'], REPO);
  git(['symbolic-ref', 'HEAD', 'refs/heads/main'], REPO);
  git(['add', '-A'], REPO);
  git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], REPO);
  write('src/clamp.php', CONTENT.clampNew);
  write('checks/Support.php', CONTENT.supportNew);
  write(TEST, CONTENT.check);
  write(SWIFT_TEST, CONTENT.check);
  write('nodesrc/clamp.mjs', CONTENT.nodeNew);
  write(NODE_TEST, CONTENT.nodeTest);
  const loaded = await loadConfigFile(path.join(REPO, '.code-forge.yml'));
  assert.equal(loaded.ok, true);
  assert.equal(validateConfig(loaded.config).valid, true);
  await startRun({ workspace: REPO, project: SLUG, runId: RUN, writeRow, config: /** @type {Record<string, any>} */ (loaded.config) });
  await openBlock({ runId: RUN, id: 'A', level: 'L1', owned: ['src/clamp.php', 'checks/**', 'swiftchecks/**'], acceptance: [{ clause: 'clamp caps at hi', tests: [TEST] }], writeRow });
});

test('(1)(7)(8)(11) a junit runner proves a .php check: RED assertion, GREEN, exit 0, one signed row with runner, failed_tests and covers', async () => {
  writeFileSync(MODE, 'normal\n');
  const calls = logLines().length;
  const rowsBefore = (await readAllRows(SLUG)).length;
  const res = await cli(proveArgs(TEST));
  assert.equal(res.code, 0, res.stderr);
  assert.equal(res.stderr, `RED ${TEST}\nGREEN ${TEST}\n`);
  const printed = JSON.parse(res.stdout);
  assert.deepEqual(
    [printed.test, printed.mechanism, printed.isolation, printed.red, printed.red_kind, printed.green, printed.proven, printed.runner, printed.failed_tests, printed.env_keys],
    [TEST, 'revert', 'export', 'RED', 'assertion', 'GREEN', true, 'junit', ['ClampCheck.caps'], ['FAKE_TOKEN']],
  );
  const added = (await readAllRows(SLUG)).slice(rowsBefore);
  assert.deepEqual(added.map((r) => [r.event, r.block, r.step]), [['proof', 'A', 'export'], ['proof', 'A', 'red-green']]);
  const row = added[1];
  assert.deepEqual(
    [row.test, row.mechanism, row.red, row.red_kind, row.green, row.proven, row.failed, row.runner, row.failed_tests, row.covers, row.env_keys],
    [TEST, 'revert', 'RED', 'assertion', 'GREEN', true, 1, 'junit', ['ClampCheck.caps'], ['src/clamp.php'], ['FAKE_TOKEN']],
  );
  // the row's full key set: an extra field (a `failed_ids`, an env value) fails here
  assert.deepEqual(Object.keys(row).sort(), ROW_KEYS);
  assert.equal(verifyRow(row, await loadKey(RUN)).ok, true);
  // (7) checks/Support.php is changed and owned, but the runner's match covers it: never reverted
  // (the fake runner saw CURRENT at RED, else it would have written an <error>)
  const steps = logLines().slice(calls);
  assert.deepEqual(steps.map((s) => [s.cwd, s.test, s.test_exists, s.fixed, s.token_ok]), [
    [EXPORT_DIR, TEST, true, false, true],
    [EXPORT_DIR, TEST, true, true, true],
  ]);
  // (8) a fresh report per step, outside the work dir and the repo, gone after the step
  assert.equal(new Set(steps.map((s) => s.report)).size, 2);
  for (const { report } of steps) {
    assert.equal(path.basename(report), 'report.xml');
    assert.equal(report.startsWith(`${REPO}${path.sep}`), false);
    assert.equal(existsSync(path.dirname(report)), false);
  }
  // (11) the env value is never printed nor written to the ledger
  assert.equal(`${res.stdout}${res.stderr}${JSON.stringify(added)}`.includes(TOKEN), false);
  // the main tree was never touched
  assert.equal(readFileSync(path.join(REPO, 'src/clamp.php'), 'utf8'), CONTENT.clampNew);
});

test('(2) no report, a report with an <error>, a report with no test ⇒ RED_INVALID error, exit 1, proven false, the export restored byte for byte', async () => {
  for (const mode of ['noreport', 'error', 'notest']) {
    writeFileSync(MODE, `${mode}\n`);
    const rowsBefore = (await readAllRows(SLUG)).length;
    const res = await cli(proveArgs(TEST));
    assert.deepEqual([mode, res.code, res.stderr], [mode, 1, `RED_INVALID ${TEST} (error)\n`]);
    const printed = JSON.parse(res.stdout);
    assert.deepEqual([mode, printed.red, printed.red_kind, printed.green, printed.proven, printed.runner], [mode, 'RED_INVALID', 'error', null, false, 'junit']);
    const added = (await readAllRows(SLUG)).slice(rowsBefore);
    assert.deepEqual(added.map((r) => [r.step, r.proven ?? null, r.runner ?? null]), [['export', null, null], ['red-green', false, 'junit']]);
    assert.deepEqual(added[1].failed_tests, mode === 'error' ? ['ClampCheck.caps'] : []);
    assert.deepEqual(
      [mode, hashOf(path.join(EXPORT_DIR, 'src/clamp.php')), hashOf(path.join(EXPORT_DIR, 'checks/Support.php')), existsSync(journalPath(EXPORT_DIR))],
      [mode, hashOf(path.join(REPO, 'src/clamp.php')), hashOf(path.join(REPO, 'checks/Support.php')), false],
    );
  }
});

test('(3) GREEN: the test that failed at RED is missing from the report ⇒ NOT_GREEN, exit 1', async () => {
  writeFileSync(MODE, 'greenmissing\n');
  const rowsBefore = (await readAllRows(SLUG)).length;
  const res = await cli(proveArgs(TEST));
  assert.deepEqual([res.code, res.stderr], [1, `RED ${TEST}\nNOT_GREEN ${TEST}\n`]);
  const printed = JSON.parse(res.stdout);
  assert.deepEqual([printed.red, printed.green, printed.proven], ['RED', 'NOT_GREEN', false]);
  const added = (await readAllRows(SLUG)).slice(rowsBefore);
  assert.deepEqual(
    added.map((r) => [r.step, r.red ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed_tests ?? null]),
    [['export', null, null, null, null, null], ['red-green', 'RED', 'NOT_GREEN', false, 'junit', ['ClampCheck.caps']]],
  );
});

test('(4) the runner comes from the snapshot: a live argv edit changes nothing, run reload refuses it, block open refuses an owned .code-forge.yml', async () => {
  writeFileSync(MODE, 'normal\n');
  const edited = [{ ...RUNNERS[0], argv: [NODE, path.join(TOOLS, 'LIVE-missing.mjs'), '{test_file}', '{report}'] }, ...RUNNERS.slice(1)];
  write('.code-forge.yml', configYaml(edited));
  try {
    const calls = logLines().length;
    const res = await cli(proveArgs(TEST));
    assert.deepEqual([res.code, res.stderr], [0, `RED ${TEST}\nGREEN ${TEST}\n`]);
    assert.equal(logLines().length - calls, 2); // the snapshot's fake runner ran both steps
    const live = await loadConfigFile(path.join(REPO, '.code-forge.yml'));
    const rowsBefore = (await readAllRows(SLUG)).length;
    await assert.rejects(reloadRun({ runId: RUN, config: /** @type {Record<string, any>} */ (live.config), writeRow }), {
      code: 'immutable-key',
      message: 'proof.runners cannot change mid-run (the commands that prove a block are fixed by the owner before the run starts) — put it back, or end the run and start a new one',
    });
    await assert.rejects(openBlock({ runId: RUN, id: 'B', level: 'L1', owned: ['.code-forge.yml'], acceptance: [{ clause: 'x', tests: [TEST] }], writeRow }), {
      code: 'config-owned',
      message: 'owned entry ".code-forge.yml" covers the project\'s .code-forge.yml; no block may own it while proof.runners is set',
    });
    assert.equal((await readAllRows(SLUG)).length, rowsBefore);
  } finally {
    write('.code-forge.yml', configYaml(RUNNERS));
  }
});

test('(4) a run started without a snapshot cannot gain proof.runners at its first reload', async () => {
  await startRun({ workspace: REPO, project: SLUG, runId: BARE_RUN, writeRow });
  await assert.rejects(reloadRun({ runId: BARE_RUN, config: { proof: { runners: RUNNERS } }, writeRow }), {
    code: 'immutable-key',
    message: 'proof.runners cannot change mid-run (the commands that prove a block are fixed by the owner before the run starts) — put it back, or end the run and start a new one',
  });
});

test('(5) validate refuses each bad runner with a message that names the key path', () => {
  const cmd = 'vendor/bin/pest';
  /** @type {[string, Record<string, any>, string[]][]} */
  const cases = [
    ['unknown adapter', { match: ['tests/**'], adapter: 'pest', argv: [cmd, '{test_file}', '{report}'] }, ['proof.runners[0].adapter must be one of: junit, swift']],
    ['empty match', { match: [], adapter: 'junit', argv: [cmd, '{test_file}', '{report}'] }, ['proof.runners[0].match must be a non-empty list of globs']],
    [
      '--log-junit={report}',
      { match: ['tests/**'], adapter: 'junit', argv: [cmd, '--log-junit={report}', '{test_file}'] },
      ['proof.runners[0].argv[1]: a token must be a whole argv item (give it as its own item)', 'proof.runners[0].argv needs a {report} item (the junit adapter reads the report code-forge places there)'],
    ],
    [
      'a token as argv[0]',
      { match: ['tests/**'], adapter: 'junit', argv: ['{test_file}', '--log-junit', '{report}'] },
      ['proof.runners[0].argv[0]: a token cannot be the command', 'proof.runners[0].argv needs a test token ({test_file}, {test_id}, {test_filter}): a proof must name its test'],
    ],
    ['no {report}', { match: ['tests/**'], adapter: 'junit', argv: [cmd, '{test_file}'] }, ['proof.runners[0].argv needs a {report} item (the junit adapter reads the report code-forge places there)']],
    [
      'no test token',
      { match: ['tests/**'], adapter: 'junit', argv: [cmd, '--log-junit', '{report}'] },
      ['proof.runners[0].argv needs a test token ({test_file}, {test_id}, {test_filter}): a proof must name its test'],
    ],
    [
      'a token of another adapter',
      { match: ['tests/**'], adapter: 'junit', argv: ['xcodebuild', 'test', '{scheme}', '{report}', '{test_file}'] },
      ['proof.runners[0].argv[2] is not a token of the junit adapter ({test_file}, {test_id}, {test_filter}, {report})'],
    ],
    ['timeout_s 59', { match: ['tests/**'], adapter: 'junit', argv: [cmd, '{test_file}', '{report}'], timeout_s: 59 }, ['proof.runners[0].timeout_s must be a whole number of seconds from 60 to 7200']],
    ['timeout_s 7201', { match: ['tests/**'], adapter: 'junit', argv: [cmd, '{test_file}', '{report}'], timeout_s: 7201 }, ['proof.runners[0].timeout_s must be a whole number of seconds from 60 to 7200']],
    ['argv as one shell string', { match: ['tests/**'], adapter: 'junit', argv: `${cmd} {test_file} {report}` }, ['proof.runners[0].argv must be a non-empty argv array of strings']],
    ['no match', { adapter: 'junit', argv: [cmd, '{test_file}', '{report}'] }, ['proof.runners[0].match must be a non-empty list of globs']],
    ['no argv', { match: ['tests/**'], adapter: 'junit' }, ['proof.runners[0].argv must be a non-empty argv array of strings']],
    ['empty argv', { match: ['tests/**'], adapter: 'junit', argv: [] }, ['proof.runners[0].argv must be a non-empty argv array of strings']],
    ['a non-string argv item', { match: ['tests/**'], adapter: 'junit', argv: [cmd, 7, '{test_file}', '{report}'] }, ['proof.runners[0].argv must be a non-empty argv array of strings']],
    ['a NUL in an argv item', { match: ['tests/**'], adapter: 'junit', argv: [cmd, '--x=FAKE\0y', '{test_file}', '{report}'] }, ['proof.runners[0].argv[1] contains a NUL character']],
    ['a NUL in an env value', { match: ['tests/**'], adapter: 'junit', argv: [cmd, '{test_file}', '{report}'], env: { FAKE_KEY: 'FAKE\0value' } }, ['proof.runners[0].env.FAKE_KEY contains a NUL character']],
    ['a key typo (timeout)', { match: ['tests/**'], adapter: 'junit', argv: [cmd, '{test_file}', '{report}'], timeout: 60 }, ['proof.runners[0]: "timeout" is not a runner key (match, adapter, argv, env, timeout_s)']],
    [
      'a bad adapter still refuses a token no adapter knows',
      { match: ['tests/**'], adapter: 'pest', argv: [cmd, '{tset_file}', '{test_file}', '{report}'] },
      ['proof.runners[0].adapter must be one of: junit, swift', 'proof.runners[0].argv[1] is not a token of any adapter ({test_file}, {test_id}, {test_filter}, {report})'],
    ],
    [
      'a bad adapter does not hide the argv errors',
      { match: ['tests/**'], adapter: 'pest', argv: [cmd, '--log={report}'] },
      [
        'proof.runners[0].adapter must be one of: junit, swift',
        'proof.runners[0].argv[1]: a token must be a whole argv item (give it as its own item)',
        'proof.runners[0].argv needs a {report} item (every adapter reads the report code-forge places there)',
        'proof.runners[0].argv needs a test token ({test_file}, {test_id}, {test_filter}): a proof must name its test',
      ],
    ],
  ];
  for (const [name, entry, messages] of cases) {
    const result = validateConfig({ ...BASE_CONFIG, proof: { runners: [entry] } });
    const ours = result.errors.filter((e) => e.rule === 'proof-runners-invalid').map((e) => e.message);
    assert.deepEqual([name, result.valid, ours], [name, false, messages]);
    // runner input trips no other domain rule (the schema layer may restate it)
    assert.deepEqual([name, result.errors.filter((e) => e.rule !== 'proof-runners-invalid' && e.rule !== 'schema')], [name, []]);
  }
  const empty = validateConfig({ ...BASE_CONFIG, proof: { runners: [] } });
  assert.deepEqual(
    [empty.valid, empty.errors.filter((e) => e.rule === 'proof-runners-invalid').map((e) => e.message)],
    [false, ['proof.runners must list at least one runner (leave the key out for none)']],
  );
  /** @type {[string, Record<string, any>][]} */
  const valid = [
    ['junit, no timeout', { match: ['tests/**/*Test.php'], adapter: 'junit', argv: [cmd, '--log-junit', '{report}', '{test_file}'], env: { FAKE_MODE: 'FAKE-ci' } }],
    ['junit, timeout_s 60', { match: ['tests/**/*Test.php'], adapter: 'junit', argv: [cmd, '--log-junit', '{report}', '{test_file}'], timeout_s: 60 }],
    ['swift, timeout_s 7200', { match: ['tests/**/*Test.php'], adapter: 'swift', argv: [cmd, '--log-junit', '{report}', '{test_id}'], timeout_s: 7200 }],
  ];
  for (const [name, entry] of valid) {
    const ok = validateConfig({ ...BASE_CONFIG, proof: { runners: [entry] } });
    assert.deepEqual([name, ok.valid, ok.errors], [name, true, []]);
  }
});

test('(6) {test_filter} with no ::case is a usage error (exit 2, no row); a test path starting with "-" is refused', async () => {
  const rowsBefore = (await readAllRows(SLUG)).length;
  const calls = logLines().length;
  const filtered = await cli(proveArgs('filtered/FCheck.php'));
  assert.deepEqual([filtered.code, filtered.stdout, filtered.stderr], [2, '', 'proof red-green: the junit runner for filtered/FCheck.php filters by test case — give --test <file>::<case>\n']);
  const dashed = await cli(proveArgs('./-x.php'));
  assert.deepEqual([dashed.code, dashed.stdout, dashed.stderr], [2, '', 'proof red-green: the test path "-x.php" starts with "-" (it would be read as an option)\n']);
  const dashedCase = await cli(proveArgs('filtered/FCheck.php::-x'));
  assert.deepEqual([dashedCase.code, dashedCase.stdout, dashedCase.stderr], [2, '', 'proof red-green: the test case starts with "-" (it would be read as an option)\n']);
  assert.deepEqual([(await readAllRows(SLUG)).length - rowsBefore, logLines().length - calls], [0, 0]);
});

test('(9) an adapter precheck that fails ⇒ exit 1, no row, no journal, tree unchanged', async () => {
  writeFileSync(MODE, 'normal\n');
  const rowsBefore = (await readAllRows(SLUG)).length;
  const calls = logLines().length;
  const hashes = treeHashes();
  const exportHashes = ['src/clamp.php', 'checks/Support.php'].map((rel) => hashOf(path.join(EXPORT_DIR, rel)));
  /** @type {string[]} */
  const seen = [];
  const res = await cli(proveArgs(TEST), {
    precheck: async (entry) => {
      seen.push(entry.adapter);
      throw new StateError('precheck-failed', 'the FAKE precheck refused');
    },
  });
  assert.deepEqual([res.code, res.stdout, res.stderr, seen], [1, '', 'proof red-green: the FAKE precheck refused\n', ['junit']]);
  assert.deepEqual(
    [(await readAllRows(SLUG)).length - rowsBefore, logLines().length - calls, existsSync(journalPath(EXPORT_DIR)), existsSync(journalPath(REPO)), treeHashes(), ['src/clamp.php', 'checks/Support.php'].map((rel) => hashOf(path.join(EXPORT_DIR, rel)))],
    [0, 0, false, false, hashes, exportHashes],
  );
});

test('(5) the runner child sees no inherited GIT_* variable (a GIT_DIR in the parent never reaches it)', async () => {
  writeFileSync(MODE, 'normal\n');
  const calls = logLines().length;
  const saved = process.env.GIT_DIR;
  process.env.GIT_DIR = path.join(TEMP_PARENT, 'FAKE-git-dir');
  let res;
  try {
    res = await cli(proveArgs(TEST));
  } finally {
    if (saved === undefined) delete process.env.GIT_DIR;
    else process.env.GIT_DIR = saved;
  }
  assert.deepEqual([res.code, res.stderr], [0, `RED ${TEST}\nGREEN ${TEST}\n`]);
  assert.deepEqual(logLines().slice(calls).map((s) => [s.git_keys, s.token_ok]), [[[], true], [[], true]]);
});

test('(11) a bare XCTest <failure message="failed"> is RED assertion with its console line, RED_INVALID error without it', async () => {
  const shape = (/** @type {Record<string, any>[]} */ rows) => rows.map((r) => [r.step, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed_tests ?? null]);
  try {
    writeFileSync(MODE, 'xctest\n');
    let rowsBefore = (await readAllRows(SLUG)).length;
    const withLine = await cli(proveArgs(TEST));
    assert.deepEqual([withLine.code, withLine.stderr], [0, `RED ${TEST}\nGREEN ${TEST}\n`]);
    assert.deepEqual(shape((await readAllRows(SLUG)).slice(rowsBefore)), [
      ['export', null, null, null, null, null, null],
      ['red-green', 'RED', 'assertion', 'GREEN', true, 'junit', ['ClampCheck.caps']],
    ]);
    writeFileSync(MODE, 'barenoline\n');
    rowsBefore = (await readAllRows(SLUG)).length;
    const noLine = await cli(proveArgs(TEST));
    assert.deepEqual([noLine.code, noLine.stderr], [1, `RED_INVALID ${TEST} (error)\n`]);
    assert.deepEqual(shape((await readAllRows(SLUG)).slice(rowsBefore)), [
      ['export', null, null, null, null, null, null],
      ['red-green', 'RED_INVALID', 'error', null, false, 'junit', ['ClampCheck.caps']],
    ]);
  } finally {
    writeFileSync(MODE, 'normal\n');
  }
});

test('(6) an empty case after :: is a usage error (exit 2, no row, no runner call)', async () => {
  const rowsBefore = (await readAllRows(SLUG)).length;
  const calls = logLines().length;
  const res = await cli(proveArgs(`${TEST}::`));
  assert.deepEqual([res.code, res.stdout, res.stderr], [2, '', 'proof red-green: --test <file>::<case> needs a case name after ::\n']);
  assert.deepEqual([(await readAllRows(SLUG)).length - rowsBefore, logLines().length - calls], [0, 0]);
  for (const entry of [RUNNERS[0], RUNNERS[1]]) {
    assert.throws(() => expandRunnerArgv(/** @type {any} */ (entry), { file: TEST, testCase: '', report: path.join(TOOLS, 'r.xml') }), {
      code: 'usage',
      message: '--test <file>::<case> needs a case name after ::',
    });
  }
});

test('proofRunners: null without the key or a snapshot, runner-invalid for a non-list or an empty list, else a deep copy of the stored list', async () => {
  const record = await readRun(RUN);
  const first = /** @type {any[]} */ (proofRunners(record));
  assert.deepEqual(first, RUNNERS);
  first[0].argv.push('--FAKE-extra');
  first[0].match[0] = 'FAKE/**';
  first[0].env.FAKE_TOKEN = 'FAKE-changed';
  first.pop();
  assert.deepEqual(proofRunners(record), RUNNERS);
  assert.deepEqual(proofRunners(await readRun(RUN)), RUNNERS);

  assert.equal(proofRunners({ run_id: 'r-x', config: initialConfigState({ ...BASE_CONFIG }) }), null);
  assert.equal(proofRunners({ run_id: 'r-x' }), null);
  assert.equal(proofRunners(await readRun(BARE_RUN)), null);
  for (const [value, what] of /** @type {[unknown, string][]} */ ([[{ a: 1 }, 'is not a list'], ['FAKE', 'is not a list'], [[], 'is an empty list']])) {
    const bad = { run_id: 'r-x', config: initialConfigState({ ...BASE_CONFIG, proof: { runners: value } }) };
    assert.throws(() => proofRunners(bad), { code: 'runner-invalid', message: `run r-x: proof.runners in the run snapshot ${what}` });
  }
});

/** @returns {{dir: string, base: string}} a small repo for direct `runRedGreen` calls: base src/a.php BROKEN, working tree FIXED */
function directRepo() {
  const dir = path.join(TEMP_PARENT, 'direct');
  if (!existsSync(dir)) {
    mkdirSync(path.join(dir, 'src'), { recursive: true });
    mkdirSync(path.join(dir, 'checks'), { recursive: true });
    writeFileSync(path.join(dir, 'src/a.php'), '<?php // BROKEN\n');
    git(['init', '-q'], dir);
    git(['add', '-A'], dir);
    git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], dir);
    writeFileSync(path.join(dir, 'src/a.php'), '<?php // FIXED\n');
    writeFileSync(path.join(dir, 'checks/ACheck.php'), '<?php // check\n');
  }
  return { dir, base: git(['rev-parse', 'HEAD'], dir) };
}

/** @returns {import('../../src/proof/runners.mjs').RunnerHook} a hook whose RED is an assertion and whose GREEN is `green` */
const fakeHook = (/** @type {() => Promise<'GREEN' | 'NOT_GREEN'>} */ green) => ({
  adapter: 'junit',
  env_keys: [],
  red: async () => ({ verdict: 'RED', red_kind: 'assertion', framework: 'junit', failed: 1, failed_tests: ['ACheck.caps'], failed_ids: ['ACheck.caps'], reason: 'an assertion failed' }),
  green,
});

test('(8) runRedGreen refuses a call with both argv and a runner, or with neither (usage, nothing touched)', async () => {
  const { dir, base } = directRepo();
  const common = { workDir: dir, base, test: { file: 'checks/ACheck.php' }, sources: ['src/a.php'], isolation: /** @type {const} */ ('lock') };
  const message = 'red-green: give exactly one of argv and runner';
  await assert.rejects(runRedGreen({ ...common, argv: [NODE, '-e', ''], runner: fakeHook(async () => 'GREEN') }), { code: 'usage', message });
  await assert.rejects(runRedGreen({ ...common }), { code: 'usage', message });
  assert.deepEqual([readFileSync(path.join(dir, 'src/a.php'), 'utf8'), existsSync(journalPath(dir))], ['<?php // FIXED\n', false]);
});

test('(9) a runner whose GREEN step throws ⇒ NOT_GREEN, proven false, the row still written, the tree restored', async () => {
  const { dir, base } = directRepo();
  const rowsBefore = (await readAllRows(SLUG)).length;
  /** @type {string[]} */
  const printed = [];
  const result = await runRedGreen({
    workDir: dir,
    base,
    test: { file: 'checks/ACheck.php' },
    sources: ['src/a.php'],
    isolation: 'lock',
    print: (line) => printed.push(line),
    record: { runId: RUN, blockId: 'A', writeRow },
    runner: fakeHook(async () => {
      throw new Error('FAKE green step blew up');
    }),
  });
  assert.deepEqual([result.red.verdict, result.green, result.proven], ['RED', 'NOT_GREEN', false]);
  assert.deepEqual(printed, ['RED checks/ACheck.php', 'NOT_GREEN checks/ACheck.php']);
  const added = (await readAllRows(SLUG)).slice(rowsBefore);
  assert.deepEqual(
    added.map((r) => [r.step, r.test, r.red, r.green, r.proven, r.runner, r.failed_tests, r.env_keys, 'failed_ids' in r]),
    [['red-green', 'checks/ACheck.php', 'RED', 'NOT_GREEN', false, 'junit', ['ACheck.caps'], [], false]],
  );
  assert.deepEqual([readFileSync(path.join(dir, 'src/a.php'), 'utf8'), existsSync(journalPath(dir))], ['<?php // FIXED\n', false]);
});

test('B88 runRedGreen refuses a runner that is not a whole hook (null included), with or without argv (usage, nothing touched)', async () => {
  const { dir, base } = directRepo();
  const common = { workDir: dir, base, test: { file: 'checks/ACheck.php' }, sources: ['src/a.php'], isolation: /** @type {const} */ ('lock') };
  const message = 'red-green: runner must be an object with red and green functions, an adapter name and an env_keys list';
  const good = fakeHook(async () => 'GREEN');
  /** @type {[string, unknown][]} */
  const bad = [
    ['null', null],
    ['false', false],
    ['a string', 'junit'],
    ['no red', { ...good, red: undefined }],
    ['green not a function', { ...good, green: 'GREEN' }],
    ['no env_keys', { ...good, env_keys: undefined }],
    ['env_keys not strings', { ...good, env_keys: [7] }],
    ['no adapter', { ...good, adapter: undefined }],
  ];
  for (const [name, runner] of bad) {
    for (const argv of [undefined, [NODE, '-e', '']]) {
      await assert.rejects(
        runRedGreen({ ...common, ...(argv ? { argv } : {}), runner: /** @type {any} */ (runner) }).then(() => [name, 'resolved']),
        (/** @type {any} */ err) => {
          assert.deepEqual([name, err.code, err.message], [name, 'usage', message]);
          return true;
        },
      );
    }
  }
  assert.deepEqual([readFileSync(path.join(dir, 'src/a.php'), 'utf8'), existsSync(journalPath(dir))], ['<?php // FIXED\n', false]);
});

test('B88 a runner GREEN value other than the exact word GREEN is NOT_GREEN (proven false)', async () => {
  const { dir, base } = directRepo();
  /** @type {[unknown, string][]} */
  const cases = [['GREEN', 'GREEN'], ['NOT_GREEN', 'NOT_GREEN'], ['green', 'NOT_GREEN'], [true, 'NOT_GREEN'], [undefined, 'NOT_GREEN'], [{ verdict: 'GREEN' }, 'NOT_GREEN']];
  for (const [value, expected] of cases) {
    const result = await runRedGreen({
      workDir: dir,
      base,
      test: { file: 'checks/ACheck.php' },
      sources: ['src/a.php'],
      isolation: 'lock',
      runner: fakeHook(async () => /** @type {any} */ (value)),
    });
    assert.deepEqual([JSON.stringify(value), result.green, result.proven], [JSON.stringify(value), expected, expected === 'GREEN']);
  }
});

test('(1) a matched entry with no hook is refused: exit 2, no row, no precheck, no runner call — never a node:test fallback', async () => {
  writeFileSync(MODE, 'normal\n');
  const rowsBefore = (await readAllRows(SLUG)).length;
  const calls = logLines().length;
  /** @type {string[]} */
  const prechecked = [];
  const res = await cli(proveArgs(TEST), {
    hook: () => null,
    precheck: async (entry) => {
      prechecked.push(entry.adapter);
    },
  });
  assert.deepEqual([res.code, res.stdout, res.stderr], [2, '', `proof red-green: the junit runner for ${TEST} has no hook; no proof was run\n`]);
  assert.deepEqual([(await readAllRows(SLUG)).length - rowsBefore, logLines().length - calls, prechecked, existsSync(journalPath(EXPORT_DIR))], [0, 0, [], false]);
});

test('(2) selectRunner and runnerCovers refuse a snapshot value that is not a list, or an empty list (runner-invalid, never a fallback)', () => {
  for (const [value, message] of /** @type {[unknown, string][]} */ ([
    [{ match: ['*.php'] }, "the run snapshot's proof.runners is not a list; no proof was run"],
    ['FAKE', "the run snapshot's proof.runners is not a list; no proof was run"],
    [undefined, "the run snapshot's proof.runners is not a list; no proof was run"],
    [[], "the run snapshot's proof.runners is an empty list; no proof was run"],
  ])) {
    assert.throws(() => selectRunner(value, TEST), { code: 'runner-invalid', message });
    assert.throws(() => runnerCovers(value, TEST), { code: 'runner-invalid', message });
  }
  assert.deepEqual([selectRunner(null, TEST), runnerCovers(null, TEST)], [null, false]);
});

test('(11) a swift runner: a bare XCTest failure with its console line is RED assertion, then GREEN; the row says runner swift', async () => {
  const shape = (/** @type {Record<string, any>[]} */ rows) => rows.map((r) => [r.step, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed_tests ?? null, r.env_keys ?? null]);
  try {
    writeFileSync(MODE, 'xctest\n');
    let rowsBefore = (await readAllRows(SLUG)).length;
    const res = await cli(proveArgs(SWIFT_TEST));
    assert.deepEqual([res.code, res.stderr], [0, `RED ${SWIFT_TEST}\nGREEN ${SWIFT_TEST}\n`]);
    const printed = JSON.parse(res.stdout);
    assert.deepEqual([printed.red_kind, printed.runner, printed.failed_tests, printed.env_keys], ['assertion', 'swift', ['ClampCheck.caps'], []]);
    assert.deepEqual(shape((await readAllRows(SLUG)).slice(rowsBefore)), [
      ['export', null, null, null, null, null, null, null],
      ['red-green', 'RED', 'assertion', 'GREEN', true, 'swift', ['ClampCheck.caps'], []],
    ]);
    writeFileSync(MODE, 'barenoline\n');
    rowsBefore = (await readAllRows(SLUG)).length;
    const noLine = await cli(proveArgs(SWIFT_TEST));
    assert.deepEqual([noLine.code, noLine.stderr], [1, `RED_INVALID ${SWIFT_TEST} (error)\n`]);
    assert.deepEqual(shape((await readAllRows(SLUG)).slice(rowsBefore)), [
      ['export', null, null, null, null, null, null, null],
      ['red-green', 'RED_INVALID', 'error', null, false, 'swift', ['ClampCheck.caps'], []],
    ]);
  } finally {
    writeFileSync(MODE, 'normal\n');
  }
});

test('B88 a node:test proof (no runner matches the file): the row and the printed JSON carry no runner, env_keys or failed_tests', async () => {
  await openBlock({ runId: RUN, id: 'N', level: 'L1', owned: ['nodesrc/**', 'nodetests/**'], acceptance: [{ clause: 'node clamp caps at hi', tests: [NODE_TEST] }], writeRow });
  const rowsBefore = (await readAllRows(SLUG)).length;
  const calls = logLines().length;
  const res = await cli(['red-green', 'N', '--run', RUN, '--test', NODE_TEST]);
  assert.deepEqual([res.code, res.stderr], [0, `RED ${NODE_TEST}\nGREEN ${NODE_TEST}\n`]);
  const printed = JSON.parse(res.stdout);
  const added = (await readAllRows(SLUG)).slice(rowsBefore);
  assert.deepEqual(added.map((r) => [r.block, r.step, r.red ?? null, r.green ?? null, r.proven ?? null]), [['N', 'export', null, null, null], ['N', 'red-green', 'RED', 'GREEN', true]]);
  const row = added[1];
  assert.deepEqual(
    [printed.red, printed.green, printed.proven, row.covers, ['runner', 'env_keys', 'failed_tests'].filter((k) => k in row || k in printed)],
    ['RED', 'GREEN', true, ['nodesrc/clamp.mjs'], []],
  );
  assert.equal(logLines().length - calls, 0); // the fake runner never ran
});
