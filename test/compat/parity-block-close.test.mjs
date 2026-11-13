import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmod, cp, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { serveDaemon } from '../helpers/daemon-fixture.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { macFor, signRow, verifyRow } from '../../src/state/signer.mjs';
import { newFileState } from '../../src/review/fixloop.mjs';
import { saveState } from '../../src/worker/review-state.mjs';
import { agentWaiverDigest } from '../../src/review/agent-waiver.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
// B603: real path, so paths git records (macOS: /var -> /private/var) match the fixture's.
const scratch = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-close-')));
const BINARY = path.join(scratch, 'package/native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const PROBE = path.join(scratch, 'argv-node');
const LOCK_WAIT = 'TEST: waiting on run lock\n';
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
  // Instrument only the private binary: the signal is emitted after mkdir
  // encounters the held lock, and report replacement is after the safety check.
  // Keep these hooks out of the shipped binary and exercise the real close code.
  const overlay = {};
  for (const [file, anchor, inserted] of [
    ['state/run.go', '\t\ttime.Sleep(lockPoll)', `\t\tfmt.Fprint(os.Stderr, ${JSON.stringify(LOCK_WAIT)})\n`],
    ['cmd/code-forge-native/block_close.go', '\tchecks, closeRows := []closeCheck{}, []ledger.Object{}',
      '\tif replacement := os.Getenv("CODE_FORGE_TEST_REPORT_REPLACEMENT"); replacement != "" {\n\t\tif err := os.Rename(replacement, blockString(flags["report"])); err != nil { panic(err) }\n\t}\n'],
  ]) {
    const original = path.join(ROOT, 'native', file), source = await readFile(original, 'utf8');
    assert.equal(source.split(anchor).length, 2, `unique instrumentation anchor in ${file}`);
    const instrumented = path.join(scratch, path.basename(file));
    await writeFile(instrumented, source.replace(anchor, inserted + anchor));
    overlay[original] = instrumented;
  }
  const overlayFile = path.join(scratch, 'overlay.json');
  await writeFile(overlayFile, JSON.stringify({ Replace: overlay }));
  const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
  linkGoBinary(BINARY, { overlay: overlayFile, ldflags: `-X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}` });
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
  } else if (['agent-waiver', 'acked'].includes(kind)) {
    // B380: the agent judge agreed with waiving each warning; no human row.
    const ids = kind === 'acked' ? ['F2', 'F1'] : ['F1'];
    rows.push({ event: 'review.round', block: 'B1', file: 'sample.mjs', content_hash: hash(current), open_after: ids.length });
    rows.push({ event: 'review.result', block: 'B1', file: 'sample.mjs', content_hash: hash(current), approved: false, status: 'reviewed', open: ids.map((id) => ({ id, severity: 'warning' })) });
    for (const id of ids) rows.push({ severity: 'warning', verdict: 'agree', judge_reason: `judge agrees on ${id}`, event: 'review.waived', block: 'B1', file: 'sample.mjs', finding: id, reason: `style only ${id}`, by: 'agent' });
  } else if (kind === 'agent-reopen') {
    // B380: a capped review at old bytes, reopened by an agreeing agent judge, then approved.
    rows.push({ event: 'review.cap', block: 'B1', file: 'sample.mjs', content_hash: hash('old'), open: ['F1'] });
    rows.push({ event: 'review.reopened', block: 'B1', file: 'sample.mjs', from: 'stopped', stop_reason: 'cap', at_seq: 0, from_hash: hash('old'), to_hash: hash(current), reason: 'fixed the capped finding', by: 'agent', verdict: { verdict: 'agree', reason: 'the fix holds' } });
    rows.push({ event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: hash(current) });
  } else if (kind === 'amended') {
    rows.push({ event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: hash(current) });
    rows.push({ event: 'block.acceptance_amended', block: 'B1', clauses: 2, by: 'human' });
  } else if (kind === 'amended-approved') {
    rows.push({ event: 'block.acceptance_amended', block: 'B1', clauses: 2, by: 'human' });
    rows.push({ event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: hash(current) });
  } else if (kind !== 'stopped') rows.push({ event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: hash(current) });
  if (kind.startsWith('amended')) record.blocks.B1.acceptance_revision = '00000000-0000-4000-8000-000000000380';
  if (kind === 'accepted-hit') {
    for (const line of [1, 2]) rows.push({ event: 'rule_break.accepted', block: 'B1', rule: 'git-clean', line, line_sha256: hash('git clean -fd'), reason: 'owner cleaned a scratch dir', by: 'human' });
  }
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
    const signed = [];
    for (const row of rows) {
      const r = signRow({ run: RUN, ...row }, KEY);
      signed.push(r);
      await appendRow(r, { slug: SLUG });
    }
    if (kind === 'acked') {
      const digest = agentWaiverDigest(signed.filter((r) => r.event === 'review.waived' && r.by === 'agent'));
      await appendRow(signRow({ run: RUN, event: 'agent_waivers.acked', block: 'B1', digest, by: 'human' }, KEY), { slug: SLUG });
    }
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
    await writeFile(path.join(logs, 'B1.log'), ['transcript-hit', 'accepted-hit'].includes(kind) ? 'git clean -fd\ngit clean -fd\n' : 'Completed the requested implementation.\n');
  }
  return f;
}
function launch(f, argv = args, node = false, env = {}, onLockWait = () => {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(node ? process.execPath : BINARY, node ? [CLI, ...argv] : argv, { cwd: f.repo, env: {
      PATH: process.env.PATH, HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', ...env,
    }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', (b) => { stdout += b; }); child.stderr.on('data', (b) => {
      stderr += b;
      if (stderr.includes(LOCK_WAIT)) onLockWait();
    });
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
});

