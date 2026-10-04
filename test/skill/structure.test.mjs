/**
 * Structural lints for the skill (plan §1.4, §1.5, §9.1 "skill/", the B14 row of §10.4):
 * line budgets, referenced files exist, the survivor table is traceable, the SKILL.md §4 rules
 * match rules-core.md's headings, `forge facts` precedes `forge author`, the two sentences the
 * plan names verbatim, no real project name, and every `forge <verb>` the skill names is a verb
 * the CLI router discovers.
 */
import assert from 'node:assert/strict';
import { access, readFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { listVerbs } from '../../bin/code-forge.mjs';
import { REFERENCES_DIR, SKILL_DIR, listSkillFiles, readSkillFile, section, tableRows } from './helpers.mjs';

const SKILL_MAX_LINES = 120;
const SKILL_PLUS_CORE_MAX_LINES = 200;
const RULE_COUNT = 16;
const SURVIVOR_ROWS = 14;

/** The files the B14 row owns under `skill/` — exactly these, no more (a stray file is a finding). */
const OWNED = [
  'SKILL.md',
  'scripts/forge',
  'references/rules-core.md',
  'references/plan.md',
  'references/harden.md',
  'references/code.md',
  'references/review.md',
  'references/decisions.md',
  'references/proof.md',
  'references/ledger.md',
  'references/continuity.md',
  'references/degraded.md',
  'references/security.md',
  'references/adapters/solo.md',
  'references/adapters/claude-code.md',
  'references/adapters/subprocess.md',
  'templates/facts-sheet.md',
  'templates/plan.md',
  'templates/block-record.md',
  'templates/coder-brief.md',
];

/** @param {string} text @returns {number} */
const lineCount = (text) => text.split('\n').filter((l, i, a) => !(i === a.length - 1 && l === '')).length;

/** @param {string} p @returns {Promise<boolean>} */
async function exists(p) {
  try {
    await access(p);
    return true;
  } catch {
    return false;
  }
}

/**
 * Every `references/...`, `templates/...`, `scripts/...` or `adapters/...` path the skill prose
 * names in backticks or Markdown links. `adapters/x.md` is resolved relative to `references/`.
 * @param {string} text
 * @returns {string[]} absolute candidate paths (deduplicated).
 */
function referencedPaths(text) {
  const out = new Set();
  const re = /`((?:references|templates|scripts|adapters)\/[A-Za-z0-9_./<>-]+)`|\]\(((?:references|templates|scripts|adapters)\/[A-Za-z0-9_./-]+)\)/g;
  let m;
  while ((m = re.exec(text))) {
    const rel = m[1] ?? m[2];
    if (rel.includes('<') || rel.endsWith('/') || rel.includes('*')) continue; // a placeholder such as adapters/<engine>.md
    out.add(rel.startsWith('adapters/') ? path.join(REFERENCES_DIR, rel) : path.join(SKILL_DIR, rel));
  }
  return [...out];
}

test('skill/ holds exactly the 20 owned files', async () => {
  const files = (await listSkillFiles()).map((f) => path.relative(SKILL_DIR, f));
  assert.deepEqual(files.sort(), [...OWNED].sort());
});

test('SKILL.md frontmatter names the skill code-forge', async () => {
  const skill = await readSkillFile('SKILL.md');
  assert.match(skill, /^---\nname: code-forge\ndescription: .+\n---\n/);
});

test(`SKILL.md is <= ${SKILL_MAX_LINES} lines and <= ${SKILL_PLUS_CORE_MAX_LINES} together with rules-core.md`, async () => {
  const skill = lineCount(await readSkillFile('SKILL.md'));
  const core = lineCount(await readSkillFile('references/rules-core.md'));
  assert.ok(skill <= SKILL_MAX_LINES, `SKILL.md has ${skill} lines`);
  assert.ok(skill + core <= SKILL_PLUS_CORE_MAX_LINES, `SKILL.md + rules-core.md = ${skill + core} lines`);
});

test('every file the skill prose references exists (and the sweep found references to check)', async () => {
  const missing = [];
  let checked = 0;
  for (const file of await listSkillFiles()) {
    if (!file.endsWith('.md')) continue;
    for (const p of referencedPaths(await readFile(file, 'utf8'))) {
      checked += 1;
      if (!(await exists(p))) missing.push(`${path.relative(SKILL_DIR, file)} -> ${path.relative(SKILL_DIR, p)}`);
    }
  }
  assert.ok(checked >= 20, `expected >= 20 path references across the skill, found ${checked}`);
  assert.deepEqual(missing, []);
});

