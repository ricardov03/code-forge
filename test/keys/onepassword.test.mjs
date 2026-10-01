import { countOccurrences, FAKE_OP_KEY } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { describeOpItem, isOpItemId, isOpRef, OP_TIMEOUT_MS, opItemIdFromInput, opRead, resolveOpItemRef, toOpRef } from '../../src/keys/onepassword.mjs';

const REF = 'op://Private/jev/credential';

/**
 * A scripted stand-in for B0's `exec`. `op` is never run.
 * @param {Array<Record<string, unknown>>} script - partial `ExecResult`s, one per call.
 */
function fakeExec(script) {
  /** @type {Array<{argv: string[], opts: any}>} */
  const invocations = [];
  const exec = async (/** @type {string[]} */ argv, /** @type {any} */ opts) => {
    invocations.push({ argv, opts });
    const step = script[invocations.length - 1] ?? { result: 'failed', code: 99, timedOut: false };
    return { result: 'failed', code: null, signal: null, stdout: '', stderr: '', timedOut: false, ...step };
  };
  return { exec: /** @type {any} */ (exec), invocations };
}

const TIMEOUT = { result: 'failed', code: null, signal: 'SIGTERM', timedOut: true, stdout: FAKE_OP_KEY, stderr: FAKE_OP_KEY };
const OK = { result: 'ok', code: 0, stdout: `${FAKE_OP_KEY}\n` };

test('the op timeout is 20 s', () => {
  assert.equal(OP_TIMEOUT_MS, 20_000);
});

test('a first-call timeout is retried exactly once with the same argv and timeout: 2 invocations', async () => {
  const { exec, invocations } = fakeExec([TIMEOUT, OK]);
  const res = await opRead(REF, { exec });
  assert.equal(invocations.length, 2);
  assert.deepEqual(res, { value: FAKE_OP_KEY, attempts: 2 });
  for (const call of invocations) {
    assert.deepEqual(call.argv, ['op', 'read', REF]);
    assert.equal(call.opts.timeoutMs, OP_TIMEOUT_MS);
  }
});

test('two timeouts in a row stop at 2 invocations — never a third — and the error carries no output', async () => {
  const { exec, invocations } = fakeExec([TIMEOUT, TIMEOUT, OK]);
  const res = await opRead(REF, { exec });
  assert.equal(invocations.length, 2);
  assert.deepEqual(res, { value: null, attempts: 2, error: '1Password did not answer in time; unlock the app and try again', kind: 'op_timeout' });
});

test('a non-timeout failure is not retried: 1 invocation, exact message without stdout/stderr', async () => {
  const { exec, invocations } = fakeExec([{ result: 'failed', code: 1, stdout: FAKE_OP_KEY, stderr: `[ERROR] You are not currently signed in. ${FAKE_OP_KEY}` }, OK]);
  const res = await opRead(REF, { exec });
  assert.equal(invocations.length, 1);
  assert.deepEqual(res, { value: null, attempts: 1, error: '1Password is locked or not signed in; unlock the app (or run `op signin`) and try again', kind: 'op_locked' });
  assert.equal(countOccurrences(JSON.stringify(res), FAKE_OP_KEY), 0);
});

test('sign-in wording wins over not-found wording: "not signed in … item not found" is op_locked', async () => {
  const stderr = 'account is not signed in; item not found';
  const { exec } = fakeExec([{ result: 'failed', code: 1, stderr }]);
  const res = await opRead(REF, { exec });
  assert.deepEqual(res, { value: null, attempts: 1, kind: 'op_locked', error: '1Password is locked or not signed in; unlock the app (or run `op signin`) and try again' });
  assert.equal(countOccurrences(JSON.stringify(res), stderr), 0);
});