/**
 * B339: native close queues a review for a changed file without a current approval (here the
 * acceptance amendment voided it) and waits review.close_wait_s; Node refuses at once. With no
 * reviewer the wait times out: Go blocks with review_pending and leaves the queued ticket.
 * @param {Array<Record<string, any>>} diffs @param {boolean} json @param {string} label
 */
function amendedQueuesReview(diffs, json, label) {
  const queue = diffs.filter((d) => /^repo\/\.code-forge\/queue\/[0-9a-f]{24}\.json$/.test(d.where));
  assert.deepEqual(queue.map((d) => [d.field, d.node, d.go]), [['$file', undefined, 'present']], `${label}: one queued ticket`);
  const ticket = path.basename(queue[0].where, '.json');
  const queued = `close: queued review sample.mjs ticket ${ticket}\n`;
  const pending = `review wait timed out after 1s; reviewer may be unavailable (ticket ${ticket})`;
  const rest = diffs.filter((d) => d !== queue[0]);
  const by = Object.fromEntries(rest.map((d) => [`${d.where}:${d.field}`, d]));
  assert.deepEqual(Object.keys(by).sort(), ['process:stderr', 'process:stdout'], `${label}, json=${json}: only stdout, stderr and the ticket differ`);
  const unreviewed = { code: 'unreviewed', file: 'sample.mjs', detail: 'no review.approved row for sample.mjs at its current content (acceptance amended)', fix: `code-forge review-file --wait-block B1 --run ${RUN}` };
  const blocked = { code: 'review_pending', file: 'sample.mjs', detail: pending, fix: 'no automatic fix: review_pending' };
  const line = (b) => `${b.code} ${b.file} ${b.detail}\n  fix: ${b.fix}\n`;
  const doc = (b) => JSON.stringify({ block: 'B1', ok: false, reason: `${b.code} sample.mjs`, blockers: [b] }) + '\n';
  assert.equal(by['process:stdout'].node, json ? doc(unreviewed) : line(unreviewed), `${label}, json=${json}: Node refuses at once`);
  assert.equal(by['process:stdout'].go, json ? doc(blocked) : queued + line(blocked), `${label}, json=${json}: Go queued, waited and blocks`);
  assert.equal(by['process:stderr'].node, 'block B1 open: unreviewed sample.mjs\n');
  assert.equal(by['process:stderr'].go, (json ? queued : '') + 'block B1 open: review_pending sample.mjs\n');
}

