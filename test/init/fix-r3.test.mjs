/**
 * B13a fix round 3: Laravel on PHPUnit is Laravel, `keys.jev` and `system1.fallback` never
 * coexist after a re-run, the second provider is checked against the EFFECTIVE L2 provider, and
 * the consensus judge (FABLE DECISION): the wizard proposes the third provider's default L3 when
 * the effective L3 shares a provider with a reviewer; a colliding `--level L3` is refused.
 */

import { FAKE_JEV_KEY, baseEnv, freshDir, makeProject, runWizard, scriptedUi } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { parse as parseYAML } from 'yaml';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';

const { proofProfile, judgeCollision, proposeJudge } = await import('../../src/install/wizard/answers.mjs');
const { mergeConfig } = await import('../../src/install/wizard/project-file.mjs');
const { askAnswers } = await import('../../src/install/wizard/steps.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');
const { detectGates } = await import('../../src/gates/detect.mjs');

const XAI_JUDGE = { model: 'grok-4.7', effort: 'xhigh', provider: 'xai' };

/**
 * A Laravel app on PHPUnit: `composer.json` + `artisan` + `vendor/bin/phpunit`, no Pest, and the
 * Vite `package.json` a Laravel app ships (no `test` script).
 * @param {string} dir
 */
async function buildLaravelPhpunit(dir) {
  await writeFile(path.join(dir, 'composer.json'), JSON.stringify({ name: 'fixture/laravel-phpunit', require: { php: '^8.2' } }, null, 2));
  await writeFile(path.join(dir, 'artisan'), '#!/usr/bin/env php\n<?php\n// fixture artisan\n');
  await mkdir(path.join(dir, 'vendor', 'bin'), { recursive: true });
  await writeFile(path.join(dir, 'vendor', 'bin', 'phpunit'), '#!/usr/bin/env php\n<?php\n// fixture phpunit binary\n');
  await writeFile(path.join(dir, 'package.json'), JSON.stringify({ private: true, scripts: { dev: 'vite', build: 'vite build' } }, null, 2));
  // B24: env files are proposed only when present (link_dirs follows the manifests instead)
  await writeFile(path.join(dir, '.env'), 'APP_KEY=FAKE\n');
  await writeFile(path.join(dir, '.env.testing'), 'APP_KEY=FAKE\n');
}

/** @param {string[]} args @param {Record<string, string>} [extraEnv] @param {Record<string, any>} [opts] */
async function laravelRun(args, extraEnv = { CLAUDECODE: '1', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }, opts = {}) {
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const env = baseEnv(home, extraEnv);
  const res = await runWizard(args, { cwd, home, env, ...opts });
  return { ...res, home, cwd, env, file: path.join(cwd, '.code-forge.yml') };
}

// ── 1. Laravel on PHPUnit ───────────────────────────────────────────────────

test('a Laravel app on PHPUnit (stack is not php-pest) gets copy_untracked [".env", ".env.testing"]', async () => {
  const cwd = await makeProject(buildLaravelPhpunit);
  // the gate detector does not know this project as PHP at all — the profile must not depend on it
  assert.equal(detectGates(cwd).stack, 'unknown');
  assert.equal(proofProfile('unknown', cwd), 'laravel');
  assert.equal(proofProfile('node', cwd), 'laravel');
  const home = freshDir('home');
  const r = await runWizard(['--no-interaction', '--no-jev'], { cwd, home, env: baseEnv(home, { CLAUDECODE: '1' }) });
  assert.equal(r.code, 0, r.stderr);
  const written = parseYAML(readFileSync(path.join(cwd, '.code-forge.yml'), 'utf8'));
  assert.deepEqual(written.proof.export.copy_untracked, ['.env', '.env.testing']);
  assert.deepEqual(written.proof.export.link_dirs, ['vendor', 'node_modules']);
});

test('PHP without artisan is php on any php-* stack; composer.json alone is php; a plain node project is node', async () => {
  const php = freshDir('php');
  await writeFile(path.join(php, 'composer.json'), '{}');
  assert.equal(proofProfile('php-pest', php), 'php');
  assert.equal(proofProfile('php-phpunit', php), 'php');
  assert.equal(proofProfile('unknown', php), 'php');
  const node = freshDir('node');
  assert.equal(proofProfile('node', node), 'node');
  assert.equal(proofProfile('unknown', node), 'unknown');
});

// ── 2. keys.jev vs system1.fallback on re-run ───────────────────────────────

