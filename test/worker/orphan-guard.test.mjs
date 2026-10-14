import { freshDir } from './helpers.mjs';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import path from 'node:path';
import { after, mock, test } from 'node:test';

const missing = path.join(freshDir('missing-run'), 'run.json');
let stats = 0;
const warnings = [];
const { default: fsDefault, ...named } = /** @type {any} */ (fs);
const statSync = (file, ...args) => {
  if (file === missing) { stats++; throw Object.assign(new Error('missing'), { code: 'ENOENT' }); }
  return fs.statSync(file, ...args);
};
mock.module('node:fs', { namedExports: { ...named, statSync }, defaultExport: { ...fsDefault, statSync } });
mock.module(new URL('../../src/util/error-log.mjs', import.meta.url).href, { namedExports: { logWarning: async (warning) => { warnings.push(warning); } } });
after(() => mock.reset());
const { createWorker } = await import('../../src/worker/loop.mjs');

test('B113: orphan stop logs its reason and path once and performs no more guard stats after stopping', async () => {
  const worker = await createWorker({ runId: 'guard-run', repoRoot: freshDir('guard-repo'), cfg: {}, runRootDir: freshDir('guard-root'), slug: 'guard', key: Buffer.alloc(32), guardRunRecordPath: missing }, { expiryCheck: async () => {} });
  try {
    assert.equal(await worker.drain(), 0);
    assert.equal(await worker.drain(), 0);
    assert.equal(stats, 1);
    assert.deepEqual(warnings, [{ warning: 'worker_orphan_stop', message: `run record missing: ${missing}` }]);
  } finally { worker.stop(); worker.release(); }
});