test('"not authorized" / "no access" / "permission" wording is op_not_found, not op_locked (3 cases)', async () => {
  for (const stderr of ['[ERROR] You are not authorized to perform this action', '[ERROR] no access to vault', '[ERROR] permission denied for item']) {
    const { exec } = fakeExec([{ result: 'failed', code: 1, stderr }]);
    assert.deepEqual(await resolveOpItemRef('abcdefghij0123456789klmnop', { exec }), {
      ref: null,
      kind: 'op_not_found',
      error: '1Password item or field not found, or no access (item abcdefghij0123456789klmnop)',
    }, stderr);
  }
});

test('unrecognised failures are op_failed: an unknown exit, a signal kill, no code and no signal', async () => {
  const unknown = fakeExec([{ result: 'failed', code: 6, stderr: `something odd ${FAKE_OP_KEY}` }]);
  assert.deepEqual(await opRead(REF, { exec: unknown.exec }), { value: null, attempts: 1, kind: 'op_failed', error: `1Password CLI failed (exit 6); run \`op read ${REF}\` yourself to see why` });
  const killed = fakeExec([{ result: 'failed', code: null, signal: 'SIGKILL' }]);
  assert.deepEqual(await opRead(REF, { exec: killed.exec }), { value: null, attempts: 1, kind: 'op_failed', error: `1Password CLI failed (signal SIGKILL); run \`op read ${REF}\` yourself to see why` });
  const none = fakeExec([{ result: 'failed', code: null, signal: null }]);
  assert.deepEqual(await opRead(REF, { exec: none.exec }), { value: null, attempts: 1, kind: 'op_failed', error: `1Password CLI failed (no exit code); run \`op read ${REF}\` yourself to see why` });
});

test('op read: a missing op binary (ENOENT) says how to install it', async () => {
  const { exec } = fakeExec([{ result: 'failed', code: null, signal: null, error: 'spawn op ENOENT' }]);
  assert.deepEqual(await opRead(REF, { exec }), { value: null, attempts: 1, error: '1Password CLI not found; install it with: brew install 1password-cli', kind: 'op_missing' });
});

test('op read: a "not an item" stderr is classified not-found, and the stderr is not echoed', async () => {
  const stderr = `[ERROR] "jev" isn't an item in the "Private" vault ${FAKE_OP_KEY}`;
  const { exec } = fakeExec([{ result: 'failed', code: 1, stderr }]);
  const res = await opRead(REF, { exec });
  assert.deepEqual(res, { value: null, attempts: 1, error: '1Password item or field not found, or no access; check the reference', kind: 'op_not_found' });
  assert.equal(countOccurrences(JSON.stringify(res), FAKE_OP_KEY), 0);
});

test('success on the first call: 1 invocation, argv array ["op","read",ref], no shell', async () => {
  const { exec, invocations } = fakeExec([OK]);
  const res = await opRead(REF, { exec });
  assert.deepEqual(res, { value: FAKE_OP_KEY, attempts: 1 });
  assert.equal(invocations.length, 1);
  assert.deepEqual(invocations[0].argv, ['op', 'read', REF]);
  assert.equal(invocations[0].opts.shell, undefined);
});

test('exactly one trailing newline (LF or CRLF) is stripped; spaces belonging to the value are kept', async () => {
  const spaces = fakeExec([{ result: 'ok', code: 0, stdout: 'ab c \n' }]);
  assert.equal((await opRead(REF, { exec: spaces.exec })).value, 'ab c ');
  const crlf = fakeExec([{ result: 'ok', code: 0, stdout: 'x\r\n' }]);
  assert.equal((await opRead(REF, { exec: crlf.exec })).value, 'x');
  const two = fakeExec([{ result: 'ok', code: 0, stdout: 'x\n\n' }]);
  assert.equal((await opRead(REF, { exec: two.exec })).value, 'x\n');
});

test('empty stdout is not a value', async () => {
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: '\n' }]);
  assert.deepEqual(await opRead(REF, { exec }), { value: null, attempts: 1, error: 'unexpected answer from op', kind: 'op_bad_output' });
});

