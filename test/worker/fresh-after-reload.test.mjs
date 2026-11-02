import { BLOCK, freshDir, makeRepo } from './helpers.mjs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { parse } from 'yaml';

const { createWorker } = await import('../../src/worker/loop.mjs');
const { enqueue, readResult, writeResult } = await import('../../src/worker/queue.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { startRun, reloadRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, signRow, verifyRow } = await import('../../src/state/signer.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');
const commonDir = execFileSync('git', ['rev-parse', '--path-format=absolute', '--git-common-dir'],
  { cwd: new URL('../../', import.meta.url), encoding: 'utf8', env: gitChildEnv() }).trim();
const FIXTURES = `${path.resolve(commonDir, '..', 'plans/tools/forge/b234')}/`;
const sample = readFileSync(`${FIXTURES}sample.jsonl`, 'utf8').trim().split('\n').map((s) => JSON.parse(s));
const previousRoute = sample.find((r) => r.event === 'review.route');
const previousFailure = sample.find((r) => r.event === 'review.result' && r.reason === 'route-invalid');
const FILE = 'src/a.mjs';

/** @param {boolean} fresh @param {boolean} [invalidCurrent] */
async function retryAfterReload(fresh, invalidCurrent = false) {
  const { repo, runId } = await makeRepo({ start: false });
  const config1 = parse(readFileSync(`${FIXTURES}sample-run-config.yml`, 'utf8'));
  const rows = [];
  const writeRow = async (row) => { rows.push(row); };
  await startRun({ workspace: repo, project: 'worker-test', config: config1, runId, writeRow });
  await openBlock({ runId, id: BLOCK, level: 'L2', owned: [FILE], acceptance: [{ clause: 'review', tests: ['reload'] }], writeRow });
  const key = await loadKey(runId);
  const first = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: FILE });
  rows.push(signRow({ ...previousRoute, run: runId, ticket: first.ticket, block: BLOCK, file: FILE, content_hash: first.content_hash }, key));
  writeResult({ repoRoot: repo, runId, ticket: first.ticket, key,
    result: { ...previousFailure, run: runId, ticket: first.ticket, block: BLOCK, file: FILE, content_hash: first.content_hash } });
  assert.deepEqual([readResult(repo, runId, first.ticket).status, readResult(repo, runId, first.ticket).reason], ['unavailable', 'route-invalid']);
  const config2 = structuredClone(config1);
  delete config2.review.routes;
  config2.levels.L2.model = 'claude-sonnet-5';
  if (invalidCurrent) config2.review.routes = [{ min_lines: -1, model: 'broken' }];
  // Preserve an old pending-ticket pin, as a reload during an in-flight retry does.
  await reloadRun({ runId, config: config2, readPending: () => [first.ticket], writeRow });
  const calls = [];
  const worker = await createWorker({ runId, repoRoot: repo, cfg: config1, runRootDir: freshDir('worker'), slug: 'worker-test', key }, {
    store: await createKeyStore({ backends: [], dir: freshDir('keys') }), env: {},
    readRows: async () => rows, writeRow,
    spawn: async (opts) => {
      const entry = opts.cfg.levels.L2;
      calls.push(entry.model);
      const text = readFileSync(opts.promptPath, 'utf8');
      return { status: 'ok', provider: entry.provider ?? opts.cfg.provider, model: entry.model,
        usage: { tokens_in: 100, tokens_out: 400 }, answer: { passed: true, summary: 'ok', findings: [],
          reviewed_hunks: text.split('\n').filter((s) => s.startsWith('- @@')).map((s) => s.slice(2)) } };
    },
  });
  worker.announce();
  try {
    let output = '';
    let error = '';
    const code = await runReviewFile([FILE, '--block', BLOCK, ...(fresh ? ['--fresh'] : [])], {
      cwd: repo, stdout: { write: (s) => { output += s; return true; } }, stderr: { write: (s) => { error += s; return true; } },
    });
    assert.equal(code, 0, error);
    assert.equal(JSON.parse(output).ticket, first.ticket, 'same content keeps the same ticket');
    assert.equal(await worker.drain(), 1);
    const result = readResult(repo, runId, first.ticket);
    assert.equal(verifyRow(result, key).ok, true);
    return { result, calls, rows };
  } finally {
    worker.stop();
    worker.release();
  }
}

test('after reload removes the route, --fresh reviews the same ticket under the current snapshot', async () => {
  const w = await retryAfterReload(true);
  assert.deepEqual([w.result.status, w.result.approved], ['reviewed', true]);
  assert.deepEqual(w.calls, ['claude-sonnet-5']);
  const invalid = await retryAfterReload(true, true);
  assert.deepEqual([invalid.result.status, invalid.result.reason, invalid.result.approved], ['unavailable', 'route-invalid', false]);
  assert.deepEqual(invalid.calls, []);
  assert.equal(invalid.rows.filter((r) => r.event === 'review.approved').length, 0);
});

test('automatic retry also ignores the queued snapshot and a previous route whose model was removed', async () => {
  const w = await retryAfterReload(false);
  assert.deepEqual([w.result.status, w.result.approved], ['reviewed', true]);
  assert.deepEqual(w.calls, ['claude-sonnet-5']);
});
