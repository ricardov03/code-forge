import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildClaudeArgv } from '../../../src/engines/builders/claude.mjs';
import { buildGrokArgv } from '../../../src/engines/builders/grok.mjs';
import { buildCodexArgv } from '../../../src/engines/builders/codex.mjs';
import { FORBIDDEN, mergeForbidden, renderForClaude, renderForCodex, renderForGrok } from '../../../src/util/forbidden.mjs';

/** B4.2: every coder build renders the merged list (FORBIDDEN + the coder-only entries). */
const CODER_LIST = mergeForbidden();

/** B4.2 literal oracle (hand-written, not from the renderer): the 4 `block waive` spellings as Claude/Grok rules. */
const WAIVE_RULES = Object.freeze([
  'Bash(code-forge block waive:*)',
  'Bash(npx code-forge block waive:*)',
  'Bash(npx @ricardov/code-forge block waive:*)',
  'Bash(forge block waive:*)',
]);
import { idsMissingFromArgv, valuesAfterFlag, valuesAfterRepeatedFlag } from './argv-check.mjs';

/**
 * `test/util/forbidden.test.mjs` (B0/B8) is the whole-directory owner of proving `renderForClaude`'s
 * OWN output is correct. This file proves something different and specific to B4: that the
 * COMMAND BUILDERS correctly consume whatever a renderer hands them — including the path-scoped
 * `Read(…)`/`Edit(…)`/`Write(…)` rule shape. `test/engines/builders/argv-snapshots.test.mjs` now
 * covers the REAL landed ★ entries (B8 has landed since this file was first written); this file
 * keeps the SYNTHETIC-list test because it proves something the real-data test cannot: that the
 * builder does not hard-code today's specific rule strings and will keep working if the renderer's
 * shape changes again later (the `renderedForbidden` parameter seam, C17).
 *
 * Fix round 1 (isolated per-file review): `idsMissingFromArgv` now requires ALL of an entry's
 * rules to be present (was `.some`, a MAJOR finding — an entry missing SOME but not all of its
 * rendered rules used to be reported as "present"), and every count here is checked against the
 * argv VALUES actually attached to the relevant flag, never the renderer's own output length.
 */

/** The exact three path-scoped rule strings plan §8.4 named for the ★ entries, before B8 landed. */
const PATH_RULE_STRINGS = Object.freeze(['Read(~/.code-forge/runs/*)', 'Edit(.code-forge/reviews/*)', 'Write(.code-forge/reviews/*)']);

/** A synthetic rendered list simulating an alternate future rule shape — proves the seam, not today's data. */
const SYNTHETIC_STAR_RENDER = Object.freeze([
  { id: 'runs-dir-read', rules: [PATH_RULE_STRINGS[0]], enforced: true },
  { id: 'reviews-dir-edit', rules: [PATH_RULE_STRINGS[1]], enforced: true },
  { id: 'reviews-dir-write', rules: [PATH_RULE_STRINGS[2]], enforced: true },
]);

test('the Claude coder builder renders a synthetic ★-style rendered list verbatim (proves the renderedForbidden seam, not tied to any one rule shape)', () => {
  const built = buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/tmp/brief.md', cwd: '/work/project', renderedForbidden: SYNTHETIC_STAR_RENDER });
  const attached = valuesAfterFlag(built.argv, '--disallowedTools');
  assert.deepEqual([...attached].sort(), [...PATH_RULE_STRINGS].sort());
  assert.deepEqual(idsMissingFromArgv(SYNTHETIC_STAR_RENDER, attached), []);
});