test('B380: agent waivers, agent reopens, accepted hits, require_ack and amended acceptance close and preview natively with Node parity', async () => {
  const ack = { review: { agent_waivers: { require_ack: true } } };
  /** @type {Array<[string, Record<string, any>, number]>} */
  const cases = [
    ['agent-waiver', {}, 0], ['agent-reopen', {}, 0], ['accepted-hit', {}, 0],
    ['agent-waiver', ack, 1], ['acked', ack, 0], ['amended', { review: { close_wait_s: 1 } }, 1], ['amended-approved', {}, 0],
  ];
  for (const [kind, config, expected] of cases) {
    const label = `${kind} ${JSON.stringify(config)}`;
    const f = await fixture(kind, config);
    const ready = await runParity(['block', 'ready', 'B1', '--run', RUN, '--json'], { fixture: f, goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(ready.diffs, [], `${label}: block ready has zero diffs`);
    const readyDirect = await launch(f, ['block', 'ready', 'B1', '--run', RUN, '--json']);
    assert.equal(readyDirect.code, expected, `${label}: block ready verdict with no Node: ${readyDirect.stderr}`);
    for (const json of [false, true]) {
      const result = await runParity([...args, ...(json ? ['--json'] : [])], { fixture: f, goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
      if (kind === 'amended') amendedQueuesReview(result.diffs, json, label);
      else assert.deepEqual(result.diffs, [], `${label}, json=${json}: zero exit, stdout, stderr, signed row, record and done-file diffs`);
    }
    const text = await launch(await fixture(kind, config), args);
    assert.equal(text.code, expected, `${label}: native close outcome with no Node: ${text.stderr}`);
    const direct = await launch(f, [...args, '--json']);
    assert.equal(direct.code, expected, `${label}: native --json close outcome with no Node: ${direct.stderr}`);
    const out = JSON.parse(direct.stdout);
    if (kind === 'agent-waiver' && expected === 0) {
      assert.deepEqual(out.agent_waivers, [{ file: 'sample.mjs', finding: 'F1', severity: 'warning', reason: 'style only F1', verdict: 'agree' }]);
      assert.match(text.stdout, /^block B1: agent waiver sample\.mjs F1 reason: style only F1; verdict: agree$/m);
    }
    if (kind === 'agent-reopen') {
      assert.deepEqual(out.agent_reopens, [{ file: 'sample.mjs', reason: 'fixed the capped finding', verdict: { verdict: 'agree', reason: 'the fix holds' } }]);
      assert.match(text.stdout, /^agent reopen sample\.mjs: fixed the capped finding \(verdict: agree\)$/m);
    }
    if (kind === 'accepted-hit') {
      assert.deepEqual(out.accepted_hits, [1, 2].map((line) => ({ rule: 'git-clean', line, reason: 'owner cleaned a scratch dir' })));
      assert.match(text.stdout, /^accepted rule_break git-clean transcript line 2: owner cleaned a scratch dir \(by: human\)$/m);
    }
    if (kind === 'agent-waiver' && expected === 1) {
      assert.deepEqual(out.blockers.map((b) => [b.code, b.fix]), [['agent-waivers-not-acked', `code-forge block ack-waivers B1 --run ${RUN}`]]);
    }
    if (kind === 'acked') assert.deepEqual(out.agent_waivers.map((w) => w.finding), ['F2', 'F1'], 'audit keeps ledger order');
    if (kind === 'amended') {
      // The amendment voids the approval: ready (no wait) names it; close re-queues the review and times out.
      assert.match(JSON.parse(readyDirect.stdout).blockers[0].detail, /\(acceptance amended\)/);
      assert.deepEqual(out.blockers.map((b) => [b.code, b.file]), [['review_pending', 'sample.mjs']]);
      assert.match(out.blockers[0].detail, /^review wait timed out after 1s; reviewer may be unavailable \(ticket [0-9a-f]{24}\)$/);
    }
    if (expected === 0 && !['agent-waiver', 'acked'].includes(kind)) assert.equal('agent_waivers' in out, false, `${label}: no empty agent_waivers key`);
  }
});

test('report safety, coder.tests and review tickets use the same snapshot despite atomic replacement', async () => {
  for (const replacementCredit of [true, false]) {
    const f = await fixture('credit', { context: { require_coder_tests: true } });
    const report = path.join(f.home, 'report.txt'), replacement = report + '.next';
    await writeFile(report, `reviewed sample.mjs ticket ${'a'.repeat(24)}\nTESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n`);
    const changedTicket = replacementCredit ? `reviewed sample.mjs ticket ${'b'.repeat(24)}\n` : '';
    await writeFile(replacement, changedTicket + `TESTS echo ${KEY.toString('hex')} exit=0\n===BLOCK B1 COMPLETE===\n`);
    const result = await launch(f, [...args, '--report', report, '--json'], false, { CODE_FORGE_TEST_REPORT_REPLACEMENT: replacement });
    assert.equal(result.code, 0, result.stderr);
    assert.equal(result.stderr, '', 'ticket validation uses the original matching ticket, not the replacement');
    const ledger = await readFile(path.join(f.home, '.code-forge/ledger', `${SLUG}.jsonl`), 'utf8');
    const rows = ledger.trim().split('\n').map((line) => JSON.parse(line));
    assert.deepEqual(rows.filter((r) => r.event === 'coder.tests').map((r) => r.command), ['node --test']);
    assert.equal(ledger.includes(KEY.toString('hex')), false, 'replacement key never reaches a signed row');
    assert.equal(rows.every((r) => verifyRow(r, KEY).ok), true);
  }
});

test('report read error is retained when the file appears after the initial read', async () => {
  for (const requireTests of [true, false]) {
    const f = await fixture('credit', { context: { require_coder_tests: requireTests } });
    const report = path.join(f.home, 'missing.txt'), replacement = report + '.next';
    await writeFile(replacement, `sample.mjs ${'a'.repeat(24)}\nTESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n`);
    const result = await launch(f, [...args, '--report', report, '--json'], false, { CODE_FORGE_TEST_REPORT_REPLACEMENT: replacement });
    assert.equal(result.code, 1, `initial read error refuses with require_coder_tests=${requireTests}`);
    assert.equal(JSON.parse(result.stdout).reason, 'coder report FAILED: --report cannot be read');
    const ledger = await readFile(path.join(f.home, '.code-forge/ledger', `${SLUG}.jsonl`), 'utf8');
    assert.equal(ledger.includes('coder.tests'), false);
    assert.equal(ledger.includes('block.close'), false);
  }
});

test('concurrent orphan is observed under the lock after a confirmed lock wait', async () => {
  const concurrent = await fixture(), clone = { repo: concurrent.repo, home: path.join(scratch, 'concurrent-home') };
  await cp(concurrent.home, clone.home, { recursive: true });
  const lock = path.join(clone.home, '.code-forge/runs', `${RUN}.lock`);
  await mkdir(lock);
  let signal;
  const waiting = new Promise((resolve) => { signal = resolve; });
  const pending = launch(clone, args, false, {}, () => signal('waiting'));
  try {
    assert.equal(await Promise.race([waiting, pending.then(() => 'exited')]), 'waiting', 'child encountered the held run lock');
    const record = path.join(clone.home, '.code-forge/runs', `${RUN}.json`);
    const fresh = JSON.parse(await readFile(record, 'utf8'));
    fresh.orphans = ['late.mjs'];
    await writeFile(record + '.next', JSON.stringify(fresh, null, 2) + '\n', { mode: 0o600 });
    await rename(record + '.next', record);
  } finally { await rm(lock, { recursive: true }); }
  const refused = await pending;
  assert.equal(refused.code, 1, 'under-lock registry recheck refuses a concurrent orphan');
  assert.match(refused.stdout, /unowned_change late\.mjs orphan in run registry: late\.mjs/, 'one concurrent orphan is named');
  assert.equal(JSON.parse(await readFile(path.join(clone.home, '.code-forge/runs', `${RUN}.json`), 'utf8')).blocks.B1.status, 'open', 'concurrent refusal preserves the open block');
});

test('Node accepts missing and invalid gate timestamps; native dates match and uncertain dates hand over', async () => {
  for (const [ts, expected, native] of [
    [undefined, 0, true], [null, 0, true], ['', 0, true],
    ['2000-01-01T00:00:00.000Z', 1, true], ['2099-01-01T00:00:00.000Z', 0, true],
    ['not-a-date', 0, false], ['2000-01-01', 1, false], ['2099-01-01', 0, false],
    [0, 1, false], ['2000-01-01T00:00:00+24:00', 0, false],
  ]) {
    const f = await fixture('clean', { gates: { test: { argv: ['true'] } } });
    for (const argv of [['add', 'sample.mjs'], ['-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'owned change']]) {
      const git = spawnSync('git', argv, { cwd: f.repo, encoding: 'utf8' });
      assert.equal(git.status, 0, git.stderr);
    }
    const row = { run: RUN, event: 'gate.done', block: 'B1', all_ok: true, ...(ts === undefined ? {} : { ts }) };
    const ledger = path.join(f.home, '.code-forge/ledger', `${SLUG}.jsonl`);
    // Preserve a genuinely missing ts: signRow/appendRow would stamp one.
    await writeFile(ledger, await readFile(ledger, 'utf8') + JSON.stringify({ ...row, mac: macFor(row, KEY) }) + '\n');
    const env = { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: native ? '/nonexistent' : process.execPath };
    const result = await runParity([...args, '--json'], { fixture: f, goCommand: BINARY, env });
    assert.deepEqual(result.diffs, [], `timestamp ${String(ts)}: exact Node parity, native=${native}`);
    if (!native) {
      const before = await Promise.all([tree(f.home), tree(path.join(f.repo, '.code-forge'))]);
      const missingNode = await launch(f, [...args, '--json']);
      assert.equal(missingNode.code, 127, `timestamp ${String(ts)} hands over before writing`);
      assert.deepEqual(await Promise.all([tree(f.home), tree(path.join(f.repo, '.code-forge'))]), before);
    }
    const direct = await launch(f, [...args, '--json'], false, env);
    assert.equal(direct.code, expected, `timestamp ${String(ts)}: ${direct.stderr}`);
  }
});

test('unsupported close cases hand over the original arguments before any state write; native usage and refusal boundaries match Node', async () => {
  const usage = await launch(await fixture(), ['block', 'close']);
  assert.equal(usage.code, 2, 'missing close arguments are a native usage refusal with Node unavailable');
  /** @type {[string, number][]} */
  const nativeFlags = [['--no-require-reviews', 2], ['--auto-claim', 0], ['--remove-worktree', 2]];
  for (const [flag, expected] of nativeFlags) {
    const f = await fixture(), argv = [...args, flag, '--json'];
    const parity = await runParity(argv, { fixture: f, goCommand: BINARY,
      env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(parity.diffs, [], `${flag}: native exit, stdout, stderr and every state byte match Node`);
    const result = await launch(f, argv);
    assert.equal(result.code, expected, `${flag}: expected native behavior with Node unavailable: ${result.stderr}`);
    const record = JSON.parse(await readFile(path.join(f.home, '.code-forge/runs', `${RUN}.json`), 'utf8'));
    assert.equal(record.blocks.B1.status, expected === 0 ? 'closed' : 'open', `${flag}: exact resulting block state`);
  }
  /** @type {Array<[Record<string, any>, string[]]>} */
  const nativeHumanCases = [
    [{}, ['--decide', '--reason', 'owner decision']],
    [{ close: { require_human: true } }, []],
  ];
  for (const [config, extra] of nativeHumanCases) {
    const f = await fixture('clean', config), argv = [...args, ...extra, '--json'];
    const before = await Promise.all([tree(f.home), tree(path.join(f.repo, '.code-forge'))]);
    const node = await launch(f, argv, true);
    assert.equal(node.code, 2, `${JSON.stringify(config)} ${extra}: Node refuses without a terminal`);
    const result = await launch(f, argv);
    assert.deepEqual(result, node, 'native terminal refusal matches every Node output byte and exit 2 with Node unavailable');
    assert.deepEqual(await Promise.all([tree(f.home), tree(path.join(f.repo, '.code-forge'))]), before, 'Node and Go terminal refusals preserve every state byte and directory');
    const parity = await runParity(argv, { fixture: f, goCommand: BINARY,
      env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(parity.diffs, [], 'native human close exit, streams and all state match Node');
  }
  /** @type {Array<[string, Record<string, any>, string[]]>} */
  const delegated = [
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
    const result = await runParity(argv, { fixture: await fixture(), goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(result.diffs, [], `${argv}: exact boundary exit and stream bytes`);
  }
  const delegatedBoundary = await runParity([...args, '--no-require-reviews'], {
    fixture: await fixture(), goCommand: BINARY, env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent' },
  });
  assert.deepEqual(delegatedBoundary.diffs, [], 'human confirmation refusal runs natively with exact Node parity');
});

test('close uses live daemon facts and stays native when an existing socket gives no answer', async () => {
  for (const answering of [true, false]) {
    const f = await fixture();
    const prepare = async (copy) => {
      const daemon = await serveDaemon(copy, RUN, async () => answering
        ? { rows: [{ event: 'session', run: RUN, block: 'B1', usd: 3.25 }] } : null);
      return daemon.cleanup;
    };
    const parity = await runParity([...args, '--json'], { fixture: f, goCommand: BINARY, tempRoot: '/tmp', prepare,
      env: { CODE_FORGE_NATIVE: '1', CODE_FORGE_NODE: '/nonexistent' } });
    assert.deepEqual(parity.diffs, [], `answering=${answering}: daemon and ledger fallback match Node`);
    const home = await mkdtemp('/tmp/cf-cd-');
    try {
      await cp(f.home, home, { recursive: true });
      const direct = { ...f, home };
      const cleanup = await prepare(direct);
      try {
        const result = await launch(direct, [...args, '--json'], false, { CODE_FORGE_NATIVE: '1' });
        assert.equal(result.code, 0, result.stderr);
        const done = JSON.parse(await readFile(path.join(home, '.code-forge/runs', RUN, 'blocks/B1.done.json'), 'utf8'));
        assert.equal(done.spent_usd, answering ? 3.25 : 0, 'facts come from the selected snapshot');
      } finally { await cleanup(); }
    } finally { await rm(home, { recursive: true, force: true }); }
  }
});
