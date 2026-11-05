import { cfgFor, freshDir, git, writeFile } from './review/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { mock, test } from 'node:test';

// Load the restart and done-file readers before counting the close checks' ledger
// snapshot. Those separate lifecycle readers are outside the report check.
const { runReviewFile } = await import('../src/cli/review-file.mjs');
await import('../src/state/block-end.mjs');
const ledger = await import('../src/ledger/write.mjs');
let ledgerReads = 0;
mock.module('../src/ledger/write.mjs', { namedExports: { ...ledger,
  readAllRows: async (slug) => { ledgerReads += 1; return ledger.readAllRows(slug); },
} });

const { runBlock } = await import('../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../src/state/run.mjs');
const { loadKey } = await import('../src/state/signer.mjs');
const { contentHash } = await import('../src/worker/ticket.mjs');
const { announceWorker, resolveTicket, retractWorker, writeResult } = await import('../src/worker/queue.mjs');
const PROBE = async (pid) => pid === 4242 ? 'start-A' : null;
const SOURCE = 'src/value.mjs';
const TEST = 'test/value.test.mjs';
let sequence = 0;

async function call(run, args, cwd, deps = {}) {
  let out = '', err = '';
  const code = await run(args, { cwd, probe: PROBE,
    stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, ...deps });
  return { code, out, err };
}

async function fixture() {
  const slug = `close-tickets-${++sequence}`;
  const repo = freshDir(slug), inputs = freshDir('inputs');
  const config = { version: 1, project: { slug }, ...cfgFor({ file_loop: 'watch' }) };
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, SOURCE, 'export const value = 1;\n');
  writeFile(repo, TEST, 'export const expected = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'current reviews count', tests: ['block-close-tickets'] }]));
  writeFile(inputs, 'coder.log', '$ node --test\n');
  const report = path.join(inputs, 'report.md');
  const text = 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n';
  writeFile(inputs, 'report.md', text);
  const runId = `r-${slug}`;
  const writeRow = (row) => ledger.appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await call(runBlock, ['open', 'B1', '--run', runId, '--level', 'L1', '--owned', SOURCE, TEST,
    '--acceptance', path.join(inputs, 'acceptance.json')], repo);
  assert.equal(opened.code, 0, opened.err);

  // Queue with the real review-file verb; stand in only for the offline review worker.
  const review = async (file, round) => {
    announceWorker(repo, { pid: process.pid, run: runId });
    const queued = await call(runReviewFile, [file, '--block', 'B1', '--run', runId], repo);
    retractWorker(repo, process.pid);
    assert.equal(queued.code, 0, queued.err || queued.out);
    const ticket = JSON.parse(queued.out).ticket;
    const content_hash = contentHash(repo, file);
    const key = await loadKey(runId);
    await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file, content_hash, round });
    const result = { event: 'review.result', block: 'B1', file, content_hash, status: 'reviewed', approved: true,
      open: [], finished_at: new Date().toISOString() };
    writeResult({ repoRoot: repo, runId, ticket, result, key });
    await writeSigned(runId, writeRow, { ...result, ticket });
    return ticket;
  };
  writeFile(repo, SOURCE, 'export const value = 2;\n');
  writeFile(repo, TEST, 'export const expected = 2;\n');
  const sourceTicket = await review(SOURCE, 1);
  const oldTestTicket = await review(TEST, 1);
  writeFile(repo, TEST, 'export const expected = 2; // fixed assertion\n');
  const testTicket = await review(TEST, 2);
  assert.notEqual(testTicket, oldTestTicket, 'the fix round records a new ticket for the test bytes');
  const close = async (deps = {}) => {
    ledgerReads = 0;
    const result = await call(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--report', report,
      '--transcript', path.join(inputs, 'coder.log'), '--json'], repo, deps);
    assert.equal(ledgerReads, 1, 'close reads the project ledger exactly once for both files');
    return { ...result, json: JSON.parse(result.out) };
  };
  return { repo, runId, inputs, text, close, writeRow, sourceTicket, testTicket, oldTestTicket };
}

// Ticket resolution is a best-effort output path: a queue lookup failure must not
// hide a completed review already recorded in the ledger.
const missingTestLine = { resolveTicket: (opts) => {
  if (opts.file === TEST) throw new Error('queue lookup unavailable');
  return resolveTicket(opts);
} };

