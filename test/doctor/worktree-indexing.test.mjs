import assert from 'node:assert/strict';
import path from 'node:path';
import { mock, test } from 'node:test';

let enabled = true;
let responds = true;
let lookupThrows = false;
let lookups = 0;
const config = {};
const top = process.env.CODE_FORGE_TEST_ROOT;

mock.module('../../src/doctor/index.mjs', { namedExports: { runDoctor: async () => [] } });
mock.module('../../src/config/load.mjs', { namedExports: {
  loadProjectConfig: async () => ({ ok: true, config, path: path.join(top, '.code-forge.yml') }),
  projectRootFor: () => top,
} });
mock.module('../../src/worker/queue.mjs', { namedExports: { liveWorker: () => ({ run: 'r-indexing' }) } });
mock.module('../../src/state/run.mjs', { namedExports: { readRun: async () => ({}) } });
mock.module('../../src/state/config-snapshot.mjs', { namedExports: { configInForce: () => config } });
mock.module('../../src/util/native.mjs', { namedExports: {
  nativePath: () => null,
  runNative: async () => null,
  daemonEnabled: () => enabled,
  daemonRequest: async () => responds ? { ok: true } : null,
} });
mock.module('../../src/proof/worktree.mjs', { namedExports: {
  DEFAULT_BRANCH_TEMPLATE: 'forge/{run}/{block}',
  repoTopOf: async () => top,
  worktreeDirectory: async () => {
    lookups++;
    if (lookupThrows) throw new Error('git worktree list failed');
    return { dir: `${top}-worktrees.noindex`, mark: true };
  },
} });
const { runDoctorCli } = await import('../../src/cli/doctor.mjs');

test('a throwing worktree lookup preserves the daemon row for live, fallback and off states', async () => {
  for (const state of ['live', 'fallback', 'off']) {
    enabled = state !== 'off';
    responds = state === 'live';
    const rows = [];
    for (const throws of [false, true]) {
      lookupThrows = throws;
      lookups = 0;
      let output = '';
      const code = await runDoctorCli(['--json', '--cwd', top], {
        platform: 'darwin',
        stdout: { write: (text) => { output += text; } },
        stderr: { write: () => {} },
      });
      assert.equal(code, 0);
      assert.equal(lookups, 1, 'the worktree lookup was reached');
      rows.push(JSON.parse(output).rows.find((row) => row.id === 'native_daemon'));
    }
    assert.deepEqual(rows[0], { id: 'native_daemon', status: 'INFO', label: 'native daemon', detail: state });
    assert.deepEqual(rows[1], rows[0], `${state}: indexing failure preserves the daemon row`);
  }
});
