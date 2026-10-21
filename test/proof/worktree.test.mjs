import { commitAll, freshDir, git, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, realpathSync, rmSync, symlinkSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

// B62a (issue #13 item 4): `proof.isolation: worktree` gives each block its own git worktree on its
// own branch, made by `block open` and removed on request by `block close --remove-worktree`. These
// tests run real `git worktree` in temp repositories and pin paths, branches, rows, exit codes and
// messages. Since B62b a close measures the block's tree (test/proof/worktree-flow.test.mjs), so
// the work of the closes below is written in the tree, under `review.external` so no local review
// is needed.

const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, reloadRun, saveRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { DEFAULT_BRANCH_TEMPLATE, blockTree, prepareWorktree, removeWorktree, worktreeSettings } = await import('../../src/proof/worktree.mjs');
const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('../../src/worker/ticket.mjs');

const T0 = new Date('2026-10-11T10:00:00.000Z');
const PROBE = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
const EXTERNAL = 'review:\n  external: {required: true, name: Grok}\n';
const WORKTREE = 'proof:\n  isolation: worktree\n';

/** @param {string} slug @param {string} extra - YAML appended to the base config. */
const configYaml = (slug, extra) =>
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
  const code = await fn(args, { stdout: stream, stderr: stream, probe: PROBE, now: () => T0, isTTY: false });
  return [code, stream.text];
}

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');

/**
 * A repo (a realpath under the per-file temp parent) with `.code-forge.yml` (`extra` appended),
 * `src/a.mjs`, a transcript and a README, all committed; `run start` through the CLI.
 * @param {string} slug @param {string} runId @param {string} [extra]
 */
