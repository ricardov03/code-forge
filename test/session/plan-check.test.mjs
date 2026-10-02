import { freshDir, sink } from './helpers.mjs';
import assert from 'node:assert/strict';
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { runPlanVerb } = await import('../../src/cli/plan.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { checkPlan } = await import('../../src/session/plan-check.mjs');

/**
 * B36: the lanes `jev ask lane --block <id>` records, in the ledger `plan check --slug lanes` reads
 * (HOME is the per-file temp dir).
 * @param {string} slug @param {Array<Record<string, any>>} rows
 */
async function seedLanes(slug, rows) {
  for (const row of rows) await appendRow({ event: 'decision', question: 'lane', ...row }, { slug });
}
await seedLanes('lanes', [
  { block: 'B1', answer: 'L1', source: 'jev', decision_id: 'd-1' },
  { block: 'B2', answer: 'L2', source: 'rules', decision_id: 'd-2' },
  { block: 'B3', answer: 'L1', source: 'jev', decision_id: 'd-3' },
]);
const LANES_LINE = 'lanes: B1 L1 (jev) · B2 L2 (rules) · B3 L1 (jev)\n';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const GOOD = path.join(REPO, 'test', 'fixtures', 'plans', 'good.plan.md');
const GOOD_TEXT = readFileSync(GOOD, 'utf8');

/**
 * Write `good.plan.md` with `from` replaced by `to` (exactly one occurrence, asserted) next to a
 * copy of the brief its facts sheet names, and run `plan check` on it.
 * @param {string} from @param {string} to
 */
async function checkVariant(from, to, extra = ['--slug', 'lanes']) {
  assert.equal(GOOD_TEXT.split(from).length, 2, `fixture text must occur once: ${from}`);
  const dir = freshDir('variant');
  mkdirSync(path.join(dir, 'plans'));
  mkdirSync(path.join(dir, 'briefs'));
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.md'), path.join(dir, 'briefs', 'tool-brief.md'));
  writeFileSync(path.join(dir, 'plans', 'x.plan.md'), GOOD_TEXT.replace(from, to));
  const stdout = sink();
  const stderr = sink();
  const code = await runPlanVerb(['check', 'plans/x.plan.md', ...extra], { stdout, stderr, cwd: dir });
  return { code, errors: stderr.text().split('\n').filter(Boolean), out: stdout.text() };
}

const TOLERANCE_ROW = '1. `--max-turns` is NOT-FOUND — B2 cites it only to prove the reviewer never passes it; tolerance: B2 asserts against the help fixture, never a live CLI.';

test('plan check: the fixture plan with the tolerance row exits 0', async () => {
  const stdout = sink();
  const stderr = sink();
  assert.equal(await runPlanVerb(['check', GOOD, '--slug', 'lanes'], { stdout, stderr, cwd: freshDir('good') }), 0);
  assert.deepEqual([stdout.text(), stderr.text()], [`plan check: ok (3 blocks)\n${LANES_LINE}`, '']);
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

test('B36 plan check: a level that differs from the recorded lane exits 1 naming the block and the lane', async () => {
  const r = await checkVariant('| **B3** | Status report | L1 |', '| **B3** | Status report | L2 |');
  assert.deepEqual([r.code, r.errors, r.out], [1, ['block B3: level L2 differs from the recorded lane L1 (jev)'], LANES_LINE]);
});

test('B36 plan check: levels with no recorded lane (an empty ledger) are refused block by block', async () => {
  const r = await checkVariant('B3 report → the human.', 'B3 report → the human.', ['--slug', 'no-lanes']);
  assert.equal(r.code, 1);
  assert.deepEqual(r.errors, [
    'block B1: level L1 has no recorded lane decision — run forge jev ask lane --block B1 --state <file> (or --rules when Jev is unavailable)',
    'block B2: level L2 has no recorded lane decision — run forge jev ask lane --block B2 --state <file> (or --rules when Jev is unavailable)',
    'block B3: level L1 has no recorded lane decision — run forge jev ask lane --block B3 --state <file> (or --rules when Jev is unavailable)',
  ]);
  assert.equal(r.out, 'lanes: B1 none · B2 none · B3 none\n');
});

test('B36 plan check: only Jev or rules lane rows for this plan (by base name) count, the latest wins, and the answer is normalised', async () => {
  await seedLanes('scoped', [
    { block: 'B1', answer: 'L2', source: 'jev' },
    { block: 'B1', answer: ' l1 (plain feature) ', source: 'jev', plan: 'elsewhere/x.plan.md' },
    { block: 'B2', answer: 'L2', source: 'jev', plan: 'other.plan.md' },
    { block: 'B2', answer: 'L2', source: 's2' },
    { block: 'B3', answer: 'L1', source: 'rules' },
  ]);
  const r = await checkVariant('B3 report → the human.', 'B3 report → the human.', ['--slug', 'scoped']);
  assert.deepEqual(
    [r.code, r.errors, r.out],
    [1, ['block B2: level L2 has no recorded lane decision — run forge jev ask lane --block B2 --state <file> (or --rules when Jev is unavailable)'], 'lanes: B1 L1 (jev) · B2 none · B3 L1 (rules)\n'],
  );
});

test('B36 plan check: a near-miss heading at §0.x is read as the unbackable section with one WARN', async () => {
  const r = await checkVariant('## 0.6 Acceptance clauses the facts sheet cannot back', '## 0.6 Unbackable-clause tolerances');
  assert.deepEqual(
    [r.code, r.errors, r.out],
    [0, ['WARN section "0.6 Unbackable-clause tolerances" at §0.x read as "Acceptance clauses the facts sheet cannot back" — use the exact heading "Acceptance clauses the facts sheet cannot back"'], `plan check: ok (3 blocks)\n${LANES_LINE}`],
  );
});

test('B36 plan check: a near-miss caller map heading at §3 passes with one WARN', async () => {
  const r = await checkVariant('## Caller map', '## §3 Callers');
  assert.deepEqual([r.code, r.errors], [0, ['WARN section "§3 Callers" at §3 read as "Caller map" — use the exact heading "Caller map"']]);
});

test('B36 plan check: the same near-miss heading away from its position is a missing section', async () => {
  const r = await checkVariant('## 0.6 Acceptance clauses the facts sheet cannot back', '## Unbackable-clause tolerances');
  assert.deepEqual([r.code, r.errors], [
    1,
    [
      'unbackable-clauses section missing (write "none" when there is none)',
      'unbackable clause without tolerance: B1 rows land under `~/.code-forge/ledger` (1)',
      'unbackable clause without tolerance: B2 the reviewer argv never carries `--max-turns` (1)',
    ],
  ]);
});

test('B36 plan check: no ledger at all is no rows — every block reads none, never a crash', async () => {
  const before = process.env.HOME;
  process.env.HOME = freshDir('empty-home');
  try {
    const r = await checkVariant('B3 report → the human.', 'B3 report → the human.');
    assert.deepEqual([r.code, r.errors.length, r.out], [1, 3, 'lanes: B1 none · B2 none · B3 none\n']);
  } finally {
    process.env.HOME = before;
  }
});

test('B36 plan check: a bad --slug is a usage error (exit 2)', async () => {
  const r = await checkVariant('B3 report → the human.', 'B3 report → the human.', ['--slug', '../x']);
  assert.deepEqual([r.code, r.errors], [2, ['plan: --slug must be lowercase letters, digits and hyphens']]);
});

test('B36 checkPlan with no plan path counts only untagged lane rows', () => {
  const rows = [
    { event: 'decision', question: 'lane', block: 'B1', answer: 'L1', source: 'jev', plan: 'good.plan.md' },
    { event: 'decision', question: 'lane', block: 'B2', answer: 'L2', source: 'rules' },
    { event: 'decision', question: 'lane', block: 'B3', answer: 'L1', source: 'jev' },
  ];
  const result = checkPlan(GOOD_TEXT, { decisions: rows });
  assert.deepEqual(result.lanes.map((l) => [l.block, l.lane]), [['B1', null], ['B2', 'L2'], ['B3', 'L1']]);
});
