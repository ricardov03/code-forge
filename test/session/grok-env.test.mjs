import { cfgWith, fakeDeps, freshDir, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { chmodSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { spawnSession } = await import('../../src/session/spawn.mjs');

test('Grok coder and reviewer children on PATH disable both compat MCP sources despite caller opt-ins', async () => {
  const dir = freshDir('grok-env');
  const fake = writeIn(dir, 'grok', `#!/usr/bin/env node
import { writeFileSync } from 'node:fs';
writeFileSync(process.env.FAKE_ENV_OUTPUT, JSON.stringify({
  GROK_CURSOR_MCPS_ENABLED: process.env.GROK_CURSOR_MCPS_ENABLED,
  GROK_CLAUDE_MCPS_ENABLED: process.env.GROK_CLAUDE_MCPS_ENABLED,
}));
process.stdout.write(JSON.stringify({ result: JSON.stringify({ ok: true }) }) + '\\n');
`);
  chmodSync(fake, 0o755);
  const packet = writeIn(dir, 'packet.md', 'check the child environment');
  const actual = [];
  for (const role of /** @type {const} */ (['coder', 'reviewer'])) {
    const out = path.join(dir, `${role}-env.json`);
    const { deps } = fakeDeps({
      PATH: `${dir}${path.delimiter}${process.env.PATH ?? ''}`,
      FAKE_ENV_OUTPUT: out,
      GROK_CURSOR_MCPS_ENABLED: 'true',
      GROK_CLAUDE_MCPS_ENABLED: 'true',
    });
    const result = await spawnSession({
      cfg: cfgWith({ provider: 'xai', model: 'fake-grok' }),
      level: 'L1', role, promptPath: packet, cwd: freshDir('grok-cwd'),
    }, { ...deps, bins: {} });
    assert.equal(result.status, 'ok', `${role} must complete using the fake grok on PATH`);
    actual.push({ role, env: JSON.parse(readFileSync(out, 'utf8')) });
  }
  assert.deepEqual(actual, ['coder', 'reviewer'].map((role) => ({
    role,
    env: { GROK_CURSOR_MCPS_ENABLED: 'false', GROK_CLAUDE_MCPS_ENABLED: 'false' },
  })), 'both children must receive exactly two false MCP switches, overriding both caller opt-ins');
});
