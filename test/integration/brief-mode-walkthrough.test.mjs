/**
 * B67 (5): the docs/brief-mode.md walk-through, end to end, with the real CLI. The config and the
 * hook script are read from the doc's own ```yaml and ```js fences, written into a temp git repo
 * (temp HOME from the helpers), and the doc's sequence runs as child processes, in the doc's
 * order: run start (--mode brief), block open with --tests/--lines (rules lane), context pack, the
 * coder's commit in the worktree, gates run and status, a refused close (no TESTS line), the close
 * with the `by: config` waiver and --remove-worktree, the done file and the hook, status, ledger
 * outside-review, report and run end. Exit codes, outputs and ledger rows are asserted, and the
 * real `~/.code-forge` and `<repo>/.code-forge` gain nothing.
 */
import { PARENT, commitAll, freshDir, git, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { existsSync, readFileSync, realpathSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const { readAllRows } = await import('../../src/ledger/write.mjs');
const { doneFilePath } = await import('../../src/state/block-end.mjs');
const { GUARDED_DIRS, snapshotGuarded } = await import('../helpers/isolate.mjs');

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const BIN = path.join(ROOT, 'bin', 'code-forge.mjs');
const DOC = readFileSync(path.join(ROOT, 'docs', 'brief-mode.md'), 'utf8');
const RUN = 'r-b67-walk';
const SLUG = 'shop-api';

/** @type {import('node:child_process').ChildProcess[]} */
const children = [];
after(() => {
  for (const c of children) c.kill('SIGKILL');
});
/** A live process that stands in for the review worker (`run start --worker-pid`). */
const sleeper = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 600000)'], { stdio: 'ignore' });
children.push(sleeper);

/** @type {NodeJS.ProcessEnv} */
const env = { ...process.env, CODE_FORGE_NO_ERROR_LOG: '1' };
delete env.NODE_TEST_CONTEXT;
/**
 * The children's temp folder (the run's tmp root, the context pack): under the helpers' one
 * per-file temp parent, which `after()` removes together with the repo and its `-worktrees`
 * sibling.
 */
const CHILD_TMP = freshDir('tmp');
env.TMPDIR = CHILD_TMP;

/**
 * @param {string[]} args @param {string} cwd
 * @returns {Promise<{code: number, stdout: string, stderr: string}>}
 */
const cf = (args, cwd) =>
  new Promise((resolve) => {
    const child = execFile(process.execPath, [BIN, ...args], { cwd, env, timeout: 60000, killSignal: 'SIGKILL' }, (error, stdout, stderr) => {
      const code = error ? (typeof error.code === 'number' ? error.code : -1) : 0;
      resolve({ code, stdout: String(stdout), stderr: String(stderr) });
    });
    children.push(child);
  });

/** @param {string} lang @returns {string} the body of the doc's first ```lang fence */
const fence = (lang) => {
  const m = DOC.match(new RegExp('^```' + lang + '\\n([\\s\\S]*?)^```$', 'm'));
  assert.ok(m, `no ${lang} fence in docs/brief-mode.md`);
  return m[1];
};

/** @returns {Promise<Array<Record<string, any>>>} this run's ledger rows, in order */
const rows = async () => (await readAllRows(SLUG)).filter((r) => r.run === RUN);

/** @param {string} body @returns {string} the cart test file with one assertion */
const CART_TEST = (body) => `import test from 'node:test';\nimport assert from 'node:assert/strict';\nimport { total } from '../src/cart.mjs';\ntest('total', () => ${body});\n`;

test('B67 (5): the docs/brief-mode.md walk-through runs as written with the real CLI', async () => {
  // The real ~/.code-forge (HOME as it was before isolate.mjs moved it) and <repo>/.code-forge.
  const realHome = process.env.CODE_FORGE_REAL_HOME ?? '';
  assert.notEqual(realHome, '');
  assert.notEqual(realHome, process.env.HOME);
  assert.deepEqual(GUARDED_DIRS, [path.join(realHome, '.code-forge'), path.join(ROOT, '.code-forge')]);
  const guardedBefore = snapshotGuarded();

  const repo = realpathSync(await makeRepo());
  writeFile(repo, '.code-forge.yml', fence('yaml'));
  writeFile(repo, 'scripts/on-block-end.mjs', fence('js'));
  writeFile(repo, 'AGENTS.md', '# Rules\n\n- Tests check behaviour.\n');
  writeFile(repo, 'docs/lessons.md', '# Lessons\n\n- Run the related tests first.\n');
  writeFile(repo, 'src/cart.mjs', 'export const total = (items) => items.length;\n');
  writeFile(repo, 'test/cart.test.mjs', CART_TEST('assert.equal(total([]), 0)'));
  writeFile(repo, 'plans/cart-total.brief.md', '# Cart total\n\nSum the item prices.\n');
  writeFile(repo, 'plans/cart-total.acc.yml', '- clause: total sums the prices\n  tests: [cart-total]\n');
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();

  // Step 1: the config validates as written.
  const validate = await cf(['validate'], repo);
  assert.equal(validate.code, 0, validate.stderr + validate.stdout);

  // Step 2: run start --mode brief.
  const start = await cf(['run', 'start', '--cwd', repo, '--run', RUN, '--mode', 'brief', '--worker-pid', String(sleeper.pid)], repo);
  assert.equal(start.code, 0, start.stderr);
  assert.equal(start.stdout, `run ${RUN} started · engine harness · mode brief (no plan) · worker pid ${sleeper.pid}\n`);
  let events = (await rows()).map((r) => r.event);
  assert.deepEqual(events, ['run.start', 'run.mode', 'review.external']);
  const mode = (await rows())[1];
  assert.equal(mode.mode, 'brief');
  assert.equal(mode.plan, 'none');

  // Step 3: block open without --level takes the rules lane.
  const open = await cf(['block', 'open', 'B1', '--run', RUN, '--owned', 'src/cart.mjs', 'test/cart.test.mjs', '--tests', 'test/cart.test.mjs', '--acceptance', 'plans/cart-total.acc.yml', '--brief', 'plans/cart-total.brief.md', '--lines', '20'], repo);
  assert.equal(open.code, 0, open.stderr);
  const tree = path.join(path.dirname(repo), `${path.basename(repo)}-worktrees`, 'B1');
  assert.equal(path.dirname(path.dirname(tree)), PARENT);
  const branch = `forge/${RUN}/B1`;
  const openLines = open.stdout.split('\n');
  assert.equal(openLines.length, 4, open.stdout);
  assert.equal(openLines[0], `block B1 open · L0 (rules lane) · code · attempt 1 · base ${base.slice(0, 12)} · 1 test file`);
  assert.equal(openLines[1], `worktree ${tree} · branch ${branch}`);
  assert.match(openLines[2], /^BRIEF plans\/cart-total\.brief\.md lines=3 sha=[0-9a-f]{8} <<<EOM>>>$/);
  assert.equal(openLines[3], '');
  const decisions = (await rows()).filter((r) => r.event === 'decision' && r.block === 'B1');
  assert.equal(decisions.length, 1);
  const decision = decisions[0];
  assert.equal(decision.question, 'lane');
  assert.equal(decision.answer, 'L0');
  assert.equal(decision.source, 'rules');
  assert.equal(existsSync(tree), true);

  // Step 4: the context pack, in the run's temp folder.
  const pack = await cf(['context', 'pack', '--run', RUN, '--block', 'B1'], repo);
  assert.equal(pack.code, 0, pack.stderr);
  const [packPath, tokens, rest] = pack.stdout.split('\n');
  assert.equal(packPath, path.join(CHILD_TMP, 'code-forge', RUN, 'context', 'B1.md'));
  assert.equal(existsSync(path.join(os.tmpdir(), 'code-forge', RUN)), false);
  assert.match(tokens, /^tokens ~\d+$/);
  assert.equal(rest, '');
  const packText = readFileSync(packPath, 'utf8');
  assert.equal(packText.includes('- Tests check behaviour.'), true);
  assert.equal(packText.includes('- Run the related tests first.'), true);
  assert.equal(packText.includes(tree), true);
  assert.equal((await rows()).filter((r) => r.event === 'context.pack').length, 1);

  // Step 5: the coder edits, runs the tests and commits in the worktree.
  writeFile(tree, 'src/cart.mjs', 'export const total = (items) => items.reduce((sum, item) => sum + item.price, 0);\n');
  writeFile(tree, 'test/cart.test.mjs', CART_TEST('assert.equal(total([{ price: 2 }, { price: 3 }]), 5)'));
  await git(['commit', '-q', '-am', 'cart total'], tree);
  const tip = (await git(['rev-parse', 'HEAD'], tree)).trim();

  // Step 6: the gates, with --slug so the run gets its gate row.
  const gates = await cf(['gates', 'run', '--slug', SLUG, '--run', RUN, '--block', 'B1'], repo);
  assert.equal(gates.code, 0, gates.stderr + gates.stdout);
  const gateOut = JSON.parse(gates.stdout);
  assert.equal(gateOut.results.length, 4, gates.stdout);
  assert.deepEqual(gateOut.results.map((/** @type {any} */ r) => [r.gate, r.skipped]), [['test', false], ['lint', true], ['types', true], ['format', true]]);
  assert.equal(gateOut.allOk, true);
  const testGates = gateOut.results.filter((/** @type {any} */ r) => r.gate === 'test');
  assert.equal(testGates.length, 1, gates.stdout);
  const testGate = testGates[0];
  assert.deepEqual(testGate.argv, ['node', '--test', 'test/cart.test.mjs']);
  assert.equal(testGate.scope, 'related');
  assert.equal(testGate.test_count, 1);
  assert.deepEqual(testGate.env_keys, ['NODE_ENV']);
  assert.equal(gates.stdout.includes('"NODE_ENV":"test"'), false);
  const gateDone = (await rows()).filter((r) => r.event === 'gate.done' && r.block === 'B1');
  assert.equal(gateDone.length, 1);

  // Step 6, continued: status shows the open block and its passing gate.
  const statusOpen = await cf(['status', '--run', RUN], repo);
  assert.equal(statusOpen.code, 0, statusOpen.stderr);
  assert.equal(statusOpen.stdout, `run ${RUN} · active · brief · spent $0.00\nB1 open L0 · - · gate pass · findings 0\n`);

  // A coder report without the TESTS line is refused and the block stays open.
  writeFile(repo, '.code-forge/B1-report.txt', '1 test, 1 pass, 0 fail\n===BLOCK B1 COMPLETE===\n');
  writeFile(repo, '.code-forge/B1-transcript.log', '$ node --test test/cart.test.mjs\nok\n');
  const closeArgs = ['block', 'close', 'B1', '--run', RUN, '--report', '.code-forge/B1-report.txt', '--transcript', '.code-forge/B1-transcript.log', '--remove-worktree'];
  const refused = await cf(closeArgs, repo);
  assert.equal(refused.code, 1, refused.stdout);
  assert.equal((refused.stdout + refused.stderr).includes('coder report FAILED: no TESTS line'), true, refused.stdout + refused.stderr);
  assert.equal((await rows()).some((r) => r.event === 'block.close'), false);
  assert.equal(existsSync(tree), true);
  assert.equal(existsSync(doneFilePath(RUN, 'B1')), false);

  // Step 7: the close, with the report the doc shows (the sentinel is its last line).
  writeFile(repo, '.code-forge/B1-report.txt', '1 test, 1 pass, 0 fail\nTESTS node --test test/cart.test.mjs exit=0\n===BLOCK B1 COMPLETE===\n');
  const close = await cf(closeArgs, repo);
  assert.equal(close.code, 0, close.stderr + close.stdout);
  assert.equal(close.stdout, `block B1 closed\nblock B1 worktree removed: ${tree} (branch ${branch} kept)\n`);
  const all = await rows();
  events = all.map((r) => r.event);
  const tail = events.slice(events.indexOf('gate.reviews_waived'));
  assert.deepEqual(tail, ['gate.reviews_waived', 'coder.tests', 'block.close', 'worktree.removed', 'hook.ran']);
  const waived = all.find((r) => r.event === 'gate.reviews_waived');
  assert.equal(waived?.by, 'config');
  assert.equal(waived?.reviewer, 'Outside reviewer');
  assert.equal(all.find((r) => r.event === 'coder.tests')?.exit, 0);
  assert.equal(existsSync(tree), false);
  assert.equal((await git(['rev-parse', branch], repo)).trim(), tip);

  // Step 8: the done file and the hook.
  const doneFile = doneFilePath(RUN, 'B1');
  const done = JSON.parse(readFileSync(doneFile, 'utf8'));
  assert.deepEqual(Object.keys(done), ['block', 'run_id', 'status', 'event', 'reason', 'commit', 'spent_usd', 'unknown_usd_sessions', 'findings', 'at']);
  assert.equal(done.block, 'B1');
  assert.equal(done.run_id, RUN);
  assert.equal(done.status, 'closed');
  assert.equal(done.event, 'block.close');
  assert.equal(done.reason, null);
  assert.equal(done.commit, tip);
  assert.equal(done.findings, 0);
  assert.equal(readFileSync(path.join(repo, '.code-forge', 'block-ends.log'), 'utf8'), `${RUN} B1 closed ${doneFile}\n`);

  // Step 9: status after the close.
  const statusDone = await cf(['status', '--run', RUN], repo);
  assert.equal(statusDone.code, 0, statusDone.stderr);
  assert.equal(statusDone.stdout, `run ${RUN} · active · brief · spent $0.00\nclosed 1 · stopped 0\n`);

  // Step 10: the outside review.
  const outside = await cf(['ledger', 'outside-review', '--run', RUN, '--pr', '42', '--rounds', '2', '--findings', '3', '--link', 'https://example.com/pr/42', '--block', 'B1'], repo);
  assert.equal(outside.code, 0, outside.stderr);
  assert.equal(outside.stdout, 'outside_review pr 42 · rounds 2 · findings 3 · blocks B1\n');

  // Step 11: the report, before run end.
  const report = await cf(['report', '--slug', SLUG], repo);
  assert.equal(report.code, 0, report.stderr);
  assert.equal(report.stdout.includes('== outside_review ==\nPR #42 · 2 outside rounds · 3 findings · code-forge $0.00 · B1\n'), true, report.stdout);
  assert.equal(report.stdout.includes(`run mode:\n  run ${RUN}: brief`), true, report.stdout);
  const reportJson = await cf(['report', '--slug', SLUG, '--json'], repo);
  assert.equal(reportJson.code, 0, reportJson.stderr);
  assert.deepEqual(JSON.parse(reportJson.stdout).outside_review, [
    { pr: 42, rounds: 2, findings: 3, link: 'https://example.com/pr/42', blocks: ['B1'], code_forge_usd: 0, unknown_usd_sessions: 0 },
  ]);

  // Step 12: run end.
  const end = await cf(['run', 'end', '--run', RUN], repo);
  assert.equal(end.code, 0, end.stderr);
  assert.equal(end.stdout, `run ${RUN} ended\n`);

  // Nothing reached the real ~/.code-forge or <repo>/.code-forge.
  assert.deepEqual([...snapshotGuarded()].filter((p) => !guardedBefore.has(p)), []);
  assert.equal(existsSync(path.join(realHome, '.code-forge', 'runs', RUN)), false);
});
