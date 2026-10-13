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

// ── B86: how a JUnit report is read ──────────────────────────────────────────

test('B86 (9): proof.md §2 says how a test report is read and gives the 8-row fail-closed table', async () => {
  const proof = await readSkillFile('references/proof.md');
  const s2 = section(proof, 2);
  assert.equal(occurrences(s2, '**How a test report is read (B86).**'), 1);
  assert.equal(occurrences(s2, 'The reader fails closed: a report that is missing, empty, over 8 MiB, not UTF-8 or malformed, that has a `<!DOCTYPE` or `<!ENTITY` (no entity is ever expanded) or a character that is not an XML character, or that has an element other than `testsuites`, `testsuite`, `testcase`, `failure`, `error`, `skipped`, `system-out`, `system-err`, `properties`, `property` is unreadable.'), 1);
  assert.equal(occurrences(s2, 'This table applies only to a runner that writes a report; a `node:test` file is still classified from its console text, as above.'), 1);
  const rows = tableRows(s2, '`red_kind`');
  assert.deepEqual(rows.map((r) => [r[0], r[1], r[2]]), [
    ['exit 0, no signal', '`NOT_RED`', '`none`'],
    ['a timeout or a signal', '`RED_INVALID`', '`fatal`'],
    ['the console text shows an import, compile or fatal error', '`RED_INVALID`', '`import` / `compile` / `fatal`'],
    ['no report, or a report is unreadable', '`RED_INVALID`', '`error` (no readable test report)'],
    ['no test ran (zero tests, or only skipped ones)', '`RED_INVALID`', '`error` (the filter ran no test)'],
    ['any test errored, or failed without an assertion', '`RED_INVALID`', '`error`'],
    ['at least one test failed on an assertion', '`RED`', '`assertion`'],
    ['the run failed, but the report has no failure', '`RED_INVALID`', '`error`'],
  ]);
  assert.equal(occurrences(s2, 'Such a bare failure (no `type`, message exactly `failure` or `failed`, an empty body; a body of only whitespace, as text or CDATA, counts as empty) counts as an assertion only when the console text has XCTest\'s assertion line for that same test'), 1);
  assert.equal(occurrences(s2, '`failed_tests` lists at most 20 test ids of at most 200 characters each, never a failure message.'), 1);
  assert.equal(occurrences(s2, 'every test that failed at RED present and passed; anything else is `NOT_GREEN`.'), 1);
});

// ── B87: proof.runners ───────────────────────────────────────────────────────

test('B87 (12): proof.md §2 has a Runners part: keys, tokens, selection from the snapshot, the fail-closed rules and the row', async () => {
  const proof = await readSkillFile('references/proof.md');
  const s2 = section(proof, 2);
  assert.equal(occurrences(s2, '**Runners (B87).**'), 1);
  assert.equal(occurrences(s2, '`only node:test files (.mjs, .cjs, .js) have a filtered test command in this version — add a proof.runners entry for it (see proof.md §2)`'), 1);
  assert.deepEqual(
    ['- **Keys.**', '- **Tokens.**', '- **Selection.**', '- **Fail closed.**', '- **The run.**', '- **The row.**'].map((label) => occurrences(s2, label)),
    [1, 1, 1, 1, 1, 1],
  );
  assert.equal(occurrences(s2, 'In this version `swift` runs exactly as `junit`'), 0); // B88 gave swift its own rules
  assert.equal(occurrences(s2, 'A token is a whole item, never part of one (`--log-junit={report}` is refused; give `--log-junit`, `"{report}"`), and never `argv[0]`.'), 1);
  assert.equal(occurrences(s2, 'The runner list is read from the run\'s config snapshot, never from the live `.code-forge.yml`: `run start` freezes it, `run reload` refuses to change it, and while it is set no block may own `.code-forge.yml`.'), 1);
  assert.equal(occurrences(s2, '`--test <file>` with no `::case` is a usage error (exit 2, no row)'), 1);
  assert.equal(occurrences(s2, 'A changed file that a runner\'s `match` covers is a test, never a source: it is not reverted.'), 1);
  assert.equal(occurrences(s2, 'add `runner: <adapter>`, `env_keys` (the entry\'s env variable names, `[]` without an `env`) and `failed_tests` (names only) when a runner was used; an invalid RED (a missing or unreadable report) has no `failed` count and an empty `failed_tests`.'), 1);
  assert.equal(occurrences(s2, 'the entry is never run, never skipped, and the file never falls through to another entry or to `node:test`.'), 1);
  assert.equal(occurrences(s2, 'a bounded list of test names (at most 20, each cut at 200 characters, control characters removed), never a failure message.'), 1);
  assert.equal(occurrences(s2, 'The full list of failed ids that GREEN checks (`failed_ids`) stays in memory and is never stored.'), 1);
  assert.equal(occurrences(s2, 'RED is classified from the report (the table above), GREEN by the GREEN rule above; a GREEN step that throws is `NOT_GREEN`, and the row is still written.'), 1);
  assert.equal(occurrences(s2, 'which never inherits a `GIT_*` variable'), 1);
  assert.equal(occurrences(s2, 'a token of another adapter'), 0);
});

// ── B88: the swift adapter ───────────────────────────────────────────────────

