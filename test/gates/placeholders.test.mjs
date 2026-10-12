import { commitAll, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { copyFileSync, readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

// B60 (issue #13 item 3): a gate argv item that is exactly `{test_files}` runs the block's related
// tests (`block open --tests`, else its owned files that look like tests), or is dropped for the
// full suite (over `gates.full_suite_threshold_files`, or no test file known); a gate may be
// `{argv, env}`. These tests pin the child's argv and env, the rows, exit codes and messages.

const { DEFAULT_FULL_SUITE_THRESHOLD, MASK_MIN_LENGTH, TEST_FILES_TOKEN, envValues, expandArgv, hasTestFilesItem, isTestPath, maskableEnvValues, printableGates, scopeFields, testScope, thresholdOf } = await import('../../src/gates/expand.mjs');
const { runGates } = await import('../../src/gates/run.mjs');
const { countOwnedFiles, ownedFileCount } = await import('../../src/gates/owned-count.mjs');
const { runGatesInExport } = await import('../../src/proof/export.mjs');
const { runGatesVerb } = await import('../../src/cli/gates.mjs');
const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun } = await import('../../src/state/run.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');
const { runGatesGuarded } = await import('../../src/proof/lock.mjs');
const { assertOwned } = await import('../../src/state/registry.mjs');
const { buildBinnacle, buildFullLog } = await import('../../src/autopilot/binnacle.mjs');

const NODE = process.execPath;
const PROBE_PID = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
/** An env value the tests look for everywhere it must NOT appear. */
const VALUE = 'plain-b60-gate-value-q7';

/** @returns {{write: (s: string) => boolean, text: string}} */
function capture() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

/** @param {Function} fn @param {string[]} args @returns {Promise<[number, string, string]>} */
async function cli(fn, args) {
  const stdout = capture();
  const stderr = capture();
  const code = await fn(args, { stdout, stderr, probe: PROBE_PID });
  return [code, stdout.text, stderr.text];
}

/**
 * A probe gate: writes what it was given (its argv after the script, the env value, whether PATH
 * was inherited) to `<dir>/seen.json`, echoes the env value to stdout and stderr, and exits 3 when
 * one of its file arguments names a `red` file. `seen()` reads the file and deletes it, so each
 * check sees only the run since the last one.
 * @returns {{script: string, seen: () => {args: string[], value: string | null, inherited: boolean}}}
 */
function probe() {
  const dir = freshDir('probe');
  const out = path.join(dir, 'seen.json');
  const script = path.join(dir, 'probe.mjs');
  writeFile(dir, 'probe.mjs', [
    "import { writeFileSync } from 'node:fs';",
    'const args = process.argv.slice(2);',
    'const value = process.env.B60_GATE_VALUE ?? null;',
    `writeFileSync(${JSON.stringify(out)}, JSON.stringify({ args, value, inherited: typeof process.env.PATH === 'string' }));`,
    "process.stdout.write(`value=${value}\\n`);",
    "process.stderr.write(`value=${value}\\n`);",
    "process.exit(args.some((a) => a.includes('red')) ? 3 : 0);",
    '',
  ].join('\n'));
  return {
    script,
    seen: () => {
      const got = JSON.parse(readFileSync(out, 'utf8'));
      rmSync(out);
      return got;
    },
  };
}

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');

/** @param {Record<string, any>} row */
const strip = ({ mac: _m, ts: _t, ...rest }) => rest;
/** What every gate row also carries (the ledger writer's usage sources). */
const SRC = { tokens_source: null, cost_source: null };

/**
 * A committed repo whose `.code-forge.yml` sets `gates` (YAML lines, indented under `gates:`), a
 * started run, and helpers to open blocks and run the gates through the CLI.
 * @param {string} slug @param {string[]} gateLines
 */
async function fixture(slug, gateLines) {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', [
    'version: 1',
    'project:',
    `  slug: ${slug}`,
    'provider: anthropic',
    'engine: harness',
    'levels:',
    '  L0: {model: claude-haiku-4-5-20251001}',
    '  L1: {model: claude-sonnet-5}',
    '  L2: {model: claude-opus-5-5}',
    '  L3: {model: claude-fable-5-1}',
    'gates:',
    ...gateLines.map((l) => `  ${l}`),
    '',
  ].join('\n'));
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  await commitAll(repo);
  const runId = `r-${slug}`;
  const [started, , startErr] = await cli(runRun, ['start', '--cwd', repo, '--run', runId, '--worker-pid', '4242']);
  assert.equal(started, 0, startErr);
  /** @param {string} id @param {string[]} more */
  const open = (id, more) => cli(runBlock, ['open', id, '--run', runId, '--level', 'L2', '--acceptance', ACC, ...more]);
  /** @param {string} id */
  const gate = async (id) => {
    const [code, out, err] = await cli(runGatesVerb, ['run', '--cwd', repo, '--slug', slug, '--run', runId, '--block', id]);
    return { code, out, err, json: out ? JSON.parse(out) : null };
  };
  /** `gates run` without --run/--block (no block to scope to) */
  const gateNoBlock = async () => {
    const [code, out, err] = await cli(runGatesVerb, ['run', '--cwd', repo, '--slug', slug]);
    return { code, out, err, json: out ? JSON.parse(out) : null };
  };
  const gateRows = async () => (await readAllRows(slug)).filter((r) => r.event.startsWith('gate.')).map(strip);
  return { repo, runId, open, gate, gateNoBlock, gateRows };
}

/** YAML for a `test` gate on the probe with the token, the other three gates off. */
const tokenGate = (/** @type {string} */ script, /** @type {string} */ extra = '') => [
  `test: {argv: [${JSON.stringify(NODE)}, ${JSON.stringify(script)}, "--run", "{test_files}"]${extra}}`,
  'lint: null',
  'types: null',
  'format: null',
];

// ── the predicate, the scope, the expansion ─────────────────────────────────

