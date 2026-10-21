import './support.mjs';
import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import * as gateCli from '../../src/cli/gates.mjs';
import { validateConfig } from '../../src/config/validate.mjs';
import { runGates } from '../../src/gates/run.mjs';
import { captureStream, withTempDir } from './support.mjs';

test('configured timeout resolves in seconds, flag wins, and killed fixed/custom gates report timeout', async () => {
  await withTempDir(async (cwd) => {
    const argv = [process.execPath, '-e', 'setTimeout(() => {}, 10000)'];
    const ran = await runGates({ test: argv, custom: [{ name: 'build', argv }] }, { cwd, timeoutMs: 1000 });
    assert.equal(ran.allOk, false);
    const executed = ran.results.filter((result) => !result.skipped);
    assert.equal(executed.length, 2);
    for (const result of executed) {
      assert.equal(result.ok, false, result.gate);
      assert.equal(result.reason, 'timeout', result.gate);
      assert.match(result.stderr, /timeout.*1 seconds.*gates\.timeout_s/i);
    }
    const { gateTimeoutMs } = gateCli;
    assert.equal(gateTimeoutMs({ config: { gates: { timeout_s: 30 } } }), 30000);
    assert.equal(gateTimeoutMs({ flag: 1200, config: { gates: { timeout_s: 30 } } }), 1200);
    assert.equal(gateTimeoutMs({ config: {} }), 300000);
    assert.equal(gateTimeoutMs({ config: { gates: { timeout_s: 7200 } } }), 7200000);
  });
});

test('timeout validation refuses out-of-range/non-integer values and gates run refuses timeout_s: 5', async () => {
  const config = {
    version: 1, provider: 'anthropic',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' },
    },
  };
  for (const timeout_s of [5, 29, 7201, 30.5, '30', null]) {
    const result = validateConfig({ ...config, gates: { timeout_s } });
    assert.equal(result.valid, false, `invalid timeout_s=${timeout_s} must fail validation`);
    assert.ok(result.errors.some((issue) => issue.rule === 'schema' && issue.path === '/gates/timeout_s'),
      `invalid timeout_s=${timeout_s} must fail schema validation`);
    assert.deepEqual(result.errors.filter((issue) => issue.rule === 'gates-not-argv-array').map((issue) => issue.message),
      ['gates.timeout_s must be an integer 30..7200'], `timeout_s=${timeout_s}`);
  }
  for (const timeout_s of [30, 300, 7200]) {
    const result = validateConfig({ ...config, gates: { timeout_s } });
    assert.deepEqual(result.errors, [], `valid timeout_s=${timeout_s} must pass both schema and domain validation`);
    assert.equal(result.valid, true);
  }
  await withTempDir(async (cwd) => {
    await writeFile(path.join(cwd, '.code-forge.yml'), 'version: 1\ngates:\n  timeout_s: 5\n');
    const stdout = captureStream(), stderr = captureStream();
    assert.equal(await gateCli.runGatesVerb(['run', '--cwd', cwd], { stdout, stderr }), 1);
    assert.equal(stdout.text, '', 'invalid timeout must prevent any gate result');
    assert.match(stderr.text, /gates\.timeout_s must be an integer 30\.\.7200/);
  });
});