test('B88 (7): proof.md §2 shows the Swift runner and the waiver for a new API; §4 says Swift VERIFIED with the versions', async () => {
  const proof = await readSkillFile('references/proof.md');
  const s2 = section(proof, 2);
  assert.equal(occurrences(s2, '- **Swift (B88).**'), 1);
  assert.equal(occurrences(s2, 'argv: [swift, test, --parallel, --filter, "{test_filter}", --xunit-output, "{report}"]'), 1);
  assert.equal(occurrences(s2, 'A `swift` runner accepts a missing case: `{test_filter}` is then the file\'s base name, regex-escaped and anchored with a `.` before it and a `/` after it (`ClampXCTests.swift` gives `\\.ClampXCTests/`), so it selects that suite only'), 1);
  assert.equal(occurrences(s2, 'A run that selects no test (a suite not named after its file) exits 0 and writes both reports with zero tests'), 1);
  assert.equal(occurrences(s2, 'RED is `NOT_RED` (exit 1, no proof; like every verdict, it still writes its `red-green` row, with `green: null` and `proven: false`); a GREEN run that selects no test is `NOT_GREEN`'), 1);
  assert.equal(occurrences(s2, 'gives `(?:^|[./])ClampSwiftTests/capsHigh\\(\\)(?![^/])`), so it never selects a near-name suite (`MoreClampSwiftTests/capsHigh()`) or a longer test name'), 1);
  assert.equal(occurrences(s2, 'The end is not `$`: SwiftPM 6.3.3 matches a Swift Testing test against an id with more after the test name'), 1);
  assert.equal(occurrences(s2, 'A missing or unreadable report proves nothing: at RED it is `RED_INVALID error`, with no `failed` count and no `failed_tests`; at GREEN it is `NOT_GREEN`, also when every test that failed at RED passed in the other report (GREEN needs every report file RED had).'), 1);
  assert.equal(occurrences(s2, 'a step that wrote the Swift Testing report but no XCTest report is `RED_INVALID error` (the reason names `--parallel`)'), 1);
  assert.equal(occurrences(s2, 'a crash (`<file>.swift:<line>: Fatal error: …`) and SwiftPM\'s closing `error: fatalError` are not compile errors'), 1);
  assert.equal(occurrences(s2, '`error: compile command failed`, `error: fatalError`'), 0);
  assert.equal(occurrences(s2, 'A `swift` runner accepts a missing case: it then filters by the file\'s suite (Swift, below).'), 1);
  assert.equal(occurrences(s2, 'Keep `--parallel`: without it SwiftPM 6.3 writes no XCTest report at `{report}`, '), 1);
  assert.equal(occurrences(s2, '`<report stem>-swift-testing.xml` (`report-swift-testing.xml`); a failure in either counts'), 1);
  assert.equal(occurrences(s2, 'A failed run that wrote no report at all and whose console shows a `swiftc` error'), 1);
  assert.equal(occurrences(s2, 'an XCTest assertion line (`<file>.swift:<line>: error: -[…]`, no column), a crash'), 1);
  assert.equal(occurrences(s2, '**A test of a new API needs the human waiver:**'), 1);
  assert.equal(occurrences(s2, 'For a high-tier file the only way past the gate is the human\'s `forge block waive <id> proof --file <path>` (D3)'), 1);
  assert.equal(occurrences(s2, '(for `swift`, anchored as `(?:^|[./])<case>(?![^/])`, and with no case the file\'s base name, regex-escaped and anchored as `\\.<name>/`)'), 1);
  assert.equal(occurrences(s2, '- **Fail closed.** A `junit` runner whose argv has `{test_filter}` needs a case'), 1);
  const s4 = section(proof, 4);
  assert.equal(occurrences(s4, '**Swift (`swift test`, XCTest and Swift Testing) red→green under export: VERIFIED** on 2026-10-13 with Apple Swift 6.3.3 (swiftlang-6.3.3.1.3), Xcode 26.6, macOS 26 on arm64.'), 1);
  assert.equal(occurrences(s4, 'Re-run it with `CODE_FORGE_LIVE_SWIFT=1 node --import ./test/helpers/isolate.mjs --test test/proof/swift-live.test.mjs`'), 1);
  assert.equal(occurrences(s4, 'a test of a new function gives `RED_INVALID compile` (exit 1), which needs the human waiver (§2 Swift)'), 1);
  assert.equal(occurrences(s4, 'and one case of each (`::ClampXCTests/testCapsHigh`, `::ClampSwiftTests/capsHigh()`), each with exactly that test in `failed_tests`: neither anchored filter selects a near-name suite, also not one with a test of the same name'), 1);
  assert.equal(occurrences(s4, 'SwiftPM builds in the export (`.code-forge/export/A/.build`), never in the package; without XCTest no XCTest file is written; a test that crashes at base (`fatalError`) gives `RED_INVALID error`; a file whose suite is not named after it selects no test and gives `NOT_RED`'), 1);
  assert.equal(occurrences(s4, 'UNVERIFIED with'), 0);
});

// ── B89: PHP red→green through the junit adapter ────────────────────────────

