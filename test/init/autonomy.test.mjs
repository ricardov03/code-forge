/**
 * B65 (issue #13 item 7): safer push and draft-PR defaults. `autonomy.coder_may_push` and
 * `autonomy.coder_may_open_draft_pr` default to false; `init` asks both (default no; the draft-PR
 * question only after a push yes), takes `--coder-may-push` / `--coder-may-open-draft-pr`, refuses a
 * draft PR without push, pre-fills a re-run from the existing file, and always writes both keys.
 * Runs the real wizard (scripted ui) and the real CLI, each in a temp project with a temp HOME.
 */

import { baseEnv, freshDir, makeProject, runWizard, scriptedUi } from './helpers.mjs';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { parse as parseYAML, stringify } from 'yaml';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';

const { configSchema, validateConfig } = await import('../../src/config/validate.mjs');
const { loadConfigFile } = await import('../../src/config/load.mjs');
const { resolveSchemaPath } = await import('../../src/config/schema-paths.mjs');

const BIN = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');

const PUSH_Q = 'May a coder push its block branch on its own?';
const DRAFT_Q = 'May a coder open a draft PR on its own?';
const PUSH_HINT = 'No: the coder hands back its branch and you (or the orchestrator) push it.';
const DRAFT_HINT = 'Only after an independent review passes; marking it ready and merging stay with you.';
const SETTINGS_Q = 'Gates and proof settings:';
const REFUSED = '--coder-may-open-draft-pr yes needs autonomy.coder_may_push yes — a draft PR needs a push; pass --coder-may-push yes or drop the flag. Nothing written.';

/** Flags that answer every pre-summary question except the two autonomy ones. */
const OTHER_ANSWERS = [
  '--no-jev', '--tools', 'current', '--harness', 'claude', '-p', '--copy', '--provider', 'anthropic',
  '--level', 'L0=claude-haiku-4-5-20251001', '--refresh-models', '--multimodel', 'off', '--engine', 'auto',
];

/** @param {{message: string}} c @returns {string} */
const firstLine = (c) => c.message.split('\n')[0];

/**
 * @param {string[]} args
 * @param {{ui?: any, isTTY?: boolean, agent?: boolean, cwd?: string, home?: string}} [opts]
 */
async function run(args, { ui, isTTY = true, agent = false, cwd, home } = {}) {
  const h = home ?? freshDir('home');
  const dir = cwd ?? (await makeProject(buildLaravelVue));
  const env = baseEnv(h, agent ? { CLAUDECODE: '1' } : {});
  const r = await runWizard(args, { cwd: dir, home: h, env, isTTY, ui });
  const file = path.join(dir, '.code-forge.yml');
  return { ...r, home: h, cwd: dir, file, read: () => parseYAML(readFileSync(file, 'utf8')) };
}

/**
 * The real CLI, non-interactive, with an env built from scratch (temp HOME, an empty PATH dir).
 * @param {string[]} args @param {string} cwd @param {string} home
 * @returns {Promise<{code: number, stdout: string, stderr: string}>}
 */
function cli(args, cwd, home) {
  const env = { HOME: home, PATH: freshDir('emptybin'), CODE_FORGE_KEY_BACKEND: 'file', CODE_FORGE_NO_ERROR_LOG: '1', CLAUDECODE: '1' };
  return new Promise((resolve) => {
    execFile(process.execPath, [BIN, 'init', ...args], { cwd, env }, (error, stdout, stderr) =>
      resolve({ code: error ? (typeof error.code === 'number' ? error.code : -1) : 0, stdout, stderr }),
    );
  });
}

