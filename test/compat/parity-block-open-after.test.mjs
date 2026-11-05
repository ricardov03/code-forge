import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { configHash } from '../../src/state/config-snapshot.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { runParity } from '../helpers/parity.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-open-after-'));
const BINARY = path.join(scratch, 'package/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const RUN = 'r-after-fixture', KEY = Buffer.alloc(32, 7);
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'package/bin'));
  await symlink(CLI, path.join(scratch, 'package/bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const built = spawnSync('go', ['build', '-trimpath', '-ldflags', `-s -w -X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}`, '-o', BINARY, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' },
  });
  assert.equal(built.status, 0, built.stderr);
});
function git(f, ...args) {
  const r = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8', env: { PATH: process.env.PATH, HOME: f.home, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull, GIT_AUTHOR_DATE: '2026-01-01T00:00:00Z', GIT_COMMITTER_DATE: '2026-01-01T00:00:00Z' } });
  assert.equal(r.status, 0, r.stderr);
  return r.stdout;
}
/** @param {{isolated?: boolean, change?: (record: any) => void, outcome?: any, pipeline?: string | null, levelRows?: any[]}} [options] */
async function fixture({ isolated = false, change = () => {}, outcome = 'approved', pipeline, levelRows = [] } = {}) {
  const root = await mkdtemp(path.join(scratch, 'fixture-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  const digest = createHash('sha256').update('export const x = 1;\n').digest('hex');
  const files = { '.gitignore': '.code-forge/\n', 'sample.mjs': 'export const x = 1;\n', 'acceptance.yml': '- clause: works\n  tests: [sample]\n', 'README.md': '# Sample\n' };
  for (const [name, body] of Object.entries(files)) await writeFile(path.join(f.repo, name), body);
  if (pipeline !== null) await writeFile(path.join(f.repo, 'code-forge.pipeline.yml'), pipeline ?? 'root: .\nsteps:\n  - id: design\n    skill: designer\n    outcome: outcome.json\n    approval: human\n');
  if (outcome !== null) await writeFile(path.join(f.repo, 'outcome.json'), typeof outcome === 'string' ? JSON.stringify({ skill: 'designer', status: outcome, files: [{ path: 'sample.mjs', sha256: digest }] }) : JSON.stringify(outcome));
  git(f, 'init', '-q', '-b', 'main'); git(f, 'add', '.');
  git(f, '-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixture');
  const cfg = { proof: { isolation: isolated ? 'worktree' : 'export', worktree: { dir: '.code-forge/worktrees' } } };
  const hash = configHash(cfg);
  const record = { run_id: RUN, project: 'after-fixture', workspace: f.repo, status: 'active', blocks: { Done: { status: 'closed', owned_files: [] }, Pending: { status: 'open', owned_files: [] }, Waiting: { status: 'awaiting_human_check', owned_files: [] } }, orphans: [], config: { hash, snapshots: { [hash]: cfg }, pins: {} } };
  change(record);
  await mkdir(path.join(f.home, '.code-forge/runs'), { recursive: true });
  await writeFile(path.join(f.home, '.code-forge/runs', `${RUN}.json`), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  await writeFile(path.join(f.home, '.code-forge/runs', `${RUN}.key`), KEY, { mode: 0o600 });
  if (levelRows.length) {
    await mkdir(path.join(f.home, '.code-forge/ledger'));
    await writeFile(path.join(f.home, '.code-forge/ledger/after-fixture.jsonl'), levelRows.map((row) => JSON.stringify(signRow({ event: 'autopilot.level', run: RUN, block: 'B1', ...row }, KEY))).join('\n') + '\n');
  }
  return f;
}
function launch(f, args, { node = false, fallback = false } = {}) {
  return new Promise((resolve, reject) => {
    const p = spawn(node ? process.execPath : BINARY, node ? [CLI, ...args] : args, { cwd: f.repo, env: { PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: fallback ? process.execPath : '/nonexistent', CODE_FORGE_KEY_BACKEND: 'file', CODE_FORGE_NO_ERROR_LOG: '1' }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    p.stdout.on('data', (b) => { stdout += b; }); p.stderr.on('data', (b) => { stderr += b; });
    p.on('error', reject); p.on('close', (code) => resolve({ code, stdout, stderr }));
  });
}
const open = (extra = [], level = true) => ['block', 'open', 'B1', '--run', RUN, ...(level ? ['--level', 'L0'] : []), '--owned', 'sample.mjs', '--acceptance', 'acceptance.yml', ...extra];
async function parity(f, args, fallback = false) {
  const r = await runParity(args, { fixture: f, goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: fallback ? process.execPath : '/nonexistent' } });
  assert.deepEqual(r.diffs, [], `${args.join(' ')}: zero process, run, signed ledger, queue, lock and worktree diffs`);
  assert.equal(r.equal, true);
}
const runFile = (f) => path.join(f.home, '.code-forge/runs', `${RUN}.json`);
const normalize = (s, f) => s.replaceAll(f.repo, '<repo>').replaceAll(f.home, '<home>').replace(/\d{4}-\d\d-\d\dT[\d:.]+Z/g, '<time>');
async function rows(f) {
  return (await readFile(path.join(f.home, '.code-forge/ledger/after-fixture.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
}

test('open dependencies and recorded default levels run natively with byte parity and serialized writes', async () => {
  for (const isolated of [false, true]) {
    const f = await fixture({ isolated });
    const speed = await launch(f, open(['--after', 'Done', 'Done', '--after-outcome', 'design', 'design', '--json']));
    assert.equal(speed.code, 0, `ported dependencies run without Node: ${speed.stderr}`);
    assert.equal(speed.stderr, '');
    const record = JSON.parse(await readFile(runFile(f), 'utf8'));
    assert.deepEqual(record.blocks.B1.after, ['Done'], 'exactly one unique block dependency');
    assert.deepEqual(record.blocks.B1.after_outcome, [{ step: 'design', sha256: createHash('sha256').update(await readFile(path.join(f.repo, 'outcome.json'))).digest('hex') }], 'exactly one byte-hashed specialist dependency');
    const written = await rows(f);
    assert.deepEqual(written.map((r) => r.event), isolated ? ['worktree.created', 'dispatch'] : ['dispatch']);
    assert.deepEqual(written.map((r) => verifyRow(r, KEY).ok), written.map(() => true), 'all signatures verify');
    for (const extra of [[], ['--json']]) {
      for (const dependencies of [['--after', 'Done', 'Done'], ['--after-outcome', 'design', 'design'], ['--after', 'Done', '--after-outcome', 'design']]) await parity(await fixture({ isolated }), open([...dependencies, ...extra]));
      const defaults = await fixture({ isolated, levelRows: [{ level: 'L1' }, { level: 'L2' }, { run: 'r-other', level: 'L3' }] });
      await parity(defaults, open(extra, false));
      const chosen = await launch(defaults, open(['--json'], false));
      assert.equal(chosen.code, 0, chosen.stderr);
      assert.deepEqual([JSON.parse(chosen.stdout).level, JSON.parse(chosen.stdout).source], ['L2', 'autopilot']);
    }
    const concurrent = await fixture({ isolated });
    const concurrentArgs = open(['--after', 'Done', '--tests', 'sample.mjs']);
    const results = await Promise.all([launch(concurrent, concurrentArgs), launch(concurrent, concurrentArgs)]);
    assert.deepEqual(results.map((r) => r.code).sort(), [0, 1], 'one concurrent writer commits and one refuses');
    assert.equal(results.find((r) => r.code === 1).stderr, 'block open: block B1 is already open\n');
    assert.equal((await rows(concurrent)).filter((r) => r.event === 'dispatch').length, 1, 'one signed dispatch after concurrent writers');
    assert.deepEqual((await readdir(path.join(concurrent.home, '.code-forge'), { recursive: true })).filter((name) => name.endsWith('.lock')), [], 'all run and ledger locks released');
    if (isolated) assert.equal(git(concurrent, 'worktree', 'list', '--porcelain').match(/^worktree /gm).length, 2, 'main and one registered block worktree');
    const oracle = await fixture({ isolated });
    const oracleResults = await Promise.all([launch(oracle, concurrentArgs, { node: true }), launch(oracle, concurrentArgs, { node: true })]);
    assert.deepEqual(results.map((r) => [r.code, normalize(r.stdout, concurrent), r.stderr]).sort(), oracleResults.map((r) => [r.code, normalize(r.stdout, oracle), r.stderr]).sort(), 'concurrent exit codes and CLI bytes equal Node');
    assert.equal(normalize(await readFile(runFile(concurrent), 'utf8'), concurrent), normalize(await readFile(runFile(oracle), 'utf8'), oracle), 'concurrent run record bytes equal Node');
    assert.equal(normalize(git(concurrent, 'worktree', 'list', '--porcelain'), concurrent), normalize(git(oracle, 'worktree', 'list', '--porcelain'), oracle), 'concurrent worktree registrations equal Node');
    assert.equal(git(concurrent, 'for-each-ref', '--format=%(refname) %(objectname)', 'refs/heads'), git(oracle, 'for-each-ref', '--format=%(refname) %(objectname)', 'refs/heads'), 'branch names and commit tips equal Node');
    const stableRows = async (f) => (await rows(f)).map((r) => {
      assert.equal(verifyRow(r, KEY).ok, true, 'each concurrent row verifies');
      const { mac, ...body } = r;
      return normalize(JSON.stringify(body), f);
    });
    assert.deepEqual(await stableRows(concurrent), await stableRows(oracle), 'every concurrent signed row field equals Node');
  }
  // A rules decision uses a fresh UUID in both engines. Check its UUID and MAC
  // before normalizing those volatile fields for the direct byte comparison.
  for (const [owned, lines, lane] of [['sample.mjs', '200', 'L0'], ['sample.mjs', '201', 'L1'], ['src/migrationHelper.mjs', '1', 'L2'], ['middleware.mjs', '1', 'L2'], ['README.md', '1', 'L0']]) {
    const copies = [];
    for (const node of [true, false]) {
      const f = await fixture({ change: (r) => { r.mode = 'brief'; } });
      const args = open(['--lines', lines, '--json'], false).map((arg) => arg === 'sample.mjs' ? owned : arg);
      const result = await launch(f, args, { node });
      assert.equal(result.code, 0, result.stderr);
      assert.equal(JSON.parse(result.stdout).asked, lane, 'rules lane before the docs floor');
      assert.equal(JSON.parse(result.stdout).source, 'rules');
      const written = await rows(f);
      assert.deepEqual(written.map((r) => r.event), ['decision', 'dispatch'], 'decision is signed before dispatch');
      assert.match(written[0].decision_id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
      for (const row of written) assert.equal(verifyRow(row, KEY).ok, true);
      const stable = written.map(({ mac, decision_id, ...row }) => row);
      copies.push([normalize(result.stdout, f), result.stderr, normalize(await readFile(runFile(f), 'utf8'), f), normalize(JSON.stringify(stable), f)]);
    }
    assert.deepEqual(copies[1], copies[0], 'brief rules output, record and verified row bodies equal Node');
  }
});

test('dependency refusals preserve exact errors and JSON; unsupported cases hand over before Go writes', async () => {
  const refused = await launch(await fixture(), open(['--after', 'Pending', '--json']));
  assert.equal(refused.code, 1, 'unmet dependency refuses natively without Node');
  assert.equal(refused.stderr, 'block open: block B1: --after dependencies not closed: Pending (open)\n');
  assert.equal(refused.stdout, '{"block":"B1","event":"refused","code":"after-not-done","message":"block B1: --after dependencies not closed: Pending (open)"}\n');
  for (const json of [[], ['--json']]) {
    for (const extra of [['--after', 'Pending', 'Missing', 'Pending', 'Waiting'], ['--after', 'B1'], ['--after', 'Pending', '--after-outcome', 'missing'], ['--after', 'B1', '--after-outcome', 'design'], ['--after-outcome', 'missing'], ['--after'], ['--after-outcome'], ['--after=Done'], ['--after-outcome=design'], ['--after', 'Pending', '--attempt', '0'], ['--after', 'Pending', '--level', 'L9']]) await parity(await fixture(), open([...extra, ...json]));
    for (const status of [null, 'draft', 'blocked', 'failed']) {
      const f = await fixture({ outcome: status });
      await parity(f, open(['--after-outcome', 'design', ...json]));
      const result = await launch(f, open(['--after-outcome', 'design', ...json]));
      assert.equal(result.code, 1);
      assert.equal(result.stderr, `block open: block B1: --after-outcome step design: ${status === null ? 'missing outcome' : `status "${status}": must be approved`}\n`);
    }
    for (const outcome of [{ skill: 'designer', status: 'approved', files: [{ path: 'sample.mjs', sha256: 'wrong' }] }, { skill: 'designer', status: 'approved', files: [], checks: [{ name: 'design', result: 'fail' }] }, { skill: 'designer', status: 'approved', files: [{ path: 'missing', sha256: '' }] }, { skill: 'designer', status: 'approved', files: [{ path: '../outside', sha256: '' }] }]) await parity(await fixture({ outcome }), open(['--after-outcome', 'design', ...json]));
    await parity(await fixture({ pipeline: null }), open(['--after-outcome', 'design', ...json]));
    const absent = await fixture();
    await parity(absent, open(json, false));
    assert.equal((await launch(absent, open(json, false))).stderr, 'block open: no --level given and no autopilot level recorded for B1\n');
    await parity(absent, open(['--lines', 'bad', ...json], false));
    await parity(await fixture({ change: (r) => { r.mode = 'brief'; } }), open(['--lines', 'bad', ...json], false));
  }
  const grants = [
    (r) => { r.autopilot = { status: 'active', until: '2099-01-01T00:00:00.000Z' }; },
    (r) => { r.autopilot = { status: 'active', until: '2000-01-01T00:00:00.000Z' }; },
    (r) => { r.autopilot_approvals = [{ status: 'active', until: '2000-01-01T00:00:00.000Z', approval_id: 'apr-test', key: 'budget.usd' }]; },
  ];
  for (const change of grants) {
    const f = await fixture({ change });
    const before = await readFile(runFile(f));
    assert.equal((await launch(f, open(['--after', 'Done']))).code, 127, 'grant cases delegate to missing Node');
    assert.deepEqual(await readFile(runFile(f)), before, 'handover preserves exact run bytes');
    assert.deepEqual((await readdir(path.join(f.home, '.code-forge'))).sort(), ['runs'], 'handover creates no Go ledger, queue or lock');
    await parity(f, open(['--after', 'Done']), true);
  }
  for (const options of [{ pipeline: 'steps: [broken\n' }, { outcome: { status: 'approved' } }]) {
    const f = await fixture(options);
    assert.equal((await launch(f, open(['--after-outcome', 'design']))).code, 127, 'unported schema/syntax delegates');
    await parity(f, open(['--after-outcome', 'design']), true);
  }
});