test('B89 (4): proof.md §2 gives the PHP runner and the new-class rule; §4 says PHP red→green VERIFIED with the versions and the artisan runner', async () => {
  const proof = await readSkillFile('references/proof.md');
  const s2 = section(proof, 2);
  assert.equal(occurrences(s2, '- **PHP (B89).**'), 1);
  assert.equal(occurrences(s2, '`php artisan test` passes the file, `--filter` and `--log-junit` through to PHPUnit and to Pest (verified live, §4), so a Laravel app needs no `vendor/bin/phpunit` or `vendor/bin/pest` runner'), 1);
  assert.equal(occurrences(s2, 'the PHPUnit method (`--test tests/Unit/ClampTest.php::test_caps_high`) or the Pest description (`--test tests/Unit/ClampTest.php::caps high`)'), 1);
  assert.equal(occurrences(s2, 'The report\'s ids are `Tests.Unit.ClampTest.test_caps_high` (PHPUnit) and `Tests.Unit.ClampTest.caps high` (Pest); in a filtered run the root suite is named after the test class (in a full-config run after the `phpunit.xml` path), and the reader does not depend on that name.'), 1);
  assert.equal(occurrences(s2, '`{test_filter}` is the case, regex-escaped. PHPUnit and Pest read `--filter` as a regex'), 1);
  assert.equal(occurrences(s2, 'a Pest description with `( ) [ ] . + ? /` is safe'), 1);
  assert.equal(occurrences(s2, '**A PHP run that selects no test is `RED_INVALID error`, where a Swift one is `NOT_RED`,** because of the exit code'), 1);
  assert.equal(occurrences(s2, 'so the classifier stops at its "the filter ran no test" row: `RED_INVALID error`'), 1);
  assert.equal(occurrences(s2, '**A test of a class the block added is `RED_INVALID`:**'), 1);
  assert.equal(occurrences(s2, 'the kind is `import` in both of Laravel\'s console forms: its plain text, and the one-line JSON summary Laravel 13 prints under an AI agent'), 1);
  assert.equal(occurrences(s2, '`error` when it prints the one-line JSON summary'), 0);
  const s4 = section(proof, 4);
  assert.equal(occurrences(s4, '**PHP (PHPUnit and Pest, Laravel) red→green under export: VERIFIED** on 2026-10-13 with PHP 8.4.25, Composer 2.10.2 and Laravel Framework 13.35.0, on PHPUnit 12.5.39 and on Pest 4.7.8 (PHPUnit 12.5.33 under it)'), 1);
  assert.equal(occurrences(s4, 'The runner is `php artisan test` (`[php, artisan, test, "{test_file}", --filter, "{test_filter}", --log-junit, "{report}"]`, §2 PHP): it passes `--log-junit` through to PHPUnit and to Pest, and both write the report.'), 1);
  assert.equal(occurrences(s4, 'gives RED `assertion`, then GREEN, exit 0 and a signed row with `runner: junit`, exactly that test in `failed_tests`'), 1);
  assert.equal(occurrences(s4, 'a test of the class the block added gives `RED_INVALID` `import` (`proof red-green` exits 1; the runner itself exits 2), from a plain terminal and under an AI agent (both seen live)'), 1);
  assert.equal(occurrences(s4, 'a root suite named after the `phpunit.xml` path, then `Unit` and `Feature`, then the classes'), 1);
  // the §2 pointer "verified live, §4" points at the section that holds the VERIFIED paragraph
  assert.equal(occurrences(section(proof, 2), 'red→green under export: VERIFIED'), 0);
  assert.equal(occurrences(s4, 'exit 0, 6 tests passed (the skeleton\'s 2 and the block\'s 4)'), 1);
  assert.equal(occurrences(s4, 'exit 0, 4 tests passed'), 0);
  assert.equal(occurrences(s4, 'The captured reports are the PHP fixtures in `test/fixtures/junit/`.'), 1);
});

// ── B102: the xcodebuild adapter ─────────────────────────────────────────────

test('B102 (7): proof.md §2 shows the Xcode runner, its tokens, the precheck codes, the reader and the build-failure rule', async () => {
  const proof = await readSkillFile('references/proof.md');
  const s2 = section(proof, 2);
  assert.equal(occurrences(s2, '- **Xcode (B102).**'), 1);
  assert.equal(occurrences(s2, 'argv: [xcodebuild, test, -workspace, "{workspace}", -scheme, "{scheme}", -destination, "{destination}", "{only_testing}", -resultBundlePath, "{result_bundle}"]'), 1);
  assert.equal(occurrences(s2, '`{only_testing}` (one item, `-only-testing:<test_target>/<Suite>[/<test>]`'), 1);
  assert.equal(occurrences(s2, '`{result_bundle}` (a fresh `<step dir>/r.xcresult` path under the run\'s temp root, outside the work dir, that does not exist yet)'), 1);
  assert.equal(occurrences(s2, '**a Swift Testing case needs its `()`:** `::ClampSwiftTests/capsHigh()`; without them nothing is selected and `xcodebuild` exits 0; an XCTest case works with or without `()`.'), 1);
  assert.equal(occurrences(s2, 'a near-name suite (`ClampXCTestsExtra`, `MoreClampSwiftTests`) was not selected'), 1);
  assert.equal(occurrences(s2, 'matches exactly'), 0);
  assert.equal(occurrences(s2, '**Exit 0 with no test run is `RED_INVALID error`** (the filter ran no test). This differs from `junit` and `swift`, where any exit 0 is `NOT_RED`'), 1);
  assert.equal(occurrences(s2, 'With a case, the scope is still the whole `<test_target>/<Suite>/`: another test of the same suite in the result is not refused'), 1);
  assert.equal(occurrences(s2, 'A nested Swift Testing suite cannot be named as a case: `Outer/Inner/test()` is refused (`usage`, nothing run)'), 1);
  assert.equal(occurrences(s2, 'two tests with the same id, and a node type, place or result the reader does not know: no readable result, `RED_INVALID error` at RED and `NOT_GREEN` at GREEN.'), 1);
  assert.equal(occurrences(s2, 'a parameterized Swift Testing test (`Arguments` nodes), repetitions (`Repetition`, `Test Case Run`), a run over several destinations or test plan configurations (`Device`, `Test Plan Configuration`), a `Runtime Warning`, and the result `Expected Failure`.'), 1);
  assert.equal(occurrences(s2, 'else exit 1 `xcode-missing` (Command Line Tools only, or no developer directory)'), 1);
  assert.equal(occurrences(s2, 'else exit 1 `destination-unavailable`, with the names that do exist (at most 10). A simulator destination with neither `name` nor `id` is `destination-unavailable` too, before `simctl` runs: the message says it names no simulator. `platform=macOS` needs no simulator check.'), 1);
  assert.equal(occurrences(s2, 'code-forge never boots, creates or deletes a simulator.'), 1);
  assert.equal(occurrences(s2, '**Only the named suite counts:** a test outside `<test_target>/<Suite>/` is `RED_INVALID error`'), 1);
  assert.equal(occurrences(s2, 'This holds for a run that exits 0 too: a passing run with any test outside the suite, or with only such tests, is `RED_INVALID error`, never `NOT_RED`'), 1);
  assert.equal(occurrences(s2, 'with an unknown adapter, an argv without a report item of any adapter (`{report}` or `{result_bundle}`) or without a test token of any adapter (`{test_file}`, `{test_id}`, `{test_filter}` or `{only_testing}`)'), 1);
  assert.equal(occurrences(s2, 'A clean exit with no bundle or an unreadable one is `RED_INVALID error` too. At GREEN, an empty run, a missing or unreadable bundle, or a test outside the suite is `NOT_GREEN`.'), 1);
  assert.equal(occurrences(s2, "`xcodebuild test`'s `Testing cancelled because the build failed.`"), 1);
  assert.equal(occurrences(s2, 'Both commands get the runner\'s env (no `GIT_*`) and a 30 s timeout.'), 1);
  assert.equal(occurrences(s2, '**A build failure is `RED_INVALID compile`:** `** BUILD FAILED **` or `** TEST BUILD FAILED **` in the output'), 1);
  assert.equal(occurrences(s2, '`xcrun xcresulttool get test-results tests --path <bundle>` (argv only, 60 s timeout, output capped at 8 MiB)'), 1);
  assert.equal(occurrences(s2, '`xcodebuild` (`xcodebuild test`, read from its result bundle; it needs an `xcode` object, Xcode below)'), 1);
});

