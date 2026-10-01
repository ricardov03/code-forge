/**
 * B25: `init` takes a 1Password item ID or item link for the Jev key (the question and
 * `--jev-ref`), resolves it with `op item get` (a fake exec here — `op` is never run) and writes the
 * full `op://<vaultId>/<itemId>/<fieldId>` reference. A 1Password failure never crashes the wizard:
 * interactive mode offers Try again / full reference / another source / skip; non-interactive mode
 * exits 2 with the error kind and writes nothing.
 */

import { baseEnv, freshDir, makeProject, runWizard, scriptedUi } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { parse as parseYAML } from 'yaml';
import { installsPath } from '../../src/install/link.mjs';
import { parseInitArgs, UsageError } from '../../src/install/wizard/flags.mjs';
import { askJevSource, OP_RESOLVE_FAILED } from '../../src/install/wizard/steps.mjs';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';

const ITEM_ID = 'abcdefghij0123456789klmnop';
const VAULT_ID = 'zyxwvutsrq9876543210ponmlk';
const RESOLVED = `op://${VAULT_ID}/${ITEM_ID}/credential`;
const FAKE_SECRET = 'sk-FAKE-op-item-b25-77aa88bb99cc';
const LINK = `https://start.1password.com/open/i?a=FAKEACCOUNT&v=${VAULT_ID}&i=${ITEM_ID}&h=my.1password.com`;
const ITEM_ARGV = ['op', 'item', 'get', ITEM_ID, '--format', 'json'];
const OK = {
  result: 'ok',
  code: 0,
  stdout: JSON.stringify({
    id: ITEM_ID,
    title: 'Jev API key',
    category: 'API_CREDENTIAL',
    vault: { id: VAULT_ID, name: 'Dev Keys' },
    fields: [
      { id: 'notesPlain', label: 'notesPlain', type: 'STRING', purpose: 'NOTES', reference: 'op://Dev Keys/Jev API key/notesPlain' },
      { id: 'credential', label: 'credential', type: 'CONCEALED', value: FAKE_SECRET, reference: 'op://Dev Keys/Jev API key/credential' },
    ],
  }),
};
const LOCKED = { result: 'failed', code: 1, stderr: `[ERROR] account is not signed in ${FAKE_SECRET}` };
const LOCKED_TEXT = '1Password is locked or not signed in; unlock the app (or run `op signin`) and try again';
const TIMEOUT = { result: 'failed', code: null, signal: 'SIGTERM', timedOut: true, stdout: FAKE_SECRET, stderr: FAKE_SECRET };

/** @param {Array<Record<string, unknown>>} script */
function fakeOp(script) {
  /** @type {string[][]} */
  const calls = [];
  const opExec = async (/** @type {string[]} */ argv) => {
    calls.push(argv);
    if (calls.length > script.length) throw new Error(`fakeOp: call ${calls.length} is past the script (${script.length})`);
    const step = script[calls.length - 1];
    return { result: 'failed', code: null, signal: null, stdout: '', stderr: '', timedOut: false, ...step };
  };
  return { opExec, calls };
}

/** @param {string[]} args @param {{ui?: any, opExec?: any, agent?: boolean, isTTY?: boolean}} opts */
async function run(args, { ui, opExec, agent = false, isTTY = true }) {
  const home = freshDir('home');
  const cwd = await makeProject(buildLaravelVue);
  const env = baseEnv(home, agent ? { CLAUDECODE: '1' } : {});
  const r = await runWizard(args, { cwd, home, env, isTTY, ui, opExec });
  const file = path.join(cwd, '.code-forge.yml');
  return { ...r, home, cwd, file, read: () => parseYAML(readFileSync(file, 'utf8')) };
}

/** @param {string} text */
const count = (text) => text.split(FAKE_SECRET).length - 1;
/** @param {{message: string}} c */
const firstLine = (c) => c.message.split('\n')[0];

test('interactive: picking 1Password and pasting an item ID writes the resolved op:// ref; the item title is printed, the value never', async () => {
  const { opExec, calls } = fakeOp([OK]);
  const ui = scriptedUi({ 'Where is the Jev key?': 'op', '1Password item ID': ITEM_ID });
  const r = await run([], { ui, opExec });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.read().keys.jev, RESOLVED);
  assert.deepEqual(calls, [ITEM_ARGV]);
  assert.equal(r.stdout.split('\n').filter((l) => l === 'keys: jev from 1Password item "Jev API key" (vault Dev Keys, field credential)').length, 1);
  assert.equal(count(r.stdout + r.stderr + readFileSync(r.file, 'utf8')), 0);
});

