import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  buildQuestionPayload,
  isKnownQuestion,
  isQuestionDisabled,
  QUESTION_IDS,
  QUESTIONS,
  renderQuestion,
} from '../../src/decide/questions.mjs';

test('QUESTION_IDS holds exactly the plan §3.2 table, in order — 7 ids', () => {
  assert.deepEqual(QUESTION_IDS, ['lane', 'risk', 'security_sensitive', 'next', 'scope', 'defect', 'resolved']);
  assert.equal(QUESTION_IDS.length, 7);
});

test('every registry entry has a valid type and non-empty instructions/criteria', () => {
  for (const id of QUESTION_IDS) {
    const spec = QUESTIONS[id];
    assert.ok(['choice', 'score', 'noul'].includes(spec.type), `${id}.type`);
    assert.ok(spec.instructions.length > 0, `${id}.instructions`);
    assert.ok(Object.keys(spec.criteria).length > 0, `${id}.criteria`);
  }
});

test('isKnownQuestion', () => {
  assert.equal(isKnownQuestion('lane'), true);
  assert.equal(isKnownQuestion('made-up'), false);
  assert.equal(isKnownQuestion(''), false);
});

test('isQuestionDisabled reads system1.disable', () => {
  assert.equal(isQuestionDisabled('defect', {}), false);
  assert.equal(isQuestionDisabled('defect', { system1: { disable: ['defect', 'resolved'] } }), true);
  assert.equal(isQuestionDisabled('lane', { system1: { disable: ['defect', 'resolved'] } }), false);
});

test('renderQuestion: ships the registry verbatim with no config', () => {
  const r = renderQuestion('lane', {});
  assert.equal(r.type, 'choice');
  assert.equal(r.instructions, QUESTIONS.lane.instructions);
  assert.deepEqual(r.criteria, QUESTIONS.lane.criteria);
});

test('renderQuestion: system1.criteria_extra merges extra criteria and can override instructions', () => {
  const cfg = {
    system1: {
      criteria_extra: {
        lane: { instructions: 'Project-specific phrasing.', criteria: { L4: 'a project-specific escape hatch' } },
      },
    },
  };
  const r = renderQuestion('lane', cfg);
  assert.equal(r.instructions, 'Project-specific phrasing.');
  assert.equal(r.criteria.L4, 'a project-specific escape hatch');
  assert.equal(r.criteria.L0, QUESTIONS.lane.criteria.L0); // extra criteria ADDS, never drops the shipped ones
});

// Fix round 1 finding: the merge test above never checked that the SHARED registry itself is left
// untouched — a bug written as `Object.assign(QUESTIONS[id].criteria, extra.criteria)` would still
// pass that test, and would carry a caller's `criteria_extra` (e.g. "L4") into every LATER call
// for every project, since the registry is a module-level singleton.
test('renderQuestion never mutates the shared QUESTIONS registry, and returns a fresh criteria object', () => {
  const before = structuredClone(QUESTIONS.lane);
  const r = renderQuestion('lane', {
    system1: { criteria_extra: { lane: { criteria: { L4: 'a project-specific escape hatch' } } } },
  });
  assert.deepEqual(QUESTIONS.lane, before); // the registry entry is byte-for-byte unchanged
  assert.equal(Object.hasOwn(QUESTIONS.lane.criteria, 'L4'), false); // "L4" did NOT leak into it
  assert.notEqual(r.criteria, QUESTIONS.lane.criteria); // the returned criteria is its OWN object

  // Proof this isn't just because the source objects are frozen (Object.assign onto a frozen
  // object throws in strict mode, which would ALSO make this pass for the wrong reason): a SECOND
  // call with no criteria_extra at all must come back with the plain registry criteria, unpolluted
  // by the first call's "L4".
  const second = renderQuestion('lane', {});
  assert.deepEqual(second.criteria, QUESTIONS.lane.criteria);
});

test('renderQuestion: throws for an unknown id or a disabled one', () => {
  assert.throws(() => renderQuestion('made-up', {}), TypeError);
  assert.throws(() => renderQuestion('defect', { system1: { disable: ['defect'] } }), TypeError);
});

test('buildQuestionPayload: renders exactly the requested ids, in the shape askJev expects', () => {
  const payload = buildQuestionPayload(['lane', 'risk'], {});
  assert.deepEqual(Object.keys(payload), ['lane', 'risk']);
  assert.equal(payload.lane.type, 'choice');
  assert.equal(payload.risk.type, 'score');
});

test('buildQuestionPayload: a score question sends criteria as a list indexed by score (Jev 422s on an object)', () => {
  const payload = buildQuestionPayload(['risk', 'lane'], {});
  assert.ok(Array.isArray(payload.risk.criteria));
  assert.deepEqual(payload.risk.criteria, [0, 1, 2, 3].map((k) => QUESTIONS.risk.criteria[k]));
  assert.equal(Array.isArray(payload.lane.criteria), false); // choice keeps its keyed object
});

test('buildQuestionPayload: rejects an empty id list', () => {
  assert.throws(() => buildQuestionPayload([], {}), TypeError);
});
