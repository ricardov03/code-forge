import { commitAll, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, unlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const gate = await import('../../src/review/gate-check.mjs');
// Before this feature, close removed no files from the reviewed set. Keep the base run
// executable so red is a behavior assertion, rather than a missing-export crash.
const revertedToBase = gate.revertedToBase ?? (async () => []);
const { contentHash, hashBytes, ticketId } = await import('../../src/worker/ticket.mjs');
const { startRun, readRun, writeSigned } = await import('../../src/state/run.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');

const H0 = 'export const value = 0;\n';
const H1 = 'export const value = 1;\n';
const H2 = 'export const value = 2;\n';
const probe = async (/** @type {number} */ pid) => pid === 4242 ? 'start-A' : null;
const review = (/** @type {string} */ file, content_hash = hashBytes(Buffer.from(H1))) =>
  ({ event: 'review.approved', block: 'B1', file, content_hash });

test('revertedToBase returns sorted base-byte reverts, including an added then deleted file, but keeps changed bytes', async () => {
  const repoRoot = await makeRepo();
  const binary = Buffer.from([0, 255, 254, 128, 10]);
  writeFile(repoRoot, 'z.mjs', H0);
  writeFile(repoRoot, 'changed.mjs', H0);
  writeFileSync(path.join(repoRoot, 'binary'), binary);
  await commitAll(repoRoot);
  const base = (await git(['rev-parse', 'HEAD'], repoRoot)).trim();
  writeFile(repoRoot, 'z.mjs', H1);
  writeFile(repoRoot, 'changed.mjs', H1);
  writeFile(repoRoot, 'added.mjs', H1);
  const rows = ['z.mjs', 'changed.mjs', 'added.mjs', 'binary'].map((file) => review(file));
  writeFile(repoRoot, 'z.mjs', H0);
  unlinkSync(path.join(repoRoot, 'added.mjs'));
  assert.deepEqual(await revertedToBase({ repoRoot, base, files: ['z.mjs', 'changed.mjs', 'binary', 'added.mjs'], rows, block: 'B1' }), ['added.mjs', 'binary', 'z.mjs']);
});

test('current-hash review rows and legacy rows keep base files; other blocks and non-review events do not', async () => {
  const repoRoot = await makeRepo();
  const events = ['review.result', 'review.round', 'review.approved', 'review.triage', 'review.cap', 'review.late_finding'];
  const files = ['reverted.mjs', 'legacy.mjs', ...events.map((_, i) => `current-${i}.mjs`)];
  for (const file of files) writeFile(repoRoot, file, H0);
  await commitAll(repoRoot);
  const base = (await git(['rev-parse', 'HEAD'], repoRoot)).trim();
  const current = contentHash(repoRoot, 'reverted.mjs');
  const rows = [
    ...files.map((file) => review(file)),
    ...events.map((event, i) => ({ event, block: 'B1', file: `./current-${i}.mjs`, content_hash: current })),
    { event: 'review.round', block: 'B1', file: 'legacy.mjs' },
    { ...review('reverted.mjs', current), block: 'B2' },
    { ...review('reverted.mjs', current), event: 'dispatch' },
  ];
  assert.deepEqual(await revertedToBase({ repoRoot, base, files, rows, block: 'B1' }), ['reverted.mjs']);
});

test('revert checks use supplied rows without a ledger directory and keep files when git cannot read the base', async () => {
  const repoRoot = await makeRepo();
  writeFile(repoRoot, 'a.mjs', H0);
  await commitAll(repoRoot);
  const base = (await git(['rev-parse', 'HEAD'], repoRoot)).trim();
  const rows = [review('a.mjs'), review('absent.mjs')];
  const files = ['a.mjs', 'absent.mjs'];
  assert.equal(existsSync(path.join(repoRoot, '.code-forge')), false);
  assert.deepEqual(await revertedToBase({ repoRoot, base: '0'.repeat(40), files, rows, block: 'B1' }), []);
  assert.deepEqual(await revertedToBase({ repoRoot, base, files, rows, block: 'B1' }), ['a.mjs', 'absent.mjs']);
  assert.equal(existsSync(path.join(repoRoot, '.code-forge')), false);
});

/** @param {string} name @param {string[]} files */
async function closeFixture(name, files) {
  const repo = await makeRepo();
  for (const file of files) writeFile(repo, file, H0);
  writeFile(repo, 'coder.log', '$ node --test\n');
  writeFile(repo, 'acceptance.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
  await commitAll(repo);
  const runId = `r-revert-${name}`;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: `revert-${name}` });
  await startRun({ workspace: repo, project: `revert-${name}`, runId, workerPid: 4242, writeRow, probe });
  /** @param {string[]} args */
  const invoke = async (args) => {
    let output = '';
    const stream = { write: (/** @type {string} */ s) => { output += s; return true; } };
    const code = await runBlock(args, { stdout: stream, stderr: stream, probe });
    return { code, output };
  };
  assert.equal((await invoke(['open', 'B1', '--run', runId, '--level', 'L2', '--owned', ...files, '--acceptance', path.join(repo, 'acceptance.json')])).code, 0);
  const close = () => invoke(['close', 'B1', '--run', runId, '--transcript', path.join(repo, 'coder.log')]);
  const approve = (/** @type {string} */ file) => writeSigned(runId, writeRow, review(file, contentHash(repo, file)));
  const reviewedLine = (/** @type {string} */ file) => `reviewed ${file} ticket ${ticketId({ run: runId, block: 'B1', file, content_hash: contentHash(repo, file) })}\n`;
  const status = async () => (await readRun(runId)).blocks.B1.status;
  return { repo, close, approve, reviewedLine, status };
}

