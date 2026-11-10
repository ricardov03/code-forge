import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { isolatedEnv, runParity } from '../helpers/parity.mjs';
import { serveDaemon } from '../helpers/daemon-fixture.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { announceWorker } from '../../src/worker/queue.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const scratch = await realpath(await mkdtemp('/tmp/cf-lfull-'));
const BINARY = path.join(scratch, 'package/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const noNode = path.join(scratch, 'path');
await mkdir(noNode);
const gitPath = spawnSync('which', ['git'], { encoding: 'utf8', env: isolatedEnv() });
assert.equal(gitPath.status, 0, gitPath.stderr);
await symlink(gitPath.stdout.trim(), path.join(noNode, 'git'));
const RUN = 'r-land-full', SLUG = 'land-full', KEY = Buffer.alloc(32, 29);
const argv = ['block', 'land', 'B1', '--run', RUN, '--report', 'report.txt', '--transcript', 'transcript.txt'];
const env = { PATH: noNode, CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_HANDOVER_LOG: '1' };
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'package/bin'));
  await symlink(path.join(ROOT, 'bin/code-forge.mjs'), path.join(scratch, 'package/bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  linkGoBinary(BINARY, { ldflags: `-X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}` });
});

function git(repo, args) {
  const out = spawnSync('git', args, { cwd: repo, encoding: 'utf8', env: isolatedEnv({ ...process.env,
    GIT_AUTHOR_DATE: '2020-01-01T00:00:00Z', GIT_COMMITTER_DATE: '2020-01-01T00:00:00Z' }) });
  assert.equal(out.status, 0, out.stderr);
  return out.stdout.trim();
}
const runFile = (f) => path.join(f.home, '.code-forge/runs', RUN + '.json');
const ledgerFile = (f) => path.join(f.home, '.code-forge/ledger', SLUG + '.jsonl');
async function fixture(shape) {
  const root = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  if (shape === 'symlink') await writeFile(path.join(f.home, 'gate-cwds.txt'), '');
  const live = { version: 1, project: { slug: SLUG }, provider: 'anthropic', engine: 'harness',
    levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } },
    gates: { test: shape === 'no-suite' ? null : ['/usr/bin/true'], lint: null, types: null, format: null },
    ...(shape === 'grant' ? { close: { require_human: true } } : {}),
    ...(shape === 'hook' ? { hooks: { on_block_end: { argv: ['/bin/sh', '-c', 'test -f "$CODE_FORGE_DONE_FILE" && printf "%s\\n" "$CODE_FORGE_BLOCK_STATUS" >> .code-forge/hook-calls.txt'] } } } : {}) };
  if (shape === 'symlink') live.gates.test = ['/bin/sh', '-c',
    `IFS= read -r content < sample.mjs && test "$content" = 'export const value = 2;' && printf '%s\\n' "$PWD" >> "$HOME/gate-cwds.txt"`];
  await writeFile(path.join(f.repo, '.code-forge.yml'), JSON.stringify(live) + '\n');
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\nreport.txt\ntranscript.txt\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const value = 1;\n');
  git(f.repo, ['init', '-qb', 'main']);
  git(f.repo, ['config', 'user.name', 'Fixture']); git(f.repo, ['config', 'user.email', 'f@example.invalid']);
  git(f.repo, ['config', 'commit.gpgsign', 'false']);
  git(f.repo, ['add', '.']); git(f.repo, ['commit', '-qm', 'base']);
  const base = git(f.repo, ['rev-parse', 'HEAD']), tree = path.join(f.repo, '.code-forge/worktrees/B1');
  git(f.repo, ['worktree', 'add', '-qb', 'block-B1', tree, base]);
  const content = 'export const value = 2;\n';
  await writeFile(path.join(tree, 'sample.mjs'), content);
  git(tree, ['commit', '-qam', 'change']);
  const record = { run_id: RUN, project: SLUG, workspace: f.repo, status: 'active',
    config: initialConfigState(live), worker: { pid: process.pid, started_at: await processStartTime(process.pid) },
    blocks: { B1: { status: 'open', owned_files: ['sample.mjs'], owned_kinds: { 'sample.mjs': 'file' }, base_sha: base,
      base_branch: 'main', tree_at_open: {}, kind: 'code', test_files: [], worktree: { path: tree, branch: 'block-B1', base_sha: base } } }, orphans: [],
    ...(shape === 'grant' ? { autopilot: { grant_id: 'ap-land', status: 'active', until: '2099-01-01T00:00:00.000Z',
      scopes: ['close:routine'], deny: [], delegate: 'L2', caps: {}, stop_at: 0.9, started_at: '2020-01-01T00:00:00.000Z',
      stopped_at: null, expired_at: null, link: null } } : {}) };
  for (const dir of [path.dirname(runFile(f)), path.join(f.repo, '.code-forge/runs')]) {
    await mkdir(dir, { recursive: true });
    await writeFile(path.join(dir, RUN + '.json'), JSON.stringify(record, null, 2) + '\n');
  }
  await writeFile(path.join(path.dirname(runFile(f)), RUN + '.key'), KEY, { mode: 0o600 });
  await mkdir(path.dirname(ledgerFile(f)), { recursive: true });
  const rows = [{ event: 'dispatch', block: 'B1', level: 'L2' }, ...(shape === 'grant' ? [{ event: 'gate.done', block: 'B1', all_ok: true }] : []), { event: 'review.approved', block: 'B1', file: 'sample.mjs',
    content_hash: createHash('sha256').update(content).digest('hex') }];
  await writeFile(ledgerFile(f), rows.map((row) => JSON.stringify(signRow({ run: RUN, ...row }, KEY))).join('\n') + '\n');
  await writeFile(path.join(f.repo, 'report.txt'), 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n');
  await writeFile(path.join(f.repo, 'transcript.txt'), 'Completed implementation.\n');
  const saved = process.env.HOME;
  try { process.env.HOME = f.home; announceWorker(f.repo, { pid: process.pid, run: RUN }); }
  finally { process.env.HOME = saved; }
  return f;
}

