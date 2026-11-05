import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { signRow } from '../../src/state/signer.mjs';
import { announceWorker } from '../../src/worker/queue.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const pkg = await mkdtemp(path.join(os.tmpdir(), 'cf-status-direct-pkg-'));
const binary = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const unavailable = '/nonexistent-status-node';
before(async () => {
  await mkdir(path.dirname(binary), { recursive: true });
  await mkdir(path.join(pkg, 'bin'));
  await symlink(path.join(root, 'bin/code-forge.mjs'), path.join(pkg, 'bin/code-forge.mjs'));
  const { version } = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'));
  const build = spawnSync('go', ['build', '-trimpath', '-ldflags', `-X main.version=${version}`, '-o', binary, './cmd/code-forge-native'], {
    cwd: path.join(root, 'native'), encoding: 'utf8', env: { ...process.env, GOPROXY: 'off', GOTOOLCHAIN: 'local', CGO_ENABLED: '0' },
  });
  assert.equal(build.status, 0, build.stderr);
});
after(() => rm(pkg, { recursive: true, force: true }));

async function fixture(t) {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'cf-status-direct-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const f = { repo: path.join(dir, 'repo'), home: path.join(dir, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  assert.equal(spawnSync('git', ['init', '-q', '-b', 'main'], { cwd: f.repo }).status, 0);
  await mkdir(path.join(f.home, '.code-forge/runs'), { recursive: true });
  await mkdir(path.join(f.home, '.code-forge/ledger'), { recursive: true });
  await writeFile(path.join(f.repo, '.code-forge.yml'), 'version: 1\nproject: {slug: sample}\nbudget: {usd: 8}\n');
  return f;
}
/** @returns {Record<string, any>} */
function record(f, id = 'r-direct', extra = {}) {
  return { run_id: id, workspace: f.repo, project: 'sample', status: 'active', started_at: '2026-01-01T00:00:00Z', blocks: {}, ...extra };
}
async function save(f, r) {
  await writeFile(path.join(f.home, '.code-forge/runs', `${r.run_id}.json`), JSON.stringify(r));
}
async function rows(f, values) {
  await writeFile(path.join(f.home, '.code-forge/ledger/sample.jsonl'), values.map((r) => JSON.stringify(r)).join('\n') + '\n');
}
function native(f, args) {
  const r = spawnSync(binary, ['status', ...args], { cwd: f.repo, encoding: 'utf8', timeout: 3000,
    env: { ...process.env, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: unavailable, CODE_FORGE_NO_ERROR_LOG: '1' } });
  assert.equal(r.error, undefined, 'status completes within three seconds even under a held run lock');
  return { code: r.status, stdout: r.stdout, stderr: r.stderr };
}
async function parity(f, args) {
  assert.deepEqual(await runParity(['status', ...args], { fixture: f, goCommand: binary,
    env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: unavailable } }), { equal: true, diffs: [] }, `${JSON.stringify(args)}: zero stream, exit and state differences`);
}
const forms = (id) => [[], ['--json'], ['--run', id], ['--run', id, '--json'], ['--json', '--run', id], [`--run=${id}`], [`--run=${id}`, '--json'], ['--json', `--run=${id}`]];