test('mergeConfig: a key after a keyless run removes system1.fallback (and an empty system1)', () => {
  const existing = { version: 1, system1: { fallback: 'rules' }, engine: 'auto' };
  const generated = { version: 1, keys: { jev: 'env:CODE_FORGE_KEY_JEV' }, engine: 'auto' };
  const merged = mergeConfig(existing, generated, { dropJev: false });
  assert.deepEqual(merged, { version: 1, engine: 'auto', keys: { jev: 'env:CODE_FORGE_KEY_JEV' } });
  // other system1 keys survive; only the fallback goes
  const kept = mergeConfig({ version: 1, system1: { fallback: 'rules', disable: ['defect'] } }, generated, { dropJev: false });
  assert.deepEqual(kept.system1, { disable: ['defect'] });
});

test('mergeConfig: no key after a keyed run removes keys.jev even when dropJev is false', () => {
  const existing = { version: 1, keys: { jev: 'env:CODE_FORGE_KEY_JEV', other: 'env:X' }, engine: 'auto' };
  const generated = { version: 1, system1: { fallback: 'rules' }, engine: 'auto' };
  const merged = mergeConfig(existing, generated, { dropJev: false });
  assert.deepEqual(merged, { version: 1, keys: { other: 'env:X' }, engine: 'auto', system1: { fallback: 'rules' } });
  const gone = mergeConfig({ version: 1, keys: { jev: 'user' } }, generated, { dropJev: false });
  assert.equal(Object.hasOwn(gone, 'keys'), false);
  // the caller's `generated` is never mutated, with or without an existing document
  assert.deepEqual(generated, { version: 1, system1: { fallback: 'rules' }, engine: 'auto' });
  assert.notEqual(mergeConfig(null, generated, { dropJev: false }), generated);
});

test('end to end: --no-jev then a key ⇒ keys.jev and no system1; a key then --no-jev ⇒ system1.fallback and no keys', async () => {
  const first = await laravelRun(['--no-interaction', '--no-jev'], { CLAUDECODE: '1' });
  assert.equal(first.code, 0, first.stderr);
  assert.deepEqual(parseYAML(readFileSync(first.file, 'utf8')).system1, { fallback: 'rules' });
  const keyed = await runWizard(['--no-interaction', '--jev-env', 'CODE_FORGE_KEY_JEV'], { cwd: first.cwd, home: first.home, env: first.env });
  assert.equal(keyed.code, 0, keyed.stderr);
  const afterKey = parseYAML(readFileSync(first.file, 'utf8'));
  assert.deepEqual(afterKey.keys, { jev: 'env:CODE_FORGE_KEY_JEV' });
  assert.equal(Object.hasOwn(afterKey, 'system1'), false);
  const keyless = await runWizard(['--no-interaction', '--no-jev'], { cwd: first.cwd, home: first.home, env: first.env });
  assert.equal(keyless.code, 0, keyless.stderr);
  const afterDrop = parseYAML(readFileSync(first.file, 'utf8'));
  assert.deepEqual(afterDrop.system1, { fallback: 'rules' });
  assert.equal(Object.hasOwn(afterDrop, 'keys'), false);
});

// ── 3. second provider vs the EFFECTIVE L2 provider ────────────────────────

test('interactive: with L2 @openai the second-provider list excludes openai (not anthropic) and the judge moves to xai', async () => {
  const ui = scriptedUi({ 'Multimodel review (consensus)?': true });
  const r = await laravelRun(['--level', 'L2=gpt-6-sol:high@openai'], { CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }, { isTTY: true, ui });
  assert.equal(r.code, 0, r.stderr);
  const second = ui.calls.filter((c) => c.message.startsWith('Second provider'));
  assert.equal(second.length, 1);
  assert.deepEqual(second[0].options.map((o) => o.value), ['anthropic', 'xai']);
  assert.equal(second[0].message.split('\n')[0], 'Second provider (must differ from the effective L2 provider, openai)');
  // reviewers are now openai (L2) and anthropic (second); the default L3 is anthropic ⇒ judge question, pre-filled with xai
  const judge = ui.calls.filter((c) => c.message.startsWith('L3 judge'));
  assert.equal(judge.length, 1);
  assert.equal(judge[0].message.split('\n')[0], 'L3 judge — must come from a third provider (reviewers: openai, anthropic); model[:effort][@provider]');
  const written = parseYAML(readFileSync(r.file, 'utf8'));
  assert.deepEqual(written.levels.L2, { model: 'gpt-6-sol', effort: 'high', provider: 'openai' });
  assert.deepEqual(written.levels.L3, XAI_JUDGE);
  assert.equal(written.review.second_provider, 'anthropic');
  assert.equal(validateConfig(written).errors.length, 0);
});

