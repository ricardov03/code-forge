// B363: `gates run --run R --block B` refuses at once, before any gate command, when the block's
// base branch moved past its recorded base. --auto-rebase rebases first; --allow-stale-base warns.
import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, symlinkSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { appendRow } from '../../src/ledger/write.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';
import { nativePath } from '../../src/util/native.mjs';

for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK', 'CODE_FORGE_GATES_BASE_CHECKED']) delete process.env[key];
const ROOT = fileURLToPath(new URL('../../', import.meta.url));
// A private build of the current source, in a package layout so a handover finds bin/code-forge.mjs.
const pkg = freshDir('pkg');
const binary = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
mkdirSync(path.dirname(binary), { recursive: true });
mkdirSync(path.join(pkg, 'bin'));
symlinkSync(path.join(ROOT, 'bin/code-forge.mjs'), path.join(pkg, 'bin/code-forge.mjs'));
const built = spawnSync('go', ['build', '-o', binary, './cmd/code-forge-native'], {
  cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' },
});
assert.equal(built.status, 0, `private native build: ${built.error?.message ?? built.stderr}`);
// The first exec of a just-built binary pays macOS's one-time check of the new file; pay it here,
// so the 2 s bound below times the stale-base check, not that first exec.
spawnSync(binary, ['--version'], { encoding: 'utf8', timeout: 60_000 });
const { runBlock } = await import('../../src/cli/block.mjs');
const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'fixture-worker' : null);

/** One worktree block B1 opened on main, with a gate that appends one line to a marker file. */
async function fixture(/** @type {string} */ label) {
  const slug = `stale-${label}`;
  const repo = freshDir(slug);
  const marker = path.join(freshDir('marker'), 'ran');
  await git(['init', '-qb', 'main'], repo);
  const gate = `require('node:fs').appendFileSync(${JSON.stringify(marker)}, 'ran\\n')`;
  const config = { version: 1, project: { slug }, ...cfgFor(), gates: { test: [process.execPath, '-e', gate], lint: null, types: null, format: null },
    proof: { isolation: 'worktree', worktree: { dir: freshDir('trees') } } };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, 'src/a.mjs', '// base\n');
  writeFile(repo, 'upstream.md', 'base\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-qm', 'base'], repo);
  const runId = `r-${slug}`;
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe, config, writeRow: (row) => appendRow(row, { slug }) });
  const inputs = freshDir('inputs');
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'stale base', tests: ['stale-base'] }]));
  let err = '';
  const opened = await runBlock(['open', 'B1', '--run', runId, '--level', 'L2', '--owned', 'src', '--acceptance', path.join(inputs, 'acceptance.json')],
    { probe, isTTY: false, stdout: { write: () => true }, stderr: { write: (/** @type {string} */ s) => { err += s; return true; } } });
  assert.equal(opened, 0, err);
  const entry = (await readRun(runId)).blocks.B1;
  assert.ok(entry.worktree?.path, 'the block works in a worktree');
  return { repo, runId, marker, base: entry.worktree.base_sha, tree: entry.worktree.path };
}

/** Main gets one commit the block does not own. @returns {Promise<string>} the new tip. */
async function moveMain(/** @type {{repo: string}} */ f) {
  writeFile(f.repo, 'upstream.md', 'moved\n');
  await git(['add', '-A'], f.repo);
  await git(['commit', '-qm', 'upstream'], f.repo);
  return (await git(['rev-parse', 'HEAD'], f.repo)).trim();
}

/** @param {{repo: string, runId: string}} f @param {string[]} extra @param {'go' | 'node' | 'node-native'} via */
function gate(f, extra = [], via = 'go') {
  const args = ['gates', 'run', '--run', f.runId, '--block', 'B1', ...extra];
  /** @type {NodeJS.ProcessEnv} */
  const env = { ...process.env, CODE_FORGE_NODE: process.execPath, CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_HANDOVER_LOG: '0' };
  if (via === 'node') env.CODE_FORGE_NATIVE = '0';
  const started = Date.now();
  const child = via === 'go'
    ? spawnSync(binary, args, { cwd: f.repo, encoding: 'utf8', env, timeout: 60_000 })
    : spawnSync(process.execPath, [path.join(ROOT, 'bin/code-forge.mjs'), ...args], { cwd: f.repo, encoding: 'utf8', env, timeout: 60_000 });
  return { status: child.status, stdout: child.stdout, stderr: child.stderr, ms: Date.now() - started };
}

const ranLines = (/** @type {string} */ marker) => (existsSync(marker) ? readFileSync(marker, 'utf8').split('\n').filter(Boolean).length : 0);
const fixLine = (/** @type {{runId: string}} */ f) => `code-forge block rebase B1 --run ${f.runId} --accept-landed`;