test('B60 (1): isTestPath — a test/tests/__tests__/spec directory, or a .test./.spec./Test.php/_test.go/test_*.py name', () => {
  const yes = ['test/a.mjs', 'pkg/tests/x.py', 'src/__tests__/a.js', 'spec/models/user_spec.rb', 'src/a.test.mjs', 'src/a.spec.ts', 'tests/Unit/UserTest.php', 'app/UserTest.php', 'pkg/a_test.go', 'test_api.py', 'a/test/**'];
  const no = ['src/a.mjs', 'src/testing/a.mjs', 'src/contest.mjs', 'src/a.test', 'docs/spec.md', 'src/latest/a.js', 'Tester.php', 'src/test_api.js', ''];
  assert.deepEqual(yes.filter((p) => !isTestPath(p)), []);
  assert.deepEqual(no.filter((p) => isTestPath(p)), []);
});

test('B60 (1)(2): testScope — explicit test files win, else owned test-looking entries; over the threshold or none known ⇒ full with its reason', () => {
  assert.equal(DEFAULT_FULL_SUITE_THRESHOLD, 10);
  assert.deepEqual(testScope({ testFiles: ['t/x.mjs'], owned: ['src/a.mjs', 'test/a.test.mjs'] }), { scope: 'related', files: ['t/x.mjs'] });
  assert.deepEqual(testScope({ owned: ['src/a.mjs', 'test/a.test.mjs'] }), { scope: 'related', files: ['test/a.test.mjs'] });
  assert.deepEqual(testScope({ testFiles: null, owned: ['src/a.mjs'] }), { scope: 'full', reason: 'no_tests' });
  assert.deepEqual(testScope({ testFiles: ['t/x.mjs'], owned: ['a', 'b', 'c'], threshold: 2 }), { scope: 'full', reason: 'threshold' });
  assert.deepEqual(testScope({ testFiles: ['t/x.mjs'], owned: ['a', 'b'], threshold: 2 }), { scope: 'related', files: ['t/x.mjs'] });
  const eleven = Array.from({ length: 11 }, (_, i) => `test/t${i}.test.mjs`);
  assert.deepEqual(testScope({ owned: eleven }), { scope: 'full', reason: 'threshold' });
  assert.deepEqual(testScope({ owned: eleven.slice(0, 10) }), { scope: 'related', files: eleven.slice(0, 10) });
});

test('B60 (1)(2): expandArgv — only a WHOLE {test_files} item expands (or is dropped); a substring or a gate without it is untouched', () => {
  const related = /** @type {import('../../src/gates/expand.mjs').TestScope} */ ({ scope: 'related', files: ['test/a.test.mjs', 'test/b.test.mjs'] });
  const full = /** @type {import('../../src/gates/expand.mjs').TestScope} */ ({ scope: 'full', reason: 'threshold' });
  assert.equal(TEST_FILES_TOKEN, '{test_files}');
  assert.deepEqual(expandArgv(['npm', 'test', '--', '{test_files}'], related), { argv: ['npm', 'test', '--', 'test/a.test.mjs', 'test/b.test.mjs'], scoped: true });
  assert.deepEqual(expandArgv(['npm', 'test', '--', '{test_files}'], full), { argv: ['npm', 'test', '--'], scoped: true });
  assert.deepEqual(expandArgv(['run', '--files={test_files}', 'x{test_files}'], related), { argv: ['run', '--files={test_files}', 'x{test_files}'], scoped: false });
  const plain = ['npm', 'test'];
  const kept = expandArgv(plain, related);
  assert.notEqual(kept.argv, plain);
  assert.deepEqual(kept, { argv: ['npm', 'test'], scoped: false });
  kept.argv.push('x');
  assert.deepEqual(plain, ['npm', 'test']);
  assert.deepEqual(scopeFields(related), { scope: 'related', test_count: 2 });
  assert.deepEqual(scopeFields(full), { scope: 'full', reason: 'threshold' });
});

test('B60 (1): the owned-file fallback takes only concrete test files — a glob or a directory entry is never passed to the gate', () => {
  assert.deepEqual(testScope({ owned: ['test/**', 'tests/', 'spec/*.rb', 'src/a.mjs', 'test/a.test.mjs'] }), { scope: 'related', files: ['test/a.test.mjs'] });
  assert.deepEqual(testScope({ owned: ['test/**', 'src/**'] }), { scope: 'full', reason: 'no_tests' });
});

test('B60 (2): thresholdOf — the configured value only when it is an integer >= 0, else the default', () => {
  assert.deepEqual([undefined, null, -1, 2.5, '3', NaN].map(thresholdOf), [10, 10, 10, 10, 10, 10]);
  assert.deepEqual([0, 3, 10, 25].map(thresholdOf), [0, 3, 10, 25]);
});

test('B60 (3): envValues covers every gate key and keeps only non-empty strings; only values of 8+ characters are masked; printableGates names keys only', () => {
  const gates = {
    test: { argv: ['npm', 'test'], env: { MODE: 'test', TOKEN: 'long-value-1234', EMPTY: '', NUM: /** @type {any} */ (5) } },
    e2e: { argv: ['npx', 'pw'], env: { BASE: 'https://example.invalid' } },
    lint: ['npm', 'run', 'lint'],
    types: null,
    full_suite_threshold_files: 4,
  };
  assert.equal(MASK_MIN_LENGTH, 8);
  assert.deepEqual(envValues(gates).sort(), ['https://example.invalid', 'long-value-1234', 'test']);
  assert.deepEqual(maskableEnvValues(gates).sort(), ['https://example.invalid', 'long-value-1234']);
  assert.deepEqual(printableGates({ test: gates.test, lint: gates.lint, none: { argv: ['x'], env: {} }, types: null }), {
    test: { argv: ['npm', 'test'], env_keys: ['EMPTY', 'MODE', 'NUM', 'TOKEN'] },
    lint: ['npm', 'run', 'lint'],
    none: { argv: ['x'] },
    types: null,
  });
});

// ── runGates: the child's exact argv and env ────────────────────────────────