test('schema: both keys default to false; a config without autonomy is valid; explicit values load and validate unchanged', async () => {
  assert.equal(resolveSchemaPath(configSchema, 'autonomy.coder_may_push')?.default, false);
  assert.equal(resolveSchemaPath(configSchema, 'autonomy.coder_may_open_draft_pr')?.default, false);
  const base = await run(['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(base.code, 0, base.stderr);
  const { autonomy: _drop, ...without } = base.read();
  assert.equal(validateConfig(without).valid, true);
  for (const autonomy of [{ coder_may_push: true, coder_may_open_draft_pr: true }, { coder_may_push: true, coder_may_open_draft_pr: false }]) {
    writeFileSync(base.file, stringify({ ...without, autonomy }));
    const loaded = await loadConfigFile(base.file);
    assert.equal(loaded.ok, true);
    assert.deepEqual(loaded.config.autonomy, autonomy);
    assert.equal(validateConfig(loaded.config).valid, true);
  }
});

/** @type {Array<[string, Record<string, unknown>, string[], {coder_may_push: boolean, coder_may_open_draft_pr: boolean}]>} */
const INTERACTIVE = [
  ['yes / yes', { 'May a coder push': true, 'May a coder open a draft PR': true }, [PUSH_Q, DRAFT_Q, SETTINGS_Q], { coder_may_push: true, coder_may_open_draft_pr: true }],
  ['yes / no', { 'May a coder push': true, 'May a coder open a draft PR': false }, [PUSH_Q, DRAFT_Q, SETTINGS_Q], { coder_may_push: true, coder_may_open_draft_pr: false }],
  ['no (draft-PR question not asked)', { 'May a coder push': false }, [PUSH_Q, SETTINGS_Q], { coder_may_push: false, coder_may_open_draft_pr: false }],
  ['Enter on both defaults', {}, [PUSH_Q, SETTINGS_Q], { coder_may_push: false, coder_may_open_draft_pr: false }],
];
for (const [label, replies, asked, expected] of INTERACTIVE) {
  test(`interactive ${label}: the exact questions asked and the exact autonomy written`, async () => {
    const ui = scriptedUi(replies);
    const r = await run(OTHER_ANSWERS, { ui });
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(ui.calls.map(firstLine), asked);
    const autonomyCalls = ui.calls.filter((c) => c.kind === 'confirm');
    assert.deepEqual(autonomyCalls.map((c) => c.message), asked.includes(DRAFT_Q) ? [`${PUSH_Q}\n${PUSH_HINT}`, `${DRAFT_Q}\n${DRAFT_HINT}`] : [`${PUSH_Q}\n${PUSH_HINT}`]);
    // default no on a fresh project
    assert.deepEqual(autonomyCalls.map((c) => c.initialValue), asked.includes(DRAFT_Q) ? [false, false] : [false]);
    assert.deepEqual(r.read().autonomy, expected);
  });
}

/** @type {Array<[string[], {coder_may_push: boolean, coder_may_open_draft_pr: boolean}]>} */
const FLAGGED = [
  [[], { coder_may_push: false, coder_may_open_draft_pr: false }],
  [['--coder-may-push', 'yes'], { coder_may_push: true, coder_may_open_draft_pr: false }],
  [['--coder-may-push', 'yes', '--coder-may-open-draft-pr', 'yes'], { coder_may_push: true, coder_may_open_draft_pr: true }],
  [['--coder-may-push', 'no', '--coder-may-open-draft-pr', 'no'], { coder_may_push: false, coder_may_open_draft_pr: false }],
];
for (const [flags, expected] of FLAGGED) {
  test(`non-interactive ${flags.join(' ') || '(no autonomy flag)'} writes ${JSON.stringify(expected)}`, async () => {
    const r = await run(['--no-interaction', '--no-jev', ...flags], { isTTY: false, agent: true });
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(r.read().autonomy, expected);
  });
}

test('interactive: a flag answers its question, which is then not asked', async () => {
  const ui = scriptedUi();
  const r = await run([...OTHER_ANSWERS, '--coder-may-push', 'yes'], { ui });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(ui.calls.map(firstLine), [DRAFT_Q, SETTINGS_Q]);
  assert.deepEqual(r.read().autonomy, { coder_may_push: true, coder_may_open_draft_pr: false });
});

test('refused: --coder-may-open-draft-pr yes with push no ⇒ exit 2, one JSON error line, nothing written', async () => {
  const r = await run(['--no-interaction', '--no-jev', '--coder-may-open-draft-pr', 'yes'], { isTTY: false, agent: true });
  assert.equal(r.code, 2);
  assert.equal(r.stderr, `init: ${REFUSED}\n`);
  const lines = r.stdout.split('\n').filter((l) => l.length > 0);
  assert.equal(lines.length, 1);
  const json = JSON.parse(lines[0]);
  assert.deepEqual([json.ok, json.error, json.wrote], [false, REFUSED, []]);
  assert.equal(existsSync(r.file), false);
  // the same with push answered no at the prompt
  const ui = scriptedUi({ 'May a coder push': false });
  const asked = await run([...OTHER_ANSWERS, '--coder-may-open-draft-pr', 'yes'], { ui });
  assert.equal(asked.code, 2);
  assert.equal(asked.stderr, `init: ${REFUSED}\n`);
  assert.deepEqual(ui.calls.map(firstLine), [PUSH_Q]);
  assert.equal(existsSync(asked.file), false);
});

test('refused: an autonomy flag value other than yes or no is a usage error (exit 2, nothing written)', async () => {
  for (const flag of ['--coder-may-push', '--coder-may-open-draft-pr']) {
    const r = await run(['--no-interaction', '--no-jev', flag, 'true'], { isTTY: false, agent: true });
    assert.equal(r.code, 2);
    assert.equal(r.stderr.split('\n')[0], `init: ${flag} must be yes or no`);
    assert.equal(existsSync(r.file), false);
  }
});

test('re-run pre-fills both questions from the existing autonomy values; Enter keeps the file byte for byte', async () => {
  const first = await run(['--no-interaction', '--no-jev', '--coder-may-push', 'yes', '--coder-may-open-draft-pr', 'yes'], { isTTY: false, agent: true });
  assert.equal(first.code, 0, first.stderr);
  const before = readFileSync(first.file);
  const ui = scriptedUi();
  const again = await run(OTHER_ANSWERS.filter((a) => a !== '--copy'), { ui, cwd: first.cwd, home: first.home });
  assert.equal(again.code, 0, again.stderr);
  const confirms = ui.calls.filter((c) => c.kind === 'confirm');
  assert.deepEqual(confirms.map((c) => [firstLine(c), c.initialValue]), [[PUSH_Q, true], [DRAFT_Q, true]]);
  assert.equal(Buffer.compare(readFileSync(first.file), before), 0);
  // non-interactive re-run with no flag keeps them too
  const quiet = await run(['--no-interaction', '--no-jev'], { isTTY: false, agent: true, cwd: first.cwd, home: first.home });
  assert.equal(quiet.code, 0, quiet.stderr);
  assert.deepEqual(quiet.read().autonomy, { coder_may_push: true, coder_may_open_draft_pr: true });
});

test('re-run on a file without autonomy proposes no and writes both keys false', async () => {
  const first = await run(['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(first.code, 0, first.stderr);
  const { autonomy: _drop, ...without } = first.read();
  writeFileSync(first.file, stringify(without));
  const ui = scriptedUi();
  const again = await run(OTHER_ANSWERS, { ui, cwd: first.cwd, home: first.home });
  assert.equal(again.code, 0, again.stderr);
  const confirms = ui.calls.filter((c) => firstLine(c) === PUSH_Q || firstLine(c) === DRAFT_Q);
  assert.deepEqual(confirms.map((c) => [firstLine(c), c.initialValue]), [[PUSH_Q, false]]);
  assert.deepEqual(again.read().autonomy, { coder_may_push: false, coder_may_open_draft_pr: false });
});

test('re-run on a file with push false and draft PR true: Enter writes both false, the draft-PR question is not asked; the draft-PR flag is refused and the file kept', async () => {
  const first = await run(['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(first.code, 0, first.stderr);
  const odd = stringify({ ...first.read(), autonomy: { coder_may_push: false, coder_may_open_draft_pr: true } });
  writeFileSync(first.file, odd);
  const refused = await run(['--no-interaction', '--no-jev', '--coder-may-open-draft-pr', 'yes'], { isTTY: false, agent: true, cwd: first.cwd, home: first.home });
  assert.equal(refused.code, 2);
  assert.equal(refused.stderr, `init: ${REFUSED}\n`);
  assert.equal(readFileSync(first.file, 'utf8'), odd);
  const ui = scriptedUi();
  const again = await run(OTHER_ANSWERS, { ui, cwd: first.cwd, home: first.home });
  assert.equal(again.code, 0, again.stderr);
  // the draft-PR question is not asked; the one change (draft PR to false) is confirmed before writing
  assert.deepEqual(ui.calls.map(firstLine), [PUSH_Q, SETTINGS_Q, 'Apply 1 change(s) to .code-forge.yml?']);
  assert.deepEqual(ui.calls.filter((c) => firstLine(c) === PUSH_Q).map((c) => c.initialValue), [false]);
  assert.deepEqual(again.read().autonomy, { coder_may_push: false, coder_may_open_draft_pr: false });
});

test('re-run on a file with both true: --coder-may-push no writes both false and is not refused', async () => {
  const first = await run(['--no-interaction', '--no-jev', '--coder-may-push', 'yes', '--coder-may-open-draft-pr', 'yes'], { isTTY: false, agent: true });
  assert.equal(first.code, 0, first.stderr);
  const again = await run(['--no-interaction', '--no-jev', '--coder-may-push', 'no'], { isTTY: false, agent: true, cwd: first.cwd, home: first.home });
  assert.equal(again.code, 0, again.stderr);
  assert.deepEqual(again.read().autonomy, { coder_may_push: false, coder_may_open_draft_pr: false });
});

test('the real CLI: flags write the autonomy mapping; no flag writes both false; draft without push is refused', async () => {
  const home = freshDir('home');
  const yes = await makeProject(buildLaravelVue);
  const r1 = await cli(['--no-interaction', '--no-jev', '--skip-doctor', '--coder-may-push', 'yes', '--coder-may-open-draft-pr', 'yes'], yes, home);
  assert.equal(r1.code, 0, r1.stderr);
  assert.deepEqual(parseYAML(readFileSync(path.join(yes, '.code-forge.yml'), 'utf8')).autonomy, { coder_may_push: true, coder_may_open_draft_pr: true });
  const none = await makeProject(buildLaravelVue);
  const r2 = await cli(['--no-interaction', '--no-jev', '--skip-doctor'], none, home);
  assert.equal(r2.code, 0, r2.stderr);
  assert.deepEqual(parseYAML(readFileSync(path.join(none, '.code-forge.yml'), 'utf8')).autonomy, { coder_may_push: false, coder_may_open_draft_pr: false });
  const bad = await makeProject(buildLaravelVue);
  const r3 = await cli(['--no-interaction', '--no-jev', '--skip-doctor', '--coder-may-open-draft-pr', 'yes'], bad, home);
  assert.equal(r3.code, 2);
  assert.equal(r3.stderr, `init: ${REFUSED}\n`);
  assert.equal(JSON.parse(r3.stdout.trim()).error, REFUSED);
  assert.equal(existsSync(path.join(bad, '.code-forge.yml')), false);
});
