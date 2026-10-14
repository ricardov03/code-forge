import assert from 'node:assert/strict';
import { after, mock, test } from 'node:test';

const original = new Error('original CLI failure');
let fail = false;
let liveWorkerCalls = 0;
mock.module(new URL('../../src/util/exec.mjs', import.meta.url).href, { namedExports: { exec: async () => {
  if (fail) throw original;
  return { code: 7, stdout: 'cli output', stderr: 'cli diagnostic' };
} } });
mock.module(new URL('../../src/worker/queue.mjs', import.meta.url).href, { namedExports: { liveWorker: () => { liveWorkerCalls += 1; throw new Error('bad worker announcement'); } } });
const { cli, freshDir } = await import('./helpers.mjs');
after(() => mock.reset());

test('B113: a broken cleanup lookup preserves both the CLI result and the original CLI error', async () => {
  const cwd = freshDir('cli-cleanup');
  const result = await cli(['version'], cwd);
  assert.deepEqual([result.code, result.stdout, result.stderr], [7, 'cli output', 'cli diagnostic']);
  assert.equal(liveWorkerCalls, 1);
  fail = true;
  await assert.rejects(cli(['version'], cwd), (err) => err === original);
  assert.equal(liveWorkerCalls, 2);
});
