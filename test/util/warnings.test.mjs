// B37 warnings: `logWarning` writes one `kind: 'warning'` line (cleaned, capped, fingerprinted,
// once per fingerprint per process); each wired site (1Password retry, review session retry,
// System 1 falling back to rules, the 80 % budget warning) writes exactly one; the hint never
// fires for a warning. HOME points into one per-file temp parent before any `src` module loads.
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, beforeEach, describe, test } from 'node:test';

const PARENT = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-warn-')));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { MESSAGE_MAX_BYTES, fingerprint, logWarning, resetWarnings } = await import('../../src/util/error-log.mjs');
const { resetBreadcrumbs } = await import('../../src/util/breadcrumbs.mjs');
const { maybeHint } = await import('../../src/util/error-hint.mjs');
const { opRead } = await import('../../src/keys/onepassword.mjs');
const { spawnWithTimeoutRetry } = await import('../../src/review/session-retry.mjs');
const { askNoul } = await import('../../src/review/triage.mjs');
const { checkBudget } = await import('../../src/ledger/spend.mjs');

let n = 0;
/** A fresh HOME (process.env.HOME points at it) and its error log path. */
function fresh() {
  n += 1;
  const home = path.join(PARENT, `h${n}`);
  mkdirSync(home, { recursive: true });
  process.env.HOME = home;
  return { home, log: path.join(home, '.code-forge', 'logs', 'errors.jsonl') };
}

/** @param {string} file @returns {any[]} */
function entries(file) {
  return existsSync(file) ? readFileSync(file, 'utf8').split('\n').filter((l) => l.length > 0).map((l) => JSON.parse(l)) : [];
}

const OK = (stdout = '') => ({ result: 'ok', code: 0, signal: null, stdout, stderr: '', timedOut: false });
const TIMEOUT = { result: 'failed', code: null, signal: 'SIGKILL', stdout: '', stderr: '', timedOut: true };

beforeEach(() => {
  resetWarnings();
  resetBreadcrumbs();
  delete process.env.CODE_FORGE_NO_ERROR_LOG;
});

