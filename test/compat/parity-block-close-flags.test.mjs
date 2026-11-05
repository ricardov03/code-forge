import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { serveDaemon } from '../helpers/daemon-fixture.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { announceWorker } from '../../src/worker/queue.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
// Keep fixture Unix sockets below macOS's sockaddr_un path limit.
const scratch = await realpath(await mkdtemp('/tmp/cf-close-flags-'));
const BINARY = path.join(scratch, 'package/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const RUN = 'r-close-flags', SLUG = 'close-flags', KEY = Buffer.alloc(32, 27);
const args = ['block', 'close', 'B1', '--run', RUN];
const hash = (text) => createHash('sha256').update(text).digest('hex');
const lockSignal = 'TEST waiting on run lock\n';
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'package/bin'));
  await symlink(CLI, path.join(scratch, 'package/bin/code-forge.mjs'));
  const source = path.join(ROOT, 'native/state/run.go');
  const overlaySource = path.join(scratch, 'run.go');
  const original = await readFile(source, 'utf8');
  const anchor = '\t\ttime.Sleep(lockPoll)';
  assert.equal(original.split(anchor).length, 2, 'one lock polling instrumentation point');
  await writeFile(overlaySource, original.replace(anchor, `\t\tfmt.Fprint(os.Stderr, ${JSON.stringify(lockSignal)})\n${anchor}`));
  const overlay = path.join(scratch, 'overlay.json');
  await writeFile(overlay, JSON.stringify({ Replace: { [source]: overlaySource } }));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  const built = spawnSync('go', ['build', '-overlay', overlay, '-ldflags', `-X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}`, '-o', BINARY, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, GOPROXY: 'off', GOTOOLCHAIN: 'local' },
  });
  assert.equal(built.status, 0, built.stderr);
});

function git(repo, argv) {
  const result = spawnSync('git', argv, { cwd: repo, encoding: 'utf8', env: { ...process.env,
    GIT_AUTHOR_DATE: '2020-01-01T00:00:00Z', GIT_COMMITTER_DATE: '2020-01-01T00:00:00Z' } });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}
