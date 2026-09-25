import { lines } from './helpers.mjs';
import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

const { parseDiff } = await import('../../src/review/context.mjs');
const { assemblePacket } = await import('../../src/review/packet.mjs');

/**
 * A FileDiff for `file` whose current content is `content` and whose diff changes the given
 * 1-based lines (one `-`/`+` pair each).
 * @param {string} file @param {string} content @param {number[]} changed
 */
function fileDiff(file, content, changed) {
  const body = changed.map((n) => `@@ -${n} +${n} @@\n-old ${n}\n+export const v${n} = ${n};`).join('\n');
  const diffText = `diff --git a/${file} b/${file}\n--- a/${file}\n+++ b/${file}\n${body}\n`;
  return { file, kind: /** @type {'tracked'} */ ('tracked'), diffText, content, ...parseDiff(diffText) };
}

/** @param {string} text @param {string} from @param {string} to @returns {string[]} the lines strictly between two headers. */
function section(text, from, to) {
  const all = text.split('\n');
  return all.slice(all.indexOf(from) + 1, all.indexOf(to));
}

describe('packet context (C10)', () => {
  test('a 380-line file is carried whole: the context section is the header + 380 numbered lines', () => {
    const packet = /** @type {any} */ (assemblePacket({ diff: fileDiff('src/w.mjs', lines(380), [10]), lens: 'full', cfg: {} }));
    assert.equal(packet.status, 'ok');
    assert.equal(packet.contextMode, 'whole');
    assert.equal(packet.contextLines, 380);
    const ctx = section(packet.text, '## context', '## diff');
    assert.equal(ctx.length, 1 + 380);
    assert.equal(ctx[0], '### src/w.mjs (whole file, 380 lines)');
    assert.equal(ctx[380], '380| export const v380 = 380;');
    // after the context: the diff section with its one hunk (whole file = 380 lines + the diff)
    assert.deepEqual(section(packet.text, '## diff', 'hunks:'), ['file: src/w.mjs']);
    assert.deepEqual(packet.hunkHeaders, ['@@ -10 +10 @@']);
  });

  test('a 900-line file with two hunks gives exactly two windows of 81 lines (± 40)', () => {
    const packet = /** @type {any} */ (assemblePacket({ diff: fileDiff('src/big.mjs', lines(900), [200, 700]), lens: 'full', cfg: {} }));
    assert.equal(packet.contextMode, 'hunks');
    const ctx = section(packet.text, '## context', '## diff');
    const heads = ctx.filter((l) => l.startsWith('### '));
    assert.deepEqual(heads, ['### src/big.mjs lines 160-240', '### src/big.mjs lines 660-740']);
    assert.equal(ctx.length, 2 + 81 + 81);
    assert.equal(packet.contextLines, 162);
  });

  test('over full_in: the context drops to minimal (± 10) while the 60-line digest stays whole', () => {
    const digest = lines(60, (i) => `rule ${i}: keep it simple`);
    const diff = fileDiff('src/w.mjs', lines(380), [100]);
    const roomy = /** @type {any} */ (assemblePacket({ diff, lens: 'full', rulesDigest: digest, cfg: {} }));
    assert.equal(roomy.contextMode, 'whole');
    // a budget the whole file cannot fit into, but the minimal window plus the full digest can
    const budget = roomy.tokensIn - 1000;
    const tight = /** @type {any} */ (assemblePacket({ diff, lens: 'full', rulesDigest: digest, cfg: { review: { budgets: { full_in: budget } } } }));
    assert.equal(tight.status, 'ok');
    assert.equal(tight.contextMode, 'minimal');
    assert.equal(tight.contextLines, 21);
    assert.equal(tight.digestLines, 60);
    assert.equal(tight.overBudget, false);
    assert.deepEqual(section(tight.text, '## context', '## diff')[0], '### src/w.mjs lines 90-110');
  });
});
