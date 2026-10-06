import { freshDir, sink } from './helpers.mjs';
import assert from 'node:assert/strict';
import { copyFileSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { runPlanVerb } = await import('../../src/cli/plan.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runJev } = await import('../../src/cli/jev.mjs');
const { loadProjectConfig, projectRootFor, slugFor } = await import('../../src/config/load.mjs');
const { checkPlan, splitClauses } = await import('../../src/session/plan-check.mjs');

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
  return checkVariants([[from, to]], extra);
}

/**
 * {@link checkVariant} with several replacements, applied in order (each `from` occurs once).
 * @param {Array<[string, string]>} pairs @param {string[]} [extra]
 */
async function checkVariants(pairs, extra = ['--slug', 'lanes'], files = /** @type {Record<string, string>} */ ({})) {
  let text = GOOD_TEXT;
  for (const [from, to] of pairs) {
    assert.equal(text.split(from).length, 2, `fixture text must occur once: ${from}`);
    text = text.replace(from, to);
  }
  const dir = freshDir('variant');
  mkdirSync(path.join(dir, 'plans'));
  mkdirSync(path.join(dir, 'briefs'));
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.md'), path.join(dir, 'briefs', 'tool-brief.md'));
  writeFileSync(path.join(dir, 'plans', 'x.plan.md'), text);
  for (const [name, content] of Object.entries(files)) writeFileSync(path.join(dir, name), content);
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

const LEDGER_ROW = '2. `~/.code-forge/ledger` is UNVERIFIABLE (the delegate may not read `.code-forge` paths) — B1; tolerance: B1 asserts the path under a temp HOME.';
const OLD_TABLE_HEAD = "| block | clause | claim | tag | tolerance (how the block's acceptance survives it) |\n|---|---|---|---|---|";
const OLD_TABLE_B1 = '| B1 | rows land under the ledger | `~/.code-forge/ledger` | UNVERIFIABLE | B1 asserts the path under a temp HOME |';

test('B50 the template list form (`- B2 `--flag` (F3, NOT-FOUND) — tolerance: …`) passes plan check', async () => {
  const r = await checkVariant(TOLERANCE_ROW, '- B2 `--max-turns` (F3, NOT-FOUND) — tolerance: B2 asserts against the help fixture, never a live CLI.');
  assert.deepEqual([r.code, r.errors], [0, []]);
});

test('B50 the older table form still passes when each tolerance cell is filled; an empty tolerance cell is refused for its block only', async () => {
  const filled = `${OLD_TABLE_HEAD}\n| B2 | the argv never carries it | \`--max-turns\` | NOT-FOUND | B2 asserts against the help fixture |\n${OLD_TABLE_B1}`;
  const ok = await checkVariant(`${TOLERANCE_ROW}\n${LEDGER_ROW}`, filled);
  assert.deepEqual([ok.code, ok.errors], [0, []]);
  const empty = `${OLD_TABLE_HEAD}\n| B2 | the argv never carries it | \`--max-turns\` | NOT-FOUND | — |\n${OLD_TABLE_B1}`;
  const bad = await checkVariant(`${TOLERANCE_ROW}\n${LEDGER_ROW}`, empty);
  assert.deepEqual([bad.code, bad.errors], [1, ['unbackable clause without tolerance: B2 the reviewer argv never carries `--max-turns` (1)']]);
});

test('B50 clauses split on ` · (n) ` markers and on `;`: a `(1) … · (2) …` cell is 2 clauses and only clause (2) is reported', async () => {
  assert.deepEqual(splitClauses('(1) an empty brief is refused · (2) the argv never carries `--max-turns`'), ['(1) an empty brief is refused', '(2) the argv never carries `--max-turns`']);
  assert.deepEqual(splitClauses('a · b; c'), ['a · b', 'c']);
  const r = await checkVariants([
    ['the reviewer argv never carries `--max-turns` (1); an empty brief is refused (1)', '(1) an empty brief is refused (`test/b2.test.mjs`) · (2) the reviewer argv never carries `--max-turns`'],
    [TOLERANCE_ROW, 'none'],
  ]);
  assert.deepEqual([r.code, r.errors], [1, ['unbackable clause without tolerance: B2 (2) the reviewer argv never carries `--max-turns`']]);
});

test('B50 tolerance tables: only a table\'s FIRST row can be its header; 7 placeholder cells count as empty', async () => {
  const head = (/** @type {string} */ cell) => `${OLD_TABLE_HEAD}\n| B2 | the argv never carries it | \`--max-turns\` | NOT-FOUND | ${cell} |\n${OLD_TABLE_B1}`;
  const placeholders = ['', '–', '-', '--', '...', 'n/a', 'N/A'];
  const results = [];
  for (const cell of placeholders) {
    const r = await checkVariant(`${TOLERANCE_ROW}\n${LEDGER_ROW}`, head(cell));
    results.push([r.code, r.errors.length]);
  }
  assert.deepEqual(results, placeholders.map(() => [1, 1]));
  // a table without a tolerance header: a data row saying "tolerance" never becomes the header
  const noHeader = `| block | note |\n|---|---|\n| B9 | tolerance |\n| B2 | \`--max-turns\` asserted on the fixture |\n\n${OLD_TABLE_HEAD}\n${OLD_TABLE_B1}`;
  const r = await checkVariant(`${TOLERANCE_ROW}\n${LEDGER_ROW}`, noHeader);
  assert.deepEqual([r.code, r.errors], [1, ['unbackable clause without tolerance: B2 the reviewer argv never carries `--max-turns` (1)']]);
});

test('B50 a clause citing `claude plugin` is backed by a VERIFIED `code-forge claude plugin` only when code-forge is the project\'s own bin (1 ok, 3 refused)', async () => {
  const pkg = { 'package.json': JSON.stringify({ name: 'x', bin: { 'code-forge': 'bin/cf.mjs' } }) };
  const err = ['unbackable clause without tolerance: B3 the report prints the `claude plugin` status (1)'];
  const ok = await checkVariants([['"claim":"claude plugin"', '"claim":"code-forge claude plugin"']], ['--slug', 'lanes'], pkg);
  assert.deepEqual([ok.code, ok.errors], [0, []]);
  const noBin = await checkVariant('"claim":"claude plugin"', '"claim":"code-forge claude plugin"');
  const otherWord = await checkVariants([['"claim":"claude plugin"', '"claim":"npx claude plugin"']], ['--slug', 'lanes'], pkg);
  const twoWords = await checkVariants([['"claim":"claude plugin"', '"claim":"npx code-forge claude plugin"']], ['--slug', 'lanes'], pkg);
  assert.deepEqual([noBin, otherWord, twoWords].map((r) => [r.code, r.errors]), [[1, err], [1, err], [1, err]]);
});

test('B50 a root config\'s project.slug wins from a subfolder: jev ask writes to it and plan check (no --slug) reads it', async () => {
  const project = freshDir('slugged');
  mkdirSync(path.join(project, '.git'));
  writeFileSync(path.join(project, '.code-forge.yml'), `${readFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), 'utf8')}project:\n  slug: root-named\n`);
  const sub = path.join(project, 'plans');
  mkdirSync(sub);
  mkdirSync(path.join(project, 'briefs'));
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.md'), path.join(project, 'briefs', 'tool-brief.md'));
  writeFileSync(path.join(sub, 'x.plan.md'), GOOD_TEXT);
  const base = { filesChanged: 0, linesAdded: 0, touchesMigration: false, touchesPolicyOrMiddleware: false, pathFloorHit: false, keywordsFound: [] };
  writeFileSync(path.join(sub, 'l1.json'), JSON.stringify({ ...base, filesChanged: 5 }));
  writeFileSync(path.join(sub, 'l2.json'), JSON.stringify({ ...base, touchesMigration: true }));
  for (const [block, state] of [['B1', 'l1.json'], ['B2', 'l2.json'], ['B3', 'l1.json']]) {
    const err = sink();
    assert.equal(await runJev(['ask', 'lane', '--state', state, '--cwd', sub, '--block', block, '--rules'], { stdout: sink(), stderr: err, env: {} }), 0, err.text());
  }
  assert.deepEqual([(await readAllRows('root-named')).length, (await readAllRows(path.basename(project))).length, (await readAllRows('plans')).length], [3, 0, 0]);
  const stdout = sink();
  assert.equal(await runPlanVerb(['check', 'x.plan.md'], { stdout, stderr: sink(), cwd: sub }), 0);
  assert.equal(stdout.text(), 'plan check: ok (3 blocks)\nlanes: B1 L1 (rules) · B2 L2 (rules) · B3 L1 (rules)\n');
});

test('B50 jev ask --rules run from a subfolder writes to the project ledger; plan check from the root (no --slug) finds those 3 lanes', async () => {
  const project = freshDir('my-proj');
  mkdirSync(path.join(project, '.git')); // a git top level: outside HOME, the search never walks up without one
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(project, '.code-forge.yml'));
  const sub = path.join(project, 'plans', 'feature');
  mkdirSync(sub, { recursive: true });
  mkdirSync(path.join(project, 'briefs'));
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.md'), path.join(project, 'briefs', 'tool-brief.md'));
  writeFileSync(path.join(project, 'plans', 'x.plan.md'), GOOD_TEXT);
  const base = { filesChanged: 0, linesAdded: 0, touchesMigration: false, touchesPolicyOrMiddleware: false, pathFloorHit: false, keywordsFound: [] };
  writeFileSync(path.join(sub, 'l1.json'), JSON.stringify({ ...base, filesChanged: 5 }));
  writeFileSync(path.join(sub, 'l2.json'), JSON.stringify({ ...base, touchesMigration: true }));
  for (const [block, state] of [['B1', 'l1.json'], ['B2', 'l2.json'], ['B3', 'l1.json']]) {
    const err = sink();
    assert.equal(await runJev(['ask', 'lane', '--state', state, '--cwd', sub, '--block', block, '--rules'], { stdout: sink(), stderr: err, env: {} }), 0, err.text());
  }
  const slug = path.basename(project);
  assert.equal((await readAllRows(slug)).length, 3);
  assert.deepEqual(await readAllRows('feature'), []);
  const stdout = sink();
  const stderr = sink();
  assert.equal(await runPlanVerb(['check', 'plans/x.plan.md'], { stdout, stderr, cwd: project }), 0, stderr.text());
  assert.equal(stdout.text(), 'plan check: ok (3 blocks)\nlanes: B1 L1 (rules) · B2 L2 (rules) · B3 L1 (rules)\n');
  // and from the subfolder too: the same project, the same ledger
  const fromSub = sink();
  assert.equal(await runPlanVerb(['check', '../x.plan.md'], { stdout: fromSub, stderr: sink(), cwd: sub }), 0);
  assert.equal(fromSub.text(), stdout.text());
});

test('B50 projectRootFor: a config above the git root and a .code-forge.yml directory are ignored; outside git the search stops at HOME', () => {
  const outer = freshDir('outer');
  writeFileSync(path.join(outer, '.code-forge.yml'), 'version: 1\n');
  const repo = path.join(outer, 'repo');
  mkdirSync(path.join(repo, '.git'), { recursive: true });
  const asDir = path.join(repo, 'pkg', '.code-forge.yml');
  mkdirSync(asDir, { recursive: true });
  const inPkg = path.join(repo, 'pkg', 'src');
  mkdirSync(inPkg);
  const home = path.join(freshDir('home-parent'), 'me');
  writeFileSync(path.join(path.dirname(home), '.code-forge.yml'), 'version: 1\n');
  const inHome = path.join(home, 'a', 'b');
  mkdirSync(inHome, { recursive: true });
  const before = process.env.HOME;
  process.env.HOME = home;
  try {
    assert.deepEqual([projectRootFor(path.join(repo, 'pkg')), projectRootFor(inPkg), projectRootFor(inHome)], [repo, repo, inHome]);
    writeFileSync(path.join(home, '.code-forge.yml'), 'version: 1\n');
    assert.equal(projectRootFor(inHome), home);
  } finally {
    process.env.HOME = before;
  }
});

test('B50 a package.json that is not JSON is read only when a claim needs the bin prefix: unused ⇒ exit 0; needed ⇒ `plan: …` on stderr, exit 2, nothing on stdout', async () => {
  const bad = { 'package.json': '{ "bin": ' };
  const unused = await checkVariants([], ['--slug', 'lanes'], bad);
  assert.deepEqual([unused.code, unused.errors, unused.out], [0, [], `plan check: ok (3 blocks)\n${LANES_LINE}`]);
  const needed = await checkVariants([['"claim":"claude plugin"', '"claim":"code-forge claude plugin"']], ['--slug', 'lanes'], bad);
  assert.deepEqual([needed.code, needed.errors, needed.out], [2, ['plan: package.json at the project root is not valid JSON'], '']);
});

test('B50 projectRootFor: a symlinked .code-forge.yml counts (dotfiles; stat follows the link), a .code-forge.yml directory does not; a HOME given through a symlink still stops the walk (realpaths)', () => {
  const repo = freshDir('linked');
  mkdirSync(path.join(repo, '.git'));
  const real = path.join(freshDir('real-config'), 'real.yml');
  writeFileSync(real, 'version: 1\n');
  const pkg = path.join(repo, 'pkg');
  mkdirSync(path.join(pkg, 'src'), { recursive: true });
  symlinkSync(real, path.join(pkg, '.code-forge.yml'));
  const other = path.join(repo, 'other');
  mkdirSync(path.join(other, '.code-forge.yml'), { recursive: true });
  assert.deepEqual([projectRootFor(path.join(pkg, 'src')), projectRootFor(other)], [pkg, repo]);
  const homeReal = path.join(freshDir('home-real'), 'me');
  mkdirSync(path.join(homeReal, 'a'), { recursive: true });
  writeFileSync(path.join(path.dirname(homeReal), '.code-forge.yml'), 'version: 1\n'); // above HOME: never counts
  const homeLink = path.join(freshDir('home-link'), 'me-link');
  symlinkSync(homeReal, homeLink);
  const before = process.env.HOME;
  process.env.HOME = homeLink;
  try {
    assert.equal(projectRootFor(path.join(homeReal, 'a')), path.join(homeReal, 'a'));
    writeFileSync(path.join(homeReal, '.code-forge.yml'), 'version: 1\n');
    assert.equal(projectRootFor(path.join(homeReal, 'a')), homeReal);
  } finally {
    process.env.HOME = before;
  }
});

test('B50 projectRootFor resolves the start once: a symlink into a repo finds its .git and gives the same root and slug as the real folder; a config above the repo never counts', () => {
  const outer = freshDir('real-outer');
  writeFileSync(path.join(outer, '.code-forge.yml'), 'version: 1\n'); // above the repo
  const repo = path.join(outer, 'Repo X');
  mkdirSync(path.join(repo, '.git'), { recursive: true });
  const deep = path.join(repo, 'src', 'cli');
  mkdirSync(deep, { recursive: true });
  const link = path.join(freshDir('link-parent'), 'shortcut');
  symlinkSync(deep, link);
  assert.deepEqual([projectRootFor(link), projectRootFor(deep), slugFor({}, link), slugFor({}, deep)], [repo, repo, 'repo-x', 'repo-x']);
});

test('B50 a symlinked .code-forge.yml at the root loads, and its project.slug is the ledger slug from a subfolder; a directory there stays a read-error', async () => {
  const root = freshDir('cfg-link');
  mkdirSync(path.join(root, '.git'));
  const target = path.join(freshDir('dotfiles'), 'code-forge.yml');
  writeFileSync(target, `${readFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), 'utf8')}project:\n  slug: dotfiles-slug\n`);
  symlinkSync(target, path.join(root, '.code-forge.yml'));
  const sub = path.join(root, 'a', 'b');
  mkdirSync(sub, { recursive: true });
  const found = projectRootFor(sub);
  const loaded = await loadProjectConfig(found);
  assert.deepEqual([found, loaded.ok, slugFor(loaded.ok ? loaded.config : {}, found)], [root, true, 'dotfiles-slug']);
  const asDir = freshDir('cfg-dir');
  mkdirSync(path.join(asDir, '.code-forge.yml'));
  const dirLoad = await loadProjectConfig(asDir);
  assert.deepEqual([dirLoad.ok, dirLoad.error], [false, 'read-error']);
});

test('B50 loadProjectConfig(<repo>/sub) reads the ROOT config (the root rule is enforced inside it; the same result from the root)', async () => {
  const repo = freshDir('load-root');
  mkdirSync(path.join(repo, '.git'));
  writeFileSync(path.join(repo, '.code-forge.yml'), `${readFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), 'utf8')}project:\n  slug: loaded-root\n`);
  const sub = path.join(repo, 'sub', 'deeper');
  mkdirSync(sub, { recursive: true });
  const fromSub = await loadProjectConfig(sub);
  const fromRoot = await loadProjectConfig(repo);
  assert.deepEqual([fromSub.ok, fromSub.path, fromSub.ok ? fromSub.config?.project?.slug : null], [true, path.join(repo, '.code-forge.yml'), 'loaded-root']);
  assert.deepEqual(fromSub, fromRoot);
});

test('B50 a .git at HOME itself (a dotfiles repo) does not make HOME the root: outside git, the start is the root unless a config sits between it and HOME', () => {
  const home = freshDir('dotfiles-home');
  mkdirSync(path.join(home, '.git'));
  const start = path.join(home, 'code', 'app');
  mkdirSync(start, { recursive: true });
  const before = process.env.HOME;
  process.env.HOME = home;
  try {
    assert.deepEqual([projectRootFor(start), slugFor({}, start)], [start, 'app']);
    writeFileSync(path.join(home, 'code', '.code-forge.yml'), 'version: 1\n');
    assert.equal(projectRootFor(start), path.join(home, 'code'));
  } finally {
    process.env.HOME = before;
  }
});

test('B50 projectRootFor answers in the caller\'s spelling: <work>/app -> <data>/app-v2 keeps the root <work>/app and slug `app`; the real path gives `app-v2`', () => {
  const real = path.join(freshDir('data'), 'app-v2');
  mkdirSync(path.join(real, '.git'), { recursive: true });
  mkdirSync(path.join(real, 'src'));
  const app = path.join(freshDir('work'), 'app');
  symlinkSync(real, app);
  assert.deepEqual(
    [projectRootFor(path.join(app, 'src')), slugFor({}, path.join(app, 'src')), projectRootFor(path.join(real, 'src')), slugFor({}, path.join(real, 'src'))],
    [app, 'app', real, 'app-v2'],
  );
});

test('B50 projectRootFor outside git and NOT under HOME looks at the start only: a config one level up is ignored, one at the start counts; slugFor follows', () => {
  const top = freshDir('not-home');
  writeFileSync(path.join(top, '.code-forge.yml'), 'version: 1\n');
  const start = path.join(top, 'work', 'Sub Dir');
  mkdirSync(start, { recursive: true });
  const before = process.env.HOME;
  process.env.HOME = freshDir('elsewhere-home');
  try {
    assert.deepEqual([projectRootFor(start), slugFor({}, start), projectRootFor(top), slugFor({}, top)], [start, 'sub-dir', top, path.basename(top)]);
    // a made-up path that does not exist: no surprise reads, the slug is its own name
    assert.deepEqual([projectRootFor('/made/up/ws-one'), slugFor({}, '/made/up/ws-one')], ['/made/up/ws-one', 'ws-one']);
  } finally {
    process.env.HOME = before;
  }
});

test('B50 projectRootFor: the nearest .code-forge.yml, else the nearest .git (dir or file), else the start; slugFor names the root', () => {
  const top = freshDir('Root Proj');
  mkdirSync(path.join(top, '.git'));
  const deep = path.join(top, 'a', 'b');
  mkdirSync(deep, { recursive: true });
  const configured = path.join(top, 'pkg');
  mkdirSync(path.join(configured, 'src'), { recursive: true });
  writeFileSync(path.join(configured, '.code-forge.yml'), 'version: 1\n');
  const worktree = freshDir('wt');
  writeFileSync(path.join(worktree, '.git'), 'gitdir: /nowhere\n');
  mkdirSync(path.join(worktree, 'docs'));
  const bare = freshDir('bare');
  assert.deepEqual(
    [projectRootFor(deep), projectRootFor(path.join(configured, 'src')), projectRootFor(path.join(worktree, 'docs')), projectRootFor(bare)],
    [top, configured, worktree, bare],
  );
  assert.deepEqual(
    [slugFor({}, deep), slugFor({}, path.join(configured, 'src')), slugFor({ project: { slug: 'named' } }, deep)],
    [path.basename(top).toLowerCase().replace(' ', '-'), path.basename(configured), 'named'],
  );
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
