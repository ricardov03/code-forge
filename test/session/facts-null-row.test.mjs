import { cfgWith, fakeDeps, freshDir, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { validateFacts, FactsError, FACTS_SESSION_SCHEMA } = await import('../../src/session/facts.mjs');
const { spawnSession } = await import('../../src/session/spawn.mjs');

const claims = /** @type {const} */ ([{ token: './a', kind: 'path' }, { token: './b', kind: 'path' }]);
const answer = () => ({ facts: claims.map((claim, i) => ({ fact_id: `F${i + 1}`, claim: claim.token, kind: claim.kind, command: `ls ${claim.token}`, output_excerpt: claim.token, tag: 'VERIFIED', why: null })) });

test('null command or output_excerpt downgrades only the matched claim without mutating the answer', () => {
  const cases = [['command'], ['output_excerpt'], ['command', 'output_excerpt']];
  assert.equal(cases.length, 3);
  for (const fields of cases) {
    const input = answer();
    for (const field of fields) input.facts[0][field] = null;
    // Echo matching, rather than row position or fact id, must carry the downgrade.
    input.facts[0].fact_id = 'F9';
    input.facts.reverse();
    const before = structuredClone(input);
    let result;
    assert.doesNotThrow(() => { result = validateFacts(input, claims); }, 'one null row must not refuse the sheet');
    const reason = `delegate returned null ${fields.join(', ')}`;
    assert.deepEqual(result.facts.map((f) => [f.fact_id, f.tag, f.why]), [
      ['F1', 'UNVERIFIABLE', reason], ['F2', 'VERIFIED', null],
    ]);
    for (const field of fields) assert.equal(result.facts[0][field], '', `${field} normalized`);
    assert.deepEqual(result.downgraded, [{ fact_id: 'F1', reason }]);
    assert.deepEqual(input, before, 'caller answer stays unchanged');
  }
});

test('null normalization also downgrades an already unverifiable row, while other schema errors remain refused', () => {
  const input = answer();
  input.facts[0].command = null;
  input.facts[0].tag = 'UNVERIFIABLE';
  let result;
  assert.doesNotThrow(() => { result = validateFacts(input, claims); }, 'unverifiable null row must be normalized');
  assert.deepEqual(result.downgraded, [{ fact_id: 'F1', reason: 'delegate returned null command' }]);
  const badRows = [
    { command: 42 }, { output_excerpt: false }, { tag: null }, { kind: 'unknown' }, { extra: true },
  ];
  assert.equal(badRows.length, 5);
  for (const patch of badRows) {
    const invalid = answer();
    Object.assign(invalid.facts[0], patch);
    invalid.facts[1].command = null;
    assert.throws(() => validateFacts(invalid, claims), (err) => err instanceof FactsError && err.code === 'refused');
  }
});

test('strict OpenAI facts session accepts null why, rejects wrong types and rejects required nulls', async () => {
  const cfg = cfgWith({ provider: 'openai', model: 'fake-gpt' });
  const packet = writeIn(freshDir('null-rows'), 'packet.md', 'check ./a and ./b');
  const cases = [
    [null, 'ok'], [{ command: 42 }, 'invalid-output'], [{ command: null }, 'invalid-output'],
  ];
  assert.equal(cases.length, 3);
  for (const [patch, status] of cases) {
    const input = answer();
    if (patch) Object.assign(input.facts[0], patch);
    const { deps } = fakeDeps({ FAKE_ANSWER: JSON.stringify(input) });
    const result = await spawnSession({ cfg, level: 'L0', role: 'facts', promptPath: packet, schema: FACTS_SESSION_SCHEMA }, deps);
    assert.equal(result.status, status, `patch ${JSON.stringify(patch)}`);
    if (status === 'ok') {
      assert.deepEqual(result.answer, input);
      assert.deepEqual(validateFacts(result.answer, claims), { facts: input.facts, downgraded: [] });
    } else assert.equal(result.answer, null);
  }
});
