import { cli, makeRepo } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { parseSentinel } = await import('../../src/engines/sentinel.mjs');
const { startRun } = await import('../../src/state/run.mjs');

test('B172: pack prints its content, including the block brief and every acceptance clause', async () => {
  const { repo, runId } = await makeRepo({ start: false });
  await startRun({ workspace: repo, project: 'worker-test', runId, writeRow: async () => {} });
  writeFileSync(path.join(repo, 'brief.md'), '# B172 task\nImplement the exact requested behavior.\n');
  writeFileSync(path.join(repo, 'acceptance.yml'), '- clause: the answer includes the brief\n  tests: [context-pack]\n- clause: the human can inspect the output\n  kind: human_check\n');
  const opened = await cli(['block', 'open', 'B172', '--run', runId, '--level', 'L2', '--owned', 'src/**', '--brief', path.join(repo, 'brief.md'), '--acceptance', path.join(repo, 'acceptance.yml')], repo);
  assert.equal(opened.code, 0, opened.stderr);
  const packed = await cli(['context', 'pack', '--run', runId, '--block', 'B172'], repo);
  assert.equal(packed.code, 0, packed.stderr);
  assert.match(packed.stdout, /# B172 task\nImplement the exact requested behavior\./);
  assert.match(packed.stdout, /the answer includes the brief/);
  assert.match(packed.stdout, /context-pack/);
  assert.match(packed.stdout, /the human can inspect the output/);
  assert.match(packed.stdout, /human_check/);
  const json = await cli(['context', 'pack', '--run', runId, '--block', 'B172', '--json'], repo);
  assert.equal(json.code, 0, json.stderr);
  assert.equal(packed.stdout, readFileSync(json.json.path, 'utf8'));
  const legacy = await cli(['context', 'pack', '--run', runId, '--block', 'B172', '--path-only'], repo);
  assert.equal(legacy.code, 0, legacy.stderr);
  assert.equal(legacy.stdout, `${json.json.path}\ntokens ~${json.json.tokens}\n`);
});

test('B172: a block without a brief still includes acceptance and no invented brief section', async () => {
  const { repo, runId } = await makeRepo();
  const packed = await cli(['context', 'pack', '--run', runId, '--block', 'B11'], repo);
  assert.equal(packed.code, 0, packed.stderr);
  assert.match(packed.stdout, /fixture files are reviewed/);
  assert.doesNotMatch(packed.stdout, /^## brief/m);
});

test('B172: JSON strings and object string fields decode TESTS and the last matching sentinel', () => {
  const text = 'TESTS old exit=1\n===BLOCK B172 FAILED: retry===\nTESTS npm test exit=0\n===BLOCK B172 COMPLETE===';
  for (const encoded of [JSON.stringify(text), JSON.stringify({ answer: text }), JSON.stringify({ result: text }), JSON.stringify({ message: text, count: 2 })]) {
    assert.deepEqual(parseSentinel(encoded, 'B172'), { status: 'complete', blockId: 'B172', tests: { command: 'npm test', exit: 0 } });
  }
});

test('B172: decoding preserves whole-line matching, block filtering and TESTS segment boundaries', () => {
  for (const wrap of [(text) => JSON.stringify(text), (text) => JSON.stringify({ answer: text })]) {
    assert.equal(parseSentinel(wrap('quoted ===BLOCK B172 COMPLETE===')), null);
    assert.equal(parseSentinel(wrap('===BLOCK B173 COMPLETE==='), 'B172'), null);
    assert.deepEqual(parseSentinel(wrap('TESTS stale exit=0\n===BLOCK B173 COMPLETE===\n===BLOCK B172 FAILED: no proof==='), 'B172'), { status: 'failed', blockId: 'B172', reason: 'no proof' });
    assert.deepEqual(parseSentinel(wrap('===BLOCK B172 COMPLETE===\nTESTS late exit=0')), { status: 'complete', blockId: 'B172' });
  }
  assert.equal(parseSentinel('{"answer":"broken\\n===BLOCK B172 COMPLETE==='), null);
  assert.equal(parseSentinel('{"count":42}'), null);
});
