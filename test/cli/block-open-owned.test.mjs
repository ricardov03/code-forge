import { freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, readRun } = await import('../../src/state/run.mjs');
const { runRecordPath } = await import('../../src/state/paths.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');

/** @param {string} slug */
async function fixture(slug) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'b.mjs', 'export const b = 1;\n');
  writeFile(repo, 'file with space.mjs', 'export const space = 1;\n');
  mkdirSync(path.join(repo, 'test dir'));
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const acceptance = path.join(freshDir('acceptance'), 'acc.json');
  writeFile(path.dirname(acceptance), 'acc.json', JSON.stringify([{ clause: 'owned paths', tests: ['b118'] }]));
  const runId = `r-${slug}`;
  await startRun({ workspace: repo, project: slug, runId, writeRow: (row) => appendRow(row, { slug }) });
  /** @param {string} id @param {string[]} paths */
  const open = async (id, paths) => {
    let stdout = '';
    let stderr = '';
    const code = await runBlock(['open', id, '--run', runId, '--level', 'L1', ...paths, '--acceptance', acceptance, '--json'], {
      stdout: { write: (s) => { stdout += s; } }, stderr: { write: (s) => { stderr += s; } },
    });
    return { code, stdout, stderr };
  };
  return { repo, runId, open, rows: () => readAllRows(slug) };
}

test('B118: a whitespace owned entry naming no file or directory refuses without writing', async () => {
  const f = await fixture('b118-owned');
  const record = readFileSync(runRecordPath(f.runId), 'utf8');
  const rows = await f.rows();
  for (const entry of ['a.mjs b.mjs', 'a.mjs\tb.mjs', 'a.mjs\nb.mjs']) {
    assert.deepEqual(await f.open('B1', ['--owned', entry]), {
      code: 1, stdout: '', stderr: `block open: owned files: invalid path ${JSON.stringify(entry)}: contains whitespace but names no existing file or directory\n`,
    });
    assert.equal(readFileSync(runRecordPath(f.runId), 'utf8'), record);
    assert.deepEqual(await f.rows(), rows);
  }
});

test('B118 boundary: existing space paths and new plain paths open; whitespace tests share the refusal', async () => {
  const f = await fixture('b118-owned-boundary');
  const accepted = await f.open('B1', ['--owned', 'file with space.mjs', 'new.mjs', '--tests', 'test dir']);
  assert.deepEqual([accepted.code, accepted.stderr], [0, '']);
  const block = (await readRun(f.runId)).blocks.B1;
  assert.deepEqual([block.owned_files, block.test_files], [['file with space.mjs', 'new.mjs'], ['test dir']]);
  assert.deepEqual(JSON.parse(accepted.stdout).tests, 1);
  const record = readFileSync(runRecordPath(f.runId), 'utf8');
  const rows = await f.rows();
  assert.deepEqual(await f.open('B2', ['--owned', 'a.mjs', '--tests', 'a.mjs b.mjs']), {
    code: 1, stdout: '', stderr: 'block open: test files: invalid path "a.mjs b.mjs": contains whitespace but names no existing file or directory\n',
  });
  assert.equal(readFileSync(runRecordPath(f.runId), 'utf8'), record);
  assert.deepEqual(await f.rows(), rows);
});
