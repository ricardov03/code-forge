import { answerFor, cfgFor, commitAll, harness, lines, makeRepo, readRecords, stdinOf, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { reviewFile } = await import('../../src/review/engine.mjs');

async function packet(review = {}) {
  const repo = await makeRepo();
  const file = 'src/late.mjs';
  const header = ['// module header', ...Array.from({ length: 14 }, (_, i) => `import v${i} from 'package-${i}';`),
    'import {', '  realpathSync,', '  readFileSync,', "} from 'node:fs';", "const local = require('local');"];
  const content = lines(620, (n) => header[n - 1] ?? `export const value${n} = ${n};`);
  writeFile(repo, file, content);
  await commitAll(repo);
  writeFile(repo, file, content.replace('value600 = 600', 'value600 = 601'));
  const cfg = cfgFor(review);
  const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answerFor(['@@ -597,7 +597,7 @@'])) } });
  const result = await reviewFile({ repoRoot: repo, file, base: 'HEAD', cfg, risk: 0, workDir: h.workDir }, { spawn: h.spawn, writeRow: h.writeRow });
  assert.equal(result.status, 'reviewed');
  const records = readRecords(h.records);
  assert.equal(records.length, 1, 'one reviewer receives the packet');
  return { text: stdinOf(records[0]), header };
}

test('a reviewer of a change at line 600 receives every import header line as context', async () => {
  const { text, header } = await packet();
  const context = text.split('## context\n')[1].split('## diff\n')[0];
  assert.deepEqual(context.split('\n').filter((line) => /^(?:[1-9]|1\d|20)\| /.test(line)), header.map((line, i) => `${i + 1}| ${line}`));
  assert.equal(context.split('\n').filter((line) => line === '600| export const value600 = 601;').length, 1);
});

test('a minimal-budget packet retains the full header once, including multiline imports and require', async () => {
  const { text, header } = await packet({ budgets: { quick_in: 1100 } });
  const context = text.split('## context\n')[1].split('## diff\n')[0];
  for (const [i, line] of header.entries()) assert.equal(context.split('\n').filter((value) => value === `${i + 1}| ${line}`).length, 1, `header line ${i + 1} appears once`);
  assert.equal(context.includes('560| '), false, 'the ordinary 40-line window was shrunk');
});
