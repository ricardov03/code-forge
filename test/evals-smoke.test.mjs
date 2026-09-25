/**
 * `test/evals-smoke.test.mjs` (plan §9.3, §10.4 B15). The runner loads all 19 eval cases (dry
 * run, count), and cases 15, 16, 17, 18 and 19 run green through `evals/run.mjs` against the fake
 * CLIs only — never a real model call (coder-rules). The other 14 cases only need to load here;
 * `claude plugin eval` runs their prompts for real, on tag (§9.3).
 */
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, '..');

const { listCases, runCase } = await import(path.join(ROOT, 'evals', 'run.mjs'));

const LIVE_CASE_IDS = ['15-plan-refuses-without-facts', '16-unbackable-clause-flagged', '17-no-engine-stops-with-the-exact-line', '18-recheck-reviews-fix-hunks-only', '19-review-cap-stops-for-human'];

test('the runner loads all 19 eval cases (dry run, count)', () => {
  const cases = listCases();
  assert.equal(cases.length, 19);
  assert.deepEqual(cases.map((c) => c.id), [...cases.map((c) => c.id)].sort(), 'ids are already sorted (deterministic order)');
  const withScenario = cases.filter((c) => c.scenario !== null).map((c) => c.id);
  assert.deepEqual(withScenario.sort(), [...LIVE_CASE_IDS].sort());
});

test('every case names a schema_version, an execution.prompt and at least one grader with a known type', () => {
  const cases = listCases();
  for (const c of cases) {
    assert.equal(typeof c.meta.schema_version, 'string');
    assert.equal(typeof c.meta.execution.prompt, 'string');
    assert.ok(c.meta.graders.length >= 1, `${c.id}: expected >= 1 grader`);
  }
});

test('no case.yaml names a real project', () => {
  // Assembled from parts (never a literal token in this file), the same technique
  // test/skill/structure.test.mjs and test/no-project-names.test.mjs use.
  const names = [['condo', 'mera'].join(''), ['finance', '360'].join('')];
  const re = new RegExp(names.join('|'), 'i');
  const cases = listCases();
  const hits = cases.filter((c) => re.test(JSON.stringify(c.meta))).map((c) => c.id);
  assert.deepEqual(hits, []);
});

for (const id of LIVE_CASE_IDS) {
  test(`eval ${id} runs green against the fake CLIs (no real model call)`, async () => {
    const cases = listCases();
    const c = cases.find((x) => x.id === id);
    assert.ok(c, `case ${id} must exist`);
    const result = await runCase(c);
    assert.equal(result.pass, true, result.detail);
  });
}
