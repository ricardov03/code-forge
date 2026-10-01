/**
 * B13a acceptance, end to end through `runInit` (temp HOME, temp fixture copy, scratch env):
 * the documented Laravel+Vue config, agent-mode JSON, the 0-byte re-run, `subprocess` never
 * proposed, the §5.1 stop text, `copy_untracked` per stack, and the hidden key paste.
 */

import { FAKE_JEV_KEY, HERMETIC_TOOLS, SKILL_SOURCE, baseEnv, freshDir, makeProject, runWizard, scriptedUi } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readdirSync, readFileSync, readlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { parse as parseYAML } from 'yaml';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';
import { build as buildNode } from '../fixtures/repos/node/build.mjs';
import { build as buildPython } from '../fixtures/repos/python/build.mjs';

const { validateConfig } = await import('../../src/config/validate.mjs');
const { createDefaultKeyStore, resolveKey } = await import('../../src/keys/store.mjs');

const HERE = path.dirname(fileURLToPath(import.meta.url));
const EXPECTED = parseYAML(readFileSync(path.join(HERE, '..', 'fixtures', 'repos', 'laravel-vue', 'expected.code-forge.yml'), 'utf8'));

/** §5.1's stop text, typed out here (not imported) so the test is an independent oracle. */
const STOP_TEXT =
  'code-forge: no Solo and no subagent tool in this harness.\n' +
  '  To run coders as detached CLI processes (power-user mode) add to .code-forge.yml:\n' +
  '    engine: subprocess\n' +
  '  Then re-run. (R2: this engine is never selected automatically.)\n';

/** @param {string} haystack @param {string} needle @returns {number} */
function count(haystack, needle) {
  return haystack.split(needle).length - 1;
}

/**
 * @param {string[]} [args] @param {Record<string, string>} [extraEnv] @param {Record<string, any>} [opts]
 */
async function laravelRun(args = ['--no-interaction'], extraEnv = { CLAUDECODE: '1', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }, opts = {}) {
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const env = baseEnv(home, extraEnv);
  const res = await runWizard(args, { cwd, home, env, ...opts });
  return { ...res, home, cwd, env, file: path.join(cwd, '.code-forge.yml') };
}

