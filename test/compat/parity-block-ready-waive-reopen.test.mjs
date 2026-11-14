import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, readdir, realpath, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { helpPageParity, runParity } from '../helpers/parity.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { newFileState } from '../../src/review/fixloop.mjs';
import { saveState } from '../../src/worker/review-state.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK']) delete process.env[key];
// B603: git records worktree links by real path (macOS: /var -> /private/var); the
// fixture must use the same spelling or the parity copy rewrites the link to a missing path.
const scratch = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-ready-waiver-')));
const BINARY = path.join(scratch, 'package/native/bin/host/code-forge-native');
const RUN = 'r-ready-fixture', KEY = Buffer.alloc(32, 37);
const hash = (s) => createHash('sha256').update(s).digest('hex');
// Every child gets an explicit environment, including git and the compiler.
const env = { PATH: process.env.PATH, HOME: process.env.HOME, GOPROXY: 'off', GOTOOLCHAIN: 'local',
  CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_HANDOVER_LOG: '0', CODE_FORGE_NO_ERROR_LOG: '1' };
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'package/bin'));
  await writeFile(path.join(scratch, 'package/bin/code-forge.mjs'), `if (process.env.CF_TEST_FALLBACK_PROBE === '1') {
    process.stdout.write(JSON.stringify(process.argv.slice(2)) + '\\n'); process.exitCode = 23;
  } else {
    const { run } = await import(${JSON.stringify(new URL('../../bin/code-forge.mjs', import.meta.url).href)});
    process.exitCode = await run(process.argv.slice(2), { configureTmpRoot: true });
  }\n`);
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  linkGoBinary(BINARY, { ldflags: `-X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}` });
});
async function fixture(kind = 'clean', config = {}) {
  const dir = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(dir, 'repo'), home: path.join(dir, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'old\n');
  if (kind === 'critical-test') await writeFile(path.join(f.repo, 'sample.test.mjs'), 'test\n');
  for (const args of [['init', '-qb', 'main'], ['add', '.'], ['-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', 'commit', '-qm', 'base']]) {
    const result = spawnSync('git', args, { cwd: f.repo, env, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
  }
  const base = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: f.repo, env, encoding: 'utf8' }).stdout.trim();
  await writeFile(path.join(f.repo, 'sample.mjs'), 'new\n');
  /** @type {any} */
  const record = { run_id: RUN, project: 'ready-fixture', workspace: f.repo, status: 'active',
    worker: { pid: process.pid, started_at: await processStartTime(process.pid) }, config: initialConfigState(config),
    blocks: { B1: { status: 'open', kind: 'code', owned_files: ['sample.mjs'], test_files: [], acceptance: [{ clause: 'works', tests: [] }], base_sha: base, tree_at_open: {} } }, orphans: [] };
  if (kind === 'closed') record.blocks.B1.status = 'closed';
  if (kind === 'unknown') delete record.blocks.B1;
  if (kind === 'down' || kind === 'down-unreviewed') delete record.worker;
  if (kind === 'orphan') record.orphans = ['orphan.mjs'];
  if (kind === 'light') record.blocks.B1.kind = 'light';
  if (kind === 'worktree' || kind === 'missing-tree') {
    const tree = path.join(f.repo, '.code-forge/tree');
    record.blocks.B1.worktree = { path: tree, branch: 'fixture-tree', base_sha: base };
    if (kind === 'worktree') {
      const result = spawnSync('git', ['worktree', 'add', '-qb', 'fixture-tree', tree, base], { cwd: f.repo, env, encoding: 'utf8' });
      assert.equal(result.status, 0, result.stderr);
      await writeFile(path.join(tree, 'sample.mjs'), 'new\n');
    }
  }
  if (kind.startsWith('critical-')) record.autopilot = { status: 'active', until: '2099-01-01T00:00:00.000Z' };
  if (kind === 'critical-test') record.blocks.B1.acceptance[0].tests = ['sample.test.mjs'];
  if (kind === 'unowned') await writeFile(path.join(f.repo, 'outsider'), 'new');
  if (kind === 'unchanged') await writeFile(path.join(f.repo, 'sample.mjs'), 'old\n');
  const rows = [signRow({ run: RUN, event: 'dispatch', block: 'B1', level: 'L2' }, KEY)];
  if (!['unreviewed', 'down-unreviewed', 'none', 'unchanged'].includes(kind)) rows.push(signRow({ run: RUN, event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: hash('new\n') }, KEY));
  if (['warning', 'critical', 'critical-grant', 'critical-test', 'judge', 'missing-file'].includes(kind)) rows.push(signRow({ run: RUN, event: 'review.result', block: 'B1', file: 'sample.mjs', content_hash: hash('new\n'), status: 'reviewed', approved: false, open: [{ id: 'F1', severity: kind.startsWith('critical') ? 'critical' : 'warning' }] }, KEY));
  if (kind === 'missing-file') await rm(path.join(f.repo, 'sample.mjs'));
  const runs = path.join(f.home, '.code-forge/runs');
  await mkdir(runs, { recursive: true });
  await writeFile(path.join(runs, RUN + '.json'), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  await writeFile(path.join(runs, RUN + '.key'), KEY, { mode: 0o600 });
  if (['stopped', 'same', 'open', 'complete', 'tampered', 'lost', 'judge-reopen', 'already', 'limit'].includes(kind)) {
    const saved = process.env.HOME;
    process.env.HOME = f.home;
    try {
      const state = newFileState({ file: 'sample.mjs', level: 'L2' });
      state.status = kind === 'open' ? 'open' : kind === 'complete' ? 'complete' : 'stopped';
      state.stop_content_hash = hash(kind === 'same' ? 'new\n' : 'old\n');
      state.next = { action: 'stop', reason: 'review_cap' };
      state.open = [{ id: 'F1', severity: 'warning', file: 'sample.mjs', claim: 'finding', line_start: 1, line_end: 1 }];
      await saveState({ runId: RUN, block: 'B1', file: 'sample.mjs', key: KEY, state, seq: 0, runRootDir: f.home, rows: [], writeRow: async (row) => { rows.push(signRow({ run: RUN, ...row }, KEY)); return rows.at(-1); } });
      if (kind === 'tampered') await writeFile(path.join(runs, RUN, 'review-state/B1', hash('sample.mjs').slice(0, 24) + '.json'), '{}');
      if (kind === 'lost') {
        await rm(path.join(runs, RUN, 'review-state/B1', hash('sample.mjs').slice(0, 24) + '.json'));
        rows[rows.length - 1] = signRow({ ...rows.at(-1), state_body: undefined, state_omitted: 'too_large' }, KEY);
      }
      if (kind === 'already' || kind === 'limit') rows.push(signRow({ run: RUN, event: 'review.reopened', block: 'B1', file: 'sample.mjs', from: 'stopped', at_seq: kind === 'already' ? 1 : 0, to_hash: hash('other'), by: kind === 'already' ? 'human' : 'agent', verdict: { verdict: 'agree' } }, KEY));
    } finally { process.env.HOME = saved; }
  }
  const ledger = path.join(f.home, '.code-forge/ledger');
  await mkdir(ledger); await writeFile(path.join(ledger, 'ready-fixture.jsonl'), rows.map((r) => JSON.stringify(r)).join('\n') + '\n');
  return f;
}
const argv = (verb, tail = []) => ['block', verb, 'B1', ...(verb === 'waive' ? ['F1'] : []), '--run', RUN, ...tail];
function launch(f, args, extra = {}) {
  const r = spawnSync(BINARY, args, { cwd: f.repo, env: { ...env, HOME: f.home, ...extra }, encoding: 'utf8' });
  return { code: r.status, stdout: r.stdout, stderr: r.stderr };
}
function launchAsync(f, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(BINARY, args, { cwd: f.repo, env: { ...env, HOME: f.home }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', (b) => { stdout += b; }); child.stderr.on('data', (b) => { stderr += b; });
    child.on('error', reject); child.on('close', (code) => resolve({ code, stdout, stderr }));
  });
}
async function parity(f, args, extra = {}, page) {
  const result = await runParity(args, { fixture: f, goCommand: BINARY, env: { ...env, ...extra } });
  // B613: a page means Go prints the help page around Node's usage; nothing else may differ.
  if (page) assert.deepEqual(result, helpPageParity(result, page), `${args.join(' ')}: only stdout differs, Go prints the help page around Node usage`);
  else assert.deepEqual(result.diffs, [], `${args.join(' ')}: zero exit, stream, signed ledger, run, queue and lock diffs`);
}
/** B613: the one-line summaries of the Go help pages. */
const helpSummaries = { ready: 'List what still blocks the close of a block', waive: 'Accept one review finding without a fix (owner)',
  reopen: 'Reopen a stopped file review for a new round (owner)' };
async function tree(dir) {
  const out = [];
  for (const e of (await readdir(dir, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
    const p = path.join(dir, e.name);
    out.push([e.name, e.isDirectory() ? await tree(p) : await readFile(p)]);
  }
  return out;
}

test('deterministic ready, waiver and reopen cases have native speed and full Node parity', async () => {
  const speed = launch(await fixture(), argv('ready'));
  assert.deepEqual(speed, { code: 0, stdout: 'block B1 ready\n', stderr: '' }, 'ready runs with Node unavailable');
  for (const kind of ['clean', 'unknown', 'closed', 'down', 'down-unreviewed', 'orphan', 'unowned', 'unchanged', 'unreviewed', 'warning', 'worktree', 'missing-tree']) {
    const f = await fixture(kind);
    await Promise.all([false, true].map((json) => parity(f, argv('ready', json ? ['--json'] : []))));
  }
  for (const config of [{ context: { require_coder_tests: true } }, { gates: { test: ['node', '--test'] } }]) await parity(await fixture('clean', config), argv('ready', ['--json']));
  for (const json of [false, true]) {
    await parity(await fixture('light', { light: { max_lines: 1 } }), argv('ready', json ? ['--json'] : []));
    await parity(await fixture('unreviewed', { review: { external: { required: true, name: 'owner' } } }), argv('ready', json ? ['--json'] : []));
    await parity(await fixture('unreviewed', { review: { external: { required: true, name: '\ufeff' } } }), argv('ready', json ? ['--json'] : []));
  }
  for (const verb of ['waive', 'reopen']) {
    await parity(await fixture(), argv(verb, ['--file', 'sample.mjs', '--reason', 'explanation']));
    for (const kind of ['unknown', 'closed', 'none', 'warning', 'critical', 'critical-grant', 'critical-test', 'stopped', 'same', 'open', 'complete', 'tampered', 'lost', 'already', 'limit', 'missing-file']) {
      const f = await fixture(kind);
      const reason = kind === 'critical-test' ? 'sample.test.mjs' : kind === 'limit' ? 'sample.mjs:1' : kind === 'missing-file' ? 'clause 1' : 'explanation';
      await Promise.all([false, true].map((json) => parity(f, argv(verb, ['--file', 'sample.mjs', '--reason', reason, '--yes', ...(json ? ['--json'] : [])]))));
    }
  }
  const concurrent = await fixture();
  const results = await Promise.all([
    launchAsync(concurrent, ['block', 'claim', 'B1', 'extra.mjs', '--run', RUN]),
    ...Array.from({ length: 3 }, () => launchAsync(concurrent, argv('ready', ['--json']))),
  ]);
  assert.deepEqual(results.map((r) => r.code), [0, 0, 0, 0], 'one locked writer and three previews all succeed without Node');
  const record = JSON.parse(await readFile(path.join(concurrent.home, '.code-forge/runs', RUN + '.json'), 'utf8'));
  assert.deepEqual(record.blocks.B1.owned_files, ['sample.mjs', 'extra.mjs'], 'preview never loses the concurrent locked ownership update');
  const rows = (await readFile(path.join(concurrent.home, '.code-forge/ledger/ready-fixture.jsonl'), 'utf8')).trim().split('\n').map((r) => JSON.parse(r));
  assert.deepEqual(rows.map((r) => r.event), ['dispatch', 'review.approved', 'block.claim'], 'only the writer adds exactly one ledger row');
  assert.deepEqual(rows.map((r) => verifyRow(r, KEY).ok), [true, true, true], 'all three ledger MACs verify after concurrent previews');
});

test('usage errors are exact and handovers leave every fixture byte unchanged', async () => {
  const f = await fixture();
  const speed = launch(f, ['block', 'waive']);
  assert.deepEqual(speed, { code: 2, stdout: '', stderr: 'block waive: a block id and --run are required\n' }, 'usage refusal runs with Node unavailable');
  for (const [kind, verb] of [['clean', 'ready'], ['unreviewed', 'ready'], ['clean', 'waive'], ['clean', 'reopen']]) {
    const logged = await fixture(kind), args = argv(verb, verb === 'ready' ? [] : ['--file', 'sample.mjs', '--reason', 'explanation']);
    await parity(logged, args, { CODE_FORGE_NO_ERROR_LOG: '0', CODE_FORGE_NODE: process.execPath });
    assert.equal(launch(logged, args, { CODE_FORGE_NO_ERROR_LOG: '0' }).code, verb === 'ready' ? kind === 'clean' ? 0 : 1 : 2, `${verb}/${kind}: default logging stays native with Node unavailable`);
  }
  for (const verb of ['ready', 'waive', 'reopen']) {
    for (const args of [[], ['B1'], ['--bogus'], ['--weird=<&>café'], ['--run'], ['B1', '--run', ''], ['B1', '--run', RUN, '--run', RUN], ['B1', '--run', RUN, '--json=1'], ['B1', '--run', RUN, '--remove-worktree']]) await parity(f, ['block', verb, ...args]);
    for (const flag of ['--help', '-h']) await parity(f, ['block', verb, flag], {}, { summary: helpSummaries[verb] });
    for (const tail of [['--worker-pid', '0'], ['--file', 'sample.mjs'], ['--reason', 'why'], ['--file', './sample.mjs', '--reason', 'why', '--yes'], ['--file', 'sample.mjs', '--reason', ' ', '--yes'], ['--file', 'other', '--reason', 'why', '--yes'], ['--file', 'bad\npath', '--reason', 'why', '--yes'], ['--file', '/absolute', '--reason', 'why', '--yes']]) await parity(f, argv(verb, tail));
    if (verb === 'waive') for (const finding of ['bad\nfinding', 'bad/finding']) {
      await parity(f, ['block', verb, 'B1', finding, '--run', RUN, '--file', 'sample.mjs', '--reason', 'why', '--yes']);
    }
    if (verb === 'reopen') for (const reason of ['\ufeff', '\u0085']) await parity(f, argv(verb, ['--file', 'sample.mjs', '--reason', reason, '--yes']));
    if (verb !== 'ready') for (const [file, reason] of [['', 'why'], ['sample.mjs', '']]) await parity(f, argv(verb, ['--file', file, '--reason', reason, '--yes']));
  }
  // B612 moved the --yes judge to Go: no Node handover. With no judge CLI the
  // session fails, Go refuses with exit 1, writes nothing and logs no handover.
  for (const [kind, verb, reason] of [['judge', 'waive', 'waive-yes-broken'], ['judge-reopen', 'reopen', 'reopen-yes-disagreed']]) {
    const f = await fixture(kind);
    // .git/index is git's own stat cache; a read-only `git status` may refresh it.
    const snap = async () => [await tree(f.home), (await tree(f.repo)).map(([n, v]) => n === '.git' ? [n, v.filter(([m]) => m !== 'index')] : [n, v])];
    const before = await snap();
    const args = argv(verb, ['--file', 'sample.mjs', '--reason', 'sample.mjs:1', '--yes']);
    assert.deepEqual(launch(f, args), { code: 1, stdout: '', stderr: `block ${verb}: ${reason}: Judge answer broken: the judge session failed.; nothing was recorded\n` }, `${verb}: judge runs in Go with Node unavailable`);
    assert.deepEqual(await snap(), before, `${verb}: a failed judge writes nothing`);
    assert.equal(launch(f, args, { CODE_FORGE_NATIVE: '1', CODE_FORGE_HANDOVER_LOG: '1' }).code, 1, `${verb}: telemetry does not change the Go exit`);
    const log = launch(f, ['logs', 'handovers', '--json']);
    assert.equal(log.code, 0, 'handover log report runs without Node');
    assert.deepEqual(JSON.parse(log.stdout).rows, [], `${verb}: no handover row`);
  }
});
