import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { checkSafeEdit, countTestDeclarations, isTestFile, safeEdit } from '../../src/gates/safe-edit.mjs';
import { buildTestRepo, git, withTempDir } from './support.mjs';

// ── The two acceptance-counted cases: deleted test file, >20% shrink ────────

test('safe-edit aborts when a test file present at base is deleted', () => {
  const result = safeEdit({
    base: { 'a.test.mjs': 3, 'b.test.mjs': 2 },
    current: { 'a.test.mjs': 3 }, // b.test.mjs is gone
  });
  assert.equal(result.ok, false);
  assert.match(result.reason, /deleted test file/);
  assert.match(result.reason, /b\.test\.mjs/);
});

test('safe-edit aborts when the total test-declaration count shrinks by more than 20%', () => {
  // base total 10, current total 7 ⇒ 30% shrink, no file deleted.
  const result = safeEdit({
    base: { 'a.test.mjs': 6, 'b.test.mjs': 4 },
    current: { 'a.test.mjs': 5, 'b.test.mjs': 2 },
  });
  assert.equal(result.ok, false);
  assert.match(result.reason, /shrank from 10 to 7/);
});

// ── The boundary and the happy path must NOT trip (the threshold is strictly >20%) ──────────

test('exactly a 20% shrink is allowed (boundary is strictly MORE than 20%)', () => {
  const result = safeEdit({ base: { 'a.test.mjs': 10 }, current: { 'a.test.mjs': 8 } });
  assert.equal(result.ok, true);
});

test('growing or unchanged test count with no deletion is ok', () => {
  assert.equal(safeEdit({ base: { 'a.test.mjs': 3 }, current: { 'a.test.mjs': 5 } }).ok, true);
  assert.equal(safeEdit({ base: {}, current: {} }).ok, true);
});

test('a deleted file is reported even when the surviving total does not shrink 20% (deletion check runs first)', () => {
  const result = safeEdit({ base: { 'a.test.mjs': 1, 'b.test.mjs': 100 }, current: { 'a.test.mjs': 200 } });
  assert.equal(result.ok, false);
  assert.match(result.reason, /deleted test file/);
});

// ── Test-declaration counting ────────────────────────────────────────────────

test('countTestDeclarations counts node:test test()/it() calls, per its own stack only', () => {
  const content = "test('a', () => {});\ntest('b', () => {});\nit('c', () => {});\n";
  assert.equal(countTestDeclarations(content, 'node'), 3);
});

test('countTestDeclarations counts pytest def test_ functions', () => {
  const content = 'def test_one():\n    pass\n\ndef test_two():\n    pass\n';
  assert.equal(countTestDeclarations(content, 'python'), 2);
});

test('countTestDeclarations counts rust #[test] and go func Test*', () => {
  assert.equal(countTestDeclarations('#[test]\nfn one() {}\n\n#[test]\nfn two() {}\n', 'rust'), 2);
  assert.equal(countTestDeclarations('func TestOne(t *testing.T) {}\nfunc TestTwo(t *testing.T) {}\n', 'go'), 2);
});

test('countTestDeclarations refuses an unknown stack (silently returning 0 would hide a real miscount)', () => {
  assert.throws(() => countTestDeclarations('test();', 'ruby'), TypeError);
});

test('counting by the FILE\'S OWN classified stack only, never every stack summed, avoids double-counting a Pest file (which legitimately uses test()/it() too)', () => {
  // Both node and Pest use `test('name', fn)`/`it('name', fn)` call syntax — the two patterns
  // legitimately overlap on IDENTICAL text. countTestDeclarations trusts the caller's `stack`
  // argument rather than guessing, so a file classified 'node' by name is counted once, with
  // node's pattern only — this is what `checkSafeEdit`'s per-file classification relies on to
  // avoid the double-count a blind "sum every pattern" implementation had during development.
  const content = "test('a', () => {});\ntest('b', () => {});\nit('c', () => {});\n";
  assert.equal(countTestDeclarations(content, 'node'), 3);
  assert.equal(countTestDeclarations(content, 'php'), 3); // same text, Pest's own pattern also matches 3 — by design
});