test('B103 (4): proof.md §4 says Xcode red→green VERIFIED with the date, the Xcode version and the simulator; how-it-works names it', async () => {
  const proof = await readSkillFile('references/proof.md');
  const s4 = section(proof, 4);
  assert.equal(occurrences(s4, '**Xcode (`xcodebuild test`, XCTest and Swift Testing on an iOS simulator) red→green under export: VERIFIED** on 2026-10-13 with Xcode 26.6 (17F113) and the iPhone 17 simulator (iOS 26.5), macOS 26 on arm64.'), 1);
  assert.equal(occurrences(s4, 'Re-run it with `CODE_FORGE_LIVE_XCODE=1 node --import ./test/helpers/isolate.mjs --test test/proof/xcodebuild-live.test.mjs`'), 1);
  assert.equal(occurrences(s4, 'when `xcode-select -p` points at the Command Line Tools, or when `xcrun simctl list devices available -j` lists no iPhone on an iOS runtime'), 1);
  assert.equal(occurrences(s4, 'and never boots, creates, erases or deletes one itself'), 1);
  assert.equal(occurrences(s4, '`xcode: {scheme: Kit, destination: "platform=iOS Simulator,name=<simulator>,OS=<iOS version>", test_target: KitTests}`, the destination filled in from the chosen simulator\'s name and iOS version (on the verified run `platform=iOS Simulator,name=iPhone 17,OS=26.5`)'), 1);
  assert.equal(occurrences(s4, 'each with exactly that test in `failed_tests` (`KitTests/ClampXCTests/testCapsHigh()`, `KitTests/ClampSwiftTests/capsHigh()`)'), 1);
  assert.equal(occurrences(s4, '`xcodebuild` exits 65 at RED and 0 at GREEN, and writes a result bundle at both'), 1);
  assert.equal(occurrences(s4, 'a test of a new function gives `RED_INVALID compile` (exit 1;'), 1);
  assert.equal(occurrences(s4, 'gives exit 1 `destination-unavailable` before the export is built: no row, no `xcodebuild` run, the package tree unchanged (same hash)'), 1);
  assert.equal(occurrences(s4, 'so the B102 fixtures in `test/fixtures/xcresult/` are kept as captured'), 1);
  assert.equal(occurrences(s4, 'red→green under export: UNVERIFIED'), 0);
  const howItWorks = await readFile(path.join(ROOT, 'docs', 'how-it-works.md'), 'utf8');
  assert.equal(occurrences(howItWorks, 'on Swift, and on Xcode through `xcodebuild test` on an iOS simulator)'), 1);
});

// ── B90: size-based review routing ───────────────────────────────────────────

test('B90 (8): review.md §3 names review.routes, how a rule is picked, the row and the fail-closed codes, and says not to edit .code-forge.yml per file', async () => {
  const review = await readSkillFile('references/review.md');
  const s3 = section(review, 3);
  assert.equal(occurrences(s3, 'Big files (B90): to send a big file to a long-context reviewer, set `review.routes` once — do not edit `.code-forge.yml` per file and restore it after.'), 1);
  assert.equal(occurrences(s3, 'A rule is `{min_lines, level?, model, provider?, effort?, fallback?, session_timeout_s?, second?}`; `min_lines` and `model` are required.'), 1);
  assert.equal(occurrences(s3, 'Left out, an optional key gets: `provider` — the top-level `provider` (the rule replaces the level entry, so the level\'s own `provider` is not inherited), and validation checks the rule with that same provider; `effort` — none (the CLI\'s default); `fallback` — none; `session_timeout_s` — the rule does not count in the timeout; `second` — `review.second_levels.<level>`.'), 1);
  assert.equal(occurrences(s3, 'The rule\'s `second` is the second-level try\'s model (§4) for its level, in place of `review.second_levels.<level>` — except an L2 rule in consensus mode, whose `second` is not used.'), 1);
  assert.equal(occurrences(s3, 'and one ticket can take one L2 rule and one L3 rule; two rules with the same `level` and `min_lines` are refused.'), 1);
  assert.equal(occurrences(s3, '`second_model` is the rule\'s `second` model when that second is used, else null — always null for an L2 rule in consensus mode, because slot B is `review.second_levels.L2`, not the rule\'s'), 1);
  assert.equal(occurrences(s3, 'A retry of the same ticket never writes its rows twice.'), 1);
  assert.equal(occurrences(s3, 'One signed `review.route {level, min_lines, lines, provider, model, session_timeout_s, second_model}` row per routed level is written before the ticket\'s first session'), 1);
  assert.equal(occurrences(s3, '`reason: route-row`'), 1);
  assert.equal(occurrences(s3, '`reason: route-invalid`'), 1);
  assert.equal(occurrences(s3, 'A deleted file is never routed.'), 1);
  assert.equal(occurrences(s3, '`run reload` accepts a change to it; validation refuses two rules with the same `level` and `min_lines`, and, with `review.multimodel`, an L2 rule whose provider (its own, else the inherited top-level one) equals the second reviewer\'s (slot B\'s, or the rule\'s own `second.provider`)'), 1);
  assert.equal(occurrences(s3, '(`review-routes-invalid`)'), 1);
  // §4 still owns the timeout and second-level mechanics; the routes paragraph lives only in §3
  assert.equal(occurrences(section(review, 4), 'review.routes'), 0);
});

