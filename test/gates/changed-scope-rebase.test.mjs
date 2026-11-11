import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { reusableBaseMove } from '../../src/gates/scope.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const scratch = await realpath(await mkdtemp(path.join(os.tmpdir(), 'changed-scope-')));
const binary = path.join(scratch, 'pkg/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const cleanEnv = () => Object.fromEntries(Object.entries(process.env).filter(([key]) =>
  !key.startsWith('GIT_') && !['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK'].includes(key)));
before(async () => {
  await mkdir(path.dirname(binary), { recursive: true });
  await mkdir(path.join(scratch, 'pkg/bin'), { recursive: true });
  await symlink(path.join(ROOT, 'bin/code-forge.mjs'), path.join(scratch, 'pkg/bin/code-forge.mjs'));
  linkGoBinary(binary, { ldflags: `-X main.reviewWaitNodeVersion=${process.version}` });
});
after(() => rm(scratch, { recursive: true, force: true }));

// B404: Go's gates run --changed selects tests by import graph and never runs
// the full suite; the legacy scope rules below hold for the Node product only.
// The Go rules are tested at the end of this file.
const NODE_ONLY = [false];

async function put(f, file, text) {
  await mkdir(path.dirname(path.join(f.repo, file)), { recursive: true });
  await writeFile(path.join(f.repo, file), text);
}
function git(f, ...args) {
  const result = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8', env: { ...cleanEnv(), GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' } });
  assert.equal(result.status, 0, `${args.join(' ')}: ${result.stderr}`);
  return result.stdout.trim();
}
function commit(f) {
  git(f, 'add', '.');
  git(f, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture');
  return git(f, 'rev-parse', 'HEAD');
}
async function fixture(native, owned = ['src/a.mjs', 'test/a.test.mjs'], threshold = 10) {
  const root = await mkdtemp(path.join(scratch, 'case-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home'), native, record: null, noNode: false, logHandover: false };
  await mkdir(f.repo); await mkdir(f.home);
  git(f, 'init', '-q', '-b', 'main');
  await put(f, '.gitignore', '.code-forge/\nexecutions\n');
  await put(f, 'package.json', '{"type":"module","version":"1.0.0"}\n');
  await put(f, 'src/a.mjs', 'export const a = 1;\n');
  await put(f, 'src/other.mjs', 'export const other = 1;\n');
  await put(f, 'src/x.txt', 'source input\n');
  await put(f, 'test/a.test.mjs', "import '../src/a.mjs';\n");
  await put(f, 'test/other.test.mjs', "import '../src/other.mjs';\n");
  await put(f, 'native/main.go', 'package main\n');
  await put(f, 'test/compat/parity-a.test.mjs', '// starts the binary\n');
  await put(f, 'test/compat/parity-b.test.mjs', '// starts the binary too\n');
  const probe = "const fs=require('node:fs');fs.appendFileSync('executions','run\\n');console.log(JSON.stringify(process.argv.slice(1)))";
  await put(f, '.code-forge.yml', JSON.stringify({ project: { slug: 'fixture' }, gates: {
    test: [process.execPath, '-e', probe, '{test_files}'], lint: null, types: null, format: null, rerun_failed: 0, full_suite_threshold_files: threshold,
  } }));
  const base = commit(f);
  git(f, 'checkout', '-qb', 'block');
  f.record = { run_id: 'fixture', project: 'fixture', workspace: f.repo, status: 'active', blocks: { B1: {
    status: 'open', kind: 'code', base_sha: base, base_branch: 'main', owned_files: owned, test_files: [],
  } } };
  await mkdir(path.join(f.home, '.code-forge/runs'), { recursive: true });
  await writeFile(path.join(f.home, '.code-forge/runs/fixture.json'), JSON.stringify(f.record));
  return f;
}
function gate(f, changed = true) {
  const mode = changed === 'full' ? ['--full'] : changed ? ['--changed'] : [];
  const args = ['gates', 'run', '--run', 'fixture', '--block', 'B1', '--json', ...mode];
  const child = spawnSync(f.native ? binary : process.execPath, f.native ? args : [path.join(ROOT, 'bin/code-forge.mjs'), ...args], {
    cwd: f.repo, encoding: 'utf8', env: { ...cleanEnv(), HOME: f.home, TMPDIR: scratch, CODE_FORGE_NATIVE: f.logHandover ? '1' : '0', CODE_FORGE_NODE: f.noNode ? '/nonexistent' : process.execPath, CODE_FORGE_HANDOVER_LOG: f.logHandover ? '1' : '0', CODE_FORGE_NO_ERROR_LOG: '1' },
  });
  assert.equal(child.status, 0, child.stderr + child.stdout);
  return JSON.parse(child.stdout).results.find((r) => r.gate === 'test');
}
async function rows(f) {
  return (await readFile(path.join(f.home, '.code-forge/ledger/fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
}
async function release(f, source = false) {
  git(f, 'checkout', '-q', 'main');
  await put(f, 'CHANGELOG.md', 'release\n');
  await put(f, 'docs/release.md', 'release docs\n');
  await put(f, 'package.json', '{"type":"module","version":"1.1.0"}\n');
  if (source) await put(f, 'src/other.mjs', 'export const other = 2;\n');
  commit(f);
  git(f, 'checkout', '-q', 'block');
  git(f, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'rebase', 'main');
}

test('release rebase selects exactly the block pair and records related scope and reason', async () => {
  for (const native of NODE_ONLY) {
    const f = await fixture(native, ['src/a.mjs', 'test/a.test.mjs', 'package.json', 'CHANGELOG.md', 'docs/release.md'], 2);
    await put(f, 'src/a.mjs', 'export const a = 2;\n');
    await put(f, 'test/a.test.mjs', "import '../src/a.mjs'; // block\n");
    commit(f);
    await release(f);
    const result = gate(f);
    assert.deepEqual(JSON.parse(result.stdout), ['test/a.test.mjs'], 'only one related test, no release files');
    assert.deepEqual([result.scope, result.reason], ['related', 'no_baseline']);
    const logged = await rows(f);
    assert.equal(logged.length, 1, 'exactly one gate row');
    assert.deepEqual([logged[0].scope, logged[0].reason, logged[0].test_count], ['related', 'no_baseline', 1]);
  }
});

test('green full suite is reused for the same tree and a release-only base move, with one row per call', async () => {
  for (const native of NODE_ONLY) {
    const f = await fixture(native, ['src/a.mjs', 'test/a.test.mjs'], 0);
    await put(f, 'src/a.mjs', 'export const a = 2;\n'); commit(f);
    assert.deepEqual(JSON.parse(gate(f).stdout), [], 'first changed call runs the full suite');
    assert.equal(gate(f).reused, true, 'same-tree full result reused');
    await release(f);
    assert.equal(gate(f).reused, true, 'docs and version-only base move reused');
    assert.equal(await readFile(path.join(f.repo, 'executions'), 'utf8'), 'run\n', 'test process starts exactly once');
    const logged = await rows(f);
    assert.equal(logged.length, 3, 'all three calls have ledger rows');
    assert.deepEqual(logged.map((row) => row.results[0].reused ?? false), [false, true, true]);
  }
});

test('a source base move invalidates a green full suite and prints why it runs', async () => {
  for (const native of NODE_ONLY) {
    const f = await fixture(native, ['src/a.mjs', 'test/a.test.mjs'], 0);
    await put(f, 'src/a.mjs', 'export const a = 2;\n'); commit(f);
    gate(f);
    await release(f, true);
    const result = gate(f);
    assert.equal(result.reused ?? false, false, 'source changes cannot reuse green');
    assert.equal(result.reuse_reason, 'tree_changed', 'rerun reason is visible');
    assert.equal(await readFile(path.join(f.repo, 'executions'), 'utf8'), 'run\nrun\n', 'exactly two real test executions');
  }
});

test('native Go-only changes select exactly all binary parity tests and print the dependency reason', async () => {
  for (const native of NODE_ONLY) {
    for (const baseAware of [false, true]) {
      const f = await fixture(native, ['native/main.go']);
      if (!baseAware) {
        delete f.record.blocks.B1.base_branch;
        delete f.record.blocks.B1.base_sha;
        await writeFile(path.join(f.home, '.code-forge/runs/fixture.json'), JSON.stringify(f.record));
      }
      await put(f, 'native/main.go', 'package main\n// block\n'); commit(f);
      const result = gate(f);
      assert.deepEqual(JSON.parse(result.stdout), ['test/compat/parity-a.test.mjs', 'test/compat/parity-b.test.mjs'], 'both parity tests selected without import edges');
      assert.equal(result.reason, 'native-binary', 'implicit binary dependency is visible');
      assert.deepEqual((await rows(f))[0].reason, 'native-binary');
    }
  }
});

test('native changes with an uncovered source keep the full-suite fallback reason', async () => {
  for (const native of NODE_ONLY) {
    for (const baseAware of [false, true]) {
      const f = await fixture(native, ['native/main.go', 'src/uncovered.mjs']);
      if (!baseAware) {
        delete f.record.blocks.B1.base_branch;
        delete f.record.blocks.B1.base_sha;
        await writeFile(path.join(f.home, '.code-forge/runs/fixture.json'), JSON.stringify(f.record));
      }
      await put(f, 'native/main.go', 'package main\n// block\n');
      await put(f, 'src/uncovered.mjs', 'export const uncovered = 1;\n');
      commit(f);
      const result = gate(f);
      const reason = baseAware ? 'unpaired' : 'no_baseline';
      assert.deepEqual(JSON.parse(result.stdout), [], 'the uncovered source requires the full suite');
      assert.equal(result.reason, reason, 'native parity coverage must not mask the fallback');
      assert.deepEqual(result.dependents, ['test/compat/parity-a.test.mjs', 'test/compat/parity-b.test.mjs']);
      assert.equal((await rows(f))[0].reason, reason, 'the ledger preserves the fallback reason');
    }
  }
});

test('moving a source file into docs invalidates the green full suite across a rebase', async () => {
  for (const native of NODE_ONLY) {
    const f = await fixture(native, ['src/a.mjs', 'test/a.test.mjs'], 0);
    await put(f, 'src/a.mjs', 'export const a = 2;\n');
    commit(f);
    gate(f);
    git(f, 'checkout', '-q', 'main');
    await mkdir(path.join(f.repo, 'docs'));
    git(f, 'mv', 'src/x.txt', 'docs/x.txt');
    const base = commit(f);
    git(f, 'checkout', '-q', 'block');
    git(f, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'rebase', 'main');
    assert.equal(await reusableBaseMove(f.repo, f.record.blocks.B1.base_sha, base), false, 'the source side of a rename invalidates base evidence');
    const result = gate(f);
    assert.equal(result.reused ?? false, false, 'the green full suite cannot be reused');
    assert.deepEqual([result.scope, result.reason], ['full', 'no_baseline']);
    assert.equal(await readFile(path.join(f.repo, 'executions'), 'utf8'), 'run\nrun\n', 'the full suite executes again');
  }
});

test('doc-only block selects its declared test without adding binary parity tests', async () => {
  for (const native of NODE_ONLY) {
    const f = await fixture(native, ['docs/guide.md']);
    f.record.blocks.B1.test_files = ['test/a.test.mjs'];
    await writeFile(path.join(f.home, '.code-forge/runs/fixture.json'), JSON.stringify(f.record));
    await put(f, 'docs/guide.md', 'guide\n'); commit(f);
    const result = gate(f);
    assert.deepEqual(JSON.parse(result.stdout), ['test/a.test.mjs'], 'one declared test and zero parity tests');
    assert.equal(result.reason, 'unpaired', 'safe fallback explains the doc without a pair');
  }
});

test('plain worktree-branch gates keep legacy rows and cannot seed changed full-suite reuse', async () => {
  for (const native of NODE_ONLY) {
    const f = await fixture(native, ['src/a.mjs', 'test/a.test.mjs'], 0);
    const config = JSON.parse(await readFile(path.join(f.repo, '.code-forge.yml'), 'utf8'));
    config.gates.test = config.gates.test.slice(0, -1);
    await put(f, '.code-forge.yml', JSON.stringify(config));
    gate(f, false);
    const [plain] = await rows(f);
    assert.equal(Object.hasOwn(plain, 'files'), false);
    assert.equal(Object.hasOwn(plain, 'test_base'), false);
    assert.equal(Object.hasOwn(plain.results[0], 'full_stamp'), false);
    assert.equal(gate(f).reused ?? false, false, 'a changed run must measure its own full-suite evidence');
    assert.equal(await readFile(path.join(f.repo, 'executions'), 'utf8'), 'run\nrun\n');
  }
});

// baseTests adds go.test.mjs and a test that builds the binary to the block's
// base, so they are not changed files of the block.
async function baseTests(f) {
  git(f, 'checkout', '-q', 'main');
  await put(f, 'test/compat/go.test.mjs', '// go build, vet and test\n');
  await put(f, 'test/build.test.mjs', "spawnSync('go', ['build', './cmd/code-forge-native']);\n");
  f.record.blocks.B1.base_sha = commit(f);
  git(f, 'checkout', '-q', 'block');
  git(f, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'rebase', '-q', 'main');
  await writeFile(path.join(f.home, '.code-forge/runs/fixture.json'), JSON.stringify(f.record));
}

test('Go --changed selects import-graph tests with no hand-over and no full suite (B404)', async () => {
  // Threshold 0 would send the Node product to the full suite.
  const f = await fixture(true, ['src/a.mjs', 'test/a.test.mjs', 'package.json', 'CHANGELOG.md', 'docs/release.md'], 0);
  f.noNode = true; f.logHandover = true;
  await put(f, 'src/a.mjs', 'export const a = 2;\n'); commit(f);
  await release(f);
  const result = gate(f);
  assert.deepEqual(JSON.parse(result.stdout), ['test/a.test.mjs'], 'the importer of the change, never the full list');
  assert.deepEqual([result.scope, result.reason], ['related', 'import_graph']);
  assert.equal((await rows(f))[0].reason, 'import_graph');
  assert.equal(await readFile(path.join(f.repo, 'executions'), 'utf8'), 'run\n');
  await assert.rejects(readFile(path.join(f.home, '.code-forge/native/handovers.jsonl')), { code: 'ENOENT' }, 'no hand-over to Node');
});

test('Go --changed on a native change selects every test that builds the native binary (B330)', async () => {
  const f = await fixture(true, ['native/main.go']);
  f.noNode = true; f.logHandover = true;
  await baseTests(f);
  await put(f, 'native/main.go', 'package main\n// block\n'); commit(f);
  const result = gate(f);
  assert.deepEqual(JSON.parse(result.stdout), ['test/compat/go.test.mjs', 'test/build.test.mjs'], 'go.test.mjs and the binary test found by its text, not a fixed list');
  assert.deepEqual([result.scope, result.reason], ['related', 'go_only']);
  await assert.rejects(readFile(path.join(f.home, '.code-forge/native/handovers.jsonl')), { code: 'ENOENT' }, 'no hand-over to Node');
});

test('Go --changed with nothing selected skips the test gate instead of the full suite (B404)', async () => {
  const f = await fixture(true, ['native/main.go', 'src/uncovered.mjs']);
  f.noNode = true; f.logHandover = true;
  await put(f, 'native/main.go', 'package main\n// block\n');
  await put(f, 'src/uncovered.mjs', 'export const uncovered = 1;\n');
  commit(f);
  const result = gate(f);
  assert.deepEqual([result.skipped, result.scope, result.reason], [true, 'related', 'import_graph']);
  await assert.rejects(readFile(path.join(f.repo, 'executions')), { code: 'ENOENT' }, 'the test command never starts');
  await assert.rejects(readFile(path.join(f.home, '.code-forge/native/handovers.jsonl')), { code: 'ENOENT' }, 'no hand-over to Node');
});

test('Go gates run --full runs the whole test command (B404)', async () => {
  const f = await fixture(true, ['src/a.mjs', 'test/a.test.mjs']);
  f.noNode = true;
  await put(f, 'src/a.mjs', 'export const a = 2;\n'); commit(f);
  const result = gate(f, 'full');
  assert.deepEqual(JSON.parse(result.stdout), [], 'no file filter');
  assert.deepEqual([result.scope, result.reason, result.skipped], ['full', 'explicit', false]);
  assert.equal(await readFile(path.join(f.repo, 'executions'), 'utf8'), 'run\n');
});

test('Go --changed doc-only block runs its declared test and adds no binary tests or go.test.mjs (B404)', async () => {
  const f = await fixture(true, ['docs/guide.md']);
  f.noNode = true; f.logHandover = true;
  await baseTests(f);
  f.record.blocks.B1.test_files = ['test/a.test.mjs'];
  await writeFile(path.join(f.home, '.code-forge/runs/fixture.json'), JSON.stringify(f.record));
  await put(f, 'docs/guide.md', 'guide\n'); commit(f);
  const result = gate(f);
  assert.deepEqual(JSON.parse(result.stdout), ['test/a.test.mjs'], 'one declared test, no binary or go.test.mjs');
  assert.equal(result.scope, 'related');
  assert.equal(result.reason, 'go_only', 'a block with no Node product file');
  await assert.rejects(readFile(path.join(f.home, '.code-forge/native/handovers.jsonl')), { code: 'ENOENT' }, 'no hand-over to Node');
});
