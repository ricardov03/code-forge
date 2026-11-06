import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { configHash } from '../../src/state/config-snapshot.mjs';
import { verifyRow } from '../../src/state/signer.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-open-'));
const pkg = path.join(scratch, 'package');
const BINARY = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const RUN = 'r-open-fixture', KEY = Buffer.alloc(32, 9);
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(pkg, 'bin'));
  await symlink(CLI, path.join(pkg, 'bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const home = process.env.CODE_FORGE_REAL_HOME ?? os.homedir();
  const cache = path.join(home, process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build');
  const built = spawnSync('go', ['build', '-trimpath', '-ldflags', `-s -w -X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}`, '-o', BINARY, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off', GOCACHE: cache },
  });
  assert.equal(built.status, 0, built.stderr);
});
function git(f, ...args) {
  const r = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8', env: { PATH: process.env.PATH, HOME: f.home, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull, GIT_AUTHOR_DATE: '2026-01-01T00:00:00Z', GIT_COMMITTER_DATE: '2026-01-01T00:00:00Z' } });
  assert.equal(r.status, 0, `git ${args[0]}: ${r.stderr}`);
  return r.stdout;
}
/** @param {{isolated?: boolean, change?: (record: any) => void, yaml?: string, config?: string}} [options] */
async function fixture({ isolated = false, change = () => {}, yaml = '- clause: works\n  tests: [sample]\n', config = '' } = {}) {
  const root = await mkdtemp(path.join(scratch, 'fixture-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  for (const [name, body] of Object.entries({ 'sample.mjs': 'export const x = 1;\n', '.gitignore': '.code-forge/\n', 'acceptance.yml': yaml, 'brief.txt': 'Build it.\n', 'README.md': '# Sample\n', ...(config ? { '.code-forge.yml': config } : {}) })) await writeFile(path.join(f.repo, name), body);
  git(f, 'init', '-q', '-b', 'main'); git(f, 'add', '.');
  git(f, '-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixture');
  const cfg = { proof: { isolation: isolated ? 'worktree' : 'export', worktree: { dir: '.code-forge/worktrees' } } };
  const hash = configHash(cfg);
  const record = { run_id: RUN, project: 'open-fixture', workspace: f.repo, status: 'active', blocks: {}, orphans: [], config: { hash, snapshots: { [hash]: cfg }, pins: {} } };
  change(record);
  await mkdir(path.join(f.home, '.code-forge/runs'), { recursive: true });
  await writeFile(path.join(f.home, '.code-forge/runs', `${RUN}.json`), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  await writeFile(path.join(f.home, '.code-forge/runs', `${RUN}.key`), KEY, { mode: 0o600 });
  return f;
}
function launch(f, args, { node = false, nodePath = '/nonexistent' } = {}) {
  return new Promise((resolve, reject) => {
    const p = spawn(node ? process.execPath : BINARY, node ? [CLI, ...args] : args, { cwd: f.repo, env: { PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: nodePath, CODE_FORGE_KEY_BACKEND: 'file', CODE_FORGE_NO_ERROR_LOG: '1' }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    p.stdout.on('data', (b) => { stdout += b; }); p.stderr.on('data', (b) => { stderr += b; });
    p.on('error', reject); p.on('close', (code) => resolve({ code, stdout, stderr }));
  });
}
const open = (extra = [], id = 'B1') => ['block', 'open', id, '--run', RUN, '--level', 'L0', '--owned', 'sample.mjs', '--acceptance', 'acceptance.yml', ...extra];
async function parity(f, args, fallback = false) {
  const r = await runParity(args, { fixture: f, goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: fallback ? process.execPath : '/nonexistent' } });
  assert.deepEqual(r.diffs, [], `${args.join(' ')}: zero exit, stream, record and signed row diffs`);
  assert.equal(r.equal, true);
}
const normalize = (s, f) => s.replaceAll(f.repo, '<repo>').replaceAll(f.home, '<home>').replace(/\d{4}-\d\d-\d\dT[\d:.]+Z/g, '<time>');
async function rows(f) {
  return (await readFile(path.join(f.home, '.code-forge/ledger/open-fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
}

test('explicit block open has native byte parity, signed rows, git isolation and concurrent serialization', async () => {
  const speed = await launch(await fixture(), open());
  assert.equal(speed.code, 0, `explicit level succeeds with CODE_FORGE_NODE=/nonexistent: ${speed.stderr}`);
  for (const isolated of [false, true]) {
    const f = await fixture({ isolated });
    for (const extra of [[], ['--json'], ['--brief', 'brief.txt'], ['--tests', 'sample.mjs', 'app/[id]/test.ts', '{one,two}'], ['--base', 'main', '--attempt', '2', '--lines', '90'], ['--kind', 'contract']]) await parity(f, open(extra));
    const copies = [];
    for (const node of [true, false]) {
      const direct = await fixture({ isolated });
      const result = await launch(direct, open(['--brief', 'brief.txt', '--json']), { node });
      assert.equal(result.code, 0, result.stderr);
      assert.equal(result.stderr, '');
      const written = await rows(direct);
      assert.deepEqual(written.map((r) => r.event), isolated ? ['worktree.created', 'dispatch'] : ['dispatch'], 'exact signed row order');
      assert.deepEqual(written.map((r) => verifyRow(r, KEY).ok), written.map(() => true), 'every row verifies');
      const record = await readFile(path.join(direct.home, '.code-forge/runs', `${RUN}.json`), 'utf8');
      const worktrees = git(direct, 'worktree', 'list', '--porcelain');
      const branches = git(direct, 'for-each-ref', '--format=%(refname) %(objectname)', 'refs/heads');
      copies.push([normalize(result.stdout, direct), normalize(record, direct), normalize(worktrees, direct), branches]);
      if (isolated) {
        const tree = JSON.parse(record).blocks.B1.worktree;
        assert.equal(git({ ...direct, repo: tree.path }, 'rev-parse', 'HEAD').trim(), tree.base_sha, 'tree HEAD equals recorded base');
        assert.equal(git({ ...direct, repo: tree.path }, 'symbolic-ref', '--short', 'HEAD').trim(), tree.branch, 'tree branch equals recorded branch');
      }
    }
    assert.deepEqual(copies[1], copies[0], 'record bytes, CLI bytes, worktree list and branch tips equal Node');
    const based = await fixture({ isolated });
    const old = git(based, 'rev-parse', 'HEAD').trim();
    await writeFile(path.join(based.repo, 'sample.mjs'), 'export const x = 2;\n');
    git(based, 'add', 'sample.mjs');
    git(based, '-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'second');
    await parity(based, open(['--base', old, '--json']));
    const explicit = await launch(based, open(['--base', old, '--json']));
    assert.equal(explicit.code, 0, explicit.stderr);
    assert.equal(JSON.parse(explicit.stdout).base_sha, old, 'explicit older base is preserved');
    if (isolated) assert.equal(git({ ...based, repo: JSON.parse(explicit.stdout).worktree.path }, 'rev-parse', 'HEAD').trim(), old, 'new worktree checks out explicit older base');
    await parity(await fixture({ isolated, config: `version: 1\nproof: {isolation: ${isolated ? 'export' : 'worktree'}}\n` }), open());
  }
  await parity(await fixture({ yaml: '- clause: human review\n  kind: human_check\n- clause: automated\n  tests: [sample, other]\n  note: "café <&>"\n' }), open(['--json']));
  await parity(await fixture({ config: 'version: 1\nlevels: {coder_floor_docs: L2}\n' }), open(['--kind', 'docs', '--json']));
  const stopped = await fixture({ isolated: true });
  assert.equal((await launch(stopped, open(), { node: true })).code, 0);
  assert.equal((await launch(stopped, ['block', 'claim', 'B1', 'claimed.mjs', '--run', RUN], { node: true })).code, 0);
  assert.equal((await launch(stopped, ['block', 'stop', 'B1', '--run', RUN, '--reason', 'retry'], { node: true })).code, 0);
  await parity(stopped, open(['--attempt', '2', '--json']));
  const reopened = await launch(stopped, open(['--attempt', '2']));
  assert.equal(reopened.code, 0, reopened.stderr);
  assert.match(reopened.stdout, /kept claims: 1\nworktree .* · reused\n/);
  assert.deepEqual((await rows(stopped)).map((r) => r.event), ['worktree.created', 'dispatch', 'block.claim', 'block.stop', 'dispatch'], 'reuse adds only dispatch');
  const concurrent = await fixture({ isolated: true });
  const results = await Promise.all(['B1', 'B2'].map((id) => launch(concurrent, open([], id))));
  assert.deepEqual(results.map((r) => r.code), [0, 0], JSON.stringify(results));
  const written = await rows(concurrent);
  assert.deepEqual(written.map((r) => r.event), ['worktree.created', 'dispatch', 'worktree.created', 'dispatch'], 'run lock keeps each publication contiguous');
  assert.deepEqual(written.map((r) => verifyRow(r, KEY).ok), [true, true, true, true]);
  assert.equal(Object.keys(JSON.parse(await readFile(path.join(concurrent.home, '.code-forge/runs', `${RUN}.json`), 'utf8')).blocks).length, 2, 'both concurrent blocks survive');
  const oracle = await fixture({ isolated: true });
  for (const id of written.filter((r) => r.event === 'dispatch').map((r) => r.block)) assert.equal((await launch(oracle, open([], id), { node: true })).code, 0, 'Node replays observed serialization order');
  const recordBytes = (f) => readFile(path.join(f.home, '.code-forge/runs', `${RUN}.json`), 'utf8');
  assert.equal(normalize(await recordBytes(concurrent), concurrent), normalize(await recordBytes(oracle), oracle), 'concurrent record bytes equal Node in observed order');
  const oracleRows = await rows(oracle);
  assert.deepEqual(oracleRows.map((r) => verifyRow(r, KEY).ok), [true, true, true, true]);
  const stableRows = (rs, f) => rs.map(({ mac, ts, ...r }) => JSON.parse(normalize(JSON.stringify(r), f)));
  assert.deepEqual(stableRows(written, concurrent), stableRows(oracleRows, oracle), 'all concurrent signed row fields equal Node');
  assert.equal(normalize(git(concurrent, 'worktree', 'list', '--porcelain'), concurrent), normalize(git(oracle, 'worktree', 'list', '--porcelain'), oracle), 'concurrent git registrations equal Node');
  assert.equal(git(concurrent, 'for-each-ref', '--format=%(refname) %(objectname)', 'refs/heads'), git(oracle, 'for-each-ref', '--format=%(refname) %(objectname)', 'refs/heads'), 'concurrent branch tips equal Node');
});

test('block open refusals and usage match Node; unsupported lanes hand over unchanged', async () => {
  const refused = await launch(await fixture(), open(['--tests', '*.mjs']));
  assert.equal(refused.code, 2, 'native exact-path refusal works without Node');
  assert.equal(refused.stderr, 'block open: --tests takes exact paths (a file or a directory); "*" and "?" are refused: "*.mjs"\n');
  const f = await fixture();
  for (const extra of [['--tests', '*.mjs'], ['--tests', '?'], ['--tests', '-x'], ['--tests', '../test'], ['--owned', 'a,b'], ['--owned', 'a b'], ['--owned', '/absolute'], ['--owned', 'a//b'], ['--level', 'L1'], ['--lines', '0'], ['--lines', 'NaN'], ['--attempt', '0'], ['--kind', 'unknown'], ['--brief', path.join(f.home, 'outside.txt')], ['--base', 'missing'], ['--remove-worktree']]) {
    await writeFile(path.join(f.home, 'outside.txt'), 'outside');
    await parity(f, open(extra));
  }
  for (const yaml of ['[]\n', '- clause: works\n  tests: []\n', '- clause: human\n  kind: human_check\n  tests: [bad]\n', '- clause: [broken\n']) await parity(await fixture({ yaml }), open(), yaml.includes('[broken'));
  const overlap = await fixture({ change: (r) => { r.blocks.B2 = { status: 'open', owned_files: ['sample.mjs'], owned_kinds: { 'sample.mjs': 'file' } }; } });
  await parity(overlap, open());
  for (const args of [['block', 'open', '--level', 'L0'], ['block', 'open', 'B1', '--run', RUN, '--level', 'L0'], ['block', 'open', 'bad/id', ...open().slice(3)]]) await parity(f, args);
  const docs = await fixture();
  await parity(f, open().map((arg) => arg === 'L0' ? 'L9' : arg));
  for (const extra of [['--json=true'], ['--owned='], ['--acceptance'], ['--attempt', '01'], ['--lines', '9007199254740992'], ['--owned', 'a\\b'], ['--owned', '*[x]'], ['--owned', 'a/{b'], ['--tests', 'a b']]) await parity(f, open(extra));
  await parity(docs, ['block', 'open', 'Docs', '--run', RUN, '--level', 'L0', '--owned', 'README.md', '--acceptance', 'acceptance.yml', '--json']);
  for (const args of [open().filter((a, i, all) => a !== '--level' && all[i - 1] !== '--level'), open(['--after', 'B0']), open(['--after-outcome', 'S1'])]) {
    const result = await launch(f, args);
    assert.equal(result.code, args.includes('--level') ? 1 : 2, `${args.join(' ')} refuses natively without Node`);
    await parity(f, args);
  }
  for (const args of [open(['--head', 'HEAD'])]) {
    assert.equal((await launch(f, args)).code, 127, `${args.join(' ')} hands original argv to missing Node`);
    await parity(f, args, true);
  }
  for (const change of [(r) => { r.autopilot = { status: 'active', until: '2099-01-01T00:00:00.000Z' }; }, (r) => { r.autopilot_approvals = [{ status: 'active', until: '2000-01-01T00:00:00.000Z', approval_id: 'apr-test', key: 'budget.usd' }]; }]) {
    const delegated = await fixture({ change });
    const file = path.join(delegated.home, '.code-forge/runs', `${RUN}.json`), before = await readFile(file);
    assert.equal((await launch(delegated, open())).code, 127, 'grant or pending approval delegates before mutation');
    assert.deepEqual(await readFile(file), before, 'fallback preserves run bytes');
    await parity(delegated, open(), true);
  }
});