test('close omits reverted files, retains non-reverted files and current-hash base reviews, and counts only retained reviews in its warning', async () => {
  for (const revert of [true, false]) {
    const { repo, close, approve, reviewedLine, status } = await closeFixture(`listing-${revert}`, ['src/a.mjs', 'src/b.mjs']);
    writeFile(repo, 'src/a.mjs', H1);
    await approve('src/a.mjs');
    await approve('src/b.mjs');
    if (revert) writeFile(repo, 'src/a.mjs', H0);
    const warning = revert ? 'WARN block B1: no owned file differs from the block base in the workspace, but 1 owned file(s) have review rows — the work was likely committed before block open or done in another worktree; they are checked at their content in the workspace\n' : '';
    assert.deepEqual(await close(), { code: 0, output: `${warning}${revert ? '' : reviewedLine('src/a.mjs')}${reviewedLine('src/b.mjs')}block B1 closed\n` });
    assert.equal(await status(), 'closed');
  }
});

test('customer case: approve H1 then revert the only file to H0 closes normally; a block with no reviews still refuses', async () => {
  const { repo, close, approve, status } = await closeFixture('customer', ['src/a.mjs']);
  assert.deepEqual(await close(), { code: 1, output: 'block B1 open: no_changes\n' });
  assert.equal(await status(), 'open');
  writeFile(repo, 'src/a.mjs', H1);
  await approve('src/a.mjs');
  writeFile(repo, 'src/a.mjs', H0);
  assert.deepEqual(await close(), { code: 0, output: 'block B1 closed\n' });
  assert.equal(await status(), 'closed');
});

test('customer boundary: a second file at unapproved H2 still needs review while the H0 revert is omitted', async () => {
  const { repo, close, approve, reviewedLine, status } = await closeFixture('customer-h2', ['src/a.mjs', 'src/b.mjs']);
  for (const file of ['src/a.mjs', 'src/b.mjs']) {
    writeFile(repo, file, H1);
    await approve(file);
  }
  writeFile(repo, 'src/a.mjs', H0);
  writeFile(repo, 'src/b.mjs', H2);
  assert.deepEqual(await close(), { code: 1, output: 'block B1 open: unreviewed src/b.mjs\n' });
  assert.equal(await status(), 'open');
  await approve('src/b.mjs');
  assert.deepEqual(await close(), { code: 0, output: `${reviewedLine('src/b.mjs')}block B1 closed\n` });
  assert.equal(await status(), 'closed');
});
