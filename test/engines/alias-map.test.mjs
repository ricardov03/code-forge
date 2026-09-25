import assert from 'node:assert/strict';
import { test } from 'node:test';
import { resolveClaudeAlias, toAgentToolDispatch } from '../../src/engines/alias-map.mjs';

test('resolveClaudeAlias maps each known family prefix to its Agent-tool alias', () => {
  assert.equal(resolveClaudeAlias('claude-haiku-4-5-20251001'), 'haiku');
  assert.equal(resolveClaudeAlias('claude-sonnet-5'), 'sonnet');
  assert.equal(resolveClaudeAlias('claude-opus-5-5'), 'opus');
  assert.equal(resolveClaudeAlias('claude-fable-5-1'), 'fable');
  // A bare exact family name (no trailing version) matches too.
  assert.equal(resolveClaudeAlias('claude-opus'), 'opus');
});

test('resolveClaudeAlias refuses an unknown family (does not guess)', () => {
  assert.throws(() => resolveClaudeAlias('claude-nova-9'), { name: 'Error', message: /no known family alias/ });
  assert.throws(() => resolveClaudeAlias('gpt-6-astra'), /no known family alias/);
});

// Fix round 1 (MINOR): the prefix check now requires a word boundary — `startsWith(prefix)` alone
// used to accept a typo'd id like `claude-opusx` because it starts with `claude-opus`.
test('resolveClaudeAlias refuses a WORD-BOUNDARY violation: a typo that merely starts with a known family prefix', () => {
  assert.throws(() => resolveClaudeAlias('claude-opusx'), /no known family alias/);
  assert.throws(() => resolveClaudeAlias('claude-sonnetfoo-5'), /no known family alias/);
  assert.throws(() => resolveClaudeAlias('claude-hai'), /no known family alias/); // prefix of "haiku", not the family itself
});

test('resolveClaudeAlias refuses a known family word in an unexpected position (never a loose includes()/regex match)', () => {
  assert.throws(() => resolveClaudeAlias('gpt-opus-5'), /no known family alias/);
  assert.throws(() => resolveClaudeAlias('opus'), /no known family alias/); // bare alias word, not a real model id
  assert.throws(() => resolveClaudeAlias('x-claude-opus'), /no known family alias/); // "opus" is not at the START
});

test('resolveClaudeAlias throws a TypeError for a non-string / empty model', () => {
  assert.throws(() => resolveClaudeAlias(''), TypeError);
  assert.throws(() => resolveClaudeAlias(/** @type {any} */ (undefined)), TypeError);
});

test('toAgentToolDispatch always records "effort_effective": null (the plan\'s exact snake_case field name), regardless of the requested effort', () => {
  const withEffort = toAgentToolDispatch({ model: 'claude-opus-5-5', effort: 'high' });
  const withoutEffort = toAgentToolDispatch({ model: 'claude-opus-5-5' });
  assert.deepEqual(withEffort, { subagentType: 'general-purpose', model: 'opus', effort_effective: null });
  assert.deepEqual(withoutEffort, { subagentType: 'general-purpose', model: 'opus', effort_effective: null });
  assert.equal('effort_effective' in withEffort, true);
  assert.equal('effortEffective' in withEffort, false); // the OLD camelCase key must be gone entirely
});

test('toAgentToolDispatch propagates the unknown-family refusal (refuses, not a silent default alias)', () => {
  assert.throws(() => toAgentToolDispatch({ model: 'claude-nova-9' }), /no known family alias/);
});

// Fix round 1 (MINOR): a missing/undefined params object used to crash with a generic
// property-access TypeError before resolveClaudeAlias's own clear error ever ran.
test('toAgentToolDispatch with a missing/undefined params object reaches resolveClaudeAlias\'s clear error, not a generic crash', () => {
  // Fix round 3 (MINOR): pin the MESSAGE — a generic destructuring TypeError ("Cannot destructure
  // property 'model' of 'undefined'") is also a TypeError and would pass a class-only check.
  const expected = { name: 'TypeError', message: 'resolveClaudeAlias: model must be a non-empty string' };
  assert.throws(() => toAgentToolDispatch(/** @type {any} */ (undefined)), expected);
  assert.throws(() => toAgentToolDispatch(), expected);
  assert.throws(() => toAgentToolDispatch(/** @type {any} */ ({})), expected); // no model at all
});