test('B90 (9): how-it-works, review-only and the README key table say the routing behaviour; config.md lists the key', async () => {
  const howItWorks = await readFile(path.join(ROOT, 'docs', 'how-it-works.md'), 'utf8');
  assert.equal(occurrences(howItWorks, 'A big file can go to another model with a longer timeout, without editing `.code-forge.yml` for\neach file. Set `review.routes` once: in each rule `min_lines` and `model` are required'), 1);
  assert.equal(occurrences(howItWorks, 'The worker writes one signed `review.route` row per routed level'), 1);
  assert.equal(occurrences(howItWorks, 'The second model is null when the rule\'s `second` is\nnot used, which includes an L2 rule in consensus mode, because slot B is not the rule\'s.'), 1);
  assert.equal(occurrences(howItWorks, 'A deleted file is never routed. With no `review.routes`, or no rule that matches, the\nreview runs exactly as before. `run reload` accepts a change to the key.'), 1);
  const reviewOnly = await readFile(path.join(ROOT, 'docs', 'review-only.md'), 'utf8');
  assert.equal(occurrences(reviewOnly, 'For big files, `review.routes` sets another model and timeout by\nline count'), 1);
  assert.equal(occurrences(reviewOnly, 'its newline bytes, plus one for a last line with no newline. A deleted file is never\nrouted.'), 1);
  assert.equal(occurrences(reviewOnly, 'Two rules with the same level and the same `min_lines`\nare refused (`review-routes-invalid`).'), 1);
  assert.equal(occurrences(reviewOnly, 'A rule with no `provider` runs on the top-level\n`provider`'), 1);
  assert.equal(occurrences(reviewOnly, 'its `second_model`\nis the rule\'s `second` model when that second is used, else null — always null for an L2 rule\nin consensus mode, because the consensus second reviewer (slot B) stays\n`review.second_levels.L2` and is not the rule\'s.'), 1);
  const readme = await readFile(path.join(ROOT, 'README.md'), 'utf8');
  assert.equal(occurrences(readme, '| `review.routes` | none | big files go to another model: a file with at least `min_lines` lines is reviewed by the rule\'s `model`, with its own `session_timeout_s` and `second` level |'), 1);
  const config = await readFile(path.join(ROOT, 'docs', 'reference', 'config.md'), 'utf8');
  for (const key of ['review.routes', 'review.routes[].min_lines', 'review.routes[].level', 'review.routes[].model', 'review.routes[].session_timeout_s', 'review.routes[].second']) {
    assert.equal(occurrences(config, `| \`${key}\` |`), 1, key);
  }
  assert.equal(occurrences(config, '| `review.routes[].level` | enum: `L2`, `L3` |  | `"L2"` |'), 1);
});