async function parity(shape, extra, json, expectedCode = 0) {
  const observed = [];
  const result = await runParity([...argv, ...extra, ...(json ? ['--json'] : [])], { fixture: await fixture(shape),
    goCommand: BINARY, tempRoot: '/tmp', samePaths: true, env,
    prepare: async (copy) => {
      if (shape === 'symlink') {
        const alias = copy.repo + '-link';
        await symlink(copy.repo, alias);
        const record = JSON.parse(await readFile(runFile(copy), 'utf8'));
        record.workspace = alias;
        for (const file of [runFile(copy), path.join(copy.repo, '.code-forge/runs', RUN + '.json')]) {
          await writeFile(file, JSON.stringify(record) + '\n');
        }
        assert.notEqual(record.workspace, git(record.workspace, ['rev-parse', '--show-toplevel']), 'workspace uses the symlink spelling');
      }
      const main = git(copy.repo, ['rev-parse', 'main']), tip = git(copy.repo, ['rev-parse', 'block-B1']);
      const daemon = await serveDaemon(copy, RUN, async (req) => {
        const rows = (await readFile(ledgerFile(copy), 'utf8')).trim().split('\n').map((s) => JSON.parse(s));
        if (req.method === 'rows') return { rows };
        if (req.method !== 'wait-block') return null;
        return { block: 'B1', done: true, files: [{ file: 'sample.mjs', status: 'reviewed', outcome: 'approved' }] };
      });
      return async () => {
        await daemon.cleanup();
        assert.deepEqual(daemon.requests.map((req) => req.method).filter((method) => !['ping', 'rows', 'wait-block'].includes(method)),
          [], 'land daemon receives only ping, rows or wait-block requests');
        // handovers is a native-only logs subcommand; query the public Go route.
        const logs = spawnSync(BINARY, ['logs', 'handovers', '--json'], { cwd: copy.repo, encoding: 'utf8',
          env: isolatedEnv({ PATH: process.env.PATH, HOME: copy.home, ...env }) });
        assert.equal(logs.status, 0, logs.stderr);
        const handovers = JSON.parse(logs.stdout);
        const record = JSON.parse(await readFile(runFile(copy), 'utf8'));
        const rows = (await readFile(ledgerFile(copy), 'utf8')).trim().split('\n').map((s) => JSON.parse(s));
        observed.push({ handovers, record, rows, main, tip, after: git(copy.repo, ['rev-parse', 'main']),
          parents: git(copy.repo, ['rev-list', '--parents', '-n', '1', 'main']).split(' '),
          gateCwds: shape === 'symlink' ? (await readFile(path.join(copy.home, 'gate-cwds.txt'), 'utf8')).trim().split('\n') : [],
          worktrees: git(copy.repo, ['worktree', 'list', '--porcelain']),
          hooks: shape === 'hook' && record.blocks.B1.status === 'closed' ? await readFile(path.join(copy.repo, '.code-forge/hook-calls.txt'), 'utf8') : '' });
      };
    },
    checkProcess: (engine, out) => {
      const actual = observed.at(-1);
      assert.equal(actual.handovers.rows.filter((row) => row.verb === 'block' && row.sub === 'land').reduce((n, row) => n + row.count, 0),
        0, `${engine} ${shape}: zero land hand-overs: ${JSON.stringify(actual.handovers)}`);
      assert.equal(out.code, expectedCode, `${engine} ${shape}: exact land exit; ${out.stderr}`);
      assert.equal(out.signal, null, `${engine}: no signal`);
      // Only the displayed elapsed time is volatile; all surrounding bytes
      // still pass through the standard parity comparison.
      for (const stream of ['stdout', 'stderr']) out[stream] = String(out[stream]).replace(/(merge gate: full suite passed \()\d+\.\d+s\)/g, '$1<duration>s)');
      assert.equal(actual.record.blocks.B1.status, 'closed', `${engine}: one closed block`);
      for (const event of ['block.close', 'land.timing', ...(shape === 'hook' ? ['hook.ran'] : []), ...(shape === 'grant' ? ['autopilot.close'] : [])]) {
        assert.equal(actual.rows.filter((row) => row.event === event).length, 1, `${engine}: exactly one ${event} row`);
      }
      assert.equal(actual.rows.filter((row) => row.mac && !verifyRow(row, KEY).ok).length, 0, `${engine}: zero invalid signed rows`);
      const merged = extra.includes('--merge') && expectedCode === 0;
      if (merged) assert.deepEqual(actual.parents, [actual.after, actual.main, actual.tip], `${engine}: merged exactly the checked parents`);
      else assert.equal(actual.after, actual.main, `${engine}: main did not move`);
      if (shape === 'hook') assert.equal(actual.hooks, 'closed\n', `${engine}: hook ran once after done publication`);
      if (shape === 'symlink') {
        assert.equal(actual.gateCwds.length, 2, `${engine}: scoped and full suite each ran once`);
        const tree = actual.record.blocks.B1.worktree.path;
        assert.equal(actual.gateCwds[0], tree, `${engine}: scoped suite ran in block worktree`);
        assert.equal(path.dirname(actual.gateCwds[1]), path.dirname(tree), `${engine}: full suite ran in a sibling trial worktree`);
        assert.match(path.basename(actual.gateCwds[1]), /^\.merge-gate-/, `${engine}: full suite ran inside the trial tree`);
        assert.equal(actual.worktrees.includes('.merge-gate-'), false, `${engine}: trial worktree was removed`);
      }
      if (expectedCode) assert.match(String(out.stderr), /land stopped at merge-gate: full suite unavailable: configure gates.test before merging/);
    } });
  assert.deepEqual(result.diffs, [], `${shape} json=${json}: exact Node/Go bytes, exit, state and rows`);
}

