import './support.mjs';
import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { runGatesVerb } from '../../src/cli/gates.mjs';
import { runLines, statusOf } from '../../src/cli/status.mjs';
import { parseTapPassed } from '../../src/gates/acceptance.mjs';
import { captureStream, withTempDir } from './support.mjs';

const PENDING = 'Promise resolution is still pending but the event loop has already resolved';
const TIMEOUT = 'test: TIMEOUT after 1 s (not a test failure); rerun with --timeout-ms 2000 or raise gates.timeout_s';

/** A fake test runner emits both Node reporter formats, then exceeds the real gate deadline.
 * @param {string} cwd @param {boolean} [realFailure] @param {boolean} [nested]
 */
async function slowGate(cwd, realFailure = false, nested = false) {
  const tapChildren = [
    'TAP version 13',
    ...(realFailure ? ['not ok 1 - real assertion', '  ---', '  error: expected 2 to equal 3', '  failureType: testCodeFailure', '  ...'] : []),
    `not ok ${realFailure ? 2 : 1} - unfinished.test.mjs`,
    '  ---', `  error: '${PENDING}'`, '  failureType: cancelledByParent', '  ...', '',
  ];
  const specChildren = [
    ...(realFailure ? ['✖ real assertion (1ms)', '  AssertionError: expected 2 to equal 3'] : []),
    `✖ unfinished.test.mjs (1000ms) '${PENDING}'`, '',
  ];
  const parentDiagnostic = ['  ---', `  error: '${realFailure ? 2 : 1} subtests failed'`, '  failureType: subtestsFailed', '  ...'];
  // describe() results close after their children; a passing sibling must not affect classification.
  const tap = (nested ? [
    'TAP version 13', '# Subtest: unfinished.test.mjs', '    # Subtest: wrapped suite',
    ...tapChildren.slice(1).filter(Boolean).map((line) => `        ${line}`),
    `        ok ${realFailure ? 3 : 2} - completed child`, `        1..${realFailure ? 3 : 2}`,
    '    not ok 1 - wrapped suite', ...parentDiagnostic.map((line) => `    ${line}`),
    '    1..1', 'not ok 1 - unfinished.test.mjs', ...parentDiagnostic, '',
  ] : tapChildren).join('\n');
  const spec = (nested ? [
    '▶ wrapped suite', ...specChildren.filter(Boolean).map((line) => `  ${line}`),
    '  ✔ completed child (1ms)',
    '✖ wrapped suite (1000ms)', '',
  ] : specChildren).join('\n');
  const argv = [process.execPath, '-e', `process.stdout.write(${JSON.stringify(tap)}); process.stderr.write(${JSON.stringify(spec)}); setTimeout(() => {}, 30000);`];
  await writeFile(path.join(cwd, '.code-forge.yml'), `version: 1\ngates:\n  test: ${JSON.stringify(argv)}\n  lint: null\n  types: null\n  format: null\n`);
  const stdout = captureStream(), stderr = captureStream();
  const code = await runGatesVerb(['run', '--cwd', cwd, '--timeout-ms', '1000'], { stdout, stderr });
  return { code, text: stderr.text, result: JSON.parse(stdout.text) };
}

test('a gate past its limit reports TIMEOUT and no unfinished tests as failures', async () => {
  await withTempDir(async (cwd) => {
    const { code, text, result } = await slowGate(cwd);
    const gate = result.results[0];
    assert.equal(gate.timed_out, true, 'test gate must explicitly report timed_out');
    assert.equal(gate.reason, 'timeout');
    assert.equal(code, 1, 'timeout still blocks the gate');
    assert.equal(result.allOk, false);
    assert.equal(text.split('\n')[0], TIMEOUT, 'first diagnostic line gives the exact doubled timeout');
    assert.deepEqual([...parseTapPassed(`${gate.stdout}\n${gate.stderr}`).failed], [], 'zero unfinished TAP files are failed tests, including diagnostic tails');
    assert.deepEqual(text.split('\n').filter((line) => /^\s*✖/.test(line)), [], 'zero unfinished spec files are displayed as failures');
  });
});