test('B60 (1)(3): runGates passes the expanded argv and the gate env (added to the inherited env) to the child; the result names env keys, never values', async () => {
  const p = probe();
  const dir = freshDir('rungates');
  const { results, allOk } = await runGates(
    { test: { argv: [NODE, p.script, '--run', '{test_files}'], env: { B60_GATE_VALUE: VALUE } }, lint: null, types: null, format: null },
    { cwd: dir, scope: { scope: 'related', files: ['test/a.test.mjs', 'test/b.test.mjs'] } },
  );
  assert.equal(allOk, true);
  assert.deepEqual(p.seen(), { args: ['--run', 'test/a.test.mjs', 'test/b.test.mjs'], value: VALUE, inherited: true });
  const { stdout: _o, stderr: _e, ...rest } = results[0];
  assert.deepEqual(rest, { gate: 'test', argv: [NODE, p.script, '--run', 'test/a.test.mjs', 'test/b.test.mjs'], skipped: false, ok: true, code: 0, scope: 'related', test_count: 2, env_keys: ['B60_GATE_VALUE'] });
});

test('B60 (3): a {argv, env: {}} gate is a gate with no env — no env_keys key on the result', async () => {
  const p = probe();
  const { results } = await runGates({ test: { argv: [NODE, p.script, 'x'], env: {} }, lint: null, types: null, format: null }, { cwd: freshDir('emptyenv') });
  assert.deepEqual(p.seen(), { args: ['x'], value: null, inherited: true });
  assert.deepEqual(Object.keys(results[0]).sort(), ['argv', 'code', 'gate', 'ok', 'skipped', 'stderr', 'stdout']);
});

test('B60 (2): runGates with no scope (no block) drops the item — the full suite, reason no_tests', async () => {
  const p = probe();
  const { results } = await runGates({ test: [NODE, p.script, '--run', '{test_files}'], lint: null, types: null, format: null }, { cwd: freshDir('noscope') });
  assert.deepEqual(p.seen(), { args: ['--run'], value: null, inherited: true });
  assert.deepEqual([results[0].argv, results[0].scope, results[0].reason, results[0].test_count], [[NODE, p.script, '--run'], 'full', 'no_tests', undefined]);
});

test('B60 (4): a gate without the token and without env runs exactly as before — same argv, no scope or env_keys keys, env not set', async () => {
  const p = probe();
  const { results } = await runGates({ test: [NODE, p.script, 'x'], lint: null, types: null, format: null }, { cwd: freshDir('plain'), scope: { scope: 'related', files: ['test/a.test.mjs'] } });
  assert.deepEqual(p.seen(), { args: ['x'], value: null, inherited: true });
  assert.deepEqual(Object.keys(results[0]).sort(), ['argv', 'code', 'gate', 'ok', 'skipped', 'stderr', 'stdout']);
});

test('B60 (1)(4): runGatesInExport runs the token gate on the scope it is given, inside the export dir; without a scope, full', async () => {
  const p = probe();
  const exportDir = freshDir('export');
  const mainTree = freshDir('main');
  const gates = { test: [NODE, p.script, '{test_files}'], lint: null, types: null, format: null };
  const scoped = await runGatesInExport({ gates, exportDir, mainTree, scope: { scope: 'related', files: ['tests/one_test.go'] } });
  assert.deepEqual([p.seen().args, scoped.results[0].scope, scoped.results[0].test_count], [['tests/one_test.go'], 'related', 1]);
  const plain = await runGatesInExport({ gates, exportDir, mainTree });
  assert.deepEqual([p.seen().args, plain.results[0].scope, plain.results[0].reason], [[], 'full', 'no_tests']);
  // a plain gate (no token, no env) inside the export: the exact argv, no scope or env_keys keys
  const untouched = await runGatesInExport({ gates: { test: [NODE, p.script, 'all'], lint: null, types: null, format: null }, exportDir, mainTree, scope: { scope: 'related', files: ['tests/one_test.go'] } });
  assert.deepEqual(p.seen().args, ['all']);
  assert.deepEqual(untouched.results[0].argv, [NODE, p.script, 'all']);
  assert.deepEqual([Object.hasOwn(untouched.results[0], 'scope'), Object.hasOwn(untouched.results[0], 'env_keys')], [false, false]);
});

// ── the CLI end to end: block open --tests, gates run --run --block, the rows ──

test('B60 (1)(2): block open --tests records test_files on the block and the dispatch row; the gate runs exactly those, gate.done says related with the count', async () => {
  const p = probe();
  const fx = await fixture('b60-related', tokenGate(p.script));
  const [code, out] = await fx.open('B1', ['--owned', 'src/a.mjs', 'test/a.test.mjs', '--tests', 'test/a.test.mjs', 'test/b.test.mjs']);
  assert.equal(code, 0, out);
  assert.match(out, /^block B1 open · L2 · code · attempt 1 · base [0-9a-f]{12} · 2 test files\n/);
  assert.deepEqual((await readRun(fx.runId)).blocks.B1.test_files, ['test/a.test.mjs', 'test/b.test.mjs']);
  const dispatch = (await readAllRows('b60-related')).find((r) => r.event === 'dispatch');
  assert.deepEqual(dispatch?.test_files, ['test/a.test.mjs', 'test/b.test.mjs']);

  const ran = await fx.gate('B1');
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(p.seen().args, ['--run', 'test/a.test.mjs', 'test/b.test.mjs']);
  assert.deepEqual([ran.json.results[0].scope, ran.json.results[0].test_count], ['related', 2]);
  assert.deepEqual(await fx.gateRows(), [{ event: 'gate.done', run: fx.runId, block: 'B1', stack: 'unknown', all_ok: true, scope: 'related', test_count: 2, ...SRC }]);
});

test('B60 (1): with no --tests the gate takes the owned files that look like tests; one file prints "1 test file" only when --tests was given', async () => {
  const p = probe();
  const fx = await fixture('b60-owned', tokenGate(p.script));
  const [code, out] = await fx.open('B1', ['--owned', 'src/a.mjs', 'test/a.test.mjs', 'src/__tests__/b.js']);
  assert.equal(code, 0, out);
  assert.match(out, /^block B1 open · L2 · code · attempt 1 · base [0-9a-f]{12}\n/);
  assert.equal(Object.hasOwn((await readRun(fx.runId)).blocks.B1, 'test_files'), false);
  assert.equal(Object.hasOwn((await readAllRows('b60-owned')).find((r) => r.event === 'dispatch') ?? {}, 'test_files'), false);
  const ran = await fx.gate('B1');
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(p.seen().args, ['--run', 'test/a.test.mjs', 'src/__tests__/b.js']);

  const [code2, out2] = await fx.open('B2', ['--owned', 'lib/**', '--tests', 'test/lib.test.mjs']);
  assert.equal(code2, 0, out2);
  assert.match(out2, / · 1 test file\n/);
});