test('a malformed reference is refused before anything is spawned (4 cases)', async () => {
  const { exec, invocations } = fakeExec([OK]);
  for (const bad of ['--help', 'op://vault', 'op://vault/item', 'op://v/i/f\nx']) {
    await assert.rejects(opRead(bad, { exec }), TypeError, bad);
  }
  assert.equal(invocations.length, 0);
});

test('a reference with spaces in vault and item names is accepted', () => {
  assert.equal(isOpRef('op://My Vault/My Item/password'), true);
  assert.equal(isOpRef(REF), true);
});

// ---- B25: a 1Password item ID or link resolved to op://<vaultId>/<itemId>/<fieldId> ----

const ITEM_ID = 'abcdefghij0123456789klmnop';
const VAULT_ID = 'zyxwvutsrq9876543210ponmlk';
const SECOND_FAKE = 'FAKE-cf-op-other-55aa66bb77cc';
const ITEM_ARGV = ['op', 'item', 'get', ITEM_ID, '--format', 'json'];

/**
 * The shape `op item get <id> --format json` returns (op 2.28.0), with fake values only.
 * @param {Array<Record<string, unknown>>} fields
 * @param {string} [category]
 */
function itemJson(fields, category = 'API_CREDENTIAL') {
  return JSON.stringify({
    id: ITEM_ID,
    title: 'Jev API key',
    category,
    vault: { id: VAULT_ID, name: 'Dev Keys' },
    fields: fields.map((f) => ({ reference: `op://Dev Keys/Jev API key/${f.id}`, ...f })),
  });
}

const API_CREDENTIAL_FIELDS = [
  { id: 'notesPlain', label: 'notesPlain', type: 'STRING', purpose: 'NOTES' },
  { id: 'username', label: 'username', type: 'STRING', value: 'fake-user' },
  { id: 'credential', label: 'credential', type: 'CONCEALED', value: FAKE_OP_KEY },
  { id: 'type', label: 'type', type: 'MENU', value: 'other' },
  { id: 'hostname', label: 'hostname', type: 'STRING', value: '' },
];

test('isOpItemId: 26 lowercase letters/digits only', () => {
  assert.deepEqual(
    [ITEM_ID, ITEM_ID.toUpperCase(), ITEM_ID.slice(1), `${ITEM_ID}x`, 'op://a/b/c'].map(isOpItemId),
    [true, false, false, false, false],
  );
});

test('opItemIdFromInput: a bare ID and https links on 1Password hosts give the i param', () => {
  assert.equal(opItemIdFromInput(ITEM_ID), ITEM_ID);
  assert.equal(opItemIdFromInput(` https://start.1password.com/open/i?a=FAKEACCOUNT&v=${VAULT_ID}&i=${ITEM_ID}&h=my.1password.com `), ITEM_ID);
  assert.equal(opItemIdFromInput(`https://1password.ca/open/i?i=${ITEM_ID}`), ITEM_ID);
  assert.equal(opItemIdFromInput(`https://start.1password.eu/open/i?i=${ITEM_ID}`), ITEM_ID);
});

test('opItemIdFromInput: links on other hosts or over http are refused, even with a valid 26-char ID', () => {
  assert.equal(opItemIdFromInput(`https://evil.example/?i=${ITEM_ID}`), null);
  assert.equal(opItemIdFromInput(`https://evil1password.com/?i=${ITEM_ID}`), null);
  assert.equal(opItemIdFromInput(`https://1password.com.evil.example/?i=${ITEM_ID}`), null);
  assert.equal(opItemIdFromInput(`http://start.1password.com/open/i?i=${ITEM_ID}`), null);
  assert.equal(opItemIdFromInput('not an id'), null);
});

test('opItemIdFromInput: a 1Password link whose i is not 26 chars is refused', () => {
  assert.equal(opItemIdFromInput('https://start.1password.com/open/i?a=x&v=y&i=short'), null);
  assert.equal(opItemIdFromInput(`https://start.1password.com/open/i?i=${ITEM_ID}x`), null);
});

