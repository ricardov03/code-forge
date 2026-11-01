import { cfgFor, commitAll, freshDir, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { unlinkSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { blockFileSet } = await import('../../src/review/gate-check.mjs');
const { DELETED_HASH, contentHash } = await import('../../src/worker/ticket.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');

const TRANSIENT = 'src/transient.mjs';

test('blockFileSet omits a path absent both at base and now', async () => {
  const repo = await makeRepo();
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  writeFile(repo, TRANSIENT, 'temporary\n');
  unlinkSync(path.join(repo, TRANSIENT));

  const files = await blockFileSet({ repoRoot: repo, base, owned: [TRANSIENT], changed: [TRANSIENT] });
  assert.deepEqual(files, [], 'the created-then-deleted path contributes zero files');
});

test('blockFileSet keeps exactly the deletion that existed at base with DELETED_HASH', async () => {
  const repo = await makeRepo();
  const deleted = 'src/deleted.mjs';
  writeFile(repo, deleted, 'original\n');
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  unlinkSync(path.join(repo, deleted));

  const files = await blockFileSet({ repoRoot: repo, base, owned: ['src'], changed: [TRANSIENT, deleted] });
  assert.deepEqual(files, [{ file: deleted, content_hash: DELETED_HASH }], 'only the real deletion needs review');
});

test('blockFileSet keeps exactly the modified file with its content hash', async () => {
  const repo = await makeRepo();
  const modified = 'src/modified.mjs';
  writeFile(repo, modified, 'original\n');
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const bytes = 'modified\n';
  writeFile(repo, modified, bytes);

  const files = await blockFileSet({ repoRoot: repo, base, owned: ['src'], changed: [TRANSIENT, modified] });
  assert.deepEqual(files, [{ file: modified, content_hash: createHash('sha256').update(bytes).digest('hex') }], 'the modified bytes keep their hash');
});

test('blockFileSet checks base existence only for files deleted now', async () => {
  const repo = await makeRepo();
  const modified = ['src/first.mjs', 'src/second.mjs'];
  for (const file of modified) writeFile(repo, file, 'original\n');
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  for (const file of modified) writeFile(repo, file, 'modified\n');
  writeFile(repo, TRANSIENT, 'temporary\n');
  unlinkSync(path.join(repo, TRANSIENT));
  const calls = [];

  const files = await blockFileSet({
    repoRoot: repo, base, owned: ['src'], changed: [...modified, TRANSIENT],
    gitRun: async (argv, opts) => {
      calls.push({ argv, opts });
      return { result: 'ok', stdout: '' };
    },
  });

  assert.equal(calls.length, 1, 'two modified files and one transient deletion cost exactly one base check');
  assert.deepEqual(calls[0].argv, ['git', 'ls-tree', '--name-only', '-z', base, '--', TRANSIENT]);
  assert.equal(calls[0].opts.cwd, repo);
  assert.equal(calls[0].opts.timeoutMs, 30000);
  assert.deepEqual(files, modified.map((file) => ({ file, content_hash: createHash('sha256').update('modified\n').digest('hex') })));
});

test('blockFileSet keeps a deleted file when its base check times out', async () => {
  const repo = await makeRepo();
  const deleted = 'src/deleted.mjs';
  writeFile(repo, deleted, 'original\n');
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  unlinkSync(path.join(repo, deleted));
  let calls = 0;

  const files = await blockFileSet({
    repoRoot: repo, base, owned: ['src'], changed: [deleted],
    gitRun: async () => {
      calls += 1;
      return { result: 'failed', timedOut: true };
    },
  });
  assert.equal(calls, 1, 'the deleted file uses the timed-out base check');
  assert.deepEqual(files, [{ file: deleted, content_hash: DELETED_HASH }]);
});

test('blockFileSet keeps a deleted file when the base SHA is invalid', async () => {
  const repo = await makeRepo();
  const deleted = 'src/deleted.mjs';
  writeFile(repo, deleted, 'original\n');
  await commitAll(repo);
  unlinkSync(path.join(repo, deleted));

  const files = await blockFileSet({ repoRoot: repo, base: '0'.repeat(40), owned: ['src'], changed: [deleted] });
  assert.deepEqual(files, [{ file: deleted, content_hash: DELETED_HASH }]);
});

test('block close accepts a reviewed file created then deleted alongside approved work', async () => {
  const repo = await makeRepo();
  const kept = 'src/kept.mjs';
  writeFile(repo, kept, 'original\n');
  await commitAll(repo);
  const logs = freshDir('logs');
  writeFile(logs, 'coder.log', '$ node --test\n');
  const runId = 'r-created-deleted';
  const probe = async () => 'start';
  const writeRow = (row) => appendRow(row, { slug: 'created-deleted' });
  await startRun({ workspace: repo, project: 'created-deleted', runId, workerPid: 4242, probe, writeRow, config: cfgFor() });
  await openBlock({ runId, id: 'B1', level: 'L1', owned: [kept], acceptance: [{ clause: 'close created then deleted', tests: ['close-created-deleted'] }], writeRow });
  const invoke = async (args) => {
    let stdout = '', stderr = '';
    const code = await runBlock([...args, '--run', runId], {
      probe,
      stdout: { write: (s) => { stdout += s; } },
      stderr: { write: (s) => { stderr += s; } },
    });
    return { code, stdout, stderr };
  };

  writeFile(repo, TRANSIENT, 'temporary\n');
  assert.deepEqual(await invoke(['claim', 'B1', TRANSIENT]), { code: 0, stdout: `block B1 claimed ${TRANSIENT}\n`, stderr: '' });
  for (const file of [TRANSIENT, kept]) {
    if (file === kept) writeFile(repo, kept, 'modified\n');
    await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file, content_hash: contentHash(repo, file) });
  }
  unlinkSync(path.join(repo, TRANSIENT));

  const result = await invoke(['close', 'B1', '--worker-pid', '4242', '--transcript', path.join(logs, 'coder.log')]);
  assert.equal(result.code, 0, result.stderr);
  assert.equal(result.stderr, '', 'close has zero refusals for the transient file');
  assert.equal(result.stdout.split('\n').filter((line) => line.startsWith('reviewed ')).length, 1, 'close reviews exactly the retained file');
  assert.match(result.stdout, /^reviewed src\/kept\.mjs ticket \S+\nblock B1 closed\n$/);
});
