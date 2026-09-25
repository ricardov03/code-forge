import assert from 'node:assert/strict';
import { test } from 'node:test';
import { estimateCostUsd, forecastTokensForDepth, REVIEW_DEPTH_TOKENS } from '../../src/ledger/prices.mjs';

test('forecastTokensForDepth matches §4.10s static table for all 4 depths', () => {
  assert.equal(forecastTokensForDepth('quick'), 7000);
  assert.equal(forecastTokensForDepth('full'), 13000);
  assert.equal(forecastTokensForDepth('a-b-judge'), 34000);
  assert.equal(forecastTokensForDepth('consensus'), 30000);
  assert.equal(Object.keys(REVIEW_DEPTH_TOKENS).length, 4);
});

test('forecastTokensForDepth throws on an unknown depth', () => {
  assert.throws(() => forecastTokensForDepth('bogus'), RangeError);
});

test('estimateCostUsd computes (tokensIn+tokensOut)/1000 * price for a round total', () => {
  assert.equal(estimateCostUsd({ provider: 'anthropic', level: 'L2', tokensIn: 8000, tokensOut: 2000 }), 0.15);
});

test('estimateCostUsd actually rounds to 4dp — a raw result with more digits is truncated to a different, exact value', () => {
  // (1 + 0) / 1000 * 0.015 = 0.000015 raw — rounds to 0.0000 at 4dp, and differs from the raw figure.
  const raw = (1 / 1000) * 0.015;
  const rounded = estimateCostUsd({ provider: 'anthropic', level: 'L2', tokensIn: 1, tokensOut: 0 });
  assert.notEqual(rounded, raw);
  assert.equal(rounded, 0);

  // A case whose raw value has real digits past 4dp: 12345 tokens at openai L1 (0.0025/1k).
  const raw2 = (12345 / 1000) * 0.0025; // 0.0308625
  const rounded2 = estimateCostUsd({ provider: 'openai', level: 'L1', tokensIn: 12345, tokensOut: 0 });
  assert.notEqual(rounded2, raw2);
  assert.equal(rounded2, 0.0309); // Math.round(308.625) / 10000
});

test('estimateCostUsd sums tokensIn and tokensOut at ONE blended rate — a different split with the same total gives the same cost', () => {
  const a = estimateCostUsd({ provider: 'anthropic', level: 'L2', tokensIn: 8000, tokensOut: 2000 });
  const b = estimateCostUsd({ provider: 'anthropic', level: 'L2', tokensIn: 2000, tokensOut: 8000 });
  assert.equal(a, b);
  assert.equal(a, 0.15);

  // A different, independently hand-computed provider/level pair.
  assert.equal(estimateCostUsd({ provider: 'xai', level: 'L0', tokensIn: 3000, tokensOut: 1000 }), 0.004); // 4000/1000 * 0.001
});

test('estimateCostUsd throws on an unknown provider or level', () => {
  assert.throws(() => estimateCostUsd({ provider: 'bogus', level: 'L1', tokensIn: 1, tokensOut: 1 }), RangeError);
  assert.throws(() => estimateCostUsd({ provider: 'anthropic', level: 'L9', tokensIn: 1, tokensOut: 1 }), RangeError);
});

test('estimateCostUsd throws on a string, NaN, or negative token count instead of silently corrupting the total', () => {
  // @ts-expect-error - deliberately passing a string where a JSON.parse'd ledger row could have one
  assert.throws(() => estimateCostUsd({ provider: 'anthropic', level: 'L2', tokensIn: '100', tokensOut: 50 }), RangeError);
  assert.throws(() => estimateCostUsd({ provider: 'anthropic', level: 'L2', tokensIn: NaN, tokensOut: 50 }), RangeError);
  assert.throws(() => estimateCostUsd({ provider: 'anthropic', level: 'L2', tokensIn: -1, tokensOut: 50 }), RangeError);
  assert.throws(() => estimateCostUsd({ provider: 'anthropic', level: 'L2', tokensIn: 50, tokensOut: -1 }), RangeError);
});
