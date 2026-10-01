/**
 * B24: `init` fills gates and proof settings from the project and prints a "Project settings
 * (detected)" summary that marks every blank. Interactive mode then asks ONE choice — Use these /
 * Customize now (the eight pre-filled questions) / Leave for later; non-interactive mode asks
 * nothing; agent mode puts `settings` + `blank` in its JSON line. Flags still override; a re-run
 * keeps a hand-set gate and may fill a blank one.
 */

import { baseEnv, freshDir, makeProject, runWizard, scriptedUi } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { parse as parseYAML, stringify } from 'yaml';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';

/** Every remaining question, first line only, in order (no Solo, no multimodel, no key prompt). */
const QUESTIONS = [
  'Install the recommended tools, or use only the current harness?',
  'Install the skill into:',
  'Scope',
  'Method',
  'Default provider',
  'Keep the level matrix? L0=claude-haiku-4-5-20251001 L1=claude-sonnet-5 L2=claude-opus-5-5 L3=claude-fable-5-1',
  'Read model ids from the Codex/Grok CLI caches on this machine?',
  'Multimodel review (consensus)?',
  'Engine',
  'Gates and proof settings:',
];

/** Flags that answer every question but the settings choice. */
const ALL_OTHER_ANSWERS = [
  '--no-jev', '--tools', 'current', '--harness', 'claude', '-p', '--copy', '--provider', 'anthropic',
  '--level', 'L0=claude-haiku-4-5-20251001', '--refresh-models', '--multimodel', 'off', '--engine', 'auto',
];

/** The "Customize now" questions, first line, in order. */
const CUSTOMIZE = [
  'Gate test (blank or "none": no gate)',
  'Gate lint (blank or "none": no gate)',
  'Gate types (blank or "none": no gate)',
  'Gate format (blank or "none": no gate)',
  'High-risk paths (comma list of globs)',
  'Proof isolation',
  'Export: directories to link',
  'Export: untracked files to copy',
];

/** The hint under each "Customize now" question, typed out as the oracle. */
const CUSTOMIZE_HINTS = [
  'the command that runs your tests; the block cannot close if it fails (blank = no test gate)',
  'the command that runs your linter; the block cannot close if it fails (blank = no lint gate)',
  'the command that runs your type checker; the block cannot close if it fails (blank = no types gate)',
  'the command that runs your format check; the block cannot close if it fails (blank = no format gate)',
  'globs where mistakes are costly (auth, billing); these files get deeper reviews and need red→green proof to close (blank = none)',
  'export = run proof tests in a temporary copy (safe with parallel coders); lock = run in your folder and pause other coders',
  'big folders the copy links to instead of copying, e.g. node_modules, vendor',
  'files git does not track but your tests need, e.g. .env, .env.testing',
];

const REMOVED = ['Gate ', 'High-risk paths', 'Proof isolation', 'Export: directories to link', 'Export: untracked files to copy'];

const EDIT_LATER = 'settings: edit .code-forge.yml later: gates.types, proof.tiers.high.paths — then run `code-forge validate`';

const DETECTED_NO_TYPES = {
  gates: { test: ['npm', 'test'], lint: ['npm', 'run', 'lint'], types: null, format: ['npx', 'prettier', '--check', '.'] },
  proof: { tiers: { high: { paths: [] } }, isolation: 'export', export: { link_dirs: ['node_modules'], copy_untracked: ['.env'] } },
};

/** @param {string} dir - a Node project whose four gates are all detected (npm scripts). */
async function buildNodeAllGates(dir) {
  const scripts = { test: 'node --test', lint: 'eslint .', typecheck: 'tsc --noEmit', 'format:check': 'prettier --check .' };
  await writeFile(path.join(dir, 'package.json'), JSON.stringify({ name: 'all-gates', scripts }, null, 2));
}

/** @param {string} dir - a Node project with no type checker (no tsconfig, no typecheck script). */
async function buildNodeNoTypes(dir) {
  const pkg = { name: 'no-types', scripts: { test: 'node --test', lint: 'eslint .' }, devDependencies: { prettier: '^3.0.0' } };
  await writeFile(path.join(dir, 'package.json'), JSON.stringify(pkg, null, 2));
  // a fresh clone: node_modules is not installed, .env is there
  await writeFile(path.join(dir, '.env'), 'TOKEN=FAKE\n');
}