async function fixture(kind = 'plain', config = {}) {
  const root = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const value = 1;\n');
  git(f.repo, ['init', '-qb', 'main']);
  git(f.repo, ['config', 'user.name', 'Fixture']);
  git(f.repo, ['config', 'user.email', 'f@example.invalid']);
  git(f.repo, ['config', 'commit.gpgsign', 'false']);
  git(f.repo, ['add', '.']); git(f.repo, ['commit', '-qm', 'base']);
  const base = git(f.repo, ['rev-parse', 'HEAD']);
  const inTree = ['tree', 'dirty', 'auto-tree'].includes(kind);
  const tree = inTree ? path.join(f.repo, '.code-forge/worktrees/B1') : f.repo;
  if (inTree) git(f.repo, ['worktree', 'add', '-qb', 'block-B1', tree, base]);
  const content = 'export const value = 2;\n';
  await writeFile(path.join(tree, 'sample.mjs'), content);
  if (kind.startsWith('auto') || kind === 'worker-down') await writeFile(path.join(tree, 'outsider.mjs'), 'export const outsider = true;\n');
  if (inTree && kind !== 'dirty') { git(tree, ['add', '.']); git(tree, ['commit', '-qm', 'changes']); }
  const block = { status: 'open', owned_files: ['sample.mjs'], base_sha: base, tree_at_open: {}, kind: 'code', test_files: [],
    ...(inTree ? { worktree: { path: tree, branch: 'block-B1', base_sha: base } } : {}) };
  const record = { run_id: RUN, project: SLUG, workspace: f.repo, status: 'active',
    worker: { pid: process.pid, started_at: await processStartTime(process.pid) },
    config: initialConfigState(config), blocks: { B1: block }, orphans: [] };
  const dir = path.join(f.home, '.code-forge/runs');
  await mkdir(dir, { recursive: true });
  await writeFile(path.join(dir, `${RUN}.json`), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  await writeFile(path.join(dir, `${RUN}.key`), KEY, { mode: 0o600 });
  const logs = path.join(f.repo, '.code-forge/runs', RUN);
  await mkdir(logs, { recursive: true });
  await writeFile(path.join(logs, 'B1.log'), 'Completed implementation.\n');
  // Wait-block reads the same informational mirror as the real worker.
  const mirror = { ...record }; delete mirror.worker;
  await writeFile(path.join(f.repo, '.code-forge/runs', `${RUN}.json`), JSON.stringify(mirror, null, 2) + '\n');
  const saved = process.env.HOME;
  try {
    process.env.HOME = f.home;
    const rows = [{ event: 'dispatch', block: 'B1', level: 'L2' },
      { event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: hash(content) }];
    if (kind.startsWith('auto')) rows.push({ event: 'review.approved', block: 'B1', file: 'outsider.mjs', content_hash: hash('export const outsider = true;\n') });
    for (const row of rows) await appendRow(signRow({ run: RUN, ...row }, KEY), { slug: SLUG });
    if (kind !== 'worker-down') announceWorker(f.repo, { pid: process.pid, run: RUN });
  } finally { process.env.HOME = saved; }
  return f;
}
async function snapshot(root) {
  const files = [];
  async function walk(dir) {
    for (const e of (await readdir(dir, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = path.join(dir, e.name);
      if (e.isDirectory()) { files.push([path.relative(root, file), 'directory']); await walk(file); }
      else files.push([path.relative(root, file), await readFile(file)]);
    }
  }
  await walk(root); return files;
}
function launch(f, argv, env = {}, onLock = () => {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(BINARY, argv, { cwd: f.repo, env: { PATH: process.env.PATH, HOME: f.home,
      CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', ...env } });
    let stdout = '', stderr = '';
    child.stdout.on('data', (b) => { stdout += b; });
    child.stderr.on('data', (b) => { stderr += b; if (stderr.includes(lockSignal)) onLock(); });
    child.on('error', reject); child.on('close', (code) => resolve({ code, stdout, stderr }));
  });
}
async function prepare(copy, outcome = 'approved') {
  const daemon = await serveDaemon(copy, RUN, async (req) => req.method === 'rows' ? { rows: [] } : {
    block: 'B1', files: [{ file: 'outsider.mjs', status: 'done', outcome }], done: outcome !== 'pending',
  });
  return async () => {
    await daemon.cleanup();
    // The shared harness compares .code-forge; also expose git's registrations
    // and branch refs there so removal and branch retention are checked.
    const facts = { worktrees: git(copy.repo, ['worktree', 'list', '--porcelain']),
      branches: git(copy.repo, ['for-each-ref', '--format=%(refname) %(objectname)', 'refs/heads']) };
    await writeFile(path.join(copy.repo, '.code-forge/git-state.json'), JSON.stringify(facts));
  };
}

test('close flags run natively with byte parity for process, signed state and git worktrees', async () => {
  const speed = await launch(await fixture(), [...args, '--auto-claim']);
  assert.equal(speed.code, 0, `auto-claim with no extra paths succeeds without Node: ${speed.stderr}`);
  /** @type {[string, string[], number][]} */
  const cases = [
    ['plain', ['--auto-claim'], 0], ['auto', ['--auto-claim'], 0],
    ['tree', ['--remove-worktree'], 0], ['dirty', ['--remove-worktree'], 1],
    ['auto-tree', ['--auto-claim', '--remove-worktree'], 0],
    ['worker-down', ['--auto-claim'], 1],
  ];
  for (const [kind, flags, expected] of cases) {
    for (const json of [false, true]) {
      const f = await fixture(kind), argv = [...args, ...flags, ...(json ? ['--json'] : [])];
      const parity = await runParity(argv, { fixture: f, goCommand: BINARY, samePaths: true, tempRoot: '/tmp', prepare,
        env: { CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent' } });
      assert.deepEqual(parity.diffs, [], `${kind}, json=${json}: zero process, state, signature and git differences`);
      const cleanup = await prepare(f);
      try {
        const direct = await launch(f, argv);
        assert.equal(direct.code, expected, `${kind}: native outcome with Node unavailable: ${direct.stderr}`);
        const rows = (await readFile(path.join(f.home, '.code-forge/ledger', `${SLUG}.jsonl`), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
        assert.equal(rows.filter((r) => !verifyRow(r, KEY).ok).length, 0, `${kind}: all ${rows.length} rows carry valid MACs`);
        assert.equal(rows.filter((r) => r.event === 'block.claim').length, kind.startsWith('auto') ? 1 : kind === 'worker-down' ? 1 : 0, `${kind}: exact automatic claim count`);
        if (flags.includes('--remove-worktree')) {
          assert.equal(rows.at(-1).event, kind === 'dirty' ? 'worktree.remove_failed' : 'worktree.removed', 'exact removal event');
          assert.equal(git(f.repo, ['show-ref', '--verify', '--hash', 'refs/heads/block-B1']).length, 40, 'the block branch is kept');
        }
      } finally { await cleanup(); }
    }
  }
  for (const outcome of ['refused', 'pending']) {
    const f = await fixture('auto');
    const parity = await runParity([...args, '--auto-claim', '--json'], { fixture: f, goCommand: BINARY, samePaths: true, tempRoot: '/tmp',
      prepare: (copy) => prepare(copy, outcome), env: { CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(parity.diffs, [], `${outcome}: exact auto-claim-review error and claim writes`);
  }
});

test('close flag refusals, concurrent claims and whole-command handovers preserve their boundaries', async () => {
  const waived = await launch(await fixture(), [...args, '--no-require-reviews']);
  assert.equal(waived.code, 2, 'no terminal: native human waiver refusal exits 2 without Node');
  for (const flags of [
    ['--no-require-reviews'], ['--auto-claim', '--no-require-reviews'], ['--remove-worktree'],
    ['--auto-claim', '--remove-worktree', '--no-require-reviews'],
    ['--auto-claim=true'], ['--auto-claim', '--auto-claim'], ['--remove-worktree=true'],
    ['--no-require-reviews=false'], ['--no-require-reviews', '--no-require-reviews'],
  ]) {
    const f = await fixture();
    const parity = await runParity([...args, ...flags, '--json'], { fixture: f, goCommand: BINARY,
      env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(parity.diffs, [], `${flags}: exact refusal, exit and zero writes`);
  }
  for (const flags of [['--auto-claim'], ['--remove-worktree'], ['--no-require-reviews'], ['--auto-claim', '--remove-worktree', '--no-require-reviews']]) {
    /** @type {[Record<string, any>, string[], boolean][]} */
    const handovers = [
      [{ hooks: { on_block_end: { argv: ['/usr/bin/true'] } } }, [], false],
      [{ close: { require_human: true } }, [], false],
      [{ review: { external: { required: true, name: 'owner' } } }, [], false],
      [{}, ['--decide'], false], [{}, [], true],
    ];
    for (const [config, extra, grant] of handovers) {
      const f = await fixture('auto-tree', config);
      if (grant) {
        const file = path.join(f.home, '.code-forge/runs', `${RUN}.json`);
        const record = JSON.parse(await readFile(file, 'utf8'));
        record.autopilot = { status: 'active', until: '2099-01-01T00:00:00.000Z' };
        await writeFile(file, JSON.stringify(record, null, 2) + '\n');
      }
      const before = await Promise.all([snapshot(f.home), snapshot(path.join(f.repo, '.code-forge'))]);
      const result = await launch(f, [...args, ...flags, ...extra, '--json']);
      assert.equal(result.code, 127, `${JSON.stringify(config)}, grant=${grant}, ${flags}, ${extra}: whole close hands over`);
      assert.deepEqual(await Promise.all([snapshot(f.home), snapshot(path.join(f.repo, '.code-forge'))]), before, 'zero Go state writes before handover');
    }
  }
  const f = await fixture('auto');
  const cleanup = await prepare(f);
  const lock = path.join(f.home, '.code-forge/runs', `${RUN}.lock`);
  await mkdir(lock);
  let released = false;
  try {
    const result = await launch(f, [...args, '--auto-claim'], {}, () => {
      if (released) return; released = true;
      void (async () => {
        const file = path.join(f.home, '.code-forge/runs', `${RUN}.json`);
        const record = JSON.parse(await readFile(file, 'utf8')); record.concurrent_writer = 'preserved';
        await writeFile(file, JSON.stringify(record, null, 2) + '\n');
        await rm(lock, { recursive: true });
      })();
    });
    assert.equal(result.code, 0, result.stderr);
    assert.equal(released, true, 'auto-claim actually waited for the shared run lock');
    const record = JSON.parse(await readFile(path.join(f.home, '.code-forge/runs', `${RUN}.json`), 'utf8'));
    assert.equal(record.concurrent_writer, 'preserved', 'claim and close preserve the concurrent writer field');
    assert.deepEqual(record.blocks.B1.owned_files, ['sample.mjs', 'outsider.mjs'], 'one new ownership entry after lock release');
  } finally { await rm(lock, { recursive: true, force: true }); await cleanup(); }
});
