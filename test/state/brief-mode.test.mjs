import { commitAll, freshDir, git, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

// B59 (issue #13 item 2): `run start --mode brief` — an official brief-only run with no plan. In
// that mode `block open` without `--level` takes the lane from the §3.5 rules over the block's
// owned entries, `--lines` and `proof.tiers.high.paths`. These tests pin the rows, exit codes and
// messages of every clause.

const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runReport } = await import('../../src/cli/report.mjs');
const { buildReport, buildRunModes, SECTION_NAMES } = await import('../../src/ledger/report.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { isMigrationPath, isPolicyPath, laneFacts } = await import('../../src/decide/lane-facts.mjs');
const { fallbackLane } = await import('../../src/decide/fallback-rules.mjs');
const { rulesRisk } = await import('../../src/review/engine.mjs');
const { openBlock } = await import('../../src/state/block.mjs');

const BIN = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');
const PROBE = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);

/** @param {string} slug @param {string} [extra] - YAML appended to the base config. */
const configYaml = (slug, extra = '') =>
  [
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
    '',
  ].join('\n') + extra;

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

/** @param {Function} fn @param {string[]} args @returns {Promise<[number, string]>} */
async function cli(fn, args) {
  const stream = capture();
  const code = await fn(args, { stdout: stream, stderr: stream, probe: PROBE });
  return [code, stream.text];
}

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');

/** @param {Record<string, any>} row */
const strip = ({ mac: _m, ts: _t, ...rest }) => rest;
/** @param {Record<string, any>} row */
const stripDecision = ({ mac: _m, ts: _t, decision_id: _d, ...rest }) => rest;

/**
 * A committed repo with `.code-forge.yml` (`extra` appended) and a few source files; `run start`
 * through the CLI with the `mode` args and a pinned worker.
 * @param {string} slug @param {string} runId @param {{extra?: string, mode?: string[]}} [opts]
 */
async function fixture(slug, runId, { extra = '', mode = ['--mode', 'brief'] } = {}) {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'src/auth/login.mjs', 'export const login = 1;\n');
  writeFile(repo, 'db/migrations/001.sql', 'select 1;\n');
  await commitAll(repo);
  const started = await cli(runRun, ['start', '--cwd', repo, '--run', runId, '--worker-pid', '4242', ...mode]);
  /** @param {string} id @param {string[]} more */
  const open = (id, more) => cli(runBlock, ['open', id, '--run', runId, '--acceptance', ACC, ...more]);
  const rows = async () => readAllRows(slug);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim().slice(0, 12);
  return { repo, started, open, rows, base };
}

test('B59 (1): run start --mode brief writes one signed run.mode row right after run.start and stores the mode', async () => {
  const fx = await fixture('b59-start-brief', 'r-b59-start-brief');
  assert.deepEqual(fx.started, [0, 'run r-b59-start-brief started · engine harness · mode brief (no plan) · worker pid 4242\n']);
  const rows = await fx.rows();
  assert.deepEqual(rows.map((r) => r.event), ['run.start', 'run.mode']);
  assert.deepEqual(strip(rows[1]), { run: 'r-b59-start-brief', event: 'run.mode', mode: 'brief', plan: 'none', tokens_source: null, cost_source: null });
  assert.deepEqual(verifyRow(rows[1], await loadKey('r-b59-start-brief')), { ok: true });
  assert.equal((await readRun('r-b59-start-brief')).mode, 'brief');
});

test('B59 (1)(6): --mode plan and no flag keep today: no run.mode row, no mode in the record, the same output', async () => {
  for (const [slug, mode] of /** @type {Array<[string, string[]]>} */ ([
    ['b59-start-plan', ['--mode', 'plan']],
    ['b59-start-none', []],
  ])) {
    const fx = await fixture(slug, `r-${slug}`, { mode });
    assert.deepEqual(fx.started, [0, `run r-${slug} started · engine harness · worker pid 4242\n`]);
    assert.deepEqual((await fx.rows()).map((r) => r.event), ['run.start']);
    assert.equal(Object.hasOwn(await readRun(`r-${slug}`), 'mode'), false);
  }
});

