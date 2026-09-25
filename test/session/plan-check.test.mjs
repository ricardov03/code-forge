import { freshDir, sink } from './helpers.mjs';
import assert from 'node:assert/strict';
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { runPlanVerb } = await import('../../src/cli/plan.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const GOOD = path.join(REPO, 'test', 'fixtures', 'plans', 'good.plan.md');
const GOOD_TEXT = readFileSync(GOOD, 'utf8');

/**
 * Write `good.plan.md` with `from` replaced by `to` (exactly one occurrence, asserted) next to a
 * copy of the brief its facts sheet names, and run `plan check` on it.
 * @param {string} from @param {string} to
 */
async function checkVariant(from, to) {
  assert.equal(GOOD_TEXT.split(from).length, 2, `fixture text must occur once: ${from}`);
  const dir = freshDir('variant');
  mkdirSync(path.join(dir, 'plans'));
  mkdirSync(path.join(dir, 'briefs'));
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.md'), path.join(dir, 'briefs', 'tool-brief.md'));
  writeFileSync(path.join(dir, 'plans', 'x.plan.md'), GOOD_TEXT.replace(from, to));
  const stdout = sink();
  const stderr = sink();
  const code = await runPlanVerb(['check', 'plans/x.plan.md'], { stdout, stderr, cwd: dir });
  return { code, errors: stderr.text().split('\n').filter(Boolean), out: stdout.text() };
}

const TOLERANCE_ROW = '1. `--max-turns` is NOT-FOUND — B2 cites it only to prove the reviewer never passes it; tolerance: B2 asserts against the help fixture, never a live CLI.';

test('plan check: the fixture plan with the tolerance row exits 0', async () => {
  const stdout = sink();
  const stderr = sink();
  assert.equal(await runPlanVerb(['check', GOOD], { stdout, stderr, cwd: freshDir('good') }), 0);
  assert.deepEqual([stdout.text(), stderr.text()], ['plan check: ok (3 blocks)\n', '']);
});

test('plan check: an unbackable clause without a tolerance exits 1 naming the block and the clause', async () => {
  const r = await checkVariant(TOLERANCE_ROW, 'none');
  assert.deepEqual([r.code, r.errors], [1, ['unbackable clause without tolerance: B2 the reviewer argv never carries `--max-turns` (1)']]);
});

test('plan check: a missing caller map exits 1', async () => {
  const r = await checkVariant('## Caller map', '## Notes');
  assert.deepEqual([r.code, r.errors], [1, ['caller map missing']]);
});

test('plan check: a block importing a batch-mate exits 1', async () => {
  const r = await checkVariant('`src/util/log.mjs`', '`src/config/load.mjs`');
  assert.deepEqual([r.code, r.errors], [1, ['block B2 imports src/config/load.mjs owned by B1, which is not in its depends_on']]);
});

test('plan check: a @types/** glob exits 1 naming the block', async () => {
  const r = await checkVariant('`types/node-extra.d.ts`', '`@types/**`');
  assert.deepEqual(
    [r.code, r.errors],
    [1, ['block B3: owned files: invalid path "@types/**": a glob may not contain [ ] ( ) ! + @ — character classes and extglobs are not supported; name the files exactly or use only *, ?, ** and {a,b}']],
  );
});

test('plan check: a dispatch step with 3 concurrent blocks exits 1', async () => {
  const r = await checkVariant('1. B1 ∥ 2. B2 → 3. B3 (when B1 lands).', '1. B1 ∥ 2. B2 ∥ 3. B3.');
  assert.deepEqual([r.code, r.errors], [1, ['dispatch step 1 (wave 1) runs 3 blocks concurrently (caps.coders 2): B1 ∥ B2 ∥ B3']]);
});

test('plan check: a block without a cases forecast exits 1', async () => {
  const r = await checkVariant('3 → 6 → **1 200**', '3 → — → **1 200**');
  assert.deepEqual([r.code, r.errors], [1, ['block B2: no cases forecast']]);
});