test('block land runs fully in Go for plain, --merge, --no-merge-gate, grant and hook shapes with Node parity and 0 land hand-overs', async (t) => {
  for (const [shape, extra] of /** @type {Array<[string, string[]]>} */ ([['plain', []], ['merge', ['--merge']],
    ['skip', ['--merge', '--no-merge-gate']], ['grant', []], ['hook', []]])) {
    await t.test(shape, async () => { for (const json of [false, true]) await parity(shape, extra, json); });
  }
});

test('land refuses a merge without a configured full test suite in Go with exact Node parity', async () => {
  for (const json of [false, true]) await parity('no-suite', ['--merge'], json, 1);
});

test('land through a symlink workspace runs the merge gate inside the trial worktree with Node parity', async () => {
  for (const json of [false, true]) await parity('symlink', ['--merge'], json);
});

test('land records the specific close preflight hand-over reason without private arguments', async () => {
  const f = await fixture('plain');
  const record = JSON.parse(await readFile(runFile(f), 'utf8'));
  record.blocks.B1.kind = 'light';
  await writeFile(runFile(f), JSON.stringify(record) + '\n');
  const daemon = await serveDaemon(f, RUN, async () => null);
  try {
    const args = ['block', 'land', 'B1', '--run', RUN, '--report', '/private/customer/secret-report',
      '--transcript', '/private/customer/secret-transcript', '--json'];
    const out = await new Promise((resolve, reject) => {
      const child = spawn(BINARY, args, { cwd: f.repo, env: isolatedEnv({ HOME: f.home, ...env }), stdio: ['ignore', 'pipe', 'pipe'] });
      let stdout = '', stderr = '';
      child.stdout.on('data', (chunk) => { stdout += chunk; });
      child.stderr.on('data', (chunk) => { stderr += chunk; });
      child.on('error', reject);
      child.on('close', (code, signal) => resolve({ code, signal, stdout, stderr }));
    });
    assert.deepEqual(daemon.requests.map((req) => req.method).filter((method) => method !== 'ping'),
      [], 'close preflight must hand over before executing child steps');
    assert.equal(out.code, 127, out.stderr);
    assert.equal(out.signal, null);
    assert.equal(out.stdout, '', 'failed hand-over emits no refusal JSON');
    const data = await readFile(path.join(f.home, '.code-forge/native/handovers.jsonl'), 'utf8');
    const rows = data.trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(rows.length, 1, 'only the whole land command records a hand-over');
    assert.deepEqual(Object.keys(rows[0]).sort(), ['at', 'reason', 'sub', 'verb', 'version']);
    assert.equal(rows[0].verb, 'block');
    assert.equal(rows[0].sub, 'land');
    assert.equal(rows[0].reason, 'close-light', 'specific close reason survives the land fallback');
    for (const privateValue of ['B1', RUN, f.repo, f.home, '/private/customer', 'secret-report', 'secret-transcript']) {
      assert.equal(data.includes(privateValue), false, 'hand-over record omits private values');
    }
    assert.equal((await readFile(runFile(f), 'utf8')), JSON.stringify(record) + '\n', 'preflight leaves run state unchanged');
  } finally { await daemon.cleanup(); }
});
