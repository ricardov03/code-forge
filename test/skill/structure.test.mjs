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
import { REFERENCES_DIR, ROOT, SKILL_DIR, listSkillFiles, readSkillFile, section, tableRows } from './helpers.mjs';

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
  'references/autopilot.md',
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

test('skill/ holds exactly the 21 owned files', async () => {
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

test('B49b: references/autopilot.md exists, SKILL.md links it once, and it names both tabs, the link step and the no-connector record', async () => {
  const skill = await readSkillFile('SKILL.md');
  assert.equal(skill.split('`references/autopilot.md`').length - 1, 1);
  const text = await readSkillFile('references/autopilot.md');
  assert.equal((text.match(/\*\*Binnacle\*\*/g) ?? []).length, 1, 'the Binnacle tab');
  assert.equal((text.match(/\*\*Full log\*\*/g) ?? []).length, 1, 'the Full log tab');
  assert.match(text, /`forge autopilot binnacle --run <r> --link <url>`/);
  assert.match(text, /`forge autopilot ask --run <r> --scope <scope>/);
  assert.match(text, /a waiver `waive,fix` · an extra round `allow,deny` · a coder level the candidate levels, e\.g\. `L1,L2`/);
  assert.match(text, /Never rewrite the whole doc/);
  assert.match(text, /^## §4 Without the Docs connector$/m);
  assert.match(text, /the two Markdown files in the run dir are the record/);
  const adapter = await readSkillFile('references/adapters/claude-code.md');
  assert.match(adapter, /^## §4 Autopilot — the live log doc \(Claude Docs connector\)$/m);
  assert.match(adapter, /Binnacle `order "a0"`, Full log `order "a1"`/);
});

test('B49b fix 1: the binnacle shape reads "the title and byline, then 7 sections: …" in the skill, the adapter and docs/autopilot.md', async () => {
  const SHAPE = 'the title and byline, then 7 sections: Status at a glance, Decisions, Blocks, Open questions, Actions only you can take, Incidents, Timeline';
  const docs = await readFile(path.join(ROOT, 'docs', 'autopilot.md'), 'utf8');
  const texts = { skill: await readSkillFile('references/autopilot.md'), adapter: await readSkillFile('references/adapters/claude-code.md'), docs: docs.replace(/\n\s*/g, ' ') };
  assert.deepEqual(Object.fromEntries(Object.entries(texts).map(([k, t]) => [k, t.split(SHAPE).length - 1])), { skill: 1, adapter: 1, docs: 1 });
  for (const [k, t] of Object.entries(texts)) assert.equal(/\b8 sections\b/.test(t), false, `${k} still says 8 sections`);
});

test('B49b fix 2–6: link stored first, one doc per run, refused edits keep the owner\'s, stop line, Full log at creation, Markdown at start', async () => {
  const text = await readSkillFile('references/autopilot.md');
  const s1 = section(text, 1);
  const link = s1.indexOf('**Store the link first**');
  const open = s1.indexOf('Open the doc for the owner');
  assert.ok(link > 0 && open > link, `the link is stored (${link}) before the doc is opened (${open})`);
  assert.match(s1, /never create a second doc for the same run/);
  assert.match(s1, /the 7 Binnacle sections, then the Full log table/);
  assert.match(section(text, 2), /re-read it, keep the owner's edit, and apply only the new row\(s\)\. Never force\./);
  const s3 = section(text, 3);
  assert.match(s3, /Add the stop \(or expiry\) event as the newest Timeline row and the newest Full log row, then write the final Status at a glance/);
  assert.match(s3, /"review Open questions and Actions only you can take before work resumes"/);
  const s4 = section(text, 4);
  assert.match(s4, /run `forge autopilot binnacle --run <r> --markdown` once/);
  assert.match(s4, /the CLI rewrites both files by itself, after every delegate decision and at every `forge autopilot status` and `forge autopilot stop`/);
  const adapter = await readSkillFile('references/adapters/claude-code.md');
  const steps = ['0. **One doc per run:**', '1. **Birth, one `batch`:**', '2. **Store the link at once,**', '3. **Open it**', '4. **Fill one section per `update`**', '5. **During the window:**', '6. **At stop:**'].map((m) => adapter.indexOf(m));
  assert.equal(steps.every((i, n) => i > 0 && (n === 0 || i > steps[n - 1])), true, `adapter steps in order: ${steps.join(',')}`);
  assert.match(adapter, /keep their edit, apply only the new row\(s\); never `force`/);
});

test('B49b: the never-delegated list in references/autopilot.md §5 has its 5 items, in order', async () => {
  const text = await readSkillFile('references/autopilot.md');
  assert.match(text, /^## §5 Never delegated — always the owner$/m);
  const items = [...section(text, 5).matchAll(/^- (.+)$/gm)].map((m) => m[1]);
  assert.deepEqual(items, [
    'plan approval (`plan.md` §4.1) and design approval;',
    'a budget raise (`code.md` §6, `review.md` §7);',
    'a threshold edit or any other limit or rule change — only the owner\'s own `forge autopilot approve` at a terminal;',
    'a critical waiver, a proof waiver, or closing a block without its reviews;',
    'merges and destructive actions.',
  ]);
});

test('B49b: every owner stop in the skill carries its Autopilot note (5 files, one note each)', async () => {
  const files = ['references/decisions.md', 'references/review.md', 'references/harden.md', 'references/plan.md', 'references/code.md'];
  /** @type {Record<string, number>} */
  const counts = {};
  for (const f of files) {
    const text = await readSkillFile(f);
    counts[f] = text.split('**Autopilot:**').length - 1;
    for (const note of text.split('**Autopilot:**').slice(1)) {
      const sentence = note.split('\n')[0];
      assert.match(sentence, /[Nn]ever delegated/, `${f}: the note says what is never delegated`);
      assert.match(sentence, /`autopilot\.md`/, `${f}: the note points to autopilot.md`);
    }
  }
  assert.deepEqual(counts, { 'references/decisions.md': 1, 'references/review.md': 2, 'references/harden.md': 1, 'references/plan.md': 1, 'references/code.md': 1 });
  assert.match(await readSkillFile('references/decisions.md'), /\*\*Autopilot:\*\* while a grant is active, a warning or nit waiver, one extra round at `review_cap` and a coder level go first to the delegate/);
});
