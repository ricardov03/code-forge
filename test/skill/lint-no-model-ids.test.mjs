/**
 * `lint-no-model-ids` (plan §1.4, C1; block B14): no model id or alias anywhere under `skill/`.
 *
 * The pattern set is NOT written here — it is `LINT_PATTERNS` from `src/config/known-ids.mjs`,
 * built from the catalog (every id escaped literally), the vendor prefixes, and the aliases in
 * value position only. This file adds: the empty-set guard (a dead lint cannot pass), the sweep
 * over every file under `skill/` read whole (Prettier-safe), the positive controls (prose that
 * must yield 0 hits) and the two negative controls (an injected id / an injected `--model <alias>`
 * must be caught).
 */
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { ALL_KNOWN_IDS, LINT_PATTERNS } from '../../src/config/known-ids.mjs';
import { SKILL_DIR, listSkillFiles } from './helpers.mjs';

/** The catalog is 11 ids today (4 + 3 + 4); the plan's floor is "≥ 11". */
const MIN_CATALOG_IDS = 11;
/** Catalog patterns + 4 vendor prefixes + 1 value-position alias pattern. */
const MIN_PATTERNS = MIN_CATALOG_IDS + 5;

/**
 * @param {string} text
 * @param {ReadonlyArray<RegExp>} patterns
 * @returns {string[]} the source text of every pattern that matched, in pattern order.
 * @throws {Error} `pattern set is empty` — the guard the plan names: a lint whose catalog source
 *   was emptied must fail the test itself, never pass with 0 hits.
 */
export function countHits(text, patterns) {
  if (!Array.isArray(patterns) || patterns.length === 0) {
    throw new Error('pattern set is empty');
  }
  return patterns.filter((re) => re.test(text)).map((re) => re.source);
}

test('the pattern set is derived from the catalog and is not empty (guard: an emptied catalog fails here)', () => {
  assert.ok(ALL_KNOWN_IDS.length >= MIN_CATALOG_IDS, `expected >= ${MIN_CATALOG_IDS} catalog ids, got ${ALL_KNOWN_IDS.length}`);
  assert.ok(LINT_PATTERNS.length >= MIN_PATTERNS, `expected >= ${MIN_PATTERNS} lint patterns, got ${LINT_PATTERNS.length}`);
  assert.throws(() => countHits('anything', []), /pattern set is empty/);
});

test('0 hits on every file under skill/** (each file read whole)', async () => {
  const files = await listSkillFiles();
  assert.ok(files.length >= 19, `expected the 19 owned skill files to be scanned, got ${files.length}`);
  const hits = [];
  for (const file of files) {
    const text = await readFile(file, 'utf8');
    for (const source of countHits(text, LINT_PATTERNS)) {
      hits.push(`${path.relative(SKILL_DIR, file)}: /${source}/`);
    }
  }
  assert.deepEqual(hits, []);
});

test('positive controls: role and tool words the old bare-token lint used to flag yield 0 hits', () => {
  const prose =
    'Solo holds the scratchpad; `forge resolve L2` prints the level; the console shows isolation ' +
    'flags; this replaces fable-forge; Jev is System 1 and answers in half a second.';
  assert.deepEqual(countHits(prose, LINT_PATTERNS), []);
});

/**
 * The pattern sources an injection adds on top of the clean text's (which must be none).
 * @param {string} clean @param {string} injected
 * @returns {string[]}
 */
function addedHits(clean, injected) {
  const before = countHits(clean, LINT_PATTERNS);
  assert.deepEqual(before, [], 'the clean file must be clean first');
  return countHits(injected, LINT_PATTERNS).filter((s) => !before.includes(s));
}

test('negative control 1: references/code.md with one catalog id injected adds exactly that id\'s literal pattern', async () => {
  const clean = await readFile(path.join(SKILL_DIR, 'references', 'code.md'), 'utf8');
  const injected = `${clean}\nThe coder runs on ${ALL_KNOWN_IDS[0]} today.\n`;
  // LINT_PATTERNS[i] is the literal pattern for ALL_KNOWN_IDS[i] (index-aligned by contract); a
  // real id also carries its vendor prefix, so exactly ONE of the 4 vendor-prefix patterns (the
  // slots right after the catalog) fires with it — the two together are the exact expectation.
  const vendorHits = LINT_PATTERNS.slice(ALL_KNOWN_IDS.length, ALL_KNOWN_IDS.length + 4)
    .filter((re) => re.test(ALL_KNOWN_IDS[0]))
    .map((re) => re.source);
  assert.equal(vendorHits.length, 1, 'a catalog id matches exactly one vendor prefix');
  assert.deepEqual(addedHits(clean, injected), [LINT_PATTERNS[0].source, ...vendorHits]);
});

test('negative control 2: references/code.md with `--model opus` injected adds exactly the value-position alias pattern', async () => {
  const clean = await readFile(path.join(SKILL_DIR, 'references', 'code.md'), 'utf8');
  const injected = `${clean}\nSpawn it with --model opus for this block.\n`;
  const aliasPattern = LINT_PATTERNS[LINT_PATTERNS.length - 1];
  assert.match(aliasPattern.source, /--model/, 'the last pattern is the value-position alias pattern');
  assert.deepEqual(addedHits(clean, injected), [aliasPattern.source]);
});
