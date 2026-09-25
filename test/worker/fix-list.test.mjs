// helpers FIRST: it pins $HOME under the per-file temp parent before any src module loads.
import './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { fixList } = await import('../../src/cli/review-file.mjs');

/** @param {string} id @param {number} line */
const finding = (id, line) => ({ id, file: 'src/a.mjs', line_start: line, line_end: line + 1, severity: 'warning', category: 'c', claim: `claim ${id}`, evidence: 'e', fix: `fix ${id}` });

test('fixList: approved ⇒ {}; 1 major ⇒ exactly 1 entry', () => {
  assert.deepEqual(fixList({ status: 'reviewed', approved: true, findings: [] }), {});
  assert.deepEqual(fixList({ status: 'reviewed', approved: false, findings: [finding('F1', 60)] }), {
    fix_list: [{ id: 'F1', severity: 'warning', lines: '60-61', claim: 'claim F1', fix: 'fix F1' }],
  });
});

test('fixList: stopped with 3 findings ⇒ stop + 3 entries; unavailable without findings ⇒ no fix_list key', () => {
  const stopped = fixList({ status: 'stopped', approved: false, stopped: 'review_stall', findings: [finding('F1', 1), finding('F2', 5), finding('F3', 9)] });
  assert.equal(stopped.stop, 'review_stall');
  assert.deepEqual(stopped.fix_list.map((/** @type {any} */ f) => f.id), ['F1', 'F2', 'F3']);
  const unavailable = fixList({ status: 'unavailable', reason: 'schema', approved: false });
  assert.deepEqual([Object.hasOwn(unavailable, 'fix_list'), unavailable], [false, {}]);
});