test('interactive: 1Password locked, then "Try again" succeeds — 2 op calls, the menu shows the message, ref written', async () => {
  const { opExec, calls } = fakeOp([LOCKED, OK]);
  const ui = scriptedUi({ 'Where is the Jev key?': 'op', '1Password item ID': [ITEM_ID, ITEM_ID], '1Password lookup failed': ['retry'] });
  const r = await run([], { ui, opExec });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(calls, [ITEM_ARGV, ITEM_ARGV]);
  const menus = ui.calls.filter((c) => firstLine(c) === '1Password lookup failed. What next?');
  assert.equal(menus.length, 1);
  assert.equal(menus[0].message, `1Password lookup failed. What next?\n${LOCKED_TEXT}`);
  assert.deepEqual(menus[0].options.map((o) => o.value), ['retry', 'full', 'other', 'skip']);
  assert.equal(menus[0].initialValue, 'retry');
  assert.equal(r.read().keys.jev, RESOLVED);
  assert.equal(count(r.stdout + r.stderr + JSON.stringify(ui.calls)), 0);
});

const LATER = 'keys: jev not set — set it later with: code-forge keys set jev --op <item-id>';

test('interactive: 1Password fails, "Skip for now" — exit 0, no keys.jev, the summary says how to set it later', async () => {
  const { opExec, calls } = fakeOp([LOCKED]);
  const ui = scriptedUi({ 'Where is the Jev key?': 'op', '1Password item ID': [ITEM_ID], '1Password lookup failed': ['skip'] });
  const r = await run([], { ui, opExec });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(calls, [ITEM_ARGV]);
  assert.equal(r.read().keys, undefined);
  assert.equal(r.stdout.split('\n').filter((l) => l === LATER).length, 1);
  assert.equal(count(r.stdout + r.stderr + readFileSync(r.file, 'utf8') + JSON.stringify(ui.calls)), 0);
});

test('interactive: the main-menu skip prints the same "set it later" line, once', async () => {
  const ui = scriptedUi({ 'Where is the Jev key?': 'skip' });
  const r = await run([], { ui, opExec: fakeOp([]).opExec });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.read().keys, undefined);
  assert.equal(r.stdout.split('\n').filter((l) => l === LATER).length, 1);
});

test('interactive: 1Password fails, "Choose another key source" goes back to the source question; env is taken', async () => {
  const { opExec, calls } = fakeOp([LOCKED]);
  const ui = scriptedUi({
    'Where is the Jev key?': ['op', 'env'],
    '1Password item ID': [ITEM_ID],
    '1Password lookup failed': ['other'],
    'Environment variable name': 'MY_JEV_KEY',
  });
  const r = await run([], { ui, opExec });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(calls.length, 1);
  assert.equal(ui.calls.filter((c) => firstLine(c) === 'Where is the Jev key?').length, 2);
  assert.equal(r.read().keys.jev, 'env:MY_JEV_KEY');
});

test('interactive: "Enter a full op:// reference" after a failure takes the reference with no further op call', async () => {
  const { opExec, calls } = fakeOp([LOCKED]);
  const ui = scriptedUi({
    'Where is the Jev key?': 'op',
    '1Password item ID': [ITEM_ID],
    '1Password lookup failed': ['full'],
    'Full op://vault/item/field reference': 'op://Dev/jev/credential',
  });
  const r = await run([], { ui, opExec });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(calls.length, 1);
  assert.equal(r.read().keys.jev, 'op://Dev/jev/credential');
});

test('--jev-ref <item link> in --no-interaction resolves the link\'s item and writes the resolved ref', async () => {
  const { opExec, calls } = fakeOp([OK]);
  const r = await run(['--no-interaction', '--jev-ref', LINK], { opExec, agent: true, isTTY: false });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(calls, [ITEM_ARGV]);
  assert.equal(r.read().keys.jev, RESOLVED);
  assert.equal(count(r.stdout + r.stderr + readFileSync(r.file, 'utf8')), 0);
});

test('--jev-ref <item-id> that fails in --no-interaction: exit 2, kind in the JSON line, nothing written', async () => {
  /** @type {Array<[Record<string, unknown>, string, string]>} */
  const cases = [
    [LOCKED, 'op_locked', LOCKED_TEXT],
    [{ result: 'failed', code: null, error: 'spawn op ENOENT' }, 'op_missing', '1Password CLI not found; install it with: brew install 1password-cli'],
    [{ result: 'failed', code: 1, stderr: `"${ITEM_ID}" isn't an item` }, 'op_not_found', `1Password item or field not found, or no access (item ${ITEM_ID})`],
    [{ result: 'ok', code: 0, stdout: `{"id":"${ITEM_ID}","fields":[{"value":"${FAKE_SECRET}"` }, 'op_bad_output', 'unexpected answer from op'],
  ];
  for (const [step, kind, text] of cases) {
    const { opExec } = fakeOp([step]);
    const r = await run(['--no-interaction', '--jev-ref', ITEM_ID, '--harness', 'claude', '-p'], { opExec, agent: true, isTTY: false });
    assert.equal(r.code, 2, String(kind));
    const lines = r.stdout.split('\n').filter((l) => l.length > 0);
    assert.equal(lines.length, 1);
    const json = JSON.parse(lines[0]);
    assert.deepEqual([json.ok, json.kind, json.error, json.wrote], [false, kind, `--jev-ref: ${text}. Nothing written.`, []]);
    assert.equal(existsSync(r.file), false);
    assert.equal(existsSync(installsPath(r.home)), false);
    assert.equal(existsSync(path.join(r.cwd, '.claude')), false);
    assert.equal(count(r.stdout + r.stderr), 0);
  }
});

