import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmod, cp, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { newFileState } from '../../src/review/fixloop.mjs';
import { saveState } from '../../src/worker/review-state.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-close-'));
const BINARY = path.join(scratch, 'package/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const PROBE = path.join(scratch, 'argv-node');
const RUN = 'r-close-fixture', KEY = Buffer.alloc(32, 27), SLUG = 'close-fixture';
const hash = (text) => createHash('sha256').update(text).digest('hex');
const args = ['block', 'close', 'B1', '--run', RUN];
const liveConfig = { version: 1, provider: 'anthropic', engine: 'harness', levels: {
  L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' },
  L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' },
}, proof: { tiers: { high: { paths: ['sample.mjs'] } } } };
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(BINARY), { recursive: true });
  await mkdir(path.join(scratch, 'package/bin'));
  await symlink(CLI, path.join(scratch, 'package/bin/code-forge.mjs'));
  await writeFile(PROBE, `#!${process.execPath}\nprocess.stdout.write(JSON.stringify(process.argv.slice(3)) + '\\n');\n`);
  await chmod(PROBE, 0o755);
  const built = spawnSync('go', ['build', '-o', BINARY, './cmd/code-forge-native'], {
    cwd: path.join(ROOT, 'native'), encoding: 'utf8', env: { ...process.env, GOPROXY: 'off', GOTOOLCHAIN: 'local' },
  });
  assert.equal(built.status, 0, `private binary builds: ${built.stderr}`);
});