describe('logWarning', () => {
  test('one cleaned line: kind warning, exit 0, no flags, no stack, the fingerprint of what happened; a repeat in the same process writes nothing', async () => {
    const w = fresh();
    const e = await logWarning({ verb: 'keys', warning: 'op_retry', message: `retried for ${w.home}/x and bob@example.com`, cwd: PARENT });
    assert.notEqual(e, null);
    const got = entries(w.log);
    assert.equal(got.length, 1);
    assert.deepEqual(Object.keys(got[0]), ['ts', 'version', 'node', 'platform', 'arch', 'verb', 'sub', 'flags', 'exit', 'kind', 'warning', 'message', 'stack', 'cleaned', 'fp', 'before']);
    assert.deepEqual(
      [got[0].verb, got[0].sub, got[0].flags, got[0].exit, got[0].kind, got[0].warning, got[0].message, got[0].stack, got[0].cleaned, got[0].before],
      ['keys', null, [], 0, 'warning', 'op_retry', 'retried for ~/x and <email>', null, [{ rule: 'home', count: 1 }, { rule: 'email', count: 1 }], []],
    );
    assert.equal(got[0].fp, fingerprint({ verb: 'keys', sub: null, kind: 'warning', message: 'retried for ~/x and <email>', warning: 'op_retry' }));
    assert.notEqual(got[0].fp, fingerprint({ verb: 'keys', sub: null, kind: 'warning', message: 'retried for ~/x and <email>', warning: 'review_retry' }));
    assert.equal(await logWarning({ verb: 'keys', warning: 'op_retry', message: `retried for ${w.home}/x and bob@example.com`, cwd: PARENT }), null);
    assert.equal(entries(w.log).length, 1);
  });

  test('the message is capped at 2,048 bytes; a kind that is not a snake_case word is "warning"; no verb noted is "unknown"', async () => {
    const w = fresh();
    await logWarning({ warning: 'Bad Kind!', message: 'x'.repeat(5000) });
    const [e] = entries(w.log);
    assert.deepEqual([e.verb, e.warning, Buffer.byteLength(e.message)], ['unknown', 'warning', MESSAGE_MAX_BYTES]);
  });

  test('never rejects: no input, or a log dir that is a file, resolves null; the fingerprint is remembered only after a written line', async () => {
    const w = fresh();
    assert.equal(await logWarning(/** @type {any} */ (undefined)), null);
    mkdirSync(path.join(w.home, '.code-forge'), { recursive: true });
    writeFileSync(path.join(w.home, '.code-forge', 'logs'), 'not a dir');
    assert.equal(await logWarning({ verb: 'keys', warning: 'op_retry', message: 'm' }), null);
    rmSync(path.join(w.home, '.code-forge', 'logs'));
    assert.notEqual(await logWarning({ verb: 'keys', warning: 'op_retry', message: 'm' }), null);
    assert.equal(entries(w.log).length, 1);
  });

  test('CODE_FORGE_NO_ERROR_LOG=1 writes nothing', async () => {
    const w = fresh();
    process.env.CODE_FORGE_NO_ERROR_LOG = '1';
    assert.equal(await logWarning({ verb: 'keys', warning: 'op_retry', message: 'm' }), null);
    assert.equal(existsSync(path.join(w.home, '.code-forge')), false);
  });

  test('the hint never fires for a warning (TTY, exit 1, a well-formed fp)', async () => {
    const w = fresh();
    const chunks = [];
    const shown = await maybeHint({ entry: { fp: 'abcdefabcdef', kind: 'warning' }, exit: 1, isTTY: true, env: { HOME: w.home }, stderr: { write: (s) => chunks.push(s) } });
    assert.deepEqual([shown, chunks.length, existsSync(path.join(w.home, '.code-forge', 'logs', 'hints.json'))], [false, 0, false]);
    const error = await maybeHint({ entry: { fp: 'abcdefabcdef', kind: 'error' }, exit: 1, isTTY: true, env: { HOME: w.home }, stderr: { write: (s) => chunks.push(s) } });
    assert.deepEqual([error, chunks.length], [true, 1]);
  });
});