test('an API Credential item gives exactly op://<vaultId>/<itemId>/credential; argv is exact; 1 call', async () => {
  const { exec, invocations } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(API_CREDENTIAL_FIELDS) }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  assert.deepEqual(res, { ref: `op://${VAULT_ID}/${ITEM_ID}/credential`, field: 'credential', vault: 'Dev Keys', title: 'Jev API key' });
  assert.equal(invocations.length, 1);
  assert.deepEqual(invocations[0].argv, ITEM_ARGV);
  assert.equal(invocations[0].opts.timeoutMs, OP_TIMEOUT_MS);
  assert.equal(countOccurrences(JSON.stringify(res), FAKE_OP_KEY), 0);
});

test('no credential field: the field with purpose PASSWORD is picked', async () => {
  const fields = [
    { id: 'username', label: 'username', type: 'STRING', purpose: 'USERNAME', value: 'fake-user' },
    { id: 'password', label: 'password', type: 'CONCEALED', purpose: 'PASSWORD', value: FAKE_OP_KEY },
    { id: 'pin1', label: 'pin', type: 'CONCEALED', value: SECOND_FAKE },
  ];
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(fields, 'LOGIN') }]);
  assert.equal((await resolveOpItemRef(ITEM_ID, { exec })).ref, `op://${VAULT_ID}/${ITEM_ID}/password`);
});

test('a PASSWORD-purpose field listed before `credential` does not win: the ref ends /credential', async () => {
  const fields = [
    { id: 'password', label: 'password', type: 'CONCEALED', purpose: 'PASSWORD', value: SECOND_FAKE },
    { id: 'credential', label: 'credential', type: 'CONCEALED', value: FAKE_OP_KEY },
  ];
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(fields) }]);
  assert.equal((await resolveOpItemRef(ITEM_ID, { exec })).ref, `op://${VAULT_ID}/${ITEM_ID}/credential`);
});

test('no credential or password field: the single CONCEALED field with a value is picked', async () => {
  const fields = [
    { id: 'abc123', label: 'api key', type: 'CONCEALED', value: FAKE_OP_KEY },
    { id: 'empty1', label: 'old key', type: 'CONCEALED', value: '' },
    { id: 'note', label: 'note', type: 'STRING', value: 'fake' },
  ];
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(fields, 'SECURE_NOTE') }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  assert.deepEqual(res, { ref: `op://${VAULT_ID}/${ITEM_ID}/abc123`, field: 'api key', vault: 'Dev Keys', title: 'Jev API key' });
});

test('two CONCEALED fields and no credential/password: error lists both labels, neither value', async () => {
  const fields = [
    { id: 'k1', label: 'prod key', type: 'CONCEALED', value: FAKE_OP_KEY },
    { id: 'k2', label: 'test key', type: 'CONCEALED', value: SECOND_FAKE },
  ];
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(fields, 'SECURE_NOTE') }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  assert.deepEqual(res, {
    ref: null,
    kind: 'op_no_field',
    error: 'the 1Password item has no single key field; pass a full op://vault/item/field reference (concealed fields: "prod key", "test key")',
  });
  assert.equal(countOccurrences(res.error ?? '', FAKE_OP_KEY), 0);
  assert.equal(countOccurrences(res.error ?? '', SECOND_FAKE), 0);
});

test('op_no_field labels: control characters stripped, each capped at 64 chars, JSON-quoted', async () => {
  const long = 'x'.repeat(80);
  const fields = [
    { id: 'k1', label: 'evil\u001b[2J\nlabel"q', type: 'CONCEALED', value: FAKE_OP_KEY },
    { id: 'k2', label: long, type: 'CONCEALED', value: SECOND_FAKE },
  ];
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(fields, 'SECURE_NOTE') }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  assert.equal(res.error, `the 1Password item has no single key field; pass a full op://vault/item/field reference (concealed fields: "evil[2Jlabel\\"q", "${'x'.repeat(64)}")`);
});

