/**
 * B26: init step 1 uses the shared tool table (`src/install/tools.mjs`), so it now offers
 * `claude` too and installs only after a per-tool yes. The installer is a fake (never run).
 *
 * PATH here is deliberately limited to ONE temp directory holding a single empty executable
 * file named `npm` (never run): no real tool is ever found, so every row is "missing" and the
 * npm rows get an install command. The platform is fixed to linux (grok/op → vendor links) and
 * the Solo app check is a fake that finds nothing — the result never depends on this machine.
 */

import { FAKE_JEV_KEY, SKILL_SOURCE, baseEnv, freshDir, makeProject, okDoctor, sink } from './helpers.mjs';
import assert from 'node:assert/strict';
import { chmodSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';

const { runInit } = await import('../../src/install/wizard/run.mjs');

/**
 * @param {string[]} args
 * @param {(argv: string[]) => Promise<any>} installTool
 * @returns {Promise<{code: number, toolLines: string[]}>}
 */
async function initWithTools(args, installTool) {
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const bin = freshDir('npmbin');
  writeFileSync(path.join(bin, 'npm'), '');
  chmodSync(path.join(bin, 'npm'), 0o755);
  const env = baseEnv(home, { CLAUDECODE: '1', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY, PATH: bin });
  const stdout = sink();
  const code = await runInit(args, {
    cwd, home, env, isTTY: false, doctor: okDoctor, stdout, stderr: sink(), skillSource: SKILL_SOURCE, installTool, platform: 'linux', toolExists: () => false,
  });
  const toolLines = readFileSync(JSON.parse(stdout.text()).log, 'utf8').split('\n').filter((l) => l.startsWith('tool '));
  return { code, toolLines };
}

const MISSING_REST = [
  'tool codex: missing — install with: npm install -g @openai/codex (or: code-forge tools install)',
  'tool gemini: missing — install with: npm install -g @google/gemini-cli (or: code-forge tools install)',
  'tool grok: missing — https://x.ai/build (or: code-forge tools install)',
  'tool op: missing — https://developer.1password.com/docs/cli/get-started/ (or: code-forge tools install)',
  'tool solo: missing — https://soloterm.com (desktop app, then add its MCP entry) (or: code-forge tools install)',
];

test('init step 1 offers the claude row from the shared table and installs it with its exact argv after --yes-tool claude', async () => {
  /** @type {string[][]} */
  const installs = [];
  const r = await initWithTools(['--no-interaction', '--tools', 'recommended', '--yes-tool', 'claude'], async (argv) => (installs.push(argv), { result: 'ok' }));
  assert.equal(r.code, 0);
  assert.deepEqual(installs, [['npm', 'install', '-g', '@anthropic-ai/claude-code']]);
  assert.deepEqual(r.toolLines, ['tool claude: installed (npm install -g @anthropic-ai/claude-code)', ...MISSING_REST]);
});

test('init step 1 without --yes-tool (non-TTY) installs nothing and prints the exact claude line', async () => {
  /** @type {string[][]} */
  const installs = [];
  const r = await initWithTools(['--no-interaction', '--tools', 'recommended'], async (argv) => (installs.push(argv), { result: 'ok' }));
  assert.equal(r.code, 0);
  assert.deepEqual(installs, []);
  assert.deepEqual(r.toolLines, ['tool claude: missing — install with: npm install -g @anthropic-ai/claude-code (or: code-forge tools install)', ...MISSING_REST]);
});

test('init step 1: a failed install names the reason (exit n)', async () => {
  const r = await initWithTools(['--no-interaction', '--tools', 'recommended', '--yes-tool', 'claude'], async () => ({ result: 'failed', code: 7, signal: null, timedOut: false }));
  assert.equal(r.toolLines[0], 'tool claude: install failed (exit 7) (npm install -g @anthropic-ai/claude-code)');
});
