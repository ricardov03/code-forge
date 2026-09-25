/**
 * B13a fix round 1: plain-PHP `copy_untracked`, Codex detected only by its session variables,
 * multimodel off drops the old second reviewer, Enter on a pre-filled level keeps `@provider`.
 */

import { FAKE_JEV_KEY, baseEnv, freshDir, makeProject, runWizard, scriptedUi } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { parse as parseYAML, stringify } from 'yaml';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';

const { proposeProof, currentHarness } = await import('../../src/install/wizard/answers.mjs');
const { askAnswers } = await import('../../src/install/wizard/steps.mjs');

test('plain PHP (no artisan) proposes copy_untracked [".env"]', () => {
  assert.deepEqual(proposeProof('php').copy_untracked, ['.env']);
});

test('CODEX_HOME alone is a person\'s shell, not Codex: no agent mode, no stop text', async () => {
  assert.deepEqual(currentHarness({ CODEX_HOME: '/x' }), { agent: false, harness: null });
  assert.deepEqual(currentHarness({ CODEX_SANDBOX_NETWORK_DISABLED: '1' }), { agent: true, harness: 'codex' });
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const r = await runWizard(['--no-interaction'], { cwd, home, env: baseEnv(home, { CODEX_HOME: '/x', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }), isTTY: true });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.stderr, '');
  assert.match(r.stdout, /^engine: auto$/m);
  assert.throws(() => JSON.parse(r.stdout));
});

test('re-run with multimodel off removes review.second_provider and review.second_levels', async () => {
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const env = baseEnv(home, { CLAUDECODE: '1', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  const first = await runWizard(['--no-interaction'], { cwd, home, env });
  assert.equal(first.code, 0, first.stderr);
  const file = path.join(cwd, '.code-forge.yml');
  // an existing file with a second reviewer, written by hand
  const cfg = parseYAML(readFileSync(file, 'utf8'));
  cfg.review = { multimodel: true, second_provider: 'openai', second_levels: { L2: { model: 'gpt-6-sol', effort: 'high', provider: 'openai' } } };
  writeFileSync(file, stringify(cfg));
  const again = await runWizard(['--no-interaction', '--multimodel', 'off'], { cwd, home, env });
  assert.equal(again.code, 0, again.stderr);
  assert.deepEqual(parseYAML(readFileSync(file, 'utf8')).review, { multimodel: false });
});

test('declining "keep the matrix" and pressing Enter on every level keeps the levels, @provider included', async () => {
  const levels = {
    L0: { model: 'claude-haiku-4-5-20251001' },
    L1: { model: 'claude-sonnet-5', effort: 'high' },
    L2: { model: 'gpt-6-sol', effort: 'high', provider: 'openai' },
    L3: { model: 'claude-fable-5-1' },
  };
  const values = /** @type {any} */ ({ levels: structuredClone(levels) });
  const sources = Object.fromEntries(
    ['tools', 'harnesses', 'scope', 'method', 'provider', 'refresh_models', 'multimodel', 'second_provider', 'engine', 'solo_project', 'gates', 'proof'].map((k) => [k, 'flag']),
  );
  const ui = scriptedUi({ 'Keep the level matrix?': false });
  await askAnswers(values, sources, /** @type {any} */ ({ detectedHarnesses: [], solo: false }), ui);
  assert.deepEqual(ui.calls.filter((c) => c.kind === 'text').map((c) => c.message.slice(0, 2)), ['L0', 'L1', 'L2', 'L3']);
  assert.deepEqual(values.levels, levels);
});
