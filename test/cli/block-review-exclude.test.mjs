import { cfgFor, freshDir, git, harness, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { mock, test } from 'node:test';

const realReview = await import('../../src/review/engine.mjs');
const packets = [];
mock.module('../../src/review/engine.mjs', { namedExports: { ...realReview,
  reviewFile: async (input) => {
    packets.push({ file: input.file, peers: input.peers, diffs: (await input.peerDiffs()).map((d) => d.file) });
    return { status: 'reviewed', approved: true, engine: 'adaptive', sessions: [] };
  },
} });

const { runBlock } = await import('../../src/cli/block.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, blockTickets, enqueue, enqueueAnswers, enqueueHash, resolveTicket, writeResult } = await import('../../src/worker/queue.mjs');
const { watchTick } = await import('../../src/worker/watch.mjs');
const { reviewTicket } = await import('../../src/worker/engine.mjs');
const PROBE = async (pid) => pid === 4242 ? 'start-A' : null;
let sequence = 0;

async function call(run, args, cwd, deps = {}) {
  let out = '', err = '';
  const code = await run(args, { cwd, stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, probe: PROBE, pollMs: 1, ...deps });
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
    report: (text) => {
      writeFile(inputs, 'report.md', text);
      return path.join(inputs, 'report.md');
    },
    close: (extra = [], deps = {}) => call(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(inputs, 'coder.log'), ...extra], repo, deps),
  };
}

test('excluded scaffolding closes with one signed hash-bound row; disabling exclusion refuses unreviewed', async () => {
  const f = await fixture();
  const ticket = resolveTicket({ repoRoot: f.repo, run: f.runId, block: 'B1', file: 'a.mjs' }).ticket;
  const report = f.report(`reviewed a.mjs ticket ${ticket}\nTESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n`);
  const closed = await f.close(['--report', report]);
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

test('report still needs the reviewed source ticket when the tool cannot supply it; excluded locks need none', async () => {
  const f = await fixture();
  const ticket = resolveTicket({ repoRoot: f.repo, run: f.runId, block: 'B1', file: 'a.mjs' }).ticket;
  const deps = { resolveTicket: () => { throw new Error('ticket unavailable'); } };
  const text = 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n';
  const report = f.report(text);
  const before = await f.rows();
  assert.deepEqual(await f.close(['--report', report], deps), {
    code: 1, out: '', err: 'block B1 open: coder report FAILED: no review-file ticket id for a.mjs\n',
  });
  assert.deepEqual(await f.rows(), before, 'refused close writes no rows, including exclusion audit rows');
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
  f.report(`reviewed a.mjs ticket ${ticket}\n${text}`);
  const closed = await f.close(['--report', report], deps);
  assert.equal(closed.code, 0, closed.err);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
  assert.equal((await f.rows()).filter((r) => r.event === 'gate.review_excluded').length, 1);
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

test('v1 npm locks refuse nested dependencies resolved on a foreign host', async () => {
  for (const file of ['package-lock.json', 'npm-shrinkwrap.json']) {
    for (const packages of [undefined, {}]) {
      const f = await fixture({}, `a.mjs ${file}`, file);
      writeFile(f.repo, file, JSON.stringify({ lockfileVersion: 1, packages, dependencies: {
        local: { dependencies: { parent: { dependencies: {
          nested: { resolved: 'https://evil.test/nested.tgz', integrity: 'sha512-example' },
        } } } },
      } }));
      const refused = await f.close();
      assert.equal(refused.code, 1, `${file}: ${refused.out}`);
      assert.match(refused.err, new RegExp(`lockfile ${file}:.*review.lockfile_registries`));
      assert.equal((await f.rows()).filter((r) => r.event === 'gate.review_excluded').length, 0);
    }
  }
});

test('v1 npm locks refuse resolved dependencies without integrity at any depth', async () => {
  for (const file of ['package-lock.json', 'npm-shrinkwrap.json']) {
    for (const nested of [false, true]) {
      const dependency = { resolved: 'https://registry.npmjs.org/example/-/example-1.tgz' };
      const f = await fixture({}, `a.mjs ${file}`, file);
      writeFile(f.repo, file, JSON.stringify({ lockfileVersion: 1, dependencies: {
        example: nested ? { dependencies: { nested: dependency } } : dependency,
      } }));
      const refused = await f.close();
      assert.equal(refused.code, 1, `${file}: ${refused.out}`);
      assert.match(refused.err, new RegExp(`lockfile ${file}:.*has no integrity`));
      assert.equal((await f.rows()).filter((r) => r.event === 'gate.review_excluded').length, 0);
    }
  }
});

test('clean v1 npm locks close with nested, local and bundled dependencies and configured registries', async () => {
  for (const file of ['package-lock.json', 'npm-shrinkwrap.json']) {
    for (const host of ['registry.npmjs.org', 'mirror.example.test']) {
      const f = await fixture({ lockfile_registries: [host] }, `a.mjs ${file}`, file);
      writeFile(f.repo, file, JSON.stringify({ lockfileVersion: 1, dependencies: {
        example: { resolved: `https://${host}/example.tgz`, integrity: 'sha512-example', dependencies: {
          local: { dependencies: { nested: { resolved: `https://${host}/nested.tgz`, integrity: 'sha512-nested' } } },
          bundled: { bundled: true },
        } },
      } }));
      const closed = await f.close();
      assert.equal(closed.code, 0, closed.err);
      assert.equal((await f.rows()).filter((r) => r.event === 'gate.review_excluded').length, 1);
    }
  }
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

test('excluded content never enters peer packets or answers reviews', async () => {
  const f = await fixture();
  const h = harness({ repoRoot: f.repo, runId: f.runId, cfg: f.config });
  packets.length = 0;
  const source = enqueue({ repoRoot: f.repo, run: f.runId, block: 'B1', file: 'a.mjs' });
  if (source.status === 'excluded') assert.fail('the source file must queue');
  await reviewTicket({ ...source, run: f.runId, block: 'B1', enqueued_at: new Date().toISOString() }, h.ctx);
  assert.deepEqual(packets, [{ file: 'a.mjs', peers: [], diffs: [] }], 'excluded locks never reach another file review as context');
  for (const [file, byFile] of [[f.file, 'a.mjs'], ['a.mjs', f.file]]) {
    const result = enqueueAnswers({ repoRoot: f.repo, run: f.runId, block: 'B1', file, byFile, answers: ['F1'] });
    assert.equal(result.status, 'excluded', `${file} answered by ${byFile} skips review`);
    assert.equal(result.ticket, undefined);
    const skipped = await reviewTicket({ ticket: 'b'.repeat(24), kind: 'answers', run: f.runId, block: 'B1', file, by_file: byFile, content_hash: contentHash(f.repo, file), enqueued_at: new Date().toISOString() }, h.ctx);
    assert.equal(skipped.status, 'excluded', 'previously queued answers also skip the model');
    const manual = await call(runReviewFile, [byFile, '--block', 'B1', '--run', f.runId, '--answers', 'F1', '--of', file], f.repo);
    assert.deepEqual(manual, { code: 0, out: `not reviewed (review.exclude **/package-lock.json): package-lock.json\n`, err: '' });
  }
  assert.equal(packets.length, 1, 'only the source review ran');
});