/** The summary printed for {@link buildNodeNoTypes}, typed out as the oracle. */
const NO_TYPES_SUMMARY = [
  'Project settings (detected)',
  '  test: npm test (from package.json)',
  '  lint: npm run lint (from package.json)',
  '  types: blank — no type checker found',
  '  format: npx prettier --check . (from package.json)',
  '  high-risk paths: blank — none set (all files light tier by path)',
  '  isolation: export (from default)',
  '  link dirs: node_modules (from package.json)',
  '  copy untracked: .env (found in the project)',
  'Set the blanks later: edit .code-forge.yml (gates.types, proof.tiers.high.paths), then run `code-forge validate`.',
].join('\n');

/** @param {string} text @param {string} needle @returns {number} */
function count(text, needle) {
  return text.split(needle).length - 1;
}

/** @param {{message: string}} c @returns {string} */
const firstLine = (c) => c.message.split('\n')[0];

/**
 * @param {(dir: string) => Promise<void>} build @param {string[]} args
 * @param {{ui?: any, isTTY?: boolean, agent?: boolean}} [opts]
 */
async function run(build, args, { ui, isTTY = true, agent = false } = {}) {
  const home = freshDir('home');
  const cwd = await makeProject(build);
  const env = baseEnv(home, agent ? { CLAUDECODE: '1' } : {});
  const r = await runWizard(args, { cwd, home, env, isTTY, ui });
  const file = path.join(cwd, '.code-forge.yml');
  return { ...r, home, cwd, env, file, read: () => parseYAML(readFileSync(file, 'utf8')) };
}

/** @param {string} stdout @returns {any} */
function oneJsonLine(stdout) {
  const lines = stdout.split('\n').filter((l) => l.length > 0);
  assert.equal(lines.length, 1, stdout);
  return JSON.parse(lines[0]);
}

test('all four gates detected: no gate/proof question by default, the summary names each gate and its source', async () => {
  const ui = scriptedUi();
  const r = await run(buildNodeAllGates, ['--no-jev'], { ui });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(ui.calls.map(firstLine), QUESTIONS);
  for (const prefix of REMOVED) assert.equal(ui.calls.filter((c) => c.message.startsWith(prefix)).length, 0, prefix);
  for (const line of [
    '  test: npm test (from package.json)',
    '  lint: npm run lint (from package.json)',
    '  types: npm run typecheck (from package.json)',
    '  format: npm run format:check (from package.json)',
  ]) assert.equal(count(r.stdout, `${line}\n`), 1, line);
  assert.equal(count(r.stdout, 'Project settings (detected)\n'), 1);
  // only proof values are blank (no high-risk paths, no .env here); no gate key is named
  assert.equal(count(r.stdout, '  link dirs: node_modules (from package.json)\n'), 1);
  assert.equal(count(r.stdout, 'Set the blanks later: edit .code-forge.yml (proof.tiers.high.paths, proof.export.copy_untracked), then run `code-forge validate`.\n'), 1);
  assert.deepEqual(r.read().gates, { test: ['npm', 'test'], lint: ['npm', 'run', 'lint'], types: ['npm', 'run', 'typecheck'], format: ['npm', 'run', 'format:check'] });
});

test('"Use these" (the default): exactly 1 question, the summary marks types blank, the detected values are written', async () => {
  const ui = scriptedUi();
  const r = await run(buildNodeNoTypes, ALL_OTHER_ANSWERS, { ui });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(ui.calls.length, 1);
  assert.equal(firstLine(ui.calls[0]), 'Gates and proof settings:');
  assert.equal(ui.calls[0].kind, 'select');
  assert.deepEqual(ui.calls[0].options.map((o) => [o.value, o.label]), [['use', 'Use these'], ['customize', 'Customize now'], ['later', 'Leave for later']]);
  assert.equal(ui.calls[0].initialValue, 'use');
  assert.equal(count(r.stdout, `${NO_TYPES_SUMMARY}\n`), 1);
  const written = r.read();
  assert.deepEqual({ gates: written.gates, proof: written.proof }, DETECTED_NO_TYPES);
  assert.equal(count(r.stdout, 'edit .code-forge.yml later:'), 0);
});

