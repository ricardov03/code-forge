/**
 * B13a acceptance: "each flag overrides exactly one answer". After Q16 = cut there are 19 answer
 * flags (`--no-nightly` is gone); each one, given alone, marks exactly one answer as `flag` and
 * sets it to the expected value.
 */

import { PARENT, baseEnv, freshDir, makeProject } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';

const { ANSWER_FLAGS, parseInitArgs, UsageError } = await import('../../src/install/wizard/flags.mjs');
const { gatherContext, resolveAnswers } = await import('../../src/install/wizard/answers.mjs');

void PARENT;
const home = freshDir('home');
const cwd = await makeProject(buildLaravelVue);
const ctx = await gatherContext({ cwd, home, env: baseEnv(home, { CLAUDECODE: '1' }) });

/** flag argv → [answer key, expected value] @type {Array<[string[], string, unknown]>} */
const CASES = [
  [['--tools', 'recommended'], 'tools', 'recommended'],
  [['--yes-tool', 'codex'], 'yes_tools', ['codex']],
  [['--harness', 'codex,grok'], 'harnesses', ['codex', 'grok']],
  [['-g'], 'scope', 'global'],
  [['-p'], 'scope', 'project'],
  [['--copy'], 'method', 'copy'],
  [['--provider', 'openai'], 'provider', 'openai'],
  [['--level', 'L1=claude-opus-5-5:high'], 'levels', {
    L0: { model: 'claude-haiku-4-5-20251001' },
    L1: { model: 'claude-opus-5-5', effort: 'high' },
    L2: { model: 'claude-opus-5-5' },
    L3: { model: 'claude-fable-5-1' },
  }],
  [['--refresh-models'], 'refresh_models', true],
  [['--multimodel', 'on'], 'multimodel', true],
  [['--second-provider', 'xai'], 'second_provider', 'xai'],
  [['--jev-ref', 'op://Dev/jev/credential'], 'jev', { mode: 'ref', ref: 'op://Dev/jev/credential' }],
  [['--jev-env', 'MY_JEV'], 'jev', { mode: 'ref', ref: 'env:MY_JEV' }],
  [['--no-jev'], 'jev', { mode: 'none' }],
  [['--engine', 'harness'], 'engine', 'harness'],
  [['--solo-project', '7'], 'solo_project', 7],
  [['--gate', 'lint=vendor/bin/phpcs --standard="PSR 12"'], 'gates', {
    test: ['php', 'artisan', 'test', '--compact'],
    lint: ['vendor/bin/phpcs', '--standard=PSR 12'],
    types: ['vendor/bin/phpstan'],
    format: ['vendor/bin/pint', '--test'],
  }],
  [['--proof', 'isolation=lock'], 'proof', {
    high: ['database/migrations/**', 'app/Policies/**', 'app/Http/Middleware/**', '**/*Money*', '**/*Webhook*'],
    isolation: 'lock',
    link_dirs: ['vendor', 'node_modules'],
    copy_untracked: ['.env', '.env.testing'],
  }],
  [['--skip-doctor'], 'doctor', false],
];

test('19 answer flags after Q16 (no --no-nightly), each with one case below', () => {
  assert.equal(Object.keys(ANSWER_FLAGS).length, 19);
  assert.equal(CASES.length, 19);
  assert.deepEqual(CASES.map((c) => c[0][0]).sort(), Object.keys(ANSWER_FLAGS).sort());
});

for (const [argv, key, expected] of CASES) {
  test(`${argv[0]} overrides exactly one answer: ${key}`, () => {
    const { values, sources } = resolveAnswers(ctx, null, null, parseInitArgs(argv).given);
    assert.deepEqual(Object.keys(sources).filter((k) => sources[k] === 'flag'), [key]);
    assert.deepEqual(values[key], expected);
  });
}

test('refused: --no-nightly (cut with Q16), --engine subprocess, two Jev flags, unknown --proof key', () => {
  for (const argv of [['--no-nightly'], ['--engine', 'subprocess'], ['--no-jev', '--jev-env', 'X'], ['--proof', 'tool=infection']]) {
    assert.throws(() => parseInitArgs(argv), UsageError, argv.join(' '));
  }
});