test('B60 (2): countOwnedFiles — a glob counts its matches, a directory every file below it, a file once, a path not there yet 1; each file once', () => {
  const tracked = [...Array.from({ length: 11 }, (_, i) => `src/m${i}.mjs`), 'lib/feature/a.mjs', 'lib/feature/b.mjs', 'lib/feature/sub/c.mjs', 'lib/featureX/d.mjs', 'README.md'];
  assert.equal(countOwnedFiles(['src/**'], tracked), 11);
  assert.equal(countOwnedFiles(['lib/feature'], tracked), 3);
  assert.equal(countOwnedFiles(['README.md'], tracked), 1);
  assert.equal(countOwnedFiles(['src/new.mjs'], tracked), 1);
  assert.equal(countOwnedFiles(['lib/feature', 'lib/feature/a.mjs', 'lib/**/*.mjs'], tracked), 4);
  assert.equal(countOwnedFiles(['lib/feature', 'docs/new.md', 'src/m0.mjs'], tracked), 5);
  assert.equal(countOwnedFiles(['a', 'b'], []), 2);
  // the count, not the entries, meets the threshold
  assert.deepEqual(testScope({ testFiles: ['t/x.test.mjs'], owned: ['src/**'], ownedFiles: 11, threshold: 10 }), { scope: 'full', reason: 'threshold' });
  assert.deepEqual(testScope({ testFiles: ['t/x.test.mjs'], owned: ['src/**'], ownedFiles: 10, threshold: 10 }), { scope: 'related', files: ['t/x.test.mjs'] });
});

test('B60 (2): ownedFileCount reads the tracked files of the workspace (git ls-files); outside a repo every entry counts 1', async () => {
  const repo = await makeRepo();
  for (let i = 0; i < 3; i += 1) writeFile(repo, `lib/feature/f${i}.mjs`, 'export {};\n');
  writeFile(repo, 'lib/other.mjs', 'export {};\n');
  await commitAll(repo);
  assert.equal(await ownedFileCount(repo, ['lib/feature']), 3);
  // lib/** covers the 4 committed files; lib/feature/new.mjs is not on disk, so that entry counts 1
  assert.equal(await ownedFileCount(repo, ['lib/**', 'lib/feature/new.mjs']), 5);
  assert.equal(await ownedFileCount(freshDir('not-a-repo'), ['lib/feature', 'x.mjs']), 2);
});

test('B60 (2): end to end, the threshold counts the files an entry covers — src/** over 11 tracked files is full (threshold), a directory over 3 is related, a new file counts 1', async () => {
  const p = probe();
  const fx = await fixture('b60-files', tokenGate(p.script));
  for (let i = 0; i < 10; i += 1) writeFile(fx.repo, `src/m${i}.mjs`, 'export {};\n');
  for (let i = 0; i < 3; i += 1) writeFile(fx.repo, `lib/feature/f${i}.mjs`, 'export {};\n');
  await commitAll(fx.repo); // src/ now holds 11 tracked files (a.mjs + m0..m9)

  assert.equal((await fx.open('B1', ['--owned', 'src/**', '--tests', 'test/a.test.mjs']))[0], 0);
  assert.equal((await fx.gate('B1')).code, 0);
  assert.deepEqual(p.seen().args, ['--run']);

  assert.equal((await fx.open('B2', ['--owned', 'lib/feature', '--tests', 'test/f.test.mjs']))[0], 0);
  assert.equal((await fx.gate('B2')).code, 0);
  assert.deepEqual(p.seen().args, ['--run', 'test/f.test.mjs']);

  // 10 new paths not created yet: 10 files, at the threshold ⇒ related
  const fresh = Array.from({ length: 10 }, (_, i) => `pkg/n${i}.mjs`);
  assert.equal((await fx.open('B3', ['--owned', ...fresh, '--tests', 'test/n.test.mjs']))[0], 0);
  assert.equal((await fx.gate('B3')).code, 0);
  assert.deepEqual(p.seen().args, ['--run', 'test/n.test.mjs']);

  assert.deepEqual((await fx.gateRows()).map((r) => [r.block, r.scope, r.reason ?? r.test_count]), [['B1', 'full', 'threshold'], ['B2', 'related', 1], ['B3', 'related', 1]]);
});

test('B60 (2): over gates.full_suite_threshold_files owned files the item is dropped — full suite; a red gate\'s gate.red row and gate.done both say threshold', async () => {
  const p = probe();
  const fx = await fixture('b60-threshold', [...tokenGate(p.script), 'full_suite_threshold_files: 1']);
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs', 'src/b.mjs', '--tests', 'test/red.test.mjs']))[0], 0);
  const ran = await fx.gate('B1');
  assert.deepEqual(p.seen().args, ['--run']);
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(await fx.gateRows(), [{ event: 'gate.done', run: fx.runId, block: 'B1', stack: 'unknown', all_ok: true, scope: 'full', reason: 'threshold', ...SRC }]);

  // at the threshold (1 owned entry) the related red test runs: gate.red carries the scope too
  assert.equal((await fx.open('B2', ['--owned', 'lib/**', '--tests', 'test/red.test.mjs']))[0], 0);
  const red = await fx.gate('B2');
  assert.equal(red.code, 1);
  assert.deepEqual(p.seen().args, ['--run', 'test/red.test.mjs']);
  assert.deepEqual((await fx.gateRows()).slice(1), [
    { event: 'gate.red', run: fx.runId, block: 'B2', gate: 'test', code: 3, scope: 'related', test_count: 1, ...SRC },
    { event: 'gate.done', run: fx.runId, block: 'B2', stack: 'unknown', all_ok: false, scope: 'related', test_count: 1, ...SRC },
  ]);
});

