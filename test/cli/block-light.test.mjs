import { cfgFor, commitAll, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, readRun, saveRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');
let seq = 0;

async function fixture(light = undefined) {
  const repo = await makeRepo(), logs = freshDir('light-logs');
  const slug = `light-${++seq}`, runId = `r-${slug}`, config = { ...cfgFor(), ...(light ? { light } : {}) };
  writeFile(repo, 'a.mjs', 'old\n');
  await commitAll(repo);
  writeFile(logs, 'acceptance.json', JSON.stringify([{ clause: 'small', tests: ['light'] }]));
  writeFile(logs, 'coder.log', 'safe\n');
  const probe = async () => 'start', writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 42, probe, writeRow, config });
  const invoke = async (args) => {
    let out = '', err = '';
    const code = await runBlock([...args, '--run', runId], { probe, stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } } });
    return { code, out, err };
  };
  const open = () => invoke(['open', 'B1', '--level', 'L2', '--kind', 'light', '--owned', '*.mjs', '--acceptance', `${logs}/acceptance.json`]);
  const prepare = async (files) => {
    // Seed the pre-existing kind so close behavior has an assertion red independently of open.
    if (!(await readRun(runId)).blocks.B1) await openBlock({ runId, id: 'B1', level: 'L2', owned: ['*.mjs'], acceptance: [{ clause: 'small', tests: ['light'] }], writeRow });
    const record = await readRun(runId);
    record.blocks.B1.kind = 'light';
    await saveRun(record);
    for (const [file, text] of Object.entries(files)) {
      writeFile(repo, file, text);
      await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file, content_hash: contentHash(repo, file) });
    }
  };
  return { repo, runId, config, open, prepare, rows: () => readAllRows(slug), close: () => invoke(['close', 'B1', '--worker-pid', '42', '--transcript', `${logs}/coder.log`]) };
}

test('B139: open records light kind and config accepts positive integer limits with defaults', async () => {
  const f = await fixture({ max_files: 5, max_lines: 300 });
  const opened = await f.open();
  assert.equal(opened.code, 0, opened.err);
  assert.equal((await readRun(f.runId)).blocks.B1.kind, 'light');
  for (const light of [undefined, {}, { max_files: 1, max_lines: 1 }]) {
    const result = await validateConfig({ version: 1, ...cfgFor(), ...(light ? { light } : {}) }, { hasCliOnPath: () => true });
    assert.equal(result.errors.length, 0, JSON.stringify(result.errors));
  }
});

test('B139: light config refuses zero, negative, fractional, string and unknown limits', async () => {
  for (const light of [{ max_files: 0 }, { max_lines: -1 }, { max_files: 1.5 }, { max_lines: '300' }, { other: 1 }]) {
    const result = await validateConfig({ version: 1, ...cfgFor(), light }, { hasCliOnPath: () => true });
    assert.equal(result.valid, false, JSON.stringify(light));
    assert.equal(result.errors.length, 1, JSON.stringify(result.errors));
    assert.equal(result.errors[0].keyword, Object.hasOwn(light, 'other') ? 'additionalProperties' : typeof Object.values(light)[0] === 'string' || light.max_files === 1.5 ? 'type' : 'minimum');
  }
});

test('B139: close refuses exact light file and added-plus-removed line counts, including untracked files', async () => {
  for (const [light, files, counts] of [
    [undefined, Object.fromEntries(['a', 'b', 'c', 'd', 'e', 'f'].map((n) => [`${n}.mjs`, 'new\n'])), '6 files, 7 lines'],
    [undefined, { 'a.mjs': 'new\n'.repeat(300) }, '1 files, 301 lines'],
    [{ max_files: 1, max_lines: 2 }, { 'a.mjs': 'new\n', 'b.mjs': 'new\n' }, '2 files, 3 lines'],
  ]) {
    const f = await fixture(light);
    await f.prepare(files);
    const result = await f.close();
    assert.equal(result.code, 1, result.err);
    assert.match(result.err, new RegExp(`light-limits ${counts}`));
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 0);
  }
});

test('B139: close at both limits succeeds and emits one auto ticket line per file', async () => {
  const f = await fixture();
  assert.equal((await f.open()).code, 0, 'light must open before closing at the limits');
  await f.prepare({ 'a.mjs': 'new\n'.repeat(295), 'b.mjs': 'b\n', 'c.mjs': 'c\n', 'd.mjs': 'd\n', 'e.mjs': 'e\n' });
  const result = await f.close();
  assert.equal(result.code, 0, result.err);
  assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
  assert.equal((result.out.match(/^reviewed \S+ ticket [a-f0-9]{24}$/gm) ?? []).length, 5, result.out);
  assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 1);
});
