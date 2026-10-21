import assert from 'node:assert/strict';
import { test } from 'node:test';
import * as grok from '../../../src/engines/builders/grok.mjs';

const ROLES = /** @type {const} */ (['coder', 'reviewer', 'judge', 's2', 'author', 'facts', 'delegate']);
const EXPECTED_ENV = {
  GROK_CURSOR_MCPS_ENABLED: 'false',
  GROK_CLAUDE_MCPS_ENABLED: 'false',
};

test('Grok disables both compat MCP sources for all seven roles', () => {
  const actual = ROLES.map((role) => ({
    role,
    env: grok.buildGrokArgv({ role, model: 'grok-4.7', promptPath: '/packet.md', cwd: '/project' }).env,
  }));
  assert.deepEqual(actual, ROLES.map((role) => ({ role, env: EXPECTED_ENV })), 'each of the seven roles must return exactly two false MCP switches');
});

test('the exported Grok MCP policy is frozen against re-enabling either source', () => {
  const policy = grok.GROK_NO_COMPAT_MCP_ENV;
  assert.deepEqual(policy, EXPECTED_ENV, 'the exported policy must contain exactly two false MCP switches');
  assert.equal(Object.isFrozen(policy), true, 'the exported policy must be frozen');
  for (const key of Object.keys(EXPECTED_ENV)) {
    assert.throws(() => { policy[key] = 'true'; }, TypeError, `${key} cannot be re-enabled`);
  }
  assert.deepEqual(
    grok.buildGrokArgv({ role: 'reviewer', model: 'grok-4.7', promptPath: '/packet.md', cwd: '/project' }).env,
    EXPECTED_ENV,
    'a subsequent build must keep both MCP sources disabled',
  );
});
