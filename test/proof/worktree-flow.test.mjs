// B62b (issue #13 item 4): under `proof.isolation: worktree` the gates, the reviews and the close
// check of a block run in the block's own tree. These tests use real git worktrees in temp
// repositories: in-process `gates run` and `block close` (a pinned fake worker pid), and real CLI
// subprocesses with the worker `run start` launches for `review-file` (the scripted fake `claude`
// on PATH). They pin the gate's cwd, the files seen, rows, exit codes and messages. Helpers FIRST
// (temp HOME/TMPDIR).
import { cli as cliProc, freshDir, queued, stopPid, stopPidAfter, useFake } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runGatesVerb } = await import('../../src/cli/gates.mjs');
const { runProof } = await import('../../src/cli/proof.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, writeSigned } = await import('../../src/state/run.mjs');
const { runRecordPath } = await import('../../src/state/paths.mjs');
const { liveWorker } = await import('../../src/worker/queue.mjs');
const { exec } = await import('../../src/util/exec.mjs');
const { contentHash, gitChildEnv, mainRootOf, ticketId } = await import('../../src/worker/ticket.mjs');

useFake(fileURLToPath(new URL('../worker/fake-scripted-reviewer.mjs', import.meta.url)));

const NODE = process.execPath;
const PROBE = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
const EXTERNAL = 'review:\n  external: {required: true, name: Grok}\n';
const WORKTREE = 'proof:\n  isolation: worktree\n';
const SRC = { tokens_source: null, cost_source: null };

/** @param {string[]} args @param {string} cwd @returns {Promise<string>} git's stdout; a failure throws. */
async function git(args, cwd) {
  const res = await exec(['git', '-c', 'user.name=Fake Tester', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', ...args], { cwd, env: gitChildEnv(), timeoutMs: 20000 });
  if (res.result !== 'ok') throw new Error(`git ${args.join(' ')} failed: ${res.stderr}`);
  return res.stdout;
}

/** @param {string} dir @param {string} rel @param {string} content */
function writeFile(dir, rel, content) {
  mkdirSync(path.dirname(path.join(dir, rel)), { recursive: true });
  writeFileSync(path.join(dir, rel), content);
}

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

/** @param {Function} fn @param {string[]} args @returns {Promise<[number, string, string]>} exit code, stdout, stderr */
async function cli(fn, args) {
  const stdout = capture();
  const stderr = capture();
  const code = await fn(args, { stdout, stderr, probe: PROBE, isTTY: false });
  return [code, stdout.text, stderr.text];
}

/** @param {string} slug @param {string[]} extra - config lines after the levels. */
const configYaml = (slug, extra) =>
  ['version: 1', 'project:', `  slug: ${slug}`, 'provider: anthropic', 'engine: harness', 'levels:', '  L0: {model: claude-haiku-4-5-20251001}', '  L1: {model: claude-sonnet-5}', '  L2: {model: claude-opus-5-5}', '  L3: {model: claude-fable-5-1}', ...extra, ''].join('\n');

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'the work is measured in the tree', tests: ['worktree-flow'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');

/**
 * A probe gate: writes its cwd and its argv after the script to `<dir>/seen.json`; `seen()` reads
 * and deletes it (null when the gate did not run).
 */
function probe() {
  const dir = freshDir('probe');
  const out = path.join(dir, 'seen.json');
  const script = path.join(dir, 'probe.mjs');
  writeFile(dir, 'probe.mjs', `import { writeFileSync } from 'node:fs';\nwriteFileSync(${JSON.stringify(out)}, JSON.stringify({ cwd: process.cwd(), args: process.argv.slice(2) }));\n`);
  return {
    script,
    seen: () => {
      if (!existsSync(out)) return null;
      const got = JSON.parse(readFileSync(out, 'utf8'));
      rmSync(out);
      return got;
    },
  };
}

/**
 * A committed repository (a realpath) with `.code-forge.yml` (`extra` config lines), `src/a.mjs`,
 * `test/a.test.mjs`, a README and a transcript; a bare `origin` with nothing pushed; `run start`
 * in-process with the fake worker pid 4242.
 * @param {string} slug @param {string[]} extra
 */
async function fixture(slug, extra) {
  const repo = realpathSync(freshDir('repo'));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'test/a.test.mjs', "import '../src/a.mjs';\n");
  writeFile(repo, 'README.md', '# demo\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const origin = realpathSync(freshDir('origin'));
  await git(['init', '-q', '--bare'], origin);
  await git(['remote', 'add', 'origin', origin], repo);
  const runId = `r-${slug}`;
  const [started, , startErr] = await cli(runRun, ['start', '--cwd', repo, '--run', runId, '--worker-pid', '4242']);
  assert.equal(started, 0, startErr);
  const head = (await git(['rev-parse', 'HEAD'], repo)).trim();
  /** @param {string} id @param {string[]} owned @param {string[]} [more] */
  const open = async (id, owned, more = []) => {
    const [code, out, err] = await cli(runBlock, ['open', id, '--run', runId, '--level', 'L2', '--owned', ...owned, '--acceptance', ACC, ...more]);
    assert.equal(code, 0, err);
    return out;
  };
  /** @param {string} id @param {string[]} [more] */
  const close = (id, more = []) => cli(runBlock, ['close', id, '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...more]);
  /** @param {string} id @param {string[]} [more] */
  const gate = async (id, more = []) => {
    const [code, out, err] = await cli(runGatesVerb, ['run', '--slug', slug, '--run', runId, '--block', id, ...more]);
    return { code, json: out ? JSON.parse(out) : null, err };
  };
  const rows = async () => readAllRows(slug);
  const gateRows = async () => (await rows()).filter((r) => r.event.startsWith('gate.r') || r.event === 'gate.done').map(({ mac: _m, ts: _t, ...r }) => r);
  return { repo, origin, runId, head, open, close, gate, rows, gateRows, tree: (/** @type {string} */ id) => `${repo}-worktrees/${id}` };
}

/** YAML lines for a `test` gate on the probe, the other gates off. @param {string} script @param {string[]} [argv] */
const probeGate = (script, argv = []) => ['gates:', `  test: [${[NODE, script, ...argv].map((a) => JSON.stringify(a)).join(', ')}]`, '  lint: null', '  types: null', '  format: null'];

/**
 * What must never move in the main tree, and on the remote: HEAD, the checked-out branch, the index,
 * every branch tip, and the remote's refs.
 * @param {string} repo @param {string} origin
 */
async function untouched(repo, origin) {
  return {
    head: (await git(['rev-parse', 'HEAD'], repo)).trim(),
    branch: (await git(['symbolic-ref', 'HEAD'], repo)).trim(),
    index: await git(['ls-files', '--stage'], repo),
    refs: await git(['for-each-ref', '--format=%(refname) %(objectname)', 'refs/heads'], repo),
    remote: await git(['for-each-ref', '--format=%(refname) %(objectname)'], origin),
  };
}

/**
 * @param {string} repo @param {string} tree
 * @returns {Promise<string | null>} the branch ref `tree` is registered on in `git worktree list`
 *   (null when it is not listed); it must also be what HEAD in the tree names.
 */
async function onBranch(repo, tree) {
  const records = (await git(['worktree', 'list', '--porcelain', '-z'], repo)).split('\0\0').map((r) => r.split('\0'));
  const mine = records.find((r) => r[0] === `worktree ${tree}`);
  if (!mine) return null;
  const listed = mine.find((l) => l.startsWith('branch '))?.slice('branch '.length) ?? null;
  assert.equal((await git(['symbolic-ref', 'HEAD'], tree)).trim(), listed);
  return listed;
}

/**
 * Edit the run record's blocks in place (a hand-edited or damaged record).
 * @param {string} runId @param {(blocks: Record<string, any>) => void} change
 */
async function editRecord(runId, change) {
  const record = await readRun(runId);
  change(record.blocks);
  writeFileSync(runRecordPath(runId), JSON.stringify(record));
}

// ── clause 1: gates run in the tree ─────────────────────────────────────────

test('B62b (1): gates run --run --block on a worktree block runs in the tree (cwd = the tree), and the JSON output and the gate.done row name the tree', async () => {
  const p = probe();
  const fx = await fixture('b62b-gates', [WORKTREE.trimEnd(), ...probeGate(p.script)]);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  const { code, json, err } = await fx.gate('B1');
  assert.equal(code, 0, err);
  assert.deepEqual(p.seen(), { cwd: tree, args: [] });
  assert.equal(json.worktree, tree);
  assert.equal(json.allOk, true);
  // --cwd naming the tree itself is the same run
  const again = await fx.gate('B1', ['--cwd', tree]);
  assert.equal(again.code, 0, again.err);
  assert.deepEqual(p.seen(), { cwd: tree, args: [] });
  assert.deepEqual(await fx.gateRows(), [
    { run: fx.runId, block: 'B1', worktree: tree, event: 'gate.done', stack: json.stack, all_ok: true, ...SRC },
    { run: fx.runId, block: 'B1', worktree: tree, event: 'gate.done', stack: json.stack, all_ok: true, ...SRC },
  ]);
  // the tree is still registered, on the block's branch
  assert.deepEqual(await onBranch(fx.repo, tree), `refs/heads/forge/${fx.runId}/B1`);
});

test('B62b (1): a worktree that is gone or malformed refuses gates run — exit 1, a message naming the block, the run and the tree, no gate run, no row', async () => {
  const p = probe();
  const fx = await fixture('b62b-gates-gone', [WORKTREE.trimEnd(), ...probeGate(p.script)]);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  const saved = (await readRun(fx.runId)).blocks.B1.worktree;
  await editRecord(fx.runId, (b) => void (b.B1.worktree = { ...saved, base_sha: '' }));
  assert.deepEqual(await fx.gate('B1'), { code: 1, json: null, err: `gates run: block B1 of run ${fx.runId}: malformed worktree record (it needs an absolute path, a branch and a base_sha); no gate was run\n` });
  await editRecord(fx.runId, (b) => void (b.B1.worktree = saved));
  rmSync(tree, { recursive: true, force: true });
  assert.deepEqual(await fx.gate('B1'), { code: 1, json: null, err: `gates run: block B1 of run ${fx.runId}: the worktree ${tree} is gone (removed or moved); no gate was run\n` });
  assert.equal(p.seen(), null);
  assert.deepEqual(await fx.gateRows(), []);
});

test('B62b (1): a red gate in the tree writes gate.red naming the tree; a block without a worktree (export) runs in --cwd and its rows carry no worktree', async () => {
  const red = path.join(freshDir('red'), 'red.mjs');
  writeFile(path.dirname(red), 'red.mjs', 'process.exit(process.cwd().endsWith("-worktrees/B1") ? 4 : 0);\n');
  const fx = await fixture('b62b-red', [WORKTREE.trimEnd(), ...probeGate(red)]);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  const { code, json } = await fx.gate('B1');
  assert.deepEqual([code, json.allOk, json.results[0].code, json.worktree], [1, false, 4, tree]);
  assert.deepEqual(await fx.gateRows(), [
    { run: fx.runId, block: 'B1', worktree: tree, event: 'gate.red', gate: 'test', code: 4, ...SRC },
    { run: fx.runId, block: 'B1', worktree: tree, event: 'gate.done', stack: json.stack, all_ok: false, ...SRC },
  ]);

  const p = probe();
  const ex = await fixture('b62b-export', probeGate(p.script));
  await ex.open('B1', ['src/a.mjs']);
  const plain = await ex.gate('B1', ['--cwd', ex.repo]);
  assert.equal(plain.code, 0, plain.err);
  assert.deepEqual(p.seen(), { cwd: ex.repo, args: [] });
  assert.equal(Object.hasOwn(plain.json, 'worktree'), false);
  assert.deepEqual(await ex.gateRows(), [{ run: ex.runId, block: 'B1', event: 'gate.done', stack: plain.json.stack, all_ok: true, ...SRC }]);
});

test('B62b (1): a --cwd other than the tree, and any --cwd=<dir>, is refused for a worktree block — exit 2, a message naming the block and the tree, no gate run, no row', async () => {
  const p = probe();
  const fx = await fixture('b62b-cwd', [WORKTREE.trimEnd(), ...probeGate(p.script)]);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  for (const other of [fx.repo, freshDir('elsewhere')]) {
    const { code, json, err } = await fx.gate('B1', ['--cwd', other]);
    assert.deepEqual([code, json, err], [2, null, `gates run: block B1 of run ${fx.runId} works in its worktree ${tree}, not in --cwd ${other} — drop --cwd (or give the worktree); no gate was run\n`]);
  }
  // `--cwd=<dir>` is refused for a worktree block, even when it names the tree itself
  for (const joined of [`--cwd=${fx.repo}`, `--cwd=${tree}`]) {
    const { code, json, err } = await fx.gate('B1', [joined]);
    assert.deepEqual([code, json, err], [2, null, `gates run: block B1 of run ${fx.runId} works in its worktree ${tree}, not in ${joined} — drop it (--cwd=<dir> is not read as --cwd); no gate was run\n`]);
  }
  assert.equal(p.seen(), null);
  assert.deepEqual(await fx.gateRows(), []);
});

test('B62b (1): a tree checked out on another branch, or a plain directory at its path, is refused by gates run, block close and proof — exit 1, the exact message, nothing run or recorded', async () => {
  const p = probe();
  const fx = await fixture('b62b-offbranch', [WORKTREE.trimEnd(), EXTERNAL.trimEnd(), ...probeGate(p.script)]);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  const branch = `forge/${fx.runId}/B1`;
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  await git(['checkout', '-q', '-b', 'other'], tree);
  const before = (await fx.rows()).length;
  const off = `the worktree ${tree} is not on its branch ${branch} (it is on other)`;
  assert.deepEqual(await fx.gate('B1'), { code: 1, json: null, err: `gates run: block B1 of run ${fx.runId}: ${off}; no gate was run\n` });
  assert.deepEqual(await fx.close('B1'), [1, '', `block close: block B1: ${off}, so its work cannot be measured; nothing was recorded\n`]);
  assert.deepEqual(await cli(runProof, ['export', 'B1', '--run', fx.runId]), [1, '', `proof export: block B1 of run ${fx.runId}: ${off}; no proof was run\n`]);
  assert.equal(p.seen(), null);
  assert.equal((await fx.rows()).length, before);
  assert.equal((await readRun(fx.runId)).blocks.B1.status, 'open');
  // back on its branch: the gates run there again
  await git(['checkout', '-q', branch], tree);
  assert.equal((await fx.gate('B1')).code, 0);
  assert.deepEqual(p.seen(), { cwd: tree, args: [] });
  // a plain directory where the tree was (removed by hand, then made again by something else)
  await git(['worktree', 'remove', '--force', tree], fx.repo);
  mkdirSync(path.join(tree, 'src'), { recursive: true });
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  const plain = `the worktree ${tree} is not on its branch ${branch} (its HEAD is detached, or it is not a git worktree)`;
  assert.deepEqual(await fx.gate('B1'), { code: 1, json: null, err: `gates run: block B1 of run ${fx.runId}: ${plain}; no gate was run\n` });
  assert.deepEqual(await fx.close('B1'), [1, '', `block close: block B1: ${plain}, so its work cannot be measured; nothing was recorded\n`]);
  assert.equal(p.seen(), null);
  assert.equal((await readRun(fx.runId)).blocks.B1.status, 'open');
});

test('B62b (1): a recorded base_sha that is not a full hex id (a dash-prefixed one, a short one) or a branch with a newline is malformed — gates run and block close refuse it before any git call uses it', async () => {
  const p = probe();
  const fx = await fixture('b62b-dash', [WORKTREE.trimEnd(), EXTERNAL.trimEnd(), ...probeGate(p.script)]);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  const saved = (await readRun(fx.runId)).blocks.B1.worktree;
  const malformed = 'malformed worktree record (it needs an absolute path, a branch and a base_sha)';
  for (const worktree of [{ ...saved, base_sha: '--output=x' }, { ...saved, base_sha: fx.head.slice(0, 7) }, { ...saved, branch: `${saved.branch}\nmain` }]) {
    await editRecord(fx.runId, (b) => void (b.B1.worktree = worktree));
    assert.deepEqual(await fx.gate('B1'), { code: 1, json: null, err: `gates run: block B1 of run ${fx.runId}: ${malformed}; no gate was run\n` });
    assert.deepEqual(await fx.close('B1'), [1, '', `block close: block B1: ${malformed}, so its work cannot be measured; nothing was recorded\n`]);
  }
  // `--output=x` never reached git as an option: no file x anywhere git could have written it
  for (const dir of [tree, fx.repo, process.cwd()]) assert.equal(existsSync(path.join(dir, 'x')), false, dir);
  assert.equal(p.seen(), null);
  assert.equal((await readRun(fx.runId)).blocks.B1.status, 'open');
  await editRecord(fx.runId, (b) => void (b.B1.worktree = saved));
  // no gate run passed (each one above was refused), so close warns once and still closes
  assert.deepEqual(await fx.close('B1'), [0, 'block B1 closed\n', `WARN block B1: no green gates run for this block; run gates run --run ${fx.runId} --block B1 before close\n`]);
});

test('B62b (1): with the workspace in a subdirectory, --cwd must name that subdirectory of the tree — the tree\'s top level is refused with exit 2, no gate run, no row', async () => {
  const p = probe();
  const repo = realpathSync(freshDir('b62b-sub'));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'app/.code-forge.yml', configYaml('b62b-sub', [WORKTREE.trimEnd(), ...probeGate(p.script)]));
  writeFile(repo, 'app/src/a.mjs', 'export const a = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const runId = 'r-b62b-sub';
  const [started, , startErr] = await cli(runRun, ['start', '--cwd', path.join(repo, 'app'), '--run', runId, '--worker-pid', '4242']);
  assert.equal(started, 0, startErr);
  const [opened, , openErr] = await cli(runBlock, ['open', 'B1', '--run', runId, '--level', 'L2', '--owned', 'src/a.mjs', '--acceptance', ACC]);
  assert.equal(opened, 0, openErr);
  const tree = `${repo}-worktrees/B1`;
  const place = path.join(tree, 'app');
  const gate = async (/** @type {string[]} */ more) => {
    const [code, out, err] = await cli(runGatesVerb, ['run', '--slug', 'b62b-sub', '--run', runId, '--block', 'B1', ...more]);
    return { code, json: out ? JSON.parse(out) : null, err };
  };
  assert.deepEqual(await gate(['--cwd', tree]), { code: 2, json: null, err: `gates run: block B1 of run ${runId} works in its worktree ${place}, not in --cwd ${tree} — drop --cwd (or give the worktree); no gate was run\n` });
  assert.equal(p.seen(), null);
  assert.deepEqual((await readAllRows('b62b-sub')).filter((r) => r.event.startsWith('gate.')), []);
  // --cwd at the workspace's place in the tree, or no --cwd: the gates run there
  for (const more of [['--cwd', place], []]) {
    const ran = await gate(more);
    assert.equal(ran.code, 0, ran.err);
    assert.deepEqual(p.seen(), { cwd: place, args: [] });
  }
});

test('B62b (1): {test_files} and the full-suite threshold count the files in the tree — files added only in the main tree never count', async () => {
  const p = probe();
  const fx = await fixture('b62b-scope', [WORKTREE.trimEnd(), 'gates:', `  test: [${[NODE, p.script, '--run', '{test_files}'].map((a) => JSON.stringify(a)).join(', ')}]`, '  lint: null', '  types: null', '  format: null', '  full_suite_threshold_files: 2']);
  await fx.open('B1', ['src/**', 'test/a.test.mjs']);
  const tree = fx.tree('B1');
  // src/a.mjs and test/a.test.mjs: 2 owned files, not over the threshold — even with two more in the main tree
  writeFile(fx.repo, 'src/b.mjs', 'export const b = 1;\n');
  writeFile(fx.repo, 'src/c.mjs', 'export const c = 1;\n');
  const related = await fx.gate('B1');
  assert.equal(related.code, 0, related.err);
  assert.deepEqual(p.seen(), { cwd: tree, args: ['--run', 'test/a.test.mjs'] });
  assert.deepEqual([related.json.results[0].scope, related.json.results[0].test_count], ['related', 1]);
  // one new file in the tree: 3 owned files there, over the threshold ⇒ the full suite
  writeFile(tree, 'src/b.mjs', 'export const b = 1;\n');
  const full = await fx.gate('B1');
  assert.equal(full.code, 0, full.err);
  assert.deepEqual(p.seen(), { cwd: tree, args: ['--run'] });
  assert.deepEqual([full.json.results[0].scope, full.json.results[0].reason], ['full', 'threshold']);
  assert.deepEqual((await fx.gateRows()).map((r) => [r.event, r.worktree, r.scope, r.test_count ?? r.reason]), [
    ['gate.done', tree, 'related', 1],
    ['gate.done', tree, 'full', 'threshold'],
  ]);
});

// ── clause 3: block close measures the tree ─────────────────────────────────

test('B62b (3): block close measures the tree — an owned change only in the main tree is no_changes; committed and uncommitted changes in the tree close it', async () => {
  const fx = await fixture('b62b-close', [WORKTREE.trimEnd(), EXTERNAL.trimEnd()]);
  await fx.open('B1', ['src/a.mjs', 'src/n.mjs']);
  const tree = fx.tree('B1');
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.close('B1'), [1, '', 'block B1 open: no_changes\n']);
  // committed on the block's branch counts
  writeFile(tree, 'src/a.mjs', 'export const a = 3;\n');
  await git(['commit', '-q', '-am', 'tree work'], tree);
  // and so does an uncommitted new owned file; the main tree's own edit still does not
  writeFile(tree, 'src/n.mjs', 'export const n = 1;\n');
  assert.deepEqual(await fx.close('B1'), [0, 'block B1 closed\n', '']);
  assert.equal((await readRun(fx.runId)).blocks.B1.status, 'closed');
});

test('B62b (3): the file set is the tree\'s, committed and uncommitted — the review check names exactly those files', async () => {
  const fx = await fixture('b62b-files', [WORKTREE.trimEnd()]);
  await fx.open('B1', ['src/**']);
  const tree = fx.tree('B1');
  writeFile(tree, 'src/a.mjs', 'export const a = 3;\n');
  await git(['commit', '-q', '-am', 'tree work'], tree);
  writeFile(tree, 'src/n.mjs', 'export const n = 1;\n');
  writeFile(fx.repo, 'src/main-only.mjs', 'export const m = 1;\n');
  // no review.external here: each changed file needs a review, so the refusal lists the file set
  assert.deepEqual(await fx.close('B1'), [1, '', 'block B1 open: unreviewed src/a.mjs; unreviewed src/n.mjs\n']);
});

test('B62b (3): an unowned change in the tree is refused; one in the main tree or in another block\'s tree never counts', async () => {
  const fx = await fixture('b62b-unowned', [WORKTREE.trimEnd(), EXTERNAL.trimEnd()]);
  // README.md is already dirty in the main tree when B1 opens: that excuses it in the main tree,
  // never in B1's fresh tree, even with the very same bytes there
  writeFile(fx.repo, 'README.md', '# changed in main\n');
  await fx.open('B1', ['src/a.mjs']);
  await fx.open('B2', ['src/b.mjs']);
  const tree = fx.tree('B1');
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  // the main tree and B2's tree: an unowned new file and an edit to a file nobody owns
  writeFile(fx.repo, 'stray.txt', 'main\n');
  writeFile(fx.tree('B2'), 'other.txt', 'b2\n');
  writeFile(fx.tree('B2'), 'README.md', '# changed in B2\n');
  // B1's tree: the same README edit and a new unowned file
  writeFile(tree, 'README.md', '# changed in main\n');
  writeFile(tree, 'notes.txt', 'b1\n');
  assert.deepEqual(await fx.close('B1'), [
    1,
    '',
    'block B1 open: unowned_change README.md; unowned_change notes.txt\n',
  ]);
  rmSync(path.join(tree, 'notes.txt'));
  await git(['checkout', '--', 'README.md'], tree);
  assert.deepEqual(await fx.close('B1'), [0, 'block B1 closed\n', '']);
});

test('B62b (3): in a block\'s tree a file a sibling block owns is unowned too — no sibling exemption; the close is refused with exit 1 naming it', async () => {
  const fx = await fixture('b62b-sibling', [WORKTREE.trimEnd(), EXTERNAL.trimEnd()]);
  await fx.open('B1', ['src/a.mjs']);
  await fx.open('B2', ['src/x.mjs']);
  const tree = fx.tree('B1');
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(tree, 'src/x.mjs', 'export const x = 1;\n');
  assert.deepEqual(await fx.close('B1'), [1, '', 'block B1 open: unowned_change src/x.mjs\n']);
  rmSync(path.join(tree, 'src/x.mjs'));
  assert.deepEqual(await fx.close('B1'), [0, 'block B1 closed\n', '']);
});

test('B62b (3): in a block\'s tree a closed block\'s signed approval never excuses a file — the same approved bytes changed in the tree are unowned', async () => {
  const fx = await fixture('b62b-closedok', [WORKTREE.trimEnd(), EXTERNAL.trimEnd()]);
  await fx.open('B0', ['src/x.mjs']);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(tree, 'src/x.mjs', 'export const x = 1;\n');
  // B0 is closed, and its signed approval is for exactly the bytes src/x.mjs has in B1's tree
  await editRecord(fx.runId, (b) => void (b.B0.status = 'closed'));
  const hash = contentHash(tree, 'src/x.mjs');
  await writeSigned(fx.runId, (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'b62b-closedok' }), { event: 'review.approved', block: 'B0', file: 'src/x.mjs', content_hash: hash, round: 1 });
  assert.deepEqual(
    (await fx.rows()).filter((r) => r.event === 'review.approved').map((r) => [r.block, r.file, r.content_hash]),
    [['B0', 'src/x.mjs', hash]],
  );
  assert.deepEqual(await fx.close('B1'), [1, '', 'block B1 open: unowned_change src/x.mjs\n']);
  rmSync(path.join(tree, 'src/x.mjs'));
  assert.deepEqual(await fx.close('B1'), [0, 'block B1 closed\n', '']);
});

test('B62b (3): a tree removed by hand refuses the close before anything is written — exit 1, the block, the tree, the branch and what to do', async () => {
  const fx = await fixture('b62b-gone', [WORKTREE.trimEnd(), EXTERNAL.trimEnd()]);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  rmSync(tree, { recursive: true, force: true });
  const before = (await fx.rows()).length;
  assert.deepEqual(await fx.close('B1'), [
    1,
    '',
    `block close: block B1: its worktree ${tree} is gone (removed or moved), so its work cannot be measured — restore it on branch forge/${fx.runId}/B1 (git worktree prune, then git worktree add ${tree} forge/${fx.runId}/B1) and close again; nothing was recorded\n`,
  ]);
  assert.equal((await fx.rows()).length, before);
  assert.equal((await readRun(fx.runId)).blocks.B1.status, 'open');
});

// ── clause 4: nothing is pushed, merged or checked out ──────────────────────

test('B62b (4): gates, close and --remove-worktree leave the main tree\'s HEAD, branch, index and branch tips unchanged and push nothing; the work stays on the block\'s branch', async () => {
  const p = probe();
  const fx = await fixture('b62b-still', [WORKTREE.trimEnd(), EXTERNAL.trimEnd(), ...probeGate(p.script)]);
  await fx.open('B1', ['src/a.mjs']);
  const tree = fx.tree('B1');
  const branch = `forge/${fx.runId}/B1`;
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  await git(['commit', '-q', '-am', 'tree work'], tree);
  const work = (await git(['rev-parse', 'HEAD'], tree)).trim();
  // a staged change in the main index must survive untouched
  writeFile(fx.repo, 'README.md', '# staged in main\n');
  await git(['add', 'README.md'], fx.repo);
  const before = await untouched(fx.repo, fx.origin);
  assert.equal((await fx.gate('B1')).code, 0);
  assert.equal(await onBranch(fx.repo, tree), `refs/heads/${branch}`);
  assert.deepEqual(await fx.close('B1', ['--remove-worktree']), [0, `block B1 closed\nblock B1 worktree removed: ${tree} (branch ${branch} kept)\n`, '']);
  const after = await untouched(fx.repo, fx.origin);
  assert.deepEqual(after, before);
  assert.equal(before.head, fx.head);
  assert.equal(before.branch, 'refs/heads/main');
  assert.equal(before.remote, '');
  assert.ok(before.refs.includes(`refs/heads/${branch} ${work}\n`), before.refs);
  assert.equal(readFileSync(path.join(fx.repo, 'src/a.mjs'), 'utf8'), 'export const a = 1;\n');
  assert.equal(await git(['diff', '--cached', '--name-only'], fx.repo), 'README.md\n');
});

// ── clause 2: review-file and the worker read the tree ──────────────────────

const LINES = Array.from({ length: 20 }, (_, k) => `export const v${k + 1} = ${k + 1};\n`).join('');

/**
 * A repository with a committed 20-line `src/a.mjs` under worktree isolation, a run whose worker
 * `run start` launched (the scripted fake reviewer on PATH), and block B1 open on the file with
 * its tree. `body` gets the paths and the env; the worker is stopped afterwards.
 * @param {string} name
 * @param {(fx: {repo: string, origin: string, tree: string, runId: string, env: Record<string, string>, records: string}) => Promise<void>} body
 */
async function withWorker(name, body) {
  const repo = realpathSync(freshDir(`${name}-repo`));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, '.code-forge.yml', ['version: 1', 'project:', `  slug: ${name}`, 'provider: anthropic', 'levels:', '  L0:', '    model: claude-haiku-4-5-20251001', '  L1:', '    model: claude-sonnet-5', '  L2:', '    model: claude-opus-5-5', '  L3:', '    model: claude-fable-5-1', 'review:', '  session_timeout_s: 60', '  min_tokens_out: 40', WORKTREE].join('\n'));
  writeFile(repo, 'src/a.mjs', LINES);
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const origin = realpathSync(freshDir(`${name}-origin`));
  await git(['init', '-q', '--bare'], origin);
  await git(['remote', 'add', 'origin', origin], repo);
  const runId = `r-${name}-${process.pid}`;
  const records = freshDir(`${name}-records`);
  const scriptFile = path.join(freshDir(`${name}-script`), 'script.json');
  writeFileSync(scriptFile, '{}');
  const env = { FAKE_RECORD: records, FAKE_SCRIPT: scriptFile };
  const started = await cliProc(['run', 'start', '--run', runId], repo, env);
  assert.equal(started.code, 0, started.stderr);
  const live = liveWorker(repo);
  assert.ok(live, 'the worker announced itself in the main tree');
  stopPidAfter(live.pid, runId);
  try {
    const opened = await cliProc(['block', 'open', 'B1', '--run', runId, '--level', 'L2', '--owned', 'src/a.mjs', '--acceptance', ACC], repo, env);
    assert.equal(opened.code, 0, opened.stderr);
    const tree = `${repo}-worktrees/B1`;
    assert.equal((await readRun(runId)).blocks.B1.worktree.path, tree);
    await body({ repo, origin, tree, runId, env, records });
  } finally {
    await stopPid(live.pid, runId);
  }
}

/** @param {string} root @param {string} file @returns {string} the sha256 of the file's bytes. */
const sha = (root, file) => createHash('sha256').update(readFileSync(path.join(root, file))).digest('hex');

test('B62b (2): review-file in the tree queues in the main tree, hashes the tree\'s content and names the tree; the worker reviews exactly that content and the packet names the tree', async () => {
  await withWorker('b62b-review', async ({ repo, origin, tree, runId, env, records }) => {
    const lines = LINES.split('\n');
    lines[9] = 'export const v10 = "tree";';
    writeFileSync(path.join(tree, 'src/a.mjs'), lines.join('\n'));
    const main = LINES.split('\n');
    main[2] = 'export const v3 = "main-only";';
    writeFileSync(path.join(repo, 'src/a.mjs'), main.join('\n'));
    const before = await untouched(repo, origin);

    const enq = await cliProc(['review-file', 'src/a.mjs', '--block', 'B1'], tree, env);
    assert.equal(enq.code, 0, enq.stdout + enq.stderr);
    const hash = sha(tree, 'src/a.mjs');
    assert.deepEqual(enq.json, { ticket: enq.json.ticket, status: 'queued', file: 'src/a.mjs', content_hash: hash, tree });
    // the ticket is in the main tree's queue (where the worker is), naming the tree; none in the tree
    const ticket = JSON.parse(readFileSync(path.join(repo, '.code-forge', 'queue', `${enq.json.ticket}.json`), 'utf8'));
    assert.deepEqual({ ...ticket, enqueued_at: 'x' }, { ticket: enq.json.ticket, run: runId, block: 'B1', file: 'src/a.mjs', content_hash: hash, enqueued_at: 'x', tree });
    assert.equal(existsSync(path.join(tree, '.code-forge')), false);

    const waited = await cliProc(['review-file', '--wait', enq.json.ticket, '--max', '30s'], tree, env);
    assert.equal(waited.json?.status, 'done', waited.stdout);
    assert.deepEqual([waited.json.result.status, waited.json.result.approved, waited.json.result.content_hash], ['reviewed', true, hash]);
    const packets = readdirSync(records).filter((n) => n.startsWith('packet-')).map((n) => readFileSync(path.join(records, n), 'utf8'));
    assert.equal(packets.length, 1);
    assert.ok(packets[0].includes(`\nWorktree: the file is reviewed as it is in the block's worktree ${tree} (branch forge/${runId}/B1), against the block base.\n`), packets[0]);
    assert.ok(packets[0].includes('+export const v10 = "tree";'), packets[0]);
    assert.equal(packets[0].includes('main-only'), false);
    // the signed approval names the tree's content
    const approved = (await readAllRows('b62b-review')).filter((r) => r.event === 'review.approved');
    assert.deepEqual(approved.map((r) => [r.block, r.file, r.content_hash]), [['B1', 'src/a.mjs', hash]]);
    // the review wrote nothing in the tree: only the coder's change, and the tree is on its branch
    assert.equal(await git(['status', '--porcelain'], tree), ' M src/a.mjs\n');
    assert.equal(await onBranch(repo, tree), `refs/heads/forge/${runId}/B1`);

    // from the main tree, the same file of the same block is the tree's content: the same ticket, done
    const fromMain = await cliProc(['review-file', 'src/a.mjs', '--block', 'B1'], repo, env);
    assert.deepEqual(fromMain.json, { ticket: enq.json.ticket, status: 'done', file: 'src/a.mjs', content_hash: hash, tree });
    assert.notEqual(sha(repo, 'src/a.mjs'), hash);
    assert.deepEqual(await untouched(repo, origin), before);
  });
});

test('B62b (2): a change made only in the main tree is never seen — the tree\'s unchanged file is queued at its base content and reviewed as no change', async () => {
  await withWorker('b62b-mainonly', async ({ repo, tree, env, records }) => {
    const main = LINES.split('\n');
    main[2] = 'export const v3 = "main-only";';
    writeFileSync(path.join(repo, 'src/a.mjs'), main.join('\n'));
    const enq = await cliProc(['review-file', 'src/a.mjs', '--block', 'B1'], repo, env);
    assert.equal(enq.code, 0, enq.stdout + enq.stderr);
    assert.deepEqual([enq.json.content_hash, enq.json.tree], [sha(tree, 'src/a.mjs'), tree]);
    const waited = await cliProc(['review-file', '--wait', enq.json.ticket, '--max', '30s'], repo, env);
    assert.equal(waited.json?.status, 'done', waited.stdout);
    assert.equal(waited.json.result.status, 'no_change');
    const packets = readdirSync(records).filter((n) => n.startsWith('packet-')).map((n) => readFileSync(path.join(records, n), 'utf8'));
    assert.equal(packets.length, 0);
  });
});

test('B62b (2): review-file for a block whose recorded tree is gone is refused — exit 1, the reason and the exact message, nothing queued', async () => {
  await withWorker('b62b-rfgone', async ({ repo, tree, env }) => {
    rmSync(tree, { recursive: true, force: true });
    const refused = await cliProc(['review-file', 'src/a.mjs', '--block', 'B1'], repo, env);
    assert.deepEqual([refused.code, refused.json, refused.stderr], [1, { status: 'refused', reason: 'worktree-missing', message: `block B1: the worktree ${tree} is gone (removed or moved); nothing was queued` }, '']);
    assert.deepEqual(queued(repo, 'json'), []);
  });
});

test('B62b (2): review-file refuses a tree off its branch and a run record that cannot be parsed — exit 1, the reason and the exact message, nothing queued', async () => {
  await withWorker('b62b-rfoff', async ({ repo, tree, runId, env }) => {
    await git(['checkout', '-q', '-b', 'other'], tree);
    const off = await cliProc(['review-file', 'src/a.mjs', '--block', 'B1'], tree, env);
    assert.deepEqual([off.code, off.json, off.stderr], [1, { status: 'refused', reason: 'worktree-branch-mismatch', message: `block B1: the worktree ${tree} is not on its branch forge/${runId}/B1 (it is on other); nothing was queued` }, '']);
    await git(['checkout', '-q', `forge/${runId}/B1`], tree);
    const saved = readFileSync(runRecordPath(runId), 'utf8');
    writeFileSync(runRecordPath(runId), '{"run_id": ');
    try {
      const corrupt = await cliProc(['review-file', 'src/a.mjs', '--block', 'B1'], tree, env);
      assert.deepEqual([corrupt.code, corrupt.json, corrupt.stderr], [1, { status: 'refused', reason: 'run-record-unreadable', message: `the run record of ${runId} cannot be read (corrupt or not a record); nothing was queued` }, '']);
    } finally {
      writeFileSync(runRecordPath(runId), saved);
    }
    assert.deepEqual(queued(repo, 'json'), []);
  });
});

test('B62b (2): the review of a tree block diffs the tree against its base_sha — a change committed on the block\'s branch and one left uncommitted are both in the packet, and the hash is the tree\'s', async () => {
  await withWorker('b62b-commit', async ({ repo, tree, env, records }) => {
    const lines = LINES.split('\n');
    lines[4] = 'export const v5 = "committed";';
    writeFileSync(path.join(tree, 'src/a.mjs'), lines.join('\n'));
    await git(['commit', '-q', '-am', 'tree work'], tree);
    lines[9] = 'export const v10 = "uncommitted";';
    writeFileSync(path.join(tree, 'src/a.mjs'), lines.join('\n'));
    const hash = sha(tree, 'src/a.mjs');
    assert.notEqual(sha(repo, 'src/a.mjs'), hash);
    const enq = await cliProc(['review-file', 'src/a.mjs', '--block', 'B1'], tree, env);
    assert.equal(enq.code, 0, enq.stdout + enq.stderr);
    assert.deepEqual([enq.json.content_hash, enq.json.tree], [hash, tree]);
    const waited = await cliProc(['review-file', '--wait', enq.json.ticket, '--max', '30s'], tree, env);
    assert.equal(waited.json?.status, 'done', waited.stdout);
    assert.deepEqual([waited.json.result.status, waited.json.result.content_hash], ['reviewed', hash]);
    const packets = readdirSync(records).filter((n) => n.startsWith('packet-')).map((n) => readFileSync(path.join(records, n), 'utf8'));
    assert.equal(packets.length, 1);
    assert.ok(packets[0].includes('+export const v5 = "committed";'), packets[0]);
    assert.ok(packets[0].includes('+export const v10 = "uncommitted";'), packets[0]);
  });
});

test('B62b (2): a ticket that names a tree other than the block\'s recorded one is never reviewed — unavailable, worktree-mismatch, no packet', async () => {
  await withWorker('b62b-mismatch', async ({ repo, tree, runId, env, records }) => {
    const lines = LINES.split('\n');
    lines[9] = 'export const v10 = "tree";';
    writeFileSync(path.join(tree, 'src/a.mjs'), lines.join('\n'));
    // a hand-made ticket with a valid id for the tree's bytes, but naming the main tree
    const body = { run: runId, block: 'B1', file: 'src/a.mjs', content_hash: sha(tree, 'src/a.mjs'), tree: repo };
    const id = ticketId(body);
    writeFile(repo, `.code-forge/queue/${id}.json`, JSON.stringify({ ticket: id, ...body, enqueued_at: new Date().toISOString() }));
    const waited = await cliProc(['review-file', '--wait', id, '--max', '30s'], repo, env);
    assert.equal(waited.json?.status, 'done', waited.stdout);
    assert.deepEqual([waited.json.result.status, waited.json.result.reason, waited.json.result.approved], ['unavailable', 'worktree-mismatch', false]);
    assert.deepEqual(readdirSync(records).filter((n) => n.startsWith('packet-')), []);
  });
});

test('B62b (2): a ticket with no tree for a block the record gives a worktree is never reviewed — unavailable, worktree-mismatch, no packet', async () => {
  await withWorker('b62b-notree', async ({ repo, runId, env, records }) => {
    const main = LINES.split('\n');
    main[2] = 'export const v3 = "main-only";';
    writeFileSync(path.join(repo, 'src/a.mjs'), main.join('\n'));
    // a hand-made ticket for the main tree's bytes that names no tree at all
    const body = { run: runId, block: 'B1', file: 'src/a.mjs', content_hash: sha(repo, 'src/a.mjs') };
    const id = ticketId(body);
    writeFile(repo, `.code-forge/queue/${id}.json`, JSON.stringify({ ticket: id, ...body, enqueued_at: new Date().toISOString() }));
    const waited = await cliProc(['review-file', '--wait', id, '--max', '30s'], repo, env);
    assert.equal(waited.json?.status, 'done', waited.stdout);
    assert.deepEqual([waited.json.result.status, waited.json.result.reason, waited.json.result.approved], ['unavailable', 'worktree-mismatch', false]);
    assert.deepEqual(readdirSync(records).filter((n) => n.startsWith('packet-')), []);
  });
});

test('B62b (2): review-file for a block the record gives no worktree hashes the main tree even when called inside another block\'s tree — the ticket names no tree', async () => {
  await withWorker('b62b-plain', async ({ repo, tree, runId, env }) => {
    const opened = await cliProc(['block', 'open', 'B2', '--run', runId, '--level', 'L2', '--owned', 'src/b.mjs', '--acceptance', ACC], repo, env);
    assert.equal(opened.code, 0, opened.stderr);
    await editRecord(runId, (blocks) => { delete blocks.B2.worktree; });
    writeFile(repo, 'src/b.mjs', 'export const b = "main";\n');
    writeFile(tree, 'src/b.mjs', 'export const b = "in-B1-tree";\n');
    const hash = sha(repo, 'src/b.mjs');
    assert.notEqual(sha(tree, 'src/b.mjs'), hash);
    const enq = await cliProc(['review-file', 'src/b.mjs', '--block', 'B2'], tree, env);
    assert.equal(enq.code, 0, enq.stdout + enq.stderr);
    assert.deepEqual(enq.json, { ticket: enq.json.ticket, status: 'queued', file: 'src/b.mjs', content_hash: hash });
    const ticket = JSON.parse(readFileSync(path.join(repo, '.code-forge', 'queue', `${enq.json.ticket}.json`), 'utf8'));
    assert.deepEqual({ ...ticket, enqueued_at: 'x' }, { ticket: enq.json.ticket, run: runId, block: 'B2', file: 'src/b.mjs', content_hash: hash, enqueued_at: 'x' });
  });
});

test('B62b (2): the queue lives in the main working tree — a repository whose main entry is bare has none, and mainRootOf refuses it with bare-main', async () => {
  const work = realpathSync(freshDir('b62b-bare-src'));
  await git(['init', '-q', '-b', 'main'], work);
  writeFile(work, 'a.txt', 'a\n');
  await git(['add', '-A'], work);
  await git(['commit', '-q', '-m', 'base'], work);
  const bare = path.join(realpathSync(freshDir('b62b-bare')), 'repo.git');
  await git(['clone', '-q', '--bare', work, bare], work);
  const linked = path.join(path.dirname(bare), 'linked');
  await git(['worktree', 'add', '-q', '-b', 'side', linked, 'main'], bare);
  await assert.rejects(mainRootOf(linked), (err) => {
    assert.deepEqual([/** @type {any} */ (err).code, /** @type {Error} */ (err).message], ['bare-main', `the repository's main entry ${bare} is bare: it has no main working tree for the review queue`]);
    return true;
  });
  // a linked tree of a normal repository: the main tree
  const side = `${work}-side`;
  await git(['worktree', 'add', '-q', '-b', 'side', side, 'main'], work);
  assert.equal(await mainRootOf(side), work);
});

// ── the mutation proof of a worktree block: an export made from the tree ────

/** The run's `proof` rows without signature and time. @param {Awaited<ReturnType<typeof fixture>>} fx */
const proofRows = async (fx) => (await fx.rows()).filter((r) => r.event === 'proof').map(({ mac: _m, ts: _t, ...r }) => r);

/** What the proof must leave as it was in the tree: its status and the block's files. @param {string} tree */
async function treeState(tree) {
  return {
    status: await git(['status', '--porcelain'], tree),
    files: ['src/a.mjs', 'test/a.test.mjs'].map((f) => readFileSync(path.join(tree, f), 'utf8')),
  };
}

const PROVING_TEST = "import assert from 'node:assert/strict';\nimport { test } from 'node:test';\nimport { a } from '../src/a.mjs';\ntest('a is two', () => assert.equal(a, 2));\n";

test('B62b: proof export and red-green of a worktree block measure an export made from the tree — the tree-only change is proven, the main-only one is never seen; the tree, the main HEAD and index stay as they were', async () => {
  const fx = await fixture('b62b-proof', [WORKTREE.trimEnd()]);
  await fx.open('B1', ['src/a.mjs', 'test/a.test.mjs']);
  const tree = fx.tree('B1');
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(tree, 'test/a.test.mjs', PROVING_TEST);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 3;\n'); // main-only: never measured
  const before = { main: await untouched(fx.repo, fx.origin), tree: await treeState(tree) };
  const exportDir = path.join(fx.repo, '.code-forge', 'export', 'B1');

  const [eCode, eOut, eErr] = await cli(runProof, ['export', 'B1', '--run', fx.runId]);
  assert.deepEqual([eCode, eErr], [0, '']);
  const exp = JSON.parse(eOut);
  assert.deepEqual([exp.dir, exp.base, exp.owned], [exportDir, fx.head, ['src/a.mjs', 'test/a.test.mjs']]);
  assert.equal(readFileSync(path.join(exportDir, 'src/a.mjs'), 'utf8'), 'export const a = 2;\n');

  const [code, out] = await cli(runProof, ['red-green', 'B1', '--run', fx.runId, '--test', 'test/a.test.mjs', '--mechanism', 'revert']);
  const json = JSON.parse(out);
  assert.deepEqual([code, json.isolation, json.mechanism, json.proven, json.export.dir], [0, 'export', 'revert', true, exportDir]);
  const exportRow = { run: fx.runId, block: 'B1', event: 'proof', isolation: 'export', step: 'export', base_sha: fx.head, restored: [], untracked: [], worktree: tree, ...SRC };
  assert.deepEqual(await proofRows(fx), [
    exportRow,
    exportRow,
    { run: fx.runId, block: 'B1', event: 'proof', isolation: 'export', step: 'red-green', test: 'test/a.test.mjs', mechanism: 'revert', red_kind: 'assertion', red: 'RED', green: 'GREEN', proven: true, failed: 1, covers: ['src/a.mjs'], ...SRC },
  ]);

  assert.deepEqual({ main: await untouched(fx.repo, fx.origin), tree: await treeState(tree) }, before);
  assert.deepEqual(before.tree.status, ' M src/a.mjs\n M test/a.test.mjs\n');
  assert.equal(readFileSync(path.join(fx.repo, 'src/a.mjs'), 'utf8'), 'export const a = 3;\n');
  assert.deepEqual(await onBranch(fx.repo, tree), `refs/heads/forge/${fx.runId}/B1`);
});

test('B62b: a worktree block is proven in an export from its tree even when the main tree\'s config later says isolation: lock — no proof lock is asked for, the rows say export', async () => {
  const fx = await fixture('b62b-proof-lock', [WORKTREE.trimEnd()]);
  await fx.open('B1', ['src/a.mjs', 'test/a.test.mjs']);
  const tree = fx.tree('B1');
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(tree, 'test/a.test.mjs', PROVING_TEST);
  const yml = readFileSync(path.join(fx.repo, '.code-forge.yml'), 'utf8');
  writeFile(fx.repo, '.code-forge.yml', yml.replace('isolation: worktree', 'isolation: lock'));
  assert.notEqual(readFileSync(path.join(fx.repo, '.code-forge.yml'), 'utf8'), yml);
  const exportDir = path.join(fx.repo, '.code-forge', 'export', 'B1');
  const [eCode, eOut, eErr] = await cli(runProof, ['export', 'B1', '--run', fx.runId]);
  assert.deepEqual([eCode, eErr, JSON.parse(eOut).dir], [0, '', exportDir]);
  const [code, out, err] = await cli(runProof, ['red-green', 'B1', '--run', fx.runId, '--test', 'test/a.test.mjs', '--mechanism', 'revert']);
  const json = JSON.parse(out);
  assert.deepEqual([code, err, json.isolation, json.proven, json.export.dir], [0, 'RED test/a.test.mjs\nGREEN test/a.test.mjs\n', 'export', true, exportDir]);
  assert.deepEqual((await proofRows(fx)).map((r) => [r.step, r.isolation, r.worktree ?? null]), [['export', 'export', tree], ['export', 'export', tree], ['red-green', 'export', null]]);
});

test('B62b: proof lock on a worktree block is refused with exit 2 and no lock row; a block without a worktree under isolation: lock still refuses proof export with isolation-lock', async () => {
  const fx = await fixture('b62b-lock-verb', [WORKTREE.trimEnd()]);
  await fx.open('B1', ['src/a.mjs']);
  const before = (await fx.rows()).length;
  assert.deepEqual(await cli(runProof, ['lock', 'B1', '--run', fx.runId]), [2, '', 'proof lock: block B1 is proven in an export built from its tree; proof lock does not apply\n']);
  assert.equal((await fx.rows()).length, before);
  assert.equal((await readRun(fx.runId)).proof_lock ?? null, null);

  const lk = await fixture('b62b-lock-export', ['proof:', '  isolation: lock']);
  await lk.open('B1', ['src/a.mjs']);
  assert.deepEqual(await cli(runProof, ['export', 'B1', '--run', lk.runId]), [1, '', 'proof export: proof.isolation is lock — use proof lock instead of an export\n']);
  const [code, out] = await cli(runProof, ['lock', 'B1', '--run', lk.runId]);
  assert.equal(code, 0);
  assert.equal(JSON.parse(out).locked.block, 'B1');
});

test('B62b: a worktree that is gone or malformed refuses proof export and red-green — exit 1, a message naming the block, the run and the tree, no proof row', async () => {
  const fx = await fixture('b62b-proof-gone', [WORKTREE.trimEnd()]);
  await fx.open('B1', ['src/a.mjs', 'test/a.test.mjs']);
  const tree = fx.tree('B1');
  const saved = (await readRun(fx.runId)).blocks.B1.worktree;
  await editRecord(fx.runId, (b) => void (b.B1.worktree = { ...saved, branch: '' }));
  const malformed = `block B1 of run ${fx.runId}: malformed worktree record (it needs an absolute path, a branch and a base_sha); no proof was run\n`;
  assert.deepEqual(await cli(runProof, ['export', 'B1', '--run', fx.runId]), [1, '', `proof export: ${malformed}`]);
  await editRecord(fx.runId, (b) => void (b.B1.worktree = saved));
  rmSync(tree, { recursive: true, force: true });
  const gone = `block B1 of run ${fx.runId}: the worktree ${tree} is gone (removed or moved); no proof was run\n`;
  assert.deepEqual(await cli(runProof, ['red-green', 'B1', '--run', fx.runId, '--test', 'test/a.test.mjs']), [1, '', `proof red-green: ${gone}`]);
  assert.deepEqual(await cli(runProof, ['export', 'B1', '--run', fx.runId]), [1, '', `proof export: ${gone}`]);
  assert.deepEqual(await proofRows(fx), []);
  assert.equal(existsSync(path.join(fx.repo, '.code-forge', 'export', 'B1')), false);
});