test('B60 (2): no test file known (no --tests, no test-looking owned entry) ⇒ the full suite, reason no_tests', async () => {
  const p = probe();
  const fx = await fixture('b60-notests', tokenGate(p.script));
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs']))[0], 0);
  const ran = await fx.gate('B1');
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(p.seen().args, ['--run']);
  assert.deepEqual([ran.json.results[0].scope, ran.json.results[0].reason], ['full', 'no_tests']);
  assert.deepEqual(await fx.gateRows(), [{ event: 'gate.done', run: fx.runId, block: 'B1', stack: 'unknown', all_ok: true, scope: 'full', reason: 'no_tests', ...SRC }]);
});

test('B60 (2): gates run without --run/--block has no block to scope to — the full suite, gate.done says full / no_tests and names no run or block', async () => {
  const p = probe();
  const fx = await fixture('b60-noblock', tokenGate(p.script));
  const ran = await fx.gateNoBlock();
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(p.seen().args, ['--run']);
  assert.deepEqual([ran.json.results[0].scope, ran.json.results[0].reason], ['full', 'no_tests']);
  assert.deepEqual(await fx.gateRows(), [{ event: 'gate.done', stack: 'unknown', all_ok: true, scope: 'full', reason: 'no_tests', ...SRC }]);
});

test('B60 (3): a {argv, env} gate runs with the variable set; the value never reaches stdout, stderr, the rows — the key is named', async () => {
  const p = probe();
  const fx = await fixture('b60-env', tokenGate(p.script, `, env: {B60_GATE_VALUE: ${VALUE}}`));
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'test/red.test.mjs']))[0], 0);
  const ran = await fx.gate('B1');
  assert.equal(ran.code, 1);
  assert.equal(p.seen().value, VALUE);
  assert.equal(ran.out.includes(VALUE) || ran.err.includes(VALUE), false);
  assert.deepEqual(ran.json.results[0].env_keys, ['B60_GATE_VALUE']);
  assert.equal(ran.json.results[0].stdout, 'value=[REDACTED]\n');
  assert.equal(ran.json.results[0].stderr, 'value=[REDACTED]\n');
  const rows = await readAllRows('b60-env');
  assert.equal(JSON.stringify(rows).includes(VALUE), false);
  assert.deepEqual(await fx.gateRows(), [
    { event: 'gate.red', run: fx.runId, block: 'B1', gate: 'test', code: 3, scope: 'related', test_count: 1, ...SRC },
    { event: 'gate.done', run: fx.runId, block: 'B1', stack: 'unknown', all_ok: false, scope: 'related', test_count: 1, ...SRC },
  ]);

  const [code, out] = await cli(runGatesVerb, ['detect', '--cwd', fx.repo]);
  assert.equal(code, 0);
  assert.equal(out.includes(VALUE), false);
  assert.deepEqual(JSON.parse(out).test, { argv: [NODE, p.script, '--run', '{test_files}'], env_keys: ['B60_GATE_VALUE'] });
});

test('B60 (3): the gates usage text states the env rule in the approved words', async () => {
  assert.deepEqual(await cli(runGatesVerb, ['nope']), [2, '', [
    'usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]\n',
    "  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.\n",
    '  gates run: a gate may be {argv, env}. code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.\n',
  ].join('')]);
});

test('B60 (3): a SHORT env value (MODE=test) is not masked — `npm test` in the argv, the output and the rows stay as they are; the value is never recorded', async () => {
  const p = probe();
  const fx = await fixture('b60-short', [`test: {argv: [${JSON.stringify(NODE)}, ${JSON.stringify(p.script)}, "npm", "test"], env: {B60_GATE_VALUE: test}}`, 'lint: null', 'types: null', 'format: null']);
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs']))[0], 0);
  const ran = await fx.gate('B1');
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(p.seen(), { args: ['npm', 'test'], value: 'test', inherited: true });
  assert.equal(ran.out.includes('[REDACTED]'), false);
  assert.deepEqual(ran.json.results[0].argv, [NODE, p.script, 'npm', 'test']);
  assert.deepEqual([ran.json.results[0].stdout, ran.json.results[0].stderr, ran.json.results[0].env_keys], ['value=test\n', 'value=test\n', ['B60_GATE_VALUE']]);
  const rows = JSON.stringify(await readAllRows('b60-short'));
  assert.equal(rows.includes('[REDACTED]') || rows.includes('"B60_GATE_VALUE":"test"'), false);
  assert.deepEqual(await fx.gateRows(), [{ event: 'gate.done', run: fx.runId, block: 'B1', stack: 'unknown', all_ok: true, ...SRC }]);
  const [, detected] = await cli(runGatesVerb, ['detect', '--cwd', fx.repo]);
  assert.deepEqual(JSON.parse(detected).test, { argv: [NODE, p.script, 'npm', 'test'], env_keys: ['B60_GATE_VALUE'] });
});

test('B60 (4): with --run --block a plain gate (no token, no env) runs as today: same argv, gate.done without scope fields', async () => {
  const p = probe();
  const fx = await fixture('b60-plain', [`test: [${JSON.stringify(NODE)}, ${JSON.stringify(p.script)}, "all"]`, 'lint: null', 'types: null', 'format: null']);
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'test/a.test.mjs']))[0], 0);
  const ran = await fx.gate('B1');
  assert.equal(ran.code, 0, ran.err);
  assert.deepEqual(p.seen(), { args: ['all'], value: null, inherited: true });
  assert.deepEqual(Object.keys(ran.json.results[0]).sort(), ['argv', 'code', 'gate', 'ok', 'skipped', 'stderr', 'stdout']);
  assert.deepEqual(await fx.gateRows(), [{ event: 'gate.done', run: fx.runId, block: 'B1', stack: 'unknown', all_ok: true, ...SRC }]);
});

test('B60 (1): runGatesGuarded on a block the run does not have — StateError no-block naming the block and the run, no gate run', async () => {
  const p = probe();
  const fx = await fixture('b60-noblk', tokenGate(p.script));
  await assert.rejects(
    runGatesGuarded({ runId: fx.runId, blockId: 'B9', gates: { test: [NODE, p.script, '{test_files}'], lint: null, types: null, format: null }, cwd: fx.repo, timeoutMs: undefined, threshold: undefined }),
    (err) => {
      assert.equal(/** @type {any} */ (err).code, 'no-block');
      assert.match(/** @type {Error} */ (err).message, /B9/);
      assert.match(/** @type {Error} */ (err).message, new RegExp(fx.runId));
      return true;
    },
  );
  assert.throws(() => p.seen(), { code: 'ENOENT' });
});

