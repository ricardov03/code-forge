import { captureStream, withFixture } from '../state/helpers.mjs';
import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { stringify } from 'yaml';
import { runRun } from '../../src/cli/run.mjs';
import { configInForce } from '../../src/state/config-snapshot.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';
import { loadProjectConfig } from '../../src/config/load.mjs';

for (const name of Object.keys(process.env)) if (name.startsWith('CODE_FORGE_')) delete process.env[name];

const SECOND = { provider: 'anthropic', model: 'claude-fable-5-1' };

/** @param {string} runId */
async function reload(runId) {
  const stdout = captureStream();
  const stderr = captureStream();
  const code = await runRun(['reload', '--run', runId], { stdout, stderr });
  return { code, out: stdout.text, err: stderr.text };
}

test('reload applies review recovery keys despite frozen proof differences and names each retained key', async () => {
  await withFixture(async ({ ws }) => {
    const loaded = await loadProjectConfig(ws);
    const before = { ...loaded.config, proof: { isolation: 'export', worktree: { dir: 'old-trees' } }, review: { session_timeout_s: 60 } };
    await startRun({ workspace: ws, project: 'two-blocks', engine: 'harness', runId: 'r-review-frozen', config: before, writeRow: async () => {} });
    const review = { second_levels: { L2: SECOND }, routes: [{ min_lines: 400, ...SECOND }], session_timeout_s: 120 };
    await writeFile(path.join(ws, '.code-forge.yml'), stringify({ ...before, proof: { isolation: 'worktree', worktree: { dir: 'new-trees' } }, review }));
    const res = await reload('r-review-frozen');
    assert.equal(res.code, 0, res.err);
    assert.equal(res.err, '');
    const after = configInForce(await readRun('r-review-frozen'));
    assert.deepEqual(after.review, review, 'all three review-only keys applied');
    assert.deepEqual(after.proof, before.proof, 'both frozen proof values retained');
    const expected = 'run r-review-frozen: config reloaded · 3 keys changed\n  review.routes\n  review.second_levels\n  review.session_timeout_s\n' +
      'kept frozen keys from the run snapshot: proof.isolation, proof.worktree\n' +
      'queued review tickets (0) keep the config they were enqueued with; new tickets use the new config\n';
    assert.equal(res.out, expected);
  });
});

test('reload without frozen differences preserves its exact success and no-change output', async () => {
  await withFixture(async ({ ws }) => {
    const loaded = await loadProjectConfig(ws);
    const before = { ...loaded.config, review: { session_timeout_s: 60 } };
    await startRun({ workspace: ws, project: 'two-blocks', engine: 'harness', runId: 'r-review-normal', config: before, writeRow: async () => {} });
    await writeFile(path.join(ws, '.code-forge.yml'), stringify({ ...before, review: { ...before.review, second_levels: { L2: SECOND } } }));
    const expected = 'run r-review-normal: config reloaded · 1 key changed\n  review.second_levels\n' +
      'queued review tickets (0) keep the config they were enqueued with; new tickets use the new config\n';
    assert.deepEqual(await reload('r-review-normal'), { code: 0, err: '', out: expected });
    assert.deepEqual(await reload('r-review-normal'), { code: 0, err: '', out: 'run r-review-normal: no config change\n' });
  });
});