test('isTestFile recognizes common naming conventions per stack', () => {
  assert.equal(isTestFile('src/foo.test.mjs'), true);
  assert.equal(isTestFile('src/foo.spec.ts'), true);
  assert.equal(isTestFile('tests/FooTest.php'), true);
  assert.equal(isTestFile('tests/test_foo.py'), true);
  assert.equal(isTestFile('pkg/foo_test.go'), true);
  assert.equal(isTestFile('src/foo.mjs'), false);
});

// ── Git-backed orchestration (checkSafeEdit) — real repo, real diff ─────────

test('checkSafeEdit: ok on an unchanged repo', async () => {
  await withTempDir(async (dir) => {
    const { baseSha } = await buildTestRepo(dir);
    const result = await checkSafeEdit({ cwd: dir, base: baseSha });
    assert.equal(result.ok, true);
  });
});

test('checkSafeEdit: aborts when the working tree deletes a test file that existed at base', async () => {
  await withTempDir(async (dir) => {
    const { baseSha } = await buildTestRepo(dir);
    await rm(path.join(dir, 'b.test.mjs'));
    const result = await checkSafeEdit({ cwd: dir, base: baseSha });
    assert.equal(result.ok, false);
    assert.match(result.reason, /deleted test file/);
    assert.match(result.reason, /b\.test\.mjs/);
  });
});

test('checkSafeEdit: aborts when edits shrink the total test count by more than 20%', async () => {
  await withTempDir(async (dir) => {
    const { baseSha } = await buildTestRepo(dir);
    // base has a.test.mjs (2 tests) + b.test.mjs (1 test) = 3 total; shrink b to 0 tests ⇒ 2/3 ≈ 33% shrink.
    await writeFile(path.join(dir, 'b.test.mjs'), "import { test } from 'node:test';\n// no tests left\n");
    const result = await checkSafeEdit({ cwd: dir, base: baseSha });
    assert.equal(result.ok, false);
    assert.match(result.reason, /test count shrank/);
  });
});

test('checkSafeEdit: a base test file whose name contains a double-quote — git C-quotes such a path without -z (e.g. `qu"ote.test.mjs` → `"qu\\"ote.test.mjs"`), which would otherwise make its basename end in a literal quote and fail classification — is still read by its EXACT path, and its deletion is reported naming that exact path', async () => {
  await withTempDir(async (dir) => {
    const quotedName = 'qu"ote.test.mjs';
    git(['init', '-q'], dir);
    git(['symbolic-ref', 'HEAD', 'refs/heads/main'], dir);
    await writeFile(path.join(dir, quotedName), "import { test } from 'node:test';\ntest('a', () => {});\ntest('b', () => {});\n");
    git(['add', '-A'], dir);
    git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], dir);
    const baseSha = git(['rev-parse', 'HEAD'], dir);

    await rm(path.join(dir, quotedName));
    const result = await checkSafeEdit({ cwd: dir, base: baseSha });
    assert.equal(result.ok, false);
    assert.match(result.reason, /deleted test file/);
    assert.ok(result.reason.includes(quotedName), `reason should name the exact path ${JSON.stringify(quotedName)}, got: ${result.reason}`);
  });
});

test('checkSafeEdit: ok when tests move to a NEW file — the total is unchanged even though no single base file keeps its old count', async () => {
  await withTempDir(async (dir) => {
    const { baseSha } = await buildTestRepo(dir);
    // base: a.test.mjs has 2 tests ('one','two'), b.test.mjs has 1 ('three') — total 3.
    // Split a.test.mjs down to 1 test, and move the other into a brand-new c.test.mjs — still
    // 3 total, just reorganized. A `current` built only from base's own file list would miss
    // c.test.mjs entirely and misreport this as a's count shrinking from 2 to 1 (a real bug this
    // regression test closes).
    await writeFile(path.join(dir, 'a.test.mjs'), "import { test } from 'node:test';\ntest('one', () => {});\n");
    await writeFile(path.join(dir, 'c.test.mjs'), "import { test } from 'node:test';\ntest('two-moved', () => {});\n");
    const result = await checkSafeEdit({ cwd: dir, base: baseSha });
    assert.deepEqual(result, { ok: true });
  });
});