async function fixture(slug, runId, extra = `${WORKTREE}${EXTERNAL}`) {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  writeFile(repo, 'README.md', '# demo\n');
  await commitAll(repo);
  assert.deepEqual(await cli(runRun, ['start', '--cwd', repo, '--run', runId, '--worker-pid', '4242']), [0, `run ${runId} started · engine harness · worker pid 4242\n`]);
  const head = (await git(['rev-parse', 'HEAD'], repo)).trim();
  /** @param {string} id @param {string[]} owned @param {string[]} [more] */
  const open = (id, owned, more = []) => cli(runBlock, ['open', id, '--run', runId, '--level', 'L2', '--owned', ...owned, '--acceptance', ACC, ...more]);
  /** @param {string} id @param {string[]} [more] */
  const close = (id, more = []) => cli(runBlock, ['close', id, '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...more]);
  const rows = async () => readAllRows(slug);
  const events = async () => (await rows()).map((r) => r.event);
  /** @param {string} branch @returns {Promise<string>} the branch's commit, or 'none' when git says it does not exist (exit 1, no output); any other git failure throws */
  const branchSha = async (branch) => {
    const res = await exec(['git', 'rev-parse', '--verify', '--quiet', `refs/heads/${branch}`], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
    if (res.result === 'ok') return res.stdout.trim();
    if (res.code === 1 && res.stdout === '' && res.stderr === '') return 'none';
    throw new Error(`git rev-parse failed (exit ${res.code}): ${res.stderr}`);
  };
  return { repo, head, open, close, rows, events, branchSha };
}

/** @param {string} tree @returns {Promise<string[]>} the worktree paths git has registered, realpath'd by git itself. */
const registered = async (tree) => (await git(['worktree', 'list', '--porcelain'], tree)).split('\n').filter((l) => l.startsWith('worktree ')).map((l) => l.slice('worktree '.length));

/** @param {Record<string, any>} row */
const strip = ({ mac: _m, ts: _t, ...rest }) => rest;
/** @param {string} id @param {string} head @param {string} tree @param {string} branch @param {string} [extra] */
const openText = (id, head, tree, branch, extra = '') =>
  `block ${id} open · L2 · code · attempt 1 · base ${head.slice(0, 12)}\nworktree ${tree} · branch ${branch}${extra}\nbrief: none given (--brief <file>)\n`;

test('B62a (2): block open makes <repo>-worktrees/<block> on forge/<run>/<block> from the block base, records it, writes a signed worktree.created row before dispatch and prints the path and the branch', async () => {
  const fx = await fixture('b62a-open', 'r-b62a-open');
  const tree = `${fx.repo}-worktrees/B1`;
  assert.deepEqual(await fx.open('B1', ['src/a.mjs']), [0, openText('B1', fx.head, tree, 'forge/r-b62a-open/B1')]);
  assert.deepEqual((await readRun('r-b62a-open')).blocks.B1.worktree, { path: tree, branch: 'forge/r-b62a-open/B1', base_sha: fx.head });
  const rows = await fx.rows();
  assert.deepEqual(rows.map((r) => r.event), ['run.start', 'review.external', 'worktree.created', 'dispatch']);
  assert.deepEqual(strip(rows[2]), { run: 'r-b62a-open', event: 'worktree.created', block: 'B1', path: tree, branch: 'forge/r-b62a-open/B1', base: fx.head, tokens_source: null, cost_source: null });
  assert.deepEqual(verifyRow(rows[2], await loadKey('r-b62a-open')), { ok: true });
  // a real worktree of the repository, on its own branch at the base, with the committed files
  assert.equal(await fx.branchSha('forge/r-b62a-open/B1'), fx.head);
  assert.equal((await git(['rev-parse', '--abbrev-ref', 'HEAD'], tree)).trim(), 'forge/r-b62a-open/B1');
  assert.equal(existsSync(path.join(tree, 'src/a.mjs')), true);
  // the main tree lists only code-forge's own state: the trees sit beside the repository; nothing is pushed
  assert.equal(await git(['status', '--porcelain'], fx.repo), '?? .code-forge/\n');
  assert.equal(await git(['remote'], fx.repo), '');
});

test('B62a (2): a refused worktree refuses the open with exit 1 and a clear message — no block, no row, no branch, no tree', async () => {
  /** @param {string} slug @param {string} extra @param {(fx: Awaited<ReturnType<typeof fixture>>) => Promise<void>} before @param {(fx: Awaited<ReturnType<typeof fixture>>) => string} message */
  const refused = async (slug, extra, before, message) => {
    const fx = await fixture(slug, `r-${slug}`, `${extra}${EXTERNAL}`);
    await before(fx);
    const [code, text] = await fx.open('B1', ['src/a.mjs']);
    assert.deepEqual([code, text], [1, `block open: ${message(fx)}\n`]);
    assert.equal((await readRun(`r-${slug}`)).blocks.B1, undefined);
    assert.deepEqual(await fx.events(), ['run.start', 'review.external']);
    return fx;
  };
  const taken = await refused('b62a-branch', WORKTREE, async (fx) => void (await git(['branch', 'forge/r-b62a-branch/B1'], fx.repo)), () => 'block B1: branch forge/r-b62a-branch/B1 already exists — delete it or change proof.worktree.branch; nothing was recorded');
  assert.equal(existsSync(`${taken.repo}-worktrees/B1`), false);

  const occupied = await refused('b62a-path', WORKTREE, async (fx) => void mkdirSync(`${fx.repo}-worktrees/B1`, { recursive: true }), (fx) => `block B1: ${fx.repo}-worktrees/B1 already exists and is not this block's worktree — move it away or change proof.worktree.dir; nothing was recorded`);
  assert.equal(await occupied.branchSha('forge/r-b62a-path/B1'), 'none');

  const badBase = await refused('b62a-base', `${WORKTREE}  worktree: {base: no-such-ref}\n`, async () => {}, () => 'block B1: proof.worktree.base "no-such-ref" is not a commit of the repository; nothing was recorded');
  assert.equal(await badBase.branchSha('forge/r-b62a-base/B1'), 'none');

  await refused('b62a-dots', `${WORKTREE}  worktree: {branch: 'forge/{run}..{block}'}\n`, async () => {}, () => 'block B1: proof.worktree.branch gives "forge/r-b62a-dots..B1", which is not a valid git branch name; nothing was recorded');
  await refused('b62a-dash', `${WORKTREE}  worktree: {branch: '-x{block}'}\n`, async () => {}, () => 'block B1: proof.worktree.branch gives "-xB1", which is not a valid git branch name; nothing was recorded');

  // git itself refuses the add (the tree directory's parent is a regular file): git's reason is named
  const fx = await fixture('b62a-add', 'r-b62a-add', `${WORKTREE}  worktree: {dir: ../b62a-add-file}\n${EXTERNAL}`);
  writeFile(path.dirname(fx.repo), 'b62a-add-file', 'not a directory\n');
  const [code, text] = await fx.open('B1', ['src/a.mjs']);
  assert.equal(code, 1, text);
  assert.ok(text.startsWith('block open: block B1: git worktree add failed ('), text);
  assert.ok(text.endsWith('); nothing was recorded\n'), text);
  assert.equal((await readRun('r-b62a-add')).blocks.B1, undefined);
  assert.deepEqual(await fx.events(), ['run.start', 'review.external']);
  // git made the branch before it failed to make the directory: the branch is deleted again
  assert.equal(await fx.branchSha('forge/r-b62a-add/B1'), 'none');
  assert.equal(existsSync(path.join(path.dirname(fx.repo), 'b62a-add-file', 'B1')), false);
});

test('B62a (2): a re-open (--attempt) reuses the worktree and the branch with no new created row; a tree removed in between is made again on the same branch', async () => {
  const fx = await fixture('b62a-reopen', 'r-b62a-reopen');
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-reopen/B1';
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n'); // work in progress in the tree survives the re-open
  assert.deepEqual(await cli(runBlock, ['stop', 'B1', '--run', 'r-b62a-reopen', '--reason', 'retry']), [0, 'block B1 stopped\n']);
  const [code, text] = await fx.open('B1', ['src/a.mjs'], ['--attempt', '2']);
  assert.equal(code, 0, text);
  assert.equal(text, openText('B1', fx.head, tree, branch, ' · reused').replace('attempt 1', 'attempt 2'));
  assert.deepEqual((await readRun('r-b62a-reopen')).blocks.B1.worktree, { path: tree, branch, base_sha: fx.head });
  assert.equal((await fx.events()).filter((e) => e === 'worktree.created').length, 1);
  assert.equal(await git(['status', '--porcelain'], tree), ' M src/a.mjs\n');

  // the tree is gone (removed by hand), the branch stays: the next attempt adds it back on that branch
  assert.deepEqual(await cli(runBlock, ['stop', 'B1', '--run', 'r-b62a-reopen', '--reason', 'retry']), [0, 'block B1 stopped\n']);
  await git(['worktree', 'remove', '--force', tree], fx.repo);
  await git(['commit', '-q', '--allow-empty', '-m', 'moved on'], fx.repo);
  // made again (no "· reused"); the base stays the tree's, not the moved HEAD
  assert.deepEqual(await fx.open('B1', ['src/a.mjs'], ['--attempt', '3']), [0, openText('B1', fx.head, tree, branch).replace('attempt 1', 'attempt 3')]);
  const rows = (await fx.rows()).filter((r) => r.event === 'worktree.created');
  assert.deepEqual(rows.map(strip).map((r) => [r.path, r.branch, r.base]), [[tree, branch, fx.head], [tree, branch, fx.head]]);
  assert.equal((await git(['rev-parse', '--abbrev-ref', 'HEAD'], tree)).trim(), branch);
  assert.equal(await fx.branchSha(branch), fx.head);
});

test('B62a (3): two open blocks with their own worktrees may own the same path (open and claim); export blocks still may not; .code-forge.yml is never owned', async () => {
  const fx = await fixture('b62a-overlap', 'r-b62a-overlap');
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  const [code, text] = await fx.open('B2', ['src/**']);
  assert.deepEqual([code, text], [0, openText('B2', fx.head, `${fx.repo}-worktrees/B2`, 'forge/r-b62a-overlap/B2')]);
  assert.deepEqual(await cli(runBlock, ['claim', 'B1', 'src/b.mjs', '--run', 'r-b62a-overlap']), [0, 'block B1 claimed src/b.mjs\n']);
  const blocks = (await readRun('r-b62a-overlap')).blocks;
  assert.deepEqual([blocks.B1.worktree.branch, blocks.B2.worktree.branch], ['forge/r-b62a-overlap/B1', 'forge/r-b62a-overlap/B2']);
  assert.deepEqual(await fx.open('B3', ['.code-forge.yml']), [1, 'block open: owned entry ".code-forge.yml" covers the project\'s .code-forge.yml; no block may own it while review.external.required is true\n']);

  const ex = await fixture('b62a-overlap-export', 'r-b62a-overlap-export', EXTERNAL);
  assert.equal((await ex.open('B1', ['src/a.mjs']))[0], 0);
  assert.deepEqual(await ex.open('B2', ['src/**']), [1, 'block open: block B2 owns src/**, which overlaps src/a.mjs owned by open block B1\n']);
  assert.equal((await ex.open('B2', ['src/c.mjs']))[0], 0);
  assert.deepEqual(await cli(runBlock, ['claim', 'B2', 'src/a.mjs', '--run', 'r-b62a-overlap-export']), [1, 'block claim: src/a.mjs overlaps src/a.mjs owned by open block B1\n']);
  assert.equal(existsSync(`${ex.repo}-worktrees`), false);
});

const RELOAD = (/** @type {string} */ key, /** @type {string} */ why) => `run reload: ${key} cannot change mid-run (${why}) — put it back, or end the run and start a new one\n`;
const ISOLATION_WHY = 'every block of the run is isolated the same way';
const WORKTREE_WHY = 'the worktrees and branches of the open blocks were made with it';

test('B62a (1): run reload refuses to switch proof.isolation or change proof.worktree; the open uses the snapshot, not the file on disk', async () => {
  const fx = await fixture('b62a-reload', 'r-b62a-reload');
  for (const [extra, key, why] of [
    [`${WORKTREE}  worktree: {dir: ../elsewhere}\n`, 'proof.worktree', WORKTREE_WHY],
    [`${WORKTREE}  worktree: {branch: 'work/{block}'}\n`, 'proof.worktree', WORKTREE_WHY],
    ['proof:\n  isolation: export\n', 'proof.isolation', ISOLATION_WHY],
  ]) {
    writeFile(fx.repo, '.code-forge.yml', configYaml('b62a-reload', `${extra}${EXTERNAL}`));
    assert.deepEqual(await cli(runRun, ['reload', '--run', 'r-b62a-reload']), [1, RELOAD(key, why)]);
  }
  // the file on disk now says export; the run still opens blocks in worktrees
  assert.deepEqual(await fx.open('B1', ['src/a.mjs']), [0, openText('B1', fx.head, `${fx.repo}-worktrees/B1`, 'forge/r-b62a-reload/B1')]);

  const ex = await fixture('b62a-reload-on', 'r-b62a-reload-on', EXTERNAL);
  writeFile(ex.repo, '.code-forge.yml', configYaml('b62a-reload-on', `${WORKTREE}${EXTERNAL}`));
  assert.deepEqual(await cli(runRun, ['reload', '--run', 'r-b62a-reload-on']), [1, RELOAD('proof.isolation', ISOLATION_WHY)]);
  // the file on disk now says worktree; the run still opens blocks without one
  assert.equal((await ex.open('B1', ['src/a.mjs']))[0], 0);
  assert.equal((await readRun('r-b62a-reload-on')).blocks.B1.worktree, undefined);
  assert.equal((await ex.events()).includes('worktree.created'), false);
  assert.equal(existsSync(`${ex.repo}-worktrees`), false);

  // a run recorded before snapshots cannot turn worktree isolation on at its first reload
  const record = await readRun('r-b62a-reload-on');
  delete record.config;
  await saveRun(record);
  const config = { version: 1, provider: 'anthropic', engine: 'harness', proof: { isolation: 'worktree' } };
  await assert.rejects(reloadRun({ runId: 'r-b62a-reload-on', config, writeRow: async () => {} }), {
    code: 'immutable-key',
    message: `proof.isolation cannot change mid-run (${ISOLATION_WHY}) — put it back, or end the run and start a new one`,
  });
  assert.equal((await readRun('r-b62a-reload-on')).config, undefined);
});

test('B62a (1): a run recorded before snapshots accepts proof.isolation lock or export at its first reload, and refuses worktree', async () => {
  await fixture('b62a-legacy', 'r-b62a-legacy', EXTERNAL);
  for (const isolation of ['lock', 'export', 'worktree']) {
    const record = await readRun('r-b62a-legacy');
    delete record.config;
    await saveRun(record);
    const config = { version: 1, provider: 'anthropic', engine: 'harness', proof: { isolation } };
    const reload = reloadRun({ runId: 'r-b62a-legacy', config, writeRow: async () => {} });
    if (isolation === 'worktree') {
      await assert.rejects(reload, { code: 'immutable-key', message: `proof.isolation cannot change mid-run (${ISOLATION_WHY}) — put it back, or end the run and start a new one` });
      assert.equal((await readRun('r-b62a-legacy')).config, undefined);
    } else {
      await reload;
      const { config: state } = await readRun('r-b62a-legacy');
      assert.equal(state.snapshots[state.hash].proof.isolation, isolation);
    }
  }
});

test('B62a (1): custom proof.worktree dir, branch and base', async () => {
  const fx = await fixture('b62a-custom', 'r-b62a-custom', `${WORKTREE}  worktree: {dir: ../custom-trees, branch: 'work/{block}-{run}', base: HEAD~1}\n${EXTERNAL}`);
  const parentSha = (await git(['rev-parse', 'HEAD~1'], fx.repo)).trim();
  const tree = path.join(path.dirname(fx.repo), 'custom-trees', 'B1');
  assert.deepEqual(await fx.open('B1', ['src/a.mjs']), [0, openText('B1', fx.head, tree, 'work/B1-r-b62a-custom')]);
  // the block's base stays HEAD; the tree starts from proof.worktree.base
  const block = (await readRun('r-b62a-custom')).blocks.B1;
  assert.equal(block.base_sha, fx.head);
  assert.deepEqual(block.worktree, { path: tree, branch: 'work/B1-r-b62a-custom', base_sha: parentSha });
  assert.equal(await fx.branchSha('work/B1-r-b62a-custom'), parentSha);
  assert.equal(existsSync(path.join(tree, 'src/a.mjs')), false);
});

test('B62a (4): block close --remove-worktree removes the tree after the close, keeps the branch and writes a signed worktree.removed row', async () => {
  const fx = await fixture('b62a-remove', 'r-b62a-remove');
  const tree = `${fx.repo}-worktrees/B1`;
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  await git(['commit', '-q', '-am', 'tree work'], tree);
  const work = await fx.branchSha('forge/r-b62a-remove/B1');
  assert.deepEqual(await fx.close('B1', ['--remove-worktree']), [0, `block B1 closed\nblock B1 worktree removed: ${tree} (branch forge/r-b62a-remove/B1 kept)\n`]);
  const rows = await fx.rows();
  assert.deepEqual(rows.map((r) => r.event).slice(-3), ['gate.reviews_waived', 'block.close', 'worktree.removed']);
  assert.deepEqual(strip(rows.at(-1) ?? {}), { run: 'r-b62a-remove', event: 'worktree.removed', block: 'B1', path: tree, branch: 'forge/r-b62a-remove/B1', tokens_source: null, cost_source: null });
  assert.deepEqual(verifyRow(rows.at(-1) ?? {}, await loadKey('r-b62a-remove')), { ok: true });
  assert.equal(existsSync(tree), false);
  assert.deepEqual(await registered(fx.repo), [fx.repo]);
  assert.notEqual(work, fx.head);
  assert.equal(await fx.branchSha('forge/r-b62a-remove/B1'), work);
  assert.equal((await readRun('r-b62a-remove')).blocks.B1.status, 'closed');
});

test('B62a (4): a tree with uncommitted work is not removed — signed worktree.remove_failed, exit 1, the block stays closed, the tree stays', async () => {
  const fx = await fixture('b62a-dirty', 'r-b62a-dirty');
  const tree = `${fx.repo}-worktrees/B1`;
  assert.equal((await fx.open('B1', ['src/a.mjs', 'notes.txt']))[0], 0);
  writeFile(tree, 'notes.txt', 'not committed\n');
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  const [code, text] = await fx.close('B1', ['--remove-worktree']);
  assert.equal(code, 1, text);
  // our own framing is exact; git's reason only has to name the tree and say why
  const head = 'block B1 closed\nblock B1 worktree NOT removed: ';
  const tail = ` — the block stays closed; the tree stays at ${tree} (commit or clean it, then git worktree remove it)\n`;
  assert.ok(text.startsWith(head) && text.endsWith(tail), text);
  const reason = text.slice(head.length, text.length - tail.length);
  assert.ok(reason.includes(tree), reason);
  assert.ok(reason.includes('untracked'), reason);
  const rows = await fx.rows();
  assert.deepEqual(rows.map((r) => r.event).slice(-2), ['block.close', 'worktree.remove_failed']);
  assert.deepEqual(strip(rows.at(-1) ?? {}), { run: 'r-b62a-dirty', event: 'worktree.remove_failed', block: 'B1', path: tree, reason, tokens_source: null, cost_source: null });
  assert.deepEqual(verifyRow(rows.at(-1) ?? {}, await loadKey('r-b62a-dirty')), { ok: true });
  assert.equal(existsSync(path.join(tree, 'notes.txt')), true);
  assert.equal((await readRun('r-b62a-dirty')).blocks.B1.status, 'closed');
});

test('B62a (4): without --remove-worktree the tree stays; the flag on a block without a worktree, or on another subcommand, is a usage error (exit 2)', async () => {
  const fx = await fixture('b62a-keep', 'r-b62a-keep');
  const tree = `${fx.repo}-worktrees/B1`;
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  assert.deepEqual(await cli(runBlock, ['stop', 'B1', '--run', 'r-b62a-keep', '--reason', 'x', '--remove-worktree']), [2, 'block stop: --remove-worktree is a block close flag\n']);
  assert.deepEqual(await cli(runBlock, ['open', 'B2', '--run', 'r-b62a-keep', '--level', 'L2', '--owned', 'src/b.mjs', '--acceptance', ACC, '--remove-worktree']), [2, 'block open: --remove-worktree is a block close flag\n']);
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.close('B1'), [0, 'block B1 closed\n']);
  assert.equal(existsSync(path.join(tree, 'src/a.mjs')), true);
  assert.equal((await fx.events()).some((e) => e.startsWith('worktree.r')), false);

  const ex = await fixture('b62a-keep-export', 'r-b62a-keep-export', EXTERNAL);
  assert.equal((await ex.open('B1', ['src/a.mjs']))[0], 0);
  writeFile(ex.repo, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await ex.close('B1', ['--remove-worktree']), [2, 'block close: block B1 has no worktree (proof.isolation is not worktree); --remove-worktree does not apply\n']);
  assert.equal((await readRun('r-b62a-keep-export')).blocks.B1.status, 'open');
  assert.equal((await ex.events()).includes('block.close'), false);
});

test('B62a (2): a re-open after the tree directory was deleted by hand prunes the stale registration and makes the tree again at the same path on the same branch', async () => {
  const fx = await fixture('b62a-stale', 'r-b62a-stale');
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-stale/B1';
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  assert.deepEqual(await cli(runBlock, ['stop', 'B1', '--run', 'r-b62a-stale', '--reason', 'retry']), [0, 'block B1 stopped\n']);
  rmSync(tree, { recursive: true, force: true });
  assert.ok((await git(['worktree', 'list', '--porcelain'], fx.repo)).includes('prunable'));
  // made again (no "· reused"), with a second signed worktree.created row
  assert.deepEqual(await fx.open('B1', ['src/a.mjs'], ['--attempt', '2']), [0, openText('B1', fx.head, tree, branch).replace('attempt 1', 'attempt 2')]);
  assert.deepEqual((await readRun('r-b62a-stale')).blocks.B1.worktree, { path: tree, branch, base_sha: fx.head });
  assert.deepEqual((await fx.rows()).filter((r) => r.event === 'worktree.created').map((r) => [r.path, r.branch, r.base]), [[tree, branch, fx.head], [tree, branch, fx.head]]);
  assert.equal(existsSync(path.join(tree, 'src/a.mjs')), true);
  assert.equal((await git(['rev-parse', '--abbrev-ref', 'HEAD'], tree)).trim(), branch);
  assert.deepEqual(await registered(fx.repo), [fx.repo, tree]);
  assert.equal((await git(['worktree', 'list', '--porcelain'], fx.repo)).includes('prunable'), false);
});

test('B62a (4): a re-open after block close --remove-worktree makes the tree again on the kept branch, with the work the branch holds', async () => {
  const fx = await fixture('b62a-again', 'r-b62a-again');
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-again/B1';
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  writeFile(tree, 'src/a.mjs', 'export const a = 3;\n');
  await git(['commit', '-q', '-am', 'tree work'], tree);
  const work = await fx.branchSha(branch);
  writeFile(fx.repo, 'src/a.mjs', 'export const a = 2;\n');
  assert.equal((await fx.close('B1', ['--remove-worktree']))[0], 0);
  assert.equal(existsSync(tree), false);
  assert.deepEqual(await fx.open('B1', ['src/a.mjs'], ['--attempt', '2']), [0, openText('B1', fx.head, tree, branch).replace('attempt 1', 'attempt 2')]);
  assert.deepEqual((await readRun('r-b62a-again')).blocks.B1.worktree, { path: tree, branch, base_sha: fx.head });
  assert.equal((await git(['rev-parse', 'HEAD'], tree)).trim(), work);
  assert.equal((await git(['rev-parse', '--abbrev-ref', 'HEAD'], tree)).trim(), branch);
  assert.equal(await fx.branchSha(branch), work);
});

test('B62a (2): a tree made for an open that then fails to record is rolled back — no tree, no branch, the run record unchanged', async () => {
  const fx = await fixture('b62a-rollback', 'r-b62a-rollback');
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-rollback/B1';
  const before = await readRun('r-b62a-rollback');
  const settings = worktreeSettings(before);
  assert.ok(settings);
  /** @type {string[]} */
  const written = [];
  const writeRow = async (/** @type {Record<string, any>} */ row) => {
    written.push(row.event);
    if (row.event === 'dispatch') throw new Error('ledger unavailable');
  };
  await assert.rejects(
    openBlock({
      runId: 'r-b62a-rollback', id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow,
      worktree: (ctx) => prepareWorktree({ cwd: fx.repo, settings, runId: 'r-b62a-rollback', id: 'B1', ...ctx }),
    }),
    { message: 'ledger unavailable' },
  );
  // the tree was made (its row was on its way) before the dispatch row failed
  assert.deepEqual(written, ['worktree.created', 'dispatch']);
  assert.equal(existsSync(tree), false);
  assert.equal(await fx.branchSha(branch), 'none');
  assert.deepEqual(await registered(fx.repo), [fx.repo]);
  assert.deepEqual(await readRun('r-b62a-rollback'), before);
  // nothing stands in the way of the next open
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  assert.equal(existsSync(tree), true);
});

/** @param {string[]} argv @param {string} cwd @returns {Promise<string>} git's reason for a call that must fail: its first fatal:/error: line, without the prefix. */
async function failingReason(argv, cwd) {
  const res = await exec(['git', ...argv], { cwd, env: gitChildEnv(), timeoutMs: 20000 });
  assert.notEqual(res.result, 'ok');
  const line = res.stderr.split('\n').find((l) => l.startsWith('fatal: ') || l.startsWith('error: ')) ?? '';
  return line.replace(/^(fatal|error): /, '').trim();
}

test('B62a (2): a rollback that cannot remove the tree or the branch throws the original error with a sentence naming each leftover and git\'s reason', async () => {
  const fx = await fixture('b62a-stuck', 'r-b62a-stuck');
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-stuck/B1';
  const before = await readRun('r-b62a-stuck');
  const settings = worktreeSettings(before);
  assert.ok(settings);
  const writeRow = async (/** @type {Record<string, any>} */ row) => {
    if (row.event !== 'dispatch') return;
    // a locked tree survives `remove --force` (one -f), and its branch, still checked out, survives `branch -D`
    await git(['worktree', 'lock', '--reason', 'held', tree], fx.repo);
    throw new Error('ledger unavailable');
  };
  const err = await openBlock({
    runId: 'r-b62a-stuck', id: 'B1', level: 'L2', owned: ['src/a.mjs'], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow,
    worktree: (ctx) => prepareWorktree({ cwd: fx.repo, settings, runId: 'r-b62a-stuck', id: 'B1', ...ctx }),
  }).then(
    () => assert.fail('the open must fail'),
    (/** @type {Error} */ e) => e,
  );
  // the same git calls, made again now, give the reasons the rollback met
  const treeReason = await failingReason(['worktree', 'remove', '--force', tree], fx.repo);
  const branchReason = await failingReason(['branch', '-D', branch], fx.repo);
  assert.equal(err.message, `ledger unavailable. Removing the worktree of the failed open left behind: the worktree ${tree} (${treeReason}); the branch ${branch} (${branchReason}) — remove them by hand.`);
  assert.ok(treeReason.includes('locked'), treeReason);
  assert.equal(existsSync(tree), true);
  assert.equal(await fx.branchSha(branch), fx.head);
  assert.deepEqual(await readRun('r-b62a-stuck'), before);
});

test('B62a (2): a re-open whose tree is registered on another branch or a detached HEAD is refused with exit 1 naming what it is on', async () => {
  const fx = await fixture('b62a-other', 'r-b62a-other');
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-other/B1';
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  assert.deepEqual(await cli(runBlock, ['stop', 'B1', '--run', 'r-b62a-other', '--reason', 'retry']), [0, 'block B1 stopped\n']);
  const eventsBefore = await fx.events();
  await git(['checkout', '-q', '--detach'], tree);
  assert.deepEqual(await fx.open('B1', ['src/a.mjs'], ['--attempt', '2']), [1, `block open: block B1: worktree ${tree} is on a detached HEAD, not ${branch} — check out ${branch} in it; nothing was recorded\n`]);
  await git(['checkout', '-q', '-b', 'side'], tree);
  assert.deepEqual(await fx.open('B1', ['src/a.mjs'], ['--attempt', '2']), [1, `block open: block B1: worktree ${tree} is on side, not ${branch} — check out ${branch} in it; nothing was recorded\n`]);
  assert.deepEqual(await fx.events(), eventsBefore);
  assert.equal((await readRun('r-b62a-other')).blocks.B1.status, 'stopped');
  assert.equal(existsSync(path.join(tree, 'src/a.mjs')), true);
  // back on its branch, the tree is reused
  await git(['checkout', '-q', branch], tree);
  assert.deepEqual(await fx.open('B1', ['src/a.mjs'], ['--attempt', '2']), [0, openText('B1', fx.head, tree, branch, ' · reused').replace('attempt 1', 'attempt 2')]);
});

test('B62a (2): a proof.worktree.dir through a symlink is planned as the realpath it is recorded under, so the overlap rule sees one tree', async () => {
  const repo = await makeRepo();
  const head = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const target = freshDir('real-trees');
  const link = path.join(path.dirname(target), `${path.basename(target)}-link`);
  symlinkSync(target, link);
  // the link exists, `nested` below it does not yet: the nearest existing parent is realpath'd
  const settings = { dir: path.join(link, 'nested'), branch: DEFAULT_BRANCH_TEMPLATE, base: null };
  const planned = await prepareWorktree({ cwd: repo, settings, runId: 'r-link', id: 'B1', baseSha: head, previous: null });
  // `target` is already a realpath (freshDir lives under a realpath'd parent); the realpath is taken anyway
  assert.equal(planned.path, path.join(realpathSync(target), 'nested', 'B1'));
  const made = await planned.make();
  assert.equal(made.created, true);
  assert.equal(made.worktree.path, planned.path);
  assert.equal(realpathSync(made.worktree.path), planned.path);
  assert.deepEqual(await registered(repo), [repo, planned.path]);
  assert.equal((await git(['rev-parse', '--verify', 'refs/heads/forge/r-link/B1'], repo)).trim(), head);
});

test('B62a (2): a branch made between the plan and make() refuses the open with the branch-exists message, and the branch is kept where it points', async () => {
  const repo = await makeRepo();
  const head = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const settings = { dir: null, branch: DEFAULT_BRANCH_TEMPLATE, base: null };
  const planned = await prepareWorktree({ cwd: repo, settings, runId: 'r-race', id: 'B1', baseSha: head, previous: null });
  // someone else's branch, on someone else's commit, appears after the plan
  await git(['commit', '-q', '--allow-empty', '-m', 'theirs'], repo);
  const theirs = (await git(['rev-parse', 'HEAD'], repo)).trim();
  await git(['branch', 'forge/r-race/B1', theirs], repo);
  await assert.rejects(planned.make(), {
    code: 'worktree-failed',
    message: 'block B1: branch forge/r-race/B1 already exists — delete it or change proof.worktree.branch; nothing was recorded',
  });
  assert.equal((await git(['rev-parse', '--verify', 'refs/heads/forge/r-race/B1'], repo)).trim(), theirs);
  assert.notEqual(theirs, head);
  assert.equal(existsSync(planned.path), false);
  assert.deepEqual(await registered(repo), [repo]);
});

test('B62a/B62b (4): a tree whose directory was deleted by hand refuses block close (its work cannot be measured; nothing recorded), and removeWorktree prunes such a tree', async () => {
  const fx = await fixture('b62a-gone', 'r-b62a-gone');
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-gone/B1';
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  rmSync(tree, { recursive: true, force: true });
  const before = await fx.events();
  assert.deepEqual(await fx.close('B1', ['--remove-worktree']), [
    1,
    `block close: block B1: its worktree ${tree} is gone (removed or moved), so its work cannot be measured — restore it on branch ${branch} (git worktree prune, then git worktree add ${tree} ${branch}) and close again; nothing was recorded\n`,
  ]);
  assert.deepEqual(await fx.events(), before);
  assert.equal((await readRun('r-b62a-gone')).blocks.B1.status, 'open');
  // the refused close pruned nothing: git still lists the vanished tree until the removal
  assert.deepEqual(await registered(fx.repo), [fx.repo, tree]);
  // the removal itself still treats a vanished (prunable) tree as removed
  assert.equal(await removeWorktree({ cwd: fx.repo, worktree: { path: tree, branch, base_sha: fx.head } }), null);
  assert.deepEqual(await registered(fx.repo), [fx.repo]);
  assert.equal(await fx.branchSha(branch), fx.head);
});

test('B159: block rebase moves an open worktree onto main, updates both bases and requires fresh gates', async () => {
  const slug = 'b159-worktree-rebase';
  const runId = `r-${slug}`;
  const fx = await fixture(slug, runId, `${WORKTREE}${EXTERNAL}gates:\n  test: [node, --test]\n`);
  await git(['branch', '-M', 'main'], fx.repo);
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = `forge/${runId}/B1`;
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  writeFile(tree, 'src/a.mjs', 'export const a = 2;\n');
  await commitAll(tree);
  // Future timestamps prove that a rebase invalidates gates by row order, not commit time.
  const green = () => writeSigned(runId, (row) => appendRow(row, { slug }), {
    event: 'gate.done', block: 'B1', all_ok: true, ts: '2099-01-01T00:00:00.000Z',
  });
  await green();
  writeFile(fx.repo, 'README.md', '# upstream demo\n');
  await git(['add', 'README.md'], fx.repo);
  await git(['commit', '-q', '-m', 'moved on'], fx.repo);
  const main = (await git(['rev-parse', 'main'], fx.repo)).trim();
  const eventsBefore = await fx.events();
  assert.deepEqual(await cli(runBlock, ['rebase', 'B1', '--run', runId]), [
    0,
    `block B1 rebased ${fx.head.slice(0, 12)} → ${main.slice(0, 12)}\n`,
  ]);
  const block = (await readRun(runId)).blocks.B1;
  assert.equal(block.status, 'open');
  assert.equal(block.base_sha, main);
  assert.equal(block.base_branch, 'main');
  assert.deepEqual(block.worktree, { path: tree, branch, base_sha: main });
  assert.equal((await git(['merge-base', 'main', branch], fx.repo)).trim(), main);
  assert.equal((await git(['rev-parse', 'HEAD^'], tree)).trim(), main);
  assert.equal(await git(['show', 'HEAD:src/a.mjs'], tree), 'export const a = 2;\n');
  assert.deepEqual(await fx.events(), [...eventsBefore, 'block.rebase']);
  const rows = await fx.rows();
  assert.deepEqual(strip(rows.at(-1)), {
    run: runId, event: 'block.rebase', block: 'B1', old_base: fx.head, new_base: main,
    worktree: true, tokens_source: null, cost_source: null,
  });
  assert.deepEqual(verifyRow(rows.at(-1), await loadKey(runId)), { ok: true });
  assert.deepEqual(await fx.close('B1'), [
    1,
    `block B1 open: no green gates run; run: code-forge gates run --run ${runId} --block B1\n`,
  ]);
  assert.equal((await readRun(runId)).blocks.B1.status, 'open');
  assert.deepEqual(await fx.rows(), rows);
  await green();
  const [code, text] = await fx.close('B1');
  assert.equal(code, 0, text);
  assert.equal((await readRun(runId)).blocks.B1.status, 'closed');
});

test('B62a (2): a re-open keeps the worktree base — the block base is the tree base, and a --base that resolves elsewhere is refused with exit 1 and nothing recorded', async () => {
  const fx = await fixture('b62a-rebase', 'r-b62a-rebase');
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-rebase/B1';
  assert.equal((await fx.open('B1', ['src/a.mjs']))[0], 0);
  assert.deepEqual(await cli(runBlock, ['stop', 'B1', '--run', 'r-b62a-rebase', '--reason', 'retry']), [0, 'block B1 stopped\n']);
  await git(['commit', '-q', '--allow-empty', '-m', 'moved on'], fx.repo);
  const moved = (await git(['rev-parse', 'HEAD'], fx.repo)).trim();
  const eventsBefore = await fx.events();
  const recordBefore = await readRun('r-b62a-rebase');
  assert.deepEqual(await fx.open('B1', ['src/a.mjs'], ['--attempt', '2', '--base', 'HEAD']), [
    1,
    `block open: block B1: --base HEAD is not ${fx.head.slice(0, 12)}, the base of the block's worktree ${tree}; a re-open keeps the worktree and its base — drop --base; nothing was recorded\n`,
  ]);
  assert.deepEqual(await fx.events(), eventsBefore);
  assert.deepEqual(await readRun('r-b62a-rebase'), recordBefore);
  // without --base (HEAD has moved) the re-open takes the tree's base, not HEAD
  assert.deepEqual(await fx.open('B1', ['src/a.mjs'], ['--attempt', '2']), [0, openText('B1', fx.head, tree, branch, ' · reused').replace('attempt 1', 'attempt 2')]);
  const block = (await readRun('r-b62a-rebase')).blocks.B1;
  assert.equal(block.base_sha, fx.head);
  assert.notEqual(block.base_sha, moved);
  assert.equal((await fx.rows()).at(-1)?.base_sha, fx.head);
  // a --base naming the tree's own base is accepted
  assert.deepEqual(await cli(runBlock, ['stop', 'B1', '--run', 'r-b62a-rebase', '--reason', 'retry']), [0, 'block B1 stopped\n']);
  assert.equal((await fx.open('B1', ['src/a.mjs'], ['--attempt', '3', '--base', fx.head]))[0], 0);
  assert.equal((await readRun('r-b62a-rebase')).blocks.B1.base_sha, fx.head);
});

test('B62a (2): an inherited GIT_DIR naming another repository does not redirect the worktree or the branch', async () => {
  const fx = await fixture('b62a-gitdir', 'r-b62a-gitdir');
  const other = await makeRepo();
  const tree = `${fx.repo}-worktrees/B1`;
  const branch = 'forge/r-b62a-gitdir/B1';
  const original = process.env.GIT_DIR;
  process.env.GIT_DIR = path.join(other, '.git');
  try {
    assert.deepEqual(await fx.open('B1', ['src/a.mjs']), [0, openText('B1', fx.head, tree, branch)]);
  } finally {
    if (original === undefined) delete process.env.GIT_DIR;
    else process.env.GIT_DIR = original;
  }
  assert.equal(await fx.branchSha(branch), fx.head);
  assert.deepEqual(await registered(fx.repo), [fx.repo, tree]);
  assert.equal((await git(['branch', '--list', 'forge/*'], other)).trim(), '');
  assert.deepEqual(await registered(other), [other]);
  assert.equal(existsSync(`${other}-worktrees`), false);
});

test('B62a (2): prepareWorktree refuses a block id that is not one plain path segment before touching git', async () => {
  const repo = await makeRepo();
  const head = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const settings = { dir: null, branch: DEFAULT_BRANCH_TEMPLATE, base: null };
  for (const id of ['a/b', '..', 'a\\b', '']) {
    await assert.rejects(prepareWorktree({ cwd: repo, settings, runId: 'r-seg', id, baseSha: head, previous: null }), {
      code: 'worktree-failed',
      message: `block ${JSON.stringify(id)}: a block id must be one plain path segment (no "/", "\\" or "..") to name its worktree; nothing was recorded`,
    });
  }
  assert.equal(existsSync(`${repo}-worktrees`), false);
  assert.equal((await git(['branch', '--list', 'forge/*'], repo)).trim(), '');
  // one plain segment is planned at <repo>-worktrees/<id>; nothing is made until make()
  const planned = await prepareWorktree({ cwd: repo, settings, runId: 'r-seg', id: 'B1', baseSha: head, previous: null });
  assert.equal(planned.path, `${repo}-worktrees/B1`);
  assert.equal(existsSync(planned.path), false);
});

test('B62b: blockTree resolves a block\'s tree at the workspace\'s place in it, null without one; a malformed record (a base_sha that is not a full hex id, a branch with control characters), a missing tree, a file, a tree off its branch or a plain directory, or a tree without the workspace directory each refuse with their own code and message', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'pkg/index.mjs', 'export {};\n');
  await commitAll(repo);
  const head = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const tree = `${repo}-tree`;
  await git(['worktree', 'add', '-q', '-b', 'side', tree, 'HEAD'], repo);
  const good = { path: tree, branch: 'side', base_sha: head };
  assert.equal(await blockTree(repo, {}), null);
  assert.equal(await blockTree(repo, { worktree: null }), null);
  assert.deepEqual(await blockTree(repo, { worktree: good }), { top: tree, workspace: tree, path: tree, branch: 'side', base_sha: head });
  assert.deepEqual(await blockTree(path.join(repo, 'pkg'), { worktree: good }), { top: tree, workspace: path.join(tree, 'pkg'), path: tree, branch: 'side', base_sha: head });
  const malformed = { code: 'worktree-malformed', message: 'malformed worktree record (it needs an absolute path, a branch and a base_sha)' };
  const bad = [
    'x',
    { ...good, base_sha: '' },
    { ...good, base_sha: undefined },
    { ...good, base_sha: '--output=x' },
    { ...good, base_sha: head.slice(0, 12) },
    { ...good, base_sha: head.toUpperCase() },
    { ...good, base_sha: `${head}\n` },
    { ...good, branch: '' },
    { ...good, branch: 'side\nmain' },
    { ...good, branch: 'side\u0007' },
    { ...good, branch: 7 },
    { ...good, path: 'rel/tree' },
    { ...good, path: `${tree}\n` },
    { ...good, path: `${tree}\u0000x` },
  ];
  for (const worktree of bad) {
    await assert.rejects(blockTree(repo, { worktree }), malformed);
  }
  // a SHA-256 object id is a full hex id too
  assert.equal((await blockTree(repo, { worktree: { ...good, base_sha: 'a'.repeat(64) } }))?.base_sha, 'a'.repeat(64));
  // the tree must be on its recorded branch: another branch, a detached HEAD, a plain directory
  const offBranch = (now) => ({ code: 'worktree-branch-mismatch', message: `the worktree ${tree} is not on its branch side (${now})` });
  await git(['checkout', '-q', '-b', 'other'], tree);
  await assert.rejects(blockTree(repo, { worktree: good }), offBranch('it is on other'));
  await git(['checkout', '-q', '--detach'], tree);
  await assert.rejects(blockTree(repo, { worktree: good }), offBranch('its HEAD is detached, or it is not a git worktree'));
  await git(['checkout', '-q', 'side'], tree);
  assert.equal((await blockTree(repo, { worktree: good }))?.top, tree);
  const plain = `${repo}-plain`;
  mkdirSync(plain);
  await assert.rejects(blockTree(repo, { worktree: { ...good, path: plain } }), {
    code: 'worktree-branch-mismatch',
    message: `the worktree ${plain} is not on its branch side (its HEAD is detached, or it is not a git worktree)`,
  });
  await assert.rejects(blockTree(repo, { worktree: { ...good, path: `${repo}-nowhere` } }), { code: 'worktree-missing', message: `the worktree ${repo}-nowhere is gone (removed or moved)` });
  writeFile(path.dirname(repo), `${path.basename(repo)}-file`, 'x\n');
  await assert.rejects(blockTree(repo, { worktree: { ...good, path: `${repo}-file` } }), { code: 'worktree-not-dir', message: `the worktree ${repo}-file is not a directory` });
  // a workspace directory the tree does not have (untracked in the main tree only)
  mkdirSync(path.join(repo, 'local'));
  await assert.rejects(blockTree(path.join(repo, 'local'), { worktree: good }), { code: 'worktree-workspace-missing', message: `the worktree ${tree} has no local directory (the run's workspace)` });
});