test('B273 clause: every status form reads disk without Node, with exact streams and zero state differences', async (t) => {
  const f = await fixture(t);
  for (const args of [[], ['--json']]) await parity(f, args);
  const blocks = Object.fromEntries(Array.from({ length: 14 }, (_, i) => [`B${i + 1}`, { status: 'open', level: 'L2', attempt: i === 0 ? 2 : 1 }]));
  Object.assign(blocks, { B20: { status: 'closed' }, B21: { status: 'stopped' }, B22: { status: 'awaiting_human_check' } });
  const r = record(f, 'r-direct', { blocks });
  await save(f, r);
  const writer = spawn(binary, ['internal', 'state-golden', '--home', f.home, '--run', r.run_id, '--op', 'lock'], {
    cwd: f.repo, env: { ...process.env, HOME: f.home }, stdio: ['pipe', 'pipe', 'pipe'],
  });
  t.after(async () => {
    if (writer.exitCode === null && writer.signalCode === null) {
      const closed = once(writer, 'close'); writer.kill('SIGTERM'); await closed;
    }
  });
  assert.equal(String((await once(writer.stdout, 'data'))[0]), 'locked\n', 'a concurrent writer holds the real run lock');
  const key = Buffer.alloc(32, 7);
  await writeFile(path.join(f.home, '.code-forge/runs/r-direct.key'), key, { mode: 0o600 });
  await rows(f, [
    { event: 'session', run: r.run_id, block: 'B1', role: 'coder', provider: 'sample', model: '<é>&😀', usd: 1.235 },
    { event: 'session.background', run: r.run_id, block: 'B2', role: 'coder', model: 'unknown', usd: null },
    { event: 'gate.red', run: r.run_id, block: 'B1', reason: 'timeout', gate: 'tests' },
    { event: 'gate.done', run: r.run_id, block: 'B1', all_ok: false, flaky: ['z.mjs', 'a.mjs', 'z.mjs'] },
    { event: 'gate.red', run: r.run_id, block: 'B2', gate: 'lint' },
    { event: 'review.result', run: r.run_id, block: 'B1', file: 'a.mjs', content_hash: 'abc', approved: false,
      open: [{ id: 'F1', severity: 'critical' }, { id: 'F2', severity: 'warning' }] },
    signRow({ event: 'review.waived', run: r.run_id, block: 'B1', file: 'a.mjs', finding: 'F2', by: 'human' }, key),
    { event: 'review.result', run: r.run_id, block: 'B2', file: 'cap.mjs', content_hash: 'hash', approved: false, open: [{ id: 'C1', severity: 'warning' }] },
    { event: 'review.cap', run: r.run_id, block: 'B2', file: 'cap.mjs', content_hash: 'hash', open: ['C1', 'C1'] },
    { event: 'review.late_finding', run: r.run_id, block: 'B2', file: 'late.mjs', finding: 'L1', severity: 'warning' },
    signRow({ event: 'review.late_ruling', run: r.run_id, block: 'B2', file: 'late.mjs', finding: 'L1', ruling: 'nit' }, key),
    { event: 'review.late_finding', run: r.run_id, block: 'B2', file: 'late.mjs', finding: 'L2', severity: 'critical' },
  ]);
  for (const args of forms(r.run_id)) await parity(f, args);
  const summary = JSON.parse(native(f, ['--json']).stdout).runs[0];
  assert.equal(summary.blocks.length, 15, 'JSON includes all fifteen unfinished blocks');
  assert.equal(summary.blocks[0].findings, 1, 'exactly one unwaived finding remains');
  assert.equal(summary.blocks[1].findings, 2, 'exactly one distinct capped finding and one unruled late finding remain');
  assert.equal(summary.blocks[0].gate.result, 'timeout', 'failed timed-out gate retains its timeout reason');
  assert.equal(summary.blocks[1].gate.result, 'fail', 'a later non-timeout gate fails normally');
  assert.equal(summary.spent_usd, 1.235, 'exactly one known-price session contributes spend');
  assert.equal(summary.unknown_usd_sessions, 1, 'exactly one unknown-price session is counted');
  assert.equal(summary.budget_usd, 8, 'workspace budget is used');
  assert.equal(native(f, []).stdout.split('\n').length, 11, 'text contains exactly ten lines and a final newline');
  assert.match(native(f, []).stdout, /spent \$1\.24\+\? of \$8\.00\n/, 'text rounds spend and includes unknown-price suffix and budget');
  await save(f, record(f, 'r-newer', { started_at: '2026-02-01T00:00:00Z', mode: 'brief' }));
  await save(f, record(f, 'r-ended', { status: 'ended' }));
  await save(f, record(f, 'r-elsewhere', { workspace: path.dirname(f.repo) }));
  announceWorker(f.repo, { pid: process.pid, run: r.run_id });
  for (const args of forms(r.run_id)) await parity(f, args);
  assert.deepEqual(JSON.parse(native(f, ['--json']).stdout).runs.map((s) => s.run_id), ['r-newer', 'r-direct'], 'exactly two matching active runs, newest first');
  assert.match(native(f, ['--run', r.run_id]).stderr, /warning: live worker \d+ is not pinned:/, 'one live unpinned worker warns on stderr');
  await writeFile(path.join(f.repo, '.code-forge.yml'), 'version: 1\n');
  await rm(path.join(f.home, '.code-forge/ledger/sample.jsonl'));
  await mkdir(path.join(f.home, '.code-forge/ledger/sample.jsonl'));
  for (const args of forms(r.run_id)) await parity(f, args);
  assert.match(native(f, ['--run', r.run_id]).stdout, /spent \$\?\n/, 'unreadable ledger and unset budget print exactly $?');
  await rm(path.join(f.home, '.code-forge/ledger/sample.jsonl'), { recursive: true });
  await rows(f, [
    { event: 'session', run: r.run_id, provider: 'openai', level: 'L1', tokens_in: 1000, tokens_out: 500 },
    { event: 'session', run: r.run_id, cost_usd: 0.125 },
    { event: 'session', run: r.run_id, source: 'shadow', usd: 10 },
    { event: 'session', run: 'r-other', usd: 10 },
    { event: 'gate.done', run: r.run_id, block: 'B1', all_ok: true },
  ]);
  for (const budget of [0, -1, 0.125, 1.005, 1e21]) {
    await writeFile(path.join(f.repo, '.code-forge.yml'), `version: 1\nbudget: {usd: ${budget}}\n`);
    for (const args of [['--run', r.run_id], ['--json', '--run', r.run_id]]) await parity(f, args);
  }
  const spend = JSON.parse(native(f, ['--json', '--run', r.run_id]).stdout).runs[0];
  assert.equal(spend.spent_usd, 0.1288, 'legacy tokens and cost_usd total 0.1288; shadow and other-run rows add zero');
  assert.equal(spend.unknown_usd_sessions, 0, 'both legacy session prices are known');
  assert.equal(spend.blocks[0].gate.result, 'pass', 'successful gate clears earlier timeout');
  // Merge ancestry is queried only for text and reserves one of the ten lines.
  for (const args of [['add', '.code-forge.yml'], ['-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture'], ['branch', 'merged']]) {
    assert.equal(spawnSync('git', args, { cwd: f.repo }).status, 0, 'merge-hint git fixture succeeds');
  }
  r.blocks = { B1: { status: 'open', base_branch: 'main', worktree: { branch: 'merged' } } };
  await save(f, r);
  for (const args of forms(r.run_id)) await parity(f, args);
  assert.match(native(f, ['--run', r.run_id]).stdout, /all open blocks are merged: end the run with: code-forge run end --run r-direct\n$/, 'merged branch prints the exact end-run hint');
  const start = spawnSync('/bin/ps', ['-o', 'lstart=', '-p', String(writer.pid)], { encoding: 'utf8', env: { ...process.env, LC_ALL: 'C' } });
  assert.equal(start.status, 0, 'the concurrent writer has a process identity');
  r.workers = [{ pid: writer.pid, started_at: start.stdout.trim().replace(/\s+/g, ' ') }, { pid: 0, started_at: 'invalid' }];
  await save(f, r);
  for (const args of [['--run', r.run_id], ['--json', '--run', r.run_id]]) await parity(f, args);
  assert.match(native(f, ['--run', r.run_id]).stderr, /\(pinned pid \d+ is replaced\)\n$/, 'an invalid secondary pin refuses the identity check even when another pin is live');
  await save(f, { ...r, status: 'ended' });
  for (const args of [['--run', r.run_id], ['--json', '--run', r.run_id]]) await parity(f, args);
  assert.equal(native(f, ['--run', r.run_id]).stderr, '', 'ended runs emit zero worker warnings');
  assert.equal(writer.exitCode, null, 'writer still holds the run lock after every direct read');
  const writerClosed = once(writer, 'close'); writer.stdin.end();
  assert.deepEqual(await writerClosed, [0, null], 'concurrent writer releases its unchanged lock and exits zero');
});