test(`the survivor table has ${SURVIVOR_ROWS} rows and every target (file + R<n>/§<n>) resolves to a heading`, async () => {
  const core = await readSkillFile('references/rules-core.md');
  const rows = tableRows(core, 'survivor');
  assert.equal(rows.length, SURVIVOR_ROWS);
  const unresolved = [];
  let targets = 0;
  for (const [survivor, landsIn] of rows) {
    const re = /`((?:adapters\/)?[a-z-]+\.md)` (R\d+|§\d+)/g;
    let m;
    let found = 0;
    while ((m = re.exec(landsIn))) {
      found += 1;
      targets += 1;
      const text = await readFile(path.join(REFERENCES_DIR, m[1]), 'utf8');
      const heading = new RegExp(`^#{2,3} ${m[2].replace('§', '§')}(?:\\b|(?=\\s|—))`, 'm');
      if (!heading.test(text)) unresolved.push(`${survivor}: ${m[1]} ${m[2]}`);
    }
    if (found === 0) unresolved.push(`${survivor}: no target of the form \`file.md\` R<n>/§<n>`);
  }
  assert.ok(targets >= SURVIVOR_ROWS, `expected at least one target per row, found ${targets}`);
  assert.deepEqual(unresolved, []);
});

test(`SKILL.md §4 lists ${RULE_COUNT} rules and they equal rules-core.md's headings, in order`, async () => {
  const skill = await readSkillFile('SKILL.md');
  const core = await readSkillFile('references/rules-core.md');
  const listed = [...section(skill, 4).matchAll(/^- \*\*(R\d+)\*\* (.+)$/gm)].map((m) => `${m[1]} — ${m[2].trim()}`);
  const headings = [...core.matchAll(/^### (R\d+) — (.+)$/gm)].map((m) => `${m[1]} — ${m[2].trim()}`);
  assert.equal(listed.length, RULE_COUNT);
  assert.equal(headings.length, RULE_COUNT);
  assert.deepEqual(listed, headings);
  assert.equal(listed[0].startsWith('R1 —'), true);
  assert.equal(listed[RULE_COUNT - 1].startsWith('R16 —'), true);
});

test('SKILL.md §3 names `forge facts` at a lower index than `forge author` (facts before design)', async () => {
  const loop = section(await readSkillFile('SKILL.md'), 3);
  const facts = loop.indexOf('`forge facts');
  const author = loop.indexOf('`forge author');
  assert.ok(facts >= 0, 'forge facts is named in §3');
  assert.ok(author >= 0, 'forge author is named in §3');
  assert.ok(facts < author, `forge facts (${facts}) must come before forge author (${author})`);
});

test('references/security.md contains "same-user boundary only" exactly once', async () => {
  const text = await readSkillFile('references/security.md');
  assert.equal(text.split('same-user boundary only').length - 1, 1);
});

test('references/review.md states the four-round cap and the fix-hunk rule (2 of 2)', async () => {
  const text = await readSkillFile('references/review.md');
  assert.equal(/four rounds per file/.test(text), true, 'the four-round cap');
  assert.equal(/reviews only the fix hunks/.test(text), true, 'the fix-hunk rule');
  assert.match(text, /`review\.max_rounds_per_file`, default 4/);
});

test('every `forge <verb>` the skill names is a verb bin/code-forge.mjs discovers', async () => {
  const verbs = new Set(await listVerbs());
  const named = new Set();
  for (const file of await listSkillFiles()) {
    if (!file.endsWith('.md')) continue;
    for (const m of (await readFile(file, 'utf8')).matchAll(/`forge ([a-z][a-z-]*)\b/g)) named.add(m[1]);
  }
  const unknown = [...named].filter((v) => !verbs.has(v));
  assert.ok(named.size >= 15, `expected the skill to name >= 15 distinct verbs, found ${named.size}`);
  assert.deepEqual(unknown, []);
});

test('no real project name under skill/** (0 hits), and the control line is caught (1 hit)', async () => {
  // Assembled from parts so this test file itself never carries the name as a literal token.
  const names = [['condo', 'mera'].join(''), ['finance', '360'].join('')];
  const re = new RegExp(names.join('|'), 'i');
  const hits = [];
  for (const file of await listSkillFiles()) {
    if (re.test(await readFile(file, 'utf8'))) hits.push(path.relative(SKILL_DIR, file));
  }
  assert.deepEqual(hits, []);
  const control = `the ${names[0].toUpperCase()} run, 16 PRs`;
  assert.equal((control.match(new RegExp(names.join('|'), 'gi')) ?? []).length, 1);
});

test('the plan-approval rule: SKILL.md, plan.md §4.1 and the Claude Code adapter all route the owner\'s approval through plan mode', async () => {
  const skill = await readSkillFile('SKILL.md');
  assert.equal(skill.split('in the harness\'s plan mode').length - 1, 1);
  assert.match(skill, /EnterPlanMode, write the plan, ExitPlanMode/);
  const plan = await readSkillFile('references/plan.md');
  assert.match(plan, /^### §4\.1 Owner approval in plan mode$/m);
  assert.match(plan, /never start `forge block open` before it/);
  const adapter = await readSkillFile('references/adapters/claude-code.md');
  assert.match(adapter, /^## §2\.1 Plan approval$/m);
  assert.match(adapter, /`EnterPlanMode` → write the plan file → `ExitPlanMode`/);
});