test('B59 (1): any other --mode value, or --mode on another verb or with --reattach, is a usage error (exit 2) and writes nothing', async () => {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', configYaml('b59-bad-mode'));
  await commitAll(repo);
  for (const value of ['task', 'Brief', 'none']) {
    assert.deepEqual(await cli(runRun, ['start', '--cwd', repo, '--run', 'r-b59-bad-mode', '--worker-pid', '4242', '--mode', value]), [2, 'run start: --mode must be one of plan, brief\n']);
  }
  await assert.rejects(readRun('r-b59-bad-mode'), { code: 'no-run' });
  assert.deepEqual(await readAllRows('b59-bad-mode'), []);

  const fx = await fixture('b59-mode-verbs', 'r-b59-mode-verbs');
  const fixed = '--mode is set once, by a new run start (the mode is fixed for the run)\n';
  assert.deepEqual(await cli(runRun, ['status', '--run', 'r-b59-mode-verbs', '--mode', 'plan']), [2, `run status: ${fixed}`]);
  assert.deepEqual(await cli(runRun, ['reload', '--run', 'r-b59-mode-verbs', '--mode', 'plan']), [2, `run reload: ${fixed}`]);
  assert.deepEqual(await cli(runRun, ['start', '--reattach', '--run', 'r-b59-mode-verbs', '--worker-pid', '4242', '--mode', 'plan']), [2, `run start: ${fixed}`]);
  assert.equal((await readRun('r-b59-mode-verbs')).mode, 'brief');
  assert.deepEqual((await fx.rows()).map((r) => r.event), ['run.start', 'run.mode']);
});

test('B59 (1): run reload never changes the mode', async () => {
  const fx = await fixture('b59-reload', 'r-b59-reload');
  writeFile(fx.repo, '.code-forge.yml', configYaml('b59-reload', 'budget:\n  usd: 5\n'));
  const [code, text] = await cli(runRun, ['reload', '--run', 'r-b59-reload']);
  assert.equal(code, 0, text);
  assert.equal(text, 'run r-b59-reload: config reloaded · 1 key changed\n  budget\nqueued review tickets (0) keep the config they were enqueued with; new tickets use the new config\n');
  assert.equal((await readRun('r-b59-reload')).mode, 'brief');
  assert.deepEqual((await fx.rows()).map((r) => r.event), ['run.start', 'run.mode', 'run.reload']);
});

