import './support.mjs';
import { fakeProbe, rowSink, withFixture } from '../state/helpers.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { checkAcceptance, parseTapPassed } from '../../src/gates/acceptance.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';

test('B137 acceptance: human checks without tests are loaded, recorded and skipped for test coverage', async () => {
  const acceptance = [{ clause: 'check layout on a phone', kind: 'human_check' }, { clause: 'renders', tests: ['renders'] }];
  assert.deepEqual(checkAcceptance(acceptance, ['renders']), { ok: true });
  await withFixture(async ({ ws }) => {
    const { writeRow } = rowSink();
    await startRun({ workspace: ws, project: 'two-blocks', runId: 'r-human-acceptance', workerPid: 4242, probe: fakeProbe({ 4242: 'start-A' }), writeRow });
    await assert.doesNotReject(() => openBlock({ runId: 'r-human-acceptance', id: 'B1', level: 'L1', owned: ['a.txt'], acceptance, writeRow }));
    const entry = (await readRun('r-human-acceptance')).blocks.B1;
    assert.deepEqual(entry.acceptance, acceptance);
    assert.deepEqual(entry.human_checks, ['check layout on a phone']);
  });
});

test('B137 acceptance boundary: human checks may not name tests and every other clause needs tests', async () => {
  await withFixture(async ({ ws }) => {
    const { writeRow } = rowSink();
    await startRun({ workspace: ws, project: 'two-blocks', runId: 'r-human-invalid', workerPid: 4242, probe: fakeProbe({ 4242: 'start-A' }), writeRow });
    for (const clause of [
      { clause: 'phone', kind: 'human_check', tests: ['renders'] },
      { clause: 'phone', kind: 'human_check', tests: [] },
      { clause: 'normal' },
      { clause: 'other kind', kind: 'other' },
    ]) {
      assert.equal(checkAcceptance([clause], ['renders']).ok, false, JSON.stringify(clause));
      await assert.rejects(() => openBlock({ runId: 'r-human-invalid', id: 'B1', level: 'L1', owned: ['a.txt'], acceptance: [clause], writeRow }), { code: 'bad-acceptance' });
    }
    assert.deepEqual((await readRun('r-human-invalid')).blocks, {});
  });
});

// ── The acceptance-counted case: 2 clauses → 3 test ids, one missing ⇒ refused naming the clause ──

test('2 clauses naming 3 test ids: one missing from the LAST clause ⇒ refused, naming the clause that owns it', () => {
  const clauses = [
    { clause: 'the widget renders', tests: ['t1', 't2'] },
    { clause: 'the widget saves', tests: ['t3'] },
  ];
  const passed = new Set(['t1', 't2']); // t3 never ran or failed
  const result = checkAcceptance(clauses, passed);
  assert.equal(result.ok, false);
  assert.equal(result.clause, 'the widget saves');
  assert.equal(result.test, 't3');
  assert.match(result.reason, /the widget saves/);
  assert.match(result.reason, /t3/);
});

test('2 clauses naming 3 test ids: one missing from the FIRST clause ⇒ refused, naming THAT clause — not always the last one checked', () => {
  // Guards against an off-by-one loop (e.g. one that starts at index 1, or that only ever
  // reports the LAST clause regardless of which one actually failed): t1 is missing from the
  // FIRST clause while t2/t3 (the second clause) are both present and passed.
  const clauses = [
    { clause: 'the widget renders', tests: ['t1'] },
    { clause: 'the widget saves', tests: ['t2', 't3'] },
  ];
  const passed = new Set(['t2', 't3']); // t1 never ran or failed
  const result = checkAcceptance(clauses, passed);
  assert.equal(result.ok, false);
  assert.equal(result.clause, 'the widget renders');
  assert.equal(result.test, 't1');
});

test('all 3 test ids present and passed ⇒ ok (positive control for both clause-position cases above)', () => {
  const clauses = [
    { clause: 'the widget renders', tests: ['t1', 't2'] },
    { clause: 'the widget saves', tests: ['t3'] },
  ];
  const passed = new Set(['t1', 't2', 't3']);
  assert.deepEqual(checkAcceptance(clauses, passed), { ok: true });
});

test('an array of passed ids works the same as a Set', () => {
  const clauses = [{ clause: 'c', tests: ['t1'] }];
  assert.deepEqual(checkAcceptance(clauses, ['t1']), { ok: true });
  assert.equal(checkAcceptance(clauses, ['other']).ok, false);
});

test('a clause naming zero tests is refused (a clause must name ≥ 1 test id)', () => {
  const result = checkAcceptance([{ clause: 'no tests named', tests: [] }], new Set(['t1']));
  assert.equal(result.ok, false);
  assert.match(result.reason, /no test id/);
});