async function fixture(kind = 'clean', config = {}) {
  const root = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const value = 1;\n');
  await writeFile(path.join(f.repo, 'answer.mjs'), 'export const answer = 1;\n');
  if (['high', 'proven'].includes(kind)) await writeFile(path.join(f.repo, '.code-forge.yml'), JSON.stringify(liveConfig) + '\n');
  for (const gitArgs of [['init', '-qb', 'main'], ['add', '.'], ['-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'base']]) {
    const git = spawnSync('git', gitArgs, { cwd: f.repo, encoding: 'utf8' });
    assert.equal(git.status, 0, `fixture git: ${git.stderr}`);
  }
  const base = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: f.repo, encoding: 'utf8' }).stdout.trim();
  const record = { run_id: RUN, project: SLUG, workspace: f.repo, status: 'active',
    worker: { pid: process.pid, started_at: await processStartTime(process.pid) },
    config: initialConfigState(config), blocks: { B1: { status: 'open', owned_files: ['sample.mjs', 'answer.mjs'], base_sha: base, tree_at_open: {}, kind: 'code', test_files: [] } }, orphans: [] };
  const current = 'export const value = 2;\n', answering = 'export const answer = 2;\n';
  await writeFile(path.join(f.repo, 'sample.mjs'), current);
  /** @type {Record<string, any>[]} */
  const rows = [{ event: 'dispatch', block: 'B1', level: 'L2' }];
  if (['finding', 'waived', 'answered'].includes(kind)) {
    rows.push({ event: 'review.round', block: 'B1', file: 'sample.mjs', content_hash: hash(current), open_after: 1 });
    rows.push({ event: 'review.result', block: 'B1', file: 'sample.mjs', content_hash: hash(current), approved: false, status: 'reviewed', open: [{ id: 'F1', severity: 'critical' }] });
    if (kind === 'waived') rows.push({ event: 'review.waived', block: 'B1', file: 'sample.mjs', finding: 'F1', by: 'human', reason: 'accepted by owner' });
    if (kind === 'answered') {
      await writeFile(path.join(f.repo, 'answer.mjs'), answering);
      rows.push({ event: 'review.approved', block: 'B1', file: 'answer.mjs', content_hash: hash(answering) });
      rows.push({ event: 'review.answered', block: 'B1', file: 'sample.mjs', finding: 'F1', content_hash: hash(current), by_file: 'answer.mjs', by_hash: hash(answering) });
    }
  } else if (kind !== 'stopped') rows.push({ event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: hash(current) });
  if (kind === 'reverted') await writeFile(path.join(f.repo, 'sample.mjs'), 'export const value = 1;\n');
  if (kind === 'unowned') await writeFile(path.join(f.repo, 'outsider.mjs'), 'unowned\n');
  if (kind === 'down') delete record.worker;
  if (kind === 'replaced') record.worker.started_at = 'different start';
  if (kind === 'closed') record.blocks.B1.status = 'closed';
  if (kind === 'unknown') delete record.blocks.B1;
  if (kind === 'missing-tree') record.blocks.B1.worktree = { path: path.join(f.repo, 'gone'), branch: 'fixture-tree', base_sha: base };
  if (kind === 'grant') record.autopilot = { status: 'active', until: '2099-01-01T00:00:00.000Z' };
  if (kind === 'approval') record.autopilot_approvals = [{ status: 'active', until: '2000-01-01T00:00:00.000Z' }];
  if (kind === 'stopped') rows.push({ event: 'review.cap', block: 'B1', file: 'sample.mjs', content_hash: hash('old'), open: ['F1'] });
  if (kind === 'credit') rows.push({ event: 'review.result', block: 'B1', file: 'sample.mjs', content_hash: hash(current), approved: true, status: 'reviewed', ticket: 'a'.repeat(24), open: [] });
  if (kind === 'proven') rows.push({ event: 'proof', block: 'B1', step: 'red-green', proven: true, red_kind: 'assertion', covers: ['sample.mjs'] });
  const dir = path.join(f.home, '.code-forge/runs');
  await mkdir(dir, { recursive: true });
  await writeFile(path.join(dir, `${RUN}.json`), JSON.stringify(record, null, 2) + '\n', { mode: 0o600 });
  await writeFile(path.join(dir, `${RUN}.key`), KEY, { mode: 0o600 });
  const savedHome = process.env.HOME;
  try {
    process.env.HOME = f.home;
    for (const row of rows) await appendRow(signRow({ run: RUN, ...row }, KEY), { slug: SLUG });
    if (kind === 'stopped') {
      const state = newFileState({ file: 'sample.mjs', level: 'L2' });
      state.status = 'stopped'; state.reviewed_content = 'old';
      state.open = [{ id: 'F1', severity: 'critical', file: 'sample.mjs', claim: 'unresolved finding', line_start: 1, line_end: 1 }];
      await saveState({ runId: RUN, block: 'B1', file: 'sample.mjs', key: KEY, state, seq: 0, runRootDir: f.home, rows: [],
        writeRow: (row) => appendRow(signRow({ run: RUN, ...row }, KEY), { slug: SLUG }) });
    }
  } finally { process.env.HOME = savedHome; }
  if (kind === 'tamper') {
    const file = path.join(f.home, '.code-forge/ledger', `${SLUG}.jsonl`);
    await writeFile(file, (await readFile(file, 'utf8')).replace('"level":"L2"', '"level":"L3"'));
  }
  if (kind !== 'missing-transcript') {
    const logs = path.join(f.repo, '.code-forge/runs', RUN);
    await mkdir(logs, { recursive: true });
    await writeFile(path.join(logs, 'B1.log'), kind === 'transcript-hit' ? 'git clean -fd\ngit clean -fd\n' : 'Completed the requested implementation.\n');
  }
  return f;
}
function launch(f, argv = args, node = false, env = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(node ? process.execPath : BINARY, node ? [CLI, ...argv] : argv, { cwd: f.repo, env: {
      PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', ...env,
    }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', (b) => { stdout += b; }); child.stderr.on('data', (b) => { stderr += b; });
    child.on('error', reject); child.on('close', (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}
async function tree(root) {
  const out = [];
  async function walk(dir) {
    let entries;
    try { entries = await readdir(dir, { withFileTypes: true }); } catch (err) { if (err.code === 'ENOENT') return; throw err; }
    for (const e of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      const file = path.join(dir, e.name), rel = path.relative(root, file);
      if (e.isDirectory()) { out.push([rel, 'directory']); await walk(file); }
      else out.push([rel, await readFile(file)]);
    }
  }
  await walk(root); return out;
}

test('plain agent close has native speed and byte parity for the complete close gate and its writes', async () => {
  const speed = await launch(await fixture());
  assert.equal(speed.code, 0, `clean close succeeds with Node unavailable: ${speed.stderr}`);
  for (const kind of ['clean', 'finding', 'waived', 'answered', 'reverted', 'unowned', 'missing-transcript', 'down', 'replaced', 'tamper', 'closed', 'unknown', 'missing-tree', 'credit', 'transcript-hit', 'high', 'proven']) {
    const f = await fixture(kind);
    for (const json of [false, true]) {
      const result = await runParity([...args, ...(json ? ['--json'] : [])], { fixture: f, goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
      assert.deepEqual(result.diffs, [], `${kind}, json=${json}: zero exit, stdout, stderr, signed row, record and done-file diffs`);
    }
    const direct = await launch(f, [...args, '--json']);
    const expected = ['clean', 'waived', 'answered', 'reverted', 'missing-transcript', 'credit', 'proven'].includes(kind) ? 0 : 1;
    assert.equal(direct.code, expected, `${kind}: the native route has the expected gate outcome with no Node`);
    const rowFile = path.join(f.home, '.code-forge/ledger', `${SLUG}.jsonl`);
    const events = (await readFile(rowFile, 'utf8')).trim().split('\n').map((line) => JSON.parse(line).event);
    if (['down', 'replaced', 'tamper'].includes(kind)) assert.equal(events.at(-1), { down: 'worker.down', replaced: 'worker.replaced', tamper: 'ledger.tamper' }[kind], `${kind}: exactly the correct stop event ends the ledger`);
    if (kind === 'missing-transcript') assert.deepEqual(events.slice(-2), ['gate.transcript_missing', 'block.close'], 'missing transcript audits once before the close');
    if (kind === 'finding') assert.deepEqual(JSON.parse(direct.stdout).blockers.map((b) => [b.code, b.finding ?? null]), [['unreviewed', null], ['not_approved', 'F1']], 'the one open finding has exactly two distinct blockers');
  }
  const f = await fixture('waived');
  const closed = await launch(f, [...args, '--json']);
  assert.equal(closed.code, 0, closed.stderr);
  const result = JSON.parse(closed.stdout);
  assert.deepEqual(result.reviewed_by_waiver.map((r) => [r.file, r.findings, r.by]), [['sample.mjs', ['F1'], 'human']], 'exactly one waived-all file records its one finding');
  const rows = (await readFile(path.join(f.home, '.code-forge/ledger', `${SLUG}.jsonl`), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
  assert.deepEqual(rows.slice(-2).map((r) => r.event), ['gate.review_waived_all', 'block.close'], 'exactly two final rows in publication order');
  assert.deepEqual(rows.map((r) => verifyRow(r, KEY).ok), rows.map(() => true), 'every ledger row verifies');
  const done = await readFile(path.join(f.home, '.code-forge/runs', RUN, 'blocks/B1.done.json'), 'utf8');
  const commit = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: f.repo, encoding: 'utf8' }).stdout.trim();
  assert.equal(done.replace(/"at":"[^"]+"/, '"at":"<time>"'), JSON.stringify({ block: 'B1', run_id: RUN, status: 'closed', event: 'block.close', reason: null, commit, spent_usd: 0, unknown_usd_sessions: 0, findings: 0, at: '<time>', reviewed: result.reviewed }) + '\n', 'done file ordered bytes and final newline');
  const report = await fixture('credit', { context: { require_coder_tests: true } });
  const reportFile = path.join(report.home, 'report.txt');
  await writeFile(reportFile, 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n');
  const credit = await runParity([...args, '--report', reportFile, '--json'], { fixture: report, goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
  assert.deepEqual(credit.diffs, [], 'B270 signed ticket credit and coder.tests publication have zero diffs');

  const concurrent = await fixture(), clone = { repo: concurrent.repo, home: path.join(scratch, 'concurrent-home') };
  await cp(concurrent.home, clone.home, { recursive: true });
  const lock = path.join(clone.home, '.code-forge/runs', `${RUN}.lock`);
  await mkdir(lock);
  const pending = launch(clone);
  await delay(150);
  const fresh = JSON.parse(await readFile(path.join(clone.home, '.code-forge/runs', `${RUN}.json`), 'utf8'));
  fresh.orphans = ['late.mjs'];
  await writeFile(path.join(clone.home, '.code-forge/runs', `${RUN}.json`), JSON.stringify(fresh, null, 2) + '\n');
  await rm(lock, { recursive: true });
  const refused = await pending;
  assert.equal(refused.code, 1, 'under-lock registry recheck refuses a concurrent orphan');
  assert.match(refused.stdout, /unowned_change late\.mjs orphan in run registry: late\.mjs/, 'one concurrent orphan is named');
  assert.equal(JSON.parse(await readFile(path.join(clone.home, '.code-forge/runs', `${RUN}.json`), 'utf8')).blocks.B1.status, 'open', 'concurrent refusal preserves the open block');
});

test('unsupported close cases hand over the original arguments before any state write; native usage and refusal boundaries match Node', async () => {
  const usage = await launch(await fixture(), ['block', 'close']);
  assert.equal(usage.code, 2, 'missing close arguments are a native usage refusal with Node unavailable');
  /** @type {Array<[string, Record<string, any>, string[]]>} */
  const delegated = [
    ['clean', {}, ['--decide', '--reason', 'owner decision']],
    ['clean', {}, ['--no-require-reviews']], ['clean', {}, ['--auto-claim']], ['clean', {}, ['--remove-worktree']],
    ['clean', { close: { require_human: true } }, []],
    ['clean', { review: { external: { required: true, name: 'owner' } } }, []],
    ['clean', { hooks: { on_block_end: { argv: ['/usr/bin/true'] } } }, []],
    ['grant', {}, []], ['approval', {}, []], ['stopped', {}, []],
  ];
  for (const [kind, config, extra] of delegated) {
    const f = await fixture(kind, config), before = await Promise.all([tree(f.home), tree(path.join(f.repo, '.code-forge'))]);
    const result = await launch(f, [...args, ...extra, '--json']);
    assert.equal(result.code, 127, `${kind} ${JSON.stringify(config)} ${extra}: fallback exits 127 when Node is unavailable`);
    assert.deepEqual(await Promise.all([tree(f.home), tree(path.join(f.repo, '.code-forge'))]), before, 'handover preserves every state byte and directory');
    const probe = await launch(f, [...args, ...extra, '--json'], false, { CODE_FORGE_NODE: PROBE });
    assert.deepEqual([probe.code, probe.stdout, probe.stderr], [0, JSON.stringify([...args, ...extra, '--json']) + '\n', ''], 'handover passes every original argument once and in order');
    assert.deepEqual(await Promise.all([tree(f.home), tree(path.join(f.repo, '.code-forge'))]), before, 'successful handover also preserves every state byte');
  }
  for (const argv of [['block', 'close'], ['block', 'close', 'B1'], [...args, '--json=true'], [...args, '--run', RUN], [...args, '--worker-pid', 'bad'], [...args, '--unknown']]) {
    const result = await runParity(argv, { fixture: await fixture(), goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: process.execPath } });
    assert.deepEqual(result.diffs, [], `${argv}: exact boundary exit and stream bytes`);
  }
});
