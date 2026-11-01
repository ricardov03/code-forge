import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';
import { commitAll, makeRepo, writeFile } from './helpers.mjs';
import { assembleJudgePacket, assembleLightPacket, assemblePacket, estimateTokens, readFileDiff } from '../../src/review/packet.mjs';

const heading = '## definitions (called by changed lines)';
const section = (text) => text.split(`${heading}\n`)[1]?.split('\n## ')[0].trimEnd() ?? '';
const entries = (text) => section(text).split('\n').filter((line) => line.startsWith('### '));
/** @param {Parameters<typeof assemblePacket>[0]} opts */
function packetFor(opts) {
  const packet = assemblePacket(opts);
  assert.equal(packet.status, 'ok');
  if (packet.status !== 'ok') throw new Error('expected reviewable fixture');
  return packet;
}

async function fixture() {
  const repoRoot = await makeRepo();
  const content = [
    "import { helper, longHelper } from './util.mjs';",
    "import { hidden } from './.env.mjs';",
    "import { outside } from '../outside.mjs';",
    "import { absent } from './missing.mjs';",
    'function local(x) {', '  return x + 10;', '}',
    ...Array.from({ length: 490 }, (_, i) => `// padding ${i}`),
    'function visible(x) { return x; }',
    'const result = 0;',
  ].join('\n') + '\n';
  writeFile(repoRoot, 'main.mjs', content);
  await commitAll(repoRoot);
  const changed = content.replace('const result = 0;', 'const result = helper(local(visible(longHelper(1)))) + hidden() + outside() + absent();');
  writeFile(repoRoot, 'main.mjs', changed);
  const files = new Map([
    ['main.mjs', changed],
    ['util.mjs', ['export function helper(x) {', '  const label = "}"; // unmatched brace in string', '  return x + label.length;', '}',
      'export async function longHelper(x) {', ...Array.from({ length: 198 }, (_, i) => `  x += ${i};`), '}'].join('\n') + '\n'],
    ['.env.mjs', 'export function hidden() { return "SECRET_BODY"; }\n'],
    ['../outside.mjs', 'export function outside() { return "OUTSIDE_BODY"; }\n'],
  ]);
  const reads = [];
  const io = {
    listFiles: () => [...files.keys()],
    readFile: (full) => { const rel = path.relative(repoRoot, full); reads.push(rel); return files.get(rel) ?? null; },
  };
  const diff = await readFileDiff({ repoRoot, file: 'main.mjs', io });
  return { diff, reads };
}

test('the review packet includes called imported and unseen local definitions, with exact ranges and bounded bodies', async () => {
  const { diff, reads } = await fixture();
  const packet = packetFor({ diff, lens: 'full' });
  assert.deepEqual(entries(packet.text), ['### util.mjs:1-4', '### main.mjs:5-7', '### util.mjs:5-44'], 'exactly three definitions in call order; visible is already shown');
  assert.equal(section(packet.text).split('\n').filter((line) => line === '3|   return x + label.length;').length, 1, 'helper body is attached once');
  assert.equal(section(packet.text).split('\n').filter((line) => line === '6|   return x + 10;').length, 1, 'unseen local body is attached once');
  const long = section(packet.text).split('### util.mjs:5-44\n')[1];
  assert.equal(long.split('\n').filter((line) => /^\d+\| /.test(line)).length, 40, '200-line function is capped at exactly 40 numbered lines');
  assert.equal(long.split('\n').at(-1), '…', 'cut body is marked');
  assert.equal(section(packet.text).includes('visible'), false, 'zero repeated definitions from shown window');
  assert.equal(reads.includes('.env.mjs'), false, 'secret import is never read');
  assert.equal(reads.includes('../outside.mjs'), false, 'outside import is never read');
  assert.equal(packet.text.includes('SECRET_BODY'), false);
  assert.equal(packet.text.includes('OUTSIDE_BODY'), false);
  assert.equal(section(packetFor({ diff, lens: 'judge' }).text), '', 'judge lens gets zero definitions');
  assert.equal(section(assembleJudgePacket({ diff, reports: { A: {}, B: {} } }).text), '', 'judge packet gets zero definitions');
  assert.equal(section(assembleLightPacket({ diffs: [diff] }).text), '', 'light packet gets zero definitions');
});

test('definition section cap and reverse dropping preserve paired context and the entire diff within budget', async () => {
  const { diff } = await fixture();
  const paired = 'test/main.test.mjs\n- checks helper';
  const roomy = packetFor({ diff, lens: 'full', paired });
  assert.equal(entries(roomy.text).length, 3, 'large budget keeps all three definitions');
  const noRelated = { ...diff, related: [] };
  const baseline = packetFor({ diff: { ...noRelated, definitions: [] }, lens: 'full', paired });
  const one = packetFor({ diff: { ...noRelated, definitions: diff.definitions?.slice(0, 1) }, lens: 'full', paired });
  const partial = packetFor({ diff, lens: 'full', paired, budget: one.tokensIn });
  assert.deepEqual(entries(partial.text), ['### util.mjs:1-4'], 'related names then last definitions are removed first');
  assert.equal(partial.text.includes('## related files (not shown)'), false);
  const tight = packetFor({ diff, lens: 'full', paired, budget: baseline.tokensIn });
  assert.equal(entries(tight.text).length, 0, 'small budget drops all definitions');
  assert.equal(tight.text, baseline.text, 'paired, ordinary context, and whole diff survive dropping definitions');
  assert.equal(tight.tokensIn <= tight.budget, true);
  assert.equal(tight.text.endsWith(diff.diffText), true, 'diff stays byte-for-byte whole');
  const fullIn = (estimateTokens(`${heading}\n${section(roomy.text)}\n`) - 1) * 8;
  const capped = packetFor({ diff, lens: 'full', paired, cfg: { review: { budgets: { full_in: fullIn } } } });
  assert.equal(capped.overBudget, false);
  assert.deepEqual(entries(capped.text), ['### util.mjs:1-4', '### main.mjs:5-7'], 'section cap excludes the long last definition even though the packet has room');
  assert.equal(estimateTokens(`${heading}\n${section(capped.text)}\n`) <= fullIn / 8, true, 'entire section, including headings and numbering, fits full_in / 8');
});