test('B59 (2): brief-mode block open without --level takes the rules lane and writes one signed decision row before dispatch', async () => {
  const fx = await fixture('b59-rules-l0', 'r-b59-rules-l0');
  const [code, text] = await fx.open('B1', ['--owned', 'src/a.mjs']);
  assert.equal(code, 0, text);
  assert.equal(text, `block B1 open · L0 (rules lane) · code · attempt 1 · base ${fx.base}\nbrief: none given (--brief <file>)\n`);
  const rows = await fx.rows();
  assert.deepEqual(rows.map((r) => r.event), ['run.start', 'run.mode', 'decision', 'dispatch']);
  assert.deepEqual(stripDecision(rows[2]), {
    run: 'r-b59-rules-l0',
    event: 'decision',
    block: 'B1',
    question: 'lane',
    answer: 'L0',
    source: 'rules',
    facts: { filesChanged: 1, linesAdded: 0, touchesMigration: false, touchesPolicyOrMiddleware: false, pathFloorHit: false },
    tokens_source: null,
    cost_source: null,
  });
  assert.match(rows[2].decision_id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  assert.deepEqual(verifyRow(rows[2], await loadKey('r-b59-rules-l0')), { ok: true });
  assert.equal(rows[3].level, 'L0');
  assert.equal((await readRun('r-b59-rules-l0')).blocks.B1.level, 'L0');
});

test('B59 (2): the rules lane reads --lines, the owned-entry count, migration and policy paths and proof.tiers.high.paths of the snapshot', async () => {
  const fx = await fixture('b59-rules-mix', 'r-b59-rules-mix', { extra: 'proof:\n  tiers:\n    high:\n      paths: [src/auth/login.mjs]\n' });
  /** @param {string} id @param {string[]} more @returns {Promise<string>} the lane of the decision row */
  const laneOf = async (id, more) => {
    const [code, text] = await fx.open(id, more);
    assert.equal(code, 0, text);
    const decisions = (await fx.rows()).filter((r) => r.event === 'decision' && r.block === id);
    assert.equal(decisions.length, 1);
    assert.equal((await readRun('r-b59-rules-mix')).blocks[id].level, decisions[0].answer);
    return decisions[0].answer;
  };
  assert.equal(await laneOf('B1', ['--owned', 'src/a.mjs', '--lines', '200']), 'L0');
  assert.equal(await laneOf('B2', ['--owned', 'src/b.mjs', '--lines', '201']), 'L1');
  assert.equal(await laneOf('B3', ['--owned', 'lib/1.mjs', 'lib/2.mjs', 'lib/3.mjs', 'lib/4.mjs', 'lib/5.mjs']), 'L1');
  assert.equal(await laneOf('B4', ['--owned', 'db/migrations/001.sql']), 'L2');
  assert.equal(await laneOf('B5', ['--owned', 'app/http/middleware/guard.mjs']), 'L2');
  assert.equal(await laneOf('B6', ['--owned', 'src/auth']), 'L2'); // a directory above a high path
});

test('B59 (2): a high path names an owned entry directly; laneFacts is the fact set the rules see', () => {
  assert.deepEqual(laneFacts({ owned: ['src/auth/login.mjs'], lines: 12, highPaths: ['src/auth/**'] }), {
    filesChanged: 1,
    linesAdded: 12,
    touchesMigration: false,
    touchesPolicyOrMiddleware: false,
    pathFloorHit: true,
  });
  assert.equal(laneFacts({ owned: ['src/a.mjs'], highPaths: ['src/auth'] }).pathFloorHit, false);
  assert.equal(laneFacts({ owned: ['src/a.mjs'], highPaths: [] }).pathFloorHit, false);
  assert.equal(laneFacts({ owned: ['config/policies.mjs'] }).touchesPolicyOrMiddleware, true);
  // highPaths that is null or not an array means none
  assert.equal(laneFacts({ owned: ['src/a.mjs'], highPaths: null }).pathFloorHit, false);
  assert.equal(laneFacts({ owned: ['src/a.mjs'], highPaths: 'src/a.mjs' }).pathFloorHit, false);
  // a high path below an owned directory, an owned file below a high directory, a sibling prefix
  assert.equal(laneFacts({ owned: ['src/auth'], highPaths: ['src/auth/login.mjs'] }).pathFloorHit, true);
  assert.equal(laneFacts({ owned: ['src/auth/login.mjs'], highPaths: ['src/auth'] }).pathFloorHit, true);
  assert.equal(laneFacts({ owned: ['src/ab'], highPaths: ['src/a'] }).pathFloorHit, false);
  assert.equal(laneFacts({ owned: ['src/a'], highPaths: ['src/ab'] }).pathFloorHit, false);
  // a repeated owned entry counts once
  assert.equal(laneFacts({ owned: ['src/a.mjs', 'src/a.mjs', 'src/b.mjs'] }).filesChanged, 2);
});

test('B59 (2)(6): the migration and policy checks are the §3.5 review rules unchanged: case-insensitive substring matches', () => {
  for (const [file, migration, policy, risk] of /** @type {Array<[string, boolean, boolean, number]>} */ ([
    ['db/Migrations/x.sql', true, false, 2],
    ['app/policies/a.rb', false, true, 2],
    ['src/Policy.ts', false, true, 2],
    ['lib/middleware.js', false, true, 2],
    ['src/migrationHelper.ts', true, false, 2],
    ['src/police.js', false, false, 0],
    // substring match on purpose, as the review rules: a migration guide counts as a migration path
    ['docs/migration-guide.md', true, false, 2],
  ])) {
    assert.equal(isMigrationPath(file), migration, file);
    assert.equal(isPolicyPath(file), policy, file);
    assert.equal(rulesRisk({ file, plusCount: 0, cfg: {} }), risk, file);
    assert.equal(fallbackLane(laneFacts({ owned: [file] })), risk === 2 ? 'L2' : 'L0', file);
  }
});

test('B59 (2): an explicit --level still wins in a brief-mode run and writes no decision row', async () => {
  const fx = await fixture('b59-explicit', 'r-b59-explicit');
  const [code, text] = await fx.open('B1', ['--level', 'L2', '--owned', 'src/a.mjs']);
  assert.equal(code, 0, text);
  assert.equal(text, `block B1 open · L2 · code · attempt 1 · base ${fx.base}\nbrief: none given (--brief <file>)\n`);
  assert.deepEqual((await fx.rows()).map((r) => r.event), ['run.start', 'run.mode', 'dispatch']);
});

test('B59 (2): a refused brief-mode open writes no decision row', async () => {
  const fx = await fixture('b59-refused', 'r-b59-refused');
  assert.equal((await fx.open('B1', ['--owned', 'src']))[0], 0);
  assert.deepEqual(await fx.open('B2', ['--owned', 'src/a.mjs']), [1, 'block open: block B2 owns src/a.mjs, which overlaps src owned by open block B1\n']);
  assert.deepEqual((await fx.rows()).map((r) => r.event), ['run.start', 'run.mode', 'decision', 'dispatch']);
});

// Clause 3, which way --lines went: it stayed an accepted flag. Before B59 `block open` already took
// `--lines` and stored it as the dispatch row's `lines_forecast` (pinned pre-B59 by
// "`block open` records base = HEAD, level, attempt, acceptance, forecast, ..." in
// test/state/block.test.mjs); B59 only also feeds it to the brief-mode rules lane.
test('B59 (3): outside brief mode no --level is the same usage error; --lines is accepted as the forecast it always was (the parser already took it)', async () => {
  const fx = await fixture('b59-plan-open', 'r-b59-plan-open', { mode: [] });
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs']), [2, 'block open: no --level given and no autopilot level recorded for B1\n']);
  assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--lines', '500']), [2, 'block open: no --level given and no autopilot level recorded for B1\n']);
  // the pre-B59 error order: with no --level, a bad --lines still reports the missing level first
  for (const value of ['abc', '-5']) {
    assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--lines', value]), [2, 'block open: no --level given and no autopilot level recorded for B1\n'], value);
  }
  assert.deepEqual(await fx.open('B1', ['--level', 'L1', '--owned', 'src/a.mjs', '--lines', 'abc']), [2, 'block open: --lines must be a positive integer\n']);
  const [code, text] = await fx.open('B1', ['--level', 'L1', '--owned', 'src/a.mjs', '--lines', '500']);
  assert.equal(code, 0, text);
  const rows = await fx.rows();
  assert.deepEqual(rows.map((r) => r.event), ['run.start', 'dispatch']);
  assert.equal(rows[1].lines_forecast, 500);
  assert.equal(rows[1].level, 'L1');
});

test('B59 (4): report shows each run as brief or plan, with the 13 pinned sections unchanged', async () => {
  const slug = 'b59-report';
  /** @param {Record<string, any>} row */
  const add = (row) => appendRow(row, { slug });
  await add({ event: 'run.start', run: 'r-one', engine: 'harness' });
  await add({ event: 'run.mode', run: 'r-one', mode: 'brief', plan: 'none' });
  await add({ event: 'run.start', run: 'r-two', engine: 'harness' });
  const rows = await readAllRows(slug);
  assert.deepEqual(buildRunModes(rows), [
    { run: 'r-one', mode: 'brief' },
    { run: 'r-two', mode: 'plan' },
  ]);
  assert.equal(SECTION_NAMES.length, 13);
  assert.deepEqual(Object.keys(buildReport(rows).sections), [...SECTION_NAMES]);
  const [code, text] = await cli(runReport, ['--slug', slug]);
  assert.equal(code, 0);
  assert.ok(text.endsWith('run mode:\n  run r-one: brief\n  run r-two: plan\n'), text);
  assert.equal(text.match(/^== /gm)?.length, 13);
  // the mode is terminal text only: `--json` carries the 13 pinned sections and no mode key
  const [jsonCode, json] = await cli(runReport, ['--slug', slug, '--json']);
  assert.equal(jsonCode, 0);
  assert.deepEqual(Object.keys(JSON.parse(json)), [...SECTION_NAMES]);
});

test('B59 (5): there is no task verb', async () => {
  const res = await new Promise((resolve) => {
    execFile(process.execPath, [BIN, 'task'], (error, stdout, stderr) => resolve({ code: error?.code ?? 0, stdout, stderr }));
  });
  assert.equal(/** @type {any} */ (res).code, 1);
  assert.match(/** @type {any} */ (res).stderr, /^code-forge: unknown verb "task"\n/);
  const help = await new Promise((resolve) => {
    execFile(process.execPath, [BIN, '--help'], (error, stdout) => resolve({ code: error?.code ?? 0, stdout }));
  });
  assert.equal(/** @type {any} */ (help).code, 0);
  assert.match(/** @type {any} */ (help).stdout, /^ {2}run$/m);
  assert.doesNotMatch(/** @type {any} */ (help).stdout, /^ {2}task$/m);
});

test('B59 (2): brief mode keeps the usage error for a missing --owned, and one --owned value is a one-item list', async () => {
  const fx = await fixture('b59-owned', 'r-b59-owned');
  assert.deepEqual(await fx.open('B1', []), [2, 'block open: block open needs --level, --owned and --acceptance\n']);
  assert.deepEqual((await fx.rows()).map((r) => r.event), ['run.start', 'run.mode']);
  const [code, text] = await fx.open('B1', ['--owned', 'src/a.mjs']);
  assert.equal(code, 0, text);
  const rows = await fx.rows();
  assert.deepEqual(rows[3].owned_files, ['src/a.mjs']);
  assert.equal(rows[2].facts.filesChanged, 1);
});

test('B59 (2): a bad --lines in brief mode is a usage error (exit 2) and writes no row', async () => {
  const fx = await fixture('b59-bad-lines', 'r-b59-bad-lines');
  for (const value of ['abc', '-5', '0']) {
    assert.deepEqual(await fx.open('B1', ['--owned', 'src/a.mjs', '--lines', value]), [2, 'block open: --lines must be a positive integer\n'], value);
  }
  assert.deepEqual((await fx.rows()).map((r) => r.event), ['run.start', 'run.mode']);
});

test('B59 (2): openBlock writes the decision row with its fixed fields over the caller\'s, and takes a given decision id', async () => {
  const fx = await fixture('b59-decision-id', 'r-b59-decision-id');
  assert.equal(fx.started[0], 0, fx.started[1]);
  /** @type {Array<Record<string, any>>} */
  const written = [];
  await openBlock({
    runId: 'r-b59-decision-id',
    id: 'B1',
    level: 'L0',
    owned: ['src/a.mjs'],
    acceptance: [{ clause: 'c', tests: ['t'] }],
    decisionId: 'd-fixed',
    // fields the type does not allow, to show the fixed ones win over a caller's
    decision: /** @type {any} */ ({ question: 'lane', answer: 'L0', source: 'rules', event: 'not-a-decision', block: 'B9', decision_id: 'd-other' }),
    writeRow: async (/** @type {Record<string, any>} */ row) => {
      written.push(row);
    },
  });
  assert.deepEqual(written.map((r) => r.event), ['decision', 'dispatch']);
  assert.deepEqual(strip(written[0]), { run: 'r-b59-decision-id', question: 'lane', answer: 'L0', source: 'rules', event: 'decision', decision_id: 'd-fixed', block: 'B1' });
  assert.deepEqual(verifyRow(written[0], await loadKey('r-b59-decision-id')), { ok: true });
});

test('B59 (1)(6): --mode plan and no flag give the same record and rows, apart from the run id, times and commit', async () => {
  const slug = 'b59-same';
  /** @param {string} runId @param {string[]} mode */
  const shape = async (runId, mode) => {
    const fx = await fixture(slug, runId, { mode });
    assert.equal(fx.started[0], 0, fx.started[1]);
    const [code, text] = await fx.open('B1', ['--level', 'L1', '--owned', 'src/a.mjs', '--lines', '40']);
    assert.equal(code, 0, text);
    const sha = (await git(['rev-parse', 'HEAD'], fx.repo)).trim();
    /** @param {unknown} value @returns {unknown} the value with the run id, commit and repo path made neutral */
    const neutral = (value) =>
      JSON.parse(JSON.stringify(value).replaceAll(runId, 'RUN').replaceAll(sha, 'SHA').replaceAll(fx.base, 'BASE').replaceAll(fx.repo, 'REPO'));
    /** @param {Record<string, any> | undefined} tree @returns {string[]} the run file holds the run id and times, so only the paths compare */
    const paths = (tree) => Object.keys(tree ?? {});
    const { started_at: _s, orchestrator_pid: _p, workspace: _w, ...record } = /** @type {Record<string, any>} */ (await readRun(runId));
    for (const block of Object.values(record.blocks ?? {})) {
      delete block.opened_at;
      block.tree_at_open = paths(block.tree_at_open);
    }
    const rows = (await fx.rows())
      .filter((r) => r.run === runId)
      .map(({ mac: _m, ts: _t, opened_at: _o, tree_at_open: tree, ...rest }) => ({ ...rest, tree_at_open: paths(tree) }));
    return neutral({ record, rows, text });
  };
  assert.deepEqual(await shape('r-b59-same-plan', ['--mode', 'plan']), await shape('r-b59-same-none', []));
});

test('B59 (4): run start --mode brief, then report, shows that run as brief', async () => {
  const fx = await fixture('b59-report-e2e', 'r-b59-report-e2e');
  assert.equal(fx.started[0], 0, fx.started[1]);
  const [code, text] = await cli(runReport, ['--slug', 'b59-report-e2e']);
  assert.equal(code, 0, text);
  assert.ok(text.endsWith('run mode:\n  run r-b59-report-e2e: brief\n'), text);
});

test('B59 (4): a ledger with no run.start prints no run mode header', async () => {
  const slug = 'b59-report-none';
  await appendRow({ event: 'decision', run: 'r-x', question: 'q', answer: 'a' }, { slug });
  const [code, text] = await cli(runReport, ['--slug', slug]);
  assert.equal(code, 0, text);
  assert.equal(buildRunModes(await readAllRows(slug)).length, 0);
  assert.doesNotMatch(text, /run mode:/);
  // the output ends with the spend lines, nothing after them
  assert.equal(text.slice(text.indexOf('spend per run')), 'spend per run (estimated USD, open blocks included):\n  total: $0.0000 · 0 session(s)\n');
});

test('B59 (2): the rules lane reads the high paths of the run snapshot, not a rewritten config', async () => {
  const fx = await fixture('b59-snapshot', 'r-b59-snapshot', { extra: 'proof:\n  tiers:\n    high:\n      paths: [src/auth/login.mjs]\n' });
  writeFile(fx.repo, '.code-forge.yml', configYaml('b59-snapshot', 'proof:\n  tiers:\n    high:\n      paths: [src/a.mjs]\n'));
  assert.equal((await fx.open('B1', ['--owned', 'src/a.mjs']))[0], 0);
  assert.equal((await fx.open('B2', ['--owned', 'src/auth/login.mjs']))[0], 0);
  const decisions = (await fx.rows()).filter((r) => r.event === 'decision');
  assert.deepEqual(decisions.map((r) => [r.block, r.answer, r.facts.pathFloorHit]), [
    ['B1', 'L0', false],
    ['B2', 'L2', true],
  ]);
});

test('B59 (2): openBlock refuses a decision whose answer is not the requested level, and writes nothing', async () => {
  const fx = await fixture('b59-decision-level', 'r-b59-decision-level');
  assert.equal(fx.started[0], 0, fx.started[1]);
  /** @type {Array<Record<string, any>>} */
  const written = [];
  await assert.rejects(
    openBlock({
      runId: 'r-b59-decision-level',
      id: 'B1',
      level: 'L0',
      owned: ['src/a.mjs'],
      acceptance: [{ clause: 'c', tests: ['t'] }],
      decision: { question: 'lane', answer: 'L1', source: 'rules', facts: {} },
      writeRow: async (/** @type {Record<string, any>} */ row) => {
        written.push(row);
      },
    }),
    { code: 'bad-decision', message: 'the decision answer L1 is not the requested level L0' },
  );
  assert.deepEqual(written, []);
  assert.deepEqual((await readRun('r-b59-decision-level')).blocks, {});
});
