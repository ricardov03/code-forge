/**
 * B56 fix round 1: `blockPeerDiffs` turns a throwing or rejecting peer reader into no peer diffs
 * (no hint) and memoises that. `peerDiffReader` is swapped through `mock.module`, which needs
 * `node --experimental-test-module-mocks` (as `npm test` runs). No git, no process.
 */
import assert from 'node:assert/strict';
import { mock, test } from 'node:test';

if (typeof mock.module !== 'function') throw new Error('test/worker/moved-peers-guard.test.mjs requires `node --experimental-test-module-mocks --test …` (run it via `npm test`).');

const real = await import('../../src/review/moved.mjs');

/** What the mocked `peerDiffReader` does: throw while building, or build a reader that rejects. */
let mode = /** @type {'throw' | 'reject'} */ ('throw');
let built = 0;
let read = 0;
mock.module(new URL('../../src/review/moved.mjs', import.meta.url).href, {
  namedExports: {
    ...real,
    peerDiffReader: () => {
      built += 1;
      if (mode === 'throw') throw new Error('reader could not be built');
      return () => {
        read += 1;
        return Promise.reject(new Error('peer diffs unreadable'));
      };
    },
  },
});
const { blockPeerDiffs } = await import(`../../src/worker/engine.mjs?moved-guard-${Date.now()}`);

for (const which of /** @type {const} */ (['throw', 'reject'])) {
  test(`a peer reader that ${which}s gives no peer diffs, memoised: asked twice, built once`, async () => {
    mode = which;
    built = 0;
    read = 0;
    // base null ⇒ no git call: the (mocked) reader is still built for the empty peer list
    const reader = blockPeerDiffs('/nonexistent-repo', { base: null, owned: ['src/a.mjs'], file: 'src/a.mjs' });
    const first = reader();
    assert.equal(reader(), first);
    assert.deepEqual(await first, []);
    assert.deepEqual(await reader(), []);
    assert.deepEqual([built, read], which === 'throw' ? [1, 0] : [1, 1]);
  });
}
