import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const BINARY = path.join(ROOT, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const scratch = await mkdtemp('/tmp/cf-grants-');
const RUN = 'r-grant-parity', KEY = Buffer.alloc(32, 29);
const env = { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', CODE_FORGE_HANDOVER_LOG: '0' };
function childEnv(extra = {}) {
  const clean = { ...process.env, ...env, ...extra };
  for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK']) delete clean[key];
  return clean;
}
after(() => rm(scratch, { recursive: true, force: true }));
const recordFile = (f) => path.join(f.home, '.code-forge/runs', `${RUN}.json`);
const ledgerFile = (f) => path.join(f.home, '.code-forge/ledger/grants.jsonl');
const hash = (s) => createHash('sha256').update(s).digest('hex');
async function fixture(state, { human = false, scope = true, acceptanceHuman = false, unknown = false } = {}) {
  const root = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const n = 1;\n');
  await writeFile(path.join(f.repo, 'acceptance.json'), JSON.stringify([{ clause: 'works', ...(acceptanceHuman ? { kind: 'human_check' } : { tests: ['sample'] }) }]));
  const git = (args) => {
    const result = spawnSync('git', args, { cwd: f.repo, encoding: 'utf8', env: childEnv() });
    assert.equal(result.status, 0, `fixture git: ${result.stderr}`);
    return result.stdout.trim();
  };
  git(['init', '-qb', 'main']); git(['add', '.']);
  git(['-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'base']);
  const base = git(['rev-parse', 'HEAD']);
  const content = 'export const n = 2;\n';
  await writeFile(path.join(f.repo, 'sample.mjs'), content);
  const record = { run_id: RUN, project: 'grants', workspace: f.repo, status: state === 'run-ended' ? 'ended' : 'active',
    worker: { pid: process.pid, started_at: await processStartTime(process.pid) },
    config: initialConfigState(human ? { close: { require_human: true } } : {}),
    blocks: { B1: { status: 'open', owned_files: ['sample.mjs'], owned_kinds: { 'sample.mjs': 'file' }, base_sha: base, tree_at_open: {}, kind: 'code', test_files: [] } }, orphans: [] };
  if (state !== 'none') record.autopilot = { grant_id: 'ap-fixture', status: state === 'stopped' ? 'stopped' : 'active',
    until: state === 'expired' ? '2000-01-01T00:00:00.000Z' : '2099-01-01T00:00:00.000Z',
    scopes: scope ? ['close:routine'] : [], deny: [], delegate: 'L2', caps: {}, stop_at: 0.9,
    started_at: '1999-01-01T00:00:00.000Z', stopped_at: null, expired_at: null, link: null,
    ...(unknown ? { status: 'future-status' } : {}) };
  await mkdir(path.dirname(recordFile(f)), { recursive: true });
  await writeFile(recordFile(f), JSON.stringify(record, null, 2) + '\n');
  await writeFile(path.join(path.dirname(recordFile(f)), `${RUN}.key`), KEY, { mode: 0o600 });
  await mkdir(path.dirname(ledgerFile(f)), { recursive: true });
  const rows = [{ event: 'dispatch', block: 'B1', level: 'L2' },
    { event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: hash(content) },
    { event: 'gate.done', block: 'B1', all_ok: true },
    { event: 'autopilot.level', block: 'B2', level: 'L1' }];
  await writeFile(ledgerFile(f), rows.map((r) => JSON.stringify(signRow({ run: RUN, ...r }, KEY))).join('\n') + '\n');
  await mkdir(path.join(f.repo, '.code-forge/runs', RUN), { recursive: true });
  await writeFile(path.join(f.repo, '.code-forge/runs', RUN, 'B1.log'), 'Completed implementation.\n');
  return f;
}
function args(verb, json) {
  return ['block', verb, verb === 'open' ? 'B2' : 'B1', ...(verb === 'claim' ? ['new.mjs'] : []), '--run', RUN,
    ...(verb === 'stop' ? ['--reason', 'finished <&> café'] : []),
    ...(verb === 'open' ? ['--owned', 'new.mjs', '--acceptance', 'acceptance.json'] : []), ...(json ? ['--json'] : [])];
}
async function parity(f, argv, extra = {}) {
  const bytes = [];
  const result = await runParity(argv, { fixture: f, goCommand: BINARY, env: { ...env, ...extra },
    prepare: async (copy) => async () => {
      const normalize = (text) => text.replaceAll(copy.repo, '<repo>').replaceAll(copy.home, '<home>')
        .replace(/\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z/g, (stamp) =>
          ['1999-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z', '2099-01-01T00:00:00.000Z'].includes(stamp) ? stamp : '<timestamp>');
      const files = [recordFile(copy), path.join(copy.repo, '.code-forge/runs', `${RUN}.json`),
        path.join(copy.home, '.code-forge/runs', RUN, 'blocks/B1.done.json'), ledgerFile(copy)];
      bytes.push(await Promise.all(files.map(async (file) => {
        let text;
        try { text = await readFile(file, 'utf8'); }
        catch (err) { if (err.code === 'ENOENT') return null; throw err; }
        if (file.endsWith('.jsonl')) {
          for (const line of text.trim().split('\n')) {
            const row = JSON.parse(line);
            assert.equal(verifyRow(row, KEY).ok, true, `${file}: signature verifies before byte normalization`);
            text = text.replace(row.mac, '<verified-mac>');
          }
        }
        return normalize(text);
      })));
    } });
  assert.deepEqual(result.diffs, [], `${argv.join(' ')}: zero stdout, stderr, exit, state or verified-row differences`);
  assert.deepEqual(bytes[1], bytes[0], `${argv.join(' ')}: exact record, mirror, done-file and signed ledger bytes after clock/path/signature normalization`);
}
function launch(f, argv, extra = {}) {
  return spawnSync(BINARY, argv, { cwd: f.repo, env: childEnv({ HOME: f.home, ...extra }), encoding: 'utf8' });
}

test('claim/stop/open/close grants have Node byte, exit, state and signed-row parity without Node', async () => {
  for (const state of ['active', 'expired', 'run-ended', 'none', 'stopped']) {
    for (const verb of ['claim', 'stop', 'open', 'close']) for (const json of [false, true]) {
      const f = await fixture(state);
      await parity(f, args(verb, json));
      const result = launch(f, args(verb, json), { CODE_FORGE_HANDOVER_LOG: '1', CODE_FORGE_NATIVE: '1' });
      assert.equal(result.status, state === 'run-ended' ? 1 : 0, `${state} ${verb}: native exit with Node unavailable: ${result.stderr}`);
      const rows = (await readFile(ledgerFile(f), 'utf8')).trim().split('\n').map((s) => JSON.parse(s));
      assert.equal(rows.filter((r) => r.event === 'autopilot.expire').length, state === 'expired' ? 1 : 0, `${state} ${verb}: exact lazy expiry row count`);
      assert.deepEqual(rows.map((r) => verifyRow(r, KEY).ok), rows.map(() => true), `${state} ${verb}: every row verifies`);
      const log = launch(f, ['logs', 'handovers', '--json']);
      assert.equal(log.status, 0, log.stderr);
      assert.deepEqual(JSON.parse(log.stdout), { total: 0, rows: [] }, `${state} ${verb}: zero Node handovers`);
      if (state === 'expired') {
        const saved = JSON.parse(await readFile(recordFile(f), 'utf8'));
        assert.equal(saved.autopilot.status, 'expired', `${verb}: expiry persisted`);
        await parity(f, args(verb, json));
      }
    }
  }
  for (const state of ['active', 'expired', 'run-ended', 'none']) {
    for (const json of [false, true]) await parity(await fixture(state, { human: true }), args('close', json), state === 'none' ? { CODE_FORGE_NODE: process.execPath } : {});
  }
});

test('grant boundaries preserve refusals and unsupported shapes hand over without mutation', async () => {
  for (const options of [{ human: true, scope: false }, { acceptanceHuman: true }]) {
    const verb = options.human ? 'close' : 'open';
    const f = await fixture('active', options);
    await parity(f, args(verb, true));
    assert.equal(launch(f, args(verb, true)).status, 1, `${verb}: active grant boundary refuses in Go`);
  }
  for (const verb of ['claim', 'close']) {
    const argv = args(verb, true);
    argv[verb === 'claim' ? 3 : 2] = verb === 'claim' ? '../outside' : 'Unknown';
    await parity(await fixture('expired'), argv);
  }
  const routine = await fixture('active', { human: true });
  const rows = (await readFile(ledgerFile(routine), 'utf8')).trim().split('\n').map((s) => JSON.parse(s));
  await writeFile(ledgerFile(routine), rows.filter((r) => r.event !== 'gate.done').map((r) => JSON.stringify(r)).join('\n') + '\n');
  await parity(routine, args('close', true));
  assert.equal(launch(routine, args('close', true)).status, 1, 'routine close requires one signed green gate row');
  // B196 clearing remains with Node until the native review gate ports agent
  // waiver evidence. An active grant must not make Go guess at a critical.
  const critical = await fixture('active');
  const initial = (await readFile(ledgerFile(critical), 'utf8')).trim().split('\n').map((s) => JSON.parse(s));
  const signed = [
    { event: 'review.round', block: 'B1', file: 'sample.mjs', content_hash: hash('export const n = 2;\n'), open_after: 1 },
    { event: 'review.result', block: 'B1', file: 'sample.mjs', content_hash: hash('export const n = 2;\n'), approved: false, status: 'reviewed', open: [{ id: 'F1', severity: 'critical' }] },
    { event: 'review.waived', block: 'B1', file: 'sample.mjs', finding: 'F1', severity: 'critical', by: 'agent', verdict: 'agree', autopilot: true, test: 'sample', reason: 'signed evidence' },
  ].map((r) => signRow({ run: RUN, ...r }, KEY));
  await writeFile(ledgerFile(critical), [...initial.filter((r) => r.event !== 'review.approved'), ...signed].map((r) => JSON.stringify(r)).join('\n') + '\n');
  const beforeCritical = await Promise.all([readFile(recordFile(critical)), readFile(ledgerFile(critical))]);
  assert.equal(launch(critical, args('close', true)).status, 127, 'unported critical waiver dialect hands over');
  assert.deepEqual(await Promise.all([readFile(recordFile(critical)), readFile(ledgerFile(critical))]), beforeCritical, 'critical waiver handover changes zero state or ledger bytes');
  await parity(critical, args('close', true), { CODE_FORGE_NODE: process.execPath });
  for (const verb of ['claim', 'stop', 'open', 'close']) {
    const f = await fixture('active', { unknown: true });
    const before = await Promise.all([readFile(recordFile(f)), readFile(ledgerFile(f))]);
    assert.equal(launch(f, args(verb, true), { CODE_FORGE_HANDOVER_LOG: '1', CODE_FORGE_NATIVE: '1' }).status, 127, `${verb}: unknown shape delegates to unavailable Node`);
    assert.deepEqual(await Promise.all([readFile(recordFile(f)), readFile(ledgerFile(f))]), before, `${verb}: no record or ledger mutation before delegation`);
    const log = JSON.parse(launch(f, ['logs', 'handovers', '--json']).stdout);
    assert.equal(log.total, 1, `${verb}: exactly one handover`);
    assert.deepEqual(log.rows.map(({ sub, reason, count }) => [sub, reason, count]), [[verb, `${verb}-autopilot-grant`, 1]], `${verb}: stable B282 reason`);
    await parity(f, args(verb, true), { CODE_FORGE_NODE: process.execPath });
  }
});