test('the Claude facts builder renders the same synthetic list verbatim too, plus its own extra write-verb rules', () => {
  const built = buildClaudeArgv({ role: 'facts', model: 'claude-opus-5-5', promptPath: '/tmp/facts-packet.json', cwd: '/tmp/iso', renderedForbidden: SYNTHETIC_STAR_RENDER });
  const attached = valuesAfterFlag(built.argv, '--disallowedTools');
  // Fix round 3 (MINOR): exact list — 3 synthetic rules then the 6 facts write-verb extras, in
  // order, no duplicates (a hand-written literal, not FACTS_EXTRA_WRITE_VERB_RULES re-read).
  assert.deepEqual(attached, [
    ...PATH_RULE_STRINGS,
    'Bash(rm*)',
    'Bash(git push*)',
    'Bash(git commit*)',
    'Bash(git merge*)',
    'Bash(mv*)',
    'Bash(cp*)',
  ]);
  assert.equal(attached.length, 9);
});

// ── "each coder builder contains every forbidden entry (count = list length)", against the REAL landed FORBIDDEN ──

test('Claude coder: every real merged-list entry represented, exact argv-attached rule count (not just renderer length)', () => {
  const built = buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c' });
  const fresh = renderForClaude(CODER_LIST);
  assert.equal(fresh.length, FORBIDDEN.length + 2);
  const attached = valuesAfterFlag(built.argv, '--disallowedTools');
  assert.equal(attached.length, fresh.flatMap((e) => e.rules).length);
  assert.deepEqual(idsMissingFromArgv(fresh, attached), []);
  // Fix round 3 (MINOR): pin WHICH entries are unenforced to a literal — an entry that silently
  // flipped to enforced:false with no rules would drop out of both counts above.
  // B4.2: the coder-only `contains` entry is the second unenforced one (the transcript grep keeps it).
  assert.deepEqual(fresh.filter((e) => !e.enforced).map((e) => e.id), ['production-marker', 'code-forge-no-require-reviews']);
  assert.equal(fresh.filter((e) => e.enforced).length, CODER_LIST.length - 2);
  // B4.2 literal oracle: each `block waive` rule is attached exactly once; the id owns exactly these 4.
  for (const rule of WAIVE_RULES) assert.equal(attached.filter((r) => r === rule).length, 1, rule);
  assert.equal(attached.filter((r) => r.includes('block waive')).length, 4);
  assert.deepEqual(fresh.find((e) => e.id === 'code-forge-block-waive-from-coder')?.rules, [...WAIVE_RULES]);
});

test('Grok coder: every real merged-list entry represented, exact argv-attached rule count', () => {
  const built = buildGrokArgv({ role: 'coder', model: 'grok-4.7', promptPath: '/p', cwd: '/c' });
  const fresh = renderForGrok(CODER_LIST);
  assert.equal(fresh.length, FORBIDDEN.length + 2);
  const attached = valuesAfterRepeatedFlag(built.argv, '--deny');
  assert.equal(attached.length, fresh.flatMap((e) => e.rules).length);
  assert.deepEqual(idsMissingFromArgv(fresh, attached), []);
  // B4.2 literal oracle: the exact `--deny` values, each exactly once, each paired with its own flag.
  for (const rule of WAIVE_RULES) {
    assert.equal(attached.filter((r) => r === rule).length, 1, rule);
    assert.equal(built.argv[built.argv.indexOf(rule) - 1], '--deny', rule);
  }
  assert.equal(attached.filter((r) => r.includes('block waive')).length, 4);
  assert.deepEqual(fresh.find((e) => e.id === 'code-forge-block-waive-from-coder')?.rules, [...WAIVE_RULES]);
});