test('close accepts no hand ticket lines after a source review and a test fix-round review', async () => {
  for (const deps of [{}, missingTestLine]) {
    const f = await fixture();
    const closed = await f.close(deps);
    assert.equal(closed.code, 0, closed.err);
    assert.deepEqual([closed.json.block, closed.json.event, closed.json.status], ['B1', 'closed', 'closed']);
    assert.deepEqual(closed.json.reviewed.map(({ file, ticket }) => ({ file, ticket })), deps === missingTestLine
      ? [{ file: SOURCE, ticket: f.sourceTicket }]
      : [{ file: SOURCE, ticket: f.sourceTicket }, { file: TEST, ticket: f.testTicket }]);
    assert.equal(closed.err, deps === missingTestLine ? `WARN no ticket for ${TEST}\n` : '');
  }
});

test('close refuses stale test bytes with coder_report_failed and warns on a wrong hand ticket', async () => {
  const stale = await fixture();
  writeFile(stale.repo, TEST, 'export const expected = 3; // not reviewed\n');
  const refused = await stale.close();
  assert.equal(refused.code, 1);
  assert.deepEqual(refused.json.blockers.filter((b) => b.code === 'coder_report_failed').map((b) => b.file), [TEST],
    'only the unreviewed test bytes need a ticket; the reviewed source needs no hand line');
  assert.deepEqual(refused.json.blockers.filter((b) => b.code === 'unreviewed').map((b) => b.file), [TEST]);

  for (const deps of [{}, missingTestLine]) {
    const f = await fixture();
    writeFile(f.inputs, 'report.md', `reviewed ${TEST} ticket ${f.oldTestTicket}\n${f.text}`);
    const closed = await f.close(deps);
    assert.equal(closed.code, 0, closed.err);
    assert.equal(closed.err, (deps === missingTestLine ? `WARN no ticket for ${TEST}\n` : '') +
      `WARN report ticket mismatch ${TEST}\n`, 'exactly one mismatch warning names the test and its obsolete ticket');
  }
});

test('close does not credit another block, run, unfinished or unsigned review for current bytes', async () => {
  for (const variant of ['block', 'run', 'unfinished', 'unsigned']) {
    const f = await fixture();
    writeFile(f.repo, TEST, 'export const expected = 3; // different bytes\n');
    const row = { event: 'review.result', block: 'B1', file: TEST, content_hash: contentHash(f.repo, TEST),
      ticket: 'a'.repeat(24), status: 'reviewed', approved: true, open: [],
      ...(variant === 'block' ? { block: 'B2' } : {}),
      ...(variant === 'run' ? { run: 'r-other' } : {}),
      ...(variant === 'unfinished' ? { status: 'unavailable' } : {}) };
    if (variant === 'unsigned') await f.writeRow({ ...row, run: f.runId });
    else await writeSigned(f.runId, f.writeRow, row);
    const refused = await f.close();
    assert.equal(refused.code, 1, `${variant}: current test bytes must refuse`);
    assert.deepEqual(refused.json.blockers.filter((b) => b.code === 'coder_report_failed').map((b) => b.file), [TEST],
      `${variant}: exactly the test lacks a finished review in this block and run`);
  }
});

test('close keeps coder_report_failed for a signed current review that left an open finding', async () => {
  const f = await fixture();
  writeFile(f.repo, TEST, 'export const expected = 3; // finding still open\n');
  const content_hash = contentHash(f.repo, TEST);
  const { ticket } = resolveTicket({ repoRoot: f.repo, run: f.runId, block: 'B1', file: TEST });
  await writeSigned(f.runId, f.writeRow, { event: 'review.round', block: 'B1', file: TEST, content_hash,
    round: 3, kind: 'full', open_after: 1 });
  await writeSigned(f.runId, f.writeRow, { event: 'review.result', block: 'B1', file: TEST, content_hash,
    ticket, status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }],
    finished_at: new Date().toISOString() });

  const refused = await f.close();
  assert.equal(refused.code, 1);
  assert.deepEqual(refused.json.blockers.filter((b) => b.code === 'not_approved').map((b) => b.file), [TEST]);
  assert.deepEqual(refused.json.blockers.filter((b) => b.code === 'coder_report_failed').map((b) => b.file), [TEST],
    'a completed review that did not approve the current bytes cannot replace a report ticket line');
});