test('--no-interaction: timeout then timeout -> 2 op calls, exit 2, kind op_timeout, nothing written', async () => {
  const { opExec, calls } = fakeOp([TIMEOUT, TIMEOUT]);
  const r = await run(['--no-interaction', '--jev-ref', ITEM_ID, '--harness', 'claude', '-p'], { opExec, agent: true, isTTY: false });
  assert.equal(r.code, 2);
  assert.deepEqual(calls, [ITEM_ARGV, ITEM_ARGV]);
  const json = JSON.parse(r.stdout.trim());
  assert.deepEqual([json.kind, json.error, json.wrote], ['op_timeout', '--jev-ref: 1Password did not answer in time; unlock the app and try again. Nothing written.', []]);
  assert.equal(existsSync(r.file), false);
  assert.equal(existsSync(installsPath(r.home)), false);
  assert.equal(count(r.stdout + r.stderr), 0);
});

test('--no-interaction: timeout then OK -> the resolved ref is written', async () => {
  const { opExec, calls } = fakeOp([TIMEOUT, OK]);
  const r = await run(['--no-interaction', '--jev-ref', ITEM_ID], { opExec, agent: true, isTTY: false });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(calls, [ITEM_ARGV, ITEM_ARGV]);
  assert.equal(r.read().keys.jev, RESOLVED);
});

test('--jev-ref op://… makes 0 op calls and keys.jev equals the input', async () => {
  const { opExec, calls } = fakeOp([]);
  const r = await run(['--no-interaction', '--jev-ref', 'op://Dev Keys/Jev API key/credential'], { opExec, agent: true, isTTY: false });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(calls.length, 0);
  assert.equal(r.read().keys.jev, 'op://Dev Keys/Jev API key/credential');
});

test('--jev-ref parsing: exact values for an op:// ref, an ID, a link and padded input; exact error for garbage', () => {
  assert.deepEqual(parseInitArgs(['--jev-ref', 'op://Dev/jev/credential']).given, [{ flag: '--jev-ref', value: 'op://Dev/jev/credential' }]);
  assert.deepEqual(parseInitArgs(['--jev-ref', ITEM_ID]).given, [{ flag: '--jev-ref', value: ITEM_ID }]);
  assert.deepEqual(parseInitArgs([`--jev-ref=${LINK}`]).given, [{ flag: '--jev-ref', value: LINK }]);
  assert.deepEqual(parseInitArgs(['--jev-ref', `  ${ITEM_ID} `]).given, [{ flag: '--jev-ref', value: ITEM_ID }]);
  assert.throws(() => parseInitArgs(['--jev-ref', 'my-jev-key']), (e) => e instanceof UsageError && e.message === '--jev-ref must be a 1Password item ID, item link or op://vault/item/field reference');
});

test('"full reference" mode: a non-op:// input shows the bad-input message with no op call; "Try again" goes back to the item-ID question', async () => {
  const { opExec, calls } = fakeOp([LOCKED, OK]);
  const ui = scriptedUi({
    'Where is the Jev key?': 'op',
    '1Password item ID': [ITEM_ID, ITEM_ID],
    '1Password lookup failed': ['full', 'retry'],
    'Full op://vault/item/field reference': [ITEM_ID],
  });
  const r = await run([], { ui, opExec });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(calls, [ITEM_ARGV, ITEM_ARGV]);
  const menus = ui.calls.filter((c) => firstLine(c) === '1Password lookup failed. What next?').map((c) => c.message.split('\n')[1]);
  assert.deepEqual(menus, [LOCKED_TEXT, 'not an op:// reference, 1Password item ID or item link']);
  assert.deepEqual(ui.calls.filter((c) => c.kind === 'text').map(firstLine), [
    '1Password item ID, item link, or op:// reference',
    'Full op://vault/item/field reference',
    '1Password item ID, item link, or op:// reference',
  ]);
  assert.equal(r.read().keys.jev, RESOLVED);
});

test('askJevSource: a resolver that answers a non-object is a failure with the fixed message; skip then returns no ref', async () => {
  const ui = scriptedUi({ 'Where is the Jev key?': 'op', '1Password item ID': ITEM_ID, '1Password lookup failed': ['skip'] });
  const resolve = /** @type {any} */ (async () => `oops ${FAKE_SECRET}`);
  const got = await askJevSource(ui, async () => { throw new Error('no store'); }, { resolve });
  assert.deepEqual(got, { ref: null, source: 'none' });
  const menu = ui.calls.filter((c) => firstLine(c) === '1Password lookup failed. What next?');
  assert.equal(menu.length, 1);
  assert.equal(menu[0].message, `1Password lookup failed. What next?\n${OP_RESOLVE_FAILED}`);
  assert.equal(count(JSON.stringify(ui.calls)), 0);
});

test('askJevSource without a resolver is a programming error', async () => {
  await assert.rejects(askJevSource(scriptedUi(), async () => ({ put: async () => undefined })), (e) => e instanceof TypeError && e.message === 'askJevSource: opts.resolve (the 1Password resolver) is required');
});