test('B60 (1): block open --tests a b — exit 0, test_files exactly on the record and the dispatch row; "1 test file" / "2 test files"; no --tests ⇒ no key and no suffix', async () => {
  const fx = await fixture('b60-tests-line', ['test: null']);
  const [c2, o2, e2] = await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'a', 'b']);
  assert.deepEqual([c2, e2], [0, '']);
  assert.match(o2, /^block B1 open · L2 · code · attempt 1 · base [0-9a-f]{12} · 2 test files\nbrief: none given \(--brief <file>\)\n$/);
  const [c1, o1] = await fx.open('B2', ['--owned', 'src/b.mjs', '--tests', 'a']);
  assert.equal(c1, 0);
  assert.match(o1, /^block B2 open · L2 · code · attempt 1 · base [0-9a-f]{12} · 1 test file\nbrief: none given \(--brief <file>\)\n$/);
  const [c0, o0] = await fx.open('B3', ['--owned', 'src/c.mjs']);
  assert.equal(c0, 0);
  assert.match(o0, /^block B3 open · L2 · code · attempt 1 · base [0-9a-f]{12}\nbrief: none given \(--brief <file>\)\n$/);
  const blocks = (await readRun(fx.runId)).blocks;
  assert.deepEqual([blocks.B1.test_files, blocks.B2.test_files, Object.hasOwn(blocks.B3, 'test_files')], [['a', 'b'], ['a'], false]);
  const dispatch = (await readAllRows('b60-tests-line')).filter((r) => r.event === 'dispatch');
  assert.deepEqual(dispatch.map((r) => [r.block, Object.hasOwn(r, 'test_files') ? r.test_files : 'absent']), [['B1', ['a', 'b']], ['B2', ['a']], ['B3', 'absent']]);
});

test('B60 (1): --tests with no path is a usage error (exit 2), nothing opened', async () => {
  const fx = await fixture('b60-tests-empty', ['test: null']);
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--tests']), [2, '', 'block open: --tests needs at least one value\n']);
  assert.equal(Object.hasOwn((await readRun(fx.runId)).blocks, 'B1'), false);
});

test('B60 (1)(2): end to end — gates run --run --block writes gate.done with run, block, scope and test_count, and the binnacle reads it', async () => {
  const p = probe();
  const fx = await fixture('b60-binnacle', tokenGate(p.script));
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'test/a.test.mjs', 'test/b.test.mjs']))[0], 0);
  assert.equal((await fx.gate('B1')).code, 0);
  const rows = await readAllRows('b60-binnacle');
  const record = await readRun(fx.runId);
  const done = rows.find((r) => r.event === 'gate.done');
  assert.deepEqual([done?.run, done?.block, done?.scope, done?.test_count], [fx.runId, 'B1', 'related', 2]);
  const b = buildBinnacle({ runId: fx.runId, rows, record, now: new Date() });
  assert.deepEqual(b.status.find((s) => s.item === 'Full suite'), { item: 'Full suite', value: 'not recorded · related tests only' });
  const log = buildFullLog({ runId: fx.runId, rows, scrubCtx: undefined });
  assert.deepEqual(log.filter((e) => e.event === 'Gates finished').map((e) => [e.block, e.detail]), [['B1', 'unknown · all green · related tests (2)']]);
});

test('B60 (1): --tests follows the owned-entry path rules — an absolute or .. path is refused (bad-tests), nothing is opened', async () => {
  const fx = await fixture('b60-bad-tests', ['test: null']);
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', '/abs/a.test.mjs']), [1, '', 'block open: test files: invalid path "/abs/a.test.mjs": must be repo-relative, not absolute\n']);
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'test/../a.test.mjs']), [1, '', 'block open: test files: invalid path "test/../a.test.mjs": no ".", ".." or empty segments\n']);
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', '../x']), [1, '', 'block open: test files: invalid path "../x": no ".", ".." or empty segments\n']);
  assert.equal(Object.hasOwn((await readRun(fx.runId)).blocks, 'B1'), false);
  assert.deepEqual((await readAllRows('b60-bad-tests')).filter((r) => r.event === 'dispatch'), []);
  assert.throws(() => assertOwned(['../x'], { code: 'bad-tests', label: 'test files' }), { code: 'bad-tests' });
});

// ── the config: schema and rule 9 ───────────────────────────────────────────

/** @param {Record<string, any>} gates */
const configWith = (gates) => ({
  version: 1,
  project: { slug: 'b60-cfg' },
  provider: 'anthropic',
  engine: 'harness',
  levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } },
  gates,
});