test('item not found (exit 1 + "isn\'t an item"): the message names the item ID only, no stdout/stderr', async () => {
  const { exec, invocations } = fakeExec([{ result: 'failed', code: 1, stdout: FAKE_OP_KEY, stderr: `[ERROR] "${ITEM_ID}" isn't an item. ${SECOND_FAKE}` }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  assert.deepEqual(res, { ref: null, kind: 'op_not_found', error: `1Password item or field not found, or no access (item ${ITEM_ID})` });
  assert.equal(invocations.length, 1);
  assert.equal(countOccurrences(res.error ?? '', FAKE_OP_KEY) + countOccurrences(res.error ?? '', SECOND_FAKE), 0);
});

test('op item get failure kinds: missing CLI, locked, timeout — exact messages, no output echoed', async () => {
  /** @type {Array<[Record<string, unknown>, string, string, number]>} */
  const cases = [
    [{ result: 'failed', code: null, error: 'spawn op ENOENT' }, 'op_missing', '1Password CLI not found; install it with: brew install 1password-cli', 1],
    [{ result: 'failed', code: 1, stderr: `[ERROR] account is not signed in ${FAKE_OP_KEY}` }, 'op_locked', '1Password is locked or not signed in; unlock the app (or run `op signin`) and try again', 1],
    [TIMEOUT, 'op_timeout', '1Password did not answer in time; unlock the app and try again', 2],
  ];
  for (const [step, kind, error, calls] of cases) {
    const { exec, invocations } = fakeExec([step, step]);
    const res = await resolveOpItemRef(ITEM_ID, { exec });
    assert.deepEqual(res, { ref: null, kind, error }, String(kind));
    assert.equal(invocations.length, calls, String(kind));
    assert.equal(countOccurrences(JSON.stringify(res), FAKE_OP_KEY), 0);
  }
});

test('a first-call timeout of op item get is retried once, then succeeds: 2 calls with the same argv', async () => {
  const { exec, invocations } = fakeExec([TIMEOUT, { result: 'ok', code: 0, stdout: itemJson(API_CREDENTIAL_FIELDS) }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  assert.equal(res.ref, `op://${VAULT_ID}/${ITEM_ID}/credential`);
  assert.deepEqual(invocations.map((c) => c.argv), [ITEM_ARGV, ITEM_ARGV]);
});

test('bad op output (not JSON / no vault / no fields): "unexpected answer from op", no content echoed', async () => {
  const outputs = [
    `{"id":"${ITEM_ID}","fields":[{"value":"${FAKE_OP_KEY}"`,
    JSON.stringify({ id: ITEM_ID, fields: [{ id: 'credential', value: FAKE_OP_KEY }] }),
    JSON.stringify({ id: ITEM_ID, vault: { id: VAULT_ID }, title: FAKE_OP_KEY }),
  ];
  for (const stdout of outputs) {
    const { exec, invocations } = fakeExec([{ result: 'ok', code: 0, stdout }]);
    const res = await resolveOpItemRef(ITEM_ID, { exec });
    assert.deepEqual(res, { ref: null, kind: 'op_bad_output', error: 'unexpected answer from op' });
    assert.equal(invocations.length, 1);
  }
});

test('toOpRef: an op:// input passes unchanged with 0 op calls; a link resolves its i; garbage is refused with 0 calls', async () => {
  const ok = { result: 'ok', code: 0, stdout: itemJson(API_CREDENTIAL_FIELDS) };
  const direct = fakeExec([ok]);
  assert.deepEqual(await toOpRef('op://Private/jev/credential', { exec: direct.exec }), { ref: 'op://Private/jev/credential' });
  assert.equal(direct.invocations.length, 0);

  const viaLink = fakeExec([ok]);
  const link = `https://start.1password.com/open/i?a=FAKEACCOUNT&v=${VAULT_ID}&i=${ITEM_ID}&h=my.1password.com`;
  assert.equal((await toOpRef(link, { exec: viaLink.exec })).ref, `op://${VAULT_ID}/${ITEM_ID}/credential`);
  assert.deepEqual(viaLink.invocations.map((c) => c.argv), [ITEM_ARGV]);

  const garbage = fakeExec([ok]);
  assert.deepEqual(await toOpRef('my jev key', { exec: garbage.exec }), { ref: null, kind: 'op_bad_input', error: 'not an op:// reference, 1Password item ID or item link' });
  assert.equal(garbage.invocations.length, 0);
});

test('title, vault name and field label are cleaned (no control chars, max 64) before they are returned or printed', async () => {
  const raw = JSON.parse(itemJson(API_CREDENTIAL_FIELDS));
  raw.title = `Jev\u001b[31m key\u0007${'t'.repeat(80)}`;
  raw.vault.name = 'Dev\nKeys\u007f';
  raw.fields = [{ id: 'credential', label: 'cred\u001b]0;x\u0007', type: 'CONCEALED', value: FAKE_OP_KEY }];
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: JSON.stringify(raw) }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  assert.deepEqual(res, { ref: `op://${VAULT_ID}/${ITEM_ID}/credential`, field: 'cred]0;x', vault: 'DevKeys', title: `Jev[31m key${'t'.repeat(53)}` });
  const line = describeOpItem(res) ?? '';
  assert.equal(line, `1Password item "Jev[31m key${'t'.repeat(53)}" (vault DevKeys, field cred]0;x)`);
  assert.equal(/[\x00-\x1f\x7f]/.test(line), false);
});

test('op_no_field lists at most 10 labels, then "and N more"', async () => {
  const fields = Array.from({ length: 13 }, (_, i) => ({ id: `k${i}`, label: `key ${i}`, type: 'CONCEALED', value: `${FAKE_OP_KEY}-${i}` }));
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(fields, 'SECURE_NOTE') }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  const listed = Array.from({ length: 10 }, (_, i) => `"key ${i}"`).join(', ');
  assert.deepEqual(res, {
    ref: null,
    kind: 'op_no_field',
    error: `the 1Password item has no single key field; pass a full op://vault/item/field reference (concealed fields: ${listed} and 3 more)`,
  });
  assert.equal(countOccurrences(res.error ?? '', FAKE_OP_KEY), 0);
});

test('zero filled CONCEALED fields: the exact op_no_field message, no value in it', async () => {
  const fields = [
    { id: 'k1', label: 'old key', type: 'CONCEALED', value: '' },
    { id: 'k2', label: 'new key', type: 'CONCEALED' },
    { id: 'note', label: 'note', type: 'STRING', value: FAKE_OP_KEY },
  ];
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(fields, 'SECURE_NOTE') }]);
  const res = await resolveOpItemRef(ITEM_ID, { exec });
  assert.deepEqual(res, {
    ref: null,
    kind: 'op_no_field',
    error: 'the 1Password item has no single key field; pass a full op://vault/item/field reference (concealed fields: "old key", "new key")',
  });
  assert.equal(countOccurrences(res.error ?? '', FAKE_OP_KEY), 0);
});

test('an empty `credential` field does not win: the PASSWORD-purpose field is picked', async () => {
  const fields = [
    { id: 'credential', label: 'credential', type: 'CONCEALED', value: '' },
    { id: 'password', label: 'password', type: 'CONCEALED', purpose: 'PASSWORD', value: FAKE_OP_KEY },
  ];
  const { exec } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(fields) }]);
  assert.equal((await resolveOpItemRef(ITEM_ID, { exec })).ref, `op://${VAULT_ID}/${ITEM_ID}/password`);
});

test('toOpRef with a bare item ID resolves it: the ref and exactly one call with ITEM_ARGV', async () => {
  const { exec, invocations } = fakeExec([{ result: 'ok', code: 0, stdout: itemJson(API_CREDENTIAL_FIELDS) }]);
  assert.equal((await toOpRef(ITEM_ID, { exec })).ref, `op://${VAULT_ID}/${ITEM_ID}/credential`);
  assert.deepEqual(invocations.map((c) => c.argv), [ITEM_ARGV]);
});
