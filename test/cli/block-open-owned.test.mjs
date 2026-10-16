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
  writeFile(repo, 'file,comma.mjs', 'export const comma = 1;\n');
  writeFile(repo, 'file [id] with space.mjs', 'export const bracket = 1;\n');
  mkdirSync(path.join(repo, 'dir [id] with space'));
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

test('B135b: commas outside braces or whitespace in missing owned and tests entries refuse without writes', async () => {
  const f = await fixture('b118-owned');
  const record = readFileSync(runRecordPath(f.runId), 'utf8');
  const rows = await f.rows();
  for (const label of ['owned', 'tests']) {
    for (const entry of ['a.mjs,b.mjs', 'a.mjs b.mjs', 'a.mjs\tb.mjs', 'a.mjs\nb.mjs',
      'src/{a,b}.mjs,other.mjs', 'src/{a,{b,c}}.mjs,other.mjs', 'src/{a, b}.mjs',
      'app/[id]/missing file.tsx', 'test dir/new.test.mjs',
      ...(label === 'owned' ? ['src/*.mjs,other.mjs', 'src/? name.mjs'] : [])]) {
      const flags = label === 'owned' ? ['--owned', entry] : ['--owned', 'a.mjs', '--tests', entry];
      assert.deepEqual(await f.open('B1', flags), {
        code: 2, stdout: '', stderr: `block open: ${label} entry ${JSON.stringify(entry)} matches no file; use spaces, not commas\n`,
      });
      assert.equal(readFileSync(runRecordPath(f.runId), 'utf8'), record);
      assert.deepEqual(await f.rows(), rows);
    }
  }
});

test('B135b boundary: literal separator paths, new plain paths, brace globs and bracket paths open', async () => {
  const f = await fixture('b118-owned-boundary');
  const owned = ['file with space.mjs', 'file,comma.mjs', 'file [id] with space.mjs', 'dir [id] with space', 'new.mjs', 'src/glob/**', 'src/{a,b}.mjs', 'src/{a,{b,c}}.mjs', 'app/[id]/page.tsx'];
  const tests = ['test dir', 'file,comma.mjs', 'missing.mjs', 'missing/new.test.mjs', 'test/{a,b}.test.mjs', 'app/[id]/page.test.tsx'];
  const accepted = await f.open('B1', ['--owned', ...owned, '--tests', ...tests]);
  assert.deepEqual([accepted.code, accepted.stderr], [0, '']);
  const block = (await readRun(f.runId)).blocks.B1;
  assert.deepEqual([block.owned_files, block.test_files], [owned, tests]);
  assert.equal(JSON.parse(accepted.stdout).tests, tests.length);
  const plainTest = await f.open('B4', ['--owned', 'b.mjs', '--tests', 'new.test.ts']);
  assert.deepEqual([plainTest.code, plainTest.stderr], [0, '']);
  assert.deepEqual((await readRun(f.runId)).blocks.B4.test_files, ['new.test.ts']);
});