test('B60 (3): the config accepts {argv, env} beside an argv array and null; each bad shape gets its exact issue list (rule, keyword, path, message), naming keys, never values', () => {
  const ok = validateConfig(configWith({ test: { argv: ['npm', 'test', '--', '{test_files}'], env: { APP_ENV: 'testing' } }, lint: { argv: ['npm', 'run', 'lint'] }, types: ['tsc'], format: null, full_suite_threshold_files: 5 }));
  assert.deepEqual([ok.valid, ok.errors], [true, []]);
  /** @param {Record<string, any>} gates */
  const issues = (gates) => validateConfig(configWith(gates)).errors.map((e) => [e.rule, e.keyword ?? null, e.path ?? null, e.message]);
  const anyOf = (/** @type {Array<[string, [string, string]]>} */ middle) => [
    ['schema', 'type', '/gates/test', '/gates/test must be array'],
    ...middle.map(([keyword, at]) => ['schema', keyword, at[0], at[1]]),
    ['schema', 'type', '/gates/test', '/gates/test must be null'],
    ['schema', 'anyOf', '/gates/test', '/gates/test must match a schema in anyOf'],
  ];
  assert.deepEqual(issues({ test: { argv: ['npm', 'test'] } }), []);
  assert.deepEqual(issues({ test: { argv: ['npm', 'test'], env: { A: 'b' } } }), []);
  assert.deepEqual(issues({ test: ['npm', 'test', '{test_files}'] }), []);
  assert.deepEqual(issues({ test: { argv: ['npm'], env: {} } }), []);
  assert.deepEqual(issues({ test: { argv: ['npm'], env: { A: 1 } } }), [
    ...anyOf([['type', ['/gates/test/env/A', '/gates/test/env/A must be string']]]),
    ['gates-not-argv-array', null, null, 'gates.test.env.A must be a string'],
  ]);
  assert.deepEqual(issues({ test: { argv: ['npm'], env: { 'BAD-NAME': 'x' } } }), [
    ...anyOf([['additionalProperties', ['/gates/test/env', '/gates/test/env must NOT have additional property "BAD-NAME"']]]),
    ['gates-not-argv-array', null, null, 'gates.test.env: "BAD-NAME" is not a variable name (letters, digits and _, not starting with a digit)'],
  ]);
  assert.deepEqual(issues({ test: { argv: ['npm'], shell: 'npm test' } }), [
    ...anyOf([['additionalProperties', ['/gates/test', '/gates/test must NOT have additional property "shell"']]]),
    ['gates-not-argv-array', null, null, 'gates.test: unknown key "shell" (an object gate has only argv and env)'],
  ]);
  assert.deepEqual(issues({ test: { env: { A: 'b' } } }), [
    ...anyOf([['required', ['/gates/test', "/gates/test must have required property 'argv'"]]]),
    ['gates-not-argv-array', null, null, 'gates.test.argv must be a non-empty argv array of strings'],
  ]);
  assert.deepEqual(issues({ test: ['{test_files}', 'x'] }), [['gates-not-argv-array', null, null, "gates.test: '{test_files}' cannot be the command (argv[0])"]]);
});

test('B60 (3): doctor checks a {argv, env} gate\'s command like an argv gate, and names no env value', async () => {
  const { checkCommands } = await import('../../src/doctor/local.mjs');
  const rows = await checkCommands({ gates: { test: { argv: ['node', 'x.mjs'], env: { B60_GATE_VALUE: VALUE } }, lint: ['node', 'y.mjs'], types: null } }, path.dirname(NODE));
  assert.deepEqual(rows.filter((r) => r.id.startsWith('gate')), [
    { id: 'gate.test', status: 'OK', label: 'gate test', detail: 'node on PATH' },
    { id: 'gate.lint', status: 'OK', label: 'gate lint', detail: 'node on PATH' },
  ]);
  assert.equal(JSON.stringify(rows).includes(VALUE), false);
});

// ── B60 fix round 3 ─────────────────────────────────────────────────────────

test('B60 (4): with --run --block in a cwd that is not a git repo, a plain gate runs as before — same argv, same exit code, gate.red and gate.done carry no scope', async () => {
  const p = probe();
  const fx = await fixture('b60-plain-nogit', [`test: [${JSON.stringify(NODE)}, ${JSON.stringify(p.script)}, "red", "x"]`, 'lint: null', 'types: null', 'format: null']);
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'test/a.test.mjs']))[0], 0);
  const nonGit = freshDir('plain-nogit');
  copyFileSync(path.join(fx.repo, '.code-forge.yml'), path.join(nonGit, '.code-forge.yml'));
  const [code, out, err] = await cli(runGatesVerb, ['run', '--cwd', nonGit, '--slug', 'b60-plain-nogit', '--run', fx.runId, '--block', 'B1']);
  assert.deepEqual([code, err], [1, '']);
  assert.deepEqual(p.seen(), { args: ['red', 'x'], value: null, inherited: true });
  const result = JSON.parse(out).results[0];
  assert.deepEqual([result.argv, result.code, Object.keys(result).sort()], [[NODE, p.script, 'red', 'x'], 3, ['argv', 'code', 'gate', 'ok', 'skipped', 'stderr', 'stdout']]);
  assert.deepEqual(await fx.gateRows(), [
    { event: 'gate.red', run: fx.runId, block: 'B1', gate: 'test', code: 3, ...SRC },
    { event: 'gate.done', run: fx.runId, block: 'B1', stack: 'unknown', all_ok: false, ...SRC },
  ]);
});

test('B60 (1)(4): hasTestFilesItem — true only when some gate argv has an item exactly {test_files}', () => {
  assert.equal(hasTestFilesItem({ test: ['npm', 'test', '{test_files}'] }), true);
  assert.equal(hasTestFilesItem({ lint: null, test: { argv: ['npm', '{test_files}'], env: { A: 'b' } } }), true);
  assert.equal(hasTestFilesItem({ test: ['npm', 'test', '--files={test_files}'], lint: { argv: ['x'] }, types: null }), false);
  assert.equal(hasTestFilesItem({ full_suite_threshold_files: 4, test: ['npm', 'test'] }), false);
  assert.equal(hasTestFilesItem(undefined), false);
});

test('B60 (2): ownedFileCount counts new (untracked, not ignored) files too — 1 tracked + 3 new under one owned directory is 4; an ignored file is not counted', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'lib/one/t.mjs', 'export {};\n');
  await commitAll(repo);
  for (let i = 0; i < 3; i += 1) writeFile(repo, `lib/one/u${i}.mjs`, 'export {};\n');
  writeFile(repo, '.gitignore', '*.log\n');
  writeFile(repo, 'lib/one/debug.log', 'x\n');
  assert.equal(await ownedFileCount(repo, ['lib/one']), 4);
});

test('B60 (2): ownedFileCount — not a git repo counts each entry 1; a git failure inside a repo (a corrupt index) is Infinity, so a {test_files} gate runs the full suite (threshold)', async () => {
  assert.equal(await ownedFileCount(freshDir('not-a-repo-2'), ['lib/feature', 'x.mjs', 'src/**']), 3);
  const repo = await makeRepo();
  writeFile(repo, 'lib/a.mjs', 'export {};\n');
  await commitAll(repo);
  writeFile(repo, '.git/index', 'not an index\n');
  assert.equal(await ownedFileCount(repo, ['lib/a.mjs']), Number.POSITIVE_INFINITY);
  assert.deepEqual(testScope({ testFiles: ['t/x.test.mjs'], owned: ['lib/a.mjs'], ownedFiles: Number.POSITIVE_INFINITY }), { scope: 'full', reason: 'threshold' });
});