describe('each wired warning writes exactly one line', () => {
  test('1Password: a call that needed its retry → 1 op_retry warning; a call that did not → 0', async () => {
    const w = fresh();
    const answers = [TIMEOUT, OK('FAKE-value\n')];
    const res = await opRead('op://Vault/Item/credential', { exec: async () => /** @type {any} */ (answers.shift()) });
    assert.equal(res.attempts, 2);
    const got = entries(w.log);
    assert.deepEqual(got.map((e) => [e.kind, e.warning, e.message]), [['warning', 'op_retry', '1Password did not answer in time; the call was retried once']]);
    assert.equal(JSON.stringify(got).split('FAKE-value').length - 1, 0);
    const v = fresh();
    resetWarnings();
    await opRead('op://Vault/Item/credential', { exec: async () => /** @type {any} */ (OK('FAKE-value\n')) });
    assert.equal(entries(v.log).length, 0);
  });

  test('a review session retried after a timeout → 1 review_retry warning; no timeout → 0', async () => {
    const w = fresh();
    const results = [{ status: 'timeout' }, { status: 'ok' }];
    const out = await spawnWithTimeoutRetry(async () => /** @type {any} */ (results.shift()), {}, async () => {});
    assert.equal(out.attempts, 2);
    assert.deepEqual(entries(w.log).map((e) => [e.kind, e.warning]), [['warning', 'review_retry']]);
    const v = fresh();
    resetWarnings();
    await spawnWithTimeoutRetry(async () => ({ status: 'ok' }), {}, async () => {});
    assert.equal(entries(v.log).length, 0);
  });

  test('System 1 (Jev) failing → 1 s1_fallback warning naming the reason; no Jev configured → 0', async () => {
    const w = fresh();
    const p = await askNoul('defect', { finding: {} }, { jev: async () => ({ ok: false, kind: 'timeout' }) });
    assert.equal(p, null);
    assert.deepEqual(entries(w.log).map((e) => [e.kind, e.warning, e.message]), [['warning', 's1_fallback', 'System 1 (Jev) gave no answer (timeout); fell back to the rules']]);
    const x = fresh();
    resetWarnings();
    const secretText = 'FAKE jev said: customer acme-private';
    assert.equal(await askNoul('defect', { finding: {} }, { jev: async () => ({ ok: false, kind: secretText }) }), null);
    assert.equal(await askNoul('resolved', { finding: {} }, { jev: async () => { throw new Error(secretText); } }), null);
    const xs = entries(x.log);
    assert.deepEqual(xs.map((e) => e.message), ['System 1 (Jev) gave no answer (other); fell back to the rules', 'System 1 (Jev) gave no answer (error); fell back to the rules']);
    assert.equal(JSON.stringify(xs).split('acme-private').length - 1, 0);
    const v = fresh();
    resetWarnings();
    assert.equal(await askNoul('defect', { finding: {} }, {}), null);
    assert.equal(await askNoul('defect', { finding: {} }, { jev: async () => ({ ok: true, answers: { defect: { noul: 0.5 } } }) }), 0.5);
    assert.equal(entries(v.log).length, 0);
  });

  test('the 80 % budget warning → 1 budget_warning line without the amounts; below 80 % → 0', async () => {
    const w = fresh();
    const rows = [{ event: 'session', run: 'r-b37', usd: 17.25 }];
    const res = await checkBudget({ budget: 20, run: 'r-b37', rows, writeRow: async () => {}, stderr: { write: () => true } });
    assert.equal(res.refuse, false);
    const got = entries(w.log);
    assert.deepEqual(got.map((e) => [e.kind, e.warning, e.message]), [['warning', 'budget_warning', 'budget.usd: 80% of the run budget is spent; new sessions stop at 100%']]);
    assert.equal(JSON.stringify(got).split('17.25').length - 1, 0);
    assert.equal(JSON.stringify(got).split('20.00').length - 1, 0);
    const v = fresh();
    resetWarnings();
    await checkBudget({ budget: 20, run: 'r-b37-low', rows: [{ event: 'session', run: 'r-b37-low', usd: 1 }], writeRow: async () => {}, stderr: { write: () => true } });
    assert.equal(entries(v.log).length, 0);
  });
});

describe('a logging failure never changes a wired operation', () => {
  /** A HOME whose logs folder is a file: every warning write fails. */
  function broken() {
    const w = fresh();
    mkdirSync(path.join(w.home, '.code-forge'), { recursive: true });
    writeFileSync(path.join(w.home, '.code-forge', 'logs'), 'not a dir');
    return w;
  }

  test('1Password: the retried read still returns its value after 2 attempts', async () => {
    broken();
    const answers = [TIMEOUT, OK('FAKE-value\n')];
    const res = await opRead('op://Vault/Item/credential', { exec: async () => /** @type {any} */ (answers.shift()) });
    assert.deepEqual([res.value, res.attempts], ['FAKE-value', 2]);
  });

  test('review retry: the second attempt is still returned', async () => {
    broken();
    const results = [{ status: 'timeout' }, { status: 'ok' }];
    const out = await spawnWithTimeoutRetry(async () => /** @type {any} */ (results.shift()), {}, async () => {});
    assert.deepEqual([out.attempts, out.res?.status], [2, 'ok']);
  });

  test('triage: askNoul still returns null (the fallback)', async () => {
    broken();
    assert.equal(await askNoul('defect', { finding: {} }, { jev: async () => ({ ok: false, kind: 'timeout' }) }), null);
  });

  test('budget: the check still answers and prints its one stderr line', async () => {
    broken();
    const lines = [];
    const res = await checkBudget({ budget: 20, run: 'r-b37-broken', rows: [{ event: 'session', run: 'r-b37-broken', usd: 17 }], writeRow: async () => {}, stderr: { write: (s) => lines.push(s) } });
    assert.deepEqual([res, lines.length], [{ refuse: false, spent: 17 }, 1]);
  });
});