test('interactive: a judge typed on a reviewer\'s provider is asked again until it differs', async () => {
  const values = /** @type {any} */ ({
    provider: 'anthropic',
    levels: { L0: { model: 'a' }, L1: { model: 'b' }, L2: { model: 'c' }, L3: { model: 'claude-fable-5-1' } },
    multimodel: true,
    second_provider: 'openai',
  });
  const sources = Object.fromEntries(
    ['tools', 'harnesses', 'scope', 'method', 'provider', 'levels', 'refresh_models', 'multimodel', 'second_provider', 'engine', 'solo_project', 'gates', 'proof'].map((k) => [k, 'flag']),
  );
  let asked = 0;
  const ui = scriptedUi();
  const text = ui.text;
  ui.text = async (o) => {
    asked += 1;
    return asked === 1 ? 'gpt-6-astra:high@openai' : text(o);
  };
  await askAnswers(values, sources, /** @type {any} */ ({ detectedHarnesses: [], solo: false }), ui);
  assert.equal(asked, 2);
  assert.deepEqual(values.levels.L3, XAI_JUDGE);
});

test('non-interactive: --second-provider equal to the effective L2 provider is refused, nothing written', async () => {
  const r = await laravelRun(['--no-interaction', '--level', 'L2=gpt-6-sol:high@openai', '--multimodel', 'on', '--second-provider', 'openai']);
  assert.equal(r.code, 2);
  assert.equal(r.stderr, 'init: review.second_provider equals the effective L2 provider (openai) — multimodel needs two providers; pass --second-provider with another one. Nothing written.\n');
  assert.equal(existsSync(r.file), false);
});

// ── FABLE DECISION: the consensus judge comes from the third provider ───────

test('judgeCollision / proposeJudge: the third provider is determined once both reviewers are known', () => {
  const base = { provider: 'anthropic', levels: { L2: { model: 'x' }, L3: { model: 'y' } }, multimodel: true, second_provider: 'openai' };
  assert.deepEqual(judgeCollision(base), { judge: 'anthropic', reviewers: ['anthropic', 'openai'], third: 'xai' });
  assert.equal(judgeCollision({ ...base, multimodel: false }), null);
  assert.equal(judgeCollision({ ...base, second_provider: null }), null);
  assert.equal(judgeCollision({ ...base, levels: { L2: { model: 'x' }, L3: { model: 'y', provider: 'xai' } } }), null);
  assert.deepEqual(proposeJudge('xai'), XAI_JUDGE);
  assert.deepEqual(proposeJudge('openai'), { model: 'gpt-6-astra', effort: 'high', provider: 'openai' });
});

test('non-interactive --multimodel on --second-provider openai from the anthropic defaults writes a valid config with an xai judge', async () => {
  const r = await laravelRun(['--no-interaction', '--multimodel', 'on', '--second-provider', 'openai']);
  assert.equal(r.code, 0, r.stderr);
  const written = parseYAML(readFileSync(r.file, 'utf8'));
  assert.deepEqual(written.levels.L3, XAI_JUDGE);
  assert.deepEqual(written.levels.L2, { model: 'claude-opus-5-5' });
  assert.equal(written.review.multimodel, true);
  assert.equal(written.review.second_provider, 'openai');
  assert.deepEqual(validateConfig(written).errors, []);
  const json = JSON.parse(r.stdout);
  const log = readFileSync(json.log, 'utf8');
  assert.equal(log.split('\n').filter((l) => l === 'levels.L3: judge proposed from xai (consensus needs a third provider; reviewers anthropic, openai)').length, 1);
  // a second run is byte-idempotent: the judge no longer collides, so nothing moves
  const before = readFileSync(r.file);
  const again = await runWizard(['--no-interaction'], { cwd: r.cwd, home: r.home, env: r.env });
  assert.equal(again.code, 0, again.stderr);
  assert.equal(Buffer.compare(readFileSync(r.file), before), 0);
});

test('non-interactive: a --level L3 pinned to a reviewer\'s provider is refused with the third provider named, nothing written', async () => {
  const r = await laravelRun(['--no-interaction', '--level', 'L3=claude-fable-5-1', '--multimodel', 'on', '--second-provider', 'openai']);
  assert.equal(r.code, 2);
  assert.equal(r.stderr, "init: --level L3 pins the judge to anthropic, a reviewer's provider — the consensus judge must come from a third provider (xai); change or drop --level L3. Nothing written.\n");
  assert.equal(existsSync(r.file), false);
  // pinned to the third provider it is accepted as typed
  const ok = await laravelRun(['--no-interaction', '--level', 'L3=grok-4.7:high@xai', '--multimodel', 'on', '--second-provider', 'openai']);
  assert.equal(ok.code, 0, ok.stderr);
  assert.deepEqual(parseYAML(readFileSync(ok.file, 'utf8')).levels.L3, { model: 'grok-4.7', effort: 'high', provider: 'xai' });
});