test('B60 (1): block open --tests refuses "*", "?" and a path starting with "-" (exit 2, nothing opened); a directory is allowed', async () => {
  const fx = await fixture('b60-tests-glob', ['test: null']);
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'test/*.test.mjs']), [2, '', 'block open: --tests takes exact paths (a file or a directory); "*" and "?" are refused: "test/*.test.mjs"\n']);
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'test/a?.mjs']), [2, '', 'block open: --tests takes exact paths (a file or a directory); "*" and "?" are refused: "test/a?.mjs"\n']);
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', '-x']), [2, '', 'block open: --tests: a path may not start with "-": "-x"\n']);
  assert.equal(Object.hasOwn((await readRun(fx.runId)).blocks, 'B1'), false);
  assert.deepEqual((await readAllRows('b60-tests-glob')).filter((r) => r.event === 'dispatch'), []);
  const [code, , err] = await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'tests/Unit']);
  assert.deepEqual([code, err], [0, '']);
  assert.deepEqual((await readRun(fx.runId)).blocks.B1.test_files, ['tests/Unit']);
});

test('B60 (1): block open --tests accepts "[" and "{" in a path (real paths use them; argv never reaches a shell) and stores it unchanged', async () => {
  const fx = await fixture('b60-tests-bracket', ['test: null']);
  const [code, , err] = await fx.open('B1', ['--owned', 'src/a.mjs', '--tests', 'app/[id]/page.test.tsx', 'app/{slug}/page.test.tsx']);
  assert.deepEqual([code, err], [0, '']);
  assert.deepEqual((await readRun(fx.runId)).blocks.B1.test_files, ['app/[id]/page.test.tsx', 'app/{slug}/page.test.tsx']);
  const dispatch = (await readAllRows('b60-tests-bracket')).filter((r) => r.event === 'dispatch');
  assert.deepEqual(dispatch.map((r) => r.test_files), [['app/[id]/page.test.tsx', 'app/{slug}/page.test.tsx']]);
});

test('B60 (1): runGates checks every gate before running any — an empty argv in a later gate throws and no earlier gate has run', async () => {
  const p = probe();
  await assert.rejects(runGates({ test: [NODE, p.script, 'x'], lint: ['{test_files}'], types: null, format: null }, { cwd: freshDir('emptylater') }), {
    name: 'TypeError',
    message: "gates.lint: the argv is empty once '{test_files}' is dropped for the full suite — put the test command before it",
  });
  assert.throws(() => p.seen(), { code: 'ENOENT' });
});

test('B60 (2): runGates with scope: null falls back to no block — the item is dropped, full suite, reason no_tests', async () => {
  const p = probe();
  const { results } = await runGates({ test: [NODE, p.script, '--run', '{test_files}'], lint: null, types: null, format: null }, { cwd: freshDir('nullscope'), scope: /** @type {any} */ (null) });
  assert.deepEqual(p.seen(), { args: ['--run'], value: null, inherited: true });
  assert.deepEqual([results[0].argv, results[0].scope, results[0].reason], [[NODE, p.script, '--run'], 'full', 'no_tests']);
});

test('B60 (1): expandArgv refuses a test path starting with "-" (TypeError), the second guard behind block open', () => {
  assert.throws(() => expandArgv(['npm', 'test', '{test_files}'], { scope: 'related', files: ['test/a.mjs', '-x'] }), { name: 'TypeError', message: 'expandArgv: test path "-x" starts with "-"' });
  assert.deepEqual(expandArgv(['npm', 'test'], { scope: 'related', files: ['-x'] }), { argv: ['npm', 'test'], scoped: false });
});

test('B60 (3): printableGates(undefined) is {}', () => {
  assert.deepEqual(printableGates(undefined), {});
});

test('B60 (2): {test_files} as the ONLY argv item is refused by validate (a full-suite run would be empty); runGates names the gate if one gets through', async () => {
  /** @param {Record<string, any>} gates */
  const issues = (gates) => validateConfig(configWith(gates)).errors.map((e) => [e.rule, e.keyword ?? null, e.path ?? null, e.message]);
  const only = "gates.test: '{test_files}' cannot be the only item (the full suite would run an empty command)";
  assert.deepEqual(issues({ test: ['{test_files}'] }), [['gates-not-argv-array', null, null, only]]);
  assert.deepEqual(issues({ test: { argv: ['{test_files}'], env: { A: 'b' } } }), [['gates-not-argv-array', null, null, only]]);
  await assert.rejects(runGates({ test: ['{test_files}'], lint: null, types: null, format: null }, { cwd: freshDir('emptyargv') }), {
    name: 'TypeError',
    message: "gates.test: the argv is empty once '{test_files}' is dropped for the full suite — put the test command before it",
  });
});

test('B60 (3): a SHORT env value (ZZQ=q7x) appears in neither the JSON results nor any ledger row, and no result carries env', async () => {
  const p = probe();
  const fx = await fixture('b60-shortzz', [`test: {argv: [${JSON.stringify(NODE)}, ${JSON.stringify(p.script)}, "ok"], env: {ZZQ: q7x}}`, `lint: {argv: [${JSON.stringify(NODE)}, ${JSON.stringify(p.script)}, "lint"], env: {ZZQ: q7x}}`, 'types: null', 'format: null']);
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs']))[0], 0);
  const ran = await fx.gate('B1');
  assert.equal(ran.code, 0, ran.err);
  assert.equal(ran.out.includes('q7x') || ran.err.includes('q7x'), false);
  assert.deepEqual(ran.json.results.map((/** @type {any} */ r) => [r.gate, Object.hasOwn(r, 'env'), r.env_keys]), [['test', false, ['ZZQ']], ['lint', false, ['ZZQ']], ['types', false, undefined], ['format', false, undefined]]);
  assert.equal(JSON.stringify(await readAllRows('b60-shortzz')).includes('q7x'), false);
});
