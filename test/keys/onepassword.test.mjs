import { FAKE_OP_KEY } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { isOpRef, OP_TIMEOUT_MS, opRead } from '../../src/keys/onepassword.mjs';

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
    assert.equal(call.opts.timeoutMs, 20_000);
  }
});

test('two timeouts in a row stop at 2 invocations — never a third — and the error carries no output', async () => {
  const { exec, invocations } = fakeExec([TIMEOUT, TIMEOUT, OK]);
  const res = await opRead(REF, { exec });
  assert.equal(invocations.length, 2);
  assert.deepEqual(res, { value: null, attempts: 2, error: 'op read failed (timed out)' });
});

test('a non-timeout failure is not retried: 1 invocation, exact message without stdout/stderr', async () => {
  const { exec, invocations } = fakeExec([{ result: 'failed', code: 1, stdout: FAKE_OP_KEY, stderr: FAKE_OP_KEY }, OK]);
  const res = await opRead(REF, { exec });
  assert.equal(invocations.length, 1);
  assert.deepEqual(res, { value: null, attempts: 1, error: 'op read failed (exit 1)' });
});

test('killed by a signal without a timeout: the message names the signal', async () => {
  const { exec } = fakeExec([{ result: 'failed', code: null, signal: 'SIGKILL' }]);
  assert.deepEqual(await opRead(REF, { exec }), { value: null, attempts: 1, error: 'op read failed (signal SIGKILL)' });
});

test('no code and no signal: a generic failure message', async () => {
  const { exec } = fakeExec([{ result: 'failed', code: null, signal: null }]);
  assert.deepEqual(await opRead(REF, { exec }), { value: null, attempts: 1, error: 'op read failed (failed)' });
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
  assert.deepEqual(await opRead(REF, { exec }), { value: null, attempts: 1, error: 'op read returned nothing' });
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
