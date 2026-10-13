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
const RULE_COUNT = 17;
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
  assert.equal(listed[RULE_COUNT - 1].startsWith('R17 —'), true);
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

test('B51: R17 "Never relax a rule alone" is in rules-core.md, SKILL.md §4, autopilot.md §5 and decisions.md §7', async () => {
  const core = await readSkillFile('references/rules-core.md');
  assert.match(core, /^# Core rules — seventeen, each with its why$/m);
  assert.equal(core.split('### R17 — Never relax a rule alone').length - 1, 1);
  assert.match(core, /When a limit blocks work: stop, show the data, propose options, wait\./);
  assert.match(core, /only the owner's own `forge autopilot approve`/);
  const skill = await readSkillFile('SKILL.md');
  assert.equal(section(skill, 4).split('- **R17** Never relax a rule alone').length - 1, 1);
  const autopilot = section(await readSkillFile('references/autopilot.md'), 5);
  assert.equal(autopilot.split('`rules-core.md` R17').length - 1, 1);
  const decisions = section(await readSkillFile('references/decisions.md'), 7);
  assert.equal(decisions.split('`rules-core.md` R17').length - 1, 1);
});

test('B63b: the coder-brief template has its sections in order, the Context pack slot just before Project rules, and states the TESTS line as code.md does', async () => {
  const brief = await readSkillFile('templates/coder-brief.md');
  const heads = brief.split('\n').filter((l) => l.startsWith('## '));
  assert.deepEqual(heads, [
    '## Reply contract',
    '## The file loop',
    '## Final report — FAILED unless it lists the ticket ids',
    '## Fix-round brief (round ≥ 2, or a respawn after a climb)',
    '## Context pack',
    '## Project rules',
  ]);
  /** @param {string} head @returns {string} the text from `head` to the next `## ` heading */
  const part = (head) => {
    const start = brief.indexOf(`\n${head}\n`);
    assert.notEqual(start, -1, head);
    const next = brief.indexOf('\n## ', start + head.length + 1);
    return brief.slice(start, next === -1 ? undefined : next);
  };
  /** @param {string} text @param {string} needle */
  const count = (text, needle) => text.split(needle).length - 1;

  const report = part('## Final report — FAILED unless it lists the ticket ids');
  assert.match(report, /^Run the block's test command \(`test_command`\) and print its result on a line of its own, exactly:$/m);
  assert.equal(count(report, '`TESTS <command> exit=<n>`'), 1);
  assert.match(report, /^`TESTS <command> exit=<n>`$/m);
  // the example: one TESTS line, and the next non-blank line is the COMPLETE sentinel
  const lines = report.split('\n');
  const at = lines.flatMap((l, i) => (/^TESTS \S/.test(l) ? [i] : []));
  assert.equal(at.length, 1, lines.join('\n'));
  const after = lines.slice(at[0] + 1).find((l) => l.trim() !== '');
  assert.equal(after, '===BLOCK B7 COMPLETE===');

  const pack = part('## Context pack');
  assert.equal(count(pack, '<!--'), 1);
  assert.equal(count(pack, '`forge context pack --run <r> --block <id>` writes it'), 1);

  const code = await readSkillFile('references/code.md');
  assert.equal(count(code, 'prints one line that is exactly `TESTS <command> exit=<n>` just before `===BLOCK <id> COMPLETE===`'), 1);
  assert.equal(count(code, 'Set `context.require_coder_tests: true` in `.code-forge.yml` **before `run start`** to make it a close check'), 1);
  for (const reason of [
    '`coder report FAILED: context.require_coder_tests needs --report`',
    '`coder report FAILED: --report cannot be read`',
    '`coder report FAILED: no ===BLOCK <id> COMPLETE=== sentinel`',
    '`coder report FAILED: no TESTS line`',
    '`coder report FAILED: TESTS exit=<n>`',
  ]) {
    assert.equal(count(code, reason), 1, reason);
  }
});

// ── B67: brief mode in the docs and the skill ───────────────────────────────

const BRIEF_DOC = path.join(ROOT, 'docs', 'brief-mode.md');
const SCHEMA_FILE = path.join(ROOT, 'schema', 'code-forge.schema.json');

/**
 * @param {string} text @param {string} [lang] - a fence language; omitted = every fence, any
 *   language or none.
 * @returns {string[]} the fence bodies
 */
const fences = (text, lang) =>
  [...text.matchAll(/^([ \t]*)```([\w-]*)[ \t]*\r?\n([\s\S]*?)^\1```[ \t]*\r?$/gm)].filter((m) => lang === undefined || m[2] === lang).map((m) => m[3]);

/**
 * The verbs a text names as `code-forge <verb>` / `forge <verb>` (`npx` in front allowed): in
 * inline code spans, and in fences of any language or none, indented too. Each fenced line is
 * split on `&&`, `;` and `|`; a leading `$ ` prompt and leading `VAR=value` assignments are
 * stripped from each command first.
 * @param {string} text @returns {Set<string>}
 */
function namedVerbs(text) {
  /** @type {Set<string>} */
  const named = new Set();
  for (const m of text.matchAll(/`(?:npx\s+)?(?:code-forge|forge)\s+([a-z][a-z-]*)\b/g)) named.add(m[1]);
  for (const body of fences(text)) {
    for (const raw of body.split(/\r?\n/)) {
      for (const part of raw.split(/&&|;|\|/)) {
        const cmd = part.trim().replace(/^\$\s+/, '').replace(/^(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)+/, '');
        const m = cmd.match(/^(?:npx\s+)?(?:code-forge|forge)\s+([a-z][a-z-]*)\b/);
        if (m) named.add(m[1]);
      }
    }
  }
  return named;
}

/**
 * Every key path of a parsed YAML mapping, depth first (`gates.test.env.NODE_ENV`). The object
 * elements of a list add their keys under the list's own path (`levels.L0.fallback.model`);
 * scalars are leaves.
 * @param {Record<string, any>} node @param {string} [prefix] @returns {string[]}
 */
function keyPaths(node, prefix = '') {
  /** @type {string[]} */
  const out = [];
  for (const [k, v] of Object.entries(node)) {
    const p = prefix ? `${prefix}.${k}` : k;
    out.push(p);
    const objects = Array.isArray(v) ? v.filter((e) => e !== null && typeof e === 'object' && !Array.isArray(e)) : v !== null && typeof v === 'object' ? [v] : [];
    for (const o of objects) for (const sub of keyPaths(o, p)) if (!out.includes(sub)) out.push(sub);
  }
  return out;
}

/**
 * The schema nodes a key path reaches: `$ref` is followed, every `anyOf`/`oneOf`/`allOf` branch
 * is tried, an array's `items` is entered, `patternProperties` match by their pattern, and an
 * object `additionalProperties` schema (or `additionalProperties: true`) takes any other key.
 * `$ref` segments are JSON-pointer decoded (`~1` ⇒ `/`, `~0` ⇒ `~`). Empty when the path does
 * not exist.
 * @param {Record<string, any>} schema @param {string} keyPath @returns {Array<Record<string, any>>}
 */
function schemaAt(schema, keyPath) {
  /** @param {string} s @returns {string} */
  const unescape = (s) => s.replace(/~1/g, '/').replace(/~0/g, '~');
  /** @param {Record<string, any>} n @returns {Array<Record<string, any>>} */
  const expand = (n) => {
    if (n.$ref) return expand(n.$ref.split('/').slice(1).map(unescape).reduce((/** @type {any} */ o, /** @type {string} */ k) => o[k], schema));
    const branches = [...(n.anyOf ?? []), ...(n.oneOf ?? []), ...(n.allOf ?? [])];
    const self = n.items && typeof n.items === 'object' ? [n, ...expand(n.items)] : [n];
    return [...self, ...branches.flatMap(expand)];
  };
  let nodes = expand(schema);
  for (const key of keyPath.split('.')) {
    nodes = nodes.flatMap((n) => {
      if (n.properties && Object.hasOwn(n.properties, key)) return expand(n.properties[key]);
      const pattern = Object.entries(n.patternProperties ?? {}).find(([re]) => new RegExp(re).test(key));
      if (pattern) return expand(pattern[1]);
      if (n.additionalProperties === true) return [{}];
      return n.additionalProperties && typeof n.additionalProperties === 'object' ? expand(n.additionalProperties) : [];
    });
  }
  return nodes;
}

test('B67 (3): docs/brief-mode.md exists, docs/README.md lists it as 5c, how-it-works.md links it', async () => {
  assert.equal(await exists(BRIEF_DOC), true);
  const readme = await readFile(path.join(ROOT, 'docs', 'README.md'), 'utf8');
  const rows = readme.split('\n').filter((l) => l.startsWith('|') && l.includes('](brief-mode.md)'));
  assert.equal(rows.length, 1, rows.join('\n'));
  assert.match(rows[0], /^\| 5c \| \[brief-mode\.md\]\(brief-mode\.md\) \| /);
  const howItWorks = await readFile(path.join(ROOT, 'docs', 'how-it-works.md'), 'utf8');
  assert.equal(howItWorks.split('[brief-mode.md](brief-mode.md)').length - 1, 1);
});

test('B67 (3): every code-forge verb docs/brief-mode.md names is a verb bin/code-forge.mjs discovers', async () => {
  const doc = await readFile(BRIEF_DOC, 'utf8');
  const verbs = new Set(await listVerbs());
  const named = namedVerbs(doc);
  assert.deepEqual([...named].sort(), ['block', 'context', 'gates', 'init', 'ledger', 'report', 'run', 'status', 'validate']);
  assert.deepEqual([...named].filter((v) => !verbs.has(v)), []);
  // controls: a prompt, env assignments and any fence language are read; an unknown verb is caught
  const control = [
    '```sh\n$ cd x && npx code-forge frobnicate --x\n```',
    '```\nFOO=1 BAR=x forge validate | tee out; forge status\n```',
    '- a list item:\n\n  ```console\r\n  $ forge gates run\r\n  ```\r\n',
    '`npx code-forge report --slug s`',
  ].join('\n\n');
  assert.deepEqual([...namedVerbs(control)].sort(), ['frobnicate', 'gates', 'report', 'status', 'validate']);
  assert.deepEqual([...namedVerbs(control)].filter((v) => !verbs.has(v)), ['frobnicate']);
});

test('B67 (3): every key path of the brief-mode.md config example exists in the schema, and a misspelt one is caught', async () => {
  const doc = await readFile(BRIEF_DOC, 'utf8');
  const yamlBlocks = fences(doc, 'yaml');
  assert.equal(yamlBlocks.length, 1);
  const { parse } = await import('yaml');
  const example = parse(yamlBlocks[0]);
  const schema = JSON.parse(await readFile(SCHEMA_FILE, 'utf8'));
  const paths = keyPaths(example);
  assert.equal(paths.length, 45);
  assert.deepEqual(paths.filter((p) => schemaAt(schema, p).length === 0), []);
  for (const p of ['review.external.required', 'review.external.name', 'gates.test.argv', 'gates.test.env', 'gates.full_suite_threshold_files', 'proof.isolation', 'proof.worktree.branch', 'context.rules', 'context.lessons', 'context.commands.test', 'context.max_lines', 'context.require_coder_tests', 'hooks.on_block_end.argv', 'hooks.on_block_end.timeout_s', 'autonomy.coder_may_push', 'autonomy.coder_may_open_draft_pr']) {
    assert.equal(paths.includes(p), true, p);
  }
  assert.deepEqual(example.gates.test.argv, ['node', '--test', '{test_files}']);
  // the walker refuses what the schema does not have
  assert.deepEqual(['review.externl', 'hooks.on_block_end.shell', 'context.commands.deploy', 'autonomy.coder_may_merge'].map((p) => schemaAt(schema, p).length), [0, 0, 0, 0]);
  // the example has no list of objects; the walker still enters one (levels.<L>.fallback[])
  const fallback = keyPaths({ levels: { L0: { model: 'm', fallback: [{ provider: 'openai', model: 'x' }, { provider: 'xai', model: 'y', effort: 'high' }] } } });
  assert.deepEqual(fallback, ['levels', 'levels.L0', 'levels.L0.model', 'levels.L0.fallback', 'levels.L0.fallback.provider', 'levels.L0.fallback.model', 'levels.L0.fallback.effort']);
  assert.deepEqual(fallback.filter((p) => schemaAt(schema, p).length === 0), []);
  assert.equal(schemaAt(schema, 'levels.L0.fallback.providr').length, 0);
  // the example's levels are the provider: anthropic defaults
  const anthropic = (await import('../../src/config/defaults/anthropic.mjs')).default;
  assert.equal(example.provider, anthropic.provider);
  assert.deepEqual(example.levels, JSON.parse(JSON.stringify(anthropic.levels)));
});

test('B67 (1)(2): SKILL.md names --mode brief and when to use each mode; code.md names the context pack, the done file, status and ledger outside-review', async () => {
  const skill = await readSkillFile('SKILL.md');
  assert.match(skill, /\*\*Brief mode\*\* — `forge run start --mode brief` \(`references\/plan\.md` §6\) — is for one small task or a short list of tasks/);
  assert.match(skill, /no plan doc, no `forge plan check`, no plan approval/);
  assert.match(skill, /\*\*Plan mode\*\* \(the default\) is for multi-block work that needs `forge plan check` and the owner's approval\./);
  const code = await readSkillFile('references/code.md');
  for (const needle of ['`forge context pack --run <r> --block <id>`', '`<run root>/blocks/<id>.done.json`', '`forge status` (B64a)', '`forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...`']) {
    assert.equal(code.includes(needle), true, needle);
  }
});

// ── B76: the review exits in the skill and the docs ─────────────────────────

/** @param {string} text @param {string} needle @returns {number} how many times `needle` occurs */
const occurrences = (text, needle) => text.split(needle).length - 1;

/**
 * Each needle occurs exactly once in `text`, and they occur in the given order.
 * @param {string} text @param {string[]} needles
 */
function assertOnceInOrder(text, needles) {
  assert.deepEqual(needles.map((n) => occurrences(text, n)), needles.map(() => 1), 'each needle exactly once');
  const at = needles.map((n) => text.indexOf(n));
  assert.deepEqual([...at].sort((a, b) => a - b), at, `in order: ${at.join(',')}`);
}

test('B76 (1): review.md §5 names the 4 exits in order, the gate.review_waived_all close and the rebuild from the ledger', async () => {
  const review = await readSkillFile('references/review.md');
  assert.equal(occurrences(review, '\n## §5 The convergence rule — bounded fix rounds (R15)\n'), 1);
  const s5 = section(review, 5);
  assertOnceInOrder(s5, [
    '**The ways out of a stop, in this order** (take the first that fits):',
    '\n   - **Exit 1 — fix, then `block reopen` (B72).**',
    '\n   - **Exit 2 — `review-file --answers` (B71).**',
    '\n   - **Exit 3 — `block waive`.**',
    '\n   - **Exit 4 — re-decompose.**',
    '\n   - **A fully waived or answered file closes with a `gate.review_waived_all` row.**',
    '\n   - **Not exits.** `block attempt` never resets review state:',
    '\n8. **A state file that goes missing is rebuilt from the ledger.**',
  ]);
  assert.equal(occurrences(s5, '**Exit '), 4);
  assert.equal(occurrences(s5, 'The close writes one signed `gate.review_waived_all {block, file, content_hash, findings, by}` row per such file just before `block.close`'), 1);
  assert.equal(occurrences(s5, 'A missing file whose anchor has no `state_body` (an older run, or `state_omitted`) is `lost`.'), 1);
});

test('B76 (2): code.md §5 orders the exits, runs block reopen only after the human says so, and says when to use --answers and --fresh; the brief names no human-only verb', async () => {
  const code = await readSkillFile('references/code.md');
  assert.equal(occurrences(code, '\n## §3 Dispatch — what the brief contains\n'), 1);
  assert.equal(occurrences(code, '\n## §5 The block gate is a re-measurement — `forge block close <id> --run <r>`\n'), 1);
  const s5 = section(code, 5);
  assertOnceInOrder(s5, [
    '**When a file stops** (`review_cap`, `l3_patch_exhausted`, `split_required`, …), the block cannot close.',
    '\n1. **Fix, then `block reopen`.**',
    'Then run `forge block reopen <id> --file <path> --run <r> --reason <text>` only after the human says so in chat, with their reason, exactly like `block waive`.',
    '\n2. **`review-file --answers`.** Use it when the fix belongs in another owned file (a missing test, say).',
    '\n3. **`block waive`.**',
    '\n4. **Re-decompose.**',
    '\n`block attempt` never resets review state: it only raises the attempt counter, and a stopped file stays stopped.',
    'Use `review-file --fresh` only after a result that never counted (`stale`, `unavailable`, or `refused` with `other-run`, `context-unreadable` or `worktree-*`); it never re-runs a counted round.',
    'A missing state file needs no exit: the worker first rebuilds it from its ledger anchor (`review.state_restored`).',
    'Only a state whose anchor has no `state_body` is `lost` (`review-state-lost`); that one needs the human\'s `block reopen`, on its current content.',
    'A `review-state-tampered` file has none of these exits: only the owner\'s `block close --no-require-reviews` gets past it.',
  ]);
  assert.equal(occurrences(s5, 'the next round is a full round 1 when the content differs from what the stop reviewed; on the stop\'s own content the stop repeats.'), 1);
  // the coder brief (§3 items 1–6) never names a human-only verb; the rule itself stays
  const s3 = section(code, 3);
  const from = s3.indexOf('The brief file carries, in this order:');
  const to = s3.indexOf('The brief never mentions');
  assert.notEqual(from, -1, 'brief items start found');
  assert.notEqual(to, -1, 'brief items end found');
  const items = s3.slice(from, to);
  assert.equal(items.length > 1000, true, `brief items found (${items.length} chars)`);
  assert.deepEqual(['block reopen', 'block waive', '--no-require-reviews'].map((v) => occurrences(items, v)), [0, 0, 0]);
  assert.equal(occurrences(items, '`forge review-file <that file> --block <id> --run <r> --answers <ids> --of <source>`'), 1);
  assert.equal(occurrences(code, 'The brief never mentions `block waive`, `block reopen`, keys, or `~/.code-forge/runs/`.'), 1);
});

test('B76 (3): how-it-works.md says where review state lives and that the ledger anchors carry it', async () => {
  const doc = await readFile(path.join(ROOT, 'docs', 'how-it-works.md'), 'utf8');
  assert.equal(occurrences(doc, '\n## Where state lives\n'), 1);
  const start = doc.indexOf('\n## Where state lives\n');
  assert.notEqual(start, -1, 'Where state lives found');
  const next = doc.indexOf('\n## ', start + 1);
  const where = doc.slice(start, next === -1 ? undefined : next);
  const rows = where.split('\n').filter((l) => l.startsWith('| `<code-forge home>/runs/<run>/review-state/<block>/` (default `~/.code-forge`) |'));
  assert.equal(rows.length, 1);
  assert.equal(occurrences(rows[0], 'Every save also writes a signed `review.state` anchor row to the ledger, and the anchor carries the state (`state_body`).'), 1);
  assert.equal(occurrences(rows[0], 'A missing one is rebuilt from that anchor (a signed `review.state_restored` row)'), 1);
  assert.equal(occurrences(rows[0], 'Outside the temp root, so a temp sweep does not lose it.'), 1);
  assert.equal(occurrences(doc, 'only your `--no-require-reviews`'), 0);
  assert.equal(occurrences(doc, 'not a stop and has none of these exits: only the owner\'s `block close --no-require-reviews` gets past it.'), 1);
});

test('B76 (4): the forbidden list names block reopen from a coder, and the brief template names --answers but no human-only verb', async () => {
  const security = await readSkillFile('references/security.md');
  assert.equal(occurrences(security, '`forge worker`, `run start`, `run reload`, `block waive` and `block reopen` from a coder (also via `npx`)'), 1);
  const brief = await readSkillFile('templates/coder-brief.md');
  assert.equal(occurrences(brief, '`forge review-file <that file> --block <id> --run <r> --answers <ids> --of <source>`'), 1);
  assert.equal(occurrences(brief, 'A fix made in another owned file is sent with `--answers <ids> --of <source>` (The file loop).'), 1);
  assert.deepEqual(['block reopen', 'block waive', '--no-require-reviews'].map((v) => occurrences(brief, v)), [0, 0, 0]);
});
