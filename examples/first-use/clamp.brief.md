# Block B1 — add `clamp()` with tests

Add `clamp(value, min, max)` to ./src/math.mjs. It returns `value` limited to the closed range
`[min, max]` and throws a `RangeError` when `min > max`. Nothing else in the module changes.

Tests go in a new file, ./test/clamp.test.mjs, run by the example's `npm test` (plain `node:test`,
the `--test` runner). One test per acceptance clause, named exactly as the acceptance file names it:
inside the range ⇒ the value, below min ⇒ min, above max ⇒ max, min above max ⇒ `RangeError`.

So that the red step against the base version (no `clamp` export) is always an assertion failure,
never an import error or a `TypeError`: import the module as a namespace
(`import * as ns from '../src/math.mjs'`), start EVERY test with
`assert.strictEqual(typeof ns.clamp, 'function')`, and check the throw with
`assert.throws(() => ns.clamp(1, 10, 0), RangeError)`.

Owned files: `src/math.mjs`, `test/clamp.test.mjs`. Level L1.
