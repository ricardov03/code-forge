// B38: the known-fixes table (`src/util/known-fixes.mjs`, the shipped `known-fixes.json`):
// validation by key, loading that never throws and leaves bad entries out, and the version status.
// One per-file temp parent, removed in `after()`; HOME points into it before any `src` module loads.
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, describe, test } from 'node:test';

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-known-fixes-'));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { KNOWN_FIXES_FILE, entryProblems, fixStatus, knownFixMap, loadKnownFixes, validateKnownFixes } = await import('../../src/util/known-fixes.mjs');

const GOOD = { fp: '0123456789ab', fixed_in: '0.4.0', summary: 'review no longer hangs on an empty diff.', issue: 42 };

describe('known-fixes table (B38)', () => {
  test('the shipped table is valid JSON, an empty array, and loads as an empty map', () => {
    assert.deepEqual(JSON.parse(readFileSync(KNOWN_FIXES_FILE, 'utf8')), []);
    assert.deepEqual(validateKnownFixes([]), []);
    assert.equal(loadKnownFixes().size, 0);
  });

  test('a good entry has 0 problems; issue may be null', () => {
    assert.deepEqual(entryProblems(GOOD), []);
    assert.deepEqual(entryProblems({ ...GOOD, issue: null }), []);
  });

  test('bad fingerprints are refused, naming the key only', () => {
    for (const fp of ['0123456789a', '0123456789abc', '0123456789AB', '0123456789ag', 12, undefined]) {
      assert.deepEqual(entryProblems({ ...GOOD, fp }), ['fp must be 12 lowercase hex characters'], String(fp));
    }
  });

  test('bad versions are refused', () => {
    for (const fixed_in of ['0.4', 'v0.4.0', '0.4.0-beta.1', '01.4.0', '0.4.0 ', 40, null]) {
      assert.deepEqual(entryProblems({ ...GOOD, fixed_in }), ['fixed_in must be a version x.y.z'], String(fixed_in));
    }
  });

  test('summary, issue and unknown keys', () => {
    assert.deepEqual(entryProblems({ ...GOOD, summary: '' }), ['summary must be a non-empty string']);
    assert.deepEqual(entryProblems({ ...GOOD, summary: 'two\nlines' }), ['summary must be one plain line (no line breaks, tabs, control or text-direction characters, or leading/trailing spaces)']);
    // C1 controls, line/paragraph separators and bidi overrides could spoof the terminal or the issue comment
    for (const bad of ['a\u009bb', 'a\u2028b', 'a\u202eb', 'a\u2066b']) assert.equal(entryProblems({ ...GOOD, summary: bad }).length, 1, JSON.stringify(bad));
    assert.deepEqual(entryProblems({ ...GOOD, summary: 'x'.repeat(201) }), ['summary must be at most 200 characters']);
    assert.deepEqual(entryProblems({ ...GOOD, issue: 0 }), ['issue must be a positive issue number or null']);
    assert.deepEqual(entryProblems({ fp: GOOD.fp, fixed_in: GOOD.fixed_in, summary: GOOD.summary }), ['issue must be a positive issue number or null']);
    assert.deepEqual(entryProblems({ ...GOOD, extra: 1 }), ['unknown key "extra"']);
    assert.deepEqual(entryProblems('x'), ['not an object']);
  });

  test('validateKnownFixes: not an array; per-entry problems numbered; a duplicate fingerprint', () => {
    assert.deepEqual(validateKnownFixes({}), ['the table must be a JSON array']);
    assert.deepEqual(validateKnownFixes([GOOD, { ...GOOD, fixed_in: '1.0' }, { ...GOOD, summary: 'again' }]), [
      'entry 2: fixed_in must be a version x.y.z',
      'entry 2: fp is already listed in entry 1',
      'entry 3: fp is already listed in entry 1',
    ]);
  });

  test('loadKnownFixes: bad entries are left out, the first of a fingerprint wins; a broken file is empty', () => {
    const file = path.join(PARENT, 'table.json');
    writeFileSync(file, JSON.stringify([GOOD, { ...GOOD, fixed_in: '9.9.9' }, { ...GOOD, fp: 'bad' }, { ...GOOD, fp: 'ffffffffffff', issue: null }]));
    const map = loadKnownFixes(file);
    assert.deepEqual([...map.keys()], ['0123456789ab', 'ffffffffffff']);
    assert.equal(map.get('0123456789ab').fixed_in, '0.4.0');
    writeFileSync(file, '[{');
    assert.equal(loadKnownFixes(file).size, 0);
    assert.equal(loadKnownFixes(path.join(PARENT, 'missing.json')).size, 0);
    assert.equal(knownFixMap({ fp: GOOD.fp }).size, 0);
  });

  test('fixStatus: older below fixed_in, has-fix at or above it, unknown for an unreadable version', () => {
    assert.equal(fixStatus(GOOD, '0.3.9'), 'older');
    assert.equal(fixStatus(GOOD, '0.4.0-rc.1'), 'older');
    assert.equal(fixStatus(GOOD, '0.4.0'), 'has-fix');
    assert.equal(fixStatus(GOOD, '0.10.0'), 'has-fix');
    assert.equal(fixStatus(GOOD, 'unknown'), 'unknown');
  });
});
