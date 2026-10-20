import { cfgFor, freshDir, git, harness, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, blockTickets, enqueue, enqueueHash, writeResult } = await import('../../src/worker/queue.mjs');
const { watchTick } = await import('../../src/worker/watch.mjs');
const { reviewTicket } = await import('../../src/worker/engine.mjs');
const PROBE = async (pid) => pid === 4242 ? 'start-A' : null;
let sequence = 0;

async function call(run, args, cwd) {
  let out = '', err = '';
  const code = await run(args, { cwd, stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, probe: PROBE, pollMs: 1 });
  return { code, out, err };
}

function lock(resolved = 'https://registry.npmjs.org/example/-/example-1.tgz', integrity = 'sha512-example') {
  return JSON.stringify({ lockfileVersion: 3, packages: { '': { name: 'site' }, 'node_modules/example': { resolved, ...(integrity ? { integrity } : {}) }, 'node_modules/workspace': { link: true, resolved: '../workspace' }, local: {} }, padding: 'x'.repeat(240000) });
}

async function fixture(review = {}, owned = 'a.mjs package-lock.json', file = 'package-lock.json') {
  const slug = `exclude-${++sequence}`;
  const repo = freshDir(slug), inputs = freshDir('inputs');
  const config = { version: 1, project: { slug }, ...cfgFor({ file_loop: 'watch', ...review }) };
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'lock exclusion', tests: ['block-review-exclude'] }]));
  writeFile(inputs, 'coder.log', '$ node --test\n');
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await call(runBlock, ['open', 'B1', '--run', runId, '--level', 'L1', '--owned', ...owned.split(' '), '--acceptance', path.join(inputs, 'acceptance.json')], repo);
  assert.equal(opened.code, 0, opened.err);
  writeFile(repo, file, lock());
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'scaffold'], repo);
  await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(repo, 'a.mjs'), round: 1 });
  return { repo, runId, config, writeRow, file,
    rows: () => readAllRows(slug),
    close: () => call(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(inputs, 'coder.log')], repo),
  };
}

test('excluded scaffolding closes with one signed hash-bound row; disabling exclusion refuses unreviewed', async () => {
  const f = await fixture();
  const closed = await f.close();
  assert.equal(closed.code, 0, closed.err);
  assert.match(closed.out, /^not reviewed \(review.exclude \*\*\/package-lock.json\): package-lock.json$/m);
  assert.equal(closed.out.split('\n').filter((l) => l.startsWith('reviewed ')).length, 1, 'only the small source file is reviewed');
  const rows = (await f.rows()).filter((r) => r.event === 'gate.review_excluded');
  assert.equal(rows.length, 1, 'one audit row per excluded file');
  assert.deepEqual([rows[0].block, rows[0].file, rows[0].content_hash, rows[0].rule], ['B1', 'package-lock.json', contentHash(f.repo, 'package-lock.json'), '**/package-lock.json']);
  assert.deepEqual(verifyRow(rows[0], await loadKey(f.runId)), { ok: true });
  const disabled = await fixture({ exclude: [] });
  const refused = await disabled.close();
  assert.equal(refused.code, 1);
  assert.match(refused.err, /unreviewed package-lock.json/);
  assert.equal((await disabled.rows()).filter((r) => r.event === 'gate.review_excluded').length, 0);
  const unowned = await fixture({}, 'a.mjs');
  const scope = await unowned.close();
  assert.equal(scope.code, 1);
  assert.match(scope.err, /unowned_change package-lock.json/);
  assert.equal((await unowned.rows()).filter((r) => r.event === 'gate.review_excluded').length, 0);
});

test('npm excluded locks reject insecure URLs, foreign hosts, absent integrity and malformed JSON; allow configured registries and local entries', async () => {
  for (const file of ['package-lock.json', 'npm-shrinkwrap.json']) {
    for (const body of [lock('https://evil.test/a.tgz'), lock('http://registry.npmjs.org/a.tgz'), lock(undefined, ''), '{']) {
      const f = await fixture({}, `a.mjs ${file}`, file);
      writeFile(f.repo, file, body);
      const refused = await f.close();
      assert.equal(refused.code, 1, `${file}: ${refused.out}`);
      assert.match(refused.err, new RegExp(`lockfile ${file}:`));
      assert.equal((await f.rows()).filter((r) => r.event === 'gate.review_excluded').length, 0);
    }
  }
  const allowed = await fixture({ lockfile_registries: ['mirror.example.test'] });
  writeFile(allowed.repo, allowed.file, lock('https://mirror.example.test/example.tgz'));
  assert.equal((await allowed.close()).code, 0);
  const other = await fixture({}, 'a.mjs yarn.lock', 'yarn.lock');
  writeFile(other.repo, other.file, 'other lock format is not parsed');
  assert.equal((await other.close()).code, 0);
});

test('watch, enqueue, worker and review-file skip excluded files; wait-block lists excluded and finishes with the reviewed source', async () => {
  const f = await fixture();
  await watchTick({ repoRoot: f.repo, runId: f.runId, cfg: f.config, writeRow: f.writeRow });
  const tickets = blockTickets({ repoRoot: f.repo, run: f.runId, block: 'B1' });
  assert.deepEqual(tickets.map((t) => t.file), ['a.mjs'], 'watch queues only source');
  for (const queued of [enqueue({ repoRoot: f.repo, run: f.runId, block: 'B1', file: f.file }), enqueueHash({ repoRoot: f.repo, run: f.runId, block: 'B1', file: f.file, content_hash: contentHash(f.repo, f.file) })]) {
    assert.equal(queued.status, 'excluded');
    assert.equal(queued.ticket, undefined, 'no ticket for an excluded file');
  }
  const h = harness({ repoRoot: f.repo, runId: f.runId, cfg: f.config });
  const skipped = await reviewTicket({ ticket: 'a'.repeat(24), run: f.runId, block: 'B1', file: f.file, content_hash: contentHash(f.repo, f.file), enqueued_at: new Date().toISOString() }, h.ctx);
  assert.equal(skipped.status, 'excluded');
  assert.equal(h.rows.length, 0, 'no model review rows');
  const manual = await call(runReviewFile, [f.file, '--block', 'B1', '--run', f.runId], f.repo);
  assert.deepEqual(manual, { code: 0, out: `not reviewed (review.exclude **/package-lock.json): package-lock.json\n`, err: '' });
  announceWorker(f.repo, { pid: process.pid, run: f.runId });
  writeResult({ repoRoot: f.repo, runId: f.runId, ticket: tickets[0].ticket, result: { status: 'reviewed', approved: true }, key: await loadKey(f.runId) });
  const waited = await call(runReviewFile, ['--wait-block', 'B1', '--run', f.runId, '--max', '1s'], f.repo);
  assert.equal(waited.code, 0, waited.err);
  const status = JSON.parse(waited.out);
  assert.equal(status.done, true);
  assert.deepEqual(status.files.map(({ file, status, ticket }) => ({ file, status, ticket })), [{ file: 'a.mjs', status: 'done', ticket: tickets[0].ticket }, { file: f.file, status: 'excluded', ticket: null }]);
  assert.equal(blockTickets({ repoRoot: f.repo, run: f.runId, block: 'B1' }).length, 1);
});