test('non-interactive run on the Laravel+Vue fixture writes the documented config (real doctor --quick, key never written)', async () => {
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const env = baseEnv(home, { CLAUDECODE: '1', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  const { runInit } = await import('../../src/install/wizard/run.mjs');
  const out = [];
  const err = [];
  const code = await runInit(['--no-interaction'], {
    cwd, home, env, isTTY: false, doctor: undefined, skillSource: SKILL_SOURCE, ...HERMETIC_TOOLS,
    stdout: { write: (s) => out.push(s) }, stderr: { write: (s) => err.push(s) },
  });
  assert.equal(code, 0, err.join(''));
  const text = readFileSync(path.join(cwd, '.code-forge.yml'), 'utf8');
  const written = parseYAML(text);
  assert.deepEqual(written, EXPECTED);
  // B24: no .env or .env.testing in the fixture, so none is copied; link_dirs follows the two
  // manifests although node_modules is not installed; no high-risk paths
  assert.deepEqual(written.proof.export.copy_untracked, []);
  assert.deepEqual(written.proof.export.link_dirs, ['vendor', 'node_modules']);
  assert.deepEqual(written.proof.tiers.high.paths, []);
  const checked = validateConfig(written);
  assert.equal(checked.errors.length, 0);
  assert.equal(readlinkSync(path.join(cwd, '.claude', 'skills', 'code-forge')), SKILL_SOURCE);
  const json = JSON.parse(out.join(''));
  assert.deepEqual(json.doctor, { ok: true, counts: { OK: 3, WARN: 0, FAIL: 0, INFO: 0 } });
  const log = readFileSync(json.log, 'utf8');
  // the key resolved from the env is referenced, never written or printed
  for (const where of [text, out.join(''), err.join(''), log]) assert.equal(count(where, FAKE_JEV_KEY), 0);
});

test('no Jev key and no key flag, non-interactive ⇒ exit 2 naming keys.jev, nothing written', async () => {
  const r = await laravelRun(['--no-interaction'], { CLAUDECODE: '1' });
  assert.equal(r.code, 2);
  assert.equal(r.stderr, 'init: missing required answers, nothing written:\n  keys.jev (pass --jev-ref, --jev-env or --no-jev)\n');
  assert.equal(existsSync(r.file), false);
  // agent mode: the failure is still exactly one JSON line
  const lines = r.stdout.split('\n').filter((l) => l.length > 0);
  assert.equal(lines.length, 1);
  const json = JSON.parse(lines[0]);
  assert.deepEqual(Object.keys(json), ['ok', 'error', 'wrote', 'log', 'log_tail']);
  assert.equal(json.ok, false);
  assert.deepEqual(json.wrote, []);
  assert.equal(existsSync(json.log), true);
});

test('agent mode ⇒ exactly 1 JSON line on stdout, with a log path that exists', async () => {
  const r = await laravelRun();
  assert.equal(r.code, 0);
  const lines = r.stdout.split('\n').filter((l) => l.length > 0);
  assert.equal(lines.length, 1);
  const json = JSON.parse(lines[0]);
  assert.deepEqual(Object.keys(json), ['ok', 'wrote', 'harnesses', 'engine_stop', 'doctor', 'settings', 'blank', 'log', 'log_tail']);
  assert.equal(path.dirname(json.log), path.join(r.home, '.code-forge', 'logs'));
  assert.equal(existsSync(json.log), true);
  assert.deepEqual(json.harnesses, ['claude']);
});

test('a person at a terminal with --no-interaction gets text lines, not JSON', async () => {
  const r = await laravelRun(['--no-interaction'], { CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }, { isTTY: true });
  assert.equal(r.code, 0);
  assert.throws(() => JSON.parse(r.stdout));
  assert.match(r.stdout, /^log: .*init-.*\.log$/m);
});

test('a second run changes the file by 0 bytes and does not rewrite it', async () => {
  const r = await laravelRun();
  const before = readFileSync(r.file);
  const again = await runWizard(['--no-interaction'], { cwd: r.cwd, home: r.home, env: r.env });
  assert.equal(again.code, 0);
  assert.equal(Buffer.compare(readFileSync(r.file), before), 0);
  assert.equal(JSON.parse(again.stdout).wrote.includes(r.file), false);
});

test('a re-run with one flag changes exactly that key and keeps a hand-added key', async () => {
  const r = await laravelRun();
  const edited = `${readFileSync(r.file, 'utf8')}telemetry: off\n`;
  writeFileSync(r.file, edited);
  const again = await runWizard(['--no-interaction', '--engine', 'harness'], { cwd: r.cwd, home: r.home, env: r.env });
  assert.equal(again.code, 0);
  const next = parseYAML(readFileSync(r.file, 'utf8'));
  assert.deepEqual(next, { ...EXPECTED, engine: 'harness', telemetry: 'off' });
});

test('subprocess is never proposed: the engine question offers auto/solo/harness only', async () => {
  const ui = scriptedUi();
  const r = await laravelRun([], { CODE_FORGE_KEY_JEV: FAKE_JEV_KEY }, { isTTY: true, ui });
  assert.equal(r.code, 0, r.stderr);
  const engine = ui.calls.filter((c) => c.message.split('\n')[0] === 'Engine');
  assert.equal(engine.length, 1);
  assert.deepEqual(engine[0].options.map((o) => o.value), ['auto', 'solo', 'harness']);
  assert.equal(ui.calls.flatMap((c) => c.options ?? []).filter((o) => o.value === 'subprocess').length, 0);
  assert.equal(parseYAML(readFileSync(r.file, 'utf8')).engine, 'auto');
});

test('under a harness with no subagent tool and no Solo the wizard prints the §5.1 stop text', async () => {
  const codex = await laravelRun(['--no-interaction'], { CODEX_SANDBOX: 'seatbelt', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  assert.equal(codex.stderr, STOP_TEXT);
  assert.equal(JSON.parse(codex.stdout).engine_stop, true);
  const claude = await laravelRun();
  assert.equal(count(claude.stderr, 'no subagent tool'), 0);
  const withSolo = await laravelRun(['--no-interaction'], { CODEX_SANDBOX: 'seatbelt', SOLO_MCP_PATH: '/fake/solo', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  assert.equal(count(withSolo.stderr, 'no subagent tool'), 0);
});

/** @type {Array<[string, (dir: string) => Promise<void>, string[]]>} */
const STACKS = [
  ['Laravel', buildLaravelVue, ['.env', '.env.testing']],
  ['Node', buildNode, ['.env', '.env.test']],
  ['Python', buildPython, ['.env']],
];
for (const [stack, build, expected] of STACKS) {
  test(`copy_untracked proposed for ${stack} when present: ${expected.join(', ')}; [] when absent`, async () => {
    const home = freshDir('home');
    const cwd = await makeProject(build);
    // the stack's candidates exist, plus one file no stack proposes
    for (const f of [...expected, '.env.local']) writeFileSync(path.join(cwd, f), 'FAKE=1\n');
    const r = await runWizard(['--no-interaction', '--no-jev'], { cwd, home, env: baseEnv(home, { CLAUDECODE: '1' }) });
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(parseYAML(readFileSync(path.join(cwd, '.code-forge.yml'), 'utf8')).proof.export.copy_untracked, expected);
    // the same stack with none of them on disk proposes none
    const bare = await makeProject(build);
    const r2 = await runWizard(['--no-interaction', '--no-jev'], { cwd: bare, home, env: baseEnv(home, { CLAUDECODE: '1' }) });
    assert.equal(r2.code, 0, r2.stderr);
    assert.deepEqual(parseYAML(readFileSync(path.join(bare, '.code-forge.yml'), 'utf8')).proof.export.copy_untracked, []);
  });
}

test('a key pasted at the hidden prompt goes to the store only: config holds "user", 0 echoes', async () => {
  const ui = scriptedUi({ 'Where is the Jev key?': 'paste', 'Jev key': FAKE_JEV_KEY });
  const r = await laravelRun([], {}, { isTTY: true, ui });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(ui.calls.filter((c) => c.kind === 'password').map((c) => c.message.split('\n')[0]), ['Jev key (input hidden)']);
  const text = readFileSync(r.file, 'utf8');
  assert.equal(parseYAML(text).keys.jev, 'user');
  const logs = path.join(r.home, '.code-forge', 'logs');
  const logText = readFileSync(path.join(logs, readdirSync(logs)[0]), 'utf8');
  const userCfg = readFileSync(path.join(r.home, '.code-forge', 'config.yml'), 'utf8');
  for (const where of [text, r.stdout, r.stderr, logText, userCfg]) assert.equal(count(where, FAKE_JEV_KEY), 0);
  const store = await createDefaultKeyStore(r.env);
  const res = await resolveKey('jev', { store, env: r.env, ref: 'user' });
  assert.equal(res.value, FAKE_JEV_KEY);
});

test('B19 (§5.5): caps.coders is 1 when engine auto resolves to the harness (no Solo), 2 with Solo, and 1 for an explicit harness engine even with Solo', async () => {
  const noSolo = await laravelRun();
  assert.equal(noSolo.code, 0, noSolo.stderr);
  const withSolo = await laravelRun(['--no-interaction'], { CLAUDECODE: '1', SOLO_MCP_PATH: '/fake/solo', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  assert.equal(withSolo.code, 0, withSolo.stderr);
  const harness = await laravelRun(['--no-interaction', '--engine', 'harness'], { CLAUDECODE: '1', SOLO_MCP_PATH: '/fake/solo', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  assert.equal(harness.code, 0, harness.stderr);
  // control: the with-Solo run minus SOLO_MCP_PATH
  const control = await laravelRun(['--no-interaction'], { CLAUDECODE: '1', CODE_FORGE_KEY_JEV: FAKE_JEV_KEY });
  assert.equal(control.code, 0, control.stderr);
  const read = (/** @type {string} */ file) => parseYAML(readFileSync(file, 'utf8'));
  assert.deepEqual(
    [noSolo, withSolo, harness, control].map((r) => [read(r.file).engine, read(r.file).caps.coders]),
    [['auto', 1], ['auto', 2], ['harness', 1], ['auto', 1]],
  );
});
