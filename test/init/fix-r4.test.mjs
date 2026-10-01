/**
 * B13a fix round 4: every failure after the flags parsed is one JSON line in agent mode with the
 * log written (also after the config is on disk), and the Jev source is recorded as the store
 * reports it. (The brace-glob proof list test went with the proof questions in B24.)
 */

import { FAKE_JEV_KEY, SKILL_SOURCE, baseEnv, freshDir, makeProject, sink } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { parse as parseYAML } from 'yaml';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';

const { runInit } = await import('../../src/install/wizard/run.mjs');

/**
 * `runInit` in agent mode on a fresh Laravel fixture, with extra deps (an installer, a store).
 * @param {string[]} args @param {Record<string, string>} extraEnv @param {Record<string, any>} [deps]
 */
async function agentRun(args, extraEnv, deps = {}) {
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const env = baseEnv(home, { CLAUDECODE: '1', ...extraEnv });
  const stdout = sink();
  const stderr = sink();
  const doctor = async () => [{ status: 'OK', label: 'config', detail: 'valid' }];
  const code = await runInit(args, { cwd, home, env, isTTY: false, doctor, stdout, stderr, skillSource: SKILL_SOURCE, ...deps });
  return { code, stdout: stdout.text(), stderr: stderr.text(), home, cwd, env, file: path.join(cwd, '.code-forge.yml') };
}

/** @param {string} stdout @returns {any} the one JSON line, asserted to be the only one */
function oneJsonLine(stdout) {
  const lines = stdout.split('\n').filter((l) => l.length > 0);
  assert.equal(lines.length, 1, stdout);
  return JSON.parse(lines[0]);
}

// ── 1. failures after parsing: one JSON line, log written ──────────────────

test('agent mode: a second-provider collision is 1 JSON line {ok:false, error, wrote, log, log_tail} and the log exists', async () => {
  const r = await agentRun(['--no-interaction', '--multimodel', 'on', '--second-provider', 'anthropic'], { CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  assert.equal(r.code, 2);
  const json = oneJsonLine(r.stdout);
  assert.deepEqual(Object.keys(json), ['ok', 'error', 'wrote', 'log', 'log_tail']);
  assert.equal(json.ok, false);
  assert.equal(json.error, 'review.second_provider equals the effective L2 provider (anthropic) — multimodel needs two providers; pass --second-provider with another one. Nothing written.');
  assert.deepEqual(json.wrote, []);
  assert.equal(existsSync(json.log), true);
  assert.equal(json.log_tail.at(-1), `error: ${json.error}`);
  assert.equal(existsSync(r.file), false);
});

test('agent mode: a missing answer (no second provider) is 1 JSON line with ok:false and the log exists', async () => {
  const r = await agentRun(['--no-interaction', '--multimodel', 'on'], { CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  assert.equal(r.code, 2);
  const json = oneJsonLine(r.stdout);
  assert.equal(json.ok, false);
  assert.equal(json.error, 'missing required answers, nothing written:\n  review.second_provider (pass --second-provider)');
  assert.equal(readFileSync(json.log, 'utf8').split('\n').filter((l) => l.startsWith('error: ')).length, 1);
  assert.equal(existsSync(r.file), false);
});

test('agent mode: an unloadable .code-forge.yml is 1 JSON line with ok:false naming the file', async () => {
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const { writeFileSync } = await import('node:fs');
  writeFileSync(path.join(cwd, '.code-forge.yml'), 'version: [\n');
  const stdout = sink();
  const code = await runInit(['--no-interaction'], { cwd, home, env: baseEnv(home, { CLAUDECODE: '1', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }), isTTY: false, stdout, stderr: sink(), skillSource: SKILL_SOURCE });
  assert.equal(code, 1);
  const json = oneJsonLine(stdout.text());
  assert.equal(json.ok, false);
  assert.match(json.error, /^\.code-forge\.yml exists but cannot be loaded/);
  assert.equal(existsSync(json.log), true);
});

// ── 2. a throw after the config is written ─────────────────────────────────

test('an installer that throws after the config is written: 1 JSON line, ok:false, wrote lists the config, log exists', async () => {
  const installTool = async () => {
    throw new Error('npm exploded');
  };
  const r = await agentRun(['--no-interaction', '--tools', 'recommended', '--yes-tool', 'codex'], { CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }, { installTool });
  assert.equal(r.code, 1);
  const json = oneJsonLine(r.stdout);
  assert.deepEqual(Object.keys(json), ['ok', 'error', 'wrote', 'log', 'log_tail']);
  assert.equal(json.ok, false);
  assert.equal(json.error, 'unexpected error: npm exploded');
  assert.deepEqual(json.wrote, [r.file]);
  assert.equal(existsSync(r.file), true);
  assert.equal(existsSync(json.log), true);
  assert.equal(r.stderr, 'init: unexpected error: npm exploded\n');
});

// ── 3. the Jev source is what the store reports ────────────────────────────

test('jev auto mode with a store that holds a 1Password-cached key: config keys.jev is "user", user-level source is "op"', async () => {
  const store = {
    read: async () => ({ value: FAKE_JEV_KEY, backend: 'op', exp: null }),
    put: async () => undefined,
  };
  const r = await agentRun(['--no-interaction'], {}, { getStore: async () => store });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(parseYAML(readFileSync(r.file, 'utf8')).keys.jev, 'user');
  const userCfg = parseYAML(readFileSync(path.join(r.home, '.code-forge', 'config.yml'), 'utf8'));
  assert.equal(userCfg.keys.jev.source, 'op');
  const json = oneJsonLine(r.stdout);
  assert.equal(readFileSync(json.log, 'utf8').split('\n').filter((l) => l === 'keys: jev from op').length, 1);
  for (const where of [r.stdout, r.stderr, readFileSync(json.log, 'utf8'), readFileSync(r.file, 'utf8')]) assert.equal(where.split(FAKE_JEV_KEY).length - 1, 0);
  // the file backend is recorded as "file", the env as "env" with its env: reference
  const file = await agentRun(['--no-interaction'], {}, { getStore: async () => ({ ...store, read: async () => ({ value: FAKE_JEV_KEY, backend: 'file', exp: null }) }) });
  assert.equal(file.code, 0, file.stderr);
  assert.equal(parseYAML(readFileSync(path.join(file.home, '.code-forge', 'config.yml'), 'utf8')).keys.jev.source, 'file');
  const fromEnv = await agentRun(['--no-interaction'], { CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }, { getStore: async () => store });
  assert.equal(parseYAML(readFileSync(fromEnv.file, 'utf8')).keys.jev, 'env:CODE_FORGE_KEY_JEV');
  assert.equal(parseYAML(readFileSync(path.join(fromEnv.home, '.code-forge', 'config.yml'), 'utf8')).keys.jev.source, 'env');
});
