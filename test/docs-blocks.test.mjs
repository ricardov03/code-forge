/**
 * B16 acceptance: "`blocks.json` lists every block id of §10.4 (count asserted)". `docs/reference/blocks.json`
 * is hand-maintained; this test is the check that keeps it from drifting.
 *
 * Fix round 1: the previous version hard-coded the expected §10.4 ids as a literal array, which
 * could go stale the moment the plan changed underneath it. This version instead PARSES
 * `plans/code-forge-plan-v1.3.md` §10.4 (Waves 2-6) for every `| **Bxx** |` table row and treats
 * that as the source of truth; only the small set of blocks the root orchestrator added during the night
 * run — which by definition are NOT in that table — comes from a hand-kept constant, named as
 * sourced from `plans/NIGHT-LOG.md`.
 */

import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const BLOCKS_JSON_PATH = path.join(REPO_ROOT, 'docs', 'reference', 'blocks.json');
const PLAN_PATH = path.join(REPO_ROOT, 'plans', 'code-forge-plan-v1.3.md');

/**
 * Every block id in a `| **Bxx** | ... |` table row inside the plan's §10.4 section (Waves 2-6).
 * A block id looks like `B0.1`, `B10a` or `B13b` — digits, an optional trailing lowercase letter,
 * an optional `.digits` amendment suffix.
 * @param {string} planText
 * @returns {string[]} in table order, duplicates kept (there are none in a well-formed plan; a
 *   duplicate here is itself a signal worth seeing in a failed assertion, not worth hiding).
 */
export function blockIdsFromPlanSection10_4(planText) {
  const start = planText.indexOf('### 10.4 Waves 2');
  const end = planText.indexOf('### 10.5 Publishing steps');
  assert.ok(start >= 0 && end > start, 'plan §10.4/§10.5 headings not found — plan structure changed');
  const section = planText.slice(start, end);
  const idPattern = /^\|\s*\*\*(B\d+[a-z]?(?:\.\d+)?)\*\*/gm;
  return [...section.matchAll(idPattern)].map((m) => m[1]);
}

/**
 * Blocks the root orchestrator added during the night run of 2026-09-25 that are NOT rows in plan §10.4
 * (source: `plans/NIGHT-LOG.md`, one entry per "root-added"/"Root-added" block mention):
 *   - B1.2  02:20 "COMMITTED `1749768` (schema cut for Q16)"
 *   - B11.1 03:01 "root-added B11.1 (symlink TOCTOU hardening per the architect's follow-up 1)"
 *   - B4.1  03:07 "Root-added security follow-up started"
 *   - B12c  03:31 "Root-added **B12c** started: wire worker -> triage -> fixloop"
 * (B3.1 is NOT here — it was already a §10.4 Wave 3 row.)
 */
const NIGHT_LOG_ADDED_IDS = ['B1.2', 'B4.1', 'B11.1', 'B12c'];

/**
 * Blocks the root added AFTER the night run, also not rows in plan §10.4 (source: the root's
 * ruling on B22's facts diff, 2026-09-26):
 *   - B22 the review-only verb (`code-forge review`)
 *   - B23 the release and changelog tools (2026-09-26)
 *   - B24 init project settings: detect, summarize blanks, one choice (2026-10-01)
 *   - B25 1Password item ID or link for the Jev key (2026-10-01)
 *   - B26 code-forge tools (2026-10-01)
 *   - B27 the local error log, `code-forge logs` and `logs report` (2026-10-01)
 *   - B28 better error reports: AI cleaning pass, fingerprints, hint, version check, edit (2026-10-01)
 *   - B29 effort per provider, B30 review session hangs, B31 ledger tail -n and test hardening (2026-10-02)
 *   - B32 codex closed-book refusal, B33 budget, B34 escalation rules, B35 run reload (2026-10-02)
 *   - B36 plan check lanes, headings and fail-closed reports; B37 report extras; B38 known fixes and triage (2026-10-02)
 */
const ROOT_ADDED_LATER_IDS = ['B22', 'B23', 'B24', 'B25', 'B26', 'B27', 'B28', 'B29', 'B30', 'B31', 'B32', 'B33', 'B34', 'B35', 'B36', 'B37', 'B38'];

// The plan is maintainer-only (not tracked since 2026-09-25): the drift check runs where it exists.
const HAS_PLAN = existsSync(PLAN_PATH);
const planText = HAS_PLAN ? readFileSync(PLAN_PATH, 'utf8') : '';
const blocksText = readFileSync(BLOCKS_JSON_PATH, 'utf8');
const doc = JSON.parse(blocksText);

test('blocks.json ids deep-equal §10.4\'s table rows plus the NIGHT-LOG additions — no drift either way', { skip: HAS_PLAN ? false : 'the maintainer plan is not in this checkout' }, () => {
  const fromPlan = blockIdsFromPlanSection10_4(planText);
  assert.equal(fromPlan.length, 19, `expected 19 §10.4 rows, parsed ${fromPlan.length}: ${fromPlan.join(', ')}`);
  const expected = [...fromPlan, ...NIGHT_LOG_ADDED_IDS, ...ROOT_ADDED_LATER_IDS].sort();
  const actual = Object.keys(doc.blocks).sort();
  assert.deepEqual(actual, expected);
  assert.equal(actual.length, 40);
  assert.equal(doc.block_count, 40);
});

test('B10c is marked deleted (Q16 = cut) — negative: no other block is', () => {
  assert.equal(doc.blocks.B10c.deleted, true);
  const deletedElsewhere = Object.entries(doc.blocks).filter(([id, b]) => id !== 'B10c' && b.deleted === true);
  assert.deepEqual(deletedElsewhere, []);
});

test('every block entry names at least one owned path and its depends_on array', () => {
  for (const [id, block] of Object.entries(doc.blocks)) {
    assert.ok(Array.isArray(block.owned) && block.owned.length > 0, `${id}: owned`);
    assert.ok(Array.isArray(block.depends_on), `${id}: depends_on`);
  }
});

test('every depends_on id resolves to a blocks key or a baseline_ids entry (B0-B8, Wave 0-1) — 0 unresolved', () => {
  assert.deepEqual(doc.baseline_ids, ['B0', 'B1', 'B2', 'B3', 'B4', 'B5', 'B6', 'B7', 'B8']);
  const known = new Set([...Object.keys(doc.blocks), ...doc.baseline_ids]);
  const allDeps = Object.values(doc.blocks).flatMap((b) => b.depends_on);
  assert.equal(allDeps.length, 83);
  const unresolved = Object.entries(doc.blocks).flatMap(([id, b]) =>
    b.depends_on.filter((dep) => !known.has(dep)).map((dep) => `${id} -> ${dep}`),
  );
  assert.deepEqual(unresolved, []);
  assert.equal(unresolved.length, 0);
});

test('blocks.json names no person and no private predecessor skill (0 occurrences of each of 4 strings)', () => {
  // Built from parts so the repo-wide name sweep does not match this test file itself.
  const skill = ['fa', 'ble-', 'forge'].join('');
  const needles = [
    ['Ric', 'ardo'].join(''),
    ['Fa', 'ble'].join(''),
    '/' + skill,
    ['~', '.claude', 'skills', skill, '**'].join('/'),
  ];
  assert.equal(needles.length, 4);
  const counts = needles.map((n) => blocksText.split(n).length - 1);
  assert.deepEqual(counts, [0, 0, 0, 0]);
  assert.equal((blocksText.match(new RegExp(['ric', 'ardo|fa', 'ble'].join(''), 'gi')) ?? []).length, 0);
});