test('"Customize now" asks the 8 questions in order, pre-filled with the detected values; typed changes are written', async () => {
  const ui = scriptedUi({
    'Gates and proof settings:': 'customize',
    'Gate types': 'tsc --noEmit',
    'High-risk paths': 'app/{auth,billing}/**, src/money/**',
    'Proof isolation': 'lock',
  });
  const r = await run(buildNodeNoTypes, ALL_OTHER_ANSWERS, { ui });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(ui.calls.map(firstLine), ['Gates and proof settings:', ...CUSTOMIZE]);
  assert.deepEqual(ui.calls.slice(1).map((c) => c.initialValue), ['npm test', 'npm run lint', '', 'npx prettier --check .', '', 'export', 'node_modules', '.env']);
  const written = r.read();
  assert.deepEqual(written.gates, { ...DETECTED_NO_TYPES.gates, types: ['tsc', '--noEmit'] });
  assert.deepEqual(written.proof, { tiers: { high: { paths: ['app/{auth,billing}/**', 'src/money/**'] } }, isolation: 'lock', export: { link_dirs: ['node_modules'], copy_untracked: ['.env'] } });
});

test('"Leave for later" writes the detected values and prints the edit-later line once', async () => {
  const ui = scriptedUi({ 'Gates and proof settings:': 'later' });
  const r = await run(buildNodeNoTypes, ALL_OTHER_ANSWERS, { ui });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(ui.calls.length, 1);
  assert.equal(count(r.stdout, `${EDIT_LATER}\n`), 1);
  const written = r.read();
  assert.deepEqual({ gates: written.gates, proof: written.proof }, DETECTED_NO_TYPES);
});

test('each "Customize now" question carries its exact hint line', async () => {
  const ui = scriptedUi({ 'Gates and proof settings:': 'customize' });
  const r = await run(buildNodeNoTypes, ALL_OTHER_ANSWERS, { ui });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(ui.calls.slice(1).map((c) => c.message), CUSTOMIZE.map((q, i) => `${q}\n${CUSTOMIZE_HINTS[i]}`));
});

test('a flag-set value is not asked under "Customize now"', async () => {
  const ui = scriptedUi({ 'Gates and proof settings:': 'customize' });
  const r = await run(buildNodeNoTypes, [...ALL_OTHER_ANSWERS, '--gate', 'lint=none', '--proof', 'isolation=lock'], { ui });
  assert.equal(r.code, 0, r.stderr);
  assert.deepEqual(ui.calls.map(firstLine), ['Gates and proof settings:', ...CUSTOMIZE.filter((q) => !q.startsWith('Gate lint') && q !== 'Proof isolation')]);
  assert.equal(r.read().gates.lint, null);
  assert.equal(r.read().proof.isolation, 'lock');
});

test('--no-interaction at a terminal asks 0 questions and still prints the summary', async () => {
  const ui = scriptedUi();
  const r = await run(buildNodeNoTypes, ['--no-interaction', '--no-jev'], { ui });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(ui.calls.length, 0);
  assert.equal(count(r.stdout, `${NO_TYPES_SUMMARY}\n`), 1);
});

