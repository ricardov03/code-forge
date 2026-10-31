import { cfgFor, commitAll, freshDir, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, symlinkSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { saveState } = await import('../../src/worker/review-state.mjs');
const { newFileState } = await import('../../src/review/fixloop.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');

let seq = 0;
async function fixture(verb) {
  const slug = `judge-evidence-${++seq}`;
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'other/file.mjs', lines(200, (n) => `// evidence ${n}`));
  const outside = freshDir('outside');
  writeFile(outside, 'escape.mjs', '// OUTSIDE SECRET\n');
  symlinkSync(path.join(outside, 'escape.mjs'), path.join(repo, 'link.mjs'));
  symlinkSync(outside, path.join(repo, 'outside'));
  await commitAll(repo);
  const writeRow = (row) => appendRow(row, { slug });
  const probe = async () => 'start-A';
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe, config: cfgFor() });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'works', tests: ['t'] }], writeRow });
  const key = await loadKey(runId);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', file: 'src/a.mjs', ...row });
  await sign({ event: 'review.result', open: [{ id: 'W1', severity: 'warning', claim: 'bounded', evidence: 'src/a.mjs:1' }] });
  const state = { ...newFileState({ file: 'src/a.mjs', level: 'L2' }), status: 'stopped', stop_content_hash: contentHash(repo, 'src/a.mjs'), next: { action: 'stop', reason: 'review_stall' } };
  await saveState({ runId, block: 'B1', file: 'src/a.mjs', key, state: /** @type {any} */ (state), seq: 0, writeRow: sign, runRootDir: freshDir('runroot'), rows: [] });
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  const packets = [];
  const invoke = async (reason) => {
    let text = '';
    const stream = { write(s) { text += s; } };
    const code = await runBlock([verb, 'B1', ...(verb === 'waive' ? ['W1'] : []), '--run', runId, '--file', 'src/a.mjs', '--reason', reason, '--yes'], {
      stdout: stream, stderr: stream, probe, isTTY: false,
      waiverJudge: async (opts) => {
        packets.push(JSON.parse(readFileSync(opts.promptPath, 'utf8')));
        return { status: 'ok', answer: { verdict: 'agree', reason: 'The evidence supports the reason.' } };
      },
    });
    assert.equal(code, 0, text);
    assert.equal(packets.length, 1, 'one fresh judge receives the packet');
    return packets[0];
  };
  return { repo, outside, invoke };
}

const window = (start, end) => ({ path: 'other/file.mjs', start, end, content: Array.from({ length: end - start + 1 }, (_, i) => `${start + i}| // evidence ${start + i}`).join('\n') });

test('waiver and reopen judges receive numbered windows for each single-line and range citation; a range alone is evidence', async () => {
  for (const verb of ['waive', 'reopen']) {
    for (const cites of ['other/file.mjs:15 and other/file.mjs:10-12', 'other/file.mjs:10-12']) {
      const f = await fixture(verb);
      const packet = await f.invoke(cites);
      assert.deepEqual(packet.evidence, cites.includes(':15') ? [window(5, 25), window(1, 22)] : [window(1, 22)], `${verb}: every citation has its exact window`);
    }
  }
});

test('judge evidence excludes missing and escaping paths and caps each of at most ten citations at eighty lines', async () => {
  const f = await fixture('waive');
  const invalid = `missing.mjs:1 ../escape.mjs:1 ${f.outside}/escape.mjs:1 link.mjs:1 outside/escape.mjs:1 other/file.mjs:20-10`;
  const cites = Array.from({ length: 12 }, (_, i) => `other/file.mjs:${i + 1}-200`).join(' ');
  const packet = await f.invoke(`${invalid} ${cites}`);
  assert.deepEqual(packet.evidence, Array.from({ length: 10 }, () => window(1, 80)), 'only the first ten valid citations are attached, with exactly eighty numbered lines each');
  assert.equal(JSON.stringify(packet.evidence).includes('OUTSIDE SECRET'), false);
});