test('a real assertion failure remains listed when the same gate times out', async () => {
  await withTempDir(async (cwd) => {
    const { text, result } = await slowGate(cwd, true);
    const gate = result.results[0];
    assert.deepEqual([...parseTapPassed(`${gate.stdout}\n${gate.stderr}`).failed], ['real assertion'], 'exactly the real assertion remains in the failed-test list');
    assert.deepEqual(gate.stderr.split('\n').filter((line) => /^\s*✖/.test(line)), ['✖ real assertion (1ms)'], 'only the real spec failure remains marked failed');
    assert.deepEqual(text.split('\n').filter((line) => /^\s*✖/.test(line)), ['✖ real assertion (1ms)'], 'the CLI displays exactly the real spec failure');
    assert.equal(gate.timed_out, true);
    assert.equal(text.split('\n')[0], TIMEOUT);
  });
});

test('a suite and enclosing file with only cancelled failures are unfinished', async () => {
  await withTempDir(async (cwd) => {
    const { result } = await slowGate(cwd, false, true);
    const gate = result.results[0];
    assert.equal(gate.timed_out, true);
    assert.deepEqual(gate.stdout.split('\n').filter((line) => /^\s*not ok\b/.test(line)), []);
    assert.deepEqual(gate.stderr.split('\n').filter((line) => /^\s*✖/.test(line)), []);
    assert.match(gate.stdout, /# unfinished 1 - wrapped suite/);
    assert.match(gate.stdout, /# unfinished 1 - unfinished\.test\.mjs/);
    assert.match(gate.stderr, /ℹ unfinished: wrapped suite/);
  });
});

test('a suite with a cancelled child and a real failure keeps its failed ancestors', async () => {
  await withTempDir(async (cwd) => {
    const { result } = await slowGate(cwd, true, true);
    const gate = result.results[0];
    assert.equal(gate.timed_out, true);
    assert.deepEqual(gate.stdout.split('\n').filter((line) => /^\s*not ok\b/.test(line)), [
      '        not ok 1 - real assertion', '    not ok 1 - wrapped suite', 'not ok 1 - unfinished.test.mjs',
    ]);
    assert.deepEqual(gate.stderr.split('\n').filter((line) => /^\s*✖/.test(line)), [
      '  ✖ real assertion (1ms)', '✖ wrapped suite (1000ms)',
    ]);
    assert.match(gate.stdout, /# unfinished 2 - unfinished\.test\.mjs/);
    assert.match(gate.stderr, /ℹ unfinished: unfinished\.test\.mjs/);
  });
});

test('status shows gate timeout for the latest timed-out gate and clears it on later results', async () => {
  await withTempDir(async (workspace) => {
    const record = { run_id: 'r-timeout-report', workspace, blocks: { B1: { status: 'open', level: 'L1' } } };
    const row = (event, fields = {}) => ({ run: record.run_id, block: 'B1', event, ...fields });
    const timeout = row('gate.red', { gate: 'test', reason: 'timeout' });
    const done = row('gate.done', { all_ok: false });
    const cases = [
      { rows: [timeout, done], field: 'timeout (test)' },
      { rows: [timeout], field: 'timeout (test)' },
      { rows: [timeout, done, row('gate.red', { gate: 'lint' }), done], field: 'fail (lint)' },
      { rows: [timeout, done, row('gate.done', { all_ok: true })], field: 'pass' },
      { rows: [timeout, done, done], field: 'fail' },
      { rows: [], field: 'none' },
    ];
    for (const { rows, field } of cases) {
      const summary = await statusOf(record, rows);
      assert.deepEqual(runLines(summary).slice(1), [`B1 open L1 · - · gate ${field} · findings 0`], `exactly one block line with gate ${field}`);
    }
  });
});