test('B62b: blockTree refuses a tree of another repository on the same branch name (worktree-foreign), and a git that cannot run (git-failed with the reason)', async () => {
  const repo = await makeRepo();
  const head = (await git(['rev-parse', 'HEAD'], repo)).trim();
  const tree = `${repo}-tree`;
  await git(['worktree', 'add', '-q', '-b', 'side', tree, 'HEAD'], repo);
  const good = { path: tree, branch: 'side', base_sha: head };
  // another repository, checked out on a branch of the same name, at the path the record names
  const other = await makeRepo();
  await git(['checkout', '-q', '-b', 'side'], other);
  await assert.rejects(blockTree(repo, { worktree: { ...good, path: other } }), { code: 'worktree-foreign', message: `the worktree ${other} belongs to another repository` });
  // a linked worktree of the other repository on that branch name is foreign too
  await git(['checkout', '-q', '--detach'], other);
  const otherTree = `${other}-tree`;
  await git(['worktree', 'add', '-q', otherTree, 'side'], other);
  await assert.rejects(blockTree(repo, { worktree: { ...good, path: otherTree } }), { code: 'worktree-foreign', message: `the worktree ${otherTree} belongs to another repository` });
  assert.equal((await blockTree(repo, { worktree: good }))?.top, tree);
  const saved = process.env.PATH;
  process.env.PATH = '';
  try {
    await assert.rejects(blockTree(repo, { worktree: good }), { code: 'git-failed', message: `git symbolic-ref HEAD could not run in the worktree ${tree} (spawn git ENOENT)` });
  } finally {
    process.env.PATH = saved;
  }
});