test('B273 boundary: unknown runs and usage errors stay native; unsupported state hands over before output', async (t) => {
  const f = await fixture(t);
  for (const args of [['--run', 'r-missing'], ['--run=../bad'], ['--run'], ['--run='], ['--json=true'], ['--json', '--json'],
    ['--run', 'a', '--run', 'b'], ['--wat'], ['positional'], ['--run', '--json'], ['positional', '--wat'], ['--run', ''], ['--'], ['--json', '--help']]) await parity(f, args);
  assert.deepEqual(native(f, ['--run', 'r-missing']), { code: 1, stdout: '', stderr: 'status: no run record for r-missing\n' });
  assert.deepEqual(native(f, ['--wat']), { code: 2, stdout: '', stderr: 'status: unknown flag "--wat"\nusage: code-forge status [--run <id>] [--json]\n' });
  await save(f, record(f));
  await rows(f, [{ event: 'review.answered', run: 'r-direct', block: 'B1' }]);
  const handed = native(f, ['--run', 'r-direct']);
  assert.equal(handed.code, 127, 'unsupported review state attempts Node exactly once');
  assert.equal(handed.stdout, '', 'handover emits zero partial stdout bytes');
  const result = await runParity(['status', '--run', 'r-direct'], { fixture: f, goCommand: binary,
    env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: process.execPath } });
  assert.deepEqual(result, { equal: true, diffs: [] }, 'handed-over review state has zero process and state differences');
});