test('Codex coder: the rules file deep-equals the Starlark built from mergeForbidden() — id, exact pattern tokens, decision="forbidden" (FORBIDDEN\'s 15 ids + block waive)', () => {
  const built = buildCodexArgv({ role: 'coder', model: 'gpt-6-astra', promptPath: '/p', cwd: '/c' });
  const fresh = renderForCodex(CODER_LIST);
  assert.equal(fresh.length, FORBIDDEN.length + 2);
  // The oracle is written out here from the render's data, not taken from the module under test.
  const expected = [
    '# code-forge forbidden list (src/util/forbidden.mjs). Generated per session; do not edit.',
    ...fresh.flatMap((e) =>
      e.patterns.map((tokens) => `prefix_rule(pattern=[${tokens.map((t) => `"${t}"`).join(', ')}], decision="forbidden", justification="code-forge: ${e.id}")`),
    ),
    '',
  ];
  const content = /** @type {{rulesFile: {content: string}}} */ (built).rulesFile.content;
  assert.deepEqual(content.split('\n'), expected);
  const forbiddenOnly = renderForCodex(FORBIDDEN).flatMap((e) => e.patterns).length;
  assert.equal(expected.length - 2, forbiddenOnly + 4); // B4.2: + the 4 `block waive` spellings
  // spot-check two entries' exact tokens against FORBIDDEN itself
  assert.equal(content.includes('prefix_rule(pattern=["git", "reset", "--hard"], decision="forbidden", justification="code-forge: git-reset-hard")'), true);
  assert.equal(content.includes('prefix_rule(pattern=["gh", "pr", "merge"], decision="forbidden", justification="code-forge: gh-pr-merge")'), true);
  const ids = new Set(content.split('\n').map((l) => /justification="code-forge: ([^"]+)"/.exec(l)?.[1]).filter(Boolean));
  assert.equal(content.includes('prefix_rule(pattern=["npx", "@ricardov/code-forge", "block", "waive"], decision="forbidden", justification="code-forge: code-forge-block-waive-from-coder")'), true);
  // `contains` and `path` entries have 0 patterns (execpolicy cannot express them): 16 of 20 ids.
  assert.deepEqual([...ids].sort(), CODER_LIST.filter((e) => e.kind !== 'contains' && e.kind !== 'path').map((e) => e.id).sort());
  assert.equal(ids.size, 16);
});

// ── idsMissingFromArgv itself: proves the .every fix and the enforced-vs-empty-rules distinction ──

test('idsMissingFromArgv requires ALL of an entry\'s rules, not just one (the MAJOR fix)', () => {
  const rendered = [{ id: 'multi-rule', rules: ['Bash(a:*)', 'Bash(b:*)', 'Bash(c:*)'], enforced: true }];
  // Only 2 of 3 rules present -> still MISSING (the old `.some` implementation would say present).
  assert.deepEqual(idsMissingFromArgv(rendered, ['Bash(a:*)', 'Bash(b:*)']), ['multi-rule']);
  // All 3 present -> not missing.
  assert.deepEqual(idsMissingFromArgv(rendered, ['Bash(a:*)', 'Bash(b:*)', 'Bash(c:*)']), []);
});

test('idsMissingFromArgv: enforced:false with 0 rules is never missing; enforced:true with 0 rules IS missing (a renderer regression)', () => {
  assert.deepEqual(idsMissingFromArgv([{ id: 'production-marker', rules: [], enforced: false }], []), []);
  assert.deepEqual(idsMissingFromArgv([{ id: 'broken-renderer', rules: [], enforced: true }], []), ['broken-renderer']);
});

test('idsMissingFromArgv reports by id when a value coincidentally appears elsewhere in argv but not in the scoped candidate list', () => {
  // Fix round 3 (MINOR): a REAL argv. In the Claude facts argv the bare token 'Bash' is the value
  // of `--tools`, not a --disallowedTools rule. An entry whose only rule is 'Bash' is present in
  // the raw argv yet absent from the deny list: scoping must report it missing.
  const built = buildClaudeArgv({ role: 'facts', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c', renderedForbidden: SYNTHETIC_STAR_RENDER });
  const rendered = [{ id: 'bare-bash', rules: ['Bash'], enforced: true }];
  assert.equal(built.argv.filter((t) => t === 'Bash').length, 1); // precondition: exactly once, as the --tools value
  const attached = valuesAfterFlag(built.argv, '--disallowedTools');
  assert.equal(attached.includes('Bash'), false);
  assert.deepEqual(idsMissingFromArgv(rendered, attached), ['bare-bash']);
  assert.deepEqual(idsMissingFromArgv(rendered, built.argv), []); // the unscoped haystack would false-positive
});