test('agent mode: exactly 1 JSON line with settings (value + source per key) and the blank list', async () => {
  const r = await run(buildNodeNoTypes, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(r.code, 0, r.stderr);
  const json = oneJsonLine(r.stdout);
  assert.deepEqual(json.settings, {
    'gates.test': { value: ['npm', 'test'], source: 'detected', note: 'package.json' },
    'gates.lint': { value: ['npm', 'run', 'lint'], source: 'detected', note: 'package.json' },
    'gates.types': { value: null, source: 'detected', note: 'no type checker found' },
    'gates.format': { value: ['npx', 'prettier', '--check', '.'], source: 'detected', note: 'package.json' },
    'proof.tiers.high.paths': { value: [], source: 'default', note: 'none set (all files light tier by path)' },
    'proof.isolation': { value: 'export', source: 'default', note: 'default' },
    'proof.export.link_dirs': { value: ['node_modules'], source: 'detected', note: 'package.json' },
    'proof.export.copy_untracked': { value: ['.env'], source: 'detected', note: 'found in the project' },
  });
  assert.deepEqual(json.blank, ['gates.types', 'proof.tiers.high.paths']);
});

test('Laravel: a missing .env.testing is not proposed; once it exists it is', async () => {
  const withEnvOnly = async (/** @type {string} */ dir) => {
    await buildLaravelVue(dir);
    await writeFile(path.join(dir, '.env'), 'APP_KEY=FAKE\n');
  };
  const one = await run(withEnvOnly, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(one.code, 0, one.stderr);
  assert.deepEqual(one.read().proof.export.copy_untracked, ['.env']);
  const withBoth = async (/** @type {string} */ dir) => {
    await withEnvOnly(dir);
    await writeFile(path.join(dir, '.env.testing'), 'APP_KEY=FAKE\n');
  };
  const two = await run(withBoth, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(two.code, 0, two.stderr);
  assert.deepEqual(two.read().proof.export.copy_untracked, ['.env', '.env.testing']);
});

test('--gate and --proof override the detected values, and the summary says so', async () => {
  const args = [
    '--no-interaction', '--no-jev',
    '--gate', 'types=tsc --noEmit', '--gate', 'lint=none',
    '--proof', 'isolation=lock', '--proof', 'high=src/billing/**', '--proof', 'copy_untracked=.env.ci',
  ];
  const r = await run(buildNodeNoTypes, args, { isTTY: false, agent: true });
  assert.equal(r.code, 0, r.stderr);
  const written = r.read();
  assert.deepEqual(written.gates, { test: ['npm', 'test'], lint: null, types: ['tsc', '--noEmit'], format: ['npx', 'prettier', '--check', '.'] });
  assert.deepEqual(written.proof, { tiers: { high: { paths: ['src/billing/**'] } }, isolation: 'lock', export: { link_dirs: ['node_modules'], copy_untracked: ['.env.ci'] } });
  const json = oneJsonLine(r.stdout);
  assert.deepEqual(json.settings['gates.types'], { value: ['tsc', '--noEmit'], source: 'flag', note: '--gate' });
  assert.deepEqual(json.settings['gates.lint'], { value: null, source: 'flag', note: 'set blank by --gate' });
  assert.deepEqual(json.settings['proof.isolation'], { value: 'lock', source: 'flag', note: '--proof' });
  assert.deepEqual(json.blank, ['gates.lint']);
});

test('re-run: a hand-edited gates.lint stays; a blank gate the project now shows is filled', async () => {
  const first = await run(buildNodeNoTypes, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(first.code, 0, first.stderr);
  const cfg = first.read();
  cfg.gates.lint = ['custom-lint', '--strict'];
  writeFileSync(first.file, stringify(cfg));
  writeFileSync(path.join(first.cwd, 'tsconfig.json'), '{}\n');
  const again = await runWizard(['--no-interaction', '--no-jev'], { cwd: first.cwd, home: first.home, env: first.env });
  assert.equal(again.code, 0, again.stderr);
  const written = parseYAML(readFileSync(first.file, 'utf8'));
  assert.deepEqual(written.gates.lint, ['custom-lint', '--strict']);
  assert.deepEqual(written.gates.types, ['npx', 'tsc', '--noEmit']);
  const json = oneJsonLine(again.stdout);
  assert.deepEqual(json.settings['gates.lint'], { value: ['custom-lint', '--strict'], source: 'existing', note: '.code-forge.yml' });
  assert.deepEqual(json.settings['gates.types'], { value: ['npx', 'tsc', '--noEmit'], source: 'detected', note: 'tsconfig.json' });
});

test('the 1Password option and its follow-up question carry the exact item-ID hint', async () => {
  const hint = "Paste the item ID (from `op item list`), the item's link, or its op:// secret reference.";
  const ui = scriptedUi({ 'Where is the Jev key?': 'op', '1Password item ID': 'op://Dev/jev/credential' });
  const r = await run(buildNodeAllGates, [], { ui });
  assert.equal(r.code, 0, r.stderr);
  const where = ui.calls.filter((c) => firstLine(c) === 'Where is the Jev key?');
  assert.equal(where.length, 1);
  assert.deepEqual(where[0].options.filter((o) => o.value === 'op').map((o) => o.hint), [hint]);
  const ref = ui.calls.filter((c) => firstLine(c) === '1Password item ID, item link, or op:// reference');
  assert.equal(ref.length, 1);
  assert.equal(ref[0].message, `1Password item ID, item link, or op:// reference\n${hint}`);
  assert.equal(r.read().keys.jev, 'op://Dev/jev/credential');
});

test('every question has exactly one non-empty hint line', async () => {
  const ui = scriptedUi({ 'Multimodel review (consensus)?': true, 'Keep the level matrix?': false, 'Gates and proof settings:': 'customize' });
  const r = await run(buildNodeNoTypes, ['--no-jev'], { ui });
  assert.equal(r.code, 0, r.stderr);
  // 9 questions + 4 levels + second provider + judge + the settings choice + 8 customize questions
  assert.equal(ui.calls.length, 24, ui.calls.map(firstLine).join(" | "));
  for (const c of ui.calls) {
    const parts = c.message.split('\n');
    assert.equal(parts.length, 2, c.message);
    assert.match(parts[1], /^\S.{9,}$/, c.message);
  }
});

test('re-run keeps gates keys outside the four gates (gates.extra.*, gates.full_suite_threshold_files) with their exact values', async () => {
  const first = await run(buildNodeNoTypes, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(first.code, 0, first.stderr);
  const cfg = first.read();
  cfg.gates.extra = { secret_scan: false };
  cfg.gates.full_suite_threshold_files = 25;
  writeFileSync(first.file, stringify(cfg));
  const { gatherContext, resolveAnswers } = await import('../../src/install/wizard/answers.mjs');
  const ctx = await gatherContext({ cwd: first.cwd, home: first.home, env: first.env });
  const { values } = resolveAnswers(ctx, cfg, null, []);
  assert.deepEqual(/** @type {any} */ (values.gates).extra, { secret_scan: false });
  assert.equal(/** @type {any} */ (values.gates).full_suite_threshold_files, 25);
  const again = await runWizard(['--no-interaction', '--no-jev'], { cwd: first.cwd, home: first.home, env: first.env });
  assert.equal(again.code, 0, again.stderr);
  const written = parseYAML(readFileSync(first.file, 'utf8'));
  assert.deepEqual(written.gates.extra, { secret_scan: false });
  assert.equal(written.gates.full_suite_threshold_files, 25);
  assert.deepEqual(Object.keys(written.gates), ['test', 'lint', 'types', 'format', 'extra', 'full_suite_threshold_files']);
});

test('link_dirs follows the manifest, installed or not: composer.json + package.json ⇒ [vendor, node_modules]; folders without a manifest ⇒ []', async () => {
  const fresh = async (/** @type {string} */ dir) => {
    await writeFile(path.join(dir, 'composer.json'), '{}');
    await writeFile(path.join(dir, 'package.json'), JSON.stringify({ name: 'fresh-clone' }));
  };
  const installed = async (/** @type {string} */ dir) => {
    await fresh(dir);
    mkdirSync(path.join(dir, 'vendor'));
    mkdirSync(path.join(dir, 'node_modules'));
  };
  for (const build of [fresh, installed]) {
    const r = await run(build, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(r.read().proof.export.link_dirs, ['vendor', 'node_modules']);
    assert.deepEqual(oneJsonLine(r.stdout).settings['proof.export.link_dirs'], { value: ['vendor', 'node_modules'], source: 'detected', note: 'composer.json, package.json' });
  }
  // folders named like candidates, without any manifest, are not proposed
  const none = await run(async (dir) => {
    await writeFile(path.join(dir, 'README.md'), 'x\n');
    mkdirSync(path.join(dir, 'node_modules'));
    mkdirSync(path.join(dir, 'vendor'));
  }, ['--no-interaction', '--no-jev'], { isTTY: true });
  assert.equal(none.code, 0, none.stderr);
  assert.deepEqual(none.read().proof.export.link_dirs, []);
  assert.equal(count(none.stdout, '  link dirs: blank — no dependency manifest found\n'), 1);
  assert.equal(count(none.stdout, '  link dirs:'), 1);
});

/** @type {Array<[string, Record<string, string>, string[], string]>} */
const MANIFEST_CASES = [
  ['Go (go.mod)', { 'go.mod': 'module example.test/x\n\ngo 1.22\n' }, ['vendor'], 'go.mod'],
  ['Python (requirements.txt only)', { 'requirements.txt': 'pytest\n' }, ['.venv'], 'requirements.txt'],
];
for (const [label, files, expected, note] of MANIFEST_CASES) {
  test(`link_dirs for ${label} is ${JSON.stringify(expected)}`, async () => {
    const r = await run(async (dir) => {
      for (const [f, text] of Object.entries(files)) await writeFile(path.join(dir, f), text);
    }, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
    assert.equal(r.code, 0, r.stderr);
    assert.deepEqual(r.read().proof.export.link_dirs, expected);
    assert.deepEqual(oneJsonLine(r.stdout).settings['proof.export.link_dirs'], { value: expected, source: 'detected', note });
  });
}

test('without a cwd the link_dirs and copy_untracked origins are "default"; with one they are "detected"', async () => {
  const home = freshDir('home');
  const cwd = await makeProject(buildNodeNoTypes);
  const { gatherContext, resolveAnswers } = await import('../../src/install/wizard/answers.mjs');
  const ctx = await gatherContext({ cwd, home, env: baseEnv(home) });
  const withCwd = resolveAnswers(ctx, null, null, []);
  assert.deepEqual([withCwd.origins.proof.link_dirs, withCwd.origins.proof.copy_untracked], ['detected', 'detected']);
  assert.deepEqual([withCwd.values.proof.link_dirs, withCwd.values.proof.copy_untracked], [['node_modules'], ['.env']]);
  const noCwd = resolveAnswers(/** @type {any} */ ({ ...ctx, cwd: undefined }), null, null, []);
  assert.deepEqual([noCwd.origins.proof.link_dirs, noCwd.origins.proof.copy_untracked], ['default', 'default']);
  assert.deepEqual([noCwd.values.proof.link_dirs, noCwd.values.proof.copy_untracked], [[], ['.env', '.env.test']]);
});

test('PHP gate evidence is named from the tool: artisan, phpstan.neon, pint.json; or vendor/bin/pest, phpstan.neon.dist, vendor/bin/pint', async () => {
  const laravel = await run(buildLaravelVue, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(laravel.code, 0, laravel.stderr);
  const notes = (/** @type {any} */ json) => ['gates.test', 'gates.types', 'gates.format'].map((k) => json.settings[k].note);
  assert.deepEqual(notes(oneJsonLine(laravel.stdout)), ['artisan', 'phpstan.neon', 'pint.json']);
  const pest = await run(async (dir) => {
    await writeFile(path.join(dir, 'composer.json'), '{}');
    mkdirSync(path.join(dir, 'vendor', 'bin'), { recursive: true });
    await writeFile(path.join(dir, 'vendor', 'bin', 'pest'), '#!/usr/bin/env php\n');
    await writeFile(path.join(dir, 'vendor', 'bin', 'pint'), '#!/usr/bin/env php\n');
    await writeFile(path.join(dir, 'phpstan.neon.dist'), 'parameters:\n');
  }, ['--no-interaction', '--no-jev'], { isTTY: false, agent: true });
  assert.equal(pest.code, 0, pest.stderr);
  assert.deepEqual(notes(oneJsonLine(pest.stdout)), ['vendor/bin/pest', 'phpstan.neon.dist', 'vendor/bin/pint']);
});

test('a blank gate kept from .code-forge.yml says "empty in .code-forge.yml"', async () => {
  const { summarizeSettings } = await import('../../src/install/wizard/summary.mjs');
  const cwd = freshDir('empty');
  const values = /** @type {any} */ ({
    gates: { test: ['npm', 'test'], lint: [], types: null, format: null },
    proof: { high: null, isolation: 'export', link_dirs: '', copy_untracked: undefined },
  });
  const origins = /** @type {any} */ ({
    gates: { test: 'existing', lint: 'existing', types: 'detected', format: 'flag' },
    proof: { high: 'existing', isolation: 'default', link_dirs: 'detected', copy_untracked: 'detected' },
  });
  const out = summarizeSettings(values, origins, { cwd, profile: 'node', detected: { stack: 'node' } });
  assert.deepEqual(out.lines.slice(1, -1), [
    '  test: npm test (from .code-forge.yml)',
    '  lint: blank — empty in .code-forge.yml',
    '  types: blank — no type checker found',
    '  format: blank — set blank by --gate',
    '  high-risk paths: blank — empty in .code-forge.yml',
    '  isolation: export (from default)',
    '  link dirs: blank — no dependency manifest found',
    '  copy untracked: blank — none found (looked for .env, .env.test)',
  ]);
  assert.deepEqual(out.blank, ['gates.lint', 'gates.types', 'gates.format', 'proof.tiers.high.paths', 'proof.export.link_dirs', 'proof.export.copy_untracked']);
});
