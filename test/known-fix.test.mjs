/**
 * B38: `scripts/known-fix.mjs`, run as a child process on a temp `src/util/known-fixes.json`
 * (never this repository's): add validates and appends, refuses a duplicate and bad input with
 * the file unchanged, and `check` validates the table.
 */

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, describe, test } from 'node:test';

const SCRIPT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'scripts', 'known-fix.mjs');
const PARENT = mkdtempSync(path.join(os.tmpdir(), 'known-fix-test-'));
let counter = 0;
after(() => rmSync(PARENT, { recursive: true, force: true }));

const USAGE = 'usage: npm run known-fix -- add <fp> <x.y.z> "<summary>" [--issue N]\n       npm run known-fix -- check\n';

/** @param {string} text the table file's content @returns {string} a temp repository root */
function repo(text) {
  counter += 1;
  const dir = path.join(PARENT, `case-${counter}`);
  mkdirSync(path.join(dir, 'src', 'util'), { recursive: true });
  writeFileSync(path.join(dir, 'src', 'util', 'known-fixes.json'), text);
  return dir;
}
/** @param {string} dir @param {string[]} args */
function knownFix(dir, args) {
  const r = spawnSync(process.execPath, [SCRIPT, ...args], { cwd: dir, encoding: 'utf8' });
  return { code: r.status, stdout: r.stdout, stderr: r.stderr, text: readFileSync(path.join(dir, 'src', 'util', 'known-fixes.json'), 'utf8') };
}

describe('scripts/known-fix.mjs (B38)', () => {
  test('add appends one entry (issue or null) in JSON.stringify(…, null, 2) layout', () => {
    const dir = repo('[]\n');
    let r = knownFix(dir, ['add', '0123456789ab', '0.4.0', 'review no longer hangs on an empty diff.', '--issue', '42']);
    assert.deepEqual([r.code, r.stdout, r.stderr], [0, 'added 0123456789ab (fixed in 0.4.0) to src/util/known-fixes.json\n', '']);
    r = knownFix(dir, ['add', 'ffffffffffff', '0.4.1', 'keys test retries once.']);
    assert.equal(r.code, 0, r.stderr);
    const table = [
      { fp: '0123456789ab', fixed_in: '0.4.0', summary: 'review no longer hangs on an empty diff.', issue: 42 },
      { fp: 'ffffffffffff', fixed_in: '0.4.1', summary: 'keys test retries once.', issue: null },
    ];
    assert.equal(r.text, `${JSON.stringify(table, null, 2)}\n`);
    const check = knownFix(dir, ['check']);
    assert.deepEqual([check.code, check.stdout], [0, 'src/util/known-fixes.json: 2 entries, valid\n']);
  });

  test('a duplicate fingerprint is refused; the file is unchanged', () => {
    const dir = repo('[]\n');
    assert.equal(knownFix(dir, ['add', '0123456789ab', '0.4.0', 'first.']).code, 0);
    const before = readFileSync(path.join(dir, 'src', 'util', 'known-fixes.json'), 'utf8');
    const r = knownFix(dir, ['add', '0123456789ab', '0.5.0', 'second.']);
    assert.deepEqual([r.code, r.stdout, r.stderr], [1, '', 'REFUSED: 0123456789ab is already in src/util/known-fixes.json\n']);
    assert.equal(r.text, before);
  });

  test('a bad fingerprint, a bad version and a bad issue are refused; the file is unchanged', () => {
    const dir = repo('[]\n');
    let r = knownFix(dir, ['add', '0123456789AB', '0.4', 'x.']);
    assert.deepEqual([r.code, r.stderr], [1, 'REFUSED: the new entry is invalid:\n  fp must be 12 lowercase hex characters\n  fixed_in must be a version x.y.z\n']);
    r = knownFix(dir, ['add', '0123456789ab', '0.4.0', 'x.', '--issue', '0']);
    assert.deepEqual([r.code, r.stderr], [2, `REFUSED: --issue must be a positive issue number\n${USAGE}`]);
    r = knownFix(dir, ['add', '0123456789ab', '0.4.0']);
    assert.deepEqual([r.code, r.stderr], [2, `REFUSED: ${USAGE}`]);
    assert.equal(r.text, '[]\n');
  });

  test('an invalid table is refused by add and check', () => {
    const bad = `${JSON.stringify([{ fp: 'nothex', fixed_in: '1.0.0', summary: 's', issue: null }])}\n`;
    const dir = repo(bad);
    const r = knownFix(dir, ['add', '0123456789ab', '0.4.0', 'x.']);
    assert.deepEqual([r.code, r.stderr, r.text], [1, 'REFUSED: src/util/known-fixes.json is invalid:\n  entry 1: fp must be 12 lowercase hex characters\n', bad]);
    assert.equal(knownFix(dir, ['check']).code, 1);
    const broken = repo('[{');
    assert.deepEqual([knownFix(broken, ['check']).code, knownFix(broken, ['check']).stderr], [1, 'REFUSED: src/util/known-fixes.json is not valid JSON\n']);
  });
});