for (const via of /** @type {const} */ (['go', 'node'])) {
  test(`${via}: a base branch one commit past the recorded base refuses in under 2 s, prints the fix line and runs no gate`, async () => {
    const f = await fixture(`refuse-${via}`);
    const tip = await moveMain(f);
    const run = gate(f, [], via);
    assert.equal(run.status, 1, `exit 1; stderr: ${run.stderr}`);
    const lines = run.stderr.split('\n').filter(Boolean);
    assert.equal(lines.length, 1, `exactly one stderr line, got: ${run.stderr}`);
    assert.ok(lines[0].includes(fixLine(f)), `the line names the fix: ${lines[0]}`);
    assert.ok(lines[0].includes(tip.slice(0, 12)) && lines[0].includes(f.base.slice(0, 12)), `the line names both bases: ${lines[0]}`);
    assert.equal(run.stdout, '', 'zero stdout bytes');
    assert.equal(ranLines(f.marker), 0, 'zero gate commands ran');
    assert.ok(run.ms < 2000, `refused in ${run.ms} ms, under 2000`);
    const entry = (await readRun(f.runId)).blocks.B1;
    assert.equal(entry.worktree.base_sha, f.base, 'the recorded base is unchanged');
  });

  test(`${via}: --allow-stale-base gates on the old base with one WARN line`, async () => {
    const f = await fixture(`allow-${via}`);
    await moveMain(f);
    const run = gate(f, ['--allow-stale-base'], via);
    assert.equal(run.status, 0, `exit 0; stderr: ${run.stderr}`);
    const warns = run.stderr.split('\n').filter((line) => line.includes('WARN'));
    assert.equal(warns.length, 1, `exactly one WARN line, got: ${run.stderr}`);
    assert.ok(warns[0].includes('--allow-stale-base') && warns[0].includes(fixLine(f)), warns[0]);
    assert.equal(ranLines(f.marker), 1, 'the gate ran exactly once');
    assert.equal((await readRun(f.runId)).blocks.B1.worktree.base_sha, f.base, 'the recorded base is unchanged');
  });
}

test('go: --auto-rebase rebases the block onto the new tip, then runs the gate', async () => {
  const f = await fixture('auto-go');
  const tip = await moveMain(f);
  const run = gate(f, ['--auto-rebase']);
  assert.equal(run.status, 0, `exit 0; stderr: ${run.stderr}`);
  const entry = (await readRun(f.runId)).blocks.B1;
  assert.equal(entry.worktree.base_sha, tip, 'worktree base is the new tip');
  assert.equal(entry.base_sha, tip, 'block base is the new tip');
  assert.equal((await git(['merge-base', 'HEAD', 'main'], f.tree)).trim(), tip, 'the tree sits on the new tip');
  assert.equal(ranLines(f.marker), 1, 'the gate ran exactly once');
  assert.equal(run.stderr.split('\n').filter((line) => line.includes('rebased')).length, 1, `one rebased line: ${run.stderr}`);
});

test('node: --auto-rebase rebases through the native helper, then runs the gate', { skip: nativePath() ? false : 'needs node scripts/build-native.mjs' }, async () => {
  const f = await fixture('auto-node');
  const tip = await moveMain(f);
  const run = gate(f, ['--auto-rebase'], 'node-native');
  assert.equal(run.status, 0, `exit 0; stderr: ${run.stderr}`);
  const entry = (await readRun(f.runId)).blocks.B1;
  assert.deepEqual([entry.worktree.base_sha, entry.base_sha], [tip, tip], 'both recorded bases are the new tip');
  assert.equal(ranLines(f.marker), 1, 'the gate ran exactly once');
});

test('go: an equal base runs the gate as before, with no stale-base line', async () => {
  const f = await fixture('equal-go');
  const run = gate(f);
  assert.equal(run.status, 0, `exit 0; stderr: ${run.stderr}`);
  assert.equal(ranLines(f.marker), 1, 'the gate ran exactly once');
  assert.ok(!run.stderr.includes('--accept-landed'), `no fix line: ${run.stderr}`);
  assert.equal(JSON.parse(run.stdout).allOk, true, 'the gate is green');
});

test('go: --auto-rebase with --allow-stale-base is a usage error and runs no gate', async () => {
  const f = await fixture('both-go');
  await moveMain(f);
  const run = gate(f, ['--auto-rebase', '--allow-stale-base']);
  assert.equal(run.status, 2, `exit 2; stderr: ${run.stderr}`);
  assert.equal(ranLines(f.marker), 0, 'zero gate commands ran');
});