test('B90 (10): the four routing texts (review.md §3, how-it-works, review-only, the schema description) say the same rules, in the same order', async () => {
  const schema = JSON.parse(await readFile(path.join(ROOT, 'schema', 'code-forge.schema.json'), 'utf8'));
  const routesParagraph = (/** @type {string} */ text, /** @type {string} */ start) => text.split('\n\n').find((p) => p.startsWith(start)) ?? '';
  const texts = {
    'review.md §3': routesParagraph(section(await readSkillFile('references/review.md'), 3), 'Big files (B90):'),
    'how-it-works.md': routesParagraph(await readFile(path.join(ROOT, 'docs', 'how-it-works.md'), 'utf8'), 'A big file can go to another model'),
    'review-only.md': routesParagraph(await readFile(path.join(ROOT, 'docs', 'review-only.md'), 'utf8'), 'For big files, `review.routes`'),
    schema: schema.properties.review.properties.routes.description,
  };
  // compared as plain prose: one space for any run of whitespace, no backticks
  const plain = (/** @type {string} */ t) => t.replace(/`/g, '').replace(/\s+/g, ' ');
  const SAME = [
    'min_lines and model are required',
    "A rule's level decides which sessions it changes: an L2 rule (level: L2, the default) changes only the reviewers' model, and in consensus mode only slot A",
    'slot B stays review.second_levels.L2',
    "changes only the judge's model",
    "the file's lines at the reviewed state (the bytes in the review packet)",
    'Rules compete only within their level: among the rules of one level, the',
    "the level's own provider is not inherited",
    'with that same provider',
    'The timeout is one value for every session of the',
    'the largest session_timeout_s among the matched rules that set one',
    'a matched rule without one is left out of that max',
  ];
  for (const [name, text] of Object.entries(texts)) {
    const p = plain(text);
    assert.notEqual(p, '', name);
    for (const phrase of SAME) assert.equal(occurrences(p, phrase), 1, `${name}: ${phrase}`);
    // the level comes before the winner: "within their level" is said before "the largest min_lines"
    const within = p.indexOf('Rules compete only within their level');
    const wins = p.indexOf('min_lines at or under');
    assert.equal(within >= 0 && wins > within, true, `${name}: within-level before the winner`);
    // gone: the claim that every session of the ticket runs on the rule's model
    for (const old of ["uses the rule's model", "runs on the rule's model", "rule's model and timeout"]) assert.equal(occurrences(p, old), 0, `${name}: ${old}`);
  }
});

// ── B91: a timeout is a broken result ────────────────────────────────────────

/** @param {string} text @returns {string} the text with every whitespace run one space, lower case (for absence checks). */
const flat = (text) => text.replace(/\s+/g, ' ').toLowerCase();

/**
 * Old claims B91 removed: none may remain anywhere in `text` (whole file, any case, any wrapping).
 * @param {string} name @param {string} text @param {string[]} gone
 */
function assertGone(name, text, gone) {
  for (const old of gone) assert.equal(occurrences(flat(text), flat(old)), 0, `${name}: ${old}`);
}

/** The claims a timeout is never a broken result, or that only schema misses count. */
const B91_GONE = [
  'only a second timeout is `unavailable: timeout`',
  'only a second timeout makes the file `unavailable: timeout`',
  'second** `schema` miss',
  'Without a second level only the diagnosis row is new',
  'Without a second level, nothing else changes',
  "A second timeout is the packet's second broken result: with a second level, that level is tried",
  'that level is tried in the same ticket; without one the file is `unavailable: timeout`',
];

test('B91 (6): review.md §1 says a timeout counts as a broken result and when it is tried; §4 says the count, the trigger and the same-ticket try', async () => {
  const review = await readSkillFile('references/review.md');
  const s1 = flat(section(review, 1));
  for (const pin of [
    'A broken result is a `schema` miss or a timed-out attempt (B91): a timeout counts as a broken result, like a schema miss.',
    'Misses and timeouts of the same packet, level and role are counted together, so one miss plus one timeout is two.',
    "A packet's second broken result is tried once on the ticket's second level, in the same ticket, only when a second level exists (`review.second_levels.<level>`, or for a routed ticket the rule's `second`) and never in consensus mode (§4).",
  ])
    assert.equal(occurrences(s1, flat(pin)), 1, pin);
  const s4 = flat(section(review, 4));
  for (const pin of [
    "At a packet's **second** broken result (same run, file, content hash, packet text, level and role — counted from the signed `review.schema_invalid` rows and the signed `review.session_timeout` notes of the level's own models, plus the result in hand; misses and timeouts count together, so one miss plus one timeout is a second broken result)",
    "the worker tries the packet ONCE on the ticket's second level when there is one — `review.second_levels.<level>`, or for a routed ticket the rule's `second` (B90; an L2 rule's `second` never applies in consensus mode)",
    '`review.schema_fallback {lens, role, level, packet_hash, from_provider, from_model, to, to_provider, to_model, attempts, trigger, refused?}`',
    '`trigger` is `schema` or `timeout`, the kind of the second broken result; a row without it (written before B91) is a `schema` one.',
    'Never in consensus mode; `budget.usd` refuses it like any session.',
    'Without a second level nothing is tried and no `review.schema_fallback` row is written: a `schema` miss adds only its diagnosis row, and a timeout runs as before B91, its note now carrying `packet_hash` and signed.',
    "A second timeout is the packet's second broken result: with a second level it is tried there in the same ticket (`trigger: timeout`), without one it is `unavailable: timeout`.",
    'With a second level, a timeout after a `schema` miss on the same packet goes there at once, with no same-model retry; without one, that timeout is retried once on the same model, then the file is `unavailable: timeout`.',
    "The second level's own misses and timeouts (`second_level: true`) never count.",
    'Notes without `packet_hash` (written before B91), unsigned notes and `second_level: true` notes are read but never count.',
    "a signed `review.session_timeout` ledger note with the packet's `packet_hash`",
  ])
    assert.equal(occurrences(s4, flat(pin)), 1, pin);
  assertGone('review.md', review, B91_GONE);
});

test('B91 (7): how-it-works and review-only say a timeout is a broken result that reaches the ticket\'s second level', async () => {
  const howItWorks = await readFile(path.join(ROOT, 'docs', 'how-it-works.md'), 'utf8');
  for (const pin of [
    'A schema miss and a timed-out attempt are both broken results, counted together per packet, level and role: one miss plus one timeout is two.',
    "The ticket's second level is `review.second_levels.<level>`, or for a routed ticket the rule's `second` (an L2 rule's `second` does not apply in consensus mode).",
    '`review.schema_fallback` row (`trigger: schema` or `trigger: timeout`).',
    'Without a second level nothing is tried and no `review.schema_fallback` row is written.',
    'The try is refused when `budget.usd` is reached, never runs in consensus mode,',
    "The trigger is the packet's second broken result, whichever kind it is (a second timeout, or a timeout after a schema miss): with a second level, that level is tried in the same ticket, at once.",
    'Without a second level, a timed-out session is started once more on the same model, and when that one times out too the file is `unavailable: timeout`.',
    "Each timeout leaves a signed ledger note with a cleaned tail of its stderr, to explain the hang, and the packet's hash.",
    "Notes without that hash (written before this rule), unsigned notes and the second level's own notes are never counted.",
  ])
    assert.equal(occurrences(flat(howItWorks), flat(pin)), 1, pin);
  assertGone('how-it-works.md', howItWorks, B91_GONE);
  const reviewOnly = await readFile(path.join(ROOT, 'docs', 'review-only.md'), 'utf8');
  for (const pin of [
    'A timeout counts as a broken result, like an answer that fails the schema; the two are counted together for the same packet, level and role, so one of each is enough.',
    "At the second broken result, the file's second level is tried once in the same run: `review.second_levels.<level>`, or for a routed file the rule's `second` (an L2 rule's `second` does not apply in consensus mode, and consensus mode never tries).",
    'Without a second level, a second timeout makes the file `unavailable: timeout`, as does a timeout after a schema miss once its same-model retry times out too.',
    "Old timeout notes without the packet's hash, unsigned notes and the second level's own notes are read but never count.",
    '`budget.usd` refuses the second-level try like any session.',
    '`budget.usd` in `.code-forge.yml` caps it',
  ])
    assert.equal(occurrences(flat(reviewOnly), flat(pin)), 1, pin);
  assertGone('review-only.md', reviewOnly, B91_GONE);
});

// ── B92: a test that reads source text ──────────────────────────────────────

/** The sentence the plan names verbatim for code.md §3 and the coder brief. */
const B92_RULE = 'proof is behaviour, not text: never read a source file in a test to assert on its text';

test('B92 (5): code.md §3 and the coder-brief template carry the proof-is-behaviour rule once', async () => {
  const s3 = flat(section(await readSkillFile('references/code.md'), 3));
  assert.equal(occurrences(s3, flat(B92_RULE)), 1);
  const brief = await readSkillFile('templates/coder-brief.md');
  assert.equal(occurrences(flat(brief), flat(B92_RULE)), 1);
  // round 1: both say the section is a hint and when the reviewer raises a warning
  const HINT = 'The section is a hint, not a finding: the reviewer checks those lines and, when the test really reads source text to assert on it, raises a `warning` (unless the file read is a fixture).';
  assert.equal(occurrences(s3, flat(HINT)), 1);
  assert.equal(occurrences(flat(brief), flat(HINT)), 1);
});

test('B92 (6): review.md §3 and how-it-works say the packet has a `## test-quality` section and that it is a hint, not a finding', async () => {
  const s3 = flat(section(await readSkillFile('references/review.md'), 3));
  for (const pin of [
    'Test quality (B92): a test file that reads a source file and asserts on its text gets a `## test-quality` section before the diff',
    "never the file's text.",
    'The section is a hint, not a finding: it writes no row and changes no gate.',
    'raise such a test as a `warning` ("test reads source text; prove behaviour instead") unless the read file is a fixture.',
    'It is extra text counted in the packet budget, like the moved-code section; a file with no flag gets no section.',
    'The read calls that count are `readFileSync`, `readFile`, `open(`, `read_text`, `String(contentsOf`, `contentsOfFile`, `File.read`, `os.ReadFile`, `fs::read_to_string`, `readText(`, `readString(` and `ReadAllText(`, matched as text.',
    'Over budget it takes the path any extra takes: the context shrinks, then the digest is trimmed; when the diff plus the extras alone are over budget, the file is `split_required` (with `tokens_in` and `budget`, no session; a Markdown file is split by section instead).',
    'Rechecks get the lens rule but no `## test-quality` section.',
    "A fixture read never counts: a read whose span holds a string literal with a `fixtures`, `fixture` or `__fixtures__` path segment (`'test/fixtures/a.mjs'`, `join(dir, 'fixtures', 'a.mjs')`), or names a name bound on a line holding such a literal (`const FIXTURES = join(__dirname, 'fixtures')`).",
  ])
    assert.equal(occurrences(s3, flat(pin)), 1, pin);
  const howItWorks = flat(await readFile(path.join(ROOT, 'docs', 'how-it-works.md'), 'utf8'));
  for (const pin of [
    'its packet gets a `## test-quality` section before the diff',
    'It is a hint, not a finding: it comes from a text match, writes no ledger row and changes no gate.',
    'raise such a test as a `warning` unless the file it reads is a fixture.',
    'The section counts toward the packet budget; a file with no flag gets no section.',
    'Over the budget it is treated like any extra: the context shrinks, then the digest is trimmed, and when the diff plus the extras alone are over budget the file is `split_required`, with no session.',
    'Rechecks get the lens rule but no `## test-quality` section.',
  ])
    assert.equal(occurrences(howItWorks, flat(pin)), 1, pin);
});

test('B93 (7): subprocess.md §3 and security.md say what each coder.sandbox profile opens', async () => {
  const s3 = flat(section(await readSkillFile('references/adapters/subprocess.md'), 3));
  for (const pin of [
    'Only the Codex (openai) coder reads it; it has no effect on the Claude and Grok coders, whose argv does not change.',
    'with writes limited to the workspace where the CLI supports it, except under `coder.sandbox.profile: full`;',
    '`doctor` prints no `coder sandbox` row without the key, one INFO row with it, a WARN row for `full`, and a no-effect INFO row only when a Claude or Grok coder is configured.',
    '`workspace` (the default, and what no `coder` key means): `-s workspace-write`, today\'s argv. The project tree (`-C <cwd>`) is the only writable root;',
    '`toolchain`: the same, plus one `--add-dir <abs>` per `coder.sandbox.add_dirs` entry right after `-s workspace-write`',
    'The argv keeps the `$TMPDIR` and `/tmp` exclusion flags, which Codex ignores there.',
    // the containment rule and the symlink rule (fix round 1)
    'Each entry is absolute or starts with `~/`, which expands against HOME, and becomes an absolute realpath: symlinks are judged by their target, a dangling symlink too.',
    'refuse `/`, the home directory itself, and an entry that is, holds or sits inside any refused dir: `~/.code-forge`, `~/.codex` (and `$CODEX_HOME`), `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config`, `~/Library/Keychains`, the run temp root or the session `CODEX_HOME`.',
    'So `~/Library` is refused (it holds `~/Library/Keychains`), and so is `$TMPDIR` or `/tmp` when it holds the run temp root.',
    'the message names the key path (`coder.sandbox.add_dirs[<i>]`), never the value.',
    'Under `workspace` or `full` the entries are unused: `validate` warns `sandbox-dirs-unused`, and a refused entry is still refused.',
    '`full`: `-s danger-full-access`, which turns the Codex sandbox off: the coder writes anywhere you can, with network as the CLI allows. Like the Claude coder, the deny rules, the transcript grep and the gates are the only limits; `doctor` prints a WARN row.',
    '`run reload` refuses a change, and `forge spawn --role coder --run <r>` exits 1 `sandbox-changed` when the live value differs from the run-start snapshot. Without `--run`, spawn reads the live config',
    'A run recorded before config snapshots has no run-start value; spawn then uses the live config.',
  ])
    assert.equal(occurrences(s3, flat(pin)), 1, pin);
  const security = flat(await readSkillFile('references/security.md'));
  for (const pin of [
    "`workspace` (the default; no key gives today's argv) opens only the project tree for writes;",
    '`toolchain` also opens each checked `add_dirs` entry.',
    'An entry is refused (`sandbox-dir-refused`) when it is `/` or the home directory, or is, holds or sits inside `~/.code-forge`, `~/.codex`, `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config`, `~/Library/Keychains`, the run temp root or the session `CODEX_HOME`, so `~/Library` is refused too;',
    '`~/` expands against HOME, each entry becomes an absolute realpath, and a symlink is judged by its target; the message names the key path, never the value.',
    '`full` (`-s danger-full-access`) turns the Codex sandbox off: it opens every path the OS user can write, `~/.code-forge/runs/` included, with network as the CLI allows, so only the same-user rules above hold, like the Claude coder;',
    'the argv keeps the `$TMPDIR` and `/tmp` exclusion flags, which Codex ignores there.',
    'Under `workspace` or `full`, `add_dirs` is unused and `validate` warns `sandbox-dirs-unused`.',
    '`doctor` prints no `coder sandbox` row without the key, one INFO row with it, a WARN row for `full`, and a no-effect INFO row only when a Claude or Grok coder is configured.',
    'The key has no effect on the Claude and Grok coders.',
    'fixed at `run start`, refused by `run reload`, and a differing live value stops `forge spawn --role coder --run <r>` with `sandbox-changed`; without `--run` the live config is read.',
  ])
    assert.equal(occurrences(security, flat(pin)), 1, pin);
});

test('B93 (8): how-it-works and brief-mode say the coder sandbox behaviour; config.md lists the coder keys', async () => {
  const howItWorks = flat(await readFile(path.join(ROOT, 'docs', 'how-it-works.md'), 'utf8'));
  for (const pin of [
    "**The Codex coder's sandbox.** A Codex coder writes only in the project tree (`-s workspace-write`, with `$TMPDIR` and `/tmp` excluded). That is profile `workspace`, the default: no key gives today's argv.",
    '`coder.sandbox.profile: toolchain` adds one `--add-dir` per `coder.sandbox.add_dirs` entry, for a build cache or a package store.',
    'An entry is refused (`sandbox-dir-refused`) when it is `/` or your home directory, or is, holds or sits inside `~/.code-forge`, `~/.codex`, `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config`, `~/Library/Keychains`, the run\'s temp root or the session `CODEX_HOME`, so `~/Library` is refused too.',
    '`~/` expands against HOME, each entry becomes an absolute realpath, and a symlink is judged by its target.',
    '`profile: full` runs `-s danger-full-access`, which turns the Codex sandbox off: the coder writes anywhere you can, with network as the CLI allows,',
    'The argv keeps the `$TMPDIR` and `/tmp` exclusion flags, which Codex ignores there.',
    'Under `workspace` or `full`, `add_dirs` is unused and `validate` warns `sandbox-dirs-unused`.',
    '`doctor` prints no `coder sandbox` row without the key, one INFO row with it, a WARN row for `full`, and a no-effect INFO row only when a Claude or Grok coder is configured.',
    'The Claude and Grok coders do not change.',
    "`spawn --role coder --run <r>` stops with `sandbox-changed` when the live value differs from the run's snapshot. Without `--run`, spawn reads the live config.",
  ])
    assert.equal(occurrences(howItWorks, flat(pin)), 1, pin);
  const brief = flat(await readFile(path.join(ROOT, 'docs', 'brief-mode.md'), 'utf8'));
  for (const pin of [
    'A Codex coder (provider `openai`) writes only in the project tree unless you set `coder.sandbox`; the default profile is `workspace`, and no key gives today\'s argv.',
    '`~/` expands against HOME, each entry becomes an absolute realpath, and a symlink is judged by its target.',
    '`profile: full` (`-s danger-full-access`) turns the Codex sandbox off: the coder writes anywhere you can, with network as the CLI allows; the argv keeps the `$TMPDIR` and `/tmp` exclusion flags, which Codex ignores there.',
    'Under `workspace` or `full`, `add_dirs` is unused and `validate` warns `sandbox-dirs-unused`.',
    'or that is, holds or sits inside a secret directory such as `~/.ssh`, `~/.codex` or `~/Library/Keychains` (so `~/Library` too), the run\'s temp root or the session `CODEX_HOME` (`sandbox-dir-refused`).',
    '`coder.sandbox` is fixed for the run too',
    '`spawn --role coder --run <r>` stops with `sandbox-changed` when the live value differs; without `--run`, spawn reads the live config.',
    'The Claude and Grok coders ignore it.',
    '`doctor` prints no `coder sandbox` row without the key, one INFO row with it, a WARN row for `full`, and a no-effect INFO row only when a Claude or Grok coder is configured.',
  ])
    assert.equal(occurrences(brief, flat(pin)), 1, pin);
  const config = await readFile(path.join(ROOT, 'docs', 'reference', 'config.md'), 'utf8');
  for (const key of ['| `coder.sandbox.profile` |', '| `coder.sandbox.add_dirs` |']) assert.equal(occurrences(config, key), 1, key);
  for (const pin of [
    'symlinks are followed, even a dangling one, so a symlink is judged by its target.',
    'and any directory that is, holds or sits inside ~/.code-forge, ~/.codex or $CODEX_HOME, ~/.ssh, ~/.gnupg, ~/.aws, ~/.config, ~/Library/Keychains, the run temp root (so $TMPDIR or /tmp when it holds it), or the session CODEX_HOME; for example ~/Library is refused because it holds ~/Library/Keychains.',
    'under workspace or full they are unused and validate warns (rule sandbox-dirs-unused)',
  ])
    assert.equal(occurrences(config, pin), 1, pin);
});
