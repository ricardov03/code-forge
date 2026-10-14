import assert from 'node:assert/strict';
import * as childProcess from 'node:child_process';
import { mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, mock, test } from 'node:test';

let listing = '';
let psCall;
/** @type {Error | null} */
let failure = null;
mock.module('node:child_process', {
  namedExports: { ...childProcess, execFileSync: (...args) => { psCall = args; if (failure) throw failure; return listing; } },
});
const { suiteWorkers } = await import('../helpers/worker-processes.mjs');
const parent = mkdtempSync(path.join(os.tmpdir(), 'b113-process-list-'));
after(() => { mock.reset(); rmSync(parent, { recursive: true, force: true }); });

test('B113: failed process listing reports only exit and error codes, with no environment dump', () => {
  failure = Object.assign(new Error('FAKE-secret-env-dump'), { status: 2, code: 'EACCES', stdout: 'FAKE-secret-stdout', stderr: 'FAKE-secret-stderr' });
  try {
    assert.throws(() => suiteWorkers('suite', parent), (err) => {
      assert.ok(err instanceof Error);
      assert.equal(err.message, 'worker process listing failed (exit=2, code=EACCES)');
      assert.deepEqual(Object.keys(err), []);
      assert.equal(err.cause, undefined);
      assert.equal(String(err.stack).includes('FAKE-secret'), false);
      assert.equal(psCall[0], 'ps');
      assert.ok(Array.isArray(psCall[1]));
      assert.ok(psCall[1].includes('eww'));
      assert.notEqual(psCall[2].shell, true);
      return true;
    });
  } finally { failure = null; }
});

test('B113: process listing resolves checkout and base aliases and reports missing or odd runs as ?', () => {
  const bin = realpathSync(fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url)));
  const alias = path.join(parent, 'checkout');
  symlinkSync(path.dirname(bin), alias);
  const base = path.join(parent, 'base');
  mkdirSync(base);
  const baseAlias = path.join(parent, 'base-alias');
  symlinkSync(base, baseAlias);
  // HOME no longer exists, but its base alias still must resolve.
  const home = path.join(baseAlias, 'code-forge-test-123', 'home');
  const command = `${process.execPath} ${path.join(alias, 'code-forge.mjs')} worker`;
  const env = `HOME=${home} CODE_FORGE_TEST_SUITE=suite`;
  listing = [
    `123 ${command} --run valid-run ${env}`,
    `124 ${command} ${env}`,
    `125 ${command} --run --odd ${env}`,
    `126 ${command} --run valid-run ${env.replace('=suite', '=other')}`,
    `127 ${command} --run valid-run ${env.replace('code-forge-test-123', 'outside')}`,
    `128 ${process.execPath} /other/bin/code-forge.mjs worker --run valid-run ${env}`,
  ].join('\n');
  assert.deepEqual(suiteWorkers('suite', base), [{ pid: 123, run: 'valid-run' }, { pid: 124, run: '?' }, { pid: 125, run: '?' }]);
});