test('no clauses at all is refused, not vacuously ok (an empty-array check that returns ok would hide a missing acceptance)', () => {
  assert.equal(checkAcceptance([], new Set()).ok, false);
});

test('a test id that FAILED (not ok) but is not in `passed` is treated the same as missing', () => {
  const clauses = [{ clause: 'c', tests: ['t1'] }];
  const { passed } = parseTapPassed('TAP version 13\nnot ok 1 - t1\n1..1\n');
  assert.equal(checkAcceptance(clauses, passed).ok, false);
});

// ── failed set: a name shared by two different tests (one passes, one fails) must NOT pass ────

test('checkAcceptance refuses a test id that is in BOTH passed and failed (two different tests share one name) — accepting on passed alone would let this through', () => {
  const clauses = [{ clause: 'the shared name works', tests: ['works'] }];
  // parseTapPassed flattens every file's/describe-block's subtests into one global name set, so
  // 'works' passing in file A and failing in file B lands 'works' in BOTH sets.
  const tap = ['TAP version 13', "ok 1 - works", "not ok 2 - works", '1..2'].join('\n');
  const { passed, failed } = parseTapPassed(tap);
  assert.ok(passed.has('works') && failed.has('works'), 'the fixture must actually produce the ambiguous name in both sets');
  const result = checkAcceptance(clauses, passed, failed);
  assert.equal(result.ok, false);
  assert.equal(result.clause, 'the shared name works');
  assert.equal(result.test, 'works');
});

test('checkAcceptance with no `failed` argument defaults to an empty set (backward compatible with a passed-only caller)', () => {
  const clauses = [{ clause: 'c', tests: ['t1'] }];
  assert.deepEqual(checkAcceptance(clauses, new Set(['t1'])), { ok: true });
});

// ── parseTapPassed: real node --test TAP shape (verified on this machine, 2026-09-24) ───────

test('parseTapPassed reads real `node --test` TAP output — 2 passed, 1 failed, exact names', () => {
  const tap = [
    'TAP version 13',
    '# Subtest: adds two numbers',
    'ok 1 - adds two numbers',
    '  ---',
    "  duration_ms: 0.49",
    '  ...',
    '# Subtest: fails on purpose',
    'not ok 2 - fails on purpose',
    '  ---',
    "  duration_ms: 0.80",
    '  ...',
    '# Subtest: a third one',
    'ok 3 - a third one',
    '1..3',
    '# tests 3',
    '# pass 2',
    '# fail 1',
  ].join('\n');
  const { passed, failed } = parseTapPassed(tap);
  assert.deepEqual([...passed].sort(), ['a third one', 'adds two numbers']);
  assert.deepEqual([...failed], ['fails on purpose']);
});

test('parseTapPassed ignores non-test TAP lines (comments, diagnostics, the plan line)', () => {
  const { passed, failed } = parseTapPassed('TAP version 13\n# a comment\n1..0\n# tests 0\n');
  assert.equal(passed.size, 0);
  assert.equal(failed.size, 0);
});

// ── TAP directives: `ok ... # SKIP` / `ok ... # TODO` are "ok" lines but never a real pass ────

test('parseTapPassed: an "ok ... # SKIP" line is NOT added to passed — a skipped test never satisfies a clause', () => {
  const { passed, failed } = parseTapPassed('TAP version 13\nok 1 - skipped one # SKIP not ready\n1..1\n');
  assert.equal(passed.has('skipped one'), false);
  assert.equal(passed.size, 0);
  assert.equal(failed.size, 0);
});

test('parseTapPassed: an "ok ... # TODO" line is NOT added to passed either', () => {
  const { passed } = parseTapPassed('TAP version 13\nok 2 - todo one # TODO not implemented\n1..1\n');
  assert.equal(passed.has('todo one'), false);
  assert.equal(passed.size, 0);
});

test('checkAcceptance refuses a clause that names a SKIPPED test — a real bug this closes: node --test reports a skip as "ok", so a naive parser would let it satisfy the clause', () => {
  const clauses = [{ clause: 'the skipped thing works', tests: ['skipped one'] }];
  const { passed, failed } = parseTapPassed('TAP version 13\nok 1 - skipped one # SKIP not ready\n1..1\n');
  const result = checkAcceptance(clauses, passed, failed);
  assert.equal(result.ok, false);
  assert.equal(result.test, 'skipped one');
});

test('a genuinely passing (non-directive) "ok" line among skipped ones still satisfies its own clause', () => {
  const tap = ['TAP version 13', 'ok 1 - real pass', 'ok 2 - skipped one # SKIP later', '1..2'].join('\n');
  const { passed, failed } = parseTapPassed(tap);
  const result = checkAcceptance([{ clause: 'c', tests: ['real pass'] }], passed, failed);
  assert.deepEqual(result, { ok: true });
});
